package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/dustinevan/scrunchie/lib/data/connections"
)

func modalHit(t *testing.T, m *Model, kind string, index int) dialogHit {
	t.Helper()
	var layout dialogLayout
	if m.modal.kind == "connection" || m.modal.kind == "connections" || m.modal.kind == "database-names" {
		layout = m.connectionLayout(m.width, m.dimensions().bodyHeight)
	} else {
		layout = m.promptLayout(m.width, m.dimensions().bodyHeight)
	}
	for _, hit := range layout.hits {
		if hit.kind == kind && (index < 0 || hit.index == index) {
			return hit
		}
	}
	t.Fatalf("no visible %s target %d in %s", kind, index, m.modal.kind)
	return dialogHit{}
}

func TestModalMouseTextSelectionMatchesDisplayedCharacters(t *testing.T) {
	m := New(Config{})
	m.openPrompt("find", "Find", "a雪\tbc")
	m.modal.input.cursor, m.modal.input.anchor = 0, -1
	m.prepareView()
	hit := modalHit(t, m, "prompt", -1)
	row := strings.Split(ansi.Strip(m.View().Content), "\n")[hit.area.y]
	if got := ansi.Cut(row, hit.input.x, hit.input.x+9); got != "a雪    bc" {
		t.Fatalf("prompt text and input target differ: %q", got)
	}
	mouseClick(m, hit.input.x+1, hit.input.y)
	mouseMotion(m, hit.input.x+8, hit.input.y)
	mouseRelease(m, hit.input.x+8, hit.input.y)
	e := &m.modal.input
	start, end := e.selection()
	if string(e.text[start:end]) != "雪\tb" {
		t.Fatalf("prompt drag selected %q", string(e.text[start:end]))
	}
	mouseClick(m, hit.input.x+9, hit.input.y, tea.ModShift)
	mouseRelease(m, hit.input.x+9, hit.input.y)
	start, end = e.selection()
	if string(e.text[start:end]) != "雪\tbc" || m.focus != editorPane || anyRunning(m) {
		t.Fatal("Shift-click lost the prompt selection or reached the workspace")
	}
	mouseClick(m, hit.input.x, hit.input.y)
	m.Update(tea.MouseMotionMsg{X: hit.input.x + 9, Y: hit.input.y, Button: tea.MouseNone})
	if e.cursor != 0 || m.modalMouseState.dragging {
		t.Fatal("passive mouse motion continued selecting text after the button was released")
	}
}

func TestModalMousePasswordUsesMaskedViewportAndCannotCopy(t *testing.T) {
	m := New(Config{})
	m.Update(tea.WindowSizeMsg{Width: 30, Height: 12})
	m.openConnectionForm(connections.Profile{Name: "Reports", Host: "localhost", Port: "5432", Database: "reports", User: "reader", SSLMode: "prefer"})
	password := "secret雪0123456789ABCDEFGHIJKLMN"
	field := newEditor(password)
	field.singleLine = true
	field.cursor = len(field.text)
	m.connectionForm.fields[connectionPassword] = field
	m.connectionForm.index = connectionPassword
	m.prepareView()
	hit := modalHit(t, m, "field", connectionPassword)
	view := ansi.Strip(m.View().Content)
	if strings.Contains(view, "secret") || strings.Contains(view, "ABCDEFGHIJ") || strings.Contains(view, "雪") {
		t.Fatal("password characters appeared in the form")
	}
	row := strings.Split(view, "\n")[hit.input.y]
	if got := ansi.Cut(row, hit.input.x, hit.input.x+hit.input.width); strings.TrimSpace(got) != strings.Repeat("*", hit.input.width-1) {
		t.Fatalf("password viewport was not masked: %q", got)
	}
	left := len(field.text) - hit.input.width + 1
	mouseClick(m, hit.input.x+2, hit.input.y)
	mouseMotion(m, hit.input.x+5, hit.input.y)
	mouseRelease(m, hit.input.x+5, hit.input.y)
	field = m.connectionForm.fields[connectionPassword]
	start, end := field.selection()
	if start != left+2 || end != left+5 {
		t.Fatalf("masked drag used raw rune width or the wrong scroll: %d:%d want %d:%d", start, end, left+2, left+5)
	}
	for _, code := range []rune{'c', 'x'} {
		if cmd := press(m, code, tea.ModCtrl); cmd != nil {
			t.Fatal("password mouse selection allowed clipboard access")
		}
	}
	if string(m.connectionForm.fields[connectionPassword].text) != password || len(m.connectionForm.fields[connectionPassword].undo) != 0 {
		t.Fatal("mouse selection changed the password or created an undo snapshot")
	}
}

