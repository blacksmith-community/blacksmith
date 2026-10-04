package broker

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/config"
	"blacksmith/internal/credhub"
	"blacksmith/pkg/logger"
	"blacksmith/pkg/reconciler"
)

const (
	// credentialSweepInterval is the least time between two sweep passes.
	credentialSweepInterval = time.Hour
	// credentialSweepPassTimeout bounds one sweep pass.
	credentialSweepPassTimeout = 5 * time.Minute
	// credentialSweepMaxCandidates is how many proven orphans one pass handles.
	credentialSweepMaxCandidates = 10
	// guidLength is the length of an 8-4-4-4-12 GUID.
	guidLength = 36
	// indexStatusDeleted marks an index entry as a tombstone, not a live instance.
	indexStatusDeleted = "deleted"
)

// ErrSweepDirectorMismatch means the director's /info name differs from the
// configured credhub.director_name, so the sweep lists nothing.
var ErrSweepDirectorMismatch = errors.New("director /info name does not equal credhub.director_name")

// credentialFinder lists CredHub credential names under a path.
// *credhub.Client implements it.
type credentialFinder interface {
	FindByPath(ctx context.Context, path string) ([]string, error)
}

// credentialSweepState keeps one sweep pass at a time and at most one an hour.
type credentialSweepState struct {
	running   atomic.Bool
	mu        sync.Mutex
	lastStart time.Time
	now       func() time.Time
}

func (s *credentialSweepState) clock() time.Time {
	s.mu.Lock()
	now := s.now
	s.mu.Unlock()

	if now == nil {
		return time.Now()
	}

	return now()
}

// sweepCandidate is one /<director>/<deployment>/ path the sweep examines.
type sweepCandidate struct {
	deployment string
	instanceID string
	prefix     string
	names      []string
}

// sweepPass holds what one pass needs and counts what it did.
type sweepPass struct {
	broker   *Broker
	mode     string
	live     map[string]bool
	cleaner  credentialCleaner
	finder   credentialFinder
	policy   credhub.Policy
	index    map[string]interface{}
	log      logger.Logger
	examined int
	handled  int
	unproven int
	deferred int
}

// CredentialSweepMode reports the sweep mode: off, dry-run, or delete, or
// reconciler.CredentialSweepModeDisabled when the broker has no cleaner.
func (b *Broker) CredentialSweepMode() string {
	if b.CredentialCleaner == nil || b.Config == nil {
		return reconciler.CredentialSweepModeDisabled
	}

	return b.Config.CredHub.Cleanup.SweepMode()
}

// CatalogPlanIDs returns the plan IDs in the broker's catalog, sorted and
// without duplicates.
func (b *Broker) CatalogPlanIDs() []string {
	seen := make(map[string]bool, len(b.Plans))
	ids := make([]string, 0, len(b.Plans))

	for _, plan := range b.Plans {
		if plan.ID == "" || seen[plan.ID] {
			continue
		}

		seen[plan.ID] = true
		ids = append(ids, plan.ID)
	}

	sort.Strings(ids)

	return ids
}

// SweepOrphanedCredentials starts one sweep pass for CredHub variables left
// behind by deployments that no longer exist, and returns at once. It does
// nothing when no cleaner or finder is set, the mode is off, the deployment
// scan came back empty, a pass is still running, or a pass started less than
// an hour ago. The pass runs in the background under a five-minute deadline,
// and a panic in it is logged and contained.
func (b *Broker) SweepOrphanedCredentials(ctx context.Context, liveDeployments map[string]bool) {
	cleaner, finder := b.CredentialCleaner, b.CredentialFinder
	if cleaner == nil || finder == nil {
		return
	}

	mode := b.CredentialSweepMode()
	if mode != config.CredHubSweepDryRun && mode != config.CredHubSweepDelete {
		return
	}

	log := logger.Get().Named("broker")

	// An empty scan is more likely a failed scan than an empty director, and
	// every dead path would look like an orphan.
	if len(liveDeployments) == 0 {
		log.Debug("CredHub orphan sweep skipped, the reconciler's deployment scan returned nothing")

		return
	}

	if !b.credentialSweep.running.CompareAndSwap(false, true) {
		log.Debug("CredHub orphan sweep skipped, the previous pass is still running")

		return
	}

	now := b.credentialSweep.clock()

	b.credentialSweep.mu.Lock()
	tooSoon := !b.credentialSweep.lastStart.IsZero() && now.Sub(b.credentialSweep.lastStart) < credentialSweepInterval

	if !tooSoon {
		b.credentialSweep.lastStart = now
	}
	b.credentialSweep.mu.Unlock()

	if tooSoon {
		b.credentialSweep.running.Store(false)

		return
	}

	live := make(map[string]bool, len(liveDeployments))
	for name, isLive := range liveDeployments {
		live[name] = isLive
	}

	b.credentialCleanups.Add(1)

	go func() {
		defer b.credentialCleanups.Done()
		defer b.credentialSweep.running.Store(false)

		defer func() {
			recovered := recover()
			if recovered != nil {
				log.Error("CredHub orphan sweep panicked and stopped this pass. The next pass runs in an hour. Panic: %v\n%s", recovered, debug.Stack())
			}
		}()

		passCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), credentialSweepPassTimeout)
		defer cancel()

		pass := &sweepPass{broker: b, mode: mode, live: live, cleaner: cleaner, finder: finder, log: log}
		pass.run(passCtx)
	}()
}

