package reconciler_test

import (
	"context"
	"testing"

	. "blacksmith/pkg/reconciler"
)

// fakeCFDiscovery stands in for the CF manager: discovery succeeds and returns
// exactly the instances configured on it.
type fakeCFDiscovery struct {
	instances []CFServiceInstanceDetails
}

func (f *fakeCFDiscovery) DiscoverAllServiceInstances(_ []string) ([]CFServiceInstanceDetails, error) {
	return f.instances, nil
}

// newUnclaimedTestManager wires a manager with a real IndexSynchronizer over the
// test vault so the reconciler can consult the index. The index already holds
// an unrelated instance, as it would on a broker with other services, and the
// CF manager is passed through untyped so a nil means "not configured".
func newUnclaimedTestManager(t *testing.T, cfManager interface{}) (*ReconcilerManager, *rmMockScanner, *rmMockUpdater, *IndexSynchronizer) {
	t.Helper()

	logger := NewMockLogger()
	manager := NewReconcilerManager(newTestManagerConfig(), nil, nil, nil, logger, cfManager)

	vault := NewTestVault(t)

	err := vault.Put("db", map[string]interface{}{
		"00000000-0000-4000-8000-000000000001": map[string]interface{}{
			"service_id": "valkey", "plan_id": "standalone", "deployment_name": "valkey-standalone-00000000-0000-4000-8000-000000000001",
		},
	})
	if err != nil {
		t.Fatalf("failed to seed index: %v", err)
	}

	scan := &rmMockScanner{}
	upd := &rmMockUpdater{}
	synchronizer := NewIndexSynchronizer(vault, logger)

	manager.Scanner = scan
	manager.Updater = upd
	manager.Synchronizer = synchronizer

	return manager, scan, upd, synchronizer
}

// A deployment left behind by a deprovision that raced a provision has no index
// entry and no CF service instance. The reconciler used to adopt it, recreate
// the index entry, and attempt credential recovery. It must report it instead.
func TestReconcilerManager_DoesNotAdoptDeploymentWithoutIndexEntryOrCFInstance(t *testing.T) {
	t.Parallel()

	const instanceID = "181c0984-f4b6-461c-83ff-98ff41872f4b"

	manager, scan, upd, synchronizer := newUnclaimedTestManager(t, &fakeCFDiscovery{})
	scan.deployments = []DeploymentInfo{{Name: "valkey-standalone-" + instanceID}}

	manager.RunReconciliation(context.Background())

	if calls := upd.getCalls(); len(calls) != 0 {
		t.Fatalf("expected no vault update for an unclaimed deployment, got %d: %+v", len(calls), calls)
	}

	idx, err := synchronizer.GetVaultIndex()
	if err != nil {
		t.Fatalf("failed to read index: %v", err)
	}

	if _, exists := idx[instanceID]; exists {
		t.Fatalf("expected unclaimed deployment to stay out of the index, got entry %+v", idx[instanceID])
	}
}

// The same deployment with a live CF service instance is a legitimate recovery
// case and must still be adopted.
func TestReconcilerManager_AdoptsDeploymentWithCFInstance(t *testing.T) {
	t.Parallel()

	const instanceID = "281c0984-f4b6-461c-83ff-98ff41872f4b"

	cf := &fakeCFDiscovery{instances: []CFServiceInstanceDetails{{
		GUID: instanceID, Name: "vk2", ServiceID: "valkey", PlanID: "standalone",
	}}}

	manager, scan, upd, synchronizer := newUnclaimedTestManager(t, cf)
	scan.deployments = []DeploymentInfo{{Name: "valkey-standalone-" + instanceID}}

	manager.RunReconciliation(context.Background())

	if calls := upd.getCalls(); len(calls) != 1 {
		t.Fatalf("expected one vault update for a deployment with a CF instance, got %d", len(calls))
	}

	idx, err := synchronizer.GetVaultIndex()
	if err != nil {
		t.Fatalf("failed to read index: %v", err)
	}

	if _, exists := idx[instanceID]; !exists {
		t.Fatalf("expected deployment with a CF instance to be added to the index, index was %+v", idx)
	}
}

// Without a CF manager the reconciler cannot tell whether the instance exists,
// so it keeps adopting deployments as it always has.
func TestReconcilerManager_AdoptsDeploymentWhenCFDiscoveryUnavailable(t *testing.T) {
	t.Parallel()

	const instanceID = "381c0984-f4b6-461c-83ff-98ff41872f4b"

	manager, scan, upd, synchronizer := newUnclaimedTestManager(t, nil)
	scan.deployments = []DeploymentInfo{{Name: "valkey-standalone-" + instanceID}}

	manager.RunReconciliation(context.Background())

	if calls := upd.getCalls(); len(calls) != 1 {
		t.Fatalf("expected one vault update when CF discovery is unavailable, got %d", len(calls))
	}

	idx, err := synchronizer.GetVaultIndex()
	if err != nil {
		t.Fatalf("failed to read index: %v", err)
	}

	if _, exists := idx[instanceID]; !exists {
		t.Fatalf("expected deployment to be adopted when CF discovery is unavailable, index was %+v", idx)
	}
}
