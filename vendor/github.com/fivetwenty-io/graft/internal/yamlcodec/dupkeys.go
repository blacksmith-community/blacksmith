package yamlcodec

import (
	"strconv"

	"github.com/goccy/go-yaml/ast"

	"github.com/fivetwenty-io/graft/internal/yamlnode"
	"github.com/fivetwenty-io/graft/internal/yamlprep"
)

// DuplicateKey is a later occurrence of a key in one mapping, as
// WalkDuplicateKeys reports it. Path names the keys on the way to it.
type DuplicateKey struct {
	Path        string
	Line        int
	Column      int
	FirstLine   int
	FirstColumn int
}

// ParseMergeBodyDuplicates is ParseMergeBody for a caller that accepts a
// repeated key. The parse allows a key to repeat in any mapping, and
// report receives each later occurrence, in document order, before any
// alias expands. A decode of the result must allow duplicate keys too,
// and it then keeps the last value of each key.
func ParseMergeBodyDuplicates(doc []byte, tag bool, report func(DuplicateKey)) (ast.Node, error) {
	doc, _ = yamlprep.Prepare(doc)
	file, err := yamlnode.ParseBytesWithDuplicates(doc, 0)
	if err != nil {
		return nil, err
	}
	body := yamlnode.FirstBody(file)
	if body == nil {
		return nil, nil
	}
	WalkDuplicateKeys(body, report)
	return PrepareBody(body, tag)
}

// WalkDuplicateKeys reports each later occurrence of a scalar key in a
// mapping of body, in document order, to report. Merge keys, and keys
// that are aliases or collections, are never reported. A node reached
// through an alias is not walked again, since the anchor's own place
// holds it.
func WalkDuplicateKeys(body ast.Node, report func(DuplicateKey)) {
	w := dupWalker{report: report}
	w.node(body, "")
}

type dupWalker struct {
	report func(DuplicateKey)
}

func (w dupWalker) node(n ast.Node, path string) {
	switch x := n.(type) {
	case *ast.AnchorNode:
		w.node(x.Value, path)
	case *ast.TagNode:
		w.node(x.Value, path)
	case *ast.MappingKeyNode:
		w.node(x.Value, path)
	case *ast.MappingNode:
		w.mapping(x.Values, path)
	case *ast.MappingValueNode:
		w.mapping([]*ast.MappingValueNode{x}, path)
	case *ast.SequenceNode:
		for i, item := range x.Values {
			w.node(item, path+"["+strconv.Itoa(i)+"]")
		}
	}
}

// mapping walks the pairs of one mapping in order. It reports a pair
// whose key repeats an earlier one, as yamlnode.KeyID describes, before
// it walks the pair's value.
func (w dupWalker) mapping(pairs []*ast.MappingValueNode, path string) {
	var keys yamlnode.KeySet[keyPos]
	for _, pair := range pairs {
		if pair == nil {
			continue
		}
		child := path
		if id, pos, ok := scalarKey(pair.Key); ok {
			if child != "" {
				child += "."
			}
			child += id.Text
			if at, repeat := keys.Add(id, pos); repeat {
				w.report(DuplicateKey{Path: child, Line: pos.line, Column: pos.col, FirstLine: at.line, FirstColumn: at.col})
			}
		}
		w.node(pair.Value, child)
	}
}

type keyPos struct{ line, col int }

// scalarKey returns the KeyID and position of key when it is a scalar
// that can repeat, and false for a merge key, an alias, or a collection.
// A key written after "?" is placed at the key, or at its first tag or
// anchor, as the Node route places it, and not at the "?".
func scalarKey(key ast.MapKeyNode) (yamlnode.KeyID, keyPos, bool) {
	var pos keyPos
	if yamlnode.IsMergeKey(key) {
		return yamlnode.KeyID{}, pos, false
	}
	var placed ast.Node = key
	if explicit, ok := key.(*ast.MappingKeyNode); ok && explicit.Value != nil {
		placed = explicit.Value
	}
	if tk := placed.GetToken(); tk != nil && tk.Position != nil {
		pos = keyPos{tk.Position.Line, tk.Position.Column}
	}
	id, ok := yamlnode.KeyIDOf(key)
	return id, pos, ok
}
