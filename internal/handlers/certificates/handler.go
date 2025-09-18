package certificates

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

	"blacksmith/pkg/certificates"
	"blacksmith/pkg/logger"
	"gopkg.in/yaml.v2"
)

// Static errors for err113 compliance.
var (
	ErrFilePathRequired                 = errors.New("filePath is required")
	ErrInvalidFilePathMustBeBOSHTrusted = errors.New("invalid file path: must be a BOSH trusted certificate")
	ErrInvalidFilePathTraversalDetected = errors.New("invalid file path: path traversal detected")
	ErrEndpointRequired                 = errors.New("endpoint is required")
	ErrCertificateRequired              = errors.New("certificate is required")
	ErrInvalidServiceID                 = errors.New("invalid service ID")
	ErrManifestNotFound                 = errors.New("manifest not found")
)

// Handler handles certificate-related HTTP endpoints.
type Handler struct {
	config interface{}
	logger logger.Logger
	broker interface{}
}

// NewHandler creates a new certificate API handler.
func NewHandler(config interface{}, logger logger.Logger, broker interface{}) *Handler {
	return &Handler{
		config: config,
		logger: logger,
		broker: broker,
	}
}

// HandleCertificatesRequest routes certificate requests to appropriate handlers.
func (h *Handler) HandleCertificatesRequest(writer http.ResponseWriter, req *http.Request) {
	// Parse the path to determine the endpoint
	path := strings.TrimPrefix(req.URL.Path, "/b/internal/certificates")

	switch {
	case path == "/trusted":
		h.handleTrustedCertificates(writer, req)
	case path == "/trusted/file":
		h.handleTrustedCertificateFile(writer, req)
	case path == "/blacksmith":
		h.handleBlacksmithCertificates(writer, req)
	case path == "/endpoint":
		h.handleEndpointCertificates(writer, req)
	case path == "/parse":
		h.handleParseCertificate(writer, req)
	case strings.HasPrefix(path, "/services/"):
		h.handleServiceCertificates(writer, req)
	default:
		h.writeErrorResponse(writer, http.StatusNotFound, "endpoint not found")
	}
}

// HandleTrustedCertificates is a public wrapper for testing purposes.
func (h *Handler) HandleTrustedCertificates(writer http.ResponseWriter, req *http.Request) {
	h.handleTrustedCertificates(writer, req)
}

// HandleTrustedCertificateFile is a public wrapper for testing purposes.
func (h *Handler) HandleTrustedCertificateFile(writer http.ResponseWriter, req *http.Request) {
	h.handleTrustedCertificateFile(writer, req)
}

// HandleBlacksmithCertificates is a public wrapper for testing purposes.
func (h *Handler) HandleBlacksmithCertificates(writer http.ResponseWriter, req *http.Request) {
	h.handleBlacksmithCertificates(writer, req)
}

// HandleEndpointCertificates is a public wrapper for testing purposes.
func (h *Handler) HandleEndpointCertificates(writer http.ResponseWriter, req *http.Request) {
	h.handleEndpointCertificates(writer, req)
}

// HandleParseCertificate is a public wrapper for testing purposes.
func (h *Handler) HandleParseCertificate(writer http.ResponseWriter, req *http.Request) {
	h.handleParseCertificate(writer, req)
}

// handleTrustedCertificates handles /b/internal/certificates/trusted.
func (h *Handler) handleTrustedCertificates(writer http.ResponseWriter, req *http.Request) {
	log := logger.Get().Named("certificates-trusted")

	if req.Method != http.MethodGet {
		h.writeErrorResponse(writer, http.StatusMethodNotAllowed, "method not allowed")

		return
	}

	log.Debug("fetching trusted certificate file list")

	// Specifically look for BOSH trusted certificates in /etc/ssl/certs
	trustedPath := "/etc/ssl/certs"

	var files []certificates.CertificateFileItem

	_, err := os.Stat(trustedPath)
	if os.IsNotExist(err) {
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
				files = append(files, certificates.CertificateFileItem{
					Name: fileName,
					Path: match,
				})
			}
		}
	}

	response := certificates.CertificateFileResponse{
		Success: true,
		Data: certificates.CertificateFileData{
			Files: files,
			Metadata: certificates.CertificateMetadata{
				Source:    "trusted",
				Timestamp: time.Now(),
				Count:     len(files),
			},
		},
	}

	h.writeJSONResponse(writer, response)
}

