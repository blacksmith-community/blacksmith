package operators

import (
	"fmt"
	"strings"

	"github.com/fivetwenty-io/graft/pkg/graft/tree"
)

// EmptyOperator has dual mode: type construction OR emptiness check
// (( empty(map) )) -> {}
// (( empty(list) )) -> []
// (( empty($var) )) -> true/false.
type EmptyOperator struct{}

// Setup initializes the operator.
func (EmptyOperator) Setup() error {
	return nil
}

// Phase returns which phase this operator should run in.
func (EmptyOperator) Phase() OperatorPhase {
	return EvalPhase
}

// Dependencies returns what keys the operator depends on.
func (EmptyOperator) Dependencies(_ *Evaluator, _ []*Expr, _ []*tree.Cursor, auto []*tree.Cursor) []*tree.Cursor {
	return auto
}

// Run executes the operator.
//
//nolint:gocyclo // empty operator handles multiple type detection patterns
func (EmptyOperator) Run(ev *Evaluator, args []*Expr) (*Response, error) {
	DEBUG("running (( empty ... )) operation at $.%s", ev.Here)
	defer DEBUG("done with (( empty ... )) operation at $.%s\n", ev.Here)

	if len(args) != 1 {
		return nil, fmt.Errorf("empty operator expects 1 argument, received %d", len(args))
	}

	// Use ResolveOperatorArgument to handle nested expressions
	val, err := ResolveOperatorArgument(ev, args[0])
	if err != nil {
		// If it's a reference that couldn't be found, check if the reference name is a type
		if args[0].Type == Reference && args[0].Reference != nil {
			refName := args[0].Reference.String()
			DEBUG("reference '%s' couldn't be resolved, checking if it's a type name", refName)

			// Extract just the last part of the reference (e.g., "hash" from "$.hash")
			parts := strings.Split(refName, ".")
			typeName := parts[len(parts)-1]

			switch strings.ToLower(typeName) {
			case "hash", "map":
				DEBUG("treating unresolved reference 'hash' as type name")
				return &Response{
					Type:  Replace,
					Value: map[string]interface{}{},
				}, nil
			case "array", "list":
				DEBUG("treating unresolved reference 'array' as type name")
				return &Response{
					Type:  Replace,
					Value: []interface{}{},
				}, nil
			case "string":
				DEBUG("treating unresolved reference 'string' as type name")
				return &Response{
					Type:  Replace,
					Value: "",
				}, nil
			}
		}

		DEBUG("failed to resolve expression to a concrete value")
		DEBUG("error was: %s", err)
		return nil, err
	}

	// If the argument resolved to a string, it might be a type name
	if typeStr, ok := val.(string); ok {
		DEBUG("argument resolved to string: %s", typeStr)

		// Check if it's a type name
		switch strings.ToLower(typeStr) {
		case "hash", "map":
			DEBUG("returning empty hash")
			return &Response{
				Type:  Replace,
				Value: map[string]interface{}{},
			}, nil

		case "array", "list":
			DEBUG("returning empty array")
			return &Response{
				Type:  Replace,
				Value: []interface{}{},
			}, nil

		case "string":
			DEBUG("returning empty string")
			return &Response{
				Type:  Replace,
				Value: "",
			}, nil

		default:
			// If it's not a recognized type, treat it as a request to check if the value is empty
			DEBUG("'%s' is not a recognized type, checking if it's empty", typeStr)
			return &Response{
				Type:  Replace,
				Value: typeStr == "",
			}, nil
		}
	}

	// If the argument resolved to something else, check if it's empty
	DEBUG("checking if resolved value is empty")
	isEmpty := false

	switch v := val.(type) {
	case nil:
		isEmpty = true
	case string:
		isEmpty = v == ""
	case []interface{}:
		isEmpty = len(v) == 0
	case map[string]interface{}:
		isEmpty = len(v) == 0
	case int, int64:
		isEmpty = v == 0 || v == int64(0)
	case float64:
		isEmpty = v == 0.0
	case bool:
		isEmpty = !v
	}

	DEBUG("value is empty: %v", isEmpty)
	return &Response{
		Type:  Replace,
		Value: isEmpty,
	}, nil
}

//nolint:gochecknoinits // Operator registration must happen at package load time
func init() {
	RegisterOp("empty", EmptyOperator{})
}
