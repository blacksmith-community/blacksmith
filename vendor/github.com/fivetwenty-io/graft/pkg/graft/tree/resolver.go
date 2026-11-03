package tree

import (
	"fmt"
	"reflect"
	"regexp"
	"strconv"
)

// predicateSegmentPattern matches a list-predicate path segment such as
// "name=primary": a field name, "=", and the value
// to match against that field on each map element of a list. Both the
// bracketed form ("servers[name=primary]") and the dotted form
// ("servers.name=primary") produce this exact segment string via
// ParseCursor/the tokenizer, so one regex and one search serves both.
var predicateSegmentPattern = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_-]*)=(.*)$`)

// parsePredicateSegment reports whether node is a list-predicate segment
// ("field=value") and, if so, returns the field and value to match.
func parsePredicateSegment(node string) (field, value string, ok bool) {
	m := predicateSegmentPattern.FindStringSubmatch(node)
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}

// IsPredicateSegment reports whether node has the "field=value" shape
// Resolve/Canonical/Glob treat as a list predicate. Exported so callers
// outside this package — notably
// operators.isDynamicBracketNode, which decides whether a bracketed
// grab segment is a dynamic key reference or a literal node — can apply the
// identical rule without duplicating the regex.
func IsPredicateSegment(node string) bool {
	_, _, ok := parsePredicateSegment(node)
	return ok
}

// ParsePredicateSegment is parsePredicateSegment, exported so callers
// outside this package — notably mergeBuilderImpl's cherry-pick/prune path
// navigation — can recognize a "field=value" segment using the identical
// rule Resolve/Canonical/Glob use, rather than duplicating the regex.
func ParsePredicateSegment(node string) (field, value string, ok bool) {
	return parsePredicateSegment(node)
}

// PredicateFind is predicateFind, exported so callers outside this package
// can match a list element by an explicit field=value predicate using the
// exact same first-match, list-containers-only semantics Resolve/Canonical/
// Glob use.
func PredicateFind(l []interface{}, field, value string) (interface{}, uint64, bool) {
	return predicateFind(l, field, value)
}

// predicateFind searches a list for the first map element whose `field` key
// stringifies equal to `value`. This generalizes listFind's fixed
// NameFields search to an explicit field name.
func predicateFind(l []interface{}, field, value string) (interface{}, uint64, bool) {
	for i, v := range l {
		idx := uint64(i) // #nosec G115 - i is from range loop, always >= 0
		m, ok := v.(map[string]interface{})
		if !ok {
			continue
		}
		fv, present := m[field]
		if !present {
			continue
		}
		if stringifyForPredicate(fv) == value {
			return v, idx, true
		}
	}
	return nil, 0, false
}

// stringifyForPredicate renders a map field's value for comparison against
// the string extracted from a "field=value" path segment. Path segments are
// always strings (they come from parsing text), so the field's value is
// stringified rather than the segment's value being parsed into a type.
func stringifyForPredicate(v interface{}) string {
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

// listFind searches for an item in a list by field name.
func listFind(l []interface{}, fields []string, key string) (interface{}, uint64, bool) {
	for _, field := range fields {
		for i, v := range l {
			// Convert index to uint64 safely
			idx := uint64(i) // #nosec G115 - i is from range loop, always >= 0

			if m, isMap := v.(map[string]interface{}); isMap {
				value, ok := m[field]
				if ok && value == key {
					return v, idx, true
				}
			}
		}
	}
	return nil, 0, false
}

// Canonical converts the cursor to canonical form based on the data structure.
func (c *Cursor) Canonical(o interface{}) (*Cursor, error) {
	canon := &Cursor{Nodes: []string{}}

	for _, k := range c.Nodes {
		switch val := o.(type) {
		case []interface{}:
			if field, value, isPredicate := parsePredicateSegment(k); isPredicate {
				found, idx, ok := predicateFind(val, field, value)
				if !ok {
					return nil, NotFoundError{
						Path: canon.Nodes,
					}
				}
				o = found
				canon.Push(fmt.Sprintf("%d", idx))
				break
			}

			i, err := strconv.ParseUint(k, 10, 0)
			if err == nil {
				// if k is an integer (in string form), go by index
				if i >= uint64(len(val)) {
					return nil, NotFoundError{
						Path: canon.Nodes,
					}
				}
				o = val[i]
			} else {
				// if k is a string, look for immediate map descendants who have
				//     'name', 'key' or 'id' fields matching k
				var found bool
				o, i, found = listFind(val, NameFields, k)
				if !found {
					return nil, NotFoundError{
						Path: canon.Nodes,
					}
				}
			}
			canon.Push(fmt.Sprintf("%d", i))

		case map[string]interface{}:
			canon.Push(k)
			var ok bool
			o, ok = val[k]
			if !ok {
				return nil, NotFoundError{
					Path: canon.Nodes,
				}
			}

		default:
			return nil, TypeMismatchError{
				Path:   canon.Nodes,
				Wanted: wantedContainer,
				Got:    gotScalar,
			}
		}
	}

	return canon, nil
}

// Resolve resolves the cursor path in the given data structure.
func (c *Cursor) Resolve(o interface{}) (interface{}, error) {
	path := make([]string, 0, len(c.Nodes))

	for _, k := range c.Nodes {
		path = append(path, k)

		switch val := o.(type) {
		case map[string]interface{}:
			v, ok := val[k]
			if !ok {
				return nil, NotFoundError{
					Path: path,
				}
			}
			o = v

		case []interface{}:
			if field, value, isPredicate := parsePredicateSegment(k); isPredicate {
				found, _, ok := predicateFind(val, field, value)
				if !ok {
					return nil, NotFoundError{
						Path: path,
					}
				}
				o = found
				continue
			}

			i, err := strconv.ParseUint(k, 10, 0)
			if err == nil {
				// if k is an integer (in string form), go by index
				if i >= uint64(len(val)) {
					return nil, NotFoundError{
						Path: path,
					}
				}
				o = val[i]
				continue
			}

			// if k is a string, look for immediate map descendants who have
			//     'name', 'key' or 'id' fields matching k
			var found bool
			o, _, found = listFind(val, NameFields, k)
			if !found {
				return nil, NotFoundError{
					Path: path,
				}
			}

		default:
			path = path[0 : len(path)-1]
			return nil, TypeMismatchError{
				Path:   path,
				Wanted: wantedContainer,
				Got:    gotScalar,
				Value:  o,
			}
		}
	}

	return o, nil
}

// ResolveString resolves the cursor path and returns the value as a string.
func (c *Cursor) ResolveString(tree interface{}) (string, error) {
	o, err := c.Resolve(tree)
	if err != nil {
		return "", err
	}

	switch val := o.(type) {
	case string:
		return val, nil
	case int:
		return fmt.Sprintf("%d", val), nil
	}
	return "", TypeMismatchError{
		Path:   c.Nodes,
		Wanted: "a string",
	}
}

// Find attempts to find the value at `path` inside data structure `o`.
// If found, returns it as a plain interface{} type, for you to
// typecheck + cast as you see fit. Errors will be
// returned for data of invalid type, or nonexistent paths.
func Find(o interface{}, path string) (interface{}, error) {
	c, err := ParseCursor(path)
	if err != nil {
		return nil, err
	}
	return c.Resolve(o)
}

// FindString attempts to find the value at `path` inside data structure `o`.
// If found, attempts to cast it as a string. Errors will be
// returned for data of invalid type, or nonexistent paths.
func FindString(o interface{}, path string) (string, error) {
	obj, err := Find(o, path)
	if err != nil {
		return "", err
	}
	if s, ok := obj.(string); ok {
		return s, nil
	}
	return "", fmt.Errorf("invalid data type - wanted string, got %s", reflect.TypeOf(obj))
}

// FindNum attempts to find the value at `path` inside data structure `o`.
// If found, attempts to cast it as a Number. Errors will be
// returned for data of invalid type, or nonexistent paths.
func FindNum(o interface{}, path string) (Number, error) {
	var num Number
	obj, err := Find(o, path)
	if err != nil {
		return num, err
	}
	switch v := obj.(type) {
	case float64:
		num = Number(v)
	case int:
		num = Number(float64(v))
	default:
		return num, fmt.Errorf("invalid data type - wanted number, got %s", reflect.TypeOf(obj))
	}
	return num, nil
}

// FindBool attempts to find the value at `path` inside data structure `o`.
// If found, attempts to cast it as a bool. Errors will be
// returned for data of invalid type, or nonexistent paths.
func FindBool(o interface{}, path string) (bool, error) {
	obj, err := Find(o, path)
	if err != nil {
		return false, err
	}
	if b, ok := obj.(bool); ok {
		return b, nil
	}
	return false, fmt.Errorf("invalid data type - wanted bool, got %s", reflect.TypeOf(obj))
}

// FindMap attempts to find the value at `path` inside data structure `o`.
// If found, attempts to cast it as a map[string]interface{}. Errors will be
// returned for data of invalid type, or nonexistent paths.
func FindMap(o interface{}, path string) (map[string]interface{}, error) {
	obj, err := Find(o, path)
	if err != nil {
		return map[string]interface{}{}, err
	}
	if m, ok := obj.(map[string]interface{}); ok {
		return m, nil
	}
	return map[string]interface{}{}, fmt.Errorf("invalid data type - wanted map, got %s", reflect.TypeOf(obj))
}

// FindArray attempts to find the value at `path` inside data structure `o`.
// If found, attempts to cast it as an interface{} slice. Errors will be
// returned for data of invalid type, or nonexistent paths.
func FindArray(o interface{}, path string) ([]interface{}, error) {
	obj, err := Find(o, path)
	if err != nil {
		return []interface{}{}, err
	}
	if arr, ok := obj.([]interface{}); ok {
		return arr, nil
	}
	return []interface{}{}, fmt.Errorf("invalid data type - wanted array, got %s", reflect.TypeOf(obj))
}

// Number represents a numeric value that can be cast to int64 or float64.
type Number float64

// Int64 returns an `int64` representation of the Number. If the value
// is not an integer, returns an error, so you do not accidentally
// lose precision while trying to see if it is an integer value.
func (n Number) Int64() (int64, error) {
	i := int64(n)
	if Number(i) != n {
		return 0, fmt.Errorf("%f does not represent an integer, cannot auto-convert", float64(n))
	}
	return i, nil
}

// Float64 returns a `float64` representation of the Number.
func (n Number) Float64() float64 {
	return float64(n)
}

// String returns a string representation of the Number.
func (n Number) String() string {
	intVal, err := n.Int64()
	if err == nil {
		return fmt.Sprintf("%d", intVal)
	}

	return fmt.Sprintf("%f", n.Float64())
}
