package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/dustinevan/scrunchie/pkg/result"
)

// gridFixture has typed columns, equal numeric values written differently,
// SQL NULL, empty text, and the string 'NULL'.
func gridFixture() *Model {
	m := New(Config{})
	m.focus = resultsPane
	m.results().batch.Results = []result.Set{{
		Command: "SELECT 6", RowCount: 6,
		Columns: []result.Column{{Name: "id", Type: "int4", OID: 23}, {Name: "amount", Type: "numeric", OID: 1700}, {Name: "state", Type: "bpchar", OID: 1042}, {Name: "note", Type: "text", OID: 25}},
		Rows: [][]result.Cell{
			{{Text: "1"}, {Text: "10.5"}, {Text: "MD"}, {Text: "alpha"}},
			{{Text: "2"}, {Null: true}, {Text: "VA"}, {Text: "Beta md"}},
			{{Text: "3"}, {Text: "-2"}, {Text: "MD"}, {Text: "NULL"}},
			{{Text: "4"}, {Text: "100"}, {Text: "CA"}, {Null: true}},
			{{Text: "5"}, {Text: "9.99"}, {Text: "TX"}, {Text: "gamma"}},
			{{Text: "6"}, {Text: "10.50"}, {Text: "MD"}, {Text: ""}},
		},
	}}
	m.prepareView()
	return m
}

// viewIDs lists the first column of each displayed row in grid order.
func viewIDs(m *Model) string {
	var ids []string
	for _, index := range m.gridRows() {
		ids = append(ids, m.results().batch.Results[m.results().setIndex].Rows[index][0].Text)
	}
	return strings.Join(ids, ",")
}

func screen(m *Model) string {
	return ansi.Strip(m.View().Content)
}

func clipboard(t *testing.T, cmd tea.Cmd) string {
	t.Helper()
	if cmd == nil {
		t.Fatal("no clipboard command")
	}
	return fmt.Sprint(cmd())
}

func pressText(m *Model, text string) {
	for _, r := range text {
		press(m, r)
	}
}

func applyFilter(m *Model, text string) {
	press(m, 'f')
	m.modal.input = newEditor(text)
	m.modal.input.singleLine = true
	press(m, tea.KeyEnter)
}

func TestGridSortCyclesByTypeWithNullPlacement(t *testing.T) {
	m := gridFixture()
	m.results().column = 1
	press(m, 's')
	if got := viewIDs(m); got != "3,5,1,6,4,2" {
		t.Fatalf("ascending numeric = %s; want exact decimal order, stable ties, NULL last", got)
	}
	if m.status != "Sorted by amount ascending · NULLs last" || !strings.Contains(screen(m), "amount ↑") {
		t.Fatalf("status %q or header arrow missing", m.status)
	}
	press(m, 's')
	if got := viewIDs(m); got != "2,4,1,6,5,3" || !strings.Contains(screen(m), "amount ↓") {
		t.Fatalf("descending numeric = %s; want NULL first and stable ties", got)
	}
	if !strings.Contains(screen(m), "›   1 2 ") {
		t.Fatal("row numbers did not show view positions")
	}
	press(m, 's')
	if got := viewIDs(m); got != "1,2,3,4,5,6" || m.status != "Original row order restored" || strings.Contains(screen(m), "↑") {
		t.Fatalf("third press = %s, status %q", got, m.status)
	}
	m.results().column = 3
	press(m, 's')
	if got := viewIDs(m); got != "6,2,3,1,5,4" {
		t.Fatalf("byte-wise text = %s; want empty, uppercase, 'NULL', lowercase, SQL NULL", got)
	}
	press(m, 's')
	m.results().column = 0
	press(m, 's')
	if got := viewIDs(m); got != "1,2,3,4,5,6" || m.results().view.order != ascending || m.results().view.sortColumn != 0 {
		t.Fatal("another column did not restart at ascending")
	}
}

