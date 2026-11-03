package yamlnode

import (
	"strconv"

	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/token"
)

// KeyID is what tells one mapping key from another when graft looks for
// a repeated key. Text is the key's text as written, under any tag or
// anchor, which is what goccy's parser compares keys by, and Null says
// the key reads as null.
//
// Two keys with one KeyID repeat each other. A null key and a string key
// whose text is null repeat each other too, though their KeyIDs differ,
// because both read as the key "null" in a map with string keys, and
// such a map holds only one of them. Two null keys spelled differently,
// such as null and ~, don't repeat each other. They read as one key, as
// spruce reads them, and the later value wins. A null key and a string
// key whose text is ~ are two keys, as they are to spruce.
type KeyID struct {
	Text string
	Null bool
}

// Slot is the key that a map with string keys holds for id.
func (id KeyID) Slot() string {
	if id.Null {
		return "null"
	}
	return id.Text
}

// KeySet holds the keys of one mapping as they are added, each with its
// place, of type T.
type KeySet[T any] struct {
	ids   map[KeyID]T
	slots map[string]keySlot[T]
}

// keySlot is what a KeySet knows of the keys that share one slot: the
// place of the first, and whether any is null and any is a string.
type keySlot[T any] struct {
	first      T
	null, text bool
}

// Add records id, a key of the mapping, at its place at. It returns the
// place of an earlier key and true when id repeats that key, as KeyID
// describes. That earlier key is one with the same KeyID, or else the
// key of the other kind with the same text, which is where goccy's
// parser places it, or else the first key of the slot.
func (s *KeySet[T]) Add(id KeyID, at T) (T, bool) {
	if s.ids == nil {
		s.ids = make(map[KeyID]T)
		s.slots = make(map[string]keySlot[T])
	}
	if first, seen := s.ids[id]; seen {
		return first, true
	}
	s.ids[id] = at
	slot, held := s.slots[id.Slot()]
	if !held {
		s.slots[id.Slot()] = keySlot[T]{first: at, null: id.Null, text: !id.Null}
		return at, false
	}
	clash := id.Null && slot.text || !id.Null && slot.null
	slot.null = slot.null || id.Null
	slot.text = slot.text || !id.Null
	s.slots[id.Slot()] = slot
	if !clash {
		return at, false
	}
	other := KeyID{Text: id.Text, Null: !id.Null}
	if first, seen := s.ids[other]; seen && other.Slot() == id.Slot() {
		return first, true
	}
	return slot.first, true
}

// KeyIDOf returns the KeyID of key, a mapping key of a syntax tree goccy
// parsed, and whether key is a scalar. A merge key, an alias, and a
// collection are not scalars, and their KeyIDs are the ones goccy's
// parser compares them by, which for a collection is the text of its
// first token. A key is null when it is a null scalar with no tag, or
// when it has the tag !!null. Any other tag, a core one such as !!str or
// a custom one such as !foo, makes the key a string.
func KeyIDOf(key ast.Node) (KeyID, bool) {
	n, tag := keyScalar(key)
	var id KeyID
	if n == nil {
		return id, false
	}
	if tk := n.GetToken(); tk != nil {
		id.Text = tk.Value
	}
	switch n.(type) {
	case *ast.AliasNode:
		return KeyID{}, false
	case *ast.MergeKeyNode, *ast.MappingNode, *ast.MappingValueNode, *ast.SequenceNode:
		return id, false
	case *ast.NullNode:
		id.Null = tag == nil
	}
	if tag != nil {
		id.Null = isNullTag(tag)
	}
	return id, true
}

// keyScalar returns the node under key's explicit key indicator, tags,
// and anchors, and the outermost tag on the way to it, or nil.
func keyScalar(key ast.Node) (ast.Node, *ast.TagNode) {
	var tag *ast.TagNode
	n := key
	for {
		switch x := n.(type) {
		case *ast.MappingKeyNode:
			n = x.Value
		case *ast.AnchorNode:
			n = x.Value
		case *ast.TagNode:
			if tag == nil {
				tag = x
			}
			n = x.Value
		default:
			return n, tag
		}
	}
}

// isNullTag reports whether t is the tag !!null, in its short or its
// verbatim form.
func isNullTag(t *ast.TagNode) bool {
	if t.Start == nil {
		return false
	}
	name := t.Start.Value
	return name == TagNull || name == "!<tag:yaml.org,2002:null>"
}

// duplicateKeyMessage is the message of the error checkDuplicateKeys
// returns, before the place of the earlier key. Unlike goccy's own, it
// doesn't quote the key, which may be a credential.
const duplicateKeyMessage = "mapping key already defined at "

// checkDuplicateKeys returns an error at the first key in file that
// repeats an earlier key of its mapping, as KeyID describes, and nil
// when no key does. It stands in for goccy's own check, which compares
// keys by their text alone, and so it checks the keys goccy checks. It
// passes a key with no colon after it in a flow mapping, such as each a
// in {a, a}, which goccy neither checks nor counts.
func checkDuplicateKeys(file *ast.File) error {
	c := &dupChecker{}
	for _, doc := range file.Docs {
		if doc != nil && doc.Body != nil {
			c.node(doc.Body)
		}
		if c.err != nil {
			return c.err
		}
	}
	return nil
}

type dupChecker struct {
	err error
}

func (c *dupChecker) node(n ast.Node) {
	if c.err != nil {
		return
	}
	switch x := n.(type) {
	case *ast.AnchorNode:
		c.node(x.Value)
	case *ast.TagNode:
		c.node(x.Value)
	case *ast.MappingKeyNode:
		c.node(x.Value)
	case *ast.MappingNode:
		c.mapping(x.Values, x.IsFlowStyle)
	case *ast.MappingValueNode:
		c.mapping([]*ast.MappingValueNode{x}, false)
	case *ast.SequenceNode:
		for _, item := range x.Values {
			c.node(item)
		}
	}
}

// mapping checks the pairs of one mapping in order, and the keys and
// values under them. flow says the mapping is a flow mapping, where
// goccy gives a key with no colon after it a pair that starts at the
// key itself.
func (c *dupChecker) mapping(pairs []*ast.MappingValueNode, flow bool) {
	var keys KeySet[*token.Token]
	for _, pair := range pairs {
		if pair == nil || c.err != nil {
			continue
		}
		at := pair.Key.GetToken()
		if !flow || pair.Start != at {
			id, _ := KeyIDOf(pair.Key)
			if first, repeat := keys.Add(id, at); repeat && at != nil && at.Position != nil {
				c.err = &yaml.SyntaxError{Message: duplicateKeyMessage + place(first), Token: at}
				return
			}
		}
		c.node(pair.Key)
		c.node(pair.Value)
	}
}

// place writes tk's position as goccy's messages do, such as [3:1].
func place(tk *token.Token) string {
	if tk == nil || tk.Position == nil {
		return "[0:0]"
	}
	return "[" + strconv.Itoa(tk.Position.Line) + ":" + strconv.Itoa(tk.Position.Column) + "]"
}
