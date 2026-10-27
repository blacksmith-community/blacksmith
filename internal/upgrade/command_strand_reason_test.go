package upgrade

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// A node cleanup settles after it was stranded says why Blacksmith lost
// track of it, and the reason names the real cause. The four causes are a
// pause, a cancel, a restart, and a worker that panicked.
func TestStrandedNodeReasonNamesTheCause(t *testing.T) {
	started := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)

	strand := func(_ *taskFixture, task *UpgradeTask) {
		task.CreationConfirmed = true
		task.Status = TaskStatusPaused
		task.Paused = true
		task.Instances[0].Status = InstanceStatusRunning
		task.Instances[0].Command.VMs[0].Phase = CommandPhaseSent
		task.Instances[0].Command.VMs[0].StartedAt = &started
	}

	reasonOf := func(task UpgradeTask) string { return task.Instances[0].Command.VMs[0].Reason }

	t.Run("pause", func(t *testing.T) {
		fix := newTaskFixture(t, 1, 1)

		fix.manager.mu.Lock()
		strand(fix, fix.task)
		fix.manager.mu.Unlock()

		fix.resume()

		done := fix.waitFor("the node to settle", func(task UpgradeTask) bool {
			return task.Instances[0].Command.VMs[0].Phase == CommandPhaseSettled
		})

		if got := reasonOf(done); !strings.Contains(got, "when it paused") {
			t.Errorf("the reason is %q, want it to name the pause", got)
		}

		fix.assertNoCommandText()
	})

	t.Run("restart", func(t *testing.T) {
		fix := newTaskFixtureFrom(t, 1, 1, strand)

		fix.resume()

		done := fix.waitFor("the node to settle", func(task UpgradeTask) bool {
			return task.Instances[0].Command.VMs[0].Phase == CommandPhaseSettled
		})

		if got := reasonOf(done); !strings.Contains(got, "when Blacksmith restarted") {
			t.Errorf("the reason is %q, want it to name the restart", got)
		}

		fix.assertNoCommandText()
	})

	t.Run("cancel", func(t *testing.T) {
		fix := newTaskFixtureFrom(t, 1, 1, func(fix *taskFixture, task *UpgradeTask) {
			strand(fix, task)
			task.Status = TaskStatusCancelled
		})

		fix.resume()

		done := fix.waitFor("the node to settle", func(task UpgradeTask) bool {
			return task.Instances[0].Command.VMs[0].Phase == CommandPhaseSettled
		})

		if got := reasonOf(done); !strings.Contains(got, "when the task was cancelled") {
			t.Errorf("the reason is %q, want it to name the cancel", got)
		}

		fix.assertNoCommandText()
	})

	t.Run("worker panic", func(t *testing.T) {
		fix := newTaskFixture(t, 1, 1)

		var fired atomic.Bool

		fix.onOp = func(_ string, _ int, op string) {
			if op == opRunExec && fired.CompareAndSwap(false, true) {
				panic("boom")
			}
		}

		fix.queue()
		fix.waitFor("the task to pause", func(task UpgradeTask) bool { return task.Paused && task.Status == TaskStatusPaused })

		err := fix.manager.ResumeTask(context.Background(), fix.task.ID)
		if err != nil {
			t.Fatalf("resume: %v", err)
		}

		done := fix.waitFor("the node to settle", func(task UpgradeTask) bool {
			return task.Instances[0].Command.VMs[0].Phase == CommandPhaseSettled
		})

		if got := reasonOf(done); !strings.Contains(got, "internal error") {
			t.Errorf("the reason is %q, want it to name the internal error", got)
		}

		fix.assertNoCommandText()
	})
}
