package yamlprep

import (
	"bytes"
	"sort"
	"strings"

	"github.com/goccy/go-yaml/token"
)

// QuoteBracePlaceholders rewrites every unquoted {{...}} template
// placeholder that sits in a value position into a single-quoted YAML
// string, so goccy reads it as the plain string it was meant to be
// instead of rejecting it as a flow mapping used as a key.
//
// The rewrite is token-driven. A placeholder is a MappingStart token
// immediately followed by a second MappingStart, whose matching
// MappingEnd tokens are also adjacent, on one source line. It applies
// only when the previous significant token is ":", "-", "[", or a ","
// inside a flow sequence, and never when the placeholder is followed
// by ":" (a mapping key). Text glued to the closing "}}" joins the span,
// whether goccy reads it as a plain string or, when it starts with "#",
// as a comment. Any "'" inside the span is doubled.
//
// goccy counts a CRLF comment line as two lines, so the tokens come from
// a copy with LF line breaks, and each span is mapped back onto data.
// Only the quotes change, so CRLF input keeps its line breaks.
//
// Input without "{{" comes back as the original slice. A candidate
// whose computed byte span does not hold the expected braces is left
// alone, so the worst case is the parse error the input had before.
func QuoteBracePlaceholders(data []byte) []byte {
	if !bytes.Contains(data, []byte("{{")) {
		return data
	}
	src := string(data)
	lf, removed := withoutCR(src)
	spans := placeholderSpans(lf)
	if len(spans) == 0 {
		return data
	}

	out := make([]byte, 0, len(data)+4*len(spans))
	prev := 0
	for _, sp := range spans {
		start := sp.start + sort.SearchInts(removed, sp.start)
		end := sp.end + sort.SearchInts(removed, sp.end)
		out = append(out, data[prev:start]...)
		out = append(out, '\'')
		if seg := data[start:end]; bytes.IndexByte(seg, '\'') < 0 {
			out = append(out, seg...)
		} else {
			out = append(out, bytes.ReplaceAll(seg, []byte("'"), []byte("''"))...)
		}
		out = append(out, '\'')
		prev = end
	}
	return append(out, data[prev:]...)
}

type span struct{ start, end int }

// placeholderSpans returns the byte spans of src to quote, in order.
func placeholderSpans(src string) []span {
	toks := Tokenize(src)
	idx := newByteIndex(src)
	var spans []span
	for i := 0; i < len(toks); i++ {
		if toks[i].Type != token.MappingStartType || !inValuePosition(toks, i) {
			continue
		}
		s, e, next, ok := placeholderSpan(toks, i, idx, src)
		if !ok {
			continue
		}
		spans = append(spans, span{s, e})
		i = next
	}
	return spans
}

// withoutCR returns src with every CRLF line break turned into LF, and
// the offsets in the result of each LF that lost its CR, in ascending
// order. The number of those offsets below a position in the result is
// how far that position moved, which sort.SearchInts counts.
func withoutCR(src string) (string, []int) {
	if !strings.Contains(src, "\r\n") {
		return src, nil
	}
	var b strings.Builder
	b.Grow(len(src))
	var removed []int
	for i := 0; i < len(src); i++ {
		if src[i] == '\r' && i+1 < len(src) && src[i+1] == '\n' {
			removed = append(removed, b.Len())
			continue
		}
		b.WriteByte(src[i])
	}
	return b.String(), removed
}

// placeholderSpan checks the token run that starts at the MappingStart
// toks[i]. It returns the byte span to quote, the index of the last token
// the span consumed, and whether toks[i] opens a placeholder.
func placeholderSpan(toks token.Tokens, i int, idx byteIndex, src string) (start, end, last int, ok bool) {
	outerClose, ok := adjacentBraces(toks, i, idx, src)
	if !ok {
		return 0, 0, 0, false
	}
	start, _ = idx.offset(toks[i].Position.Line, toks[i].Position.Column)
	closeAt, _ := idx.offset(toks[outerClose].Position.Line, toks[outerClose].Position.Column)
	end, last, ok = joinSuffix(toks, outerClose, closeAt+1, idx, src)
	if !ok || followedByMappingValue(toks, last, toks[outerClose].Position.Line) {
		return 0, 0, 0, false
	}
	return start, end, last, true
}

