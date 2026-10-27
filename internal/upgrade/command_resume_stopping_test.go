package upgrade

import (
	"context"
	"errors"
	"testing"
)

// storedTask reads the task as the fixture's Vault holds it.
func (f *taskFixture) storedTask() UpgradeTask {
	f.t.Helper()

	var stored UpgradeTask

	_, err := f.vault.confirmVault.Get(context.Background(), taskRecordPath(f.task), &stored)
	if err != nil {
		f.t.Fatalf("reading the stored task: %v", err)
	}

	return stored
}

// A Resume cleanup that reaches a stopping manager starts no cleanup, so
// it is refused and the task goes on waiting for one, in memory and in
// Vault, and the next broker still offers it.
func TestResumeCleanupOfAStoppingManagerKeepsTheTaskWaiting(t *testing.T) {
	fix := newTaskFixture(t, 1, 1)
	fix.strandSent()

	fix.manager.Stop()

	err := fix.manager.ResumeTask(context.Background(), fix.task.ID)
	if !errors.Is(err, ErrCleanupNotStarted) {
		t.Fatalf("the resume returned %v, want ErrCleanupNotStarted", err)
	}

	if snap := fix.snapshot(); !snap.Paused || !fix.waits() {
		t.Errorf("the task is paused %v and waiting %v, want it still waiting for Resume cleanup", snap.Paused, fix.waits())
	}

	if stored := fix.storedTask(); stored.Status != TaskStatusCancelled || !stored.Paused {
		t.Errorf("Vault holds the task %s and paused %v, want cancelled and paused", stored.Status, stored.Paused)
	}
}

// A cancelled task saved with a held node and no pause, which nothing can
// clean when it loads, loads waiting for Resume cleanup, so the hold it
// keeps has a control that reaches it.
func TestLoadedCancelledTaskThatCannotBeCleanedWaitsForResumeCleanup(t *testing.T) {
	fix := newTaskFixtureFrom(t, 1, 1, func(fix *taskFixture, task *UpgradeTask) {
		task.CreationConfirmed = true
		task.Status = TaskStatusCancelled
		task.Paused = false
		task.Instances[0].Status = InstanceStatusRunning
		task.Instances[0].Command.VMs[0].Phase = CommandPhaseSent
		task.Instances[0].Command.VMs[0].StartedAt = &loadedStart

		fix.manager.director = nil
	})

	snap := fix.snapshot()
	if !snap.Paused || !fix.waits() {
		t.Fatalf("the loaded task is paused %v and waiting %v, want it waiting for Resume cleanup", snap.Paused, fix.waits())
	}

	if !fix.holds() {
		t.Error("the loaded task let go of its held node")
	}

	if len(fix.recorded()) != 0 {
		t.Errorf("the load sent %v", fix.recorded())
	}
}
