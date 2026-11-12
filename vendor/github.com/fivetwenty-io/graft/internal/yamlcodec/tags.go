package yamlcodec

import (
	"encoding/base64"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/token"

	"github.com/fivetwenty-io/graft/internal/yamlnode"
)

// longTagPrefix starts the verbatim form of a core tag, as in
// !<tag:yaml.org,2002:str>.
const longTagPrefix = "tag:yaml.org,2002:"

// TagValueError reports a scalar whose explicit tag cannot hold its
// text, such as !!int on a word. spruce's YAML library refuses the same
// scalars, but its message quotes the text, and this one names only the
// tags and the tag's position, with Line and Column counted from 1.
type TagValueError struct {
	// Tag is the scalar's tag in its short form, such as !!int.
	Tag string
	// Resolved is the tag the text resolves to without the explicit
	// one, such as !!str, or "" when the tag is !!binary and the text is
	// not base64.
	Resolved string
	Line     int
	Column   int
}

// Message is the error's text without its position.
func (e *TagValueError) Message() string {
	if e.Resolved == "" {
		return e.Tag + " value contains invalid base64 data"
	}
	return fmt.Sprintf("cannot decode a %s scalar as %s", e.Resolved, e.Tag)
}

func (e *TagValueError) Error() string {
	if e.Line > 0 {
		return fmt.Sprintf("yaml: line %d, column %d: %s", e.Line, e.Column, e.Message())
	}
	return "yaml: " + e.Message()
}

// ResolveTags returns body, a document's root node, with every scalar
// that carries an explicit !!str, !!bool, !!int, !!float, !!null, or
// !!binary tag replaced by the scalar spruce's YAML library (yaml.v2)
// reads it as. It rewrites body in place.
//
// !!str keeps the text as written, so !!str yes stays the word yes.
// !!bool, !!int, !!float, and !!null resolve the text by yaml.v2's YAML
// 1.1 rules and fail with a TagValueError when it resolves to another
// type. !!binary decodes base64 to the text of its bytes. A tag spruce
// leaves alone and a tag on a collection stay as they are, and a tag on
// an alias or over another tag fails, as it does in spruce.
//
// A string the pass makes is a double-quoted scalar, so the YAML 1.1
// boolean coercion that follows leaves it a string, and each new scalar
// takes the place of the tag's and the scalar's tokens, so goccy's
// formatter still reads the document.
func ResolveTags(body ast.Node) (ast.Node, error) {
	return tagResolver{}.resolveTagsIn(body)
}

// tagResolver is the walk ResolveTags makes. moved, when set, hears of
// each token the walk puts in the place of another, so a caller that
// keeps a record keyed by token can follow the change.
type tagResolver struct {
	moved func(from, to *token.Token)
}

// replace tells moved that to now stands where from stood.
func (r tagResolver) replace(from, to *token.Token) {
	if r.moved != nil && from != nil {
		r.moved(from, to)
	}
}

// splice is spliceTokens, and it tells moved that tk stands where the
// tag's token and the scalar's first token stood.
func (r tagResolver) splice(tag, first, last, tk *token.Token) {
	spliceTokens(tag, first, last, tk)
	r.replace(tag, tk)
	r.replace(first, tk)
}

func (r tagResolver) resolveTagsIn(n ast.Node) (ast.Node, error) {
	var err error
	switch x := n.(type) {
	case *ast.TagNode:
		return r.resolveTagNode(x)
	case *ast.AnchorNode:
		x.Value, err = r.resolveTagsIn(x.Value)
	case *ast.MappingKeyNode:
		x.Value, err = r.resolveTagsIn(x.Value)
	case *ast.MappingValueNode:
		err = r.resolveMappingValue(x)
	case *ast.MappingNode:
		for _, mv := range x.Values {
			if err = r.resolveMappingValue(mv); err != nil {
				break
			}
		}
	case *ast.SequenceNode:
		err = r.resolveSequence(x)
	}
	if err != nil {
		return nil, err
	}
	return n, nil
}

// resolveSequence resolves the tags in seq's items, keeping its entries,
// which the formatter reads, in step with its values.
func (r tagResolver) resolveSequence(seq *ast.SequenceNode) error {
	for i, v := range seq.Values {
		got, err := r.resolveTagsIn(v)
		if err != nil {
			return err
		}
		seq.Values[i] = got
		if i < len(seq.Entries) && seq.Entries[i] != nil && seq.Entries[i].Value == v {
			seq.Entries[i].Value = got
		}
	}
	return nil
}

