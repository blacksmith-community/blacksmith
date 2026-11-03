//
// Copyright (c) 2011-2019 Canonical Ltd
// Copyright (c) 2006-2010 Kirill Simonov
//
// Permission is hereby granted, free of charge, to any person obtaining a copy of
// this software and associated documentation files (the "Software"), to deal in
// the Software without restriction, including without limitation the rights to
// use, copy, modify, merge, publish, distribute, sublicense, and/or sell copies
// of the Software, and to permit persons to whom the Software is furnished to do
// so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in all
// copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
// SOFTWARE.
//
// Copyright (c) 2011-2019 Canonical Ltd
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
// Derived from libyaml's comment scanning as ported in go.yaml.in/yaml/v3
// v3.0.4 (scannerc.go yaml_parser_scan_comments, parserc.go, and decode.go
// parser.mapping) and rewritten for graft to work on goccy positions.

package yamlnode

import (
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/token"

	"github.com/fivetwenty-io/graft/internal/yamlprep"
)

// The comment placer reproduces where yaml.v3 attaches comments, for the
// slots neat prints and one more: a mapping key's head comment (#KH) and
// foot comment (#KF), a scalar value's line comment (#VL) and foot comment
// (#VF), and a scalar sequence item's head comment (#VH). It works from
// lexer comment tokens and node positions, because goccy's own comment
// placement differs from yaml.v3's and its ParseComments mode rejects
// valid input. Slots are keyed by a path of mapping pair and sequence
// indexes such as "d0{3}[1]{0}#KH", so duplicate keys never share a slot,
// and applySlots writes them onto the built tree. The segmentation follows
// yaml_parser_scan_comments (scannerc.go:2887-3040), the BLOCK-END
// placement in yaml_parser_unroll_indent (scannerc.go:1029-1080), the
// comment unfolding in parserc.go:80-108 and the document head split at
// parserc.go:288-301, and the foot moves in decode.go's parser.mapping.

// anc is one open level. It records the column of its key or dash and
// the slot where a foot comment at that level lands. key marks a mapping
// key's level, and block marks a level that libyaml closes with a
// BLOCK-END token, which every level but an indentless sequence is.
type anc struct {
	col   int
	foot  string
	key   bool
	block bool
}

// item is one key, scalar value, or sequence dash in document order.
type item struct {
	kind      byte // 'K' key, 'S' scalar value, 'E' sequence dash
	line, col int
	end       int
	head      string // slot for a head comment
	lineSlot  string
	valueNext bool // a key whose value starts on a later line
	null      bool // an empty value: an implicit null, or "~" on a rewritten bare-dash line
	plain     bool // a plain or block scalar, whose scanner reads past the line break after it
	ancs      []anc
}

// flowEntry is one entry of a flow collection. line and col locate the
// token a line comment after the entry follows.
type flowEntry struct {
	line, col int
	head      string
	lineSlot  string
	foot      string
}

// flowColl is a flow collection and its direct entries, with the
// positions of its opening and closing brackets.
type flowColl struct {
	startLine, startCol int
	endLine, endCol     int
	entries             []flowEntry
}

type cmt struct {
	line, col int
	text      string
	prev      *token.Token // the last token before the comment that is not a comment
	flow      bool         // the comment sits inside a flow collection
}

func endLine(tk *token.Token) int {
	o := strings.Trim(tk.Origin, " \t\r\n")
	return tk.Position.Line + strings.Count(o, "\n")
}

func unwrap(n ast.Node) ast.Node {
	for {
		switch x := n.(type) {
		case *ast.AnchorNode:
			n = x.Value
		case *ast.TagNode:
			n = x.Value
		default:
			return n
		}
	}
}

func isFlow(n ast.Node) bool {
	switch x := n.(type) {
	case *ast.MappingNode:
		return x.IsFlowStyle
	case *ast.SequenceNode:
		return x.IsFlowStyle
	default:
		return false
	}
}

func isCollection(n ast.Node) bool {
	switch n.(type) {
	case *ast.MappingNode, *ast.MappingValueNode, *ast.SequenceNode:
		return true
	default:
		return false
	}
}

// blockScalarEnd returns the last line of a literal or folded scalar that
// holds content. goccy's body token runs on through the blank lines and
// the next line's indentation, and its line number is unreliable, but its
// text always starts on the line after the header.
func blockScalarEnd(x *ast.LiteralNode) int {
	lines := strings.Split(x.Value.GetToken().Origin, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			return x.Start.Position.Line + 1 + i
		}
	}
	return x.Start.Position.Line
}

// walker collects the items and flow collections of one document.
// nulled holds stream lines, and offset turns a chunk line into one.
type walker struct {
	items  []item
	flows  []*flowColl
	nulled map[int]bool
	offset int
}

// empty reports whether n is an empty value: an implicit null, or the
// "~" that stands for a bare sequence dash.
func (w *walker) empty(n ast.Node) bool {
	tk := n.GetToken()
	return tk != nil && (tk.Type == token.ImplicitNullType || (tk.Type == token.NullType && tk.Value == "~" && w.nulled[tk.Position.Line+w.offset]))
}

func (w *walker) scalarItem(n ast.Node, slot string, ancs []anc) item {
	tk := n.GetToken()
	it := item{kind: 'S', line: tk.Position.Line, col: tk.Position.Column, lineSlot: slot, ancs: ancs, end: endLine(tk), null: w.empty(n)}
	if tk.Type == token.ImplicitNullType {
		it.lineSlot = ""
	}
	it.plain = tk.Type != token.SingleQuoteType && tk.Type != token.DoubleQuoteType && !isFlow(n)
	switch x := n.(type) {
	case *ast.LiteralNode:
		it.end = blockScalarEnd(x)
	case *ast.MappingNode:
		if x.IsFlowStyle {
			it.end = x.End.Position.Line
		}
	case *ast.SequenceNode:
		if x.IsFlowStyle {
			it.end = x.End.Position.Line
		}
	}
	return it
}

// rootScalarLine is the line comment slot of a document whose root is a
// scalar.
const rootScalarLine = "d0#VL"

