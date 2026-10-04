package reconciler_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"blacksmith/internal/bosh"
	. "blacksmith/pkg/reconciler"
	"blacksmith/pkg/testutil"
)

var errSweepDirectorDown = errors.New("director unreachable")

const (
	sweepZombieID     = "181c0984-f4b6-461c-83ff-98ff41872f4b"
	sweepZombieName   = "valkey-standalone-" + sweepZombieID
	sweepSurvivorID   = "00000000-0000-4000-8000-00000000abcd"
	sweepSurvivorName = "valkey-standalone-" + sweepSurvivorID

	statusField         = "status"
	deletedAtField      = "deleted_at"
	deletedByField      = "deleted_by"
	deletionReasonField = "deletion_reason"
)

// sweepFixture wires a manager with a scripted director, a real
// IndexSynchronizer over the test vault, and an index holding one healthy
// instance whose deployment the scanner reports plus one zombie entry whose
// deployment is gone.
type sweepFixture struct {
	manager      *ReconcilerManager
	director     *testutil.ScriptedBOSHDirector
	synchronizer *IndexSynchronizer
	vault        *RealTestVault
}

func newSweepFixture(t *testing.T, zombieAge time.Duration) *sweepFixture {
	t.Helper()

	logger := NewMockLogger()
	director := testutil.NewScriptedBOSHDirector()
	director.GetDeploymentFn = func(name string) (*bosh.DeploymentDetail, error) {
		return nil, fmt.Errorf("%w: %s", bosh.ErrDeploymentNotFound, name)
	}
	director.FindRunningTaskForDeploymentFn = func(string) (*bosh.Task, error) { return nil, nil } //nolint:nilnil // no running task is a nil task without an error

	manager := NewReconcilerManager(newTestManagerConfig(), nil, nil, director, logger, nil)

	vault := NewTestVault(t)
	stamp := time.Now().Add(-zombieAge).Format(time.RFC3339)

	err := vault.Put("db", map[string]interface{}{
		sweepSurvivorID: map[string]interface{}{
			"service_id": "valkey", "plan_id": "standalone", "deployment_name": sweepSurvivorName,
			"reconciled": true, "reconciled_at": stamp,
		},
		sweepZombieID: map[string]interface{}{
			"service_id": "valkey", "plan_id": "standalone", "deployment_name": sweepZombieName,
			"reconciled": true, "reconciled_at": stamp, "discovered_at": stamp,
		},
	})
	if err != nil {
		t.Fatalf("failed to seed index: %v", err)
	}

	scan := &rmMockScanner{deployments: []DeploymentInfo{{Name: sweepSurvivorName}}}
	synchronizer := NewIndexSynchronizer(vault, logger)

	manager.Scanner = scan
	manager.Updater = &rmMockUpdater{}
	manager.Synchronizer = synchronizer

	return &sweepFixture{manager: manager, director: director, synchronizer: synchronizer, vault: vault}
}

func (f *sweepFixture) indexHas(t *testing.T, instanceID string) bool {
	t.Helper()

	idx, err := f.synchronizer.GetVaultIndex()
	if err != nil {
		t.Fatalf("failed to read index: %v", err)
	}

	_, exists := idx[instanceID]

	return exists
}

// An index entry whose deployment the director confirms missing, with no task
// in flight, is removed instead of being warned about every run.
func TestOrphanSweep_RemovesEntryWhenDirectorConfirmsDeploymentGone(t *testing.T) {
	t.Parallel()

	fixture := newSweepFixture(t, 3*time.Hour)

	fixture.manager.RunReconciliation(context.Background())

	if fixture.indexHas(t, sweepZombieID) {
		t.Fatalf("expected zombie entry %s to be removed from the index", sweepZombieID)
	}

	if !fixture.indexHas(t, sweepSurvivorID) {
		t.Fatalf("expected healthy entry %s to stay in the index", sweepSurvivorID)
	}

	if calls := fixture.director.Calls("GetDeployment"); len(calls) != 1 || calls[0] != sweepZombieName {
		t.Fatalf("expected one GetDeployment confirmation for %s, got %v", sweepZombieName, calls)
	}
}

