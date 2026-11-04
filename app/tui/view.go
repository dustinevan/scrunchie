package tui

import (
	"fmt"
	"image/color"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/dustinevan/scrunchie/lib/logic/cell"
	"github.com/dustinevan/scrunchie/lib/logic/sqledit"
	"github.com/dustinevan/scrunchie/pkg/termtext"
)

// The palette is mall-rat neon: hot pink, grape, turquoise, mint, and lemon on
// a plum night. Colors degrade through the terminal's color profile; reverse
// video and underline keep the cursor and selection visible without color.
// A solid pink header is reserved for WRITES ON, so pink elsewhere is only
// ever text, borders, or a single cell.
var (
	hotPink   = lipgloss.Color("#ff3ea5")
	bubblegum = lipgloss.Color("#ff71ce")
	grape     = lipgloss.Color("#b967ff")
	turquoise = lipgloss.Color("#01cdfe")
	mint      = lipgloss.Color("#05ffa1")
	lemon     = lipgloss.Color("#ffe45e")
	lavender  = lipgloss.Color("#a08cc8")
	plum      = lipgloss.Color("#4b3470")
	velvet    = lipgloss.Color("#5b2386")

	accentStyle   = lipgloss.NewStyle().Foreground(turquoise).Bold(true)
	titleStyle    = lipgloss.NewStyle().Foreground(hotPink).Bold(true)
	mutedStyle    = lipgloss.NewStyle().Foreground(lavender)
	sparkleStyle  = lipgloss.NewStyle().Foreground(hotPink)
	warningStyle  = lipgloss.NewStyle().Foreground(lemon)
	errorStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#ff5f5f")).Bold(true)
	selectedStyle = lipgloss.NewStyle().Background(velvet).Foreground(lipgloss.Color("#fff4fb")).Underline(true)
	cursorStyle   = lipgloss.NewStyle().Foreground(hotPink).Reverse(true).Bold(true)
	writesStyle   = lipgloss.NewStyle().Background(lipgloss.Color("#ff2e88")).Foreground(lipgloss.Color("#000000")).Bold(true)

	keywordStyle = lipgloss.NewStyle().Foreground(bubblegum).Bold(true)
	stringStyle  = lipgloss.NewStyle().Foreground(mint)
	numberStyle  = lipgloss.NewStyle().Foreground(lemon)

	// focusBlend wraps around a focused panel's border; the ends match so the
	// top-left corner has no seam.
	focusBlend = []color.Color{hotPink, grape, turquoise, grape, hotPink}
	logo       = rainbow("scrunchie")
)

// rainbow colors each rune of text along the pink → grape → turquoise → mint →
// lemon blend, like a scrunchie of 80s ribbon.
func rainbow(text string) string {
	runes := []rune(text)
	colors := lipgloss.Blend1D(len(runes), hotPink, grape, turquoise, mint, lemon)
	var output strings.Builder
	for index, r := range runes {
		output.WriteString(lipgloss.NewStyle().Foreground(colors[index]).Bold(true).Render(string(r)))
	}
	return output.String()
}

// mascot is the scrunchie, face-on: a puffy ruffled ring with a face in the
// hole. Every rune is one cell wide.
var mascot = []string{
	"     ◠◠◠◠◠◠◠◠◠     ",
	"   ◠◠◠       ◠◠◠   ",
	"  ((   ◕   ◕   ))  ",
	"  ((     ◡     ))  ",
	"   ◡◡◡       ◡◡◡   ",
	"     ◡◡◡◡◡◡◡◡◡     ",
}

// mascotView draws the mascot with its ring shading from pink at the top to
// turquoise at the bottom, a bright face, and a pink smile.
func mascotView() string {
	shades := lipgloss.Blend1D(len(mascot), hotPink, grape, turquoise)
	eyes := lipgloss.NewStyle().Foreground(lipgloss.Color("#fff4fb")).Bold(true)
	var lines []string
	for row, text := range mascot {
		ring := lipgloss.NewStyle().Foreground(shades[row]).Bold(true)
		var line strings.Builder
		for _, r := range text {
			switch r {
			case ' ':
				line.WriteRune(r)
			case '◕':
				line.WriteString(eyes.Render(string(r)))
			case '◡':
				if row == 3 {
					line.WriteString(titleStyle.Render(string(r)))
				} else {
					line.WriteString(ring.Render(string(r)))
				}
			default:
				line.WriteString(ring.Render(string(r)))
			}
		}
		lines = append(lines, line.String())
	}
	return strings.Join(lines, "\n")
}

// footerLine renders a key-hint line with pink separators between hints.
func footerLine(text string) string {
	parts := strings.Split(text, " · ")
	for index := range parts {
		parts[index] = mutedStyle.Render(parts[index])
	}
	return strings.Join(parts, sparkleStyle.Render(" · "))
}

type layout struct {
	bodyHeight   int
	catalogWidth int
	rightWidth   int
	editorHeight int
	resultHeight int
	single       bool
}

