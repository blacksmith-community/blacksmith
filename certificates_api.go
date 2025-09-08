package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"blacksmith/pkg/logger"
	"gopkg.in/yaml.v2"
)

// Constants for certificate API operations.
const (
	DefaultCertFetchTimeout = 5 * time.Second
	MaxCertFetchTimeout     = 30 * time.Second
	MinCertMatchParts       = 2
)

// Static errors for err113 compliance.
var (
	ErrFileDoesNotContainCertificate = errors.New("file does not contain a certificate")
)

// CertificateAPI handles certificate-related HTTP endpoints.
type CertificateAPI struct {
	config Config
	logger logger.Logger
	broker *Broker
}

// NewCertificateAPI creates a new certificate API handler.
func NewCertificateAPI(config Config, logger logger.Logger, broker *Broker) *CertificateAPI {
	return &CertificateAPI{
		config: config,
		logger: logger,
		broker: broker,
	}
}

// HandleCertificatesRequest routes certificate requests to appropriate handlers.
func (c *CertificateAPI) HandleCertificatesRequest(w http.ResponseWriter, req *http.Request) {
	// Parse the path to determine the endpoint
	path := strings.TrimPrefix(req.URL.Path, "/b/internal/certificates")

	switch {
	case path == "/trusted":
		c.handleTrustedCertificates(w, req)
	case path == "/trusted/file":
		c.handleTrustedCertificateFile(w, req)
	case path == "/blacksmith":
		c.handleBlacksmithCertificates(w, req)
	case path == "/endpoint":
		c.handleEndpointCertificates(w, req)
	case path == "/parse":
		c.handleParseCertificate(w, req)
	case strings.HasPrefix(path, "/services/"):
		c.handleServiceCertificates(w, req)
	default:
		c.writeErrorResponse(w, http.StatusNotFound, "endpoint not found")
	}
}

// handleTrustedCertificates handles /b/internal/certificates/trusted.
func (c *CertificateAPI) handleTrustedCertificates(w http.ResponseWriter, req *http.Request) {
	log := logger.Get().Named("certificates-trusted")

	if req.Method != http.MethodGet {
		c.writeErrorResponse(w, http.StatusMethodNotAllowed, "method not allowed")

		return
	}

	log.Debug("fetching trusted certificate file list")

	// Specifically look for BOSH trusted certificates in /etc/ssl/certs
	trustedPath := "/etc/ssl/certs"

	var files []CertificateFileItem

	if _, err := os.Stat(trustedPath); os.IsNotExist(err) {
		log.Debug("trusted certificates directory %s does not exist", trustedPath)
	} else {
		log.Debug("scanning for bosh-trusted-cert-*.pem files in %s", trustedPath)

		// Use filepath.Glob to find bosh-trusted-cert-*.pem files
		pattern := filepath.Join(trustedPath, "bosh-trusted-cert-*.pem")

		matches, err := filepath.Glob(pattern)
		if err != nil {
			log.Error("failed to search for BOSH trusted certificates: %s", err)
		} else {
			for _, match := range matches {
				fileName := filepath.Base(match)
				files = append(files, CertificateFileItem{
					Name: fileName,
					Path: match,
				})
			}
		}
	}

	response := CertificateFileResponse{
		Success: true,
		Data: CertificateFileData{
			Files: files,
			Metadata: CertificateMetadata{
				Source:    "trusted",
				Timestamp: time.Now(),
				Count:     len(files),
			},
		},
	}

	c.writeJSONResponse(w, response)
}

// handleTrustedCertificateFile handles /b/internal/certificates/trusted/file.
func (c *CertificateAPI) handleTrustedCertificateFile(w http.ResponseWriter, req *http.Request) {
	l := logger.Get().Named("certificates-trusted-file")

	if req.Method != http.MethodPost {
		c.writeErrorResponse(w, http.StatusMethodNotAllowed, "method not allowed")

		return
	}

	// Parse the request body
	var requestData struct {
		FilePath string `json:"filePath"`
	}

	if err := json.NewDecoder(req.Body).Decode(&requestData); err != nil {
		c.writeErrorResponse(w, http.StatusBadRequest, fmt.Sprintf("invalid request body: %s", err))

		return
	}

	if requestData.FilePath == "" {
		c.writeErrorResponse(w, http.StatusBadRequest, "filePath is required")

		return
	}

	l.Debug("fetching certificate details for file %s", requestData.FilePath)

	// Security validation: ensure the file path is in /etc/ssl/certs and matches the expected pattern
	if !strings.HasPrefix(requestData.FilePath, "/etc/ssl/certs/bosh-trusted-cert-") ||
		!strings.HasSuffix(requestData.FilePath, ".pem") {
		c.writeErrorResponse(w, http.StatusBadRequest, "invalid file path: must be a BOSH trusted certificate")

		return
	}

	// Validate the file path doesn't contain any traversal attempts
	cleanPath := filepath.Clean(requestData.FilePath)
	if cleanPath != requestData.FilePath {
		c.writeErrorResponse(w, http.StatusBadRequest, "invalid file path: path traversal detected")

		return
	}

	// Load the certificate from the specified file
	cert, err := c.loadCertificateFromFile(req.Context(), requestData.FilePath, filepath.Base(requestData.FilePath))
	if err != nil {
		c.writeErrorResponse(w, http.StatusBadRequest, fmt.Sprintf("failed to load certificate: %s", err))

		return
	}

	certificates := []CertificateListItem{*cert}

	response := CertificateResponse{
		Success: true,
		Data: CertificateResponseData{
			Certificates: certificates,
			Metadata: CertificateMetadata{
				Source:    "trusted-file",
				Timestamp: time.Now(),
				Count:     len(certificates),
			},
		},
	}

	c.writeJSONResponse(w, response)
}

