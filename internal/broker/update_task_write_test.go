package broker_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/pkg/logger"
	"blacksmith/pkg/testutil"

	"github.com/fivetwenty-io/osbapi/v2/pkg/osbapi"
)

// recordInProgress is the state of a record whose update hasn't settled.
const recordInProgress = "in_progress"

// failTaskWriteAtDeploy fails the first write of the instance's task
// record after the director takes the update's deploy, which is the write
// that records the deploy's task.
func failTaskWriteAtDeploy(fixture *updateFixture, proxy *testutil.VaultFaultProxy, instanceID string) {
	deploy := fixture.director.UpdateDeploymentFn
	armed := false

	fixture.director.UpdateDeploymentFn = func(deployment, manifest string) (*bosh.Task, error) {
		if !armed {
			armed = true

			failSettleWrites(proxy, instanceID, 1)
		}

		return deploy(deployment, manifest)
	}
}

// A failed write of the deploy's task is tried again, so the record names
// the task while it runs, and the update still settles as succeeded on
// its record and leaves one change record.
func TestUpdate_TaskWriteFails_StillSettlesSucceeded(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fixture, proxy := faultedUpdateFixture(t)

	script := &deployScript{}
	script.script(fixture, outcomeTaskDone)

	held := fixture.holdTask(updateTaskID)
	instanceID := fixture.provisioned(ctx, t, openUpdatePlan, lastGoodParams())
	failTaskWriteAtDeploy(fixture, proxy, instanceID)

	response, async, err := fixture.broker.Update(ctx, instanceID, failingUpdateRequest(), true)
	requireAccepted(t, response, async, err)

	held.waitFollowed(t)

	if record := readTaskRecord(ctx, t, fixture.vault, instanceID); fmt.Sprint(record["task"]) != "77" {
		t.Fatalf("while its task runs, the update's record is %v, want it to name task 77", record)
	}

	close(held.release)

	if answer := fixture.settled(ctx, t, instanceID); answer.State != osbapi.StateSucceeded {
		t.Fatalf("the update settled as %+v, want succeeded", answer)
	}

	fixture.requireOneChangeRecord(ctx, t, changeSucceeded)
	fixture.requireNoSavedManifest(ctx, t, instanceID)
	fixture.requireReleased(t, instanceID)
}

// A failed write of the deploy's task is tried again, so a failed deploy
// with update_rollback on still runs its one rollback and settles it.
func TestUpdate_TaskWriteFails_StillRollsBack(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fixture, proxy := faultedUpdateFixture(t)
	fixture.rollbackOn(ctx, t, true)

	script := &rollbackScript{}
	script.script(fixture, outcomeTaskDone)

	instanceID := fixture.provisioned(ctx, t, openUpdatePlan, lastGoodParams())
	failTaskWriteAtDeploy(fixture, proxy, instanceID)

	response, async, err := fixture.broker.Update(ctx, instanceID, failingUpdateRequest(), true)
	requireAccepted(t, response, async, err)

	if answer := fixture.settled(ctx, t, instanceID); answer.State != osbapi.StateFailed {
		t.Fatalf("the update settled as %+v, want failed", answer)
	}

	if sent := script.sent(); len(sent) != 2 {
		t.Fatalf("the update sent %d deploys, want its own and the rollback", len(sent))
	}

	fixture.requireOneChangeRecord(ctx, t, changeRolledBack)
	fixture.requireNoSavedManifest(ctx, t, instanceID)
	fixture.requireReleased(t, instanceID)
}

// A settle write that lands while its answer is lost is refused when it
// is tried again, because the record already holds it. The broker counts
// it as landed and never logs that the update went unrecorded.
//
//nolint:paralleltest // the test swaps the process-wide logger
func TestUpdate_SettleWriteLandsWithLostAnswer_CountsAsLanded(t *testing.T) {
	capture := newCaptureLogger()
	previous := logger.Get()

	logger.Set(capture)
	t.Cleanup(func() { logger.Set(previous) })

	for name, rollbackState := range map[string]string{outcomeStateFailed: "", changeRollbackFail: outcomeTaskError} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			fixture, proxy := faultedUpdateFixture(t)

			instanceID := fixture.provisioned(ctx, t, openUpdatePlan, lastGoodParams())
			outcome := outcomeStateFailed

			if rollbackState == "" {
				script := &deployScript{}
				script.script(fixture, outcomeTaskDone)

				fixture.director.UpdateDeploymentFn = func(string, string) (*bosh.Task, error) {
					proxy.FailWritesAfterApplying(instanceID+"/task", http.StatusBadRequest, "the answer was lost", 1)

					return nil, errDeployRefused
				}
			} else {
				fixture.rollbackOn(ctx, t, true)

				script := &rollbackScript{hold: make(chan struct{}), holding: make(chan struct{})}
				script.script(fixture, rollbackState)

				go func() {
					<-script.holding
					proxy.FailWritesAfterApplying(instanceID+"/task", http.StatusBadRequest, "the answer was lost", 1)
					close(script.hold)
				}()

				outcome = changeRollbackFail
			}

			response, async, err := fixture.broker.Update(ctx, instanceID, failingUpdateRequest(), true)
			requireAccepted(t, response, async, err)

			if answer := fixture.settled(ctx, t, instanceID); answer.State != osbapi.StateFailed {
				t.Fatalf("the update settled as %+v, want failed", answer)
			}

			fixture.requireOneChangeRecord(ctx, t, outcome)
			fixture.requireReleased(t, instanceID)
		})
	}

	if output := capture.output(); strings.Contains(output, "CRITICAL") {
		t.Fatalf("a settle write that landed was logged as unrecorded:\n%s", output)
	}
}

