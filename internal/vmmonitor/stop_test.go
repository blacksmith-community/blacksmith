package vmmonitor_test

import (
	"testing"

	"blacksmith/internal/vmmonitor"
)

// Shutdown stops the monitor whether or not it started, and it does not
// start when VM monitoring is disabled, so a stop before any start must
// return without a panic.
func TestMonitorStopsWithoutHavingStarted(t *testing.T) {
	t.Parallel()

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("stopping a monitor that never started panicked: %v", r)
		}
	}()

	vmmonitor.NewWithVault(nil, nil, nil).Stop()
}