// root walks a document's root node. A scalar or flow collection at the
// root is an item of its own, at libyaml's indentation of -1 outside
// every block collection, and yaml.v3 keeps a scalar's head, line, and
// foot comments on the scalar. An empty root holds no comments.
func (w *walker) root(n ast.Node) {
	u := unwrap(n)
	switch {
	case isFlow(u):
		it := w.scalarItem(u, "d0#CL", []anc{{col: 0}})
		it.head = "d0#CH"
		w.items = append(w.items, it)
		w.flow(u, "d0")
	case isCollection(u):
		w.node(n, "d0", nil)
	default:
		if tk := u.GetToken(); tk == nil || tk.Type == token.ImplicitNullType {
			return
		}
		it := w.scalarItem(u, rootScalarLine, []anc{{col: 0, foot: "d0#VF"}})
		it.head = "d0#VH"
		w.items = append(w.items, it)
	}
}

func (w *walker) node(n ast.Node, p string, ancs []anc) {
	switch x := unwrap(n).(type) {
	case *ast.MappingNode:
		if !x.IsFlowStyle {
			w.mapping(x.Values, p, ancs)
		}
	case *ast.MappingValueNode:
		w.mapping([]*ast.MappingValueNode{x}, p, ancs)
	case *ast.SequenceNode:
		if !x.IsFlowStyle {
			w.sequence(x, p, ancs)
		}
	}
}

func (w *walker) mapping(values []*ast.MappingValueNode, p string, ancs []anc) {
	for i, mv := range values {
		kt := mv.Key.GetToken()
		kp := fmt.Sprintf("%s{%d}", p, i)
		kancs := append(append([]anc{}, ancs...), anc{kt.Position.Column, kp + "#KF", true, true})
		v := unwrap(mv.Value)
		valueNext := v.GetToken() != nil && v.GetToken().Position.Line > kt.Position.Line
		if _, ok := v.(*ast.LiteralNode); ok {
			valueNext = false
		}
		implicitNull := v.GetToken() != nil && v.GetToken().Type == token.ImplicitNullType
		if implicitNull {
			valueNext = true
		}
		w.items = append(w.items, item{kind: 'K', line: kt.Position.Line, col: kt.Position.Column, end: kt.Position.Line, head: kp + "#KH", lineSlot: kp + "#KL", valueNext: valueNext, ancs: kancs})
		switch {
		case isFlow(v):
			w.items = append(w.items, w.scalarItem(v, kp+"#CL", kancs))
			w.flow(v, kp)
		case isCollection(v):
			w.node(v, kp, kancs)
		case !implicitNull:
			w.items = append(w.items, w.scalarItem(v, kp+"#VL", kancs))
		}
	}
}

// sequence walks a block sequence. A foot comment at the level of a dash
// whose item is a block collection lands on the sequence in yaml.v3, and
// decode.go moves a sequence's foot to the key that holds it, so that
// level's foot slot is the parent key's. A scalar or flow item keeps the
// foot itself, and neat prints it only on a scalar. A bare dash's empty
// value is no event that takes comments, so its level has the parent
// key's foot slot as well.
func (w *walker) sequence(x *ast.SequenceNode, p string, ancs []anc) {
	seqFoot, indentless := "", false
	if n := len(ancs); n > 0 && ancs[n-1].key {
		seqFoot = ancs[n-1].foot
		indentless = len(x.Entries) > 0 && x.Entries[0].Start.Position.Column == ancs[n-1].col
	}
	for i, v := range x.Values {
		ip := fmt.Sprintf("%s[%d]", p, i)
		dash := x.Entries[i].Start
		uv := unwrap(v)
		scalar := !isCollection(uv)
		foot, head := seqFoot, ""
		if (scalar || isFlow(uv)) && !w.empty(uv) {
			foot = ip + "#VF"
		}
		if scalar {
			head = ip + "#VH"
		} else {
			head = ip + "#CH"
		}
		eancs := append(append([]anc{}, ancs...), anc{dash.Position.Column, foot, false, !indentless})
		w.items = append(w.items, item{kind: 'E', line: dash.Position.Line, col: dash.Position.Column, end: dash.Position.Line, head: head, ancs: eancs})
		e := len(w.items) - 1
		switch {
		case isFlow(uv):
			w.items = append(w.items, w.scalarItem(uv, ip+"#CL", eancs))
			w.flow(uv, ip)
		case !scalar:
			w.node(uv, ip, eancs)
			w.stem(e, v != uv)
		default:
			it := w.scalarItem(uv, ip+"#VL", eancs)
			it.head = head
			w.items = append(w.items, it)
		}
	}
}

// stem gives the dash item at e the head slot of its mapping's first key
// when an anchor or tag sits between the dash and a mapping that starts
// on a later line. yaml.v3 otherwise keeps a comment above the dash as
// the mapping's own head comment, which neat never prints, because
// yaml_parser_split_stem_comment only sees a mapping that follows the
// dash directly.
func (w *walker) stem(e int, decorated bool) {
	if !decorated || e+1 >= len(w.items) {
		return
	}
	if first := w.items[e+1]; first.kind == 'K' && first.line > w.items[e].line {
		w.items[e].head = first.head
	}
}

// flow records a flow collection's direct entries so comments inside it
// can find them. A nested collection is recorded on its own.
func (w *walker) flow(n ast.Node, p string) {
	switch x := n.(type) {
	case *ast.SequenceNode:
		f := &flowColl{x.Start.Position.Line, x.Start.Position.Column, x.End.Position.Line, x.End.Position.Column, nil}
		w.flows = append(w.flows, f)
		for i, v := range x.Values {
			ip := fmt.Sprintf("%s[%d]", p, i)
			uv := unwrap(v)
			tk := uv.GetToken()
			if pair, ok := uv.(*ast.MappingNode); ok && !pair.IsFlowStyle {
				// A "key: value" entry in a flow sequence is a mapping
				// of its own, with a single pair.
				for j, mv := range pair.Values {
					w.flowPair(f, mv, fmt.Sprintf("%s{%d}", ip, j))
				}
				continue
			}
			if isFlow(uv) {
				f.entries = append(f.entries, flowEntry{line: tk.Position.Line, col: tk.Position.Column})
				w.flow(uv, ip)
				continue
			}
			f.entries = append(f.entries, flowEntry{tk.Position.Line, tk.Position.Column, ip + "#VH", ip + "#VL", ip + "#VF"})
		}
	case *ast.MappingNode:
		f := &flowColl{x.Start.Position.Line, x.Start.Position.Column, x.End.Position.Line, x.End.Position.Column, nil}
		w.flows = append(w.flows, f)
		for i, mv := range x.Values {
			w.flowPair(f, mv, fmt.Sprintf("%s{%d}", p, i))
		}
	}
}

