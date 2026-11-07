package operators

import (
	"fmt"
	"strconv"

	"github.com/fivetwenty-io/graft/internal/errtext"
	"github.com/fivetwenty-io/graft/internal/literal"
	"github.com/fivetwenty-io/graft/internal/pending"
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

// Dependencies returns the paths each argument reads, plus, for a path
// with a dynamic bracket key, the key's own path and the target the
// path reaches once every key holds its value.
func (GrabOperator) Dependencies(ev *Evaluator, args []*Expr, locs []*tree.Cursor, auto []*tree.Cursor) []*tree.Cursor {
	deps := make([]*tree.Cursor, 0, len(auto))
	deps = append(deps, auto...)

	for _, arg := range args {
		if arg != nil {
			argDeps := arg.Dependencies(ev, locs)
			deps = append(deps, argDeps...)
			hard, _ := bracketDependencies(ev, arg, locs)
			deps = append(deps, hard...)
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
func resolveGrabArgValue(ev *Evaluator, arg *Expr, read grabRead) (interface{}, error) {
	if arg == nil {
		return nil, nil
	}

	if arg.Type == LogicalOr {
		val, err := resolveGrabArgValue(ev, arg.Left, read)
		if err == nil {
			return val, nil
		}
		if pending.StopsFallback(err) {
			// A refused literal, or a target whose operator has not
			// run, is never a reason to fall back.
			return nil, err
		}
		return resolveGrabArgValue(ev, arg.Right, read)
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

	// Use ResolveOperatorArgument to handle nested expressions. A nested
	// call is run directly instead, so that a literal it returns, such as
	// the result of a nested (( grab )), stays a literal here.
	var val interface{}
	var err error
	before := literal.Reads(ev)
	if arg.Type == OperatorCall {
		val, err = evaluateNestedOperator(ev, arg)
	} else {
		val, err = ResolveOperatorArgument(ev, arg)
	}
	if err != nil {
		return nil, err
	}
	if computedFromLiteral(ev, arg, val, before) {
		return nil, literal.Refuse("grab", "a path")
	}

	if arg.Type == Reference {
		// Direct reference, resolve it normally. ResolveLiteral keeps a
		// value that sits inside a literal opaque at its new place.
		resolved, err := arg.Reference.ResolveLiteral(ev.Tree)
		if err != nil {
			return nil, fmt.Errorf("unable to resolve `%s`: %w", arg.Reference, err)
		}
		if err := checkPending(ev, arg.Reference, resolved, read); err != nil {
			return nil, err
		}
		return resolved, nil
	}

	if pathStr, ok := val.(string); ok && !valueIsFinal(arg) {
		return resolveComputedPath(ev, pathStr, read)
	}

	// For literals and other non-string values, use them directly.
	return val, nil
}

// computedFromLiteral reports whether val is a path that grab would
// compute from a literal: arg is neither a reference nor final, val is
// text, and resolving arg read a literal since Reads returned before. A
// literal never names the tree path grab reads, so grab refuses such a
// path, even one that would not parse as a path.
func computedFromLiteral(ev *Evaluator, arg *Expr, val interface{}, before int) bool {
	if arg.Type == Reference || valueIsFinal(arg) || !readsLiteral(ev, before) {
		return false
	}
	_, isText := literal.Strip(val).(string)
	return isText
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
func resolveComputedPath(ev *Evaluator, pathStr string, read grabRead) (interface{}, error) {
	cursor, err := tree.ParseCursor(pathStr)
	if err != nil {
		return pathStr, nil //nolint:nilerr // a non-path string is a legitimate grab result
	}

	resolved, err := cursor.ResolveLiteral(ev.Tree)
	if err != nil {
		return nil, fmt.Errorf("unable to resolve `%s`: %w", pathStr, err)
	}
	if err := checkPending(ev, cursor, resolved, read); err != nil {
		return nil, err
	}
	return resolved, nil
}

// grabRead says how the value a grab reads reaches its reader, which
// decides how much of it checkPending looks at.
type grabRead int

const (
	// readWritten is a grab of one path that the evaluator writes to the
	// tree. Its value is the same container as the target's, so each
	// operator under the target that runs later fills in the grab's
	// value too.
	readWritten grabRead = iota
	// readCopied is a grab of two or more paths that the evaluator
	// writes to the tree. It copies the entries of each list it reads
	// into a new list, which no operator fills in later.
	readCopied
	// readNested is a grab nested in another operator call's arguments,
	// whose value that operator reads straight away.
	readNested
)

// grabReadFor returns how the value of the grab call with arguments
// args, run on ev, reaches its reader.
func grabReadFor(ev *Evaluator, args []*Expr) grabRead {
	switch {
	case !pending.IsOuterCall(ev, args):
		return readNested
	case len(args) > 1:
		return readCopied
	default:
		return readWritten
	}
}

// checkPending returns a pending.NotEvaluatedError when the value val,
// which a grab read at the path target, holds an operator of the
// running phase that has not run yet. The grab would otherwise hand that
// operator's text on as if it were data. The target itself is checked
// by its path and by the entry of the container that holds it, so a path
// that reaches an operator through another grab's value is caught too.
// What is checked below the target depends on read. A nested grab's
// value is checked at every depth, because its reader uses it before
// any operator under it runs. A grab of two or more paths checks the
// entries of each list it copies. A grab of one path checks nothing
// below its target, because its value is the target's own container,
// which takes on each operator's value when that operator runs.
//
// The evaluator's record of operators still to run decides this, never
// the text at the path, so text that only looks like an operator, such
// as the output of (( defer )), is grabbed like any other value. A grab
// that reaches its own entry is caught as well, because the record
// holds an operator until it has run. Nothing is checked when the
// evaluator keeps no record.
func checkPending(ev *Evaluator, target *tree.Cursor, val interface{}, read grabRead) error {
	rec := pending.For(ev)
	if rec == nil || target == nil {
		return nil
	}
	notRun := func(where string) error {
		grab := ""
		if ev.Here != nil {
			grab = ev.Here.String()
		}
		return &pending.NotEvaluatedError{Grab: grab, Target: where}
	}
	canon := canonicalPath(ev, target)
	if where, ok := rec.At(canon.Nodes); ok {
		return notRun(where)
	}
	if n := len(canon.Nodes); n > 0 {
		parent, err := (&tree.Cursor{Nodes: canon.Nodes[:n-1]}).Resolve(ev.Tree)
		if err == nil {
			if where, ok := rec.In(parent, canon.Nodes[n-1]); ok {
				return notRun(where)
			}
		}
	}
	switch read {
	case readNested:
		if where, ok := rec.Within(val); ok {
			return notRun(where)
		}
	case readCopied:
		if list, ok := val.([]interface{}); ok {
			for i := range list {
				if where, ok := rec.In(list, strconv.Itoa(i)); ok {
					return notRun(where)
				}
			}
		}
	case readWritten:
	}
	return nil
}

// Run ...
func (GrabOperator) Run(ev *Evaluator, args []*Expr) (*Response, error) {
	DEBUG("running (( grab ... )) operation at $.%s", ev.Here)
	defer DEBUG("done with (( grab ... )) operation at $%s\n", ev.Here)

	var vals []interface{}
	read := grabReadFor(ev, args)

	for i, arg := range args {
		val, err := resolveGrabArgValue(ev, arg, read)
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
		return nil, errtext.Colorf("no arguments specified to @c{(( grab ... ))}")

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
			case literal.Value:
				// A literal list flattens like any other list, and each
				// item it gives up stays a literal.
				items, ok := v.Value().([]interface{})
				if !ok {
					flat = append(flat, lst)
					continue
				}
				DEBUG("    [%d]: value is a literal list; flattening it out", i)
				for _, item := range items {
					flat = append(flat, literal.Wrap(item))
				}
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

// SoftDependencies returns the path each argument reads, so the grab
// runs after the operators at or under it, other than the grab itself,
// unless that would close a cycle. The value it hands on then holds
// their values rather than their text. For a path with a dynamic
// bracket key that an operator still has to compute, the target is not
// known yet, so the path before that key stands in for it. A LogicalOr
// argument contributes both sides. A path an expression computes is not
// known until the grab runs and contributes nothing.
func (GrabOperator) SoftDependencies(ev *Evaluator, args []*Expr, locs []*tree.Cursor) []*tree.Cursor {
	out := make([]*tree.Cursor, 0, len(args))
	for _, arg := range args {
		out = append(out, grabSoftDependencies(ev, arg, locs)...)
	}
	return out
}

// grabSoftDependencies is SoftDependencies for one argument.
func grabSoftDependencies(ev *Evaluator, arg *Expr, locs []*tree.Cursor) []*tree.Cursor {
	if arg == nil {
		return nil
	}
	if arg.Type == LogicalOr {
		return append(grabSoftDependencies(ev, arg.Left, locs), grabSoftDependencies(ev, arg.Right, locs)...)
	}
	if arg.Type != Reference || arg.Reference == nil {
		return nil
	}
	if hasDynamicBracket(arg.Reference.Nodes, arg.BracketedNodes) {
		_, soft := referenceBracketDependencies(ev, arg.Reference.Nodes, arg.BracketedNodes, locs)
		return soft
	}
	return []*tree.Cursor{canonicalPath(ev, &tree.Cursor{Nodes: append([]string(nil), arg.Reference.Nodes...)})}
}
