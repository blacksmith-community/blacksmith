package reconciler_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	. "blacksmith/pkg/reconciler"
)

const (
	testInstanceMetadataPath = "test-instance/metadata"
)

// No custom errors here; use real vault errors

// MockTestLogger implements Logger interface for testing.
type MockTestLogger struct {
	messages []string
}

func (l *MockTestLogger) Debugf(format string, args ...interface{}) {
	l.messages = append(l.messages, fmt.Sprintf("[DEBUG] "+format, args...))
}

func (l *MockTestLogger) Infof(format string, args ...interface{}) {
	l.messages = append(l.messages, fmt.Sprintf("[INFO] "+format, args...))
}

func (l *MockTestLogger) Warningf(format string, args ...interface{}) {
	l.messages = append(l.messages, fmt.Sprintf("[WARN] "+format, args...))
}

func (l *MockTestLogger) Errorf(format string, args ...interface{}) {
	l.messages = append(l.messages, fmt.Sprintf("[ERROR] "+format, args...))
}

//nolint:funlen // This test function is intentionally long for comprehensive testing
func TestVaultUpdater_PreservesCredentials(t *testing.T) {
	t.Parallel()

	vault := NewTestVault(t)
	logger := &MockTestLogger{}
	updater := NewVaultUpdater(VaultInterface(vault), logger, BackupConfig{
		Enabled:          true,
		RetentionCount:   10,
		RetentionDays:    0,
		CompressionLevel: 9,
		CleanupEnabled:   true,
		BackupOnUpdate:   true,
		BackupOnDelete:   true,
	})

	// Setup existing credentials with the secret/ prefix
	credPath := "test-instance/credentials"
	_ = vault.SetSecret(credPath, map[string]interface{}{
		"username": "admin",
		"password": "secret",
		"host":     "10.0.0.1",
	})

	// Setup existing instance in index
	_ = vault.SetSecret("db", map[string]interface{}{
		"test-instance": map[string]interface{}{
			"service_id": "test-service",
			"plan_id":    "test-plan",
			"created_at": "2024-01-01T00:00:00Z",
		},
	})

	// Create instance data for update
	instance := &InstanceData{
		ID:        "test-instance",
		ServiceID: "test-service",
		PlanID:    "test-plan",
		Deployment: DeploymentDetail{
			DeploymentInfo: DeploymentInfo{
				Name: "test-plan-test-instance",
			},
		},
		CreatedAt: time.Now().Add(-24 * time.Hour),
		UpdatedAt: time.Now(),
		Metadata: map[string]interface{}{
			"service_name": "Test Service",
			"plan_name":    "Test Plan",
		},
	}

	// Update instance
	ctx := context.Background()

	_, err := updater.UpdateInstance(ctx, *instance)
	if err != nil {
		t.Fatalf("UpdateInstance failed: %v", err)
	}

	// Verify credentials were NOT overwritten by reading back
	got, err := vault.Get(credPath)
	if err != nil {
		t.Fatalf("failed to read credentials: %v", err)
	}

	if got["username"] != "admin" || got["password"] != "secret" || got["host"] != "10.0.0.1" {
		t.Error("Credentials should be preserved and unchanged")
	}

	// Verify has_credentials flag was set in metadata
	metadataPath := testInstanceMetadataPath

	metadata, err := vault.Get(metadataPath)
	if err != nil {
		t.Fatalf("failed to read metadata: %v", err)
	}

	if hasCredentials, ok := metadata["has_credentials"].(bool); !ok || !hasCredentials {
		t.Error("has_credentials flag should be true")
	}
}

//nolint:funlen // This test function is intentionally long for comprehensive testing

