package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/dustinevan/scrunchie/lib/data/postgres"
	"github.com/dustinevan/scrunchie/lib/logic/cell"
	"github.com/dustinevan/scrunchie/pkg/result"
)

func TestPrintableKeysBelongToEditorAndPrompts(t *testing.T) {
	for _, kind := range []string{"", "palette", "history", "find", "open", "save", "export", "goto"} {
		t.Run(kind, func(t *testing.T) {
			m := New(Config{})
			if kind != "" {
				m.openPrompt(kind, kind, "")
			}
			for _, r := range "qjkliv?/[" {
				if cmd := press(m, r); cmd != nil {
					t.Fatalf("printable %q emitted a command in %s", r, kind)
				}
			}
			got := string(m.buffers[0].editor.text)
			if kind != "" {
				got = string(m.modal.input.text)
				if len(m.buffers[0].editor.text) != 0 || m.modal.kind != kind {
					t.Fatal("prompt input escaped to the editor")
				}
			}
			if got != "qjkliv?/[" || anyRunning(m) || m.quitIntent {
				t.Fatalf("text=%q running=%t quitting=%t", got, anyRunning(m), m.quitIntent)
			}
		})
	}
}

func TestEditorTabNewlineAndModalPrecedence(t *testing.T) {
	m := New(Config{})
	press(m, tea.KeyTab)
	press(m, tea.KeyEnter)
	if string(m.buffers[0].editor.text) != "    \n" || m.focus != editorPane || anyRunning(m) {
		t.Fatal("Tab or Enter was interpreted as navigation or execution")
	}
	m.openPrompt("find", "Find", "")
	for _, msg := range []tea.KeyPressMsg{key(tea.KeyF5), key('j', tea.ModCtrl), key('n', tea.ModCtrl), key('w', tea.ModCtrl)} {
		m.Update(msg)
	}
	m.Update(tea.PasteMsg{Content: "a\nb"})
	if m.focus != editorPane || anyRunning(m) || len(m.buffers) != 1 || string(m.modal.input.text) != "a b" {
		t.Fatal("modal allowed pane, run, buffer, or paste state to leak")
	}
	press(m, tea.KeyEsc)
	if m.modal.kind != "" {
		t.Fatal("Escape did not close prompt")
	}
}

func TestRunUsesSelectionOrCurrentStatementAndKeepsSnapshot(t *testing.T) {
	m := New(Config{Demo: true})
	m.buffers[0].editor = newEditor("SELECT '雪;';\nSELECT 2;\nSELECT 3;")
	e := &m.buffers[0].editor
	e.cursor = len([]rune("SELECT '雪;';\nSEL"))
	press(m, 'j', tea.ModCtrl)
	defer m.Cancel()
	c := m.conn()
	if !c.query.running || strings.TrimSpace(c.query.sql) != "SELECT 2;" {
		t.Fatalf("query = %q, running = %t", c.query.sql, c.query.running)
	}
	snapshot, serial := c.query.source, c.query.serial
	e.insert("changed", "")
	m.focus = catalogPane
	m.Update(queryMsg{conn: c.id, serial: serial, batch: result.Batch{Connected: true, TxStatus: 'I'}})
	if len(m.history) != 1 || strings.TrimSpace(m.history[0].sql) != "SELECT 2;" || m.focus != catalogPane {
		t.Fatal("completion lost execution snapshot or stole focus")
	}
	if string(e.text) == snapshot {
		t.Fatal("editing while a query ran was lost")
	}
	e.text = []rune("SELECT 10; SELECT 20;")
	e.anchor, e.cursor = 11, len(e.text)
	m.runSQL(false)
	if c.query.sql != "SELECT 20;" {
		t.Fatalf("selected query = %q", c.query.sql)
	}
}

func TestStaleQueryMessageIsIgnored(t *testing.T) {
	m := New(Config{})
	c := offlineConnection(m)
	c.query.running, c.query.serial = true, 2
	m.Update(queryMsg{conn: c.id, serial: 1, err: errors.New("old failure")})
	// A reply for a connection that is not open is dropped as well.
	m.Update(queryMsg{conn: c.id + 1, serial: 2, err: errors.New("old failure")})
	if !c.query.running || len(m.history) != 0 || strings.Contains(m.status, "old failure") {
		t.Fatal("stale operation changed current state")
	}
}

func TestQueryErrorPositionUsesOriginalUnicodeBuffer(t *testing.T) {
	m := New(Config{Demo: true})
	source := "SELECT '雪';\nSELECT bad;"
	m.buffers[0].editor = newEditor(source)
	m.buffers[0].editor.cursor = len([]rune(source))
	m.runSQL(false)
	defer m.Cancel()
	position := m.conn().query.offset + 7
	m.Update(queryMsg{conn: m.conn().id, serial: m.conn().query.serial, batch: result.Batch{Connected: true, TxStatus: 'I'}, err: postgres.Error{Message: "missing column", Position: 8, Detail: "details", Hint: "hint"}})
	if m.buffers[0].editor.cursor != position || !strings.Contains(m.lastMessage, "details") || !strings.Contains(m.lastMessage, "hint") {
		t.Fatalf("cursor=%d want=%d, details=%q", m.buffers[0].editor.cursor, position, m.lastMessage)
	}
}

