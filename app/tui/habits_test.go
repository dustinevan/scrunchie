package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// placeFixture builds a model whose keyboard input lands in p, with a
// selection away from every edge so movement is visible.
func placeFixture(t *testing.T, p place) *Model {
	t.Helper()
	m := New(Config{Demo: true, InitialSQL: "SELECT 1;\nSELECT 2;\n"})
	m.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	switch p {
	case placeSQL:
		m.buffers[0].editor.cursor = 3
	case placeResults:
		m.buffers[0].results.batch = demoBatch()
		m.focusPane(resultsPane)
		m.results().row, m.results().column = 2, 1
	case placeDatabases:
		m.focusPane(catalogPane)
		m.selectDatabaseRow(3)
	case placeFilter:
		m.focusPane(catalogPane)
		m.typeFilter("i")
	case placeJSON, placeText:
		m.buffers[0].results.batch = demoBatch()
		m.focusPane(resultsPane)
		m.results().column = 2
		if p == placeText {
			m.results().column = 1
		}
		m.openInspector()
		m.inspector.index = 1
	case placeStructure:
		m.focusPane(catalogPane)
		m.structure = structureView{active: true, loaded: true, data: structureFixture(), conn: m.conn().id, origin: catalogPane, collapsed: map[string]bool{}, index: 2}
	case placeReader:
		m.openHelp()
		m.modal.index = 3
	case placeConfirm:
		m.modal = modal{kind: "confirm-dirty", title: "Unsaved SQL", message: "Save first?", buttons: []string{"Cancel", "Save", "Discard"}}
	}
	m.prepareView()
	if got := m.place(); got != p {
		t.Fatalf("fixture for %s lands in %s", p, got)
	}
	return m
}

// withoutStatus is the screen with its status line blanked.
func withoutStatus(m *Model) string {
	lines := strings.Split(ansi.Strip(m.View().Content), "\n")
	lines[len(lines)-2] = ""
	return strings.Join(lines, "\n")
}

func TestEveryKeyInTheTableIsARealKeyPress(t *testing.T) {
	check := func(where string, keys []string) {
		for _, key := range keys {
			if got := keyMsg(key).String(); got != key {
				t.Errorf("%s: keyMsg(%q).String() = %q", where, key, got)
			}
		}
	}
	for _, action := range globalActions {
		check("global", action.keys)
		check("global", action.habits)
	}
	for name, where := range places {
		for _, action := range where.actions {
			check(string(name), action.keys)
			check(string(name), action.habits)
		}
		for _, inert := range where.inert {
			check(string(name), inert.keys)
		}
	}
}

func TestEachKeyMeansOneThingInEachPlace(t *testing.T) {
	for name, where := range places {
		meaning := map[string]string{}
		record := func(key, canonical string) {
			if prior, ok := meaning[key]; ok && prior != canonical {
				t.Errorf("%s: %s means both %s and %s", name, key, prior, canonical)
			}
			meaning[key] = canonical
		}
		own := map[string]bool{}
		for _, action := range where.actions {
			for _, key := range append(append([]string{}, action.keys...), action.habits...) {
				own[key] = true
			}
		}
		for _, action := range append(append([]keyAction{}, where.actions...), globalActions...) {
			for _, key := range action.keys {
				record(key, key)
			}
			for _, habit := range action.habits {
				record(habit, action.keys[0])
			}
		}
		// An inert key may shadow a global that means nothing here, never one
		// of the place's own actions.
		for _, inert := range where.inert {
			for _, key := range inert.keys {
				if own[key] {
					t.Errorf("%s: %s is both an action and inert", name, key)
				}
				if where.typeToFilter && filterRune(key) {
					t.Errorf("%s: letters filter here, but %s is inert", name, key)
				}
			}
		}
		if where.typeToFilter {
			for key := range meaning {
				if filterRune(key) {
					t.Errorf("%s: letters filter here, but %s is bound", name, key)
				}
			}
		}
	}
}

