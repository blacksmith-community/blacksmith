package main

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/fivetwenty-io/capi/v3/pkg/capi"
	"github.com/fivetwenty-io/capi/v3/pkg/cfclient"
)

// CFConnectionManager manages connections to Cloud Foundry endpoints with resilience
type CFConnectionManager struct {
	endpoints map[string]CFAPIConfig
	clients   map[string]*CFEndpointClient
	mutex     sync.RWMutex
	logger    *Log
}

// CFEndpointClient wraps a CF client with connection state and retry logic
type CFEndpointClient struct {
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
	logger              *Log
}

// CFServiceInstanceMetadata contains CF-specific metadata for service instances
type CFServiceInstanceMetadata struct {
	ServiceName   string          `json:"service_name,omitempty"`
	OrgName       string          `json:"org_name,omitempty"`
	OrgGUID       string          `json:"org_guid,omitempty"`
	SpaceName     string          `json:"space_name,omitempty"`
	SpaceGUID     string          `json:"space_guid,omitempty"`
	Bindings      []CFBindingInfo `json:"bindings,omitempty"`
	LastCheckedAt time.Time       `json:"last_checked_at"`
	CheckError    string          `json:"check_error,omitempty"`
}

// CFBindingInfo contains information about service bindings
type CFBindingInfo struct {
	GUID    string `json:"guid"`
	Name    string `json:"name,omitempty"`
	AppGUID string `json:"app_guid,omitempty"`
	AppName string `json:"app_name,omitempty"`
	Type    string `json:"type,omitempty"`
}

const (
	maxRetryAttempts       = 5
	baseRetryDelay         = 2 * time.Second
	maxRetryDelay          = 5 * time.Minute
	healthCheckInterval    = 30 * time.Second
	connectionTimeout      = 30 * time.Second
	circuitBreakerFailures = 3
	circuitBreakerTimeout  = 2 * time.Minute
)

// NewCFConnectionManager creates a new CF connection manager
func NewCFConnectionManager(endpoints map[string]CFAPIConfig, logger *Log) *CFConnectionManager {
	if logger == nil {
		logger = Logger.Wrap("cf-manager")
	}

	manager := &CFConnectionManager{
		endpoints: endpoints,
		clients:   make(map[string]*CFEndpointClient),
		logger:    logger,
	}

	// Initialize clients for configured endpoints
	manager.initializeClients()

	return manager
}

// initializeClients creates client instances for all configured endpoints
func (m *CFConnectionManager) initializeClients() {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	if len(m.endpoints) == 0 {
		m.logger.Info("CF reconciliation disabled: no CF API endpoints configured")
		return
	}

	for name, config := range m.endpoints {
		if config.Endpoint == "" || config.Username == "" || config.Password == "" {
			m.logger.Error("CF endpoint '%s' has incomplete configuration (missing endpoint, username, or password)", name)
			continue
		}

		clientLogger := m.logger.Wrap("cf-client")
		client := &CFEndpointClient{
			config:     config,
			isHealthy:  false,
			retryCount: 0,
			logger:     clientLogger,
		}

		// Attempt initial connection
		if err := client.connect(context.Background()); err != nil {
			client.logger.Error("initial connection to CF endpoint '%s' failed: %v", name, err)
			client.markUnhealthy(err)
		} else {
			client.markHealthy()
			client.logger.Info("successfully connected to CF endpoint '%s' at %s", name, config.Endpoint)
		}

		m.clients[name] = client
	}

	if len(m.clients) == 0 {
		m.logger.Info("CF reconciliation disabled: no valid CF API endpoints available")
	} else {
		m.logger.Info("CF connection manager initialized with %d endpoint(s)", len(m.clients))
	}
}

// connect establishes a connection to the CF API
func (c *CFEndpointClient) connect(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, connectionTimeout)
	defer cancel()

	config := &capi.Config{
		APIEndpoint: c.config.Endpoint,
		Username:    c.config.Username,
		Password:    c.config.Password,
	}

	client, err := cfclient.New(config)
	if err != nil {
		return fmt.Errorf("failed to create CF client: %w", err)
	}

	// Test the connection by getting CF info
	_, err = client.GetInfo(ctx)
	if err != nil {
		return fmt.Errorf("failed to connect to CF API: %w", err)
	}

	c.client = client
	return nil
}

// markHealthy marks the client as healthy and resets retry state
func (c *CFEndpointClient) markHealthy() {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	c.isHealthy = true
	c.lastError = nil
	c.lastHealthy = time.Now()
	c.retryCount = 0
	c.nextRetryTime = time.Time{}

	// Reset circuit breaker state
	if c.circuitOpen {
		c.logger.Info("CF endpoint circuit breaker closed - connection restored")
	}
	c.consecutiveFailures = 0
	c.circuitOpen = false
	c.circuitOpenTime = time.Time{}
}

