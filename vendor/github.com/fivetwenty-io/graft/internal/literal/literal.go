// Package literal holds the opaque wrapper that graft's merge and
// evaluation treat as a single scalar. pkg/graft exports the type as
// LiteralValue and builds one with NewLiteral; this package keeps the
// helpers that every tree walker needs, so that pkg/graft, its tree and
// merger packages, and the YAML encoders can all recognize a wrapper
// without importing each other.
package literal

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
)

// ErrPathInside is the error a Set or Delete returns when its path runs
// into a literal. Callers know the type as graft.LiteralValue, so the
// message names it that way and not by this package's type name.
var ErrPathInside = errors.New("the path runs into a literal value (graft.LiteralValue), which is replaced whole and cannot be changed inside")

// maxDepth bounds the recursion in Strip and Copy. A document deeper
// than this is far past anything graft can merge, and the bound keeps a
// cyclic value from overflowing the stack.
const maxDepth = 10000

// Value wraps a value that graft must keep whole. A merge replaces it as
// a unit and never merges into it, the evaluator never scans inside it,
// and every output strips the wrapper so that only the plain value is
// written. The zero Value wraps nil.
//
// Value holds its content behind a pointer so that two Values compare
// with == without a runtime panic, whatever they hold.
type Value struct {
	b *box
}

type box struct {
	v any
}

// Wrap returns v wrapped as a Value. A v that is already a Value is
// returned unchanged, so wrapping twice is the same as wrapping once.
func Wrap(v any) Value {
	if l, ok := v.(Value); ok {
		return l
	}
	return Value{b: &box{v: v}}
}

// Value returns the wrapped value, which may itself hold other Values.
func (l Value) Value() any {
	if l.b == nil {
		return nil
	}
	return l.b.v
}

// MarshalYAML returns the wrapped value with every wrapper inside it
// removed, so a YAML encoder that honors the method writes plain data.
func (l Value) MarshalYAML() (any, error) {
	return Strip(l.Value()), nil
}

// MarshalJSON writes the wrapped value as JSON, with every wrapper
// inside it removed.
func (l Value) MarshalJSON() ([]byte, error) {
	return json.Marshal(Strip(l.Value()))
}

// Is reports whether v is a Value.
func Is(v any) bool {
	_, ok := v.(Value)
	return ok
}

// Unwrap returns the content of v when v is a Value, and v itself
// otherwise. The bool reports whether v was a Value.
func Unwrap(v any) (any, bool) {
	if l, ok := v.(Value); ok {
		return l.Value(), true
	}
	return v, false
}

// Strip returns v with every Value at any depth replaced by its content.
// It copies only the maps and lists on the way to a Value, so a v that
// holds no Value comes back as the very same container, and the caller's
// data is never modified.
func Strip(v any) any {
	out, _ := strip(v, 0)
	return out
}

// strip is Strip, and it also reports whether anything changed.
func strip(v any, depth int) (any, bool) {
	if depth > maxDepth {
		return v, false
	}
	switch t := v.(type) {
	case Value:
		inner, _ := strip(t.Value(), depth+1)
		return inner, true
	case map[string]any:
		return stripMap(t, depth)
	case map[any]any:
		return stripMap(t, depth)
	case []any:
		return stripList(t, depth)
	default:
		return v, false
	}
}

// stripMap is strip for a map. It copies m only when a value in it
// changes.
func stripMap[K comparable](m map[K]any, depth int) (any, bool) {
	var out map[K]any
	for k, e := range m {
		s, changed := strip(e, depth+1)
		if !changed {
			continue
		}
		if out == nil {
			out = make(map[K]any, len(m))
			for k2, e2 := range m {
				out[k2] = e2
			}
		}
		out[k] = s
	}
	if out == nil {
		return m, false
	}
	return out, true
}

// stripList is strip for a list. It copies l only when an item in it
// changes.
func stripList(l []any, depth int) (any, bool) {
	var out []any
	for i, e := range l {
		s, changed := strip(e, depth+1)
		if !changed {
			continue
		}
		if out == nil {
			out = make([]any, len(l))
			copy(out, l)
		}
		out[i] = s
	}
	if out == nil {
		return l, false
	}
	return out, true
}

// Contains reports whether v is a Value or holds one at any depth.
func Contains(v any) bool {
	return contains(v, 0)
}

func contains(v any, depth int) bool {
	if depth > maxDepth {
		return false
	}
	switch t := v.(type) {
	case Value:
		return true
	case map[string]any:
		for _, e := range t {
			if contains(e, depth+1) {
				return true
			}
		}
	case map[any]any:
		for _, e := range t {
			if contains(e, depth+1) {
				return true
			}
		}
	case []any:
		for _, e := range t {
			if contains(e, depth+1) {
				return true
			}
		}
	}
	return false
}