// flowPair records a key and its value as entries of the flow collection
// f. A value that is a flow collection is recorded on its own, and the
// key keeps the foot comment that follows the value.
func (w *walker) flowPair(f *flowColl, mv *ast.MappingValueNode, kp string) {
	kt, uv := mv.Key.GetToken(), unwrap(mv.Value)
	f.entries = append(f.entries, flowEntry{line: kt.Position.Line, col: kt.Position.Column, head: kp + "#KH"})
	vt := uv.GetToken()
	if isFlow(uv) {
		f.entries = append(f.entries, flowEntry{line: vt.Position.Line, col: vt.Position.Column, foot: kp + "#KF"})
		w.flow(uv, kp)
		return
	}
	f.entries = append(f.entries, flowEntry{line: vt.Position.Line, col: vt.Position.Column, lineSlot: kp + "#VL", foot: kp + "#KF"})
}

// commentSlots computes the comment slots of the one document a chunk
// holds, and the comments of the document itself. Everything here works
// in the chunk's own lines, which start at 1. nulled holds the stream
// lines whose bare "-" became "- ~". A nil file stands for a chunk that
// holds no document, whose comments can only reach the one before it.
func commentSlots(file *ast.File, c chunk, nulled map[int]bool) (map[string]string, docSlots) {
	out := map[string]string{}
	w := &walker{nulled: nulled, offset: c.lineOffset()}
	explicit := false
	if file != nil {
		for _, doc := range file.Docs {
			if doc.Body == nil {
				continue
			}
			if _, ok := doc.Body.(*ast.DirectiveNode); ok {
				continue
			}
			w.root(doc.Body)
			explicit = doc.Start != nil
			break
		}
	}
	return out, placeComments(w, c, explicit, out)
}

// docSlots holds a document's own comments. head is its head comment.
// feet and heads are the foot and head comments still waiting for a
// token when the document ends, and carry holds the foot comments right
// after the token that ended the document before it, which that
// document's end takes.
type docSlots struct {
	head        string
	feet, heads []string
	carry       []string
}

// setDocumentComments fills each document's head and foot comments. As
// in yaml.v3, a document's end takes the foot comments waiting for it and
// those its successor carries back, or failing those, the head comments.
// ends has one entry per document, and one more when comments follow the
// last document's "...".
func setDocumentComments(docs []*Node, ends []docSlots) {
	for i, d := range docs {
		d.HeadComment = ends[i].head
		feet := ends[i].feet
		if i+1 < len(ends) {
			feet = append(append([]string{}, feet...), ends[i+1].carry...)
		}
		if len(feet) == 0 {
			feet = ends[i].heads
		}
		d.FootComment = strings.Join(feet, "\n")
	}
}

// lexed is what commentSlots needs from the lexer: the comments, the
// least column of content on each line, and the "---" lines.
type lexed struct {
	cmts       []cmt
	contentCol map[int]int
	headerLine map[int]bool
	lines      []string
}

func lex(text string) lexed {
	l := lexed{contentCol: map[int]int{}, headerLine: map[int]bool{}}
	var prev *token.Token
	depth := 0
	for _, t := range yamlprep.Tokenize(text) {
		switch t.Type {
		case token.CommentType:
			l.cmts = append(l.cmts, cmt{t.Position.Line, t.Position.Column, "#" + t.Value, prev, depth > 0})
			continue
		case token.DocumentHeaderType:
			l.headerLine[t.Position.Line] = true
		case token.SequenceStartType, token.MappingStartType:
			depth++
		case token.SequenceEndType, token.MappingEndType:
			depth--
		default:
			// Every other token is content and counts toward contentCol below.
		}
		// A block scalar's body holds no comments, and goccy numbers its
		// lines unreliably, so it does not count as content.
		blockBody := prev != nil && (prev.Type == token.LiteralType || prev.Type == token.FoldedType)
		if t.Type != token.DocumentHeaderType && !blockBody {
			for ln := t.Position.Line; ln <= endLine(t); ln++ {
				if col, ok := l.contentCol[ln]; !ok || t.Position.Column < col {
					l.contentCol[ln] = t.Position.Column
				}
			}
		}
		prev = t
	}
	return l
}

func placeComments(w *walker, c chunk, explicit bool, out map[string]string) docSlots {
	lines := strings.Split(strings.TrimSuffix(c.text, "\n"), "\n")
	l := lex(c.text)
	l.lines = lines
	own, dashLines, inline := lineComments(w, l, out)
	headFoot, ownFeet, d := "", map[string]bool{}, &docSlots{}
	if (c.index == 0 || c.afterEnd) && len(w.items) > 0 {
		headFoot = w.items[0].ancs[len(w.items[0].ancs)-1].foot
	}
	for _, g := range buildGaps(w.items, len(lines), dashLines, c.marker) {
		if !gapHasComment(g, own) && (g.prev != nil || len(c.endComments) == 0) {
			continue
		}
		s := newGapScanner(g, own, inline, lines, out, c)
		s.headFoot, s.ownFeet, s.doc = headFoot, ownFeet, d
		if c.unterminated && g.next == nil {
			s.eofCol = utf8.RuneCountInString(lines[len(lines)-1])
		}
		s.scan()
		if g.prev != nil {
			s.resolve()
		}
		s.finish(explicit)
	}
	return *d
}