// A running BOSH task on the deployment means an operation is in flight; the
// entry stays.
func TestOrphanSweep_KeepsEntryWhileTaskRuns(t *testing.T) {
	t.Parallel()

	fixture := newSweepFixture(t, 3*time.Hour)
	fixture.director.FindRunningTaskForDeploymentFn = func(string) (*bosh.Task, error) {
		return &bosh.Task{ID: 189, State: "processing", Description: "create deployment"}, nil
	}

	fixture.manager.RunReconciliation(context.Background())

	if !fixture.indexHas(t, sweepZombieID) {
		t.Fatalf("expected entry %s to be kept while a task runs", sweepZombieID)
	}
}

// A director error other than 404 does not confirm anything; the entry stays.
func TestOrphanSweep_KeepsEntryWhenDirectorUnreachable(t *testing.T) {
	t.Parallel()

	fixture := newSweepFixture(t, 3*time.Hour)
	fixture.director.GetDeploymentFn = func(string) (*bosh.DeploymentDetail, error) {
		return nil, errSweepDirectorDown
	}

	fixture.manager.RunReconciliation(context.Background())

	if !fixture.indexHas(t, sweepZombieID) {
		t.Fatalf("expected entry %s to be kept when the director cannot confirm absence", sweepZombieID)
	}
}

// A deployment that exists with an empty manifest is an in-flight deploy, not
// a missing deployment; the entry stays.
func TestOrphanSweep_KeepsEntryWhenDeploymentHasNoManifestYet(t *testing.T) {
	t.Parallel()

	fixture := newSweepFixture(t, 3*time.Hour)
	fixture.director.GetDeploymentFn = func(name string) (*bosh.DeploymentDetail, error) {
		return &bosh.DeploymentDetail{Name: name, Manifest: ""}, nil
	}

	fixture.manager.RunReconciliation(context.Background())

	if !fixture.indexHas(t, sweepZombieID) {
		t.Fatalf("expected entry %s to be kept while its deployment exists without a manifest", sweepZombieID)
	}
}

// A young entry may belong to a provision that has not reached BOSH yet; the
// entry stays.
func TestOrphanSweep_KeepsYoungEntry(t *testing.T) {
	t.Parallel()

	fixture := newSweepFixture(t, time.Minute)

	fixture.manager.RunReconciliation(context.Background())

	if !fixture.indexHas(t, sweepZombieID) {
		t.Fatalf("expected young entry %s to be kept", sweepZombieID)
	}

	if calls := fixture.director.Calls("GetDeployment"); len(calls) != 0 {
		t.Fatalf("expected no director confirmation for a young entry, got %v", calls)
	}
}

// A provision task record updated recently means the provision goroutine is
// still working; the entry stays.
func TestOrphanSweep_KeepsEntryWithRecentProvisionRecord(t *testing.T) {
	t.Parallel()

	fixture := newSweepFixture(t, 3*time.Hour)

	err := fixture.vault.Put(sweepZombieID+"/task", map[string]interface{}{
		"action": "provision", "state": "initializing", "task": 0, "updated_at": time.Now().Unix(),
	})
	if err != nil {
		t.Fatalf("failed to write task record: %v", err)
	}

	fixture.manager.RunReconciliation(context.Background())

	if !fixture.indexHas(t, sweepZombieID) {
		t.Fatalf("expected entry %s to be kept while its provision record is fresh", sweepZombieID)
	}
}

// A provision task record from hours ago is the normal leftover of a finished
// provision and must not block the sweep.
func TestOrphanSweep_RemovesEntryWithStaleProvisionRecord(t *testing.T) {
	t.Parallel()

	fixture := newSweepFixture(t, 3*time.Hour)

	err := fixture.vault.Put(sweepZombieID+"/task", map[string]interface{}{
		"action": "provision", "state": "in_progress", "task": 189, "updated_at": time.Now().Add(-3 * time.Hour).Unix(),
	})
	if err != nil {
		t.Fatalf("failed to write task record: %v", err)
	}

	fixture.manager.RunReconciliation(context.Background())

	if fixture.indexHas(t, sweepZombieID) {
		t.Fatalf("expected entry %s to be removed despite its stale provision record", sweepZombieID)
	}
}

