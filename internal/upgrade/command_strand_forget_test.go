package upgrade

import (
	"strings"
	"testing"
	"time"
)

// strandCausesOf counts the causes the manager keeps for the nodes of the
// fixture's task.
func strandCausesOf(fix *taskFixture) int {
	fix.manager.mu.RLock()
	defer fix.manager.mu.RUnlock()

	n := 0

	for key := range fix.manager.strandCauses {
		if strings.HasPrefix(key, fix.task.ID+"/") {
			n++
		}
	}

	return n
}

// The cause of a stranded node is kept only while the task can still
// settle the node with it, so a task that ends leaves none behind.
func TestStrandCausesAreForgottenWhenTheTaskEnds(t *testing.T) {
	started := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)

	strand := func(_ *taskFixture, task *UpgradeTask) {
		task.CreationConfirmed = true
		task.Status = TaskStatusPaused
		task.Paused = true
		task.Instances[0].Status = InstanceStatusRunning
		task.Instances[0].Command.VMs[0].Phase = CommandPhaseSent
		task.Instances[0].Command.VMs[0].StartedAt = &started
	}

	t.Run("cancel", func(t *testing.T) {
		fix := newTaskFixtureFrom(t, 1, 1, strand)

		if got := strandCausesOf(fix); got != 1 {
			t.Fatalf("the loaded task keeps %d causes, want the restart's", got)
		}

		fix.cancel()

		if got := strandCausesOf(fix); got != 0 {
			t.Errorf("the cancelled task keeps %d causes, want none", got)
		}

		fix.assertNoCommandText()
	})

	t.Run("finish", func(t *testing.T) {
		fix := newTaskFixture(t, 1, 1)

		fix.manager.mu.Lock()
		fix.manager.noteStrandCauseLocked(fix.task, &fix.task.Instances[0], 0, strandedByPanic)
		fix.manager.mu.Unlock()

		fix.queue()

		fix.waitFor("the task to finish", func(task UpgradeTask) bool {
			return task.Status == TaskStatusCompleted || task.Status == TaskStatusFailed
		})

		if got := strandCausesOf(fix); got != 0 {
			t.Errorf("the finished task keeps %d causes, want none", got)
		}

		fix.assertNoCommandText()
	})

	t.Run("a cause noted after the end is not kept", func(t *testing.T) {
		fix := newTaskFixture(t, 1, 1)

		fix.manager.mu.Lock()
		strand(fix, fix.task)
		fix.manager.mu.Unlock()

		fix.cancel()

		fix.manager.mu.Lock()
		fix.manager.noteStrandCauseLocked(fix.task, &fix.task.Instances[0], 0, strandedByPanic)
		fix.manager.mu.Unlock()

		if got := strandCausesOf(fix); got != 0 {
			t.Errorf("the cancelled task keeps %d causes, want none", got)
		}

		fix.assertNoCommandText()
	})
}
