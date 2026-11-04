package tui

import (
	"crypto/rand"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/dustinevan/scrunchie/lib/data/connections"
	"github.com/dustinevan/scrunchie/pkg/termtext"
)

const (
	connectionName = iota
	connectionHost
	connectionPort
	connectionDatabase
	connectionUser
	connectionPassword
	connectionPasswordEnv
	connectionSSLMode
	connectionSSLRootCert
	connectionSSLCert
	connectionSSLKey
	connectionRemember
	connectionAllowWrites
	connectionConnect
	connectionSave
	connectionCancel
	connectionDiscover
	connectionControlCount
)

var connectionLabels = [connectionRemember]string{
	"Name", "Host", "Port", "Database", "User", "Password", "Password env", "TLS mode", "CA certificate", "Client cert", "Client key",
}

type connectionForm struct {
	fields      [connectionRemember]editor
	index       int
	id          string
	remember    bool
	allowWrites bool
	fromManager bool
	// title is the form's heading, restored when the database-name picker closes.
	title string
}

type profileMsg struct {
	profiles     []connections.Profile
	selectedID   string
	afterConnect bool
	removed      bool
	// note replaces the status after a save that followed switching to an
	// already open connection.
	note string
	err  error
}

func (m *Model) openDatabases() {
	if m.profileBusy || m.connecting || m.discovering {
		m.status = "Wait for the current connection operation to finish"
		return
	}
	m.completion = completion{}
	m.modalMouseState = modalMouseState{}
	m.completeAfterCatalog = false
	m.connectionForm = connectionForm{}
	if len(m.profiles) == 0 {
		m.openConnectionForm(connections.FromEnvironment())
		return
	}
	m.profileIndex = max(0, min(m.profileIndex, len(m.profiles)))
	m.modal = modal{kind: "connections", title: "Databases"}
}

func (m *Model) openConnectionForm(profile connections.Profile) {
	fromManager := m.modal.kind == "connections"
	if m.modal.kind == "connection" || m.modal.kind == "database-names" {
		fromManager = m.connectionForm.fromManager
	}
	m.modalMouseState = modalMouseState{}
	m.completion = completion{}
	m.completeAfterCatalog = false
	if profile.ID == "" {
		profile.ID = rand.Text()
	}
	if profile.Name == "" {
		profile.Name = profile.Database + " @ " + profile.Host
	}
	values := [connectionRemember]string{
		profile.Name, profile.Host, profile.Port, profile.Database, profile.User, "", profile.PasswordEnv,
		profile.SSLMode, profile.SSLRootCert, profile.SSLCert, profile.SSLKey,
	}
	form := connectionForm{id: profile.ID, remember: true, allowWrites: profile.AllowWrites, fromManager: fromManager}
	for index, value := range values {
		form.fields[index] = newEditor(value)
		form.fields[index].singleLine = true
		form.fields[index].cursor = len(form.fields[index].text)
		if value != "" {
			form.fields[index].anchor = 0
		}
	}
	m.connectionForm = form
	title := "Add database"
	for _, saved := range m.profiles {
		if saved.ID == profile.ID {
			title = "Edit database"
			break
		}
	}
	if m.connectionForProfile(profile.ID) != nil {
		title = "Edit database"
	}
	m.connectionForm.title = title
	m.modal = modal{kind: "connection", title: title}
	if m.config.ProfilesError != "" {
		m.modal.error = "Saved databases could not be loaded. Turn Remember off to connect without saving."
		m.connectionForm.remember = false
	}
}

func (f connectionForm) profile() connections.Profile {
	return connections.Profile{
		ID: f.id, Name: string(f.fields[connectionName].text),
		Host: string(f.fields[connectionHost].text), Port: string(f.fields[connectionPort].text),
		Database: string(f.fields[connectionDatabase].text), User: string(f.fields[connectionUser].text),
		PasswordEnv: string(f.fields[connectionPasswordEnv].text), SSLMode: string(f.fields[connectionSSLMode].text),
		SSLRootCert: string(f.fields[connectionSSLRootCert].text), SSLCert: string(f.fields[connectionSSLCert].text),
		SSLKey: string(f.fields[connectionSSLKey].text), AllowWrites: f.allowWrites,
	}
}

