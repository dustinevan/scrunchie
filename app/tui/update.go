package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"github.com/dustinevan/scrunchie/lib/data/postgres"
	"github.com/dustinevan/scrunchie/lib/logic/cell"
	"github.com/dustinevan/scrunchie/lib/logic/sqledit"
	"github.com/dustinevan/scrunchie/pkg/sqltext"
	"github.com/dustinevan/scrunchie/pkg/termtext"
)

// Update routes each key to exactly one active input state.
func (m *Model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	kind, title, detail, confirming := m.modal.kind, m.modal.title, m.modal.message, m.confirmBuffer
	defer func() {
		if m.modal.kind != kind || m.modal.title != title || m.modal.message != detail || m.confirmBuffer != confirming {
			m.modalSerial++
			m.modalMouseState = modalMouseState{}
			m.mouse.drag = ""
		}
		m.prepareView()
	}()
	switch msg := message.(type) {
	case tea.WindowSizeMsg:
		m.mouse.drag = ""
		m.mouse.lastClick = time.Time{}
		m.modalMouseState = modalMouseState{}
		m.width, m.height = max(1, msg.Width), max(1, msg.Height)
		return m, nil
	case spinner.TickMsg:
		if m.busy() {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			return m, cmd
		}
	case queryMsg:
		return m, m.queryResult(msg)
	case catalogMsg:
		return m, m.catalogResult(msg)
	case writeModeMsg:
		return m, m.writeModeResult(msg)
	case completionCatalogMsg:
		return m, m.completionCatalogResult(msg)
	case reconnectMsg:
		return m, m.reconnectResult(msg)
	case disconnectMsg:
		return m, m.disconnectResult(msg)
	case fileMsg:
		return m, m.fileResult(msg)
	case profileMsg:
		return m, m.profileResult(msg)
	case databaseNamesMsg:
		return m, m.databaseNamesResult(msg)
	case tea.MouseMsg:
		return m, m.mouseEvent(msg)
	case mouseScrollMsg:
		return m, m.mouseScroll(msg)
	case tea.PasteMsg:
		m.interaction++
		m.mouse.drag = ""
		m.mouse.lastClick = time.Time{}
		m.modalMouseState = modalMouseState{}
		m.buffers[m.bufferIndex].editor.scrolling = false
		if m.modal.kind == "connection" {
			m.connectionPaste(msg.Content)
			return m, nil
		}
		if m.modal.kind != "" {
			if m.modalAcceptsText() && !m.fileBusy {
				m.modal.input.insert(msg.Content, "")
				m.modal.index = 0
			}
			return m, nil
		}
		if m.focus == editorPane && !m.inspector.active {
			m.buffers[m.bufferIndex].editor.insert(msg.Content, "")
			m.completion = completion{}
		} else {
			m.status, m.statusError = "Focus the editor (F2) before pasting SQL", false
		}
	case tea.KeyPressMsg:
		m.interaction++
		m.mouse.drag = ""
		m.mouse.lastClick = time.Time{}
		m.modalMouseState = modalMouseState{}
		m.gridScrolling = false
		m.inspector.scrolling = false
		key := msg.String()
		// Habits translate to the keys the handlers understand; inert and
		// unknown keys stop here with a hint. See habits.go.
		here := m.place()
		closedWithQ := m.closedWithQ
		m.closedWithQ = false
		if closedWithQ && key == "q" && (here == placeSQL || here == placeDatabases) {
			// A second q after q closed a view would otherwise type a q.
			m.closedWithQ = true
			m.status, m.statusError = "q already closed it · Esc also backs out · Ctrl+Q quits", false
			return m, nil
		}
		if (here == placeDatabases || here == placeFilter) && filterRune(key) {
			m.typeFilter(key)
			return m, nil
		}
		if here != "" {
			canonical, hint, _ := habit(here, key)
			if canonical == "" {
				m.status, m.statusError = hint, false
				return m, nil
			}
			if canonical != key {
				m.closedWithQ = key == "q"
				key, msg = canonical, keyMsg(canonical)
			}
		}
		// Ctrl+C cancels the current buffer's query before a connect or lookup
		// running beside it, unless the database dialogs are open or the
		// connect replaces this very connection.
		current := m.conn()
		queryFirst := current != nil && current.query.running && m.modal.kind != "connection" && m.modal.kind != "connections" && m.modal.kind != "database-names" &&
			(m.reconnectAttempt == nil || m.reconnectAttempt.target != current.id)
		if key == "ctrl+c" && m.discovering && !queryFirst {
			if m.discoveryCancel != nil {
				m.discoveryCancel()
			}
			m.status = "Canceling database discovery…"
			return m, nil
		}
		if key == "ctrl+c" && m.connecting && !queryFirst {
			if m.connectCancel != nil {
				m.connectCancel()
			}
			m.status = "Canceling connection…"
			return m, nil
		}
		// Ctrl+C cancels the request of the current buffer's connection only;
		// queries on other connections keep running.
		if c := current; key == "ctrl+c" && c != nil && c.query.running {
			if c.query.queued {
				c.query = queryState{}
				m.status = "Queued query canceled"
			} else if c.query.cancel != nil {
				c.query.cancel()
				c.query.canceling = true
				m.status = "Canceling query on " + termtext.Safe(c.name) + "…"
			}
			return m, nil
		}
		if key == "ctrl+q" {
			return m, m.beginQuit()
		}
		if m.modal.kind != "" {
			return m, m.modalKey(msg)
		}
		switch key {
		case "ctrl+tab", "ctrl+shift+tab", "shift+ctrl+tab":
			m.cyclePane(key != "ctrl+tab")
			return m, nil
		case "ctrl+b":
			m.openDatabases()
			return m, nil
		case "ctrl+p":
			m.openPrompt("palette", "Commands", "")
			return m, nil
		case "f1", "f7":
			m.openHelp()
			return m, nil
		case "f2", "f6":
			if key == "f2" {
				m.focusPane(editorPane)
			} else {
				m.focusPane(catalogPane)
			}
			return m, nil
		case "f8":
			m.openPrompt("history", "Query history · every connection, this session only", "")
			return m, nil
		case "f9":
			m.showCatalog = !m.showCatalog
			if !m.showCatalog && m.focus == catalogPane {
				m.focus = editorPane
			}
			return m, nil
		case "f10":
			m.maximized = !m.maximized
			return m, nil
		case "ctrl+j", "ctrl+enter":
			return m, m.runSQL(false)
		case "ctrl+f":
			m.beginSearch()
			return m, nil
		case "f3":
			m.findNext(m.lastSearch)
			return m, nil
		case "ctrl+c", copyKey:
			return m, m.copySelection(false)
		case "ctrl+v":
			m.status, m.statusError = "Use your terminal's Paste command; scrunch never reads the system clipboard", false
			return m, nil
		}
		if m.inspector.active {
			return m, m.inspectorKey(msg)
		}
		if m.structure.active {
			return m, m.structureKey(msg)
		}
		if m.focus != editorPane && (key == "tab" || key == "shift+tab") {
			m.cyclePane(key == "shift+tab")
			return m, nil
		}
		switch m.focus {
		case catalogPane:
			return m, m.catalogKey(msg)
		case resultsPane:
			return m, m.gridKey(msg)
		case editorPane:
			return m, m.editorKey(msg)
		}
	}
	return m, nil
}

