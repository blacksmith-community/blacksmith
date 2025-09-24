package cf

import (
	"errors"
	"sync"

	"blacksmith/pkg/logger"
)

// Static errors for err113 compliance.
var (
	ErrCFEndpointNotConfigured                    = errors.New("CF endpoint not configured")
	ErrCFEndpointCircuitBreakerOpen               = errors.New("CF endpoint circuit breaker is open")
	ErrCFEndpointUnhealthy                        = errors.New("CF endpoint is unhealthy")
	ErrNoCFEndpointsConfigured                    = errors.New("no CF endpoints configured")
	ErrFailedToGetAppEnvironmentFromAnyCFEndpoint = errors.New("failed to get app environment from any CF endpoint")
)

// ExternalCFAPIConfig represents the CF API config from the main package.
type ExternalCFAPIConfig struct {
	Name     string `yaml:"name"`     // Display name for the CF endpoint
	Endpoint string `yaml:"endpoint"` // CF API endpoint URL
	Username string `yaml:"username"` // CF API username
	Password string `yaml:"password"` // CF API password
}

// NewManagerFromExternal creates a new CF connection manager from external config.
func NewManagerFromExternal(endpoints map[string]ExternalCFAPIConfig, log logger.Logger) *Manager {
	// Convert external config to internal CFAPIConfig
	internalEndpoints := make(map[string]CFAPIConfig)
	for name, config := range endpoints {
		internalEndpoints[name] = CFAPIConfig(config)
	}

	return NewManager(internalEndpoints, log)
}

// NewManager creates a new CF connection manager.
func NewManager(endpoints map[string]CFAPIConfig, log logger.Logger) *Manager {
	if log == nil {
		// Create a default logger if none provided
		log = logger.Get().Named("cf-manager")
	}

	manager := &Manager{
		endpoints: endpoints,
		clients:   make(map[string]*EndpointClient),
		logger:    log,
	}

	// Initialize clients for configured endpoints
	manager.initializeClients()

	return manager
}

// GetClientCount returns the number of valid clients for testing purposes.
func (m *Manager) GetClientCount() int {
	return len(m.clients)
}

// GetClients returns the clients for testing purposes.
func (m *Manager) GetClients() []*EndpointClient {
	clients := make([]*EndpointClient, 0, len(m.clients))
	for _, client := range m.clients {
		clients = append(clients, client)
	}

	return clients
}

// IsCFManager implements the interfaces.CFManager interface.
func (m *Manager) IsCFManager() bool {
	return true
}

// initializeClients initializes CF clients for all configured endpoints.
func (m *Manager) initializeClients() {
	if len(m.endpoints) == 0 {
		m.logger.Debug("no CF endpoints configured")

		return
	}

	m.logger.Info("initializing CF clients for %d endpoints", len(m.endpoints))

	// Use a WaitGroup to wait for all connection attempts to complete
	var waitGroup sync.WaitGroup

	for name, config := range m.endpoints {
		if !m.validateEndpointCredentials(name, config) {
			continue
		}

		client := m.createEndpointClient(config)
		m.clients[name] = client

		// Increment the WaitGroup counter
		waitGroup.Add(1)

		// Start initial connection asynchronously
		go m.attemptInitialConnectionAsync(name, client, config, &waitGroup)
	}

	// Wait for all connection attempts to complete
	m.logger.Debug("waiting for initial connection attempts to complete...")
	waitGroup.Wait()

	m.logInitializationResult()
}

// validateEndpointCredentials validates that all required CF endpoint credentials are provided.
func (m *Manager) validateEndpointCredentials(name string, config CFAPIConfig) bool {
	missingFields := m.getMissingFields(config)
	if len(missingFields) > 0 {
		m.logger.Error("CF endpoint %s is missing required fields: %v", name, missingFields)

		return false
	}

	return true
}

// getMissingFields returns a list of missing required fields in the CF config.
func (m *Manager) getMissingFields(config CFAPIConfig) []string {
	var missing []string

	if config.Endpoint == "" {
		missing = append(missing, "endpoint")
	}

	if config.Username == "" {
		missing = append(missing, "username")
	}

	if config.Password == "" {
		missing = append(missing, "password")
	}

	return missing
}

// logInitializationResult logs the results of CF client initialization.
func (m *Manager) logInitializationResult() {
	healthyCount := 0

	for _, client := range m.clients {
		if client.IsHealthy() {
			healthyCount++
		}
	}

	m.logger.Info("CF client initialization complete: %d/%d endpoints healthy", healthyCount, len(m.clients))

	if healthyCount == 0 && len(m.clients) > 0 {
		m.logger.Warning("no CF endpoints are currently healthy - service instance discovery and CF integration will be unavailable")
	}
}
