package operators

import (
	"fmt"

	"github.com/fivetwenty-io/graft/internal/utils/ansi"
	"github.com/fivetwenty-io/graft/pkg/graft"
	"github.com/fivetwenty-io/graft/pkg/graft/tree"
)

// GrabOperator handles nested operator calls.
type GrabOperator struct{}

// Setup ...
func (GrabOperator) Setup() error {
	return nil
}

// Phase ...
func (GrabOperator) Phase() OperatorPhase {
	return EvalPhase
}

// Dependencies ...
func (GrabOperator) Dependencies(ev *Evaluator, args []*Expr, locs []*tree.Cursor, auto []*tree.Cursor) []*tree.Cursor {
	deps := make([]*tree.Cursor, 0, len(auto))
	deps = append(deps, auto...)

	for _, arg := range args {
		if arg != nil {
			argDeps := arg.Dependencies(ev, locs)
			deps = append(deps, argDeps...)
		}
	}

	return deps
}

// resolveGrabArgValue resolves and post-processes one (( grab ... ))
// argument: a bare reference is resolved directly, a nested-call's
// computed string result is re-interpreted as a grab path and resolved
// against the tree, and a literal/env-var/other value is used as-is.
//
// A LogicalOr argument (a `||` fallback nested inside grab's own argument,
// e.g. `grab (concat "a." "b") || fallback`) recurses into this same
// dispatch for whichever side is being tried, instead of resolving the raw
// sub-expression once and using it as-is: that is what applies the
// reference/computed-path/literal dispatch below to whichever side
// actually produced the value, and what lets a computed path that fails
// to resolve — not just a sub-expression that errors outright — fall
// through to the right side.
func resolveGrabArgValue(ev *Evaluator, arg *Expr) (interface{}, error) {
	if arg == nil {
		return nil, nil
	}

	if arg.Type == LogicalOr {
		if val, err := resolveGrabArgValue(ev, arg.Left); err == nil {
			return val, nil
		}
		return resolveGrabArgValue(ev, arg.Right)
	}

	// Substitute any bracket-notation dynamic key segments (e.g.
	// "key[lookup]") before resolving the reference itself, so both the
	// resolution below and ResolveOperatorArgument see the substituted
	// path.
	if arg.Type == Reference {
		if err := resolveGrabDynamicBrackets(arg, ev); err != nil {
			return nil, err
		}
	}

	// Use ResolveOperatorArgument to handle nested expressions.
	val, err := ResolveOperatorArgument(ev, arg)
	if err != nil {
		return nil, err
	}

	if arg.Type == Reference {
		// Direct reference, resolve it normally.
		resolved, err := arg.Reference.Resolve(ev.Tree)
		if err != nil {
			return nil, fmt.Errorf("unable to resolve `%s`: %w", arg.Reference, err)
		}
		return resolved, nil
	}

	if pathStr, ok := val.(string); ok && !valueIsFinal(arg) {
		return resolveComputedPath(ev, pathStr)
	}

	// For literals and other non-string values, use them directly.
	return val, nil
}

// valueIsFinal reports whether arg's resolved value is grab's answer
// already, rather than a computed path string to resolve one more time.
// Literals and env vars are self-evidently final. A nested (( grab ... ))
// call's result is grab's own final, fully-resolved answer, not a
// computed path fragment like a nested concat's is - the documented
// `grab a.b || grab c.d` fallback pattern needs the right side's grab
// result used as-is, not re-grabbed. A nested (( prune )) call is exempt
// for a different reason: prune's own Run (op_prune.go) marks ev.Here for
// deletion as a side effect the moment it runs, and the key is dropped in
// post-processing regardless of what value ends up written here - but
// mid-evaluation that return value is ev.Here's own not-yet-replaced
// content, i.e. this marker's own raw source text, which is not a path.
// Using it as-is is what keeps `grab X || (( prune ))` working.
func valueIsFinal(arg *Expr) bool {
	if arg.Type == Literal || arg.Type == EnvVar {
		return true
	}
	return arg.Type == OperatorCall && (arg.Op() == "grab" || arg.Op() == "prune")
}