func (r tagResolver) resolveMappingValue(mv *ast.MappingValueNode) error {
	if mv == nil {
		return nil
	}
	k, err := r.resolveTagsIn(mv.Key)
	if err != nil {
		return err
	}
	if key, ok := k.(ast.MapKeyNode); ok {
		mv.Key = key
	}
	v, err := r.resolveTagsIn(mv.Value)
	if err != nil {
		return err
	}
	mv.Value = v
	return nil
}

// resolveTagNode resolves t when it tags a scalar with a core tag, and
// otherwise walks the node it tags, dropping a core tag that sits on a
// collection. A node takes one tag, and an alias
// takes none, so t on another tag or on an alias fails, as it does in
// spruce, whose parser stops there. The error for a second tag names the
// line of that tag, where spruce's names the line before it, or none on
// the first line.
func (r tagResolver) resolveTagNode(t *ast.TagNode) (ast.Node, error) {
	inner := t.Value
	if a, ok := inner.(*ast.AnchorNode); ok {
		inner = a.Value
	}
	switch x := inner.(type) {
	case *ast.TagNode:
		line, col := position(x.Start)
		return nil, &yamlnode.ParseError{Line: line, SourceLine: line, SourceColumn: col, Message: "found a second tag on one node"}
	case *ast.AliasNode:
		return nil, propertyError(x.Start, "found a tag on an alias")
	}
	tag := coreTag(t)
	scalar, anchor := taggedScalar(t.Value)
	if tag == "" && scalar != nil {
		return r.keepTaggedText(t, scalar, anchor), nil
	}
	if tag == "" || scalar == nil {
		v, err := r.resolveTagsIn(t.Value)
		if err != nil {
			return nil, err
		}
		t.Value = v
		if tag == "" {
			return t, nil
		}
		// yaml.v2 reads a collection as one whatever its tag, so a core
		// scalar tag on one, which only a hand-built Node can carry past
		// goccy's parser, comes off.
		dropToken(t.Start)
		return v, nil
	}

	in, first, last := scalarSource(scalar)
	value, err := resolveScalar(tag, in)
	if err != nil {
		// goccy places a scalar after a tag one column early, so the
		// error points at the tag, which it places right.
		err.Line, err.Column = position(t.Start)
		return nil, err
	}
	out := replacementNode(tag, in, value, scalar, t)
	r.splice(t.Start, first, last, out.GetToken())
	if anchor != nil {
		anchor.Value = out
		return anchor, nil
	}
	return out, nil
}

// coreTag returns t's tag in its short form when it is one of the tags
// ResolveTags resolves, or "".
func coreTag(t *ast.TagNode) string {
	switch name := tagName(t); name {
	case yamlnode.TagStr, yamlnode.TagBool, yamlnode.TagInt, yamlnode.TagFloat, yamlnode.TagNull, yamlnode.TagBinary:
		return name
	}
	return ""
}

// tagName returns t's tag as written, with the verbatim form of a core
// tag, such as !<tag:yaml.org,2002:str>, in its short form, or "" when t
// has no token.
func tagName(t *ast.TagNode) string {
	if t.Start == nil {
		return ""
	}
	name := t.Start.Value
	if strings.HasPrefix(name, "!<"+longTagPrefix) && strings.HasSuffix(name, ">") {
		return "!!" + name[len("!<"+longTagPrefix):len(name)-1]
	}
	return name
}

// keepTaggedText handles scalar, which sits under t, a tag that spruce's
// YAML library does not resolve, such as !!timestamp, a custom tag like
// !foo, or the bare !. That library keeps the scalar's text as a string,
// so !!timestamp word stays the word, and !foo yes stays the word yes.
//
// goccy reads a !!timestamp scalar as a time whatever its text, and the
// zero time when the text isn't a date, so that tag comes off and the
// scalar becomes the string !!str makes, which a time.Time field still
// parses. Under any other tag goccy already reads the text as written,
// apart from a YAML 1.1 boolean word, which the compat coercion would
// turn into a boolean, and no text at all, which goccy reads as null and
// spruce as the empty string. That word, or the empty string, becomes a
// quoted string under the same tag, and any other scalar stays as it is.
func (r tagResolver) keepTaggedText(t *ast.TagNode, scalar ast.ScalarNode, anchor *ast.AnchorNode) ast.Node {
	in, first, last := scalarSource(scalar)
	if tagName(t) == yamlnode.TagTimestamp {
		out := replacementNode(yamlnode.TagStr, in, in, scalar, t)
		r.splice(t.Start, first, last, out.GetToken())
		if anchor != nil {
			anchor.Value = out
			return anchor
		}
		return out
	}
	if !Yaml11BoolLookalikeWords[in] && !isImplicitNull(scalar) {
		return t
	}
	if s, ok := scalar.(*ast.StringNode); ok && s.Token != nil && IsQuotedScalarToken(s.Token.Type) {
		return t
	}
	out := replacementNode(yamlnode.TagStr, in, in, scalar, t)
	tk := out.GetToken()
	r.replace(first, tk)
	lead, trail := " ", ""
	if first != nil && first.Position != nil {
		pos := *first.Position
		tk.Position = &pos
	}
	if linked(first) {
		lead, trail = leadingSpace(first.Origin), trailingSpace(last.Origin)
		replaceRun(first, last, tk)
	}
	tk.Origin = lead + tk.Origin + trail
	if anchor != nil {
		anchor.Value = out
	} else {
		t.Value = out
	}
	return t
}

