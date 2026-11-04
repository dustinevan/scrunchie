package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/dustinevan/scrunchie/lib/logic/cell"
	"github.com/dustinevan/scrunchie/lib/logic/jsontree"
	"github.com/dustinevan/scrunchie/pkg/termtext"
)

func (m *Model) openInspector() {
	r := m.results()
	value, column, ok := m.selectedCell()
	if !ok {
		m.status, m.statusError = "Select a result cell to inspect", false
		return
	}
	m.focusPane(resultsPane)
	m.inspector = inspector{active: true, value: value, column: column, format: m.columnFormat(), row: r.row, source: m.gridRows()[r.row]}
	m.prepareInspector()
}

func (m *Model) prepareInspector() {
	i := &m.inspector
	i.json, i.error, i.filter = false, "", ""
	i.scrolling = false
	i.index, i.top, i.left = 0, 0, 0
	i.visible, i.lines = nil, nil
	raw := []byte(i.value.Text)
	if i.column.OID == 17 {
		raw = i.value.Bytes
	}
	if !i.value.Null && (i.format == cell.Auto || i.format == cell.JSON) && json.Valid(raw) {
		tree, err := jsontree.Parse(raw)
		if err == nil {
			i.tree, i.json = tree, true
			i.visible = i.tree.Visible()
			return
		}
		i.error = err.Error()
	}
	value := i.value
	if len(value.Text) > 64<<10 {
		value.Text = value.Text[:64<<10]
		i.error = "Preview shows the first 64 KiB · Copy or export retains the full value"
	}
	if len(value.Bytes) > 64<<10 {
		value.Bytes = value.Bytes[:64<<10]
		i.error = "Preview shows the first 64 KiB · Copy or export retains the full value"
	}
	text, err := cell.Raw(value, i.column, i.format)
	if err != nil {
		i.error = err.Error()
		text = cell.Display(value, i.column, cell.Auto)
	}
	if i.value.Null {
		text = "NULL (SQL NULL; no value)"
	}
	i.lines = strings.Split(termtext.SafeMultiline(text), "\n")
}

func (m *Model) refreshInspectorRows() {
	i := &m.inspector
	selected := -1
	if i.index >= 0 && i.index < len(i.visible) {
		selected = i.visible[i.index]
	}
	if i.filter == "" {
		i.visible = i.tree.Visible()
	} else {
		include := make([]bool, len(i.tree.Nodes))
		query := strings.ToLower(i.filter)
		for index, node := range i.tree.Nodes {
			if strings.Contains(strings.ToLower(node.Key), query) || strings.Contains(strings.ToLower(node.Path), query) {
				for index >= 0 {
					include[index] = true
					index = i.tree.Nodes[index].Parent
				}
			}
		}
		i.visible = nil
		for index, show := range include {
			if show {
				i.visible = append(i.visible, index)
			}
		}
	}
	for selected >= 0 {
		for position, index := range i.visible {
			if index == selected {
				i.index = position
				return
			}
		}
		selected = i.tree.Nodes[selected].Parent
	}
	i.index = max(0, min(i.index, len(i.visible)-1))
}

func (m *Model) inspectorKey(msg tea.KeyPressMsg) tea.Cmd {
	i := &m.inspector
	key := msg.String()
	switch key {
	case "esc":
		if i.filter != "" {
			i.filter = ""
			m.refreshInspectorRows()
		} else {
			i.active = false
			m.focusPane(resultsPane)
		}
		return nil
	case "/":
		m.beginSearch()
		return nil
	case "v":
		i.format = cell.Next(i.format)
		m.prepareInspector()
		return nil
	case "p":
		return m.copyJSONPath()
	}
	count := len(i.lines)
	if i.json {
		count = len(i.visible)
	}
	if count == 0 {
		return nil
	}
	i.index = min(i.index, count-1)
	switch key {
	case "up":
		i.index = max(0, i.index-1)
	case "down":
		i.index = min(count-1, i.index+1)
	case "pgup":
		i.index = max(0, i.index-max(1, m.height-9))
	case "pgdown":
		i.index = min(count-1, i.index+max(1, m.height-9))
	case "home", "ctrl+home":
		i.index = 0
	case "end", "ctrl+end":
		i.index = count - 1
	case "alt+left":
		i.left = max(0, i.left-8)
	case "alt+right":
		i.left += 8
	case "e":
		if i.json {
			if i.filter != "" {
				m.status, m.statusError = "Filtered JSON keeps matching ancestors open · Esc clears the filter", false
				return nil
			}
			i.tree.ExpandAll()
			m.refreshInspectorRows()
		}
	case "c":
		if i.json {
			if i.filter != "" {
				m.status, m.statusError = "Filtered JSON keeps matching ancestors open · Esc clears the filter", false
				return nil
			}
			i.tree.CollapseAll()
			i.index, i.top = 0, 0
			m.refreshInspectorRows()
		}
	case "enter", "space", "left", "right":
		if !i.json {
			if key == "left" {
				i.left = max(0, i.left-8)
			} else if key == "right" {
				i.left += 8
			}
			return nil
		}
		index := i.visible[i.index]
		node := i.tree.Nodes[index]
		if i.filter != "" {
			for position, candidate := range i.visible {
				if key == "left" && candidate == node.Parent || key != "left" && i.tree.Nodes[candidate].Parent == index {
					i.index = position
					break
				}
			}
			return nil
		}
		if key == "left" {
			if node.Expanded {
				i.tree.Collapse(index)
			} else {
				for position, candidate := range i.visible {
					if candidate == node.Parent {
						i.index = position
						break
					}
				}
			}
		} else if key == "right" {
			if node.Expanded && len(node.Children) > 0 {
				i.index = min(len(i.visible)-1, i.index+1)
			} else {
				i.tree.Expand(index)
			}
		} else {
			i.tree.Toggle(index)
		}
		m.refreshInspectorRows()
	}
	return nil
}

