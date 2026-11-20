package broker_test

import (
	"context"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"blacksmith/internal/bosh"
	"blacksmith/internal/broker"
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

// requireUnresumableAnswer fails t unless answer settles an update
// re-adoption can't resume as failed and usable, naming task 77, saying
// why with reason and what comes next with next, and answering
// update_repeatable as repeatable.
func requireUnresumableAnswer(t *testing.T, answer osbapi.LastOperationResponse, reason, next string, repeatable bool) {
	t.Helper()

	if answer.State != osbapi.StateFailed || !strings.Contains(answer.Description, reason) || !strings.Contains(answer.Description, "77") {
		t.Fatalf("the update re-adoption can't resume settled as %+v, want failed, naming task 77 and saying why", answer)
	}

	if !strings.Contains(answer.Description, next) {
		t.Fatalf("the update re-adoption can't resume settled with the description %q, want it to say %q", answer.Description, next)
	}

	if answer.UpdateRepeatable == nil || *answer.UpdateRepeatable != repeatable {
		t.Fatalf("the update re-adoption can't resume answered update_repeatable %s, want %v", boolField(answer.UpdateRepeatable), repeatable)
	}

	if answer.InstanceUsable == nil || !*answer.InstanceUsable {
		t.Fatalf("the update re-adoption can't resume answered instance_usable %s, want true", boolField(answer.InstanceUsable))
	}
}

// boolField names the value of an optional answer field.
func boolField(field *bool) string {
	if field == nil {
		return "unset"
	}

	return strconv.FormatBool(*field)
}

// An interrupted update that re-adoption can't carry through, because the
// plan it stages has left the catalog or its staged values don't decode,
// is still followed to the end of its task under the claim. It then
// settles as failed with a reason, commits nothing, and frees the
// instance for the next update. The answer tells the tenant what comes
// next. Staged values that don't decode leave the update repeatable,
// while a plan that left the catalog needs an operator to restore it, so
// the answer says so and the update isn't repeatable until then.
func TestReadopt_UnresumableUpdate_SettlesFailed(t *testing.T) {
	t.Parallel()

	for _, unresumable := range []struct {
		name       string
		spoil      func(ctx context.Context, t *testing.T, fixture *updateFixture, instanceID string)
		reason     string
		next       string
		repeatable bool
	}{
		{planLeftCatalog, dropOpenPlan, "no longer in the catalog", "An operator must restore the plan", false},
		{"its staged values don't decode", garbleStagedUpdate, "can't read", "Send the update again", true},
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

			(&deployScript{}).script(fixture, outcomeTaskDone)

			held := fixture.holdTask(updateTaskID)

			err := fixture.broker.ReadoptInFlightOperations(ctx)
			if err != nil {
				t.Fatalf("re-adoption failed: %v", err)
			}

			fixture.requireHeldPastBound(ctx, t, held, instanceID)
			close(held.release)

			answer := fixture.settled(ctx, t, instanceID)
			requireUnresumableAnswer(t, answer, unresumable.reason, unresumable.next, unresumable.repeatable)

			if root, _ := fixture.storedParameters(ctx, t, instanceID); !reflect.DeepEqual(root, lastGoodParams()) {
				t.Fatalf("the stored parameters are %v, want the last good %v", root, lastGoodParams())
			}

			fixture.requireNoSavedManifest(ctx, t, instanceID)
			fixture.requireReleased(t, instanceID)

			if records := fixture.updateRecords(ctx, t); len(records) != 1 || records[0].Outcome != outcomeStateFailed || records[0].TaskID != updateTaskID {
				t.Fatalf("the change records are %+v, want one failed update naming task 77", records)
			}

			if calls := fixture.director.Calls("UpdateDeployment"); len(calls) != 0 {
				t.Fatalf("re-adoption sent %d deploys, want none", len(calls))
			}

			// The fixture's broker stopped tracking its goroutines once
			// settled waited for them, so a broker of the next start, over
			// the same Vault and with the plan back in the catalog, sends
			// the update again and waits for it.
			next := &broker.Broker{BOSH: fixture.director, Vault: fixture.vault, Plans: plans}
			next.SetChanges(fixture.changes)

			response, async, err := next.Update(ctx, instanceID, failingUpdateRequest(), true)
			requireAccepted(t, response, async, err)
			next.WaitForBackground(ctx)

			answer, err = next.LastOperation(ctx, instanceID, osbapi.LastOperationRequest{Operation: updateFailureAction})
			if err != nil || answer.State != osbapi.StateSucceeded {
				t.Fatalf("the update sent again settled as %+v (%v), want succeeded", answer, err)
			}
		})
	}
}

// planLeftCatalog names the case of an update whose plan has left the
// catalog.
const planLeftCatalog = "its plan left the catalog"

// dropOpenPlan takes the plan the interrupted update stages out of the
// fixture's catalog.
func dropOpenPlan(_ context.Context, _ *testing.T, fixture *updateFixture, _ string) {
	delete(fixture.broker.Plans, recordedNameService+"/"+openUpdatePlan)
}