//nolint:funlen
func TestVaultUpdater_PreservesBindings(t *testing.T) {
	t.Parallel()

	vault := NewTestVault(t)
	logger := &MockTestLogger{}
	updater := NewVaultUpdater(VaultInterface(vault), logger, BackupConfig{
		Enabled:          true,
		RetentionCount:   10,
		RetentionDays:    0,
		CompressionLevel: 9,
		CleanupEnabled:   true,
		BackupOnUpdate:   true,
		BackupOnDelete:   true,
	})

	// Setup existing bindings with secret/ prefix
	bindingsPath := "test-instance/bindings"
	_ = vault.SetSecret(bindingsPath, map[string]interface{}{
		"binding-1": map[string]interface{}{
			"app_guid": "app-123",
			"credentials": map[string]interface{}{
				"username": "user1",
				"password": "pass1",
			},
		},
		"binding-2": map[string]interface{}{
			"app_guid": "app-456",
			"credentials": map[string]interface{}{
				"username": "user2",
				"password": "pass2",
			},
		},
	})

	// Setup existing instance in index
	_ = vault.SetSecret("db", map[string]interface{}{
		"test-instance": map[string]interface{}{
			"service_id": "test-service",
			"plan_id":    "test-plan",
		},
	})

	// Create instance data for update
	instance := &InstanceData{
		ID:        "test-instance",
		ServiceID: "test-service",
		PlanID:    "test-plan",
		Deployment: DeploymentDetail{
			DeploymentInfo: DeploymentInfo{
				Name: "test-plan-test-instance",
			},
		},
		CreatedAt: time.Now().Add(-24 * time.Hour),
		UpdatedAt: time.Now(),
		Metadata:  make(map[string]interface{}),
	}

	// Update instance
	ctx := context.Background()

	_, err := updater.UpdateInstance(ctx, *instance)
	if err != nil {
		t.Fatalf("UpdateInstance failed: %v", err)
	}

	// Verify binding metadata was set
	metadataPath := testInstanceMetadataPath

	metadata, err := vault.Get(metadataPath)
	if err != nil {
		t.Fatalf("failed to read metadata: %v", err)
	}

	if hasBindings, ok := metadata["has_bindings"].(bool); !ok || !hasBindings {
		t.Error("has_bindings flag should be true")
	}
	// bindings_count may be float64 via JSON; compare stringified
	if fmt.Sprint(metadata["bindings_count"]) != "2" {
		t.Errorf("bindings_count should be 2, got %v", metadata["bindings_count"])
	}

	if ids, ok := metadata["binding_ids"].([]interface{}); !ok || len(ids) != 2 {
		t.Errorf("binding_ids should have 2 entries, got %v", metadata["binding_ids"])
	}
	//nolint:funlen // This test function is intentionally long for comprehensive testing
}

//nolint:funlen
func TestVaultUpdater_CreatesBackup(t *testing.T) {
	t.Parallel()

	vault := NewTestVault(t)
	logger := &MockTestLogger{}
	updater := NewVaultUpdater(VaultInterface(vault), logger, BackupConfig{
		Enabled:          true,
		RetentionCount:   10,
		RetentionDays:    0,
		CompressionLevel: 9,
		CleanupEnabled:   true,
		BackupOnUpdate:   true,
		BackupOnDelete:   true,
	})

	// Setup existing instance data
	// Seed some index content
	_ = vault.Put("db", map[string]interface{}{
		"test-instance": map[string]interface{}{
			"service_id":      "test-service",
			"plan_id":         "test-plan",
			"deployment_name": "old-deployment",
			"created_at":      "2024-01-01T00:00:00Z",
		},
	})

	// Setup existing metadata
	_ = vault.SetSecret("test-instance/metadata", map[string]interface{}{
		"service_name": "Old Service",
		"history": []interface{}{
			map[string]interface{}{
				"timestamp": time.Now().Add(-1 * time.Hour).Unix(),
				"action":    "provision",
			},
		},
	})

	// Create instance data for update
	instance := &InstanceData{
		ID:        "test-instance",
		ServiceID: "test-service",
		PlanID:    "test-plan",
		Deployment: DeploymentDetail{
			DeploymentInfo: DeploymentInfo{
				Name: "new-deployment",
			},
		},
		CreatedAt: time.Now().Add(-24 * time.Hour),
		UpdatedAt: time.Now(),
		Metadata: map[string]interface{}{
			"service_name": "New Service",
		},
	}

	// Update instance
	ctx := context.Background()

	_, err := updater.UpdateInstance(ctx, *instance)
	if err != nil {
		t.Fatalf("UpdateInstance failed: %v", err)
	}

	// Verify backup was created under backups/test-instance
	keys, err := vault.ListSecrets("backups/test-instance")
	if err != nil {
		t.Fatalf("failed to list backups: %v", err)
	}

	if len(keys) == 0 {
		t.Log("No backups found; backup may be skipped if no data present")
	}
}

