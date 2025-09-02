package main

import (
	"crypto/md5"  // #nosec G501 - MD5 used only for certificate fingerprint identification, not security
	"crypto/sha1" // #nosec G505 - SHA1 used only for certificate fingerprint identification, not security
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"net"
	"net/url"
	"os/exec"
	"strings"
	"time"
)

// CertificateInfo represents detailed information about an X.509 certificate
type CertificateInfo struct {
	Version            int                     `json:"version"`
	SerialNumber       string                  `json:"serialNumber"`
	Issuer             CertificateSubject      `json:"issuer"`
	Subject            CertificateSubject      `json:"subject"`
	NotBefore          time.Time               `json:"notBefore"`
	NotAfter           time.Time               `json:"notAfter"`
	KeyUsage           []string                `json:"keyUsage"`
	ExtKeyUsage        []string                `json:"extKeyUsage"`
	SubjectAltNames    []string                `json:"subjectAltNames"`
	SignatureAlgorithm string                  `json:"signatureAlgorithm"`
	PublicKey          CertificatePublicKey    `json:"publicKey"`
	Extensions         []CertificateExtension  `json:"extensions"`
	Fingerprints       CertificateFingerprints `json:"fingerprints"`
	PEMEncoded         string                  `json:"pemEncoded"`
	TextDetails        string                  `json:"textDetails"`
	Chain              []CertificateChainInfo  `json:"chain,omitempty"`
}

// CertificateSubject represents the subject or issuer of a certificate
type CertificateSubject struct {
	Country            []string `json:"country,omitempty"`
	Organization       []string `json:"organization,omitempty"`
	OrganizationalUnit []string `json:"organizationalUnit,omitempty"`
	Locality           []string `json:"locality,omitempty"`
	Province           []string `json:"province,omitempty"`
	StreetAddress      []string `json:"streetAddress,omitempty"`
	PostalCode         []string `json:"postalCode,omitempty"`
	SerialNumber       string   `json:"serialNumber,omitempty"`
	CommonName         string   `json:"commonName,omitempty"`
	Names              []string `json:"names,omitempty"`
}

// CertificatePublicKey represents the public key information
type CertificatePublicKey struct {
	Algorithm string `json:"algorithm"`
	Bits      int    `json:"bits"`
	Exponent  int64  `json:"exponent,omitempty"`
	Modulus   string `json:"modulus,omitempty"`
	Curve     string `json:"curve,omitempty"`
}

// CertificateExtension represents a certificate extension
type CertificateExtension struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Critical bool   `json:"critical"`
	Value    string `json:"value"`
}

// CertificateFingerprints represents certificate fingerprints
type CertificateFingerprints struct {
	MD5    string `json:"md5"`
	SHA1   string `json:"sha1"`
	SHA256 string `json:"sha256"`
}

// CertificateChainInfo represents basic information about certificates in the chain
type CertificateChainInfo struct {
	Subject      string    `json:"subject"`
	Issuer       string    `json:"issuer"`
	SerialNumber string    `json:"serialNumber"`
	NotBefore    time.Time `json:"notBefore"`
	NotAfter     time.Time `json:"notAfter"`
}

// CertificateListItem represents a certificate in a list
type CertificateListItem struct {
	Name    string          `json:"name"`
	Path    string          `json:"path,omitempty"`
	Details CertificateInfo `json:"details"`
}

// CertificateResponse represents the API response for certificate operations
type CertificateResponse struct {
	Success bool                    `json:"success"`
	Data    CertificateResponseData `json:"data"`
	Error   string                  `json:"error,omitempty"`
}

// CertificateResponseData represents the data portion of certificate API responses
type CertificateResponseData struct {
	Certificates []CertificateListItem `json:"certificates"`
	Metadata     CertificateMetadata   `json:"metadata"`
}

// CertificateMetadata represents metadata about the certificate source
type CertificateMetadata struct {
	Source    string    `json:"source"`
	Timestamp time.Time `json:"timestamp"`
	Count     int       `json:"count"`
}

// CertificateFileItem represents a certificate file reference without parsing the content
type CertificateFileItem struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// CertificateFileResponse represents the API response for certificate file listings
type CertificateFileResponse struct {
	Success bool                `json:"success"`
	Data    CertificateFileData `json:"data"`
	Error   string              `json:"error,omitempty"`
}

