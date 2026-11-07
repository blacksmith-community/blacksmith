package operators

import (
	"fmt"

	"github.com/fivetwenty-io/graft/internal/errtext"
	"github.com/fivetwenty-io/graft/internal/literal"
	"github.com/fivetwenty-io/graft/internal/pending"
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

// resolveInjectArg resolves one (( inject ... )) argument without
// stripping a literal from it, the way resolveGrabArgValue does for grab.
// A || fallback tries each side in turn through this same function, and a
// nested call's result is used as it came back, so a map read from
// inside a literal by any of these routes still reaches Run wrapped.
// Every reference gets the environment variable expansion grab gives it.
// viaOr is set for a reference on either side of a ||, which also wraps a
// failed lookup in an "unable to resolve" message.
func resolveInjectArg(ev *Evaluator, arg *Expr, viaOr bool) (interface{}, error) {
	switch arg.Type {
	case LogicalOr:
		val, err := resolveInjectArg(ev, arg.Left, true)
		if err == nil {
			return val, nil
		}
		if pending.StopsFallback(err) {
			// A refused literal, or a grab of an operator that has not run,
			// is never a reason to fall back.
			return nil, err
		}
		return resolveInjectArg(ev, arg.Right, true)
	case Reference:
		// arg.Reference is nil for a bare @name token that parsed as an
		// orphaned target (no cursor to resolve): see
		// ResolveOperatorArgument's doc comment on the same condition.
		if arg.Reference == nil {
			return nil, fmt.Errorf("unable to resolve reference: @%s is a target, not a value", arg.Name)
		}
		arg.Reference.Nodes = ResolveEnv(arg.Reference.Nodes)
		val, err := arg.Reference.ResolveLiteral(ev.Tree)
		if err != nil && viaOr {
			return nil, fmt.Errorf("unable to resolve `%s`: %w", arg.Reference.String(), err)
		}
		return val, err
	case OperatorCall:
		return evaluateNestedOperator(ev, arg)
	default:
		return ResolveOperatorArgument(ev, arg)
	}
}

// Run executes the operator.
func (InjectOperator) Run(ev *Evaluator, args []*Expr) (*Response, error) {
	DEBUG("running (( inject ... )) operation at $.%s", ev.Here)
	defer DEBUG("done with (( inject ... )) operation at $%s\n", ev.Here)

	var vals []map[string]interface{}

	for i, arg := range args {
		if arg.Type == Reference && arg.Reference != nil {
			DEBUG("  arg[%d]: trying to resolve reference $.%s", i, arg.Reference)
		}
		s, err := resolveInjectArg(ev, arg, false)
		if err != nil {
			DEBUG("     [%d]: resolution failed\n    error: %s", i, err)
			return nil, err
		}
		if s == nil && arg.Type != Reference {
			DEBUG("  arg[%d]: resolved to nil", i)
			return nil, fmt.Errorf("inject operator argument cannot be nil")
		}

		// A map that is, or sits inside, a literal injects each of its
		// values as a literal, so the evaluator still never looks inside
		// them once they land here.
		wrapped := false
		if l, isLiteral := s.(literal.Value); isLiteral {
			s, wrapped = l.Value(), true
		}

		m, ok := s.(map[string]interface{})
		if !ok {
			DEBUG("     [%d]: resolved to something that is not a map.  that is unacceptable.", i)
			if arg.Type == Reference {
				return nil, errtext.Colorf("@c{%s} @R{is not a map}", arg.Reference)
			}
			return nil, errtext.Colorf("@R{inject operator argument must resolve to a map}")
		}

		DEBUG("     [%d]: resolved to a map; appending to the list of maps to merge/inject", i)
		// Deep copy the map to avoid modifying the original
		m = DeepCopyMap(m)
		if wrapped {
			for k, v := range m {
				m[k] = literal.Wrap(v)
			}
		}
		vals = append(vals, m)
		DEBUG("")
	}

	switch len(vals) {
	case 0:
		DEBUG("  no arguments supplied to (( inject ... )) operation.  oops.")
		return nil, errtext.Colorf("no arguments specified to @c{(( inject ... ))}")

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