// run lists /<director>/, proves each candidate an orphan, and handles at most
// credentialSweepMaxCandidates proven ones.
func (p *sweepPass) run(ctx context.Context) {
	cfg := p.broker.Config.CredHub
	root := "/" + cfg.DirectorName + "/"

	infoName, err := p.confirmDirector(cfg.DirectorName)
	if err != nil {
		p.log.Error("CredHub orphan sweep (%s) listed nothing under %s, because it could not confirm the director: %s. Likely causes are a director that is unreachable or a credhub.director_name that does not match the director's /info name.",
			p.mode, root, err)

		return
	}

	p.policy = credhub.Policy{
		DirectorName:         cfg.DirectorName,
		InfoDirectorName:     infoName,
		PlanIDs:              p.broker.CatalogPlanIDs(),
		ProtectedDeployments: cfg.Cleanup.ProtectedDeployments,
	}

	index, err := p.broker.Vault.GetIndex(ctx, "db")
	if err != nil {
		p.log.Error("CredHub orphan sweep (%s) listed nothing under %s, because it could not read the service index to rule out live instances: %s", p.mode, root, err)

		return
	}

	p.index = index.Data

	names, err := p.finder.FindByPath(ctx, root)
	if err != nil {
		p.log.Error("CredHub orphan sweep (%s) could not list the credentials under %s: %s. Likely causes are an unreachable CredHub, a client without credhub.read, or a token UAA refused. The next pass tries again in an hour.",
			p.mode, root, err)

		return
	}

	candidates := p.candidates(root, names)

	for index, candidate := range candidates {
		if ctx.Err() != nil || p.handled >= credentialSweepMaxCandidates {
			p.deferred = len(candidates) - index

			break
		}

		p.examined++

		requestedAt, reason := p.prove(ctx, candidate)
		if reason != "" {
			p.unproven++
			p.log.Info("CredHub orphan sweep (%s) left %d credentials under %s alone, because deployment %s is not a proven orphan: %s. If Cloud Foundry no longer has instance %s, list them with `credhub find -p %s` and remove each one with `credhub delete -n <name>`.",
				p.mode, len(candidate.names), candidate.prefix, candidate.deployment, reason, candidate.instanceID, candidate.prefix)

			continue
		}

		p.handled++
		p.handle(ctx, candidate, requestedAt)
	}

	p.log.Info("CredHub orphan sweep (%s) under %s examined %d candidate deployments: %d proven orphans handled, %d unproven, %d deferred to the next pass",
		p.mode, root, p.examined, p.handled, p.unproven, p.deferred)
}

// confirmDirector checks that /info names the configured director and
// returns that name.
func (p *sweepPass) confirmDirector(configured string) (string, error) {
	info, err := p.broker.BOSH.GetInfo()
	if err != nil {
		return "", fmt.Errorf("director /info failed: %w", err)
	}

	if info == nil || info.Name != configured {
		reported := ""
		if info != nil {
			reported = info.Name
		}

		return "", fmt.Errorf("%w: /info reports %q and the configuration says %q", ErrSweepDirectorMismatch, reported, configured)
	}

	return info.Name, nil
}

