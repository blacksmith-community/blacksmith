package reconciler_test

import (
	"context"
	"testing"

	"blacksmith/internal/bosh"
	. "blacksmith/pkg/reconciler"

	"github.com/fivetwenty-io/graft/pkg/yaml"
)

// The director the broker holds is a bosh.Director, so the populator has to
// read a manifest through that interface and not through a method shape no
// director has. The fake implements exactly the bosh.Director method set.
var _ bosh.Director = (*mockDirector)(nil)

const recoveryFixturePassword = "recovery-fixture-not-real"

func TestFetchCredsFromBOSHReadsManifestThroughDirector(t *testing.T) {
	t.Parallel()

	const deployment = "redis-standalone-" + parityInstanceID

	manifest := handBuiltManifest("redis", "redis", map[string]interface{}{credentialField: recoveryFixturePassword}, true)

	body, err := yaml.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}

	director := &mockDirector{details: map[string]*bosh.DeploymentDetail{
		deployment: {Name: deployment, Manifest: string(body)},
	}}

	populator := NewCredentialPopulator(nil, NewMockLogger())

	creds, err := populator.FetchCredsFromBOSH(context.Background(), deployment, director)
	if err != nil {
		t.Fatalf("FetchCredsFromBOSH with a bosh.Director failed: %v", err)
	}

	if creds[credentialField] != recoveryFixturePassword {
		t.Errorf("password not recovered from the manifest")
	}
}
