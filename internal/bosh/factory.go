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

// CreatePooledDirector creates a Director with connection pooling. The
// base director comes from Factory.New, so a zero config.MaxRetries or
// config.RequestTimeout takes the factory's default, and the pool logs to
// config.Logger.
func CreatePooledDirector(config Config, maxConnections int, timeout time.Duration) (*PooledDirector, error) {
	baseDirector, err := NewDefaultFactory().New(config)
	if err != nil {
		return nil, err
	}

	return NewPooledDirector(baseDirector, maxConnections, timeout, config.Logger), nil
}

// CreatePooledAndBatchDirectors creates both a PooledDirector for general use
// and a BatchDirector for batch upgrade operations. They share the same base
// director but have separate connection pools. Both follow the tasks they
// start until the tasks end, and their owner calls their Stop to end those
// watchers. taskWatchMax sets both followers' watch limit, which is how
// long a lost job-state answer holds its slot and how long a task is
// followed while its reads fail. Zero means the default of 6 hours. The
// base director comes from Factory.New, as for CreatePooledDirector, and
// both wrappers log to config.Logger.
func CreatePooledAndBatchDirectors(config Config, taskWatchMax time.Duration, maxConnections, maxBatchJobs int, timeout time.Duration) (*PooledDirector, *BatchDirector, error) {
	// Create base director (shared between both)
	baseDirector, err := NewDefaultFactory().New(config)
	if err != nil {
		return nil, nil, err
	}

	logger := config.Logger

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
