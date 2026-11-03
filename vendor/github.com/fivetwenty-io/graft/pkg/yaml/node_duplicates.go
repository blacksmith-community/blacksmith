package yaml

import (
	"strconv"

	"github.com/fivetwenty-io/graft/internal/yamlnode"
)

// collapseDuplicates walks the tree under root in document order. For
// each later occurrence of a scalar key in a mapping it calls report,
// and it drops the later pair after moving its value into the first
// pair, so the mapping keeps one pair for the key, holding the last
// value. Merge keys are never reported, and the merge keys of one
// mapping become one, as mapping describes. Keys that are aliases or
// collections are left alone. A node reached a second time, through an
// alias, is not walked again.
func collapseDuplicates(root *Node, report func(DuplicateKey)) {
	w := nodeDupWalker{report: report, seen: make(map[*Node]bool)}
	w.walk(root, "")
}

type nodeDupWalker struct {
	report func(DuplicateKey)
	seen   map[*Node]bool
}

func (w nodeDupWalker) walk(n *Node, path string) {
	if n == nil || w.seen[n] {
		return
	}
	w.seen[n] = true
	switch n.Kind {
	case DocumentNode:
		for _, child := range n.Content {
			w.walk(child, path)
		}
	case SequenceNode:
		for i, item := range n.Content {
			w.walk(item, path+"["+strconv.Itoa(i)+"]")
		}
	case MappingNode:
		w.mapping(n, path)
	case ScalarNode, AliasNode:
	}
}

// mapping walks the pairs of one mapping node, and rebuilds its Content
// without the later pairs of a repeated key, so that a decode of the
// mapping gives what a decode of the text with repeats allowed gives,
// which reads the pairs in turn and lets each write over the ones
// before it. A key repeats an earlier one as yamlnode.KeyID describes,
// and a later pair's value moves into the last pair kept whose key
// reads as the same key in a map.
//
// Merge keys are never reported. The first one keeps its place, and when
// the mapping holds more than one, its value becomes a sequence of the
// sources of every merge key, in order, which a decode merges with a key
// in a later source winning. A decode lets a merged value win over a key
// written before the merge key and lets a key written after it win, so a
// pair whose key a source supplies moves to the side of the merge key
// that keeps the value written last, as arrange describes.
func (w nodeDupWalker) mapping(n *Node, path string) {
	var keys yamlnode.KeySet[*Node]
	lastAt := make(map[string]int, len(n.Content)/2)
	var kept []keptPair
	var m merges
	for i := 0; i+1 < len(n.Content); i += 2 {
		key, val := n.Content[i], n.Content[i+1]
		if key != nil && key.Kind == ScalarNode && key.Tag == yamlnode.TagMerge {
			w.walk(val, path)
			if m.add(i, val, len(kept)) {
				kept = append(kept, keptPair{key: key, val: val, last: i})
			} else {
				kept[m.at].val = m.value
			}
			continue
		}
		pair := keptPair{key: key, val: val, last: i}
		child := path
		if key != nil && key.Kind == ScalarNode {
			if child != "" {
				child += "."
			}
			child += key.Value
			id := yamlnode.KeyID{Text: key.Value, Null: yamlnode.ShortTag(key.Tag) == yamlnode.TagNull}
			pair.slot, pair.scalar = id.Slot(), true
			if first, repeat := keys.Add(id, key); repeat {
				w.report(DuplicateKey{Path: child, Line: key.Line, Column: key.Column, FirstLine: first.Line, FirstColumn: first.Column})
				w.walk(val, child)
				at := lastAt[id.Slot()]
				kept[at].val, kept[at].last = val, i
				continue
			}
			lastAt[id.Slot()] = len(kept)
		}
		w.walk(val, child)
		kept = append(kept, pair)
	}
	out := make([]*Node, 0, len(n.Content))
	for _, pair := range m.arrange(kept) {
		out = append(out, pair.key, pair.val)
	}
	if len(n.Content)%2 == 1 {
		out = append(out, n.Content[len(n.Content)-1])
	}
	n.Content = out
}

