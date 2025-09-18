package certificates_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"blacksmith/internal/handlers/certificates"
	pkgCertificates "blacksmith/pkg/certificates"
	"blacksmith/pkg/logger"
)

func NewTestLogger(component, _ string) logger.Logger {
	return logger.Get().Named(component)
}

// testConfig implements the Config interface for testing.
type testConfig struct{}

func (c *testConfig) GetBrokerTLSEnabled() bool       { return false }
func (c *testConfig) GetBrokerTLSCertificate() string { return "" }
func (c *testConfig) GetVaultCACert() string          { return "" }
func (c *testConfig) GetBOSHCACert() string           { return "" }

type certificateRequestTestCase struct {
	name           string
	path           string
	method         string
	body           string
	expectedStatus int
	checkResponse  bool
}

func getCertificateRequestTestCases() []certificateRequestTestCase {
	return []certificateRequestTestCase{
		{
			name:           "GET trusted certificates",
			path:           "/b/internal/certificates/trusted",
			method:         "GET",
			expectedStatus: 200,
			checkResponse:  true,
		},
		{
			name:           "GET blacksmith certificates",
			path:           "/b/internal/certificates/blacksmith",
			method:         "GET",
			expectedStatus: 200,
			checkResponse:  true,
		},
		{
			name:           "POST endpoint certificate",
			path:           "/b/internal/certificates/endpoint",
			method:         "POST",
			body:           `{"endpoint":"google.com:443","timeout":10}`,
			expectedStatus: 200,
			checkResponse:  true,
		},
		{
			name:           "POST parse certificate",
			path:           "/b/internal/certificates/parse",
			method:         "POST",
			body:           `{"certificate":"-----BEGIN CERTIFICATE-----\nMIIDATCCAemgAwIBAgIRAKSHXfU2doalRidzWA24AOowDQYJKoZIhvcNAQELBQAw\n-----END CERTIFICATE-----"}`,
			expectedStatus: 400, // Expected to fail with invalid cert
			checkResponse:  false,
		},
		{
			name:           "GET service certificates",
			path:           "/b/internal/certificates/services/test-instance-id",
			method:         "GET",
			expectedStatus: 200, // Returns empty list for non-existent service
			checkResponse:  true,
		},
		{
			name:           "Invalid path",
			path:           "/b/internal/certificates/invalid",
			method:         "GET",
			expectedStatus: 404,
			checkResponse:  false,
		},
	}
}

func runCertificateRequestTest(t *testing.T, api *certificates.Handler, test certificateRequestTestCase) {
	t.Helper()

	var body io.Reader
	if test.body != "" {
		body = strings.NewReader(test.body)
	}

	req := httptest.NewRequest(test.method, test.path, body)
	if test.body != "" {
		req.Header.Set("Content-Type", "application/json")
	}

	responseRecorder := httptest.NewRecorder()
	api.HandleCertificatesRequest(responseRecorder, req)

	validateCertificateResponse(t, test, responseRecorder)
}

func validateCertificateResponse(t *testing.T, test certificateRequestTestCase, responseRecorder *httptest.ResponseRecorder) {
	t.Helper()
	// Endpoint lookup may fail without network; accept 200 or 400 for endpoint tests
	if test.path == "/b/internal/certificates/endpoint" {
		if responseRecorder.Code != 200 && responseRecorder.Code != 400 {
			t.Errorf("Expected status 200 or 400 for endpoint request, got %d", responseRecorder.Code)
		}
	} else if responseRecorder.Code != test.expectedStatus {
		t.Errorf("Expected status %d, got %d", test.expectedStatus, responseRecorder.Code)
	}

	if test.checkResponse {
		validateJSONResponse(t, responseRecorder)
	}
}

func validateJSONResponse(t *testing.T, responseRecorder *httptest.ResponseRecorder) {
	t.Helper()

	var response map[string]interface{}

	err := json.Unmarshal(responseRecorder.Body.Bytes(), &response)
	if err != nil {
		t.Errorf("Response is not valid JSON: %v", err)
	}

	if _, ok := response["success"]; !ok {
		t.Errorf("Response missing 'success' field")
	}
}

