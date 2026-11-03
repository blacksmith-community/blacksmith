package operators

import (
	"fmt"

	"github.com/fivetwenty-io/graft/internal/utils/ansi"
	"github.com/fivetwenty-io/graft/pkg/graft"
	"github.com/fivetwenty-io/graft/pkg/graft/tree"
)

// InjectOperator deep merges maps into current location.
type InjectOperator struct{}

// Setup initializes the operator.
func (InjectOperator) Setup() error {
	return nil
}

// Phase returns which phase this operator should run in.
func (InjectOperator) Phase() OperatorPhase {
	return MergePhase
}

// Dependencies returns what keys the operator depends on.
func (InjectOperator) Dependencies(ev *Evaluator, args []*Expr, locs []*tree.Cursor, auto []*tree.Cursor) []*tree.Cursor {
	l := []*tree.Cursor{}

	for _, arg := range args {
		switch arg.Type {
		case Reference:
			// arg.Reference is nil for a bare @name token that parsed as an
			// orphaned target rather than an operator's own @target (see
			// operator_helpers.go's ResolveOperatorArgument doc comment on
			// the same condition). It contributes no dependency; Run will
			// report the error when it tries to resolve the argument.
			if arg.Reference == nil {
				continue
			}
			for _, other := range locs {
				canon, err := arg.Reference.Canonical(ev.Tree)
				if err != nil {
					return []*tree.Cursor{}
				}
				if other.Under(canon) {
					l = append(l, other)
				}
			}
		case OperatorCall:
			// Get dependencies from nested operator, preferring ev's
			// engine-local registry (see evaluateNestedOperator).
			// graft.EngineOf, not graft.GetEngine: see operator_helpers.go.
			nestedOp := OperatorForEngine(graft.EngineOf(ev), arg.Op())
			if _, ok := nestedOp.(graft.NullOperator); !ok {
				nestedDeps := nestedOp.Dependencies(ev, arg.Args(), locs, auto)
				l = append(l, nestedDeps...)
			}
		case Literal, List, Or, Negate, Addition, Subtraction, Multiplication, Division, Modulo,
			Equal, NotEqual, LessThan, LessThanOrEqual, GreaterThan, GreaterThanOrEqual,
			LogicalAnd, LogicalOr, RegexpMatch, EnvVar, BoshVar, VaultGroup, VaultChoice:
			// No dependencies for these types
		}
	}

	l = append(l, auto...)

	return l
}

// Run executes the operator.
func (InjectOperator) Run(ev *Evaluator, args []*Expr) (*Response, error) {
	DEBUG("running (( inject ... )) operation at $.%s", ev.Here)
	defer DEBUG("done with (( inject ... )) operation at $%s\n", ev.Here)

	var vals []map[string]interface{}

	for i, arg := range args {
		// Special handling for references vs expressions
		if arg.Type == Reference {
			// arg.Reference is nil for a bare @name token that parsed as an
			// orphaned target (no cursor to resolve): see
			// ResolveOperatorArgument's doc comment on the same condition.
			if arg.Reference == nil {
				return nil, fmt.Errorf("unable to resolve reference: @%s is a target, not a value", arg.Name)
			}
			// Direct reference - resolve it directly
			DEBUG("  arg[%d]: trying to resolve reference $.%s", i, arg.Reference)
			s, err := arg.Reference.Resolve(ev.Tree)
			if err != nil {
				DEBUG("     [%d]: resolution failed\n    error: %s", i, err)
				return nil, err
			}

			m, ok := s.(map[string]interface{})
			if !ok {
				DEBUG("     [%d]: resolved to something that is not a map.  that is unacceptable.", i)
				return nil, ansi.Errorf("@c{%s} @R{is not a map}", arg.Reference)
			}

			DEBUG("     [%d]: resolved to a map; appending to the list of maps to merge/inject", i)
			// Deep copy the map to avoid modifying the original
			vals = append(vals, DeepCopyMap(m))
		} else {
			// Use ResolveOperatorArgument for all other expressions (including nested operators)
			val, err := ResolveOperatorArgument(ev, arg)
			if err != nil {
				DEBUG("  arg[%d]: failed to resolve expression to a concrete value", i)
				DEBUG("     [%d]: error was: %s", i, err)
				return nil, err
			}

			if val == nil {
				DEBUG("  arg[%d]: resolved to nil", i)
				return nil, fmt.Errorf("inject operator argument cannot be nil")
			}

			// Check if the resolved value is a map
			m, ok := val.(map[string]interface{})
			if !ok {
				DEBUG("     [%d]: resolved to something that is not a map", i)
				return nil, ansi.Errorf("@R{inject operator argument must resolve to a map}")
			}

			DEBUG("     [%d]: resolved to a map; appending to the list of maps to merge/inject", i)
			// Deep copy the map to avoid modifying the original
			vals = append(vals, DeepCopyMap(m))
		}
		DEBUG("")
	}

	switch len(vals) {
	case 0:
		DEBUG("  no arguments supplied to (( inject ... )) operation.  oops.")
		return nil, ansi.Errorf("no arguments specified to @c{(( inject ... ))}")

	default:
		DEBUG("  merging found maps into a single map to be injected")
		// Merge all maps together
		merged := make(map[string]interface{})
		for _, val := range vals {
			err := Merge(merged, val)
			if err != nil {
				DEBUG("  failed: %s\n", err)
				return nil, err
			}
		}
		return &Response{
			Type:  Inject,
			Value: merged,
		}, nil
	}
}

//nolint:gochecknoinits // Operator registration must happen at package load time
func init() {
	RegisterOp("inject", InjectOperator{})
}
