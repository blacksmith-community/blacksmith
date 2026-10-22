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
		instance.Error = ""

		m.logger.Info("BOSH task %d deployed %s for instance %s, so its certificates are verified next", taskID, instance.DeploymentName, instance.InstanceID)

		return
	}

	instance.CompletedAt = &now
	instance.Status = InstanceStatusFailed
	instance.Error = fmt.Sprintf("BOSH task %d ended in state %s: %s. CredHub keeps the new certificate versions, which are not deployed yet.",
		taskID, finished.State, finished.Result)

	if finished.State == boshTaskCancelled {
		instance.Error = fmt.Sprintf("BOSH task %d was cancelled. CredHub keeps the new certificate versions, which are not deployed yet.", taskID)
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

// followedContextID returns the context ID of the deploy a certificate
// instance follows. A converge deploy saves its context ID on the
// rotation's converge record when the instance holds none of its own, as
// the loader reads it. Following the deploy puts its task ID on the
// instance, so only the context ID tells which record to read.
func followedContextID(instance *InstanceUpgrade) string {
	if instance.ContextID != "" {
		return instance.ContextID
	}

	if rotation := instance.Certificates; rotation != nil && rotation.Phase == RotationPhaseConverge && rotation.Converge != nil {
		return rotation.Converge.ContextID
	}

	return ""
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

// deployContract is the certificate finisher that records the deploy's
// outcome and goes no further.
type deployContract struct {
	m *Manager
}

func (c deployContract) certAfterDeploy(_ context.Context, task *UpgradeTask, inst *InstanceUpgrade, taskID int, finished *bosh.Task) {
	c.m.settleCertificateDeploy(task, inst, taskID, finished)
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
