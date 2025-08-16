package main

import (
	"crypto/tls"
	"fmt"
	"strings"
)

// TLS version mapping from string to Go constants
var tlsVersionMap = map[string]uint16{
	"TLSv1.2": tls.VersionTLS12,
	"TLSv1.3": tls.VersionTLS13,
}

// Cipher suite mapping from OpenSSL names to Go constants
var cipherSuiteMap = map[string]uint16{
	// TLS 1.3 cipher suites
	"TLS_AES_128_GCM_SHA256":       tls.TLS_AES_128_GCM_SHA256,
	"TLS_AES_256_GCM_SHA384":       tls.TLS_AES_256_GCM_SHA384,
	"TLS_CHACHA20_POLY1305_SHA256": tls.TLS_CHACHA20_POLY1305_SHA256,

	// TLS 1.2 cipher suites - ECDHE
	"ECDHE-RSA-AES128-GCM-SHA256": tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
	"ECDHE-RSA-AES256-GCM-SHA384": tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
	"ECDHE-RSA-AES128-SHA256":     tls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA256,
	"ECDHE-RSA-AES128-SHA":        tls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA,
	"ECDHE-RSA-AES256-SHA":        tls.TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA,
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

// ParseTLSVersions parses a space-separated list of TLS protocol versions
// and returns the minimum and maximum versions as Go constants
func ParseTLSVersions(protocols string) (min, max uint16, err error) {
	if protocols == "" {
		return tls.VersionTLS12, tls.VersionTLS13, nil
	}

	versions := strings.Fields(protocols)
	if len(versions) == 0 {
		return tls.VersionTLS12, tls.VersionTLS13, nil
	}

	var supportedVersions []uint16
	for _, version := range versions {
		if tlsVersion, ok := tlsVersionMap[version]; ok {
			supportedVersions = append(supportedVersions, tlsVersion)
		} else {
			return 0, 0, fmt.Errorf("unsupported TLS protocol version: %s", version)
		}
	}

	if len(supportedVersions) == 0 {
		return 0, 0, fmt.Errorf("no valid TLS protocol versions found")
	}

	// Find minimum and maximum versions
	min = supportedVersions[0]
	max = supportedVersions[0]
	for _, version := range supportedVersions {
		if version < min {
			min = version
		}
		if version > max {
			max = version
		}
	}

	return min, max, nil
}

// ParseCipherSuites parses a colon-separated list of cipher suite names
// and returns the corresponding Go cipher suite constants
func ParseCipherSuites(ciphers string) ([]uint16, error) {
	if ciphers == "" {
		// Return Go's default secure cipher suites
		return nil, nil
	}

	// Handle special keywords
	if strings.Contains(ciphers, "HIGH") {
		// Use Go's default secure cipher suites for HIGH
		return nil, nil
	}

	cipherNames := strings.Split(ciphers, ":")
	var suites []uint16
	var warnings []string

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
		Logger.Wrap("tls").Error("Unknown cipher suites (using Go defaults): %s", strings.Join(warnings, ", "))
	}

	// If no valid cipher suites found, return nil to use Go defaults
	if len(suites) == 0 {
		return nil, nil
	}

	return suites, nil
}

// ValidateCertificateFiles validates that certificate and key files exist and are valid
func ValidateCertificateFiles(certFile, keyFile string) error {
	if certFile == "" {
		return fmt.Errorf("TLS certificate file path is required when TLS is enabled")
	}
	if keyFile == "" {
		return fmt.Errorf("TLS key file path is required when TLS is enabled")
	}

	// Load and validate the certificate/key pair
	_, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return fmt.Errorf("failed to load TLS certificate/key pair: %w", err)
	}

	return nil
}

// CreateTLSConfig creates a tls.Config from the TLS configuration
func CreateTLSConfig(tlsConfig TLSConfig) (*tls.Config, error) {
	if !tlsConfig.Enabled {
		return nil, nil
	}

	// Validate certificate files
	if err := ValidateCertificateFiles(tlsConfig.Certificate, tlsConfig.Key); err != nil {
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
	cipherSuites, err := ParseCipherSuites(tlsConfig.Ciphers)
	if err != nil {
		return nil, fmt.Errorf("failed to parse cipher suites: %w", err)
	}

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