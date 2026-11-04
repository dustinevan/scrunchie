package tui

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/dustinevan/scrunchie/lib/data/postgres"
	"github.com/dustinevan/scrunchie/pkg/termtext"
)

// structureView is a read-only snapshot of one relation's catalog definition,
// read on connection conn. Like the value inspector it covers the workspace;
// Esc returns to its origin.
type structureView struct {
	active    bool
	target    postgres.Relation
	conn      uint64
	origin    pane
	serial    uint64 // the catalog request that fills this view
	queued    bool
	loading   bool
	loaded    bool
	data      postgres.Structure
	err       string
	collapsed map[string]bool
	index     int
	top       int
	left      int
	scrolling bool
}

// structureLine is a section header or one of its items. Text is the raw
// catalog text: rendering escapes it, and copying keeps it exact.
type structureLine struct {
	section string
	header  bool
	text    string
}

// ddlRequest is DDL waiting for connection conn. Its reply takes over the
// editor only if no input arrived after the request.
type ddlRequest struct {
	label       string
	relation    uint32
	routine     uint32
	conn        uint64
	serial      uint64
	queued      bool
	interaction uint64
}

// catalogBlocked explains why c's catalog metadata cannot be read right now.
func (m *Model) catalogBlocked(c *connection) bool {
	switch {
	case m.config.Demo:
		m.status, m.statusError = "Demo catalog · no database connection", false
	case c == nil || c.session == nil:
		m.status, m.statusError = "Connect a database to read its catalog", false
	case m.connecting && m.reconnectAttempt != nil && m.reconnectAttempt.target == c.id:
		m.status, m.statusError = "Wait for the connection to finish before reading the catalog", false
	case !c.connected:
		m.status, m.statusError = "Connection closed · Enter on the database reconnects", false
	default:
		return false
	}
	return true
}

// openStructure handles `d` on c's tree: tables, views, and their columns open
// the structure view; functions and sequences, which have little else to
// browse, open their DDL instead.
func (m *Model) openStructure(c *connection, node catalogNode) tea.Cmd {
	if node.kind == "function" || node.kind == "relation" && node.relation.Kind == "sequence" {
		return m.requestDDL(c, node)
	}
	if node.kind != "relation" && node.kind != "column" {
		m.status, m.statusError = "Select a table, view, column, sequence, or function to show its structure", false
		return nil
	}
	if m.catalogBlocked(c) {
		return nil
	}
	m.structure = structureView{active: true, target: node.relation, conn: c.id, origin: m.focus, collapsed: make(map[string]bool)}
	m.completion = completion{}
	m.completeAfterCatalog = false
	return m.loadStructure()
}

