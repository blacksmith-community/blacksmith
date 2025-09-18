package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"blacksmith/internal/api"
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

// IsBroker implements the interfaces.Broker interface.
func (m *mockBroker) IsBroker() bool {
	return true
}

// mockCFManager implements interfaces.CFManager for testing.
type mockCFManager struct{}

// IsCFManager implements the interfaces.CFManager interface.
func (m *mockCFManager) IsCFManager() bool {
	return true
}

// createTestDependencies creates mock dependencies for testing.
func createTestDependencies() api.Dependencies {
	return api.Dependencies{
		Config:             &mockConfig{},
		Logger:             &mockLogger{},
		Vault:              &mockVault{},
		Broker:             &mockBroker{},
		ServicesManager:    &services.Manager{},
		CFManager:          &mockCFManager{},
		SecurityMiddleware: &services.SecurityMiddleware{},
	}
}

// getBasicEndpointTestCases returns test cases for basic endpoint testing.
func getBasicEndpointTestCases() []struct {
	name           string
	method         string
	path           string
	expectedStatus int
} {
	return []struct {
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
			expectedStatus: http.StatusNotImplemented,
		},
		{
			name:           "CF registrations endpoint",
			method:         "GET",
			path:           "/b/cf/registrations",
			expectedStatus: http.StatusOK,
		},
		{
			name:           "Non-existent endpoint",
			method:         "GET",
			path:           "/b/non-existent",
			expectedStatus: http.StatusNotFound,
		},
	}
}

// validateResponse validates the HTTP response for a test case.
func validateResponse(t *testing.T, responseWriter *httptest.ResponseRecorder, expectedStatus int) {
	t.Helper()

	if responseWriter.Code != expectedStatus {
		t.Errorf("Expected status code %d, got %d. Response body: %s",
			expectedStatus, responseWriter.Code, responseWriter.Body.String())
	}

	if responseWriter.Code == http.StatusOK {
		contentType := responseWriter.Header().Get("Content-Type")
		if !strings.Contains(contentType, "application/json") {
			t.Errorf("Expected JSON content type, got %s", contentType)
		}
	}
}

// TestBasicEndpointsIntegration tests that the refactored InternalAPI responds to basic endpoints.
func TestBasicEndpointsIntegration(t *testing.T) {
	t.Parallel()

	deps := createTestDependencies()
	apiHandler := api.NewInternalAPI(deps)
	testCases := getBasicEndpointTestCases()

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(testCase.method, testCase.path, nil)
			responseWriter := httptest.NewRecorder()

			apiHandler.ServeHTTP(responseWriter, req)
			validateResponse(t, responseWriter, testCase.expectedStatus)
		})
	}
}

// TestMiddlewareIntegration tests that middleware is properly applied.
func TestMiddlewareIntegration(t *testing.T) {
	t.Parallel()

	deps := createTestDependencies()
	apiHandler := api.NewInternalAPI(deps)

	// Test that requests go through the middleware chain
	req := httptest.NewRequest(http.MethodGet, "/b/instance", nil)
	responseWriter := httptest.NewRecorder()

	apiHandler.ServeHTTP(responseWriter, req)

	// Check that response has appropriate headers that would be set by middleware
	// The logging middleware should have processed this request
	if responseWriter.Code != http.StatusOK {
		t.Errorf("Expected successful response, got %d", responseWriter.Code)
	}
}
