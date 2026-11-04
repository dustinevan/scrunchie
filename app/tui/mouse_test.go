package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/dustinevan/scrunchie/lib/data/postgres"
	"github.com/dustinevan/scrunchie/pkg/result"
)

func mouseFixture(sql string) *Model {
	m := New(Config{Demo: true, InitialSQL: sql})
	m.buffers[0].editor = newEditor(sql)
	m.buffers[0].saved = sql
	set := result.Set{Command: "SELECT 30", RowCount: 30}
	for column := 0; column < 5; column++ {
		set.Columns = append(set.Columns, result.Column{Name: fmt.Sprintf("c%d", column), Type: "text", OID: 25})
	}
	for row := 0; row < 30; row++ {
		values := make([]result.Cell, len(set.Columns))
		for column := range values {
			values[column].Text = fmt.Sprintf("r%dc%d", row, column)
		}
		set.Rows = append(set.Rows, values)
	}
	m.results().batch.Results = []result.Set{set}
	m.results().widths = nil
	m.prepareView()
	return m
}

func mouseClick(m *Model, x, y int, modifiers ...tea.KeyMod) tea.Cmd {
	var mod tea.KeyMod
	for _, value := range modifiers {
		mod |= value
	}
	_, cmd := m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft, Mod: mod})
	return cmd
}

func mouseRelease(m *Model, x, y int) {
	m.Update(tea.MouseReleaseMsg{X: x, Y: y, Button: tea.MouseLeft})
}

func mouseMotion(m *Model, x, y int) tea.Cmd {
	_, cmd := m.Update(tea.MouseMotionMsg{X: x, Y: y, Button: tea.MouseLeft})
	return cmd
}

func mouseWheel(m *Model, x, y int, button tea.MouseButton) {
	m.Update(tea.MouseWheelMsg{X: x, Y: y, Button: button})
}

func mouseCellPoint(m *Model, row, column int) (int, int) {
	area := m.gridDataRect()
	x := area.x + 6
	for index := m.results().gridLeft; index < column; index++ {
		x += m.results().widths[m.results().setIndex][index] + 1
	}
	return x, area.y + row - m.results().gridTop
}

func TestMouseEditorClickUsesRenderedUnicodeAndTabCells(t *testing.T) {
	for _, tc := range []struct {
		column int
		cursor int
	}{{0, 0}, {1, 1}, {2, 1}, {3, 2}, {6, 2}, {7, 3}, {8, 4}, {9, 5}} {
		t.Run(fmt.Sprintf("display-column-%d", tc.column), func(t *testing.T) {
			m := mouseFixture("a雪\tbc\nsecond")
			// The 100x30 split view renders text at global x=31, y=4:
			// 25 database columns, border, five line-number columns.
			lines := strings.Split(ansi.Strip(m.View().Content), "\n")
			if !strings.Contains(lines[4], "a雪    bc") {
				t.Fatalf("SQL row was not rendered at y=4: %q", lines[4])
			}
			mouseClick(m, 31+tc.column, 4)
			mouseRelease(m, 31+tc.column, 4)
			if m.focus != editorPane || m.buffers[0].editor.cursor != tc.cursor {
				t.Fatalf("click at (%d,4): focus=%d cursor=%d, want SQL cursor=%d", 31+tc.column, m.focus, m.buffers[0].editor.cursor, tc.cursor)
			}
		})
	}
}

func TestMouseEditorDragSelectsExactRunesAndShiftClickExtends(t *testing.T) {
	m := mouseFixture("a雪\tbc\nsecond")
	mouseClick(m, 32, 4)
	mouseMotion(m, 39, 4)
	mouseRelease(m, 39, 4)
	e := &m.buffers[0].editor
	start, end := e.selection()
	if got := string(e.text[start:end]); got != "雪\tb" {
		t.Fatalf("drag selected %q, want exact wide rune, tab, and b", got)
	}
	cmd := press(m, 'c', tea.ModCtrl)
	if cmd == nil || fmt.Sprint(cmd()) != "雪\tb" {
		t.Fatal("mouse selection copy did not retain the original tab")
	}
	mouseClick(m, 40, 4, tea.ModShift)
	mouseRelease(m, 40, 4)
	start, end = e.selection()
	if got := string(e.text[start:end]); got != "雪\tbc" {
		t.Fatalf("Shift+click selected %q", got)
	}
	if string(e.text) != "a雪\tbc\nsecond" || anyRunning(m) {
		t.Fatal("mouse selection edited or executed SQL")
	}
}

