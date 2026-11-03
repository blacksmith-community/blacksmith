package yamlnode

import (
	"strings"

	"github.com/goccy/go-yaml/token"

	"github.com/fivetwenty-io/graft/internal/yamlprep"
)

// complexKey is a mapping key that is a collection, which goccy cannot
// parse and yaml.v3 reads. line and col are where the key starts in the
// chunk, numbered from 1, and node is the key parsed on its own.
type complexKey struct {
	line, col int
	node      *Node
}

// keyRegion is the text of a collection key: from its first token to
// endCol on endLine, both numbered from 1, with endCol just past the
// key's last character. A flow key's end is found in the text, since
// goccy puts every token after a tag on the tag's line one column early.
type keyRegion struct {
	line, col, endLine, endCol int
	flow                       bool
}

// placeComplexKeys finds every mapping key in text that is a collection,
// a flow collection before ":" or a flow or block collection after "?",
// and returns text with each such key replaced by a plain scalar of
// underscores on its first line and spaces on the rest, so every other
// token keeps its line and column. Each key is parsed on its own, with
// its columns kept, and returned keyed by where it starts. It reports
// false when text holds no such key or one of them does not parse, and
// always unless the builder reads collection keys.
func (b *builder) placeComplexKeys(text string) (string, []complexKey, bool) {
	if !b.opts.CollectionKeys {
		return "", nil, false
	}
	regions := complexKeyRegions(yamlprep.Tokenize(text))
	if len(regions) == 0 {
		return "", nil, false
	}
	lines := strings.SplitAfter(text, "\n")
	runes := make([][]rune, len(lines))
	for i, l := range lines {
		runes[i] = []rune(l)
	}
	keys := make([]complexKey, 0, len(regions))
	for _, r := range regions {
		src, ok := cutRegion(runes, r)
		if !ok {
			return "", nil, false
		}
		docs, err := ParseWith([]byte(src), b.opts)
		if err != nil || len(docs) != 1 || len(docs[0].Content) != 1 {
			return "", nil, false
		}
		node := docs[0].Content[0]
		if node.Kind != MappingNode && node.Kind != SequenceNode {
			return "", nil, false
		}
		keys = append(keys, complexKey{line: r.line, col: r.col, node: node})
	}
	var out strings.Builder
	for _, row := range runes {
		out.WriteString(string(row))
	}
	return out.String(), keys, true
}

// cutRegion returns the text of the key in region r, with the columns
// before it on its first line as spaces, and replaces it in runes with
// underscores on its first line and spaces on the rest.
func cutRegion(runes [][]rune, r keyRegion) (string, bool) {
	if r.flow {
		var ok bool
		if r.endLine, r.endCol, ok = scanFlowEnd(runes, r.line, r.col); !ok {
			return "", false
		}
	}
	if r.endLine > len(runes) {
		return "", false
	}
	var src strings.Builder
	for l := r.line; l <= r.endLine; l++ {
		row := runes[l-1]
		from, to, fill := 0, lineEnd(row), ' '
		if l == r.line {
			from, fill = r.col-1, '_'
			src.WriteString(strings.Repeat(" ", from))
		}
		if l == r.endLine {
			to = min(to, r.endCol-1)
		}
		if from > to {
			return "", false
		}
		src.WriteString(string(row[from:to]))
		src.WriteByte('\n')
		for i := from; i < to; i++ {
			row[i] = fill
		}
	}
	return src.String(), true
}

// lineEnd is the length of row without its line break.
func lineEnd(row []rune) int {
	if n := len(row); n > 0 && row[n-1] == '\n' {
		return n - 1
	}
	return len(row)
}

