package upgrade

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"blacksmith/internal/apierr"
	"blacksmith/internal/bosh"
	"blacksmith/internal/certinventory"
)

const (
	upgradePreviewIDPrefix = "up-"
	// purposeUpgradePreview and purposeUpgradeRecheck name the CredHub
	// reads of an upgrade of a diverged instance, in a failure's message.
	purposeUpgradePreview = "the upgrade preview"
	purposeUpgradeRecheck = "the re-check of an upgrade's approved changes"
	// approvalDiffers is the failure of an instance whose deploy would
	// apply changes the operator did not approve.
	approvalDiffers = "This instance's additional changes differ from what was approved. Preview the upgrade again."
)

// DivergedInstance is one instance of an upgrade that has certificates
// patched in place which BOSH has not deployed. Its deploy applies the
// latest version of every variable, so it lists each variable that the
// deploy changes besides the release and stemcell targets.
type DivergedInstance struct {
	InstanceID string `json:"instance_id"`
	// Divergence summarises the instance's record. It is reverted when any
	// patched variable is reverted.
	Divergence *certinventory.DivergenceRef `json:"divergence"`
	// Variables names the variables the record holds.
	Variables []string `json:"variables"`
	// Additional lists the variables whose latest version the deploy
	// applies in place of the deployed one.
	Additional []VersionPair `json:"additional"`
}

// UpgradePreview is the answer to an upgrade preview. It binds what the
// upgrade of each diverged instance would deploy, and it lives only in the
// broker's memory until it expires.
type UpgradePreview struct {
	ID          string    `json:"id"`
	ExpiresAt   time.Time `json:"expires_at"`
	InstanceIDs []string  `json:"instance_ids"`
	// Targets holds the request's stemcell and release targets, so a
	// create can tell that it asks for the upgrade that was previewed.
	Targets  []string           `json:"-"`
	Diverged []DivergedInstance `json:"diverged"`

	// boundAdditional holds the additional variables of each diverged
	// instance, which a task created from the preview copies.
	boundAdditional map[string][]VersionPair
}

// storedUpgradePreview is an upgrade preview with the moment it expires.
type storedUpgradePreview struct {
	preview *UpgradePreview
	expires time.Time
}

// UpgradePreviewStore keeps the upgrade previews in memory until they
// expire. It is apart from the rotation previews, so the ID of one kind of
// preview never finds a preview of the other.
type UpgradePreviewStore struct {
	now func() time.Time

	mu       sync.Mutex
	previews map[string]storedUpgradePreview
}

// NewUpgradePreviewStore returns an empty store that tells time with now.
func NewUpgradePreviewStore(now func() time.Time) *UpgradePreviewStore {
	if now == nil {
		now = time.Now
	}

	return &UpgradePreviewStore{now: now, previews: make(map[string]storedUpgradePreview)}
}

// Put stores the preview for ttl, sets its ExpiresAt, and drops every
// preview that has expired.
func (s *UpgradePreviewStore) Put(p *UpgradePreview, ttl time.Duration) {
	now := s.now()

	s.mu.Lock()
	defer s.mu.Unlock()

	for id, stored := range s.previews {
		if !now.Before(stored.expires) {
			delete(s.previews, id)
		}
	}

	p.ExpiresAt = now.Add(ttl).UTC()
	s.previews[p.ID] = storedUpgradePreview{preview: p, expires: p.ExpiresAt}
}

// Get returns the preview with the ID, or a 410 preview_expired error when
// the store has none or it has expired. A stored preview is never changed,
// so callers read it without copying it.
func (s *UpgradePreviewStore) Get(id string) (*UpgradePreview, error) {
	now := s.now()

	s.mu.Lock()
	defer s.mu.Unlock()

	stored, ok := s.previews[id]
	if ok && now.Before(stored.expires) {
		return stored.preview, nil
	}

	delete(s.previews, id)

	return nil, upgradePreviewExpired()
}

func upgradePreviewExpired() error {
	return apierr.New(http.StatusGone, apierr.CodePreviewExpired, "The upgrade preview has expired or is unknown, so preview the upgrade again.")
}

// checkUpgradeRequest checks the kind, the instances, and the targets of
// an upgrade request, as a create does before anything else.
func checkUpgradeRequest(req CreateTaskRequest) error {
	switch req.Kind {
	case "", TaskKindUpgrade:
	case TaskKindCertificates:
		return ErrCertificateTaskKind
	default:
		return fmt.Errorf("%w: %q", ErrInvalidTaskKind, req.Kind)
	}

	if len(req.InstanceIDs) == 0 {
		return ErrNoInstances
	}

	return validateTargets(req)
}

