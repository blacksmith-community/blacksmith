package bosh_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"blacksmith/internal/bosh"
)

// batchWaitLimit bounds how long a test waits for something the batch
// director does on another goroutine. A test that passes never waits this
// long.
const batchWaitLimit = 5 * time.Second

// The director's description of a deploy, and the task states the tests
// use.
const (
	createDeploymentDescription = "create deployment"
	batchTaskProcessing         = "processing"
	batchTaskDone               = "done"
	batchTaskError              = "error"
)

var errBatchTestRejected = errors.New("the director rejected the deploy")

// fakeBatchTask is a task the fake base director started.
type fakeBatchTask struct {
	task bosh.Task
	done chan struct{}
}

// fakeBatchBase is a base director for the batch director. A deploy it
// accepts stays processing until the test finishes its task, unless the
// test makes deploys block until they finish, the way the bosh-cli adapter
// does.
type fakeBatchBase struct {
	bosh.Director

	mu       sync.Mutex
	nextID   int
	tasks    map[int]*fakeBatchTask
	accepted []string
	acceptCh chan string

	// blockUntilFinished makes UpdateDeployment return only once the test
	// finishes the task, with the finished task.
	blockUntilFinished bool
	// updateErr, when it returns an error, is UpdateDeployment's answer.
	updateErr func(name string) error
}

func newFakeBatchBase() *fakeBatchBase {
	return &fakeBatchBase{
		nextID:   100,
		tasks:    map[int]*fakeBatchTask{},
		acceptCh: make(chan string, 64),
	}
}

func (f *fakeBatchBase) UpdateDeployment(ctx context.Context, name, _ string) (*bosh.Task, error) {
	if f.updateErr != nil {
		err := f.updateErr(name)
		if err != nil {
			return nil, err
		}
	}

	f.mu.Lock()
	f.nextID++
	started := &fakeBatchTask{
		task: bosh.Task{ID: f.nextID, State: batchTaskProcessing, Description: createDeploymentDescription, Deployment: name},
		done: make(chan struct{}),
	}
	f.tasks[started.task.ID] = started
	f.accepted = append(f.accepted, name)
	accepted := started.task
	f.mu.Unlock()

	f.acceptCh <- name

	if !f.blockUntilFinished {
		return &accepted, nil
	}

	select {
	case <-started.done:
	case <-ctx.Done():
		return nil, fmt.Errorf("deploy of %s: %w", name, ctx.Err())
	}

	return f.taskSnapshot(accepted.ID), nil
}

func (f *fakeBatchBase) WaitForTask(ctx context.Context, taskID int) (*bosh.Task, error) {
	f.mu.Lock()
	started, ok := f.tasks[taskID]
	f.mu.Unlock()

	if !ok {
		return nil, fmt.Errorf("task %d: %w", taskID, errBatchTestRejected)
	}

	select {
	case <-started.done:
		return f.taskSnapshot(taskID), nil
	case <-ctx.Done():
		return nil, fmt.Errorf("wait for task %d: %w", taskID, ctx.Err())
	}
}

func (f *fakeBatchBase) taskSnapshot(taskID int) *bosh.Task {
	f.mu.Lock()
	defer f.mu.Unlock()

	task := f.tasks[taskID].task

	return &task
}

// taskOf returns the ID of the task the deploy of name started.
func (f *fakeBatchBase) taskOf(t *testing.T, name string) int {
	t.Helper()

	f.mu.Lock()
	defer f.mu.Unlock()

	for id, started := range f.tasks {
		if started.task.Deployment == name {
			return id
		}
	}

	t.Fatalf("no task was started for deployment %s", name)

	return 0
}

// finish ends a task in the state given.
func (f *fakeBatchBase) finish(t *testing.T, taskID int, state string) {
	t.Helper()

	f.mu.Lock()
	defer f.mu.Unlock()

	started, ok := f.tasks[taskID]
	if !ok {
		t.Fatalf("task %d was never started", taskID)
	}

	started.task.State = state
	close(started.done)
}

// waitAccepted waits for the director to accept a deploy and returns its
// deployment name.
func (f *fakeBatchBase) waitAccepted(t *testing.T) string {
	t.Helper()

	select {
	case name := <-f.acceptCh:
		return name
	case <-time.After(batchWaitLimit):
		t.Fatal("the director accepted no deploy")

		return ""
	}
}

// acceptedCount reports how many deploys the director accepted.
func (f *fakeBatchBase) acceptedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return len(f.accepted)
}

// waitBatchStats waits until the batch director reports the active and
// queued counts given, and fails the test if it never does.
func waitBatchStats(t *testing.T, batch *bosh.BatchDirector, wantActive, wantQueued int) {
	t.Helper()

	deadline := time.Now().Add(batchWaitLimit)

	for {
		active, queued, _ := batch.GetBatchPoolStats()
		if active == wantActive && queued == wantQueued {
			return
		}

		if time.Now().After(deadline) {
			t.Fatalf("expected %d active and %d queued, got %d active and %d queued", wantActive, wantQueued, active, queued)
		}

		time.Sleep(time.Millisecond)
	}
}

