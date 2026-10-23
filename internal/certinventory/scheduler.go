package certinventory

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	mrand "math/rand/v2"
	"net/http"
	"slices"
	"sort"
	"sync"
	"time"

	"blacksmith/internal/apierr"
)

// Scan states.
const (
	ScanIdle    = "idle"
	ScanRunning = "running"
	ScanFailed  = "failed"
)

const (
	firstScanDelay  = 30 * time.Second
	firstScanJitter = 5 * time.Second
	minConcurrency  = 1
	maxConcurrency  = 16
	minScanInterval = 5 * time.Minute
)

// ErrUnknownInstance reports a forced scan of an instance the index does
// not list.
var ErrUnknownInstance = apierr.New(http.StatusNotFound, apierr.CodeInstanceNotFound, "the broker does not know this instance")

// ScanState describes the current or most recent fleet scan.
type ScanState struct {
	ID          string
	State       string
	StartedAt   *time.Time
	CompletedAt *time.Time
	NextAt      time.Time
	Done, Total int
	Errors      []ScanError
}

// ScanError names an instance whose latest scan failed.
type ScanError struct {
	InstanceID    string     `json:"instance_id"`
	Error         string     `json:"error"`
	LastSuccessAt *time.Time `json:"last_success_at,omitempty"`
}

// MarshalJSON writes the scan block in the shape the inventory response
// uses, with the counts nested under progress.
func (st ScanState) MarshalJSON() ([]byte, error) {
	type progress struct {
		Done  int `json:"done"`
		Total int `json:"total"`
	}

	var next *time.Time
	if !st.NextAt.IsZero() {
		n := st.NextAt
		next = &n
	}

	errs := st.Errors
	if errs == nil {
		errs = []ScanError{}
	}

	return json.Marshal(struct {
		ID          string      `json:"id"`
		State       string      `json:"state"`
		StartedAt   *time.Time  `json:"started_at,omitempty"`
		CompletedAt *time.Time  `json:"completed_at,omitempty"`
		NextAt      *time.Time  `json:"next_at,omitempty"`
		Progress    progress    `json:"progress"`
		Errors      []ScanError `json:"errors"`
	}{st.ID, st.State, st.StartedAt, st.CompletedAt, next, progress{st.Done, st.Total}, errs})
}

// InstanceLister returns the service instances the broker knows, from the
// same Vault index that feeds /b/status.
type InstanceLister func(ctx context.Context) ([]InstanceRef, error)

type instanceScanner interface {
	ScanInstance(ctx context.Context, ref InstanceRef) InstanceScan
	ScanBroker(ctx context.Context, src BrokerSources) InstanceScan
}

// Scheduler runs fleet scans on a timer and on request, one at a time, and
// stores each instance's result in the cache. Each instance has a lock, so a
// forced scan of one instance never overlaps a fleet scan of it.
type Scheduler struct {
	scanner  instanceScanner
	cache    *Cache
	list     InstanceLister
	broker   func() BrokerSources
	settings func() Settings
	now      func() time.Time
	after    func(time.Duration) <-chan time.Time

	ctx    context.Context //nolint:containedctx // The scheduler owns this lifecycle context; Stop cancels it.
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu      sync.Mutex
	stopped bool
	started bool
	run     *fleetRun
	nextAt  time.Time
	errs    map[string]ScanError
	listErr string

	locksMu sync.Mutex
	locks   map[string]chan struct{}

	// divergence and sources, guarded by mu, reconcile the divergence
	// records after each instance scan. A nil divergence reconciles
	// nothing.
	divergence *DivergenceStore
	sources    ReconcileSources
}

// fleetRun is one fleet scan. Its fields other than id, done, and subset
// are guarded by the scheduler's mutex.
type fleetRun struct {
	id   string
	done chan struct{}
	// subset says the run scans only named instances, so it cannot stand
	// in for a timed full scan.
	subset      bool
	running     bool
	failed      bool
	startedAt   time.Time
	completedAt time.Time
	finished    int
	total       int
}

type scanJob struct {
	id  string
	run func(ctx context.Context) InstanceScan
}