func appendSlot(out map[string]string, slot, text string) {
	if slot == "" || text == "" {
		return
	}
	if out[slot] != "" {
		out[slot] += "\n" + text
	} else {
		out[slot] = text
	}
}

// lineComments places every comment that shares a line with earlier
// content, and every comment inside a flow collection. A comment that
// follows a sequence dash with no value of its own is a head comment for
// whatever follows, as in yaml.v3, so it is returned with the comments
// that sit on lines of their own, and its line is marked in dashLines.
// inline marks every line that ends in a line comment.
func lineComments(w *walker, l lexed, out map[string]string) (own map[int]cmt, dashLines, inline map[int]bool) {
	own, dashLines, inline = map[int]cmt{}, map[int]bool{}, map[int]bool{}
	runs := l.flowRuns(w.items, w.offset)
	for i, c := range l.cmts {
		if l.decorated(c) {
			// A comment after a tag or an anchor is a line comment of
			// the first scalar after it, as in yaml.v3.
			appendSlot(out, afterDecoration(w.items, c.line), c.text)
			inline[c.line] = inline[c.line] || !l.headerLine[c.line]
			continue
		}
		isInline := l.inline(w.items, c)
		if c.flow {
			flowComment(w.flows, c, isInline, runs[i], out)
			continue
		}
		if !isInline {
			own[c.line] = c
			continue
		}
		inline[c.line] = true
		best := lastItemOnLine(w.items, c.line)
		switch {
		case best < 0:
		case afterBareDash(w.items, best):
			own[c.line] = c
			dashLines[c.line] = true
		case w.items[best].kind == 'S' || w.items[best].kind == 'K':
			appendSlot(out, w.items[best].lineSlot, c.text)
		}
	}
	return own, dashLines, inline
}

// inline reports whether c follows content on its line. On a "---"
// line, a comment inside a flow collection that starts there and a
// comment after a root scalar are line comments, and a comment after
// anything else is a head comment for what follows.
func (l lexed) inline(items []item, c cmt) bool {
	cc, hasContent := l.contentCol[c.line]
	if !hasContent || cc >= c.col {
		return false
	}
	if !l.headerLine[c.line] {
		return true
	}
	if c.flow && docStartHasContent(l.lines[c.line-1]) {
		return true
	}
	i := lastItemOnLine(items, c.line)
	return i >= 0 && items[i].lineSlot == rootScalarLine
}

// decorated reports whether c follows a tag or an anchor that ends its
// line, outside a flow collection.
func (l lexed) decorated(c cmt) bool {
	if c.flow || c.prev == nil {
		return false
	}
	// goccy lexes an anchor as an Anchor token for the "&" and a String
	// token for its name.
	if c.prev.Type != token.TagType && (c.prev.Prev == nil || c.prev.Prev.Type != token.AnchorType) {
		return false
	}
	cc, ok := l.contentCol[c.line]
	return ok && cc < c.col
}

// afterDecoration returns the line comment slot that yaml.v3 gives a
// comment after a tag or an anchor on line. It is the slot of the first
// scalar after the line, past any sequence dashes. A mapping key holds
// the comment instead when one comes first, and neat never prints a
// key's line comment, so no slot is returned.
func afterDecoration(items []item, line int) string {
	for _, it := range items {
		if it.line <= line || it.kind == 'E' {
			continue
		}
		return it.lineSlot
	}
	return ""
}

// afterBareDash reports whether items[i], the last item on its line, is
// a sequence dash with no value, or the empty value of one.
func afterBareDash(items []item, i int) bool {
	it := items[i]
	if it.kind == 'E' {
		return true
	}
	return it.kind == 'S' && it.null && i > 0 && items[i-1].kind == 'E' && items[i-1].line == it.line
}

// lastItemOnLine returns the index of the last item that ends on line or
// starts there. A scalar that starts on the line and ends later is a
// block scalar, and a comment after its header is its line comment.
func lastItemOnLine(items []item, line int) int {
	best := -1
	for i := range items {
		if items[i].end == line || items[i].line == line {
			best = i
		}
	}
	return best
}

// blankAfter reports whether the line after line is blank.
func (l lexed) blankAfter(line int) bool {
	return line < len(l.lines) && strings.TrimSpace(l.lines[line]) == ""
}

// flowRun describes the block of comments on consecutive lines of their
// own that a comment inside a flow collection belongs to.
//
// yaml.v3 reads such a block in yaml_parser_scan_comments, and a blank
// line ends it. The block is a foot comment of the entry before it when
// no blank line came before it, it starts on the line after that entry,
// and the token before it is not a ":" that still waits for its value.
// The scanner only has a foot line once it stands past the first line
// of the stream, so a block that follows the first line is never a
// foot. Any other block that a blank line ends is a head comment of the
// entry after it, and keeps the blank line as a trailing newline.
type flowRun struct {
	last  bool // the comment is the last one of its block
	blank bool // a blank line follows the block
	foot  bool // the block is a foot comment of the entry before it
}

// flowRuns returns the flowRun of every comment in l.cmts that sits on a
// line of its own inside a flow collection. offset is the number of
// stream lines before the first line of the chunk.
func (l lexed) flowRuns(items []item, offset int) []flowRun {
	runs := make([]flowRun, len(l.cmts))
	own := func(i int) bool { return l.cmts[i].flow && !l.inline(items, l.cmts[i]) }
	for i := 0; i < len(l.cmts); i++ {
		if !own(i) {
			continue
		}
		j := i
		for j+1 < len(l.cmts) && own(j+1) && l.cmts[j+1].line == l.cmts[j].line+1 {
			j++
		}
		blank := l.blankAfter(l.cmts[j].line)
		foot := blank && flowFoot(l.cmts[i], offset)
		for k := i; k <= j; k++ {
			runs[k] = flowRun{k == j, blank, foot}
		}
		i = j
	}
	return runs
}

// flowFoot reports whether the block that starts with c, and that a
// blank line ends, is a foot comment of the entry before it.
func flowFoot(c cmt, offset int) bool {
	tk := c.prev
	if tk == nil || c.line != endLine(tk)+1 || endLine(tk)+offset < 2 {
		return false
	}
	if tk.Type == token.CollectEntryType {
		tk = tk.Prev
	}
	return tk != nil && tk.Type != token.MappingValueType
}

