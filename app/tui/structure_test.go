package tui

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/dustinevan/scrunchie/lib/data/postgres"
	"github.com/dustinevan/scrunchie/pkg/result"
)

// offlineCatalog uses an unopened session: commands are prepared but never
// run, and replies are delivered by the test.
func structureCatalog(nodes ...catalogNode) *Model {
	m := New(Config{Session: &postgres.Session{}})
	m.conn().catalog = nodes
	m.focusActiveDatabase()
	return m
}

func structureFixture() postgres.Structure {
	return postgres.Structure{
		Relation:  postgres.Relation{OID: 42, Schema: "claims", Name: "claim_line", Kind: "table"},
		Qualified: "claims.claim_line", Owner: "owner", RowEstimate: 20000, TotalSize: "6400 kB",
		Columns: []postgres.ColumnDefinition{
			{Name: "claim_id", Quoted: "claim_id", Type: "bigint", NotNull: true},
			{Name: "note", Quoted: "note", Type: "text", Comment: "free\x1b[31m text"},
		},
		Constraints: []postgres.Constraint{
			{Name: "claim_line_pkey", Quoted: "claim_line_pkey", Kind: "primary key", Definition: "PRIMARY KEY (claim_id)", Validated: true},
			{Name: "claim_line_fkey", Quoted: "claim_line_fkey", Kind: "foreign key", Definition: "FOREIGN KEY (claim_id) REFERENCES claims.beneficiary(id) ON DELETE CASCADE",
				Table: "claims.beneficiary", Columns: "claim_id", References: "id", OnDelete: "CASCADE", OnUpdate: "NO ACTION", Validated: true},
		},
		Indexes: []postgres.Index{
			{Name: "claim_line_pkey", Definition: "CREATE UNIQUE INDEX claim_line_pkey ON claims.claim_line USING btree (claim_id)", Primary: true, Unique: true, Valid: true, Constraint: true},
			{Name: "claim_line_note", Definition: "CREATE INDEX claim_line_note ON claims.claim_line USING btree (note)", Valid: true, Size: "8192 bytes"},
		},
	}
}

func lineIndex(m *Model, prefix string) int {
	for position, line := range m.structureLines(false) {
		if strings.HasPrefix(line.text, prefix) {
			return position
		}
	}
	return -1
}

func TestSelectedCatalogAndProfileRowsHaveNoStrayEscapeText(t *testing.T) {
	m := New(Config{Demo: true, Profiles: paneProfiles(2)})
	m.focusActiveDatabase()
	for index, row := range m.databaseRows() {
		m.selectDatabaseRow(index)
		view := ansi.Strip(m.databasePaneView(40, 20))
		if strings.Contains(view, "[38;") || strings.Contains(view, "\x1b") {
			t.Fatalf("selected %v row leaked escape text: %q", row.kind, view)
		}
	}
	if _, node, ok := m.selectedCatalogNode(); ok {
		t.Fatalf("test did not end on a saved profile: %+v", node)
	}
	m.selectDatabaseRow(4)
	if _, node, ok := m.selectedCatalogNode(); !ok || node.kind != "column" || !strings.Contains(ansi.Strip(m.databasePaneView(40, 20)), "· id int8 PK NOT NULL") {
		t.Fatal("selected column row lost its muted detail text")
	}
	m.openDatabases()
	m.profileIndex = 1
	if view := ansi.Strip(m.View().Content); strings.Contains(view, "[38;") || !strings.Contains(view, "reader@must-not-connect.invalid") {
		t.Fatalf("selected saved database leaked escape text: %q", view)
	}
}

