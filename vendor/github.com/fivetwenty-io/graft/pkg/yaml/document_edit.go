package yaml

import (
	"bytes"
	"errors"
	"strings"

	"github.com/goccy/go-yaml/lexer"
	"github.com/goccy/go-yaml/token"

	"github.com/fivetwenty-io/graft/internal/yamlnode"
	"github.com/fivetwenty-io/graft/internal/yamlprep"
)

// The fixed reasons an edit is refused with. None of them quotes the
// input.
const (
	reasonCarriageReturn = "the input holds a carriage return"
	reasonByteOrderMark  = "the input starts with a byte order mark"
	reasonThroughAlias   = "the path passes through an alias"
	reasonAlias          = "the value is an alias"
	reasonAnchored       = "the value is anchored"
	reasonTagged         = "the value has a tag"
	reasonCollection     = "the value is a collection"
	reasonNoText         = "the value has no text to replace"
	reasonMultiLine      = "the value is a multi-line flow scalar"
	reasonTab            = "a tab sits on a line the edit touches"
	reasonInexact        = "the value's source position is not exact"
)

// The errors scalarSpan returns. Each one's text is the reason an edit
// refuses with.
var (
	errMultiLine = errors.New(reasonMultiLine)
	errInexact   = errors.New(reasonInexact)
)

// span is a run of bytes in d.src, from start up to but not including
// end.
type span struct{ start, end int }

// slot is where Set writes the value at a path: the span of its text,
// and for a block scalar the header and the body's indentation.
type slot struct {
	span
	header blockHeader
	indent int
}

// Editable reports whether Set can replace the value at path. It
// returns nil when it can, and otherwise the error Set would return.
//
// Set replaces the text of one plain, single-quoted, or double-quoted
// scalar that sits on one line, which may be inside a flow collection,
// and the body of a literal or folded block scalar. It refuses, with
// ErrUnsupportedEdit, a value that is a collection, an alias, anchored,
// or tagged, a plain value with no text, a block scalar whose header has
// an indentation indicator, a value inside an anchored node or reached
// through an alias, a value on a line that holds a tab, and any value of
// an input that holds a carriage return or starts with a byte order
// mark. It also refuses a value whose text it can't place exactly in the
// input, such as one on a line that the parser only reads after
// rewriting it.
func (d *Document) Editable(path string) error {
	_, _, err := d.setTarget("edit", path)
	return err
}

// setTarget resolves path for Set and returns where the value is and the
// slot Set writes it in, or the error Set returns. op is the operation the
// caller reports in an error.
func (d *Document) setTarget(op, path string) (located, slot, error) {
	loc, err := d.resolve(op, path)
	if err != nil {
		return located{}, slot{}, err
	}
	if reason := d.setRefusal(loc); reason != "" {
		return located{}, slot{}, loc.refusal(op, path, reason)
	}
	var sl slot
	if isBlockScalar(loc.node) {
		sl.header, sl.span, sl.indent, err = d.blockSlot(loc.node)
		if err == nil && sl.end == len(d.src) && sl.header.chomp != '-' {
			// No line break follows the body, so only a strip header
			// can hold a value with text.
			err = errBlockFit
		}
	} else {
		sl.span, err = d.scalarSpan(loc.node)
	}
	if err != nil {
		return located{}, slot{}, loc.refusal(op, path, err.Error())
	}
	return loc, sl, nil
}

// refusal is the error that refuses an edit of the value l names, at the
// line the value is written on.
func (l located) refusal(op, path, reason string) error {
	return &DocumentError{Op: op, Path: path, Line: l.written().Line, Reason: reason, err: ErrUnsupportedEdit}
}

// written is the value l names as it is written, which is the alias
// itself when the value is an alias.
func (l located) written() *yamlnode.Node {
	return l.parent.Content[l.index]
}

// pathRefusal returns the reason no edit can change the value or entry
// at loc, or "". It covers what refuses every kind of edit: an input
// that holds a carriage return or starts with a byte order mark, whose
// lines and columns don't match the text the parser read, and an alias
// at or above the value, since the text there is shared with every other
// use of its anchor.
func (d *Document) pathRefusal(loc located) string {
	switch {
	case bytes.IndexByte(d.src, '\r') >= 0:
		return reasonCarriageReturn
	case hasByteOrderMark(d.src):
		return reasonByteOrderMark
	case loc.isAlias:
		return reasonAlias
	case loc.throughAlias:
		return reasonThroughAlias
	}
	return ""
}

