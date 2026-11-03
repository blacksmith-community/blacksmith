package operators

// DivideOperator implements the / operator with type awareness.
type DivideOperator struct {
	*ArithmeticOperatorBase
}

// NewDivideOperator creates a new divide operator.
func NewDivideOperator() DivideOperator {
	return DivideOperator{
		ArithmeticOperatorBase: NewArithmeticOperatorBase("/"),
	}
}

//nolint:gochecknoinits // Operator registration must happen at package load time
func init() {
	RegisterOp("/", NewDivideOperator())
}
