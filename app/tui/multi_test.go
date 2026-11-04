package tui

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/dustinevan/scrunchie/lib/data/connections"
	"github.com/dustinevan/scrunchie/lib/data/postgres"
	"github.com/dustinevan/scrunchie/pkg/result"
)

// offlineConnection adds an open connection without a session and binds the
// current buffer to it. It stands for a previous target whose replies the
// test delivers itself; nothing is ever sent through it.
func offlineConnection(m *Model) *connection {
	m.nextConnection++
	c := &connection{id: m.nextConnection, name: fmt.Sprintf("offline %d", m.nextConnection), description: "offline", connected: true, txStatus: 'I'}
	m.connections = append(m.connections, c)
	m.buffers[m.bufferIndex].conn = c.id
	return c
}

// anyRunning reports a query or write-mode request on any connection.
func anyRunning(m *Model) bool {
	for _, c := range m.connections {
		if c.query.running {
			return true
		}
	}
	return false
}

// anyCatalogBusy reports catalog work on any connection.
func anyCatalogBusy(m *Model) bool {
	for _, c := range m.connections {
		if c.catalogBusy {
			return true
		}
	}
	return false
}

// labeledRows is one text column whose cells start with label.
func labeledRows(label string, rows int) result.Batch {
	set := result.Set{Command: fmt.Sprintf("SELECT %d", rows), RowCount: int64(rows), Columns: []result.Column{{Name: "label", Type: "text", OID: 25}}}
	for i := 0; i < rows; i++ {
		set.Rows = append(set.Rows, []result.Cell{{Text: fmt.Sprintf("%s %d", label, i)}})
	}
	return result.Batch{Connected: true, TxStatus: 'I', Results: []result.Set{set}}
}

// finishOn delivers a completed query on c started from the current buffer.
func finishOn(m *Model, c *connection, sql string, batch result.Batch) {
	m.serial++
	c.query = queryState{running: true, serial: m.serial, sql: sql, buffer: m.buffers[m.bufferIndex].id, started: time.Now()}
	m.Update(queryMsg{conn: c.id, serial: m.serial, batch: batch})
}

func header(m *Model) string {
	return ansi.Strip(strings.Split(m.View().Content, "\n")[0])
}

func TestQueriesOnTwoConnectionsKeepResultsWithTheirBuffers(t *testing.T) {
	m := New(Config{})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	a := offlineConnection(m)
	m.buffers[0].editor = newEditor("SELECT 'a';")
	m.addBuffer("SELECT 'b';", "", true, 0)
	b := offlineConnection(m)
	m.switchBuffer(0)
	m.prepareView()
	m.runSQL(true)
	m.switchBuffer(1)
	m.prepareView()
	m.runSQL(true)
	defer m.Cancel()
	if !a.query.running || !b.query.running || a.query.buffer != m.buffers[0].id || b.query.buffer != m.buffers[1].id {
		t.Fatal("queries on two connections did not run at the same time")
	}
	if !strings.Contains(header(m), "Ctrl+C cancel") {
		t.Fatalf("header hides the current buffer's query: %q", header(m))
	}
	aSerial := a.query.serial
	m.Update(queryMsg{conn: b.id, serial: b.query.serial, batch: labeledRows("from b", 5)})
	m.results().row = 3
	m.Update(queryMsg{conn: a.id, serial: aSerial, batch: labeledRows("from a", 2)})
	if m.bufferIndex != 1 || m.results().batch.Results[0].Rows[0][0].Text != "from b 0" || m.results().row != 3 {
		t.Fatal("another buffer's reply replaced or moved the current buffer's results")
	}
	if m.status != "Query finished on offline 1 · SQL 1" || len(m.history) != 2 || m.history[1].connName != "offline 1" {
		t.Fatalf("background completion notice %q, history %d", m.status, len(m.history))
	}
	if got := m.buffers[0].results.batch.Results[0].Rows[1][0].Text; got != "from a 1" || m.buffers[0].results.conn != a.id {
		t.Fatalf("the buffer that ran the query got %q", got)
	}
	if view := screen(m); !strings.Contains(view, "from b 3") || strings.Contains(view, "from a") {
		t.Fatal("results pane mixed buffers")
	}
	m.switchBuffer(0)
	m.prepareView()
	if view := screen(m); !strings.Contains(view, "from a 1") || strings.Contains(view, "from b") || m.results().row != 0 {
		t.Fatal("switching buffers did not show that buffer's results")
	}
	if !strings.Contains(m.status, "2 rows") || m.statusError {
		t.Fatalf("switching did not show the buffer's run status: %q", m.status)
	}
	m.switchBuffer(1)
	m.prepareView()
	if m.results().row != 3 || m.results().batch.Results[0].Rows[3][0].Text != "from b 3" {
		t.Fatal("returning to a buffer lost its grid position")
	}
	// Closing a buffer frees its results; the other buffer keeps its own.
	m.closeBuffer()
	if len(m.buffers) != 1 || m.buffers[0].results.batch.Results[0].Rows[0][0].Text != "from a 0" {
		t.Fatal("closing a buffer disturbed another buffer's results")
	}
}

