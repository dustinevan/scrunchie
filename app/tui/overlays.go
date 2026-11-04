package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/dustinevan/scrunchie/lib/data/connections"
	"github.com/dustinevan/scrunchie/lib/logic/cell"
	"github.com/dustinevan/scrunchie/pkg/sqltext"
	"github.com/dustinevan/scrunchie/pkg/termtext"
)

type command struct {
	id    string
	label string
	key   string
}

var commands = []command{
	{"databases", "Databases: connect or manage saved entries", "Ctrl+B"},
	{"add-database", "Add database", ""},
	{"run", "Run selection or statement at cursor", "Ctrl+J"},
	{"run-all", "Run entire SQL buffer", ""},
	{"editor", "Focus SQL editor", "F2"},
	{"results", "Focus query results", ""},
	{"catalog", "Focus database catalog", "F6"},
	{"new", "New SQL buffer", "Ctrl+N"},
	{"open", "Open SQL file", "Ctrl+O"},
	{"save", "Save SQL buffer", "Ctrl+S"},
	{"save-as", "Save SQL buffer as…", ""},
	{"close", "Close SQL buffer", "Ctrl+W"},
	{"next-buffer", "Next SQL buffer", "Ctrl+PgDn"},
	{"previous-buffer", "Previous SQL buffer", "Ctrl+PgUp"},
	{"format", "Format SQL", "F4"},
	{"comment", "Toggle SQL line comments", "Ctrl+/"},
	{"goto", "Go to SQL line", "Ctrl+G"},
	{"complete", "Complete SQL word", "Ctrl+Space"},
	{"find", "Find in focused pane", "Ctrl+F"},
	{"find-next", "Find next match", "F3"},
	{"clear-filter", "Clear catalog or JSON filter", "Esc"},
	{"history", "Query history (in memory)", "F8"},
	{"inspect", "Inspect selected result value", "Enter"},
	{"display", "Cycle selected column display format", "v"},
	{"copy", "Copy selection to terminal clipboard", "Ctrl+C"},
	{"copy-path", "Copy selected JSON path", "p"},
	{"sort", "Sort results by selected column (asc, desc, original)", "s"},
	{"filter-rows", "Filter result rows by text or column:text", "f"},
	{"copy-as", "Copy results as TSV, CSV, Markdown, JSON, or SQL…", "y"},
	{"expand", "Expand all JSON containers", "e"},
	{"collapse", "Collapse all JSON containers", "c"},
	{"export", "Export retained result rows as CSV or JSON", "Ctrl+E"},
	{"fetch-more", "Fetch more rows (re-runs the query)", "m"},
	{"next-result", "Next statement result", "]"},
	{"previous-result", "Previous statement result", "["},
	{"refresh", "Refresh database catalog", "Ctrl+R"},
	{"insert-name", "Insert selected catalog identifier", ""},
	{"preview", "Prepare SELECT for catalog table", ""},
	{"structure", "Show table structure", ""},
	{"ddl", "Show DDL", "D"},
	{"count", "Prepare table row-count query", ""},
	{"begin", "Prepare BEGIN transaction", ""},
	{"commit", "Prepare COMMIT transaction", ""},
	{"rollback", "Prepare ROLLBACK transaction", ""},
	{"explain", "Prepare EXPLAIN (FORMAT JSON) for SQL", ""},
	{"reconnect", "Reconnect this buffer's connection (new session)", ""},
	{"buffer-connection", "Change buffer connection…", ""},
	{"disconnect", "Disconnect this connection", ""},
	{"edit-database", "Edit the selected database's saved settings", ""},
	{"disconnect-database", "Disconnect the selected database", ""},
	{"allow-writes", "Allow writes on this connection…", ""},
	{"read-only", "Make this connection read-only", ""},
	{"sidebar", "Toggle catalog sidebar", "F9"},
	{"maximize", "Maximize focused pane", "F10"},
	{"message", "Read last status or error", ""},
	{"help", "What you can do here, and every key", "F1 · ?"},
	{"quit", "Quit scrunch", "Ctrl+Q"},
}

func (m *Model) openPrompt(kind, title, value string) {
	m.modalMouseState = modalMouseState{}
	m.completion = completion{}
	m.completeAfterCatalog = false
	input := newEditor(value)
	input.singleLine = true
	input.cursor = len(input.text)
	if value != "" {
		input.anchor = 0
	}
	m.modal = modal{kind: kind, title: title, input: input}
}

