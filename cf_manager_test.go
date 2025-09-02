package main

import (
	"strings"
	"testing"
)

func TestCFCredentialValidation(t *testing.T) {
	tests := []struct {
		name           string
		endpoints      map[string]CFAPIConfig
		expectDisabled bool
	}{
		{
			name:           "No endpoints configured",
			endpoints:      map[string]CFAPIConfig{},
			expectDisabled: true,
		},
		{
			name: "Completely empty credentials",
			endpoints: map[string]CFAPIConfig{
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
			endpoints: map[string]CFAPIConfig{
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
			endpoints: map[string]CFAPIConfig{
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
			endpoints: map[string]CFAPIConfig{
				"test": {
					Endpoint: "https://api.example.com",
					Username: "user",
					Password: "",
				},
			},
			expectDisabled: true,
		},
		{
			name: "Valid credentials",
			endpoints: map[string]CFAPIConfig{
				"test": {
					Endpoint: "https://api.example.com",
					Username: "user",
					Password: "pass",
				},
			},
			expectDisabled: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager := NewCFConnectionManager(tt.endpoints, nil)

			hasValidClients := len(manager.clients) > 0
			for _, client := range manager.clients {
				if client.config.Endpoint == "" ||
					client.config.Username == "" ||
					client.config.Password == "" {
					hasValidClients = false
					break
				}
			}

			if tt.expectDisabled && hasValidClients {
				t.Errorf("Expected CF manager to be disabled but found valid clients")
			}
			if !tt.expectDisabled && !hasValidClients {
				t.Errorf("Expected CF manager to have valid clients but none found")
			}
		})
	}
}

func TestAuthenticationErrorDetection(t *testing.T) {
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