func TestControlCAndRunTargetTheCurrentBuffersConnection(t *testing.T) {
	m := New(Config{})
	a := offlineConnection(m)
	m.addBuffer("SELECT 2;", "", true, 0)
	b := offlineConnection(m)
	ctxA, cancelA := context.WithCancel(context.Background())
	defer cancelA()
	ctxB, cancelB := context.WithCancel(context.Background())
	defer cancelB()
	a.query = queryState{running: true, cancel: cancelA, serial: 1, buffer: m.buffers[0].id}
	b.query = queryState{running: true, cancel: cancelB, serial: 2, buffer: m.buffers[1].id}
	press(m, 'c', tea.ModCtrl)
	if ctxB.Err() == nil || ctxA.Err() != nil || !b.query.canceling || a.query.canceling {
		t.Fatal("Ctrl+C did not cancel exactly the current buffer's connection")
	}
	if cmd := m.runSQL(false); cmd != nil || !strings.Contains(m.status, "already running") {
		t.Fatalf("Run on a busy connection was not refused: %q", m.status)
	}
	if m.closeBuffer(); len(m.buffers) != 2 || !strings.Contains(m.status, "before closing") {
		t.Fatal("a buffer closed while its query ran, orphaning the results")
	}
	// A third connection runs while both others are busy.
	m.addBuffer("SELECT 3;", "", true, 0)
	c := offlineConnection(m)
	if cmd := m.runSQL(false); cmd == nil || !c.query.running || c.query.sql != "SELECT 3;" {
		t.Fatal("a busy connection blocked a query on another one")
	}
	defer m.Cancel()
	// A buffer whose connection runs another buffer's query cannot start one.
	m.addBuffer("SELECT 4;", "", true, a.id)
	if cmd := m.runSQL(false); cmd != nil || !strings.Contains(m.status, "busy with another buffer") {
		t.Fatalf("a second query on one connection was not refused: %q", m.status)
	}
}

func TestDisconnectAsksFirstThenUnbindsBuffersAndDropsLateReplies(t *testing.T) {
	m := New(Config{})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	a := offlineConnection(m)
	m.addBuffer("SELECT 1;", "", true, a.id)
	finishOn(m, a, "SELECT 1;", labeledRows("kept", 2))
	m.addBuffer("", "", true, 0)
	b := offlineConnection(m)
	a.txStatus = 'T'
	m.focusPane(catalogPane)
	m.selectDatabaseRow(connectionRowIndex(m.databaseRows(), a.id))
	press(m, 'x')
	if m.modal.kind != "confirm-disconnect" || m.modal.index != 0 || !strings.Contains(m.modal.message, "roll back its open transaction") {
		t.Fatalf("disconnecting a transaction did not ask first: %q %q", m.modal.kind, m.modal.message)
	}
	press(m, tea.KeyEnter)
	if m.modal.kind != "" || m.connectionFor(a.id) == nil || !strings.Contains(m.status, "still connected") {
		t.Fatal("the default choice disconnected")
	}
	press(m, 'x')
	press(m, tea.KeyRight)
	press(m, tea.KeyEnter)
	if m.connectionFor(a.id) != nil || len(m.connections) != 1 || m.buffers[0].conn != 0 || m.buffers[1].conn != 0 || m.buffers[2].conn != b.id {
		t.Fatal("disconnect did not close exactly that connection and unbind its buffers")
	}
	if !strings.Contains(m.status, "2 buffers now without a connection") {
		t.Fatalf("disconnect status %q", m.status)
	}
	m.switchBuffer(1)
	m.prepareView()
	view := screen(m)
	if !strings.Contains(header(m), "no connection") || !strings.Contains(view, "SQL 2 · no connection") || !strings.Contains(view, "kept 1") || !strings.Contains(view, "from offline 1 · connection closed") {
		t.Fatalf("an unbound buffer lost its results or label:\n%s", view)
	}
	if cmd := m.runSQL(false); cmd != nil || !m.statusError || !strings.Contains(m.status, "Ctrl+P → Change buffer connection") {
		t.Fatalf("Run without a connection did not explain the choice: %q", m.status)
	}
	history := len(m.history)
	m.Update(queryMsg{conn: a.id, serial: m.serial, batch: labeledRows("late", 1), err: context.Canceled})
	m.Update(catalogMsg{conn: a.id, serial: m.serial, parent: -1, nodes: []catalogNode{{kind: "schema", name: "late", parent: -1}}})
	if len(m.history) != history || strings.Contains(screen(m), "late") || m.connectionFor(a.id) != nil {
		t.Fatal("a reply for the closed connection changed the workspace")
	}
	// Without a transaction or query, x closes at once.
	m.focusPane(catalogPane)
	m.selectDatabaseRow(connectionRowIndex(m.databaseRows(), b.id))
	press(m, 'x')
	if m.modal.kind != "" || len(m.connections) != 0 {
		t.Fatal("an idle connection asked before disconnecting")
	}
}

