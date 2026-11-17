package websocket

import (
	"testing"

	"blacksmith/pkg/logger"
	"blacksmith/pkg/testutil"
	vaultPkg "blacksmith/pkg/vault"
)

// The reconciler can rewrite an index entry's plan ID after the deployment
// was created, so the handler opens SSH to the deployment the entry records,
// and builds the name from the plan ID only for an entry that records none.
func TestServiceInstanceDeploymentNamesTheDeploymentAnIndexEntryRecords(t *testing.T) {
	t.Parallel()

	vault := &testutil.InstanceVault{Instances: map[string]vaultPkg.Instance{
		"adopted": {ServiceID: "redis", PlanID: "standalone", DeploymentName: "standalone-classic-adopted"},
		"legacy":  {ServiceID: "redis", PlanID: "standalone"},
	}}
	log := logger.Get().Named("test")
	handler := NewHandler(Dependencies{Logger: log, Vault: vault})

	for instanceID, want := range map[string]string{"adopted": "standalone-classic-adopted", "legacy": "standalone-legacy"} {
		deploymentName, _, err := handler.resolveServiceInstanceDeployment(t.Context(), instanceID, log)
		if err != nil {
			t.Fatalf("instance %s: %v", instanceID, err)
		}

		if deploymentName != want {
			t.Errorf("instance %s resolves to deployment %q, want %q", instanceID, deploymentName, want)
		}
	}
}

// A deprovisioned instance leaves a tombstone in the index, and nothing
// opens a session to, or toggles, the deployment of an instance that is
// gone.
func TestServiceInstanceDeploymentRefusesATombstonedInstance(t *testing.T) {
	t.Parallel()

	vault := &testutil.InstanceVault{Instances: map[string]vaultPkg.Instance{
		"gone": {ServiceID: "redis", PlanID: "standalone", Status: vaultPkg.StatusDeleted},
	}}
	handler := NewHandler(Dependencies{Logger: logger.Get().Named("test"), Vault: vault})

	_, _, err := handler.resolveServiceInstanceDeployment(t.Context(), "gone", logger.Get().Named("test"))
	if err == nil {
		t.Fatal("a tombstoned instance resolved to a deployment")
	}
}
