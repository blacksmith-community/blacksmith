package yaml

import (
	"encoding"
	"reflect"
	"strings"
	"time"

	goyaml "github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"
)

// asWritten returns body prepared for goccy to decode into a value of
// type t, with the scalars that a type which decodes itself will read.
//
// goccy formats a number or a boolean it puts into a string, so
// version: 1.10 would become "1.1", where yaml.v2 and yaml.v3 keep the
// text as written. Wherever t puts such a scalar into a string, the walk
// swaps in a string node that holds the scalar's source text and keeps
// its token, so every error still names the right line. It does the same
// for the keys of a mapping that fills a struct, since goccy matches
// keys to fields as strings and empties the whole struct when a key is
// anything else, and for the keys of a typed map with string keys. The
// body comes from yamlcodec.PrepareBody, which has folded every << merge
// key into its mapping, so the walk meets none.
//
// Alias expansion shares one subtree among every alias to an anchor, and
// two aliases can fill places of different types, so the walk never
// changes a node in place. It copies each node on the path to one it
// replaces and leaves every other subtree shared.
func asWritten(body ast.Node, t reflect.Type) (ast.Node, ownScalars) {
	w := &textWalk{
		structs: make(map[reflect.Type]*structKeys),
		kinds:   make(map[reflect.Type]ownKind),
	}
	return w.node(body, t), w.own
}

// textWalk walks a body and a target type side by side. structs caches
// the keys of each struct type, kinds caches how each type decodes, and
// own collects the scalars that a type which decodes itself reads.
type textWalk struct {
	structs map[reflect.Type]*structKeys
	kinds   map[reflect.Type]ownKind
	own     ownScalars
}

// ownKind says how goccy decodes a type that decodes itself, in the order
// goccy checks: by a YAML unmarshaler method, as time.Time, as
// time.Duration, or by UnmarshalText.
type ownKind uint8

const (
	notOwn ownKind = iota
	ownYAML
	ownTime
	ownDuration
	ownText
)

var (
	timeType            = reflect.TypeFor[time.Time]()
	durationType        = reflect.TypeFor[time.Duration]()
	textUnmarshalerType = reflect.TypeFor[encoding.TextUnmarshaler]()

	// yamlUnmarshalerTypes are goccy's unmarshaler interfaces.
	yamlUnmarshalerTypes = []reflect.Type{
		reflect.TypeFor[goyaml.BytesUnmarshaler](),
		reflect.TypeFor[goyaml.BytesUnmarshalerContext](),
		reflect.TypeFor[goyaml.InterfaceUnmarshaler](),
		reflect.TypeFor[goyaml.InterfaceUnmarshalerContext](),
		reflect.TypeFor[goyaml.NodeUnmarshaler](),
		reflect.TypeFor[goyaml.NodeUnmarshalerContext](),
	}
)

// kind reports how goccy decodes a value of type t, which it checks on a
// pointer to the value.
func (w *textWalk) kind(t reflect.Type) ownKind {
	if got, ok := w.kinds[t]; ok {
		return got
	}
	got := ownKindOf(t)
	w.kinds[t] = got
	return got
}

func ownKindOf(t reflect.Type) ownKind {
	p := reflect.PointerTo(t)
	for _, it := range yamlUnmarshalerTypes {
		if p.Implements(it) {
			return ownYAML
		}
	}
	switch {
	case t == timeType:
		return ownTime
	case t == durationType:
		return ownDuration
	case p.Implements(textUnmarshalerType):
		return ownText
	default:
		return notOwn
	}
}

// node returns n prepared to fill a value of type t.
func (w *textWalk) node(n ast.Node, t reflect.Type) ast.Node {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if n == nil {
		return nil
	}
	if k := w.kind(t); k != notOwn {
		if k != ownYAML && isScalarNode(n) {
			w.own = append(w.own, ownScalar{kind: k, typ: t, node: n})
		}
		return n
	}
	switch t.Kind() {
	case reflect.String:
		return scalarText(n)
	case reflect.Struct:
		return w.structMapping(n, t)
	case reflect.Map:
		return w.typedMapping(n, t)
	case reflect.Slice, reflect.Array:
		if seq, ok := n.(*ast.SequenceNode); ok {
			return w.sequence(seq, t.Elem())
		}
	default:
	}
	return n
}

// isScalarNode reports whether n is a scalar goccy reads with no node
// below it.
func isScalarNode(n ast.Node) bool {
	switch n.Type() {
	case ast.MappingType, ast.MappingValueType, ast.SequenceType, ast.NullType:
		return false
	default:
		return true
	}
}

// sequence returns seq with each item prepared to fill a value of type
// elem.
func (w *textWalk) sequence(seq *ast.SequenceNode, elem reflect.Type) ast.Node {
	var out *ast.SequenceNode
	for i, item := range seq.Values {
		got := w.node(item, elem)
		if got == item {
			continue
		}
		if out == nil {
			c := *seq
			c.Values = append([]ast.Node(nil), seq.Values...)
			out = &c
		}
		out.Values[i] = got
	}
	if out == nil {
		return seq
	}
	return out
}

