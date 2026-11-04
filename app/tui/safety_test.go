package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/dustinevan/scrunchie/lib/data/connections"
	"github.com/dustinevan/scrunchie/lib/data/postgres"
	"github.com/dustinevan/scrunchie/pkg/result"
)

// safetyModel places the cursor at the "|" marker in an offline workspace.
func safetyModel(buffer string) *Model {
	m := New(Config{Demo: true})
	cursor := len([]rune(buffer[:strings.Index(buffer, "|")]))
	m.buffers[0].editor = newEditor(strings.Replace(buffer, "|", "", 1))
	m.buffers[0].editor.cursor = cursor
	m.prepareView()
	return m
}

func TestRunAfterSemicolonRunsThatStatementNotTheNext(t *testing.T) {
	cases := []struct {
		name, buffer, want string
		line               int
	}{
		{"reported repro", "select * from patients where id = 1;|\n\ndelete from audit_log;", "select * from patients where id = 1;", 1},
		{"blank line gap", "select 1;\n\n|\ndelete from audit_log;", "select 1;", 1},
		{"trailing comment gap", "select 1; -- next|\ndelete from audit_log;", "select 1;", 1},
		{"before first", "\n\n|\n-- about it\nselect 1;", "-- about it\nselect 1;", 5},
		{"later statement", "select 1;\n\nselect '雪🙂';|\nselect 3;", "select '雪🙂';", 3},
		{"unicode before cursor", "select '雪🙂 名前';|\n\ndelete from audit_log;", "select '雪🙂 名前';", 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := safetyModel(c.buffer)
			press(m, 'j', tea.ModCtrl)
			defer m.Cancel()
			if m.modal.kind != "" || !anyRunning(m) || m.conn().query.sql != c.want {
				t.Fatalf("modal %q, running %t, query %q", m.modal.kind, m.conn().query.running, m.conn().query.sql)
			}
			m.Update(queryMsg{conn: m.conn().id, serial: m.conn().query.serial, batch: result.Batch{Connected: true, TxStatus: 'I'}})
			if want := "Line " + string(rune('0'+c.line)) + " · "; !strings.HasPrefix(m.status, want) {
				t.Errorf("status %q does not start with %q", m.status, want)
			}
		})
	}
}

func TestUnfilteredSQLRunsOnlyAfterExplicitConfirmation(t *testing.T) {
	m := safetyModel("select 1;\ndelete from claims.beneficiary;|")
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	press(m, 'j', tea.ModCtrl)
	if m.modal.kind != "confirm-unfiltered" || m.modal.index != 0 || anyRunning(m) {
		t.Fatalf("modal %q index %d running %t", m.modal.kind, m.modal.index, anyRunning(m))
	}
	if !strings.Contains(m.modal.message, "DELETE without WHERE affects every row in claims.beneficiary.") || !strings.Contains(m.modal.message, "Line 2") {
		t.Fatalf("message %q", m.modal.message)
	}
	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "Cancel") || !strings.Contains(view, "Run anyway") {
		t.Fatal("confirmation buttons are not visible")
	}
	// Repeated Run, typing, and clicks outside the dialog cannot reach the editor.
	for range 3 {
		press(m, 'j', tea.ModCtrl)
		press(m, tea.KeyEnter, tea.ModCtrl)
	}
	press(m, 'y')
	mouseClick(m, 40, 4)
	mouseRelease(m, 40, 4)
	mouseClick(m, 40, 25)
	mouseRelease(m, 40, 25)
	if anyRunning(m) || m.modal.kind != "confirm-unfiltered" || string(m.buffers[0].editor.text) != "select 1;\ndelete from claims.beneficiary;" {
		t.Fatal("confirmation was bypassed or leaked input")
	}
	press(m, tea.KeyEnter)
	if anyRunning(m) || m.modal.kind != "" || !strings.Contains(m.status, "nothing was run") {
		t.Fatalf("default Enter did not cancel: running %t, status %q", m.conn().query.running, m.status)
	}
	press(m, 'j', tea.ModCtrl)
	press(m, tea.KeyEsc)
	if anyRunning(m) || m.modal.kind != "" {
		t.Fatal("Escape did not cancel")
	}
	press(m, 'j', tea.ModCtrl)
	hit := modalHit(t, m, "button", 1)
	mouseClick(m, hit.area.x+1, hit.area.y)
	defer m.Cancel()
	if !anyRunning(m) || m.modal.kind != "" || m.conn().query.sql != "delete from claims.beneficiary;" || m.conn().query.line != 2 {
		t.Fatalf("Run anyway did not run the checked SQL: running %t, query %q", m.conn().query.running, m.conn().query.sql)
	}
	// While it runs, another Run is refused before any check.
	press(m, 'j', tea.ModCtrl)
	if m.modal.kind != "" {
		t.Fatal("a racing Run opened another confirmation")
	}
}