// upgradeTargets names the request's targets, sorted, so two requests for
// the same upgrade name the same targets.
func upgradeTargets(req CreateTaskRequest) []string {
	targets := make([]string, 0, 1+len(req.TargetReleases))

	if req.TargetStemcell.IsSet() {
		targets = append(targets, "stemcell "+req.TargetStemcell.OS+"/"+req.TargetStemcell.Version)
	}

	for _, target := range req.TargetReleases {
		targets = append(targets, "release "+target.Name+"/"+target.Version)
	}

	sort.Strings(targets)

	return targets
}

// uniqueIDs returns the IDs with each named once, in the order given.
func uniqueIDs(ids []string) []string {
	seen := make(map[string]bool, len(ids))
	out := make([]string, 0, len(ids))

	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}

	return out
}

// sameInstanceSet reports whether the two lists name the same instances.
func sameInstanceSet(a, b []string) bool {
	left, right := uniqueIDs(a), uniqueIDs(b)
	sort.Strings(left)
	sort.Strings(right)

	return slices.Equal(left, right)
}

// PreviewUpgrade checks an upgrade request as a create would, and lists
// for each instance with a divergence record the variables its deploy
// would apply at their latest versions. It stores the preview with the
// instances, the targets, and those variables, so a create that names it
// copies them. A request whose instances have no divergence record gets a
// preview with an empty list, and CredHub is never asked.
func (m *Manager) PreviewUpgrade(ctx context.Context, req CreateTaskRequest) (*UpgradePreview, error) {
	err := checkUpgradeRequest(req)
	if err != nil {
		return nil, err
	}

	err = m.checkReleasesOnDirector(ctx, req.TargetReleases)
	if err != nil {
		return nil, err
	}

	d := m.certDeps()
	if d.UpgradePreviews == nil {
		return nil, apierr.New(http.StatusInternalServerError, apierr.CodeInternal, "Blacksmith has no store for upgrade previews.")
	}

	ids := uniqueIDs(req.InstanceIDs)
	preview := &UpgradePreview{
		ID:              upgradePreviewIDPrefix + uuid.NewString(),
		InstanceIDs:     ids,
		Targets:         upgradeTargets(req),
		Diverged:        []DivergedInstance{},
		boundAdditional: make(map[string][]VersionPair),
	}

	for _, id := range ids {
		diverged, found, err := m.previewDiverged(ctx, id)
		if err != nil {
			return nil, err
		}

		if !found {
			continue
		}

		preview.Diverged = append(preview.Diverged, diverged)
		preview.boundAdditional[id] = slices.Clone(diverged.Additional)
	}

	d.UpgradePreviews.Put(preview, m.previewTTL())

	return preview, nil
}

// previewDiverged reads the instance's divergence record and, when it has
// one, the variables its deploy would apply. It reports whether the
// instance has a record.
func (m *Manager) previewDiverged(ctx context.Context, instanceID string) (DivergedInstance, bool, error) {
	record, found, err := m.divergenceOf(ctx, instanceID)
	if err != nil {
		return DivergedInstance{}, false, apierr.New(http.StatusInternalServerError, apierr.CodeInternal,
			fmt.Sprintf("The divergence record of instance %s could not be read: %v", instanceID, err))
	}

	if !found {
		return DivergedInstance{}, false, nil
	}

	instances, err := m.buildInstanceList(ctx, []string{instanceID})
	if err != nil {
		return DivergedInstance{}, false, apierr.New(http.StatusInternalServerError, apierr.CodeInternal,
			fmt.Sprintf("Blacksmith could not read instance %s: %v", instanceID, err))
	}

	instance := instances[0]
	if instance.Status == InstanceStatusFailed || instance.DeploymentName == "" {
		return DivergedInstance{}, false, apierr.New(http.StatusInternalServerError, apierr.CodeInternal,
			fmt.Sprintf("Blacksmith could not find the deployment of instance %s, so it can't list what its upgrade would deploy: %s", instanceID, instance.Error))
	}

	additional, err := m.currentAdditional(ctx, instance.DeploymentName, purposeUpgradePreview)
	if err != nil {
		return DivergedInstance{}, false, err
	}

	ref, variables := divergenceSummary(record)

	return DivergedInstance{
		InstanceID: instanceID,
		Divergence: ref,
		Variables:  variables,
		Additional: additional,
	}, true, nil
}

