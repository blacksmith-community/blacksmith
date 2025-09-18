package reconciler_test

import (
	"testing"
	"time"

	. "blacksmith/pkg/reconciler"
)

// Simple unit tests for synchronizer validation functions

func TestIsValidInstanceID_Simple(t *testing.T) {
	t.Parallel()

	tests := []struct {
		id    string
		valid bool
	}{
		{"550e8400-e29b-41d4-a716-446655440000", true},
		{"87654321-4321-4321-4321-cba987654321", true},
		{"not-a-uuid", false},
		{"12345678-1234-1234-1234", false},
		{"", false},
	}

	for _, testCase := range tests {
		t.Run(testCase.id, func(t *testing.T) {
			t.Parallel()

			result := IsValidInstanceID(testCase.id)
			if result != testCase.valid {
				t.Errorf("IsValidInstanceID(%q) = %v, want %v", testCase.id, result, testCase.valid)
			}
		})
	}
}

func TestIsLegacyDeploymentName_Simple(t *testing.T) {
	t.Parallel()

	synchronizer := &IndexSynchronizer{}

	tests := []struct {
		name       string
		deployment string
		planID     string
		instanceID string
		expect     bool
	}{
		{
			name:       "standard pattern",
			deployment: "plan-1-550e8400-e29b-41d4-a716-446655440000",
			planID:     "plan-1",
			instanceID: "550e8400-e29b-41d4-a716-446655440000",
			expect:     true,
		},
		{
			name:       "blacksmith prefix",
			deployment: "blacksmith-plan-1-550e8400-e29b-41d4-a716-446655440000",
			planID:     "plan-1",
			instanceID: "550e8400-e29b-41d4-a716-446655440000",
			expect:     true,
		},
		{
			name:       "service prefix",
			deployment: "service-plan-1-550e8400-e29b-41d4-a716-446655440000",
			planID:     "plan-1",
			instanceID: "550e8400-e29b-41d4-a716-446655440000",
			expect:     true,
		},
		{
			name:       "underscore separator",
			deployment: "plan-1_550e8400-e29b-41d4-a716-446655440000",
			planID:     "plan-1",
			instanceID: "550e8400-e29b-41d4-a716-446655440000",
			expect:     true,
		},
		{
			name:       "wrong pattern",
			deployment: "something-else",
			planID:     "plan-1",
			instanceID: "550e8400-e29b-41d4-a716-446655440000",
			expect:     false,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			result := synchronizer.IsLegacyDeploymentName(testCase.deployment, testCase.planID, testCase.instanceID)
			if result != testCase.expect {
				t.Errorf("IsLegacyDeploymentName(%q, %q, %q) = %v, want %v",
					testCase.deployment, testCase.planID, testCase.instanceID, result, testCase.expect)
			}
		})
	}
}

//nolint:funlen // This test function is intentionally long for comprehensive testing
func TestCheckEntryWarnings_Simple(t *testing.T) {
	t.Parallel()

	synchronizer := &IndexSynchronizer{}

	tests := []struct {
		name         string
		data         map[string]interface{}
		expectWarn   bool
		warnContains string
	}{
		{
			name: "healthy instance",
			data: map[string]interface{}{
				"service_id":      "service-1",
				"plan_id":         "plan-1",
				"deployment_name": "plan-1-instance-1",
				"reconciled":      true,
				"reconciled_at":   time.Now().Format(time.RFC3339),
			},
			expectWarn: false,
		},
		{
			name: "orphaned instance",
			data: map[string]interface{}{
				"orphaned":    true,
				"orphaned_at": time.Now().Add(-8 * 24 * time.Hour).Format(time.RFC3339),
			},
			expectWarn:   true,
			warnContains: "orphaned",
		},
		{
			name: "never reconciled",
			data: map[string]interface{}{
				"service_id": "service-1",
				"plan_id":    "plan-1",
			},
			expectWarn:   true,
			warnContains: "never reconciled",
		},
		{
			name: "missing deployment name",
			data: map[string]interface{}{
				"service_id":    "service-1",
				"plan_id":       "plan-1",
				"reconciled":    true,
				"reconciled_at": time.Now().Format(time.RFC3339),
			},
			expectWarn:   true,
			warnContains: "missing deployment name",
		},
		{
			name: "old reconciliation",
			data: map[string]interface{}{
				"service_id":      "service-1",
				"plan_id":         "plan-1",
				"deployment_name": "plan-1-instance-1",
				"reconciled":      true,
				"reconciled_at":   time.Now().Add(-48 * time.Hour).Format(time.RFC3339),
			},
			expectWarn:   true,
			warnContains: "not reconciled for",
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			warning := synchronizer.CheckEntryWarnings("test-id", testCase.data)
			if testCase.expectWarn && warning == "" {
				t.Error("Expected warning but got none")
			}

			if !testCase.expectWarn && warning != "" {
				t.Errorf("Unexpected warning: %s", warning)
			}

			if testCase.expectWarn && testCase.warnContains != "" {
				if !contains(warning, testCase.warnContains) {
					t.Errorf("Warning %q doesn't contain expected text %q", warning, testCase.warnContains)
				}
			}
		})
	}
}

// Helper function.
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && len(substr) > 0 && s[:len(substr)] == substr || len(s) > len(substr) && s[len(s)-len(substr):] == substr || (len(substr) > 0 && len(s) > len(substr) && findSubstring(s, substr)))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}

	return false
}
