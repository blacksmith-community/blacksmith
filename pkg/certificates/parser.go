package certificates

import (
	"blacksmith/pkg/logger"
	"context"
	"crypto/md5"  // #nosec G501 - MD5 used only for certificate fingerprint identification, not security
	"crypto/sha1" // #nosec G505 - SHA1 used only for certificate fingerprint identification, not security
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"os/exec"
	"strings"
)

// ParseCertificateFromPEM parses a PEM-encoded certificate and returns detailed information.
func ParseCertificateFromPEM(ctx context.Context, pemData string) (*CertificateInfo, error) {
	block, _ := pem.Decode([]byte(pemData))
	if block == nil {
		return nil, ErrFailedToParsePEMBlock
	}

	if block.Type != CertificateBlockType {
		return nil, ErrPEMBlockIsNotCertificate
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse certificate: %w", err)
	}

	return ParseCertificateFromX509(ctx, cert, pemData)
}

// ParseCertificateFromX509 converts an x509.Certificate to CertificateInfo.
func ParseCertificateFromX509(ctx context.Context, cert *x509.Certificate, pemData string) (*CertificateInfo, error) {
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
	textDetails, err := GetOpenSSLTextOutput(ctx, pemData)
	if err != nil {
		logger.Get().Named("certificates").Error("Failed to get OpenSSL text output: %s", err)

		certInfo.TextDetails = "OpenSSL text output not available"
	} else {
		certInfo.TextDetails = textDetails
	}

	return certInfo, nil
}

// GetOpenSSLTextOutput executes OpenSSL to get the text representation of a certificate.
func GetOpenSSLTextOutput(ctx context.Context, pemData string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, OpensslTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "openssl", "x509", "-text", "-noout")
	cmd.Stdin = strings.NewReader(pemData)

	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to execute openssl command: %w", err)
	}

	return string(output), nil
}

// ParseCertificateChain parses a certificate chain from PEM data.
func ParseCertificateChain(ctx context.Context, pemData string) ([]*CertificateInfo, error) {
	var certificates []*CertificateInfo

	remaining := []byte(pemData)

	for len(remaining) > 0 {
		block, rest := pem.Decode(remaining)
		if block == nil {
			break
		}

		if block.Type != CertificateBlockType {
			remaining = rest

			continue
		}

		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("failed to parse certificate in chain: %w", err)
		}

		// Convert back to PEM for this individual certificate
		certPEM := pem.EncodeToMemory(block)

		certInfo, err := ParseCertificateFromX509(ctx, cert, string(certPEM))
		if err != nil {
			return nil, err
		}

		certificates = append(certificates, certInfo)
		remaining = rest
	}

	if len(certificates) == 0 {
		return nil, ErrNoValidCertificatesFoundInPEMData
	}

	return certificates, nil
}

// convertPkixName converts a pkix.Name to CertificateSubject.
func convertPkixName(name pkix.Name) CertificateSubject {
	names := make([]string, 0, len(name.Names))
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

// convertKeyUsage converts x509.KeyUsage to string slice.
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

// ConvertKeyUsage is a public wrapper for testing purposes.
func ConvertKeyUsage(usage x509.KeyUsage) []string {
	return convertKeyUsage(usage)
}

// ConvertExtKeyUsage is a public wrapper for testing purposes.
func ConvertExtKeyUsage(usage []x509.ExtKeyUsage) []string {
	return convertExtKeyUsage(usage)
}

// ExtractSubjectAltNames is a public wrapper for testing purposes.
func ExtractSubjectAltNames(cert *x509.Certificate) []string {
	return extractSubjectAltNames(cert)
}

// CalculateFingerprints is a public wrapper for testing purposes.
func CalculateFingerprints(certData []byte) CertificateFingerprints {
	return calculateFingerprints(certData)
}

// GetExtensionName is a public wrapper for testing purposes.
func GetExtensionName(oid string) string {
	return getExtensionName(oid)
}

// ConvertPkixName is a public wrapper for testing purposes.
func ConvertPkixName(name pkix.Name) CertificateSubject {
	return convertPkixName(name)
}

// convertExtKeyUsage converts x509.ExtKeyUsage to string slice.
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
		case x509.ExtKeyUsageAny:
			usages = append(usages, "any")
		case x509.ExtKeyUsageIPSECEndSystem:
			usages = append(usages, "ipsecEndSystem")
		case x509.ExtKeyUsageIPSECTunnel:
			usages = append(usages, "ipsecTunnel")
		case x509.ExtKeyUsageIPSECUser:
			usages = append(usages, "ipsecUser")
		case x509.ExtKeyUsageMicrosoftServerGatedCrypto:
			usages = append(usages, "microsoftServerGatedCrypto")
		case x509.ExtKeyUsageNetscapeServerGatedCrypto:
			usages = append(usages, "netscapeServerGatedCrypto")
		case x509.ExtKeyUsageMicrosoftCommercialCodeSigning:
			usages = append(usages, "microsoftCommercialCodeSigning")
		case x509.ExtKeyUsageMicrosoftKernelCodeSigning:
			usages = append(usages, "microsoftKernelCodeSigning")
		}
	}

	return usages
}

// extractSubjectAltNames extracts subject alternative names from the certificate.
func extractSubjectAltNames(cert *x509.Certificate) []string {
	sans := make([]string, 0, len(cert.DNSNames)+len(cert.IPAddresses)+len(cert.EmailAddresses)+len(cert.URIs))

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

// convertPublicKey extracts public key information.
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

// convertExtensions converts certificate extensions.
func convertExtensions(extensions []pkix.Extension) []CertificateExtension {
	exts := make([]CertificateExtension, 0, len(extensions))

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

// getExtensionName returns a human-readable name for extension OIDs.
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

// calculateFingerprints calculates certificate fingerprints.
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
