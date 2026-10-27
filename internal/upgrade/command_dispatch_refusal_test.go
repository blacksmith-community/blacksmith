package upgrade

import (
	"context"
	"errors"
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
