package broker_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	internalVault "blacksmith/internal/vault"

	"github.com/fivetwenty-io/osbapi/v2/pkg/osbapi"
)

// garbleStagedParameters spoils the staged parameters in the record of
// instanceID, so the staged values don't decode, while the rollback they
// hold still reads.
func garbleStagedParameters(ctx context.Context, t *testing.T, fixture *updateFixture, instanceID string) {
	t.Helper()

	record := readTaskRecord(ctx, t, fixture.vault, instanceID)

	staged, _ := record[internalVault.TaskRecordUpdateKey].(map[string]interface{})
	if staged == nil || staged["rollback"] == nil {
		t.Fatalf("the record holds no staged rollback to keep: %v", record)
	}

	staged["parameters"] = "not parameters"

	err := fixture.vault.Put(ctx, instanceID+"/task", record)
	if err != nil {
		t.Fatalf("failed to garble the staged parameters: %v", err)
	}
}

// savedManifestField is the field of the record saved for a rollback
// that holds the manifest.
const savedManifestField = "manifest"

// savedDigestField is the field of the record saved for a rollback that
// holds the manifest's digest.
const savedDigestField = "digest"

// savedOtherManifest saves a manifest for the rollback that doesn't match
// the digest saved with it.
func savedOtherManifest(ctx context.Context, t *testing.T, fixture *updateFixture, instanceID string) {
	t.Helper()

	err := fixture.vault.Put(ctx, instanceID+"/update_rollback_manifest", map[string]interface{}{
		savedManifestField: "name: other\n", savedDigestField: "not the rollback's digest",
	})
	if err != nil {
		t.Fatalf("failed to save the other manifest: %v", err)
	}
}

// savedUnnamedManifest saves a manifest for the rollback that matches its
// own digest, and is not the one the rollback's digest names.
func savedUnnamedManifest(ctx context.Context, t *testing.T, fixture *updateFixture, instanceID string) {
	t.Helper()

	other := "name: other\n"
	sum := sha256.Sum256([]byte(other))

	err := fixture.vault.Put(ctx, instanceID+"/update_rollback_manifest", map[string]interface{}{
		savedManifestField: other, savedDigestField: hex.EncodeToString(sum[:]),
	})
	if err != nil {
		t.Fatalf("failed to save the unnamed manifest: %v", err)
	}
}

// A restart in the middle of a rollback, on a record whose staged values
// no longer decode, still re-attaches to the rollback, because the
// rollback reads apart from the rest of the staged values, and the
// manifest saved for it matches its digest. The rollback settles as it
// would have, rolled_back with the instance usable or rollback_failed
// with it unusable, and it is never sent again. A saved manifest that
// doesn't match the rollback's digest leaves the broker unable to vouch
// for the rollback, so it settles as rollback_failed whatever its task
// did. Attention required lists the update unless the rollback restored
// the deployment, which leaves it on the stored values.
func TestReadopt_ResumesRollback_StagedValuesLost(t *testing.T) {
	t.Parallel()

	for _, rollbackCase := range []struct {
		name          string
		record        func(ctx context.Context, t *testing.T, fixture *updateFixture, instanceID string)
		saved         func(ctx context.Context, t *testing.T, fixture *updateFixture, instanceID string)
		rollbackState string
		clock         func() time.Time
		outcome       string
		rollbackTask  int
		attention     string
	}{
		{"its rollback task restores the deployment", recordedRollbackTask, savedRollbackManifest, outcomeTaskDone, nil, changeRolledBack, rollbackTaskID, ""},
		{"its rollback task fails", recordedRollbackTask, savedRollbackManifest, outcomeTaskError, nil, changeRollbackFail, rollbackTaskID, stagedLostAttention},
		{"its rollback's deploy never started", recordedRollbackLostAnswer, savedRollbackManifest, outcomeTaskDone, pastGraceClock(), changeRollbackFail, 0, stagedLostAttention},
		{"its saved manifest doesn't match its digest", recordedRollbackTask, savedOtherManifest, outcomeTaskDone, nil, changeRollbackFail, rollbackTaskID, stagedLostAttention},
		{"its saved manifest isn't the one the rollback names", recordedRollbackTask, savedUnnamedManifest, outcomeTaskDone, nil, changeRollbackFail, rollbackTaskID, stagedLostAttention},
	} {
		t.Run(rollbackCase.name, func(t *testing.T) {
			t.Parallel()

			fixture := newUpdateFixture(t)
			ctx := context.Background()
			instanceID := fixture.provisioned(ctx, t, openUpdatePlan, lastGoodParams())

			fixture.rollbackOn(ctx, t, true)
			interruptedUpdate(ctx, t, fixture, instanceID)
			recordedDeployTask(ctx, t, fixture, instanceID)
			rollbackCase.saved(ctx, t, fixture, instanceID)
			rollbackCase.record(ctx, t, fixture, instanceID)
			garbleStagedParameters(ctx, t, fixture, instanceID)

			(&lostAnswer{}).script(fixture, nil, 0)

			script := &readoptRollbackScript{}
			script.script(fixture, rollbackCase.rollbackState)

			if rollbackCase.clock != nil {
				fixture.broker.SetUnknownOutcomeClock(rollbackCase.clock)
			}

			err := fixture.broker.ReadoptInFlightOperations(ctx)
			if err != nil {
				t.Fatalf("re-adoption failed: %v", err)
			}

			requireRollbackSettled(ctx, t, fixture, instanceID, script, rollbackCase.outcome, rollbackCase.rollbackTask, rollbackCase.attention)
		})
	}
}

// requireRollbackSettled fails t unless the re-adopted rollback of
// instanceID settled as outcome, naming rollbackTask, sent no deploy, and
// left the Attention required entry that attention names, or none, both
// at once and at the next start.
func requireRollbackSettled(ctx context.Context, t *testing.T, fixture *updateFixture, instanceID string, script *readoptRollbackScript, outcome string, rollbackTask int, attention string) {
	t.Helper()

	answer := fixture.settled(ctx, t, instanceID)

	if outcome == changeRollbackFail {
		requireRollbackFailed(t, answer)
	} else if answer.State != osbapi.StateFailed || answer.InstanceUsable == nil || !*answer.InstanceUsable || !strings.Contains(answer.Description, "restored the previous deployment") {
		t.Fatalf("the rolled back update was answered %+v, want failed, usable, and saying the previous deployment was restored", answer)
	}

	if sent := script.sent(); len(sent) != 0 {
		t.Fatalf("re-adoption sent %d deploys, want none", len(sent))
	}

	records := fixture.updateRecords(ctx, t)
	if len(records) != 1 || records[0].Outcome != outcome || records[0].TaskID != updateTaskID || records[0].RollbackTaskID != rollbackTask {
		t.Fatalf("the change records are %+v, want one %s naming task 77 and rollback task %d", records, outcome, rollbackTask)
	}

	if rollback := internalVault.StagedRollback(readTaskRecord(ctx, t, fixture.vault, instanceID)); rollback == nil || rollback.Phase != outcome {
		t.Fatalf("the staged rollback is %+v, want the phase %s", rollback, outcome)
	}

	requireUpdateAttention(t, fixture.broker, instanceID, attention, "once the rollback settled")
	requireUpdateAttention(t, startedBroker(ctx, t, fixture, updatePlans()), instanceID, attention, "at the next start")
	fixture.requireNoSavedManifest(ctx, t, instanceID)
	fixture.requireReleased(t, instanceID)
}
