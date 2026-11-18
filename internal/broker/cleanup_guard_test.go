package broker_test

import (
	"context"
	"strconv"
	"testing"

	"blacksmith/internal/bosh"
	"blacksmith/internal/broker"
	internalVault "blacksmith/internal/vault"
	"blacksmith/pkg/testutil"
)

// cleanupGuardDirector returns a director on which the deployment exists
// and a delete is accepted.
func cleanupGuardDirector() *testutil.ScriptedBOSHDirector {
	director := testutil.NewScriptedBOSHDirector()
	director.GetDeploymentFn = func(name string) (*bosh.DeploymentDetail, error) {
		return &bosh.DeploymentDetail{Name: name}, nil
	}
	director.DeleteDeploymentFn = func(string) (*bosh.Task, error) {
		return &bosh.Task{ID: 1}, nil
	}

	return director
}

// cleanupGuardRecord is a record the cleanup of a failed deployment may
// find, and whether it deletes the deployment over it.
type cleanupGuardRecord struct {
	name    string
	record  func(instanceID string) error
	deletes bool
}

// cleanupGuardRecords returns the records the cleanup is run over. Only a
// failed provision deletes.
func cleanupGuardRecords(ctx context.Context, vaultClient *internalVault.Vault) []cleanupGuardRecord {
	return []cleanupGuardRecord{
		{"a failed provision", func(instanceID string) error {
			return vaultClient.TrackProgress(ctx, instanceID, outcomeProvision, "BOSH deployment task 7 is error", -1, nil)
		}, true},
		{"a provision still running", func(instanceID string) error {
			return vaultClient.TrackProgress(ctx, instanceID, outcomeProvision, "BOSH deployment in progress (task 7)", 7, nil)
		}, false},
		{"a failed update", func(instanceID string) error {
			return vaultClient.TrackProgress(ctx, instanceID, updateFailureAction, "BOSH deployment task 7 is error", -1, nil)
		}, false},
		{"a failed deprovision", func(instanceID string) error {
			return vaultClient.TrackProgress(ctx, instanceID, outcomeDeprovision, "BOSH deletion task 7 is error", -1, nil)
		}, false},
		{"a failed unknown action", func(instanceID string) error {
			return vaultClient.TrackProgress(ctx, instanceID, "unknown", "BOSH task 7 is error", -1, nil)
		}, false},
		{"a failure with no action", func(instanceID string) error {
			return vaultClient.Put(ctx, instanceID+"/task", map[string]interface{}{"state": outcomeStateFailed, "task": -1})
		}, false},
		{"no record", func(string) error { return nil }, false},
	}
}

// The cleanup of a failed deployment deletes the deployment, which is right
// only for a provision that never produced a usable instance. It takes no
// caller's word for what failed. It reads the instance's own record, and
// any record but a failed provision, including one it cannot read, is
// refused before the director is asked anything.
func TestCleanupFailedDeployment_DecidesFromTheRecord(t *testing.T) {
	t.Parallel()

	server, err := testutil.NewVaultDevServer(t)
	if err != nil {
		t.Fatalf("failed to start Vault: %v", err)
	}

	t.Cleanup(server.Close)

	vaultClient := internalVault.New(server.Addr, server.RootToken, true)
	ctx := context.Background()

	records := cleanupGuardRecords(ctx, vaultClient)

	for index, record := range records {
		t.Run(record.name, func(t *testing.T) {
			t.Parallel()

			instanceID := "cleanup-guard-" + strconv.Itoa(index)
			deploymentName := standalonePlanID + "-" + instanceID

			err := record.record(instanceID)
			if err != nil {
				t.Fatalf("failed to write the record: %v", err)
			}

			director := cleanupGuardDirector()

			(&broker.Broker{BOSH: director, Vault: vaultClient}).CleanupFailedDeployment(ctx, instanceID, deploymentName)

			deleted := director.Calls(directorDeleteDeployment)

			switch {
			case record.deletes && (len(deleted) != 1 || deleted[0] != deploymentName):
				t.Errorf("expected the failed provision's deployment to be deleted once, got %v", deleted)
			case !record.deletes && len(deleted) != 0:
				t.Errorf("the cleanup over %s deleted %v", record.name, deleted)
			case !record.deletes && len(director.Calls("GetDeployment")) != 0:
				t.Errorf("the cleanup over %s asked the director for %v", record.name, director.Calls("GetDeployment"))
			}
		})
	}

	t.Run("an unreadable record", func(t *testing.T) {
		t.Parallel()

		director := cleanupGuardDirector()
		unreachable := internalVault.New("http://127.0.0.1:1", server.RootToken, true)

		(&broker.Broker{BOSH: director, Vault: unreachable}).CleanupFailedDeployment(ctx, "cleanup-guard-unreadable", standalonePlanID+"-cleanup-guard-unreadable")

		if calls := director.Calls(directorDeleteDeployment); len(calls) != 0 {
			t.Errorf("the cleanup over an unreadable record deleted %v", calls)
		}
	})
}
