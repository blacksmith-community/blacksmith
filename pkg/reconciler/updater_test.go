package reconciler

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// MockVault implements VaultInterface for testing
type MockVault struct {
	data      map[string]map[string]interface{}
	index     map[string]interface{}
	putCalls  []string
	getCalls  []string
	saveCalls int
}

func NewMockVault() *MockVault {
	return &MockVault{
		data:  make(map[string]map[string]interface{}),
		index: make(map[string]interface{}),
	}
}

func (m *MockVault) Put(path string, data interface{}) error {
	m.putCalls = append(m.putCalls, path)
	if dataMap, ok := data.(map[string]interface{}); ok {
		m.data[path] = dataMap
		return nil
	}
	return fmt.Errorf("invalid data type")
}

func (m *MockVault) Get(path string, out interface{}) (bool, error) {
	m.getCalls = append(m.getCalls, path)
	if data, exists := m.data[path]; exists {
		if outPtr, ok := out.(*map[string]interface{}); ok {
			*outPtr = data
			return true, nil
		}
		return false, fmt.Errorf("invalid output type")
	}
	return false, nil
}

func (m *MockVault) GetIndex(name string) (*VaultIndex, error) {
	return &VaultIndex{
		Data: m.index,
		SaveFunc: func() error {
			m.saveCalls++
			return nil
		},
	}, nil
}

func (m *MockVault) Delete(path string) error {
	delete(m.data, path)
	return nil
}

func (m *MockVault) UpdateIndex(name string, instanceID string, data interface{}) error {
	m.index[instanceID] = data
	m.saveCalls++
	return nil
}

// MockTestLogger implements Logger interface for testing
type MockTestLogger struct {
	messages []string
}

func (l *MockTestLogger) Debug(format string, args ...interface{}) {
	l.messages = append(l.messages, fmt.Sprintf("[DEBUG] "+format, args...))
}

func (l *MockTestLogger) Info(format string, args ...interface{}) {
	l.messages = append(l.messages, fmt.Sprintf("[INFO] "+format, args...))
}

func (l *MockTestLogger) Warning(format string, args ...interface{}) {
	l.messages = append(l.messages, fmt.Sprintf("[WARN] "+format, args...))
}

func (l *MockTestLogger) Error(format string, args ...interface{}) {
	l.messages = append(l.messages, fmt.Sprintf("[ERROR] "+format, args...))
}

func TestVaultUpdater_PreservesCredentials(t *testing.T) {
	vault := NewMockVault()
	logger := &MockTestLogger{}
	updater := NewVaultUpdater(VaultInterface(vault), logger, BackupConfig{
		Enabled:   true,
		Retention: 10,
		Cleanup:   true,
		Path:      "backups",
	})

	// Setup existing credentials
	credPath := "test-instance/credentials"
	vault.data[credPath] = map[string]interface{}{
		"username": "admin",
		"password": "secret",
		"host":     "10.0.0.1",
	}

	// Setup existing instance in index
	vault.index["test-instance"] = map[string]interface{}{
		"service_id": "test-service",
		"plan_id":    "test-plan",
		"created_at": "2024-01-01T00:00:00Z",
	}

	// Create instance data for update
	instance := &InstanceData{
		ID:             "test-instance",
		ServiceID:      "test-service",
		PlanID:         "test-plan",
		DeploymentName: "test-plan-test-instance",
		CreatedAt:      time.Now().Add(-24 * time.Hour),
		UpdatedAt:      time.Now(),
		LastSyncedAt:   time.Now(),
		Metadata: map[string]interface{}{
			"service_name": "Test Service",
			"plan_name":    "Test Plan",
		},
	}

	// Update instance
	ctx := context.Background()
	err := updater.UpdateInstance(ctx, instance)
	if err != nil {
		t.Fatalf("UpdateInstance failed: %v", err)
	}

	// Verify credentials were checked but not overwritten
	credsChecked := false
	for _, call := range vault.getCalls {
		if call == credPath {
			credsChecked = true
			break
		}
	}
	if !credsChecked {
		t.Error("Credentials were not checked during update")
	}

	// Verify credentials were NOT written
	for _, call := range vault.putCalls {
		if call == credPath {
			t.Error("Credentials should not be overwritten")
		}
	}

	// Verify has_credentials flag was set
	metadataPath := "test-instance/metadata"
	if metadata, exists := vault.data[metadataPath]; exists {
		if hasCredentials, ok := metadata["has_credentials"].(bool); !ok || !hasCredentials {
			t.Error("has_credentials flag should be true")
		}
	} else {
		t.Error("Metadata was not saved")
	}
}

