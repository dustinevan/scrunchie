package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/dustinevan/scrunchie/lib/logic/sqledit"
	"github.com/dustinevan/scrunchie/pkg/termtext"
)

type writeModeMsg struct {
	conn     uint64
	serial   uint64
	readOnly bool
	err      error
}

// confirmUnfiltered holds checked SQL until the user explicitly runs it. The
// dialog owns input, so repeated Run keys and clicks cannot reach the editor.
func (m *Model) confirmUnfiltered(run queryRun, findings []sqledit.Finding) {
	m.pendingRun = run
	title := "Change every row?"
	var lines []string
	for i, finding := range findings {
		if i == 3 {
			lines = append(lines, fmt.Sprintf("…and %d more.", len(findings)-i))
			break
		}
		if finding.Problem != "" {
			title = "SQL could not be checked"
		}
		lines = append(lines, finding.String())
	}
	lines = append(lines, "", fmt.Sprintf("Line %d · Cancel runs nothing.", run.line))
	m.modal = modal{kind: "confirm-unfiltered", title: title, message: strings.Join(lines, "\n"), buttons: []string{"Cancel", "Run anyway"}}
	m.status, m.statusError = "Nothing has run · confirm or cancel", false
}

// runConfirmed starts the held SQL only if nothing it was checked against
// changed: the buffer's text, the buffer's connection, and that connection's
// session.
func (m *Model) runConfirmed() tea.Cmd {
	run := m.pendingRun
	m.pendingRun = queryRun{}
	m.modal = modal{}
	c := m.connectionFor(run.conn)
	changed := run.sql == "" || c == nil || c.session != run.session
	found := false
	for _, b := range m.buffers {
		if b.id == run.buffer {
			found = true
			changed = changed || b.conn != run.conn || string(b.editor.text) != run.source
		}
	}
	changed = changed || !found
	if changed || c.query.running || m.connecting && m.reconnectAttempt != nil && m.reconnectAttempt.target == c.id {
		m.status, m.statusError = "The SQL or connection changed before confirmation · nothing was run", true
		return nil
	}
	return m.startQuery(run)
}

// writeModeRefusal explains why c's write mode cannot change now, or is "".
func (m *Model) writeModeRefusal(c *connection, allow bool) string {
	switch {
	case m.config.Demo:
		return "Demo stays offline and read-only"
	case c == nil && len(m.connections) == 0:
		return "No PostgreSQL session is connected · Ctrl+B adds a database"
	case c == nil:
		return "This buffer has no connection · Ctrl+P → Change buffer connection"
	case !c.connected:
		return "The connection is closed · reconnect first"
	case c.query.running:
		return "Wait for the running query to finish · Ctrl+C cancels it"
	case m.connecting && m.reconnectAttempt != nil && m.reconnectAttempt.target == c.id:
		return "Wait for the current connection operation to finish"
	case c.txStatus == 'T' || c.txStatus == 'E':
		return "Commit or roll back the open transaction before changing the write mode"
	case allow && c.session.Standby():
		return "This server is a read-only standby; writes are not possible"
	case allow && c.writable:
		return "Writes are already on for this connection"
	case !allow && !c.writable:
		return "This connection is already read-only"
	}
	return ""
}

// requestWrites asks before allowing writes on the current buffer's
// connection, naming its database.
func (m *Model) requestWrites() {
	c := m.conn()
	if refusal := m.writeModeRefusal(c, true); refusal != "" {
		m.status, m.statusError = refusal, true
		return
	}
	database := c.description
	if c.profile.ID != "" {
		database = c.profile.Database + " (" + c.profile.Name + ")"
	}
	m.modal = modal{kind: "confirm-writes", title: "Allow writes?", target: c.id,
		message: "Allow writes on " + database + "?\n\nINSERT, UPDATE, DELETE, and DDL can change data until you make this connection read-only or reconnect. The header turns pink while writes are on.",
		buttons: []string{"Cancel", "Allow writes"}}
}

// setWriteMode sends SET default_transaction_read_only on c. It holds c's
// running state so Run, catalog, and quit wait for it.
func (m *Model) setWriteMode(c *connection, readOnly bool) tea.Cmd {
	if refusal := m.writeModeRefusal(c, !readOnly); refusal != "" {
		m.status, m.statusError = refusal, true
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	m.serial++
	c.query = queryState{running: true, cancel: cancel, serial: m.serial, started: time.Now()}
	m.status, m.statusError = "Changing the write mode · Ctrl+C cancels", false
	id, serial, session := c.id, c.query.serial, c.session
	return tea.Batch(func() tea.Msg {
		err := session.SetReadOnly(ctx, readOnly)
		return writeModeMsg{conn: id, serial: serial, readOnly: session.ReadOnly(), err: err}
	}, m.spinner.Tick)
}

func (m *Model) writeModeResult(msg writeModeMsg) tea.Cmd {
	c := m.connectionFor(msg.conn)
	if c == nil || !c.query.running || msg.serial != c.query.serial {
		return nil
	}
	if c.query.cancel != nil {
		c.query.cancel()
	}
	c.query = queryState{}
	c.writable = !msg.readOnly
	switch {
	case msg.err != nil:
		m.status, m.statusError = "Write mode unchanged: "+msg.err.Error(), true
	case c.writable:
		m.status, m.statusError = "Writes are on for "+termtext.Safe(c.name)+" · Ctrl+P → Make this connection read-only", false
	default:
		m.status, m.statusError = termtext.Safe(c.name)+" is read-only · new transactions cannot write", false
	}
	m.lastMessage = m.status
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
