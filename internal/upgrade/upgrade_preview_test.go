//nolint:testpackage // the tests read the previews' bound effects and the store's entries
package upgrade

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"blacksmith/internal/apierr"
	"blacksmith/internal/bosh"
	"blacksmith/internal/certinventory"
	"blacksmith/internal/credhub"
	"blacksmith/internal/interfaces"
	"blacksmith/pkg/logger"
)

const (
	divergedInstance2   = "instance-2"
	divergedDeployment2 = "redis-instance-2"
	divergedPatchTask   = "patch-task-1"
	divergedStemcellOS  = "ubuntu-noble"
	divergedStemcell    = "1.2"
	divergedCAVersion   = "ca1"
)

func divergedLeaf(deployment string) string { return "/ws-bosh/" + deployment + "/redis_crt" }

func divergedCA(deployment string) string { return "/ws-bosh/" + deployment + "/services_ca" }

func divergedPassword(deployment string) string { return "/ws-bosh/" + deployment + "/redis_password" }

// divergedCredHub answers the latest version of each variable from a map,
// and counts every call it gets.
type divergedCredHub struct {
	mu     sync.Mutex
	latest map[string]string
	calls  atomic.Int64
}

func (c *divergedCredHub) CertificateMetadata(context.Context, string) (*credhub.CertificateMeta, error) {
	c.calls.Add(1)

	return nil, credhub.ErrNotFound
}

func (c *divergedCredHub) CredentialVersions(_ context.Context, name string, _ int) ([]credhub.CredentialVersion, error) {
	c.calls.Add(1)

	c.mu.Lock()
	defer c.mu.Unlock()

	id, ok := c.latest[name]
	if !ok {
		return nil, credhub.ErrNotFound
	}

	return []credhub.CredentialVersion{{ID: id}}, nil
}

func (c *divergedCredHub) CertificateVersion(context.Context, string) (*credhub.CertificatePublic, error) {
	c.calls.Add(1)

	return nil, credhub.ErrNotFound
}

func (c *divergedCredHub) Regenerate(context.Context, string) (*credhub.RegenerateResult, error) {
	c.calls.Add(1)

	return nil, credhub.ErrNotFound
}

func (c *divergedCredHub) CertificateKeyPair(context.Context, string) (*credhub.KeyPair, error) {
	c.calls.Add(1)

	return nil, credhub.ErrNotFound
}

func (c *divergedCredHub) setLatest(name, id string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.latest[name] = id
}

// divergedDirector is a waitDirector that lists each deployment's
// variables and counts the deploys it is sent. When getGate is set, the
// first GetDeployment closes getStarted and then waits for getGate.
type divergedDirector struct {
	*waitDirector

	getGate    chan struct{}
	getStarted chan struct{}
	getOnce    sync.Once

	varMu     sync.Mutex
	variables map[string][]bosh.Variable
	version   string
	releases  []bosh.Release

	deploys   atomic.Int64
	infoCalls atomic.Int64
}

func newDivergedDirector() *divergedDirector {
	return &divergedDirector{waitDirector: newWaitDirector(), variables: make(map[string][]bosh.Variable), version: "282.0.0"}
}

func (d *divergedDirector) GetInfo(context.Context) (*bosh.Info, error) {
	d.infoCalls.Add(1)

	d.varMu.Lock()
	defer d.varMu.Unlock()

	return &bosh.Info{Name: "ws-bosh", Version: d.version}, nil
}

func (d *divergedDirector) GetReleases(context.Context) ([]bosh.Release, error) {
	d.varMu.Lock()
	defer d.varMu.Unlock()

	return slices.Clone(d.releases), nil
}

func (d *divergedDirector) GetDeployment(ctx context.Context, name string) (*bosh.DeploymentDetail, error) {
	if d.getGate != nil {
		d.getOnce.Do(func() {
			close(d.getStarted)
			<-d.getGate
		})
	}

	return d.waitDirector.GetDeployment(ctx, name)
}

func (d *divergedDirector) ListVariables(_ context.Context, deployment string) ([]bosh.Variable, error) {
	d.varMu.Lock()
	defer d.varMu.Unlock()

	return slices.Clone(d.variables[deployment]), nil
}

func (d *divergedDirector) UpdateDeployment(ctx context.Context, name, manifest string) (*bosh.Task, error) {
	d.deploys.Add(1)

	return d.waitDirector.UpdateDeployment(ctx, name, manifest)
}

func (d *divergedDirector) UpdateDeploymentWithOptions(ctx context.Context, name, manifest string, opts bosh.DeployOptions) (*bosh.Task, error) {
	d.deploys.Add(1)

	return d.waitDirector.UpdateDeploymentWithOptions(ctx, name, manifest, opts)
}

func (d *divergedDirector) setVersion(version string) {
	d.varMu.Lock()
	defer d.varMu.Unlock()

	d.version = version
}

func (d *divergedDirector) options() []bosh.DeployOptions {
	d.mu.Lock()
	defer d.mu.Unlock()

	return slices.Clone(d.deployOptions)
}

// divergedEnv is a started manager over two instances whose deployments
// each hold a leaf at v1, a CA at ca1, and a password at p1. CredHub's
// latest versions start equal to the deployed ones.
type divergedEnv struct {
	manager  *Manager
	director *divergedDirector
	vault    *waitVault
	store    *certinventory.DivergenceStore
	credhub  *divergedCredHub

	// stop ends the manager once, and the test's cleanup calls it too.
	stop func()

	clockMu sync.Mutex
	now     time.Time
}

