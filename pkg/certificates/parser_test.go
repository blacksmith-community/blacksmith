package certificates_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"testing"
	"time"

	"blacksmith/pkg/certificates"
)

//nolint:funlen // This test function is intentionally long for comprehensive testing
func TestParseCertificateFromPEM(t *testing.T) {
	t.Parallel()
	// Generate a real certificate for testing
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("Failed to generate private key: %v", err)
	}

	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			Organization: []string{"Test Organization"},
			CommonName:   "test.example.com",
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatalf("Failed to create certificate: %v", err)
	}

	// Convert to PEM
	validTestCertPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: certDER,
	})

	tests := []struct {
		name        string
		pemData     string
		expectError bool
		checkFields bool
	}{
		{
			name:        "Valid certificate",
			pemData:     string(validTestCertPEM),
			expectError: false,
			checkFields: true,
		},
		{
			name:        "Invalid PEM format",
			pemData:     "invalid pem data",
			expectError: true,
			checkFields: false,
		},
		{
			name:        "Empty PEM data",
			pemData:     "",
			expectError: true,
			checkFields: false,
		},
		{
			name:        "Non-certificate PEM",
			pemData:     "-----BEGIN RSA PRIVATE KEY-----\ndata\n-----END RSA PRIVATE KEY-----",
			expectError: true,
			checkFields: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			cert, err := certificates.ParseCertificateFromPEM(context.Background(), test.pemData)

			if test.expectError {
				if err == nil {
					t.Errorf("Expected error but got none")
				}

				return
			}

			if err != nil {
				t.Errorf("Unexpected error: %v", err)

				return
			}

			if test.checkFields && cert != nil {
				// Basic field checks
				if cert.Version == 0 {
					t.Errorf("Expected non-zero version")
				}

				if cert.SerialNumber == "" {
					t.Errorf("Expected non-empty serial number")
				}

				if cert.PEMEncoded == "" {
					t.Errorf("Expected PEM encoded data")
				}
			}
		})
	}
}
//nolint:funlen
func TestValidateCertificateFormat(t *testing.T) {
	t.Parallel()
	// Generate a real certificate for testing
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("Failed to generate private key: %v", err)
	}

	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test.example.com"},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Hour),
	}

	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatalf("Failed to create certificate: %v", err)
	}

	validTestCertPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: certDER,
	})

	tests := []struct {
		name        string
		pemData     string
		expectError bool
	}{
		{
			name:        "Valid certificate",
			pemData:     string(validTestCertPEM),
			expectError: false,
		},
		{
			name:        "Invalid PEM format",
			pemData:     "not a pem certificate",
			expectError: true,
		},
		{
			name:        "Empty data",
			pemData:     "",
			expectError: true,
		},
		{
			name:        "Private key instead of certificate",
			pemData:     "-----BEGIN PRIVATE KEY-----\ndata\n-----END PRIVATE KEY-----",
			expectError: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			err := certificates.ValidateCertificateFormat(test.pemData)

			if test.expectError && err == nil {
				t.Errorf("Expected error but got none")
			}

			if !test.expectError && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
		})
	}
}

func TestConvertKeyUsage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		usage    x509.KeyUsage
		expected []string
	}{
		{
			name:     "Digital signature only",
			usage:    x509.KeyUsageDigitalSignature,
			expected: []string{"digitalSignature"},
		},
		{
			name:     "Multiple usages",
			usage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
			expected: []string{"digitalSignature", "keyEncipherment"},
		},
		{
			name:     "No usage",
			usage:    0,
			expected: []string{},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			result := certificates.ConvertKeyUsage(test.usage)

			if len(result) != len(test.expected) {
				t.Errorf("Expected %d usages, got %d", len(test.expected), len(result))

				return
			}

			for i, expected := range test.expected {
				if result[i] != expected {
					t.Errorf("Expected usage %s at index %d, got %s", expected, i, result[i])
				}
			}
		})
	}
}