// flowComment places a comment inside a flow collection. A comment after
// an entry, or after the "," that follows it, is that entry's line
// comment, and so is a comment after the ":" that follows a key, which
// belongs to the key's value. A comment on a line of its own is the head
// comment of the next entry, or the foot comment of the last one before
// the closing bracket. run says how a block of such comments is placed
// when a blank line follows it.
func flowComment(flows []*flowColl, c cmt, inline bool, run flowRun, out map[string]string) {
	if inline {
		flowLineComment(flows, c, out)
		return
	}
	f := innermostFlow(flows, c)
	if f == nil {
		return
	}
	for i, e := range f.entries {
		if before(c.line, c.col, e.line, e.col) {
			switch {
			case run.foot && i > 0:
				appendSlot(out, f.entries[i-1].foot, c.text)
			case run.foot:
				// Nothing before the first entry holds a foot comment.
			default:
				appendSlot(out, e.head, c.text)
				if run.last && run.blank && e.head != "" {
					out[e.head] += "\n"
				}
			}
			return
		}
		if i == len(f.entries)-1 {
			appendSlot(out, e.foot, c.text)
		}
	}
}

// flowLineComment places a comment that follows content on its line
// inside a flow collection.
func flowLineComment(flows []*flowColl, c cmt, out map[string]string) {
	prev := c.prev
	if prev != nil && prev.Type == token.CollectEntryType {
		prev = prev.Prev
	}
	for prev != nil && prev.Type == token.CommentType {
		prev = prev.Prev
	}
	if prev == nil {
		return
	}
	afterKey := prev.Type == token.MappingValueType && prev.Prev != nil
	if afterKey {
		prev = prev.Prev
	}
	for _, f := range flows {
		for i, e := range f.entries {
			if e.line != prev.Position.Line || e.col != prev.Position.Column {
				continue
			}
			if afterKey {
				if i+1 < len(f.entries) {
					appendSlot(out, f.entries[i+1].lineSlot, c.text)
				}
			} else {
				appendSlot(out, e.lineSlot, c.text)
			}
			return
		}
	}
}

func before(l1, c1, l2, c2 int) bool {
	return l1 < l2 || (l1 == l2 && c1 < c2)
}

// innermostFlow returns the last recorded flow collection that encloses
// the comment. Collections are recorded outer first.
func innermostFlow(flows []*flowColl, c cmt) *flowColl {
	var best *flowColl
	for _, f := range flows {
		if before(f.startLine, f.startCol, c.line, c.col) && before(c.line, c.col, f.endLine, f.endCol) {
			best = f
		}
	}
	return best
}

// gap is the run of lines between the end of one item and the start of
// the next. dash marks a gap that starts on the line of a bare sequence
// dash, with the comment after it. marker marks the last gap of a chunk
// that a "---" or "..." ends, rather than the end of the stream.
type gap struct {
	prev, next *item
	after      *item  // the item after next
	rest       []item // the items from next on
	from, to   int
	dash       bool
	marker     bool
}

func buildGaps(items []item, lastLine int, dashLines map[int]bool, marker bool) []gap {
	if len(items) == 0 {
		return []gap{{from: 1, to: lastLine, marker: marker}}
	}
	gaps := []gap{{prev: nil, next: &items[0], rest: items, from: 1, to: items[0].line - 1}}
	if len(items) > 1 {
		gaps[0].after = &items[1]
	}
	for i := 0; i+1 < len(items); i++ {
		if items[i+1].line > items[i].end {
			g := gap{prev: &items[i], next: &items[i+1], rest: items[i+1:], from: items[i].end + 1, to: items[i+1].line - 1}
			if i+2 < len(items) {
				g.after = &items[i+2]
			}
			if dashLines[items[i].end] {
				g.from, g.dash = items[i].end, true
			}
			gaps = append(gaps, g)
		}
	}
	lastEnd := 0
	var lastItem *item
	for i := range items {
		if items[i].end >= lastEnd {
			lastEnd, lastItem = items[i].end, &items[i]
		}
	}
	g := gap{prev: lastItem, from: lastEnd + 1, to: lastLine, marker: marker}
	if dashLines[lastEnd] {
		g.from, g.dash = lastEnd, true
	}
	return append(gaps, g)
}

func gapHasComment(g gap, own map[int]cmt) bool {
	for l := g.from; l <= g.to; l++ {
		if _, ok := own[l]; ok {
			return true
		}
	}
	return false
}

// pos is a position in the chunk. A line break sits at column eol, past
// anything else on its line.
type pos struct{ line, col int }

const eol = 1 << 20

func (p pos) before(q pos) bool { return before(p.line, p.col, q.line, q.col) }

// foot is a foot comment waiting for the token it attaches to. libyaml
// attaches a comment to the first token at or after its token mark, so
// mark is where the comment starts when it is dedented, where the scan
// stood after the previous foot, or nothing, which means the token before
// the gap.
type foot struct {
	text  string
	start pos
	mark  pos
	prior bool
}

// gapScanner runs yaml_parser_scan_comments' segmentation over one gap,
// collecting foot comments as it goes and leaving the final block as the
// next item's head comment. resolve then places the feet the way the
// BLOCK-END tokens of yaml_parser_unroll_indent and decode.go's moves of
// foot comments do.
type gapScanner struct {
	g           gap
	own         map[int]cmt
	lines       []string
	out         map[string]string
	nextIndent  int
	isValue     bool
	footLine    int
	firstEmpty  bool
	recentEmpty bool
	text        []string
	startLine   int
	startCol    int

	feet   []foot
	starts []pos // the start of every comment block, feet and head alike
	mark   pos   // where the scan stood after the last foot
	marked bool  // whether a foot has moved mark off the token before the gap

	// headFoot is where a foot comment right after a document's "---"
	// lands, preHead holds the head comments seen before that "---", and
	// afterHeader records that the scan has passed it.
	headFoot    string
	preHead     []string
	afterHeader bool

	// ownFeet holds the key foot slots filled from the key's own event.
	// decode.go's parser.mapping then leaves the value's foot on the value.
	ownFeet map[string]bool

	// eofCol is the 0-based column libyaml stands at when the stream ends
	// after the gap. It is 0 after a final line break, and the length of
	// the last line when the stream has none.
	eofCol int

	// streamStart records that the chunk's first line is the stream's
	// first line, where libyaml has no foot line.
	streamStart bool

	// doc collects the document's own comments. carryBefore and
	// carryAfter say whether a foot comment in the first gap, before or
	// after the document's "---", belongs to the document before it.
	doc         *docSlots
	carryBefore bool
	carryAfter  bool

	// endAt is the line of the token that ends the document, once the
	// scan reaches it, and lastFoot is the line where the last foot
	// comment ended.
	endAt    int
	lastFoot int
}