// queryResult applies a finished query to the buffer that ran it, even when
// another buffer is current, and records it in the shared history. A reply
// for a closed connection or a superseded request is dropped.
func (m *Model) queryResult(msg queryMsg) tea.Cmd {
	c := m.connectionFor(msg.conn)
	if c == nil || !c.query.running || msg.serial != c.query.serial {
		return nil
	}
	q := c.query
	c.query = queryState{}
	if q.cancel != nil {
		q.cancel()
	}
	// A request canceled or refused before Execute reached the connection
	// (it returns before timing anything) says nothing about the session,
	// so the known transaction and write state stay as they were.
	notStarted := msg.err != nil && msg.batch.Duration == 0 && !msg.batch.Connected && len(msg.batch.Results) == 0 &&
		(errors.Is(msg.err, context.Canceled) || errors.Is(msg.err, context.DeadlineExceeded) || errors.Is(msg.err, postgres.ErrBusy))
	if !notStarted {
		c.connected, c.txStatus = msg.batch.Connected, msg.batch.TxStatus
		c.writable = msg.batch.Connected && !msg.batch.ReadOnly && !m.config.Demo
	}
	entry := historyEntry{sql: q.sql, at: q.started, duration: msg.batch.Duration, conn: c.id, connName: c.name}
	for _, set := range msg.batch.Results {
		entry.rows += set.RowCount
		entry.more = entry.more || set.HasMore
	}
	index := -1
	for i, b := range m.buffers {
		if b.id == q.buffer {
			index = i
			break
		}
	}
	current := index == m.bufferIndex
	status, statusError, detail := "", false, ""
	if msg.err != nil {
		entry.err = msg.err.Error()
		status, statusError = msg.err.Error(), true
		var pgErr postgres.Error
		if errors.As(msg.err, &pgErr) {
			if pgErr.Detail != "" {
				status += " · " + pgErr.Detail
			}
			if pgErr.Hint != "" {
				status += " · Hint: " + pgErr.Hint
			}
			if pgErr.Code == "25006" && c.session.Standby() {
				status = "This server is a read-only standby · " + status
			} else if pgErr.Code == "25006" && !c.writable {
				status = "Writes are off for this connection · Ctrl+P → Allow writes · " + status
			}
			if index >= 0 {
				b := &m.buffers[index]
				if string(b.editor.text) == q.source && pgErr.Position > 0 && m.interaction == q.interaction {
					b.editor.cursor = min(len(b.editor.text), q.offset+pgErr.Position-1)
					b.editor.anchor = -1
					b.editor.verticalMotion = false
				}
			}
		}
	} else {
		status, detail = runStatus(msg.batch, q.page, m.rowBudget())
		if len(msg.batch.Notices) > 0 {
			status += " · " + msg.batch.Notices[len(msg.batch.Notices)-1]
		}
	}
	if q.line > 0 {
		status = fmt.Sprintf("Line %d · %s", q.line, status)
	}
	message := status
	if detail != "" {
		message += "\n\n" + detail
	}
	if len(msg.batch.Notices) > 0 {
		message += "\n\nPostgreSQL notices:\n" + strings.Join(msg.batch.Notices, "\n")
	}
	if index >= 0 {
		r := m.buffers[index].results
		if current {
			if m.mouse.drag == "grid" {
				m.mouse.drag = ""
			}
			m.mouse.lastClick = time.Time{}
		}
		// A larger page of the same query keeps the selected cell and scroll.
		// A run that failed without results still replaces earlier results,
		// so they are not mistaken for its output.
		samePage := q.fetchMore && len(msg.batch.Results) == 1 && len(r.batch.Results) == 1 && len(msg.batch.Results[0].Columns) == len(r.batch.Results[0].Columns)
		r.batch, r.sql = msg.batch, q.sql
		r.conn, r.connName, r.serial = c.id, c.name, msg.serial
		if samePage && r.view.set != nil {
			// More rows of the same result keep its sort and filter: the view
			// follows the new set and is rebuilt from it in syncRowView.
			r.view.set, r.view.count = &r.batch.Results[0], -1
		} else if !current {
			// The current buffer's view resyncs in prepareView; another
			// buffer's would keep the replaced batch alive until shown.
			r.view = rowView{}
		}
		if !samePage {
			r.setIndex, r.row, r.column, r.gridTop, r.gridLeft = 0, 0, 0, 0, 0
			r.formats = make([]map[int]cell.Format, len(r.batch.Results))
			r.widths = nil
			r.rowAnchor, r.columnAnchor = -1, -1
		}
		r.origin = runOrigin{sql: q.sql, buffer: q.buffer, conn: c.id, offset: q.offset, source: q.source, page: q.page}
		r.err = ""
		if msg.err != nil && len(msg.batch.Results) == 0 {
			r.err = failureText(msg.err, q.source, q.offset)
		}
		r.status, r.statusError, r.message = status, statusError, message
	}
	if current {
		m.status, m.statusError, m.lastMessage = status, statusError, message
		if msg.err == nil && m.focus == editorPane && q.focus == editorPane && m.interaction == q.interaction && m.modal.kind == "" && !m.inspector.active {
			m.focus = resultsPane
		}
	} else {
		// Another buffer's run finished: say so briefly; its results wait there.
		where := "a closed buffer"
		if index >= 0 {
			where = bufferName(m.buffers[index])
		}
		m.status, m.statusError = "Query finished on "+termtext.Safe(c.name)+" · "+where, statusError
		if statusError {
			m.status = "Query failed on " + termtext.Safe(c.name) + " · " + where + " · " + status
		}
		m.lastMessage = m.status + "\n\n" + message
	}
	m.history = append(m.history, entry)
	m.historySerial++
	historyBytes := 0
	first := len(m.history) - 1
	for first >= 0 {
		historyBytes += len(m.history[first].sql)
		if historyBytes > 32<<20 || len(m.history)-first > 100 {
			break
		}
		first--
	}
	if first >= 0 {
		m.history = append([]historyEntry(nil), m.history[first+1:]...)
	}
	// Work queued on c still starts if the user stays instead of quitting.
	if m.quitIntent && m.modal.kind == "confirm-quit" {
		m.confirmQuit()
	} else if m.quitIntent {
		if cmd := m.continueQuit(); cmd != nil {
			return cmd
		}
	}
	return m.nextCatalogRequest(c)
}

