package vault_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"blacksmith/internal/vault"
	"blacksmith/pkg/testutil"
)

// The states of a recorded operation that has a task and of one that has
// none yet.
const (
	recordStateInProgress   = "in_progress"
	recordStateInitializing = "initializing"
)

// newDevVault starts an in-process Vault that lives as long as the test
// and returns a client of it.
func newDevVault(t *testing.T) *vault.Vault {
	t.Helper()

	server, err := testutil.NewVaultDevServer(t)
	if err != nil {
		t.Fatalf("starting the in-process Vault: %s", err)
	}

	t.Cleanup(server.Close)

	return vault.New(server.Addr, server.RootToken, true)
}

// readRecord returns the state, the task ID, and the whole operation
// record of an instance.
func readRecord(ctx context.Context, t *testing.T, client *vault.Vault, instanceID string) (string, int, map[string]interface{}) {
	t.Helper()

	state, taskID, record, err := client.State(ctx, instanceID)
	if err != nil {
		t.Fatalf("reading the record of %s: %s", instanceID, err)
	}

	return state, taskID, record
}

// newerOperation is an operation recorded over an earlier one, and the
// state and task ID its record keeps.
type newerOperation struct {
	name   string
	record func(instanceID string) error
	state  string
	taskID int
}

// TrackSucceeded records a finished provision over the record of the task
// that finished, and over nothing else. A record that holds a newer
// operation, whether a provision with another task or a deprovision, is
// left as it is.
func TestTrackSucceededLeavesANewerOperationAlone(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := newDevVault(t)

	t.Run("records success over the task that finished", func(t *testing.T) {
		t.Parallel()

		instanceID := fmt.Sprintf("succeeded-same-%d", time.Now().UnixNano())

		err := client.TrackProgressWithContextID(ctx, instanceID, "provision", "BOSH deployment in progress (task 41)", "context-41", 41, nil)
		if err != nil {
			t.Fatalf("recording task 41: %s", err)
		}

		err = client.TrackSucceeded(ctx, instanceID, "provision", "Provisioned", 41)
		if err != nil {
			t.Fatalf("TrackSucceeded over task 41: %s", err)
		}

		state, taskID, record := readRecord(ctx, t, client, instanceID)
		if state != vault.TaskStateSucceeded || taskID != 41 {
			t.Fatalf("the record is %s with task %d, want %s with task 41", state, taskID, vault.TaskStateSucceeded)
		}

		if record["context_id"] != "context-41" || record["description"] != "Provisioned" {
			t.Fatalf("the record is %v, want context ID context-41 kept and the description Provisioned", record)
		}
	})

	for index, operation := range trackSucceededNewer(ctx, client) {
		t.Run("leaves "+operation.name+" alone", func(t *testing.T) {
			t.Parallel()

			instanceID := fmt.Sprintf("succeeded-newer-%d-%d", index, time.Now().UnixNano())
			expectTrackSucceededLeaves(t, client, instanceID, operation)
		})
	}

	t.Run("writes nothing when there is no record", func(t *testing.T) {
		t.Parallel()

		instanceID := fmt.Sprintf("succeeded-none-%d", time.Now().UnixNano())

		err := client.TrackSucceeded(ctx, instanceID, "provision", "Provisioned", 41)
		if !errors.Is(err, vault.ErrTaskRecordReplaced) {
			t.Fatalf("TrackSucceeded with no record returned %v, want an error wrapping ErrTaskRecordReplaced", err)
		}

		state, _, _ := readRecord(ctx, t, client, instanceID)
		if state != "not_found" {
			t.Fatalf("the record is %s, want none", state)
		}
	})
}

// trackSucceededNewer returns the operations that may replace the record
// of provision task 41 before TrackSucceeded runs for it.
func trackSucceededNewer(ctx context.Context, client *vault.Vault) []newerOperation {
	return []newerOperation{
		{
			name: "a provision with another task",
			record: func(instanceID string) error {
				return client.TrackProgress(ctx, instanceID, "provision", "BOSH deployment in progress (task 42)", 42, nil)
			},
			state:  recordStateInProgress,
			taskID: 42,
		},
		{
			name: "a deprovision",
			record: func(instanceID string) error {
				return client.TrackProgressWithDeployment(ctx, instanceID, "deprovision", "Deleting", "context-delete", "a-deployment", 0, nil)
			},
			state:  recordStateInitializing,
			taskID: 0,
		},
		{
			name: "a deprovision with the same task ID",
			record: func(instanceID string) error {
				return client.TrackProgress(ctx, instanceID, "deprovision", "BOSH deletion in progress (task 41)", 41, nil)
			},
			state:  recordStateInProgress,
			taskID: 41,
		},
	}
}

// expectTrackSucceededLeaves records task 41 of a provision, records the
// newer operation over it, and checks that TrackSucceeded for task 41
// leaves the newer record as it is.
func expectTrackSucceededLeaves(t *testing.T, client *vault.Vault, instanceID string, operation newerOperation) {
	t.Helper()

	ctx := context.Background()

	err := client.TrackProgress(ctx, instanceID, "provision", "BOSH deployment in progress (task 41)", 41, nil)
	if err != nil {
		t.Fatalf("recording task 41: %s", err)
	}

	err = operation.record(instanceID)
	if err != nil {
		t.Fatalf("recording %s: %s", operation.name, err)
	}

	err = client.TrackSucceeded(ctx, instanceID, "provision", "Provisioned", 41)
	if !errors.Is(err, vault.ErrTaskRecordReplaced) {
		t.Fatalf("TrackSucceeded over %s returned %v, want an error wrapping ErrTaskRecordReplaced", operation.name, err)
	}

	state, taskID, _ := readRecord(ctx, t, client, instanceID)
	if state != operation.state || taskID != operation.taskID {
		t.Fatalf("the record is %s with task %d, want %s left as %s with task %d", state, taskID, operation.name, operation.state, operation.taskID)
	}
}
