package yaml

import (
	"reflect"
	"strings"
)

// interfaceMapType is map[interface{}]interface{}, which Unmarshal
// refuses wherever it sits in a target type.
var interfaceMapType = reflect.TypeFor[map[interface{}]interface{}]()

// checkTarget refuses a target Unmarshal cannot decode into, before
// anything parses. That is a nil v, a v that is not a pointer, a nil
// pointer, and a type that holds map[interface{}]interface{} anywhere a
// decoder can reach, which is through pointers, slice and array elements,
// map keys and values, and exported or embedded struct fields. It also
// refuses an Unmarshaler where its method cannot run, which is as a map
// key or as a named ,inline field.
func checkTarget(v any) *DecodeError {
	return checkTargetFor("Unmarshal", v)
}

// checkTargetFor is checkTarget for the function named fn, which its
// messages name.
func checkTargetFor(fn string, v any) *DecodeError {
	if v == nil {
		return typeError("%s(nil)", fn)
	}
	rv := reflect.ValueOf(v)
	t := rv.Type()
	if t.Kind() != reflect.Pointer {
		return typeError("%s(non-pointer %s)", fn, t)
	}
	if rv.IsNil() {
		return typeError("%s(nil %s)", fn, t)
	}
	w := targetWalk{seen: make(map[walked]bool)}
	found := w.find(t.Elem(), t.Elem().String(), true)
	switch {
	case found.typ == nil:
		return nil
	case found.as != "":
		return typeError("%s cannot call the UnmarshalYAML method of %s, which %s holds at %s as %s", fn, found.typ, t, found.where, found.as)
	case found.typ == mapSliceType:
		return typeError("%s does not support yaml.MapSlice, which %s holds at %s; decode into a yaml.Node to keep key order", fn, t, found.where)
	default:
		return typeError("%s does not support map[interface {}]interface {}, which %s holds at %s; use map[string]any instead", fn, t, found.where)
	}
}

// targetWalk searches a type for map[interface{}]interface{}, for
// MapSlice, and for an Unmarshaler where its method cannot run. seen
// holds every type the walk has entered, with whether it was looking for
// Unmarshalers there, so a recursive type such as struct{ Next *T } is
// walked once and the walk ends.
type targetWalk struct {
	seen map[walked]bool
}

// walked is a type the walk has entered, and whether it was looking for
// Unmarshalers there.
type walked struct {
	t     reflect.Type
	hooks bool
}

// found is what targetWalk found. typ is the type it refuses, or nil
// when it found none, and where is the path to it. as is set for an
// Unmarshaler, and says how the type is used, as "a map key" or as "an
// inline field".
type found struct {
	where string
	typ   reflect.Type
	as    string
}

// find reports whether t holds map[interface{}]interface{} or MapSlice,
// and where, as a path that starts at path, the name of t. A map's key
// adds "{key}" to the path, and an element or a map's value adds "[]".
// When hooks is set, it also reports an Unmarshaler used as a map key or
// as a named ,inline field. It stops looking for those inside a type
// that decodes itself, whose own method decides what it reads.
func (w targetWalk) find(t reflect.Type, path string, hooks bool) found {
	if w.seen[walked{t, hooks}] {
		return found{}
	}
	w.seen[walked{t, hooks}] = true
	if t == mapSliceType {
		return found{where: path, typ: mapSliceType}
	}
	if t.Kind() == reflect.Map && t.Key() == interfaceMapType.Key() && t.Elem() == interfaceMapType.Elem() {
		return found{where: path, typ: interfaceMapType}
	}
	if hooks && (isUnmarshaler(t) || ownKindOf(t) != notOwn) {
		hooks = false
	}
	switch t.Kind() {
	case reflect.Pointer:
		return w.find(t.Elem(), path, hooks)
	case reflect.Map:
		return w.findInMap(t, path, hooks)
	case reflect.Slice, reflect.Array:
		return w.find(t.Elem(), path+"[]", hooks)
	case reflect.Struct:
		return w.findInStruct(t, path, hooks)
	default:
		return found{}
	}
}

// findInMap is find for t, a map type.
func (w targetWalk) findInMap(t reflect.Type, path string, hooks bool) found {
	if key := pointee(t.Key()); hooks && isUnmarshaler(key) {
		return found{where: path + "{key}", typ: key, as: "a map key"}
	}
	if got := w.find(t.Key(), path+"{key}", hooks); got.typ != nil {
		return got
	}
	return w.find(t.Elem(), path+"[]", hooks)
}

// findInStruct is find for t, a struct type, whose exported and embedded
// fields it searches in order.
func (w targetWalk) findInStruct(t reflect.Type, path string, hooks bool) found {
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() && !f.Anonymous {
			continue
		}
		if ft := pointee(f.Type); hooks && namedInline(f) && isUnmarshaler(ft) {
			return found{where: path + "." + f.Name, typ: ft, as: "an inline field"}
		}
		if got := w.find(f.Type, path+"."+f.Name, hooks); got.typ != nil {
			return got
		}
	}
	return found{}
}

// pointee returns t with its pointers followed.
func pointee(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}

// namedInline reports whether f is a named field that goccy decodes
// with the ,inline option. An embedded field's methods belong to the
// struct that embeds it, so its own method runs for that struct.
func namedInline(f reflect.StructField) bool {
	_, opts, _ := strings.Cut(fieldTag(f), ",")
	return !f.Anonymous && decodesField(f) && hasOption(opts, "inline")
}