func TestConvertExtKeyUsage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		usage    []x509.ExtKeyUsage
		expected []string
	}{
		{
			name:     "Server auth only",
			usage:    []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
			expected: []string{"serverAuth"},
		},
		{
			name:     "Multiple extended usages",
			usage:    []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
			expected: []string{"serverAuth", "clientAuth"},
		},
		{
			name:     "Empty usage",
			usage:    []x509.ExtKeyUsage{},
			expected: []string{},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			result := certificates.ConvertExtKeyUsage(test.usage)

			if len(result) != len(test.expected) {
				t.Errorf("Expected %d usages, got %d", len(test.expected), len(result))

				return
			}

			for i, expected := range test.expected {
				if result[i] != expected {
					t.Errorf("Expected usage %s at index %d, got %s", expected, i, result[i])
				}
			}
		})
	}
}

func TestExtractSubjectAltNames(t *testing.T) {
	t.Parallel()
	// Create a test certificate with SAN
	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			Organization: []string{"Test"},
		},
		NotBefore:      time.Now(),
		NotAfter:       time.Now().Add(time.Hour),
		DNSNames:       []string{"example.com", "www.example.com"},
		IPAddresses:    []net.IP{net.IPv4(127, 0, 0, 1)},
		EmailAddresses: []string{"test@example.com"},
	}

	// Generate a private key for testing
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("Failed to generate private key: %v", err)
	}

	// Create the certificate
	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatalf("Failed to create certificate: %v", err)
	}

	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		t.Fatalf("Failed to parse certificate: %v", err)
	}

	result := certificates.ExtractSubjectAltNames(cert)

	expectedCount := 4 // 2 DNS names + 1 IP + 1 email
	if len(result) != expectedCount {
		t.Errorf("Expected %d SAN entries, got %d", expectedCount, len(result))
	}

	// Check that expected values are present
	expectedValues := []string{"example.com", "www.example.com", "127.0.0.1", "test@example.com"}
	for _, expected := range expectedValues {
		found := false

		for _, actual := range result {
			if actual == expected {
				found = true

				break
			}
		}

		if !found {
			t.Errorf("Expected SAN %s not found in result %v", expected, result)
		}
	}
}

func TestCalculateFingerprints(t *testing.T) {
	t.Parallel()
	// Simple test data
	testData := []byte("test certificate data")

	fingerprints := certificates.CalculateFingerprints(testData)

	if fingerprints.MD5 == "" {
		t.Errorf("Expected non-empty MD5 fingerprint")
	}

	if fingerprints.SHA1 == "" {
		t.Errorf("Expected non-empty SHA1 fingerprint")
	}

	if fingerprints.SHA256 == "" {
		t.Errorf("Expected non-empty SHA256 fingerprint")
	}

	// Check fingerprint format (should be hex)
	if len(fingerprints.MD5) != 32 {
		t.Errorf("Expected MD5 fingerprint length 32, got %d", len(fingerprints.MD5))
	}

	if len(fingerprints.SHA1) != 40 {
		t.Errorf("Expected SHA1 fingerprint length 40, got %d", len(fingerprints.SHA1))
	}

	if len(fingerprints.SHA256) != 64 {
		t.Errorf("Expected SHA256 fingerprint length 64, got %d", len(fingerprints.SHA256))
	}
}

func TestGetExtensionName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		oid      string
		expected string
	}{
		{"2.5.29.15", "keyUsage"},
		{"2.5.29.37", "extKeyUsage"},
		{"2.5.29.17", "subjectAltName"},
		{"2.5.29.19", "basicConstraints"},
		{"1.2.3.4.5", "unknown"},
		{"", "unknown"},
	}

	for _, test := range tests {
		t.Run(test.oid, func(t *testing.T) {
			t.Parallel()

			result := certificates.GetExtensionName(test.oid)
			if result != test.expected {
				t.Errorf("Expected %s, got %s", test.expected, result)
			}
		})
	}
}

