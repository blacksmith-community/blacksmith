//nolint:testpackage // the tests read the manager's unexported state
package upgrade

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/certinventory"
	"blacksmith/internal/mutation"
)

// failedSaves returns each record the vault stored with the instance
// failed, in the order they were stored.
func (e *redeployEnv) failedSaves(instanceID string) []UpgradeTask {
	e.vault.script.Lock()
	defer e.vault.script.Unlock()

	var saves []UpgradeTask

	for _, record := range e.vault.records {
		index := slices.IndexFunc(record.Instances, func(inst InstanceUpgrade) bool { return inst.InstanceID == instanceID })
		if index >= 0 && record.Instances[index].Status == InstanceStatusFailed {
			saves = append(saves, record)
		}
	}

	return saves
}

// firstFailedSave waits until the vault stores the instance failed and
// returns that first record.
func (e *redeployEnv) firstFailedSave(instanceID string) UpgradeTask {
	e.t.Helper()

	waitUntil(e.t, "a save of the failed instance "+instanceID, func() bool { return len(e.failedSaves(instanceID)) > 0 })

	return e.failedSaves(instanceID)[0]
}

// settingsPausing gives the env certificate settings whose
// pause_on_failure is pause.
func (e *redeployEnv) settingsPausing(pause bool) {
	e.settings = func() certinventory.Settings {
		settings := certinventory.DefaultSettings()
		settings.PauseOnFailure = pause

		return settings
	}
}

// deploysSent returns how many deploys the director was sent.
func (d *redeployDirector) deploysSent() int {
	d.mu.Lock()
	defer d.mu.Unlock()

	return len(d.sent)
}

// endDeploysIn makes every deploy's BOSH task end in the state given.
func (d *redeployDirector) endDeploysIn(state string) {
	d.finish = func(_ context.Context, taskID int) (*bosh.Task, error) {
		return &bosh.Task{ID: taskID, State: state, Deployment: waitDeployment}, nil
	}
}

// expectWentOn waits until the task's last instance leaves pending, which
// it does only when the task went on past the failure before it.
func expectWentOn(t *testing.T, manager *Manager) {
	t.Helper()

	waitUntil(t, "the next instance to start", func() bool {
		instances := current(t, manager).Instances

		return instances[len(instances)-1].Status != InstanceStatusPending
	})
}

// certificateFailure is a way an instance of a two-instance certificate
// task fails before the task's second instance starts.
type certificateFailure struct {
	name string
	// failed is the instance that fails.
	failed string
	// stored arranges the env for the failure and returns the task to
	// store.
	stored func(env *redeployEnv) UpgradeTask
	// inFlight is whether the stored task was saved with its deploy in
	// flight, so it runs on load, rather than never started, so it
	// loads paused and the test resumes it.
	inFlight bool
}

// certificateFailures are the failures that each settle an instance
// along their own path: the preflight's, a deploy the director refused,
// a deploy whose BOSH task failed, a verification that found the old
// version deployed, and a deploy a restart finds failed.
func certificateFailures() []certificateFailure {
	return []certificateFailure{
		{
			name:   "the preflight fails",
			failed: "instance-0",
			stored: (*redeployEnv).failingFirstTask,
		},
		{
			name:   "the director refuses the deploy",
			failed: waitInstanceID,
			stored: func(env *redeployEnv) UpgradeTask {
				env.director.deployErr = errors.New("director 500")

				return env.twoInstanceTask()
			},
		},
		{
			name:   "the deploy's BOSH task fails",
			failed: waitInstanceID,
			stored: func(env *redeployEnv) UpgradeTask {
				env.director.endDeploysIn("error")

				return env.twoInstanceTask()
			},
		},
		{
			name:   "the verification finds the old version deployed",
			failed: waitInstanceID,
			stored: func(env *redeployEnv) UpgradeTask {
				env.director.applies = ""

				return env.twoInstanceTask()
			},
		},
		{
			name:   "a restart finds the deploy failed",
			failed: waitInstanceID,
			stored: func(env *redeployEnv) UpgradeTask {
				env.director.endDeploysIn("error")

				return env.withPending(env.deployedTask(), "instance-2")
			},
			inFlight: true,
		},
	}
}

// start runs the failure's stored task, with its pause_on_failure set to
// pause, or left out when pause is nil.
func (f certificateFailure) start(env *redeployEnv, pause *bool) *Manager {
	env.t.Helper()

	task := f.stored(env)
	task.PauseOnFailure = pause

	if f.inFlight {
		return env.load(task)
	}

	return env.startLoaded(task)
}

