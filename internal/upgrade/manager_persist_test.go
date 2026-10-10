//nolint:testpackage // the tests hold the manager's saves and read its unexported tasks
package upgrade

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"blacksmith/internal/interfaces"
	"blacksmith/pkg/logger"
)

// gatedVault is a waitVault whose reads and saves a test can hold. Each
// save is marshalled when it is made, the way a Vault client builds its
// request before sending it, and beforeStore sees it before it is stored.
// Each read takes what is stored when it is made, and afterGet sees it
// before it is answered. The test sets both before the manager reads or
// saves anything it gates.
type gatedVault struct {
	*waitVault

	afterGet    func(path string)
	beforeStore func(path string, raw []byte)
}

func newGatedVault() *gatedVault { return &gatedVault{waitVault: newWaitVault()} }

func (v *gatedVault) Get(ctx context.Context, path string, result interface{}) (bool, error) {
	exists, err := v.waitVault.Get(ctx, path, result)

	if v.afterGet != nil {
		v.afterGet(path)
	}

	return exists, err
}

func (v *gatedVault) Put(ctx context.Context, path string, data interface{}) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", path, err)
	}

	if v.beforeStore != nil {
		v.beforeStore(path, raw)
	}

	return v.waitVault.Put(ctx, path, json.RawMessage(raw))
}

// managerWithTasks starts a manager over the vault, waits for its load, and
// puts a running upgrade task with one pending instance in it for each ID,
// as if each had been created and nothing had processed it yet. The
// manager stops when the test ends.
func managerWithTasks(t *testing.T, vault interfaces.Vault, taskIDs ...string) *Manager {
	t.Helper()

	manager := NewManager(&logger.NoOpLogger{}, newWaitDirector(), vault)
	t.Cleanup(manager.Stop)
	manager.Start()

	// The load reads the vault in the background, so the test's gates are
	// set only once it is done.
	waitForLoad(t, manager)

	manager.mu.Lock()
	defer manager.mu.Unlock()

	for _, taskID := range taskIDs {
		started := time.Now()

		manager.tasks[taskID] = &UpgradeTask{
			ID:             taskID,
			Status:         TaskStatusRunning,
			TargetStemcell: StemcellTarget{OS: "ubuntu-noble", Version: "1.2"},
			Instances: []InstanceUpgrade{
				{InstanceID: waitInstanceID, DeploymentName: waitDeployment, Status: InstanceStatusPending},
			},
			TotalCount: 1,
			CreatedAt:  started,
			StartedAt:  &started,
		}
	}

	return manager
}

// liveTask returns the manager's own task with the ID.
func liveTask(manager *Manager, taskID string) *UpgradeTask {
	manager.mu.RLock()
	defer manager.mu.RUnlock()

	return manager.tasks[taskID]
}

// A save that started before a cancel must not land after the cancel's
// save, or the stored task forgets the cancel and a restart runs the
// upgrade again.
//
//nolint:paralleltest // it holds a save for up to 500 milliseconds
func TestCancelDuringASaveIsNotOverwritten(t *testing.T) {
	const taskID = "task-cancelled-during-a-save"

	taskPath := upgradeTasksVaultPath + "/" + taskID
	vault := newGatedVault()
	manager := managerWithTasks(t, vault, taskID)

	held := make(chan struct{})
	landed := make(chan struct{})

	var holdOnce, landOnce sync.Once

	// The first save of the task waits until the cancel's save is stored,
	// or 500 milliseconds pass.
	vault.beforeStore = func(path string, _ []byte) {
		if path != taskPath {
			return
		}

		first := false

		holdOnce.Do(func() { first = true })

		if !first {
			return
		}

		close(held)

		select {
		case <-landed:
		case <-time.After(500 * time.Millisecond):
		}
	}
	vault.onPut = func(path string, raw []byte) {
		if path == taskPath && strings.Contains(string(raw), `"status":"cancelled"`) {
			landOnce.Do(func() { close(landed) })
		}
	}

	saved := make(chan error, 1)
	task := liveTask(manager, taskID)

	go func() { saved <- manager.persistTask(context.Background(), task) }()

	select {
	case <-held:
	case <-time.After(waitTimeout):
		t.Fatal("the first save never reached the vault")
	}

	err := manager.CancelTask(context.Background(), taskID)
	if err != nil {
		t.Fatalf("the cancel was refused: %v", err)
	}

	select {
	case err := <-saved:
		if err != nil {
			t.Fatalf("the first save failed: %v", err)
		}
	case <-time.After(waitTimeout):
		t.Fatal("the first save never finished")
	}

	if stored := vault.savedTask(t, taskID); stored.Status != TaskStatusCancelled {
		t.Fatalf("expected the stored task to stay cancelled, got %s", stored.Status)
	}
}

