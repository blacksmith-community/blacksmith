//nolint:testpackage // the tests read the manager's unexported state
package upgrade

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"blacksmith/internal/apierr"
	"blacksmith/internal/certinventory"
)

// failDeploy makes the BOSH task the director answers for its next
// deploy end in state error. The director numbers its deploys from 101.
func (d *resumeDirector) failDeploy(taskID int) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.states[taskID] = "error"
}

// holdTask makes the wait on the task last until the test releases it.
func (d *resumeDirector) holdTask(taskID int) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.releases[taskID] = make(chan struct{})
}

// createUpgrade starts a manager over the director and asks it for an
// upgrade of the instances given, which pauses on a failure as pause
// says, or is left out when pause is nil.
func createUpgrade(t *testing.T, director *resumeDirector, pause *bool, instanceIDs ...string) (*Manager, *UpgradeTask) {
	t.Helper()

	manager := loadManager(t, director, newWaitVault())

	task, err := manager.CreateTask(context.Background(), CreateTaskRequest{
		InstanceIDs:    instanceIDs,
		TargetStemcell: StemcellTarget{OS: targetStemcellOS, Version: targetStemcellVersion},
		PauseOnFailure: pause,
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	return manager, task
}

// expectFailurePause waits until the upgrade pauses after a failure and
// fails the test unless its reason names the failed instance.
func expectFailurePause(t *testing.T, manager *Manager, taskID, failedID string) UpgradeTask {
	t.Helper()

	paused := eventuallyWithin(t, manager, taskID, waitTimeout, "the pause of upgrade "+taskID, func(current UpgradeTask) bool {
		return current.Status == TaskStatusPaused
	})

	if !paused.Paused || !strings.Contains(paused.PauseReason, failedID) {
		t.Fatalf("expected a pause whose reason names %s, got paused %v with reason %q", failedID, paused.Paused, paused.PauseReason)
	}

	return paused
}

// An upgrade whose request leaves pause_on_failure out records that it
// doesn't pause, and goes on past a failed instance.
//
//nolint:paralleltest // the upgrade waits on a shared test director
func TestUpgradeLeftOutPauseOnFailureGoesOn(t *testing.T) {
	director := newResumeDirector()
	director.failDeploy(101)

	manager, task := createUpgrade(t, director, nil, "instance-1", "instance-2")

	if task.PauseOnFailure == nil || *task.PauseOnFailure {
		t.Fatalf("expected the upgrade to record that it doesn't pause, got %v", task.PauseOnFailure)
	}

	done := expectCompleted(t, manager, task.ID)

	if done.Instances[0].Status != InstanceStatusFailed || done.Instances[1].Status != InstanceStatusSuccess || done.PauseReason != "" {
		t.Fatalf("expected the first instance failed and the second deployed with no pause, got %+v", done)
	}
}

// An upgrade that asks to pause on a failure pauses once an instance
// fails, starts no other instance, and names the failed one. Resume goes
// on with the remaining instances and never retries the failed one.
//
//nolint:paralleltest // the upgrade waits on a shared test director
func TestUpgradePausesOnFailureWhenAsked(t *testing.T) {
	director := newResumeDirector()
	director.failDeploy(101)

	manager, task := createUpgrade(t, director, new(true), "instance-1", "instance-2", "instance-3")

	if task.PauseOnFailure == nil || !*task.PauseOnFailure {
		t.Fatalf("expected the upgrade to record that it pauses, got %v", task.PauseOnFailure)
	}

	paused := expectFailurePause(t, manager, task.ID, "instance-1")

	expectQuiet(t, manager, director, task.ID, TaskStatusPaused, "redis-instance-2")

	if paused.Instances[0].Status != InstanceStatusFailed || paused.Instances[1].Status != InstanceStatusPending || paused.Instances[2].Status != InstanceStatusPending {
		t.Fatalf("expected the failed instance and two pending ones, got %+v", paused.Instances)
	}

	if saved := savedTask(t, manager, task.ID); saved.Status != TaskStatusPaused || saved.PauseReason != paused.PauseReason || saved.PauseOnFailure == nil || !*saved.PauseOnFailure {
		t.Fatalf("expected the pause and its reason saved, got %+v", saved)
	}

	err := manager.ResumeTask(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("ResumeTask: %v", err)
	}

	done := expectCompleted(t, manager, task.ID)

	if done.PauseReason != "" || done.Instances[1].Status != InstanceStatusSuccess || done.Instances[2].Status != InstanceStatusSuccess {
		t.Fatalf("expected the remaining instances deployed and the reason cleared, got %+v", done)
	}

	if deploys := director.allDeploys(); !slices.Equal(deploys, []string{"redis-instance-1", "redis-instance-2", "redis-instance-3"}) {
		t.Fatalf("expected each instance deployed once, got %v", deploys)
	}
}

// A cancel of an upgrade paused after a failure clears the reason.
//
//nolint:paralleltest // the upgrade waits on a shared test director
func TestCancelClearsTheFailurePauseReason(t *testing.T) {
	director := newResumeDirector()
	director.failDeploy(101)

	manager, task := createUpgrade(t, director, new(true), "instance-1", "instance-2")

	expectFailurePause(t, manager, task.ID, "instance-1")

	err := manager.CancelTask(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("CancelTask: %v", err)
	}

	cancelled := eventuallyWithin(t, manager, task.ID, waitTimeout, "the cancel of upgrade "+task.ID, func(current UpgradeTask) bool {
		return current.Status == TaskStatusCancelled
	})

	if cancelled.PauseReason != "" || director.deployCount("redis-instance-2") != 0 {
		t.Fatalf("expected the reason cleared and nothing more deployed, got %+v and deploys %v", cancelled, director.allDeploys())
	}
}

// An upgrade saved before tasks carried pause_on_failure goes on past a
// failed instance, as it always did.
//
//nolint:paralleltest // the upgrade waits on a shared test director
func TestStoredUpgradeWithoutPauseOnFailureGoesOn(t *testing.T) {
	director := newResumeDirector()
	director.failDeploy(101)

	vault := newWaitVault()
	storeUpgrade(t, vault, storedUpgrade(loadedTaskID,
		pendingInstance("instance-1", firstDeployment),
		pendingInstance("instance-2", nextDeployment)))

	manager := loadManager(t, director, vault)

	done := expectCompleted(t, manager, loadedTaskID)

	if done.PauseOnFailure != nil || done.Instances[0].Status != InstanceStatusFailed || done.Instances[1].Status != InstanceStatusSuccess {
		t.Fatalf("expected the old upgrade to go on past its failure, got %+v", done)
	}
}

// A failure pauses an upgrade whose other deploy is in flight. That deploy
// finishes, no pending instance starts, and Resume carries on with it.
//
//nolint:paralleltest // the settle waits on a shared test director
func TestUpgradeFailureLetsInFlightDeploysFinish(t *testing.T) {
	const heldTaskID = 8

	director := newResumeDirector()
	director.failDeploy(adoptedTaskID)
	director.holdTask(heldTaskID)

	started := time.Now().Add(-time.Minute)
	failing := InstanceUpgrade{InstanceID: "instance-a", DeploymentName: "redis-instance-a", Status: InstanceStatusInterrupted, BOSHTaskID: adoptedTaskID, ContextID: loadedContextID, StartedAt: &started}
	held := InstanceUpgrade{InstanceID: "instance-b", DeploymentName: "redis-instance-b", Status: InstanceStatusInterrupted, BOSHTaskID: heldTaskID, ContextID: "7c2a4d5f-1b8e-4d66-8b2f-4a3c9e0d1f22", StartedAt: &started}

	stored := storedUpgrade(loadedTaskID, failing, held, pendingInstance("instance-c", "redis-instance-c"))
	stored.PauseOnFailure = new(true)

	vault := newWaitVault()
	storeUpgrade(t, vault, stored)

	manager := loadManager(t, director, vault)

	expectFailurePause(t, manager, loadedTaskID, "instance-a")

	director.release(heldTaskID)

	settled := eventuallyWithin(t, manager, loadedTaskID, waitTimeout, "the in-flight deploy to finish", func(current UpgradeTask) bool {
		return current.Instances[1].Status == InstanceStatusSuccess
	})

	if settled.Status != TaskStatusPaused || settled.Instances[2].Status != InstanceStatusPending {
		t.Fatalf("expected the upgrade to stay paused with its last instance pending, got %+v", settled)
	}

	expectQuiet(t, manager, director, loadedTaskID, TaskStatusPaused, "redis-instance-c")

	err := manager.ResumeTask(context.Background(), loadedTaskID)
	if err != nil {
		t.Fatalf("ResumeTask: %v", err)
	}

	done := expectCompleted(t, manager, loadedTaskID)

	if done.Instances[0].Status != InstanceStatusFailed || done.Instances[2].Status != InstanceStatusSuccess {
		t.Fatalf("expected the failed instance left failed and the pending one deployed, got %+v", done.Instances)
	}

	if deploys := director.allDeploys(); !slices.Equal(deploys, []string{"redis-instance-c"}) {
		t.Fatalf("expected one deploy, of the pending instance, got %v", deploys)
	}
}

// A certificate task that doesn't pause on a failure goes on past one
// while the certificate settings pause.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestCertificateTaskPauseOffOverridesTheSettings(t *testing.T) {
	env := newRedeployEnv(t)

	task := env.failingFirstTask()
	task.PauseOnFailure = new(false)

	manager := env.startLoaded(task)

	done := waitForInstance(t, manager, 1, InstanceStatusSuccess)
	if done.Instances[0].Status != InstanceStatusFailed {
		t.Fatalf("expected the first instance to fail, got %s", done.Instances[0].Status)
	}

	waitUntil(t, "the task to complete", func() bool { return current(t, manager).Status == TaskStatusCompleted })
}

// A certificate task that pauses on a failure pauses after one while the
// certificate settings don't, with a reason that names the instance.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestCertificateTaskPauseOnOverridesTheSettings(t *testing.T) {
	env := newRedeployEnv(t)
	env.settings = func() certinventory.Settings {
		settings := certinventory.DefaultSettings()
		settings.PauseOnFailure = false

		return settings
	}

	task := env.failingFirstTask()
	task.PauseOnFailure = new(true)

	manager := env.startLoaded(task)

	waitForInstance(t, manager, 0, InstanceStatusFailed)
	waitUntil(t, "the task to pause", func() bool { return current(t, manager).Status == TaskStatusPaused })

	paused := current(t, manager)
	if paused.Instances[1].Status != InstanceStatusPending || !strings.Contains(paused.PauseReason, "instance-0") {
		t.Fatalf("expected the next instance pending and a reason naming instance-0, got %s and %q", paused.Instances[1].Status, paused.PauseReason)
	}
}

