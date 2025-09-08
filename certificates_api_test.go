package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"blacksmith/pkg/logger"
)

func NewTestLogger(component, _ string) logger.Logger {
	return logger.Get().Named(component)
}

func TestCertificateAPI_HandleCertificatesRequest(t *testing.T) {
	t.Parallel()
	// Create a test config and logger
	config := Config{} // Minimal config for testing
	testLogger := NewTestLogger("test", "INFO")

	// Initialize the global logger for the API to use
	_ = logger.InitFromEnv()

	api := NewCertificateAPI(config, testLogger, nil)

	tests := []struct {
		name           string
		path           string
		method         string
		body           string
		expectedStatus int
		checkResponse  bool
	}{
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

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

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

			// Endpoint lookup may fail without network; accept 200 or 400 for endpoint tests
			if test.path == "/b/internal/certificates/endpoint" {
				if responseRecorder.Code != 200 && responseRecorder.Code != 400 {
					t.Errorf("Expected status 200 or 400 for endpoint request, got %d", responseRecorder.Code)
				}
			} else if responseRecorder.Code != test.expectedStatus {
				t.Errorf("Expected status %d, got %d", test.expectedStatus, responseRecorder.Code)
			}

			if test.checkResponse {
				// Check that response is valid JSON
				var response map[string]interface{}

				err := json.Unmarshal(responseRecorder.Body.Bytes(), &response)
				if err != nil {
					t.Errorf("Response is not valid JSON: %v", err)
				}

				// Check that response has expected structure
				if _, ok := response["success"]; !ok {
					t.Errorf("Response missing 'success' field")
				}
			}
		})
	}
}

func TestCertificateAPI_HandleTrustedCertificates(t *testing.T) {
	t.Parallel()

	config := Config{}
	testLogger := NewTestLogger("test", "INFO")

	// Initialize the global logger for the API to use
	_ = logger.InitFromEnv()

	api := NewCertificateAPI(config, testLogger, nil)

	req := httptest.NewRequest(http.MethodGet, "/b/internal/certificates/trusted", nil)
	w := httptest.NewRecorder()

	api.handleTrustedCertificates(w, req)

	// Should return 200 even if no certificates found
	if w.Code != 200 {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	// Check response structure
	var response CertificateResponse

	err := json.Unmarshal(w.Body.Bytes(), &response)
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

	config := Config{}
	testLogger := NewTestLogger("test", "INFO")

	// Initialize the global logger for the API to use
	_ = logger.InitFromEnv()

	api := NewCertificateAPI(config, testLogger, nil)

	tests := []struct {
		name           string
		method         string
		body           string
		expectedStatus int
		expectedError  string
	}{
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

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			var body io.Reader
			if testCase.body != "" {
				body = strings.NewReader(testCase.body)
			}

			req := httptest.NewRequest(testCase.method, "/b/internal/certificates/trusted/file", body)
			if testCase.method == "POST" {
				req.Header.Set("Content-Type", "application/json")
			}

			recorder := httptest.NewRecorder()
			api.handleTrustedCertificateFile(recorder, req)

			if recorder.Code != testCase.expectedStatus {
				t.Errorf("Expected status %d, got %d", testCase.expectedStatus, recorder.Code)
			}

			if testCase.expectedError != "" {
				var response CertificateResponse

				err := json.Unmarshal(recorder.Body.Bytes(), &response)
				if err != nil {
					t.Fatalf("Failed to unmarshal response: %v", err)
				}

				if !response.Success && !strings.Contains(response.Error, testCase.expectedError) {
					t.Errorf("Expected error containing '%s', got '%s'", testCase.expectedError, response.Error)
				}
			}
		})
	}
}