// complexKeyRegions returns the region of every collection key outside
// a flow collection, in source order. A key inside another key's region
// is left to that key's own parse.
func complexKeyRegions(toks token.Tokens) []keyRegion {
	var regions []keyRegion
	depth := 0
	for i := 0; i < len(toks); i++ {
		tk := toks[i]
		switch tk.Type {
		case token.MappingKeyType:
			if depth > 0 {
				continue
			}
			if r, next, ok := explicitKey(toks, i); ok {
				regions = append(regions, r)
				i = next - 1
			}
		case token.SequenceStartType, token.MappingStartType:
			if depth == 0 {
				if end := flowEnd(toks, i); end > 0 {
					if keyOnItsLine(toks, end) {
						regions = append(regions, flowRegion(toks[propsStart(toks, i)]))
						i = end
						continue
					}
				}
			}
			depth++
		case token.SequenceEndType, token.MappingEndType:
			if depth > 0 {
				depth--
			}
		default:
		}
	}
	return regions
}

// explicitKey returns the region of the key after the "?" at toks[q]
// when that key is a collection, and the index of the first token after
// it.
func explicitKey(toks token.Tokens, q int) (keyRegion, int, bool) {
	at := toks[q].Position
	start := nextNonCommentIndex(toks, q+1)
	first := skipProps(toks, start)
	if first >= len(toks) || toks[first].Position.Line != at.Line {
		return keyRegion{}, 0, false
	}
	switch toks[first].Type {
	case token.SequenceStartType, token.MappingStartType:
		end := flowEnd(toks, first)
		if end < 0 {
			return keyRegion{}, 0, false
		}
		return flowRegion(toks[start]), end + 1, true
	case token.SequenceEntryType:
	default:
		if !keyOnItsLine(toks, first) {
			return keyRegion{}, 0, false
		}
	}
	// A block collection runs on to the first token on a later line that
	// is not indented past the "?".
	end := first + 1
	for end < len(toks) && (toks[end].Position.Line == at.Line || toks[end].Position.Column > at.Column) {
		end++
	}
	last := at.Line
	if end < len(toks) {
		last = toks[end].Position.Line - 1
	} else if end > 0 {
		last = toks[end-1].Position.Line
	}
	s := toks[start].Position
	return keyRegion{line: s.Line, col: s.Column, endLine: last, endCol: 1 << 30}, end, true
}

// skipProps returns the index of the first token at or after i that is
// not a tag, an anchor, or a comment.
func skipProps(toks token.Tokens, i int) int {
	for i < len(toks) && (toks[i].Type == token.TagType || toks[i].Type == token.AnchorType) {
		if toks[i].Type == token.AnchorType {
			// goccy gives an anchor's name a token of its own.
			i++
		}
		i = nextNonCommentIndex(toks, i+1)
	}
	return i
}

// keyOnItsLine reports whether toks[i] is followed by ":" on its line,
// which makes it the first key of a block mapping.
func keyOnItsLine(toks token.Tokens, i int) bool {
	v := nextNonCommentIndex(toks, i+1)
	return v < len(toks) && toks[v].Type == token.MappingValueType && toks[v].Position.Line == toks[i].Position.Line
}

// flowEnd returns the index of the token that closes the flow collection
// opened at toks[open], or -1 when nothing does.
func flowEnd(toks token.Tokens, open int) int {
	depth := 0
	for i := open; i < len(toks); i++ {
		switch toks[i].Type {
		case token.SequenceStartType, token.MappingStartType:
			depth++
		case token.SequenceEndType, token.MappingEndType:
			depth--
			if depth == 0 {
				return i
			}
		default:
		}
	}
	return -1
}

// flowRegion is the region of a flow key whose first token is start.
func flowRegion(start *token.Token) keyRegion {
	return keyRegion{line: start.Position.Line, col: start.Position.Column, flow: true}
}

// propsStart returns the index of the first of the tag and anchor tokens
// on the same line just before toks[i], or i when there are none.
func propsStart(toks token.Tokens, i int) int {
	line := toks[i].Position.Line
	for i > 0 {
		p := toks[i-1]
		switch {
		case p.Position.Line != line:
			return i
		case p.Type == token.TagType:
			i--
		case i > 1 && toks[i-2].Type == token.AnchorType && toks[i-2].Position.Line == line:
			i -= 2
		default:
			return i
		}
	}
	return i
}