// adjacentBraces reports whether toks[i] and toks[i+1] open "{{" and
// their matching closes form "}}" on the same line, with the source bytes
// confirming every brace. It returns the index of the outer close.
func adjacentBraces(toks token.Tokens, i int, idx byteIndex, src string) (int, bool) {
	if i+1 >= len(toks) || toks[i+1].Type != token.MappingStartType {
		return 0, false
	}
	outerClose, innerClose := matchingClose(toks, i), matchingClose(toks, i+1)
	if outerClose < 0 || innerClose < 0 || outerClose != innerClose+1 ||
		toks[i].Position.Line != toks[outerClose].Position.Line {
		return 0, false
	}
	at := make([]int, 0, 4)
	for _, k := range []int{i, i + 1, innerClose, outerClose} {
		off, ok := idx.offset(toks[k].Position.Line, toks[k].Position.Column)
		if !ok {
			return 0, false
		}
		at = append(at, off)
	}
	if at[1] != at[0]+1 || at[3] != at[2]+1 ||
		src[at[0]] != '{' || src[at[1]] != '{' || src[at[2]] != '}' || src[at[3]] != '}' {
		return 0, false
	}
	return outerClose, true
}

// joinSuffix extends a span that ends at byte end (just past "}}") over
// the text glued to it. goccy reads that text as a plain string, unless
// it starts with "#", which goccy takes for a comment although YAML does
// not, because a comment needs a space before it. joinSuffix reports
// false when the glued text runs onto another line or does not match the
// source, and when "#" text holds a ": " that would make it a key.
func joinSuffix(toks token.Tokens, closeIdx, end int, idx byteIndex, src string) (newEnd, last int, ok bool) {
	n := closeIdx + 1
	if n >= len(toks) || toks[n].Position.Line != toks[closeIdx].Position.Line {
		return end, closeIdx, true
	}
	if t := toks[n].Type; t != token.StringType && t != token.CommentType {
		return end, closeIdx, true
	}
	at, found := idx.offset(toks[n].Position.Line, toks[n].Position.Column)
	if !found || at != end {
		return end, closeIdx, true
	}
	if toks[n].Type == token.CommentType {
		text, ok := hashSuffix(src[at:], inFlow(toks, closeIdx))
		return at + len(text), n, ok
	}
	text := strings.TrimRight(toks[n].Origin, " \t\r\n")
	if strings.ContainsAny(text, "\r\n") || !strings.HasPrefix(src[at:], text) {
		return 0, 0, false
	}
	return at + len(text), n, true
}

// hashSuffix returns the plain-scalar text at the start of rest, which
// starts with "#". The text ends at the end of the line, at whitespace
// before a real comment, and inside a flow collection at ",", "]", or
// "}". It reports false when the text holds ": " or ends in ":", where
// YAML would read a mapping key, so the candidate keeps its parse error.
func hashSuffix(rest string, flow bool) (string, bool) {
	end := len(rest)
	for i := 0; i < len(rest); i++ {
		c := rest[i]
		if c == '\n' || (flow && strings.IndexByte(",]}", c) >= 0) {
			end = i
			break
		}
		if (c == ' ' || c == '\t') && strings.HasPrefix(strings.TrimLeft(rest[i:], " \t"), "#") {
			end = i
			break
		}
	}
	text := strings.TrimRight(rest[:end], " \t")
	if strings.Contains(text, ": ") || strings.Contains(text, ":\t") || strings.HasSuffix(text, ":") {
		return "", false
	}
	return text, true
}