func TestCatalogFoldersMarkKindsAndKeepTreeNavigation(t *testing.T) {
	m := structureCatalog(catalogNode{kind: "schema", name: "claims", schema: "claims", parent: -1, expanded: true})
	m.conn().catalogBusy, m.conn().catalogSerial = true, 3
	m.conn().catalog[0].loading = true
	view := postgres.Relation{OID: 2, Schema: "claims", Name: "md", Kind: "view"}
	sequence := postgres.Relation{OID: 3, Schema: "claims", Name: "seq", Kind: "sequence"}
	routine := postgres.Routine{OID: 4, Schema: "claims", Name: "paid_total", Arguments: "b bigint", Kind: "function", Result: "numeric"}
	m.Update(catalogMsg{conn: m.conn().id, serial: 3, parent: 0, nodes: []catalogNode{
		{kind: "relation", name: "md", schema: "claims", relation: view, detail: "view"},
		{kind: "folder", name: "functions", schema: "claims", detail: "1", loaded: true, children: []int{2}},
		{kind: "function", name: "paid_total", schema: "claims", routine: routine, detail: "(b bigint) → numeric", loaded: true},
		{kind: "folder", name: "sequences", schema: "claims", detail: "1", loaded: true, children: []int{4}},
		{kind: "relation", name: "seq", schema: "claims", relation: sequence, detail: "sequence", loaded: true},
	}})
	if len(m.conn().catalog[0].children) != 3 || m.conn().catalog[2].parent != 0 || m.conn().catalog[3].parent != 2 || m.conn().catalog[5].parent != 4 {
		t.Fatalf("folder children were not attached: %+v", m.conn().catalog)
	}
	text := ansi.Strip(m.databasePaneView(40, 12))
	for _, want := range []string{"▸ md view", "▸ functions 1", "▸ sequences 1"} {
		if !strings.Contains(text, want) {
			t.Fatalf("tree lacks %q:\n%s", want, text)
		}
	}
	m.databasePaneKey(key(tea.KeyDown))
	m.databasePaneKey(key(tea.KeyDown))
	m.databasePaneKey(key(tea.KeyDown))
	m.databasePaneKey(key(tea.KeyEnter))
	m.databasePaneKey(key(tea.KeyDown))
	_, node, ok := m.selectedCatalogNode()
	if !ok || node.kind != "function" || !node.leaf() || !strings.Contains(ansi.Strip(m.databasePaneView(40, 12)), "· paid_total(b bigint) → numeric") {
		t.Fatalf("Enter on the functions folder did not lead to its function: %+v", node)
	}
	if cmd := m.databasePaneKey(key(tea.KeyEnter)); cmd != nil || anyCatalogBusy(m) != false || !strings.Contains(m.status, "paid_total") {
		t.Fatal("Enter on a function did not only describe it")
	}
	if m.copySelection(false) == nil || !strings.Contains(m.status, "bytes") {
		t.Fatal("Ctrl+C on a function did not copy its name")
	}
	m.databasePaneKey(key('i'))
	if got := string(m.buffers[0].editor.text); got != `"claims"."paid_total"` {
		t.Fatalf("i inserted %q", got)
	}
	m.focusPane(catalogPane)
	m.databasePaneKey(key(tea.KeyLeft))
	if _, node, _ := m.selectedCatalogNode(); node.kind != "folder" {
		t.Fatal("Left on a function did not select its folder")
	}
	if cmd := m.copySelection(false); cmd != nil {
		t.Fatal("a folder was copied as if it were a SQL name")
	}
	m.buffers[0].editor = newEditor("fun")
	m.buffers[0].editor.cursor = 3
	m.completeWord(true)
	for _, item := range m.completion.items {
		if strings.Contains(item, "functions") {
			t.Fatalf("completion offered the folder: %q", m.completion.items)
		}
	}
}

