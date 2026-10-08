package bosh_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"blacksmith/internal/bosh"

	"github.com/fivetwenty-io/boshapi/pkg/boshapi"
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

// The deployments and the context ID the tests deploy with.
const (
	firstDeployment  = "dep-1"
	secondDeployment = "dep-2"
	thirdDeployment  = "dep-3"
	fourthDeployment = "dep-4"
	firstContextID   = "ctx-" + firstDeployment
	batchTestAddress = "https://127.0.0.1:25555"
	deploymentsRoute = "/deployments"
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

	// waitCalls counts the WaitForTask calls for each task.
	waitCalls map[int]int
	// waitErr, when set, is every WaitForTask call's answer.
	waitErr error
	// byContextID maps a context ID to the task the director lists for it.
	byContextID map[string]int
	// contextErr, when set, is every FindTaskByContextID call's answer.
	contextErr error
	// contextLookups counts the FindTaskByContextID calls.
	contextLookups int
	// running is the task FindRunningTaskForDeployment answers with.
	running *bosh.Task
	// runningErr, when set, is every FindRunningTaskForDeployment call's
	// answer.
	runningErr error

	// blockUntilFinished makes UpdateDeployment return only once the test
	// finishes the task, with the finished task.
	blockUntilFinished bool
	// updateErr, when it returns an error, is UpdateDeployment's answer.
	updateErr func(name string) error
	// taskWithErr, when set, is the task UpdateDeployment returns with the
	// error updateErr gives.
	taskWithErr *bosh.Task
	// noTask makes UpdateDeployment accept nothing and name no task.
	noTask bool
	// hold, when set, runs first in every UpdateDeployment call, so a test
	// can keep the call in flight.
	hold func(name string)
}

func newFakeBatchBase() *fakeBatchBase {
	return &fakeBatchBase{
		nextID:      100,
		tasks:       map[int]*fakeBatchTask{},
		acceptCh:    make(chan string, 64),
		waitCalls:   map[int]int{},
		byContextID: map[string]int{},
	}
}

func (f *fakeBatchBase) UpdateDeployment(ctx context.Context, name, _ string) (*bosh.Task, error) {
	if f.hold != nil {
		f.hold(name)
	}

	if f.updateErr != nil {
		err := f.updateErr(name)
		if err != nil {
			return f.taskWithErr, err
		}
	}

	if f.noTask {
		return nil, nil //nolint:nilnil // a deploy the director names no task for is what the test is about
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
	f.waitCalls[taskID]++
	started, found := f.tasks[taskID]
	waitErr := f.waitErr
	f.mu.Unlock()

	if waitErr != nil {
		return nil, waitErr
	}

	if !found {
		return nil, fmt.Errorf("task %d: %w", taskID, errBatchTestRejected)
	}

	select {
	case <-started.done:
		return f.taskSnapshot(taskID), nil
	case <-ctx.Done():
		return nil, fmt.Errorf("wait for task %d: %w", taskID, ctx.Err())
	}
}

func (f *fakeBatchBase) FindTaskByContextID(_ context.Context, contextID string) (*bosh.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.contextLookups++

	if f.contextErr != nil {
		return nil, f.contextErr
	}

	taskID, ok := f.byContextID[contextID]
	if !ok {
		return nil, nil //nolint:nilnil // no task with the context ID is an answer
	}

	task := f.tasks[taskID].task

	return &task, nil
}

func (f *fakeBatchBase) FindRunningTaskForDeployment(_ context.Context, _ string) (*bosh.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.runningErr != nil {
		return nil, f.runningErr
	}

	if f.running == nil {
		return nil, nil //nolint:nilnil // no running task is an answer
	}

	task := *f.running

	return &task, nil
}

// addTask records a task the director runs that no deploy through the fake
// started.
func (f *fakeBatchBase) addTask(taskID int, deployment, description string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.tasks[taskID] = &fakeBatchTask{
		task: bosh.Task{ID: taskID, State: batchTaskProcessing, Description: description, Deployment: deployment},
		done: make(chan struct{}),
	}
}

// set changes what the fake answers, under its lock.
func (f *fakeBatchBase) set(change func(f *fakeBatchBase)) {
	f.mu.Lock()
	defer f.mu.Unlock()

	change(f)
}

// waitCallsFor reports how many times WaitForTask was called for a task.
func (f *fakeBatchBase) waitCallsFor(taskID int) int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.waitCalls[taskID]
}

// lookups reports how many times FindTaskByContextID was called.
func (f *fakeBatchBase) lookups() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.contextLookups
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

// fakeBatchSleeper is a watcher waiting on the fake clock.
type fakeBatchSleeper struct {
	until    time.Time
	duration time.Duration
	wake     chan struct{}
}

// fakeBatchClock is a clock that moves only when the test advances it.
type fakeBatchClock struct {
	mu       sync.Mutex
	now      time.Time
	sleepers []*fakeBatchSleeper
}