func TestEveryRunPathChecksTheSQLItSends(t *testing.T) {
	m := safetyModel("select 1;|\ntruncate claims.claim_line;")
	press(m, 'j', tea.ModCtrl)
	defer m.Cancel()
	if m.modal.kind != "" || m.conn().query.sql != "select 1;" {
		t.Fatal("statement at cursor was checked against the whole buffer")
	}
	m = safetyModel("select 1;|\ntruncate claims.claim_line;")
	m.action("run-all")
	if m.modal.kind != "confirm-unfiltered" || anyRunning(m) || !strings.Contains(m.modal.message, "TRUNCATE removes every row from claims.claim_line.") {
		t.Fatalf("run-all was not checked: %q", m.modal.message)
	}
	m = safetyModel("update t set a = 1 where id = 2; update u set b = 3;|")
	e := &m.buffers[0].editor
	e.anchor, e.cursor = 0, len([]rune("update t set a = 1 where id = 2;"))
	press(m, 'j', tea.ModCtrl)
	if m.modal.kind != "" || m.conn().query.sql != "update t set a = 1 where id = 2;" {
		t.Fatal("filtered selection was confirmed or not run")
	}
	m = safetyModel("update t set a = 1 where id = 2; update u set b = 3;|")
	e = &m.buffers[0].editor
	e.anchor, e.cursor = len([]rune("update t set a = 1 where id = 2; ")), len(e.text)
	press(m, 'j', tea.ModCtrl)
	if m.modal.kind != "confirm-unfiltered" || m.pendingRun.sql != "update u set b = 3;" {
		t.Fatalf("selected unfiltered UPDATE was not held for confirmation: %q", m.pendingRun.sql)
	}
}

func TestConfirmationRefusesChangedSQL(t *testing.T) {
	m := safetyModel("delete from t;|")
	press(m, 'j', tea.ModCtrl)
	m.buffers[0].editor.insert(" -- changed", "")
	m.modal.index = 1
	press(m, tea.KeyEnter)
	if anyRunning(m) || !m.statusError || !strings.Contains(m.status, "nothing was run") {
		t.Fatalf("changed SQL ran after confirmation: %q", m.status)
	}
}

func TestHeaderIsQuietWhenReadOnlyAndPinkWhenWritable(t *testing.T) {
	for _, width := range []int{20, 40, 80, 140} {
		m := New(Config{Session: &postgres.Session{}, Connection: "reader@example/reports"})
		m.Update(tea.WindowSizeMsg{Width: width, Height: 20})
		header := strings.Split(m.View().Content, "\n")[0]
		if m.conn().writable || strings.Contains(ansi.Strip(header), "WRITES ON") || width >= 40 && !strings.Contains(ansi.Strip(header), "read-only") {
			t.Fatalf("width %d read-only header %q", width, ansi.Strip(header))
		}
		m.conn().writable = true
		m.conn().txStatus = 'T'
		header = strings.Split(m.View().Content, "\n")[0]
		plain := ansi.Strip(header)
		if !strings.Contains(plain, "WRITES ON") || strings.Contains(plain, "read-only") || ansi.StringWidth(header) != width {
			t.Fatalf("width %d writable header %q", width, plain)
		}
		if width >= 80 && !strings.Contains(plain, "IN TRANSACTION") {
			t.Fatalf("writable header hid transaction state: %q", plain)
		}
		// One style covers the whole bar; nested styles would end the background early.
		if strings.Count(header, "\x1b[") > 2 || !strings.HasSuffix(header, "\x1b[m") {
			t.Fatalf("writable header is not one styled run: %q", header)
		}
	}
	m := New(Config{Session: &postgres.Session{}, Connection: "reader@example\x1b]52;c;INJECT\x07\x1b[2J"})
	m.conn().writable = true
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 20})
	header := strings.Split(m.View().Content, "\n")[0]
	if strings.Contains(header, "\x1b]") || strings.Contains(header, "\x07") || strings.Contains(header, "\x1b[2J") {
		t.Fatalf("writable header emitted database-originated controls: %q", header)
	}
	demo := New(Config{Demo: true})
	demo.conn().writable = true
	if header := ansi.Strip(strings.Split(demo.View().Content, "\n")[0]); strings.Contains(header, "WRITES ON") || !strings.Contains(header, "read-only") {
		t.Fatalf("demo header %q", header)
	}
	blank := New(Config{})
	if header := ansi.Strip(strings.Split(blank.View().Content, "\n")[0]); strings.Contains(header, "read-only") || strings.Contains(header, "WRITES ON") {
		t.Fatalf("disconnected header %q", header)
	}
}

