//nolint:testpackage // the tests read the manager's unexported state
package upgrade

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/mutation"
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
				held.DeploymentName = "redis-" + holding.heldID
			}

			manager.mu.Lock()
			manager.tasks[busyTaskID] = &UpgradeTask{ID: busyTaskID, Status: holding.task, Instances: []InstanceUpgrade{held}}
			manager.owned[busyTaskID] = holding.owned
			manager.settling[busyTaskID] = holding.settling
			manager.restoreReservationsLocked(manager.tasks[busyTaskID])
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
			manager.restoreReservationsLocked(manager.tasks[busyTaskID])
			manager.mu.Unlock()

			_, err := manager.CreateTask(t.Context(), stemcellRequest(waitInstanceID))
			if !errors.Is(err, ErrInstanceBusy) {
				t.Fatalf("expected ErrInstanceBusy while the upgrade is followed, got %v", err)
			}

			manager.mu.Lock()
			following.letGo(manager)
			manager.releaseUnheldLocked(manager.tasks[busyTaskID])
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

// holdFor reserves the instance and its deployment in the manager's guard
// for a provision, as the broker does, and frees it when the test ends.
func holdFor(t *testing.T, manager *Manager, instanceID string) {
	t.Helper()

	release, err := manager.Guard().Acquire(instanceID, "redis-"+instanceID, mutation.Holder{
		Kind:        mutation.KindProvision,
		Description: "provision of " + instanceID,
	})
	if err != nil {
		t.Fatalf("holding %s: %v", instanceID, err)
	}

	t.Cleanup(release)
}

// expectFree fails the test when the guard holds the instance.
func expectFree(t *testing.T, manager *Manager, instanceID string) {
	t.Helper()

	if holder, held := manager.Guard().Holder(instanceID, ""); held {
		t.Fatalf("expected %s to be free, but %+v holds it", instanceID, holder)
	}
}

// expectHeldBy fails the test unless the upgrade with the task ID holds
// the instance.
func expectHeldBy(t *testing.T, manager *Manager, instanceID, taskID string) {
	t.Helper()

	holder, held := manager.Guard().Holder(instanceID, "")
	if !held || holder.Kind != mutation.KindUpgrade || holder.TaskID != taskID {
		t.Fatalf("expected upgrade %s to hold %s, got %+v (held %v)", taskID, instanceID, holder, held)
	}
}

// eventuallyFree waits until the guard no longer holds the instance.
func eventuallyFree(t *testing.T, manager *Manager, instanceID string) {
	t.Helper()

	waitUntil(t, "the release of "+instanceID, func() bool {
		_, held := manager.Guard().Holder(instanceID, "")

		return !held
	})
}

func TestCreateTaskBusyFromGuard(t *testing.T) {
	t.Parallel()

	director := newResumeDirector()
	manager := loadManager(t, director, newWaitVault())
	holdFor(t, manager, waitInstanceID)

	_, err := manager.CreateTask(t.Context(), stemcellRequest(waitInstanceID))
	if !errors.Is(err, ErrInstanceBusy) || !strings.Contains(err.Error(), "provision") {
		t.Fatalf("expected ErrInstanceBusy naming the provision, got %v", err)
	}

	var busy *mutation.BusyError
	if !errors.As(err, &busy) || busy.Holder.Kind != mutation.KindProvision {
		t.Fatalf("expected a BusyError naming the provision, got %#v", err)
	}

	if len(manager.ListTasks()) != 0 || director.deployCount(waitDeployment) != 0 {
		t.Fatalf("expected no upgrade and no deploy, got %d upgrades and %d deploys", len(manager.ListTasks()), director.deployCount(waitDeployment))
	}
}

func TestCreateTaskReleasesOnAcquireFailure(t *testing.T) {
	t.Parallel()

	director := newResumeDirector()
	manager := loadManager(t, director, newWaitVault())
	holdFor(t, manager, "instance-2")

	_, err := manager.CreateTask(t.Context(), stemcellRequest(waitInstanceID, "instance-2"))
	if !errors.Is(err, ErrInstanceBusy) {
		t.Fatalf("expected ErrInstanceBusy, got %v", err)
	}

	expectFree(t, manager, waitInstanceID)

	if _, held := manager.Guard().Holder("", waitDeployment); held {
		t.Fatalf("expected deployment %s to be free", waitDeployment)
	}

	manager.mu.RLock()
	reservations := len(manager.reservations)
	manager.mu.RUnlock()

	if reservations != 0 {
		t.Fatalf("expected the manager to keep no reservation, got %d tasks", reservations)
	}
}