// keptPair is a pair mapping keeps. last is the index in Content of the
// last pair whose value it holds, slot is the key a map holds for its
// key, and scalar says its key is a scalar, and so has a slot.
type keptPair struct {
	key, val *Node
	last     int
	slot     string
	scalar   bool
}

// merges gathers the merge keys of one mapping. at is the index among
// the kept pairs of the first, value is the value that pair holds, and
// supplied gives, for each key a source supplies, the index in Content
// of the last merge key with a source that supplies it.
type merges struct {
	at       int
	count    int
	value    *Node
	supplied map[string]int
}

// add records a merge key with the value val at index i of Content,
// where at is the number of pairs kept so far. It returns true for the
// first merge key, which the caller keeps as a pair, and false for a
// later one, whose sources join value.
func (m *merges) add(i int, val *Node, at int) bool {
	if m.supplied == nil {
		m.supplied = make(map[string]int)
	}
	for slot := range suppliedKeys(val, make(map[*Node]bool)) {
		m.supplied[slot] = i
	}
	m.count++
	if m.count == 1 {
		m.at, m.value = at, val
		return true
	}
	if m.count == 2 {
		m.value = mergeSequence(m.value)
	}
	m.value.Content = append(m.value.Content, sourcesOf(val)...)
	return false
}

// arrange returns kept in the order a decode reads as the pairs in
// turn. A pair whose key no source supplies, or whose key is not a
// scalar, stays where it is. A pair whose key a source supplies goes
// after the merge key when it was written after the last merge key
// that supplies it, so its value wins, and before the merge key
// otherwise, so the merged value wins. Pairs keep their order on each
// side, and with one merge key and no repeats nothing moves.
func (m *merges) arrange(kept []keptPair) []keptPair {
	if m.count == 0 {
		return kept
	}
	before := make([]keptPair, 0, len(kept))
	var after []keptPair
	for i, pair := range kept {
		if i == m.at {
			continue
		}
		side := i > m.at
		if last, ok := m.supplied[pair.slot]; ok && pair.scalar {
			side = pair.last > last
		}
		if side {
			after = append(after, pair)
		} else {
			before = append(before, pair)
		}
	}
	return append(append(before, kept[m.at]), after...)
}

// suppliedKeys returns the keys a map holds for the keys the merge
// sources that n, the value of a merge key, name, with the keys their
// own merge keys bring in. seen holds the nodes already read.
func suppliedKeys(n *Node, seen map[*Node]bool) map[string]bool {
	out := make(map[string]bool)
	for _, src := range sourcesOf(n) {
		if src.Kind == AliasNode && src.Alias != nil {
			src = src.Alias
		}
		if src.Kind != MappingNode || seen[src] {
			continue
		}
		seen[src] = true
		for i := 0; i+1 < len(src.Content); i += 2 {
			key := src.Content[i]
			switch {
			case key == nil || key.Kind != ScalarNode:
			case key.Tag == yamlnode.TagMerge:
				for slot := range suppliedKeys(src.Content[i+1], seen) {
					out[slot] = true
				}
			default:
				out[yamlnode.KeyID{Text: key.Value, Null: yamlnode.ShortTag(key.Tag) == yamlnode.TagNull}.Slot()] = true
			}
		}
	}
	return out
}

// mergeSequence returns a flow sequence at first's place of the merge
// sources that first, the value of a merge key, names. A merge key with
// that sequence as its value merges the sources in turn, as the merge
// keys that named them would, and the sources of a later merge key are
// appended to it.
func mergeSequence(first *Node) *Node {
	out := &Node{
		Kind: SequenceNode, Tag: yamlnode.TagSeq, Line: first.Line, Column: first.Column,
		style: yamlnode.StyleFlow, text: first.text, opts: first.opts,
	}
	out.Content = append(out.Content, sourcesOf(first)...)
	return out
}

// sourcesOf returns the merge sources n, the value of a merge key, names.
func sourcesOf(n *Node) []*Node {
	switch {
	case n.Kind == SequenceNode:
		return n.Content
	case n.Kind == AliasNode && n.Alias != nil && n.Alias.Kind == SequenceNode:
		return n.Alias.Content
	}
	return []*Node{n}
}