// loadStructure reads the open view's relation, or waits until SQL and other
// catalog work on its connection finish, like catalog expansion.
func (m *Model) loadStructure() tea.Cmd {
	v := &m.structure
	v.loading, v.err = true, ""
	c := m.connectionFor(v.conn)
	if c == nil || c.session == nil || !c.connected {
		v.loading, v.queued = false, false
		v.err = "Connection closed · Enter on the database reconnects"
		return nil
	}
	if c.query.running || c.catalogBusy {
		v.queued = true
		m.status, m.statusError = "Structure request queued until "+termtext.Safe(c.name)+" is idle", false
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	c.catalogCancel = cancel
	c.catalogBusy = true
	m.serial++
	c.catalogSerial = m.serial
	v.serial, v.queued = c.catalogSerial, false
	id, serial, session, oid := c.id, c.catalogSerial, c.session, v.target.OID
	command := func() tea.Msg {
		structure, err := session.Structure(ctx, oid)
		return catalogMsg{conn: id, serial: serial, kind: "structure", structure: structure, err: err}
	}
	return tea.Batch(command, m.spinner.Tick)
}

// requestDDL prepares DDL for an object of c's catalog. Columns use their relation.
func (m *Model) requestDDL(c *connection, node catalogNode) tea.Cmd {
	request := ddlRequest{label: node.identifier(), interaction: m.interaction}
	switch node.kind {
	case "function":
		request.routine = node.routine.OID
	case "relation", "column":
		request.label, request.relation = node.relation.QualifiedName(), node.relation.OID
	default:
		m.status, m.statusError = "Select a table, view, sequence, or function to show its DDL", false
		return nil
	}
	if m.catalogBlocked(c) {
		return nil
	}
	request.conn = c.id
	m.ddl = request
	return m.loadDDL()
}

func (m *Model) loadDDL() tea.Cmd {
	c := m.connectionFor(m.ddl.conn)
	if c == nil || c.session == nil || !c.connected {
		m.ddl = ddlRequest{}
		m.status, m.statusError = "Connection closed · Enter on the database reconnects", false
		return nil
	}
	if c.query.running || c.catalogBusy {
		m.ddl.queued = true
		m.status, m.statusError = "DDL request queued until "+termtext.Safe(c.name)+" is idle", false
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	c.catalogCancel = cancel
	c.catalogBusy = true
	m.serial++
	c.catalogSerial = m.serial
	m.ddl.serial, m.ddl.queued = c.catalogSerial, false
	id, serial, session, request := c.id, c.catalogSerial, c.session, m.ddl
	command := func() tea.Msg {
		if request.routine != 0 {
			text, err := session.RoutineDDL(ctx, request.routine)
			return catalogMsg{conn: id, serial: serial, kind: "ddl", text: text, err: err}
		}
		structure, err := session.Structure(ctx, request.relation)
		if err != nil {
			return catalogMsg{conn: id, serial: serial, kind: "ddl", err: err}
		}
		return catalogMsg{conn: id, serial: serial, kind: "ddl", text: postgres.DDL(structure)}
	}
	return tea.Batch(command, m.spinner.Tick)
}

// structureResult applies a structure or DDL reply from c. A reply for a
// structure view that was closed or reopened is dropped. DDL opens in a new
// buffer bound to c.
func (m *Model) structureResult(c *connection, msg catalogMsg) {
	switch msg.kind {
	case "structure":
		v := &m.structure
		if !v.active || v.conn != c.id || v.serial != msg.serial {
			return
		}
		v.loading = false
		if msg.err != nil {
			v.err = msg.err.Error()
			return
		}
		v.data, v.loaded, v.err = msg.structure, true, ""
	case "ddl":
		request := m.ddl
		if request.conn != c.id || request.serial != msg.serial {
			return
		}
		m.ddl = ddlRequest{}
		if msg.err != nil {
			m.status, m.statusError = "DDL: "+msg.err.Error(), true
			return
		}
		if request.interaction == m.interaction && m.modal.kind == "" {
			m.addBuffer(msg.text, "", true, c.id)
			m.status, m.statusError = "DDL for "+request.label+" is in a new buffer · it has not been executed", false
			return
		}
		// Input arrived while waiting; do not move the user's cursor or focus.
		m.nextBuffer++
		b := buffer{id: m.nextBuffer, editor: newEditor(msg.text), conn: c.id, results: newResults()}
		b.saved = string(b.editor.text)
		m.buffers = append(m.buffers, b)
		m.status, m.statusError = fmt.Sprintf("DDL for %s is ready in SQL %d · it has not been executed", request.label, b.id), false
	}
}

// structureDDL opens DDL for the structure being viewed, from its snapshot,
// in a buffer bound to the connection it was read from.
func (m *Model) structureDDL() tea.Cmd {
	if !m.structure.loaded {
		m.status, m.statusError = "Wait for the structure to load before generating DDL", false
		return nil
	}
	label := m.structure.data.Qualified
	conn := uint64(0)
	if c := m.connectionFor(m.structure.conn); c != nil {
		conn = c.id
	}
	m.addBuffer(postgres.DDL(m.structure.data), "", true, conn)
	m.status, m.statusError = "DDL for "+label+" is in a new buffer · it has not been executed", false
	return nil
}

// structureLines lists headers and items in display order. Collapsed
// sections contribute only their header unless all is set.
func (m *Model) structureLines(all bool) []structureLine {
	v := m.structure
	if !v.loaded {
		return nil
	}
	s := v.data
	kind := s.Relation.Kind
	table := kind == "table" || kind == "partitioned table"

	var columns [][]string
	for _, column := range s.Columns {
		nullability := "NULL"
		if column.NotNull {
			nullability = "NOT NULL"
		}
		var extras []string
		if column.Options != "" {
			extras = append(extras, "OPTIONS ("+column.Options+")")
		}
		if column.Collation != "" {
			extras = append(extras, "COLLATE "+column.Collation)
		}
		switch {
		case column.Generated != "" && column.Virtual:
			extras = append(extras, "GENERATED ALWAYS AS ("+column.Generated+") VIRTUAL")
		case column.Generated != "":
			extras = append(extras, "GENERATED ALWAYS AS ("+column.Generated+") STORED")
		case column.Identity != "" && column.IdentityOptions != "":
			extras = append(extras, "GENERATED "+column.Identity+" AS IDENTITY ("+column.IdentityOptions+")")
		case column.Identity != "":
			extras = append(extras, "GENERATED "+column.Identity+" AS IDENTITY")
		case column.Default != "":
			extras = append(extras, "DEFAULT "+column.Default)
		}
		if column.Inherited {
			extras = append(extras, "(inherited)")
		}
		if column.Comment != "" {
			extras = append(extras, "-- "+column.Comment)
		}
		columns = append(columns, []string{column.Name, column.Type, nullability, strings.Join(extras, "  ")})
	}

	var keys, foreign, checks [][]string
	for _, constraint := range s.Constraints {
		text := constraint.Definition
		if constraint.Kind == "foreign key" {
			text = "(" + constraint.Columns + ") → " + constraint.Table + " (" + constraint.References + ")  ON DELETE " + constraint.OnDelete + "  ON UPDATE " + constraint.OnUpdate
			if constraint.MatchFull {
				text += "  MATCH FULL"
			}
			if constraint.Deferrable != "" {
				text += "  " + constraint.Deferrable
			}
			if !constraint.Validated {
				text += "  NOT VALID"
			}
		}
		if constraint.Inherited {
			text += "  (inherited)"
		}
		switch constraint.Kind {
		case "foreign key":
			foreign = append(foreign, []string{constraint.Name, text})
		case "check":
			checks = append(checks, []string{constraint.Name, text})
		default:
			keys = append(keys, []string{constraint.Name, text})
		}
	}
	var incoming [][]string
	for _, constraint := range s.ReferencedBy {
		text := constraint.Table + " (" + constraint.Columns + ") → (" + constraint.References + ")  ON DELETE " + constraint.OnDelete + "  ON UPDATE " + constraint.OnUpdate
		if !constraint.Validated {
			text += "  NOT VALID"
		}
		incoming = append(incoming, []string{constraint.Name, text})
	}
	var indexes [][]string
	for _, index := range s.Indexes {
		var flags []string
		switch {
		case index.Primary:
			flags = append(flags, "primary")
		case index.Unique:
			flags = append(flags, "unique")
		}
		if index.Predicate != "" {
			flags = append(flags, "partial")
		}
		if !index.Valid {
			flags = append(flags, "INVALID")
		}
		if index.Size != "" {
			flags = append(flags, index.Size)
		}
		indexes = append(indexes, []string{index.Name, strings.Join(flags, " · "), index.Definition})
	}
	var triggers [][]string
	for _, trigger := range s.Triggers {
		state := trigger.Enabled
		if trigger.Inherited {
			state += " (inherited)"
		}
		triggers = append(triggers, []string{trigger.Name, state, trigger.Definition})
	}
	var definition [][]string
	if s.Definition != "" {
		for _, line := range strings.Split(strings.TrimRight(s.Definition, "\n"), "\n") {
			definition = append(definition, []string{line})
		}
	}

	details := [][]string{{"Owner", s.Owner}}
	if kind != "view" {
		details = append(details, []string{"Rows", structureRows(s.RowEstimate) + " · pg_class.reltuples, updated by ANALYZE"})
	}
	if s.TotalSize != "" {
		details = append(details, []string{"Total size", s.TotalSize + " (table, indexes, and TOAST)"})
	} else if s.SizesSkipped && kind != "view" && kind != "foreign table" {
		details = append(details, []string{"Total size", "not read inside a transaction; it would wait for table locks"})
	}
	if s.Comment != "" {
		details = append(details, []string{"Comment", s.Comment})
	}
	if s.Persistence != "" && s.Persistence != "permanent" {
		details = append(details, []string{"Persistence", s.Persistence})
	}
	if s.PartitionKey != "" {
		details = append(details, []string{"Partition key", s.PartitionKey})
	}
	if s.PartitionOf != "" {
		details = append(details, []string{"Partition of", s.PartitionOf + " " + s.PartitionBound})
	}
	if s.Inherits != "" {
		details = append(details, []string{"Inherits", s.Inherits})
	}
	if s.Options != "" {
		details = append(details, []string{"Options", s.Options})
	}
	if s.RowSecurity {
		state := "enabled"
		if s.ForceSecurity {
			state += ", forced for the owner"
		}
		details = append(details, []string{"Row security", state})
	}
	if s.Server != "" {
		server := s.Server
		if s.ServerOptions != "" {
			server += " OPTIONS (" + s.ServerOptions + ")"
		}
		details = append(details, []string{"Foreign server", server})
	}
	if kind == "materialized view" && !s.Populated {
		details = append(details, []string{"Populated", "no; REFRESH MATERIALIZED VIEW loads it"})
	}

	sections := []struct {
		title   string
		rows    [][]string
		show    bool
		counted bool
	}{
		{"Columns", columns, true, true},
		{"Primary key & unique", keys, table, true},
		{"Foreign keys", foreign, table, true},
		{"Referenced by", incoming, table, true},
		{"Indexes", indexes, table || kind == "materialized view", true},
		{"Check constraints", checks, table, true},
		{"Triggers", triggers, table, true},
		{"Definition", definition, false, false},
		{"Details", details, true, false},
	}
	var lines []structureLine
	for _, section := range sections {
		if len(section.rows) == 0 && !section.show {
			continue
		}
		header := section.title
		if section.counted {
			header += " (" + strconv.Itoa(len(section.rows)) + ")"
		}
		lines = append(lines, structureLine{section: section.title, header: true, text: header})
		if !all && v.collapsed[section.title] {
			continue
		}
		// Pad every cell but the last to its column's widest escaped width,
		// capped so one long name cannot push the definitions off screen.
		var widths []int
		for _, row := range section.rows {
			for i := 0; i < len(row)-1; i++ {
				if i == len(widths) {
					widths = append(widths, 0)
				}
				widths[i] = max(widths[i], min(32, lipgloss.Width(termtext.Safe(row[i]))))
			}
		}
		for _, row := range section.rows {
			var text strings.Builder
			for i, cell := range row {
				text.WriteString(cell)
				if i < len(row)-1 {
					text.WriteString(strings.Repeat(" ", max(0, widths[i]-lipgloss.Width(termtext.Safe(cell)))+2))
				}
			}
			lines = append(lines, structureLine{section: section.title, text: strings.TrimRight(text.String(), " ")})
		}
	}
	return lines
}

// structureRows labels pg_class.reltuples, which is only an estimate.
func structureRows(estimate float64) string {
	if estimate < 0 {
		return "row count unknown (not analyzed)"
	}
	digits := strconv.FormatInt(int64(math.Round(estimate)), 10)
	var grouped strings.Builder
	for i, digit := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			grouped.WriteByte(',')
		}
		grouped.WriteRune(digit)
	}
	return "~" + grouped.String() + " rows (estimate)"
}

