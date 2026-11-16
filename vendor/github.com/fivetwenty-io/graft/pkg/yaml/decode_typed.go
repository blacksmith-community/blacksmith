package yaml

import (
	"errors"
	"reflect"
	"strings"

	goyaml "github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"

	"github.com/fivetwenty-io/graft/internal/yamlcodec"
)

// boolType is the Go type the bool hook decodes into.
var boolType = reflect.TypeFor[bool]()

// decodeTyped decodes the document src returns into v, a non-nil
// pointer that checkTarget accepted, with goccy's own decoder. Every
// error it returns is a *DecodeError.
//
// When owners is set, src records in it the Node each token came from,
// and every place in v that an Unmarshaler fills is a hole that goccy
// leaves alone. Once goccy and the fixer are done, each of those places
// has its method called in document order, with the Node that holds its
// value, and the first failure ends the decode. A key that more than one
// struct field reads, past those that goccy shadows, is refused before
// goccy decodes anything when one of them can reach an Unmarshaler,
// since goccy would fill that field natively and its method would never
// run. The refusal comes from a walk in document order, so it is the
// same on every run and needs none of the search for the earliest error
// that a goccy error inside an ,inline map gets.
//
// The document comes with every alias expanded, as a merge parses it,
// and goccy decodes it into v natively. In YAML 1.1 mode a hook reads
// plain yes, no, on, and off into bool fields. Afterwards, a
// genericFixer converts every generic value in v, which is a value of
// interface type, with the conversion the generic route applies, so a
// map[string]any field holds what a top-level decode of the same text
// gives. Generic values come from goccy rather than from a hook, because
// a hook receives the node's source text, and an expanded alias keeps
// the indentation of the place its anchor was defined.
func decodeTyped(src source, v any, opts DecodeOptions, owners *tokenOwners) error {
	target := reflect.TypeOf(v)
	generic := reachesType(target, isInterface)
	// Quoted YAML 1.1 words need the marker only where the conversion
	// below reads them, which is in generic values and only in 1.1 mode.
	tag := generic && !opts.YAML12
	body, err := src(tag)
	if err != nil {
		return err
	}
	if body == nil || body.Type() == ast.NullType {
		// An empty or null document leaves v unchanged, as it does on
		// the generic route.
		return nil
	}

	var plan hookPlan
	if owners != nil {
		body, plan, err = hookSites(body, target.Elem(), owners, opts)
		if err != nil {
			return err
		}
		if body == nil {
			// The root itself is a hook site, so goccy has nothing to do.
			return runHookSites(reflect.ValueOf(v).Elem(), plan.sites)
		}
		// Each entry a stand-in key holds moves to its NaN key on every
		// way out, once the hooks have run or the decode has failed.
		defer moveKeys(reflect.ValueOf(v).Elem(), plan.moves)
	}
	body, own := asWritten(body, target.Elem())
	err = decodeBody(body, v, opts)
	if generic {
		// goccy fills v in part before it fails, so the fixer runs on
		// the error path too, and no caller sees a raw value or a marker.
		f := &genericFixer{
			compat:  &yamlcodec.YAMLCompat{ConvertYAML11Booleans: !opts.YAML12},
			strip:   tag,
			needs:   make(map[reflect.Type]bool),
			visited: make(map[visit]bool),
		}
		f.fix(reflect.ValueOf(v))
	}
	if err != nil {
		de := translate(own.locate(locate(err, body, target), target.Elem()))
		if de.Kind == ErrorKindType && de.Line > 0 && reachesInlineMap(target) {
			// goccy fills an ,inline map in Go map order, so the value
			// its error names varies from run to run. The earliest bad
			// value is the same on every run.
			de = earliestTypeError(body, v, opts, own, de)
		}
		return de
	}
	if err := own.badTime(); err != nil {
		return err
	}
	return runHookSites(reflect.ValueOf(v).Elem(), plan.sites)
}

// decodeBody decodes body into v with goccy, and returns a decoder panic
// as an error, so the caller still runs the fixer on what goccy filled.
// A panic from a caller's own method goes on unrecovered.
func decodeBody(body ast.Node, v any, opts DecodeOptions) (err error) {
	defer func() {
		if r := recover(); r != nil {
			if !panicFromDecoder() {
				panic(r)
			}
			err = decoderPanicError(reflect.TypeOf(v))
		}
	}()
	return yamlcodec.DecodeBody(body, v, decodeOptions(opts)...)
}

