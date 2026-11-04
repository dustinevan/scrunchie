package tui

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/dustinevan/scrunchie/lib/data/postgres"
)

// typeText sends each rune as a key press, as a terminal does while typing.
func typeText(t *testing.T, m *Model, text string) {
	t.Helper()
	for _, r := range text {
		finishCommands(t, m, press(m, r))
	}
}

// placeCursor removes the | marker from the active buffer and puts the cursor there.
func placeCursor(m *Model) {
	e := &m.buffers[m.bufferIndex].editor
	text := string(e.text)
	at := strings.Index(text, "|")
	e.text = []rune(text[:at] + text[at+1:])
	e.cursor, e.anchor = len([]rune(text[:at])), -1
}

// offlineCatalog stands in for a database tree with one loaded table on the
// current buffer's connection, adding an offline one if it has none.
func offlineCatalog(m *Model) {
	c := m.conn()
	if c == nil {
		c = offlineConnection(m)
	}
	relation := postgres.Relation{OID: 7, Schema: "public", Name: "people", Kind: "table"}
	c.catalog = []catalogNode{
		{kind: "schema", name: "public", schema: "public", parent: -1, children: []int{1}, loaded: true},
		{kind: "relation", name: "people", schema: "public", relation: relation, parent: 0, children: []int{2, 3}, loaded: true},
		{kind: "column", name: "name", schema: "public", relation: relation, column: postgres.Column{Name: "name", Type: "text"}, parent: 1, loaded: true},
		{kind: "column", name: "name_suffix", schema: "public", relation: relation, column: postgres.Column{Name: "name_suffix", Type: "text"}, parent: 1, loaded: true},
	}
}

func TestCompletionInvalidatedByEveryBufferEdit(t *testing.T) {
	for _, edit := range []struct {
		name string
		key  tea.KeyPressMsg
	}{
		{"Shift+Delete", key(tea.KeyDelete, tea.ModShift)},
		{"Ctrl+/", key('/', tea.ModCtrl)},
		{"Ctrl+Z", key('z', tea.ModCtrl)},
	} {
		t.Run(edit.name, func(t *testing.T) {
			m := New(Config{})
			typeText(t, m, "sel")
			if len(m.completion.items) == 0 || m.completion.items[0] != "select" {
				t.Fatalf("typing sel offered %q", m.completion.items)
			}
			m.Update(edit.key)
			before := string(m.buffers[0].editor.text)
			press(m, tea.KeyEnter)
			after := string(m.buffers[0].editor.text)
			if strings.Contains(strings.ToLower(after), "select") || after == before {
				t.Fatalf("Enter after %s produced %q from %q", edit.name, after, before)
			}
		})
	}
	m := New(Config{InitialSQL: "SELECT 1"})
	e := &m.buffers[0].editor
	e.cursor = len(e.text)
	m.completion = completion{items: []string{"SELECT"}, start: 6, end: 30, buffer: m.buffers[0].id, revision: e.revision}
	m.acceptCompletion()
	if string(e.text) != "SELECT 1" {
		t.Fatal("an out-of-range completion changed SQL")
	}
	m.completion = completion{items: []string{"SELECT"}, start: 0, end: 1, buffer: m.buffers[0].id, revision: e.revision + 1}
	m.acceptCompletion()
	if string(e.text) != "SELECT 1" {
		t.Fatal("a completion from another revision changed SQL")
	}
}

func TestFormatInvalidatesOpenCompletion(t *testing.T) {
	m := New(Config{InitialSQL: "select a,b from t where a=1 and fr"})
	e := &m.buffers[0].editor
	e.cursor = len(e.text)
	finishCommands(t, m, press(m, 'o'))
	if len(m.completion.items) == 0 {
		t.Fatal("no completion before formatting")
	}
	press(m, tea.KeyF4)
	formatted := string(e.text)
	if formatted == "select a,b from t where a=1 and fro" || len(m.completion.items) != 0 {
		t.Fatalf("formatting %q kept a stale completion range", formatted)
	}
	press(m, tea.KeyEnter)
	if strings.Join(strings.Fields(string(e.text)), " ") != strings.Join(strings.Fields(formatted), " ") {
		t.Fatalf("Enter after formatting inserted a completion: %q", string(e.text))
	}
}

