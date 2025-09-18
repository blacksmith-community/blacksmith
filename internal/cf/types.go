package cf

import (
	"sync"
	"time"

	"blacksmith/pkg/logger"
	"github.com/fivetwenty-io/capi/v3/pkg/capi"
)

// Constants for CF API operations.
const (
	DefaultCFPerPage     = 100
	DefaultCFTimeout     = 30 * time.Second
	DefaultCFLongTimeout = 15 * time.Second
)

// Manager manages connections to Cloud Foundry endpoints with resilience.
type Manager struct {
	endpoints map[string]CFAPIConfig
	clients   map[string]*EndpointClient
	mutex     sync.RWMutex
	logger    logger.Logger
}

// EndpointClient wraps a CF client with connection state and retry logic.
type EndpointClient struct {
	client        capi.Client
	config        CFAPIConfig
	isHealthy     bool
	lastError     error
	lastHealthy   time.Time
	retryCount    int
	nextRetryTime time.Time
	// Circuit breaker state
	consecutiveFailures int
	circuitOpen         bool
	circuitOpenTime     time.Time
	mutex               sync.RWMutex
	logger              logger.Logger
}

// GetConfig returns the config for testing purposes.
func (ec *EndpointClient) GetConfig() CFAPIConfig {
	return ec.config
}

// CFAPIConfig represents CF API endpoint configuration.
// This is imported from the main config package to maintain compatibility.
type CFAPIConfig struct {
	Name     string `yaml:"name"`     // Display name for the CF endpoint
	Endpoint string `yaml:"endpoint"` // CF API endpoint URL
	Username string `yaml:"username"` // CF API username
	Password string `yaml:"password"` // CF API password
}
