package upgrade

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"blacksmith/internal/apierr"
	"blacksmith/internal/bosh"
	"blacksmith/internal/certinventory"
	"blacksmith/internal/credhub"
	"blacksmith/internal/mutation"
)

const (
	previewIDPrefix = "pv-"
	// defaultPreviewTTL applies when the settings name no positive TTL.
	defaultPreviewTTL = 15 * time.Minute
	// latestReadWorkers is how many CredHub version reads a preview runs at
	// once.
	latestReadWorkers = 4
	// gateSingleVM is the health gate of an instance with one VM, which
	// needs no cluster check between VMs.
	gateSingleVM = "single-vm"
	hoursPerDay  = 24
	patchWarning = "Temporary repair: BOSH can restore the old certificate until this instance converges."
)

// Selection names an instance and the full CredHub names of the variables
// to rotate on it. A converge selects no variables.
type Selection struct {
	InstanceID string   `json:"instance_id"`
	Variables  []string `json:"variables"`
}

// PreviewRequest is the body of a rotation preview.
type PreviewRequest struct {
	Mode       RotationMode   `json:"mode"`
	Converge   ConvergePolicy `json:"converge,omitempty"`
	Selections []Selection    `json:"selections"`
}

// Blocked says why an instance cannot take part in a rotation.
type Blocked struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// PreviewVariable describes one selected variable to the operator.
type PreviewVariable struct {
	Name              string `json:"name"`
	DeployedVersionID string `json:"deployed_version_id"`
	LatestVersionID   string `json:"latest_version_id"`
	DaysLeft          int    `json:"days_left"`
}

// PreviewConsumer is one place on a VM that patch mode would change.
type PreviewConsumer struct {
	VM           string `json:"vm"`
	Job          string `json:"job"`
	Files        int    `json:"files"`
	Verification string `json:"verification"`
}

// PreviewInstance describes what a rotation would do to one instance, and
// binds the effects the operator approves by confirming it.
type PreviewInstance struct {
	InstanceID     string            `json:"instance_id"`
	InstanceName   string            `json:"instance_name"`
	Service        string            `json:"service"`
	Plan           string            `json:"plan"`
	VMs            int               `json:"vms"`
	Mode           RotationMode      `json:"mode"`
	Gate           string            `json:"gate"`
	ManifestDigest string            `json:"manifest_digest"`
	Selected       []PreviewVariable `json:"selected"`
	Additional     []VersionPair     `json:"additional"`
	Consumers      []PreviewConsumer `json:"consumers"`
	Blocked        *Blocked          `json:"blocked"`
	Impact         string            `json:"impact"`

	// approved holds the effects a task created from this preview copies
	// into its record.
	approved ApprovedEffects
	// gate is the health gate the task will apply.
	gate       string
	deployment string
	serviceID  string
	planID     string
	// certificateIDs maps each selected name to its CredHub certificate ID.
	certificateIDs map[string]string
	// failure is set when the instance could not be previewed at all, and
	// the whole preview then fails with it.
	failure error
}

// Preview is the answer to a rotation preview. It lives only in the
// broker's memory, until it expires.
type Preview struct {
	ID        string            `json:"preview_id"`
	ExpiresAt time.Time         `json:"expires_at"`
	Instances []PreviewInstance `json:"instances"`
	Warnings  []string          `json:"warnings"`

	mode     RotationMode
	converge ConvergePolicy
	// forTask, forInstance, and decisionSince bind a preview to one
	// decision of one task's instance. They are empty for an ordinary
	// preview.
	forTask       string
	forInstance   string
	decisionSince time.Time
	// selection holds the full names a bound preview selected on its
	// instance, sorted.
	selection []string
}

// storedPreview is a preview with the moment it expires. Exactly one of
// preview and command is set, and kind says which.
type storedPreview struct {
	kind    PreviewKind
	preview *Preview
	command *CommandPreview
	expires time.Time
}

// PreviewStore keeps the previews in memory until they expire.
type PreviewStore struct {
	now func() time.Time

	mu       sync.Mutex
	previews map[string]storedPreview

	// spends counts the calls of Spend, so a test can tell a peek from a
	// spend that was put back.
	spends atomic.Int64
}

