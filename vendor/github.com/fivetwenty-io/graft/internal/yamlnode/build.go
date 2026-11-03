package yamlnode

import (
	"bytes"
	"strings"

	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/token"
)

// builder turns goccy ASTs into Nodes. One builder serves a whole stream,
// because yaml.v3 keeps anchors across documents. %TAG handles last only
// for the document that declares them, as in libyaml.
type builder struct {
	opts        ParseOptions
	anchors     map[string]*Node
	open        map[*Node]bool // anchored collections still being built
	tagHandles  map[string]string
	nulledLines map[int]bool

	// retagged holds the core tags Parse blanked in the current chunk so
	// goccy would read it, keyed by each tag's line and column there.
	retagged map[[2]int]string

	// lineOffset is the current chunk's chunk.lineOffset, which every
	// recorded line adds to what goccy reports.
	lineOffset int

	// lastBlock is the most recent block scalar built, and lastBlockStrips
	// records whether its header asked for strip chomping. Parse uses them
	// when the stream ends inside that scalar.
	lastBlock       *Node
	lastBlockStrips bool

	// flow counts the flow collections being built, so a mapping that a
	// flow sequence holds as a single pair is flow style too.
	flow int

	// sourceLines and preparedLines are the stream's lines before and
	// after yamlprep, which quotes some plain scalars so goccy reads
	// them. Both are nil when yamlprep changed nothing.
	sourceLines, preparedLines []string
}

func newBuilder(nulledLines []int) *builder {
	b := &builder{
		anchors:     map[string]*Node{},
		open:        map[*Node]bool{},
		tagHandles:  map[string]string{},
		nulledLines: map[int]bool{},
	}
	for _, l := range nulledLines {
		b.nulledLines[l] = true
	}
	return b
}

// document builds the one document a chunk holds. goccy reports %YAML and
// %TAG directives as documents of their own, so we read their handles and
// skip them. A chunk's directives travel with it, so the handles start
// over at every chunk.
func (b *builder) document(file *ast.File, c chunk) (*Node, error) {
	clear(b.tagHandles)
	b.lineOffset = c.lineOffset()
	var body ast.Node
	found := false
	for _, d := range file.Docs {
		if dir, ok := d.Body.(*ast.DirectiveNode); ok {
			b.readDirective(dir)
			continue
		}
		if !found {
			body, found = d.Body, true
		}
	}
	root, err := b.build(body)
	if err != nil {
		return nil, err
	}
	if root.Line == 0 {
		root.Line, root.Column = c.startLine, 1
	}
	return &Node{Kind: DocumentNode, Line: root.Line, Column: 1, Content: []*Node{root}}, nil
}

func (b *builder) readDirective(dir *ast.DirectiveNode) {
	if dir.Name == nil || dir.Name.GetToken().Value != "TAG" || len(dir.Values) != 2 {
		return
	}
	b.tagHandles[dir.Values[0].GetToken().Value] = dir.Values[1].GetToken().Value
}

// nodeProps is a node with its tag and anchor peeled off. tag is the
// tag it resolves to, bare says it had the non-specific tag "!", and
// start is the first token of its properties, or nil when it has none.
type nodeProps struct {
	node        ast.Node
	tag, anchor string
	bare        bool
	start       *token.Token
}

// peel takes the tags and anchors off n, which wrap the node they apply
// to, and checks their names.
func (b *builder) peel(n ast.Node) (nodeProps, error) {
	var p nodeProps
	for {
		switch x := n.(type) {
		case *ast.TagNode:
			p.start = firstToken(p.start, x.Start)
			t, err := b.tag(x.Start)
			if err != nil {
				return p, err
			}
			if t != "" {
				p.tag = t
			} else {
				p.bare = true
			}
			n = x.Value
			continue
		case *ast.AnchorNode:
			p.start = firstToken(p.start, x.Start)
			if err := PropertyNameError(x.Start, x.Name.GetToken(), b.lineOffset, b.scannerLine); err != nil {
				return p, err
			}
			p.anchor = x.Name.GetToken().Value
			n = x.Value
			continue
		}
		p.node = n
		return p, nil
	}
}

