package operators

import (
	"fmt"
	"reflect"
	"sort"

	"github.com/fivetwenty-io/graft/pkg/graft"
	"github.com/fivetwenty-io/graft/pkg/graft/tree"
)

// SortOperator sorts lists by value or by key (for maps in list).
type SortOperator struct{}

// Setup initializes the operator.
func (SortOperator) Setup() error {
	return nil
}

// Phase returns which phase this operator should run in.
func (SortOperator) Phase() OperatorPhase {
	return MergePhase
}

// Dependencies returns what keys the operator depends on.
func (SortOperator) Dependencies(_ *Evaluator, _ []*Expr, _ []*tree.Cursor, auto []*tree.Cursor) []*tree.Cursor {
	return auto
}

// Run executes the operator.
//
// A well-formed (( sort by X )) marker is consumed at merge time (see
// addToSortListIfNecessary in pkg/graft/merger/merge.go), never reaching
// Run(): it is queued as a path-to-sort and the prior document's list is
// kept in its place. Run() is only ever invoked when a (( sort ... ))
// marker had no prior list to attach to (e.g. it's the first document
// defining that path, or it appears somewhere other than as a scalar
// override of an existing list) — in spruce this is always a hard error,
// never a silent pass-through.
func (SortOperator) Run(ev *Evaluator, _ []*Expr) (*Response, error) {
	DEBUG("running (( sort ... )) operation at $.%s", ev.Here)
	defer DEBUG("done with (( sort ... )) operation at $.%s\n", ev.Here)

	return nil, fmt.Errorf("orphaned (( sort )) operator at $.%s, no list exists at that path", ev.Here)
}

//nolint:gochecknoinits // Operator registration must happen at package load time
func init() {
	RegisterOp("sort", SortOperator{})
}

// AddToSortListIfNecessaryWithEngine is the engine-aware version.
func AddToSortListIfNecessaryWithEngine(operator string, path string, engine graft.Engine) {
	if opcall, err := graft.ParseOpcallForEngine(engine, MergePhase, operator); err == nil {
		var byKey string
		args := opcall.Args()
		if len(args) == 2 {
			byKey = args[1].String()
		}

		DEBUG("adding sort by '%s' of path '%s' to the list of paths to sort", byKey, path)
		if engine != nil {
			engine.GetOperatorState().AddPathToSort(path, byKey)
		}
	}
}

// universalLess compares two values for sorting.
func universalLess(a, b interface{}, key string) bool {
	switch aVal := a.(type) {
	case string:
		if bVal, ok := b.(string); ok {
			return aVal < bVal
		}
	case float64:
		if bVal, ok := b.(float64); ok {
			return aVal < bVal
		}
	case int:
		if bVal, ok := b.(int); ok {
			return aVal < bVal
		}
	case map[string]interface{}:
		if entryB, ok := b.(map[string]interface{}); ok {
			return universalLess(aVal[key], entryB[key], key)
		}
	}

	return false
}

// SortList sorts a list by value or by key for maps.
func SortList(path string, list []interface{}, key string) error {
	return sortList(path, list, key)
}

// sortList sorts a list by value or by key for maps.
func sortList(path string, list []interface{}, key string) error {
	typeCheckMap := map[string]struct{}{}
	for _, entry := range list {
		reflectType := reflect.TypeOf(entry)

		var typeName string
		if reflectType != nil {
			typeName = reflectType.Kind().String()
		} else {
			typeName = "nil"
		}

		if _, ok := typeCheckMap[typeName]; !ok {
			typeCheckMap[typeName] = struct{}{}
		}
	}

	if length := len(typeCheckMap); length > 0 && length != 1 {
		return tree.TypeMismatchError{
			Path:   []string{path},
			Wanted: "a list with homogeneous entry types",
			Got:    "a list with different types",
		}
	}

	for kind := range typeCheckMap {
		switch kind {
		case reflect.Map.String():
			if key == "" {
				key = "name" // default identifier key
			}

			// Check if all maps have the key
			for _, item := range list {
				if m, ok := item.(map[string]interface{}); ok {
					if _, hasKey := m[key]; !hasKey {
						return tree.TypeMismatchError{
							Path:   []string{path},
							Wanted: fmt.Sprintf("a list with map entries each containing %s", key),
							Got:    fmt.Sprintf("a list with map entries, where some do not contain %s", key),
						}
					}
				}
			}

		case reflect.Slice.String():
			return tree.TypeMismatchError{
				Path:   []string{path},
				Wanted: "a list with maps, strings or numbers",
				Got:    "a list with list entries",
			}
		}
	}

	sort.Slice(list, func(i int, j int) bool {
		return universalLess(list[i], list[j], key)
	})

	return nil
}
