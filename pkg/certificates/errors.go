package certificates

import "errors"

// Static errors for err113 compliance.
var (
	// Parser errors.
	ErrFailedToParsePEMBlock             = errors.New("failed to parse PEM block")
	ErrPEMBlockIsNotCertificate          = errors.New("PEM block is not a certificate")
	ErrConnectionIsNotTLS                = errors.New("connection is not a TLS connection")
	ErrNoCertificatesFoundForAddress     = errors.New("no certificates found for address")
	ErrInvalidPEMFormat                  = errors.New("invalid PEM format")
	ErrPEMBlockIsNotCertificateWithType  = errors.New("PEM block is not a certificate")
	ErrNoValidCertificatesFoundInPEMData = errors.New("no valid certificates found in PEM data")

	// API errors.
	ErrFileDoesNotContainCertificate = errors.New("file does not contain a certificate")
)
