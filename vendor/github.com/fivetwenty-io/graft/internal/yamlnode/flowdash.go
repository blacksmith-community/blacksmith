package yamlnode

import (
	"strings"

	"github.com/goccy/go-yaml/token"
)

// flowBlockEntryMessage is the message of the error refuseFlowBlockEntries
// gives. Like libyaml's, it quotes nothing of the input.
const flowBlockEntryMessage = "a block sequence entry cannot appear inside a flow collection"

// refuseFlowBlockEntries turns every block sequence entry in toks that
// sits inside a flow collection into an invalid token, so the parse
// fails at the dash. libyaml reads a "-" followed by a blank as a block
// entry wherever it sits, and its parser refuses one inside a flow
// collection, so spruce fails such a document. goccy's parser reads the
// entry as the start of a nested block list instead.
//
// A "-" with no blank after it, as in [-] or [x, -w], is text to both
// scanners, and goccy gives it no entry token. A plain scalar that
// continues onto a line starting with such a dash, as in "[x" over
// "- w]", fails here too, at the dash. libyaml folds the dash into the
// scalar and reads the text "x - w" there, but goccy reads the rest of
// the line, the closing bracket with it, into the scalar, so graft
// refuses the document rather than read a different value.
func refuseFlowBlockEntries(toks token.Tokens) {
	depth := 0
	for i, tk := range toks {
		switch tk.Type {
		case token.SequenceStartType, token.MappingStartType:
			depth++
		case token.SequenceEndType, token.MappingEndType:
			if depth > 0 {
				depth--
			}
		case token.DocumentHeaderType, token.DocumentEndType:
			depth = 0
		case token.SequenceEntryType:
			if depth > 0 {
				tk.Type, tk.CharacterType, tk.Error = token.InvalidType, token.CharacterTypeInvalid, flowBlockEntryMessage
			}
		default:
			if depth > 0 && isPlainScalar(tk) && isPlainValue(toks, i) {
				refuseEntryLine(toks, i)
			}
		}
	}
}

// refuseEntryLine turns toks[i], a plain scalar inside a flow
// collection, into an invalid token at the first line it continues onto
// that starts with a block entry, if there is one.
func refuseEntryLine(toks token.Tokens, i int) {
	tk := toks[i]
	if tk.Position == nil {
		return
	}
	_, rest, ok := strings.Cut(tk.Origin, "\n")
	for n := 1; ok; n++ {
		var line string
		line, rest, ok = strings.Cut(rest, "\n")
		at := len(line) - len(strings.TrimLeft(line, " \t"))
		if !strings.HasPrefix(line[at:], "-") || len(line) > at+1 && !isBlank(rune(line[at+1])) && line[at+1] != '\r' {
			continue
		}
		pos := *tk.Position
		pos.Line, pos.Column = originStart(toks, i)+n, column(line, at)
		tk.Type, tk.CharacterType, tk.Error, tk.Position = token.InvalidType, token.CharacterTypeInvalid, flowBlockEntryMessage, &pos
		return
	}
}
