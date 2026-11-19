package vault_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"blacksmith/internal/vault"
)

// The plan the specs' provisions mint their deployment names under, and
// the state a failed operation records.
const (
	updatePlan  = "small"
	stateFailed = "failed"
)

// recordSucceededMintedProvision records a provision of plan small that
// minted small-<id> and succeeded in task 41.
func recordSucceededMintedProvision(ctx context.Context, client *vault.Vault, instanceID string) error {
	err := client.TrackProgressWithMintedDeployment(ctx, instanceID, "provision", "Creating BOSH deployment", "context-41", updatePlan, 0, nil)
	if err == nil {
		err = client.TrackProgress(ctx, instanceID, "provision", "BOSH deployment in progress (task 41)", 41, nil)
	}

	if err == nil {
		err = client.TrackSucceeded(ctx, instanceID, "provision", "Provisioned", 41)
	}

	if err != nil {
		return fmt.Errorf("recording a succeeded provision: %w", err)
	}

	return nil
}

func stagedUpdate() vault.StagedUpdate {
	return vault.StagedUpdate{
		Fingerprint: stagedDigest, PreviousPlanID: updatePlan, PlanID: updatePlan,
		Parameters: map[string]interface{}{"maxmemory-policy": "allkeys-lru"},
	}
}

// An accepted update keeps the deployment name and the plan the
// provision minted it under, and its staged values, through every later
// write of the same update, an unknown outcome included.
func TestTrackUpdateAccepted_CarriesTheMintedDeploymentForward(t *testing.T) {
	t.Parallel()

	client := newDevVault(t)
	ctx := context.Background()
	instanceID := fmt.Sprintf("update-carry-%d", time.Now().UnixNano())

	err := recordSucceededMintedProvision(ctx, client, instanceID)
	if err != nil {
		t.Fatal(err)
	}

	err = client.TrackUpdateAccepted(ctx, instanceID, "Update request accepted", "context-update", "index-name", stagedUpdate())
	if err != nil {
		t.Fatalf("the update was not recorded: %s", err)
	}

	requireCarried := func(step string) {
		t.Helper()

		_, _, record := readRecord(ctx, t, client, instanceID)
		if record["deployment_name"] != updatePlan+"-"+instanceID || record["deployment_plan_id"] != updatePlan {
			t.Fatalf("%s: the record names %v under plan %v, want the minted small-%s under small", step, record["deployment_name"], record["deployment_plan_id"], instanceID)
		}

		staged, _ := record[vault.TaskRecordUpdateKey].(map[string]interface{})
		if staged["fingerprint"] != stagedDigest || staged["plan_id"] != updatePlan {
			t.Fatalf("%s: the staged update is %v", step, staged)
		}
	}

	requireCarried("accepted")

	err = client.TrackProgress(ctx, instanceID, "update", "Deploying (task 42)", 42, nil)
	if err != nil {
		t.Fatal(err)
	}

	requireCarried("progress")

	err = client.TrackOutcomeUnknown(ctx, instanceID, "update", "lost", "context-update", time.Now())
	if err != nil {
		t.Fatal(err)
	}

	requireCarried("unknown outcome")
}

// With no name on record, the update records the name the index holds,
// with no plan, since the broker can't say it minted it.
func TestTrackUpdateAccepted_NamesTheIndexedDeploymentWhenNoneIsRecorded(t *testing.T) {
	t.Parallel()

	client := newDevVault(t)
	ctx := context.Background()
	instanceID := fmt.Sprintf("update-unnamed-%d", time.Now().UnixNano())

	err := client.TrackUpdateAccepted(ctx, instanceID, "Update request accepted", "context-update", "index-name", stagedUpdate())
	if err != nil {
		t.Fatalf("an update over no record was refused: %s", err)
	}

	state, _, record := readRecord(ctx, t, client, instanceID)
	if state != "initializing" || record["action"] != "update" || record["deployment_name"] != "index-name" || record["deployment_plan_id"] != nil {
		t.Fatalf("the record is %v", record)
	}
}