func TestModalMouseSavedEntriesSelectBeforeExplicitActivation(t *testing.T) {
	t.Setenv("SCRUNCH_MOUSE_MISSING_PASSWORD", "")
	profile := connections.Profile{ID: "reports", Name: "Reports", Host: "localhost", Port: "5432", Database: "reports", User: "reader", SSLMode: "prefer", PasswordEnv: "SCRUNCH_MOUSE_MISSING_PASSWORD"}
	m := New(Config{Profiles: []connections.Profile{profile}})
	m.openDatabases()
	hit := modalHit(t, m, "list", 0)
	if cmd := mouseClick(m, hit.area.x+3, hit.area.y); cmd != nil || m.modal.kind != "connections" || m.connecting {
		t.Fatal("a single saved-entry click attempted a connection")
	}
	mouseRelease(m, hit.area.x+3, hit.area.y)
	mouseClick(m, hit.area.x+3, hit.area.y)
	if m.modal.kind != "connection" || !strings.Contains(m.modal.error, "missing or empty") || m.connecting {
		t.Fatal("double click did not invoke explicit saved-entry connection validation")
	}
	press(m, tea.KeyEsc)
	m.profileIndex = len(m.profiles)
	hit = modalHit(t, m, "list", len(m.profiles))
	mouseClick(m, hit.area.x+2, hit.area.y)
	if m.modal.kind != "connection" || m.connecting {
		t.Fatal("the Add button did not open its form on one click")
	}
	m = New(Config{})
	m.openConnectionForm(profile)
	m.databaseNames = []string{"one", "two"}
	m.databaseNameIndex = 0
	m.modal = modal{kind: "database-names", title: "Choose database"}
	hit = modalHit(t, m, "list", 1)
	mouseClick(m, hit.area.x+2, hit.area.y)
	if m.databaseNameIndex != 1 || m.modal.kind != "database-names" {
		t.Fatal("database name click did not remain a selection")
	}
	mouseRelease(m, hit.area.x+2, hit.area.y)
	mouseClick(m, hit.area.x+2, hit.area.y)
	if m.modal.kind != "connection" || string(m.connectionForm.fields[connectionDatabase].text) != "two" || m.connecting {
		t.Fatal("database-name double click did not only fill the form")
	}
}

func TestModalMouseButtonsAndCheckboxMatchNarrowRendering(t *testing.T) {
	for _, size := range [][2]int{{18, 8}, {30, 12}, {80, 24}} {
		m := New(Config{})
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		m.openConnectionForm(connections.Profile{Name: "Local", Host: "localhost", Port: "5432", Database: "postgres", User: "reader"})
		m.connectionForm.index = connectionRemember
		hit := modalHit(t, m, "checkbox", connectionRemember)
		row := strings.Split(ansi.Strip(m.View().Content), "\n")[hit.area.y]
		if !strings.Contains(row, "[x]") {
			t.Fatalf("Remember target is not on its rendered row at %dx%d", size[0], size[1])
		}
		mouseClick(m, hit.area.x+2, hit.area.y)
		mouseRelease(m, hit.area.x+2, hit.area.y)
		if m.connectionForm.remember || m.connecting || m.profileBusy {
			t.Fatal("Remember click activated another control")
		}
		m.connectionForm.index = connectionCancel
		hit = modalHit(t, m, "button", connectionCancel)
		row = strings.Split(ansi.Strip(m.View().Content), "\n")[hit.area.y]
		if !strings.Contains(row, "Cancel") {
			t.Fatalf("focused Cancel is clipped at %dx%d: %q", size[0], size[1], row)
		}
		mouseClick(m, hit.area.x+2, hit.area.y)
		if m.modal.kind != "" || m.connecting || m.profileBusy {
			t.Fatal("Cancel click did not return to the workspace")
		}
	}
	for _, width := range []int{12, 18, 30, 80} {
		m := New(Config{})
		m.Update(tea.WindowSizeMsg{Width: width, Height: 9})
		m.modal = modal{kind: "confirm-dirty", title: "Unsaved SQL", message: strings.Repeat("Unsaved changes. ", 10), buttons: []string{"Cancel", "Save", "Discard"}}
		for index := range m.modal.buttons {
			m.modal.index = index
			hit := modalHit(t, m, "button", index)
			row := strings.Split(ansi.Strip(m.View().Content), "\n")[hit.area.y]
			if !strings.Contains(row, m.modal.buttons[index][:min(3, len(m.modal.buttons[index]))]) {
				t.Fatalf("selected confirmation button %d is not visible at width %d: %q", index, width, row)
			}
		}
		m.modal.index = 0
		hit := modalHit(t, m, "button", 0)
		mouseClick(m, hit.area.x, hit.area.y)
		if m.modal.kind != "" || m.fileBusy {
			t.Fatal("confirmation Cancel click activated another button")
		}
	}
}