func (m *Model) connectionKey(msg tea.KeyPressMsg) tea.Cmd {
	key := msg.String()
	if m.profileBusy {
		m.status = "Saving database settings…"
		return nil
	}
	if m.connecting {
		if key == "esc" && m.connectCancel != nil {
			m.connectCancel()
			m.status = "Canceling connection…"
		}
		return nil
	}
	if m.discovering {
		if key == "esc" && m.discoveryCancel != nil {
			m.discoveryCancel()
			m.status = "Canceling database discovery…"
		}
		return nil
	}
	if m.modal.kind == "database-names" {
		switch key {
		case "esc":
			m.modal = modal{kind: "connection", title: m.connectionForm.title}
			m.databaseNames = nil
		case "up":
			m.databaseNameIndex = max(0, m.databaseNameIndex-1)
		case "down":
			m.databaseNameIndex = min(max(0, len(m.databaseNames)-1), m.databaseNameIndex+1)
		case "tab":
			if len(m.databaseNames) > 0 {
				m.databaseNameIndex = (m.databaseNameIndex + 1) % len(m.databaseNames)
			}
		case "shift+tab":
			if len(m.databaseNames) > 0 {
				m.databaseNameIndex = (m.databaseNameIndex + len(m.databaseNames) - 1) % len(m.databaseNames)
			}
		case "home", "ctrl+home":
			m.databaseNameIndex = 0
		case "end", "ctrl+end":
			m.databaseNameIndex = max(0, len(m.databaseNames)-1)
		case "pgup", "pgdown":
			rows := m.connectionLayout(m.width, m.dimensions().bodyHeight).listRows()
			if key == "pgup" {
				m.databaseNameIndex = max(0, m.databaseNameIndex-rows)
			} else {
				m.databaseNameIndex = min(max(0, len(m.databaseNames)-1), m.databaseNameIndex+rows)
			}
		case "enter", "space":
			if m.databaseNameIndex < len(m.databaseNames) {
				field := newEditor(m.databaseNames[m.databaseNameIndex])
				field.singleLine = true
				field.cursor, field.anchor = len(field.text), 0
				m.connectionForm.fields[connectionDatabase] = field
				m.connectionForm.index = connectionDatabase
				m.modal = modal{kind: "connection", title: m.connectionForm.title}
				m.databaseNames = nil
				m.status = "Database selected in the form · Connect opens its session"
			}
		}
		return nil
	}
	if m.modal.kind == "confirm-remove-profile" {
		switch key {
		case "esc":
			m.modal = modal{kind: "connections", title: "Databases"}
		case "tab", "shift+tab", "left", "right", "up", "down":
			m.modal.index = 1 - m.modal.index
		case "home":
			m.modal.index = 0
		case "end":
			m.modal.index = 1
		case "enter", "space":
			if m.modal.index == 0 {
				m.modal = modal{kind: "connections", title: "Databases"}
				return nil
			}
			if m.profileIndex < 0 || m.profileIndex >= len(m.profiles) {
				return nil
			}
			profiles := append([]connections.Profile(nil), m.profiles[:m.profileIndex]...)
			profiles = append(profiles, m.profiles[m.profileIndex+1:]...)
			return m.saveProfiles(profileMsg{profiles: profiles, removed: true})
		}
		return nil
	}
	if m.modal.kind == "connections" {
		switch key {
		case "esc", "ctrl+b":
			m.modal = modal{}
		case "up":
			m.profileIndex = max(0, m.profileIndex-1)
		case "down":
			m.profileIndex = min(len(m.profiles), m.profileIndex+1)
		case "tab":
			m.profileIndex = (m.profileIndex + 1) % (len(m.profiles) + 1)
		case "shift+tab":
			m.profileIndex = (m.profileIndex + len(m.profiles)) % (len(m.profiles) + 1)
		case "home", "ctrl+home":
			m.profileIndex = 0
		case "end", "ctrl+end":
			m.profileIndex = len(m.profiles)
		case "pgup", "pgdown":
			rows := m.connectionLayout(m.width, m.dimensions().bodyHeight).listRows()
			if key == "pgup" {
				m.profileIndex = max(0, m.profileIndex-rows)
			} else {
				m.profileIndex = min(len(m.profiles), m.profileIndex+rows)
			}
		case "n":
			m.openConnectionForm(connections.FromEnvironment())
		case "enter", "space":
			if m.profileIndex == len(m.profiles) {
				m.openConnectionForm(connections.FromEnvironment())
				return nil
			}
			if m.profileIndex >= 0 && m.profileIndex < len(m.profiles) {
				return tea.Batch(m.connectProfile(m.profiles[m.profileIndex], "", false), m.spinner.Tick)
			}
		case "e":
			if m.profileIndex >= 0 && m.profileIndex < len(m.profiles) {
				m.openConnectionForm(m.profiles[m.profileIndex])
			}
		case "d", "delete":
			if m.profileIndex >= 0 && m.profileIndex < len(m.profiles) {
				m.modal = modal{kind: "confirm-remove-profile", title: "Remove saved database?", message: "Remove " + m.profiles[m.profileIndex].Name + " from remembered connections? This only removes local settings. It does not delete a PostgreSQL database or disconnect an open connection.", buttons: []string{"Cancel", "Remove saved entry"}}
			}
		}
		return nil
	}
	form := &m.connectionForm
	if key == "space" && form.index >= connectionConnect {
		key = "enter"
	}
	switch key {
	case "esc":
		fromManager := form.fromManager
		m.connectionForm = connectionForm{}
		m.modal = modal{}
		if fromManager {
			m.modal = modal{kind: "connections", title: "Databases"}
		}
		return nil
	case "tab":
		form.index = (form.index + 1) % connectionControlCount
		return nil
	case "shift+tab":
		form.index = (form.index + connectionControlCount - 1) % connectionControlCount
		return nil
	case "down":
		form.index = min(connectionControlCount-1, form.index+1)
		return nil
	case "up":
		form.index = max(0, form.index-1)
		return nil
	case "enter":
		switch form.index {
		case connectionConnect:
			profile := form.profile()
			if err := profile.Validate(); err != nil {
				m.modal.error = err.Error()
				return nil
			}
			return tea.Batch(m.connectProfile(profile, string(form.fields[connectionPassword].text), form.remember), m.spinner.Tick)
		case connectionSave:
			return m.saveProfile(form.profile(), false, "")
		case connectionCancel:
			return m.connectionKey(tea.KeyPressMsg{Code: tea.KeyEsc})
		case connectionDiscover:
			return tea.Batch(m.discoverDatabases(form.profile(), string(form.fields[connectionPassword].text)), m.spinner.Tick)
		default:
			form.index = (form.index + 1) % connectionControlCount
		}
		return nil
	case "space":
		if form.index == connectionRemember {
			form.remember = !form.remember
			return nil
		}
		if form.index == connectionAllowWrites {
			form.allowWrites = !form.allowWrites
			return nil
		}
	case "home", "end":
		if form.index >= connectionConnect {
			form.index = connectionConnect
			if key == "end" {
				form.index = connectionDiscover
			}
			return nil
		}
	case "left", "right":
		if form.index >= connectionConnect {
			if key == "left" {
				form.index = connectionConnect + (form.index-connectionConnect+3)%4
			} else {
				form.index = connectionConnect + (form.index-connectionConnect+1)%4
			}
			return nil
		}
	case "ctrl+v":
		m.status = "Use your terminal's native Paste command"
		return nil
	case "ctrl+c", "ctrl+x":
		if form.index == connectionPassword {
			m.status = "The password field cannot be copied"
			return nil
		}
		if form.index < connectionRemember {
			field := &form.fields[form.index]
			start, end := field.selection()
			if start != end {
				text := string(field.text[start:end])
				if key == "ctrl+x" {
					field.delete(start, end)
				}
				m.status = "Copy requested through terminal OSC52"
				return tea.SetClipboard(text)
			}
		}
		return nil
	}
	if form.index < connectionRemember {
		field := &form.fields[form.index]
		start, end := field.selection()
		if msg.Text != "" && len(field.text)-(end-start)+len([]rune(msg.Text)) > 4096 {
			m.modal.error = "Connection fields are limited to 4096 characters"
			return nil
		}
		field.key(msg, 1)
		if form.index == connectionPassword {
			field.undo, field.redo = nil, nil
		}
		m.modal.error = ""
	}
	return nil
}