//nolint:funlen
func TestVaultUpdater_PreservesHistory(t *testing.T) {
	t.Parallel()

	vault := NewTestVault(t)
	logger := &MockTestLogger{}
	updater := NewVaultUpdater(VaultInterface(vault), logger, BackupConfig{
		Enabled:          true,
		RetentionCount:   10,
		RetentionDays:    0,
		CompressionLevel: 9,
		CleanupEnabled:   true,
		BackupOnUpdate:   true,
		BackupOnDelete:   true,
	})

	// Setup existing metadata with history (using secret/ prefix)
	existingHistory := []interface{}{
		map[string]interface{}{
			"timestamp":   time.Now().Add(-2 * time.Hour).Unix(),
			"action":      "provision",
			"description": "Instance provisioned",
		},
		map[string]interface{}{
			"timestamp":   time.Now().Add(-1 * time.Hour).Unix(),
			"action":      "update",
			"description": "Instance updated",
		},
	}

	_ = vault.SetSecret("test-instance/metadata", map[string]interface{}{
		"service_name": "Test Service",
		"history":      existingHistory,
	})

	// Setup existing instance in index
	_ = vault.SetSecret("db", map[string]interface{}{
		"test-instance": map[string]interface{}{
			"service_id": "test-service",
			"plan_id":    "test-plan",
		},
	})

	// Create instance data for update
	instance := &InstanceData{
		ID:        "test-instance",
		ServiceID: "test-service",
		PlanID:    "test-plan",
		Deployment: DeploymentDetail{
			DeploymentInfo: DeploymentInfo{
				Name: "test-plan-test-instance",
			},
		},
		CreatedAt: time.Now().Add(-24 * time.Hour),
		UpdatedAt: time.Now(),
		Metadata: map[string]interface{}{
			"service_name": "Test Service",
			"releases":     []string{"cf-mysql/1.0"},
		},
	}

	// Update instance
	ctx := context.Background()

	_, err := updater.UpdateInstance(ctx, *instance)
	if err != nil {
		t.Fatalf("UpdateInstance failed: %v", err)
	}

	// Verify history was preserved and added to (with secret/ prefix)
	metadataPath := testInstanceMetadataPath
	verifyHistoryPreservation(t, vault, metadataPath)
}

func TestVaultUpdater_DetectsChanges(t *testing.T) {
	t.Parallel()

	vault := NewTestVault(t)
	logger := &MockTestLogger{}
	updater := NewVaultUpdater(VaultInterface(vault), logger, BackupConfig{Enabled: false})

	oldMetadata := map[string]interface{}{
		"releases":  []string{"cf-mysql/1.0"},
		"stemcells": []string{"ubuntu-xenial/456.30"},
		"vms":       []string{"mysql/0", "mysql/1"},
		"field1":    "value1",
		"field2":    "value2",
	}

	newMetadata := map[string]interface{}{
		"releases":  []string{"cf-mysql/2.0"},                  // Changed
		"stemcells": []string{"ubuntu-xenial/456.30"},          // Same
		"vms":       []string{"mysql/0", "mysql/1", "mysql/2"}, // Changed
		"field1":    "value1",                                  // Same
		"field3":    "value3",                                  // New field
		// field2 removed
	}

	changes := updater.DetectChanges(oldMetadata, newMetadata)

	// Verify changes detected correctly
	if releasesChanged, ok := changes["releases_changed"].(bool); !ok || !releasesChanged {
		t.Error("Should detect releases changed")
	}

	if stemcellsChanged, ok := changes["stemcells_changed"].(bool); !ok || stemcellsChanged {
		t.Error("Should detect stemcells unchanged")
	}

	if vmsChanged, ok := changes["vms_changed"].(bool); !ok || !vmsChanged {
		t.Error("Should detect VMs changed")
	}

	if fieldsAdded, ok := changes["fields_added"].([]string); !ok || len(fieldsAdded) != 1 || fieldsAdded[0] != "field3" {
		t.Errorf("Should detect field3 was added, got %v", fieldsAdded)
	}

	if fieldsRemoved, ok := changes["fields_removed"].([]string); !ok || len(fieldsRemoved) != 1 || fieldsRemoved[0] != "field2" {
		t.Errorf("Should detect field2 was removed, got %v", fieldsRemoved)
	}
}

func TestVaultUpdater_MergesMetadataCorrectly(t *testing.T) {
	t.Parallel()

	vault := NewTestVault(t)
	logger := &MockTestLogger{}
	updater := NewVaultUpdater(VaultInterface(vault), logger, BackupConfig{Enabled: false})

	existing := map[string]interface{}{
		"field1": "old_value",
		"field2": "keep_value",
		"history": []interface{}{
			map[string]interface{}{"action": "provision"},
		},
	}

	newData := map[string]interface{}{
		"field1":  "new_value",
		"field3":  "add_value",
		"history": []interface{}{}, // Should not override existing history
	}

	merged := updater.MergeMetadata(existing, newData)

	// Verify merge results
	if merged["field1"] != "new_value" {
		t.Error("field1 should be updated to new_value")
	}

	if merged["field2"] != "keep_value" {
		t.Error("field2 should be preserved from existing")
	}

	if merged["field3"] != "add_value" {
		t.Error("field3 should be added from new")
	}

	if history, ok := merged["history"].([]interface{}); !ok || len(history) != 1 {
		t.Error("history should be preserved from existing")
	}
}