func TestConvertPkixName(t *testing.T) {
	t.Parallel()

	name := pkix.Name{
		Country:            []string{"US"},
		Organization:       []string{"Test Org"},
		OrganizationalUnit: []string{"Test Unit"},
		Locality:           []string{"Test City"},
		Province:           []string{"Test State"},
		StreetAddress:      []string{"123 Test St"},
		PostalCode:         []string{"12345"},
		SerialNumber:       "123456789",
		CommonName:         "test.example.com",
	}

	result := certificates.ConvertPkixName(name)

	if len(result.Country) != 1 || result.Country[0] != "US" {
		t.Errorf("Expected Country [US], got %v", result.Country)
	}

	if len(result.Organization) != 1 || result.Organization[0] != "Test Org" {
		t.Errorf("Expected Organization [Test Org], got %v", result.Organization)
	}

	if result.CommonName != "test.example.com" {
		t.Errorf("Expected CommonName test.example.com, got %s", result.CommonName)
	}

	if result.SerialNumber != "123456789" {
		t.Errorf("Expected SerialNumber 123456789, got %s", result.SerialNumber)
	}
}

func TestParseCertificateChain(t *testing.T) {
	t.Parallel()
	// Generate two test certificates
	privateKey1, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("Failed to generate private key 1: %v", err)
	}

	privateKey2, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("Failed to generate private key 2: %v", err)
	}

	template1 := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "cert1.example.com"},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Hour),
	}
	template2 := x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "cert2.example.com"},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Hour),
	}

	cert1DER, err := x509.CreateCertificate(rand.Reader, &template1, &template1, &privateKey1.PublicKey, privateKey1)
	if err != nil {
		t.Fatalf("Failed to create certificate 1: %v", err)
	}

	cert2DER, err := x509.CreateCertificate(rand.Reader, &template2, &template2, &privateKey2.PublicKey, privateKey2)
	if err != nil {
		t.Fatalf("Failed to create certificate 2: %v", err)
	}

	cert1PEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert1DER})
	cert2PEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert2DER})

	// Test with multiple certificates in PEM data
	chainPEM := string(cert1PEM) + string(cert2PEM)

	certs, err := certificates.ParseCertificateChain(context.Background(), chainPEM)
	if err != nil {
		t.Errorf("Expected successful chain parsing, got error: %v", err)
	} else if len(certs) != 2 {
		t.Errorf("Expected 2 certificates in chain, got %d", len(certs))
	}

	// Test with empty data
	_, err = certificates.ParseCertificateChain(context.Background(), "")
	if err == nil {
		t.Errorf("Expected error for empty PEM data")
	}
}
//nolint:funlen // This test function is intentionally long for comprehensive testing

// Integration test with actual certificate generation.
//nolint:funlen
func TestIntegrationCertificateGeneration(t *testing.T) {
	t.Parallel()
	// Generate a real certificate for testing
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("Failed to generate private key: %v", err)
	}

	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			Organization:  []string{"Test Organization"},
			Country:       []string{"US"},
			Province:      []string{"CA"},
			Locality:      []string{"San Francisco"},
			StreetAddress: []string{"123 Test St"},
			PostalCode:    []string{"94102"},
			CommonName:    "test.example.com",
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"test.example.com", "www.test.example.com"},
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1)},
		EmailAddresses:        []string{"admin@test.example.com"},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatalf("Failed to create certificate: %v", err)
	}

	// Convert to PEM
	certPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: certDER,
	})

	// Test our parsing function
	certInfo, err := certificates.ParseCertificateFromPEM(context.Background(), string(certPEM))
	if err != nil {
		t.Fatalf("Failed to parse generated certificate: %v", err)
	}

	// Verify parsed data
	if certInfo.Subject.CommonName != "test.example.com" {
		t.Errorf("Expected CommonName test.example.com, got %s", certInfo.Subject.CommonName)
	}

	if len(certInfo.Subject.Organization) != 1 || certInfo.Subject.Organization[0] != "Test Organization" {
		t.Errorf("Expected Organization [Test Organization], got %v", certInfo.Subject.Organization)
	}

	if len(certInfo.KeyUsage) == 0 {
		t.Errorf("Expected non-empty key usage")
	}

	if len(certInfo.ExtKeyUsage) == 0 {
		t.Errorf("Expected non-empty extended key usage")
	}

	if len(certInfo.SubjectAltNames) == 0 {
		t.Errorf("Expected non-empty subject alt names")
	}

	if certInfo.Fingerprints.SHA256 == "" {
		t.Errorf("Expected non-empty SHA256 fingerprint")
	}

	// Check that dates are valid
	if certInfo.NotBefore.IsZero() {
		t.Errorf("Expected valid NotBefore date")
	}

	if certInfo.NotAfter.IsZero() {
		t.Errorf("Expected valid NotAfter date")
	}

	if !certInfo.NotAfter.After(certInfo.NotBefore) {
		t.Errorf("Expected NotAfter to be after NotBefore")
	}
}

