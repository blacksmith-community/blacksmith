// Package yamlcodec holds the YAML encoding and decoding helpers that
// pkg/graft and pkg/yaml share.
package yamlcodec

import (
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/token"

	"github.com/fivetwenty-io/graft/internal/yamlnode"
	"github.com/fivetwenty-io/graft/internal/yamlprep"
)

// QuoteInjectKeys pre-processes YAML bytes to quote the graft-specific
// <<<: inject key, which goccy/go-yaml rejects when unquoted because it
// reads <<< as a variant of the YAML merge key <<. It handles both the
// standalone (<<<:) and dotted path (foo.<<<:) forms.
func QuoteInjectKeys(data []byte) []byte {
	return yamlprep.QuoteInjectKeys(data)
}

// NormalizeMap deep-converts any map[interface{}]interface{} values
// to map[string]interface{} throughout the tree. This is needed because
// values that other decoders produce, such as yaml.v2 through go-patch,
// use map[interface{}]interface{} when maps contain non-string keys
// (e.g., integer keys like 1:, 2:). goccy decodes those same maps into
// map[string]interface{} with stringified keys.
func NormalizeMap(data map[string]interface{}) map[string]interface{} {
	if data == nil {
		return nil
	}
	for k, v := range data {
		data[k] = NormalizeValue(v)
	}
	return data
}

// NormalizeValue converts nested map[interface{}]interface{} values in v
// to map[string]interface{}.
func NormalizeValue(v interface{}) interface{} {
	switch val := v.(type) {
	case map[interface{}]interface{}:
		converted := make(map[string]interface{}, len(val))
		for k, v := range val {
			converted[fmt.Sprintf("%v", k)] = NormalizeValue(v)
		}
		return converted
	case map[string]interface{}:
		return NormalizeMap(val)
	case []interface{}:
		for i, item := range val {
			val[i] = NormalizeValue(item)
		}
		return val
	case uint64:
		if val > uint64(^uint(0)>>1) {
			return val // preserve for values exceeding int range
		}
		return int(val)
	case int64:
		return int(val)
	case float32:
		return float64(val)
	default:
		return v
	}
}

// YAMLCompat controls YAML 1.1 backward compatibility behavior.
type YAMLCompat struct {
	// ConvertYAML11Booleans converts "yes"/"no"/"on"/"off" strings to booleans.
	ConvertYAML11Booleans bool
}

// DefaultYAMLCompat returns compat settings with YAML 1.1 booleans enabled.
func DefaultYAMLCompat() *YAMLCompat {
	return &YAMLCompat{ConvertYAML11Booleans: true}
}

// ConvertValue applies YAML 1.1 compatibility conversions to a string value.
// Its yes, no, on, and off table serves merge. That differs on purpose from
// the YAML 1.2 core forms in internal/yamlnode and from the YAML 1.1 set
// that toBool in internal/yamldiff accepts for nodes tagged !!bool, so the
// three tables must not be merged.
func (c *YAMLCompat) ConvertValue(s string) interface{} {
	if !c.ConvertYAML11Booleans {
		return s
	}
	switch s {
	case "yes", "Yes", "YES", "on", "On", "ON":
		return true
	case "no", "No", "NO", "off", "Off", "OFF":
		return false
	default:
		return s
	}
}

// ConvertMapValues recursively applies YAML 1.1 compatibility conversions.
func (c *YAMLCompat) ConvertMapValues(data map[string]interface{}) map[string]interface{} {
	if !c.ConvertYAML11Booleans {
		return data
	}
	for k, v := range data {
		data[k] = c.convertAny(v)
	}
	return data
}

func (c *YAMLCompat) convertAny(v interface{}) interface{} {
	switch val := v.(type) {
	case string:
		return c.ConvertValue(val)
	case uint64:
		if val > uint64(^uint(0)>>1) {
			return val
		}
		return int(val)
	case int64:
		return int(val)
	case float32:
		return float64(val)
	case map[string]interface{}:
		return c.ConvertMapValues(val)
	case map[interface{}]interface{}:
		// Other decoders produce this for maps with non-string keys (e.g.,
		// integer keys); goccy itself stringifies them.
		for k, v := range val {
			val[k] = c.convertAny(v)
		}
		return val
	case []interface{}:
		return c.convertSlice(val)
	default:
		return v
	}
}

