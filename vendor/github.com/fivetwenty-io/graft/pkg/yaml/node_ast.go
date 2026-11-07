package yaml

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/token"

	"github.com/fivetwenty-io/graft/internal/yamlcodec"
	"github.com/fivetwenty-io/graft/internal/yamlnode"
)

// The limits Decode keeps. maxNodeDepth is the parser's own nesting
// limit, so any tree Unmarshal returns is within it. maxAliasChain is how
// many aliases in a row Decode follows before it gives up on a chain.
const (
	maxNodeDepth  = 10000
	maxAliasChain = 64
)

// The messages for a tree Decode cannot read. None of them quotes a
// value, a tag, or an anchor name.
const (
	nodeDepthMessage        = "exceeded max depth of 10000"
	nodeKindMessage         = "node has no valid kind"
	nodeNilMessage          = "node holds a nil child"
	nodeOddMappingMessage   = "mapping node holds an odd number of children"
	nodeEmptyAliasMessage   = "alias node points at no node"
	nodeAliasChainMessage   = "alias node never reaches a value"
	nodeDocumentMessage     = "document node appears inside another node"
	nodeDocumentRootMessage = "document node holds more than one root"
	nodeCollectionKeyMsg    = "mapping key is a sequence or mapping"
	nodeUTF8Message         = "scalar is not valid UTF-8"
	nodeTagMessage          = "node has a tag YAML cannot write"
	nodeDuplicateKeyMessage = "mapping key already defined"
)

// source returns the source Decode reads n through. Each call builds a
// fresh syntax tree from n, the tree goccy's parser would build from n
// written as text, and hands it to the same step that follows the parse
// in Unmarshal. A node reached more than once, directly or through
// aliases, is built once under an anchor and is an alias after that, so
// the tree grows with n and not with its expansion, and the expansion
// meets the limit Unmarshal sets.
func (n *Node) source() source {
	return func(tag bool) (ast.Node, error) {
		root, de := documentRoot(n)
		if de != nil {
			return nil, de
		}
		if root == nil {
			return nil, nil
		}
		b := &astBuilder{
			refs:    make(map[*Node]int),
			open:    make(map[*Node]bool),
			anchors: make(map[*Node]string),
			yaml12:  n.opts.YAML12,
		}
		if de := b.scan(root, 0); de != nil {
			return nil, de
		}
		body := b.value(root, false)
		if b.err != nil {
			return nil, b.err
		}
		body, err := yamlcodec.PrepareBody(body, tag)
		if err != nil {
			return nil, translate(err)
		}
		return body, nil
	}
}

// documentRoot returns the node Decode reads for n, which is n itself or
// a document's root, and nil for a document with no root.
func documentRoot(n *Node) (*Node, *DecodeError) {
	if n.Kind != DocumentNode {
		return n, nil
	}
	switch len(n.Content) {
	case 0:
		return nil, nil
	case 1:
		if n.Content[0] == nil {
			return nil, treeError(n, nodeNilMessage)
		}
		return n.Content[0], nil
	default:
		return nil, treeError(n, nodeDocumentRootMessage)
	}
}

// treeError reports a tree Decode cannot read, at the place of the node
// at fault, which is the caller's own place for a hand-built node.
func treeError(n *Node, msg string) *DecodeError {
	e := &DecodeError{Kind: ErrorKindSyntax, Msg: msg}
	if n != nil {
		pos := position(n)
		e.Line, e.Column = pos.Line, pos.Column
	}
	return e
}

// position returns the place a token built for n carries, which is
// where any error about n points. A Line below 1 gives no place at all,
// and a negative Column counts as 0.
func position(n *Node) *token.Position {
	if n.Line <= 0 {
		return &token.Position{}
	}
	return &token.Position{Line: n.Line, Column: max(n.Column, 0)}
}

// tokenPosition returns the place the token of n's own value carries,
// after any tag or anchor. For a node Unmarshal read it is where the
// parser's token sat, so an error about the value points where Unmarshal
// points it, and for any other node it is the node's own place.
func tokenPosition(n *Node) *token.Position {
	if n.read && n.tokenLine > 0 {
		return &token.Position{Line: n.tokenLine, Column: max(n.tokenColumn, 0)}
	}
	return position(n)
}

