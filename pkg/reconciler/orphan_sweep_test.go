package reconciler_test

import (
	"context"
	"errors"
	"fmt"
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
