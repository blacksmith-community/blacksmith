package upgrade

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"
)

var loadedStart = time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)

// store saves the task under its index in the fixture's Vault, as an
// earlier broker left it.
func (f *taskFixture) store(task *UpgradeTask) {
	f.t.Helper()

	ctx := context.Background()

	err := f.vault.confirmVault.Put(ctx, taskRecordPath(task), task)
	if err != nil {
		f.t.Fatalf("storing the task: %v", err)
	}

	index := map[string]interface{}{}

	_, err = f.vault.confirmVault.Get(ctx, commandTasksVaultPath, &index)
	if err != nil {
		f.t.Fatalf("reading the index: %v", err)
	}

	index[task.ID] = map[string]interface{}{}

	err = f.vault.confirmVault.Put(ctx, commandTasksVaultPath, index)
	if err != nil {
		f.t.Fatalf("storing the index: %v", err)
	}
}

// restartedWith loads a broker whose stored task is running, with its first
// instance running and its nodes in the phases given by index.
func restartedWith(phases map[int]CommandPhase) func(*taskFixture, *UpgradeTask) {
	return func(_ *taskFixture, task *UpgradeTask) {
		task.Status = TaskStatusRunning
		task.CreationConfirmed = true
		task.ClientRequestID = "request-1"
		task.Instances[0].Status = InstanceStatusRunning
		task.Instances[0].StartedAt = &loadedStart

		for index, phase := range phases {
			node := &task.Instances[0].Command.VMs[index]
			node.Phase = phase

			if phase == CommandPhaseStaged || phase == CommandPhaseSent {
				node.StartedAt = &loadedStart
			}
		}
	}
}

// holdCleanups makes every cleanup wait for the returned release, so a test
// can look at the manager while a loaded node is still being settled.
func (f *taskFixture) holdCleanups(t *testing.T) (release func()) {
	t.Helper()

	gate := make(chan struct{})

	var once func()

	once = func() {
		select {
		case <-gate:
		default:
			close(gate)
		}
	}

	t.Cleanup(once)

	f.onOp = func(_ string, _ int, op string) {
		if op == opRunCleanup {
			<-gate
		}
	}

	return once
}

func (f *taskFixture) owns() bool {
	f.manager.mu.RLock()
	defer f.manager.mu.RUnlock()

	return f.manager.owned[f.task.ID]
}

// reserved returns how many instances the task reserves.
func (f *taskFixture) reserved() int {
	f.manager.mu.RLock()
	defer f.manager.mu.RUnlock()

	return len(f.manager.reservations[f.task.ID])
}

// busyWith returns the error another task gets when it reserves the
// instance.
func (f *taskFixture) busyWith(instance int) error {
	other := &UpgradeTask{ID: "other", Kind: TaskKindUpgrade, Status: TaskStatusPending, Instances: []InstanceUpgrade{pendingInstance(f.task.Instances[instance].InstanceID, f.task.Instances[instance].DeploymentName)}}

	f.manager.mu.Lock()
	defer f.manager.mu.Unlock()

	return f.manager.reserveNewLocked(other)
}

func (f *taskFixture) assertNeverSentTo(deployment string, index int) {
	f.t.Helper()

	for _, op := range f.opsOf(deployment, index) {
		if op == opRunPrepare || op == opRunExec {
			f.t.Errorf("node %s/%d was sent %s after a restart", deployment, index, op)
		}
	}
}

