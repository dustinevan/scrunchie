package tui

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/dustinevan/scrunchie/lib/data/connections"
	"github.com/dustinevan/scrunchie/lib/data/postgres"
	"github.com/dustinevan/scrunchie/pkg/termtext"
)

type reconnectMsg struct {
	session  *postgres.Session
	attempt  *connectionAttempt
	profile  *connections.Profile
	password string
	remember bool
	dsn      string
	warning  error
	err      error
	// allowWrites is the mode this target opens with, kept for Reconnect.
	allowWrites bool
}

// connectionAttempt owns a connection between the background Open call and
// event-loop delivery. Shutdown can reclaim it even if its message is dropped.
// target is the open connection a Reconnect replaces, or 0 for a new one.
type connectionAttempt struct {
	mu          sync.Mutex
	session     *postgres.Session
	closed      bool
	started     bool
	done        chan struct{}
	fromManager bool // Fixed at request time; completion restores this UI origin.
	target      uint64
}

func (a *connectionAttempt) begin() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return false
	}
	a.started = true
	return true
}

func (a *connectionAttempt) retain(session *postgres.Session) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return false
	}
	a.session = session
	return true
}

func (a *connectionAttempt) release(closing bool) (*postgres.Session, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if closing {
		a.closed = true
	}
	session := a.session
	a.session = nil
	return session, a.started
}

func (a *connectionAttempt) close(ctx context.Context) error {
	if a == nil {
		return nil
	}
	pending, started := a.release(true)
	var closeErr error
	if pending != nil {
		closeErr = pending.Close(ctx)
	}
	if started {
		select {
		case <-a.done:
		case <-ctx.Done():
			closeErr = errors.Join(closeErr, ctx.Err())
		}
	}
	return closeErr
}

// Close cancels outstanding work and closes every open session, including
// one established by a pending Reconnect and those still closing after a
// Disconnect. Closing a session never commits its transaction.
func (m *Model) Close(ctx context.Context) error {
	m.Cancel()
	closeErr := errors.Join(m.reconnectAttempt.close(ctx), m.discoveryAttempt.close(ctx))
	for _, c := range m.connections {
		closeErr = errors.Join(closeErr, c.session.Close(ctx))
	}
	// Session.Close waits for the session and is a no-op once closed, so a
	// background close still in progress finishes here.
	for _, session := range m.closing {
		closeErr = errors.Join(closeErr, session.Close(ctx))
	}
	return closeErr
}

// reconnect replaces the current buffer's connection with a new session. A
// buffer without one reopens the command-line target if it is not open.
func (m *Model) reconnect() tea.Cmd {
	if c := m.conn(); c != nil {
		return m.reconnectConnection(c)
	}
	cli := m.config.DSN != "" || m.config.ConnectionExplicit
	for _, c := range m.connections {
		if c.profile.ID == "" {
			cli = false
		}
	}
	if cli {
		return m.startConnection(0, m.config.DSN, nil, "", false, m.config.AllowWrites)
	}
	if len(m.connections) == 0 && !m.config.Demo {
		m.openConnectionForm(connections.FromEnvironment())
		return nil
	}
	m.status, m.statusError = bufferName(m.buffers[m.bufferIndex])+" has no connection to reconnect · Ctrl+P → Change buffer connection", false
	return nil
}

// reconnectConnection opens a new session with c's settings and, once it
// succeeds, replaces c's session. c keeps its id, buffers, and results; no
// SQL is replayed.
func (m *Model) reconnectConnection(c *connection) tea.Cmd {
	dsn, allowWrites := c.dsn, c.allowWrites
	if c.profile.ID != "" {
		var err error
		dsn, err = c.profile.DSN(c.password)
		if err != nil {
			m.status, m.statusError = err.Error(), true
			m.lastMessage = m.status
			return nil
		}
		allowWrites = c.profile.AllowWrites
	}
	// Reconnect never turns writes on: a connection the user made read-only
	// stays read-only, and writes allowed only at runtime return to the
	// configured read-only mode.
	allowWrites = allowWrites && c.writable
	return m.startConnection(c.id, dsn, nil, "", false, allowWrites)
}

