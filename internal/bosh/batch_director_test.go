package bosh_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"blacksmith/internal/bosh"

	"github.com/fivetwenty-io/boshapi/pkg/boshapi"
)

// batchWaitLimit bounds how long a test waits for something the batch
// director does on another goroutine. A test that passes never waits this
// long.
const batchWaitLimit = 5 * time.Second

// The description of an errand's task, a refusal's text, and the task
// states the tests use.
const (
	errandDescription   = "run errand smoke-tests"
	notAuthorized       = "Not authorized"
	batchTaskProcessing = "processing"
	batchTaskDone       = "done"
	batchTaskError      = "error"
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
	taskListRoute    = "/tasks"
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
	// getCalls counts the GetTask calls, and getErr, when set, is every
	// GetTask call's answer.
	getCalls int
	getErr   error
	// getBlocks makes GetTask wait for its context to end.
	getBlocks bool
	// noAnswer makes WaitForTask and GetTask answer with no task and no
	// error.
	noAnswer bool
	// interrupt ends a WaitForTask call that waits for its task with the
	// error sent on it.
	interrupt chan error
	// byContextID maps a context ID to the task the director lists for it.
	byContextID map[string]int
	// contextErr, when set, is every FindTaskByContextID call's answer.
	contextErr error
	// contextLookups counts the FindTaskByContextID calls.
	contextLookups int
	// onLookup, when set, runs at the start of each FindTaskByContextID
	// call, outside the fake's lock, with the call's context.
	onLookup func(ctx context.Context)
	// completeAnswer makes a blocking deploy return the task with its
	// deployment and end time, as the director records it.
	completeAnswer bool
	// getState, when set, replaces the state of the task GetTask returns.
	getState string
	// running is the tasks FindRunningTasksForDeployment answers with.
	running []bosh.Task
	// runningOn is the deployment the director lists running as running on,
	// and runningAsked holds the deployment of each
	// FindRunningTasksForDeployment call. A call for any other deployment
	// finds no task.
	runningOn    string
	runningAsked []string
	// runningErr, when set, is every FindRunningTasksForDeployment call's
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
	// zeroTask makes UpdateDeployment accept nothing and return a task
	// whose ID is 0.
	zeroTask bool
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
		interrupt:   make(chan error, 1),
		runningOn:   firstDeployment,
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

	if f.zeroTask {
		return &bosh.Task{State: batchTaskProcessing, Deployment: name}, nil
	}

	f.mu.Lock()
	f.nextID++
	started := &fakeBatchTask{
		task: bosh.Task{ID: f.nextID, State: batchTaskProcessing, Description: bosh.DeployTaskDescription, Deployment: name},
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

	// The bosh-cli adapter's task carries no deployment and no end time.
	finished := f.taskSnapshot(accepted.ID)

	f.mu.Lock()
	complete := f.completeAnswer
	f.mu.Unlock()

	if !complete {
		finished.Deployment, finished.EndedAt = "", nil
	}

	return finished, nil
}

func (f *fakeBatchBase) GetTask(ctx context.Context, taskID int) (*bosh.Task, error) {
	f.mu.Lock()
	f.getCalls++
	getErr, getBlocks, noAnswer, getState := f.getErr, f.getBlocks, f.noAnswer, f.getState
	_, found := f.tasks[taskID]
	f.mu.Unlock()

	if noAnswer {
		return nil, nil //nolint:nilnil // a director that answers with nothing is what the test is about
	}

	if getBlocks {
		<-ctx.Done()

		return nil, fmt.Errorf("get task %d: %w", taskID, ctx.Err())
	}

	if getErr != nil {
		return nil, getErr
	}

	if !found {
		return nil, fmt.Errorf("task %d: %w", taskID, errBatchTestRejected)
	}

	task := f.taskSnapshot(taskID)
	if getState != "" {
		task.State = getState
	}

	return task, nil
}

func (f *fakeBatchBase) WaitForTask(ctx context.Context, taskID int) (*bosh.Task, error) {
	f.mu.Lock()
	f.waitCalls[taskID]++
	started, found := f.tasks[taskID]
	waitErr, noAnswer := f.waitErr, f.noAnswer
	f.mu.Unlock()

	if noAnswer {
		return nil, nil //nolint:nilnil // a director that answers with nothing is what the test is about
	}

	if waitErr != nil {
		return nil, waitErr
	}

	if !found {
		return nil, fmt.Errorf("task %d: %w", taskID, errBatchTestRejected)
	}

	select {
	case <-started.done:
		return f.taskSnapshot(taskID), nil
	case err := <-f.interrupt:
		return nil, err
	case <-ctx.Done():
		return nil, fmt.Errorf("wait for task %d: %w", taskID, ctx.Err())
	}
}

func (f *fakeBatchBase) FindTaskByContextID(ctx context.Context, contextID string) (*bosh.Task, error) {
	f.mu.Lock()
	onLookup := f.onLookup
	f.mu.Unlock()

	if onLookup != nil {
		onLookup(ctx)
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	f.contextLookups++

	if contextID == "" {
		return nil, bosh.ErrEmptyContextID
	}

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

func (f *fakeBatchBase) FindRunningTasksForDeployment(_ context.Context, deployment string) ([]bosh.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.runningAsked = append(f.runningAsked, deployment)

	if f.runningErr != nil {
		return nil, f.runningErr
	}

	if deployment != f.runningOn {
		return nil, nil
	}

	return slices.Clone(f.running), nil
}

// runningAskedFor returns the deployment of each
// FindRunningTasksForDeployment call so far.
func (f *fakeBatchBase) runningAskedFor() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return slices.Clone(f.runningAsked)
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

// gets reports how many times GetTask was called.
func (f *fakeBatchBase) gets() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.getCalls
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

	ended := started.task.StartedAt.Add(time.Minute)
	started.task.State, started.task.EndedAt = state, &ended
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
		if sleeper.duration != bosh.BatchDonePrune && sleeper.duration != bosh.BatchLostAnswerInterval &&
			sleeper.duration != bosh.BatchFollowReportInterval {
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

// recordingLogger keeps every line a batch director logs.
type recordingLogger struct {
	mu    sync.Mutex
	lines []string
	// onLog, when set, runs at the start of each log call.
	onLog func()
}

func (l *recordingLogger) Infof(format string, args ...interface{}) {
	l.recordf("INFO", format, args...)
}
func (l *recordingLogger) Debugf(format string, args ...interface{}) {
	l.recordf("DEBUG", format, args...)
}
func (l *recordingLogger) Errorf(format string, args ...interface{}) {
	l.recordf("ERROR", format, args...)
}

func (l *recordingLogger) recordf(level, format string, args ...interface{}) {
	l.mu.Lock()
	onLog := l.onLog
	l.mu.Unlock()

	if onLog != nil {
		onLog()
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	l.lines = append(l.lines, level+" "+fmt.Sprintf(format, args...))
}

// count reports how many lines start with the level and hold every part
// given.
func (l *recordingLogger) count(level string, parts ...string) int {
	l.mu.Lock()
	defer l.mu.Unlock()

	count := 0

	for _, line := range l.lines {
		if !strings.HasPrefix(line, level+" ") {
			continue
		}

		matched := true

		for _, part := range parts {
			if !strings.Contains(line, part) {
				matched = false

				break
			}
		}

		if matched {
			count++
		}
	}

	return count
}

// newLoggedTestBatch is newTestBatch with a limit of one and a logger that
// records each line.
func newLoggedTestBatch(t *testing.T, base *fakeBatchBase) (*bosh.BatchDirector, *fakeBatchClock, *recordingLogger) {
	t.Helper()

	logger := &recordingLogger{}
	batch := bosh.NewBatchDirector(base, 1, time.Minute, logger)
	clock := newFakeBatchClock()
	bosh.SetBatchClock(batch, clock)
	t.Cleanup(batch.Stop)

	return batch, clock, logger
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

// finishBlockingDeploy runs a deploy through a base director whose
// UpdateDeployment returns once the deploy has finished, finishes it, and
// returns the task the deploy call returned. The slot is free by the time
// the call returns.
func finishBlockingDeploy(t *testing.T, base *fakeBatchBase, batch *bosh.BatchDirector) *bosh.Task {
	t.Helper()

	type deployed struct {
		task *bosh.Task
		err  error
	}

	result := make(chan deployed, 1)

	go func() {
		task, err := batch.UpdateDeployment(t.Context(), firstDeployment, "manifest")
		active, _, _ := batch.GetBatchPoolStats()

		if active != 0 {
			err = fmt.Errorf("%w: %d slots held when the deploy call returned", errBatchTestRejected, active)
		}

		result <- deployed{task: task, err: err}
	}()

	base.waitAccepted(t)
	base.finish(t, base.taskOf(t, firstDeployment), batchTaskDone)

	got := <-result
	if got.err != nil || got.task == nil {
		t.Fatalf("expected the finished task with the slot free, got %+v and %v", got.task, got.err)
	}

	return got.task
}

// The bosh-cli adapter's UpdateDeployment returns once the deploy has
// finished, so the slot is released before the call returns, with no read
// of the task. Waiters get the director's record of the task, read after
// the slot was released.
func TestBatchSlotIsReleasedWhenABlockingDeployReturnsAFinishedTask(t *testing.T) {
	t.Parallel()

	base := newFakeBatchBase()
	base.blockUntilFinished = true
	batch, _ := newTestBatch(t, base, 1)

	returned := finishBlockingDeploy(t, base, batch)

	task, err := batch.WaitForTask(t.Context(), returned.ID)
	if err != nil || task == nil || task.Deployment != firstDeployment || task.EndedAt == nil || task.State != batchTaskDone {
		t.Fatalf("expected the director's record of the finished task, got %+v and %v", task, err)
	}

	if calls, gets := base.waitCallsFor(returned.ID), base.gets(); calls != 0 || gets != 1 {
		t.Fatalf("expected one read of the finished task and no wait, got %d reads and %d waits", gets, calls)
	}
}

// A finished deploy's slot does not wait for a director that cannot be
// read, and its waiters get the task the deploy call returned.
func TestBatchSlotOfAFinishedDeployIsReleasedWhenTheDirectorCannotBeRead(t *testing.T) {
	t.Parallel()

	failures := map[string]struct {
		setup func(f *fakeBatchBase)
		stop  bool
	}{
		"a failed read":         {setup: func(f *fakeBatchBase) { f.getErr, f.waitErr = errBatchTestRejected, errBatchTestRejected }},
		"a read that Stop ends": {setup: func(f *fakeBatchBase) { f.getBlocks = true }, stop: true},
		"a read that shows the task running": {
			setup: func(f *fakeBatchBase) { f.getState, f.waitErr = batchTaskProcessing, errBatchTestRejected },
		},
	}

	for name, failure := range failures {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			base := newFakeBatchBase()
			base.blockUntilFinished = true
			base.set(failure.setup)
			batch, _ := newTestBatch(t, base, 1)

			returned := finishBlockingDeploy(t, base, batch)
			waited := waitInBackground(func() (*bosh.Task, error) { return batch.WaitForTask(t.Context(), returned.ID) })

			base.set(func(f *fakeBatchBase) { f.blockUntilFinished = false })
			startDeploy(t, base, batch, secondDeployment)

			if failure.stop {
				waitUntil(t, "the finished task is read", func() bool { return base.gets() == 1 })
				batch.Stop()
			}

			got := receive(t, waited)
			if got.err != nil || got.task == nil || got.task.ID != returned.ID || got.task.State != batchTaskDone {
				t.Fatalf("expected the task the deploy call returned, got %+v and %v", got.task, got.err)
			}
		})
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

	base.addTask(lostTaskID, firstDeployment, bosh.DeployTaskDescription)
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

	base.addTask(runningTaskID, firstDeployment, bosh.DeployTaskDescription)
	base.set(func(f *fakeBatchBase) {
		f.running = []bosh.Task{{ID: runningTaskID, State: batchTaskProcessing, Description: bosh.DeployTaskDescription, Deployment: firstDeployment}}
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

	asked := base.runningAskedFor()
	if len(asked) == 0 {
		t.Fatal("expected the watcher to list the running tasks of the deployment")
	}

	for _, deployment := range asked {
		if deployment != firstDeployment {
			t.Fatalf("expected every listing to name %s, got %q", firstDeployment, asked)
		}
	}
}

// A dry run, a task that is not a deploy, or a deploy of another
// deployment is not followed for a lost answer, and does not keep the
// watcher from giving up.
func TestBatchLostAnswerDoesNotFollowOtherRunningTasks(t *testing.T) {
	t.Parallel()

	others := map[string]bosh.Task{
		"a dry run":                    {ID: 700, Description: "create deployment (dry run)", Deployment: firstDeployment},
		"an errand":                    {ID: 700, Description: errandDescription, Deployment: firstDeployment},
		"a deploy of another instance": {ID: 700, Description: bosh.DeployTaskDescription, Deployment: "dep-9"},
	}

	for name, other := range others {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			base := newFakeBatchBase()
			base.addTask(other.ID, other.Deployment, other.Description)
			base.set(func(f *fakeBatchBase) {
				running := other
				running.State = batchTaskProcessing
				f.running = []bosh.Task{running}
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
	base.addTask(firstAdopted, firstDeployment, bosh.DeployTaskDescription)
	base.addTask(secondAdopted, secondDeployment, bosh.DeployTaskDescription)
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

// Only the director's task-not-found answer ends a watch at once. A 404
// for a route the director does not serve is a routing problem, and any
// other not-found answer, such as one a proxy gives, does not say that
// the task is gone, so the watcher keeps the slot and reads the task
// again.
func TestWatcherKeepsTheSlotWhenTheTaskRouteIs404(t *testing.T) {
	t.Parallel()

	answers := map[string]error{
		"a route 404": &boshapi.APIError{StatusCode: 404, Method: "GET", Path: "/tasks/101", Description: "Endpoint '/tasks/101' not found."},
		"a director-not-found answer": &boshapi.APIError{
			StatusCode: 404, Method: "GET", Path: "/tasks/101", Code: boshapi.ErrorCodeDirectorNotFound, Description: "Director not found",
		},
		"a deployment-not-found answer": &boshapi.APIError{
			StatusCode: 404, Method: "GET", Path: "/tasks/101", Code: boshapi.ErrorCodeDeploymentNotFound, Description: "Deployment not found",
		},
		"a not-found error with no answer": fmt.Errorf("read BOSH task: %w", boshapi.ErrNotFound),
	}

	for name, answer := range answers {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			base := newFakeBatchBase()
			base.set(func(f *fakeBatchBase) { f.waitErr, f.getErr = answer, answer })
			batch, clock := newTestBatch(t, base, 1)

			startDeploy(t, base, batch, firstDeployment)
			waitUntil(t, "the watcher waits to read the task again", func() bool { return clock.retrySleep() > 0 })
			waitBatchStats(t, batch, 1, 0)
		})
	}
}

// retryUntil advances the fake clock through the watcher's retries until
// the clock reaches until or the watcher gives up, checks that the slot
// stays held meanwhile, and returns the retry waits it saw.
func retryUntil(t *testing.T, batch *bosh.BatchDirector, clock *fakeBatchClock, until time.Time) []time.Duration {
	t.Helper()

	var waits []time.Duration

	for clock.Now().Before(until) {
		waitUntil(t, "the watcher waits to read again or gives up", func() bool {
			active, _, _ := batch.GetBatchPoolStats()

			return clock.retrySleep() > 0 || active == 0
		})

		backoff := clock.retrySleep()
		if backoff == 0 {
			break
		}

		waitBatchStats(t, batch, 1, 0)

		waits = append(waits, backoff)
		clock.advance(backoff)
	}

	return waits
}

// expectAbandoned checks that the watcher of the task gave up, released
// its slot, and reported ErrWatchAbandoned with the read's error.
func expectAbandoned(t *testing.T, batch *bosh.BatchDirector, taskID int) {
	t.Helper()

	waitBatchStats(t, batch, 0, 0)

	_, err := batch.WaitForTask(t.Context(), taskID)
	if !errors.Is(err, bosh.ErrWatchAbandoned) || !errors.Is(err, errBatchTestRejected) {
		t.Fatalf("expected ErrWatchAbandoned with the read's error, got %v", err)
	}
}

// A watcher whose reads all fail waits 5 s, then twice as long each time
// up to 2 minutes, and gives up once the reads have failed for the
// maximum watch.
func TestWatcherReleasesTheSlotAfterTheMaximumWatch(t *testing.T) {
	t.Parallel()

	backoffs := []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second, 40 * time.Second, 80 * time.Second, 2 * time.Minute, 2 * time.Minute}

	var watchMax time.Duration
	for _, backoff := range backoffs {
		watchMax += backoff
	}

	base := newFakeBatchBase()
	base.set(func(f *fakeBatchBase) { f.waitErr, f.getErr = errBatchTestRejected, errBatchTestRejected })
	batch, clock := newTestBatch(t, base, 1)
	bosh.SetBatchWatchMax(batch, watchMax)

	start := clock.Now()
	taskID := startDeploy(t, base, batch, firstDeployment)

	waits := retryUntil(t, batch, clock, start.Add(time.Hour))
	if fmt.Sprint(waits) != fmt.Sprint(backoffs) {
		t.Fatalf("expected the watcher to wait %v between reads, got %v", backoffs, waits)
	}

	if elapsed := clock.Now().Sub(start); elapsed != watchMax {
		t.Fatalf("expected the watcher to give up once the reads had failed for %v, but it gave up after %v", watchMax, elapsed)
	}

	expectAbandoned(t, batch, taskID)
}

// A deploy that runs longer than the maximum watch keeps its slot when a
// read then fails, because the count starts at the first failed read.
func TestWatcherOfALongDeployKeepsTheSlotWhenOneReadFails(t *testing.T) {
	t.Parallel()

	base := newFakeBatchBase()
	batch, clock := newTestBatch(t, base, 1)
	bosh.SetBatchWatchMax(batch, time.Hour)

	taskID := startDeploy(t, base, batch, firstDeployment)
	waitUntil(t, "the watcher waits for the task", func() bool { return base.waitCallsFor(taskID) == 1 })

	clock.advance(2 * time.Hour)

	base.interrupt <- errBatchTestRejected

	waitUntil(t, "the watcher waits to read the task again", func() bool { return clock.retrySleep() > 0 })
	waitBatchStats(t, batch, 1, 0)

	clock.advance(clock.retrySleep())
	waitUntil(t, "the watcher waits for the task again", func() bool { return base.waitCallsFor(taskID) == 2 })

	base.finish(t, taskID, batchTaskDone)
	waitBatchStats(t, batch, 0, 0)
}

// A read that succeeds ends a run of failed reads, so the maximum watch
// counts again from the next failed read.
func TestWatcherCountsTheMaximumWatchFromTheLatestFailedRead(t *testing.T) {
	t.Parallel()

	const watchMax = time.Hour

	base := newFakeBatchBase()
	batch, clock := newTestBatch(t, base, 1)
	bosh.SetBatchWatchMax(batch, watchMax)

	taskID := startDeploy(t, base, batch, firstDeployment)
	waitUntil(t, "the watcher waits for the task", func() bool { return base.waitCallsFor(taskID) == 1 })

	base.set(func(f *fakeBatchBase) { f.getErr = errBatchTestRejected })

	base.interrupt <- errBatchTestRejected

	retryUntil(t, batch, clock, clock.Now().Add(watchMax-10*time.Minute))
	base.set(func(f *fakeBatchBase) { f.getErr = nil })
	clock.advance(clock.retrySleep())
	waitUntil(t, "the watcher waits for the task again", func() bool { return base.waitCallsFor(taskID) == 2 })

	base.set(func(f *fakeBatchBase) { f.getErr = errBatchTestRejected })

	base.interrupt <- errBatchTestRejected

	failedAt := clock.Now()
	retryUntil(t, batch, clock, failedAt.Add(watchMax+time.Hour))

	if elapsed := clock.Now().Sub(failedAt); elapsed < watchMax || elapsed >= watchMax+2*time.Minute {
		t.Fatalf("expected the watcher to give up %v after the latest failed read, but it gave up after %v", watchMax, elapsed)
	}

	expectAbandoned(t, batch, taskID)
}

// A director that answers a read with no task and no error has not been
// read, so the watcher retries it and reports the empty answer when it
// gives up.
func TestWatcherTreatsAnEmptyAnswerAsAFailedRead(t *testing.T) {
	t.Parallel()

	base := newFakeBatchBase()
	base.set(func(f *fakeBatchBase) { f.noAnswer = true })
	batch, clock := newTestBatch(t, base, 1)
	bosh.SetBatchWatchMax(batch, time.Minute)

	taskID := startDeploy(t, base, batch, firstDeployment)
	retryUntil(t, batch, clock, clock.Now().Add(time.Hour))
	waitBatchStats(t, batch, 0, 0)

	_, err := batch.WaitForTask(t.Context(), taskID)
	if !errors.Is(err, bosh.ErrWatchAbandoned) || !errors.Is(err, bosh.ErrEmptyDirectorAnswer) {
		t.Fatalf("expected ErrWatchAbandoned with the empty answer, got %v", err)
	}
}

// The maximum watch of a lost answer counts from the first lookup that
// failed after the latest lookup that succeeded, not from when the answer
// was lost.
func TestBatchLostAnswerIsAbandonedOnceLookupsFailForTheMaximumWatch(t *testing.T) {
	t.Parallel()

	const watchMax = time.Hour

	base := newFakeBatchBase()
	base.set(func(f *fakeBatchBase) { f.contextErr = errBatchTestRejected })
	batch, clock := newTestBatch(t, base, 1)
	bosh.SetBatchWatchMax(batch, watchMax)

	waited := startLostAnswer(t, base, batch)

	checkLostAnswerHeld(t, batch, clock, 10*time.Minute)

	for _, lookupErr := range []error{nil, errBatchTestRejected} {
		clock.waitSleeping(t, bosh.BatchLostAnswerInterval)
		base.set(func(f *fakeBatchBase) { f.contextErr = lookupErr })
		clock.advance(bosh.BatchLostAnswerInterval)
	}

	checkLostAnswerHeld(t, batch, clock, watchMax)
	waitBatchStats(t, batch, 0, 0)

	got := receive(t, waited)
	if !errors.Is(got.err, bosh.ErrWatchAbandoned) || !errors.Is(got.err, errBatchTestRejected) || got.task != nil {
		t.Fatalf("expected ErrWatchAbandoned with the lookup's error, got %+v and %v", got.task, got.err)
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

	answers := map[string]func(f *fakeBatchBase){
		"no task":              func(f *fakeBatchBase) { f.noTask = true },
		"a task whose ID is 0": func(f *fakeBatchBase) { f.zeroTask = true },
	}

	for name, answer := range answers {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			base := newFakeBatchBase()
			answer(base)
			batch, _ := newTestBatch(t, base, 1)

			task, err := batch.UpdateDeployment(t.Context(), firstDeployment, "manifest")
			if err != nil || (task != nil && task.ID != 0) {
				t.Fatalf("expected no task named and no error, got %+v and %v", task, err)
			}

			waitBatchStats(t, batch, 0, 0)

			if running := bosh.BatchWatchersRunning(batch); running != 0 {
				t.Fatalf("expected no watcher, got %d", running)
			}
		})
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
		"an error that keeps the slot": func(base *fakeBatchBase) {
			base.updateErr = func(string) error { return errBatchTestRejected }
		},
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
		"a sent request not marked as a change": {
			err: &boshapi.TransportError{
				Method: "POST", Path: deploymentsRoute, ContextID: firstContextID, Sent: true, Attempts: 1, Err: errBatchTestRejected,
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
	base.set(func(f *fakeBatchBase) { f.running = []bosh.Task{*running} })

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

func TestWatcherLogsEveryHourWhileItFollowsATask(t *testing.T) {
	t.Parallel()

	base := newFakeBatchBase()
	batch, clock, logger := newLoggedTestBatch(t, base)

	taskID := startDeploy(t, base, batch, firstDeployment)
	what := fmt.Sprintf("BOSH task %d of deployment %s", taskID, firstDeployment)

	for hours := 1; hours <= 2; hours++ {
		clock.waitSleeping(t, bosh.BatchFollowReportInterval)
		clock.advance(bosh.BatchFollowReportInterval)

		waitUntil(t, fmt.Sprintf("the watcher logs hour %d", hours), func() bool {
			return logger.count("INFO", what, fmt.Sprintf("for %v", time.Duration(hours)*time.Hour)) == 1
		})
	}

	base.finish(t, taskID, batchTaskDone)
	waitBatchStats(t, batch, 0, 0)

	if sleeping := clock.sleeping(bosh.BatchFollowReportInterval); sleeping != 0 {
		t.Fatalf("expected the hourly log to stop with the follow, got %d waiting", sleeping)
	}
}

// A read the director refuses cannot succeed on its own, so the watcher
// gives up after UnknownOutcomeGrace of refused reads, and logs each at
// Error.
func TestWatcherGivesUpOnRefusedReadsAfterTheGrace(t *testing.T) {
	t.Parallel()

	refusals := map[string]error{
		"a 401": &boshapi.APIError{StatusCode: 401, Method: "GET", Path: "/tasks/101", Description: notAuthorized},
		"a 403": &boshapi.APIError{StatusCode: 403, Method: "GET", Path: "/tasks/101", Code: boshapi.ErrorCodeNotAuthorized},
	}

	for name, refusal := range refusals {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			base := newFakeBatchBase()
			base.set(func(f *fakeBatchBase) { f.waitErr, f.getErr = refusal, refusal })
			batch, clock, logger := newLoggedTestBatch(t, base)

			start := clock.Now()
			taskID := startDeploy(t, base, batch, firstDeployment)
			retryUntil(t, batch, clock, start.Add(time.Hour))

			if elapsed := clock.Now().Sub(start); elapsed < bosh.UnknownOutcomeGrace || elapsed >= bosh.UnknownOutcomeGrace+2*time.Minute {
				t.Fatalf("expected the watcher to give up %v after the first refused read, but it gave up after %v", bosh.UnknownOutcomeGrace, elapsed)
			}

			waitBatchStats(t, batch, 0, 0)

			_, err := batch.WaitForTask(t.Context(), taskID)
			if !errors.Is(err, bosh.ErrWatchAbandoned) || !errors.Is(err, refusal) {
				t.Fatalf("expected ErrWatchAbandoned with the refusal, got %v", err)
			}

			if logged := logger.count("ERROR", "refused"); logged < 2 {
				t.Fatalf("expected each refused read to be logged at Error, got %d lines", logged)
			}
		})
	}
}

func TestBatchLostAnswerGivesUpOnRefusedLookupsAfterTheGrace(t *testing.T) {
	t.Parallel()

	refusal := &boshapi.APIError{StatusCode: 401, Method: "GET", Path: taskListRoute, Description: notAuthorized}

	base := newFakeBatchBase()
	base.set(func(f *fakeBatchBase) { f.contextErr = refusal })
	batch, clock, logger := newLoggedTestBatch(t, base)

	waited := startLostAnswer(t, base, batch)

	checkLostAnswerHeld(t, batch, clock, bosh.UnknownOutcomeGrace)
	waitBatchStats(t, batch, 0, 0)

	got := receive(t, waited)
	if !errors.Is(got.err, bosh.ErrWatchAbandoned) || !errors.Is(got.err, refusal) {
		t.Fatalf("expected ErrWatchAbandoned with the refusal, got %+v and %v", got.task, got.err)
	}

	if logged := logger.count("ERROR", "refused"); logged < 2 {
		t.Fatalf("expected each refused lookup to be logged at Error, got %d lines", logged)
	}
}

// A deploy that runs on the deployment behind a newer task of another kind
// is still found and followed for a lost answer.
func TestBatchLostAnswerFollowsACreateTaskBehindAnotherRunningTask(t *testing.T) {
	t.Parallel()

	const errandTaskID, createTaskID = 701, 601

	base := newFakeBatchBase()
	base.addTask(errandTaskID, firstDeployment, errandDescription)
	base.addTask(createTaskID, firstDeployment, bosh.DeployTaskDescription)
	base.set(func(f *fakeBatchBase) {
		f.running = []bosh.Task{
			{ID: errandTaskID, State: batchTaskProcessing, Description: errandDescription, Deployment: firstDeployment},
			{ID: createTaskID, State: batchTaskProcessing, Description: bosh.DeployTaskDescription, Deployment: firstDeployment},
		}
	})
	batch, clock := newTestBatch(t, base, 1)
	waited := startLostAnswer(t, base, batch)

	waitUntil(t, "the watcher follows the deploy", func() bool { return base.waitCallsFor(createTaskID) == 1 })
	clock.advance(bosh.UnknownOutcomeGrace + time.Hour)
	waitBatchStats(t, batch, 1, 0)

	base.finish(t, createTaskID, batchTaskDone)
	waitBatchStats(t, batch, 0, 0)

	got := receive(t, waited)
	if got.err != nil || got.task == nil || got.task.ID != createTaskID {
		t.Fatalf("expected the deploy's task, got %+v and %v", got.task, got.err)
	}

	if calls := base.waitCallsFor(errandTaskID); calls != 0 {
		t.Fatalf("expected the errand not to be followed, but it was waited for %d times", calls)
	}
}

// A lost deploy that carries no context ID cannot be looked up by it, so
// a check at or after the grace that finds no deploy running on the
// deployment releases the slot.
func TestBatchLostAnswerWithNoContextIDIsGivenUpWhenNoDeployRuns(t *testing.T) {
	t.Parallel()

	base := newFakeBatchBase()
	base.updateErr = func(string) error { return errBatchTestRejected }
	batch, clock, logger := newLoggedTestBatch(t, base)

	_, err := batch.UpdateDeployment(t.Context(), firstDeployment, "manifest")
	if !errors.Is(err, errBatchTestRejected) {
		t.Fatalf("expected the deploy's error, got %v", err)
	}

	checkLostAnswerHeld(t, batch, clock, bosh.UnknownOutcomeGrace)
	waitBatchStats(t, batch, 0, 0)

	if lookups := base.lookups(); lookups != 0 {
		t.Fatalf("expected no lookup by an empty context ID, got %d", lookups)
	}

	if logged := logger.count("ERROR", "No deploy runs on deployment "+firstDeployment); logged != 1 {
		t.Fatalf("expected one error saying no deploy runs, got %d", logged)
	}

	if running := bosh.BatchWatchersRunning(batch); running != 1 {
		t.Fatalf("expected the watcher to be pruning, got %d watchers", running)
	}
}

// With no watcher for a context ID, a lookup that finds no task does not
// show that no deploy ran, so the wait reports that no watcher looks for
// the context ID rather than that no task has it.
func TestBatchWaitForContextIDWithNoWatcherOrTask(t *testing.T) {
	t.Parallel()

	base := newFakeBatchBase()
	batch, _ := newTestBatch(t, base, 1)

	task, err := batch.WaitForContextID(t.Context(), firstContextID)
	if !errors.Is(err, bosh.ErrNoWatcherForContextID) || task != nil {
		t.Fatalf("expected ErrNoWatcherForContextID and no task, got %+v and %v", task, err)
	}

	if errors.Is(err, bosh.ErrNoTaskWithContextID) {
		t.Fatalf("expected no claim that no task has the context ID, got %v", err)
	}

	if lookups := base.lookups(); lookups != 1 {
		t.Fatalf("expected one lookup by the context ID, got %d", lookups)
	}
}

// With no watcher for a context ID, a task the lookup finds is waited for
// on the base director when no watcher follows it either.
func TestBatchWaitForContextIDWithNoWatcherWaitsForTheFoundTask(t *testing.T) {
	t.Parallel()

	const foundTaskID = 610

	base := newFakeBatchBase()
	base.addTask(foundTaskID, firstDeployment, bosh.DeployTaskDescription)
	base.set(func(f *fakeBatchBase) { f.byContextID[firstContextID] = foundTaskID })
	batch, _ := newTestBatch(t, base, 1)

	waited := waitInBackground(func() (*bosh.Task, error) { return batch.WaitForContextID(t.Context(), firstContextID) })

	waitUntil(t, "the base director waits for the found task", func() bool { return base.waitCallsFor(foundTaskID) == 1 })
	base.finish(t, foundTaskID, batchTaskDone)

	got := receive(t, waited)
	if got.err != nil || got.task == nil || got.task.ID != foundTaskID || got.task.State != batchTaskDone {
		t.Fatalf("expected the found task, done, got %+v and %v", got.task, got.err)
	}
}

// With no watcher for a context ID, a task the lookup finds that a watcher
// already follows is served by that watcher, with no second wait on the
// base director.
func TestBatchWaitForContextIDWithNoWatcherIsServedByTheTaskWatcher(t *testing.T) {
	t.Parallel()

	const foundTaskID = 620

	base := newFakeBatchBase()
	base.addTask(foundTaskID, firstDeployment, bosh.DeployTaskDescription)
	base.set(func(f *fakeBatchBase) { f.byContextID[firstContextID] = foundTaskID })
	batch, _ := newTestBatch(t, base, 1)

	batch.Adopt(foundTaskID)
	waitUntil(t, "the watcher follows the adopted task", func() bool { return base.waitCallsFor(foundTaskID) == 1 })

	waited := waitInBackground(func() (*bosh.Task, error) { return batch.WaitForContextID(t.Context(), firstContextID) })

	waitUntil(t, "the context ID is looked up", func() bool { return base.lookups() == 1 })
	base.finish(t, foundTaskID, batchTaskDone)

	got := receive(t, waited)
	if got.err != nil || got.task == nil || got.task.ID != foundTaskID || got.task.State != batchTaskDone {
		t.Fatalf("expected the found task, done, got %+v and %v", got.task, got.err)
	}

	if calls := base.waitCallsFor(foundTaskID); calls != 1 {
		t.Fatalf("expected only the watcher to wait on the base director, got %d waits", calls)
	}
}

// A wait for the task a lost answer's watcher found, by its task ID, is
// served by that watcher, with no second wait on the base director.
func TestBatchLostAnswerServesAWaitForTheTaskItFound(t *testing.T) {
	t.Parallel()

	const lostTaskID = 630

	base := newFakeBatchBase()
	batch, _ := newTestBatch(t, base, 1)

	base.addTask(lostTaskID, firstDeployment, bosh.DeployTaskDescription)
	base.set(func(f *fakeBatchBase) { f.byContextID[firstContextID] = lostTaskID })
	startLostAnswer(t, base, batch)
	waitUntil(t, "the watcher follows the task it found", func() bool { return base.waitCallsFor(lostTaskID) == 1 })

	waited := waitInBackground(func() (*bosh.Task, error) { return batch.WaitForTask(t.Context(), lostTaskID) })

	base.finish(t, lostTaskID, batchTaskDone)

	got := receive(t, waited)
	if got.err != nil || got.task == nil || got.task.ID != lostTaskID || got.task.State != batchTaskDone {
		t.Fatalf("expected the found task, done, got %+v and %v", got.task, got.err)
	}

	if calls := base.waitCallsFor(lostTaskID); calls != 1 {
		t.Fatalf("expected only the watcher to wait on the base director, got %d waits", calls)
	}
}

// The watcher of a lost answer is pruned by its context ID as well as by
// its task ID, so a later wait for the context ID looks the task up again.
func TestBatchLostAnswerIsPrunedByItsContextID(t *testing.T) {
	t.Parallel()

	const lostTaskID = 640

	base := newFakeBatchBase()
	batch, clock := newTestBatch(t, base, 1)

	base.addTask(lostTaskID, firstDeployment, bosh.DeployTaskDescription)
	base.set(func(f *fakeBatchBase) { f.byContextID[firstContextID] = lostTaskID })
	waited := startLostAnswer(t, base, batch)
	waitUntil(t, "the watcher follows the task it found", func() bool { return base.waitCallsFor(lostTaskID) == 1 })
	base.finish(t, lostTaskID, batchTaskDone)
	receive(t, waited)

	clock.waitSleeping(t, bosh.BatchDonePrune)
	clock.advance(bosh.BatchDonePrune)
	waitUntil(t, "the watcher is pruned", func() bool {
		tasks, contextIDs := bosh.BatchWatchesHeld(batch)

		return tasks == 0 && contextIDs == 0 && bosh.BatchWatchersRunning(batch) == 0
	})

	before := base.lookups()

	task, err := batch.WaitForContextID(t.Context(), firstContextID)
	if err != nil || task == nil || task.ID != lostTaskID {
		t.Fatalf("expected the task found by a fresh lookup, got %+v and %v", task, err)
	}

	if lookups := base.lookups(); lookups != before+1 {
		t.Fatalf("expected one more lookup after the prune, got %d then %d", before, lookups)
	}
}

// The watcher of a lost answer judges the grace by the time its check
// began, so a lookup that ends past the grace does not let a check that
// began inside it give up.
func TestBatchLostAnswerJudgesTheGraceByWhenTheCheckBegan(t *testing.T) {
	t.Parallel()

	base := newFakeBatchBase()
	batch, clock := newTestBatch(t, base, 1)

	var once sync.Once

	base.set(func(f *fakeBatchBase) {
		f.onLookup = func(context.Context) { once.Do(func() { clock.advance(bosh.UnknownOutcomeGrace) }) }
	})

	startLostAnswer(t, base, batch)
	clock.waitSleeping(t, bosh.BatchLostAnswerInterval)
	waitBatchStats(t, batch, 1, 0)
}

// A task ID of zero or less names no task, so Adopt takes no slot and
// starts no watcher for it.
func TestAdoptIgnoresATaskIDThatNamesNoTask(t *testing.T) {
	t.Parallel()

	base := newFakeBatchBase()
	batch, _, logger := newLoggedTestBatch(t, base)

	batch.Adopt(0)
	batch.Adopt(-1)

	if active, queued, _ := batch.GetBatchPoolStats(); active != 0 || queued != 0 {
		t.Fatalf("expected no slot taken, got %d active and %d queued", active, queued)
	}

	if running := bosh.BatchWatchersRunning(batch); running != 0 {
		t.Fatalf("expected no watcher, got %d", running)
	}

	if logged := logger.count("ERROR", "names no task"); logged != 2 {
		t.Fatalf("expected two errors naming no task, got %d", logged)
	}
}

// After Stop, Adopt of a task no watcher follows takes no slot.
func TestAdoptAfterStopTakesNoSlot(t *testing.T) {
	t.Parallel()

	const adopted = 811

	base := newFakeBatchBase()
	base.addTask(adopted, firstDeployment, bosh.DeployTaskDescription)
	batch, _ := newTestBatch(t, base, 1)

	batch.Stop()
	batch.Adopt(adopted)

	if active, queued, _ := batch.GetBatchPoolStats(); active != 0 || queued != 0 {
		t.Fatalf("expected no slot taken after Stop, got %d active and %d queued", active, queued)
	}

	if running := bosh.BatchWatchersRunning(batch); running != 0 {
		t.Fatalf("expected no watcher after Stop, got %d", running)
	}
}

// A watcher that has finished stays in the batch director for a while, so
// a later Adopt of its task follows the task again rather than taking the
// finished watcher for a running one.
func TestAdoptFollowsAgainATaskWhoseWatcherFinished(t *testing.T) {
	t.Parallel()

	const adopted = 821

	base := newFakeBatchBase()
	base.addTask(adopted, firstDeployment, bosh.DeployTaskDescription)
	batch, _, logger := newLoggedTestBatch(t, base)

	batch.Adopt(adopted)
	waitUntil(t, "the watcher follows the adopted task", func() bool { return base.waitCallsFor(adopted) == 1 })
	base.finish(t, adopted, batchTaskDone)
	waitBatchStats(t, batch, 0, 0)

	if tasks, _ := bosh.BatchWatchesHeld(batch); tasks != 1 {
		t.Fatalf("expected the finished watcher to be kept until it is pruned, got %d watches", tasks)
	}

	batch.Adopt(adopted)
	waitUntil(t, "a new watcher follows the task", func() bool { return base.waitCallsFor(adopted) == 2 })
	waitBatchStats(t, batch, 0, 0)

	if logged := logger.count("INFO", fmt.Sprintf("Adopted BOSH task %d", adopted)); logged != 2 {
		t.Fatalf("expected the task to be adopted twice, got %d", logged)
	}
}

// Adopt logs after it lets go of the batch director's lock, so a logger
// that reads the batch's stats does not wait on it.
func TestAdoptLogsOutsideTheBatchLock(t *testing.T) {
	t.Parallel()

	const adopted, missed = 831, 832

	base := newFakeBatchBase()
	base.addTask(adopted, firstDeployment, bosh.DeployTaskDescription)
	batch, _, logger := newLoggedTestBatch(t, base)

	var blocked atomic.Int32

	logger.mu.Lock()
	logger.onLog = func() {
		read := make(chan struct{})

		go func() {
			batch.GetBatchPoolStats()
			close(read)
		}()

		select {
		case <-read:
		case <-time.After(200 * time.Millisecond):
			blocked.Add(1)
		}
	}
	logger.mu.Unlock()

	batch.Adopt(adopted)
	batch.Adopt(adopted)
	batch.Stop()
	batch.Adopt(missed)

	if got := blocked.Load(); got != 0 {
		t.Fatalf("expected no log line written under the batch lock, got %d", got)
	}

	for _, part := range []string{"Adopted BOSH task", "already follows", "is stopped"} {
		if logged := logger.count("INFO", part); logged != 1 {
			t.Fatalf("expected one line saying %q, got %d", part, logged)
		}
	}
}

// Each waiter gets its own copy of the watcher's final task, so one that
// changes it does not change what another waiter gets.
func TestBatchWaitersGetTheirOwnCopyOfTheFinalTask(t *testing.T) {
	t.Parallel()

	base := newFakeBatchBase()
	batch, _ := newTestBatch(t, base, 1)

	taskID := startDeploy(t, base, batch, firstDeployment)
	base.finish(t, taskID, batchTaskDone)

	first, err := batch.WaitForTask(t.Context(), taskID)
	if err != nil || first == nil || first.EndedAt == nil {
		t.Fatalf("expected the watcher's final task with its end time, got %+v and %v", first, err)
	}

	ended := *first.EndedAt
	first.State = batchTaskError
	*first.EndedAt = ended.Add(time.Hour)

	second, err := batch.WaitForTask(t.Context(), taskID)
	if err != nil || second == nil || second.EndedAt == nil {
		t.Fatalf("expected the watcher's final task with its end time, got %+v and %v", second, err)
	}

	if second.State != batchTaskDone || !second.EndedAt.Equal(ended) {
		t.Fatalf("expected the second waiter's task to be untouched, got state %s and end %v", second.State, second.EndedAt)
	}
}

// A deploy call that returns the director's full record of a finished task
// needs no read of it, and its waiters get that record.
func TestBatchFinishedDeployWithAFullRecordIsNotReadAgain(t *testing.T) {
	t.Parallel()

	base := newFakeBatchBase()
	base.blockUntilFinished = true
	base.completeAnswer = true
	batch, _ := newTestBatch(t, base, 1)

	returned := finishBlockingDeploy(t, base, batch)

	task, err := batch.WaitForTask(t.Context(), returned.ID)
	if err != nil || task == nil || task.Deployment != firstDeployment || task.EndedAt == nil || task.State != batchTaskDone {
		t.Fatalf("expected the record the deploy call returned, got %+v and %v", task, err)
	}

	if gets := base.gets(); gets != 0 {
		t.Fatalf("expected no read of a task whose record was complete, got %d", gets)
	}
}

// panicLogger panics on the log line that has the text given.
type panicLogger struct {
	on string
}

func (l panicLogger) Infof(format string, _ ...interface{}) {
	if strings.Contains(format, l.on) {
		panic("the logger failed")
	}
}
func (panicLogger) Debugf(string, ...interface{}) {}
func (panicLogger) Errorf(string, ...interface{}) {}

// A panic in the logger after a watcher took over the slot leaves the slot
// to that watcher, which releases it once.
func TestBatchSlotIsReleasedOnceWhenTheLoggerPanicsAfterTheHandOff(t *testing.T) {
	t.Parallel()

	base := newFakeBatchBase()
	base.updateErr = lostAnswer(firstDeployment)
	batch := bosh.NewBatchDirector(base, 1, time.Minute, panicLogger{on: "stays held"})
	bosh.SetBatchClock(batch, newFakeBatchClock())
	t.Cleanup(batch.Stop)

	func() {
		defer func() {
			if recovered := recover(); recovered == nil {
				t.Error("expected the logger's panic to reach the caller")
			}
		}()

		_, _ = batch.UpdateDeployment(t.Context(), firstDeployment, "manifest")
	}()

	waitBatchStats(t, batch, 1, 0)
	batch.Stop()

	if active, _, _ := batch.GetBatchPoolStats(); active != 0 {
		t.Fatalf("expected the slot to be released once, got %d slots held", active)
	}
}

// deployWithin runs a deploy of the deployment and fails the test if it
// does not return within a few seconds, the way a deploy that waits for a
// slot that never frees would not.
func deployWithin(t *testing.T, batch *bosh.BatchDirector, name string) *bosh.Task {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), batchWaitLimit)
	defer cancel()

	task, err := batch.UpdateDeployment(ctx, name, "manifest")
	if err != nil || task == nil {
		t.Fatalf("expected the deploy of %s to be accepted, got %+v and %v", name, task, err)
	}

	return task
}

// A deploy of a deployment whose earlier deploy lost its answer ends that
// deploy's watcher and takes over its slot, whether or not the lost deploy
// carried a context ID. The old watcher gives back no slot of its own, so
// the deployment holds one slot, and its waiters get ErrLostAnswerSuperseded.
func TestBatchNewDeployEndsTheLostAnswerWatcherOfItsDeployment(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		lose func(f *fakeBatchBase)
	}{
		"with a context ID":    {lose: func(f *fakeBatchBase) { f.updateErr = lostAnswer(firstDeployment) }},
		"without a context ID": {lose: func(f *fakeBatchBase) { f.updateErr = func(string) error { return errBatchTestRejected } }},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			base := newFakeBatchBase()
			tc.lose(base)
			batch, clock := newTestBatch(t, base, 1)

			_, err := batch.UpdateDeployment(t.Context(), firstDeployment, "manifest")
			if err == nil {
				t.Fatal("expected the lost answer to reach the caller")
			}

			waited := waitInBackground(func() (*bosh.Task, error) { return batch.WaitForContextID(t.Context(), firstContextID) })

			clock.waitSleeping(t, bosh.BatchLostAnswerInterval)
			waitBatchStats(t, batch, 1, 0)

			base.set(func(f *fakeBatchBase) { f.updateErr = nil })

			task := deployWithin(t, batch, firstDeployment)
			waitBatchStats(t, batch, 1, 0)

			// The old watcher is gone, so no lapse of the grace frees the new
			// deploy's slot.
			clock.advance(bosh.UnknownOutcomeGrace + time.Hour)
			waitBatchStats(t, batch, 1, 0)

			if name == "with a context ID" {
				got := receive(t, waited)
				if !errors.Is(got.err, bosh.ErrLostAnswerSuperseded) || got.task != nil {
					t.Fatalf("expected ErrLostAnswerSuperseded and no task, got %+v and %v", got.task, got.err)
				}
			}

			base.finish(t, task.ID, batchTaskDone)
			waitBatchStats(t, batch, 0, 0)
		})
	}
}

// The slot passes from the lost deploy's watcher to the new deploy with no
// moment in which it is free, so a deploy queued for another deployment
// does not take it, and the count never goes over the limit.
func TestBatchSupersedingALostAnswerNeverFreesTheSlotToAQueuedDeploy(t *testing.T) {
	t.Parallel()

	base := newFakeBatchBase()
	base.updateErr = lostAnswer(firstDeployment)
	batch, _ := newTestBatch(t, base, 1)

	_, err := batch.UpdateDeployment(t.Context(), firstDeployment, "manifest")
	if err == nil {
		t.Fatal("expected the lost answer to reach the caller")
	}

	queued := deployInBackground(t.Context(), batch, secondDeployment)
	waitBatchStats(t, batch, 1, 1)

	base.set(func(f *fakeBatchBase) { f.updateErr = nil })

	stopSampling := make(chan struct{})
	sampled := make(chan int, 1)

	go func() {
		most := 0

		for {
			select {
			case <-stopSampling:
				sampled <- most

				return
			default:
			}

			active, _, _ := batch.GetBatchPoolStats()
			most = max(most, active)

			time.Sleep(50 * time.Microsecond)
		}
	}()

	task := deployWithin(t, batch, firstDeployment)
	waitBatchStats(t, batch, 1, 1)

	if accepted := base.acceptedCount(); accepted != 1 {
		t.Fatalf("expected only the new deploy of %s to be accepted, got %d accepted", firstDeployment, accepted)
	}

	close(stopSampling)

	if most := <-sampled; most > 1 {
		t.Fatalf("expected at most one slot held, got %d", most)
	}

	base.finish(t, task.ID, batchTaskDone)

	select {
	case err := <-queued:
		if err != nil {
			t.Fatalf("expected the queued deploy to be accepted once the slot frees, got %v", err)
		}
	case <-time.After(batchWaitLimit):
		t.Fatal("the queued deploy never got the slot")
	}
}

// The batch and pooled directors pass the listing of the running tasks of
// a deployment on to the base director, with the deployment and the error.
func TestRunningTasksListingPassesThroughTheBatchAndPooledDirectors(t *testing.T) {
	t.Parallel()

	const (
		batchWrapper  = "batch"
		pooledWrapper = "pooled"
	)

	wrappers := map[string]func(base bosh.Director) bosh.Director{
		batchWrapper:  func(base bosh.Director) bosh.Director { return bosh.NewBatchDirector(base, 1, time.Minute, nil) },
		pooledWrapper: func(base bosh.Director) bosh.Director { return bosh.NewPooledDirector(base, 1, time.Minute, nil) },
	}

	for name, wrap := range wrappers {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			base := newFakeBatchBase()
			base.running = []bosh.Task{{ID: 700, State: batchTaskProcessing, Description: bosh.DeployTaskDescription, Deployment: firstDeployment}}
			wrapped := wrap(base)

			tasks, err := wrapped.FindRunningTasksForDeployment(t.Context(), firstDeployment)
			if err != nil || len(tasks) != 1 || tasks[0].ID != 700 {
				t.Fatalf("expected the running task of %s, got %+v and %v", firstDeployment, tasks, err)
			}

			tasks, err = wrapped.FindRunningTasksForDeployment(t.Context(), secondDeployment)
			if err != nil || len(tasks) != 0 {
				t.Fatalf("expected no task for %s, got %+v and %v", secondDeployment, tasks, err)
			}

			if asked := base.runningAskedFor(); !slices.Equal(asked, []string{firstDeployment, secondDeployment}) {
				t.Fatalf("expected the listings to name the deployments asked about, got %q", asked)
			}

			base.set(func(f *fakeBatchBase) { f.runningErr = errBatchTestRejected })

			_, err = wrapped.FindRunningTasksForDeployment(t.Context(), firstDeployment)
			if !errors.Is(err, errBatchTestRejected) {
				t.Fatalf("expected the base director's error, got %v", err)
			}
		})
	}
}

// A task kept for waiters shares no end time with the task the deploy call
// returned, so a caller that changes the one does not change the other.
func TestBatchFinishedDeployKeepsItsOwnEndTime(t *testing.T) {
	t.Parallel()

	base := newFakeBatchBase()
	base.blockUntilFinished = true
	base.completeAnswer = true
	batch, _ := newTestBatch(t, base, 1)

	returned := finishBlockingDeploy(t, base, batch)
	if returned.EndedAt == nil {
		t.Fatal("expected the deploy call to return an end time")
	}

	ended := *returned.EndedAt
	*returned.EndedAt = ended.Add(-time.Hour)

	task, err := batch.WaitForTask(t.Context(), returned.ID)
	if err != nil || task == nil || task.EndedAt == nil {
		t.Fatalf("expected the kept task with its end time, got %+v and %v", task, err)
	}

	if !task.EndedAt.Equal(ended) {
		t.Fatalf("expected the kept end time %v to be untouched, got %v", ended, *task.EndedAt)
	}
}

// Stop wakes a deploy waiting for a slot even when the slot is held by a
// deploy still in its director call, which no watcher follows yet.
func TestStopWakesAWaiterBehindADeployStillInItsCall(t *testing.T) {
	t.Parallel()

	base := newFakeBatchBase()
	gate, entered := holdDeploys(base)
	batch, _ := newTestBatch(t, base, 1)

	first := deployInBackground(t.Context(), batch, firstDeployment)

	select {
	case <-entered:
	case <-time.After(batchWaitLimit):
		t.Fatal("the deploy never reached the base director")
	}

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

	close(gate)
	<-first
	waitBatchStats(t, batch, 0, 0)
}

// A waiter at the head of the queue that leaves while a slot is free
// wakes the waiter behind it, which takes the slot.
func TestAWaiterThatLeavesTheHeadWakesTheNextWaiter(t *testing.T) {
	t.Parallel()

	base := newFakeBatchBase()
	batch, _ := newTestBatch(t, base, 1)

	ticket := bosh.JoinBatchQueue(batch)
	waiting := queueDeploy(t.Context(), t, batch, firstDeployment)

	bosh.LeaveBatchQueue(batch, ticket)

	if name := base.waitAccepted(t); name != firstDeployment {
		t.Fatalf("expected dep-1 to be accepted once the head left, got %s", name)
	}

	err := <-waiting
	if err != nil {
		t.Fatalf("expected dep-1 to be accepted, got %v", err)
	}
}

// A waiter that takes a slot from the head of the queue wakes the waiter
// behind it, which takes a slot the limit still allows.
func TestAWaiterThatTakesASlotWakesTheNextWaiter(t *testing.T) {
	t.Parallel()

	base := newFakeBatchBase()
	batch, _ := newTestBatch(t, base, 2)

	bosh.JoinBatchQueue(batch)
	waiting := queueDeploy(t.Context(), t, batch, firstDeployment)

	bosh.TakeBatchSlotAtHead(batch)

	if name := base.waitAccepted(t); name != firstDeployment {
		t.Fatalf("expected dep-1 to be accepted once the head took its slot, got %s", name)
	}

	err := <-waiting
	if err != nil {
		t.Fatalf("expected dep-1 to be accepted, got %v", err)
	}

	waitBatchStats(t, batch, 2, 0)
}

// A refused lookup that follows a lookup failing some other way starts a
// new run of refusals, so the shorter limit counts from that refusal.
func TestBatchLostAnswerCountsRefusalsFromTheLatestRun(t *testing.T) {
	t.Parallel()

	refusal := &boshapi.APIError{StatusCode: 401, Method: "GET", Path: taskListRoute, Description: notAuthorized}

	base := newFakeBatchBase()
	base.set(func(f *fakeBatchBase) { f.contextErr = refusal })
	batch, clock := newTestBatch(t, base, 1)

	startLostAnswer(t, base, batch)
	checkLostAnswerHeld(t, batch, clock, 10*time.Minute)

	clock.waitSleeping(t, bosh.BatchLostAnswerInterval)
	base.set(func(f *fakeBatchBase) { f.contextErr = errBatchTestRejected })
	clock.advance(bosh.BatchLostAnswerInterval)
	clock.waitSleeping(t, bosh.BatchLostAnswerInterval)
	base.set(func(f *fakeBatchBase) { f.contextErr = refusal })
	clock.advance(bosh.BatchLostAnswerInterval)

	checkLostAnswerHeld(t, batch, clock, bosh.UnknownOutcomeGrace-time.Minute)
	checkLostAnswerHeld(t, batch, clock, time.Minute)
	waitBatchStats(t, batch, 0, 0)
}

// A lookup that Stop ends after the grace ends the watcher as stopped,
// not as a lost answer that no task has the context ID of.
func TestBatchLostAnswerStoppedDuringALookupReportsTheStop(t *testing.T) {
	t.Parallel()

	base := newFakeBatchBase()
	batch, clock, logger := newLoggedTestBatch(t, base)
	graceEnds := clock.Now().Add(bosh.UnknownOutcomeGrace)

	var once sync.Once

	base.set(func(f *fakeBatchBase) {
		f.onLookup = func(ctx context.Context) {
			if clock.Now().Before(graceEnds) {
				return
			}

			once.Do(func() {
				go batch.Stop()

				<-ctx.Done()
			})
		}
	})

	startLostAnswer(t, base, batch)
	checkLostAnswerHeld(t, batch, clock, bosh.UnknownOutcomeGrace)
	waitUntil(t, "the watcher ends", func() bool { return bosh.BatchWatchersRunning(batch) == 0 })
	waitBatchStats(t, batch, 0, 0)

	if logged := logger.count("ERROR", "No task has the context ID"); logged != 0 {
		t.Fatalf("expected the watcher to end as stopped, but it gave up on the lost answer %d times", logged)
	}
}
