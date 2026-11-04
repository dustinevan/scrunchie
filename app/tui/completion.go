package tui

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/dustinevan/scrunchie/lib/data/postgres"
	"github.com/dustinevan/scrunchie/lib/logic/sqledit"
)

const (
	completionShown  = 200    // suggestions kept for one popup
	completionScan   = 5000   // candidates gathered before ranking
	completionBefore = 64_000 // runes analyzed before the cursor
	completionAfter  = 16_000 // runes analyzed after the cursor
	completionCached = 256    // relations whose columns stay cached
	completionBatch  = 8      // relations whose columns load per request
	completionStale  = 5 * time.Minute
)

var sqlWords = []string{
	"ALL", "ALTER", "AND", "AS", "ASC", "BEGIN", "BETWEEN", "BY", "CASE", "COALESCE", "COMMIT", "COUNT", "CREATE", "CROSS", "CURRENT_DATE", "CURRENT_TIMESTAMP", "DELETE", "DESC", "DISTINCT", "DROP", "ELSE", "END", "EXCEPT", "EXISTS", "EXPLAIN", "FALSE", "FETCH", "FILTER", "FIRST", "FROM", "FULL", "GROUP", "HAVING", "ILIKE", "IN", "INNER", "INSERT", "INTERSECT", "INTERVAL", "INTO", "IS", "JOIN", "JSONB_AGG", "JSONB_BUILD_OBJECT", "LATERAL", "LEFT", "LIKE", "LIMIT", "MATERIALIZED", "NATURAL", "NOT", "NULL", "NULLS", "OFFSET", "ON", "ONLY", "OR", "ORDER", "OUTER", "OVER", "PARTITION", "RECURSIVE", "RETURNING", "RIGHT", "ROLLBACK", "SELECT", "SET", "TABLE", "THEN", "TRUE", "TRUNCATE", "UNION", "UPDATE", "USING", "VALUES", "WHEN", "WHERE", "WINDOW", "WITH",
}

// completionCatalog caches names for SQL completion apart from the database
// tree, so suggestions never depend on which branches happen to be expanded.
// Each connection keeps its own; it resets when that connection's session
// changes, as on Reconnect.
type completionCatalog struct {
	session    *postgres.Session
	serial     uint64
	history    uint64 // historySerial whose statements were checked for DDL
	loaded     bool   // names were read for this session
	loadedAt   time.Time
	failed     bool // reading names failed; only explicit completion retries
	searchPath []string
	schemas    []string
	relations  map[string][]postgres.Relation
	listed     map[string]bool // schemas whose relation lists are loaded
	columns    map[uint32][]postgres.Column
	wait       completionWait
}

// completionWait remembers what a catalog load is for. The popup refreshes
// only if the buffer, its revision, and the cursor are unchanged on arrival.
type completionWait struct {
	active   bool
	buffer   uint64
	revision uint64
	cursor   int
	force    bool
}

type completionCatalogMsg struct {
	conn      uint64
	serial    uint64
	session   *postgres.Session
	names     *postgres.CompletionNames
	schema    string
	relations []postgres.Relation
	requested []uint32
	columns   map[uint32][]postgres.Column
	err       error
}

type completionItem struct {
	text   string // replacement, quoted as needed
	detail string
	rank   int
	key    string // lowercase sort key
}

// completionNeeds names catalog data missing from the current suggestions.
type completionNeeds struct {
	schema    string
	relations []uint32
}

// completionIndex joins the completion catalog with whatever the database
// tree has loaded, so tree expansion and the offline demo also complete.
type completionIndex struct {
	searchPath []string
	schemas    []string
	relations  map[string][]postgres.Relation
	listed     map[string]bool
	columns    map[uint32][]postgres.Column
}

type completionBuilder struct {
	word  string // lowercase typed prefix
	seen  map[string]bool
	items []completionItem
}

