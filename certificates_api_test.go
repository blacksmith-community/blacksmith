package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
)

func NewLogger(component, level string) *Log {
	return &Log{
		ctx:   []string{component},
		max:   1024,
		index: 0,
		ring:  make([]string, 0),
	}
}

func TestCertificateAPI_HandleCertificatesRequest(t *testing.T) {
	// Create a test config and logger
	config := Config{} // Minimal config for testing
	logger := NewLogger("test", "INFO")

	api := NewCertificateAPI(config, logger, nil)

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
			var body io.Reader
			if test.body != "" {
				body = strings.NewReader(test.body)
			}

			req := httptest.NewRequest(test.method, test.path, body)
			if test.body != "" {
				req.Header.Set("Content-Type", "application/json")
			}

			w := httptest.NewRecorder()
			api.HandleCertificatesRequest(w, req)

			// Endpoint lookup may fail without network; accept 200 or 400 for endpoint tests
			if test.path == "/b/internal/certificates/endpoint" {
				if !(w.Code == 200 || w.Code == 400) {
					t.Errorf("Expected status 200 or 400 for endpoint request, got %d", w.Code)
				}
			} else if w.Code != test.expectedStatus {
				t.Errorf("Expected status %d, got %d", test.expectedStatus, w.Code)
			}

			if test.checkResponse {
				// Check that response is valid JSON
				var response map[string]interface{}
				if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
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
	config := Config{}
	logger := NewLogger("test", "INFO")
	api := NewCertificateAPI(config, logger, nil)

	req := httptest.NewRequest("GET", "/b/internal/certificates/trusted", nil)
	w := httptest.NewRecorder()

	api.handleTrustedCertificates(w, req)

	// Should return 200 even if no certificates found
	if w.Code != 200 {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	// Check response structure
	var response CertificateResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
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
	config := Config{}
	logger := Logger.Wrap("test")
	api := NewCertificateAPI(config, logger, nil)

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

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var body io.Reader
			if tt.body != "" {
				body = strings.NewReader(tt.body)
			}

			req := httptest.NewRequest(tt.method, "/b/internal/certificates/trusted/file", body)
			if tt.method == "POST" {
				req.Header.Set("Content-Type", "application/json")
			}

			w := httptest.NewRecorder()
			api.handleTrustedCertificateFile(w, req)

			if w.Code != tt.expectedStatus {
				t.Errorf("Expected status %d, got %d", tt.expectedStatus, w.Code)
			}

			if tt.expectedError != "" {
				var response CertificateResponse
				if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
					t.Fatalf("Failed to unmarshal response: %v", err)
				}

				if !response.Success && !strings.Contains(response.Error, tt.expectedError) {
					t.Errorf("Expected error containing '%s', got '%s'", tt.expectedError, response.Error)
				}
			}
		})
	}
}

func TestCertificateAPI_HandleBlacksmithCertificates(t *testing.T) {
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
	logger := NewLogger("test", "INFO")
	api := NewCertificateAPI(config, logger, nil)

	req := httptest.NewRequest("GET", "/b/internal/certificates/blacksmith", nil)
	w := httptest.NewRecorder()

	api.handleBlacksmithCertificates(w, req)

	if w.Code != 200 {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	var response CertificateResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Errorf("Failed to parse response: %v", err)
	}

	if !response.Success {
		t.Logf("Response indicates failure (expected in test environment): %s", response.Error)
	}
}

func TestCertificateAPI_HandleEndpointCertificates(t *testing.T) {
	config := Config{}
	logger := NewLogger("test", "INFO")
	api := NewCertificateAPI(config, logger, nil)

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
			req := httptest.NewRequest("POST", "/b/internal/certificates/endpoint",
				strings.NewReader(test.requestBody))
			req.Header.Set("Content-Type", "application/json")

			w := httptest.NewRecorder()
			api.handleEndpointCertificates(w, req)

			// Accept 200 or 400 for live endpoint fetch depending on environment
			if strings.Contains(test.requestBody, "google.com:443") {
				if !(w.Code == 200 || w.Code == 400) {
					t.Errorf("Expected status 200 or 400, got %d. Response: %s", w.Code, w.Body.String())
				}
			} else if w.Code != test.expectedStatus {
				t.Errorf("Expected status %d, got %d. Response: %s",
					test.expectedStatus, w.Code, w.Body.String())
			}

			// Check response is valid JSON for all cases
			var response map[string]interface{}
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Errorf("Response is not valid JSON: %v", err)
			}
		})
	}
}

func TestCertificateAPI_HandleParseCertificate(t *testing.T) {
	config := Config{}
	logger := NewLogger("test", "INFO")
	api := NewCertificateAPI(config, logger, nil)

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
			req := httptest.NewRequest("POST", "/b/internal/certificates/parse",
				strings.NewReader(test.requestBody))
			req.Header.Set("Content-Type", "application/json")

			w := httptest.NewRecorder()
			api.handleParseCertificate(w, req)

			if w.Code != test.expectedStatus {
				t.Errorf("Expected status %d, got %d", test.expectedStatus, w.Code)
			}

			var response CertificateResponse
			if w.Code == 200 {
				if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
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
	config := Config{}
	logger := NewLogger("test", "INFO")
	api := NewCertificateAPI(config, logger, nil)

	// Test with non-existent service
	req := httptest.NewRequest("GET", "/b/internal/certificates/services/test-instance", nil)
	w := httptest.NewRecorder()

	api.handleServiceCertificates(w, req)

	// Should return 200 with empty list for non-existent service
	if w.Code != 200 {
		t.Errorf("Expected status 200 for non-existent service, got %d", w.Code)
	}

	var response CertificateResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Errorf("Failed to parse response: %v", err)
	}

	if !response.Success {
		t.Errorf("Expected success response with empty list for non-existent service")
	}

	if len(response.Data.Certificates) != 0 {
		t.Errorf("Expected empty certificate list for non-existent service, got %d certificates", len(response.Data.Certificates))
	}
}

