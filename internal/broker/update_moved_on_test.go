package broker_test

import (
	"context"
	"testing"

	"blacksmith/internal/bosh"

	"github.com/fivetwenty-io/osbapi/v2/pkg/osbapi"
)

// A record that has moved on by the time the rollback would be recorded
// sends no rollback, and the failed deploy still leaves its change record
// and deletes the saved manifest, so neither is lost for good.
func TestUpdate_RecordMovedOnBeforeRollback_LeavesChangeRecord(t *testing.T) {
	t.Parallel()

	fixture := newUpdateFixture(t)
	ctx := context.Background()
	fixture.rollbackOn(ctx, t, true)

	script := &rollbackScript{}
	script.script(fixture, outcomeTaskDone)

	var instanceID string

	fixture.director.WaitForTaskFn = func(taskID int) (*bosh.Task, error) {
		// Something else settles the record before the update's
		// goroutine sees its task fail.
		record := readTaskRecord(ctx, t, fixture.vault, instanceID)
		contextID, _ := record["context_id"].(string)

		err := fixture.vault.TrackFailed(ctx, instanceID, updateFailureAction, "Settled elsewhere", contextID, taskID)
		if err != nil {
			t.Errorf("failed to settle the record elsewhere: %v", err)
		}

		return &bosh.Task{ID: taskID, State: outcomeTaskError}, nil
	}

	instanceID = fixture.provisioned(ctx, t, openUpdatePlan, lastGoodParams())

	response, async, err := fixture.broker.Update(ctx, instanceID, failingUpdateRequest(), true)
	requireAccepted(t, response, async, err)

	if answer := fixture.settled(ctx, t, instanceID); answer.State != osbapi.StateFailed {
		t.Fatalf("the update settled as %+v, want failed", answer)
	}

	if sent := script.sent(); len(sent) != 1 {
		t.Fatalf("the update sent %d deploys, want only its own", len(sent))
	}

	if records := fixture.updateRecords(ctx, t); len(records) != 1 || records[0].Outcome != outcomeStateFailed || records[0].TaskID != updateTaskID {
		t.Fatalf("the change records are %+v, want one failed update naming task 77", records)
	}

	fixture.requireNoSavedManifest(ctx, t, instanceID)
	fixture.requireReleased(t, instanceID)
}