func (m *Model) modalAcceptsText() bool {
	switch m.modal.kind {
	case "palette", "history", "find", "open", "save", "export", "goto", "catalog-actions", "row-filter":
		return true
	}
	return false
}

// modalHolds reports a dialog holding a decision or typed input, which a
// background reply must not close or replace.
func (m *Model) modalHolds() bool {
	return strings.HasPrefix(m.modal.kind, "confirm") || m.modal.kind == "buffer-connection" || m.modal.kind == "copy-as" || m.modalAcceptsText()
}

func (m *Model) paletteItems() []command {
	query := strings.ToLower(string(m.modal.input.text))
	var items []command
	// "." lists only what applies to the selected database-pane row.
	rowActions := map[string]bool{"preview": true, "structure": true, "ddl": true, "count": true, "insert-name": true}
	if rows := m.databaseRows(); m.modal.kind == "catalog-actions" && m.databasePane.index < len(rows) {
		switch rows[m.databasePane.index].kind {
		case databaseConnection:
			rowActions = map[string]bool{"edit-database": true, "disconnect-database": true, "refresh": true}
		case databaseSaved:
			rowActions = map[string]bool{"edit-database": true}
		}
	}
	for _, item := range commands {
		if m.modal.kind == "catalog-actions" && !rowActions[item.id] {
			continue
		}
		if strings.Contains(strings.ToLower(item.label+" "+item.key), query) {
			items = append(items, item)
		}
	}
	return items
}