// A rollback write that lands while its answer is lost is refused when it
// is tried again, because the record already names the rollback, and the
// rollback goes ahead once.
func TestUpdate_RollbackWriteLandsWithLostAnswer_StillRollsBack(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fixture, proxy := faultedUpdateFixture(t)
	fixture.rollbackOn(ctx, t, true)

	script := &rollbackScript{}
	script.script(fixture, outcomeTaskDone)

	instanceID := fixture.provisioned(ctx, t, openUpdatePlan, lastGoodParams())

	wait := fixture.director.WaitForTaskFn
	fixture.director.WaitForTaskFn = func(taskID int) (*bosh.Task, error) {
		if taskID == updateTaskID {
			proxy.FailWritesAfterApplying(instanceID+"/task", http.StatusBadRequest, "the answer was lost", 1)
		}

		return wait(taskID)
	}

	response, async, err := fixture.broker.Update(ctx, instanceID, failingUpdateRequest(), true)
	requireAccepted(t, response, async, err)

	if answer := fixture.settled(ctx, t, instanceID); answer.State != osbapi.StateFailed {
		t.Fatalf("the update settled as %+v, want failed", answer)
	}

	if sent := script.sent(); len(sent) != 2 {
		t.Fatalf("the update sent %d deploys, want its own and the rollback", len(sent))
	}

	fixture.requireOneChangeRecord(ctx, t, changeRolledBack)
	fixture.requireReleased(t, instanceID)
}

// dropRecordedTask writes the update's record back to naming no task,
// with its context ID and staged values kept, as a write that lost the
// task would leave it.
func dropRecordedTask(ctx context.Context, t *testing.T, fixture *updateFixture, instanceID string) {
	t.Helper()

	err := fixture.vault.TrackProgress(ctx, instanceID, updateFailureAction, "Deploying the update", 0, nil)
	if err != nil {
		t.Errorf("failed to drop the recorded task: %v", err)
	}
}

// awaitReleased waits until the update gives its claim up, and fails the
// spec when it still holds it after the limit. The broker shuts down at
// the end of the spec, so a settle that never lands ends with it.
func (f *updateFixture) awaitReleased(t *testing.T, instanceID string) {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for f.broker.OperationClaimed(instanceID) {
		if time.Now().After(deadline) {
			t.Fatal("the update still holds its claim, so its settle never landed")
		}

		time.Sleep(followBound)
	}
}

// A record that names no task once the deploy's task ends still holds the
// update under its context ID, so it hasn't moved on. The broker records
// the task again and settles the update, and it runs the rollback when
// the setting is on.
func TestUpdate_RecordLosesTask_SettlesUnderItsContextID(t *testing.T) {
	t.Parallel()

	for name, rollback := range map[string]bool{changeSucceeded: false, changeRolledBack: true} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			fixture := newUpdateFixture(t)

			service, shutdown := context.WithCancel(ctx)

			t.Cleanup(func() {
				shutdown()
				fixture.broker.WaitForBackground(ctx)
			})

			fixture.broker.ServiceContext = service
			fixture.broker.SetUpdateTaskWait(followBound)

			var sent func() []string

			if rollback {
				fixture.rollbackOn(ctx, t, true)

				script := &rollbackScript{}
				script.script(fixture, outcomeTaskDone)
				sent = script.sent
			} else {
				script := &deployScript{}
				script.script(fixture, outcomeTaskDone)
				sent = script.sent
			}

			instanceID := fixture.provisioned(ctx, t, openUpdatePlan, lastGoodParams())

			wait := fixture.director.WaitForTaskFn
			fixture.director.WaitForTaskFn = func(taskID int) (*bosh.Task, error) {
				if taskID == updateTaskID {
					dropRecordedTask(ctx, t, fixture, instanceID)
				}

				return wait(taskID)
			}

			response, async, err := fixture.broker.Update(ctx, instanceID, failingUpdateRequest(), true)
			requireAccepted(t, response, async, err)

			fixture.awaitReleased(t, instanceID)

			if record := readTaskRecord(ctx, t, fixture.vault, instanceID); record["state"] == recordInProgress {
				t.Fatalf("the update's record is %v, want it settled", record)
			}

			if want := map[bool]int{false: 1, true: 2}[rollback]; len(sent()) != want {
				t.Fatalf("the update sent %d deploys, want %d", len(sent()), want)
			}

			fixture.requireOneChangeRecord(ctx, t, name)
		})
	}
}