func newFakeBatchClock() *fakeBatchClock {
	return &fakeBatchClock{now: time.Date(2026, time.October, 8, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeBatchClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now
}

func (c *fakeBatchClock) Sleep(ctx context.Context, duration time.Duration) error {
	if duration <= 0 {
		return nil
	}

	c.mu.Lock()
	sleeper := &fakeBatchSleeper{until: c.now.Add(duration), duration: duration, wake: make(chan struct{})}
	c.sleepers = append(c.sleepers, sleeper)
	c.mu.Unlock()

	select {
	case <-sleeper.wake:
		return nil
	case <-ctx.Done():
		c.mu.Lock()
		defer c.mu.Unlock()

		for index, waiting := range c.sleepers {
			if waiting == sleeper {
				c.sleepers = append(c.sleepers[:index], c.sleepers[index+1:]...)

				break
			}
		}

		return fmt.Errorf("fake sleep: %w", ctx.Err())
	}
}

// advance moves the clock on and wakes every sleeper that is due.
func (c *fakeBatchClock) advance(duration time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.now = c.now.Add(duration)
	waiting := c.sleepers[:0]

	for _, sleeper := range c.sleepers {
		if sleeper.until.After(c.now) {
			waiting = append(waiting, sleeper)

			continue
		}

		close(sleeper.wake)
	}

	c.sleepers = waiting
}

// sleeping reports how many sleepers wait for the duration given.
func (c *fakeBatchClock) sleeping(duration time.Duration) int {
	c.mu.Lock()
	defer c.mu.Unlock()

	count := 0

	for _, sleeper := range c.sleepers {
		if sleeper.duration == duration {
			count++
		}
	}

	return count
}

// retrySleep returns the duration of a sleeper that waits to read a task
// again, or 0 when none does.
func (c *fakeBatchClock) retrySleep() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, sleeper := range c.sleepers {
		if sleeper.duration != bosh.BatchDonePrune && sleeper.duration != bosh.BatchLostAnswerInterval {
			return sleeper.duration
		}
	}

	return 0
}

// waitUntil waits until the condition holds, and fails the test if it
// never does.
func waitUntil(t *testing.T, what string, condition func() bool) {
	t.Helper()

	deadline := time.Now().Add(batchWaitLimit)

	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting until %s", what)
		}

		time.Sleep(time.Millisecond)
	}
}

// waitSleeping waits until a watcher sleeps for the duration given.
func (c *fakeBatchClock) waitSleeping(t *testing.T, duration time.Duration) {
	t.Helper()

	waitUntil(t, fmt.Sprintf("a watcher sleeps for %v", duration), func() bool { return c.sleeping(duration) > 0 })
}

// newTestBatch returns a batch director over the fake base director with
// the limit given and a fake clock, and stops it when the test ends.
func newTestBatch(t *testing.T, base *fakeBatchBase, limit int) (*bosh.BatchDirector, *fakeBatchClock) {
	t.Helper()

	batch := bosh.NewBatchDirector(base, limit, time.Minute, nil)
	clock := newFakeBatchClock()
	bosh.SetBatchClock(batch, clock)
	t.Cleanup(batch.Stop)

	return batch, clock
}

// lostAnswer makes the deploy of the deployment given lose its answer.
func lostAnswer(deployment string) func(name string) error {
	return func(name string) error {
		if name != deployment {
			return nil
		}

		return &bosh.OutcomeUnknownError{
			Operation:  "UpdateDeployment",
			Director:   batchTestAddress,
			Deployment: name,
			ContextID:  "ctx-" + name,
			Err:        errBatchTestRejected,
		}
	}
}

// waitResult is what a background wait returned.
type waitResult struct {
	task *bosh.Task
	err  error
}

// waitInBackground runs a wait on its own goroutine and returns a channel
// with its result.
func waitInBackground(wait func() (*bosh.Task, error)) <-chan waitResult {
	result := make(chan waitResult, 1)

	go func() {
		task, err := wait()
		result <- waitResult{task: task, err: err}
	}()

	return result
}

// receive returns a background wait's result, and fails the test if it
// never comes.
func receive(t *testing.T, result <-chan waitResult) waitResult {
	t.Helper()

	select {
	case got := <-result:
		return got
	case <-time.After(batchWaitLimit):
		t.Fatal("the wait never returned")

		return waitResult{}
	}
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
	t.Cleanup(batch.Stop)

	first := deployInBackground(t.Context(), batch, firstDeployment)
	base.waitAccepted(t)

	second := deployInBackground(t.Context(), batch, secondDeployment)
	waitBatchStats(t, batch, 1, 1)

	_, _, limit := batch.GetBatchPoolStats()
	if limit != 1 {
		t.Fatalf("expected a limit of 1, got %d", limit)
	}

	base.finish(t, base.taskOf(t, firstDeployment), batchTaskDone)
	base.waitAccepted(t)
	base.finish(t, base.taskOf(t, secondDeployment), batchTaskDone)

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
	t.Cleanup(batch.Stop)

	_, _, limit := batch.GetBatchPoolStats()
	if limit != 4 {
		t.Fatalf("expected max_batch_jobs of 0 to mean 4, got %d", limit)
	}

	for _, size := range []int{0, -1} {
		batch.UpdateMaxBatchJobs(2)
		batch.UpdateMaxBatchJobs(size)

		_, _, limit = batch.GetBatchPoolStats()
		if limit != 4 {
			t.Fatalf("expected max_batch_jobs of %d to mean 4, got %d", size, limit)
		}
	}
}

// queueDeploy starts a deploy that has to wait for a slot, and waits until
// it is counted as waiting, so the deploys a test queues ask in order.
func queueDeploy(ctx context.Context, t *testing.T, batch *bosh.BatchDirector, name string) <-chan error {
	t.Helper()

	_, queued, _ := batch.GetBatchPoolStats()
	result := deployInBackground(ctx, batch, name)

	waitUntil(t, name+" waits for a slot", func() bool {
		_, waiting, _ := batch.GetBatchPoolStats()

		return waiting == queued+1
	})

	return result
}

func TestBatchSlotsGoToWaitingDeploysInTheOrderTheyAsked(t *testing.T) {
	t.Parallel()

	base := newFakeBatchBase()
	batch, _ := newTestBatch(t, base, 1)

	held := startDeploy(t, base, batch, firstDeployment)

	waiting := []string{secondDeployment, thirdDeployment, fourthDeployment}
	for _, name := range waiting {
		queueDeploy(t.Context(), t, batch, name)
	}

	for _, want := range waiting {
		base.finish(t, held, batchTaskDone)

		name := base.waitAccepted(t)
		if name != want {
			t.Fatalf("expected %s to get the slot next, got %s", want, name)
		}

		held = base.taskOf(t, name)
	}
}

