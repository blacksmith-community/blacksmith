package reconciler_test

import (
	"errors"
	"testing"

	. "blacksmith/pkg/reconciler"
)

//nolint:funlen // This test function is intentionally long for comprehensive testing
func TestServiceMatcher_MatchDeployment_ValidUUIDs(t *testing.T) {
	t.Parallel()

	services := []Service{
		{
			ID:   "redis-cache",
			Name: "Redis Cache",
			Plans: []Plan{
				{ID: "small", Name: "Small"},
				{ID: "medium", Name: "Medium"},
			},
		},
		{
			ID:   "postgres",
			Name: "PostgreSQL",
			Plans: []Plan{
				{ID: "basic", Name: "Basic"},
			},
		},
	}

	broker := &mockBroker{services: services}
	logger := NewMockLogger()

	matcher := NewServiceMatcher(broker, logger)

	tests := []struct {
		name        string
		deployment  DeploymentDetail
		expected    *MatchResult
		expectError bool
	}{
		{
			name: "exact match with service-plan-uuid format",
			deployment: DeploymentDetail{
				DeploymentInfo: DeploymentInfo{
					Name: "redis-cache-small-12345678-1234-1234-1234-123456789abc",
				},
			},
			expected: &MatchResult{
				ServiceID:   "redis-cache",
				PlanID:      "small",
				InstanceID:  "12345678-1234-1234-1234-123456789abc",
				Confidence:  1.0,
				MatchReason: "exact_name_match",
			},
		},
		{
			name: "postgres deployment match",
			deployment: DeploymentDetail{
				DeploymentInfo: DeploymentInfo{
					Name: "postgres-basic-87654321-4321-4321-4321-cba987654321",
				},
			},
			expected: &MatchResult{
				ServiceID:   "postgres",
				PlanID:      "basic",
				InstanceID:  "87654321-4321-4321-4321-cba987654321",
				Confidence:  1.0,
				MatchReason: "exact_name_match",
			},
		},
		{
			name: "deployment with no UUID",
			deployment: DeploymentDetail{
				DeploymentInfo: DeploymentInfo{
					Name: "some-other-deployment",
				},
			},
			expected:    nil,
			expectError: true,
		},
		{
			name: "deployment with invalid UUID",
			deployment: DeploymentDetail{
				DeploymentInfo: DeploymentInfo{
					Name: "redis-cache-small-not-a-valid-uuid",
				},
			},
			expected:    nil,
			expectError: true,
		},
		{
			name: "unknown service",
			deployment: DeploymentDetail{
				DeploymentInfo: DeploymentInfo{
					Name: "unknown-service-plan-12345678-1234-1234-1234-123456789abc",
				},
			},
			expected:    nil,
			expectError: true,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			result, err := matcher.MatchDeployment(testCase.deployment, services)

			validateTestError(t, testCase.expectError, err)
			validateTestResult(t, testCase.expected, result)
		})
	}
}

// validateTestError validates error expectations.
func validateTestError(t *testing.T, expectError bool, err error) {
	t.Helper()

	if expectError && err == nil {
		t.Error("Expected error but got none")
	}

	if !expectError && err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
}

// validateTestResult validates test result expectations.
func validateTestResult(t *testing.T, expected, actual *MatchResult) {
	t.Helper()

	if expected == nil && actual != nil {
		t.Errorf("Expected no match but got: %+v", actual)

		return
	}

	if expected != nil && actual == nil {
		t.Error("Expected a match but got nil")

		return
	}

	if expected != nil && actual != nil {
		validateMatchResult(t, expected, actual)
	}
}

// validateMatchResult validates individual fields of MatchResult.
func validateMatchResult(t *testing.T, expected, actual *MatchResult) {
	t.Helper()

	if actual.ServiceID != expected.ServiceID {
		t.Errorf("ServiceID: expected %s, got %s", expected.ServiceID, actual.ServiceID)
	}

	if actual.PlanID != expected.PlanID {
		t.Errorf("PlanID: expected %s, got %s", expected.PlanID, actual.PlanID)
	}

	if actual.InstanceID != expected.InstanceID {
		t.Errorf("InstanceID: expected %s, got %s", expected.InstanceID, actual.InstanceID)
	}
}

func TestServiceMatcher_MatchDeployment_ByMetadata(t *testing.T) {
	t.Parallel()

	services := []Service{
		{
			ID:   "redis-cache",
			Name: "Redis Cache",
			Plans: []Plan{
				{ID: "small", Name: "Small"},
			},
		},
	}

	broker := &mockBroker{services: services}
	logger := NewMockLogger()

	matcher := NewServiceMatcher(broker, logger)

	// Test matching by metadata - this should work via the alternative matching logic
	deployment := DeploymentDetail{
		DeploymentInfo: DeploymentInfo{
			Name: "custom-redis-deployment",
		},
		Manifest: `
name: custom-redis-deployment
properties:
  blacksmith:
    service_id: redis-cache
    plan_id: small
    instance_id: 12345678-1234-1234-1234-123456789abc
`,
	}

	instance, err := matcher.MatchDeployment(deployment, services)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if instance == nil {
		t.Fatal("Expected a match but got nil")
	}

	if instance.ServiceID != "redis-cache" {
		t.Errorf("Expected service ID redis-cache, got %s", instance.ServiceID)
	}

	if instance.PlanID != "small" {
		t.Errorf("Expected plan ID small, got %s", instance.PlanID)
	}

	if instance.InstanceID != "12345678-1234-1234-1234-123456789abc" {
		t.Errorf("Expected instance ID 12345678-1234-1234-1234-123456789abc, got %s", instance.InstanceID)
	}
}
//nolint:funlen // This test function is intentionally long for comprehensive testing

