package yaml

import (
	"bytes"
	"strings"
	"unicode/utf8"

	"github.com/fivetwenty-io/graft/internal/yamlcodec"
	"github.com/fivetwenty-io/graft/internal/yamlnode"
)

// Node is one node of a parsed YAML document, laid out like yaml.v3's
// Node so that code walking a v3 tree reads this one unchanged.
//
// Unmarshal into a *Node fills it as a DocumentNode whose single child
// is the document's root. An alias stays an AliasNode whose Alias is the
// very *Node that holds the anchor, so a walker can compare pointers to
// guard against revisiting a node. A << merge key stays in its mapping as
// a key tagged !!merge, with its value beside it, and nothing is merged
// in. Unmarshal refuses an alias inside the collection its anchor names,
// so no tree it returns has a cycle.
//
// A Node also remembers, out of sight, how its scalar was written, such
// as quoted or as a block, and the DecodeOptions it was read with, so
// that Decode reads it as Unmarshal read the text.
type Node struct {
	// Kind says whether the node is a document, sequence, mapping, scalar,
	// or alias.
	Kind Kind
	// Tag is the node's resolved short tag, such as "!!str" or "!!map",
	// and "!!merge" for a "<<" key. Tags resolve as yaml.v3 resolves
	// them, so a plain yes is "!!str" whatever DecodeOptions says. A
	// walker that wants YAML 1.1 booleans calls Decode or reads Value.
	Tag string
	// Value is a scalar's text, or an alias's anchor name.
	Value string
	// Content holds a document's root, a sequence's items, or a mapping's
	// keys and values in alternating order.
	Content []*Node
	// Alias points at the node an alias refers to, which is the same
	// *Node that appears where the anchor was defined.
	Alias *Node
	// Line is the node's 1-based line in the input.
	Line int
	// Column is the node's 1-based column in the input.
	Column int

	// style is how the node was written, such as quoted, as a block, or
	// with an explicit tag, which Decode keeps, and it is zero for a Node
	// a caller built.
	style yamlnode.Style
	// read is set on a Node that Unmarshal read, so Decode can tell a
	// plain scalar, whose tag follows YAML 1.2, from one a caller built.
	read bool
	// readTag is the Tag Unmarshal read, so Decode can tell a tag the
	// input wrote, or resolved, from one the caller set since.
	readTag string
	// tokenLine and tokenColumn are where the parser's own token for the
	// node sat, which is where Unmarshal reports an error about it, and
	// Decode reports its errors there too.
	tokenLine, tokenColumn int
	// readValue is the Value Unmarshal read, so Decode can tell whether
	// the caller has changed it since.
	readValue string
	// text is the document the node was read from, shared by the whole
	// tree, which Decode parses again when it needs to know how
	// Unmarshal's own parser read a scalar.
	text *readText
	// bareTag is set on a node Unmarshal read with the non-specific tag
	// "!", which Tag does not show, but which makes a plain scalar a
	// string.
	bareTag bool
	// opts are the options the tree was read with, which Decode uses.
	opts DecodeOptions
}

// Kind says what a Node is, with yaml.v3's type and values.
type Kind uint32

const (
	// DocumentNode is the root of a document, with its single child in Content.
	DocumentNode Kind = 1 << iota
	// SequenceNode is a sequence, with its items in Content.
	SequenceNode
	// MappingNode is a mapping, with its keys and values in Content.
	MappingNode
	// ScalarNode is a scalar, with its text in Value.
	ScalarNode
	// AliasNode is an alias, with its target in Alias.
	AliasNode
)

// publicKinds maps each internal kind, numbered 1 through 5 in yaml.v3's
// order, to the public kind with yaml.v3's value.
var publicKinds = [...]Kind{
	yamlnode.DocumentNode: DocumentNode,
	yamlnode.SequenceNode: SequenceNode,
	yamlnode.MappingNode:  MappingNode,
	yamlnode.ScalarNode:   ScalarNode,
	yamlnode.AliasNode:    AliasNode,
}

// decodeNode is the route for a *Node target. It reads the first document
// of data with graft's YAML reader and copies it into out. Empty input,
// or input that holds only comments, leaves out unchanged.
func decodeNode(data []byte, out *Node, opts DecodeOptions) error {
	doc, err := yamlcodec.FirstMergeDocument(data)
	if err != nil {
		return translate(err)
	}
	docs, err := yamlnode.Parse(doc)
	if err != nil {
		return translate(err)
	}
	if len(docs) == 0 {
		return nil
	}
	lines := bytes.Split(doc, []byte("\n"))
	c := &converter{seen: make(map[*yamlnode.Node]*Node), opts: opts, lines: lines, text: &readText{doc: doc}}
	var root Node
	c.fill(&root, docs[0])
	if opts.OnDuplicateKey != nil {
		collapseDuplicates(&root, opts.OnDuplicateKey)
	}
	if line := documentLine(data); line > 0 {
		root.Line, root.Column = line, 1
	}
	*out = root
	return nil
}

