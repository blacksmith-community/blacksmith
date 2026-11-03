package operators

import (
	"fmt"
	"reflect"
	"strconv"
)

// MixedTypeHandler handles operations between different types as a fallback handler.
type MixedTypeHandler struct {
	*BaseTypeHandler
}

// NewMixedTypeHandler creates a new mixed type handler.
func NewMixedTypeHandler() *MixedTypeHandler {
	handler := &MixedTypeHandler{
		BaseTypeHandler: NewBaseTypeHandler(10), // Lowest priority - fallback handler
	}

	// This handler can handle any type combination that other handlers don't support
	// It uses CanHandle override instead of AddSupportedTypes

	return handler
}

// CanHandle returns true for any type combination not handled by higher-priority handlers.
func (h *MixedTypeHandler) CanHandle(aType, bType OperandType) bool {
	// This handler is a fallback, so it can handle any combination
	return true
}

// Add implements mixed-type addition with null-safe behavior and fallback to string concatenation.
func (h *MixedTypeHandler) Add(a, b interface{}) (interface{}, error) {
	// Null-safe operations: null + anything = anything
	if a == nil {
		return b, nil
	}
	if b == nil {
		return a, nil
	}

	// Try to convert to strings and concatenate as a fallback
	aStr := toString(a)
	bStr := toString(b)

	return aStr + bStr, nil
}

// Subtract is generally not meaningful for mixed types.
func (h *MixedTypeHandler) Subtract(a, b interface{}) (interface{}, error) {
	// Null-safe: anything - null = anything, null - anything = -anything (if numeric)
	if a == nil && b == nil {
		return nil, nil
	}
	if a == nil {
		// null - b: try to negate b if it's numeric
		if bNum, err := toNumeric(b); err == nil {
			switch v := bNum.(type) {
			case int64:
				return -v, nil
			case float64:
				return -v, nil
			}
		}
		return nil, NotImplementedError("subtract", a, b)
	}
	if b == nil {
		return a, nil // a - null = a
	}

	return nil, NotImplementedError("subtract", a, b)
}

// Multiply is generally not meaningful for mixed types.
func (h *MixedTypeHandler) Multiply(a, b interface{}) (interface{}, error) {
	// Null-safe: anything * null = null
	if a == nil || b == nil {
		return nil, nil
	}

	return nil, NotImplementedError("multiply", a, b)
}

// Divide is generally not meaningful for mixed types.
func (h *MixedTypeHandler) Divide(a, b interface{}) (interface{}, error) {
	// Null-safe: null / anything = null, anything / null = error
	if a == nil {
		return nil, nil
	}
	if b == nil {
		return nil, fmt.Errorf("division by null")
	}

	return nil, NotImplementedError("divide", a, b)
}

// Modulo is generally not meaningful for mixed types.
func (h *MixedTypeHandler) Modulo(a, b interface{}) (interface{}, error) {
	// Null-safe: null % anything = null, anything % null = error
	if a == nil {
		return nil, nil
	}
	if b == nil {
		return nil, fmt.Errorf("modulo by null")
	}

	return nil, NotImplementedError("modulo", a, b)
}

// Equal implements universal equality using deep comparison.
func (h *MixedTypeHandler) Equal(a, b interface{}) (bool, error) {
	// Handle null comparisons
	if a == nil && b == nil {
		return true, nil
	}
	if a == nil || b == nil {
		return false, nil
	}

	// Try type coercion for common cases
	if canCoerceToNumber(a) && canCoerceToNumber(b) {
		aNum, aErr := toNumeric(a)
		bNum, bErr := toNumeric(b)
		if aErr == nil && bErr == nil {
			// Compare as numbers using float64 conversion to handle int/float comparisons
			aFloat := convertToFloat64(aNum)
			bFloat := convertToFloat64(bNum)
			return aFloat == bFloat, nil
		}
	}

	// Try string comparison as fallback
	if canCoerceToString(a) && canCoerceToString(b) {
		return toString(a) == toString(b), nil
	}

	// Use deep equality as final fallback
	return reflect.DeepEqual(a, b), nil
}

// NotEqual implements universal inequality.
func (h *MixedTypeHandler) NotEqual(a, b interface{}) (bool, error) {
	equal, err := h.Equal(a, b)
	if err != nil {
		return false, err
	}
	return !equal, nil
}

