package broker

import (
	"net/http"

	"github.com/fivetwenty-io/osbapi/v2/internal/server"
	"github.com/fivetwenty-io/osbapi/v2/pkg/osbapi"
)

// Option configures the broker handler.
type Option = server.Option

// NewHandler creates an http.Handler that serves all OSB API v2.17 endpoints.
// The provided ServiceBroker implementation handles the business logic.
func NewHandler(b osbapi.ServiceBroker, opts ...Option) http.Handler {
	return server.NewHandler(b, opts...)
}

// WithBasicAuth configures HTTP Basic Authentication for the handler.
func WithBasicAuth(username, password string) Option {
	return server.WithBasicAuth(username, password)
}

// WithLogger configures structured logging for the handler.
func WithLogger(logger osbapi.Logger) Option {
	return server.WithLogger(logger)
}

// WithMinAPIVersion sets the minimum required OSB API version.
// Defaults to "2.13" if not set.
func WithMinAPIVersion(version string) Option {
	return server.WithMinAPIVersion(version)
}