func TestLoadedNodeIsSettledThroughCleanupOnly(t *testing.T) {
	for _, phase := range []CommandPhase{CommandPhaseSent, CommandPhaseStaged} {
		t.Run(string(phase), func(t *testing.T) {
			fix := newTaskFixtureFrom(t, 2, 2, restartedWith(map[int]CommandPhase{0: phase}))
			fix.cleanups["dep-0/0"] = "cleanup started=yes exit=7 killed=no"

			release := fix.holdCleanups(t)

			// The task is owned from the load and runs its settle.
			waitUntil(t, "the loaded task to be owned", fix.owns)

			if held := fix.reserved(); held != 2 {
				t.Errorf("the loaded task holds %d instances, want both", held)
			}

			release()

			paused := fix.waitFor("the task to pause", func(task UpgradeTask) bool {
				return task.Status == TaskStatusPaused && task.Paused && task.Instances[0].Status == InstanceStatusFailed
			})

			if ops := fix.opsOf("dep-0", 0); !slices.Equal(ops, []string{opRunCleanup}) {
				t.Errorf("the loaded node saw %v, want exactly [%s]", ops, opRunCleanup)
			}

			if ops := fix.opsOf("dep-0", 1); len(ops) != 0 {
				t.Errorf("the node after it saw %v, want nothing, since it was skipped", ops)
			}

			if fix.sentTo("dep-1") {
				t.Error("the task went on to its second instance after a node's outcome was lost")
			}

			node := paused.Instances[0].Command.VMs[0]
			if node.Phase != CommandPhaseSettled || node.Status != CommandStatusFailed || !strings.Contains(node.Reason, "outcome is not known") {
				t.Errorf("the node is %s/%s with reason %q, want settled and failed with an unknown outcome", node.Phase, node.Status, node.Reason)
			}

			if node.ExitCode == nil || *node.ExitCode != 7 {
				t.Errorf("the node's exit code is %v, want the 7 cleanup found", node.ExitCode)
			}

			if got := paused.Instances[0].Command.VMs[1].Phase; got != CommandPhaseSkipped {
				t.Errorf("the node after it is %s, want skipped", got)
			}

			if paused.Instances[1].Status != InstanceStatusPending || paused.FailedCount != 1 {
				t.Errorf("the second instance is %s and %d instances failed, want pending and one", paused.Instances[1].Status, paused.FailedCount)
			}

			fix.assertNeverSentTo("dep-0", 0)
			fix.assertNoCommandText()
		})
	}
}

func TestLoadedParallelNodesEachGetTheirOwnCleanup(t *testing.T) {
	fix := newTaskFixtureFrom(t, 1, 3, restartedWith(map[int]CommandPhase{0: CommandPhaseSent, 1: CommandPhaseSent}), withParallel(2))

	done := fix.waitFor("the task to finish", func(task UpgradeTask) bool { return task.Status == TaskStatusFailed })

	for index := 0; index < 2; index++ {
		if ops := fix.opsOf("dep-0", index); !slices.Equal(ops, []string{opRunCleanup}) {
			t.Errorf("node %d saw %v, want exactly [%s]", index, ops, opRunCleanup)
		}
	}

	if ops := fix.opsOf("dep-0", 2); len(ops) != 0 {
		t.Errorf("the pending node saw %v, want nothing, since it was skipped", ops)
	}

	if got := phases(done.Instances[0]); !slices.Equal(got, []CommandPhase{CommandPhaseSettled, CommandPhaseSettled, CommandPhaseSkipped}) {
		t.Errorf("phases = %v, want two settled and one skipped", got)
	}

	if done.Instances[0].Status != InstanceStatusFailed {
		t.Errorf("the instance is %s, want failed", done.Instances[0].Status)
	}

	fix.assertNoCommandText()
}

// A create that answered "never started" can leave a confirmed record, so
// a loaded task that did nothing waits for an operator and sends nothing.
func TestLoadedNeverStartedTaskPausesAndSendsNothing(t *testing.T) {
	fix := newTaskFixtureFrom(t, 2, 1, func(_ *taskFixture, task *UpgradeTask) {
		task.CreationConfirmed = true
		task.ClientRequestID = "request-1"
	})

	paused := fix.snapshot()
	if paused.Status != TaskStatusPaused || !paused.Paused {
		t.Fatalf("the task is %s paused=%v, want paused", paused.Status, paused.Paused)
	}

	for _, inst := range paused.Instances {
		if inst.Error != commandNeverStartedMessage || inst.Status != InstanceStatusPending {
			t.Errorf("instance %s is %s with %q, want pending with the never-started message", inst.InstanceID, inst.Status, inst.Error)
		}
	}

	if fix.owns() || len(fix.recorded()) != 0 {
		t.Errorf("the never-started task was queued or sent %v", fix.recorded())
	}

	if _, _, found := fix.manager.requests.Lookup("request-1"); !found {
		t.Error("the client request ID of the loaded command task is not indexed")
	}

	err := fix.busyWith(0)
	if err == nil || !strings.Contains(err.Error(), "command") {
		t.Errorf("another task reserved an instance of the never-started task: %v", err)
	}

	fix.assertNoCommandText()
}