func TestCertificateAPI_HandleCertificatesRequest(t *testing.T) {
	t.Parallel()

	testLogger := NewTestLogger("test", "INFO")
	_ = logger.InitFromEnv()
	api := certificates.NewHandler(&testConfig{}, testLogger, nil)

	tests := getCertificateRequestTestCases()

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			runCertificateRequestTest(t, api, test)
		})
	}
}

func TestCertificateAPI_HandleTrustedCertificates(t *testing.T) {
	t.Parallel()

	testLogger := NewTestLogger("test", "INFO")

	// Initialize the global logger for the API to use
	_ = logger.InitFromEnv()

	api := certificates.NewHandler(&testConfig{}, testLogger, nil)

	req := httptest.NewRequest(http.MethodGet, "/b/internal/certificates/trusted", nil)
	recorder := httptest.NewRecorder()

	api.HandleTrustedCertificates(recorder, req)

	// Should return 200 even if no certificates found
	if recorder.Code != 200 {
		t.Errorf("Expected status 200, got %d", recorder.Code)
	}

	// Check response structure
	var response pkgCertificates.CertificateResponse

	err := json.Unmarshal(recorder.Body.Bytes(), &response)
	if err != nil {
		t.Errorf("Failed to parse response: %v", err)
	}

	if !response.Success {
		t.Logf("Response indicates failure (expected in test environment): %s", response.Error)
	}

	// Check that data structure is correct
	if response.Data.Metadata.Source == "" {
		t.Errorf("Expected non-empty metadata source")
	}
}

func TestCertificateAPI_HandleTrustedCertificateFile(t *testing.T) {
	t.Parallel()

	testLogger := NewTestLogger("test", "INFO")
	_ = logger.InitFromEnv()
	api := certificates.NewHandler(&testConfig{}, testLogger, nil)

	tests := getTrustedCertFileTestCases()

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			runTrustedCertFileTest(t, api, testCase)
		})
	}
}

type trustedCertFileTestCase struct {
	name           string
	method         string
	body           string
	expectedStatus int
	expectedError  string
}

func getTrustedCertFileTestCases() []trustedCertFileTestCase {
	return []trustedCertFileTestCase{
		{
			name:           "Valid request with non-existent file",
			method:         "POST",
			body:           `{"filePath": "/etc/ssl/certs/bosh-trusted-cert-test.pem"}`,
			expectedStatus: 400,
			expectedError:  "failed to load certificate",
		},
		{
			name:           "Invalid method",
			method:         "GET",
			body:           "",
			expectedStatus: 405,
			expectedError:  "method not allowed",
		},
		{
			name:           "Invalid JSON",
			method:         "POST",
			body:           `{invalid json}`,
			expectedStatus: 400,
			expectedError:  "invalid request body",
		},
		{
			name:           "Missing filePath",
			method:         "POST",
			body:           `{}`,
			expectedStatus: 400,
			expectedError:  "filePath is required",
		},
		{
			name:           "Invalid file path - not BOSH trusted cert",
			method:         "POST",
			body:           `{"filePath": "/etc/ssl/certs/ca-certificates.crt"}`,
			expectedStatus: 400,
			expectedError:  "invalid file path: must be a BOSH trusted certificate",
		},
		{
			name:           "Path traversal attempt",
			method:         "POST",
			body:           `{"filePath": "/etc/ssl/certs/../../../etc/passwd"}`,
			expectedStatus: 400,
			expectedError:  "invalid file path: must be a BOSH trusted certificate",
		},
	}
}