func (m *Model) connectionPaste(text string) {
	form := &m.connectionForm
	if m.connecting || m.discovering || m.profileBusy || m.fileBusy || form.index >= connectionRemember {
		return
	}
	field := &form.fields[form.index]
	start, end := field.selection()
	if len(field.text)-(end-start)+len([]rune(text)) > 4096 {
		m.modal.error = "Connection fields are limited to 4096 characters"
		return
	}
	field.insert(text, "")
	if form.index == connectionPassword {
		field.undo, field.redo = nil, nil
	}
	m.modal.error = ""
}

func (m *Model) databaseNamesResult(message databaseNamesMsg) tea.Cmd {
	if message.serial != m.discoverySerial {
		return nil
	}
	m.discovering = false
	m.discoveryAttempt = nil
	if m.discoveryCancel != nil {
		m.discoveryCancel()
		m.discoveryCancel = nil
	}
	if m.quitIntent {
		return m.continueQuit()
	}
	if message.err != nil {
		m.modal.error = message.err.Error()
		m.status, m.statusError = message.err.Error(), true
		return nil
	}
	m.databaseNames, m.databaseNameIndex = message.names, 0
	m.modal = modal{kind: "database-names", title: "Choose database"}
	m.status, m.statusError = "Choose a database to fill the form · open connections are unchanged", false
	return nil
}

