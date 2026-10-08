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
type BatchClock = batchClock

// How often a watcher looks for the task of a lost answer, how long it
// keeps a final task for waiters, and how often it logs a task it still
// follows.
const (
	BatchLostAnswerInterval   = batchLostAnswerInterval
	BatchDonePrune            = batchDonePrune
	BatchFollowReportInterval = batchFollowReportInterval
)

// SetBatchClock replaces the time source of a batch director's watchers.
// It is set before the batch director starts any deploy.
func SetBatchClock(batch *BatchDirector, clock BatchClock) {
	batch.clock = clock
}

// SetBatchWatchMax shortens how long a watcher reads a task the director
// does not answer for. It is set before the batch director starts any
// deploy.
func SetBatchWatchMax(batch *BatchDirector, watchMax time.Duration) {
	batch.watchMax = watchMax
}

// BatchWatchersRunning reports how many watcher goroutines have not
// returned.
func BatchWatchersRunning(batch *BatchDirector) int {
	batch.mu.Lock()
	defer batch.mu.Unlock()

	return batch.running
}

// BatchWatchesHeld reports how many watchers a batch director holds by
// task ID and by context ID.
func BatchWatchesHeld(batch *BatchDirector) (int, int) {
	batch.mu.Lock()
	defer batch.mu.Unlock()

	return len(batch.byTask), len(batch.byContextID)
}