func TestMouseEditorClickAfterHorizontalScroll(t *testing.T) {
	m := mouseFixture("abc雪\tdefghijklmnopqrstuvwxyz")
	e := &m.buffers[0].editor
	e.cursor, e.anchor = 1, 0
	mouseWheel(m, 35, 4, tea.MouseWheelRight)
	if e.left != 3 || e.cursor != 1 || e.anchor != 0 {
		t.Fatalf("horizontal wheel changed selection or failed to scroll: left=%d cursor=%d anchor=%d", e.left, e.cursor, e.anchor)
	}
	// After three hidden runes, 雪 is at columns 0..1, tab at 2..5,
	// and d at column 6. Click coordinates use that scrolled display.
	mouseClick(m, 37, 4)
	mouseRelease(m, 37, 4)
	if e.cursor != 5 {
		t.Fatalf("scrolled click cursor=%d, want d at rune 5", e.cursor)
	}
}

func TestMouseEditorDoubleAndTripleClickSelectWordAndLine(t *testing.T) {
	m := mouseFixture("SELECT snow_name;\nnext")
	for click := 0; click < 2; click++ {
		mouseClick(m, 40, 4)
		mouseRelease(m, 40, 4)
	}
	e := &m.buffers[0].editor
	start, end := e.selection()
	if got := string(e.text[start:end]); got != "snow_name" {
		t.Fatalf("double click selected %q", got)
	}
	mouseClick(m, 40, 4)
	mouseRelease(m, 40, 4)
	start, end = e.selection()
	if got := string(e.text[start:end]); got != "SELECT snow_name;\n" {
		t.Fatalf("triple click selected %q", got)
	}
}

func TestMouseGridRectangleShiftClickAndClipboard(t *testing.T) {
	m := mouseFixture("SELECT 1;")
	x, y := mouseCellPoint(m, 1, 1)
	mouseClick(m, x, y)
	x, y = mouseCellPoint(m, 3, 2)
	mouseMotion(m, x, y)
	mouseRelease(m, x, y)
	if m.focus != resultsPane || m.results().rowAnchor != 1 || m.results().columnAnchor != 1 || m.results().row != 3 || m.results().column != 2 {
		t.Fatalf("grid drag focus=%d rectangle=(%d,%d)-(%d,%d)", m.focus, m.results().rowAnchor, m.results().columnAnchor, m.results().row, m.results().column)
	}
	cmd := press(m, 'c', tea.ModCtrl)
	if cmd == nil || fmt.Sprint(cmd()) != "r1c1\tr1c2\nr2c1\tr2c2\nr3c1\tr3c2" {
		t.Fatal("mouse rectangle copy did not match selected cells")
	}
	x, y = mouseCellPoint(m, 4, 3)
	mouseClick(m, x, y, tea.ModShift)
	mouseRelease(m, x, y)
	if m.results().rowAnchor != 1 || m.results().columnAnchor != 1 || m.results().row != 4 || m.results().column != 3 {
		t.Fatal("Shift+click did not retain rectangle anchor")
	}
}