func TestModalMouseBusyStateOnlyAllowsExplicitCancellation(t *testing.T) {
	m := New(Config{InitialSQL: "untouched"})
	m.openConnectionForm(connections.Profile{Name: "Local", Host: "localhost", Port: "5432", Database: "postgres", User: "reader"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.connecting, m.connectCancel = true, cancel
	before := m.connectionForm.index
	hit := modalHit(t, m, "field", connectionHost)
	mouseClick(m, hit.area.x, hit.area.y)
	if m.connectionForm.index != before || ctx.Err() != nil {
		t.Fatal("busy field click changed focus or canceled the connection")
	}
	mouseClick(m, 0, 0)
	if m.modal.kind != "connection" || string(m.buffers[0].editor.text) != "untouched" {
		t.Fatal("background click escaped a connection modal")
	}
	hit = modalHit(t, m, "close", -1)
	row := strings.Split(ansi.Strip(m.View().Content), "\n")[hit.area.y]
	if got := ansi.Cut(row, hit.area.x, hit.area.x+hit.area.width); !strings.Contains(got, "×") {
		t.Fatalf("close hit target differs from its visible glyph: %q", got)
	}
	mouseClick(m, hit.area.x, hit.area.y)
	if ctx.Err() != context.Canceled || m.modal.kind != "connection" {
		t.Fatal("close did not request cancellation while retaining the pending form")
	}
	m.connecting, m.profileBusy = false, true
	mouseClick(m, hit.area.x+1, hit.area.y)
	if m.modal.kind != "connection" || !m.profileBusy {
		t.Fatal("close escaped before an explicit settings write finished")
	}
}

func TestModalMouseListWheelAndCloseStayInsideTheDialog(t *testing.T) {
	m := New(Config{})
	m.openPrompt("palette", "Commands", "")
	m.prepareView()
	hit := modalHit(t, m, "list", 0)
	mouseClick(m, hit.area.x+3, hit.area.y)
	if m.modal.kind != "palette" || m.modal.index != 0 {
		t.Fatal("single command click activated an action")
	}
	mouseWheel(m, hit.area.x, hit.area.y, tea.MouseWheelDown)
	if m.modal.index != 3 || m.focus != editorPane || m.connecting || anyRunning(m) {
		t.Fatal("palette wheel reached the workspace or activated a command")
	}
	mouseWheel(m, 0, 0, tea.MouseWheelDown)
	if m.modal.index != 3 {
		t.Fatal("wheel outside the dialog navigated its list")
	}
	hit = modalHit(t, m, "close", -1)
	mouseClick(m, hit.area.x, hit.area.y)
	if m.modal.kind != "" || m.focus != editorPane {
		t.Fatal("dialog close did not restore its original pane")
	}
}

func TestProfileSaveReturnsToItsLaunchingSurface(t *testing.T) {
	profile := connections.Profile{ID: "local", Name: "Local", Host: "localhost", Port: "5432", Database: "postgres", User: "reader"}
	for _, manager := range []bool{false, true} {
		m := New(Config{Profiles: []connections.Profile{profile}})
		m.openConnectionForm(profile)
		m.connectionForm.fromManager = manager
		m.profileResult(profileMsg{profiles: []connections.Profile{profile}, selectedID: profile.ID})
		want := ""
		if manager {
			want = "connections"
		}
		if m.modal.kind != want {
			t.Fatalf("manager origin %t returned to %q", manager, m.modal.kind)
		}
	}
}