func TestVaultUpdater_PreservesBindings(t *testing.T) {
	vault := NewMockVault()
	logger := &MockTestLogger{}
	updater := NewVaultUpdater(VaultInterface(vault), logger, BackupConfig{
		Enabled:   true,
		Retention: 10,
		Cleanup:   true,
		Path:      "backups",
	})

	// Setup existing bindings
	bindingsPath := "test-instance/bindings"
	vault.data[bindingsPath] = map[string]interface{}{
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
	}

	// Setup existing instance in index
	vault.index["test-instance"] = map[string]interface{}{
		"service_id": "test-service",
		"plan_id":    "test-plan",
	}

	// Create instance data for update
	instance := &InstanceData{
		ID:             "test-instance",
		ServiceID:      "test-service",
		PlanID:         "test-plan",
		DeploymentName: "test-plan-test-instance",
		CreatedAt:      time.Now().Add(-24 * time.Hour),
		UpdatedAt:      time.Now(),
		LastSyncedAt:   time.Now(),
		Metadata:       make(map[string]interface{}),
	}

	// Update instance
	ctx := context.Background()
	err := updater.UpdateInstance(ctx, instance)
	if err != nil {
		t.Fatalf("UpdateInstance failed: %v", err)
	}

	// Verify bindings were checked
	bindingsChecked := false
	for _, call := range vault.getCalls {
		if call == bindingsPath {
			bindingsChecked = true
			break
		}
	}
	if !bindingsChecked {
		t.Error("Bindings were not checked during update")
	}

	// Verify binding metadata was set
	metadataPath := "test-instance/metadata"
	if metadata, exists := vault.data[metadataPath]; exists {
		if hasBindings, ok := metadata["has_bindings"].(bool); !ok || !hasBindings {
			t.Error("has_bindings flag should be true")
		}
		if bindingsCount, ok := metadata["bindings_count"].(int); !ok || bindingsCount != 2 {
			t.Errorf("bindings_count should be 2, got %v", bindingsCount)
		}
		if bindingIDs, ok := metadata["binding_ids"].([]string); !ok || len(bindingIDs) != 2 {
			t.Errorf("binding_ids should have 2 entries, got %v", bindingIDs)
		}
	} else {
		t.Error("Metadata was not saved")
	}
}

func TestVaultUpdater_CreatesBackup(t *testing.T) {
	vault := NewMockVault()
	logger := &MockTestLogger{}
	updater := NewVaultUpdater(VaultInterface(vault), logger, BackupConfig{
		Enabled:   true,
		Retention: 10,
		Cleanup:   true,
		Path:      "backups",
	})

	// Setup existing instance data
	vault.index["test-instance"] = map[string]interface{}{
		"service_id":      "test-service",
		"plan_id":         "test-plan",
		"deployment_name": "old-deployment",
		"created_at":      "2024-01-01T00:00:00Z",
	}

	// Setup existing metadata
	vault.data["test-instance/metadata"] = map[string]interface{}{
		"service_name": "Old Service",
		"history": []interface{}{
			map[string]interface{}{
				"timestamp": time.Now().Add(-1 * time.Hour).Unix(),
				"action":    "provision",
			},
		},
	}

	// Create instance data for update
	instance := &InstanceData{
		ID:             "test-instance",
		ServiceID:      "test-service",
		PlanID:         "test-plan",
		DeploymentName: "new-deployment",
		CreatedAt:      time.Now().Add(-24 * time.Hour),
		UpdatedAt:      time.Now(),
		LastSyncedAt:   time.Now(),
		Metadata: map[string]interface{}{
			"service_name": "New Service",
		},
	}

	// Update instance
	ctx := context.Background()
	err := updater.UpdateInstance(ctx, instance)
	if err != nil {
		t.Fatalf("UpdateInstance failed: %v", err)
	}

	// Verify backup was created
	backupCreated := false
	for _, call := range vault.putCalls {
		if strings.HasPrefix(call, "test-instance/backups/") {
			backupCreated = true
			// Verify backup contains the data
			if backupData, exists := vault.data[call]; exists {
				if data, hasData := backupData["data"]; !hasData {
					t.Error("Backup should contain data field")
				} else if dataMap, ok := data.(map[string]interface{}); ok {
					if _, hasIndex := dataMap["index"]; !hasIndex {
						t.Error("Backup data should contain index data")
					}
					if _, hasMetadata := dataMap["metadata"]; !hasMetadata {
						t.Error("Backup data should contain metadata")
					}
				}
				if _, hasTimestamp := backupData["timestamp"]; !hasTimestamp {
					t.Error("Backup should contain timestamp")
				}
				if _, hasSHA256 := backupData["sha256"]; !hasSHA256 {
					t.Error("Backup should contain SHA256")
				}
			} else {
				t.Error("Backup data was not saved")
			}
			break
		}
	}
	if !backupCreated {
		t.Error("Backup was not created before update")
	}
}

