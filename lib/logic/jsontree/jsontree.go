// Package jsontree preserves JSON member order and numeric text for inspection.
package jsontree

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/dustinevan/scrunchie/pkg/termtext"
)

const (
	MaxDocumentBytes = 16 << 20
	MaxNodes         = 100_000
	MaxDepth         = 256
	MaxPathLength    = 64 << 10
	MaxPathBytes     = 16 << 20
)

// Node is one JSON value. Nodes are stored in document order, with parents
// before children. Value retains the exact input bytes for this subtree.
// Kind is the first JSON byte: {, [, ", n, t, f, a digit, or minus.
// Path uses $["key"] and $[0]; duplicate object keys append #2, #3, and so on.
// Occurrence is 1 for a first object member, and 0 for roots or array elements.
type Node struct {
	Key        string
	Path       string
	Value      json.RawMessage
	Parent     int
	Depth      int
	Container  bool
	Expanded   bool
	Kind       byte
	Children   []int
	Occurrence int
}

// Tree owns an independent copy of the input and each node's expansion state.
// The zero value is an empty tree.
type Tree struct {
	Nodes []Node
}

type parser struct {
	data      []byte
	decoder   *json.Decoder
	tree      Tree
	pathBytes int
}

// Parse accepts one UTF-8 JSON document and starts with its root expanded.
// It rejects input exceeding the document, node, depth, or path limits above.
// A token decoder retains object order and duplicate keys; UseNumber prevents
// a floating-point conversion from changing large integers or decimal text.
func Parse(data []byte) (Tree, error) {
	if len(data) > MaxDocumentBytes {
		return Tree{}, fmt.Errorf("parsing JSON: document exceeds %d bytes", MaxDocumentBytes)
	}
	if !utf8.Valid(data) {
		return Tree{}, fmt.Errorf("parsing JSON: invalid UTF-8")
	}
	owned := bytes.Clone(data)
	decoder := json.NewDecoder(bytes.NewReader(owned))
	decoder.UseNumber()
	p := parser{data: owned, decoder: decoder}
	if err := p.readNode(-1, "", "$", 0, 0); err != nil {
		return Tree{}, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err != nil {
			return Tree{}, fmt.Errorf("parsing JSON at byte %d: %w", decoder.InputOffset(), err)
		}
		return Tree{}, fmt.Errorf("parsing JSON: multiple top-level values")
	}
	return p.tree, nil
}

func (p *parser) readNode(parent int, key, path string, depth, occurrence int) error {
	if depth > MaxDepth {
		return fmt.Errorf("parsing JSON: nesting exceeds %d levels", MaxDepth)
	}
	if len(p.tree.Nodes) >= MaxNodes {
		return fmt.Errorf("parsing JSON: document exceeds %d nodes", MaxNodes)
	}
	if len(path) > MaxPathLength || p.pathBytes+len(path) > MaxPathBytes {
		return fmt.Errorf("parsing JSON: paths exceed the inspection size limit")
	}
	p.pathBytes += len(path)
	start := int(p.decoder.InputOffset())
	for start < len(p.data) {
		c := p.data[start]
		if c != ' ' && c != '\n' && c != '\r' && c != '\t' && c != ':' && c != ',' {
			break
		}
		start++
	}
	token, err := p.decoder.Token()
	if err != nil {
		return fmt.Errorf("parsing JSON at byte %d: %w", p.decoder.InputOffset(), err)
	}
	if start >= len(p.data) {
		return fmt.Errorf("parsing JSON: missing value")
	}
	index := len(p.tree.Nodes)
	node := Node{
		Key: key, Path: path, Parent: parent, Depth: depth,
		Kind: p.data[start], Occurrence: occurrence,
	}
	delimiter, container := token.(json.Delim)
	if container && depth >= MaxDepth {
		return fmt.Errorf("parsing JSON: nesting exceeds %d levels", MaxDepth)
	}
	if container && delimiter != '{' && delimiter != '[' {
		return fmt.Errorf("parsing JSON: unexpected closing delimiter at byte %d", start)
	}
	node.Container = container
	node.Expanded = container && parent == -1
	p.tree.Nodes = append(p.tree.Nodes, node)
	if parent >= 0 {
		p.tree.Nodes[parent].Children = append(p.tree.Nodes[parent].Children, index)
	}
	if !container {
		p.tree.Nodes[index].Value = p.data[start:p.decoder.InputOffset()]
		return nil
	}
	occurrences := make(map[string]int)
	for p.decoder.More() {
		childKey := ""
		childPath := ""
		childOccurrence := 0
		if delimiter == '{' {
			keyToken, err := p.decoder.Token()
			if err != nil {
				return fmt.Errorf("parsing JSON object key at byte %d: %w", p.decoder.InputOffset(), err)
			}
			var ok bool
			childKey, ok = keyToken.(string)
			if !ok {
				return fmt.Errorf("parsing JSON: object key at byte %d is not a string", p.decoder.InputOffset())
			}
			occurrences[childKey]++
			childOccurrence = occurrences[childKey]
			quoted, err := json.Marshal(childKey)
			if err != nil {
				return fmt.Errorf("parsing JSON object path: %w", err)
			}
			childPath = path + "[" + string(quoted) + "]"
			if childOccurrence > 1 {
				childPath += "#" + strconv.Itoa(childOccurrence)
			}
		} else {
			childKey = strconv.Itoa(len(p.tree.Nodes[index].Children))
			childPath = path + "[" + childKey + "]"
		}
		if err := p.readNode(index, childKey, childPath, depth+1, childOccurrence); err != nil {
			return err
		}
	}
	closing, err := p.decoder.Token()
	if err != nil {
		return fmt.Errorf("parsing JSON closing delimiter at byte %d: %w", p.decoder.InputOffset(), err)
	}
	expected := json.Delim('}')
	if delimiter == '[' {
		expected = ']'
	}
	if closing != expected {
		return fmt.Errorf("parsing JSON: unexpected closing delimiter at byte %d", p.decoder.InputOffset())
	}
	p.tree.Nodes[index].Value = p.data[start:p.decoder.InputOffset()]
	return nil
}

