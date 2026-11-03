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
// Ported from go.yaml.in/yaml/v3 v3.0.4 decode.go and resolve.go and
// modified for graft: only decoding into an interface{} is kept, the
// value half of resolve is merged in, and the long switches are split
// into helpers to meet graft's lint rules, with the same decisions in
// the same order.

package yamlnode

import (
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
)

// Decode returns what yaml.v3's Node.Decode(&v) stores in an interface{}.
func Decode(n *Node) (result interface{}, err error) {
	if n == nil {
		return nil, nil
	}
	d := &decoder{aliases: make(map[*Node]bool)}
	defer func() {
		if r := recover(); r != nil {
			f, ok := r.(decodeFailure)
			if !ok {
				panic(r)
			}
			result, err = nil, f.err
		}
	}()
	out := &slot{kind: slotInterface}
	d.unmarshal(n, out)
	if len(d.terrors) > 0 {
		return nil, errors.New("yaml: unmarshal errors:\n  " + strings.Join(d.terrors, "\n  "))
	}
	return out.iface, nil
}

// decodeFailure carries an error raised with failf up to Decode.
type decodeFailure struct {
	err error
}

func failf(format string, args ...interface{}) {
	panic(decodeFailure{fmt.Errorf("yaml: "+format, args...)})
}

// slotKind says what a slot stands for in yaml.v3's decoder: an
// interface{} value, a string, or an existing map being filled.
type slotKind uint8

const (
	slotInterface slotKind = iota
	slotString
	slotMap
)

// slot is the destination of one decode, in place of the reflect.Value
// yaml.v3 passes around.
type slot struct {
	kind  slotKind
	iface interface{}
	str   string
	sink  mapSink
}

func (s *slot) typeName() string {
	switch s.kind {
	case slotString:
		return "string"
	case slotMap:
		return s.sink.typeName()
	default:
		return "interface {}"
	}
}

// value returns the decoded value as yaml.v3's k.Interface() does.
func (s *slot) value() interface{} {
	if s.kind == slotString {
		return s.str
	}
	return s.iface
}

// mapSink is a map yaml.v3 decodes into, string keyed or general.
type mapSink struct {
	s map[string]interface{}
	g map[interface{}]interface{}
}

func (m mapSink) typeName() string {
	if m.s != nil {
		return fmt.Sprintf("%T", m.s)
	}
	return fmt.Sprintf("%T", m.g)
}

func (m mapSink) newKey() *slot {
	if m.s != nil {
		return &slot{kind: slotString}
	}
	return &slot{kind: slotInterface}
}

func (m mapSink) has(k *slot) bool {
	if m.s != nil {
		_, ok := m.s[k.str]
		return ok
	}
	_, ok := m.g[k.iface]
	return ok
}

func (m mapSink) set(k *slot, v interface{}) {
	if m.s != nil {
		m.s[k.str] = v
		return
	}
	m.g[k.iface] = v
}

type decoder struct {
	terrors      []string
	aliases      map[*Node]bool
	aliasCount   int
	aliasDepth   int
	decodeCount  int
	mergedFields map[interface{}]bool
}

func (d *decoder) terror(n *Node, tag string, outType string) {
	if n.Tag != "" {
		tag = n.Tag
	}
	value := n.Value
	if tag != TagSeq && tag != TagMap {
		if len(value) > 10 {
			value = " `" + value[:7] + "...`"
		} else {
			value = " `" + value + "`"
		}
	}
	d.terrors = append(d.terrors, fmt.Sprintf("line %d: cannot unmarshal %s%s into %s", n.Line, ShortTag(tag), value, outType))
}

const (
	// 400,000 decode operations is ~500kb of dense object declarations, or
	// ~5kb of dense object declarations with 10000% alias expansion.
	aliasRatioRangeLow = 400000

	// 4,000,000 decode operations is ~5MB of dense object declarations, or
	// ~4.5MB of dense object declarations with 10% alias expansion.
	aliasRatioRangeHigh = 4000000

	// aliasRatioRange is the range over which we scale allowed alias ratios.
	aliasRatioRange = float64(aliasRatioRangeHigh - aliasRatioRangeLow)
)