func TestGridSortReportsPartialResultsAndNeverResortsOnView(t *testing.T) {
	m := gridFixture()
	m.results().batch.Results[0].Truncated = true
	m.results().column = 1
	press(m, 's')
	if m.status != "Sorted the 6 fetched rows client-side by amount ascending · NULLs last · the result is partial" {
		t.Fatalf("partial status = %q", m.status)
	}
	// Changing stored text in place does not change the view until it is
	// rebuilt, which shows View neither sorts nor filters.
	m.results().batch.Results[0].Rows[2][1].Text = "1000"
	for range 3 {
		m.View()
	}
	if got := viewIDs(m); got != "3,5,1,6,4,2" {
		t.Fatalf("View changed the sorted order: %s", got)
	}
	applyFilter(m, "md")
	if !strings.HasSuffix(m.status, " · fetched rows only") {
		t.Fatalf("partial filter status = %q", m.status)
	}
}

func TestGridFilterPromptColumnSyntaxAndEscOrder(t *testing.T) {
	m := gridFixture()
	press(m, 'f')
	if m.modal.kind != "row-filter" || !strings.HasPrefix(m.modal.title, "Filter rows") {
		t.Fatalf("f opened %q %q", m.modal.kind, m.modal.title)
	}
	pressText(m, "sfyMD")
	if string(m.modal.input.text) != "sfyMD" || m.results().view.order != unsorted {
		t.Fatal("letters in the filter prompt did not type")
	}
	m.modal.input = newEditor("md")
	press(m, tea.KeyEnter)
	if got := viewIDs(m); got != "1,2,3,6" || m.status != "filter 'md' · 4 of 6 rows" || !strings.Contains(screen(m), "filter 'md' · 4 of 6 rows") {
		t.Fatalf("filter rows = %s, status %q", got, m.status)
	}
	press(m, 'f')
	if string(m.modal.input.text) != "md" {
		t.Fatal("prompt did not start with the current filter")
	}
	press(m, tea.KeyEsc)
	applyFilter(m, "STATE:md")
	if got := viewIDs(m); got != "1,3,6" || m.status != "filter 'STATE:md' · 3 of 6 rows" {
		t.Fatalf("column filter = %s, status %q", got, m.status)
	}
	// Display text is the filter's subject: SQL NULL is displayed as NULL.
	applyFilter(m, "note:null")
	if got := viewIDs(m); got != "3,4" {
		t.Fatalf("display-text filter = %s", got)
	}
	applyFilter(m, "missing:5")
	if got := viewIDs(m); got != "" {
		t.Fatalf("an unknown column name searched as text: %s", got)
	}
	applyFilter(m, "zzz")
	if !strings.Contains(screen(m), "No rows match · Esc clears the filter") {
		t.Fatal("no-match state was not shown")
	}
	for _, code := range []rune{tea.KeyDown, tea.KeyEnter, 'a', 'y', 's'} {
		press(m, code)
	}
	press(m, 'a', tea.ModCtrl)
	if cmd := press(m, 'c', tea.ModCtrl); cmd != nil || m.inspector.active || m.modal.kind != "" {
		t.Fatal("an empty view produced a copy, inspector, or dialog")
	}
	applyFilter(m, "md")
	m.results().row = 2
	press(m, tea.KeyDown, tea.ModShift)
	press(m, tea.KeyEsc)
	if m.results().rowAnchor != -1 || m.results().view.filter != "md" || m.focus != resultsPane {
		t.Fatal("first Esc did not clear only the rectangle")
	}
	press(m, tea.KeyEsc)
	if m.results().view.filter != "" || m.focus != resultsPane || viewIDs(m) != "1,2,3,4,5,6" || m.results().row != 5 {
		t.Fatalf("second Esc did not clear the filter and keep row id 6: row=%d", m.results().row)
	}
	press(m, tea.KeyEsc)
	if m.focus != editorPane {
		t.Fatal("third Esc did not return to SQL")
	}
}