func TestResumeOfALoadedNeverStartedTaskRunsIt(t *testing.T) {
	fix := newTaskFixtureFrom(t, 1, 1, func(_ *taskFixture, task *UpgradeTask) {
		task.CreationConfirmed = true
	})

	err := fix.manager.ResumeTask(context.Background(), fix.task.ID)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}

	done := fix.waitFor("the task to complete", func(task UpgradeTask) bool { return task.Status == TaskStatusCompleted })

	if ops := fix.opsOf("dep-0", 0); !slices.Equal(ops, []string{opSudoCheck, opRunPrepare, opRunExec, opRunCleanup}) {
		t.Errorf("the node saw %v, want the command's four operations", ops)
	}

	if done.Instances[0].Error != "" {
		t.Errorf("the instance still says %q after the resume", done.Instances[0].Error)
	}

	fix.assertNoCommandText()
}

func TestResumeOfALoadedNeverStartedTaskIsRefusedWhenTheSwitchIsOff(t *testing.T) {
	fix := newTaskFixtureFrom(t, 1, 1, func(fix *taskFixture, task *UpgradeTask) {
		task.CreationConfirmed = true
		task.Command.RunAs = "root"
		fix.setRoot(false)
	})

	err := fix.manager.ResumeTask(context.Background(), fix.task.ID)
	if err == nil {
		t.Fatal("a never-started root task resumed while allow_root is off")
	}

	if got := fix.snapshot(); got.Status != TaskStatusPaused || !got.Paused || len(fix.recorded()) != 0 {
		t.Errorf("the task is %s paused=%v and sent %v, want it left paused and silent", got.Status, got.Paused, fix.recorded())
	}

	fix.assertNoCommandText()
}

func TestLoadedTaskTheAuthorizerRefusesPausesWithAReason(t *testing.T) {
	fix := newTaskFixtureFrom(t, 2, 1, func(fix *taskFixture, task *UpgradeTask) {
		task.CreationConfirmed = true
		task.Command.RunAs = "root"
		fix.setRoot(false)

		// The first instance already ran, so the task is not one that
		// sent nothing.
		task.Status = TaskStatusRunning
		task.Instances[0].Status = InstanceStatusSuccess
		task.Instances[0].Command.VMs[0].Phase = CommandPhaseSettled
		task.Instances[0].Command.VMs[0].Status = CommandStatusSucceeded
	})

	paused := fix.waitFor("the task to pause", func(task UpgradeTask) bool { return task.Status == TaskStatusPaused && task.Paused })

	if !strings.Contains(paused.Instances[1].Error, "no longer allowed") {
		t.Errorf("the second instance says %q, want the refusal", paused.Instances[1].Error)
	}

	if paused.Instances[1].Status != InstanceStatusPending {
		t.Errorf("the second instance is %s, want pending, so a resume runs what is left", paused.Instances[1].Status)
	}

	if len(fix.recorded()) != 0 {
		t.Errorf("the refused task sent %v", fix.recorded())
	}

	if fix.owns() {
		t.Error("the refused task is still owned")
	}

	// A paused task keeps its unfinished instances held, as any paused
	// command task does.
	err := fix.busyWith(1)
	if err == nil || !strings.Contains(err.Error(), "command") {
		t.Errorf("another task reserved an instance of the paused task: %v", err)
	}

	err = fix.manager.ResumeTask(context.Background(), fix.task.ID)
	if err == nil {
		t.Fatal("a root task resumed while allow_root is off")
	}

	fix.setRoot(true)

	err = fix.manager.ResumeTask(context.Background(), fix.task.ID)
	if err != nil {
		t.Fatalf("resume once root is allowed: %v", err)
	}

	fix.waitFor("the task to complete", func(task UpgradeTask) bool { return task.Status == TaskStatusCompleted })
	fix.assertNoCommandText()
}

