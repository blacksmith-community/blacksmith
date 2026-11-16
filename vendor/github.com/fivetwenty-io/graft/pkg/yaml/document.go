package yaml

import (
	"bytes"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/fivetwenty-io/graft/internal/yamlnode"
)

// Document is one YAML file held as the bytes it was read from, so that
// edits change only the text they name. It is not graft's merge
// document, pkg/graft.Document, which holds decoded Go values.
//
// Until an edit succeeds, Bytes returns the input byte for byte, with its
// comments, blank lines, quoting, and line breaks exactly as written. A
// path names a node the way graft's cursor paths do, such as
// "jobs.name=web.port", "jobs.web.port", or "meta.[a.b]".
//
// A Document is not safe for concurrent use.
type Document struct {
	src   []byte
	docs  []*yamlnode.Node
	lines []int // byte offset of the start of each line, lines[0] == 0
}

// The sentinel errors a Document method wraps in a *DocumentError, so a
// caller can branch on them with errors.Is.
var (
	// ErrPathNotFound means the path names nothing in the input.
	ErrPathNotFound = errors.New("yaml: path not found")
	// ErrInvalidPath means the path itself can't be read.
	ErrInvalidPath = errors.New("yaml: invalid path")
	// ErrAmbiguousPath means the path could name more than one node.
	ErrAmbiguousPath = errors.New("yaml: path is ambiguous")
	// ErrUnsupportedEdit means the edit can't be made by changing only
	// the text it names.
	ErrUnsupportedEdit = errors.New("yaml: edit not supported")
)

// DocumentError is the error a Document method returns. Reason is
// fixed text that never quotes the input, and Unwrap returns one of the
// sentinel errors above.
type DocumentError struct {
	Op     string // "get", "set", "delete", or "edit"
	Path   string // the path the caller gave
	Line   int    // 1-based line of the node involved, or 0
	Reason string
	err    error
}

// Error returns "yaml: <op> <path>: <reason>", with " at line <n>" after
// it when the error has a line.
func (e *DocumentError) Error() string {
	msg := fmt.Sprintf("yaml: %s %s: %s", e.Op, e.Path, e.Reason)
	if e.Line > 0 {
		msg += fmt.Sprintf(" at line %d", e.Line)
	}
	return msg
}

// Unwrap returns the sentinel error e is an instance of.
func (e *DocumentError) Unwrap() error { return e.err }

// ParseDocument reads data as a YAML stream and keeps its own copy of
// the bytes. It accepts any stream Unmarshal's reader accepts, including
// empty input, several documents, CRLF line breaks, tabs, and a byte
// order mark. A syntax error comes back as a *DecodeError.
//
// A path can only name a node in a stream that holds exactly one
// document, so the methods that take one refuse a stream with several
// with ErrAmbiguousPath, and an empty stream with ErrPathNotFound.
func ParseDocument(data []byte) (*Document, error) {
	docs, err := yamlnode.Parse(data)
	if err != nil {
		return nil, translate(err)
	}
	src := bytes.Clone(data)
	if src == nil {
		src = []byte{}
	}
	return &Document{src: src, docs: docs, lines: newLineIndex(src)}, nil
}

// Bytes returns a copy of the document's text, which is the input byte
// for byte until an edit succeeds.
func (d *Document) Bytes() []byte {
	return bytes.Clone(d.src)
}

// Get returns the node path names. Its Line and Column are those of the
// input, and the tree under it is the one Unmarshal into a Node gives for
// the same text. A path that reaches an alias returns the node the alias
// refers to. Get never expands a << merge, so a key that only a merge
// brings in is not found. The returned Node is a copy, so changing it
// does not change the Document.
func (d *Document) Get(path string) (*Node, error) {
	loc, err := d.resolve("get", path)
	if err != nil {
		return nil, err
	}
	return publicNode(d.src, loc.node), nil
}

// publicNode returns the Node that Get gives for n, a node of the tree
// yamlnode.Parse reads from src.
func publicNode(src []byte, n *yamlnode.Node) *Node {
	text := parsedText(src)
	c := &converter{
		seen:  make(map[*yamlnode.Node]*Node),
		lines: bytes.Split(text, []byte("\n")),
		text:  &readText{doc: text},
	}
	return c.node(n)
}

// parsedText returns src as yamlnode.Parse reads it, with no byte order
// mark and with every CRLF, lone CR, and U+0085 made LF, so that its
// lines and columns are the ones the parsed tree reports.
func parsedText(src []byte) []byte {
	text := yamlnode.TrimBOM(src)
	if bytes.IndexByte(text, '\r') < 0 && !bytes.Contains(text, nextLine) {
		return text
	}
	text = bytes.ReplaceAll(text, []byte("\r\n"), []byte("\n"))
	text = bytes.ReplaceAll(text, []byte("\r"), []byte("\n"))
	return bytes.ReplaceAll(text, nextLine, []byte("\n"))
}

// nextLine is U+0085 in UTF-8, which the parser reads as a line break.
var nextLine = []byte{0xC2, 0x85}

// newLineIndex returns the byte offset in src at which each line starts.
// A line ends at LF, CRLF, a lone CR, or U+0085, as it does for the
// parser, so the index of a node's Line is the line it sits on.
func newLineIndex(src []byte) []int {
	lines := []int{0}
	for i := 0; i < len(src); i++ {
		switch {
		case src[i] == '\n':
			lines = append(lines, i+1)
		case src[i] == '\r' && i+1 < len(src) && src[i+1] == '\n':
			i++
			lines = append(lines, i+1)
		case src[i] == '\r':
			lines = append(lines, i+1)
		case src[i] == nextLine[0] && i+1 < len(src) && src[i+1] == nextLine[1]:
			i++
			lines = append(lines, i+1)
		}
	}
	return lines
}

// offset returns the byte offset in d.src of column column on line line,
// both counted from 1, with the column counted in runes. A column one
// past the last rune of the line is the line's break, or the end of the
// input on the last line, and is allowed. A place outside the input
// reports false.
func (d *Document) offset(line, column int) (int, bool) {
	if line < 1 || line > len(d.lines) || column < 1 {
		return 0, false
	}
	at := d.lines[line-1]
	for col := 1; col < column; col++ {
		if at >= len(d.src) || endsLine(d.src[at:]) {
			return 0, false
		}
		_, size := utf8.DecodeRune(d.src[at:])
		at += size
	}
	return at, true
}

// endsLine reports whether b starts with a line break.
func endsLine(b []byte) bool {
	return b[0] == '\n' || b[0] == '\r' || bytes.HasPrefix(b, nextLine)
}
