package tui

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"github.com/dustinevan/scrunchie/lib/logic/sqledit"
)

func (e *editor) formatSQL() error {
	start, end := e.selection()
	selected := start != end
	if !selected {
		start, end = 0, len(e.text)
	}
	formatted, err := sqledit.Format(string(e.text[start:end]))
	if err != nil {
		return fmt.Errorf("formatting SQL: %w", err)
	}
	if formatted == string(e.text[start:end]) {
		return nil
	}
	e.remember("")
	replacement := []rune(formatted)
	// Formatting changes whitespace; keep the cursor beside the same token.
	characters := 0
	for i := start; i < min(e.cursor, end); i++ {
		if !unicode.IsSpace(e.text[i]) {
			characters++
		}
	}
	position := 0
	for position < len(replacement) && characters > 0 {
		if !unicode.IsSpace(replacement[position]) {
			characters--
		}
		position++
	}
	next := make([]rune, 0, len(e.text)-(end-start)+len(replacement))
	next = append(next, e.text[:start]...)
	next = append(next, replacement...)
	next = append(next, e.text[end:]...)
	e.text = next
	e.cursor = start + position
	e.anchor = -1
	if selected {
		e.anchor = start
		e.cursor = start + len(replacement)
	}
	return nil
}

func (e *editor) toggleComments() {
	start, end := e.selection()
	for start > 0 && e.text[start-1] != '\n' {
		start--
	}
	if end > start && e.text[end-1] == '\n' {
		end--
	}
	for end < len(e.text) && e.text[end] != '\n' {
		end++
	}
	lines := strings.Split(string(e.text[start:end]), "\n")
	uncomment := true
	for _, line := range lines {
		trimmed := strings.TrimLeft(line, " \t")
		if trimmed != "" && !strings.HasPrefix(trimmed, "--") {
			uncomment = false
			break
		}
	}
	e.remember("")
	oldCursor, oldAnchor := e.cursor, e.anchor
	position := start
	for i, line := range lines {
		trimmed := strings.TrimLeft(line, " \t")
		indent := len(line) - len(trimmed)
		prefix := position + len([]rune(line[:indent]))
		change := 3
		if uncomment {
			if !strings.HasPrefix(trimmed, "--") {
				position += len([]rune(line)) + 1
				continue
			}
			remove := 2
			if strings.HasPrefix(trimmed, "-- ") {
				remove = 3
			}
			lines[i] = line[:indent] + trimmed[remove:]
			change = -remove
		} else {
			lines[i] = line[:indent] + "-- " + trimmed
		}
		if oldCursor >= prefix {
			e.cursor += max(change, -(oldCursor - prefix))
		}
		if oldAnchor >= prefix {
			e.anchor += max(change, -(oldAnchor - prefix))
		}
		position += len([]rune(line)) + 1
	}
	replacement := []rune(strings.Join(lines, "\n"))
	next := make([]rune, 0, len(e.text)+len(replacement)-(end-start))
	next = append(next, e.text[:start]...)
	next = append(next, replacement...)
	next = append(next, e.text[end:]...)
	e.text = next
	e.cursor = max(0, min(e.cursor, len(e.text)))
	if e.anchor >= 0 {
		e.anchor = min(e.anchor, len(e.text))
	}
}

func (e *editor) deleteLine() {
	start, end := e.lineBounds()
	if end < len(e.text) {
		end++
	} else if start > 0 {
		start--
	}
	e.delete(start, end)
}

func (e *editor) gotoLine(value string) error {
	line, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || line < 1 {
		return fmt.Errorf("going to line: enter a positive line number")
	}
	current, position := 1, 0
	for position < len(e.text) && current < line {
		if e.text[position] == '\n' {
			current++
		}
		position++
	}
	if current < line {
		return fmt.Errorf("going to line: the buffer has %d lines", current)
	}
	e.cursor = position
	e.anchor = -1
	e.lastKind = ""
	e.verticalMotion = false
	return nil
}
