package operators

import (
	"fmt"
	"strings"

	"github.com/fivetwenty-io/graft/internal/errtext"
	"github.com/fivetwenty-io/graft/internal/utils/ansi"
	graft "github.com/fivetwenty-io/graft/pkg/graft"
	"github.com/fivetwenty-io/graft/pkg/graft/tree"
)

// ConcatOperator handles nested operator calls.
type ConcatOperator struct{}

// Setup ...
func (ConcatOperator) Setup() error {
	return nil
}

// Phase ...
func (ConcatOperator) Phase() OperatorPhase {
	return EvalPhase
}

// Dependencies returns the automatic dependencies, those of each nested
// operator call, and, for a reference on the left of a ||, every
// operator under its path. Run refuses a map or a list wherever it
// comes from, so the last of these only turns a read of concat's own
// ancestor into the data-flow cycle error.
func (ConcatOperator) Dependencies(ev *Evaluator, args []*Expr, locs []*tree.Cursor, auto []*tree.Cursor) []*tree.Cursor {
	// Include dependencies from nested operator calls
	deps := auto

	for _, arg := range args {
		if arg.Type == LogicalOr {
			deps = append(deps, readDeps(ev, arg, locs, false)...)
		}
		if arg.Type == OperatorCall {
			// Get dependencies from nested operator, preferring ev's
			// engine-local registry (see evaluateNestedOperator).
			// graft.EngineOf, not graft.GetEngine: see operator_helpers.go.
			nestedOp := OperatorForEngine(graft.EngineOf(ev), arg.Op())
			if _, ok := nestedOp.(graft.NullOperator); !ok {
				nestedDeps := nestedOp.Dependencies(ev, arg.Args(), locs, auto)
				deps = append(deps, nestedDeps...)
			}
		}
	}

	return deps
}

// Run ...
func (ConcatOperator) Run(ev *Evaluator, args []*Expr) (*Response, error) {
	DEBUG("running (( concat ... )) operation at $.%s", ev.Here)
	defer DEBUG("done with (( concat ... )) operation at $%s\n", ev.Here)

	l := GetStringSlice()
	defer PutStringSlice(l)

	if len(args) < 2 {
		return nil, fmt.Errorf("concat operator requires at least two arguments")
	}

	for i, arg := range args {
		// Use the helper to resolve arguments, including nested operators
		v, leaf, err := resolveNotingLeaf(ev, arg)
		if err != nil {
			DEBUG("  arg[%d]: failed to resolve expression to a concrete value", i)
			DEBUG("     [%d]: error was: %s", i, err)
			return nil, err
		}

		// A map or a list is not a string scalar and cannot be
		// concatenated, matching spruce's behavior, whether it comes from
		// a reference, from a || chain, or from a nested call. Only an
		// environment variable's decoded value is printed whole, the way
		// spruce prints it, so a list reads [a b] and a map map[a:b].
		var stringVal string
		switch val := v.(type) {
		case string:
			stringVal = val
		case map[string]interface{}, []interface{}:
			if leaf == nil || leaf.Type != EnvVar {
				DEBUG("  arg[%d]: %T is not a string scalar", i, v)
				return nil, notScalarError(leaf)
			}
			stringVal = fmt.Sprintf("%v", val)
		default:
			stringVal = fmt.Sprintf("%v", v)
		}

		DEBUG("  arg[%d]: using '%s'", i, stringVal)
		*l = append(*l, stringVal)
	}

	DEBUG("  result: %s", ansi.Sprintf("@c{%s}", strings.Join(*l, "")))
	return &Response{
		Type:  Replace,
		Value: strings.Join(*l, ""),
	}, nil
}

// notScalarError is the error for a map or a list that leaf gave
// concat. It names the reference or the nested operator, never the
// value.
func notScalarError(leaf *Expr) error {
	if leaf == nil {
		leaf = &Expr{Type: Literal}
	}
	switch leaf.Type {
	case Reference:
		return errtext.Colorf("@R{tried to concat} @c{%s}@R{, which is not a string scalar}", leaf.Reference)
	case OperatorCall:
		return errtext.Colorf("@R{tried to concat the result of} @c{(( %s ))}@R{, which is not a string scalar}", leaf.Op())
	default:
		return errtext.Colorf("@R{tried to concat a map or a list, which is not a string scalar}")
	}
}

//nolint:gochecknoinits // Operator registration must happen at package load time
func init() {
	RegisterOp("concat", ConcatOperator{})
}
