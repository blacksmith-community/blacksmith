package yamlnode

import (
	"strings"

	"github.com/goccy/go-yaml/token"

	"github.com/fivetwenty-io/graft/internal/yamlprep"
)

// goccy's scanner reads a block scalar whose last line ends the input,
// with no line break after it, differently from one whose last line
// ends in a line break. When that last line is a single character, a >
// block leaves the line break before it unfolded, so "a: >\n  x\n  y"
// reads as "x\ny" where libyaml reads "x y". It also drops the spaces
// and tabs that end that last line, of a literal block as well as a
// folded one, where libyaml keeps them.

// foldBlockAtEnd gives the block scalar whose last line ends text, when
// there is one, the value libyaml reads for it. It tokenizes text with
// a line break added, which goccy reads right, and gives the block's
// content token in toks that value less the added line break. Only the
// value changes, so every token keeps its position in text. toks must
// be the tokens of text.
func foldBlockAtEnd(text string, toks token.Tokens) {
	if !endsInBlockScalar(text, toks) {
		return
	}
	i := len(toks) - 1
	padded := yamlprep.Tokenize(text + "\n")
	if len(padded) != len(toks) {
		return
	}
	tk, pt := toks[i], padded[i]
	if pt.Type != tk.Type || padded[i-1].Type != toks[i-1].Type {
		return
	}
	tk.Value = strings.TrimSuffix(pt.Value, "\n")
}

// endsInBlockScalar reports whether the last token of toks, the tokens
// of text, is the content of a block scalar, and the last line of text,
// which ends it, has no line break after it and holds a character other
// than a space. A tab there is content past the indentation, which
// goccy drops at the end of the input. A block scalar whose last line
// holds only spaces reads the same either way, and a line break added
// after it would change the value of a keep block.
func endsInBlockScalar(text string, toks token.Tokens) bool {
	if len(toks) == 0 || toks[len(toks)-1].Type != token.StringType || !lastLineHasText(text) {
		return false
	}
	_, ok := lastBlockScalarHeader(toks)
	return ok
}

// lastBlockScalarHeader reports whether the last token of toks is the
// content of a literal or folded block scalar, so the text ends inside
// it, and returns that scalar's header token, such as "|2-". Comments
// between the two are skipped. goccy's lexer marks the content Invalid
// when it is about to reject it, so both types count.
func lastBlockScalarHeader(toks token.Tokens) (*token.Token, bool) {
	n := len(toks)
	if n < 2 || (toks[n-1].Type != token.StringType && toks[n-1].Type != token.InvalidType) {
		return nil, false
	}
	for k := n - 2; k >= 0; k-- {
		switch toks[k].Type {
		case token.CommentType:
			continue
		case token.LiteralType, token.FoldedType:
			return toks[k], true
		default:
			return nil, false
		}
	}
	return nil, false
}

// lastLineHasText reports whether text ends in a line with no line break
// after it that holds a character other than a space.
func lastLineHasText(text string) bool {
	if text == "" || strings.ContainsAny(text[len(text)-1:], "\n\r") {
		return false
	}
	return strings.Trim(text[strings.LastIndexByte(text, '\n')+1:], " ") != ""
}