// decodeOptions returns the goccy options for a typed decode. The parser
// has already refused duplicate keys, so the decoder's own check only
// ever fires on a key that a << merge key also supplies, which YAML
// allows. The later key wins, as on the generic route.
func decodeOptions(opts DecodeOptions) []goyaml.DecodeOption {
	out := []goyaml.DecodeOption{goyaml.AllowDuplicateMapKey()}
	if !opts.YAML12 {
		out = append(out, goyaml.CustomUnmarshaler(yaml11Bool))
	}
	return out
}

// yaml11Bool is the CustomUnmarshaler for bool in YAML 1.1 mode. raw is
// the scalar's source text, with its quotes and tag when it has them. A
// plain yes, no, on, or off reads through YAMLCompat.ConvertValue, the
// table a merge uses, and anything else goes to goccy's own rule, so a
// quoted "yes" is refused, as yaml.v2 refuses it.
func yaml11Bool(b *bool, raw []byte) error {
	text := strings.TrimSpace(string(raw))
	if got, ok := yamlcodec.DefaultYAMLCompat().ConvertValue(text).(bool); ok {
		*b = got
		return nil
	}
	switch text {
	case "true", "True", "TRUE":
		*b = true
		return nil
	case "false", "False", "FALSE":
		*b = false
		return nil
	}
	var got bool
	if err := goyaml.Unmarshal(raw, &got); err != nil {
		// The error belongs to a parse of raw alone, so its position is
		// wrong for the document, and locate finds the right one.
		return &goyaml.TypeError{DstType: boolType, SrcType: reflect.TypeFor[string]()}
	}
	*b = got
	return nil
}

// locate returns err, an error decoding body into a value of type
// target, with the position of the scalar it concerns when err is the
// bool hook's refusal, which carries none. It decodes body again with no
// hook, after turning every plain YAML 1.1 boolean into a boolean node,
// so goccy refuses the same scalar itself and names its token. When that
// second decode fails some other way, err comes back unchanged and its
// DecodeError has Line 0.
func locate(err error, body ast.Node, target reflect.Type) error {
	var te *goyaml.TypeError
	if !errors.As(err, &te) || te.Token != nil || te.DstType != boolType {
		return err
	}
	again := yamlcodec.DecodeBody(yamlcodec.PlainBoolsAsBoolNodes(body), reflect.New(target.Elem()).Interface(), goyaml.AllowDuplicateMapKey())
	var found *goyaml.TypeError
	if !errors.As(again, &found) || found.Token == nil || found.DstType.Kind() != reflect.Bool ||
		!sameField(found.StructFieldName, te.StructFieldName) {
		return err
	}
	return again
}

