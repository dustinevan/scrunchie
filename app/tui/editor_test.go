package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func key(code rune, mods ...tea.KeyMod) tea.KeyPressMsg {
	msg := tea.KeyPressMsg{Code: code}
	if code >= ' ' && code < 0x110000 {
		msg.Text = string(code)
	}
	for _, mod := range mods {
		msg.Mod |= mod
	}
	if msg.Mod.Contains(tea.ModCtrl) || msg.Mod.Contains(tea.ModAlt) {
		msg.Text = ""
	}
	return msg
}

func press(m *Model, code rune, mods ...tea.KeyMod) tea.Cmd {
	_, cmd := m.Update(key(code, mods...))
	return cmd
}

func TestEditorUnicodeSelectionUndoAndRedo(t *testing.T) {
	e := newEditor("SELECT '雪';")
	e.cursor = 9
	e.move("left", true, 10)
	start, end := e.selection()
	if string(e.text[start:end]) != "雪" {
		t.Fatalf("selection = %q", string(e.text[start:end]))
	}
	e.insert("🙂", "")
	if string(e.text) != "SELECT '🙂';" {
		t.Fatalf("insert = %q", string(e.text))
	}
	e.key(key('z', tea.ModCtrl), 10)
	if string(e.text) != "SELECT '雪';" {
		t.Fatalf("undo = %q", string(e.text))
	}
	e.key(key('y', tea.ModCtrl), 10)
	if string(e.text) != "SELECT '🙂';" {
		t.Fatalf("redo = %q", string(e.text))
	}
}

func TestEditorMultilineIndentAndCommentsPreserveSelection(t *testing.T) {
	original := "  SELECT '雪';\n  SELECT 2;\nSELECT 3;"
	e := newEditor(original)
	e.anchor, e.cursor = 0, len([]rune("  SELECT '雪';\n  SELECT 2;\n"))
	e.indent(false)
	if got := string(e.text); got != "      SELECT '雪';\n      SELECT 2;\nSELECT 3;" {
		t.Fatalf("indent = %q", got)
	}
	e.indent(true)
	if string(e.text) != original {
		t.Fatalf("dedent = %q", string(e.text))
	}
	e.toggleComments()
	if got := string(e.text); got != "  -- SELECT '雪';\n  -- SELECT 2;\nSELECT 3;" {
		t.Fatalf("comments = %q", got)
	}
	e.toggleComments()
	if string(e.text) != original {
		t.Fatalf("uncomment = %q", string(e.text))
	}
}

func TestEditorPasteAndLineNavigation(t *testing.T) {
	e := newEditor("")
	e.insert("one\r\ntwo\r雪\x00\x1b\x07", "")
	if string(e.text) != "one\ntwo\n雪" {
		t.Fatalf("paste = %q", string(e.text))
	}
	if err := e.gotoLine("3"); err != nil || e.cursor != 8 {
		t.Fatalf("goto line = %d, %v", e.cursor, err)
	}
	if err := e.gotoLine("4"); err == nil || e.cursor != 8 {
		t.Fatal("out-of-range goto changed cursor")
	}
	e.deleteLine()
	if string(e.text) != "one\ntwo" {
		t.Fatalf("delete final line = %q", string(e.text))
	}
	e = newEditor("SELECT(a.b)")
	e.cursor = 10
	e.move("ctrl+left", false, 1)
	if e.cursor != 9 {
		t.Fatalf("word left at punctuation stopped at %d", e.cursor)
	}
	e.move("ctrl+left", false, 1)
	if e.cursor != 8 {
		t.Fatalf("word left did not cross period: %d", e.cursor)
	}
	line := newEditor("")
	line.singleLine = true
	line.insert("a\nb\tc", "")
	if string(line.text) != "a b c" {
		t.Fatalf("single-line paste = %q", string(line.text))
	}
}

func TestFormatSelectionAndUndo(t *testing.T) {
	original := "select a,b from t where x=1;\nSELECT 9;"
	e := newEditor(original)
	e.anchor, e.cursor = 0, strings.Index(original, "\n")
	if err := e.formatSQL(); err != nil {
		t.Fatal(err)
	}
	if string(e.text) == original || !strings.HasSuffix(string(e.text), "\nSELECT 9;") {
		t.Fatalf("format changed outside selection: %q", string(e.text))
	}
	start, end := e.selection()
	if !strings.Contains(string(e.text[start:end]), "\n") {
		t.Fatal("formatted selection was not retained")
	}
	e.key(key('z', tea.ModCtrl), 10)
	if string(e.text) != original {
		t.Fatalf("format undo = %q", string(e.text))
	}
}

func TestEditorMovementAndEditsStayWithinText(t *testing.T) {
	e := newEditor("雪\n\nword (a.b)\nlast")
	inputs := []tea.KeyPressMsg{
		key(tea.KeyLeft), key(tea.KeyUp), key(tea.KeyRight, tea.ModShift),
		key(tea.KeyDown, tea.ModShift), key(tea.KeyEnd), key(tea.KeyHome, tea.ModCtrl),
		key(tea.KeyPgDown), key(tea.KeyPgUp), key(tea.KeyLeft, tea.ModCtrl),
		key(tea.KeyRight, tea.ModCtrl), key(tea.KeyBackspace), key(tea.KeyDelete),
		key(tea.KeyTab), key(tea.KeyTab, tea.ModShift), key('z', tea.ModCtrl),
		key('y', tea.ModCtrl), key('k', tea.ModCtrl), key('u', tea.ModCtrl),
		key(tea.KeyEnter), key('雪'), key('a', tea.ModCtrl),
	}
	for step := 0; step < 600; step++ {
		e.key(inputs[(step*17+step/7)%len(inputs)], 3)
		if e.cursor < 0 || e.cursor > len(e.text) || e.anchor < -1 || e.anchor > len(e.text) {
			t.Fatalf("step %d: cursor=%d anchor=%d length=%d", step, e.cursor, e.anchor, len(e.text))
		}
		e.viewport(12, 4)
		_ = e.render(12, 4, true, true)
	}
}
