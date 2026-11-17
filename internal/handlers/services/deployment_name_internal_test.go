package services

import (
	"testing"

	"blacksmith/pkg/logger"
	"blacksmith/pkg/testutil"
	vaultPkg "blacksmith/pkg/vault"
)

// The reconciler can rewrite an index entry's plan ID after the deployment
// was created, so the handler names the deployment the entry records, and
// builds the name from the plan ID only for an entry that records none.
func TestResolveDeploymentInfoNamesTheDeploymentAnIndexEntryRecords(t *testing.T) {
	t.Parallel()

	vault := &testutil.InstanceVault{Instances: map[string]vaultPkg.Instance{
		"adopted": {ServiceID: "redis", PlanID: "standalone", DeploymentName: "standalone-classic-adopted"},
		"legacy":  {ServiceID: "redis", PlanID: "standalone"},
	}}
	handler := NewHandler(Dependencies{Logger: logger.Get().Named("test"), Vault: vault})

	for instanceID, want := range map[string]string{"adopted": "standalone-classic-adopted", "legacy": "standalone-legacy"} {
		info, err := handler.resolveDeploymentInfo(t.Context(), instanceID)
		if err != nil {
			t.Fatalf("instance %s: %v", instanceID, err)
		}

		if info.Deployment != want {
			t.Errorf("instance %s resolves to deployment %q, want %q", instanceID, info.Deployment, want)
		}
	}
}

// A deprovisioned instance leaves a tombstone in the index, and nothing
// opens a session to, or toggles, the deployment of an instance that is
// gone.
func TestResolveDeploymentInfoRefusesATombstonedInstance(t *testing.T) {
	t.Parallel()

	vault := &testutil.InstanceVault{Instances: map[string]vaultPkg.Instance{
		"gone": {ServiceID: "redis", PlanID: "standalone", Status: vaultPkg.StatusDeleted},
	}}
	handler := NewHandler(Dependencies{Logger: logger.Get().Named("test"), Vault: vault})

	_, err := handler.resolveDeploymentInfo(t.Context(), "gone")
	if err == nil {
		t.Fatal("a tombstoned instance resolved to a deployment")
	}
}
