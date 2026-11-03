package operators

import (
	"fmt"

	"github.com/fivetwenty-io/graft/log"
	"github.com/fivetwenty-io/graft/pkg/graft"
	"github.com/fivetwenty-io/graft/pkg/graft/tree"
)

// NegateOperator handles the negate operator for explicit negation
// This is an alternative to the ! operator, useful when ! might be ambiguous.
type NegateOperator struct{}

// Setup initializes the operator.
func (NegateOperator) Setup() error {
	return nil
}

// Phase returns the operator phase.
func (NegateOperator) Phase() graft.OperatorPhase {
	return graft.EvalPhase
}

// Dependencies returns operator dependencies.
func (NegateOperator) Dependencies(_ *graft.Evaluator, _ []*graft.Expr, _ []*tree.Cursor, auto []*tree.Cursor) []*tree.Cursor {
	return auto
}

// Run executes the negate operator
// It uses the same truthiness rules as the ! operator:
// - nil/null -> true (negation of falsy)
// - false -> true
// - 0, 0.0 -> true
// - "" -> true
// - empty list/map -> true
// - Everything else -> false.
func (NegateOperator) Run(ev *graft.Evaluator, args []*graft.Expr) (*graft.Response, error) {
	log.DEBUG("running (( negate ... )) operation at $.%s", ev.Here)
	defer log.DEBUG("done with (( negate ... )) operation at $.%s\n", ev.Here)

	if len(args) != 1 {
		return nil, fmt.Errorf("negate operator requires exactly one argument")
	}

	// Use ResolveOperatorArgument to handle nested expressions
	val, err := ResolveOperatorArgument(ev, args[0])
	if err != nil {
		log.DEBUG("failed to resolve expression to a concrete value")
		log.DEBUG("error was: %s", err)
		return nil, err
	}

	// Use the shared IsTruthy function for consistent truthiness evaluation
	truthy := IsTruthy(val)
	log.DEBUG("negating value %v (type: %T, truthy: %v)", val, val, truthy)

	return &graft.Response{
		Type:  graft.Replace,
		Value: !truthy,
	}, nil
}

//nolint:gochecknoinits // Operator registration must happen at package load time
func init() {
	RegisterOp("negate", NegateOperator{})
}
