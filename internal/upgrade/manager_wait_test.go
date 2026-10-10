package upgrade

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/interfaces"
	"blacksmith/pkg/logger"
)

const (
	waitInstanceID   = "instance-1"
	waitDeployment   = "redis-instance-1"
	waitDeployTaskID = 42
	waitTimeout      = 5 * time.Second
	waitManifest     = "name: redis-instance-1\nstemcells:\n- alias: default\n  os: ubuntu-noble\n  version: \"1.1\"\n"
	waitStateRunning = "processing"
)

var errNoTaskInTest = errors.New("the test director has no such task")

// waitVault is an interfaces.Vault that holds the instance the upgrade
// reads and keeps a JSON copy of every upgrade task the manager saves. A
// call it does not script panics through the nil embedded Vault.
type waitVault struct {
	interfaces.Vault

	// onPut, when set, sees each saved value as JSON after it is saved.
	onPut func(path string, raw []byte)

	mu    sync.Mutex
	saved map[string][]byte
}

func newWaitVault() *waitVault { return &waitVault{saved: make(map[string][]byte)} }

func (v *waitVault) Get(_ context.Context, path string, result interface{}) (bool, error) {
	if strings.HasPrefix(path, "instance-") {
		raw, _ := json.Marshal(map[string]string{"deployment_name": "redis-" + path})

		return true, json.Unmarshal(raw, result)
	}

	v.mu.Lock()
	raw, ok := v.saved[path]
	v.mu.Unlock()

	if !ok {
		return false, nil
	}

	return true, json.Unmarshal(raw, result)
}

func (v *waitVault) Put(_ context.Context, path string, data interface{}) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", path, err)
	}

	v.mu.Lock()
	v.saved[path] = raw
	v.mu.Unlock()

	if v.onPut != nil {
		v.onPut(path, raw)
	}

	return nil
}

// savedTask returns the upgrade task as the manager last saved it.
func (v *waitVault) savedTask(t *testing.T, taskID string) UpgradeTask {
	t.Helper()

	v.mu.Lock()
	raw := v.saved[upgradeTasksVaultPath+"/"+taskID]
	v.mu.Unlock()

	var task UpgradeTask
	if err := json.Unmarshal(raw, &task); err != nil {
		t.Fatalf("the upgrade task was not saved: %v", err)
	}

	return task
}

// waitDirector is a director for one upgrade deploy. The deploy returns
// task 42, at once unless the test gates it, and the wait on it returns the task the test sends on
// finish, or stops when its context ends. A call it does not script
// panics through the nil embedded Director.
type waitDirector struct {
	bosh.Director

	finish      chan *bosh.Task
	waitStarted chan struct{}
	startOnce   sync.Once

	// updateStarted, when set, is closed as the deploy is sent, and the
	// deploy then holds its answer until updateGate closes.
	updateStarted chan struct{}
	updateGate    chan struct{}

	// onCancel answers CancelTask when it is set.
	onCancel func(ctx context.Context, taskID int) error

	mu      sync.Mutex
	cancels []int
	waits   int
}

func newWaitDirector() *waitDirector {
	return &waitDirector{finish: make(chan *bosh.Task, 1), waitStarted: make(chan struct{})}
}

func (d *waitDirector) GetDeployment(_ context.Context, name string) (*bosh.DeploymentDetail, error) {
	return &bosh.DeploymentDetail{Name: name, Manifest: waitManifest}, nil
}

func (d *waitDirector) UpdateDeployment(_ context.Context, _, _ string) (*bosh.Task, error) {
	if d.updateStarted != nil {
		close(d.updateStarted)
		<-d.updateGate
	}

	return &bosh.Task{ID: waitDeployTaskID, State: waitStateRunning, Deployment: waitDeployment}, nil
}

func (d *waitDirector) WaitForTask(ctx context.Context, taskID int) (*bosh.Task, error) {
	d.mu.Lock()
	d.waits++
	d.mu.Unlock()

	if taskID != waitDeployTaskID {
		return nil, fmt.Errorf("%w: %d", errNoTaskInTest, taskID)
	}

	d.startOnce.Do(func() { close(d.waitStarted) })

	select {
	case task := <-d.finish:
		return task, nil
	case <-ctx.Done():
		return nil, &bosh.DirectorCallError{
			Operation: "WaitForTask",
			Director:  "https://director.example.test:25555",
			Cause:     ctx.Err(),
			Err:       ctx.Err(),
			TaskID:    taskID,
			Task:      bosh.TaskLeftRunning,
		}
	}
}

func (d *waitDirector) CancelTask(ctx context.Context, taskID int) error {
	d.mu.Lock()
	d.cancels = append(d.cancels, taskID)
	onCancel := d.onCancel
	d.mu.Unlock()

	if onCancel != nil {
		return onCancel(ctx, taskID)
	}

	return nil
}

func (d *waitDirector) cancelled() []int {
	d.mu.Lock()
	defer d.mu.Unlock()

	return append([]int(nil), d.cancels...)
}

