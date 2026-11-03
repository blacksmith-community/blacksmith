package operators

import (
	"fmt"

	"github.com/fivetwenty-io/graft/internal/utils/ansi"
	"github.com/fivetwenty-io/graft/pkg/graft"
	"github.com/fivetwenty-io/graft/pkg/graft/tree"
)

// ParamOperator runs in ParamPhase for early validation
// Returns error with formatted message using ansi.Errorf for colored output.
type ParamOperator struct{}

// Setup initializes the operator.
func (ParamOperator) Setup() error {
	return nil
}

// Phase returns which phase this operator should run in.
func (ParamOperator) Phase() OperatorPhase {
	return ParamPhase
}

// Dependencies returns what keys the operator depends on.
func (ParamOperator) Dependencies(_ *Evaluator, _ []*Expr, _ []*tree.Cursor, auto []*tree.Cursor) []*tree.Cursor {
	return auto
}

// Run executes the operator.
func (ParamOperator) Run(ev *Evaluator, args []*Expr) (*Response, error) {
	DEBUG("running (( param ... )) operation at $.%s", ev.Here)
	defer DEBUG("done with (( param ... )) operation at $.%s\n", ev.Here)

	if len(args) != 1 {
		return nil, graft.WithCode(ansi.Errorf("@R{param operator only expects} @c{one argument}"), graft.CodeArgumentCount)
	}

	// For param operator, we need to be careful about evaluation
	// since it runs in ParamPhase, nested operators might not be available
	// We'll try to resolve but fall back to string representation if needed

	var paramMessage string

	// First, try to resolve as a nested expression
	val, err := ResolveOperatorArgument(ev, args[0])
	if err == nil && val != nil {
		paramMessage = fmt.Sprintf("%v", val)
		DEBUG("resolved param message to: %s", paramMessage)
	} else {
		// Fall back to direct evaluation
		DEBUG("failed to resolve with ResolveOperatorArgument, trying direct evaluation")
		// Set evaluator for nested operator calls
		args[0].SetEvaluator(ev)
		v, err := args[0].Evaluate(ev.Tree)
		if err != nil {
			DEBUG("direct evaluation also failed")
			return nil, err
		}
		paramMessage = fmt.Sprintf("%v", v)
		DEBUG("param message from direct evaluation: %s", paramMessage)
	}

	// Always return an error with the specified message
	return nil, graft.WithCode(ansi.Errorf("@R{%s}", paramMessage), graft.CodeParamRequired)
}

//nolint:gochecknoinits // Operator registration must happen at package load time
func init() {
	RegisterOp("param", ParamOperator{})
}
