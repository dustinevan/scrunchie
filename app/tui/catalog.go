package tui

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/dustinevan/scrunchie/lib/data/postgres"
	"github.com/dustinevan/scrunchie/pkg/termtext"
)

// catalogNode is one row of the catalog tree. Folder nodes group a schema's
// functions and sequences; they are not database objects themselves.
type catalogNode struct {
	kind     string // "schema", "relation", "column", "folder", or "function"
	name     string
	detail   string
	schema   string
	relation postgres.Relation
	column   postgres.Column
	routine  postgres.Routine
	parent   int
	children []int
	expanded bool
	loaded   bool
	loading  bool
}

// identity names a node independently of its index, which a catalog refresh
// replaces. Overloaded functions differ by their argument lists.
func (n catalogNode) identity() string {
	return n.kind + "\x00" + n.schema + "\x00" + n.relation.Name + "\x00" + n.name + "\x00" + n.routine.Arguments
}

// leaf reports nodes without children: columns, functions, and sequences.
func (n catalogNode) leaf() bool {
	return n.kind == "column" || n.kind == "function" || n.kind == "relation" && n.relation.Kind == "sequence"
}

// identifier is the SQL name that `i` inserts and Ctrl+C copies. Folders
// have none.
func (n catalogNode) identifier() string {
	switch n.kind {
	case "relation":
		return n.relation.QualifiedName()
	case "function":
		return n.routine.QualifiedName()
	case "folder":
		return ""
	}
	return `"` + strings.ReplaceAll(n.name, `"`, `""`) + `"`
}

