package operators

// AddOperator implements the + operator with type awareness.
type AddOperator struct {
	*ArithmeticOperatorBase
}

// NewAddOperator creates a new add operator.
func NewAddOperator() AddOperator {
	return AddOperator{
		ArithmeticOperatorBase: NewArithmeticOperatorBase("+"),
	}
}

//nolint:gochecknoinits // Operator registration must happen at package load time
func init() {
	RegisterOp("+", NewAddOperator())
}