func TestQuitConfirmsOnceForEveryConnectionAtRisk(t *testing.T) {
	m := New(Config{})
	a := offlineConnection(m)
	a.txStatus = 'E'
	m.addBuffer("", "", true, 0)
	b := offlineConnection(m)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b.query = queryState{running: true, cancel: cancel, serial: 5, buffer: m.buffers[1].id}
	m.addBuffer("", "", true, 0)
	idle := offlineConnection(m)
	press(m, 'q', tea.ModCtrl)
	if m.modal.kind != "confirm-quit" || m.modal.index != 0 || !strings.Contains(m.modal.message, "offline 1: failed transaction will roll back") ||
		!strings.Contains(m.modal.message, "offline 2: running query will be canceled") || strings.Contains(m.modal.message, idle.name) {
		t.Fatalf("quit confirmation %q: %q", m.modal.kind, m.modal.message)
	}
	press(m, tea.KeyEnter)
	if m.modal.kind != "" || m.quitIntent || ctx.Err() != nil {
		t.Fatal("Stay quit or canceled the query")
	}
	press(m, 'q', tea.ModCtrl)
	press(m, tea.KeyRight)
	if cmd := press(m, tea.KeyEnter); cmd != nil || ctx.Err() == nil || !strings.Contains(m.status, "Canceling running queries") {
		t.Fatalf("confirmed quit did not cancel and wait for the query: %q", m.status)
	}
	_, cmd := m.Update(queryMsg{conn: b.id, serial: 5, err: context.Canceled})
	if cmd == nil {
		t.Fatal("quit did not continue after the canceled query returned")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("quit did not finish")
	}
}

func TestHeaderTabsAndWriteModeFollowTheCurrentBuffer(t *testing.T) {
	m := New(Config{})
	m.Update(tea.WindowSizeMsg{Width: 140, Height: 24})
	a := offlineConnection(m)
	a.session, a.description, a.writable, a.txStatus = &postgres.Session{}, "alpha@example/alpha", true, 'T'
	m.addBuffer("", "", true, 0)
	b := offlineConnection(m)
	b.session, b.description = &postgres.Session{}, "beta@example/beta"
	if h := header(m); !strings.Contains(h, "read-only") || !strings.Contains(h, "beta@example/beta") || strings.Contains(h, "WRITES ON") || strings.Contains(h, "TRANSACTION") {
		t.Fatalf("read-only buffer header %q", h)
	}
	m.switchBuffer(0)
	m.prepareView()
	if h := header(m); !strings.Contains(h, "WRITES ON") || !strings.Contains(h, "alpha@example/alpha") || !strings.Contains(h, "IN TRANSACTION") {
		t.Fatalf("writable buffer header %q", h)
	}
	if view := screen(m); !strings.Contains(view, "SQL 1 · offline 1") || !strings.Contains(view, "SQL 2 · offline 2") {
		t.Fatalf("tabs lack connection labels:\n%s", view)
	}
	if !strings.Contains(screen(m), "● offline 1 [writes] [tx]") {
		t.Fatalf("database pane lacks connection markers:\n%s", screen(m))
	}
	// The write-mode commands act on the current buffer's connection.
	m.switchBuffer(1)
	m.prepareView()
	m.action("allow-writes")
	if m.modal.kind != "confirm-writes" || m.modal.target != b.id || !strings.Contains(m.modal.message, "beta@example/beta") {
		t.Fatalf("Allow writes named %q", m.modal.message)
	}
	press(m, tea.KeyEsc)
	m.switchBuffer(0)
	m.prepareView()
	if cmd := m.action("read-only"); cmd != nil || !strings.Contains(m.status, "transaction") {
		t.Fatal("read-only did not check the current connection's transaction")
	}
	m.addBuffer("", "", true, 0)
	if h := header(m); !strings.Contains(h, "SQL 3 · no connection") || strings.Contains(h, "read-only") || strings.Contains(h, "WRITES ON") {
		t.Fatalf("unbound buffer header %q", h)
	}
}