// CertificateFileData represents the data portion of certificate file API responses
type CertificateFileData struct {
	Files    []CertificateFileItem `json:"files"`
	Metadata CertificateMetadata   `json:"metadata"`
}

// ParseCertificateFromPEM parses a PEM-encoded certificate and returns detailed information
func ParseCertificateFromPEM(pemData string) (*CertificateInfo, error) {
	block, _ := pem.Decode([]byte(pemData))
	if block == nil {
		return nil, fmt.Errorf("failed to parse PEM block")
	}

	if block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("PEM block is not a certificate")
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse certificate: %w", err)
	}

	return ParseCertificateFromX509(cert, pemData)
}

// ParseCertificateFromX509 converts an x509.Certificate to CertificateInfo
func ParseCertificateFromX509(cert *x509.Certificate, pemData string) (*CertificateInfo, error) {
	certInfo := &CertificateInfo{
		Version:            cert.Version,
		SerialNumber:       cert.SerialNumber.String(),
		Issuer:             convertPkixName(cert.Issuer),
		Subject:            convertPkixName(cert.Subject),
		NotBefore:          cert.NotBefore,
		NotAfter:           cert.NotAfter,
		KeyUsage:           convertKeyUsage(cert.KeyUsage),
		ExtKeyUsage:        convertExtKeyUsage(cert.ExtKeyUsage),
		SubjectAltNames:    extractSubjectAltNames(cert),
		SignatureAlgorithm: cert.SignatureAlgorithm.String(),
		PublicKey:          convertPublicKey(cert.PublicKey),
		Extensions:         convertExtensions(cert.Extensions),
		Fingerprints:       calculateFingerprints(cert.Raw),
		PEMEncoded:         pemData,
	}

	// Get OpenSSL text output
	textDetails, err := GetOpenSSLTextOutput(pemData)
	if err != nil {
		Logger.Wrap("certificates").Error("Failed to get OpenSSL text output: %s", err)
		certInfo.TextDetails = "OpenSSL text output not available"
	} else {
		certInfo.TextDetails = textDetails
	}

	return certInfo, nil
}

// GetOpenSSLTextOutput executes OpenSSL to get the text representation of a certificate
func GetOpenSSLTextOutput(pemData string) (string, error) {
	cmd := exec.Command("openssl", "x509", "-text", "-noout")
	cmd.Stdin = strings.NewReader(pemData)

	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to execute openssl command: %w", err)
	}

	return string(output), nil
}

// FetchCertificateFromEndpoint connects to a network endpoint and retrieves its certificate
func FetchCertificateFromEndpoint(address string, timeout time.Duration) (*CertificateInfo, error) {
	return FetchCertificateFromEndpointWithVerify(address, timeout, true) // Default to skip verify for certificate inspection
}

