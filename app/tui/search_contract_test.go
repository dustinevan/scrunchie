package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/dustinevan/scrunchie/pkg/result"
)

func TestCatalogSearchSelectsActualMatchesAndF3Wraps(t *testing.T) {
	m := New(Config{Demo: true})
	m.conn().catalog = []catalogNode{
		{kind: "schema", name: "public", parent: -1, loaded: true, children: []int{1}},
		{kind: "relation", name: "records", parent: 0, loaded: true, children: []int{2, 3, 4}},
		{kind: "column", name: "match_name", detail: "text", parent: 1, loaded: true},
		{kind: "column", name: "other", detail: "text MATCH detail", parent: 1, loaded: true},
		{kind: "column", name: "unrelated", detail: "text", parent: 1, loaded: true},
	}
	m.focusPane(catalogPane)
	m.beginSearch()
	for _, r := range "match" {
		press(m, r)
	}
	press(m, tea.KeyEnter)
	for _, want := range []string{"match_name", "other", "match_name"} {
		_, node, ok := m.selectedCatalogNode()
		if !ok || node.name != want || m.catalogFilter != "match" {
			t.Fatalf("selected catalog match = %q, want %q", node.name, want)
		}
		if anyCatalogBusy(m) || anyRunning(m) || m.connecting || m.fileBusy {
			t.Fatal("local catalog search started work")
		}
		press(m, tea.KeyF3)
	}
	visible := m.visibleCatalog(m.conn())
	if len(visible) != 4 || visible[0] != 0 || visible[1] != 1 {
		t.Fatal("catalog filtering discarded context ancestors or retained an unrelated column")
	}
	m.focusActiveDatabase()
	m.conn().collapsed = true
	press(m, tea.KeyF3)
	_, node, ok := m.selectedCatalogNode()
	if !ok || node.name != "match_name" || m.conn().collapsed {
		t.Fatal("F3 did not reveal the existing match from a collapsed database root")
	}
	m.findNext("absent")
	m.prepareView()
	if m.catalogFilter != "absent" || len(m.visibleCatalog(m.conn())) != 0 || len(m.databaseRows()) < 1 || m.databaseRows()[0].kind != databaseAdd {
		t.Fatal("a no-match search stranded database navigation")
	}
	press(m, tea.KeyF3)
	if anyRunning(m) || anyCatalogBusy(m) {
		t.Fatal("F3 on an empty filter started a query")
	}
}

func jsonSearchModel() *Model {
	m := New(Config{})
	m.results().batch.Results = []result.Set{{Columns: []result.Column{{Name: "json", OID: 3802}}, Rows: [][]result.Cell{{{Text: `{"unrelated":0,"outer":{"match":1},"other":{"match":2}}`}}}}}
	m.openInspector()
	return m
}

func selectedJSONPath(m *Model) string {
	i := &m.inspector
	if i.index < 0 || i.index >= len(i.visible) {
		return ""
	}
	return i.tree.Nodes[i.visible[i.index]].Path
}

func TestJSONFilterSearchSkipsAncestorsAndF3Wraps(t *testing.T) {
	m := jsonSearchModel()
	m.beginSearch()
	m.modal.input = newEditor("match")
	m.modal.input.singleLine = true
	press(m, tea.KeyEnter)
	for _, want := range []string{`$["outer"]["match"]`, `$["other"]["match"]`, `$["outer"]["match"]`} {
		if got := selectedJSONPath(m); got != want {
			t.Fatalf("JSON match = %q, want %q", got, want)
		}
		press(m, tea.KeyF3)
	}
	if len(m.inspector.visible) != 5 || !m.inspector.active || anyRunning(m) {
		t.Fatal("JSON search lost ancestors, changed input owner, or ran SQL")
	}
	m.findNext("missing")
	press(m, tea.KeyF3)
	press(m, tea.KeyLeft)
	press(m, tea.KeyRight)
	if len(m.inspector.visible) != 0 || !m.inspector.active {
		t.Fatal("no-match JSON filter did not remain safely inside the inspector")
	}
	press(m, tea.KeyEsc)
	if m.inspector.filter != "" || !m.inspector.active || len(m.inspector.visible) == 0 {
		t.Fatal("Escape did not restore the unfiltered JSON tree")
	}
}

func TestFilteredJSONArrowsTraverseWithoutChangingExpansion(t *testing.T) {
	m := jsonSearchModel()
	expanded := make([]bool, len(m.inspector.tree.Nodes))
	for index, node := range m.inspector.tree.Nodes {
		expanded[index] = node.Expanded
	}
	m.findNext("match")
	for _, step := range []struct {
		key  rune
		path string
	}{
		{tea.KeyLeft, `$["outer"]`},
		{tea.KeyLeft, `$`},
		{tea.KeyLeft, `$`},
		{tea.KeyRight, `$["outer"]`},
		{tea.KeyRight, `$["outer"]["match"]`},
		{tea.KeyRight, `$["outer"]["match"]`},
		{tea.KeyLeft, `$["outer"]`},
		{tea.KeyEnter, `$["outer"]["match"]`},
		{tea.KeyLeft, `$["outer"]`},
		{tea.KeySpace, `$["outer"]["match"]`},
	} {
		press(m, step.key)
		if got := selectedJSONPath(m); got != step.path {
			t.Fatalf("%s selected %q, want %q", key(step.key).String(), got, step.path)
		}
	}
	press(m, 'e')
	press(m, 'c')
	for index, node := range m.inspector.tree.Nodes {
		if node.Expanded != expanded[index] {
			t.Fatal("filtered tree navigation changed its saved expansion state")
		}
	}
	press(m, tea.KeyEsc)
	if got := selectedJSONPath(m); got != `$["outer"]` || !m.inspector.active {
		t.Fatalf("clearing filter did not select the nearest visible ancestor: %q", got)
	}
	press(m, tea.KeyRight)
	if !m.inspector.tree.Nodes[m.inspector.visible[m.inspector.index]].Expanded {
		t.Fatal("Right did not regain ordinary expansion behavior after clearing the filter")
	}
}

func TestClearingJSONFilterPreservesTheSelectedNodeWhenVisible(t *testing.T) {
	m := jsonSearchModel()
	for index, node := range m.inspector.tree.Nodes {
		if node.Key == "outer" {
			m.inspector.tree.Expand(index)
		}
	}
	m.refreshInspectorRows()
	m.findNext("match")
	before := selectedJSONPath(m)
	press(m, tea.KeyEsc)
	if got := selectedJSONPath(m); got != before || got != `$["outer"]["match"]` {
		t.Fatalf("filter clearing changed node identity from %q to %q", before, got)
	}
}
