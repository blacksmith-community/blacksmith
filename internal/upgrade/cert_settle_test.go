//nolint:testpackage // the tests read the manager's guard and settling state
package upgrade

import (
	"context"
	"fmt"
	"testing"
	"time"

	"blacksmith/internal/bosh"
)

// settleDirector answers every wait on a deploy task with the task and
// error it was given.
type settleDirector struct {
	bosh.Director

	task *bosh.Task
	err  error
}

func (d *settleDirector) WaitForTask(context.Context, int) (*bosh.Task, error) {
	return d.task, d.err
}

// deployingTask returns the test certificate task with its instance in
// the deploy phase of a redeploy, its deploy's IDs saved, and no decision
// pending.
func deployingTask() UpgradeTask {
	task := fullCertificateTask()
	task.Certificates.Mode = RotationModeRedeploy

	inst := &task.Instances[0]
	inst.BOSHTaskID = waitDeployTaskID
	inst.ContextID = loadedContextID
	inst.Certificates.Phase = RotationPhaseDeploy
	inst.Certificates.Pending = nil
	inst.Certificates.VMs = nil

	return task
}

// lookUpContextIDWith replaces the lookup by context ID for the test with
// one that answers with the task and error given.
func lookUpContextIDWith(t *testing.T, found *bosh.Task, err error) {
	t.Helper()

	original := awaitTaskByContextID
	awaitTaskByContextID = func(context.Context, bosh.Director, string, time.Time) (*bosh.Task, error) {
		return found, err
	}

	t.Cleanup(func() { awaitTaskByContextID = original })
}

// settledLoad waits until the loader has stopped settling the certificate
// task and returns the manager's copy of it.
func settledLoad(t *testing.T, manager *Manager) UpgradeTask {
	t.Helper()

	waitUntil(t, "the loader to settle the certificate task", func() bool {
		manager.mu.RLock()
		defer manager.mu.RUnlock()

		return manager.settling[certTaskID] == 0
	})

	task, err := manager.GetTask(certTaskID)
	if err != nil {
		t.Fatalf("reading the task: %v", err)
	}

	return *task
}

// storedRotation returns the rotation of the test instance as Vault holds
// it.
func storedRotation(t *testing.T, vault *waitVault) *InstanceRotation {
	t.Helper()

	var stored UpgradeTask

	found, err := vault.Get(context.Background(), certificateTasksVaultPath+"/"+certTaskID, &stored)
	if err != nil || !found || len(stored.Instances) == 0 {
		t.Fatalf("reading the stored task: found %v, %v", found, err)
	}

	return stored.Instances[0].Certificates
}

// A deploy that ends done records BOSH's outcome on the rotation and
// leaves the instance running and reserved, so its deployed versions and
// listeners are verified before it counts as a success.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestDeployDoneRecordsTheOutcomeAndKeepsTheInstance(t *testing.T) {
	vault := newWaitVault()
	storeCertificateTask(t, vault, deployingTask())

	director := &settleDirector{task: &bosh.Task{ID: waitDeployTaskID, State: boshTaskDone}}
	manager := loadManager(t, director, vault)

	task := settledLoad(t, manager)
	inst := task.Instances[0]

	if inst.Status != InstanceStatusRunning || inst.CompletedAt != nil || task.CompletedCount != 0 {
		t.Fatalf("expected the instance to stay running and uncounted, got %s, completed at %v, count %d", inst.Status, inst.CompletedAt, task.CompletedCount)
	}

	outcome := inst.Certificates.Deploy
	if outcome == nil || outcome.BOSHTaskID != waitDeployTaskID || outcome.State != boshTaskDone || outcome.FinishedAt.IsZero() {
		t.Fatalf("expected the rotation to record BOSH task %d as done, got %+v", waitDeployTaskID, outcome)
	}

	expectHeld(t, manager)

	waitUntil(t, "the outcome to be saved", func() bool {
		saved := storedRotation(t, vault).Deploy

		return saved != nil && saved.State == boshTaskDone
	})
}