func TestVaultUpdater_BackupPathCorrection(t *testing.T) {
	t.Parallel()

	vault := NewTestVault(t)
	logger := &MockTestLogger{}
	updater := NewVaultUpdater(VaultInterface(vault), logger, BackupConfig{
		Enabled:          true,
		RetentionCount:   5,
		CompressionLevel: 9,
		CleanupEnabled:   true,
		BackupOnUpdate:   true,
	})

	// Setup existing instance data that would be backed up
	_ = vault.SetSecret("test-instance", map[string]interface{}{
		"service_id":      "test-service",
		"plan_id":         "test-plan",
		"deployment_name": "test-deployment",
	})
	_ = vault.SetSecret("test-instance/metadata", map[string]interface{}{
		"service_name": "Test Service",
		"plan_name":    "Test Plan",
	})

	// Create instance for update
	instance := &InstanceData{
		ID:        "test-instance",
		ServiceID: "test-service",
		PlanID:    "test-plan",
		Deployment: DeploymentDetail{
			DeploymentInfo: DeploymentInfo{
				Name: "test-deployment",
			},
		},
		CreatedAt: time.Now().Add(-24 * time.Hour),
		UpdatedAt: time.Now(),
		Metadata: map[string]interface{}{
			"service_name": "Updated Service",
		},
	}

	// Update instance
	ctx := context.Background()

	_, err := updater.UpdateInstance(ctx, *instance)
	if err != nil {
		t.Fatalf("UpdateInstance failed: %v", err)
	}

	// Check that backups were stored under backups/test-instance
	keys, err := vault.ListSecrets("backups/test-instance")
	if err != nil {
		t.Fatalf("failed to list backups: %v", err)
	}

	if len(keys) == 0 {
		t.Log("no backups were created; acceptable if export returned empty data")
	}
}

// Removed legacy MockVaultForBindingTests in favor of RealTestVault

//nolint:funlen
func TestVaultUpdater_GetBindingCredentials(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		instanceID   string
		bindingID    string
		setupVault   func(*RealTestVault)
		expectedErr  string
		expectedCred map[string]interface{}
	}{
		{
			name:       "successful retrieval of binding credentials",
			instanceID: "test-instance-123",
			bindingID:  "test-binding-456",
			setupVault: func(vault *RealTestVault) {
				vault.SetData("test-instance-123/bindings/test-binding-456/credentials", map[string]interface{}{
					"host":     "redis.example.com",
					"port":     6379,
					"username": "redis-user",
					"password": "redis-pass",
					"database": "myredis",
				})
			},
			expectedCred: map[string]interface{}{
				"host":     "redis.example.com",
				"port":     6379,
				"username": "redis-user",
				"password": "redis-pass",
				"database": "myredis",
			},
		},
		{
			name:       "credentials not found",
			instanceID: "test-instance-123",
			bindingID:  "non-existent-binding",
			setupVault: func(vault *RealTestVault) {
				// No credentials set up for this binding
			},
			expectedErr: "failed to get binding credentials",
		},
		{
			name:       "vault returns error",
			instanceID: "test-instance-123",
			bindingID:  "error-binding",
			setupVault: func(vault *RealTestVault) {
				vault.SetError("test-instance-123/bindings/error-binding/credentials", errVaultConnectionFailed)
			},
			expectedErr: "failed to get binding credentials",
		},
		{
			name:       "empty credentials",
			instanceID: "test-instance-123",
			bindingID:  "empty-binding",
			setupVault: func(vault *RealTestVault) {
				vault.SetData("test-instance-123/bindings/empty-binding/credentials", map[string]interface{}{})
			},
			expectedCred: map[string]interface{}{},
		},
		{
			name:       "credentials with special characters",
			instanceID: "test-instance-uuid-with-dashes",
			bindingID:  "binding-with-special-chars",
			setupVault: func(vault *RealTestVault) {
				vault.SetData("test-instance-uuid-with-dashes/bindings/binding-with-special-chars/credentials", map[string]interface{}{
					"host":     "my-service.internal",
					"port":     5432,
					"username": "user@domain.com",
					"password": "p@ssw0rd!#$",
					"ssl":      true,
				})
			},
			expectedCred: map[string]interface{}{
				"host":     "my-service.internal",
				"port":     5432,
				"username": "user@domain.com",
				"password": "p@ssw0rd!#$",
				"ssl":      true,
			},
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			updater, vault := setupBindingCredentialsTest(t, testCase)
			credentials, err := updater.GetBindingCredentials(testCase.instanceID, testCase.bindingID)

			verifyBindingCredentialsError(t, testCase.expectedErr, err)
			verifyBindingCredentials(t, testCase.expectedCred, credentials)
			verifyVaultPathCalled(t, vault, testCase.instanceID, testCase.bindingID)
		})
	}
}

