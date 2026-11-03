package operators

import (
	"fmt"
	"strings"

	"github.com/fivetwenty-io/graft/log"
	"github.com/fivetwenty-io/graft/pkg/graft"
	"github.com/fivetwenty-io/graft/pkg/graft/tree"
)

// ArithmeticOperatorBase provides common functionality for arithmetic operators (+, -, *, /, %).
type ArithmeticOperatorBase struct {
	op       string
	registry *TypeRegistry
}

// NewArithmeticOperatorBase creates a new arithmetic operator base.
func NewArithmeticOperatorBase(op string) *ArithmeticOperatorBase {
	return &ArithmeticOperatorBase{
		op:       op,
		registry: GetGlobalTypeRegistry(),
	}
}

// Setup initializes the operator.
func (a *ArithmeticOperatorBase) Setup() error {
	return nil
}

// Phase returns the evaluation phase.
func (a *ArithmeticOperatorBase) Phase() graft.OperatorPhase {
	return graft.EvalPhase
}

// Dependencies returns the operator dependencies.
func (a *ArithmeticOperatorBase) Dependencies(_ *graft.Evaluator, _ []*graft.Expr, _ []*tree.Cursor, auto []*tree.Cursor) []*tree.Cursor {
	return auto
}

// Run executes the arithmetic operation using type handlers.
func (a *ArithmeticOperatorBase) Run(ev *graft.Evaluator, args []*graft.Expr) (*graft.Response, error) {
	log.DEBUG("ArithmeticOperatorBase.Run called for operator %s", a.op)
	log.DEBUG("running (( %s ... )) operation at $.%s", a.op, ev.Here)
	defer log.DEBUG("done with (( %s ... )) operation at $.%s\n", a.op, ev.Here)

	if len(args) != 2 {
		return nil, fmt.Errorf("%s operator requires exactly two arguments", a.op)
	}

	// Resolve arguments
	left, err := ResolveOperatorArgument(ev, args[0])
	if err != nil {
		return nil, err
	}

	right, err := ResolveOperatorArgument(ev, args[1])
	if err != nil {
		return nil, err
	}

	log.DEBUG("  [0] = %v (type: %T)", left, left)
	log.DEBUG("  [1] = %v (type: %T)", right, right)

	// Get operand types
	leftType := GetOperandType(left)
	rightType := GetOperandType(right)

	// Find appropriate handler
	handler := a.registry.FindHandler(leftType, rightType)
	if handler == nil {
		// Fall back to the old performArithmetic function for backward compatibility
		log.DEBUG("No type handler found for %s and %s, falling back to legacy arithmetic", leftType, rightType)
		result, arithErr := performArithmetic(left, right, a.op)
		if arithErr != nil {
			return nil, arithErr
		}
		return &graft.Response{
			Type:  graft.Replace,
			Value: result,
		}, nil
	}

	// Execute operation based on operator type
	var result interface{}
	switch a.op {
	case "+":
		result, err = handler.Add(left, right)
	case "-":
		result, err = handler.Subtract(left, right)
	case "*":
		result, err = handler.Multiply(left, right)
	case "/":
		result, err = handler.Divide(left, right)
	case "%":
		result, err = handler.Modulo(left, right)
	default:
		return nil, fmt.Errorf("unknown arithmetic operator: %s", a.op)
	}

	if err != nil {
		// Check if this is a "not supported" error, if so fall back to legacy arithmetic
		errStr := err.Error()
		if strings.Contains(errStr, "not supported") || strings.Contains(errStr, "not implemented") {
			log.DEBUG("Type handler returned 'not supported' for %s, falling back to legacy arithmetic", a.op)
			result, err = performArithmetic(left, right, a.op)
			if err != nil {
				return nil, err
			}
			return &graft.Response{
				Type:  graft.Replace,
				Value: result,
			}, nil
		}
		return nil, err
	}

	log.DEBUG("ArithmeticOperatorBase.Run returning result: %v (type %T)", result, result)
	return &graft.Response{
		Type:  graft.Replace,
		Value: result,
	}, nil
}

// TypeAwareAddOperator implements the + operator with type awareness.
type TypeAwareAddOperator struct {
	*ArithmeticOperatorBase
}

// NewTypeAwareAddOperator creates a new type-aware add operator.
func NewTypeAwareAddOperator() *TypeAwareAddOperator {
	return &TypeAwareAddOperator{
		ArithmeticOperatorBase: NewArithmeticOperatorBase("+"),
	}
}

// TypeAwareSubtractOperator implements the - operator with type awareness.
type TypeAwareSubtractOperator struct {
	*ArithmeticOperatorBase
}

// NewTypeAwareSubtractOperator creates a new type-aware subtract operator.
func NewTypeAwareSubtractOperator() *TypeAwareSubtractOperator {
	return &TypeAwareSubtractOperator{
		ArithmeticOperatorBase: NewArithmeticOperatorBase("-"),
	}
}

// TypeAwareMultiplyOperator implements the * operator with type awareness.
type TypeAwareMultiplyOperator struct {
	*ArithmeticOperatorBase
}

// NewTypeAwareMultiplyOperator creates a new type-aware multiply operator.
func NewTypeAwareMultiplyOperator() *TypeAwareMultiplyOperator {
	return &TypeAwareMultiplyOperator{
		ArithmeticOperatorBase: NewArithmeticOperatorBase("*"),
	}
}

// TypeAwareDivideOperator implements the / operator with type awareness.
type TypeAwareDivideOperator struct {
	*ArithmeticOperatorBase
}

// NewTypeAwareDivideOperator creates a new type-aware divide operator.
func NewTypeAwareDivideOperator() *TypeAwareDivideOperator {
	return &TypeAwareDivideOperator{
		ArithmeticOperatorBase: NewArithmeticOperatorBase("/"),
	}
}

// TypeAwareModuloOperator implements the % operator with type awareness.
type TypeAwareModuloOperator struct {
	*ArithmeticOperatorBase
}

// NewTypeAwareModuloOperator creates a new type-aware modulo operator.
func NewTypeAwareModuloOperator() *TypeAwareModuloOperator {
	return &TypeAwareModuloOperator{
		ArithmeticOperatorBase: NewArithmeticOperatorBase("%"),
	}
}
