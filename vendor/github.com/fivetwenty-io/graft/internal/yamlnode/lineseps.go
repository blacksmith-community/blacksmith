package yamlnode

import (
	"bytes"
	"strings"
	"unicode/utf8"

	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/token"
)

// libyaml reads a LINE SEPARATOR (U+2028) or PARAGRAPH SEPARATOR
// (U+2029) as a line break, and keeps the character itself in the value
// where the break is kept. goccy reads either one as an ordinary
// character. spruce writes a value that holds one in single quotes or in
// a literal block scalar, with the separator followed by the indentation
// of a new line, and libyaml reads those spaces as indentation.
const (
	lineSeparator      = " "
	paragraphSeparator = " "
)

// hasLineSeparator reports whether data holds a LINE SEPARATOR or a
// PARAGRAPH SEPARATOR.
func hasLineSeparator(data []byte) bool {
	return bytes.Contains(data, []byte(lineSeparator)) || bytes.Contains(data, []byte(paragraphSeparator))
}

// fixLineSeparators gives every single-quoted scalar and literal block
// scalar in file that holds a LINE SEPARATOR or PARAGRAPH SEPARATOR the
// value libyaml reads for it. Double-quoted, plain, and folded scalars
// keep goccy's value.
func fixLineSeparators(file *ast.File) {
	for _, doc := range file.Docs {
		ast.Walk(lineSeparatorFixer{}, doc)
	}
}

type lineSeparatorFixer struct{}

func (f lineSeparatorFixer) Visit(n ast.Node) ast.Visitor {
	switch x := n.(type) {
	case *ast.StringNode:
		tk := x.GetToken()
		if tk == nil || tk.Type != token.SingleQuoteType || !strings.ContainsAny(tk.Origin, lineSeparator+paragraphSeparator) {
			return f
		}
		if v, ok := singleQuotedValue(tk.Origin); ok {
			x.Value, tk.Value = v, v
		}
	case *ast.LiteralNode:
		if x.Start == nil || x.Start.Type != token.LiteralType || x.Value == nil ||
			!strings.ContainsAny(x.Value.Value, lineSeparator+paragraphSeparator) {
			return nil
		}
		indent := blockIndent(x.Start, strings.Split(x.Value.GetToken().Origin, "\n"))
		v := literalSeparatorValue(x.Value.Value, indent, x.Start.Value)
		x.Value.Value = v
		if tk := x.Value.GetToken(); tk != nil {
			tk.Value = v
		}
		return nil
	}
	return f
}

// isFlowBreak reports whether s starts with a line break as libyaml
// reads one, and returns the break's length in s and the text libyaml
// puts in a value for it. A CRLF, a lone CR, an LF, and a NEXT LINE
// (U+0085) each read as "\n"; a LINE SEPARATOR and a PARAGRAPH
// SEPARATOR read as themselves.
func isFlowBreak(s string) (int, string, bool) {
	switch {
	case strings.HasPrefix(s, "\r\n"):
		return 2, "\n", true
	case strings.HasPrefix(s, "\r"), strings.HasPrefix(s, "\n"):
		return 1, "\n", true
	case strings.HasPrefix(s, "\u0085"):
		return len("\u0085"), "\n", true
	case strings.HasPrefix(s, lineSeparator):
		return len(lineSeparator), lineSeparator, true
	case strings.HasPrefix(s, paragraphSeparator):
		return len(paragraphSeparator), paragraphSeparator, true
	}
	return 0, "", false
}

// singleQuotedValue returns the value libyaml's scan_flow_scalar reads
// for the single-quoted scalar whose source text, quotes included, ends
// origin. A run of spaces and tabs before a line break is dropped, as is
// one after it. A single LF break between two pieces of text folds to a
// space, a run of LF breaks after the first one is kept as it is, and a
// break that starts with a LINE SEPARATOR or PARAGRAPH SEPARATOR keeps
// that character and the breaks after it.
func singleQuotedValue(origin string) (string, bool) {
	open, end := strings.IndexByte(origin, '\''), strings.LastIndexByte(origin, '\'')
	if open < 0 || end <= open {
		return "", false
	}
	s := origin[open+1 : end]
	var out, blanks strings.Builder
	for i := 0; i < len(s); {
		switch c := s[i]; {
		case c == '\'' && i+1 < len(s) && s[i+1] == '\'':
			out.WriteString(blanks.String())
			blanks.Reset()
			out.WriteByte('\'')
			i += 2
		case c == ' ' || c == '\t':
			blanks.WriteByte(c)
			i++
		default:
			n, leading, ok := isFlowBreak(s[i:])
			if !ok {
				out.WriteString(blanks.String())
				blanks.Reset()
				_, size := utf8.DecodeRuneInString(s[i:])
				out.WriteString(s[i : i+size])
				i += size
				continue
			}
			blanks.Reset()
			i = foldFlowBreak(s, i+n, leading, &out)
		}
	}
	out.WriteString(blanks.String())
	return out.String(), true
}

// foldFlowBreak reads the line breaks, spaces, and tabs in s from i on,
// which follow a first line break that reads as leading, writes the
// text libyaml folds them to into out, and returns the index of the
// first byte past them.
func foldFlowBreak(s string, i int, leading string, out *strings.Builder) int {
	var trailing strings.Builder
	for i < len(s) {
		if s[i] == ' ' || s[i] == '\t' {
			i++
			continue
		}
		m, brk, ok := isFlowBreak(s[i:])
		if !ok {
			break
		}
		trailing.WriteString(brk)
		i += m
	}
	switch {
	case leading != "\n":
		out.WriteString(leading + trailing.String())
	case trailing.Len() == 0:
		out.WriteByte(' ')
	default:
		out.WriteString(trailing.String())
	}
	return i
}

// literalSeparatorValue turns value, goccy's reading of a literal block
// scalar with content indentation indent and header header, into
// libyaml's. libyaml starts a new line after each LINE SEPARATOR or
// PARAGRAPH SEPARATOR, so up to indent spaces after one are indentation.
// It also chomps the content's last line break and the breaks after it,
// which are separators or LF breaks, where goccy chomps only the LF
// breaks at the end.
func literalSeparatorValue(value string, indent int, header string) string {
	var b strings.Builder
	for i := 0; i < len(value); {
		n, brk, ok := isFlowBreak(value[i:])
		if !ok || brk == "\n" {
			b.WriteByte(value[i])
			i++
			continue
		}
		b.WriteString(brk)
		i += n
		for k := 0; k < indent && i < len(value) && value[i] == ' '; k++ {
			i++
		}
	}
	v := b.String()
	if strings.Contains(header, "+") {
		return v
	}
	content := strings.TrimRight(v, "\n"+lineSeparator+paragraphSeparator)
	if content == "" || content == v {
		return v
	}
	if strings.Contains(header, "-") {
		return content
	}
	_, size := utf8.DecodeRuneInString(v[len(content):])
	return v[:len(content)+size]
}