// NewPreviewStore returns an empty store that tells time with now.
func NewPreviewStore(now func() time.Time) *PreviewStore {
	if now == nil {
		now = time.Now
	}

	return &PreviewStore{now: now, previews: make(map[string]storedPreview)}
}

// Put stores the preview for ttl, sets its ExpiresAt, and drops every
// preview that has expired.
func (s *PreviewStore) Put(p *Preview, ttl time.Duration) {
	now := s.now()

	s.mu.Lock()
	defer s.mu.Unlock()

	for id, stored := range s.previews {
		if !now.Before(stored.expires) {
			delete(s.previews, id)
		}
	}

	p.ExpiresAt = now.Add(ttl).UTC()
	s.previews[p.ID] = storedPreview{kind: PreviewKindCertificate, preview: p, expires: p.ExpiresAt}
}

// PutCommand stores the command preview for ttl, sets its ExpiresAt, and
// drops every preview that has expired.
func (s *PreviewStore) PutCommand(p *CommandPreview, ttl time.Duration) {
	now := s.now()

	s.mu.Lock()
	defer s.mu.Unlock()

	for id, stored := range s.previews {
		if !now.Before(stored.expires) {
			delete(s.previews, id)
		}
	}

	p.ExpiresAt = now.Add(ttl).UTC()
	s.previews[p.ID] = storedPreview{kind: PreviewKindCommand, command: p, expires: p.ExpiresAt}
}

// restoreCommand puts a spent command preview back with the expiry it had,
// so a create whose reservation failed, before any save was attempted, can
// be tried again. A preview that has expired meanwhile stays gone. The
// remaining window is the span between the spend and this call, one
// critical section of the manager wide, where a create on the same preview
// with another request ID answers 410 preview_expired although the preview
// comes back a moment later.
func (s *PreviewStore) restoreCommand(p *CommandPreview) {
	now := s.now()

	s.mu.Lock()
	defer s.mu.Unlock()

	if !now.Before(p.ExpiresAt) {
		return
	}

	s.previews[p.ID] = storedPreview{kind: PreviewKindCommand, command: p, expires: p.ExpiresAt}
}

// SpentPreview is what Spend returns, with the field of its kind set.
type SpentPreview struct {
	Certificate *Preview
	Command     *CommandPreview
}

// Spend returns the preview with the ID and removes it, under one lock, so
// exactly one caller spends it. A preview of another kind is refused with a
// 409 preview_mismatch and stays in the store, and one that is unknown or
// has expired answers 410 preview_expired.
func (s *PreviewStore) Spend(id string, kind PreviewKind) (SpentPreview, error) {
	s.spends.Add(1)

	now := s.now()

	s.mu.Lock()
	defer s.mu.Unlock()

	stored, ok := s.previews[id]
	if !ok || !now.Before(stored.expires) {
		delete(s.previews, id)

		return SpentPreview{}, errPreviewGone()
	}

	if stored.kind != kind {
		return SpentPreview{}, errPreviewKind(kind)
	}

	delete(s.previews, id)

	return SpentPreview{Certificate: stored.preview, Command: stored.command}, nil
}

// GetCommand returns the command preview with the ID without spending it.
func (s *PreviewStore) GetCommand(id string) (*CommandPreview, error) {
	now := s.now()

	s.mu.Lock()
	defer s.mu.Unlock()

	stored, ok := s.previews[id]
	if !ok || !now.Before(stored.expires) {
		delete(s.previews, id)

		return nil, errPreviewGone()
	}

	if stored.kind != PreviewKindCommand {
		return nil, errPreviewKind(PreviewKindCommand)
	}

	return stored.command, nil
}

func errPreviewGone() error {
	return apierr.New(http.StatusGone, apierr.CodePreviewExpired, "The preview has expired or is unknown, so preview it again.")
}

func errPreviewKind(want PreviewKind) error {
	return apierr.New(http.StatusConflict, apierr.CodePreviewMismatch, "This preview is not a "+string(want)+" preview.")
}

// Get returns the preview with the ID, or a 410 preview_expired error when
// the store has none or it has expired. A stored preview is never changed,
// so callers read it without copying it.
func (s *PreviewStore) Get(id string) (*Preview, error) {
	now := s.now()

	s.mu.Lock()
	defer s.mu.Unlock()

	stored, ok := s.previews[id]
	if ok && now.Before(stored.expires) {
		if stored.kind != PreviewKindCertificate {
			return nil, errPreviewKind(PreviewKindCertificate)
		}

		return stored.preview, nil
	}

	delete(s.previews, id)

	return nil, errPreviewGone()
}

