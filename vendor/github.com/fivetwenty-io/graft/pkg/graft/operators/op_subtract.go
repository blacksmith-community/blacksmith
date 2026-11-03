package operators

// SubtractOperator implements the - operator with type awareness.
type SubtractOperator struct {
	*ArithmeticOperatorBase
}

// NewSubtractOperator creates a new subtract operator.
func NewSubtractOperator() SubtractOperator {
	return SubtractOperator{
		ArithmeticOperatorBase: NewArithmeticOperatorBase("-"),
	}
}

//nolint:gochecknoinits // Operator registration must happen at package load time
func init() {
	RegisterOp("-", NewSubtractOperator())
}
