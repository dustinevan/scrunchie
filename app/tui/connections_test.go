package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/dustinevan/scrunchie/lib/data/connections"
)

func TestBlankWorkspaceDoesNotConnectWhenProfilesAreLoaded(t *testing.T) {
	profile := connections.Profile{ID: "local", Name: "Local", Host: "localhost", Port: "5432", Database: "reports", User: "reader", SSLMode: "prefer"}
	m := New(Config{Profiles: []connections.Profile{profile}})
	if cmd := m.Init(); cmd != nil || len(m.connections) != 0 || m.connecting || anyCatalogBusy(m) {
		t.Fatal("loading a remembered database started a connection or catalog request")
	}
	if len(m.buffers) != 1 || len(m.buffers[0].editor.text) != 0 || len(m.results().batch.Results) != 0 || len(m.connections) != 0 || m.modal.kind != "" {
		t.Fatal("startup was not a blank editor workspace")
	}
	if !strings.Contains(ansi.Strip(m.View().Content), "Ctrl+B") {
		t.Fatal("blank workspace did not expose Add database")
	}
	press(m, 'b', tea.ModCtrl)
	if m.modal.kind != "connections" || m.connecting {
		t.Fatal("Ctrl+B did not open the saved database list without connecting")
	}
	press(m, tea.KeyDown)
	press(m, tea.KeyEnter)
	if m.modal.kind != "connection" || m.connecting {
		t.Fatal("Add database row did not open an editable form")
	}
}

func TestConnectionFormOwnsTextAndKeepsShortcutsLocal(t *testing.T) {
	m := New(Config{})
	press(m, 'b', tea.ModCtrl)
	if m.modal.kind != "connection" || !m.connectionForm.remember {
		t.Fatal("Ctrl+B without saved entries did not open Add database with Remember checked")
	}
	for _, r := range "qjkliv?/[" {
		press(m, r)
	}
	if got := string(m.connectionForm.fields[connectionName].text); got != "qjkliv?/[" {
		t.Fatalf("form name = %q", got)
	}
	for _, msg := range []tea.KeyPressMsg{key(tea.KeyF5), key('j', tea.ModCtrl), key('n', tea.ModCtrl), key('o', tea.ModCtrl), key('p', tea.ModCtrl), key('b', tea.ModCtrl)} {
		m.Update(msg)
	}
	if m.modal.kind != "connection" || m.focus != editorPane || anyRunning(m) || m.connecting || len(m.buffers) != 1 || len(m.buffers[0].editor.text) != 0 {
		t.Fatal("connection form input escaped into workspace shortcuts")
	}
	press(m, tea.KeyEnter)
	if m.connectionForm.index != connectionHost || m.connecting {
		t.Fatal("Enter in a field connected instead of advancing")
	}
	press(m, tea.KeyTab, tea.ModShift)
	if m.connectionForm.index != connectionName {
		t.Fatal("Shift+Tab did not return to the prior field")
	}
	m.connectionForm.index = connectionRemember
	press(m, tea.KeySpace)
	if m.connectionForm.remember {
		t.Fatal("Space did not toggle Remember")
	}
	press(m, tea.KeyEnter)
	if m.connectionForm.index != connectionAllowWrites || m.connecting {
		t.Fatal("Enter on Remember should advance to Allow writes")
	}
	press(m, tea.KeyEnter)
	if m.connectionForm.index != connectionConnect || m.connecting || m.connectionForm.allowWrites {
		t.Fatal("Enter on Allow writes should advance to Connect without toggling or activating")
	}
	press(m, tea.KeyRight)
	if m.connectionForm.index != connectionSave {
		t.Fatal("Right did not select Save")
	}
	press(m, tea.KeyLeft)
	press(m, tea.KeyLeft)
	if m.connectionForm.index != connectionDiscover {
		t.Fatal("Left did not cycle through the form buttons")
	}
	press(m, tea.KeyEsc)
	if m.modal.kind != "" || m.connectionForm.id != "" {
		t.Fatal("Escape did not discard the new form and restore the workspace")
	}
}

