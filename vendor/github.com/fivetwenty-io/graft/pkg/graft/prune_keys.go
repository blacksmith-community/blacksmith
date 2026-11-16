package graft

import (
	"context"

	"github.com/fivetwenty-io/graft/internal/prunekeys"
)

//nolint:gochecknoinits // operators read the paths to prune through this hook
func init() {
	prunekeys.SetLookup(func(ev any) []string {
		e, ok := ev.(*Evaluator)
		if !ok || e == nil {
			return nil
		}
		return e.pathsToPrune()
	})
}

// pathsToPrune returns every path this evaluation will remove from its
// output, which is the paths operators have queued on the run's state
// so far, followed by the paths the caller prunes once it is done.
func (ev *Evaluator) pathsToPrune() []string {
	var keys []string
	if eng := EngineOf(ev); eng != nil {
		if state := eng.GetOperatorState(); state != nil {
			keys = state.GetKeysToPrune()
		}
	}
	return append(keys, ev.callerPrunes...)
}

// callerPrunesKey is the context key a merge carries its --prune paths
// under, so Evaluate can hand them to the evaluator.
type callerPrunesKey struct{}

// withCallerPrunes returns a copy of ctx carrying paths, the paths the
// caller prunes after evaluation.
func withCallerPrunes(ctx context.Context, paths []string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, callerPrunesKey{}, paths)
}

// callerPrunesFrom returns the paths ctx carries, or nil when it
// carries none.
func callerPrunesFrom(ctx context.Context) []string {
	if ctx == nil {
		return nil
	}
	paths, _ := ctx.Value(callerPrunesKey{}).([]string)
	return paths
}