func (m *Model) dimensions() layout {
	l := layout{bodyHeight: max(1, m.height-3), rightWidth: m.width}
	l.single = m.maximized || m.width < 68 || m.height < 18
	if l.single {
		l.editorHeight, l.resultHeight = l.bodyHeight, l.bodyHeight
		return l
	}
	if m.showCatalog {
		l.catalogWidth = min(34, max(22, m.width/4))
		l.rightWidth -= l.catalogWidth
	}
	l.editorHeight = max(7, l.bodyHeight*45/100)
	l.resultHeight = l.bodyHeight - l.editorHeight
	return l
}

// prepareView changes scroll offsets only during model updates.
func (m *Model) prepareView() {
	r := m.results()
	if len(m.buffers) == 0 {
		return
	}
	l := m.dimensions()
	e := &m.buffers[m.bufferIndex].editor
	if m.buffers[m.bufferIndex].checkedRevision != e.revision {
		m.buffers[m.bufferIndex].dirty = string(e.text) != m.buffers[m.bufferIndex].saved
		m.buffers[m.bufferIndex].checkedRevision = e.revision
	}
	// Any edit, including Shift+Delete, comments, formatting, and undo,
	// invalidates the replacement range computed for an open completion.
	if len(m.completion.items) > 0 && (m.completion.buffer != m.buffers[m.bufferIndex].id || m.completion.revision != e.revision) {
		m.completion = completion{}
	}
	editorRows := max(1, l.editorHeight-5)
	if len(m.completion.items) > 0 && m.focus == editorPane {
		editorRows = max(1, editorRows-completionHeight(editorRows))
	}
	e.viewport(max(1, l.rightWidth-9), editorRows)
	m.prepareDatabasePane(max(1, l.bodyHeight-3))
	if m.modal.kind == "connection" {
		m.prepareConnectionForm()
	}
	if m.modal.kind == "history" && (!m.modal.historyReady || m.modal.historyQuery != string(m.modal.input.text) || m.modal.historySerial != m.historySerial) {
		query := strings.ToLower(string(m.modal.input.text))
		m.modal.historyRows = nil
		for index := len(m.history) - 1; index >= 0; index-- {
			if strings.Contains(strings.ToLower(m.history[index].sql+"\x00"+m.history[index].connName), query) {
				m.modal.historyRows = append(m.modal.historyRows, index)
			}
		}
		m.modal.historyReady = true
		m.modal.historyQuery = string(m.modal.input.text)
		m.modal.historySerial = m.historySerial
	}
	if m.modalAcceptsText() {
		boxWidth := max(1, min(96, m.width-4))
		contentWidth := max(1, boxWidth-2)
		if boxWidth < 4 || min(l.bodyHeight, 24) < 4 {
			contentWidth = boxWidth
		}
		m.modal.input.viewport(contentWidth, 1)
	}
	m.syncRowView()
	if len(r.widths) != len(r.batch.Results) {
		r.widths = make([][]int, len(r.batch.Results))
		for setIndex, set := range r.batch.Results {
			rows := []int{0, 1, 2}[:min(3, len(set.Rows))]
			if setIndex == r.setIndex {
				rows = m.gridRows()
			}
			r.widths[setIndex] = make([]int, len(set.Columns))
			for column := range set.Columns {
				r.widths[setIndex][column] = columnWidth(set, column, rows)
			}
		}
	}
	if r.setIndex >= 0 && r.setIndex < len(r.batch.Results) {
		set := r.batch.Results[r.setIndex]
		count := len(m.gridRows())
		r.row = max(0, min(r.row, count-1))
		r.column = max(0, min(r.column, len(set.Columns)-1))
		rows := max(1, l.resultHeight-6)
		if m.gridScrolling {
			r.gridTop = max(0, min(r.gridTop, count-rows))
			r.gridLeft = max(0, min(r.gridLeft, len(set.Columns)-1))
		} else {
			r.gridTop = max(0, min(r.gridTop, r.row))
			if r.row >= r.gridTop+rows {
				r.gridTop = r.row - rows + 1
			}
			r.gridLeft = max(0, min(r.gridLeft, r.column))
		}
		available := max(1, l.rightWidth-8)
		for !m.gridScrolling && r.gridLeft < r.column {
			width := 0
			for col := r.gridLeft; col <= r.column && col < len(r.widths[r.setIndex]); col++ {
				width += r.widths[r.setIndex][col] + 1
			}
			if width <= available {
				break
			}
			r.gridLeft++
		}
	}
	if m.structure.active {
		m.prepareStructure()
	}
	if m.inspector.active {
		rows := max(1, m.height-9)
		count := len(m.inspector.lines)
		if m.inspector.json {
			count = len(m.inspector.visible)
		}
		if m.inspector.scrolling {
			m.inspector.top = max(0, min(m.inspector.top, count-rows))
		} else {
			m.inspector.top = max(0, min(m.inspector.top, m.inspector.index))
			if m.inspector.index >= m.inspector.top+rows {
				m.inspector.top = m.inspector.index - rows + 1
			}
		}
		// Horizontal scrolling stops once the longest visible line's end shows.
		longest := 0
		for position := m.inspector.top; position < min(count, m.inspector.top+rows); position++ {
			if m.inspector.json {
				longest = max(longest, lipgloss.Width(m.inspector.tree.Display(m.inspector.visible[position])))
			} else {
				longest = max(longest, lipgloss.Width(m.inspector.lines[position]))
			}
		}
		m.inspector.left = max(0, min(m.inspector.left, longest-max(1, m.width-4)))
	}
}