func TestQueuedCatalogExpansionsAllLoadInOrder(t *testing.T) {
	m := structureCatalog(
		catalogNode{kind: "schema", name: "alpha", schema: "alpha", parent: -1},
		catalogNode{kind: "schema", name: "beta", schema: "beta", parent: -1},
	)
	m.conn().query.running, m.conn().query.serial = true, 5
	m.databasePaneKey(key(tea.KeyDown))
	m.databasePaneKey(key(tea.KeyEnter))
	m.databasePaneKey(key(tea.KeyDown))
	m.databasePaneKey(key(tea.KeyEnter))
	if len(m.conn().catalogQueue) != 2 || !m.conn().catalog[0].expanded || !m.conn().catalog[1].expanded || anyCatalogBusy(m) {
		t.Fatalf("expansions during SQL were not both queued: %q", m.conn().catalogQueue)
	}
	if text := ansi.Strip(m.databasePaneView(40, 8)); strings.Count(text, m.spinner.View()) < 2 {
		t.Fatalf("queued schemas show no loading state:\n%s", text)
	}
	m.Update(queryMsg{conn: m.conn().id, serial: 5, batch: result.Batch{Connected: true, TxStatus: 'I'}})
	if !anyCatalogBusy(m) || !m.conn().catalog[0].loading || m.conn().catalog[1].loading || len(m.conn().catalogQueue) != 1 {
		t.Fatal("finished SQL did not start the first queued expansion")
	}
	m.Update(catalogMsg{conn: m.conn().id, serial: m.conn().catalogSerial, parent: 0, nodes: []catalogNode{{kind: "relation", name: "a", schema: "alpha", relation: postgres.Relation{Schema: "alpha", Name: "a", Kind: "table"}}}})
	if !m.conn().catalog[0].loaded || !m.conn().catalog[1].loading || len(m.conn().catalogQueue) != 0 {
		t.Fatal("the second queued expansion was lost")
	}
	m.Update(catalogMsg{conn: m.conn().id, serial: m.conn().catalogSerial, parent: 1, nodes: []catalogNode{{kind: "relation", name: "b", schema: "beta", relation: postgres.Relation{Schema: "beta", Name: "b", Kind: "table"}}}})
	if !m.conn().catalog[1].loaded || anyCatalogBusy(m) || len(m.visibleCatalog(m.conn())) != 4 {
		t.Fatal("both schemas did not end loaded and expanded")
	}
}

func TestExpansionQueuedDuringRefreshFollowsItsSchema(t *testing.T) {
	m := structureCatalog(
		catalogNode{kind: "schema", name: "alpha", schema: "alpha", parent: -1},
		catalogNode{kind: "schema", name: "beta", schema: "beta", parent: -1},
	)
	m.conn().catalogBusy, m.conn().catalogSerial = true, 7
	m.databasePaneKey(key(tea.KeyDown))
	m.databasePaneKey(key(tea.KeyDown))
	m.databasePaneKey(key(tea.KeyEnter))
	if len(m.conn().catalogQueue) != 1 {
		t.Fatal("expansion during a refresh was not queued")
	}
	m.Update(catalogMsg{conn: m.conn().id, serial: 7, parent: -1, nodes: []catalogNode{
		{kind: "schema", name: "aardvark", schema: "aardvark", parent: -1},
		{kind: "schema", name: "alpha", schema: "alpha", parent: -1},
		{kind: "schema", name: "beta", schema: "beta", parent: -1},
	}})
	if m.conn().catalog[1].loading || !m.conn().catalog[2].loading || !m.conn().catalog[2].expanded || m.conn().catalog[2].name != "beta" {
		t.Fatalf("queued expansion loaded the node at its old index: %+v", m.conn().catalog)
	}
	if _, node, ok := m.selectedCatalogNode(); !ok || node.name != "beta" {
		t.Fatal("refresh lost the selected schema")
	}
}

func TestPreparedSQLBuffersStartCleanAndQuitWithoutPrompts(t *testing.T) {
	m := New(Config{Demo: true})
	m.focusActiveDatabase()
	m.databasePaneKey(key(tea.KeyDown))
	m.databasePaneKey(key(tea.KeyDown))
	m.databasePaneKey(key('p'))
	for _, id := range []string{"count", "begin", "commit", "rollback", "explain"} {
		m.focusPane(catalogPane)
		m.action(id)
	}
	m.history = []historyEntry{{sql: "SELECT 'from history';", at: time.Now()}}
	m.openPrompt("history", "History", "")
	m.prepareView()
	press(m, tea.KeyEnter)
	m.structure = structureView{active: true, loaded: true, data: structureFixture(), collapsed: map[string]bool{}}
	press(m, 'D')
	if len(m.buffers) != 9 {
		t.Fatalf("expected eight prepared buffers, got %d", len(m.buffers)-1)
	}
	for _, b := range m.buffers[1:] {
		if b.dirty || string(b.editor.text) != b.saved || b.editor.text == nil {
			t.Fatalf("prepared buffer %q starts dirty", string(b.editor.text))
		}
	}
	if !strings.HasPrefix(string(m.buffers[8].editor.text), "CREATE TABLE claims.claim_line (") || m.structure.active || m.focus != editorPane {
		t.Fatal("D did not open the structure's DDL in the editor")
	}
	cmd := press(m, 'q', tea.ModCtrl)
	if m.modal.kind != "" || cmd == nil {
		t.Fatalf("quit asked about unedited prepared SQL: %q", m.modal.message)
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("quit did not finish")
	}
	m.quitIntent = false
	m.buffers[m.bufferIndex].editor.insert("-- edited\n", "")
	m.prepareView()
	if press(m, 'q', tea.ModCtrl); !m.buffers[m.bufferIndex].dirty || m.modal.kind != "confirm-dirty" {
		t.Fatal("an edited prepared buffer did not become dirty")
	}
}

