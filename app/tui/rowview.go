package tui

import (
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/dustinevan/scrunchie/lib/data/files"
	"github.com/dustinevan/scrunchie/lib/logic/cell"
	"github.com/dustinevan/scrunchie/lib/logic/sqledit"
	"github.com/dustinevan/scrunchie/pkg/result"
	"github.com/dustinevan/scrunchie/pkg/sqltext"
	"github.com/dustinevan/scrunchie/pkg/termtext"
)

type sortOrder uint8

const (
	unsorted sortOrder = iota
	ascending
	descending
)

// rowView is the current result set as the grid shows it: retained row
// indices after the client-side filter, then a stable sort. Grid positions
// (r.row, r.rowAnchor, r.gridTop) index rows, never set.Rows directly. It is
// rebuilt only when the filter, sort, or retained rows change.
type rowView struct {
	set           *result.Set
	count         int
	columns       int
	rows          []int
	sortColumn    int
	order         sortOrder
	filter        string
	needle        string
	filterColumns []int
}

func (v rowView) changed() bool {
	return v.order != unsorted || v.filter != ""
}

// gridRows maps grid positions to retained row indices for the current
// result. Between a result change and the next prepareView, it returns
// retained order so every position stays in range.
func (m *Model) gridRows() []int {
	r := m.results()
	if r.setIndex < 0 || r.setIndex >= len(r.batch.Results) {
		return nil
	}
	set := &r.batch.Results[r.setIndex]
	if r.view.set == set && r.view.count == len(set.Rows) && r.view.columns == len(set.Columns) {
		return r.view.rows
	}
	rows := make([]int, len(set.Rows))
	for i := range rows {
		rows[i] = i
	}
	return rows
}

// syncRowView starts each new batch or result set in retained order. When
// the same result retains more rows, it keeps the sort and filter and the
// cursor's row.
func (m *Model) syncRowView() {
	r := m.results()
	var set *result.Set
	if r.setIndex >= 0 && r.setIndex < len(r.batch.Results) {
		set = &r.batch.Results[r.setIndex]
	}
	v := &r.view
	if v.set == set && (set == nil || v.count == len(set.Rows) && v.columns == len(set.Columns)) {
		return
	}
	if m.modal.kind == "copy-as" && v.set != nil {
		// The open dialog describes a selection that no longer exists.
		m.modal = modal{}
		m.status += " · Copy as canceled because results changed"
	}
	if set == nil {
		r.view, r.sql = rowView{}, ""
		return
	}
	if v.set != set || v.columns != len(set.Columns) {
		r.view = rowView{set: set}
		m.buildRowView()
		return
	}
	m.refreshRowView()
}

// refreshRowView rebuilds the view after its inputs change. If earlier
// positions moved, the rectangle would cover different rows, so it is
// cleared and the cursor follows its retained row when that row is shown.
func (m *Model) refreshRowView() {
	r := m.results()
	v := &r.view
	previous := v.rows
	current := -1
	if r.row >= 0 && r.row < len(previous) {
		current = previous[r.row]
	}
	m.buildRowView()
	stable := len(previous) <= len(v.rows)
	for i := 0; stable && i < len(previous); i++ {
		stable = previous[i] == v.rows[i]
	}
	if stable {
		return
	}
	r.rowAnchor, r.columnAnchor = -1, -1
	for position, index := range v.rows {
		if index == current {
			r.row = position
			return
		}
	}
}

func (m *Model) openRowFilter() {
	r := m.results()
	m.syncRowView()
	if r.view.set == nil || len(r.view.set.Columns) == 0 {
		m.status, m.statusError = "There is no result table to filter", false
		return
	}
	m.focusPane(resultsPane)
	m.openPrompt("row-filter", "Filter rows · text, or column:text for one column", r.view.filter)
}

// buildRowView filters on each column's displayed text, case-insensitively,
// then stably sorts by the selected column's PostgreSQL type.
func (m *Model) buildRowView() {
	r := m.results()
	v := &r.view
	set := v.set
	v.count, v.columns = len(set.Rows), len(set.Columns)
	v.rows = make([]int, 0, len(set.Rows))
	formats := make([]cell.Format, len(set.Columns))
	searched := make([]bool, len(set.Columns))
	for col := range set.Columns {
		formats[col] = cell.Auto
		if r.setIndex < len(r.formats) && r.formats[r.setIndex] != nil {
			if format, ok := r.formats[r.setIndex][col]; ok {
				formats[col] = format
			}
		}
		searched[col] = v.filterColumns == nil
	}
	for _, col := range v.filterColumns {
		searched[col] = true
	}
	needle := strings.ToLower(v.needle)
	for index, row := range set.Rows {
		match := v.filter == ""
		for col := 0; !match && col < len(set.Columns) && col < len(row); col++ {
			if searched[col] {
				match = strings.Contains(strings.ToLower(cell.Display(row[col], set.Columns[col], formats[col])), needle)
			}
		}
		if match {
			v.rows = append(v.rows, index)
		}
	}
	if v.order != unsorted && v.sortColumn < len(set.Columns) {
		cell.Sort(set.Rows, v.rows, v.sortColumn, set.Columns[v.sortColumn], v.order == descending)
	}
}

