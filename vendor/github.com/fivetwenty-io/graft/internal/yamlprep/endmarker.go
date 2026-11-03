package yamlprep

import (
	"bytes"
	"sort"
	"strings"

	"github.com/goccy/go-yaml/token"
)

// QuoteEndMarkerScalars single-quotes each plain scalar at column 1 that
// starts with "..." and a character other than a blank, a line break, or
// a ".", so goccy reads it as the scalar it is.
//
// YAML ends a document at a "..." only when a blank, a line break, or
// the end of the input follows it, and spruce's yaml.v3 reads "...x: 2"
// as the key "...x". goccy v1.19.2 ends the document at the three dots
// whatever follows them, and then drops the key, renames it, or fails on
// it. A "#" right after the dots starts no comment, since no blank comes
// before it, so "...#c: 2" is the key "...#c" to spruce too.
//
// The rewrite is token-driven. It quotes a line only where goccy's lexer
// puts a document end at column 1, so a line inside a quoted string or a
// block scalar never changes. Inside a flow collection, flowEndMarkerSpan
// decides the span. Outside every flow collection, the scalar runs to
// the ": " that makes it a key, and the rewrite quotes the key alone.
// goccy rejects a tab between a quoted key and its ":", so the blanks
// there become spaces. A scalar that is no key runs to a " #"
// or the end of the line. The rewrite quotes it only when it is the
// first content of its document and no later line before the next
// marker line could continue it. Anywhere else it is part of a longer
// scalar or of a collection, where quotes would land inside a value, so
// the line stays as goccy reads it. Any "'" inside the scalar is
// doubled. Only quotes are added, so every line keeps its number, and
// goccy reads the tokens after the scalar a column or more to the right.
//
// goccy counts a CRLF comment line as two lines, so the tokens come from
// a copy with LF line breaks, and each span is mapped back onto data.
// Input with no such line comes back as the original slice.
func QuoteEndMarkerScalars(data []byte) []byte {
	if !hasEndMarkerScalar(data) {
		return data
	}
	src := string(data)
	lf, removed := withoutCR(src)
	spans := endMarkerSpans(lf)
	if len(spans) == 0 {
		return data
	}

	out := make([]byte, 0, len(data)+4*len(spans))
	prev := 0
	for _, sp := range spans {
		// A span lies within one line, so one shift maps all of it.
		shift := sort.SearchInts(removed, sp.start)
		start, quoteEnd, end := sp.start+shift, sp.quoteEnd+shift, sp.end+shift
		out = append(out, data[prev:start]...)
		out = append(out, '\'')
		out = append(out, bytes.ReplaceAll(data[start:quoteEnd], []byte("'"), []byte("''"))...)
		out = append(out, '\'')
		out = append(out, bytes.Repeat([]byte(" "), end-quoteEnd)...)
		prev = end
	}
	return append(out, data[prev:]...)
}

// endSpan is the part of a line the rewrite changes. It quotes the
// bytes from start to quoteEnd and turns the blanks from quoteEnd to end
// into spaces.
type endSpan struct{ start, quoteEnd, end int }

// hasEndMarkerScalar reports whether a line of data starts with "..."
// and a character that isEndMarkerScalar accepts.
func hasEndMarkerScalar(data []byte) bool {
	for len(data) > 0 {
		if isEndMarkerScalar(data) {
			return true
		}
		nl := bytes.IndexByte(data, '\n')
		if nl < 0 {
			return false
		}
		data = data[nl+1:]
	}
	return false
}

// isEndMarkerScalar reports whether line starts with "..." and a fourth
// byte that is not a blank, a line break, or a ".". goccy reads those
// three dots as the end of a document, and YAML reads them as the start
// of a plain scalar. Four dots are text to goccy too.
func isEndMarkerScalar(line []byte) bool {
	return len(line) > 3 && line[0] == '.' && line[1] == '.' && line[2] == '.' &&
		bytes.IndexByte([]byte(" \t\r\n."), line[3]) < 0
}

