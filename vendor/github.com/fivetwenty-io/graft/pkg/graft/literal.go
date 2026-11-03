package graft

import "github.com/fivetwenty-io/graft/internal/literal"

// LiteralValue is a value that graft keeps whole. NewLiteral builds one,
// and Value returns what it wraps.
//
// A merge treats a LiteralValue as a single scalar. It replaces whatever
// value it lands on, whole, and a later document replaces it whole in
// turn, so nothing inside it is deep-merged, merged by name, appended
// to, or touched by an (( append )) or (( replace )) marker. The
// evaluator never looks inside it, so a "(( ... ))" string in it stays
// text, even when it is malformed. A path that reads into it, such as
// (( grab params.size )), (( keys params )), or a || fallback, sees
// through the wrapper, and a value that (( grab )), (( inject )), or a
// bare (( a || b )) copies out of it stays a LiteralValue at its new
// place. When any other operator builds a result from data inside one,
// every string in that result that holds "((" is wrapped again, so a
// later phase or a later Evaluate never runs it.
//
// When an operator that read from a literal fails, and the Error() text
// of its error holds a key or a scalar of 3 or more characters from
// inside any literal in the document, the evaluator replaces that text
// with a message that names only the path. A shorter text, such as "80",
// is not withheld, because it turns up in too many ordinary messages,
// and an operator that read no literal keeps its message. DetailedError
// still returns the original text, literal values and all, so a caller
// should show it only where the literal's content may be seen.
//
// Every output is plain data. RawData, GetData, ToMap, Get and the other
// getters, ToYAML, ToJSON, ToJSONIndent, MarshalYAML, pkg/yaml Marshal,
// and encoding/json all remove the wrapper at any depth, and they leave
// the document itself unchanged.
//
// The zero LiteralValue wraps nil. Two LiteralValues compare equal with
// == only when one is a copy of the other.
type LiteralValue = literal.Value

// NewLiteral wraps a deep copy of v so that graft keeps it whole. A v
// that is already a LiteralValue is returned unchanged, and v may be nil.
// NewLiteral copies every map[string]interface{},
// map[interface{}]interface{}, and []interface{} in v, so a later change
// to one of those does not reach the document, and nothing graft does to
// the document reaches v.
//
// On the way NewLiteral turns each map[interface{}]interface{} in v into
// a map[string]interface{}, with every key printed by fmt, so a path into
// the result sees the shapes that ParseYAML and encoding/json produce. A
// typed container, such as a []string or a map[string]string, is kept as
// it is and is not copied. A path cannot read into one, just as it cannot
// when the value is not wrapped, and a change to it after the call does
// reach the document. Put the result anywhere in a document tree, as a
// map value or a list item.
//
// NewLiteral panics when a map or list in v contains itself at any
// depth, because graft cannot hold cyclic data. It finds the cycle the
// first time round, and the panic message quotes nothing from v. A map
// or list that appears in more than one place in v is not a cycle, and
// each place gets its own copy.
func NewLiteral(v any) LiteralValue {
	return literal.New(v)
}

// plainValue returns v with every LiteralValue at any depth replaced by
// its content. It copies only the containers on the way to a wrapper.
func plainValue(v interface{}) interface{} {
	return literal.Strip(v)
}

// plainMap is plainValue for a document root.
func plainMap(m map[string]interface{}) map[string]interface{} {
	out, _ := literal.Strip(m).(map[string]interface{})
	return out
}

// treeOf returns doc's own tree with every LiteralValue still in place.
// The engine reads documents through it instead of RawData, which strips
// the wrappers that merge and evaluation depend on.
func treeOf(doc Document) interface{} {
	if d, ok := doc.(*document); ok {
		return d.data
	}
	return doc.RawData()
}