// NewScheduler builds a scheduler. broker may be nil, which leaves the broker
// out of every scan. A nil now means time.Now and a nil after means
// time.After; every wait goes through after.
func NewScheduler(scanner instanceScanner, cache *Cache, list InstanceLister, broker func() BrokerSources, settings func() Settings, now func() time.Time, after func(time.Duration) <-chan time.Time) *Scheduler {
	if now == nil {
		now = time.Now
	}

	if after == nil {
		after = time.After
	}

	ctx, cancel := context.WithCancel(context.Background())

	return &Scheduler{
		scanner:  scanner,
		cache:    cache,
		list:     list,
		broker:   broker,
		settings: settings,
		now:      now,
		after:    after,
		ctx:      ctx,
		cancel:   cancel,
		errs:     map[string]ScanError{},
		locks:    map[string]chan struct{}{},
	}
}

// Start runs the timer loop. The first fleet scan starts 30 seconds later
// plus up to 5 seconds of jitter, and each later one waits the scan interval
// read from the settings just before the wait. Cancelling ctx has the same
// effect as Stop, except that it does not wait. Start does nothing after the
// first call or after Stop.
func (s *Scheduler) Start(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.started || s.stopped {
		return
	}

	s.started = true

	context.AfterFunc(ctx, s.cancel)

	s.wg.Add(1)

	go s.loop()
}

// Stop cancels the timer loop and any running scan and waits for them to
// end. Once stopped, the scheduler starts no more fleet scans.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	s.stopped = true
	s.mu.Unlock()

	s.cancel()
	s.wg.Wait()
}

func (s *Scheduler) loop() {
	defer s.wg.Done()

	// The jitter only spreads load, so it needs no secure source.
	delay := firstScanDelay + time.Duration(mrand.Int64N(int64(firstScanJitter)+1)) //nolint:gosec // jitter, not a secret

	for {
		s.mu.Lock()
		s.nextAt = s.now().Add(delay)
		s.mu.Unlock()

		select {
		case <-s.ctx.Done():
			return
		case <-s.after(delay):
		}

		// A full scan already running stands in for this one. A subset
		// run does not, so the full scan starts once it ends.
		for {
			run, joined := s.requestScan(nil)
			if run == nil {
				return
			}

			select {
			case <-s.ctx.Done():
				return
			case <-run.done:
			}

			if !joined || !run.subset {
				break
			}
		}

		delay = scanInterval(s.settings())
	}
}

// RequestScan starts a fleet scan, or a scan of only the named instances
// when ids is not empty, and returns its ID. While a scan runs, it returns
// that scan's ID and alreadyRunning true instead of starting another. After
// Stop it starts nothing and returns an empty ID.
func (s *Scheduler) RequestScan(ids []string) (string, bool) {
	run, running := s.requestScan(ids)
	if run == nil {
		return "", false
	}

	return run.id, running
}

func (s *Scheduler) requestScan(ids []string) (*fleetRun, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.stopped || s.ctx.Err() != nil {
		return nil, false
	}

	if s.run != nil && s.run.running {
		return s.run, true
	}

	run := &fleetRun{id: newScanID(), done: make(chan struct{}), subset: len(ids) > 0, running: true, startedAt: s.now()}
	s.run = run

	var subset map[string]bool

	if len(ids) > 0 {
		subset = make(map[string]bool, len(ids))
		for _, id := range ids {
			subset[id] = true
		}
	}

	s.wg.Add(1)

	go func() {
		defer s.wg.Done()
		defer close(run.done)

		s.fleetScan(s.ctx, run, subset)
	}()

	return run, false
}