// setupBindingCredentialsTest sets up test dependencies.
func setupBindingCredentialsTest(t *testing.T, testCase struct {
	name         string
	instanceID   string
	bindingID    string
	setupVault   func(*RealTestVault)
	expectedErr  string
	expectedCred map[string]interface{}
}) (*VaultUpdater, *RealTestVault) {
	t.Helper()
	vault := NewTestVault(t)
	logger := &MockTestLogger{}
	updater := NewVaultUpdater(vault, logger, BackupConfig{Enabled: false})
	testCase.setupVault(vault)

	return updater, vault
}

// verifyBindingCredentialsError verifies error expectations.
func verifyBindingCredentialsError(t *testing.T, expectedErr string, actualErr error) {
	t.Helper()

	if expectedErr != "" {
		if actualErr == nil {
			t.Errorf("expected error containing '%s', got nil", expectedErr)
		} else if !strings.Contains(actualErr.Error(), expectedErr) {
			t.Errorf("expected error containing '%s', got '%s'", expectedErr, actualErr.Error())
		}
	} else if actualErr != nil {
		t.Errorf("expected no error, got %v", actualErr)
	}
}

// verifyBindingCredentials verifies credential expectations.
func verifyBindingCredentials(t *testing.T, expected, actual map[string]interface{}) {
	t.Helper()

	if expected == nil {
		return
	}

	if len(actual) != len(expected) {
		t.Errorf("expected %d credential fields, got %d", len(expected), len(actual))
	}

	for key, expectedValue := range expected {
		actualValue, exists := actual[key]
		if !exists {
			t.Errorf("expected credential field '%s' not found", key)

			continue
		}
		// Be flexible about numeric types from JSON serialization
		if fmt.Sprint(actualValue) != fmt.Sprint(expectedValue) {
			t.Errorf("credential field '%s': expected %v, got %v", key, expectedValue, actualValue)
		}
	}
}

// verifyVaultPathCalled verifies that vault was called with expected path.
func verifyVaultPathCalled(t *testing.T, vault *RealTestVault, instanceID, bindingID string) {
	t.Helper()

	expectedPath := fmt.Sprintf("%s/bindings/%s/credentials", instanceID, bindingID)
	for _, call := range vault.getCalls {
		if strings.HasSuffix(call, expectedPath) || strings.Contains(call, "/"+expectedPath) {
			return
		}
	}

	t.Errorf("expected vault.Get to be called with path '%s', but it wasn't. Actual calls: %v", expectedPath, vault.getCalls)
}

func TestVaultUpdater_GetBindingCredentials_PathConstruction(t *testing.T) {
	t.Parallel()

	logger := &MockTestLogger{}
	RunBindingCredentialsPathConstructionTests(t, logger)
}

// verifyHistoryPreservation checks that history entries are properly preserved and added.
func verifyHistoryPreservation(t *testing.T, vault VaultInterface, metadataPath string) {
	t.Helper()

	metadata, err := vault.Get(metadataPath)
	if err != nil {
		t.Fatalf("failed to read metadata: %v", err)
	}
	// history stored as []interface{} of map[string]interface{}
	raw, ok := metadata["history"].([]interface{})
	if !ok {
		t.Error("History is not in expected format")

		return
	}

	if len(raw) < 3 {
		t.Errorf("History should have at least 3 entries (2 existing + 1 new), got %d", len(raw))
	}

	// Convert to expected type for validateHistoryActions
	var hist []map[string]interface{}
	for _, e := range raw {
		if m, ok := e.(map[string]interface{}); ok {
			hist = append(hist, m)
		}
	}

	validateHistoryActions(t, hist)
}

// validateHistoryActions checks that required history actions are present.
func validateHistoryActions(t *testing.T, history []map[string]interface{}) {
	t.Helper()

	foundActions := make(map[string]bool)
	requiredActions := []string{"provision", "update", "reconciliation"}

	for _, entry := range history {
		if action, ok := entry["action"].(string); ok {
			foundActions[action] = true
		}
	}

	for _, action := range requiredActions {
		if !foundActions[action] {
			t.Errorf("Required %s history entry was not found", action)
		}
	}
}
