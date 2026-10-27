package upgrade

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

const (
	cleanupSurvived = "cleanup started=yes exit=- killed=survived"
	cleanupEnded    = "cleanup started=yes exit=137 killed=yes"
)

// setCleanup changes what cleanup answers for a node.
func (f *taskFixture) setCleanup(key, record string) {
	f.mu.Lock()
	f.cleanups[key] = record
	f.mu.Unlock()
}

// replaceVM gives the listed VM of a node another CID, as a recreate does.
func (f *taskFixture) replaceVM(deployment string, index int) {
	f.mu.Lock()
	f.listed[deployment][index].CID = "cid-recreated"
	f.mu.Unlock()
}

// removeVM drops a node's VM from the deployment's listing, as a delete
// does.
func (f *taskFixture) removeVM(deployment string, index int) {
	f.mu.Lock()
	f.listed[deployment] = slices.Delete(slices.Clone(f.listed[deployment]), index, index+1)
	f.mu.Unlock()
}

// failListing makes the director fail to list the deployment, or list it
// again when err is nil.
func (f *taskFixture) failListing(deployment string, err error) {
	f.mu.Lock()
	f.listErr[deployment] = err
	f.mu.Unlock()
}

// holds reports whether the guard still holds the first instance.
func (f *taskFixture) holds() bool {
	_, held := f.manager.Guard().Holder("inst-0", "dep-0")

	return held
}

// settling reports whether a cleanup of the task is under way.
func (f *taskFixture) settling() bool {
	f.manager.mu.RLock()
	defer f.manager.mu.RUnlock()

	return f.manager.settling[f.task.ID] > 0
}

// heldIDs returns the instances the task's summary names as held.
func (f *taskFixture) heldIDs() []string {
	f.manager.mu.RLock()
	defer f.manager.mu.RUnlock()

	return heldCommandInstanceIDs(f.task)
}

// waitSurvivorPause runs the fixture until the first node's processes
// outlive KILL, which fails the first instance and pauses the task.
func (f *taskFixture) waitSurvivorPause() UpgradeTask {
	f.t.Helper()

	f.setCleanup("dep-0/0", cleanupSurvived)
	f.queue()

	return f.waitFor("the survivor to pause the task", func(task UpgradeTask) bool {
		return task.Paused && task.Instances[0].Status == InstanceStatusFailed
	})
}

// A cancel never releases an instance whose processes outlived KILL. The
// task waits for Resume cleanup, which checks the processes again, keeps the
// hold while they live, and releases the instance once they are gone.
func TestCommandCancelKeepsTheHoldOfASurvivor(t *testing.T) {
	fix := newTaskFixture(t, 1, 1)
	fix.waitSurvivorPause()
	fix.cancel()

	if !fix.holds() {
		t.Fatal("the cancel released an instance whose processes outlived KILL")
	}

	snap := fix.snapshot()
	if snap.Status != TaskStatusCancelled || !snap.Paused || !fix.waits() {
		t.Errorf("the task is %s, paused %v, waiting %v, want cancelled and waiting for Resume cleanup", snap.Status, snap.Paused, fix.waits())
	}

	if got := snap.Instances[0].Error; !strings.Contains(got, "survived KILL on redis/0") || !strings.Contains(got, "Resume cleanup") {
		t.Errorf("the instance says %q, want it to name the survivor and Resume cleanup", got)
	}

	if got := fix.heldIDs(); len(got) != 1 || got[0] != "inst-0" {
		t.Errorf("the summary holds %v, want inst-0", got)
	}

	waitUntil(t, "the processor to let go", func() bool { return !fix.owned() })

	cleanups := fix.countOps("dep-0", 0, opRunCleanup)

	fix.resume()

	again := fix.waitFor("the recheck to hold the task again", func(task UpgradeTask) bool { return task.Paused })
	if fix.countOps("dep-0", 0, opRunCleanup) != cleanups+1 {
		t.Errorf("Resume cleanup sent %d cleanups, want one", fix.countOps("dep-0", 0, opRunCleanup)-cleanups)
	}

	if !nodeOf(again, 0, 0).Survived || !fix.holds() {
		t.Fatal("a recheck that found the processes alive dropped the survivor or the hold")
	}

	fix.setCleanup("dep-0/0", cleanupEnded)
	fix.resume()
	fix.waitReleased()

	done := fix.waitFor("the task to stop waiting", func(task UpgradeTask) bool { return !task.Paused && !nodeOf(task, 0, 0).Survived })
	if done.Status != TaskStatusCancelled || len(fix.heldIDs()) != 0 {
		t.Errorf("the task is %s holding %v, want cancelled and holding nothing", done.Status, fix.heldIDs())
	}

	if n := fix.countOps("dep-0", 0, opRunExec); n != 1 {
		t.Errorf("exec was sent %d times, want once", n)
	}

	fix.assertNoCommandText()
}

