//nolint:testpackage // the tests stop the manager mid-upgrade and read its unexported tasks
package upgrade

import (
	"context"
	"fmt"
	"testing"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/pkg/logger"
)

const (
	// liveInstance and savedInstance name, in failures, the instance as the
	// manager holds it and as it was saved.
	liveInstance  = "instance"
	savedInstance = "saved instance"
)

// waited returns how many waits on a deploy task the director has seen.
func (d *waitDirector) waited() int {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.waits
}

// stoppingDirector is a waitDirector whose GetDeployment waits until its
// context ends, so the test can stop the manager while the upgrade reads
// the deployment.
type stoppingDirector struct {
	*waitDirector

	reading chan struct{}
}

func (d *stoppingDirector) GetDeployment(ctx context.Context, _ string) (*bosh.DeploymentDetail, error) {
	close(d.reading)
	<-ctx.Done()

	return nil, fmt.Errorf("get deployment: %w", ctx.Err())
}

// expectPending fails the test unless the instance is pending, as it was
// before the upgrade started it.
func expectPending(t *testing.T, what string, instance InstanceUpgrade) {
	t.Helper()

	if instance.Status != InstanceStatusPending || instance.StartedAt != nil || instance.ContextID != "" || instance.Error != "" {
		t.Errorf("expected the %s to be pending, as it was before it started, got %+v", what, instance)
	}
}

// expectNoCounts fails the test when any count of the upgrade changed.
func expectNoCounts(t *testing.T, task UpgradeTask) {
	t.Helper()

	if task.CompletedCount != 0 || task.FailedCount != 0 || task.CancelledCount != 0 || task.SkippedCount != 0 || task.UnknownCount != 0 {
		t.Fatalf("expected no count to change, got %+v", task)
	}
}

//nolint:paralleltest // MergeStemcellOverlay runs spruce, whose global state races across tests
func TestStopLeavesRemainingInstancesPending(t *testing.T) {
	director := newWaitDirector()
	vault := newWaitVault()

	manager, task, stop := startUpgrade(t, director, vault, waitInstanceID, secondInstanceID)

	stop()

	stopped := snapshot(manager, task)

	expectPending(t, "second instance", stopped.Instances[1])
	expectPending(t, "saved second instance", vault.savedTask(t, task.ID).Instances[1])
	expectNoCounts(t, stopped)
}

//nolint:paralleltest // MergeStemcellOverlay runs spruce, whose global state races across tests
func TestStopMarksTheWaitingInstanceInterrupted(t *testing.T) {
	director := newWaitDirector()
	vault := newWaitVault()

	manager, task, stop := startUpgrade(t, director, vault)

	stop()

	if cancels := director.cancelled(); len(cancels) != 0 {
		t.Fatalf("expected the shutdown to leave the task running, but it cancelled %v", cancels)
	}

	want := fmt.Sprintf("Blacksmith stopped waiting at shutdown; BOSH task %d is still running", waitDeployTaskID)

	for what, instance := range map[string]InstanceUpgrade{
		liveInstance:  snapshot(manager, task).Instances[0],
		savedInstance: vault.savedTask(t, task.ID).Instances[0],
	} {
		if instance.Status != InstanceStatusInterrupted || instance.Error != want {
			t.Errorf("expected the %s to be interrupted and say %q, got %+v", what, want, instance)
		}

		if instance.BOSHTaskID != waitDeployTaskID || instance.ContextID == "" || instance.CompletedAt != nil {
			t.Errorf("expected the %s to keep BOSH task %d and its context ID, got %+v", what, waitDeployTaskID, instance)
		}
	}

	expectNoCounts(t, snapshot(manager, task))
}

//nolint:paralleltest // MergeStemcellOverlay runs spruce, whose global state races across tests
func TestStopDuringTheLastInstanceLeavesTheUpgradeRunning(t *testing.T) {
	director := newWaitDirector()
	vault := newWaitVault()

	manager, task, stop := startUpgrade(t, director, vault)

	stop()

	for what, upgrade := range map[string]UpgradeTask{
		"upgrade":       snapshot(manager, task),
		"saved upgrade": vault.savedTask(t, task.ID),
	} {
		if upgrade.Status != TaskStatusRunning || upgrade.CompletedAt != nil {
			t.Errorf("expected the %s to stay running, got %+v", what, upgrade)
		}
	}
}