func newDivergedEnv(t *testing.T, withDivergence bool) *divergedEnv {
	t.Helper()

	return newDivergedEnvWith(t, withDivergence, nil)
}

// newDivergedEnvWith builds the environment with the divergence store
// reading through storeVault, when it is set, in place of the vault.
func newDivergedEnvWith(t *testing.T, withDivergence bool, storeVault func(*waitVault) interfaces.Vault) *divergedEnv {
	t.Helper()

	env := &divergedEnv{
		director: newDivergedDirector(),
		vault:    newWaitVault(),
		credhub:  &divergedCredHub{latest: make(map[string]string)},
		now:      validateNow,
	}

	for _, deployment := range []string{waitDeployment, divergedDeployment2} {
		env.director.variables[deployment] = []bosh.Variable{
			{ID: "v1", Name: divergedLeaf(deployment)},
			{ID: divergedCAVersion, Name: divergedCA(deployment)},
			{ID: "p1", Name: divergedPassword(deployment)},
		}
		env.credhub.setLatest(divergedLeaf(deployment), "v1")
		env.credhub.setLatest(divergedCA(deployment), divergedCAVersion)
		env.credhub.setLatest(divergedPassword(deployment), "p1")
	}

	deps := Deps{
		CredHub:  env.credhub,
		Settings: certinventory.DefaultSettings,
		Now:      env.clock,
	}

	if withDivergence {
		var backing interfaces.Vault = env.vault
		if storeVault != nil {
			backing = storeVault(env.vault)
		}

		env.store = certinventory.NewDivergenceStore(backing, nil)
		deps.Divergence = env.store
	}

	env.manager = NewManager(&logger.NoOpLogger{}, env.director, env.vault)

	err := env.manager.Configure(deps)
	if err != nil {
		t.Fatalf("Configure: %v", err)
	}

	var once sync.Once

	env.stop = func() { once.Do(env.manager.Stop) }
	t.Cleanup(env.stop)
	env.manager.Start()
	waitForLoad(t, env.manager)

	return env
}

func (e *divergedEnv) clock() time.Time {
	e.clockMu.Lock()
	defer e.clockMu.Unlock()

	return e.now
}

func (e *divergedEnv) advance(d time.Duration) {
	e.clockMu.Lock()
	defer e.clockMu.Unlock()

	e.now = e.now.Add(d)
}

// diverge records that a patch wrote v2 of instance-1's leaf while BOSH
// still deploys v1, and makes v2 the leaf's latest CredHub version.
func (e *divergedEnv) diverge(t *testing.T) {
	t.Helper()

	e.recordDivergence(t, waitInstanceID, waitDeployment)
	e.credhub.setLatest(divergedLeaf(waitDeployment), "v2")
}

// recordDivergence records that a patch wrote v2 of the deployment's leaf
// while BOSH still deploys v1.
func (e *divergedEnv) recordDivergence(t *testing.T, instanceID, deployment string) {
	t.Helper()

	err := e.store.Update(context.Background(), instanceID, func(*certinventory.Divergence) (*certinventory.Divergence, error) {
		return &certinventory.Divergence{InstanceID: instanceID, Variables: []certinventory.DivergentVariable{{
			Name: divergedLeaf(deployment), BOSHVersionID: "v1", AppliedVersionID: "v2",
			TaskID: divergedPatchTask, Since: validateNow, State: certinventory.DivergenceUnconverged,
			VMs: map[string]certinventory.DivergentVM{"vm-a": {State: certinventory.DivergentVMApplied, Group: "redis"}},
		}}}, nil
	})
	if err != nil {
		t.Fatalf("recording the divergence of %s: %v", instanceID, err)
	}
}

func upgradeRequest(instanceIDs ...string) CreateTaskRequest {
	return CreateTaskRequest{
		InstanceIDs:    instanceIDs,
		TargetStemcell: StemcellTarget{OS: divergedStemcellOS, Version: divergedStemcell},
	}
}

func (e *divergedEnv) preview(t *testing.T, req CreateTaskRequest) *UpgradePreview {
	t.Helper()

	preview, err := e.manager.PreviewUpgrade(context.Background(), req)
	if err != nil {
		t.Fatalf("PreviewUpgrade: %v", err)
	}

	return preview
}

// expectNothingCreated fails the test unless no upgrade exists, none was
// saved, no instance is reserved, and no deploy was sent.
func (e *divergedEnv) expectNothingCreated(t *testing.T) {
	t.Helper()

	if tasks := e.manager.ListTasks(); len(tasks) != 0 {
		t.Fatalf("expected no upgrade, got %+v", tasks)
	}

	for _, path := range e.vault.storedPaths() {
		if strings.HasPrefix(path, upgradeTasksVaultPath) {
			t.Fatalf("expected no upgrade to be saved, found %s", path)
		}
	}

	if held := e.manager.Guard().Snapshot(); len(held) != 0 {
		t.Fatalf("expected no reservation, got %v", held)
	}

	if deploys := e.director.deploys.Load(); deploys != 0 {
		t.Fatalf("expected no deploy, got %d", deploys)
	}
}