func TestWriteModeCommandsRefuseWithoutAnIdleSession(t *testing.T) {
	m := New(Config{Demo: true})
	m.action("allow-writes")
	if m.modal.kind != "" || !strings.Contains(m.status, "Demo") {
		t.Fatalf("demo allowed writes: %q", m.status)
	}
	m = New(Config{Session: &postgres.Session{}})
	m.conn().txStatus = 'T'
	m.action("allow-writes")
	if m.modal.kind != "" || !strings.Contains(m.status, "transaction") {
		t.Fatalf("transaction allowed writes: %q", m.status)
	}
	m.conn().txStatus = 'I'
	m.conn().query.running = true
	if cmd := m.action("read-only"); cmd != nil || !strings.Contains(m.status, "running query") {
		t.Fatalf("running query allowed a mode change: %q", m.status)
	}
	m.conn().query.running = false
	m.action("allow-writes")
	if m.modal.kind != "confirm-writes" || m.modal.index != 0 || !strings.Contains(m.modal.message, "Allow writes on") {
		t.Fatalf("allow writes did not ask first: %q", m.modal.message)
	}
	press(m, tea.KeyEnter)
	if m.modal.kind != "" || anyRunning(m) || m.conn().writable {
		t.Fatal("default choice changed the write mode")
	}
}

func TestConnectionFormAllowWritesCheckbox(t *testing.T) {
	m := New(Config{})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
	m.openConnectionForm(connections.Profile{Name: "Local", Host: "localhost", Port: "5432", Database: "postgres", User: "reader"})
	if m.connectionForm.allowWrites || m.connectionForm.profile().AllowWrites {
		t.Fatal("new connections allowed writes by default")
	}
	m.connectionForm.index = connectionAllowWrites
	press(m, 'x')
	press(m, tea.KeySpace)
	if !m.connectionForm.allowWrites || !m.connectionForm.profile().AllowWrites || string(m.connectionForm.fields[connectionName].text) != "Local" {
		t.Fatal("Space did not toggle Allow writes, or text escaped into a field")
	}
	for _, size := range [][2]int{{18, 8}, {30, 12}, {80, 24}, {120, 36}} {
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		m.connectionForm.index = connectionAllowWrites
		m.prepareView()
		hit := modalHit(t, m, "checkbox", connectionAllowWrites)
		row := strings.Split(ansi.Strip(m.View().Content), "\n")[hit.area.y]
		if !strings.Contains(row, "[") {
			t.Fatalf("Allow writes target is not on its rendered row at %dx%d: %q", size[0], size[1], row)
		}
		before := m.connectionForm.allowWrites
		mouseClick(m, hit.area.x+2, hit.area.y)
		mouseRelease(m, hit.area.x+2, hit.area.y)
		if m.connectionForm.allowWrites == before || m.connecting || m.profileBusy || !m.connectionForm.remember {
			t.Fatal("Allow writes click toggled the wrong control")
		}
		m.mouse.lastClick = time.Time{}
		m.modalMouseState = modalMouseState{}
	}
	saved := connections.Profile{ID: "w", Name: "Writable", Host: "localhost", Port: "5432", Database: "app", User: "owner", AllowWrites: true}
	m.openConnectionForm(saved)
	if !m.connectionForm.allowWrites || m.connectionForm.profile() != saved {
		t.Fatal("editing a writable profile lost its mode")
	}
}

// Live tests below use the isolated cluster and roll back every write.