// A deploy that asks for a slot while another deploy waits joins the end
// of the queue, even when a slot is free at that moment.
func TestANewDeployDoesNotTakeASlotAheadOfAWaitingOne(t *testing.T) {
	t.Parallel()

	base := newFakeBatchBase()
	batch, _ := newTestBatch(t, base, 1)

	startDeploy(t, base, batch, firstDeployment)
	waiter := queueDeploy(t.Context(), t, batch, secondDeployment)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	firstAccepted := make(chan string, 1)

	go func() {
		defer cancel()

		select {
		case name := <-base.acceptCh:
			firstAccepted <- name
		case <-time.After(batchWaitLimit):
			firstAccepted <- "no deploy"
		}
	}()

	batch.UpdateMaxBatchJobs(2)

	_, err := batch.UpdateDeployment(ctx, thirdDeployment, "manifest")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected the new deploy to wait behind dep-2 until it gave up, got %v", err)
	}

	if name := <-firstAccepted; name != secondDeployment {
		t.Fatalf("expected the waiting dep-2 to get the free slot, got %s", name)
	}

	err = <-waiter
	if err != nil {
		t.Fatalf("expected dep-2 to be accepted, got %v", err)
	}

	waitBatchStats(t, batch, 2, 0)
}

// A waiter that stops waiting leaves the queue, so the waiter behind it
// gets the next slot.
func TestACancelledWaiterLetsTheNextWaiterIn(t *testing.T) {
	t.Parallel()

	base := newFakeBatchBase()
	batch, _ := newTestBatch(t, base, 1)

	held := startDeploy(t, base, batch, firstDeployment)

	ctx, cancel := context.WithCancel(t.Context())
	cancelled := queueDeploy(ctx, t, batch, secondDeployment)
	next := queueDeploy(t.Context(), t, batch, thirdDeployment)

	cancel()

	err := <-cancelled
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected the cancelled waiter to end with its context, got %v", err)
	}

	waitBatchStats(t, batch, 1, 1)
	base.finish(t, held, batchTaskDone)

	if name := base.waitAccepted(t); name != thirdDeployment {
		t.Fatalf("expected dep-3 to get the slot, got %s", name)
	}

	err = <-next
	if err != nil {
		t.Fatalf("expected dep-3 to be accepted, got %v", err)
	}
}

func TestAWaiterThatTimesOutLeavesTheQueue(t *testing.T) {
	t.Parallel()

	base := newFakeBatchBase()
	batch := bosh.NewBatchDirector(base, 1, 50*time.Millisecond, nil)
	bosh.SetBatchClock(batch, newFakeBatchClock())
	t.Cleanup(batch.Stop)

	startDeploy(t, base, batch, firstDeployment)

	_, err := batch.UpdateDeployment(t.Context(), secondDeployment, "manifest")
	if !errors.Is(err, bosh.ErrBatchPoolTimeout) {
		t.Fatalf("expected the waiter to time out, got %v", err)
	}

	waitBatchStats(t, batch, 1, 0)
}

func TestStopWakesTheDeploysWaitingForASlot(t *testing.T) {
	t.Parallel()

	base := newFakeBatchBase()
	batch, _ := newTestBatch(t, base, 1)

	startDeploy(t, base, batch, firstDeployment)
	waiting := queueDeploy(t.Context(), t, batch, secondDeployment)

	batch.Stop()

	select {
	case err := <-waiting:
		if !errors.Is(err, bosh.ErrBatchDirectorStopped) {
			t.Fatalf("expected the waiter to get ErrBatchDirectorStopped, got %v", err)
		}
	case <-time.After(batchWaitLimit):
		t.Fatal("Stop did not wake the deploy waiting for a slot")
	}

	waitBatchStats(t, batch, 0, 0)
}

func TestRaisingTheLimitWakesAWaitingDeploy(t *testing.T) {
	t.Parallel()

	base := newFakeBatchBase()
	batch, _ := newTestBatch(t, base, 1)

	startDeploy(t, base, batch, firstDeployment)
	waiting := queueDeploy(t.Context(), t, batch, secondDeployment)

	batch.UpdateMaxBatchJobs(2)

	if name := base.waitAccepted(t); name != secondDeployment {
		t.Fatalf("expected dep-2 to be accepted once the limit was raised, got %s", name)
	}

	err := <-waiting
	if err != nil {
		t.Fatalf("expected dep-2 to be accepted, got %v", err)
	}

	waitBatchStats(t, batch, 2, 0)
}

