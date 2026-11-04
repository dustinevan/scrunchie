package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/dustinevan/scrunchie/lib/data/connections"
	"github.com/dustinevan/scrunchie/lib/data/postgres"
)

func paneProfiles(count int) []connections.Profile {
	var profiles []connections.Profile
	for index := 0; index < count; index++ {
		profiles = append(profiles, connections.Profile{
			ID: fmt.Sprintf("saved-%d", index), Name: fmt.Sprintf("Reports %d", index),
			Host: "must-not-connect.invalid", Port: "5432", Database: "reports",
			User: "reader", SSLMode: "verify-full",
		})
	}
	return profiles
}

func TestBlankDatabasePaneProvidesAddAndReturnsFromForm(t *testing.T) {
	for _, activate := range []rune{tea.KeyEnter, tea.KeySpace} {
		m := New(Config{})
		m.focusActiveDatabase()
		rows := m.databaseRows()
		if len(rows) != 1 || rows[0].kind != databaseAdd || m.databasePane.index != 0 {
			t.Fatal("blank workspace did not select Add database")
		}
		m.databasePaneKey(key(activate))
		if m.modal.kind != "connection" || m.connectionForm.fromManager || m.connecting || anyRunning(m) {
			t.Fatal("Add did not open a local connection form")
		}
		m.connectionKey(key(tea.KeyEsc))
		if m.modal.kind != "" || m.focus != catalogPane || m.databasePane.index != 0 {
			t.Fatal("canceling Add did not return to its database-pane selection")
		}
	}
}

func TestSavedDatabaseArrowsNeverConnectAndActivationChecksCredentials(t *testing.T) {
	t.Setenv("SCRUNCH_PANE_TEST_MISSING_PASSWORD", "")
	profiles := paneProfiles(2)
	profiles[0].PasswordEnv = "SCRUNCH_PANE_TEST_MISSING_PASSWORD"
	m := New(Config{Profiles: profiles})
	m.focusActiveDatabase()
	if m.databasePane.index != 0 {
		t.Fatal("disconnected boundary navigation should select Add")
	}
	m.databasePaneKey(key(tea.KeyDown))
	if m.databaseRows()[m.databasePane.index].kind != databaseSaved || m.connecting {
		t.Fatal("moving onto a saved database activated it")
	}
	if cmd := m.databasePaneKey(key(tea.KeyRight)); cmd != nil || m.focus != editorPane || m.connecting || len(m.connections) != 0 {
		t.Fatal("Right did not enter SQL without connecting")
	}
	m.focusPane(catalogPane)
	m.databasePaneKey(key(tea.KeyEnter))
	if m.modal.kind != "connection" || m.modal.error == "" || m.connecting {
		t.Fatal("explicit activation did not report the missing password environment value")
	}
	if string(m.connectionForm.fields[connectionPasswordEnv].text) != profiles[0].PasswordEnv {
		t.Fatal("connection correction form lost the password environment name")
	}
}

func TestProfileValidationFailureRestoresItsActualOrigin(t *testing.T) {
	t.Setenv("SCRUNCH_PANE_TEST_MISSING_PASSWORD", "")
	for _, manager := range []bool{false, true} {
		for _, existingForm := range []bool{false, true} {
			profile := paneProfiles(1)[0]
			profile.PasswordEnv = "SCRUNCH_PANE_TEST_MISSING_PASSWORD"
			m := New(Config{Profiles: []connections.Profile{profile}})
			m.focusPane(catalogPane)
			if manager {
				m.openDatabases()
			}
			if existingForm {
				m.openConnectionForm(profile)
				m.connectionForm.fields[connectionName] = newEditor("Keep these form edits")
			}
			m.connectProfile(profile, "", false)
			if m.modal.kind != "connection" || m.connectionForm.fromManager != manager || m.connecting {
				t.Fatalf("validation changed origin: manager=%t existing form=%t", manager, existingForm)
			}
			if existingForm && string(m.connectionForm.fields[connectionName].text) != "Keep these form edits" {
				t.Fatal("validation replaced the existing form")
			}
			m.connectionKey(key(tea.KeyEsc))
			want := ""
			if manager {
				want = "connections"
			}
			if m.modal.kind != want || m.focus != catalogPane {
				t.Fatalf("Escape returned to %q, want %q", m.modal.kind, want)
			}
		}
	}
}