// cycleSort moves the selected column through ascending, descending, and
// retained order. Sorting is client-side over retained rows only.
func (m *Model) cycleSort() {
	r := m.results()
	m.syncRowView()
	v := &r.view
	if v.set == nil || len(v.set.Columns) == 0 {
		m.status, m.statusError = "There is no result table to sort", false
		return
	}
	column := max(0, min(r.column, len(v.set.Columns)-1))
	switch {
	case v.order == unsorted || v.sortColumn != column:
		v.sortColumn, v.order = column, ascending
	case v.order == ascending:
		v.order = descending
	default:
		v.order = unsorted
	}
	m.buildRowView()
	r.row, r.gridTop = 0, 0
	r.rowAnchor, r.columnAnchor = -1, -1
	m.gridScrolling = false
	m.fitColumnWidths()
	m.statusError = false
	if v.order == unsorted {
		m.status = "Original row order restored"
		return
	}
	direction := "ascending · NULLs last"
	if v.order == descending {
		direction = "descending · NULLs first"
	}
	name := v.set.Columns[column].Name
	m.status = "Sorted by " + name + " " + direction
	if v.set.Truncated || v.set.HasMore {
		m.status = "Sorted the " + countText(v.count) + " fetched rows client-side by " + name + " " + direction + " · the result is partial"
	}
}

// filterRows applies text, or column:text for one column, to displayed cell
// text. A new filter starts at the first match; clearing keeps the cursor's
// row selected so it can be seen in context.
func (m *Model) filterRows(text string) {
	r := m.results()
	m.syncRowView()
	v := &r.view
	if v.set == nil || len(v.set.Columns) == 0 {
		m.status, m.statusError = "There is no result table to filter", false
		return
	}
	v.filter, v.needle, v.filterColumns = text, text, nil
	// The longest matching column name wins, so names may contain colons.
	for i := 0; i < len(text); i++ {
		if text[i] != ':' {
			continue
		}
		var columns []int
		for index, column := range v.set.Columns {
			if strings.EqualFold(column.Name, text[:i]) {
				columns = append(columns, index)
			}
		}
		if len(columns) > 0 {
			v.filterColumns, v.needle = columns, text[i+1:]
		}
	}
	m.gridScrolling = false
	m.statusError = false
	if text == "" {
		m.refreshRowView()
		m.status = "Row filter cleared · " + countText(len(v.rows)) + " rows"
		return
	}
	m.buildRowView()
	r.row, r.gridTop = 0, 0
	r.rowAnchor, r.columnAnchor = -1, -1
	m.fitColumnWidths()
	m.status = "filter '" + text + "' · " + countText(len(v.rows)) + " of " + countText(v.count) + " rows"
	if v.set.Truncated || v.set.HasMore {
		m.status += " · fetched rows only"
	}
}

// fitColumnWidths widens the current result's columns for the first rows the
// view shows. It never narrows a column, so manual widths survive.
func (m *Model) fitColumnWidths() {
	r := m.results()
	v := r.view
	if v.set == nil || r.setIndex >= len(r.widths) {
		return
	}
	for col := range r.widths[r.setIndex] {
		if col < len(v.set.Columns) {
			r.widths[r.setIndex][col] = max(r.widths[r.setIndex][col], columnWidth(*v.set, col, v.rows))
		}
	}
}

// columnWidth estimates a grid column from its name and up to three rows,
// given as retained row indices in display order.
func columnWidth(set result.Set, column int, rows []int) int {
	width := max(10, min(34, lipgloss.Width(termtext.Safe(set.Columns[column].Name))+2))
	for _, row := range rows[:min(3, len(rows))] {
		if column < len(set.Rows[row]) {
			width = max(width, min(34, lipgloss.Width(cell.Display(set.Rows[row][column], set.Columns[column], cell.Auto))+2))
		}
	}
	return width
}

type copyFormat struct {
	id    string
	label string
}

var copyFormats = []copyFormat{
	{"tsv", "TSV with header"},
	{"csv", "CSV with header"},
	{"markdown", "Markdown table"},
	{"json", "JSON array of objects"},
	{"insert", "SQL INSERT statements"},
	{"in", "SQL IN list (selected column)"},
}

