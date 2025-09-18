package vault

import "time"

// Constants for Vault operations.
const (
	CredentialsFilePermissions = 0600
	DefaultTimeout             = 30 * time.Second
	HistoryMaxSize             = 50
	UnsealCheckInterval        = 15 * time.Second
)
