package yaml

import (
	"errors"
	"strings"

	"github.com/fivetwenty-io/graft/internal/yamlnode"
)

// The reasons Set refuses a block scalar edit with.
const (
	reasonBlockString     = "a block scalar takes a string"
	reasonIndentIndicator = "the block scalar has an indentation indicator"
	reasonBlockFit        = "the value does not fit the block scalar's header"
)

// The errors the block scalar helpers return. Each one's text is the
// reason an edit refuses with.
var (
	errIndentIndicator = errors.New(reasonIndentIndicator)
	errBlockFit        = errors.New(reasonBlockFit)
)

// blockHeader is what a block scalar's header says about its body.
type blockHeader struct {
	folded   bool
	chomp    byte // 0 for clip, '-' for strip, '+' for keep
	explicit bool // the header has an indentation indicator
}

// isBlockScalar reports whether n is a literal or folded block scalar.
func isBlockScalar(n *yamlnode.Node) bool {
	return n.Kind == yamlnode.ScalarNode && n.Style&(yamlnode.StyleLiteral|yamlnode.StyleFolded) != 0
}

// parseBlockHeader reads the indicator text that starts at the node's
// TokenColumn on its header line, such as "|", ">-", or "|+", up to the
// end of that line. The indicator takes at most one chomping indicator
// and one indentation indicator, in either order, and only white space
// or white space and a comment may follow them.
func parseBlockHeader(text string) (blockHeader, bool) {
	if text == "" || (text[0] != '|' && text[0] != '>') {
		return blockHeader{}, false
	}
	h := blockHeader{folded: text[0] == '>'}
	i := 1
	for ; i < len(text); i++ {
		c := text[i]
		if (c == '+' || c == '-') && h.chomp == 0 {
			h.chomp = c
		} else if c >= '1' && c <= '9' && !h.explicit {
			h.explicit = true
		} else {
			break
		}
	}
	rest := text[i:]
	after := strings.TrimLeft(rest, " \t")
	if rest != "" && (len(after) == len(rest) || (after != "" && after[0] != '#')) {
		return blockHeader{}, false
	}
	return h, true
}

// indicator returns the header text that h stands for, without an
// indentation indicator.
func (h blockHeader) indicator() string {
	text := "|"
	if h.folded {
		text = ">"
	}
	if h.chomp != 0 {
		text += string(h.chomp)
	}
	return text
}

// blockSlot reads the header of the block scalar n and returns it, with
// the span of n's body and the body's indentation, as blockSpan gives
// them. It refuses a header with an indentation indicator, and returns
// errInexact for a header it can't place exactly in the input.
func (d *Document) blockSlot(n *yamlnode.Node) (blockHeader, span, int, error) {
	line := n.TokenLine
	if !isBlockScalar(n) {
		return blockHeader{}, span{}, 0, errInexact
	}
	if _, ok := d.exactLine(line); !ok {
		return blockHeader{}, span{}, 0, errInexact
	}
	start, ok := d.offset(line, n.TokenColumn)
	if !ok {
		return blockHeader{}, span{}, 0, errInexact
	}
	h, ok := parseBlockHeader(string(d.src[start:d.textEnd(line)]))
	if !ok || h.folded != (n.Style&yamlnode.StyleFolded != 0) {
		return blockHeader{}, span{}, 0, errInexact
	}
	if h.explicit {
		return blockHeader{}, span{}, 0, errIndentIndicator
	}
	sp, indent, err := d.blockSpan(n, h)
	if err != nil {
		return blockHeader{}, span{}, 0, err
	}
	return h, sp, indent, nil
}

