package tui

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/dustinevan/scrunchie/lib/data/postgres"
	"github.com/dustinevan/scrunchie/pkg/result"
)

func TestLiveReconnectKeepsAConnectionTheUserMadeReadOnly(t *testing.T) {
	dsn, _ := testConnection(t)
	session, _ := liveSession(t, true)
	m := New(Config{Session: session, DSN: dsn, AllowWrites: true, ProfilesPath: filepath.Join(t.TempDir(), ".scrunch")})
	closeTestModel(t, m)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	finishCommands(t, m, m.Init())
	c := m.conn()
	if !c.writable {
		t.Fatalf("setup: writes should start on: %s", m.status)
	}
	finishCommands(t, m, m.setWriteMode(c, true))
	if c.writable {
		t.Fatalf("Make read-only did not take effect: %s", m.status)
	}
	finishCommands(t, m, m.reconnect())
	c = m.conn()
	if c == nil || c.session == session || c.writable || !c.session.ReadOnly() {
		t.Fatalf("reconnect turned writes back on: %s", m.status)
	}
}

func TestFetchMoreKeepsSortAndFilter(t *testing.T) {
	m := New(Config{Demo: true})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	page := func(rows int) result.Batch {
		set := result.Set{Command: fmt.Sprintf("SELECT %d", rows), Columns: []result.Column{{Name: "n", Type: "int4", OID: 23}, {Name: "s", Type: "text", OID: 25}}, RowCount: int64(rows), HasMore: true}
		for i := 0; i < rows; i++ {
			parity := "even"
			if i%2 == 1 {
				parity = "odd"
			}
			set.Rows = append(set.Rows, []result.Cell{{Text: fmt.Sprint(i)}, {Text: parity}})
		}
		return result.Batch{Connected: true, TxStatus: 'I', Results: []result.Set{set}}
	}
	c := m.conn()
	r := m.results()
	r.batch = page(10)
	r.origin = runOrigin{sql: "select", buffer: m.buffers[m.bufferIndex].id, conn: c.id, page: 10}
	m.prepareView()
	m.focusPane(resultsPane)
	press(m, 's')
	press(m, 's')
	applyFilter(m, "s:odd")
	m.prepareView()
	if !r.view.changed() || len(r.view.rows) != 5 || r.view.rows[0] != 9 {
		t.Fatalf("setup view rows %v", r.view.rows)
	}
	// A larger page of the same query arrives through the ordinary reply path.
	c.query = queryState{running: true, serial: 77, fetchMore: true, buffer: m.buffers[m.bufferIndex].id, sql: "select", page: 100}
	m.queryResult(queryMsg{conn: c.id, serial: 77, batch: page(100)})
	m.prepareView()
	r = m.results()
	if r.view.order == unsorted || r.view.filter == "" || len(r.view.rows) != 50 || r.view.rows[0] != 99 {
		t.Fatalf("fetch more dropped the sort or filter: order %v filter %q rows %d first %v", r.view.order, r.view.filter, len(r.view.rows), r.view.rows[:1])
	}
}

func TestRequestThatNeverStartedKeepsConnectionState(t *testing.T) {
	m := New(Config{Demo: true})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	c := m.conn()
	c.connected, c.txStatus, c.writable = true, 'T', true
	c.query = queryState{running: true, serial: 5, buffer: m.buffers[m.bufferIndex].id}
	m.queryResult(queryMsg{conn: c.id, serial: 5, err: fmt.Errorf("executing SQL: %w", context.Canceled)})
	if !c.connected || c.txStatus != 'T' || !c.writable {
		t.Fatalf("an early cancel replaced the known session state: connected %t tx %c writable %t", c.connected, c.txStatus, c.writable)
	}
	c.query = queryState{running: true, serial: 6, buffer: m.buffers[m.bufferIndex].id}
	m.queryResult(queryMsg{conn: c.id, serial: 6, err: fmt.Errorf("executing SQL: %w", postgres.ErrBusy)})
	if !c.connected || c.txStatus != 'T' {
		t.Fatal("a busy refusal replaced the known session state")
	}
}
