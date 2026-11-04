package tui

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/dustinevan/scrunchie/lib/logic/cell"
)

func (m *Model) beginSearch() {
	m.searchPane, m.searchInspector = m.focus, m.inspector.active
	title := "Find SQL text"
	value := m.lastSearch
	if m.inspector.active {
		title = "Filter JSON keys or find value text"
		value = m.inspector.filter
	} else if m.structure.active {
		title = "Find in structure"
	} else if m.focus == catalogPane {
		title = "Filter loaded catalog objects"
		value = m.catalogFilter
	} else if m.focus == resultsPane {
		title = "Find in retained result values"
	}
	m.openPrompt("find", title, value)
}

func (m *Model) findNext(query string) {
	r := m.results()
	m.lastSearch = query
	if m.structure.active {
		// Search collapsed sections too, and open the one holding the match.
		v := &m.structure
		all, visible := m.structureLines(true), m.structureLines(false)
		if query == "" || len(all) == 0 {
			return
		}
		current := -1
		if v.index < len(visible) {
			for position, line := range all {
				if line == visible[v.index] {
					current = position
					break
				}
			}
		}
		needle := strings.ToLower(query)
		for offset := 1; offset <= len(all); offset++ {
			line := all[(current+offset)%len(all)]
			if !strings.Contains(strings.ToLower(line.text), needle) {
				continue
			}
			v.collapsed[line.section] = false
			for position, candidate := range m.structureLines(false) {
				if candidate == line {
					v.index, v.scrolling = position, false
					break
				}
			}
			m.status, m.statusError = "Match selected · F3 finds the next", false
			return
		}
		m.status, m.statusError = "No matching structure text", false
		return
	}
	if m.inspector.active {
		if m.inspector.json {
			i := &m.inspector
			changed := i.filter != query
			i.filter, i.scrolling = query, false
			m.refreshInspectorRows()
			if query == "" {
				m.status, m.statusError = "JSON key filter cleared", false
				return
			}
			start := i.index
			if changed {
				start, i.top = -1, 0
			}
			needle := strings.ToLower(query)
			matches, selected := 0, -1
			for offset := 1; offset <= len(i.visible); offset++ {
				position := (start + offset) % len(i.visible)
				node := i.tree.Nodes[i.visible[position]]
				if strings.Contains(strings.ToLower(node.Key), needle) || strings.Contains(strings.ToLower(node.Path), needle) {
					matches++
					if selected < 0 {
						selected = position
					}
				}
			}
			if selected >= 0 {
				i.index = selected
				m.status, m.statusError = fmt.Sprintf("%d matching JSON keys/paths · F3 next · Esc clears the filter", matches), false
			} else {
				m.status, m.statusError = "No matching JSON keys or paths · Esc clears the filter", false
			}
			return
		}
		for offset := 1; offset <= len(m.inspector.lines); offset++ {
			index := (m.inspector.index + offset) % len(m.inspector.lines)
			if strings.Contains(strings.ToLower(m.inspector.lines[index]), strings.ToLower(query)) {
				m.inspector.index = index
				return
			}
		}
		m.status = "No matching text in this value preview"
		return
	}
	if m.focus == catalogPane {
		changed := m.catalogFilter != query
		m.catalogFilter = query
		if query != "" {
			for _, c := range m.connections {
				c.collapsed = false
			}
		}
		m.prepareDatabasePane(max(1, m.dimensions().bodyHeight-3))
		if query == "" {
			m.status, m.statusError = "Catalog filter cleared", false
			return
		}
		rows := m.databaseRows()
		start := m.databasePane.index
		if changed {
			start = -1
		}
		needle := strings.ToLower(query)
		matches, selected := 0, -1
		for offset := 1; offset <= len(rows); offset++ {
			position := (start + offset) % len(rows)
			row := rows[position]
			if row.kind != databaseCatalog {
				continue
			}
			node := m.connectionFor(row.conn).catalog[row.node]
			if strings.Contains(strings.ToLower(node.name+" "+node.detail), needle) {
				matches++
				if selected < 0 {
					selected = position
				}
			}
		}
		if selected >= 0 {
			m.selectDatabaseRow(selected)
			m.status, m.statusError = fmt.Sprintf("%d matching catalog objects · F3 next · Esc clears the filter", matches), false
		} else {
			m.status, m.statusError = "No matching loaded catalog objects · Esc clears the filter", false
		}
		return
	}
	if query == "" {
		return
	}
	needle := strings.ToLower(query)
	if m.focus == editorPane {
		e := &m.buffers[m.bufferIndex].editor
		queryRunes := []rune(needle)
		index := -1
		for offset := 0; offset < len(e.text); offset++ {
			position := (e.cursor + offset) % len(e.text)
			if position+len(queryRunes) > len(e.text) {
				continue
			}
			match := true
			for i, r := range queryRunes {
				if unicode.ToLower(e.text[position+i]) != r {
					match = false
					break
				}
			}
			if match {
				index = position
				break
			}
		}
		if index >= 0 {
			e.anchor = index
			e.cursor = index + len(queryRunes)
			e.verticalMotion = false
			m.status, m.statusError = "Match selected · F3 finds the next", false
		} else {
			m.status, m.statusError = "No matching SQL text", false
		}
		return
	}
	if r.setIndex >= len(r.batch.Results) {
		return
	}
	set := r.batch.Results[r.setIndex]
	rows := m.gridRows()
	count := len(rows) * len(set.Columns)
	if count == 0 {
		return
	}
	start := r.row*len(set.Columns) + r.column
	for offset := 1; offset <= count; offset++ {
		index := (start + offset) % count
		row, col := index/len(set.Columns), index%len(set.Columns)
		values := set.Rows[rows[row]]
		if col >= len(values) {
			continue
		}
		text := cell.Display(values[col], set.Columns[col], cell.Auto)
		if strings.Contains(strings.ToLower(text), needle) {
			r.row, r.column = row, col
			r.rowAnchor, r.columnAnchor = -1, -1
			m.status, m.statusError = "Matching cell selected · F3 finds the next", false
			return
		}
	}
	m.status, m.statusError = "No match in retained cell previews", false
	if r.view.filter != "" {
		m.status = "No match in the filtered rows' cell previews · Esc clears the filter"
	}
}