// firstToken returns whichever of a and b comes first in the text, or
// the other when one is nil. A tag and an anchor usually wrap their node
// in the order they are written, but an anchor that the parser read
// before a scalar tag, as anchorBeforeScalarTag arranges, wraps the tag
// though it follows it.
func firstToken(a, b *token.Token) *token.Token {
	switch {
	case a == nil || a.Position == nil:
		return b
	case b == nil || b.Position == nil:
		return a
	case b.Position.Line < a.Position.Line ||
		b.Position.Line == a.Position.Line && b.Position.Column < a.Position.Column:
		return b
	}
	return a
}

// build converts one goccy node. Tags and anchors wrap the node they
// apply to, so we peel them off first.
func (b *builder) build(n ast.Node) (*Node, error) {
	p, err := b.peel(n)
	if err != nil {
		return nil, err
	}
	n, explicitTag, anchor, bare, props := p.node, p.tag, p.anchor, p.bare, p.start

	if alias, ok := n.(*ast.AliasNode); ok {
		if err := PropertyNameError(alias.Start, alias.Value.GetToken(), b.lineOffset, b.scannerLine); err != nil {
			return nil, err
		}
		name := alias.Value.GetToken().Value
		target := b.anchors[name]
		line := alias.Start.Position.Line + b.lineOffset
		if target == nil {
			return nil, &ParseError{SourceLine: line, SourceColumn: alias.Value.GetToken().Position.Column, Message: "unknown anchor '" + name + "' referenced"}
		}
		if b.open[target] {
			// The alias sits inside the collection it names, so the tree
			// would contain itself. yaml.v3 builds that tree and fails
			// only when it decodes it, but nothing in graft may walk a
			// cycle, so the load fails here with the decoder's message.
			return nil, &ParseError{SourceLine: line, Message: containsItself(name)}
		}
		out := &Node{Kind: AliasNode, Value: name, Alias: target}
		b.at(out, alias)
		return out, nil
	}

	out, err := b.buildValue(n, anchor)
	if err != nil {
		return nil, err
	}
	delete(b.open, out)
	b.startAt(out, props)
	if explicitTag != "" {
		out.Tag = explicitTag
		out.Style |= StyleTagged
	}
	out.BareTag = bare && explicitTag == ""
	if anchor != "" && out.Anchor == "" {
		out.Anchor = anchor
		b.anchors[anchor] = out
	}
	return out, nil
}

func (b *builder) buildValue(n ast.Node, anchor string) (*Node, error) {
	switch x := n.(type) {
	case nil:
		return &Node{Kind: ScalarNode, Tag: TagNull}, nil
	case *ast.MappingNode:
		out := b.collection(MappingNode, TagMap, anchor, x)
		defer b.enter(out)()
		for _, mv := range x.Values {
			if err := b.pair(out, mv); err != nil {
				return nil, err
			}
		}
		return out, nil
	case *ast.MappingValueNode:
		out := b.collection(MappingNode, TagMap, anchor, x)
		return out, b.pair(out, x)
	case *ast.SequenceNode:
		out := b.collection(SequenceNode, TagSeq, anchor, x)
		defer b.enter(out)()
		for _, v := range x.Values {
			item, err := b.build(v)
			if err != nil {
				return nil, err
			}
			out.Content = append(out.Content, item)
		}
		return out, nil
	case ast.ScalarNode:
		return b.buildScalar(x), nil
	default:
		line := n.GetToken().Position.Line + b.lineOffset
		return nil, &ParseError{Line: line, SourceLine: line, Message: "unsupported YAML node " + n.Type().String()}
	}
}

