package tui

import (
	"path/filepath"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/dustinevan/scrunchie/pkg/termtext"
)

// addBuffer opens SQL in a new current buffer bound to connection conn, or to
// none when conn is 0. SQL that scrunch prepares (previews, history,
// transactions, EXPLAIN, DDL) passes clean: only the user's later edits make
// it dirty, so closing or quitting does not ask to save it.
func (m *Model) addBuffer(sql, path string, clean bool, conn uint64) {
	if m.connectionFor(conn) == nil {
		conn = 0
	}
	m.nextBuffer++
	b := buffer{id: m.nextBuffer, path: path, editor: newEditor(sql), conn: conn, results: newResults()}
	b.dirty = !clean && sql != ""
	if clean {
		b.saved = string(b.editor.text)
	}
	m.buffers = append(m.buffers, b)
	m.focus = editorPane
	m.inspector.active = false
	m.structure.active = false
	m.switchBuffer(len(m.buffers) - 1)
}

func (m *Model) editorKey(msg tea.KeyPressMsg) tea.Cmd {
	key := msg.String()
	if len(m.completion.items) > 0 {
		switch key {
		case "esc":
			m.completion = completion{}
			m.completeAfterCatalog = false
			return nil
		case "up":
			m.completion.index = max(0, m.completion.index-1)
			return nil
		case "down":
			m.completion.index = min(len(m.completion.items)-1, m.completion.index+1)
			return nil
		case "pgup", "pgdown":
			rows := completionHeight(max(1, m.dimensions().editorHeight-5))
			if key == "pgup" {
				m.completion.index = max(0, m.completion.index-rows)
			} else {
				m.completion.index = min(len(m.completion.items)-1, m.completion.index+rows)
			}
			return nil
		case "shift+tab":
			m.completion.index = (m.completion.index + len(m.completion.items) - 1) % len(m.completion.items)
			return nil
		case "enter", "tab":
			// Enter after a fully typed word that opened its own popup ends
			// the line instead of re-inserting the same word.
			if key == "tab" || !m.completion.auto || !m.completionTyped() {
				m.acceptCompletion()
				return nil
			}
			m.completion = completion{}
			m.completeAfterCatalog = false
		}
	}
	switch key {
	case "ctrl+n":
		m.addBuffer("", "", true, m.buffers[m.bufferIndex].conn)
		return nil
	case "ctrl+o":
		if m.fileBusy {
			m.status = "A file operation is already in progress"
			return nil
		}
		m.openPrompt("open", "Open SQL file", "")
		return nil
	case "ctrl+s":
		return m.saveBuffer(m.buffers[m.bufferIndex].id, false)
	case "ctrl+w":
		return m.closeBuffer()
	case "ctrl+pgup":
		m.switchBuffer((m.bufferIndex + len(m.buffers) - 1) % len(m.buffers))
		return nil
	case "ctrl+pgdown":
		m.switchBuffer((m.bufferIndex + 1) % len(m.buffers))
		return nil
	case "ctrl+x":
		return m.copySelection(true)
	case "f4":
		return m.action("format")
	case "ctrl+g":
		return m.action("goto")
	case "ctrl+/", "ctrl+_":
		m.buffers[m.bufferIndex].editor.toggleComments()
		return nil
	case "shift+delete":
		m.buffers[m.bufferIndex].editor.deleteLine()
		return nil
	case "ctrl+space", "ctrl+@", "alt+/":
		return m.completeWord(true)
	case "esc":
		m.completion = completion{}
		m.completeAfterCatalog = false
		m.buffers[m.bufferIndex].editor.anchor = -1
		return nil
	}
	e := &m.buffers[m.bufferIndex].editor
	start, end := e.selection()
	if start == end {
		if key == "left" && e.cursor == 0 {
			m.focusPane(catalogPane)
			m.focusActiveDatabase()
			return nil
		}
		if key == "down" && e.cursor == len(e.text) {
			m.focusPane(resultsPane)
			return nil
		}
	}
	before, open := e.revision, len(m.completion.items) > 0
	e.key(msg, max(1, m.dimensions().editorHeight-5))
	typed := []rune(msg.Text)
	if before != e.revision && len(typed) == 1 && (unicode.IsLetter(typed[0]) || typed[0] == '_' || typed[0] == '.' || open && (unicode.IsDigit(typed[0]) || typed[0] == '$')) {
		return m.completeWord(false)
	}
	if before != e.revision && open && (key == "backspace" || key == "ctrl+h") {
		return m.completeWord(false)
	}
	m.completion = completion{}
	m.completeAfterCatalog = false
	return nil
}