func TestResizingTheBatchCountsDeploysAlreadyRunning(t *testing.T) {
	t.Parallel()

	base := newFakeBatchBase()
	base.blockUntilFinished = true
	batch := bosh.NewBatchDirector(base, 2, time.Minute, nil)
	t.Cleanup(batch.Stop)

	first := deployInBackground(t.Context(), batch, firstDeployment)
	second := deployInBackground(t.Context(), batch, secondDeployment)

	base.waitAccepted(t)
	base.waitAccepted(t)

	batch.UpdateMaxBatchJobs(1)

	third := deployInBackground(t.Context(), batch, thirdDeployment)
	waitBatchStats(t, batch, 2, 1)

	if got := base.acceptedCount(); got != 2 {
		t.Fatalf("expected the third deploy to wait while two run against a limit of 1, but %d were accepted", got)
	}

	base.finish(t, base.taskOf(t, firstDeployment), batchTaskDone)

	err := <-first
	if err != nil {
		t.Fatalf("expected the first deploy to succeed, got %v", err)
	}

	waitBatchStats(t, batch, 1, 1)

	if got := base.acceptedCount(); got != 2 {
		t.Fatalf("expected the third deploy to wait while one runs against a limit of 1, but %d were accepted", got)
	}

	base.finish(t, base.taskOf(t, secondDeployment), batchTaskDone)

	if name := base.waitAccepted(t); name != thirdDeployment {
		t.Fatalf("expected dep-3 to be accepted, got %s", name)
	}

	base.finish(t, base.taskOf(t, thirdDeployment), batchTaskDone)

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
	t.Cleanup(batch.Stop)

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

func TestBatchSlotIsHeldUntilTheDeployFinishes(t *testing.T) {
	t.Parallel()

	base := newFakeBatchBase()
	batch, _ := newTestBatch(t, base, 2)

	first := deployInBackground(t.Context(), batch, firstDeployment)
	base.waitAccepted(t)

	second := deployInBackground(t.Context(), batch, secondDeployment)
	base.waitAccepted(t)

	for _, result := range []<-chan error{first, second} {
		err := <-result
		if err != nil {
			t.Fatalf("expected the deploy to be accepted, got %v", err)
		}
	}

	third := deployInBackground(t.Context(), batch, thirdDeployment)
	waitBatchStats(t, batch, 2, 1)

	if got := base.acceptedCount(); got != 2 {
		t.Fatalf("expected the third deploy to wait while two deploy tasks run, but %d were accepted", got)
	}

	base.finish(t, base.taskOf(t, firstDeployment), batchTaskDone)

	if name := base.waitAccepted(t); name != thirdDeployment {
		t.Fatalf("expected dep-3 to be accepted once dep-1's task was done, got %s", name)
	}

	err := <-third
	if err != nil {
		t.Fatalf("expected the third deploy to be accepted, got %v", err)
	}

	waitBatchStats(t, batch, 2, 0)
}

func TestBatchSlotIsReleasedWhenTheDeployIsRejected(t *testing.T) {
	t.Parallel()

	rejections := map[string]error{
		"a rejection": &boshapi.APIError{
			StatusCode: 400, Method: "POST", Path: deploymentsRoute, Code: 440001, Description: "manifest is invalid", Mutation: true,
		},
		"a request that was never sent": &boshapi.TransportError{
			Method: "POST", Path: deploymentsRoute, Sent: false, Mutation: true, Attempts: 1, Err: errBatchTestRejected,
		},
		"a failed read of the task after the deploy returned": fmt.Errorf("deploy: %w", bosh.ErrFailedGetTaskUpdate),
	}

	for name, rejection := range rejections {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			base := newFakeBatchBase()
			base.updateErr = func(deployment string) error {
				if deployment == firstDeployment {
					return rejection
				}

				return nil
			}
			batch, _ := newTestBatch(t, base, 1)

			_, err := batch.UpdateDeployment(t.Context(), firstDeployment, "manifest")
			if !errors.Is(err, rejection) {
				t.Fatalf("expected the director's answer, got %v", err)
			}

			waitBatchStats(t, batch, 0, 0)

			_, err = batch.UpdateDeployment(t.Context(), secondDeployment, "manifest")
			if err != nil {
				t.Fatalf("expected the next deploy to get the slot, got %v", err)
			}

			if running := bosh.BatchWatchersRunning(batch); running != 1 {
				t.Fatalf("expected only the accepted deploy to be followed, got %d watchers", running)
			}
		})
	}
}

// The bosh-cli adapter's UpdateDeployment returns once the deploy has
// finished, so the watcher's wait returns at once and frees the slot.
func TestBatchSlotIsReleasedWhenABlockingDeployReturnsAFinishedTask(t *testing.T) {
	t.Parallel()

	base := newFakeBatchBase()
	base.blockUntilFinished = true
	batch, _ := newTestBatch(t, base, 1)

	result := deployInBackground(t.Context(), batch, firstDeployment)
	base.waitAccepted(t)
	waitBatchStats(t, batch, 1, 0)

	taskID := base.taskOf(t, firstDeployment)
	base.finish(t, taskID, batchTaskDone)

	err := <-result
	if err != nil {
		t.Fatalf("expected the deploy to succeed, got %v", err)
	}

	waitBatchStats(t, batch, 0, 0)

	if calls := base.waitCallsFor(taskID); calls != 1 {
		t.Fatalf("expected the watcher to read the finished task once, got %d reads", calls)
	}
}

func TestBatchSlotIsHeldWhileAnUnknownOutcomeIsResolved(t *testing.T) {
	t.Parallel()

	base := newFakeBatchBase()
	base.updateErr = lostAnswer(firstDeployment)
	batch, clock := newTestBatch(t, base, 1)

	_, err := batch.UpdateDeployment(t.Context(), firstDeployment, "manifest")
	if !errors.Is(err, bosh.ErrOutcomeUnknown) {
		t.Fatalf("expected the lost answer to reach the caller, got %v", err)
	}

	second := deployInBackground(t.Context(), batch, secondDeployment)
	waitBatchStats(t, batch, 1, 1)
	clock.waitSleeping(t, bosh.BatchLostAnswerInterval)

	if base.lookups() == 0 {
		t.Fatal("expected the watcher to look the task up by its context ID")
	}

	const lostTaskID = 500

	base.addTask(lostTaskID, firstDeployment, createDeploymentDescription)
	base.set(func(f *fakeBatchBase) { f.byContextID[firstContextID] = lostTaskID })

	waited := waitInBackground(func() (*bosh.Task, error) { return batch.WaitForContextID(t.Context(), firstContextID) })

	clock.advance(bosh.BatchLostAnswerInterval)
	waitUntil(t, "the watcher follows the task it found", func() bool { return base.waitCallsFor(lostTaskID) == 1 })
	clock.advance(bosh.UnknownOutcomeGrace)
	waitBatchStats(t, batch, 1, 1)

	if got := base.acceptedCount(); got != 0 {
		t.Fatalf("expected the next deploy to wait while the found task runs, but %d were accepted", got)
	}

	base.finish(t, lostTaskID, batchTaskDone)

	if name := base.waitAccepted(t); name != secondDeployment {
		t.Fatalf("expected dep-2 to be accepted, got %s", name)
	}

	err = <-second
	if err != nil {
		t.Fatalf("expected the next deploy to be accepted, got %v", err)
	}

	got := receive(t, waited)
	if got.err != nil || got.task == nil || got.task.ID != lostTaskID || got.task.State != batchTaskDone {
		t.Fatalf("expected the found task, done, got %+v and %v", got.task, got.err)
	}
}

