package operators

import (
	"errors"
	"fmt"
	"os"

	"github.com/fivetwenty-io/graft/internal/literal"
	"github.com/fivetwenty-io/graft/internal/pending"
	"github.com/fivetwenty-io/graft/internal/yamlcodec"
	"github.com/fivetwenty-io/graft/pkg/graft"
)

// ResolveOperatorArgument resolves an expression argument, including nested operator calls
// This helper allows operators to handle nested expressions transparently.
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
		val, err := arg.Reference.ResolveLiteral(ev.Tree)
		DEBUG("ResolveOperatorArgument: reference %s resolved to %v (type %T)", arg.Reference.String(), val, val)
		if err != nil {
			// Format the error message to match the expected format
			return nil, fmt.Errorf("unable to resolve `%s`: %w", arg.Reference.String(), err)
		}
		// An operator works on plain data, so a value read from inside a
		// literal comes back without the wrapper, and the read is noted.
		return stripNotingRead(ev, val), nil

	case EnvVar:
		// Get environment variable value
		val := os.Getenv(arg.Name)
		if val == "" {
			// Return an error so LogicalOr can fall back to the next option
			return nil, fmt.Errorf("environment variable '%s' is not set", arg.Name)
		}
		// The value decodes the way spruce decodes it. See
		// yamlcodec.DecodeEnvValue.
		return yamlcodec.DecodeEnvValue(val), nil

	case OperatorCall:
		// Evaluate nested operator call. Its result reaches this operator
		// as plain data, like a reference does.
		val, err := evaluateNestedOperator(ev, arg)
		if err != nil {
			return nil, err
		}
		return stripNotingRead(ev, val), nil

	case LogicalOr:
		// Handle || as an or-else operator - if left fails, try right
		// Note: nil is a valid concrete value and should NOT cause fallback
		left, err := ResolveOperatorArgument(ev, arg.Left)
		if err == nil {
			// Left succeeded (even if nil), return its value
			DEBUG("LogicalOr: left side succeeded with value %v (type %T), stopping here", left, left)
			return left, nil
		}
		if pending.StopsFallback(err) {
			// A refused literal, or a grab of an operator that has not
			// run, is never a reason to fall back.
			return nil, err
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

// resolveNotingEnv resolves arg like ResolveOperatorArgument and also
// reports whether the value came from an environment variable, either as
// the argument itself or as the leaf of a || chain that gave the value.
// spruce prints such a value whole, the way Go's %v prints it, where it
// would walk a list or map reached through a reference, so join and
// concat need to know which one they hold.
func resolveNotingEnv(ev *Evaluator, arg *Expr) (interface{}, bool, error) {
	val, leaf, err := resolveNotingLeaf(ev, arg)
	return val, leaf != nil && leaf.Type == EnvVar, err
}

// resolveNotingLeaf resolves arg like ResolveOperatorArgument and also
// returns the expression that gave the value: arg itself, or the leaf
// of a || chain in arg that the value came from.
func resolveNotingLeaf(ev *Evaluator, arg *Expr) (interface{}, *Expr, error) {
	if arg != nil && arg.Type == LogicalOr {
		left, leaf, err := resolveNotingLeaf(ev, arg.Left)
		if err == nil {
			return left, leaf, nil
		}
		if pending.StopsFallback(err) {
			return nil, nil, err
		}
		return resolveNotingLeaf(ev, arg.Right)
	}
	val, err := ResolveOperatorArgument(ev, arg)
	if err != nil {
		return nil, nil, err
	}
	return val, arg, nil
}

// stripNotingRead returns val without any literal wrapper in it, and
// notes on ev that the operator read a literal when val was one, sat
// inside one, or held one.
func stripNotingRead(ev *Evaluator, val interface{}) interface{} {
	plain, read := literal.StripReport(val)
	if read {
		literal.NoteRead(ev)
	}
	return plain
}

// readsLiteral reports whether ev has noted a read of a literal since
// Reads returned before.
func readsLiteral(ev *Evaluator, before int) bool {
	return literal.Reads(ev) > before
}

// resolveRefusingLiteral resolves arg like ResolveOperatorArgument, for an
// operator that reads the value as code, as a path, or as the name of
// something outside to read. When resolving it read a literal anywhere,
// even when it then failed, the call is refused with an error that names
// operator and role and quotes nothing from the value.
func resolveRefusingLiteral(ev *Evaluator, arg *Expr, operator, role string) (interface{}, error) {
	before := literal.Reads(ev)
	val, err := ResolveOperatorArgument(ev, arg)
	if literal.IsRefusal(err) {
		return nil, err
	}
	if readsLiteral(ev, before) {
		return nil, literal.Refuse(operator, role)
	}
	return val, err
}

// evaluateNestedOperator evaluates a nested operator call expression. The
// result is what the nested operator returned, so it may be or hold a
// literal; ResolveOperatorArgument strips it, and (( grab )) and
// (( inject )) keep it.
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
		return nil, pending.UnknownOperator(opName)
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
		// opcall.Run names ev.Here in a path error, and the call this
		// one is nested in names the same path in its own, so only the
		// cause is kept here and the path appears once.
		var perr *graft.PathError
		if ev.Here != nil && errors.As(err, &perr) && perr.Path == ev.Here.String() {
			err = perr.Cause
		}
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
