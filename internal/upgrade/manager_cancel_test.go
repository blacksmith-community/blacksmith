package upgrade

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/pkg/logger"
)

// cancelUpgrade cancels the upgrade task through the manager, as the
// upgrade API does, and fails the test when the cancel is refused.
func cancelUpgrade(t *testing.T, manager *Manager, task *UpgradeTask) {
	t.Helper()

	if err := manager.CancelTask(context.Background(), task.ID); err != nil {
		t.Fatalf("the cancel was refused: %v", err)
	}
}

//nolint:paralleltest // MergeStemcellOverlay runs spruce, whose global state races across tests
func TestUpgradeCancelCancelsTheRecordedTask(t *testing.T) {
	director := newWaitDirector()
	manager, task, _ := startUpgrade(t, director, newWaitVault())

	cancelUpgrade(t, manager, task)

	director.finish <- &bosh.Task{ID: waitDeployTaskID, State: boshTaskCancelled}

	done := eventually(t, manager, task, "the instance's settling", func(current UpgradeTask) bool {
		return current.Instances[0].Status != InstanceStatusRunning
	})

	// Give a second cancel, were one coming, time to arrive.
	time.Sleep(50 * time.Millisecond)

	if cancels := director.cancelled(); len(cancels) != 1 || cancels[0] != waitDeployTaskID {
		t.Fatalf("expected exactly one cancel of BOSH task %d, got %v", waitDeployTaskID, cancels)
	}

	if done.Instances[0].Status != InstanceStatusCancelled || done.CancelledCount != 1 || done.FailedCount != 0 {
		t.Fatalf("expected one cancelled instance and no failed one, got %+v", done)
	}

	if done.Status != TaskStatusCancelled {
		t.Errorf("expected the upgrade to stay cancelled, got %s", done.Status)
	}
}

//nolint:paralleltest // MergeStemcellOverlay runs spruce, whose global state races across tests
func TestUpgradeCancelReleasesTheLockBeforeCallingTheDirector(t *testing.T) {
	director := newWaitDirector()
	heldLock := make(chan bool, 1)

	var manager *Manager

	director.onCancel = func(_ context.Context, _ int) error {
		read := make(chan struct{})

		go func() {
			defer close(read)

			_ = manager.ListTasks()
		}()

		select {
		case <-read:
			heldLock <- false
		case <-time.After(waitTimeout):
			heldLock <- true
		}

		return nil
	}

	manager, task, _ := startUpgrade(t, director, newWaitVault())

	cancelUpgrade(t, manager, task)

	select {
	case held := <-heldLock:
		if held {
			t.Fatal("CancelTask held the manager's lock while it called the director")
		}
	default:
		t.Fatal("CancelTask never called the director")
	}

	director.finish <- &bosh.Task{ID: waitDeployTaskID, State: boshTaskCancelled}

	eventually(t, manager, task, "the instance's settling", func(current UpgradeTask) bool {
		return current.Instances[0].Status == InstanceStatusCancelled
	})
}

//nolint:paralleltest // MergeStemcellOverlay runs spruce, whose global state races across tests
func TestUpgradeCancelTakesTheTasksOwnResultWhenItEndsAnotherWay(t *testing.T) {
	director := newWaitDirector()
	manager, task, _ := startUpgrade(t, director, newWaitVault())

	cancelUpgrade(t, manager, task)

	director.finish <- &bosh.Task{ID: waitDeployTaskID, State: boshTaskDone}

	done := eventually(t, manager, task, "the instance's settling", func(current UpgradeTask) bool {
		return current.Instances[0].Status != InstanceStatusRunning
	})

	if done.Instances[0].Status != InstanceStatusSuccess || done.CompletedCount != 1 || done.CancelledCount != 0 || done.FailedCount != 0 {
		t.Fatalf("expected the instance to take the task's own result, success, got %+v", done)
	}
}

//nolint:paralleltest // it shortens the wait after a cancel, a package variable
func TestUpgradeCancelLeavesTheInstanceRunningWhenTheTaskOutlivesTheDeadline(t *testing.T) {
	previous := cancelWaitTimeout
	cancelWaitTimeout = 100 * time.Millisecond

	t.Cleanup(func() { cancelWaitTimeout = previous })

	director := newWaitDirector()
	vault := newWaitVault()
	manager, task, _ := startUpgrade(t, director, vault)

	cancelUpgrade(t, manager, task)

	want := fmt.Sprintf("cancel requested for BOSH task %d", waitDeployTaskID)

	settled := eventually(t, manager, task, "the message after the deadline", func(current UpgradeTask) bool {
		return current.Instances[0].Error == want
	})

	if settled.Instances[0].Status != InstanceStatusRunning {
		t.Fatalf("expected the instance to stay running, got %+v", settled.Instances[0])
	}

	if settled.CancelledCount != 0 || settled.FailedCount != 0 || settled.CompletedCount != 0 {
		t.Fatalf("expected no count to change, got %+v", settled)
	}

	saved := eventuallySaved(t, vault, task.ID, func(saved UpgradeTask) bool { return saved.Instances[0].Error == want })
	if saved.Instances[0].Status != InstanceStatusRunning {
		t.Fatalf("expected the saved instance to stay running, got %+v", saved.Instances[0])
	}
}

