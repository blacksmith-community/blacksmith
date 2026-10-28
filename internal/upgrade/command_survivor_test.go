package upgrade

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	cleanupSurvived = "cleanup started=yes exit=- killed=survived"
	cleanupEnded    = "cleanup started=yes exit=137 killed=yes"
	// cleanupFailedExit settles a stranded node as failed on its exit
	// status.
	cleanupFailedExit = "cleanup started=yes exit=3 killed=no"
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

var errSendGateRan = errors.New("the gate of a resume that sends the command ran")

// A Resume cleanup that lands while the run saves the instance it ended
// for the processes that outlived KILL stops the run there. The task is
// processed again from that instance, so the survivors are checked before
// the task pauses before the next instance.
func TestCommandResumeCleanupDuringTheInstanceSaveChecksTheSurvivors(t *testing.T) {
	fix := newTaskFixture(t, 2, 1)

	var gated int

	// A gate that runs refuses the resume, so the cleanup must not reach it.
	gate := func(context.Context) error {
		gated++

		return errSendGateRan
	}

	resumed := make(chan error, 1)

	var once sync.Once

	// The resume starts inside the run's save of the ended instance, and
	// the save waits until the resume has changed the task.
	fix.vault.onPut = func(path string, raw []byte) {
		var task UpgradeTask
		if path != commandTasksVaultPath+"/"+fix.task.ID || json.Unmarshal(raw, &task) != nil {
			return
		}

		if !task.Paused || task.Instances[0].Status != InstanceStatusFailed || !nodeOf(task, 0, 0).Survived {
			return
		}

		once.Do(func() {
			fix.setCleanup("dep-0/0", cleanupEnded)

			go func() { resumed <- fix.manager.ResumeTaskGated(context.Background(), fix.task.ID, gate) }()

			for deadline := time.Now().Add(waitTimeout); fix.snapshot().Paused && time.Now().Before(deadline); {
				time.Sleep(time.Millisecond)
			}
		})
	}

	fix.setCleanup("dep-0/0", cleanupSurvived)
	fix.queue()

	select {
	case err := <-resumed:
		if err != nil {
			t.Fatalf("resume cleanup: %v", err)
		}
	case <-time.After(waitTimeout):
		t.Fatal("the run never saved the instance the survivors failed")
	}

	if gated != 0 {
		t.Error("Resume cleanup ran the gate of a resume that sends the command")
	}

	paused := fix.waitFor("the task to pause", func(task UpgradeTask) bool { return task.Paused })

	if nodeOf(paused, 0, 0).Survived {
		t.Error("the task paused with the survivors unchecked")
	}

	if got := paused.Instances[1].Error; !strings.Contains(got, "Resume to run the command") {
		t.Errorf("the next instance says %q, want the Resume cleanup to pause before it", got)
	}

	if fix.sentTo("dep-1") {
		t.Error("Resume cleanup sent the command to the next instance")
	}
}

// A Resume cleanup that lands after a node's processes outlived KILL, and
// before the run ends the instance, clears the pause the node recorded.
// The instance ends without pausing the task again for its failure, and
// the run stops there, so the task is processed again from the instance.
func TestASurvivorInstanceEndedAfterAResumeCleanupStopsTheRun(t *testing.T) {
	fix := newTaskFixture(t, 2, 1)
	inst := &fix.task.Instances[0]

	fix.manager.mu.Lock()
	fix.task.Status = TaskStatusRunning
	fix.task.CleanupOnly = true
	inst.Status = InstanceStatusRunning
	inst.Command.VMs[0].Phase = CommandPhaseSettled
	inst.Command.VMs[0].Status = CommandStatusFailed
	inst.Command.VMs[0].Survived = true
	fix.manager.mu.Unlock()

	if fix.manager.endCommandInstance(context.Background(), fix.task, inst, true, true) {
		t.Error("the run went on past the instance whose processes outlived KILL")
	}

	snap := fix.snapshot()
	if snap.Paused || snap.Status != TaskStatusRunning || snap.Instances[0].Status != InstanceStatusFailed {
		t.Errorf("the task is %s paused=%v with its instance %s, want it running for the Resume cleanup and the instance failed", snap.Status, snap.Paused, snap.Instances[0].Status)
	}
}