func (e *editor) viewport(width, height int) {
	if e.lines == nil {
		e.lines = []int{0}
		for i, r := range e.text {
			if r == '\n' {
				e.lines = append(e.lines, i+1)
			}
		}
	}
	e.cursor = max(0, min(e.cursor, len(e.text)))
	if e.scrolling {
		e.top = max(0, min(e.top, len(e.lines)-height))
		e.left = max(0, e.left)
		return
	}
	row := max(0, sort.SearchInts(e.lines, e.cursor+1)-1)
	e.top = max(0, min(e.top, row))
	if row >= e.top+height {
		e.top = row - height + 1
	}
	column := e.cursor - e.lines[row]
	lineEnd := len(e.text)
	if row+1 < len(e.lines) {
		lineEnd = e.lines[row+1] - 1
	}
	// Bring shorter edited lines back into view instead of leaving the cursor
	// at the left edge of an otherwise empty viewport.
	e.left = max(0, min(e.left, column, lineEnd-e.lines[row]-width+1))
	if column >= e.left+width {
		e.left = column - width + 1
	}
	visibleWidth := 0
	for _, char := range e.text[e.lines[row]+e.left : e.cursor] {
		visibleWidth += editorRuneWidth(char)
	}
	for e.left < column && visibleWidth >= width {
		visibleWidth -= editorRuneWidth(e.text[e.lines[row]+e.left])
		e.left++
	}
}

func fitLine(text string, width int) string {
	width = max(0, width)
	text = ansi.Truncate(text, width, "")
	return text + strings.Repeat(" ", max(0, width-lipgloss.Width(text)))
}

func fitBlock(text string, width, height int) string {
	if height <= 0 || width <= 0 {
		return ""
	}
	lines := strings.Split(text, "\n")
	var output strings.Builder
	for i := 0; i < height; i++ {
		if i > 0 {
			output.WriteByte('\n')
		}
		line := ""
		if i < len(lines) {
			line = lines[i]
		}
		output.WriteString(fitLine(line, width))
	}
	return output.String()
}

func panel(title, content string, width, height int, focused bool) string {
	if width < 4 || height < 4 {
		return fitBlock(content, width, height)
	}
	style := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(plum)
	if focused {
		style = style.BorderForegroundBlend(focusBlend...)
		title = titleStyle.Render("✦ " + title)
	} else {
		title = mutedStyle.Render("  " + title)
	}
	body := fitLine(title, width-2) + "\n" + fitBlock(content, width-2, height-3)
	return style.Render(body)
}