// setRefusal returns the reason Set can't replace the value at loc, or
// "". It adds to pathRefusal the reasons that only Set has: a value that
// is not one scalar, a plain scalar with no text, an anchor on the value
// or on any node above it, which every alias of that anchor would see
// changed, and a tab on the value's line, after which goccy's column is
// not the column in the input. A block scalar's line is its header's.
// scalarSpan and blockSlot make the checks that need the value's text.
func (d *Document) setRefusal(loc located) string {
	if reason := d.pathRefusal(loc); reason != "" {
		return reason
	}
	n := loc.node
	switch {
	case loc.anchor:
		return reasonAnchored
	case n.Style&yamlnode.StyleTagged != 0 || n.BareTag:
		return reasonTagged
	case n.Kind != yamlnode.ScalarNode:
		return reasonCollection
	case n.Value == "" && n.Style&(yamlnode.StyleSingle|yamlnode.StyleDouble) == 0 && !isBlockScalar(n):
		return reasonNoText
	}
	return d.tabRefusal(min(n.Line, n.TokenLine), max(n.Line, n.TokenLine))
}

// tabRefusal returns the tab reason when a tab sits on any line from
// first to last, and "" otherwise.
func (d *Document) tabRefusal(first, last int) string {
	for line := max(first, 1); line <= last && line <= len(d.lines); line++ {
		if bytes.IndexByte(d.lineText(line), '\t') >= 0 {
			return reasonTab
		}
	}
	return ""
}

// lineText returns line line of d.src, counted from 1, with its line
// break.
func (d *Document) lineText(line int) []byte {
	return lineOf(d.src, d.lines, line)
}

// lineOf returns line line of src, whose line index is lines, counted
// from 1 and with its line break. A line past the end is empty.
func lineOf(src []byte, lines []int, line int) []byte {
	return src[lineStart(src, lines, line):lineStart(src, lines, line+1)]
}

// lineStart returns the offset in src at which line line starts, or
// len(src) for a line past the end.
func lineStart(src []byte, lines []int, line int) int {
	if line > len(lines) {
		return len(src)
	}
	return lines[line-1]
}

// hasByteOrderMark reports whether src starts with a UTF-8 or UTF-16
// byte order mark, which the parser takes off before it counts columns.
func hasByteOrderMark(src []byte) bool {
	return bytes.HasPrefix(src, []byte{0xEF, 0xBB, 0xBF}) ||
		bytes.HasPrefix(src, []byte{0xFF, 0xFE}) ||
		bytes.HasPrefix(src, []byte{0xFE, 0xFF})
}

// scalarSpan finds the bytes of a one-line scalar by lexing d.src as the
// parser reads it and taking the token that sits at the node's TokenLine
// and TokenColumn. It checks that those bytes read back as n.Value.
//
// The token's line must be one exactLine accepts. A quoted scalar ends
// at its closing quote, and a plain one where its token's text ends. A
// scalar that runs past its line gives errMultiLine, and any other doubt
// gives errInexact.
func (d *Document) scalarSpan(n *yamlnode.Node) (span, error) {
	line := n.TokenLine
	if n.Kind != yamlnode.ScalarNode {
		return span{}, errInexact
	}
	read, ok := d.exactLine(line)
	if !ok {
		return span{}, errInexact
	}
	tk := tokenAt(read, line, n.TokenColumn)
	if tk == nil || !scalarToken(tk.Type) {
		return span{}, errInexact
	}
	start, ok := d.offset(line, n.TokenColumn)
	if !ok {
		return span{}, errInexact
	}
	var end int
	var err error
	switch tk.Type {
	case token.DoubleQuoteType, token.SingleQuoteType:
		end, err = d.quotedEnd(tk.Type, start, d.textEnd(line))
	default:
		end, err = d.plainEnd(tk.Origin, start, d.textEnd(line))
	}
	if err != nil {
		return span{}, err
	}
	if !readsAs(string(d.src[start:end]), n.Value) {
		return span{}, errInexact
	}
	return span{start, end}, nil
}

