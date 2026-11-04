package tui

import (
	"fmt"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/dustinevan/scrunchie/pkg/termtext"
)

type screenRect struct{ x, y, width, height int }

func (r screenRect) contains(x, y int) bool {
	return r.width > 0 && r.height > 0 && x >= r.x && x < r.x+r.width && y >= r.y && y < r.y+r.height
}

// content matches panel's border and title, including its borderless tiny view.
func (r screenRect) content() screenRect {
	if r.width < 4 || r.height < 4 {
		return r
	}
	return screenRect{r.x + 1, r.y + 2, r.width - 2, r.height - 3}
}

func (m *Model) centeredRect(width, height int) screenRect {
	return screenRect{max(0, (m.width-width)/2), 1 + max(0, (m.dimensions().bodyHeight-height)/2), width, height}
}

func (m *Model) paneRect(target pane) screenRect {
	l := m.dimensions()
	if l.single {
		if target == m.focus {
			return screenRect{0, 1, m.width, l.bodyHeight}
		}
		return screenRect{}
	}
	switch target {
	case catalogPane:
		return screenRect{0, 1, l.catalogWidth, l.bodyHeight}
	case editorPane:
		return screenRect{l.catalogWidth, 1, l.rightWidth, l.editorHeight}
	case resultsPane:
		return screenRect{l.catalogWidth, 1 + l.editorHeight, l.rightWidth, l.resultHeight}
	}
	return screenRect{}
}

func (m *Model) editorTextRect() screenRect {
	r := m.paneRect(editorPane).content()
	if r.width == 0 || r.height < 2 {
		return screenRect{}
	}
	rows := max(1, r.height-2)
	if len(m.completion.items) > 0 && m.focus == editorPane {
		rows = max(1, rows-completionHeight(rows))
	}
	return screenRect{r.x, r.y + 1, r.width, min(rows, r.height-1)}
}

func (m *Model) gridDataRect() screenRect {
	r := m.paneRect(resultsPane).content()
	return screenRect{r.x, r.y + 3, r.width, max(0, r.height-3)}
}

// editorPosition translates terminal cells back to rune offsets. It uses the
// same visible escaping and tab width as the editor instead of counting bytes.
func editorPosition(e editor, area screenRect, x, y int, numbers, masked bool) int {
	if len(e.lines) == 0 {
		e.viewport(max(1, area.width), max(1, area.height))
	}
	row := max(0, min(e.top+max(0, y-area.y), len(e.lines)-1))
	start := e.lines[row]
	end := len(e.text)
	if row+1 < len(e.lines) {
		end = e.lines[row+1] - 1
	}
	column := max(0, x-area.x)
	if numbers {
		column = max(0, column-lipgloss.Width(fmt.Sprintf("%4d ", row+1)))
	}
	start = min(end, start+e.left)
	used := 0
	for position := start; position < end; position++ {
		width := editorRuneWidth(e.text[position])
		if masked {
			width = 1
		}
		if column < used+width {
			return position
		}
		used += width
	}
	return end
}

func editorRuneWidth(r rune) int {
	if r == '\t' {
		return 4
	}
	return lipgloss.Width(termtext.Safe(string(r)))
}

type mouseState struct {
	drag      string
	buffer    uint64
	result    int
	query     uint64
	x, y      int
	lastX     int
	lastY     int
	lastClick time.Time
	context   string
	target    string
	clicks    int
	serial    uint64
	tick      bool
}

type mouseScrollMsg struct{ serial uint64 }

