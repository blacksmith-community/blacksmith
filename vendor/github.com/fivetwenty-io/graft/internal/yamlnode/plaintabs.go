package yamlnode

import (
	"strings"

	"github.com/goccy/go-yaml/token"
)

// goccy's scanner reads a tab inside a plain scalar as indentation, so it
// drops the tab from the value and keeps it only in the token's Origin.
// For "a: x<TAB>y" it gives "xy" where libyaml gives "x<TAB>y", and for
// "a: 1<TAB>2" it gives the integer 12 where libyaml gives a string.

// keepPlainTabs gives every plain scalar token in toks that holds a tab
// between two other characters the value libyaml reads for it, which is
// a string. A tab at either end of a line, and one before a comment, is
// a separator that libyaml drops too, so a token holding only those
// keeps goccy's reading. Run it only when the text holds a tab.
func keepPlainTabs(toks token.Tokens) {
	for i, tk := range toks {
		if !isPlainScalar(tk) || strings.IndexByte(tk.Origin, '\t') < 0 || !isPlainValue(toks, i) {
			continue
		}
		v, ok := plainValue(tk.Origin)
		// A value with no tab is one goccy already reads right. One that
		// differs from goccy's in more than its tabs comes from an Origin
		// this fold does not read as goccy does, so goccy's value stands.
		if !ok || strings.IndexByte(v, '\t') < 0 || strings.ReplaceAll(v, "\t", "") != tk.Value {
			continue
		}
		s := token.String(v, tk.Origin, tk.Position)
		tk.Type, tk.CharacterType, tk.Indicator, tk.Value = s.Type, s.CharacterType, s.Indicator, s.Value
	}
}

// isPlainScalar reports whether tk is a token goccy makes for a plain
// scalar, which takes its type from its value.
func isPlainScalar(tk *token.Token) bool {
	switch tk.Type {
	case token.StringType, token.NullType, token.BoolType, token.InfinityType, token.NanType,
		token.IntegerType, token.BinaryIntegerType, token.OctetIntegerType, token.HexIntegerType,
		token.FloatType, token.MergeKeyType:
		return true
	default:
		return false
	}
}

// isPlainValue reports whether toks[i] is a scalar of the document rather
// than the body of a block scalar, the name of an anchor or alias, or the
// rest of a directive, which goccy also gives string tokens.
func isPlainValue(toks token.Tokens, i int) bool {
	for k := i - 1; k >= 0; k-- {
		switch toks[k].Type {
		case token.CommentType:
			continue
		case token.LiteralType, token.FoldedType, token.AnchorType, token.AliasType, token.DirectiveType:
			return false
		default:
			return true
		}
	}
	return true
}

// plainValue returns the value libyaml reads for the plain scalar whose
// source text, with the blanks and line breaks around it, is origin. Each
// line loses the spaces and tabs at its ends, a line break between two
// lines of text folds to a space, and each empty line between them reads
// as a line break. A tab between two characters of a line stays. It
// reports false when origin holds no text.
func plainValue(origin string) (string, bool) {
	var b strings.Builder
	empty, started := 0, false
	for line := range strings.SplitSeq(origin, "\n") {
		line = strings.Trim(line, " \t\r")
		switch {
		case line == "":
			empty++
			continue
		case !started:
			started = true
		case empty == 0:
			b.WriteByte(' ')
		default:
			b.WriteString(strings.Repeat("\n", empty))
		}
		empty = 0
		b.WriteString(line)
	}
	return b.String(), started
}