// PreviewRotation reads the current state of each selected instance and
// returns a stored preview that binds the effects a rotation would have.
// An instance that cannot take part is listed with Blocked set, and a read
// that fails for any instance fails the whole preview, because a preview
// that cannot list every effect must not be approved.
func (m *Manager) PreviewRotation(ctx context.Context, req PreviewRequest) (*Preview, error) {
	selections, err := normalizeSelections(req)
	if err != nil {
		return nil, err
	}

	err = m.certWritesAllowed(req.Mode)
	if err != nil {
		return nil, err
	}

	err = m.credhubReady()
	if err != nil {
		return nil, err
	}

	_, err = m.rotationDirector(ctx)
	if err != nil {
		return nil, err
	}

	d := m.certDeps()
	if d.Previews == nil {
		return nil, apierr.New(http.StatusInternalServerError, apierr.CodeInternal, "Blacksmith has no store for previews.")
	}

	preview := &Preview{
		ID:        previewIDPrefix + uuid.NewString(),
		Instances: make([]PreviewInstance, 0, len(selections)),
		Warnings:  []string{},
		mode:      req.Mode,
		converge:  req.Converge,
	}

	for _, sel := range selections {
		inst := m.previewInstance(ctx, req, sel, "")
		if inst.failure != nil {
			return nil, inst.failure
		}

		preview.Instances = append(preview.Instances, inst)
	}

	if req.Mode == RotationModePatch {
		preview.Warnings = append(preview.Warnings, patchWarning)
	}

	d.Previews.Put(preview, m.previewTTL())

	return preview, nil
}

// normalizeSelections checks the mode and the selections and returns the
// selections with each instance named once and each variable once.
func normalizeSelections(req PreviewRequest) ([]Selection, error) {
	if len(req.Selections) == 0 {
		return nil, apierr.New(http.StatusBadRequest, apierr.CodeNoSelections, "Select at least one instance to preview.")
	}

	switch req.Mode {
	case RotationModePatch:
		if req.Converge != ConvergePending && req.Converge != ConvergeImmediate {
			return nil, apierr.New(http.StatusBadRequest, apierr.CodeInvalidMode, "Patch mode needs a converge policy of pending or immediate.")
		}
	case RotationModeRedeploy, RotationModeConverge:
		if req.Converge != "" {
			return nil, apierr.New(http.StatusBadRequest, apierr.CodeInvalidMode, "A converge policy applies to patch mode only.")
		}
	default:
		return nil, apierr.New(http.StatusBadRequest, apierr.CodeInvalidMode, fmt.Sprintf("The mode %q is not redeploy, patch, or converge.", req.Mode))
	}

	order := make([]string, 0, len(req.Selections))
	merged := make(map[string][]string, len(req.Selections))
	seen := make(map[string]bool)

	for _, sel := range req.Selections {
		if sel.InstanceID == "" {
			return nil, apierr.New(http.StatusBadRequest, apierr.CodeNoSelections, "Every selection must name an instance.")
		}

		if _, known := merged[sel.InstanceID]; !known {
			order = append(order, sel.InstanceID)
			merged[sel.InstanceID] = []string{}
		}

		for _, name := range sel.Variables {
			key := sel.InstanceID + "\x00" + name
			if name == "" || seen[key] {
				continue
			}

			seen[key] = true
			merged[sel.InstanceID] = append(merged[sel.InstanceID], name)
		}
	}

	out := make([]Selection, 0, len(order))

	for _, id := range order {
		variables := merged[id]

		switch {
		case req.Mode == RotationModeConverge && len(variables) > 0:
			return nil, apierr.New(http.StatusBadRequest, apierr.CodeInvalidMode, "A converge regenerates nothing, so it selects instances without variables.")
		case req.Mode != RotationModeConverge && len(variables) == 0:
			return nil, apierr.New(http.StatusBadRequest, apierr.CodeNoSelections, fmt.Sprintf("Select at least one certificate on instance %s.", id))
		}

		out = append(out, Selection{InstanceID: id, Variables: variables})
	}

	return out, nil
}

