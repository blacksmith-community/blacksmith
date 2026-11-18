package broker

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"blacksmith/internal/settings"
	"blacksmith/pkg/logger"
	vaultPkg "blacksmith/pkg/vault"
)

const (
	// cleanupSweepPassTimeout bounds one pass's reads of the index, the
	// listing, and the records, so a Vault that does not answer ends the
	// pass and the next pass reads again.
	cleanupSweepPassTimeout = 5 * time.Minute

	// cleanupSweepMaxBackoff is the longest the sweep waits before it
	// tries one instance's cleanup again.
	cleanupSweepMaxBackoff = 6 * time.Hour

	// cleanupSweepFullScanInterval is how often the sweep reads every
	// instance's record rather than only the suspects'.
	cleanupSweepFullScanInterval = 6 * time.Hour
)

// cleanupRetryIntervalOverride, when positive, replaces the interval the
// settings give between passes of the sweep of owed cleanups.
//
//nolint:gochecknoglobals // a test seam, set only through export_test.go
var cleanupRetryIntervalOverride atomic.Int64

// owedRetry is how often one instance's owed cleanup has been tried by the
// sweep without the mark coming off, and when the sweep tries it next.
type owedRetry struct {
	tries int
	next  time.Time
}

// stalledProvision is a provision the sweep found in progress on record
// with no index entry, under the context ID it carried, and when the sweep
// first saw it so.
type stalledProvision struct {
	contextID string
	since     time.Time
}

// cleanupSweep is the state the sweep of owed cleanups keeps between
// passes. Its zero value is ready to use.
//
// suspects holds the instances that may owe a cleanup: every instance
// whose failed provision was recorded with the mark, whose cleanup ran,
// or that boot found owing one. A pass reads only their records, and
// drops each one whose record no longer owes a cleanup. A full scan of
// the index, the listing, and every record runs on the first pass and
// then every cleanupSweepFullScanInterval, so an instance a suspect entry
// missed waits at most that long.
type cleanupSweep struct {
	// pass serializes passes, and guards everything below mu's fields.
	pass     sync.Mutex
	lastFull time.Time
	retries  map[string]*owedRetry
	stalled  map[string]stalledProvision

	mu       sync.Mutex
	suspects map[string]uint64
	noted    uint64
	clock    func() time.Time
}

// now reads the sweep's clock, which a spec may replace.
func (s *cleanupSweep) now() time.Time {
	s.mu.Lock()
	clock := s.clock
	s.mu.Unlock()

	if clock == nil {
		return time.Now()
	}

	return clock()
}

// note adds instanceID to the suspects.
func (s *cleanupSweep) note(instanceID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.suspects == nil {
		s.suspects = map[string]uint64{}
	}

	s.noted++
	s.suspects[instanceID] = s.noted
}

// snapshot returns the suspects, sorted, with the note each was last
// added under.
func (s *cleanupSweep) snapshot() ([]string, map[string]uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	notes := make(map[string]uint64, len(s.suspects))
	ids := make([]string, 0, len(s.suspects))

	for instanceID, note := range s.suspects {
		notes[instanceID] = note
		ids = append(ids, instanceID)
	}

	sort.Strings(ids)

	return ids, notes
}

// drop removes instanceID from the suspects, unless it was added again
// since the snapshot that gave note.
func (s *cleanupSweep) drop(instanceID string, note uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.suspects[instanceID] == note {
		delete(s.suspects, instanceID)
	}
}

// noteOwedCleanup tells the sweep that instanceID may owe a cleanup.
func (b *Broker) noteOwedCleanup(instanceID string) {
	b.sweep.note(instanceID)
}