// newGapScanner prepares the scan of one gap. inline holds the lines that
// end in a line comment. libyaml has no foot line when its scan starts on
// the stream's first line, which happens there unless the token before
// the gap is a plain or block scalar with no line comment, whose scanner
// reads on past the line break.
//
// When the chunk does not start the stream, the scan of its first gap
// starts as it would after the blank lines that stand in for the lines
// before the chunk. After a "...", it starts on the "..." line's foot
// line instead, and a foot comment there belongs to the document the
// "..." ends, as does one right after a "---" that ends a document.
// When that foot line is the chunk's own "---" line, marker starts the
// scan afresh there, so only comments above the "---" can be such a
// foot.
func newGapScanner(g gap, own map[int]cmt, inline map[int]bool, lines []string, out map[string]string, c chunk) *gapScanner {
	s := &gapScanner{g: g, own: own, lines: lines, out: out, footLine: -1, firstEmpty: true, streamStart: c.lineOffset() == 0}
	if g.prev == nil && c.index > 0 {
		s.carryBefore, s.carryAfter = c.afterEnd, !c.afterEnd
	}
	s.seedEndComments(c)
	switch {
	case g.prev == nil && s.carryBefore:
		s.footLine = 1
	case g.prev == nil && !s.streamStart:
		s.firstEmpty, s.recentEmpty = false, true
	}
	if g.prev != nil {
		s.nextIndent = g.prev.ancs[len(g.prev.ancs)-1].col - 1
		s.isValue = g.prev.kind == 'K' && g.prev.valueNext
		firstLine := s.streamStart && g.prev.end == 1 && (!g.prev.plain || inline[1])
		if !g.dash && !firstLine {
			s.footLine = g.from
		}
	}
	return s
}

// seedEndComments starts the first gap's comment block with the comments
// on the "..." line that ended the document before the chunk. They sit on
// a line before the chunk, so a foot line never closes the block.
func (s *gapScanner) seedEndComments(c chunk) {
	if s.g.prev == nil && len(c.endComments) > 0 {
		s.text, s.startLine, s.startCol = slices.Clone(c.endComments), 0, 1
	}
}

func (s *gapScanner) scan() {
	for l := s.g.from; l <= s.g.to+1; l++ {
		if l > s.g.to {
			s.end(l)
			return
		}
		c, isCmt := s.own[l]
		switch {
		case isCmt && s.isHeader(l):
			s.marker(l)
			if docStartHasContent(s.lines[l-1]) {
				appendSlot(s.out, s.nodeLineSlot(), c.text)
			} else {
				s.comment(l, c)
			}
		case isCmt:
			s.comment(l, c)
		case strings.TrimSpace(s.lines[l-1]) == "":
			s.blankLine(l)
		default:
			s.marker(l) // a "---", a "...", or a directive
		}
	}
}

// isHeader reports whether line l of the document's first gap is its
// "---" line. A comment on that line follows the "---" token, so the
// scan passes the header before it reads the comment. The scan then
// asks docStartHasContent whether a tag or an anchor sits between the
// "---" and the comment, because such a comment belongs to the node.
func (s *gapScanner) isHeader(l int) bool {
	return s.g.prev == nil && strings.HasPrefix(s.lines[l-1], "---")
}

// docStartHasContent reports whether any token other than a comment
// follows the "---" at the start of line, as in "--- !!map # c" or
// "--- &a # c". yaml.v3 gives a comment after such a token to the node
// as a line comment, instead of making it a head comment of the
// document's first node. The gap scan uses this predicate today, and
// lexed.inline uses it too, to tell a comment inside a flow collection
// that starts on the "---" line from a head comment.
func docStartHasContent(line string) bool {
	rest := strings.Trim(strings.TrimPrefix(line, "---"), " \t\r")
	return rest != "" && rest[0] != '#'
}

// nodeLineSlot returns the line comment slot that yaml.v3 uses for a
// comment after a tag or an anchor on the "---" line. A scalar root, or
// the scalar in the first dash of a sequence root, holds the comment as
// its line comment. A mapping root gives it to the first key, whose line
// comment the report never prints, so no slot is returned.
func (s *gapScanner) nodeLineSlot() string {
	next := s.g.next
	switch {
	case next == nil:
		return ""
	case next.kind == 'S' || next.kind == 'K':
		return next.lineSlot
	case next.kind == 'E' && s.g.after != nil && s.g.after.kind == 'S' && s.g.after.line == next.line:
		return s.g.after.lineSlot
	}
	return ""
}

// end handles the line after the gap: the next item, the end of the
// stream, which closes comments like a blank line, or a "---" or "..."
// that ends the chunk.
func (s *gapScanner) end(l int) {
	switch {
	case s.g.next != nil:
		if len(s.text) > 0 && s.g.next.col-1 < s.nextIndent && s.g.next.col != s.startCol {
			s.emitFoot(s.tokenMark(), pos{l, s.g.next.col})
		}
	case s.endAt > 0:
		// A "..." inside the gap has ended the document already.
	case s.g.marker:
		s.marker(l)
	default:
		s.blankLine(l)
		s.endAt = l
	}
}