// completeWord offers completions for the word at the cursor. Typing calls it
// with force false: the popup then needs two typed characters or a qualifier
// and stays closed when the only suggestion is the word already typed.
func (m *Model) completeWord(force bool) tea.Cmd {
	b := &m.buffers[m.bufferIndex]
	e := &b.editor
	c := m.conn()
	m.completion = completion{}
	m.completeAfterCatalog = false
	if c != nil {
		c.completion.wait = completionWait{}
	}
	from := max(0, e.cursor-completionBefore)
	for from > 0 && from < e.cursor && e.text[from-1] != '\n' {
		from++
	}
	to := min(len(e.text), e.cursor+completionAfter)
	text := string(e.text[from:to])
	cursor := 0
	for _, r := range e.text[from:e.cursor] {
		cursor += utf8.RuneLen(r)
	}
	site := sqledit.Analyze(text, cursor)
	if site.Clause == sqledit.NoClause || !force && len(site.Qualifier) == 0 && utf8.RuneCountInString(site.Word) < 2 {
		return nil
	}
	m.syncCompletionCatalog(c)
	x := m.completionIndex(c)
	builder := completionBuilder{word: strings.ToLower(site.Word), seen: make(map[string]bool)}
	needs := builder.collect(site, x)
	items := builder.items
	sort.SliceStable(items, func(i, j int) bool {
		exactI, exactJ := sameWord(items[i].text, site.Word), sameWord(items[j].text, site.Word)
		if exactI != exactJ {
			return exactI
		}
		if items[i].rank != items[j].rank {
			return items[i].rank < items[j].rank
		}
		if items[i].key != items[j].key {
			return items[i].key < items[j].key
		}
		return items[i].text < items[j].text
	})
	items = items[:min(len(items), completionShown)]
	start := from + utf8.RuneCountInString(text[:site.Start])
	end := from + utf8.RuneCountInString(text[:site.End])
	if len(items) > 0 && (force || len(items) > 1 || !sameWord(items[0].text, string(e.text[start:e.cursor]))) {
		m.completion = completion{start: start, end: end, buffer: b.id, revision: e.revision, auto: !force}
		for _, item := range items {
			m.completion.items = append(m.completion.items, item.text)
			m.completion.details = append(m.completion.details, item.detail)
		}
	}
	cmd := m.completionLoad(c, needs, force)
	if force && cmd == nil && !m.completeAfterCatalog && len(items) == 0 {
		m.status, m.statusError = "No completions at the cursor", false
	}
	return cmd
}

// collect adds candidates for one site in rank order, so the scan limit drops
// the least relevant names first.
func (b *completionBuilder) collect(site sqledit.Site, x completionIndex) completionNeeds {
	var needs completionNeeds
	switch len(site.Qualifier) {
	case 0:
		switch site.Clause {
		case sqledit.ColumnClause:
			var scope []sqledit.Reference
			for _, ref := range site.References {
				if ref.Local {
					scope = append(scope, ref)
				}
			}
			if len(scope) == 0 {
				scope = site.References
			}
			for _, ref := range scope {
				b.referenceColumns(x, ref, 0, &needs)
			}
			for _, ref := range scope {
				label, detail := ref.Alias, "alias"
				if label == "" {
					label, detail = ref.Name, "table"
				}
				if ref.CTE && ref.Alias == "" {
					detail = "cte"
				}
				if label != "" && b.matches(label) {
					b.add(sqledit.QuoteIdentifier(label), label, detail, 1)
				}
			}
			b.ctes(site, 1)
			b.relations(x, 2, 3, false)
			b.keywords(site, 4)
		case sqledit.RelationClause:
			b.ctes(site, 0)
			b.relations(x, 1, 2, true)
			b.schemas(x, 2)
			b.keywords(site, 3)
		case sqledit.KeywordClause:
			b.keywords(site, 0)
		}
	case 1:
		qualifier := site.Qualifier[0]
		ref, isRef := completionReference(site, qualifier)
		for _, cte := range site.CTEs {
			if !isRef && cte.Name == qualifier {
				// Not in FROM yet, as while typing "SELECT r." before "FROM r".
				ref, isRef = cte, true
			}
		}
		schema := x.schemaName(qualifier)
		switch {
		case isRef && (site.Clause != sqledit.RelationClause || schema == ""):
			b.referenceColumns(x, ref, 0, &needs)
		case schema != "":
			for _, relation := range x.relations[schema] {
				if b.matches(relation.Name) {
					b.add(sqledit.QuoteIdentifier(relation.Name), relation.Name, relation.Kind, 0)
				}
			}
			if !x.listed[schema] {
				needs.schema = schema
			}
		default:
			if relation, ok := x.relation("", qualifier); ok {
				b.relationColumns(x, relation, 0, &needs)
			}
		}
	case 2:
		if relation, ok := x.relation(site.Qualifier[0], site.Qualifier[1]); ok {
			b.relationColumns(x, relation, 0, &needs)
		} else if schema := x.schemaName(site.Qualifier[0]); schema != "" && !x.listed[schema] {
			needs.schema = schema
		}
	}
	return needs
}

