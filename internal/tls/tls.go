package tls

import (
	"blacksmith/internal/config"
	"blacksmith/pkg/logger"
	"crypto/tls"
	"errors"
	"fmt"
	"strings"
)

// Static errors for err113 compliance.
var (
	ErrUnsupportedTLSProtocolVersion   = errors.New("unsupported TLS protocol version")
	ErrNoValidTLSProtocolVersionsFound = errors.New("no valid TLS protocol versions found")
	ErrTLSCertificateFilePathRequired  = errors.New("TLS certificate file path is required when TLS is enabled")
	ErrTLSKeyFilePathRequired          = errors.New("TLS key file path is required when TLS is enabled")
	ErrTLSDisabled                     = errors.New("TLS is disabled")
)

// getTLSVersionMap returns the TLS version mapping from string to Go constants.
func getTLSVersionMap() map[string]uint16 {
	return map[string]uint16{
		"TLSv1.2": tls.VersionTLS12,
		"TLSv1.3": tls.VersionTLS13,
	}
}

// getCipherSuiteMap returns the cipher suite mapping from OpenSSL names to Go constants.
func getCipherSuiteMap() map[string]uint16 {
	return map[string]uint16{
		// TLS 1.3 cipher suites
		"TLS_AES_128_GCM_SHA256":       tls.TLS_AES_128_GCM_SHA256,
		"TLS_AES_256_GCM_SHA384":       tls.TLS_AES_256_GCM_SHA384,
		"TLS_CHACHA20_POLY1305_SHA256": tls.TLS_CHACHA20_POLY1305_SHA256,

		// TLS 1.2 cipher suites - ECDHE
		"ECDHE-RSA-AES128-GCM-SHA256":   tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
		"ECDHE-RSA-AES256-GCM-SHA384":   tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
		"ECDHE-RSA-AES128-SHA256":       tls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA256,
		"ECDHE-RSA-AES128-SHA":          tls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA,
		"ECDHE-RSA-AES256-SHA":          tls.TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA,
		"ECDHE-ECDSA-AES128-GCM-SHA256": tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
		"ECDHE-ECDSA-AES256-GCM-SHA384": tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
		"ECDHE-ECDSA-AES128-SHA256":     tls.TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA256,
		"ECDHE-ECDSA-AES128-SHA":        tls.TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA,
		"ECDHE-ECDSA-AES256-SHA":        tls.TLS_ECDHE_ECDSA_WITH_AES_256_CBC_SHA,

		// Non-ECDHE cipher suites
		"AES128-GCM-SHA256": tls.TLS_RSA_WITH_AES_128_GCM_SHA256,
		"AES256-GCM-SHA384": tls.TLS_RSA_WITH_AES_256_GCM_SHA384,
		"AES128-SHA256":     tls.TLS_RSA_WITH_AES_128_CBC_SHA256,
		"AES128-SHA":        tls.TLS_RSA_WITH_AES_128_CBC_SHA,
		"AES256-SHA":        tls.TLS_RSA_WITH_AES_256_CBC_SHA,
	}
}

// ParseTLSVersions parses a space-separated list of TLS protocol versions
// and returns the minimum and maximum versions as Go constants.
func ParseTLSVersions(protocols string) (uint16, uint16, error) {
	if protocols == "" {
		return tls.VersionTLS12, tls.VersionTLS13, nil
	}

	versions := strings.Fields(protocols)
	if len(versions) == 0 {
		return tls.VersionTLS12, tls.VersionTLS13, nil
	}

	var (
		supportedVersions []uint16
		minVer, maxVer    uint16
	)

	tlsVersionMap := getTLSVersionMap()

	for _, version := range versions {
		if tlsVersion, ok := tlsVersionMap[version]; ok {
			supportedVersions = append(supportedVersions, tlsVersion)
		} else {
			return 0, 0, fmt.Errorf("unsupported TLS protocol version %s: %w", version, ErrUnsupportedTLSProtocolVersion)
		}
	}

	if len(supportedVersions) == 0 {
		return 0, 0, ErrNoValidTLSProtocolVersionsFound
	}

	// Find minimum and maximum versions
	minVer = supportedVersions[0]

	maxVer = supportedVersions[0]
	for _, version := range supportedVersions {
		if version < minVer {
			minVer = version
		}

		if version > maxVer {
			maxVer = version
		}
	}

	return minVer, maxVer, nil
}

// ParseCipherSuites parses a colon-separated list of cipher suite names
// and returns the corresponding Go cipher suite constants.
func ParseCipherSuites(ciphers string) []uint16 {
	if ciphers == "" {
		// Return Go's default secure cipher suites
		return nil
	}

	// Handle special keywords
	if strings.Contains(ciphers, "HIGH") {
		// Use Go's default secure cipher suites for HIGH
		return nil
	}

	cipherNames := strings.Split(ciphers, ":")
	cipherSuiteMap := getCipherSuiteMap()

	var (
		suites   []uint16
		warnings []string
	)

	for _, name := range cipherNames {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}

		// Skip negative specifications (starting with !)
		if strings.HasPrefix(name, "!") {
			continue
		}

		if suite, ok := cipherSuiteMap[name]; ok {
			suites = append(suites, suite)
		} else {
			warnings = append(warnings, name)
		}
	}

	if len(warnings) > 0 {
		logger.Get().Named("tls").Error("Unknown cipher suites (using Go defaults): %s", strings.Join(warnings, ", "))
	}

	// If no valid cipher suites found, return nil to use Go defaults
	if len(suites) == 0 {
		return nil
	}

	return suites
}

// ValidateCertificateFiles validates that certificate and key files exist and are valid.
func ValidateCertificateFiles(certFile, keyFile string) error {
	if certFile == "" {
		return ErrTLSCertificateFilePathRequired
	}

	if keyFile == "" {
		return ErrTLSKeyFilePathRequired
	}

	// Load and validate the certificate/key pair
	_, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return fmt.Errorf("failed to load TLS certificate/key pair: %w", err)
	}

	return nil
}

// CreateTLSConfig creates a tls.Config from the TLS configuration.
func CreateTLSConfig(tlsConfig config.TLSConfig) (*tls.Config, error) {
	if !tlsConfig.Enabled {
		return nil, ErrTLSDisabled
	}

	// Validate certificate files
	err := ValidateCertificateFiles(tlsConfig.Certificate, tlsConfig.Key)
	if err != nil {
		return nil, err
	}

	// Parse TLS versions
	minVersion, maxVersion, err := ParseTLSVersions(tlsConfig.Protocols)
	if err != nil {
		return nil, fmt.Errorf("failed to parse TLS protocols: %w", err)
	}

	// Enforce minimum TLS 1.2 for security
	if minVersion < tls.VersionTLS12 {
		minVersion = tls.VersionTLS12
	}

	// Parse cipher suites
	cipherSuites := ParseCipherSuites(tlsConfig.Ciphers)

	// Load certificate
	cert, err := tls.LoadX509KeyPair(tlsConfig.Certificate, tlsConfig.Key)
	if err != nil {
		return nil, fmt.Errorf("failed to load certificate: %w", err)
	}

	config := &tls.Config{ // #nosec G402 - MinVersion is enforced to be >= TLS 1.2 above
		MinVersion:               minVersion,
		MaxVersion:               maxVersion,
		PreferServerCipherSuites: true,
		Certificates:             []tls.Certificate{cert},
	}

	// Set cipher suites if specified
	if cipherSuites != nil {
		config.CipherSuites = cipherSuites
	}

	return config, nil
}
