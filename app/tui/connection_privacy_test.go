package tui

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/dustinevan/scrunchie/lib/data/connections"
)

func privacyProfile() connections.Profile {
	return connections.Profile{ID: "private-profile", Name: "Example database", Host: "selected.invalid", Port: "5432", Database: "example", User: "reader", SSLMode: "disable"}
}

func TestConnectionPasswordIsMaskedAndCannotBeCopied(t *testing.T) {
	const password = "private-'password\\雪"
	m := New(Config{})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m.openConnectionForm(privacyProfile())
	m.connectionForm.index = connectionPassword
	m.Update(tea.PasteMsg{Content: password})
	field := &m.connectionForm.fields[connectionPassword]
	if string(field.text) != password || len(field.undo) != 0 || len(field.redo) != 0 {
		t.Fatal("password paste changed the input or retained undo snapshots")
	}
	for cursor := 0; cursor <= len(field.text); cursor++ {
		field.cursor = cursor
		view := ansi.Strip(m.View().Content)
		if strings.Contains(view, password) || strings.Contains(view, "private-") || !strings.Contains(view, "****") {
			t.Fatal("password rendering disclosed text or omitted its mask")
		}
	}
	field.cursor, field.anchor = len(field.text), 0
	for _, code := range []rune{'c', 'x'} {
		if cmd := press(m, code, tea.ModCtrl); cmd != nil {
			t.Fatal("password field emitted a clipboard command")
		}
		if string(field.text) != password {
			t.Fatal("blocked password cut changed the input")
		}
	}
	field.anchor = -1
	press(m, tea.KeyBackspace)
	if len(field.undo) != 0 || len(field.redo) != 0 {
		t.Fatal("password deletion retained undo snapshots")
	}
	press(m, tea.KeyEsc)
	if m.connectionForm.id != "" || len(m.connectionForm.fields[connectionPassword].text) != 0 || m.modal.kind != "" {
		t.Fatal("closing the form retained password input")
	}
}

func TestExplicitProfileSaveNeverStoresPasswordValues(t *testing.T) {
	const typedPassword = "typed-password-sentinel"
	const envPassword = "environment-password-sentinel"
	t.Setenv("SCRUNCH_TEST_UI_PASSWORD", envPassword)
	for _, source := range []string{"typed", "environment"} {
		t.Run(source, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), ".scrunch")
			profile := privacyProfile()
			if source == "environment" {
				profile.PasswordEnv = "SCRUNCH_TEST_UI_PASSWORD"
			}
			m := New(Config{ProfilesPath: path})
			m.openConnectionForm(profile)
			m.connectionForm.index = connectionPassword
			if source == "typed" {
				m.Update(tea.PasteMsg{Content: typedPassword})
			} else if len(m.connectionForm.fields[connectionPassword].text) != 0 {
				t.Fatal("opening a profile resolved its password environment variable")
			}
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("opening or editing a form wrote configuration")
			}
			m.connectionForm.index = connectionSave
			cmd := press(m, tea.KeyEnter)
			if cmd == nil || !m.profileBusy || m.connecting || len(m.connections) != 0 {
				t.Fatal("Save failed to schedule settings only")
			}
			m.Update(cmd())
			if m.profileBusy || len(m.profiles) != 1 || m.connectionForm.id != "" || m.connecting || len(m.connections) != 0 {
				t.Fatal("Save did not finish without opening a database session")
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(data, []byte(typedPassword)) || bytes.Contains(data, []byte(envPassword)) || bytes.Contains(data, []byte(`"password":`)) || bytes.Contains(data, []byte(`"dsn":`)) {
				t.Fatal("explicit settings save persisted a password or complete DSN")
			}
			if source == "environment" && !bytes.Contains(data, []byte("SCRUNCH_TEST_UI_PASSWORD")) {
				t.Fatal("settings save lost the named environment reference")
			}
			if strings.Contains(m.View().Content+m.status+m.modal.error, typedPassword) || strings.Contains(m.View().Content+m.status+m.modal.error, envPassword) {
				t.Fatal("save output disclosed a password")
			}
		})
	}
}

