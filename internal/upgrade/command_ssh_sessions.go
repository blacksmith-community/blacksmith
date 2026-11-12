package upgrade

import (
	"context"

	"blacksmith/internal/bosh/sshconn"
)

// commandSessionsFor returns the sessions of one instance's command nodes,
// which newCommandSessions describes, and records on the instance the BOSH
// tasks that set up and remove each session they open. Each open gets its
// own entry in the instance's SSHSessions. The setup task is recorded as
// soon as the director accepts it, before the open waits for it, and the
// cleanup task when the director accepts the cleanup, however the session
// ends. A setup the director never accepted makes no entry. The records
// are saved in the background, as requestSSHSaveLocked describes, and
// closing the sessions waits for those saves to end, so the task's record
// holds every ID by the time the instance is done with its sessions.
func (m *Manager) commandSessionsFor(task *UpgradeTask, inst *InstanceUpgrade) *patchSessions {
	sessions := m.newCommandSessions(inst.Command.Deployment)
	sessions.observe = func(ctx context.Context, group string) context.Context {
		return sshconn.WithTaskObserver(ctx, m.sshSessionObserver(task, inst, group))
	}
	sessions.closed = func() { m.awaitSSHSaves(task) }

	return sessions
}

// sshSessionEntry is where one session's tasks go in an instance's
// SSHSessions. index is -1 until the first task arrives, and it is
// guarded by the manager's mu.
type sshSessionEntry struct {
	index int
}

// sshSaveWriter saves one task's record in the background after its SSH
// session tasks change. pending is set, under the manager's mu, when a
// change arrives while a save is under way, and the writer then saves once
// more, so any number of changes during one save cost one more save. done
// closes when the writer ends.
type sshSaveWriter struct {
	pending bool
	done    chan struct{}
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
// entry on the session's first task, and asks for the task record to be
// saved. Both happen under mu, and nothing here waits for Vault, because
// the IDs only describe the run and must never hold up the setup or the
// cleanup that reported them. Every later save of the task carries them
// too, since they are in the task the manager holds.
func (m *Manager) recordSSHSessionTask(task *UpgradeTask, inst *InstanceUpgrade, group string, entry *sshSessionEntry, set func(*SSHSessionTasks)) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if entry.index < 0 || entry.index >= len(inst.SSHSessions) || inst.SSHSessions[entry.index].Group != group {
		inst.SSHSessions = append(inst.SSHSessions, SSHSessionTasks{Group: group})
		entry.index = len(inst.SSHSessions) - 1
	}

	set(&inst.SSHSessions[entry.index])
	m.requestSSHSaveLocked(task)
}

// requestSSHSaveLocked asks for the task's record to be saved because its
// SSH session tasks changed. The caller holds mu. A task has at most one
// writer, so its saves never overlap. A request while the writer saves
// marks one more save, and a request with no writer starts one. It never
// blocks.
func (m *Manager) requestSSHSaveLocked(task *UpgradeTask) {
	if writer, ok := m.sshSaves[task]; ok {
		writer.pending = true

		return
	}

	writer := &sshSaveWriter{done: make(chan struct{})}
	m.sshSaves[task] = writer

	go m.writeSSHSaves(task, writer)
}

// writeSSHSaves is the task's writer. It saves the task through
// persistCertConfirmed, which snapshots the whole record, until no change
// is pending, and then ends, so it never outlives the saves it was asked
// for. Each save is bounded by persistTaskTimeout, and one that is not
// confirmed is logged and nothing else.
func (m *Manager) writeSSHSaves(task *UpgradeTask, writer *sshSaveWriter) {
	defer close(writer.done)

	ctx := context.WithoutCancel(m.ctx)

	for {
		err := m.persistCertConfirmed(ctx, task)
		if err != nil {
			m.logger.Error("Could not save the SSH session tasks of command task %s: %v", task.ID, err)
		}

		m.mu.Lock()

		if !writer.pending {
			delete(m.sshSaves, task)
			m.mu.Unlock()

			return
		}

		writer.pending = false
		m.mu.Unlock()
	}
}

// awaitSSHSaves waits until the task has no writer, which is when every
// save of its SSH session tasks asked for so far has ended.
func (m *Manager) awaitSSHSaves(task *UpgradeTask) {
	for {
		m.mu.RLock()
		writer, ok := m.sshSaves[task]
		m.mu.RUnlock()

		if !ok {
			return
		}

		<-writer.done
	}
}