// handleBlacksmithCertificates handles /b/internal/certificates/blacksmith.
func (c *CertificateAPI) handleBlacksmithCertificates(w http.ResponseWriter, req *http.Request) {
	l := logger.Get().Named("certificates-blacksmith")

	if req.Method != http.MethodGet {
		c.writeErrorResponse(w, http.StatusMethodNotAllowed, "method not allowed")

		return
	}

	l.Debug("fetching blacksmith configuration certificates")

	var certificates []CertificateListItem

	// Check TLS configuration certificates
	if c.config.Broker.TLS.Enabled {
		if c.config.Broker.TLS.Certificate != "" {
			cert, err := c.loadCertificateFromFile(req.Context(), c.config.Broker.TLS.Certificate, "Broker TLS Certificate")
			if err != nil {
				l.Error("failed to load broker TLS certificate: %s", err)
			} else {
				certificates = append(certificates, *cert)
			}
		}
	}

	// Check Vault TLS certificates
	if c.config.Vault.CACert != "" {
		cert, err := c.loadCertificateFromFile(req.Context(), c.config.Vault.CACert, "Vault CA Certificate")
		if err != nil {
			l.Error("failed to load vault CA certificate: %s", err)
		} else {
			certificates = append(certificates, *cert)
		}
	}

	// Check BOSH certificates
	if c.config.BOSH.CACert != "" {
		cert, err := c.loadCertificateFromFile(req.Context(), c.config.BOSH.CACert, "BOSH CA Certificate")
		if err != nil {
			l.Error("failed to load BOSH CA certificate: %s", err)
		} else {
			certificates = append(certificates, *cert)
		}
	}

	response := CertificateResponse{
		Success: true,
		Data: CertificateResponseData{
			Certificates: certificates,
			Metadata: CertificateMetadata{
				Source:    "blacksmith",
				Timestamp: time.Now(),
				Count:     len(certificates),
			},
		},
	}

	c.writeJSONResponse(w, response)
}

// handleEndpointCertificates handles /b/internal/certificates/endpoint.
func (c *CertificateAPI) handleEndpointCertificates(w http.ResponseWriter, req *http.Request) {
	log := logger.Get().Named("certificates-endpoint")

	if req.Method != http.MethodPost {
		c.writeErrorResponse(w, http.StatusMethodNotAllowed, "method not allowed")

		return
	}

	// Parse the request body
	var requestData struct {
		Endpoint string `json:"endpoint"`
		Timeout  int    `json:"timeout,omitempty"`
	}

	if err := json.NewDecoder(req.Body).Decode(&requestData); err != nil {
		c.writeErrorResponse(w, http.StatusBadRequest, fmt.Sprintf("invalid request body: %s", err))

		return
	}

	if requestData.Endpoint == "" {
		c.writeErrorResponse(w, http.StatusBadRequest, "endpoint is required")

		return
	}

	// Set default timeout
	timeout := DefaultCertFetchTimeout
	if requestData.Timeout > 0 {
		timeout = time.Duration(requestData.Timeout) * time.Second
		// Limit timeout to 30 seconds for security
		if timeout > MaxCertFetchTimeout {
			timeout = MaxCertFetchTimeout
		}
	}

	log.Debug("fetching certificate from endpoint %s with timeout %v", requestData.Endpoint, timeout)

	// Fetch certificate from endpoint
	certInfo, err := FetchCertificateFromEndpoint(req.Context(), requestData.Endpoint, timeout)
	if err != nil {
		c.writeErrorResponse(w, http.StatusBadRequest, fmt.Sprintf("failed to fetch certificate: %s", err))

		return
	}

	certificates := []CertificateListItem{
		{
			Name:    "Certificate from " + requestData.Endpoint,
			Details: *certInfo,
		},
	}

	response := CertificateResponse{
		Success: true,
		Data: CertificateResponseData{
			Certificates: certificates,
			Metadata: CertificateMetadata{
				Source:    "endpoint",
				Timestamp: time.Now(),
				Count:     len(certificates),
			},
		},
	}

	c.writeJSONResponse(w, response)
}

