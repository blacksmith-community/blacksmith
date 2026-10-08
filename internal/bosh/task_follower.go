package bosh

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/fivetwenty-io/boshapi/pkg/boshapi"
)

// kindDeploy is the kind of a deploy's follow, and the noun for a running
// deploy task.
const kindDeploy = "deploy"

// Errors a follower reports to the waiters of its watches.
var (
	// ErrWatchAbandoned is reported to the waiters of a watcher that gave
	// up on its task and released its slot, because the director no longer
	// knew the task, could not be read for followWatchMax, or refused reads
	// for UnknownOutcomeGrace, because no matching task ran on the
	// deployment of a lost answer that carried no context ID, because the
	// director can't be asked for the task of a lost answer, or because the
	// deployment of a lost answer never went idle.
	ErrWatchAbandoned = errors.New("Blacksmith stopped following the BOSH task and released its slot") //nolint:staticcheck // Blacksmith is a proper noun
	// ErrNoTaskWithContextID is reported to the waiters of a watcher whose
	// deploy lost its answer, when the director showed that no task has its
	// context ID.
	ErrNoTaskWithContextID = errors.New("the BOSH director lists no task with the context ID")
	// ErrLostAnswerSuperseded is reported to the waiters of a watcher of a
	// lost answer that a later deploy of the same deployment ended. The
	// director takes a lock for each deployment, so once it accepted the
	// later deploy, the lost one is not running.
	ErrLostAnswerSuperseded = errors.New("a later deploy of the same deployment superseded the deploy whose answer was lost")
)

const (
	// followWatchMax is how long a watcher keeps reading a task, or looking
	// for the task of a lost answer, while every read fails. It counts from
	// the first failed read of a run of failures, and a read that succeeds
	// starts the count again, so a task that runs longer is still followed.
	// It also caps how long a lost answer is held while its lookup finds
	// the deployment busy, counted from when the answer was lost.
	followWatchMax = 6 * time.Hour
	// followLostAnswerInterval is how often a watcher looks for the task of
	// a call whose answer was lost.
	followLostAnswerInterval = 30 * time.Second
	// followWatchBackoffMin and followWatchBackoffMax bound the wait before
	// a watcher reads its task again after a read failed.
	followWatchBackoffMin = 5 * time.Second
	followWatchBackoffMax = 2 * time.Minute
	// followWatchBackoffFactor is how much longer each wait is than the
	// one before.
	followWatchBackoffFactor = 2
	// followDonePrune is how long a watcher keeps the final task of its
	// call for waiters after the task ends.
	followDonePrune = 10 * time.Minute
	// followReportInterval is how often a watcher logs that it still
	// follows a task that holds a slot.
	followReportInterval = time.Hour
	// followFinishedReadTimeout bounds the read of a task that had already
	// ended when the call returned it.
	followFinishedReadTimeout = 30 * time.Second
)

// followClock is the time source of a follower's watchers.
type followClock interface {
	Now() time.Time
	// Sleep waits for the duration given, and returns the context's error
	// when the context ends first.
	Sleep(ctx context.Context, duration time.Duration) error
}

// systemFollowClock reads the system clock.
type systemFollowClock struct{}

func (systemFollowClock) Now() time.Time {
	return time.Now()
}

func (systemFollowClock) Sleep(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()

	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("sleep ended early: %w", ctx.Err())
	}
}

// follow describes one call whose task a follower follows.
type follow struct {
	// kind names the call in logs and errors, such as "deploy".
	kind string
	// deployment is the deployment the call is for. It is empty for
	// calls that name none and for adopted tasks.
	deployment string
	// supersedable lets a later call on the deployment end the watch of a
	// lost answer and take over its slot.
	supersedable bool
	// lookup is how the watch of a lost answer looks for the call's task.
	lookup lostLookup
}

// subject names what a call's slot belongs to, for logs.
func (c follow) subject() string {
	if c.deployment == "" {
		return "the " + c.kind
	}

	return "deployment " + c.deployment
}

// lostLookup is how the watch of a call whose answer was lost looks for
// the call's task.
type lostLookup interface {
	// find returns the call's task ID when it found the task. busy means a
	// task that may be the call's runs on the deployment, so this check
	// can't end the grace. 0, false, and a nil error mean every check it
	// made succeeded and found nothing.
	find(ctx context.Context, f *taskFollower, w *followWatch) (taskID int, busy bool, err error)
	// giveUp logs the Error line and returns the error the watch's waiters
	// get, once a check that succeeded at or after the grace found
	// nothing, or once a busy deployment outlasted the watch limit.
	giveUp(f *taskFollower, w *followWatch) error
}

// runningMatch picks the call's task out of the tasks running on its
// deployment.
type runningMatch struct {
	// noun is how log lines name a matching task, such as "deploy".
	noun string
	// name is the description it matches, for the "not followed" log.
	name string
	// match reports whether a running task is the call's task.
	match func(Task) bool
}

