package tui

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/dustinevan/scrunchie/lib/data/connections"
	"github.com/dustinevan/scrunchie/lib/data/postgres"
	"github.com/dustinevan/scrunchie/pkg/termtext"
)

type disconnectMsg struct {
	session *postgres.Session
	name    string
	err     error
}

// connectionLabel names a connection briefly: a profile's name, else the
// database it reached, else its description.
func connectionLabel(profile connections.Profile, session *postgres.Session, description string) string {
	switch {
	case profile.Name != "":
		return profile.Name
	case profile.Database != "":
		return profile.Database
	case session.Database() != "":
		return session.Database()
	case description != "":
		return description
	}
	return "PostgreSQL"
}

// conn is the open connection the current buffer's SQL runs on, or nil.
func (m *Model) conn() *connection {
	return m.connectionFor(m.buffers[m.bufferIndex].conn)
}

// connectionFor finds an open connection by id. Closed ids find nothing.
func (m *Model) connectionFor(id uint64) *connection {
	if id == 0 {
		return nil
	}
	for _, c := range m.connections {
		if c.id == id {
			return c
		}
	}
	return nil
}

// connectionForProfile finds the open connection a saved entry opened.
func (m *Model) connectionForProfile(id string) *connection {
	if id == "" {
		return nil
	}
	for _, c := range m.connections {
		if c.profile.ID == id {
			return c
		}
	}
	return nil
}

// results are the current buffer's query output.
func (m *Model) results() *bufferResults {
	return m.buffers[m.bufferIndex].results
}

// bufferQuery is the connection running a query started from buffer id.
func (m *Model) bufferQuery(id uint64) *connection {
	for _, c := range m.connections {
		if c.query.running && c.query.buffer == id {
			return c
		}
	}
	return nil
}

// busy reports work that keeps the spinner turning.
func (m *Model) busy() bool {
	if m.fileBusy || m.connecting || m.profileBusy || m.discovering {
		return true
	}
	for _, c := range m.connections {
		if c.query.running || c.catalogBusy {
			return true
		}
	}
	return false
}

// bufferName is the tab name without its connection label.
func bufferName(b buffer) string {
	if b.path == "" {
		return fmt.Sprintf("SQL %d", b.id)
	}
	return termtext.Safe(filepath.Base(b.path))
}

// bufferTab is a buffer's editor tab text. Rendering and mouse hit testing
// both measure it, so a click selects the tab it lands on.
func (m *Model) bufferTab(b buffer) string {
	label := "no connection"
	if c := m.connectionFor(b.conn); c != nil {
		label = ansi.Truncate(termtext.Safe(c.name), 16, "…")
	}
	name := bufferName(b) + " · " + label
	if b.dirty {
		name += " *"
	}
	if m.bufferQuery(b.id) != nil {
		name += " " + m.spinner.View()
	}
	return " " + name + " "
}

// switchBuffer makes buffers[index] current and shows its state: a query it
// is running, its last run's status, or which connection it uses.
func (m *Model) switchBuffer(index int) {
	index = max(0, min(index, len(m.buffers)-1))
	m.bufferIndex = index
	m.useClock++
	b := &m.buffers[index]
	b.used = m.useClock
	m.completion = completion{}
	m.completeAfterCatalog = false
	m.mouse.drag = ""
	m.gridScrolling = false
	c := m.connectionFor(b.conn)
	// Read last status describes this buffer's run, not another buffer's.
	m.lastMessage = b.results.message
	switch {
	case c != nil && c.query.running && c.query.buffer == b.id:
		m.status, m.statusError = "Query running on "+termtext.Safe(c.name)+" · Ctrl+C cancels it", false
	case c != nil && c.query.running:
		m.status, m.statusError = termtext.Safe(c.name)+" is busy with another buffer's request · Ctrl+C cancels it", false
	case b.results.status != "":
		m.status, m.statusError = b.results.status, b.results.statusError
	case c != nil:
		m.status, m.statusError = bufferName(*b)+" runs on "+termtext.Safe(c.name)+" · Ctrl+J runs SQL", false
	case len(m.connections) > 0:
		m.status, m.statusError = bufferName(*b)+" has no connection · Ctrl+P → Change buffer connection", false
	}
}

