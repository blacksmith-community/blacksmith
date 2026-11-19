package broker_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/broker"
	"blacksmith/pkg/testutil"

	"github.com/fivetwenty-io/osbapi/v2/pkg/osbapi"
)

// followBound is the short bound the follow specs set, which the update's
// BOSH task outlives.
const followBound = 20 * time.Millisecond

// heldTaskDirector holds the wait on one BOSH task until release is
// closed, and ends the wait early when the caller's context ends, as the
// director adapter does. Every other wait goes straight to the scripted
// director.
type heldTaskDirector struct {
	*testutil.ScriptedBOSHDirector

	taskID  int
	release chan struct{}
	waiting chan struct{}
	once    sync.Once
}

// holdTask points the fixture's broker at a director that holds the wait
// on taskID.
func (f *updateFixture) holdTask(taskID int) *heldTaskDirector {
	held := &heldTaskDirector{ScriptedBOSHDirector: f.director, taskID: taskID, release: make(chan struct{}), waiting: make(chan struct{})}
	f.broker.BOSH = held

	return held
}

// WaitForTask holds the wait on the held task.
func (d *heldTaskDirector) WaitForTask(ctx context.Context, taskID int) (*bosh.Task, error) {
	if taskID == d.taskID {
		d.once.Do(func() { close(d.waiting) })

		select {
		case <-d.release:
		case <-ctx.Done():
			return nil, fmt.Errorf("stopped waiting for BOSH task %d: %w", taskID, ctx.Err())
		}
	}

	task, err := d.ScriptedBOSHDirector.WaitForTask(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("wait for BOSH task %d: %w", taskID, err)
	}

	return task, nil
}

// requireHeldPastBound waits for the held task's wait to start and then
// well past the bound, and fails the spec unless the update still holds
// its claim and still answers in progress.
func (f *updateFixture) requireHeldPastBound(ctx context.Context, t *testing.T, held *heldTaskDirector, instanceID string) {
	t.Helper()

	select {
	case <-held.waiting:
	case <-time.After(10 * time.Second):
		t.Fatalf("BOSH task %d was never followed", held.taskID)
	}

	time.Sleep(10 * followBound)

	if !f.broker.OperationClaimed(instanceID) {
		t.Fatalf("the claim was released while BOSH task %d still ran", held.taskID)
	}

	answer, err := f.broker.LastOperation(ctx, instanceID, osbapi.LastOperationRequest{Operation: updateFailureAction})
	if err != nil || answer.State != osbapi.StateInProgress {
		t.Fatalf("while BOSH task %d still ran, the poll answered %+v, %v, want in progress", held.taskID, answer, err)
	}
}

// requireReleased fails the spec while the instance's claim is held.
func (f *updateFixture) requireReleased(t *testing.T, instanceID string) {
	t.Helper()

	if f.broker.OperationClaimed(instanceID) {
		t.Fatal("the settled update still holds its claim")
	}
}

// An update's deploy task that runs past the bound on the steps before
// the deploy keeps the claim, and the update settles once the task ends.
func TestUpdate_TaskOutlivesBound_HoldsClaimUntilSettled(t *testing.T) {
	t.Parallel()

	fixture := newUpdateFixture(t)
	ctx := context.Background()

	fixture.broker.SetUpdatePrepareBound(followBound)

	script := &deployScript{}
	script.script(fixture, outcomeTaskDone)

	held := fixture.holdTask(updateTaskID)
	instanceID := fixture.provisioned(ctx, t, openUpdatePlan, lastGoodParams())

	response, async, err := fixture.broker.Update(ctx, instanceID, failingUpdateRequest(), true)
	requireAccepted(t, response, async, err)

	fixture.requireHeldPastBound(ctx, t, held, instanceID)
	close(held.release)

	answer := fixture.settled(ctx, t, instanceID)
	if answer.State != osbapi.StateSucceeded {
		t.Fatalf("the update whose task outlived the bound settled as %+v, want succeeded", answer)
	}

	fixture.requireReleased(t, instanceID)
}

// A rollback task that runs past the bound keeps the claim too, and the
// rollback settles once its task ends.
func TestUpdate_RollbackOutlivesBound_HoldsClaimUntilSettled(t *testing.T) {
	t.Parallel()

	fixture := newUpdateFixture(t)
	ctx := context.Background()

	fixture.broker.SetUpdatePrepareBound(followBound)
	fixture.rollbackOn(ctx, t, true)

	script := &rollbackScript{}
	script.script(fixture, outcomeTaskDone)

	held := fixture.holdTask(rollbackTaskID)
	instanceID := fixture.startFailingUpdate(ctx, t)

	fixture.requireHeldPastBound(ctx, t, held, instanceID)
	close(held.release)

	answer := fixture.settled(ctx, t, instanceID)
	if answer.State != osbapi.StateFailed || !strings.Contains(answer.Description, "restored") {
		t.Fatalf("the rollback whose task outlived the bound settled as %+v, want failed with the deployment restored", answer)
	}

	fixture.requireReleased(t, instanceID)
}