// unresumableSidePath is one way an update re-adoption can't carry
// through settles.
type unresumableSidePath struct {
	name string
	// record writes what the update had recorded when the broker
	// stopped, over its accepted record.
	record func(ctx context.Context, t *testing.T, fixture *updateFixture, instanceID string)
	// script points the director at what it answers after the restart.
	script func(fixture *updateFixture)
}

// unresumableSidePaths returns the ways an update re-adoption can't carry
// through settles other than by its deploy task succeeding.
func unresumableSidePaths() []unresumableSidePath {
	neverStarted := func(fixture *updateFixture) {
		(&lostAnswer{}).script(fixture, nil, 0)
		fixture.broker.SetUnknownOutcomeClock(pastGraceClock())
	}

	return []unresumableSidePath{
		{"the resolver finds its deploy never started", recordedLostAnswer, neverStarted},
		{"a restart lands after the resolver's failure", func(ctx context.Context, t *testing.T, fixture *updateFixture, instanceID string) {
			t.Helper()

			recordedLostAnswer(ctx, t, fixture, instanceID)
			resolverFailed(ctx, t, fixture, instanceID, readoptUpdateContextID)
		}, neverStarted},
		{"the director pruned its task", recordedDeployTask, func(fixture *updateFixture) {
			fixture.director.WaitForTaskFn = func(taskID int) (*bosh.Task, error) { return nil, readoptTaskNotFound(taskID) }
		}},
		{"its deploy failed", recordedDeployTask, func(fixture *updateFixture) {
			(&deployScript{}).script(fixture, outcomeTaskError)
		}},
	}
}

// unresumableKind is a reason re-adoption can't carry an update through,
// and what the settled update then says.
type unresumableKind struct {
	name       string
	spoil      func(ctx context.Context, t *testing.T, fixture *updateFixture, instanceID string)
	reason     string
	next       string
	attention  string
	repeatable bool
}

// requireSettled fails t unless the update of instanceID settled as an
// update of this kind settles, on any path.
func (k unresumableKind) requireSettled(ctx context.Context, t *testing.T, fixture *updateFixture, instanceID string) {
	t.Helper()

	answer := fixture.settled(ctx, t, instanceID)
	if answer.State != osbapi.StateFailed || !strings.Contains(answer.Description, k.reason) || !strings.Contains(answer.Description, k.next) {
		t.Fatalf("the update settled as %+v, want failed, saying %q and %q", answer, k.reason, k.next)
	}

	if answer.UpdateRepeatable == nil || *answer.UpdateRepeatable != k.repeatable || answer.InstanceUsable == nil || !*answer.InstanceUsable {
		t.Fatalf("the update answered update_repeatable %s and instance_usable %s, want %v and true", boolField(answer.UpdateRepeatable), boolField(answer.InstanceUsable), k.repeatable)
	}

	requireUpdateAttention(t, fixture.broker, instanceID, k.attention, "once the update settled")

	if root, _ := fixture.storedParameters(ctx, t, instanceID); !reflect.DeepEqual(root, lastGoodParams()) {
		t.Fatalf("the stored parameters are %v, want the last good %v", root, lastGoodParams())
	}

	if records := fixture.updateRecords(ctx, t); len(records) != 1 || records[0].Outcome != outcomeStateFailed {
		t.Fatalf("the change records are %+v, want one failed update", records)
	}

	fixture.requireNoSavedManifest(ctx, t, instanceID)
	fixture.requireReleased(t, instanceID)
}

// An update re-adoption can't carry through settles the same way on every
// path, whether the resolver finds that its deploy never started, a
// restart lands after the resolver's failure, the director pruned its
// task, or its deploy failed. The answer says why and what comes next, a
// plan that left the catalog makes the update unrepeatable, and the
// instance is listed in Attention required at once, not only at the next
// start.
func TestReadopt_UnresumableUpdate_EverySettlePathSaysWhy(t *testing.T) {
	t.Parallel()

	for _, kind := range []unresumableKind{
		{planLeftCatalog, dropOpenPlan, "no longer in the catalog", "An operator must restore the plan", "an operator restores the plan", false},
		{"its staged values don't decode", garbleStagedUpdate, "can't read", "Send the update again", stagedLostAttention, true},
	} {
		for _, path := range unresumableSidePaths() {
			t.Run(kind.name+", and "+path.name, func(t *testing.T) {
				t.Parallel()

				fixture := newUpdateFixture(t)
				ctx := context.Background()
				instanceID := fixture.provisioned(ctx, t, openUpdatePlan, lastGoodParams())

				interruptedUpdate(ctx, t, fixture, instanceID)
				path.record(ctx, t, fixture, instanceID)
				savedRollbackManifest(ctx, t, fixture, instanceID)
				kind.spoil(ctx, t, fixture, instanceID)
				path.script(fixture)

				err := fixture.broker.ReadoptInFlightOperations(ctx)
				if err != nil {
					t.Fatalf("re-adoption failed: %v", err)
				}

				kind.requireSettled(ctx, t, fixture, instanceID)
			})
		}
	}
}