func TestEveryHabitDoesExactlyWhatItsKeyDoes(t *testing.T) {
	for name, where := range places {
		for _, action := range append(append([]keyAction{}, where.actions...), globalActions...) {
			for _, habit := range action.habits {
				if name == placeConfirm || name == placeReader && action.keys[0] == "ctrl+j" {
					continue // dialogs ignore the global habits they own no meaning for
				}
				want, got := placeFixture(t, name), placeFixture(t, name)
				_, wantCmd := want.Update(keyMsg(action.keys[0]))
				_, gotCmd := got.Update(keyMsg(habit))
				if withoutStatus(want) != withoutStatus(got) || want.status != got.status || (wantCmd == nil) != (gotCmd == nil) {
					t.Errorf("%s: %s did not do what %s does\nwant status %q\n got status %q", name, keyLabel(habit), keyLabel(action.keys[0]), want.status, got.status)
				}
			}
		}
	}
}

func TestUnknownAndInertKeysChangeNothingButTheHint(t *testing.T) {
	unknown := []string{"w", "ctrl+t", "~", "%", "alt+z", "W"}
	for name, where := range places {
		if !where.gated {
			continue
		}
		for _, key := range unknown {
			if where.typeToFilter && filterRune(key) {
				continue
			}
			if _, _, known := habit(name, key); known {
				continue
			}
			m := placeFixture(t, name)
			before := withoutStatus(m)
			if _, cmd := m.Update(keyMsg(key)); cmd != nil || withoutStatus(m) != before || !strings.Contains(m.status, "does nothing here") {
				t.Errorf("%s: unknown %s changed the screen or gave no hint (status %q)", name, key, m.status)
			}
		}
	}
	for name, where := range places {
		for _, inert := range where.inert {
			for _, key := range inert.keys {
				m := placeFixture(t, name)
				before, text := withoutStatus(m), string(m.buffers[0].editor.text)
				if _, cmd := m.Update(keyMsg(key)); cmd != nil || withoutStatus(m) != before || string(m.buffers[0].editor.text) != text || m.status != inert.hint {
					t.Errorf("%s: inert %s changed something (status %q)", name, key, m.status)
				}
			}
		}
	}
}

func TestMashingQBacksOutButNeverQuitsOrTypes(t *testing.T) {
	for _, start := range []place{placeJSON, placeText, placeStructure, placeReader} {
		m := placeFixture(t, start)
		sql := string(m.buffers[0].editor.text)
		for range 6 {
			if cmd := press(m, 'q'); cmd != nil {
				t.Fatalf("%s: q returned a command", start)
			}
		}
		if m.quitIntent || m.inspector.active || m.structure.active || m.modal.kind != "" {
			t.Fatalf("%s: q did not just back out", start)
		}
		if string(m.buffers[0].editor.text) != sql || m.catalogFilter != "" {
			t.Fatalf("%s: repeated q typed into %s", start, m.place())
		}
	}
	m := placeFixture(t, placeResults)
	press(m, tea.KeyDown, tea.ModShift)
	press(m, 'q')
	if m.results().rowAnchor >= 0 || m.focus != resultsPane {
		t.Fatal("q in results did not clear the selection and stay")
	}
	press(m, 'q')
	if m.focus != resultsPane || !strings.Contains(m.status, "Ctrl+Q") {
		t.Fatal("q with nothing to clear left results or gave no hint")
	}
}

func TestTypingInTheDatabaseTreeFiltersAsYouType(t *testing.T) {
	m := placeFixture(t, placeDatabases)
	for _, r := range "slee" {
		press(m, r)
	}
	if _, node, ok := m.selectedCatalogNode(); !ok || node.name != "sleepover" || m.catalogFilter != "slee" || !m.databasePane.typing {
		t.Fatalf("typing did not filter to and select sleepover (filter %q)", m.catalogFilter)
	}
	if !strings.Contains(ansi.Strip(m.View().Content), "DATABASES · slee") {
		t.Fatal("the pane title did not show the filter")
	}
	m.Update(keyMsg("backspace"))
	if m.catalogFilter != "sle" {
		t.Fatal("Backspace did not edit the filter")
	}
	press(m, tea.KeyEnter)
	if m.databasePane.typing || m.catalogFilter != "sle" {
		t.Fatal("Enter did not keep the filter and stop typing")
	}
	press(m, 'd')
	if m.catalogFilter != "d" || !m.databasePane.typing {
		t.Fatal("a letter after Enter did not start a new filter")
	}
	if m.structure.active || len(m.buffers) != 1 {
		t.Fatal("a typed letter ran a row action")
	}
	press(m, tea.KeyEsc)
	if m.catalogFilter != "" || m.databasePane.typing || m.focus != catalogPane {
		t.Fatal("Esc did not clear the filter and stay in the tree")
	}
	press(m, '.')
	if m.modal.kind != "catalog-actions" {
		t.Fatal(". did not open row actions")
	}
}