// credhubReady refuses when the CredHub connection is known to be down.
func (m *Manager) credhubReady() error {
	d := m.certDeps()
	if d.CredHubState == nil {
		return nil
	}

	state, reason := d.CredHubState()

	switch state {
	case credhub.StateConfigured:
		return nil
	case credhub.StateNotConfigured:
		return apierr.New(http.StatusForbidden, apierr.CodeCredHubNotConfigured, "CredHub is not configured, so certificates can't be regenerated.")
	case credhub.StateUnreachable, credhub.StateRefused:
	}

	message := "CredHub is configured but is not answering now"
	if reason != "" {
		message += ": " + reason
	}

	return apierr.New(http.StatusServiceUnavailable, apierr.CodeCredHubUnavailable, message+".")
}

// directorCall runs one director call under its own deadline of
// certinventory.DirectorCallTimeout, the way the inventory scan bounds its
// director calls, and returns when the call or the deadline finishes. A
// call that ignores its context keeps running in the background, but the
// caller no longer waits for it, and its result is dropped.
func directorCall[T any](ctx context.Context, call func(context.Context) (T, error)) (T, error) {
	callCtx, cancel := context.WithTimeout(ctx, certinventory.DirectorCallTimeout)
	defer cancel()

	type result struct {
		value T
		err   error
	}

	done := make(chan result, 1)

	go func() {
		value, err := call(callCtx)
		done <- result{value, err}
	}()

	select {
	case r := <-done:
		return r.value, r.err
	case <-callCtx.Done():
		var zero T

		return zero, fmt.Errorf("the director did not answer in time: %w", callCtx.Err())
	}
}

// rotationDirector asks the director for its name and checks that its
// version accepts force_latest_variables.
func (m *Manager) rotationDirector(ctx context.Context) (string, error) {
	if m.director == nil {
		return "", apierr.New(http.StatusForbidden, apierr.CodeDirectorUnsupported, "Blacksmith has no BOSH director, so it can't rotate certificates.")
	}

	name, unsupported, err := m.latestVariablesDirector(ctx)
	if err != nil {
		return "", err
	}

	if unsupported != "" {
		return "", apierr.New(http.StatusForbidden, apierr.CodeDirectorUnsupported, "Certificates can't be rotated, because "+unsupported+".")
	}

	return name, nil
}

// latestVariablesDirector asks the director for its name and checks that
// its version accepts force_latest_variables. When it does not, it returns
// the reason to show operators and no error, so each caller words the
// refusal for what it was about to do. A director that can't be asked
// returns an API error.
func (m *Manager) latestVariablesDirector(ctx context.Context) (string, string, error) {
	if m.director == nil {
		return "", "Blacksmith has no BOSH director", nil
	}

	info, err := directorCall(ctx, m.director.GetInfo)
	if err != nil {
		return "", "", apierr.New(http.StatusInternalServerError, apierr.CodeInternal, fmt.Sprintf("The BOSH director did not report its version: %v", err))
	}

	if info == nil || info.Name == "" {
		return "", "", apierr.New(http.StatusInternalServerError, apierr.CodeInternal, "The BOSH director did not report its name.")
	}

	supported, reason := bosh.ForceLatestVariablesSupported(info.Version)
	if !supported {
		return "", reason, nil
	}

	return info.Name, "", nil
}

// previewTTL is how long a preview lives, from the certificate settings.
func (m *Manager) previewTTL() time.Duration {
	d := m.certDeps()
	if d.Settings == nil {
		return defaultPreviewTTL
	}

	minutes := d.Settings().PreviewTTLMinutes
	if minutes <= 0 {
		return defaultPreviewTTL
	}

	return time.Duration(minutes) * time.Minute
}

