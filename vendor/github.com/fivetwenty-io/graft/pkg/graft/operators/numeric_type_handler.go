package operators

import (
	"fmt"
	"math"
)

// NumericTypeHandler handles arithmetic and comparison operations for numeric types (int and float).
type NumericTypeHandler struct {
	*BaseTypeHandler
	derived DerivedComparisons[*NumericTypeHandler]
}

// NewNumericTypeHandler creates a new handler for numeric operations.
func NewNumericTypeHandler() *NumericTypeHandler {
	handler := &NumericTypeHandler{
		BaseTypeHandler: NewBaseTypeHandler(100), // Highest priority for numeric operations
	}
	handler.derived = NewDerivedComparisons(handler)

	// Support int-int, int-float, float-int, and float-float combinations
	handler.AddSupportedTypes(
		TypePair{A: TypeInt, B: TypeInt},
		TypePair{A: TypeInt, B: TypeFloat},
		TypePair{A: TypeFloat, B: TypeInt},
		TypePair{A: TypeFloat, B: TypeFloat},
	)

	return handler
}

// numericToFloat converts the result of toNumeric to float64
// Used internally by NumericTypeHandler methods.
func numericToFloat(val interface{}) float64 {
	switch v := val.(type) {
	case int64:
		return float64(v)
	case float64:
		return v
	default:
		return 0
	}
}

// numericToInteger converts a numeric value to int64, handling floats that represent whole numbers.
func numericToInteger(val interface{}) (int64, error) {
	switch v := val.(type) {
	case int64:
		return v, nil
	case float64:
		// Check if the float represents a whole number
		if v == math.Trunc(v) && v >= math.MinInt64 && v <= math.MaxInt64 {
			return int64(v), nil
		}
		return 0, fmt.Errorf("float %v is not a whole number or out of int64 range", v)
	default:
		return 0, fmt.Errorf("cannot convert %T to integer", val)
	}
}

// Add performs addition on numeric types with overflow detection
// Type coercion: int+int->int, int+float->float, float+float->float
// Overflow: promotes to float on integer overflow.
func (h *NumericTypeHandler) Add(a, b interface{}) (interface{}, error) {
	aNum, err := toNumeric(a)
	if err != nil {
		return nil, fmt.Errorf("cannot convert %v to numeric: %w", a, err)
	}

	bNum, err := toNumeric(b)
	if err != nil {
		return nil, fmt.Errorf("cannot convert %v to numeric: %w", b, err)
	}

	// If either operand is a float, result is float
	if aFloat, ok := aNum.(float64); ok {
		return aFloat + numericToFloat(bNum), nil
	}
	if bFloat, ok := bNum.(float64); ok {
		return numericToFloat(aNum) + bFloat, nil
	}

	// Both are integers
	aInt, _ := aNum.(int64)
	bInt, _ := bNum.(int64)

	// Check for overflow and convert to float if necessary
	if (bInt > 0 && aInt > math.MaxInt64-bInt) || (bInt < 0 && aInt < math.MinInt64-bInt) {
		// Overflow detected, promote to float
		return float64(aInt) + float64(bInt), nil
	}

	return aInt + bInt, nil
}

// Subtract performs subtraction on numeric types with overflow detection.
func (h *NumericTypeHandler) Subtract(a, b interface{}) (interface{}, error) {
	aNum, err := toNumeric(a)
	if err != nil {
		return nil, fmt.Errorf("cannot convert %v to numeric: %w", a, err)
	}

	bNum, err := toNumeric(b)
	if err != nil {
		return nil, fmt.Errorf("cannot convert %v to numeric: %w", b, err)
	}

	// If either operand is a float, result is float
	if aFloat, ok := aNum.(float64); ok {
		return aFloat - numericToFloat(bNum), nil
	}
	if bFloat, ok := bNum.(float64); ok {
		return numericToFloat(aNum) - bFloat, nil
	}

	// Both are integers
	aInt, _ := aNum.(int64)
	bInt, _ := bNum.(int64)

	// Check for overflow
	if (bInt < 0 && aInt > math.MaxInt64+bInt) || (bInt > 0 && aInt < math.MinInt64+bInt) {
		// Overflow detected, promote to float
		return float64(aInt) - float64(bInt), nil
	}

	return aInt - bInt, nil
}