func TestCertificateAPI_HandleBlacksmithCertificates(t *testing.T) {
	t.Parallel()

	config := Config{
		BOSH: BOSHConfig{
			Address:  "https://bosh.example.com:25555",
			Username: "admin",
			CACert:   "-----BEGIN CERTIFICATE-----\ntest\n-----END CERTIFICATE-----",
		},
		Vault: VaultConfig{
			Address: "https://vault.example.com:8200",
			CACert:  "-----BEGIN CERTIFICATE-----\ntest\n-----END CERTIFICATE-----",
		},
	}
	testLogger := NewTestLogger("test", "INFO")

	// Initialize the global logger for the API to use
	_ = logger.InitFromEnv()

	api := NewCertificateAPI(config, testLogger, nil)

	req := httptest.NewRequest(http.MethodGet, "/b/internal/certificates/blacksmith", nil)
	w := httptest.NewRecorder()

	api.handleBlacksmithCertificates(w, req)

	if w.Code != 200 {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	var response CertificateResponse

	err := json.Unmarshal(w.Body.Bytes(), &response)
	if err != nil {
		t.Errorf("Failed to parse response: %v", err)
	}

	if !response.Success {
		t.Logf("Response indicates failure (expected in test environment): %s", response.Error)
	}
}

func TestCertificateAPI_HandleEndpointCertificates(t *testing.T) {
	t.Parallel()

	config := Config{}
	testLogger := NewTestLogger("test", "INFO")

	// Initialize the global logger for the API to use
	_ = logger.InitFromEnv()

	api := NewCertificateAPI(config, testLogger, nil)

	tests := []struct {
		name           string
		requestBody    string
		expectedStatus int
	}{
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

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(http.MethodPost, "/b/internal/certificates/endpoint",
				strings.NewReader(test.requestBody))
			req.Header.Set("Content-Type", "application/json")

			recorder := httptest.NewRecorder()
			api.handleEndpointCertificates(recorder, req)

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
		})
	}
}

func TestCertificateAPI_HandleParseCertificate(t *testing.T) {
	t.Parallel()

	config := Config{}
	testLogger := NewTestLogger("test", "INFO")

	// Initialize the global logger for the API to use
	_ = logger.InitFromEnv()

	api := NewCertificateAPI(config, testLogger, nil)

	tests := []struct {
		name           string
		requestBody    string
		expectedStatus int
		shouldSucceed  bool
	}{
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

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(http.MethodPost, "/b/internal/certificates/parse",
				strings.NewReader(test.requestBody))
			req.Header.Set("Content-Type", "application/json")

			parseCertRecorder := httptest.NewRecorder()
			api.handleParseCertificate(parseCertRecorder, req)

			if parseCertRecorder.Code != test.expectedStatus {
				t.Errorf("Expected status %d, got %d", test.expectedStatus, parseCertRecorder.Code)
			}

			var response CertificateResponse
			if parseCertRecorder.Code == 200 {
				err := json.Unmarshal(parseCertRecorder.Body.Bytes(), &response)
				if err != nil {
					t.Errorf("Failed to parse response: %v", err)
				}

				if response.Success != test.shouldSucceed {
					t.Errorf("Expected success %v, got %v", test.shouldSucceed, response.Success)
				}
			}
		})
	}
}