func TestMouseGridHeadersSelectRangesWithoutJumpingViewport(t *testing.T) {
	t.Run("column and Shift column", func(t *testing.T) {
		m := mouseFixture("SELECT 1;")
		m.focus = resultsPane
		m.results().row, m.results().gridTop = 3, 3
		m.gridScrolling = true
		m.prepareView()
		content := m.paneRect(resultsPane).content()
		x, _ := mouseCellPoint(m, 3, 1)
		mouseClick(m, x, content.y+1)
		mouseRelease(m, x, content.y+1)
		if m.results().rowAnchor != 0 || m.results().row != 29 || m.results().columnAnchor != 1 || m.results().column != 1 {
			t.Fatal("column header did not select all retained rows in that column")
		}
		if m.results().gridTop != 3 || m.results().gridLeft != 0 {
			t.Fatalf("column selection jumped viewport to top=%d left=%d", m.results().gridTop, m.results().gridLeft)
		}
		x, _ = mouseCellPoint(m, 3, 3)
		mouseClick(m, x, content.y+2, tea.ModShift)
		mouseRelease(m, x, content.y+2)
		if m.results().columnAnchor != 1 || m.results().column != 3 || m.results().rowAnchor != 0 || m.results().row != 29 || m.results().gridTop != 3 {
			t.Fatal("Shift+click type header did not extend columns without scrolling")
		}
	})
	t.Run("row, Shift row, and corner", func(t *testing.T) {
		m := mouseFixture("SELECT 1;")
		m.Update(tea.WindowSizeMsg{Width: 70, Height: 30})
		m.focus = resultsPane
		m.prepareView()
		area := m.gridDataRect()
		mouseClick(m, area.x+2, area.y+1)
		mouseRelease(m, area.x+2, area.y+1)
		if m.results().rowAnchor != 1 || m.results().row != 1 || m.results().columnAnchor != 0 || m.results().column != 4 || m.results().gridLeft != 0 {
			t.Fatal("row number did not select its complete row without horizontal scrolling")
		}
		mouseClick(m, area.x+2, area.y+3, tea.ModShift)
		mouseRelease(m, area.x+2, area.y+3)
		if m.results().rowAnchor != 1 || m.results().row != 3 || m.results().columnAnchor != 0 || m.results().column != 4 || m.results().gridLeft != 0 {
			t.Fatal("Shift+click row number did not extend the full-row rectangle")
		}
		content := m.paneRect(resultsPane).content()
		mouseClick(m, content.x+2, content.y+1)
		mouseRelease(m, content.x+2, content.y+1)
		if m.results().rowAnchor != 0 || m.results().columnAnchor != 0 || m.results().row != 29 || m.results().column != 4 || m.results().gridTop != 0 || m.results().gridLeft != 0 {
			t.Fatal("corner header did not select retained table without jumping viewport")
		}
	})
}

func TestMouseGridBlankTailDoesNotSelectUnrenderedCell(t *testing.T) {
	m := mouseFixture("SELECT 1;")
	m.focus = resultsPane
	m.results().row, m.results().column = 2, 1
	m.prepareView()
	area := m.gridDataRect()
	x := area.x + 6
	for _, width := range m.results().widths[0] {
		x += width + 1
	}
	if x >= area.x+area.width {
		t.Fatal("fixture requires visible blank space after the table")
	}
	mouseClick(m, x+1, area.y+1)
	mouseRelease(m, x+1, area.y+1)
	if m.results().row != 2 || m.results().column != 1 || m.results().rowAnchor >= 0 {
		t.Fatal("blank tail after last column selected an unrelated cell")
	}
}

func TestMouseDragAcrossPaneBordersKeepsOriginalOwner(t *testing.T) {
	t.Run("SQL over results and database", func(t *testing.T) {
		m := mouseFixture(strings.Repeat("0123456789\n", 40))
		mouseClick(m, 34, 4)
		anchor := m.buffers[0].editor.anchor
		area := m.gridDataRect()
		cmd := mouseMotion(m, area.x+8, area.y+2)
		if m.focus != editorPane || m.mouse.drag != "editor" || m.buffers[0].editor.anchor != anchor || cmd == nil {
			t.Fatal("SQL drag crossing results changed owner or failed to schedule edge scrolling")
		}
		mouseMotion(m, 2, 8)
		if m.focus != editorPane || m.modal.kind != "" {
			t.Fatal("SQL drag crossing database pane activated a row")
		}
		mouseRelease(m, 2, 8)
		if m.mouse.drag != "" {
			t.Fatal("release outside original pane did not end selection")
		}
	})
	t.Run("results over SQL", func(t *testing.T) {
		m := mouseFixture("unchanged SQL")
		x, y := mouseCellPoint(m, 2, 1)
		mouseClick(m, x, y)
		mouseMotion(m, 40, 4)
		if m.focus != resultsPane || m.mouse.drag != "grid" || m.results().rowAnchor != 2 || m.results().columnAnchor != 1 || string(m.buffers[0].editor.text) != "unchanged SQL" {
			t.Fatal("grid drag crossing SQL changed selection owner or SQL")
		}
		mouseRelease(m, 40, 4)
	})
}

