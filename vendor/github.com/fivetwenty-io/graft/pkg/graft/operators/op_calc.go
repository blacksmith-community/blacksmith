package operators

import (
	"fmt"
	"math"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	"github.com/casbin/govaluate"

	"github.com/fivetwenty-io/graft/pkg/graft"
	"github.com/fivetwenty-io/graft/pkg/graft/tree"
)

// CalcOperator is invoked with (( calc <expression> )).
type CalcOperator struct{}

// Setup initializes the operator.
func (CalcOperator) Setup() error {
	return nil
}

// Phase returns the evaluation phase.
func (CalcOperator) Phase() graft.OperatorPhase {
	return graft.EvalPhase
}

// Dependencies returns dependencies found in the expression.
func (CalcOperator) Dependencies(ev *graft.Evaluator, args []*graft.Expr, _ []*tree.Cursor, auto []*tree.Cursor) []*tree.Cursor {
	DEBUG("Calculating dependencies for (( calc ... ))")
	deps := []*tree.Cursor{}

	// Check dependencies in all arguments
	for _, arg := range args {
		deps = append(deps, arg.Dependencies(ev, nil)...)

		// Also check for references in literal strings
		if arg.Type == Literal && arg.Literal != nil {
			if str, ok := arg.Literal.(string); ok {
				cursors := searchForCursors(str)
				deps = append(deps, cursors...)
				deps = append(deps, calcBareNameDependencies(ev, str)...)
			}
		}
	}

	return append(auto, deps...)
}