// A resume of a task paused by a survivor checks the processes again before
// anything else, and the task never finishes while they live.
func TestCommandResumeKeepsTheHoldWhileASurvivorLives(t *testing.T) {
	fix := newTaskFixture(t, 1, 1)
	fix.waitSurvivorPause()
	waitUntil(t, "the processor to let go", func() bool { return !fix.owned() })

	cleanups := fix.countOps("dep-0", 0, opRunCleanup)

	fix.resume()

	waitUntil(t, "the recheck", func() bool { return fix.countOps("dep-0", 0, opRunCleanup) > cleanups })

	again := fix.waitFor("the task to pause again", func(task UpgradeTask) bool { return task.Paused })
	if again.Status != TaskStatusPaused || !nodeOf(again, 0, 0).Survived || !fix.holds() {
		t.Fatalf("the task is %s, survived %v, held %v, want paused and held", again.Status, nodeOf(again, 0, 0).Survived, fix.holds())
	}

	fix.setCleanup("dep-0/0", cleanupEnded)
	fix.resume()

	done := fix.waitFor("the task to end", func(task UpgradeTask) bool { return task.Status == TaskStatusFailed })
	if nodeOf(done, 0, 0).Survived {
		t.Error("the node still records survivors after cleanup found them gone")
	}

	fix.waitReleased()

	if n := fix.countOps("dep-0", 0, opRunExec); n != 1 {
		t.Errorf("exec was sent %d times, want once", n)
	}

	fix.assertNoCommandText()
}

// Resume cleanup needs no second credential, so once it has cleared the
// survivor it pauses before any other instance runs. The resume after it
// is gated as any resume that sends the command is.
func TestCommandResumeCleanupPausesBeforeTheNextInstance(t *testing.T) {
	fix := newTaskFixture(t, 2, 1)
	fix.waitSurvivorPause()
	fix.setCleanup("dep-0/0", cleanupEnded)

	var gated int

	gate := func(context.Context) error {
		gated++

		return nil
	}

	err := fix.manager.ResumeTaskGated(context.Background(), fix.task.ID, gate)
	if err != nil {
		t.Fatalf("resume cleanup: %v", err)
	}

	if gated != 0 {
		t.Error("Resume cleanup ran the gate of a resume that sends the command")
	}

	paused := fix.waitFor("the cleanup to pause the task", func(task UpgradeTask) bool {
		return task.Paused && !nodeOf(task, 0, 0).Survived
	})

	if fix.sentTo("dep-1") {
		t.Fatal("Resume cleanup sent the command to the next instance")
	}

	if paused.Status != TaskStatusPaused || paused.CleanupOnly {
		t.Errorf("the task is %s with cleanup_only %v, want paused for a gated resume", paused.Status, paused.CleanupOnly)
	}

	if got := paused.Instances[1].Error; !strings.Contains(got, "Resume to run the command") {
		t.Errorf("the next instance says %q, want it to say how to go on", got)
	}

	fix.waitReleased()

	err = fix.manager.ResumeTaskGated(context.Background(), fix.task.ID, gate)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}

	if gated != 1 {
		t.Errorf("the resume that sends the command ran the gate %d times, want once", gated)
	}

	done := fix.waitFor("the task to end", func(task UpgradeTask) bool { return task.Status == TaskStatusCompleted })
	if done.Instances[1].Status != InstanceStatusSuccess {
		t.Errorf("the second instance is %s, want success", done.Instances[1].Status)
	}
}

