//nolint:testpackage // the tests read the manager's unexported state
package upgrade

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"blacksmith/internal/bosh"
)

// takingDirector answers the deploy of one deployment without a task,
// either refusing it or naming no task, and deploys every other one as
// resumeDirector does.
type takingDirector struct {
	*resumeDirector

	deployment string
	noTask     bool

	mu    sync.Mutex
	tried int
}

func (d *takingDirector) UpdateDeployment(ctx context.Context, name, manifest string) (*bosh.Task, error) {
	if name != d.deployment {
		return d.resumeDirector.UpdateDeployment(ctx, name, manifest)
	}

	d.mu.Lock()
	d.tried++
	d.mu.Unlock()

	if d.noTask {
		return &bosh.Task{}, nil
	}

	return nil, errDeployRefusedInTest
}

func (d *takingDirector) UpdateDeploymentWithOptions(ctx context.Context, name, manifest string, _ bosh.DeployOptions) (*bosh.Task, error) {
	return d.UpdateDeployment(ctx, name, manifest)
}

// attempts returns how many times the deployment's deploy was sent.
func (d *takingDirector) attempts() int {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.tried
}

// untakenDeploys are the two ways a director doesn't take an upgrade's
// deploy.
var untakenDeploys = []struct {
	name   string
	noTask bool
}{
	{name: "the director refuses the deploy"},
	{name: "the director names no task", noTask: true},
}

// startUntaken starts a two-instance upgrade whose first deploy the
// director doesn't take, with pause_on_failure as pause says, and returns
// the first save that records the first instance failed once it lands.
func startUntaken(t *testing.T, noTask, pause bool) (*Manager, *takingDirector, string, <-chan UpgradeTask) {
	t.Helper()

	director := &takingDirector{resumeDirector: newResumeDirector(), deployment: "redis-instance-1", noTask: noTask}

	var once sync.Once

	firstFailed := make(chan UpgradeTask, 1)

	vault := newWaitVault()
	vault.onPut = func(path string, raw []byte) {
		if !strings.HasPrefix(path, upgradeTasksVaultPath+"/") {
			return
		}

		var saved UpgradeTask
		if json.Unmarshal(raw, &saved) != nil || len(saved.Instances) == 0 || saved.Instances[0].Status != InstanceStatusFailed {
			return
		}

		once.Do(func() { firstFailed <- saved })
	}

	manager := loadManager(t, director, vault)

	task, err := manager.CreateTask(context.Background(), CreateTaskRequest{
		InstanceIDs:    []string{"instance-1", "instance-2"},
		TargetStemcell: StemcellTarget{OS: targetStemcellOS, Version: targetStemcellVersion},
		PauseOnFailure: new(pause),
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	return manager, director, task.ID, firstFailed
}

// waitForSave returns the save the channel carries, and fails the test
// when it never lands.
func waitForSave(t *testing.T, saves <-chan UpgradeTask) UpgradeTask {
	t.Helper()

	select {
	case saved := <-saves:
		return saved
	case <-time.After(waitTimeout):
		t.Fatal("the failed instance was never saved")
	}

	return UpgradeTask{}
}

// An upgrade that pauses on a failure pauses when the director doesn't
// take an instance's deploy, in the save that records the failure, with
// the reason and the Continue choice any other failure gives, and its
// next instance waits for the resume, which never retries the failed one.
//
//nolint:paralleltest // the upgrade waits on a shared test director
func TestUpgradePausesWhenTheDirectorDoesNotTakeTheDeploy(t *testing.T) {
	for _, tc := range untakenDeploys {
		t.Run(tc.name, func(t *testing.T) {
			manager, director, taskID, firstFailed := startUntaken(t, tc.noTask, true)

			first := waitForSave(t, firstFailed)
			if first.Status != TaskStatusPaused || !first.Paused || first.FailedCount != 1 {
				t.Fatalf("expected the save of the failure to carry the pause, got %s paused %v with %d failed", first.Status, first.Paused, first.FailedCount)
			}

			if want := failurePauseReason(&first.Instances[0]); first.PauseReason != want || !strings.Contains(want, "Continue remaining instances") {
				t.Fatalf("expected the failure's reason %q, got %q", want, first.PauseReason)
			}

			expectQuiet(t, manager, director.resumeDirector, taskID, TaskStatusPaused, "redis-instance-2")

			err := manager.ResumeTask(context.Background(), taskID)
			if err != nil {
				t.Fatalf("ResumeTask: %v", err)
			}

			done := expectCompleted(t, manager, taskID)
			if done.Instances[0].Status != InstanceStatusFailed || done.Instances[1].Status != InstanceStatusSuccess || done.PauseReason != "" {
				t.Fatalf("expected the failed instance kept and the next one deployed, got %+v", done)
			}

			if director.attempts() != 1 {
				t.Fatalf("expected the failed instance's deploy sent once, got %d", director.attempts())
			}
		})
	}
}

// An upgrade that doesn't pause on a failure goes on past an instance
// whose deploy the director doesn't take.
//
//nolint:paralleltest // the upgrade waits on a shared test director
func TestUpgradeGoesOnWhenTheDirectorDoesNotTakeTheDeploy(t *testing.T) {
	for _, tc := range untakenDeploys {
		t.Run(tc.name, func(t *testing.T) {
			manager, director, taskID, firstFailed := startUntaken(t, tc.noTask, false)

			if first := waitForSave(t, firstFailed); first.Paused || first.PauseReason != "" {
				t.Fatalf("expected the save of the failure unpaused, got paused %v with reason %q", first.Paused, first.PauseReason)
			}

			done := expectCompleted(t, manager, taskID)
			if done.Instances[0].Status != InstanceStatusFailed || done.Instances[1].Status != InstanceStatusSuccess || done.PauseReason != "" {
				t.Fatalf("expected the first instance failed and the second deployed with no pause, got %+v", done)
			}

			if deploys := director.allDeploys(); !slices.Equal(deploys, []string{"redis-instance-2"}) {
				t.Fatalf("expected only the second instance deployed, got %v", deploys)
			}
		})
	}
}