// Run executes the calc operation.
//
//nolint:gocyclo // calc operator handles expression parsing with path substitution
func (CalcOperator) Run(ev *graft.Evaluator, args []*graft.Expr) (*graft.Response, error) {
	DEBUG("running (( calc ... )) operation at $.%s", ev.Here)
	defer DEBUG("done with (( calc ... )) operation at $%s\n", ev.Here)

	// The calc operator expects one argument containing the expression to be evaluated
	if len(args) != 1 {
		return nil, fmt.Errorf("calc operator only expects one argument containing the expression")
	}

	// Resolve the argument using ResolveOperatorArgument to support nested expressions
	val, err := ResolveOperatorArgument(ev, args[0])
	if err != nil {
		return nil, err
	}

	// Convert the resolved value to a string for processing
	var input string
	switch v := val.(type) {
	case string:
		input = v
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		input = fmt.Sprintf("%d", v)
	case float32, float64:
		input = fmt.Sprintf("%f", v)
	default:
		return nil, fmt.Errorf("calc operator argument must resolve to a string or number, got %T", val)
	}

	// Check if the expression starts with an operator (*, /, +, -, ^, %)
	// If so, prepend the current value (not path reference)
	trimmedInput := strings.TrimSpace(input)
	if trimmedInput != "" {
		firstChar := trimmedInput[0]
		if firstChar == '*' || firstChar == '/' || firstChar == '+' || firstChar == '-' || firstChar == '^' || firstChar == '%' {
			// The "existing value" to modify: by the
			// time this operator runs, ev.Here already resolves to the
			// unevaluated operator node itself, since the merge already
			// replaced whatever was at this path with this expression's own
			// source text — there is nothing left at ev.Here to multiply.
			// ev.PriorValues, populated by the merge builder only for this
			// expression shape and only when a value actually existed at
			// this path before the overlay overwrote it, carries the real
			// prior value. Fall back to ev.Here.Resolve (today's existing,
			// pre-A5 behavior) for anything PriorValues has no entry for —
			// a single-file document where nothing was actually overwritten
			// — and finally to 0, the documented default for a path that
			// does not exist.
			currentVal, haveCurrentVal := calcPriorValue(ev)
			var currentNumStr string

			if !haveCurrentVal {
				// Default to 0 if current value doesn't exist
				currentNumStr = "0"
				DEBUG("  current value not found, defaulting to 0")
			} else {
				// Convert current value to string
				switch v := currentVal.(type) {
				case int, int8, int16, int32, int64:
					currentNumStr = fmt.Sprintf("%d", v)
				case uint, uint8, uint16, uint32, uint64:
					currentNumStr = fmt.Sprintf("%d", v)
				case float32, float64:
					currentNumStr = fmt.Sprintf("%f", v)
				default:
					// If current value is not numeric, default to 0
					currentNumStr = "0"
					DEBUG("  current value is not numeric (type %T), defaulting to 0", v)
				}
			}

			// Prepend the current value
			input = currentNumStr + " " + input
			DEBUG("  prepended current value: %s", currentNumStr)
		}
	}

	// Replace all Graft references with the respective value
	DEBUG("  input expression: %s", input)
	processed, replaceError := replaceCalcReferences(ev, input)
	if replaceError != nil {
		return nil, replaceError
	}

	// Once all Graft references (variables) are replaced, try to read the expression
	DEBUG("  processed expression: %s", processed)
	expression, expressionError := govaluate.NewEvaluableExpressionWithFunctions(processed, supportedCalcFunctions())
	if expressionError != nil {
		return nil, expressionError
	}

	// Named variables in the expression: resolve each
	// name still reported by govaluate's own parser relative to the calc
	// call's own parent first — a sibling reference, matching
	// arithmetic.md's documented example where the referenced names are
	// siblings of the calc key — then absolutely from the document root;
	// the first hit wins. A name that resolves nowhere is reported; a name
	// that resolves to a non-numeric value is a distinct, immediate error
	// (resolveCalcVariable).
	varNames := expression.Vars()
	var parameters map[string]interface{}
	var unresolvedVars []string
	if len(varNames) > 0 {
		parameters = make(map[string]interface{}, len(varNames))
		for _, name := range varNames {
			val, found, resolveErr := resolveCalcVariable(ev, name)
			if resolveErr != nil {
				return nil, resolveErr
			}
			if !found {
				unresolvedVars = append(unresolvedVars, name)
				continue
			}
			parameters[name] = val
		}
	}

	if len(unresolvedVars) > 0 {
		return nil, fmt.Errorf("calc operator does not support named variables in expression: %s", strings.Join(unresolvedVars, ", "))
	}

	// An expression with no variables after substitution takes a
	// byte-identical path to before A5 — same govaluate.Evaluate(nil) call,
	// same result normalization below (B-8).
	var result interface{}
	var evaluateError error
	if len(parameters) > 0 {
		result, evaluateError = expression.Evaluate(parameters)
	} else {
		result, evaluateError = expression.Evaluate(nil)
	}
	if evaluateError != nil {
		return nil, evaluateError
	}

	// Convert float results to int if they have no fractional part
	if resultFloat, ok := result.(float64); ok {
		resultInt := int64(resultFloat)
		if float64(resultInt) == resultFloat {
			result = resultInt
		}
	}

	DEBUG("  evaluated result: %v", result)
	return &graft.Response{
		Type:  graft.Replace,
		Value: result,
	}, nil
}

// resolveCalcVariable resolves a bare named variable that survived
// replaceCalcReferences's dotted-path substitution.
// It tries, in order: a sibling of the calc call's own path (ev.Here's
// parent plus name), then an absolute path from the document root. The
// first cursor that resolves wins; if that value is not numeric, resolution
// stops immediately with the same type-mismatch message
// replaceCalcReferences uses for dotted paths, rather than falling through
// to the other candidate. found=false (with a nil error) means neither
// candidate resolved at all — the caller collects these into the existing
// "does not support named variables" error.
func resolveCalcVariable(ev *graft.Evaluator, name string) (value float64, found bool, err error) {
	var candidates []*tree.Cursor

	if ev.Here != nil && ev.Here.Depth() >= 1 {
		sibling := ev.Here.Copy()
		sibling.Pop()
		sibling.Push(name)
		candidates = append(candidates, sibling)
	}

	if cursor, parseErr := tree.ParseCursor(name); parseErr == nil {
		candidates = append(candidates, cursor)
	}

	for _, cursor := range candidates {
		resolved, resolveErr := cursor.Resolve(ev.Tree)
		if resolveErr != nil {
			continue
		}
		// An explicitly null value resolves without error but has no
		// reflect.Type, so it gets replaceCalcReferences's own nil message
		// rather than the type-mismatch one, which would dereference a nil
		// *reflect.rtype.
		if resolved == nil {
			return 0, false, fmt.Errorf("path %s references a nil value, which cannot be used in calculations", cursor.String())
		}
		f, numeric := calcToFloat64(resolved)
		if !numeric {
			return 0, false, fmt.Errorf("path %s is of type %s, which cannot be used in calculations", cursor.String(), reflect.TypeOf(resolved).Kind())
		}
		return f, true, nil
	}

	return 0, false, nil
}

