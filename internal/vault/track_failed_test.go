package vault_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"blacksmith/internal/vault"
)

// guardedWriteCase is a record a guarded write runs over, and whether
// the write lands on it.
type guardedWriteCase struct {
	name   string
	record func(ctx context.Context, client *vault.Vault, instanceID string) error
	lands  bool
}

// recordProvisionTask41 records provision task 41 with context ID
// context-41.
func recordProvisionTask41(ctx context.Context, client *vault.Vault, instanceID string) error {
	err := client.TrackProgressWithContextID(ctx, instanceID, "provision", "Starting provisioning process", "context-41", 0, nil)
	if err == nil {
		err = client.TrackProgress(ctx, instanceID, "provision", "BOSH deployment in progress (task 41)", 41, nil)
	}

	if err != nil {
		return fmt.Errorf("recording provision task 41: %w", err)
	}

	return nil
}

// recordNewerUpdate records an update over whatever the instance holds.
func recordNewerUpdate(ctx context.Context, client *vault.Vault, instanceID string) error {
	err := client.TrackProgressWithContextID(ctx, instanceID, "update", "Update request accepted", "context-update", 0, nil)
	if err != nil {
		return fmt.Errorf("recording an update: %w", err)
	}

	return nil
}

// runGuardedWrite records the case, runs write, and checks that the write
// landed, or that it returned ErrTaskRecordReplaced and left the record
// as it was.
func runGuardedWrite(t *testing.T, client *vault.Vault, prefix string, index int, test guardedWriteCase, write func(ctx context.Context, instanceID string) error, landed string) {
	t.Helper()

	ctx := context.Background()
	instanceID := fmt.Sprintf("%s-%d-%d", prefix, index, time.Now().UnixNano())

	if test.record != nil {
		err := test.record(ctx, client, instanceID)
		if err != nil {
			t.Fatalf("recording %s: %s", test.name, err)
		}
	}

	beforeState, beforeTask, before := readRecord(ctx, t, client, instanceID)

	err := write(ctx, instanceID)

	state, taskID, record := readRecord(ctx, t, client, instanceID)

	if test.lands {
		if err != nil {
			t.Fatalf("the write over %s returned %v, want it to land", test.name, err)
		}

		if state != landed {
			t.Fatalf("the record is %s with task %d, want %s", state, taskID, landed)
		}

		return
	}

	if !errors.Is(err, vault.ErrTaskRecordReplaced) {
		t.Fatalf("the write over %s returned %v, want an error wrapping ErrTaskRecordReplaced", test.name, err)
	}

	if state != beforeState || taskID != beforeTask || fmt.Sprint(record["action"]) != fmt.Sprint(before["action"]) {
		t.Fatalf("the record changed from a %v %s with task %d to a %v %s with task %d", before["action"], beforeState, beforeTask, record["action"], state, taskID)
	}
}

// TrackFailed records the failure of provision task 41 only while the
// record holds that provision unsettled, and leaves every other record
// as it is.
func TestTrackFailedLandsOnlyOnTheOperationThatFailed(t *testing.T) {
	t.Parallel()

	client := newDevVault(t)

	cases := []guardedWriteCase{
		{"provision task 41", recordProvisionTask41, true},
		{"an update accepted since", func(ctx context.Context, client *vault.Vault, instanceID string) error {
			err := recordProvisionTask41(ctx, client, instanceID)
			if err != nil {
				return err
			}

			return recordNewerUpdate(ctx, client, instanceID)
		}, false},
		{"a provision with another task", func(ctx context.Context, client *vault.Vault, instanceID string) error {
			return client.TrackProgressWithContextID(ctx, instanceID, "provision", "BOSH deployment in progress (task 42)", "context-41", 42, nil)
		}, false},
		{"a provision with another context ID", func(ctx context.Context, client *vault.Vault, instanceID string) error {
			return client.TrackProgressWithContextID(ctx, instanceID, "provision", "BOSH deployment in progress (task 41)", "context-other", 41, nil)
		}, false},
		{"provision task 41 already succeeded", func(ctx context.Context, client *vault.Vault, instanceID string) error {
			err := recordProvisionTask41(ctx, client, instanceID)
			if err != nil {
				return err
			}

			return client.TrackSucceeded(ctx, instanceID, "provision", "Provisioned", 41)
		}, false},
		{"no record", nil, false},
	}

	for index, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			runGuardedWrite(t, client, "failed", index, test, func(ctx context.Context, instanceID string) error {
				return client.TrackFailed(ctx, instanceID, "provision", "BOSH deployment task 41 is error", "context-41", 41)
			}, "failed")
		})
	}
}

// TrackFoundTask records a found task only over a provision with no task
// that is still unsettled.
func TestTrackFoundTaskLandsOnlyOnAProvisionWithNoTask(t *testing.T) {
	t.Parallel()

	client := newDevVault(t)

	recordAccepted := func(ctx context.Context, client *vault.Vault, instanceID string) error {
		return client.TrackProgressWithContextID(ctx, instanceID, "provision", "Starting provisioning process", "context-41", 0, nil)
	}

	cases := []guardedWriteCase{
		{"a provision with no task", recordAccepted, true},
		{"an update accepted since", func(ctx context.Context, client *vault.Vault, instanceID string) error {
			err := recordAccepted(ctx, client, instanceID)
			if err != nil {
				return err
			}

			return recordNewerUpdate(ctx, client, instanceID)
		}, false},
		{"a provision with a task", recordProvisionTask41, false},
		{"a provision whose outcome is unknown", func(ctx context.Context, client *vault.Vault, instanceID string) error {
			return client.TrackOutcomeUnknown(ctx, instanceID, "provision", "The deploy's answer was lost", "context-41", time.Now())
		}, false},
		{"no record", nil, false},
	}

	for index, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			runGuardedWrite(t, client, "found", index, test, func(ctx context.Context, instanceID string) error {
				return client.TrackFoundTask(ctx, instanceID, "provision", "BOSH deployment in progress (task 41)", 41)
			}, "in_progress")
		})
	}
}