// profileConnected completes the form only after the session was adopted.
func (m *Model) profileConnected(profile connections.Profile, remember bool) tea.Cmd {
	m.connectionForm.fields[connectionPassword] = editor{}
	if remember {
		return m.saveProfile(profile, true, "")
	}
	if !m.modalHolds() {
		m.connectionForm = connectionForm{}
		m.modal = modal{}
	}
	return nil
}

func (m *Model) saveProfile(profile connections.Profile, afterConnect bool, note string) tea.Cmd {
	if err := profile.Validate(); err != nil {
		m.modal.error = err.Error()
		return nil
	}
	if strings.TrimSpace(profile.Name) == "" {
		m.modal.error = "Give this saved database a name"
		return nil
	}
	profiles := append([]connections.Profile(nil), m.profiles...)
	found := false
	for index := range profiles {
		if profiles[index].ID == profile.ID {
			profiles[index] = profile
			found = true
			break
		}
	}
	if !found {
		profiles = append(profiles, profile)
	}
	return m.saveProfiles(profileMsg{profiles: profiles, selectedID: profile.ID, afterConnect: afterConnect, note: note})
}

func (m *Model) saveProfiles(message profileMsg) tea.Cmd {
	if m.profileBusy || m.fileBusy {
		m.modal.error = "Wait for the current file operation to finish"
		return nil
	}
	if m.config.ProfilesError != "" {
		m.modal.error = "Saved databases could not be loaded; fix the settings file before replacing it. Connect with Remember off."
		return nil
	}
	path := m.config.ProfilesPath
	if path == "" {
		var err error
		path, err = connections.DefaultPath()
		if err != nil {
			m.modal.error = err.Error()
			return nil
		}
	}
	m.profileBusy = true
	m.modal.error = ""
	m.status, m.statusError = "Saving database settings…", false
	return func() tea.Msg {
		message.err = connections.Save(path, message.profiles)
		return message
	}
}

