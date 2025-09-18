package certificates

import (
	"crypto/x509"
	"encoding/pem"
	"fmt"
)

// ValidateCertificateFormat validates that the input is a valid PEM certificate.
func ValidateCertificateFormat(pemData string) error {
	block, _ := pem.Decode([]byte(pemData))
	if block == nil {
		return ErrInvalidPEMFormat
	}

	if block.Type != CertificateBlockType {
		return fmt.Errorf("%w, got: %s", ErrPEMBlockIsNotCertificateWithType, block.Type)
	}

	_, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return fmt.Errorf("invalid certificate data: %w", err)
	}

	return nil
}