// View returns a full-screen view without changing application state.
// View returns a full-screen view without changing application state. The
// header describes the current buffer's connection: its write mode, its
// transaction, and the query it runs.
func (m *Model) View() tea.View {
	c := m.conn()
	connection := m.connectionSummary()
	if m.config.Demo {
		connection = "DEMO · local sample data"
	}
	mode := ""
	if c != nil {
		mode = "read-only  "
	}
	header := sparkleStyle.Render(" ✦ ") + logo + sparkleStyle.Render(" ✦") + mutedStyle.Render("  PostgreSQL  "+mode) + connection
	tx := ""
	if c != nil {
		switch c.txStatus {
		case 'T':
			tx = warningStyle.Render("IN TRANSACTION")
		case 'E':
			tx = errorStyle.Render("FAILED TRANSACTION · ROLLBACK")
		}
		if !c.connected && !m.config.Demo {
			tx = errorStyle.Render("CONNECTION LOST · Ctrl+P → Reconnect")
		}
	}
	// A query on another connection keeps a spinner in view.
	elsewhere := ""
	for _, open := range m.connections {
		if open != c && open.query.running && elsewhere == "" {
			elsewhere = termtext.Safe(open.name)
		}
	}
	switch {
	case c != nil && c.query.running:
		tx = m.spinner.View() + " " + time.Since(c.query.started).Round(time.Second).String() + " · Ctrl+C cancel"
	case m.connecting:
		tx = m.spinner.View() + " connecting"
	case m.discovering:
		tx = m.spinner.View() + " listing databases"
	case elsewhere != "" && tx == "":
		tx = m.spinner.View() + " running on " + elsewhere
	case elsewhere != "":
		tx += "  " + m.spinner.View() + " " + elsewhere
	}
	if tx != "" && m.width > 35 {
		header = fitLine(ansi.Truncate(header, max(1, m.width-lipgloss.Width(tx)-3), "…"), max(1, m.width-lipgloss.Width(tx)-2)) + tx
	}
	if c != nil && c.writable && c.session != nil && !m.config.Demo {
		// One style over plain text: a nested style would end the pink early.
		// The warning comes first so a narrow terminal never cuts it off.
		bar := " WRITES ON  scrunchie  " + ansi.Strip(connection)
		if tx != "" && m.width > 35 {
			bar = fitLine(bar, max(1, m.width-lipgloss.Width(tx)-3)) + " " + ansi.Strip(tx)
		}
		header = writesStyle.Render(fitLine(bar, m.width))
	}
	l := m.dimensions()
	var body string
	if m.modal.kind == "connection" || m.modal.kind == "connections" || m.modal.kind == "database-names" {
		body = m.connectionsView(m.width, l.bodyHeight)
	} else if m.modal.kind != "" {
		body = m.modalView(m.width, l.bodyHeight)
	} else if m.inspector.active {
		body = m.inspectorView(m.width, l.bodyHeight)
	} else if m.structure.active {
		body = m.structurePanel(m.width, l.bodyHeight)
	} else if l.single {
		switch m.focus {
		case editorPane:
			body = panel("SQL", m.editorView(m.width-2, l.bodyHeight-3), m.width, l.bodyHeight, true)
		case resultsPane:
			body = panel("RESULTS", m.gridView(m.width-2, l.bodyHeight-3), m.width, l.bodyHeight, true)
		case catalogPane:
			body = panel(m.databaseTitle(), m.databasePaneView(m.width-2, l.bodyHeight-3), m.width, l.bodyHeight, true)
		}
	} else {
		editor := panel("SQL", m.editorView(l.rightWidth-2, l.editorHeight-3), l.rightWidth, l.editorHeight, m.focus == editorPane)
		grid := panel("RESULTS", m.gridView(l.rightWidth-2, l.resultHeight-3), l.rightWidth, l.resultHeight, m.focus == resultsPane)
		body = lipgloss.JoinVertical(lipgloss.Left, editor, grid)
		if l.catalogWidth > 0 {
			catalog := panel(m.databaseTitle(), m.databasePaneView(l.catalogWidth-2, l.bodyHeight-3), l.catalogWidth, l.bodyHeight, m.focus == catalogPane)
			body = lipgloss.JoinHorizontal(lipgloss.Top, catalog, body)
		}
	}
	status := "  " + termtext.Safe(m.status)
	if m.statusError {
		status = "! " + termtext.Safe(m.status)
	}
	// A long status ends in an ellipsis; Read last status shows all of it.
	status = ansi.Truncate(status, m.width, "…")
	if m.statusError {
		status = errorStyle.Render(status)
	} else {
		status = mutedStyle.Render(status)
	}
	footer := " Arrows move · ← at start: databases · ↓ at end: results · Ctrl+J run · Ctrl+P commands · F1 help"
	if m.modal.kind == "connection" {
		footer = " Tab next  Shift+Tab back  Enter next/activate  Space checkbox  Esc cancel"
	} else if m.modal.kind == "connections" {
		footer = " ↑↓ select  Enter connect  n add  e edit  d remove entry  Esc back"
	} else if m.modal.kind == "database-names" {
		footer = " ↑↓ select  Enter fill Database field  Esc back to connection form"
	} else if m.modal.kind != "" {
		footer = " Enter accept  Esc back  ↑↓ select  Tab next  Ctrl+Q quit"
	} else if m.inspector.active {
		footer = " ↑↓ move · ←→ close/open · Enter toggle · v format · Ctrl+C copy · Esc back · ? help"
		if !m.inspector.json {
			footer = " ↑↓ lines · ←→ scroll · v format · Ctrl+C copy · Esc results · ? help"
		} else if m.inspector.filter != "" {
			footer = " ↑↓ keys · ← parent · →/Enter child · n next match · Esc clear filter"
		}
	} else if m.structure.active {
		footer = " ↑↓ lines · Enter toggle section · D DDL · Ctrl+C copy · Esc back · ? help"
	} else if m.focus == resultsPane {
		footer = " Arrows move · Enter inspect · s sort · f filter · y copy as · Esc SQL · ? help"
		if m.results().view.filter != "" {
			footer = " Esc clears filter · f edit filter · s sort · y copy as · Enter inspect · ? help"
		}
	} else if m.focus == catalogPane {
		footer = " Type to filter · ↑↓ select · Enter open · . actions · + add · → SQL · ? help"
		if m.databasePane.typing {
			footer = " Keep typing to narrow · ↑↓ choose · Enter open · Esc clear · → SQL"
		}
	}
	content := fitLine(header, m.width) + "\n" + fitBlock(body, m.width, l.bodyHeight) + "\n" + fitLine(status, m.width) + "\n" + fitLine(footerLine(footer), m.width)
	view := tea.NewView(fitBlock(content, m.width, m.height))
	view.AltScreen = true
	view.MouseMode = tea.MouseModeCellMotion
	return view
}

