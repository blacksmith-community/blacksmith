package yamlcodec

import (
	"fmt"

	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/token"

	"github.com/fivetwenty-io/graft/internal/yamlnode"
)

// mergeValueMessage is the error for a << merge key whose value is not a
// map or a list of maps. It is the text spruce's YAML library gives, and
// like that text it quotes no part of the document.
const mergeValueMessage = "map merge requires map or sequence of maps as the value"

// ExpandBody returns body, the root node of a document that CheckAnchors
// accepts, with every alias expanded as ExpandAliases expands it and
// every << merge key folded into its mapping as spruce's YAML library
// folds it. So goccy's decoder meets neither an alias nor a merge key.
// It rewrites body in place, as ExpandAliases does.
func ExpandBody(body ast.Node) (ast.Node, error) {
	merges := findMergeKeys(body)
	body, err := ExpandAliases(body)
	if err != nil || !merges.found {
		return body, err
	}
	for _, key := range merges.explicit {
		key.Value = explicitMergeKey(key.Value)
	}
	x := &mergeExpander{done: make(map[ast.Node]ast.Node), aliasValued: merges.aliasValued}
	return x.node(body)
}

// mergeKeys is what findMergeKeys learns of a document before its
// aliases expand. found says it holds a merge key, aliasValued holds
// each merge key pair whose value is an alias, and explicit holds each
// merge key written as "? <<", which goccy reads as a string.
type mergeKeys struct {
	found       bool
	aliasValued map[*ast.MappingValueNode]bool
	explicit    []*ast.MappingKeyNode
}

// findMergeKeys walks body, whose aliases have not expanded yet, for
// merge keys. Merge keys are rare, so a document without one skips the
// fold, and the walk visits each node of the text once.
//
// spruce's YAML library refuses a merge key whose value is an alias to a
// sequence, though it reads the same sequence written in place, so the
// pairs whose value is an alias are recorded before expansion hides
// them. A "? <<" key is recorded too, since once an alias puts a <<
// string inside an explicit key, as "? *x" does, the two look alike.
func findMergeKeys(body ast.Node) mergeKeys {
	m := mergeKeys{aliasValued: make(map[*ast.MappingValueNode]bool)}
	ast.Walk(&m, body)
	return m
}

func (m *mergeKeys) Visit(n ast.Node) ast.Visitor {
	if pair, ok := n.(*ast.MappingValueNode); ok && yamlnode.IsMergeKey(pair.Key) {
		m.found = true
		if key, ok := pair.Key.(*ast.MappingKeyNode); ok && yamlnode.ExplicitMergeKey(key) != nil {
			m.explicit = append(m.explicit, key)
		}
		if _, alias := pair.Value.(*ast.AliasNode); alias {
			m.aliasValued[pair] = true
		}
	}
	return m
}

// explicitMergeKey returns the merge key node to put inside a "? <<"
// key in place of n, the << string goccy read there, so that the key
// reads as a merge key from then on. The string itself is left as it
// is, since an alias to its anchor may name it.
func explicitMergeKey(n ast.Node) ast.Node {
	s, ok := n.(*ast.StringNode)
	if !ok || s.Token == nil {
		return n
	}
	tk := *s.Token
	tk.Type = token.MergeKeyType
	return ast.MergeKey(&tk)
}

// mergeExpander folds the merge keys of a document whose aliases have
// expanded. done holds each collection it has entered, with the node
// that replaces it, so a node that several aliases share is folded once.
// writes counts the pairs the fold has written, and aliasValued is what
// findMergeKeys recorded.
type mergeExpander struct {
	done        map[ast.Node]ast.Node
	aliasValued map[*ast.MappingValueNode]bool
	writes      int
}

// node folds the merge keys in n and under it, and returns the node to
// put in n's slot.
func (x *mergeExpander) node(n ast.Node) (ast.Node, error) {
	switch n.(type) {
	case *ast.MappingNode, *ast.MappingValueNode, *ast.SequenceNode, *ast.TagNode, *ast.MappingKeyNode, *ast.AnchorNode:
	default:
		return n, nil
	}
	if out, ok := x.done[n]; ok {
		return out, nil
	}
	// CheckAnchors refuses a node inside itself, so this entry is never
	// read before it is replaced. It only keeps a walk finite on a body
	// built by hand.
	x.done[n] = n
	out, err := x.expand(n)
	if err != nil {
		return nil, err
	}
	x.done[n] = out
	return out, nil
}

func (x *mergeExpander) expand(n ast.Node) (ast.Node, error) {
	var err error
	switch v := n.(type) {
	case *ast.TagNode:
		v.Value, err = x.node(v.Value)
	case *ast.AnchorNode:
		v.Value, err = x.node(v.Value)
	case *ast.MappingKeyNode:
		v.Value, err = x.node(v.Value)
	case *ast.SequenceNode:
		for i, item := range v.Values {
			if v.Values[i], err = x.node(item); err != nil {
				return nil, err
			}
		}
	case *ast.MappingNode:
		return x.mapping(v, v.Values)
	case *ast.MappingValueNode:
		return x.mapping(v, []*ast.MappingValueNode{v})
	}
	if err != nil {
		return nil, err
	}
	return n, nil
}

