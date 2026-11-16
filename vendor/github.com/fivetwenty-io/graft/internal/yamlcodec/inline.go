package yamlcodec

import (
	"github.com/fivetwenty-io/graft/internal/yamlnode"
)

// InlineStyle is the style a caller would like a one-line scalar in.
type InlineStyle uint8

const (
	// InlinePlain asks for the style the encoder would choose, which is
	// plain unless plain text would read back as something else.
	InlinePlain InlineStyle = iota
	// InlineSingle asks for single quotes.
	InlineSingle
	// InlineDouble asks for double quotes.
	InlineDouble
)

// InlineScalar returns s written as a scalar on one line. It uses
// prefer when s can be written that way and reads back as the same
// string, and otherwise the style the encoder would choose for s. A
// string holding a line break is always double-quoted. It never folds.
//
// Single quotes hold no escapes, so a string with a character that
// needs one, such as a tab or a control character, is double-quoted
// when prefer is InlineSingle. Text in any style but double quotes is
// parsed back, and a string the parser would read differently, such as
// "<<" or a date the encoder writes plain, is double-quoted instead.
// The result reads back as s where a block mapping value or sequence
// item sits, and a caller writing inside a flow collection must still
// quote text that holds a flow indicator.
func InlineScalar(s string, prefer InlineStyle) string {
	value := []byte(s)
	a := analyzeScalar(value)
	style := doubleQuotedScalarStyle
	switch {
	case a.multiline:
	case prefer == InlineSingle && a.singleQuotedAllowed:
		style = singleQuotedScalarStyle
	case prefer == InlinePlain:
		style = selectScalarStyle(value, requestedScalarStyle(s), a, false)
	}
	if style == literalScalarStyle {
		style = doubleQuotedScalarStyle
	}
	if style != doubleQuotedScalarStyle {
		if out := writeInline(value, style); readsBackAs(out, s) {
			return out
		}
	}
	return writeInline(value, doubleQuotedScalarStyle)
}

// writeInline writes value in style at the start of a line, with
// folding turned off.
func writeInline(value []byte, style scalarStyle) string {
	w := scalarWriter{whitespace: true, indention: true}
	switch style {
	case plainScalarStyle:
		w.writePlain(value, false)
	case singleQuotedScalarStyle:
		w.writeSingleQuoted(value, false)
	default:
		w.writeDoubleQuoted(value, false)
	}
	return string(w.buf)
}

// readsBackAs reports whether text, written as a block mapping value,
// parses to the string s.
func readsBackAs(text, s string) bool {
	docs, err := yamlnode.Parse([]byte("k: " + text + "\n"))
	if err != nil || len(docs) != 1 || len(docs[0].Content) != 1 {
		return false
	}
	m := docs[0].Content[0]
	if m.Kind != yamlnode.MappingNode || len(m.Content) != 2 {
		return false
	}
	v := m.Content[1]
	return v.Kind == yamlnode.ScalarNode && v.Value == s && v.Tag == yamlnode.TagStr
}