// deployMatch matches a deploy running on the deployment, whose
// description is exactly DeployTaskDescription.
func deployMatch(deployment string) runningMatch {
	return runningMatch{
		noun: kindDeploy,
		name: DeployTaskDescription,
		match: func(task Task) bool {
			return task.Deployment == deployment && task.Description == DeployTaskDescription
		},
	}
}

// deleteMatch matches a delete running on the deployment, whose
// description is exactly "delete deployment <name>".
func deleteMatch(deployment string) runningMatch {
	description := "delete deployment " + deployment

	return runningMatch{
		noun: "delete",
		name: description,
		match: func(task Task) bool {
			return task.Deployment == deployment && task.Description == description
		},
	}
}

// anyRunning matches every task that runs on the deployment.
func anyRunning() runningMatch {
	return runningMatch{
		noun:  "task",
		name:  "any",
		match: func(Task) bool { return true },
	}
}

// contextIDLookup looks for a lost call's task by its context ID, when it
// carries one, and then among the tasks running on its deployment.
type contextIDLookup struct {
	match runningMatch
}

// find looks for the task by the watch's context ID, when it carries one,
// and then as a task running on its deployment that the match accepts,
// behind any other task that runs there. It returns the task ID when
// either finds the task, and an error when either lookup failed. It never
// reports the deployment busy.
func (l contextIDLookup) find(ctx context.Context, follower *taskFollower, watch *followWatch) (int, bool, error) {
	var lookupErr error

	if watch.contextID != "" {
		task, err := follower.director.FindTaskByContextID(ctx, watch.contextID)
		if err != nil {
			lookupErr = fmt.Errorf("the lookup of the task with context ID %s failed: %w", watch.contextID, err)
		} else if task != nil {
			follower.logger.Infof("Found BOSH task %d by the context ID %s of the %s of %s", task.ID, watch.contextID, watch.call.kind, watch.call.deployment)

			return task.ID, false, nil
		}
	}

	running, err := follower.director.FindRunningTasksForDeployment(ctx, watch.call.deployment)
	if err != nil {
		return 0, false, errors.Join(lookupErr, fmt.Errorf("the check for a task running on deployment %s failed: %w", watch.call.deployment, err))
	}

	for _, task := range running {
		if l.match.match(task) {
			follower.logger.Infof("BOSH task %d is a %s running on deployment %s, so the %s follows it for %s",
				task.ID, l.match.noun, watch.call.deployment, follower.owner, watch.lostCall())

			return task.ID, false, nil
		}
	}

	for _, task := range running {
		follower.logger.Infof("BOSH task %d (%q on deployment %q) runs on deployment %s, and it is not a %q task, so it is not followed",
			task.ID, task.Description, task.Deployment, watch.call.deployment, l.match.name)
	}

	return 0, false, lookupErr
}

// giveUp ends the watch of a lost answer whose task never turned up. A
// call that carries no context ID ends with ErrWatchAbandoned, and one
// that carries an ID ends with ErrNoTaskWithContextID.
func (l contextIDLookup) giveUp(follower *taskFollower, watch *followWatch) error {
	if watch.contextID == "" {
		follower.logger.Errorf("No %s runs on deployment %s %v after the answer to %s was lost, so the %s released its slot",
			l.match.noun, watch.call.deployment, UnknownOutcomeGrace, watch.lostCall(), follower.owner)

		return fmt.Errorf("%w: no %s runs on deployment %s, and its lost %s carries no context ID",
			ErrWatchAbandoned, l.match.noun, watch.call.deployment, watch.call.kind)
	}

	follower.logger.Errorf("No task has the context ID of %s, and no %s runs on the deployment, %v after its answer was lost, so the %s released its slot",
		watch.lostCall(), l.match.noun, UnknownOutcomeGrace, follower.owner)

	return fmt.Errorf("%w %s, and no %s runs on deployment %s", ErrNoTaskWithContextID, watch.contextID, l.match.noun, watch.call.deployment)
}

// runningTaskLookup holds the slot of a lost call that can't be told apart
// from other tasks on its deployment, such as a job-state change, for as
// long as the match finds a task running there. It never picks a task to
// follow, and it never looks a task up by context ID, because the
// director may not store the ID for these calls.
type runningTaskLookup struct {
	match runningMatch
}

// find reports the deployment busy when the match accepts any task that
// runs on it, and an error when the check failed.
func (l runningTaskLookup) find(ctx context.Context, follower *taskFollower, watch *followWatch) (int, bool, error) {
	running, err := follower.director.FindRunningTasksForDeployment(ctx, watch.call.deployment)
	if err != nil {
		return 0, false, fmt.Errorf("the check for a task running on deployment %s failed: %w", watch.call.deployment, err)
	}

	for _, task := range running {
		if l.match.match(task) {
			follower.logger.Debugf("BOSH task %d (%q) runs on deployment %s, so the %s keeps the %s of %s",
				task.ID, task.Description, watch.call.deployment, follower.owner, follower.slot, watch.lostCall())

			return 0, true, nil
		}
	}

	return 0, false, nil
}

