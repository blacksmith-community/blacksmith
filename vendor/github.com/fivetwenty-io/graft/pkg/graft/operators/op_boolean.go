package operators

import (
	"fmt"

	"github.com/fivetwenty-io/graft/log"
	"github.com/fivetwenty-io/graft/pkg/graft"
	"github.com/fivetwenty-io/graft/pkg/graft/tree"
)

// OrElseOperator implements or-else behavior (||) - returns first non-nil value
// This is the fallback/coalesce operator, not true boolean OR.
type OrElseOperator struct{}

// Setup initializes the operator.
func (OrElseOperator) Setup() error {
	return nil
}

// Phase returns the operator phase.
func (OrElseOperator) Phase() graft.OperatorPhase {
	return graft.EvalPhase
}

// Dependencies returns operator dependencies.
func (OrElseOperator) Dependencies(ev *graft.Evaluator, args []*graft.Expr, locs []*tree.Cursor, auto []*tree.Cursor) []*tree.Cursor {
	deps := make([]*tree.Cursor, 0, len(auto))
	deps = append(deps, auto...)

	// Always collect all dependencies for cycle detection
	// The optimization logic (skipping dependencies after literals) should be
	// handled at a higher level, not here.
	for _, arg := range args {
		if arg != nil {
			deps = append(deps, arg.Dependencies(ev, locs)...)
		}
	}

	return deps
}

// Run executes the or-else operator.
func (OrElseOperator) Run(ev *graft.Evaluator, args []*graft.Expr) (*graft.Response, error) {
	log.DEBUG("OrElseOperator.Run called")
	log.DEBUG("running (( || ... )) operation at $.%s", ev.Here)
	defer log.DEBUG("done with (( || ... )) operation at $.%s\n", ev.Here)

	if len(args) != 2 {
		return nil, fmt.Errorf("|| operator requires exactly 2 arguments, got %d", len(args))
	}

	// Evaluate left first, but allow for missing values
	leftResp, err := graft.EvaluateExpr(args[0], ev)
	if err != nil {
		// If left evaluation fails (e.g., missing key), try right
		log.DEBUG("  left evaluation failed: %v, trying right", err)
		return graft.EvaluateExpr(args[1], ev)
	}

	// If left evaluation succeeded but the value is nil, try right
	if leftResp.Value == nil {
		log.DEBUG("  left is nil, trying right")
		return graft.EvaluateExpr(args[1], ev)
	}

	// Left evaluation succeeded and value is not nil, return it
	log.DEBUG("  left = %v, returning", leftResp.Value)
	return leftResp, nil
}

//nolint:gochecknoinits // Operator registration must happen at package load time
func init() {
	// Register boolean operators
	// Use type-aware boolean operators for && and !
	RegisterOp("&&", NewTypeAwareAndOperator())
	RegisterOp("||", &OrElseOperator{}) // Use or-else operator, not boolean OR
	RegisterOp("!", NewTypeAwareNotOperator())
}