func allowedAliasRatio(decodeCount int) float64 {
	switch {
	case decodeCount <= aliasRatioRangeLow:
		// Allow 99% to come from alias expansion for small-to-medium documents.
		return 0.99
	case decodeCount >= aliasRatioRangeHigh:
		// Allow 10% to come from alias expansion for very large documents.
		return 0.10
	default:
		// Scale smoothly from 99% down to 10% over the range.
		return 0.99 - 0.89*(float64(decodeCount-aliasRatioRangeLow)/aliasRatioRange)
	}
}

// unmarshal is yaml.v3's decoder.unmarshal for the targets graft uses.
// yaml.v3's prepare step only matters for pointers and Unmarshaler
// implementations, which an interface{}, a string, or a map never is.
func (d *decoder) unmarshal(n *Node, out *slot) bool {
	d.decodeCount++
	if d.aliasDepth > 0 {
		d.aliasCount++
	}
	if d.aliasCount > 100 && d.decodeCount > 1000 && float64(d.aliasCount)/float64(d.decodeCount) > allowedAliasRatio(d.decodeCount) {
		failf("document contains excessive aliasing")
	}
	switch n.Kind {
	case DocumentNode:
		return d.document(n, out)
	case AliasNode:
		return d.alias(n, out)
	case ScalarNode:
		return d.scalar(n, out)
	case MappingNode:
		return d.mapping(n, out)
	case SequenceNode:
		return d.sequence(n, out)
	default:
		return d.unknownKind(n, out)
	}
}

func (d *decoder) unknownKind(n *Node, out *slot) bool {
	if n.Kind == 0 && isZeroNode(n) {
		return d.null(out)
	}
	failf("cannot decode node with unknown kind %d", n.Kind)
	return false
}

func isZeroNode(n *Node) bool {
	return n.Kind == 0 && n.Tag == "" && n.Value == "" && n.Anchor == "" && n.Alias == nil &&
		n.Content == nil && n.HeadComment == "" && n.LineComment == "" && n.FootComment == "" &&
		n.Line == 0 && n.Column == 0
}

func (d *decoder) document(n *Node, out *slot) bool {
	if len(n.Content) == 1 {
		d.unmarshal(n.Content[0], out)
		return true
	}
	return false
}

// containsItself is yaml.v3's message for an alias that sits inside the
// node its anchor names. The decoder raises it when a decode reaches such
// an alias, and Parse raises it when the alias is built.
func containsItself(anchor string) string {
	return "anchor '" + anchor + "' value contains itself"
}

func (d *decoder) alias(n *Node, out *slot) bool {
	if d.aliases[n] {
		failf("%s", containsItself(n.Value))
	}
	d.aliases[n] = true
	d.aliasDepth++
	good := d.unmarshal(n.Alias, out)
	d.aliasDepth--
	delete(d.aliases, n)
	return good
}

// null stores a nil in an interface{} and fails for every other target,
// as yaml.v3 does for an addressable value.
func (d *decoder) null(out *slot) bool {
	if out.kind == slotInterface {
		out.iface = nil
		return true
	}
	return false
}

func (d *decoder) scalar(n *Node, out *slot) bool {
	tag, resolved := scalarValue(n)
	if resolved == nil {
		return d.null(out)
	}
	switch out.kind {
	case slotInterface:
		out.iface = resolved
		return true
	case slotString:
		if s, ok := resolved.(string); ok {
			out.str = s
		} else {
			out.str = n.Value
		}
		return true
	default:
		d.terror(n, tag, out.typeName())
		return false
	}
}

// scalarValue returns the tag and Go value of a scalar, decoding a
// !!binary value to its bytes as a string.
func scalarValue(n *Node) (string, interface{}) {
	if isIndicatedString(n) {
		return TagStr, n.Value
	}
	tag, resolved := resolve(n.Tag, n.Value)
	if tag == TagBinary {
		text, _ := resolved.(string)
		data, err := base64.StdEncoding.DecodeString(text)
		if err != nil {
			failf("!!binary value contains invalid base64 data")
		}
		resolved = string(data)
	}
	return tag, resolved
}