func TestDeliveredProfileFailureRetainsTheRequestOrigin(t *testing.T) {
	for _, manager := range []bool{false, true} {
		for _, existingForm := range []bool{false, true} {
			profile := paneProfiles(1)[0]
			m := New(Config{Profiles: []connections.Profile{profile}})
			m.focusPane(catalogPane)
			if manager {
				m.openDatabases()
			}
			if existingForm {
				m.openConnectionForm(profile)
				m.connectionForm.fields[connectionName] = newEditor("Keep these form edits")
			}
			// Prepare, but never execute, the connection command. Delivering a
			// failure exercises the UI callback without contacting a database.
			if cmd := m.connectProfile(profile, "", false); cmd == nil || m.reconnectAttempt == nil {
				t.Fatal("explicit connect did not create an attempt")
			}
			if m.reconnectAttempt.fromManager != manager {
				m.Cancel()
				t.Fatal("attempt did not capture its origin")
			}
			if !existingForm {
				m.modal = modal{kind: "help", title: "Opened while connecting"}
			}
			m.Update(reconnectMsg{attempt: m.reconnectAttempt, profile: &profile, err: errors.New("synthetic connection failure")})
			m.Cancel()
			if m.modal.kind != "connection" || m.connectionForm.fromManager != manager || m.connecting {
				t.Fatalf("delivery changed origin: manager=%t existing form=%t", manager, existingForm)
			}
			if existingForm && string(m.connectionForm.fields[connectionName].text) != "Keep these form edits" {
				t.Fatal("failure replaced the existing form")
			}
			m.connectionKey(key(tea.KeyEsc))
			want := ""
			if manager {
				want = "connections"
			}
			if m.modal.kind != want || m.focus != catalogPane {
				t.Fatalf("Escape returned to %q, want %q", m.modal.kind, want)
			}
		}
	}
}

func TestDisconnectedActiveDatabaseRequiresExplicitReconnect(t *testing.T) {
	for _, activate := range []rune{tea.KeyEnter, tea.KeySpace} {
		// No command runs. The empty session only represents a previous target
		// whose connection has ended, as reported by its final query result.
		session := &postgres.Session{}
		m := New(Config{Session: session, DSN: "host=must-not-connect.invalid dbname=reports"})
		m.conn().connected = false
		m.conn().catalog = []catalogNode{{kind: "schema", name: "unloaded", schema: "unloaded", parent: -1}}
		if cmd := m.loadCatalog(m.conn(), 0); cmd != nil || anyCatalogBusy(m) {
			t.Fatal("closed-session metadata refresh returned a database command")
		}
		m.focusActiveDatabase()
		m.databasePaneKey(key(tea.KeyDown))
		if cmd := press(m, tea.KeyRight); cmd != nil || m.focus != editorPane || anyCatalogBusy(m) || m.connecting {
			t.Fatal("Right on disconnected metadata did not enter SQL without loading or reconnecting")
		}
		m.focusPane(catalogPane)
		if cmd := m.databasePaneKey(key(activate)); cmd != nil || anyCatalogBusy(m) || m.connecting || !strings.Contains(m.status, "reconnect") {
			t.Fatal("activating disconnected metadata attempted to load or reconnect")
		}
		m.focusActiveDatabase()
		cmd := m.databasePaneKey(key(activate))
		if cmd == nil || !m.connecting || m.conn().session != session {
			t.Fatal("activation did not prepare an explicit replacement connection")
		}
		m.Cancel()
	}
	model := New(Config{Demo: true})
	model.focusActiveDatabase()
	if cmd := model.databasePaneKey(key(tea.KeyEnter)); cmd != nil || model.connecting || !model.conn().collapsed {
		t.Fatal("offline Demo treated its sample database as a lost connection")
	}
}

