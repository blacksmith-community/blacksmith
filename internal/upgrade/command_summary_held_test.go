package upgrade

import (
	"slices"
	"strings"
	"testing"
)

// heldSummaryTask is a command task in the status with one instance in each
// state an instance of it can be in. The failed one has a node whose
// processes outlived KILL.
func heldSummaryTask(status TaskStatus) *UpgradeTask {
	task := newCommandTask()
	task.Status = status

	base := task.Instances[0]
	task.Instances = nil

	for _, state := range []struct {
		id       string
		status   InstanceStatus
		survived bool
	}{
		{"i-running", InstanceStatusRunning, false},
		{"i-pending", InstanceStatusPending, false},
		{"i-success", InstanceStatusSuccess, false},
		{"i-failed", InstanceStatusFailed, false},
		{"i-survived", InstanceStatusFailed, true},
		{"i-skipped", InstanceStatusSkipped, false},
	} {
		inst := base
		inst.InstanceID = state.id
		inst.Status = state.status
		inst.Command = base.Command.clone()
		inst.Command.VMs[0].Survived = state.survived
		task.Instances = append(task.Instances, inst)
	}

	return task
}

// The task list summary of an unfinished command task names the instances
// it holds, so a page that may not read the task's detail can still show
// them as busy. It names them by ID alone, and a task that has ended names
// only an instance with processes that outlived KILL.
func TestACommandTaskSummaryNamesTheInstancesItHolds(t *testing.T) {
	t.Parallel()

	want := []string{"i-running", "i-pending", "i-survived"}

	for _, status := range []TaskStatus{TaskStatusPending, TaskStatusRunning, TaskStatusPaused} {
		summary := heldSummaryTask(status).ToSummary()
		if !slices.Equal(summary.HeldInstanceIDs, want) {
			t.Errorf("a %s task's summary names %v, want %v", status, summary.HeldInstanceIDs, want)
		}

		raw := mustJSON(t, summary)
		if strings.Contains(raw, commandSecretText) || strings.Contains(raw, `"instances"`) || strings.Contains(raw, "dep-1") {
			t.Errorf("a %s task's summary carries more than the instance IDs", status)
		}
	}

	// A task that has ended still holds the instance whose processes
	// outlived KILL, and nothing else.
	for _, status := range []TaskStatus{TaskStatusCompleted, TaskStatusFailed, TaskStatusCancelled} {
		summary := heldSummaryTask(status).ToSummary()
		if !slices.Equal(summary.HeldInstanceIDs, []string{"i-survived"}) {
			t.Errorf("a %s task's summary names %v, want the instance with survivors", status, summary.HeldInstanceIDs)
		}
	}
}

// A command task that holds nothing says so with an empty list, so a page
// can tell it from the summary of a server that names no held instances at
// all. A task of another kind carries no list.
func TestASummaryOfACommandTaskThatHoldsNothingNamesAnEmptyList(t *testing.T) {
	t.Parallel()

	for _, status := range []TaskStatus{TaskStatusCompleted, TaskStatusFailed, TaskStatusCancelled} {
		task := heldSummaryTask(status)
		for i := range task.Instances {
			task.Instances[i].Command.VMs[0].Survived = false
		}

		if raw := mustJSON(t, task.ToSummary()); !strings.Contains(raw, `"held_instance_ids":[]`) {
			t.Errorf("a %s task's summary has no empty held_instance_ids list", status)
		}
	}

	upgrade := &UpgradeTask{ID: "upgrade-1", Kind: TaskKindUpgrade, Status: TaskStatusRunning}
	if raw := mustJSON(t, upgrade.ToSummary()); strings.Contains(raw, "held_instance_ids") {
		t.Error("an upgrade task's summary carries held_instance_ids")
	}
}

// A cancelled command task with a node still to clean holds that node's
// instance until its cleanup returns, as while it waits for a resume to run
// the cleanup, and it holds the instance with processes that outlived
// KILL, so its summary names those two and no other.
func TestACancelledCommandTaskSummaryNamesTheInstanceItHoldsForCleanup(t *testing.T) {
	t.Parallel()

	task := heldSummaryTask(TaskStatusCancelled)
	task.Paused = true
	task.Instances[0].Command.VMs[0].Phase = CommandPhaseSent

	if got := task.ToSummary().HeldInstanceIDs; !slices.Equal(got, []string{"i-running", "i-survived"}) {
		t.Errorf("the cancelled task's summary names %v, want the instances held for cleanup", got)
	}
}