// profileResult adopts saved settings. Open connections are matched to saved
// entries by ID, never by comparing settings, so editing an open database
// relabels it instead of listing it twice. New settings reach its session
// only on Reconnect.
func (m *Model) profileResult(message profileMsg) tea.Cmd {
	m.profileBusy = false
	if message.err != nil {
		m.modal.error = message.err.Error()
		m.status, m.statusError = "Could not finish saving database settings: "+message.err.Error(), true
		return nil
	}
	m.profiles = message.profiles
	m.profileIndex = min(m.profileIndex, len(m.profiles))
	for index, profile := range m.profiles {
		if profile.ID == message.selectedID {
			m.profileIndex = index
			break
		}
	}
	var reconnect []string
	for _, c := range m.connections {
		for _, profile := range m.profiles {
			if c.profile.ID == "" || profile.ID != c.profile.ID || profile == c.profile {
				continue
			}
			settings, previous := profile, c.profile
			settings.Name, previous.Name = "", ""
			if settings != previous {
				reconnect = append(reconnect, termtext.Safe(profile.Name))
			}
			c.profile, c.name = profile, connectionLabel(profile, c.session, c.description)
			if profile.PasswordEnv != "" {
				c.password = ""
			}
		}
	}
	// A save that followed a background connect leaves any other dialog alone.
	if fromManager := m.connectionForm.fromManager; !m.modalHolds() || m.modal.kind == "confirm-remove-profile" {
		m.connectionForm = connectionForm{}
		m.modal = modal{}
		if (fromManager || message.removed) && !message.afterConnect {
			m.modal = modal{kind: "connections", title: "Databases"}
		}
	}
	m.status, m.statusError = "Database settings saved · passwords are never written", false
	if message.afterConnect {
		m.status = "Connected · database settings saved · password kept out of saved settings"
		if message.note != "" {
			m.status = message.note + " · database settings saved"
		}
	} else if message.removed {
		m.status = "Saved entry removed · PostgreSQL database and open connections are unchanged"
	}
	if len(reconnect) > 0 && message.note == "" {
		m.status += " · open connection " + strings.Join(reconnect, ", ") + " uses the new settings after Reconnect"
	}
	if m.quitIntent {
		return m.continueQuit()
	}
	return nil
}

func connectionFieldSizes(contentWidth int) (labelWidth, fieldOffset, fieldWidth int) {
	labelWidth = min(14, max(1, (contentWidth-4)/3))
	fieldOffset = labelWidth + 3
	if contentWidth < 10 {
		labelWidth, fieldOffset = 0, 0
	}
	return labelWidth, fieldOffset, max(1, contentWidth-fieldOffset)
}

func (m *Model) prepareConnectionForm() {
	box := m.centeredRect(max(1, min(88, m.width-4)), max(1, min(m.dimensions().bodyHeight, 22)))
	_, _, width := connectionFieldSizes(max(1, box.content().width))
	for index := range m.connectionForm.fields {
		field := &m.connectionForm.fields[index]
		if index == connectionPassword {
			masked := editor{text: []rune(strings.Repeat("*", len(field.text))), cursor: field.cursor, anchor: field.anchor, left: field.left, singleLine: true}
			masked.viewport(width, 1)
			field.left, field.top = masked.left, 0
		} else {
			field.viewport(width, 1)
		}
	}
}

