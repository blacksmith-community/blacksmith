package upgrade

import (
	"slices"
	"testing"
)

// A paused command task that nothing processes is cancelled. Its running
// instance, which holds nothing on a VM, ends as cancelled with its
// unstarted node skipped, and its pending instance is skipped with every
// node skipped, at once and in the saved record, since no processor will
// ever come to end them.
func TestCancelOfAnUnownedPausedCommandTaskEndsWhatNeverRan(t *testing.T) {
	fix := newTaskFixture(t, 2, 2)

	fix.manager.mu.Lock()
	fix.task.Status = TaskStatusPaused
	fix.task.Paused = true
	fix.task.Instances[0].Status = InstanceStatusRunning
	fix.task.Instances[0].Command.VMs[0].Phase = CommandPhaseSettled
	fix.task.Instances[0].Command.VMs[0].Status = CommandStatusSucceeded
	fix.manager.mu.Unlock()

	fix.cancel()

	for name, task := range map[string]UpgradeTask{"memory": fix.snapshot(), "the saved record": fix.lastSaved()} {
		if task.Status != TaskStatusCancelled {
			t.Errorf("in %s the task is %s, want cancelled", name, task.Status)
		}

		first, second := task.Instances[0], task.Instances[1]
		if first.Status != InstanceStatusCancelled || !slices.Equal(phases(first), []CommandPhase{CommandPhaseSettled, CommandPhaseSkipped}) {
			t.Errorf("in %s the running instance is %s with nodes %v, want cancelled with its unstarted node skipped", name, first.Status, phases(first))
		}

		if second.Status != InstanceStatusSkipped || !slices.Equal(phases(second), []CommandPhase{CommandPhaseSkipped, CommandPhaseSkipped}) {
			t.Errorf("in %s the pending instance is %s with nodes %v, want skipped with every node skipped", name, second.Status, phases(second))
		}

		if task.CancelledCount != 1 || task.SkippedCount != 1 {
			t.Errorf("in %s the counts are cancelled %d and skipped %d, want 1 and 1", name, task.CancelledCount, task.SkippedCount)
		}
	}

	if node := nodeOf(fix.snapshot(), 1, 0); node.Reason != reasonCancelledBeforeStart {
		t.Errorf("a node of the skipped instance gives the reason %q, want the cancel", node.Reason)
	}

	if len(fix.recorded()) != 0 {
		t.Errorf("the cancel sent %v", fix.recorded())
	}

	fix.waitReleased()
	fix.assertNoCommandText()
}