func (m *Model) editorView(width, height int) string {
	if width < 1 || height < 1 {
		return ""
	}
	var tabs []string
	for index := max(0, m.bufferIndex-1); index < min(len(m.buffers), m.bufferIndex+4); index++ {
		name := m.bufferTab(m.buffers[index])
		if index == m.bufferIndex {
			name = selectedStyle.Render(name)
		} else {
			name = mutedStyle.Render(name)
		}
		tabs = append(tabs, name)
	}
	e := m.buffers[m.bufferIndex].editor
	rows := max(1, height-2)
	completionRows := 0
	if len(m.completion.items) > 0 && m.focus == editorPane {
		completionRows = completionHeight(rows)
		rows = max(1, rows-completionRows)
	}
	body := strings.Join(tabs, " ") + "\n" + e.render(width, rows, m.focus == editorPane, true)
	if completionRows > 0 {
		start := max(0, m.completion.index-completionRows+1)
		end := min(len(m.completion.items), start+completionRows)
		labelWidth := 0
		for index := start; index < end; index++ {
			labelWidth = max(labelWidth, lipgloss.Width(termtext.Safe(m.completion.items[index])))
		}
		labelWidth = min(labelWidth, max(8, width/2))
		for index := start; index < end; index++ {
			label := fitLine(termtext.Safe(m.completion.items[index]), labelWidth)
			detail := ""
			if index < len(m.completion.details) {
				detail = termtext.Safe(m.completion.details[index])
			}
			item := "  " + label + "  " + mutedStyle.Render(detail)
			if index == m.completion.index {
				item = selectedStyle.Render(fitLine("› "+label+"  "+detail+"  Tab/Enter", width))
			}
			body += "\n" + item
		}
	}
	row := max(0, sort.SearchInts(e.lines, e.cursor+1)-1)
	column := e.cursor + 1
	if row < len(e.lines) {
		column -= e.lines[row]
	}
	body += "\n" + mutedStyle.Render(fmt.Sprintf(" Ln %d, Col %d  ·  Tab indent  ·  Ctrl+Space complete", row+1, column))
	return fitBlock(body, width, height)
}

func (e editor) render(width, height int, focused, numbers bool) string {
	if len(e.lines) == 0 {
		return ""
	}
	selectionStart, selectionEnd := e.selection()
	var output strings.Builder
	for row := e.top; row < e.top+height; row++ {
		if row > e.top {
			output.WriteByte('\n')
		}
		if row >= len(e.lines) {
			output.WriteString(mutedStyle.Render("  ~"))
			continue
		}
		gutter := ""
		if numbers {
			gutter = mutedStyle.Render(fmt.Sprintf("%4d ", row+1))
		}
		available := max(1, width-lipgloss.Width(gutter))
		start := e.lines[row]
		end := len(e.text)
		if row+1 < len(e.lines) {
			end = e.lines[row+1] - 1
		}
		start = min(end, start+e.left)
		end = min(end, start+available*2)
		tokens := sqledit.Tokenize(string(e.text[start:end]))
		var line strings.Builder
		position := start
		for _, token := range tokens {
			style := lipgloss.NewStyle()
			switch token.Kind {
			case sqledit.Keyword:
				style = keywordStyle
			case sqledit.String:
				style = stringStyle
			case sqledit.Comment:
				style = mutedStyle.Italic(true)
			case sqledit.Number:
				style = numberStyle
			}
			for _, r := range token.Text {
				character := termtext.Safe(string(r))
				if r == '\t' {
					character = "    "
				}
				current := style
				if position >= selectionStart && position < selectionEnd {
					current = selectedStyle
				}
				if focused && position == e.cursor {
					current = cursorStyle
				}
				line.WriteString(current.Render(character))
				position++
			}
		}
		if focused && e.cursor == end {
			line.WriteString(cursorStyle.Render(" "))
		}
		output.WriteString(gutter + ansi.Truncate(line.String(), available, ""))
	}
	return output.String()
}