func TestMouseWheelPreservesFocusCaretAndSelections(t *testing.T) {
	m := mouseFixture(strings.Repeat("01234567890123456789\n", 50))
	e := &m.buffers[0].editor
	e.cursor, e.anchor = 7, 2
	m.focus = resultsPane
	m.results().row, m.results().column, m.results().rowAnchor, m.results().columnAnchor = 3, 1, 1, 0
	m.prepareView()
	mouseWheel(m, 40, 6, tea.MouseWheelDown)
	if m.focus != resultsPane || e.cursor != 7 || e.anchor != 2 || e.top != 3 {
		t.Fatalf("wheel over SQL: focus=%d caret=%d anchor=%d top=%d", m.focus, e.cursor, e.anchor, e.top)
	}
	x, y := mouseCellPoint(m, 3, 1)
	mouseWheel(m, x, y, tea.MouseWheelDown)
	if m.focus != resultsPane || m.results().row != 3 || m.results().column != 1 || m.results().rowAnchor != 1 || m.results().columnAnchor != 0 || m.results().gridTop != 3 {
		t.Fatal("grid wheel changed focus or rectangle instead of only the viewport")
	}
	press(m, tea.KeyRight)
	if m.gridScrolling || m.results().row != 3 || m.results().column != 2 || m.results().rowAnchor >= 0 {
		t.Fatal("keyboard movement did not resume at the actual selected cell")
	}
	press(m, tea.KeyF2)
	if e.top != 0 || e.cursor != 7 || e.anchor != 2 {
		t.Fatal("return to SQL did not reveal its remembered selection")
	}
	mouseWheel(m, 40, 6, tea.MouseWheelDown)
	press(m, tea.KeyRight)
	if e.top != 0 || e.scrolling || e.cursor != 7 || e.anchor >= 0 {
		t.Fatal("first caret movement after scrolling did not reveal/collapse selection")
	}
}

func TestMouseBufferAndResultTabsSelectWithoutExecuting(t *testing.T) {
	m := mouseFixture("first buffer")
	m.buffers[0].editor.cursor = 4
	m.addBuffer("second buffer", "", true, m.buffers[0].conn)
	m.buffers[1].editor.cursor = 5
	m.prepareView()
	content := m.paneRect(editorPane).content()
	mouseClick(m, content.x+2, content.y)
	mouseRelease(m, content.x+2, content.y)
	if m.bufferIndex != 0 || m.buffers[0].editor.cursor != 4 || m.buffers[1].editor.cursor != 5 {
		t.Fatal("tab click lost SQL buffer or caret state")
	}
	m.results().batch.Results = append(m.results().batch.Results, result.Set{Command: "UPDATE 2"})
	m.results().rowAnchor, m.results().columnAnchor = 1, 1
	m.prepareView()
	content = m.paneRect(resultsPane).content()
	secondTabX := content.x + len(" 1 SELECT 30 ") + 1
	mouseClick(m, secondTabX+1, content.y)
	mouseRelease(m, secondTabX+1, content.y)
	if m.results().setIndex != 1 || m.focus != resultsPane || m.results().rowAnchor >= 0 || anyRunning(m) {
		t.Fatal("result tab click did not select command-only result safely")
	}
	press(m, tea.KeyUp)
	if m.focus != editorPane {
		t.Fatal("command-only result had no Up edge back to SQL")
	}
}

