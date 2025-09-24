package vault

import "time"

// Constants for Vault operations.
const (
	CredentialsFilePermissions  = 0600
	DefaultTimeout              = 30 * time.Second
	DefaultHistoryRetentionDays = 30
	HistoryMaxSize              = 1000
	UnsealCheckInterval         = 15 * time.Second
)