func TestCertificateAPI_HandleServiceCertificates(t *testing.T) {
	t.Parallel()

	config := Config{}
	testLogger := NewTestLogger("test", "INFO")

	// Initialize the global logger for the API to use
	_ = logger.InitFromEnv()

	api := NewCertificateAPI(config, testLogger, nil)

	// Test with non-existent service
	req := httptest.NewRequest(http.MethodGet, "/b/internal/certificates/services/test-instance", nil)
	recorder := httptest.NewRecorder()

	api.handleServiceCertificates(recorder, req)

	// Should return 200 with empty list for non-existent service
	if recorder.Code != 200 {
		t.Errorf("Expected status 200 for non-existent service, got %d", recorder.Code)
	}

	var response CertificateResponse

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

// Test helper functions.
func TestCertificateAPI_Helpers(t *testing.T) {
	t.Parallel()

	config := Config{}
	testLogger := NewTestLogger("test", "INFO")

	// Initialize the global logger for the API to use
	_ = logger.InitFromEnv()

	api := NewCertificateAPI(config, testLogger, nil)

	// Test sendResponse
	recorder := httptest.NewRecorder()
	testData := CertificateResponseData{
		Certificates: []CertificateListItem{},
		Metadata: CertificateMetadata{
			Source: "test",
			Count:  0,
		},
	}

	api.writeJSONResponse(recorder, CertificateResponse{
		Success: true,
		Data:    testData,
	})

	if recorder.Code != 200 {
		t.Errorf("Expected status 200, got %d", recorder.Code)
	}

	var response CertificateResponse

	err := json.Unmarshal(recorder.Body.Bytes(), &response)
	if err != nil {
		t.Errorf("Failed to parse response: %v", err)
	}

	if !response.Success {
		t.Errorf("Expected successful response")
	}

	// Test sendError
	errorRecorder := httptest.NewRecorder()
	api.writeErrorResponse(errorRecorder, 400, "test error")

	if errorRecorder.Code != 400 {
		t.Errorf("Expected status 400, got %d", errorRecorder.Code)
	}

	var errorResponse CertificateResponse

	err = json.Unmarshal(errorRecorder.Body.Bytes(), &errorResponse)
	if err != nil {
		t.Errorf("Failed to parse error response: %v", err)
	}

	if errorResponse.Success {
		t.Errorf("Expected failed response")
	}

	if errorResponse.Error != "test error" {
		t.Errorf("Expected error message 'test error', got '%s'", errorResponse.Error)
	}
}

// Integration test for API routing.
func TestCertificateAPI_Routing(t *testing.T) {
	t.Parallel()

	config := Config{}
	testLogger := NewTestLogger("test", "INFO")

	// Initialize the global logger for the API to use
	_ = logger.InitFromEnv()

	api := NewCertificateAPI(config, testLogger, nil)

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

	config := Config{}
	testLogger := NewTestLogger("test", "INFO")

	// Initialize the global logger for the API to use
	_ = logger.InitFromEnv()

	api := NewCertificateAPI(config, testLogger, nil)

	req := httptest.NewRequest(http.MethodGet, "/b/internal/certificates/trusted", nil)
	w := httptest.NewRecorder()

	api.HandleCertificatesRequest(w, req)

	contentType := w.Header().Get("Content-Type")
	if contentType != "application/json" {
		t.Errorf("Expected Content-Type application/json, got %s", contentType)
	}
}

func TestCertificateAPI_MethodValidation(t *testing.T) {
	t.Parallel()

	config := Config{}
	testLogger := NewTestLogger("test", "INFO")

	// Initialize the global logger for the API to use
	_ = logger.InitFromEnv()

	api := NewCertificateAPI(config, testLogger, nil)

	// Test that GET endpoints reject POST
	req := httptest.NewRequest(http.MethodPost, "/b/internal/certificates/trusted", nil)
	w := httptest.NewRecorder()

	api.HandleCertificatesRequest(w, req)

	if w.Code != 405 {
		t.Errorf("Expected status 405 for wrong method, got %d", w.Code)
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

	config := Config{}
	testLogger := NewTestLogger("test", "INFO")

	// Initialize the global logger for the API to use
	_ = logger.InitFromEnv()

	api := NewCertificateAPI(config, testLogger, nil)

	// Test with malformed JSON
	req := httptest.NewRequest(http.MethodPost, "/b/internal/certificates/parse",
		strings.NewReader(`{"pem": incomplete json`))
	req.Header.Set("Content-Type", "application/json")

	responseWriter := httptest.NewRecorder()
	api.HandleCertificatesRequest(responseWriter, req)

	if responseWriter.Code != 400 {
		t.Errorf("Expected status 400 for malformed JSON, got %d", responseWriter.Code)
	}

	var response CertificateResponse

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
	config := Config{}
	testLogger := NewTestLogger("test", "INFO")

	// Initialize the global logger for the API to use
	_ = logger.InitFromEnv()

	api := NewCertificateAPI(config, testLogger, nil)

	req := httptest.NewRequest(http.MethodGet, "/b/internal/certificates/trusted", nil)

	b.ResetTimer()

	for range b.N {
		w := httptest.NewRecorder()
		api.HandleCertificatesRequest(w, req)
	}
}

func BenchmarkCertificateAPI_BlacksmithCertificates(b *testing.B) {
	config := Config{}
	testLogger := NewTestLogger("test", "INFO")

	// Initialize the global logger for the API to use
	_ = logger.InitFromEnv()

	api := NewCertificateAPI(config, testLogger, nil)

	req := httptest.NewRequest(http.MethodGet, "/b/internal/certificates/blacksmith", nil)

	b.ResetTimer()

	for range b.N {
		w := httptest.NewRecorder()
		api.HandleCertificatesRequest(w, req)
	}
}

// Test response structure validation.
func TestCertificateResponse_Structure(t *testing.T) {
	t.Parallel()

	response := CertificateResponse{
		Success: true,
		Data: CertificateResponseData{
			Certificates: []CertificateListItem{
				{
					Name: "test.crt",
					Path: "/etc/ssl/certs/test.crt",
					Details: CertificateInfo{
						Version:      3,
						SerialNumber: "123456789",
						Subject: CertificateSubject{
							CommonName: "test.example.com",
						},
						Issuer: CertificateSubject{
							CommonName: "Test CA",
						},
						KeyUsage:    []string{"digitalSignature"},
						ExtKeyUsage: []string{"serverAuth"},
					},
				},
			},
			Metadata: CertificateMetadata{
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
	var unmarshaled CertificateResponse
	if err := json.Unmarshal(jsonData, &unmarshaled); err != nil {
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