// activateConnection makes c the current buffer's connection by returning to
// the buffer that used it last, or opening a buffer for it.
func (m *Model) activateConnection(c *connection) {
	best := -1
	for i, b := range m.buffers {
		if b.conn == c.id && (best < 0 || b.used > m.buffers[best].used) {
			best = i
		}
	}
	if best < 0 {
		m.addBuffer("", "", true, c.id)
		return
	}
	m.switchBuffer(best)
}

// bindNewConnection gives a newly opened connection a buffer: the current one
// when it is empty, clean, and has no results or running query; otherwise a
// new buffer. Other buffers keep their connections.
func (m *Model) bindNewConnection(c *connection) {
	b := &m.buffers[m.bufferIndex]
	if len(b.editor.text) == 0 && b.saved == "" && b.path == "" && len(b.results.batch.Results) == 0 && b.results.err == "" && m.bufferQuery(b.id) == nil {
		b.conn = c.id
		m.switchBuffer(m.bufferIndex)
		return
	}
	m.addBuffer("", "", true, c.id)
}

// requestDisconnect closes c, asking first when that would cancel its query
// or roll back its transaction. The confirmation starts on Cancel.
func (m *Model) requestDisconnect(c *connection) tea.Cmd {
	switch {
	case m.config.Demo:
		m.status, m.statusError = "Demo stays offline; it has no connection to close", false
		return nil
	case c == nil:
		m.status, m.statusError = "This buffer has no connection to disconnect", false
		return nil
	case m.connecting && m.reconnectAttempt != nil && m.reconnectAttempt.target == c.id:
		m.status, m.statusError = "Wait for the reconnect to finish · Ctrl+C cancels it", false
		return nil
	}
	var effects []string
	if c.query.running {
		effects = append(effects, "cancel its running query")
	}
	switch c.txStatus {
	case 'T':
		effects = append(effects, "roll back its open transaction")
	case 'E':
		effects = append(effects, "roll back its failed transaction")
	}
	if len(effects) == 0 {
		return m.disconnect(c)
	}
	m.modal = modal{kind: "confirm-disconnect", title: "Disconnect " + c.name + "?", target: c.id,
		message: "Disconnecting " + c.name + " will " + strings.Join(effects, " and ") + ".\n\nBuffers that use it keep their SQL and results.",
		buttons: []string{"Cancel", "Disconnect"}}
	return nil
}

// disconnect removes c from the workspace at once and closes its session in
// the background. Its buffers keep their SQL and results but have no
// connection; replies still on their way find no connection and are dropped.
func (m *Model) disconnect(c *connection) tea.Cmd {
	if c.query.cancel != nil {
		c.query.cancel()
	}
	if c.catalogCancel != nil {
		c.catalogCancel()
	}
	for i, open := range m.connections {
		if open == c {
			m.connections = append(m.connections[:i:i], m.connections[i+1:]...)
			break
		}
	}
	unbound := 0
	for i := range m.buffers {
		if m.buffers[i].conn == c.id {
			m.buffers[i].conn = 0
			unbound++
		}
	}
	if m.structure.conn == c.id && m.structure.loading {
		m.structure.loading, m.structure.queued = false, false
		m.structure.err = "Connection closed · the structure was not read"
	}
	if m.ddl.conn == c.id {
		m.ddl = ddlRequest{}
	}
	m.completion = completion{}
	m.completeAfterCatalog = false
	name := termtext.Safe(c.name)
	m.status, m.statusError = "Disconnected "+name, false
	if unbound > 0 {
		m.status += " · " + plural(unbound, "buffer") + " now without a connection · Ctrl+P → Change buffer connection"
	}
	m.lastMessage = m.status
	if c.session == nil {
		return nil
	}
	session := c.session
	m.closing = append(m.closing, session)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		return disconnectMsg{session: session, name: name, err: session.Close(ctx)}
	}
}