// The save that records a certificate instance's failure records the
// pause with it when the task pauses on a failure, so a restart from that
// save, as after a stop or a crash before any later save, starts no other
// instance. The certificate settings don't pause, so only the task's own
// choice can hold it.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestCertificateSavesTheFailureAndThePauseTogether(t *testing.T) {
	for _, tc := range certificateFailures() {
		t.Run(tc.name, func(t *testing.T) {
			env := newRedeployEnv(t)
			env.settingsPausing(false)

			tc.start(env, new(true))

			first := env.firstFailedSave(tc.failed)
			if first.Status != TaskStatusPaused || !first.Paused || !strings.Contains(first.PauseReason, tc.failed) {
				t.Fatalf("expected the save of the failure to carry the pause, got %s paused %v with reason %q", first.Status, first.Paused, first.PauseReason)
			}

			if first.PauseReason != failurePauseReason(&first.Instances[slices.IndexFunc(first.Instances, func(inst InstanceUpgrade) bool { return inst.InstanceID == tc.failed })]) {
				t.Fatalf("expected the failure's own pause reason, got %q", first.PauseReason)
			}

			restarted := newRedeployEnv(t)
			restarted.settingsPausing(false)

			manager := restarted.load(first)
			time.Sleep(quietPeriod)

			again := current(t, manager)
			if again.Status != TaskStatusPaused || again.Instances[len(again.Instances)-1].Status != InstanceStatusPending || restarted.director.deploysSent() != 0 {
				t.Fatalf("expected the restart to stay paused before the next instance, got %s with %+v and %d deploys", again.Status, again.Instances, restarted.director.deploysSent())
			}
		})
	}
}

// A certificate failure follows the task's own choice over the
// certificate settings, whichever way the two disagree: a task that
// pauses saves the pause with the failure, and one that doesn't goes on
// and never saves a reason to hold.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestCertificateFailureFollowsTheTaskChoice(t *testing.T) {
	for _, tc := range certificateFailures() {
		for _, choice := range []struct {
			name     string
			settings bool
			task     bool
		}{
			{name: "the task pauses where the settings don't", settings: false, task: true},
			{name: "the task goes on where the settings pause", settings: true, task: false},
		} {
			t.Run(tc.name+"/"+choice.name, func(t *testing.T) {
				env := newRedeployEnv(t)
				env.settingsPausing(choice.settings)

				manager := tc.start(env, new(choice.task))

				first := env.firstFailedSave(tc.failed)
				if first.Paused != choice.task {
					t.Fatalf("expected the save of the failure paused %v with the task's choice, got paused %v with reason %q", choice.task, first.Paused, first.PauseReason)
				}

				if choice.task {
					return
				}

				expectWentOn(t, manager)

				for _, saved := range env.failedSaves(tc.failed) {
					if saved.PauseReason != "" {
						t.Fatalf("expected no save to hold a task that goes on, got reason %q", saved.PauseReason)
					}
				}
			})
		}
	}
}

// A certificate task saved before tasks carried pause_on_failure follows
// the certificate settings: it pauses after a failure when they pause,
// with the failure's reason, and goes on when they don't.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestStoredCertificateTaskWithoutTheChoiceFollowsTheSettings(t *testing.T) {
	for _, tc := range certificateFailures() {
		for _, pause := range []bool{true, false} {
			name := tc.name + "/the settings go on"
			if pause {
				name = tc.name + "/the settings pause"
			}

			t.Run(name, func(t *testing.T) {
				env := newRedeployEnv(t)
				env.settingsPausing(pause)

				manager := tc.start(env, nil)

				env.firstFailedSave(tc.failed)

				if !pause {
					expectWentOn(t, manager)

					if saved := env.stored(); saved.PauseReason != "" {
						t.Fatalf("expected the task to go on with no reason to hold, got %q", saved.PauseReason)
					}

					return
				}

				waitUntil(t, "the save of the pause", func() bool {
					saved := env.stored()

					return saved.Paused && strings.Contains(saved.PauseReason, tc.failed)
				})

				if next := current(t, manager).Instances; next[len(next)-1].Status != InstanceStatusPending {
					t.Fatalf("expected the next instance to wait, got %s", next[len(next)-1].Status)
				}
			})
		}
	}
}