// Test helper functions
func TestCertificateAPI_Helpers(t *testing.T) {
	config := Config{}
	logger := NewLogger("test", "INFO")
	api := NewCertificateAPI(config, logger, nil)

	// Test sendResponse
	w := httptest.NewRecorder()
	testData := CertificateResponseData{
		Certificates: []CertificateListItem{},
		Metadata: CertificateMetadata{
			Source: "test",
			Count:  0,
		},
	}

	api.writeJSONResponse(w, CertificateResponse{
		Success: true,
		Data:    testData,
	})

	if w.Code != 200 {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	var response CertificateResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Errorf("Failed to parse response: %v", err)
	}

	if !response.Success {
		t.Errorf("Expected successful response")
	}

	// Test sendError
	w2 := httptest.NewRecorder()
	api.writeErrorResponse(w2, 400, "test error")

	if w2.Code != 400 {
		t.Errorf("Expected status 400, got %d", w2.Code)
	}

	var errorResponse CertificateResponse
	if err := json.Unmarshal(w2.Body.Bytes(), &errorResponse); err != nil {
		t.Errorf("Failed to parse error response: %v", err)
	}

	if errorResponse.Success {
		t.Errorf("Expected failed response")
	}

	if errorResponse.Error != "test error" {
		t.Errorf("Expected error message 'test error', got '%s'", errorResponse.Error)
	}
}

// Integration test for API routing
func TestCertificateAPI_Routing(t *testing.T) {
	config := Config{}
	logger := NewLogger("test", "INFO")
	api := NewCertificateAPI(config, logger, nil)

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

	for _, tc := range testCases {
		t.Run(tc.path+" "+tc.method, func(t *testing.T) {
			var body io.Reader
			if tc.method == "POST" {
				body = bytes.NewReader([]byte{}) // Empty body to trigger 400
			}

			req := httptest.NewRequest(tc.method, tc.path, body)
			w := httptest.NewRecorder()

			api.HandleCertificatesRequest(w, req)

			if w.Code != tc.status {
				t.Errorf("Expected status %d, got %d. Response: %s",
					tc.status, w.Code, w.Body.String())
			}
		})
	}
}

func TestCertificateAPI_ContentType(t *testing.T) {
	config := Config{}
	logger := NewLogger("test", "INFO")
	api := NewCertificateAPI(config, logger, nil)

	req := httptest.NewRequest("GET", "/b/internal/certificates/trusted", nil)
	w := httptest.NewRecorder()

	api.HandleCertificatesRequest(w, req)

	contentType := w.Header().Get("Content-Type")
	if contentType != "application/json" {
		t.Errorf("Expected Content-Type application/json, got %s", contentType)
	}
}

func TestCertificateAPI_MethodValidation(t *testing.T) {
	config := Config{}
	logger := NewLogger("test", "INFO")
	api := NewCertificateAPI(config, logger, nil)

	// Test that GET endpoints reject POST
	req := httptest.NewRequest("POST", "/b/internal/certificates/trusted", nil)
	w := httptest.NewRecorder()

	api.HandleCertificatesRequest(w, req)

	if w.Code != 405 {
		t.Errorf("Expected status 405 for wrong method, got %d", w.Code)
	}

	// Test that POST endpoints reject GET
	req2 := httptest.NewRequest("GET", "/b/internal/certificates/endpoint", nil)
	w2 := httptest.NewRecorder()

	api.HandleCertificatesRequest(w2, req2)

	if w2.Code != 405 {
		t.Errorf("Expected status 405 for wrong method, got %d", w2.Code)
	}
}

// Test error handling and edge cases
func TestCertificateAPI_ErrorHandling(t *testing.T) {
	config := Config{}
	logger := NewLogger("test", "INFO")
	api := NewCertificateAPI(config, logger, nil)

	// Test with malformed JSON
	req := httptest.NewRequest("POST", "/b/internal/certificates/parse",
		strings.NewReader(`{"pem": incomplete json`))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	api.HandleCertificatesRequest(w, req)

	if w.Code != 400 {
		t.Errorf("Expected status 400 for malformed JSON, got %d", w.Code)
	}

	var response CertificateResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Errorf("Failed to parse error response: %v", err)
	}

	if response.Success {
		t.Errorf("Expected failed response for malformed JSON")
	}
}

// Benchmark API performance
func BenchmarkCertificateAPI_TrustedCertificates(b *testing.B) {
	config := Config{}
	logger := NewLogger("test", "INFO")
	api := NewCertificateAPI(config, logger, nil)

	req := httptest.NewRequest("GET", "/b/internal/certificates/trusted", nil)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w := httptest.NewRecorder()
		api.HandleCertificatesRequest(w, req)
	}
}

func BenchmarkCertificateAPI_BlacksmithCertificates(b *testing.B) {
	config := Config{}
	logger := NewLogger("test", "INFO")
	api := NewCertificateAPI(config, logger, nil)

	req := httptest.NewRequest("GET", "/b/internal/certificates/blacksmith", nil)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w := httptest.NewRecorder()
		api.HandleCertificatesRequest(w, req)
	}
}

// Test response structure validation
func TestCertificateResponse_Structure(t *testing.T) {
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