// StartOwedCleanupSweep starts the sweep that tries again the cleanups
// failed provisions still owe, such as one whose delete the director
// refused or did not answer in time. The sweep runs until the service
// context ends, and the shutdown waits for it. With no director there is
// nothing to sweep.
//
// Each pass reads the records of the suspects, or of every instance on a
// full scan, and runs the cleanup of each failed provision whose record
// still owes one, as a start does. The cleanup reads the record again
// under the settle lock, so a cleanup that a poll or a start ran first
// deletes nothing more. An instance whose cleanup the sweep tried and
// that still owes it waits twice as long each time, up to
// cleanupSweepMaxBackoff.
func (b *Broker) StartOwedCleanupSweep() {
	if b.BOSH == nil {
		return
	}

	b.track("sweep of owed cleanups", b.sweepOwedCleanups)
}

// sweepOwedCleanups runs a pass after each interval until the service
// context ends. It reads the interval again before each wait, so a change
// to the setting takes effect without a restart.
func (b *Broker) sweepOwedCleanups() {
	ctx := b.serviceContext()
	logger := logger.Get().Named("cleanup-sweep")

	for {
		interval := b.cleanupRetryInterval(ctx, logger)
		timer := time.NewTimer(interval)

		select {
		case <-ctx.Done():
			timer.Stop()

			return
		case <-timer.C:
		}

		b.sweepOwedCleanupsOnce(ctx, interval, logger)
	}
}

// sweepOwedCleanupsOnce runs one pass. It reads the records of the
// suspects, or of every instance when a full scan is due, forgets the
// retries and the suspects of every instance read that no longer owes a
// cleanup, and runs each owed cleanup whose backoff has passed.
func (b *Broker) sweepOwedCleanupsOnce(ctx context.Context, interval time.Duration, logger logger.Logger) {
	sweep := &b.sweep

	sweep.pass.Lock()
	defer sweep.pass.Unlock()

	if sweep.retries == nil {
		sweep.retries = map[string]*owedRetry{}
		sweep.stalled = map[string]stalledProvision{}
	}

	now := sweep.now()
	full := sweep.lastFull.IsZero() || now.Sub(sweep.lastFull) >= cleanupSweepFullScanInterval

	passCtx, cancel := context.WithTimeout(ctx, cleanupSweepPassTimeout)
	defer cancel()

	store := b.readoptVaultStore()
	suspects, notes := sweep.snapshot()

	var (
		candidates []*readoptCandidate
		err        error
	)

	if full {
		candidates, _, err = readoptListOnce(passCtx, store)
	} else {
		candidates, err = suspectCandidates(passCtx, store, suspects)
	}

	if err != nil {
		logger.Warning("The sweep of owed cleanups could not read the instances from Vault, and reads them again in %v: %s", interval, err)

		return
	}

	err = readoptReadRecords(passCtx, store, candidates, logger)
	if err != nil {
		logger.Warning("The sweep of owed cleanups could not read the records from Vault, and reads them again in %v: %s", interval, err)

		return
	}

	if full {
		sweep.lastFull = now
	}

	cleanups := owedCleanups(candidates)

	owed := make(map[string]bool, len(cleanups))
	for _, cleanup := range cleanups {
		owed[cleanup.instanceID] = true
	}

	stalled := b.adoptStalledProvisions(candidates, now, full, logger) //nolint:contextcheck // an adopted provision outlives the pass and runs under the service context

	for instanceID := range stalled {
		owed[instanceID] = true
	}

	b.forgetSettledSuspects(candidates, owed, notes, full)
	b.retryOwedCleanups(ctx, cleanups, now, interval, logger)
}

// retryOwedCleanups runs the cleanup of each owed cleanup whose backoff
// has passed, and sets when the sweep may try it next.
func (b *Broker) retryOwedCleanups(ctx context.Context, cleanups []owedCleanup, now time.Time, interval time.Duration, logger logger.Logger) {
	sweep := &b.sweep

	for _, cleanup := range cleanups {
		if ctx.Err() != nil {
			return
		}

		retry := sweep.retries[cleanup.instanceID]
		if retry != nil && now.Before(retry.next) {
			continue
		}

		if retry == nil {
			retry = &owedRetry{}
			sweep.retries[cleanup.instanceID] = retry
		}

		retry.tries++
		retry.next = now.Add(owedRetryBackoff(interval, retry.tries))

		logger.Info("Instance %s: its failed provision still owes the delete of deployment %s, so the sweep tries the cleanup again", cleanup.instanceID, cleanup.deploymentName)
		b.cleanupFailedDeployment(ctx, cleanup.instanceID, cleanup.deploymentName, logger)
	}
}

