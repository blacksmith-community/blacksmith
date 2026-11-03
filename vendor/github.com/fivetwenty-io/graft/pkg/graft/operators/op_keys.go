package operators

import (
	"fmt"
	"sort"

	"github.com/fivetwenty-io/graft/pkg/graft/tree"
)

// KeysOperator extracts keys from maps.
type KeysOperator struct{}

// Setup initializes the operator.
func (KeysOperator) Setup() error {
	return nil
}

// Phase returns which phase this operator should run in.
func (KeysOperator) Phase() OperatorPhase {
	return EvalPhase
}

// Dependencies returns what keys the operator depends on.
func (KeysOperator) Dependencies(_ *Evaluator, _ []*Expr, _ []*tree.Cursor, auto []*tree.Cursor) []*tree.Cursor {
	return auto
}

// Run executes the operator.
func (KeysOperator) Run(ev *Evaluator, args []*Expr) (*Response, error) {
	DEBUG("running (( keys ... )) operation at $.%s", ev.Here)
	defer DEBUG("done with (( keys ... )) operation at $.%s\n", ev.Here)

	if len(args) == 0 {
		return nil, fmt.Errorf("no arguments specified to (( keys ... ))")
	}

	// Collect all keys from all arguments
	keySet := make(map[string]bool)

	for i, arg := range args {
		// Use ResolveOperatorArgument to handle nested expressions
		val, err := ResolveOperatorArgument(ev, arg)
		if err != nil {
			DEBUG("arg[%d]: failed to resolve expression to a concrete value", i)
			DEBUG("error was: %s", err)
			return nil, err
		}

		if val == nil {
			DEBUG("arg[%d]: resolved to nil, skipping", i)
			continue
		}

		// Extract keys based on the type
		switch v := val.(type) {
		case map[string]interface{}:
			DEBUG("arg[%d]: extracting keys from map[string]interface{}", i)
			for key := range v {
				keySet[key] = true
			}

		default:
			DEBUG("arg[%d]: is not a map: %T", i, v)
			// Try to get the original path reference from the argument
			if arg.Type == Reference && arg.Reference != nil {
				return nil, fmt.Errorf("%s is not a map", arg.Reference.String())
			}
			return nil, fmt.Errorf("keys operator only works on maps, argument %d is %T", i, v)
		}
	}

	// Convert to sorted list
	stringKeys := make([]string, 0, len(keySet))
	for key := range keySet {
		stringKeys = append(stringKeys, key)
	}
	sort.Strings(stringKeys)

	// Convert to interface slice
	result := make([]interface{}, len(stringKeys))
	for i, k := range stringKeys {
		result[i] = k
	}

	DEBUG("extracted %d unique keys from %d arguments", len(result), len(args))

	return &Response{
		Type:  Replace,
		Value: result,
	}, nil
}

//nolint:gochecknoinits // Operator registration must happen at package load time
func init() {
	RegisterOp("keys", KeysOperator{})
}