func runTrustedCertFileTest(t *testing.T, api *certificates.Handler, testCase trustedCertFileTestCase) {
	t.Helper()

	var body io.Reader
	if testCase.body != "" {
		body = strings.NewReader(testCase.body)
	}

	req := httptest.NewRequest(testCase.method, "/b/internal/certificates/trusted/file", body)
	if testCase.method == "POST" {
		req.Header.Set("Content-Type", "application/json")
	}

	recorder := httptest.NewRecorder()
	api.HandleTrustedCertificateFile(recorder, req)

	if recorder.Code != testCase.expectedStatus {
		t.Errorf("Expected status %d, got %d", testCase.expectedStatus, recorder.Code)
	}

	if testCase.expectedError != "" {
		validateTrustedCertFileError(t, recorder, testCase.expectedError)
	}
}

func validateTrustedCertFileError(t *testing.T, recorder *httptest.ResponseRecorder, expectedError string) {
	t.Helper()

	var response pkgCertificates.CertificateResponse

	err := json.Unmarshal(recorder.Body.Bytes(), &response)
	if err != nil {
		t.Fatalf("Failed to unmarshal response: %v", err)
	}

	if !response.Success && !strings.Contains(response.Error, expectedError) {
		t.Errorf("Expected error containing '%s', got '%s'", expectedError, response.Error)
	}
}

func TestCertificateAPI_HandleBlacksmithCertificates(t *testing.T) {
	t.Parallel()

	// config := Config{
	//	BOSH: BOSHConfig{
	//		Address:  "https://bosh.example.com:25555",
	//		Username: "admin",
	//		CACert:   "-----BEGIN CERTIFICATE-----\ntest\n-----END CERTIFICATE-----",
	//	},
	//	Vault: VaultConfig{
	//		Address: "https://vault.example.com:8200",
	//		CACert:  "-----BEGIN CERTIFICATE-----\ntest\n-----END CERTIFICATE-----",
	//	},
	// }
	testLogger := NewTestLogger("test", "INFO")

	// Initialize the global logger for the API to use
	_ = logger.InitFromEnv()

	api := certificates.NewHandler(&testConfig{}, testLogger, nil)

	req := httptest.NewRequest(http.MethodGet, "/b/internal/certificates/blacksmith", nil)
	recorder := httptest.NewRecorder()

	api.HandleBlacksmithCertificates(recorder, req)

	if recorder.Code != 200 {
		t.Errorf("Expected status 200, got %d", recorder.Code)
	}

	var response pkgCertificates.CertificateResponse

	err := json.Unmarshal(recorder.Body.Bytes(), &response)
	if err != nil {
		t.Errorf("Failed to parse response: %v", err)
	}

	if !response.Success {
		t.Logf("Response indicates failure (expected in test environment): %s", response.Error)
	}
}

func TestCertificateAPI_HandleEndpointCertificates(t *testing.T) {
	t.Parallel()

	testLogger := NewTestLogger("test", "INFO")
	_ = logger.InitFromEnv()
	api := certificates.NewHandler(&testConfig{}, testLogger, nil)

	tests := getEndpointCertTestCases()

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			runEndpointCertTest(t, api, test)
		})
	}
}

type endpointCertTestCase struct {
	name           string
	requestBody    string
	expectedStatus int
}

func getEndpointCertTestCases() []endpointCertTestCase {
	return []endpointCertTestCase{
		{
			name:           "Valid request",
			requestBody:    `{"endpoint":"google.com:443","timeout":10}`,
			expectedStatus: 200, // Accept 200 or 400 depending on environment
		},
		{
			name:           "Invalid JSON",
			requestBody:    `invalid json`,
			expectedStatus: 400,
		},
		{
			name:           "Missing endpoint",
			requestBody:    `{"timeout":10}`,
			expectedStatus: 400,
		},
		{
			name:           "Empty request",
			requestBody:    `{}`,
			expectedStatus: 400,
		},
	}
}

func runEndpointCertTest(t *testing.T, api *certificates.Handler, test endpointCertTestCase) {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, "/b/internal/certificates/endpoint",
		strings.NewReader(test.requestBody))
	req.Header.Set("Content-Type", "application/json")

	recorder := httptest.NewRecorder()
	api.HandleEndpointCertificates(recorder, req)

	validateEndpointCertResponse(t, test, recorder)
}