func TestSortedFilteredViewDrivesCopyInspectorSearchAndExport(t *testing.T) {
	m := gridFixture()
	applyFilter(m, "md")
	m.results().column = 1
	press(m, 's')
	press(m, 's')
	if got := viewIDs(m); got != "2,1,6,3" {
		t.Fatalf("filtered descending view = %s", got)
	}
	m.results().row, m.results().column = 2, 0
	if got := clipboard(t, press(m, 'c', tea.ModCtrl)); got != "6" {
		t.Fatalf("cell copy = %q", got)
	}
	m.results().rowAnchor, m.results().columnAnchor, m.results().row, m.results().column = 1, 0, 3, 1
	if got := clipboard(t, press(m, 'c', tea.ModCtrl)); got != "1\t10.5\n6\t10.50\n3\t-2" {
		t.Fatalf("rectangle copy = %q", got)
	}
	m.results().rowAnchor, m.results().columnAnchor, m.results().row, m.results().column = -1, -1, 1, 3
	press(m, tea.KeyEnter)
	if !m.inspector.active || m.inspector.value.Text != "alpha" || !strings.Contains(screen(m), "row 2 (original row 1)") {
		t.Fatalf("inspector opened %q without the original row", m.inspector.value.Text)
	}
	press(m, tea.KeyEsc)
	press(m, 'a', tea.ModCtrl)
	if m.results().rowAnchor != 0 || m.results().row != 3 {
		t.Fatal("Ctrl+A did not select the visible rows")
	}
	m.results().rowAnchor, m.results().columnAnchor, m.results().row, m.results().column = -1, -1, 0, 0
	m.findNext("gamma")
	if m.results().row != 0 || !strings.Contains(m.status, "filtered rows") {
		t.Fatalf("search found a filtered-out row: row=%d status=%q", m.results().row, m.status)
	}
	m.findNext("alpha")
	if m.results().row != 1 || m.results().column != 3 {
		t.Fatalf("search selected view position %d,%d", m.results().row, m.results().column)
	}
	press(m, 'e', tea.ModCtrl)
	if m.modal.kind != "export" || !strings.Contains(m.modal.title, "sorted, filtered view · 4 of 6 retained rows") {
		t.Fatalf("export title = %q", m.modal.title)
	}
	set := m.fileJob.set
	if len(set.Rows) != 4 || set.Rows[0][0].Text != "2" || set.Rows[3][0].Text != "3" || set.RowCount != 4 || len(m.results().batch.Results[0].Rows) != 6 {
		t.Fatal("export did not use the view's rows in order without changing the result")
	}
}

func TestMouseHitTestingUsesSortedFilteredRows(t *testing.T) {
	m := mouseFixture("SELECT 1;")
	m.focus = resultsPane
	applyFilter(m, "r1")
	m.results().column = 0
	press(m, 's')
	press(m, 's')
	// Byte-wise descending puts r1c0 ('c' > '9') before r19c0 … r10c0.
	if got := viewIDs(m); got != "r1c0,r19c0,r18c0,r17c0,r16c0,r15c0,r14c0,r13c0,r12c0,r11c0,r10c0" {
		t.Fatalf("view = %s", got)
	}
	x, y := mouseCellPoint(m, 2, 1)
	mouseClick(m, x, y)
	mouseRelease(m, x, y)
	if got := clipboard(t, press(m, 'c', tea.ModCtrl)); got != "r18c1" {
		t.Fatalf("clicked cell copied %q", got)
	}
	x, y = mouseCellPoint(m, 0, 0)
	mouseClick(m, x, y)
	x, y = mouseCellPoint(m, 1, 1)
	mouseMotion(m, x, y)
	mouseRelease(m, x, y)
	if got := clipboard(t, press(m, 'c', tea.ModCtrl)); got != "r1c0\tr1c1\nr19c0\tr19c1" {
		t.Fatalf("dragged rectangle copied %q", got)
	}
	area := m.gridDataRect()
	mouseClick(m, area.x+2, area.y+3)
	mouseRelease(m, area.x+2, area.y+3)
	if got := clipboard(t, press(m, 'c', tea.ModCtrl)); got != "r17c0\tr17c1\tr17c2\tr17c3\tr17c4" {
		t.Fatalf("row-number click copied %q", got)
	}
	content := m.paneRect(resultsPane).content()
	x, _ = mouseCellPoint(m, 0, 2)
	mouseClick(m, x, content.y+1)
	mouseRelease(m, x, content.y+1)
	if m.results().rowAnchor != 0 || m.results().row != 10 || m.results().column != 2 {
		t.Fatalf("column header selected rows %d-%d", m.results().rowAnchor, m.results().row)
	}
	for range 10 {
		mouseWheel(m, area.x+10, area.y+1, tea.MouseWheelDown)
	}
	if m.results().gridTop > max(0, 11-area.height) {
		t.Fatalf("wheel scrolled past the view: top=%d", m.results().gridTop)
	}
	m.results().gridTop = 0
	x, y = mouseCellPoint(m, 4, 3)
	mouseClick(m, x, y)
	mouseRelease(m, x, y)
	mouseClick(m, x, y)
	if !m.inspector.active || m.inspector.value.Text != "r16c3" || m.inspector.source != 16 || m.inspector.row != 4 {
		t.Fatalf("double click inspected %q", m.inspector.value.Text)
	}
}