// handleParseCertificate handles /b/internal/certificates/parse.
func (c *CertificateAPI) handleParseCertificate(w http.ResponseWriter, req *http.Request) {
	log := logger.Get().Named("certificates-parse")

	if req.Method != http.MethodPost {
		c.writeErrorResponse(w, http.StatusMethodNotAllowed, "method not allowed")

		return
	}

	// Parse the request body
	var requestData struct {
		Certificate string `json:"certificate"`
		Name        string `json:"name,omitempty"`
	}

	if err := json.NewDecoder(req.Body).Decode(&requestData); err != nil {
		c.writeErrorResponse(w, http.StatusBadRequest, fmt.Sprintf("invalid request body: %s", err))

		return
	}

	if requestData.Certificate == "" {
		c.writeErrorResponse(w, http.StatusBadRequest, "certificate is required")

		return
	}

	// Validate certificate format
	if err := ValidateCertificateFormat(requestData.Certificate); err != nil {
		c.writeErrorResponse(w, http.StatusBadRequest, fmt.Sprintf("invalid certificate format: %s", err))

		return
	}

	log.Debug("parsing provided certificate")

	// Parse the certificate
	certInfo, err := ParseCertificateFromPEM(req.Context(), requestData.Certificate)
	if err != nil {
		c.writeErrorResponse(w, http.StatusBadRequest, fmt.Sprintf("failed to parse certificate: %s", err))

		return
	}

	name := requestData.Name
	if name == "" {
		name = "User-provided certificate"
	}

	certificates := []CertificateListItem{
		{
			Name:    name,
			Details: *certInfo,
		},
	}

	response := CertificateResponse{
		Success: true,
		Data: CertificateResponseData{
			Certificates: certificates,
			Metadata: CertificateMetadata{
				Source:    "parse",
				Timestamp: time.Now(),
				Count:     len(certificates),
			},
		},
	}

	c.writeJSONResponse(w, response)
}

// handleServiceCertificates handles /b/internal/certificates/services/{id}.
func (c *CertificateAPI) handleServiceCertificates(w http.ResponseWriter, req *http.Request) {
	l := logger.Get().Named("certificates-service")

	if req.Method != http.MethodGet {
		c.writeErrorResponse(w, http.StatusMethodNotAllowed, "method not allowed")

		return
	}

	// Extract service ID from path
	pattern := regexp.MustCompile(`^/services/([^/]+)/?.*$`)

	matches := pattern.FindStringSubmatch(strings.TrimPrefix(req.URL.Path, "/b/internal/certificates"))
	if len(matches) < MinCertMatchParts {
		c.writeErrorResponse(w, http.StatusBadRequest, "invalid service ID")

		return
	}

	serviceID := matches[1]
	l.Debug("fetching certificates for service %s", serviceID)

	// Check if broker is available
	if c.broker == nil || c.broker.Vault == nil {
		l.Info("broker or vault not available for certificate retrieval")
		// Return empty certificate list for testing
		response := CertificateResponse{
			Success: true,
			Data: CertificateResponseData{
				Certificates: []CertificateListItem{},
				Metadata: CertificateMetadata{
					Source:    "service",
					Count:     0,
					Timestamp: time.Now(),
				},
			},
		}

		w.Header().Set("Content-Type", "application/json")

		err := json.NewEncoder(w).Encode(response)
		if err != nil {
			http.Error(w, "Failed to encode response", http.StatusInternalServerError)
			l.Error("Failed to encode certificate response: %v", err)
		}

		return
	}

	// Get the manifest from vault
	var manifestData struct {
		Manifest string `json:"manifest"`
	}

	exists, err := c.broker.Vault.Get(req.Context(), serviceID+"/manifest", &manifestData)
	if err != nil || !exists {
		l.Error("unable to find manifest for instance %s: %v", serviceID, err)
		c.writeErrorResponse(w, http.StatusNotFound, "manifest not available for this instance")

		return
	}

	// Parse the manifest and extract certificates
	certificates := c.extractCertificatesFromManifest(req.Context(), manifestData.Manifest)

	response := CertificateResponse{
		Success: true,
		Data: CertificateResponseData{
			Certificates: certificates,
			Metadata: CertificateMetadata{
				Source:    "manifest",
				Timestamp: time.Now(),
				Count:     len(certificates),
			},
		},
	}

	c.writeJSONResponse(w, response)
}