// Multiply performs multiplication on numeric types with overflow detection.
func (h *NumericTypeHandler) Multiply(a, b interface{}) (interface{}, error) {
	aNum, err := toNumeric(a)
	if err != nil {
		return nil, fmt.Errorf("cannot convert %v to numeric: %w", a, err)
	}

	bNum, err := toNumeric(b)
	if err != nil {
		return nil, fmt.Errorf("cannot convert %v to numeric: %w", b, err)
	}

	// If either operand is a float, result is float
	if aFloat, ok := aNum.(float64); ok {
		return aFloat * numericToFloat(bNum), nil
	}
	if bFloat, ok := bNum.(float64); ok {
		return numericToFloat(aNum) * bFloat, nil
	}

	// Both are integers
	aInt, _ := aNum.(int64)
	bInt, _ := bNum.(int64)

	// Check for overflow
	if aInt != 0 && bInt != 0 {
		result := aInt * bInt
		if result/aInt != bInt {
			// Overflow detected, promote to float
			return float64(aInt) * float64(bInt), nil
		}
		return result, nil
	}

	return int64(0), nil
}

// Divide performs division on numeric types (always returns float64)
// Returns error on division by zero.
func (h *NumericTypeHandler) Divide(a, b interface{}) (interface{}, error) {
	aNum, err := toNumeric(a)
	if err != nil {
		return nil, fmt.Errorf("cannot convert %v to numeric: %w", a, err)
	}

	bNum, err := toNumeric(b)
	if err != nil {
		return nil, fmt.Errorf("cannot convert %v to numeric: %w", b, err)
	}

	bFloat := numericToFloat(bNum)
	if bFloat == 0 {
		return nil, fmt.Errorf("division by zero")
	}

	return numericToFloat(aNum) / bFloat, nil
}

// Modulo performs modulo operation on numeric types
// Requires integer operands; returns error for non-integers or division by zero.
func (h *NumericTypeHandler) Modulo(a, b interface{}) (interface{}, error) {
	aNum, err := toNumeric(a)
	if err != nil {
		return nil, fmt.Errorf("cannot convert %v to numeric: %w", a, err)
	}

	bNum, err := toNumeric(b)
	if err != nil {
		return nil, fmt.Errorf("cannot convert %v to numeric: %w", b, err)
	}

	// Convert operands to integers, handling floats that represent whole numbers
	aInt, err := numericToInteger(aNum)
	if err != nil {
		return nil, fmt.Errorf("modulo requires integer operands: left operand is not an integer")
	}

	bInt, err := numericToInteger(bNum)
	if err != nil {
		return nil, fmt.Errorf("modulo requires integer operands: right operand is not an integer")
	}

	if bInt == 0 {
		return nil, fmt.Errorf("modulo by zero")
	}

	return aInt % bInt, nil
}

// Equal performs equality comparison on numeric types.
func (h *NumericTypeHandler) Equal(a, b interface{}) (bool, error) {
	aNum, err := toNumeric(a)
	if err != nil {
		return false, fmt.Errorf("cannot convert %v to numeric: %w", a, err)
	}

	bNum, err := toNumeric(b)
	if err != nil {
		return false, fmt.Errorf("cannot convert %v to numeric: %w", b, err)
	}

	// Convert both to float for comparison to handle int-float comparisons
	return numericToFloat(aNum) == numericToFloat(bNum), nil
}

// NotEqual performs inequality comparison on numeric types.
func (h *NumericTypeHandler) NotEqual(a, b interface{}) (bool, error) {
	return h.derived.NotEqual(a, b)
}

// Less performs less-than comparison on numeric types.
func (h *NumericTypeHandler) Less(a, b interface{}) (bool, error) {
	aNum, err := toNumeric(a)
	if err != nil {
		return false, fmt.Errorf("cannot convert %v to numeric: %w", a, err)
	}

	bNum, err := toNumeric(b)
	if err != nil {
		return false, fmt.Errorf("cannot convert %v to numeric: %w", b, err)
	}

	// Convert both to float for comparison
	return numericToFloat(aNum) < numericToFloat(bNum), nil
}

// Greater performs greater-than comparison on numeric types.
func (h *NumericTypeHandler) Greater(a, b interface{}) (bool, error) {
	aNum, err := toNumeric(a)
	if err != nil {
		return false, fmt.Errorf("cannot convert %v to numeric: %w", a, err)
	}

	bNum, err := toNumeric(b)
	if err != nil {
		return false, fmt.Errorf("cannot convert %v to numeric: %w", b, err)
	}

	// Convert both to float for comparison
	return numericToFloat(aNum) > numericToFloat(bNum), nil
}

// LessOrEqual performs less-than-or-equal comparison on numeric types.
func (h *NumericTypeHandler) LessOrEqual(a, b interface{}) (bool, error) {
	return h.derived.LessOrEqual(a, b)
}

// GreaterOrEqual performs greater-than-or-equal comparison on numeric types.
func (h *NumericTypeHandler) GreaterOrEqual(a, b interface{}) (bool, error) {
	return h.derived.GreaterOrEqual(a, b)
}

//nolint:gochecknoinits // Type handler registration must happen at package load time
func init() {
	// Register the numeric type handler with the global registry
	GetGlobalThreadSafeRegistry().Register(NewNumericTypeHandler())
}
