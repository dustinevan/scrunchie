package tui

import (
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
)

type editSnapshot struct {
	text   string
	cursor int
	anchor int
}

type editor struct {
	text            []rune
	cursor          int
	anchor          int
	top             int
	left            int
	undo            []editSnapshot
	redo            []editSnapshot
	lastEdit        time.Time
	lastKind        string
	singleLine      bool
	lines           []int
	revision        uint64
	preferredColumn int
	verticalMotion  bool
	scrolling       bool
}

func newEditor(text string) editor {
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	return editor{text: []rune(text), anchor: -1}
}

func (e editor) selection() (int, int) {
	if e.anchor < 0 || e.anchor == e.cursor {
		return e.cursor, e.cursor
	}
	return min(e.anchor, e.cursor), max(e.anchor, e.cursor)
}

func (e *editor) remember(kind string) {
	e.lines = nil
	e.revision++
	e.verticalMotion = false
	if kind != e.lastKind || time.Since(e.lastEdit) > 700*time.Millisecond || kind == "" {
		e.undo = append(e.undo, editSnapshot{string(e.text), e.cursor, e.anchor})
		bytes := 0
		first := len(e.undo) - 1
		for first >= 0 {
			bytes += len(e.undo[first].text)
			if bytes > 32<<20 || len(e.undo)-first > 100 {
				break
			}
			first--
		}
		if first >= 0 {
			e.undo = append([]editSnapshot(nil), e.undo[first+1:]...)
		}
	}
	e.redo = nil
	e.lastKind = kind
	e.lastEdit = time.Now()
}

func (e *editor) insert(text, kind string) {
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	var clean strings.Builder
	for _, r := range text {
		if r == '\n' || r == '\t' {
			if e.singleLine {
				clean.WriteByte(' ')
			} else {
				clean.WriteRune(r)
			}
		} else if !unicode.IsControl(r) {
			clean.WriteRune(r)
		}
	}
	if clean.Len() == 0 {
		return
	}
	e.remember(kind)
	start, end := e.selection()
	added := []rune(clean.String())
	next := make([]rune, 0, len(e.text)-(end-start)+len(added))
	next = append(next, e.text[:start]...)
	next = append(next, added...)
	next = append(next, e.text[end:]...)
	e.text = next
	e.cursor = start + len(added)
	e.anchor = -1
}

func (e *editor) delete(start, end int) {
	if start == end {
		return
	}
	e.remember("delete")
	e.text = append(e.text[:start], e.text[end:]...)
	e.cursor = start
	e.anchor = -1
}

func (e editor) lineBounds() (int, int) {
	start, end := e.cursor, e.cursor
	for start > 0 && e.text[start-1] != '\n' {
		start--
	}
	for end < len(e.text) && e.text[end] != '\n' {
		end++
	}
	return start, end
}

func (e *editor) move(key string, shift bool, page int) {
	if key != "up" && key != "down" && key != "pgup" && key != "pgdown" {
		e.verticalMotion = false
	}
	e.lastKind = ""
	if shift && e.anchor < 0 {
		e.anchor = e.cursor
	}
	if !shift {
		start, end := e.selection()
		if start != end && (key == "left" || key == "right") {
			if key == "left" {
				e.cursor = start
			} else {
				e.cursor = end
			}
			e.anchor = -1
			return
		}
		e.anchor = -1
	}
	start, end := e.lineBounds()
	switch key {
	case "left":
		e.cursor = max(0, e.cursor-1)
	case "right":
		e.cursor = min(len(e.text), e.cursor+1)
	case "home":
		e.cursor = start
	case "end":
		e.cursor = end
	case "ctrl+home":
		e.cursor = 0
	case "ctrl+end":
		e.cursor = len(e.text)
	case "ctrl+left", "alt+left", "alt+b":
		old := e.cursor
		for e.cursor > 0 && unicode.IsSpace(e.text[e.cursor-1]) {
			e.cursor--
		}
		for e.cursor > 0 && (unicode.IsLetter(e.text[e.cursor-1]) || unicode.IsDigit(e.text[e.cursor-1]) || e.text[e.cursor-1] == '_') {
			e.cursor--
		}
		if e.cursor == old && e.cursor > 0 {
			e.cursor--
		}
	case "ctrl+right", "alt+right", "alt+f":
		old := e.cursor
		for e.cursor < len(e.text) && (unicode.IsLetter(e.text[e.cursor]) || unicode.IsDigit(e.text[e.cursor]) || e.text[e.cursor] == '_') {
			e.cursor++
		}
		for e.cursor < len(e.text) && unicode.IsSpace(e.text[e.cursor]) {
			e.cursor++
		}
		if e.cursor == old && e.cursor < len(e.text) {
			e.cursor++
		}
	case "up", "down", "pgup", "pgdown":
		column := e.cursor - start
		if e.verticalMotion {
			column = e.preferredColumn
		} else {
			e.preferredColumn = column
			e.verticalMotion = true
		}
		count := 1
		if key == "pgup" || key == "pgdown" {
			count = max(1, page)
		}
		for i := 0; i < count; i++ {
			start, end = e.lineBounds()
			if key == "up" || key == "pgup" {
				if start == 0 {
					e.cursor = min(column, end)
					break
				}
				previous := start - 1
				for previous > 0 && e.text[previous-1] != '\n' {
					previous--
				}
				e.cursor = min(previous+column, start-1)
			} else {
				if end == len(e.text) {
					break
				}
				nextEnd := end + 1
				for nextEnd < len(e.text) && e.text[nextEnd] != '\n' {
					nextEnd++
				}
				e.cursor = min(end+1+column, nextEnd)
			}
		}
	}
}