func TestRowViewResetsOnNewBatchAndResultSwitchButFollowsMoreRows(t *testing.T) {
	m := gridFixture()
	m.results().batch.Results = append(m.results().batch.Results, result.Set{Command: "SELECT 1", Columns: []result.Column{{Name: "x", OID: 25}}, Rows: [][]result.Cell{{{Text: "only"}}}})
	m.results().widths = nil
	m.prepareView()
	m.results().column = 1
	press(m, 's')
	applyFilter(m, "1")
	if viewIDs(m) != "1,6,4" {
		t.Fatalf("setup view = %s", viewIDs(m))
	}
	press(m, ']')
	if m.results().setIndex != 1 || m.results().view.changed() || m.results().view.set != &m.results().batch.Results[1] {
		t.Fatal("switching results kept the sort or filter")
	}
	press(m, '[')
	if m.results().view.changed() || viewIDs(m) != "1,2,3,4,5,6" {
		t.Fatal("returning to a result kept its old view")
	}
	m.results().column = 1
	press(m, 's')
	m.results().row = 3
	// The same result retaining more rows keeps its sort and the cursor's row.
	m.results().batch.Results[0].Rows = append(m.results().batch.Results[0].Rows, []result.Cell{{Text: "7"}, {Text: "-50"}, {Text: "MD"}, {Text: "late"}})
	m.prepareView()
	if got := viewIDs(m); got != "7,3,5,1,6,4,2" || m.results().row != 4 {
		t.Fatalf("appended rows = %s, row=%d", got, m.results().row)
	}
	c := offlineConnection(m)
	c.query = queryState{running: true, serial: 9, sql: "SELECT * FROM next_table;", buffer: m.buffers[0].id}
	m.Update(queryMsg{conn: c.id, serial: 9, batch: result.Batch{Results: []result.Set{{Command: "SELECT 1", Columns: []result.Column{{Name: "n", OID: 23}}, Rows: [][]result.Cell{{{Text: "2"}}, {{Text: "1"}}}}}}})
	if m.results().view.changed() || viewIDs(m) != "2,1" || m.results().sql != "SELECT * FROM next_table;" {
		t.Fatal("a new batch kept the previous sort or SQL")
	}
	c.query = queryState{running: true, serial: 10, buffer: m.buffers[0].id}
	m.Update(queryMsg{conn: c.id, serial: 10, batch: result.Batch{}})
	if m.results().view.set != nil || m.results().sql != "" {
		t.Fatal("an empty batch retained the previous view or SQL")
	}
}

func TestSortAndFilterWidenColumnsForVisibleRows(t *testing.T) {
	m := gridFixture()
	m.results().batch.Results[0].Rows[5][3].Text = strings.Repeat("w", 40)
	m.results().widths = nil
	m.prepareView()
	before := m.results().widths[0][3]
	m.results().widths[0][1] = 30
	applyFilter(m, "www")
	if m.results().widths[0][3] <= before || m.results().widths[0][3] != 34 || m.results().widths[0][1] != 30 {
		t.Fatalf("widths after filter = %v (before %d)", m.results().widths[0], before)
	}
}