// tombstoneEntry builds the bare entry the vm-monitor leaves behind: status
// deleted with no service, plan, or deployment fields.
func tombstoneEntry(deletedAgo time.Duration, extra map[string]interface{}) map[string]interface{} {
	entry := map[string]interface{}{
		statusField:         StatusDeleted,
		deletedAtField:      time.Now().Add(-deletedAgo).Format(time.RFC3339),
		deletedByField:      "vm-monitor",
		deletionReasonField: "deployment not found in BOSH director",
	}

	for key, value := range extra {
		entry[key] = value
	}

	return entry
}

// seedZombieEntry replaces the fixture's zombie entry with the given one.
func (f *sweepFixture) seedZombieEntry(t *testing.T, entry map[string]interface{}) {
	t.Helper()

	idx, err := f.synchronizer.GetVaultIndex()
	if err != nil {
		t.Fatalf("failed to read index: %v", err)
	}

	idx[sweepZombieID] = entry

	err = f.synchronizer.SaveVaultIndex(idx)
	if err != nil {
		t.Fatalf("failed to seed entry: %v", err)
	}
}

// A tombstone that names no deployment is the vm-monitor's record of a 404;
// once it is old enough its age alone lets the sweep remove it.
func TestOrphanSweep_RemovesBareTombstoneByAge(t *testing.T) {
	t.Parallel()

	fixture := newSweepFixture(t, 3*time.Hour)
	fixture.seedZombieEntry(t, tombstoneEntry(3*time.Hour, nil))

	fixture.manager.RunReconciliation(context.Background())

	if fixture.indexHas(t, sweepZombieID) {
		t.Fatalf("expected bare tombstone %s to be removed", sweepZombieID)
	}

	if calls := fixture.director.Calls("GetDeployment"); len(calls) != 0 {
		t.Fatalf("expected no director call for a tombstone without a deployment name, got %v", calls)
	}
}

// A fresh tombstone stays until it is older than the sweep minimum age.
func TestOrphanSweep_KeepsYoungTombstone(t *testing.T) {
	t.Parallel()

	fixture := newSweepFixture(t, 3*time.Hour)
	fixture.seedZombieEntry(t, tombstoneEntry(time.Minute, nil))

	fixture.manager.RunReconciliation(context.Background())

	if !fixture.indexHas(t, sweepZombieID) {
		t.Fatalf("expected young tombstone %s to be kept", sweepZombieID)
	}
}

// A tombstone that still names its deployment is confirmed with the director
// and removed on a real 404.
func TestOrphanSweep_RemovesNamedTombstoneAfterDirectorConfirms(t *testing.T) {
	t.Parallel()

	fixture := newSweepFixture(t, 3*time.Hour)
	fixture.seedZombieEntry(t, tombstoneEntry(3*time.Hour, map[string]interface{}{"last_deployment": sweepZombieName}))

	fixture.manager.RunReconciliation(context.Background())

	if fixture.indexHas(t, sweepZombieID) {
		t.Fatalf("expected named tombstone %s to be removed after the director confirmed the 404", sweepZombieID)
	}

	if calls := fixture.director.Calls("GetDeployment"); len(calls) != 1 || calls[0] != sweepZombieName {
		t.Fatalf("expected one GetDeployment confirmation for %s, got %v", sweepZombieName, calls)
	}
}

// A named tombstone whose deployment still exists on the director is kept.
func TestOrphanSweep_KeepsNamedTombstoneWhenDeploymentExists(t *testing.T) {
	t.Parallel()

	fixture := newSweepFixture(t, 3*time.Hour)
	fixture.seedZombieEntry(t, tombstoneEntry(3*time.Hour, map[string]interface{}{"deployment_name": sweepZombieName}))
	fixture.director.GetDeploymentFn = func(name string) (*bosh.DeploymentDetail, error) {
		return &bosh.DeploymentDetail{Name: name, Manifest: "name: " + name}, nil
	}

	fixture.manager.RunReconciliation(context.Background())

	if !fixture.indexHas(t, sweepZombieID) {
		t.Fatalf("expected named tombstone %s to be kept while its deployment exists", sweepZombieID)
	}
}

