package yaml

import (
	"errors"
	"reflect"
	"sync"

	"github.com/fivetwenty-io/graft/internal/yamlnode"
)

// Unmarshaler is the interface of a type that decodes itself from a Node,
// as yaml.v3's Unmarshaler does. Node.Decode calls UnmarshalYAML with the
// node that holds the value, wherever a value of the type sits in the
// target, so the method can read the node's Line, Column, and Tag, and
// call node.Decode itself, usually into a type of its own that has no
// such method, so it does not call itself again.
//
// A node reached through an alias is the node that holds the anchor, and
// a mapping with a << merge key is the mapping as written, which
// node.Decode merges as Unmarshal does. A null never reaches the method,
// whether it has no tag or the tag !!null, as in yaml.v3. It decodes as
// it would for a type without the method, so a struct field and the
// value at the root keep what they held, a pointer below the root
// becomes nil, and a map value or a list item becomes the zero value. A
// type with both this method and UnmarshalText decodes through this one.
//
// When the method fails, Decode returns a *DecodeError. A *DecodeError
// the method returns comes back as it is. An error that wraps one, such
// as fmt.Errorf("endpoint: %w", err) around an error node.Decode gave,
// comes back as a copy of the inner DecodeError, and any other error
// comes back as an ErrorKindType DecodeError at the node's place. Either
// way, Unwrap returns the method's error, and Error never prints it.
//
// Decode refuses a target that uses such a type where no method can
// run, which is as a map key or as a named ,inline field.
type Unmarshaler interface {
	UnmarshalYAML(node *Node) error
}

// unmarshalerType is the Unmarshaler interface type.
var unmarshalerType = reflect.TypeFor[Unmarshaler]()

// isUnmarshaler reports whether a value of type t, through its address,
// is an Unmarshaler, which covers a method on t and one on *t.
func isUnmarshaler(t reflect.Type) bool {
	return reflect.PointerTo(t).Implements(unmarshalerType)
}

// reachedUnmarshalers caches reachesUnmarshaler's answer for each type.
var reachedUnmarshalers sync.Map

// reachesUnmarshaler reports whether t, or anything it holds at a place a
// decode fills, is an Unmarshaler. Those places are the elements of
// pointers, slices, and arrays, the values of maps, and the struct fields
// goccy decodes into. The walk never looks inside an interface type or a
// type that goccy decodes by its own rules, such as a type with one of
// goccy's UnmarshalYAML methods, time.Time, or a TextUnmarshaler.
func reachesUnmarshaler(t reflect.Type) bool {
	if got, ok := reachedUnmarshalers.Load(t); ok {
		reached, _ := got.(bool)
		return reached
	}
	w := typeWalk{
		leaf:   isUnmarshaler,
		opaque: func(t reflect.Type) bool { return ownKindOf(t) != notOwn },
		field:  decodesField,
		seen:   make(map[reflect.Type]bool),
	}
	got := w.reaches(t)
	reachedUnmarshalers.Store(t, got)
	return got
}

// decodeHooked is Node.Decode for a target v whose type reaches an
// Unmarshaler. A target that is one itself has its method called with n.
// Any other target decodes through a tree that records which Node each
// of its tokens came from, so each method finds the node that holds its
// value.
func (n *Node) decodeHooked(v any) error {
	if u, ok := v.(Unmarshaler); ok {
		root, de := documentRoot(n)
		if de != nil || root == nil {
			return nilIfNoError(de)
		}
		resolved, de := target(root)
		if de != nil {
			return de
		}
		return callUnmarshaler(u, resolved, reflect.TypeOf(v).Elem())
	}
	owners := &tokenOwners{}
	return decodeFrom(n.trackedSource(owners), v, n.opts, owners)
}

// nilIfNoError returns de as an error, and nil when de is nil, so a nil
// *DecodeError never becomes a non-nil error.
func nilIfNoError(de *DecodeError) error {
	if de == nil {
		return nil
	}
	return de
}

// callUnmarshaler calls u's method with n, unless n is a null, which
// leaves u as it was. t is the type of the value u points at.
func callUnmarshaler(u Unmarshaler, n *Node, t reflect.Type) error {
	if nullNode(n) {
		return nil
	}
	if err := u.UnmarshalYAML(n); err != nil {
		return hookError(err, n, t)
	}
	return nil
}

// nullNode reports whether n is a null that no method sees, as yaml.v3
// skips every node whose tag is !!null. That is the zero Node, a plain
// scalar such as ~, null, or nothing at all, and any scalar tagged !!null,
// whether the text wrote the tag or the caller set it. A scalar the text
// marked with the non-specific tag "!" is a string, not a null.
func nullNode(n *Node) bool {
	if isZero(n) {
		return true
	}
	if n.Kind != ScalarNode {
		return false
	}
	if n.read && n.Tag == n.readTag {
		return n.Tag == yamlnode.TagNull && !n.bareTag
	}
	if n.Tag == yamlnode.TagNull {
		return true
	}
	if n.style&(yamlnode.StyleSingle|yamlnode.StyleDouble|yamlnode.StyleLiteral|yamlnode.StyleFolded) != 0 {
		return false
	}
	return n.Tag == "" && yamlnode.ResolvePlainTag(n.Value) == yamlnode.TagNull
}

// hookError returns err, the error an UnmarshalYAML method returned for
// the node n into a value of type t, as a *DecodeError. A *DecodeError
// comes back unchanged. An error that wraps one gives a copy of it with
// err as its cause, so the place and the kind are those of the failure
// inside. Any other error gives an ErrorKindType error at n's place with
// err as its cause.
func hookError(err error, n *Node, t reflect.Type) error {
	if de, ok := err.(*DecodeError); ok { //nolint:errorlint // A DecodeError the method made itself comes back as the very same error.
		return de
	}
	var inner *DecodeError
	if errors.As(err, &inner) {
		out := *inner
		out.cause = err
		return &out
	}
	pos := position(n)
	return &DecodeError{Line: pos.Line, Column: pos.Column, Kind: ErrorKindType, Msg: ownMessage(t), cause: err}
}
