package tui

import (
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/dustinevan/scrunchie/pkg/termtext"
)

type modalMouseState struct {
	dragging  bool
	kind      string
	field     int
	anchor    int
	pressKind string
}

type dialogHit struct {
	kind  string
	index int
	area  screenRect
	input screenRect
}

type dialogLayout struct {
	box   screenRect
	title string
	lines []string
	hits  []dialogHit
}

func (d dialogLayout) listRows() int {
	rows := 0
	for _, hit := range d.hits {
		if hit.kind == "list" {
			rows++
		}
	}
	return max(1, rows)
}

func (d *dialogLayout) clipHits() {
	content := d.box.content()
	visible := d.hits[:0]
	for _, hit := range d.hits {
		right := min(hit.area.x+hit.area.width, content.x+content.width)
		bottom := min(hit.area.y+hit.area.height, content.y+content.height)
		hit.area.x = max(hit.area.x, content.x)
		hit.area.y = max(hit.area.y, content.y)
		hit.area.width = right - hit.area.x
		hit.area.height = bottom - hit.area.y
		if hit.area.width > 0 && hit.area.height > 0 {
			visible = append(visible, hit)
		}
	}
	d.hits = visible
}

func (d *dialogLayout) addClose(title string) {
	d.title = termtext.Safe(title)
	if d.box.width >= 8 && d.box.height >= 4 {
		d.title = fitLine(d.title, d.box.width-6) + " ×"
		d.hits = append(d.hits, dialogHit{kind: "close", area: screenRect{x: d.box.x + d.box.width - 3, y: d.box.y + 1, width: 2, height: 1}})
	}
}