// handleTrustedCertificateFile handles /b/internal/certificates/trusted/file.
func (h *Handler) handleTrustedCertificateFile(writer http.ResponseWriter, req *http.Request) {
	log := logger.Get().Named("certificates-trusted-file")

	if req.Method != http.MethodPost {
		h.writeErrorResponse(writer, http.StatusMethodNotAllowed, "method not allowed")

		return
	}

	filePath, err := h.parseTrustedCertFileRequest(req)
	if err != nil {
		h.writeErrorResponse(writer, http.StatusBadRequest, err.Error())

		return
	}

	err = h.validateTrustedCertFilePath(filePath)
	if err != nil {
		h.writeErrorResponse(writer, http.StatusBadRequest, err.Error())

		return
	}

	log.Debug("fetching certificate details for file %s", filePath)

	cert, err := h.loadCertificateFromFile(req.Context(), filePath, filepath.Base(filePath))
	if err != nil {
		h.writeErrorResponse(writer, http.StatusBadRequest, fmt.Sprintf("failed to load certificate: %s", err))

		return
	}

	h.sendTrustedCertificateResponse(writer, cert)
}

func (h *Handler) parseTrustedCertFileRequest(req *http.Request) (string, error) {
	var requestData struct {
		FilePath string `json:"filePath"`
	}

	err := json.NewDecoder(req.Body).Decode(&requestData)
	if err != nil {
		return "", fmt.Errorf("invalid request body: %w", err)
	}

	if requestData.FilePath == "" {
		return "", ErrFilePathRequired
	}

	return requestData.FilePath, nil
}

func (h *Handler) validateTrustedCertFilePath(filePath string) error {
	// Security validation: ensure the file path is in /etc/ssl/certs and matches the expected pattern
	if !strings.HasPrefix(filePath, "/etc/ssl/certs/bosh-trusted-cert-") ||
		!strings.HasSuffix(filePath, ".pem") {
		return ErrInvalidFilePathMustBeBOSHTrusted
	}

	// Validate the file path doesn't contain any traversal attempts
	cleanPath := filepath.Clean(filePath)
	if cleanPath != filePath {
		return ErrInvalidFilePathTraversalDetected
	}

	return nil
}

func (h *Handler) sendTrustedCertificateResponse(writer http.ResponseWriter, cert *certificates.CertificateListItem) {
	certList := []certificates.CertificateListItem{*cert}

	response := certificates.CertificateResponse{
		Success: true,
		Data: certificates.CertificateResponseData{
			Certificates: certList,
			Metadata: certificates.CertificateMetadata{
				Source:    "trusted-file",
				Timestamp: time.Now(),
				Count:     len(certList),
			},
		},
	}

	h.writeJSONResponse(writer, response)
}

// handleBlacksmithCertificates handles /b/internal/certificates/blacksmith.
func (h *Handler) handleBlacksmithCertificates(writer http.ResponseWriter, req *http.Request) {
	log := logger.Get().Named("certificates-blacksmith")

	if req.Method != http.MethodGet {
		h.writeErrorResponse(writer, http.StatusMethodNotAllowed, "method not allowed")

		return
	}

	log.Debug("fetching blacksmith configuration certificates")

	certList := h.loadBlacksmithCertificates(req.Context(), log)

	response := certificates.CertificateResponse{
		Success: true,
		Data: certificates.CertificateResponseData{
			Certificates: certList,
			Metadata: certificates.CertificateMetadata{
				Source:    "blacksmith",
				Timestamp: time.Now(),
				Count:     len(certList),
			},
		},
	}

	h.writeJSONResponse(writer, response)
}

