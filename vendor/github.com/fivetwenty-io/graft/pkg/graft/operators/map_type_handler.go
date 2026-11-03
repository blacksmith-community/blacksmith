package operators

import (
	"fmt"
	"reflect"
)

// MapTypeHandler handles operations on map types (TypeMap).
type MapTypeHandler struct {
	*BaseTypeHandler
	derived DerivedComparisons[*MapTypeHandler]
}

// NewMapTypeHandler creates a new map type handler.
func NewMapTypeHandler() *MapTypeHandler {
	handler := &MapTypeHandler{
		BaseTypeHandler: NewBaseTypeHandler(70), // Higher priority than numeric/string handlers
	}
	handler.derived = NewDerivedComparisons(handler)

	// Add supported type combinations
	handler.AddSupportedTypes(
		TypePair{A: TypeMap, B: TypeMap}, // map + map, map == map, etc.
	)

	return handler
}

// Add implements map concatenation (merging)
// For maps, + means merge the second map into the first.
func (h *MapTypeHandler) Add(a, b interface{}) (interface{}, error) {
	mapA, okA := convertToMap(a)
	mapB, okB := convertToMap(b)

	if !okA || !okB {
		return nil, NotImplementedError("add", a, b)
	}

	// Create a new map with all entries from both maps
	// If there are conflicts, the second map (b) takes precedence
	result := make(map[string]interface{})

	// Copy all entries from mapA
	for k, v := range mapA {
		result[k] = v
	}

	// Merge entries from mapB, overwriting duplicates
	for k, v := range mapB {
		result[k] = v
	}

	return result, nil
}

// Subtract is not meaningful for maps.
func (h *MapTypeHandler) Subtract(a, b interface{}) (interface{}, error) {
	return nil, NotImplementedError("subtract", a, b)
}

// Multiply is not meaningful for maps.
func (h *MapTypeHandler) Multiply(a, b interface{}) (interface{}, error) {
	return nil, NotImplementedError("multiply", a, b)
}

// Divide is not meaningful for maps.
func (h *MapTypeHandler) Divide(a, b interface{}) (interface{}, error) {
	return nil, NotImplementedError("divide", a, b)
}

// Modulo is not meaningful for maps.
func (h *MapTypeHandler) Modulo(a, b interface{}) (interface{}, error) {
	return nil, NotImplementedError("modulo", a, b)
}

// Equal checks if two maps are equal (same keys and values).
func (h *MapTypeHandler) Equal(a, b interface{}) (bool, error) {
	mapA, okA := convertToMap(a)
	mapB, okB := convertToMap(b)

	if !okA || !okB {
		return false, NotImplementedError("equal", a, b)
	}

	// Different number of keys means not equal
	if len(mapA) != len(mapB) {
		return false, nil
	}

	// Check that all keys and values match
	for k, vA := range mapA {
		vB, exists := mapB[k]
		if !exists {
			return false, nil
		}

		// Use deep equality check
		if !reflect.DeepEqual(vA, vB) {
			return false, nil
		}
	}

	return true, nil
}

// NotEqual checks if two maps are not equal.
func (h *MapTypeHandler) NotEqual(a, b interface{}) (bool, error) {
	return h.derived.NotEqual(a, b)
}

// Less is not meaningful for maps.
func (h *MapTypeHandler) Less(a, b interface{}) (bool, error) {
	return false, NotImplementedError("less", a, b)
}

// Greater is not meaningful for maps.
func (h *MapTypeHandler) Greater(a, b interface{}) (bool, error) {
	return false, NotImplementedError("greater", a, b)
}

// LessOrEqual is not meaningful for maps.
func (h *MapTypeHandler) LessOrEqual(a, b interface{}) (bool, error) {
	return false, NotImplementedError("lessOrEqual", a, b)
}

// GreaterOrEqual is not meaningful for maps.
func (h *MapTypeHandler) GreaterOrEqual(a, b interface{}) (bool, error) {
	return false, NotImplementedError("greaterOrEqual", a, b)
}

// convertToMap converts various map types to map[string]interface{}.
func convertToMap(val interface{}) (map[string]interface{}, bool) {
	if val == nil {
		return nil, false
	}

	switch m := val.(type) {
	case map[string]interface{}:
		return m, true
	default:
		// Use reflection to handle other map types
		rv := reflect.ValueOf(val)
		if rv.Kind() != reflect.Map {
			return nil, false
		}

		result := make(map[string]interface{})
		for _, key := range rv.MapKeys() {
			result[fmt.Sprintf("%v", key.Interface())] = rv.MapIndex(key).Interface()
		}
		return result, true
	}
}
