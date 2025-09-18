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

// Static errors for this test file (now using common errors from test_mocks_internal_test.go)

// Simple mock vault for isolated testing.
type SimpleMockVault struct {
	data   map[string]map[string]interface{}
	errors map[string]error
	calls  []string
}

func NewSimpleMockVault() *SimpleMockVault {
	return &SimpleMockVault{
		data:   make(map[string]map[string]interface{}),
		errors: make(map[string]error),
		calls:  []string{},
	}
}

func (m *SimpleMockVault) Get(path string) (map[string]interface{}, error) {
	m.calls = append(m.calls, "GET:"+path)
	if err, exists := m.errors[path]; exists {
		return nil, err
	}

	if data, exists := m.data[path]; exists {
		return data, nil
	}

	return nil, fmt.Errorf("%w: %s", errNoDataFoundAtPath, path)
}

func (m *SimpleMockVault) Put(path string, secret map[string]interface{}) error {
	m.calls = append(m.calls, "PUT:"+path)
	m.data[path] = secret

	return nil
}

func (m *SimpleMockVault) GetSecret(path string) (map[string]interface{}, error) {
	return m.Get(path)
}

func (m *SimpleMockVault) SetSecret(path string, secret map[string]interface{}) error {
	return m.Put(path, secret)
}

func (m *SimpleMockVault) DeleteSecret(path string) error {
	delete(m.data, path)

	return nil
}

func (m *SimpleMockVault) ListSecrets(path string) ([]string, error) {
	var secrets []string

	prefix := path + "/"
	for key := range m.data {
		if strings.HasPrefix(key, prefix) {
			secrets = append(secrets, key)
		}
	}

	return secrets, nil
}

func (m *SimpleMockVault) SetData(path string, data map[string]interface{}) {
	m.data[path] = data
}

func (m *SimpleMockVault) SetError(path string, err error) {
	m.errors[path] = err
}

// Simple logger for isolated testing.
type SimpleLogger struct{}

func (l *SimpleLogger) Debugf(format string, args ...interface{})   {}
func (l *SimpleLogger) Infof(format string, args ...interface{})    {}
func (l *SimpleLogger) Warningf(format string, args ...interface{}) {}
func (l *SimpleLogger) Errorf(format string, args ...interface{})   {}

//nolint:funlen // This test function is intentionally long for comprehensive testing
func TestGetBindingCredentials_Standalone(t *testing.T) {
	t.Parallel()

	vault := NewSimpleMockVault()
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
		vault.SetData("test-instance-123/bindings/test-binding-456/credentials", expectedCreds)

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
			} else if actual != expected {
				t.Errorf("credential %s: expected %v, got %v", key, expected, actual)
			}
		}

		// Verify vault path
		expectedPath := "test-instance-123/bindings/test-binding-456/credentials"
		found := false

		for _, call := range vault.calls {
			if strings.Contains(call, expectedPath) {
				found = true

				break
			}
		}

		if !found {
			t.Errorf("expected vault call with path containing '%s', got calls: %v", expectedPath, vault.calls)
		}
	})

	// Test not found
	t.Run("credentials not found", func(t *testing.T) {
		t.Parallel()

		vault.calls = []string{} // Reset calls
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

	// Test vault error
	t.Run("vault error", func(t *testing.T) {
		t.Parallel()

		vault.calls = []string{} // Reset calls
		instanceID := testInstanceID
		bindingID := "error-binding"

		// Setup vault error
		vault.SetError("test-instance-123/bindings/error-binding/credentials", errVaultConnectionFailed)

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

		vault.calls = []string{} // Reset calls
		instanceID := testInstanceID
		bindingID := "empty-binding"

		// Setup empty credentials
		vault.SetData("test-instance-123/bindings/empty-binding/credentials", map[string]interface{}{})

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

	vault := NewSimpleMockVault()
	logger := &SimpleLogger{}

	updater := NewVaultUpdater(vault, logger, BackupConfig{Enabled: false})

	testCases := []struct {
		instanceID   string
		bindingID    string
		expectedPath string
	}{
		{
			instanceID:   "simple-instance",
			bindingID:    "simple-binding",
			expectedPath: "simple-instance/bindings/simple-binding/credentials",
		},
		{
			instanceID:   "instance-with-uuid-12345678-1234-1234-1234-123456789abc",
			bindingID:    "binding-with-uuid-87654321-4321-4321-4321-cba987654321",
			expectedPath: "instance-with-uuid-12345678-1234-1234-1234-123456789abc/bindings/binding-with-uuid-87654321-4321-4321-4321-cba987654321/credentials",
		},
		{
			instanceID:   "instance_with_underscores",
			bindingID:    "binding_with_underscores",
			expectedPath: "instance_with_underscores/bindings/binding_with_underscores/credentials",
		},
	}

	for _, testCase := range testCases {
		t.Run(fmt.Sprintf("path_%s_%s", testCase.instanceID, testCase.bindingID), func(t *testing.T) {
			t.Parallel()
			// Clear previous calls
			vault.calls = []string{}

			// Execute (will fail, but we just want to check path construction)
			_, _ = updater.GetBindingCredentials(testCase.instanceID, testCase.bindingID)

			// Verify path construction
			if len(vault.calls) != 1 {
				t.Errorf("expected exactly 1 vault call, got %d: %v", len(vault.calls), vault.calls)
			} else {
				call := vault.calls[0]
				if !strings.Contains(call, testCase.expectedPath) {
					t.Errorf("expected call to contain path '%s', got '%s'", testCase.expectedPath, call)
				}
			}
		})
	}
}