// calcToFloat64 converts a resolved value to float64 for use as a
// govaluate expression parameter, matching the numeric type set
// replaceCalcReferences and supportedCalcFunctions already accept
// elsewhere in this file.
func calcToFloat64(val interface{}) (float64, bool) {
	switch v := val.(type) {
	case int:
		return float64(v), true
	case int8:
		return float64(v), true
	case int16:
		return float64(v), true
	case int32:
		return float64(v), true
	case int64:
		return float64(v), true
	case uint:
		return float64(v), true
	case uint8:
		return float64(v), true
	case uint16:
		return float64(v), true
	case uint32:
		return float64(v), true
	case uint64:
		return float64(v), true
	case float32:
		return float64(v), true
	case float64:
		return v, true
	default:
		return 0, false
	}
}

// calcBareNameDependencies extends Dependencies to cover §5.4's bare named
// variables (e.g. the "a" and "b" in "a + b", as opposed to the dotted
// paths searchForCursors already covers). Both candidate cursors
// resolveCalcVariable will later try — the sibling-of-calc's-own-path
// cursor and the absolute-from-root cursor — are added for each name
// govaluate's own parser reports as a variable in the raw (unsubstituted)
// expression string, so the evaluator orders whichever one actually
// resolves ahead of this calc call. A raw string that also contains dotted
// paths may not parse as a bare govaluate expression at all; that is fine,
// since searchForCursors already covers the dotted-path case on its own.
func calcBareNameDependencies(ev *graft.Evaluator, expr string) []*tree.Cursor {
	parsed, err := govaluate.NewEvaluableExpressionWithFunctions(expr, supportedCalcFunctions())
	if err != nil {
		return nil
	}

	var deps []*tree.Cursor
	for _, name := range parsed.Vars() {
		if ev != nil && ev.Here != nil && ev.Here.Depth() >= 1 {
			sibling := ev.Here.Copy()
			sibling.Pop()
			sibling.Push(name)
			deps = append(deps, sibling)
		}
		if cursor, cerr := tree.ParseCursor(name); cerr == nil {
			deps = append(deps, cursor)
		}
	}
	return deps
}

// calcPriorValue returns the "existing value" for op_calc.go's
// leading-operator value-modification form: the
// merge builder's recorded prior value at ev.Here's canonical path if one
// was recorded, else whatever currently resolves at ev.Here (today's
// pre-A5 fallback, kept for documents where nothing was actually
// overwritten), else ok=false when neither yields anything.
func calcPriorValue(ev *graft.Evaluator) (value interface{}, ok bool) {
	if ev.PriorValues != nil {
		if prior, found := ev.PriorValues[ev.Here.String()]; found {
			return prior, true
		}
	}

	resolved, err := ev.Here.Resolve(ev.Tree)
	if err != nil || resolved == nil {
		return nil, false
	}
	return resolved, true
}

// searchForCursors finds path references in the input string.
func searchForCursors(input string) []*tree.Cursor {
	result := []*tree.Cursor{}

	// Search for sub-strings that contain the path separator dot character
	// https://regex101.com/r/TIEyak/1
	re := regexp.MustCompile(`(\w+|-)\.(\w+|-|\.)+`)
	candidates := re.FindAllString(input, -1)
	DEBUG("    strings found containing the path separator: %v", strings.Join(candidates, ", "))

	// If it is a path, it can be parsed (parse errors will be ignored)
	for _, candidate := range candidates {
		// Skip floats
		if _, err := strconv.ParseFloat(candidate, 64); err == nil {
			continue
		}

		// Skip ints
		if _, err := strconv.ParseInt(candidate, 10, 64); err == nil {
			continue
		}

		if cursor, parseError := tree.ParseCursor(candidate); parseError == nil {
			result = append(result, cursor)
		}
	}

	DEBUG("    result cursors: %v", result)
	return result
}

