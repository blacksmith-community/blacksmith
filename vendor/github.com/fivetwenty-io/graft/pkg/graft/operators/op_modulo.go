package operators

// ModuloOperator implements the % operator with type awareness.
type ModuloOperator struct {
	*ArithmeticOperatorBase
}

// NewModuloOperator creates a new modulo operator.
func NewModuloOperator() ModuloOperator {
	return ModuloOperator{
		ArithmeticOperatorBase: NewArithmeticOperatorBase("%"),
	}
}

//nolint:gochecknoinits // Operator registration must happen at package load time
func init() {
	RegisterOp("%", NewModuloOperator())
}