func (s *gapScanner) comment(l int, c cmt) {
	if len(s.text) > 0 && c.col-1 < s.nextIndent && c.col != s.startCol {
		s.emitFoot(s.tokenMark(), pos{l, c.col})
	}
	if len(s.text) == 0 {
		s.startLine, s.startCol = l, c.col
		s.starts = append(s.starts, pos{l, c.col})
	}
	s.recentEmpty = false
	s.text = append(s.text, c.text)
}

// marker handles a line at column 1 that is not a comment or an item. In
// the document's first gap, the comments so far become head comments of
// the first node, and a "---" starts a fresh scan whose foot line is the
// line below it, except on the first line of the stream, where libyaml
// has no foot line. At the end of a document, the comments that are not
// a foot by then stay pending for the document's own end.
func (s *gapScanner) marker(l int) {
	if len(s.text) > 0 && 0 < s.nextIndent && s.startCol != 1 {
		s.emitFoot(s.tokenMark(), pos{l, 1})
	}
	if s.g.prev != nil || l > len(s.lines) || strings.HasPrefix(s.lines[l-1], "...") {
		s.endAt = l
		return
	}
	if len(s.text) > 0 {
		s.preHead = append(s.preHead, strings.Join(s.text, "\n"))
		s.text = nil
	}
	if strings.HasPrefix(s.lines[l-1], "---") {
		s.afterHeader, s.firstEmpty, s.recentEmpty, s.footLine = true, true, false, -1
		if l > 1 || !s.streamStart {
			s.footLine = l + 1
		}
	}
}

func (s *gapScanner) blankLine(l int) {
	if !s.recentEmpty {
		dedented := s.startCol <= s.nextIndent
		switch {
		case s.firstEmpty && dedented:
			s.emitFoot(foot{start: pos{s.startLine, s.startCol}, mark: pos{s.startLine, s.startCol}}, pos{l, eol})
		case s.firstEmpty && s.startLine == s.footLine && !s.isValue:
			s.emitFoot(s.tokenMark(), pos{l, eol})
		case len(s.text) > 0 && l <= s.g.to:
			s.text = append(s.text, "")
		}
	}
	s.firstEmpty = false
	s.recentEmpty = true
}

// tokenMark returns a foot that carries libyaml's token_mark. That mark
// is the token before the gap until a foot is emitted, and the scan
// position after that.
func (s *gapScanner) tokenMark() foot {
	return foot{start: pos{s.startLine, s.startCol}, mark: s.mark, prior: !s.marked}
}

// emitFoot closes the current comment block as a foot comment and moves
// the scan position to at.
func (s *gapScanner) emitFoot(f foot, at pos) {
	if len(s.text) == 0 {
		return
	}
	f.text = strings.Join(s.text, "\n")
	s.text = nil
	s.mark, s.marked, s.lastFoot = at, true, at.line
	if s.g.prev == nil {
		s.startFoot(f.text)
		return
	}
	s.feet = append(s.feet, f)
}

// startFoot places a foot comment from a document's first gap. Before
// the document's "---" it can only follow a "...", and after it, it
// lands on the first node that takes comments, or on the document's own
// end when there is no node. Either way it belongs to the document
// before when its end has not taken its comments yet.
func (s *gapScanner) startFoot(text string) {
	switch {
	case s.afterHeader && s.carryAfter, !s.afterHeader && s.carryBefore:
		s.doc.carry = append(s.doc.carry, text)
	case !s.afterHeader:
	case s.g.next == nil:
		s.doc.feet = append(s.doc.feet, text)
	default:
		s.ownFoot(s.headFoot, text)
	}
}

// blockEnd is a BLOCK-END token. It records where libyaml put the token
// and the slot a foot comment lands in when the event it ends takes
// comments. A mapping's end takes them, and decode.go moves them to its
// last key, while a block sequence's end leaves them for the next event
// that takes comments.
type blockEnd struct {
	at    pos
	foot  string
	takes bool
}

// blockEnds returns the BLOCK-END tokens between the gap's two items,
// innermost first. yaml_parser_unroll_indent moves each one back to the
// earliest comment in the gap that starts at its level's column, and a
// level with no such comment ends where the level inside it did. At the
// end of the stream or before "---" or "...", libyaml first closes the
// levels indented past the column it stands at, and then closes the rest
// in a second pass that starts after every comment. That pass moves back
// to the comments only when the last of them reaches the end, which a
// foot comment followed by more than one blank line does not.
func (s *gapScanner) blockEnds() []blockEnd {
	a := s.g.prev.ancs
	var out []blockEnd
	at, search, final := pos{s.g.from, 0}, true, false
	for i := len(a) - 1; i >= 0; i-- {
		if !a[i].block {
			continue
		}
		if s.g.next != nil && a[i].col <= s.g.next.col {
			break
		}
		if s.g.next == nil && a[i].col-1 <= s.eofCol && !final {
			at, search, final = pos{eol, eol}, s.reachesEnd(), true
		}
		if st, ok := s.firstStart(a[i].col); ok && search {
			at = st
		}
		out = append(out, blockEnd{at, a[i].foot, a[i].key})
	}
	return out
}

// firstStart returns the start of the earliest comment block in the gap
// that starts at col.
func (s *gapScanner) firstStart(col int) (pos, bool) {
	for _, st := range s.starts {
		if st.col == col {
			return st, true
		}
	}
	return pos{}, false
}

// reachesEnd reports whether the gap's last comment ends right before the
// token that ends the document. A head comment always does, and a foot
// comment does when it ends at most one line before that token.
func (s *gapScanner) reachesEnd() bool {
	return len(s.text) > 0 || s.lastFoot >= s.endAt-1
}