// structureRowCount is the list height inside the panel: border and title,
// the summary line, and a blank line precede it.
func (m *Model) structureRowCount() int {
	return max(1, m.dimensions().bodyHeight-5)
}

func (m *Model) prepareStructure() {
	v := &m.structure
	count := len(m.structureLines(false))
	rows := m.structureRowCount()
	v.index = max(0, min(v.index, count-1))
	if v.scrolling {
		v.top = max(0, min(v.top, count-rows))
		return
	}
	v.top = max(0, min(v.top, v.index))
	if v.index >= v.top+rows {
		v.top = v.index - rows + 1
	}
}

func (m *Model) structureKey(msg tea.KeyPressMsg) tea.Cmd {
	v := &m.structure
	key := msg.String()
	v.scrolling = false
	switch key {
	case "esc":
		origin := v.origin
		m.structure = structureView{}
		m.focusPane(origin)
		return nil
	case "D":
		return m.structureDDL()
	case "/":
		m.beginSearch()
		return nil
	case "ctrl+r":
		if v.loading {
			m.status, m.statusError = "The structure is already loading", false
			return nil
		}
		if m.catalogBlocked(m.connectionFor(v.conn)) {
			return nil
		}
		v.loaded = false
		return m.loadStructure()
	}
	lines := m.structureLines(false)
	if len(lines) == 0 {
		return nil
	}
	v.index = min(v.index, len(lines)-1)
	line := lines[v.index]
	switch key {
	case "up":
		v.index = max(0, v.index-1)
	case "down":
		v.index = min(len(lines)-1, v.index+1)
	case "pgup":
		v.index = max(0, v.index-m.structureRowCount())
	case "pgdown":
		v.index = min(len(lines)-1, v.index+m.structureRowCount())
	case "home", "ctrl+home":
		v.index = 0
	case "end", "ctrl+end":
		v.index = len(lines) - 1
	case "alt+left":
		v.left = max(0, v.left-8)
	case "alt+right":
		v.left += 8
	case "enter", "space":
		if line.header {
			v.collapsed[line.section] = !v.collapsed[line.section]
		} else {
			m.status, m.statusError = "Ctrl+C copies this line · Enter on a section header collapses it", false
		}
	case "left":
		if line.header {
			v.collapsed[line.section] = true
		} else {
			for v.index > 0 && !lines[v.index].header {
				v.index--
			}
		}
	case "right":
		if line.header && v.collapsed[line.section] {
			v.collapsed[line.section] = false
		} else if line.header && v.index+1 < len(lines) && !lines[v.index+1].header {
			v.index++
		}
	}
	return nil
}

