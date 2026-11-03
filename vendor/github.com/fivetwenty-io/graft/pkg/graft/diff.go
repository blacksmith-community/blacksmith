package graft

import (
	stdfmt "fmt"
	"regexp"
	"sort"
	"strings"

	fmt "github.com/fivetwenty-io/graft/internal/utils/ansi"
)

func pad1(pad, s string) string {
	return strings.TrimSpace(indent(pad, s))
}

func indent(pad, s string) string {
	re := regexp.MustCompile(`(?m)^`)
	return re.ReplaceAllString(s, pad)
}

// style says how a diff is rendered. Color is applied only here, at render
// time, because Diff stores raw values and keys.
//
// Only graft's own template text is markup, and the color flag decides
// whether that markup becomes escapes or is stripped. Keys and values appear
// exactly as written. The verbatim flag marks the plain rendering, which
// also strips escapes from a Diffable that graft doesn't own.
type style struct {
	color    bool
	verbatim bool
}

var plainStyle = style{verbatim: true}

func toggleStyle() style {
	return style{color: fmt.IsColorEnabled()}
}

// text prepares a stored key or value for display. User text is never read
// as markup, so it comes back unchanged.
func (st style) text(x string) string {
	return x
}

// format expands a template whose markup is graft's own. The arguments are
// formatted in after the markup is processed, so they stay literal.
func (st style) format(template string, args ...interface{}) string {
	return fmt.SprintfTemplate(st.color, template, args...)
}

// childKey builds the key a nested diff renders under.
func (st style) childKey(format, key, k string) string {
	return stdfmt.Sprintf(format, key, k)
}

func (st style) yamlstring(f string, x Diffable) string {
	const defaultPad = "    "
	s, _ := MarshalYAML(x.Value())
	return st.format(f, indent(defaultPad, strings.TrimSuffix(string(s), "\n")))
}

// sorted orders keys as they display.
func (st style) sorted(m map[string]Diffable) []string {
	kk := make([]string, 0, len(m))
	for k := range m {
		kk = append(kk, k)
	}
	sort.Slice(kk, func(i, j int) bool { return st.text(kk[i]) < st.text(kk[j]) })
	return kk
}

func yamlmarshal(x interface{}) string {
	s, _ := MarshalYAML(x)
	return string(s)
}

func sortkeys(m map[string]Diffable) []string {
	return plainStyle.sorted(m)
}

// Type represents the type category of a YAML value.
type Type int

// Type constants for YAML value classification.
const (
	// Scalar represents a scalar value (string, number, boolean, null).
	Scalar Type = iota
	// Map represents a map/object value.
	Map
	// SimpleList represents a list of non-keyed items.
	SimpleList
	// KeyedList represents a list of keyed items (maps with name/id/key).
	KeyedList
)

func keyed(l []interface{}) string {
	for _, v := range l {
		if typeof(v) != Map {
			return ""
		}
	}

KEYSEARCH:
	for _, k := range []string{"name", "id", "key"} {
		for _, v := range l {
			o, ok := v.(map[string]interface{})
			if !ok {
				continue KEYSEARCH
			}
			if _, ok := o[k]; !ok {
				continue KEYSEARCH
			}
		}

		return k
	}

	return ""
}

func mapify(l []interface{}, key string) map[string]interface{} {
	m := make(map[string]interface{})

	for _, v := range l {
		if typeof(v) != Map {
			return nil
		}
		o, ok := v.(map[string]interface{})
		if !ok {
			return nil
		}
		kv, ok := o[key]
		if !ok {
			return nil
		}
		ks := stdfmt.Sprintf("%v", kv)
		m[ks] = v
	}

	return m
}

func (t Type) String() string {
	switch t {
	case Scalar:
		return "scalar"
	case Map:
		return valueTypeMap
	case SimpleList:
		return "simple list"
	case KeyedList:
		return "keyed list"
	default:
		return "unknown type"
	}
}