// An instance that failed when the upgrade was created, because Vault
// does not know it, has settled, so the upgrade does not hold it.
func TestCreateTaskFreesAnInstanceSettledAtCreation(t *testing.T) {
	t.Parallel()

	director := newResumeDirector()
	director.holdDeployment(waitDeployment)
	t.Cleanup(func() { director.releaseDeployment(waitDeployment) })

	manager := loadManager(t, director, newWaitVault())

	task, err := manager.CreateTask(t.Context(), stemcellRequest(waitInstanceID, "unknown-instance"))
	if err != nil {
		t.Fatalf("creating the upgrade: %v", err)
	}

	expectHeldBy(t, manager, waitInstanceID, task.ID)
	expectFree(t, manager, "unknown-instance")
}

//nolint:paralleltest // the upgrade merges the stemcell overlay under the process-wide manifestMu
func TestInstanceReleasedWhenSettled(t *testing.T) {
	director := newResumeDirector()
	director.holdDeployment(nextDeployment)
	t.Cleanup(func() { director.releaseDeployment(nextDeployment) })

	manager := loadManager(t, director, newWaitVault())

	task, err := manager.CreateTask(t.Context(), stemcellRequest(waitInstanceID, "instance-2"))
	if err != nil {
		t.Fatalf("creating the upgrade: %v", err)
	}

	director.waitForDeployOf(t, nextDeployment)

	running := snapshot(manager, task)
	if running.Instances[0].Status != InstanceStatusSuccess {
		t.Fatalf("expected the first instance to have settled, got %+v", running.Instances[0])
	}

	expectFree(t, manager, waitInstanceID)
	expectHeldBy(t, manager, "instance-2", task.ID)

	director.releaseDeployment(nextDeployment)
	expectCompleted(t, manager, task.ID)
	eventuallyFree(t, manager, "instance-2")

	if snap := manager.Guard().Snapshot(); len(snap) != 0 {
		t.Fatalf("expected no reservation once the upgrade completed, got %v", snap)
	}
}

// refusingDirector refuses every deploy, as a director that rejects the
// manifest does.
type refusingDirector struct {
	*resumeDirector
}

var errDeployRefusedInTest = errors.New("the director refused the deploy")

func (d *refusingDirector) UpdateDeployment(context.Context, string, string) (*bosh.Task, error) {
	return nil, errDeployRefusedInTest
}

// abandoningDirector stops following every deploy task it is asked about,
// so its outcome is never known.
type abandoningDirector struct {
	*resumeDirector
}

func (d *abandoningDirector) WaitForTask(context.Context, int) (*bosh.Task, error) {
	return nil, fmt.Errorf("watching the task: %w", bosh.ErrWatchAbandoned)
}

// erroringDirector ends every deploy task in the error state.
type erroringDirector struct {
	*resumeDirector
}

func (d *erroringDirector) WaitForTask(_ context.Context, taskID int) (*bosh.Task, error) {
	return &bosh.Task{ID: taskID, State: "error", Result: "the deploy broke"}, nil
}

// Every way an instance can end frees its reservation, so no upgrade
// keeps an instance it has finished with.
//
//nolint:paralleltest // the upgrade merges the stemcell overlay under the process-wide manifestMu
func TestTerminalInstancesReleaseTheirReservations(t *testing.T) {
	cases := map[string]struct {
		director func() bosh.Director
		status   InstanceStatus
	}{
		"succeeded":       {director: func() bosh.Director { return newResumeDirector() }, status: InstanceStatusSuccess},
		"deploy refused":  {director: func() bosh.Director { return &refusingDirector{newResumeDirector()} }, status: InstanceStatusFailed},
		"task errored":    {director: func() bosh.Director { return &erroringDirector{newResumeDirector()} }, status: InstanceStatusFailed},
		"outcome unknown": {director: func() bosh.Director { return &abandoningDirector{newResumeDirector()} }, status: InstanceStatusOutcomeUnknown},
	}

	for name, ending := range cases {
		t.Run(name, func(t *testing.T) {
			manager := loadManager(t, ending.director(), newWaitVault())

			task, err := manager.CreateTask(t.Context(), stemcellRequest(waitInstanceID))
			if err != nil {
				t.Fatalf("creating the upgrade: %v", err)
			}

			done := eventually(t, manager, task, "the end of the upgrade", func(current UpgradeTask) bool {
				return current.Status == TaskStatusCompleted || current.Status == TaskStatusFailed
			})

			if done.Instances[0].Status != ending.status {
				t.Fatalf("expected the instance to end %s, got %+v", ending.status, done.Instances[0])
			}

			eventuallyFree(t, manager, waitInstanceID)

			if _, held := manager.Guard().Holder("", waitDeployment); held {
				t.Fatalf("expected deployment %s to be free", waitDeployment)
			}

			// A new upgrade of the instance is accepted at once.
			again, err := manager.CreateTask(t.Context(), stemcellRequest(waitInstanceID))
			if err != nil {
				t.Fatalf("expected a new upgrade of the freed instance, got %v", err)
			}

			eventually(t, manager, again, "the end of the second upgrade", func(current UpgradeTask) bool {
				return current.Status == TaskStatusCompleted || current.Status == TaskStatusFailed
			})
		})
	}
}