// blockSpan returns the bytes of the body of the block scalar n, whose
// header is h, and the body's indentation. The span runs from the line
// break that ends the header line to the end of the last body line, not
// counting that line's own break, which stays in place after an edit.
// So the span of an empty body is empty and sits at the header's line
// break. The last body line is, for clip and strip, the last line with
// content, and for keep, the last of the blank lines that follow it.
//
// The body's indentation is that of its first line with content, or the
// header line's own indentation plus 2 when the body has none. The
// header's line break must be LF, and the span must read back as
// n.Value, or the body has no span.
func (d *Document) blockSpan(n *yamlnode.Node, h blockHeader) (sp span, indent int, err error) {
	header := n.TokenLine
	if header < 1 || header > len(d.lines) {
		return span{}, 0, errInexact
	}
	start := d.textEnd(header)
	if start < len(d.src) && d.src[start] != '\n' {
		return span{}, 0, errInexact
	}
	indent, last := leadingSpaces(d.lineText(header))+2, header
	if strings.Trim(n.Value, "\n") != "" {
		var ok bool
		if indent, last, ok = d.bodyLines(header, h.chomp == '+'); !ok {
			return span{}, 0, errInexact
		}
	}
	sp = span{start, d.textEnd(last)}
	readBack := h.indicator() + string(d.src[sp.start:sp.end])
	if sp.end < len(d.src) {
		readBack += "\n"
	}
	if !readsAs(readBack, n.Value) {
		return span{}, 0, errInexact
	}
	return sp, indent, nil
}

// bodyLines returns the indentation and the last line of the body that
// follows the header on line header, where the body has a line with
// content. The first line with any character other than a space sets
// the indentation, and the body runs on through every line indented at
// least that far, and every blank line, up to the first line indented
// less. A line of spaces only is blank when it is no longer than the
// indentation, and content otherwise. The last line is the last one
// with content, or with keep the last blank line that follows it. It
// reports false when no line has content.
func (d *Document) bodyLines(header int, keep bool) (indent, last int, ok bool) {
	indent, last = -1, header
	for line := header + 1; line <= len(d.lines) && d.lines[line-1] < len(d.src); line++ {
		text := withoutBreak(d.lineText(line))
		lead := leadingSpaces(text)
		switch {
		case lead == len(text) && (indent < 0 || lead <= indent):
			if keep && indent >= 0 {
				last = line
			}
		case indent < 0:
			indent, last = lead, line
		case lead >= indent:
			last = line
		default:
			return indent, last, true
		}
	}
	return indent, last, indent >= 0
}

// leadingSpaces returns the number of spaces line starts with.
func leadingSpaces(line []byte) int {
	n := 0
	for n < len(line) && line[n] == ' ' {
		n++
	}
	return n
}

// blockBody renders value as body lines indented by indent spaces. Each
// line comes after the line break that ends the line before it, so the
// text goes in place of blockSpan's span, and the break after the last
// line is the one already in the input. A line with text gets exactly
// indent spaces before it and an empty line gets none, so no line ends
// in white space.
//
// It refuses a value the header's chomping can't hold, a value whose
// first non-empty line starts with a space or a tab, and, for a folded
// header, any line that starts with a space or a tab. It also refuses a
// value that holds a carriage return or U+0085, which the parser would
// read back as a line break, or U+2028 or U+2029, which YAML 1.1
// parsers such as yaml.v3 and libyaml read as one. Clip takes an
// empty value or one that ends in exactly one line break, strip a value
// with no final line break, and keep a value with at least one, each
// extra one of which becomes a blank line. A value that holds nothing
// but line breaks has no text to set the indentation, so no header
// takes it. A folded body writes each run of m line breaks between two
// pieces of text as m+1 line breaks, and never folds a long line.
func blockBody(value string, h blockHeader, indent int) (string, error) {
	text := strings.TrimRight(value, "\n")
	breaks := len(value) - len(text)
	core := strings.TrimLeft(text, "\n")
	if !chompFits(h.chomp, value, core, breaks) || startsIndented(core) ||
		strings.ContainsAny(value, "\r\u0085\u2028\u2029") {
		return "", errBlockFit
	}
	if value == "" {
		return "", nil
	}
	var b strings.Builder
	b.WriteString(strings.Repeat("\n", len(text)-len(core)))
	pad := strings.Repeat(" ", indent)
	lines := strings.Split(core, "\n")
	for i, line := range lines {
		if line == "" {
			b.WriteString("\n")
			continue
		}
		if h.folded && startsIndented(line) {
			return "", errBlockFit
		}
		b.WriteString("\n" + pad + line)
		if h.folded && i < len(lines)-1 {
			b.WriteString("\n")
		}
	}
	b.WriteString(strings.Repeat("\n", max(breaks-1, 0)))
	return b.String(), nil
}

