package graft

import "context"

// runEngine is the Engine an evaluator sees during one run. It embeds
// both the shared *DefaultEngine and the run's own *operatorState. The
// run state's methods sit one level shallower than the ones the engine
// promotes from its own state, so they win: GetEngine(ev).GetOperatorState()
// and an assertion of GetEngine(ev) to OperatorState both reach this
// run's markers, while every other Engine method goes to the shared
// engine. Several runs can therefore share one engine without reading
// or clearing one another's markers.
type runEngine struct {
	*DefaultEngine
	*operatorState
}

// startRun begins a run on e. The run works on state, or on a fresh
// state from forRun when state is nil, and the returned runEngine is what
// the run's operators see. The caller must call finish exactly once when
// the run ends, on every exit path: finish publishes the run's outputs to
// e through absorb, passing publishPrunes along, and then clears the
// run's own prune, sort, and static IP markers.
func (e *DefaultEngine) startRun(state *operatorState) (run *runEngine, finish func(publishPrunes bool)) {
	if state == nil {
		state = e.forRun()
	}
	run = &runEngine{DefaultEngine: e, operatorState: state}
	finish = func(publishPrunes bool) {
		e.absorb(state, publishPrunes)
		state.resetPerRunState()
	}
	return run, finish
}

// GetOperatorState returns the run's own operator state.
func (r *runEngine) GetOperatorState() OperatorState {
	return r.operatorState
}

// runStateKey is the context key a merge carries its run state under,
// so the engine's Evaluate continues the run the merge builder started
// instead of starting another.
type runStateKey struct{}

// withRunState returns a copy of ctx carrying state as the current run's
// operator state.
func withRunState(ctx context.Context, state *operatorState) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, runStateKey{}, state)
}

// runStateFrom returns the run state ctx carries, or nil when it carries
// none.
func runStateFrom(ctx context.Context) *operatorState {
	if ctx == nil {
		return nil
	}
	state, _ := ctx.Value(runStateKey{}).(*operatorState)
	return state
}

// forRun returns a fresh operator state for one run. It copies only the
// settings - the skip, redact, and warning flags - and starts every map
// and list empty, so the run neither sees nor disturbs another run's
// markers. operatorState holds mutexes, so this builds a new value
// rather than copying e.
func (e *operatorState) forRun() *operatorState {
	e.vaultMutex.RLock()
	defer e.vaultMutex.RUnlock()
	return &operatorState{
		vaultRefs:         make(map[string][]string),
		vaultPlaceholders: make(map[string]string),
		usedIPs:           make(map[string]string),
		pathsToSort:       make(map[string]string),
		skipVault:         e.skipVault,
		skipAws:           e.skipAws,
		skipNats:          e.skipNats,
		redactMode:        e.redactMode,
		suppressWarnings:  e.suppressWarnings,
	}
}

// absorb publishes a finished run's outputs to e, the engine's state.
// The run's vault references, vault placeholders, and deferred paths are
// merged into what e already holds, so callers that read them from the
// engine after a run see what a run on an unshared engine always left
// there. When publishPrunes is true, the run's pruned paths replace e's
// as the most recently finished run's; a run that stopped before its
// prune step passes false and leaves the previous run's paths in place.
func (e *operatorState) absorb(run *operatorState, publishPrunes bool) {
	refs := run.GetVaultRefs()
	placeholders := run.snapshotVaultPlaceholders()

	e.vaultMutex.Lock()
	if e.vaultRefs == nil {
		e.vaultRefs = make(map[string][]string)
	}
	for path, keys := range refs {
		e.vaultRefs[path] = append(e.vaultRefs[path], keys...)
	}
	if e.vaultPlaceholders == nil {
		e.vaultPlaceholders = make(map[string]string)
	}
	for path, key := range placeholders {
		e.vaultPlaceholders[path] = key
	}
	e.vaultMutex.Unlock()

	if deferred := run.GetDeferredPaths(); len(deferred) > 0 {
		e.deferMutex.Lock()
		e.deferredPaths = append(e.deferredPaths, deferred...)
		e.deferMutex.Unlock()
	}

	if publishPrunes {
		run.pruneMutex.RLock()
		pruned := run.lastEvaluatedPrunedPaths
		run.pruneMutex.RUnlock()

		e.pruneMutex.Lock()
		e.lastEvaluatedPrunedPaths = pruned
		e.pruneMutex.Unlock()
	}
}

// opState returns the operator state this merge's post-processing reads
// and writes: the run state applyPostProcessing set in m.runState, or the
// engine's own state for an engine that is not a *DefaultEngine.
func (m *mergeBuilderImpl) opState() OperatorState {
	if m.runState != nil {
		return m.runState
	}
	return m.engine.GetOperatorState()
}
