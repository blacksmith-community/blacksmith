package broker_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/mutation"
	"blacksmith/internal/settings"
	internalVault "blacksmith/internal/vault"

	"github.com/fivetwenty-io/osbapi/v2/pkg/osbapi"
)

// The rollback deploy's task, and the outcomes the rollback specs read.
const (
	rollbackTaskID     = 78
	changeRolledBack   = "rolled_back"
	changeRollbackFail = "rollback_failed"
)

// rollbackScript scripts the director for an update whose deploy, task
// 77, fails, and whose rollback deploy, task 78, ends in rollbackState.
// When hold is set, the wait on the rollback's task blocks until hold is
// closed, and holding is closed once that wait starts. When lost is set,
// the director loses its answer to the rollback deploy.
type rollbackScript struct {
	mu        sync.Mutex
	manifests []string
	hold      chan struct{}
	holding   chan struct{}
	holdOnce  sync.Once
	lost      bool
}

// script points the fixture's director at the script.
func (r *rollbackScript) script(fixture *updateFixture, rollbackState string) {
	fixture.director.GetReleasesFn = func() ([]bosh.Release, error) { return nil, nil }
	fixture.director.GetDeploymentFn = func(name string) (*bosh.DeploymentDetail, error) {
		return &bosh.DeploymentDetail{Name: name, Manifest: liveManifest}, nil
	}
	fixture.director.UpdateDeploymentFn = func(_, manifest string) (*bosh.Task, error) {
		r.mu.Lock()
		defer r.mu.Unlock()

		r.manifests = append(r.manifests, manifest)
		if len(r.manifests) == 1 {
			return &bosh.Task{ID: updateTaskID, State: outcomeTaskProcessing}, nil
		}

		if r.lost {
			return nil, fmt.Errorf("the rollback deploy timed out: %w", bosh.ErrOutcomeUnknown)
		}

		return &bosh.Task{ID: rollbackTaskID, State: outcomeTaskProcessing}, nil
	}
	fixture.director.WaitForTaskFn = func(taskID int) (*bosh.Task, error) {
		if taskID == updateTaskID {
			return &bosh.Task{ID: taskID, State: outcomeTaskError}, nil
		}

		if r.hold != nil {
			r.holdOnce.Do(func() { close(r.holding) })
			<-r.hold
		}

		return &bosh.Task{ID: taskID, State: rollbackState}, nil
	}
}

// held returns a script whose rollback task waits until release is
// called.
func heldRollback() *rollbackScript {
	return &rollbackScript{hold: make(chan struct{}), holding: make(chan struct{})}
}

// sent returns the manifests the update and its rollback deployed.
func (r *rollbackScript) sent() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]string(nil), r.manifests...)
}

// waitHolding waits until the rollback's task is being followed.
func (r *rollbackScript) waitHolding(t *testing.T) {
	t.Helper()

	select {
	case <-r.holding:
	case <-time.After(10 * time.Second):
		t.Fatal("the rollback deploy was never followed")
	}
}

// rollbackOn saves the rollback setting in the fixture's Vault.
func (f *updateFixture) rollbackOn(ctx context.Context, t *testing.T, on bool) {
	t.Helper()

	err := settings.SaveUpdateRollback(ctx, f.vault, on)
	if err != nil {
		t.Fatalf("failed to save the rollback setting: %v", err)
	}
}

// startFailingUpdate provisions an instance whose stored parameters are
// lastGoodParams, and sends it an update the script fails.
func (f *updateFixture) startFailingUpdate(ctx context.Context, t *testing.T) string {
	t.Helper()

	instanceID := f.provisioned(ctx, t, openUpdatePlan, lastGoodParams())

	response, async, err := f.broker.Update(ctx, instanceID, failingUpdateRequest(), true)
	requireAccepted(t, response, async, err)

	return instanceID
}

// lastGoodParams are the parameters the rollback specs provision with.
func lastGoodParams() map[string]interface{} {
	return map[string]interface{}{policyKey: policyNoEvict}
}

// storedValues reads the records a succeeded update commits, which are
// the instance, its root record, its stored provision request, and its
// stored manifest.
func (f *updateFixture) storedValues(ctx context.Context, t *testing.T, instanceID string) string {
	t.Helper()

	instance, _, err := f.vault.FindInstance(ctx, instanceID)
	if err != nil || instance == nil {
		t.Fatalf("the instance could not be read (%v)", err)
	}

	parts := make([]string, 0, 4)
	parts = append(parts, fmt.Sprintf("%+v", instance))

	for _, path := range []string{instanceID, instanceID + "/" + instance.DeploymentNameFor(instanceID), instanceID + "/manifest"} {
		var record map[string]interface{}

		_, err = f.vault.Get(ctx, path, &record)
		if err != nil {
			t.Fatal(err)
		}

		parts = append(parts, fmt.Sprint(record))
	}

	return strings.Join(parts, "\n")
}