func TestEnterAfterTypedWordEndsTheLine(t *testing.T) {
	m := New(Config{InitialSQL: "SELECT | FROM people"})
	placeCursor(m)
	offlineCatalog(m)
	typeText(t, m, "name")
	if len(m.completion.items) != 2 || m.completion.items[0] != "name" || !m.completion.auto {
		t.Fatalf("typing name offered %q", m.completion.items)
	}
	press(m, tea.KeyEnter)
	if string(m.buffers[0].editor.text) != "SELECT name\n FROM people" || len(m.completion.items) != 0 {
		t.Fatalf("Enter on a typed word = %q", string(m.buffers[0].editor.text))
	}

	m = New(Config{InitialSQL: "SELECT | FROM people"})
	placeCursor(m)
	offlineCatalog(m)
	typeText(t, m, "na")
	press(m, tea.KeyDown)
	press(m, tea.KeyEnter)
	if string(m.buffers[0].editor.text) != "SELECT name_suffix FROM people" {
		t.Fatalf("Enter on a prefix = %q", string(m.buffers[0].editor.text))
	}

	m = New(Config{InitialSQL: "SELECT name| FROM people"})
	placeCursor(m)
	offlineCatalog(m)
	finishCommands(t, m, press(m, ' ', tea.ModCtrl))
	press(m, tea.KeyEnter)
	if string(m.buffers[0].editor.text) != "SELECT name FROM people" {
		t.Fatalf("Enter after Ctrl+Space = %q", string(m.buffers[0].editor.text))
	}

	m = New(Config{InitialSQL: "SELECT | FROM people"})
	placeCursor(m)
	offlineCatalog(m)
	typeText(t, m, "name")
	press(m, tea.KeyTab)
	if string(m.buffers[0].editor.text) != "SELECT name FROM people" {
		t.Fatalf("Tab on a typed word = %q", string(m.buffers[0].editor.text))
	}
}

func TestCompletionStaysClosedInLiteralsAndForSoleExactMatch(t *testing.T) {
	m := New(Config{InitialSQL: "SELECT * FROM people WHERE note = '|"})
	placeCursor(m)
	offlineCatalog(m)
	typeText(t, m, "na")
	if len(m.completion.items) != 0 {
		t.Fatalf("completion opened in a string: %q", m.completion.items)
	}
	m = New(Config{InitialSQL: "SELECT 1 -- |"})
	placeCursor(m)
	typeText(t, m, "sel")
	if len(m.completion.items) != 0 {
		t.Fatal("completion opened in a comment")
	}
	m = New(Config{})
	typeText(t, m, "select")
	if len(m.completion.items) != 0 {
		t.Fatalf("a typed keyword reopened its own sole suggestion: %q", m.completion.items)
	}
}

func TestCompletionScopesColumnsToAliases(t *testing.T) {
	m := New(Config{InitialSQL: "SELECT p.| FROM people p"})
	placeCursor(m)
	offlineCatalog(m)
	m.completeWord(true)
	if !slices.Equal(m.completion.items, []string{"name", "name_suffix"}) || m.completion.details[0] != "column · text" {
		t.Fatalf("alias columns = %q %q", m.completion.items, m.completion.details)
	}
	m = New(Config{InitialSQL: "SELECT x.| FROM other x"})
	placeCursor(m)
	offlineCatalog(m)
	m.completeWord(true)
	if len(m.completion.items) != 0 {
		t.Fatalf("an unknown alias offered %q", m.completion.items)
	}
	m = New(Config{InitialSQL: "SELECT * FROM peo|"})
	placeCursor(m)
	offlineCatalog(m)
	m.completeWord(true)
	if len(m.completion.items) == 0 || m.completion.items[0] != "people" || m.completion.details[0] != "table · public" {
		t.Fatalf("relation suggestions = %q %q", m.completion.items, m.completion.details)
	}
}

