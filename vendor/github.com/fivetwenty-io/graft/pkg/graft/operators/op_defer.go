package operators

import (
	"fmt"
	"strings"

	"github.com/fivetwenty-io/graft/pkg/graft/tree"
)

// DeferOperator handles nested operator calls.
type DeferOperator struct{}

// Setup ...
func (DeferOperator) Setup() error {
	return nil
}

// Phase ...
func (DeferOperator) Phase() OperatorPhase {
	return EvalPhase
}

// Dependencies ...
func (DeferOperator) Dependencies(_ *Evaluator, _ []*Expr, _ []*tree.Cursor, auto []*tree.Cursor) []*tree.Cursor {
	// Defer operator should not track dependencies since it's generating a string
	// representation of an expression, not evaluating it
	return []*tree.Cursor{}
}

// Run ...
func (DeferOperator) Run(ev *Evaluator, args []*Expr) (*Response, error) {
	DEBUG("running (( defer ... )) operation at $.%s", ev.Here)
	defer DEBUG("done with (( defer ... )) operation at $.%s\n", ev.Here)

	if len(args) == 0 {
		return nil, fmt.Errorf("defer has no arguments - what are you deferring?")
	}

	// Debug: log the arguments we received
	DEBUG("defer received %d arguments:", len(args))
	for i, arg := range args {
		DEBUG("  arg[%d]: type=%v, value=%v", i, arg.Type, arg)
	}

	// The defer operator's purpose is to preserve the expression for later evaluation
	// Build the deferred expression from all arguments

	components := []string{"(("} // Join these with spaces at the end

	// Special handling for when defer receives multiple arguments but the second is a LogicalOr
	// This happens when parseArgs splits "grab this || that" into ["grab", LogicalOr(this, that)]
	if len(args) == 2 && args[1].Type == LogicalOr {
		// Reconstruct as a single expression
		components = append(components, reconstructExpr(args[0]))
		components = append(components, reconstructExpr(args[1]))
	} else {
		for _, arg := range args {
			components = append(components, reconstructExpr(arg))
		}
	}
	components = append(components, "))")

	deferred := strings.Join(components, " ")
	DEBUG("deferring expression: %s", deferred)

	return &Response{
		Type:  Replace,
		Value: deferred,
	}, nil
}

// reconstructExpr reconstructs a string representation of an expression.
func reconstructExpr(e *Expr) string {
	if e == nil {
		return ""
	}

	switch e.Type {
	case Literal:
		if e.Literal == nil {
			return "nil"
		}
		if s, ok := e.Literal.(string); ok {
			return fmt.Sprintf("%q", s)
		}
		return fmt.Sprintf("%v", e.Literal)

	case Reference:
		if e.Reference != nil {
			return e.Reference.String()
		}
		return ""

	case EnvVar:
		return fmt.Sprintf("$%s", e.Name)

	case LogicalOr:
		left := reconstructExpr(e.Left)
		right := reconstructExpr(e.Right)
		return fmt.Sprintf("%s || %s", left, right)

	case OperatorCall:
		op := e.Op()
		args := e.Args()
		if op == "" {
			return e.String() // fallback
		}
		argStrs := make([]string, len(args))
		for i, arg := range args {
			argStrs[i] = reconstructExpr(arg)
		}
		if len(argStrs) == 0 {
			return op
		}
		return fmt.Sprintf("%s %s", op, strings.Join(argStrs, " "))

	case List, Or, Negate, Addition, Subtraction, Multiplication, Division, Modulo,
		Equal, NotEqual, LessThan, LessThanOrEqual, GreaterThan, GreaterThanOrEqual,
		LogicalAnd, RegexpMatch, BoshVar, VaultGroup, VaultChoice:
		return e.String()
	}
	return e.String()
}

//nolint:gochecknoinits // Operator registration must happen at package load time
func init() {
	RegisterOp("defer", DeferOperator{})
}
