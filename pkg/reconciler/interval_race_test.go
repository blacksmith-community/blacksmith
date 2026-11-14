package reconciler_test

import (
	"sync"
	"testing"
	"time"

	. "blacksmith/pkg/reconciler"
	"blacksmith/pkg/testutil"
)

// The settings endpoint changes and reads the interval from separate
// request goroutines, so GetInterval must not race UpdateInterval. Run
// with -race to catch a regression.
func TestManagerIntervalConcurrentUpdateAndRead(t *testing.T) {
	t.Parallel()

	manager := NewReconcilerManager(newTestManagerConfig(), nil, nil, testutil.NewScriptedBOSHDirector(), NewMockLogger(), nil)

	const rounds = 200

	var waitGroup sync.WaitGroup

	waitGroup.Go(func() {
		for i := range rounds {
			manager.UpdateInterval(time.Duration(i+1) * time.Minute)
		}
	})

	waitGroup.Go(func() {
		for range rounds {
			if manager.GetInterval() <= 0 {
				t.Error("GetInterval returned a non-positive interval")

				return
			}
		}
	})

	waitGroup.Wait()

	if got := manager.GetInterval(); got != rounds*time.Minute {
		t.Errorf("interval = %v, want %v", got, rounds*time.Minute)
	}
}