// previewInstance previews one instance. It treats the guard as free when
// the holder's task is holderTask, so a task can preview an instance it
// holds, and an empty holderTask matches no holder.
func (m *Manager) previewInstance(ctx context.Context, req PreviewRequest, sel Selection, holderTask string) PreviewInstance {
	inst := PreviewInstance{
		InstanceID: sel.InstanceID,
		Mode:       req.Mode,
		Selected:   []PreviewVariable{},
		Additional: []VersionPair{},
		Consumers:  []PreviewConsumer{},
	}

	directorName, err := m.rotationDirector(ctx)
	if err != nil {
		inst.failure = err

		return inst
	}

	d := m.certDeps()
	if d.ScanInstance == nil || d.CredHub == nil {
		inst.failure = apierr.New(http.StatusInternalServerError, apierr.CodeInternal, "Blacksmith is not set up to read certificates, so it can't preview a rotation.")

		return inst
	}

	scan, err := d.ScanInstance(ctx, sel.InstanceID)

	switch {
	case errors.Is(err, certinventory.ErrUnknownInstance):
		return inst.block(apierr.CodeInstanceNotFound, "Blacksmith does not know this instance, so it may have been deleted.")
	case err != nil:
		inst.failure = internalPreviewError(fmt.Sprintf("The instance %s could not be scanned: %v", sel.InstanceID, err))

		return inst
	case scan.Err != "":
		inst.failure = internalPreviewError(fmt.Sprintf("The instance %s could not be scanned: %s", sel.InstanceID, scan.Err))

		return inst
	}

	inst.describe(scan, req.Mode)

	blocked := m.busyBlock(scan.Ref, holderTask)
	if blocked != nil {
		inst.Blocked = blocked

		return inst
	}

	m.previewDeployment(ctx, &inst, req, sel, directorName, scan)

	return inst
}

// previewDeployment reads the instance's deployment and CredHub and fills
// in the effects, or blocks the instance.
func (m *Manager) previewDeployment(ctx context.Context, inst *PreviewInstance, req PreviewRequest, sel Selection, directorName string, scan certinventory.InstanceScan) {
	deployment := scan.Ref.Deployment

	detail, err := directorCall(ctx, func(c context.Context) (*bosh.DeploymentDetail, error) {
		return m.director.GetDeployment(c, deployment)
	})

	switch {
	case errors.Is(err, bosh.ErrDeploymentNotFound):
		*inst = inst.block(apierr.CodeInstanceNotFound, fmt.Sprintf("The director has no deployment %s, so the instance may have been deleted.", deployment))

		return
	case err != nil:
		inst.failure = internalPreviewError(fmt.Sprintf("The director did not return deployment %s: %v", deployment, err))

		return
	case detail == nil:
		inst.failure = internalPreviewError(fmt.Sprintf("The director returned no manifest for deployment %s.", deployment))

		return
	}

	variables, err := directorCall(ctx, func(c context.Context) ([]bosh.Variable, error) {
		return m.director.ListVariables(c, deployment)
	})
	if err != nil {
		inst.failure = internalPreviewError(fmt.Sprintf("The director did not list the variables of deployment %s: %v", deployment, err))

		return
	}

	running, err := directorCall(ctx, func(c context.Context) ([]bosh.Task, error) {
		return m.director.FindRunningTasksForDeployment(c, deployment)
	})
	if err != nil {
		inst.failure = internalPreviewError(fmt.Sprintf("The director did not list the tasks of deployment %s: %v", deployment, err))

		return
	}

	if len(running) > 0 {
		*inst = inst.block(apierr.CodeDeploymentLocked, fmt.Sprintf("The director is running task %d on deployment %s, so wait for it to finish.", running[0].ID, deployment))

		return
	}

	deployed := make(map[string]string, len(variables))
	for _, variable := range variables {
		deployed[variable.Name] = variable.ID
	}

	api := m.certDeps().CredHub
	inst.certificateIDs = make(map[string]string, len(sel.Variables))

	for _, name := range sel.Variables {
		check := m.checkSelected(ctx, api, purposePreview, directorName, scan, deployed, name)
		if check.failure != nil {
			inst.failure = check.failure

			return
		}

		if check.blocked != nil {
			*inst = inst.block(check.blocked.Code, check.blocked.Message)

			return
		}

		inst.Selected = append(inst.Selected, check.variable)
		inst.certificateIDs[name] = check.certificateID
	}

	latest, err := latestVersions(ctx, api, purposePreview, variables)
	if err != nil {
		inst.failure = err

		return
	}

	inst.bindEffects(req, sel, detail.Manifest, variables, latest, scan)

	if req.Mode == RotationModePatch && inst.Blocked == nil {
		m.previewPatch(ctx, inst, directorName, scan)
	}

	if req.Mode == RotationModeConverge && inst.Blocked == nil {
		m.previewConverge(ctx, inst, scan)
	}
}

