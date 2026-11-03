package graft

import "sync"

// operatorState holds the state operators read and write through the
// OperatorState interface. DefaultEngine embeds it, so its methods
// promote onto *DefaultEngine. Each run works on its own operatorState,
// built by forRun, and absorb publishes the run's outputs into the
// engine's embedded one when the run ends (see run_state.go). It holds
// mutexes, so it must never be copied by value.
type operatorState struct {
	// Vault state (refs tracking only; client and cache live in internal/backends/vault)
	vaultRefs  map[string][]string
	vaultMutex sync.RWMutex
	// skipVault, skipAws, skipNats, redactMode, and suppressWarnings are
	// settings, guarded by vaultMutex like the vault state. forRun reads
	// them under it, so a setter called while a run starts is safe.
	skipVault bool

	// vaultPlaceholders maps a tree path (Cursor.String()) to the vault
	// key a skip-vault sentinel written there stands in for, guarded by
	// vaultMutex alongside vaultRefs. See RecordVaultPlaceholder.
	vaultPlaceholders map[string]string

	// redactMode selects, for skipVault/skipAws/skipNats above, whether a
	// skipped backend returns the flat "REDACTED" sentinel (true) or
	// defers itself, leaving its own "(( ... ))" expression intact
	// (false, the default). Set at construction by WithRedact. REDACT=1
	// forces it true (see evaluate) regardless of construction options,
	// but only on the run's own copy of the settings, never on the
	// engine's.
	redactMode bool

	// deferredPaths lists every tree path deferred so far, from either
	// source: a --skip-vault/--skip-aws/--skip-nats flag (redactMode
	// false - see op_skip_defer.go), or graft merge --defer-on-error/
	// --adaptive's own retry loop (cmd/graft). Guarded by deferMutex. Read
	// back by cmd/graft's --report-deferred comment rendering.
	deferredPaths []DeferredPath
	deferMutex    sync.RWMutex

	// AWS state (client and cache live in internal/backends/aws)
	skipAws bool

	// NATS state
	skipNats bool

	// Static IPs state
	usedIPs map[string]string
	ipMutex sync.RWMutex

	// Prune state
	keysToPrune []string
	pruneMutex  sync.RWMutex

	// lastEvaluatedPrunedPaths is a snapshot of the operator-queued
	// (( prune )) paths (dotted, no "$." prefix) actually removed by a
	// run, captured on the run's state just before evaluate()'s deferred
	// resetPerRunState clears keysToPrune, and published to the engine's
	// state by absorb as the most recently finished run's - see
	// evaluate()'s "Post-processing: apply operator-level pruning" step
	// and GetLastEvaluatedPrunedPaths. Guarded by pruneMutex alongside
	// keysToPrune, the state it is derived from.
	lastEvaluatedPrunedPaths []string

	// Sort state
	pathsToSort map[string]string
	sortMutex   sync.RWMutex

	// Warning suppression
	suppressWarnings bool
}

// EngineContext interface methods for internal operator access

// AddVaultRef records a Vault reference for tracking.
func (e *operatorState) AddVaultRef(path string, keys []string) {
	e.vaultMutex.Lock()
	defer e.vaultMutex.Unlock()

	if e.vaultRefs[path] == nil {
		e.vaultRefs[path] = []string{}
	}
	e.vaultRefs[path] = append(e.vaultRefs[path], keys...)
}

// IsVaultSkipped returns true if Vault operations should be skipped.
func (e *operatorState) IsVaultSkipped() bool {
	e.vaultMutex.RLock()
	defer e.vaultMutex.RUnlock()
	return e.skipVault
}

// SetRedactMode sets whether a skipped backend (skipVault/skipAws/
// skipNats) returns the flat "REDACTED" sentinel (true) or defers itself
// (false). See the redactMode field's doc comment.
func (e *operatorState) SetRedactMode(v bool) {
	e.vaultMutex.Lock()
	defer e.vaultMutex.Unlock()
	e.redactMode = v
}

// IsRedactMode returns true if a skipped backend should return the flat
// "REDACTED" sentinel rather than defer itself.
func (e *operatorState) IsRedactMode() bool {
	e.vaultMutex.RLock()
	defer e.vaultMutex.RUnlock()
	return e.redactMode
}

