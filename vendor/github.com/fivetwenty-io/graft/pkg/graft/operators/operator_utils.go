package operators

import (
	"fmt"
	"math"
	"reflect"
)

// isTruthy determines if a value is "truthy" according to Graft's rules
// false, nil, 0, "", [], {} are falsy
// Everything else is truthy.
//
//nolint:gocyclo // type switch handles all Go types for truthiness check
func isTruthy(v interface{}) bool {
	if v == nil {
		return false
	}

	// Check for boolean
	if b, ok := v.(bool); ok {
		return b
	}

	// Check for numeric zero
	switch num := v.(type) {
	case int:
		return num != 0
	case int8:
		return num != 0
	case int16:
		return num != 0
	case int32:
		return num != 0
	case int64:
		return num != 0
	case uint:
		return num != 0
	case uint8:
		return num != 0
	case uint16:
		return num != 0
	case uint32:
		return num != 0
	case uint64:
		return num != 0
	case float32:
		return num != 0
	case float64:
		return num != 0
	}

	// Check for empty string
	if s, ok := v.(string); ok {
		return s != ""
	}

	// Check for empty slice/array/map using reflection
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Slice, reflect.Array:
		return rv.Len() > 0
	case reflect.Map:
		return rv.Len() > 0
	case reflect.Invalid, reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64, reflect.Complex64, reflect.Complex128,
		reflect.Chan, reflect.Func, reflect.Interface, reflect.Pointer, reflect.String, reflect.Struct, reflect.UnsafePointer:
		// Fall through
	}

	// Everything else is truthy
	return true
}

// toNumeric converts a value to a numeric type (int64 or float64)
// Returns the numeric value and nil error if successful
// Returns nil and error if the value cannot be converted to a number.
//
//nolint:gocyclo // type switch handles all Go numeric types
func toNumeric(val interface{}) (interface{}, error) {
	if val == nil {
		return int64(0), nil
	}

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
		if v <= math.MaxInt64 {
			return int64(v), nil
		}
		return float64(v), nil
	case float32:
		return float64(v), nil
	case float64:
		return v, nil
	case string:
		return nil, fmt.Errorf("cannot use string '%s' in arithmetic operation", v)
	case bool:
		return nil, fmt.Errorf("cannot use boolean '%v' in arithmetic operation", v)
	default:
		return nil, fmt.Errorf("cannot convert %T to numeric value", val)
	}
}

// toFloat converts a single numeric value to float64.
func toFloat(val interface{}) float64 {
	switch v := val.(type) {
	case int64:
		return float64(v)
	case float64:
		return v
	default:
		// This should not happen if toNumeric was called first
		return 0
	}
}

// performArithmetic executes an arithmetic operation and returns the result
// It maintains type consistency: int op int = int, anything with float = float
// This is a legacy fallback function used when no type handler is available.
//
//nolint:gocyclo // arithmetic operations with overflow checking is inherently complex
func performArithmetic(a, b interface{}, op string) (interface{}, error) {
	aNum, err := toNumeric(a)
	if err != nil {
		return nil, fmt.Errorf("left operand: %w", err)
	}

	bNum, err := toNumeric(b)
	if err != nil {
		return nil, fmt.Errorf("right operand: %w", err)
	}

	// Check if either operand is a float
	_, aIsFloat := aNum.(float64)
	_, bIsFloat := bNum.(float64)

	if aIsFloat || bIsFloat || op == "/" {
		// Promote to float for float operations or division
		aFloat := toFloat(aNum)
		bFloat := toFloat(bNum)

		switch op {
		case "+":
			return aFloat + bFloat, nil
		case "-":
			return aFloat - bFloat, nil
		case "*":
			return aFloat * bFloat, nil
		case "/":
			if bFloat == 0 {
				return nil, fmt.Errorf("division by zero")
			}
			return aFloat / bFloat, nil
		case "%":
			return nil, fmt.Errorf("modulo operation requires integer operands")
		default:
			return nil, fmt.Errorf("unknown arithmetic operator: %s", op)
		}
	} else {
		// Both are integers, keep as integer except for division
		aInt, _ := aNum.(int64)
		bInt, _ := bNum.(int64)

		switch op {
		case "+":
			result := aInt + bInt
			// Check for overflow
			if (result > aInt) != (bInt > 0) {
				// Overflow occurred, promote to float
				return float64(aInt) + float64(bInt), nil
			}
			return result, nil
		case "-":
			result := aInt - bInt
			// Check for overflow
			if (result < aInt) != (bInt > 0) {
				// Overflow occurred, promote to float
				return float64(aInt) - float64(bInt), nil
			}
			return result, nil
		case "*":
			result := aInt * bInt
			// Check for overflow
			if aInt != 0 && result/aInt != bInt {
				// Overflow occurred, promote to float
				return float64(aInt) * float64(bInt), nil
			}
			return result, nil
		case "%":
			if bInt == 0 {
				return nil, fmt.Errorf("modulo by zero")
			}
			return aInt % bInt, nil
		default:
			return nil, fmt.Errorf("unknown arithmetic operator: %s", op)
		}
	}
}