// propertyError is the parse error for a tag that sits where the token
// tk is, which no tag may, with the message msg.
func propertyError(tk *token.Token, msg string) error {
	line, col := position(tk)
	return &yamlnode.ParseError{Line: libyamlLine(line), SourceLine: line, SourceColumn: col, Message: msg}
}

// taggedScalar returns the scalar n is, or the scalar the anchor n is
// names along with that anchor, or nil when n holds no scalar.
func taggedScalar(n ast.Node) (ast.ScalarNode, *ast.AnchorNode) {
	anchor, _ := n.(*ast.AnchorNode)
	if anchor != nil {
		n = anchor.Value
	}
	switch n.(type) {
	case *ast.StringNode, *ast.IntegerNode, *ast.FloatNode, *ast.BoolNode,
		*ast.NullNode, *ast.InfinityNode, *ast.NanNode, *ast.LiteralNode, *ast.MergeKeyNode:
		if s, ok := n.(ast.ScalarNode); ok {
			return s, anchor
		}
	}
	return nil, nil
}

// isImplicitNull reports whether s is the null the parser gives a node
// with no text at all.
func isImplicitNull(s ast.ScalarNode) bool {
	n, ok := s.(*ast.NullNode)
	return ok && (n.Token == nil || n.Token.Type == token.ImplicitNullType)
}

// scalarSource returns the text of s as the input wrote it, after any
// quotes and escapes, and the first and last of its tokens.
func scalarSource(s ast.ScalarNode) (string, *token.Token, *token.Token) {
	switch x := s.(type) {
	case *ast.StringNode:
		return x.Value, x.Token, x.Token
	case *ast.LiteralNode:
		var last *token.Token
		if x.Value != nil {
			last = x.Value.Token
			return x.Value.Value, x.Start, last
		}
		return "", x.Start, x.Start
	case *ast.NullNode:
		if isImplicitNull(x) {
			return "", x.Token, x.Token
		}
	}
	tk := s.GetToken()
	if tk == nil {
		return "", nil, nil
	}
	return tk.Value, tk, tk
}

// resolveScalar is yaml.v2's resolve for a scalar with the explicit tag
// tag and the text in, followed by its decoder's !!binary step. It
// returns the Go value spruce reads, which for !!str and !!binary is the
// text, or the error spruce's library raises, without the text.
func resolveScalar(tag, in string) (any, *TagValueError) {
	switch tag {
	case yamlnode.TagStr:
		return in, nil
	case yamlnode.TagBinary:
		data, err := base64.StdEncoding.DecodeString(in)
		if err != nil {
			return nil, &TagValueError{Tag: tag}
		}
		return string(data), nil
	}
	rtag, value := resolvePlain(in)
	if tag == yamlnode.TagFloat && rtag == yamlnode.TagInt {
		// graft reads integer text under !!float as that integer's
		// float. spruce refuses it, and this is a deliberate
		// divergence.
		switch v := value.(type) {
		case int64:
			return float64(v), nil
		case uint64:
			return float64(v), nil
		}
	}
	if rtag != tag {
		return nil, &TagValueError{Tag: tag, Resolved: rtag}
	}
	return value, nil
}

// resolveWord holds a word yaml.v2 resolves by lookup, and its tag.
type resolveWord struct {
	tag   string
	value any
}