func TestCatalogTreesAndTheirActionsBelongToTheirConnection(t *testing.T) {
	m := New(Config{})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	a := offlineConnection(m)
	a.session = &postgres.Session{}
	alpha := postgres.Relation{OID: 1, Schema: "public", Name: "alpha_table", Kind: "table"}
	a.catalog = []catalogNode{
		{kind: "schema", name: "public", schema: "public", parent: -1, children: []int{1}, expanded: true, loaded: true},
		{kind: "relation", name: "alpha_table", schema: "public", relation: alpha, parent: 0, loaded: true},
	}
	m.addBuffer("", "", true, 0)
	b := offlineConnection(m)
	b.session = &postgres.Session{}
	b.catalog = []catalogNode{{kind: "schema", name: "public", schema: "public", parent: -1}}
	rows := m.databaseRows()
	if len(rows) != 6 || rows[1].conn != a.id || rows[3].kind != databaseCatalog || rows[3].conn != a.id || rows[4].conn != b.id || rows[5].conn != b.id {
		t.Fatalf("rows %+v", rows)
	}
	// Expanding b's schema while a runs SQL does not wait for a.
	a.query.running = true
	m.focusPane(catalogPane)
	m.selectDatabaseRow(5)
	if cmd := press(m, tea.KeyEnter); cmd == nil || !b.catalogBusy || a.catalogBusy || len(a.catalogQueue) != 0 {
		t.Fatal("b's catalog request waited for a or ran on a")
	}
	beta := postgres.Relation{OID: 2, Schema: "public", Name: "beta_table", Kind: "table"}
	m.Update(catalogMsg{conn: a.id, serial: b.catalogSerial, parent: 0, nodes: []catalogNode{{kind: "relation", name: "wrong", relation: beta}}})
	m.Update(catalogMsg{conn: b.id, serial: b.catalogSerial, parent: 0, nodes: []catalogNode{{kind: "relation", name: "beta_table", schema: "public", relation: beta, loaded: true}}})
	if len(a.catalog) != 2 || len(b.catalog) != 2 || b.catalog[1].name != "beta_table" || b.catalogBusy {
		t.Fatal("a catalog reply reached the wrong connection")
	}
	// Prepared SQL is bound to the row's connection, not the current buffer's.
	m.selectDatabaseRow(3)
	press(m, 'p')
	if c := m.conn(); c != a || !strings.Contains(string(m.buffers[m.bufferIndex].editor.text), "alpha_table") {
		t.Fatal("preview of a's table did not open on a")
	}
	m.focusPane(catalogPane)
	m.selectDatabaseRow(connectionRowIndex(m.databaseRows(), b.id) + 2)
	m.action("count")
	if c := m.conn(); c != b || !strings.Contains(string(m.buffers[m.bufferIndex].editor.text), "beta_table") {
		t.Fatal("count of b's table did not open on b")
	}
	// Structure reads use the row's connection; a's are queued behind its SQL.
	m.focusPane(catalogPane)
	m.selectDatabaseRow(connectionRowIndex(m.databaseRows(), b.id) + 2)
	if cmd := press(m, 'd'); cmd == nil || m.structure.conn != b.id || !b.catalogBusy {
		t.Fatal("structure did not load from b")
	}
	press(m, tea.KeyEsc)
	m.selectDatabaseRow(3)
	if cmd := press(m, 'd'); cmd != nil || m.structure.conn != a.id || !m.structure.queued || a.catalogBusy {
		t.Fatal("structure on a did not wait for a's SQL")
	}
	m.Update(queryMsg{conn: a.id, serial: a.query.serial, batch: result.Batch{Connected: true, TxStatus: 'I'}})
	if !a.catalogBusy || m.structure.queued || m.structure.serial != a.catalogSerial {
		t.Fatal("a's finished SQL did not start its queued structure request")
	}
	// Completion uses the current buffer's connection's names.
	press(m, tea.KeyEsc)
	m.switchBuffer(connectionBuffer(m, b))
	m.prepareView()
	m.buffers[m.bufferIndex].editor = newEditor("SELECT * FROM ")
	m.buffers[m.bufferIndex].editor.cursor = len("SELECT * FROM ")
	m.focusPane(editorPane)
	m.completeWord(true)
	if !strings.Contains(strings.Join(m.completion.items, ","), "beta_table") || strings.Contains(strings.Join(m.completion.items, ","), "alpha_table") {
		t.Fatalf("completion on b offered %q", m.completion.items)
	}
}

// connectionBuffer is the index of the latest buffer bound to c.
func connectionBuffer(m *Model, c *connection) int {
	best := -1
	for i, b := range m.buffers {
		if b.conn == c.id && (best < 0 || b.used > m.buffers[best].used) {
			best = i
		}
	}
	return best
}