// pauseCreateEnv is createEnv with certificate settings whose
// pause_on_failure the test sets.
func pauseCreateEnv(t *testing.T, pause *atomic.Bool) *previewEnv {
	t.Helper()

	env := newPreviewEnv(t)
	env.configure(t, func(deps *Deps) {
		deps.Settings = func() certinventory.Settings {
			settings := certinventory.DefaultSettings()
			settings.PauseOnFailure = pause.Load()

			return settings
		}
	})

	env.manager.vault = &createVault{waitVault: newWaitVault()}
	env.manager.createRetry = 5 * time.Millisecond
	env.manager.Start()
	waitForLoad(t, env.manager)

	return env
}

// A certificate task takes the settings' pause_on_failure as it stands at
// its creation when the request leaves it out, and the request's value
// otherwise. A later change to the settings leaves the task's value as it
// was.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestCreateCertificateTaskPauseOnFailure(t *testing.T) {
	for _, tc := range []struct {
		name     string
		settings bool
		request  *bool
		want     bool
	}{
		{name: "left out with the settings on", settings: true, want: true},
		{name: "left out with the settings off", settings: false, want: false},
		{name: "off with the settings on", settings: true, request: new(false), want: false},
		{name: "on with the settings off", settings: false, request: new(true), want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var pause atomic.Bool
			pause.Store(tc.settings)

			env := pauseCreateEnv(t, &pause)
			preview := previewOf(t, env, redeploy(waitInstanceID))

			req := createRequest(preview.ID)
			req.PauseOnFailure = tc.request

			task, _, err := env.manager.CreateCertificateTask(context.Background(), req)
			if err != nil {
				t.Fatalf("CreateCertificateTask: %v", err)
			}

			if task.PauseOnFailure == nil || *task.PauseOnFailure != tc.want {
				t.Fatalf("expected pause_on_failure %v, got %v", tc.want, task.PauseOnFailure)
			}

			if env.manager.pausesOnFailure() != tc.settings {
				t.Fatal("the request changed the certificate settings")
			}

			pause.Store(!tc.settings)

			kept := taskCopy(t, env.manager, task.ID)
			if *kept.PauseOnFailure != tc.want || env.manager.taskPausesOnFailure(&kept) != tc.want {
				t.Fatalf("expected the task to keep pause_on_failure %v after the settings changed, got %v", tc.want, *kept.PauseOnFailure)
			}
		})
	}
}

// A retry of a certificate create that asks for another pause_on_failure
// is a different request.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestCreateCertificateTaskPauseOnFailureConflict(t *testing.T) {
	env, _ := createEnv(t)
	preview := previewOf(t, env, redeploy(waitInstanceID))

	_, _, err := env.manager.CreateCertificateTask(context.Background(), createRequest(preview.ID))
	if err != nil {
		t.Fatalf("first create: %v", err)
	}

	changed := createRequest(preview.ID)
	changed.PauseOnFailure = new(false)

	_, _, err = env.manager.CreateCertificateTask(context.Background(), changed)
	expectAPIError(t, err, http.StatusConflict, apierr.CodeRequestIDConflict)
}

// savedTask reads the upgrade the manager saved in its vault.
func savedTask(t *testing.T, manager *Manager, taskID string) UpgradeTask {
	t.Helper()

	var saved UpgradeTask

	_, err := manager.vault.Get(context.Background(), upgradeTasksVaultPath+"/"+taskID, &saved)
	if err != nil {
		t.Fatalf("reading the saved upgrade: %v", err)
	}

	return saved
}