// loadBlacksmithCertificates loads certificates from the blacksmith configuration.
func (h *Handler) loadBlacksmithCertificates(ctx context.Context, log interface {
	Error(msg string, args ...interface{})
}) []certificates.CertificateListItem {
	var certList []certificates.CertificateListItem

	cfg, valid := h.config.(interface {
		GetBrokerTLSEnabled() bool
		GetBrokerTLSCertificate() string
		GetVaultCACert() string
		GetBOSHCACert() string
	})
	if !valid {
		return certList
	}

	h.loadBrokerTLSCertificate(ctx, cfg, &certList, log)
	h.loadVaultCACertificate(ctx, cfg, &certList, log)
	h.loadBOSHCACertificate(ctx, cfg, &certList, log)

	return certList
}

// loadBrokerTLSCertificate loads the broker TLS certificate if enabled.
func (h *Handler) loadBrokerTLSCertificate(ctx context.Context, cfg interface {
	GetBrokerTLSEnabled() bool
	GetBrokerTLSCertificate() string
}, certList *[]certificates.CertificateListItem, log interface {
	Error(msg string, args ...interface{})
}) {
	if !cfg.GetBrokerTLSEnabled() || cfg.GetBrokerTLSCertificate() == "" {
		return
	}

	cert, err := h.loadCertificateFromFile(ctx, cfg.GetBrokerTLSCertificate(), "Broker TLS Certificate")
	if err != nil {
		log.Error("failed to load broker TLS certificate: %s", err)

		return
	}

	*certList = append(*certList, *cert)
}

// loadVaultCACertificate loads the Vault CA certificate if configured.
func (h *Handler) loadVaultCACertificate(ctx context.Context, cfg interface {
	GetVaultCACert() string
}, certList *[]certificates.CertificateListItem, log interface {
	Error(msg string, args ...interface{})
}) {
	vaultCACert := cfg.GetVaultCACert()
	if vaultCACert == "" {
		return
	}

	cert, err := h.loadCertificateFromFile(ctx, vaultCACert, "Vault CA Certificate")
	if err != nil {
		log.Error("failed to load vault CA certificate: %s", err)

		return
	}

	*certList = append(*certList, *cert)
}

// loadBOSHCACertificate loads the BOSH CA certificate if configured.
func (h *Handler) loadBOSHCACertificate(ctx context.Context, cfg interface {
	GetBOSHCACert() string
}, certList *[]certificates.CertificateListItem, log interface {
	Error(msg string, args ...interface{})
}) {
	boshCACert := cfg.GetBOSHCACert()
	if boshCACert == "" {
		return
	}

	cert, err := h.loadCertificateFromFile(ctx, boshCACert, "BOSH CA Certificate")
	if err != nil {
		log.Error("failed to load BOSH CA certificate: %s", err)

		return
	}

	*certList = append(*certList, *cert)
}

// handleEndpointCertificates handles /b/internal/certificates/endpoint.
func (h *Handler) handleEndpointCertificates(writer http.ResponseWriter, req *http.Request) {
	log := logger.Get().Named("certificates-endpoint")

	if req.Method != http.MethodPost {
		h.writeErrorResponse(writer, http.StatusMethodNotAllowed, "method not allowed")

		return
	}

	endpoint, timeout, err := h.parseEndpointCertRequest(req)
	if err != nil {
		h.writeErrorResponse(writer, http.StatusBadRequest, err.Error())

		return
	}

	log.Debug("fetching certificate from endpoint %s with timeout %v", endpoint, timeout)

	certInfo, err := certificates.FetchCertificateFromEndpoint(req.Context(), endpoint, timeout)
	if err != nil {
		h.writeErrorResponse(writer, http.StatusBadRequest, fmt.Sprintf("failed to fetch certificate: %s", err))

		return
	}

	h.sendEndpointCertificateResponse(writer, endpoint, certInfo)
}

