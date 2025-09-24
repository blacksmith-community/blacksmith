package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"blacksmith/internal/api"
	"blacksmith/internal/config"
	"blacksmith/internal/interfaces"
	"blacksmith/internal/services"
	pkgservices "blacksmith/pkg/services"
	"blacksmith/pkg/testutil"
	"blacksmith/pkg/vault"
)

// Test sentinel errors.
var (
	errMockCFClientNotAvailable = errors.New("mock CF client not available")
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

func (m *mockConfig) GetEnvironment() string {
	return "test"
}

func (m *mockConfig) GetBOSHConfig() config.BOSHConfig {
	return config.BOSHConfig{}
}

func (m *mockConfig) GetVaultConfig() config.VaultConfig {
	return config.VaultConfig{Address: "http://localhost:8200"}
}

func (m *mockConfig) GetBrokerConfig() config.BrokerConfig {
	return config.BrokerConfig{Username: "user", Port: "8080", BindIP: "127.0.0.1"}
}

func (m *mockConfig) GetCFConfig() config.CFBrokerConfig {
	return config.CFBrokerConfig{}
}

// mockBroker implements interfaces.Broker for testing.
type mockBroker struct{}

// IsBroker implements the interfaces.Broker interface.
func (m *mockBroker) IsBroker() bool {
	return true
}

func (m *mockBroker) GetPlans() map[string]services.Plan {
	return map[string]services.Plan{}
}

func (m *mockBroker) GetVault() interfaces.Vault {
	return nil
}

// mockCFManager implements interfaces.CFManager for testing.
type mockCFManager struct{}

// IsCFManager implements the interfaces.CFManager interface.
func (m *mockCFManager) IsCFManager() bool {
	return true
}

// GetClient implements the interfaces.CFManager interface.
func (m *mockCFManager) GetClient(endpointName string) (interface{}, error) {
	return nil, errMockCFClientNotAvailable
}

// GetStatus implements the interfaces.CFManager interface.
func (m *mockCFManager) GetStatus() map[string]interface{} {
	return map[string]interface{}{
		"enabled":           true,
		"total_endpoints":   1,
		"healthy_endpoints": 1,
		"endpoints": map[string]interface{}{
			"test": map[string]interface{}{
				"healthy":      true,
				"retry_count":  0,
				"last_healthy": time.Now(),
			},
		},
	}
}

// testVaultAdapter wraps the vault client to implement interfaces.Vault.
type testVaultAdapter struct {
	client *vault.Client
	server *testutil.VaultDevServer
}

func (v *testVaultAdapter) Get(ctx context.Context, path string, result interface{}) (bool, error) {
	return false, nil // Not needed for these tests
}

func (v *testVaultAdapter) Put(ctx context.Context, path string, data interface{}) error {
	return nil // Not needed for these tests
}

func (v *testVaultAdapter) Delete(ctx context.Context, path string) error {
	return nil // Not needed for these tests
}

func (v *testVaultAdapter) FindInstance(ctx context.Context, instanceID string) (*vault.Instance, bool, error) {
	return nil, false, nil // Not needed for these tests
}

func (v *testVaultAdapter) ListCFRegistrations(ctx context.Context) ([]map[string]interface{}, error) {
	return []map[string]interface{}{}, nil
}

func (v *testVaultAdapter) SaveCFRegistration(ctx context.Context, registration map[string]interface{}) error {
	return nil
}

func (v *testVaultAdapter) GetCFRegistration(ctx context.Context, registrationID string, out interface{}) (bool, error) {
	return false, nil
}

func (v *testVaultAdapter) DeleteCFRegistration(ctx context.Context, registrationID string) error {
	return nil
}

func (v *testVaultAdapter) UpdateCFRegistrationStatus(ctx context.Context, registrationID, status, errorMsg string) error {
	return nil
}

func (v *testVaultAdapter) SaveCFRegistrationProgress(ctx context.Context, registrationID string, progress map[string]interface{}) error {
	return nil
}

// createTestDependencies creates mock dependencies for testing.
func createTestDependencies(t *testing.T) api.Dependencies {
	t.Helper()

	vaultServer, err := testutil.NewVaultDevServer(t)
	if err != nil {
		t.Fatalf("failed to create vault dev server: %v", err)
	}

	vaultClient, err := vault.NewClient(vaultServer.Addr, vaultServer.RootToken, true)
	if err != nil {
		t.Fatalf("failed to create vault client: %v", err)
	}

	vaultAdapter := &testVaultAdapter{
		client: vaultClient,
		server: vaultServer,
	}

	return api.Dependencies{
		Config:             &mockConfig{},
		Logger:             &mockLogger{},
		Vault:              vaultAdapter,
		Broker:             &mockBroker{},
		ServicesManager:    &pkgservices.Manager{},
		CFManager:          &mockCFManager{},
		SecurityMiddleware: &pkgservices.SecurityMiddleware{},
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
			name:           "Service filter options endpoint",
			method:         "GET",
			path:           "/b/service-filter-options",
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

	deps := createTestDependencies(t)
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

	deps := createTestDependencies(t)
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

// TestServiceFilterOptionsFormat tests that the service filter options endpoint
// returns a properly formatted JSON response with string options.
func TestServiceFilterOptionsFormat(t *testing.T) {
	t.Parallel()

	deps := createTestDependencies(t)
	apiHandler := api.NewInternalAPI(deps)

	req := httptest.NewRequest(http.MethodGet, "/b/service-filter-options", nil)
	responseWriter := httptest.NewRecorder()

	apiHandler.ServeHTTP(responseWriter, req)

	if responseWriter.Code != http.StatusOK {
		t.Errorf("Expected HTTP 200, got %d", responseWriter.Code)

		return
	}

	response := parseJSONResponse(t, responseWriter.Body.Bytes())
	optionsSlice := validateOptionsField(t, response)
	validateOptionsAreStrings(t, optionsSlice)

	expectedOptions := []string{"blacksmith", "service-instances", "redis", "rabbitmq", "postgresql"}
	verifyExpectedOptions(t, optionsSlice, expectedOptions)
}

func parseJSONResponse(t *testing.T, data []byte) map[string]interface{} {
	t.Helper()

	var response map[string]interface{}

	err := json.Unmarshal(data, &response)
	if err != nil {
		t.Fatalf("Failed to parse JSON response: %v", err)
	}

	return response
}

func validateOptionsField(t *testing.T, response map[string]interface{}) []interface{} {
	t.Helper()

	options, exists := response["options"]
	if !exists {
		t.Fatal("Response missing 'options' field")
	}

	optionsSlice, ok := options.([]interface{})
	if !ok {
		t.Fatal("'options' field is not an array")
	}

	return optionsSlice
}

func validateOptionsAreStrings(t *testing.T, optionsSlice []interface{}) {
	t.Helper()

	for i, option := range optionsSlice {
		if _, ok := option.(string); !ok {
			t.Errorf("Option at index %d is not a string: %v", i, option)
		}
	}
}

func verifyExpectedOptions(t *testing.T, optionsSlice []interface{}, expectedOptions []string) {
	t.Helper()

	if len(optionsSlice) != len(expectedOptions) {
		t.Fatalf("Expected %d options, got %d", len(expectedOptions), len(optionsSlice))
	}

	for index, expected := range expectedOptions {
		if index >= len(optionsSlice) {
			t.Errorf("Missing expected option: %s", expected)

			continue
		}

		optionStr, ok := optionsSlice[index].(string)
		if !ok {
			t.Errorf("Expected option at index %d to be a string, got %T", index, optionsSlice[index])

			continue
		}

		if optionStr != expected {
			t.Errorf("Expected option '%s' at index %d, got '%s'", expected, index, optionStr)
		}
	}
}