// endMarkerSpans returns the byte spans of src to quote, in order. Each
// one is the scalar at the start of a line where goccy's lexer puts a
// document end at column 1, outside a flow collection or inside one.
func endMarkerSpans(src string) []endSpan {
	idx := newByteIndex(src)
	var spans []endSpan
	flow := 0
	for _, tk := range Tokenize(src) {
		switch tk.Type {
		case token.SequenceStartType, token.MappingStartType:
			flow++
		case token.SequenceEndType, token.MappingEndType:
			flow = max(flow-1, 0)
		case token.DocumentEndType:
			if tk.Position.Column != 1 {
				continue
			}
			span := endMarkerSpan
			if flow > 0 {
				span = flowEndMarkerSpan
			}
			if sp, ok := span(src, idx, tk.Position.Line); ok {
				spans = append(spans, sp)
			}
		default:
			// Other tokens leave the flow level as it stands.
		}
	}
	return spans
}

// endMarkerSpan returns the span to rewrite for the plain scalar that
// starts line, numbered from 1, when the line starts with one goccy
// misreads and quoting the scalar keeps its value. That holds for a key,
// and for a scalar that is no key only when it is the first content of
// its document and nothing after it continues it.
func endMarkerSpan(src string, idx byteIndex, line int) (endSpan, bool) {
	if line < 1 || line > len(idx.lines) {
		return endSpan{}, false
	}
	start := idx.lines[line-1]
	text, next, _ := strings.Cut(src[start:], "\n")
	if !isEndMarkerScalar([]byte(text)) {
		return endSpan{}, false
	}
	n, colon := PlainScalarEnd(text)
	if colon >= 0 {
		return endSpan{start, start + n, start + colon}, true
	}
	if !StartsDocument(src[:start]) || !endsScalar(next) {
		return endSpan{}, false
	}
	return endSpan{start, start + n, start + n}, true
}

// flowEndMarkerSpan returns the span to rewrite for the plain scalar
// that starts line, numbered from 1, inside a flow collection, when the
// line starts with one goccy misreads. spruce reads the scalar up to a
// ",", a "]", a "}", a ":" that a blank or the end of the line follows,
// or a comment. The rewrite quotes it when it stops at one of the first
// four. When it stops at a comment or at the end of its line, the
// rewrite quotes it only when the next line that is not blank or a
// comment starts with a ",", a "]", or a "}", as with "...x" before a
// "]" on its own line.
//
// It leaves the line as goccy reads it when the scalar holds a "?", a
// "[", a "{", or a ":" that a blank does not follow. spruce fails each
// of those, and the diff's yaml.v3 reads some of them as text, so no
// quoting would serve both. It also leaves a scalar that a more
// indented line or other text on a later line continues, as in
// "...x\ny", which spruce reads as "...x y". goccy then fails the
// stream, so no line of it is lost.
func flowEndMarkerSpan(src string, idx byteIndex, line int) (endSpan, bool) {
	if line < 1 || line > len(idx.lines) {
		return endSpan{}, false
	}
	start := idx.lines[line-1]
	text, next, _ := strings.Cut(src[start:], "\n")
	if !isEndMarkerScalar([]byte(text)) {
		return endSpan{}, false
	}
	end, colon, ok := flowScalarEnd(text)
	if !ok || (end == len(text) || text[end] == '#') && !flowScalarEnds(next) {
		return endSpan{}, false
	}
	n := len(strings.TrimRight(text[:end], " \t\r"))
	if colon >= 0 {
		return endSpan{start, start + n, start + colon}, true
	}
	return endSpan{start, start + n, start + n}, true
}

// flowScalarEnd returns where the flow scalar that starts text, a line
// that starts with "...", stops on that line, which is len(text) when
// nothing stops it, and the offset of the ":" that makes it a key, or
// -1. It reports false when the scalar holds a character spruce and the
// diff read differently.
func flowScalarEnd(text string) (end, colon int, ok bool) {
	for i := 3; i < len(text); i++ {
		switch c := text[i]; {
		case c == ':' && (i+1 == len(text) || isBlank(text[i+1])):
			return i, i, true
		case c == ',' || c == ']' || c == '}', c == '#' && isBlank(text[i-1]):
			return i, -1, true
		case c == ':' || c == '?' || c == '[' || c == '{':
			return 0, -1, false
		}
	}
	return len(text), -1, true
}

