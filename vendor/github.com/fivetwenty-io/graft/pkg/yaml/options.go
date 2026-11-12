package yaml

import "github.com/fivetwenty-io/graft/pkg/sortkeys"

// DecodeOptions selects how Unmarshal reads a document, and its zero value
// is the default.
type DecodeOptions struct {
	// YAML12 reads yes, no, on, and off as strings, as YAML 1.2 does,
	// instead of as booleans.
	YAML12 bool
	// OnDuplicateKey, when it is not nil, lets a document repeat a key
	// in any mapping at any depth. A later occurrence replaces the
	// earlier one, as yaml.v2 does, so a struct field, a map entry, or
	// an any value holds the last value written. A Node target keeps one
	// pair for the key, with the last value, at the place of the first
	// key. Go maps have no order, and yaml.v2 keeps all of a MapSlice's
	// pairs, so the Node order is our own choice.
	//
	// Unmarshal calls it once for each later occurrence, in document
	// order, on the calling goroutine, and before UnmarshalWith returns.
	// It is called even when the decode later fails for another reason.
	// A key given three times produces two calls, and each call's
	// FirstLine and FirstColumn name the first occurrence. A panic in
	// the callback propagates to the caller.
	//
	// Merge keys (<<) are never reported, and a key that arrives
	// through a merge never counts as a duplicate of an explicit key.
	// Two merge keys in one mapping merge both sources in order, so a
	// key in the later source wins, and a Node target holds them as one
	// merge key whose value is a sequence of the sources. Every target
	// reads a mapping's pairs and merge keys in turn, each writing over
	// the ones before it, so a key written after a merge key that also
	// supplies it wins, and the merged value wins over a key written
	// before. A Node target keeps that outcome by moving a pair whose
	// key a merge supplies to the side of the merge key that gives it,
	// and leaves every other pair at the place of its first key.
	//
	// A nil OnDuplicateKey keeps the strict refusal of a repeated key
	// that Unmarshal applies, except that a Node target keeps every pair
	// of a repeated key, as it always has.
	OnDuplicateKey func(DuplicateKey)
}

// DuplicateKey describes a later occurrence of a mapping key, which
// DecodeOptions.OnDuplicateKey receives. It never holds a key or a value
// from the input except through Path, which names the keys on the way to
// the repeated one.
//
// The Column can differ between targets on two kinds of line. One is a
// line that holds a tab after a tag or an anchor, and the other is a
// continuation line of a multi-line flow mapping that holds a tab. On
// either, a target other than a Node counts each such tab as no column,
// so its Column can be less than that of a Node target, which counts the
// tab as the space it stands for.
type DuplicateKey struct {
	// Path locates the repeated key. It joins map keys with a dot and
	// writes a list item as [i], with i counting from 0, and it has no
	// leading dot, so paths read "features", "plans.standalone.features",
	// and "list[2].name". A key that holds a dot or a bracket is written
	// as it is, with no escaping. The format of Path may change.
	Path string
	// Line and Column are the 1-based position of the later key. A key
	// written after "?" is placed at the key, or at its tag or anchor,
	// and not at the "?".
	Line   int
	Column int
	// FirstLine and FirstColumn are the position of the first occurrence.
	FirstLine   int
	FirstColumn int
}

// EncodeOptions selects how Marshal writes a value, and its zero value is
// the default.
type EncodeOptions struct {
	// KeyOrder orders every map's keys. The zero value means
	// sortkeys.Spruce, sortkeys.Lexical is also accepted, and
	// sortkeys.None is refused because Go maps have no order to keep.
	KeyOrder sortkeys.Mode
	// Indent is how many columns a nested mapping sits past its parent
	// key, and a block scalar's content sits past its key. The zero value
	// means 2, 2 through 9 are accepted, and any other value is refused.
	Indent int
	// IndentSequence indents a sequence under a mapping key by Indent
	// columns, and a root sequence by Indent columns too, as goccy's
	// IndentSequence(true) does. Without it a sequence sits at its key's
	// column.
	IndentSequence bool
}