// A named tombstone with a running BOSH task on its deployment is kept.
func TestOrphanSweep_KeepsNamedTombstoneWhileTaskRuns(t *testing.T) {
	t.Parallel()

	fixture := newSweepFixture(t, 3*time.Hour)
	fixture.seedZombieEntry(t, tombstoneEntry(3*time.Hour, map[string]interface{}{"deployment_name": sweepZombieName}))
	fixture.director.FindRunningTaskForDeploymentFn = func(string) (*bosh.Task, error) {
		return &bosh.Task{ID: 191, State: "processing", Description: "create deployment"}, nil
	}

	fixture.manager.RunReconciliation(context.Background())

	if !fixture.indexHas(t, sweepZombieID) {
		t.Fatalf("expected named tombstone %s to be kept while a task runs", sweepZombieID)
	}
}

// credentialField is the secret key the retention test writes and reads back.
const credentialField = "password"

// The sweep removes the index entry only. A normal deprovision keeps the
// instance's secrets for auditing, so the sweep keeps them too.
func TestOrphanSweep_KeepsInstanceSecretsAfterRemovingTombstone(t *testing.T) {
	t.Parallel()

	fixture := newSweepFixture(t, 3*time.Hour)
	fixture.seedZombieEntry(t, tombstoneEntry(3*time.Hour, nil))

	credentials := map[string]interface{}{credentialField: "keep-me"}

	err := fixture.vault.Put(sweepZombieID+"/credentials", credentials)
	if err != nil {
		t.Fatalf("failed to seed credentials: %v", err)
	}

	fixture.manager.RunReconciliation(context.Background())

	if fixture.indexHas(t, sweepZombieID) {
		t.Fatalf("expected tombstone %s to be removed from the index", sweepZombieID)
	}

	kept, err := fixture.vault.Get(sweepZombieID + "/credentials")
	if err != nil {
		t.Fatalf("expected the instance credentials to survive the sweep: %v", err)
	}

	if kept[credentialField] != "keep-me" {
		t.Fatalf("expected the credentials to be unchanged, got %v", kept)
	}
}

var errSweepSaveRefused = errors.New("vault refused the index write")

const sweepModeDryRun = "dry-run"

// credentialCall is one CleanupDeploymentCredentials call the reconciler made.
type credentialCall struct {
	instanceID     string
	deploymentName string
}

// recordingCredentialHooks records the reconciler's calls into the broker's
// CredHub cleanup entry points. It can be told to panic.
type recordingCredentialHooks struct {
	mu      sync.Mutex
	cleans  []credentialCall
	sweeps  []map[string]bool
	mode    string
	explode bool
}

func (h *recordingCredentialHooks) CleanupDeploymentCredentials(_ context.Context, instanceID, deploymentName string) {
	h.mu.Lock()
	h.cleans = append(h.cleans, credentialCall{instanceID: instanceID, deploymentName: deploymentName})
	explode := h.explode
	h.mu.Unlock()

	if explode {
		panic("credential cleaner exploded")
	}
}

func (h *recordingCredentialHooks) SweepOrphanedCredentials(_ context.Context, live map[string]bool) {
	h.mu.Lock()
	h.sweeps = append(h.sweeps, live)
	explode := h.explode
	h.mu.Unlock()

	if explode {
		panic("credential sweeper exploded")
	}
}

func (h *recordingCredentialHooks) CredentialSweepMode() string {
	return h.mode
}

func (h *recordingCredentialHooks) Cleans() []credentialCall {
	h.mu.Lock()
	defer h.mu.Unlock()

	return append([]credentialCall(nil), h.cleans...)
}

func (h *recordingCredentialHooks) Sweeps() []map[string]bool {
	h.mu.Lock()
	defer h.mu.Unlock()

	return append([]map[string]bool(nil), h.sweeps...)
}

// sweepSaveFailingVault refuses the index write that drops the zombie entry,
// which is the orphan sweep's save, and lets every other write through.
type sweepSaveFailingVault struct {
	*RealTestVault
}