func validateEndpointCertResponse(t *testing.T, test endpointCertTestCase, recorder *httptest.ResponseRecorder) {
	t.Helper()
	// Accept 200 or 400 for live endpoint fetch depending on environment
	if strings.Contains(test.requestBody, "google.com:443") {
		if recorder.Code != 200 && recorder.Code != 400 {
			t.Errorf("Expected status 200 or 400, got %d. Response: %s", recorder.Code, recorder.Body.String())
		}
	} else if recorder.Code != test.expectedStatus {
		t.Errorf("Expected status %d, got %d. Response: %s",
			test.expectedStatus, recorder.Code, recorder.Body.String())
	}

	// Check response is valid JSON for all cases
	var response map[string]interface{}

	err := json.Unmarshal(recorder.Body.Bytes(), &response)
	if err != nil {
		t.Errorf("Response is not valid JSON: %v", err)
	}
}

func TestCertificateAPI_HandleParseCertificate(t *testing.T) {
	t.Parallel()

	testLogger := NewTestLogger("test", "INFO")
	_ = logger.InitFromEnv()
	api := certificates.NewHandler(&testConfig{}, testLogger, nil)

	tests := getParseCertTestCases()

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			runParseCertTest(t, api, test)
		})
	}
}

type parseCertTestCase struct {
	name           string
	requestBody    string
	expectedStatus int
	shouldSucceed  bool
}

func getParseCertTestCases() []parseCertTestCase {
	return []parseCertTestCase{
		{
			name:           "Invalid certificate PEM",
			requestBody:    `{"certificate":"invalid pem data"}`,
			expectedStatus: 400, // API returns 400 for invalid certificate
			shouldSucceed:  false,
		},
		{
			name:           "Empty certificate",
			requestBody:    `{"certificate":""}`,
			expectedStatus: 400,
			shouldSucceed:  false,
		},
		{
			name:           "Invalid JSON",
			requestBody:    `invalid json`,
			expectedStatus: 400,
			shouldSucceed:  false,
		},
	}
}

func runParseCertTest(t *testing.T, api *certificates.Handler, test parseCertTestCase) {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, "/b/internal/certificates/parse",
		strings.NewReader(test.requestBody))
	req.Header.Set("Content-Type", "application/json")

	parseCertRecorder := httptest.NewRecorder()
	api.HandleParseCertificate(parseCertRecorder, req)

	if parseCertRecorder.Code != test.expectedStatus {
		t.Errorf("Expected status %d, got %d", test.expectedStatus, parseCertRecorder.Code)
	}

	if parseCertRecorder.Code == 200 {
		validateParseCertSuccess(t, parseCertRecorder, test.shouldSucceed)
	}
}

func validateParseCertSuccess(t *testing.T, recorder *httptest.ResponseRecorder, expectedSuccess bool) {
	t.Helper()

	var response pkgCertificates.CertificateResponse

	err := json.Unmarshal(recorder.Body.Bytes(), &response)
	if err != nil {
		t.Errorf("Failed to parse response: %v", err)
	}

	if response.Success != expectedSuccess {
		t.Errorf("Expected success %v, got %v", expectedSuccess, response.Success)
	}
}

func TestCertificateAPI_HandleServiceCertificates(t *testing.T) {
	t.Parallel()

	testLogger := NewTestLogger("test", "INFO")

	// Initialize the global logger for the API to use
	_ = logger.InitFromEnv()

	api := certificates.NewHandler(&testConfig{}, testLogger, nil)

	// Test with non-existent service
	req := httptest.NewRequest(http.MethodGet, "/b/internal/certificates/services/test-instance", nil)
	recorder := httptest.NewRecorder()

	api.HandleCertificatesRequest(recorder, req)

	// Should return 200 with empty list for non-existent service
	if recorder.Code != 200 {
		t.Errorf("Expected status 200 for non-existent service, got %d", recorder.Code)
	}

	var response pkgCertificates.CertificateResponse

	err := json.Unmarshal(recorder.Body.Bytes(), &response)
	if err != nil {
		t.Errorf("Failed to parse response: %v", err)
	}

	if !response.Success {
		t.Errorf("Expected success response with empty list for non-existent service")
	}

	if len(response.Data.Certificates) != 0 {
		t.Errorf("Expected empty certificate list for non-existent service, got %d certificates", len(response.Data.Certificates))
	}
}

