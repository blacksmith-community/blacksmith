package certificates

import "time"

const (
	// Certificate parsing constants.
	CertificateBlockType = "CERTIFICATE"
	OpensslTimeout       = 30 * time.Second

	// API operation constants.
	DefaultCertFetchTimeout = 5 * time.Second
	MaxCertFetchTimeout     = 30 * time.Second
	MinCertMatchParts       = 2
)
