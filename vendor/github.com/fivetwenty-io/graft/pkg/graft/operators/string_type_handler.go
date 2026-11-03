package operators

import (
	"fmt"
	"math"
	"strings"
)

// StringTypeHandler handles operations for string types.
type StringTypeHandler struct {
	*BaseTypeHandler
	derived DerivedComparisons[*StringTypeHandler]
}

// NewStringTypeHandler creates a new handler for string operations.
func NewStringTypeHandler() *StringTypeHandler {
	handler := &StringTypeHandler{
		BaseTypeHandler: NewBaseTypeHandler(90), // High priority for string operations
	}
	handler.derived = NewDerivedComparisons(handler)

	// Support string-string operations and string-int for multiplication
	handler.AddSupportedTypes(
		TypePair{A: TypeString, B: TypeString},
		TypePair{A: TypeString, B: TypeInt}, // For repetition
		TypePair{A: TypeInt, B: TypeString}, // For repetition (reversed)
	)

	return handler
}

// Add performs string concatenation
// string + string -> concatenation
// string + any -> concatenation (converts other value to string).
func (h *StringTypeHandler) Add(a, b interface{}) (interface{}, error) {
	// For string + string, concatenate
	if aStr, aOk := a.(string); aOk {
		if bStr, bOk := b.(string); bOk {
			return aStr + bStr, nil
		}
		// String + other type: convert to string and concatenate
		return aStr + fmt.Sprintf("%v", b), nil
	}

	return nil, NotImplementedError("add", a, b)
}

// Subtract is not supported for strings.
func (h *StringTypeHandler) Subtract(a, b interface{}) (interface{}, error) {
	return nil, fmt.Errorf("subtract operation not supported for string type")
}

// Multiply performs string repetition when multiplying by an integer
// string * int -> repeated string (with 10000 character limit)
// int * string -> repeated string (commutative).
func (h *StringTypeHandler) Multiply(a, b interface{}) (interface{}, error) {
	// String * int = repeated string
	if aStr, aOk := a.(string); aOk {
		if bInt, err := toIntForString(b); err == nil {
			return repeatString(aStr, bInt)
		}
	}

	// Int * string = repeated string (commutative)
	if aInt, err := toIntForString(a); err == nil {
		if bStr, bOk := b.(string); bOk {
			return repeatString(bStr, aInt)
		}
	}

	return nil, NotImplementedError("multiply", a, b)
}

// repeatString repeats a string n times with safety limits.
func repeatString(s string, n int64) (interface{}, error) {
	if n < 0 {
		return nil, fmt.Errorf("cannot repeat string negative times: %d", n)
	}
	if n == 0 {
		return "", nil
	}

	const maxRepetitions = 10000
	if n > maxRepetitions {
		return nil, fmt.Errorf("string repetition count too large: %d (max %d)", n, maxRepetitions)
	}

	// Also check resulting length to prevent memory issues
	if s != "" && n > int64(maxRepetitions/len(s)) {
		return nil, fmt.Errorf("string repetition would exceed maximum length")
	}

	return strings.Repeat(s, int(n)), nil
}

// Divide is not supported for strings.
func (h *StringTypeHandler) Divide(a, b interface{}) (interface{}, error) {
	return nil, fmt.Errorf("divide operation not supported for string type")
}

// Modulo is not supported for strings.
func (h *StringTypeHandler) Modulo(a, b interface{}) (interface{}, error) {
	return nil, fmt.Errorf("modulo operation not supported for string type")
}

// Equal performs string equality comparison.
func (h *StringTypeHandler) Equal(a, b interface{}) (bool, error) {
	aStr, aOk := a.(string)
	bStr, bOk := b.(string)

	if aOk && bOk {
		return aStr == bStr, nil
	}

	// If one is string and other isn't, they're not equal
	if aOk || bOk {
		return false, nil
	}

	return false, NotImplementedError("equal", a, b)
}

// NotEqual performs string inequality comparison.
func (h *StringTypeHandler) NotEqual(a, b interface{}) (bool, error) {
	return h.derived.NotEqual(a, b)
}

// Less performs lexicographic comparison.
func (h *StringTypeHandler) Less(a, b interface{}) (bool, error) {
	aStr, aOk := a.(string)
	bStr, bOk := b.(string)

	if aOk && bOk {
		return aStr < bStr, nil
	}

	return false, NotImplementedError("less", a, b)
}

// Greater performs lexicographic comparison.
func (h *StringTypeHandler) Greater(a, b interface{}) (bool, error) {
	aStr, aOk := a.(string)
	bStr, bOk := b.(string)

	if aOk && bOk {
		return aStr > bStr, nil
	}

	return false, NotImplementedError("greater", a, b)
}

// LessOrEqual performs lexicographic comparison.
func (h *StringTypeHandler) LessOrEqual(a, b interface{}) (bool, error) {
	return h.derived.LessOrEqual(a, b)
}

// GreaterOrEqual performs lexicographic comparison.
func (h *StringTypeHandler) GreaterOrEqual(a, b interface{}) (bool, error) {
	return h.derived.GreaterOrEqual(a, b)
}

// CanHandle checks if this handler can handle the given type combination.
func (h *StringTypeHandler) CanHandle(aType, bType OperandType) bool {
	// Handle string with any type for concatenation (Add operation)
	if aType == TypeString || bType == TypeString {
		return true
	}
	return h.BaseTypeHandler.CanHandle(aType, bType)
}

// toIntForString converts a value to int64 if possible (for string operations).
func toIntForString(val interface{}) (int64, error) {
	switch v := val.(type) {
	case int:
		return int64(v), nil
	case int8:
		return int64(v), nil
	case int16:
		return int64(v), nil
	case int32:
		return int64(v), nil
	case int64:
		return v, nil
	case uint:
		if uint64(v) > uint64(math.MaxInt64) {
			return 0, fmt.Errorf("uint value %d overflows int64", v)
		}
		return int64(v), nil // #nosec G115 - bounds checked above
	case uint8:
		return int64(v), nil
	case uint16:
		return int64(v), nil
	case uint32:
		return int64(v), nil
	case uint64:
		if v <= 9223372036854775807 { // Max int64
			return int64(v), nil
		}
		return 0, fmt.Errorf("uint64 value %d exceeds int64 range", v)
	case float32:
		return int64(v), nil
	case float64:
		return int64(v), nil
	default:
		return 0, fmt.Errorf("cannot convert %T to int", val)
	}
}

//nolint:gochecknoinits // Type handler registration must happen at package load time
func init() {
	// Register the string type handler with the global registry
	GetGlobalThreadSafeRegistry().Register(NewStringTypeHandler())
}