func TestF5RunsAndHelpStartsWithWhatYouCanDoHere(t *testing.T) {
	m := placeFixture(t, placeSQL)
	if cmd := press(m, tea.KeyF5); cmd == nil {
		t.Fatal("F5 did not run SQL")
	}
	m = placeFixture(t, placeResults)
	press(m, '?')
	if m.modal.kind != "help" || !strings.HasPrefix(m.referenceText(), "WHAT YOU CAN DO HERE · RESULTS") || !strings.Contains(m.referenceText(), "ON PURPOSE, THESE DO NOTHING HERE") {
		t.Fatal("? in results did not open help about results")
	}
}

func runLine(t *testing.T, m *Model, sql string, cursorLine int) tea.Cmd {
	t.Helper()
	m.buffers[0].editor = newEditor(sql)
	lines := strings.Split(sql, "\n")
	cursor := 0
	for index := 0; index < cursorLine; index++ {
		cursor += len([]rune(lines[index])) + 1
	}
	m.buffers[0].editor.cursor = cursor
	m.focusPane(editorPane)
	_, cmd := m.Update(key('j', tea.ModCtrl))
	return cmd
}

func TestPsqlCommandsDoTheirScrunchEquivalents(t *testing.T) {
	m := New(Config{Demo: true})
	if runLine(t, m, `\dt`, 0); m.focus != catalogPane || anyRunning(m) {
		t.Fatal(`\dt did not show the database tree`)
	}
	if runLine(t, m, `\l`, 0); m.modal.kind != "connections" && m.modal.kind != "connection" {
		t.Fatalf(`\l opened %q`, m.modal.kind)
	}
	m.modal = modal{}
	if runLine(t, m, `\?`, 0); m.modal.kind != "help" {
		t.Fatal(`\? did not open help`)
	}
	m.modal = modal{}
	if runLine(t, m, `\pset border 2`, 0); !strings.Contains(m.status, "isn't available") || anyRunning(m) {
		t.Fatalf(`\pset was not explained: %q`, m.status)
	}
	finishCommands(t, m, runLine(t, m, "\\x\nSELECT 1;", 1))
	if len(m.history) == 0 || strings.Contains(m.history[len(m.history)-1].sql, `\x`) {
		t.Fatal(`a psql line above SQL was sent to the server`)
	}
	finishCommands(t, m, runLine(t, m, "SELECT '\n\\d inside a string\n';", 1))
	if len(m.history) < 2 || !strings.Contains(m.history[len(m.history)-1].sql, `\d inside`) {
		t.Fatal(`a backslash line inside a string literal was treated as psql`)
	}
	offline := New(Config{})
	if cmd := runLine(t, offline, "exit", 0); cmd == nil || offline.modal.kind != "" {
		t.Fatal("exit alone did not quit without asking to save it")
	}
	offline = New(Config{})
	if runLine(t, offline, "SELECT 1;\n\\q", 1); offline.modal.kind != "confirm-dirty" {
		t.Fatal(`\q under real SQL did not ask to save it`)
	}
	offline = New(Config{})
	if runLine(t, offline, `\d users`, 0); !strings.Contains(offline.status, "needs a database") {
		t.Fatalf(`\d without a connection said %q`, offline.status)
	}
}

func TestKeyLabelsReadTheWayPeopleSayThem(t *testing.T) {
	for key, want := range map[string]string{"ctrl+d": "Ctrl+D", "super+enter": "Cmd+Enter", "shift+space": "Shift+Space", "pgdown": "PgDn", "f5": "F5", "G": "G", "+": "+", "ctrl+/": "Ctrl+/", "up": "↑"} {
		if got := keyLabel(key); got != want {
			t.Errorf("keyLabel(%q) = %q, want %q", key, got, want)
		}
	}
}