func TestLoadedPausedRootTaskIsRefusedAtResume(t *testing.T) {
	fix := newTaskFixtureFrom(t, 1, 1, func(fix *taskFixture, task *UpgradeTask) {
		task.CreationConfirmed = true
		task.Status = TaskStatusPaused
		task.Paused = true
		task.Command.RunAs = "root"
		fix.setRoot(false)
	})

	err := fix.manager.ResumeTask(context.Background(), fix.task.ID)
	if err == nil {
		t.Fatal("a paused root task resumed while allow_root is off")
	}

	if status := fix.snapshot(); status.Status != TaskStatusPaused || !status.Paused || len(fix.recorded()) != 0 {
		t.Errorf("the task is %s paused=%v after the refusal and sent %v, want it left paused and silent", status.Status, status.Paused, fix.recorded())
	}

	fix.assertNoCommandText()
}

func TestLoadedFinishedTasksAreLoadedAsTheyAre(t *testing.T) {
	for _, status := range []TaskStatus{TaskStatusCompleted, TaskStatusFailed, TaskStatusCancelled} {
		t.Run(string(status), func(t *testing.T) {
			var before []byte

			fix := newTaskFixtureFrom(t, 1, 2, func(_ *taskFixture, task *UpgradeTask) {
				task.CreationConfirmed = true
				task.Status = status
				task.Instances[0].Status = InstanceStatusSuccess

				for i := range task.Instances[0].Command.VMs {
					task.Instances[0].Command.VMs[i].Phase = CommandPhaseSettled
					task.Instances[0].Command.VMs[i].Status = CommandStatusSucceeded
				}

				before, _ = json.Marshal(task)
			})

			snapshot := fix.snapshot()

			after, err := json.Marshal(&snapshot)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}

			if string(before) != string(after) {
				t.Errorf("the loaded %s task changed", status)
			}

			if len(fix.recorded()) != 0 || fix.owns() {
				t.Errorf("the loaded %s task ran or is owned", status)
			}

			if err := fix.busyWith(0); err != nil {
				t.Errorf("the loaded %s task holds its instance: %v", status, err)
			}

			fix.assertNoCommandText()
		})
	}
}

func TestLoadedRunningTaskHoldsItsInstancesAsACommand(t *testing.T) {
	fix := newTaskFixtureFrom(t, 2, 1, func(_ *taskFixture, task *UpgradeTask) {
		task.CreationConfirmed = true
		task.Status = TaskStatusPaused
		task.Paused = true
		task.Instances[0].Status = InstanceStatusRunning
	})

	for instance := 0; instance < 2; instance++ {
		err := fix.busyWith(instance)
		if err == nil || !strings.Contains(err.Error(), "command") {
			t.Errorf("another task reserved instance %d of a loaded command task, or the refusal does not name the command: %v", instance, err)
		}
	}

	fix.assertNoCommandText()
}

func TestLoadedCommandTasksOfOneInstanceFailTheLaterOne(t *testing.T) {
	fix := newTaskFixtureFrom(t, 1, 1, func(fix *taskFixture, task *UpgradeTask) {
		task.CreationConfirmed = true
		task.Status = TaskStatusPaused
		task.Paused = true
		task.CreatedAt = loadedStart

		later := &UpgradeTask{}
		raw, _ := json.Marshal(task)
		_ = json.Unmarshal(raw, later)
		later.ID = "later-task"
		later.CreatedAt = loadedStart.Add(time.Hour)
		fix.store(later)
	})

	fix.manager.mu.RLock()
	later := fix.manager.tasks["later-task"]
	failed := later.Instances[0].Status == InstanceStatusFailed
	reason := later.Instances[0].Error
	fix.manager.mu.RUnlock()

	if !failed || !strings.Contains(reason, "reserved by task "+fix.task.ID) {
		t.Errorf("the later command task's instance is failed=%v with %q, want it failed and naming the first task", failed, reason)
	}
}