func (b *completionBuilder) matches(name string) bool {
	return strings.HasPrefix(strings.ToLower(name), b.word)
}

func (b *completionBuilder) add(text, key, detail string, rank int) {
	if len(b.items) >= completionScan || b.seen[text] {
		return
	}
	b.seen[text] = true
	b.items = append(b.items, completionItem{text: text, detail: detail, rank: rank, key: strings.ToLower(key)})
}

// referenceColumns adds the columns of one table reference. Subqueries and
// WITH queries contribute only the names the statement itself declares.
func (b *completionBuilder) referenceColumns(x completionIndex, ref sqledit.Reference, rank int, needs *completionNeeds) {
	if ref.Columns != nil || ref.Derived || ref.CTE {
		for _, column := range ref.Columns {
			if b.matches(column) {
				b.add(sqledit.QuoteIdentifier(column), column, "column", rank)
			}
		}
		return
	}
	if relation, ok := x.relation(ref.Schema, ref.Name); ok {
		b.relationColumns(x, relation, rank, needs)
	} else if schema := x.schemaName(ref.Schema); schema != "" && !x.listed[schema] && needs.schema == "" {
		needs.schema = schema
	}
}

func (b *completionBuilder) relationColumns(x completionIndex, relation postgres.Relation, rank int, needs *completionNeeds) {
	columns, ok := x.columns[relation.OID]
	if !ok {
		for _, oid := range needs.relations {
			if oid == relation.OID {
				return
			}
		}
		needs.relations = append(needs.relations, relation.OID)
		return
	}
	for _, column := range columns {
		if b.matches(column.Name) {
			b.add(sqledit.QuoteIdentifier(column.Name), column.Name, "column · "+column.Type, rank)
		}
	}
}

func (b *completionBuilder) ctes(site sqledit.Site, rank int) {
	for _, cte := range site.CTEs {
		if b.matches(cte.Name) {
			b.add(sqledit.QuoteIdentifier(cte.Name), cte.Name, "cte", rank)
		}
	}
}

// relations adds tables reachable through search_path bare and all others
// schema-qualified. In FROM-like clauses a schema prefix also matches.
func (b *completionBuilder) relations(x completionIndex, pathRank, otherRank int, bySchema bool) {
	reachable := make(map[string]bool)
	inPath := make(map[string]bool)
	for _, schema := range x.searchPath {
		inPath[schema] = true
		for _, relation := range x.relations[schema] {
			if !b.matches(relation.Name) {
				continue
			}
			if reachable[relation.Name] {
				b.add(sqledit.QuoteIdentifier(schema)+"."+sqledit.QuoteIdentifier(relation.Name), relation.Name, relation.Kind, otherRank)
				continue
			}
			reachable[relation.Name] = true
			b.add(sqledit.QuoteIdentifier(relation.Name), relation.Name, relation.Kind+" · "+schema, pathRank)
		}
	}
	for _, schema := range x.schemas {
		if inPath[schema] {
			continue
		}
		bySchemaName := bySchema && b.matches(schema)
		for _, relation := range x.relations[schema] {
			if bySchemaName || b.matches(relation.Name) {
				b.add(sqledit.QuoteIdentifier(schema)+"."+sqledit.QuoteIdentifier(relation.Name), schema+"."+relation.Name, relation.Kind, otherRank)
			}
		}
	}
}

func (b *completionBuilder) schemas(x completionIndex, rank int) {
	for _, schema := range x.schemas {
		if !strings.HasPrefix(schema, "pg_toast") && !strings.HasPrefix(schema, "pg_temp") && b.matches(schema) {
			b.add(sqledit.QuoteIdentifier(schema), schema, "schema", rank)
		}
	}
}