// startLostAnswer starts a deploy of dep-1 whose answer is lost, and
// returns a background wait for its context ID.
func startLostAnswer(t *testing.T, base *fakeBatchBase, batch *bosh.BatchDirector) <-chan waitResult {
	t.Helper()

	base.updateErr = lostAnswer(firstDeployment)

	_, err := batch.UpdateDeployment(t.Context(), firstDeployment, "manifest")
	if !errors.Is(err, bosh.ErrOutcomeUnknown) {
		t.Fatalf("expected the lost answer to reach the caller, got %v", err)
	}

	return waitInBackground(func() (*bosh.Task, error) { return batch.WaitForContextID(t.Context(), firstContextID) })
}

// checkLostAnswerHeld advances the clock through the lost-answer checks for
// the duration given, and fails the test if the slot is released.
func checkLostAnswerHeld(t *testing.T, batch *bosh.BatchDirector, clock *fakeBatchClock, duration time.Duration) {
	t.Helper()

	for elapsed := time.Duration(0); elapsed < duration; elapsed += bosh.BatchLostAnswerInterval {
		clock.waitSleeping(t, bosh.BatchLostAnswerInterval)
		waitBatchStats(t, batch, 1, 0)
		clock.advance(bosh.BatchLostAnswerInterval)
	}
}

// expectGivenUp checks that the watcher of a lost answer gave up, released
// the slot, and reported the empty answer.
func expectGivenUp(t *testing.T, batch *bosh.BatchDirector, waited <-chan waitResult) {
	t.Helper()

	waitBatchStats(t, batch, 0, 0)

	got := receive(t, waited)
	if !errors.Is(got.err, bosh.ErrNoTaskWithContextID) || got.task != nil {
		t.Fatalf("expected ErrNoTaskWithContextID and no task, got %+v and %v", got.task, got.err)
	}
}

// The watcher of a lost answer gives up at the first check made at or
// after the grace period's end, when that check's lookup succeeded, found
// no task, and found no deploy running on the deployment.
func TestBatchLostAnswerIsGivenUpOnlyAfterAnEmptyLookupPastTheGrace(t *testing.T) {
	t.Parallel()

	base := newFakeBatchBase()
	batch, clock := newTestBatch(t, base, 1)
	waited := startLostAnswer(t, base, batch)

	checkLostAnswerHeld(t, batch, clock, bosh.UnknownOutcomeGrace)
	expectGivenUp(t, batch, waited)
}

// A lookup that fails never lets the watcher give up, however long after
// the grace period it runs.
func TestBatchLostAnswerKeepsTheSlotWhileLookupsFailPastTheGrace(t *testing.T) {
	t.Parallel()

	base := newFakeBatchBase()
	base.set(func(f *fakeBatchBase) { f.contextErr = errBatchTestRejected })
	batch, clock := newTestBatch(t, base, 1)
	waited := startLostAnswer(t, base, batch)

	checkLostAnswerHeld(t, batch, clock, bosh.UnknownOutcomeGrace+time.Minute)

	base.set(func(f *fakeBatchBase) { f.contextErr, f.runningErr = nil, errBatchTestRejected })
	checkLostAnswerHeld(t, batch, clock, bosh.BatchLostAnswerInterval)

	base.set(func(f *fakeBatchBase) { f.runningErr = nil })
	expectGivenUp(t, batch, waited)
}

// A deploy running on the deployment is followed for a lost answer, even
// when no task has its context ID, and holds the slot past the grace.
func TestBatchLostAnswerFollowsARunningCreateTask(t *testing.T) {
	t.Parallel()

	const runningTaskID = 600

	base := newFakeBatchBase()
	batch, clock := newTestBatch(t, base, 1)
	waited := startLostAnswer(t, base, batch)

	clock.waitSleeping(t, bosh.BatchLostAnswerInterval)

	base.addTask(runningTaskID, firstDeployment, createDeploymentDescription)
	base.set(func(f *fakeBatchBase) {
		f.running = &bosh.Task{ID: runningTaskID, State: batchTaskProcessing, Description: createDeploymentDescription, Deployment: firstDeployment}
	})
	clock.advance(bosh.BatchLostAnswerInterval)

	waitUntil(t, "the watcher follows the running deploy", func() bool { return base.waitCallsFor(runningTaskID) == 1 })
	clock.advance(bosh.UnknownOutcomeGrace + time.Hour)
	waitBatchStats(t, batch, 1, 0)

	base.finish(t, runningTaskID, batchTaskDone)
	waitBatchStats(t, batch, 0, 0)

	got := receive(t, waited)
	if got.err != nil || got.task == nil || got.task.ID != runningTaskID {
		t.Fatalf("expected the running deploy's task, got %+v and %v", got.task, got.err)
	}
}

