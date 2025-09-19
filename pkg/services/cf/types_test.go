package cf_test

import (
	"strings"
	"testing"

	. "blacksmith/pkg/services/cf"
)

//nolint:funlen // This test function is intentionally long for comprehensive testing
func TestValidateRegistrationRequest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		req     *RegistrationRequest
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid request",
			req: &RegistrationRequest{
				Name:     "test-registration",
				APIURL:   "https://api.cf.example.com",
				Username: "admin",
				Password: "password123",
			},
			wantErr: false,
		},
		{
			name:    "nil request",
			req:     nil,
			wantErr: true,
			errMsg:  "registration request cannot be nil",
		},
		{
			name: "missing name",
			req: &RegistrationRequest{
				APIURL:   "https://api.cf.example.com",
				Username: "admin",
				Password: "password123",
			},
			wantErr: true,
			errMsg:  "registration name is required",
		},
		{
			name: "missing API URL",
			req: &RegistrationRequest{
				Name:     "test-registration",
				Username: "admin",
				Password: "password123",
			},
			wantErr: true,
			errMsg:  "CF API URL is required",
		},
		{
			name: "invalid URL - http instead of https",
			req: &RegistrationRequest{
				Name:     "test-registration",
				APIURL:   "http://api.cf.example.com",
				Username: "admin",
				Password: "password123",
			},
			wantErr: true,
			errMsg:  "API URL must use HTTPS protocol",
		},
		{
			name: "private network URL blocked",
			req: &RegistrationRequest{
				Name:     "test-registration",
				APIURL:   "https://192.168.1.100",
				Username: "admin",
				Password: "password123",
			},
			wantErr: true,
			errMsg:  "private/local network URLs are not allowed",
		},
		{
			name: "short password",
			req: &RegistrationRequest{
				Name:     "test-registration",
				APIURL:   "https://api.cf.example.com",
				Username: "admin",
				Password: "123",
			},
			wantErr: true,
			errMsg:  "password must be at least 6 characters long",
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			err := ValidateRegistrationRequest(testCase.req)
			if (err != nil) != testCase.wantErr {
				t.Errorf("ValidateRegistrationRequest() error = %v, wantErr %v", err, testCase.wantErr)

				return
			}

			if testCase.wantErr && testCase.errMsg != "" && err != nil {
				if !containsErrorMessage(err.Error(), testCase.errMsg) {
					t.Errorf("ValidateRegistrationRequest() error = %v, expected to contain %v", err.Error(), testCase.errMsg)
				}
			}
		})
	}
}

//nolint:funlen // This test function is intentionally long for comprehensive testing

//nolint:funlen
func TestValidateURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		url     string
		wantErr bool
		errMsg  string
	}{
		{
			name:    "valid HTTPS URL",
			url:     "https://api.cf.example.com",
			wantErr: false,
		},
		{
			name:    "valid HTTPS URL with path",
			url:     "https://api.cf.example.com/v3",
			wantErr: false,
		},
		{
			name:    "HTTP URL not allowed",
			url:     "http://api.cf.example.com",
			wantErr: true,
			errMsg:  "API URL must use HTTPS protocol",
		},
		{
			name:    "localhost blocked",
			url:     "https://localhost:8080",
			wantErr: true,
			errMsg:  "private/local network URLs are not allowed",
		},
		{
			name:    "private IP blocked",
			url:     "https://192.168.1.100",
			wantErr: true,
			errMsg:  "private/local network URLs are not allowed",
		},
		{
			name:    "malformed URL",
			url:     "not-a-url",
			wantErr: true,
			errMsg:  "API URL must use HTTPS protocol",
		},
		{
			name:    "empty hostname",
			url:     "https://",
			wantErr: true,
			errMsg:  "API URL must have a valid hostname",
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			err := ValidateURL(testCase.url)
			if (err != nil) != testCase.wantErr {
				t.Errorf("ValidateURL() error = %v, wantErr %v", err, testCase.wantErr)

				return
			}

			if testCase.wantErr && testCase.errMsg != "" && err != nil {
				if !containsErrorMessage(err.Error(), testCase.errMsg) {
					t.Errorf("ValidateURL() error = %v, expected to contain %v", err.Error(), testCase.errMsg)
				}
			}
		})
	}
}

// runValidationTests is a helper function to run validation tests with the given validator function.
func runValidationTests(t *testing.T, validatorName string, validator func(string) error, tests []struct {
	name    string
	input   string
	wantErr bool
	errMsg  string
}) {
	t.Helper()

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			err := validator(testCase.input)
			if (err != nil) != testCase.wantErr {
				t.Errorf("%s() error = %v, wantErr %v", validatorName, err, testCase.wantErr)

				return
			}

			if testCase.wantErr && testCase.errMsg != "" && err != nil {
				if !containsErrorMessage(err.Error(), testCase.errMsg) {
					t.Errorf("%s() error = %v, expected to contain %v", validatorName, err.Error(), testCase.errMsg)
				}
			}
		})
	}
}

