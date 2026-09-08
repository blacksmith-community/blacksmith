package upgrade

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/interfaces"
	"blacksmith/pkg/logger"
)

// --- test doubles ---------------------------------------------------------
//
// Both fakes EMBED the real interface (as a nil value) so they satisfy the
// full method surface for free; we override only what upgradeInstance calls.

// fakeDirector models the new flow: UpdateDeploymentAsync fires and returns a
// task id immediately; the watch is our own GetTask poll loop. `state` is what
// GetTask reports. If getTaskGate is non-nil, the FIRST GetTask call blocks on
// it — simulating a single stalled poll that the per-call timeout must survive.
type fakeDirector struct {
	bosh.Director

	manifest string
	taskID   int

	mu           sync.Mutex
	state        string
	getTaskGate  chan struct{}
	getTaskCalls int32
	fireCalls    int32
}

func (f *fakeDirector) setState(s string) {
	f.mu.Lock()
	f.state = s
	f.mu.Unlock()
}

func (f *fakeDirector) GetDeployment(name string) (*bosh.DeploymentDetail, error) {
	return &bosh.DeploymentDetail{Name: name, Manifest: f.manifest}, nil
}

func (f *fakeDirector) UpdateDeploymentAsync(_, _ string) (*bosh.Task, error) {
	atomic.AddInt32(&f.fireCalls, 1)

	return &bosh.Task{ID: f.taskID, State: "queued"}, nil
}

func (f *fakeDirector) GetTask(_ int) (*bosh.Task, error) {
	if atomic.AddInt32(&f.getTaskCalls, 1) == 1 && f.getTaskGate != nil {
		<-f.getTaskGate // first poll stalls until the test releases it (or forever)
	}

	f.mu.Lock()
	s := f.state
	f.mu.Unlock()

	return &bosh.Task{ID: f.taskID, State: s}, nil
}

// fakeVault: nothing persisted; reads report "not found", writes succeed.
type fakeVault struct {
	interfaces.Vault
}

func (f *fakeVault) Get(_ context.Context, _ string, _ interface{}) (bool, error) { return false, nil }
func (f *fakeVault) Put(_ context.Context, _ string, _ interface{}) error         { return nil }

// --- helpers --------------------------------------------------------------

func setFastWatch(t *testing.T, deadline time.Duration) {
	t.Helper()

	oldD, oldP, oldC := instanceDeadline, pollInterval, perCallTimeout
	instanceDeadline = deadline
	pollInterval = 5 * time.Millisecond
	perCallTimeout = 20 * time.Millisecond

	t.Cleanup(func() {
		instanceDeadline, pollInterval, perCallTimeout = oldD, oldP, oldC
	})
}

func instStatus(m *Manager, task *UpgradeTask, i int) InstanceStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return task.Instances[i].Status
}

func waitForInstanceStatus(m *Manager, task *UpgradeTask, i int, want InstanceStatus, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if instStatus(m, task, i) == want {
			return true
		}

		time.Sleep(5 * time.Millisecond)
	}

	return false
}

func oneInstanceTask() *UpgradeTask {
	return &UpgradeTask{
		ID:             "test-task",
		Status:         TaskStatusPending,
		TargetStemcell: StemcellTarget{OS: "ubuntu-jammy", Version: "2.0"},
		Instances: []InstanceUpgrade{
			{InstanceID: "i-1", DeploymentName: "test-dep", Status: InstanceStatusPending},
		},
		TotalCount: 1,
		CreatedAt:  time.Now(),
	}
}

const testManifest = "name: test-dep\nstemcells:\n- alias: default\n  os: ubuntu-jammy\n  version: \"1.0\"\n"

// --- tests ----------------------------------------------------------------

// The original bug: BOSH task done, but blacksmith stuck. Now the watch is our
// own GetTask poll, so a done task completes the instance promptly.
func TestUpgradeInstance_CompletesWhenTaskDone(t *testing.T) {
	setFastWatch(t, time.Second)

	dir := &fakeDirector{manifest: testManifest, taskID: 42, state: "done"}
	m := NewManager(&logger.NoOpLogger{}, dir, &fakeVault{})

	task := oneInstanceTask()
	go m.processTask(task)

	if !waitForInstanceStatus(m, task, 0, InstanceStatusSuccess, 2*time.Second) {
		t.Fatalf("expected success, got %q", instStatus(m, task, 0))
	}
}

// A single stalled poll must NOT hang the watch: the per-call timeout abandons
// it and the next poll (fresh connection) sees the task done.
func TestUpgradeInstance_RecoversFromStalledPoll(t *testing.T) {
	setFastWatch(t, time.Second)

	gate := make(chan struct{})
	dir := &fakeDirector{manifest: testManifest, taskID: 7, state: "done", getTaskGate: gate}
	t.Cleanup(func() { close(gate) }) // release the leaked first-poll goroutine at end

	m := NewManager(&logger.NoOpLogger{}, dir, &fakeVault{})

	task := oneInstanceTask()
	go m.processTask(task)

	if !waitForInstanceStatus(m, task, 0, InstanceStatusSuccess, 2*time.Second) {
		t.Fatalf("expected success after stalled-poll recovery, got %q", instStatus(m, task, 0))
	}
}

// A task that never finishes must time out at the deadline, be marked timed-out
// (not failed), and the job must move on to the next instance.
func TestUpgradeInstance_TimesOutAndContinues(t *testing.T) {
	setFastWatch(t, 60*time.Millisecond)

	dir := &fakeDirector{manifest: testManifest, taskID: 9, state: "processing"}
	m := NewManager(&logger.NoOpLogger{}, dir, &fakeVault{})

	task := &UpgradeTask{
		ID:             "test-task",
		Status:         TaskStatusPending,
		TargetStemcell: StemcellTarget{OS: "ubuntu-jammy", Version: "2.0"},
		Instances: []InstanceUpgrade{
			{InstanceID: "i-1", DeploymentName: "test-dep", Status: InstanceStatusPending},
			{InstanceID: "i-2", DeploymentName: "test-dep", Status: InstanceStatusPending},
		},
		TotalCount: 2,
		CreatedAt:  time.Now(),
	}

	finished := make(chan struct{})
	go func() { m.processTask(task); close(finished) }()

	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("processTask did not finish; a timed-out instance wedged the job")
	}

	for i := 0; i < 2; i++ {
		if got := instStatus(m, task, i); got != InstanceStatusTimedOut {
			t.Fatalf("instance %d: expected timed_out, got %q", i, got)
		}
	}

	if task.TimedOutCount != 2 {
		t.Fatalf("expected TimedOutCount=2, got %d", task.TimedOutCount)
	}
}

func TestUpgradeInstance_TaskError(t *testing.T) {
	setFastWatch(t, time.Second)

	dir := &fakeDirector{manifest: testManifest, taskID: 5, state: "error"}
	m := NewManager(&logger.NoOpLogger{}, dir, &fakeVault{})

	task := oneInstanceTask()
	go m.processTask(task)

	if !waitForInstanceStatus(m, task, 0, InstanceStatusFailed, 2*time.Second) {
		t.Fatalf("expected failed, got %q", instStatus(m, task, 0))
	}
}
