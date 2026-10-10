package vault_test

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"testing"
	"time"

	"blacksmith/internal/vault"
)

// The keys of an upgrade task and of a registration the broker keeps in
// its own folders.
const (
	upgradeStatusKey    = "status"
	registrationNameKey = "name"
)

// ListOperationRecordIDs names every instance folder at the root of the
// mount, and leaves out the folders that hold the broker's own state and
// the keys that are not folders.
func TestListOperationRecordIDsNamesInstanceFolders(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := newDevVault(t)

	writes := map[string]map[string]interface{}{
		"instance-a/task":                    {"action": "provision"},
		"instance-b/metadata":                {"created_at": "2026-01-01T00:00:00Z"},
		"upgrade-tasks/task-1":               {upgradeStatusKey: "running"},
		"blacksmith/settings":                {"max_batch_jobs": 2},
		"secret/blacksmith/registrations/r1": {registrationNameKey: "cf"},
		"upgrade-settings":                   {"max_batch_jobs": 2},
	}

	for path, data := range writes {
		err := client.Put(ctx, path, data)
		if err != nil {
			t.Fatalf("writing %s: %s", path, err)
		}
	}

	ids, indexListed, err := client.ListOperationRecordIDs(ctx)
	if err != nil {
		t.Fatalf("listing the mount: %s", err)
	}

	if indexListed {
		t.Fatalf("the listing reported the index, which was never written")
	}

	sort.Strings(ids)

	if fmt.Sprint(ids) != "[instance-a instance-b]" {
		t.Fatalf("the listing named %v, want [instance-a instance-b]", ids)
	}

	err = client.Index(ctx, "instance-a", map[string]interface{}{"plan_id": "standalone"})
	if err != nil {
		t.Fatalf("writing the index: %s", err)
	}

	_, indexListed, err = client.ListOperationRecordIDs(ctx)
	if err != nil {
		t.Fatalf("listing the mount again: %s", err)
	}

	if !indexListed {
		t.Fatalf("the listing did not report the index once it was written")
	}
}

// TrackDeleted records a finished delete over the deprovision that sent
// it, named by its context ID, and over nothing else.
func TestTrackDeletedLeavesANewerOperationAlone(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := newDevVault(t)

	t.Run("records the delete that carried the context ID", func(t *testing.T) {
		t.Parallel()

		instanceID := fmt.Sprintf("deleted-same-%d", time.Now().UnixNano())

		err := client.TrackProgressWithDeployment(ctx, instanceID, "deprovision", "BOSH deletion in progress (task 51)", "context-51", "a-deployment", 51, nil)
		if err != nil {
			t.Fatalf("recording task 51: %s", err)
		}

		err = client.TrackDeleted(ctx, instanceID, "context-51", "Deleted")
		if err != nil {
			t.Fatalf("TrackDeleted over context ID context-51: %s", err)
		}

		state, _, record := readRecord(ctx, t, client, instanceID)
		if state != vault.TaskStateDeleted || record["context_id"] != "context-51" || record["deployment_name"] != "a-deployment" || record["description"] != "Deleted" {
			t.Fatalf("the record is %v, want the state %s with its context ID and deployment kept", record, vault.TaskStateDeleted)
		}
	})

	newer := []newerOperation{
		{
			name: "a deprovision with another context ID",
			record: func(instanceID string) error {
				return client.TrackProgressWithDeployment(ctx, instanceID, "deprovision", "Deleting", "context-52", "a-deployment", 0, nil)
			},
			state: recordStateInitializing,
		},
		{
			name: "a provision",
			record: func(instanceID string) error {
				return client.TrackProgress(ctx, instanceID, "provision", "BOSH deployment in progress (task 53)", 53, nil)
			},
			state:  recordStateInProgress,
			taskID: 53,
		},
	}

	for index, operation := range newer {
		t.Run("leaves "+operation.name+" alone", func(t *testing.T) {
			t.Parallel()

			instanceID := fmt.Sprintf("deleted-newer-%d-%d", index, time.Now().UnixNano())
			expectTrackDeletedLeaves(t, client, instanceID, operation)
		})
	}

	t.Run("writes nothing when there is no record", func(t *testing.T) {
		t.Parallel()

		instanceID := fmt.Sprintf("deleted-none-%d", time.Now().UnixNano())

		err := client.TrackDeleted(ctx, instanceID, "context-51", "Deleted")
		if !errors.Is(err, vault.ErrTaskRecordReplaced) {
			t.Fatalf("TrackDeleted with no record returned %v, want an error wrapping ErrTaskRecordReplaced", err)
		}

		state, _, _ := readRecord(ctx, t, client, instanceID)
		if state != "not_found" {
			t.Fatalf("the record is %s, want none", state)
		}
	})
}

// expectTrackDeletedLeaves records a delete with the context ID
// context-51, records the newer operation over it, and checks that
// TrackDeleted for that context ID leaves the newer record as it is.
func expectTrackDeletedLeaves(t *testing.T, client *vault.Vault, instanceID string, operation newerOperation) {
	t.Helper()

	ctx := context.Background()

	err := client.TrackProgressWithDeployment(ctx, instanceID, "deprovision", "Deleting", "context-51", "a-deployment", 0, nil)
	if err != nil {
		t.Fatalf("recording the first delete: %s", err)
	}

	err = operation.record(instanceID)
	if err != nil {
		t.Fatalf("recording %s: %s", operation.name, err)
	}

	err = client.TrackDeleted(ctx, instanceID, "context-51", "Deleted")
	if !errors.Is(err, vault.ErrTaskRecordReplaced) {
		t.Fatalf("TrackDeleted over %s returned %v, want an error wrapping ErrTaskRecordReplaced", operation.name, err)
	}

	state, taskID, _ := readRecord(ctx, t, client, instanceID)
	if state != operation.state || taskID != operation.taskID {
		t.Fatalf("the record is %s with task %d, want %s left as %s with task %d", state, taskID, operation.name, operation.state, operation.taskID)
	}
}