func (m *Model) connectionLayout(width, height int) dialogLayout {
	boxWidth := max(1, min(88, width-4))
	boxHeight := max(1, min(height, 22))
	dialog := dialogLayout{box: m.centeredRect(boxWidth, boxHeight)}
	content := dialog.box.content()
	contentWidth := max(1, content.width)
	rows := max(1, content.height)
	var lines []string
	if m.modal.kind == "database-names" {
		if rows >= 4 {
			lines = append(lines, mutedStyle.Render("Enter fills Database · Esc returns to the form"), "")
		}
		visible := max(1, rows-len(lines))
		start := max(0, m.databaseNameIndex-visible+1)
		for index := start; index < min(len(m.databaseNames), start+visible); index++ {
			line := "  " + termtext.Safe(m.databaseNames[index])
			if index == m.databaseNameIndex {
				line = selectedStyle.Render(fitLine("› "+termtext.Safe(m.databaseNames[index]), contentWidth))
			}
			dialog.hits = append(dialog.hits, dialogHit{kind: "list", index: index, area: screenRect{x: content.x, y: content.y + len(lines), width: contentWidth, height: 1}})
			lines = append(lines, line)
		}
		if len(m.databaseNames) == 0 {
			lines = append(lines, "No connectable databases were returned. Esc goes back.")
		}
	} else if m.modal.kind == "connections" {
		visible := rows
		if rows >= 6 {
			lines = append(lines, mutedStyle.Render("Enter connect · n add · e edit · d remove saved entry"), "")
			visible = max(1, rows-5)
		}
		start := max(0, m.profileIndex-visible+1)
		for index := start; index < min(len(m.profiles)+1, start+visible); index++ {
			label := "+ Add database"
			if index < len(m.profiles) {
				profile := m.profiles[index]
				label = termtext.Safe(profile.Name) + "  " + mutedStyle.Render(termtext.Safe(profile.User+"@"+profile.Host+":"+profile.Port+"/"+profile.Database))
				if profile.AllowWrites {
					label += "  [writes]"
				}
				if m.connectionForProfile(profile.ID) != nil {
					label += "  [open]"
				}
			}
			if index == m.profileIndex {
				// selectedStyle styles each rune; strip the muted detail's
				// escape sequences so they cannot be split into visible text.
				label = selectedStyle.Render(fitLine("› "+ansi.Strip(label), contentWidth))
			} else {
				label = "  " + label
			}
			dialog.hits = append(dialog.hits, dialogHit{kind: "list", index: index, area: screenRect{x: content.x, y: content.y + len(lines), width: contentWidth, height: 1}})
			lines = append(lines, label)
		}
		if rows >= 6 {
			lines = append(lines, "", mutedStyle.Render("Saved settings contain no passwords. Esc returns to SQL."))
		}
		if m.config.ProfilesError != "" {
			lines = append(lines, errorStyle.Render(termtext.Safe(m.config.ProfilesError)))
		}
		if m.modal.error != "" {
			lines = append(lines, errorStyle.Render(termtext.Safe(m.modal.error)))
		}
	} else {
		form := m.connectionForm
		labelWidth, fieldOffset, fieldWidth := connectionFieldSizes(contentWidth)
		lines = append(lines, mutedStyle.Render("Connect opens a session. Save only remembers settings."), "")
		for index, label := range connectionLabels {
			field := form.fields[index]
			if index == connectionPassword {
				field = editor{text: []rune(strings.Repeat("*", len(field.text))), cursor: field.cursor, anchor: field.anchor, left: field.left, singleLine: true}
			}
			field.viewport(fieldWidth, 1)
			prefix := "  "
			if index == form.index {
				prefix = "› "
			}
			prefix += fitLine(label, labelWidth) + " "
			if fieldOffset == 0 {
				prefix = ""
			}
			dialog.hits = append(dialog.hits, dialogHit{kind: "field", index: index,
				area:  screenRect{x: content.x, y: content.y + len(lines), width: contentWidth, height: 1},
				input: screenRect{x: content.x + fieldOffset, y: content.y + len(lines), width: fieldWidth, height: 1}})
			lines = append(lines, prefix+field.render(fieldWidth, 1, index == form.index, false))
		}
		controlPrefix := "  "
		if contentWidth < 10 {
			controlPrefix = ""
		}
		checkbox := "[ ] Remember in ~/.scrunch (no password)"
		if form.remember {
			checkbox = "[x] Remember in ~/.scrunch (no password)"
		}
		if form.index == connectionRemember {
			checkbox = cursorStyle.Render(checkbox)
		}
		dialog.hits = append(dialog.hits, dialogHit{kind: "checkbox", index: connectionRemember, area: screenRect{x: content.x, y: content.y + len(lines), width: contentWidth, height: 1}})
		lines = append(lines, controlPrefix+checkbox)
		writes := "[ ] Allow writes (otherwise read-only)"
		if form.allowWrites {
			writes = "[x] Allow writes (otherwise read-only)"
		}
		if form.index == connectionAllowWrites {
			writes = cursorStyle.Render(writes)
		}
		dialog.hits = append(dialog.hits, dialogHit{kind: "checkbox", index: connectionAllowWrites, area: screenRect{x: content.x, y: content.y + len(lines), width: contentWidth, height: 1}})
		lines = append(lines, controlPrefix+writes)
		var buttons []string
		for index, label := range []string{"Connect", "Save", "Cancel", "List databases"} {
			label = " " + label + " "
			if form.index == connectionConnect+index {
				label = cursorStyle.Render(label)
			} else {
				label = mutedStyle.Render(label)
			}
			buttons = append(buttons, label)
		}
		buttonLine := strings.Join(buttons, "  ")
		if contentWidth < 50 {
			buttonIndex := max(0, min(len(buttons)-1, form.index-connectionConnect))
			buttonLine = buttons[buttonIndex] + " ←→"
			dialog.hits = append(dialog.hits, dialogHit{kind: "button", index: connectionConnect + buttonIndex,
				area: screenRect{x: content.x + len(controlPrefix), y: content.y + len(lines), width: lipgloss.Width(buttons[buttonIndex]), height: 1}})
		} else {
			x := content.x + len(controlPrefix)
			for index, button := range buttons {
				buttonWidth := lipgloss.Width(button)
				dialog.hits = append(dialog.hits, dialogHit{kind: "button", index: connectionConnect + index,
					area: screenRect{x: x, y: content.y + len(lines), width: buttonWidth, height: 1}})
				x += buttonWidth + 2
			}
		}
		lines = append(lines, controlPrefix+buttonLine)
		if form.index == connectionPasswordEnv || form.index == connectionPassword {
			lines = append(lines, mutedStyle.Render("Use Password OR Password env; the env field is a variable name."))
		} else if form.index == connectionSSLMode {
			lines = append(lines, mutedStyle.Render("TLS: disable, allow, prefer, require, verify-ca, verify-full"))
		} else if form.index == connectionDiscover {
			lines = append(lines, mutedStyle.Render("Temporarily connects to postgres on this host to list databases."))
		} else if form.index == connectionAllowWrites {
			lines = append(lines, mutedStyle.Render("Unchecked sessions are read-only. Writable ones show a pink header."))
		} else {
			lines = append(lines, mutedStyle.Render("Tab/Shift+Tab moves · Enter advances · Space toggles a checkbox"))
		}
		if m.modal.error != "" {
			lines = append(lines, errorStyle.Render(termtext.Safe(m.modal.error)))
		}
		if m.connecting {
			lines = append(lines, m.spinner.View()+" Connecting… Ctrl+C cancels")
		} else if m.discovering {
			lines = append(lines, m.spinner.View()+" Listing databases… Ctrl+C cancels")
		} else if m.profileBusy {
			lines = append(lines, m.spinner.View()+" Saving settings…")
		}
		focusRow := min(form.index, connectionConnect) + 2
		if m.modal.error != "" || m.connecting || m.discovering || m.profileBusy {
			focusRow = len(lines) - 1
		}
		start := max(0, min(focusRow-rows+1, len(lines)-rows))
		lines = lines[start:min(len(lines), start+rows)]
		for index := range dialog.hits {
			dialog.hits[index].area.y -= start
			dialog.hits[index].input.y -= start
		}
	}
	dialog.lines = lines
	dialog.clipHits()
	dialog.addClose(m.modal.title)
	return dialog
}

func (m *Model) connectionsView(width, height int) string {
	dialog := m.connectionLayout(width, height)
	box := panel(dialog.title, strings.Join(dialog.lines, "\n"), dialog.box.width, dialog.box.height, true)
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, box)
}

// connectionSummary describes the current buffer's connection for the header.
func (m *Model) connectionSummary() string {
	if m.config.Demo {
		return "Offline demo"
	}
	c := m.conn()
	switch {
	case c == nil && len(m.connections) == 0:
		return "Not connected · Ctrl+B add a database"
	case c == nil:
		return bufferName(m.buffers[m.bufferIndex]) + " · no connection · Ctrl+P → Change buffer connection"
	case c.profile.ID != "":
		return fmt.Sprintf("%s · %s · %s", termtext.Safe(c.profile.Database), termtext.Safe(c.profile.Name), termtext.Safe(c.description))
	}
	return ansi.Truncate(termtext.Safe(c.description), max(1, m.width), "…")
}
