package testutil_test

import (
	"errors"
	"testing"

	"blacksmith/pkg/testutil"
)

func TestVaultDevServer(t *testing.T) {
	t.Parallel()
	// Create a new vault dev server
	vaultServer, err := testutil.NewVaultDevServer(t)
	if err != nil {
		t.Fatalf("Failed to create vault dev server: %v", err)
	}
	// Cleanup is automatic via t.Cleanup()

	// Test that we can write a secret
	testData := map[string]interface{}{
		"username": "testuser",
		"password": "testpass",
	}

	err = vaultServer.WriteSecret("test/credentials", testData)
	if err != nil {
		t.Fatalf("Failed to write secret: %v", err)
	}

	// Test that we can read the secret back
	readData, err := vaultServer.ReadSecret("test/credentials")
	if err != nil {
		t.Fatalf("Failed to read secret: %v", err)
	}

	if readData == nil {
		t.Fatal("Expected data but got nil")
	}

	// Verify the data matches
	if readData["username"] != testData["username"] {
		t.Errorf("Username mismatch: expected %v, got %v", testData["username"], readData["username"])
	}

	if readData["password"] != testData["password"] {
		t.Errorf("Password mismatch: expected %v, got %v", testData["password"], readData["password"])
	}

	// Test that we can delete the secret
	err = vaultServer.DeleteSecret("test/credentials")
	if err != nil {
		t.Fatalf("Failed to delete secret: %v", err)
	}

	// Verify it's deleted
	readData, err = vaultServer.ReadSecret("test/credentials")
	if !errors.Is(err, testutil.ErrSecretNotFound) {
		t.Fatalf("Expected ErrSecretNotFound after delete, got: %v", err)
	}

	if readData != nil {
		t.Error("Expected nil data after delete but got data")
	}

	// Test the Vault client is functional
	health, err := vaultServer.Client.Sys().Health()
	if err != nil {
		t.Fatalf("Failed to check health: %v", err)
	}

	if health.Sealed {
		t.Error("Vault should not be sealed")
	}

	if !health.Initialized {
		t.Error("Vault should be initialized")
	}
}

func TestVaultDevServerNoCleanup(t *testing.T) {
	t.Parallel()
	// Test the no-cleanup variant
	vaultServer, err := testutil.NewVaultDevServerNoCleanup()
	if err != nil {
		t.Fatalf("Failed to create vault dev server: %v", err)
	}
	defer vaultServer.Close() // Manual cleanup

	// Test basic operations
	testData := map[string]interface{}{
		"key": "value",
	}

	err = vaultServer.WriteSecret("simple/test", testData)
	if err != nil {
		t.Fatalf("Failed to write secret: %v", err)
	}

	readData, err := vaultServer.ReadSecret("simple/test")
	if err != nil {
		t.Fatalf("Failed to read secret: %v", err)
	}

	if readData["key"] != testData["key"] {
		t.Errorf("Data mismatch: expected %v, got %v", testData["key"], readData["key"])
	}
}
