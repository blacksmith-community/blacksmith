package broker_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"blacksmith/internal/broker"
	internalVault "blacksmith/internal/vault"
	"blacksmith/pkg/testutil"

	"github.com/fivetwenty-io/osbapi/v2/pkg/osbapi"
)

func routingVault(t *testing.T) *internalVault.Vault {
	t.Helper()

	server, err := testutil.NewVaultDevServer(t)
	if err != nil {
		t.Fatalf("failed to start Vault: %v", err)
	}

	t.Cleanup(server.Close)

	return internalVault.New(server.Addr, server.RootToken, true)
}

func indexLiveInstance(ctx context.Context, t *testing.T, vaultClient *internalVault.Vault, instanceID string) {
	t.Helper()

	err := vaultClient.Index(ctx, instanceID, map[string]interface{}{
		"service_id": recordedNameService, "plan_id": "small", "deployment_name": "small-" + instanceID,
	})
	if err != nil {
		t.Fatalf("failed to index %s: %v", instanceID, err)
	}
}

// A record that holds an action the broker doesn't know makes the poll an
// error, and nothing is settled, recorded, or deleted over it.
func TestLastOperation_UnknownActionErrors(t *testing.T) {
	t.Parallel()

	vaultClient := routingVault(t)
	director := cleanupGuardDirector()
	brokerInstance := &broker.Broker{BOSH: director, Vault: vaultClient}
	ctx := context.Background()
	instanceID := fmt.Sprintf("unknown-action-%d", time.Now().UnixNano())

	indexLiveInstance(ctx, t, vaultClient, instanceID)

	err := vaultClient.TrackProgressWithContextID(ctx, instanceID, "frobnicate", "Doing something", "context-x", 0, nil)
	if err != nil {
		t.Fatal(err)
	}

	before, _ := readTaskRecord(ctx, t, vaultClient, instanceID)

	_, err = brokerInstance.LastOperation(ctx, instanceID, osbapi.LastOperationRequest{})
	if !errors.Is(err, broker.ErrUnknownRecordedAction) {
		t.Fatalf("the poll was answered %v, want ErrUnknownRecordedAction", err)
	}

	after, _ := readTaskRecord(ctx, t, vaultClient, instanceID)
	if fmt.Sprint(before) != fmt.Sprint(after) {
		t.Fatalf("the poll changed the record from %v to %v", before, after)
	}

	if calls := director.Calls("DeleteDeployment"); len(calls) != 0 {
		t.Fatalf("the poll deleted %v", calls)
	}
}

// An update poll answers from the update on record and never settles a
// provision, and a poll with no operation data follows the recorded
// update.
func TestLastOperation_RoutesAnUpdatePoll(t *testing.T) {
	t.Parallel()

	vaultClient := routingVault(t)
	director := cleanupGuardDirector()
	brokerInstance := &broker.Broker{BOSH: director, Vault: vaultClient}
	ctx := context.Background()

	provisioned := fmt.Sprintf("routes-provisioned-%d", time.Now().UnixNano())
	indexLiveInstance(ctx, t, vaultClient, provisioned)

	err := vaultClient.TrackProgressWithContextID(ctx, provisioned, "provision", "BOSH deployment in progress (task 41)", "context-41", 41, nil)
	if err == nil {
		err = vaultClient.TrackFailed(ctx, provisioned, "provision", "BOSH deployment task 41 is error", "context-41", 41)
	}

	if err != nil {
		t.Fatal(err)
	}

	response, err := brokerInstance.LastOperation(ctx, provisioned, osbapi.LastOperationRequest{Operation: "update"})
	if err != nil || response.State != osbapi.StateInProgress {
		t.Fatalf("an update poll over a failed provision was answered %v, %v, want in progress", response, err)
	}

	if calls := director.Calls("DeleteDeployment"); len(calls) != 0 {
		t.Fatalf("an update poll deleted %v", calls)
	}

	updated := fmt.Sprintf("routes-updated-%d", time.Now().UnixNano())
	indexLiveInstance(ctx, t, vaultClient, updated)

	err = vaultClient.TrackUpdateAccepted(ctx, updated, "Deploying the update", "context-update", "small-"+updated, internalVault.StagedUpdate{PlanID: "small"})
	if err != nil {
		t.Fatal(err)
	}

	response, err = brokerInstance.LastOperation(ctx, updated, osbapi.LastOperationRequest{})
	if err != nil || response.State != osbapi.StateInProgress || response.Description != "Deploying the update" {
		t.Fatalf("a poll over a running update was answered %v, %v", response, err)
	}
}

// readTaskRecord returns the instance's task record and whether it exists.
func readTaskRecord(ctx context.Context, t *testing.T, vaultClient *internalVault.Vault, instanceID string) (map[string]interface{}, bool) {
	t.Helper()

	var record map[string]interface{}

	exists, err := vaultClient.Get(ctx, instanceID+"/task", &record)
	if err != nil {
		t.Fatalf("failed to read the task record of %s: %v", instanceID, err)
	}

	return record, exists
}
