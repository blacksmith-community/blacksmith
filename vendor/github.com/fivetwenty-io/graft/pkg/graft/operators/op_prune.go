package operators

import (
	"github.com/fivetwenty-io/graft/pkg/graft"
	"github.com/fivetwenty-io/graft/pkg/graft/tree"
)

// addToPruneListIfNecessary adds paths to the prune list.
func addToPruneListIfNecessary(engine graft.Engine, paths ...string) {
	prunePaths := engine.GetOperatorState().GetKeysToPrune()
	for _, path := range paths {
		if !isIncluded(prunePaths, path) {
			DEBUG("adding '%s' to the list of paths to prune", path)
			engine.GetOperatorState().AddKeyToPrune(path)
		}
	}
}

// isIncluded checks if a name is in the list.
func isIncluded(list []string, name string) bool {
	for _, entry := range list {
		if entry == name {
			return true
		}
	}

	return false
}

// PruneOperator marks the current path for deletion
// Uses engine state: AddKeyToPrune()
// Returns current value (allows references before deletion)
// Actual deletion happens in post-processing.
type PruneOperator struct{}

// Setup initializes the operator.
func (PruneOperator) Setup() error {
	return nil
}

// Phase returns which phase this operator should run in.
func (PruneOperator) Phase() OperatorPhase {
	return EvalPhase
}

// Dependencies returns what keys the operator depends on.
func (PruneOperator) Dependencies(_ *Evaluator, _ []*Expr, _ []*tree.Cursor, auto []*tree.Cursor) []*tree.Cursor {
	return auto
}

// Run executes the operator.
func (PruneOperator) Run(ev *Evaluator, args []*Expr) (*Response, error) {
	DEBUG("running (( prune ... )) operation at $.%s", ev.Here)
	defer DEBUG("done with (( prune ... )) operation at $.%s\n", ev.Here)

	engine := graft.GetEngine(ev)
	// Quoted keeps a dotted node, such as a Genesis network name, whole
	// when the prune step splits this path again
	addToPruneListIfNecessary(engine, ev.Here.Quoted())

	// Get the current value at this location
	val, err := ev.Here.Resolve(ev.Tree)
	if err != nil {
		DEBUG("  failed to resolve current value: %s", err)
		// If we can't resolve the current value, just return a nil replacement
		return &Response{
			Type:  Replace,
			Value: nil,
		}, nil
	}

	DEBUG("  current value at %s: %T(%v)", ev.Here, val, val)

	// Return the current value unchanged - just mark it for pruning at the end
	// This allows other operators to still reference the value
	return &Response{
		Type:  Replace,
		Value: val,
	}, nil
}

//nolint:gochecknoinits // Operator registration must happen at package load time
func init() {
	RegisterOp("prune", PruneOperator{})
}
