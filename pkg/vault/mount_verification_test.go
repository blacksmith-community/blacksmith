package vault_test

import (
	"testing"

	"blacksmith/pkg/testutil"
	"blacksmith/pkg/vault"

	vapi "github.com/hashicorp/vault/api"
)

// TestVerifyMountFailsLoudlyWhenListingForbidden reproduces the freshly
// deployed broker scenario: the secret/ mount does not exist yet, and the
// token used by the broker cannot list sys/mounts to discover that. Prior
// behavior silently treated this as "nothing to do", so the mount was never
// created and provisioning failed later with an opaque 500. VerifyMount must
// instead surface a clear error so the operator (or broker startup logging)
// knows the mount was never verified or created.
func TestVerifyMountFailsLoudlyWhenListingForbidden(t *testing.T) {
	t.Parallel()

	server, err := testutil.NewVaultDevServer(t)
	if err != nil {
		t.Fatalf("failed to start vault dev server: %v", err)
	}

	// Simulate a fresh store: the dev server pre-mounts secret/, so remove
	// it to model a store that has never had the mount created.
	unmountErr := server.Client.Sys().Unmount("secret")
	if unmountErr != nil {
		t.Fatalf("failed to unmount secret/: %v", unmountErr)
	}

	// Issue a token bound to an explicit, empty policy (and no default
	// policy), mirroring an operator-restricted internal-vault token that
	// lacks the sys/mounts list capability. Leaving Policies unset/empty
	// when created by a root token causes Vault to grant "root" instead,
	// so an explicit named policy is required to actually restrict it.
	const restrictedPolicyName = "verify-mount-test-no-mounts-access"

	const restrictedPolicyDoc = `
path "unrelated/*" {
  capabilities = ["deny"]
}
`

	policyErr := server.Client.Sys().PutPolicy(restrictedPolicyName, restrictedPolicyDoc)
	if policyErr != nil {
		t.Fatalf("failed to create restricted policy: %v", policyErr)
	}

	tokenResp, err := server.Client.Auth().Token().Create(&vapi.TokenCreateRequest{
		Policies:        []string{restrictedPolicyName},
		NoDefaultPolicy: true,
		TTL:             "10m",
	})
	if err != nil {
		t.Fatalf("failed to create restricted token: %v", err)
	}

	restrictedClient, err := vault.NewClient(server.Addr, tokenResp.Auth.ClientToken, true)
	if err != nil {
		t.Fatalf("failed to create restricted vault client: %v", err)
	}

	err = restrictedClient.VerifyMount("secret", true)
	if err == nil {
		t.Fatal("expected VerifyMount to return an error when mount " +
			"existence cannot be verified due to a 403 on sys/mounts, " +
			"but it returned nil (silent skip)")
	}
}
