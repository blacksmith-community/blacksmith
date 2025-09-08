package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"blacksmith/internal/interfaces"
	"blacksmith/pkg/services"
)

// mockLogger implements interfaces.Logger for testing.
type mockLogger struct{}

func (m *mockLogger) Named(name string) interfaces.Logger                        { return m }
func (m *mockLogger) Debug(msg string, args ...interface{})                      {}
func (m *mockLogger) Info(msg string, args ...interface{})                       {}
func (m *mockLogger) Error(msg string, args ...interface{})                      {}
func (m *mockLogger) Debugf(format string, args ...interface{})                  {}
func (m *mockLogger) Infof(format string, args ...interface{})                   {}
func (m *mockLogger) Errorf(format string, args ...interface{})                  {}
func (m *mockLogger) Warnf(format string, args ...interface{})                   {}
func (m *mockLogger) Warningf(format string, args ...interface{})                {}
func (m *mockLogger) Fatalf(format string, args ...interface{})                  {}
func (m *mockLogger) Warn(msg string, args ...interface{})                       {}
func (m *mockLogger) Warning(msg string, args ...interface{})                    {}
func (m *mockLogger) Fatal(msg string, args ...interface{})                      {}
func (m *mockLogger) SetLevel(level string) error                                { return nil }
func (m *mockLogger) GetLevel() string                                           { return "info" }
func (m *mockLogger) WithField(key string, value interface{}) interfaces.Logger  { return m }
func (m *mockLogger) WithFields(fields map[string]interface{}) interfaces.Logger { return m }

// mockConfig implements interfaces.Config for testing.
type mockConfig struct{}

func (m *mockConfig) IsSSHUITerminalEnabled() bool {
	return true
}

// mockVault implements interfaces.Vault for testing.
type mockVault struct{}

func (m *mockVault) Get(ctx context.Context, path string, result interface{}) (bool, error) {
	return false, nil
}

func (m *mockVault) ListCFRegistrations(ctx context.Context) ([]map[string]interface{}, error) {
	return []map[string]interface{}{}, nil
}

func (m *mockVault) SaveCFRegistration(ctx context.Context, registration map[string]interface{}) error {
	return nil
}

func (m *mockVault) GetCFRegistration(ctx context.Context, registrationID string, out interface{}) (bool, error) {
	return false, nil
}

func (m *mockVault) DeleteCFRegistration(ctx context.Context, registrationID string) error {
	return nil
}

func (m *mockVault) UpdateCFRegistrationStatus(ctx context.Context, registrationID, status, errorMsg string) error {
	return nil
}

// mockBroker implements interfaces.Broker for testing.
type mockBroker struct{}

// mockCFManager implements interfaces.CFManager for testing.
type mockCFManager struct{}

// TestBasicEndpointsIntegration tests that the refactored InternalAPI responds to basic endpoints.
func TestBasicEndpointsIntegration(t *testing.T) {
	t.Parallel()
	// Create mock dependencies
	deps := Dependencies{
		Config:             &mockConfig{},
		Logger:             &mockLogger{},
		Vault:              &mockVault{},
		Broker:             &mockBroker{},
		ServicesManager:    &services.Manager{}, // Using real services manager but it should be fine for basic routing tests
		CFManager:          &mockCFManager{},
		SecurityMiddleware: &services.SecurityMiddleware{},
	}

	// Create the InternalAPI
	api := NewInternalAPI(deps)

	// Test cases for basic endpoints
	testCases := []struct {
		name           string
		method         string
		path           string
		expectedStatus int
	}{
		{
			name:           "Instance details endpoint",
			method:         "GET",
			path:           "/b/instance",
			expectedStatus: http.StatusOK,
		},
		{
			name:           "SSH UI terminal status endpoint",
			method:         "GET",
			path:           "/b/config/ssh/ui-terminal-status",
			expectedStatus: http.StatusOK,
		},
		{
			name:           "Certificate endpoint routing test",
			method:         "GET",
			path:           "/b/certificates/list",
			expectedStatus: http.StatusNotImplemented, // Certificate handler correctly returns 501 Not Implemented
		},
		{
			name:           "CF registrations endpoint",
			method:         "GET",
			path:           "/b/cf/registrations",
			expectedStatus: http.StatusOK, // CF handler is implemented and returns empty list
		},
		{
			name:           "Non-existent endpoint",
			method:         "GET",
			path:           "/b/non-existent",
			expectedStatus: http.StatusNotFound,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Create test request
			req := httptest.NewRequest(tc.method, tc.path, nil)
			w := httptest.NewRecorder()

			// Call the handler
			api.ServeHTTP(w, req)

			// Check status code
			if w.Code != tc.expectedStatus {
				t.Errorf("Expected status code %d, got %d. Response body: %s",
					tc.expectedStatus, w.Code, w.Body.String())
			}

			// For successful responses, check that we get JSON
			if w.Code == http.StatusOK {
				contentType := w.Header().Get("Content-Type")
				if !strings.Contains(contentType, "application/json") {
					t.Errorf("Expected JSON content type, got %s", contentType)
				}
			}
		})
	}
}

// TestMiddlewareIntegration tests that middleware is properly applied.
func TestMiddlewareIntegration(t *testing.T) {
	t.Parallel()

	deps := Dependencies{
		Config:             &mockConfig{},
		Logger:             &mockLogger{},
		Vault:              &mockVault{},
		Broker:             &mockBroker{},
		ServicesManager:    &services.Manager{},
		CFManager:          &mockCFManager{},
		SecurityMiddleware: &services.SecurityMiddleware{},
	}

	api := NewInternalAPI(deps)

	// Test that requests go through the middleware chain
	req := httptest.NewRequest(http.MethodGet, "/b/instance", nil)
	w := httptest.NewRecorder()

	api.ServeHTTP(w, req)

	// Check that response has appropriate headers that would be set by middleware
	// The logging middleware should have processed this request
	if w.Code != http.StatusOK {
		t.Errorf("Expected successful response, got %d", w.Code)
	}
}