func (m *Model) modalKey(msg tea.KeyPressMsg) tea.Cmd {
	key := msg.String()
	if m.modal.kind == "connection" || m.modal.kind == "connections" || m.modal.kind == "confirm-remove-profile" || m.modal.kind == "database-names" {
		return m.connectionKey(msg)
	}
	if m.fileBusy {
		m.status = "File operation is in progress"
		return nil
	}
	if key == "esc" || (key == "ctrl+p" && m.modal.kind == "palette") {
		if m.modal.kind == "confirm-unfiltered" || m.modal.kind == "confirm-writes" {
			m.pendingRun = queryRun{}
			m.status, m.statusError = "Canceled · nothing was run", false
		}
		m.modal = modal{}
		m.quitIntent, m.closeIntent = false, false
		m.quitDiscard = make(map[uint64]bool)
		return nil
	}
	if m.modal.kind == "help" || m.modal.kind == "message" {
		text := termtext.SafeMultiline(m.modal.message)
		if m.modal.kind == "help" {
			text = m.referenceText()
		}
		content := m.centeredRect(max(1, min(96, m.width-4)), max(1, min(m.dimensions().bodyHeight, 24))).content()
		width, rows := max(1, content.width), max(1, content.height-1)
		last := max(0, strings.Count(ansi.Hardwrap(text, width, false), "\n")+1-rows)
		m.modal.index = min(m.modal.index, last)
		switch key {
		case "enter", "f1", "f7":
			m.modal = modal{}
		case "up":
			m.modal.index = max(0, m.modal.index-1)
		case "down":
			m.modal.index = min(last, m.modal.index+1)
		case "pgup":
			m.modal.index = max(0, m.modal.index-rows)
		case "pgdown", "space":
			m.modal.index = min(last, m.modal.index+rows)
		case "shift+space":
			m.modal.index = max(0, m.modal.index-rows)
		case "home", "ctrl+home":
			m.modal.index = 0
		case "end", "ctrl+end":
			m.modal.index = last
		}
		return nil
	}
	if len(m.modal.buttons) > 0 {
		switch key {
		case "tab", "right", "down":
			m.modal.index = (m.modal.index + 1) % len(m.modal.buttons)
		case "shift+tab", "left", "up":
			m.modal.index = (m.modal.index + len(m.modal.buttons) - 1) % len(m.modal.buttons)
		case "home":
			m.modal.index = 0
		case "end":
			m.modal.index = len(m.modal.buttons) - 1
		case "enter", "space":
			return m.confirmChoice()
		}
		return nil
	}
	if key == "ctrl+c" || key == "ctrl+x" {
		start, end := m.modal.input.selection()
		if start == end {
			return nil
		}
		text := string(m.modal.input.text[start:end])
		if key == "ctrl+x" {
			m.modal.input.delete(start, end)
		}
		m.status = "Copy requested through terminal OSC52"
		return tea.SetClipboard(text)
	}
	if key == "ctrl+v" {
		m.status = "Use your terminal's native Paste command"
		return nil
	}
	count := 0
	if m.modal.kind == "palette" || m.modal.kind == "catalog-actions" {
		count = len(m.paletteItems())
	} else if m.modal.kind == "history" {
		count = len(m.modal.historyRows)
	} else if m.modal.kind == "buffer-connection" {
		count = len(m.modal.choices)
		if key == "enter" || key == "space" {
			m.chooseBufferConnection(max(0, min(m.modal.index, count-1)))
			return nil
		}
	} else if m.modal.kind == "copy-as" {
		count = len(copyFormats)
		if key == "enter" || key == "space" {
			m.copyAsIndex = max(0, min(m.modal.index, count-1))
			m.modal = modal{}
			return m.copyAs(copyFormats[m.copyAsIndex].id)
		}
	}
	if count > 0 {
		switch key {
		case "tab":
			m.modal.index = (m.modal.index + 1) % count
			return nil
		case "shift+tab":
			m.modal.index = (m.modal.index + count - 1) % count
			return nil
		case "down":
			m.modal.index = min(count-1, m.modal.index+1)
			return nil
		case "up":
			m.modal.index = max(0, m.modal.index-1)
			return nil
		case "pgdown", "pgup":
			rows := m.promptLayout(m.width, m.dimensions().bodyHeight).listRows()
			if key == "pgup" {
				m.modal.index = max(0, m.modal.index-rows)
			} else {
				m.modal.index = min(count-1, m.modal.index+rows)
			}
			return nil
		}
	}
	if key == "enter" {
		value := string(m.modal.input.text)
		switch m.modal.kind {
		case "palette", "catalog-actions":
			items := m.paletteItems()
			if len(items) > 0 {
				item := items[min(m.modal.index, len(items)-1)]
				m.modal = modal{}
				return m.action(item.id)
			}
		case "history":
			items := m.modal.historyRows
			if len(items) > 0 {
				entry := m.history[items[min(m.modal.index, len(items)-1)]]
				m.modal = modal{}
				// The SQL returns to its connection if that is still open; it never runs.
				c := m.connectionFor(entry.conn)
				if c != nil {
					m.addBuffer(entry.sql, "", true, c.id)
					m.status = "History SQL opened in a new buffer on " + termtext.Safe(c.name) + " · Ctrl+J executes it"
				} else {
					m.addBuffer(entry.sql, "", true, 0)
					m.status = "History SQL opened in a new buffer without a connection · Ctrl+P → Change buffer connection"
					if entry.connName != "" {
						m.status = "History SQL opened without a connection · " + termtext.Safe(entry.connName) + " is closed · Ctrl+P → Change buffer connection"
					}
				}
			}
		case "find":
			m.modal = modal{}
			m.lastSearch = value
			m.findNext(value)
		case "row-filter":
			m.modal = modal{}
			m.filterRows(value)
		case "goto":
			if err := m.buffers[m.bufferIndex].editor.gotoLine(value); err != nil {
				m.modal.error = err.Error()
			} else {
				m.modal = modal{}
				m.focusPane(editorPane)
			}
		case "open", "save", "export":
			return m.acceptPath(value)
		}
		return nil
	}
	if m.modalAcceptsText() {
		m.modal.input.key(msg, 1)
		m.modal.index = 0
		m.modal.error = ""
	}
	return nil
}