func (d *decoder) sequence(n *Node, out *slot) bool {
	if out.kind != slotInterface {
		d.terror(n, TagSeq, out.typeName())
		return false
	}
	l := len(n.Content)
	items := make([]interface{}, l)
	j := 0
	for i := 0; i < l; i++ {
		e := &slot{kind: slotInterface}
		if d.unmarshal(n.Content[i], e) {
			items[j] = e.iface
			j++
		}
	}
	out.iface = items[:j]
	return true
}

func (d *decoder) mapping(n *Node, out *slot) bool {
	if !d.uniqueKeys(n) {
		return false
	}
	var sink mapSink
	switch out.kind {
	case slotInterface:
		if isStringMap(n) {
			sink = mapSink{s: make(map[string]interface{})}
			out.iface = sink.s
		} else {
			sink = mapSink{g: make(map[interface{}]interface{})}
			out.iface = sink.g
		}
	case slotMap:
		sink = out.sink
	default:
		d.terror(n, TagMap, out.typeName())
		return false
	}

	mergedFields := d.mergedFields
	d.mergedFields = nil
	var mergeNode *Node
	for i := 0; i < len(n.Content); i += 2 {
		if isMerge(n.Content[i]) {
			mergeNode = n.Content[i+1]
			continue
		}
		d.mapEntry(sink, mergedFields, n.Content[i], n.Content[i+1])
	}
	d.mergedFields = mergedFields
	if mergeNode != nil {
		d.merge(n, mergeNode, &slot{kind: slotMap, sink: sink})
	}
	return true
}

// uniqueKeys reports whether n has no repeated keys, recording an error
// for each repeat.
func (d *decoder) uniqueKeys(n *Node) bool {
	l := len(n.Content)
	nerrs := len(d.terrors)
	for i := 0; i < l; i += 2 {
		ni := n.Content[i]
		for j := i + 2; j < l; j += 2 {
			nj := n.Content[j]
			if ni.Kind == nj.Kind && ni.Value == nj.Value {
				d.terrors = append(d.terrors, fmt.Sprintf("line %d: mapping key %#v already defined at line %d", nj.Line, nj.Value, ni.Line))
			}
		}
	}
	return len(d.terrors) == nerrs
}

// mapEntry decodes one key and value into sink, skipping a key that a
// merge already recorded.
func (d *decoder) mapEntry(sink mapSink, mergedFields map[interface{}]bool, keyNode, valueNode *Node) {
	k := sink.newKey()
	if !d.unmarshal(keyNode, k) {
		return
	}
	if mergedFields != nil {
		ki := k.value()
		if d.getPossiblyUnhashableKey(mergedFields, ki) {
			return
		}
		d.setPossiblyUnhashableKey(mergedFields, ki, true)
	}
	switch reflect.ValueOf(k.value()).Kind() {
	case reflect.Map, reflect.Slice:
		failf("invalid map key: %#v", k.value())
	default:
	}
	e := &slot{kind: slotInterface}
	if d.unmarshal(valueNode, e) || nodeShortTag(valueNode) == TagNull && !sink.has(k) {
		sink.set(k, e.iface)
	}
}

func isStringMap(n *Node) bool {
	if n.Kind != MappingNode {
		return false
	}
	for i := 0; i < len(n.Content); i += 2 {
		shortTag := nodeShortTag(n.Content[i])
		if shortTag != TagStr && shortTag != TagMerge {
			return false
		}
	}
	return true
}

func failWantMap() {
	failf("map merge requires map or sequence of maps as the value")
}

func (d *decoder) setPossiblyUnhashableKey(m map[interface{}]bool, key interface{}, value bool) {
	defer func() {
		if err := recover(); err != nil {
			failf("%v", err)
		}
	}()
	m[key] = value
}

func (d *decoder) getPossiblyUnhashableKey(m map[interface{}]bool, key interface{}) bool {
	defer func() {
		if err := recover(); err != nil {
			failf("%v", err)
		}
	}()
	return m[key]
}

