package broker_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"blacksmith/internal/audit"
	"blacksmith/internal/audit/audittest"
	"blacksmith/internal/bosh"
	"blacksmith/internal/broker"
	internalVault "blacksmith/internal/vault"
	"blacksmith/pkg/testutil"

	"github.com/fivetwenty-io/osbapi/v2/pkg/osbapi"
)

// settleWriteFailures is how many times the specs fail the write that
// settles an update.
const settleWriteFailures = 3

// errDeployRefused is the director's refusal of a deploy.
var errDeployRefused = errors.New("the director refused the deploy")

// faultedUpdateFixture is an update fixture whose broker reaches Vault
// through a proxy that can fail writes, while the fixture reads Vault
// directly. A failed settle write is retried at the follow bound.
func faultedUpdateFixture(t *testing.T) (*updateFixture, *testutil.VaultFaultProxy) {
	t.Helper()

	server, err := testutil.NewVaultDevServer(t)
	if err != nil {
		t.Fatalf("failed to start Vault: %v", err)
	}

	t.Cleanup(server.Close)

	proxy, err := testutil.NewVaultFaultProxy(server.Addr)
	if err != nil {
		t.Fatalf("failed to start the Vault proxy: %v", err)
	}

	t.Cleanup(proxy.Close)

	director := testutil.NewScriptedBOSHDirector()
	director.GetInfoFn = func() (*bosh.Info, error) { return &bosh.Info{UUID: updateDirector}, nil }
	director.FindRunningTaskForDeploymentFn = func(string) (*bosh.Task, error) { return nil, nil } //nolint:nilnil // no running task is a nil task without an error

	changes := audit.NewChangeStore(audittest.NewKV(), &audittest.Logger{})
	brokerInstance := &broker.Broker{BOSH: director, Vault: internalVault.New(proxy.Addr, server.RootToken, true), Plans: updatePlans()}
	brokerInstance.SetChanges(changes)
	brokerInstance.SetUpdateTaskWait(followBound)

	direct := internalVault.New(server.Addr, server.RootToken, true)

	return &updateFixture{broker: brokerInstance, vault: direct, director: director, changes: changes}, proxy
}

// failSettleWrites fails the next writes of the instance's task record,
// which is the write that settles its update.
func failSettleWrites(proxy *testutil.VaultFaultProxy, instanceID string, times int) {
	proxy.FailWrites(instanceID+"/task", http.StatusBadRequest, "task record write refused by the test", times)
}

// waitFollowed waits until the update follows the held task, so every
// write of its record before the settle has landed.
func (d *heldTaskDirector) waitFollowed(t *testing.T) {
	t.Helper()

	select {
	case <-d.waiting:
	case <-time.After(10 * time.Second):
		t.Fatalf("BOSH task %d was never followed", d.taskID)
	}
}

// requireOneChangeRecord fails the spec unless the update left one change
// record, with outcome.
func (f *updateFixture) requireOneChangeRecord(ctx context.Context, t *testing.T, outcome string) {
	t.Helper()

	if records := f.updateRecords(ctx, t); len(records) != 1 || records[0].Outcome != outcome {
		t.Fatalf("the change records are %+v, want one %s update", records, outcome)
	}
}

// A succeeded update whose settle write fails retries the write until it
// lands, and only then writes its change record, deletes the saved
// manifest, and releases the claim.
func TestUpdate_SettleWriteFails_RetriesSuccess(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fixture, proxy := faultedUpdateFixture(t)
	script := &deployScript{}
	script.script(fixture, outcomeTaskDone)

	held := fixture.holdTask(updateTaskID)
	instanceID := fixture.startFailingUpdate(ctx, t)

	held.waitFollowed(t)
	failSettleWrites(proxy, instanceID, settleWriteFailures)
	close(held.release)

	if answer := fixture.settled(ctx, t, instanceID); answer.State != osbapi.StateSucceeded {
		t.Fatalf("the update settled as %+v, want succeeded", answer)
	}

	fixture.requireOneChangeRecord(ctx, t, "succeeded")
	fixture.requireNoSavedManifest(ctx, t, instanceID)
	fixture.requireReleased(t, instanceID)
}

