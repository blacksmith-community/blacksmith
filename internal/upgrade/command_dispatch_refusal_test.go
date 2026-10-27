package upgrade

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
)

// A command task the authorizer refuses when the queue reaches it is
// paused with the refusal on its instance, not left pending where no
// control reaches it. Its instance stays held, a resume runs it once the
// authorizer allows it, and a cancel frees its instance.
func TestDispatchRefusalPausesTheCommandTask(t *testing.T) {
	newRefused := func(t *testing.T) (*taskFixture, *atomic.Bool) {
		t.Helper()

		fix := newTaskFixture(t, 1, 1)

		var refusing atomic.Bool

		refusing.Store(true)

		fix.authHook = func(context.Context) error {
			if refusing.Load() {
				return errors.New("the authorizer refuses the stored task")
			}

			return nil
		}

		fix.queue()

		return fix, &refusing
	}

	t.Run("the task pauses with a reason and keeps its hold", func(t *testing.T) {
		fix, _ := newRefused(t)

		paused := fix.waitFor("the dispatcher to pause the task", func(task UpgradeTask) bool { return task.Status == TaskStatusPaused })

		inst := paused.Instances[0]
		if !paused.Paused || inst.Status != InstanceStatusPending || !strings.HasPrefix(inst.Error, commandRefusedMessage) {
			t.Errorf("the task paused=%v with instance %s and error %q, want paused with a pending instance and the refusal", paused.Paused, inst.Status, inst.Error)
		}

		if strings.Contains(inst.Error, "restarted") {
			t.Errorf("the reason %q blames a restart that did not happen", inst.Error)
		}

		if fix.owns() || len(fix.recorded()) != 0 {
			t.Errorf("the refused task is owned or sent %v", fix.recorded())
		}

		if err := fix.tryReserve(); err == nil {
			t.Error("the refused task let another task reserve its instance")
		}

		fix.assertNoCommandText()
	})

	t.Run("resume runs it once the authorizer allows it", func(t *testing.T) {
		fix, refusing := newRefused(t)

		fix.waitFor("the dispatcher to pause the task", func(task UpgradeTask) bool { return task.Status == TaskStatusPaused })

		if err := fix.manager.ResumeTask(context.Background(), fix.task.ID); err == nil {
			t.Fatal("resume was accepted while the authorizer still refuses the task")
		}

		refusing.Store(false)

		err := fix.manager.ResumeTask(context.Background(), fix.task.ID)
		if err != nil {
			t.Fatalf("resume: %v", err)
		}

		done := fix.waitFor("the resumed task to finish", func(task UpgradeTask) bool {
			return task.Status == TaskStatusCompleted || task.Status == TaskStatusFailed
		})

		if done.Instances[0].Status != InstanceStatusSuccess || done.Instances[0].Error != "" {
			t.Errorf("the instance is %s with error %q, want success with the refusal cleared", done.Instances[0].Status, done.Instances[0].Error)
		}

		fix.assertNoCommandText()
	})

	t.Run("cancel frees the instance", func(t *testing.T) {
		fix, _ := newRefused(t)

		fix.waitFor("the dispatcher to pause the task", func(task UpgradeTask) bool { return task.Status == TaskStatusPaused })

		err := fix.manager.CancelTask(context.Background(), fix.task.ID)
		if err != nil {
			t.Fatalf("cancel: %v", err)
		}

		if status := fix.snapshot().Status; status != TaskStatusCancelled {
			t.Errorf("the task is %s after the cancel, want cancelled", status)
		}

		if len(fix.recorded()) != 0 {
			t.Errorf("the cancelled task sent %v", fix.recorded())
		}

		waitUntil(t, "the cancelled task to free its instance", func() bool { return fix.busyWith(0) == nil })

		fix.assertNoCommandText()
	})
}