// A re-adopted update whose task runs past the re-adoption bound keeps
// the claim, and settles once the task ends.
//
//nolint:paralleltest // it sets the re-adoption bound, which every re-adoption reads
func TestReadopt_UpdateOutlivesBound_HoldsClaimUntilSettled(t *testing.T) {
	defer broker.SetReadoptBound(followBound)()
	defer broker.SetReadoptIntervals(followBound)()

	fixture := newUpdateFixture(t)
	ctx := context.Background()
	instanceID := fixture.provisioned(ctx, t, openUpdatePlan, lastGoodParams())

	interruptedUpdate(ctx, t, fixture, instanceID)
	recordedDeployTask(ctx, t, fixture, instanceID)

	script := &deployScript{}
	script.script(fixture, outcomeTaskDone)

	held := fixture.holdTask(updateTaskID)

	err := fixture.broker.ReadoptInFlightOperations(ctx)
	if err != nil {
		t.Fatalf("re-adoption failed: %v", err)
	}

	fixture.requireHeldPastBound(ctx, t, held, instanceID)
	close(held.release)

	answer := fixture.settled(ctx, t, instanceID)
	if answer.State != osbapi.StateSucceeded {
		t.Fatalf("the re-adopted update whose task outlived the bound settled as %+v, want succeeded", answer)
	}

	fixture.requireReleased(t, instanceID)
}

// A deploy task the director no longer knows fails the update as
// repeatable, with the instance usable, instead of following it forever.
func TestUpdate_PrunedTask_FailsRepeatable(t *testing.T) {
	t.Parallel()

	fixture := newUpdateFixture(t)
	ctx := context.Background()

	script := &deployScript{}
	script.script(fixture, outcomeTaskDone)

	fixture.director.WaitForTaskFn = func(taskID int) (*bosh.Task, error) { return nil, readoptTaskNotFound(taskID) }

	instanceID := fixture.provisioned(ctx, t, openUpdatePlan, lastGoodParams())

	response, async, err := fixture.broker.Update(ctx, instanceID, failingUpdateRequest(), true)
	requireAccepted(t, response, async, err)

	answer := fixture.settled(ctx, t, instanceID)
	if answer.State != osbapi.StateFailed || answer.InstanceUsable == nil || !*answer.InstanceUsable ||
		answer.UpdateRepeatable == nil || !*answer.UpdateRepeatable || !strings.Contains(answer.Description, "no longer known") {
		t.Fatalf("the update whose task was pruned settled as %+v, want failed, usable, and repeatable", answer)
	}

	requireLastGood(ctx, t, fixture, instanceID, lastGoodParams(), answer)
	fixture.requireNoSavedManifest(ctx, t, instanceID)
	fixture.requireReleased(t, instanceID)
}

// A director that can't be reached while the update follows its task
// keeps the claim, and the update settles once the director answers.
func TestUpdate_UnreachableDirector_HoldsClaimUntilSettled(t *testing.T) {
	t.Parallel()

	fixture := newUpdateFixture(t)
	ctx := context.Background()

	fixture.broker.SetUpdatePrepareBound(followBound)
	fixture.broker.SetUpdateTaskWait(followBound)

	script := &deployScript{}
	script.script(fixture, outcomeTaskDone)

	reachable := make(chan struct{})
	failed := make(chan struct{}, 1)
	fixture.director.WaitForTaskFn = func(taskID int) (*bosh.Task, error) {
		select {
		case <-reachable:
			return &bosh.Task{ID: taskID, State: outcomeTaskDone}, nil
		default:
		}

		select {
		case failed <- struct{}{}:
		default:
		}

		return nil, fmt.Errorf("read BOSH task %d: %w", taskID, errDirectorUnreachable)
	}

	instanceID := fixture.provisioned(ctx, t, openUpdatePlan, lastGoodParams())

	response, async, err := fixture.broker.Update(ctx, instanceID, failingUpdateRequest(), true)
	requireAccepted(t, response, async, err)

	<-failed
	time.Sleep(10 * followBound)

	if !fixture.broker.OperationClaimed(instanceID) {
		t.Fatal("the claim was released while the director could not be reached")
	}

	close(reachable)

	answer := fixture.settled(ctx, t, instanceID)
	if answer.State != osbapi.StateSucceeded {
		t.Fatalf("the update settled as %+v once the director answered, want succeeded", answer)
	}

	fixture.requireReleased(t, instanceID)
}
