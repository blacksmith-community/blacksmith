package broker_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/settings"
	internalVault "blacksmith/internal/vault"

	"github.com/fivetwenty-io/osbapi/v2/pkg/osbapi"
)

// readoptRollbackContextID is the context ID of the rollback a restart
// interrupted.
const readoptRollbackContextID = "readopt-rollback-context"

// rollbackTaskPruned scripts a director that no longer has the rollback's
// task.
const rollbackTaskPruned = "pruned"

// readoptRollbackCase is one point at which a restart interrupts an
// update's rollback.
type readoptRollbackCase struct {
	name string
	// record writes what the rollback had recorded when the broker
	// stopped, over the update's failed deploy.
	record        func(ctx context.Context, t *testing.T, fixture *updateFixture, instanceID string)
	found         *bosh.Task
	rollbackState string
	clock         func() time.Time
	deploys       int
	outcome       string
	rollbackTask  int
}

// savedRollbackManifest saves the live manifest for the rollback, as the
// update did before its deploy.
func savedRollbackManifest(ctx context.Context, t *testing.T, fixture *updateFixture, instanceID string) {
	t.Helper()

	sum := sha256.Sum256([]byte(liveManifest))

	err := fixture.vault.Put(ctx, instanceID+"/update_rollback_manifest", map[string]interface{}{
		savedManifestField: liveManifest, savedDigestField: hex.EncodeToString(sum[:]),
	})
	if err != nil {
		t.Fatalf("failed to save the rollback manifest: %v", err)
	}
}

// recordedRollingBack records that the update's deploy, task 77, failed
// and its rollback started.
func recordedRollingBack(ctx context.Context, t *testing.T, fixture *updateFixture, instanceID string) {
	t.Helper()

	sum := sha256.Sum256([]byte(liveManifest))

	err := fixture.vault.TrackRollingBack(ctx, instanceID, "The update failed in BOSH task 77, and the broker is redeploying the previous manifest", internalVault.UpdateRollback{
		Setting: true, SettingSource: settings.SourceVault, FailedTaskID: updateTaskID, FailedContextID: readoptUpdateContextID,
		ContextID: readoptRollbackContextID, ManifestDigest: hex.EncodeToString(sum[:]),
	})
	if err != nil {
		t.Fatalf("failed to record the rollback: %v", err)
	}
}

// recordedRollbackTask records the rollback's task.
func recordedRollbackTask(ctx context.Context, t *testing.T, fixture *updateFixture, instanceID string) {
	t.Helper()

	recordedRollingBack(ctx, t, fixture, instanceID)

	err := fixture.vault.TrackProgress(ctx, instanceID, updateFailureAction, "The update failed in BOSH task 77, and the broker is redeploying the previous manifest (task 78)", rollbackTaskID, nil)
	if err != nil {
		t.Fatalf("failed to record the rollback's task: %v", err)
	}
}

// recordedRollbackLostAnswer records that the rollback deploy's answer
// was lost.
func recordedRollbackLostAnswer(ctx context.Context, t *testing.T, fixture *updateFixture, instanceID string) {
	t.Helper()

	recordedRollingBack(ctx, t, fixture, instanceID)

	err := fixture.vault.TrackOutcomeUnknown(ctx, instanceID, updateFailureAction, "The rollback deploy's answer was lost", readoptRollbackContextID, time.Now())
	if err != nil {
		t.Fatalf("failed to record the lost answer: %v", err)
	}
}

// readoptRollbackScript scripts the director for a re-adopted rollback.
// The update's deploy, task 77, failed, a deploy sent now starts task 78,
// and task 78 ends in state.
type readoptRollbackScript struct {
	mu        sync.Mutex
	manifests []string
}

// script points the fixture's director at the script.
func (r *readoptRollbackScript) script(fixture *updateFixture, state string) {
	fixture.director.GetDeploymentFn = func(name string) (*bosh.DeploymentDetail, error) {
		return &bosh.DeploymentDetail{Name: name, Manifest: liveManifest}, nil
	}
	fixture.director.UpdateDeploymentFn = func(_, manifest string) (*bosh.Task, error) {
		r.mu.Lock()
		defer r.mu.Unlock()

		r.manifests = append(r.manifests, manifest)

		return &bosh.Task{ID: rollbackTaskID, State: outcomeTaskProcessing}, nil
	}
	fixture.director.WaitForTaskFn = func(taskID int) (*bosh.Task, error) {
		switch {
		case taskID == updateTaskID:
			return &bosh.Task{ID: taskID, State: outcomeTaskError}, nil
		case state == rollbackTaskPruned:
			return nil, readoptTaskNotFound(taskID)
		}

		return &bosh.Task{ID: taskID, State: state}, nil
	}
}

// sent returns the manifests re-adoption deployed.
func (r *readoptRollbackScript) sent() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]string(nil), r.manifests...)
}