func (m *Model) mouseEvent(message tea.MouseMsg) tea.Cmd {
	r := m.results()
	pointer := message.Mouse()
	_, click := message.(tea.MouseClickMsg)
	_, release := message.(tea.MouseReleaseMsg)
	_, motion := message.(tea.MouseMotionMsg)
	_, wheel := message.(tea.MouseWheelMsg)
	if click || wheel {
		m.interaction++
	}
	if wheel {
		m.mouse.lastClick = time.Time{}
		m.mouse.clicks = 0
		m.mouse.target, m.mouse.drag = "", ""
		m.modalMouseState.dragging = false
	}
	double := false
	previousTarget := m.mouse.target
	if click {
		if pointer.Button != tea.MouseLeft {
			m.mouse.drag = ""
			return nil
		}
		context := fmt.Sprintf("%s/%d/%d/%t/%d/%d/%d/%d", m.modal.kind, m.modalSerial, m.confirmBuffer, m.inspector.active, m.bufferIndex, r.setIndex, m.width, m.height)
		if m.modal.kind != "" && context != m.mouse.context && pointer.X == m.mouse.lastX && pointer.Y == m.mouse.lastY && time.Since(m.mouse.lastClick) < 400*time.Millisecond {
			return nil
		}
		if pointer.X == m.mouse.lastX && pointer.Y == m.mouse.lastY && context == m.mouse.context && time.Since(m.mouse.lastClick) < 400*time.Millisecond {
			m.mouse.clicks++
		} else {
			m.mouse.clicks = 1
		}
		m.mouse.context, m.mouse.lastClick = context, time.Now()
		m.mouse.lastX, m.mouse.lastY = pointer.X, pointer.Y
		m.mouse.serial++
		m.mouse.tick = false
		m.mouse.drag = ""
		m.mouse.target = ""
		double = m.mouse.clicks == 2
	}
	if m.modal.kind != "" {
		m.mouse.drag = ""
		return m.modalMouse(message, double)
	}
	m.modalMouseState = modalMouseState{}
	if release {
		if m.mouse.drag == "grid" && r.rowAnchor == r.row && r.columnAnchor == r.column {
			r.rowAnchor, r.columnAnchor = -1, -1
		}
		m.mouse.drag = ""
		return nil
	}
	if motion {
		if pointer.Button != tea.MouseLeft {
			m.mouse.drag = ""
			return nil
		}
		m.mouse.x, m.mouse.y = pointer.X, pointer.Y
		return m.dragSelection()
	}
	if pointer.X < 0 || pointer.Y < 1 || pointer.X >= m.width || pointer.Y >= m.height-2 {
		return nil
	}
	if m.inspector.active {
		return m.inspectorMouse(pointer, click, wheel, double)
	}
	if m.structure.active {
		return m.structureMouse(pointer, click, wheel, double)
	}
	if wheel {
		m.mouse.drag = ""
		m.mouse.clicks = 0
		m.wheelPane(pointer)
		return nil
	}
	if !click {
		return nil
	}
	for _, target := range []pane{catalogPane, editorPane, resultsPane} {
		box := m.paneRect(target)
		if !box.contains(pointer.X, pointer.Y) {
			continue
		}
		content := box.content()
		// Keep completion's visible list until its click has been interpreted.
		if target == editorPane && m.focus == editorPane {
			if cmd, handled := m.completionMouse(pointer); handled {
				return cmd
			}
		}
		m.focusPane(target)
		switch target {
		case catalogPane:
			visible := m.visibleDatabaseRows(content.height)
			line := pointer.Y - content.y
			if !content.contains(pointer.X, pointer.Y) || line >= len(visible) {
				return nil
			}
			index := visible[line]
			m.selectDatabaseRow(index)
			row := m.databaseRows()[index]
			m.mouse.target = "database/" + row.key
			if previousTarget != m.mouse.target {
				double, m.mouse.clicks = false, 1
			}
			// A disclosure triangle is a button; the rest of the row selects.
			if (row.kind == databaseConnection || row.kind == databaseCatalog) && pointer.X == content.x+row.depth*2 {
				if c := m.connectionFor(row.conn); row.kind == databaseConnection && c != nil {
					c.collapsed = !c.collapsed
					return nil
				}
				return m.databasePaneKey(tea.KeyPressMsg{Code: tea.KeyEnter})
			}
			if row.kind == databaseAdd || double {
				return m.databasePaneKey(tea.KeyPressMsg{Code: tea.KeyEnter})
			}
		case editorPane:
			if pointer.Y == content.y && m.clickBufferTab(pointer.X-content.x) {
				return nil
			}
			area := m.editorTextRect()
			if !area.contains(pointer.X, pointer.Y) {
				return nil
			}
			e := &m.buffers[m.bufferIndex].editor
			position := editorPosition(*e, area, pointer.X, pointer.Y, true, false)
			anchor := position
			if pointer.Mod.Contains(tea.ModShift) {
				anchor = e.anchor
				if anchor < 0 {
					anchor = e.cursor
				}
			}
			e.cursor, e.anchor = position, anchor
			e.verticalMotion, e.scrolling, e.lastKind = false, false, ""
			if m.mouse.clicks == 3 {
				e.anchor, e.cursor = e.lineBounds()
				if e.cursor < len(e.text) {
					e.cursor++
				}
				return nil
			}
			if double {
				e.anchor, e.cursor = editorWord(e.text, position)
				return nil
			}
			m.mouse.drag, m.mouse.buffer = "editor", m.buffers[m.bufferIndex].id
		case resultsPane:
			if pointer.Y == content.y && m.clickResultTab(pointer.X-content.x) {
				return nil
			}
			if m.gridHeaderClick(pointer) {
				return nil
			}
			row, column, ok := m.gridPosition(pointer.X, pointer.Y, false)
			if !ok {
				return nil
			}
			if pointer.Mod.Contains(tea.ModShift) {
				if r.rowAnchor < 0 {
					r.rowAnchor, r.columnAnchor = r.row, r.column
				}
			} else {
				r.rowAnchor, r.columnAnchor = row, column
			}
			r.row, r.column = row, column
			if double {
				r.rowAnchor, r.columnAnchor = -1, -1
				m.openInspector()
				return nil
			}
			m.mouse.drag, m.mouse.buffer, m.mouse.result, m.mouse.query = "grid", m.buffers[m.bufferIndex].id, r.setIndex, r.serial
		}
		m.mouse.x, m.mouse.y = pointer.X, pointer.Y
		return nil
	}
	return nil
}

