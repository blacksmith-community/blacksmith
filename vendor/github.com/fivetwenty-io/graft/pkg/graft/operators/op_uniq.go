package operators

import (
	"fmt"
	"reflect"

	"github.com/fivetwenty-io/graft/pkg/graft/tree"
)

// UniqOperator removes duplicate elements from an array, keeping the first
// occurrence of each distinct value and preserving input order — it never
// sorts. Element equality is reflect.DeepEqual, so
// maps and lists dedupe structurally, matching legacyEqual's own use of
// DeepEqual elsewhere in the operator corpus.
type UniqOperator struct{}

// Setup initializes the operator.
func (UniqOperator) Setup() error {
	return nil
}

// Phase returns which phase this operator should run in.
func (UniqOperator) Phase() OperatorPhase {
	return EvalPhase
}

// Dependencies returns what the operator depends on.
func (UniqOperator) Dependencies(_ *Evaluator, _ []*Expr, _ []*tree.Cursor, auto []*tree.Cursor) []*tree.Cursor {
	return auto
}

// Run executes the operator.
func (UniqOperator) Run(ev *Evaluator, args []*Expr) (*Response, error) {
	DEBUG("running (( uniq ... )) operation at $.%s", ev.Here)
	defer DEBUG("done with (( uniq ... )) operation at $.%s\n", ev.Here)

	if len(args) != 1 {
		return nil, fmt.Errorf("uniq operator requires exactly one argument, got %d", len(args))
	}

	val, err := ResolveOperatorArgument(ev, args[0])
	if err != nil {
		DEBUG("  arg[0]: failed to resolve expression to a concrete value")
		DEBUG("  error was: %s", err)
		return nil, err
	}

	list, ok := val.([]interface{})
	if !ok {
		return nil, fmt.Errorf("uniq operator requires a list argument, got %T", val)
	}

	result := make([]interface{}, 0, len(list))
	for _, elem := range list {
		if !containsDeepEqual(result, elem) {
			result = append(result, elem)
		}
	}

	return &Response{
		Type:  Replace,
		Value: result,
	}, nil
}

// containsDeepEqual reports whether elem is reflect.DeepEqual to any
// element already in seen.
func containsDeepEqual(seen []interface{}, elem interface{}) bool {
	for _, s := range seen {
		if reflect.DeepEqual(s, elem) {
			return true
		}
	}
	return false
}

//nolint:gochecknoinits // Operator registration must happen at package load time
func init() {
	RegisterOp("uniq", UniqOperator{})
}
