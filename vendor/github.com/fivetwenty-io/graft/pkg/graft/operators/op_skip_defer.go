package operators

import (
	"fmt"
	"strings"
)

// This file implements the shared "--skip-vault"/"--skip-aws"/
// "--skip-nats" defer behavior: when one of these CLI flags (not
// REDACT=1 - see graft.OperatorState.IsRedactMode) skips a backend, the
// affected operator (vault, vault-try, awsparam, awssecret, nats) leaves
// its own "(( ... ))" expression intact in the output instead of making
// a backend call or substituting the flat "REDACTED" sentinel, exactly
// as if it had been wrapped in "(( defer ... ))" (DeferOperator.Run,
// op_defer.go, whose reconstructExpr this reuses). A document merged
// this way can be merged again later, once the backend is reachable,
// and evaluates cleanly at that point.

// deferSkippedCall implements that behavior for one operator call: it
// records the tree path being deferred (see
// graft.OperatorState.AddDeferredPath, consumed later by Phase 4's
// --report-deferred machinery) and returns a Replace response whose
// value is the call's own reconstructed source text. It does not
// resolve any argument, so a value composed from another
// deferred/unreachable lookup (e.g. a (( grab )) of a deferred vault
// value used as another vault call's path segment) defers too instead
// of erroring - see docs/user-guide/secrets/vault.md and
// plans/dennis-feedback-gaps.md's Item 3 "transitive defer" requirement.
func deferSkippedCall(ev *Evaluator, engine Engine, name string, args []*Expr) *Response {
	reason := fmt.Sprintf("skipped (--skip-%s)", skipFlagBackend(name))
	engine.GetOperatorState().AddDeferredPath(ev.Here.String(), reason)
	return &Response{
		Type:  Replace,
		Value: reconstructDeferredCall(operatorNameWithModifiers(name, ev), args),
	}
}

// skipFlagBackend maps an operator name to the --skip-<backend> flag
// name that defers it (cmd/graft's merge flags), for deferSkippedCall's
// reason text: "vault"/"vault-try" both defer under --skip-vault,
// "awsparam"/"awssecret" under --skip-aws, and "nats" under --skip-nats.
// An unrecognized name (defensive only - every caller today passes one
// of the five above) passes through unchanged rather than panicking.
func skipFlagBackend(operatorName string) string {
	switch operatorName {
	case "vault", "vault-try":
		return "vault"
	case "awsparam", "awssecret":
		return "aws"
	case "nats":
		return "nats"
	default:
		return operatorName
	}
}

// reconstructDeferredCall reconstructs an operator call's
// "(( name arg1 arg2 ... ))" source text from its own name (already
// including any ":nocache"/"@target" modifiers - see
// operatorNameWithModifiers) and already-parsed argument expressions,
// using the same reconstructExpr logic "(( defer <name> <args...> ))"
// already relies on (DeferOperator.Run, op_defer.go) to reconstruct a
// nested call.
func reconstructDeferredCall(name string, args []*Expr) string {
	components := make([]string, 0, len(args)+3)
	components = append(components, "((", name)
	for _, arg := range args {
		components = append(components, reconstructExpr(arg))
	}
	components = append(components, "))")
	return strings.Join(components, " ")
}

// operatorNameWithModifiers reconstructs "name[:nocache][@target]" - the
// form Opcall.Run parses apart into separate name/target/noCache fields
// (interfaces.go's Opcall) before calling an operator's Run - from the
// pieces available on ev (Target, NoCache) during Run() and the
// operator's own literal name. Order matches the parser's own
// convention (see docs/user-guide/secrets/vault.md's "Bypassing the
// Cache" section): ":nocache" precedes "@target", e.g.
// "vault:nocache@prod".
func operatorNameWithModifiers(name string, ev *Evaluator) string {
	if ev.NoCache {
		name += ":nocache"
	}
	if ev.Target != "" {
		name += "@" + ev.Target
	}
	return name
}
