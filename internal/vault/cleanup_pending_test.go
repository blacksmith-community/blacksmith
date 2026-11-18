package vault_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"blacksmith/internal/vault"
)

// failedContextID is the context ID of the failed provision these tests
// record.
const failedContextID = "context-41"

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
			return client.TrackFailedCleanupPending(ctx, instanceID, "provision", "BOSH deployment task 41 is error", failedContextID, 41)
		}, true},
		{"a failure owing nothing", func(instanceID string) error {
			return recordProvisionTask41(ctx, client, instanceID)
		}, func(instanceID string) error {
			return client.TrackFailed(ctx, instanceID, "provision", "BOSH deployment task 41 is error", failedContextID, 41)
		}, false},
		{"a settled unknown outcome owing its cleanup", func(instanceID string) error {
			return client.TrackOutcomeUnknown(ctx, instanceID, "provision", "The deploy's answer was lost", failedContextID, time.Now())
		}, func(instanceID string) error {
			return client.SettleOutcomeUnknownCleanupPending(ctx, instanceID, "provision", "The director lists no task", failedContextID)
		}, true},
		{"a settled unknown outcome owing nothing", func(instanceID string) error {
			return client.TrackOutcomeUnknown(ctx, instanceID, "provision", "The deploy's answer was lost", failedContextID, time.Now())
		}, func(instanceID string) error {
			return client.SettleOutcomeUnknown(ctx, instanceID, "provision", "The director lists no task", failedContextID, -1)
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
				err := client.ClearCleanupPending(ctx, instanceID, "provision", failedContextID)
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

				if taskID != -1 || record["description"] != "BOSH deployment task 41 is error" || record["context_id"] != failedContextID {
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
		err = client.TrackFailedCleanupPending(ctx, instanceID, "provision", "BOSH deployment task 41 is error", failedContextID, 41)
	}

	if err == nil {
		err = client.ClearCleanupPending(ctx, instanceID, "provision", failedContextID)
	}

	if err != nil {
		t.Fatalf("clearing the mark: %s", err)
	}

	err = client.ClearCleanupPending(ctx, instanceID, "provision", failedContextID)
	if !errors.Is(err, vault.ErrTaskRecordReplaced) {
		t.Fatalf("clearing the mark again returned %v, want an error wrapping ErrTaskRecordReplaced", err)
	}
}

// recordMarkedFailure records provision task 41 as failed, owing its
// cleanup.
func recordMarkedFailure(ctx context.Context, client *vault.Vault, instanceID string) error {
	err := recordProvisionTask41(ctx, client, instanceID)
	if err == nil {
		err = client.TrackFailedCleanupPending(ctx, instanceID, "provision", "BOSH deployment task 41 is error", failedContextID, 41)
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
				err = client.TrackFailed(ctx, instanceID, "provision", "BOSH deployment task 41 is error", failedContextID, 41)
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

// NameCleanupDeployment names the deployment of a marked failure whose
// record names none, with the name minted under the plan it is given and
// that plan, and writes nothing over any other record.
func TestNameCleanupDeploymentLandsOnlyOnAnUnnamedMarkedFailure(t *testing.T) {
	t.Parallel()

	client := newDevVault(t)

	recordFailure := func(ctx context.Context, instanceID string) error {
		return client.TrackFailedCleanupPending(ctx, instanceID, "provision", "BOSH deployment task 41 is error", failedContextID, 41)
	}

	cases := []struct {
		name      string
		prepare   func(ctx context.Context, instanceID string) error
		contextID string
		lands     bool
	}{
		{"unnamed and marked", recordFailure, failedContextID, true},
		{"already named", func(ctx context.Context, instanceID string) error {
			err := client.TrackProgressWithDeployment(ctx, instanceID, "provision", "deploying", failedContextID, "standalone-other", 41, nil)
			if err != nil {
				return fmt.Errorf("naming the deployment: %w", err)
			}

			return recordFailure(ctx, instanceID)
		}, failedContextID, false},
		{"mark cleared", func(ctx context.Context, instanceID string) error {
			err := recordFailure(ctx, instanceID)
			if err != nil {
				return fmt.Errorf("recording the failure: %w", err)
			}

			return client.ClearCleanupPending(ctx, instanceID, "provision", failedContextID)
		}, failedContextID, false},
		{"another context ID", recordFailure, "context-42", false},
	}

	for index, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			instanceID := fmt.Sprintf("name-cleanup-%d-%d", index, time.Now().UnixNano())

			err := recordProvisionTask41(ctx, client, instanceID)
			if err == nil {
				err = test.prepare(ctx, instanceID)
			}

			if err != nil {
				t.Fatalf("preparing the record: %s", err)
			}

			_, _, before := readRecord(ctx, t, client, instanceID)

			err = client.NameCleanupDeployment(ctx, instanceID, "provision", test.contextID, "standalone")

			_, _, after := readRecord(ctx, t, client, instanceID)

			requireNaming(t, err, before, after, "standalone-"+instanceID, test.lands)
		})
	}
}

