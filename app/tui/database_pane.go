package tui

import (
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/dustinevan/scrunchie/lib/data/connections"
	"github.com/dustinevan/scrunchie/pkg/termtext"
)

type databaseRowKind uint8

const (
	databaseAdd databaseRowKind = iota
	databaseConnection
	databaseSaved
	databaseCatalog
)

// databaseRow describes both keyboard selection and mouse hit testing. Rows
// name their connection and keep catalog node indexes, so preparing SQL never
// confuses a saved entry, or another connection's object, with the selection.
type databaseRow struct {
	kind    databaseRowKind
	conn    uint64
	profile int
	node    int
	depth   int
	key     string
}

type databasePaneState struct {
	index        int
	top          int
	pageRows     int
	selection    string
	filter       string
	manualScroll bool
	// typing is true while typed letters edit the catalog filter.
	typing bool
}

// databaseRows lists Add, each open connection followed by its visible
// catalog, then saved entries that are not open.
func (m *Model) databaseRows() []databaseRow {
	rows := []databaseRow{{kind: databaseAdd, profile: -1, node: -1, key: "add"}}
	for _, c := range m.connections {
		id := strconv.FormatUint(c.id, 10)
		rows = append(rows, databaseRow{kind: databaseConnection, conn: c.id, profile: -1, node: -1, key: "conn\x00" + id})
		if c.collapsed {
			continue
		}
		for _, index := range m.visibleCatalog(c) {
			node := c.catalog[index]
			depth := 1
			for parent := node.parent; parent >= 0; parent = c.catalog[parent].parent {
				depth++
			}
			rows = append(rows, databaseRow{kind: databaseCatalog, conn: c.id, profile: -1, node: index, depth: depth, key: "catalog\x00" + id + "\x00" + node.identity()})
		}
	}
	for index, profile := range m.profiles {
		if m.connectionForProfile(profile.ID) != nil {
			continue
		}
		rows = append(rows, databaseRow{kind: databaseSaved, profile: index, node: -1, key: "profile\x00" + profile.ID})
	}
	return rows
}

// connectionRowIndex is the row of connection id, or -1.
func connectionRowIndex(rows []databaseRow, id uint64) int {
	for i, row := range rows {
		if row.kind == databaseConnection && row.conn == id {
			return i
		}
	}
	return -1
}

// visibleDatabaseRows is the exact sequence rendered in the pane's content
// area. Add stays pinned above a scrolling tree. A one-line terminal must show
// the selected item instead, so tiny screens still permit keyboard navigation.
func (m *Model) visibleDatabaseRows(height int) []int {
	if height < 1 {
		return nil
	}
	rows := m.databaseRows()
	if height == 1 {
		if m.databasePane.manualScroll {
			return []int{max(0, min(m.databasePane.top, len(rows)-1))}
		}
		return []int{max(0, min(m.databasePane.index, len(rows)-1))}
	}
	visible := []int{0}
	start := max(1, m.databasePane.top)
	for index := start; index < min(len(rows), start+height-1); index++ {
		visible = append(visible, index)
	}
	return visible
}