func TestInvalidSavedConfigurationCannotBeReplacedFromForm(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".scrunch")
	original := []byte("malformed configuration to preserve")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	m := New(Config{ProfilesPath: path, ProfilesError: "invalid configuration"})
	m.openConnectionForm(privacyProfile())
	if m.connectionForm.remember {
		t.Fatal("unreadable saved configuration defaulted to replacement")
	}
	m.connectionForm.index = connectionPassword
	m.connectionPaste("password-sentinel")
	m.connectionForm.index = connectionSave
	if cmd := press(m, tea.KeyEnter); cmd != nil || m.profileBusy {
		t.Fatal("form scheduled replacement of unreadable configuration")
	}
	data, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(data, original) {
		t.Fatal("blocked save changed configuration")
	}
	if strings.Contains(m.modal.error+m.status, "password-sentinel") {
		t.Fatal("blocked save echoed password input")
	}
}

func TestProfileSaveFailureRetainsMemoryAndReportsNoPassword(t *testing.T) {
	profile := privacyProfile()
	m := New(Config{Profiles: []connections.Profile{profile}, ProfilesPath: t.TempDir()})
	m.openConnectionForm(profile)
	m.connectionForm.index = connectionPassword
	m.connectionPaste("password-error-sentinel")
	m.connectionForm.fields[connectionName] = newEditor("Changed name")
	m.connectionForm.index = connectionSave
	cmd := press(m, tea.KeyEnter)
	if cmd == nil {
		t.Fatal("save was not scheduled")
	}
	m.Update(cmd())
	if m.profileBusy || len(m.profiles) != 1 || m.profiles[0] != profile || m.modal.error == "" {
		t.Fatal("failed save lost remembered settings or did not report its error")
	}
	if strings.Contains(m.View().Content+m.modal.error+m.status, "password-error-sentinel") {
		t.Fatal("save error disclosed a password")
	}
}

func TestCancelConnectionWithSelectedPasswordNeverCopies(t *testing.T) {
	m := New(Config{})
	m.openConnectionForm(privacyProfile())
	m.connectionForm.index = connectionPassword
	m.connectionPaste("selected-password-sentinel")
	field := &m.connectionForm.fields[connectionPassword]
	field.anchor, field.cursor = 0, len(field.text)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.connecting, m.connectCancel = true, cancel
	if cmd := press(m, 'c', tea.ModCtrl); cmd != nil || ctx.Err() != context.Canceled {
		t.Fatal("connection cancellation emitted a command or failed to cancel")
	}
	if strings.Contains(m.status+m.modal.error+m.View().Content, "selected-password-sentinel") {
		t.Fatal("cancellation displayed the selected password")
	}
}

func TestConnectionMetadataCannotInjectTerminalCommands(t *testing.T) {
	const hostile = "name\x1b]52;c;attacker\x07\x1b[2J\u009b\u202e雪"
	profile := privacyProfile()
	profile.Name = hostile
	profile.Host = "/socket/" + hostile
	profile.Database, profile.User = hostile, hostile
	for _, state := range []string{"form", "list", "remove", "database names"} {
		m := New(Config{Profiles: []connections.Profile{profile}})
		m.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
		m.openConnectionForm(profile)
		switch state {
		case "list":
			m.openDatabases()
		case "remove":
			m.openDatabases()
			press(m, 'd')
		case "database names":
			m.modal = modal{kind: "database-names", title: "Choose database"}
			m.databaseNames = []string{hostile}
		}
		view := m.View().Content
		if strings.Contains(view, "\x1b]52;") || strings.Contains(view, "\x1b[2J") || strings.ContainsAny(view, "\x07\u009b\u202e") {
			t.Fatalf("%s rendering emitted database-controlled terminal instructions", state)
		}
		if !strings.Contains(ansi.Strip(view), `\x1b`) {
			t.Fatalf("%s did not display escaped metadata", state)
		}
	}
}
