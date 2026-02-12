package reconciler_test

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	. "blacksmith/pkg/reconciler"
)

// Common test errors shared across reconciler_test package.
var (
	// Comment out unused error to fix unused variable issue
	// errNoDataFoundAtPath     = errors.New("no data found at path").
	errVaultConnectionFailed = errors.New("vault connection failed")
)

// Mock implementations for testing

type MockLoggerLocal struct {
	mu        sync.Mutex
	debugLogs []string
	infoLogs  []string
	warnLogs  []string
	errorLogs []string
}

func NewMockLoggerLocal() *MockLoggerLocal {
	return &MockLoggerLocal{
		debugLogs: []string{},
		infoLogs:  []string{},
		warnLogs:  []string{},
		errorLogs: []string{},
	}
}

func (l *MockLoggerLocal) Debugf(format string, args ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.debugLogs = append(l.debugLogs, format)
}

func (l *MockLoggerLocal) Infof(format string, args ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.infoLogs = append(l.infoLogs, format)
}

func (l *MockLoggerLocal) Warningf(format string, args ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.warnLogs = append(l.warnLogs, format)
}

func (l *MockLoggerLocal) Errorf(format string, args ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.errorLogs = append(l.errorLogs, format)
}

// RunBindingCredentialsPathConstructionTests exercises GetBindingCredentials path logic across specs.
func RunBindingCredentialsPathConstructionTests(t *testing.T, logger Logger) {
	t.Helper()

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
		// Capture testCase for parallel execution
		tc := testCase
		t.Run(fmt.Sprintf("path_%s_%s", tc.instanceID, tc.bindingID), func(t *testing.T) {
			t.Parallel()

			// Create separate vault and updater per subtest to avoid race conditions
			vault := NewTestVault(t)
			updater := NewVaultUpdater(vault, logger, BackupConfig{Enabled: false})

			// Write then read to verify correct path construction
			creds := map[string]interface{}{"k": "v"}

			err := vault.SetSecret(tc.expectedPath, creds)
			if err != nil {
				t.Fatalf("failed to set secret: %v", err)
			}

			got, err := updater.GetBindingCredentials(tc.instanceID, tc.bindingID)
			if err != nil {
				t.Fatalf("unexpected error retrieving credentials: %v", err)
			}

			if got["k"] != "v" {
				t.Fatalf("expected to read back value from %s", tc.expectedPath)
			}
		})
	}
}