// catalogResult applies a tree, structure, or DDL reply on its connection,
// then starts work that waited for that connection.
func (m *Model) catalogResult(msg catalogMsg) tea.Cmd {
	c := m.connectionFor(msg.conn)
	if c == nil || msg.serial != c.catalogSerial {
		return nil
	}
	c.catalogBusy = false
	if msg.kind == "" && msg.parent >= 0 && msg.parent < len(c.catalog) {
		c.catalog[msg.parent].loading = false
	}
	if c.catalogCancel != nil {
		c.catalogCancel()
		c.catalogCancel = nil
	}
	if msg.kind != "" {
		m.structureResult(c, msg)
	} else if msg.err == nil {
		if msg.parent < 0 {
			c.catalog = msg.nodes
		} else if msg.parent < len(c.catalog) {
			c.catalog[msg.parent].loaded = true
			base := len(c.catalog)
			nested := make([]bool, len(msg.nodes))
			for _, node := range msg.nodes {
				for _, child := range node.children {
					nested[child] = true
				}
			}
			for index, node := range msg.nodes {
				node.parent = msg.parent
				if !nested[index] {
					c.catalog[msg.parent].children = append(c.catalog[msg.parent].children, base+index)
				}
				children := make([]int, 0, len(node.children))
				for _, child := range node.children {
					children = append(children, base+child)
				}
				node.children = children
				c.catalog = append(c.catalog, node)
			}
			for index := base; index < len(c.catalog); index++ {
				for _, child := range c.catalog[index].children {
					c.catalog[child].parent = index
				}
			}
		}
	} else if !errors.Is(msg.err, context.Canceled) {
		m.status, m.statusError = "Catalog of "+termtext.Safe(c.name)+": "+msg.err.Error(), true
	}
	if c.query.queued {
		c.query.queued = false
		return m.queryCommand(c)
	}
	if m.completeAfterCatalog && m.conn() == c {
		if cmd := m.resumeCompletion(); cmd != nil {
			return cmd
		}
	}
	if cmd := m.nextCatalogRequest(c); cmd != nil {
		return cmd
	}
	if msg.kind == "" && msg.parent < 0 && msg.err == nil && c.txStatus != 'T' && c.txStatus != 'E' {
		for index, node := range c.catalog {
			if node.kind == "schema" && node.name == "public" && !node.loaded {
				return m.loadCatalog(c, index)
			}
		}
	}
	return nil
}

