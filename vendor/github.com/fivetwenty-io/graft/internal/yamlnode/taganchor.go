package yamlnode

import "github.com/goccy/go-yaml/token"

// goccy's parser reads the node after a scalar core tag, such as !!str,
// as a scalar and then steps past one more token. When an anchor sits
// between the tag and a node that the anchor does not take on its own
// line, such as a collection or a scalar on a later line, the anchor
// reads that node and steps past it, and the tag's extra step then skips
// the token after it. So in "a: !!str &n {k: v}\nb: 1" the key b is lost
// with no error, and in "a: !!str &n\n  x\nb: 1" the same happens to a
// scalar that spruce reads. The anchor and tag in the other order parse
// right, and refuse a collection under the scalar tag as they do with no
// anchor.

// scalarCoreTags holds the tags goccy's parser reads a scalar under.
var scalarCoreTags = map[string]bool{
	string(token.IntegerTag):   true,
	string(token.FloatTag):     true,
	string(token.StringTag):    true,
	string(token.BinaryTag):    true,
	string(token.TimestampTag): true,
	string(token.BooleanTag):   true,
	string(token.NullTag):      true,
}

// anchorScalarTypes holds the token types that an anchor takes as its
// value when one follows its name on the same line, as goccy's parser
// groups them.
var anchorScalarTypes = map[token.Type]bool{
	token.AnchorType:        true,
	token.AliasType:         true,
	token.LiteralType:       true,
	token.FoldedType:        true,
	token.NullType:          true,
	token.ImplicitNullType:  true,
	token.BoolType:          true,
	token.IntegerType:       true,
	token.BinaryIntegerType: true,
	token.OctetIntegerType:  true,
	token.HexIntegerType:    true,
	token.FloatType:         true,
	token.InfinityType:      true,
	token.NanType:           true,
	token.StringType:        true,
	token.SingleQuoteType:   true,
	token.DoubleQuoteType:   true,
}

// anchorBeforeScalarTag puts each anchor that follows a scalar core tag,
// and that does not take a scalar on its own line, before that tag in
// toks, so the parser reads the two in the order it reads right. Only
// the order in toks changes. Every token keeps its position and its
// links to the tokens around it in the text, and a comment between the
// tag and the anchor stays after the tag.
func anchorBeforeScalarTag(toks token.Tokens) {
	for i, tk := range toks {
		if tk.Type != token.TagType || !scalarCoreTags[tk.Value] {
			continue
		}
		a := nextNonCommentAt(toks, i+1)
		if a < 0 || toks[a].Type != token.AnchorType || a+1 >= len(toks) {
			continue
		}
		v := nextNonCommentAt(toks, a+2)
		if v < 0 || anchorTakes(toks[a], toks[v]) {
			continue
		}
		anchor, name := toks[a], toks[a+1]
		copy(toks[i+2:a+2], toks[i:a])
		toks[i], toks[i+1] = anchor, name
	}
}

// anchorTakes reports whether the parser groups the anchor tk with v,
// the first token after its name, as its value.
func anchorTakes(tk, v *token.Token) bool {
	return tk.Position != nil && v.Position != nil &&
		tk.Position.Line == v.Position.Line && anchorScalarTypes[v.Type]
}

// nextNonCommentAt returns the index of the first token in toks at or
// after from that is not a comment, or -1 when there is none.
func nextNonCommentAt(toks token.Tokens, from int) int {
	for i := from; i < len(toks); i++ {
		if toks[i].Type != token.CommentType {
			return i
		}
	}
	return -1
}
