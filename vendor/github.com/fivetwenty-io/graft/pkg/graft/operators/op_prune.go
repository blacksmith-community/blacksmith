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
// Gives nil to any reader before the deletion
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

	if ev.Here == nil {
		// With no path there is no key to prune or value to keep.
		return &Response{Type: Replace, Value: nil}, nil
	}

	engine := graft.GetEngine(ev)
	// Quoted keeps a dotted node, such as a Genesis network name, whole
	// when the prune step splits this path again
	addToPruneListIfNecessary(engine, ev.Here.Quoted())

	// The value at this path is the call itself, so an operator that
	// reads the key before the prune step removes it gets nil, as in
	// spruce, and never the call's text.
	return &Response{Type: Replace, Value: nil}, nil
}

//nolint:gochecknoinits // Operator registration must happen at package load time
func init() {
	RegisterOp("prune", PruneOperator{})
}