func typeof(x interface{}) Type {
	switch v := x.(type) {
	case map[string]interface{}:
		return Map
	case []interface{}:
		if keyed(v) != "" {
			return KeyedList
		}
		return SimpleList

	default:
		return Scalar
	}
}

// Diffable represents a value that can be compared in a diff operation.
type Diffable interface {
	// Changed returns true if the value has changed.
	Changed() bool
	// String returns a formatted string representation of the change.
	String(key string) string
	// Value returns the current value.
	Value() interface{}
}

// derefOwn returns the value a non-nil pointer to one of graft's own Diffable
// types points at, and any other Diffable unchanged.
func derefOwn(d Diffable) Diffable {
	switch v := d.(type) {
	case *DiffNone:
		if v != nil {
			return *v
		}
	case *DiffType:
		if v != nil {
			return *v
		}
	case *DiffScalar:
		if v != nil {
			return *v
		}
	case *DiffMap:
		if v != nil {
			return *v
		}
	case *DiffList:
		if v != nil {
			return *v
		}
	}
	return d
}

// renderDiffable renders d in the given style. Only graft's own Diffable
// types, matched by exact dynamic type, take the style; any other type,
// including one that embeds a graft type and overrides String, renders
// through its own String method. Under a verbatim style that output has its
// escape sequences stripped.
func renderDiffable(d Diffable, key string, st style) string {
	d = derefOwn(d)
	switch v := d.(type) {
	case DiffNone:
		return v.render(key, st)
	case DiffType:
		return v.render(key, st)
	case DiffScalar:
		return v.render(key, st)
	case DiffMap:
		return v.render(key, st)
	case DiffList:
		return v.render(key, st)
	}
	out := d.String(key)
	if st.verbatim {
		out = fmt.StripEscapes(out)
	}
	return out
}

// PlainString renders d as d.String(key) does, but without ANSI color
// escapes. For graft's own Diffable types it reads no package-wide color
// setting, so concurrent callers can use it safely, and the key and the
// diffed values appear exactly as written, never read as color markup. A
// Diffable from outside graft renders through its own String method with
// escape sequences stripped, and that method may still read the package-wide
// color setting.
func PlainString(d Diffable, key string) string {
	return renderDiffable(d, key, plainStyle)
}

// DiffNone represents an unchanged value.
type DiffNone struct {
	Orig interface{}
}

// Changed returns false for unchanged values.
func (d DiffNone) Changed() bool {
	return false
}

// String returns an empty string for unchanged values.
func (d DiffNone) String(key string) string {
	return d.render(key, toggleStyle())
}

func (d DiffNone) render(string, style) string {
	return ""
}

// Value returns the original value.
func (d DiffNone) Value() interface{} {
	return d.Orig
}

// DiffType represents a type change between old and new values.
type DiffType struct {
	Old interface{}
	New interface{}
}

// Changed returns true if the types differ.
func (d DiffType) Changed() bool {
	return typeof(d.Old) != typeof(d.New)
}

// String returns a formatted description of the type change.
func (d DiffType) String(key string) string {
	return d.render(key, toggleStyle())
}

func (d DiffType) render(key string, st style) string {
	return st.format("  @C{%s} changed type\n    from @R{%s}\n      to @G{%s}\n\n",
		key, typeof(d.Old), typeof(d.New))
}

// Value returns nil for type changes.
func (d DiffType) Value() interface{} {
	return nil
}

// DiffScalar represents a change between two scalar values.
type DiffScalar struct {
	Old string
	New string
}

// Changed returns true if old and new scalar values differ.
func (d DiffScalar) Changed() bool {
	return d.Old != d.New
}

// String returns a formatted description of the scalar change.
func (d DiffScalar) String(key string) string {
	return d.render(key, toggleStyle())
}

func (d DiffScalar) render(key string, st style) string {
	return st.format("  @C{%s} changed value\n    from @R{%s}\n      to @G{%s}\n\n",
		key, pad1("         ", st.text(d.Old)), pad1("         ", st.text(d.New)))
}