// chompFits reports whether the chomping indicator chomp can hold value,
// whose text without its leading and final line breaks is core and
// which ends in breaks line breaks.
func chompFits(chomp byte, value, core string, breaks int) bool {
	switch {
	case value == "":
		return chomp != '+'
	case core == "":
		return false
	case chomp == '-':
		return breaks == 0
	case chomp == '+':
		return breaks >= 1
	default:
		return breaks == 1
	}
}

// startsIndented reports whether line starts with a space or a tab.
func startsIndented(line string) bool {
	return line != "" && (line[0] == ' ' || line[0] == '\t')
}

// setBlock makes Set's edit of the block scalar at loc, whose slot
// setTarget has found. It writes value as the scalar's body under the header
// as written, at the body's indentation, and leaves the header line and
// its comment alone. A value equal to the one the scalar holds changes
// nothing, since the body may be written another way that reads the
// same.
func (d *Document) setBlock(path string, loc located, sl slot, value any) error {
	const op = "set"
	s, ok := value.(string)
	if !ok {
		return loc.refusal(op, path, reasonBlockString)
	}
	h, sp, indent := sl.header, sl.span, sl.indent
	if s == loc.node.Value {
		return nil
	}
	body, err := blockBody(s, h, indent)
	if err != nil || (sp.end == len(d.src) && strings.HasSuffix(s, "\n")) {
		// With no line break after the body, the value can't end in one.
		return loc.refusal(op, path, reasonBlockFit)
	}
	next := make([]byte, 0, len(d.src)-(sp.end-sp.start)+len(body))
	next = append(next, d.src[:sp.start]...)
	next = append(next, body...)
	next = append(next, d.src[sp.end:]...)
	w := blockText{header: h, body: body, value: s, indent: indent, of: loc.node}
	wrote := span{sp.start, sp.start + len(body)}
	return d.commit(path, next, loc, sp, func(n *yamlnode.Node) error {
		return w.check(next, wrote, n)
	})
}

// blockText is the body Set writes in place of a block scalar's body,
// and what the scalar must read as once it is written.
type blockText struct {
	header blockHeader
	body   string
	value  string
	indent int
	of     *yamlnode.Node // the scalar as it was before the edit
}

// check reports whether n, a node of the tree parsed from next, is the
// block scalar Set meant to write at wrote. It must be a string with
// the value Set wrote and the old node's style, its header must sit
// where the old one did and say the same, and blockSpan must place its
// body exactly on the bytes at wrote, at the indentation Set wrote them
// with when the value has text.
func (w blockText) check(next []byte, wrote span, n *yamlnode.Node) error {
	if n.Kind != yamlnode.ScalarNode || n.Value != w.value || n.Tag != yamlnode.TagStr ||
		n.Style != w.of.Style || n.BareTag || n.TokenLine != w.of.TokenLine || n.TokenColumn != w.of.TokenColumn {
		return errNotWritten
	}
	edited := &Document{src: next, lines: newLineIndex(next)}
	h, at, indent, err := edited.blockSlot(n)
	if err != nil || h != w.header || at != wrote || string(next[at.start:at.end]) != w.body {
		return errNotWritten
	}
	if strings.Trim(w.value, "\n") != "" && indent != w.indent {
		return errNotWritten
	}
	return nil
}
