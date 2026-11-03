package operators

// MultiplyOperator implements the * operator with type awareness.
type MultiplyOperator struct {
	*ArithmeticOperatorBase
}

// NewMultiplyOperator creates a new multiply operator.
func NewMultiplyOperator() MultiplyOperator {
	return MultiplyOperator{
		ArithmeticOperatorBase: NewArithmeticOperatorBase("*"),
	}
}

//nolint:gochecknoinits // Operator registration must happen at package load time
func init() {
	RegisterOp("*", NewMultiplyOperator())
}