func TestUpgradePreviewListsDivergedEffects(t *testing.T) {
	t.Parallel()

	env := newDivergedEnv(t, true)
	env.diverge(t)
	// A CA rotated in CredHub but not deployed is applied by the same
	// deploy, so it is listed too.
	env.credhub.setLatest(divergedCA(waitDeployment), "ca2")

	preview := env.preview(t, upgradeRequest(waitInstanceID, divergedInstance2))

	if !strings.HasPrefix(preview.ID, "up-") || !preview.ExpiresAt.Equal(validateNow.Add(15*time.Minute)) {
		t.Fatalf("expected an up- ID expiring in 15 minutes, got %q at %s", preview.ID, preview.ExpiresAt)
	}

	if !slices.Equal(preview.InstanceIDs, []string{waitInstanceID, divergedInstance2}) {
		t.Fatalf("expected both instances, got %v", preview.InstanceIDs)
	}

	if len(preview.Diverged) != 1 {
		t.Fatalf("expected one diverged instance, got %+v", preview.Diverged)
	}

	diverged := preview.Diverged[0]
	if diverged.InstanceID != waitInstanceID || diverged.Divergence == nil ||
		diverged.Divergence.State != certinventory.DivergenceUnconverged || diverged.Divergence.TaskID != divergedPatchTask {
		t.Fatalf("unexpected diverged instance %+v (divergence %+v)", diverged, diverged.Divergence)
	}

	if !slices.Equal(diverged.Variables, []string{divergedLeaf(waitDeployment)}) {
		t.Fatalf("expected the patched leaf, got %v", diverged.Variables)
	}

	want := []VersionPair{
		{Name: divergedLeaf(waitDeployment), DeployedVersionID: "v1", LatestVersionID: "v2"},
		{Name: divergedCA(waitDeployment), DeployedVersionID: divergedCAVersion, LatestVersionID: "ca2"},
	}
	slices.SortFunc(want, func(a, b VersionPair) int { return strings.Compare(a.Name, b.Name) })

	if !reflect.DeepEqual(diverged.Additional, want) {
		t.Fatalf("expected additional %+v, got %+v", want, diverged.Additional)
	}

	stored, err := env.manager.Dependencies().UpgradePreviews.Get(preview.ID)
	if err != nil || stored != preview {
		t.Fatalf("expected the preview to be stored, got %v and %v", stored, err)
	}

	if !reflect.DeepEqual(stored.boundAdditional, map[string][]VersionPair{waitInstanceID: want}) {
		t.Fatalf("expected the bound effects of the diverged instance only, got %+v", stored.boundAdditional)
	}

	if !slices.Equal(stored.Targets, []string{"stemcell " + divergedStemcellOS + "/" + divergedStemcell}) {
		t.Fatalf("expected the stemcell target, got %v", stored.Targets)
	}
}

func TestUpgradePreviewCleanInstancesNoCredHub(t *testing.T) {
	t.Parallel()

	env := newDivergedEnv(t, true)

	preview := env.preview(t, upgradeRequest(waitInstanceID, divergedInstance2))

	if preview.Diverged == nil || len(preview.Diverged) != 0 {
		t.Fatalf("expected an empty diverged list, got %#v", preview.Diverged)
	}

	if calls := env.credhub.calls.Load(); calls != 0 {
		t.Fatalf("expected no CredHub request, got %d", calls)
	}

	_, err := env.manager.Dependencies().UpgradePreviews.Get(preview.ID)
	if err != nil {
		t.Fatalf("expected the preview to be stored: %v", err)
	}

	raw, err := json.Marshal(preview)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(raw), `"diverged":[]`) {
		t.Fatalf("expected an empty diverged list in %s", raw)
	}
}

func TestUpgradePreviewValidatesLikeCreate(t *testing.T) {
	t.Parallel()

	env := newDivergedEnv(t, true)

	for name, test := range map[string]struct {
		req  CreateTaskRequest
		want error
	}{
		"no instance":           {req: upgradeRequest(), want: ErrNoInstances},
		"no target":             {req: CreateTaskRequest{InstanceIDs: []string{waitInstanceID}}, want: ErrNoUpgradeTarget},
		"a certificate task":    {req: CreateTaskRequest{Kind: TaskKindCertificates, InstanceIDs: []string{waitInstanceID}}, want: ErrCertificateTaskKind},
		"a half-set stemcell":   {req: CreateTaskRequest{InstanceIDs: []string{waitInstanceID}, TargetStemcell: StemcellTarget{OS: "x"}}, want: ErrInvalidStemcell},
		"a release named twice": {req: CreateTaskRequest{InstanceIDs: []string{waitInstanceID}, TargetReleases: []ReleaseTarget{{Name: "a", Version: "1"}, {Name: "a", Version: "2"}}}, want: ErrInvalidRelease},
	} {
		_, err := env.manager.PreviewUpgrade(context.Background(), test.req)
		if !errors.Is(err, test.want) {
			t.Errorf("%s: expected %v, got %v", name, test.want, err)
		}
	}
}

//nolint:paralleltest // a created upgrade runs spruce, whose global state races across tests
func TestCreateUpgradeRequiresPreviewWhenDiverged(t *testing.T) {
	env := newDivergedEnv(t, true)
	env.diverge(t)

	_, err := env.manager.CreateTask(context.Background(), upgradeRequest(divergedInstance2, waitInstanceID))
	expectAPIError(t, err, http.StatusConflict, apierr.CodeUpgradePreviewRequired)

	env.expectNothingCreated(t)
}

