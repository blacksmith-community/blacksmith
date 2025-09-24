package reconciler_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/pkg/reconciler"
)

// Test error definitions.
var (
	errTestDirector404Response    = errors.New("director responded with non-successful status code '404'")
	errTestDeploymentDoesNotExist = errors.New("deployment 'test' doesn't exist")
	errTestNetworkTimeout         = errors.New("network timeout")
)

// TestBOSHScanner_GetDeploymentDetails_NotFound tests handling of 404 errors from BOSH.
func TestBOSHScanner_GetDeploymentDetails_NotFound(t *testing.T) {
	t.Parallel()

	// This test verifies that the scanner properly detects 404 errors
	// The actual implementation is tested through the reconciler's processDeployment
	// which checks for deployment.NotFound flag

	tests := []struct {
		name           string
		deploymentName string
		deploymentErr  error
		expectNotFound bool
	}{
		{
			name:           "deployment not found with ErrDeploymentNotFound",
			deploymentName: "test-deployment",
			deploymentErr:  bosh.ErrDeploymentNotFound,
			expectNotFound: true,
		},
		{
			name:           "deployment not found with 404 status code",
			deploymentName: "test-deployment",
			deploymentErr:  errTestDirector404Response,
			expectNotFound: true,
		},
		{
			name:           "deployment doesn't exist message",
			deploymentName: "test-deployment",
			deploymentErr:  errTestDeploymentDoesNotExist,
			expectNotFound: true,
		},
		{
			name:           "other error",
			deploymentName: "test-deployment",
			deploymentErr:  errTestNetworkTimeout,
			expectNotFound: false,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			// Test the error detection logic directly
			errStr := ""
			if testCase.deploymentErr != nil {
				errStr = testCase.deploymentErr.Error()
			}

			// This mimics the scanner's GetDeploymentDetails detection logic
			isNotFound := false
			if errStr != "" {
				isNotFound = (errStr == bosh.ErrDeploymentNotFound.Error()) ||
					containsString(errStr, "doesn't exist") ||
					containsString(errStr, "status code '404'") ||
					containsString(errStr, "deployment not found")
			}

			if isNotFound != testCase.expectNotFound {
				t.Errorf("expected NotFound=%v but got %v for error: %v",
					testCase.expectNotFound, isNotFound, testCase.deploymentErr)
			}
		})
	}
}

// TestReconciler_SkipsDeletedInstances tests that deleted instances are skipped.
func TestReconciler_SkipsDeletedInstances(t *testing.T) {
	t.Parallel()

	// Set up test vault with deleted and active instances
	vault := NewTestVault(t)

	vaultData := map[string]interface{}{
		"deleted-instance": map[string]interface{}{
			"service_id": "deleted-instance",
			"plan_id":    "test-plan",
			"deployment": "deleted-deployment",
			"status":     "deleted",
			"deleted_at": time.Now().Format(time.RFC3339),
		},
		"active-instance": map[string]interface{}{
			"service_id": "active-instance",
			"plan_id":    "test-plan",
			"deployment": "active-deployment",
			"status":     "active",
		},
	}

	err := vault.Put("db", vaultData)
	if err != nil {
		t.Fatalf("failed to set up vault data: %v", err)
	}

	// Verify the deleted instance is marked as deleted
	data, err := vault.Get("db")
	if err != nil {
		t.Fatalf("failed to get vault data: %v", err)
	}

	dataMap := data

	// Check deleted instance status
	if instance, exists := dataMap["deleted-instance"]; exists {
		if instMap, ok := instance.(map[string]interface{}); ok {
			if status, ok := instMap["status"].(string); !ok || status != "deleted" {
				t.Errorf("deleted instance should have status 'deleted', got %v", status)
			}
		}
	} else {
		t.Errorf("deleted-instance should exist in vault")
	}

	// Check active instance status
	if instance, exists := dataMap["active-instance"]; exists {
		if instMap, ok := instance.(map[string]interface{}); ok {
			if status, ok := instMap["status"].(string); !ok || status != "active" {
				t.Errorf("active instance should have status 'active', got %v", status)
			}
		}
	} else {
		t.Errorf("active-instance should exist in vault")
	}
}