// previewConverge blocks the converge of an instance that nothing waits
// on. A converge lists an instance with a divergence record, and one with
// a certificate whose latest version differs from its deployed one, which
// the inventory reports as deploy_pending. The variables the converge
// deploys are already bound as the additional ones. A record that can't
// be read fails the preview, since it can't tell whether the instance
// diverged.
func (m *Manager) previewConverge(ctx context.Context, inst *PreviewInstance, scan certinventory.InstanceScan) {
	certificates := make(map[string]bool, len(scan.Material.CredHub))
	for _, cert := range scan.Material.CredHub {
		certificates[cert.Name] = true
	}

	for _, pair := range inst.Additional {
		if certificates[pair.Name] {
			return
		}
	}

	if store := m.certDeps().Divergence; store != nil {
		_, found, err := store.Get(ctx, scan.Ref.ID)
		if err != nil {
			inst.failure = internalPreviewError(fmt.Sprintf("The divergence record of instance %s could not be read: %v", scan.Ref.ID, err))

			return
		}

		if found {
			return
		}
	}

	*inst = inst.block(apierr.CodeNotRotatable,
		"Nothing on this instance waits for a converge: it has no divergence record, and every certificate is deployed at its latest version.")
}

// bindEffects fills in the selected latest versions, the additional
// variables, and the effects a task binds.
func (inst *PreviewInstance) bindEffects(req PreviewRequest, sel Selection, manifest string, variables []bosh.Variable, latest map[string]string, scan certinventory.InstanceScan) {
	selected := make(map[string]bool, len(sel.Variables))
	pairs := make([]VersionPair, 0, len(inst.Selected))

	for i := range inst.Selected {
		variable := &inst.Selected[i]

		id, ok := latest[variable.Name]
		if !ok {
			*inst = inst.block(apierr.CodeNotRotatable, fmt.Sprintf("CredHub lists no version of %s.", variable.Name))

			return
		}

		variable.LatestVersionID = id
		selected[variable.Name] = true
		pairs = append(pairs, VersionPair{Name: variable.Name, DeployedVersionID: variable.DeployedVersionID, LatestVersionID: id})
	}

	inst.Additional = append(inst.Additional, differingPairs(variables, latest, selected)...)

	cids := make([]string, 0, len(scan.VMs))

	for _, vm := range scan.VMs {
		if vm.CID != "" {
			cids = append(cids, vm.CID)
		}
	}

	sort.Strings(cids)

	inst.ManifestDigest = manifestDigest(manifest)
	inst.approved = ApprovedEffects{
		ManifestDigest: inst.ManifestDigest,
		Selected:       pairs,
		Additional:     append([]VersionPair(nil), inst.Additional...),
		VMCIDs:         cids,
	}
	inst.Impact = impactOf(req.Mode, len(scan.VMs))
}

// describe copies what the scan says about the instance.
func (inst *PreviewInstance) describe(scan certinventory.InstanceScan, mode RotationMode) {
	ref := scan.Ref

	inst.InstanceName = ref.Name
	inst.Service = firstNonEmpty(ref.ServiceName, ref.ServiceID)
	inst.Plan = firstNonEmpty(ref.PlanName, ref.PlanID)
	inst.VMs = len(scan.VMs)
	inst.deployment = ref.Deployment
	inst.serviceID = ref.ServiceID
	inst.planID = ref.PlanID

	if mode == RotationModePatch {
		inst.gate = scan.HealthGate
		if inst.gate == "" && len(scan.VMs) == 1 {
			inst.gate = gateSingleVM
		}

		inst.Gate = inst.gate
	}
}

// block returns the instance blocked with the code and message, with no
// effects bound.
func (inst *PreviewInstance) block(code, message string) PreviewInstance {
	blocked := *inst
	blocked.Blocked = &Blocked{Code: code, Message: message}
	blocked.Selected = []PreviewVariable{}
	blocked.Additional = []VersionPair{}
	blocked.approved = ApprovedEffects{}
	blocked.certificateIDs = nil

	return blocked
}

