package bosh

import boshdirector "github.com/cloudfoundry/bosh-cli/v7/director"

// CreateBasicFactoryConfig exposes createBasicFactoryConfig to package bosh_test.
func CreateBasicFactoryConfig(config Config) *boshdirector.FactoryConfig {
	return createBasicFactoryConfig(config)
}