// keywords follow the typed word's case: "fr" offers "from", "FR" or an
// explicit request with no word offers "FROM".
func (b *completionBuilder) keywords(site sqledit.Site, rank int) {
	if site.Quoted {
		return
	}
	lower := site.Word != "" && site.Word == strings.ToLower(site.Word)
	for _, keyword := range sqlWords {
		if !b.matches(keyword) {
			continue
		}
		text := keyword
		if lower {
			text = strings.ToLower(keyword)
		}
		b.add(text, keyword, "keyword", rank)
	}
}

// completionReference finds the table reference a qualifier names: an alias,
// or the table name itself when it has no alias. The cursor's scope wins.
func completionReference(site sqledit.Site, qualifier string) (sqledit.Reference, bool) {
	best := -1
	for i, ref := range site.References {
		if ref.Alias != qualifier && (ref.Alias != "" || ref.Name != qualifier) {
			continue
		}
		if best < 0 || ref.Local && !site.References[best].Local {
			best = i
		}
	}
	if best < 0 {
		return sqledit.Reference{}, false
	}
	return site.References[best], true
}

// relation resolves a folded name the way PostgreSQL does, trying search_path
// for unqualified names, then falls back to any loaded schema.
func (x completionIndex) relation(schema, name string) (postgres.Relation, bool) {
	if schema != "" {
		return x.find(x.schemaName(schema), name)
	}
	for _, candidate := range x.searchPath {
		if relation, ok := x.find(candidate, name); ok {
			return relation, true
		}
	}
	for _, candidate := range x.schemas {
		if relation, ok := x.find(candidate, name); ok {
			return relation, true
		}
	}
	return postgres.Relation{}, false
}

func (x completionIndex) find(schema, name string) (postgres.Relation, bool) {
	relations := x.relations[schema]
	for _, relation := range relations {
		if relation.Name == name {
			return relation, true
		}
	}
	for _, relation := range relations {
		if strings.EqualFold(relation.Name, name) {
			return relation, true
		}
	}
	return postgres.Relation{}, false
}

// schemaName returns the catalog spelling of a folded schema name, or "".
func (x completionIndex) schemaName(name string) string {
	if name == "" {
		return ""
	}
	for _, schema := range x.schemas {
		if schema == name {
			return schema
		}
	}
	for _, schema := range x.schemas {
		if strings.EqualFold(schema, name) {
			return schema
		}
	}
	return ""
}

// completionIndex joins conn's completion catalog with its loaded tree. A
// buffer without a connection completes keywords only.
func (m *Model) completionIndex(conn *connection) completionIndex {
	if conn == nil {
		return completionIndex{searchPath: []string{"public"}}
	}
	c := &conn.completion
	x := completionIndex{searchPath: c.searchPath, schemas: c.schemas, relations: c.relations, listed: c.listed, columns: c.columns}
	if len(x.searchPath) == 0 {
		x.searchPath = []string{"public"}
	}
	var treeRelations map[string][]postgres.Relation
	var treeColumns map[uint32][]postgres.Column
	known := make(map[string]bool, len(x.schemas))
	for _, schema := range x.schemas {
		known[schema] = true
	}
	var schemas []string
	for _, node := range conn.catalog {
		switch {
		case node.kind == "relation" && node.relation.Kind != "sequence":
			if _, cached := c.relations[node.relation.Schema]; cached {
				continue
			}
			if treeRelations == nil {
				treeRelations = make(map[string][]postgres.Relation)
			}
			treeRelations[node.relation.Schema] = append(treeRelations[node.relation.Schema], node.relation)
			if !known[node.relation.Schema] {
				known[node.relation.Schema] = true
				schemas = append(schemas, node.relation.Schema)
			}
		case node.kind == "column":
			if _, cached := c.columns[node.relation.OID]; cached {
				continue
			}
			if treeColumns == nil {
				treeColumns = make(map[uint32][]postgres.Column)
			}
			treeColumns[node.relation.OID] = append(treeColumns[node.relation.OID], node.column)
		case node.kind == "schema" && !known[node.name]:
			known[node.name] = true
			schemas = append(schemas, node.name)
		}
	}
	if len(schemas) > 0 {
		schemas = append(schemas, x.schemas...)
		sort.Strings(schemas)
		x.schemas = schemas
	}
	if treeRelations != nil {
		for schema, relations := range c.relations {
			treeRelations[schema] = relations
		}
		x.relations = treeRelations
	}
	if treeColumns != nil {
		for oid, columns := range c.columns {
			treeColumns[oid] = columns
		}
		x.columns = treeColumns
	}
	return x
}

