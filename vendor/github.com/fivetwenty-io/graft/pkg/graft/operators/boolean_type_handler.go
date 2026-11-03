package operators

// Note: isTruthy is defined in operator_utils.go

// BooleanTypeHandler handles operations for boolean types.
type BooleanTypeHandler struct {
	*BaseTypeHandler
	derived DerivedComparisons[*BooleanTypeHandler]
}

// NewBooleanTypeHandler creates a new handler for boolean operations.
func NewBooleanTypeHandler() *BooleanTypeHandler {
	handler := &BooleanTypeHandler{
		BaseTypeHandler: NewBaseTypeHandler(80), // Medium-high priority
	}
	handler.derived = NewDerivedComparisons(handler)

	// Support boolean operations with mixed types for logical operations
	// Equality will use strict checking, but logical operations use truthiness
	handler.AddSupportedTypes(
		TypePair{A: TypeBool, B: TypeBool},
		TypePair{A: TypeBool, B: TypeInt},
		TypePair{A: TypeBool, B: TypeString},
		TypePair{A: TypeBool, B: TypeNull},
	)

	return handler
}

// Add performs logical OR for booleans (non-standard but useful).
func (h *BooleanTypeHandler) Add(a, b interface{}) (interface{}, error) {
	// Boolean OR operation: true + true = true, true + false = true, etc.
	aBool, aOk := toBoolStrict(a)
	bBool, bOk := toBoolStrict(b)

	if aOk && bOk {
		return aBool || bBool, nil
	}

	return nil, NotImplementedError("add", a, b)
}

// Subtract is not supported for booleans.
func (h *BooleanTypeHandler) Subtract(a, b interface{}) (interface{}, error) {
	return nil, NotImplementedError("subtract", a, b)
}

// Multiply performs logical AND for booleans (non-standard but useful).
func (h *BooleanTypeHandler) Multiply(a, b interface{}) (interface{}, error) {
	// Boolean AND operation: true * true = true, true * false = false, etc.
	aBool, aOk := toBoolStrict(a)
	bBool, bOk := toBoolStrict(b)

	if aOk && bOk {
		return aBool && bBool, nil
	}

	return nil, NotImplementedError("multiply", a, b)
}

// Divide is not supported for booleans.
func (h *BooleanTypeHandler) Divide(a, b interface{}) (interface{}, error) {
	return nil, NotImplementedError("divide", a, b)
}

// Modulo is not supported for booleans.
func (h *BooleanTypeHandler) Modulo(a, b interface{}) (interface{}, error) {
	return nil, NotImplementedError("modulo", a, b)
}

// Equal performs boolean equality comparison with strict type checking.
func (h *BooleanTypeHandler) Equal(a, b interface{}) (bool, error) {
	aBool, aOk := toBoolStrict(a)
	bBool, bOk := toBoolStrict(b)

	if aOk && bOk {
		return aBool == bBool, nil
	}

	// For strict type checking, booleans are only equal to other booleans
	// Return false for different types (no truthiness conversion for equality)
	if aOk || bOk {
		return false, nil
	}

	return false, NotImplementedError("equal", a, b)
}

// NotEqual performs boolean inequality comparison.
func (h *BooleanTypeHandler) NotEqual(a, b interface{}) (bool, error) {
	return h.derived.NotEqual(a, b)
}

// Less treats false < true.
func (h *BooleanTypeHandler) Less(a, b interface{}) (bool, error) {
	aBool, aOk := toBoolStrict(a)
	bBool, bOk := toBoolStrict(b)

	if aOk && bOk {
		// false < true, true < false is false
		return !aBool && bBool, nil
	}

	return false, NotImplementedError("less", a, b)
}

// Greater treats true > false.
func (h *BooleanTypeHandler) Greater(a, b interface{}) (bool, error) {
	aBool, aOk := toBoolStrict(a)
	bBool, bOk := toBoolStrict(b)

	if aOk && bOk {
		// true > false, false > true is false
		return aBool && !bBool, nil
	}

	return false, NotImplementedError("greater", a, b)
}

// LessOrEqual performs boolean comparison.
func (h *BooleanTypeHandler) LessOrEqual(a, b interface{}) (bool, error) {
	return h.derived.LessOrEqual(a, b)
}

// GreaterOrEqual performs boolean comparison.
func (h *BooleanTypeHandler) GreaterOrEqual(a, b interface{}) (bool, error) {
	return h.derived.GreaterOrEqual(a, b)
}

// toBoolStrict converts a value to boolean if it is actually a boolean.
func toBoolStrict(val interface{}) (bool, bool) {
	if b, ok := val.(bool); ok {
		return b, true
	}
	return false, false
}

// LogicalOperations provides specialized boolean operations.
type LogicalOperations interface {
	And(a, b interface{}) (bool, error)
	Or(a, b interface{}) (bool, error)
	Not(a interface{}) (bool, error)
	Xor(a, b interface{}) (bool, error)
}

// BooleanLogicalOps implements logical operations for boolean types.
type BooleanLogicalOps struct {
	handler *BooleanTypeHandler
}

// NewBooleanLogicalOps creates a new logical operations handler.
func NewBooleanLogicalOps(handler *BooleanTypeHandler) *BooleanLogicalOps {
	return &BooleanLogicalOps{handler: handler}
}

// And performs logical AND operation using truthiness.
func (ops *BooleanLogicalOps) And(a, b interface{}) (bool, error) {
	return isTruthy(a) && isTruthy(b), nil
}

// Or performs logical OR operation using truthiness.
func (ops *BooleanLogicalOps) Or(a, b interface{}) (bool, error) {
	return isTruthy(a) || isTruthy(b), nil
}

// Not performs logical NOT operation using truthiness.
func (ops *BooleanLogicalOps) Not(a interface{}) (bool, error) {
	return !isTruthy(a), nil
}

// Xor performs logical XOR operation using truthiness.
func (ops *BooleanLogicalOps) Xor(a, b interface{}) (bool, error) {
	aTruthy := isTruthy(a)
	bTruthy := isTruthy(b)
	return aTruthy != bTruthy, nil
}

//nolint:gochecknoinits // Type handler registration must happen at package load time
func init() {
	// Register the boolean type handler with the global registry
	GetGlobalThreadSafeRegistry().Register(NewBooleanTypeHandler())
}