func editorWord(text []rune, position int) (int, int) {
	if len(text) == 0 {
		return 0, 0
	}
	position = min(position, len(text)-1)
	word := func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' }
	if !word(text[position]) {
		return position, position + 1
	}
	start, end := position, position+1
	for start > 0 && word(text[start-1]) {
		start--
	}
	for end < len(text) && word(text[end]) {
		end++
	}
	return start, end
}

func (m *Model) completionMouse(pointer tea.Mouse) (tea.Cmd, bool) {
	if len(m.completion.items) == 0 {
		return nil, false
	}
	area := m.editorTextRect()
	rows := completionHeight(max(1, m.paneRect(editorPane).content().height-2))
	line := pointer.Y - area.y - area.height
	if pointer.X < area.x || pointer.X >= area.x+area.width || line < 0 || line >= rows {
		return nil, false
	}
	index := max(0, m.completion.index-rows+1) + line
	if index >= len(m.completion.items) {
		return nil, true
	}
	m.completion.index = index
	m.acceptCompletion()
	return nil, true
}

func (m *Model) clickBufferTab(x int) bool {
	used := 0
	for index := max(0, m.bufferIndex-1); index < min(len(m.buffers), m.bufferIndex+4); index++ {
		width := lipgloss.Width(m.bufferTab(m.buffers[index]))
		if x >= used && x < used+width {
			if index != m.bufferIndex {
				m.switchBuffer(index)
			}
			return true
		}
		used += width + 1
	}
	return false
}

func (m *Model) clickResultTab(x int) bool {
	r := m.results()
	used := 0
	for index := max(0, r.setIndex-1); index < min(len(r.batch.Results), r.setIndex+5); index++ {
		width := lipgloss.Width(fmt.Sprintf(" %d %s ", index+1, termtext.Safe(r.batch.Results[index].Command)))
		if x >= used && x < used+width {
			if index != r.setIndex {
				r.setIndex = index
				r.row, r.column, r.gridTop, r.gridLeft = 0, 0, 0, 0
				r.rowAnchor, r.columnAnchor = -1, -1
			}
			return true
		}
		used += width + 1
	}
	return false
}

func (m *Model) gridPosition(x, y int, clamp bool) (int, int, bool) {
	r := m.results()
	area := m.gridDataRect()
	if area.width == 0 || area.height == 0 || r.setIndex < 0 || r.setIndex >= len(r.batch.Results) || r.setIndex >= len(r.widths) {
		return 0, 0, false
	}
	set := r.batch.Results[r.setIndex]
	rows := m.gridRows()
	if len(rows) == 0 || len(set.Columns) == 0 {
		return 0, 0, false
	}
	if !clamp && !area.contains(x, y) {
		return 0, 0, false
	}
	row := r.gridTop + max(0, min(y-area.y, area.height-1))
	if !clamp && row >= len(rows) {
		return 0, 0, false
	}
	row = min(len(rows)-1, row)
	used := area.x + 6
	for column := r.gridLeft; column < len(set.Columns); column++ {
		width := r.widths[r.setIndex][column]
		if x < used+width+1 || (clamp && column == len(set.Columns)-1) {
			return row, column, true
		}
		used += width + 1
	}
	return 0, 0, false
}

