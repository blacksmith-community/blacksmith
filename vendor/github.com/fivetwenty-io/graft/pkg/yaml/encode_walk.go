package yaml

import (
	"encoding"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"

	goyaml "github.com/goccy/go-yaml"

	"github.com/fivetwenty-io/graft/internal/yamlcodec"
)

// maxEncodeDepth bounds how deep the walk goes, so a MarshalYAML method
// that returns a value holding itself ends in an error and not in a
// stack overflow.
const maxEncodeDepth = 10000

// yamlMarshaler is a type that chooses its own YAML value.
type yamlMarshaler interface {
	MarshalYAML() (any, error)
}

// bytesMarshaler is goccy's other MarshalYAML form, which returns YAML
// text. Marshal refuses it rather than write the type as a struct.
type bytesMarshaler interface {
	MarshalYAML() ([]byte, error)
}

// normalizer turns a Go value into the generic form graft's encoder
// styles and orders: yaml.MapSlice with string or PlainScalar keys,
// []any, FlowValue, PlainScalar, and the scalars nil, bool, int64,
// uint64, and string. Every map comes out as a yaml.MapSlice in its final
// order, so the encoder never sees a typed Go value.
type normalizer struct {
	less   func(a, b string) bool
	onPath map[walkID]bool
	done   map[walkID]bool
	depth  int
}

// walkID identifies a pointer, map, or slice the walk entered.
type walkID struct {
	ptr uintptr
	typ reflect.Type
	len int
}

func newNormalizer(less func(a, b string) bool) *normalizer {
	return &normalizer{less: less, onPath: make(map[walkID]bool), done: make(map[walkID]bool)}
}

func refuse(format string, args ...any) error {
	return &encodeError{msg: fmt.Sprintf(format, args...)}
}

// value normalizes v, which sits at path, a Go type and field path that
// errors name.
func (n *normalizer) value(v reflect.Value, path string) (any, error) {
	if !v.IsValid() {
		return nil, nil
	}
	n.depth++
	defer func() { n.depth-- }()
	if n.depth > maxEncodeDepth {
		return nil, refuse("Marshal does not support a value nested deeper than %d levels, which %s holds", maxEncodeDepth, path)
	}
	t := v.Type()
	if t == nodeType {
		return nil, refuse("Marshal does not support yaml.Node, which %s holds", path)
	}
	switch v.Kind() {
	case reflect.Interface:
		if v.IsNil() {
			return nil, nil
		}
		return n.value(v.Elem(), path)
	case reflect.Pointer:
		if v.IsNil() {
			return nil, nil
		}
		if t.Elem() == nodeType {
			return nil, refuse("Marshal does not support yaml.Node, which %s holds", path)
		}
	default:
	}
	if out, handled, err := n.marshaler(v, path); handled {
		return out, err
	}
	if v.Kind() == reflect.Pointer {
		return n.enter(walkID{ptr: v.Pointer(), typ: t}, path, func() (any, error) {
			return n.value(v.Elem(), path)
		})
	}
	if out, ok := scalarOf(v); ok {
		return out, nil
	}
	return n.composite(v, path)
}

// composite normalizes a slice, an array, a map, or a struct.
func (n *normalizer) composite(v reflect.Value, path string) (any, error) {
	t := v.Type()
	if t == mapSliceType {
		return n.orderedItems(v, path)
	}
	switch v.Kind() {
	case reflect.Slice:
		if v.IsNil() || v.Len() == 0 {
			return []any{}, nil
		}
		return n.enter(walkID{ptr: v.Pointer(), typ: t, len: v.Len()}, path, func() (any, error) {
			return n.elements(v, path)
		})
	case reflect.Array:
		return n.elements(v, path)
	case reflect.Map:
		if v.IsNil() {
			return goyaml.MapSlice{}, nil
		}
		return n.enter(walkID{ptr: v.Pointer(), typ: t}, path, func() (any, error) {
			items, err := n.mapItems(v, path)
			return items, err
		})
	case reflect.Struct:
		items, err := n.structItems(v, path)
		return items, err
	default:
		return nil, refuse("Marshal does not support Go type %s, which %s holds", t, path)
	}
}

// scalarOf normalizes a bool, a number, or a string, and reports false
// for any other kind.
func scalarOf(v reflect.Value) (any, bool) {
	switch v.Kind() {
	case reflect.Bool:
		return v.Bool(), true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int(), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return v.Uint(), true
	case reflect.Float32:
		return yamlcodec.PlainScalar(formatFloat(v.Float(), 32)), true
	case reflect.Float64:
		return yamlcodec.PlainScalar(formatFloat(v.Float(), 64)), true
	case reflect.String:
		return v.String(), true
	default:
		return nil, false
	}
}