// A survivor's VM that the director lists with another CID is gone, and so
// is every process on it, so Resume cleanup releases the instance without
// reaching the node.
func TestCommandSurvivorOnAReplacedVMIsReleased(t *testing.T) {
	fix := newTaskFixture(t, 1, 1)
	fix.waitSurvivorPause()
	fix.cancel()
	waitUntil(t, "the processor to let go", func() bool { return !fix.owned() })

	cleanups := fix.countOps("dep-0", 0, opRunCleanup)

	fix.replaceVM("dep-0", 0)
	fix.resume()
	fix.waitReleased()

	done := fix.waitFor("the task to stop waiting", func(task UpgradeTask) bool { return !task.Paused })

	node := nodeOf(done, 0, 0)
	if node.Survived || !strings.Contains(node.Reason, "VM redis/0 was replaced") {
		t.Errorf("the node is survived %v with reason %q, want the survivor cleared because its VM was replaced", node.Survived, node.Reason)
	}

	if done.Status != TaskStatusCancelled {
		t.Errorf("the task is %s, want cancelled", done.Status)
	}

	if fix.countOps("dep-0", 0, opRunCleanup) != cleanups {
		t.Error("cleanup was sent to a VM that was replaced")
	}
}

// A director that can't list the deployment is no evidence that the
// survivor's processes are gone, so the hold stays and the task waits.
func TestCommandSurvivorKeepsTheHoldWhenTheDirectorFails(t *testing.T) {
	fix := newTaskFixture(t, 1, 1)
	fix.waitSurvivorPause()
	fix.cancel()
	waitUntil(t, "the processor to let go", func() bool { return !fix.owned() })

	fix.setCleanup("dep-0/0", cleanupEnded)
	fix.failListing("dep-0", errors.New("deployment dep-0 not found"))
	fix.resume()

	waitUntil(t, "the resume to end", func() bool { return fix.waits() && !fix.settling() })

	if !fix.holds() || !nodeOf(fix.snapshot(), 0, 0).Survived {
		t.Fatal("a director error released a survivor")
	}

	fix.failListing("dep-0", nil)
	fix.resume()
	fix.waitReleased()
}

// A panic in the processing of a command task that holds an unresolved
// node pauses the task rather than failing it, so the hold it keeps still
// has a Resume cleanup to resolve it. A cancelled one waits for one.
func TestCommandPanicKeepsAHeldTaskWaitingForResumeCleanup(t *testing.T) {
	for _, status := range []TaskStatus{TaskStatusRunning, TaskStatusCancelled} {
		t.Run(string(status), func(t *testing.T) {
			fix := newTaskFixture(t, 1, 1)

			fix.manager.mu.Lock()
			fix.task.Status = status
			fix.task.Instances[0].Status = InstanceStatusFailed
			fix.task.Instances[0].Command.VMs[0].Phase = CommandPhaseSettled
			fix.task.Instances[0].Command.VMs[0].Status = CommandStatusFailed
			fix.task.Instances[0].Command.VMs[0].Survived = true
			fix.manager.mu.Unlock()

			func() {
				defer fix.manager.recoverTask(context.Background(), fix.task)

				panic("a test panic")
			}()

			snap := fix.snapshot()
			if !snap.Paused || (snap.Status != TaskStatusPaused && snap.Status != TaskStatusCancelled) {
				t.Fatalf("the task is %s paused=%v, want it waiting for Resume cleanup", snap.Status, snap.Paused)
			}

			fix.manager.mu.RLock()
			cleansOnly := commandCleansOnlyLocked(fix.task)
			fix.manager.mu.RUnlock()

			if !cleansOnly {
				t.Error("the held task's resume is not a Resume cleanup")
			}
		})
	}
}