// divergenceOf reads the instance's divergence record. Without a store
// no instance has one.
func (m *Manager) divergenceOf(ctx context.Context, instanceID string) (*certinventory.Divergence, bool, error) {
	store := m.certDeps().Divergence
	if store == nil {
		return nil, false, nil
	}

	record, found, err := store.Get(ctx, instanceID)
	if err != nil {
		return nil, false, fmt.Errorf("reading the divergence record of %s: %w", instanceID, err)
	}

	return record, found, nil
}

// divergenceSummary returns one reference for the whole record and the
// names of its variables, sorted. The reference is a reverted variable's
// when the record has one, and otherwise the earliest variable's. A record
// with no variables still counts as unconverged.
func divergenceSummary(record *certinventory.Divergence) (*certinventory.DivergenceRef, []string) {
	var summary *certinventory.DivergenceRef

	names := make([]string, 0, len(record.Variables))

	for _, variable := range record.Variables {
		ref := certinventory.DivergenceRefOf(record, variable.Name)
		if ref == nil {
			continue
		}

		names = append(names, variable.Name)

		reverted := ref.State == certinventory.DivergenceReverted

		switch {
		case summary == nil:
			summary = ref
		case reverted && summary.State != certinventory.DivergenceReverted:
			summary = ref
		case ref.State == summary.State && ref.Since.Before(summary.Since):
			summary = ref
		}
	}

	sort.Strings(names)

	if summary == nil {
		summary = &certinventory.DivergenceRef{State: certinventory.DivergenceUnconverged}
	}

	return summary, names
}

// currentAdditional lists the deployment's variables whose latest CredHub
// version differs from the deployed one, which a deploy with
// force_latest_variables applies. It never returns nil, so an empty list
// still marks an approval.
func (m *Manager) currentAdditional(ctx context.Context, deployment, purpose string) ([]VersionPair, error) {
	api := m.certDeps().CredHub
	if api == nil {
		return nil, apierr.New(http.StatusForbidden, apierr.CodeCredHubNotConfigured,
			"CredHub is not configured, so Blacksmith can't list what the deploy would apply.")
	}

	if m.director == nil {
		return nil, apierr.New(http.StatusInternalServerError, apierr.CodeInternal, "Blacksmith has no BOSH director.")
	}

	variables, err := directorCall(ctx, func(c context.Context) ([]bosh.Variable, error) {
		return m.director.ListVariables(c, deployment)
	})
	if err != nil {
		return nil, apierr.New(http.StatusInternalServerError, apierr.CodeInternal,
			fmt.Sprintf("The director did not list the variables of deployment %s: %v", deployment, err))
	}

	latest, err := latestVersions(ctx, api, purpose, variables)
	if err != nil {
		return nil, err
	}

	pairs := differingPairs(variables, latest, nil)
	if pairs == nil {
		pairs = []VersionPair{}
	}

	return pairs, nil
}

// approvedDivergence finds the requested instances that have a divergence
// record and returns, for each, the additional variables the named upgrade
// preview bound. A request with no such instance needs no preview and gets
// nil. Otherwise the preview must be live and must name the same instances
// and the same targets, and must have bound each diverged instance. It
// never reserves or saves anything.
func (m *Manager) approvedDivergence(ctx context.Context, req CreateTaskRequest) (map[string][]VersionPair, error) {
	var diverged []string

	for _, id := range uniqueIDs(req.InstanceIDs) {
		_, found, err := m.divergenceOf(ctx, id)
		if err != nil {
			return nil, apierr.New(http.StatusInternalServerError, apierr.CodeInternal,
				fmt.Sprintf("The divergence record of instance %s could not be read, so Blacksmith can't tell whether the upgrade needs a preview: %v", id, err))
		}

		if found {
			diverged = append(diverged, id)
		}
	}

	if len(diverged) == 0 {
		return nil, nil //nolint:nilnil // no instance needs an approval, which is not an error
	}

	if req.UpgradePreviewID == "" {
		return nil, apierr.New(http.StatusConflict, apierr.CodeUpgradePreviewRequired,
			fmt.Sprintf("Instance %s has certificates patched in place that BOSH has not deployed, and its upgrade would deploy other changes too. Preview the upgrade, then create it with the preview's ID.", diverged[0]))
	}

	store := m.certDeps().UpgradePreviews
	if store == nil {
		return nil, upgradePreviewExpired()
	}

	preview, err := store.Get(req.UpgradePreviewID)
	if err != nil {
		return nil, err
	}

	if !sameInstanceSet(preview.InstanceIDs, req.InstanceIDs) || !slices.Equal(preview.Targets, upgradeTargets(req)) {
		return nil, apierr.New(http.StatusConflict, apierr.CodeUpgradePreviewRequired,
			"The upgrade preview was made for other instances or other targets than this upgrade. Preview the upgrade again.")
	}

	approved := make(map[string][]VersionPair, len(diverged))

	for _, id := range diverged {
		bound, ok := preview.boundAdditional[id]
		if !ok {
			return nil, apierr.New(http.StatusConflict, apierr.CodeUpgradePreviewRequired,
				fmt.Sprintf("Instance %s has certificates patched in place that the upgrade preview did not list. Preview the upgrade again.", id))
		}

		approved[id] = bound
	}

	return approved, nil
}

