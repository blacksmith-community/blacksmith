package bosh

import (
	"errors"
	"fmt"
	"time"
)

// Constants for factory defaults.
const (
	defaultTimeout        = 30 * time.Minute
	defaultConnectTimeout = 30 * time.Second
	defaultMaxRetries     = 3
	defaultRetryInterval  = 5 * time.Second
)

// Static error variables to satisfy err113.
var (
	ErrUnexpectedDirectorType = errors.New("unexpected director type returned from NewDirectorAdapter")
	ErrCertAuthNotImplemented = errors.New("certificate authentication not yet implemented in the BOSH CLI adapter")
)

// Factory creates Director instances based on configuration.
type Factory struct {
}

// NewFactory creates a new Director factory.
func NewFactory() *Factory {
	return &Factory{}
}

// NewDefaultFactory creates a factory with the default adapter type
// This now always uses the BOSH CLI director implementation.
func NewDefaultFactory() *Factory {
	return &Factory{}
}

// New creates a new Director instance based on the factory configuration.
func (f *Factory) New(config Config) (*DirectorAdapter, error) {
	// Set defaults if not specified
	if config.Timeout == 0 {
		config.Timeout = defaultTimeout
	}

	if config.ConnectTimeout == 0 {
		config.ConnectTimeout = defaultConnectTimeout
	}

	if config.MaxRetries == 0 {
		config.MaxRetries = defaultMaxRetries
	}

	if config.RetryInterval == 0 {
		config.RetryInterval = defaultRetryInterval
	}

	// Always use the BOSH CLI director implementation
	director, err := NewDirectorAdapter(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create director adapter: %w", err)
	}
	// Type assert to *DirectorAdapter since we know NewDirectorAdapter returns this type
	adapter, ok := director.(*DirectorAdapter)
	if !ok {
		return nil, ErrUnexpectedDirectorType
	}

	return adapter, nil
}

// CreateDirectorFromLegacyConfig creates a Director from legacy Blacksmith config
// This is a convenience function for migrating from the old configuration.
func CreateDirectorFromLegacyConfig(address, username, password string, skipSSL bool) (*DirectorAdapter, error) {
	factory := NewDefaultFactory()

	config := Config{
		Address:           address,
		Username:          username,
		Password:          password,
		UAA:               nil,
		CACert:            "",
		ClientCert:        "",
		ClientKey:         "",
		SkipSSLValidation: skipSSL,
		Timeout:           0,
		ConnectTimeout:    0,
		MaxRetries:        0,
		RetryInterval:     0,
		EventLogger:       nil,
		Logger:            nil,
	}

	return factory.New(config)
}

// CreateDirectorWithLogger creates a Director with a logger for  logging.
func CreateDirectorWithLogger(address, username, password, caCert string, skipSSL bool, logger Logger) (*DirectorAdapter, error) {
	factory := NewDefaultFactory()

	config := Config{
		Address:           address,
		Username:          username,
		Password:          password,
		UAA:               nil,
		CACert:            caCert,
		ClientCert:        "",
		ClientKey:         "",
		SkipSSLValidation: skipSSL,
		Timeout:           0,
		ConnectTimeout:    0,
		MaxRetries:        0,
		RetryInterval:     0,
		EventLogger:       nil,
		Logger:            logger,
	}

	return factory.New(config)
}

// CreateDirectorWithUAA creates a Director with UAA authentication
// NOTE: UAA authentication support is available through the BOSH CLI adapter.
func CreateDirectorWithUAA(address string, uaaConfig *UAAConfig, skipSSL bool) (*DirectorAdapter, error) {
	factory := NewDefaultFactory()

	config := Config{
		Address:           address,
		Username:          "",
		Password:          "",
		UAA:               uaaConfig,
		CACert:            "",
		ClientCert:        "",
		ClientKey:         "",
		SkipSSLValidation: skipSSL,
		Timeout:           0,
		ConnectTimeout:    0,
		MaxRetries:        0,
		RetryInterval:     0,
		EventLogger:       nil,
		Logger:            nil,
	}

	return factory.New(config)
}

// CreateDirectorWithCertificate creates a Director with certificate authentication
// NOTE: Certificate authentication can be implemented in the director adapter.
func CreateDirectorWithCertificate(address, caCert, clientCert, clientKey string, skipSSL bool) (*DirectorAdapter, error) {
	return nil, ErrCertAuthNotImplemented
}

// CreatePooledDirector creates a Director with connection pooling.
func CreatePooledDirector(address, username, password, caCert string, skipSSL bool, maxConnections int, timeout time.Duration, logger Logger) (*PooledDirector, error) {
	// Create base director
	baseDirector, err := CreateDirectorWithLogger(address, username, password, caCert, skipSSL, logger)
	if err != nil {
		return nil, err
	}

	// Wrap with pooling
	return NewPooledDirector(baseDirector, maxConnections, timeout, logger), nil
}

// CreatePooledAndBatchDirectors creates both a PooledDirector for general use
// and a BatchDirector for batch upgrade operations. They share the same base
// director but have separate connection pools.
func CreatePooledAndBatchDirectors(address, username, password, caCert string, skipSSL bool, maxConnections, maxBatchJobs int, timeout time.Duration, logger Logger) (*PooledDirector, *BatchDirector, error) {
	// Create base director (shared between both)
	baseDirector, err := CreateDirectorWithLogger(address, username, password, caCert, skipSSL, logger)
	if err != nil {
		return nil, nil, err
	}

	// Create pooled director for general operations
	pooledDirector := NewPooledDirector(baseDirector, maxConnections, timeout, logger)

	// Create batch director for batch upgrade operations
	// Uses its own pool, bypassing the general pool for UpdateDeployment
	batchDirector := NewBatchDirector(baseDirector, maxBatchJobs, timeout, logger)

	return pooledDirector, batchDirector, nil
}