//nolint:paralleltest // MergeStemcellOverlay runs spruce, whose global state races across tests
func TestStopWhileWaitingForABatchSlotLeavesTheInstancePending(t *testing.T) {
	director := newWaitDirector()
	vault := newWaitVault()
	batch := newTestBatchDirector(t, director)

	// An adopted deploy holds the only slot, so the upgrade's deploy waits
	// for one.
	batch.Adopt(waitDeployTaskID)

	manager, task, stop := beginUpgrade(t, &logger.NoOpLogger{}, batch, vault)

	queued := time.Now().Add(waitTimeout)

	for {
		if _, waiters, _ := batch.GetBatchPoolStats(); waiters == 1 {
			break
		}

		if time.Now().After(queued) {
			t.Fatal("the upgrade's deploy never waited for a batch slot")
		}

		time.Sleep(time.Millisecond)
	}

	stop()

	stopped := snapshot(manager, task)

	expectPending(t, liveInstance, stopped.Instances[0])
	expectPending(t, savedInstance, vault.savedTask(t, task.ID).Instances[0])
	expectNoCounts(t, stopped)
}

//nolint:paralleltest // MergeStemcellOverlay runs spruce, whose global state races across tests
func TestStopDuringGetDeploymentLeavesTheInstancePending(t *testing.T) {
	director := &stoppingDirector{waitDirector: newWaitDirector(), reading: make(chan struct{})}
	vault := newWaitVault()

	manager, task, stop := beginUpgrade(t, &logger.NoOpLogger{}, director, vault)

	select {
	case <-director.reading:
	case <-time.After(waitTimeout):
		t.Fatal("the upgrade never read the deployment")
	}

	stop()

	stopped := snapshot(manager, task)

	expectPending(t, liveInstance, stopped.Instances[0])
	expectPending(t, savedInstance, vault.savedTask(t, task.ID).Instances[0])
	expectNoCounts(t, stopped)
}

//nolint:paralleltest // MergeStemcellOverlay runs spruce, whose global state races across tests
func TestStopEndsTheWaitAfterACancel(t *testing.T) {
	previous := cancelWaitTimeout
	cancelWaitTimeout = 10 * time.Second

	t.Cleanup(func() { cancelWaitTimeout = previous })

	director := newWaitDirector()
	vault := newWaitVault()

	manager, task, stop := startUpgrade(t, director, vault)
	endTheTaskAtCleanup(t, director)

	cancelUpgrade(t, manager, task)

	// The second wait on the task is the one after the cancel.
	waiting := time.Now().Add(waitTimeout)

	for director.waited() < 2 {
		if time.Now().After(waiting) {
			t.Fatal("the upgrade never waited for the task after the cancel")
		}

		time.Sleep(time.Millisecond)
	}

	stopped := make(chan struct{})

	go func() {
		defer close(stopped)

		stop()
	}()

	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("Stop waited for the wait after the cancel")
	}

	want := fmt.Sprintf("Blacksmith stopped waiting at shutdown after a cancel was requested for BOSH task %d", waitDeployTaskID)

	instance := snapshot(manager, task).Instances[0]
	if instance.Status != InstanceStatusInterrupted || instance.BOSHTaskID != waitDeployTaskID || instance.Error != want {
		t.Fatalf("expected the instance to be interrupted with BOSH task %d and say %q, got %+v", waitDeployTaskID, want, instance)
	}
}

//nolint:paralleltest // MergeStemcellOverlay runs spruce, whose global state races across tests
func TestStopDuringALostAnswerLookupNamesTheContextID(t *testing.T) {
	// The deploy loses its answer, and no lookup finds its task, so the
	// upgrade keeps looking by context ID until it is stopped.
	director := newLostDirector()
	director.lose = 1
	vault := newWaitVault()

	manager, task, stop := beginUpgrade(t, &logger.NoOpLogger{}, director, vault)

	looking := time.Now().Add(waitTimeout)

	for len(director.lookedUp()) == 0 {
		if time.Now().After(looking) {
			t.Fatal("the upgrade never looked for its task by context ID")
		}

		time.Sleep(time.Millisecond)
	}

	stop()

	contextID := director.contextIDs()[0]
	want := fmt.Sprintf("Blacksmith stopped looking for the deploy with context ID %s at shutdown", contextID)

	for what, instance := range map[string]InstanceUpgrade{
		liveInstance:  snapshot(manager, task).Instances[0],
		savedInstance: vault.savedTask(t, task.ID).Instances[0],
	} {
		if instance.Status != InstanceStatusInterrupted || instance.Error != want {
			t.Errorf("expected the %s to be interrupted and say %q, got %+v", what, want, instance)
		}

		if instance.ContextID != contextID || instance.UnknownSince == nil || instance.CompletedAt != nil {
			t.Errorf("expected the %s to keep context ID %s and the time the answer was lost, got %+v", what, contextID, instance)
		}
	}

	expectNoCounts(t, snapshot(manager, task))
}

func TestStopTwiceDoesNotPanic(t *testing.T) {
	t.Parallel()

	manager := NewManager(&logger.NoOpLogger{}, newWaitDirector(), newWaitVault())

	manager.Stop()

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("the second Stop panicked: %v", r)
		}
	}()

	manager.Stop()
}