//nolint:paralleltest // a created upgrade runs spruce, whose global state races across tests
func TestCreateUpgradeRejectsMismatchedPreview(t *testing.T) {
	env := newDivergedEnv(t, true)
	env.director.releases = []bosh.Release{{Name: "redis-forge", ReleaseVersions: []bosh.ReleaseVersion{{Version: "9.9.8"}, {Version: "9.9.9"}}}}

	// A preview taken before the instance diverged bound nothing for it.
	before := env.preview(t, upgradeRequest(waitInstanceID))

	env.diverge(t)

	preview := env.preview(t, upgradeRequest(waitInstanceID))

	otherStemcell := upgradeRequest(waitInstanceID)
	otherStemcell.TargetStemcell.Version = "1.3"

	withRelease := upgradeRequest(waitInstanceID)
	withRelease.TargetReleases = []ReleaseTarget{{Name: "redis-forge", Version: "9.9.9"}}
	releasePreview := env.preview(t, withRelease)

	otherRelease := upgradeRequest(waitInstanceID)
	otherRelease.TargetReleases = []ReleaseTarget{{Name: "redis-forge", Version: "9.9.8"}}

	for name, test := range map[string]struct {
		req CreateTaskRequest
		id  string
	}{
		"another instance set":            {req: upgradeRequest(waitInstanceID, divergedInstance2), id: preview.ID},
		"another stemcell":                {req: otherStemcell, id: preview.ID},
		"a preview from before the patch": {req: upgradeRequest(waitInstanceID), id: before.ID},
		"another release version":         {req: otherRelease, id: releasePreview.ID},
		"no release target":               {req: upgradeRequest(waitInstanceID), id: releasePreview.ID},
	} {
		test.req.UpgradePreviewID = test.id

		_, err := env.manager.CreateTask(context.Background(), test.req)

		var apiErr *apierr.Error
		if !errors.As(err, &apiErr) || apiErr.Status != http.StatusConflict || apiErr.Code != apierr.CodeUpgradePreviewRequired {
			t.Errorf("%s: expected 409 upgrade_preview_required, got %v", name, err)
		}

		env.expectNothingCreated(t)
	}
}

func TestUpgradePreviewStoreExpiry(t *testing.T) {
	t.Parallel()

	now := validateNow
	store := NewUpgradePreviewStore(func() time.Time { return now })

	first := &UpgradePreview{ID: "up-first"}
	store.Put(first, 15*time.Minute)
	store.Put(&UpgradePreview{ID: "up-never-read"}, 15*time.Minute)

	if !first.ExpiresAt.Equal(validateNow.Add(15 * time.Minute)) {
		t.Fatalf("expected the expiry 15 minutes on, got %s", first.ExpiresAt)
	}

	got, err := store.Get("up-first")
	if err != nil || got != first {
		t.Fatalf("expected the stored preview, got %v and %v", got, err)
	}

	_, err = store.Get("up-unknown")
	expectAPIError(t, err, http.StatusGone, apierr.CodePreviewExpired)

	now = validateNow.Add(16 * time.Minute)

	_, err = store.Get("up-first")
	expectAPIError(t, err, http.StatusGone, apierr.CodePreviewExpired)

	store.Put(&UpgradePreview{ID: "up-second"}, 15*time.Minute)

	store.mu.Lock()
	count := len(store.previews)
	store.mu.Unlock()

	if count != 1 {
		t.Fatalf("expected Put to sweep the expired preview, got %d stored", count)
	}
}

//nolint:paralleltest // a created upgrade runs spruce, whose global state races across tests
func TestCreateUpgradeRejectsExpiredPreview(t *testing.T) {
	env := newDivergedEnv(t, true)
	env.diverge(t)

	preview := env.preview(t, upgradeRequest(waitInstanceID))

	env.advance(16 * time.Minute)

	req := upgradeRequest(waitInstanceID)
	req.UpgradePreviewID = preview.ID

	_, err := env.manager.CreateTask(context.Background(), req)
	expectAPIError(t, err, http.StatusGone, apierr.CodePreviewExpired)

	env.expectNothingCreated(t)
}

//nolint:paralleltest // a created upgrade runs spruce, whose global state races across tests
func TestCreateUpgradeRejectsRotationPreviewID(t *testing.T) {
	env := newDivergedEnv(t, true)
	env.diverge(t)

	rotation := &Preview{ID: "pv-live-rotation"}
	env.manager.Dependencies().Previews.Put(rotation, 15*time.Minute)

	_, err := env.manager.Dependencies().Previews.Get(rotation.ID)
	if err != nil {
		t.Fatalf("the rotation preview is not live: %v", err)
	}

	req := upgradeRequest(waitInstanceID)
	req.UpgradePreviewID = rotation.ID

	_, err = env.manager.CreateTask(context.Background(), req)
	expectAPIError(t, err, http.StatusGone, apierr.CodePreviewExpired)

	env.expectNothingCreated(t)
}

// normalizedSave decodes a saved upgrade and blanks the fields that differ
// between two runs of the same upgrade: its ID, its times, and the deploy's
// context ID.
func normalizedSave(t *testing.T, raw []byte) map[string]any {
	t.Helper()

	var task map[string]any

	err := json.Unmarshal(raw, &task)
	if err != nil {
		t.Fatalf("decoding a saved upgrade: %v", err)
	}

	for _, key := range []string{"id", "created_at", "started_at", "completed_at"} {
		if _, ok := task[key]; ok {
			task[key] = "-"
		}
	}

	instances, _ := task["instances"].([]any)
	for _, item := range instances {
		instance, _ := item.(map[string]any)
		for _, key := range []string{"started_at", "completed_at", "context_id"} {
			if _, ok := instance[key]; ok {
				instance[key] = "-"
			}
		}
	}

	return task
}