func TestCopyAsFormatsScopesAndMouse(t *testing.T) {
	m := gridFixture()
	m.results().sql = "SELECT id, amount, state, note FROM claims.beneficiary WHERE id < 10;"
	m.results().column = 1
	press(m, 'y')
	if m.modal.kind != "copy-as" || !strings.Contains(screen(m), "Current row 1 · IN list from this cell (amount)") || !strings.Contains(screen(m), "SQL INSERT statements") {
		t.Fatal("copy-as dialog did not describe the current-row scope")
	}
	if cmd := press(m, 'j'); cmd != nil || m.modal.kind != "copy-as" {
		t.Fatal("a letter acted in the copy-as list")
	}
	if got := clipboard(t, press(m, tea.KeyEnter)); got != "id\tamount\tstate\tnote\n1\t10.5\tMD\talpha" || m.status != "Copied 1 row as TSV" {
		t.Fatalf("TSV = %q, status %q", got, m.status)
	}
	press(m, 'y')
	press(m, tea.KeyDown)
	if got := clipboard(t, press(m, tea.KeySpace)); got != "\"id\",\"amount\",\"state\",\"note\"\n\"1\",\"10.5\",\"MD\",\"alpha\"\n" || m.status != "Copied 1 row as CSV" {
		t.Fatalf("CSV = %q, status %q", got, m.status)
	}
	press(m, 'y')
	if m.modal.index != 1 {
		t.Fatal("copy-as did not remember the last format")
	}
	press(m, tea.KeyEsc)
	if m.modal.kind != "" {
		t.Fatal("Esc did not close copy-as")
	}
	m.results().rowAnchor, m.results().columnAnchor, m.results().row, m.results().column = 1, 1, 2, 3
	m.results().column = 1
	m.results().columnAnchor = 3
	press(m, 'y')
	if !strings.Contains(screen(m), "Selection: 2 rows × 3 columns · IN list from amount") {
		t.Fatal("copy-as dialog did not describe the rectangle")
	}
	hit := func(index int) (int, int) {
		for _, hit := range m.promptLayout(m.width, m.dimensions().bodyHeight).hits {
			if hit.kind == "list" && hit.index == index {
				return hit.area.x + 2, hit.area.y
			}
		}
		t.Fatalf("no list hit %d", index)
		return 0, 0
	}
	x, y := hit(3)
	if cmd := mouseClick(m, x, y); cmd != nil || m.modal.index != 3 {
		t.Fatal("a single click did not only select JSON")
	}
	mouseRelease(m, x, y)
	want := "[\n" +
		`  {"amount": null, "state": "VA", "note": "Beta md"},` + "\n" +
		`  {"amount": "-2", "state": "MD", "note": "NULL"}` + "\n]"
	if got := clipboard(t, mouseClick(m, x, y)); got != want || m.status != "Copied 2 rows as JSON" {
		t.Fatalf("JSON = %s, status %q", got, m.status)
	}
	press(m, 'y')
	m.modal.index = 4
	want = "INSERT INTO claims.beneficiary (amount, state, note) VALUES (NULL, 'VA', 'Beta md');\n" +
		"INSERT INTO claims.beneficiary (amount, state, note) VALUES (-2, 'MD', 'NULL');"
	if got := clipboard(t, press(m, tea.KeyEnter)); got != want || m.status != "Copied 2 rows as SQL INSERT statements" {
		t.Fatalf("INSERT = %s, status %q", got, m.status)
	}
	press(m, 'y')
	m.modal.index = 5
	if got := clipboard(t, press(m, tea.KeyEnter)); got != "(-2)" || m.status != "Copied 1 value of amount as SQL IN list · skipped 1 NULL; IN never matches NULL" {
		t.Fatalf("IN = %s, status %q", got, m.status)
	}
	m.results().rowAnchor, m.results().columnAnchor, m.results().row, m.results().column = -1, -1, 1, 1
	press(m, 'y')
	m.modal.index = 5
	if cmd := press(m, tea.KeyEnter); cmd != nil || !strings.Contains(m.status, "Only SQL NULL") {
		t.Fatal("an IN list of only NULL was copied")
	}
	m.results().row, m.results().column = 4, 3
	press(m, 'y')
	m.modal.index = 2
	if got := clipboard(t, press(m, tea.KeyEnter)); got != "| id | amount | state | note |\n| ---: | ---: | --- | --- |\n| 5 | 9.99 | TX | gamma |" {
		t.Fatalf("Markdown = %q", got)
	}
	m.results().sql = "SELECT * FROM a JOIN b USING (id);"
	press(m, 'y')
	m.modal.index = 4
	if got := clipboard(t, press(m, tea.KeyEnter)); !strings.HasPrefix(got, "INSERT INTO table_name (id, amount, state, note) VALUES (5, 9.99, 'TX', 'gamma');") || !strings.HasSuffix(m.status, "table_name is a placeholder") {
		t.Fatalf("placeholder INSERT = %q, status %q", got, m.status)
	}
	m.action("copy-as")
	if m.modal.kind != "copy-as" {
		t.Fatal("palette did not open copy-as")
	}
}