// candidates groups the listed names by deployment segment and keeps the
// segments that look like a Blacksmith deployment of a catalog plan and are
// not in the reconciler's scan of live deployments.
func (p *sweepPass) candidates(root string, names []string) []sweepCandidate {
	grouped := make(map[string][]string)

	for _, name := range names {
		rest, found := strings.CutPrefix(name, root)
		if !found {
			continue
		}

		segment, leaf, nested := strings.Cut(rest, "/")
		if !nested || segment == "" || leaf == "" || strings.Contains(leaf, "/") {
			p.log.Debug("CredHub orphan sweep skipped %s, which is not a single variable of one deployment", name)

			continue
		}

		grouped[segment] = append(grouped[segment], name)
	}

	segments := make([]string, 0, len(grouped))
	for segment := range grouped {
		segments = append(segments, segment)
	}

	sort.Strings(segments)

	var candidates []sweepCandidate

	for _, segment := range segments {
		if p.live[segment] {
			p.log.Debug("CredHub orphan sweep skipped %s%s/, the deployment is live", root, segment)

			continue
		}

		instanceID := ""
		if len(segment) > guidLength {
			instanceID = segment[len(segment)-guidLength:]
		}

		prefix, err := credhub.DeploymentPrefix(credhub.Target{InstanceID: instanceID, DeploymentName: segment}, p.policy)
		if err != nil {
			p.logShapeRefusal(root, segment, len(grouped[segment]), err)

			continue
		}

		candidates = append(candidates, sweepCandidate{deployment: segment, instanceID: instanceID, prefix: prefix, names: grouped[segment]})
	}

	return candidates
}

// logShapeRefusal logs a path the guard will not treat as a Blacksmith
// deployment. A path of a plan that left the catalog is worth an operator's
// attention; any other path is not Blacksmith's and is logged at debug.
func (p *sweepPass) logShapeRefusal(root, segment string, count int, err error) {
	if errors.Is(err, credhub.ErrPlanNotInCatalog) {
		p.log.Info("CredHub orphan sweep (%s) left %d credentials under %s%s/ alone, because %s. A plan that has left the catalog is never swept, so delete them by hand if the instance is gone.",
			p.mode, count, root, segment, err)

		return
	}

	p.log.Debug("CredHub orphan sweep skipped %s%s/, it is not a Blacksmith deployment the guard accepts: %s", root, segment, err)
}

// prove applies sweep proof rules 3 and 4. It returns the time Cloud Foundry
// requested the deprovision, or the reason the candidate is not proven.
func (p *sweepPass) prove(ctx context.Context, candidate sweepCandidate) (time.Time, string) {
	_, err := p.broker.BOSH.GetDeployment(candidate.deployment)
	if err == nil {
		return time.Time{}, "the director still has the deployment"
	}

	if !errors.Is(err, bosh.ErrDeploymentNotFound) {
		return time.Time{}, fmt.Sprintf("the director could not confirm the deployment is gone (%s)", err)
	}

	task, err := p.broker.BOSH.FindRunningTaskForDeployment(candidate.deployment)
	if err != nil {
		return time.Time{}, fmt.Sprintf("the director could not say whether a task is running on it (%s)", err)
	}

	if task != nil {
		return time.Time{}, fmt.Sprintf("task %d (%s) is %s on it", task.ID, task.Description, task.State)
	}

	requestedAt, requested := p.broker.hasDeprovisionRequest(ctx, candidate.instanceID)
	if !requested {
		return time.Time{}, "there is no deprovision request on record, because " + candidate.instanceID + "/metadata has no delete_requested_at"
	}

	age := time.Since(requestedAt)
	if age < reconciler.OrphanSweepMinimumAge {
		return time.Time{}, fmt.Sprintf("Cloud Foundry requested the deprovision only %s ago, under the %s minimum", age.Round(time.Minute), reconciler.OrphanSweepMinimumAge)
	}

	if indexHasLiveEntry(p.index, candidate.instanceID) {
		return time.Time{}, "the service index still has a live entry for instance " + candidate.instanceID
	}

	return requestedAt, ""
}

// handle logs a proven orphan in dry-run, or hands it to the cleaner in
// delete mode, which proves everything again before it deletes.
func (p *sweepPass) handle(ctx context.Context, candidate sweepCandidate, requestedAt time.Time) {
	if p.mode == config.CredHubSweepDryRun {
		owned, _ := credhub.FilterOwned(candidate.prefix, candidate.names, p.policy)
		p.log.Info("CredHub orphan sweep (dry-run) would delete %d credentials under %s (deployment absent, Cloud Foundry requested the deprovision at %s): %s",
			len(owned), candidate.prefix, requestedAt.Format(time.RFC3339), strings.Join(owned, ", "))

		return
	}

	p.cleaner.CleanupDeployment(ctx, credhub.Target{InstanceID: candidate.instanceID, DeploymentName: candidate.deployment})
}

// indexHasLiveEntry reports whether the index holds an entry for instanceID
// that is not a deleted tombstone.
func indexHasLiveEntry(index map[string]interface{}, instanceID string) bool {
	entry, found := index[instanceID]
	if !found {
		return false
	}

	fields, isMap := entry.(map[string]interface{})
	if !isMap {
		return true
	}

	status, _ := fields["status"].(string)

	return status != indexStatusDeleted
}