func TestLoadedSurvivorKeepsTheTaskPausedAndTheInstanceHeld(t *testing.T) {
	fix := newTaskFixtureFrom(t, 2, 2, func(_ *taskFixture, task *UpgradeTask) {
		task.CreationConfirmed = true
		task.Status = TaskStatusRunning
		task.Instances[0].Status = InstanceStatusFailed
		task.Instances[0].CompletedAt = &loadedStart
		task.FailedCount = 1

		node := &task.Instances[0].Command.VMs[0]
		node.Phase = CommandPhaseSettled
		node.Status = CommandStatusFailed
		node.Survived = true
	})

	paused := fix.snapshot()
	if paused.Status != TaskStatusPaused || !paused.Paused {
		t.Errorf("the task is %s paused=%v, want paused", paused.Status, paused.Paused)
	}

	if fix.owns() || len(fix.recorded()) != 0 {
		t.Error("a task with surviving processes ran or was queued")
	}

	err := fix.busyWith(0)
	if err == nil || !strings.Contains(err.Error(), "command") {
		t.Errorf("another task reserved the instance of a survived node, or the refusal does not name the command: %v", err)
	}

	if got := paused.Instances[0].Command.VMs[1].Phase; got != CommandPhaseSkipped {
		t.Errorf("the node after the survivor is %s, want skipped", got)
	}

	err = fix.manager.CancelTask(context.Background(), fix.task.ID)
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}

	fix.waitFor("the cancel to release the hold", func(UpgradeTask) bool { return fix.busyWith(0) == nil })

	fix.assertNoCommandText()
}

func TestLoadedFailedNodeSkipsItsPendingNodesAndIsNeverDriven(t *testing.T) {
	fix := newTaskFixtureFrom(t, 1, 3, func(_ *taskFixture, task *UpgradeTask) {
		task.CreationConfirmed = true
		task.Status = TaskStatusRunning
		task.Instances[0].Status = InstanceStatusRunning

		node := &task.Instances[0].Command.VMs[0]
		node.Phase = CommandPhaseSettled
		node.Status = CommandStatusFailed
	})

	done := fix.waitFor("the task to finish", func(task UpgradeTask) bool { return task.Status == TaskStatusFailed })

	if got := phases(done.Instances[0]); !slices.Equal(got, []CommandPhase{CommandPhaseSettled, CommandPhaseSkipped, CommandPhaseSkipped}) {
		t.Errorf("phases = %v, want the failed node settled and the rest skipped", got)
	}

	if len(fix.recorded()) != 0 {
		t.Errorf("the loaded task sent %v to nodes after a failure", fix.recorded())
	}

	if done.Instances[0].Status != InstanceStatusFailed {
		t.Errorf("the instance is %s, want failed", done.Instances[0].Status)
	}

	fix.assertNoCommandText()
}

func TestLoadedOrphanCommandTaskIsFailedAndNeverDriven(t *testing.T) {
	fix := newTaskFixtureFrom(t, 2, 2, func(_ *taskFixture, task *UpgradeTask) {
		task.CreationConfirmed = false
		task.ClientRequestID = "orphan-request"
		task.Status = TaskStatusPending
	})

	failed := fix.snapshot()
	if failed.Status != TaskStatusFailed {
		t.Fatalf("the orphan is %s, want failed", failed.Status)
	}

	for _, inst := range failed.Instances {
		if inst.Status != InstanceStatusFailed {
			t.Errorf("instance %s is %s, want failed", inst.InstanceID, inst.Status)
		}

		for _, node := range inst.Command.VMs {
			if node.Phase == CommandPhasePending {
				t.Errorf("a node of instance %s is still pending in a failed orphan", inst.InstanceID)
			}
		}
	}

	if fix.owns() || len(fix.recorded()) != 0 {
		t.Errorf("the orphan was queued or sent %v", fix.recorded())
	}

	if _, _, found := fix.manager.requests.Lookup("orphan-request"); found {
		t.Error("the orphan indexed its client request ID")
	}

	if err := fix.busyWith(0); err != nil {
		t.Errorf("the orphan holds its instance: %v", err)
	}

	fix.assertNoCommandText()
}

