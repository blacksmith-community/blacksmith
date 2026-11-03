package operators

import (
	"fmt"
	"os"

	"github.com/goccy/go-yaml"

	"github.com/fivetwenty-io/graft/pkg/graft"
)

// Literal value constants for YAML parsing.
const (
	literalTrue  = "true"
	literalFalse = "false"
	literalNull  = "null"
)

// ResolveOperatorArgument resolves an expression argument, including nested operator calls
// This helper allows operators to handle nested expressions transparently.
//
//nolint:gocyclo // expression resolution requires handling all expression types
func ResolveOperatorArgument(ev *Evaluator, arg *Expr) (interface{}, error) {
	if arg == nil {
		return nil, nil
	}

	switch arg.Type {
	case Literal:
		return arg.Literal, nil

	case Reference:
		// A Reference expression can carry a nil cursor: the parser emits
		// one for an orphaned @name token (a target with no operator call
		// attached to it). There is nothing to resolve.
		if arg.Reference == nil {
			return nil, fmt.Errorf("unable to resolve reference: @%s is a target, not a value", arg.Name)
		}
		// Use the existing reference resolution
		DEBUG("ResolveOperatorArgument: resolving reference %s", arg.Reference.String())
		// Expand environment variables in the reference path
		arg.Reference.Nodes = ResolveEnv(arg.Reference.Nodes)
		DEBUG("ResolveOperatorArgument: after env expansion: %s", arg.Reference.String())
		val, err := arg.Reference.Resolve(ev.Tree)
		DEBUG("ResolveOperatorArgument: reference %s resolved to %v (type %T)", arg.Reference.String(), val, val)
		if err != nil {
			// Format the error message to match the expected format
			return nil, fmt.Errorf("unable to resolve `%s`: %w", arg.Reference.String(), err)
		}
		return val, nil

	case EnvVar:
		// Get environment variable value
		val := os.Getenv(arg.Name)
		if val == "" {
			// Return an error so LogicalOr can fall back to the next option
			return nil, fmt.Errorf("environment variable '%s' is not set", arg.Name)
		}
		// Try to unmarshal the value as YAML if it looks like structured data
		if val == literalTrue || val == literalFalse || val == literalNull ||
			(val != "" && (val[0] == '{' || val[0] == '[' || val[0] == '-')) {
			var unmarshalled interface{}
			if err := yaml.Unmarshal([]byte(val), &unmarshalled); err == nil {
				// Only use unmarshalled value if it's not a string
				if _, isString := unmarshalled.(string); !isString {
					if m, isMap := unmarshalled.(map[string]interface{}); isMap {
						unmarshalled = graft.NormalizeMap(m)
					}
					return unmarshalled, nil
				}
			}
		}
		// Return as string for plain strings or if unmarshalling fails
		return val, nil

	case OperatorCall:
		// Evaluate nested operator call
		return evaluateNestedOperator(ev, arg)

	case LogicalOr:
		// Handle || as an or-else operator - if left fails, try right
		// Note: nil is a valid concrete value and should NOT cause fallback
		left, err := ResolveOperatorArgument(ev, arg.Left)
		if err == nil {
			// Left succeeded (even if nil), return its value
			DEBUG("LogicalOr: left side succeeded with value %v (type %T), stopping here", left, left)
			return left, nil
		}
		// Left failed, try right
		DEBUG("LogicalOr: left side failed with error %v, trying right side", err)
		return ResolveOperatorArgument(ev, arg.Right)

	case Negate, Addition, Subtraction, Multiplication, Division, Modulo,
		Equal, NotEqual, LessThan, LessThanOrEqual, GreaterThan, GreaterThanOrEqual, LogicalAnd:
		// Infix nodes appearing as an operator's own argument (including a
		// nested infix operand of another infix node — see graft.EvaluateInfix's
		// doc comment for why this recursion terminates) dispatch through the
		// same shared entry point Expr.Evaluate uses.
		return graft.EvaluateInfix(ev, arg)

	case List, Or, RegexpMatch, BoshVar, VaultGroup, VaultChoice:
		return nil, fmt.Errorf("unsupported expression type: %v", arg.Type)
	}
	return nil, fmt.Errorf("unknown expression type: %v", arg.Type)
}