func (v *sweepSaveFailingVault) Put(path string, secret map[string]interface{}) error {
	if path == "db" {
		if _, kept := secret[sweepZombieID]; !kept {
			return errSweepSaveRefused
		}
	}

	return v.RealTestVault.Put(path, secret)
}

// recordingReconcilerLogger keeps every line the manager logs.
type recordingReconcilerLogger struct {
	mu    sync.Mutex
	lines []string
}

func (l *recordingReconcilerLogger) Debugf(format string, args ...interface{}) {
	l.recordf(format, args...)
}
func (l *recordingReconcilerLogger) Infof(format string, args ...interface{}) {
	l.recordf(format, args...)
}
func (l *recordingReconcilerLogger) Warningf(format string, args ...interface{}) {
	l.recordf(format, args...)
}
func (l *recordingReconcilerLogger) Errorf(format string, args ...interface{}) {
	l.recordf(format, args...)
}

func (l *recordingReconcilerLogger) recordf(format string, args ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *recordingReconcilerLogger) output() string {
	l.mu.Lock()
	defer l.mu.Unlock()

	return strings.Join(l.lines, "\n")
}

// A removed entry triggers exactly one cleanup call, naming its instance and
// deployment.
func TestOrphanSweep_CallsCredentialCleanerForRemovedEntry(t *testing.T) {
	t.Parallel()

	fixture := newSweepFixture(t, 3*time.Hour)
	hooks := &recordingCredentialHooks{}
	fixture.manager.SetCredentialHooks(hooks, nil)

	fixture.manager.RunReconciliation(context.Background())

	if fixture.indexHas(t, sweepZombieID) {
		t.Fatalf("expected zombie entry %s to be removed", sweepZombieID)
	}

	want := []credentialCall{{instanceID: sweepZombieID, deploymentName: sweepZombieName}}
	if got := hooks.Cleans(); len(got) != 1 || got[0] != want[0] {
		t.Fatalf("expected exactly %v, got %v", want, got)
	}
}

// A removed tombstone that names no deployment triggers no cleanup call.
func TestOrphanSweep_SkipsCredentialCleanerForNamelessTombstone(t *testing.T) {
	t.Parallel()

	fixture := newSweepFixture(t, 3*time.Hour)
	fixture.seedZombieEntry(t, tombstoneEntry(3*time.Hour, nil))

	hooks := &recordingCredentialHooks{}
	fixture.manager.SetCredentialHooks(hooks, nil)

	fixture.manager.RunReconciliation(context.Background())

	if fixture.indexHas(t, sweepZombieID) {
		t.Fatalf("expected bare tombstone %s to be removed", sweepZombieID)
	}

	if got := hooks.Cleans(); len(got) != 0 {
		t.Fatalf("expected no cleanup call for a tombstone without a deployment name, got %v", got)
	}
}

// An entry the sweep keeps triggers no cleanup call.
func TestOrphanSweep_SkipsCredentialCleanerForKeptEntry(t *testing.T) {
	t.Parallel()

	fixture := newSweepFixture(t, 3*time.Hour)
	fixture.director.FindRunningTaskForDeploymentFn = func(string) (*bosh.Task, error) {
		return &bosh.Task{ID: 233, State: "processing", Description: "delete deployment"}, nil
	}

	hooks := &recordingCredentialHooks{}
	fixture.manager.SetCredentialHooks(hooks, nil)

	fixture.manager.RunReconciliation(context.Background())

	if !fixture.indexHas(t, sweepZombieID) {
		t.Fatalf("expected entry %s to be kept while a task runs", sweepZombieID)
	}

	if got := hooks.Cleans(); len(got) != 0 {
		t.Fatalf("expected no cleanup call for a kept entry, got %v", got)
	}
}