// runCleanUpgrade creates and finishes an upgrade of the clean instance,
// and returns every save of it in order.
func runCleanUpgrade(t *testing.T, withDivergence bool) ([]map[string]any, *divergedEnv) {
	t.Helper()

	env := newDivergedEnv(t, withDivergence)

	var (
		mu    sync.Mutex
		saves [][]byte
	)

	env.vault.onPut = func(path string, raw []byte) {
		if strings.HasPrefix(path, upgradeTasksVaultPath+"/") {
			mu.Lock()
			defer mu.Unlock()

			saves = append(saves, raw)
		}
	}

	task, err := env.manager.CreateTask(context.Background(), upgradeRequest(waitInstanceID))
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	if task.Instances[0].ApprovedAdditional != nil {
		t.Fatalf("expected no approved effects on a clean instance, got %+v", task.Instances[0].ApprovedAdditional)
	}

	select {
	case <-env.director.waitStarted:
	case <-time.After(waitTimeout):
		t.Fatal("the upgrade never waited on its deploy")
	}

	env.director.finish <- &bosh.Task{ID: waitDeployTaskID, State: boshTaskDone}

	eventuallySaved(t, env.vault, task.ID, func(saved UpgradeTask) bool { return saved.Status == TaskStatusCompleted })
	env.stop()

	mu.Lock()
	defer mu.Unlock()

	out := make([]map[string]any, 0, len(saves))
	for _, raw := range saves {
		if strings.Contains(string(raw), "approved_additional") {
			t.Fatalf("a clean upgrade saved approved effects: %s", raw)
		}

		out = append(out, normalizedSave(t, raw))
	}

	return out, env
}

//nolint:paralleltest // the upgrade runs spruce, whose global state races across tests
func TestCreateUpgradeCleanUnchanged(t *testing.T) {
	today, _ := runCleanUpgrade(t, false)
	now, env := runCleanUpgrade(t, true)

	if !reflect.DeepEqual(today, now) {
		t.Fatalf("a clean upgrade saved something else than today:\ntoday %v\nnow   %v", today, now)
	}

	if options := env.director.options(); len(options) != 0 {
		t.Fatalf("expected a plain deploy, got options %+v", options)
	}

	if deploys := env.director.deploys.Load(); deploys != 1 {
		t.Fatalf("expected one deploy, got %d", deploys)
	}

	if calls := env.credhub.calls.Load(); calls != 0 {
		t.Fatalf("expected no CredHub request, got %d", calls)
	}

	if calls := env.director.infoCalls.Load(); calls != 0 {
		t.Fatalf("expected no director version check, got %d", calls)
	}
}

//nolint:paralleltest // the upgrade runs spruce, whose global state races across tests
func TestUpgradeOfDivergedInstanceForcesLatest(t *testing.T) {
	env := newDivergedEnv(t, true)
	env.diverge(t)

	preview := env.preview(t, upgradeRequest(waitInstanceID))

	req := upgradeRequest(waitInstanceID)
	req.UpgradePreviewID = preview.ID

	task, err := env.manager.CreateTask(context.Background(), req)
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	want := []VersionPair{{Name: divergedLeaf(waitDeployment), DeployedVersionID: "v1", LatestVersionID: "v2"}}
	if !reflect.DeepEqual(task.Instances[0].ApprovedAdditional, want) {
		t.Fatalf("expected the approved effects %+v, got %+v", want, task.Instances[0].ApprovedAdditional)
	}

	select {
	case <-env.director.waitStarted:
	case <-time.After(waitTimeout):
		t.Fatal("the upgrade never waited on its deploy")
	}

	if options := env.director.options(); !slices.Equal(options, []bosh.DeployOptions{{ForceLatestVariables: true}}) {
		t.Fatalf("expected one deploy with the latest variables, got %+v", options)
	}

	env.director.finish <- &bosh.Task{ID: waitDeployTaskID, State: boshTaskDone}

	saved := eventuallySaved(t, env.vault, task.ID, func(saved UpgradeTask) bool { return saved.Status == TaskStatusCompleted })
	if !reflect.DeepEqual(saved.Instances[0].ApprovedAdditional, want) {
		t.Fatalf("expected the saved approved effects %+v, got %+v", want, saved.Instances[0].ApprovedAdditional)
	}

	// The upgrade never writes the divergence record; reconciliation
	// clears it.
	_, found, err := env.store.Get(context.Background(), waitInstanceID)
	if err != nil || !found {
		t.Fatalf("expected the divergence record to stay, got found %t and %v", found, err)
	}
}

