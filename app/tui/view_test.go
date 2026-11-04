package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/dustinevan/scrunchie/lib/data/connections"
	"github.com/dustinevan/scrunchie/pkg/result"
)

func TestViewsFitTerminalAcrossStatesAndSizes(t *testing.T) {
	for _, size := range [][2]int{{1, 1}, {4, 3}, {20, 8}, {64, 16}, {80, 24}, {120, 36}, {180, 50}} {
		for _, state := range []string{"blank", "blank-results", "editor", "results", "catalog", "inspector", "help", "palette", "confirm", "history", "message", "maximized"} {
			m := New(Config{Demo: true})
			if strings.HasPrefix(state, "blank") {
				// A disconnected workspace shows the mascot in empty results.
				m = New(Config{})
			}
			m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			switch state {
			case "blank-results":
				m.focus = resultsPane
			case "editor":
				m.buffers[0].editor.insert(strings.Repeat("雪🙂", 200), "")
			case "results":
				m.focus = resultsPane
			case "catalog":
				m.focus = catalogPane
			case "inspector":
				m.focus, m.results().column = resultsPane, 2
				m.openInspector()
			case "help":
				m.action("help")
			case "palette":
				m.openPrompt("palette", "Commands", "")
			case "confirm":
				m.modal = modal{kind: "confirm-dirty", title: "Unsaved SQL", message: strings.Repeat("a long filename ", 20), buttons: []string{"Cancel", "Save", "Discard"}}
			case "history":
				m.history = []historyEntry{{sql: "SELECT '雪';"}}
				m.action("history")
			case "message":
				m.modal = modal{kind: "message", title: "Details", message: strings.Repeat("a long detail line\n", 200)}
			case "maximized":
				m.maximized = true
			}
			m.prepareView()
			view := m.View().Content
			lines := strings.Split(view, "\n")
			if len(lines) != size[1] {
				t.Fatalf("%s %dx%d: got %d lines", state, size[0], size[1], len(lines))
			}
			for i, line := range lines {
				if width := ansi.StringWidth(line); width != size[0] {
					t.Fatalf("%s %dx%d line %d width=%d: %q", state, size[0], size[1], i, width, line)
				}
			}
			if again := m.View().Content; view != again {
				t.Fatal("View mutated render state")
			}
		}
	}
}

func TestUntrustedTextCannotEmitTerminalControlSequences(t *testing.T) {
	attack := "secret\x1b]52;c;INJECT\x07\x1b[2J\r\u202eevil"
	m := New(Config{Connection: attack, InitialSQL: "SELECT '" + attack + "';", InitialPath: attack})
	m.results().batch = result.Batch{Connected: true, TxStatus: 'I', Results: []result.Set{{Columns: []result.Column{{Name: attack, Type: attack, OID: 25}}, Rows: [][]result.Cell{{{Text: attack}}}, Command: attack}}}
	c := offlineConnection(m)
	c.name, c.description, c.profile = attack, attack, connections.Profile{ID: "attack", Name: attack, Database: attack}
	c.catalog = []catalogNode{{kind: "schema", name: attack, parent: -1}}
	m.status = attack
	for _, state := range []string{"editor", "grid", "inspector", "message", "prompt", "catalog"} {
		m.modal = modal{}
		m.inspector.active = false
		switch state {
		case "grid":
			m.focus = resultsPane
		case "inspector":
			m.openInspector()
		case "message":
			m.modal = modal{kind: "message", title: attack, message: attack}
		case "prompt":
			m.openPrompt("open", "Open", attack)
		case "catalog":
			m.focus = catalogPane
		}
		m.prepareView()
		view := m.View().Content
		if strings.Contains(view, "\x1b]52") || strings.Contains(view, "\x1b[2J") || strings.ContainsAny(view, "\x07\r\u202e") {
			t.Fatalf("%s emitted an untrusted terminal instruction", state)
		}
	}
}

func TestReplacingLongLineRestoresVisibleSQL(t *testing.T) {
	e := newEditor(strings.Repeat("x", 200))
	e.cursor = len(e.text)
	e.viewport(80, 10)
	if e.left == 0 {
		t.Fatal("long line did not scroll to cursor")
	}
	e.anchor = 0
	e.insert("SELECT pg_sleep(30);", "")
	e.viewport(80, 10)
	if e.left != 0 || !strings.Contains(ansi.Strip(e.render(85, 10, true, true)), "SELECT pg_sleep(30);") {
		t.Fatal("replacing long SQL left the shorter query offscreen")
	}
}