// sameField reports whether two struct field names goccy recorded agree.
func sameField(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// visit identifies a pointer the fixer has entered, so a value that
// reaches itself is walked once.
type visit struct {
	ptr uintptr
	typ reflect.Type
}

// genericFixer walks a decoded value through the places goccy fills,
// which are exported and embedded struct fields, pointers, slice and
// array elements, and map values. It converts each generic value it
// finds with compat, and when strip is set it removes the quoted-boolean
// marker from each string. goccy fills a []byte only from a sequence of
// integers, so no []byte can hold the marker.
type genericFixer struct {
	compat  *yamlcodec.YAMLCompat
	strip   bool
	needs   map[reflect.Type]bool
	visited map[visit]bool
}

// fix converts the generic values and strips the markers in v.
func (f *genericFixer) fix(v reflect.Value) {
	if !f.reaches(v.Type()) {
		return
	}
	switch v.Kind() {
	case reflect.Interface:
		f.fixInterface(v)
	case reflect.Pointer:
		f.fixPointer(v)
	case reflect.Struct:
		f.fixStruct(v)
	case reflect.Slice, reflect.Array:
		for i := range v.Len() {
			f.fix(v.Index(i))
		}
	case reflect.Map:
		f.fixMap(v)
	case reflect.String:
		if s, ok := yamlcodec.UnprotectString(v.String()); ok && v.CanSet() {
			v.SetString(s)
		}
	default:
	}
}

// fixPointer fixes what v points to, once for each pointer.
func (f *genericFixer) fixPointer(v reflect.Value) {
	if v.IsNil() {
		return
	}
	key := visit{ptr: v.Pointer(), typ: v.Type()}
	if f.visited[key] {
		return
	}
	f.visited[key] = true
	f.fix(v.Elem())
}

// fixStruct fixes the exported and embedded fields of v.
func (f *genericFixer) fixStruct(v reflect.Value) {
	t := v.Type()
	for i := range t.NumField() {
		if field := t.Field(i); field.IsExported() || field.Anonymous {
			f.fix(v.Field(i))
		}
	}
}

// fixInterface converts the generic value v holds. It walks a pointer
// instead, which only a target that held one before decoding can hold,
// and the value of a non-empty interface, since a converted value might
// no longer implement that interface.
func (f *genericFixer) fixInterface(v reflect.Value) {
	if v.IsNil() || !v.CanSet() {
		return
	}
	inner := v.Elem()
	if inner.Kind() == reflect.Pointer || v.NumMethod() > 0 {
		f.fix(inner)
		return
	}
	if out := f.compat.ConvertGeneric(inner.Interface()); out != nil {
		v.Set(reflect.ValueOf(out))
	}
}

// fixMap fixes each value of the map v, through a copy, since a map's
// values cannot be set in place.
//
// A NaN key finds no entry, so storing a value under it adds an entry,
// which a walk of the map can then meet again. The values under NaN keys
// are set aside instead, and when there are any, the map is emptied and
// filled again, with each entry it held once.
func (f *genericFixer) fixMap(v reflect.Value) {
	if v.IsNil() {
		return
	}
	elem := v.Type().Elem()
	if !f.reaches(elem) {
		return
	}
	var nans []reflect.Value
	iter := v.MapRange()
	for iter.Next() {
		item := reflect.New(elem).Elem()
		item.Set(iter.Value())
		f.fix(item)
		if valueNaN(iter.Key()) {
			nans = append(nans, iter.Key(), item)
			continue
		}
		v.SetMapIndex(iter.Key(), item)
	}
	if len(nans) == 0 {
		return
	}
	pairs := make([]reflect.Value, 0, 2*v.Len())
	for it := v.MapRange(); it.Next(); {
		if !valueNaN(it.Key()) {
			pairs = append(pairs, it.Key(), it.Value())
		}
	}
	v.Clear()
	for _, set := range [][]reflect.Value{pairs, nans} {
		for i := 0; i+1 < len(set); i += 2 {
			v.SetMapIndex(set[i], set[i+1])
		}
	}
}

// reaches reports whether a value of type t can hold, at a place fix
// visits, a value fix changes.
func (f *genericFixer) reaches(t reflect.Type) bool {
	got, ok := f.needs[t]
	if !ok {
		got = reachesType(t, f.changes)
		f.needs[t] = got
	}
	return got
}

// changes reports whether fix changes values of type t itself.
func (f *genericFixer) changes(t reflect.Type) bool {
	return isInterface(t) || f.strip && t.Kind() == reflect.String
}

// isInterface reports whether t is an interface type.
func isInterface(t reflect.Type) bool {
	return t.Kind() == reflect.Interface
}

// reachesType reports whether t holds a type that satisfies leaf at a
// place goccy fills, which are the same places genericFixer visits.
func reachesType(t reflect.Type, leaf func(reflect.Type) bool) bool {
	w := typeWalk{leaf: leaf, seen: make(map[reflect.Type]bool)}
	return w.reaches(t)
}

// typeWalk searches a type for one that satisfies leaf. It enters the
// element of a pointer, a slice, an array, or a map, and each struct
// field that field accepts, or each exported or embedded field when
// field is nil. It never looks inside a type that opaque accepts, when
// opaque is set. seen holds every type the walk has entered, so a
// recursive type is walked once.
type typeWalk struct {
	leaf   func(reflect.Type) bool
	opaque func(reflect.Type) bool
	field  func(reflect.StructField) bool
	seen   map[reflect.Type]bool
}

func (w typeWalk) reaches(t reflect.Type) bool {
	if w.seen[t] {
		return false
	}
	w.seen[t] = true
	if w.leaf(t) {
		return true
	}
	if w.opaque != nil && w.opaque(t) {
		return false
	}
	switch t.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Map:
		return w.reaches(t.Elem())
	case reflect.Struct:
		for i := range t.NumField() {
			if field := t.Field(i); w.enters(field) && w.reaches(field.Type) {
				return true
			}
		}
	default:
	}
	return false
}

// enters reports whether the walk looks inside the struct field f.
func (w typeWalk) enters(f reflect.StructField) bool {
	if w.field != nil {
		return w.field(f)
	}
	return f.IsExported() || f.Anonymous
}