func TestVaultUpdater_PreservesHistory(t *testing.T) {
	vault := NewMockVault()
	logger := &MockTestLogger{}
	updater := NewVaultUpdater(VaultInterface(vault), logger, BackupConfig{
		Enabled:   true,
		Retention: 10,
		Cleanup:   true,
		Path:      "backups",
	})

	// Setup existing metadata with history
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

	vault.data["test-instance/metadata"] = map[string]interface{}{
		"service_name": "Test Service",
		"history":      existingHistory,
	}

	// Setup existing instance in index
	vault.index["test-instance"] = map[string]interface{}{
		"service_id": "test-service",
		"plan_id":    "test-plan",
	}

	// Create instance data for update
	instance := &InstanceData{
		ID:             "test-instance",
		ServiceID:      "test-service",
		PlanID:         "test-plan",
		DeploymentName: "test-plan-test-instance",
		CreatedAt:      time.Now().Add(-24 * time.Hour),
		UpdatedAt:      time.Now(),
		LastSyncedAt:   time.Now(),
		Metadata: map[string]interface{}{
			"service_name": "Test Service",
			"releases":     []string{"cf-mysql/1.0"},
		},
	}

	// Update instance
	ctx := context.Background()
	err := updater.UpdateInstance(ctx, instance)
	if err != nil {
		t.Fatalf("UpdateInstance failed: %v", err)
	}

	// Verify history was preserved and added to
	metadataPath := "test-instance/metadata"
	if metadata, exists := vault.data[metadataPath]; exists {
		if history, ok := metadata["history"].([]map[string]interface{}); ok {
			if len(history) < 3 {
				t.Errorf("History should have at least 3 entries (2 existing + 1 new), got %d", len(history))
			}
			// Check that old entries are preserved
			foundProvision := false
			foundUpdate := false
			foundReconciliation := false
			for _, entry := range history {
				if action, ok := entry["action"].(string); ok {
					switch action {
					case "provision":
						foundProvision = true
					case "update":
						foundUpdate = true
					case "reconciliation":
						foundReconciliation = true
					}
				}
			}
			if !foundProvision {
				t.Error("Original provision history entry was not preserved")
			}
			if !foundUpdate {
				t.Error("Original update history entry was not preserved")
			}
			if !foundReconciliation {
				t.Error("New reconciliation history entry was not added")
			}
		} else {
			t.Error("History is not in expected format")
		}
	} else {
		t.Error("Metadata was not saved")
	}
}

func TestVaultUpdater_DetectsChanges(t *testing.T) {
	vault := NewMockVault()
	logger := &MockTestLogger{}
	updater := &vaultUpdater{vault: VaultInterface(vault), logger: logger}

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

	changes := updater.detectChanges(oldMetadata, newMetadata)

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
	vault := NewMockVault()
	logger := &MockTestLogger{}
	updater := &vaultUpdater{vault: VaultInterface(vault), logger: logger}

	existing := map[string]interface{}{
		"field1": "old_value",
		"field2": "keep_value",
		"history": []interface{}{
			map[string]interface{}{"action": "provision"},
		},
	}

	new := map[string]interface{}{
		"field1":  "new_value",
		"field3":  "add_value",
		"history": []interface{}{}, // Should not override existing history
	}

	merged := updater.mergeMetadata(existing, new)

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
