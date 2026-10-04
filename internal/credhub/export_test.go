package credhub

import (
	"context"
	"time"

	"blacksmith/pkg/logger"
)

// ExplainFailure exposes explainFailure to package credhub_test.
func ExplainFailure(operation string, err error) (string, string) {
	return explainFailure(operation, err)
}

// SetCleanerBackoff replaces the waits between retry attempts so tests do
// not sleep for seconds.
func SetCleanerBackoff(cleaner *Cleaner, delays ...time.Duration) {
	cleaner.backoff = delays
}

// ProbeWithin runs the startup probe under timeout instead of ProbeTimeout,
// so a test of the deadline does not wait thirty seconds.
func ProbeWithin(ctx context.Context, client *Client, directorName string, log logger.Logger, timeout time.Duration) error {
	return probeWithin(ctx, client, directorName, log, timeout)
}