// Value returns nil for scalar changes.
func (d DiffScalar) Value() interface{} {
	return nil
}

// DiffMap represents changes between two maps.
type DiffMap struct {
	Removed map[string]Diffable
	Added   map[string]Diffable
	Common  map[string]Diffable
}

// Changed returns true if any entries were added, removed, or modified.
func (d DiffMap) Changed() bool {
	if len(d.Removed)+len(d.Added) > 0 {
		return true
	}

	for _, x := range d.Common {
		if x.Changed() {
			return true
		}
	}
	return false
}

// String returns a formatted description of the map changes.
func (d DiffMap) String(key string) string {
	return d.render(key, toggleStyle())
}

func (d DiffMap) render(key string, st style) string {
	s := ""

	for _, k := range st.sorted(d.Added) {
		v := d.Added[k]
		s += st.format("  @C{%s.%s} added\n", key, k)
		s += st.yamlstring("@G{%s}\n\n", v)
	}
	for _, k := range st.sorted(d.Removed) {
		v := d.Removed[k]
		s += st.format("  @C{%s.%s} removed\n", key, k)
		s += st.yamlstring("@R{%s}\n\n", v)
	}

	for _, k := range st.sorted(d.Common) {
		v := d.Common[k]
		if v.Changed() {
			s += renderDiffable(v, st.childKey("%s.%s", key, k), st)
		}
	}
	return s
}

// Value returns nil for map changes.
func (d DiffMap) Value() interface{} {
	return nil
}

// DiffList represents changes between two lists.
type DiffList struct {
	Removed map[string]Diffable
	Added   map[string]Diffable
	Common  map[string]Diffable
}

// Changed returns true if any list items were added, removed, or modified.
func (d DiffList) Changed() bool {
	if len(d.Removed)+len(d.Added) > 0 {
		return true
	}
	for _, x := range d.Common {
		if x.Changed() {
			return true
		}
	}
	return false
}

// String returns a formatted description of the list changes.
func (d DiffList) String(key string) string {
	return d.render(key, toggleStyle())
}

func (d DiffList) render(key string, st style) string {
	s := ""

	for _, k := range st.sorted(d.Added) {
		v := d.Added[k]
		s += st.format("  @C{%s[%s]} added\n", key, k)
		s += st.yamlstring("@G{%s}\n\n", v)
	}
	for _, k := range st.sorted(d.Removed) {
		v := d.Removed[k]
		s += st.format("  @C{%s[%s]} removed\n", key, k)
		s += st.yamlstring("@R{%s}\n\n", v)
	}
	for _, k := range st.sorted(d.Common) {
		v := d.Common[k]
		if v.Changed() {
			s += renderDiffable(v, st.childKey("%s[%s]", key, k), st)
		}
	}
	return s
}

// Value returns nil for list changes.
func (d DiffList) Value() interface{} {
	return nil
}

// Diff computes the difference between two values and returns a Diffable.
func Diff(a, b interface{}) (Diffable, error) {
	if typeof(a) != typeof(b) {
		return DiffType{Old: a, New: b}, nil
	}

	switch typeof(a) {
	case Scalar:
		return DiffScalar{Old: yamlmarshal(a), New: yamlmarshal(b)}, nil
	case Map:
		aMap, aOk := a.(map[string]interface{})
		bMap, bOk := b.(map[string]interface{})
		if !aOk || !bOk {
			return DiffScalar{}, stdfmt.Errorf("expected map types")
		}
		return diffMaps(aMap, bMap)
	case SimpleList:
		aList, aOk := a.([]interface{})
		bList, bOk := b.([]interface{})
		if !aOk || !bOk {
			return DiffScalar{}, stdfmt.Errorf("expected list types")
		}
		return diffSimpleLists(aList, bList)
	case KeyedList:
		aList, aOk := a.([]interface{})
		bList, bOk := b.([]interface{})
		if !aOk || !bOk {
			return DiffScalar{}, stdfmt.Errorf("expected list types")
		}
		return diffKeyedLists(aList, bList)
	default:
		return DiffScalar{}, stdfmt.Errorf("diff not implemented for this type")
	}
}