// prepareDatabasePane preserves selection across asynchronous catalog replies,
// profile edits, and connections opening or closing. It runs during Update,
// never while rendering View.
func (m *Model) prepareDatabasePane(height int) {
	state := &m.databasePane
	state.pageRows = max(1, height-1)
	filterChanged := state.filter != m.catalogFilter
	manualScroll := state.manualScroll && !filterChanged
	if filterChanged && m.catalogFilter != "" {
		// A new filter reveals matches in every connection's tree.
		for _, c := range m.connections {
			c.collapsed = false
		}
	}
	rows := m.databaseRows()
	index := max(0, min(state.index, len(rows)-1))
	current := connectionRowIndex(rows, m.buffers[m.bufferIndex].conn)
	found := false
	for i, row := range rows {
		if row.key == state.selection {
			index, found = i, true
			break
		}
	}
	if !found {
		manualScroll = false
		// A vanished object falls back to its connection, then to the
		// current buffer's connection.
		fallback := -1
		if parts := strings.Split(state.selection, "\x00"); len(parts) > 1 && (parts[0] == "conn" || parts[0] == "catalog") {
			if id, err := strconv.ParseUint(parts[1], 10, 64); err == nil {
				fallback = connectionRowIndex(rows, id)
			}
		}
		if fallback < 0 && (state.selection == "" || strings.HasPrefix(state.selection, "conn\x00") || strings.HasPrefix(state.selection, "catalog\x00")) {
			fallback = current
		}
		if fallback >= 0 {
			index = fallback
		}
	}
	if filterChanged && m.catalogFilter != "" {
		first := current
		for i, row := range rows {
			if row.kind == databaseCatalog {
				first = i
				break
			}
		}
		if first >= 0 {
			index = first
		}
		state.top = 1
	}
	state.filter = m.catalogFilter
	top := state.top
	m.selectDatabaseRow(index)
	if manualScroll {
		state.manualScroll, state.top = true, top
	}
	state.top = max(1, min(state.top, max(1, len(rows)-state.pageRows)))
	if index > 0 && !state.manualScroll {
		state.top = min(state.top, index)
		if index >= state.top+state.pageRows {
			state.top = index - state.pageRows + 1
		}
	}
}

func (m *Model) selectDatabaseRow(index int) {
	rows := m.databaseRows()
	index = max(0, min(index, len(rows)-1))
	m.databasePane.index = index
	m.databasePane.selection = rows[index].key
	m.databasePane.manualScroll = false
	if index == 0 {
		m.databasePane.top = 1
	}
}

// scrollDatabaseRows changes the viewport without moving its selection. Mouse
// wheel scrolling must not connect a database or change later keyboard actions.
func (m *Model) scrollDatabaseRows(delta int) {
	if delta == 0 {
		return
	}
	rows := m.databaseRows()
	m.databasePane.top = max(1, min(m.databasePane.top+delta, max(1, len(rows)-max(1, m.databasePane.pageRows))))
	m.databasePane.manualScroll = true
}

// selectedCatalogNode is the selected tree object and the connection it
// belongs to. Catalog actions use that connection, not the current buffer's.
func (m *Model) selectedCatalogNode() (*connection, catalogNode, bool) {
	rows := m.databaseRows()
	index := m.databasePane.index
	if index < 0 || index >= len(rows) || rows[index].kind != databaseCatalog {
		return nil, catalogNode{}, false
	}
	c := m.connectionFor(rows[index].conn)
	node := rows[index].node
	if c == nil || node < 0 || node >= len(c.catalog) {
		return nil, catalogNode{}, false
	}
	return c, c.catalog[node], true
}

// focusActiveDatabase selects the current buffer's connection, or Add.
func (m *Model) focusActiveDatabase() {
	m.focusPane(catalogPane)
	if index := connectionRowIndex(m.databaseRows(), m.buffers[m.bufferIndex].conn); index >= 0 {
		m.selectDatabaseRow(index)
		return
	}
	m.selectDatabaseRow(0)
}

