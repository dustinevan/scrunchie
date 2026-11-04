package tui

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
)

// The keyboard is organized around what a person tries, not what they must
// learn. Each place lists what can be done there and every key someone
// arriving from vim, less, readline, a desktop editor, a spreadsheet, or a SQL
// GUI is likely to press for it. The rules:
//
//  1. A habit acts only when the tools that bind it agree on its meaning in
//     that place. j means down in every tool that binds it in a grid.
//  2. A key whose meaning is ambiguous or unavailable is inert: it changes
//     nothing and the status line names the likely intents and their keys.
//     Ctrl+D means half-page in vim, exit in psql, and delete in editors.
//  3. Repeating a guess is harmless. g jumps to the top, so gg lands there
//     too; q backs out one level and never quits.
//  4. Costly actions (run, quit, connect, disconnect, file writes) are never
//     reached by a habit that could mean something else.
//
// The same table drives dispatch, the "?" help, and the contract test, so what
// help says is what the keys do.

type place string

const (
	placeSQL       place = "SQL"
	placeResults   place = "results"
	placeDatabases place = "databases"
	placeFilter    place = "database filter"
	placeJSON      place = "JSON value"
	placeText      place = "value text"
	placeStructure place = "table structure"
	placeReader    place = "reader"
	placeConfirm   place = "confirmation"
)

// keyAction is one thing a person can do in a place. The place's handler
// understands keys directly; each habit translates to keys[0].
type keyAction struct {
	keys   []string
	habits []string
	does   string
	// label replaces the key list in help when the list is too long to read.
	label string
}

// inertKey is a key someone might reasonably press where its meaning is
// ambiguous or unavailable. It changes nothing and explains the choices.
type inertKey struct {
	keys []string
	hint string
}

type keyPlace struct {
	actions []keyAction
	inert   []inertKey
	// gated places accept only listed keys; anything else shows summary.
	// Text owners (SQL) are not gated: their printable keys are text.
	gated bool
	// typeToFilter makes letters, digits, and _ start the database filter.
	typeToFilter bool
	summary      string
}

// copyKey is the copy-only key. Habits translate to it rather than to Ctrl+C,
// which cancels a running query before it copies.
const copyKey = "super+c"

var (
	readOnly    = inertKey{keys: []string{"delete", "backspace", "i", "a", "o", "x", "d"}, hint: "This is a read-only snapshot · change data with SQL (F2)"}
	halfPage    = inertKey{keys: []string{"ctrl+d"}, hint: "Ctrl+D means different things in different tools · page down is PgDn · quit is Ctrl+Q"}
	commandLine = inertKey{keys: []string{":"}, hint: "There's no command line · Ctrl+P searches every command"}
	previous    = inertKey{keys: []string{"N", "shift+f3"}, hint: "There's no previous match · n or F3 finds the next one, wrapping around"}
)

// globalActions run in every place without an open dialog. They come first in
// Update, so places never redefine them.
var globalActions = []keyAction{
	{keys: []string{"ctrl+j", "ctrl+enter"}, habits: []string{"f5", "super+enter"}, label: "Ctrl+J F5 Ctrl+Enter Cmd+Enter", does: "Run the statement at the cursor, or the selected SQL"},
	{keys: []string{"ctrl+p"}, habits: []string{"ctrl+shift+p", "super+p", "shift+super+p"}, label: "Ctrl+P Cmd+P", does: "Search every command"},
	{keys: []string{"ctrl+b"}, does: "Databases: connect, add, edit"},
	{keys: []string{"f1", "f7"}, does: "Show what you can do here"},
	{keys: []string{"ctrl+f"}, habits: []string{"super+f"}, does: "Find or filter here"},
	{keys: []string{"f3"}, does: "Find the next match"},
	{keys: []string{"ctrl+c"}, does: "Cancel a running query; otherwise copy"},
	{keys: []string{copyKey}, does: "Copy (never cancels)"},
	{keys: []string{"f8"}, does: "Query history, this session only"},
	{keys: []string{"ctrl+tab", "ctrl+shift+tab"}, does: "Next or previous pane"},
	{keys: []string{"f2", "f6"}, does: "Go to SQL / databases"},
	{keys: []string{"f9", "f10"}, does: "Show or hide databases / maximize this pane"},
	{keys: []string{"ctrl+v"}, does: "Explain how to paste (your terminal pastes)"},
	{keys: []string{"ctrl+q"}, does: "Quit, checking unsaved SQL and open transactions"},
}