func (m *Model) disconnectResult(msg disconnectMsg) tea.Cmd {
	for i, session := range m.closing {
		if session == msg.session {
			m.closing = append(m.closing[:i:i], m.closing[i+1:]...)
			break
		}
	}
	if msg.err != nil {
		m.status, m.statusError = "Closing "+msg.name+": "+msg.err.Error(), true
		m.lastMessage = m.status
	}
	return nil
}

// openBufferConnections lists open connections, and none, for the current
// buffer. A buffer keeps its connection while its query runs. The dialog
// records the buffer and the listed ids, so connections opening meanwhile
// cannot shift the choice.
func (m *Model) openBufferConnections() {
	b := m.buffers[m.bufferIndex]
	if c := m.bufferQuery(b.id); c != nil {
		m.status, m.statusError = bufferName(b)+" is running a query on "+termtext.Safe(c.name)+" · Ctrl+C cancels it", false
		return
	}
	if m.config.Demo {
		m.status, m.statusError = "Demo has one offline connection", false
		return
	}
	if len(m.connections) == 0 {
		m.status, m.statusError = "No database is open · Ctrl+B opens or adds one", false
		return
	}
	choices := make([]uint64, 0, len(m.connections)+1)
	index := len(m.connections)
	for i, c := range m.connections {
		choices = append(choices, c.id)
		if c.id == b.conn {
			index = i
		}
	}
	choices = append(choices, 0)
	m.modal = modal{kind: "buffer-connection", title: "Connection for " + bufferName(b), index: index, target: b.id, choices: choices}
}

// chooseBufferConnection binds the chooser's buffer to its listed connection
// at index, or to none. Its results stay, labeled with their origin.
func (m *Model) chooseBufferConnection(index int) {
	target, choices := m.modal.target, m.modal.choices
	m.modal = modal{}
	position := -1
	for i, b := range m.buffers {
		if b.id == target {
			position = i
		}
	}
	if position < 0 || index < 0 || index >= len(choices) {
		return
	}
	b := &m.buffers[position]
	if c := m.bufferQuery(b.id); c != nil {
		m.status, m.statusError = bufferName(*b)+" is running a query on "+termtext.Safe(c.name)+" · Ctrl+C cancels it", false
		return
	}
	label := "no connection · Run needs one"
	c := m.connectionFor(choices[index])
	if choices[index] != 0 && c == nil {
		m.status, m.statusError = "That connection closed · "+bufferName(*b)+" is unchanged", false
		return
	}
	b.conn = 0
	if c != nil {
		b.conn, label = c.id, termtext.Safe(c.name)
	}
	m.completion = completion{}
	m.completeAfterCatalog = false
	m.status, m.statusError = bufferName(*b)+" now uses "+label, false
}

// connectionRow describes an open connection for lists: its name, optionally
// its database, then markers for writes and transactions, plain so callers
// can style it once.
func connectionRow(c *connection, demo, database bool) string {
	name := c.name
	if database && c.profile.Database != "" && c.profile.Database != c.name {
		name += " · " + c.profile.Database
	}
	line := termtext.Safe(name)
	if demo {
		return line + " · offline"
	}
	if c.writable {
		line += " [writes]"
	}
	switch c.txStatus {
	case 'T':
		line += " [tx]"
	case 'E':
		line += " [tx failed]"
	}
	if !c.connected {
		line += " · disconnected"
	}
	return line
}

// quitRisks lists the connections whose work quitting would discard.
func (m *Model) quitRisks() []string {
	var lines []string
	for _, c := range m.connections {
		var effects []string
		if c.query.running {
			effects = append(effects, "running query will be canceled")
		}
		switch c.txStatus {
		case 'T':
			effects = append(effects, "open transaction will roll back")
		case 'E':
			effects = append(effects, "failed transaction will roll back")
		}
		if len(effects) > 0 {
			lines = append(lines, "• "+c.name+": "+strings.Join(effects, "; "))
		}
	}
	return lines
}