func (h *Handler) parseEndpointCertRequest(req *http.Request) (string, time.Duration, error) {
	var requestData struct {
		Endpoint string `json:"endpoint"`
		Timeout  int    `json:"timeout,omitempty"`
	}

	err := json.NewDecoder(req.Body).Decode(&requestData)
	if err != nil {
		return "", 0, fmt.Errorf("invalid request body: %w", err)
	}

	if requestData.Endpoint == "" {
		return "", 0, ErrEndpointRequired
	}

	// Set default timeout
	timeout := certificates.DefaultCertFetchTimeout
	if requestData.Timeout > 0 {
		timeout = time.Duration(requestData.Timeout) * time.Second
		// Limit timeout to 30 seconds for security
		if timeout > certificates.MaxCertFetchTimeout {
			timeout = certificates.MaxCertFetchTimeout
		}
	}

	return requestData.Endpoint, timeout, nil
}

func (h *Handler) sendEndpointCertificateResponse(writer http.ResponseWriter, endpoint string, certInfo *certificates.CertificateInfo) {
	certList := []certificates.CertificateListItem{
		{
			Name:    "Certificate from " + endpoint,
			Details: *certInfo,
		},
	}

	response := certificates.CertificateResponse{
		Success: true,
		Data: certificates.CertificateResponseData{
			Certificates: certList,
			Metadata: certificates.CertificateMetadata{
				Source:    "endpoint",
				Timestamp: time.Now(),
				Count:     len(certList),
			},
		},
	}

	h.writeJSONResponse(writer, response)
}

// handleParseCertificate handles /b/internal/certificates/parse.
func (h *Handler) handleParseCertificate(writer http.ResponseWriter, req *http.Request) {
	log := logger.Get().Named("certificates-parse")

	if req.Method != http.MethodPost {
		h.writeErrorResponse(writer, http.StatusMethodNotAllowed, "method not allowed")

		return
	}

	certData, name, err := h.parseCertificateRequest(req)
	if err != nil {
		h.writeErrorResponse(writer, http.StatusBadRequest, err.Error())

		return
	}

	err = certificates.ValidateCertificateFormat(certData)
	if err != nil {
		h.writeErrorResponse(writer, http.StatusBadRequest, fmt.Sprintf("invalid certificate format: %s", err))

		return
	}

	log.Debug("parsing provided certificate")

	certInfo, err := certificates.ParseCertificateFromPEM(req.Context(), certData)
	if err != nil {
		h.writeErrorResponse(writer, http.StatusBadRequest, fmt.Sprintf("failed to parse certificate: %s", err))

		return
	}

	h.sendParsedCertificateResponse(writer, name, certInfo)
}

func (h *Handler) parseCertificateRequest(req *http.Request) (string, string, error) {
	var requestData struct {
		Certificate string `json:"certificate"`
		Name        string `json:"name,omitempty"`
	}

	err := json.NewDecoder(req.Body).Decode(&requestData)
	if err != nil {
		return "", "", fmt.Errorf("invalid request body: %w", err)
	}

	if requestData.Certificate == "" {
		return "", "", ErrCertificateRequired
	}

	name := requestData.Name
	if name == "" {
		name = "User-provided certificate"
	}

	return requestData.Certificate, name, nil
}

func (h *Handler) sendParsedCertificateResponse(writer http.ResponseWriter, name string, certInfo *certificates.CertificateInfo) {
	certList := []certificates.CertificateListItem{
		{
			Name:    name,
			Details: *certInfo,
		},
	}

	response := certificates.CertificateResponse{
		Success: true,
		Data: certificates.CertificateResponseData{
			Certificates: certList,
			Metadata: certificates.CertificateMetadata{
				Source:    "parse",
				Timestamp: time.Now(),
				Count:     len(certList),
			},
		},
	}

	h.writeJSONResponse(writer, response)
}