// Integration test for API routing.
func TestCertificateAPI_Routing(t *testing.T) {
	t.Parallel()

	testLogger := NewTestLogger("test", "INFO")

	// Initialize the global logger for the API to use
	_ = logger.InitFromEnv()

	api := certificates.NewHandler(&testConfig{}, testLogger, nil)

	testCases := []struct {
		path   string
		method string
		status int
	}{
		{"/b/internal/certificates/trusted", "GET", 200},
		{"/b/internal/certificates/trusted/file", "POST", 400}, // Missing body
		{"/b/internal/certificates/blacksmith", "GET", 200},
		{"/b/internal/certificates/endpoint", "POST", 400},     // Missing body
		{"/b/internal/certificates/parse", "POST", 400},        // Missing body
		{"/b/internal/certificates/services/test", "GET", 200}, // Non-existent service returns empty list
		{"/b/internal/certificates/invalid", "GET", 404},       // Invalid path
	}

	for _, testCase := range testCases {
		t.Run(testCase.path+" "+testCase.method, func(t *testing.T) {
			t.Parallel()

			var body io.Reader
			if testCase.method == "POST" {
				body = bytes.NewReader([]byte{}) // Empty body to trigger 400
			}

			req := httptest.NewRequest(testCase.method, testCase.path, body)
			responseRecorder := httptest.NewRecorder()

			api.HandleCertificatesRequest(responseRecorder, req)

			if responseRecorder.Code != testCase.status {
				t.Errorf("Expected status %d, got %d. Response: %s",
					testCase.status, responseRecorder.Code, responseRecorder.Body.String())
			}
		})
	}
}

func TestCertificateAPI_ContentType(t *testing.T) {
	t.Parallel()

	testLogger := NewTestLogger("test", "INFO")

	// Initialize the global logger for the API to use
	_ = logger.InitFromEnv()

	api := certificates.NewHandler(&testConfig{}, testLogger, nil)

	req := httptest.NewRequest(http.MethodGet, "/b/internal/certificates/trusted", nil)
	recorder := httptest.NewRecorder()

	api.HandleCertificatesRequest(recorder, req)

	contentType := recorder.Header().Get("Content-Type")
	if contentType != "application/json" {
		t.Errorf("Expected Content-Type application/json, got %s", contentType)
	}
}

func TestCertificateAPI_MethodValidation(t *testing.T) {
	t.Parallel()

	testLogger := NewTestLogger("test", "INFO")

	// Initialize the global logger for the API to use
	_ = logger.InitFromEnv()

	api := certificates.NewHandler(&testConfig{}, testLogger, nil)

	// Test that GET endpoints reject POST
	req := httptest.NewRequest(http.MethodPost, "/b/internal/certificates/trusted", nil)
	recorder := httptest.NewRecorder()

	api.HandleCertificatesRequest(recorder, req)

	if recorder.Code != 405 {
		t.Errorf("Expected status 405 for wrong method, got %d", recorder.Code)
	}

	// Test that POST endpoints reject GET
	req2 := httptest.NewRequest(http.MethodGet, "/b/internal/certificates/endpoint", nil)
	w2 := httptest.NewRecorder()

	api.HandleCertificatesRequest(w2, req2)

	if w2.Code != 405 {
		t.Errorf("Expected status 405 for wrong method, got %d", w2.Code)
	}
}

