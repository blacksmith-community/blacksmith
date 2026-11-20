package broker_test

import (
	"context"
	"errors"
	"testing"

	"blacksmith/internal/bosh"
	"blacksmith/internal/broker"
	internalVault "blacksmith/internal/vault"

	"github.com/fivetwenty-io/osbapi/v2/pkg/osbapi"
)

// repairFailedAttention is what the Attention required entry of a failed
// repair says.
const repairFailedAttention = "didn't succeed"

// planGoneAttention is what the Attention required entry says when the
// plan has left the catalog.
const planGoneAttention = "an operator restores the plan"

// errLiveVersionsUnread is the director's answer when a repair update
// reads the live release and stemcell versions.
var errLiveVersionsUnread = errors.New("the director didn't answer")

// settledStagedLostBroker settles an update of instanceID whose staged
// values don't decode through re-adoption, and returns the next start's
// broker, which lists the instance under Attention required.
func settledStagedLostBroker(ctx context.Context, t *testing.T, fixture *updateFixture, instanceID string) *broker.Broker {
	t.Helper()

	interruptedUpdate(ctx, t, fixture, instanceID)
	recordedDeployTask(ctx, t, fixture, instanceID)
	savedRollbackManifest(ctx, t, fixture, instanceID)
	garbleStagedUpdate(ctx, t, fixture, instanceID)
	(&deployScript{}).script(fixture, outcomeTaskDone)

	err := fixture.broker.ReadoptInFlightOperations(ctx)
	if err != nil {
		t.Fatalf("re-adoption failed: %v", err)
	}

	fixture.settled(ctx, t, instanceID)

	next := restartedBroker(ctx, t, fixture, updatePlans())
	requireUpdateAttention(t, next, instanceID, stagedLostAttention, "before the repair")

	return next
}

// repairRequest repeats the stored parameters, so it changes nothing
// against the stored values.
func repairRequest() osbapi.UpdateRequest {
	return osbapi.UpdateRequest{ServiceID: recordedNameService, Parameters: map[string]any{policyKey: policyNoEvict}}
}

// sendRepair has next send repairRequest to instanceID, calls during, when
// it is set, while the repair runs, waits for the repair, and returns its
// last_operation answer.
func sendRepair(ctx context.Context, t *testing.T, next *broker.Broker, instanceID string, during func()) osbapi.LastOperationResponse {
	t.Helper()

	response, async, err := next.Update(ctx, instanceID, repairRequest(), true)
	requireAccepted(t, response, async, err)

	if during != nil {
		during()
	}

	next.WaitForBackground(ctx)

	answer, err := next.LastOperation(ctx, instanceID, osbapi.LastOperationRequest{Operation: updateFailureAction})
	if err != nil {
		t.Fatalf("the repair's poll failed: %v", err)
	}

	return answer
}

// A repair update that fails leaves the deployment as unknown as the lost
// update did, so the instance stays under Attention required, then and at
// the next start, and the next update with no effect deploys again. Only
// a repair that succeeds takes the instance off the list.
func TestUpdate_FailedRepairOfLostValues_StaysInRepair(t *testing.T) {
	t.Parallel()

	for _, failure := range []struct {
		name  string
		spoil func(ctx context.Context, t *testing.T, fixture *updateFixture) chan struct{}
	}{
		{"its live versions can't be read", func(_ context.Context, _ *testing.T, fixture *updateFixture) chan struct{} {
			(&deployScript{}).script(fixture, outcomeTaskDone)
			fixture.director.GetDeploymentFn = func(string) (*bosh.DeploymentDetail, error) { return nil, errLiveVersionsUnread }

			return nil
		}},
		{"its deploy fails", func(_ context.Context, _ *testing.T, fixture *updateFixture) chan struct{} {
			script := &deployScript{release: make(chan struct{})}
			script.script(fixture, outcomeTaskError)

			return script.release
		}},
		{"its deploy fails and the rollback restores the deployment", func(ctx context.Context, t *testing.T, fixture *updateFixture) chan struct{} {
			t.Helper()

			fixture.rollbackOn(ctx, t, true)
			(&rollbackScript{}).script(fixture, outcomeTaskDone)

			return nil
		}},
	} {
		t.Run(failure.name, func(t *testing.T) {
			t.Parallel()

			fixture := newUpdateFixture(t)
			ctx := context.Background()
			instanceID := fixture.provisioned(ctx, t, openUpdatePlan, lastGoodParams())
			next := settledStagedLostBroker(ctx, t, fixture, instanceID)

			release := failure.spoil(ctx, t, fixture)

			// While a held repair runs, the deployment may still run what
			// the lost update deployed, so the instance stays listed.
			during := func() {
				defer close(release)

				requireUpdateAttention(t, next, instanceID, stagedLostAttention, "while the repair runs")
			}
			if release == nil {
				during = nil
			}

			if answer := sendRepair(ctx, t, next, instanceID, during); answer.State != osbapi.StateFailed {
				t.Fatalf("the repair was answered %+v, want failed", answer)
			}

			requireUpdateAttention(t, next, instanceID, repairFailedAttention, "once the repair failed")
			requireUpdateAttention(t, startedBroker(ctx, t, fixture, updatePlans()), instanceID, repairFailedAttention, "at the start after the failed repair")

			// The next repair, with the director working, deploys again.
			script := &deployScript{}
			script.script(fixture, outcomeTaskDone)

			again := restartedBroker(ctx, t, fixture, updatePlans())
			if answer := sendRepair(ctx, t, again, instanceID, nil); answer.State != osbapi.StateSucceeded {
				t.Fatalf("the second repair was answered %+v, want succeeded", answer)
			}

			if sent := script.sent(); len(sent) != 1 {
				t.Fatalf("the second repair sent %d deploys, want one", len(sent))
			}

			requireUpdateAttention(t, again, instanceID, "", "once a repair succeeded")
			requireUpdateAttention(t, startedBroker(ctx, t, fixture, updatePlans()), instanceID, "", "at the start after the repair succeeded")
		})
	}
}