func TestLoadedCancelledTaskKeepsItsHeldNodeAndCleansItOnResume(t *testing.T) {
	fix := newTaskFixtureFrom(t, 2, 2, func(fix *taskFixture, task *UpgradeTask) {
		task.CreationConfirmed = true
		task.Status = TaskStatusCancelled
		task.Paused = true
		task.Command.RunAs = "root"
		fix.setRoot(false)

		task.Instances[0].Status = InstanceStatusRunning
		task.Instances[0].Command.VMs[0].Phase = CommandPhaseStaged
		task.Instances[0].Command.VMs[0].StartedAt = &loadedStart
	})

	fix.manager.mu.RLock()
	waits := waitsCancelledLocked(fix.task)
	fix.manager.mu.RUnlock()

	if !waits {
		t.Fatal("the loaded cancelled task does not wait for a resume")
	}

	err := fix.busyWith(0)
	if err == nil || !strings.Contains(err.Error(), "command") {
		t.Errorf("another task reserved the instance of a cancelled task's staged node, or the refusal does not name the command: %v", err)
	}

	if len(fix.recorded()) != 0 {
		t.Errorf("the load sent %v before any resume", fix.recorded())
	}

	// The resume needs only the broker's ordinary authorization, so it
	// goes through with root runs off.
	err = fix.manager.ResumeTask(context.Background(), fix.task.ID)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}

	done := fix.waitFor("the instance to end", func(task UpgradeTask) bool { return task.Instances[0].Status == InstanceStatusCancelled })

	if ops := fix.opsOf("dep-0", 0); !slices.Equal(ops, []string{opRunCleanup}) {
		t.Errorf("the staged node saw %v, want exactly [%s]", ops, opRunCleanup)
	}

	if len(fix.recorded()) != 1 {
		t.Errorf("the resume sent %v, want one cleanup", fix.recorded())
	}

	if done.Status != TaskStatusCancelled {
		t.Errorf("the task is %s, want cancelled", done.Status)
	}

	waitUntil(t, "the cleaned instance to be released", func() bool { return fix.busyWith(0) == nil })
	fix.assertNoCommandText()
}

func TestLoadedCancelledTaskWithNothingHeldEndsItsRunningInstance(t *testing.T) {
	fix := newTaskFixtureFrom(t, 2, 2, func(_ *taskFixture, task *UpgradeTask) {
		task.CreationConfirmed = true
		task.Status = TaskStatusCancelled
		task.Instances[0].Status = InstanceStatusRunning
		task.Instances[0].Command.VMs[0].Phase = CommandPhaseSettled
		task.Instances[0].Command.VMs[0].Status = CommandStatusSucceeded
	})

	done := fix.waitFor("the instances to end", func(task UpgradeTask) bool {
		return task.Instances[0].Status == InstanceStatusCancelled && task.Instances[1].Status == InstanceStatusSkipped
	})

	if got := phases(done.Instances[0]); !slices.Equal(got, []CommandPhase{CommandPhaseSettled, CommandPhaseSkipped}) {
		t.Errorf("phases = %v, want the pending node skipped", got)
	}

	if len(fix.recorded()) != 0 {
		t.Errorf("the load sent %v for a cancelled task", fix.recorded())
	}

	waitUntil(t, "the instance to be released", func() bool { return fix.busyWith(0) == nil })

	fix.assertNoCommandText()
}

