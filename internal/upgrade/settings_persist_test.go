//nolint:testpackage // the tests read the manager's job semaphore
package upgrade

import (
	"context"
	"errors"
	"testing"

	"blacksmith/pkg/logger"
)

var errSettingsVaultDown = errors.New("vault is sealed")

// settingsFailVault is a waitVault whose save of the upgrade settings fails.
type settingsFailVault struct {
	*waitVault
}

func (v *settingsFailVault) Put(ctx context.Context, path string, data interface{}) error {
	if path == upgradeSettingsVaultPath {
		return errSettingsVaultDown
	}

	return v.waitVault.Put(ctx, path, data) //nolint:wrapcheck // the fake passes the inner fake's answer through
}

// A failed save of the batch settings leaves the running settings, the job
// semaphore, and the BatchDirector pool as they were, so nothing takes
// effect that Vault does not hold.
func TestUpdateSettingsChangesNothingWhenTheSaveFails(t *testing.T) {
	t.Parallel()

	manager := NewManager(&logger.NoOpLogger{}, noCallDirector{}, &settingsFailVault{waitVault: newWaitVault()})
	before := manager.GetSettings()
	semaphore := manager.jobSemaphore

	var notified []int

	manager.SetOnMaxBatchJobsChanged(func(maxJobs int) { notified = append(notified, maxJobs) })

	err := manager.UpdateSettings(context.Background(), Settings{MaxBatchJobs: before.MaxBatchJobs + 18})
	if !errors.Is(err, errSettingsVaultDown) {
		t.Fatalf("UpdateSettings error = %v, want the Vault failure", err)
	}

	if got := manager.GetSettings(); got != before {
		t.Fatalf("running settings = %+v after a failed save, want %+v", got, before)
	}

	if manager.jobSemaphore != semaphore {
		t.Fatal("the job semaphore was resized after a failed save")
	}

	if len(notified) != 0 {
		t.Fatalf("the pool was told %v after a failed save, want nothing", notified)
	}
}

// A save that lands applies the settings and tells the pool.
func TestUpdateSettingsAppliesASavedChange(t *testing.T) {
	t.Parallel()

	manager := NewManager(&logger.NoOpLogger{}, noCallDirector{}, newWaitVault())

	var notified []int

	manager.SetOnMaxBatchJobsChanged(func(maxJobs int) { notified = append(notified, maxJobs) })

	err := manager.UpdateSettings(context.Background(), Settings{MaxBatchJobs: 7})
	if err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}

	if got := manager.GetSettings().MaxBatchJobs; got != 7 {
		t.Fatalf("max_batch_jobs = %d, want 7", got)
	}

	if cap(manager.jobSemaphore) != 7 {
		t.Fatalf("job semaphore holds %d, want 7", cap(manager.jobSemaphore))
	}

	if len(notified) != 1 || notified[0] != 7 {
		t.Fatalf("pool told %v, want [7]", notified)
	}
}