func (d *decoder) merge(parent *Node, merge *Node, out *slot) {
	mergedFields := d.mergedFields
	if mergedFields == nil {
		d.mergedFields = make(map[interface{}]bool)
		for i := 0; i < len(parent.Content); i += 2 {
			k := &slot{kind: slotInterface}
			if d.unmarshal(parent.Content[i], k) {
				d.setPossiblyUnhashableKey(d.mergedFields, k.iface, true)
			}
		}
	}

	switch merge.Kind {
	case MappingNode:
		d.unmarshal(merge, out)
	case AliasNode:
		if merge.Alias != nil && merge.Alias.Kind != MappingNode {
			failWantMap()
		}
		d.unmarshal(merge, out)
	case SequenceNode:
		d.mergeSequence(merge, out)
	default:
		failWantMap()
	}

	d.mergedFields = mergedFields
}

func (d *decoder) mergeSequence(merge *Node, out *slot) {
	for i := 0; i < len(merge.Content); i++ {
		ni := merge.Content[i]
		if ni.Kind == AliasNode {
			if ni.Alias != nil && ni.Alias.Kind != MappingNode {
				failWantMap()
			}
		} else if ni.Kind != MappingNode {
			failWantMap()
		}
		d.unmarshal(ni, out)
	}
}

func isMerge(n *Node) bool {
	return n.Kind == ScalarNode && n.Value == "<<" && (n.Tag == "" || n.Tag == "!" || ShortTag(n.Tag) == TagMerge)
}

// isIndicatedString reports whether a scalar is a string by its tag.
// The model has no style bits, so a quoted or block scalar already
// carries !!str.
func isIndicatedString(n *Node) bool {
	return n.Kind == ScalarNode && ShortTag(n.Tag) == TagStr
}

// nodeShortTag is yaml.v3's Node.ShortTag.
func nodeShortTag(n *Node) string {
	if isIndicatedString(n) {
		return TagStr
	}
	if n.Tag != "" && n.Tag != "!" {
		return ShortTag(n.Tag)
	}
	switch n.Kind {
	case MappingNode:
		return TagMap
	case SequenceNode:
		return TagSeq
	case AliasNode:
		if n.Alias != nil {
			return nodeShortTag(n.Alias)
		}
	case ScalarNode:
		tag, _ := resolve("", n.Value)
		return tag
	default:
		if isZeroNode(n) {
			return TagNull
		}
	}
	return ""
}

func resolvableTag(tag string) bool {
	switch tag {
	case "", TagStr, TagBool, TagInt, TagFloat, TagNull, TagTimestamp:
		return true
	default:
		return false
	}
}

// resolve is the value half of yaml.v3's resolve: it returns the tag and
// Go value of a scalar, and fails when an explicit tag cannot hold it.
func resolve(tag string, in string) (string, interface{}) {
	tag = ShortTag(tag)
	if !resolvableTag(tag) {
		return tag, in
	}
	rtag, out := resolveValue(tag, in)
	switch tag {
	case "", rtag, TagStr, TagBinary:
		return rtag, out
	case TagFloat:
		if rtag == TagInt {
			switch v := out.(type) {
			case int64:
				return TagFloat, float64(v)
			case int:
				return TagFloat, float64(v)
			default:
			}
		}
	default:
	}
	failf("cannot decode %s `%s` as a %s", ShortTag(rtag), in, ShortTag(tag))
	return "", nil
}

// resolveHint classifies the first byte of a scalar as yaml.v3's
// resolveTable does.
func resolveHint(c byte) byte {
	switch {
	case c == '+' || c == '-':
		return 'S'
	case c >= '0' && c <= '9':
		return 'D'
	case strings.IndexByte("yYnNtTfFoO~", c) >= 0:
		return 'M'
	case c == '.':
		return '.'
	default:
		return 0
	}
}