//nolint:funlen
func TestServiceMatcher_MatchDeployment_ByReleases(t *testing.T) {
	t.Parallel()

	services := []Service{
		{
			ID:   "redis-cache",
			Name: "Redis Cache",
			Plans: []Plan{
				{ID: "small", Name: "Small"},
			},
		},
		{
			ID:   "postgres",
			Name: "PostgreSQL",
			Plans: []Plan{
				{ID: "basic", Name: "Basic"},
			},
		},
	}

	broker := &mockBroker{services: services}
	logger := NewMockLogger()

	matcher := NewServiceMatcher(broker, logger)

	tests := []struct {
		name          string
		deployment    DeploymentDetail
		expectedMatch bool
		expectedSvc   string
	}{
		{
			name: "match by standard naming",
			deployment: DeploymentDetail{
				DeploymentInfo: DeploymentInfo{
					Name:     "redis-cache-small-12345678-1234-1234-1234-123456789abc",
					Releases: []string{"redis/1.0.0", "bpm/1.0.0"},
				},
			},
			expectedMatch: true,
			expectedSvc:   "redis-cache",
		},
		{
			name: "match postgres by standard naming",
			deployment: DeploymentDetail{
				DeploymentInfo: DeploymentInfo{
					Name:     "postgres-basic-87654321-4321-4321-4321-cba987654321",
					Releases: []string{"postgres/42.0.0"},
				},
			},
			expectedMatch: true,
			expectedSvc:   "postgres",
		},
		{
			name: "no matching - invalid naming",
			deployment: DeploymentDetail{
				DeploymentInfo: DeploymentInfo{
					Name:     "unknown-deployment",
					Releases: []string{"unknown-release/1.0.0"},
				},
			},
			expectedMatch: false,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			instance, err := matcher.MatchDeployment(testCase.deployment, services)
			
			if testCase.expectedMatch {
				if err != nil {
					t.Fatalf("Unexpected error: %v", err)
				}
				
				if instance == nil {
					t.Error("Expected a match but got nil")

					return
				}

				if instance.ServiceID != testCase.expectedSvc {
					t.Errorf("Expected service ID %s, got %s", testCase.expectedSvc, instance.ServiceID)
				}
			} else {
				if err == nil {
					t.Error("Expected ErrNoMatchFound error but got none")
				} else if !errors.Is(err, ErrNoMatchFound) {
					t.Errorf("Expected ErrNoMatchFound but got: %v", err)
				}
				
				if instance != nil {
					t.Errorf("Expected no match but got instance: %+v", instance)
				}
			}
		})
	}
}

//nolint:funlen
func TestServiceMatcher_ValidateMatch(t *testing.T) {
	t.Parallel()

	services := []Service{
		{
			ID:   "redis-service",
			Name: "Redis",
			Plans: []Plan{
				{ID: "small", Name: "Small"},
			},
		},
	}

	broker := &mockBroker{services: services}
	logger := NewMockLogger()

	matcher := NewServiceMatcher(broker, logger)

	tests := []struct {
		name        string
		match       *MatchResult
		expectError bool
	}{
		{
			name: "valid match with high confidence",
			match: &MatchResult{
				ServiceID:   "redis-service",
				PlanID:      "small",
				InstanceID:  "12345678-1234-1234-1234-123456789abc",
				Confidence:  0.9,
				MatchReason: "exact_match",
			},
			expectError: false,
		},
		{
			name: "low confidence match",
			match: &MatchResult{
				ServiceID:   "redis-service",
				PlanID:      "small",
				InstanceID:  "12345678-1234-1234-1234-123456789abc",
				Confidence:  0.2,
				MatchReason: "partial_match",
			},
			expectError: true,
		},
		{
			name:        "nil match",
			match:       nil,
			expectError: true,
		},
		{
			name: "missing service ID",
			match: &MatchResult{
				PlanID:      "small",
				InstanceID:  "12345678-1234-1234-1234-123456789abc",
				Confidence:  0.9,
				MatchReason: "exact_match",
			},
			expectError: true,
		},
		{
			name: "invalid UUID format",
			match: &MatchResult{
				ServiceID:   "redis-service",
				PlanID:      "small",
				InstanceID:  "not-a-uuid",
				Confidence:  0.9,
				MatchReason: "exact_match",
			},
			expectError: true,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			// Access the ValidateMatch method directly
			err := matcher.ValidateMatch(testCase.match)
			if testCase.expectError {
				if err == nil {
					t.Error("Expected error but got nil")
				}
			} else {
				if err != nil {
					t.Errorf("Expected no error but got: %v", err)
				}
			}
		})
	}
}

// Mock broker for testing.
type mockBroker struct {
	services []Service
}

func (b *mockBroker) GetServices() []Service {
	return b.services
}

// Test helper to check if UUID validation works.
func TestIsValidUUID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		uuid     string
		expected bool
	}{
		{"12345678-1234-1234-1234-123456789abc", true},
		{"87654321-4321-4321-4321-cba987654321", true},
		{"00000000-0000-0000-0000-000000000000", true},
		{"not-a-uuid", false},
		{"12345678-1234-1234-1234", false},
		{"12345678-1234-1234-1234-123456789abcd", false}, // Too long
		{"12345678_1234_1234_1234_123456789abc", false},  // Wrong separator
		{"", false},
	}

	for _, testCase := range tests {
		t.Run(testCase.uuid, func(t *testing.T) {
			t.Parallel()

			result := IsValidUUID(testCase.uuid)
			if result != testCase.expected {
				t.Errorf("Expected isValidUUID(%s) = %v, got %v", testCase.uuid, testCase.expected, result)
			}
		})
	}
}
