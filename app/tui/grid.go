package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/dustinevan/scrunchie/lib/logic/cell"
	"github.com/dustinevan/scrunchie/pkg/result"
)

func (m *Model) selectedCell() (result.Cell, result.Column, bool) {
	r := m.results()
	if r.setIndex < 0 || r.setIndex >= len(r.batch.Results) {
		return result.Cell{}, result.Column{}, false
	}
	set := r.batch.Results[r.setIndex]
	rows := m.gridRows()
	if r.row < 0 || r.row >= len(rows) || r.column < 0 || r.column >= len(set.Columns) || r.column >= len(set.Rows[rows[r.row]]) {
		return result.Cell{}, result.Column{}, false
	}
	return set.Rows[rows[r.row]][r.column], set.Columns[r.column], true
}

func (m *Model) columnFormat() cell.Format {
	r := m.results()
	if r.setIndex < len(r.formats) && r.formats[r.setIndex] != nil {
		if format, ok := r.formats[r.setIndex][r.column]; ok {
			return format
		}
	}
	return cell.Auto
}

func (m *Model) cycleFormat() {
	r := m.results()
	_, column, ok := m.selectedCell()
	if !ok {
		return
	}
	for len(r.formats) <= r.setIndex {
		r.formats = append(r.formats, nil)
	}
	if r.formats[r.setIndex] == nil {
		r.formats[r.setIndex] = make(map[int]cell.Format)
	}
	format := cell.Next(m.columnFormat())
	r.formats[r.setIndex][r.column] = format
	if r.view.filter != "" && r.view.set == &r.batch.Results[r.setIndex] {
		m.refreshRowView()
	}
	m.status, m.statusError = fmt.Sprintf("%s · display %s · original value retained", column.Name, format), false
}

func (m *Model) gridKey(msg tea.KeyPressMsg) tea.Cmd {
	r := m.results()
	key := msg.String()
	switch key {
	case "esc":
		if r.rowAnchor >= 0 {
			r.rowAnchor, r.columnAnchor = -1, -1
		} else if r.view.filter != "" {
			m.filterRows("")
		} else {
			m.focusPane(editorPane)
		}
		return nil
	case "q":
		// Like Esc, but q never leaves results: a second q would type into SQL.
		if r.rowAnchor >= 0 {
			r.rowAnchor, r.columnAnchor = -1, -1
		} else if r.view.filter != "" {
			m.filterRows("")
		} else {
			m.status, m.statusError = "Nothing to clear · Esc goes back to SQL · Ctrl+Q quits", false
		}
		return nil
	case "/":
		m.beginSearch()
		return nil
	case "ctrl+e":
		m.exportPrompt()
		return nil
	case "m":
		return m.fetchMore()
	case "[", "ctrl+pgup":
		r.setIndex = max(0, r.setIndex-1)
		r.row, r.column, r.gridTop, r.gridLeft = 0, 0, 0, 0
		r.rowAnchor, r.columnAnchor = -1, -1
		return nil
	case "]", "ctrl+pgdown":
		r.setIndex = max(0, min(len(r.batch.Results)-1, r.setIndex+1))
		r.row, r.column, r.gridTop, r.gridLeft = 0, 0, 0, 0
		r.rowAnchor, r.columnAnchor = -1, -1
		return nil
	}
	if r.rowAnchor < 0 {
		if key == "left" && r.column == 0 {
			m.focusPane(catalogPane)
			m.focusActiveDatabase()
			return nil
		}
		if key == "up" && r.row == 0 {
			m.focusPane(editorPane)
			return nil
		}
	}
	if r.setIndex < 0 || r.setIndex >= len(r.batch.Results) {
		return nil
	}
	set := r.batch.Results[r.setIndex]
	switch key {
	case "s":
		m.cycleSort()
		return nil
	case "f":
		m.openRowFilter()
		return nil
	case "y":
		m.openCopyAs()
		return nil
	}
	rows := m.gridRows()
	if len(rows) == 0 || len(set.Columns) == 0 {
		return nil
	}
	if key == "ctrl+a" {
		r.rowAnchor, r.columnAnchor = 0, 0
		r.row, r.column = len(rows)-1, len(set.Columns)-1
		return nil
	}
	// Spreadsheet habits: Shift+Space selects the row, Ctrl+Space the column.
	// Pressing either again selects the same cells.
	switch key {
	case "shift+space":
		r.row = min(r.row, len(rows)-1)
		r.rowAnchor, r.columnAnchor = r.row, 0
		r.column = len(set.Columns) - 1
		m.status, m.statusError = "Row selected · Ctrl+C copies it · y copies as…", false
		return nil
	case "ctrl+space", "ctrl+@":
		r.column = min(r.column, len(set.Columns)-1)
		r.rowAnchor, r.columnAnchor = 0, r.column
		r.row = len(rows) - 1
		m.status, m.statusError = "Column selected · Ctrl+C copies it · y copies as…", false
		return nil
	}
	if key == "+" || key == "=" || key == "-" {
		if r.setIndex < len(r.widths) && r.column < len(r.widths[r.setIndex]) {
			if key == "-" {
				r.widths[r.setIndex][r.column] = max(6, r.widths[r.setIndex][r.column]-2)
			} else {
				r.widths[r.setIndex][r.column] = min(100, r.widths[r.setIndex][r.column]+2)
			}
		}
		return nil
	}
	navigationKey := strings.ReplaceAll(key, "shift+", "")
	switch navigationKey {
	case "up", "down", "left", "right", "home", "end", "pgup", "pgdown", "ctrl+up", "ctrl+down", "ctrl+left", "ctrl+right", "ctrl+home", "ctrl+end":
		if strings.Contains(key, "shift+") {
			if r.rowAnchor < 0 {
				r.rowAnchor, r.columnAnchor = r.row, r.column
			}
		} else {
			r.rowAnchor, r.columnAnchor = -1, -1
		}
		key = navigationKey
	}
	page := max(1, m.dimensions().resultHeight-6)
	switch key {
	case "up":
		r.row = max(0, r.row-1)
	case "down":
		r.row = min(len(rows)-1, r.row+1)
	case "left":
		r.column = max(0, r.column-1)
	case "right":
		r.column = min(len(set.Columns)-1, r.column+1)
	case "pgup":
		r.row = max(0, r.row-page)
	case "pgdown":
		r.row = min(len(rows)-1, r.row+page)
	case "home", "ctrl+left":
		r.column = 0
	case "end", "ctrl+right":
		r.column = len(set.Columns) - 1
	case "ctrl+up":
		r.row = 0
	case "ctrl+down":
		r.row = len(rows) - 1
	case "ctrl+home":
		r.row, r.column = 0, 0
	case "ctrl+end":
		r.row, r.column = len(rows)-1, len(set.Columns)-1
	case "enter", "space":
		m.openInspector()
	case "v":
		m.cycleFormat()
	}
	return nil
}