// giveUp ends the watch once the deployment went idle after the grace, or
// once it stayed busy for the watch limit.
func (l runningTaskLookup) giveUp(follower *taskFollower, watch *followWatch) error {
	if watch.busy {
		follower.logger.Errorf("Deployment %s never went idle in the %v after the answer to %s was lost, so the %s released its slot",
			watch.call.deployment, follower.watchMax, watch.lostCall(), follower.owner)

		return fmt.Errorf("%w: deployment %s never went idle in the %v after the answer to its %s was lost",
			ErrWatchAbandoned, watch.call.deployment, follower.watchMax, watch.call.kind)
	}

	follower.logger.Errorf("No %s runs on deployment %s %v after the answer to %s was lost, so the %s released its slot",
		l.match.noun, watch.call.deployment, UnknownOutcomeGrace, watch.lostCall(), follower.owner)

	return fmt.Errorf("%w: no %s runs on deployment %s %v after the answer to its %s was lost",
		ErrWatchAbandoned, l.match.noun, watch.call.deployment, UnknownOutcomeGrace, watch.call.kind)
}

// graceHold holds the slot of a lost call whose task the director can't
// be asked for, such as an upload or a cleanup, for UnknownOutcomeGrace.
type graceHold struct{}

// find reports nothing found, and calls nothing on the director.
func (graceHold) find(context.Context, *taskFollower, *followWatch) (int, bool, error) {
	return 0, false, nil
}

// giveUp ends the watch once the grace has passed.
func (graceHold) giveUp(follower *taskFollower, watch *followWatch) error {
	follower.logger.Errorf("The director can't be asked for the task of %s, so the %s released its %s %v after the answer was lost",
		watch.lostCall(), follower.owner, follower.slot, UnknownOutcomeGrace)

	return fmt.Errorf("%w: the director can't be asked for the task of the %s whose answer was lost", ErrWatchAbandoned, watch.call.kind)
}

// followWatch is the watch of one call's task.
type followWatch struct {
	// call is the call whose task the watch follows.
	call follow
	// contextID is the context ID of a call whose answer was lost.
	contextID string
	// taskID is the task the watcher follows, or 0 while it looks for the
	// task of a lost answer. It is guarded by the follower's mu.
	taskID int
	// holdsSlot is false for the watcher of a task that had already ended
	// when the call returned it, whose slot was released at once.
	holdsSlot bool
	// lost is true for the watcher of a call whose answer was lost, and
	// lostAt is when the follower learned that the answer was lost.
	lost   bool
	lostAt time.Time
	// busy is what the latest lookup of a lost answer's task reported, so
	// the lookup's giveUp can tell a deployment that stayed busy from one
	// that went idle. Only the watcher's own goroutine touches it.
	busy bool
	// cancel ends the watcher's follow. The watcher sets it, under mu, as
	// it starts.
	cancel context.CancelFunc
	// slotSettled is set once the watcher has decided who releases its
	// slot, and superseded when a later call on the deployment took the
	// slot over. Both are guarded by the follower's mu.
	slotSettled bool
	superseded  bool
	// done is closed once task and err hold the watcher's result.
	done chan struct{}
	task *Task
	err  error
}

func newFollowWatch(call follow, contextID string) *followWatch {
	return &followWatch{call: call, contextID: contextID, holdsSlot: true, done: make(chan struct{})}
}

// describeTask names a task, and its deployment when there is one, for
// logs and errors.
func describeTask(deployment string, taskID int) string {
	if deployment == "" {
		return fmt.Sprintf("BOSH task %d", taskID)
	}

	return fmt.Sprintf("BOSH task %d of deployment %s", taskID, deployment)
}

// describe names the task of a watch for logs and errors.
func (w *followWatch) describe(taskID int) string {
	return describeTask(w.call.deployment, taskID)
}

// finished reports whether the watcher's follow has returned.
func (w *followWatch) finished() bool {
	select {
	case <-w.done:
		return true
	default:
		return false
	}
}

// lostCall names the call of a watch whose answer was lost, for logs and
// errors.
func (w *followWatch) lostCall() string {
	what := "the " + w.call.kind
	if w.call.deployment != "" {
		what += " of " + w.call.deployment
	}

	if w.contextID == "" {
		return what + ", which carries no context ID,"
	}

	return what + " with context ID " + w.contextID
}

// followerConfig is what a wrapper gives the follower it shares.
type followerConfig struct {
	director Director
	logger   Logger
	// mu is the wrapper's own mutex, which the follower shares, so a
	// wrapper can count a slot and register a watch in one critical
	// section.
	mu *sync.Mutex
	// releaseSlot frees one slot. The follower calls it without mu held.
	releaseSlot func()
	// owner and slot are the words logs and errors use for the wrapper
	// and its slot, such as "batch director" and "batch slot".
	owner string
	slot  string
}

