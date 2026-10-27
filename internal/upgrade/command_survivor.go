package upgrade

import (
	"context"
	"errors"
	"fmt"
)

// The messages a check of a node whose processes outlived KILL leaves on
// its instance while the instance stays held. Each takes the node's group,
// its index, and the instance ID.
const (
	survivorUnreachable = "Blacksmith could not reach %s/%d to check the processes of the command that survived KILL there, " +
		"so instance %s stays held. Once the node can be reached, use Resume cleanup to check them again."
	survivorUnclean = "The cleanup on %s/%d did not report cleanly, so Blacksmith can't tell whether the processes of the command " +
		"that survived KILL still run. Instance %s stays held. Use Resume cleanup to check them again."
	survivorStill = "Processes of the command still survive KILL on %s/%d, so instance %s stays held. " +
		"Use Resume cleanup to check them again."
	survivorUnknown = "Cleanup found no record of the command on %s/%d, so Blacksmith can't tell whether the processes " +
		"that survived KILL still run. Instance %s stays held. Recreating the VM ends them, and Resume cleanup then releases the instance."
	survivorUnchecked = "Cleanup on %s/%d found no record of the processes that survived KILL there, so it did not check them, " +
		"and Blacksmith can't tell whether they still run. Instance %s stays held. Recreating the VM ends them, and Resume cleanup then releases the instance."
)

// cleanupOnlyDone is what the next instance of a task says once a Resume
// cleanup has nothing more to clean and the task pauses before the command
// would be sent.
const cleanupOnlyDone = "Resume cleanup finished, and it sends no command. Resume to run the command on the nodes that have not run it."

// recheckSurvivors runs cleanup again on each node of the instance whose
// processes outlived KILL, and reports whether any of them still holds the
// instance. A node is resolved only by evidence that nothing of the
// command is left on its VM: cleanup reporting killed=yes, which the
// wrapper prints only once no process its survivors record names still
// lives, or a listing that shows the VM was replaced or removed. A director
// error, a node that can't be reached, a cleanup that does not report
// cleanly, a record that can't say what ran, and a record with killed=no
// are no evidence, so the node stays as it is, the instance stays held, and
// the task waits for Resume cleanup. killed=no means the wrapper found no
// survivors record, so it never checked the processes.
func (m *Manager) recheckSurvivors(ctx context.Context, sessions *patchSessions, task *UpgradeTask, inst *InstanceUpgrade) bool {
	held := false

	for vm := range inst.Command.VMs {
		if !m.commandNodeSurvived(inst, vm) {
			continue
		}

		if m.recheckSurvivor(ctx, sessions, task, inst, vm) {
			held = true
		}
	}

	return held
}

// commandNodeSurvived reports whether the node records processes that
// outlived KILL.
func (m *Manager) commandNodeSurvived(inst *InstanceUpgrade, vm int) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return inst.Command.VMs[vm].Survived
}

// recheckSurvivor checks one node whose processes outlived KILL, and
// reports whether it still holds the instance.
func (m *Manager) recheckSurvivor(ctx context.Context, sessions *patchSessions, task *UpgradeTask, inst *InstanceUpgrade, vm int) bool {
	run := m.newCommandRun(sessions, task, inst, vm)
	identity := run.identity

	err := run.buildInputs()

	var live CommandVMIdentity

	if err == nil {
		var outcome *commandOutcome

		live, outcome, err = run.relist(ctx)
		if errors.Is(err, errCommandVMChanged) {
			reason := fmt.Sprintf("VM %s/%d was replaced or removed after processes of the command survived KILL on it, so nothing of them is left.", identity.Group, identity.Index)

			return m.clearSurvivor(ctx, run, reason, false)
		}

		if outcome != nil {
			err = outcome.err
		}
	}

	if err == nil {
		err = run.connect(ctx, live)
	}

	if err != nil {
		m.holdSurvivor(ctx, task, inst, fmt.Sprintf(survivorUnreachable, identity.Group, identity.Index, inst.InstanceID), err)

		return true
	}

	record, survived, err := run.cleanup(ctx)

	switch {
	case err != nil:
		m.holdSurvivor(ctx, task, inst, fmt.Sprintf(survivorUnclean, identity.Group, identity.Index, inst.InstanceID), err)

		return true
	case survived:
		m.holdSurvivor(ctx, task, inst, fmt.Sprintf(survivorStill, identity.Group, identity.Index, inst.InstanceID), nil)

		return true
	case record.Started != "yes" && record.Started != "no":
		m.holdSurvivor(ctx, task, inst, fmt.Sprintf(survivorUnknown, identity.Group, identity.Index, inst.InstanceID), nil)

		return true
	case record.Killed != "yes":
		m.holdSurvivor(ctx, task, inst, fmt.Sprintf(survivorUnchecked, identity.Group, identity.Index, inst.InstanceID), nil)

		return true
	}

	reason := "Processes of the command survived KILL on the node, and a later cleanup found them ended."

	return m.clearSurvivor(ctx, run, reason, true)
}