//nolint:paralleltest // MergeStemcellOverlay runs spruce, whose global state races across tests
func TestUpgradeCancelSkipsThePendingInstances(t *testing.T) {
	director := newWaitDirector()
	manager, task, _ := startUpgrade(t, director, newWaitVault(), waitInstanceID, "instance-2")

	cancelUpgrade(t, manager, task)

	if pending := snapshot(manager, task).Instances[1]; pending.Status != InstanceStatusPending {
		t.Fatalf("expected the cancel to leave the pending instance to the upgrade, got %+v", pending)
	}

	director.finish <- &bosh.Task{ID: waitDeployTaskID, State: boshTaskCancelled}

	done := eventually(t, manager, task, "the skipping of the pending instance", func(current UpgradeTask) bool {
		return current.Instances[1].Status != InstanceStatusPending
	})

	if done.Instances[1].Status != InstanceStatusSkipped || done.SkippedCount != 1 {
		t.Fatalf("expected the pending instance to be skipped, got %+v", done)
	}

	if done.Instances[0].Status != InstanceStatusCancelled || done.CancelledCount != 1 {
		t.Fatalf("expected the running instance to be cancelled, got %+v", done)
	}
}

// endTheTaskAtCleanup ends the deploy task as cancelled when the test
// ends, should the test stop before it does, so the upgrade's wait after
// the cancel ends and the manager can stop.
func endTheTaskAtCleanup(t *testing.T, director *waitDirector) {
	t.Helper()

	t.Cleanup(func() {
		select {
		case director.finish <- &bosh.Task{ID: waitDeployTaskID, State: boshTaskCancelled}:
		default:
		}
	})
}

// eventuallySaved waits until the saved upgrade task satisfies done.
func eventuallySaved(t *testing.T, vault *waitVault, taskID string, done func(UpgradeTask) bool) UpgradeTask {
	t.Helper()

	deadline := time.Now().Add(waitTimeout)

	for {
		saved := vault.savedTask(t, taskID)
		if done(saved) {
			return saved
		}

		if time.Now().After(deadline) {
			t.Fatalf("the saved upgrade task never matched; it is %+v", saved)
		}

		time.Sleep(5 * time.Millisecond)
	}
}

//nolint:paralleltest // MergeStemcellOverlay runs spruce, whose global state races across tests
func TestUpgradeCancelBeforeTheTaskIDIsStoredCancelsTheTask(t *testing.T) {
	director := newWaitDirector()
	director.updateStarted = make(chan struct{})
	director.updateGate = make(chan struct{})

	manager, task, _ := beginUpgrade(t, &logger.NoOpLogger{}, director, newWaitVault())
	endTheTaskAtCleanup(t, director)

	select {
	case <-director.updateStarted:
	case <-time.After(waitTimeout):
		t.Fatal("the upgrade never sent its deploy")
	}

	// The instance is running, but the deploy has not named its task yet.
	cancelUpgrade(t, manager, task)

	if cancels := director.cancelled(); len(cancels) != 0 {
		t.Fatalf("expected no cancel before the task was named, got %v", cancels)
	}

	close(director.updateGate)

	select {
	case <-director.waitStarted:
	case <-time.After(waitTimeout):
		t.Fatal("the upgrade never waited on its deploy task")
	}

	if cancels := director.cancelled(); len(cancels) != 1 || cancels[0] != waitDeployTaskID {
		t.Fatalf("expected exactly one cancel of BOSH task %d once the deploy named it, got %v", waitDeployTaskID, cancels)
	}

	director.finish <- &bosh.Task{ID: waitDeployTaskID, State: boshTaskCancelled}

	done := eventually(t, manager, task, "the instance's settling", func(current UpgradeTask) bool {
		return current.Instances[0].Status != InstanceStatusRunning
	})

	if cancels := director.cancelled(); len(cancels) != 1 {
		t.Fatalf("expected exactly one cancel of BOSH task %d, got %v", waitDeployTaskID, cancels)
	}

	if done.Instances[0].Status != InstanceStatusCancelled || done.CancelledCount != 1 || done.FailedCount != 0 {
		t.Fatalf("expected the instance to settle as cancelled from the task's final state, got %+v", done)
	}
}