// taskFollower follows the director tasks of the calls a wrapper made,
// and frees each call's slot when its task ends, or when the director is
// known to have started no task. A watch runs on its own goroutine under
// the follower's lifetime context, never under a caller's, and Stop ends
// every watch.
type taskFollower struct {
	director    Director
	logger      Logger
	mu          *sync.Mutex
	releaseSlot func()
	owner       string
	slot        string
	clock       followClock
	watchMax    time.Duration

	// lifetime is the context the watchers run under, and stop cancels it.
	lifetime context.Context //nolint:containedctx // watchers outlive the calls that start them, and Stop ends them through it
	stop     context.CancelFunc
	watchers sync.WaitGroup

	// The fields below are guarded by mu.
	stopped bool
	// running counts the watcher goroutines that have not returned.
	running int
	// byTask and byContextID hold each watcher until followDonePrune after
	// its task ends, keyed by its task ID and by the context ID of a lost
	// answer.
	byTask      map[int]*followWatch
	byContextID map[string]*followWatch
	// lostByDeployment holds the watcher of a supersedable lost answer for
	// each deployment, until the watcher ends.
	lostByDeployment map[string]*followWatch
}

func newTaskFollower(cfg followerConfig) *taskFollower {
	logger := cfg.logger
	if logger == nil {
		logger = &noOpLogger{}
	}

	lifetime, stop := context.WithCancel(context.Background())

	return &taskFollower{
		director:         cfg.director,
		logger:           logger,
		mu:               cfg.mu,
		releaseSlot:      cfg.releaseSlot,
		owner:            cfg.owner,
		slot:             cfg.slot,
		clock:            systemFollowClock{},
		watchMax:         followWatchMax,
		lifetime:         lifetime,
		stop:             stop,
		byTask:           map[int]*followWatch{},
		byContextID:      map[string]*followWatch{},
		lostByDeployment: map[string]*followWatch{},
	}
}

// Stop ends every watcher and waits for them to return. A watcher ended
// by Stop releases its slot and reports context.Canceled, with no final
// task, to its waiters.
func (f *taskFollower) Stop() {
	f.stop()
	f.watchers.Wait()
}

// owned is the follower's owner with a capital first letter, for the
// start of a log line.
func (f *taskFollower) owned() string {
	return "The " + f.owner
}

// watcherName names one of the follower's watchers, such as "batch
// watcher".
func (f *taskFollower) watcherName() string {
	return strings.TrimSuffix(f.slot, " slot") + " watcher"
}

// settleCall decides what happens to a call's slot once the base director
// returned the call's task and error. It frees the slot at once when the
// error shows that no task started, or when the call returned no usable
// task. It keeps a task that already ended for waiters, follows a task
// that still runs, and starts a lost-answer watch for any other error. It
// sets handedOff, in the step that starts a watch, when that watch took
// the slot, so the wrapper still frees a slot nobody took over. It returns
// the task and error the call returned.
func (f *taskFollower) settleCall(call follow, task *Task, err error, handedOff *bool) (*Task, error) {
	if err != nil {
		var unknown *OutcomeUnknownError

		if errors.As(err, &unknown) {
			f.watchLostAnswer(call, unknown.ContextID, handedOff)
		} else if reason, ok := noTaskStarted(err, call.kind); ok {
			f.logger.Infof("Released the %s of %s at once, because %s: %v", f.slot, call.subject(), reason, err)
		} else {
			f.logger.Infof("Kept the %s of %s, because the %s's error does not show that no %s task runs: %v",
				f.slot, call.subject(), call.kind, call.kind, err)

			f.watchLostAnswer(call, contextIDOf(err), handedOff)
		}

		return task, err
	}

	if task == nil || task.ID <= 0 {
		f.logger.Errorf("Released the %s of %s at once, because the director named no task for the %s", f.slot, call.subject(), call.kind)

		return task, nil
	}

	if isTerminalTaskState(task.State) {
		*handedOff = true

		f.releaseSlot()
		f.logger.Infof("%s had ended in state %s when the %s call returned, so its %s is released at once",
			describeTask(call.deployment, task.ID), task.State, call.kind, f.slot)
		f.keepFinishedTask(call, *copyTask(task))

		return task, nil
	}

	f.watchTask(call, task.ID, handedOff)

	return task, nil
}

// startTaskLocked follows a task the wrapper has counted in a slot, such as
// one adopted after a restart. It reports false, and starts nothing, once
// the follower is stopped. The caller holds mu.
func (f *taskFollower) startTaskLocked(c follow, taskID int) bool {
	watch := newFollowWatch(c, "")

	return f.startWatchLocked(watch, taskID, func(ctx context.Context) (*Task, error) {
		return f.followTask(ctx, watch, taskID)
	})
}