// holdSurvivor keeps the instance held because a node whose processes
// outlived KILL is still unresolved, and says why on the instance. An
// unfinished task pauses, and a cancelled one waits for Resume cleanup.
func (m *Manager) holdSurvivor(ctx context.Context, task *UpgradeTask, inst *InstanceUpgrade, message string, cause error) {
	m.mu.Lock()

	switch task.Status {
	case TaskStatusPending, TaskStatusRunning, TaskStatusPaused:
		task.Status = TaskStatusPaused
		task.Paused = true
	case TaskStatusCancelled:
		task.Paused = true
	case TaskStatusCompleted, TaskStatusFailed:
	}

	inst.Error = message
	m.mu.Unlock()

	if cause != nil {
		m.logger.Error("Command task %s still holds instance %s for processes that survived KILL: %s", task.ID, inst.InstanceID, commandErrorText(cause))
	} else {
		m.logger.Info("Command task %s still holds instance %s for processes that survived KILL", task.ID, inst.InstanceID)
	}

	err := m.persistCertConfirmed(ctx, task)
	if err != nil {
		m.logger.Error("Command task %s holds instance %s for processes that survived KILL, but the hold could not be confirmed in Vault", task.ID, inst.InstanceID)

		m.mu.Lock()
		inst.Error += " The hold is not confirmed in Vault, so a restart may not find the task waiting."
		m.mu.Unlock()
	}
}

// clearSurvivor records that nothing of the node's processes is left, and
// releases the instance once nothing else of it is unresolved. The node
// stays failed. It reports whether the instance is still held because the
// save could not be confirmed, which pauses the task.
func (m *Manager) clearSurvivor(ctx context.Context, run *commandRun, reason string, killed bool) bool {
	err := run.advance(ctx, func(node *CommandVM) {
		node.Survived = false
		node.Reason = reason
		node.Killed = node.Killed || killed

		// A settled instance ends with the reason of its failed node, and
		// one still open takes it when it ends.
		if run.inst.Status.settled() {
			run.inst.Error = reason
		} else {
			run.inst.Error = ""
		}
	})
	if err != nil {
		m.pauseUnconfirmed(run.task, run.inst, "record that the processes which survived KILL have ended")

		return true
	}

	m.logger.Info("Command task %s found the processes that survived KILL on a node of instance %s ended", run.task.ID, run.inst.InstanceID)

	m.mu.Lock()
	m.releaseUnheldLocked(run.task)
	m.mu.Unlock()

	return false
}

// commandCleansOnlyLocked reports whether a resume of the task is a Resume
// cleanup, which runs cleanup on the unresolved nodes and sends no
// command. A cancelled task that waits for a resume is one. So is a paused
// task with an unresolved node that no worker is driving, which a node
// that outlived KILL, or one a lost result, a restart, or a panic left
// staged or sent, leaves. A paused task with a worker still in flight is
// resumed as any task is. The caller holds mu.
func commandCleansOnlyLocked(task *UpgradeTask) bool {
	if task.KindOrDefault() != TaskKindCommand {
		return false
	}

	if waitsCancelledLocked(task) {
		return true
	}

	if task.Status != TaskStatusPaused || !task.Paused {
		return false
	}

	unresolved := false

	for i := range task.Instances {
		if task.Instances[i].Command == nil {
			continue
		}

		for _, node := range task.Instances[i].Command.VMs {
			if node.claimed {
				return false
			}

			unresolved = unresolved || commandNodeUnresolved(node)
		}
	}

	return unresolved
}

// pauseAfterCleanup pauses a task that a Resume cleanup resumed, before it
// would send the command to the instance, and reports whether it did. The
// resume that goes on sends the command and is gated as any such resume
// is.
func (m *Manager) pauseAfterCleanup(ctx context.Context, task *UpgradeTask, inst *InstanceUpgrade) bool {
	m.mu.Lock()

	if !task.CleanupOnly {
		m.mu.Unlock()

		return false
	}

	task.CleanupOnly = false

	if task.Status == TaskStatusPending || task.Status == TaskStatusRunning {
		task.Status = TaskStatusPaused
		task.Paused = true
	}

	inst.Error = cleanupOnlyDone
	m.mu.Unlock()

	m.logger.Info("Command task %s finished its Resume cleanup, so it pauses before it sends the command to instance %s", task.ID, inst.InstanceID)

	err := m.persistCertConfirmed(ctx, task)
	if err != nil {
		m.pauseUnconfirmed(task, inst, "record that the cleanup finished")
	}

	return true
}