// replaceCalcReferences replaces path references with their values.
func replaceCalcReferences(ev *graft.Evaluator, input string) (string, error) {
	cursors := searchForCursors(input)

	for _, cursor := range cursors {
		value, resolveError := cursor.Resolve(ev.Tree)
		if resolveError != nil {
			return "", resolveError
		}

		path := cursor.String()
		DEBUG("    path/value: %s=%v", path, value)

		switch value.(type) {
		case int, uint8, uint16, uint32, uint64, int8, int16, int32, int64:
			input = strings.ReplaceAll(input, path, fmt.Sprintf("%d", value))

		case float32, float64:
			input = strings.ReplaceAll(input, path, fmt.Sprintf("%f", value))

		case nil:
			return "", fmt.Errorf("path %s references a nil value, which cannot be used in calculations", path)

		default:
			return "", fmt.Errorf("path %s is of type %s, which cannot be used in calculations", path, reflect.TypeOf(value).Kind())
		}
	}

	return input, nil
}

// supportedCalcFunctions returns the built-in functions available in calc expressions.
//
//nolint:gocyclo // function map initialization with type validation
func supportedCalcFunctions() map[string]govaluate.ExpressionFunction {
	return map[string]govaluate.ExpressionFunction{
		"min": func(args ...interface{}) (interface{}, error) {
			if len(args) == 2 {
				a, aOK := args[0].(float64)
				b, bOK := args[1].(float64)
				if aOK && bOK {
					return math.Min(a, b), nil
				}
			}
			return -1, fmt.Errorf("min function expects two arguments of type float64")
		},

		"max": func(args ...interface{}) (interface{}, error) {
			if len(args) == 2 {
				a, aOK := args[0].(float64)
				b, bOK := args[1].(float64)
				if aOK && bOK {
					return math.Max(a, b), nil
				}
			}
			return -1, fmt.Errorf("max function expects two arguments of type float64")
		},

		"mod": func(args ...interface{}) (interface{}, error) {
			if len(args) == 2 {
				a, aOK := args[0].(float64)
				b, bOK := args[1].(float64)
				if aOK && bOK {
					return math.Mod(a, b), nil
				}
			}
			return -1, fmt.Errorf("mod function expects two arguments of type float64")
		},

		"pow": func(args ...interface{}) (interface{}, error) {
			if len(args) == 2 {
				a, aOK := args[0].(float64)
				b, bOK := args[1].(float64)
				if aOK && bOK {
					return math.Pow(a, b), nil
				}
			}
			return -1, fmt.Errorf("pow function expects two arguments of type float64")
		},

		"sqrt": func(args ...interface{}) (interface{}, error) {
			if len(args) == 1 {
				if a, ok := args[0].(float64); ok {
					return math.Sqrt(a), nil
				}
			}
			return -1, fmt.Errorf("sqrt function expects one argument of type float64")
		},

		"floor": func(args ...interface{}) (interface{}, error) {
			if len(args) == 1 {
				if a, ok := args[0].(float64); ok {
					return math.Floor(a), nil
				}
			}
			return -1, fmt.Errorf("floor function expects one argument of type float64")
		},

		"ceil": func(args ...interface{}) (interface{}, error) {
			if len(args) == 1 {
				if a, ok := args[0].(float64); ok {
					return math.Ceil(a), nil
				}
			}
			return -1, fmt.Errorf("ceil function expects one argument of type float64")
		},
	}
}

//nolint:gochecknoinits // Operator registration must happen at package load time
func init() {
	RegisterOp("calc", CalcOperator{})
}
