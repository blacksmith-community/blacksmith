package manifest

import (
	"testing"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/services"
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

// CreateJobFromVM exposes the job the credentials manifest builds for one
// VM.
func CreateJobFromVM(instance bosh.VM, plan services.Plan, deployment, networkDNS string) services.Job {
	return createJobFromVM(instance, plan, deployment, networkDNS)
}