func TestCopyAsFollowsSortedFilteredView(t *testing.T) {
	m := gridFixture()
	applyFilter(m, "state:md")
	m.results().column = 1
	press(m, 's')
	press(m, 'a', tea.ModCtrl)
	m.results().column = 1
	press(m, 'y')
	m.modal.index = 5
	if got := clipboard(t, press(m, tea.KeyEnter)); got != "(-2, 10.5, 10.50)" {
		t.Fatalf("IN list over view = %s", got)
	}
}

func TestNullCellCopyIsEmptyButRectanglesKeepBackslashN(t *testing.T) {
	m := gridFixture()
	m.results().row, m.results().column = 1, 1
	if got := clipboard(t, press(m, 'c', tea.ModCtrl)); got != "" || m.status != "Copied SQL NULL as empty text" {
		t.Fatalf("NULL cell copy = %q, status %q", got, m.status)
	}
	m.results().row, m.results().column = 2, 3
	if got := clipboard(t, press(m, 'c', tea.ModCtrl)); got != "NULL" {
		t.Fatalf("the string 'NULL' copied %q", got)
	}
	m.results().batch.Results[0].Rows[0][3].Text = `\N`
	m.results().rowAnchor, m.results().columnAnchor, m.results().row, m.results().column = 0, 1, 1, 3
	if got := clipboard(t, press(m, 'c', tea.ModCtrl)); got != "10.5\tMD\t\"\\N\"\n\\N\tVA\tBeta md" {
		t.Fatalf("rectangle copy = %q", got)
	}
	m.results().rowAnchor, m.results().columnAnchor, m.results().row, m.results().column = -1, -1, 3, 3
	press(m, tea.KeyEnter)
	if got := clipboard(t, press(m, 'c', tea.ModCtrl)); !m.inspector.active || got != "" || m.status != "Copied SQL NULL as empty text" {
		t.Fatalf("inspector NULL copy = %q", got)
	}
}

func TestTextInspectorHorizontalScrollStopsAtLongestVisibleLine(t *testing.T) {
	m := gridFixture()
	m.results().batch.Results[0].Rows[0][3].Text = strings.Repeat("x", 300) + "\nshort"
	m.results().row, m.results().column = 0, 3
	press(m, tea.KeyEnter)
	limit := 300 - (m.width - 4)
	for range 60 {
		press(m, tea.KeyRight)
	}
	if m.inspector.left != limit {
		t.Fatalf("Right scrolled to %d, want %d", m.inspector.left, limit)
	}
	press(m, tea.KeyRight, tea.ModAlt)
	mouseWheel(m, 10, 6, tea.MouseWheelRight)
	if m.inspector.left != limit || !strings.Contains(screen(m), "xxxx  │") {
		t.Fatalf("Alt+Right or wheel passed the limit: %d", m.inspector.left)
	}
	press(m, tea.KeyLeft)
	if m.inspector.left != limit-8 {
		t.Fatalf("one Left moved to %d", m.inspector.left)
	}
	press(m, tea.KeyEsc)
	m.results().row = 4
	press(m, tea.KeyEnter)
	press(m, tea.KeyRight)
	if m.inspector.left != 0 {
		t.Fatal("a short value scrolled horizontally")
	}
}

