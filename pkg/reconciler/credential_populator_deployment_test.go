package reconciler_test

import (
	"context"
	"testing"

	"blacksmith/internal/bosh"
	. "blacksmith/pkg/reconciler"

	"github.com/fivetwenty-io/graft/pkg/yaml"
)

// The populator fills in an instance's missing credentials from the
// deployment the reconciler found for it, under whatever name that
// deployment carries. A name built from the instance ID would miss a
// deployment named for another ID, and the plan the populator reads out
// of the name only labels a log line, so a name it can't parse a plan
// from still has its credentials read.
func TestEnsureCredentialsReadsTheDeploymentItIsHanded(t *testing.T) {
	t.Parallel()

	deployments := map[string]string{
		"a deployment named for another instance ID": "redis-standalone-00000000-0000-0000-0000-0000000000ff",
		"a deployment whose name holds no plan":      "redis-standalone-legacy",
	}

	for name, deployment := range deployments {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			manifest := handBuiltManifest("redis", "redis", map[string]interface{}{credentialField: recoveryFixturePassword}, true)

			body, err := yaml.Marshal(manifest)
			if err != nil {
				t.Fatalf("marshal manifest: %v", err)
			}

			director := &mockDirector{details: map[string]*bosh.DeploymentDetail{
				deployment: {Name: deployment, Manifest: string(body)},
			}}

			vault := NewTestVault(t)
			populator := NewCredentialPopulator(NewVaultUpdater(vault, NewMockLogger(), BackupConfig{}), NewMockLogger())

			err = populator.EnsureCredentials(context.Background(), parityInstanceID, DeploymentInfo{Name: deployment}, director, nil)
			if err != nil {
				t.Fatalf("EnsureCredentials for deployment %s failed: %v", deployment, err)
			}

			creds, err := vault.Get(parityInstanceID + "/credentials")
			if err != nil {
				t.Fatalf("read the stored credentials: %v", err)
			}

			if creds[credentialField] != recoveryFixturePassword {
				t.Errorf("the credentials of deployment %s were not stored for the instance", deployment)
			}
		})
	}
}