func (m *Model) runSQL(all bool) tea.Cmd {
	more := m.fetchPending
	m.fetchPending = false
	b := &m.buffers[m.bufferIndex]
	c := m.conn()
	// psql habits (\d users, \q, exit) work with or without a connection and
	// are never sent to the server. See psql.go.
	if !more {
		escapes := c != nil && c.session.BackslashEscapes()
		if cmd, handled := m.psqlFromEditor(escapes); handled {
			return cmd
		}
	}
	switch {
	case c == nil && len(m.connections) == 0:
		m.status, m.statusError = "No PostgreSQL session is connected · Ctrl+B adds a database", true
		return nil
	case c == nil:
		m.status, m.statusError = bufferName(*b)+" has no connection · Ctrl+P → Change buffer connection, or activate a database (F6)", true
		return nil
	case m.connecting && m.reconnectAttempt != nil && m.reconnectAttempt.target == c.id:
		m.status, m.statusError = "Wait for the reconnect to finish · Ctrl+C cancels it", false
		return nil
	case c.query.running && c.query.buffer == b.id:
		m.status, m.statusError = "A query is already running · Ctrl+C cancels it", false
		return nil
	case c.query.running:
		m.status, m.statusError = termtext.Safe(c.name)+" is busy with another buffer's request · wait, or Ctrl+C cancels it", false
		return nil
	}
	escapes := c.session.BackslashEscapes()
	source := string(b.editor.text)
	sql, offset := source, 0
	start, end := b.editor.selection()
	// Plain reads fetch a first page; fetching more re-runs the SQL behind the
	// current result, whatever the editor now selects.
	run := queryRun{buffer: b.id, conn: c.id, session: c.session, page: min(firstPageRows, m.rowBudget())}
	if more {
		origin := b.results.origin
		run.page, run.more = min(origin.page*10, m.rowBudget()), true
		sql, offset, source = origin.sql, origin.offset, origin.source
	} else if !all && start != end {
		sql, offset = string(b.editor.text[start:end]), start
	} else if !all {
		// Editor positions count runes; statement offsets count bytes.
		statements := sqltext.SplitMode(source, escapes)
		sql = ""
		if index := sqltext.At(statements, len(string(b.editor.text[:b.editor.cursor]))); index >= 0 {
			sql = statements[index].Text
			offset = utf8.RuneCountInString(source[:statements[index].Start])
		}
	}
	// psql runs a command line above SQL on its own; skip it rather than send
	// a backslash the server can only reject.
	for !more && isPsqlLine(firstLine(sql)) && strings.Contains(sql, "\n") {
		line, rest, _ := strings.Cut(sql, "\n")
		sql, offset = rest, offset+utf8.RuneCountInString(line)+1
	}
	if strings.TrimSpace(sql) == "" {
		m.status, m.statusError = "The editor has no SQL to run", false
		return nil
	}
	run.sql, run.offset, run.source = sql, offset, source
	sourceRunes := []rune(source)
	run.line = strings.Count(string(sourceRunes[:min(offset, len(sourceRunes))]), "\n") + 1
	for _, statement := range sqltext.SplitMode(sql, escapes) {
		if statement.Body < statement.End {
			run.line += strings.Count(sql[:statement.Body], "\n")
			break
		}
	}
	m.completion = completion{}
	m.completeAfterCatalog = false
	if findings := sqledit.Unfiltered(sql, escapes); len(findings) > 0 {
		m.confirmUnfiltered(run, findings)
		return nil
	}
	return m.startQuery(run)
}

