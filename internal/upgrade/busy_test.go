//nolint:testpackage // the tests read the manager's unexported state
package upgrade

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"blacksmith/pkg/logger"
)

const busyTaskID = "busy-task"

// heldIndexVault is a waitVault whose read of the upgrade tasks index
// waits until the test lets it go.
type heldIndexVault struct {
	*waitVault

	release chan struct{}
	once    sync.Once
}

func (v *heldIndexVault) Get(ctx context.Context, path string, result interface{}) (bool, error) {
	if path == upgradeTasksVaultPath {
		select {
		case <-v.release:
		case <-ctx.Done():
			return false, fmt.Errorf("reading %s: %w", path, ctx.Err())
		}
	}

	return v.waitVault.Get(ctx, path, result)
}

// letGo lets the read of the index, and every later one, go on.
func (v *heldIndexVault) letGo() { v.once.Do(func() { close(v.release) }) }

// stemcellRequest asks for a stemcell upgrade of the instances.
func stemcellRequest(instanceIDs ...string) CreateTaskRequest {
	return CreateTaskRequest{
		InstanceIDs:    instanceIDs,
		TargetStemcell: StemcellTarget{OS: targetStemcellOS, Version: targetStemcellVersion},
	}
}

func TestCreateTaskRejectsAnInstanceAnUnfinishedUpgradeHolds(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		task     TaskStatus
		instance InstanceStatus
		heldID   string
		// owned and settling say whether a processor still follows the
		// upgrade, as runTask or the loader's settle does.
		owned    bool
		settling int
		busy     bool
	}{
		"pending in a pending upgrade":                      {task: TaskStatusPending, instance: InstanceStatusPending, busy: true},
		"running in a running upgrade":                      {task: TaskStatusRunning, instance: InstanceStatusRunning, busy: true},
		"interrupted in a running upgrade":                  {task: TaskStatusRunning, instance: InstanceStatusInterrupted, busy: true},
		"pending in a paused upgrade":                       {task: TaskStatusPaused, instance: InstanceStatusPending, busy: true},
		"upgraded in a running upgrade":                     {task: TaskStatusRunning, instance: InstanceStatusSuccess},
		"failed in a running upgrade":                       {task: TaskStatusRunning, instance: InstanceStatusFailed},
		"unknown in a running upgrade":                      {task: TaskStatusRunning, instance: InstanceStatusOutcomeUnknown},
		"pending in a completed upgrade":                    {task: TaskStatusCompleted, instance: InstanceStatusPending},
		"interrupted in a failed upgrade":                   {task: TaskStatusFailed, instance: InstanceStatusInterrupted},
		"pending in a cancelled upgrade":                    {task: TaskStatusCancelled, instance: InstanceStatusPending},
		"running in a cancelled upgrade":                    {task: TaskStatusCancelled, instance: InstanceStatusRunning},
		"running in a cancelled upgrade still owned":        {task: TaskStatusCancelled, instance: InstanceStatusRunning, owned: true, busy: true},
		"interrupted in a cancelled upgrade still settling": {task: TaskStatusCancelled, instance: InstanceStatusInterrupted, settling: 1, busy: true},
		"interrupted in a failed upgrade still owned":       {task: TaskStatusFailed, instance: InstanceStatusInterrupted, owned: true, busy: true},
		"pending in a cancelled upgrade still owned":        {task: TaskStatusCancelled, instance: InstanceStatusPending, owned: true},
		"upgraded in a completed upgrade still owned":       {task: TaskStatusCompleted, instance: InstanceStatusSuccess, owned: true},
		"skipped in a running upgrade":                      {task: TaskStatusRunning, instance: InstanceStatusSkipped},
		"cancelled in a running upgrade":                    {task: TaskStatusRunning, instance: InstanceStatusCancelled},
		"another instance in a pending upgrade":             {task: TaskStatusPending, instance: InstanceStatusPending, heldID: "instance-2"},
	}

	for name, holding := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			director := newResumeDirector()
			manager := loadManager(t, director, newWaitVault())

			held := InstanceUpgrade{InstanceID: waitInstanceID, DeploymentName: waitDeployment, Status: holding.instance}
			if holding.heldID != "" {
				held.InstanceID = holding.heldID
			}

			manager.mu.Lock()
			manager.tasks[busyTaskID] = &UpgradeTask{ID: busyTaskID, Status: holding.task, Instances: []InstanceUpgrade{held}}
			manager.owned[busyTaskID] = holding.owned
			manager.settling[busyTaskID] = holding.settling
			manager.mu.Unlock()

			task, err := manager.CreateTask(t.Context(), stemcellRequest(waitInstanceID))

			if !holding.busy {
				if err != nil {
					t.Fatalf("expected the upgrade to be created, got %v", err)
				}

				expectCompleted(t, manager, task.ID)

				return
			}

			if !errors.Is(err, ErrInstanceBusy) || !strings.Contains(err.Error(), busyTaskID) || !strings.Contains(err.Error(), waitInstanceID) {
				t.Fatalf("expected ErrInstanceBusy naming %s and %s, got %v", waitInstanceID, busyTaskID, err)
			}

			if len(manager.ListTasks()) != 1 || director.deployCount(waitDeployment) != 0 {
				t.Fatalf("expected no new upgrade and no deploy, got %d upgrades and %d deploys", len(manager.ListTasks()), director.deployCount(waitDeployment))
			}
		})
	}
}