// failingUpdateRequest is the update the rollback specs send.
func failingUpdateRequest() osbapi.UpdateRequest {
	return osbapi.UpdateRequest{ServiceID: recordedNameService, Parameters: map[string]any{policyKey: policyLRU}}
}

// requireNoSavedManifest fails the spec unless the manifest the update
// saved for its rollback is gone.
func (f *updateFixture) requireNoSavedManifest(ctx context.Context, t *testing.T, instanceID string) {
	t.Helper()

	var saved map[string]interface{}

	found, err := f.vault.Get(ctx, instanceID+"/update_rollback_manifest", &saved)
	if err != nil || found {
		t.Fatalf("the settled update left its saved manifest behind (%v)", err)
	}
}

// With the setting on, a failed deploy is followed by one deploy of the
// manifest the director ran before the update, exactly as it was, so an
// upgraded instance keeps its versions, and the update answers failed,
// usable, and repeatable, with the last good values in place.
func TestUpdate_FailedTask_RollbackOn_RedeploysLiveManifest(t *testing.T) {
	t.Parallel()

	fixture := newUpdateFixture(t)
	ctx := context.Background()

	fixture.rollbackOn(ctx, t, true)

	script := &rollbackScript{}
	script.script(fixture, outcomeTaskDone)

	instanceID := fixture.startFailingUpdate(ctx, t)
	answer := fixture.settled(ctx, t, instanceID)

	sent := script.sent()
	if len(sent) != 2 || sent[1] != liveManifest || sent[0] == liveManifest {
		t.Fatalf("the update sent %d deploys, want the update's and then the live manifest as it was", len(sent))
	}

	if answer.State != osbapi.StateFailed || !strings.Contains(answer.Description, "restored") ||
		!strings.Contains(answer.Description, "77") || !strings.Contains(answer.Description, "78") {
		t.Fatalf("the rolled-back update was answered %+v, want failed, naming both tasks and the restore", answer)
	}

	requireLastGood(ctx, t, fixture, instanceID, lastGoodParams(), answer)
	fixture.requireNoSavedManifest(ctx, t, instanceID)
}

// While the rollback runs, the poll answers in progress and says the
// update failed and the previous manifest is being redeployed.
func TestUpdate_RollbackInProgress_ReportsInProgress(t *testing.T) {
	t.Parallel()

	fixture := newUpdateFixture(t)
	ctx := context.Background()

	fixture.rollbackOn(ctx, t, true)

	script := heldRollback()
	script.script(fixture, outcomeTaskDone)

	instanceID := fixture.startFailingUpdate(ctx, t)
	script.waitHolding(t)

	answer, err := fixture.broker.LastOperation(ctx, instanceID, osbapi.LastOperationRequest{Operation: updateFailureAction})

	close(script.hold)

	if err != nil || answer.State != osbapi.StateInProgress || !strings.Contains(answer.Description, "redeploying the previous manifest") ||
		!strings.Contains(answer.Description, "77") {
		t.Fatalf("the poll during the rollback answered %+v, %v, want in progress, naming the failed task and the redeploy", answer, err)
	}

	fixture.settled(ctx, t, instanceID)
}

// A rollback that restores the deployment leaves the plan, the
// parameters, and the stored manifest as they were.
func TestUpdate_RollbackSucceeded_ReportsFailedUsable(t *testing.T) {
	t.Parallel()

	fixture := newUpdateFixture(t)
	ctx := context.Background()

	fixture.rollbackOn(ctx, t, true)

	script := &rollbackScript{}
	script.script(fixture, outcomeTaskDone)

	instanceID := fixture.provisioned(ctx, t, openUpdatePlan, lastGoodParams())
	before := fixture.storedValues(ctx, t, instanceID)

	response, async, err := fixture.broker.Update(ctx, instanceID, failingUpdateRequest(), true)
	requireAccepted(t, response, async, err)

	answer := fixture.settled(ctx, t, instanceID)
	requireLastGood(ctx, t, fixture, instanceID, lastGoodParams(), answer)

	if after := fixture.storedValues(ctx, t, instanceID); after != before {
		t.Fatalf("the rolled-back update changed the stored values:\nbefore %s\nafter  %s", before, after)
	}

	record := readTaskRecord(ctx, t, fixture.vault, instanceID)
	if rollback := internalVault.StagedRollback(record); record["state"] != outcomeStateFailed || rollback == nil ||
		rollback.Phase != internalVault.UpdatePhaseRolledBack || rollback.TaskID != rollbackTaskID || rollback.FailedTaskID != updateTaskID {
		t.Fatalf("the task record is %v, want failed with the rollback restored by task 78", record)
	}
}

