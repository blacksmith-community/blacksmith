package operators

import (
	"fmt"
	"maps"
	"reflect"
	"slices"
	"sort"
	"strings"

	"github.com/fivetwenty-io/graft/internal/errtext"
	"github.com/fivetwenty-io/graft/internal/literal"
	"github.com/fivetwenty-io/graft/internal/prunekeys"
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

// Dependencies returns the nodes that (( join ... )) requires to be
// resolved. A reference argument also waits for every operator under
// the path it names, the way stringify does, and so does a reference on
// either side of a ||, so join never reads an operator's text and a
// read of its own ancestor is a cycle.
func (JoinOperator) Dependencies(ev *Evaluator, args []*Expr, locs []*tree.Cursor, auto []*tree.Cursor) []*tree.Cursor {
	deps := make([]*tree.Cursor, 0, len(auto))
	deps = append(deps, auto...)

	// Skip the first argument (separator) and process the rest
	for i := 1; i < len(args); i++ {
		arg := args[i]
		if arg == nil {
			continue
		}

		if arg.Type == LogicalOr {
			deps = append(deps, readDeps(ev, arg, locs, true)...)
			continue
		}

		// For reference arguments, we need to check if they point to lists
		// and expand them to individual element dependencies
		if arg.Type == Reference && arg.Reference != nil {
			deps = append(deps, locsUnder(ev, arg.Reference, locs)...)

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

// SoftDependencies returns the operators above a key that will be
// pruned, so join finds the key in the map that holds it and leaves it
// out. See prunedKeyHolders.
func (JoinOperator) SoftDependencies(ev *Evaluator, _ []*Expr, locs []*tree.Cursor) []*tree.Cursor {
	return prunedKeyHolders(ev, locs)
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
			val, leaf, err := resolveNotingLeaf(ev, arg)
			if err != nil {
				DEBUG("     [%d]: resolution failed\n    error: %s", i, err)
				// ResolveOperatorArgument already formats reference resolution
				// failures as "unable to resolve `<ref>`: <err>"; do not re-wrap.
				return nil, err
			}

			fromEnv := leaf != nil && leaf.Type == EnvVar
			if _, ok := val.(map[string]interface{}); ok && !fromEnv {
				val = withoutPrunedKeys(ev, val)
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

// withoutPrunedKeys returns val without the map keys that will be
// pruned, at any depth, since they will not be in the output. A pruned
// entry of a list is gone from the list before join or stringify reads
// it, and a pruned key of a map is left out the same way. A grab hands
// back the map it reads, not a copy, so a key is matched by the map that
// holds it in the tree, whether the operator reached that map through a
// reference or a nested call. val is returned as it is when no key in
// it will be pruned, and without a look at the paths to prune when it
// holds no map, so a reader of a list pays nothing for them.
func withoutPrunedKeys(ev *Evaluator, val interface{}) interface{} {
	if !holdsMap(val) {
		return val
	}
	pruned := prunedKeysByMap(ev)
	if len(pruned) == 0 {
		return val
	}
	out, _ := dropPrunedKeys(val, pruned, map[uintptr]bool{})
	return out
}

// holdsMap reports whether val is a map or a list that holds one at
// any depth.
func holdsMap(val interface{}) bool {
	switch v := val.(type) {
	case map[string]interface{}:
		return true
	case []interface{}:
		for _, e := range v {
			if holdsMap(e) {
				return true
			}
		}
	}
	return false
}

// prunedKeysByMap returns, for each map in the tree that holds a key
// that will be pruned, the set of those keys, keyed by the map's
// address. A key will be pruned when an operator has queued it, or when
// the caller prunes it after evaluation, as graft merge --prune does.
func prunedKeysByMap(ev *Evaluator) map[uintptr]map[string]bool {
	if ev == nil || ev.Tree == nil {
		return nil
	}
	var out map[uintptr]map[string]bool
	for _, path := range prunekeys.For(ev) {
		c, err := tree.ParseCursor(path)
		if err != nil || len(c.Nodes) == 0 {
			continue
		}
		key := c.Pop()
		pm, ok := prunedKeyParent(ev, c)
		if !ok {
			continue
		}
		if _, ok := pm[key]; !ok {
			continue
		}
		if out == nil {
			out = map[uintptr]map[string]bool{}
		}
		p := reflect.ValueOf(pm).Pointer()
		if out[p] == nil {
			out[p] = map[string]bool{}
		}
		out[p][key] = true
	}
	return out
}

// prunedKeyParent returns the map at the path c, which holds a key that
// will be pruned. A grab on the way that has not run yet is read
// through, as resolvePastGrabs says.
func prunedKeyParent(ev *Evaluator, c *tree.Cursor) (map[string]interface{}, bool) {
	parent, err := c.Resolve(ev.Tree)
	if pm, ok := parent.(map[string]interface{}); err == nil && ok {
		return pm, pm != nil
	}
	parent, found := resolvePastGrabs(ev, c.Nodes)
	if found != pastFound {
		return nil, false
	}
	pm, ok := parent.(map[string]interface{})
	return pm, ok && pm != nil
}

// dropPrunedKeys returns val with every key in pruned left out of the
// maps it holds, at any depth, and reports whether anything changed. A
// map or list that changes is copied, and the tree is left as it is.
// seen holds the maps on the current path, so a map that holds itself
// is not walked again.
func dropPrunedKeys(val interface{}, pruned map[uintptr]map[string]bool, seen map[uintptr]bool) (interface{}, bool) {
	switch v := val.(type) {
	case map[string]interface{}:
		if v == nil {
			return val, false
		}
		p := reflect.ValueOf(v).Pointer()
		if seen[p] {
			return val, false
		}
		seen[p] = true
		defer delete(seen, p)
		var out map[string]interface{}
		for k, e := range v {
			if pruned[p][k] {
				if out == nil {
					out = maps.Clone(v)
				}
				delete(out, k)
				continue
			}
			if ne, changed := dropPrunedKeys(e, pruned, seen); changed {
				if out == nil {
					out = maps.Clone(v)
				}
				out[k] = ne
			}
		}
		if out == nil {
			return val, false
		}
		return out, true
	case []interface{}:
		var out []interface{}
		for i, e := range v {
			if ne, changed := dropPrunedKeys(e, pruned, seen); changed {
				if out == nil {
					out = slices.Clone(v)
				}
				out[i] = ne
			}
		}
		if out == nil {
			return val, false
		}
		return out, true
	}
	return val, false
}

//nolint:gochecknoinits // Operator registration must happen at package load time
func init() {
	RegisterOp("join", JoinOperator{})
}
