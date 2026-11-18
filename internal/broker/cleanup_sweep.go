package broker

import (
	"context"
	"sync/atomic"
	"time"

	"blacksmith/internal/settings"
	"blacksmith/pkg/logger"
)

const (
	// cleanupSweepPassTimeout bounds one pass's reads of the index, the
	// listing, and the records, so a Vault that does not answer ends the
	// pass and the next pass reads again.
	cleanupSweepPassTimeout = 5 * time.Minute

	// cleanupSweepMaxBackoff is the longest the sweep waits before it
	// tries one instance's cleanup again.
	cleanupSweepMaxBackoff = 6 * time.Hour
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

// StartOwedCleanupSweep starts the sweep that tries again the cleanups
// failed provisions still owe, such as one whose delete the director
// refused or did not answer in time. The sweep runs until the service
// context ends, and the shutdown waits for it. With no director there is
// nothing to sweep.
//
// Each pass reads the index, the listing, and every record, and runs the
// cleanup of each failed provision whose record still owes one, as a start
// does. The cleanup reads the record again under the settle lock, so a
// cleanup that a poll or a start ran first deletes nothing more. An
// instance whose cleanup the sweep tried and that still owes it waits
// twice as long each time, up to cleanupSweepMaxBackoff.
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
	retries := map[string]*owedRetry{}

	for {
		interval := b.cleanupRetryInterval(ctx, logger)
		timer := time.NewTimer(interval)

		select {
		case <-ctx.Done():
			timer.Stop()

			return
		case <-timer.C:
		}

		b.sweepOwedCleanupsOnce(ctx, interval, retries, logger)
	}
}

// sweepOwedCleanupsOnce runs one pass. It forgets the retries of every
// instance that no longer owes a cleanup, and runs each owed cleanup whose
// backoff has passed.
func (b *Broker) sweepOwedCleanupsOnce(ctx context.Context, interval time.Duration, retries map[string]*owedRetry, logger logger.Logger) {
	passCtx, cancel := context.WithTimeout(ctx, cleanupSweepPassTimeout)
	defer cancel()

	store := b.readoptVaultStore()

	candidates, _, err := readoptListOnce(passCtx, store)
	if err != nil {
		logger.Warning("The sweep of owed cleanups could not read the instances from Vault, and reads them again in %v: %s", interval, err)

		return
	}

	err = readoptReadRecords(passCtx, store, candidates, logger)
	if err != nil {
		logger.Warning("The sweep of owed cleanups could not read the records from Vault, and reads them again in %v: %s", interval, err)

		return
	}

	cleanups := owedCleanups(candidates, logger)

	owed := make(map[string]bool, len(cleanups))
	for _, cleanup := range cleanups {
		owed[cleanup.instanceID] = true
	}

	for instanceID := range retries {
		if !owed[instanceID] {
			delete(retries, instanceID)
		}
	}

	for _, cleanup := range cleanups {
		if ctx.Err() != nil {
			return
		}

		retry := retries[cleanup.instanceID]
		if retry != nil && time.Now().Before(retry.next) {
			continue
		}

		if retry == nil {
			retry = &owedRetry{}
			retries[cleanup.instanceID] = retry
		}

		retry.tries++
		retry.next = time.Now().Add(owedRetryBackoff(interval, retry.tries))

		logger.Info("Instance %s: its failed provision still owes the delete of deployment %s, so the sweep tries the cleanup again", cleanup.instanceID, cleanup.deploymentName)
		b.cleanupFailedDeployment(ctx, cleanup.instanceID, cleanup.deploymentName, logger)
	}
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
