package bosh

import (
	"errors"
	"fmt"
	"time"
)

// Constants for factory defaults.
const (
	// defaultMaxRetries is how many times a request that is safe to resend
	// is sent again when Config.MaxRetries is not set.
	defaultMaxRetries = 3
	// defaultRequestTimeout bounds one request to the director when
	// bosh.request_timeout is not set.
	defaultRequestTimeout = 60 * time.Second
)

// Static error variables to satisfy err113.
var (
	ErrUnexpectedDirectorType = errors.New("unexpected director type returned from NewDirectorAdapter")
)

// Factory creates Director instances based on configuration.
type Factory struct {
}

// NewDefaultFactory creates a factory.
func NewDefaultFactory() *Factory {
	return &Factory{}
}

// New creates a director adapter from config, with the defaults applied for
// the retries and the request timeout when they are not set.
func (f *Factory) New(config Config) (*DirectorAdapter, error) {
	if config.MaxRetries == 0 {
		config.MaxRetries = defaultMaxRetries
	}

	if config.RequestTimeout == 0 {
		config.RequestTimeout = defaultRequestTimeout
	}

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

// CreateDirectorWithLogger creates a Director that logs to logger. A
// zero requestTimeout means the default of 60 seconds.
func CreateDirectorWithLogger(address, username, password, caCert string, skipSSL bool, requestTimeout time.Duration, logger Logger) (*DirectorAdapter, error) {
	factory := NewDefaultFactory()

	config := Config{
		Address:           address,
		Username:          username,
		Password:          password,
		CACert:            caCert,
		SkipSSLValidation: skipSSL,
		MaxRetries:        0,
		RequestTimeout:    requestTimeout,
		Logger:            logger,
	}

	return factory.New(config)
}

// CreatePooledDirector creates a Director with connection pooling.
func CreatePooledDirector(address, username, password, caCert string, skipSSL bool, requestTimeout time.Duration, maxConnections int, timeout time.Duration, logger Logger) (*PooledDirector, error) {
	// Create base director
	baseDirector, err := CreateDirectorWithLogger(address, username, password, caCert, skipSSL, requestTimeout, logger)
	if err != nil {
		return nil, err
	}

	// Wrap with pooling
	return NewPooledDirector(baseDirector, maxConnections, timeout, logger), nil
}

// CreatePooledAndBatchDirectors creates both a PooledDirector for general use
// and a BatchDirector for batch upgrade operations. They share the same base
// director but have separate connection pools. Both follow the tasks they
// start until the tasks end, and their owner calls their Stop to end those
// watchers. taskWatchMax sets both followers' watch limit, which is how
// long a lost job-state answer holds its slot and how long a task is
// followed while its reads fail. Zero means the default of 6 hours.
func CreatePooledAndBatchDirectors(address, username, password, caCert string, skipSSL bool, requestTimeout, taskWatchMax time.Duration, maxConnections, maxBatchJobs int, timeout time.Duration, logger Logger) (*PooledDirector, *BatchDirector, error) {
	// Create base director (shared between both)
	baseDirector, err := CreateDirectorWithLogger(address, username, password, caCert, skipSSL, requestTimeout, logger)
	if err != nil {
		return nil, nil, err
	}

	// Create pooled director for general operations
	pooledDirector := NewPooledDirector(baseDirector, maxConnections, timeout, logger)

	// Create batch director for batch upgrade operations
	// Uses its own pool, bypassing the general pool for UpdateDeployment,
	// and holds each deploy's slot until the deploy's task ends
	batchDirector := NewBatchDirector(baseDirector, maxBatchJobs, timeout, logger)

	pooledDirector.follow.setWatchMax(taskWatchMax)
	batchDirector.follow.setWatchMax(taskWatchMax)

	return pooledDirector, batchDirector, nil
}
