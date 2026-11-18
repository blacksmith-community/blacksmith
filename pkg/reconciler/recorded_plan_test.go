package reconciler_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	. "blacksmith/pkg/reconciler"
)

// The index entry fields these tests seed, and the reason the stand-in
// matcher gives.
const (
	indexServiceKey    = "service_id"
	indexPlanKey       = "plan_id"
	indexDeploymentKey = "deployment_name"
	exactNameMatch     = "exact_name_match"
)

// staleNameMatcher infers the plan from the deployment name, as the real
// matcher does, so it names the plan the deployment was created under.
type staleNameMatcher struct {
	serviceID, planID string
}

func (m staleNameMatcher) MatchDeployment(deployment DeploymentDetail, _ []Service) (*MatchResult, error) {
	return &MatchResult{
		ServiceID:   m.serviceID,
		PlanID:      m.planID,
		InstanceID:  deploymentInstanceID(deployment.Name),
		Confidence:  1.0,
		MatchReason: exactNameMatch,
	}, nil
}

// deploymentInstanceID returns the instance ID that ends a deployment name.
func deploymentInstanceID(name string) string {
	const uuidLength = 36

	return name[len(name)-uuidLength:]
}

// An instance whose plan changed keeps the deployment it was created under,
// so the deployment's name still starts with the old plan. A reconcile pass
// infers that old plan from the name, and it must leave the plan the index
// records alone, in both the index and the instance the updater writes.
// The deployment name the entry records is valid for the instance, even
// though it no longer starts with the recorded plan.
func TestReconciler_KeepsRecordedPlanID(t *testing.T) {
	t.Parallel()

	const (
		instanceID     = "481c0984-f4b6-461c-83ff-98ff41872f4b"
		recordedPlanID = "valkey-large"
		inferredPlanID = "valkey-standalone"
		deploymentName = inferredPlanID + "-" + instanceID
	)

	manager, scan, upd, synchronizer := newUnclaimedTestManager(t, nil)
	manager.Matcher = staleNameMatcher{serviceID: unclaimedTestService, planID: inferredPlanID}
	scan.deployments = []DeploymentInfo{{Name: deploymentName}}

	idx, err := synchronizer.GetVaultIndex()
	if err != nil {
		t.Fatalf("failed to read index: %v", err)
	}

	idx[instanceID] = map[string]interface{}{
		indexServiceKey:    unclaimedTestService,
		indexPlanKey:       recordedPlanID,
		indexDeploymentKey: deploymentName,
	}

	err = synchronizer.SaveVaultIndex(idx)
	if err != nil {
		t.Fatalf("failed to seed index: %v", err)
	}

	manager.RunReconciliation(context.Background())

	calls := upd.getCalls()
	if len(calls) != 1 {
		t.Fatalf("expected one vault update, got %d", len(calls))
	}

	if calls[0].PlanID != recordedPlanID {
		t.Errorf("the updater was handed plan %q, want the recorded plan %q", calls[0].PlanID, recordedPlanID)
	}

	idx, err = synchronizer.GetVaultIndex()
	if err != nil {
		t.Fatalf("failed to read index: %v", err)
	}

	entry, _ := idx[instanceID].(map[string]interface{})
	if entry[indexPlanKey] != recordedPlanID {
		t.Errorf("the index entry's plan is %v after a reconcile, want the recorded plan %q", entry[indexPlanKey], recordedPlanID)
	}

	if entry[indexDeploymentKey] != deploymentName {
		t.Errorf("the index entry's deployment is %v after a reconcile, want %q", entry[indexDeploymentKey], deploymentName)
	}

	err = synchronizer.ValidateIndex(context.Background())
	if err != nil {
		t.Errorf("the index entry of an instance whose plan changed failed validation: %v", err)
	}
}

// A recorded deployment name that ends with another instance's ID names a
// deployment the entry does not own, and validation still reports it.
func TestValidateIndex_RefusesANameOfAnotherInstance(t *testing.T) {
	t.Parallel()

	const (
		instanceID = "581c0984-f4b6-461c-83ff-98ff41872f4b"
		otherID    = "681c0984-f4b6-461c-83ff-98ff41872f4b"
	)

	vault := NewTestVault(t)

	err := vault.Put("db", map[string]interface{}{
		instanceID: map[string]interface{}{
			indexServiceKey:    unclaimedTestService,
			indexPlanKey:       unclaimedTestService + "-standalone",
			indexDeploymentKey: unclaimedTestService + "-standalone-" + otherID,
		},
	})
	if err != nil {
		t.Fatalf("failed to seed index: %v", err)
	}

	synchronizer := NewIndexSynchronizer(vault, NewMockLogger())

	err = synchronizer.ValidateIndex(context.Background())
	if !errors.Is(err, ErrIndexValidationFailed) || !strings.Contains(err.Error(), instanceID) {
		t.Errorf("expected validation to report instance %s, got %v", instanceID, err)
	}
}

// The synchronizer keeps an entry's recorded plan on its own, apart from
// the reconcile pass that hands it the recorded plan. A sync that is
// handed the plan inferred from the deployment's name, as it is when
// that pass is skipped or the entry changed between the two reads,
// still leaves the recorded plan in the index.
func TestSyncIndexWithValidation_KeepsRecordedPlanID(t *testing.T) {
	t.Parallel()

	const (
		instanceID     = "5d0f1c2a-9b7e-4c3d-8a6f-2e1b0c9d8a7f"
		recordedPlanID = "valkey-large"
		inferredPlanID = "valkey-standalone"
		deploymentName = inferredPlanID + "-" + instanceID
	)

	synchronizer := NewIndexSynchronizer(NewTestVault(t), NewMockLogger())

	err := synchronizer.SaveVaultIndex(map[string]interface{}{
		instanceID: map[string]interface{}{
			indexServiceKey:    unclaimedTestService,
			indexPlanKey:       recordedPlanID,
			indexDeploymentKey: deploymentName,
		},
	})
	if err != nil {
		t.Fatalf("failed to seed index: %v", err)
	}

	instances := []InstanceData{{
		ID:         instanceID,
		ServiceID:  unclaimedTestService,
		PlanID:     inferredPlanID,
		Deployment: DeploymentDetail{DeploymentInfo: DeploymentInfo{Name: deploymentName}},
	}}

	err = synchronizer.SyncIndexWithValidation(context.Background(), instances, map[string]bool{deploymentName: true})
	if err != nil {
		t.Fatalf("SyncIndexWithValidation failed: %v", err)
	}

	idx, err := synchronizer.GetVaultIndex()
	if err != nil {
		t.Fatalf("failed to read index: %v", err)
	}

	entry, _ := idx[instanceID].(map[string]interface{})
	if entry[indexPlanKey] != recordedPlanID {
		t.Errorf("the index entry's plan is %v after a sync handed plan %q, want the recorded plan %q", entry[indexPlanKey], inferredPlanID, recordedPlanID)
	}

	if entry[indexDeploymentKey] != deploymentName {
		t.Errorf("the index entry's deployment is %v after a sync, want %q", entry[indexDeploymentKey], deploymentName)
	}
}