// adoptStalledProvisions finds each provision that is in progress on
// record with no index entry, and that no provision in this process owns.
// Such a provision's goroutine is gone, so nothing else records its
// failure or deletes what its deploy left. Once the sweep has seen it so
// for longer than provisionTimeout, by when no live provision can still
// own it, the sweep claims it and settles it the way boot re-adoption
// does, which records the failure with the mark and runs the cleanup. It
// returns the provisions it is still watching, which stay suspects.
func (b *Broker) adoptStalledProvisions(candidates []*readoptCandidate, now time.Time, full bool, logger logger.Logger) map[string]bool {
	sweep := &b.sweep
	watching := map[string]bool{}
	read := make(map[string]bool, len(candidates))

	for _, candidate := range candidates {
		read[candidate.instanceID] = true
		recorded := candidate.recorded

		if !b.unownedUnindexedProvision(candidate) {
			delete(sweep.stalled, candidate.instanceID)

			continue
		}

		seen, ok := sweep.stalled[candidate.instanceID]
		if !ok || seen.contextID != recorded.ContextID {
			sweep.stalled[candidate.instanceID] = stalledProvision{contextID: recorded.ContextID, since: now}
			watching[candidate.instanceID] = true

			continue
		}

		if now.Sub(seen.since) <= provisionTimeout {
			watching[candidate.instanceID] = true

			continue
		}

		if !b.adoptStalledProvision(candidate.instanceID, recorded, now, logger) {
			watching[candidate.instanceID] = true

			continue
		}

		delete(sweep.stalled, candidate.instanceID)
	}

	if full {
		for instanceID := range sweep.stalled {
			if !read[instanceID] {
				delete(sweep.stalled, instanceID)
			}
		}
	}

	// A provision a full scan found stays a suspect, so the passes between
	// full scans keep reading it.
	for instanceID := range watching {
		sweep.note(instanceID)
	}

	return watching
}

// unownedUnindexedProvision reports whether candidate's record holds a
// provision in progress or initializing, with no index entry, that no
// provision in this process owns.
func (b *Broker) unownedUnindexedProvision(candidate *readoptCandidate) bool {
	recorded := candidate.recorded
	if candidate.indexed || !candidate.exists || !candidate.parsed || recorded.Action != operationTypeProvision {
		return false
	}

	if recorded.State != recordedStateInProgress && recorded.State != recordedStateInitializing {
		return false
	}

	return !b.provisionActive(candidate.instanceID)
}

// adoptStalledProvision claims a stalled provision as boot re-adoption
// claims one, and settles it in a goroutine of its own. It reports false
// when the instance is claimed already or held in the mutation guard.
func (b *Broker) adoptStalledProvision(instanceID string, recorded recordedOperation, now time.Time, logger logger.Logger) bool {
	claim := &operationClaim{operation: operationTypeProvision, claimed: now, readopted: true}
	claim.launched.Store(true)

	_, taken, err := b.claimOperation(instanceID, recorded.DeploymentName, claim)
	if err != nil || !taken {
		logger.Info("Instance %s: its provision has been in progress with no index entry for longer than %v, and the instance is claimed or held, so the sweep looks again on its next pass", instanceID, provisionTimeout)

		return false
	}

	logger.Warning("Instance %s: its provision has been in progress with no index entry for longer than %v, and no provision in this broker owns it, so the sweep settles it", instanceID, provisionTimeout)

	operation := readoptOperation{instanceID: instanceID, deploymentName: recorded.DeploymentName, recorded: recorded, claim: claim}

	b.track("adoption of the stalled provision of instance "+instanceID, func() {
		b.readoptOperation(operation, now, nil, logger)
	})

	return true
}