func (e *editor) indent(backward bool) {
	start, end := e.selection()
	if !backward && start == end {
		line, _ := e.lineBounds()
		e.insert(strings.Repeat(" ", 4-(e.cursor-line)%4), "")
		return
	}
	e.remember("")
	for start > 0 && e.text[start-1] != '\n' {
		start--
	}
	if end > start && end > 0 && e.text[end-1] == '\n' {
		end--
	}
	for end < len(e.text) && e.text[end] != '\n' {
		end++
	}
	lines := strings.Split(string(e.text[start:end]), "\n")
	oldCursor, oldAnchor := e.cursor, e.anchor
	position, delta := start, 0
	for i, line := range lines {
		change := 4
		if backward {
			remove := 0
			for remove < len(line) && remove < 4 && line[remove] == ' ' {
				remove++
			}
			if strings.HasPrefix(line, "\t") {
				remove = 1
			}
			lines[i] = line[remove:]
			change = -remove
		} else {
			lines[i] = "    " + line
		}
		if oldCursor >= position {
			e.cursor += max(change, -max(0, oldCursor-position))
		}
		if oldAnchor >= position {
			e.anchor += max(change, -max(0, oldAnchor-position))
		}
		position += len([]rune(line)) + 1
		delta += change
	}
	replacement := []rune(strings.Join(lines, "\n"))
	next := make([]rune, 0, max(0, len(e.text)+delta))
	next = append(next, e.text[:start]...)
	next = append(next, replacement...)
	next = append(next, e.text[end:]...)
	e.text = next
	e.cursor = min(max(0, e.cursor), len(e.text))
	if e.anchor >= 0 {
		e.anchor = min(e.anchor, len(e.text))
	}
}

func (e *editor) key(msg tea.KeyPressMsg, page int) bool {
	e.scrolling = false
	key := msg.String()
	shift := strings.Contains(key, "shift+")
	moveKey := strings.ReplaceAll(key, "shift+", "")
	switch moveKey {
	case "left", "right", "up", "down", "home", "end", "pgup", "pgdown", "ctrl+home", "ctrl+end", "ctrl+left", "ctrl+right", "alt+left", "alt+right", "alt+b", "alt+f":
		e.move(moveKey, shift, page)
		return true
	}
	start, end := e.selection()
	switch key {
	case "ctrl+a":
		e.anchor = 0
		e.cursor = len(e.text)
		e.verticalMotion = false
	case "backspace", "ctrl+h":
		if start == end {
			start = max(0, start-1)
		}
		e.delete(start, end)
	case "delete", "ctrl+d":
		if start == end {
			end = min(len(e.text), end+1)
		}
		e.delete(start, end)
	case "ctrl+backspace", "alt+backspace":
		if start == end {
			for start > 0 && unicode.IsSpace(e.text[start-1]) {
				start--
			}
			for start > 0 && !unicode.IsSpace(e.text[start-1]) {
				start--
			}
		}
		e.delete(start, end)
	case "ctrl+k":
		_, end = e.lineBounds()
		if end == e.cursor && end < len(e.text) {
			end++
		}
		e.delete(e.cursor, end)
	case "ctrl+u":
		start, _ = e.lineBounds()
		e.delete(start, e.cursor)
	case "tab":
		if !e.singleLine {
			e.indent(false)
		}
	case "shift+tab":
		if !e.singleLine {
			e.indent(true)
		}
	case "enter":
		if !e.singleLine {
			e.insert("\n", "")
		}
	case "ctrl+z":
		if len(e.undo) > 0 {
			e.redo = append(e.redo, editSnapshot{string(e.text), e.cursor, e.anchor})
			previous := e.undo[len(e.undo)-1]
			e.undo = e.undo[:len(e.undo)-1]
			e.text, e.cursor, e.anchor = []rune(previous.text), previous.cursor, previous.anchor
			e.lines = nil
			e.revision++
			e.lastKind = ""
			e.verticalMotion = false
		}
	case "ctrl+y", "ctrl+shift+z":
		if len(e.redo) > 0 {
			e.undo = append(e.undo, editSnapshot{string(e.text), e.cursor, e.anchor})
			next := e.redo[len(e.redo)-1]
			e.redo = e.redo[:len(e.redo)-1]
			e.text, e.cursor, e.anchor = []rune(next.text), next.cursor, next.anchor
			e.lines = nil
			e.revision++
			e.lastKind = ""
			e.verticalMotion = false
		}
	default:
		if msg.Text == "" || msg.Mod.Contains(tea.ModCtrl) || msg.Mod.Contains(tea.ModAlt) || msg.Mod.Contains(tea.ModSuper) {
			return false
		}
		e.insert(msg.Text, "typing")
	}
	return true
}