// resolveWords is yaml.v2's resolveMap, without the merge key, which no
// hint ever reaches.
var resolveWords = func() map[string]resolveWord {
	m := make(map[string]resolveWord)
	for _, item := range []struct {
		value any
		tag   string
		words []string
	}{
		{true, yamlnode.TagBool, []string{"y", "Y", "yes", "Yes", "YES", "true", "True", "TRUE", "on", "On", "ON"}},
		{false, yamlnode.TagBool, []string{"n", "N", "no", "No", "NO", "false", "False", "FALSE", "off", "Off", "OFF"}},
		{nil, yamlnode.TagNull, []string{"", "~", "null", "Null", "NULL"}},
		{math.NaN(), yamlnode.TagFloat, []string{".nan", ".NaN", ".NAN"}},
		{math.Inf(1), yamlnode.TagFloat, []string{".inf", ".Inf", ".INF", "+.inf", "+.Inf", "+.INF"}},
		{math.Inf(-1), yamlnode.TagFloat, []string{"-.inf", "-.Inf", "-.INF"}},
	} {
		for _, w := range item.words {
			m[w] = resolveWord{item.tag, item.value}
		}
	}
	return m
}()

// resolvePlain is yaml.v2's resolve of in for a tag other than !!str and
// !!binary. It returns the tag in resolves to and its value, which is an
// int64, a uint64, a float64, a bool, nil, or the text itself.
func resolvePlain(in string) (string, any) {
	hint := byte('N')
	if in != "" {
		hint = yamlV2ResolveTable[in[0]]
	}
	if hint != 0 {
		if w, ok := resolveWords[in]; ok {
			return w.tag, w.value
		}
		switch hint {
		case '.':
			if f, err := strconv.ParseFloat(in, 64); err == nil {
				return yamlnode.TagFloat, f
			}
		case 'D', 'S':
			if tag, v, ok := resolveNumber(strings.ReplaceAll(in, "_", "")); ok {
				return tag, v
			}
		}
	}
	if utf8.ValidString(in) {
		return yamlnode.TagStr, in
	}
	return yamlnode.TagBinary, in
}

// resolveNumber is the integer and float half of yaml.v2's resolve, for
// plain, the text with its underscores removed.
func resolveNumber(plain string) (string, any, bool) {
	if i, err := strconv.ParseInt(plain, 0, 64); err == nil {
		return yamlnode.TagInt, i, true
	}
	if u, err := strconv.ParseUint(plain, 0, 64); err == nil {
		return yamlnode.TagInt, u, true
	}
	if f, err := strconv.ParseFloat(plain, 64); err == nil {
		return yamlnode.TagFloat, f, true
	}
	switch {
	case strings.HasPrefix(plain, "0b"):
		if i, err := strconv.ParseInt(plain[2:], 2, 64); err == nil {
			return yamlnode.TagInt, i, true
		}
		if u, err := strconv.ParseUint(plain[2:], 2, 64); err == nil {
			return yamlnode.TagInt, u, true
		}
	case strings.HasPrefix(plain, "-0b"):
		if i, err := strconv.ParseInt(plain[3:], 2, 64); err == nil {
			return yamlnode.TagInt, -i, true
		}
	}
	return "", nil, false
}

// replacementNode builds the scalar that stands in for the tagged
// scalar s, whose tag node is t. Its token keeps in, the text as
// written, as its value, which is the text a string field reads, as
// yaml.v2 gives it, and carries the value's canonical text as its
// origin, which is the text a type's own unmarshal method reads.
func replacementNode(tag, in string, value any, s ast.ScalarNode, t *ast.TagNode) ast.ScalarNode {
	src := s.GetToken()
	if lit, ok := s.(*ast.LiteralNode); ok && lit.Value != nil {
		src = lit.Value.Token
	}
	var tk *token.Token
	if src != nil {
		tk = src.Clone()
	} else {
		tk = &token.Token{Position: t.Start.Position}
	}
	tk.Prev, tk.Next = nil, nil
	tk.Position = t.Start.Position
	if tk.Position != nil {
		pos := *tk.Position
		tk.Position = &pos
	}
	tk.Value = in

	base := baseNodeOf(s, t)
	var out ast.ScalarNode
	var text string
	switch v := value.(type) {
	case string:
		// !!str and !!binary.
		if tag == yamlnode.TagBinary {
			tk.Value = v
		}
		tk.Type, tk.CharacterType = token.DoubleQuoteType, token.CharacterTypeMiscellaneous
		text = strconv.Quote(v)
		out = &ast.StringNode{BaseNode: base, Token: tk, Value: v}
	case bool:
		tk.Type = token.BoolType
		text = strconv.FormatBool(v)
		out = &ast.BoolNode{BaseNode: base, Token: tk, Value: v}
	case int64:
		tk.Type = token.IntegerType
		text = strconv.FormatInt(v, 10)
		out = &ast.IntegerNode{BaseNode: base, Token: tk, Value: v}
	case uint64:
		tk.Type = token.IntegerType
		text = strconv.FormatUint(v, 10)
		out = &ast.IntegerNode{BaseNode: base, Token: tk, Value: v}
	case float64:
		tk.Type = token.FloatType
		text = floatText(v)
		out = &ast.FloatNode{BaseNode: base, Token: tk, Value: v}
	default:
		// !!null.
		tk.Type = token.NullType
		text = "null"
		out = &ast.NullNode{BaseNode: base, Token: tk}
	}
	tk.Origin = text
	return out
}

