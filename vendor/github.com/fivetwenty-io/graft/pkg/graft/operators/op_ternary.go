package operators

import (
	"fmt"

	"github.com/fivetwenty-io/graft/log"
	"github.com/fivetwenty-io/graft/pkg/graft"
	"github.com/fivetwenty-io/graft/pkg/graft/tree"
)

// TernaryOperator implements the ternary conditional operator (? :).
type TernaryOperator struct{}

// Setup initializes the operator.
func (TernaryOperator) Setup() error {
	return nil
}

// Phase returns the operator phase.
func (TernaryOperator) Phase() graft.OperatorPhase {
	return graft.EvalPhase
}

// Dependencies returns operator dependencies.
func (TernaryOperator) Dependencies(_ *graft.Evaluator, args []*graft.Expr, _ []*tree.Cursor, auto []*tree.Cursor) []*tree.Cursor {
	deps := make([]*tree.Cursor, 0)
	// Only need dependencies from the condition and the branch that will be taken
	// But we don't know which branch yet, so include all
	for _, arg := range args {
		if arg.Type == graft.Reference && arg.Reference != nil {
			deps = append(deps, arg.Reference)
		}
	}
	return append(auto, deps...)
}

// Run executes the ternary operator with short-circuit evaluation.
func (TernaryOperator) Run(ev *graft.Evaluator, args []*graft.Expr) (*graft.Response, error) {
	log.DEBUG("TernaryOperator.Run called")
	log.DEBUG("running (( ?: ... )) operation at $.%s", ev.Here)
	defer log.DEBUG("done with (( ?: ... )) operation at $.%s\n", ev.Here)

	if len(args) != 3 {
		return nil, fmt.Errorf("?: operator requires exactly 3 arguments (condition, true_value, false_value), got %d", len(args))
	}

	// Evaluate the condition
	condResp, err := graft.EvaluateExpr(args[0], ev)
	if err != nil {
		return nil, fmt.Errorf("failed to evaluate ternary condition: %w", err)
	}

	// Short-circuit evaluation: only evaluate the branch we need
	if isTruthy(condResp.Value) {
		// Condition is truthy, evaluate and return true branch
		log.DEBUG("  condition is truthy, evaluating true branch")
		return graft.EvaluateExpr(args[1], ev)
	}
	// Condition is falsy, evaluate and return false branch
	log.DEBUG("  condition is falsy, evaluating false branch")
	return graft.EvaluateExpr(args[2], ev)
}

// TypeAwareTernaryOperator implements the ternary conditional operator (? :) with type awareness.
type TypeAwareTernaryOperator struct{}

// Setup initializes the operator.
func (TypeAwareTernaryOperator) Setup() error {
	return nil
}

// Phase returns the operator phase.
func (TypeAwareTernaryOperator) Phase() graft.OperatorPhase {
	return graft.EvalPhase
}

// Dependencies returns operator dependencies.
func (TypeAwareTernaryOperator) Dependencies(_ *graft.Evaluator, _ []*graft.Expr, _ []*tree.Cursor, auto []*tree.Cursor) []*tree.Cursor {
	// We can't know which branch will be taken until evaluation time,
	// so we need to include dependencies from all branches
	return auto
}

// Run executes the ternary operator with type-aware truthiness evaluation.
func (TypeAwareTernaryOperator) Run(ev *graft.Evaluator, args []*graft.Expr) (*graft.Response, error) {
	log.DEBUG("TypeAwareTernaryOperator.Run called")
	log.DEBUG("running (( ?: ... )) operation at $.%s", ev.Here)
	defer log.DEBUG("done with (( ?: ... )) operation at $.%s\n", ev.Here)

	if len(args) != 3 {
		return nil, fmt.Errorf("?: operator requires exactly 3 arguments (condition, true_value, false_value), got %d", len(args))
	}

	// Evaluate the condition
	condition, err := ResolveOperatorArgument(ev, args[0])
	if err != nil {
		return nil, fmt.Errorf("failed to evaluate ternary condition: %w", err)
	}

	log.DEBUG("  condition = %v (type: %T)", condition, condition)

	// Use type-aware truthiness evaluation
	conditionTruthy := IsTruthy(condition)
	log.DEBUG("  condition is truthy: %v", conditionTruthy)

	// Short-circuit evaluation: only evaluate the branch we need
	if conditionTruthy {
		// Condition is truthy, evaluate and return true branch
		log.DEBUG("  evaluating true branch")
		trueResult, trueErr := ResolveOperatorArgument(ev, args[1])
		if trueErr != nil {
			return nil, fmt.Errorf("failed to evaluate true branch: %w", trueErr)
		}
		return &graft.Response{
			Type:  graft.Replace,
			Value: trueResult,
		}, nil
	}
	// Condition is falsy, evaluate and return false branch
	log.DEBUG("  evaluating false branch")
	falseResult, falseErr := ResolveOperatorArgument(ev, args[2])
	if falseErr != nil {
		return nil, fmt.Errorf("failed to evaluate false branch: %w", falseErr)
	}
	return &graft.Response{
		Type:  graft.Replace,
		Value: falseResult,
	}, nil
}

//nolint:gochecknoinits // Operator registration must happen at package load time
func init() {
	// Register ternary operator
	// Use type-aware ternary operator
	RegisterOp("?:", TypeAwareTernaryOperator{})
}