// scanFlowEnd finds the end of the flow collection that is the first
// one at or after column col of line, both numbered from 1, skipping
// quoted scalars and comments. It returns the line and the column just
// past the closing bracket.
func scanFlowEnd(runes [][]rune, line, col int) (int, int, bool) {
	var f flowScan
	for l := line; l <= len(runes); l++ {
		start := 0
		if l == line {
			start = col - 1
		}
		if i, ok := f.line(runes[l-1], start); ok {
			return l, i + 2, true
		}
	}
	return 0, 0, false
}

// flowScan tracks the bracket depth and the open quote while scanning a
// flow collection line by line.
type flowScan struct {
	depth int
	quote rune
}

// line scans row from start and returns the index of the bracket that
// closes the collection, when the row holds it.
func (f *flowScan) line(row []rune, start int) (int, bool) {
	for i := start; i < len(row); i++ {
		c := row[i]
		switch {
		case f.quote != 0:
			i = f.quoted(row, i)
		case c == '#' && startsComment(row, i):
			return 0, false
		case f.depth > 0 && (c == '"' || c == '\''):
			f.quote = c
		case c == '[' || c == '{':
			f.depth++
		case f.depth > 0 && (c == ']' || c == '}'):
			f.depth--
			if f.depth == 0 {
				return i, true
			}
		default:
		}
	}
	return 0, false
}

// startsComment reports whether the "#" at row[i] starts a comment,
// which it does at the start of a line or after a space or tab.
func startsComment(row []rune, i int) bool {
	return i == 0 || row[i-1] == ' ' || row[i-1] == '\t'
}

// quoted reads row[i] inside a quoted scalar and returns the index of the
// last character it consumed, closing the quote at its end.
func (f *flowScan) quoted(row []rune, i int) int {
	c := row[i]
	switch {
	case f.quote == '"' && c == '\\':
		return i + 1
	case c != f.quote:
		return i
	case f.quote == '\'' && i+1 < len(row) && row[i+1] == '\'':
		return i + 1
	default:
		f.quote = 0
		return i
	}
}

// nextNonCommentIndex returns the index of the first token at or after i
// that is not a comment.
func nextNonCommentIndex(toks token.Tokens, i int) int {
	for i < len(toks) && toks[i].Type == token.CommentType {
		i++
	}
	return i
}

// restoreComplexKeys puts each parsed key in place of the placeholder
// scalar that stands for it in doc. lineOffset is what the builder added
// to every line in the chunk. A key keeps the comments the chunk gave
// its placeholder.
func restoreComplexKeys(doc *Node, keys []complexKey, lineOffset int) {
	at := make(map[[2]int]*Node, len(keys))
	for _, k := range keys {
		at[[2]int{k.line + lineOffset, k.col}] = k.node
	}
	var walk func(n *Node)
	walk = func(n *Node) {
		for i, c := range n.Content {
			if n.Kind == MappingNode && i%2 == 0 && c.Kind == ScalarNode {
				if key, ok := at[[2]int{c.Line, c.Column}]; ok {
					shiftLines(key, c.Line-1)
					key.HeadComment = joinComment(c.HeadComment, key.HeadComment)
					key.LineComment = joinComment(key.LineComment, c.LineComment)
					key.FootComment = joinComment(key.FootComment, c.FootComment)
					n.Content[i] = key
					continue
				}
			}
			walk(c)
		}
	}
	walk(doc)
}

// shiftLines moves n and everything under it down by lines.
func shiftLines(n *Node, lines int) {
	if n.Line > 0 {
		n.Line += lines
	}
	for _, c := range n.Content {
		shiftLines(c, lines)
	}
}

// joinComment joins two comment texts with a line break, leaving out an
// empty one.
func joinComment(a, b string) string {
	if a == "" || b == "" {
		return a + b
	}
	return a + "\n" + b
}
