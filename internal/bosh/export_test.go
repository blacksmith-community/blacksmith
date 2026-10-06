package bosh

import (
	"time"

	boshdirector "github.com/cloudfoundry/bosh-cli/v7/director"
	"github.com/fivetwenty-io/boshapi/pkg/boshapi"
)

// CreateBasicFactoryConfig exposes createBasicFactoryConfig to package bosh_test.
func CreateBasicFactoryConfig(config Config) *boshdirector.FactoryConfig {
	return createBasicFactoryConfig(config)
}

// RequestTimeoutOf reports the request timeout an adapter runs with.
func RequestTimeoutOf(director Director) time.Duration {
	return director.(*DirectorAdapter).requestTimeout
}

// WithAPI replaces the boshapi client an adapter reads through.
func WithAPI(director Director, api boshapi.Client) {
	director.(*DirectorAdapter).api = api
}

// RequestTimeoutCause exposes the cause a request timeout carries to package
// bosh_test.
func RequestTimeoutCause(timeout time.Duration, attempts int) error {
	return &requestTimeoutError{timeout: timeout, attempts: attempts}
}