// When the index save fails, nothing was removed, so no cleanup call is made.
func TestOrphanSweep_SkipsCredentialCleanerWhenIndexSaveFails(t *testing.T) {
	t.Parallel()

	fixture := newSweepFixture(t, 3*time.Hour)
	failing := &sweepSaveFailingVault{RealTestVault: fixture.vault}
	synchronizer := NewIndexSynchronizer(failing, NewMockLogger())
	fixture.manager.Synchronizer = synchronizer
	fixture.synchronizer = synchronizer

	hooks := &recordingCredentialHooks{}
	fixture.manager.SetCredentialHooks(hooks, nil)

	fixture.manager.RunReconciliation(context.Background())

	if !fixture.indexHas(t, sweepZombieID) {
		t.Fatalf("expected entry %s to stay when the index save fails", sweepZombieID)
	}

	if got := hooks.Cleans(); len(got) != 0 {
		t.Fatalf("expected no cleanup call when the index save fails, got %v", got)
	}
}

// With no cleaner set, the sweep removes the entry exactly as before.
func TestOrphanSweep_NilCredentialCleanerChangesNothing(t *testing.T) {
	t.Parallel()

	fixture := newSweepFixture(t, 3*time.Hour)
	fixture.manager.SetCredentialHooks(nil, nil)

	fixture.manager.RunReconciliation(context.Background())

	if fixture.indexHas(t, sweepZombieID) {
		t.Fatalf("expected zombie entry %s to be removed", sweepZombieID)
	}

	if !fixture.indexHas(t, sweepSurvivorID) {
		t.Fatalf("expected healthy entry %s to stay", sweepSurvivorID)
	}
}

// A cleaner that panics cannot stop the run: the entry is removed and the run
// finishes and records its result.
func TestOrphanSweep_CredentialCleanerPanicDoesNotStopTheRun(t *testing.T) {
	t.Parallel()

	fixture := newSweepFixture(t, 3*time.Hour)
	logger := &recordingReconcilerLogger{}
	hooks := &recordingCredentialHooks{explode: true}

	manager := NewReconcilerManager(newTestManagerConfig(), nil, nil, fixture.director, logger, nil)
	manager.Scanner = fixture.manager.Scanner
	manager.Updater = fixture.manager.Updater
	manager.Synchronizer = fixture.synchronizer
	manager.SetCredentialHooks(hooks, nil)

	manager.RunReconciliation(context.Background())

	if fixture.indexHas(t, sweepZombieID) {
		t.Fatalf("expected zombie entry %s to be removed", sweepZombieID)
	}

	if got := hooks.Cleans(); len(got) != 1 {
		t.Fatalf("expected one cleanup call, got %v", got)
	}

	if !strings.Contains(logger.output(), "orphaned index entries removed") {
		t.Fatalf("expected the run to finish after the cleaner panicked, log was:\n%s", logger.output())
	}

	if !strings.Contains(logger.output(), "CredHub cleanup hook for deployment "+sweepZombieName+" (instance "+sweepZombieID+") panicked") {
		t.Fatalf("expected the panic to be logged with the deployment name, log was:\n%s", logger.output())
	}
}

// After the index sweep, the run hands the orphan credential sweep the names
// of the deployments the director listed this run.
func TestOrphanSweep_CallsCredentialSweeperWithTheDeploymentScan(t *testing.T) {
	t.Parallel()

	fixture := newSweepFixture(t, 3*time.Hour)
	hooks := &recordingCredentialHooks{mode: sweepModeDryRun}
	fixture.manager.SetCredentialHooks(hooks, hooks)

	fixture.manager.RunReconciliation(context.Background())

	sweeps := hooks.Sweeps()
	if len(sweeps) != 1 {
		t.Fatalf("expected one sweep call, got %d", len(sweeps))
	}

	if len(sweeps[0]) != 1 || !sweeps[0][sweepSurvivorName] {
		t.Fatalf("expected the sweep to receive only %s as live, got %v", sweepSurvivorName, sweeps[0])
	}
}

