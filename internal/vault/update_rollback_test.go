package vault_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"blacksmith/internal/vault"
)

// The context IDs of the update's deploy and of its rollback, and the
// digest the specs record.
const (
	updateContextID   = "update-context"
	rollbackContextID = "rollback-context"
	stagedDigest      = "digest"
)

// failedUpdateRecord records an update of a provisioned instance whose
// deploy, task 77 with context ID update-context, is in flight.
func failedUpdateRecord(ctx context.Context, t *testing.T, client *vault.Vault, name string) string {
	t.Helper()

	instanceID := fmt.Sprintf("update-rollback-%s-%d", name, time.Now().UnixNano())

	err := recordSucceededMintedProvision(ctx, client, instanceID)
	if err == nil {
		err = client.TrackUpdateAccepted(ctx, instanceID, "Update request accepted", updateContextID, "", stagedUpdate())
	}

	if err == nil {
		err = client.TrackProgress(ctx, instanceID, "update", "BOSH deployment in progress (task 77)", 77, nil)
	}

	if err != nil {
		t.Fatalf("failed to record the update: %v", err)
	}

	return instanceID
}

// readUpdateRecord reads an instance's task record.
func readUpdateRecord(ctx context.Context, t *testing.T, client *vault.Vault, instanceID string) map[string]interface{} {
	t.Helper()

	var record map[string]interface{}

	_, err := client.Get(ctx, instanceID+"/task", &record)
	if err != nil {
		t.Fatal(err)
	}

	return record
}

// A rollback moves the record to the rollback's context ID with the
// action still update, and keeps the staged values, the deployment pair,
// and the failed deploy's task and context ID. Its settle records the
// end state with the rollback beside the staged values.
func TestTrackRollingBack_KeepsTheUpdateAndRecordsBothDeploys(t *testing.T) {
	t.Parallel()

	client := newDevVault(t)
	ctx := context.Background()
	instanceID := failedUpdateRecord(ctx, t, client, "both")

	rollback := vault.UpdateRollback{
		Setting: true, SettingSource: "Vault settings", FailedTaskID: 77, FailedContextID: updateContextID,
		ContextID: rollbackContextID, ManifestDigest: stagedDigest,
	}

	err := client.TrackRollingBack(ctx, instanceID, "Redeploying the previous manifest", rollback)
	if err != nil {
		t.Fatalf("the rollback was not recorded: %v", err)
	}

	record := readUpdateRecord(ctx, t, client, instanceID)
	staged, _ := record[vault.TaskRecordUpdateKey].(map[string]interface{})
	recorded, _ := staged["rollback"].(map[string]interface{})

	if record["action"] != "update" || record["context_id"] != rollbackContextID || record["deployment_name"] != updatePlan+"-"+instanceID ||
		staged["fingerprint"] != stagedDigest || recorded["phase"] != vault.UpdatePhaseRollingBack || recorded["failed_context_id"] != updateContextID {
		t.Fatalf("the rolling-back record is %v", record)
	}

	err = client.TrackProgress(ctx, instanceID, "update", "Redeploying the previous manifest (task 78)", 78, nil)
	if err == nil {
		rollback.Phase, rollback.TaskID = vault.UpdatePhaseRollbackFailed, 78
		err = client.SettleUpdateFailure(ctx, instanceID, vault.TaskStateRollbackFailed, "Both failed", rollbackContextID, rollback)
	}

	if err != nil {
		t.Fatalf("the failed rollback was not recorded: %v", err)
	}

	requireRollbackSettled(ctx, t, client, instanceID)
}

// requireRollbackSettled fails the spec unless the record holds the
// failed rollback of task 78, still names the failed deploy's task 77 as
// its task, and refuses a write that holds the update unsettled.
func requireRollbackSettled(ctx context.Context, t *testing.T, client *vault.Vault, instanceID string) {
	t.Helper()

	record := readUpdateRecord(ctx, t, client, instanceID)
	staged, _ := record[vault.TaskRecordUpdateKey].(map[string]interface{})
	recorded, _ := staged["rollback"].(map[string]interface{})

	if record["state"] != vault.TaskStateRollbackFailed || recorded["phase"] != vault.UpdatePhaseRollbackFailed || staged["plan_id"] != updatePlan {
		t.Fatalf("the settled record is %v", record)
	}

	if task := fmt.Sprint(record["task"]); task != "77" {
		t.Fatalf("the settled record names task %s, want the failed deploy's task 77", task)
	}

	if read := vault.StagedRollback(record); read == nil || read.TaskID != 78 || read.FailedTaskID != 77 || read.ContextID != rollbackContextID {
		t.Fatalf("the staged rollback reads as %+v", read)
	}

	// A failed rollback is settled, so a write that holds the update
	// unsettled finds nothing to write over.
	err := client.TrackFailed(ctx, instanceID, "update", "late", rollbackContextID, -1)
	if err == nil {
		t.Fatal("a write over a failed rollback landed, want it refused as settled")
	}
}

// A rollback write over a record that no longer holds the update's
// deploy in flight writes nothing.
func TestTrackRollingBack_RefusesASettledOrReplacedRecord(t *testing.T) {
	t.Parallel()

	client := newDevVault(t)
	ctx := context.Background()

	for name, contextID := range map[string]string{"another context": "other-context", "settled": updateContextID, "another task": updateContextID} {
		instanceID := failedUpdateRecord(ctx, t, client, "refused")

		if name == "settled" {
			err := client.TrackFailed(ctx, instanceID, "update", "BOSH deployment task 77 is error", updateContextID, 77)
			if err != nil {
				t.Fatal(err)
			}
		}

		failedTask := 77
		if name == "another task" {
			failedTask = 76
		}

		before := readUpdateRecord(ctx, t, client, instanceID)

		err := client.TrackRollingBack(ctx, instanceID, "Redeploying", vault.UpdateRollback{FailedTaskID: failedTask, FailedContextID: contextID, ContextID: rollbackContextID})
		if !errors.Is(err, vault.ErrUpdateNotInFlight) {
			t.Fatalf("%s: the rollback write returned %v, want ErrUpdateNotInFlight", name, err)
		}

		if after := readUpdateRecord(ctx, t, client, instanceID); fmt.Sprint(after) != fmt.Sprint(before) {
			t.Fatalf("%s: the refused write changed the record to %v", name, after)
		}
	}
}
