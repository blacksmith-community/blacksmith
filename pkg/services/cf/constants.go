package cf

import "time"

// Timeout Constants.
const (
	DefaultHTTPTimeout    = 30 * time.Second
	AuthContextTimeout    = 30 * time.Second
	RefreshContextTimeout = 30 * time.Second
)

// Validation Constants.
const (
	MinServiceNameLength = 3
	MaxServiceNameLength = 50
	MinBrokerNameLength  = 3
	MaxBrokerNameLength  = 30
	MinUsernameLength    = 2
	MaxUsernameLength    = 100
	MinPasswordLength    = 6
	MaxPasswordLength    = 200
)
