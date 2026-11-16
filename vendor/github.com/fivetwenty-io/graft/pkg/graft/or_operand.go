package graft

import (
	"errors"
	"os"

	"github.com/fivetwenty-io/graft/internal/pending"
)

// errEmptyVariable is why a || falls back from an environment variable
// that is unset or empty.
var errEmptyVariable = errors.New("environment variable is unset or empty")

// orOperand evaluates x, an operand of a || that a later operand can
// still replace, with eval. An environment variable that is unset or
// empty fails there, so the || falls back, as it does for
// (( grab $NAME || ... )). A || in that place, as on the left of
// $A || $B || "fb", applies the same rule to both of its operands, so
// the chain reaches its last operand. The last operand of a chain, and
// a variable anywhere else, give the empty string as before.
func orOperand[T any](x *Expr, eval func(*Expr) (T, error)) (T, error) {
	var zero T
	switch {
	case x != nil && x.Type == EnvVar && os.Getenv(x.Name) == "":
		return zero, errEmptyVariable
	case x != nil && x.Type == LogicalOr:
		left, err := orOperand(x.Left, eval)
		if err == nil || pending.StopsFallback(err) {
			return left, err
		}
		return orOperand(x.Right, eval)
	}
	return eval(x)
}

// pruneFallback reports whether x, the operand a || falls back to, is
// (( prune )). When it is, the path ev is at is marked for pruning, so
// the key leaves the output and the || gives nil in its place. An
// evaluator with no path, or with no engine to hold the mark, marks
// nothing. EngineOf, not GetEngine, so no throwaway engine is built.
func pruneFallback(x *Expr, ev *Evaluator) bool {
	if x == nil || x.Type != OperatorCall || x.Operator != "prune" {
		return false
	}
	if ev == nil || ev.Here == nil {
		return true
	}
	if eng := EngineOf(ev); eng != nil {
		if state := eng.GetOperatorState(); state != nil {
			state.AddKeyToPrune(ev.Here.Quoted())
		}
	}
	return true
}