func (m *Model) gridView(width, height int) string {
	r := m.results()
	if width < 1 || height < 1 {
		return ""
	}
	if len(r.batch.Results) == 0 || r.setIndex >= len(r.batch.Results) {
		if r.err != "" {
			return m.resultErrorView(width, height)
		}
		// The mascot waits beside the hints when there is room for both.
		if width >= 46 && height >= 7 {
			hints := "\n" + titleStyle.Render("Run SQL with Ctrl+J.") + "\n" + mutedStyle.Render("Results appear here.") + "\n\n" + mutedStyle.Render("Enter inspects a cell.") + "\n" + mutedStyle.Render("v changes its format.")
			art := lipgloss.NewStyle().Padding(1, 3, 0, 2).Render(mascotView())
			return lipgloss.JoinHorizontal(lipgloss.Top, art, hints)
		}
		return mutedStyle.Render("\n  Run SQL with Ctrl+J. Results appear here.\n\n  Enter inspects a cell · v changes its display format.")
	}
	if r.setIndex >= len(r.widths) || len(r.widths[r.setIndex]) != len(r.batch.Results[r.setIndex].Columns) {
		// prepareView sizes columns during Update; nothing to draw before it.
		return ""
	}
	set := r.batch.Results[r.setIndex]
	var tabs []string
	for index := max(0, r.setIndex-1); index < min(len(r.batch.Results), r.setIndex+5); index++ {
		label := fmt.Sprintf(" %d %s ", index+1, termtext.Safe(r.batch.Results[index].Command))
		if index == r.setIndex {
			label = selectedStyle.Render(label)
		} else {
			label = mutedStyle.Render(label)
		}
		tabs = append(tabs, label)
	}
	lines := []string{strings.Join(tabs, " ")}
	if note := pageNote(set, r.origin.page, m.rowBudget()); note != "" {
		lines[0] = ansi.Truncate(lines[0]+"  "+warningStyle.Render(note), width, "…")
	}
	// Results outlive a rebind or disconnect; say where they came from.
	if r.conn != 0 && r.conn != m.buffers[m.bufferIndex].conn {
		origin := "from " + termtext.Safe(r.connName)
		if m.connectionFor(r.conn) == nil {
			origin += " · connection closed"
		}
		lines[0] = ansi.Truncate(lines[0]+"  "+warningStyle.Render(origin), width, "…")
	}
	if len(set.Columns) == 0 {
		lines = append(lines, "", accentStyle.Render("  "+termtext.Safe(set.Command)), "  Statement completed without a result table.")
		return fitBlock(strings.Join(lines, "\n"), width, height)
	}
	view := r.view
	if view.set != &r.batch.Results[r.setIndex] {
		view = rowView{}
	}
	gridRows := m.gridRows()
	arrow := ""
	var notes []string
	if view.filter != "" {
		notes = append(notes, "filter '"+termtext.Safe(view.filter)+"' · "+countText(len(gridRows))+" of "+countText(view.count)+" rows")
	}
	if view.order != unsorted && view.sortColumn < len(set.Columns) {
		arrow = "↑"
		if view.order == descending {
			arrow = "↓"
		}
		notes = append(notes, "sorted by "+termtext.Safe(set.Columns[view.sortColumn].Name)+" "+arrow)
	}
	if len(notes) > 0 {
		lines[0] += "  " + warningStyle.Render(strings.Join(notes, " · "))
	}
	columns := make([]int, 0, 10)
	used := 6
	for col := r.gridLeft; col < len(set.Columns); col++ {
		if used >= width && len(columns) > 0 {
			break
		}
		columns = append(columns, col)
		used += r.widths[r.setIndex][col] + 1
	}
	names, types := mutedStyle.Render("    # "), "      "
	for _, col := range columns {
		column := set.Columns[col]
		format := cell.Auto
		if r.setIndex < len(r.formats) && r.formats[r.setIndex] != nil {
			if value, ok := r.formats[r.setIndex][col]; ok {
				format = value
			}
		}
		name := termtext.Safe(column.Name)
		typeName := termtext.Safe(column.Type)
		if column.OID == 17 || format != cell.Auto {
			typeName += " · " + string(format)
		}
		columnWidth := r.widths[r.setIndex][col]
		if arrow != "" && col == view.sortColumn {
			name = ansi.Truncate(name, max(1, columnWidth-2), "") + " " + arrow
		}
		names += accentStyle.Render(fitLine(name, columnWidth)) + " "
		types += mutedStyle.Render(fitLine(typeName, columnWidth)) + " "
	}
	lines = append(lines, names, types)
	rows := max(1, height-3)
	for row := r.gridTop; row < min(len(gridRows), r.gridTop+rows); row++ {
		values := set.Rows[gridRows[row]]
		rowLabel := fmt.Sprintf("%5d ", row+1)
		if row == r.row {
			rowLabel = fmt.Sprintf("›%4d ", row+1)
		}
		line := mutedStyle.Render(rowLabel)
		for _, col := range columns {
			text := ""
			format := cell.Auto
			if r.setIndex < len(r.formats) && r.formats[r.setIndex] != nil {
				if value, ok := r.formats[r.setIndex][col]; ok {
					format = value
				}
			}
			if col < len(values) {
				text = cell.Display(values[col], set.Columns[col], format)
			}
			text = fitLine(ansi.Truncate(text, max(1, r.widths[r.setIndex][col]-1), "…"), r.widths[r.setIndex][col])
			if row == r.row && col == r.column {
				text = cursorStyle.Render(text)
			} else if r.rowAnchor >= 0 && row >= min(r.row, r.rowAnchor) && row <= max(r.row, r.rowAnchor) && col >= min(r.column, r.columnAnchor) && col <= max(r.column, r.columnAnchor) {
				text = selectedStyle.Render(text)
			} else if col < len(values) && values[col].Null {
				text = mutedStyle.Italic(true).Render(text)
			}
			line += text + " "
		}
		lines = append(lines, line)
	}
	if len(set.Rows) == 0 {
		lines = append(lines, mutedStyle.Render("  No rows returned."))
	} else if len(gridRows) == 0 {
		lines = append(lines, mutedStyle.Render("  No rows match · Esc clears the filter"))
	}
	return fitBlock(strings.Join(lines, "\n"), width, height)
}

