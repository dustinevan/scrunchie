package jsontree

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestParsePreservesOrderDuplicatesAndNumberText(t *testing.T) {
	input := []byte(`{"b":9007199254740993,"a":[1e400,{"x":true}],"b":null}`)
	original := bytes.Clone(input)
	tree, err := Parse(input)
	if err != nil {
		t.Fatal(err)
	}
	wantPaths := []string{"$", `$["b"]`, `$["a"]`, `$["a"][0]`, `$["a"][1]`, `$["a"][1]["x"]`, `$["b"]#2`}
	if len(tree.Nodes) != len(wantPaths) {
		t.Fatalf("got %d nodes, want %d", len(tree.Nodes), len(wantPaths))
	}
	for i, path := range wantPaths {
		if tree.Nodes[i].Path != path {
			t.Errorf("node %d path %q, want %q", i, tree.Nodes[i].Path, path)
		}
	}
	if string(tree.Nodes[1].Value) != "9007199254740993" || string(tree.Nodes[3].Value) != "1e400" || tree.Nodes[6].Occurrence != 2 {
		t.Fatal("numbers or duplicate members changed")
	}
	input[0] = '!'
	pretty, err := tree.Pretty(0)
	if err != nil {
		t.Fatal(err)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, []byte(pretty)); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(compact.Bytes(), original) {
		t.Errorf("pretty JSON changed the document: %s", pretty)
	}
}

func TestExpansionRetainsDescendantState(t *testing.T) {
	tree, err := Parse([]byte(`{"a":[1,{"b":2}],"c":3}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := tree.Visible(); !sameIndices(got, []int{0, 1, 5}) {
		t.Fatalf("initial visible nodes: %v", got)
	}
	tree.Expand(1)
	tree.Expand(3)
	tree.Collapse(1)
	if got := tree.Visible(); !sameIndices(got, []int{0, 1, 5}) {
		t.Errorf("collapsed visible nodes: %v", got)
	}
	tree.Toggle(1)
	if got := tree.Visible(); !sameIndices(got, []int{0, 1, 2, 3, 4, 5}) {
		t.Errorf("descendant expansion was lost: %v", got)
	}
	tree.CollapseAll()
	if got := tree.Visible(); !sameIndices(got, []int{0}) {
		t.Errorf("CollapseAll: %v", got)
	}
	tree.ExpandAll()
	tree.Toggle(-1)
	tree.Expand(99)
	tree.Collapse(99)
	if len(tree.Visible()) != 6 {
		t.Fatal("invalid indices or ExpandAll changed visibility")
	}
}

func sameIndices(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestParseAcceptsScalarRoots(t *testing.T) {
	for _, input := range []string{`null`, `true`, `false`, `"text"`, `-1.23e400`, `[]`, `{}`} {
		tree, err := Parse([]byte(input))
		if err != nil || len(tree.Nodes) != 1 || string(tree.Nodes[0].Value) != input {
			t.Errorf("%s: %+v, %v", input, tree, err)
		}
	}
}

func TestParseRejectsInvalidAndExcessiveDocuments(t *testing.T) {
	inputs := []string{
		"", "{", "[1,]", "{} {}", `{"x":}`, "NaN", string([]byte{0xff}),
		strings.Repeat(" ", MaxDocumentBytes+1),
		strings.Repeat("[", MaxDepth+1) + strings.Repeat("]", MaxDepth+1),
		"[" + strings.Repeat("0,", MaxNodes) + "0]",
		`{"` + strings.Repeat("k", MaxPathLength) + `":0}`,
		strings.Repeat(`{"long_path_component_0123456789":`, 128) + "[" + strings.Repeat("0,", 5000) + "0]" + strings.Repeat("}", 128),
	}
	for i, input := range inputs {
		if _, err := Parse([]byte(input)); err == nil {
			t.Errorf("case %d accepted invalid or excessive JSON (%d bytes)", i, len(input))
		}
	}
}

func TestDisplayBoundsScalarAndEscapesKeys(t *testing.T) {
	input := `{"\u001b\u202e":"` + strings.Repeat("é", 100_000) + `"}`
	tree, err := Parse([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	got := tree.Display(1)
	if len(got) > 4096 || !utf8.ValidString(got) || strings.ContainsAny(got, "\x1b\u202e") || !strings.Contains(got, "bytes)") {
		t.Errorf("unsafe or unbounded tree row: %d bytes", len(got))
	}
	if _, err := tree.Pretty(-1); err == nil {
		t.Fatal("Pretty accepted an invalid index")
	}
}
