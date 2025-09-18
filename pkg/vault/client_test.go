package vault_test

import (
	"os"
	"testing"

	"blacksmith/pkg/vault"
)

// Integration test - requires a running Vault instance
// Run with: VAULT_ADDR=http://127.0.0.1:8200 go test -v -run TestVaultClientIntegration.
func TestVaultClientIntegration(t *testing.T) {
	t.Parallel()

	// Skip if VAULT_ADDR is not set
	if os.Getenv("VAULT_ADDR") == "" {
		t.Skip("Skipping integration test: VAULT_ADDR not set")
	}

	// This would require a test Vault instance
	// For now, we'll just test that the client can be created
	client, err := vault.NewClient("http://127.0.0.1:8200", "", true)
	if err != nil {
		t.Fatalf("Failed to create vault client: %v", err)
	}

	if client == nil {
		t.Fatal("Expected non-nil client")
	}
}