// isZero reports whether n is the zero Node, which reads as null
// wherever it sits, as yaml.v3's IsZero Node does.
func isZero(n *Node) bool {
	return n.Kind == 0 && n.Tag == "" && n.Value == "" && n.Content == nil && n.Alias == nil &&
		n.Line == 0 && n.Column == 0 && n.style == 0 && !n.read
}

// astBuilder builds goccy's syntax tree for a Node tree. refs counts how
// often scan reached each node, open holds the collections scan is
// inside, and anchors names each node built under an anchor. Every token
// joins one chain in the order the text would hold it, since goccy's
// formatter, which writes a value out for a type's own UnmarshalYAML
// method, walks that chain. err is the first duplicate key found.
type astBuilder struct {
	refs    map[*Node]int
	open    map[*Node]bool
	anchors map[*Node]string
	yaml12  bool
	last    *token.Token
	err     *DecodeError
}

// target follows x through any aliases to the node they name.
func target(x *Node) (*Node, *DecodeError) {
	t := x
	for range maxAliasChain {
		if t.Kind != AliasNode {
			return t, nil
		}
		if t.Alias == nil {
			return nil, treeError(t, nodeEmptyAliasMessage)
		}
		t = t.Alias
	}
	return nil, treeError(x, nodeAliasChainMessage)
}

// scan checks that x, reached at depth collections down, can be read,
// and counts how often each node is reached. It descends into a node only
// the first time, so it ends on any tree, and a node reached again while
// scan is inside it is a cycle.
func (b *astBuilder) scan(x *Node, depth int) *DecodeError {
	t, err := target(x)
	if err != nil {
		return err
	}
	if b.open[t] {
		return treeError(x, selfContainingAnchorMessage)
	}
	b.refs[t]++
	if b.refs[t] > 1 {
		return nil
	}
	if err := checkNode(t, depth); err != nil || (t.Kind != MappingNode && t.Kind != SequenceNode) {
		return err
	}
	b.open[t] = true
	defer delete(b.open, t)
	for i, child := range t.Content {
		if child == nil {
			return treeError(t, nodeNilMessage)
		}
		if t.Kind == MappingNode && i%2 == 0 && (child.Kind == MappingNode || child.Kind == SequenceNode) {
			return treeError(child, nodeCollectionKeyMsg)
		}
		if err := b.scan(child, depth+1); err != nil {
			return err
		}
	}
	return nil
}

// checkNode checks the node t itself, reached at depth collections down,
// leaving its children to scan.
func checkNode(t *Node, depth int) *DecodeError {
	switch t.Kind {
	case ScalarNode:
		if !utf8.ValidString(t.Value) {
			return treeError(t, nodeUTF8Message)
		}
	case MappingNode, SequenceNode:
		if depth >= maxNodeDepth {
			return &DecodeError{Kind: ErrorKindLimit, Msg: nodeDepthMessage}
		}
		if t.Kind == MappingNode && len(t.Content)%2 != 0 {
			return treeError(t, nodeOddMappingMessage)
		}
	case DocumentNode:
		return treeError(t, nodeDocumentMessage)
	default:
		if isZero(t) {
			return nil
		}
		return treeError(t, nodeKindMessage)
	}
	return checkTag(t)
}

// checkTag refuses a tag that holds a space, an angle bracket, or a
// character that is not printable, which no form of tag can carry. A tag
// as Unmarshal read it passes, since the parser took it from the text.
func checkTag(t *Node) *DecodeError {
	if t.read && t.Tag == t.readTag {
		return nil
	}
	for _, r := range t.Tag {
		if r == ' ' || r == '<' || r == '>' || !unicode.IsPrint(r) {
			return treeError(t, nodeTagMessage)
		}
	}
	if !utf8.ValidString(t.Tag) {
		return treeError(t, nodeTagMessage)
	}
	return nil
}

// tok adds tk to the end of the token chain and returns it.
func (b *astBuilder) tok(tk *token.Token) *token.Token {
	if b.last != nil {
		b.last.Next, tk.Prev = tk, b.last
	}
	b.last = tk
	return tk
}