func (m *Model) action(id string) tea.Cmd {
	r := m.results()
	switch id {
	case "databases":
		m.openDatabases()
	case "add-database":
		if m.profileBusy || m.connecting || m.discovering {
			m.status = "Wait for the current connection operation to finish"
			return nil
		}
		m.openConnectionForm(connections.FromEnvironment())
	case "run":
		return m.runSQL(false)
	case "run-all":
		return m.runSQL(true)
	case "editor", "results", "catalog":
		if id == "editor" {
			m.focusPane(editorPane)
		} else if id == "results" {
			m.focusPane(resultsPane)
		} else {
			m.focusPane(catalogPane)
		}
	case "new":
		m.addBuffer("", "", true, m.buffers[m.bufferIndex].conn)
	case "open":
		if m.fileBusy {
			m.status = "A file operation is already in progress"
			return nil
		}
		m.openPrompt("open", "Open SQL file", "")
	case "save", "save-as":
		return m.saveBuffer(m.buffers[m.bufferIndex].id, id == "save-as")
	case "close":
		return m.closeBuffer()
	case "next-buffer":
		m.switchBuffer((m.bufferIndex + 1) % len(m.buffers))
		m.focusPane(editorPane)
	case "previous-buffer":
		m.switchBuffer((m.bufferIndex + len(m.buffers) - 1) % len(m.buffers))
		m.focusPane(editorPane)
	case "format":
		if err := m.buffers[m.bufferIndex].editor.formatSQL(); err != nil {
			m.status, m.statusError = err.Error(), true
		} else {
			m.status, m.statusError = "SQL formatted · Ctrl+Z undoes the change", false
		}
	case "comment":
		m.buffers[m.bufferIndex].editor.toggleComments()
	case "goto":
		m.openPrompt("goto", "Go to SQL line", "")
	case "complete":
		m.focusPane(editorPane)
		return m.completeWord(true)
	case "find":
		m.beginSearch()
	case "find-next":
		m.findNext(m.lastSearch)
	case "clear-filter":
		m.catalogFilter, m.inspector.filter = "", ""
		if m.inspector.json {
			m.refreshInspectorRows()
		}
	case "history":
		m.openPrompt("history", "Query history · every connection, this session only", "")
	case "inspect":
		m.openInspector()
	case "display":
		if m.inspector.active {
			m.inspector.format = cell.Next(m.inspector.format)
			m.prepareInspector()
		} else {
			m.cycleFormat()
		}
	case "copy":
		return m.copySelection(false)
	case "copy-path":
		return m.copyJSONPath()
	case "sort":
		m.focusPane(resultsPane)
		m.cycleSort()
	case "filter-rows":
		m.openRowFilter()
	case "copy-as":
		m.openCopyAs()
	case "expand":
		if m.inspector.json {
			m.inspector.tree.ExpandAll()
			m.refreshInspectorRows()
		}
	case "collapse":
		if m.inspector.json {
			m.inspector.tree.CollapseAll()
			m.inspector.index, m.inspector.top = 0, 0
			m.refreshInspectorRows()
		}
	case "export":
		m.exportPrompt()
	case "fetch-more":
		return m.fetchMore()
	case "next-result":
		r.setIndex = max(0, min(len(r.batch.Results)-1, r.setIndex+1))
		r.row, r.column, r.gridTop, r.gridLeft = 0, 0, 0, 0
		r.rowAnchor, r.columnAnchor = -1, -1
	case "previous-result":
		r.setIndex = max(0, r.setIndex-1)
		r.row, r.column, r.gridTop, r.gridLeft = 0, 0, 0, 0
		r.rowAnchor, r.columnAnchor = -1, -1
	case "refresh":
		// In the database pane, refresh the selected row's connection.
		if m.focus == catalogPane {
			return m.catalogKey(tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl})
		}
		if m.conn() == nil {
			m.status, m.statusError = "Connect a database before refreshing its catalog", false
			return nil
		}
		return m.loadCatalog(m.conn(), -1)
	case "insert-name":
		return m.catalogKey(tea.KeyPressMsg{Code: 'i', Text: "i"})
	case "preview":
		return m.catalogKey(tea.KeyPressMsg{Code: 'p', Text: "p"})
	case "structure", "ddl":
		if id == "ddl" && m.structure.active {
			return m.structureDDL()
		}
		c, node, ok := m.selectedCatalogNode()
		if !ok {
			m.status = "Select a table, view, sequence, or function in the database pane"
			return nil
		}
		if id == "ddl" {
			return m.requestDDL(c, node)
		}
		return m.openStructure(c, node)
	case "count":
		c, node, ok := m.selectedCatalogNode()
		if !ok {
			m.status = "Select a table or column in the database pane"
			return nil
		}
		if node.kind != "relation" && node.kind != "column" {
			m.status = "Select a table or column in the catalog"
			return nil
		}
		m.addBuffer("SELECT COUNT(*)\nFROM "+node.relation.QualifiedName()+";", "", true, c.id)
	case "begin", "commit", "rollback":
		m.addBuffer(strings.ToUpper(id)+";", "", true, m.buffers[m.bufferIndex].conn)
		m.status = "Transaction SQL is ready · Ctrl+J executes it"
	case "explain":
		b := m.buffers[m.bufferIndex]
		e := b.editor
		var escapes bool
		if c := m.conn(); c != nil {
			escapes = c.session.BackslashEscapes()
		}
		start, end := e.selection()
		sql := string(e.text)
		if start != end {
			sql = string(e.text[start:end])
			if len(sqltext.SplitMode(sql, escapes)) != 1 {
				m.status, m.statusError = "Select exactly one SQL statement to explain", true
				return nil
			}
		} else {
			sql = sqltext.CurrentMode(sql, len(string(e.text[:e.cursor])), escapes)
		}
		if strings.TrimSpace(sql) == "" {
			m.status = "There is no SQL statement to explain"
			return nil
		}
		m.addBuffer("EXPLAIN (FORMAT JSON)\n"+sql, "", true, b.conn)
	case "reconnect":
		return m.reconnect()
	case "buffer-connection":
		m.openBufferConnections()
	case "disconnect":
		return m.requestDisconnect(m.conn())
	case "allow-writes":
		m.requestWrites()
	case "read-only":
		return m.setWriteMode(m.conn(), true)
	case "sidebar":
		m.showCatalog = !m.showCatalog
		if !m.showCatalog && m.focus == catalogPane {
			m.focusPane(editorPane)
		}
	case "maximize":
		m.maximized = !m.maximized
	case "message":
		text := m.lastMessage
		if text == "" {
			text = m.status
		}
		m.modal = modal{kind: "message", title: "Last query details", message: text}
	case "help":
		m.openHelp()
	case "edit-database", "disconnect-database":
		if m.focus != catalogPane {
			m.status, m.statusError = "Select a database in the left pane first (F6)", false
			return nil
		}
		if id == "edit-database" {
			return m.databasePaneKey(keyMsg("e"))
		}
		return m.databasePaneKey(keyMsg("x"))
	case "quit":
		m.quitIntent, m.quitConfirmed = true, false
		m.quitDiscard = make(map[uint64]bool)
		return m.continueQuit()
	}
	return nil
}