func (c *YAMLCompat) convertSlice(s []interface{}) []interface{} {
	for i, v := range s {
		s[i] = c.convertAny(v)
	}
	return s
}

// ConvertAndUnprotect applies ConvertMapValues and
// UnprotectYAML11QuotedBools in a single walk. The parse path always
// runs the two back to back - each a full tree traversal - and their
// composition per value is order-independent to fuse: a marker-tagged
// string never matches ConvertValue's bare words (the marker prefixes
// it), so stripping the marker and skipping coercion is exactly what
// the sequential pair produced. Numeric normalization stays gated on
// ConvertYAML11Booleans, as in ConvertMapValues; marker stripping is
// unconditional, as in UnprotectYAML11QuotedBools.
func (c *YAMLCompat) ConvertAndUnprotect(data map[string]interface{}) map[string]interface{} {
	for k, v := range data {
		data[k] = c.convertAndUnprotectAny(v)
	}
	return data
}

func (c *YAMLCompat) convertAndUnprotectAny(v interface{}) interface{} {
	return c.convertAndUnprotectWith(v, c.ConvertYAML11Booleans)
}

// ConvertGeneric applies to v, a generic value goccy decoded from a
// body that ParseFirstBody tagged, the conversion DecodeGeneric applies
// when its intsAlways is set. It strips the quoted-boolean markers, reads
// yes, no, on, and off as booleans when c converts YAML 1.1 booleans,
// turns int-sized integers into int, and widens float32 to float64. It
// changes maps and slices in place and returns the converted value.
//
// v can be a value a caller's target held before the decode, which can
// hold itself, so the walk enters each map and slice once.
func (c *YAMLCompat) ConvertGeneric(v any) any {
	w := genericWalk{c: c, seen: make(map[genericRef]bool)}
	return w.convert(v)
}

// genericRef identifies a map or a slice by where its data lives, with a
// slice's length, since two slices of one array can differ in length.
type genericRef struct {
	ptr uintptr
	n   int
}

// genericWalk is ConvertGeneric's walk. seen holds every map and slice
// it has entered.
type genericWalk struct {
	c    *YAMLCompat
	seen map[genericRef]bool
}

// enter reports whether the walk has not yet entered the map or slice v,
// and records it.
func (w *genericWalk) enter(v any, n int) bool {
	ref := genericRef{ptr: reflect.ValueOf(v).Pointer(), n: n}
	if w.seen[ref] {
		return false
	}
	w.seen[ref] = true
	return true
}

func (w *genericWalk) convert(v any) any {
	switch val := v.(type) {
	case map[string]interface{}:
		if w.enter(val, 0) {
			for k, item := range val {
				val[k] = w.convert(item)
			}
		}
		return val
	case map[interface{}]interface{}:
		if w.enter(val, 0) {
			for k, item := range val {
				val[k] = w.convert(item)
			}
		}
		return val
	case []interface{}:
		if len(val) > 0 && w.enter(val, len(val)) {
			for i, item := range val {
				val[i] = w.convert(item)
			}
		}
		return val
	default:
		return w.c.convertAndUnprotectWith(v, true)
	}
}

// convertAndUnprotectWith is convertAndUnprotectAny with the numeric
// normalizations applied when ints is set. c applies them on its own
// only when it converts YAML 1.1 booleans.
func (c *YAMLCompat) convertAndUnprotectWith(v interface{}, ints bool) interface{} {
	switch val := v.(type) {
	case string:
		if strings.HasPrefix(val, yaml11QuotedBoolMarker) {
			return strings.TrimPrefix(val, yaml11QuotedBoolMarker)
		}
		if c.ConvertYAML11Booleans {
			return c.ConvertValue(val)
		}
		return val
	case uint64, int64, float32:
		if !ints {
			return val
		}
		return normalizeNumber(val)
	case map[string]interface{}:
		for k, item := range val {
			val[k] = c.convertAndUnprotectWith(item, ints)
		}
		return val
	case map[interface{}]interface{}:
		for k, item := range val {
			val[k] = c.convertAndUnprotectWith(item, ints)
		}
		return val
	case []interface{}:
		for i, item := range val {
			val[i] = c.convertAndUnprotectWith(item, ints)
		}
		return val
	default:
		return v
	}
}