// A dry run, a task that is not a deploy, or a deploy of another
// deployment is not followed for a lost answer, and does not keep the
// watcher from giving up.
func TestBatchLostAnswerDoesNotFollowOtherRunningTasks(t *testing.T) {
	t.Parallel()

	others := map[string]bosh.Task{
		"a dry run":                    {ID: 700, Description: "create deployment (dry run)", Deployment: firstDeployment},
		"an errand":                    {ID: 700, Description: "run errand smoke-tests", Deployment: firstDeployment},
		"a deploy of another instance": {ID: 700, Description: createDeploymentDescription, Deployment: "dep-9"},
	}

	for name, other := range others {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			base := newFakeBatchBase()
			base.addTask(other.ID, other.Deployment, other.Description)
			base.set(func(f *fakeBatchBase) {
				running := other
				running.State = batchTaskProcessing
				f.running = &running
			})
			batch, clock := newTestBatch(t, base, 1)
			waited := startLostAnswer(t, base, batch)

			checkLostAnswerHeld(t, batch, clock, bosh.UnknownOutcomeGrace)
			expectGivenUp(t, batch, waited)

			if calls := base.waitCallsFor(other.ID); calls != 0 {
				t.Fatalf("expected task %d not to be followed, but it was read %d times", other.ID, calls)
			}
		})
	}
}

func TestAdoptOverTheLimitDelaysTheNextDeploy(t *testing.T) {
	t.Parallel()

	const firstAdopted, secondAdopted = 801, 802

	base := newFakeBatchBase()
	base.addTask(firstAdopted, firstDeployment, createDeploymentDescription)
	base.addTask(secondAdopted, secondDeployment, createDeploymentDescription)
	batch, _ := newTestBatch(t, base, 1)

	batch.Adopt(firstAdopted)
	batch.Adopt(secondAdopted)
	batch.Adopt(firstAdopted)
	waitBatchStats(t, batch, 2, 0)

	next := deployInBackground(t.Context(), batch, thirdDeployment)
	waitBatchStats(t, batch, 2, 1)

	base.finish(t, firstAdopted, batchTaskDone)
	waitBatchStats(t, batch, 1, 1)

	if got := base.acceptedCount(); got != 0 {
		t.Fatalf("expected the next deploy to wait while an adopted deploy fills the batch, but %d were accepted", got)
	}

	base.finish(t, secondAdopted, batchTaskError)

	if name := base.waitAccepted(t); name != thirdDeployment {
		t.Fatalf("expected dep-3 to be accepted, got %s", name)
	}

	err := <-next
	if err != nil {
		t.Fatalf("expected the next deploy to be accepted, got %v", err)
	}
}

// startDeploy runs one deploy that the director accepts, and returns its
// task ID.
func startDeploy(t *testing.T, base *fakeBatchBase, batch *bosh.BatchDirector, name string) int {
	t.Helper()

	task, err := batch.UpdateDeployment(t.Context(), name, "manifest")
	if err != nil || task == nil {
		t.Fatalf("expected the deploy of %s to be accepted, got %+v and %v", name, task, err)
	}

	base.waitAccepted(t)

	return task.ID
}

func TestWatcherReleasesTheSlotWhenTheTaskIs404(t *testing.T) {
	t.Parallel()

	base := newFakeBatchBase()
	base.set(func(f *fakeBatchBase) {
		f.waitErr = fmt.Errorf("read BOSH task: %w", &boshapi.APIError{
			StatusCode: 404, Method: "GET", Path: "/tasks/101", Code: boshapi.ErrorCodeTaskNotFound, Description: "Task 101 not found",
		})
	})
	batch, _ := newTestBatch(t, base, 1)

	taskID := startDeploy(t, base, batch, firstDeployment)
	waitBatchStats(t, batch, 0, 0)

	_, err := batch.WaitForTask(t.Context(), taskID)
	if !errors.Is(err, bosh.ErrWatchAbandoned) {
		t.Fatalf("expected ErrWatchAbandoned, got %v", err)
	}
}

// A 404 for a route the director does not serve is a routing problem, not
// a missing task, so the watcher keeps the slot and reads the task again.
func TestWatcherKeepsTheSlotWhenTheTaskRouteIs404(t *testing.T) {
	t.Parallel()

	base := newFakeBatchBase()
	base.set(func(f *fakeBatchBase) {
		f.waitErr = &boshapi.APIError{StatusCode: 404, Method: "GET", Path: "/tasks/101", Description: "Endpoint '/tasks/101' not found."}
	})
	batch, clock := newTestBatch(t, base, 1)

	startDeploy(t, base, batch, firstDeployment)
	waitUntil(t, "the watcher waits to read the task again", func() bool { return clock.retrySleep() > 0 })
	waitBatchStats(t, batch, 1, 0)
}

func TestWatcherReleasesTheSlotAfterTheMaximumWatch(t *testing.T) {
	t.Parallel()

	const watchMax = time.Hour

	base := newFakeBatchBase()
	base.set(func(f *fakeBatchBase) { f.waitErr = errBatchTestRejected })
	batch, clock := newTestBatch(t, base, 1)
	bosh.SetBatchWatchMax(batch, watchMax)

	start := clock.Now()

	taskID := startDeploy(t, base, batch, firstDeployment)

	for {
		waitUntil(t, "the watcher waits to read again or gives up", func() bool {
			active, _, _ := batch.GetBatchPoolStats()

			return clock.retrySleep() > 0 || active == 0
		})

		backoff := clock.retrySleep()
		if backoff == 0 {
			break
		}

		if clock.Now().Sub(start) >= watchMax {
			t.Fatalf("expected the watcher to give up once %v had passed, but it still waits at %v", watchMax, clock.Now().Sub(start))
		}

		waitBatchStats(t, batch, 1, 0)
		clock.advance(backoff)
	}

	if elapsed := clock.Now().Sub(start); elapsed < watchMax {
		t.Fatalf("expected the watcher to keep the slot for %v, but it gave up after %v", watchMax, elapsed)
	}

	_, err := batch.WaitForTask(t.Context(), taskID)
	if !errors.Is(err, bosh.ErrWatchAbandoned) || !errors.Is(err, errBatchTestRejected) {
		t.Fatalf("expected ErrWatchAbandoned with the read's error, got %v", err)
	}
}

