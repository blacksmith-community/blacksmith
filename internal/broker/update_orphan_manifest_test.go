package broker_test

import (
	"context"
	"testing"
)

// A restart between the write that settles an update and the delete of
// its saved rollback manifest leaves the manifest behind under a settled
// record, which re-adoption skips. Re-adoption deletes it before the
// servers start, whichever way the update settled.
func TestReadopt_SettledUpdate_DeletesItsSavedManifest(t *testing.T) {
	t.Parallel()

	for name, settle := range map[string]func(ctx context.Context, fixture *updateFixture, instanceID string) error{
		changeSucceeded: func(ctx context.Context, fixture *updateFixture, instanceID string) error {
			return fixture.vault.TrackSucceeded(ctx, instanceID, updateFailureAction, "Updated by BOSH deployment task 77", updateTaskID)
		},
		outcomeStateFailed: func(ctx context.Context, fixture *updateFixture, instanceID string) error {
			return fixture.vault.TrackFailed(ctx, instanceID, updateFailureAction, "BOSH deployment task 77 failed", readoptUpdateContextID, updateTaskID)
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fixture := newUpdateFixture(t)
			ctx := context.Background()
			instanceID := fixture.provisioned(ctx, t, openUpdatePlan, lastGoodParams())

			interruptedUpdate(ctx, t, fixture, instanceID)
			recordedDeployTask(ctx, t, fixture, instanceID)

			err := settle(ctx, fixture, instanceID)
			if err != nil {
				t.Fatalf("failed to settle the update: %v", err)
			}

			savedRollbackManifest(ctx, t, fixture, instanceID)

			err = fixture.broker.ReadoptInFlightOperations(ctx)
			if err != nil {
				t.Fatalf("re-adoption failed: %v", err)
			}

			fixture.requireNoSavedManifest(ctx, t, instanceID)
			fixture.broker.WaitForBackground(ctx)
		})
	}
}