// copyStructureLine copies the selected item, or a header's whole section.
func (m *Model) copyStructureLine() tea.Cmd {
	lines := m.structureLines(false)
	if len(lines) == 0 {
		m.status, m.statusError = "Nothing to copy until the structure loads", false
		return nil
	}
	selected := lines[min(m.structure.index, len(lines)-1)]
	text := selected.text
	if selected.header {
		var items []string
		for _, line := range m.structureLines(true) {
			if line.section == selected.section && !line.header {
				items = append(items, line.text)
			}
		}
		text = strings.Join(items, "\n")
	}
	if len(text) > 4<<20 {
		m.status, m.statusError = "Text exceeds the 4 MiB clipboard limit", true
		return nil
	}
	m.status, m.statusError = fmt.Sprintf("Copy requested through terminal OSC52 · %d bytes", len(text)), false
	return tea.SetClipboard(text)
}

func (m *Model) structureMouse(pointer tea.Mouse, click, wheel, double bool) tea.Cmd {
	v := &m.structure
	if click && m.width >= 4 && m.dimensions().bodyHeight >= 4 && pointer.Y == 2 && pointer.X >= 3 && pointer.X < 10 {
		origin := v.origin
		m.structure = structureView{}
		m.focusPane(origin)
		return nil
	}
	area := (screenRect{0, 1, m.width, m.dimensions().bodyHeight}).content()
	area.y += 2
	area.height = m.structureRowCount()
	lines := m.structureLines(false)
	if wheel {
		delta := 3
		if pointer.Button == tea.MouseWheelUp || pointer.Button == tea.MouseWheelLeft {
			delta = -3
		}
		v.scrolling = true
		if pointer.Button == tea.MouseWheelLeft || pointer.Button == tea.MouseWheelRight || pointer.Mod.Contains(tea.ModShift) {
			v.left = max(0, v.left+delta)
		} else {
			v.top = max(0, min(max(0, len(lines)-area.height), v.top+delta))
		}
		return nil
	}
	if click && area.contains(pointer.X, pointer.Y) {
		position := v.top + pointer.Y - area.y
		if position >= len(lines) {
			return nil
		}
		v.index, v.scrolling = position, false
		if lines[position].header && (double || pointer.X == area.x) {
			v.collapsed[lines[position].section] = !v.collapsed[lines[position].section]
		}
	}
	return nil
}