// requireNaming checks that a naming that lands writes want with the
// standalone plan and keeps the mark and the context ID, and that one
// refused wraps ErrTaskRecordReplaced and leaves the name and the plan as
// they were.
func requireNaming(t *testing.T, err error, before, after map[string]interface{}, want string, lands bool) {
	t.Helper()

	if lands {
		if err != nil {
			t.Fatalf("naming the deployment: %s", err)
		}

		if after[vault.TaskRecordDeploymentNameKey] != want || after[vault.TaskRecordDeploymentPlanIDKey] != "standalone" ||
			after[vault.TaskRecordCleanupPendingKey] != true || after["context_id"] != failedContextID {
			t.Fatalf("the record after naming is %v", after)
		}

		return
	}

	if !errors.Is(err, vault.ErrTaskRecordReplaced) {
		t.Fatalf("naming returned %v, want an error wrapping ErrTaskRecordReplaced", err)
	}

	if after[vault.TaskRecordDeploymentNameKey] != before[vault.TaskRecordDeploymentNameKey] ||
		after[vault.TaskRecordDeploymentPlanIDKey] != before[vault.TaskRecordDeploymentPlanIDKey] {
		t.Fatalf("a refused naming changed the record from %v to %v", before, after)
	}
}

// NameCleanupDeployment refuses an empty plan before it writes anything,
// since no name can be minted under it.
func TestNameCleanupDeploymentRefusesAnEmptyPlan(t *testing.T) {
	t.Parallel()

	client := newDevVault(t)
	ctx := context.Background()
	instanceID := fmt.Sprintf("name-cleanup-empty-plan-%d", time.Now().UnixNano())

	err := recordProvisionTask41(ctx, client, instanceID)
	if err == nil {
		err = client.TrackFailedCleanupPending(ctx, instanceID, "provision", "BOSH deployment task 41 is error", failedContextID, 41)
	}

	if err != nil {
		t.Fatalf("preparing the record: %s", err)
	}

	_, _, before := readRecord(ctx, t, client, instanceID)

	err = client.NameCleanupDeployment(ctx, instanceID, "provision", failedContextID, "")

	_, _, after := readRecord(ctx, t, client, instanceID)

	if !errors.Is(err, vault.ErrEmptyPlanID) {
		t.Fatalf("naming under an empty plan returned %v, want an error wrapping ErrEmptyPlanID", err)
	}

	if after[vault.TaskRecordDeploymentNameKey] != before[vault.TaskRecordDeploymentNameKey] ||
		after[vault.TaskRecordDeploymentPlanIDKey] != before[vault.TaskRecordDeploymentPlanIDKey] {
		t.Fatalf("a naming under an empty plan changed the record from %v to %v", before, after)
	}
}
