package upgrade

import (
	"context"
	"fmt"
	"time"

	"blacksmith/internal/bosh"
)

// settleCertificateDeployLocked records how the BOSH task of a
// certificate instance's deploy ended. A done task leaves the instance
// running and reserved, with the outcome on its rotation, because the
// instance succeeds only once the deployed variable versions and the
// listeners show the new certificates, which the rotation's runner
// checks next. A task that ended in any other state fails the instance,
// and CredHub keeps the new versions, which a later redeploy or converge
// applies. A wait that returned no task leaves the outcome unknown, so the
// instance waits for a decision. The caller holds mu, and releases what
// the task no longer holds.
func (m *Manager) settleCertificateDeployLocked(task *UpgradeTask, instance *InstanceUpgrade, taskID int, finished *bosh.Task) {
	if finished == nil {
		m.awaitDeployDecisionLocked(task, instance,
			fmt.Sprintf("The director returned no task when Blacksmith waited on BOSH task %d, which may still be running.", taskID))

		return
	}

	if finished.ID > 0 {
		taskID = finished.ID
	}

	if instance.Certificates == nil {
		instance.Certificates = &InstanceRotation{}
	}

	now := time.Now()
	instance.Certificates.Deploy = &DeployOutcome{
		BOSHTaskID: taskID,
		State:      finished.State,
		Result:     finished.Result,
		FinishedAt: now,
	}

	if finished.State == boshTaskDone {
		// An instance saved interrupted at shutdown runs again, so its
		// verification can follow.
		if instance.Status == InstanceStatusInterrupted {
			instance.Status = InstanceStatusRunning
		}

		instance.Error = ""

		m.logger.Info("BOSH task %d deployed %s for instance %s, so its certificates are verified next", taskID, instance.DeploymentName, instance.InstanceID)

		return
	}

	instance.CompletedAt = &now
	instance.Status = InstanceStatusFailed
	instance.Error = fmt.Sprintf("BOSH task %d ended in state %s: %s. %s",
		taskID, finished.State, finished.Result, deployPendingNote)

	if finished.State == boshTaskCancelled {
		instance.Error = fmt.Sprintf("BOSH task %d was cancelled. %s", taskID, deployPendingNote)
	}

	task.FailedCount++

	m.logger.Error("Instance %s of %s: %s", instance.InstanceID, instance.DeploymentName, instance.Error)
}

// awaitDeployDecisionLocked records that Blacksmith can't learn how a
// certificate instance's deploy ended. The instance's outcome is unknown,
// it waits for an operator to decide how the rotation goes on, it stays
// reserved, and no count changes, since no timer may settle it. The task
// pauses unless it has already ended. The caller holds mu.
func (m *Manager) awaitDeployDecisionLocked(task *UpgradeTask, instance *InstanceUpgrade, why string) {
	if instance.Certificates == nil {
		instance.Certificates = &InstanceRotation{}
	}

	lookFor := "the deployment's latest task"
	if contextID := followedContextID(instance); contextID != "" {
		lookFor = "context ID " + contextID
	}

	instance.Status = InstanceStatusOutcomeUnknown
	instance.CompletedAt = nil
	instance.Error = fmt.Sprintf("%s Check the director's task list for %s, then decide how the rotation goes on.", why, lookFor)
	instance.Certificates.Pending = &PendingDecision{
		Kind:  DecisionResolveDeployOutcome,
		Since: time.Now(),
	}

	pauseForDecisionLocked(task)

	m.logger.Error("Instance %s of %s waits for a decision: %s", instance.InstanceID, instance.DeploymentName, instance.Error)
}

// convergeRecord returns the rotation's converge record when the instance
// is converging. A converge deploy keeps both of its IDs there, so the
// record is the one home of the deploy the instance follows.
func convergeRecord(instance *InstanceUpgrade) *ConvergeDeploy {
	if rotation := instance.Certificates; rotation != nil && rotation.Phase == RotationPhaseConverge && rotation.Converge != nil {
		return rotation.Converge
	}

	return nil
}

// followedTaskID returns the BOSH task ID of the deploy an instance
// follows, from the converge record when the instance is converging.
func followedTaskID(instance *InstanceUpgrade) int {
	if converge := convergeRecord(instance); converge != nil {
		return converge.BOSHTaskID
	}

	return instance.BOSHTaskID
}

// followedContextID returns the context ID of the deploy an instance
// follows, from the converge record when the instance is converging.
func followedContextID(instance *InstanceUpgrade) string {
	if converge := convergeRecord(instance); converge != nil {
		return converge.ContextID
	}

	return instance.ContextID
}

// recordFollowedTask records the BOSH task ID of the deploy an instance
// follows where followedTaskID reads it. The caller holds mu.
func recordFollowedTask(instance *InstanceUpgrade, taskID int) {
	if converge := convergeRecord(instance); converge != nil {
		converge.BOSHTaskID = taskID

		return
	}

	instance.BOSHTaskID = taskID
}

// recordContextID records the context ID of the deploy an instance is
// about to send where followedContextID reads it. The caller holds mu.
func recordContextID(instance *InstanceUpgrade, contextID string) {
	if converge := convergeRecord(instance); converge != nil {
		converge.ContextID = contextID

		return
	}

	instance.ContextID = contextID
}

// deployEnded reports whether the end of the BOSH task given is already on
// a certificate instance's record. An upgrade instance has no such record.
// The caller holds mu.
func deployEnded(instance *InstanceUpgrade, taskID int) bool {
	rotation := instance.Certificates

	return rotation != nil && rotation.Deploy != nil && taskID > 0 && rotation.Deploy.BOSHTaskID == taskID
}

// pauseForDecisionLocked pauses a certificate task one of whose instances
// waits for a decision. A cancelled or finished task keeps its status.
// The caller holds mu.
func pauseForDecisionLocked(task *UpgradeTask) {
	if task.Status == TaskStatusRunning || task.Status == TaskStatusPending || task.Status == TaskStatusPaused {
		task.Status = TaskStatusPaused
		task.Paused = true
	}
}

// certFinisher ends the following of a certificate instance's deploy once
// its BOSH task has ended, or its wait returned no task.
type certFinisher interface {
	certAfterDeploy(ctx context.Context, task *UpgradeTask, inst *InstanceUpgrade, taskID int, finished *bosh.Task)
}

// settleCertificateDeploy records how a certificate instance's deploy
// ended, as settleCertificateDeployLocked does, and releases what the task
// no longer holds.
func (m *Manager) settleCertificateDeploy(task *UpgradeTask, inst *InstanceUpgrade, taskID int, finished *bosh.Task) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Deferred calls run in reverse, so the settled instance is released
	// before the lock is.
	defer m.releaseUnheldLocked(task)

	m.settleCertificateDeployLocked(task, inst, taskID, finished)
}