// AddDeferredPath records that path (Cursor.String()) deferred instead
// of evaluating, and why. See the deferredPaths field's doc comment.
func (e *operatorState) AddDeferredPath(path, reason string) {
	e.deferMutex.Lock()
	defer e.deferMutex.Unlock()
	e.deferredPaths = append(e.deferredPaths, DeferredPath{Path: path, Reason: reason})
}

// GetDeferredPaths returns a copy of the paths deferred so far.
func (e *operatorState) GetDeferredPaths() []DeferredPath {
	e.deferMutex.RLock()
	defer e.deferMutex.RUnlock()

	paths := make([]DeferredPath, len(e.deferredPaths))
	copy(paths, e.deferredPaths)
	return paths
}

// ResetDeferredPaths clears the deferred-path list.
func (e *operatorState) ResetDeferredPaths() {
	e.deferMutex.Lock()
	defer e.deferMutex.Unlock()
	e.deferredPaths = nil
}

// IsAWSSkipped returns true if AWS operations should be skipped.
func (e *operatorState) IsAWSSkipped() bool {
	e.vaultMutex.RLock()
	defer e.vaultMutex.RUnlock()
	return e.skipAws
}

// GetUsedIPs returns a copy of the used IP addresses map.
func (e *operatorState) GetUsedIPs() map[string]string {
	e.ipMutex.RLock()
	defer e.ipMutex.RUnlock()

	ips := make(map[string]string)
	for k, v := range e.usedIPs {
		ips[k] = v
	}
	return ips
}

// SetUsedIP records a used IP address.
func (e *operatorState) SetUsedIP(key, ip string) {
	e.ipMutex.Lock()
	defer e.ipMutex.Unlock()
	e.usedIPs[key] = ip
}

// AddKeyToPrune adds a key to the list of keys to prune.
func (e *operatorState) AddKeyToPrune(key string) {
	e.pruneMutex.Lock()
	defer e.pruneMutex.Unlock()
	e.keysToPrune = append(e.keysToPrune, key)
}

// GetKeysToPrune returns a copy of the keys to prune.
func (e *operatorState) GetKeysToPrune() []string {
	e.pruneMutex.RLock()
	defer e.pruneMutex.RUnlock()

	keys := make([]string, len(e.keysToPrune))
	copy(keys, e.keysToPrune)
	return keys
}

// AddPathToSort adds a path to sort with the specified order.
func (e *operatorState) AddPathToSort(path, order string) {
	e.sortMutex.Lock()
	defer e.sortMutex.Unlock()
	e.pathsToSort[path] = order
}

// GetPathsToSort returns a copy of the paths to sort.
func (e *operatorState) GetPathsToSort() map[string]string {
	e.sortMutex.RLock()
	defer e.sortMutex.RUnlock()

	paths := make(map[string]string)
	for k, v := range e.pathsToSort {
		paths[k] = v
	}
	return paths
}

// GetVaultRefs returns a copy of the vault references map.
func (e *operatorState) GetVaultRefs() map[string][]string {
	e.vaultMutex.RLock()
	defer e.vaultMutex.RUnlock()

	result := make(map[string][]string)
	for k, v := range e.vaultRefs {
		refs := make([]string, len(v))
		copy(refs, v)
		result[k] = refs
	}
	return result
}

// snapshotVaultPlaceholders returns a copy of the vault placeholders
// recorded so far, keyed by tree path, taken under vaultMutex like
// GetVaultRefs.
func (e *operatorState) snapshotVaultPlaceholders() map[string]string {
	e.vaultMutex.RLock()
	defer e.vaultMutex.RUnlock()

	result := make(map[string]string, len(e.vaultPlaceholders))
	for path, key := range e.vaultPlaceholders {
		result[path] = key
	}
	return result
}

// RecordVaultPlaceholder remembers that treePath received the
// skip-vault sentinel for vaultKey, so a later vault-path-building
// reference to treePath can render a symbolic "<vaultKey>" form (see
// VaultPlaceholderFor and op_vault.go's vaultArgProcessor) instead of
// the literal "REDACTED" text.
func (e *operatorState) RecordVaultPlaceholder(treePath, vaultKey string) {
	e.vaultMutex.Lock()
	defer e.vaultMutex.Unlock()
	if e.vaultPlaceholders == nil {
		e.vaultPlaceholders = make(map[string]string)
	}
	e.vaultPlaceholders[treePath] = vaultKey
}