// A rotation that already records its deploy's outcome is left for the
// runner, so a restart neither waits on nor looks up the task again.
//
//nolint:paralleltest // it replaces the lookup by context ID
func TestRecordedDeployOutcomeIsNotFollowedAgain(t *testing.T) {
	contextLookups := countContextLookups(t)
	vault := newWaitVault()

	stored := deployingTask()
	stored.Instances[0].Certificates.Deploy = &DeployOutcome{
		BOSHTaskID: waitDeployTaskID,
		State:      boshTaskDone,
		FinishedAt: time.Date(2026, 10, 22, 8, 0, 0, 0, time.UTC),
	}
	storeCertificateTask(t, vault, stored)

	director := &lookupDirector{}
	manager := loadManager(t, director, vault)

	task := settledLoad(t, manager)
	if count := contextLookups.Load() + director.lookups.Load(); count != 0 {
		t.Fatalf("expected no wait or lookup on the director, got %d", count)
	}

	if inst := task.Instances[0]; inst.Status != InstanceStatusRunning {
		t.Fatalf("expected the instance to stay running, got %s", inst.Status)
	}

	expectHeld(t, manager)
}

// A deploy that ends in error or is cancelled fails the instance with no
// CredHub rollback, records BOSH's outcome, and frees the instance.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestFailedOrCancelledDeployFailsTheInstance(t *testing.T) {
	for _, state := range []string{"error", boshTaskCancelled, "timeout"} {
		t.Run(state, func(t *testing.T) {
			vault := newWaitVault()
			storeCertificateTask(t, vault, deployingTask())

			director := &settleDirector{task: &bosh.Task{ID: waitDeployTaskID, State: state, Result: "update failed"}}
			manager := loadManager(t, director, vault)

			task := settledLoad(t, manager)
			inst := task.Instances[0]

			if inst.Status != InstanceStatusFailed || task.FailedCount != 1 || task.CancelledCount != 0 {
				t.Fatalf("expected the instance to fail, got %s, failed %d, cancelled %d", inst.Status, task.FailedCount, task.CancelledCount)
			}

			if outcome := inst.Certificates.Deploy; outcome == nil || outcome.State != state {
				t.Fatalf("expected the rotation to record BOSH's %s, got %+v", state, outcome)
			}

			if holder, held := manager.Guard().Holder(waitInstanceID, waitDeployment); held {
				t.Fatalf("expected the failed instance to be free, held by %+v", holder)
			}
		})
	}
}

// expectAwaitingDeployDecision fails the test unless the instance waits
// for a decision on its deploy's outcome, uncounted, reserved, and with
// its task paused.
func expectAwaitingDeployDecision(t *testing.T, manager *Manager, task UpgradeTask) {
	t.Helper()

	inst := task.Instances[0]
	if inst.Status != InstanceStatusOutcomeUnknown || inst.CompletedAt != nil {
		t.Fatalf("expected the instance's outcome to be unknown and unsettled, got %s, completed at %v", inst.Status, inst.CompletedAt)
	}

	if task.FailedCount != 0 || task.UnknownCount != 0 || task.CompletedCount != 0 {
		t.Fatalf("expected no count to change, got failed %d, unknown %d, completed %d", task.FailedCount, task.UnknownCount, task.CompletedCount)
	}

	pending := inst.Certificates.Pending
	if pending == nil || pending.Kind != DecisionResolveDeployOutcome || pending.Since.IsZero() {
		t.Fatalf("expected a decision on the deploy's outcome, got %+v", pending)
	}

	if task.Status != TaskStatusPaused || !task.Paused {
		t.Fatalf("expected the task to pause, got %s, paused %v", task.Status, task.Paused)
	}

	expectHeld(t, manager)
}

// A deploy whose BOSH outcome Blacksmith can't learn leaves the instance
// waiting for a decision, never failed and never counted.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestUnknownDeployOutcomeWaitsForADecision(t *testing.T) {
	cases := map[string]*settleDirector{
		"watch abandoned": {err: fmt.Errorf("%w: BOSH task %d could not be read", bosh.ErrWatchAbandoned, waitDeployTaskID)},
		"no task":         {},
		"wait failed":     {err: errNoTaskInTest},
	}

	for name, director := range cases {
		t.Run(name, func(t *testing.T) {
			vault := newWaitVault()
			storeCertificateTask(t, vault, deployingTask())

			manager := loadManager(t, director, vault)

			expectAwaitingDeployDecision(t, manager, settledLoad(t, manager))

			waitUntil(t, "the decision to be saved", func() bool {
				saved := storedRotation(t, vault).Pending

				return saved != nil && saved.Kind == DecisionResolveDeployOutcome
			})
		})
	}
}