func (m *Model) copyJSONPath() tea.Cmd {
	i := &m.inspector
	if !i.active || !i.json || len(i.visible) == 0 {
		m.status, m.statusError = "Open a JSON result value to copy a key path", false
		return nil
	}
	path := i.tree.Nodes[i.visible[min(i.index, len(i.visible)-1)]].Path
	m.status, m.statusError = "JSON path copy requested through terminal OSC52", false
	return tea.SetClipboard(path)
}

func (m *Model) copySelection(cut bool) tea.Cmd {
	r := m.results()
	if m.structure.active {
		return m.copyStructureLine()
	}
	var text string
	if m.inspector.active {
		i := &m.inspector
		if i.json && len(i.visible) > 0 {
			var err error
			text, err = i.tree.Pretty(i.visible[min(i.index, len(i.visible)-1)])
			if err != nil {
				m.status, m.statusError = err.Error(), true
				return nil
			}
		} else if i.value.Null {
			// The text "NULL" would be indistinguishable from the string 'NULL'.
			m.status, m.statusError = "Copied SQL NULL as empty text", false
			return tea.SetClipboard("")
		} else {
			var err error
			text, err = cell.Raw(i.value, i.column, i.format)
			if err != nil {
				m.status, m.statusError = err.Error(), true
				return nil
			}
		}
	} else if m.focus == editorPane {
		e := &m.buffers[m.bufferIndex].editor
		start, end := e.selection()
		if start == end {
			m.status, m.statusError = "Select SQL text first · Ctrl+A selects the buffer", false
			return nil
		}
		text = string(e.text[start:end])
		if cut && len(text) <= 4<<20 {
			e.delete(start, end)
		}
	} else if m.focus == catalogPane {
		_, node, ok := m.selectedCatalogNode()
		if !ok {
			return nil
		}
		text = node.identifier()
		if text == "" {
			return nil
		}
	} else {
		value, column, ok := m.selectedCell()
		if !ok {
			return nil
		}
		single := r.rowAnchor < 0 || (r.rowAnchor == r.row && r.columnAnchor == r.column)
		if single && value.Null {
			// The text "NULL" would be indistinguishable from the string 'NULL'.
			m.status, m.statusError = "Copied SQL NULL as empty text", false
			return tea.SetClipboard("")
		} else if single {
			var err error
			text, err = cell.Raw(value, column, m.columnFormat())
			if err != nil {
				m.status, m.statusError = err.Error(), true
				return nil
			}
		} else {
			set := r.batch.Results[r.setIndex]
			rows := m.gridRows()
			var output strings.Builder
			for row := min(r.row, r.rowAnchor); row <= max(r.row, r.rowAnchor) && row < len(rows); row++ {
				if row > min(r.row, r.rowAnchor) {
					output.WriteByte('\n')
				}
				values := set.Rows[rows[row]]
				for col := min(r.column, r.columnAnchor); col <= max(r.column, r.columnAnchor); col++ {
					if col > min(r.column, r.columnAnchor) {
						output.WriteByte('\t')
					}
					if col >= len(values) {
						continue
					}
					format := cell.Auto
					if r.setIndex < len(r.formats) && r.formats[r.setIndex] != nil {
						if selected, ok := r.formats[r.setIndex][col]; ok {
							format = selected
						}
					}
					field, err := cell.Raw(values[col], set.Columns[col], format)
					if err != nil {
						m.status, m.statusError = err.Error(), true
						return nil
					}
					if values[col].Null {
						field = `\N`
					} else if field == `\N` || strings.ContainsAny(field, "\t\n\r\"") {
						field = `"` + strings.ReplaceAll(field, `"`, `""`) + `"`
					}
					output.WriteString(field)
					if output.Len() > 4<<20 {
						m.status, m.statusError = "Selection exceeds the 4 MiB clipboard limit; use Export", true
						return nil
					}
				}
			}
			text = output.String()
		}
	}
	if len(text) > 4<<20 {
		m.status, m.statusError = "Value exceeds the 4 MiB clipboard limit; use Export", true
		return nil
	}
	m.status, m.statusError = fmt.Sprintf("Copy requested through terminal OSC52 · %d bytes", len(text)), false
	return tea.SetClipboard(text)
}