// forgetSettledSuspects drops the suspects, the retries, and the
// overview's attention entries of every instance the pass read that no
// longer owes a cleanup. A full scan read every instance, so it also
// drops those of an instance it did not find.
func (b *Broker) forgetSettledSuspects(candidates []*readoptCandidate, owed map[string]bool, notes map[string]uint64, full bool) {
	read := make(map[string]bool, len(candidates))
	for _, candidate := range candidates {
		read[candidate.instanceID] = true
	}

	settled := func(instanceID string) bool {
		return !owed[instanceID] && (full || read[instanceID])
	}

	for instanceID, note := range notes {
		if settled(instanceID) {
			b.sweep.drop(instanceID, note)
		}
	}

	for instanceID := range b.sweep.retries {
		if settled(instanceID) {
			delete(b.sweep.retries, instanceID)
		}
	}

	for _, entry := range b.CleanupAttention() {
		if settled(entry.InstanceID) {
			b.forgetCleanupAttention(entry.InstanceID)
		}
	}
}

// suspectCandidates reads the index and returns a candidate for each
// suspect, indexed when the index names it.
func suspectCandidates(ctx context.Context, store readoptStore, suspects []string) ([]*readoptCandidate, error) {
	if len(suspects) == 0 {
		return nil, nil
	}

	idx, err := store.GetIndex(ctx, "db")
	if err != nil {
		return nil, fmt.Errorf("failed to read the instance index: %w", err)
	}

	candidates := make([]*readoptCandidate, 0, len(suspects))

	for _, instanceID := range suspects {
		raw, indexed := idx.Data[instanceID]
		if !indexed || raw == nil {
			candidates = append(candidates, &readoptCandidate{instanceID: instanceID})

			continue
		}

		entry := copyIndexEntry(raw)

		candidates = append(candidates, &readoptCandidate{
			instanceID:     instanceID,
			indexed:        true,
			entry:          entry,
			deploymentName: vaultPkg.EntryDeploymentName(instanceID, entry),
		})
	}

	return candidates, nil
}

// owedRetryBackoff is how long the sweep waits after its tries-th try of
// one cleanup before it tries again: the interval after the first, twice
// that after the second, and so on, up to cleanupSweepMaxBackoff.
func owedRetryBackoff(interval time.Duration, tries int) time.Duration {
	backoff := interval

	for range tries - 1 {
		if backoff >= cleanupSweepMaxBackoff/2 {
			return cleanupSweepMaxBackoff
		}

		backoff *= 2
	}

	return min(backoff, cleanupSweepMaxBackoff)
}

// cleanupRetryInterval is the interval between passes of the sweep, from
// the Vault settings, then BLACKSMITH_CLEANUP_RETRY_INTERVAL, then
// broker.cleanup_retry_interval, then the default. Each layer it refuses is
// logged.
func (b *Broker) cleanupRetryInterval(ctx context.Context, logger logger.Logger) time.Duration {
	override := cleanupRetryIntervalOverride.Load()
	if override > 0 {
		return time.Duration(override)
	}

	var configured *time.Duration
	if b.Config != nil {
		configured = b.Config.Broker.CleanupRetryInterval
	}

	var store settings.Store
	if b.Vault != nil {
		store = b.Vault
	}

	readCtx, cancel := recordContext(ctx)
	defer cancel()

	choice := settings.CleanupRetryInterval(readCtx, store, configured)

	for _, refusal := range choice.Refused {
		logger.Warning("Ignoring the cleanup retry interval from %s: %v; the interval is %v from %s", refusal.Source, refusal.Err, choice.Interval, choice.Source)
	}

	return choice.Interval
}