func TestChangeBufferConnectionRebindsAndWaitsForItsQuery(t *testing.T) {
	m := New(Config{})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	a := offlineConnection(m)
	m.addBuffer("SELECT 1;", "", true, 0)
	b := offlineConnection(m)
	m.action("buffer-connection")
	if m.modal.kind != "buffer-connection" || m.modal.index != 1 {
		t.Fatalf("chooser %q index %d", m.modal.kind, m.modal.index)
	}
	if view := screen(m); !strings.Contains(view, "● offline 1") || !strings.Contains(view, "● offline 2") || !strings.Contains(view, "No connection") {
		t.Fatalf("chooser lists:\n%s", view)
	}
	press(m, tea.KeyUp)
	press(m, tea.KeyEnter)
	if m.modal.kind != "" || m.buffers[1].conn != a.id || !strings.Contains(m.status, "now uses offline 1") {
		t.Fatalf("rebinding failed: %q", m.status)
	}
	m.action("buffer-connection")
	press(m, tea.KeyDown)
	press(m, tea.KeyDown)
	press(m, tea.KeyEnter)
	if m.buffers[1].conn != 0 || !strings.Contains(header(m), "no connection") {
		t.Fatal("choosing No connection did not unbind the buffer")
	}
	m.buffers[1].conn = b.id
	m.runSQL(true)
	defer m.Cancel()
	m.action("buffer-connection")
	if m.modal.kind != "" || m.buffers[1].conn != b.id || !strings.Contains(m.status, "is running a query") {
		t.Fatal("a buffer changed connection while its query ran")
	}
}

func TestEnterOnAnOpenConnectionSwitchesToItsLatestBuffer(t *testing.T) {
	m := New(Config{})
	a := offlineConnection(m)
	m.addBuffer("", "", true, a.id)
	m.addBuffer("", "", true, 0)
	b := offlineConnection(m)
	m.focusPane(catalogPane)
	m.selectDatabaseRow(connectionRowIndex(m.databaseRows(), a.id))
	press(m, tea.KeyEnter)
	if m.bufferIndex != 1 || m.conn() != a || m.focus != catalogPane || a.collapsed {
		t.Fatalf("Enter switched to buffer %d", m.bufferIndex)
	}
	press(m, tea.KeyEnter)
	if !a.collapsed || m.bufferIndex != 1 {
		t.Fatal("Enter on the current connection did not toggle its tree")
	}
	m.selectDatabaseRow(connectionRowIndex(m.databaseRows(), b.id))
	press(m, tea.KeyEnter)
	if m.bufferIndex != 2 || m.conn() != b {
		t.Fatal("Enter did not return to b's buffer")
	}
	// A connection without buffers gets a new one.
	m.nextConnection++
	c := &connection{id: m.nextConnection, name: "unused", connected: true, txStatus: 'I'}
	m.connections = append(m.connections, c)
	m.selectDatabaseRow(connectionRowIndex(m.databaseRows(), c.id))
	press(m, tea.KeyEnter)
	if len(m.buffers) != 4 || m.bufferIndex != 3 || m.conn() != c || m.focus != catalogPane {
		t.Fatal("Enter on a connection without buffers did not open one")
	}
	// Left at SQL offset zero selects the current buffer's connection row.
	m.switchBuffer(1)
	m.prepareView()
	m.focusPane(editorPane)
	press(m, tea.KeyLeft)
	if rows := m.databaseRows(); rows[m.databasePane.index].conn != a.id || rows[m.databasePane.index].kind != databaseConnection {
		t.Fatal("Left at offset zero did not select this buffer's connection")
	}
}

func TestNewConnectionBindsAnEmptyBufferOrOpensItsOwn(t *testing.T) {
	m := New(Config{})
	// The empty sessions are never queried; their catalog commands are not run.
	m.Update(reconnectMsg{session: &postgres.Session{}, dsn: "host=first.invalid"})
	if len(m.connections) != 1 || len(m.buffers) != 1 || m.buffers[0].conn != m.connections[0].id {
		t.Fatal("a new connection did not bind the empty buffer")
	}
	first := m.connections[0]
	m.buffers[0].editor.insert("SELECT 1;", "")
	m.Update(reconnectMsg{session: &postgres.Session{}, dsn: "host=second.invalid"})
	if len(m.connections) != 2 || len(m.buffers) != 2 || m.bufferIndex != 1 || m.buffers[0].conn != first.id || m.buffers[1].conn != m.connections[1].id {
		t.Fatal("a buffer with SQL was rebound instead of opening a new one")
	}
	if !strings.Contains(m.status, "SQL 2 runs on it") {
		t.Fatalf("connect status %q", m.status)
	}
}