//nolint:paralleltest // the upgrade runs spruce, whose global state races across tests
func TestUpgradeOfDivergedInstanceWithNothingElseKeepsItsApproval(t *testing.T) {
	env := newDivergedEnv(t, true)
	env.diverge(t)
	// The patched version is deployed already, so nothing else differs.
	env.credhub.setLatest(divergedLeaf(waitDeployment), "v1")

	preview := env.preview(t, upgradeRequest(waitInstanceID))
	if len(preview.Diverged) != 1 || preview.Diverged[0].Additional == nil || len(preview.Diverged[0].Additional) != 0 {
		t.Fatalf("expected one diverged instance with nothing additional, got %+v", preview.Diverged)
	}

	req := upgradeRequest(waitInstanceID)
	req.UpgradePreviewID = preview.ID

	task, err := env.manager.CreateTask(context.Background(), req)
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	select {
	case <-env.director.waitStarted:
	case <-time.After(waitTimeout):
		t.Fatal("the upgrade never waited on its deploy")
	}

	saved := env.vault.savedTask(t, task.ID)
	if saved.Instances[0].ApprovedAdditional == nil {
		t.Fatal("an empty approval was lost in the save")
	}

	if options := env.director.options(); !slices.Equal(options, []bosh.DeployOptions{{ForceLatestVariables: true}}) {
		t.Fatalf("expected one deploy with the latest variables, got %+v", options)
	}

	env.director.finish <- &bosh.Task{ID: waitDeployTaskID, State: boshTaskDone}

	eventuallySaved(t, env.vault, task.ID, func(saved UpgradeTask) bool { return saved.Status == TaskStatusCompleted })
}

// expectStoppedForApproval waits until the instance failed with the
// approval message, and checks that nothing was deployed and the
// instance is free.
func expectStoppedForApproval(t *testing.T, env *divergedEnv, taskID string) {
	t.Helper()

	saved := eventuallySaved(t, env.vault, taskID, func(saved UpgradeTask) bool {
		return saved.Instances[0].Status == InstanceStatusFailed
	})

	if message := saved.Instances[0].Error; message != "This instance's additional changes differ from what was approved. Preview the upgrade again." {
		t.Fatalf("unexpected failure %q", message)
	}

	if deploys := env.director.deploys.Load(); deploys != 0 {
		t.Fatalf("expected no deploy, got %d", deploys)
	}

	if saved.Instances[0].ContextID != "" {
		t.Fatalf("expected no context ID, got %q", saved.Instances[0].ContextID)
	}

	deadline := time.Now().Add(waitTimeout)
	for len(env.manager.Guard().Snapshot()) != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("the failed instance stayed reserved: %v", env.manager.Guard().Snapshot())
		}

		time.Sleep(5 * time.Millisecond)
	}
}

//nolint:paralleltest // the upgrade runs spruce, whose global state races across tests
func TestUpgradeStopsWhenEffectsGrew(t *testing.T) {
	env := newDivergedEnv(t, true)
	env.diverge(t)

	preview := env.preview(t, upgradeRequest(waitInstanceID))

	req := upgradeRequest(waitInstanceID)
	req.UpgradePreviewID = preview.ID

	var task *UpgradeTask

	release := env.gateDeploymentRead(t, func() {
		var err error

		task, err = env.manager.CreateTask(context.Background(), req)
		if err != nil {
			t.Fatalf("CreateTask: %v", err)
		}
	})

	// After the create, the password gains a version the preview did not
	// list.
	env.credhub.setLatest(divergedPassword(waitDeployment), "p2")
	release()

	expectStoppedForApproval(t, env, task.ID)
}

//nolint:paralleltest // the upgrade runs spruce, whose global state races across tests
func TestUpgradeStopsWhenDivergedAfterCreate(t *testing.T) {
	env := newDivergedEnv(t, true)
	env.director.getGate = make(chan struct{})
	env.director.getStarted = make(chan struct{})

	task, err := env.manager.CreateTask(context.Background(), upgradeRequest(waitInstanceID))
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	select {
	case <-env.director.getStarted:
	case <-time.After(waitTimeout):
		t.Fatal("the upgrade never read its deployment")
	}

	env.diverge(t)

	close(env.director.getGate)

	expectStoppedForApproval(t, env, task.ID)
}

// flakyDivergenceVault fails the next failures reads of a divergence
// record, and counts every such read.
type flakyDivergenceVault struct {
	*waitVault

	failures atomic.Int64
	reads    atomic.Int64
}

func (v *flakyDivergenceVault) Get(ctx context.Context, path string, result interface{}) (bool, error) {
	if strings.HasPrefix(path, "certificate-divergence/") {
		v.reads.Add(1)

		if v.failures.Add(-1) >= 0 {
			return false, errVaultSealed
		}
	}

	return v.waitVault.Get(ctx, path, result)
}

var errVaultSealed = errors.New("vault: 503 sealed")

// newFlakyDivergedEnv builds the environment with a divergence store whose
// reads fail while the returned vault has failures left.
func newFlakyDivergedEnv(t *testing.T) (*divergedEnv, *flakyDivergenceVault) {
	t.Helper()

	var flaky *flakyDivergenceVault

	env := newDivergedEnvWith(t, true, func(v *waitVault) interfaces.Vault {
		flaky = &flakyDivergenceVault{waitVault: v}

		return flaky
	})

	return env, flaky
}

// gateDeploymentRead makes the upgrade wait on its read of the deployment
// until the returned function is called, and returns once the read began.
func (e *divergedEnv) gateDeploymentRead(t *testing.T, create func()) func() {
	t.Helper()

	e.director.getGate = make(chan struct{})
	e.director.getStarted = make(chan struct{})

	create()

	select {
	case <-e.director.getStarted:
	case <-time.After(waitTimeout):
		t.Fatal("the upgrade never read its deployment")
	}

	return func() { close(e.director.getGate) }
}