// structMapping returns n prepared to fill a struct of type t. Every key
// becomes a string, and each value is prepared for the one type that
// reads its key, if there is exactly one.
func (w *textWalk) structMapping(n ast.Node, t reflect.Type) ast.Node {
	m, ok := n.(*ast.MappingNode)
	if !ok {
		return n
	}
	keys := w.structKeys(t)
	// pairs stays nil until a pair changes, and then holds every pair.
	var pairs []*ast.MappingValueNode
	for i, mv := range m.Values {
		got := w.pair(mv, keyText, keys.valueType)
		if pairs == nil && got != mv {
			pairs = append(make([]*ast.MappingValueNode, 0, len(m.Values)), m.Values[:i]...)
		}
		if pairs != nil {
			pairs = append(pairs, got)
		}
	}
	if pairs == nil {
		return m
	}
	c := *m
	c.Values = pairs
	return &c
}

// typedMapping returns n prepared to fill a map of type t. Each value is
// prepared for the element type.
// A key becomes its source text when the map's keys are strings, unless
// its values are generic, whose keys follow the generic route.
func (w *textWalk) typedMapping(n ast.Node, t reflect.Type) ast.Node {
	m, ok := n.(*ast.MappingNode)
	if !ok {
		return n
	}
	key := func(k ast.MapKeyNode) ast.MapKeyNode { return k }
	if t.Key().Kind() == reflect.String && w.kind(t.Key()) == notOwn {
		key = keyText
		if t.Elem().Kind() == reflect.Interface {
			key = genericKeyText
		}
	}
	elem := func(string) (reflect.Type, bool) { return t.Elem(), true }
	var out *ast.MappingNode
	for i, mv := range m.Values {
		got := w.pair(mv, key, elem)
		if got == mv {
			continue
		}
		if out == nil {
			c := *m
			c.Values = append([]*ast.MappingValueNode(nil), m.Values...)
			out = &c
		}
		out.Values[i] = got
	}
	if out == nil {
		return m
	}
	return out
}

// pair returns mv prepared to sit in a mapping. key rewrites the key,
// and value gives the type the value under a key fills, or false when
// the walk must leave the value alone.
func (w *textWalk) pair(mv *ast.MappingValueNode, key func(ast.MapKeyNode) ast.MapKeyNode, value func(string) (reflect.Type, bool)) *ast.MappingValueNode {
	k, v := key(mv.Key), mv.Value
	if ft, ok := value(keyString(k)); ok {
		v = w.node(v, ft)
	}
	if k == mv.Key && v == mv.Value {
		return mv
	}
	c := *mv
	c.Key = k
	c.Value = v
	return &c
}

// keyString returns the text of a string key, and "" for any other key,
// which no struct field reads once keyText has run.
func keyString(k ast.MapKeyNode) string {
	if mk, ok := k.(*ast.MappingKeyNode); ok {
		if s, ok := mk.Value.(*ast.StringNode); ok {
			return s.Value
		}
		return ""
	}
	if s, ok := k.(*ast.StringNode); ok {
		return s.Value
	}
	return ""
}

// structKeys records, for a struct type, the types of the fields that
// read each key. goccy hands an ,inline map every key of the mapping, so
// every holds the element types of those maps, and opaque is set when an
// ,inline field decodes itself and so reads every key as written.
type structKeys struct {
	named  map[string][]reflect.Type
	every  []reflect.Type
	opaque bool
}

// valueType returns the one type the value under key fills, or false
// when no field reads the key or the fields that read it disagree.
func (k *structKeys) valueType(key string) (reflect.Type, bool) {
	if k.opaque {
		return nil, false
	}
	var got reflect.Type
	for _, list := range [][]reflect.Type{k.named[key], k.every} {
		for _, t := range list {
			if got != nil && got != t {
				return nil, false
			}
			got = t
		}
	}
	return got, got != nil
}

// structKeys returns the keys a struct of type t reads, under goccy's
// rules. A field's key is the name in its yaml tag, else the name in its
// json tag, else its name in lower case. An unexported field that is not
// embedded is skipped, as is a field tagged "-". An ,inline struct adds
// its own keys.
func (w *textWalk) structKeys(t reflect.Type) *structKeys {
	if got, ok := w.structs[t]; ok {
		return got
	}
	got := &structKeys{named: make(map[string][]reflect.Type)}
	// Entering the cache first ends the walk of a struct that inlines
	// itself through a pointer.
	w.structs[t] = got
	for i := range t.NumField() {
		f := t.Field(i)
		tag := f.Tag.Get("yaml")
		if tag == "" {
			tag = f.Tag.Get("json")
		}
		if (f.PkgPath != "" && !f.Anonymous) || tag == "-" {
			continue
		}
		name, opts, _ := strings.Cut(tag, ",")
		if hasOption(opts, "inline") {
			w.addInline(got, f.Type)
			continue
		}
		if name == "" {
			name = strings.ToLower(f.Name)
		}
		got.named[name] = append(got.named[name], f.Type)
	}
	return got
}

