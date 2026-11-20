package vault_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"blacksmith/internal/vault"
)

// unknownUpdate records an accepted update of a new instance whose
// deploy answer, sent with contextID, was lost, and returns the
// instance's ID. When rollingBack is true, the update's deploy failed in
// task 42 first, and the lost answer is its rollback's.
func unknownUpdate(ctx context.Context, t *testing.T, client *vault.Vault, contextID string, rollingBack bool) string {
	t.Helper()

	instanceID := fmt.Sprintf("settle-pending-%d", time.Now().UnixNano())

	err := recordSucceededMintedProvision(ctx, client, instanceID)
	if err == nil {
		err = client.TrackUpdateAccepted(ctx, instanceID, "accepted", updateContextID, "index-name", stagedUpdate())
	}

	if err == nil && rollingBack {
		err = client.TrackUpdateTask(ctx, instanceID, "BOSH deployment in progress (task 42)", updateContextID, 42)
		if err == nil {
			err = client.TrackRollingBack(ctx, instanceID, "rolling back", vault.UpdateRollback{FailedTaskID: 42, FailedContextID: updateContextID, ContextID: contextID})
		}
	}

	if err == nil {
		err = client.TrackOutcomeUnknown(ctx, instanceID, "update", "the answer was lost", contextID, time.Now())
	}

	if err != nil {
		t.Fatal(err)
	}

	return instanceID
}

// The resolver's failure of an update whose deploy never started marks
// the record as still owing the update's settle, in the same write, so a
// restart before the update settles finds it. Only the settle of that
// update, under the same context ID, takes the mark off, and every other
// field stays.
func TestSettleOutcomeUnknownSettlePending_MarksUntilTheUpdateSettles(t *testing.T) {
	t.Parallel()

	client := newDevVault(t)
	ctx := context.Background()

	instanceID := unknownUpdate(ctx, t, client, updateContextID, false)

	err := client.SettleOutcomeUnknownSettlePending(ctx, instanceID, "never started", updateContextID)
	if err != nil {
		t.Fatalf("settling the unknown outcome was refused: %v", err)
	}

	state, task, record := readRecord(ctx, t, client, instanceID)
	if state != stateFailed || task != -1 || record[vault.TaskRecordSettlePendingKey] != true || record[vault.TaskRecordUpdateKey] == nil {
		t.Fatalf("the settled outcome is %s, task %d, record %v, want failed -1, marked, with the staged values", state, task, record)
	}

	err = client.SettleOutcomeUnknownSettlePending(ctx, instanceID, "never started", updateContextID)
	if !errors.Is(err, vault.ErrOutcomeUnknownSettled) {
		t.Fatalf("settling a settled outcome answered %v, want ErrOutcomeUnknownSettled", err)
	}

	err = client.ClearSettlePending(ctx, instanceID, "context-other", "")
	if !errors.Is(err, vault.ErrTaskRecordReplaced) {
		t.Fatalf("clearing the mark under another context ID answered %v, want ErrTaskRecordReplaced", err)
	}

	err = client.ClearSettlePending(ctx, instanceID, updateContextID, "never started, and the plan is gone")
	if err != nil {
		t.Fatalf("clearing the mark was refused: %v", err)
	}

	state, _, record = readRecord(ctx, t, client, instanceID)
	if _, marked := record[vault.TaskRecordSettlePendingKey]; marked || state != stateFailed || record[vault.TaskRecordUpdateKey] == nil || record["description"] != "never started, and the plan is gone" {
		t.Fatalf("after the clear, the record is %s %v, want failed, unmarked, with the staged values and the new description", state, record)
	}

	err = client.ClearSettlePending(ctx, instanceID, updateContextID, "")
	if !errors.Is(err, vault.ErrTaskRecordReplaced) {
		t.Fatalf("clearing a mark already cleared answered %v, want ErrTaskRecordReplaced", err)
	}
}

// The settle of a rollback whose deploy never started takes the mark off
// in the write that settles it.
func TestSettleUpdateFailure_ClearsTheSettleMark(t *testing.T) {
	t.Parallel()

	client := newDevVault(t)
	ctx := context.Background()

	instanceID := unknownUpdate(ctx, t, client, "context-rollback", true)

	err := client.SettleOutcomeUnknownSettlePending(ctx, instanceID, "never started", "context-rollback")
	if err != nil {
		t.Fatalf("settling the unknown outcome was refused: %v", err)
	}

	err = client.SettleUpdateFailure(ctx, instanceID, vault.TaskStateRollbackFailed, "rollback failed", "context-rollback", vault.UpdateRollback{
		FailedTaskID: 42, FailedContextID: updateContextID, ContextID: "context-rollback", Phase: vault.UpdatePhaseRollbackFailed,
	})
	if err != nil {
		t.Fatalf("settling the rollback was refused: %v", err)
	}

	state, _, record := readRecord(ctx, t, client, instanceID)
	if _, marked := record[vault.TaskRecordSettlePendingKey]; marked || state != vault.TaskStateRollbackFailed {
		t.Fatalf("after the rollback settled, the record is %s %v, want rollback_failed and unmarked", state, record)
	}
}

// The description that takes the settle mark off reaches the history, as
// the last entry of the update's failure, and moves updated_at, so the
// operator's view of the instance shows the full text.
func TestClearSettlePending_DescriptionReachesTheHistory(t *testing.T) {
	t.Parallel()

	client := newDevVault(t)
	ctx := context.Background()

	instanceID := unknownUpdate(ctx, t, client, updateContextID, false)

	err := client.SettleOutcomeUnknownSettlePending(ctx, instanceID, "never started", updateContextID)
	if err != nil {
		t.Fatalf("settling the unknown outcome was refused: %v", err)
	}

	_, _, record := readRecord(ctx, t, client, instanceID)
	record["updated_at"] = 1

	err = client.Put(ctx, instanceID+"/task", record)
	if err != nil {
		t.Fatal(err)
	}

	const full = "never started, and the plan is gone"

	err = client.ClearSettlePending(ctx, instanceID, updateContextID, full)
	if err != nil {
		t.Fatalf("clearing the mark was refused: %v", err)
	}

	_, _, record = readRecord(ctx, t, client, instanceID)
	if fmt.Sprint(record["updated_at"]) == "1" {
		t.Fatalf("after the clear, updated_at is %v, want it moved", record["updated_at"])
	}

	var metadata map[string]interface{}

	_, err = client.Get(ctx, instanceID+"/metadata", &metadata)
	if err != nil {
		t.Fatal(err)
	}

	history, _ := metadata["history"].([]interface{})
	if len(history) == 0 {
		t.Fatalf("the history is empty: %v", metadata)
	}

	last, _ := history[len(history)-1].(map[string]interface{})
	if last["description"] != full || last["state"] != stateFailed || last["action"] != record["action"] {
		t.Fatalf("the last history entry is %v, want the update's failure saying %q", last, full)
	}
}