// flowScalarEnds reports whether rest, the text after a line whose flow
// scalar runs to its end or to a comment, ends that scalar the way
// spruce reads it. Blank lines and comment lines are skipped, and the
// next line has to hold a ",", a "]", or a "}" after its indent. Other
// text after the line break continues the scalar. After a comment,
// spruce fails any other text, including the ":" of a key that goccy
// would read, and a marker line or the end of the input fails the
// stream either way.
func flowScalarEnds(rest string) bool {
	for rest != "" {
		var text string
		text, rest, _ = strings.Cut(rest, "\n")
		content := strings.TrimLeft(text, " \t\r")
		if content == "" || content[0] == '#' {
			continue
		}
		return strings.IndexByte(",]}", content[0]) >= 0
	}
	return false
}

// PlainScalarEnd returns the length of the plain scalar that starts
// text, a line in block context that starts with "...", with the blanks
// after it trimmed, and the offset of the ":" that makes it a key, or -1
// when it is no key. A ":" ends the scalar when a blank or the end of
// the line follows it, and a "#" ends it when a blank comes before it.
func PlainScalarEnd(text string) (int, int) {
	end, colon := len(text), -1
	for i := 3; i < len(text); i++ {
		if text[i] == ':' && (i+1 == len(text) || isBlank(text[i+1])) {
			end, colon = i, i
			break
		}
		if text[i] == '#' && isBlank(text[i-1]) {
			end = i
			break
		}
	}
	return len(strings.TrimRight(text[:end], " \t\r")), colon
}

// StartsDocument reports whether before, the text ahead of a line, puts
// that line at the start of a document's content. Walking back to the
// last marker line or the start of the input, every line has to be
// blank, a comment, a directive, or node properties alone, and a "---"
// may carry only node properties. Anything else holds content the line
// would continue or join.
func StartsDocument(before string) bool {
	for before != "" {
		before = strings.TrimSuffix(before, "\n")
		cut := strings.LastIndexByte(before, '\n')
		text := strings.TrimSuffix(before[cut+1:], "\r")
		before = before[:cut+1]
		switch {
		case isMarker(text, "---"):
			return propertiesOnly(text[3:])
		case isMarker(text, "..."):
			return true
		case strings.HasPrefix(text, "%"):
		case !propertiesOnly(text):
			return false
		}
	}
	return true
}

// isMarker reports whether text is a marker line that starts with
// marker and then a blank or the end of the line.
func isMarker(text, marker string) bool {
	return strings.HasPrefix(text, marker) && (len(text) == 3 || isBlank(text[3]))
}

// propertiesOnly reports whether text holds nothing but tags, anchors,
// and a comment, or nothing at all.
func propertiesOnly(text string) bool {
	for _, f := range strings.Fields(text) {
		if f[0] == '#' {
			return true
		}
		if f[0] != '!' && f[0] != '&' {
			return false
		}
	}
	return true
}

// endsScalar reports whether no line of rest, the text after a line
// holding a plain scalar that is no key, continues that scalar. Lines
// that are blank or hold only a comment are skipped, and a marker line
// or the end of the input ends the scalar.
func endsScalar(rest string) bool {
	for rest != "" {
		var text string
		text, rest, _ = strings.Cut(rest, "\n")
		switch content := strings.Trim(text, " \t\r"); {
		case content == "" || content[0] == '#':
		case strings.HasPrefix(text, "---") || strings.HasPrefix(text, "..."):
			return isMarker(text, text[:3])
		default:
			return false
		}
	}
	return true
}

// isBlank reports whether c is a space, a tab, or a carriage return.
func isBlank(c byte) bool {
	return c == ' ' || c == '\t' || c == '\r'
}