func TestEditingAnOpenProfileRelabelsItsConnection(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".scrunch")
	profile := paneProfiles(1)[0]
	m := New(Config{Profiles: []connections.Profile{profile}, ProfilesPath: path})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	c := offlineConnection(m)
	c.profile, c.name = profile, profile.Name
	if rows := m.databaseRows(); len(rows) != 2 || rows[1].kind != databaseConnection {
		t.Fatalf("an open profile was also listed as saved: %+v", rows)
	}
	m.focusPane(catalogPane)
	m.selectDatabaseRow(1)
	press(m, 'e')
	if m.modal.kind != "connection" || m.modal.title != "Edit database" {
		t.Fatalf("e opened %q %q", m.modal.kind, m.modal.title)
	}
	m.connectionForm.fields[connectionName] = newEditor("Renamed reports")
	m.connectionForm.fields[connectionHost] = newEditor("elsewhere.invalid")
	m.connectionForm.index = connectionSave
	finishCommands(t, m, press(m, tea.KeyEnter))
	rows := m.databaseRows()
	pane := ansi.Strip(m.databasePaneView(40, 10))
	if len(rows) != 2 || c.name != "Renamed reports" || c.profile.Host != "elsewhere.invalid" || strings.Contains(pane, "○") || !strings.Contains(pane, "● Renamed reports") {
		t.Fatalf("saving an open profile listed it twice or kept its old label:\n%s", pane)
	}
	if !strings.Contains(m.status, "uses the new settings after Reconnect") || len(m.connections) != 1 {
		t.Fatalf("save status %q", m.status)
	}
	saved, err := connections.Load(path)
	if err != nil || len(saved) != 1 || saved[0].Name != "Renamed reports" {
		t.Fatalf("saved %v %v", saved, err)
	}
}

func TestDatabaseNamePickerReturnsToTheFormsOwnTitle(t *testing.T) {
	m := New(Config{Profiles: paneProfiles(1)})
	for _, profile := range []connections.Profile{m.profiles[0], {Host: "new.invalid", Database: "postgres"}} {
		m.openConnectionForm(profile)
		title := m.modal.title
		for _, key := range []rune{tea.KeyEsc, tea.KeyEnter} {
			m.Update(databaseNamesMsg{serial: m.discoverySerial, names: []string{"alpha", "beta"}})
			if m.modal.kind != "database-names" {
				t.Fatal("database names did not open the picker")
			}
			press(m, key)
			if m.modal.kind != "connection" || m.modal.title != title {
				t.Fatalf("picker returned to %q titled %q, want %q", m.modal.kind, m.modal.title, title)
			}
		}
	}
}

func TestHistoryNamesEachConnectionAndReopensOnIt(t *testing.T) {
	m := New(Config{})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	a := offlineConnection(m)
	finishOn(m, a, "SELECT 'on a';", labeledRows("a", 1))
	m.addBuffer("", "", true, 0)
	b := offlineConnection(m)
	finishOn(m, b, "SELECT 'on b';", labeledRows("b", 1))
	press(m, tea.KeyF8)
	if view := screen(m); !strings.Contains(view, "offline 1") || !strings.Contains(view, "offline 2") {
		t.Fatalf("history lacks connection names:\n%s", view)
	}
	m.modal.input = newEditor("offline 1")
	m.prepareView()
	if len(m.modal.historyRows) != 1 || m.history[m.modal.historyRows[0]].sql != "SELECT 'on a';" {
		t.Fatal("history filter did not match a connection name")
	}
	press(m, tea.KeyEsc)
	m.switchBuffer(1)
	m.prepareView()
	press(m, tea.KeyF8)
	press(m, tea.KeyDown)
	press(m, tea.KeyEnter)
	if len(m.buffers) != 3 || m.conn() != a || string(m.buffers[2].editor.text) != "SELECT 'on a';" || anyRunning(m) {
		t.Fatal("history did not reopen SQL on its connection without running it")
	}
	m.disconnect(b)
	press(m, tea.KeyF8)
	press(m, tea.KeyEnter)
	if len(m.buffers) != 4 || m.buffers[3].conn != 0 || !strings.Contains(m.status, "offline 2 is closed") {
		t.Fatalf("history of a closed connection: %q", m.status)
	}
}

func TestFetchMoreAndConfirmationsStayOnTheirConnection(t *testing.T) {
	m := New(Config{})
	a := offlineConnection(m)
	m.serial++
	a.query = queryState{running: true, serial: m.serial, sql: "SELECT id FROM t", buffer: m.buffers[0].id, page: firstPageRows}
	m.Update(queryMsg{conn: a.id, serial: m.serial, batch: result.Batch{Connected: true, TxStatus: 'I', Results: []result.Set{pagedSet(1000, true)}}})
	if m.results().origin.conn != a.id {
		t.Fatal("results did not record their connection")
	}
	b := offlineConnection(m)
	m.focus = resultsPane
	if cmd := press(m, 'm'); cmd != nil || a.query.running || b.query.running || !strings.Contains(m.status, "came from offline 1") {
		t.Fatalf("fetch more ran on another connection: %q", m.status)
	}
	// Unfiltered-write confirmation re-checks the buffer's connection.
	m.buffers[0].editor = newEditor("delete from audit_log;")
	m.focus = editorPane
	m.runSQL(true)
	if m.modal.kind != "confirm-unfiltered" || m.pendingRun.conn != b.id {
		t.Fatal("unfiltered SQL did not ask first")
	}
	m.buffers[0].conn = a.id
	m.modal.index = 1
	press(m, tea.KeyEnter)
	if a.query.running || b.query.running || !strings.Contains(m.status, "changed before confirmation") {
		t.Fatalf("confirmed SQL ran after its buffer changed connection: %q", m.status)
	}
}