// A rolled-back update whose settle write fails retries the write until
// it lands, and only then writes its change record, deletes the saved
// manifest, and releases the claim.
func TestUpdate_SettleWriteFails_RetriesRolledBack(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fixture, proxy := faultedUpdateFixture(t)
	fixture.rollbackOn(ctx, t, true)

	script := heldRollback()
	script.script(fixture, outcomeTaskDone)

	instanceID := fixture.startFailingUpdate(ctx, t)
	script.waitHolding(t)
	failSettleWrites(proxy, instanceID, settleWriteFailures)
	close(script.hold)

	answer := fixture.settled(ctx, t, instanceID)
	if answer.State != osbapi.StateFailed || answer.InstanceUsable == nil || !*answer.InstanceUsable {
		t.Fatalf("the update settled as %+v, want failed and usable", answer)
	}

	fixture.requireOneChangeRecord(ctx, t, "rolled_back")
	fixture.requireNoSavedManifest(ctx, t, instanceID)
	fixture.requireReleased(t, instanceID)
}

// An update whose deploy the director refused, and whose failure write
// fails, retries the write until it lands, and only then writes its
// change record, deletes the saved manifest, and releases the claim.
func TestUpdate_SettleWriteFails_RetriesRefusedDeploy(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fixture, proxy := faultedUpdateFixture(t)
	script := &deployScript{}
	script.script(fixture, outcomeTaskDone)

	refusing := make(chan struct{})
	refuse := make(chan struct{})
	fixture.director.UpdateDeploymentFn = func(string, string) (*bosh.Task, error) {
		close(refusing)
		<-refuse

		return nil, errDeployRefused
	}

	instanceID := fixture.startFailingUpdate(ctx, t)

	<-refusing
	failSettleWrites(proxy, instanceID, settleWriteFailures)
	close(refuse)

	if answer := fixture.settled(ctx, t, instanceID); answer.State != osbapi.StateFailed {
		t.Fatalf("the update settled as %+v, want failed", answer)
	}

	fixture.requireOneChangeRecord(ctx, t, outcomeStateFailed)
	fixture.requireNoSavedManifest(ctx, t, instanceID)
	fixture.requireReleased(t, instanceID)
}

// A settle write that keeps failing until the broker shuts down leaves
// the update unsettled on record with its saved manifest and no change
// record, so re-adoption settles it at the next start.
func TestUpdate_SettleWriteFailsUntilShutdown_LeavesItForReadopt(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fixture, proxy := faultedUpdateFixture(t)

	service, shutdown := context.WithCancel(ctx)
	defer shutdown()

	fixture.broker.ServiceContext = service

	script := &deployScript{}
	script.script(fixture, outcomeTaskDone)

	held := fixture.holdTask(updateTaskID)
	instanceID := fixture.startFailingUpdate(ctx, t)

	held.waitFollowed(t)

	tried := len(proxy.WritesTo(instanceID + "/task"))

	failSettleWrites(proxy, instanceID, -1)
	close(held.release)

	// The settle write fails, and is tried again, before the shutdown.
	deadline := time.Now().Add(10 * time.Second)
	for len(proxy.WritesTo(instanceID+"/task")) < tried+2 {
		if time.Now().After(deadline) {
			t.Fatal("the failed settle write was never tried again")
		}

		time.Sleep(followBound)
	}

	shutdown()
	fixture.broker.WaitForBackground(ctx)

	if record := readTaskRecord(ctx, t, fixture.vault, instanceID); record["state"] != "in_progress" {
		t.Fatalf("the record of the update the broker couldn't settle is %v, want it still in progress", record)
	}

	var saved map[string]interface{}

	found, err := fixture.vault.Get(ctx, instanceID+"/update_rollback_manifest", &saved)
	if err != nil || !found {
		t.Fatalf("the saved manifest of the unsettled update is gone (%v), want it kept for re-adoption", err)
	}

	if records := fixture.updateRecords(ctx, t); len(records) != 0 {
		t.Fatalf("the change records are %+v, want none until the update settles", records)
	}

	fixture.requireReleased(t, instanceID)
}