// floatText writes f so YAML reads it back as the same float.
func floatText(f float64) string {
	switch {
	case math.IsNaN(f):
		return ".nan"
	case math.IsInf(f, 1):
		return ".inf"
	case math.IsInf(f, -1):
		return "-.inf"
	}
	s := strconv.FormatFloat(f, 'g', -1, 64)
	if !strings.ContainsAny(s, ".eEn") {
		s += ".0"
	}
	return s
}

// baseNodeOf returns a copy of the base the tagged scalar s carries,
// which holds its path and comment, falling back to the comment on its
// tag node t.
func baseNodeOf(s ast.ScalarNode, t *ast.TagNode) *ast.BaseNode {
	base := &ast.BaseNode{}
	if b, ok := s.(interface{ GetPath() string }); ok {
		base.Path = b.GetPath()
	}
	base.Comment = s.GetComment()
	if base.Comment == nil {
		base.Comment = t.GetComment()
	}
	return base
}

// spliceTokens puts tk, the replacement for a tagged scalar, into the
// token chain the parser linked, in place of tag, the tag's token, and
// first through last, the scalar's tokens. tk's origin, its value's
// text, gains the space around the text it replaces, so the formatter
// reads the document as it was laid out. An anchor between the tag and
// the scalar keeps its tokens, and the tag's leading space moves onto
// them.
func spliceTokens(tag, first, last, tk *token.Token) {
	lead, trail := "", ""
	switch {
	case linked(first) && first == tag.Next:
		// The tag runs straight into its scalar.
		lead, trail = leadingSpace(tag.Origin), trailingSpace(last.Origin)
		replaceRun(tag, last, tk)
	case linked(first):
		// Tokens such as an anchor sit between the tag and its scalar.
		lead, trail = leadingSpace(first.Origin), trailingSpace(last.Origin)
		replaceRun(first, last, tk)
		dropToken(tag)
	case linked(tag):
		// The scalar never joined the chain, as an empty value after a
		// tag does not.
		lead, trail = leadingSpace(tag.Origin), trailingSpace(tag.Origin)
		replaceRun(tag, tag, tk)
	default:
		lead = " "
	}
	tk.Origin = lead + tk.Origin + trail
}

// linked reports whether tk sits in a token chain.
func linked(tk *token.Token) bool {
	return tk != nil && ((tk.Prev != nil && tk.Prev.Next == tk) || (tk.Next != nil && tk.Next.Prev == tk))
}

// replaceRun puts tk in the chain in place of the tokens first through
// last.
func replaceRun(first, last, tk *token.Token) {
	tk.Prev, tk.Next = first.Prev, last.Next
	if tk.Prev != nil {
		tk.Prev.Next = tk
	}
	if tk.Next != nil {
		tk.Next.Prev = tk
	}
}

// dropToken takes tk out of its chain and hands its leading space to
// the token after it.
func dropToken(tk *token.Token) {
	if !linked(tk) {
		return
	}
	if tk.Next != nil {
		if lead := leadingSpace(tk.Origin); lead != "" && leadingSpace(tk.Next.Origin) == "" {
			tk.Next.Origin = lead + tk.Next.Origin
		}
		tk.Next.Prev = tk.Prev
	}
	if tk.Prev != nil {
		tk.Prev.Next = tk.Next
	}
}

func leadingSpace(s string) string {
	return s[:len(s)-len(strings.TrimLeft(s, " \t\r\n"))]
}

func trailingSpace(s string) string {
	return s[len(strings.TrimRight(s, " \t\r\n")):]
}

// position returns tk's line and column, counted from 1, or zeros when
// it has none.
func position(tk *token.Token) (int, int) {
	if tk == nil || tk.Position == nil {
		return 0, 0
	}
	return tk.Position.Line, tk.Position.Column
}