// loadCatalog loads c's schemas (parent < 0) or one node's children. Requests
// made while c is busy wait in order, keyed by node identity. Other
// connections are not involved.
func (m *Model) loadCatalog(c *connection, parent int) tea.Cmd {
	if c == nil {
		return nil
	}
	if m.connecting && m.reconnectAttempt != nil && m.reconnectAttempt.target == c.id {
		m.status = "Wait for the reconnect to finish before loading the catalog"
		return nil
	}
	if m.config.Demo {
		m.status, m.statusError = "Demo catalog · no database connection", false
		return nil
	}
	if c.session == nil {
		return nil
	}
	if !c.connected {
		m.status, m.statusError = "Connection closed · Enter on the database reconnects", false
		return nil
	}
	if parent >= len(c.catalog) {
		return nil
	}
	if parent >= 0 && c.catalog[parent].loaded {
		c.catalog[parent].expanded = true
		return nil
	}
	if parent >= 0 && c.catalog[parent].loading {
		return nil
	}
	if parent >= 0 {
		c.catalog[parent].expanded = true
	}
	if c.query.running || c.catalogBusy {
		if parent < 0 {
			c.refreshPending = true
		} else if identity := c.catalog[parent].identity(); !slices.Contains(c.catalogQueue, identity) {
			c.catalogQueue = append(c.catalogQueue, identity)
		}
		m.status, m.statusError = "Catalog request queued until "+termtext.Safe(c.name)+" is idle", false
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	c.catalogCancel = cancel
	c.catalogBusy = true
	m.serial++
	c.catalogSerial = m.serial
	id, serial, session := c.id, c.catalogSerial, c.session
	var node catalogNode
	if parent >= 0 && parent < len(c.catalog) {
		c.catalog[parent].loading = true
		node = c.catalog[parent]
	}
	// Reply nodes are children of parent unless another reply node lists
	// them, by position in the reply, as its own children.
	command := func() tea.Msg {
		var nodes []catalogNode
		if parent < 0 {
			schemas, err := session.Schemas(ctx)
			if err != nil {
				return catalogMsg{conn: id, serial: serial, parent: parent, err: err}
			}
			for _, schema := range schemas {
				nodes = append(nodes, catalogNode{kind: "schema", name: schema.Name, schema: schema.Name, parent: -1})
			}
		} else if node.kind == "schema" {
			relations, err := session.Relations(ctx, node.schema)
			if err != nil {
				return catalogMsg{conn: id, serial: serial, parent: parent, err: err}
			}
			routines, err := session.Routines(ctx, node.schema)
			if err != nil {
				return catalogMsg{conn: id, serial: serial, parent: parent, err: err}
			}
			var sequences []catalogNode
			for _, relation := range relations {
				child := catalogNode{kind: "relation", name: relation.Name, schema: relation.Schema, relation: relation, detail: relation.Kind}
				if relation.Kind == "sequence" {
					child.loaded = true
					sequences = append(sequences, child)
				} else {
					nodes = append(nodes, child)
				}
			}
			if len(routines) > 0 {
				folder := catalogNode{kind: "folder", name: "functions", schema: node.schema, detail: strconv.Itoa(len(routines)), loaded: true}
				for index := range routines {
					folder.children = append(folder.children, len(nodes)+1+index)
				}
				nodes = append(nodes, folder)
				for _, routine := range routines {
					detail := "(" + routine.Arguments + ")"
					if routine.Kind == "function" {
						detail += " → " + routine.Result
					} else {
						detail += " " + routine.Kind
					}
					nodes = append(nodes, catalogNode{kind: "function", name: routine.Name, schema: routine.Schema, routine: routine, detail: detail, loaded: true})
				}
			}
			if len(sequences) > 0 {
				folder := catalogNode{kind: "folder", name: "sequences", schema: node.schema, detail: strconv.Itoa(len(sequences)), loaded: true}
				for index := range sequences {
					folder.children = append(folder.children, len(nodes)+1+index)
				}
				nodes = append(nodes, folder)
				nodes = append(nodes, sequences...)
			}
		} else if node.kind == "relation" {
			columns, err := session.Columns(ctx, node.relation.OID)
			if err != nil {
				return catalogMsg{conn: id, serial: serial, parent: parent, err: err}
			}
			for _, column := range columns {
				detail := column.Type
				if column.PrimaryKey {
					detail += " PK"
				}
				if !column.Nullable {
					detail += " NOT NULL"
				}
				if column.Default != "" {
					detail += " DEFAULT " + column.Default
				}
				nodes = append(nodes, catalogNode{kind: "column", name: column.Name, schema: node.schema, relation: node.relation, column: column, detail: detail, loaded: true})
			}
		}
		return catalogMsg{conn: id, serial: serial, parent: parent, nodes: nodes}
	}
	return tea.Batch(command, m.spinner.Tick)
}

// nextCatalogRequest starts the oldest catalog work waiting for c once its SQL
// and previous catalog request have finished. Queued branches are found by
// identity, so a refresh that renumbered the tree still opens the right one.
func (m *Model) nextCatalogRequest(c *connection) tea.Cmd {
	if c.refreshPending {
		c.refreshPending = false
		return m.loadCatalog(c, -1)
	}
	for len(c.catalogQueue) > 0 {
		identity := c.catalogQueue[0]
		c.catalogQueue = c.catalogQueue[1:]
		for index, node := range c.catalog {
			if node.identity() == identity && !node.loaded && !node.loading {
				return m.loadCatalog(c, index)
			}
		}
	}
	if m.structure.active && m.structure.queued && m.structure.conn == c.id {
		return m.loadStructure()
	}
	if m.ddl.queued && m.ddl.conn == c.id {
		return m.loadDDL()
	}
	return nil
}

// visibleCatalog lists c's tree rows in display order: expanded branches, or
// every match of the catalog filter with its ancestors.
func (m *Model) visibleCatalog(c *connection) []int {
	query := strings.ToLower(m.catalogFilter)
	include := make([]bool, len(c.catalog))
	if query != "" {
		for i, node := range c.catalog {
			if strings.Contains(strings.ToLower(node.name+" "+node.detail), query) {
				for index := i; index >= 0; index = c.catalog[index].parent {
					include[index] = true
				}
			}
		}
	}
	var stack, visible []int
	for i := len(c.catalog) - 1; i >= 0; i-- {
		if c.catalog[i].parent < 0 {
			stack = append(stack, i)
		}
	}
	for len(stack) > 0 {
		index := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		node := c.catalog[index]
		if query != "" && !include[index] {
			continue
		}
		visible = append(visible, index)
		if node.expanded || query != "" {
			for i := len(node.children) - 1; i >= 0; i-- {
				stack = append(stack, node.children[i])
			}
		}
	}
	return visible
}

func (m *Model) catalogKey(msg tea.KeyPressMsg) tea.Cmd {
	return m.databasePaneKey(msg)
}