func liveSession(t *testing.T, allowWrites bool) (*postgres.Session, string) {
	t.Helper()
	dsn, _ := testConnection(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session, err := postgres.Open(ctx, dsn, postgres.Options{AllowWrites: allowWrites})
	if err != nil {
		t.Fatal(err)
	}
	return session, dsn
}

func runBuffer(t *testing.T, m *Model, sql string) {
	t.Helper()
	m.buffers[m.bufferIndex].editor = newEditor(sql)
	m.buffers[m.bufferIndex].editor.cursor = len([]rune(sql))
	m.focus = editorPane
	finishCommands(t, m, m.runSQL(true))
}

func TestLiveWriteModeFollowsSessionAndToggles(t *testing.T) {
	session, dsn := liveSession(t, false)
	m := New(Config{Session: session, DSN: dsn})
	closeTestModel(t, m)
	if m.conn().writable {
		t.Fatal("default session was shown as writable")
	}
	runBuffer(t, m, `BEGIN; INSERT INTO public."Mixed Case" VALUES (-1, 'probe');`)
	if !strings.Contains(m.status, "Ctrl+P → Allow writes") || !strings.Contains(m.status, "25006") {
		t.Fatalf("read-only write status %q", m.status)
	}
	runBuffer(t, m, "ROLLBACK;")
	runBuffer(t, m, "SET default_transaction_read_only = off;")
	if !m.conn().writable || !strings.Contains(ansi.Strip(m.View().Content), "WRITES ON") {
		t.Fatal("a manual SET did not turn the header pink")
	}
	finishCommands(t, m, m.action("read-only"))
	if m.conn().writable || anyRunning(m) || session.ReadOnly() != true {
		t.Fatalf("read-only command failed: %q", m.status)
	}
	m.action("allow-writes")
	if m.modal.kind != "confirm-writes" || !strings.Contains(m.modal.message, "scrunch_test") {
		t.Fatalf("confirmation did not name the database: %q", m.modal.message)
	}
	press(m, tea.KeyRight)
	_, cmd := m.Update(key(tea.KeyEnter))
	finishCommands(t, m, cmd)
	if !m.conn().writable || session.ReadOnly() || !strings.Contains(m.status, "Writes are on") {
		t.Fatalf("allow writes failed: %q", m.status)
	}
	runBuffer(t, m, `BEGIN; INSERT INTO public."Mixed Case" VALUES (-1, 'probe'); ROLLBACK;`)
	if m.statusError || len(m.results().batch.Results) != 3 || m.results().batch.Results[1].Command != "INSERT 0 1" {
		t.Fatalf("writable session could not write: %q", m.status)
	}
	runBuffer(t, m, "BEGIN;")
	m.action("read-only")
	if anyRunning(m) || !m.conn().writable || !strings.Contains(m.status, "transaction") {
		t.Fatalf("mode changed inside a transaction: %q", m.status)
	}
	runBuffer(t, m, "ROLLBACK;")
	runBuffer(t, m, "RESET ALL;")
	if m.conn().writable {
		t.Fatal("RESET ALL did not restore the read-only startup mode")
	}
}

func TestLiveReconnectAndProfilesUseConfiguredMode(t *testing.T) {
	session, dsn := liveSession(t, true)
	m := New(Config{Session: session, DSN: dsn, AllowWrites: true})
	closeTestModel(t, m)
	cli := m.conn()
	if !cli.writable {
		t.Fatal("writable startup session was shown read-only")
	}
	finishCommands(t, m, m.reconnect())
	if !cli.writable || cli.session == session || cli.session.ReadOnly() || m.conn() != cli {
		t.Fatal("reconnect lost the --write mode or the connection's identity")
	}
	_, profile := testConnection(t)
	finishCommands(t, m, m.connectProfile(profile, "", false))
	reader := m.conn()
	if reader == cli || reader.writable || !reader.session.ReadOnly() {
		t.Fatalf("read-only profile opened writable: %q", m.status)
	}
	if !cli.writable || cli.session.ReadOnly() {
		t.Fatal("opening a read-only profile changed the other connection's mode")
	}
	finishCommands(t, m, m.reconnect())
	if reader.writable || !reader.session.ReadOnly() {
		t.Fatal("reconnecting a read-only profile allowed writes")
	}
	profile.ID, profile.AllowWrites = "integration-writes", true
	finishCommands(t, m, m.connectProfile(profile, "", false))
	if writer := m.conn(); writer == reader || !writer.writable || writer.session.ReadOnly() || reader.writable {
		t.Fatalf("writable profile opened read-only: %q", m.status)
	}
}