// inFlow reports whether toks[k] sits inside a flow collection that is
// still open at that point.
func inFlow(toks token.Tokens, k int) bool {
	depth := 0
	for ; k >= 0; k-- {
		switch toks[k].Type {
		case token.SequenceEndType, token.MappingEndType:
			depth++
		case token.SequenceStartType, token.MappingStartType:
			if depth == 0 {
				return true
			}
			depth--
		default:
			// Other tokens neither open nor close a flow collection.
		}
	}
	return false
}

// followedByMappingValue reports whether the next significant token after
// toks[last] is a ":" on the given line, which makes the placeholder a
// mapping key.
func followedByMappingValue(toks token.Tokens, last, line int) bool {
	n := nextSignificant(toks, last+1)
	return n >= 0 && toks[n].Type == token.MappingValueType && toks[n].Position.Line == line
}

// inValuePosition reports whether the MappingStart toks[i] sits where a
// value belongs: after ":", "-", "[", or a "," whose innermost open
// flow collection is a sequence.
func inValuePosition(toks token.Tokens, i int) bool {
	p := prevSignificant(toks, i-1)
	if p < 0 {
		return false
	}
	switch toks[p].Type {
	case token.MappingValueType, token.SequenceEntryType, token.SequenceStartType:
		return true
	case token.CollectEntryType:
		return innermostFlowIsSequence(toks, p)
	default:
		return false
	}
}

// innermostFlowIsSequence walks back from toks[p] and reports whether
// the nearest unclosed flow collection is a sequence.
func innermostFlowIsSequence(toks token.Tokens, p int) bool {
	depth := 0
	for k := p - 1; k >= 0; k-- {
		switch toks[k].Type {
		case token.SequenceEndType, token.MappingEndType:
			depth++
		case token.SequenceStartType:
			if depth == 0 {
				return true
			}
			depth--
		case token.MappingStartType:
			if depth == 0 {
				return false
			}
			depth--
		default:
			// Other tokens neither open nor close a flow collection.
		}
	}
	return false
}

// matchingClose returns the index of the token that closes the flow
// collection opened at toks[open], or -1 when the braces never balance.
func matchingClose(toks token.Tokens, open int) int {
	depth := 0
	for k := open; k < len(toks); k++ {
		switch toks[k].Type {
		case token.MappingStartType, token.SequenceStartType:
			depth++
		case token.MappingEndType, token.SequenceEndType:
			depth--
			if depth == 0 {
				if toks[k].Type != token.MappingEndType {
					return -1
				}
				return k
			}
		default:
			// Other tokens neither open nor close a flow collection.
		}
	}
	return -1
}

func prevSignificant(toks token.Tokens, k int) int {
	for ; k >= 0; k-- {
		if toks[k].Type != token.CommentType {
			return k
		}
	}
	return -1
}

func nextSignificant(toks token.Tokens, k int) int {
	for ; k < len(toks); k++ {
		if toks[k].Type != token.CommentType {
			return k
		}
	}
	return -1
}

// byteIndex turns goccy's 1-based line and column into a byte offset.
// goccy counts columns in runes and does not count tab characters, and
// its Position.Offset drifts after block scalars, so we rebuild the
// offset from the line text instead of trusting Offset.
type byteIndex struct {
	src   string
	lines []int
}

func newByteIndex(src string) byteIndex {
	lines := []int{0}
	for i := 0; i < len(src); i++ {
		if src[i] == '\n' {
			lines = append(lines, i+1)
		}
	}
	return byteIndex{src: src, lines: lines}
}

func (b byteIndex) offset(line, column int) (int, bool) {
	if line < 1 || line > len(b.lines) || column < 1 {
		return 0, false
	}
	start := b.lines[line-1]
	end := len(b.src)
	if line < len(b.lines) {
		end = b.lines[line] - 1
	}
	col := 1
	for i, r := range b.src[start:end] {
		if r == '\t' {
			continue
		}
		if col == column {
			return start + i, true
		}
		col++
	}
	return 0, false
}
