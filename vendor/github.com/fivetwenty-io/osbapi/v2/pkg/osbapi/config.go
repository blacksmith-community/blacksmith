package osbapi

import "time"

// ClientConfig represents configuration for building an OSB API client.
type ClientConfig struct {
	// URL is the base URL of the service broker (e.g., "https://broker.example.com").
	URL string

	// Username is the HTTP Basic Auth username.
	Username string

	// Password is the HTTP Basic Auth password.
	Password string

	// APIVersion overrides the default OSB API version header.
	// If empty, defaults to "2.17".
	APIVersion string

	// HTTPTimeout is the timeout for HTTP requests.
	// If zero, a default timeout is used.
	HTTPTimeout time.Duration

	// Logger is an optional structured logger.
	Logger Logger

	// UserAgent overrides the default User-Agent header.
	UserAgent string

	// Verbose enables debug logging of HTTP requests/responses.
	Verbose bool
}

// Logger interface for structured logging.
type Logger interface {
	Debug(msg string, fields map[string]any)
	Info(msg string, fields map[string]any)
	Warn(msg string, fields map[string]any)
	Error(msg string, fields map[string]any)
}

// PollConfig configures polling behavior for async operations.
type PollConfig struct {
	// Interval between poll requests. Defaults to 5 seconds.
	Interval time.Duration

	// Timeout is the maximum time to wait. Defaults to 30 minutes.
	Timeout time.Duration
}