// value builds the node x, which scan accepted, as a mapping key when key
// is set. A node reached more than once is built under an anchor the
// first time and as an alias of it after that.
func (b *astBuilder) value(x *Node, key bool) ast.Node {
	t, _ := target(x)
	if b.refs[t] < 2 {
		return b.node(t, key)
	}
	if name, ok := b.anchors[t]; ok {
		pos := tokenPosition(x)
		alias := ast.Alias(b.tok(token.Alias(" *", pos)))
		alias.Value = ast.String(b.tok(token.New(name, name, after(pos))))
		return alias
	}
	name := "a" + strconv.Itoa(len(b.anchors)+1)
	b.anchors[t] = name
	pos := position(t)
	anchor := ast.Anchor(b.tok(token.Anchor(" &", pos)))
	anchor.Name = ast.String(b.tok(token.New(name, name, after(pos))))
	anchor.Value = b.node(t, key)
	return anchor
}

// after returns the place just after pos, where the name of an anchor or
// an alias goes, so the reader takes the name as written right after its
// indicator.
func after(pos *token.Position) *token.Position {
	return &token.Position{Line: pos.Line, Column: pos.Column + 1}
}

// node builds t, which is not an alias, as a mapping key when key is set.
func (b *astBuilder) node(t *Node, key bool) ast.Node {
	switch t.Kind {
	case MappingNode:
		return b.tagged(t, func() ast.Node { return b.mapping(t) })
	case SequenceNode:
		return b.tagged(t, func() ast.Node { return b.sequence(t) })
	default:
		return b.tagged(t, func() ast.Node { return b.scalar(t, key) })
	}
}

// tagged builds t's tag, when it has one to write, and then t's value
// with build, inside the tag.
func (b *astBuilder) tagged(t *Node, build func() ast.Node) ast.Node {
	text := b.writtenTag(t)
	if text == "" {
		return build()
	}
	// The parser's tag token holds the space after the tag, so the text
	// a type's own method receives separates them as the input did, and
	// a tag as Unmarshal read it keeps its own text.
	n := ast.Tag(b.tok(token.Tag(text, " "+text+" ", position(t))))
	n.Value = build()
	read := t.parsed()
	switch null, ok := n.Value.(*ast.NullNode); {
	case read != nil && read.tag == text && read.tagOrigin != "":
		n.Start.Origin = read.tagOrigin
		if !strings.ContainsRune(" \t\r\n", rune(read.tagOrigin[0])) {
			n.Start.Origin = " " + read.tagOrigin
		}
	case ok && null.Token.Type == token.ImplicitNullType:
		// A tag with no value ends its line, so the text a type's own
		// method receives reads as the tag over an empty value.
		n.Start.Origin = " " + text + "\n"
	}
	return n
}

// writtenTag is the tag the text of t would carry, as it would be
// written, or "" when it would carry none. A tag Unmarshal read is
// written when the input wrote it, as the input wrote it when Decode
// knows that, and otherwise in its short form as the input could write
// it, and a tag the caller set is written unless it is the tag the
// value resolves to without one.
func (b *astBuilder) writtenTag(t *Node) string {
	if t.read && t.Tag == t.readTag {
		if t.style&yamlnode.StyleTagged == 0 {
			if t.bareTag {
				return "!"
			}
			return ""
		}
		if read := t.parsed(); read != nil && read.tag != "" {
			return read.tag
		}
		if strings.HasPrefix(t.Tag, "!") {
			return t.Tag
		}
	} else if t.Tag == "" || t.Tag == b.impliedTag(t) {
		return ""
	}
	return tagText(t.Tag)
}

// impliedTag is the tag t has when its text carries none. A plain scalar
// resolves as Unmarshal reads it, so in YAML 1.1 mode yes is !!bool.
func (b *astBuilder) impliedTag(t *Node) string {
	switch {
	case t.Kind == MappingNode:
		return yamlnode.TagMap
	case t.Kind == SequenceNode:
		return yamlnode.TagSeq
	case t.style&(yamlnode.StyleSingle|yamlnode.StyleDouble|yamlnode.StyleLiteral|yamlnode.StyleFolded) != 0:
		return yamlnode.TagStr
	}
	if !b.yaml12 {
		if _, ok := yamlcodec.DefaultYAMLCompat().ConvertValue(t.Value).(bool); ok {
			return yamlnode.TagBool
		}
	}
	return yamlnode.ResolvePlainTag(t.Value)
}