// busyBlock blocks an instance that the guard holds, or that a task waits
// to reserve, for anything but holderTask.
func (m *Manager) busyBlock(ref certinventory.InstanceRef, holderTask string) *Blocked {
	holder, held := m.busyHolder(ref, holderTask)
	if !held {
		return nil
	}

	return &Blocked{Code: apierr.CodeInstanceBusy, Message: "The instance is busy with " + holder.Description + "."}
}

// busyHolder returns the holder of the instance, or false when it is free
// or held by holderTask.
func (m *Manager) busyHolder(ref certinventory.InstanceRef, holderTask string) (mutation.Holder, bool) {
	holder, held := m.Guard().Holder(ref.ID, ref.Deployment)
	if !held || (holderTask != "" && holder.TaskID == holderTask) {
		holder, held = m.WaitingFor(ref.ID, ref.Deployment)
		if held && holderTask != "" && holder.TaskID == holderTask {
			held = false
		}
	}

	return holder, held
}

// selectedCheck is the outcome of checking one selected variable.
type selectedCheck struct {
	variable      PreviewVariable
	certificateID string
	blocked       *Blocked
	failure       error
}

func blockedCheck(code, message string) selectedCheck {
	return selectedCheck{blocked: &Blocked{Code: code, Message: message}}
}

// checkSelected applies the rotatability rules of spec 7.9 to one selected
// variable. The switches and the director version were checked already.
// purpose names the step the CredHub read is for, in a failure's message.
func (m *Manager) checkSelected(ctx context.Context, api CredHubAPI, purpose, directorName string, scan certinventory.InstanceScan, deployed map[string]string, name string) selectedCheck {
	prefix := "/" + directorName + "/" + scan.Ref.Deployment + "/"
	if !strings.HasPrefix(name, prefix) || len(name) == len(prefix) {
		return blockedCheck(apierr.CodeNotRotatable, fmt.Sprintf("%s is not under %s, so it does not belong to this instance alone.", name, prefix))
	}

	deployedID, ok := deployed[name]
	if !ok {
		return blockedCheck(apierr.CodeUnknownVariable, fmt.Sprintf("%s is not a variable of deployment %s.", name, scan.Ref.Deployment))
	}

	meta, err := api.CertificateMetadata(ctx, name)

	switch {
	case errors.Is(err, credhub.ErrNotFound):
		return blockedCheck(apierr.CodeNotRotatable, name+" is not a certificate in CredHub.")
	case err != nil:
		return selectedCheck{failure: credhubUnavailable(purpose, err)}
	case meta == nil:
		return blockedCheck(apierr.CodeNotRotatable, name+" is not a certificate in CredHub.")
	}

	version, found := deployedVersion(meta, deployedID)
	if !found {
		return blockedCheck(apierr.CodeNotRotatable, fmt.Sprintf("CredHub does not list the deployed version of %s.", name))
	}

	material := scannedCertificate(scan, name)

	switch {
	case version.CertificateAuthority || (material != nil && material.Deployed != nil && material.Deployed.IsCA):
		return blockedCheck(apierr.CodeNotRotatable, name+" is a CA, and only a leaf certificate can be rotated.")
	case material == nil || material.Deployed == nil:
		return blockedCheck(apierr.CodeNotRotatable, fmt.Sprintf("The deployed certificate %s could not be read, so Blacksmith can't tell that it is a leaf.", name))
	}

	signer := material.Versions.SigningCA
	if signer != "" && signer != name {
		ca := scan.Material.SigningCAs[signer]
		if ca != nil && !signedBy(material.Deployed, ca) {
			return blockedCheck(apierr.CodeSigningCAChanged, fmt.Sprintf("The CA %s would sign a regenerated %s, but it is not the CA that issued the deployed one.", signer, name))
		}
	}

	return selectedCheck{
		variable: PreviewVariable{
			Name:              name,
			DeployedVersionID: deployedID,
			DaysLeft:          m.daysLeft(version.ExpiryDate),
		},
		certificateID: meta.ID,
	}
}

// signedBy reports whether ca's key signed the certificate. It skips the
// CA-flag checks that CheckSignatureFrom makes, since the rule asks only
// who signed the leaf.
func signedBy(cert, ca *x509.Certificate) bool {
	return ca.CheckSignature(cert.SignatureAlgorithm, cert.RawTBSCertificate, cert.Signature) == nil
}