func TestDatabaseTreeActivationAndRightReturnToSQL(t *testing.T) {
	m := New(Config{Demo: true, Profiles: paneProfiles(1)})
	m.buffers[0].editor.cursor, m.buffers[0].editor.anchor = 4, 1
	for index := range m.databaseRows() {
		m.focusPane(catalogPane)
		m.selectDatabaseRow(index)
		before := m.databasePaneView(40, 20)
		if cmd := press(m, tea.KeyRight); cmd != nil || m.focus != editorPane || anyRunning(m) || m.connecting || anyCatalogBusy(m) || m.modal.kind != "" {
			t.Fatalf("Right on row %d did not enter SQL without starting work", index)
		}
		if m.databasePaneView(40, 20) != before || m.buffers[0].editor.cursor != 4 || m.buffers[0].editor.anchor != 1 {
			t.Fatalf("Right on row %d changed the tree or SQL selection", index)
		}
	}
	m.focusActiveDatabase()
	m.databasePaneKey(key(tea.KeyLeft))
	m.prepareView()
	if !m.conn().collapsed || len(m.databaseRows()) != 3 {
		t.Fatal("Left on the active database did not collapse its entire catalog")
	}
	m.databasePaneKey(key(tea.KeyEnter))
	if m.conn().collapsed || m.databaseRows()[m.databasePane.index].kind != databaseConnection {
		t.Fatal("Enter did not expand the database in place")
	}
	m.databasePaneKey(key(tea.KeyDown))
	_, node, ok := m.selectedCatalogNode()
	if !ok || node.kind != "schema" {
		t.Fatal("Down did not enter the schema")
	}
	m.databasePaneKey(key(tea.KeyLeft))
	m.prepareView()
	_, node, _ = m.selectedCatalogNode()
	if node.expanded {
		t.Fatal("Left on an expanded schema did not collapse it in place")
	}
	m.databasePaneKey(key(tea.KeyLeft))
	if m.databaseRows()[m.databasePane.index].kind != databaseConnection {
		t.Fatal("Left on a collapsed schema did not select its database parent")
	}
	m.databasePaneKey(key(tea.KeyDown))
	m.databasePaneKey(key(tea.KeySpace))
	m.databasePaneKey(key(tea.KeyDown))
	_, node, ok = m.selectedCatalogNode()
	if !ok || node.kind != "relation" {
		t.Fatal("schema expansion did not lead into its relation")
	}
	m.databasePaneKey(key(tea.KeyDown))
	_, node, ok = m.selectedCatalogNode()
	if !ok || node.kind != "column" {
		t.Fatal("expanded relation did not lead into its first column")
	}
	if cmd := m.databasePaneKey(key(tea.KeyRight)); cmd != nil || m.focus != editorPane || anyRunning(m) {
		t.Fatal("Right on a column did not return to SQL without preparing or running a query")
	}
}

func TestEmptyDatabaseContainersReturnToSQL(t *testing.T) {
	m := New(Config{Demo: true})
	m.conn().catalog = nil
	m.focusActiveDatabase()
	m.databasePaneKey(key(tea.KeyLeft))
	m.databasePaneKey(key(tea.KeyRight))
	if m.focus != editorPane || !m.conn().collapsed {
		t.Fatal("Right did not leave the empty database collapsed and enter SQL")
	}
	m.conn().catalog = []catalogNode{{kind: "schema", name: "empty", schema: "empty", parent: -1, loaded: true}}
	m.focusActiveDatabase()
	m.databasePaneKey(key(tea.KeyEnter))
	m.databasePaneKey(key(tea.KeyDown))
	m.databasePaneKey(key(tea.KeyRight))
	if m.focus != editorPane || m.conn().catalog[0].expanded {
		t.Fatal("Right did not leave the empty schema collapsed and enter SQL")
	}
}