// VaultPlaceholderFor reports the vault key a skip-vault sentinel
// written at treePath stands in for, if any.
func (e *operatorState) VaultPlaceholderFor(treePath string) (string, bool) {
	e.vaultMutex.RLock()
	defer e.vaultMutex.RUnlock()
	key, ok := e.vaultPlaceholders[treePath]
	return key, ok
}

// ResetVaultPlaceholders clears the vault placeholder tracking map.
func (e *operatorState) ResetVaultPlaceholders() {
	e.vaultMutex.Lock()
	defer e.vaultMutex.Unlock()
	e.vaultPlaceholders = make(map[string]string)
}

// ResetVaultRefs clears the vault references map.
func (e *operatorState) ResetVaultRefs() {
	e.vaultMutex.Lock()
	defer e.vaultMutex.Unlock()
	e.vaultRefs = make(map[string][]string)
}

// SetSkipVault sets whether vault operations should be skipped.
func (e *operatorState) SetSkipVault(v bool) {
	e.vaultMutex.Lock()
	defer e.vaultMutex.Unlock()
	e.skipVault = v
}

// SetSkipAws sets whether AWS operations should be skipped.
func (e *operatorState) SetSkipAws(v bool) {
	e.vaultMutex.Lock()
	defer e.vaultMutex.Unlock()
	e.skipAws = v
}

// SetSkipNats sets whether NATS operations should be skipped.
func (e *operatorState) SetSkipNats(v bool) {
	e.vaultMutex.Lock()
	defer e.vaultMutex.Unlock()
	e.skipNats = v
}

// IsNATSSkipped returns true if NATS operations should be skipped.
func (e *operatorState) IsNATSSkipped() bool {
	e.vaultMutex.RLock()
	defer e.vaultMutex.RUnlock()
	return e.skipNats
}

// GetLastEvaluatedPrunedPaths returns a copy of the operator-queued
// (( prune )) paths the most recently finished Evaluate() call on this
// engine actually removed. Unlike GetKeysToPrune, this survives past
// Evaluate() returning (absorb publishes it to the engine, and
// resetPerRunState clears only the run's keysToPrune), so a caller
// inspecting the engine after a merge - cmd/graft/merge_history.go's
// buildMergeHistorySteps, distinguishing a path an operator pruned away
// from one that merely evaluated to an explicit YAML null - can still see
// what was pruned. On an engine shared by concurrent calls, the most
// recently finished call may be another caller's. Returns nil if
// Evaluate() has not run on this engine yet, or its most recent run
// queued no prunes.
func (e *operatorState) GetLastEvaluatedPrunedPaths() []string {
	e.pruneMutex.RLock()
	defer e.pruneMutex.RUnlock()
	if len(e.lastEvaluatedPrunedPaths) == 0 {
		return nil
	}
	paths := make([]string, len(e.lastEvaluatedPrunedPaths))
	copy(paths, e.lastEvaluatedPrunedPaths)
	return paths
}

// ResetKeysToPrune clears the keys to prune list.
func (e *operatorState) ResetKeysToPrune() {
	e.pruneMutex.Lock()
	defer e.pruneMutex.Unlock()
	e.keysToPrune = nil
}

// ResetPathsToSort clears the paths to sort map.
func (e *operatorState) ResetPathsToSort() {
	e.sortMutex.Lock()
	defer e.sortMutex.Unlock()
	e.pathsToSort = make(map[string]string)
}

// ResetUsedIPs clears the used IPs map.
func (e *operatorState) ResetUsedIPs() {
	e.ipMutex.Lock()
	defer e.ipMutex.Unlock()
	e.usedIPs = make(map[string]string)
}

// resetPerRunState clears the prune/sort/used-IP markers that accumulate
// during a single evaluate() run. evaluate defers it on the run's own
// state, so it fires exactly once per run, after this run's own
// post-processing has already read the markers it needs, and never
// touches another run's markers.
func (e *operatorState) resetPerRunState() {
	e.ResetKeysToPrune()
	e.ResetPathsToSort()
	e.ResetUsedIPs()
}

// SuppressWarnings returns whether warnings should be suppressed.
func (e *operatorState) SuppressWarnings() bool {
	e.vaultMutex.RLock()
	defer e.vaultMutex.RUnlock()
	return e.suppressWarnings
}

// SetSuppressWarnings sets whether warnings should be suppressed.
func (e *operatorState) SetSuppressWarnings(v bool) {
	e.vaultMutex.Lock()
	defer e.vaultMutex.Unlock()
	e.suppressWarnings = v
}