func TestStopEndsWatchersWithoutAFinalTask(t *testing.T) {
	t.Parallel()

	base := newFakeBatchBase()
	batch, clock := newTestBatch(t, base, 3)

	following := startDeploy(t, base, batch, firstDeployment)
	finished := startDeploy(t, base, batch, secondDeployment)
	base.finish(t, finished, batchTaskDone)
	clock.waitSleeping(t, bosh.BatchDonePrune)

	waitedForContextID := startLostAnswer(t, base, batch)
	clock.waitSleeping(t, bosh.BatchLostAnswerInterval)

	waitedForTask := waitInBackground(func() (*bosh.Task, error) { return batch.WaitForTask(t.Context(), following) })

	if running := bosh.BatchWatchersRunning(batch); running != 3 {
		t.Fatalf("expected 3 watchers before Stop, got %d", running)
	}

	batch.Stop()

	if running := bosh.BatchWatchersRunning(batch); running != 0 {
		t.Fatalf("expected no watcher to outlive Stop, got %d", running)
	}

	waitBatchStats(t, batch, 0, 0)

	for name, waited := range map[string]<-chan waitResult{"the task": waitedForTask, "the context ID": waitedForContextID} {
		got := receive(t, waited)
		if !errors.Is(got.err, context.Canceled) || got.task != nil {
			t.Fatalf("expected the wait for %s to end with context.Canceled and no task, got %+v and %v", name, got.task, got.err)
		}
	}

	_, err := batch.UpdateDeployment(t.Context(), "dep-4", "manifest")
	if !errors.Is(err, bosh.ErrBatchDirectorStopped) {
		t.Fatalf("expected a deploy after Stop to get ErrBatchDirectorStopped, got %v", err)
	}

	batch.Adopt(following)

	if active, _, _ := batch.GetBatchPoolStats(); active != 0 || bosh.BatchWatchersRunning(batch) != 0 {
		t.Fatalf("expected nothing adopted after Stop, got %d active and %d watchers", active, bosh.BatchWatchersRunning(batch))
	}
}

func TestDoneMapIsPruned(t *testing.T) {
	t.Parallel()

	base := newFakeBatchBase()
	batch, clock := newTestBatch(t, base, 1)

	taskID := startDeploy(t, base, batch, firstDeployment)
	base.finish(t, taskID, batchTaskDone)

	task, err := batch.WaitForTask(t.Context(), taskID)
	if err != nil || task == nil || task.State != batchTaskDone {
		t.Fatalf("expected the watcher's final task, got %+v and %v", task, err)
	}

	clock.waitSleeping(t, bosh.BatchDonePrune)
	clock.advance(bosh.BatchDonePrune - time.Second)

	if tasks, _ := bosh.BatchWatchesHeld(batch); tasks != 1 {
		t.Fatalf("expected the final task to be kept until it is pruned, got %d watches", tasks)
	}

	clock.advance(time.Second)
	waitUntil(t, "the watcher is pruned", func() bool {
		tasks, contextIDs := bosh.BatchWatchesHeld(batch)

		return tasks == 0 && contextIDs == 0 && bosh.BatchWatchersRunning(batch) == 0
	})

	_, err = batch.WaitForTask(t.Context(), taskID)
	if err != nil {
		t.Fatalf("expected a wait after pruning to pass through, got %v", err)
	}

	if calls := base.waitCallsFor(taskID); calls != 2 {
		t.Fatalf("expected the watcher's read and the passed-through read, got %d reads", calls)
	}
}

func TestBatchWaitReturnsTheWatchersFinalTask(t *testing.T) {
	t.Parallel()

	base := newFakeBatchBase()
	batch, _ := newTestBatch(t, base, 2)

	taskID := startDeploy(t, base, batch, firstDeployment)
	waited := waitInBackground(func() (*bosh.Task, error) { return batch.WaitForTask(t.Context(), taskID) })

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := batch.WaitForTask(ctx, taskID)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected a wait to end with its caller's context, got %v", err)
	}

	waitBatchStats(t, batch, 1, 0)
	base.finish(t, taskID, batchTaskError)

	got := receive(t, waited)
	if got.err != nil || got.task == nil || got.task.ID != taskID || got.task.State != batchTaskError {
		t.Fatalf("expected the watcher's final task in state error, got %+v and %v", got.task, got.err)
	}

	if calls := base.waitCallsFor(taskID); calls != 1 {
		t.Fatalf("expected only the watcher to read the task, got %d reads", calls)
	}
}

func TestBatchSlotIsReleasedWhenTheBaseDirectorPanics(t *testing.T) {
	t.Parallel()

	base := newFakeBatchBase()
	base.updateErr = func(name string) error {
		if name == firstDeployment {
			panic("the base director panicked")
		}

		return nil
	}
	batch, _ := newTestBatch(t, base, 1)

	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected the base director's panic to reach the caller")
			}
		}()

		_, _ = batch.UpdateDeployment(t.Context(), firstDeployment, "manifest")
	}()

	waitBatchStats(t, batch, 0, 0)
	startDeploy(t, base, batch, secondDeployment)
}

// A deploy the director accepted, but named no task for, has nothing a
// watcher could follow, so its slot is released at once.
func TestBatchSlotIsReleasedWhenTheDirectorNamesNoTask(t *testing.T) {
	t.Parallel()

	base := newFakeBatchBase()
	base.noTask = true
	batch, _ := newTestBatch(t, base, 1)

	task, err := batch.UpdateDeployment(t.Context(), firstDeployment, "manifest")
	if err != nil || task != nil {
		t.Fatalf("expected no task and no error, got %+v and %v", task, err)
	}

	waitBatchStats(t, batch, 0, 0)

	if running := bosh.BatchWatchersRunning(batch); running != 0 {
		t.Fatalf("expected no watcher, got %d", running)
	}
}

