//nolint:testpackage // the tests read the manager's guard after a load
package upgrade

import (
	"context"
	"strings"
	"testing"
	"time"

	"blacksmith/internal/mutation"
	"blacksmith/pkg/logger"
)

// patchingTask returns the test certificate task with its instance part way
// through a patch, its first VM swapping files, and no decision pending.
func patchingTask(status TaskStatus) UpgradeTask {
	task := fullCertificateTask()
	task.Status = status
	task.Instances[0].Certificates.Pending = nil
	task.Instances[0].Certificates.VMs[0].Phase = VMPhaseSwapping

	return task
}

// expectHeld fails the test unless the guard still holds the test instance
// for the certificate task.
func expectHeld(t *testing.T, manager *Manager) {
	t.Helper()

	holder, held := manager.Guard().Holder(waitInstanceID, waitDeployment)
	if !held || holder.TaskID != certTaskID {
		t.Fatalf("expected the certificate task to keep the instance reserved, got %+v held %v", holder, held)
	}
}

// Cancelling a loaded certificate task whose VM is mid-swap leaves the
// instance reserved, since its files may be half replaced.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestCancelKeepsAPatchingCertificateInstanceHeld(t *testing.T) {
	vault := newWaitVault()
	storeCertificateTask(t, vault, patchingTask(TaskStatusRunning))

	manager := loadManager(t, newWaitDirector(), vault)
	expectHeld(t, manager)

	err := manager.CancelTask(context.Background(), certTaskID)
	if err != nil {
		t.Fatalf("cancelling the task: %v", err)
	}

	expectHeld(t, manager)
}

// A certificate task stored cancelled with an instance still running keeps
// that instance reserved after the load.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestLoadKeepsARunningInstanceOfACancelledCertificateTaskHeld(t *testing.T) {
	vault := newWaitVault()
	storeCertificateTask(t, vault, patchingTask(TaskStatusCancelled))

	manager := loadManager(t, newWaitDirector(), vault)

	expectHeld(t, manager)
}

// An instance that waits on a decision stays reserved even when its task
// was cancelled.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestLoadKeepsAnAwaitingInstanceOfACancelledCertificateTaskHeld(t *testing.T) {
	vault := newWaitVault()

	stored := patchingTask(TaskStatusCancelled)
	stored.Instances[0].Status = StatusAwaitingDecision
	storeCertificateTask(t, vault, stored)

	manager := loadManager(t, newWaitDirector(), vault)

	expectHeld(t, manager)
}

// A cancelled certificate task's running instance that a deprovision held
// at load waits for it, and is reserved once the deprovision frees it.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestCancelledCertificateInstanceWaitsForAnotherHolder(t *testing.T) {
	guard := mutation.NewGuard(time.Now)

	freeDeprovision, err := guard.Acquire(waitInstanceID, waitDeployment, mutation.Holder{
		Kind:        mutation.KindDeprovision,
		Description: "deprovision of " + waitDeployment,
	})
	if err != nil {
		t.Fatalf("holding %s: %v", waitInstanceID, err)
	}

	t.Cleanup(freeDeprovision)

	vault := newWaitVault()
	storeCertificateTask(t, vault, patchingTask(TaskStatusCancelled))

	manager := NewManager(&logger.NoOpLogger{}, newWaitDirector(), vault)
	manager.SetGuard(guard)
	manager.restoreWait = 20 * time.Millisecond
	manager.restoreRetry = 5 * time.Millisecond
	t.Cleanup(manager.Stop)
	manager.Start()

	waitForLoad(t, manager)

	manager.mu.RLock()
	waiting := manager.waitingLocked(certTaskID)
	manager.mu.RUnlock()

	if !waiting {
		t.Fatalf("expected the cancelled certificate task to wait for the instance")
	}

	freeDeprovision()

	waitUntil(t, "the certificate task's reservation of "+waitInstanceID, func() bool {
		holder, held := guard.Holder(waitInstanceID, "")

		return held && holder.Kind == mutation.KindCertificates && holder.TaskID == certTaskID
	})
}