// normalizeNumber applies the compat pass's numeric normalizations:
// int-sized integers become int, float32 widens to float64, and an
// integer too large for int stays uint64 untouched.
func normalizeNumber(v interface{}) interface{} {
	switch val := v.(type) {
	case uint64:
		if val > uint64(^uint(0)>>1) {
			return val
		}
		return int(val)
	case int64:
		return int(val)
	case float32:
		return float64(val)
	default:
		return v
	}
}

// yaml11QuotedBoolMarker prefixes a decoded string value that came from
// an explicitly quoted YAML 1.1 boolean-lookalike scalar (e.g. "yes",
// 'On'). ConvertValue's coercion switch matches on the bare words only,
// so a tagged value falls through untouched; UnprotectYAML11QuotedBools
// removes the prefix afterward, unconditionally, restoring the original
// string. U+E0DA is a Private Use Area code point that cannot appear in
// hand-authored deployment YAML.
const yaml11QuotedBoolMarker = "\uE0DA"

// Yaml11BoolLookalikeWords are the exact-cased tokens ConvertValue
// coerces to a boolean. Quoting one of them (single or double) is an
// author's explicit request, honored by both spruce and YAML 1.1, to
// keep the value a string -- that request must survive the compat
// coercion pass below.
var Yaml11BoolLookalikeWords = map[string]bool{
	"yes": true, "Yes": true, "YES": true,
	"no": true, "No": true, "NO": true,
	"on": true, "On": true, "ON": true,
	"off": true, "Off": true, "OFF": true,
}

// quotedBoolTagger is an ast.Visitor that mutates the Value of every
// explicitly-quoted (single- or double-quoted) *ast.StringNode, and of
// every literal or folded block scalar, whose content is a YAML 1.1
// boolean-lookalike word, prefixing it with yaml11QuotedBoolMarker.
// spruce's YAML library never resolves a quoted or block scalar, so a
// block scalar holding only yes stays the word. Plain (unquoted) scalars
// are left untouched. So are mapping keys, which the compat pass never
// coerces, so a marker on a key would never come off.
type quotedBoolTagger struct{}

func (quotedBoolTagger) Visit(n ast.Node) ast.Visitor {
	if n == nil {
		return nil
	}
	if mv, ok := n.(*ast.MappingValueNode); ok {
		ast.Walk(quotedBoolTagger{}, mv.Value)
		return nil
	}
	switch sn := n.(type) {
	case *ast.StringNode:
		if sn.Token != nil && IsQuotedScalarToken(sn.Token.Type) && Yaml11BoolLookalikeWords[sn.Value] {
			sn.Value = yaml11QuotedBoolMarker + sn.Value
		}
	case *ast.LiteralNode:
		if sn.Value != nil && Yaml11BoolLookalikeWords[sn.Value.Value] {
			sn.Value.Value = yaml11QuotedBoolMarker + sn.Value.Value
		}
		// Its content is the node marked above, which the walk must not
		// reach again.
		return nil
	}
	return quotedBoolTagger{}
}

// PlainBoolsAsBoolNodes returns n, a body ParseFirstBody returned, with
// every plain scalar that YAMLCompat reads as a YAML 1.1 boolean, such as
// yes or Off, replaced by a boolean node that keeps the scalar's token.
// It rewrites n in place, and mapping keys stay as they are. goccy then
// decodes those scalars into a bool without a hook, and so reports any
// other scalar a bool cannot hold with that scalar's position.
func PlainBoolsAsBoolNodes(n ast.Node) ast.Node {
	switch v := n.(type) {
	case *ast.StringNode:
		if v.Token != nil && !IsQuotedScalarToken(v.Token.Type) {
			if b, ok := DefaultYAMLCompat().ConvertValue(v.Value).(bool); ok {
				return &ast.BoolNode{BaseNode: &ast.BaseNode{}, Token: v.Token, Value: b}
			}
		}
	case *ast.TagNode:
		v.Value = PlainBoolsAsBoolNodes(v.Value)
	case *ast.MappingValueNode:
		v.Value = PlainBoolsAsBoolNodes(v.Value)
	case *ast.MappingNode:
		for _, item := range v.Values {
			PlainBoolsAsBoolNodes(item)
		}
	case *ast.SequenceNode:
		for i, item := range v.Values {
			v.Values[i] = PlainBoolsAsBoolNodes(item)
		}
	}
	return n
}