// connectProfile opens profile as an additional connection. A profile that
// is already open is not opened twice: the workspace switches to it, and
// changed settings wait for its Reconnect.
func (m *Model) connectProfile(profile connections.Profile, password string, remember bool) tea.Cmd {
	if c := m.connectionForProfile(profile.ID); c != nil {
		return m.useOpenProfile(c, profile, password, remember)
	}
	dsn, err := profile.DSN(password)
	if err != nil {
		if m.modal.kind != "connection" {
			m.openConnectionForm(profile)
		}
		m.modal.error = err.Error()
		m.status, m.statusError = err.Error(), true
		return nil
	}
	return m.startConnection(0, dsn, &profile, password, remember, profile.AllowWrites)
}

// useOpenProfile switches to the open connection for profile. Settings
// submitted from the form replace its stored ones for the next Reconnect.
func (m *Model) useOpenProfile(c *connection, profile connections.Profile, password string, remember bool) tea.Cmd {
	changed := profile != c.profile
	if changed {
		c.profile, c.name = profile, connectionLabel(profile, c.session, c.description)
		if profile.PasswordEnv != "" {
			c.password = ""
		}
	}
	if password != "" {
		c.password = password
	}
	fromForm := m.modal.kind == "connection"
	m.connectionForm.fields[connectionPassword] = editor{}
	m.activateConnection(c)
	m.status, m.statusError = termtext.Safe(c.name)+" is already open · switched to "+bufferName(m.buffers[m.bufferIndex]), false
	if changed {
		m.status += " · changed settings apply on Reconnect"
	}
	if fromForm && remember {
		return m.saveProfile(profile, true, m.status)
	}
	m.connectionForm = connectionForm{}
	m.modal = modal{}
	return nil
}

// startConnection opens a session in the background. target 0 adds a new
// connection; otherwise the session replaces target's after it opens.
func (m *Model) startConnection(target uint64, dsn string, profile *connections.Profile, password string, remember, allowWrites bool) tea.Cmd {
	if m.config.Demo {
		m.status, m.statusError = "Demo stays offline; start scrunch without --demo to add a database", false
		m.modal.error = m.status
		return nil
	}
	if m.fileBusy || m.profileBusy || m.connecting || m.discovering {
		m.status, m.statusError = "Finish or cancel the current operation before connecting", false
		m.modal.error = m.status
		return nil
	}
	var previous *postgres.Session
	if c := m.connectionFor(target); c != nil {
		if c.query.running || c.catalogBusy {
			m.status, m.statusError = "Finish or cancel "+termtext.Safe(c.name)+"'s running request before reconnecting", false
			return nil
		}
		if c.connected && (c.txStatus == 'T' || c.txStatus == 'E') {
			m.status, m.statusError = "Commit or roll back "+termtext.Safe(c.name)+"'s open transaction before reconnecting", true
			return nil
		}
		previous = c.session
	}
	m.connecting = true
	m.status, m.statusError = "Connecting to PostgreSQL · Ctrl+C cancels", false
	timeout := m.config.ConnectTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	m.connectCancel = cancel
	fromManager := m.modal.kind == "connections"
	if m.modal.kind == "connection" {
		fromManager = m.connectionForm.fromManager
	}
	attempt := &connectionAttempt{done: make(chan struct{}), fromManager: fromManager, target: target}
	m.reconnectAttempt = attempt
	return func() tea.Msg {
		defer close(attempt.done)
		msg := reconnectMsg{attempt: attempt, profile: profile, password: password, remember: remember, dsn: dsn, allowWrites: allowWrites}
		if !attempt.begin() {
			msg.err = context.Canceled
			return msg
		}
		next, err := postgres.Open(ctx, dsn, postgres.Options{AllowWrites: allowWrites})
		if err != nil {
			msg.err = err
			return msg
		}
		if ctx.Err() != nil || !attempt.retain(next) {
			closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
			closeErr := next.Close(closeCtx)
			closeCancel()
			msg.err = errors.Join(context.Canceled, closeErr)
			return msg
		}
		// A Reconnect replaces its session only after the new one opens.
		// Finish closing the old one even if cancellation arrives here.
		if previous != nil {
			closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
			msg.warning = previous.Close(closeCtx)
			closeCancel()
		}
		msg.session = next
		return msg
	}
}