// exactLine reports whether line line of d.src is one whose columns are
// those the parser reports, and returns d.src as the parser reads it.
// The parser reads the input with its byte
// order mark taken off, with every CR and U+0085 line break made LF, and
// with yamlprep's rewrites, which can move the columns of a line. So the
// line must read the same there as in d.src. goccy's column is also not
// the column in the input after a tab between tokens, so the line must
// hold no tab.
func (d *Document) exactLine(line int) ([]byte, bool) {
	if line < 1 || line > len(d.lines) {
		return nil, false
	}
	asWritten := d.lineText(line)
	if bytes.IndexByte(asWritten, '\t') >= 0 {
		return nil, false
	}
	read, _ := yamlprep.Prepare(parsedText(d.src))
	readLines := newLineIndex(read)
	if line > len(readLines) || !bytes.Equal(withoutBreak(lineOf(read, readLines, line)), withoutBreak(asWritten)) {
		return nil, false
	}
	return read, true
}

// textEnd returns the offset in d.src at which the text of line line
// ends, which is where its line break starts.
func (d *Document) textEnd(line int) int {
	return lineStart(d.src, d.lines, line) + len(withoutBreak(d.lineText(line)))
}

// quotedEnd returns the offset just past the closing quote of the quoted
// scalar of token type t that opens at start, on a line whose text ends
// at lineEnd. goccy's Origin leaves out some escapes, such as \u0001, so
// the end is found in the input rather than taken from the token.
func (d *Document) quotedEnd(t token.Type, start, lineEnd int) (int, error) {
	quote := byte('"')
	if t == token.SingleQuoteType {
		quote = '\''
	}
	if start >= lineEnd || d.src[start] != quote {
		return 0, errInexact
	}
	end := closingQuote(d.src[:lineEnd], start)
	if end < 0 {
		return 0, errMultiLine
	}
	return end, nil
}

// plainEnd returns the offset where the plain scalar whose token text is
// origin ends, when it opens at start on a line whose text ends at
// lineEnd.
func (d *Document) plainEnd(origin string, start, lineEnd int) (int, error) {
	text := strings.Trim(origin, " \t\r\n")
	if strings.ContainsAny(text, "\r\n") {
		return 0, errMultiLine
	}
	end := start + len(text)
	if text == "" || end > lineEnd || string(d.src[start:end]) != text {
		return 0, errInexact
	}
	return end, nil
}

// closingQuote returns the offset just past the quote that closes the
// quoted scalar opening at src[start], or -1 when src holds none. In
// double quotes a backslash escapes the byte after it, and in single
// quotes two quotes stand for one.
func closingQuote(src []byte, start int) int {
	q := src[start]
	for i := start + 1; i < len(src); i++ {
		switch {
		case q == '"' && src[i] == '\\':
			i++
		case src[i] != q:
		case q == '\'' && i+1 < len(src) && src[i+1] == '\'':
			i++
		default:
			return i + 1
		}
	}
	return -1
}

// withoutBreak returns line without the line break that ends it.
func withoutBreak(line []byte) []byte {
	switch {
	case bytes.HasSuffix(line, []byte("\r\n")):
		return line[:len(line)-2]
	case bytes.HasSuffix(line, nextLine):
		return line[:len(line)-len(nextLine)]
	case bytes.HasSuffix(line, []byte("\n")), bytes.HasSuffix(line, []byte("\r")):
		return line[:len(line)-1]
	}
	return line
}

// tokenAt returns the token goccy's lexer places at line and column of
// text, or nil.
func tokenAt(text []byte, line, column int) (found *token.Token) {
	defer func() {
		if recover() != nil {
			found = nil
		}
	}()
	for _, tk := range lexer.Tokenize(string(text)) {
		if tk.Position != nil && tk.Position.Line == line && tk.Position.Column == column {
			return tk
		}
	}
	return nil
}

// scalarToken reports whether a token of type t is the text of a plain
// or quoted scalar.
func scalarToken(t token.Type) bool {
	switch t {
	case token.StringType, token.SingleQuoteType, token.DoubleQuoteType,
		token.NullType, token.BoolType, token.IntegerType, token.BinaryIntegerType,
		token.OctetIntegerType, token.HexIntegerType, token.FloatType,
		token.InfinityType, token.NanType:
		return true
	default:
		return false
	}
}

// readsAs reports whether text, written as a mapping value, parses to
// one scalar whose value is value.
func readsAs(text, value string) bool {
	docs, err := yamlnode.Parse([]byte("k: " + text))
	if err != nil || len(docs) != 1 || len(docs[0].Content) != 1 {
		return false
	}
	m := docs[0].Content[0]
	if m.Kind != yamlnode.MappingNode || len(m.Content) != 2 {
		return false
	}
	v := m.Content[1]
	return v.Kind == yamlnode.ScalarNode && v.Value == value
}