// evaluateNestedOperator evaluates a nested operator call expression.
func evaluateNestedOperator(ev *Evaluator, expr *Expr) (interface{}, error) {
	if expr.Type != OperatorCall {
		return nil, fmt.Errorf("not an operator call expression")
	}

	opName := expr.Op()

	// Get the operator, preferring ev's engine-local registry so a custom
	// operator registered via Engine.RegisterOperator / WithCustomOperator
	// resolves the same nested (e.g. (( concat "a" (upper "b") ))) as it
	// does at the top level. graft.EngineOf, not graft.GetEngine: this runs
	// once per nested operator call, and GetEngine materializes a whole
	// new default engine (with an unstoppable cache-cleanup goroutine) on
	// every nil-engine Evaluator, which OperatorForEngine's own nil
	// handling makes unnecessary here.
	op := OperatorForEngine(graft.EngineOf(ev), opName)
	if _, ok := op.(graft.NullOperator); ok {
		return nil, fmt.Errorf("unknown operator: %s", opName)
	}

	// Create a temporary opcall for the nested operator. NewOpcallForExpr
	// (not NewOpcall) so the "@target" and ":nocache" modifier parsed onto
	// the expression reach Opcall.Run — a nested (( vault:nocache ... ))
	// must bypass the backend cache exactly like the top-level form.
	opcall := graft.NewOpcallForExpr(op, expr, "")

	// Set the where field to the current evaluator's position
	// This is important for operators like vault that use ev.Here
	if ev.Here != nil {
		opcall.SetWhere(ev.Here.Copy())
	}

	// Run the nested operator
	resp, err := opcall.Run(ev)
	if err != nil {
		return nil, fmt.Errorf("nested operator %s failed: %w", opName, err)
	}

	// Return the value from the response
	if resp.Type == Replace {
		return resp.Value, nil
	}

	return nil, fmt.Errorf("nested operator %s returned unexpected response type: %v", opName, resp.Type)
}

// ResolveAllOperatorArguments resolves all arguments in a slice.
func ResolveAllOperatorArguments(ev *Evaluator, args []*Expr) ([]interface{}, error) {
	results := make([]interface{}, len(args))

	for i, arg := range args {
		val, err := ResolveOperatorArgument(ev, arg)
		if err != nil {
			return nil, fmt.Errorf("argument %d: %w", i, err)
		}
		results[i] = val
	}

	return results, nil
}

// AsString attempts to convert an argument value to string.
func AsString(val interface{}) (string, error) {
	if val == nil {
		return "", nil
	}

	switch v := val.(type) {
	case string:
		return v, nil
	case fmt.Stringer:
		return v.String(), nil
	case map[string]interface{}:
		return "", fmt.Errorf("value is a map, not a string")
	case []interface{}:
		return "", fmt.Errorf("value is a list, not a string")
	case []string:
		return "", fmt.Errorf("value is a list, not a string")
	case int, int32, int64, float32, float64:
		return fmt.Sprintf("%v", v), nil
	default:
		return fmt.Sprintf("%v", v), nil
	}
}

// AsStringArray attempts to convert an argument value to string array.
func AsStringArray(val interface{}) ([]string, error) {
	if val == nil {
		return []string{}, nil
	}

	switch v := val.(type) {
	case []string:
		return v, nil
	case []interface{}:
		result := make([]string, len(v))
		for i, item := range v {
			str, err := AsString(item)
			if err != nil {
				return nil, err
			}
			result[i] = str
		}
		return result, nil
	default:
		// Single value becomes single-element array
		str, err := AsString(val)
		if err != nil {
			return nil, err
		}
		return []string{str}, nil
	}
}