// IsQuotedScalarToken reports whether t is a single- or double-quoted
// scalar token.
func IsQuotedScalarToken(t token.Type) bool {
	return t == token.SingleQuoteType || t == token.DoubleQuoteType
}

// ParseYAML11CompatAware parses data the same way yaml.Unmarshal(data,
// &interface{}{}) would, except every explicitly-quoted YAML 1.1
// boolean-lookalike scalar ("yes", 'On', "OFF", ...) is protected from
// YAMLCompat's later yes/no/on/off -> bool coercion. Unquoted
// occurrences of those words decode as plain strings exactly as before,
// so ConvertMapValues keeps converting them.
//
// Quoting information only exists at the token level -- once a document
// is decoded into interface{} values, a quoted and an unquoted "yes"
// are indistinguishable Go strings. This parses data into goccy's AST,
// tags quoted matches using the AST's own token type (not a text-level
// guess), then decodes the (locally mutated) AST back into the same
// generic shape a direct yaml.Unmarshal call would produce.
//
// Callers must run the result through UnprotectYAML11QuotedBools after
// applying YAMLCompat, unconditionally, so a tagged value that YAMLCompat
// left alone (e.g. compat disabled) still comes out as the original
// word rather than the internal marker-prefixed string.
//
// Library callers pass raw input through FirstMergeDocument first, since
// goccy's parser stalls on nesting that function fails cheaply.
func ParseYAML11CompatAware(data []byte) (interface{}, error) {
	body, err := ParseFirstBody(data, true)
	if err != nil || body == nil {
		return nil, err
	}
	var result interface{}
	if err := DecodeBody(body, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// ParseFirstBody parses data and returns the root node of its first
// document with every alias expanded, or nil when data holds no
// document. It fails a document with an alias inside the collection its
// anchor names, and one that expands past maxExpandedNodes. When tag is
// set, every quoted YAML 1.1 boolean-lookalike scalar carries
// yaml11QuotedBoolMarker, so a later YAMLCompat pass leaves it a string,
// and a caller that sets tag must strip the marker from every string it
// decodes. ParseYAML11CompatAware tags, and so does pkg/yaml's typed
// route when its target can hold a generic value.
func ParseFirstBody(data []byte, tag bool) (ast.Node, error) {
	file, err := yamlnode.ParseBytes(data, 0)
	if err != nil {
		return nil, err
	}
	body := yamlnode.FirstBody(file)
	if body == nil {
		return nil, nil
	}
	return PrepareBody(body, tag)
}

// PrepareBody is the part of ParseFirstBody that follows the parse. It
// takes body, the root node of a document, which may come from goccy's
// parser or be built by hand, and returns it ready for DecodeBody. It
// fails a body with an alias inside the collection its anchor names, or
// with an invalid tag, resolves every scalar with an explicit core tag as
// spruce does (see ResolveTags), marks the quoted YAML 1.1
// boolean-lookalike scalars when tag is set, and expands every alias. It
// rewrites body in place, so a caller passes a body that nothing else
// holds.
func PrepareBody(body ast.Node, tag bool) (ast.Node, error) {
	if err := CheckAnchors(body); err != nil {
		return nil, err
	}

	// Tags resolve before the tagger runs, so a scalar !!str or !!binary
	// makes reads as a quoted string, which no coercion turns into a
	// boolean, and before expansion, so an alias copies the resolved
	// scalar.
	body, err := ResolveTags(body)
	if err != nil {
		return nil, err
	}

	if tag {
		ast.Walk(quotedBoolTagger{}, body)
	}

	// The tagger runs first, since expansion shares one subtree among
	// every alias to it, and the tagger must mark each scalar only once.
	return ExpandAliases(body)
}

// DecodeFirst decodes the first document of data into v the way goccy's
// Unmarshal does, after the steps ParseFirstBody applies with tag unset.
// So it fails an alias inside its own anchor's collection, resolves
// explicit tags as spruce does, and expands aliases, but it applies no
// YAML 1.1 boolean coercion. v is left as it is when data holds no
// document. It suits a typed target such as a go-patch op definition,
// whose fields hold their text. A value that a merge reads goes through
// DecodeFirstGeneric instead.
func DecodeFirst(data []byte, v any) error {
	body, err := ParseFirstBody(data, false)
	if err != nil || body == nil {
		return err
	}
	return DecodeBody(body, v)
}

// DecodeFirstGeneric decodes the first document of data into the generic
// values a merge reads, through the same DecodeGenericBody step that
// DecodeGeneric takes with the default YAML 1.1 settings. So a plain yes,
// no, on, or off becomes a boolean, while a quoted word, a !!str word,
// and a block scalar stay strings, just as they do in a merge input.
// Unlike DecodeGeneric, it applies no yamlprep rewrites, which leaves
// them to a caller that wants them. A nil value with a nil error means
// data holds no document, or a null one. The (( load )) operator and
// go-patch ops files read their values through it.
func DecodeFirstGeneric(data []byte) (any, error) {
	body, err := ParseFirstBody(data, true)
	if err != nil {
		return nil, err
	}
	return DecodeGenericBody(body, DefaultYAMLCompat(), false)
}

// DecodeBody decodes body, a node ParseFirstBody returned, into v with
// goccy's NodeToValue and opts.
func DecodeBody(body ast.Node, v any, opts ...yaml.DecodeOption) error {
	if err := yaml.NodeToValue(body, v, opts...); err != nil {
		if errors.Is(err, yaml.ErrExceededMaxDepth) {
			// Expansion can nest an aliased value deeper than the
			// document's own text, past what FirstMergeDocument checks.
			return &MaxRecursionError{cause: err}
		}
		return err
	}
	return nil
}

// DecodeGeneric decodes doc into the generic values a merge reads, which
// are map[string]any for a mapping, []any for a sequence, and Go scalars
// for the rest. doc is a first document that FirstMergeDocument returned,
// and the caller may rewrite it in between, as the engine's control-flow
// expander does.
//
// It applies yamlprep.Prepare, parses and decodes the way
// ParseYAML11CompatAware does, and then converts the result and strips
// the quoted-boolean markers with c. c turns int-sized integers into int
// only when it converts YAML 1.1 booleans, and intsAlways turns them
// into int either way. A nil value with a nil error means doc holds no
// document, or a null one. Errors come back as ParseYAML11CompatAware
// returns them.
func DecodeGeneric(doc []byte, c *YAMLCompat, intsAlways bool) (any, error) {
	body, err := ParseMergeBody(doc, true)
	if err != nil {
		return nil, err
	}
	return DecodeGenericBody(body, c, intsAlways)
}

// ParseMergeBody parses doc, a first document that FirstMergeDocument
// returned, into the body ParseFirstBody describes, with tag as it says.
// It is the one place that applies graft's goccy input workarounds before
// a parse, which are the bare "-" terminator rewrite, quoting of the
// <<<: inject key, and quoting of {{...}} template placeholders so they
// read as strings rather than as flow mappings used as keys (see
// internal/yamlprep). Every decode route parses through it.
//
// When tag is set, quoted YAML 1.1 boolean-lookalike scalars ("yes",
// 'On', "OFF", ...) are tagged during the parse so a later conversion
// skips them, matching spruce, where quoting is an explicit request to
// keep the value a string.
func ParseMergeBody(doc []byte, tag bool) (ast.Node, error) {
	doc, _ = yamlprep.Prepare(doc)
	return ParseFirstBody(doc, tag)
}

// DecodeGenericBody is the part of DecodeGeneric that follows the
// parse. body is what ParseFirstBody or PrepareBody returned with tag
// set, and nil for a document that holds nothing. It decodes body and
// converts the result as DecodeGeneric does.
func DecodeGenericBody(body ast.Node, c *YAMLCompat, intsAlways bool, opts ...yaml.DecodeOption) (any, error) {
	var v any
	if body != nil {
		if err := DecodeBody(body, &v, opts...); err != nil {
			return nil, err
		}
	}
	return c.convertAndUnprotectWith(v, intsAlways || c.ConvertYAML11Booleans), nil
}

// maxExpandedNodes caps the nodes a document holds once every alias in
// it is expanded. A billion-laughs document of ten lines expands to ten
// billion nodes, so without a cap expansion would exhaust memory. The
// cap keeps the worst document it accepts inside the merge's memory
// budget of about 300 MB. Of the documents we measured just under it, a
// 200 KB file of aliases peaked highest, at 269 MB RSS when merged, and
// a 5 KB one peaked at 244 MB. The cf-deployment manifest, among the
// largest real merge inputs, holds under 10,000 nodes, so the cap leaves
// a margin of forty times. spruce's yaml.v2 has no rule that rejects
// such a document, and it decodes one of five million nodes.
const maxExpandedNodes = 400_000

// ExpandAliases returns body with every alias replaced by the node its
// anchor names and every anchor replaced by the node it wraps, so
// yaml.NodeToValue never resolves an alias itself. goccy registers
// anchor names in a map that every nested value shares, and it decodes
// an alias to a name registered inside an enclosing anchored value as
// null, where yaml.v3 and spruce read the value the name is bound to.
//
// A name binds to its latest definition in document order, as in
// anchorWalk. Each alias takes the bound node itself rather than a deep
// copy. That is safe because nothing changes the tree after expansion,
// and goccy decodes a fresh value each time it meets a node. It fails a
// document that expands past maxExpandedNodes with the recursion text.
// CheckAnchors must pass first, since an alias inside its own anchor's
// value is left in place.
func ExpandAliases(body ast.Node) (ast.Node, error) {
	x := &aliasExpander{bound: make(map[string]*expandedAnchor)}
	n, _, err := x.expand(body)
	return n, err
}

// expandedAnchor is an anchor's value once its own aliases are expanded,
// and the number of nodes in it. node stays nil while the walk is inside
// the anchor's value.
type expandedAnchor struct {
	node ast.Node
	size int
}

type aliasExpander struct {
	bound map[string]*expandedAnchor // each name's latest definition
}

// expand rewrites n in place and returns the node to put in its slot,
// with the number of nodes it holds once expanded.
func (x *aliasExpander) expand(n ast.Node) (ast.Node, int, error) {
	switch v := n.(type) {
	case *ast.AnchorNode:
		e := &expandedAnchor{}
		x.bound[v.Name.GetToken().Value] = e
		var err error
		if e.node, e.size, err = x.expand(v.Value); err != nil {
			return nil, 0, err
		}
		return e.node, e.size, nil
	case *ast.AliasNode:
		if e := x.bound[v.Value.GetToken().Value]; e != nil && e.node != nil {
			return e.node, e.size, nil
		}
		return n, 1, nil
	}
	c := &expandCount{x: x, size: 1}
	if err := c.children(n); err != nil {
		return nil, 0, err
	}
	return n, c.size, nil
}

// expandCount expands one node's children and counts the node with
// everything under it. Every size it adds is at most maxExpandedNodes,
// so no sum can overflow.
type expandCount struct {
	x    *aliasExpander
	size int
}

func (c *expandCount) add(child ast.Node) (ast.Node, error) {
	n, size, err := c.x.expand(child)
	if err != nil {
		return nil, err
	}
	if c.size += size; c.size > maxExpandedNodes {
		return nil, &MaxRecursionError{cause: fmt.Errorf("aliases expand to more than %d nodes", maxExpandedNodes)}
	}
	return n, nil
}

// children puts the expansion of each of n's children in its slot.
func (c *expandCount) children(n ast.Node) error {
	var err error
	switch v := n.(type) {
	case *ast.TagNode:
		v.Value, err = c.add(v.Value)
	case *ast.MappingKeyNode:
		v.Value, err = c.add(v.Value)
	case *ast.MappingValueNode:
		var key ast.Node
		if key, err = c.add(v.Key); err == nil {
			v.Key = asMapKey(key, v.Key)
			v.Value, err = c.add(v.Value)
		}
	case *ast.MappingNode:
		// Each item is a *ast.MappingValueNode, which expand rewrites in
		// place and returns unchanged, so the slot needs no update.
		for _, item := range v.Values {
			if _, err = c.add(item); err != nil {
				return err
			}
		}
	case *ast.SequenceNode:
		for i, item := range v.Values {
			if v.Values[i], err = c.add(item); err != nil {
				return err
			}
		}
	}
	return err
}

// asMapKey returns key, the expansion of was, as a mapping key. An
// aliased or anchored collection is no MapKeyNode, so it goes inside an
// explicit key node, which goccy decodes to the collection, as it
// decoded the alias or anchor.
func asMapKey(key ast.Node, was ast.MapKeyNode) ast.MapKeyNode {
	if k, ok := key.(ast.MapKeyNode); ok {
		return k
	}
	return &ast.MappingKeyNode{BaseNode: &ast.BaseNode{}, Start: was.GetToken(), Value: key}
}

// MaxRecursionMessage is the text CheckForCycles gives for a merged tree
// nested past its limit. spruce gives the same text for any merge input
// nested past 4,096 levels.
const MaxRecursionMessage = "Hit max recursion depth. You seem to have a self-referencing dataset"

// MaxRecursionError reports merge input nested too deep to parse. It
// reads as MaxRecursionMessage and wraps the depth error underneath.
type MaxRecursionError struct {
	cause error
}

func (e *MaxRecursionError) Error() string { return MaxRecursionMessage }

func (e *MaxRecursionError) Unwrap() error { return e.cause }

// FirstMergeDocument returns the first document of data, raw YAML bytes
// about to be merged, since the merge reads no other, as spruce's does.
// goccy parses every document it is given, so a syntax error or deep
// nesting in a later document would otherwise fail the merge. It strips
// a leading UTF-8 byte order mark first, as spruce does, since goccy
// reads one as text. The result is a prefix of the rest of data, with
// every CRLF and lone CR turned into an LF, and it is all of that rest
// when nothing ends the first document. See yamlnode.FirstDocument.
//
// It fails a first document that nests deeper than 10,000 levels, and
// it runs before anything else tokenizes data, because goccy's parser
// slows down faster than linearly with depth, and 20,000 unclosed
// brackets cost it hundreds of megabytes before it fails. The error
// reads as the text a merge gives for a tree nested past 4,096 levels,
// so every over-deep merge fails with one message whatever its depth.
//
// Content after the "..." that ends the first document needs a "---"
// before it, and spruce fails a file without one. The merge must not
// drop that content, so FirstMergeDocument fails it with a parse error
// that carries yaml.v3's message and line.
func FirstMergeDocument(data []byte) ([]byte, error) {
	first, err := yamlnode.FirstDocument(yamlnode.TrimBOM(data))
	switch {
	case yamlnode.IsDepthError(err):
		return nil, &MaxRecursionError{cause: err}
	case err != nil:
		return nil, err
	}
	return first, nil
}

// CheckSelfContainingAnchors fails data, YAML bytes about to be decoded,
// when an alias in its first document sits inside the collection its
// anchor names, with the message spruce gives. goccy decodes such an
// alias as null, so without the check the value would quietly vanish. A
// syntax error passes the check, so the caller's own decode reports it.
// Library callers pass raw input through FirstMergeDocument first, since
// goccy's parser stalls on nesting that function fails cheaply.
func CheckSelfContainingAnchors(data []byte) error {
	if file, err := yamlnode.ParseBytes(data, 0); err == nil {
		if body := yamlnode.FirstBody(file); body != nil {
			return CheckAnchors(body)
		}
	}
	return nil
}

// CheckAnchors walks body, a document's root node, for an alias that
// sits inside the collection its anchor names.
func CheckAnchors(body ast.Node) error {
	w := &anchorWalk{
		bound: make(map[string]*ast.AnchorNode),
		open:  make(map[*ast.AnchorNode]bool),
	}
	return w.walk(body)
}

// anchorWalk tracks anchors the way yaml.v3 resolves aliases. A name
// binds to its latest definition in document order, from the moment the
// definition starts, so an alias inside its own anchor's collection
// finds that collection still open on the walk's path.
type anchorWalk struct {
	bound map[string]*ast.AnchorNode // each name's latest definition
	open  map[*ast.AnchorNode]bool   // the definitions the walk is inside
}

func (w *anchorWalk) walk(n ast.Node) error {
	switch x := n.(type) {
	case *ast.AnchorNode:
		return w.anchor(x)
	case *ast.AliasNode:
		return w.alias(x)
	case *ast.TagNode:
		if err := yamlnode.TagError(x.Start, 0, libyamlLine, yamlnode.MergeTags); err != nil {
			return err
		}
		return w.walk(x.Value)
	case *ast.MappingKeyNode:
		return w.walk(x.Value)
	case *ast.MappingValueNode:
		if err := w.walk(x.Key); err != nil {
			return err
		}
		return w.walk(x.Value)
	case *ast.MappingNode:
		for _, v := range x.Values {
			if err := w.walk(v); err != nil {
				return err
			}
		}
	case *ast.SequenceNode:
		for _, v := range x.Values {
			if err := w.walk(v); err != nil {
				return err
			}
		}
	}
	return nil
}

func (w *anchorWalk) anchor(a *ast.AnchorNode) error {
	if err := yamlnode.PropertyNameError(a.Start, a.Name.GetToken(), 0, libyamlLine); err != nil {
		return err
	}
	w.bound[a.Name.GetToken().Value] = a
	w.open[a] = true
	err := w.walk(a.Value)
	delete(w.open, a)
	return err
}

func (w *anchorWalk) alias(a *ast.AliasNode) error {
	if err := yamlnode.PropertyNameError(a.Start, a.Value.GetToken(), 0, libyamlLine); err != nil {
		return err
	}
	name := a.Value.GetToken().Value
	if w.open[w.bound[name]] {
		return &SelfContainingAnchorError{Name: name, Line: a.Start.Position.Line, Column: a.Start.Position.Column}
	}
	return nil
}

// SelfContainingAnchorError reports an alias that sits inside the
// collection its anchor names. It reads as spruce's message, which names
// the anchor, and it carries the alias's position, with Line and Column
// counted from 1, for callers that point at the input.
type SelfContainingAnchorError struct {
	Name   string
	Line   int
	Column int
}

func (e *SelfContainingAnchorError) Error() string {
	return fmt.Sprintf("anchor '%s' value contains itself", e.Name)
}

// libyamlLine is the line spruce's YAML library reports for an error on
// line, numbered from 1. It counts from 0, and a line of 0 is left out.
func libyamlLine(line int) int {
	return line - 1
}

// UnprotectYAML11QuotedBools reverses the tagging ParseYAML11CompatAware
// applied: it walks v, stripping yaml11QuotedBoolMarker from any string
// value that still carries it, restoring the original bare word. Mutates
// map and slice values in place; returns the (possibly replaced) root
// value, mirroring ConvertMapValues' in-place style.
func UnprotectYAML11QuotedBools(v interface{}) interface{} {
	switch val := v.(type) {
	case string:
		s, _ := UnprotectString(val)
		return s
	case map[string]interface{}:
		for k, item := range val {
			val[k] = UnprotectYAML11QuotedBools(item)
		}
		return val
	case map[interface{}]interface{}:
		for k, item := range val {
			val[k] = UnprotectYAML11QuotedBools(item)
		}
		return val
	case []interface{}:
		for i, item := range val {
			val[i] = UnprotectYAML11QuotedBools(item)
		}
		return val
	default:
		return v
	}
}

// UnprotectString returns s without the yaml11QuotedBoolMarker that
// ParseFirstBody puts on a quoted YAML 1.1 boolean-lookalike scalar, and
// reports whether s carried it.
func UnprotectString(s string) (string, bool) {
	if strings.HasPrefix(s, yaml11QuotedBoolMarker) {
		return strings.TrimPrefix(s, yaml11QuotedBoolMarker), true
	}
	return s, false
}