func TestMouseJSONSelectionDisclosureAndDoubleClick(t *testing.T) {
	m := mouseFixture("SELECT 1;")
	m.results().batch.Results = []result.Set{{Columns: []result.Column{{Name: "doc", Type: "jsonb", OID: 3802}}, Rows: [][]result.Cell{{{Text: `{"outer":{"inside":1},"other":2}`}}}}}
	m.results().widths = nil
	m.focus = resultsPane
	m.prepareView()
	press(m, tea.KeyEnter)
	if !m.inspector.active || len(m.inspector.visible) != 3 {
		t.Fatal("JSON fixture did not open root and first-level keys")
	}
	// Root row is y=5; outer is y=6 and its triangle is x=3.
	mouseClick(m, 8, 6)
	mouseRelease(m, 8, 6)
	if m.inspector.index != 1 || len(m.inspector.visible) != 3 {
		t.Fatal("single label click toggled JSON instead of selecting its node")
	}
	mouseClick(m, 3, 6)
	mouseRelease(m, 3, 6)
	if len(m.inspector.visible) != 4 {
		t.Fatal("disclosure click did not expand selected JSON object")
	}
	mouseClick(m, 8, 6)
	mouseRelease(m, 8, 6)
	mouseClick(m, 8, 6)
	mouseRelease(m, 8, 6)
	if len(m.inspector.visible) != 3 || !m.inspector.active {
		t.Fatal("double click did not collapse JSON object in its inspector")
	}
}

func TestMouseDisconnectedDatabaseDisclosureNeverReconnects(t *testing.T) {
	// No command is executed. The zero-value session represents a previous
	// target reported disconnected by the final PostgreSQL result.
	m := New(Config{Session: &postgres.Session{}, Connection: "previous target", DSN: "host=must-not-connect.invalid dbname=reports"})
	m.conn().connected = false
	m.conn().catalog = []catalogNode{{kind: "schema", name: "cached", schema: "cached", parent: -1, loaded: true}}
	m.prepareView()
	defer m.Cancel()
	// Two fast disclosure clicks must collapse then expand, even though
	// double-clicking the row label would explicitly request reconnection.
	for click := 0; click < 2; click++ {
		if cmd := mouseClick(m, 1, 4); cmd != nil || m.connecting || anyCatalogBusy(m) || m.modal.kind != "" {
			t.Fatal("disclosure activation prepared a connection or catalog request")
		}
		mouseRelease(m, 1, 4)
		if m.conn().collapsed != (click == 0) {
			t.Fatal("disclosure did not toggle the cached tree exactly once")
		}
	}
}

func TestMouseCatalogArrivalDoesNotDoubleActivateDifferentDatabase(t *testing.T) {
	m := New(Config{Demo: true, Profiles: paneProfiles(2)})
	m.conn().catalog = nil
	m.prepareView()
	// Add, active database, Reports 0, Reports 1 occupy y=3..6.
	if cmd := mouseClick(m, 8, 6); cmd != nil {
		t.Fatal("first saved-row click emitted an activation command")
	}
	mouseRelease(m, 8, 6)
	selected := m.databaseRows()[m.databasePane.index]
	if selected.kind != databaseSaved || selected.profile != 1 {
		t.Fatal("fixture did not click the second saved database")
	}
	// Metadata adds a schema above the saved rows. The same coordinate now
	// belongs to Reports 0, so another click is a new selection, not a double
	// activation inherited from Reports 1.
	m.Update(catalogMsg{conn: m.conn().id, serial: m.conn().catalogSerial, parent: -1, nodes: []catalogNode{{kind: "schema", name: "loaded", schema: "loaded", parent: -1, loaded: true}}})
	if cmd := mouseClick(m, 8, 6); cmd != nil || m.connecting || m.modal.kind != "" {
		t.Fatal("metadata-shifted row inherited another profile's double-click activation")
	}
	mouseRelease(m, 8, 6)
	selected = m.databaseRows()[m.databasePane.index]
	if selected.kind != databaseSaved || selected.profile != 0 {
		t.Fatal("second click did not select the profile now visible at its coordinate")
	}
}

