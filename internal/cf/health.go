package cf

import (
	"context"
	"fmt"
	"time"

	"github.com/fivetwenty-io/capi/v3/pkg/capi"
)

// Circuit breaker and retry constants.
const (
	maxRetryAttempts       = 10
	baseRetryDelay         = 2 * time.Second
	maxRetryDelay          = 2 * time.Minute
	healthCheckInterval    = 30 * time.Second
	connectionTimeout      = 30 * time.Second
	circuitBreakerFailures = 3
	circuitBreakerTimeout  = 5 * time.Minute
)

// HealthCheck performs health checks on all CF endpoints.
func (m *Manager) HealthCheck(ctx context.Context) {
	m.mutex.RLock()
	clients := make([]*EndpointClient, 0, len(m.clients))

	for _, client := range m.clients {
		clients = append(clients, client)
	}

	m.mutex.RUnlock()

	for _, client := range clients {
		if client.IsHealthy() {
			// Test the connection
			_, err := client.client.GetInfo(ctx)
			if err != nil {
				client.markUnhealthy(fmt.Errorf("health check failed: %w", err))
			}
		} else if client.CanRetry() {
			// Try to reconnect
			_ = client.TryReconnect(ctx)
		}
	}
}

// StartHealthCheckLoop starts a background health check loop.
func (m *Manager) StartHealthCheckLoop(ctx context.Context) {
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

// GetStatus returns the current status of all CF endpoints.
func (m *Manager) GetStatus() map[string]interface{} {
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
			healthyCount, ok := status["healthy_endpoints"].(int)
			if !ok {
				healthyCount = 0
			}

			status["healthy_endpoints"] = healthyCount + 1
		}

		client.mutex.RUnlock()

		if endpoints, ok := status["endpoints"].(map[string]interface{}); ok {
			endpoints[name] = endpointStatus
		}
	}

	return status
}

// GetAppEnvironmentWithVCAP fetches an app's environment including VCAP_SERVICES
// This is used by the reconciler to recover missing service instance credentials.
func (m *Manager) GetAppEnvironmentWithVCAP(_ context.Context, appGUID string) (map[string]interface{}, error) {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	if len(m.clients) == 0 {
		return nil, ErrNoCFEndpointsConfigured
	}

	// Try each healthy endpoint
	for name, client := range m.clients {
		if !client.isHealthy {
			m.logger.Debug("Skipping unhealthy CF endpoint %s", name)

			continue
		}

		// For now, skip this endpoint - the CAPI v3 library doesn't expose raw HTTP client
		// This functionality needs to be reimplemented using the Apps().GetEnvironment() method
		// TODO: Use the appropriate CAPI v3 method when available
		_ = appGUID

		m.logger.Debug("GetAppEnvironmentWithVCAP not yet implemented for CAPI v3")

		continue
	}

	return nil, ErrFailedToGetAppEnvironmentFromAnyCFEndpoint
}

// FindAppsByServiceInstance finds apps bound to a specific service instance.
func (m *Manager) FindAppsByServiceInstance(ctx context.Context, serviceInstanceGUID string) ([]string, error) {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	if len(m.clients) == 0 {
		return nil, ErrNoCFEndpointsConfigured
	}

	var appGUIDs []string

	// Try each healthy endpoint
	for name, client := range m.clients {
		if !client.isHealthy {
			m.logger.Debug("Skipping unhealthy CF endpoint %s for service instance lookup", name)

			continue
		}

		// Get service bindings for this service instance
		bindingParams := capi.NewQueryParams().
			WithFilter("service_instance_guids", serviceInstanceGUID).
			WithPerPage(DefaultCFPerPage)

		bindingResponse, err := client.client.ServiceCredentialBindings().List(ctx, bindingParams)
		if err != nil {
			m.logger.Debug("Failed to get bindings for service instance %s from endpoint %s: %v",
				serviceInstanceGUID, name, err)

			continue
		}

		// Extract app GUIDs from bindings
		for _, binding := range bindingResponse.Resources {
			if binding.Relationships.App != nil && binding.Relationships.App.Data != nil {
				appGUIDs = append(appGUIDs, binding.Relationships.App.Data.GUID)
			}
		}

		if len(appGUIDs) > 0 {
			m.logger.Debug("Found %d apps bound to service instance %s from endpoint %s",
				len(appGUIDs), serviceInstanceGUID, name)

			return appGUIDs, nil
		}
	}

	m.logger.Debug("No apps found bound to service instance %s", serviceInstanceGUID)

	return appGUIDs, nil
}
