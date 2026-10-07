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