// Benchmark certificate parsing performance.
func BenchmarkParseCertificateFromPEM(b *testing.B) {
	// Generate a real certificate for benchmarking
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		b.Fatalf("Failed to generate private key: %v", err)
	}

	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			Organization: []string{"Benchmark Test"},
			CommonName:   "benchmark.example.com",
		},
		NotBefore:   time.Now(),
		NotAfter:    time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:    x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &privateKey.PublicKey, privateKey)
	if err != nil {
		b.Fatalf("Failed to create certificate: %v", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: certDER,
	})

	b.ResetTimer()

	for range b.N {
		_, err := certificates.ParseCertificateFromPEM(context.Background(), string(certPEM))
		if err != nil {
			b.Fatalf("Parse failed: %v", err)
		}
	}
}

func TestFetchCertificateFromEndpoint_InvalidAddress(t *testing.T) {
	t.Parallel()
	// Test with invalid addresses
	tests := []struct {
		name    string
		address string
	}{
		{"Empty address", ""},
		{"Invalid port", "example.com:abc"},
		{"Non-existent host", "non-existent-host-12345.com"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := certificates.FetchCertificateFromEndpoint(context.Background(), test.address, 5*time.Second)
			if err == nil {
				t.Errorf("Expected error for invalid address %s", test.address)
			}
		})
	}
}

// Test certificate chain parsing with multiple certificates.
func TestParseCertificateChainMultiple(t *testing.T) {
	t.Parallel()
	// Generate two certificates
	privateKey1, _ := rsa.GenerateKey(rand.Reader, 2048)
	privateKey2, _ := rsa.GenerateKey(rand.Reader, 2048)

	template1 := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "cert1.example.com"},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Hour),
	}

	template2 := x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "cert2.example.com"},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Hour),
	}

	cert1DER, _ := x509.CreateCertificate(rand.Reader, &template1, &template1, &privateKey1.PublicKey, privateKey1)
	cert2DER, _ := x509.CreateCertificate(rand.Reader, &template2, &template2, &privateKey2.PublicKey, privateKey2)

	cert1PEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert1DER})
	cert2PEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert2DER})

	chainPEM := string(cert1PEM) + string(cert2PEM)

	certs, err := certificates.ParseCertificateChain(context.Background(), chainPEM)
	if err != nil {
		t.Fatalf("Failed to parse certificate chain: %v", err)
	}

	if len(certs) != 2 {
		t.Errorf("Expected 2 certificates in chain, got %d", len(certs))
	}

	// Verify the certificates
	if certs[0].Subject.CommonName != "cert1.example.com" {
		t.Errorf("Expected first cert CN cert1.example.com, got %s", certs[0].Subject.CommonName)
	}

	if certs[1].Subject.CommonName != "cert2.example.com" {
		t.Errorf("Expected second cert CN cert2.example.com, got %s", certs[1].Subject.CommonName)
	}
}
