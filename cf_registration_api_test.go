package main

import (
	"strings"
	"testing"

	"blacksmith/pkg/services/cf"
)

// TestCFRegistrationValidation tests the CF registration validation integration
func TestCFRegistrationValidation(t *testing.T) {
	tests := []struct {
		name           string
		request        cf.RegistrationRequest
		expectError    bool
		expectedErrMsg string
	}{
		{
			name: "valid registration request",
			request: cf.RegistrationRequest{
				Name:     "test-registration",
				APIURL:   "https://api.cf.example.com",
				Username: "admin",
				Password: "password123",
			},
			expectError: false,
		},
		{
			name: "invalid URL - HTTP instead of HTTPS",
			request: cf.RegistrationRequest{
				Name:     "test-registration",
				APIURL:   "http://api.cf.example.com",
				Username: "admin",
				Password: "password123",
			},
			expectError:    true,
			expectedErrMsg: "API URL must use HTTPS protocol",
		},
		{
			name: "missing required field",
			request: cf.RegistrationRequest{
				APIURL:   "https://api.cf.example.com",
				Username: "admin",
				Password: "password123",
			},
			expectError:    true,
			expectedErrMsg: "registration name is required",
		},
		{
			name: "private network URL blocked",
			request: cf.RegistrationRequest{
				Name:     "test-registration",
				APIURL:   "https://192.168.1.100",
				Username: "admin",
				Password: "password123",
			},
			expectError:    true,
			expectedErrMsg: "private/local network URLs are not allowed",
		},
		{
			name: "name with dangerous content",
			request: cf.RegistrationRequest{
				Name:     "test<script>alert('xss')</script>",
				APIURL:   "https://api.cf.example.com",
				Username: "admin",
				Password: "password123",
			},
			expectError:    true,
			expectedErrMsg: "name can only contain letters, numbers, spaces, hyphens, and underscores",
		},
		{
			name: "short password",
			request: cf.RegistrationRequest{
				Name:     "test-registration",
				APIURL:   "https://api.cf.example.com",
				Username: "admin",
				Password: "123",
			},
			expectError:    true,
			expectedErrMsg: "password must be at least 6 characters long",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := cf.ValidateRegistrationRequest(&tt.request)
			if (err != nil) != tt.expectError {
				t.Errorf("ValidateRegistrationRequest() error = %v, wantErr %v", err, tt.expectError)
				return
			}
			if tt.expectError && tt.expectedErrMsg != "" && err != nil {
				if !strings.Contains(err.Error(), tt.expectedErrMsg) {
					t.Errorf("ValidateRegistrationRequest() error = %v, expected to contain %v", err.Error(), tt.expectedErrMsg)
				}
			}
		})
	}
}

// TestCFRegistrationSanitization tests the sanitization functionality
func TestCFRegistrationSanitization(t *testing.T) {
	request := &cf.RegistrationRequest{
		Name:       "  test-registration  ",
		APIURL:     "  https://api.cf.example.com/  ",
		Username:   "  admin  ",
		BrokerName: "  ",
		Metadata: map[string]string{
			"env":      "  production  ",
			"password": "secret123",
			"token":    "abc123",
			"region":   "us-west-1",
		},
	}

	cf.SanitizeRegistrationRequest(request)

	// Check trimmed fields
	if request.Name != "test-registration" {
		t.Errorf("Name not trimmed correctly: got %s", request.Name)
	}
	if request.APIURL != "https://api.cf.example.com" {
		t.Errorf("APIURL not trimmed correctly: got %s", request.APIURL)
	}
	if request.Username != "admin" {
		t.Errorf("Username not trimmed correctly: got %s", request.Username)
	}

	// Check default broker name set
	if request.BrokerName != "blacksmith" {
		t.Errorf("Default broker name not set: got %s", request.BrokerName)
	}

	// Check metadata sanitization
	if _, exists := request.Metadata["password"]; exists {
		t.Error("Password metadata should have been removed")
	}
	if _, exists := request.Metadata["token"]; exists {
		t.Error("Token metadata should have been removed")
	}
	if request.Metadata["env"] != "production" {
		t.Errorf("Env metadata not trimmed: got %s", request.Metadata["env"])
	}
	if request.Metadata["region"] != "us-west-1" {
		t.Errorf("Region metadata incorrect: got %s", request.Metadata["region"])
	}
}

// TestCFValidationHelper tests individual validation helper functions
func TestCFValidationHelper(t *testing.T) {
	t.Run("ValidateURL", func(t *testing.T) {
		validURLs := []string{
			"https://api.cf.example.com",
			"https://api.system.cf.example.com:443",
			"https://cf.pivotal.io",
		}

		for _, url := range validURLs {
			if err := cf.ValidateURL(url); err != nil {
				t.Errorf("Expected %s to be valid, got error: %v", url, err)
			}
		}

		invalidURLs := map[string]string{
			"http://api.cf.example.com": "HTTPS protocol",
			"https://localhost:8080":    "private/local network URLs",
			"https://127.0.0.1":         "private/local network URLs",
			"https://192.168.1.100":     "private/local network URLs",
			"https://10.0.0.1":          "private/local network URLs",
			"malformed":                 "HTTPS protocol",
		}

		for url, expectedErr := range invalidURLs {
			if err := cf.ValidateURL(url); err == nil {
				t.Errorf("Expected %s to be invalid", url)
			} else if !strings.Contains(err.Error(), expectedErr) {
				t.Errorf("Expected error containing %s, got: %v", expectedErr, err)
			}
		}
	})

	t.Run("ValidateName", func(t *testing.T) {
		validNames := []string{
			"test-registration",
			"Test Registration 01",
			"my_broker_name",
			"Production-CF-Env",
		}

		for _, name := range validNames {
			if err := cf.ValidateName(name); err != nil {
				t.Errorf("Expected %s to be valid, got error: %v", name, err)
			}
		}

		invalidNames := map[string]string{
			"ab":                "at least 3 characters",
			"test@registration": "letters, numbers, spaces, hyphens, and underscores",
			"test<script>":      "letters, numbers, spaces, hyphens, and underscores",
			"this-is-way-too-long-registration-name-that-exceeds": "no more than 50 characters",
		}

		for name, expectedErr := range invalidNames {
			if err := cf.ValidateName(name); err == nil {
				t.Errorf("Expected %s to be invalid", name)
			} else if !strings.Contains(err.Error(), expectedErr) {
				t.Errorf("Expected error containing %s, got: %v", expectedErr, err)
			}
		}
	})
}