// buildScalar converts a goccy scalar. A quoted or block scalar is always
// "!!str", a plain scalar gets yaml.v3's resolved tag for its source text,
// and goccy's own typing is ignored.
func (b *builder) buildScalar(n ast.ScalarNode) *Node {
	switch x := n.(type) {
	case *ast.LiteralNode:
		out := b.scalar(x, TagStr, literalValue(x))
		out.Style = StyleLiteral
		if x.Start.Type == token.FoldedType {
			out.Style = StyleFolded
		}
		b.lastBlock, b.lastBlockStrips = out, strings.Contains(x.Start.Value, "-")
		return out
	case *ast.MergeKeyNode:
		return b.scalar(x, TagMerge, "<<")
	case *ast.StringNode:
		if tk := x.GetToken(); tk.Type == token.SingleQuoteType || tk.Type == token.DoubleQuoteType {
			out := b.scalar(x, TagStr, x.Value)
			if b.quotedInSource(tk) {
				out.Style = StyleSingle
				if tk.Type == token.DoubleQuoteType {
					out.Style = StyleDouble
				}
			}
			return out
		}
		return b.scalar(x, ResolvePlainTag(x.Value), x.Value)
	case *ast.NullNode:
		tk := x.GetToken()
		v := tk.Value
		if tk.Type == token.ImplicitNullType || (v == "~" && b.nulledLines[tk.Position.Line+b.lineOffset]) {
			v = ""
		}
		return b.scalar(x, TagNull, v)
	default:
		v := n.GetToken().Value
		return b.scalar(n, ResolvePlainTag(v), v)
	}
}

// collection starts a mapping or sequence and registers its anchor before
// its children are built, as yaml.v3's parser does. An anchored
// collection stays open until build finishes it, so an alias to it from
// inside can be caught.
func (b *builder) collection(kind Kind, tag, anchor string, n ast.Node) *Node {
	out := &Node{Kind: kind, Tag: tag, Anchor: anchor}
	if b.flow > 0 || isFlow(n) {
		out.Style = StyleFlow
	}
	if anchor != "" {
		b.anchors[anchor] = out
		b.open[out] = true
	}
	b.at(out, n)
	return out
}

// enter notes that the children of out are being built, and returns the
// function that notes they are done.
func (b *builder) enter(out *Node) func() {
	if out.Style&StyleFlow == 0 {
		return func() {}
	}
	b.flow++
	return func() { b.flow-- }
}

// keepSourceLines records the stream's lines before and after yamlprep
// when yamlprep changed them, for quotedInSource. text is prepared as
// goccy reads it.
func (b *builder) keepSourceLines(source, prepared []byte, text string) {
	if bytes.Equal(prepared, source) {
		return
	}
	b.sourceLines, b.preparedLines = strings.Split(string(source), "\n"), strings.Split(text, "\n")
}

// quotedInSource reports whether the quote that opens tk was in the
// source rather than added by yamlprep. yamlprep inserts quotes and may
// respell the blanks between tokens, so matching the prepared line
// against the source line from the left, with a run of blanks matching
// a run of blanks, shows whether the quote has a source character of
// its own. goccy counts columns in runes.
func (b *builder) quotedInSource(tk *token.Token) bool {
	if b.sourceLines == nil {
		return true
	}
	i := tk.Position.Line + b.lineOffset - 1
	if i < 0 || i >= len(b.preparedLines) || i >= len(b.sourceLines) || b.preparedLines[i] == b.sourceLines[i] {
		return true
	}
	prepared, source := []rune(b.preparedLines[i]), []rune(b.sourceLines[i])
	col := tk.Position.Column - 1
	if col < 0 || col >= len(prepared) {
		return true
	}
	j := alignTo(prepared, source, col)
	return j < len(source) && source[j] == prepared[col]
}