func (m *Model) inspectorView(width, height int) string {
	i := m.inspector
	position := fmt.Sprintf("row %d", i.row+1)
	if i.source != i.row {
		position += fmt.Sprintf(" (original row %d)", i.source+1)
	}
	meta := termtext.Safe(i.column.Name) + " · " + termtext.Safe(i.column.Type) + " · " + string(i.format) + " · " + position + " · value snapshot"
	if i.column.OID == 17 {
		meta += fmt.Sprintf(" · %d bytes", len(i.value.Bytes))
	}
	lines := []string{mutedStyle.Render(meta)}
	rows := max(1, height-6)
	if i.json {
		if i.filter != "" {
			lines = append(lines, warningStyle.Render("Key filter: "+termtext.Safe(i.filter)+" · Esc clears"))
		} else if len(i.visible) > 0 {
			lines = append(lines, accentStyle.Render(termtext.Safe(i.tree.Nodes[i.visible[min(i.index, len(i.visible)-1)]].Path)))
		}
		for position := i.top; position < min(len(i.visible), i.top+rows); position++ {
			node := i.tree.Nodes[i.visible[position]]
			label, value := i.tree.DisplayParts(i.visible[position])
			if i.filter != "" && node.Container {
				label = strings.Replace(label, "▸", "▾", 1)
			}
			if position == i.index {
				// One style over plain text so the highlight spans the row.
				line := ansi.Cut(label+": "+value, i.left, i.left+max(1, width-4))
				lines = append(lines, selectedStyle.Render(fitLine(line, max(1, width-2))))
				continue
			}
			valueStyle := mutedStyle
			switch {
			case node.Container:
			case node.Kind == '"':
				valueStyle = stringStyle
			case node.Kind == 't' || node.Kind == 'f':
				valueStyle = accentStyle
			case node.Kind == 'n':
				valueStyle = mutedStyle.Italic(true)
			default:
				valueStyle = numberStyle
			}
			line := keywordStyle.UnsetBold().Render(label) + mutedStyle.Render(": ") + valueStyle.Render(value)
			lines = append(lines, ansi.Cut(line, i.left, i.left+max(1, width-4)))
		}
		if len(i.visible) == 0 {
			lines = append(lines, "No keys match. Esc clears the filter.")
		}
	} else {
		lines = append(lines, "")
		for position := i.top; position < min(len(i.lines), i.top+rows); position++ {
			line := ansi.Cut(i.lines[position], i.left, i.left+max(1, width-4))
			if position == i.index {
				line = selectedStyle.Render(fitLine(line, max(1, width-2)))
			}
			lines = append(lines, line)
		}
	}
	if i.error != "" {
		lines = append(lines, errorStyle.Render(termtext.Safe(i.error)))
	}
	return panel("× Close · VALUE INSPECTOR", strings.Join(lines, "\n"), width, height, true)
}

func (m *Model) modalView(width, height int) string {
	dialog := m.promptLayout(width, height)
	box := panel(dialog.title, strings.Join(dialog.lines, "\n"), dialog.box.width, dialog.box.height, true)
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, box)
}