func (m *Model) confirmChoice() tea.Cmd {
	kind, choice, target := m.modal.kind, m.modal.index, m.modal.target
	if choice == 0 {
		if kind == "confirm-unfiltered" || kind == "confirm-writes" {
			m.pendingRun = queryRun{}
			m.status, m.statusError = "Canceled · nothing was run", false
		}
		if kind == "confirm-disconnect" {
			m.status, m.statusError = "Canceled · still connected", false
		}
		m.modal = modal{}
		m.quitIntent, m.closeIntent = false, false
		m.quitDiscard = make(map[uint64]bool)
		return nil
	}
	switch kind {
	case "confirm-unfiltered":
		return m.runConfirmed()
	case "confirm-writes":
		m.modal = modal{}
		return m.setWriteMode(m.connectionFor(target), false)
	case "confirm-disconnect":
		m.modal = modal{}
		c := m.connectionFor(target)
		if c == nil {
			m.status, m.statusError = "That connection is already closed", false
			return nil
		}
		return m.disconnect(c)
	case "confirm-quit":
		m.quitConfirmed = true
		m.modal = modal{}
		return m.continueQuit()
	case "confirm-overwrite":
		m.modal = modal{kind: m.fileJob.kind, title: "Writing " + m.fileJob.path}
		return m.performFile(m.fileJob, false)
	case "confirm-dirty":
		if choice == 1 {
			return m.saveBuffer(m.confirmBuffer, false)
		}
		m.modal = modal{}
		if m.quitIntent {
			m.quitDiscard[m.confirmBuffer] = true
			return m.continueQuit()
		}
		if m.closeIntent {
			m.closeIntent = false
			for i, b := range m.buffers {
				if b.id == m.confirmBuffer {
					m.removeBuffer(i)
					break
				}
			}
		}
	}
	return nil
}