// fleetScan scans every listed instance, or only those in subset, with
// scan_concurrency workers. An instance whose lock is held by a forced scan
// is skipped and keeps its data. A full scan drops instances the index no
// longer lists.
func (s *Scheduler) fleetScan(ctx context.Context, run *fleetRun, subset map[string]bool) {
	refs, err := within(ctx, vaultCallTimeout, func(c context.Context) ([]InstanceRef, error) {
		return s.list(c)
	})

	var jobs []scanJob

	if s.broker != nil && (subset == nil || subset[BrokerScanID]) {
		jobs = append(jobs, s.brokerJob())
	}

	listed := make(map[string]bool, len(refs))

	if err == nil {
		for _, ref := range refs {
			listed[ref.ID] = true

			if subset == nil || subset[ref.ID] {
				jobs = append(jobs, s.instanceJob(ref))
			}
		}
	}

	s.mu.Lock()
	run.total = len(jobs)

	if err != nil {
		s.listErr = "list the instances: " + err.Error()
	} else {
		s.listErr = ""
	}
	s.mu.Unlock()

	succeeded, failed := s.runJobs(ctx, run, jobs)

	if err == nil && subset == nil && ctx.Err() == nil {
		s.drop(listed)
	}

	s.mu.Lock()
	run.running = false
	run.completedAt = s.now()
	run.failed = err != nil || (succeeded == 0 && failed > 0)
	s.mu.Unlock()
}

// runJobs runs the jobs on the worker pool and returns how many scans
// succeeded and failed. Skipped and cancelled scans count as neither.
func (s *Scheduler) runJobs(ctx context.Context, run *fleetRun, jobs []scanJob) (int, int) {
	feed := make(chan scanJob)

	var (
		wg              sync.WaitGroup
		mu              sync.Mutex
		succeeded, fail int
	)

	for range min(scanConcurrency(s.settings()), max(len(jobs), 1)) {
		wg.Add(1)

		go func() {
			defer wg.Done()

			for job := range feed {
				ok, ran := s.tryScan(ctx, job)

				mu.Lock()
				switch {
				case ran && ok:
					succeeded++
				case ran:
					fail++
				}
				mu.Unlock()

				s.mu.Lock()
				run.finished++
				s.mu.Unlock()
			}
		}()
	}

feeding:
	for _, job := range jobs {
		select {
		case <-ctx.Done():
			break feeding
		case feed <- job:
		}
	}

	close(feed)
	wg.Wait()

	return succeeded, fail
}

// tryScan scans one instance when its lock is free and stores the result.
// It reports whether the scan succeeded and whether it ran and was stored.
func (s *Scheduler) tryScan(ctx context.Context, job scanJob) (bool, bool) {
	lock := s.lockFor(job.id)

	select {
	case lock <- struct{}{}:
	default:
		return false, false
	}

	scan, stored := s.scanHeld(ctx, job, lock)
	if !stored {
		return false, false
	}

	s.reconcile(ctx, scan)

	return scan.Err == "", true
}

// scanHeld runs the job while the caller holds the instance's lock, stores
// the scan unless ctx ended, and releases the lock. It reports whether the
// scan was stored.
func (s *Scheduler) scanHeld(ctx context.Context, job scanJob, lock chan struct{}) (InstanceScan, bool) {
	defer func() { <-lock }()

	scan := job.run(ctx)
	if ctx.Err() != nil {
		return scan, false
	}

	s.store(scan)

	return scan, true
}

// SetReconciler gives the scheduler the divergence store and the sources
// that reconciliation reads. The service calls it once, before Start. A
// nil store turns reconciliation off, and a nil src.Versions leaves every
// record as it is.
func (s *Scheduler) SetReconciler(store *DivergenceStore, src ReconcileSources) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.divergence, s.sources = store, src
}

// reconcile updates the instance's divergence record from a stored scan.
// It runs after the scan released the instance's lock, so a scan and a
// patch runner writing the record never wait on each other. A failure
// leaves the record as it was, and the next scan tries again.
func (s *Scheduler) reconcile(ctx context.Context, scan InstanceScan) {
	s.mu.Lock()
	store, src := s.divergence, s.sources
	s.mu.Unlock()

	if store == nil {
		return
	}

	if err := store.ReconcileScan(ctx, scan, src); err != nil {
		src.log().Errorf("certificate divergence: the record of instance %s could not be reconciled with its scan, so it stays as it was until the next scan: %v", scan.Ref.ID, err)
	}
}