// handleServiceCertificates handles /b/internal/certificates/services/{id}.
func (h *Handler) handleServiceCertificates(writer http.ResponseWriter, req *http.Request) {
	log := logger.Get().Named("certificates-service")

	if req.Method != http.MethodGet {
		h.writeErrorResponse(writer, http.StatusMethodNotAllowed, "method not allowed")

		return
	}

	serviceID, err := h.extractServiceIDFromPath(req.URL.Path)
	if err != nil {
		h.writeErrorResponse(writer, http.StatusBadRequest, err.Error())

		return
	}

	log.Debug("fetching certificates for service %s", serviceID)

	brokerWithVault, available := h.checkBrokerVaultAvailability()
	if !available {
		h.sendEmptyServiceCertificateResponse(writer, log)

		return
	}

	manifestData, err := h.getManifestFromVault(req.Context(), brokerWithVault, serviceID)
	if err != nil {
		log.Error("unable to find manifest for instance %s: %v", serviceID, err)
		h.writeErrorResponse(writer, http.StatusNotFound, "manifest not available for this instance")

		return
	}

	certList := h.extractCertificatesFromManifest(req.Context(), manifestData)
	h.sendServiceCertificateResponse(writer, certList)
}

func (h *Handler) extractServiceIDFromPath(urlPath string) (string, error) {
	pattern := regexp.MustCompile(`^/services/([^/]+)/?.*$`)
	matches := pattern.FindStringSubmatch(strings.TrimPrefix(urlPath, "/b/internal/certificates"))

	if len(matches) < certificates.MinCertMatchParts {
		return "", ErrInvalidServiceID
	}

	return matches[1], nil
}

type brokerWithVault interface {
	GetVault() interface {
		Get(ctx context.Context, path string, out interface{}) (bool, error)
	}
}

func (h *Handler) checkBrokerVaultAvailability() (brokerWithVault, bool) {
	if h.broker == nil {
		return nil, false
	}

	brokerWithVault, ok := h.broker.(brokerWithVault)
	if !ok || brokerWithVault.GetVault() == nil {
		return nil, false
	}

	return brokerWithVault, true
}

func (h *Handler) sendEmptyServiceCertificateResponse(writer http.ResponseWriter, log interface {
	Info(msg string, args ...interface{})
}) {
	log.Info("broker or vault not available for certificate retrieval")

	response := certificates.CertificateResponse{
		Success: true,
		Data: certificates.CertificateResponseData{
			Certificates: []certificates.CertificateListItem{},
			Metadata: certificates.CertificateMetadata{
				Source:    "service",
				Count:     0,
				Timestamp: time.Now(),
			},
		},
	}

	writer.Header().Set("Content-Type", "application/json")

	err := json.NewEncoder(writer).Encode(response)
	if err != nil {
		http.Error(writer, "Failed to encode response", http.StatusInternalServerError)
		log.Info("Failed to encode certificate response: %v", err)
	}
}

func (h *Handler) getManifestFromVault(ctx context.Context, broker brokerWithVault, serviceID string) (string, error) {
	var manifestData struct {
		Manifest string `json:"manifest"`
	}

	exists, err := broker.GetVault().Get(ctx, serviceID+"/manifest", &manifestData)
	if err != nil || !exists {
		return "", ErrManifestNotFound
	}

	return manifestData.Manifest, nil
}

func (h *Handler) sendServiceCertificateResponse(writer http.ResponseWriter, certList []certificates.CertificateListItem) {
	response := certificates.CertificateResponse{
		Success: true,
		Data: certificates.CertificateResponseData{
			Certificates: certList,
			Metadata: certificates.CertificateMetadata{
				Source:    "manifest",
				Timestamp: time.Now(),
				Count:     len(certList),
			},
		},
	}

	h.writeJSONResponse(writer, response)
}

// extractCertificatesFromManifest parses a YAML manifest and extracts certificates.
func (h *Handler) extractCertificatesFromManifest(ctx context.Context, manifest string) []certificates.CertificateListItem {
	log := logger.Get().Named("extract-manifest-certs")
	certList := []certificates.CertificateListItem{}

	// Parse YAML manifest
	var data map[interface{}]interface{}

	err := yaml.Unmarshal([]byte(manifest), &data)
	if err != nil {
		log.Error("failed to parse manifest YAML: %v", err)

		return certList
	}

	// Find certificates recursively in the YAML structure
	h.findCertificatesInYAML(ctx, data, "", &certList)

	log.Debug("found %d certificates in manifest", len(certList))

	return certList
}

