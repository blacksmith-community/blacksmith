package bosh

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"blacksmith/internal/bosh/sshconn"
	boshdirector "github.com/cloudfoundry/bosh-cli/v7/director"
	"github.com/fivetwenty-io/boshapi/pkg/boshapi"
	"golang.org/x/crypto/ssh"
)

// CreateBasicFactoryConfig exposes createBasicFactoryConfig to package bosh_test.
func CreateBasicFactoryConfig(config Config) *boshdirector.FactoryConfig {
	return createBasicFactoryConfig(config)
}

// RequestTimeoutOf reports the request timeout an adapter runs with.
func RequestTimeoutOf(director Director) time.Duration {
	return director.(*DirectorAdapter).requestTimeout
}

// WithAPI replaces the boshapi client an adapter reads through.
func WithAPI(director Director, api boshapi.Client) {
	director.(*DirectorAdapter).api = api
}

// SetTaskPollInterval sets how often an adapter checks a task it waits for.
func SetTaskPollInterval(director Director, interval time.Duration) {
	director.(*DirectorAdapter).taskPollInterval = interval
}

// SetSSHPort points an adapter's SSH connections at port, such as a
// loopback test server's.
func SetSSHPort(director Director, port string) {
	adapter, ok := director.(*DirectorAdapter)
	if !ok {
		panic(fmt.Sprintf("SetSSHPort needs a *DirectorAdapter, got %T", director))
	}

	adapter.sshPort = port
}

// SetSSHSetupSettle shortens how long an adapter waits for a failed SSH
// setup to end before its cleanup: grace for a setup whose answer was
// lost to become a task, and settle for the setup task to end.
func SetSSHSetupSettle(director Director, grace, settle time.Duration) {
	adapter, ok := director.(*DirectorAdapter)
	if !ok {
		panic(fmt.Sprintf("SetSSHSetupSettle needs a *DirectorAdapter, got %T", director))
	}

	adapter.lostSetupGrace = grace
	adapter.setupSettleTimeout = settle
}

// SetSSHSetupTimeout shortens an adapter's bound on the wait for an SSH
// setup task.
func SetSSHSetupTimeout(director Director, timeout time.Duration) {
	adapter, ok := director.(*DirectorAdapter)
	if !ok {
		panic(fmt.Sprintf("SetSSHSetupTimeout needs a *DirectorAdapter, got %T", director))
	}

	adapter.setupTimeout = timeout
}

// RequestTimeoutCause exposes the cause a request timeout carries to package
// bosh_test.
func RequestTimeoutCause(timeout time.Duration, attempts int) error {
	return &requestTimeoutError{timeout: timeout, attempts: attempts}
}

// BatchClock is the time source a batch director's watchers read.
type BatchClock = followClock

// How often a watcher looks for the task of a lost answer, how long it
// keeps a final task for waiters, and how often it logs a task it still
// follows.
const (
	BatchLostAnswerInterval   = followLostAnswerInterval
	BatchDonePrune            = followDonePrune
	BatchFollowReportInterval = followReportInterval
)

// SetBatchClock replaces the time source of a batch director's watchers.
// It is set before the batch director starts any deploy.
func SetBatchClock(batch *BatchDirector, clock BatchClock) {
	batch.follow.clock = clock
}

// SetBatchWatchMax shortens how long a watcher reads a task the director
// does not answer for. It is set before the batch director starts any
// deploy.
func SetBatchWatchMax(batch *BatchDirector, watchMax time.Duration) {
	batch.follow.watchMax = watchMax
}

// BatchWatchersRunning reports how many watcher goroutines have not
// returned.
func BatchWatchersRunning(batch *BatchDirector) int {
	batch.mu.Lock()
	defer batch.mu.Unlock()

	return batch.follow.running
}

// BatchWatchesHeld reports how many watchers a batch director holds by
// task ID and by context ID.
func BatchWatchesHeld(batch *BatchDirector) (int, int) {
	batch.mu.Lock()
	defer batch.mu.Unlock()

	return len(batch.follow.byTask), len(batch.follow.byContextID)
}

// SetPoolClock replaces the time source of a pooled director's watchers.
// It is set before the pooled director starts any mutation.
func SetPoolClock(pool *PooledDirector, clock BatchClock) {
	pool.follow.clock = clock
}

// SetPoolWatchMax shortens how long a pooled director's watcher reads a
// task the director does not answer for, and how long it holds a lost
// job-state answer. It is set before the pooled director starts any
// mutation.
func SetPoolWatchMax(pool *PooledDirector, watchMax time.Duration) {
	pool.follow.watchMax = watchMax
}

// PoolWatchersRunning reports how many of a pooled director's watcher
// goroutines have not returned.
func PoolWatchersRunning(pool *PooledDirector) int {
	pool.mu.Lock()
	defer pool.mu.Unlock()

	return pool.follow.running
}

// PoolWatchMaxOf reports the watch limit of a pooled director's follower.
func PoolWatchMaxOf(pool *PooledDirector) time.Duration {
	return pool.follow.watchMax
}