func (m *Model) databasePaneView(width, height int) string {
	if width < 1 || height < 1 {
		return ""
	}
	rows := m.databaseRows()
	current := m.conn()
	var lines []string
	for _, index := range m.visibleDatabaseRows(height) {
		row := rows[index]
		line := "+ Add database"
		switch row.kind {
		case databaseConnection:
			c := m.connectionFor(row.conn)
			marker := "▾"
			if c.collapsed {
				marker = "▸"
			}
			line = marker + " ● " + connectionRow(c, m.config.Demo, true)
			if lipgloss.Width(line) > width {
				// Keep the write and transaction markers in a narrow pane.
				line = marker + " ● " + connectionRow(c, m.config.Demo, false)
			}
			if c == current && index != m.databasePane.index {
				// The current buffer's connection stands out; one style over plain text.
				line = accentStyle.Render(line)
			}
		case databaseSaved:
			profile := m.profiles[row.profile]
			name := profile.Name
			if name != profile.Database {
				name += " · " + profile.Database
			}
			line = "○ " + termtext.Safe(name)
		case databaseCatalog:
			c := m.connectionFor(row.conn)
			node := c.catalog[row.node]
			marker := "·"
			if !node.leaf() {
				marker = "▸"
				if node.expanded || m.catalogFilter != "" {
					marker = "▾"
				}
			}
			line = strings.Repeat("  ", row.depth) + marker + " " + termtext.Safe(node.name)
			if node.loading || slices.Contains(c.catalogQueue, node.identity()) {
				line += " " + m.spinner.View()
			}
			// Muted details are rendered separately: selectedStyle styles each
			// rune, which would split an embedded escape sequence into text.
			suffix := ""
			switch node.kind {
			case "column", "folder":
				suffix = " " + termtext.Safe(node.detail)
			case "function":
				suffix = termtext.Safe(node.detail)
			case "relation":
				suffix = map[string]string{"view": " view", "materialized view": " matview", "foreign table": " foreign", "partitioned table": " partitioned"}[node.relation.Kind]
			}
			if suffix != "" && index == m.databasePane.index {
				line += suffix
			} else if suffix != "" {
				line += mutedStyle.Render(suffix)
			}
		}
		if index == m.databasePane.index {
			line = selectedStyle.Render(fitLine(line, width))
		}
		lines = append(lines, line)
	}
	if len(lines) < height {
		loading := false
		for _, c := range m.connections {
			loading = loading || c.catalogBusy
		}
		hint := ""
		if loading {
			hint = m.spinner.View() + " Loading catalog…"
		} else if m.catalogFilter != "" {
			hint = "Esc clears the filter"
		} else if len(m.connections) == 0 && !m.config.Demo {
			hint = "Enter or Space opens selection"
		} else if current != nil && len(current.catalog) == 0 && !current.collapsed {
			hint = "No objects · Ctrl+R refreshes"
		} else if len(m.connections) > 1 {
			hint = "Enter switches connection · . shows actions"
		}
		if hint != "" {
			lines = append(lines, mutedStyle.Render(hint))
		}
	}
	return fitBlock(strings.Join(lines, "\n"), width, height)
}