// Two tasks saved at once each add themselves to the tasks index. Neither
// may write an index read before the other's entry landed.
//
//nolint:paralleltest // it holds the index reads for up to 500 milliseconds
func TestConcurrentPersistsKeepEveryIndexEntry(t *testing.T) {
	taskIDs := []string{"task-saved-first", "task-saved-second"}
	vault := newGatedVault()
	manager := managerWithTasks(t, vault, taskIDs...)

	var (
		readsMu sync.Mutex
		reads   int
	)

	bothReading := make(chan struct{})

	// Each read of the index is answered once a second read has been
	// made, or 500 milliseconds pass, so two saves that may overlap do.
	vault.afterGet = func(path string) {
		if path != upgradeTasksVaultPath {
			return
		}

		readsMu.Lock()
		reads++

		if reads == len(taskIDs) {
			close(bothReading)
		}
		readsMu.Unlock()

		select {
		case <-bothReading:
		case <-time.After(500 * time.Millisecond):
		}
	}

	var saves sync.WaitGroup

	for _, taskID := range taskIDs {
		task := liveTask(manager, taskID)

		saves.Add(1)

		go func() {
			defer saves.Done()

			err := manager.persistTask(context.Background(), task)
			if err != nil {
				t.Errorf("saving %s failed: %v", taskID, err)
			}
		}()
	}

	saves.Wait()

	vault.afterGet = nil

	var index map[string]interface{}

	_, err := vault.Get(context.Background(), upgradeTasksVaultPath, &index)
	if err != nil {
		t.Fatalf("the tasks index could not be read: %v", err)
	}

	for _, taskID := range taskIDs {
		if _, ok := index[taskID]; !ok {
			t.Errorf("the tasks index lost the entry for %s: %v", taskID, index)
		}
	}
}

// A pause saves the task, and a save can be slow. Readers of the task must
// not wait for it.
//
//nolint:paralleltest // it times a read of the manager's tasks
func TestPauseDoesNotHoldTheLockAcrossTheSave(t *testing.T) {
	const taskID = "task-paused-during-a-slow-save"

	taskPath := upgradeTasksVaultPath + "/" + taskID
	vault := newGatedVault()
	manager := managerWithTasks(t, vault, taskID)

	held := make(chan struct{})
	release := make(chan struct{})

	var holdOnce, releaseOnce sync.Once

	vault.beforeStore = func(path string, _ []byte) {
		if path != taskPath {
			return
		}

		holdOnce.Do(func() {
			close(held)
			<-release
		})
	}

	var pauseErr error

	paused := make(chan struct{})

	go func() {
		defer close(paused)

		pauseErr = manager.PauseTask(context.Background(), taskID)
	}()

	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
		<-paused
	})

	select {
	case <-held:
	case <-time.After(waitTimeout):
		t.Fatal("the pause never saved the task")
	}

	read := make(chan error, 1)

	go func() {
		_, err := manager.GetTask(taskID)
		read <- err
	}()

	select {
	case err := <-read:
		if err != nil {
			t.Fatalf("GetTask failed: %v", err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("GetTask waited for the pause's save to the vault")
	}

	releaseOnce.Do(func() { close(release) })

	<-paused

	if pauseErr != nil {
		t.Fatalf("the pause failed: %v", pauseErr)
	}
}

// A caller that changes the task GetTask returned must not change the
// manager's own task, which the manager reads and writes under its lock.
//
//nolint:paralleltest // the upgrade package's tests run serially
func TestGetTaskReturnsASnapshot(t *testing.T) {
	const taskID = "task-read-and-changed"

	manager := managerWithTasks(t, newWaitVault(), taskID)

	manager.mu.RLock()
	started := *manager.tasks[taskID].StartedAt
	manager.mu.RUnlock()

	returned, err := manager.GetTask(taskID)
	if err != nil {
		t.Fatalf("GetTask failed: %v", err)
	}

	returned.Status = TaskStatusFailed
	returned.Instances[0].Status = InstanceStatusFailed
	*returned.StartedAt = time.Time{}

	after := liveTask(manager, taskID)

	manager.mu.RLock()
	defer manager.mu.RUnlock()

	if after.Status != TaskStatusRunning || after.Instances[0].Status != InstanceStatusPending || !after.StartedAt.Equal(started) {
		t.Fatalf("changing the task GetTask returned changed the manager's task: %+v", *after)
	}
}
