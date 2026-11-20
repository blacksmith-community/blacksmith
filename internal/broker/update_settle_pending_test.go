package broker_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	internalVault "blacksmith/internal/vault"

	"github.com/fivetwenty-io/osbapi/v2/pkg/osbapi"
)

// resolverFailed records what the resolver of the lost answer with
// contextID writes once it finds the director never started the deploy,
// as a broker that stopped right after that write left it.
func resolverFailed(ctx context.Context, t *testing.T, fixture *updateFixture, instanceID, contextID string) {
	t.Helper()

	err := fixture.vault.SettleOutcomeUnknownSettlePending(ctx, instanceID, "The director lists no task for the deploy", contextID)
	if err != nil {
		t.Fatalf("failed to record the resolver's failure: %v", err)
	}
}

// A restart right after the resolver recorded that an update's deploy,
// or its rollback's deploy, never started leaves the update owing its
// settle. Re-adoption finishes it once. The update's own deploy gets its
// failed change record, and the rollback's settles as rollback_failed,
// which takes the rollback_failed phase. Either way the saved manifest is
// deleted, the mark comes off, nothing is deployed, and the claim is
// released.
func TestReadopt_ResolverFailedUpdate_FinishesItsSettle(t *testing.T) {
	t.Parallel()

	for _, gap := range []struct {
		name      string
		record    func(ctx context.Context, t *testing.T, fixture *updateFixture, instanceID string)
		contextID string
		state     string
		phase     string
		outcome   string
	}{
		{"the update's deploy never started", recordedLostAnswer, readoptUpdateContextID, outcomeStateFailed, "", outcomeStateFailed},
		{"the rollback's deploy never started", func(ctx context.Context, t *testing.T, fixture *updateFixture, instanceID string) {
			t.Helper()

			recordedDeployTask(ctx, t, fixture, instanceID)
			recordedRollbackLostAnswer(ctx, t, fixture, instanceID)
		}, readoptRollbackContextID, internalVault.TaskStateRollbackFailed, internalVault.UpdatePhaseRollbackFailed, internalVault.TaskStateRollbackFailed},
	} {
		t.Run(gap.name, func(t *testing.T) {
			t.Parallel()

			fixture := newUpdateFixture(t)
			ctx := context.Background()
			instanceID := fixture.provisioned(ctx, t, openUpdatePlan, lastGoodParams())

			interruptedUpdate(ctx, t, fixture, instanceID)
			gap.record(ctx, t, fixture, instanceID)
			savedRollbackManifest(ctx, t, fixture, instanceID)
			resolverFailed(ctx, t, fixture, instanceID, gap.contextID)

			err := fixture.broker.ReadoptInFlightOperations(ctx)
			if err != nil {
				t.Fatalf("re-adoption failed: %v", err)
			}

			answer := fixture.settled(ctx, t, instanceID)
			if answer.State != osbapi.StateFailed {
				t.Fatalf("the update settled as %+v, want failed", answer)
			}

			record := readTaskRecord(ctx, t, fixture.vault, instanceID)
			if _, marked := record[internalVault.TaskRecordSettlePendingKey]; marked || record["state"] != gap.state {
				t.Fatalf("the record is %v, want %s with no settle mark", record, gap.state)
			}

			if rollback := internalVault.StagedRollback(record); gap.phase != "" && (rollback == nil || rollback.Phase != gap.phase) {
				t.Fatalf("the staged rollback is %+v, want the phase %s", rollback, gap.phase)
			}

			if records := fixture.updateRecords(ctx, t); len(records) != 1 || records[0].Outcome != gap.outcome || records[0].Actor != readoptUpdateActor {
				t.Fatalf("the change records are %+v, want one %s update naming the user", records, gap.outcome)
			}

			if calls := fixture.director.Calls("UpdateDeployment"); len(calls) != 0 {
				t.Fatalf("re-adoption sent %d deploys, want none", len(calls))
			}

			fixture.requireNoSavedManifest(ctx, t, instanceID)
			fixture.requireReleased(t, instanceID)
		})
	}
}