// followsLocked reports whether a watcher that has not finished follows
// the task. The caller holds mu.
func (f *taskFollower) followsLocked(taskID int) bool {
	current := f.byTask[taskID]

	return current != nil && !current.finished()
}

// markStoppedLocked stops the follower from starting watches. The caller
// holds mu, and calls Stop once it has released mu.
func (f *taskFollower) markStoppedLocked() {
	f.stopped = true
}

// awaitTask waits for the watch that follows the task, and reports false
// when no watch follows it, so the wrapper passes the wait through.
func (f *taskFollower) awaitTask(ctx context.Context, taskID int) (*Task, bool, error) {
	f.mu.Lock()
	watch := f.byTask[taskID]
	f.mu.Unlock()

	if watch == nil {
		return nil, false, nil
	}

	task, err := f.awaitWatch(ctx, watch, fmt.Sprintf("BOSH task %d", taskID))

	return task, true, err
}

// awaitContextID waits for the watch of the lost answer with the context
// ID, and reports false when no watch looks for it.
func (f *taskFollower) awaitContextID(ctx context.Context, contextID string) (*Task, bool, error) {
	f.mu.Lock()
	watch := f.byContextID[contextID]
	f.mu.Unlock()

	if watch == nil {
		return nil, false, nil
	}

	task, err := f.awaitWatch(ctx, watch, "the BOSH task with context ID "+contextID)

	return task, true, err
}

// supersedeLostAnswer ends the watcher of a lost answer for the
// deployment, if one still holds its slot, and reports whether the caller
// now owns that slot. The watcher is told, under mu, not to release the
// slot, so the slot is never free in between. Its waiters get
// ErrLostAnswerSuperseded.
func (f *taskFollower) supersedeLostAnswer(deployment string) bool {
	f.mu.Lock()
	watch := f.lostByDeployment[deployment]

	if watch == nil || watch.slotSettled {
		f.mu.Unlock()

		return false
	}

	watch.superseded = true
	watch.slotSettled = true

	delete(f.lostByDeployment, deployment)

	if watch.cancel != nil {
		watch.cancel()
	}

	f.mu.Unlock()

	f.logger.Infof("A new %s of deployment %s ended the %s of %s and took over its %s",
		watch.call.kind, deployment, f.watcherName(), watch.lostCall(), f.slot)

	return true
}

// keepFinishedTask keeps a task that had already ended when the call
// returned it, so a wait for it gets the final task. It holds no slot.
// When the task lacks the end time, or lacks the deployment of a call
// that names one, it reads the task once, bounded by
// followFinishedReadTimeout, and when that read fails, waiters get the
// task the call returned.
func (f *taskFollower) keepFinishedTask(c follow, returned Task) {
	watch := newFollowWatch(c, "")
	watch.holdsSlot = false

	f.mu.Lock()
	f.startWatchLocked(watch, returned.ID, func(ctx context.Context) (*Task, error) {
		return f.readFinishedTask(ctx, watch, returned), nil
	})
	f.mu.Unlock()
}

// readFinishedTask returns the director's record of a finished task, or
// the task the call returned when that record cannot be read.
func (f *taskFollower) readFinishedTask(ctx context.Context, watch *followWatch, returned Task) *Task {
	if returned.EndedAt != nil && (returned.Deployment != "" || watch.call.deployment == "") {
		return &returned
	}

	readCtx, cancel := context.WithTimeout(ctx, followFinishedReadTimeout)
	defer cancel()

	task, err := f.director.GetTask(readCtx, returned.ID)
	if err != nil || task == nil || task.ID != returned.ID || !isTerminalTaskState(task.State) {
		f.logger.Infof("The read of finished %s did not give its final record, so its waiters get the task the %s call returned: %v",
			watch.describe(returned.ID), watch.call.kind, err)

		return &returned
	}

	return task
}

// watchTask follows the task of a call the director accepted, and
// releases the call's slot when the task ends. It sets handedOff, in the
// step that starts the watcher, when a watcher took the slot, which it does
// not once the follower is stopped.
func (f *taskFollower) watchTask(c follow, taskID int, handedOff *bool) {
	watch := newFollowWatch(c, "")

	f.mu.Lock()
	started := f.startWatchLocked(watch, taskID, func(ctx context.Context) (*Task, error) {
		return f.followTask(ctx, watch, taskID)
	})
	*handedOff = started
	f.mu.Unlock()

	if !started {
		f.logger.Infof("%s is stopped, so it released the slot of %s without following it", f.owned(), watch.describe(taskID))
	}
}