func TestCatalogFilterPreservesParentsAndEscapeRestoresSelection(t *testing.T) {
	m := New(Config{Demo: true})
	m.focusActiveDatabase()
	m.findNext("diary")
	m.prepareView()
	_, node, ok := m.selectedCatalogNode()
	if !ok || node.name != "diary" {
		t.Fatal("catalog filter did not select its first actual matching object")
	}
	m.databasePaneKey(key(tea.KeyLeft))
	m.databasePaneKey(key(tea.KeyLeft))
	_, node, ok = m.selectedCatalogNode()
	if !ok || node.kind != "schema" {
		t.Fatal("catalog filter omitted the matching column's ancestors")
	}
	if cmd := press(m, tea.KeyRight); cmd != nil || m.focus != editorPane || m.catalogFilter != "diary" {
		t.Fatal("Right in a filtered tree did not enter SQL while retaining the filter")
	}
	m.focusPane(catalogPane)
	m.databasePaneKey(key(tea.KeyEnter))
	m.databasePaneKey(key(tea.KeySpace))
	_, node, ok = m.selectedCatalogNode()
	if !ok || node.name != "diary" {
		t.Fatal("filtered ancestry could not be traversed to the matching column")
	}
	m.databasePaneKey(key(tea.KeyEsc))
	m.prepareView()
	_, node, ok = m.selectedCatalogNode()
	if m.catalogFilter != "" || m.focus != catalogPane || !ok || node.name != "diary" {
		t.Fatal("first Escape did not clear the filter while preserving its selected object")
	}
	m.databasePaneKey(key(tea.KeyEsc))
	if m.focus != editorPane {
		t.Fatal("Escape without a filter did not return to SQL")
	}
}

func TestDatabaseRowsCannotUseAStaleCatalogSelection(t *testing.T) {
	m := New(Config{Demo: true})
	m.focusActiveDatabase()
	m.databasePaneKey(key(tea.KeyDown))
	m.databasePaneKey(key(tea.KeyDown))
	if _, node, ok := m.selectedCatalogNode(); !ok || node.kind != "relation" {
		t.Fatal("relation selection failed")
	}
	m.focusActiveDatabase()
	if _, _, ok := m.selectedCatalogNode(); ok {
		t.Fatal("active database was mistaken for the previously selected relation")
	}
	before := len(m.buffers)
	sql := string(m.buffers[0].editor.text)
	for _, action := range []string{"preview", "insert-name", "structure", "ddl", "count"} {
		m.action(action)
		if len(m.buffers) != before || anyRunning(m) || string(m.buffers[0].editor.text) != sql {
			t.Fatalf("%s acted on a stale catalog object", action)
		}
	}
	if cmd := m.copySelection(false); cmd != nil {
		t.Fatal("copy on the database row copied the previously selected relation")
	}
}

func TestDatabaseWheelScrollingDoesNotMoveSelection(t *testing.T) {
	m := New(Config{Profiles: paneProfiles(20)})
	m.focusPane(catalogPane)
	m.selectDatabaseRow(1)
	m.prepareDatabasePane(4)
	m.scrollDatabaseRows(10)
	m.prepareDatabasePane(4)
	visible := m.visibleDatabaseRows(4)
	if m.databasePane.index != 1 || m.databasePane.top != 11 || !m.databasePane.manualScroll || len(visible) != 4 || visible[0] != 0 || visible[1] != 11 {
		t.Fatalf("wheel changed selection or snapped back: state=%+v visible=%v", m.databasePane, visible)
	}
	m.databasePaneKey(key(tea.KeyDown))
	m.prepareDatabasePane(4)
	if m.databasePane.index != 2 || m.databasePane.manualScroll || m.databasePane.top != 2 || m.connecting {
		t.Fatal("keyboard navigation did not return to its remembered selection")
	}
	m.selectDatabaseRow(0)
	m.scrollDatabaseRows(10)
	m.prepareDatabasePane(4)
	if m.databasePane.index != 0 || m.databasePane.top != 11 || m.visibleDatabaseRows(4)[0] != 0 {
		t.Fatal("selected pinned Add prevented manual scrolling")
	}
	m.databasePaneKey(key(tea.KeyHome))
	m.prepareDatabasePane(4)
	if m.databasePane.index != 0 || m.databasePane.top != 1 || m.databasePane.manualScroll {
		t.Fatal("Home did not restore Add and the beginning of the tree")
	}
}

