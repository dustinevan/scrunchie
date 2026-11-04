package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/dustinevan/scrunchie/pkg/sqltext"
	"github.com/dustinevan/scrunchie/pkg/termtext"
)

// PostgreSQL users type psql's backslash commands out of habit. Run treats a
// psql command line as a request for scrunch's equivalent and never sends it
// to the server, where it could only be a syntax error.

// isPsqlLine reports a line psql would treat as a command: a backslash
// command, or exit or quit alone on the line.
func isPsqlLine(line string) bool {
	line = strings.TrimSpace(line)
	if strings.HasPrefix(line, `\`) {
		return true
	}
	word := strings.ToLower(strings.TrimSuffix(line, ";"))
	return word == "exit" || word == "quit"
}

// psqlFromEditor handles Run when the cursor's line, or the whole selection,
// is a psql command. The line must begin its statement, so a backslash inside
// a multi-line string literal is never mistaken for one.
func (m *Model) psqlFromEditor(escapes bool) (tea.Cmd, bool) {
	e := m.buffers[m.bufferIndex].editor
	start, end := e.selection()
	if start != end {
		text := strings.TrimSpace(string(e.text[start:end]))
		if isPsqlLine(text) && !strings.Contains(text, "\n") {
			return m.psqlCommand(text), true
		}
		return nil, false
	}
	lineStart, lineEnd := e.cursor, e.cursor
	for lineStart > 0 && e.text[lineStart-1] != '\n' {
		lineStart--
	}
	for lineEnd < len(e.text) && e.text[lineEnd] != '\n' {
		lineEnd++
	}
	line := string(e.text[lineStart:lineEnd])
	if !isPsqlLine(line) {
		return nil, false
	}
	// The statement holding the cursor must begin, after any comments, with a
	// psql command line.
	source := string(e.text)
	statements := sqltext.SplitMode(source, escapes)
	if index := sqltext.At(statements, len(string(e.text[:e.cursor]))); index >= 0 {
		body := strings.TrimSpace(source[statements[index].Body:statements[index].End])
		if first, _, _ := strings.Cut(body, "\n"); !isPsqlLine(first) {
			return nil, false
		}
	}
	return m.psqlCommand(strings.TrimSpace(line)), true
}

// psqlCommand performs scrunch's equivalent of one psql command line.
func (m *Model) psqlCommand(line string) tea.Cmd {
	fields := strings.Fields(line)
	command := fields[0]
	argument := ""
	if len(fields) > 1 {
		argument = fields[1]
	}
	shown := termtext.Safe(command)
	m.statusError = false
	switch strings.TrimSuffix(strings.ToLower(strings.TrimSuffix(command, ";")), "+") {
	case `\q`, "exit", "quit":
		// A buffer holding only the quit command has nothing worth saving.
		if b := &m.buffers[m.bufferIndex]; strings.TrimSpace(string(b.editor.text)) == line {
			b.saved = string(b.editor.text)
		}
		return m.beginQuit()
	case `\?`, `\h`, `\help`:
		m.openHelp()
		m.status = shown + " · here's what you can do · F1 or ? opens this anytime"
	case `\l`, `\list`:
		m.openDatabases()
		m.status = shown + " · your databases · Enter opens one as its own connection"
	case `\c`, `\connect`:
		m.openDatabases()
		m.status = shown + " · choose a database · each one opens as its own connection"
	case `\d`:
		if argument != "" {
			return m.psqlDescribe(shown, argument)
		}
		m.psqlBrowse(shown, "")
	case `\dt`, `\dv`, `\dm`, `\de`, `\di`, `\ds`, `\df`, `\dn`, `\dp`, `\db`:
		m.psqlBrowse(shown, argument)
	case `\x`:
		m.status = shown + " · there's no expanded mode · Enter on a cell inspects the whole value"
	case `\timing`:
		m.status = shown + " · run time is always shown in the status line"
	case `\s`:
		m.openPrompt("history", "Query history · every connection, this session only", "")
	case `\i`, `\ir`:
		m.status = shown + " · Ctrl+O opens a SQL file in a new buffer · Ctrl+J runs it"
	case `\e`, `\ef`, `\ev`:
		m.status = shown + " · you're already in the editor"
	case `\copy`:
		m.status = shown + " · COPY isn't supported · in results, Ctrl+E exports CSV or JSON"
	case `\g`, `\gx`:
		m.status = shown + " · Ctrl+J runs the statement at the cursor"
	default:
		m.status = "psql's " + shown + " isn't available in scrunch · Ctrl+P searches every command"
	}
	return nil
}

// psqlBrowse answers the \d family by showing the database tree, filtered to
// the pattern when there is one.
func (m *Model) psqlBrowse(shown, pattern string) {
	m.focusPane(catalogPane)
	m.focusActiveDatabase()
	name := psqlName(pattern)
	if index := strings.LastIndex(name, "."); index >= 0 {
		name = name[index+1:]
	}
	name = strings.Trim(name, "*")
	if name != "" {
		m.findNext(name)
	}
	m.status, m.statusError = shown+" · everything is in the database tree · type a name to filter it", false
}

// psqlDescribe answers \d name with the table's structure when the name is in
// the loaded catalog, and otherwise filters the tree to it.
func (m *Model) psqlDescribe(shown, argument string) tea.Cmd {
	c := m.conn()
	if c == nil {
		m.status, m.statusError = shown+" needs a database · Ctrl+B adds one", false
		return nil
	}
	name := psqlName(argument)
	schema := ""
	if index := strings.LastIndex(name, "."); index >= 0 {
		schema, name = name[:index], name[index+1:]
	}
	for _, node := range c.catalog {
		if node.kind == "relation" && node.relation.Name == name && (schema == "" || node.relation.Schema == schema) {
			return m.openStructure(c, node)
		}
	}
	m.psqlBrowse(shown, argument)
	m.status = shown + " " + termtext.Safe(argument) + " · not loaded yet · open its schema in the tree, then . → Structure"
	return nil
}

// psqlName applies psql's identifier rules: unquoted names fold to lower
// case, and double-quoted names keep their case.
func psqlName(pattern string) string {
	if strings.Contains(pattern, `"`) {
		return strings.ReplaceAll(pattern, `"`, "")
	}
	return strings.ToLower(pattern)
}

// firstLine returns text's first nonblank line, trimmed.
func firstLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) != "" {
			return strings.TrimSpace(line)
		}
	}
	return ""
}