// watchLostAnswer keeps the slot of a call whose answer was lost, and
// looks for its task through the call's lookup until it finds it, or until
// the lookup gives up. It sets handedOff, in the step that starts the
// watcher, when a watcher took the slot, which it does not once the
// follower is stopped.
func (f *taskFollower) watchLostAnswer(c follow, contextID string, handedOff *bool) {
	watch := newFollowWatch(c, contextID)
	watch.lost = true
	watch.lostAt = f.clock.Now()

	f.mu.Lock()
	started := f.startWatchLocked(watch, 0, func(ctx context.Context) (*Task, error) {
		return f.resolveLostAnswer(ctx, watch)
	})
	*handedOff = started
	f.mu.Unlock()

	if !started {
		f.logger.Infof("%s is stopped, so it released the slot of %s without looking for its task", f.owned(), watch.lostCall())

		return
	}

	f.logger.Infof("The %s of %s stays held while the %s looks for its task", f.slot, watch.lostCall(), f.owner)
}

// startWatchLocked registers a watch and runs it on its own goroutine,
// which releases the watch's slot, if it holds one, when the watch ends.
// It reports false, and starts nothing, once the follower is stopped. The
// caller holds mu, and has counted a watch that holds a slot.
func (f *taskFollower) startWatchLocked(watch *followWatch, taskID int, run func(ctx context.Context) (*Task, error)) bool {
	if f.stopped {
		return false
	}

	if taskID > 0 {
		watch.taskID = taskID
		f.byTask[taskID] = watch
	}

	if watch.contextID != "" {
		f.byContextID[watch.contextID] = watch
	}

	if watch.lost && watch.call.supersedable {
		f.lostByDeployment[watch.call.deployment] = watch
	}

	f.running++
	f.watchers.Add(1)

	go f.runWatch(f.lifetime, watch, run)

	return true
}