func deployedVersion(meta *credhub.CertificateMeta, versionID string) (credhub.CertificateVersionMeta, bool) {
	for _, version := range meta.Versions {
		if version.ID == versionID {
			return version, true
		}
	}

	return credhub.CertificateVersionMeta{}, false
}

func scannedCertificate(scan certinventory.InstanceScan, name string) *certinventory.CredHubCert {
	for i := range scan.Material.CredHub {
		if scan.Material.CredHub[i].Name == name {
			return &scan.Material.CredHub[i]
		}
	}

	return nil
}

// daysLeft is the whole number of days until expiry, negative once it has
// passed.
func (m *Manager) daysLeft(expiry time.Time) int {
	now := time.Now
	if d := m.certDeps(); d.Now != nil {
		now = d.Now
	}

	return int(math.Floor(expiry.Sub(now()).Hours() / hoursPerDay))
}

// latestVersions reads the newest CredHub version of every variable, four
// at a time. A variable CredHub does not know is left out, since a deploy
// generates nothing for it, and any other failure fails the whole read
// with a message that names the purpose of the read.
func latestVersions(ctx context.Context, api CredHubAPI, purpose string, variables []bosh.Variable) (map[string]string, error) {
	readCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	names := make(chan string)
	latest := make(map[string]string, len(variables))

	var (
		mu       sync.Mutex
		firstErr error
		wg       sync.WaitGroup
	)

	for range min(latestReadWorkers, len(variables)) {
		wg.Add(1)

		go func() {
			defer wg.Done()

			for name := range names {
				versions, err := api.CredentialVersions(readCtx, name, 1)

				mu.Lock()

				switch {
				case errors.Is(err, credhub.ErrNotFound):
				case err != nil:
					if firstErr == nil {
						firstErr = err

						cancel()
					}
				case len(versions) > 0:
					latest[name] = versions[0].ID
				}

				mu.Unlock()
			}
		}()
	}

feed:
	for _, variable := range variables {
		select {
		case names <- variable.Name:
		case <-readCtx.Done():
			break feed
		}
	}

	close(names)
	wg.Wait()

	if firstErr != nil {
		return nil, credhubUnavailable(purpose, firstErr)
	}

	err := ctx.Err()
	if err != nil {
		return nil, fmt.Errorf("reading the latest CredHub versions: %w", err)
	}

	return latest, nil
}

// Purposes name what a CredHub read was for, so a credhub_unavailable
// message tells the operator which step could not go on.
const (
	purposePreview   = "the preview"
	purposePreflight = "the preflight check of the selected certificates"
	purposeRecheck   = "the re-check of the approved effects"
)

// credhubUnavailable reports a CredHub read that failed, naming the
// purpose the read was for. The client's errors carry no credential value.
func credhubUnavailable(purpose string, err error) error {
	return apierr.New(http.StatusServiceUnavailable, apierr.CodeCredHubUnavailable,
		fmt.Sprintf("CredHub did not answer a read that %s needs: %v", purpose, err))
}

func internalPreviewError(message string) error {
	return apierr.New(http.StatusInternalServerError, apierr.CodeInternal, message)
}

// manifestDigest is the SHA-256 of the manifest text, as the preview binds
// it.
func manifestDigest(manifest string) string {
	sum := sha256.Sum256([]byte(manifest))

	return "sha256:" + hex.EncodeToString(sum[:])
}

// impactOf says in one sentence what the operator should expect.
func impactOf(mode RotationMode, vms int) string {
	count := fmt.Sprintf("%d VMs", vms)
	if vms == 1 {
		count = "1 VM"
	}

	switch mode {
	case RotationModePatch:
		return fmt.Sprintf("Blacksmith replaces the certificate files on each of the instance's %s in turn and restarts the jobs that use them, without a BOSH deploy.", count)
	case RotationModeConverge:
		return fmt.Sprintf("BOSH deploys the unchanged manifest with the latest variables and rolls the instance's %s through its update block, with canaries and drains.", count)
	case RotationModeRedeploy:
	}

	return fmt.Sprintf("BOSH rolls the instance's %s through its update block, with canaries and drains.", count)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}

	return ""
}
