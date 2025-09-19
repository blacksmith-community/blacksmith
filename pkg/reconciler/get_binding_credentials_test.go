package reconciler_test

import (
	"fmt"
	"strings"
	"testing"

	. "blacksmith/pkg/reconciler"
)

const (
	testInstanceID = "test-instance-123"
)

// Simple logger for isolated testing.
type SimpleLogger struct{}

func (l *SimpleLogger) Debugf(format string, args ...interface{})   {}
func (l *SimpleLogger) Infof(format string, args ...interface{})    {}
func (l *SimpleLogger) Warningf(format string, args ...interface{}) {}
func (l *SimpleLogger) Errorf(format string, args ...interface{})   {}

//nolint:funlen // This test function is intentionally long for comprehensive testing
func TestGetBindingCredentials_Standalone(t *testing.T) {
	t.Parallel()

	vault := NewTestVault(t)
	logger := &SimpleLogger{}

	// Create updater using constructor
	updater := NewVaultUpdater(vault, logger, BackupConfig{Enabled: false})

	// Test successful retrieval
	t.Run("successful retrieval", func(t *testing.T) {
		t.Parallel()

		instanceID := testInstanceID
		bindingID := "test-binding-456"
		expectedCreds := map[string]interface{}{
			"host":     "redis.example.com",
			"port":     6379,
			"username": "redis-user",
			"password": "redis-pass",
		}

		// Setup vault data
		_ = vault.SetSecret("test-instance-123/bindings/test-binding-456/credentials", expectedCreds)

		// Execute
		credentials, err := updater.GetBindingCredentials(instanceID, bindingID)

		// Verify
		if err != nil {
			t.Errorf("expected no error, got %v", err)
		}

		if len(credentials) != len(expectedCreds) {
			t.Errorf("expected %d credentials, got %d", len(expectedCreds), len(credentials))
		}

		for key, expected := range expectedCreds {
			if actual, exists := credentials[key]; !exists {
				t.Errorf("missing credential key: %s", key)
			} else if fmt.Sprint(actual) != fmt.Sprint(expected) {
				t.Errorf("credential %s: expected %v, got %v", key, expected, actual)
			}
		}

		// Path is implicitly verified by successful read above
	})

	// Test not found
	t.Run("credentials not found", func(t *testing.T) {
		t.Parallel()

		instanceID := testInstanceID
		bindingID := "non-existent-binding"

		// Execute
		_, err := updater.GetBindingCredentials(instanceID, bindingID)

		// Verify
		if err == nil {
			t.Error("expected error, got nil")
		} else if !strings.Contains(err.Error(), "failed to get binding credentials") {
			t.Errorf("expected error to contain 'failed to get binding credentials', got: %s", err.Error())
		}
	})

	// Test empty credentials
	t.Run("empty credentials", func(t *testing.T) {
		t.Parallel()

		instanceID := testInstanceID
		bindingID := "empty-binding"

		// Setup empty credentials
		_ = vault.SetSecret("test-instance-123/bindings/empty-binding/credentials", map[string]interface{}{})

		// Execute
		credentials, err := updater.GetBindingCredentials(instanceID, bindingID)

		// Verify
		if err != nil {
			t.Errorf("expected no error, got %v", err)
		}

		if len(credentials) != 0 {
			t.Errorf("expected 0 credentials, got %d", len(credentials))
		}
	})
}

func TestGetBindingCredentials_PathConstruction(t *testing.T) {
	t.Parallel()

	logger := &SimpleLogger{}
	RunBindingCredentialsPathConstructionTests(t, logger)
}