// A command task is cancelled after it was queued and before the
// dispatcher reads it, and the authorizer then refuses it at the queue.
// The task stays cancelled, so turning commands back on and resuming it
// never runs the command, and the instances it never started end.
func TestACancelledTaskRefusedAtDispatchStaysCancelled(t *testing.T) {
	fix := newTaskFixture(t, 2, 1)

	var refusing atomic.Bool

	refusing.Store(true)

	fix.authHook = func(context.Context) error {
		if refusing.Load() {
			return errors.New("the authorizer refuses the stored task")
		}

		return nil
	}

	fix.manager.mu.Lock()
	fix.task.Status = TaskStatusRunning
	fix.manager.owned[fix.task.ID] = true
	fix.manager.mu.Unlock()

	fix.cancel()
	fix.manager.enqueue(fix.task)

	waitUntil(t, "the dispatcher to let go of the task", func() bool { return !fix.owns() })

	got := fix.snapshot()
	if got.Status != TaskStatusCancelled || got.Paused {
		t.Errorf("the task is %s with paused %v after the refusal, want cancelled and not paused", got.Status, got.Paused)
	}

	for i, inst := range got.Instances {
		if inst.Status != InstanceStatusSkipped {
			t.Errorf("instance %d is %s, want skipped, since the task never started it", i, inst.Status)
		}
	}

	refusing.Store(false)

	if err := fix.manager.ResumeTask(context.Background(), fix.task.ID); err == nil {
		t.Error("a resume of the cancelled task was accepted")
	}

	if got := fix.snapshot().Status; got != TaskStatusCancelled {
		t.Errorf("the task is %s after the resume, want cancelled", got)
	}

	if len(fix.recorded()) != 0 {
		t.Errorf("the cancelled task sent %v", fix.recorded())
	}

	fix.waitReleased()
	fix.assertNoCommandText()
}

// The authorizer refuses a queued command task when the dispatcher first
// asks, and allows it by the time the dispatcher would pause it, as when
// the switch is turned back on in between. The task runs, rather than
// being let go running with no processor.
func TestAQueuedTaskAllowedAgainBeforeItsPauseRuns(t *testing.T) {
	fix := newTaskFixture(t, 1, 1)

	var checks atomic.Int32

	fix.authHook = func(context.Context) error {
		if checks.Add(1) == 1 {
			return errors.New("the authorizer refuses the stored task")
		}

		return nil
	}

	fix.queue()

	done := fix.waitFor("the task to finish", func(task UpgradeTask) bool {
		return task.Status == TaskStatusCompleted || task.Status == TaskStatusFailed
	})

	if done.Instances[0].Status != InstanceStatusSuccess || done.Paused {
		t.Errorf("the instance is %s with the task paused %v, want success", done.Instances[0].Status, done.Paused)
	}

	fix.assertNoCommandText()
}

// A command task with a sent node is cancelled while it is queued and
// owned, so the cancel leaves the node's cleanup to whatever processes
// the task next. The authorizer then refuses it at the queue, and the
// dispatcher lets it go. The node is still cleaned, it never runs again,
// and the instance is freed.
func TestACancelledTaskDroppedAtDispatchCleansItsSentNode(t *testing.T) {
	fix := newTaskFixture(t, 1, 2)

	fix.authHook = func(context.Context) error {
		return errors.New("the authorizer refuses the stored task")
	}

	strandNodes(fix, 1)

	fix.manager.mu.Lock()
	fix.task.Status = TaskStatusRunning
	fix.task.Paused = false
	fix.manager.owned[fix.task.ID] = true
	fix.manager.mu.Unlock()

	fix.cancel()
	fix.manager.enqueue(fix.task)

	done := fix.waitFor("the sent node to be cleaned", func(task UpgradeTask) bool {
		return task.Instances[0].Status.settled()
	})

	inst := done.Instances[0]
	if want := []CommandPhase{CommandPhaseSettled, CommandPhaseSkipped}; inst.Status != InstanceStatusCancelled || !slices.Equal(phases(inst), want) {
		t.Errorf("the instance is %s with nodes %v, want cancelled with %v", inst.Status, phases(inst), want)
	}

	if done.Status != TaskStatusCancelled {
		t.Errorf("the task is %s, want cancelled", done.Status)
	}

	if got := fix.countOps("dep-0", 0, opRunCleanup); got != 1 {
		t.Errorf("the sent node saw %d cleanups, want 1", got)
	}

	for _, op := range []string{opRunPrepare, opRunExec} {
		if got := fix.countOps("dep-0", 0, op) + fix.countOps("dep-0", 1, op); got != 0 {
			t.Errorf("the cancelled task saw %d %s operations, want none", got, op)
		}
	}

	fix.waitReleased()
	fix.assertNoCommandText()
}