// diffMaps computes the difference between two maps.
func diffMaps(ma, mb map[string]interface{}) (DiffMap, error) {
	x := DiffMap{
		Removed: make(map[string]Diffable),
		Added:   make(map[string]Diffable),
		Common:  make(map[string]Diffable),
	}

	for k, v1 := range ma {
		key := k
		if v2, ok := mb[k]; ok {
			d, err := Diff(v1, v2)
			if err != nil {
				return x, err
			}
			x.Common[key] = d
		} else {
			x.Removed[key] = DiffNone{v1}
		}
	}

	for k, v2 := range mb {
		if _, ok := ma[k]; !ok {
			x.Added[k] = DiffNone{v2}
		}
	}
	return x, nil
}

// diffSimpleLists computes the difference between two simple lists.
func diffSimpleLists(la, lb []interface{}) (DiffList, error) {
	x := DiffList{
		Removed: make(map[string]Diffable),
		Added:   make(map[string]Diffable),
		Common:  make(map[string]Diffable),
	}

	for i, v1 := range la {
		key := stdfmt.Sprintf("%d", i)
		if i < len(lb) {
			d, err := Diff(v1, lb[i])
			if err != nil {
				return x, err
			}
			x.Common[key] = d
		} else {
			x.Removed[key] = DiffNone{v1}
		}
	}

	for i := len(la); i < len(lb); i++ {
		x.Added[stdfmt.Sprintf("%d", i)] = DiffNone{lb[i]}
	}
	return x, nil
}

// canonicalizeArrayOrder returns a copy of v with every non-keyed
// ("simple") list, at any nesting depth, sorted into a deterministic
// canonical order (by the YAML encoding of each element). It is the
// pre-pass behind DiffOptions.IgnoreArrayOrder (diff_options.go): running
// the unmodified Diff/diffSimpleLists comparison below against two
// canonicalized copies makes simple lists compare as multisets (same
// elements, any order, no reported change), without threading any option
// state through Diff itself, which stays exactly as it behaves on the
// default path. Keyed lists (see keyed) are already matched by key rather
// than position by diffKeyedLists, so their element order is left as-is;
// only their entries are recursed into, in case they themselves contain
// nested simple lists. Maps and scalars are copied/passed through
// unchanged aside from the same recursion. v is not mutated.
func canonicalizeArrayOrder(v interface{}) interface{} {
	switch val := v.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(val))
		for k, item := range val {
			out[k] = canonicalizeArrayOrder(item)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(val))
		for i, item := range val {
			out[i] = canonicalizeArrayOrder(item)
		}
		if keyed(val) != "" {
			return out
		}
		sort.SliceStable(out, func(i, j int) bool {
			return yamlmarshal(out[i]) < yamlmarshal(out[j])
		})
		return out
	default:
		return v
	}
}

// diffKeyedLists computes the difference between two keyed lists.
func diffKeyedLists(la, lb []interface{}) (DiffList, error) {
	key := keyed(la)
	ma := mapify(la, key)
	mb := mapify(lb, key)

	x := DiffList{
		Removed: make(map[string]Diffable),
		Added:   make(map[string]Diffable),
		Common:  make(map[string]Diffable),
	}

	for k, v1 := range ma {
		keyStr := k
		if v2, ok := mb[k]; ok {
			d, err := Diff(v1, v2)
			if err != nil {
				return x, err
			}
			x.Common[keyStr] = d
		} else {
			x.Removed[keyStr] = DiffNone{v1}
		}
	}

	for k, v2 := range mb {
		if _, ok := ma[k]; !ok {
			x.Added[k] = DiffNone{v2}
		}
	}
	return x, nil
}