func TestMouseModalWheelResetsDoubleClickTarget(t *testing.T) {
	m := mouseFixture("SELECT 1;")
	m.openPrompt("palette", "Commands", "")
	m.prepareView()
	var last dialogHit
	for _, hit := range m.promptLayout(m.width, m.dimensions().bodyHeight).hits {
		if hit.kind == "list" {
			last = hit
		}
	}
	if last.kind != "list" {
		t.Fatal("palette fixture has no visible items")
	}
	x, y := last.area.x+3, last.area.y
	mouseClick(m, x, y)
	mouseRelease(m, x, y)
	firstIndex := m.modal.index
	mouseWheel(m, x, y, tea.MouseWheelDown)
	if m.modal.index <= firstIndex {
		t.Fatal("wheel did not move the palette's visible item window")
	}
	if cmd := mouseClick(m, x, y); cmd != nil || m.modal.kind != "palette" || anyRunning(m) || len(m.buffers) != 1 {
		t.Fatal("click after modal wheel activated a new item as a stale double click")
	}
	mouseRelease(m, x, y)
	if m.modal.index <= firstIndex {
		t.Fatal("click did not select the new item under the pointer")
	}
}

func TestMouseModalDoesNotClickThroughAndResizeEndsDrag(t *testing.T) {
	m := mouseFixture("unchanged SQL")
	m.openPrompt("find", "Find", "selected prompt")
	caret := m.buffers[0].editor.cursor
	// Behind the modal, this is the database pane's Add control.
	mouseClick(m, 2, 3)
	mouseMotion(m, 40, 4)
	mouseRelease(m, 40, 4)
	mouseWheel(m, 40, 4, tea.MouseWheelDown)
	if m.modal.kind != "find" || string(m.modal.input.text) != "selected prompt" || m.buffers[0].editor.cursor != caret || m.focus != editorPane || m.buffers[0].editor.top != 0 {
		t.Fatal("modal pointer event affected obscured workspace")
	}
	press(m, tea.KeyEsc)
	mouseClick(m, 33, 4)
	if m.mouse.drag != "editor" {
		t.Fatal("SQL click did not start selection")
	}
	m.Update(tea.WindowSizeMsg{Width: 50, Height: 12})
	caret = m.buffers[0].editor.cursor
	mouseMotion(m, 40, 10)
	if m.mouse.drag != "" || m.buffers[0].editor.cursor != caret {
		t.Fatal("resize left a drag using old global coordinates")
	}
}

func TestMouseDoubleClickCannotDiscardConsecutiveDirtyBuffers(t *testing.T) {
	m := mouseFixture("first")
	m.buffers[0].editor.insert("changed ", "")
	m.addBuffer("second", "", false, m.buffers[0].conn)
	press(m, 'q', tea.ModCtrl)
	if m.modal.kind != "confirm-dirty" {
		t.Fatal("quit did not show dirty confirmation")
	}
	firstBuffer := m.confirmBuffer
	x, y := -1, -1
	for row, line := range strings.Split(ansi.Strip(m.View().Content), "\n") {
		if index := strings.Index(line, "Discard"); index >= 0 {
			x, y = len([]rune(line[:index])), row
			break
		}
	}
	if x < 0 {
		t.Fatal("Discard action is not visible")
	}
	mouseClick(m, x, y)
	mouseRelease(m, x, y)
	if !m.quitDiscard[firstBuffer] || m.modal.kind != "confirm-dirty" || m.confirmBuffer == firstBuffer {
		t.Fatal("explicit Discard did not advance to the second dirty buffer")
	}
	secondBuffer := m.confirmBuffer
	mouseClick(m, x, y)
	mouseRelease(m, x, y)
	if m.quitDiscard[secondBuffer] || m.modal.kind != "confirm-dirty" || m.confirmBuffer != secondBuffer {
		t.Fatal("second click of a double click accepted a newly opened dirty confirmation")
	}
}

