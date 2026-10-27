package upgrade

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

// strandNodes leaves the first count nodes of the fixture's only instance
// sent by an earlier run, and the task paused with the instance running.
func strandNodes(fix *taskFixture, count int) {
	started := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)

	fix.manager.mu.Lock()
	defer fix.manager.mu.Unlock()

	for vm := 0; vm < count; vm++ {
		node := &fix.task.Instances[0].Command.VMs[vm]
		node.Phase = CommandPhaseSent
		node.StartedAt = &started
	}

	fix.task.Status = TaskStatusPaused
	fix.task.Paused = true
	fix.task.Instances[0].Status = InstanceStatusRunning
}

// Two nodes are stranded. The first is cleaned and settles failed, and the
// cleanup of the second does not report cleanly. The instance must stay
// open with the second node still sent, so the next resume cleans it, and
// the failure of the first still stops the deployment then.
func TestASecondStrandedNodeIsCleanedByTheNextResume(t *testing.T) {
	fix := newTaskFixture(t, 1, 3)

	fix.override = func(_ context.Context, deployment string, index int, op string) *opAnswer {
		if deployment == "dep-0" && index == 1 && op == opRunCleanup {
			return &opAnswer{err: errors.New("the session broke")}
		}

		return nil
	}

	strandNodes(fix, 2)

	err := fix.manager.ResumeTask(context.Background(), fix.task.ID)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}

	fix.waitFor("the task to pause again", func(task UpgradeTask) bool { return task.Status == TaskStatusPaused })
	waitUntil(t, "the processor to let go of the task", func() bool { return !fix.owns() })

	paused := fix.snapshot().Instances[0]
	if paused.Status != InstanceStatusRunning || paused.Command.VMs[1].Phase != CommandPhaseSent {
		t.Fatalf("the instance is %s with nodes %v, want running with the second node still sent", paused.Status, phases(paused))
	}

	if paused.Command.VMs[0].Phase != CommandPhaseSettled || paused.Command.VMs[2].Phase != CommandPhaseSkipped {
		t.Errorf("the nodes are %v, want the first settled and the pending one skipped", phases(paused))
	}

	fix.mu.Lock()
	fix.override = nil
	fix.mu.Unlock()

	err = fix.manager.ResumeTask(context.Background(), fix.task.ID)
	if err != nil {
		t.Fatalf("second resume: %v", err)
	}

	done := fix.waitFor("the task to finish", func(task UpgradeTask) bool {
		return task.Status == TaskStatusCompleted || task.Status == TaskStatusFailed
	})

	inst := done.Instances[0]
	if want := []CommandPhase{CommandPhaseSettled, CommandPhaseSettled, CommandPhaseSkipped}; !slices.Equal(phases(inst), want) {
		t.Errorf("the nodes are %v, want %v", phases(inst), want)
	}

	if inst.Status != InstanceStatusFailed {
		t.Errorf("the instance is %s, want failed", inst.Status)
	}

	if got := fix.countOps("dep-0", 1, opRunCleanup); got != 2 {
		t.Errorf("the second node saw %d cleanups, want 2", got)
	}

	if ops := fix.opsOf("dep-0", 2); len(ops) != 0 {
		t.Errorf("the pending node saw %v, want nothing", ops)
	}

	fix.assertNoCommandText()
}

// Two nodes are stranded, and the cleanup of the first finds processes
// that outlived KILL, which pauses the task. The second node is still
// cleaned before the instance ends, so no node is left sent behind a
// settled instance.
func TestASurvivingStrandedNodeLeavesNoOtherNodeSent(t *testing.T) {
	fix := newTaskFixture(t, 1, 3)
	fix.cleanups["dep-0/0"] = "cleanup started=yes exit=- killed=survived"

	strandNodes(fix, 2)

	err := fix.manager.ResumeTask(context.Background(), fix.task.ID)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}

	paused := fix.waitFor("the instance to end", func(task UpgradeTask) bool {
		return task.Status == TaskStatusPaused && task.Instances[0].Status.settled()
	})

	inst := paused.Instances[0]
	if want := []CommandPhase{CommandPhaseSettled, CommandPhaseSettled, CommandPhaseSkipped}; !slices.Equal(phases(inst), want) {
		t.Errorf("the nodes are %v, want %v", phases(inst), want)
	}

	if inst.Status != InstanceStatusFailed || !inst.Command.VMs[0].Survived {
		t.Errorf("the instance is %s with the first node survived %v, want failed and survived", inst.Status, inst.Command.VMs[0].Survived)
	}

	if got := fix.countOps("dep-0", 1, opRunCleanup); got != 1 {
		t.Errorf("the second node saw %d cleanups, want 1", got)
	}

	if ops := fix.opsOf("dep-0", 2); len(ops) != 0 {
		t.Errorf("the pending node saw %v, want nothing", ops)
	}

	fix.assertNoCommandText()
}