// Copy returns a deep copy of v's maps and lists. A Value stays a Value
// and gets a copy of its content, so the copy shares no container with
// v. Copy also copies typed containers the way New does, such as a
// []string or a map[string][]string, and keeps every type it meets, so
// a map[any]any stays one. A struct, and a pointer to a struct or a
// scalar, is shared, as it is in New. Any other value is returned as it
// is. Copy panics with CycleMessage when v contains itself.
func Copy(v any) any {
	w := walk{keepTypes: true}
	return w.deepCopy(v, 0)
}

func (w *walk) deepCopy(v any, depth int) any {
	if depth > maxDepth {
		return v
	}
	switch t := v.(type) {
	case Value:
		return Value{b: &box{v: w.deepCopy(t.Value(), depth+1)}}
	case map[string]any:
		defer w.leave(w.enter(t, -1))
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[k] = w.deepCopy(e, depth+1)
		}
		return out
	case map[any]any:
		defer w.leave(w.enter(t, -1))
		out := make(map[any]any, len(t))
		for k, e := range t {
			out[k] = w.deepCopy(e, depth+1)
		}
		return out
	case []any:
		defer w.leave(w.enter(t, len(t)))
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = w.deepCopy(e, depth+1)
		}
		return out
	default:
		return w.copyTyped(v, depth)
	}
}

// copyPointer copies what a pointer to a container points at, and
// returns any other pointer as it is. It panics with CycleMessage when
// the pointer is already being copied.
func (w *walk) copyPointer(rv reflect.Value, depth int) reflect.Value {
	if rv.IsNil() {
		return rv
	}
	switch rv.Elem().Kind() {
	case reflect.Slice, reflect.Array, reflect.Map, reflect.Pointer, reflect.Interface:
	default:
		return rv
	}
	id := containerID{addr: rv.Pointer(), n: -2}
	if w.stack == nil {
		w.stack = make(map[containerID]struct{})
	}
	if _, inside := w.stack[id]; inside {
		panic(CycleMessage)
	}
	w.stack[id] = struct{}{}
	defer delete(w.stack, id)
	out := reflect.New(rv.Type().Elem())
	out.Elem().Set(w.copyValue(rv.Elem(), depth+1))
	return out
}

// CycleMessage is the text of the panic New and Copy raise when a value
// contains itself. It quotes nothing from the value.
const CycleMessage = "graft: the value contains itself, and a literal cannot hold cyclic data"

// walk tracks the maps and lists a copy is inside, so that it meets a
// cycle the first time round instead of following it until maxDepth,
// which takes exponential time when a container refers to itself more
// than once.
//
// keepTypes is set for Copy, which keeps every type it meets. Without it
// the walk is New's, which turns a map[any]any into a map[string]any.
type walk struct {
	stack     map[containerID]struct{}
	keepTypes bool
}

// element copies a value the reflect walk finds inside an interface or
// a Value, in the way the walk copies values.
func (w *walk) element(v any, depth int) any {
	if w.keepTypes {
		return w.deepCopy(v, depth)
	}
	return w.normalize(v, depth)
}

// containerID names a map or a list by the address of its storage. A
// list also needs its length, since a shorter list over the same array
// is a different list and not a cycle. A map's length is -1.
type containerID struct {
	addr uintptr
	n    int
}

// enter records that the walk is inside c, and panics with CycleMessage
// when it already was. An empty container holds nothing to recurse
// into, so it is not recorded.
func (w *walk) enter(c any, n int) containerID {
	rv := reflect.ValueOf(c)
	if rv.Len() == 0 {
		return containerID{}
	}
	id := containerID{addr: rv.Pointer(), n: n}
	if w.stack == nil {
		w.stack = make(map[containerID]struct{})
	}
	if _, inside := w.stack[id]; inside {
		panic(CycleMessage)
	}
	w.stack[id] = struct{}{}
	return id
}

// leave records that the walk has left the container enter named.
func (w *walk) leave(id containerID) {
	if id.addr != 0 {
		delete(w.stack, id)
	}
}

// GuardText returns v with every string that holds "((" wrapped as a
// Value, so the evaluator never runs it as an operator. It copies only
// the maps and lists on the way to a string it wraps, leaves a Value
// that is already in v as it is, and reports whether anything changed.
func GuardText(v any) (any, bool) {
	return guardText(v, 0)
}

func guardText(v any, depth int) (any, bool) {
	if depth > maxDepth {
		return v, false
	}
	switch t := v.(type) {
	case string:
		if strings.Contains(t, "((") {
			return Wrap(t), true
		}
		return v, false
	case map[string]any:
		var out map[string]any
		for k, e := range t {
			g, changed := guardText(e, depth+1)
			if !changed {
				continue
			}
			if out == nil {
				out = make(map[string]any, len(t))
				for k2, e2 := range t {
					out[k2] = e2
				}
			}
			out[k] = g
		}
		if out == nil {
			return v, false
		}
		return out, true
	case []any:
		var out []any
		for i, e := range t {
			g, changed := guardText(e, depth+1)
			if !changed {
				continue
			}
			if out == nil {
				out = make([]any, len(t))
				copy(out, t)
			}
			out[i] = g
		}
		if out == nil {
			return v, false
		}
		return out, true
	default:
		return v, false
	}
}