// databasePaneKey handles keys in the database tree. Typed letters never reach
// it: they edit the filter first (typeFilter). Its letter cases (e, x, i, p,
// d) are the row actions that "." and the command palette perform.
func (m *Model) databasePaneKey(msg tea.KeyPressMsg) tea.Cmd {
	key := msg.String()
	if m.databasePane.typing {
		switch key {
		case "up", "down", "pgup", "pgdown":
			// Choosing among matches keeps typing.
		case "backspace":
			runes := []rune(m.catalogFilter)
			if len(runes) <= 1 {
				m.databasePane.typing = false
				m.findNext("")
				return nil
			}
			m.findNext(string(runes[:len(runes)-1]))
			return nil
		case "ctrl+u", "esc":
			m.databasePane.typing = false
			m.findNext("")
			return nil
		default:
			m.databasePane.typing = false
		}
	}
	rows := m.databaseRows()
	index := max(0, min(m.databasePane.index, len(rows)-1))
	row := rows[index]
	c := m.connectionFor(row.conn)
	switch key {
	case "right":
		m.focusPane(editorPane)
		return nil
	case "up":
		m.selectDatabaseRow(max(0, index-1))
		return nil
	case "down":
		m.selectDatabaseRow(min(len(rows)-1, index+1))
		return nil
	case "home", "ctrl+home":
		m.selectDatabaseRow(0)
		return nil
	case "end", "ctrl+end":
		m.selectDatabaseRow(len(rows) - 1)
		return nil
	case "pgup":
		m.selectDatabaseRow(max(0, index-max(1, m.databasePane.pageRows)))
		return nil
	case "pgdown":
		m.selectDatabaseRow(min(len(rows)-1, index+max(1, m.databasePane.pageRows)))
		return nil
	case "+", "n":
		m.addDatabaseFromPane()
		return nil
	case "/":
		m.databasePane.typing = true
		m.status, m.statusError = "Type to filter · ↑↓ choose · Enter opens · Esc clears", false
		return nil
	case "backspace":
		if m.catalogFilter == "" {
			m.status, m.statusError = "No filter to edit · type a name to filter", false
			return nil
		}
		runes := []rune(m.catalogFilter)
		m.databasePane.typing = len(runes) > 1
		m.findNext(string(runes[:len(runes)-1]))
		return nil
	case "ctrl+r":
		if c == nil {
			c = m.conn()
		}
		if c == nil {
			m.status, m.statusError = "Connect a database before refreshing its catalog", false
			return nil
		}
		return m.loadCatalog(c, -1)
	case "esc":
		if m.catalogFilter != "" {
			m.catalogFilter = ""
			m.status, m.statusError = "Catalog filter cleared", false
		} else {
			m.focusPane(editorPane)
		}
		return nil
	case "e":
		if m.profileBusy || m.connecting || m.discovering {
			m.status, m.statusError = "Wait for the current connection operation to finish", false
			return nil
		}
		if row.kind == databaseSaved {
			m.openConnectionForm(m.profiles[row.profile])
			m.connectionForm.fromManager = false
		} else if row.kind == databaseConnection && c.profile.ID != "" {
			m.openConnectionForm(c.profile)
			m.connectionForm.fromManager = false
		} else {
			m.status, m.statusError = "Select a saved database to edit · + adds a database", false
		}
		return nil
	case "x":
		if row.kind != databaseConnection {
			m.status, m.statusError = "Select an open database (●) to disconnect it", false
			return nil
		}
		return m.requestDisconnect(c)
	case ".":
		switch row.kind {
		case databaseAdd:
			m.status, m.statusError = "Enter adds a database", false
		case databaseCatalog:
			m.openPrompt("catalog-actions", "Actions · "+termtext.Safe(c.catalog[row.node].name), "")
		default:
			name := ""
			if row.kind == databaseSaved {
				name = m.profiles[row.profile].Name
			} else if c != nil {
				name = c.name
			}
			m.openPrompt("catalog-actions", "Actions · "+termtext.Safe(name), "")
		}
		return nil
	case "i", "p", "d":
		if row.kind == databaseSaved && key == "d" {
			m.status, m.statusError = "d shows table structure · remove saved settings from the Ctrl+B manager", false
			return nil
		}
		if row.kind != databaseCatalog {
			m.status, m.statusError = "Select a schema, relation, or column for catalog actions", false
			return nil
		}
	}
	switch row.kind {
	case databaseAdd:
		switch key {
		case "enter", "space":
			m.addDatabaseFromPane()
		}
	case databaseSaved:
		switch key {
		case "enter", "space":
			return tea.Batch(m.connectProfile(m.profiles[row.profile], "", false), m.spinner.Tick)
		}
	case databaseConnection:
		switch key {
		case "enter", "space":
			if !m.config.Demo && !c.connected {
				return tea.Batch(m.reconnectConnection(c), m.spinner.Tick)
			}
			if c != m.conn() {
				// Switch to the connection's latest buffer; the tree keeps focus
				// so browsing continues, and Right enters its SQL.
				m.activateConnection(c)
				c.collapsed = false
				m.focusPane(catalogPane)
				m.status = bufferName(m.buffers[m.bufferIndex]) + " runs on " + termtext.Safe(c.name) + " · → enters SQL · . shows actions"
				return nil
			}
			c.collapsed = !c.collapsed
		case "left":
			c.collapsed = true
		}
	case databaseCatalog:
		return m.databaseCatalogKey(c, key, row.node)
	}
	return nil
}

