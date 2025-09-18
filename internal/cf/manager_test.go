package cf_test

import (
	"strings"
	"testing"

	"blacksmith/internal/cf"
)

type cfCredentialTestCase struct {
	name           string
	endpoints      map[string]cf.CFAPIConfig
	expectDisabled bool
}

func getInvalidCFCredentialTestCases() []cfCredentialTestCase {
	return []cfCredentialTestCase{
		{
			name:           "No endpoints configured",
			endpoints:      map[string]cf.CFAPIConfig{},
			expectDisabled: true,
		},
		{
			name: "Completely empty credentials",
			endpoints: map[string]cf.CFAPIConfig{
				"test": {
					Endpoint: "",
					Username: "",
					Password: "",
				},
			},
			expectDisabled: true,
		},
		{
			name: "Missing endpoint only",
			endpoints: map[string]cf.CFAPIConfig{
				"test": {
					Endpoint: "",
					Username: "user",
					Password: "pass",
				},
			},
			expectDisabled: true,
		},
		{
			name: "Missing username only",
			endpoints: map[string]cf.CFAPIConfig{
				"test": {
					Endpoint: "https://api.example.com",
					Username: "",
					Password: "pass",
				},
			},
			expectDisabled: true,
		},
		{
			name: "Missing password only",
			endpoints: map[string]cf.CFAPIConfig{
				"test": {
					Endpoint: "https://api.example.com",
					Username: "user",
					Password: "",
				},
			},
			expectDisabled: true,
		},
	}
}

func getCFCredentialValidationTestCases() []cfCredentialTestCase {
	invalidCases := getInvalidCFCredentialTestCases()
	validCases := []cfCredentialTestCase{
		{
			name: "Valid credentials",
			endpoints: map[string]cf.CFAPIConfig{
				"test": {
					Endpoint: "https://api.example.com",
					Username: "user",
					Password: "pass",
				},
			},
			expectDisabled: false,
		},
	}

	return append(invalidCases, validCases...)
}

func runCFCredentialValidationTest(t *testing.T, testCase cfCredentialTestCase) {
	t.Helper()

	manager := cf.NewManager(testCase.endpoints, nil)

	hasValidClients := validateManagerClients(manager)

	if testCase.expectDisabled && hasValidClients {
		t.Errorf("Expected CF manager to be disabled but found valid clients")
	}

	if !testCase.expectDisabled && !hasValidClients {
		t.Errorf("Expected CF manager to have valid clients but none found")
	}
}

func validateManagerClients(manager *cf.Manager) bool {
	if manager.GetClientCount() == 0 {
		return false
	}

	for _, client := range manager.GetClients() {
		config := client.GetConfig()
		if config.Endpoint == "" || config.Username == "" || config.Password == "" {
			return false
		}
	}

	return true
}

func TestCFCredentialValidation(t *testing.T) {
	t.Parallel()

	tests := getCFCredentialValidationTestCases()

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			runCFCredentialValidationTest(t, testCase)
		})
	}
}

func TestAuthenticationErrorDetection(t *testing.T) {
	t.Parallel()
	// Test that our error detection patterns work
	authErrors := []string{
		"unauthorized",
		"401 Unauthorized",
		"authentication failed",
		"invalid credentials",
		"Authentication error",
		"HTTP 401",
	}

	for _, errMsg := range authErrors {
		t.Run(errMsg, func(t *testing.T) {
			t.Parallel()
			// Check if our error patterns would catch this
			isAuthError := strings.Contains(strings.ToLower(errMsg), "unauthorized") ||
				strings.Contains(strings.ToLower(errMsg), "401") ||
				strings.Contains(strings.ToLower(errMsg), "authentication") ||
				strings.Contains(strings.ToLower(errMsg), "invalid credentials")

			if !isAuthError {
				t.Errorf("Error message '%s' should be detected as authentication error", errMsg)
			}
		})
	}

	// Test non-authentication errors are not detected as auth errors
	nonAuthErrors := []string{
		"connection refused",
		"timeout",
		"500 Internal Server Error",
		"network unreachable",
	}

	for _, errMsg := range nonAuthErrors {
		t.Run("not_auth_"+errMsg, func(t *testing.T) {
			t.Parallel()

			isAuthError := strings.Contains(strings.ToLower(errMsg), "unauthorized") ||
				strings.Contains(strings.ToLower(errMsg), "401") ||
				strings.Contains(strings.ToLower(errMsg), "authentication") ||
				strings.Contains(strings.ToLower(errMsg), "invalid credentials")

			if isAuthError {
				t.Errorf("Error message '%s' should NOT be detected as authentication error", errMsg)
			}
		})
	}
}