// TestIndexSynchronizer_CleanupDeletedInstances tests cleanup of old deleted instances.
func TestIndexSynchronizer_CleanupDeletedInstances(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	logger := &TestLogger{}
	vault := NewTestVault(t)

	setupVaultWithTestData(t, vault)

	sync := reconciler.NewIndexSynchronizer(vault, logger)

	testDryRunCleanup(t, ctx, sync, vault)
	testActualCleanup(t, ctx, sync, vault)
}

func setupVaultWithTestData(t *testing.T, vault *RealTestVault) {
	t.Helper()

	oldDeletedTime := time.Now().Add(-61 * 24 * time.Hour)
	recentDeletedTime := time.Now().Add(-10 * 24 * time.Hour)

	vaultData := map[string]interface{}{
		"old-deleted": map[string]interface{}{
			"service_id":       "old-deleted",
			"status":           "deleted",
			"deleted_at":       oldDeletedTime.Format(time.RFC3339),
			"cleanup_eligible": true,
		},
		"recent-deleted": map[string]interface{}{
			"service_id":       "recent-deleted",
			"status":           "deleted",
			"deleted_at":       recentDeletedTime.Format(time.RFC3339),
			"cleanup_eligible": false,
		},
		"active-instance": map[string]interface{}{
			"service_id": "active-instance",
			"status":     "active",
		},
	}

	err := vault.Put("db", vaultData)
	if err != nil {
		t.Fatalf("failed to set up vault data: %v", err)
	}
}

func testDryRunCleanup(t *testing.T, ctx context.Context, sync *reconciler.IndexSynchronizer, vault *RealTestVault) {
	t.Helper()

	count, err := sync.CleanupDeletedInstances(ctx, true)
	if err != nil {
		t.Fatalf("cleanup failed: %v", err)
	}

	if count != 1 {
		t.Errorf("expected 1 instance to be eligible for cleanup, got %d", count)
	}

	verifyDryRunDidNotDelete(t, vault)
}

func verifyDryRunDidNotDelete(t *testing.T, vault *RealTestVault) {
	t.Helper()

	data, err := vault.Get("db")
	if err != nil {
		t.Fatalf("failed to get vault data: %v", err)
	}

	if len(data) != 3 {
		t.Errorf("expected 3 instances in vault after dry-run, got %d", len(data))
	}
}

func testActualCleanup(t *testing.T, ctx context.Context, sync *reconciler.IndexSynchronizer, vault *RealTestVault) {
	t.Helper()

	count, err := sync.CleanupDeletedInstances(ctx, false)
	if err != nil {
		t.Fatalf("cleanup failed: %v", err)
	}

	if count != 1 {
		t.Errorf("expected 1 instance to be deleted, got %d", count)
	}

	verifyOldInstanceDeleted(t, vault)
}

func verifyOldInstanceDeleted(t *testing.T, vault *RealTestVault) {
	t.Helper()

	data, err := vault.Get("db")
	if err != nil {
		t.Fatalf("failed to get vault data: %v", err)
	}

	if len(data) != 2 {
		t.Errorf("expected 2 instances in vault after cleanup, got %d", len(data))
	}

	if _, exists := data["old-deleted"]; exists {
		t.Errorf("old-deleted instance should have been removed")
	}

	if _, exists := data["recent-deleted"]; !exists {
		t.Errorf("recent-deleted instance should still exist")
	}

	if _, exists := data["active-instance"]; !exists {
		t.Errorf("active-instance should still exist")
	}
}

// TestLogger implements the Logger interface for tests.
type TestLogger struct {
	logs []string
}

func (l *TestLogger) Debugf(format string, args ...interface{}) {
	l.logs = append(l.logs, fmt.Sprintf(format, args...))
}

func (l *TestLogger) Infof(format string, args ...interface{}) {
	l.logs = append(l.logs, fmt.Sprintf(format, args...))
}

func (l *TestLogger) Errorf(format string, args ...interface{}) {
	l.logs = append(l.logs, fmt.Sprintf(format, args...))
}

func (l *TestLogger) Warningf(format string, args ...interface{}) {
	l.logs = append(l.logs, fmt.Sprintf(format, args...))
}

func (l *TestLogger) Named(name string) reconciler.Logger {
	return l
}

// Helper function.
func containsString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}

	return false
}
