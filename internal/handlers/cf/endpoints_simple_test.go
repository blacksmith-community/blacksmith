package cf_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"blacksmith/internal/handlers/cf"
	"blacksmith/pkg/logger"
)

// TestEndpointsCompile verifies that the endpoint handlers compile and handle nil CFManager correctly.
func TestEndpointsCompile(t *testing.T) {
	t.Parallel()

	log := logger.Get()
	handler := cf.NewHandler(log, nil, nil, nil)

	tests := createEndpointCompileTests(handler)

	for _, testCase := range tests {
		runEndpointCompileTest(t, testCase)
	}
}

type endpointCompileTest struct {
	name           string
	testFunc       func(w http.ResponseWriter, r *http.Request)
	expectedStatus int
	expectedError  string
}

func createEndpointCompileTests(handler *cf.Handler) []endpointCompileTest {
	return []endpointCompileTest{
		{
			name: "GetMarketplace with nil CFManager",
			testFunc: func(w http.ResponseWriter, r *http.Request) {
				handler.GetMarketplace(w, r, "test-endpoint")
			},
			expectedStatus: http.StatusServiceUnavailable,
			expectedError:  "CF functionality disabled - no CF endpoints configured",
		},
		{
			name: "GetOrganizations with nil CFManager",
			testFunc: func(w http.ResponseWriter, r *http.Request) {
				handler.GetOrganizations(w, r, "test-endpoint")
			},
			expectedStatus: http.StatusServiceUnavailable,
			expectedError:  "CF functionality disabled - no CF endpoints configured",
		},
		{
			name: "GetSpaces with nil CFManager",
			testFunc: func(w http.ResponseWriter, r *http.Request) {
				handler.GetSpaces(w, r, "test-endpoint", "org-guid")
			},
			expectedStatus: http.StatusServiceUnavailable,
			expectedError:  "CF functionality disabled - no CF endpoints configured",
		},
		{
			name: "GetServices with nil CFManager",
			testFunc: func(w http.ResponseWriter, r *http.Request) {
				handler.GetServices(w, r, "test-endpoint", "org-guid", "space-guid")
			},
			expectedStatus: http.StatusServiceUnavailable,
			expectedError:  "CF functionality disabled - no CF endpoints configured",
		},
		{
			name: "GetServiceBindings with nil CFManager",
			testFunc: func(w http.ResponseWriter, r *http.Request) {
				handler.GetServiceBindings(w, r, "test-endpoint", "org-guid", "space-guid", "service-guid")
			},
			expectedStatus: http.StatusServiceUnavailable,
			expectedError:  "CF functionality disabled - no CF endpoints configured",
		},
	}
}

func runEndpointCompileTest(t *testing.T, testCase endpointCompileTest) {
	t.Helper()

	t.Run(testCase.name, func(t *testing.T) {
		t.Parallel()

		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "/test", nil)
		if err != nil {
			t.Fatalf("Failed to create request: %v", err)
		}

		recorder := httptest.NewRecorder()
		testCase.testFunc(recorder, req)

		validateEndpointCompileResponse(t, recorder, testCase)
	})
}

func validateEndpointCompileResponse(t *testing.T, recorder *httptest.ResponseRecorder, testCase endpointCompileTest) {
	t.Helper()

	if recorder.Code != testCase.expectedStatus {
		t.Errorf("Expected status %d, got %d", testCase.expectedStatus, recorder.Code)
	}

	var body map[string]interface{}

	err := json.NewDecoder(recorder.Body).Decode(&body)
	if err != nil {
		t.Fatalf("Failed to parse JSON response: %v", err)
	}

	if body["error"] != testCase.expectedError {
		t.Errorf("Expected error '%s', got '%v'", testCase.expectedError, body["error"])
	}
}

// TestEndpointRouting verifies that the routing logic correctly identifies and routes CF endpoint requests.
func TestEndpointRouting(t *testing.T) {
	t.Parallel()

	log := logger.Get()
	handler := cf.NewHandler(log, nil, nil, nil)

	tests := createEndpointRoutingTests()

	for _, testCase := range tests {
		runEndpointRoutingTest(t, handler, testCase)
	}
}

type endpointRoutingTest struct {
	name        string
	url         string
	shouldMatch bool
}

func createEndpointRoutingTests() []endpointRoutingTest {
	return []endpointRoutingTest{
		{
			name:        "Marketplace endpoint",
			url:         "/b/cf/endpoints/test/marketplace",
			shouldMatch: true,
		},
		{
			name:        "Organizations endpoint",
			url:         "/b/cf/endpoints/test/orgs",
			shouldMatch: true,
		},
		{
			name:        "Spaces endpoint",
			url:         "/b/cf/endpoints/test/orgs/org-123/spaces",
			shouldMatch: true,
		},
		{
			name:        "Services endpoint",
			url:         "/b/cf/endpoints/test/orgs/org-123/spaces/space-456/services",
			shouldMatch: true,
		},
		{
			name:        "Service bindings endpoint",
			url:         "/b/cf/endpoints/test/orgs/org-123/spaces/space-456/service_instances/si-789/bindings",
			shouldMatch: true,
		},
		{
			name:        "Invalid endpoint",
			url:         "/b/cf/invalid",
			shouldMatch: false,
		},
	}
}

func runEndpointRoutingTest(t *testing.T, handler *cf.Handler, testCase endpointRoutingTest) {
	t.Helper()

	t.Run(testCase.name, func(t *testing.T) {
		t.Parallel()

		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, testCase.url, nil)
		if err != nil {
			t.Fatalf("Failed to create request: %v", err)
		}

		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)

		validateEndpointRoutingResponse(t, recorder, testCase)
	})
}

func validateEndpointRoutingResponse(t *testing.T, recorder *httptest.ResponseRecorder, testCase endpointRoutingTest) {
	t.Helper()

	if testCase.shouldMatch {
		// Should get 503 (service unavailable) because CFManager is nil
		if recorder.Code != http.StatusServiceUnavailable {
			t.Errorf("Expected status 503 for matched route, got %d", recorder.Code)
		}
	} else {
		// Should get 404 for unmatched routes
		if recorder.Code != http.StatusNotFound {
			t.Errorf("Expected status 404 for unmatched route, got %d", recorder.Code)
		}
	}
}
