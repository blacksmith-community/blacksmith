package upgrade

import (
	"context"
	"fmt"

	"blacksmith/internal/certinventory"
)

// ErrSaveUnconfirmed means a certificate task's save could not be
// confirmed, so the mutation it guards is not sent. It is the certinventory
// sentinel under this package's name, so errors.Is matches either name.
//
//nolint:gochecknoglobals // a sentinel shared with certinventory
var ErrSaveUnconfirmed = certinventory.ErrSaveUnconfirmed

// confirmedWrites is how many times a confirmed save sends a record that
// did not land.
const confirmedWrites = 3

// persistCertConfirmed saves the certificate task and returns nil only
// once it knows the record and the task's index entry are in Vault. Every
// certificate step calls it before it sends a mutation, and sends nothing
// when it returns an error.
//
// It bumps the task's revision and takes a snapshot under mu, then writes
// the snapshot through writeSnapshot. A save that writeSnapshot drops
// counts as confirmed, because a newer snapshot already landed. A write
// that returns an error may still have stored the record, so the record is
// read back: a revision at or above the snapshot's confirms it, and an
// older revision or no record means the write is sent again, up to
// confirmedWrites writes in all within persistTaskTimeout. A failed
// read-back is unconfirmed. Until a confirmed save has seen the task in
// the index, the index is read back too, and an index that lacks the task
// is written once more. The caller does not hold mu. Every error it
// returns wraps ErrSaveUnconfirmed.
func (m *Manager) persistCertConfirmed(ctx context.Context, task *UpgradeTask) error {
	m.mu.Lock()
	task.Revision++
	snapshot := m.snapshotLocked(task)
	m.mu.Unlock()

	// The record must land even when the caller's context has ended, as
	// writeSnapshot's own saves must, and the confirmation as a whole is
	// bounded by one save's timeout.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), persistTaskTimeout)
	defer cancel()

	err := m.confirmRecord(ctx, snapshot)
	if err == nil {
		err = m.confirmIndex(ctx, snapshot.task)
	}

	if err != nil {
		m.logger.Error("Could not confirm the save of certificate task %s at revision %d: %v", task.ID, snapshot.task.Revision, err)

		return err
	}

	return nil
}

// confirmRecord writes the snapshot until the task's record is known to
// hold it or a newer state, as persistCertConfirmed describes.
func (m *Manager) confirmRecord(ctx context.Context, snapshot taskSnapshot) error {
	var writeErr error

	for attempt := 1; attempt <= confirmedWrites; attempt++ {
		writeErr = m.writeSnapshot(ctx, snapshot)
		if writeErr == nil {
			return nil
		}

		landed, readErr := m.recordLanded(ctx, snapshot)
		if readErr != nil {
			return fmt.Errorf("%w: the write of task %s failed (%w) and reading it back failed: %w", ErrSaveUnconfirmed, snapshot.task.ID, writeErr, readErr)
		}

		if landed {
			return nil
		}

		if ctx.Err() != nil {
			return fmt.Errorf("%w: task %s was not saved within %v: %w", ErrSaveUnconfirmed, snapshot.task.ID, persistTaskTimeout, writeErr)
		}

		m.logger.Info("Certificate task %s was not saved on write %d of %d, so it is written again: %v", snapshot.task.ID, attempt, confirmedWrites, writeErr)
	}

	return fmt.Errorf("%w: task %s was not saved after %d writes: %w", ErrSaveUnconfirmed, snapshot.task.ID, confirmedWrites, writeErr)
}

// recordLanded reports whether the task's record holds the snapshot or a
// newer state after a write that returned an error. A record that landed
// before the error, such as one whose index write failed, counts at once.
// Otherwise the record is read back under the task's save lock, and a
// stored revision at or above the snapshot's marks the snapshot written,
// so no older snapshot can land over it later.
func (m *Manager) recordLanded(ctx context.Context, snapshot taskSnapshot) (bool, error) {
	persistence := m.persistenceFor(snapshot.task.ID)

	persistence.mu.Lock()
	defer persistence.mu.Unlock()

	if persistence.written >= snapshot.seq {
		return true, nil
	}

	path := taskRecordPath(snapshot.task)

	var stored UpgradeTask

	exists, err := m.vault.Get(ctx, path, &stored)
	if err != nil {
		return false, fmt.Errorf("reading %s: %w", path, err)
	}

	if !exists || stored.Revision < snapshot.task.Revision {
		return false, nil
	}

	persistence.written = snapshot.seq

	return true, nil
}

// confirmIndex makes sure the task's index names it, once per task, as
// persistCertConfirmed describes. It writes the task's entry from the
// snapshot, never from the live task.
func (m *Manager) confirmIndex(ctx context.Context, task *UpgradeTask) error {
	persistence := m.persistenceFor(task.ID)
	if persistence.indexed.Load() {
		return nil
	}

	indexPath := tasksIndexPath(task.KindOrDefault())

	var index map[string]interface{}

	exists, err := m.vault.Get(ctx, indexPath, &index)
	if err != nil {
		return fmt.Errorf("%w: reading %s back for task %s: %w", ErrSaveUnconfirmed, indexPath, task.ID, err)
	}

	if _, named := index[task.ID]; !exists || !named {
		err = m.addToTasksIndex(ctx, task)
		if err != nil {
			return fmt.Errorf("%w: %s does not name task %s: %w", ErrSaveUnconfirmed, indexPath, task.ID, err)
		}
	}

	persistence.indexed.Store(true)

	return nil
}

// pauseUnconfirmed pauses a certificate task whose save could not be
// confirmed before the step. The instance stays in its phase and records
// why, and nothing is saved, so Vault keeps the last confirmed record,
// which comes before the step that was not taken. A nil instance records
// nothing on an instance. The caller does not hold mu.
func (m *Manager) pauseUnconfirmed(task *UpgradeTask, inst *InstanceUpgrade, step string) {
	message := fmt.Sprintf("Blacksmith could not confirm that it saved this task's progress to Vault, so it did not %s. Resume once Vault is healthy.", step)

	m.mu.Lock()

	// A finished task stays as it is, and an unfinished one pauses.
	if task.Status == TaskStatusPending || task.Status == TaskStatusRunning || task.Status == TaskStatusPaused {
		task.Status = TaskStatusPaused
		task.Paused = true
	}

	if inst != nil {
		inst.Error = message
	}

	m.mu.Unlock()

	m.logger.Error("Certificate task %s paused: %s", task.ID, message)
}
