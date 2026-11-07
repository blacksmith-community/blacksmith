package operators

import (
	"fmt"
	"sort"
	"strings"

	"github.com/fivetwenty-io/graft/internal/errtext"
	"github.com/fivetwenty-io/graft/internal/literal"
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
			val = literal.Strip(val)
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
		return nil, errtext.Colorf("no arguments specified to @c{(( join ... ))}")
	}

	if len(args) == 1 {
		DEBUG("  too few arguments supplied to (( join ... )) operation.")
		return nil, errtext.Colorf("too few arguments supplied to @c{(( join ... ))}")
	}

	var separator string
	list := GetStringSlice()
	defer PutStringSlice(list)

	for i, arg := range args {
		if i == 0 { // argument #0: separator
			// The separator must be a literal
			if arg.Type != Literal {
				DEBUG("     [%d]: separator is not a literal", i)
				return nil, errtext.Colorf("join operator only accepts literal argument for the separator")
			}

			DEBUG("     [%d]: list separator will be: %v", i, arg.Literal)
			separator = fmt.Sprintf("%v", arg.Literal)
		} else { // argument #1..n: list, literal, or expression
			// Use ResolveOperatorArgument to handle nested expressions
			val, fromEnv, err := resolveNotingEnv(ev, arg)
			if err != nil {
				DEBUG("     [%d]: resolution failed\n    error: %s", i, err)
				// ResolveOperatorArgument already formats reference resolution
				// failures as "unable to resolve `<ref>`: <err>"; do not re-wrap.
				return nil, err
			}

			parts, err := joinParts(i, val, fromEnv)
			if err != nil {
				return nil, err
			}
			*list = append(*list, parts...)
		}
	}

	// finally, join and return
	DEBUG("  joined list: %s", strings.Join(*list, separator))
	return &Response{
		Type:  Replace,
		Value: strings.Join(*list, separator),
	}, nil
}

// joinParts gives the strings that val, the resolved argument at index
// i, adds to the joined list. An environment variable's decoded value is
// printed whole, the way spruce prints it, so a list reads [a b], a map
// map[a:b], and a null <nil>. Otherwise a null adds nothing, a list adds
// each of its entries, and a map adds a key:value pair for each key in
// sorted order.
func joinParts(i int, val interface{}, fromEnv bool) ([]string, error) {
	if fromEnv {
		DEBUG("     [%d]: resolved to an environment value", i)
		return []string{fmt.Sprintf("%v", val)}, nil
	}
	switch v := val.(type) {
	case nil:
		DEBUG("     [%d]: argument resolved to nil, skipping", i)
		return nil, nil

	case []interface{}:
		DEBUG("     [%d]: resolved to a list", i)
		parts := make([]string, 0, len(v))
		for idx, entry := range v {
			switch entry.(type) {
			case []interface{}:
				DEBUG("     [%d]: entry #%d in list is a list (not a literal)", i, idx)
				return nil, errtext.Colorf("entry #%d in list is not compatible for @c{(( join ... ))}", idx)

			case map[string]interface{}:
				DEBUG("     [%d]: entry #%d in list is a map (not a literal)", i, idx)
				return nil, errtext.Colorf("entry #%d in list is not compatible for @c{(( join ... ))}", idx)

			default:
				parts = append(parts, fmt.Sprintf("%v", entry))
			}
		}
		return parts, nil

	case map[string]interface{}:
		DEBUG("     [%d]: resolved to a map", i)
		// Sort keys for consistent output
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		// Join key:value pairs
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("%s:%v", k, v[k]))
		}
		return parts, nil
	}
	DEBUG("     [%d]: resolved to a literal value", i)
	return []string{fmt.Sprintf("%v", val)}, nil
}

//nolint:gochecknoinits // Operator registration must happen at package load time
func init() {
	RegisterOp("join", JoinOperator{})
}