func TestLoadedCancelledTaskWithAHeldNodeCleansItWithoutAResume(t *testing.T) {
	fix := newTaskFixtureFrom(t, 1, 2, func(_ *taskFixture, task *UpgradeTask) {
		task.CreationConfirmed = true
		task.Status = TaskStatusCancelled
		task.Instances[0].Status = InstanceStatusRunning
		task.Instances[0].Command.VMs[0].Phase = CommandPhaseSent
		task.Instances[0].Command.VMs[0].StartedAt = &loadedStart
	})

	done := fix.waitFor("the instance to end", func(task UpgradeTask) bool { return task.Instances[0].Status == InstanceStatusCancelled })

	if ops := fix.opsOf("dep-0", 0); !slices.Equal(ops, []string{opRunCleanup}) {
		t.Errorf("the sent node saw %v, want exactly [%s]", ops, opRunCleanup)
	}

	if ops := fix.opsOf("dep-0", 1); len(ops) != 0 {
		t.Errorf("the pending node saw %v, want nothing", ops)
	}

	if got := phases(done.Instances[0]); !slices.Equal(got, []CommandPhase{CommandPhaseSettled, CommandPhaseSkipped}) {
		t.Errorf("phases = %v, want the sent node settled and the pending one skipped", got)
	}

	fix.assertNoCommandText()
}

func TestLoadedPausedTaskWithAStagedNodeSettlesItOnResumeOnly(t *testing.T) {
	fix := newTaskFixtureFrom(t, 1, 2, func(_ *taskFixture, task *UpgradeTask) {
		task.CreationConfirmed = true
		task.Status = TaskStatusPaused
		task.Paused = true
		task.Instances[0].Status = InstanceStatusRunning
		task.Instances[0].Command.VMs[0].Phase = CommandPhaseStaged
		task.Instances[0].Command.VMs[0].StartedAt = &loadedStart
	})

	if len(fix.recorded()) != 0 {
		t.Fatalf("the load sent %v to a paused task", fix.recorded())
	}

	err := fix.manager.ResumeTask(context.Background(), fix.task.ID)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}

	fix.waitFor("the task to finish", func(task UpgradeTask) bool { return task.Status == TaskStatusFailed })

	for index := 0; index < 2; index++ {
		if ops := fix.opsOf("dep-0", index); index == 0 && !slices.Equal(ops, []string{opRunCleanup}) || index == 1 && len(ops) != 0 {
			t.Errorf("node %d saw %v, want only the staged node's cleanup", index, ops)
		}
	}

	fix.assertNoCommandText()
}

// A save of the sent phase that fails after prepare leaves the staged
// directory on the VM and the node staged in the record. The broker that
// restarts settles it through cleanup and never sends exec.
func TestLoadedNodeLeftStagedByAFailedSentSaveIsCleanedOnly(t *testing.T) {
	before := newTaskFixture(t, 1, 1)
	before.vault.failPhase = CommandPhaseSent
	before.queue()

	stranded := before.waitFor("the task to pause with its node staged", func(task UpgradeTask) bool {
		return task.Paused && task.Instances[0].Command.VMs[0].Phase == CommandPhaseStaged
	})

	if ops := before.opsOf("dep-0", 0); slices.Contains(ops, opRunExec) {
		t.Fatalf("the first broker sent exec after the sent save failed: %v", ops)
	}

	fix := newTaskFixtureFrom(t, 1, 1, func(_ *taskFixture, task *UpgradeTask) {
		*task = stranded
		task.CreationConfirmed = true
		task.Status = TaskStatusRunning
		task.Paused = false
	})

	done := fix.waitFor("the task to finish", func(task UpgradeTask) bool { return task.Status == TaskStatusFailed })

	if ops := fix.opsOf("dep-0", 0); !slices.Equal(ops, []string{opRunCleanup}) {
		t.Errorf("the staged node saw %v, want exactly [%s]", ops, opRunCleanup)
	}

	if done.Instances[0].Command.VMs[0].Phase != CommandPhaseSettled {
		t.Errorf("the node is %s, want settled", done.Instances[0].Command.VMs[0].Phase)
	}

	before.assertNoCommandText()
	fix.assertNoCommandText()
}