// forwardVaultPlaceholder forwards a skip-vault placeholder along a grab
// alias edge: when a single-argument grab's resolved value is exactly
// the skip-vault sentinel and its argument is a direct reference to a
// tree path a placeholder was recorded for (performVaultLookup, or an
// earlier grab in a chain), the same placeholder is recorded for grab's
// own tree path. That lets a later (( vault ... )) call that composes
// its path through this alias render the symbolic "<path/to/secret:key>"
// form (vaultArgProcessor.symbolicPlaceholder) instead of the literal
// "REDACTED" text. The grab result itself is untouched - REDACT=1 merge
// output stays byte-identical.
func forwardVaultPlaceholder(ev *Evaluator, arg *Expr, val interface{}) {
	str, ok := val.(string)
	if !ok || str != redactedValue {
		return
	}
	if arg == nil || arg.Type != Reference || arg.Reference == nil {
		return
	}
	state := graft.GetEngine(ev).GetOperatorState()
	if key, ok := state.VaultPlaceholderFor(arg.Reference.String()); ok {
		state.RecordVaultPlaceholder(ev.Here.String(), key)
	}
}

// resolveComputedPath resolves a string an expression produced as a
// reference path. A string that does not parse as a path is not an error:
// it is a legitimate grab result, returned as-is.
func resolveComputedPath(ev *Evaluator, pathStr string) (interface{}, error) {
	cursor, err := tree.ParseCursor(pathStr)
	if err != nil {
		return pathStr, nil //nolint:nilerr // a non-path string is a legitimate grab result
	}

	resolved, err := cursor.Resolve(ev.Tree)
	if err != nil {
		return nil, fmt.Errorf("unable to resolve `%s`: %w", pathStr, err)
	}
	return resolved, nil
}

// Run ...
func (GrabOperator) Run(ev *Evaluator, args []*Expr) (*Response, error) {
	DEBUG("running (( grab ... )) operation at $.%s", ev.Here)
	defer DEBUG("done with (( grab ... )) operation at $%s\n", ev.Here)

	var vals []interface{}

	for i, arg := range args {
		val, err := resolveGrabArgValue(ev, arg)
		if err != nil {
			DEBUG("     [%d]: resolution failed\n    error: %s", i, err)
			return nil, err
		}

		// Allow nil values to pass through - they are valid values
		if val == nil {
			DEBUG("     [%d]: resolved to nil (allowed)", i)
		} else {
			DEBUG("  arg[%d]: resolved to value (type: %T)", i, val)
		}
		vals = append(vals, val)
		DEBUG("")
	}

	switch len(args) {
	case 0:
		DEBUG("  no arguments supplied to (( grab ... )) operation.  oops.")
		return nil, ansi.Errorf("no arguments specified to @c{(( grab ... ))}")

	case 1:
		DEBUG("  called with only one argument; returning value as-is")
		forwardVaultPlaceholder(ev, args[0], vals[0])
		return &Response{
			Type:  Replace,
			Value: vals[0],
		}, nil

	default:
		DEBUG("  called with more than one arguments; flattening top-level lists into a single list")
		flat := []interface{}{}
		for i, lst := range vals {
			switch v := lst.(type) {
			case []interface{}:
				DEBUG("    [%d]: value is a list; flattening it out", i)
				flat = append(flat, v...)
			default:
				DEBUG("    [%d]: value is not a list; appending it as-is", i)
				flat = append(flat, lst)
			}
		}
		DEBUG("")

		return &Response{
			Type:  Replace,
			Value: flat,
		}, nil
	}
}

//nolint:gochecknoinits // Operator registration must happen at package load time
func init() {
	RegisterOp("grab", GrabOperator{})
}