// An update replaces only a settled operation that owes nothing.
func TestTrackUpdateAccepted_RefusesWhatItMustNotReplace(t *testing.T) {
	t.Parallel()

	client := newDevVault(t)

	cases := []struct {
		name   string
		record func(ctx context.Context, instanceID string) error
		want   error
		// kind is the finer refusal the error also wraps, or nil for one
		// that must wrap neither ErrInstanceDeleted nor ErrInstanceNotReady.
		kind error
	}{
		{"a provision that owes a cleanup", func(ctx context.Context, instanceID string) error {
			err := client.TrackProgressWithMintedDeployment(ctx, instanceID, "provision", "Creating", "context-41", updatePlan, 41, nil)
			if err != nil {
				return fmt.Errorf("recording a provision: %w", err)
			}

			return client.TrackFailedCleanupPending(ctx, instanceID, "provision", "failed", "context-41", 41)
		}, vault.ErrCleanupOwed, nil},
		{"a provision still running", func(ctx context.Context, instanceID string) error {
			return client.TrackProgressWithContextID(ctx, instanceID, "provision", "Creating", "context-41", 41, nil)
		}, vault.ErrUpdateNotAllowed, vault.ErrInstanceNotReady},
		{"a provision that failed", func(ctx context.Context, instanceID string) error {
			err := client.TrackProgressWithContextID(ctx, instanceID, "provision", "Creating", "context-41", 41, nil)
			if err != nil {
				return fmt.Errorf("recording a provision: %w", err)
			}

			return client.TrackFailed(ctx, instanceID, "provision", "failed", "context-41", 41)
		}, vault.ErrUpdateNotAllowed, vault.ErrInstanceNotReady},
		{"a deprovision", func(ctx context.Context, instanceID string) error {
			return client.TrackProgressWithContextID(ctx, instanceID, "deprovision", "Deleting", "context-del", 0, nil)
		}, vault.ErrUpdateNotAllowed, vault.ErrInstanceDeleted},
		{"an update still running", func(ctx context.Context, instanceID string) error {
			return client.TrackUpdateAccepted(ctx, instanceID, "accepted", "context-first", "index-name", stagedUpdate())
		}, vault.ErrUpdateNotAllowed, nil},
		{"an unknown outcome", func(ctx context.Context, instanceID string) error {
			return client.TrackOutcomeUnknown(ctx, instanceID, "provision", "lost", "context-41", time.Now())
		}, vault.ErrOutcomeUnknownRecorded, nil},
	}

	for index, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			instanceID := fmt.Sprintf("update-refused-%d-%d", index, time.Now().UnixNano())

			err := test.record(ctx, instanceID)
			if err != nil {
				t.Fatal(err)
			}

			requireUpdateRefused(ctx, t, client, instanceID, test.want, test.kind)
		})
	}
}

// requireUpdateRefused fails the test unless an update over the record of
// instanceID is refused with want, wraps kind as its only finer refusal,
// and leaves the record as it was.
func requireUpdateRefused(ctx context.Context, t *testing.T, client *vault.Vault, instanceID string, want, kind error) {
	t.Helper()

	_, _, before := readRecord(ctx, t, client, instanceID)

	err := client.TrackUpdateAccepted(ctx, instanceID, "Update request accepted", "context-update", "index-name", stagedUpdate())
	if !errors.Is(err, want) {
		t.Fatalf("the update returned %v, want %v", err, want)
	}

	refusal := vault.UpdateRefusal(instanceID, before)
	if !errors.Is(refusal, want) {
		t.Fatalf("UpdateRefusal returned %v, want %v", refusal, want)
	}

	for _, finer := range []error{vault.ErrInstanceDeleted, vault.ErrInstanceNotReady} {
		if errors.Is(err, finer) != errors.Is(kind, finer) {
			t.Fatalf("the update returned %v, which should wrap %v only when the case's kind %v is it", err, finer, kind)
		}
	}

	_, _, after := readRecord(ctx, t, client, instanceID)
	if fmt.Sprint(before) != fmt.Sprint(after) {
		t.Fatalf("the refused update changed the record from %v to %v", before, after)
	}
}

// A settled update, failed or succeeded, takes the next update.
func TestTrackUpdateAccepted_ReplacesASettledUpdate(t *testing.T) {
	t.Parallel()

	client := newDevVault(t)
	ctx := context.Background()

	for _, settle := range []string{stateFailed, "succeeded"} {
		instanceID := fmt.Sprintf("update-again-%s-%d", settle, time.Now().UnixNano())

		err := recordSucceededMintedProvision(ctx, client, instanceID)
		if err == nil {
			err = client.TrackUpdateAccepted(ctx, instanceID, "accepted", "context-first", "index-name", stagedUpdate())
		}

		if err == nil {
			err = client.TrackProgress(ctx, instanceID, "update", "Deploying (task 42)", 42, nil)
		}

		if err == nil && settle == stateFailed {
			err = client.TrackFailed(ctx, instanceID, "update", "failed", "context-first", 42)
		}

		if err == nil && settle == "succeeded" {
			err = client.TrackSucceeded(ctx, instanceID, "update", "Updated", 42)
		}

		if err != nil {
			t.Fatal(err)
		}

		err = client.TrackUpdateAccepted(ctx, instanceID, "accepted", "context-second", "index-name", stagedUpdate())
		if err != nil {
			t.Fatalf("an update over a %s update was refused: %s", settle, err)
		}

		_, _, record := readRecord(ctx, t, client, instanceID)
		if record["context_id"] != "context-second" || record["deployment_name"] != updatePlan+"-"+instanceID || record["deployment_plan_id"] != updatePlan {
			t.Fatalf("after a %s update, the record is %v", settle, record)
		}
	}
}
