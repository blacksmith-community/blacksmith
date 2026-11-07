package yamlnode

import (
	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/token"
)

// IsMergeKey reports whether key, read from the text before any alias
// expands, is a << merge key. That is any key goccy reads as one, and
// also a plain << written after the explicit key indicator, as in
// "? <<", alone or under an anchor. goccy's scanner reads a << as a
// merge key only when a colon follows it on its line, so it reads that
// one as a string, but libyaml reads every plain << key as a merge key,
// and so spruce merges it. A quoted << is a string to both, and so is
// one with a tag, or an alias whose anchor names a << scalar.
func IsMergeKey(key ast.MapKeyNode) bool {
	if key == nil {
		return false
	}
	return key.IsMergeKey() || ExplicitMergeKey(key) != nil
}

// ExplicitMergeKey returns the << scalar of key when key is a merge key
// written after "?" that goccy reads as a string, as IsMergeKey
// describes, and nil for any other key. A key that alias expansion put
// inside an explicit key node starts at the alias, not at a "?", so it
// is never one.
func ExplicitMergeKey(key ast.MapKeyNode) *ast.StringNode {
	mk, ok := key.(*ast.MappingKeyNode)
	if !ok || mk.Start == nil || mk.Start.Type != token.MappingKeyType {
		return nil
	}
	n := mk.Value
	if a, ok := n.(*ast.AnchorNode); ok {
		n = a.Value
	}
	s, ok := n.(*ast.StringNode)
	if !ok || s.Value != "<<" || s.Token == nil || s.Token.Type != token.StringType {
		return nil
	}
	return s
}