// Once the load stops waiting on a holder, a waiting instance is free in
// the guard until the next retry reserves it, so WaitingFor names the
// stored task that owns it, by the instance or by its deployment.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestWaitingForNamesTheStoredTaskBetweenRetries(t *testing.T) {
	guard := mutation.NewGuard(time.Now)

	freeDeprovision, err := guard.Acquire(waitInstanceID, waitDeployment, mutation.Holder{
		Kind:        mutation.KindDeprovision,
		Description: "deprovision of " + waitDeployment,
	})
	if err != nil {
		t.Fatalf("holding %s: %v", waitInstanceID, err)
	}

	vault := newWaitVault()
	storeCertificateTask(t, vault, patchingTask(TaskStatusCancelled))

	manager := NewManager(&logger.NoOpLogger{}, newWaitDirector(), vault)
	manager.SetGuard(guard)
	manager.restoreWait = 20 * time.Millisecond
	manager.restoreRetry = time.Hour
	t.Cleanup(manager.Stop)
	manager.Start()

	waitForLoad(t, manager)
	freeDeprovision()

	if holder, held := guard.Holder(waitInstanceID, waitDeployment); held {
		t.Fatalf("expected the guard to be free before the next retry, held by %+v", holder)
	}

	for _, key := range [][2]string{{waitInstanceID, ""}, {"", waitDeployment}} {
		holder, waiting := manager.WaitingFor(key[0], key[1])
		if !waiting || holder.TaskID != certTaskID || holder.Kind != mutation.KindCertificates || !strings.Contains(holder.Description, "waits to reserve") {
			t.Fatalf("WaitingFor(%q, %q) = %+v, %v, want the stored certificate task", key[0], key[1], holder, waiting)
		}
	}

	if holder, waiting := manager.WaitingFor(certSecondInstance, "redis-"+certSecondInstance); waiting {
		t.Fatalf("expected no task to wait for another instance, got %+v", holder)
	}
}

// idlePatchTask returns the test certificate task with its patch stopped
// between VMs, its one VM in the phase given and no decision pending.
func idlePatchTask(status TaskStatus, phase VMPhase) UpgradeTask {
	task := patchingTask(status)
	rotation := task.Instances[0].Certificates
	rotation.Converge = nil
	rotation.VMs[0].Phase = phase

	return task
}

// A cancelled patch task stored with an instance that nothing follows
// ends that instance at the load and frees it, whether its VM was never
// swapped, rolled back, or done.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestLoadEndsAnIdleInstanceOfACancelledPatchTask(t *testing.T) {
	for _, phase := range []VMPhase{VMPhasePlanned, VMPhaseBackedUp, VMPhaseRolledBack, VMPhaseDone} {
		t.Run(string(phase), func(t *testing.T) {
			vault := newWaitVault()
			storeCertificateTask(t, vault, idlePatchTask(TaskStatusCancelled, phase))

			manager := loadManager(t, newWaitDirector(), vault)

			task := eventuallyInstance(t, manager, InstanceStatusCancelled)
			if task.CancelledCount != 1 {
				t.Fatalf("expected one cancelled instance, got %d", task.CancelledCount)
			}

			expectReleased(t, manager)
		})
	}
}

// Cancelling a loaded patch task whose VM was never swapped ends the
// instance and frees it.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestCancelEndsAnUnswappedPatchInstance(t *testing.T) {
	vault := newWaitVault()
	storeCertificateTask(t, vault, idlePatchTask(TaskStatusRunning, VMPhaseBackedUp))

	manager := loadManager(t, newWaitDirector(), vault)
	expectHeld(t, manager)

	err := manager.CancelTask(context.Background(), certTaskID)
	if err != nil {
		t.Fatalf("cancelling the task: %v", err)
	}

	eventuallyInstance(t, manager, InstanceStatusCancelled)
	expectReleased(t, manager)
}

// A cancelled patch task whose VM was left unrecovered fails the
// instance with the advice to redeploy it, and frees it.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestLoadFailsAnUnrecoveredInstanceOfACancelledPatchTask(t *testing.T) {
	vault := newWaitVault()
	storeCertificateTask(t, vault, idlePatchTask(TaskStatusCancelled, VMPhaseUnrecovered))

	manager := loadManager(t, newWaitDirector(), vault)

	task := eventuallyInstance(t, manager, InstanceStatusFailed)
	if !strings.Contains(task.Instances[0].Error, redeployToRepair) {
		t.Fatalf("expected the redeploy advice, got %q", task.Instances[0].Error)
	}

	expectReleased(t, manager)
}