// alignTo returns the index in source that lines up with prepared[col],
// matching from the left and taking a run of blanks for a run of blanks.
func alignTo(prepared, source []rune, col int) int {
	j := 0
	for p := 0; p < col; {
		if j < len(source) && isBlank(prepared[p]) && isBlank(source[j]) {
			for p < col && isBlank(prepared[p]) {
				p++
			}
			for j < len(source) && isBlank(source[j]) {
				j++
			}
			continue
		}
		if j < len(source) && prepared[p] == source[j] {
			j++
		}
		p++
	}
	return j
}

func isBlank(r rune) bool { return r == ' ' || r == '\t' }

func (b *builder) pair(out *Node, mv *ast.MappingValueNode) error {
	var keyNode ast.Node = mv.Key
	if mk, ok := keyNode.(*ast.MappingKeyNode); ok {
		keyNode = mk.Value
	}
	k, err := b.build(keyNode)
	if err != nil {
		return err
	}
	v, err := b.build(mv.Value)
	if err != nil {
		return err
	}
	out.Content = append(out.Content, k, v)
	return nil
}

func (b *builder) scalar(n ast.Node, tag, value string) *Node {
	out := &Node{Kind: ScalarNode, Tag: tag, Value: value}
	b.at(out, n)
	return out
}

// startAt moves out's position to where yaml.v3 starts the node, which
// is its first tag or anchor when it has one, and its first key for a
// block mapping, which goccy places at the key's ":".
func (b *builder) startAt(out *Node, props *token.Token) {
	switch {
	case props != nil && props.Position != nil:
		out.Line, out.Column = props.Position.Line+b.lineOffset, props.Position.Column
	case out.Kind == MappingNode && out.Style&StyleFlow == 0 && len(out.Content) > 0:
		out.Line, out.Column = out.Content[0].Line, out.Content[0].Column
	default:
	}
}

// at records the source position of n on out, as both its position and
// its token's.
func (b *builder) at(out *Node, n ast.Node) {
	if tk := n.GetToken(); tk != nil {
		out.Line, out.Column = tk.Position.Line+b.lineOffset, tk.Position.Column
		out.TokenLine, out.TokenColumn = out.Line, out.Column
	}
}

// tag checks the tag token tk the way yaml.v3 scans it, and then
// expands it.
func (b *builder) tag(tk *token.Token) (string, error) {
	if len(b.retagged) > 0 && tk.Position != nil {
		if original, ok := b.retagged[[2]int{tk.Position.Line, tk.Position.Column}]; ok {
			tk = tk.Clone()
			tk.Value, tk.Origin = original, strings.Replace(tk.Origin, tk.Value, original, 1)
		}
	}
	if err := TagError(tk, b.lineOffset, b.scannerLine, DiffTags); err != nil {
		return "", err
	}
	return b.expandTag(tk)
}

// scannerLine is the line yaml.v3 reports for a scanner error on line
// of the current chunk, numbered from 1. It counts the stream's lines
// from 1 too, and leaves out the first line.
func (b *builder) scannerLine(line int) int {
	if line += b.lineOffset; line <= 1 {
		return 0
	}
	return line
}

// expandTag applies yaml.v3's tag rules to an explicit tag: a bare "!" is
// ignored, a verbatim "!<...>" tag is unwrapped, a %TAG handle is
// expanded, and the result is shortened with ShortTag. A named handle
// such as "!e!" that no %TAG directive declared is libyaml's "found
// undefined tag handle" error, which yaml.v3 reports one line above the
// node, and without a line on the first line.
func (b *builder) expandTag(tk *token.Token) (string, error) {
	tag := tk.Value
	switch {
	case tag == "!":
		return "", nil
	case strings.HasPrefix(tag, "!<") && strings.HasSuffix(tag, ">"):
		return ShortTag(tag[2 : len(tag)-1]), nil
	case strings.HasPrefix(tag, "!!"):
		if prefix, ok := b.tagHandles["!!"]; ok {
			return ShortTag(prefix + tag[2:]), nil
		}
		return tag, nil
	}
	end := strings.Index(tag[1:], "!")
	if end < 0 {
		if prefix, ok := b.tagHandles["!"]; ok {
			return ShortTag(prefix + tag[1:]), nil
		}
		return tag, nil
	}
	handle := tag[:end+2]
	prefix, ok := b.tagHandles[handle]
	if !ok {
		line := tk.Position.Line + b.lineOffset
		return "", &ParseError{Line: line - 1, SourceLine: line, Message: "found undefined tag handle"}
	}
	return ShortTag(prefix + tag[end+2:]), nil
}

