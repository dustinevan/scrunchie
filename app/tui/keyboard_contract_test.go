package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/dustinevan/scrunchie/lib/data/connections"
	"github.com/dustinevan/scrunchie/pkg/result"
)

func TestEditorBoundaryArrowsChangePanesWithoutActivatingAnything(t *testing.T) {
	for _, demo := range []bool{false, true} {
		m := New(Config{Demo: demo})
		m.buffers[0].editor = newEditor("a\nb")
		m.showCatalog = false
		if cmd := m.editorKey(key(tea.KeyLeft)); cmd != nil || m.focus != catalogPane || !m.showCatalog {
			t.Fatal("Left at offset zero did not reveal the database pane without an action")
		}
		rows := m.databaseRows()
		want := databaseAdd
		if demo {
			want = databaseConnection
		}
		if rows[m.databasePane.index].kind != want || anyRunning(m) || m.connecting || anyCatalogBusy(m) || m.fileBusy {
			t.Fatal("editor boundary selected the wrong database row or started work")
		}
		m.focus = editorPane
		e := &m.buffers[0].editor
		e.cursor = 2
		m.editorKey(key(tea.KeyLeft))
		if m.focus != editorPane || e.cursor != 1 {
			t.Fatal("Left at a later line start left the SQL editor")
		}
		e.cursor = len(e.text)
		if cmd := m.editorKey(key(tea.KeyDown)); cmd != nil || m.focus != resultsPane || anyRunning(m) {
			t.Fatal("Down at the absolute buffer end did not focus results without running SQL")
		}
	}
}

func TestSelectionsAndModifiedMovementStayInsideTheEditor(t *testing.T) {
	m := New(Config{InitialSQL: "ab\ncd"})
	e := &m.buffers[0].editor
	for _, msg := range []tea.KeyPressMsg{key(tea.KeyLeft, tea.ModShift), key(tea.KeyLeft, tea.ModCtrl), key(tea.KeyLeft, tea.ModAlt), key(tea.KeyHome), key(tea.KeyPgUp)} {
		e.cursor, e.anchor = 0, -1
		m.editorKey(msg)
		if m.focus != editorPane {
			t.Fatalf("%s left the editor", msg.String())
		}
	}
	e.cursor, e.anchor = 0, len(e.text)
	m.editorKey(key(tea.KeyLeft))
	if m.focus != editorPane || e.anchor != -1 || e.cursor != 0 {
		t.Fatal("Left did not first collapse SQL selection at the boundary")
	}
	m.editorKey(key(tea.KeyLeft))
	if m.focus != catalogPane {
		t.Fatal("the next Left did not cross the cleared boundary")
	}
	m.focus = editorPane
	e.cursor, e.anchor = len(e.text), 0
	m.editorKey(key(tea.KeyDown))
	if m.focus != editorPane || e.anchor != -1 {
		t.Fatal("Down crossed the boundary with SQL selected")
	}
	m.editorKey(key(tea.KeyDown, tea.ModShift))
	if m.focus != editorPane {
		t.Fatal("Shift+Down crossed a pane boundary")
	}
	for _, msg := range []tea.KeyPressMsg{key(tea.KeyEnd), key(tea.KeyPgDown), key(tea.KeyEnd, tea.ModCtrl)} {
		m.editorKey(msg)
		if m.focus != editorPane {
			t.Fatalf("%s left the editor", msg.String())
		}
	}
}

func TestCompletionConsumesItsKeysBeforePaneBoundaries(t *testing.T) {
	m := New(Config{InitialSQL: "SEL"})
	e := &m.buffers[0].editor
	e.cursor, e.anchor = len(e.text), 0
	m.completion = completion{items: []string{"SELECT", "SELECT DISTINCT"}, start: 0, end: len(e.text)}
	m.editorKey(key(tea.KeyDown))
	if m.focus != editorPane || m.completion.index != 1 || string(e.text) != "SEL" {
		t.Fatal("completion Down changed panes or SQL")
	}
	m.editorKey(key(tea.KeyTab, tea.ModShift))
	if m.completion.index != 0 || string(e.text) != "SEL" {
		t.Fatal("Shift+Tab indented SQL instead of selecting completion")
	}
	m.editorKey(key(tea.KeyEsc))
	if len(m.completion.items) != 0 || e.anchor != 0 {
		t.Fatal("first Escape did not dismiss only completion")
	}
	m.editorKey(key(tea.KeyEsc))
	if e.anchor != -1 || m.focus != editorPane {
		t.Fatal("second Escape did not clear SQL selection in place")
	}
	m.completion = completion{items: []string{"SELECT"}, start: 0, end: len(e.text), buffer: m.buffers[0].id}
	m.editorKey(key(tea.KeyEnter))
	if string(e.text) != "SELECT" || anyRunning(m) || m.focus != editorPane {
		t.Fatal("completion Enter inserted a newline, ran SQL, or changed panes")
	}
	m.completion = completion{items: []string{"a", "b", "c", "d", "e", "f", "g"}, start: 0, end: len(e.text)}
	cursor := e.cursor
	m.editorKey(key(tea.KeyPgDown))
	if m.completion.index != completionHeight(max(1, m.dimensions().editorHeight-5)) || e.cursor != cursor || m.focus != editorPane {
		t.Fatal("completion Page Down did not keep paging inside its visible list")
	}
	m.editorKey(key(tea.KeyPgUp))
	if m.completion.index != 0 || e.cursor != cursor {
		t.Fatal("completion Page Up moved the SQL cursor")
	}
}