// An ordinary resume that clears the pause a node's surviving processes
// recorded, before the run ends an instance that is not the last, keeps
// the task running. The run stops there without pausing again for the
// failure, so the task is processed again from the instance and the
// survivors are checked first.
func TestASurvivorInstanceEndedAfterAnOrdinaryResumeStopsTheRun(t *testing.T) {
	fix := newTaskFixture(t, 2, 1)
	inst := &fix.task.Instances[0]

	fix.manager.mu.Lock()
	fix.task.Status = TaskStatusRunning
	inst.Status = InstanceStatusRunning
	inst.Command.VMs[0].Phase = CommandPhaseSettled
	inst.Command.VMs[0].Status = CommandStatusFailed
	inst.Command.VMs[0].Survived = true
	fix.manager.mu.Unlock()

	if fix.manager.endCommandInstance(context.Background(), fix.task, inst, true, true) {
		t.Error("the run went on past the instance whose processes outlived KILL")
	}

	snap := fix.snapshot()
	if snap.Paused || snap.Status != TaskStatusRunning || snap.Instances[0].Status != InstanceStatusFailed {
		t.Errorf("the task is %s paused=%v with its instance %s, want it running for the resume and the instance failed", snap.Status, snap.Paused, snap.Instances[0].Status)
	}
}

// waitSurvivorWindow runs two instances of three nodes, two at a time,
// until the processes of dep-0's second node outlive KILL while its first
// node is still held before exec, and resumes the task there through the
// gate a resume that sends the command needs. It returns the gate that
// holds the first node, and how many cleanups the survivor has had.
func (f *taskFixture) waitSurvivorWindow() (*nodeGate, int) {
	f.t.Helper()

	f.setCleanup("dep-0/1", cleanupSurvived)
	exec := f.gate(opRunExec, "dep-0/0", "dep-0/1")
	f.queue()

	exec.next()
	exec.next()
	exec.let("dep-0/1")

	f.waitFor("the survivor to pause the task", func(task UpgradeTask) bool {
		return task.Paused && nodeOf(task, 0, 1).Survived
	})

	cleanups := f.countOps("dep-0", 1, opRunCleanup)

	gated := 0

	err := f.manager.ResumeTaskGated(context.Background(), f.task.ID, func(context.Context) error {
		gated++

		return nil
	})
	if err != nil {
		f.t.Fatalf("resume: %v", err)
	}

	if snap := f.snapshot(); gated != 1 || snap.CleanupOnly || snap.Paused {
		f.t.Fatalf("the resume ran the gate %d times, cleanup_only %v, paused %v, want an ordinary resume", gated, snap.CleanupOnly, snap.Paused)
	}

	return exec, cleanups
}

// An ordinary resume that lands while a sibling of a node whose processes
// outlived KILL is still in flight is honored once the sibling ends. The
// task is processed again from the instance, and the survivors are checked
// first, so while they live the task pauses again for Resume cleanup and
// nothing reaches the next instance.
func TestCommandResumeDuringTheSurvivorWindowChecksTheSurvivorsFirst(t *testing.T) {
	fix := newTaskFixture(t, 2, 3, withParallel(2))
	exec, cleanups := fix.waitSurvivorWindow()

	exec.let("dep-0/0")

	paused := fix.waitFor("the task to stop", func(task UpgradeTask) bool { return task.Paused && !fix.owned() })

	if n := fix.countOps("dep-0", 1, opRunCleanup) - cleanups; n != 1 {
		t.Errorf("the survivor had %d cleanups after the resume, want one recheck", n)
	}

	if got := paused.Instances[0].Error; !strings.Contains(got, "still survive KILL on redis/1") || !strings.Contains(got, "Resume cleanup") {
		t.Errorf("the instance says %q, want the recheck to name the survivor and Resume cleanup", got)
	}

	if paused.Status != TaskStatusPaused || !nodeOf(paused, 0, 1).Survived || !fix.holds() {
		t.Errorf("the task is %s, survived %v, held %v, want paused and held", paused.Status, nodeOf(paused, 0, 1).Survived, fix.holds())
	}

	if fix.sentTo("dep-1") {
		t.Error("the resume sent the command to the next instance while the survivors live")
	}

	fix.assertNoCommandText()
}

