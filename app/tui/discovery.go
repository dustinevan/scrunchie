package tui

import (
	"context"
	"errors"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/dustinevan/scrunchie/lib/data/connections"
	"github.com/dustinevan/scrunchie/lib/data/postgres"
)

type databaseNamesMsg struct {
	serial uint64
	names  []string
	err    error
}

// discoverDatabases opens a temporary maintenance connection only after the
// form's explicit List databases action. It never touches an open connection.
func (m *Model) discoverDatabases(profile connections.Profile, password string) tea.Cmd {
	if m.config.Demo {
		m.modal.error = "Demo stays offline; start scrunch without --demo to list databases"
		return nil
	}
	if m.fileBusy || m.profileBusy || m.connecting || m.discovering {
		m.modal.error = "Finish or cancel the current operation before listing databases"
		return nil
	}
	profile.Database = "postgres"
	dsn, err := profile.DSN(password)
	if err != nil {
		m.modal.error = err.Error()
		return nil
	}
	timeout := m.config.ConnectTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	m.discoveryCancel = cancel
	m.discovering = true
	m.discoverySerial++
	serial := m.discoverySerial
	attempt := &connectionAttempt{done: make(chan struct{})}
	m.discoveryAttempt = attempt
	m.modal.error = ""
	m.status, m.statusError = "Listing databases on this server · Ctrl+C cancels", false
	return func() tea.Msg {
		defer close(attempt.done)
		if !attempt.begin() {
			return databaseNamesMsg{serial: serial, err: context.Canceled}
		}
		session, err := postgres.Open(ctx, dsn, postgres.Options{})
		if err != nil {
			return databaseNamesMsg{serial: serial, err: err}
		}
		if !attempt.retain(session) {
			closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
			closeErr := session.Close(closeCtx)
			closeCancel()
			return databaseNamesMsg{serial: serial, err: errors.Join(context.Canceled, closeErr)}
		}
		names, queryErr := session.Databases(ctx)
		pending, _ := attempt.release(false)
		var closeErr error
		if pending != nil {
			closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
			closeErr = pending.Close(closeCtx)
			closeCancel()
		}
		return databaseNamesMsg{serial: serial, names: names, err: errors.Join(queryErr, closeErr)}
	}
}
