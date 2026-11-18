package broker_test

import (
	"context"
	"net/http"
	"strconv"
	"testing"

	internalVault "blacksmith/internal/vault"
	"blacksmith/pkg/testutil"

	"github.com/fivetwenty-io/osbapi/v2/pkg/osbapi"
)

// recordRunningProvision records the provision of f's instance as running
// in its create task, with a context ID.
func recordRunningProvision(ctx context.Context, t *testing.T, client *internalVault.Vault, instanceID string) {
	t.Helper()

	err := client.TrackProgressWithContextID(ctx, instanceID, outcomeProvision, "Starting provisioning process", racingProvisionContextID, 0, nil)
	if err == nil {
		err = client.TrackProgress(ctx, instanceID, outcomeProvision, "BOSH deployment in progress (task "+strconv.Itoa(updateFailureCreateTaskID)+")", updateFailureCreateTaskID, nil)
	}

	if err != nil {
		t.Fatalf("failed to record the provision: %v", err)
	}
}

// pollProvision runs a provision poll for f's instance.
func (f *updateFailure) pollProvision(ctx context.Context, t *testing.T) osbapi.LastOperationResponse {
	t.Helper()

	response, err := f.broker.LastOperation(ctx, f.instanceID, osbapi.LastOperationRequest{
		ServiceID: valkeyServiceID, PlanID: standalonePlanID, Operation: outcomeProvision,
	})
	if err != nil {
		t.Fatalf("the poll failed: %v", err)
	}

	return response
}

// waitAndCountDeletes waits for f's broker's background work and returns
// how many deletes its director received.
func (f *updateFailure) waitAndCountDeletes(ctx context.Context, t *testing.T) int {
	t.Helper()

	waitCtx, cancel := context.WithTimeout(ctx, updateFailureWaitBound)
	defer cancel()

	f.broker.WaitForBackground(waitCtx)

	return len(f.director.Calls(directorDeleteDeployment))
}

// requireCleanupMark fails unless the record of f's instance holds a
// failed provision with the cleanup mark set as want says.
func (f *updateFailure) requireCleanupMark(ctx context.Context, t *testing.T, want bool) {
	t.Helper()

	recorded := f.current(ctx, t)
	if recorded.Action != outcomeProvision || recorded.State != outcomeStateFailed || recorded.CleanupPending != want {
		t.Fatalf("the record holds a %s %s with the cleanup mark %v, want a failed provision with the mark %v",
			recorded.State, recorded.Action, recorded.CleanupPending, want)
	}
}

// Vault can apply the write that records a provision's failure and lose
// its answer. The poll that wrote it answers in progress, and the next
// poll answers failed from the record. The record says the cleanup is
// still owed, so that poll starts it, and it runs once.
func TestFailedProvision_CleanedUpWhenTheFailureWritesAnswerIsLost(t *testing.T) {
	t.Parallel()

	server, err := testutil.NewVaultDevServer(t)
	if err != nil {
		t.Fatalf("failed to start Vault: %v", err)
	}

	t.Cleanup(server.Close)

	proxy, err := testutil.NewVaultFaultProxy(server.Addr)
	if err != nil {
		t.Fatalf("failed to start the Vault proxy: %v", err)
	}

	t.Cleanup(proxy.Close)

	ctx := t.Context()
	failure := newUpdateFailure(ctx, t, internalVault.New(proxy.Addr, server.RootToken, true), "answer-lost")
	recordRunningProvision(ctx, t, internalVault.New(server.Addr, server.RootToken, true), failure.instanceID)

	proxy.FailWritesAfterApplying(failure.instanceID+"/task", http.StatusBadRequest, "answer lost by the test", 1)

	first := failure.pollProvision(ctx, t)
	if first.State != osbapi.StateInProgress {
		t.Fatalf("the poll whose failure write lost its answer answered %s (%s), want in progress", first.State, first.Description)
	}

	for poll := 2; poll <= 3; poll++ {
		response := failure.pollProvision(ctx, t)
		if response.State != osbapi.StateFailed {
			t.Fatalf("poll %d answered %s (%s), want failed from the record", poll, response.State, response.Description)
		}
	}

	if deletes := failure.waitAndCountDeletes(ctx, t); deletes != 1 {
		t.Fatalf("the failed provision's deployment was deleted %d times, want once", deletes)
	}

	failure.requireCleanupMark(ctx, t, false)
}