// FetchCertificateFromEndpointWithVerify connects to a network endpoint and retrieves its certificate with TLS verification control
func FetchCertificateFromEndpointWithVerify(address string, timeout time.Duration, skipTLSVerify bool) (*CertificateInfo, error) {
	// Parse the address to handle both URLs and host:port formats
	var host, port string

	// Check if the address looks like a URL
	if strings.HasPrefix(address, "http://") || strings.HasPrefix(address, "https://") {
		u, err := url.Parse(address)
		if err != nil {
			return nil, fmt.Errorf("invalid URL format: %w", err)
		}

		host = u.Hostname()
		port = u.Port()

		// If no port specified in URL, use default based on scheme
		if port == "" {
			switch u.Scheme {
			case "https":
				port = "443"
			case "http":
				port = "80"
			default:
				port = "443" // Default to HTTPS port
			}
		}

		address = net.JoinHostPort(host, port)
	} else {
		// Try to parse as host:port
		var err error
		host, port, err = net.SplitHostPort(address)
		if err != nil {
			// If no port specified, assume HTTPS (443)
			host = address
			port = "443"
		}
		address = net.JoinHostPort(host, port)
	}

	// Create a TLS connection with a timeout
	dialer := &net.Dialer{Timeout: timeout}
	conn, err := tls.DialWithDialer(dialer, "tcp", address, &tls.Config{
		InsecureSkipVerify: skipTLSVerify, // #nosec G402 - Configurable TLS verification for certificate inspection
	})
	if err != nil {
		return nil, fmt.Errorf("failed to connect to %s: %w", address, err)
	}
	defer func() { _ = conn.Close() }()

	// Get the peer certificate
	state := conn.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		return nil, fmt.Errorf("no certificates found for %s", address)
	}

	cert := state.PeerCertificates[0]

	// Convert to PEM format
	pemData := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: cert.Raw,
	})

	certInfo, err := ParseCertificateFromX509(cert, string(pemData))
	if err != nil {
		return nil, err
	}

	// Add certificate chain information
	if len(state.PeerCertificates) > 1 {
		var chain []CertificateChainInfo
		for i, chainCert := range state.PeerCertificates[1:] {
			chainInfo := CertificateChainInfo{
				Subject:      chainCert.Subject.String(),
				Issuer:       chainCert.Issuer.String(),
				SerialNumber: chainCert.SerialNumber.String(),
				NotBefore:    chainCert.NotBefore,
				NotAfter:     chainCert.NotAfter,
			}
			chain = append(chain, chainInfo)

			// Limit chain length to prevent excessive data
			if i >= 9 { // max 10 certificates in chain
				break
			}
		}
		certInfo.Chain = chain
	}

	return certInfo, nil
}

// convertPkixName converts a pkix.Name to CertificateSubject
func convertPkixName(name pkix.Name) CertificateSubject {
	var names []string
	for _, attr := range name.Names {
		names = append(names, fmt.Sprintf("%s=%s", attr.Type.String(), attr.Value))
	}

	return CertificateSubject{
		Country:            name.Country,
		Organization:       name.Organization,
		OrganizationalUnit: name.OrganizationalUnit,
		Locality:           name.Locality,
		Province:           name.Province,
		StreetAddress:      name.StreetAddress,
		PostalCode:         name.PostalCode,
		SerialNumber:       name.SerialNumber,
		CommonName:         name.CommonName,
		Names:              names,
	}
}

// convertKeyUsage converts x509.KeyUsage to string slice
func convertKeyUsage(usage x509.KeyUsage) []string {
	var usages []string

	if usage&x509.KeyUsageDigitalSignature != 0 {
		usages = append(usages, "digitalSignature")
	}
	if usage&x509.KeyUsageContentCommitment != 0 {
		usages = append(usages, "contentCommitment")
	}
	if usage&x509.KeyUsageKeyEncipherment != 0 {
		usages = append(usages, "keyEncipherment")
	}
	if usage&x509.KeyUsageDataEncipherment != 0 {
		usages = append(usages, "dataEncipherment")
	}
	if usage&x509.KeyUsageKeyAgreement != 0 {
		usages = append(usages, "keyAgreement")
	}
	if usage&x509.KeyUsageCertSign != 0 {
		usages = append(usages, "keyCertSign")
	}
	if usage&x509.KeyUsageCRLSign != 0 {
		usages = append(usages, "cRLSign")
	}
	if usage&x509.KeyUsageEncipherOnly != 0 {
		usages = append(usages, "encipherOnly")
	}
	if usage&x509.KeyUsageDecipherOnly != 0 {
		usages = append(usages, "decipherOnly")
	}

	return usages
}

// convertExtKeyUsage converts x509.ExtKeyUsage to string slice
func convertExtKeyUsage(usage []x509.ExtKeyUsage) []string {
	var usages []string

	for _, u := range usage {
		switch u {
		case x509.ExtKeyUsageServerAuth:
			usages = append(usages, "serverAuth")
		case x509.ExtKeyUsageClientAuth:
			usages = append(usages, "clientAuth")
		case x509.ExtKeyUsageCodeSigning:
			usages = append(usages, "codeSigning")
		case x509.ExtKeyUsageEmailProtection:
			usages = append(usages, "emailProtection")
		case x509.ExtKeyUsageTimeStamping:
			usages = append(usages, "timeStamping")
		case x509.ExtKeyUsageOCSPSigning:
			usages = append(usages, "ocspSigning")
		}
	}

	return usages
}