// documentLine returns the line of the first document's "---" marker or
// first directive in data, where yaml.v3 places a document that has one,
// and 0 for a document that starts with its content. It reads data
// itself, since the document Unmarshal parses has its directives blanked.
func documentLine(data []byte) int {
	data = bytes.ReplaceAll(yamlnode.TrimBOM(data), []byte("\r\n"), []byte("\n"))
	data = bytes.ReplaceAll(data, []byte("\r"), []byte("\n"))
	for i, line := range bytes.Split(data, []byte("\n")) {
		text := bytes.TrimLeft(line, " \t")
		switch {
		case len(text) == 0 || text[0] == '#':
			continue
		case line[0] == '%':
			return i + 1
		case bytes.HasPrefix(line, []byte("---")) && (len(line) == 3 || line[3] == ' ' || line[3] == '\t'):
			return i + 1
		}
		return 0
	}
	return 0
}

// v3Column returns the column yaml.v3 gives the character at column col
// of line, as goccy's scanner counts it. goccy skips a tab between tokens
// without counting it, where yaml.v3 counts it as one column, so the two
// differ by the tabs before col that sit outside quotes and comments.
func v3Column(line []byte, col int) int {
	g, c := 1, 1
	var s quoteScan
	for i := 0; i < len(line); {
		r, size := utf8.DecodeRune(line[i:])
		i += size
		if s.quote == 0 && r == '\t' {
			c++
			s.prev = r
			continue
		}
		if g == col {
			return c
		}
		if s.comment(r) {
			return col + c - g
		}
		if skip := s.step(r, line[i:]); skip > 0 {
			i += skip
			g++
			c++
		}
		g++
		c++
		s.prev = r
	}
	return col + c - g
}

// quoteScan follows a scan of one line through its quoted scalars.
// quote is the quote the scan is inside, or 0, and prev is the character
// before the current one, or 0 at the start of the line. flowMaps counts
// the flow mappings open on the line, keyed reports that a ":" has ended
// a key since the last token, and delim reports that prev is such a ":".
type quoteScan struct {
	quote, prev  rune
	flowMaps     int
	keyed, delim bool
}

// comment reports whether r, outside quotes, starts a comment.
func (s *quoteScan) comment(r rune) bool {
	return s.quote == 0 && r == '#' && (s.prev == 0 || s.prev == ' ' || s.prev == '\t')
}

// step moves the scan past r, which rest follows. It returns the number
// of bytes of rest that go with r as one more character, which is the
// character after a backslash in double quotes, or the second quote of
// a doubled single quote, and 0 otherwise. A quote opens a quoted scalar
// after a blank, the start of the line, "[", "{", ",", or a ":" that
// ends a key.
func (s *quoteScan) step(r rune, rest []byte) int {
	delim := s.delim
	s.delim = false
	switch {
	case s.quote == 0 && s.opens(r, delim):
		s.quote = r
	case s.quote == '"' && r == '\\' && len(rest) > 0:
		_, size := utf8.DecodeRune(rest)
		return size
	case s.quote == '"' && r == '"':
		s.quote, s.keyed = 0, false
	case s.quote == '\'' && r == '\'':
		if len(rest) > 0 && rest[0] == '\'' {
			return 1
		}
		s.quote, s.keyed = 0, false
	case s.quote == 0:
		s.delim = s.flow(r, len(rest) > 0 && rest[0] == '/')
	}
	return 0
}

// opens reports whether r, outside quotes, opens a quoted scalar. delim
// reports that the character before r is a ":" that ends a key.
func (s *quoteScan) opens(r rune, delim bool) bool {
	return (r == '"' || r == '\'') && (delim || strings.ContainsRune("\x00 \t[{,", s.prev))
}

// flow follows goccy's scanner over r, outside quotes, through the flow
// mappings of the line, and reports whether r is a ":" that ends a key
// with no blank after it. In a flow mapping, or anything inside one,
// goccy reads a ":" as ending a key unless one has ended since the last
// token or a "/" follows it, as slash does. A quote right after it then
// opens a quoted scalar, as in {"k":"v"} and {k:"v"}. Outside a flow
// mapping a ":" ends a key only before a blank, which opens a quote too.
func (s *quoteScan) flow(r rune, slash bool) bool {
	switch r {
	case '{':
		s.flowMaps++
		s.keyed = false
	case '}':
		s.flowMaps = max(s.flowMaps-1, 0)
		s.keyed = false
	case '[', ']', ',':
		s.keyed = false
	case ':':
		if s.flowMaps > 0 && !s.keyed && !slash {
			s.keyed = true
			return true
		}
	}
	return false
}