// markUnhealthy marks the client as unhealthy and schedules next retry
func (c *CFEndpointClient) markUnhealthy(err error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	c.isHealthy = false
	c.lastError = err
	c.retryCount++
	c.consecutiveFailures++

	// Check if circuit breaker should be opened
	if c.consecutiveFailures >= circuitBreakerFailures && !c.circuitOpen {
		c.circuitOpen = true
		c.circuitOpenTime = time.Now()
		c.logger.Error("CF endpoint circuit breaker opened after %d consecutive failures - blocking requests for %v",
			c.consecutiveFailures, circuitBreakerTimeout)
	}

	// Calculate exponential backoff delay
	delay := time.Duration(float64(baseRetryDelay) * math.Pow(2, float64(c.retryCount-1)))
	if delay > maxRetryDelay {
		delay = maxRetryDelay
	}

	c.nextRetryTime = time.Now().Add(delay)

	c.logger.Error("CF endpoint marked unhealthy (attempt %d/%d, failures: %d): %v. Next retry in %v",
		c.retryCount, maxRetryAttempts, c.consecutiveFailures, err, delay)
}

// IsHealthy returns whether the client is currently healthy
func (c *CFEndpointClient) IsHealthy() bool {
	c.mutex.RLock()
	defer c.mutex.RUnlock()
	return c.isHealthy
}

// CanRetry returns whether the client can attempt a retry
func (c *CFEndpointClient) CanRetry() bool {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	if c.isHealthy {
		return false
	}

	if c.retryCount >= maxRetryAttempts {
		return false
	}

	return time.Now().After(c.nextRetryTime)
}

// TryReconnect attempts to reconnect to the CF endpoint
func (c *CFEndpointClient) TryReconnect(ctx context.Context) error {
	if !c.CanRetry() {
		c.mutex.RLock()
		err := c.lastError
		c.mutex.RUnlock()
		return fmt.Errorf("cannot retry connection: %w", err)
	}

	c.logger.Info("attempting to reconnect to CF endpoint (attempt %d/%d)", c.retryCount+1, maxRetryAttempts)

	if err := c.connect(ctx); err != nil {
		c.markUnhealthy(err)
		return err
	}

	c.markHealthy()
	c.logger.Info("successfully reconnected to CF endpoint")
	return nil
}

// GetClient returns the CF client if healthy, otherwise attempts reconnection
func (m *CFConnectionManager) GetClient(endpointName string) (capi.Client, error) {
	m.mutex.RLock()
	client, exists := m.clients[endpointName]
	m.mutex.RUnlock()

	if !exists {
		return nil, fmt.Errorf("CF endpoint '%s' not configured", endpointName)
	}

	// Check circuit breaker state
	client.mutex.RLock()
	if client.circuitOpen {
		// Check if circuit breaker timeout has passed
		if time.Since(client.circuitOpenTime) < circuitBreakerTimeout {
			client.mutex.RUnlock()
			return nil, fmt.Errorf("CF endpoint '%s' is circuit breaker open - requests blocked for %v more",
				endpointName, circuitBreakerTimeout-time.Since(client.circuitOpenTime))
		}
		// Circuit breaker timeout passed, allow one test request
		client.mutex.RUnlock()
	} else {
		client.mutex.RUnlock()
	}

	if client.IsHealthy() {
		return client.client, nil
	}

	// Try to reconnect if possible
	ctx, cancel := context.WithTimeout(context.Background(), connectionTimeout)
	defer cancel()

	if err := client.TryReconnect(ctx); err != nil {
		return nil, fmt.Errorf("CF endpoint '%s' is unhealthy and reconnection failed: %w", endpointName, err)
	}

	return client.client, nil
}

// GetHealthyClients returns all healthy CF clients
func (m *CFConnectionManager) GetHealthyClients() map[string]capi.Client {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	healthy := make(map[string]capi.Client)
	for name, client := range m.clients {
		if client.IsHealthy() {
			healthy[name] = client.client
		}
	}

	return healthy
}

// EnrichServiceInstanceWithCF attempts to enrich service instance metadata with CF information
func (m *CFConnectionManager) EnrichServiceInstanceWithCF(instanceID, serviceName string) *CFServiceInstanceMetadata {
	if len(m.clients) == 0 {
		m.logger.Debug("skipping CF enrichment for instance %s: no CF endpoints configured", instanceID)
		return nil
	}

	metadata := &CFServiceInstanceMetadata{
		LastCheckedAt: time.Now(),
	}

	// Try each healthy CF endpoint to find the service instance
	for endpointName, client := range m.GetHealthyClients() {
		cfMetadata, err := m.findServiceInstanceInCF(client, instanceID, serviceName, endpointName)
		if err != nil {
			m.logger.Debug("failed to find service instance %s in CF endpoint %s: %v", instanceID, endpointName, err)
			metadata.CheckError = fmt.Sprintf("endpoint %s: %v", endpointName, err)
			continue
		}

		if cfMetadata != nil {
			m.logger.Info("successfully enriched service instance %s with CF metadata from endpoint %s", instanceID, endpointName)
			return cfMetadata
		}
	}

	m.logger.Debug("service instance %s not found in any healthy CF endpoint", instanceID)
	return metadata
}