// resolve places the gap's foot comments. A foot marked with the token
// before the gap belongs to that token, unless the token is a bare
// sequence dash, which holds no comments. Any other foot is unfolded at
// the first BLOCK-END at or after its mark and lands on the first
// mapping that ends from there on, or failing that on the next item.
//
// decode.go assigns a mapping's foot, and a key's foot handed to the key
// before it, over whatever that key held, so those feet replace the slot.
// A value's foot moves to its key only when the key has none of its own.
func (s *gapScanner) resolve() {
	if len(s.feet) == 0 {
		return
	}
	ends := s.blockEnds()
	replace := map[string][]string{}
	q := 0
	for _, f := range s.feet {
		if s.heldByPrior(f) {
			s.priorFoot(f.text)
			continue
		}
		for !f.prior && q < len(ends) && ends[q].at.before(f.mark) {
			q++
		}
		k := q
		for k < len(ends) && !ends[k].takes {
			k++
		}
		if k < len(ends) {
			replace[ends[k].foot] = append(replace[ends[k].foot], f.text)
			continue
		}
		switch slot, handed := s.nextFoot(); {
		case handed:
			replace[slot] = append(replace[slot], f.text)
		case s.g.next == nil:
			s.doc.feet = append(s.doc.feet, f.text)
		default:
			s.ownFoot(slot, f.text)
		}
	}
	for slot, texts := range replace {
		if slot != "" {
			s.out[slot] = strings.Join(texts, "\n")
		}
	}
}

// heldByPrior reports whether f stays with the item before the gap. A
// sequence dash holds no comments, so a foot moves on when the item
// before the gap is a dash whose value starts on a later line, or when
// the gap starts at a comment on a bare dash's own line.
func (s *gapScanner) heldByPrior(f foot) bool {
	return f.prior && !s.g.dash && s.g.prev.kind != 'E'
}

// priorFoot places a foot that belongs to the scalar before the gap. A
// mapping value hands it to its key unless the key has a foot of its own.
func (s *gapScanner) priorFoot(text string) {
	a := s.g.prev.ancs
	slot := a[len(a)-1].foot
	if s.g.prev.kind == 'S' && a[len(a)-1].key && s.ownFeet[slot] {
		slot = strings.TrimSuffix(slot, "#KF") + "#VF"
	}
	appendSlot(s.out, slot, text)
}

// ownFoot places a foot that a key's or a scalar item's own event takes.
func (s *gapScanner) ownFoot(slot, text string) {
	appendSlot(s.out, slot, text)
	if slot != "" {
		s.ownFeet[slot] = true
	}
}

// nextFoot returns the slot of a foot comment that reaches the next item,
// and whether decode.go hands it on to an earlier key. A key hands it to
// the key before it in the same mapping, as parser.mapping does, and keeps
// it when it has no such key. A sequence dash passes it to the first node
// after it that takes comments, past any nested dashes: a scalar item, or
// the first key of a mapping item.
func (s *gapScanner) nextFoot() (string, bool) {
	next, a := s.g.next, s.g.prev.ancs
	switch {
	case next == nil:
		return "", false
	case next.kind == 'E':
		for _, it := range s.g.rest {
			if it.kind != 'E' {
				return it.ancs[len(it.ancs)-1].foot, false
			}
		}
		return "", false
	}
	for i := len(a) - 1; i >= 0; i-- {
		if a[i].key && a[i].col == next.col {
			return a[i].foot, true
		}
	}
	return next.ancs[len(next.ancs)-1].foot, false
}

// finish hands the comment block the scan ended on to the next item as
// its head comment. After the last item, or in a document with none, the
// comments still pending stay with the document's end.
func (s *gapScanner) finish(explicit bool) {
	t := strings.Join(s.text, "\n")
	if s.g.next == nil {
		if t != "" {
			s.preHead = append(s.preHead, t)
		}
		s.doc.heads = append(s.doc.heads, s.preHead...)
		return
	}
	if s.g.prev == nil && !explicit {
		s.doc.head, t = splitDocumentHead(t)
	}
	if t != "" {
		s.preHead = append(s.preHead, t)
	}
	appendSlot(s.out, s.g.next.head, strings.Join(s.preHead, "\n"))
}

// splitDocumentHead splits the comments before an implicit document's
// first node at the last blank line, as yaml_parser_parse_document_start
// does. The part above goes to the document and the rest to the node.
// A blank line right before the node gives every comment to the document.
func splitDocumentHead(t string) (head, rest string) {
	for i := len(t) - 1; i > 0; i-- {
		switch {
		case t[i] != '\n':
		case i == len(t)-1:
			return t[:i], t[i+1:]
		case t[i-1] == '\n':
			return t[:i-1], t[i+1:]
		}
	}
	return "", t
}

// applySlots writes the computed slots onto a built document, walking it
// with the same path keys the walker used. A scalar keeps its comments in
// the #V slots and a collection in the #C slots, and a mapping key keeps
// its line comment in #KL.
func applySlots(n *Node, path string, slots map[string]string) {
	switch n.Kind {
	case DocumentNode:
		for _, c := range n.Content {
			applyItemSlots(c, path, slots)
			applySlots(c, path, slots)
		}
	case MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			kp := fmt.Sprintf("%s{%d}", path, i/2)
			k.HeadComment = slots[kp+"#KH"]
			k.LineComment = slots[kp+"#KL"]
			k.FootComment = slots[kp+"#KF"]
			if v.Kind == ScalarNode {
				v.LineComment = slots[kp+"#VL"]
				v.FootComment = slots[kp+"#VF"]
			} else if v.Kind != AliasNode {
				v.LineComment = slots[kp+"#CL"]
			}
			applySlots(v, kp, slots)
		}
	case SequenceNode:
		for i, c := range n.Content {
			ip := fmt.Sprintf("%s[%d]", path, i)
			applyItemSlots(c, ip, slots)
			applySlots(c, ip, slots)
		}
	default:
		// Scalars and aliases hold no further slots.
	}
}

// applyItemSlots writes the slots of a document's root or a sequence
// item, which hold its head, line, and foot comments.
func applyItemSlots(c *Node, path string, slots map[string]string) {
	switch c.Kind {
	case ScalarNode:
		c.HeadComment = slots[path+"#VH"]
		c.LineComment = slots[path+"#VL"]
		c.FootComment = slots[path+"#VF"]
	case MappingNode, SequenceNode:
		c.HeadComment = slots[path+"#CH"]
		c.LineComment = slots[path+"#CL"]
	default:
		// An alias holds no comments of its own here.
	}
}