func TestCreateTaskRejectsAStemcellUpgradeOfAnInstanceBeingUpgraded(t *testing.T) {
	t.Parallel()

	director := newResumeDirector()
	director.holdDeployment(waitDeployment)
	manager := loadManager(t, director, newWaitVault())

	first, err := manager.CreateTask(t.Context(), stemcellRequest(waitInstanceID))
	if err != nil {
		t.Fatalf("creating the first upgrade: %v", err)
	}

	director.waitForDeployOf(t, waitDeployment)

	_, err = manager.CreateTask(t.Context(), stemcellRequest("instance-2", waitInstanceID))
	if !errors.Is(err, ErrInstanceBusy) || !strings.Contains(err.Error(), first.ID) {
		t.Fatalf("expected ErrInstanceBusy naming upgrade %s, got %v", first.ID, err)
	}

	director.releaseDeployment(waitDeployment)
	expectCompleted(t, manager, first.ID)

	second, err := manager.CreateTask(t.Context(), stemcellRequest(waitInstanceID))
	if err != nil {
		t.Fatalf("expected an upgrade once the first one finished, got %v", err)
	}

	expectCompleted(t, manager, second.ID)

	if count := director.deployCount(waitDeployment); count != 2 {
		t.Fatalf("expected two deploys of %s, got %d", waitDeployment, count)
	}
}

// A finished upgrade holds a running or interrupted instance only while a
// processor still follows it, and frees it once the follower lets go.
func TestCreateTaskFreesAnInstanceOnceItsFollowerLetsGo(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		instance InstanceStatus
		follow   func(manager *Manager)
		letGo    func(manager *Manager)
	}{
		"a cancelled upgrade still running": {
			instance: InstanceStatusRunning,
			follow:   func(manager *Manager) { manager.owned[busyTaskID] = true },
			letGo:    func(manager *Manager) { delete(manager.owned, busyTaskID) },
		},
		"a loaded cancelled upgrade still settling": {
			instance: InstanceStatusInterrupted,
			follow:   func(manager *Manager) { manager.settling[busyTaskID] = 1 },
			letGo:    func(manager *Manager) { delete(manager.settling, busyTaskID) },
		},
	}

	for name, following := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			director := newResumeDirector()
			manager := loadManager(t, director, newWaitVault())

			held := InstanceUpgrade{InstanceID: waitInstanceID, DeploymentName: waitDeployment, Status: following.instance}

			manager.mu.Lock()
			manager.tasks[busyTaskID] = &UpgradeTask{ID: busyTaskID, Status: TaskStatusCancelled, Instances: []InstanceUpgrade{held}}
			following.follow(manager)
			manager.mu.Unlock()

			_, err := manager.CreateTask(t.Context(), stemcellRequest(waitInstanceID))
			if !errors.Is(err, ErrInstanceBusy) {
				t.Fatalf("expected ErrInstanceBusy while the upgrade is followed, got %v", err)
			}

			manager.mu.Lock()
			following.letGo(manager)
			manager.mu.Unlock()

			task, err := manager.CreateTask(t.Context(), stemcellRequest(waitInstanceID))
			if err != nil {
				t.Fatalf("expected the upgrade to be created once the follower let go, got %v", err)
			}

			expectCompleted(t, manager, task.ID)
		})
	}
}

// An upgrade asked for before the stored upgrades have loaded is turned
// away, since the busy check can't see the instances a stored upgrade
// holds until it has loaded.
func TestCreateTaskRejectsAnUpgradeWhileTheStoredUpgradesLoad(t *testing.T) {
	t.Parallel()

	director := newResumeDirector()
	director.holdDeployment(waitDeployment)

	stored := newWaitVault()
	storeUpgrade(t, stored, storedUpgrade(loadedTaskID, pendingInstance(waitInstanceID, waitDeployment)))

	vault := &heldIndexVault{waitVault: stored, release: make(chan struct{})}
	manager := NewManager(&logger.NoOpLogger{}, director, vault)
	t.Cleanup(manager.Stop)
	t.Cleanup(func() { director.releaseDeployment(waitDeployment) })
	t.Cleanup(vault.letGo)
	manager.Start()

	_, err := manager.CreateTask(t.Context(), stemcellRequest(waitInstanceID))
	if !errors.Is(err, ErrTasksLoading) {
		t.Fatalf("expected %v while the stored upgrades load, got %v", ErrTasksLoading, err)
	}

	if tasks := manager.ListTasks(); len(tasks) != 0 {
		t.Fatalf("expected no upgrade to be created, got %+v", tasks)
	}

	vault.letGo()
	waitForLoad(t, manager)

	_, err = manager.CreateTask(t.Context(), stemcellRequest(waitInstanceID))
	if !errors.Is(err, ErrInstanceBusy) || !strings.Contains(err.Error(), loadedTaskID) {
		t.Fatalf("expected ErrInstanceBusy naming the stored upgrade %s, got %v", loadedTaskID, err)
	}
}