func (m *Model) promptLayout(width, height int) dialogLayout {
	boxWidth := max(1, min(96, width-4))
	boxHeight := max(1, min(height, 24))
	contentWidth := max(1, boxWidth-2)
	if boxWidth < 4 || boxHeight < 4 {
		contentWidth = boxWidth
	}
	var lines []string
	var hits []dialogHit
	if m.modalAcceptsText() {
		hits = append(hits, dialogHit{kind: "prompt", index: -1, area: screenRect{width: contentWidth, height: 1}, input: screenRect{width: contentWidth, height: 1}})
		lines = append(lines, m.modal.input.render(contentWidth, 1, true, false), mutedStyle.Render(strings.Repeat("─", contentWidth)))
	}
	switch m.modal.kind {
	case "palette", "catalog-actions":
		items := m.paletteItems()
		rows := max(1, boxHeight-6)
		start := max(0, m.modal.index-rows+1)
		for index := start; index < min(len(items), start+rows); index++ {
			item := items[index]
			line := fitLine("  "+item.label, max(1, contentWidth-len(item.key)-2)) + mutedStyle.Render(item.key)
			if index == m.modal.index {
				line = selectedStyle.Render(fitLine("› "+item.label, max(1, contentWidth-len(item.key)-2)) + item.key)
			}
			hits = append(hits, dialogHit{kind: "list", index: index, area: screenRect{y: len(lines), width: contentWidth, height: 1}})
			lines = append(lines, line)
		}
		if len(items) == 0 {
			lines = append(lines, "No matching commands")
		}
	case "history":
		items := m.modal.historyRows
		rows := max(1, (boxHeight-7)/2)
		start := max(0, m.modal.index-rows+1)
		for index := start; index < min(len(items), start+rows); index++ {
			entry := m.history[items[index]]
			label := entry.at.Format("15:04:05")
			if entry.connName != "" {
				label += "  " + termtext.Safe(entry.connName)
			}
			label += "  " + entry.duration.Round(time.Millisecond).String() + "  " + strconv.FormatInt(entry.rows, 10) + " rows"
			if entry.more {
				label += " fetched, more available"
			}
			if entry.err != "" {
				label += "  ERROR"
			}
			preview := entry.sql
			if len(preview) > 512 {
				preview = preview[:512] + "…"
			}
			text := termtext.Safe(strings.Join(strings.Fields(preview), " "))
			if index == m.modal.index {
				label = selectedStyle.Render(fitLine("› "+label, contentWidth))
			}
			hits = append(hits, dialogHit{kind: "list", index: index, area: screenRect{y: len(lines), width: contentWidth, height: 2}})
			lines = append(lines, label, "  "+ansi.Truncate(text, max(1, contentWidth-2), "…"))
		}
		if len(items) == 0 {
			lines = append(lines, "No matching queries in this session")
		}
	case "buffer-connection":
		lines = append(lines, mutedStyle.Render("Run uses the chosen connection · results stay with the buffer"), "")
		for index, id := range m.modal.choices {
			label := "No connection"
			if c := m.connectionFor(id); c != nil {
				label = "● " + connectionRow(c, m.config.Demo, true)
			} else if id != 0 {
				label = "○ closed connection"
			}
			line := "  " + label
			if index == m.modal.index {
				line = selectedStyle.Render(fitLine("› "+label, contentWidth))
			}
			hits = append(hits, dialogHit{kind: "list", index: index, area: screenRect{y: len(lines), width: contentWidth, height: 1}})
			lines = append(lines, line)
		}
	case "copy-as":
		lines = append(lines, mutedStyle.Render(ansi.Truncate(termtext.Safe(m.modal.message), contentWidth, "…")), "")
		for index, format := range copyFormats {
			line := "  " + format.label
			if index == m.modal.index {
				line = selectedStyle.Render(fitLine("› "+format.label, contentWidth))
			}
			hits = append(hits, dialogHit{kind: "list", index: index, area: screenRect{y: len(lines), width: contentWidth, height: 1}})
			lines = append(lines, line)
		}
	case "help", "message":
		text := termtext.SafeMultiline(m.modal.message)
		if m.modal.kind == "help" {
			text = m.referenceText()
		}
		all := strings.Split(ansi.Hardwrap(text, contentWidth, false), "\n")
		rows := max(1, m.centeredRect(boxWidth, boxHeight).content().height-1)
		start := min(m.modal.index, max(0, len(all)-rows))
		lines = append(lines, all[start:min(len(all), start+rows)]...)
	default:
		if m.modal.message != "" {
			lines = append(lines, strings.Split(ansi.Hardwrap(termtext.SafeMultiline(m.modal.message), contentWidth, true), "\n")...)
		}
	}
	if m.modal.error != "" {
		lines = append(lines, "")
		for _, line := range strings.Split(ansi.Hardwrap(termtext.Safe(m.modal.error), contentWidth, true), "\n") {
			lines = append(lines, errorStyle.Render(line))
		}
	}
	if m.fileBusy {
		lines = append(lines, "", m.spinner.View()+" Working…")
	}
	if len(m.modal.buttons) > 0 {
		buttonLines := []string{""}
		var buttonHits []dialogHit
		x, row, selectedRow := 0, 0, 0
		for index, button := range m.modal.buttons {
			label := " " + termtext.Safe(button) + " "
			buttonWidth := min(contentWidth, lipgloss.Width(label))
			if x > 0 && x+buttonWidth > contentWidth {
				row++
				buttonLines = append(buttonLines, "")
				x = 0
			}
			label = fitLine(label, buttonWidth)
			if index == m.modal.index {
				selectedRow = row
				label = cursorStyle.Render(label)
			} else {
				label = mutedStyle.Render(label)
			}
			if x > 0 {
				buttonLines[row] += "  "
			}
			buttonLines[row] += label
			buttonHits = append(buttonHits, dialogHit{kind: "button", index: index, area: screenRect{x: x, y: row, width: buttonWidth, height: 1}})
			x += buttonWidth + 2
		}
		boxHeight = min(height, max(7, len(lines)+len(buttonLines)+4))
		available := max(1, boxHeight-3)
		if boxWidth < 4 || boxHeight < 4 {
			available = max(1, boxHeight)
		}
		first := max(0, selectedRow-available+1)
		shown := min(len(buttonLines)-first, available)
		messageRows := max(0, available-shown-1)
		if len(lines) > messageRows {
			lines = lines[:messageRows]
		}
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		buttonStart := len(lines)
		lines = append(lines, buttonLines[first:first+shown]...)
		for _, hit := range buttonHits {
			if hit.area.y >= first && hit.area.y < first+shown {
				hit.area.y += buttonStart - first
				hits = append(hits, hit)
			}
		}
	} else if m.modal.kind != "palette" && m.modal.kind != "history" && m.modal.kind != "help" && m.modal.kind != "message" && m.modal.kind != "catalog-actions" {
		boxHeight = min(height, max(7, len(lines)+4))
	}
	dialog := dialogLayout{box: m.centeredRect(boxWidth, boxHeight), lines: lines, hits: hits}
	content := dialog.box.content()
	for index := range dialog.hits {
		dialog.hits[index].area.x += content.x
		dialog.hits[index].area.y += content.y
		dialog.hits[index].input.x += content.x
		dialog.hits[index].input.y += content.y
	}
	dialog.clipHits()
	dialog.addClose(m.modal.title)
	return dialog
}

