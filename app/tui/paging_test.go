package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/dustinevan/scrunchie/lib/data/postgres"
	"github.com/dustinevan/scrunchie/pkg/result"
)

func pagedSet(rows int, more bool) result.Set {
	set := result.Set{Columns: []result.Column{{Name: "id", Type: "int8", OID: 20}, {Name: "name", Type: "text", OID: 25}}, Command: "SELECT", RowCount: int64(rows), HasMore: more}
	for i := 0; i < rows; i++ {
		set.Rows = append(set.Rows, []result.Cell{{Text: "1"}, {Text: "n"}})
	}
	return set
}

func TestRunStatusReportsFetchedRowsWithoutClaimingTotals(t *testing.T) {
	duration := 1500 * time.Microsecond
	for _, test := range []struct {
		name         string
		batch        result.Batch
		page, budget int
		status       string
	}{
		{"first page", result.Batch{Results: []result.Set{pagedSet(1000, true)}, Duration: duration}, 1000, 10000,
			"First 1,000 rows · more available · m fetches more (re-runs query) · 2ms"},
		{"row budget", result.Batch{Results: []result.Set{pagedSet(10000, true)}, Duration: duration}, 10000, 10000,
			"First 10,000 rows · more available · row limit reached; narrow the query or raise --max-rows · 2ms"},
		{"drained", result.Batch{Results: []result.Set{{Rows: make([][]result.Cell, 10000), RowCount: 50000, Truncated: true}}, Truncated: true, Duration: duration}, 1000, 10000,
			"10,000 of 50,000 rows shown · display limit · 2ms"},
		{"several drained", result.Batch{Results: []result.Set{{RowCount: 1}, {Rows: make([][]result.Cell, 9999), RowCount: 50000, Truncated: true}}, Truncated: true, Duration: duration}, 1000, 10000,
			"2 results · 9,999 of 50,001 rows shown · display limit · 2ms"},
		{"byte limit inside page", result.Batch{Results: []result.Set{{Rows: make([][]result.Cell, 812), RowCount: 1000, Truncated: true, HasMore: true}}, Truncated: true, Duration: duration}, 1000, 10000,
			"812 of the first 1,000 rows shown · display limit · more available · 2ms"},
		{"complete", result.Batch{Results: []result.Set{pagedSet(523, false)}, Duration: duration}, 1000, 10000,
			"1 result · 523 rows · 2ms"},
		{"commands", result.Batch{Results: []result.Set{{Command: "BEGIN"}, {Command: "SET"}}, Duration: duration}, 1000, 10000,
			"2 results · 0 rows · 2ms"},
	} {
		status, _ := runStatus(test.batch, test.page, test.budget)
		if status != test.status {
			t.Errorf("%s: status %q, want %q", test.name, status, test.status)
		}
	}
}

func TestDisplayLimitStatusIsOneMessageAndEndsInEllipsis(t *testing.T) {
	m := New(Config{Demo: true})
	m.Update(tea.WindowSizeMsg{Width: 60, Height: 20})
	m.runSQL(false)
	defer m.Cancel()
	batch := result.Batch{Connected: true, TxStatus: 'I', Truncated: true, Notices: []string{"NOTICE: from the server"},
		Results: []result.Set{{Columns: []result.Column{{Name: "g"}}, Rows: make([][]result.Cell, 10000), RowCount: 50000, Truncated: true}}}
	m.Update(queryMsg{conn: m.conn().id, serial: m.conn().query.serial, batch: batch})
	if strings.Count(m.status, "limit") != 1 || strings.Contains(m.status, "drained") || !strings.Contains(m.status, "· 10,000 of 50,000 rows shown · display limit") {
		t.Fatalf("status %q", m.status)
	}
	lines := strings.Split(screen(m), "\n")
	status := lines[len(lines)-2]
	if ansi.StringWidth(status) != 60 || !strings.HasSuffix(strings.TrimRight(status, " "), "…") {
		t.Fatalf("status line %q does not end in an ellipsis within the width", status)
	}
	if !strings.HasPrefix(m.lastMessage, m.status) || !strings.Contains(m.lastMessage, "NOTICE: from the server") || !strings.Contains(m.lastMessage, "--max-rows") {
		t.Fatalf("Read last status lost the full text: %q", m.lastMessage)
	}
	m.action("message")
	if m.modal.kind != "message" || !strings.Contains(m.modal.message, m.status) {
		t.Fatalf("message command shows %q", m.modal.message)
	}
}