// An ordinary resume that lands while a sibling of a node whose processes
// outlived KILL is still in flight goes on to the next instance once the
// sibling ends and the recheck finds the survivors gone.
func TestCommandResumeDuringTheSurvivorWindowGoesOnOnceTheSurvivorsAreGone(t *testing.T) {
	fix := newTaskFixture(t, 2, 3, withParallel(2))
	exec, cleanups := fix.waitSurvivorWindow()

	fix.setCleanup("dep-0/1", cleanupEnded)
	exec.let("dep-0/0")

	done := fix.waitFor("the task to stop", func(task UpgradeTask) bool {
		over := task.Status == TaskStatusCompleted || task.Status == TaskStatusFailed

		return (task.Paused || over) && !fix.owned()
	})

	if done.Paused || done.Status != TaskStatusCompleted {
		t.Fatalf("the task is %s paused=%v, want it completed", done.Status, done.Paused)
	}

	if n := fix.countOps("dep-0", 1, opRunCleanup) - cleanups; n != 1 {
		t.Errorf("the survivor had %d cleanups after the resume, want one recheck", n)
	}

	if nodeOf(done, 0, 1).Survived {
		t.Error("the node still records survivors after the recheck found them gone")
	}

	if done.Instances[1].Status != InstanceStatusSuccess {
		t.Errorf("the next instance is %s, want success", done.Instances[1].Status)
	}

	for index := range 3 {
		if n := fix.countOps("dep-1", index, opRunExec); n != 1 {
			t.Errorf("dep-1/%d was sent exec %d times, want once", index, n)
		}
	}

	fix.assertNoCommandText()
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

	fix.waitFor("the recheck to keep the hold", func(task UpgradeTask) bool {
		return strings.Contains(task.Instances[0].Error, "could not reach")
	})
	waitUntil(t, "the resume to end", func() bool { return fix.waits() && !fix.settling() })

	if !fix.holds() || !nodeOf(fix.snapshot(), 0, 0).Survived {
		t.Fatal("a director error released a survivor")
	}

	fix.failListing("dep-0", nil)
	fix.resume()
	fix.waitReleased()
}

// A recheck that finds no evidence that the survivor's processes ended
// keeps the survivor and the hold, and says why on the instance. A director
// that can't list the deployment, a node that can't be reached, a cleanup
// that does not report cleanly, a record that can't say what ran, and a
// record that says cleanup could not check the session are each no
// evidence.
func TestCommandSurvivorRecheckWithoutEvidenceKeepsTheHold(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*taskFixture)
		want  string
	}{
		{name: "director error", want: "could not reach", setup: func(f *taskFixture) {
			f.failListing("dep-0", errors.New("deployment dep-0 not found"))
		}},
		{name: "unreachable node", want: "could not reach", setup: func(f *taskFixture) {
			f.runner = func(context.Context, string, string, []int) (PatchSession, error) { return nil, errNodeUnreachable }
		}},
		{name: "unclean cleanup", want: "did not report cleanly", setup: func(f *taskFixture) {
			f.mu.Lock()
			f.override = func(_ context.Context, _ string, _ int, op string) *opAnswer {
				if op == opRunCleanup {
					return &opAnswer{err: errNodeUnreachable}
				}

				return nil
			}
			f.mu.Unlock()
		}},
		{name: "unknown record", want: "found no record", setup: func(f *taskFixture) {
			f.setCleanup("dep-0/0", "cleanup started=- exit=- killed=no")
		}},
		{name: "unchecked", want: "could not check", setup: func(f *taskFixture) {
			f.setCleanup("dep-0/0", "cleanup started=yes exit=- killed=unknown")
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fix := newTaskFixture(t, 1, 1)
			fix.waitSurvivorPause()
			waitUntil(t, "the processor to let go", func() bool { return !fix.owned() })

			fix.setCleanup("dep-0/0", cleanupEnded)
			tc.setup(fix)
			fix.resume()

			held := fix.waitFor("the recheck to keep the hold", func(task UpgradeTask) bool {
				return strings.Contains(task.Instances[0].Error, tc.want)
			})
			waitUntil(t, "the resume to end", func() bool { return !fix.owned() && !fix.settling() })

			snap := fix.snapshot()
			if !nodeOf(snap, 0, 0).Survived || !fix.holds() {
				t.Fatalf("the node is survived %v and held %v, want both kept", nodeOf(snap, 0, 0).Survived, fix.holds())
			}

			if snap.Status != TaskStatusPaused || !snap.Paused {
				t.Errorf("the task is %s, paused %v, want paused for Resume cleanup", snap.Status, snap.Paused)
			}

			if !strings.Contains(held.Instances[0].Error, "Resume cleanup") && !strings.Contains(held.Instances[0].Error, "Recreating the VM") {
				t.Errorf("the instance says %q, want it to say how the hold ends", held.Instances[0].Error)
			}

			fix.assertNoCommandText()
		})
	}
}