// legacyEqual performs deep equality comparison for legacy comparison support.
func legacyEqual(a, b interface{}) bool {
	// Handle nil cases
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}

	// Try numeric comparison first
	aNum, aIsNum := legacyToFloat64(a)
	bNum, bIsNum := legacyToFloat64(b)
	if aIsNum && bIsNum {
		return aNum == bNum
	}

	// Use reflect.DeepEqual for other types
	return reflect.DeepEqual(a, b)
}

// legacyCompare performs ordering comparison for legacy comparison support
// Returns -1 if a < b, 0 if a == b, 1 if a > b.
//
//nolint:gocyclo,dupl // comparison logic with type coercion is inherently complex
func legacyCompare(a, b interface{}) (int, error) {
	// Handle nil cases
	if a == nil && b == nil {
		return 0, nil
	}
	if a == nil {
		return -1, nil // nil is less than any value
	}
	if b == nil {
		return 1, nil // any value is greater than nil
	}

	// Try numeric comparison
	aNum, aIsNum := legacyToFloat64(a)
	bNum, bIsNum := legacyToFloat64(b)
	if aIsNum && bIsNum {
		if aNum < bNum {
			return -1, nil
		} else if aNum > bNum {
			return 1, nil
		}
		return 0, nil
	}

	// Try string comparison
	aStr, aIsStr := a.(string)
	bStr, bIsStr := b.(string)
	if aIsStr && bIsStr {
		if aStr < bStr {
			return -1, nil
		} else if aStr > bStr {
			return 1, nil
		}
		return 0, nil
	}

	// If types don't match, convert to strings
	if aIsNum && bIsStr {
		aStr = fmt.Sprintf("%v", a)
		if aStr < bStr {
			return -1, nil
		} else if aStr > bStr {
			return 1, nil
		}
		return 0, nil
	}
	if aIsStr && bIsNum {
		bStr = fmt.Sprintf("%v", b)
		if aStr < bStr {
			return -1, nil
		} else if aStr > bStr {
			return 1, nil
		}
		return 0, nil
	}

	// Can't compare other types
	return 0, fmt.Errorf("cannot compare %T and %T", a, b)
}

// NotImplementedError returns an error for operations not implemented by a handler.
func NotImplementedError(op string, a, b interface{}) error {
	return fmt.Errorf("%s operation not supported for types %T and %T", op, a, b)
}

// legacyToFloat64 attempts to convert a value to float64 for legacy comparison.
func legacyToFloat64(v interface{}) (float64, bool) {
	switch val := v.(type) {
	case float64:
		return val, true
	case int64:
		return float64(val), true
	case int:
		return float64(val), true
	case float32:
		return float64(val), true
	case int32:
		return float64(val), true
	case int16:
		return float64(val), true
	case int8:
		return float64(val), true
	case uint:
		return float64(val), true
	case uint64:
		return float64(val), true
	case uint32:
		return float64(val), true
	case uint16:
		return float64(val), true
	case uint8:
		return float64(val), true
	}
	return 0, false
}