// The update's own goroutine, once the resolver records that its deploy
// never started, takes the mark off as it settles, so the next start
// doesn't settle it again.
func TestUpdate_NeverStarted_ClearsTheSettleMark(t *testing.T) {
	t.Parallel()

	fixture := newUpdateFixture(t)
	ctx := context.Background()
	instanceID := fixture.provisioned(ctx, t, openUpdatePlan, lastGoodParams())

	interruptedUpdate(ctx, t, fixture, instanceID)

	lost := &lostAnswer{}
	lost.script(fixture, nil, 0)
	fixture.broker.SetUnknownOutcomeClock(pastGraceClock())

	err := fixture.broker.ReadoptInFlightOperations(ctx)
	if err != nil {
		t.Fatalf("re-adoption failed: %v", err)
	}

	if answer := fixture.settled(ctx, t, instanceID); answer.State != osbapi.StateFailed {
		t.Fatalf("the update settled as %+v, want failed", answer)
	}

	record := readTaskRecord(ctx, t, fixture.vault, instanceID)
	if _, marked := record[internalVault.TaskRecordSettlePendingKey]; marked || record["state"] != outcomeStateFailed {
		t.Fatalf("the record is %v, want failed with no settle mark", record)
	}

	next := startedBroker(ctx, t, fixture, updatePlans())
	if next.OperationClaimed(instanceID) {
		t.Fatal("the next start took the settled update's claim")
	}

	if records := fixture.updateRecords(ctx, t); len(records) != 1 || records[0].Outcome != outcomeStateFailed {
		t.Fatalf("the change records are %+v, want one failed update", records)
	}
}

// A broker that shuts down after the resolver recorded that the update's
// deploy never started, and before the update settled, leaves the record
// marked and no change record. The next start writes the one change
// record and takes the mark off.
func TestUpdate_NeverStarted_ShutdownLeavesTheSettleToTheNextStart(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fixture, proxy := faultedUpdateFixture(t)
	instanceID := fixture.provisioned(ctx, t, openUpdatePlan, lastGoodParams())

	interruptedUpdate(ctx, t, fixture, instanceID)
	recordedLostAnswer(ctx, t, fixture, instanceID)
	savedRollbackManifest(ctx, t, fixture, instanceID)

	lost := &lostAnswer{}
	lost.script(fixture, nil, 0)
	fixture.broker.SetUnknownOutcomeClock(pastGraceClock())

	serviceCtx, shutdown := context.WithCancel(ctx)
	fixture.broker.ServiceContext = serviceCtx

	// The resolver's write lands, and every later write of the record
	// fails until the broker shuts down.
	proxy.FailWritesAfter(instanceID+"/task", 1, http.StatusBadRequest, "task record write refused by the test", -1)

	go func() {
		defer shutdown()

		deadline := time.Now().Add(10 * time.Second)
		for len(proxy.WritesTo(instanceID+"/task")) < 2 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
	}()

	err := fixture.broker.ReadoptInFlightOperations(ctx)
	if err != nil {
		t.Fatalf("re-adoption failed: %v", err)
	}

	fixture.broker.WaitForBackground(ctx)
	fixture.changes.Wait()

	record := readTaskRecord(ctx, t, fixture.vault, instanceID)
	if record["state"] != outcomeStateFailed || record[internalVault.TaskRecordSettlePendingKey] != true {
		t.Fatalf("after the shutdown, the record is %v, want failed with the settle mark", record)
	}

	if records := fixture.updateRecords(ctx, t); len(records) != 0 {
		t.Fatalf("after the shutdown, the change records are %+v, want none", records)
	}

	proxy.ClearFaults()

	next := startedBroker(ctx, t, fixture, updatePlans())
	if next.OperationClaimed(instanceID) {
		t.Fatal("the next start kept the settled update's claim")
	}

	record = readTaskRecord(ctx, t, fixture.vault, instanceID)
	if _, marked := record[internalVault.TaskRecordSettlePendingKey]; marked || record["state"] != outcomeStateFailed {
		t.Fatalf("after the next start, the record is %v, want failed with no settle mark", record)
	}

	if records := fixture.updateRecords(ctx, t); len(records) != 1 || records[0].Outcome != outcomeStateFailed || records[0].Actor != readoptUpdateActor {
		t.Fatalf("after the next start, the change records are %+v, want one failed update naming the user", records)
	}

	fixture.requireNoSavedManifest(ctx, t, instanceID)
}