// runWatch runs one watcher. A watcher that holds a slot releases it
// before it reports the result, so a waiter that starts the next call
// finds the slot free, and every watcher keeps its result for waiters
// until followDonePrune has passed.
func (f *taskFollower) runWatch(parent context.Context, watch *followWatch, run func(ctx context.Context) (*Task, error)) {
	defer f.watchers.Done()
	defer f.watcherReturned()

	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	f.mu.Lock()
	watch.cancel = cancel

	if watch.superseded {
		cancel()
	}

	f.mu.Unlock()

	if watch.holdsSlot {
		watch.task, watch.err = f.followHoldingSlot(ctx, watch, run)
	} else {
		watch.task, watch.err = run(ctx)
	}

	f.mu.Lock()
	superseded := watch.superseded
	f.mu.Unlock()

	if superseded {
		watch.task, watch.err = nil, ErrLostAnswerSuperseded
	}

	close(watch.done)

	err := f.clock.Sleep(f.lifetime, followDonePrune)
	if err != nil {
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	if watch.taskID > 0 && f.byTask[watch.taskID] == watch {
		delete(f.byTask, watch.taskID)
	}

	if watch.contextID != "" && f.byContextID[watch.contextID] == watch {
		delete(f.byContextID, watch.contextID)
	}
}

// followHoldingSlot runs a watcher's follow, and releases its slot however
// the follow ends, unless a later call took the slot over.
func (f *taskFollower) followHoldingSlot(ctx context.Context, watch *followWatch, run func(ctx context.Context) (*Task, error)) (*Task, error) {
	defer f.releaseWatchSlot(watch)

	return run(ctx)
}

// releaseWatchSlot releases the slot of a watcher that ended, unless a
// later call took it over. It decides that under mu, so a call takes the
// slot over or the watcher releases it, never both.
func (f *taskFollower) releaseWatchSlot(watch *followWatch) {
	f.mu.Lock()
	taken := watch.superseded
	watch.slotSettled = true

	if f.lostByDeployment[watch.call.deployment] == watch {
		delete(f.lostByDeployment, watch.call.deployment)
	}

	f.mu.Unlock()

	if !taken {
		f.releaseSlot()
	}
}

func (f *taskFollower) watcherReturned() {
	f.mu.Lock()
	f.running--
	f.mu.Unlock()
}

// readFailures tracks a run of failed reads. since is when the first
// failed read of the run happened, and refusedSince is when the first of
// the latest run of reads the director refused happened. Each is zero when
// no such run is going on.
type readFailures struct {
	since        time.Time
	refusedSince time.Time
}

// succeeded ends the run of failed reads.
func (r *readFailures) succeeded() {
	r.since, r.refusedSince = time.Time{}, time.Time{}
}

// failed records a read that failed at now with err. It returns how long
// the run of failures has gone on and how long it may go on, and whether
// the director refused the read. A run of refused reads, which a retry
// cannot fix on its own, may go on for UnknownOutcomeGrace, and any other
// run for watchMax.
func (r *readFailures) failed(now time.Time, err error, watchMax time.Duration) (time.Duration, time.Duration, bool) {
	if r.since.IsZero() {
		r.since = now
	}

	if !boshapi.IsUnauthorized(err) && !boshapi.IsForbidden(err) {
		r.refusedSince = time.Time{}

		return now.Sub(r.since), watchMax, false
	}

	if r.refusedSince.IsZero() {
		r.refusedSince = now
	}

	return now.Sub(r.refusedSince), min(UnknownOutcomeGrace, watchMax), true
}

// followTask waits for the task to end and returns it. After a wait that
// fails, it reads the task with backoff until a read succeeds, and then
// waits again. It gives up once reads have failed for watchMax with no
// read that succeeded in between, or once the director has refused them
// for UnknownOutcomeGrace, and a task the director answers it has no
// record of ends the watch at once. It logs every hour that it still
// follows the task.
func (f *taskFollower) followTask(ctx context.Context, watch *followWatch, taskID int) (*Task, error) {
	stopReports := f.reportFollow(ctx, watch, taskID)
	defer stopReports()

	var failures readFailures

	for {
		task, err := f.director.WaitForTask(ctx, taskID)
		if err == nil && task != nil {
			f.logger.Infof("%s ended in state %s, so its %s is released", watch.describe(taskID), task.State, f.slot)

			return task, nil
		}

		task, err = f.readUntilAnswered(ctx, watch, taskID, &failures, err)
		if err != nil || task != nil {
			return task, err
		}
	}
}

// reportFollow logs every followReportInterval that the watcher still
// follows the task, until the function it returns is called.
func (f *taskFollower) reportFollow(ctx context.Context, watch *followWatch, taskID int) func() {
	reportCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	started := f.clock.Now()

	go func() {
		defer close(done)

		for f.clock.Sleep(reportCtx, followReportInterval) == nil {
			f.logger.Infof("%s has followed %s for %v, and the task still holds a %s",
				f.owned(), watch.describe(taskID), f.clock.Now().Sub(started), f.slot)
		}
	}()

	return func() {
		cancel()
		<-done
	}
}

// readUntilAnswered handles a wait for a task that failed with waitErr. It
// reads the task with backoff until a read succeeds. It returns the task
// when the read shows that it ended, nil with no error when it still runs,
// and an error when the watch ends.
func (f *taskFollower) readUntilAnswered(ctx context.Context, watch *followWatch, taskID int, failures *readFailures, waitErr error) (*Task, error) {
	backoff := followWatchBackoffMin
	err := waitErr

	for {
		if err == nil {
			err = ErrEmptyDirectorAnswer
		}

		if ctx.Err() != nil {
			return nil, f.stoppedFollowing(watch.describe(taskID))
		}

		if isTaskNotFound(err) {
			f.logger.Errorf("The director has no %s, so the %s released its slot: %v", watch.describe(taskID), f.owner, err)

			return nil, fmt.Errorf("%w: the director has no %s: %w", ErrWatchAbandoned, watch.describe(taskID), err)
		}

		failing, limit, refused := failures.failed(f.clock.Now(), err, f.watchMax)
		if refused {
			f.logger.Errorf("The director refused the %s's read of %s, which it retries for at most %v: %v", f.owner, watch.describe(taskID), limit, err)
		}

		if failing >= limit {
			f.logger.Errorf("%s could not read %s for %v, so it released the slot of the %s: %v",
				f.owned(), watch.describe(taskID), failing, watch.call.kind, err)

			return nil, fmt.Errorf("%w: %s could not be read for %v: %w", ErrWatchAbandoned, watch.describe(taskID), failing, err)
		}

		f.logger.Infof("Reading %s failed, so the %s reads it again in %v: %v", watch.describe(taskID), f.owner, backoff, err)

		if f.clock.Sleep(ctx, backoff) != nil {
			return nil, f.stoppedFollowing(watch.describe(taskID))
		}

		backoff = min(backoff*followWatchBackoffFactor, followWatchBackoffMax)

		var task *Task

		task, err = f.director.GetTask(ctx, taskID)
		if err == nil && task != nil {
			failures.succeeded()

			if isTerminalTaskState(task.State) {
				f.logger.Infof("%s ended in state %s, so its %s is released", watch.describe(taskID), task.State, f.slot)

				return task, nil
			}

			return nil, nil //nolint:nilnil // a task that still runs is waited for again
		}
	}
}

// resolveLostAnswer looks for the task of a call whose answer was lost
// every followLostAnswerInterval, through the call's lookup, and follows
// the task once found. The lookup gives up only after a check that
// succeeded, made at or after lostAt plus UnknownOutcomeGrace, found
// nothing and did not find the deployment busy, or once the deployment has
// stayed busy for watchMax since lostAt. The watch also ends once lookups
// have failed for watchMax with no lookup that succeeded in between, or
// once the director has refused them for UnknownOutcomeGrace.
func (f *taskFollower) resolveLostAnswer(ctx context.Context, watch *followWatch) (*Task, error) {
	what := watch.lostCall()

	var failures readFailures

	for {
		now := f.clock.Now()
		taskID, busy, err := watch.call.lookup.find(ctx, f, watch)

		if ctx.Err() != nil {
			return nil, f.stoppedFollowing(what)
		}

		if taskID > 0 {
			f.followFoundTask(watch, taskID)

			return f.followTask(ctx, watch, taskID)
		}

		watch.busy = busy

		if busy && !now.Before(watch.lostAt.Add(f.watchMax)) {
			return nil, watch.call.lookup.giveUp(f, watch)
		}

		if err == nil && !busy && !now.Before(watch.lostAt.Add(UnknownOutcomeGrace)) {
			return nil, watch.call.lookup.giveUp(f, watch)
		}

		if err == nil {
			failures.succeeded()
		} else {
			failing, limit, refused := failures.failed(now, err, f.watchMax)
			if refused {
				f.logger.Errorf("The director refused the %s's lookup of the task of %s, which it retries for at most %v: %v", f.owner, what, limit, err)
			}

			if failing >= limit {
				f.logger.Errorf("%s could not look for the task of %s for %v, so it released the slot: %v", f.owned(), what, failing, err)

				return nil, fmt.Errorf("%w: the task of %s could not be looked up for %v: %w", ErrWatchAbandoned, what, failing, err)
			}

			f.logger.Infof("Looking for the task of %s failed, so the %s looks again in %v: %v", what, f.owner, followLostAnswerInterval, err)
		}

		if f.clock.Sleep(ctx, followLostAnswerInterval) != nil {
			return nil, f.stoppedFollowing(what)
		}
	}
}

// followFoundTask records the task a watcher of a lost answer found, so a
// wait for that task is served by the watcher. A task another watcher
// already follows keeps that watcher.
func (f *taskFollower) followFoundTask(watch *followWatch, taskID int) {
	f.mu.Lock()
	defer f.mu.Unlock()

	watch.taskID = taskID

	if f.byTask[taskID] == nil {
		f.byTask[taskID] = watch
	}
}

// awaitWatch waits for a watcher's result or for the caller's context.
// Each waiter gets its own copy of the watcher's final task.
func (f *taskFollower) awaitWatch(ctx context.Context, watch *followWatch, what string) (*Task, error) {
	select {
	case <-watch.done:
		return copyTask(watch.task), watch.err
	case <-ctx.Done():
		return nil, fmt.Errorf("stopped waiting for %s, which the %s still follows: %w", what, f.owner, ctx.Err())
	}
}

// stoppedFollowing is the result of a watcher that Stop ended.
func (f *taskFollower) stoppedFollowing(what string) error {
	return fmt.Errorf("the %s stopped and no longer follows %s: %w", f.owner, what, context.Canceled)
}

// isTaskNotFound reports whether the director answered that it has no
// such task, with its task-not-found code. Any other not-found answer,
// such as a route the director does not serve or a director-not-found
// code from a proxy, is a failed read.
func isTaskNotFound(err error) bool {
	var apiErr *boshapi.APIError

	return errors.As(err, &apiErr) && apiErr.Code == boshapi.ErrorCodeTaskNotFound
}

// noTaskStarted reports whether a call's error shows that the call started
// no task on the director, and says why. kind names the call. Only the
// director's own error answer, a request that never reached the director,
// and a failed read of the task after the call returned show that. A
// gateway's error answer to the call does not, because the director may
// have accepted the request behind it.
func noTaskStarted(err error, kind string) (string, bool) {
	if boshapi.IsOutcomeUnknown(err) {
		return "", false
	}

	var apiErr *boshapi.APIError
	if errors.As(err, &apiErr) {
		return "the director answered the " + kind + " with an error", true
	}

	var transportErr *boshapi.TransportError
	if errors.As(err, &transportErr) && !transportErr.Sent {
		return "the " + kind + " request never reached the director", true
	}

	// The base director returns ErrFailedGetTaskUpdate only after its
	// call returned without an error, which a bosh-cli call does once its
	// task has ended.
	if errors.Is(err, ErrFailedGetTaskUpdate) {
		return "the " + kind + " call returned and only the read of its task failed", true
	}

	return "", false
}

// contextIDOf returns the context ID a call's error carries, or an empty
// string when it carries none.
func contextIDOf(err error) string {
	var transportErr *boshapi.TransportError
	if errors.As(err, &transportErr) && transportErr.ContextID != "" {
		return transportErr.ContextID
	}

	var answerErr *boshapi.TaskAnswerError
	if errors.As(err, &answerErr) && answerErr.ContextID != "" {
		return answerErr.ContextID
	}

	var redirectErr *boshapi.RedirectError
	if errors.As(err, &redirectErr) {
		return redirectErr.ContextID
	}

	return ""
}

// copyTask returns a copy of a task, its end time included, so a waiter
// that changes the task it got does not change what other waiters get.
func copyTask(task *Task) *Task {
	if task == nil {
		return nil
	}

	copied := *task

	if task.EndedAt != nil {
		ended := *task.EndedAt
		copied.EndedAt = &ended
	}

	return &copied
}
