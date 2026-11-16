package yaml

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/fivetwenty-io/graft/internal/cursorpath"
	"github.com/fivetwenty-io/graft/internal/yamlnode"
)

// located is where a path ends in a Document's tree.
type located struct {
	// node is the value the path names. When the value as written is an
	// alias, node is the node the alias refers to.
	node *yamlnode.Node
	// key is node's key when parent is a mapping, and nil otherwise.
	key *yamlnode.Node
	// parent is the mapping or sequence that holds the value, or the
	// DocumentNode for the empty path.
	parent *yamlnode.Node
	// index is the value's index in parent.Content. parent.Content[index]
	// is the value as written, which is the alias itself when isAlias is
	// set.
	index int
	// isAlias says the value as written is an alias.
	isAlias bool
	// throughAlias says the walk went through an alias above the value,
	// so parent sits inside an anchored node that is written elsewhere.
	throughAlias bool
	// anchor says some node the walk passed has an anchor, the value
	// included.
	anchor bool
}

// enter makes n, as written in parent.Content at index, the value l names.
func (l *located) enter(n *yamlnode.Node) {
	l.isAlias = n.Kind == yamlnode.AliasNode
	l.node = yamlnode.FollowAlias(n)
	if l.node.Anchor != "" {
		l.anchor = true
	}
}

// resolve finds the node path names in d's one document. It reads the
// path as graft's cursor paths read, and walks it as pkg/graft/tree
// does, except that it never expands a << merge and reads a scalar by its
// text. op is the operation the caller reports in an error.
//
// In a mapping, a segment names the key whose text it is, and a key that
// appears twice is ambiguous. In a sequence, a segment shaped field=value
// names the first item that is a mapping whose field holds that text. A
// segment of digits names the item at that index. Any other segment names
// the first item whose name, key, or id field holds its text, trying the
// fields in that order.
func (d *Document) resolve(op, path string) (located, error) {
	fail := func(sentinel error, line int, reason string) (located, error) {
		return located{}, &DocumentError{Op: op, Path: path, Line: line, Reason: reason, err: sentinel}
	}
	segs, err := cursorpath.Parse(path)
	if err != nil {
		var se cursorpath.SyntaxError
		if errors.As(err, &se) {
			return fail(ErrInvalidPath, 0, fmt.Sprintf("%s at position %d", se.Problem, se.Position))
		}
		return fail(ErrInvalidPath, 0, "the path can't be read")
	}
	if pos, open := unclosedBracket(path); open {
		return fail(ErrInvalidPath, 0, fmt.Sprintf("unclosed '[' at position %d", pos))
	}
	switch {
	case len(d.docs) == 0:
		return fail(ErrPathNotFound, 0, "the input holds no document")
	case len(d.docs) > 1:
		return fail(ErrAmbiguousPath, 0, "the input holds more than one document")
	case len(d.docs[0].Content) == 0:
		return fail(ErrPathNotFound, 0, "the document holds no value")
	}

	loc := located{parent: d.docs[0], index: 0}
	loc.enter(d.docs[0].Content[0])
	for _, seg := range segs {
		cur := loc.node
		var (
			idx      int
			sentinel error
			reason   string
		)
		switch cur.Kind {
		case yamlnode.MappingNode:
			idx, reason, sentinel = findKey(cur, seg)
		case yamlnode.SequenceNode:
			idx, reason, sentinel = findItem(cur, seg)
		default:
			sentinel, reason = ErrPathNotFound, "the path continues past a scalar"
		}
		if sentinel != nil {
			return fail(sentinel, cur.Line, reason)
		}
		next := located{parent: cur, index: idx, throughAlias: loc.throughAlias || loc.isAlias, anchor: loc.anchor}
		if cur.Kind == yamlnode.MappingNode {
			next.key = cur.Content[idx-1]
		}
		next.enter(cur.Content[idx])
		loc = next
	}
	return loc, nil
}

// unclosedBracket reports the position of a '[' that path never closes.
// cursorpath.Parse reads such a path as if the bracket were closed at the
// end, which an edit must not guess at.
// It runs only after cursorpath.Parse has accepted path.
func unclosedBracket(path string) (int, bool) {
	open := -1
	for i, c := range path {
		switch c {
		case '[':
			open = i
		case ']':
			open = -1
		}
	}
	return open, open >= 0
}

// findKey returns the index in m.Content of the value whose key reads
// as seg, or the reason and sentinel for why there is no one such value.
func findKey(m *yamlnode.Node, seg string) (int, string, error) {
	found := -1
	for i := 0; i+1 < len(m.Content); i += 2 {
		k := yamlnode.FollowAlias(m.Content[i])
		if k.Kind != yamlnode.ScalarNode || k.Value != seg {
			continue
		}
		if found >= 0 {
			return 0, "the key appears more than once in the mapping", ErrAmbiguousPath
		}
		found = i + 1
	}
	if found < 0 {
		return 0, "the key is not in the mapping", ErrPathNotFound
	}
	return found, "", nil
}

// findItem returns the index in s.Content of the item seg names, or the
// reason and sentinel for why it names none, with the rules resolve
// describes.
func findItem(s *yamlnode.Node, seg string) (int, string, error) {
	if field, value, ok := cursorpath.Predicate(seg); ok {
		if i := itemWithField(s, field, value); i >= 0 {
			return i, "", nil
		}
		return 0, "no item has the field with that value", ErrPathNotFound
	}
	if i, err := strconv.ParseUint(seg, 10, 0); err == nil {
		if i >= uint64(len(s.Content)) {
			return 0, "the index is past the end of the sequence", ErrPathNotFound
		}
		return int(i), "", nil // #nosec G115 -- i < len(s.Content), checked above
	}
	for _, field := range cursorpath.NameFields {
		if i := itemWithField(s, field, seg); i >= 0 {
			return i, "", nil
		}
	}
	return 0, "no item has that name", ErrPathNotFound
}

// itemWithField returns the index of the first item of s that is a
// mapping whose field holds a scalar with the text value, or -1.
func itemWithField(s *yamlnode.Node, field, value string) int {
	for i, item := range s.Content {
		m := yamlnode.FollowAlias(item)
		if m.Kind != yamlnode.MappingNode {
			continue
		}
		for j := 0; j+1 < len(m.Content); j += 2 {
			k := yamlnode.FollowAlias(m.Content[j])
			v := yamlnode.FollowAlias(m.Content[j+1])
			if k.Kind == yamlnode.ScalarNode && k.Value == field && v.Kind == yamlnode.ScalarNode && v.Value == value {
				return i
			}
		}
	}
	return -1
}