//nolint:paralleltest // the upgrade runs spruce, whose global state races across tests
func TestUpgradeRetriesDivergenceRead(t *testing.T) {
	env, flaky := newFlakyDivergedEnv(t)

	var task *UpgradeTask

	release := env.gateDeploymentRead(t, func() {
		var err error

		task, err = env.manager.CreateTask(context.Background(), upgradeRequest(waitInstanceID))
		if err != nil {
			t.Fatalf("CreateTask: %v", err)
		}
	})

	// Vault fails twice, and the third read finds no record.
	flaky.reads.Store(0)
	flaky.failures.Store(2)
	release()

	select {
	case <-env.director.waitStarted:
	case <-time.After(waitTimeout):
		t.Fatal("the upgrade never deployed after Vault recovered")
	}

	if reads := flaky.reads.Load(); reads != 3 {
		t.Fatalf("expected three reads of the divergence record, got %d", reads)
	}

	if options := env.director.options(); len(options) != 0 {
		t.Fatalf("expected a plain deploy, got %+v", options)
	}

	env.director.finish <- &bosh.Task{ID: waitDeployTaskID, State: boshTaskDone}

	eventuallySaved(t, env.vault, task.ID, func(saved UpgradeTask) bool { return saved.Status == TaskStatusCompleted })
}

//nolint:paralleltest // the upgrade runs spruce, whose global state races across tests
func TestUpgradeFailsClosedWhenDivergenceUnreadable(t *testing.T) {
	env, flaky := newFlakyDivergedEnv(t)

	var task *UpgradeTask

	release := env.gateDeploymentRead(t, func() {
		var err error

		task, err = env.manager.CreateTask(context.Background(), upgradeRequest(waitInstanceID))
		if err != nil {
			t.Fatalf("CreateTask: %v", err)
		}
	})

	flaky.reads.Store(0)
	flaky.failures.Store(1000)
	release()

	saved := eventuallySaved(t, env.vault, task.ID, func(saved UpgradeTask) bool {
		return saved.Instances[0].Status == InstanceStatusFailed
	})

	message := saved.Instances[0].Error
	if !strings.Contains(message, "could not read") || !strings.Contains(message, "can be upgraded again") {
		t.Fatalf("expected a failed read that can be retried, got %q", message)
	}

	if reads := flaky.reads.Load(); reads != 3 {
		t.Fatalf("expected three reads of the divergence record, got %d", reads)
	}

	if deploys := env.director.deploys.Load(); deploys != 0 {
		t.Fatalf("expected no deploy, got %d", deploys)
	}
}

//nolint:paralleltest // a created upgrade runs spruce, whose global state races across tests
func TestUpgradeAnswers503WhenDivergenceUnreadable(t *testing.T) {
	env, flaky := newFlakyDivergedEnv(t)
	flaky.failures.Store(1000)

	_, err := env.manager.PreviewUpgrade(context.Background(), upgradeRequest(waitInstanceID, divergedInstance2))
	expectAPIError(t, err, http.StatusServiceUnavailable, apierr.CodeVaultUnavailable)

	_, err = env.manager.CreateTask(context.Background(), upgradeRequest(waitInstanceID, divergedInstance2))
	expectAPIError(t, err, http.StatusServiceUnavailable, apierr.CodeVaultUnavailable)

	env.expectNothingCreated(t)
}

// An ID that can't name a divergence record has none, so the upgrade
// treats it as it always has and fails only that instance.
//
//nolint:paralleltest // a created upgrade runs spruce, whose global state races across tests
func TestCreateUpgradeWithBadInstanceIDFailsOnlyThatInstance(t *testing.T) {
	const badID = "a/b"

	without := newDivergedEnv(t, false)

	want, err := without.manager.CreateTask(context.Background(), upgradeRequest(badID))
	if err != nil {
		t.Fatalf("CreateTask without a divergence store: %v", err)
	}

	env := newDivergedEnv(t, true)

	got, err := env.manager.CreateTask(context.Background(), upgradeRequest(badID))
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	if got.Instances[0].Status != InstanceStatusFailed || got.Instances[0].Status != want.Instances[0].Status ||
		got.Instances[0].Error != want.Instances[0].Error {
		t.Fatalf("expected the instance to fail as it does without a store (%s %q), got %s %q",
			want.Instances[0].Status, want.Instances[0].Error, got.Instances[0].Status, got.Instances[0].Error)
	}

	preview := env.preview(t, upgradeRequest(badID))
	if len(preview.Diverged) != 0 {
		t.Fatalf("expected no diverged instance, got %+v", preview.Diverged)
	}
}

func TestUpgradePreviewChecksDirectorOnlyWhenDiverged(t *testing.T) {
	t.Parallel()

	env := newDivergedEnv(t, true)
	env.director.setVersion("278.0.0")

	// A clean request never asks the director for its version.
	env.preview(t, upgradeRequest(waitInstanceID, divergedInstance2))

	if calls := env.director.infoCalls.Load(); calls != 0 {
		t.Fatalf("expected no director version check, got %d", calls)
	}

	env.diverge(t)

	_, err := env.manager.PreviewUpgrade(context.Background(), upgradeRequest(waitInstanceID, divergedInstance2))
	expectAPIError(t, err, http.StatusForbidden, apierr.CodeDirectorUnsupported)
}