// Less implements mixed-type less-than with type coercion.
func (h *MixedTypeHandler) Less(a, b interface{}) (bool, error) {
	// Handle null comparisons (null is less than everything except null)
	if a == nil && b == nil {
		return false, nil
	}
	if a == nil {
		return true, nil
	}
	if b == nil {
		return false, nil
	}

	// Try numeric comparison
	if canCoerceToNumber(a) && canCoerceToNumber(b) {
		aNum, aErr := toNumeric(a)
		bNum, bErr := toNumeric(b)
		if aErr == nil && bErr == nil {
			return compareNumbers(aNum, bNum) < 0, nil
		}
	}

	// Try string comparison only if both are actually strings
	aStr, aIsStr := a.(string)
	bStr, bIsStr := b.(string)
	if aIsStr && bIsStr {
		return aStr < bStr, nil
	}

	return false, NotImplementedError("less", a, b)
}

// Greater implements mixed-type greater-than with type coercion.
func (h *MixedTypeHandler) Greater(a, b interface{}) (bool, error) {
	// Handle null comparisons (nothing is greater than null except non-null)
	if a == nil && b == nil {
		return false, nil
	}
	if a == nil {
		return false, nil
	}
	if b == nil {
		return true, nil
	}

	// Try numeric comparison
	if canCoerceToNumber(a) && canCoerceToNumber(b) {
		aNum, aErr := toNumeric(a)
		bNum, bErr := toNumeric(b)
		if aErr == nil && bErr == nil {
			return compareNumbers(aNum, bNum) > 0, nil
		}
	}

	// Try string comparison only if both are actually strings
	aStr, aIsStr := a.(string)
	bStr, bIsStr := b.(string)
	if aIsStr && bIsStr {
		return aStr > bStr, nil
	}

	return false, NotImplementedError("greater", a, b)
}

// LessOrEqual implements mixed-type less-than-or-equal with type coercion.
func (h *MixedTypeHandler) LessOrEqual(a, b interface{}) (bool, error) {
	equal, err := h.Equal(a, b)
	if err == nil && equal {
		return true, nil
	}

	return h.Less(a, b)
}

// GreaterOrEqual implements mixed-type greater-than-or-equal with type coercion.
func (h *MixedTypeHandler) GreaterOrEqual(a, b interface{}) (bool, error) {
	equal, err := h.Equal(a, b)
	if err == nil && equal {
		return true, nil
	}

	return h.Greater(a, b)
}

// Helper functions

// toString converts any value to a string representation.
func toString(val interface{}) string {
	if val == nil {
		return ""
	}

	switch v := val.(type) {
	case string:
		return v
	case bool:
		if v {
			return "true"
		}
		return "false"
	case int, int8, int16, int32, int64:
		return fmt.Sprintf("%d", v)
	case uint, uint8, uint16, uint32, uint64:
		return fmt.Sprintf("%d", v)
	case float32, float64:
		return fmt.Sprintf("%g", v)
	default:
		return fmt.Sprintf("%v", v)
	}
}

// canCoerceToNumber checks if a value can be converted to a number.
func canCoerceToNumber(val interface{}) bool {
	if val == nil {
		return false
	}

	switch v := val.(type) {
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		return true
	case string:
		// Check if string represents a number
		_, err := strconv.ParseFloat(v, 64)
		return err == nil
	case bool:
		return true // booleans can be converted to 0/1
	default:
		return false
	}
}

// canCoerceToString checks if a value can be meaningfully converted to a string.
func canCoerceToString(val interface{}) bool {
	// Almost everything can be converted to a string
	return true
}

// compareNumbers compares two numeric values, returning -1, 0, or 1.
func compareNumbers(a, b interface{}) int {
	aFloat := convertToFloat64(a)
	bFloat := convertToFloat64(b)

	if aFloat < bFloat {
		return -1
	} else if aFloat > bFloat {
		return 1
	}
	return 0
}

// convertToFloat64 converts a numeric value to float64.
func convertToFloat64(val interface{}) float64 {
	switch v := val.(type) {
	case int:
		return float64(v)
	case int8:
		return float64(v)
	case int16:
		return float64(v)
	case int32:
		return float64(v)
	case int64:
		return float64(v)
	case uint:
		return float64(v)
	case uint8:
		return float64(v)
	case uint16:
		return float64(v)
	case uint32:
		return float64(v)
	case uint64:
		return float64(v)
	case float32:
		return float64(v)
	case float64:
		return v
	default:
		return 0
	}
}

//nolint:gochecknoinits // Type handler registration must happen at package load time
func init() {
	// Register the mixed type handler with the global registry
	GetGlobalThreadSafeRegistry().Register(NewMixedTypeHandler())
}