// marshaler applies the type's own MarshalYAML or MarshalText method, if
// it has one and v can be read.
func (n *normalizer) marshaler(v reflect.Value, path string) (out any, handled bool, err error) {
	if !v.CanInterface() {
		return nil, false, nil
	}
	switch m := v.Interface().(type) {
	case yamlMarshaler:
		r, merr := m.MarshalYAML()
		if merr != nil {
			return nil, true, refuse("MarshalYAML of %s, which %s holds, failed", v.Type(), path)
		}
		out, err = n.value(reflect.ValueOf(r), path)
		return out, true, err
	case bytesMarshaler:
		return nil, true, refuse("Marshal does not support the MarshalYAML() ([]byte, error) form of %s, which %s holds", v.Type(), path)
	case encoding.TextMarshaler:
		b, merr := m.MarshalText()
		if merr != nil {
			return nil, true, refuse("MarshalText of %s, which %s holds, failed", v.Type(), path)
		}
		return string(b), true, nil
	}
	return nil, false, nil
}

// enter runs walk for the pointer, map, or slice at id. It refuses the
// value when the walk is already inside it, which is a cycle, and it
// reuses nothing, since a value reached twice is encoded twice.
func (n *normalizer) enter(id walkID, path string, walk func() (any, error)) (any, error) {
	if n.onPath[id] {
		return nil, refuse("Marshal does not support a cycle, which %s holds", path)
	}
	n.onPath[id] = true
	out, err := walk()
	delete(n.onPath, id)
	return out, err
}

// elements normalizes the items of a slice or array.
func (n *normalizer) elements(v reflect.Value, path string) (any, error) {
	out := make([]any, v.Len())
	for i := range out {
		item, err := n.value(v.Index(i), path+"[]")
		if err != nil {
			return nil, err
		}
		out[i] = item
	}
	return out, nil
}

// mapItems normalizes a map into items sorted by the text of their keys.
func (n *normalizer) mapItems(v reflect.Value, path string) (goyaml.MapSlice, error) {
	items := make(goyaml.MapSlice, 0, v.Len())
	iter := v.MapRange()
	for iter.Next() {
		key, err := n.key(iter.Key(), path+"{key}")
		if err != nil {
			return nil, err
		}
		value, err := n.value(iter.Value(), path+"{value}")
		if err != nil {
			return nil, err
		}
		items = append(items, goyaml.MapItem{Key: key, Value: value})
	}
	n.sortItems(items)
	return items, nil
}

// orderedItems normalizes a yaml.MapSlice into items in the order it
// holds them, and refuses two items whose keys write the same text.
func (n *normalizer) orderedItems(v reflect.Value, path string) (any, error) {
	if v.Len() == 0 {
		return goyaml.MapSlice{}, nil
	}
	return n.enter(walkID{ptr: v.Pointer(), typ: v.Type(), len: v.Len()}, path, func() (any, error) {
		items := make(goyaml.MapSlice, 0, v.Len())
		seen := make(map[string]bool, v.Len())
		for i := range v.Len() {
			item := v.Index(i)
			key, err := n.key(item.Field(0), path+"{key}")
			if err != nil {
				return nil, err
			}
			text := sortText(key)
			if seen[text] {
				return nil, refuse("Marshal does not support a key repeated in a yaml.MapSlice, which %s holds at item %d", path, i)
			}
			seen[text] = true
			value, err := n.value(item.Field(1), path+"{value}")
			if err != nil {
				return nil, err
			}
			items = append(items, goyaml.MapItem{Key: key, Value: value})
		}
		return items, nil
	})
}

// key normalizes a map key to a string, which the encoder styles, or to a
// PlainScalar for a number, a bool, or a null, which it writes as is.
func (n *normalizer) key(k reflect.Value, path string) (any, error) {
	norm, err := n.value(k, path)
	if err != nil {
		return nil, err
	}
	switch kv := norm.(type) {
	case string, yamlcodec.PlainScalar:
		return kv, nil
	case nil:
		return yamlcodec.PlainScalar("null"), nil
	case bool:
		return yamlcodec.PlainScalar(strconv.FormatBool(kv)), nil
	case int64:
		return yamlcodec.PlainScalar(strconv.FormatInt(kv, 10)), nil
	case uint64:
		return yamlcodec.PlainScalar(strconv.FormatUint(kv, 10)), nil
	default:
		// A key held in an any is named by the type it holds. A nil
		// one normalizes to nil, so it never reaches this arm.
		kt := k.Type()
		if k.Kind() == reflect.Interface && !k.IsNil() {
			kt = k.Elem().Type()
		}
		return nil, refuse("Marshal does not support a map key of Go type %s, which %s holds", kt, path)
	}
}

// sortText is the text a key sorts by.
func sortText(k any) string {
	switch kv := k.(type) {
	case string:
		return kv
	case yamlcodec.PlainScalar:
		return string(kv)
	default:
		return ""
	}
}

func (n *normalizer) sortItems(items goyaml.MapSlice) {
	sort.SliceStable(items, func(i, j int) bool {
		return n.less(sortText(items[i].Key), sortText(items[j].Key))
	})
}

// structField is how a struct field's yaml tag names it.
type tagOptions struct {
	name                    string
	omitEmpty, flow, inline bool
}

