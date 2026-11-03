package operators

import (
	"fmt"

	"github.com/fivetwenty-io/graft/pkg/graft/tree"
)

// NullOpOperator handles null/nil values
// No args: returns nil
// With arg: returns true if nil, false otherwise.
type NullOpOperator struct{}

// Setup initializes the operator.
func (NullOpOperator) Setup() error {
	return nil
}

// Phase returns which phase this operator should run in.
func (NullOpOperator) Phase() OperatorPhase {
	return EvalPhase
}

// Dependencies returns what keys the operator depends on.
func (NullOpOperator) Dependencies(_ *Evaluator, _ []*Expr, _ []*tree.Cursor, auto []*tree.Cursor) []*tree.Cursor {
	return auto
}

// Run executes the operator.
func (NullOpOperator) Run(ev *Evaluator, args []*Expr) (*Response, error) {
	DEBUG("running (( null ... )) operation at $.%s", ev.Here)
	defer DEBUG("done with (( null ... )) operation at $.%s\n", ev.Here)

	DEBUG("null operator received %d arguments", len(args))
	for i, arg := range args {
		DEBUG("  arg %d: %s (type: %v)", i, arg.String(), arg.Type)
	}

	if len(args) > 1 {
		return nil, fmt.Errorf("null operator takes at most one argument")
	}

	// If no arguments, just return nil
	if len(args) == 0 {
		DEBUG("no arguments, returning nil")
		return &Response{
			Type:  Replace,
			Value: nil,
		}, nil
	}

	// With one argument, check if it's null/nil
	val, err := ResolveOperatorArgument(ev, args[0])
	if err != nil {
		DEBUG("failed to resolve expression: %s", err)
		return nil, err
	}

	isNull := val == nil
	DEBUG("checking if value is null: %v", isNull)

	// If used as a check, return boolean
	return &Response{
		Type:  Replace,
		Value: isNull,
	}, nil
}

//nolint:gochecknoinits // Operator registration must happen at package load time
func init() {
	RegisterOp("null", NullOpOperator{})
}
