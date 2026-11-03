package operators

import (
	"fmt"

	"github.com/fivetwenty-io/graft/pkg/graft/tree"
)

// FlattenOperator recursively flattens a nested array to a single flat
// array: [[1,2],[3,4],[[5,6],7]] becomes
// [1,2,3,4,5,6,7], however deeply the input nests. It takes exactly one
// argument (interpretation decision T-2); a documented depth argument was
// never implemented.
type FlattenOperator struct{}

// Setup initializes the operator.
func (FlattenOperator) Setup() error {
	return nil
}

// Phase returns which phase this operator should run in.
func (FlattenOperator) Phase() OperatorPhase {
	return EvalPhase
}

// Dependencies returns what the operator depends on.
func (FlattenOperator) Dependencies(_ *Evaluator, _ []*Expr, _ []*tree.Cursor, auto []*tree.Cursor) []*tree.Cursor {
	return auto
}

// Run executes the operator.
func (FlattenOperator) Run(ev *Evaluator, args []*Expr) (*Response, error) {
	DEBUG("running (( flatten ... )) operation at $.%s", ev.Here)
	defer DEBUG("done with (( flatten ... )) operation at $.%s\n", ev.Here)

	if len(args) != 1 {
		return nil, fmt.Errorf("flatten operator requires exactly one argument, got %d", len(args))
	}

	val, err := ResolveOperatorArgument(ev, args[0])
	if err != nil {
		DEBUG("  arg[0]: failed to resolve expression to a concrete value")
		DEBUG("  error was: %s", err)
		return nil, err
	}

	list, ok := val.([]interface{})
	if !ok {
		return nil, fmt.Errorf("flatten operator requires a list argument, got %T", val)
	}

	result := make([]interface{}, 0, len(list))
	result = flattenInto(result, list)

	return &Response{
		Type:  Replace,
		Value: result,
	}, nil
}

// flattenInto appends every non-list element of list to acc, recursively
// descending into any nested []interface{} elements to arbitrary depth. A
// nil element is preserved as an element (not dropped); an empty nested
// list contributes nothing.
func flattenInto(acc []interface{}, list []interface{}) []interface{} {
	for _, elem := range list {
		if nested, ok := elem.([]interface{}); ok {
			acc = flattenInto(acc, nested)
			continue
		}
		acc = append(acc, elem)
	}
	return acc
}

//nolint:gochecknoinits // Operator registration must happen at package load time
func init() {
	RegisterOp("flatten", FlattenOperator{})
}