// extractCertificatesFromManifest parses a YAML manifest and extracts certificates.
func (c *CertificateAPI) extractCertificatesFromManifest(ctx context.Context, manifest string) []CertificateListItem {
	l := logger.Get().Named("extract-manifest-certs")
	certificates := []CertificateListItem{}

	// Parse YAML manifest
	var data map[interface{}]interface{}

	err := yaml.Unmarshal([]byte(manifest), &data)
	if err != nil {
		l.Error("failed to parse manifest YAML: %v", err)

		return certificates
	}

	// Find certificates recursively in the YAML structure
	c.findCertificatesInYAML(ctx, data, "", &certificates)

	l.Debug("found %d certificates in manifest", len(certificates))

	return certificates
}

// findCertificatesInYAML recursively searches for certificate PEM blocks in YAML data.
func (c *CertificateAPI) findCertificatesInYAML(ctx context.Context, data interface{}, path string, certificates *[]CertificateListItem) {
	switch v := data.(type) {
	case map[interface{}]interface{}:
		for key, value := range v {
			keyStr := fmt.Sprintf("%v", key)

			newPath := path
			if newPath == "" {
				newPath = keyStr
			} else {
				newPath = fmt.Sprintf("%s.%s", path, keyStr)
			}

			c.findCertificatesInYAML(ctx, value, newPath, certificates)
		}
	case map[string]interface{}:
		for key, value := range v {
			newPath := path
			if newPath == "" {
				newPath = key
			} else {
				newPath = fmt.Sprintf("%s.%s", path, key)
			}

			c.findCertificatesInYAML(ctx, value, newPath, certificates)
		}
	case []interface{}:
		for i, item := range v {
			newPath := fmt.Sprintf("%s[%d]", path, i)
			c.findCertificatesInYAML(ctx, item, newPath, certificates)
		}
	case string:
		// Check if this string contains a certificate
		if strings.Contains(v, "-----BEGIN CERTIFICATE-----") && strings.Contains(v, "-----END CERTIFICATE-----") {
			// Extract all certificates from the string (might be a chain)
			certs := c.extractCertificatesFromPEM(v)
			for certIndex, certPEM := range certs {
				certInfo, err := ParseCertificateFromPEM(ctx, certPEM)
				if err != nil {
					continue
				}

				name := path
				if len(certs) > 1 {
					name = fmt.Sprintf("%s (cert %d)", path, certIndex+1)
				}

				*certificates = append(*certificates, CertificateListItem{
					Name:    name,
					Path:    path,
					Details: *certInfo,
				})
			}
		}
	}
}

// extractCertificatesFromPEM extracts individual certificate PEM blocks from a string.
func (c *CertificateAPI) extractCertificatesFromPEM(pemData string) []string {
	var certs []string

	// Split by certificate boundaries
	parts := strings.Split(pemData, "-----BEGIN CERTIFICATE-----")
	for _, part := range parts {
		if strings.Contains(part, "-----END CERTIFICATE-----") {
			endIndex := strings.Index(part, "-----END CERTIFICATE-----")
			if endIndex > 0 {
				certContent := part[:endIndex]
				fullCert := "-----BEGIN CERTIFICATE-----" + certContent + "-----END CERTIFICATE-----"
				certs = append(certs, fullCert)
			}
		}
	}

	return certs
}

// loadCertificateFromFile loads and parses a certificate from a file.
func (c *CertificateAPI) loadCertificateFromFile(ctx context.Context, filePath, name string) (*CertificateListItem, error) {
	// #nosec G304 - filePath is validated by caller
	content, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	// Check if the content contains a certificate
	if !strings.Contains(string(content), "BEGIN CERTIFICATE") {
		return nil, ErrFileDoesNotContainCertificate
	}

	certInfo, err := ParseCertificateFromPEM(ctx, string(content))
	if err != nil {
		return nil, fmt.Errorf("failed to parse certificate: %w", err)
	}

	return &CertificateListItem{
		Name:    name,
		Path:    filePath,
		Details: *certInfo,
	}, nil
}

// writeJSONResponse writes a JSON response.
func (c *CertificateAPI) writeJSONResponse(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")

	jsonData, err := json.Marshal(data)
	if err != nil {
		c.writeErrorResponse(w, http.StatusInternalServerError, "failed to marshal response")

		return
	}

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(jsonData)
}

// writeErrorResponse writes an error response.
func (c *CertificateAPI) writeErrorResponse(w http.ResponseWriter, statusCode int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)

	response := CertificateResponse{
		Success: false,
		Error:   message,
		Data: CertificateResponseData{
			Certificates: []CertificateListItem{},
			Metadata: CertificateMetadata{
				Source:    "error",
				Timestamp: time.Now(),
				Count:     0,
			},
		},
	}

	jsonData, err := json.Marshal(response)
	if err != nil {
		http.Error(w, "Failed to marshal response", http.StatusInternalServerError)

		return
	}

	_, _ = w.Write(jsonData)
}
