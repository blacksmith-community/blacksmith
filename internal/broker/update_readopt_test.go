package broker_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"blacksmith/internal/bosh"
	internalVault "blacksmith/internal/vault"

	"github.com/fivetwenty-io/osbapi/v2/pkg/osbapi"
)

// readoptUpdateContextID is the context ID of the update a restart
// interrupted.
const readoptUpdateContextID = "readopt-update-context"

// readoptUpdateActor is the platform user who sent the interrupted update.
const readoptUpdateActor = "cloudfoundry:user-3"

// readoptUpdateCase is one point at which a restart interrupts an update.
type readoptUpdateCase struct {
	name string
	// record writes what the update had recorded when the broker stopped,
	// over its accepted record.
	record    func(ctx context.Context, t *testing.T, fixture *updateFixture, instanceID string)
	found     *bosh.Task
	taskState string
	clock     func() time.Time
	held      bool
	want      osbapi.OperationState
	outcome   string
	taskID    int
}

// interruptedUpdate records an accepted update of instanceID that sets
// the policy to allkeys-lru, as the broker that stopped left it.
func interruptedUpdate(ctx context.Context, t *testing.T, fixture *updateFixture, instanceID string) {
	t.Helper()

	err := fixture.vault.TrackUpdateAccepted(ctx, instanceID, "Update request accepted", readoptUpdateContextID, openUpdatePlan+"-"+instanceID, internalVault.StagedUpdate{
		Fingerprint: "readopt-fingerprint", PreviousPlanID: openUpdatePlan, PlanID: openUpdatePlan,
		Parameters: map[string]interface{}{policyKey: policyLRU}, ChangedKeys: []string{policyKey}, Actor: readoptUpdateActor,
	})
	if err != nil {
		t.Fatalf("failed to record the accepted update: %v", err)
	}
}

// recordedDeployTask records the update's deploy task.
func recordedDeployTask(ctx context.Context, t *testing.T, fixture *updateFixture, instanceID string) {
	t.Helper()

	err := fixture.vault.TrackProgress(ctx, instanceID, updateFailureAction, "BOSH deployment in progress (task 77)", updateTaskID, nil)
	if err != nil {
		t.Fatalf("failed to record the deploy task: %v", err)
	}
}

// recordedLostAnswer records that the update's deploy answer was lost.
func recordedLostAnswer(ctx context.Context, t *testing.T, fixture *updateFixture, instanceID string) {
	t.Helper()

	err := fixture.vault.TrackOutcomeUnknown(ctx, instanceID, updateFailureAction, "The deploy's answer was lost", readoptUpdateContextID, time.Now())
	if err != nil {
		t.Fatalf("failed to record the lost answer: %v", err)
	}
}

// recordedNothingMore leaves the accepted record as it is, as a restart
// before the deploy's task was recorded does.
func recordedNothingMore(context.Context, *testing.T, *updateFixture, string) {}

