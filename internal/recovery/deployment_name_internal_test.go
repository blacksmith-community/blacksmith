package recovery

import (
	"fmt"
	"testing"

	"blacksmith/internal/bosh"
	"blacksmith/internal/broker"
	internalVault "blacksmith/internal/vault"
	"blacksmith/pkg/testutil"
)

// The reconciler can rewrite an index entry's plan ID after the deployment
// was created, so the regeneration check asks the director for the deployment
// the entry records, and builds the name from the plan ID only for an entry
// that records none.
func TestRegenerationCheckAsksForTheDeploymentAnIndexEntryRecords(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	server, err := testutil.NewVaultDevServer(t)
	if err != nil {
		t.Fatalf("failed to start vault: %v", err)
	}

	vaultClient := internalVault.New(server.Addr, server.RootToken, true)

	entries := map[string]map[string]interface{}{
		"adopted": {"service_id": "redis", "plan_id": "redis-standalone", "deployment_name": "redis-standalone-classic-adopted"},
		"legacy":  {"service_id": "redis", "plan_id": "redis-standalone"},
	}

	for id, entry := range entries {
		err = vaultClient.Index(ctx, id, entry)
		if err != nil {
			t.Fatalf("failed to seed %s: %v", id, err)
		}
	}

	live := map[string]bool{"redis-standalone-classic-adopted": true, "redis-standalone-legacy": true}

	director := testutil.NewScriptedBOSHDirector()
	director.GetDeploymentFn = func(name string) (*bosh.DeploymentDetail, error) {
		if live[name] {
			return &bosh.DeploymentDetail{Name: name}, nil
		}

		return nil, fmt.Errorf("deployment %s: %w", name, bosh.ErrDeploymentNotFound)
	}

	recovery := NewCredentialRecovery(&broker.Broker{BOSH: director}, vaultClient)

	for id := range entries {
		err = recovery.checkRegenerationPossible(ctx, id)
		if err != nil {
			t.Errorf("instance %s: %v (the director was asked for %v)", id, err, director.Calls("GetDeployment"))
		}
	}
}