func TestPaletteOffersSortFilterAndCopyAs(t *testing.T) {
	ids := map[string]bool{}
	for _, item := range commands {
		ids[item.id] = true
	}
	if !ids["sort"] || !ids["filter-rows"] || !ids["copy-as"] {
		t.Fatal("palette is missing sort, filter, or copy-as")
	}
	m := gridFixture()
	m.focus = editorPane
	m.results().column = 1
	m.action("sort")
	if m.focus != resultsPane || viewIDs(m) != "3,5,1,6,4,2" {
		t.Fatal("palette sort did not sort results")
	}
	m.action("filter-rows")
	if m.modal.kind != "row-filter" {
		t.Fatal("palette did not open the row filter")
	}
}

func TestSortTenThousandRowsOncePerChange(t *testing.T) {
	m := New(Config{})
	m.focus = resultsPane
	set := result.Set{Columns: []result.Column{{Name: "n", OID: 701}, {Name: "d", OID: 1184}}}
	for i := 0; i < 10000; i++ {
		set.Rows = append(set.Rows, []result.Cell{{Text: fmt.Sprintf("%de-3", (i*7919)%10007)}, {Text: fmt.Sprintf("2024-%02d-%02d 10:00:00+%02d", 1+i%12, 1+i%28, i%13)}})
	}
	m.results().batch.Results = []result.Set{set}
	m.prepareView()
	started := time.Now()
	press(m, 's')
	m.results().column = 1
	press(m, 's')
	applyFilter(m, "2024-0")
	for range 20 {
		m.View()
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("sorting and rendering 10,000 rows took %s", elapsed)
	}
	rows := m.gridRows()
	for i := 1; i < len(rows); i++ {
		if m.results().batch.Results[0].Rows[rows[i-1]][1].Text > "2024-10" || m.results().batch.Results[0].Rows[rows[i]][1].Text > "2024-10" {
			t.Fatal("filter kept a non-matching row")
		}
	}
	if len(rows) == 0 || len(rows) == 10000 {
		t.Fatalf("filter kept %d rows", len(rows))
	}
}

func TestArrivingResultsCancelAnOpenCopyAsDialog(t *testing.T) {
	m := gridFixture()
	c := offlineConnection(m)
	c.query = queryState{running: true, serial: 3, buffer: m.buffers[0].id}
	press(m, 'y')
	m.Update(queryMsg{conn: c.id, serial: 3, batch: result.Batch{Results: []result.Set{{Command: "SELECT 1", Columns: []result.Column{{Name: "n", OID: 23}}, Rows: [][]result.Cell{{{Text: "1"}}}}}}})
	if m.modal.kind != "" || !strings.HasPrefix(m.status, "1 result") || !strings.HasSuffix(m.status, " · Copy as canceled because results changed") {
		t.Fatalf("copy-as stayed open over new results: %q %q", m.modal.kind, m.status)
	}
	if cmd := press(m, tea.KeyEnter); cmd != nil {
		t.Fatal("Enter after cancellation copied the new result")
	}
}

func TestFilterFollowsDisplayFormatChanges(t *testing.T) {
	m := New(Config{})
	m.focus = resultsPane
	m.results().batch.Results = []result.Set{{Columns: []result.Column{{Name: "raw", Type: "bytea", OID: 17}}, Rows: [][]result.Cell{{{Bytes: []byte("abc")}}, {{Bytes: []byte("xyz")}}}}}
	m.prepareView()
	applyFilter(m, "abc")
	press(m, 'v')
	if len(m.gridRows()) != 1 {
		t.Fatal("text display lost the matching row")
	}
	press(m, 'v')
	if len(m.gridRows()) != 0 || !strings.Contains(screen(m), "No rows match") {
		t.Fatal("JSON display did not refilter the displayed text")
	}
}
