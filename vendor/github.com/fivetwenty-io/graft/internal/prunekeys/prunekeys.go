// Package prunekeys hands the operators package every path the current
// evaluation will remove from its output. That is the paths operators
// have queued for pruning so far, plus the paths the caller asked to
// prune once evaluation is done, as graft merge --prune does, which are
// not in the run's queue while operators run.
package prunekeys

// lookup returns the paths to prune for the evaluator ev.
var lookup func(ev any) []string

// SetLookup installs the function For calls. pkg/graft calls it once,
// from an init function.
func SetLookup(f func(ev any) []string) {
	lookup = f
}

// For returns every path the evaluator ev will prune, or nil when no
// hook is installed or ev prunes nothing.
func For(ev any) []string {
	if lookup == nil {
		return nil
	}
	return lookup(ev)
}