// Row numbers and column headers select whole rows or columns. They change
// only the retained-result selection, never the database contents.
func (m *Model) gridHeaderClick(pointer tea.Mouse) bool {
	r := m.results()
	if r.setIndex < 0 || r.setIndex >= len(r.batch.Results) || r.setIndex >= len(r.widths) {
		return false
	}
	set := r.batch.Results[r.setIndex]
	rows := m.gridRows()
	if len(rows) == 0 || len(set.Columns) == 0 {
		return false
	}
	content := m.paneRect(resultsPane).content()
	data := m.gridDataRect()
	if !content.contains(pointer.X, pointer.Y) {
		return false
	}
	if pointer.X < content.x+6 && pointer.Y >= data.y && pointer.Y < data.y+data.height {
		row := r.gridTop + pointer.Y - data.y
		if row >= len(rows) {
			return false
		}
		if !pointer.Mod.Contains(tea.ModShift) || r.rowAnchor < 0 {
			r.rowAnchor = row
		}
		r.columnAnchor, r.column = 0, len(set.Columns)-1
		r.row = row
		m.gridScrolling = true
		return true
	}
	if pointer.Y != content.y+1 && pointer.Y != content.y+2 {
		return false
	}
	if pointer.X < content.x+6 {
		r.rowAnchor, r.columnAnchor = 0, 0
		r.row, r.column = len(rows)-1, len(set.Columns)-1
		m.gridScrolling = true
		return true
	}
	used := content.x + 6
	for column := r.gridLeft; column < len(set.Columns); column++ {
		width := r.widths[r.setIndex][column]
		if pointer.X < used+width+1 {
			if !pointer.Mod.Contains(tea.ModShift) || r.columnAnchor < 0 {
				r.columnAnchor = column
			}
			r.rowAnchor, r.row = 0, len(rows)-1
			r.column = column
			m.gridScrolling = true
			return true
		}
		used += width + 1
	}
	return false
}

func (m *Model) wheelPane(pointer tea.Mouse) {
	r := m.results()
	delta := 3
	if pointer.Button == tea.MouseWheelUp || pointer.Button == tea.MouseWheelLeft {
		delta = -3
	}
	horizontal := pointer.Button == tea.MouseWheelLeft || pointer.Button == tea.MouseWheelRight || pointer.Mod.Contains(tea.ModShift)
	if m.paneRect(editorPane).contains(pointer.X, pointer.Y) {
		e := &m.buffers[m.bufferIndex].editor
		e.scrolling = true
		if horizontal {
			e.left = max(0, e.left+delta)
		} else {
			e.top = max(0, min(max(0, len(e.lines)-m.editorTextRect().height), e.top+delta))
		}
	} else if m.paneRect(resultsPane).contains(pointer.X, pointer.Y) && r.setIndex >= 0 && r.setIndex < len(r.batch.Results) {
		set := r.batch.Results[r.setIndex]
		rows := m.gridRows()
		m.gridScrolling = true
		if horizontal {
			r.gridLeft = max(0, min(len(set.Columns)-1, r.gridLeft+delta))
		} else {
			r.gridTop = max(0, min(max(0, len(rows)-m.gridDataRect().height), r.gridTop+delta))
		}
	} else if m.paneRect(catalogPane).contains(pointer.X, pointer.Y) && !horizontal {
		m.scrollDatabaseRows(delta)
	}
}