// A broker can stop after the failure is on the record and before the
// cleanup sends its delete. The next start finds the mark, sends the
// delete once, and clears the mark, so the start after that, and a poll,
// send none.
func TestFailedProvision_CleanedUpAfterARestart(t *testing.T) {
	t.Parallel()

	server, err := testutil.NewVaultDevServer(t)
	if err != nil {
		t.Fatalf("failed to start Vault: %v", err)
	}

	t.Cleanup(server.Close)

	ctx := t.Context()
	client := internalVault.New(server.Addr, server.RootToken, true)
	first := newUpdateFailure(ctx, t, client, "restart")
	recordRunningProvision(ctx, t, client, first.instanceID)

	// The broker that stopped wrote this, and nothing after it.
	err = client.TrackFailedCleanupPending(ctx, first.instanceID, outcomeProvision, "BOSH deployment task "+strconv.Itoa(updateFailureCreateTaskID)+" is error",
		racingProvisionContextID, updateFailureCreateTaskID)
	if err != nil {
		t.Fatalf("failed to record the failure: %v", err)
	}

	err = first.broker.ReadoptInFlightOperations(ctx)
	if err != nil {
		t.Fatalf("the first start failed: %v", err)
	}

	if deletes := first.waitAndCountDeletes(ctx, t); deletes != 1 {
		t.Fatalf("the start after the stop deleted the deployment %d times, want once", deletes)
	}

	first.requireCleanupMark(ctx, t, false)

	second := newUpdateFailure(ctx, t, client, "restart")

	err = second.broker.ReadoptInFlightOperations(ctx)
	if err != nil {
		t.Fatalf("the second start failed: %v", err)
	}

	if response := second.pollProvision(ctx, t); response.State != osbapi.StateFailed {
		t.Fatalf("the poll after the second start answered %s (%s), want failed", response.State, response.Description)
	}

	if deletes := second.waitAndCountDeletes(ctx, t); deletes != 0 {
		t.Fatalf("the second start and its poll deleted the deployment %d times, want none", deletes)
	}
}

// A record a broker wrote before the mark existed says nothing about the
// cleanup, so a start leaves it alone and deletes nothing.
func TestFailedProvision_UnmarkedRecordIsLeftAloneOnStart(t *testing.T) {
	t.Parallel()

	server, err := testutil.NewVaultDevServer(t)
	if err != nil {
		t.Fatalf("failed to start Vault: %v", err)
	}

	t.Cleanup(server.Close)

	ctx := t.Context()
	client := internalVault.New(server.Addr, server.RootToken, true)
	failure := newUpdateFailure(ctx, t, client, "unmarked-start")
	recordRunningProvision(ctx, t, client, failure.instanceID)

	err = client.TrackFailed(ctx, failure.instanceID, outcomeProvision, "BOSH deployment task "+strconv.Itoa(updateFailureCreateTaskID)+" is error",
		racingProvisionContextID, updateFailureCreateTaskID)
	if err != nil {
		t.Fatalf("failed to record the failure: %v", err)
	}

	err = failure.broker.ReadoptInFlightOperations(ctx)
	if err != nil {
		t.Fatalf("the start failed: %v", err)
	}

	if deletes := failure.waitAndCountDeletes(ctx, t); deletes != 0 {
		t.Fatalf("the start deleted the deployment of an unmarked failure %d times, want none", deletes)
	}
}