// converter copies an internal tree into public Nodes. seen maps each
// internal node to its copy, so every alias points at the copy of its
// anchor, and a node is copied once however many times it is reached.
// Each node enters seen before its children are copied, so the copy ends
// on any finite tree, even one with a cycle.
type converter struct {
	seen  map[*yamlnode.Node]*Node
	opts  DecodeOptions
	lines [][]byte
	text  *readText
}

// node returns the copy of in, making it on first sight.
func (c *converter) node(in *yamlnode.Node) *Node {
	if in == nil {
		return nil
	}
	if out, ok := c.seen[in]; ok {
		return out
	}
	out := &Node{}
	c.fill(out, in)
	return out
}

// fill makes out the copy of in.
func (c *converter) fill(out *Node, in *yamlnode.Node) {
	c.seen[in] = out
	out.Tag, out.Value, out.readTag = in.Tag, in.Value, in.Tag
	out.Line, out.Column = in.Line, in.Column
	if in.Line > 0 && in.Line <= len(c.lines) && in.Column > 0 {
		out.Column = v3Column(c.lines[in.Line-1], in.Column)
	}
	out.tokenLine, out.tokenColumn = in.TokenLine, in.TokenColumn
	out.style, out.read, out.opts, out.bareTag = in.Style, true, c.opts, in.BareTag
	out.readValue, out.text = in.Value, c.text
	if int(in.Kind) < len(publicKinds) {
		out.Kind = publicKinds[in.Kind]
	}
	out.Alias = c.node(in.Alias)
	if len(in.Content) > 0 {
		out.Content = make([]*Node, len(in.Content))
		for i, child := range in.Content {
			out.Content[i] = c.node(child)
		}
	}
}

// Decode decodes n, and everything under it, into v, with the routes,
// the rules, and the errors of Unmarshal. A plain yes decodes into a bool
// as true, struct tags apply, map[interface{}]interface{} is refused, and
// every error is a *DecodeError. A tree read with UnmarshalWith decodes
// with the options it was read with, and any other Node decodes with the
// default ones, so it reads YAML 1.1 booleans.
//
// A tree Unmarshal returned decodes to the value Unmarshal gives for the
// same text, with a few known exceptions. The tree reader follows yaml.v3
// and Unmarshal follows goccy, so the two can differ on a node with two
// tags, on a tag written on an alias, and on a tag with nothing after it
// at the end of a document that ends with no line break.
//
// Decode reads the tree as it is now. A Node that the caller changed after
// Unmarshal, or built by hand, decodes what it holds at that moment, and the
// value is resolved as the same text would be in Unmarshal. A scalar keeps
// the style it was read in, which is plain for one a caller built, so a
// plain yes is true even after a caller sets it. A tag that Unmarshal read
// counts only where the input wrote it. A Tag that the caller set counts as
// that tag written before the value, unless it is the tag the value has
// anyway. An empty plain scalar with no tag is null, and so is the zero
// Node. Under a tag, an empty scalar is null under !!null, an error under
// !!int, !!float, and !!bool, and the empty string under any other tag,
// as in spruce. An alias resolves wherever its anchor sits, inside n or
// outside it, and a node that appears in two places decodes in both.
// A DocumentNode decodes its root, and one with no root leaves v unchanged,
// as a null does.
//
// An error's Line and Column are those of the node it is about, so they
// point at the input the tree was read from, and at the anchor's own
// lines for a value reached through an alias. Both are 0 where the
// failure has no node. A Node built by hand gives the Line and Column the
// caller set, with a negative Column counted as 0, and a Line below 1
// gives no place at all.
//
// Decode builds from n the syntax tree that the parser would build from its
// text, and it decodes that tree as Unmarshal does once it has parsed. A
// type with its own UnmarshalYAML method receives the tree written out as
// flow-style text, which keeps every value but not the layout or the
// comments of the input.
//
// Decode into a *Node copies n into it. A malformed tree is an
// ErrorKindSyntax error, such as one with an alias that points at
// nothing, a mapping with an odd number of children, a mapping key that
// is a sequence or mapping, a key that appears twice in one mapping, or a
// node inside itself. A tree nested more than 10,000 levels deep is an
// ErrorKindLimit error, as is one whose aliases expand past the limit
// Unmarshal sets.
//
// Decode never changes n, so several goroutines may decode one tree at the
// same time, as long as none of them changes it.
func (n *Node) Decode(v any) error {
	if n == nil {
		return typeError("Decode on a nil *yaml.Node")
	}
	if p, ok := v.(*Node); ok {
		if p == nil {
			return typeError("Decode(nil %T)", p)
		}
		*p = *n
		return nil
	}
	if err := checkTargetFor("Decode", v); err != nil {
		return err
	}
	return decodeFrom(n.source(), v, n.opts)
}