func TestValidateName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		wantErr bool
		errMsg  string
	}{
		{
			name:    "valid name",
			input:   "test-registration",
			wantErr: false,
		},
		{
			name:    "valid name with spaces",
			input:   "Test Registration 01",
			wantErr: false,
		},
		{
			name:    "too short",
			input:   "ab",
			wantErr: true,
			errMsg:  "name must be at least 3 characters long",
		},
		{
			name:    "too long",
			input:   "this-is-a-very-long-registration-name-that-exceeds-the-limit",
			wantErr: true,
			errMsg:  "name must be no more than 50 characters long",
		},
		{
			name:    "script injection attempt",
			input:   "test<script>alert('xss')</script>",
			wantErr: true,
			errMsg:  "name can only contain letters, numbers, spaces, hyphens, and underscores",
		},
		{
			name:    "invalid characters",
			input:   "test@registration#",
			wantErr: true,
			errMsg:  "name can only contain letters, numbers, spaces, hyphens, and underscores",
		},
	}

	runValidationTests(t, "ValidateName", ValidateName, tests)
}

func TestValidateBrokerName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		wantErr bool
		errMsg  string
	}{
		{
			name:    "valid broker name",
			input:   "blacksmith",
			wantErr: false,
		},
		{
			name:    "valid broker name with hyphens",
			input:   "my-broker-01",
			wantErr: false,
		},
		{
			name:    "too short",
			input:   "ab",
			wantErr: true,
			errMsg:  "broker name must be at least 3 characters long",
		},
		{
			name:    "spaces not allowed",
			input:   "my broker",
			wantErr: true,
			errMsg:  "broker name can only contain letters, numbers, and hyphens",
		},
		{
			name:    "starts with hyphen",
			input:   "-broker",
			wantErr: true,
			errMsg:  "broker name must start and end with a letter or number",
		},
		{
			name:    "ends with hyphen",
			input:   "broker-",
			wantErr: true,
			errMsg:  "broker name must start and end with a letter or number",
		},
	}

	runValidationTests(t, "ValidateBrokerName", ValidateBrokerName, tests)
}

type registrationRequestTestCase struct {
	name     string
	input    *RegistrationRequest
	expected *RegistrationRequest
}

func getRegistrationRequestTestCases() []registrationRequestTestCase {
	return []registrationRequestTestCase{
		{
			name: "trim whitespace",
			input: &RegistrationRequest{
				Name:       "  test-registration  ",
				APIURL:     "  https://api.cf.example.com/  ",
				Username:   "  admin  ",
				BrokerName: "  my-broker  ",
			},
			expected: &RegistrationRequest{
				Name:       "test-registration",
				APIURL:     "https://api.cf.example.com",
				Username:   "admin",
				BrokerName: "my-broker",
			},
		},
		{
			name: "set default broker name",
			input: &RegistrationRequest{
				Name:       "test-registration",
				APIURL:     "https://api.cf.example.com",
				Username:   "admin",
				BrokerName: "",
			},
			expected: &RegistrationRequest{
				Name:       "test-registration",
				APIURL:     "https://api.cf.example.com",
				Username:   "admin",
				BrokerName: "blacksmith",
			},
		},
		{
			name: "sanitize metadata",
			input: &RegistrationRequest{
				Name:     "test-registration",
				APIURL:   "https://api.cf.example.com",
				Username: "admin",
				Metadata: map[string]string{
					"env":      "  production  ",
					"password": "secret123",
					"token":    "abc123",
					"region":   "us-west-1",
				},
			},
			expected: &RegistrationRequest{
				Name:       "test-registration",
				APIURL:     "https://api.cf.example.com",
				Username:   "admin",
				BrokerName: "blacksmith",
				Metadata: map[string]string{
					"env":    "production",
					"region": "us-west-1",
				},
			},
		},
	}
}

func runRegistrationRequestTest(t *testing.T, testCase registrationRequestTestCase) {
	t.Helper()
	SanitizeRegistrationRequest(testCase.input)

	validateBasicFields(t, testCase.input, testCase.expected)
	validateMetadata(t, testCase.input, testCase.expected)
}

func validateBasicFields(t *testing.T, input, expected *RegistrationRequest) {
	t.Helper()

	if input.Name != expected.Name {
		t.Errorf("Name = %v, expected %v", input.Name, expected.Name)
	}

	if input.APIURL != expected.APIURL {
		t.Errorf("APIURL = %v, expected %v", input.APIURL, expected.APIURL)
	}

	if input.Username != expected.Username {
		t.Errorf("Username = %v, expected %v", input.Username, expected.Username)
	}

	if input.BrokerName != expected.BrokerName {
		t.Errorf("BrokerName = %v, expected %v", input.BrokerName, expected.BrokerName)
	}
}

func validateMetadata(t *testing.T, input, expected *RegistrationRequest) {
	t.Helper()

	if expected.Metadata == nil {
		return
	}

	validateExpectedMetadataExists(t, input, expected)
	validateSensitiveKeysRemoved(t, input, expected)
}

func validateExpectedMetadataExists(t *testing.T, input, expected *RegistrationRequest) {
	t.Helper()

	for key, expectedValue := range expected.Metadata {
		if actualValue, exists := input.Metadata[key]; !exists || actualValue != expectedValue {
			t.Errorf("Metadata[%s] = %v, expected %v", key, actualValue, expectedValue)
		}
	}
}

func validateSensitiveKeysRemoved(t *testing.T, input, expected *RegistrationRequest) {
	t.Helper()

	for key := range input.Metadata {
		if _, expectedExists := expected.Metadata[key]; !expectedExists {
			t.Errorf("Metadata[%s] should have been removed but still exists", key)
		}
	}
}

func TestSanitizeRegistrationRequest(t *testing.T) {
	t.Parallel()

	tests := getRegistrationRequestTestCases()

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			runRegistrationRequestTest(t, testCase)
		})
	}
}

// Helper function to check if error message contains expected text.
func containsErrorMessage(actual, expected string) bool {
	return strings.Contains(actual, expected)
}