func TestCatalogReplyDoesNotReopenCompletionAfterTheCursorMoves(t *testing.T) {
	m := New(Config{InitialSQL: "SELECT p. FROM people p"})
	c := offlineConnection(m)
	e := &m.buffers[0].editor
	e.cursor = len("SELECT p.")
	c.completion.wait = completionWait{active: true, buffer: m.buffers[0].id, revision: e.revision, cursor: e.cursor, force: true}
	m.completeAfterCatalog = true
	press(m, tea.KeyLeft)
	if m.completeAfterCatalog {
		t.Fatal("moving the cursor kept the pending completion")
	}
	offlineCatalog(m)
	catalog := c.catalog
	m.completeAfterCatalog = true
	m.Update(catalogMsg{conn: c.id, serial: c.catalogSerial, parent: 0, nodes: nil})
	if len(m.completion.items) != 0 {
		t.Fatalf("a late catalog reply opened completion at the moved cursor: %q", m.completion.items)
	}
	c.catalog = catalog
	press(m, tea.KeyRight)
	c.completion.wait = completionWait{active: true, buffer: m.buffers[0].id, revision: e.revision, cursor: e.cursor, force: true}
	m.completeAfterCatalog = true
	m.Update(catalogMsg{conn: c.id, serial: c.catalogSerial, parent: 0, nodes: nil})
	if len(m.completion.items) == 0 {
		t.Fatal("a catalog reply at the requested cursor did not refresh completion")
	}
}