func TestSavedDatabaseListWaitsForExplicitConnect(t *testing.T) {
	t.Setenv("SCRUNCH_TEST_MISSING_PASSWORD", "")
	profile := connections.Profile{ID: "saved", Name: "Reports", Host: "localhost", Port: "5432", Database: "reports", User: "reader", SSLMode: "prefer", PasswordEnv: "SCRUNCH_TEST_MISSING_PASSWORD"}
	m := New(Config{Profiles: []connections.Profile{profile}})
	m.openDatabases()
	for _, code := range []rune{tea.KeyDown, tea.KeyUp, tea.KeyHome, tea.KeyEnd, tea.KeyUp, 'q', 'j', 'k'} {
		press(m, code)
		if m.connecting || anyRunning(m) {
			t.Fatal("navigation started network work")
		}
	}
	press(m, tea.KeyHome)
	press(m, tea.KeyEnter)
	if m.modal.kind != "connection" || !strings.Contains(m.modal.error, "missing or empty") || m.connecting {
		t.Fatalf("explicit connect did not report credential failure in form: %q", m.modal.error)
	}
	press(m, tea.KeyEsc)
	if m.modal.kind != "connections" {
		t.Fatal("Escape did not return to remembered databases")
	}
	press(m, 'd')
	if m.modal.kind != "confirm-remove-profile" || m.modal.index != 0 {
		t.Fatal("remove confirmation did not begin on Cancel")
	}
	press(m, 'd')
	press(m, tea.KeyEnter)
	if len(m.profiles) != 1 || m.profileBusy || m.modal.kind != "connections" {
		t.Fatal("default confirmation removed settings or opened another state")
	}
}

func TestDatabaseNameSelectionOnlyFillsConnectionForm(t *testing.T) {
	m := New(Config{})
	m.openConnectionForm(connections.Profile{ID: "new", Name: "Reports", Host: "localhost", Port: "5432", Database: "postgres", User: "reader", SSLMode: "prefer"})
	m.discovering, m.discoverySerial = true, 2
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.discoveryCancel = cancel
	m.Update(databaseNamesMsg{serial: 1, names: []string{"stale"}})
	if !m.discovering || m.modal.kind != "connection" || ctx.Err() != nil {
		t.Fatal("stale database discovery changed the form")
	}
	m.Update(databaseNamesMsg{serial: 2, names: []string{"postgres", "reports", "warehouse"}})
	if m.discovering || m.modal.kind != "database-names" || ctx.Err() != context.Canceled {
		t.Fatal("discovery did not present names and finish its cancellation context")
	}
	press(m, tea.KeyDown)
	press(m, tea.KeyEnter)
	if got := string(m.connectionForm.fields[connectionDatabase].text); got != "reports" {
		t.Fatalf("selected database = %q", got)
	}
	if m.modal.kind != "connection" || m.connectionForm.index != connectionDatabase || m.connecting || len(m.connections) != 0 || anyRunning(m) {
		t.Fatal("choosing a database connected it or left the wrong input state")
	}
	m.discovering = true
	m.Update(databaseNamesMsg{serial: 2, err: errors.New("discovery error")})
	if m.modal.kind != "connection" || m.modal.error != "discovery error" || string(m.connectionForm.fields[connectionDatabase].text) != "reports" {
		t.Fatal("discovery failure closed or changed the form")
	}
}

func TestDatabaseFormAndListsFitEveryTerminalSize(t *testing.T) {
	profile := connections.Profile{ID: "local", Name: "Reports 雪", Host: "localhost", Port: "5432", Database: "reports", User: "reader", SSLMode: "verify-full", SSLRootCert: strings.Repeat("/certificate", 20)}
	for _, size := range [][2]int{{1, 1}, {4, 3}, {20, 8}, {64, 16}, {80, 24}, {120, 36}} {
		for _, state := range []string{"form", "error", "list", "names", "remove", "connecting", "saving"} {
			m := New(Config{Profiles: []connections.Profile{profile}})
			m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			m.openConnectionForm(profile)
			m.connectionForm.index = connectionSSLRootCert
			switch state {
			case "error":
				m.modal.error = strings.Repeat("Long connection error ", 30)
			case "list":
				m.openDatabases()
			case "names":
				m.databaseNames = []string{"one", "two", strings.Repeat("long", 100)}
				m.databaseNameIndex = 2
				m.modal = modal{kind: "database-names", title: "Choose database"}
			case "remove":
				m.openDatabases()
				press(m, 'd')
			case "connecting":
				m.connecting = true
			case "saving":
				m.profileBusy = true
			}
			m.prepareView()
			view := m.View().Content
			lines := strings.Split(view, "\n")
			if len(lines) != size[1] {
				t.Fatalf("%s %dx%d: got %d lines", state, size[0], size[1], len(lines))
			}
			for _, line := range lines {
				if ansi.StringWidth(line) != size[0] {
					t.Fatalf("%s %dx%d: width=%d", state, size[0], size[1], ansi.StringWidth(line))
				}
			}
			if view != m.View().Content {
				t.Fatal("connection rendering mutated model state")
			}
		}
	}
}