const helpText = `MOVING AROUND
Arrows          Move within the selected text, table, or tree
SQL Left at 0   Move to this buffer's database (or Add database)
SQL Down at end Move to results, keeping their previous selection
Results Up/Left At the top/left edge, return to SQL/databases
Database Right  Move to SQL from any selected row
Enter / Space   Activate the selected tree item, button, or cell
Esc             Back out one level: filter, selection, inspector, dialog
Shift+movement  Extend selection within the current control
Click / drag    Focus and select text or a rectangle of result cells
Double click    Select a SQL word, inspect a cell, or activate a list row
Wheel           Scroll the pane under the pointer; keep keyboard focus
Ctrl+Tab        Cycle panes; Ctrl+Shift+Tab cycles backward

MAIN
Ctrl+P          Command palette: every action is available here
Ctrl+B          Databases: open more connections, add, edit
F2 / F6         Focus SQL / databases
Ctrl+J / F5     Run selected SQL or statement at cursor
Ctrl+Enter      Run alias (also Cmd+Enter where the terminal forwards it)
\d \l \q \?     psql commands run as their scrunch equivalents
Ctrl+C          Cancel this buffer's query or connecting; otherwise copy
Ctrl+Q          Quit, checking unsaved SQL and open transactions
F8              In-memory history of every connection (Enter opens; never runs)
F9 / F10        Toggle catalog / maximize focused pane

CONNECTIONS
Open several    Each saved database opens as its own connection
Buffers         Each SQL buffer runs on one connection, shown in its tab
Header          Shows the current buffer's connection and write mode
Ctrl+P          Change buffer connection… picks the one Run uses
Results         Each buffer keeps its own; switching buffers shows them
Concurrency     One query per connection; others keep running
Disconnect      x on an open database, or Ctrl+P → Disconnect

SAFETY
Read-only       Connections start read-only; Ctrl+P → Allow writes
WRITES ON       Pink header: this buffer's connection can change data
No WHERE        UPDATE/DELETE without WHERE and TRUNCATE ask first
Ctrl+J          After a semicolon or in a gap, runs the statement above

SQL EDITOR
Arrows          Move; Shift+arrows selects text
Home / End      Line edges; Ctrl+Home/End buffer edges
Ctrl+Left/Right Move by word; Shift also selects
Tab / Shift+Tab Indent / dedent; never changes editor focus
Ctrl+A          Select all SQL
Ctrl+X/C        Cut / copy selection through terminal OSC52
Ctrl+Z / Ctrl+Y Undo / redo
Ctrl+N/O/S/W    New / open / save / close buffer (new ones keep its connection)
Ctrl+PgUp/PgDn  Previous / next SQL buffer
Ctrl+F / F3     Find / next match
Ctrl+G          Go to line
F4              Format SQL
Ctrl+/ Ctrl+_   Toggle comments (underscore is the fallback)
Shift+Delete    Delete the current line
Ctrl+Space      Completion; Tab/Enter accepts, Esc dismisses
                Enter after a fully typed word ends the line instead
Paste           Use the terminal's own Paste command

RESULTS
Arrows          Move cells; Shift+arrows selects a rectangle
Ctrl+A          Select all retained cells
Home/End        First/last column; Ctrl+Home/End first/last cell
PgUp/PgDn       Page through retained rows
[ / ]           Previous / next statement result
Enter           Inspect complete cell value
v               Cycle auto/text/JSON/hex/base64/UUID display
+ / -           Widen / narrow selected column
Ctrl+C          Copy selected cell or rectangle as TSV; NULL cell as empty
y               Copy as TSV, CSV, Markdown, JSON, INSERT, or IN list
s               Sort by column: ascending, descending, original order
f               Filter rows by displayed text; column:text for one column
Esc             Clear rectangle, then filter, then return to SQL
Ctrl+E          Export the current (sorted/filtered) view to .csv or .json
m               Fetch more rows: re-runs a paged query for 10× rows
/ or Ctrl+F     Find in bounded cell previews; F3 next match

DATABASE PANE
Arrows          Up/Down select; Left collapse/parent; Right SQL
Enter/Space     Open a saved database, switch to an open one, or toggle tree
+ Add database  Pinned row: arrows then Enter/Space, or click
Letters         Type a name to filter; matches show as you type
.               Actions: SELECT, structure, DDL, COUNT, insert name,
                edit settings, disconnect
  In structure  Enter/Space section · D DDL buffer · Ctrl+C line · Esc
/ or Ctrl+F     Filter too; F3 next match; Esc clears
Ctrl+R          Refresh catalog

JSON / VALUE INSPECTOR
Up/Down         Select visible key or preview line
Left/Right      Collapse/expand JSON, or scroll text horizontally
Enter/Space     Toggle selected container
e / c           Expand all / collapse all (without a filter)
/ or Ctrl+F     Filter JSON keys (ancestors stay visible)
F3              Next matching key/path, wrapping through matches
While filtered  Left selects parent; Right/Enter/Space enters child
Ctrl+C / p      Copy selected JSON value / copy its path
v               Cycle display format
Alt+Left/Right  Horizontal scrolling
Esc             Clear filter, then return to originating cell

PROMPTS
Printable keys  Always edit the prompt; no navigation letters
Up/Down/Tab     Select an item or confirmation button
Enter / Esc     Accept / cancel; confirmations begin on Cancel

DATABASES
Up/Down         Select a saved database or Add database
Enter           Open as another connection, or switch if open (runs no SQL)
n / e / d       Add / edit / remove saved connection settings
Tab / Shift+Tab Move through connection form fields and buttons
Enter           Next field, or activate Connect / Save / Cancel
List databases  Explicitly connect to postgres and choose a database name
Space           Toggle Remember when its checkbox has focus
Password        Masked, cannot be copied, never saved
Password env    Optional environment variable NAME, saved as a name
Esc             Close form; while connecting, request cancellation
Plain scrunch      Starts blank without contacting PostgreSQL

SSH AND DATA
Ctrl+J works without enhanced keyboard support. Some terminals
intercept modified keys; the command palette has alternatives.
Clipboard writes require terminal OSC52 support. Copy requests
are explicit; scrunch never reads your system clipboard. Paste is
native bracketed paste. No clipboard executables are required.
History holds at most 100 queries / 32 MiB and is never saved.
Results are limited in memory. Export includes retained rows.
A single SELECT/VALUES/TABLE/WITH fetches 1,000 rows first;
PostgreSQL stops there. m re-runs it for more, up to --max-rows.
Changing display format never changes the original value.`