func liveCompletionModel(t *testing.T, sql string) *Model {
	t.Helper()
	dsn, _ := testConnection(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session, err := postgres.Open(ctx, dsn, postgres.Options{AllowWrites: true})
	if err != nil {
		t.Fatal(err)
	}
	m := New(Config{Session: session, DSN: dsn, InitialSQL: sql})
	closeTestModel(t, m)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	finishCommands(t, m, m.Init())
	placeCursor(m)
	return m
}

func TestLiveCompletionResolvesAliasesSchemasAndQuoting(t *testing.T) {
	m := liveCompletionModel(t, "select b.| from claims.beneficiary b")
	finishCommands(t, m, m.completeWord(true))
	want := []string{"attrs", "balance", "birth_date", "created_at", "id", "mbi", "name", "photo", "state"}
	if !slices.Equal(m.completion.items, want) {
		t.Fatalf("b. offered %q", m.completion.items)
	}
	if index := slices.Index(m.completion.items, "balance"); !strings.HasPrefix(m.completion.details[index], "column · numeric") {
		t.Fatalf("balance hint = %q", m.completion.details[index])
	}

	m = liveCompletionModel(t, "select * from claims.|")
	finishCommands(t, m, m.completeWord(true))
	if !slices.Equal(m.completion.items, []string{"beneficiary", "claim_line", "md_beneficiaries"}) {
		t.Fatalf("claims. offered %q", m.completion.items)
	}

	m = liveCompletionModel(t, "select * from |")
	typeText(t, m, "mix")
	if len(m.completion.items) == 0 || m.completion.items[0] != `"Mixed Case"` {
		t.Fatalf("mix offered %q", m.completion.items)
	}
	press(m, tea.KeyTab)
	if got := string(m.buffers[0].editor.text); got != `select * from "Mixed Case"` {
		t.Fatalf("accepted %q", got)
	}

	m = liveCompletionModel(t, `select m.| from "Mixed Case" m`)
	finishCommands(t, m, m.completeWord(true))
	if !slices.Equal(m.completion.items, []string{`"Id"`, `"select"`}) {
		t.Fatalf("Mixed Case columns = %q", m.completion.items)
	}

	m = liveCompletionModel(t, "select * from bene|")
	finishCommands(t, m, m.completeWord(true))
	if len(m.completion.items) == 0 || m.completion.items[0] != "claims.beneficiary" {
		t.Fatalf("a table outside search_path was not qualified: %q", m.completion.items)
	}
}

func TestLiveTypingLoadsColumnsForReferencesAfterTheCursor(t *testing.T) {
	m := liveCompletionModel(t, "select | from claims.claim_line l join claims.beneficiary b on b.id = l.beneficiary_id")
	typeText(t, m, "l.")
	if !slices.Contains(m.completion.items, "hcpcs") || slices.Contains(m.completion.items, "mbi") || !m.completion.auto {
		t.Fatalf("typing l. offered %q", m.completion.items)
	}
	typeText(t, m, "pa")
	if !slices.Equal(m.completion.items, []string{"paid"}) {
		t.Fatalf("l.pa offered %q", m.completion.items)
	}
	press(m, tea.KeyEsc)
	finishCommands(t, m, press(m, ' '))
	typeText(t, m, "be")
	if len(m.completion.items) == 0 || m.completion.items[0] != "beneficiary_id" {
		t.Fatalf("unqualified be offered %q", m.completion.items)
	}
	if m.conn().catalogBusy || m.completeAfterCatalog {
		t.Fatal("completion left the session marked busy")
	}

	// A table created in this session appears after the statement runs.
	m.buffers[0].editor = newEditor("create temp table completion_fresh (fresh_column int)")
	finishCommands(t, m, m.runSQL(true))
	m.focus = editorPane
	m.buffers[0].editor = newEditor("select * from completion_fr|")
	placeCursor(m)
	finishCommands(t, m, m.completeWord(true))
	if len(m.completion.items) != 1 || m.completion.items[0] != "completion_fresh" {
		t.Fatalf("new temporary table offered %q", m.completion.items)
	}
}

func TestLiveCompletionWaitsForRunningSQL(t *testing.T) {
	m := liveCompletionModel(t, "select b.| from claims.beneficiary b")
	m.buffers = append(m.buffers, buffer{id: 99, editor: newEditor("select pg_sleep(0.3)"), conn: m.buffers[0].conn, results: newResults()})
	m.bufferIndex = 1
	run := m.runSQL(true)
	m.bufferIndex = 0
	if cmd := m.completeWord(true); cmd != nil || m.conn().catalogBusy {
		t.Fatal("completion requested catalog data while SQL ran")
	}
	finishCommands(t, m, run)
	m.focus = editorPane
	if m.conn().query.running {
		t.Fatal("query did not finish")
	}
	finishCommands(t, m, m.completeWord(true))
	if !slices.Contains(m.completion.items, "mbi") {
		t.Fatalf("completion after the query offered %q", m.completion.items)
	}
}

func TestLiveTypingInsideATransactionDoesNotQueryTheCatalog(t *testing.T) {
	m := liveCompletionModel(t, "begin|")
	finishCommands(t, m, m.runSQL(true))
	m.focus = editorPane
	if m.results().batch.TxStatus != 'T' {
		t.Fatalf("transaction status = %c", m.results().batch.TxStatus)
	}
	m.buffers[0].editor = newEditor("select * from claims.beneficiary b where b.|")
	placeCursor(m)
	m.conn().completion = completionCatalog{}
	typeText(t, m, "na")
	if m.conn().catalogBusy || m.conn().completion.loaded || len(m.completion.items) != 0 {
		t.Fatal("typing read the catalog inside a transaction")
	}
	finishCommands(t, m, press(m, ' ', tea.ModCtrl))
	if !slices.Equal(m.completion.items, []string{"name"}) || m.results().batch.TxStatus != 'T' {
		t.Fatalf("Ctrl+Space in a transaction offered %q, status %c", m.completion.items, m.results().batch.TxStatus)
	}
	m.buffers[0].editor = newEditor("rollback")
	finishCommands(t, m, m.runSQL(true))
}