func TestStructureViewLoadsNavigatesCopiesAndReturnsToItsRow(t *testing.T) {
	relation := postgres.Relation{OID: 42, Schema: "claims", Name: "claim_line", Kind: "table"}
	m := structureCatalog(
		catalogNode{kind: "schema", name: "claims", schema: "claims", parent: -1, expanded: true, loaded: true, children: []int{1}},
		catalogNode{kind: "relation", name: "claim_line", schema: "claims", relation: relation, detail: "table", parent: 0, expanded: true, loaded: true, children: []int{2}},
		catalogNode{kind: "column", name: "note", schema: "claims", relation: relation, detail: "text", parent: 1, loaded: true},
	)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m.databasePaneKey(key(tea.KeyDown))
	m.databasePaneKey(key(tea.KeyDown))
	m.databasePaneKey(key(tea.KeyDown))
	selection := m.databasePane.selection
	if cmd := press(m, 'd'); cmd == nil || !m.structure.active || !m.structure.loading || !anyCatalogBusy(m) || m.structure.target.OID != 42 {
		t.Fatal("d on a column did not request its table's structure")
	}
	if text := ansi.Strip(m.View().Content); !strings.Contains(text, "Loading structure") {
		t.Fatalf("loading structure is not visible:\n%s", text)
	}
	m.Update(catalogMsg{conn: m.conn().id, serial: m.conn().catalogSerial, kind: "structure", structure: structureFixture()})
	text := ansi.Strip(m.View().Content)
	for _, want := range []string{"claims.claim_line · table · ~20,000 rows (estimate) · 6400 kB", "▾ Columns (2)", "claim_id  bigint  NOT NULL",
		`note      text    NULL      -- free\x1b[31m text`, "claim_line_fkey  (claim_id) → claims.beneficiary (id)  ON DELETE CASCADE  ON UPDATE NO ACTION",
		"claim_line_note  8192 bytes", "▾ Referenced by (0)", "Owner"} {
		if !strings.Contains(text, want) {
			t.Fatalf("structure view lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "\x1b[31m") {
		t.Fatal("a database comment reached the terminal unescaped")
	}
	before := len(m.structureLines(false))
	press(m, tea.KeyEnter)
	if len(m.structureLines(false)) != before-2 || !m.structure.collapsed["Columns"] {
		t.Fatal("Enter on a section header did not collapse it")
	}
	press(m, tea.KeyRight)
	press(m, tea.KeyRight)
	if m.structure.index != 1 || len(m.structureLines(false)) != before {
		t.Fatal("Right did not expand the section and enter its first item")
	}
	if cmd := m.copySelection(false); cmd == nil || !strings.Contains(m.status, "bytes") {
		t.Fatal("Ctrl+C did not copy the selected line")
	}
	press(m, tea.KeyDown)
	press(m, tea.KeyLeft)
	if m.structure.index != 0 {
		t.Fatal("Left on an item did not select its section header")
	}
	m.structure.index = lineIndex(m, "Indexes")
	press(m, tea.KeyLeft)
	if !m.structure.collapsed["Indexes"] || lineIndex(m, "claim_line_note") >= 0 {
		t.Fatal("Left on an expanded header did not collapse it")
	}
	m.findNext("claim_line_note")
	if m.structure.collapsed["Indexes"] || m.structure.index != lineIndex(m, "claim_line_note") {
		t.Fatal("find did not open the collapsed section holding the match")
	}
	press(m, tea.KeyEsc)
	if m.structure.active || m.focus != catalogPane || m.databasePane.selection != selection {
		t.Fatal("Esc did not return to the database pane at the same row")
	}
	if _, node, ok := m.selectedCatalogNode(); !ok || node.name != "note" {
		t.Fatal("the originating column row is no longer selected")
	}
}

func TestStructureRequestsWaitForSQLAndIgnoreStaleReplies(t *testing.T) {
	relation := postgres.Relation{OID: 42, Schema: "claims", Name: "claim_line", Kind: "table"}
	m := structureCatalog(catalogNode{kind: "relation", name: "claim_line", schema: "claims", relation: relation, detail: "table", parent: -1})
	m.databasePaneKey(key(tea.KeyDown))
	m.conn().query.running, m.conn().query.serial = true, 9
	press(m, 'd')
	if !m.structure.active || !m.structure.queued || anyCatalogBusy(m) {
		t.Fatal("structure request ran while SQL owned the session")
	}
	m.Update(queryMsg{conn: m.conn().id, serial: 9, batch: result.Batch{Connected: true, TxStatus: 'T'}})
	if !anyCatalogBusy(m) || m.structure.queued || m.structure.serial != m.conn().catalogSerial {
		t.Fatal("finished SQL did not start the queued structure request")
	}
	serial := m.conn().catalogSerial
	press(m, tea.KeyEsc)
	m.Update(catalogMsg{conn: m.conn().id, serial: serial, kind: "structure", structure: structureFixture()})
	if m.structure.active || m.structure.loaded || anyCatalogBusy(m) {
		t.Fatal("a reply for a closed structure view reopened it or kept the session busy")
	}
	m.focusPane(catalogPane)
	m.conn().connected = false
	if cmd := press(m, 'd'); cmd != nil || m.structure.active || !strings.Contains(m.status, "reconnect") {
		t.Fatal("structure request on a closed session did not explain reconnection")
	}
	saved := New(Config{Profiles: paneProfiles(1)})
	saved.focusPane(catalogPane)
	saved.selectDatabaseRow(1)
	if cmd := press(saved, 'd'); cmd != nil || saved.modal.kind != "" || len(saved.profiles) != 1 || !strings.Contains(saved.status, "Ctrl+B") {
		t.Fatal("d on a saved database did something other than explain where removal lives")
	}
	demo := New(Config{Demo: true})
	demo.focusActiveDatabase()
	demo.databasePaneKey(key(tea.KeyDown))
	demo.databasePaneKey(key(tea.KeyDown))
	if cmd := press(demo, 'd'); cmd != nil || demo.structure.active || !strings.Contains(demo.status, "Demo") {
		t.Fatal("offline demo attempted a catalog request")
	}
}

func TestLateDDLReplyNeverTakesOverTheEditor(t *testing.T) {
	routine := postgres.Routine{OID: 4, Schema: "claims", Name: "paid_total", Arguments: "b bigint", Kind: "function"}
	for _, interrupted := range []bool{false, true} {
		m := structureCatalog(catalogNode{kind: "function", name: "paid_total", schema: "claims", routine: routine, parent: -1, loaded: true})
		m.databasePaneKey(key(tea.KeyDown))
		m.conn().query.running, m.conn().query.serial = true, 2
		press(m, 'd')
		if !m.ddl.queued || m.structure.active {
			t.Fatal("d on a function did not queue its DDL")
		}
		m.Update(queryMsg{conn: m.conn().id, serial: 2, batch: result.Batch{Connected: true, TxStatus: 'I'}})
		if interrupted {
			press(m, tea.KeyRight)
			press(m, 'x')
		}
		m.Update(catalogMsg{conn: m.conn().id, serial: m.conn().catalogSerial, kind: "ddl", text: "CREATE FUNCTION claims.paid_total(b bigint);\n"})
		b := m.buffers[len(m.buffers)-1]
		if len(m.buffers) != 2 || b.dirty || string(b.editor.text) != b.saved || anyRunning(m) {
			t.Fatal("DDL did not open in a clean, unexecuted buffer")
		}
		if interrupted && (m.bufferIndex != 0 || string(m.buffers[0].editor.text) != "x" || !strings.Contains(m.status, "SQL 2")) {
			t.Fatal("a late DDL reply moved the user away from the buffer being edited")
		}
		if !interrupted && (m.bufferIndex != 1 || m.focus != editorPane) {
			t.Fatal("an awaited DDL reply did not open in the editor")
		}
	}
}

func TestMouseStructureSelectsTogglesScrollsAndCloses(t *testing.T) {
	m := New(Config{Demo: true})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 12})
	m.focusPane(catalogPane)
	m.structure = structureView{active: true, loaded: true, origin: catalogPane, data: structureFixture(), collapsed: map[string]bool{}}
	m.prepareView()
	mouseClick(m, 10, 6)
	if m.structure.index != 1 {
		t.Fatalf("click selected line %d", m.structure.index)
	}
	mouseClick(m, 1, 5)
	if !m.structure.collapsed["Columns"] {
		t.Fatal("clicking a header's disclosure marker did not collapse it")
	}
	mouseWheel(m, 10, 6, tea.MouseWheelDown)
	if m.structure.top == 0 || m.structure.index != 0 {
		t.Fatal("wheel did not scroll without moving the selection")
	}
	mouseClick(m, 4, 2)
	if m.structure.active || m.focus != catalogPane {
		t.Fatal("the Close control did not return to the database pane")
	}
}

// The structure view and DDL against the synthetic claims schema.
func TestStructureAndDDLFromLiveCatalog(t *testing.T) {
	dsn := os.Getenv("SCRUNCH_TEST_DSN")
	if dsn == "" {
		t.Skip("SCRUNCH_TEST_DSN must name an isolated test database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	session, err := postgres.Open(ctx, dsn, postgres.Options{})
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	m := New(Config{Session: session, DSN: dsn})
	closeTestModel(t, m)
	m.Update(tea.WindowSizeMsg{Width: 140, Height: 60})
	finishCommands(t, m, m.Init())
	m.focusActiveDatabase()
	for _, name := range []string{"claims", "beneficiary"} {
		for m.databasePane.index < len(m.databaseRows())-1 {
			m.databasePaneKey(key(tea.KeyDown))
			if _, node, ok := m.selectedCatalogNode(); ok && node.name == name {
				break
			}
		}
		if name == "claims" {
			finishCommands(t, m, m.databasePaneKey(key(tea.KeyEnter)))
		}
	}
	if _, node, ok := m.selectedCatalogNode(); !ok || node.name != "beneficiary" {
		t.Fatal("could not select claims.beneficiary")
	}
	text := ansi.Strip(m.databasePaneView(60, 40))
	for _, want := range []string{"md_beneficiaries view", "functions 1", "sequences 1"} {
		if !strings.Contains(text, want) {
			t.Fatalf("live tree lacks %q:\n%s", want, text)
		}
	}
	finishCommands(t, m, press(m, 'd'))
	if !m.structure.loaded || m.structure.err != "" {
		t.Fatalf("structure did not load: %q", m.structure.err)
	}
	text = ansi.Strip(m.View().Content)
	for _, want := range []string{"claims.beneficiary · table · ~50,000 rows (estimate)", "beneficiary_mbi_key  UNIQUE (mbi)",
		"claim_line_beneficiary_id_fkey  claims.claim_line (beneficiary_id) → (id)  ON DELETE CASCADE", "Comment     Synthetic beneficiaries"} {
		if !strings.Contains(text, want) {
			t.Fatalf("live structure lacks %q:\n%s", want, text)
		}
	}
	press(m, 'D')
	b := m.buffers[m.bufferIndex]
	if !strings.HasPrefix(string(b.editor.text), "CREATE TABLE claims.beneficiary (\n") || b.dirty || anyRunning(m) || len(m.history) != 0 {
		t.Fatal("DDL buffer was not clean, unexecuted CREATE TABLE text")
	}
	m.focusPane(catalogPane)
	for m.databasePane.index < len(m.databaseRows())-1 {
		m.databasePaneKey(key(tea.KeyDown))
		if _, node, ok := m.selectedCatalogNode(); ok && node.kind == "folder" && node.name == "sequences" {
			break
		}
	}
	m.databasePaneKey(key(tea.KeyEnter))
	m.databasePaneKey(key(tea.KeyDown))
	finishCommands(t, m, press(m, 'd'))
	b = m.buffers[m.bufferIndex]
	if !strings.HasPrefix(string(b.editor.text), "CREATE SEQUENCE claims.beneficiary_id_seq\n    AS bigint") || b.dirty {
		t.Fatalf("sequence DDL got %q", string(b.editor.text))
	}
}