// scalar builds the scalar t, or null for the zero Node, in the style it
// was written in. A scalar as Unmarshal read it takes the token Unmarshal's
// own parser gave it. Any other plain scalar takes the type goccy's
// scanner gives its text, except that after a tag goccy does not know it
// is a string, as the scanner makes it. An empty plain scalar is null,
// and a plain "<<" key is the merge key.
func (b *astBuilder) scalar(t *Node, key bool) ast.Node {
	v, pos := t.Value, tokenPosition(t)
	read := t.parsed()
	block := t.style&(yamlnode.StyleLiteral|yamlnode.StyleFolded) != 0
	switch {
	case read != nil && read.block:
		return b.blockScalar(read.typ, read.origin, read.value, pos)
	case read != nil:
		return b.scalarToken(t, readOrigin(read), read.typ, read.value, pos)
	case block:
		return b.changedBlock(t, pos)
	case t.style&yamlnode.StyleSingle != 0:
		return b.scalarToken(t, " "+quote(v), token.SingleQuoteType, v, pos)
	case t.style&yamlnode.StyleDouble != 0:
		return b.scalarToken(t, " "+quote(v), token.DoubleQuoteType, v, pos)
	case v == "":
		return b.scalarToken(t, " null", token.ImplicitNullType, "null", pos)
	case key && v == "<<":
		return b.scalarToken(t, " <<", token.MergeKeyType, v, pos)
	}
	tk := token.New(v, scalarOrigin(v, token.StringType), pos)
	if tag := b.writtenTag(t); tag != "" && token.ReservedTagKeywordMap[token.ReservedTagKeyword(tag)] == nil {
		tk.Type = token.StringType
	}
	return b.scalarToken(t, tk.Origin, tk.Type, tk.Value, pos)
}

// changedBlock builds the block scalar t, which Unmarshal did not read
// as it now stands, with a plain "|" or ">" header.
func (b *astBuilder) changedBlock(t *Node, pos *token.Position) ast.Node {
	v := t.Value
	if !strings.HasSuffix(v, "\n") {
		// A block scalar that ends without a line break ends at the end
		// of the input or strips its last one, and either way the
		// parser drops the spaces that end its last line.
		v = strings.TrimRight(v, " ")
	}
	if t.style&yamlnode.StyleFolded != 0 {
		return b.blockScalar(token.FoldedType, ">", v, pos)
	}
	return b.blockScalar(token.LiteralType, "|", v, pos)
}

// scalarOrigin is the text written for a scalar token with value v and
// type typ, on one line after a space, which is what a type's own
// UnmarshalYAML method receives for it.
func scalarOrigin(v string, typ token.Type) string {
	switch {
	case typ == token.ImplicitNullType:
		return " null"
	case typ == token.SingleQuoteType || typ == token.DoubleQuoteType || !plainSafe(v, true):
		return " " + quote(v)
	}
	return " " + v
}

// readOrigin is the text written for a scalar as Unmarshal's parser
// read it. A plain scalar keeps the text it was read from, such as
// n<tab>o, which the parser reads as the value no, because a type's own
// UnmarshalYAML method receives that text in Unmarshal, and a scalar
// read from no text at all is written as none. A quoted scalar is
// written from its value.
func readOrigin(read *parsedScalar) string {
	if read.typ == token.SingleQuoteType || read.typ == token.DoubleQuoteType || read.typ == token.ImplicitNullType {
		return scalarOrigin(read.value, read.typ)
	}
	org := strings.TrimRight(read.origin, "\r\n ")
	if org == "" || strings.ContainsRune(" \t\r\n", rune(org[0])) {
		return org
	}
	return " " + org
}

// blockScalar builds a block scalar of type typ, literal or folded, that
// holds v, with header, such as "|-" or ">2", as its first token's
// value, which is what goccy's parser compares a block key by.
func (b *astBuilder) blockScalar(typ token.Type, header, v string, pos *token.Position) ast.Node {
	start := token.Literal(header, " ", pos)
	if typ == token.FoldedType {
		start = token.Folded(header, " ", pos)
	}
	n := ast.Literal(b.tok(start))
	n.Value = ast.String(b.tok(token.String(v, quote(v), pos)))
	return n
}