func parseTag(f reflect.StructField) (opts tagOptions, skip bool) {
	tag := f.Tag.Get("yaml")
	if tag == "" {
		tag = f.Tag.Get("json")
	}
	if tag == "-" {
		return opts, true
	}
	parts := strings.Split(tag, ",")
	opts.name = parts[0]
	for _, flag := range parts[1:] {
		switch flag {
		case "omitempty":
			opts.omitEmpty = true
		case "flow":
			opts.flow = true
		case "inline":
			opts.inline = true
		}
	}
	if opts.name == "" {
		opts.name = strings.ToLower(f.Name)
	}
	return opts, false
}

// structItems normalizes a struct into items in field order.
func (n *normalizer) structItems(v reflect.Value, path string) (goyaml.MapSlice, error) {
	t := v.Type()
	items := make(goyaml.MapSlice, 0, t.NumField())
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() && !f.Anonymous {
			continue
		}
		opts, skip := parseTag(f)
		if skip {
			continue
		}
		fv := v.Field(i)
		fpath := path + "." + f.Name
		if opts.inline {
			inlined, err := n.inline(fv, fpath)
			if err != nil {
				return nil, err
			}
			items = append(items, inlined...)
			continue
		}
		if opts.omitEmpty && isEmptyValue(fv) {
			continue
		}
		value, err := n.value(fv, fpath)
		if err != nil {
			return nil, err
		}
		if opts.flow {
			switch value.(type) {
			case goyaml.MapSlice, []any:
				value = yamlcodec.FlowValue{V: value}
			}
		}
		items = append(items, goyaml.MapItem{Key: opts.name, Value: value})
	}
	return items, nil
}

// inline normalizes the field a ,inline tag names into items of its
// parent. A struct adds its fields in order, and a map adds its entries
// in key order.
func (n *normalizer) inline(fv reflect.Value, path string) (goyaml.MapSlice, error) {
	for fv.Kind() == reflect.Pointer || fv.Kind() == reflect.Interface {
		if fv.IsNil() {
			return nil, nil
		}
		if fv.Kind() == reflect.Pointer && fv.Type().Elem() == nodeType {
			return nil, refuse("Marshal does not support yaml.Node, which %s holds", path)
		}
		fv = fv.Elem()
	}
	switch {
	case fv.Type() == nodeType:
		return nil, refuse("Marshal does not support yaml.Node, which %s holds", path)
	case fv.Kind() == reflect.Struct:
		return n.structItems(fv, path)
	case fv.Kind() == reflect.Map:
		if fv.IsNil() {
			return nil, nil
		}
		return n.mapItems(fv, path)
	default:
		return nil, refuse("Marshal does not support ,inline on Go type %s, which %s holds", fv.Type(), path)
	}
}

// isEmptyValue reports whether v is empty for ,omitempty, as yaml.v2 reads it.
func isEmptyValue(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.String, reflect.Slice, reflect.Map:
		return v.Len() == 0
	case reflect.Interface, reflect.Pointer:
		return v.IsNil()
	case reflect.Bool:
		return !v.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int() == 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return v.Uint() == 0
	case reflect.Float32, reflect.Float64:
		return v.Float() == 0
	case reflect.Struct:
		if z, ok := zeroer(v); ok {
			return z
		}
		t := v.Type()
		for i := range t.NumField() {
			if t.Field(i).PkgPath != "" {
				continue
			}
			if !isEmptyValue(v.Field(i)) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

// zeroer asks a type that has an IsZero method, such as time.Time.
func zeroer(v reflect.Value) (zero, ok bool) {
	if !v.CanInterface() {
		return false, false
	}
	z, ok := v.Interface().(interface{ IsZero() bool })
	if !ok {
		return false, false
	}
	return z.IsZero(), true
}

// formatFloat writes f as a YAML 1.1 scalar that reads back as a number.
// A whole number that fits in an int64 is plain integer digits, so 3.0 is
// 3 and 1e6 is 1000000. Any other float has a "." so that a reader takes
// it for a float: a whole number past the int64 range, such as 1e20,
// becomes 1.0e+20, and a fraction too small to print without an exponent
// gets a signed exponent as the YAML 1.1 float expression wants. NaN and
// the infinities are .nan, .inf, and -.inf.
func formatFloat(f float64, bitSize int) string {
	switch {
	case math.IsNaN(f):
		return ".nan"
	case math.IsInf(f, 1):
		return ".inf"
	case math.IsInf(f, -1):
		return "-.inf"
	case f == 0:
		return "0"
	case f == math.Trunc(f) && f >= -(1<<63) && f < 1<<63:
		return strconv.FormatInt(int64(f), 10)
	case f != math.Trunc(f) && math.Abs(f) >= 1e-4:
		return strconv.FormatFloat(f, 'f', -1, bitSize)
	}
	s := strconv.FormatFloat(f, 'e', -1, bitSize)
	mantissa, exp, _ := strings.Cut(s, "e")
	if !strings.Contains(mantissa, ".") {
		mantissa += ".0"
	}
	return mantissa + "e" + exp
}