// A recheck whose record says killed=no found nothing of the command left
// to end: the wrapper checks the session of a command without a usable
// exit status, and records the command's own exit status otherwise. That
// is evidence, so the survivor clears and the instance is released.
func TestCommandSurvivorRecheckThatKilledNothingReleases(t *testing.T) {
	for _, record := range []string{
		"cleanup started=yes exit=137 killed=no",
		"cleanup started=yes exit=- killed=no",
		"cleanup started=no exit=- killed=no",
	} {
		t.Run(record, func(t *testing.T) {
			fix := newTaskFixture(t, 1, 1)
			fix.waitSurvivorPause()
			waitUntil(t, "the processor to let go", func() bool { return !fix.owned() })

			fix.setCleanup("dep-0/0", record)
			fix.resume()
			fix.waitReleased()

			done := fix.waitFor("the task to stop waiting", func(task UpgradeTask) bool { return !task.Paused && !nodeOf(task, 0, 0).Survived })

			if node := nodeOf(done, 0, 0); !strings.Contains(node.Reason, "found nothing of the command left") {
				t.Errorf("the node gives the reason %q, want the recheck's evidence", node.Reason)
			}

			if n := fix.countOps("dep-0", 0, opRunExec); n != 1 {
				t.Errorf("exec was sent %d times, want once", n)
			}

			fix.assertNoCommandText()
		})
	}
}

// A recheck that finds the processes ended, but whose save of that is
// refused, keeps the hold and says the result was not saved. The wrapper
// already removed the task directory, so the next Resume cleanup saves
// the evidence it kept without cleaning the node again, and releases the
// instance once the save lands.
func TestCommandSurvivorEndSavedOnTheNextResume(t *testing.T) {
	fix := newTaskFixture(t, 1, 1)
	fix.waitSurvivorPause()
	waitUntil(t, "the processor to let go", func() bool { return !fix.owned() })

	cleared := func(task UpgradeTask) bool { return !task.Instances[0].Command.VMs[0].Survived }

	fix.vault.refuseWhen(cleared)
	fix.setCleanup("dep-0/0", cleanupEnded)
	fix.resume()

	held := fix.waitFor("the refused save to keep the hold", func(task UpgradeTask) bool {
		return strings.Contains(task.Instances[0].Error, "could not save")
	})
	waitUntil(t, "the resume to end", func() bool { return !fix.owned() && !fix.settling() })

	snap := fix.snapshot()
	if !nodeOf(snap, 0, 0).Survived || !fix.holds() || len(fix.heldIDs()) != 1 {
		t.Fatalf("the node is survived %v, held %v, summary %v, want the hold kept until the save lands", nodeOf(snap, 0, 0).Survived, fix.holds(), fix.heldIDs())
	}

	if snap.Status != TaskStatusPaused || !snap.Paused || !strings.Contains(held.Instances[0].Error, "Resume cleanup") {
		t.Errorf("the task is %s, paused %v, saying %q, want paused for Resume cleanup", snap.Status, snap.Paused, held.Instances[0].Error)
	}

	if stored := fix.vault.stored(t); !stored.Survived {
		t.Fatal("Vault holds the node as cleared after the save was refused")
	}

	cleanups := fix.countOps("dep-0", 0, opRunCleanup)

	fix.vault.refuseWhen(nil)
	fix.setCleanup("dep-0/0", "cleanup started=- exit=- killed=no")
	fix.resume()
	fix.waitReleased()

	done := fix.waitFor("the task to end", func(task UpgradeTask) bool { return task.Status == TaskStatusFailed })
	if nodeOf(done, 0, 0).Survived || len(fix.heldIDs()) != 0 {
		t.Errorf("the node is survived %v holding %v, want it cleared and released", nodeOf(done, 0, 0).Survived, fix.heldIDs())
	}

	if !strings.Contains(nodeOf(done, 0, 0).Reason, "found them ended") {
		t.Errorf("the node says %q, want the reason the recheck found", nodeOf(done, 0, 0).Reason)
	}

	if stored := fix.vault.stored(t); stored.Survived {
		t.Error("Vault still holds the node as survived after the save landed")
	}

	if n := fix.countOps("dep-0", 0, opRunCleanup); n != cleanups {
		t.Errorf("the second Resume cleanup sent %d cleanups, want none", n-cleanups)
	}

	fix.assertNoCommandText()
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