func TestFailedRunWithoutResultsReplacesPreviousResults(t *testing.T) {
	m := New(Config{Demo: true})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m.buffers[0].editor = newEditor("SELECT 1;\nSELECT\n  nope;")
	m.buffers[0].editor.cursor = len(m.buffers[0].editor.text)
	m.runSQL(false)
	defer m.Cancel()
	if len(m.results().batch.Results) == 0 {
		t.Fatal("demo fixture has no previous results")
	}
	m.Update(queryMsg{conn: m.conn().id, serial: m.conn().query.serial, batch: result.Batch{Connected: true, TxStatus: 'I'}, err: postgres.Error{Message: "column \"nope\" does not exist", Code: "42703", Position: 10, Hint: "Check the name"}})
	if len(m.results().batch.Results) != 0 || m.results().batch.TxStatus != 'I' || !m.results().batch.Connected {
		t.Fatalf("previous results survived a failed run: %#v", m.results().batch)
	}
	view := screen(m)
	for _, want := range []string{`! column "nope" does not exist`, "SQLSTATE 42703 · line 3, column 3", "Hint: Check the name", "This run returned no results."} {
		if !strings.Contains(view, want) {
			t.Errorf("results pane lacks %q:\n%s", want, view)
		}
	}

	completed := result.Set{Columns: []result.Column{{Name: "kept"}}, Rows: [][]result.Cell{{{Text: "earlier statement"}}}, Command: "SELECT 1", RowCount: 1}
	m.runSQL(true)
	m.Update(queryMsg{conn: m.conn().id, serial: m.conn().query.serial, batch: result.Batch{Connected: true, TxStatus: 'I', Results: []result.Set{completed}}, err: errors.New("later statement failed")})
	if len(m.results().batch.Results) != 1 || m.results().err != "" || !strings.Contains(screen(m), "earlier statement") {
		t.Fatal("completed results of the failed batch were not shown")
	}
	m.runSQL(false)
	m.Update(queryMsg{conn: m.conn().id, serial: m.conn().query.serial, batch: demoBatch()})
	if m.results().err != "" || strings.Contains(screen(m), "This run returned no results") {
		t.Fatal("a successful run kept the earlier error")
	}
}

func TestFetchMoreReRunsResultSQLWithLargerPage(t *testing.T) {
	m := New(Config{Demo: true, Limits: result.Limits{MaxRows: 100000}})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	source := "SELECT 'other';\nSELECT id, name FROM people;"
	m.buffers[0].editor = newEditor(source)
	m.buffers[0].editor.cursor = len([]rune(source))
	m.runSQL(false)
	defer m.Cancel()
	if m.conn().query.page != firstPageRows || m.conn().query.fetchMore {
		t.Fatalf("first run page %d, fetch more %v", m.conn().query.page, m.conn().query.fetchMore)
	}
	m.Update(queryMsg{conn: m.conn().id, serial: m.conn().query.serial, batch: result.Batch{Connected: true, TxStatus: 'I', Results: []result.Set{pagedSet(1000, true)}}})
	if !strings.Contains(screen(m), "first 1,000 rows · more available · m fetches more") || !strings.Contains(m.status, "· First 1,000 rows · more available") {
		t.Fatalf("partial result not labeled; status %q", m.status)
	}
	m.focus, m.results().row, m.results().column, m.results().gridTop = resultsPane, 700, 1, 690
	m.buffers[0].editor.cursor = 0
	m.buffers[0].editor.insert("-- edited\n", "")
	if press(m, 'm') == nil || !anyRunning(m) || !m.conn().query.fetchMore || m.conn().query.page != 10000 || strings.TrimSpace(m.conn().query.sql) != "SELECT id, name FROM people;" {
		t.Fatalf("fetch more ran %q with page %d (running %v)", m.conn().query.sql, m.conn().query.page, anyRunning(m))
	}
	top := m.results().gridTop
	if m.conn().query.source != source || !strings.Contains(m.status, "up to 10,000 rows") {
		t.Fatalf("fetch more lost its origin: source %q status %q", m.conn().query.source, m.status)
	}
	m.Update(queryMsg{conn: m.conn().id, serial: m.conn().query.serial, batch: result.Batch{Connected: true, TxStatus: 'I', Results: []result.Set{pagedSet(10000, true)}}})
	if m.results().row != 700 || m.results().column != 1 || m.results().gridTop != top || top < 600 || len(m.results().batch.Results[0].Rows) != 10000 {
		t.Fatalf("fetch more moved the selection to row %d column %d top %d", m.results().row, m.results().column, m.results().gridTop)
	}
	if len(m.history) != 2 || m.history[1].sql != m.history[0].sql || strings.Contains(m.history[1].sql, "DECLARE") || !m.history[1].more {
		t.Fatalf("history recorded %#v", m.history)
	}
	m.action("fetch-more")
	if !anyRunning(m) || m.conn().query.page != 100000 {
		t.Fatalf("palette fetch more page %d", m.conn().query.page)
	}
	m.Update(queryMsg{conn: m.conn().id, serial: m.conn().query.serial, batch: result.Batch{Connected: true, TxStatus: 'I', Results: []result.Set{pagedSet(100000, true)}}})
	if press(m, 'm') != nil || anyRunning(m) || !strings.Contains(m.status, "--max-rows") {
		t.Fatalf("fetch more past the row budget: running %v status %q", m.conn().query.running, m.status)
	}
}

