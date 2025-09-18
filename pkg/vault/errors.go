package vault

import "errors"

// Vault-related errors.
var (
	ErrNotAvailable           = errors.New("vault is not available")
	ErrInvalidResponse        = errors.New("invalid response from vault")
	ErrCouldNotRemarshalData  = errors.New("could not remarshal vault data")
	ErrIndexedValueMalformed  = errors.New("indexed value is malformed (not a real map)")
	ErrInstanceNotFound       = errors.New("instance not found in Vault")
	ErrRegistrationIDRequired = errors.New("registration ID is required")
	ErrRegistrationNotFound   = errors.New("registration not found")
	ErrSealedNoCredentials    = errors.New("vault sealed and no credentials available")
	ErrSealKeyNotFound        = errors.New("seal key not found in credentials file")
	ErrStillSealed            = errors.New("vault is still sealed after unseal attempt")
	ErrSecretMountMissing     = errors.New("secret mount is missing")
	ErrTooManyRedirects       = errors.New("stopped after 10 redirects")
	ErrIsSealed               = errors.New("vault is sealed")
	ErrKeyNotFoundInIndex     = errors.New("key not found in index")
	ErrInvalidInstanceData    = errors.New("instance data is not a valid map[string]interface{}")
)