// mapping folds the merge keys of the mapping n, whose pairs are pairs,
// and returns the node to put in n's slot. A mapping without a merge
// key keeps its pairs, with the merge keys under them folded.
//
// The pairs take effect in the order written, each writing over the
// ones before it, as spruce's YAML library reads them. A merge key
// writes the pairs of the map it names, so its values win over a key
// written before it and lose to a key written after it, and a second
// merge key writes over the first. When its value is a list of maps,
// an earlier map wins over a later one. Two keys are the same key when
// a map with string keys holds them in one slot, as yamlnode.KeyID
// describes, and a key keeps the place where it was first written.
func (x *mergeExpander) mapping(n ast.Node, pairs []*ast.MappingValueNode) (ast.Node, error) {
	merged := false
	for _, pair := range pairs {
		if pair == nil {
			continue
		}
		if pair.Key.IsMergeKey() {
			merged = true
		}
		key, err := x.node(pair.Key)
		if err != nil {
			return nil, err
		}
		pair.Key = asMapKey(key, pair.Key)
		if pair.Value, err = x.node(pair.Value); err != nil {
			return nil, err
		}
	}
	if !merged {
		return n, nil
	}
	return x.fold(n, pairs)
}

// fold writes the pairs of n, a mapping whose keys and values are
// folded and which holds a merge key, into one list in the order
// mapping describes, and returns the node to put in n's slot.
func (x *mergeExpander) fold(n ast.Node, pairs []*ast.MappingValueNode) (ast.Node, error) {
	var out mergedPairs
	for _, pair := range pairs {
		if pair == nil {
			continue
		}
		if !pair.Key.IsMergeKey() {
			if err := x.put(&out, pair); err != nil {
				return nil, err
			}
			continue
		}
		sources, ok, err := x.sources(pair)
		if err != nil {
			return nil, err
		}
		if !ok {
			// An alias whose anchor the document never defines stays
			// as it is, and the decoder reports it.
			return n, nil
		}
		for i := len(sources) - 1; i >= 0; i-- {
			for _, p := range sources[i] {
				if err := x.put(&out, p); err != nil {
					return nil, err
				}
			}
		}
	}
	if m, ok := n.(*ast.MappingNode); ok {
		m.Values = out.pairs
		return m, nil
	}
	return ast.Mapping(n.GetToken(), false, out.pairs...), nil
}

// put writes pair into out, and fails once the fold has written more
// pairs than a document's aliases may expand to, so a chain of merges
// that each name the last stays within the limit ExpandAliases keeps.
func (x *mergeExpander) put(out *mergedPairs, pair *ast.MappingValueNode) error {
	x.writes++
	if x.writes > maxExpandedNodes {
		return &MaxRecursionError{cause: fmt.Errorf("merge keys expand to more than %d pairs", maxExpandedNodes)}
	}
	out.put(pair)
	return nil
}

// sources returns the pairs of each map that pair, a merge key and its
// folded value, names, in the order written. It reports false for a
// value that is an alias whose anchor is never defined, and fails a
// value that is neither a map nor a list of maps, and a value that is an
// alias to a list, as spruce's YAML library does.
func (x *mergeExpander) sources(pair *ast.MappingValueNode) ([][]*ast.MappingValueNode, bool, error) {
	value := untagged(pair.Value)
	if _, alias := value.(*ast.AliasNode); alias {
		return nil, false, nil
	}
	if pairs, ok := mapPairs(value); ok {
		return [][]*ast.MappingValueNode{pairs}, true, nil
	}
	seq, ok := value.(*ast.SequenceNode)
	if !ok || x.aliasValued[pair] {
		return nil, false, mergeValueError(pair)
	}
	out := make([][]*ast.MappingValueNode, 0, len(seq.Values))
	for _, item := range seq.Values {
		item = untagged(item)
		if _, alias := item.(*ast.AliasNode); alias {
			return nil, false, nil
		}
		pairs, ok := mapPairs(item)
		if !ok {
			return nil, false, mergeValueError(pair)
		}
		out = append(out, pairs)
	}
	return out, true, nil
}

// untagged returns n without a tag that sits on a collection, such as
// the !!map in "<<: !!map {a: 1}", which spruce's YAML library reads past.
func untagged(n ast.Node) ast.Node {
	for {
		t, ok := n.(*ast.TagNode)
		if !ok {
			return n
		}
		switch t.Value.(type) {
		case *ast.MappingNode, *ast.MappingValueNode, *ast.SequenceNode, *ast.TagNode:
			n = t.Value
		default:
			return n
		}
	}
}

// mapPairs returns the pairs of n when n is a mapping.
func mapPairs(n ast.Node) ([]*ast.MappingValueNode, bool) {
	switch v := n.(type) {
	case *ast.MappingNode:
		return v.Values, true
	case *ast.MappingValueNode:
		return []*ast.MappingValueNode{v}, true
	}
	return nil, false
}

// mergeValueError is the error for pair, a merge key whose value no
// merge may take. It is placed at the merge key, since the value an
// alias names may lie anywhere in the document.
func mergeValueError(pair *ast.MappingValueNode) error {
	line, col := position(pair.Key.GetToken())
	return &yamlnode.ParseError{Line: line, SourceLine: line, SourceColumn: col, Message: mergeValueMessage}
}

// mergedPairs is the pairs a folded mapping holds, with the index in
// pairs of each key that a map with string keys holds in one slot. A
// key that is not a scalar has no slot, and every one is kept.
type mergedPairs struct {
	pairs []*ast.MappingValueNode
	at    map[string]int
}

func (m *mergedPairs) put(pair *ast.MappingValueNode) {
	id, ok := yamlnode.KeyIDOf(pair.Key)
	if !ok {
		m.pairs = append(m.pairs, pair)
		return
	}
	if i, seen := m.at[id.Slot()]; seen {
		m.pairs[i] = pair
		return
	}
	if m.at == nil {
		m.at = make(map[string]int)
	}
	m.at[id.Slot()] = len(m.pairs)
	m.pairs = append(m.pairs, pair)
}