// A rollback that fails answers failed, not usable, and repeatable,
// records rollback_failed, sends no third deploy, and gives the claim up.
func TestUpdate_RollbackFailed_NoSecondRollback(t *testing.T) {
	t.Parallel()

	fixture := newUpdateFixture(t)
	fixture.broker.Guard = mutation.NewGuard(time.Now)
	ctx := context.Background()

	fixture.rollbackOn(ctx, t, true)

	script := &rollbackScript{}
	script.script(fixture, outcomeTaskError)

	instanceID := fixture.startFailingUpdate(ctx, t)
	answer := fixture.settled(ctx, t, instanceID)

	requireRollbackFailed(t, answer)

	if sent := script.sent(); len(sent) != 2 {
		t.Fatalf("the update sent %d deploys, want the update's and one rollback", len(sent))
	}

	if record := readTaskRecord(ctx, t, fixture.vault, instanceID); record["state"] != internalVault.TaskStateRollbackFailed {
		t.Fatalf("the task record is %v, want rollback_failed", record)
	}

	release, err := fixture.broker.Guard.Acquire(instanceID, openUpdatePlan+"-"+instanceID, mutation.Holder{Kind: "upgrade", Description: "spec"})
	if err != nil {
		t.Fatalf("the instance is still held after the failed rollback: %v", err)
	}

	release()
	fixture.requireNoSavedManifest(ctx, t, instanceID)
}

// requireRollbackFailed fails the spec unless the answer is that of an
// update whose rollback failed.
func requireRollbackFailed(t *testing.T, answer osbapi.LastOperationResponse) {
	t.Helper()

	if answer.State != osbapi.StateFailed || answer.InstanceUsable == nil || *answer.InstanceUsable ||
		answer.UpdateRepeatable == nil || !*answer.UpdateRepeatable || !strings.Contains(answer.Description, "inconsistent") {
		t.Fatalf("the update whose rollback failed was answered %+v, want failed, not usable, repeatable, and saying the deployment may be inconsistent", answer)
	}
}

// A lost answer to the rollback deploy is resolved by the rollback's own
// context ID, and never by sending the rollback again. A task found that
// way is followed, and a lookup that finds no task past the grace period
// fails the rollback.
func TestUpdate_RollbackOutcomeUnknown_ResolvesByContextID_NoRetry(t *testing.T) {
	t.Parallel()

	for _, lostCase := range []struct {
		name  string
		found *bosh.Task
		clock func() time.Time
		want  string
	}{
		{"the rollback's task is found", &bosh.Task{ID: rollbackTaskID, State: outcomeTaskProcessing}, nil, changeRolledBack},
		{"no task past the grace period", nil, pastGraceClock(), changeRollbackFail},
	} {
		t.Run(lostCase.name, func(t *testing.T) {
			t.Parallel()

			fixture := newUpdateFixture(t)
			ctx := context.Background()

			fixture.rollbackOn(ctx, t, true)

			lost := &lostAnswer{}
			lost.script(fixture, lostCase.found, 1)

			// The script answers the update's deploy and loses only the
			// rollback's answer.
			script := &rollbackScript{lost: true}
			script.script(fixture, outcomeTaskDone)

			if lostCase.clock != nil {
				fixture.broker.SetUnknownOutcomeClock(lostCase.clock)
			}

			instanceID := fixture.startFailingUpdate(ctx, t)
			answer := fixture.settled(ctx, t, instanceID)

			record := readTaskRecord(ctx, t, fixture.vault, instanceID)
			rollback := internalVault.StagedRollback(record)

			if rollback == nil || record["context_id"] != rollback.ContextID || rollback.ContextID == rollback.FailedContextID {
				t.Fatalf("the task record is %v, want the rollback's own context ID on record", record)
			}

			looked := lost.lookedUp()
			for _, contextID := range looked {
				if contextID != rollback.ContextID {
					t.Fatalf("the rollback looked its task up by %q, want its own context ID %q", contextID, rollback.ContextID)
				}
			}

			if len(looked) == 0 || len(script.sent()) != 2 {
				t.Fatalf("the rollback looked its task up %d times and sent %d deploys, want lookups and two deploys", len(looked), len(script.sent()))
			}

			if records := fixture.updateRecords(ctx, t); len(records) != 1 || records[0].Outcome != lostCase.want {
				t.Fatalf("the change records are %+v, want one %s", records, lostCase.want)
			}

			if lostCase.want == changeRollbackFail {
				requireRollbackFailed(t, answer)
			}
		})
	}
}