// requireReadopted fails the spec unless the re-adopted rollback settled
// as the case wants, sent only the deploys the case allows, looked tasks
// up only by the rollback's context ID, and left one change record.
func (c readoptRollbackCase) requireReadopted(ctx context.Context, t *testing.T, fixture *updateFixture, instanceID string, script *readoptRollbackScript, answer osbapi.LastOperationResponse) {
	t.Helper()

	if c.outcome == changeRollbackFail {
		requireRollbackFailed(t, answer)
	} else {
		requireLastGood(ctx, t, fixture, instanceID, lastGoodParams(), answer)
	}

	if c.rollbackTask != 0 && !strings.Contains(answer.Description, "78") {
		t.Fatalf("the re-adopted rollback was answered %q, want its task named", answer.Description)
	}

	if sent := script.sent(); len(sent) != c.deploys || (c.deploys == 1 && sent[0] != liveManifest) {
		t.Fatalf("re-adoption sent %d deploys, want %d of the saved manifest", len(sent), c.deploys)
	}

	for _, contextID := range fixture.director.Calls("FindTaskByContextID") {
		if contextID != readoptRollbackContextID {
			t.Fatalf("re-adoption looked a task up by context ID %q, want the rollback's", contextID)
		}
	}

	if calls := fixture.director.Calls("DeleteDeployment"); len(calls) != 0 {
		t.Fatalf("re-adoption deleted %v", calls)
	}

	records := fixture.updateRecords(ctx, t)
	if len(records) != 1 || records[0].Outcome != c.outcome || records[0].TaskID != updateTaskID ||
		records[0].RollbackTaskID != c.rollbackTask || records[0].Actor != readoptUpdateActor {
		t.Fatalf("the change records are %+v, want one %s naming task 77, rollback task %d, and the user", records, c.outcome, c.rollbackTask)
	}

	fixture.requireNoSavedManifest(ctx, t, instanceID)
}

// A restart in the middle of a rollback re-attaches to the rollback by
// its own context ID and never sends another one. A restart after the
// update's deploy failed and before the rollback was recorded runs the
// one rollback the setting calls for, of the manifest saved before the
// update.
func TestReadopt_ResumesRollback(t *testing.T) {
	t.Parallel()

	running := &bosh.Task{ID: rollbackTaskID, State: outcomeTaskProcessing}

	for _, rollbackCase := range []readoptRollbackCase{
		{"its rollback task restores the deployment", recordedRollbackTask, nil, outcomeTaskDone, nil, 0, changeRolledBack, rollbackTaskID},
		{"its rollback task fails", recordedRollbackTask, nil, outcomeTaskError, nil, 0, changeRollbackFail, rollbackTaskID},
		{"its rollback task was pruned", recordedRollbackTask, nil, rollbackTaskPruned, nil, 0, changeRollbackFail, 0},
		{"its rollback's lost answer names a task", recordedRollbackLostAnswer, running, outcomeTaskDone, nil, 0, changeRolledBack, rollbackTaskID},
		{"it stopped before the rollback's task was recorded", recordedRollingBack, nil, outcomeTaskDone, pastGraceClock(), 0, changeRollbackFail, 0},
		{"it stopped before the rollback was recorded", recordedNothingMore, nil, outcomeTaskDone, nil, 1, changeRolledBack, rollbackTaskID},
	} {
		t.Run(rollbackCase.name, func(t *testing.T) {
			t.Parallel()

			fixture := newUpdateFixture(t)
			ctx := context.Background()
			instanceID := fixture.provisioned(ctx, t, openUpdatePlan, lastGoodParams())

			fixture.rollbackOn(ctx, t, true)
			interruptedUpdate(ctx, t, fixture, instanceID)
			recordedDeployTask(ctx, t, fixture, instanceID)
			savedRollbackManifest(ctx, t, fixture, instanceID)
			rollbackCase.record(ctx, t, fixture, instanceID)

			lost := &lostAnswer{}
			lost.script(fixture, rollbackCase.found, 0)

			script := &readoptRollbackScript{}
			script.script(fixture, rollbackCase.rollbackState)

			if rollbackCase.clock != nil {
				fixture.broker.SetUnknownOutcomeClock(rollbackCase.clock)
			}

			err := fixture.broker.ReadoptInFlightOperations(ctx)
			if err != nil {
				t.Fatalf("re-adoption failed: %v", err)
			}

			fixture.broker.WaitForBackground(ctx)

			if rollbackCase.found != nil {
				requireHistoryNamesRollback(ctx, t, fixture, instanceID)
			}

			rollbackCase.requireReadopted(ctx, t, fixture, instanceID, script, fixture.settled(ctx, t, instanceID))
		})
	}
}

// requireHistoryNamesRollback fails the spec unless the instance's
// history shows that the re-adopted rollback recorded its task as the
// rollback's.
func requireHistoryNamesRollback(ctx context.Context, t *testing.T, fixture *updateFixture, instanceID string) {
	t.Helper()

	var metadata map[string]interface{}

	_, err := fixture.vault.Get(ctx, instanceID+"/metadata", &metadata)
	if err != nil {
		t.Fatal(err)
	}

	history, _ := metadata["history"].([]interface{})
	for _, entry := range history {
		if strings.Contains(fmt.Sprint(entry), "redeploying the previous manifest (task 78)") {
			return
		}
	}

	t.Fatalf("the history %v never shows the rollback's task 78", history)
}
