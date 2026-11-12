package upgrade

import (
	"context"

	"blacksmith/internal/bosh/sshconn"
)

// commandSessionsFor returns the sessions of one instance's command nodes,
// which newCommandSessions describes, and records on the instance the BOSH
// tasks that set up and remove each session they open. Each open gets its
// own entry in the instance's SSHSessions. The setup task is saved as soon
// as the director accepts it, before the open waits for it, and the
// cleanup task when the director accepts the cleanup, however the session
// ends. A setup the director never accepted makes no entry.
func (m *Manager) commandSessionsFor(task *UpgradeTask, inst *InstanceUpgrade) *patchSessions {
	sessions := m.newCommandSessions(inst.Command.Deployment)
	sessions.observe = func(ctx context.Context, group string) context.Context {
		return sshconn.WithTaskObserver(ctx, m.sshSessionObserver(task, inst, group))
	}

	return sessions
}

// sshSessionEntry is where one session's tasks go in an instance's
// SSHSessions. index is -1 until the first task arrives, and it is
// guarded by the manager's mu.
type sshSessionEntry struct {
	index int
}

// sshSessionObserver returns the observer of one session on the group,
// which records its tasks in one entry of the instance's SSHSessions.
func (m *Manager) sshSessionObserver(task *UpgradeTask, inst *InstanceUpgrade, group string) sshconn.TaskObserver {
	entry := &sshSessionEntry{index: -1}

	return sshconn.TaskObserver{
		Setup: func(taskID int) {
			m.recordSSHSessionTask(task, inst, group, entry, func(tasks *SSHSessionTasks) { tasks.SetupTaskID = taskID })
		},
		Cleanup: func(taskID int) {
			m.recordSSHSessionTask(task, inst, group, entry, func(tasks *SSHSessionTasks) { tasks.CleanupTaskID = taskID })
		},
	}
}

// recordSSHSessionTask sets a task of the session in its entry, making the
// entry on the session's first task, and saves the task record through
// persistCertConfirmed. The change is made under mu, and mu is released
// before the save, which takes it again for its snapshot. A save that is
// not confirmed is logged and nothing else, because the IDs only describe
// the run and must never hold up a command or its cleanup. A later save of
// the task carries them all the same.
func (m *Manager) recordSSHSessionTask(task *UpgradeTask, inst *InstanceUpgrade, group string, entry *sshSessionEntry, set func(*SSHSessionTasks)) {
	m.mu.Lock()

	if entry.index < 0 || entry.index >= len(inst.SSHSessions) || inst.SSHSessions[entry.index].Group != group {
		inst.SSHSessions = append(inst.SSHSessions, SSHSessionTasks{Group: group})
		entry.index = len(inst.SSHSessions) - 1
	}

	set(&inst.SSHSessions[entry.index])
	recorded := inst.SSHSessions[entry.index]
	m.mu.Unlock()

	err := m.persistCertConfirmed(context.Background(), task)
	if err != nil {
		m.logger.Error("Could not save SSH setup task %d and cleanup task %d of group %s on instance %s of command task %s: %v",
			recorded.SetupTaskID, recorded.CleanupTaskID, group, inst.InstanceID, task.ID, err)
	}
}