// The update keeps the instance's claim through its rollback, so another
// operation finds the instance busy, a different update gets 422, and the
// same update gets 202 with the same operation.
func TestUpdate_Rollback_HoldsClaim(t *testing.T) {
	t.Parallel()

	fixture := newUpdateFixture(t)
	fixture.broker.Guard = mutation.NewGuard(time.Now)
	ctx := context.Background()

	fixture.rollbackOn(ctx, t, true)

	script := heldRollback()
	script.script(fixture, outcomeTaskDone)

	instanceID := fixture.startFailingUpdate(ctx, t)
	script.waitHolding(t)

	_, busyErr := fixture.broker.Guard.Acquire(instanceID, openUpdatePlan+"-"+instanceID, mutation.Holder{Kind: "upgrade", Description: "spec"})

	_, _, differentErr := fixture.broker.Update(ctx, instanceID, osbapi.UpdateRequest{
		ServiceID: recordedNameService, Parameters: map[string]any{policyKey: policyNoEvict},
	}, true)

	response, async, sameErr := fixture.broker.Update(ctx, instanceID, failingUpdateRequest(), true)

	close(script.hold)
	fixture.settled(ctx, t, instanceID)

	var busy *mutation.BusyError
	if !errors.As(busyErr, &busy) {
		t.Fatalf("another operation during the rollback got %v, want a BusyError", busyErr)
	}

	if !errors.Is(differentErr, osbapi.ErrConcurrencyError) {
		t.Fatalf("a different update during the rollback got %v, want ConcurrencyError", differentErr)
	}

	requireAccepted(t, response, async, sameErr)

	if sent := script.sent(); len(sent) != 2 {
		t.Fatalf("the update sent %d deploys, want 2", len(sent))
	}
}

// The change record of a failed update names the failed deploy's task,
// the rollback's task, and the setting in effect at the failure, and
// holds no manifest content.
func TestUpdate_Rollback_AuditRecord(t *testing.T) {
	t.Parallel()

	for _, auditCase := range []struct {
		name          string
		on            bool
		rollbackState string
		outcome       string
		rollbackTask  int
	}{
		{"the rollback restores the deployment", true, outcomeTaskDone, changeRolledBack, rollbackTaskID},
		{"the rollback fails", true, outcomeTaskError, changeRollbackFail, rollbackTaskID},
		{"the setting is off", false, outcomeTaskDone, outcomeStateFailed, 0},
	} {
		t.Run(auditCase.name, func(t *testing.T) {
			t.Parallel()

			fixture := newUpdateFixture(t)
			ctx := context.Background()

			fixture.rollbackOn(ctx, t, auditCase.on)

			script := &rollbackScript{}
			script.script(fixture, auditCase.rollbackState)

			instanceID := fixture.startFailingUpdate(ctx, t)
			fixture.settled(ctx, t, instanceID)

			records := fixture.updateRecords(ctx, t)
			if len(records) != 1 {
				t.Fatalf("the change records are %+v, want one", records)
			}

			record := records[0]
			wantSetting := fmt.Sprintf("update_rollback=%t (%s)", auditCase.on, settings.SourceVault)

			if record.Outcome != auditCase.outcome || record.TaskID != updateTaskID || record.RollbackTaskID != auditCase.rollbackTask || record.Setting != wantSetting {
				t.Fatalf("the change record is %+v, want %s naming task 77, rollback task %d, and %q", record, auditCase.outcome, auditCase.rollbackTask, wantSetting)
			}

			encoded, err := json.Marshal(record)
			if err != nil || strings.Contains(string(encoded), "ubuntu-noble") || strings.Contains(string(encoded), "newer-in-template") {
				t.Fatalf("the change record holds manifest content: %s", encoded)
			}
		})
	}
}