// scalarToken builds the scalar node for t from a token of type typ that
// holds value and is written as org. The null of an empty value after a
// tag or an anchor stays out of the token chain, as the parser leaves
// it, so the text a type's own method receives holds nothing for it.
func (b *astBuilder) scalarToken(t *Node, org string, typ token.Type, value string, pos *token.Position) ast.Node {
	tk := token.New(value, org, pos)
	tk.Type, tk.Value = typ, value
	if _, anchored := b.anchors[t]; typ != token.ImplicitNullType || (!anchored && b.writtenTag(t) == "") {
		b.tok(tk)
	}
	switch typ {
	case token.MergeKeyType:
		return ast.MergeKey(tk)
	case token.NullType, token.ImplicitNullType:
		return ast.Null(tk)
	case token.BoolType:
		return ast.Bool(tk)
	case token.IntegerType, token.BinaryIntegerType, token.OctetIntegerType, token.HexIntegerType:
		return ast.Integer(tk)
	case token.FloatType:
		return ast.Float(tk)
	case token.InfinityType:
		return ast.Infinity(tk)
	case token.NanType:
		return ast.Nan(tk)
	default:
		return ast.String(tk)
	}
}

// sequence builds the sequence t in flow style.
func (b *astBuilder) sequence(t *Node) ast.Node {
	pos := tokenPosition(t)
	n := ast.Sequence(b.tok(token.SequenceStart(" [", pos)), true)
	for i, item := range t.Content {
		sep := ""
		if i > 0 {
			sep = ","
		}
		entry := b.tok(token.CollectEntry(sep, pos))
		v := b.value(item, false)
		n.Values = append(n.Values, v)
		n.Entries = append(n.Entries, ast.SequenceEntry(entry, v, nil))
	}
	n.End = b.tok(token.SequenceEnd(" ]", pos))
	return n
}

// mapping builds the mapping t in flow style. A key that appears twice
// is an error, as goccy's parser makes it, except where the parser lets
// it through, which is a key with no colon after it in a flow mapping.
func (b *astBuilder) mapping(t *Node) ast.Node {
	pos := tokenPosition(t)
	n := ast.Mapping(b.tok(token.MappingStart(" {", pos)), true)
	var keys yamlnode.KeySet[*token.Position]
	for i := 0; i < len(t.Content); i += 2 {
		var entry *token.Token
		if i > 0 {
			entry = b.tok(token.CollectEntry(",", pos))
		}
		key := mapKey(b.value(t.Content[i], true), explicitKey(t.Content[i]))
		if !bareKey(t, i) {
			b.checkKey(&keys, key, t.Content[i].Kind == AliasNode)
		}
		colon := b.tok(token.MappingValue(position(t.Content[i])))
		pair := ast.MappingValue(colon, key, b.value(t.Content[i+1], false))
		pair.CollectEntry = entry
		n.Values = append(n.Values, pair)
	}
	n.End = b.tok(token.MappingEnd(" }", pos))
	return n
}

// mapKey returns n as a mapping key. A collection, which an alias can
// name as a key, goes inside an explicit key node, as alias expansion
// puts one, and so does a key the text marked with "?", when explicit is
// set, as the parser puts one.
func mapKey(n ast.Node, explicit bool) ast.MapKeyNode {
	if k, ok := n.(ast.MapKeyNode); ok && !explicit {
		return k
	}
	if k, ok := n.(*ast.MappingKeyNode); ok {
		return k
	}
	start := token.MappingKey(n.GetToken().Position)
	start.Origin = " ?"
	return &ast.MappingKeyNode{BaseNode: &ast.BaseNode{}, Start: start, Value: n}
}

// explicitKey reports whether the key k is unchanged from the text and
// the text marked it with "?".
func explicitKey(k *Node) bool {
	read := k.parsed()
	return read != nil && read.explicit
}

// bareKey reports whether the pair at i in the mapping t was read from a
// flow mapping entry with no colon, such as each a in {a, a}. The parser
// gives such an entry an empty null right after the first character of
// its key, where a colon would put it later. The places compared are the
// parser's own, which count a tab as one column, as Column does not.
func bareKey(t *Node, i int) bool {
	k, v := t.Content[i], t.Content[i+1]
	return t.read && t.style&yamlnode.StyleFlow != 0 && k.read && v.read &&
		v.Kind == ScalarNode && v.Value == "" && v.Tag == v.readTag && v.style == 0 &&
		k.tokenLine > 0 && v.tokenLine == k.tokenLine && v.tokenColumn == k.tokenColumn+1
}