func TestMouseGridReplacementInvalidatesDragAndDelayedTicks(t *testing.T) {
	m := mouseFixture("SELECT 1;")
	m.runSQL(false)
	defer m.Cancel()
	x, y := mouseCellPoint(m, 1, 1)
	mouseClick(m, x, y)
	oldSerial := m.mouse.serial
	area := m.gridDataRect()
	mouseMotion(m, area.x+area.width+2, area.y+area.height+2)
	m.Update(queryMsg{conn: m.conn().id, serial: m.conn().query.serial, batch: result.Batch{Results: []result.Set{{Columns: []result.Column{{Name: "new", OID: 25}}, Rows: [][]result.Cell{{{Text: "replacement"}}}}}}})
	if m.mouse.drag != "" || m.results().rowAnchor >= 0 || m.results().row != 0 || m.results().column != 0 {
		t.Fatal("new query results retained a selection drag from the old table")
	}
	m.Update(mouseScrollMsg{serial: oldSerial})
	if m.results().row != 0 || m.results().column != 0 || m.results().gridTop != 0 || m.results().gridLeft != 0 {
		t.Fatal("stale drag tick changed replacement table")
	}
}

func TestQueryCompletionRespectsInterveningInteraction(t *testing.T) {
	for _, action := range []string{"none", "typing", "paste", "SQL click", "pane round trip", "wheel"} {
		t.Run(action, func(t *testing.T) {
			m := mouseFixture("SELECT 1;\nSELECT 2;")
			press(m, 'j', tea.ModCtrl)
			defer m.Cancel()
			switch action {
			case "typing":
				press(m, 'x')
			case "paste":
				m.Update(tea.PasteMsg{Content: "-- local edit\n"})
			case "SQL click":
				mouseClick(m, 35, 4)
				mouseRelease(m, 35, 4)
			case "pane round trip":
				press(m, tea.KeyF6)
				press(m, tea.KeyF2)
			case "wheel":
				mouseWheel(m, 35, 4, tea.MouseWheelDown)
			}
			m.Update(queryMsg{conn: m.conn().id, serial: m.conn().query.serial, batch: demoBatch()})
			want := editorPane
			if action == "none" {
				want = resultsPane
			}
			if m.focus != want {
				t.Fatalf("query finish after %s: focus=%d, want %d", action, m.focus, want)
			}
		})
	}
}

func TestQueryErrorRespectsCaretMovedByMouse(t *testing.T) {
	m := mouseFixture("SELECT missing;\nSELECT 2;")
	press(m, 'j', tea.ModCtrl)
	defer m.Cancel()
	mouseClick(m, 34, 5)
	mouseRelease(m, 34, 5)
	caret := m.buffers[0].editor.cursor
	m.Update(queryMsg{conn: m.conn().id, serial: m.conn().query.serial, batch: result.Batch{}, err: postgres.Error{Message: "missing column", Position: 8}})
	if m.focus != editorPane || m.buffers[0].editor.cursor != caret {
		t.Fatalf("error moved an intervening user caret: got=%d want=%d", m.buffers[0].editor.cursor, caret)
	}
}