// Test error handling and edge cases.
func TestCertificateAPI_ErrorHandling(t *testing.T) {
	t.Parallel()

	testLogger := NewTestLogger("test", "INFO")

	// Initialize the global logger for the API to use
	_ = logger.InitFromEnv()

	api := certificates.NewHandler(&testConfig{}, testLogger, nil)

	// Test with malformed JSON
	req := httptest.NewRequest(http.MethodPost, "/b/internal/certificates/parse",
		strings.NewReader(`{"pem": incomplete json`))
	req.Header.Set("Content-Type", "application/json")

	responseWriter := httptest.NewRecorder()
	api.HandleCertificatesRequest(responseWriter, req)

	if responseWriter.Code != 400 {
		t.Errorf("Expected status 400 for malformed JSON, got %d", responseWriter.Code)
	}

	var response pkgCertificates.CertificateResponse

	err := json.Unmarshal(responseWriter.Body.Bytes(), &response)
	if err != nil {
		t.Errorf("Failed to parse error response: %v", err)
	}

	if response.Success {
		t.Errorf("Expected failed response for malformed JSON")
	}
}

// Benchmark API performance.
func BenchmarkCertificateAPI_TrustedCertificates(b *testing.B) {
	testLogger := NewTestLogger("test", "INFO")

	// Initialize the global logger for the API to use
	_ = logger.InitFromEnv()

	api := certificates.NewHandler(&testConfig{}, testLogger, nil)

	req := httptest.NewRequest(http.MethodGet, "/b/internal/certificates/trusted", nil)

	b.ResetTimer()

	for range b.N {
		recorder := httptest.NewRecorder()
		api.HandleCertificatesRequest(recorder, req)
	}
}

func BenchmarkCertificateAPI_BlacksmithCertificates(b *testing.B) {
	testLogger := NewTestLogger("test", "INFO")

	// Initialize the global logger for the API to use
	_ = logger.InitFromEnv()

	api := certificates.NewHandler(&testConfig{}, testLogger, nil)

	req := httptest.NewRequest(http.MethodGet, "/b/internal/certificates/blacksmith", nil)

	b.ResetTimer()

	for range b.N {
		recorder := httptest.NewRecorder()
		api.HandleCertificatesRequest(recorder, req)
	}
}

// Test response structure validation.
func TestCertificateResponse_Structure(t *testing.T) {
	t.Parallel()

	response := pkgCertificates.CertificateResponse{
		Success: true,
		Data: pkgCertificates.CertificateResponseData{
			Certificates: []pkgCertificates.CertificateListItem{
				{
					Name: "test.crt",
					Path: "/etc/ssl/certs/test.crt",
					Details: pkgCertificates.CertificateInfo{
						Version:      3,
						SerialNumber: "123456789",
						Subject: pkgCertificates.CertificateSubject{
							CommonName: "test.example.com",
						},
						Issuer: pkgCertificates.CertificateSubject{
							CommonName: "Test CA",
						},
						KeyUsage:    []string{"digitalSignature"},
						ExtKeyUsage: []string{"serverAuth"},
					},
				},
			},
			Metadata: pkgCertificates.CertificateMetadata{
				Source: "trusted",
				Count:  1,
			},
		},
	}

	// Test JSON marshaling
	jsonData, err := json.Marshal(response)
	if err != nil {
		t.Errorf("Failed to marshal response: %v", err)
	}

	// Test JSON unmarshaling
	var unmarshaled pkgCertificates.CertificateResponse

	err = json.Unmarshal(jsonData, &unmarshaled)
	if err != nil {
		t.Errorf("Failed to unmarshal response: %v", err)
	}

	// Verify structure preservation
	if unmarshaled.Success != response.Success {
		t.Errorf("Success field not preserved")
	}

	if len(unmarshaled.Data.Certificates) != len(response.Data.Certificates) {
		t.Errorf("Certificates count not preserved")
	}

	if unmarshaled.Data.Metadata.Source != response.Data.Metadata.Source {
		t.Errorf("Metadata source not preserved")
	}
}