// startUpgrade starts a manager and an upgrade of the instances, one
// instance when none is named, and waits until the upgrade waits on its
// first deploy task. The returned stop ends the manager once, and the
// test's cleanup calls it too.
func startUpgrade(t *testing.T, director *waitDirector, vault *waitVault, instanceIDs ...string) (*Manager, *UpgradeTask, func()) {
	t.Helper()

	manager, task, stop := beginUpgrade(t, &logger.NoOpLogger{}, director, vault, instanceIDs...)

	select {
	case <-director.waitStarted:
	case <-time.After(waitTimeout):
		t.Fatal("the upgrade never waited on its deploy task")
	}

	return manager, task, stop
}

// beginUpgrade starts a manager with the logger and an upgrade of the
// instances, one instance when none is named, and returns without waiting
// for the upgrade to reach the director. The returned stop ends the
// manager once, and the test's cleanup calls it too.
func beginUpgrade(t *testing.T, log logger.Logger, director bosh.Director, vault *waitVault, instanceIDs ...string) (*Manager, *UpgradeTask, func()) {
	t.Helper()

	if len(instanceIDs) == 0 {
		instanceIDs = []string{waitInstanceID}
	}

	manager := NewManager(log, director, vault)

	var once sync.Once

	stop := func() { once.Do(manager.Stop) }

	t.Cleanup(stop)

	task, err := manager.CreateTask(context.Background(), CreateTaskRequest{
		InstanceIDs:    instanceIDs,
		TargetStemcell: StemcellTarget{OS: "ubuntu-noble", Version: "1.2"},
	})
	if err != nil {
		t.Fatalf("failed to create the upgrade task: %v", err)
	}

	return manager, task, stop
}

// snapshot copies the manager's own upgrade task, the one with the ID of
// task, under the manager's lock. The task CreateTask returned is a copy,
// so the upgrade's progress is read from the manager.
func snapshot(manager *Manager, task *UpgradeTask) UpgradeTask {
	manager.mu.RLock()
	defer manager.mu.RUnlock()

	live := manager.tasks[task.ID]

	copied := *live
	copied.Instances = append([]InstanceUpgrade(nil), live.Instances...)

	return copied
}

// eventually waits until done holds for the upgrade task, and fails the
// test with the last snapshot when it never does.
func eventually(t *testing.T, manager *Manager, task *UpgradeTask, what string, done func(UpgradeTask) bool) UpgradeTask {
	t.Helper()

	deadline := time.Now().Add(waitTimeout)

	for {
		current := snapshot(manager, task)
		if done(current) {
			return current
		}

		if time.Now().After(deadline) {
			t.Fatalf("%s never happened; the upgrade task is %+v", what, current)
		}

		time.Sleep(5 * time.Millisecond)
	}
}

//nolint:paralleltest // MergeStemcellOverlay runs spruce, whose global state races across tests
func TestUpgradeRecordsTheTaskBeforeItFinishes(t *testing.T) {
	director := newWaitDirector()
	vault := newWaitVault()

	manager, task, _ := startUpgrade(t, director, vault)

	running := snapshot(manager, task)
	if running.Instances[0].BOSHTaskID != waitDeployTaskID || running.Instances[0].Status != InstanceStatusRunning {
		t.Fatalf("expected the running instance to hold BOSH task %d, got %+v", waitDeployTaskID, running.Instances[0])
	}

	saved := vault.savedTask(t, task.ID)
	if saved.Instances[0].BOSHTaskID != waitDeployTaskID {
		t.Fatalf("expected BOSH task %d to be saved before the task finished, got %+v", waitDeployTaskID, saved.Instances[0])
	}

	director.finish <- &bosh.Task{ID: waitDeployTaskID, State: "done"}

	done := eventually(t, manager, task, "the upgrade's completion", func(current UpgradeTask) bool {
		return current.Status == TaskStatusCompleted
	})

	if done.Instances[0].Status != InstanceStatusSuccess || done.CompletedCount != 1 || done.FailedCount != 0 {
		t.Fatalf("expected one successful instance, got %+v", done)
	}
}

//nolint:paralleltest // MergeStemcellOverlay runs spruce, whose global state races across tests
func TestUpgradeNilTaskFailsTheInstance(t *testing.T) {
	director := newWaitDirector()
	vault := newWaitVault()

	manager, task, _ := startUpgrade(t, director, vault)

	// The wait answers with no task and no error.
	director.finish <- nil

	done := eventually(t, manager, task, "the upgrade's end", func(current UpgradeTask) bool {
		return current.Status != TaskStatusRunning
	})

	instance := done.Instances[0]
	if instance.Status != InstanceStatusFailed || !strings.Contains(instance.Error, "no task") {
		t.Fatalf("expected the instance to fail with a message that the director returned no task, got %+v", instance)
	}

	if done.FailedCount != 1 || done.CompletedCount != 0 || done.CancelledCount != 0 {
		t.Fatalf("expected one failed instance, got %+v", done)
	}

	if done.Status != TaskStatusFailed || done.CompletedAt == nil {
		t.Fatalf("expected the upgrade to end failed, got %+v", done)
	}
}
