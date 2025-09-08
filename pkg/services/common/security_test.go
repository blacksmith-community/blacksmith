package common_test

import (
	"testing"
	"time"

	"blacksmith/pkg/services/common"
)

func TestRateLimiter(t *testing.T) {
	t.Parallel()

	// Create a rate limiter with 2 requests per second
	rl := common.NewRateLimiter(2, time.Second)

	key := "test-key"

	// First two requests should be allowed
	if !rl.AllowRequest(key) {
		t.Error("First request should be allowed")
	}

	if !rl.AllowRequest(key) {
		t.Error("Second request should be allowed")
	}

	// Third request should be denied
	if rl.AllowRequest(key) {
		t.Error("Third request should be denied")
	}

	// Check remaining requests
	remaining := rl.GetRemainingRequests(key)
	if remaining != 0 {
		t.Errorf("Remaining requests = %d, expected 0", remaining)
	}

	// Wait for window to expire
	time.Sleep(1100 * time.Millisecond)

	// Should be allowed again
	if !rl.AllowRequest(key) {
		t.Error("Request should be allowed after window expires")
	}

	// Check remaining requests
	remaining = rl.GetRemainingRequests(key)
	if remaining != 1 {
		t.Errorf("Remaining requests = %d, expected 1", remaining)
	}
}

func TestMaskCredentials(t *testing.T) {
	t.Parallel()

	creds := common.Credentials{
		"host":        "example.com",
		"port":        6379,
		"password":    "secret123",
		"secret":      "topsecret",
		"token":       "auth_token",
		"key":         "secret_key",
		"private_key": "-----BEGIN RSA PRIVATE KEY-----",
		"certificate": "-----BEGIN CERTIFICATE-----",
		"public_data": "visible_data",
	}

	masked := common.MaskCredentials(creds)

	sensitiveFields := []string{"password", "secret", "token", "key", "private_key"}
	for _, field := range sensitiveFields {
		if masked[field] != "***MASKED***" {
			t.Errorf("Field %s should be masked, got: %v", field, masked[field])
		}
	}

	// Non-sensitive fields should be preserved
	if masked["host"] != "example.com" {
		t.Errorf("Host should be preserved")
	}

	if masked["certificate"] != "-----BEGIN CERTIFICATE-----" {
		t.Errorf("Certificate should be preserved")
	}

	if masked["public_data"] != "visible_data" {
		t.Errorf("Public data should be preserved")
	}
}

func TestValidateInputs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		inputs      map[string]interface{}
		expectError bool
	}{
		{
			name: "safe inputs",
			inputs: map[string]interface{}{
				"key":   "test-key",
				"value": "test-value",
				"count": 5,
			},
			expectError: false,
		},
		{
			name: "SQL injection attempt",
			inputs: map[string]interface{}{
				"key": "test'; DROP TABLE users; --",
			},
			expectError: true,
		},
		{
			name: "script injection attempt",
			inputs: map[string]interface{}{
				"message": "<script>alert('xss')</script>",
			},
			expectError: true,
		},
		{
			name: "safe special characters",
			inputs: map[string]interface{}{
				"json": `{"key": "value", "number": 123}`,
			},
			expectError: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			err := common.ValidateInputs(test.inputs)

			if test.expectError && err == nil {
				t.Error("Expected error but got none")
			}

			if !test.expectError && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
		})
	}
}

func TestContainsSQLInjection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input    string
		expected bool
	}{
		{"normal input", false},
		{"test'; DROP TABLE users; --", true},
		{"test\"; DROP TABLE users; --", true},
		{"' OR '1'='1", true},
		{"\" OR \"1\"=\"1", true},
		{"' UNION SELECT * FROM users", true},
		{"safe ' quote", false},
		{"safe \" quote", false},
		{"DROP not at start", false},
	}

	for _, test := range tests {
		result := common.ContainsSQLInjection(test.input)
		if result != test.expected {
			t.Errorf("containsSQLInjection(%q) = %v, expected %v", test.input, result, test.expected)
		}
	}
}

func TestContainsScriptInjection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input    string
		expected bool
	}{
		{"normal input", false},
		{"<script>alert('xss')</script>", true},
		{"javascript:alert('xss')", true},
		{"vbscript:msgbox('xss')", true},
		{"onload=alert('xss')", true},
		{"onclick=doSomething()", true},
		{"safe <tag> content", false},
		{"safe: colon content", false},
	}

	for _, test := range tests {
		result := common.ContainsScriptInjection(test.input)
		if result != test.expected {
			t.Errorf("containsScriptInjection(%q) = %v, expected %v", test.input, result, test.expected)
		}
	}
}
