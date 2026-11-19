package broker_test

import (
	"context"
	"strings"
	"testing"

	internalVault "blacksmith/internal/vault"

	"github.com/fivetwenty-io/osbapi/v2/pkg/osbapi"
)

// garbleStagedUpdate replaces the staged update in the record of
// instanceID with a value that doesn't decode.
func garbleStagedUpdate(ctx context.Context, t *testing.T, fixture *updateFixture, instanceID string) {
	t.Helper()

	record := readTaskRecord(ctx, t, fixture.vault, instanceID)
	record[internalVault.TaskRecordUpdateKey] = "not a staged update"

	err := fixture.vault.Put(ctx, instanceID+"/task", record)
	if err != nil {
		t.Fatalf("failed to garble the staged update: %v", err)
	}
}

// An interrupted update that re-adoption can't carry through, because the
// plan it stages has left the catalog or its staged values don't decode,
// is still followed to the end of its task under the claim. It then
// settles as failed and repeatable with a reason, commits nothing, and
// frees the instance for the next update.
func TestReadopt_UnresumableUpdate_SettlesFailed(t *testing.T) {
	t.Parallel()

	for _, unresumable := range []struct {
		name   string
		spoil  func(ctx context.Context, t *testing.T, fixture *updateFixture, instanceID string)
		reason string
	}{
		{"its plan left the catalog", func(_ context.Context, _ *testing.T, fixture *updateFixture, _ string) {
			delete(fixture.broker.Plans, recordedNameService+"/"+openUpdatePlan)
		}, "no longer in the catalog"},
		{"its staged values don't decode", garbleStagedUpdate, "can't read"},
	} {
		t.Run(unresumable.name, func(t *testing.T) {
			t.Parallel()

			fixture := newUpdateFixture(t)
			ctx := context.Background()
			plans := updatePlans()
			instanceID := fixture.provisioned(ctx, t, openUpdatePlan, lastGoodParams())

			interruptedUpdate(ctx, t, fixture, instanceID)
			recordedDeployTask(ctx, t, fixture, instanceID)
			savedRollbackManifest(ctx, t, fixture, instanceID)
			unresumable.spoil(ctx, t, fixture, instanceID)

			script := &deployScript{}
			script.script(fixture, outcomeTaskDone)

			held := fixture.holdTask(updateTaskID)

			err := fixture.broker.ReadoptInFlightOperations(ctx)
			if err != nil {
				t.Fatalf("re-adoption failed: %v", err)
			}

			fixture.requireHeldPastBound(ctx, t, held, instanceID)
			close(held.release)

			answer := fixture.settled(ctx, t, instanceID)
			if answer.State != osbapi.StateFailed || !strings.Contains(answer.Description, unresumable.reason) || !strings.Contains(answer.Description, "77") {
				t.Fatalf("the update re-adoption can't resume settled as %+v, want failed, naming task 77 and saying why", answer)
			}

			requireLastGood(ctx, t, fixture, instanceID, lastGoodParams(), answer)
			fixture.requireNoSavedManifest(ctx, t, instanceID)
			fixture.requireReleased(t, instanceID)

			if records := fixture.updateRecords(ctx, t); len(records) != 1 || records[0].Outcome != outcomeStateFailed || records[0].TaskID != updateTaskID {
				t.Fatalf("the change records are %+v, want one failed update naming task 77", records)
			}

			if calls := fixture.director.Calls("UpdateDeployment"); len(calls) != 0 {
				t.Fatalf("re-adoption sent %d deploys, want none", len(calls))
			}

			fixture.broker.Plans = plans

			// The broker has stopped once settled waited for it, so the
			// accepted update's goroutine ends at once, and the spec only
			// checks that the instance takes the update.
			response, async, err := fixture.broker.Update(ctx, instanceID, failingUpdateRequest(), true)
			requireAccepted(t, response, async, err)
		})
	}
}