// Texts returns the text of every Value in v at any depth: each string
// it holds, each map key, and each other scalar as fmt prints it. The
// engine uses it to keep that text out of an error message.
func Texts(v any) []string {
	var out []string
	collectTexts(v, false, 0, &out)
	return out
}

func collectTexts(v any, inside bool, depth int, out *[]string) {
	if depth > maxDepth {
		return
	}
	switch t := v.(type) {
	case Value:
		collectTexts(t.Value(), true, depth+1, out)
	case map[string]any:
		for k, e := range t {
			if inside {
				*out = append(*out, k)
			}
			collectTexts(e, inside, depth+1, out)
		}
	case map[any]any:
		for k, e := range t {
			if inside {
				*out = append(*out, fmt.Sprint(k))
			}
			collectTexts(e, inside, depth+1, out)
		}
	case []any:
		for _, e := range t {
			collectTexts(e, inside, depth+1, out)
		}
	case nil:
	default:
		if inside {
			*out = append(*out, fmt.Sprint(t))
		}
	}
}

// New wraps a deep copy of v, so a later change to v does not reach the
// Value. On the way it turns every map[any]any into a map[string]any,
// with each key printed by fmt, which is the shape a path read expects.
// A v that is already a Value is returned unchanged. New panics with
// CycleMessage when v contains itself.
func New(v any) Value {
	if l, ok := v.(Value); ok {
		return l
	}
	var w walk
	return Value{b: &box{v: w.normalize(v, 0)}}
}

func (w *walk) normalize(v any, depth int) any {
	if depth > maxDepth {
		return v
	}
	switch t := v.(type) {
	case Value:
		return Value{b: &box{v: w.normalize(t.Value(), depth+1)}}
	case map[string]any:
		defer w.leave(w.enter(t, -1))
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[k] = w.normalize(e, depth+1)
		}
		return out
	case map[any]any:
		defer w.leave(w.enter(t, -1))
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[fmt.Sprint(k)] = w.normalize(e, depth+1)
		}
		return out
	case []any:
		defer w.leave(w.enter(t, len(t)))
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = w.normalize(e, depth+1)
		}
		return out
	default:
		return w.copyTyped(v, depth)
	}
}

// copyTyped copies a slice, array, map, or pointer to one of those that
// the cases above do not cover, such as a []string or a
// map[string][]string, and keeps its type. Everything else comes back as
// it is, and that includes a struct, because a struct can hold fields
// that reflect cannot copy, and a pointer to anything but a container.
func (w *walk) copyTyped(v any, depth int) any {
	if v == nil {
		return nil
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Slice, reflect.Array, reflect.Map, reflect.Pointer:
		return w.copyValue(rv, depth).Interface()
	default:
		return v
	}
}

var valueType = reflect.TypeOf(Value{})

func (w *walk) copyValue(rv reflect.Value, depth int) reflect.Value {
	if depth > maxDepth {
		return rv
	}
	switch rv.Kind() {
	case reflect.Interface:
		if rv.IsNil() {
			return rv
		}
		out := reflect.New(rv.Type()).Elem()
		out.Set(reflect.ValueOf(w.element(rv.Elem().Interface(), depth+1)))
		return out
	case reflect.Slice:
		if rv.IsNil() {
			return rv
		}
		defer w.leave(w.enter(rv.Interface(), rv.Len()))
		out := reflect.MakeSlice(rv.Type(), rv.Len(), rv.Len())
		for i := range rv.Len() {
			out.Index(i).Set(w.copyValue(rv.Index(i), depth+1))
		}
		return out
	case reflect.Array:
		out := reflect.New(rv.Type()).Elem()
		for i := range rv.Len() {
			out.Index(i).Set(w.copyValue(rv.Index(i), depth+1))
		}
		return out
	case reflect.Map:
		if rv.IsNil() {
			return rv
		}
		defer w.leave(w.enter(rv.Interface(), -1))
		out := reflect.MakeMapWithSize(rv.Type(), rv.Len())
		for it := rv.MapRange(); it.Next(); {
			out.SetMapIndex(it.Key(), w.copyValue(it.Value(), depth+1))
		}
		return out
	case reflect.Pointer:
		return w.copyPointer(rv, depth)
	case reflect.Struct:
		if rv.Type() == valueType {
			return reflect.ValueOf(w.element(rv.Interface(), depth+1))
		}
		return rv
	default:
		return rv
	}
}
