package bosh

import (
	"time"

	boshdirector "github.com/cloudfoundry/bosh-cli/v7/director"
	"github.com/fivetwenty-io/boshapi/pkg/boshapi"
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