// Visible returns node indices in display order, excluding collapsed children.
func (t Tree) Visible() []int {
	visible := make([]int, 0, len(t.Nodes))
	hiddenDepth := -1
	for i, node := range t.Nodes {
		if hiddenDepth >= 0 && node.Depth > hiddenDepth {
			continue
		}
		hiddenDepth = -1
		visible = append(visible, i)
		if node.Container && !node.Expanded {
			hiddenDepth = node.Depth
		}
	}
	return visible
}

// Toggle changes a container's expansion state; invalid indices are ignored.
func (t *Tree) Toggle(index int) {
	if index >= 0 && index < len(t.Nodes) && t.Nodes[index].Container {
		t.Nodes[index].Expanded = !t.Nodes[index].Expanded
	}
}

// Expand opens a container without changing its descendants' expansion state.
func (t *Tree) Expand(index int) {
	if index >= 0 && index < len(t.Nodes) && t.Nodes[index].Container {
		t.Nodes[index].Expanded = true
	}
}

// Collapse closes a container without changing its descendants' expansion state.
func (t *Tree) Collapse(index int) {
	if index >= 0 && index < len(t.Nodes) && t.Nodes[index].Container {
		t.Nodes[index].Expanded = false
	}
}

// ExpandAll opens every container.
func (t *Tree) ExpandAll() {
	for i := range t.Nodes {
		if t.Nodes[i].Container {
			t.Nodes[i].Expanded = true
		}
	}
}

// CollapseAll closes every container, including the root.
func (t *Tree) CollapseAll() {
	for i := range t.Nodes {
		if t.Nodes[i].Container {
			t.Nodes[i].Expanded = false
		}
	}
}

// Pretty indents the exact JSON subtree at index. The result is raw data;
// terminal callers must escape it with termtext.SafeMultiline before display.
func (t Tree) Pretty(index int) (string, error) {
	if index < 0 || index >= len(t.Nodes) {
		return "", fmt.Errorf("formatting JSON subtree: node index %d is out of range", index)
	}
	estimate := len(t.Nodes[index].Value)
	for i := index; i < len(t.Nodes); i++ {
		if i > index && t.Nodes[i].Depth <= t.Nodes[index].Depth {
			break
		}
		estimate += 4 * (t.Nodes[i].Depth - t.Nodes[index].Depth + 1)
		if estimate > 32<<20 {
			return "", fmt.Errorf("formatting JSON subtree: indented output would exceed 32 MiB")
		}
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, t.Nodes[index].Value, "", "  "); err != nil {
		return "", fmt.Errorf("formatting JSON subtree: %w", err)
	}
	return pretty.String(), nil
}

// Display returns a terminal-safe row including indentation, marker, and key.
func (t Tree) Display(index int) string {
	if index < 0 || index >= len(t.Nodes) {
		return ""
	}
	label, value := t.DisplayParts(index)
	return label + ": " + value
}

// DisplayParts returns Display's row split at the separator: the terminal-safe
// indentation, marker, and key, then the terminal-safe value or container
// summary. Keys may themselves contain ": ", so callers that style the two
// halves differently use this instead of splitting Display.
func (t Tree) DisplayParts(index int) (label, value string) {
	if index < 0 || index >= len(t.Nodes) {
		return "", ""
	}
	node := t.Nodes[index]
	label = "$"
	if node.Parent >= 0 {
		if t.Nodes[node.Parent].Kind == '[' {
			label = "[" + node.Key + "]"
		} else {
			label = strconv.Quote(node.Key)
			if node.Occurrence > 1 {
				label += " (duplicate " + strconv.Itoa(node.Occurrence) + ")"
			}
		}
	}
	marker := "  "
	if node.Container {
		marker = "▸ "
		if node.Expanded {
			marker = "▾ "
		}
		count := len(node.Children)
		if node.Kind == '{' {
			value = "{" + strconv.Itoa(count) + " keys}"
		} else {
			value = "[" + strconv.Itoa(count) + " items]"
		}
	} else {
		if len(node.Value) > 512 {
			end := 512
			for end > 0 && !utf8.RuneStart(node.Value[end]) {
				end--
			}
			value = string(node.Value[:end]) + fmt.Sprintf("… (%d bytes)", len(node.Value))
		} else {
			value = string(node.Value)
		}
	}
	return termtext.Safe(strings.Repeat("  ", node.Depth) + marker + label), termtext.Safe(value)
}