// BatchWatchMaxOf reports the watch limit of a batch director's follower.
func BatchWatchMaxOf(batch *BatchDirector) time.Duration {
	return batch.follow.watchMax
}

// ErrNoPoolWatcherForContextID is returned by AwaitPoolContextID when no
// watch of the pooled director looks for the context ID. The pool makes no
// lookup on the director for it.
var ErrNoPoolWatcherForContextID = errors.New("no pooled director watcher looks for the context ID")

// AwaitPoolContextID waits for a pooled director's watch of the lost
// answer with the context ID, and returns ErrNoPoolWatcherForContextID
// when no watch looks for it.
func AwaitPoolContextID(ctx context.Context, pool *PooledDirector, contextID string) (*Task, error) {
	task, found, err := pool.follow.awaitContextID(ctx, contextID)
	if !found {
		return nil, fmt.Errorf("%w: %s", ErrNoPoolWatcherForContextID, contextID)
	}

	return task, err
}

// JoinBatchQueue puts a waiter that never takes a slot at the back of a
// batch director's queue, and returns its ticket.
func JoinBatchQueue(batch *BatchDirector) uint64 {
	batch.mu.Lock()
	defer batch.mu.Unlock()

	ticket := batch.nextTicket
	batch.nextTicket++
	batch.waiters = append(batch.waiters, ticket)

	return ticket
}

// LeaveBatchQueue takes a ticket out of a batch director's queue, the way
// a waiter that times out or is cancelled leaves it.
func LeaveBatchQueue(batch *BatchDirector, ticket uint64) {
	batch.leaveQueue(ticket)
}

// TakeBatchSlotAtHead gives a slot to the waiter at the head of a batch
// director's queue, the way acquireSlot does for a waiter whose turn has
// come.
func TakeBatchSlotAtHead(batch *BatchDirector) {
	batch.mu.Lock()
	defer batch.mu.Unlock()

	batch.takeHeadLocked()
}

// AwaitTaskByContextIDWithClock is AwaitTaskByContextID on the clock
// given.
func AwaitTaskByContextIDWithClock(ctx context.Context, director Director, contextID string, since time.Time, clock BatchClock) (*Task, error) {
	return awaitTaskByContextID(ctx, director, contextID, since, clock)
}

// AnswerLostRequest reports whether err is a DirectorCallError whose cause
// is a lost answer to a request that starts a read-only task, and names
// that request's method and path.
func AnswerLostRequest(err error) (string, string, bool) {
	var callErr *DirectorCallError
	if !errors.As(err, &callErr) {
		return "", "", false
	}

	var lost *answerLostError
	if !errors.As(callErr.Cause, &lost) {
		return "", "", false
	}

	return lost.Method, lost.Path, true
}

// ConvertVMInfo converts one row of a format=full VM listing, as
// GetDeploymentVMs does.
func ConvertVMInfo(details boshapi.VMDetails) VM {
	return (&DirectorAdapter{log: &noOpLogger{}}).convertVMInfo(details)
}

// ExplainContext runs an adapter's explanation of a failed call on err, as
// the deferred explanation of each director call does, and returns the
// result.
func ExplainContext(ctx context.Context, director Director, operation string, err error) error {
	adapter, ok := director.(*DirectorAdapter)
	if !ok {
		panic(fmt.Sprintf("ExplainContext needs a *DirectorAdapter, got %T", director))
	}

	adapter.explainContext(ctx, operation, &err)

	return err
}

// TaskResultUnreadable builds the error a listing returns when the result
// of its finished task does not parse.
func TaskResultUnreadable(resource, deployment string, taskID int, err error) error {
	return &taskResultError{resource: resource, deployment: deployment, director: "https://127.0.0.1:25555", taskID: taskID, err: err}
}

// DialSSHSession connects to a session's first host the way SSHCommand
// does.
func DialSSHSession(ctx context.Context, director Director, session *sshconn.Session) (*ssh.Client, error) {
	adapter, ok := director.(*DirectorAdapter)
	if !ok {
		panic(fmt.Sprintf("DialSSHSession needs a *DirectorAdapter, got %T", director))
	}

	return adapter.createSSHClient(ctx, session)
}

// SetLateSSHCleanupTimeout shortens an adapter's bound on the send of the
// cleanup of a setup that ended done after the SSH shutdown began.
func SetLateSSHCleanupTimeout(director Director, timeout time.Duration) {
	adapter, ok := director.(*DirectorAdapter)
	if !ok {
		panic(fmt.Sprintf("SetLateSSHCleanupTimeout needs a *DirectorAdapter, got %T", director))
	}

	adapter.lateCleanupTimeout = timeout
}

// ExtractLogsFromTarGz exposes extractLogsFromTarGz to package bosh_test,
// so a test can stream a large bundle without a director.
func ExtractLogsFromTarGz(data io.Reader, budget *LogBudget) (*LogBundle, error) {
	return extractLogsFromTarGz(data, budget)
}