// recordedRepair records an accepted repair of lost values for
// instanceID, with the deploy task and the rollback manifest it had saved
// when the broker stopped.
func recordedRepair(ctx context.Context, t *testing.T, fixture *updateFixture, instanceID string) {
	t.Helper()

	err := fixture.vault.TrackUpdateAccepted(ctx, instanceID, "Update request accepted", "repair-context-id", openUpdatePlan+"-"+instanceID, internalVault.StagedUpdate{
		Fingerprint: "repair-fingerprint", PreviousPlanID: openUpdatePlan, PlanID: openUpdatePlan,
		Parameters: lastGoodParams(), Actor: readoptUpdateActor, RepairsLostValues: true,
	})
	if err != nil {
		t.Fatalf("failed to record the accepted repair: %v", err)
	}

	recordedDeployTask(ctx, t, fixture, instanceID)
	savedRollbackManifest(ctx, t, fixture, instanceID)
}

// A repair a restart interrupted is re-adopted as a repair, so when its
// deploy fails after the restart, the instance is listed under Attention
// required as soon as it settles.
func TestReadopt_InterruptedRepairFails_StaysInRepair(t *testing.T) {
	t.Parallel()

	fixture := newUpdateFixture(t)
	ctx := context.Background()
	instanceID := fixture.provisioned(ctx, t, openUpdatePlan, lastGoodParams())
	settledStagedLostBroker(ctx, t, fixture, instanceID)

	recordedRepair(ctx, t, fixture, instanceID)
	(&deployScript{}).script(fixture, outcomeTaskError)

	requireUpdateAttention(t, startedBroker(ctx, t, fixture, updatePlans()), instanceID, repairFailedAttention, "once the re-adopted repair failed")
}

// An update with no effect after lost values deploys, so a request that
// doesn't accept an asynchronous answer gets 422 AsyncRequired, sends no
// deploy, and leaves the instance listed under Attention required.
func TestUpdate_NoEffectAfterStagedLost_SyncRequestNeedsAsync(t *testing.T) {
	t.Parallel()

	fixture := newUpdateFixture(t)
	ctx := context.Background()
	instanceID := fixture.provisioned(ctx, t, openUpdatePlan, lastGoodParams())
	next := settledStagedLostBroker(ctx, t, fixture, instanceID)

	script := &deployScript{}
	script.script(fixture, outcomeTaskDone)

	response, async, err := next.Update(ctx, instanceID, repairRequest(), false)
	if !errors.Is(err, osbapi.ErrAsyncRequired) || async || response.Operation != "" {
		t.Fatalf("the synchronous update was answered %+v, async %v, %v, want 422 AsyncRequired", response, async, err)
	}

	if sent := script.sent(); len(sent) != 0 {
		t.Fatalf("the synchronous update sent %d deploys, want none", len(sent))
	}

	requireUpdateAttention(t, next, instanceID, stagedLostAttention, "after the synchronous update")
}

// A repair a restart interrupted, on a catalog that no longer holds its
// plan, settles as failed, and every update of the instance is refused
// until an operator restores the plan. Attention required then says to
// restore the plan, and not to send the update again, both once the
// repair settled and at every later start.
func TestReadopt_InterruptedRepairWithPlanGone_NamesThePlan(t *testing.T) {
	t.Parallel()

	fixture := newUpdateFixture(t)
	ctx := context.Background()
	instanceID := fixture.provisioned(ctx, t, openUpdatePlan, lastGoodParams())
	settledStagedLostBroker(ctx, t, fixture, instanceID)
	recordedRepair(ctx, t, fixture, instanceID)
	(&deployScript{}).script(fixture, outcomeTaskDone)

	plans := updatePlans()
	delete(plans, recordedNameService+"/"+openUpdatePlan)

	requireUpdateAttention(t, startedBroker(ctx, t, fixture, plans), instanceID, planGoneAttention, "once the re-adopted repair settled")
	requireUpdateAttention(t, startedBroker(ctx, t, fixture, plans), instanceID, planGoneAttention, "at the start after it settled")
}

// A repair a restart interrupted is listed under Attention required as
// soon as the broker starts, and not only once the repair settles,
// because until the repair succeeds the deployment may still run what
// the lost update deployed. A repair that then succeeds takes the
// instance off the list.
func TestReadopt_InterruptedRepairRunning_ListedAtOnce(t *testing.T) {
	t.Parallel()

	fixture := newUpdateFixture(t)
	ctx := context.Background()
	instanceID := fixture.provisioned(ctx, t, openUpdatePlan, lastGoodParams())
	settledStagedLostBroker(ctx, t, fixture, instanceID)
	recordedRepair(ctx, t, fixture, instanceID)

	script := &deployScript{release: make(chan struct{})}
	script.script(fixture, outcomeTaskDone)

	released := false
	release := func() {
		if !released {
			released = true

			close(script.release)
		}
	}

	next := restartedBroker(ctx, t, fixture, updatePlans())

	// The cleanup runs before the broker's, so its work can end.
	t.Cleanup(release)

	requireUpdateAttention(t, next, instanceID, repairFailedAttention, "while the re-adopted repair runs")

	release()
	next.WaitForBackground(ctx)

	requireUpdateAttention(t, next, instanceID, "", "once the re-adopted repair succeeded")
}
