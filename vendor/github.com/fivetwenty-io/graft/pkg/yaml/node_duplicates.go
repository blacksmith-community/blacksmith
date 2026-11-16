package yaml

import (
	"slices"
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
//
// When last is set, the last repeat's pair is kept at its own place, and
// every earlier pair with the same slot is dropped instead. A decode
// into a map reads keys such as 1 and +1 as one key, and the last pair
// that gives a key wins, so a pair written between two repeats must
// still lose to the later one. The pairs with the same slot hold keys
// that a map with string keys holds as one, such as ~ and null, which
// the later repeat writes over in any case. A Node keeps the first
// place, and only a decode that goes on into Go values sets last. A
// mapping with a merge key keeps the first place either way, since the
// merge fold of the text route keeps each key where it was first
// written, as yamlcodec's mergeExpander does.
//
// When last is set, it returns the pairs each mapping it changed held
// before, or nil when it changed none. The decode into Go values reads
// those pairs, as writtenPairs describes.
func collapseDuplicates(root *Node, report func(DuplicateKey), last bool) writtenPairs {
	w := &nodeDupWalker{report: report, seen: make(map[*Node]bool), last: last}
	w.walk(root, "")
	return w.written
}

type nodeDupWalker struct {
	report  func(DuplicateKey)
	seen    map[*Node]bool
	written writtenPairs
	last    bool
}

// writtenPairs holds, for each mapping whose pairs OnDuplicateKey's fold
// changed on the way to a decode into Go values, the Content it held
// before the fold, with every pair the text wrote. The fold reads a key
// as its text, so it takes keys such as a null and the string null, or
// 1 and "1", for one key, though a map with interface keys holds them
// as two. The decode reads these pairs in turn, as it reads the text,
// and the Nodes that methods receive keep the fold.
type writtenPairs map[*Node][]*Node

func (w *nodeDupWalker) walk(n *Node, path string) {
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
// sources of every merge key, with the sources of a later merge key
// before those of an earlier one. A decode merges that sequence with a
// key in an earlier source winning, so a later merge key still wins over
// an earlier one, as it does in the text. A decode lets a merged value
// win over a key written before the merge key and lets a key written
// after it win, so a pair whose key a source supplies moves to the side
// of the merge key that keeps the value written last, as arrange
// describes.
//
// When the value of any merge key is one a decode refuses, as
// refusedMerge describes, the merge keys stay as they are, so that a
// decode fails at the merge key the text route fails at, and not at a
// sequence that hides an alias to a list.
func (w *nodeDupWalker) mapping(n *Node, path string) {
	var keys yamlnode.KeySet[*Node]
	lastAt := make(map[string]int, len(n.Content)/2)
	var kept []keptPair
	var m merges
	combine := !refusedMerge(n)
	move := w.last && !holdsMergeKey(n)
	for i := 0; i+1 < len(n.Content); i += 2 {
		key, val := n.Content[i], n.Content[i+1]
		if isMergeKey(key) {
			w.walk(val, path)
			if !combine {
				kept = append(kept, keptPair{key: key, val: val, last: i})
				continue
			}
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
			first, repeat := keys.Add(id, key)
			if repeat {
				w.report(DuplicateKey{Path: child, Line: key.Line, Column: key.Column, FirstLine: first.Line, FirstColumn: first.Column})
			}
			at, held := lastAt[id.Slot()]
			switch {
			case repeat && !move:
				w.walk(val, child)
				kept[at].val, kept[at].last = val, i
				continue
			case repeat:
				dropSlot(kept, at)
			case held:
				pair.prev = at + 1
			}
			lastAt[id.Slot()] = len(kept)
		}
		w.walk(val, child)
		kept = append(kept, pair)
	}
	w.replace(n, contentOf(m.arrange(kept), n.Content))
}

// replace sets the Content of the mapping n to pairs. When last is set
// and pairs differ from what n held, it keeps what n held in written.
func (w *nodeDupWalker) replace(n *Node, pairs []*Node) {
	if w.last && !slices.Equal(pairs, n.Content) {
		if w.written == nil {
			w.written = make(writtenPairs)
		}
		w.written[n] = n.Content
	}
	n.Content = pairs
}

// contentOf returns the Content of a mapping that holds pairs, leaving
// out the pairs a later repeat dropped. A lone key at the end of old,
// the mapping's Content before, stays at the end.
func contentOf(pairs []keptPair, old []*Node) []*Node {
	out := make([]*Node, 0, len(old))
	for _, pair := range pairs {
		if !pair.dropped {
			out = append(out, pair.key, pair.val)
		}
	}
	if len(old)%2 == 1 {
		out = append(out, old[len(old)-1])
	}
	return out
}

// isMergeKey reports whether key is a << merge key.
func isMergeKey(key *Node) bool {
	return key != nil && key.Kind == ScalarNode && key.Tag == yamlnode.TagMerge
}

// holdsMergeKey reports whether the mapping n holds a << merge key.
func holdsMergeKey(n *Node) bool {
	for i := 0; i < len(n.Content); i += 2 {
		if isMergeKey(n.Content[i]) {
			return true
		}
	}
	return false
}

// refusedMerge reports whether the mapping n holds a merge key whose
// value a decode refuses. That is a value that is neither a map nor a
// list of maps, a list that holds anything but maps, and an alias to a
// list, which spruce refuses though it takes the same list written in
// place.
func refusedMerge(n *Node) bool {
	for i := 0; i+1 < len(n.Content); i += 2 {
		if !isMergeKey(n.Content[i]) {
			continue
		}
		val := n.Content[i+1]
		if val == nil {
			return true
		}
		if val.Kind == AliasNode {
			if val.Alias == nil || val.Alias.Kind != MappingNode {
				return true
			}
			continue
		}
		if val.Kind == SequenceNode {
			for _, item := range val.Content {
				if item != nil && item.Kind == AliasNode {
					item = item.Alias
				}
				if item == nil || item.Kind != MappingNode {
					return true
				}
			}
			continue
		}
		if val.Kind != MappingNode {
			return true
		}
	}
	return false
}

// keptPair is a pair mapping keeps. last is the index in Content of the
// last pair whose value it holds, slot is the key a map holds for its
// key, and scalar says its key is a scalar, and so has a slot. dropped
// says a later repeat's pair takes its place, and prev, when not 0, is
// one more than the index among the kept pairs of the pair before it
// with the same slot, which a repeat drops as well.
type keptPair struct {
	key, val *Node
	last     int
	slot     string
	scalar   bool
	dropped  bool
	prev     int
}

// dropSlot drops the kept pair at index at and every pair before it
// with the same slot.
func dropSlot(kept []keptPair, at int) {
	for {
		kept[at].dropped = true
		if kept[at].prev == 0 {
			return
		}
		at = kept[at].prev - 1
	}
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
// later one, whose sources go at the front of value, so that they win
// over the sources of the merge keys before it.
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
	m.value.Content = append(append([]*Node(nil), sourcesOf(val)...), m.value.Content...)
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
// that sequence as its value merges the sources as the merge key that
// named them would, and the sources of a later merge key go at its
// front.
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