func TestDatabaseViewportRowsMatchRenderedRowsAtEveryHeight(t *testing.T) {
	m := New(Config{Profiles: paneProfiles(20)})
	for height := 1; height <= 8; height++ {
		m.selectDatabaseRow(20)
		m.prepareDatabasePane(height)
		visible := m.visibleDatabaseRows(height)
		if len(visible) != height || visible[len(visible)-1] != 20 || (height > 1 && visible[0] != 0) {
			t.Fatalf("height %d: unexpected mouse rows %v", height, visible)
		}
		view := ansi.Strip(m.databasePaneView(40, height))
		if strings.Count(view, "\n") != height-1 || !strings.Contains(view, "Reports 19") || (height > 1 && !strings.Contains(view, "+ Add database")) {
			t.Fatalf("height %d: rendering does not match selectable rows: %q", height, view)
		}
	}
}

func TestCatalogCompletionDoesNotReopenACollapsedLoadingNode(t *testing.T) {
	m := New(Config{Demo: true})
	m.conn().catalog = []catalogNode{{kind: "schema", name: "sample", schema: "sample", parent: -1, loading: true, expanded: true}}
	m.conn().catalogBusy, m.conn().catalogSerial = true, 12
	m.focusActiveDatabase()
	m.databasePaneKey(key(tea.KeyDown))
	m.databasePaneKey(key(tea.KeyLeft))
	if m.conn().catalog[0].expanded {
		t.Fatal("a loading schema could not be collapsed")
	}
	m.Update(catalogMsg{conn: m.conn().id, serial: 12, parent: 0, nodes: []catalogNode{{kind: "relation", name: "events", schema: "sample", relation: postgres.Relation{Schema: "sample", Name: "events"}}}})
	if m.conn().catalog[0].loading || !m.conn().catalog[0].loaded || m.conn().catalog[0].expanded || len(m.databaseRows()) != 3 {
		t.Fatal("metadata completion lost the explicit collapse")
	}
	m.databasePaneKey(key(tea.KeyEnter))
	m.databasePaneKey(key(tea.KeyDown))
	if _, node, ok := m.selectedCatalogNode(); !ok || node.name != "events" {
		t.Fatal("completed metadata could not be expanded and entered later")
	}
}

func TestCatalogExpansionFailureAndQueuedCollapseRemainRecoverable(t *testing.T) {
	m := New(Config{Demo: true})
	m.conn().catalog = []catalogNode{{kind: "schema", name: "sample", schema: "sample", parent: -1, loading: true, expanded: true}}
	m.conn().catalogBusy, m.conn().catalogSerial = true, 13
	m.Update(catalogMsg{conn: m.conn().id, serial: 13, parent: 0, err: errors.New("metadata unavailable")})
	if m.conn().catalog[0].loading || m.conn().catalog[0].loaded || !m.statusError {
		t.Fatal("failed metadata request left an unrepeatable loading state")
	}
	m.focusActiveDatabase()
	m.databasePaneKey(key(tea.KeyDown))
	m.conn().query.running, m.conn().catalogQueue = true, []string{m.conn().catalog[0].identity()}
	m.databasePaneKey(key(tea.KeyLeft))
	if len(m.conn().catalogQueue) != 0 || m.conn().catalog[0].expanded || !anyRunning(m) {
		t.Fatal("collapse did not cancel only the queued metadata expansion")
	}
}

// rowAction performs a database-pane row action the way "." and the command
// palette do. Typed letters in the pane filter instead.
func rowAction(m *Model, letter rune) tea.Cmd {
	cmd := m.databasePaneKey(key(letter))
	m.prepareView()
	return cmd
}
