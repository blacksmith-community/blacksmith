package graft

import "reflect"

// Limits for the quick check that finds a tree already in plain form.
// A tree that goes deeper or holds more values than these allows is
// handed to the slower walk, which tracks the containers it has seen,
// so a cycle or heavy sharing can never make the quick check run long.
const (
	plainMaxDepth  = 512
	plainMaxVisits = 1 << 21
)

// plainGoValue returns v with every slice or array (except []byte) as
// []interface{} and every map with string keys as
// map[string]interface{}, recursively. Other values come back as they
// are. Values already in the plain form are rebuilt only when something
// inside them changes, and a tree that is plain throughout comes back
// without any allocation. A container that holds itself is left as it
// is, so the cycle checks further on still see it, and a container
// that appears in several places is converted once.
func plainGoValue(v interface{}) interface{} {
	budget := plainMaxVisits
	if isPlainTree(v, 0, &budget) {
		return v
	}
	p := &plainer{
		active: map[plainVisit]struct{}{},
		done:   map[plainVisit]interface{}{},
	}
	return p.value(v)
}

// plainGoMap is plainGoValue for a map[string]interface{}.
func plainGoMap(m map[string]interface{}) map[string]interface{} {
	if out, ok := plainGoValue(m).(map[string]interface{}); ok {
		return out
	}
	return m
}

// isPlainTree reports whether nothing in v needs converting. It
// returns false, without having decided anything, when the tree is
// deeper or larger than the limits allow.
func isPlainTree(v interface{}, depth int, budget *int) bool {
	switch t := v.(type) {
	case nil, string, bool, int, int64, float64, []byte:
		return true
	case map[string]interface{}:
		if depth > plainMaxDepth {
			return false
		}
		for _, e := range t {
			*budget--
			if *budget < 0 || !isPlainTree(e, depth+1, budget) {
				return false
			}
		}
		return true
	case []interface{}:
		if depth > plainMaxDepth {
			return false
		}
		for _, e := range t {
			*budget--
			if *budget < 0 || !isPlainTree(e, depth+1, budget) {
				return false
			}
		}
		return true
	}
	return !needsConversion(reflect.TypeOf(v))
}

// needsConversion reports whether a value of type t is a slice, array,
// or string-keyed map that plainGoValue rewrites.
func needsConversion(t reflect.Type) bool {
	k := t.Kind()
	if k == reflect.Map {
		return t.Key().Kind() == reflect.String
	}
	if k == reflect.Slice || k == reflect.Array {
		return t.Elem().Kind() != reflect.Uint8
	}
	return false
}

// plainVisit identifies a map or slice by its storage.
type plainVisit struct {
	ptr uintptr
	typ reflect.Type
	n   int
}

type plainer struct {
	active map[plainVisit]struct{}
	done   map[plainVisit]interface{}
}

func (p *plainer) value(v interface{}) interface{} {
	switch v.(type) {
	case nil, string, bool, int, int64, float64, []byte:
		return v
	}
	rv := reflect.ValueOf(v)
	if !needsConversion(rv.Type()) {
		return v
	}
	if rv.Kind() == reflect.Array || rv.Pointer() == 0 {
		return p.convert(v, rv)
	}
	k := plainVisit{ptr: rv.Pointer(), typ: rv.Type(), n: rv.Len()}
	if res, ok := p.done[k]; ok {
		return res
	}
	if _, busy := p.active[k]; busy {
		return v
	}
	p.active[k] = struct{}{}
	res := p.convert(v, rv)
	delete(p.active, k)
	p.done[k] = res
	return res
}

func (p *plainer) convert(v interface{}, rv reflect.Value) interface{} {
	switch t := v.(type) {
	case map[string]interface{}:
		return p.plainMap(t)
	case []interface{}:
		return p.plainSlice(t)
	}
	return p.reflected(rv)
}

// plainMap normalizes the values of m, copying m only when one of
// them changes.
func (p *plainer) plainMap(m map[string]interface{}) map[string]interface{} {
	var out map[string]interface{}
	for k, e := range m {
		ne := p.value(e)
		if out == nil && !sameValue(e, ne) {
			out = make(map[string]interface{}, len(m))
			for k2, e2 := range m {
				out[k2] = e2
			}
		}
		if out != nil {
			out[k] = ne
		}
	}
	if out == nil {
		return m
	}
	return out
}

// plainSlice normalizes the elements of s, copying s only when one of
// them changes.
func (p *plainer) plainSlice(s []interface{}) []interface{} {
	var out []interface{}
	for i, e := range s {
		ne := p.value(e)
		if out == nil && !sameValue(e, ne) {
			out = make([]interface{}, len(s))
			copy(out, s)
		}
		if out != nil {
			out[i] = ne
		}
	}
	if out == nil {
		return s
	}
	return out
}

// reflected converts a typed slice, array, or string-keyed map.
func (p *plainer) reflected(rv reflect.Value) interface{} {
	if rv.Kind() == reflect.Map {
		out := make(map[string]interface{}, rv.Len())
		iter := rv.MapRange()
		for iter.Next() {
			out[iter.Key().String()] = p.value(iter.Value().Interface())
		}
		return out
	}
	out := make([]interface{}, rv.Len())
	for i := range out {
		out[i] = p.value(rv.Index(i).Interface())
	}
	return out
}

// sameValue reports whether plainGoValue left a value untouched. Only
// maps and slices can change, and a rebuilt one is a different object.
func sameValue(a, b interface{}) bool {
	switch x := a.(type) {
	case map[string]interface{}:
		y, ok := b.(map[string]interface{})
		return ok && reflect.ValueOf(x).Pointer() == reflect.ValueOf(y).Pointer()
	case []interface{}:
		y, ok := b.([]interface{})
		if !ok || len(x) != len(y) {
			return false
		}
		return len(x) == 0 || &x[0] == &y[0]
	}
	return reflect.TypeOf(a) == reflect.TypeOf(b)
}