//nolint:paralleltest // MergeStemcellOverlay runs spruce, whose global state races across tests
func TestUpgradeCancelAfterTheTaskIDIsStoredCancelsOnce(t *testing.T) {
	director := newWaitDirector()
	vault := newWaitVault()

	stored := make(chan struct{})
	release := make(chan struct{})

	var storedOnce sync.Once

	// The first save that holds the task ID stops until the test has
	// cancelled, so the cancel lands after the ID is stored and before the
	// upgrade waits on the task.
	vault.onPut = func(path string, raw []byte) {
		if !strings.HasPrefix(path, upgradeTasksVaultPath+"/") || !strings.Contains(string(raw), fmt.Sprintf(`"bosh_task_id":%d`, waitDeployTaskID)) {
			return
		}

		first := false

		storedOnce.Do(func() { first = true })

		if first {
			close(stored)
			<-release
		}
	}

	manager, task, _ := beginUpgrade(t, &logger.NoOpLogger{}, director, vault)
	endTheTaskAtCleanup(t, director)

	select {
	case <-stored:
	case <-time.After(waitTimeout):
		t.Fatal("the upgrade never saved the task ID")
	}

	cancelUpgrade(t, manager, task)
	close(release)

	select {
	case <-director.waitStarted:
	case <-time.After(waitTimeout):
		t.Fatal("the upgrade never waited on its deploy task")
	}

	director.finish <- &bosh.Task{ID: waitDeployTaskID, State: boshTaskCancelled}

	done := eventually(t, manager, task, "the instance's settling", func(current UpgradeTask) bool {
		return current.Instances[0].Status != InstanceStatusRunning
	})

	if cancels := director.cancelled(); len(cancels) != 1 || cancels[0] != waitDeployTaskID {
		t.Fatalf("expected exactly one cancel of BOSH task %d, got %v", waitDeployTaskID, cancels)
	}

	if done.Instances[0].Status != InstanceStatusCancelled || done.CancelledCount != 1 {
		t.Fatalf("expected the instance to be cancelled, got %+v", done)
	}
}

// gateLogger holds the upgrade at the line that announces an instance's
// start, for the instance it names, until release closes. The upgrade
// logs that line before it checks for a cancel, so the test can cancel
// as the instance is about to start.
type gateLogger struct {
	*logger.NoOpLogger

	instanceID string
	reached    chan struct{}
	release    chan struct{}
	once       sync.Once
}

func (l *gateLogger) Named(string) logger.Logger { return l }

func (l *gateLogger) Info(msg string, args ...interface{}) {
	if !strings.HasPrefix(msg, "Upgrading instance") || len(args) == 0 || args[0] != l.instanceID {
		return
	}

	l.once.Do(func() {
		close(l.reached)
		<-l.release
	})
}

//nolint:paralleltest // MergeStemcellOverlay runs spruce, whose global state races across tests
func TestUpgradeCancelBeforeTheLastInstanceStartsSkipsIt(t *testing.T) {
	const lastInstanceID = "instance-2"

	director := newWaitDirector()
	gate := &gateLogger{
		NoOpLogger: &logger.NoOpLogger{},
		instanceID: lastInstanceID,
		reached:    make(chan struct{}),
		release:    make(chan struct{}),
	}

	manager, task, _ := beginUpgrade(t, gate, director, newWaitVault(), waitInstanceID, lastInstanceID)

	select {
	case <-director.waitStarted:
	case <-time.After(waitTimeout):
		t.Fatal("the upgrade never waited on its first deploy task")
	}

	director.finish <- &bosh.Task{ID: waitDeployTaskID, State: boshTaskDone}

	select {
	case <-gate.reached:
	case <-time.After(waitTimeout):
		t.Fatal("the upgrade never reached the last instance")
	}

	cancelUpgrade(t, manager, task)
	close(gate.release)

	done := eventually(t, manager, task, "the last instance's settling", func(current UpgradeTask) bool {
		return current.Instances[1].Status != InstanceStatusPending
	})

	if done.Instances[1].Status != InstanceStatusSkipped || done.SkippedCount != 1 {
		t.Fatalf("expected the last instance to be skipped, got %+v", done)
	}

	if done.Instances[0].Status != InstanceStatusSuccess || done.CompletedCount != 1 {
		t.Fatalf("expected the first instance to keep its success, got %+v", done)
	}

	if cancels := director.cancelled(); len(cancels) != 0 {
		t.Fatalf("expected no cancel, since no deploy was running, got %v", cancels)
	}
}