// findCertificatesInYAML recursively searches for certificate PEM blocks in YAML data.
func (h *Handler) findCertificatesInYAML(ctx context.Context, data interface{}, path string, certList *[]certificates.CertificateListItem) {
	switch value := data.(type) {
	case map[interface{}]interface{}:
		for key, value := range value {
			keyStr := fmt.Sprintf("%v", key)

			newPath := path
			if newPath == "" {
				newPath = keyStr
			} else {
				newPath = fmt.Sprintf("%s.%s", path, keyStr)
			}

			h.findCertificatesInYAML(ctx, value, newPath, certList)
		}
	case map[string]interface{}:
		for key, value := range value {
			newPath := path
			if newPath == "" {
				newPath = key
			} else {
				newPath = fmt.Sprintf("%s.%s", path, key)
			}

			h.findCertificatesInYAML(ctx, value, newPath, certList)
		}
	case []interface{}:
		for i, item := range value {
			newPath := fmt.Sprintf("%s[%d]", path, i)
			h.findCertificatesInYAML(ctx, item, newPath, certList)
		}
	case string:
		// Check if this string contains a certificate
		if strings.Contains(value, "-----BEGIN CERTIFICATE-----") && strings.Contains(value, "-----END CERTIFICATE-----") {
			// Extract all certificates from the string (might be a chain)
			certs := h.extractCertificatesFromPEM(value)
			for certIndex, certPEM := range certs {
				certInfo, err := certificates.ParseCertificateFromPEM(ctx, certPEM)
				if err != nil {
					continue
				}

				name := path
				if len(certs) > 1 {
					name = fmt.Sprintf("%s (cert %d)", path, certIndex+1)
				}

				*certList = append(*certList, certificates.CertificateListItem{
					Name:    name,
					Path:    path,
					Details: *certInfo,
				})
			}
		}
	}
}

// extractCertificatesFromPEM extracts individual certificate PEM blocks from a string.
func (h *Handler) extractCertificatesFromPEM(pemData string) []string {
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
func (h *Handler) loadCertificateFromFile(ctx context.Context, filePath, name string) (*certificates.CertificateListItem, error) {
	// #nosec G304 - filePath is validated by caller
	content, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	// Check if the content contains a certificate
	if !strings.Contains(string(content), "BEGIN CERTIFICATE") {
		return nil, certificates.ErrFileDoesNotContainCertificate
	}

	certInfo, err := certificates.ParseCertificateFromPEM(ctx, string(content))
	if err != nil {
		return nil, fmt.Errorf("failed to parse certificate: %w", err)
	}

	return &certificates.CertificateListItem{
		Name:    name,
		Path:    filePath,
		Details: *certInfo,
	}, nil
}

// writeJSONResponse writes a JSON response.
func (h *Handler) writeJSONResponse(writer http.ResponseWriter, data interface{}) {
	writer.Header().Set("Content-Type", "application/json")

	jsonData, err := json.Marshal(data)
	if err != nil {
		h.writeErrorResponse(writer, http.StatusInternalServerError, "failed to marshal response")

		return
	}

	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(jsonData)
}

// writeErrorResponse writes an error response.
func (h *Handler) writeErrorResponse(writer http.ResponseWriter, statusCode int, message string) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(statusCode)

	response := certificates.CertificateResponse{
		Success: false,
		Error:   message,
		Data: certificates.CertificateResponseData{
			Certificates: []certificates.CertificateListItem{},
			Metadata: certificates.CertificateMetadata{
				Source:    "error",
				Timestamp: time.Now(),
				Count:     0,
			},
		},
	}

	jsonData, err := json.Marshal(response)
	if err != nil {
		writer.WriteHeader(http.StatusInternalServerError)

		return
	}

	_, _ = writer.Write(jsonData)
}