// addInline adds to keys what an ,inline field of type ft reads.
func (w *textWalk) addInline(keys *structKeys, ft reflect.Type) {
	for ft.Kind() == reflect.Pointer {
		ft = ft.Elem()
	}
	switch {
	case w.kind(ft) != notOwn:
		keys.opaque = true
	case ft.Kind() == reflect.Struct:
		inner := w.structKeys(ft)
		if inner == keys {
			return
		}
		for name, types := range inner.named {
			keys.named[name] = append(keys.named[name], types...)
		}
		keys.every = append(keys.every, inner.every...)
		keys.opaque = keys.opaque || inner.opaque
	case ft.Kind() == reflect.Map:
		keys.every = append(keys.every, ft.Elem())
	default:
		keys.opaque = true
	}
}

// hasOption reports whether the comma-separated tag options opts hold
// want.
func hasOption(opts, want string) bool {
	for opts != "" {
		var opt string
		opt, opts, _ = strings.Cut(opts, ",")
		if opt == want {
			return true
		}
	}
	return false
}

// scalarText returns n as a string node that holds its source text when
// goccy would read n as a number or a boolean, and n otherwise. A core
// scalar tag over a scalar is dropped, since goccy converts the scalar
// the tag names, as it converts !!float 1e3 to "1000", where yaml.v2 and
// yaml.v3 put an explicitly tagged scalar's source text into a string.
func scalarText(n ast.Node) ast.Node {
	switch v := n.(type) {
	case *ast.IntegerNode, *ast.FloatNode, *ast.InfinityNode, *ast.NanNode, *ast.BoolNode:
		return sourceText(n)
	case *ast.TagNode:
		if !coreScalarTag(v) || !isScalarNode(v.Value) {
			return n
		}
		return scalarText(v.Value)
	default:
	}
	return n
}

// coreScalarTag reports whether n's tag is one of the core schema's
// scalar tags, which yaml.v2 and yaml.v3 ignore when they read the
// scalar into a string.
func coreScalarTag(n *ast.TagNode) bool {
	if n.Start == nil {
		return false
	}
	switch n.Start.Value {
	case "!!str", "!!int", "!!float", "!!bool", "!!null":
		return true
	}
	return false
}

// sourceText returns a string node that holds the source text of n, a
// scalar, with n's token.
func sourceText(n ast.Node) *ast.StringNode {
	tk := n.GetToken()
	return &ast.StringNode{BaseNode: baseOf(n), Token: tk, Value: tk.Value}
}

// keyText returns a key goccy would read as a number or a boolean as a
// string node of its source text, and a null key as "", which is how
// yaml.v2 reads keys into strings. Any other key stays as it is.
func keyText(k ast.MapKeyNode) ast.MapKeyNode {
	return rewriteKey(k, "")
}

// genericKeyText is keyText for a map whose values are generic, where a
// null key reads as "null", as a top-level decode reads it, and other
// keys stay as they are, since goccy formats them the same way.
func genericKeyText(k ast.MapKeyNode) ast.MapKeyNode {
	if _, ok := k.(*ast.NullNode); ok {
		return rewriteKey(k, "null")
	}
	return k
}

// rewriteKey turns k, or the key inside an explicit ? key or under a
// core scalar tag, into a string node. A null key becomes null. A core
// tag is dropped, since goccy would read a tagged number as the number
// and refuse a tagged key for a struct field.
func rewriteKey(k ast.MapKeyNode, null string) ast.MapKeyNode {
	switch v := k.(type) {
	case *ast.IntegerNode, *ast.FloatNode, *ast.InfinityNode, *ast.NanNode, *ast.BoolNode:
		return sourceText(v)
	case *ast.NullNode:
		return &ast.StringNode{BaseNode: baseOf(v), Token: v.GetToken(), Value: null}
	case *ast.MappingKeyNode:
		inner, ok := v.Value.(ast.MapKeyNode)
		if !ok {
			return k
		}
		if got := rewriteKey(inner, null); got != inner {
			c := *v
			c.Value = got
			return &c
		}
	case *ast.TagNode:
		if inner, ok := v.Value.(ast.MapKeyNode); ok && coreScalarTag(v) {
			return rewriteKey(inner, null)
		}
	default:
	}
	return k
}

// baseOf returns a copy of n's base node, so a replacement keeps n's
// comment and path without sharing them.
func baseOf(n ast.Node) *ast.BaseNode {
	if b, ok := n.(interface{ GetBaseNode() *ast.BaseNode }); ok && b.GetBaseNode() != nil {
		c := *b.GetBaseNode()
		return &c
	}
	return &ast.BaseNode{}
}
