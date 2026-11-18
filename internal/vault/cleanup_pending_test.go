package vault_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"blacksmith/internal/vault"
)

// A provision failure that owes the cleanup of its deployment says so in
// the same write that records the failure, so a broker that stops before
// the cleanup runs, or a poll whose write landed while its answer was
// lost, still finds the cleanup owed.
func TestFailureWritesCarryTheCleanupMark(t *testing.T) {
	t.Parallel()

	client := newDevVault(t)
	ctx := context.Background()

	writes := []struct {
		name   string
		record func(instanceID string) error
		write  func(instanceID string) error
		marked bool
	}{
		{"a failure owing its cleanup", func(instanceID string) error {
			return recordProvisionTask41(ctx, client, instanceID)
		}, func(instanceID string) error {
			return client.TrackFailedCleanupPending(ctx, instanceID, "provision", "BOSH deployment task 41 is error", "context-41", 41)
		}, true},
		{"a failure owing nothing", func(instanceID string) error {
			return recordProvisionTask41(ctx, client, instanceID)
		}, func(instanceID string) error {
			return client.TrackFailed(ctx, instanceID, "provision", "BOSH deployment task 41 is error", "context-41", 41)
		}, false},
		{"a settled unknown outcome owing its cleanup", func(instanceID string) error {
			return client.TrackOutcomeUnknown(ctx, instanceID, "provision", "The deploy's answer was lost", "context-41", time.Now())
		}, func(instanceID string) error {
			return client.SettleOutcomeUnknownCleanupPending(ctx, instanceID, "provision", "The director lists no task", "context-41")
		}, true},
		{"a settled unknown outcome owing nothing", func(instanceID string) error {
			return client.TrackOutcomeUnknown(ctx, instanceID, "provision", "The deploy's answer was lost", "context-41", time.Now())
		}, func(instanceID string) error {
			return client.SettleOutcomeUnknown(ctx, instanceID, "provision", "The director lists no task", "context-41", -1)
		}, false},
	}

	for index, test := range writes {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			instanceID := fmt.Sprintf("cleanup-mark-%d-%d", index, time.Now().UnixNano())

			err := test.record(instanceID)
			if err == nil {
				err = test.write(instanceID)
			}

			if err != nil {
				t.Fatalf("writing the failure: %s", err)
			}

			state, _, record := readRecord(ctx, t, client, instanceID)
			marked, _ := record[vault.TaskRecordCleanupPendingKey].(bool)

			if state != "failed" || marked != test.marked {
				t.Fatalf("the record is %s with the cleanup mark %v, want failed with the mark %v", state, marked, test.marked)
			}
		})
	}
}

// ClearCleanupPending removes the mark from the failed provision that
// holds it and keeps the rest of the record. Over any other record it
// writes nothing.
func TestClearCleanupPendingLandsOnlyOnTheMarkedFailure(t *testing.T) {
	t.Parallel()

	client := newDevVault(t)
	cases := clearMarkCases()

	for index, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			runGuardedWrite(t, client, "clear-mark", index, test, func(ctx context.Context, instanceID string) error {
				err := client.ClearCleanupPending(ctx, instanceID, "provision", "context-41")
				if err != nil {
					return fmt.Errorf("clearing the mark: %w", err)
				}

				if !test.lands {
					return nil
				}

				_, taskID, record := readRecord(ctx, t, client, instanceID)
				if _, kept := record[vault.TaskRecordCleanupPendingKey]; kept {
					t.Errorf("the record still holds the cleanup mark: %v", record)
				}

				if taskID != -1 || record["description"] != "BOSH deployment task 41 is error" || record["context_id"] != "context-41" {
					t.Errorf("clearing the mark changed the rest of the record: %v", record)
				}

				return nil
			}, "failed")
		})
	}
}

// A mark that is already cleared is reported as such, so the second of two
// cleanups knows that the first one ran.
func TestClearCleanupPendingTwice(t *testing.T) {
	t.Parallel()

	client := newDevVault(t)
	ctx := context.Background()
	instanceID := fmt.Sprintf("clear-mark-twice-%d", time.Now().UnixNano())

	err := recordProvisionTask41(ctx, client, instanceID)
	if err == nil {
		err = client.TrackFailedCleanupPending(ctx, instanceID, "provision", "BOSH deployment task 41 is error", "context-41", 41)
	}

	if err == nil {
		err = client.ClearCleanupPending(ctx, instanceID, "provision", "context-41")
	}

	if err != nil {
		t.Fatalf("clearing the mark: %s", err)
	}

	err = client.ClearCleanupPending(ctx, instanceID, "provision", "context-41")
	if !errors.Is(err, vault.ErrTaskRecordReplaced) {
		t.Fatalf("clearing the mark again returned %v, want an error wrapping ErrTaskRecordReplaced", err)
	}
}

// recordMarkedFailure records provision task 41 as failed, owing its
// cleanup.
func recordMarkedFailure(ctx context.Context, client *vault.Vault, instanceID string) error {
	err := recordProvisionTask41(ctx, client, instanceID)
	if err == nil {
		err = client.TrackFailedCleanupPending(ctx, instanceID, "provision", "BOSH deployment task 41 is error", "context-41", 41)
	}

	if err != nil {
		return fmt.Errorf("recording the marked failure: %w", err)
	}

	return nil
}

// clearMarkCases are the records ClearCleanupPending runs over. Only the
// marked failure loses its mark.
func clearMarkCases() []guardedWriteCase {
	return []guardedWriteCase{
		{"the marked failure", recordMarkedFailure, true},
		{"a failure with no mark", func(ctx context.Context, client *vault.Vault, instanceID string) error {
			err := recordProvisionTask41(ctx, client, instanceID)
			if err == nil {
				err = client.TrackFailed(ctx, instanceID, "provision", "BOSH deployment task 41 is error", "context-41", 41)
			}

			if err != nil {
				return fmt.Errorf("recording the failure: %w", err)
			}

			return nil
		}, false},
		{"a deprovision accepted since", func(ctx context.Context, client *vault.Vault, instanceID string) error {
			err := recordMarkedFailure(ctx, client, instanceID)
			if err == nil {
				err = client.TrackProgressWithContextID(ctx, instanceID, "deprovision", "Deprovision request accepted", "context-delete", 0, nil)
			}

			if err != nil {
				return fmt.Errorf("recording the deprovision: %w", err)
			}

			return nil
		}, false},
		{"a provision still running", recordProvisionTask41, false},
		{"an instance with no record", nil, false},
	}
}
