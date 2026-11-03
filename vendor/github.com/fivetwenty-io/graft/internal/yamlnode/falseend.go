package yamlnode

import (
	"bytes"

	"github.com/goccy/go-yaml/token"

	"github.com/fivetwenty-io/graft/internal/yamlprep"
)

// falseEndMessage is the error for a plain scalar at the start of a line
// that yaml.v3 reads as a key or as a whole document, and that goccy
// would lose at the document end it reads in the three dots.
const falseEndMessage = `cannot read a plain scalar that starts a line with "..."; quote it`

// isFalseEnd reports whether t is a document end that goccy reads where
// yaml.v3 reads a plain scalar. That is a "..." at column 1 that a
// character other than a blank or a line break follows in src, such as
// "...#c" or "...x". yaml.v3 ends a document only at a "..." that a
// blank, a line break, or the end of the input follows.
func isFalseEnd(src []byte, t *token.Token) bool {
	if t.Type != token.DocumentEndType || t.Position.Column != 1 {
		return false
	}
	off := lineStart(src, t.Position.Line)
	if off < 0 || off+3 >= len(src) || !bytes.HasPrefix(src[off:], []byte("...")) {
		return false
	}
	return bytes.IndexByte([]byte(" \t\r\n"), src[off+3]) < 0
}

// falseEndIndex returns the index of the first token in toks that
// isFalseEnd finds outside every flow collection, or -1. In a flow
// collection, goccy fails the stream on its own. The search stops at an
// invalid token, since the tokens after it say nothing of the input.
func falseEndIndex(src []byte, toks token.Tokens) int {
	flow := 0
	for i, t := range toks {
		switch t.Type {
		case token.InvalidType:
			return -1
		case token.SequenceStartType, token.MappingStartType:
			flow++
		case token.SequenceEndType, token.MappingEndType:
			flow = max(flow-1, 0)
		case token.DocumentEndType:
			if flow == 0 && isFalseEnd(src, t) {
				return i
			}
		default:
			// Other tokens leave the flow level as it stands.
		}
	}
	return -1
}

// falseEndError returns the error for the false end on line of src,
// numbered from 1. goccy ends the document there and drops what follows,
// so FirstDocument fails the line rather than hand goccy the stream.
//
// Where yaml.v3 cannot read the scalar as part of the document either,
// the error is the one yaml.v3 gives, on its line. A key is one yaml.v3
// reads, and so is a scalar that makes up the whole document. The
// end-marker rewrite quotes each one it can prove, so a false end left
// here is one it could not, and fails with falseEndMessage on the "..."
// line, since quoting the scalar is what lets goccy read it.
func falseEndError(src []byte, line int) error {
	off := lineStart(src, line)
	text, rest, broke := cutLine(src[off:])
	if _, colon := yamlprep.PlainScalarEnd(string(text)); colon >= 0 {
		return &ParseError{Line: line, SourceLine: line, Message: falseEndMessage}
	}
	if yamlprep.StartsDocument(string(normalizeLineBreaks(src[:off]))) {
		return rootFalseEnd(text, rest, line)
	}
	return blockFalseEnd(text, rest, broke, line)
}

// blockFalseEnd returns yaml.v3's error for text, a false end on line
// that follows content of its document, and rest, the input after it.
// broke reports whether a line break ends text. yaml.v3 reads the line
// as a plain scalar where a key has to start, so libyaml fails with
// "could not find expected ':'" once the scalar ends with no ":" after
// it. It gives the line of whatever ends the scalar, counted from 0,
// which is a comment, a line at column 1, a more indented line with a
// ":" or a comment, or the end of the input. Any other more indented
// line continues the scalar, unless a comment on the "..." line has
// already ended it. A line that starts with a tab fails first, on the
// tab. The end of the input counts as the line after the last one when
// a line break ends the input or the "..." line is the last. When an
// unbroken line after the "..." line is the last, libyaml finds the
// scalar stale on that line, before it reaches the end of the input.
func blockFalseEnd(text, rest []byte, broke bool, line int) error {
	_, comment := lineMarks(text, 4)
	n := line
	for len(rest) > 0 {
		var l []byte
		l, rest, broke = cutLine(rest)
		n++
		switch {
		case len(l) == 0:
			continue
		case l[0] == '\t':
			return &ParseError{Line: n - 1, SourceLine: n, Message: "found a tab character that violate indentation"}
		case len(bytes.Trim(l, " \t")) == 0:
			continue
		case l[0] != ' ':
			return expectedColonError(n)
		}
		colon, c := lineMarks(bytes.TrimLeft(l, " "), 0)
		if comment || colon || c {
			return expectedColonError(n)
		}
	}
	if broke || n == line {
		return expectedColonError(n + 1)
	}
	return expectedColonError(n)
}

// rootFalseEnd returns the error for text, a false end on line that is
// the first content of its document, and rest, the input after it.
// yaml.v3 reads the line as a plain scalar at the root, which every later
// line continues, whatever its indent, until a marker line, a comment,
// or the end of the input. A ":" in the scalar fails with "mapping
// values are not allowed in this context", and content after a comment
// with "did not find expected <document start>", each on its line
// counted from 0. A scalar that ends cleanly is the whole document,
// which yaml.v3 reads and goccy would lose.
func rootFalseEnd(text, rest []byte, line int) error {
	_, comment := lineMarks(text, 4)
	n := line
	for len(rest) > 0 {
		var l []byte
		l, rest, _ = cutLine(rest)
		n++
		body := bytes.TrimLeft(l, " \t")
		switch {
		case len(body) == 0:
			continue
		case isMarkerLine(l):
			return &ParseError{Line: line, SourceLine: line, Message: falseEndMessage}
		case body[0] == '#':
			comment = true
			continue
		case comment:
			return documentStartError(n)
		}
		colon, c := lineMarks(body, 0)
		if colon {
			return &ParseError{Line: n - 1, SourceLine: n, Message: "mapping values are not allowed in this context"}
		}
		comment = c
	}
	return &ParseError{Line: line, SourceLine: line, Message: falseEndMessage}
}

// expectedColonError is libyaml's error for a plain scalar where a key
// has to start, reported where whatever ends the scalar starts, on line
// numbered from 1.
func expectedColonError(line int) error {
	return &ParseError{Line: line - 1, SourceLine: line, Message: "could not find expected ':'"}
}

// lineMarks scans b from index from for the first ":" that a blank or
// the end of b follows, which makes a mapping value, and the first "#"
// at the start of b or after a blank, which starts a comment. It reports
// which of the two comes first.
func lineMarks(b []byte, from int) (colon, comment bool) {
	for i := from; i < len(b); i++ {
		switch b[i] {
		case ':':
			if i+1 == len(b) || b[i+1] == ' ' || b[i+1] == '\t' {
				return true, false
			}
		case '#':
			if i == 0 || b[i-1] == ' ' || b[i-1] == '\t' {
				return false, true
			}
		default:
			// Other bytes are part of the scalar.
		}
	}
	return false, false
}

// cutLine splits src at its first line break, where a CRLF, a lone CR,
// or an LF is one break, as lineStart counts them. It returns the line
// without its break, the input after the break, and whether a break
// ended the line.
func cutLine(src []byte) (line, rest []byte, broke bool) {
	i := bytes.IndexAny(src, "\r\n")
	if i < 0 {
		return src, nil, false
	}
	j := i + 1
	if src[i] == '\r' && j < len(src) && src[j] == '\n' {
		j++
	}
	return src[:i], src[j:], true
}