// deployInBackground runs an upgrade deploy on its own goroutine and
// returns a channel with its error.
func deployInBackground(ctx context.Context, batch *bosh.BatchDirector, name string) <-chan error {
	result := make(chan error, 1)

	go func() {
		_, err := batch.UpdateDeployment(ctx, name, "manifest")
		result <- err
	}()

	return result
}

func TestBatchPoolStatsReportInFlightWaitersAndLimit(t *testing.T) {
	t.Parallel()

	base := newFakeBatchBase()
	base.blockUntilFinished = true
	batch := bosh.NewBatchDirector(base, 1, time.Minute, nil)

	first := deployInBackground(t.Context(), batch, "dep-1")
	base.waitAccepted(t)

	second := deployInBackground(t.Context(), batch, "dep-2")
	waitBatchStats(t, batch, 1, 1)

	_, _, limit := batch.GetBatchPoolStats()
	if limit != 1 {
		t.Fatalf("expected a limit of 1, got %d", limit)
	}

	base.finish(t, base.taskOf(t, "dep-1"), batchTaskDone)
	base.waitAccepted(t)
	base.finish(t, base.taskOf(t, "dep-2"), batchTaskDone)

	for _, result := range []<-chan error{first, second} {
		err := <-result
		if err != nil {
			t.Fatalf("expected the deploy to succeed, got %v", err)
		}
	}
}

func TestBatchLimitOfZeroOrLessMeansTheDefault(t *testing.T) {
	t.Parallel()

	batch := bosh.NewBatchDirector(newFakeBatchBase(), 0, time.Minute, nil)

	_, _, limit := batch.GetBatchPoolStats()
	if limit != 4 {
		t.Fatalf("expected max_batch_jobs of 0 to mean 4, got %d", limit)
	}

	batch.UpdateMaxBatchJobs(-1)

	_, _, limit = batch.GetBatchPoolStats()
	if limit != 4 {
		t.Fatalf("expected max_batch_jobs of -1 to mean 4, got %d", limit)
	}
}

func TestResizingTheBatchCountsDeploysAlreadyRunning(t *testing.T) {
	t.Parallel()

	base := newFakeBatchBase()
	base.blockUntilFinished = true
	batch := bosh.NewBatchDirector(base, 2, time.Minute, nil)

	first := deployInBackground(t.Context(), batch, "dep-1")
	second := deployInBackground(t.Context(), batch, "dep-2")

	base.waitAccepted(t)
	base.waitAccepted(t)

	batch.UpdateMaxBatchJobs(1)

	third := deployInBackground(t.Context(), batch, "dep-3")
	waitBatchStats(t, batch, 2, 1)

	if got := base.acceptedCount(); got != 2 {
		t.Fatalf("expected the third deploy to wait while two run against a limit of 1, but %d were accepted", got)
	}

	base.finish(t, base.taskOf(t, "dep-1"), batchTaskDone)

	err := <-first
	if err != nil {
		t.Fatalf("expected the first deploy to succeed, got %v", err)
	}

	waitBatchStats(t, batch, 1, 1)

	if got := base.acceptedCount(); got != 2 {
		t.Fatalf("expected the third deploy to wait while one runs against a limit of 1, but %d were accepted", got)
	}

	base.finish(t, base.taskOf(t, "dep-2"), batchTaskDone)

	if name := base.waitAccepted(t); name != "dep-3" {
		t.Fatalf("expected dep-3 to be accepted, got %s", name)
	}

	base.finish(t, base.taskOf(t, "dep-3"), batchTaskDone)

	for _, result := range []<-chan error{second, third} {
		err := <-result
		if err != nil {
			t.Fatalf("expected the deploy to succeed, got %v", err)
		}
	}

	waitBatchStats(t, batch, 0, 0)
}

// TestResizingTheBatchWhileDeploysRunIsRaceFree resizes the batch while
// deploys take and release slots. Run with -race, it fails on any resize
// that is not under the slot lock.
func TestResizingTheBatchWhileDeploysRunIsRaceFree(t *testing.T) {
	t.Parallel()

	base := newFakeBatchBase()
	batch := bosh.NewBatchDirector(base, 2, time.Minute, nil)

	const deploys = 20

	var group sync.WaitGroup

	for index := range deploys {
		group.Go(func() {
			_, err := batch.UpdateDeployment(t.Context(), fmt.Sprintf("dep-%d", index), "manifest")
			if err != nil {
				t.Errorf("expected deploy %d to succeed, got %v", index, err)
			}
		})
	}

	for size := range deploys {
		batch.UpdateMaxBatchJobs(size%3 + 1)
	}

	for index := range deploys {
		name := base.waitAccepted(t)

		state := batchTaskDone
		if index%2 == 1 {
			state = batchTaskError
		}

		base.finish(t, base.taskOf(t, name), state)
	}

	group.Wait()
}