func TestBatchDeployReturnsTheTaskThatCameWithAnError(t *testing.T) {
	t.Parallel()

	base := newFakeBatchBase()
	base.taskWithErr = &bosh.Task{ID: 900, State: batchTaskError}
	base.updateErr = func(string) error {
		return &boshapi.APIError{StatusCode: 400, Method: "POST", Path: deploymentsRoute, Code: 440001, Mutation: true}
	}
	batch, _ := newTestBatch(t, base, 1)

	task, err := batch.UpdateDeployment(t.Context(), firstDeployment, "manifest")
	if err == nil || task == nil || task.ID != 900 {
		t.Fatalf("expected task 900 with the director's error, got %+v and %v", task, err)
	}
}

// holdDeploys keeps every UpdateDeployment call of the base director in
// flight until the test closes the gate it returns, and reports each call
// that starts on the channel it returns.
func holdDeploys(base *fakeBatchBase) (chan<- struct{}, <-chan string) {
	gate := make(chan struct{})
	entered := make(chan string, 8)

	base.hold = func(name string) {
		entered <- name

		<-gate
	}

	return gate, entered
}

// A deploy whose director call returns after Stop finds no watcher to hand
// its slot to, so the slot is released and nothing follows the deploy.
func TestStopWhileADeployIsInFlightReleasesItsSlot(t *testing.T) {
	t.Parallel()

	answers := map[string]func(base *fakeBatchBase){
		"an accepted deploy": func(*fakeBatchBase) {},
		"a lost answer":      func(base *fakeBatchBase) { base.updateErr = lostAnswer(firstDeployment) },
	}

	for name, answer := range answers {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			base := newFakeBatchBase()
			answer(base)
			gate, entered := holdDeploys(base)
			batch, _ := newTestBatch(t, base, 1)

			result := deployInBackground(t.Context(), batch, firstDeployment)

			select {
			case <-entered:
			case <-time.After(batchWaitLimit):
				t.Fatal("the deploy never reached the base director")
			}

			waitBatchStats(t, batch, 1, 0)

			batch.Stop()
			close(gate)

			select {
			case <-result:
			case <-time.After(batchWaitLimit):
				t.Fatal("the deploy never returned")
			}

			waitBatchStats(t, batch, 0, 0)

			tasks, contextIDs := bosh.BatchWatchesHeld(batch)
			if running := bosh.BatchWatchersRunning(batch); running != 0 || tasks != 0 || contextIDs != 0 {
				t.Fatalf("expected no watcher after Stop, got %d running and %d and %d held", running, tasks, contextIDs)
			}
		})
	}
}

// An error that does not show that no deploy task runs keeps the slot, and
// a watcher looks for the task by the context ID the error carries, if
// any, and as a deploy running on the deployment.
func TestBatchSlotIsKeptWhenTheDeployMayHaveStarted(t *testing.T) {
	t.Parallel()

	errs := map[string]struct {
		err       error
		contextID string
	}{
		"a request that was sent": {
			err: &boshapi.TransportError{
				Method: "POST", Path: deploymentsRoute, ContextID: firstContextID, Sent: true, Mutation: true, Attempts: 1, Err: errBatchTestRejected,
			},
			contextID: firstContextID,
		},
		"a gateway's error answer": {
			err: &boshapi.APIError{StatusCode: 502, Method: "POST", Path: deploymentsRoute, Mutation: true},
		},
		"a call whose context ended": {
			err: &bosh.DirectorCallError{Operation: "UpdateDeployment", Director: batchTestAddress, Cause: context.Canceled, Err: errBatchTestRejected},
		},
		"an error of no known kind": {
			err: errBatchTestRejected,
		},
	}

	for name, answer := range errs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			base := newFakeBatchBase()
			base.updateErr = func(string) error { return answer.err }
			batch, clock := newTestBatch(t, base, 1)

			_, err := batch.UpdateDeployment(t.Context(), firstDeployment, "manifest")
			if !errors.Is(err, answer.err) {
				t.Fatalf("expected the deploy's error, got %v", err)
			}

			clock.waitSleeping(t, bosh.BatchLostAnswerInterval)
			waitBatchStats(t, batch, 1, 0)

			_, contextIDs := bosh.BatchWatchesHeld(batch)
			wantContextIDs, wantLookups := 0, 0

			if answer.contextID != "" {
				wantContextIDs, wantLookups = 1, 1
			}

			if contextIDs != wantContextIDs || base.lookups() != wantLookups {
				t.Fatalf("expected %d watches by context ID and %d lookups, got %d and %d", wantContextIDs, wantLookups, contextIDs, base.lookups())
			}
		})
	}
}

// A deploy call whose context ends while the director runs the deploy
// keeps its slot, and the watcher follows the deploy running on the
// deployment until it ends.
func TestBatchSlotIsHeldThroughADeployWhoseCallEnded(t *testing.T) {
	t.Parallel()

	base := newFakeBatchBase()
	base.blockUntilFinished = true
	batch, _ := newTestBatch(t, base, 1)

	ctx, cancel := context.WithCancel(t.Context())
	result := deployInBackground(ctx, batch, firstDeployment)

	base.waitAccepted(t)

	taskID := base.taskOf(t, firstDeployment)
	running := base.taskSnapshot(taskID)
	base.set(func(f *fakeBatchBase) { f.running = running })

	cancel()

	err := <-result
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected the deploy call to end with its context, got %v", err)
	}

	waitUntil(t, "the watcher follows the running deploy", func() bool { return base.waitCallsFor(taskID) == 1 })
	waitBatchStats(t, batch, 1, 0)

	base.finish(t, taskID, batchTaskDone)
	waitBatchStats(t, batch, 0, 0)
}