func TestManagerMarksOpenProfilesAndSwitchesToThem(t *testing.T) {
	profiles := paneProfiles(2)
	m := New(Config{Profiles: profiles})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	c := offlineConnection(m)
	c.profile, c.name = profiles[1], profiles[1].Name
	m.addBuffer("SELECT 'elsewhere';", "", true, 0)
	press(m, 'b', tea.ModCtrl)
	view := screen(m)
	if !strings.Contains(view, "Reports 1") || strings.Count(view, "[open]") != 1 {
		t.Fatalf("manager does not mark the open profile:\n%s", view)
	}
	press(m, tea.KeyDown)
	if press(m, tea.KeyEnter); m.connecting || m.reconnectAttempt != nil || len(m.connections) != 1 || m.conn() != c || m.bufferIndex != 0 || m.modal.kind != "" {
		t.Fatalf("Enter on an open profile did not switch to it: %q", m.status)
	}
	if !strings.Contains(m.status, "already open") {
		t.Fatalf("switch status %q", m.status)
	}
}

func TestTwoConnectionViewsFitEveryTerminalSize(t *testing.T) {
	m := New(Config{})
	a := offlineConnection(m)
	a.session, a.writable, a.txStatus = &postgres.Session{}, true, 'T'
	a.catalog = []catalogNode{{kind: "schema", name: "public", schema: "public", parent: -1}}
	finishOn(m, a, "SELECT 1;", labeledRows("a", 3))
	m.addBuffer("SELECT 2;", "", true, 0)
	b := offlineConnection(m)
	b.name = "a rather long connection name for narrow panes"
	b.query = queryState{running: true, serial: 99, buffer: m.buffers[1].id, started: time.Now()}
	for _, size := range [][2]int{{1, 1}, {20, 6}, {40, 12}, {68, 18}, {100, 30}, {160, 50}} {
		for _, state := range []string{"editor", "catalog", "chooser", "quit"} {
			m.modal = modal{}
			m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			switch state {
			case "catalog":
				m.focusPane(catalogPane)
			case "chooser":
				m.switchBuffer(0)
				m.openBufferConnections()
			case "quit":
				m.confirmQuit()
			}
			m.prepareView()
			lines := strings.Split(m.View().Content, "\n")
			if len(lines) != size[1] {
				t.Fatalf("%s at %dx%d rendered %d lines", state, size[0], size[1], len(lines))
			}
			for _, line := range lines {
				if ansi.StringWidth(line) != size[0] {
					t.Fatalf("%s at %dx%d rendered a %d-cell line: %q", state, size[0], size[1], ansi.StringWidth(line), ansi.Strip(line))
				}
			}
		}
	}
}

func TestConnectingAnOpenProfileFromTheFormSwitchesAndDefersSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".scrunch")
	profile := paneProfiles(1)[0]
	m := New(Config{Profiles: []connections.Profile{profile}, ProfilesPath: path})
	c := offlineConnection(m)
	c.profile, c.name = profile, profile.Name
	m.addBuffer("SELECT 'other buffer';", "", true, 0)
	m.openConnectionForm(profile)
	m.connectionForm.fields[connectionDatabase] = newEditor("reports_next")
	m.connectionForm.index = connectionConnect
	finishCommands(t, m, press(m, tea.KeyEnter))
	if m.connecting || m.reconnectAttempt != nil || len(m.connections) != 1 || m.conn() != c || m.modal.kind != "" {
		t.Fatalf("Connect on an open profile opened another connection: %q", m.status)
	}
	if c.profile.Database != "reports_next" || !strings.Contains(m.status, "already open") || !strings.Contains(m.status, "apply on Reconnect") || !strings.Contains(m.status, "settings saved") {
		t.Fatalf("status %q, database %q", m.status, c.profile.Database)
	}
	if saved, err := connections.Load(path); err != nil || len(saved) != 1 || saved[0].Database != "reports_next" {
		t.Fatalf("saved %v %v", saved, err)
	}
}