var places = map[place]keyPlace{
	placeSQL: {
		actions: []keyAction{
			{keys: []string{"ctrl+space", "alt+/"}, does: "Complete a name"},
			{keys: []string{"f8"}, habits: []string{"ctrl+r"}, does: "Search query history"},
			{keys: []string{"ctrl+s"}, habits: []string{"super+s"}, does: "Save"},
			{keys: []string{"ctrl+o"}, habits: []string{"super+o"}, does: "Open a SQL file"},
			{keys: []string{"ctrl+n"}, habits: []string{"super+n"}, does: "New SQL buffer"},
			{keys: []string{"ctrl+w"}, habits: []string{"super+w"}, does: "Close this buffer"},
			{keys: []string{"ctrl+z"}, habits: []string{"super+z"}, does: "Undo"},
			{keys: []string{"ctrl+y", "ctrl+shift+z"}, habits: []string{"shift+super+z"}, does: "Redo"},
			{keys: []string{"ctrl+a"}, habits: []string{"super+a"}, does: "Select all SQL"},
			{keys: []string{"ctrl+x"}, habits: []string{"super+x"}, does: "Cut"},
			{keys: []string{"ctrl+/", "ctrl+_"}, habits: []string{"super+/"}, does: "Comment or uncomment lines"},
			{keys: []string{"f4"}, does: "Format SQL"},
			{keys: []string{"ctrl+g"}, does: "Go to a line"},
			{keys: []string{"tab", "shift+tab"}, does: "Indent / dedent"},
			{keys: []string{"left"}, does: "At the very start, go to databases"},
			{keys: []string{"down"}, does: "At the very end, go to results"},
		},
		inert: []inertKey{
			halfPage,
			{keys: []string{"ctrl+e"}, hint: "Ctrl+E is end-of-line in some tools and run in others · End goes to the line end · Ctrl+J runs"},
			{keys: []string{"ctrl+l"}, hint: "Nothing to clear · the screen redraws itself"},
		},
	},
	placeResults: {
		gated: true,
		actions: []keyAction{
			{keys: []string{"up"}, habits: []string{"k"}, does: "Up · from the top row, back to SQL"},
			{keys: []string{"down"}, habits: []string{"j"}, does: "Down"},
			{keys: []string{"left"}, habits: []string{"h"}, does: "Left · from the first column, to databases"},
			{keys: []string{"right"}, habits: []string{"l"}, does: "Right"},
			{keys: []string{"ctrl+up"}, habits: []string{"g"}, does: "First row"},
			{keys: []string{"ctrl+down"}, habits: []string{"G"}, does: "Last row"},
			{keys: []string{"home", "ctrl+left"}, habits: []string{"0", "^"}, does: "First column"},
			{keys: []string{"end", "ctrl+right"}, habits: []string{"$"}, does: "Last column"},
			{keys: []string{"ctrl+home", "ctrl+end"}, does: "First / last cell"},
			{keys: []string{"pgup", "pgdown"}, does: "Page up / down"},
			{keys: []string{"shift+up", "shift+down", "shift+left", "shift+right", "shift+home", "shift+end", "shift+pgup", "shift+pgdown", "ctrl+shift+up", "ctrl+shift+down", "ctrl+shift+left", "ctrl+shift+right", "ctrl+shift+home", "ctrl+shift+end"}, label: "Shift+any move", does: "Select a rectangle"},
			{keys: []string{"shift+space"}, does: "Select this row"},
			{keys: []string{"ctrl+space", "ctrl+@"}, does: "Select this column"},
			{keys: []string{"ctrl+a"}, does: "Select every cell"},
			{keys: []string{"enter", "space"}, does: "Inspect the whole value (JSON opens as a tree)"},
			{keys: []string{"esc"}, does: "Clear the selection, then the filter, then back to SQL"},
			{keys: []string{"q"}, does: "Clear the selection, then the filter; never leaves results"},
			{keys: []string{"/"}, does: "Find in values"},
			{keys: []string{"f3"}, habits: []string{"n"}, does: "Next match"},
			{keys: []string{"f"}, does: "Filter rows by what they show · column:text for one column"},
			{keys: []string{"s"}, does: "Sort by this column: ascending, descending, original"},
			{keys: []string{"y"}, does: "Copy as TSV, CSV, Markdown, JSON, INSERT, or IN list"},
			{keys: []string{"v"}, does: "Change this column's display format"},
			{keys: []string{"m"}, does: "Fetch more rows (re-runs a paged query)"},
			{keys: []string{"[", "]", "ctrl+pgup", "ctrl+pgdown"}, does: "Previous / next statement result"},
			{keys: []string{"+", "=", "-"}, does: "Widen / narrow this column"},
			{keys: []string{"ctrl+e"}, does: "Export this view to .csv or .json"},
			{keys: []string{"tab", "shift+tab"}, does: "Next / previous pane"},
			{keys: []string{"f1"}, habits: []string{"?"}, does: "Show what you can do here"},
		},
		inert:   []inertKey{readOnly, halfPage, commandLine, previous},
		summary: "Enter inspects · Esc goes back to SQL · ? shows everything here",
	},
	placeDatabases: {
		gated:        true,
		typeToFilter: true,
		actions: []keyAction{
			{keys: []string{"up", "down"}, does: "Select"},
			{keys: []string{"pgup", "pgdown"}, does: "Page up / down"},
			{keys: []string{"home", "ctrl+home"}, does: "Add database (the top row)"},
			{keys: []string{"end", "ctrl+end"}, does: "Last row"},
			{keys: []string{"right"}, does: "Go to SQL, from any row"},
			{keys: []string{"left"}, does: "Collapse, then go to the parent"},
			{keys: []string{"enter", "space"}, does: "Open: connect, switch connection, or expand"},
			{keys: []string{"."}, does: "Actions for this row: SELECT, structure, DDL, count, insert name, edit, disconnect"},
			{keys: []string{"+"}, does: "Add a database"},
			{keys: []string{"/"}, does: "Filter by name (or just start typing)"},
			{keys: []string{"backspace"}, does: "Edit the filter"},
			{keys: []string{"esc"}, does: "Clear the filter, then back to SQL"},
			{keys: []string{"ctrl+r"}, does: "Refresh this connection's catalog"},
			{keys: []string{"tab", "shift+tab"}, does: "Next / previous pane"},
			{keys: []string{"f1"}, habits: []string{"?"}, does: "Show what you can do here"},
		},
		inert: []inertKey{
			{keys: []string{"delete"}, hint: "Nothing is deleted from here · . → Disconnect · Ctrl+B manages saved databases"},
			halfPage, commandLine,
		},
		summary: "Type a name to filter · Enter opens · . shows actions · ? everything here",
	},
	placeFilter: {
		gated:        true,
		typeToFilter: true,
		actions: []keyAction{
			{keys: []string{"up", "down", "pgup", "pgdown"}, does: "Select among the matches"},
			{keys: []string{"enter"}, does: "Open the selection; the filter stays"},
			{keys: []string{"backspace"}, does: "Delete a character"},
			{keys: []string{"ctrl+u"}, does: "Clear what you typed"},
			{keys: []string{"esc"}, does: "Clear the filter"},
			{keys: []string{"right"}, does: "Go to SQL; the filter stays"},
			{keys: []string{"left"}, does: "Stop typing, then collapse or go to the parent"},
			{keys: []string{"tab", "shift+tab"}, does: "Next / previous pane; the filter stays"},
		},
		inert: []inertKey{
			{keys: []string{"space"}, hint: "Names have no spaces · Enter opens the selection"},
		},
		summary: "Keep typing to narrow · Enter opens · Esc clears",
	},
	placeJSON: {
		gated: true,
		actions: []keyAction{
			{keys: []string{"up"}, habits: []string{"k"}, does: "Up"},
			{keys: []string{"down"}, habits: []string{"j"}, does: "Down"},
			{keys: []string{"left"}, habits: []string{"h"}, does: "Collapse, then go to the parent"},
			{keys: []string{"right"}, habits: []string{"l"}, does: "Expand, then go to the first child"},
			{keys: []string{"home", "ctrl+home"}, habits: []string{"g"}, does: "Top"},
			{keys: []string{"end", "ctrl+end"}, habits: []string{"G"}, does: "Bottom"},
			{keys: []string{"pgup", "pgdown"}, does: "Page up / down"},
			{keys: []string{"enter", "space"}, does: "Open or close this object or array"},
			{keys: []string{"e", "c"}, does: "Expand / collapse everything"},
			{keys: []string{copyKey}, habits: []string{"y"}, does: "Copy this value"},
			{keys: []string{"p"}, does: "Copy this value's path"},
			{keys: []string{"v"}, does: "Change the display format"},
			{keys: []string{"/"}, does: "Filter keys and paths"},
			{keys: []string{"f3"}, habits: []string{"n"}, does: "Next match"},
			{keys: []string{"alt+left", "alt+right"}, does: "Scroll sideways"},
			{keys: []string{"esc"}, habits: []string{"q"}, does: "Clear the filter, then back to results"},
			{keys: []string{"f1"}, habits: []string{"?"}, does: "Show what you can do here"},
		},
		inert:   []inertKey{readOnly, halfPage, commandLine, previous},
		summary: "← → open and close · Esc goes back · ? shows everything here",
	},
	placeText: {
		gated: true,
		actions: []keyAction{
			{keys: []string{"up"}, habits: []string{"k"}, does: "Up"},
			{keys: []string{"down"}, habits: []string{"j"}, does: "Down"},
			{keys: []string{"left"}, habits: []string{"h"}, does: "Scroll left"},
			{keys: []string{"right"}, habits: []string{"l"}, does: "Scroll right"},
			{keys: []string{"home", "ctrl+home"}, habits: []string{"g"}, does: "Top"},
			{keys: []string{"end", "ctrl+end"}, habits: []string{"G"}, does: "Bottom"},
			{keys: []string{"pgup", "pgdown"}, does: "Page up / down"},
			{keys: []string{copyKey}, habits: []string{"y"}, does: "Copy this value"},
			{keys: []string{"v"}, does: "Change the display format"},
			{keys: []string{"/"}, does: "Find text"},
			{keys: []string{"f3"}, habits: []string{"n"}, does: "Next match"},
			{keys: []string{"alt+left", "alt+right"}, does: "Scroll sideways"},
			{keys: []string{"esc"}, habits: []string{"q"}, does: "Back to results"},
			{keys: []string{"f1"}, habits: []string{"?"}, does: "Show what you can do here"},
		},
		inert:   []inertKey{readOnly, halfPage, commandLine, previous},
		summary: "↑↓ scroll · Esc goes back · ? shows everything here",
	},
	placeStructure: {
		gated: true,
		actions: []keyAction{
			{keys: []string{"up"}, habits: []string{"k"}, does: "Up"},
			{keys: []string{"down"}, habits: []string{"j"}, does: "Down"},
			{keys: []string{"left"}, habits: []string{"h"}, does: "Collapse the section, or go to its header"},
			{keys: []string{"right"}, habits: []string{"l"}, does: "Expand, then go to the first item"},
			{keys: []string{"home", "ctrl+home"}, habits: []string{"g"}, does: "Top"},
			{keys: []string{"end", "ctrl+end"}, habits: []string{"G"}, does: "Bottom"},
			{keys: []string{"pgup", "pgdown"}, does: "Page up / down"},
			{keys: []string{"enter", "space"}, does: "Open or close this section"},
			{keys: []string{"D"}, does: "Open the DDL in a new SQL buffer"},
			{keys: []string{copyKey}, habits: []string{"y"}, does: "Copy the line, or the whole section from its header"},
			{keys: []string{"/"}, does: "Find, including inside closed sections"},
			{keys: []string{"f3"}, habits: []string{"n"}, does: "Next match"},
			{keys: []string{"ctrl+r"}, does: "Reload"},
			{keys: []string{"alt+left", "alt+right"}, does: "Scroll sideways"},
			{keys: []string{"esc"}, habits: []string{"q"}, does: "Back to databases"},
			{keys: []string{"f1"}, habits: []string{"?"}, does: "Show what you can do here"},
		},
		inert: []inertKey{
			{keys: []string{"d"}, hint: "Structure is read-only · capital D opens its DDL"},
			{keys: []string{"delete", "backspace", "i", "a", "o", "x"}, hint: "Structure is read-only · change it with SQL (D opens the DDL)"},
			halfPage, commandLine, previous,
		},
		summary: "Enter opens a section · D shows DDL · Esc goes back · ? everything here",
	},
	placeReader: {
		gated: true,
		actions: []keyAction{
			{keys: []string{"up"}, habits: []string{"k"}, does: "Scroll up"},
			{keys: []string{"down"}, habits: []string{"j"}, does: "Scroll down"},
			{keys: []string{"pgup", "shift+space"}, habits: []string{"b"}, does: "Page up"},
			{keys: []string{"pgdown", "space"}, habits: []string{"f"}, does: "Page down"},
			{keys: []string{"home", "ctrl+home"}, habits: []string{"g"}, does: "Top"},
			{keys: []string{"end", "ctrl+end"}, habits: []string{"G"}, does: "Bottom"},
			{keys: []string{"esc", "enter", "f1", "f7"}, habits: []string{"q", "?"}, does: "Close"},
		},
		inert: []inertKey{
			{keys: []string{"/", "ctrl+f"}, hint: "There's no search here · PgDn pages through"},
			halfPage, commandLine,
		},
		summary: "↑↓ scroll · Space pages · Esc closes",
	},
	placeConfirm: {
		gated: true,
		actions: []keyAction{
			{keys: []string{"tab", "shift+tab", "left", "right", "up", "down", "home", "end"}, does: "Choose a button"},
			{keys: []string{"enter", "space"}, does: "Press the chosen button"},
			{keys: []string{"esc"}, does: "Cancel"},
		},
		inert: []inertKey{
			{keys: []string{"y", "n", "Y", "N"}, hint: "No one-letter answers here · choose with ← → or Tab, then Enter · Esc cancels"},
		},
		summary: "Choose with ← → or Tab, then Enter · Esc cancels",
	},
}

