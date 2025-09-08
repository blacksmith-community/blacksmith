package main

import "errors"

// Common static errors to replace dynamic error creation
var (
	// Vault-related errors
	ErrVaultNotAvailable            = errors.New("vault is not available")
	ErrVaultInvalidResponse         = errors.New("invalid response from vault")
	ErrVaultCouldNotRemarshalData   = errors.New("could not remarshal vault data")
	ErrVaultIndexedValueMalformed   = errors.New("indexed value is malformed (not a real map)")
	ErrVaultInstanceNotFound        = errors.New("instance not found in Vault")
	ErrVaultRegistrationIDRequired  = errors.New("registration ID is required")
	ErrVaultRegistrationNotFound    = errors.New("registration not found")
	ErrVaultSealedNoCredentials     = errors.New("vault sealed and no credentials available")
	ErrVaultSealKeyNotFound         = errors.New("seal key not found in credentials file")
	ErrVaultStillSealed            = errors.New("vault is still sealed after unseal attempt")
	ErrVaultSecretMountMissing     = errors.New("secret mount is missing")
	ErrVaultTooManyRedirects       = errors.New("stopped after 10 redirects")
	ErrVaultIsSealed               = errors.New("vault is sealed")

	// Reconciler-related errors
	ErrNoDataFoundAtPath           = errors.New("no data found at path")
	ErrVaultConnectionFailed       = errors.New("vault connection failed")
	ErrSyncFailure                 = errors.New("sync failure")
	ErrDeploymentNotFound          = errors.New("deployment not found")
	ErrEmptyServiceID              = errors.New("empty service_id")
	ErrEmptyPlanID                 = errors.New("empty plan_id")
	ErrInvalidUUIDFormat           = errors.New("invalid UUID format")
	ErrMissingCredentials          = errors.New("missing credentials")
	ErrNotFound                    = errors.New("not found")

	// VM Monitor-related errors
	ErrUnexpectedDataType          = errors.New("unexpected data type in Put for db")
	ErrDirectorNonSuccessfulStatus = errors.New("Director responded with non-successful status code")

	// Vault Index-related errors
	ErrKeyNotFoundInIndex = errors.New("key not found in index")
)