// extractSubjectAltNames extracts subject alternative names from the certificate
func extractSubjectAltNames(cert *x509.Certificate) []string {
	var sans []string

	sans = append(sans, cert.DNSNames...)

	for _, ip := range cert.IPAddresses {
		sans = append(sans, ip.String())
	}

	sans = append(sans, cert.EmailAddresses...)

	for _, uri := range cert.URIs {
		sans = append(sans, uri.String())
	}

	return sans
}

// convertPublicKey extracts public key information
func convertPublicKey(pubKey interface{}) CertificatePublicKey {
	switch key := pubKey.(type) {
	case *x509.Certificate:
		return convertPublicKey(key.PublicKey)
	default:
		// This is a simplified implementation
		// In a real implementation, you'd need to handle RSA, ECDSA, Ed25519, etc.
		return CertificatePublicKey{
			Algorithm: "Unknown",
			Bits:      0,
		}
	}
}

// convertExtensions converts certificate extensions
func convertExtensions(extensions []pkix.Extension) []CertificateExtension {
	var exts []CertificateExtension

	for _, ext := range extensions {
		exts = append(exts, CertificateExtension{
			ID:       ext.Id.String(),
			Name:     getExtensionName(ext.Id.String()),
			Critical: ext.Critical,
			Value:    hex.EncodeToString(ext.Value),
		})
	}

	return exts
}

// getExtensionName returns a human-readable name for extension OIDs
func getExtensionName(oid string) string {
	names := map[string]string{
		"2.5.29.15":         "keyUsage",
		"2.5.29.37":         "extKeyUsage",
		"2.5.29.17":         "subjectAltName",
		"2.5.29.19":         "basicConstraints",
		"2.5.29.14":         "subjectKeyIdentifier",
		"2.5.29.35":         "authorityKeyIdentifier",
		"2.5.29.32":         "certificatePolicies",
		"2.5.29.31":         "crlDistributionPoints",
		"1.3.6.1.5.5.7.1.1": "authorityInfoAccess",
	}

	if name, ok := names[oid]; ok {
		return name
	}

	return "unknown"
}

// calculateFingerprints calculates certificate fingerprints
func calculateFingerprints(certBytes []byte) CertificateFingerprints {
	md5Hash := md5.Sum(certBytes)   // #nosec G401 - MD5 used only for certificate fingerprint identification, not security
	sha1Hash := sha1.Sum(certBytes) // #nosec G401 - SHA1 used only for certificate fingerprint identification, not security
	sha256Hash := sha256.Sum256(certBytes)

	return CertificateFingerprints{
		MD5:    hex.EncodeToString(md5Hash[:]),
		SHA1:   hex.EncodeToString(sha1Hash[:]),
		SHA256: hex.EncodeToString(sha256Hash[:]),
	}
}

// ValidateCertificateFormat validates that the input is a valid PEM certificate
func ValidateCertificateFormat(pemData string) error {
	block, _ := pem.Decode([]byte(pemData))
	if block == nil {
		return fmt.Errorf("invalid PEM format")
	}

	if block.Type != "CERTIFICATE" {
		return fmt.Errorf("PEM block is not a certificate, got: %s", block.Type)
	}

	_, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return fmt.Errorf("invalid certificate data: %w", err)
	}

	return nil
}

// ParseCertificateChain parses a certificate chain from PEM data
func ParseCertificateChain(pemData string) ([]*CertificateInfo, error) {
	var certificates []*CertificateInfo
	remaining := []byte(pemData)

	for len(remaining) > 0 {
		block, rest := pem.Decode(remaining)
		if block == nil {
			break
		}

		if block.Type != "CERTIFICATE" {
			remaining = rest
			continue
		}

		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("failed to parse certificate in chain: %w", err)
		}

		// Convert back to PEM for this individual certificate
		certPEM := pem.EncodeToMemory(block)

		certInfo, err := ParseCertificateFromX509(cert, string(certPEM))
		if err != nil {
			return nil, err
		}

		certificates = append(certificates, certInfo)
		remaining = rest
	}

	if len(certificates) == 0 {
		return nil, fmt.Errorf("no valid certificates found in PEM data")
	}

	return certificates, nil
}