// place reports where a key press lands, or "" when a dialog that owns typed
// text (a prompt or the connection form) has it.
func (m *Model) place() place {
	switch {
	case m.modal.kind == "help" || m.modal.kind == "message":
		return placeReader
	case m.modal.kind == "connection" || m.modal.kind == "connections" || m.modal.kind == "database-names" || m.modal.kind == "confirm-remove-profile":
		return ""
	case m.modal.kind != "" && len(m.modal.buttons) > 0 && !m.modalAcceptsText():
		return placeConfirm
	case m.modal.kind != "":
		return ""
	case m.inspector.active && m.inspector.json:
		return placeJSON
	case m.inspector.active:
		return placeText
	case m.structure.active:
		return placeStructure
	case m.focus == catalogPane && m.databasePane.typing:
		return placeFilter
	case m.focus == catalogPane:
		return placeDatabases
	case m.focus == resultsPane:
		return placeResults
	}
	return placeSQL
}

// habit resolves a key pressed in p. It returns the key the handlers should
// see, and whether the key belongs to p at all. Inert keys belong to p and
// carry a hint. The place's own actions come first, then its inert keys,
// which may shadow a global meaning that does not apply there, then globals.
func habit(p place, key string) (canonical, hint string, known bool) {
	where, ok := places[p]
	if canonical, found := actionKey(where.actions, key); found {
		return canonical, "", true
	}
	for _, inert := range where.inert {
		for _, candidate := range inert.keys {
			if candidate == key {
				return "", inert.hint, true
			}
		}
	}
	if canonical, found := actionKey(globalActions, key); found {
		// A global habit takes on its key's meaning here, inert included.
		for _, inert := range where.inert {
			for _, candidate := range inert.keys {
				if candidate == canonical {
					return "", inert.hint, true
				}
			}
		}
		return canonical, "", true
	}
	if !ok || !where.gated {
		return key, "", true
	}
	return "", keyLabel(key) + " does nothing here · " + where.summary, false
}