// The setting is read when the deploy fails, so a change saved while the
// update deploys decides whether it rolls back.
func TestUpdate_RollbackSettingToggledMidUpdate_UsesValueAtFailure(t *testing.T) {
	t.Parallel()

	for _, toggled := range []bool{true, false} {
		t.Run(fmt.Sprintf("turned to %t", toggled), func(t *testing.T) {
			t.Parallel()

			fixture := newUpdateFixture(t)
			ctx := context.Background()

			fixture.rollbackOn(ctx, t, !toggled)

			script := &rollbackScript{}
			script.script(fixture, outcomeTaskDone)

			release := make(chan struct{})
			waitUpdate := fixture.director.WaitForTaskFn
			fixture.director.WaitForTaskFn = func(taskID int) (*bosh.Task, error) {
				if taskID == updateTaskID {
					<-release
				}

				return waitUpdate(taskID)
			}

			instanceID := fixture.startFailingUpdate(ctx, t)

			fixture.rollbackOn(ctx, t, toggled)
			close(release)
			fixture.settled(ctx, t, instanceID)

			want := 1
			if toggled {
				want = 2
			}

			if sent := script.sent(); len(sent) != want {
				t.Fatalf("with the setting turned to %t during the deploy, the update sent %d deploys, want %d", toggled, len(sent), want)
			}

			rollback := internalVault.StagedRollback(readTaskRecord(ctx, t, fixture.vault, instanceID))
			if rollback == nil || rollback.Setting != toggled || rollback.SettingSource != settings.SourceVault {
				t.Fatalf("the recorded rollback is %+v, want the setting %t from Vault", rollback, toggled)
			}
		})
	}
}

// A saved manifest that is gone, or that no longer matches its digest, is
// never deployed. The update is answered failed and usable, says the
// previous manifest could not be read, and sends no second deploy.
func TestUpdate_RollbackManifestUnreadable_FailsWithoutRedeploy(t *testing.T) {
	t.Parallel()

	cases := map[string]func(ctx context.Context, t *testing.T, fixture *updateFixture, path string){
		"deleted after it was saved": dropSavedManifest,
		"changed after it was saved": changeSavedManifest,
	}

	for name, spoil := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fixture := newUpdateFixture(t)
			ctx := context.Background()

			fixture.rollbackOn(ctx, t, true)

			script := &rollbackScript{}
			script.script(fixture, outcomeTaskDone)

			waiting, release := make(chan struct{}), make(chan struct{})
			waitUpdate := fixture.director.WaitForTaskFn
			fixture.director.WaitForTaskFn = func(taskID int) (*bosh.Task, error) {
				if taskID == updateTaskID {
					close(waiting)
					<-release
				}

				return waitUpdate(taskID)
			}

			instanceID := fixture.startFailingUpdate(ctx, t)

			select {
			case <-waiting:
			case <-time.After(10 * time.Second):
				t.Fatal("the update's deploy was never followed")
			}

			spoil(ctx, t, fixture, instanceID+"/update_rollback_manifest")
			close(release)

			answer := fixture.settled(ctx, t, instanceID)
			if sent := script.sent(); len(sent) != 1 {
				t.Fatalf("the update sent %d deploys, want only its own", len(sent))
			}

			if answer.State != osbapi.StateFailed || answer.InstanceUsable == nil || !*answer.InstanceUsable ||
				!strings.Contains(answer.Description, "previous manifest could not be read") {
				t.Fatalf("the update was answered %+v, want failed, usable, and saying the previous manifest could not be read", answer)
			}

			requireLastGood(ctx, t, fixture, instanceID, lastGoodParams(), answer)
			fixture.requireNoSavedManifest(ctx, t, instanceID)
		})
	}
}

// dropSavedManifest deletes the manifest the update saved for its
// rollback.
func dropSavedManifest(ctx context.Context, t *testing.T, fixture *updateFixture, path string) {
	t.Helper()

	err := fixture.vault.Delete(ctx, path)
	if err != nil {
		t.Fatalf("failed to delete the saved manifest: %v", err)
	}
}

// changeSavedManifest changes the manifest the update saved for its
// rollback and leaves its digest as it was.
func changeSavedManifest(ctx context.Context, t *testing.T, fixture *updateFixture, path string) {
	t.Helper()

	var saved map[string]interface{}

	found, err := fixture.vault.Get(ctx, path, &saved)
	if err != nil || !found {
		t.Fatalf("the update saved no manifest before its deploy (%v)", err)
	}

	saved["manifest"] = liveManifest + "\n# changed\n"

	err = fixture.vault.Put(ctx, path, saved)
	if err != nil {
		t.Fatalf("failed to change the saved manifest: %v", err)
	}
}