func (m *Model) dragSelection() tea.Cmd {
	r := m.results()
	if m.mouse.drag == "" || m.modal.kind != "" || m.inspector.active {
		return nil
	}
	x, y := m.mouse.x, m.mouse.y
	area := screenRect{}
	switch m.mouse.drag {
	case "editor":
		if m.buffers[m.bufferIndex].id != m.mouse.buffer {
			m.mouse.drag = ""
			return nil
		}
		area = m.editorTextRect()
		e := &m.buffers[m.bufferIndex].editor
		if y < area.y {
			e.top = max(0, e.top-1)
		} else if y >= area.y+area.height {
			e.top = min(max(0, len(e.lines)-area.height), e.top+1)
		}
		if x < area.x+5 {
			e.left = max(0, e.left-1)
		} else if x >= area.x+area.width {
			row := max(0, min(len(e.lines)-1, e.top+max(0, min(y-area.y, area.height-1))))
			end := len(e.text)
			if row+1 < len(e.lines) {
				end = e.lines[row+1] - 1
			}
			e.left = max(0, min(end-e.lines[row], e.left+1))
		}
		e.cursor = editorPosition(*e, area, max(area.x, min(x, area.x+area.width-1)), max(area.y, min(y, area.y+area.height-1)), true, false)
		e.verticalMotion, e.scrolling = false, false
	case "grid":
		if m.buffers[m.bufferIndex].id != m.mouse.buffer || r.setIndex != m.mouse.result || r.serial != m.mouse.query || r.setIndex >= len(r.batch.Results) {
			m.mouse.drag = ""
			return nil
		}
		area = m.gridDataRect()
		set := r.batch.Results[r.setIndex]
		rows := m.gridRows()
		if y < area.y {
			r.gridTop = max(0, r.gridTop-1)
		} else if y >= area.y+area.height {
			r.gridTop = min(max(0, len(rows)-area.height), r.gridTop+1)
		}
		if x < area.x+6 {
			r.gridLeft = max(0, r.gridLeft-1)
		} else if x >= area.x+area.width {
			r.gridLeft = min(max(0, len(set.Columns)-1), r.gridLeft+1)
		}
		row, column, ok := m.gridPosition(max(area.x, min(x, area.x+area.width-1)), y, true)
		if ok {
			r.row, r.column = row, column
		}
	}
	if area.width > 0 && area.height > 0 && !area.contains(x, y) && !m.mouse.tick {
		m.mouse.tick = true
		serial := m.mouse.serial
		return tea.Tick(60*time.Millisecond, func(time.Time) tea.Msg { return mouseScrollMsg{serial: serial} })
	}
	return nil
}

func (m *Model) mouseScroll(message mouseScrollMsg) tea.Cmd {
	if message.serial != m.mouse.serial {
		return nil
	}
	m.mouse.tick = false
	return m.dragSelection()
}

func (m *Model) inspectorMouse(pointer tea.Mouse, click, wheel, double bool) tea.Cmd {
	if click && m.width >= 4 && m.dimensions().bodyHeight >= 4 && pointer.Y == 2 && pointer.X >= 3 && pointer.X < 10 {
		m.focusPane(resultsPane)
		return nil
	}
	area := (screenRect{0, 1, m.width, m.dimensions().bodyHeight}).content()
	area.y += 2
	area.height = max(0, m.dimensions().bodyHeight-6)
	i := &m.inspector
	count := len(i.lines)
	if i.json {
		count = len(i.visible)
	}
	if wheel {
		delta := 3
		if pointer.Button == tea.MouseWheelUp || pointer.Button == tea.MouseWheelLeft {
			delta = -3
		}
		i.scrolling = true
		if pointer.Button == tea.MouseWheelLeft || pointer.Button == tea.MouseWheelRight || pointer.Mod.Contains(tea.ModShift) {
			i.left = max(0, i.left+delta)
		} else {
			i.top = max(0, min(max(0, count-area.height), i.top+delta))
		}
		return nil
	}
	if click && area.contains(pointer.X, pointer.Y) {
		position := i.top + pointer.Y - area.y
		if position >= count {
			return nil
		}
		i.index, i.scrolling = position, false
		if i.json {
			node := i.tree.Nodes[i.visible[position]]
			// Display indents JSON containers by their nesting depth.
			depth := 0
			for parent := node.Parent; parent >= 0; parent = i.tree.Nodes[parent].Parent {
				depth++
			}
			if double || pointer.X-area.x+i.left == depth*2 {
				return m.inspectorKey(tea.KeyPressMsg{Code: tea.KeyEnter})
			}
		}
	}
	return nil
}