func (m *Model) structurePanel(width, height int) string {
	v := m.structure
	meta := v.target.QualifiedName() + " · " + v.target.Kind
	if v.loaded {
		meta = v.data.Qualified + " · " + v.data.Relation.Kind
		if v.data.Relation.Kind != "view" {
			meta += " · " + structureRows(v.data.RowEstimate)
		}
		if v.data.TotalSize != "" {
			meta += " · " + v.data.TotalSize
		}
	}
	lines := []string{mutedStyle.Render(termtext.Safe(meta)), ""}
	switch {
	case v.loading && v.queued:
		lines = append(lines, m.spinner.View()+" Waiting for the session to finish before reading the catalog…")
	case v.loading:
		lines = append(lines, m.spinner.View()+" Loading structure…")
	case v.err != "":
		lines = append(lines, errorStyle.Render(termtext.Safe(v.err)), "", mutedStyle.Render("Ctrl+R retries · Esc returns"))
	default:
		visible := m.structureLines(false)
		for position := v.top; position < min(len(visible), v.top+max(1, height-5)); position++ {
			line := visible[position]
			text := "  " + termtext.Safe(line.text)
			if line.header {
				marker := "▾ "
				if v.collapsed[line.section] {
					marker = "▸ "
				}
				text = marker + line.text
			}
			text = ansi.Cut(text, v.left, v.left+max(1, width-2))
			if position == v.index {
				text = selectedStyle.Render(fitLine(text, max(1, width-2)))
			} else if line.header {
				text = accentStyle.Render(text)
			}
			lines = append(lines, text)
		}
	}
	return panel("× Close · STRUCTURE", strings.Join(lines, "\n"), width, height, true)
}