// startQuery sends exactly the SQL chosen, and when needed confirmed, by Run,
// on the connection it was checked against. Callers ensure it is open.
func (m *Model) startQuery(run queryRun) tea.Cmd {
	c := m.connectionFor(run.conn)
	m.serial++
	c.query = queryState{running: true, serial: m.serial, started: time.Now(), sql: run.sql, buffer: run.buffer,
		focus: m.focus, interaction: m.interaction, offset: run.offset, source: run.source, line: run.line,
		page: run.page, fetchMore: run.more}
	m.status, m.statusError = "Running query · Ctrl+C to cancel", false
	if c.catalogBusy {
		c.query.queued = true
		m.status = "Finishing catalog request before running SQL · Ctrl+C to cancel"
		return m.spinner.Tick
	}
	return tea.Batch(m.queryCommand(c), m.spinner.Tick)
}

func (m *Model) queryCommand(c *connection) tea.Cmd {
	ctx, cancel := context.WithCancel(context.Background())
	if m.config.Timeout > 0 {
		cancel()
		ctx, cancel = context.WithTimeout(context.Background(), m.config.Timeout)
	}
	c.query.cancel = cancel
	id, serial, sql, session, limits, demo := c.id, c.query.serial, c.query.sql, c.session, m.config.Limits, m.config.Demo
	limits.PageRows = c.query.page
	return func() tea.Msg {
		if demo {
			return queryMsg{conn: id, serial: serial, batch: demoBatch(), err: nil}
		}
		batch, err := session.Execute(ctx, sql, limits)
		return queryMsg{conn: id, serial: serial, batch: batch, err: err}
	}
}

// beginQuit starts the quit checks for unsaved SQL, open transactions, and
// running queries. Ctrl+Q and psql's \q both use it.
func (m *Model) beginQuit() tea.Cmd {
	if m.profileBusy {
		m.status = "Wait for database settings to finish saving before quitting"
		return nil
	}
	if m.fileBusy || strings.HasPrefix(m.modal.kind, "confirm") {
		return nil
	}
	m.connectionForm = connectionForm{}
	m.quitIntent, m.closeIntent, m.quitConfirmed = true, false, false
	m.quitDiscard = make(map[uint64]bool)
	return m.continueQuit()
}