// filterRune reports a key that types into the database filter.
func filterRune(key string) bool {
	r, size := utf8.DecodeRuneInString(key)
	return size == len(key) && size > 0 && (unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_')
}

var namedKeys = map[string]rune{
	"up": tea.KeyUp, "down": tea.KeyDown, "left": tea.KeyLeft, "right": tea.KeyRight,
	"home": tea.KeyHome, "end": tea.KeyEnd, "pgup": tea.KeyPgUp, "pgdown": tea.KeyPgDown,
	"esc": tea.KeyEscape, "enter": tea.KeyEnter, "space": tea.KeySpace, "tab": tea.KeyTab,
	"backspace": tea.KeyBackspace, "delete": tea.KeyDelete,
	"f1": tea.KeyF1, "f2": tea.KeyF2, "f3": tea.KeyF3, "f4": tea.KeyF4, "f5": tea.KeyF5, "f6": tea.KeyF6,
	"f7": tea.KeyF7, "f8": tea.KeyF8, "f9": tea.KeyF9, "f10": tea.KeyF10,
}

// keyMsg builds the key press whose String is key, so a translated habit
// reaches handlers exactly as if that key had been pressed.
func keyMsg(key string) tea.KeyPressMsg {
	var msg tea.KeyPressMsg
	for {
		prefix, rest, found := strings.Cut(key, "+")
		if !found || rest == "" {
			break
		}
		switch prefix {
		case "ctrl":
			msg.Mod |= tea.ModCtrl
		case "alt":
			msg.Mod |= tea.ModAlt
		case "shift":
			msg.Mod |= tea.ModShift
		case "super":
			msg.Mod |= tea.ModSuper
		default:
			panic(fmt.Sprintf("keyMsg: unknown modifier in %q", key))
		}
		key = rest
	}
	if code, ok := namedKeys[key]; ok {
		msg.Code = code
		if code == tea.KeySpace {
			msg.Text = " "
		}
		return msg
	}
	msg.Code, _ = utf8.DecodeRuneInString(key)
	if msg.Mod&^tea.ModShift == 0 {
		msg.Text = key
	}
	return msg
}

var keyNames = map[string]string{
	"up": "↑", "down": "↓", "left": "←", "right": "→", "pgup": "PgUp", "pgdown": "PgDn",
	"esc": "Esc", "enter": "Enter", "space": "Space", "tab": "Tab", "backspace": "Backspace",
	"delete": "Delete", "home": "Home", "end": "End",
	"ctrl": "Ctrl", "alt": "Alt", "shift": "Shift", "super": "Cmd",
}

// keyLabel writes a key the way people say it: ctrl+d is Ctrl+D.
func keyLabel(key string) string {
	if key == "+" {
		return key
	}
	parts := strings.Split(key, "+")
	if strings.HasSuffix(key, "++") {
		parts = append(strings.Split(strings.TrimSuffix(key, "++"), "+"), "+")
	}
	for index, part := range parts {
		if name, ok := keyNames[part]; ok {
			parts[index] = name
		} else if strings.HasPrefix(part, "f") && len(part) > 1 && unicode.IsDigit(rune(part[1])) {
			parts[index] = strings.ToUpper(part)
		} else if len(parts) > 1 {
			parts[index] = strings.ToUpper(part)
		}
	}
	return strings.Join(parts, "+")
}

// hereHelp lists what can be done in p, every key that does each thing, and
// the keys that do nothing there on purpose.
func hereHelp(p place) string {
	var out strings.Builder
	where := places[p]
	title := strings.ToUpper(string(p))
	if p == placeReader {
		title = "THIS VIEW"
	}
	fmt.Fprintf(&out, "WHAT YOU CAN DO HERE · %s\n", title)
	if where.typeToFilter {
		fmt.Fprintf(&out, "%-24s %s\n", "a–z 0–9 _", "Type a name to filter")
	}
	for _, action := range where.actions {
		fmt.Fprintf(&out, "%-24s %s\n", helpKeys(action), action.does)
	}
	if len(where.inert) > 0 {
		out.WriteString("\nON PURPOSE, THESE DO NOTHING HERE\n")
		for _, inert := range where.inert {
			fmt.Fprintf(&out, "%-24s %s\n", helpKeys(keyAction{keys: inert.keys}), inert.hint)
		}
	}
	if p != placeReader && p != placeConfirm {
		out.WriteString("\nEVERYWHERE\n")
		for _, action := range globalActions {
			if action.keys[0] == copyKey {
				continue // the same Ctrl+C, listed once
			}
			fmt.Fprintf(&out, "%-24s %s\n", helpKeys(action), action.does)
		}
	}
	return out.String()
}

// helpKeys names every key that performs action, or its short label. The
// internal copy-only key is shown as the Ctrl+C people press.
func helpKeys(action keyAction) string {
	if action.label != "" {
		return action.label
	}
	var labels []string
	for _, key := range append(append([]string{}, action.keys...), action.habits...) {
		if key == copyKey {
			key = "ctrl+c"
		}
		labels = append(labels, keyLabel(key))
	}
	return strings.Join(labels, " ")
}

// openHelp opens the reference with what can be done where help was asked
// for at the top.
func (m *Model) openHelp() {
	here := m.place()
	if here == "" || here == placeReader || here == placeConfirm {
		here = placeSQL
	}
	m.modal = modal{kind: "help", title: "Keyboard reference", message: hereHelp(here) + "\nFULL REFERENCE\n" + helpText}
}

// referenceText is the open help's text: what can be done where it was
// opened, then the full reference.
func (m *Model) referenceText() string {
	if m.modal.message != "" {
		return m.modal.message
	}
	return helpText
}

// actionKey finds key among actions: itself when an action takes it
// directly, or the action's first key when key is one of its habits.
func actionKey(actions []keyAction, key string) (string, bool) {
	for _, action := range actions {
		for _, candidate := range action.keys {
			if candidate == key {
				return key, true
			}
		}
		for _, candidate := range action.habits {
			if candidate == key {
				return action.keys[0], true
			}
		}
	}
	return "", false
}