func (m *Model) reconnectResult(msg reconnectMsg) tea.Cmd {
	if msg.attempt != nil && msg.attempt != m.reconnectAttempt {
		return nil
	}
	m.connecting = false
	if m.connectCancel != nil {
		m.connectCancel()
		m.connectCancel = nil
	}
	if msg.err != nil {
		m.status, m.statusError = msg.err.Error(), true
		m.lastMessage = m.status
		// The form reopens for corrections unless a dialog holds a decision or
		// typed input; then the status alone reports the failure.
		if msg.profile != nil && (m.modal.kind == "connection" || !m.modalHolds()) {
			if m.modal.kind != "connection" {
				m.openConnectionForm(*msg.profile)
				if msg.attempt != nil {
					m.connectionForm.fromManager = msg.attempt.fromManager
				}
			}
			m.modal.error = msg.err.Error()
		}
		if m.quitIntent {
			return m.continueQuit()
		}
		return nil
	}
	session, target := msg.session, uint64(0)
	if m.reconnectAttempt != nil {
		session, _ = m.reconnectAttempt.release(false)
		target = m.reconnectAttempt.target
		m.reconnectAttempt = nil
	}
	if session == nil {
		m.status, m.statusError = "Connection closed before it was attached to the workspace", true
		return nil
	}
	if target != 0 {
		c := m.connectionFor(target)
		if c == nil {
			// The connection it was meant to replace is gone; do not leak this one.
			m.closing = append(m.closing, session)
			return func() tea.Msg {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				return disconnectMsg{session: session, name: "reconnected session", err: session.Close(ctx)}
			}
		}
		c.session, c.description = session, session.Description()
		c.connected, c.txStatus, c.writable = true, 'I', !session.ReadOnly()
		m.status, m.statusError = "Reconnected "+termtext.Safe(c.name)+" · session reset · SQL was not replayed · existing results are snapshots", false
		if msg.warning != nil {
			m.status += " · " + msg.warning.Error()
			m.statusError = true
		}
		m.lastMessage = m.status
		if m.quitIntent {
			return m.continueQuit()
		}
		return m.loadCatalog(c, -1)
	}
	m.nextConnection++
	c := &connection{id: m.nextConnection, description: session.Description(), dsn: msg.dsn, allowWrites: msg.allowWrites,
		session: session, connected: true, txStatus: 'I', writable: !session.ReadOnly()}
	if msg.profile != nil {
		c.profile, c.password, c.dsn = *msg.profile, msg.password, ""
	}
	c.name = connectionLabel(c.profile, session, c.description)
	m.connections = append(m.connections, c)
	if m.modalHolds() || m.inspector.active || m.structure.active {
		// The user is busy elsewhere: ready a buffer without moving them.
		m.nextBuffer++
		m.buffers = append(m.buffers, buffer{id: m.nextBuffer, conn: c.id, editor: newEditor(""), results: newResults()})
		m.status, m.statusError = "Connected to "+termtext.Safe(c.name)+" · "+bufferName(m.buffers[len(m.buffers)-1])+" is ready for it", false
	} else {
		m.bindNewConnection(c)
		m.databasePane.selection = "conn\x00" + strconv.FormatUint(c.id, 10)
		m.focus = editorPane
		m.status, m.statusError = "Connected to "+termtext.Safe(c.name)+" · "+bufferName(m.buffers[m.bufferIndex])+" runs on it · Ctrl+J runs SQL", false
	}
	m.lastMessage = m.status
	var save tea.Cmd
	if msg.profile != nil {
		save = m.profileConnected(*msg.profile, msg.remember && !m.quitIntent)
	}
	if m.quitIntent {
		return m.continueQuit()
	}
	return tea.Batch(save, m.loadCatalog(c, -1))
}
