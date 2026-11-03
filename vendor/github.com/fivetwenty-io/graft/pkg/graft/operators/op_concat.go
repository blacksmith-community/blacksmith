package operators

import (
	"fmt"
	"strings"

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

// Dependencies ...
func (ConcatOperator) Dependencies(ev *Evaluator, args []*Expr, locs []*tree.Cursor, auto []*tree.Cursor) []*tree.Cursor {
	// Include dependencies from nested operator calls
	deps := auto

	for _, arg := range args {
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
		v, err := ResolveOperatorArgument(ev, arg)
		if err != nil {
			DEBUG("  arg[%d]: failed to resolve expression to a concrete value", i)
			DEBUG("     [%d]: error was: %s", i, err)
			return nil, err
		}

		// A reference that resolves to a map or a list is not a string
		// scalar and cannot be concatenated, matching spruce's behavior.
		if arg.Type == Reference {
			switch v.(type) {
			case map[string]interface{}, []interface{}:
				DEBUG("  arg[%d]: %v is not a string scalar", i, v)
				return nil, ansi.Errorf("@R{tried to concat} @c{%s}@R{, which is not a string scalar}", arg.Reference)
			}
		}

		// Convert to string. References already rejected non-scalar values
		// above; this branch only handles scalars and non-reference list
		// results (e.g. from a nested operator call).
		var stringVal string
		switch val := v.(type) {
		case string:
			stringVal = val
		case []interface{}:
			stringSlice := make([]string, len(val))
			for j, elem := range val {
				stringSlice[j] = fmt.Sprintf("%v", elem)
			}
			stringVal = strings.Join(stringSlice, "")
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

//nolint:gochecknoinits // Operator registration must happen at package load time
func init() {
	RegisterOp("concat", ConcatOperator{})
}
