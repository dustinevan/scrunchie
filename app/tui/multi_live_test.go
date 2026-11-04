package tui

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"github.com/dustinevan/scrunchie/lib/data/connections"
	"github.com/dustinevan/scrunchie/lib/data/postgres"
	"github.com/dustinevan/scrunchie/pkg/result"
)

// otherProfile is the saved entry for the second isolated database.
func otherProfile(t *testing.T) connections.Profile {
	t.Helper()
	_, profile := testConnection(t)
	other := os.Getenv("SCRUNCH_TEST_OTHER_DATABASE")
	if other == "" {
		t.Skip("SCRUNCH_TEST_OTHER_DATABASE must name the second isolated database")
	}
	profile.ID, profile.Name, profile.Database = "other", "Other database", other
	return profile
}

// controlOther opens a writable session on the second isolated database for
// test setup, separate from the workspace's connections.
func controlOther(t *testing.T) *postgres.Session {
	t.Helper()
	profile := otherProfile(t)
	dsn, err := profile.DSN("")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	control, err := postgres.Open(ctx, dsn, postgres.Options{AllowWrites: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := control.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return control
}

func execute(t *testing.T, session *postgres.Session, sql string) result.Batch {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	batch, err := session.Execute(ctx, sql, result.Limits{})
	if err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return batch
}

// startAsync runs a command in the background, as Bubble Tea does, and
// delivers its messages for the test to apply on its own goroutine.
func startAsync(cmd tea.Cmd) <-chan tea.Msg {
	out := make(chan tea.Msg, 8)
	go func() {
		defer close(out)
		queue := []tea.Cmd{cmd}
		for len(queue) > 0 {
			next := queue[0]
			queue = queue[1:]
			if next == nil {
				continue
			}
			switch msg := next().(type) {
			case tea.BatchMsg:
				queue = append(queue, msg...)
			case spinner.TickMsg:
			default:
				out <- msg
			}
		}
	}()
	return out
}

func receive(t *testing.T, messages <-chan tea.Msg) tea.Msg {
	t.Helper()
	select {
	case msg := <-messages:
		return msg
	case <-time.After(15 * time.Second):
		t.Fatal("background command did not finish")
	}
	return nil
}

func cellText(m *Model, set, row, column int) string {
	r := m.results()
	if set >= len(r.batch.Results) || row >= len(r.batch.Results[set].Rows) {
		return ""
	}
	return r.batch.Results[set].Rows[row][column].Text
}

func TestLiveTwoConnectionsKeepTransactionsResultsAndRunConcurrently(t *testing.T) {
	dsn, _ := testConnection(t)
	profile := otherProfile(t)
	session, _ := liveSession(t, true)
	m := New(Config{Session: session, DSN: dsn, AllowWrites: true, ProfilesPath: filepath.Join(t.TempDir(), ".scrunch")})
	closeTestModel(t, m)
	m.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	finishCommands(t, m, m.Init())
	a := m.conn()
	runBuffer(t, m, "BEGIN; CREATE TEMP TABLE multi_marker (n int); INSERT INTO multi_marker VALUES (1);")
	if a.txStatus != 'T' || !a.writable {
		t.Fatalf("setup transaction %c writable %t: %s", a.txStatus, a.writable, m.status)
	}
	finishCommands(t, m, m.connectProfile(profile, "", false))
	b := m.conn()
	if b == nil || b == a || len(m.connections) != 2 || m.bufferIndex != 1 || m.buffers[0].conn != a.id {
		t.Fatalf("the second database did not open beside the first: %s", m.status)
	}
	if h := header(m); !strings.Contains(h, profile.Database) || !strings.Contains(h, "read-only") || strings.Contains(h, "TRANSACTION") || strings.Contains(h, "WRITES ON") {
		t.Fatalf("header of the second connection %q", h)
	}
	runBuffer(t, m, "SELECT current_database(), 41 + 1;")
	if cellText(m, 0, 0, 0) != profile.Database || cellText(m, 0, 0, 1) != "42" || b.txStatus != 'I' || a.txStatus != 'T' {
		t.Fatalf("query on the second connection: %q", m.status)
	}
	m.switchBuffer(0)
	m.prepareView()
	if h := header(m); !strings.Contains(h, "WRITES ON") || !strings.Contains(h, "IN TRANSACTION") {
		t.Fatalf("header of the first connection %q", h)
	}
	if len(m.results().batch.Results) != 3 || m.results().batch.Results[2].Command != "INSERT 0 1" {
		t.Fatal("the first buffer lost its results")
	}
	runBuffer(t, m, "SELECT count(*) FROM multi_marker;")
	if cellText(m, 0, 0, 0) != "1" || a.txStatus != 'T' {
		t.Fatal("the first connection's transaction did not survive the second connection")
	}

	// A slow query on a while a quick one runs on b.
	m.buffers[0].editor = newEditor("SELECT pg_sleep(30);")
	slow := startAsync(m.runSQL(true))
	if !a.query.running {
		t.Fatal("slow query did not start")
	}
	m.switchBuffer(1)
	m.buffers[1].editor = newEditor("SELECT 'b while a runs';")
	finishCommands(t, m, m.runSQL(true))
	if !a.query.running || b.query.running || cellText(m, 0, 0, 0) != "b while a runs" {
		t.Fatalf("b did not run while a was busy: a running %t, status %q", a.query.running, m.status)
	}
	if h := header(m); !strings.Contains(h, "running on scrunch_test") {
		t.Fatalf("header shows no spinner for the other connection's query: %q", h)
	}
	// Ctrl+C here is b's: b is idle, so it copies instead of canceling a.
	m.focus = resultsPane
	if cmd := press(m, 'c', tea.ModCtrl); cmd == nil || a.query.canceling {
		t.Fatal("Ctrl+C on b's buffer touched a's query")
	}
	m.switchBuffer(0)
	press(m, 'c', tea.ModCtrl)
	if !a.query.canceling {
		t.Fatal("Ctrl+C on a's buffer did not cancel a's query")
	}
	_, cmd := m.Update(receive(t, slow))
	finishCommands(t, m, cmd)
	if a.query.running || a.txStatus != 'E' || !strings.Contains(m.results().err, "57014") || b.txStatus != 'I' {
		t.Fatalf("cancel: a tx %c, b tx %c, error %q", a.txStatus, b.txStatus, m.results().err)
	}
	if m.buffers[1].results.batch.Results[0].Rows[0][0].Text != "b while a runs" {
		t.Fatal("canceling a disturbed b's results")
	}
	runBuffer(t, m, "ROLLBACK;")

	// Reconnect replaces only b's session, keeping its id and buffers.
	m.switchBuffer(1)
	before := b.session
	finishCommands(t, m, m.reconnect())
	if b.session == before || m.conn() != b || m.buffers[1].conn != b.id || a.session != session || len(m.connections) != 2 {
		t.Fatalf("reconnect did not keep b's identity or touched a: %s", m.status)
	}
	for _, entry := range m.history {
		want := "scrunch_test"
		if strings.Contains(entry.sql, "current_database") || strings.Contains(entry.sql, "b while a runs") {
			want = profile.Name
		}
		if entry.connName != want {
			t.Fatalf("history labeled %q with %q", entry.sql, entry.connName)
		}
	}
}

func TestLiveDisconnectRollsBackClosesAndDropsItsReply(t *testing.T) {
	profile := otherProfile(t)
	profile.AllowWrites = true
	control := controlOther(t)
	execute(t, control, "CREATE TABLE public.multi_probe (n int);")
	t.Cleanup(func() { execute(t, control, "DROP TABLE public.multi_probe;") })
	m := New(Config{ProfilesPath: filepath.Join(t.TempDir(), ".scrunch")})
	closeTestModel(t, m)
	finishCommands(t, m, m.connectProfile(profile, "", false))
	b := m.conn()
	runBuffer(t, m, "BEGIN; INSERT INTO multi_probe VALUES (1);")
	if b.txStatus != 'T' || m.statusError {
		t.Fatalf("setup: %s", m.status)
	}
	m.buffers[0].editor = newEditor("SELECT pg_sleep(30);")
	slow := startAsync(m.runSQL(true))
	session := b.session
	m.requestDisconnect(b)
	if m.modal.kind != "confirm-disconnect" || !strings.Contains(m.modal.message, "cancel its running query and roll back its open transaction") {
		t.Fatalf("disconnect confirmation %q", m.modal.message)
	}
	m.modal.index = 1
	finishCommands(t, m, m.confirmChoice())
	if len(m.connections) != 0 || m.buffers[0].conn != 0 || len(m.closing) != 0 {
		t.Fatalf("disconnect left the connection or its close pending: %s", m.status)
	}
	history := len(m.history)
	m.Update(receive(t, slow))
	if len(m.history) != history || !strings.HasPrefix(m.status, "Disconnected Other database") {
		t.Fatalf("the canceled query's reply was applied: %q", m.status)
	}
	if _, err := session.Execute(context.Background(), "SELECT 1;", result.Limits{}); err == nil {
		t.Fatal("the disconnected session is still open")
	}
	if batch := execute(t, control, "SELECT count(*) FROM multi_probe;"); batch.Results[0].Rows[0][0].Text != "0" {
		t.Fatal("disconnect did not roll back the open transaction")
	}
}

func TestLiveCatalogStructureAndCompletionFollowTheirConnection(t *testing.T) {
	dsn, _ := testConnection(t)
	profile := otherProfile(t)
	control := controlOther(t)
	execute(t, control, "CREATE SCHEMA multi_other; CREATE TABLE multi_other.only_here (id int PRIMARY KEY, note text);")
	t.Cleanup(func() { execute(t, control, "DROP SCHEMA multi_other CASCADE;") })
	session, _ := liveSession(t, false)
	m := New(Config{Session: session, DSN: dsn, InitialSQL: "SELECT 1;"})
	closeTestModel(t, m)
	m.Update(tea.WindowSizeMsg{Width: 140, Height: 60})
	finishCommands(t, m, m.Init())
	a := m.conn()
	finishCommands(t, m, m.connectProfile(profile, "", false))
	b := m.conn()
	find := func(c *connection, name string) int {
		for index, row := range m.databaseRows() {
			if row.kind == databaseCatalog && row.conn == c.id && c.catalog[row.node].name == name {
				return index
			}
		}
		return -1
	}
	if find(a, "claims") < 0 || find(b, "multi_other") < 0 || find(b, "claims") >= 0 || find(a, "multi_other") >= 0 {
		t.Fatal("catalog trees mixed their connections")
	}
	m.focusPane(catalogPane)
	m.selectDatabaseRow(find(b, "multi_other"))
	finishCommands(t, m, press(m, tea.KeyEnter))
	m.selectDatabaseRow(find(b, "only_here"))
	finishCommands(t, m, rowAction(m, 'd'))
	if !m.structure.loaded || m.structure.conn != b.id || m.structure.data.Qualified != "multi_other.only_here" {
		t.Fatalf("structure from b: %q %q", m.structure.data.Qualified, m.structure.err)
	}
	press(m, 'D')
	if m.conn() != b || !strings.HasPrefix(string(m.buffers[m.bufferIndex].editor.text), "CREATE TABLE multi_other.only_here (") || anyRunning(m) {
		t.Fatal("DDL did not open, unexecuted, on b")
	}
	m.buffers[m.bufferIndex].editor = newEditor("SELECT * FROM multi_other.|")
	placeCursor(m)
	m.focus = editorPane
	finishCommands(t, m, m.completeWord(true))
	if !slices.Equal(m.completion.items, []string{"only_here"}) {
		t.Fatalf("completion on b offered %q", m.completion.items)
	}
	m.switchBuffer(0)
	m.buffers[0].editor = newEditor("SELECT * FROM claims.|")
	placeCursor(m)
	m.focus = editorPane
	finishCommands(t, m, m.completeWord(true))
	if !slices.Contains(m.completion.items, "beneficiary") || slices.Contains(m.completion.items, "only_here") {
		t.Fatalf("completion on a offered %q", m.completion.items)
	}
}