// literalValue returns a block scalar's value and restores the trailing
// spaces goccy drops from a "-" (strip) chomped scalar. goccy trims every
// space from the end of the value, but libyaml keeps whatever lies past
// the content indentation on the scalar's last content line, and a
// whitespace-only line longer than the indentation is a content line.
func literalValue(x *ast.LiteralNode) string {
	value := x.Value.Value
	if !strings.Contains(x.Start.Value, "-") {
		return value
	}
	lines := strings.Split(x.Value.GetToken().Origin, "\n")
	indent := blockIndent(x.Start, lines)
	for i := len(lines) - 1; i >= 0; i-- {
		if len(lines[i]) <= indent {
			continue
		}
		content := lines[i][indent:]
		if value == "" {
			// goccy turns a value of one line break into "", so the line
			// breaks before the only content line are gone as well.
			return strings.Repeat("\n", i) + content
		}
		return value + content[len(strings.TrimRight(content, " ")):]
	}
	return value
}

// indentIndicator returns the indentation indicator of a block scalar
// header such as "|2-", or 0 when the header has none.
func indentIndicator(header string) int {
	for _, c := range header {
		if c >= '1' && c <= '9' {
			return int(c - '0')
		}
	}
	return 0
}

// blockIndent returns the content indentation libyaml gives the block
// scalar whose header token is hdr and whose body lines are lines. An
// indentation indicator counts from the enclosing block collection's
// indentation. Without one, the indentation is that of the first
// non-empty line, or of a longer whitespace-only line before it, and at
// least one more than the enclosing collection's.
func blockIndent(hdr *token.Token, lines []string) int {
	parent := blockParentIndent(hdr)
	if n := indentIndicator(hdr.Value); n > 0 {
		return max(parent, 0) + n
	}
	indent := max(parent+1, 1)
	for _, line := range lines {
		text := strings.TrimLeft(line, " ")
		indent = max(indent, len(line)-len(text))
		if text != "" {
			break
		}
	}
	return indent
}

// blockParentIndent returns libyaml's indentation for the block
// collection that holds the block scalar whose header token is hdr, as a
// 0-based column. For a sequence item it is the column of the "-", for a
// mapping value the column where the key's line starts after any "-"
// entries, and at the top level of a document it is -1. Tags and anchors
// between the "-" or ":" and the header are skipped.
func blockParentIndent(hdr *token.Token) int {
	t := hdr.Prev
	for t != nil {
		if t.Type == token.TagType || t.Type == token.AnchorType {
			t = t.Prev
			continue
		}
		if t.Type == token.StringType && t.Prev != nil && t.Prev.Type == token.AnchorType {
			t = t.Prev.Prev // an anchor's name
			continue
		}
		break
	}
	return entryIndent(t)
}

// entryIndent returns libyaml's indentation, as a 0-based column, for the
// block collection whose entry t opens: the column of a "-", or the
// column where a ":" key's line starts after any "-" entries. Any other
// token, or none, gives -1, the top level of a document.
func entryIndent(t *token.Token) int {
	switch {
	case t == nil:
		return -1
	case t.Type == token.SequenceEntryType:
		return t.Position.Column - 1
	case t.Type == token.MappingValueType:
		start := t
		for p := t.Prev; p != nil && p.Position.Line == t.Position.Line && p.Type != token.SequenceEntryType; p = p.Prev {
			start = p
		}
		return start.Position.Column - 1
	}
	return -1
}
