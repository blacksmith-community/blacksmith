package websocket

import (
	"testing"

	"blacksmith/pkg/logger"
	"blacksmith/pkg/testutil"
	vaultPkg "blacksmith/pkg/vault"
)

// The reconciler can rewrite an index entry's plan ID after the deployment
// was created, so the handler names the deployment the entry records, and
// builds the name from the plan ID only for an entry that records none.
func TestDeploymentContextNamesTheDeploymentAnIndexEntryRecords(t *testing.T) {
	t.Parallel()

	vault := &testutil.InstanceVault{Instances: map[string]vaultPkg.Instance{
		"adopted": {ServiceID: "rabbitmq", PlanID: "single-node", DeploymentName: "single-node-classic-adopted"},
		"legacy":  {ServiceID: "rabbitmq", PlanID: "single-node"},
	}}
	handler := NewHandler(Dependencies{Logger: logger.Get().Named("test"), Vault: vault})

	for instanceID, want := range map[string]string{"adopted": "single-node-classic-adopted", "legacy": "single-node-legacy"} {
		deploymentName, _, err := handler.getDeploymentContext(t.Context(), instanceID)
		if err != nil {
			t.Fatalf("instance %s: %v", instanceID, err)
		}

		if deploymentName != want {
			t.Errorf("instance %s resolves to deployment %q, want %q", instanceID, deploymentName, want)
		}
	}
}
