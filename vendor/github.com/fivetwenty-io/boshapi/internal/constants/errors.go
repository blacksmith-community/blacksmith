package constants

import "errors"

// API and configuration errors.
var (
	ErrNoDirectorEndpoint = errors.New("no BOSH Director endpoint provided")
)