func TestControlCCancelsWithoutQuittingOrCopying(t *testing.T) {
	m := New(Config{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := offlineConnection(m)
	c.query.running, c.query.cancel = true, cancel
	m.openPrompt("find", "Find", "selected")
	if cmd := press(m, 'c', tea.ModCtrl); cmd != nil || ctx.Err() != context.Canceled || !c.query.canceling || m.quitIntent {
		t.Fatal("Ctrl+C did not exclusively cancel the running query")
	}
	if m.modal.kind != "find" || string(m.modal.input.text) != "selected" {
		t.Fatal("cancel modified prompt")
	}
	c.query.running = false
	if cmd := press(m, 'c', tea.ModCtrl); cmd == nil || fmt.Sprint(cmd()) != "selected" {
		t.Fatal("idle Ctrl+C did not copy selected prompt text")
	}
}

func TestQueuedQueryCancellationDoesNotCancelCatalog(t *testing.T) {
	m := New(Config{Demo: true})
	catalogCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := m.conn()
	c.catalogBusy, c.catalogCancel = true, cancel
	m.runSQL(false)
	if !c.query.running || !c.query.queued || catalogCtx.Err() != nil {
		t.Fatal("query did not queue behind metadata")
	}
	press(m, 'c', tea.ModCtrl)
	if c.query.running || c.query.queued || catalogCtx.Err() != nil {
		t.Fatal("canceling queued SQL canceled catalog or left SQL queued")
	}
}

func TestQuitConfirmationsDefaultToStay(t *testing.T) {
	m := New(Config{InitialSQL: "SELECT 1;"})
	m.buffers[0].editor.insert("-- edited\n", "")
	press(m, 'q', tea.ModCtrl)
	if m.modal.kind != "confirm-dirty" || m.modal.index != 0 {
		t.Fatal("dirty buffer quit did not default to Cancel")
	}
	press(m, 'd')
	if m.modal.index != 0 {
		t.Fatal("letter key selected destructive action")
	}
	press(m, tea.KeyEnter)
	if m.quitIntent || m.modal.kind != "" {
		t.Fatal("default confirmation quit or stayed open")
	}
	m.buffers[0].saved = string(m.buffers[0].editor.text)
	offlineConnection(m).txStatus = 'E'
	press(m, 'q', tea.ModCtrl)
	if m.modal.kind != "confirm-quit" || m.modal.index != 0 || !strings.Contains(m.modal.message, "failed transaction will roll back") {
		t.Fatal("failed transaction quit did not warn")
	}
	press(m, tea.KeyRight)
	cmd := press(m, tea.KeyEnter)
	if cmd == nil {
		t.Fatal("confirmed quit did not return quit command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("confirmed quit returned another command")
	}
}

func TestQuitWaitsForReconnectCancellation(t *testing.T) {
	m := New(Config{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.connecting, m.connectCancel = true, cancel
	if cmd := press(m, 'q', tea.ModCtrl); cmd != nil || ctx.Err() != context.Canceled || !m.quitIntent {
		t.Fatal("quit did not wait for the connection command to return")
	}
	_, cmd := m.Update(reconnectMsg{err: context.Canceled})
	if cmd == nil {
		t.Fatal("quit did not continue after canceled connection")
	}
}

func TestHistoryIsBoundedAndOpeningNeverRunsSQL(t *testing.T) {
	m := New(Config{})
	c := offlineConnection(m)
	for i := 0; i < 105; i++ {
		m.serial++
		c.query = queryState{running: true, serial: m.serial, sql: fmt.Sprintf("SELECT %d;", i), started: time.Now(), buffer: m.buffers[0].id}
		m.Update(queryMsg{conn: c.id, serial: m.serial, batch: result.Batch{Connected: true, TxStatus: 'I'}})
	}
	if len(m.history) != 100 || m.history[0].sql != "SELECT 5;" {
		t.Fatalf("history = %d entries, first %q", len(m.history), m.history[0].sql)
	}
	press(m, tea.KeyF8)
	press(m, tea.KeyEnter)
	if anyRunning(m) || len(m.buffers) != 2 || string(m.buffers[1].editor.text) != "SELECT 104;" {
		t.Fatal("history did not open the latest SQL without execution")
	}
}

func TestUnicodeSearchAndQualifiedCompletion(t *testing.T) {
	m := New(Config{InitialSQL: "İ雪abc ABC"})
	m.findNext("abc")
	e := &m.buffers[0].editor
	start, end := e.selection()
	if string(e.text[start:end]) != "abc" || start != 2 {
		t.Fatalf("Unicode search selection = %q at %d", string(e.text[start:end]), start)
	}
	m.findNext("abc")
	start, end = e.selection()
	if string(e.text[start:end]) != "ABC" {
		t.Fatalf("find next = %q", string(e.text[start:end]))
	}
	e.text = []rune("SELECT * FROM public.us")
	e.cursor, e.anchor = len(e.text), -1
	offline := offlineConnection(m)
	offline.catalog = []catalogNode{{kind: "relation", name: "users", schema: "public", relation: postgres.Relation{Schema: "public", Name: "users"}}}
	m.completeWord(true)
	press(m, tea.KeyTab)
	if string(e.text) != `SELECT * FROM public.users` {
		t.Fatalf("qualified completion = %q", string(e.text))
	}
}

func TestExplainPreparesExactlyOneStatement(t *testing.T) {
	m := New(Config{InitialSQL: "SELECT 1; DELETE FROM secrets;"})
	m.action("explain")
	if anyRunning(m) || len(m.buffers) != 2 || strings.Contains(string(m.buffers[1].editor.text), "DELETE") {
		t.Fatal("EXPLAIN included a later statement or executed SQL")
	}
	m.bufferIndex = 0
	m.buffers[0].editor.anchor = 0
	m.buffers[0].editor.cursor = len(m.buffers[0].editor.text)
	m.action("explain")
	if len(m.buffers) != 2 || !m.statusError {
		t.Fatal("EXPLAIN accepted a multi-statement selection")
	}
}

func TestGridRectangularCopyFormatsAndResultSwitch(t *testing.T) {
	m := New(Config{})
	m.focus = resultsPane
	m.results().batch.Results = []result.Set{
		{Columns: []result.Column{{Name: "a", OID: 25}, {Name: "b", OID: 25}}, Rows: [][]result.Cell{{{Text: "hello\tworld"}, {Null: true}}, {{Text: "third"}, {Text: "last"}}}},
		{Columns: []result.Column{{Name: "bytes", OID: 17}}, Rows: [][]result.Cell{{{Bytes: []byte("abc")}}}},
	}
	m.prepareView()
	press(m, tea.KeyRight, tea.ModShift)
	press(m, tea.KeyDown, tea.ModShift)
	cmd := press(m, 'c', tea.ModCtrl)
	if cmd == nil || fmt.Sprint(cmd()) != "\"hello\tworld\"\t\\N\nthird\tlast" {
		t.Fatal("rectangle was not copied as quoted TSV")
	}
	m.action("next-result")
	if m.results().rowAnchor != -1 || m.results().columnAnchor != -1 {
		t.Fatal("switching result kept an out-of-range selection")
	}
	m.prepareView()
	for i := 0; i < 3; i++ {
		press(m, 'v')
	}
	if m.columnFormat() != cell.Hex {
		t.Fatalf("format = %s", m.columnFormat())
	}
	cmd = press(m, 'c', tea.ModCtrl)
	if cmd == nil || fmt.Sprint(cmd()) != "0x616263" || string(m.results().batch.Results[1].Rows[0][0].Bytes) != "abc" {
		t.Fatal("display conversion changed stored bytes or copied wrong value")
	}
}

func TestJSONInspectorNavigationFilterAndReturn(t *testing.T) {
	m := New(Config{})
	m.focus = resultsPane
	m.results().batch.Results = []result.Set{{Columns: []result.Column{{Name: "doc", OID: 3802}}, Rows: [][]result.Cell{{{Text: `{"outer":{"number":9007199254740993,"list":[1,2]},"other":null}`}}}}}
	m.prepareView()
	press(m, tea.KeyEnter)
	if !m.inspector.active || !m.inspector.json || len(m.inspector.visible) != 3 {
		t.Fatal("JSON inspector did not open root with keys")
	}
	press(m, tea.KeyDown)
	press(m, tea.KeyRight)
	if len(m.inspector.visible) != 5 {
		t.Fatal("Right did not expand selected object")
	}
	press(m, tea.KeyRight)
	cmd := press(m, 'p')
	if cmd == nil || fmt.Sprint(cmd()) != `$["outer"]["number"]` {
		t.Fatal("selected JSON key path was incorrect")
	}
	cmd = press(m, 'c', tea.ModCtrl)
	if cmd == nil || fmt.Sprint(cmd()) != "9007199254740993" {
		t.Fatal("JSON number lost precision on copy")
	}
	m.findNext("list")
	if len(m.inspector.visible) < 3 {
		t.Fatal("key filter lost key or ancestors")
	}
	press(m, tea.KeyEsc)
	if !m.inspector.active || m.inspector.filter != "" {
		t.Fatal("Escape did not clear JSON filter first")
	}
	press(m, 'c')
	if len(m.inspector.visible) != 1 {
		t.Fatal("collapse all did not retain root")
	}
	press(m, 'e')
	if len(m.inspector.visible) != len(m.inspector.tree.Nodes) {
		t.Fatal("expand all omitted nodes")
	}
	press(m, tea.KeyEsc)
	if m.inspector.active || m.focus != resultsPane || m.results().row != 0 || m.results().column != 0 {
		t.Fatal("return from inspector lost grid focus or location")
	}
}