func (m *Model) addDatabaseFromPane() {
	if m.profileBusy || m.connecting || m.discovering {
		m.status, m.statusError = "Wait for the current connection operation to finish", false
		return
	}
	m.openConnectionForm(connections.FromEnvironment())
	m.connectionForm.fromManager = false
}

// databaseCatalogKey acts on node index of c's tree. Prepared SQL opens in a
// new buffer bound to c.
func (m *Model) databaseCatalogKey(c *connection, key string, index int) tea.Cmd {
	node := &c.catalog[index]
	queued := slices.Index(c.catalogQueue, node.identity())
	switch key {
	case "enter", "space":
		if node.leaf() {
			hint := " · . → DDL"
			if node.kind == "column" {
				hint = " · . → Structure"
			}
			m.status, m.statusError = node.name+" · "+node.detail+hint, false
			return nil
		}
		if !node.loaded {
			if !m.config.Demo && !c.connected {
				m.status, m.statusError = "Connection closed · Enter on the database reconnects", false
				return nil
			}
			if node.loading || queued >= 0 {
				node.expanded = !node.expanded
				if !node.expanded && queued >= 0 {
					c.catalogQueue = slices.Delete(c.catalogQueue, queued, queued+1)
				}
				return nil
			}
			return m.loadCatalog(c, index)
		}
		if m.catalogFilter != "" {
			for position, row := range m.databaseRows() {
				if row.kind == databaseCatalog && row.conn == c.id && c.catalog[row.node].parent == index {
					m.selectDatabaseRow(position)
					break
				}
			}
		} else {
			node.expanded = !node.expanded
		}
	case "left":
		if node.expanded && !node.leaf() && m.catalogFilter == "" {
			node.expanded = false
			if queued >= 0 {
				c.catalogQueue = slices.Delete(c.catalogQueue, queued, queued+1)
			}
		} else {
			for position, row := range m.databaseRows() {
				if row.conn == c.id && (node.parent < 0 && row.kind == databaseConnection || row.kind == databaseCatalog && row.node == node.parent) {
					m.selectDatabaseRow(position)
					break
				}
			}
		}
	case ".":
		m.openPrompt("catalog-actions", "Catalog actions · prepare SQL", "")
	case "d":
		return m.openStructure(c, *node)
	case "i":
		name := node.identifier()
		if name == "" {
			m.status, m.statusError = "Select a schema, relation, column, or function to insert its name", false
			return nil
		}
		m.buffers[m.bufferIndex].editor.insert(name, "")
		m.focusPane(editorPane)
	case "p":
		if node.kind == "relation" || node.kind == "column" {
			m.addBuffer("SELECT *\nFROM "+node.relation.QualifiedName()+"\nLIMIT 100;", "", true, c.id)
			m.status, m.statusError = "Preview SQL is ready for "+termtext.Safe(c.name)+" · Ctrl+J executes it", false
		} else {
			m.status, m.statusError = "Select a table or column to prepare preview SQL", false
		}
	}
	return nil
}

// typeFilter narrows the database tree as a name is typed. The first letter
// starts a new filter; later letters extend it. Matches show as you type, and
// the first one is selected.
func (m *Model) typeFilter(key string) {
	filter := m.catalogFilter
	if !m.databasePane.typing {
		filter = ""
	}
	m.databasePane.typing = true
	m.findNext(filter + key)
}

// databaseTitle shows the filter in the pane title, where it stays visible
// however long the tree is, with a cursor while it is being typed.
func (m *Model) databaseTitle() string {
	if m.catalogFilter == "" && !m.databasePane.typing {
		return "DATABASES"
	}
	title := "DATABASES · " + termtext.Safe(m.catalogFilter)
	if m.databasePane.typing && m.focus == catalogPane {
		title += "▏"
	}
	return title
}