// A success never holds a certificate task that pauses on a failure. No
// save carries a reason to hold, and the task goes on past its first
// instance and completes. Its second instance's preflight fails, since
// the env's one leaf moved on with the first, but as the last instance it
// leaves nothing to hold for.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestCertificateSuccessDoesNotHoldATaskThatPausesOnFailure(t *testing.T) {
	env := newRedeployEnv(t)
	env.settingsPausing(false)

	task := env.twoInstanceTask()
	task.PauseOnFailure = new(true)

	manager := env.startLoaded(task)

	waitUntil(t, "the task to complete or hold", func() bool {
		task := current(t, manager)

		return task.Status == TaskStatusCompleted || task.PauseReason != ""
	})

	done := current(t, manager)
	if done.Status != TaskStatusCompleted || done.Instances[0].Status != InstanceStatusSuccess {
		t.Fatalf("expected the task to complete after its first instance succeeds, got %s with %s and reason %q", done.Status, done.Instances[0].Status, done.PauseReason)
	}

	env.vault.script.Lock()
	saves := slices.Clone(env.vault.records)
	env.vault.script.Unlock()

	for _, saved := range saves {
		if saved.PauseReason != "" {
			t.Fatalf("expected no save to hold the task, got reason %q", saved.PauseReason)
		}
	}
}

// reservingTaskID is the task that holds the env's instance in
// loadReserved's guard.
const reservingTaskID = "reserving-task"

// loadReserved stores the task and starts a manager over a guard in which
// another task holds the env's instance, so the load fails the stored
// task's in-flight instance as reserved by that task.
func (e *redeployEnv) loadReserved(task UpgradeTask) *Manager {
	e.t.Helper()

	guard := mutation.NewGuard(time.Now)

	_, err := guard.Acquire(waitInstanceID, waitDeployment, mutation.Holder{Kind: mutation.KindCertificates, TaskID: reservingTaskID, Description: "certificate rotation task " + reservingTaskID})
	if err != nil {
		e.t.Fatalf("holding the instance: %v", err)
	}

	storeCertificateTask(e.t, e.vault.waitVault, task)

	manager := e.newManager()
	manager.SetGuard(guard)
	manager.Start()
	waitForLoad(e.t, manager)

	return manager
}

// A restart that finds a certificate task's in-flight instance reserved
// by another task fails the instance, and when the task pauses on a
// failure, the save of that failure carries the pause, so the next
// instance doesn't start. The certificate settings don't pause, so only
// the task's own choice can hold it.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestCertificateInstanceReservedOnLoadPausesTheTask(t *testing.T) {
	env := newRedeployEnv(t)
	env.settingsPausing(false)

	task := env.withPending(env.deployedTask(), "instance-2")
	task.PauseOnFailure = new(true)

	manager := env.loadReserved(task)

	first := env.firstFailedSave(waitInstanceID)
	if first.Instances[0].Error != "instance reserved by task "+reservingTaskID {
		t.Fatalf("expected the instance to fail naming %s, got %q", reservingTaskID, first.Instances[0].Error)
	}

	if first.Status != TaskStatusPaused || !first.Paused || first.PauseReason != failurePauseReason(&first.Instances[0]) {
		t.Fatalf("expected the save of the failure to carry the pause, got %s paused %v with reason %q", first.Status, first.Paused, first.PauseReason)
	}

	time.Sleep(quietPeriod)

	again := current(t, manager)
	if again.Status != TaskStatusPaused || again.Instances[1].Status != InstanceStatusPending || env.director.deploysSent() != 0 {
		t.Fatalf("expected the task to stay paused before the next instance, got %s with %s and %d deploys", again.Status, again.Instances[1].Status, env.director.deploysSent())
	}
}

// A certificate task that goes on after a failure fails the instance
// another task reserves and goes on to its next instance, though the
// certificate settings pause.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestCertificateInstanceReservedOnLoadGoesOn(t *testing.T) {
	env := newRedeployEnv(t)
	env.settingsPausing(true)

	task := env.withPending(env.deployedTask(), "instance-2")
	task.PauseOnFailure = new(false)

	manager := env.loadReserved(task)

	first := env.firstFailedSave(waitInstanceID)
	if first.Paused || first.PauseReason != "" {
		t.Fatalf("expected the save of the failure not to hold the task, got paused %v with reason %q", first.Paused, first.PauseReason)
	}

	expectWentOn(t, manager)

	for _, saved := range env.failedSaves(waitInstanceID) {
		if saved.PauseReason != "" {
			t.Fatalf("expected no save to hold a task that goes on, got reason %q", saved.PauseReason)
		}
	}
}