func TestBackgroundConnectLeavesOpenDialogsAndViewsAlone(t *testing.T) {
	m := New(Config{})
	a := offlineConnection(m)
	a.txStatus = 'T'
	m.focusPane(catalogPane)
	m.selectDatabaseRow(connectionRowIndex(m.databaseRows(), a.id))
	press(m, 'x')
	if m.modal.kind != "confirm-disconnect" {
		t.Fatal("setup: no confirmation")
	}
	// A pane-initiated connect finishes while the user decides.
	m.Update(reconnectMsg{session: &postgres.Session{}, dsn: "host=late.invalid"})
	if m.modal.kind != "confirm-disconnect" || m.modal.target != a.id || m.bufferIndex != 0 || m.focus != catalogPane {
		t.Fatalf("a finished connect replaced the dialog or moved the user: %q", m.modal.kind)
	}
	if len(m.connections) != 2 || len(m.buffers) != 2 || m.buffers[1].conn != m.connections[1].id || !strings.Contains(m.status, "SQL 2 is ready for it") {
		t.Fatalf("the new connection got no buffer: %q", m.status)
	}
	press(m, tea.KeyEnter)
	if m.connectionFor(a.id) == nil {
		t.Fatal("the user's Cancel was lost")
	}
	// A failed profile connect reports in the status instead of replacing a dialog.
	m.openBufferConnections()
	profile := paneProfiles(1)[0]
	m.Update(reconnectMsg{profile: &profile, err: fmt.Errorf("synthetic failure")})
	if m.modal.kind != "buffer-connection" || !strings.Contains(m.status, "synthetic failure") {
		t.Fatalf("a failed connect replaced the chooser: %q", m.modal.kind)
	}
	// Connections that open while the chooser is up do not shift its rows.
	m.Update(reconnectMsg{session: &postgres.Session{}, dsn: "host=third.invalid"})
	if len(m.modal.choices) != 3 {
		t.Fatalf("chooser lists %v", m.modal.choices)
	}
	press(m, tea.KeyDown)
	press(m, tea.KeyDown)
	press(m, tea.KeyEnter)
	if m.buffers[0].conn != 0 || len(m.connections) != 3 {
		t.Fatal("choosing No connection bound the buffer to a connection that opened meanwhile")
	}
	// The inspector stays open too.
	m.buffers[0].results.batch = labeledRows("cell", 1)
	m.focusPane(resultsPane)
	m.prepareView()
	press(m, tea.KeyEnter)
	if !m.inspector.active {
		t.Fatal("setup: inspector did not open")
	}
	m.Update(reconnectMsg{session: &postgres.Session{}, dsn: "host=fourth.invalid"})
	if !m.inspector.active || m.bufferIndex != 0 {
		t.Fatal("a finished connect closed the inspector")
	}
}

func TestControlCPrefersTheCurrentQueryOverABackgroundConnect(t *testing.T) {
	m := New(Config{})
	c := offlineConnection(m)
	queryCtx, cancelQuery := context.WithCancel(context.Background())
	defer cancelQuery()
	connectCtx, cancelConnect := context.WithCancel(context.Background())
	defer cancelConnect()
	c.query = queryState{running: true, cancel: cancelQuery, serial: 1, buffer: m.buffers[0].id}
	m.connecting, m.connectCancel = true, cancelConnect
	press(m, 'c', tea.ModCtrl)
	if queryCtx.Err() == nil || connectCtx.Err() != nil {
		t.Fatal("Ctrl+C canceled the background connect instead of this buffer's query")
	}
	c.query = queryState{}
	press(m, 'c', tea.ModCtrl)
	if connectCtx.Err() == nil {
		t.Fatal("Ctrl+C with an idle buffer did not cancel the connect")
	}
	// With the connection form open, Ctrl+C belongs to the connect.
	connectCtx, cancelConnect = context.WithCancel(context.Background())
	defer cancelConnect()
	queryCtx, cancelQuery = context.WithCancel(context.Background())
	defer cancelQuery()
	c.query = queryState{running: true, cancel: cancelQuery, serial: 2, buffer: m.buffers[0].id}
	m.connectCancel = cancelConnect
	m.openConnectionForm(connections.Profile{Host: "x.invalid", Database: "x"})
	press(m, 'c', tea.ModCtrl)
	if connectCtx.Err() == nil || queryCtx.Err() != nil {
		t.Fatal("Ctrl+C in the connection form did not cancel the connect")
	}
}

func TestQueuedCatalogWorkResumesWhenAQuitPromptIsDeclined(t *testing.T) {
	m := New(Config{})
	c := offlineConnection(m)
	c.session = &postgres.Session{}
	c.catalog = []catalogNode{{kind: "schema", name: "queued", schema: "queued", parent: -1}}
	c.query = queryState{running: true, serial: 7, buffer: m.buffers[0].id}
	m.focusPane(catalogPane)
	m.selectDatabaseRow(2)
	press(m, tea.KeyEnter)
	if len(c.catalogQueue) != 1 || c.catalogBusy {
		t.Fatal("setup: expansion did not queue behind the query")
	}
	press(m, 'q', tea.ModCtrl)
	if m.modal.kind != "confirm-quit" {
		t.Fatal("setup: no quit confirmation")
	}
	m.Update(queryMsg{conn: c.id, serial: 7, batch: result.Batch{Connected: true, TxStatus: 'I'}})
	if !c.catalogBusy || len(c.catalogQueue) != 0 || m.modal.kind != "confirm-quit" {
		t.Fatal("queued catalog work stalled behind the quit prompt")
	}
	press(m, tea.KeyEnter)
	if m.quitIntent || m.modal.kind != "" {
		t.Fatal("Stay did not stay")
	}
}