// A refusal that is on an instance when the operator resumes is cleared, so
// a later failure shows its own reason.
func TestResumeAfterARefusedLoadClearsTheRefusalFromAFailure(t *testing.T) {
	fix := newTaskFixtureFrom(t, 1, 2, func(fix *taskFixture, task *UpgradeTask) {
		task.CreationConfirmed = true
		task.Command.RunAs = "root"
		fix.setRoot(false)

		task.Status = TaskStatusRunning
		task.Instances[0].Status = InstanceStatusRunning
		task.Instances[0].Command.VMs[0].Phase = CommandPhaseSettled
		task.Instances[0].Command.VMs[0].Status = CommandStatusSucceeded
	})
	fix.exits["dep-0/1"] = 3

	paused := fix.snapshot()
	if paused.Status != TaskStatusPaused || !strings.HasPrefix(paused.Instances[0].Error, commandRefusedMessage) {
		t.Fatalf("the task is %s with %q, want it paused with the refusal", paused.Status, paused.Instances[0].Error)
	}

	fix.setRoot(true)

	err := fix.manager.ResumeTask(context.Background(), fix.task.ID)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}

	done := fix.waitFor("the task to fail", func(task UpgradeTask) bool { return task.Status == TaskStatusFailed })

	if strings.Contains(done.Instances[0].Error, "no longer allowed") {
		t.Errorf("the failed instance still shows the refusal: %q", done.Instances[0].Error)
	}

	fix.assertNoCommandText()
}

// A task a restart paused because the authorizer refuses it can still be
// cancelled, and the resume that follows only cleans, so it needs no
// authorization and a node left sent is not stranded.
func TestLoadedRefusedTaskCancelledThenResumedOnlyCleans(t *testing.T) {
	fix := newTaskFixtureFrom(t, 2, 2, func(fix *taskFixture, task *UpgradeTask) {
		task.CreationConfirmed = true
		task.Command.RunAs = "root"
		fix.setRoot(false)

		task.Status = TaskStatusRunning
		task.Instances[0].Status = InstanceStatusRunning
		task.Instances[0].Command.VMs[0].Phase = CommandPhaseSent
		task.Instances[0].Command.VMs[0].StartedAt = &loadedStart
	})

	paused := fix.snapshot()
	if paused.Status != TaskStatusPaused || !paused.Paused {
		t.Fatalf("the task is %s paused=%v, want paused", paused.Status, paused.Paused)
	}

	err := fix.manager.ResumeTask(context.Background(), fix.task.ID)
	if err == nil {
		t.Fatal("a refused root task resumed while allow_root is off")
	}

	err = fix.manager.CancelTask(context.Background(), fix.task.ID)
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}

	fix.manager.mu.RLock()
	waits := waitsCancelledLocked(fix.task)
	fix.manager.mu.RUnlock()

	if !waits {
		t.Fatal("the cancelled task does not wait for a resume")
	}

	if err := fix.busyWith(0); err == nil {
		t.Error("the cancelled task freed the instance with a sent node")
	}

	err = fix.manager.ResumeTask(context.Background(), fix.task.ID)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}

	done := fix.waitFor("the instances to end", func(task UpgradeTask) bool {
		return task.Instances[0].Status == InstanceStatusCancelled && task.Instances[1].Status == InstanceStatusSkipped
	})

	if got := fix.recorded(); len(got) != 1 || got[0].op != opRunCleanup || got[0].index != 0 {
		t.Errorf("the resume sent %v, want one cleanup to the sent node", got)
	}

	if done.Status != TaskStatusCancelled {
		t.Errorf("the task is %s, want cancelled", done.Status)
	}

	waitUntil(t, "the instance to be released", func() bool { return fix.busyWith(0) == nil })
	fix.assertNoCommandText()
}