func (m *Model) closeBuffer() tea.Cmd {
	m.completion = completion{}
	m.completeAfterCatalog = false
	b := m.buffers[m.bufferIndex]
	if c := m.bufferQuery(b.id); c != nil {
		// Its results would have nowhere to go; the query must end first.
		m.status, m.statusError = bufferName(b)+" is running a query on "+termtext.Safe(c.name)+" · Ctrl+C cancels it before closing", false
		return nil
	}
	if string(b.editor.text) != b.saved {
		m.closeIntent, m.quitIntent = true, false
		m.confirmBuffer = b.id
		name := filepath.Base(b.path)
		if b.path == "" {
			name = "untitled SQL"
		}
		m.modal = modal{kind: "confirm-dirty", title: "Unsaved SQL", message: "Save changes to " + name + " before closing?", buttons: []string{"Cancel", "Save", "Discard"}}
		return nil
	}
	m.removeBuffer(m.bufferIndex)
	return nil
}

// removeBuffer drops buffers[index] and its results.
func (m *Model) removeBuffer(index int) {
	closed := m.buffers[index]
	m.buffers = append(m.buffers[:index], m.buffers[index+1:]...)
	if len(m.buffers) == 0 {
		m.addBuffer("", "", true, closed.conn)
		return
	}
	m.switchBuffer(min(m.bufferIndex, len(m.buffers)-1))
}

func (m *Model) continueQuit() tea.Cmd {
	if m.profileBusy {
		m.status = "Waiting for database settings to finish saving…"
		return nil
	}
	if m.discovering {
		m.status = "Canceling database lookup before quitting…"
		if m.discoveryCancel != nil {
			m.discoveryCancel()
		}
		return nil
	}
	if m.connecting {
		m.modal = modal{}
		m.status = "Canceling connection before quitting…"
		if m.connectCancel != nil {
			m.connectCancel()
		}
		return nil
	}
	for _, b := range m.buffers {
		if string(b.editor.text) != b.saved && !m.quitDiscard[b.id] {
			m.confirmBuffer = b.id
			name := filepath.Base(b.path)
			if b.path == "" {
				name = "untitled SQL"
			}
			m.modal = modal{kind: "confirm-dirty", title: "Unsaved SQL", message: "Save " + name + " before quitting?", buttons: []string{"Cancel", "Save", "Discard"}}
			return nil
		}
	}
	if !m.quitConfirmed && len(m.quitRisks()) > 0 {
		m.confirmQuit()
		return nil
	}
	// After confirmation, wait for canceled queries to return so their
	// sessions are idle when they close.
	running := 0
	for _, c := range m.connections {
		if !c.query.running {
			continue
		}
		running++
		if c.query.queued {
			c.query = queryState{}
			running--
		} else if c.query.cancel != nil && !c.query.canceling {
			c.query.cancel()
			c.query.canceling = true
		}
	}
	if running > 0 {
		m.status = "Canceling running queries before quitting…"
		return nil
	}
	m.Cancel()
	return tea.Quit
}

// confirmQuit lists, in one dialog, every connection whose running query or
// transaction quitting would discard. It refreshes as those queries finish.
func (m *Model) confirmQuit() {
	risks := m.quitRisks()
	message := "Quitting closes these connections:\n" + strings.Join(risks, "\n")
	if len(risks) == 0 {
		message = "Nothing is running and no transaction is open now. Continue quitting?"
	}
	index := 0
	if m.modal.kind == "confirm-quit" {
		index = m.modal.index
	}
	m.modal = modal{kind: "confirm-quit", title: "Close connections and quit?", message: message, buttons: []string{"Stay", "Close and quit"}, index: index}
}
