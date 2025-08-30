package bosh

import (
	"fmt"
	"time"
)

// Factory creates Director instances based on configuration
type Factory struct {
}

// NewFactory creates a new Director factory
func NewFactory() *Factory {
	return &Factory{}
}

// NewDefaultFactory creates a factory with the default adapter type
// This now always uses the BOSH CLI director implementation
func NewDefaultFactory() *Factory {
	return &Factory{}
}

// New creates a new Director instance based on the factory configuration
func (f *Factory) New(config Config) (Director, error) {
	// Set defaults if not specified
	if config.Timeout == 0 {
		config.Timeout = 30 * time.Minute
	}
	if config.ConnectTimeout == 0 {
		config.ConnectTimeout = 30 * time.Second
	}
	if config.MaxRetries == 0 {
		config.MaxRetries = 3
	}
	if config.RetryInterval == 0 {
		config.RetryInterval = 5 * time.Second
	}

	// Always use the BOSH CLI director implementation
	return NewDirectorAdapter(config)
}

// CreateDirectorFromLegacyConfig creates a Director from legacy Blacksmith config
// This is a convenience function for migrating from the old configuration
func CreateDirectorFromLegacyConfig(address, username, password string, skipSSL bool) (Director, error) {
	factory := NewDefaultFactory()

	config := Config{
		Address:           address,
		Username:          username,
		Password:          password,
		SkipSSLValidation: skipSSL,
	}

	return factory.New(config)
}

// CreateDirectorWithLogger creates a Director with a logger for  logging
func CreateDirectorWithLogger(address, username, password, caCert string, skipSSL bool, logger Logger) (Director, error) {
	factory := NewDefaultFactory()

	config := Config{
		Address:           address,
		Username:          username,
		Password:          password,
		CACert:            caCert,
		SkipSSLValidation: skipSSL,
		Logger:            logger,
	}

	return factory.New(config)
}

// CreateDirectorWithUAA creates a Director with UAA authentication
// NOTE: UAA authentication support is available through the BOSH CLI adapter
func CreateDirectorWithUAA(address string, uaaConfig *UAAConfig, skipSSL bool) (Director, error) {
	factory := NewDefaultFactory()

	config := Config{
		Address:           address,
		UAA:               uaaConfig,
		SkipSSLValidation: skipSSL,
	}

	return factory.New(config)
}

// CreateDirectorWithCertificate creates a Director with certificate authentication
// NOTE: Certificate authentication can be implemented in the director adapter
func CreateDirectorWithCertificate(address, caCert, clientCert, clientKey string, skipSSL bool) (Director, error) {
	return nil, fmt.Errorf("certificate authentication not yet implemented in the BOSH CLI adapter")
}

// CreatePooledDirector creates a Director with connection pooling
func CreatePooledDirector(address, username, password, caCert string, skipSSL bool, maxConnections int, timeout time.Duration, logger Logger) (Director, error) {
	// Create base director
	baseDirector, err := CreateDirectorWithLogger(address, username, password, caCert, skipSSL, logger)
	if err != nil {
		return nil, err
	}

	// Wrap with pooling
	return NewPooledDirector(baseDirector, maxConnections, timeout, logger), nil
}