// requireReadopted fails the spec unless the re-adopted update settled as
// the case wants with no deploy and no delete of its own, and left one
// change record.
func (c readoptUpdateCase) requireReadopted(ctx context.Context, t *testing.T, fixture *updateFixture, instanceID string, answer osbapi.LastOperationResponse) {
	t.Helper()

	if answer.State != c.want {
		t.Fatalf("the re-adopted update settled as %+v, want %s", answer, c.want)
	}

	if calls := fixture.director.Calls("UpdateDeployment"); len(calls) != 0 {
		t.Fatalf("re-adoption sent %d deploys, want none", len(calls))
	}

	record := readTaskRecord(ctx, t, fixture.vault, instanceID)
	if calls := fixture.director.Calls("DeleteDeployment"); len(calls) != 0 || record["action"] != updateFailureAction {
		t.Fatalf("re-adoption deleted %v or replaced the update's record: %v", calls, record)
	}

	for _, contextID := range fixture.director.Calls("FindTaskByContextID") {
		if contextID != readoptUpdateContextID {
			t.Fatalf("re-adoption looked the task up by context ID %q", contextID)
		}
	}

	records := fixture.updateRecords(ctx, t)
	if len(records) != 1 || records[0].Outcome != c.outcome || records[0].TaskID != c.taskID || records[0].Actor != readoptUpdateActor {
		t.Fatalf("the change records are %+v, want one %s update naming task %d and the user", records, c.outcome, c.taskID)
	}

	want := map[string]interface{}{policyKey: policyNoEvict}
	if c.want == osbapi.StateSucceeded {
		want = map[string]interface{}{policyKey: policyLRU}

		var stored map[string]interface{}

		_, err := fixture.vault.Get(ctx, instanceID+"/manifest", &stored)
		if err != nil || stored["manifest"] != liveManifest {
			t.Fatalf("the stored manifest is not the deployment's live one (%v)", err)
		}
	}

	root, _ := fixture.storedParameters(ctx, t, instanceID)
	if !reflect.DeepEqual(root, want) {
		t.Fatalf("the stored parameters are %v, want %v", root, want)
	}
}

// A restart in the middle of an update leaves its record in flight, and
// re-adoption takes the update's claim, follows its deploy task, resolves
// a lost answer by the update's context ID, and settles the update as its
// own goroutine would have. It never sends a deploy, never runs a
// deprovision's recovery, and deletes nothing. An update whose instance
// another operation holds at start is resumed once the instance is free.
func TestReadopt_ResumesUpdate(t *testing.T) {
	t.Parallel()

	running := &bosh.Task{ID: updateTaskID, State: outcomeTaskProcessing}

	for _, readoptCase := range []readoptUpdateCase{
		{"its deploy task succeeds", recordedDeployTask, nil, outcomeTaskDone, nil, false, osbapi.StateSucceeded, changeSucceeded, updateTaskID},
		{"its deploy task fails", recordedDeployTask, nil, outcomeTaskError, nil, false, osbapi.StateFailed, outcomeStateFailed, updateTaskID},
		{"its lost answer names a task", recordedLostAnswer, running, outcomeTaskDone, nil, false, osbapi.StateSucceeded, changeSucceeded, updateTaskID},
		{"it stopped before its task was recorded", recordedNothingMore, nil, outcomeTaskDone, pastGraceClock(), false, osbapi.StateFailed, outcomeStateFailed, 0},
		{"another operation holds the instance at start", recordedDeployTask, nil, outcomeTaskDone, nil, true, osbapi.StateSucceeded, changeSucceeded, updateTaskID},
	} {
		t.Run(readoptCase.name, func(t *testing.T) {
			t.Parallel()

			fixture := newUpdateFixture(t)
			ctx := context.Background()
			instanceID := fixture.provisioned(ctx, t, openUpdatePlan, map[string]interface{}{policyKey: policyNoEvict})

			interruptedUpdate(ctx, t, fixture, instanceID)
			readoptCase.record(ctx, t, fixture, instanceID)

			script := &deployScript{}
			script.script(fixture, readoptCase.taskState)

			lost := &lostAnswer{}
			lost.script(fixture, readoptCase.found, 0)

			if readoptCase.clock != nil {
				fixture.broker.SetUnknownOutcomeClock(readoptCase.clock)
			}

			release := func() {}
			if readoptCase.held {
				release = fixture.broker.HoldOperationClaim(instanceID, "upgrade")
			}

			err := fixture.broker.ReadoptInFlightOperations(ctx)
			if err != nil {
				t.Fatalf("re-adoption failed: %v", err)
			}

			if calls := fixture.director.Calls("WaitForTask"); readoptCase.held && len(calls) != 0 {
				t.Fatalf("re-adoption followed the task while another operation held the instance")
			}

			release()

			readoptCase.requireReadopted(ctx, t, fixture, instanceID, fixture.settled(ctx, t, instanceID))
		})
	}
}