// ScanInstanceNow scans one instance at once, waiting for its lock if a
// fleet scan holds it, and returns the instance's cache entry, which keeps
// its earlier data marked stale when this scan failed. An ID the index does
// not list returns ErrUnknownInstance. The broker answers to BrokerScanID.
func (s *Scheduler) ScanInstanceNow(ctx context.Context, id string) (InstanceScan, error) {
	// Stop cancels a forced scan as it cancels a fleet scan.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	defer context.AfterFunc(s.ctx, cancel)() //nolint:contextcheck // Stop cancels the scheduler context, which must end a forced scan too.

	if err := ctx.Err(); err != nil {
		return InstanceScan{}, err
	}

	var job *scanJob

	if id == BrokerScanID {
		if s.broker != nil {
			j := s.brokerJob()
			job = &j
		}
	} else {
		refs, err := within(ctx, vaultCallTimeout, func(c context.Context) ([]InstanceRef, error) {
			return s.list(c)
		})
		if err != nil {
			return InstanceScan{}, fmt.Errorf("list the instances: %w", err)
		}

		for _, ref := range refs {
			if ref.ID == id {
				j := s.instanceJob(ref)
				job = &j

				break
			}
		}
	}

	if job == nil {
		return InstanceScan{}, ErrUnknownInstance
	}

	lock := s.lockFor(id)

	select {
	case lock <- struct{}{}:
	case <-ctx.Done():
		return InstanceScan{}, ctx.Err()
	}

	scan, stored := s.scanHeld(ctx, *job, lock)
	if !stored {
		return scan, ctx.Err()
	}

	s.reconcile(ctx, scan)

	entry, ok := s.cache.Get(id)
	if !ok {
		return scan, nil
	}

	return entry, nil
}

// State returns a copy of the current or most recent fleet scan's state.
func (s *Scheduler) State() ScanState {
	s.mu.Lock()
	defer s.mu.Unlock()

	st := ScanState{State: ScanIdle, NextAt: s.nextAt}

	if run := s.run; run != nil {
		started := run.startedAt
		st.ID = run.id
		st.StartedAt = &started
		st.Done, st.Total = run.finished, run.total

		switch {
		case run.running:
			st.State = ScanRunning
		case run.failed:
			st.State = ScanFailed
		}

		if !run.running {
			completed := run.completedAt
			st.CompletedAt = &completed
		}
	}

	if s.listErr != "" {
		st.Errors = append(st.Errors, ScanError{Error: s.listErr})
	}

	ids := make([]string, 0, len(s.errs))
	for id := range s.errs {
		ids = append(ids, id)
	}

	sort.Strings(ids)

	for _, id := range ids {
		e := s.errs[id]
		if e.LastSuccessAt != nil {
			at := *e.LastSuccessAt
			e.LastSuccessAt = &at
		}

		st.Errors = append(st.Errors, e)
	}

	return st
}

// store records a scan in the cache. A failed scan of an instance with
// earlier data marks that data stale; a first failure stores the failed scan
// itself. Either way the instance's error is recorded with the time of its
// last success.
func (s *Scheduler) store(scan InstanceScan) {
	id := scan.Ref.ID

	if scan.Err == "" {
		if prev, ok := s.cache.Get(id); ok {
			scan.Material = keepLastGood(scan.Material, prev)
		}

		s.cache.Put(scan)

		s.mu.Lock()
		delete(s.errs, id)
		s.mu.Unlock()

		return
	}

	e := ScanError{InstanceID: id, Error: scan.Err}

	if prev, ok := s.cache.Get(id); ok {
		if !prev.LastSuccessAt.IsZero() {
			last := prev.LastSuccessAt
			e.LastSuccessAt = &last
		}

		s.cache.MarkFailed(id, scan.Err, scan.ScannedAt)
	} else {
		s.cache.Put(scan)
	}

	s.mu.Lock()
	s.errs[id] = e
	s.mu.Unlock()
}

