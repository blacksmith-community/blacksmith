package certificates

import (
	"time"
)

// CertificateInfo represents detailed information about an X.509 certificate.
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

// CertificateSubject represents the subject or issuer of a certificate.
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

// CertificatePublicKey represents the public key information.
type CertificatePublicKey struct {
	Algorithm string `json:"algorithm"`
	Bits      int    `json:"bits"`
	Exponent  int64  `json:"exponent,omitempty"`
	Modulus   string `json:"modulus,omitempty"`
	Curve     string `json:"curve,omitempty"`
}

// CertificateExtension represents a certificate extension.
type CertificateExtension struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Critical bool   `json:"critical"`
	Value    string `json:"value"`
}

// CertificateFingerprints represents certificate fingerprints.
type CertificateFingerprints struct {
	MD5    string `json:"md5"`
	SHA1   string `json:"sha1"`
	SHA256 string `json:"sha256"`
}

// CertificateChainInfo represents basic information about certificates in the chain.
type CertificateChainInfo struct {
	Subject      string    `json:"subject"`
	Issuer       string    `json:"issuer"`
	SerialNumber string    `json:"serialNumber"`
	NotBefore    time.Time `json:"notBefore"`
	NotAfter     time.Time `json:"notAfter"`
}

// CertificateListItem represents a certificate in a list.
type CertificateListItem struct {
	Name    string          `json:"name"`
	Path    string          `json:"path,omitempty"`
	Details CertificateInfo `json:"details"`
}

// CertificateResponse represents the API response for certificate operations.
type CertificateResponse struct {
	Success bool                    `json:"success"`
	Data    CertificateResponseData `json:"data"`
	Error   string                  `json:"error,omitempty"`
}

// CertificateResponseData represents the data portion of certificate API responses.
type CertificateResponseData struct {
	Certificates []CertificateListItem `json:"certificates"`
	Metadata     CertificateMetadata   `json:"metadata"`
}

// CertificateMetadata represents metadata about the certificate source.
type CertificateMetadata struct {
	Source    string    `json:"source"`
	Timestamp time.Time `json:"timestamp"`
	Count     int       `json:"count"`
}

// CertificateFileItem represents a certificate file reference without parsing the content.
type CertificateFileItem struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// CertificateFileResponse represents the API response for certificate file listings.
type CertificateFileResponse struct {
	Success bool                `json:"success"`
	Data    CertificateFileData `json:"data"`
	Error   string              `json:"error,omitempty"`
}

// CertificateFileData represents the data portion of certificate file API responses.
type CertificateFileData struct {
	Files    []CertificateFileItem `json:"files"`
	Metadata CertificateMetadata   `json:"metadata"`
}
