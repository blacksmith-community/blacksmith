package parity_test

import (
	"os"
	"testing"

	"blacksmith/internal/parity"
)

// TestMain stops the OpenBao dev server that SharedVault starts.
func TestMain(m *testing.M) {
	code := m.Run()

	parity.CloseSharedVault()
	os.Exit(code)
}

// TestSharedVaultRestoresTheEnvironment checks that SharedVault points
// VAULT_ADDR and VAULT_TOKEN at the shared server only while its caller
// runs, and that the values from before come back when the caller ends.
//
//nolint:paralleltest // t.Setenv cannot run in a parallel test
func TestSharedVaultRestoresTheEnvironment(t *testing.T) {
	const (
		addrBefore  = "parity-addr-before-the-shared-vault"
		tokenBefore = "parity-token-before-the-shared-vault" //nolint:gosec // a placeholder, not a credential
	)

	t.Setenv("VAULT_ADDR", addrBefore)
	t.Setenv("VAULT_TOKEN", tokenBefore)

	t.Run("caller", func(t *testing.T) {
		server := parity.SharedVault(t)

		if os.Getenv("VAULT_ADDR") != server.Addr {
			t.Error("VAULT_ADDR does not point at the shared server")
		}

		if os.Getenv("VAULT_TOKEN") != server.RootToken {
			t.Error("VAULT_TOKEN is not the shared server's token")
		}
	})

	if os.Getenv("VAULT_ADDR") != addrBefore {
		t.Error("VAULT_ADDR was not restored after the caller ended")
	}

	if os.Getenv("VAULT_TOKEN") != tokenBefore {
		t.Error("VAULT_TOKEN was not restored after the caller ended")
	}
}