func TestFetchMoreRefusesCompleteResultsAndBusySessions(t *testing.T) {
	m := New(Config{Demo: true})
	m.focus = resultsPane
	m.results().batch = result.Batch{Connected: true, TxStatus: 'I', Results: []result.Set{pagedSet(3, false)}}
	if press(m, 'm') != nil || anyRunning(m) || !strings.Contains(m.status, "already shown") {
		t.Fatalf("complete result fetched more: %q", m.status)
	}
	m.results().batch.Results[0].HasMore = true
	m.results().origin = runOrigin{sql: "SELECT 1", page: 1000}
	m.conn().query.running = true
	if press(m, 'm') != nil || m.fetchPending {
		t.Fatal("fetch more while running left a pending re-run")
	}
	m.conn().query.running = false
	m.buffers[0].editor = newEditor("SELECT 'cursor statement'")
	m.runSQL(false)
	defer m.Cancel()
	if m.conn().query.sql != "SELECT 'cursor statement'" || m.conn().query.fetchMore {
		t.Fatalf("Run after a refused fetch more ran %q", m.conn().query.sql)
	}
}

func TestExportOfPartialResultSaysOnlyFetchedRows(t *testing.T) {
	m := New(Config{Demo: true})
	m.results().batch = result.Batch{Results: []result.Set{pagedSet(1000, true)}}
	m.exportPrompt()
	if m.modal.kind != "export" || m.modal.title != "Exports the 1,000 fetched rows · m fetches more first" {
		t.Fatalf("export prompt %q %q", m.modal.kind, m.modal.title)
	}
}

func TestFetchMoreCommandFollowsExport(t *testing.T) {
	for i, command := range commands {
		if command.id == "export" {
			if i+1 >= len(commands) || commands[i+1].id != "fetch-more" || commands[i+1].key != "m" {
				t.Fatal("fetch-more palette entry must follow export")
			}
			return
		}
	}
	t.Fatal("export command missing")
}

func TestIntegration_TUIFetchesFirstPageThenMore(t *testing.T) {
	dsn, _ := testConnection(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session, err := postgres.Open(ctx, dsn, postgres.Options{})
	if err != nil {
		t.Fatal(err)
	}
	m := New(Config{Session: session, DSN: dsn})
	closeTestModel(t, m)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m.buffers[0].editor = newEditor("SELECT id FROM claims.beneficiary ORDER BY id;\nSELECT 'not this';")
	finishCommands(t, m, m.runSQL(false))
	set := m.results().batch.Results[0]
	if len(set.Rows) != 1000 || !set.HasMore || set.Command != "SELECT 1000" || !strings.Contains(m.status, "· First 1,000 rows · more available") || m.results().batch.TxStatus != 'I' {
		t.Fatalf("first page rows=%d more=%v command=%q status=%q", len(set.Rows), set.HasMore, set.Command, m.status)
	}
	m.focus, m.results().row = resultsPane, 999
	m.buffers[0].editor.cursor = len(m.buffers[0].editor.text)
	finishCommands(t, m, press(m, 'm'))
	set = m.results().batch.Results[0]
	if len(set.Rows) != 10000 || !set.HasMore || m.results().row != 999 || set.Rows[9999][0].Text != "10000" || !strings.Contains(m.status, "--max-rows") {
		t.Fatalf("fetch more rows=%d more=%v row=%d status=%q", len(set.Rows), set.HasMore, m.results().row, m.status)
	}
	if len(m.history) != 2 || m.history[1].sql != "SELECT id FROM claims.beneficiary ORDER BY id;" {
		t.Fatalf("history %#v", m.history)
	}

	m.buffers[0].editor = newEditor("SELECT 1/0;")
	finishCommands(t, m, m.runSQL(false))
	if len(m.results().batch.Results) != 0 || !strings.Contains(screen(m), "division by zero") || !strings.Contains(screen(m), "SQLSTATE 22012") {
		t.Fatalf("failed run left results %#v", m.results().batch.Results)
	}
}