// keepLastGood returns m with the last good certificate of every CredHub
// variable this scan could not read. A variable whose read failed, and
// which the previous scan held a certificate for, keeps that certificate
// and its versions along with the new error. A stale observation from the
// time of the good read goes with it, so the certificate's coverage says it
// is stale (spec 7.10). The previous scan's material is never changed.
func keepLastGood(m ScanMaterial, prev InstanceScan) ScanMaterial {
	good := map[string]CredHubCert{}

	for _, ch := range prev.Material.CredHub {
		if ch.Deployed != nil {
			good[ch.Name] = ch
		}
	}

	if len(good) == 0 {
		return m
	}

	// observedAt is when each carried certificate was last read, which a
	// carry from an earlier carry keeps.
	observedAt := map[string]time.Time{}

	for _, r := range prev.Material.Raw {
		if r.Source == SourceCredHub && r.State == StateStale {
			observedAt[r.Locator] = r.ObservedAt
		}
	}

	var credhub []CredHubCert

	for i, ch := range m.CredHub {
		last, ok := good[ch.Name]
		if ch.Err == "" || ch.Deployed != nil || !ok {
			continue
		}

		if credhub == nil {
			credhub = slices.Clone(m.CredHub)
			m.Raw = slices.Clip(m.Raw)
		}

		ch.Deployed, ch.Latest, ch.Versions = last.Deployed, last.Latest, last.Versions
		ch.Chain = slices.Clone(last.Chain)
		credhub[i] = ch

		at, ok := observedAt[ch.Name]
		if !ok {
			at = prev.ScannedAt
		}

		notAfter := last.Deployed.NotAfter

		m.Raw = append(m.Raw, RawObservation{
			Observation: Observation{
				Source:      SourceCredHub,
				Owner:       ch.Owner,
				Locator:     ch.Name,
				Fingerprint: Fingerprint(last.Deployed),
				Serial:      last.Deployed.SerialNumber.Text(16),
				NotAfter:    &notAfter,
				Matches:     MatchDeployed,
				State:       StateStale,
				ObservedAt:  at,
			},
			Cert: last.Deployed,
		})
	}

	if credhub != nil {
		m.CredHub = credhub
	}

	return m
}

// drop removes every cached instance the index no longer lists, keeping the
// broker entry. An instance whose lock is held, by a forced scan that is
// about to store a result, stays until a later fleet scan.
func (s *Scheduler) drop(listed map[string]bool) {
	for _, id := range s.cache.ids() {
		if id == BrokerScanID || listed[id] {
			continue
		}

		lock := s.lockFor(id)

		select {
		case lock <- struct{}{}:
		default:
			continue
		}

		s.cache.Remove(id)

		s.mu.Lock()
		delete(s.errs, id)
		s.mu.Unlock()

		<-lock
	}
}

func (s *Scheduler) instanceJob(ref InstanceRef) scanJob {
	return scanJob{id: ref.ID, run: func(ctx context.Context) InstanceScan {
		return s.scanner.ScanInstance(ctx, ref)
	}}
}

func (s *Scheduler) brokerJob() scanJob {
	return scanJob{id: BrokerScanID, run: func(ctx context.Context) InstanceScan {
		return s.scanner.ScanBroker(ctx, s.broker())
	}}
}

// lockFor returns an instance's scan lock, a channel that holds one token
// while a scan of that instance runs.
func (s *Scheduler) lockFor(id string) chan struct{} {
	s.locksMu.Lock()
	defer s.locksMu.Unlock()

	lock, ok := s.locks[id]
	if !ok {
		lock = make(chan struct{}, 1)
		s.locks[id] = lock
	}

	return lock
}

// scanConcurrency returns the worker count, which defaults to 4 and never
// leaves 1 to 16.
func scanConcurrency(st Settings) int {
	n := st.ScanConcurrency
	if n < minConcurrency {
		return DefaultSettings().ScanConcurrency
	}

	return min(n, maxConcurrency)
}

// scanInterval returns the wait between fleet scans. A value under the
// 5-minute minimum, which validation never saves, falls back to the default.
func scanInterval(st Settings) time.Duration {
	d := time.Duration(st.ScanIntervalMinutes) * time.Minute
	if d < minScanInterval {
		return time.Duration(DefaultSettings().ScanIntervalMinutes) * time.Minute
	}

	return d
}

func newScanID() string {
	var b [16]byte

	_, _ = rand.Read(b[:])

	return hex.EncodeToString(b[:])
}