// findServiceInstanceInCF searches for a service instance across all orgs and spaces in a CF endpoint
func (m *CFConnectionManager) findServiceInstanceInCF(client capi.Client, instanceID, serviceName, endpointName string) (*CFServiceInstanceMetadata, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Get all organizations
	orgParams := capi.NewQueryParams().WithPerPage(100)
	orgResponse, err := client.Organizations().List(ctx, orgParams)
	if err != nil {
		return nil, fmt.Errorf("failed to list organizations: %w", err)
	}

	// Search through organizations and spaces
	for _, org := range orgResponse.Resources {
		spaceParams := capi.NewQueryParams().WithFilter("organization_guids", org.GUID).WithPerPage(100)
		spaceResponse, err := client.Spaces().List(ctx, spaceParams)
		if err != nil {
			m.logger.Debug("failed to list spaces for org %s: %v", org.Name, err)
			continue
		}

		for _, space := range spaceResponse.Resources {
			siParams := capi.NewQueryParams().WithFilter("space_guids", space.GUID).WithPerPage(100)
			siResponse, err := client.ServiceInstances().List(ctx, siParams)
			if err != nil {
				m.logger.Debug("failed to list service instances for space %s: %v", space.Name, err)
				continue
			}

			for _, si := range siResponse.Resources {
				// Match by name or ID (depending on how the matching works)
				if si.Name == instanceID || si.GUID == instanceID || si.Name == serviceName {
					// Found the service instance, get bindings
					bindings := m.getServiceBindings(client, si.GUID)

					return &CFServiceInstanceMetadata{
						ServiceName:   si.Name,
						OrgName:       org.Name,
						OrgGUID:       org.GUID,
						SpaceName:     space.Name,
						SpaceGUID:     space.GUID,
						Bindings:      bindings,
						LastCheckedAt: time.Now(),
					}, nil
				}
			}
		}
	}

	return nil, nil // Not found, but no error
}

// getServiceBindings retrieves bindings for a service instance
func (m *CFConnectionManager) getServiceBindings(client capi.Client, serviceInstanceGUID string) []CFBindingInfo {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	bindingParams := capi.NewQueryParams().WithFilter("service_instance_guids", serviceInstanceGUID).WithPerPage(100)
	bindingResponse, err := client.ServiceCredentialBindings().List(ctx, bindingParams)
	if err != nil {
		m.logger.Debug("failed to get bindings for service instance %s: %v", serviceInstanceGUID, err)
		return nil
	}

	var bindings []CFBindingInfo
	for _, binding := range bindingResponse.Resources {
		bindingInfo := CFBindingInfo{
			GUID: binding.GUID,
			Name: binding.Name,
			Type: binding.Type,
		}

		// If it's an app binding, try to get app details
		if binding.Relationships.App != nil {
			bindingInfo.AppGUID = binding.Relationships.App.Data.GUID
			// You could fetch app name here if needed
		}

		bindings = append(bindings, bindingInfo)
	}

	return bindings
}

// HealthCheck performs health checks on all CF endpoints
func (m *CFConnectionManager) HealthCheck(ctx context.Context) {
	m.mutex.RLock()
	clients := make([]*CFEndpointClient, 0, len(m.clients))
	for _, client := range m.clients {
		clients = append(clients, client)
	}
	m.mutex.RUnlock()

	for _, client := range clients {
		if client.IsHealthy() {
			// Test the connection
			if _, err := client.client.GetInfo(ctx); err != nil {
				client.markUnhealthy(fmt.Errorf("health check failed: %w", err))
			}
		} else if client.CanRetry() {
			// Try to reconnect
			if err := client.TryReconnect(ctx); err != nil {
				// Error already logged in TryReconnect, no need to log again
			}
		}
	}
}

// StartHealthCheckLoop starts a background health check loop
func (m *CFConnectionManager) StartHealthCheckLoop(ctx context.Context) {
	ticker := time.NewTicker(healthCheckInterval)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.HealthCheck(ctx)
			}
		}
	}()
}

// GetStatus returns the current status of all CF endpoints
func (m *CFConnectionManager) GetStatus() map[string]interface{} {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	status := map[string]interface{}{
		"enabled":           len(m.endpoints) > 0,
		"total_endpoints":   len(m.endpoints),
		"healthy_endpoints": 0,
		"endpoints":         make(map[string]interface{}),
	}

	for name, client := range m.clients {
		client.mutex.RLock()
		endpointStatus := map[string]interface{}{
			"healthy":      client.isHealthy,
			"last_healthy": client.lastHealthy,
			"retry_count":  client.retryCount,
			"next_retry":   client.nextRetryTime,
		}

		if client.lastError != nil {
			endpointStatus["last_error"] = client.lastError.Error()
		}

		if client.isHealthy {
			status["healthy_endpoints"] = status["healthy_endpoints"].(int) + 1
		}

		client.mutex.RUnlock()
		status["endpoints"].(map[string]interface{})[name] = endpointStatus
	}

	return status
}
