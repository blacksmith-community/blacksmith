package operators

import (
	"fmt"

	"github.com/fivetwenty-io/graft/pkg/graft"
	"github.com/fivetwenty-io/graft/pkg/graft/tree"
)

// StringifyOperator handles nested operator calls.
type StringifyOperator struct{}

// Setup ...
func (StringifyOperator) Setup() error {
	return nil
}

// Phase ...
func (StringifyOperator) Phase() OperatorPhase {
	return EvalPhase
}

// Dependencies returns what keys the operator depends on. stringify
// captures and YAML-marshals its whole target subtree, so any other
// opcall whose location is under that target must resolve first — the
// same problem InjectOperator.Dependencies (op_inject.go) already solves
// for its own reference argument, by walking locs (every other opcall's
// location in the document) for entries under the reference's own
// canonical path. Without this, the dependency graph has no edge forcing
// e.g. a nested "(( grab ... ))" under the target to run before
// stringify, and stringify can capture (and permanently serialize) that
// opcall's still-unevaluated marker text instead of its resolved value.
func (StringifyOperator) Dependencies(ev *Evaluator, args []*Expr, locs []*tree.Cursor, auto []*tree.Cursor) []*tree.Cursor {
	deps := make([]*tree.Cursor, 0, len(auto))
	deps = append(deps, auto...)

	for _, arg := range args {
		if arg == nil || arg.Type != Reference || arg.Reference == nil {
			continue
		}
		canon, err := arg.Reference.Canonical(ev.Tree)
		if err != nil {
			continue
		}
		for _, other := range locs {
			if other.Under(canon) {
				deps = append(deps, other)
			}
		}
	}

	return deps
}

// Run ...
func (StringifyOperator) Run(ev *Evaluator, args []*Expr) (*Response, error) {
	DEBUG("running (( stringify ... )) operation at $.%s", ev.Here)
	defer DEBUG("done with (( stringify ... )) operation at $.%s\n", ev.Here)

	if len(args) != 1 {
		return nil, fmt.Errorf("stringify operator requires exactly one argument")
	}

	// Use ResolveOperatorArgument to handle nested expressions
	val, err := ResolveOperatorArgument(ev, args[0])
	if err != nil {
		DEBUG("failed to resolve expression to a concrete value")
		DEBUG("error was: %s", err)
		return nil, err
	}

	// Handle nil specially
	if val == nil {
		DEBUG("resolved to nil, returning nil")
		return &Response{
			Type:  Replace,
			Value: nil,
		}, nil
	}

	// For scalars, convert directly to string
	switch v := val.(type) {
	case string:
		DEBUG("already a string: %s", v)
		return &Response{
			Type:  Replace,
			Value: v,
		}, nil

	case int, int64, float64, bool:
		DEBUG("converting scalar to string: %v", v)
		return &Response{
			Type:  Replace,
			Value: fmt.Sprintf("%v", v),
		}, nil
	}

	// For complex types, use the shared YAML marshal so stringified
	// subtrees carry the same spruce-compatible key ordering as every
	// other YAML emit.
	DEBUG("converting complex type to YAML string")
	out, err := graft.MarshalYAML(val)
	if err != nil {
		DEBUG("YAML marshaling failed: %s", err)
		return nil, fmt.Errorf("unable to stringify value: %w", err)
	}

	return &Response{
		Type:  Replace,
		Value: string(out),
	}, nil
}

//nolint:gochecknoinits // Operator registration must happen at package load time
func init() {
	RegisterOp("stringify", StringifyOperator{})
}