// A deprovision the broker re-adopted at boot can still hold an instance
// that a stored upgrade must hold. That is no corrupt record, so the
// upgrade's instance keeps its state and its deploy is followed, the load
// finishes after a bounded wait, no new upgrade can take the instance,
// and the upgrade reserves it once the deprovision frees it.
func TestLoadWaitsForAnotherHolder(t *testing.T) {
	t.Parallel()

	director := newResumeDirector()
	director.holdAdopted()
	t.Cleanup(func() { director.release(adoptedTaskID) })

	guard := mutation.NewGuard(time.Now)

	freeDeprovision, err := guard.Acquire(waitInstanceID, adoptedDeployment, mutation.Holder{
		Kind:        mutation.KindDeprovision,
		Description: "deprovision of " + adoptedDeployment,
	})
	if err != nil {
		t.Fatalf("holding %s: %v", waitInstanceID, err)
	}

	t.Cleanup(freeDeprovision)

	vault := newWaitVault()
	storeUpgrade(t, vault, storedUpgrade(loadedTaskID, inFlight(adoptedDeployment, InstanceStatusRunning, adoptedTaskID, loadedContextID)))

	manager := NewManager(&logger.NoOpLogger{}, director, vault)
	manager.SetGuard(guard)
	manager.restoreWait = 50 * time.Millisecond
	manager.restoreRetry = 5 * time.Millisecond
	t.Cleanup(manager.Stop)
	manager.Start()

	waitForLoad(t, manager)
	director.waitForWaitOn(t, adoptedTaskID)

	expectWaitingFor(t, manager, guard)

	freeDeprovision()

	waitUntil(t, "the upgrade's reservation of "+waitInstanceID, func() bool {
		holder, held := guard.Holder(waitInstanceID, "")

		return held && holder.Kind == mutation.KindUpgrade && holder.TaskID == loadedTaskID
	})

	director.release(adoptedTaskID)

	settled := eventuallyWithin(t, manager, loadedTaskID, waitTimeout, "the settle of the followed deploy", func(current UpgradeTask) bool {
		return current.Instances[0].Status == InstanceStatusSuccess
	})

	if settled.FailedCount != 0 {
		t.Fatalf("expected no failed instance, got %+v", settled)
	}

	eventuallyFree(t, manager, waitInstanceID)

	if director.deployCount(adoptedDeployment) != 0 {
		t.Fatalf("expected no new deploy of %s, got %d", adoptedDeployment, director.deployCount(adoptedDeployment))
	}
}

// expectWaitingFor fails the test unless the stored upgrade's instance
// still runs while the deprovision holds it, and a new upgrade of the
// instance is busy with the stored one.
func expectWaitingFor(t *testing.T, manager *Manager, guard *mutation.Guard) {
	t.Helper()

	loaded, err := manager.GetTask(loadedTaskID)
	if err != nil {
		t.Fatalf("GetTask failed: %v", err)
	}

	if loaded.Instances[0].Status != InstanceStatusRunning || loaded.FailedCount != 0 {
		t.Fatalf("expected the instance to keep running while the deprovision holds it, got %+v", loaded.Instances[0])
	}

	if holder, _ := guard.Holder(waitInstanceID, ""); holder.Kind != mutation.KindDeprovision {
		t.Fatalf("expected the deprovision to still hold %s, got %+v", waitInstanceID, holder)
	}

	_, err = manager.CreateTask(t.Context(), stemcellRequest(waitInstanceID))

	var busy *mutation.BusyError
	if !errors.Is(err, ErrInstanceBusy) || !errors.As(err, &busy) || busy.Holder.TaskID != loadedTaskID {
		t.Fatalf("expected a new upgrade to be busy with %s, got %v", loadedTaskID, err)
	}
}