// A sweeper that panics cannot stop the run, which still finishes and logs
// its result.
func TestOrphanSweep_CredentialSweeperPanicDoesNotStopTheRun(t *testing.T) {
	t.Parallel()

	fixture := newSweepFixture(t, 3*time.Hour)
	logger := &recordingReconcilerLogger{}
	sweeper := &recordingCredentialHooks{explode: true}

	manager := NewReconcilerManager(newTestManagerConfig(), nil, nil, fixture.director, logger, nil)
	manager.Scanner = fixture.manager.Scanner
	manager.Updater = fixture.manager.Updater
	manager.Synchronizer = fixture.synchronizer
	manager.SetCredentialHooks(nil, sweeper)

	manager.RunReconciliation(context.Background())

	if got := sweeper.Sweeps(); len(got) != 1 {
		t.Fatalf("expected one sweep call, got %d", len(got))
	}

	if !strings.Contains(logger.output(), "CredHub orphan sweep hook panicked, and the reconciler run continues") {
		t.Fatalf("expected the panic to be logged, log was:\n%s", logger.output())
	}

	if !strings.Contains(logger.output(), "orphaned index entries removed") {
		t.Fatalf("expected the run to finish after the sweeper panicked, log was:\n%s", logger.output())
	}
}

// With no sweeper set, the run makes no sweep call and finishes as before.
func TestOrphanSweep_NilCredentialSweeperChangesNothing(t *testing.T) {
	t.Parallel()

	fixture := newSweepFixture(t, 3*time.Hour)
	cleaner := &recordingCredentialHooks{}
	fixture.manager.SetCredentialHooks(cleaner, nil)

	fixture.manager.RunReconciliation(context.Background())

	if got := cleaner.Sweeps(); len(got) != 0 {
		t.Fatalf("expected no sweep call without a sweeper, got %d", len(got))
	}

	if fixture.indexHas(t, sweepZombieID) {
		t.Fatalf("expected zombie entry %s to be removed", sweepZombieID)
	}
}

// CredentialHooks returns exactly what SetCredentialHooks stored.
func TestCredentialHooks_ReturnsWhatWasSet(t *testing.T) {
	t.Parallel()

	manager := NewReconcilerManager(newTestManagerConfig(), nil, nil, nil, NewMockLogger(), nil)

	cleaner, sweeper := manager.CredentialHooks()
	if cleaner != nil || sweeper != nil {
		t.Fatalf("expected no hooks on a new manager, got %v and %v", cleaner, sweeper)
	}

	hooks := &recordingCredentialHooks{mode: sweepModeDryRun}
	manager.SetCredentialHooks(hooks, hooks)

	cleaner, sweeper = manager.CredentialHooks()
	if cleaner != hooks || sweeper != hooks {
		t.Fatalf("expected both hooks to be the recorder, got %v and %v", cleaner, sweeper)
	}
}

// Start logs whether each hook is wired and the sweep mode, so a dead wiring
// is visible in the log.
func TestCredentialHooks_StartLogsTheWiring(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		cleaner DeploymentCredentialCleaner
		sweeper OrphanedCredentialSweeper
		want    string
	}{
		{name: "nothing wired", want: "CredHub cleanup hooks not wired: deployment cleanup off, orphan sweep off"},
		{
			name:    "cleanup only",
			cleaner: &recordingCredentialHooks{},
			want:    "CredHub cleanup hooks wired: deployment cleanup on, orphan sweep not wired",
		},
		{
			name:    "both, sweep in dry-run",
			cleaner: &recordingCredentialHooks{mode: sweepModeDryRun},
			sweeper: &recordingCredentialHooks{mode: sweepModeDryRun},
			want:    "CredHub cleanup hooks wired: deployment cleanup on, orphan sweep dry-run",
		},
		{
			name:    "both, cleanup disabled in the broker",
			cleaner: &recordingCredentialHooks{mode: CredentialSweepModeDisabled},
			sweeper: &recordingCredentialHooks{mode: CredentialSweepModeDisabled},
			want:    "CredHub cleanup hooks wired, but CredHub cleanup is disabled in the broker",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			logger := &recordingReconcilerLogger{}
			config := newTestManagerConfig()
			config.Interval = time.Hour

			manager := NewReconcilerManager(config, nil, nil, nil, logger, nil)
			manager.SetCredentialHooks(testCase.cleaner, testCase.sweeper)

			err := manager.Start(context.Background())
			if err != nil {
				t.Fatalf("start failed: %v", err)
			}

			t.Cleanup(func() { _ = manager.Stop() })

			if !strings.Contains(logger.output(), testCase.want) {
				t.Fatalf("expected %q in the startup log, got:\n%s", testCase.want, logger.output())
			}
		})
	}
}
