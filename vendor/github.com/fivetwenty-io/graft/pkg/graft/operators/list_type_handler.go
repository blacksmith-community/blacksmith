package operators

import (
	"fmt"
	"reflect"
)

// ListTypeHandler handles operations on list types (TypeList).
type ListTypeHandler struct {
	*BaseTypeHandler
	derived DerivedComparisons[*ListTypeHandler]
}

// NewListTypeHandler creates a new list type handler.
func NewListTypeHandler() *ListTypeHandler {
	handler := &ListTypeHandler{
		BaseTypeHandler: NewBaseTypeHandler(70), // Higher priority than numeric/string handlers
	}
	handler.derived = NewDerivedComparisons(handler)

	// Add supported type combinations
	handler.AddSupportedTypes(
		TypePair{A: TypeList, B: TypeList}, // list + list, list == list, etc.
		TypePair{A: TypeList, B: TypeInt},  // list * int for repetition
	)

	return handler
}

// Add implements list concatenation
// For lists, + means concatenate the second list to the first.
func (h *ListTypeHandler) Add(a, b interface{}) (interface{}, error) {
	listA, okA := convertToList(a)
	listB, okB := convertToList(b)

	if !okA || !okB {
		return nil, NotImplementedError("add", a, b)
	}

	// Create a new list with all elements from both lists
	result := make([]interface{}, 0, len(listA)+len(listB))
	result = append(result, listA...)
	result = append(result, listB...)

	return result, nil
}

// Subtract is not meaningful for lists.
func (h *ListTypeHandler) Subtract(a, b interface{}) (interface{}, error) {
	return nil, NotImplementedError("subtract", a, b)
}

// Multiply implements list repetition
// For lists, * means repeat the list n times.
func (h *ListTypeHandler) Multiply(a, b interface{}) (interface{}, error) {
	// Check if we have list * int
	list, isList := convertToList(a)
	times, isInt := convertToInt(b)

	if !isList || !isInt {
		// Try the reverse: int * list
		times, isInt = convertToInt(a)
		list, isList = convertToList(b)

		if !isList || !isInt {
			return nil, NotImplementedError("multiply", a, b)
		}
	}

	if times < 0 {
		return nil, fmt.Errorf("cannot repeat list negative times: %d", times)
	}

	if times == 0 {
		return []interface{}{}, nil
	}

	// Prevent excessive memory usage
	const maxRepetitions = 10000
	if len(list) > 0 && times > int64(maxRepetitions/len(list)) {
		return nil, fmt.Errorf("list repetition too large (would result in %d elements, max %d)",
			len(list)*int(times), maxRepetitions)
	}

	// Create repeated list
	result := make([]interface{}, 0, len(list)*int(times))
	for i := 0; i < int(times); i++ {
		result = append(result, list...)
	}

	return result, nil
}

// Divide is not meaningful for lists.
func (h *ListTypeHandler) Divide(a, b interface{}) (interface{}, error) {
	return nil, NotImplementedError("divide", a, b)
}

// Modulo is not meaningful for lists.
func (h *ListTypeHandler) Modulo(a, b interface{}) (interface{}, error) {
	return nil, NotImplementedError("modulo", a, b)
}

// Equal checks if two lists are equal (same length and elements in same order).
func (h *ListTypeHandler) Equal(a, b interface{}) (bool, error) {
	listA, okA := convertToList(a)
	listB, okB := convertToList(b)

	if !okA || !okB {
		return false, NotImplementedError("equal", a, b)
	}

	// Different lengths means not equal
	if len(listA) != len(listB) {
		return false, nil
	}

	// Check that all elements match in order
	for i, vA := range listA {
		vB := listB[i]

		// Use deep equality check
		if !reflect.DeepEqual(vA, vB) {
			return false, nil
		}
	}

	return true, nil
}

// NotEqual checks if two lists are not equal.
func (h *ListTypeHandler) NotEqual(a, b interface{}) (bool, error) {
	return h.derived.NotEqual(a, b)
}

// Less is not meaningful for lists (would need to define ordering).
func (h *ListTypeHandler) Less(a, b interface{}) (bool, error) {
	return false, NotImplementedError("less", a, b)
}

// Greater is not meaningful for lists.
func (h *ListTypeHandler) Greater(a, b interface{}) (bool, error) {
	return false, NotImplementedError("greater", a, b)
}

// LessOrEqual is not meaningful for lists.
func (h *ListTypeHandler) LessOrEqual(a, b interface{}) (bool, error) {
	return false, NotImplementedError("lessOrEqual", a, b)
}

// GreaterOrEqual is not meaningful for lists.
func (h *ListTypeHandler) GreaterOrEqual(a, b interface{}) (bool, error) {
	return false, NotImplementedError("greaterOrEqual", a, b)
}

// CanHandle overrides the base implementation to handle special cases.
func (h *ListTypeHandler) CanHandle(aType, bType OperandType) bool {
	// Standard list-list operations
	if h.BaseTypeHandler.CanHandle(aType, bType) {
		return true
	}

	// Special case: list * int or int * list for repetition
	if (aType == TypeList && bType == TypeInt) || (aType == TypeInt && bType == TypeList) {
		return true
	}

	return false
}

// convertToList converts various list types to []interface{}.
func convertToList(val interface{}) ([]interface{}, bool) {
	if val == nil {
		return nil, false
	}

	switch l := val.(type) {
	case []interface{}:
		return l, true
	default:
		// Use reflection to handle other slice/array types
		rv := reflect.ValueOf(val)
		if rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array {
			return nil, false
		}

		result := make([]interface{}, rv.Len())
		for i := 0; i < rv.Len(); i++ {
			result[i] = rv.Index(i).Interface()
		}
		return result, true
	}
}

// convertToInt converts various integer types to int64.
func convertToInt(val interface{}) (int64, bool) {
	switch v := val.(type) {
	case int:
		return int64(v), true
	case int8:
		return int64(v), true
	case int16:
		return int64(v), true
	case int32:
		return int64(v), true
	case int64:
		return v, true
	case uint:
		if v <= 9223372036854775807 { // Max int64
			return int64(v), true
		}
		return 0, false
	case uint8:
		return int64(v), true
	case uint16:
		return int64(v), true
	case uint32:
		return int64(v), true
	case uint64:
		if v <= 9223372036854775807 { // Max int64
			return int64(v), true
		}
		return 0, false
	default:
		return 0, false
	}
}