// openCopyAs lists copy formats for the rectangle, or for the current row
// (IN list: current cell) when nothing larger is selected.
func (m *Model) openCopyAs() {
	r := m.results()
	_, column, ok := m.selectedCell()
	if !ok {
		m.status, m.statusError = "Select a result cell to copy", false
		return
	}
	m.focusPane(resultsPane)
	scope := fmt.Sprintf("Current row %d · IN list from this cell (%s)", r.row+1, column.Name)
	if r.rowAnchor >= 0 && (r.rowAnchor != r.row || r.columnAnchor != r.column) {
		rows, columns := max(r.row, r.rowAnchor)-min(r.row, r.rowAnchor)+1, max(r.column, r.columnAnchor)-min(r.column, r.columnAnchor)+1
		scope = fmt.Sprintf("Selection: %s × %s · IN list from %s", plural(rows, "row"), plural(columns, "column"), column.Name)
	}
	m.modal = modal{kind: "copy-as", title: "Copy as…", message: scope, index: max(0, min(m.copyAsIndex, len(copyFormats)-1))}
}

// copyAs encodes the selection with PostgreSQL text values, independent of
// display formats, and sends it through the terminal clipboard.
func (m *Model) copyAs(id string) tea.Cmd {
	r := m.results()
	rows := m.gridRows()
	if r.setIndex < 0 || r.setIndex >= len(r.batch.Results) || len(rows) == 0 || len(r.batch.Results[r.setIndex].Columns) == 0 {
		m.status, m.statusError = "Select a result cell to copy", false
		return nil
	}
	set := r.batch.Results[r.setIndex]
	row, column := max(0, min(r.row, len(rows)-1)), max(0, min(r.column, len(set.Columns)-1))
	top, bottom, left, right := row, row, 0, len(set.Columns)-1
	if r.rowAnchor >= 0 && (r.rowAnchor != r.row || r.columnAnchor != r.column) {
		anchorRow, anchorColumn := max(0, min(r.rowAnchor, len(rows)-1)), max(0, min(r.columnAnchor, len(set.Columns)-1))
		top, bottom = min(row, anchorRow), max(row, anchorRow)
		left, right = min(column, anchorColumn), max(column, anchorColumn)
	}
	// Literals follow the string mode of the connection that produced the rows.
	var backslashEscapes bool
	if c := m.connectionFor(r.conn); c != nil {
		backslashEscapes = c.session.BackslashEscapes()
	}
	var text, status string
	if id == "in" {
		var values []result.Cell
		nulls := 0
		for position := top; position <= bottom; position++ {
			source := set.Rows[rows[position]]
			if column >= len(source) || source[column].Null {
				nulls++
				continue
			}
			values = append(values, source[column])
		}
		if len(values) == 0 {
			m.status, m.statusError = "Only SQL NULL is selected · an IN list never matches NULL", false
			return nil
		}
		text = cell.InList(values, set.Columns[column], backslashEscapes)
		status = "Copied " + plural(len(values), "value") + " of " + set.Columns[column].Name + " as SQL IN list"
		if nulls > 0 {
			status += " · skipped " + plural(nulls, "NULL") + "; IN never matches NULL"
		}
	} else {
		table := cell.Table{Columns: set.Columns[left : right+1]}
		for position := top; position <= bottom; position++ {
			source := set.Rows[rows[position]]
			if right < len(source) {
				table.Rows = append(table.Rows, source[left:right+1])
				continue
			}
			cells := make([]result.Cell, right-left+1)
			for col := left; col <= right; col++ {
				cells[col-left] = result.Cell{Null: true}
				if col < len(source) {
					cells[col-left] = source[col]
				}
			}
			table.Rows = append(table.Rows, cells)
		}
		label := ""
		switch id {
		case "tsv":
			text, label = cell.TSV(table), "TSV"
		case "csv":
			var output strings.Builder
			if err := files.Write(&output, result.Set{Columns: table.Columns, Rows: table.Rows}, "csv"); err != nil {
				m.status, m.statusError = err.Error(), true
				return nil
			}
			text, label = output.String(), "CSV"
		case "markdown":
			text, label = cell.Markdown(table), "Markdown"
		case "json":
			text, label = cell.JSONObjects(table), "JSON"
		case "insert":
			var relation []string
			statements := sqltext.SplitMode(r.sql, backslashEscapes)
			if len(statements) == len(r.batch.Results) && r.setIndex < len(statements) {
				relation, _ = sqledit.Relation(statements[r.setIndex].Text)
			}
			text, label = cell.Inserts(table, relation, backslashEscapes), "SQL INSERT statements"
			if relation == nil {
				label += " · table_name is a placeholder"
			}
		default:
			return nil
		}
		status = "Copied " + plural(len(table.Rows), "row") + " as " + label
	}
	if len(text) > 4<<20 {
		m.status, m.statusError = "Copy exceeds the 4 MiB clipboard limit; use Export", true
		return nil
	}
	m.status, m.statusError = status, false
	return tea.SetClipboard(text)
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return countText(n) + " " + noun + "s"
}

// countText writes n with thousands separators, as in 1,000.
func countText(n int) string {
	digits := strconv.Itoa(n)
	sign := ""
	if strings.HasPrefix(digits, "-") {
		sign, digits = "-", digits[1:]
	}
	for i := len(digits) - 3; i > 0; i -= 3 {
		digits = digits[:i] + "," + digits[i:]
	}
	return sign + digits
}