// A deploy whose answer was lost, and whose task the lookup by context ID
// never settles, waits for a decision. No timer fails it.
//
//nolint:paralleltest // it replaces the lookup by context ID
func TestLostDeployAnswerWaitsForADecision(t *testing.T) {
	cases := map[string]struct {
		found *bosh.Task
		err   error
	}{
		"no task within grace": {err: fmt.Errorf("%w %s", bosh.ErrNoTaskWithContextID, loadedContextID)},
		"lookup abandoned":     {err: fmt.Errorf("%w: the lookup gave up", bosh.ErrWatchAbandoned)},
		"superseded":           {err: fmt.Errorf("%w: a later deploy ran", bosh.ErrLostAnswerSuperseded)},
		"no task named":        {},
	}

	for name, answer := range cases {
		t.Run(name, func(t *testing.T) {
			lookUpContextIDWith(t, answer.found, answer.err)

			vault := newWaitVault()

			stored := deployingTask()
			unknownSince := time.Now().Add(-time.Hour)
			stored.Instances[0].BOSHTaskID = 0
			stored.Instances[0].UnknownSince = &unknownSince
			storeCertificateTask(t, vault, stored)

			manager := loadManager(t, &lookupDirector{}, vault)

			expectAwaitingDeployDecision(t, manager, settledLoad(t, manager))
		})
	}
}

// A cancelled certificate deploy whose BOSH task outlives the wait after
// the cancel waits for a decision on its outcome. It stays reserved with
// nothing following it only until an operator resolves it, and no timer
// fails it.
//
//nolint:paralleltest // it shortens the wait after a cancel, a package variable
func TestCancelledDeployThatOutlivesTheWaitAwaitsADecision(t *testing.T) {
	previous := cancelWaitTimeout
	cancelWaitTimeout = 50 * time.Millisecond

	t.Cleanup(func() { cancelWaitTimeout = previous })

	vault := newWaitVault()
	storeCertificateTask(t, vault, deployingTask())

	// The director never ends the deploy task.
	director := newWaitDirector()
	manager := loadManager(t, director, vault)

	select {
	case <-director.waitStarted:
	case <-time.After(waitTimeout):
		t.Fatal("the loader never waited on the deploy task")
	}

	err := manager.CancelTask(context.Background(), certTaskID)
	if err != nil {
		t.Fatalf("cancelling the task: %v", err)
	}

	task := settledLoad(t, manager)
	inst := task.Instances[0]

	if inst.Status != InstanceStatusOutcomeUnknown || inst.CompletedAt != nil {
		t.Fatalf("expected the instance's outcome to be unknown and unsettled, got %s, completed at %v", inst.Status, inst.CompletedAt)
	}

	if pending := inst.Certificates.Pending; pending == nil || pending.Kind != DecisionResolveDeployOutcome {
		t.Fatalf("expected a decision on the deploy's outcome, got %+v", pending)
	}

	if task.Status != TaskStatusCancelled || task.FailedCount != 0 || task.CancelledCount != 0 || task.CompletedCount != 0 {
		t.Fatalf("expected the task to stay cancelled with no count changed, got %s, failed %d, cancelled %d, completed %d",
			task.Status, task.FailedCount, task.CancelledCount, task.CompletedCount)
	}

	if cancels := director.cancelled(); len(cancels) == 0 || cancels[0] != waitDeployTaskID {
		t.Fatalf("expected the cancel to reach BOSH task %d, got %v", waitDeployTaskID, cancels)
	}

	expectHeld(t, manager)

	waitUntil(t, "the decision to be saved", func() bool {
		saved := storedRotation(t, vault).Pending

		return saved != nil && saved.Kind == DecisionResolveDeployOutcome
	})
}