// checkKey records key among keys, the keys of one mapping so far, and
// keeps the first repeated key it finds as b.err. Keys compare as the
// text route compares them, which yamlnode.KeyID describes. alias says
// the key is an AliasNode, which compares as the empty text, as goccy's
// parser compares an alias, even where the alias's anchor lies outside
// the tree and key holds the value written in its place. A << merge key
// repeats no key, since a second one is a second merge, as the text
// route reads it.
func (b *astBuilder) checkKey(keys *yamlnode.KeySet[*token.Position], key ast.MapKeyNode, alias bool) {
	if !alias && yamlnode.IsMergeKey(key) {
		return
	}
	id, _ := yamlnode.KeyIDOf(key)
	if alias {
		id = yamlnode.KeyID{}
	}
	first, repeat := keys.Add(id, key.GetToken().Position)
	if !repeat || b.err != nil {
		return
	}
	at := key.GetToken().Position
	msg := nodeDuplicateKeyMessage
	if first.Line > 0 {
		msg += " at [" + strconv.Itoa(first.Line) + ":" + strconv.Itoa(first.Column) + "]"
	}
	b.err = &DecodeError{Kind: ErrorKindSyntax, Line: at.Line, Column: at.Column, Msg: msg}
}

// tagText writes tag in its short form when it has one, and in the
// verbatim !<...> form otherwise. A tag in the YAML namespace, such as
// tag:yaml.org,2002:str, takes its !! form, and the non-specific tag "!"
// stays as it is.
func tagText(tag string) string {
	if tag == "!" {
		return tag
	}
	if suffix, ok := strings.CutPrefix(tag, "tag:yaml.org,2002:"); ok && shortSuffix(suffix) {
		return "!!" + suffix
	}
	suffix, ok := strings.CutPrefix(tag, "!!")
	if !ok {
		suffix, ok = strings.CutPrefix(tag, "!")
	}
	if ok && shortSuffix(suffix) {
		return tag
	}
	return "!<" + tag + ">"
}

// shortSuffix reports whether suffix can follow "!" or "!!" in a tag.
func shortSuffix(suffix string) bool {
	return suffix != "" && strings.IndexFunc(suffix, func(r rune) bool { return !tagRune(r) }) < 0
}

// tagRune reports whether r may appear in a short tag's suffix.
func tagRune(r rune) bool {
	return r < utf8.RuneSelf && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_.:/;=+~$@", r))
}

// plainSafe reports whether v reads back as the same text when written
// as a plain scalar, in flow context when flow is set. It errs toward
// false, since a quoted string reads back as the same string.
func plainSafe(v string, flow bool) bool {
	if !plainStart(v) || strings.Contains(v, ": ") || strings.Contains(v, " #") {
		return false
	}
	for i, r := range v {
		switch {
		case r == ' ':
		case !unicode.IsPrint(r):
			return false
		case !flow:
		case strings.ContainsRune(",[]{}", r):
			return false
		case r == ':' && i+1 < len(v) && strings.ContainsRune(",[]{}", rune(v[i+1])):
			return false
		}
	}
	return true
}

// plainStart reports whether v begins and ends the way a plain scalar
// may, with no indicator first, no space at either end, and no colon
// last. A document marker such as --- is fine, since it is written after
// a space, where it is no marker.
func plainStart(v string) bool {
	if v == "" || v[0] == ' ' || v[len(v)-1] == ' ' || v[len(v)-1] == ':' {
		return false
	}
	if v[0] == '-' {
		return len(v) > 1 && v[1] != ' '
	}
	return !strings.ContainsRune("?:,[]{}#&*!|>'\"%@`", rune(v[0]))
}

// quote writes v as a double-quoted scalar on one line, escaping quotes,
// backslashes, and every character that is not printable.
func quote(v string) string {
	var b strings.Builder
	b.Grow(len(v) + 2)
	b.WriteByte('"')
	for _, r := range v {
		switch {
		case r == '"':
			b.WriteString(`\"`)
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\t':
			b.WriteString(`\t`)
		case r == ' ' || unicode.IsPrint(r):
			b.WriteRune(r)
		case r < 0x100:
			b.WriteString(`\x`)
			b.WriteString(hex(r, 2))
		case r < 0x10000:
			b.WriteString(`\u`)
			b.WriteString(hex(r, 4))
		default:
			b.WriteString(`\U`)
			b.WriteString(hex(r, 8))
		}
	}
	b.WriteByte('"')
	return b.String()
}

// hex formats r as width hexadecimal digits.
func hex(r rune, width int) string {
	s := strconv.FormatInt(int64(r), 16)
	return strings.Repeat("0", width-len(s)) + s
}