func resolveValue(tag, in string) (string, interface{}) {
	// Any data is accepted as a !!str or !!binary. Otherwise, the prefix
	// is enough of a hint about what it might be.
	if tag == TagStr || tag == TagBinary {
		return TagStr, in
	}
	hint := byte('N')
	if in != "" {
		hint = resolveHint(in[0])
	}
	if hint == 0 {
		return TagStr, in
	}
	if rtag, v, ok := resolveSpecial(in); ok {
		return rtag, v
	}
	switch hint {
	case '.':
		if floatv, err := strconv.ParseFloat(in, 64); err == nil {
			return TagFloat, floatv
		}
	case 'D', 'S':
		if rtag, v, ok := numericValue(tag, in); ok {
			return rtag, v
		}
	default:
	}
	return TagStr, in
}

// The lower-case spellings of the float specials.
const (
	litNaN    = ".nan"
	litInf    = ".inf"
	litNegInf = "-.inf"
)

// resolveSpecial looks up the scalars yaml.v3 resolves from a table. Its
// boolean literals are only the YAML 1.2 core forms. That differs on
// purpose from the YAML 1.1 set that toBool in internal/yamldiff accepts
// for nodes already tagged !!bool, and from the yes, no, on, and off table
// in pkg/graft that serves merge, so the three tables must not be merged.
func resolveSpecial(in string) (string, interface{}, bool) {
	switch in {
	case "true", "True", "TRUE":
		return TagBool, true, true
	case "false", "False", "FALSE":
		return TagBool, false, true
	case "", "~", "null", "Null", "NULL":
		return TagNull, nil, true
	case litNaN, ".NaN", ".NAN":
		return TagFloat, math.NaN(), true
	case litInf, ".Inf", ".INF", "+.inf", "+.Inf", "+.INF":
		return TagFloat, math.Inf(+1), true
	case litNegInf, "-.Inf", "-.INF":
		return TagFloat, math.Inf(-1), true
	case "<<":
		return TagMerge, "<<", true
	default:
		return "", nil, false
	}
}

// numericValue resolves a scalar that starts with a digit or a sign as a
// timestamp, an integer, or a float.
func numericValue(tag, in string) (string, interface{}, bool) {
	// Only try values as a timestamp if the value is unquoted or there's
	// an explicit !!timestamp tag.
	if tag == "" || tag == TagTimestamp {
		if t, ok := parseTimestamp(in); ok {
			return TagTimestamp, t, true
		}
	}
	plain := strings.ReplaceAll(in, "_", "")
	if rtag, v, ok := decimalValue(plain); ok {
		return rtag, v, true
	}
	// Octals as introduced in version 1.2 of the spec. Octals from the
	// 1.1 spec, spelled as 0777, are still decoded for compatibility.
	if v, ok := prefixedIntValue(plain, "0b", 2); ok {
		return TagInt, v, true
	}
	if v, ok := prefixedIntValue(plain, "0o", 8); ok {
		return TagInt, v, true
	}
	return "", nil, false
}

func decimalValue(plain string) (string, interface{}, bool) {
	if intv, err := strconv.ParseInt(plain, 0, 64); err == nil {
		return TagInt, intOrInt64(intv), true
	}
	if uintv, err := strconv.ParseUint(plain, 0, 64); err == nil {
		return TagInt, uintv, true
	}
	if yamlStyleFloat.MatchString(plain) {
		if floatv, err := strconv.ParseFloat(plain, 64); err == nil {
			return TagFloat, floatv, true
		}
	}
	return "", nil, false
}

func intOrInt64(v int64) interface{} {
	if v == int64(int(v)) {
		return int(v)
	}
	return v
}

// prefixedIntValue reads an integer written with a base prefix such as
// 0b or 0o, and the negative form, which yaml.v3 always returns as int.
func prefixedIntValue(plain, prefix string, base int) (interface{}, bool) {
	if rest, ok := strings.CutPrefix(plain, prefix); ok {
		if intv, err := strconv.ParseInt(rest, base, 64); err == nil {
			return intOrInt64(intv), true
		}
		if uintv, err := strconv.ParseUint(rest, base, 64); err == nil {
			return uintv, true
		}
		return nil, false
	}
	if rest, ok := strings.CutPrefix(plain, "-"+prefix); ok {
		if intv, err := strconv.ParseInt("-"+rest, base, 64); err == nil {
			return int(intv), true
		}
	}
	return nil, false
}