// A node failed in an earlier pass, and the halt it caused never reached
// the record, so the next pass finds a pending node and nothing stranded.
// The failure still halts the deployment, so the pending node is skipped
// and never sent.
func TestANodeThatFailedEarlierHaltsTheNextPass(t *testing.T) {
	fix := newTaskFixture(t, 1, 2)

	fix.manager.mu.Lock()
	failed := &fix.task.Instances[0].Command.VMs[0]
	failed.Phase = CommandPhaseSettled
	failed.Status = CommandStatusFailed
	fix.task.Status = TaskStatusPaused
	fix.task.Paused = true
	fix.task.Instances[0].Status = InstanceStatusRunning
	fix.manager.mu.Unlock()

	err := fix.manager.ResumeTask(context.Background(), fix.task.ID)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}

	done := fix.waitFor("the task to finish", func(task UpgradeTask) bool {
		return task.Status == TaskStatusCompleted || task.Status == TaskStatusFailed
	})

	inst := done.Instances[0]
	if want := []CommandPhase{CommandPhaseSettled, CommandPhaseSkipped}; !slices.Equal(phases(inst), want) || inst.Status != InstanceStatusFailed {
		t.Errorf("the instance is %s with nodes %v, want failed with %v", inst.Status, phases(inst), want)
	}

	if ops := fix.opsOf("dep-0", 1); len(ops) != 0 {
		t.Errorf("the pending node saw %v, want nothing", ops)
	}

	fix.assertNoCommandText()
}

// The cleanup of the last stranded node does not report cleanly, which
// pauses the task with a message that it waits for a resume. Once a resume
// cleans the node and the instance ends, the instance gives the reason of
// the node that failed, not the message of the pause that is over. When a
// node's processes also outlived KILL, the resume checks them first and
// keeps the instance open and held, saying so, while they live, and the
// instance ends only once a later check finds them ended.
func TestAnInstanceEndsWithoutTheStrandPauseMessage(t *testing.T) {
	cases := []struct {
		name     string
		stranded int
		survivor int
	}{
		{name: "two passes", stranded: 2, survivor: -1},
		{name: "a survivor", stranded: 2, survivor: 0},
		{name: "a later survivor", stranded: 3, survivor: 1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fix := newTaskFixture(t, 1, 3)
			last := tc.stranded - 1

			if tc.survivor >= 0 {
				fix.cleanups[nodeKey("dep-0", tc.survivor)] = cleanupSurvived
			}

			fix.override = func(_ context.Context, deployment string, index int, op string) *opAnswer {
				if deployment == "dep-0" && index == last && op == opRunCleanup {
					return &opAnswer{err: errors.New("the session broke")}
				}

				return nil
			}

			strandNodes(fix, tc.stranded)

			err := fix.manager.ResumeTask(context.Background(), fix.task.ID)
			if err != nil {
				t.Fatalf("resume: %v", err)
			}

			paused := fix.waitFor("the task to pause again", func(task UpgradeTask) bool { return task.Status == TaskStatusPaused })
			waitUntil(t, "the processor to let go of the task", func() bool { return !fix.owns() })

			if !strings.Contains(paused.Instances[0].Error, "still waiting for a resume") {
				t.Fatalf("the paused instance says %q, want the strand pause message", paused.Instances[0].Error)
			}

			fix.mu.Lock()
			fix.override = nil
			fix.mu.Unlock()

			if tc.survivor >= 0 {
				err = fix.manager.ResumeTask(context.Background(), fix.task.ID)
				if err != nil {
					t.Fatalf("resume with the survivor alive: %v", err)
				}

				waitUntil(t, "the recheck to let go of the task", func() bool { return !fix.owns() && fix.snapshot().Paused })

				held := fix.snapshot().Instances[0]
				if held.Status.settled() || !strings.Contains(held.Error, "still survive KILL") {
					t.Fatalf("the instance is %s saying %q, want it open and saying the processes still survive", held.Status, held.Error)
				}

				fix.setCleanup(nodeKey("dep-0", tc.survivor), cleanupEnded)
			}

			err = fix.manager.ResumeTask(context.Background(), fix.task.ID)
			if err != nil {
				t.Fatalf("second resume: %v", err)
			}

			ended := fix.waitFor("the instance to end", func(task UpgradeTask) bool { return task.Instances[0].Status.settled() })

			inst := ended.Instances[0]

			if inst.Status != InstanceStatusFailed || inst.Error != inst.Command.VMs[0].Reason {
				t.Errorf("the instance is %s with error %q, want failed with node 0's reason %q", inst.Status, inst.Error, inst.Command.VMs[0].Reason)
			}

			for i := range tc.stranded {
				if inst.Command.VMs[i].Survived {
					t.Errorf("node %d still records survivors", i)
				}
			}

			fix.assertNoCommandText()
		})
	}
}
