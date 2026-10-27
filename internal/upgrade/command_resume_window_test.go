package upgrade

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// A resume that lands after the processor paused the task but before the
// processor stopped being owned used to leave the task running with no
// processor. The authorizer lets the dispatch gate through and refuses the
// next check, which is the refusal before the first node, and the log
// hook holds the processor between its pause and its save, which is the
// window an operator's resume can land in.
func TestResumeWhileTheProcessorIsStillOwnedDrivesTheTask(t *testing.T) {
	fix := newTaskFixture(t, 1, 1, withRoot())

	var checks atomic.Int32

	var refusing atomic.Bool

	refusing.Store(true)

	fix.authHook = func(context.Context) error {
		if checks.Add(1) >= 2 && refusing.Load() {
			return errors.New("the authorizer refuses the stored task")
		}

		return nil
	}

	paused := make(chan struct{})
	release := make(chan struct{})

	var hit atomic.Bool

	t.Cleanup(func() {
		if hit.Load() {
			select {
			case <-release:
			default:
				close(release)
			}
		}
	})

	fix.log.hook = func(line string) {
		if strings.Contains(line, "paused before it started") && hit.CompareAndSwap(false, true) {
			close(paused)
			<-release
		}
	}

	fix.queue()

	select {
	case <-paused:
	case <-time.After(waitTimeout):
		t.Fatal("the refusal before the first node never paused the task")
	}

	refusing.Store(false)

	err := fix.manager.ResumeTask(context.Background(), fix.task.ID)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}

	close(release)

	done := fix.waitFor("the resumed task to finish", func(task UpgradeTask) bool {
		return task.Status == TaskStatusCompleted || task.Status == TaskStatusFailed
	})

	if done.Instances[0].Status != InstanceStatusSuccess {
		t.Errorf("the instance is %s, want success after the resume drove it", done.Instances[0].Status)
	}

	if len(fix.recorded()) == 0 {
		t.Error("the resume landed in the window and no node was ever driven")
	}

	fix.assertNoCommandText()
}

// An upgrade whose processor returns with the task still running, as one
// does when an instance is still running with nothing settling it, is not
// put back on the queue, since no resume landed while it was processed.
// Without that the task would be processed again at once, without end.
// The window is bounded, and the task is failed afterwards so nothing of
// it outlives the test.
func TestAProcessorThatLeavesItsTaskRunningDoesNotRequeueIt(t *testing.T) {
	fix := newTaskFixture(t, 1, 1)

	var processed atomic.Int32

	fix.log.hook = func(line string) {
		if strings.Contains(line, "so the upgrade stops until it settles") {
			processed.Add(1)
		}
	}

	upgrade := &UpgradeTask{
		ID:        "upgrade-left-running",
		Kind:      TaskKindUpgrade,
		Status:    TaskStatusRunning,
		Instances: []InstanceUpgrade{{InstanceID: "left-0", DeploymentName: "left-dep", Status: InstanceStatusRunning}},
	}

	fix.manager.mu.Lock()
	fix.manager.tasks[upgrade.ID] = upgrade
	fix.manager.mu.Unlock()

	owned := func() bool {
		fix.manager.mu.RLock()
		defer fix.manager.mu.RUnlock()

		return fix.manager.owned[upgrade.ID]
	}

	t.Cleanup(func() {
		fix.manager.mu.Lock()
		upgrade.Status = TaskStatusFailed
		fix.manager.mu.Unlock()

		waitUntil(t, "the upgrade to be let go", func() bool { return !owned() })
	})

	fix.manager.resumeIfReady(upgrade)

	waitUntil(t, "the upgrade to be processed", func() bool { return processed.Load() >= 1 })
	waitUntil(t, "the processor to let go of the upgrade", func() bool { return !owned() || processed.Load() > 1 })

	time.Sleep(200 * time.Millisecond)

	if got := processed.Load(); got != 1 {
		t.Errorf("the upgrade was processed %d times, want once", got)
	}
}