func (m *Model) modalMouse(message tea.MouseMsg, double bool) tea.Cmd {
	mouse := message.Mouse()
	if _, release := message.(tea.MouseReleaseMsg); release {
		m.modalMouseState.dragging = false
		return nil
	}
	var dialog dialogLayout
	if m.modal.kind == "connection" || m.modal.kind == "connections" || m.modal.kind == "database-names" {
		dialog = m.connectionLayout(m.width, m.dimensions().bodyHeight)
	} else {
		dialog = m.promptLayout(m.width, m.dimensions().bodyHeight)
	}
	busy := m.fileBusy || m.profileBusy || m.connecting || m.discovering
	if _, motion := message.(tea.MouseMotionMsg); motion {
		state := &m.modalMouseState
		if mouse.Button != tea.MouseLeft || !state.dragging || state.kind != m.modal.kind || busy {
			state.dragging = false
			return nil
		}
		for _, hit := range dialog.hits {
			if (hit.kind == "prompt" || hit.kind == "field") && hit.index == state.field {
				field := &m.modal.input
				masked := false
				if hit.kind == "field" {
					field = &m.connectionForm.fields[hit.index]
					masked = hit.index == connectionPassword
				}
				shown := *field
				if hit.kind == "field" {
					if masked {
						shown = editor{text: []rune(strings.Repeat("*", len(field.text))), cursor: field.cursor, anchor: field.anchor, left: field.left, singleLine: true}
					}
					shown.viewport(hit.input.width, 1)
				}
				field.left, field.top = shown.left, shown.top
				field.cursor = editorPosition(shown, hit.input, mouse.X, hit.input.y, false, masked)
				field.anchor = state.anchor
				field.verticalMotion, field.scrolling = false, false
				field.lastKind = ""
				return nil
			}
		}
		return nil
	}
	if !dialog.box.contains(mouse.X, mouse.Y) {
		return nil
	}
	if _, wheel := message.(tea.MouseWheelMsg); wheel {
		if busy {
			return nil
		}
		code := tea.KeyDown
		if mouse.Button == tea.MouseWheelUp {
			code = tea.KeyUp
		} else if mouse.Button != tea.MouseWheelDown {
			return nil
		}
		for range 3 {
			m.modalKey(tea.KeyPressMsg{Code: code})
		}
		return nil
	}
	if _, click := message.(tea.MouseClickMsg); !click || mouse.Button != tea.MouseLeft {
		return nil
	}
	state := &m.modalMouseState
	state.dragging = false
	if double && state.pressKind != "" && state.pressKind != m.modal.kind {
		state.pressKind = m.modal.kind
		return nil
	}
	state.pressKind = m.modal.kind
	for _, hit := range dialog.hits {
		if !hit.area.contains(mouse.X, mouse.Y) {
			continue
		}
		if busy {
			if !m.fileBusy && !m.profileBusy && (hit.kind == "close" || hit.kind == "button" && hit.index == connectionCancel && m.modal.kind == "connection") {
				return m.modalKey(tea.KeyPressMsg{Code: tea.KeyEsc})
			}
			return nil
		}
		switch hit.kind {
		case "close":
			return m.modalKey(tea.KeyPressMsg{Code: tea.KeyEsc})
		case "prompt", "field":
			field := &m.modal.input
			masked := false
			if hit.kind == "field" {
				m.connectionForm.index = hit.index
				field = &m.connectionForm.fields[hit.index]
				masked = hit.index == connectionPassword
			}
			shown := *field
			if hit.kind == "field" {
				if masked {
					shown = editor{text: []rune(strings.Repeat("*", len(field.text))), cursor: field.cursor, anchor: field.anchor, left: field.left, singleLine: true}
				}
				shown.viewport(hit.input.width, 1)
			}
			field.left, field.top = shown.left, shown.top
			position := editorPosition(shown, hit.input, mouse.X, mouse.Y, false, masked)
			anchor := position
			if mouse.Mod.Contains(tea.ModShift) {
				anchor = field.anchor
				if anchor < 0 {
					anchor = field.cursor
				}
				field.anchor = anchor
			} else {
				field.anchor = -1
			}
			field.cursor = position
			field.verticalMotion, field.scrolling = false, false
			field.lastKind = ""
			state.dragging, state.kind, state.field, state.anchor = true, m.modal.kind, hit.index, anchor
		case "checkbox":
			m.connectionForm.index = hit.index
			return m.connectionKey(tea.KeyPressMsg{Code: tea.KeySpace})
		case "button":
			if m.modal.kind == "connection" {
				m.connectionForm.index = hit.index
				return m.connectionKey(tea.KeyPressMsg{Code: tea.KeyEnter})
			}
			m.modal.index = hit.index
			return m.modalKey(tea.KeyPressMsg{Code: tea.KeyEnter})
		case "list":
			switch m.modal.kind {
			case "connections":
				m.profileIndex = hit.index
				if double || hit.index == len(m.profiles) {
					return m.connectionKey(tea.KeyPressMsg{Code: tea.KeyEnter})
				}
			case "database-names":
				m.databaseNameIndex = hit.index
				if double {
					return m.connectionKey(tea.KeyPressMsg{Code: tea.KeyEnter})
				}
			default:
				m.modal.index = hit.index
				if double {
					return m.modalKey(tea.KeyPressMsg{Code: tea.KeyEnter})
				}
			}
		}
		return nil
	}
	return nil
}