// syncCompletionCatalog resets conn's cached names when its session changes.
// Names are reread, keeping the old ones until then, after a statement that
// could change the catalog runs on conn or after completionStale passes,
// which picks up tables other sessions create.
func (m *Model) syncCompletionCatalog(conn *connection) {
	if conn == nil {
		return
	}
	c := &conn.completion
	if c.session != conn.session {
		*c = completionCatalog{session: conn.session, serial: c.serial, history: m.historySerial}
		return
	}
	stale := c.loaded && time.Since(c.loadedAt) > completionStale
	recent := int(min(uint64(len(m.history)), m.historySerial-c.history))
	for _, entry := range m.history[len(m.history)-recent:] {
		stale = stale || entry.conn == conn.id && sqledit.ChangesCatalog(entry.sql)
	}
	c.history = m.historySerial
	if stale {
		c.loaded, c.failed = false, false
	}
}

// completionLoad requests one batch of catalog data missing from conn's
// suggestions. Requests share the session with SQL and the database tree, so
// they hold conn's catalogBusy and wait while either is active; typing never
// waits for them. Inside a transaction only Ctrl+Space reads the catalog,
// because a failed or timed-out catalog query would abort the user's
// transaction.
func (m *Model) completionLoad(conn *connection, needs completionNeeds, force bool) tea.Cmd {
	if conn == nil || conn.session == nil || m.config.Demo || m.connecting && m.reconnectAttempt != nil && m.reconnectAttempt.target == conn.id ||
		!conn.connected || conn.txStatus == 'E' || conn.txStatus == 'T' && !force {
		return nil
	}
	c := &conn.completion
	session := conn.session
	readNames := !c.loaded && (!c.failed || force)
	if !readNames && needs.schema == "" && len(needs.relations) == 0 || conn.query.running {
		return nil
	}
	b := m.buffers[m.bufferIndex]
	c.wait = completionWait{active: true, buffer: b.id, revision: b.editor.revision, cursor: b.editor.cursor, force: force}
	m.completeAfterCatalog = true
	if conn.catalogBusy {
		// The pending catalog reply resumes this completion.
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	conn.catalogCancel = cancel
	conn.catalogBusy = true
	m.serial++
	c.serial = m.serial
	msg := completionCatalogMsg{conn: conn.id, serial: c.serial, session: session}
	if !readNames && needs.schema != "" {
		msg.schema = needs.schema
	} else if !readNames {
		msg.requested = needs.relations[:min(completionBatch, len(needs.relations))]
	}
	return func() tea.Msg {
		switch {
		case readNames:
			names, err := session.CompletionNames(ctx)
			msg.names, msg.err = &names, err
		case msg.schema != "":
			msg.relations, msg.err = session.Relations(ctx, msg.schema)
		default:
			msg.columns = make(map[uint32][]postgres.Column, len(msg.requested))
			for _, oid := range msg.requested {
				columns, err := session.Columns(ctx, oid)
				if err != nil {
					msg.err = err
					break
				}
				msg.columns[oid] = columns
			}
		}
		return msg
	}
}

// completionCatalogResult stores catalog data, releases the connection to
// work that queued behind it, and refreshes the popup if nothing moved.
func (m *Model) completionCatalogResult(msg completionCatalogMsg) tea.Cmd {
	conn := m.connectionFor(msg.conn)
	if conn == nil || msg.serial != conn.completion.serial {
		return nil
	}
	c := &conn.completion
	conn.catalogBusy = false
	if conn.catalogCancel != nil {
		conn.catalogCancel()
		conn.catalogCancel = nil
	}
	quiet := msg.err == nil || errors.Is(msg.err, context.Canceled) || errors.Is(msg.err, postgres.ErrBusy)
	if msg.session == c.session && (msg.err == nil || !quiet) {
		switch {
		case msg.names != nil && msg.err != nil:
			c.failed = true
		case msg.names != nil:
			c.loaded, c.failed, c.loadedAt, c.columns = true, false, time.Now(), nil
			c.searchPath, c.schemas = msg.names.SearchPath, msg.names.Schemas
			c.relations = make(map[string][]postgres.Relation)
			for _, relation := range msg.names.Relations {
				c.relations[relation.Schema] = append(c.relations[relation.Schema], relation)
			}
			c.listed = make(map[string]bool)
			for _, schema := range msg.names.Listed {
				c.listed[schema] = true
			}
		case msg.schema != "":
			if c.relations == nil || c.listed == nil {
				c.relations, c.listed = make(map[string][]postgres.Relation), make(map[string]bool)
			}
			var relations []postgres.Relation
			for _, relation := range msg.relations {
				if relation.Kind != "sequence" {
					relations = append(relations, relation)
				}
			}
			c.relations[msg.schema], c.listed[msg.schema] = relations, true
		default:
			if c.columns == nil || len(c.columns)+len(msg.requested) > completionCached {
				c.columns = make(map[uint32][]postgres.Column)
			}
			for _, oid := range msg.requested {
				// A failed read caches no columns so it is not retried on every key.
				c.columns[oid] = msg.columns[oid]
			}
		}
	}
	if !quiet {
		m.status, m.statusError = "Completion catalog: "+msg.err.Error(), true
	}
	if msg.err != nil {
		m.completeAfterCatalog = false
	}
	var cmds []tea.Cmd
	if conn.query.queued {
		conn.query.queued = false
		cmds = append(cmds, m.queryCommand(conn))
	}
	// Database tree work that queued behind this request goes next.
	if !conn.query.running {
		if cmd := m.nextCatalogRequest(conn); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	if m.completeAfterCatalog && m.conn() == conn {
		cmds = append(cmds, m.resumeCompletion())
	}
	return tea.Batch(cmds...)
}

// resumeCompletion reopens completion after catalog data arrive, but only at
// the place it was requested, keeping the selected suggestion when possible.
func (m *Model) resumeCompletion() tea.Cmd {
	conn := m.conn()
	m.completeAfterCatalog = false
	if conn == nil {
		return nil
	}
	wait := conn.completion.wait
	conn.completion.wait = completionWait{}
	b := m.buffers[m.bufferIndex]
	if !wait.active || m.focus != editorPane || m.modal.kind != "" || m.inspector.active {
		return nil
	}
	if b.id != wait.buffer || b.editor.revision != wait.revision || b.editor.cursor != wait.cursor {
		// Typing moved on while the session was busy. Read names now so the
		// next keystroke has them; that reply refreshes only an unmoved cursor.
		return m.completionLoad(conn, completionNeeds{}, false)
	}
	selected := ""
	if m.completion.index < len(m.completion.items) {
		selected = m.completion.items[m.completion.index]
	}
	cmd := m.completeWord(wait.force)
	for index, item := range m.completion.items {
		if item == selected {
			m.completion.index = index
		}
	}
	return cmd
}

// acceptCompletion replaces the recorded range only while the buffer still
// has the revision the suggestions were computed for.
func (m *Model) acceptCompletion() {
	c := m.completion
	m.completion = completion{}
	m.completeAfterCatalog = false
	b := &m.buffers[m.bufferIndex]
	e := &b.editor
	if b.id != c.buffer || e.revision != c.revision || c.index < 0 || c.index >= len(c.items) || c.start < 0 || c.start > c.end || c.end > len(e.text) || e.cursor < c.start || e.cursor > c.end {
		return
	}
	e.cursor, e.anchor = c.end, c.start
	e.insert(c.items[c.index], "")
}

// completionTyped reports whether the word before the cursor already equals
// the selected suggestion, ignoring case and quotes.
func (m *Model) completionTyped() bool {
	c := m.completion
	e := m.buffers[m.bufferIndex].editor
	if c.index < 0 || c.index >= len(c.items) || c.start < 0 || c.start > e.cursor || e.cursor > len(e.text) {
		return false
	}
	return sameWord(c.items[c.index], string(e.text[c.start:e.cursor]))
}

func sameWord(a, b string) bool {
	a = strings.ReplaceAll(strings.TrimSuffix(strings.TrimPrefix(a, `"`), `"`), `""`, `"`)
	b = strings.ReplaceAll(strings.TrimSuffix(strings.TrimPrefix(b, `"`), `"`), `""`, `"`)
	return strings.EqualFold(a, b)
}

// completionHeight is the popup's share of an editor with rows text rows.
func completionHeight(rows int) int {
	return min(6, max(1, rows*2/5))
}
