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

// The cleanup deletes a failed provision's deployment only once the
// record says the provision failed. When Vault refuses that write, the
// poll that saw the failed create task must not answer failed, because
// Cloud Controller stops polling on that answer and nothing would record
// the failure or delete the deployment. The next poll records it and
// starts the cleanup.
func TestFailedProvision_CleanedUpOnceItsFailureIsRecorded(t *testing.T) {
	t.Parallel()

	server, err := testutil.NewVaultDevServer(t)
	if err != nil {
		t.Fatalf("failed to start Vault: %v", err)
	}

	t.Cleanup(server.Close)

	ctx := t.Context()
	instanceID := "update-failure-unrecorded"
	direct := internalVault.New(server.Addr, server.RootToken, true)
	proxy := newRacingVault(t, server.Addr, instanceID, func() {})
	failure := newUpdateFailure(ctx, t, internalVault.New(proxy.URL, server.RootToken, true), "unrecorded")

	err = direct.TrackProgressWithContextID(ctx, instanceID, outcomeProvision, "Starting provisioning process", racingProvisionContextID, 0, nil)
	if err == nil {
		err = direct.TrackProgress(ctx, instanceID, outcomeProvision, "BOSH deployment in progress (task "+strconv.Itoa(updateFailureCreateTaskID)+")", updateFailureCreateTaskID, nil)
	}

	if err != nil {
		t.Fatalf("failed to record the provision: %v", err)
	}

	poll := func() osbapi.LastOperationResponse {
		response, pollErr := failure.broker.LastOperation(ctx, instanceID, osbapi.LastOperationRequest{
			ServiceID: valkeyServiceID, PlanID: standalonePlanID, Operation: outcomeProvision,
		})
		if pollErr != nil {
			t.Fatalf("the poll failed: %v", pollErr)
		}

		return response
	}

	refuseTaskWrites(proxy.URL, true)

	first := poll()
	if first.State != osbapi.StateInProgress {
		t.Fatalf("the poll whose failure write was refused answered %s (%s), want in progress so a later poll records the failure", first.State, first.Description)
	}

	refuseTaskWrites(proxy.URL, false)

	second := poll()
	if second.State != osbapi.StateFailed {
		t.Fatalf("the poll after Vault took writes again answered %s (%s), want failed", second.State, second.Description)
	}

	// Waiting for the background work stops the broker taking more, so
	// it waits only once, after the last poll.
	waitCtx, cancel := context.WithTimeout(ctx, updateFailureWaitBound)
	defer cancel()

	failure.broker.WaitForBackground(waitCtx)

	if calls := failure.director.Calls(directorDeleteDeployment); len(calls) != 1 {
		t.Fatalf("the failed provision's deployment was deleted %d times (%v), want once", len(calls), calls)
	}
}

// A failure write can land on the record while the history entry written
// after it does not. The record then says the provision failed, and later
// polls answer failed from it without starting the cleanup, so the poll
// whose write landed must start the cleanup, and only that poll.
func TestFailedProvision_CleanedUpWhenItsHistoryIsNotAppended(t *testing.T) {
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
	instanceID := "update-failure-history-lost"
	direct := internalVault.New(server.Addr, server.RootToken, true)
	failure := newUpdateFailure(ctx, t, internalVault.New(proxy.Addr, server.RootToken, true), "history-lost")

	err = direct.TrackProgressWithContextID(ctx, instanceID, outcomeProvision, "Starting provisioning process", racingProvisionContextID, 0, nil)
	if err == nil {
		err = direct.TrackProgress(ctx, instanceID, outcomeProvision, "BOSH deployment in progress (task "+strconv.Itoa(updateFailureCreateTaskID)+")", updateFailureCreateTaskID, nil)
	}

	if err != nil {
		t.Fatalf("failed to record the provision: %v", err)
	}

	proxy.FailWrites(instanceID+"/metadata", http.StatusBadRequest, "history write refused by the test", -1)

	for poll := 1; poll <= 3; poll++ {
		response, pollErr := failure.broker.LastOperation(ctx, instanceID, osbapi.LastOperationRequest{
			ServiceID: valkeyServiceID, PlanID: standalonePlanID, Operation: outcomeProvision,
		})
		if pollErr != nil {
			t.Fatalf("poll %d failed: %v", poll, pollErr)
		}

		if response.State != osbapi.StateFailed {
			t.Fatalf("poll %d answered %s (%s), want failed, because the failure is on the record", poll, response.State, response.Description)
		}
	}

	waitCtx, cancel := context.WithTimeout(ctx, updateFailureWaitBound)
	defer cancel()

	failure.broker.WaitForBackground(waitCtx)

	if calls := failure.director.Calls(directorDeleteDeployment); len(calls) != 1 {
		t.Fatalf("the failed provision's deployment was deleted %d times (%v), want once", len(calls), calls)
	}
}