//nolint:paralleltest // the upgrade runs spruce, whose global state races across tests
func TestUpgradeOfDivergedInstanceChecksDirector(t *testing.T) {
	env := newDivergedEnv(t, true)
	env.diverge(t)

	preview := env.preview(t, upgradeRequest(waitInstanceID))

	req := upgradeRequest(waitInstanceID)
	req.UpgradePreviewID = preview.ID

	var task *UpgradeTask

	release := env.gateDeploymentRead(t, func() {
		var err error

		task, err = env.manager.CreateTask(context.Background(), req)
		if err != nil {
			t.Fatalf("CreateTask: %v", err)
		}
	})

	// The director was replaced by one that ignores latest variables.
	env.director.setVersion("278.0.0")
	release()

	saved := eventuallySaved(t, env.vault, task.ID, func(saved UpgradeTask) bool {
		return saved.Instances[0].Status == InstanceStatusFailed
	})

	if message := saved.Instances[0].Error; !strings.Contains(message, "278.0.0") {
		t.Fatalf("expected a failure naming the director version, got %q", message)
	}

	if deploys := env.director.deploys.Load(); deploys != 0 {
		t.Fatalf("expected no deploy, got %d", deploys)
	}
}

func TestUpgradePreviewOfVanishedDivergedInstance(t *testing.T) {
	t.Parallel()

	env := newDivergedEnv(t, true)

	// gone-1 has no instance data left, and nodeploy-1 names no deployment.
	env.recordDivergence(t, "gone-1", "redis-gone-1")
	env.recordDivergence(t, "nodeploy-1", "redis-nodeploy-1")

	err := env.vault.Put(context.Background(), "nodeploy-1", map[string]string{"instance_name": "redis"})
	if err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{"gone-1", "nodeploy-1"} {
		_, err := env.manager.PreviewUpgrade(context.Background(), upgradeRequest(waitInstanceID, id))
		expectAPIError(t, err, http.StatusNotFound, apierr.CodeInstanceNotFound)

		if !strings.Contains(err.Error(), id) {
			t.Fatalf("expected the error to name %s, got %v", id, err)
		}
	}
}

//nolint:paralleltest // a created upgrade runs spruce, whose global state races across tests
func TestCreateUpgradeApprovesOnlyDivergedInstances(t *testing.T) {
	env := newDivergedEnv(t, true)
	env.diverge(t)

	preview := env.preview(t, upgradeRequest(waitInstanceID, divergedInstance2))

	req := upgradeRequest(waitInstanceID, divergedInstance2)
	req.UpgradePreviewID = preview.ID

	task, err := env.manager.CreateTask(context.Background(), req)
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	want := []VersionPair{{Name: divergedLeaf(waitDeployment), DeployedVersionID: "v1", LatestVersionID: "v2"}}

	for name, instances := range map[string][]InstanceUpgrade{
		"created": task.Instances,
		"saved":   env.vault.savedTask(t, task.ID).Instances,
	} {
		if len(instances) != 2 {
			t.Fatalf("%s: expected two instances, got %+v", name, instances)
		}

		for _, inst := range instances {
			switch inst.InstanceID {
			case waitInstanceID:
				if !reflect.DeepEqual(inst.ApprovedAdditional, want) {
					t.Errorf("%s: expected the diverged instance's approval %+v, got %+v", name, want, inst.ApprovedAdditional)
				}
			case divergedInstance2:
				if inst.ApprovedAdditional != nil {
					t.Errorf("%s: expected no approval on the clean instance, got %+v", name, inst.ApprovedAdditional)
				}
			default:
				t.Errorf("%s: unexpected instance %s", name, inst.InstanceID)
			}
		}
	}
}

// A converge or reconciliation can clear the record between the create
// and the deploy, and the instance then deploys as any upgrade does.
//
//nolint:paralleltest // the upgrade runs spruce, whose global state races across tests
func TestUpgradeDeploysPlainWhenRecordClearedAfterCreate(t *testing.T) {
	env := newDivergedEnv(t, true)
	env.diverge(t)

	preview := env.preview(t, upgradeRequest(waitInstanceID))

	req := upgradeRequest(waitInstanceID)
	req.UpgradePreviewID = preview.ID

	var task *UpgradeTask

	release := env.gateDeploymentRead(t, func() {
		var err error

		task, err = env.manager.CreateTask(context.Background(), req)
		if err != nil {
			t.Fatalf("CreateTask: %v", err)
		}
	})

	env.vault.mu.Lock()
	delete(env.vault.saved, "certificate-divergence/"+waitInstanceID)
	env.vault.mu.Unlock()

	_, found, err := env.store.Get(context.Background(), waitInstanceID)
	if err != nil || found {
		t.Fatalf("expected the record to be gone, got found %t and %v", found, err)
	}

	release()

	select {
	case <-env.director.waitStarted:
	case <-time.After(waitTimeout):
		t.Fatal("the upgrade never waited on its deploy")
	}

	if options := env.director.options(); len(options) != 0 {
		t.Fatalf("expected a plain deploy, got %+v", options)
	}

	if deploys := env.director.deploys.Load(); deploys != 1 {
		t.Fatalf("expected one deploy, got %d", deploys)
	}

	env.director.finish <- &bosh.Task{ID: waitDeployTaskID, State: boshTaskDone}

	eventuallySaved(t, env.vault, task.ID, func(saved UpgradeTask) bool { return saved.Status == TaskStatusCompleted })
}
