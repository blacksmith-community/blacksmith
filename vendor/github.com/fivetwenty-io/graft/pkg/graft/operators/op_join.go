package operators

import (
	"fmt"
	"sort"
	"strings"

	"github.com/fivetwenty-io/graft/internal/utils/ansi"
	"github.com/fivetwenty-io/graft/pkg/graft/tree"
)

// JoinOperator handles nested operator calls.
type JoinOperator struct{}

// Setup ...
func (JoinOperator) Setup() error {
	return nil
}

// Phase ...
func (JoinOperator) Phase() OperatorPhase {
	return EvalPhase
}

// Dependencies returns the nodes that (( join ... )) requires to be resolved.
func (JoinOperator) Dependencies(ev *Evaluator, args []*Expr, _ []*tree.Cursor, auto []*tree.Cursor) []*tree.Cursor {
	deps := make([]*tree.Cursor, 0, len(auto))
	deps = append(deps, auto...)

	// Skip the first argument (separator) and process the rest
	for i := 1; i < len(args); i++ {
		arg := args[i]
		if arg == nil {
			continue
		}

		// For reference arguments, we need to check if they point to lists
		// and expand them to individual element dependencies
		if arg.Type == Reference && arg.Reference != nil {
			// Try to resolve to see if it's a list
			val, err := arg.Reference.Resolve(ev.Tree)
			if err == nil && val != nil {
				switch v := val.(type) {
				case []interface{}:
					// It's a list, add dependencies for each element
					for idx := range v {
						elemCursor := arg.Reference.Copy()
						elemCursor.Push(fmt.Sprintf("%d", idx))
						deps = append(deps, elemCursor)
					}
				default:
					// Not a list, just add the reference itself
					deps = append(deps, arg.Reference)
				}
			} else {
				// Couldn't resolve, add the reference as-is
				deps = append(deps, arg.Reference)
			}
		}
	}

	return deps
}

// Run ...
func (JoinOperator) Run(ev *Evaluator, args []*Expr) (*Response, error) {
	DEBUG("running (( join ... )) operation at $.%s", ev.Here)
	defer DEBUG("done with (( join ... )) operation at $%s\n", ev.Here)

	if len(args) == 0 {
		DEBUG("  no arguments supplied to (( join ... )) operation.")
		return nil, ansi.Errorf("no arguments specified to @c{(( join ... ))}")
	}

	if len(args) == 1 {
		DEBUG("  too few arguments supplied to (( join ... )) operation.")
		return nil, ansi.Errorf("too few arguments supplied to @c{(( join ... ))}")
	}

	var separator string
	list := GetStringSlice()
	defer PutStringSlice(list)

	for i, arg := range args {
		if i == 0 { // argument #0: separator
			// The separator must be a literal
			if arg.Type != Literal {
				DEBUG("     [%d]: separator is not a literal", i)
				return nil, ansi.Errorf("join operator only accepts literal argument for the separator")
			}

			DEBUG("     [%d]: list separator will be: %v", i, arg.Literal)
			separator = fmt.Sprintf("%v", arg.Literal)
		} else { // argument #1..n: list, literal, or expression
			// Use ResolveOperatorArgument to handle nested expressions
			val, err := ResolveOperatorArgument(ev, arg)
			if err != nil {
				DEBUG("     [%d]: resolution failed\n    error: %s", i, err)
				// ResolveOperatorArgument already formats reference resolution
				// failures as "unable to resolve `<ref>`: <err>"; do not re-wrap.
				return nil, err
			}

			if val == nil {
				DEBUG("     [%d]: argument resolved to nil, skipping", i)
				continue
			}

			switch v := val.(type) {
			case []interface{}:
				DEBUG("     [%d]: resolved to a list", i)
				for idx, entry := range v {
					switch entry.(type) {
					case []interface{}:
						DEBUG("     [%d]: entry #%d in list is a list (not a literal)", i, idx)
						return nil, ansi.Errorf("entry #%d in list is not compatible for @c{(( join ... ))}", idx)

					case map[string]interface{}:
						DEBUG("     [%d]: entry #%d in list is a map (not a literal)", i, idx)
						return nil, ansi.Errorf("entry #%d in list is not compatible for @c{(( join ... ))}", idx)

					default:
						*list = append(*list, fmt.Sprintf("%v", entry))
					}
				}

			case map[string]interface{}:
				DEBUG("     [%d]: resolved to a map", i)
				// Sort keys for consistent output
				keys := make([]string, 0, len(v))
				for k := range v {
					keys = append(keys, k)
				}
				sort.Strings(keys)

				// Join key:value pairs
				for _, k := range keys {
					pair := fmt.Sprintf("%s:%v", k, v[k])
					*list = append(*list, pair)
				}

			default:
				DEBUG("     [%d]: resolved to a literal value", i)
				*list = append(*list, fmt.Sprintf("%v", v))
			}
		}
	}

	// finally, join and return
	DEBUG("  joined list: %s", strings.Join(*list, separator))
	return &Response{
		Type:  Replace,
		Value: strings.Join(*list, separator),
	}, nil
}

//nolint:gochecknoinits // Operator registration must happen at package load time
func init() {
	RegisterOp("join", JoinOperator{})
}