func TestResultBoundaryArrowsAndEscapeRespectRectangles(t *testing.T) {
	m := New(Config{})
	m.results().batch.Results = []result.Set{{Columns: []result.Column{{Name: "a"}, {Name: "b"}}, Rows: [][]result.Cell{{{Text: "one"}, {Text: "two"}}, {{Text: "three"}, {Text: "four"}}}}}
	m.focus = resultsPane
	m.gridKey(key(tea.KeyLeft, tea.ModShift))
	if m.focus != resultsPane || m.results().rowAnchor != 0 || m.results().columnAnchor != 0 {
		t.Fatal("Shift+Left crossed the results boundary or lost selection")
	}
	m.gridKey(key(tea.KeyLeft))
	if m.focus != resultsPane || m.results().rowAnchor != -1 {
		t.Fatal("plain Left should clear the rectangle before crossing the boundary")
	}
	if cmd := m.gridKey(key(tea.KeyLeft)); cmd != nil || m.focus != catalogPane {
		t.Fatal("Left at the cleared first column did not focus databases")
	}
	m.focus = resultsPane
	m.gridKey(key(tea.KeyHome, tea.ModCtrl))
	if m.focus != resultsPane {
		t.Fatal("Ctrl+Home left results")
	}
	if cmd := m.gridKey(key(tea.KeyUp)); cmd != nil || m.focus != editorPane {
		t.Fatal("Up at the first result row did not focus SQL")
	}
	m.focus = resultsPane
	m.results().rowAnchor, m.results().columnAnchor = 0, 0
	m.gridKey(key(tea.KeyEsc))
	if m.focus != resultsPane || m.results().rowAnchor != -1 {
		t.Fatal("first Escape did not clear the rectangle")
	}
	m.gridKey(key(tea.KeyEsc))
	if m.focus != editorPane || anyRunning(m) || m.connecting || m.fileBusy {
		t.Fatal("second Escape did not return to SQL without side effects")
	}
}

func TestEmptyResultsRemainNavigable(t *testing.T) {
	for _, sets := range [][]result.Set{nil, {{Command: "UPDATE 1"}}, {{Columns: []result.Column{{Name: "empty"}}}}} {
		m := New(Config{})
		m.results().batch.Results = sets
		m.focus = resultsPane
		m.gridKey(key(tea.KeyUp))
		if m.focus != editorPane {
			t.Fatal("empty results trapped Up")
		}
		m.focus = resultsPane
		m.gridKey(key(tea.KeyLeft))
		if m.focus != catalogPane {
			t.Fatal("empty results trapped Left")
		}
		m.focus = resultsPane
		m.gridKey(key(tea.KeyDown, tea.ModShift))
		m.gridKey(key(tea.KeySpace))
		if m.focus != resultsPane || m.inspector.active || anyRunning(m) {
			t.Fatal("an empty grid created an inspector or activated a pane")
		}
	}
}

func TestPageMovementUsesVisiblePaneRows(t *testing.T) {
	m := New(Config{InitialSQL: strings.Repeat("x\n", 100)})
	m.height, m.width = 50, 120
	editorRows := max(1, m.dimensions().editorHeight-5)
	m.editorKey(key(tea.KeyPgDown))
	if m.buffers[0].editor.cursor != editorRows*2 {
		t.Fatal("editor page movement disagrees with visible rows")
	}
	m.results().batch.Results = []result.Set{{Columns: []result.Column{{Name: "n"}}, Rows: make([][]result.Cell, 100)}}
	m.focus = resultsPane
	gridRows := max(1, m.dimensions().resultHeight-6)
	m.gridKey(key(tea.KeyPgDown))
	if m.results().row != gridRows {
		t.Fatalf("grid page moved %d rows, want %d", m.results().row, gridRows)
	}
}

