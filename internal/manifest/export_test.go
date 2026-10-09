package manifest

import (
	"testing"
	"time"
)

// SetReleasePollTimes shortens how often, and for how long, a provision
// lists the director's releases after an upload's answer was lost, until
// the test ends. A test that calls it must not run in parallel.
func SetReleasePollTimes(t *testing.T, interval, deadline time.Duration) {
	t.Helper()

	oldInterval, oldDeadline := releasePollInterval, releasePollDeadline
	releasePollInterval, releasePollDeadline = interval, deadline

	t.Cleanup(func() {
		releasePollInterval, releasePollDeadline = oldInterval, oldDeadline
	})
}