func TestPaneCycleAndFocusKeysRespectHiddenAndNarrowLayouts(t *testing.T) {
	for _, tc := range []struct {
		name        string
		width       int
		height      int
		hideCatalog bool
	}{
		{"split", 100, 30, false},
		{"narrow", 48, 12, false},
		{"hidden", 100, 30, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := mouseFixture("SELECT 1;")
			m.Update(tea.WindowSizeMsg{Width: tc.width, Height: tc.height})
			m.showCatalog = !tc.hideCatalog
			m.buffers[0].editor.cursor, m.buffers[0].editor.anchor = 4, 1
			m.results().row, m.results().column = 3, 2
			m.completion = completion{items: []string{"SELECT"}}
			m.completeAfterCatalog = true
			press(m, tea.KeyTab, tea.ModCtrl)
			if m.focus != resultsPane || len(m.completion.items) != 0 || m.completeAfterCatalog {
				t.Fatal("pane cycle did not dismiss SQL completion")
			}
			press(m, tea.KeyTab, tea.ModCtrl)
			want := catalogPane
			if tc.hideCatalog {
				want = editorPane
			}
			if m.focus != want {
				t.Fatalf("next pane=%d, want=%d", m.focus, want)
			}
			press(m, tea.KeyTab, tea.ModCtrl, tea.ModShift)
			if m.focus != resultsPane {
				t.Fatal("reverse pane cycle did not return to results")
			}
			press(m, tea.KeyF6)
			if m.focus != catalogPane || !m.showCatalog {
				t.Fatal("explicit database focus did not reveal it")
			}
			press(m, tea.KeyF2)
			if m.buffers[0].editor.cursor != 4 || m.buffers[0].editor.anchor != 1 || m.results().row != 3 || m.results().column != 2 {
				t.Fatal("focus round trip lost pane selections")
			}
			m.openPrompt("find", "Find", "")
			press(m, tea.KeyTab, tea.ModCtrl)
			press(m, tea.KeyF5)
			if m.focus != editorPane || m.modal.kind != "find" {
				t.Fatal("explicit pane key escaped an active dialog")
			}
		})
	}
}

func TestArrowGraphTraversesActiveDatabaseAndResultsWithoutShortcuts(t *testing.T) {
	m := mouseFixture("SELECT 1;")
	m.showCatalog = false
	press(m, tea.KeyLeft)
	rows := m.databaseRows()
	if m.focus != catalogPane || !m.showCatalog || rows[m.databasePane.index].kind != databaseConnection {
		t.Fatal("Left at SQL origin did not select the active database")
	}
	press(m, tea.KeyRight)
	if m.focus != editorPane || m.buffers[0].editor.cursor != 0 || anyRunning(m) {
		t.Fatal("one Right from the database pane did not return to original SQL caret")
	}
	press(m, tea.KeyEnd)
	press(m, tea.KeyDown)
	if m.focus != resultsPane {
		t.Fatal("Down at SQL end did not enter results")
	}
	press(m, tea.KeyUp)
	if m.focus != editorPane || m.buffers[0].editor.cursor != len(m.buffers[0].editor.text) {
		t.Fatal("Up from first result row did not restore SQL caret")
	}
	if m.serial != 0 || m.connecting || m.profileBusy || m.discovering {
		t.Fatal("pure arrow navigation started database or persistence work")
	}
}

func TestMouseDoubleClickDetectionExpiresAfterContextChange(t *testing.T) {
	m := mouseFixture("SELECT 1;")
	// An old click at an identical screen coordinate must not turn a new
	// first click after resize into word selection.
	m.mouse.lastX, m.mouse.lastY = 33, 4
	m.mouse.lastClick = time.Now()
	m.mouse.context = "old layout"
	m.mouse.clicks = 1
	mouseClick(m, 33, 4)
	start, end := m.buffers[0].editor.selection()
	if start != end || m.mouse.clicks != 1 {
		t.Fatal("new interaction context reused a stale double click")
	}
}

func TestMouseClippedWorkspaceHasNoHiddenTargets(t *testing.T) {
	for _, size := range [][2]int{{1, 1}, {2, 3}, {3, 4}, {7, 8}, {20, 10}, {48, 12}, {67, 17}, {68, 18}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			m := mouseFixture("SELECT '雪';")
			m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			for _, point := range [][2]int{{-1, -1}, {size[0], 1}, {1, size[1]}, {size[0] - 1, size[1] - 1}} {
				mouseClick(m, point[0], point[1])
				mouseMotion(m, point[0], point[1])
				mouseRelease(m, point[0], point[1])
				mouseWheel(m, point[0], point[1], tea.MouseWheelDown)
			}
			if m.modal.kind != "" || anyRunning(m) || m.connecting || m.profileBusy || string(m.buffers[0].editor.text) != "SELECT '雪';" {
				t.Fatal("out-of-content pointer event activated a hidden action or changed SQL")
			}
		})
	}
}