func TestInspectorTreeBoundariesRemainLocalAndEscapeReturnsToResults(t *testing.T) {
	m := New(Config{})
	m.results().batch.Results = []result.Set{{Columns: []result.Column{{Name: "doc", OID: 3802}}, Rows: [][]result.Cell{{{Text: `{"nested":{"a":1}}`}}}}}
	m.openInspector()
	m.inspectorKey(key(tea.KeyLeft))
	m.inspectorKey(key(tea.KeyLeft))
	if !m.inspector.active || m.focus != resultsPane {
		t.Fatal("JSON root Left escaped the tree")
	}
	m.inspector.filter = "nested"
	m.refreshInspectorRows()
	m.inspectorKey(key(tea.KeyEsc))
	if !m.inspector.active || m.inspector.filter != "" {
		t.Fatal("first inspector Escape did not clear its filter")
	}
	m.inspectorKey(key(tea.KeyEsc))
	if m.inspector.active || m.focus != resultsPane {
		t.Fatal("inspector Escape did not explicitly restore results focus")
	}
}

func TestSpaceEditsTextButActivatesNonTextControls(t *testing.T) {
	for _, kind := range []string{"palette", "history", "find", "open", "save", "export", "goto"} {
		m := New(Config{})
		m.openPrompt(kind, kind, "ab")
		m.modal.input.cursor, m.modal.input.anchor = 1, -1
		m.modalKey(key(tea.KeySpace))
		if string(m.modal.input.text) != "a b" || m.modal.kind != kind || anyRunning(m) || m.fileBusy {
			t.Fatalf("Space activated the %s text prompt", kind)
		}
	}
	m := New(Config{})
	m.modal = modal{kind: "confirm-dirty", buttons: []string{"Cancel", "Save", "Discard"}}
	m.modalKey(key(tea.KeyTab, tea.ModShift))
	if m.modal.index != 2 {
		t.Fatal("Shift+Tab did not cycle confirmation buttons backward")
	}
	m.modalKey(key(tea.KeyHome))
	m.modalKey(key(tea.KeySpace))
	if m.modal.kind != "" || m.fileBusy {
		t.Fatal("Space did not activate the focused Cancel button")
	}
	profile := connections.Profile{ID: "local", Name: "Local", Host: "localhost", Port: "5432", Database: "postgres", User: "reader", SSLMode: "prefer"}
	m = New(Config{Profiles: []connections.Profile{profile}})
	m.openDatabases()
	m.profileIndex = len(m.profiles)
	m.connectionKey(key(tea.KeySpace))
	if m.modal.kind != "connection" || m.connecting {
		t.Fatal("Space on Add database did not open its form")
	}
	m.connectionForm.fields[connectionName] = newEditor("Local")
	m.connectionForm.fields[connectionName].singleLine = true
	m.connectionForm.fields[connectionName].cursor = 5
	m.connectionKey(key(tea.KeySpace))
	if string(m.connectionForm.fields[connectionName].text) != "Local " {
		t.Fatal("Space did not edit the connection field")
	}
	m.connectionForm.index = connectionCancel
	m.connectionKey(key(tea.KeySpace))
	if m.modal.kind != "connections" {
		t.Fatal("Space did not activate the form's Cancel button")
	}
	m.openConnectionForm(profile)
	m.modal = modal{kind: "database-names", title: "Choose database"}
	m.databaseNames = []string{"reports"}
	m.connectionKey(key(tea.KeySpace))
	if m.modal.kind != "connection" || string(m.connectionForm.fields[connectionDatabase].text) != "reports" || m.connecting {
		t.Fatal("Space did not select a database name without connecting")
	}
}

func TestHelpScrollingHasNoOverscrollDebt(t *testing.T) {
	m := New(Config{})
	m.modal = modal{kind: "help", title: "Keyboard reference"}
	m.modalKey(key(tea.KeyEnd))
	last := m.modal.index
	if last <= 0 {
		t.Fatal("End did not reach the final help page")
	}
	for i := 0; i < 20; i++ {
		m.modalKey(key(tea.KeyPgDown))
	}
	if m.modal.index != last {
		t.Fatal("help scrolling accumulated an invisible offset past the last page")
	}
	m.modalKey(key(tea.KeyUp))
	if m.modal.index != last-1 {
		t.Fatal("Up did not immediately move from the last help page")
	}
	m.modalKey(key(tea.KeyHome))
	m.modalKey(key(tea.KeySpace))
	if m.modal.index == 0 || m.modal.kind != "help" {
		t.Fatal("Space did not page through help")
	}
}

func TestConnectionArrowsClampWhileTabCyclesControls(t *testing.T) {
	m := New(Config{})
	m.openConnectionForm(connections.Profile{})
	m.connectionKey(key(tea.KeyUp))
	if m.connectionForm.index != connectionName {
		t.Fatal("Up wrapped from the first form field")
	}
	m.connectionKey(key(tea.KeyTab, tea.ModShift))
	if m.connectionForm.index != connectionDiscover {
		t.Fatal("Shift+Tab did not cycle to the last form control")
	}
	m.connectionKey(key(tea.KeyDown))
	if m.connectionForm.index != connectionDiscover {
		t.Fatal("Down wrapped from the last form control")
	}
	m.connectionKey(key(tea.KeyTab))
	if m.connectionForm.index != connectionName {
		t.Fatal("Tab did not cycle back to the first form field")
	}
}
