package reconciler

import (
	"testing"
	"time"
)

// Simple unit tests for synchronizer validation functions

func TestIsValidInstanceID_Simple(t *testing.T) {
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

	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			result := isValidInstanceID(tt.id)
			if result != tt.valid {
				t.Errorf("isValidInstanceID(%q) = %v, want %v", tt.id, result, tt.valid)
			}
		})
	}
}

func TestIsLegacyDeploymentName_Simple(t *testing.T) {
	s := &indexSynchronizer{}

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

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := s.isLegacyDeploymentName(tt.deployment, tt.planID, tt.instanceID)
			if result != tt.expect {
				t.Errorf("isLegacyDeploymentName(%q, %q, %q) = %v, want %v",
					tt.deployment, tt.planID, tt.instanceID, result, tt.expect)
			}
		})
	}
}

func TestCheckEntryWarnings_Simple(t *testing.T) {
	s := &indexSynchronizer{}

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

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			warning := s.checkEntryWarnings("test-id", tt.data)
			if tt.expectWarn && warning == "" {
				t.Error("Expected warning but got none")
			}
			if !tt.expectWarn && warning != "" {
				t.Errorf("Unexpected warning: %s", warning)
			}
			if tt.expectWarn && tt.warnContains != "" {
				if !contains(warning, tt.warnContains) {
					t.Errorf("Warning %q doesn't contain expected text %q", warning, tt.warnContains)
				}
			}
		})
	}
}

// Helper function
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