// approveInstances copies each diverged instance's approved variables into
// the instance, as a list that is never nil.
func approveInstances(instances []InstanceUpgrade, approved map[string][]VersionPair) {
	for i := range instances {
		bound, ok := approved[instances[i].InstanceID]
		if !ok {
			continue
		}

		pairs := slices.Clone(bound)
		if pairs == nil {
			pairs = []VersionPair{}
		}

		instances[i].ApprovedAdditional = pairs
	}
}

// outsideApproval names each current variable that the approval does not
// list at the same latest version.
func outsideApproval(current, approved []VersionPair) []string {
	listed := make(map[string]string, len(approved))
	for _, pair := range approved {
		listed[pair.Name] = pair.LatestVersionID
	}

	var outside []string

	for _, pair := range current {
		latest, ok := listed[pair.Name]
		if !ok || latest != pair.LatestVersionID {
			outside = append(outside, fmt.Sprintf("%s at %s", shortName(pair.Name), pair.LatestVersionID))
		}
	}

	return outside
}

// approvedAdditional reads the instance's approved variables under the
// manager's lock.
func (m *Manager) approvedAdditional(instance *InstanceUpgrade) []VersionPair {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return instance.ApprovedAdditional
}

// divergedDeployOptions decides how an upgrade deploys the instance,
// immediately before the deploy. An instance without a divergence record
// deploys as any upgrade does. One with a record deploys with
// force_latest_variables, so its patched certificates converge, but only
// when its upgrade was created from a preview that listed it and the
// variables the deploy would apply now are among those the preview listed.
// Otherwise the instance fails and nothing is sent. It never changes the
// divergence record. It reports whether the deploy may go on.
func (m *Manager) divergedDeployOptions(ctx, persistCtx context.Context, task *UpgradeTask, instance *InstanceUpgrade) (bosh.DeployOptions, bool) {
	_, found, err := m.divergenceOf(ctx, instance.InstanceID)
	if err != nil && stoppedBeforeTheDeploy(ctx, err) {
		m.returnToPending(persistCtx, task, instance, err)

		return bosh.DeployOptions{}, false
	}

	if err != nil {
		m.failInstance(persistCtx, task, instance,
			fmt.Sprintf("The divergence record of this instance could not be read, so Blacksmith can't tell what its deploy would apply: %v", err))

		return bosh.DeployOptions{}, false
	}

	if !found {
		return bosh.DeployOptions{}, true
	}

	approved := m.approvedAdditional(instance)
	if approved == nil {
		m.logger.Info("Instance %s of task %s has a divergence record that its upgrade's preview did not list", instance.InstanceID, task.ID)
		m.failInstance(persistCtx, task, instance, approvalDiffers)

		return bosh.DeployOptions{}, false
	}

	current, err := m.currentAdditional(ctx, instance.DeploymentName, purposeUpgradeRecheck)
	if err != nil && stoppedBeforeTheDeploy(ctx, err) {
		m.returnToPending(persistCtx, task, instance, err)

		return bosh.DeployOptions{}, false
	}

	if err != nil {
		m.failInstance(persistCtx, task, instance,
			fmt.Sprintf("Blacksmith could not check what this instance's deploy would apply: %v", err))

		return bosh.DeployOptions{}, false
	}

	if outside := outsideApproval(current, approved); len(outside) > 0 {
		m.logger.Info("Instance %s of task %s would deploy changes its preview did not list: %s",
			instance.InstanceID, task.ID, strings.Join(outside, ", "))
		m.failInstance(persistCtx, task, instance, approvalDiffers)

		return bosh.DeployOptions{}, false
	}

	return bosh.DeployOptions{ForceLatestVariables: true}, true
}
