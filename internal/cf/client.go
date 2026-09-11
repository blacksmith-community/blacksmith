package cf

import (
	"context"
	"fmt"
	"math"
	"os"
	"sync"
	"time"

	"blacksmith/pkg/logger"
	"github.com/fivetwenty-io/capi/v3/pkg/capi"
	"github.com/fivetwenty-io/capi/v3/pkg/cfclient"
)

// capiDevModeEnv is the environment variable the capi library requires before
// it honours Config.SkipTLSVerify; without it the client refuses to start.
const capiDevModeEnv = "CAPI_DEV_MODE"

// connect establishes a connection to the CF endpoint.
func (c *EndpointClient) connect(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, connectionTimeout)
	defer cancel()

	client, err := cfclient.New(ctx, c.capiConfig())
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

// capiConfig translates the endpoint configuration into a capi client config.
// A configured CA bundle keeps verification enabled while trusting a private
// CA; skip_ssl_validation disables verification entirely.
func (c *EndpointClient) capiConfig() *capi.Config {
	return &capi.Config{
		APIEndpoint:   c.config.Endpoint,
		Username:      c.config.Username,
		Password:      c.config.Password,
		CACertPEM:     c.config.CACert,
		SkipTLSVerify: c.config.SkipSSLValidation,
	}
}

// allowInsecureTLS opens the capi library's development-only gate so that
// SkipTLSVerify takes effect. The gate is process wide, but capi only skips
// verification for clients whose own config asks for it, so endpoints that
// leave skip_ssl_validation false are still verified.
func allowInsecureTLS(log logger.Logger) {
	log.Warn("TLS certificate verification is disabled for this CF endpoint; configure cacert instead outside development")

	if os.Getenv(capiDevModeEnv) != "" {
		return
	}

	err := os.Setenv(capiDevModeEnv, "true")
	if err != nil {
		log.Error("failed to set %s, skip_ssl_validation will not take effect: %v", capiDevModeEnv, err)
	}
}

// markHealthy marks the client as healthy and resets retry state.
func (c *EndpointClient) markHealthy() {
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

// markUnhealthy marks the client as unhealthy and schedules next retry.
func (c *EndpointClient) markUnhealthy(err error) {
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
	const exponentialBackoffBase = 2

	delay := time.Duration(float64(baseRetryDelay) * math.Pow(exponentialBackoffBase, float64(c.retryCount-1)))
	if delay > maxRetryDelay {
		delay = maxRetryDelay
	}

	c.nextRetryTime = time.Now().Add(delay)

	c.logger.Error("CF endpoint marked unhealthy (attempt %d/%d, failures: %d): %v. Next retry in %v",
		c.retryCount, maxRetryAttempts, c.consecutiveFailures, err, delay)
}

// IsHealthy returns whether the client is currently healthy.
func (c *EndpointClient) IsHealthy() bool {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	return c.isHealthy
}

// CanRetry returns whether the client can attempt a retry.
func (c *EndpointClient) CanRetry() bool {
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

// TryReconnect attempts to reconnect to the CF endpoint.
func (c *EndpointClient) TryReconnect(ctx context.Context) error {
	if !c.CanRetry() {
		c.mutex.RLock()
		err := c.lastError
		c.mutex.RUnlock()

		return fmt.Errorf("cannot retry connection: %w", err)
	}

	c.logger.Info("attempting to reconnect to CF endpoint (attempt %d/%d)", c.retryCount+1, maxRetryAttempts)

	err := c.connect(ctx)
	if err != nil {
		c.markUnhealthy(err)

		return err
	}

	c.markHealthy()
	c.logger.Info("successfully reconnected to CF endpoint")

	return nil
}

// GetClient returns the CF client if healthy, otherwise attempts reconnection
//
//nolint:ireturn // capi.Client is an external interface from github.com/fivetwenty-io/capi
func (m *Manager) GetClient(endpointName string) (interface{}, error) {
	m.mutex.RLock()
	client, exists := m.clients[endpointName]
	m.mutex.RUnlock()

	if !exists {
		return nil, fmt.Errorf("%w: %s", ErrCFEndpointNotConfigured, endpointName)
	}

	// Check circuit breaker state
	client.mutex.RLock()

	if client.circuitOpen {
		// Check if circuit breaker timeout has passed
		if time.Since(client.circuitOpenTime) < circuitBreakerTimeout {
			client.mutex.RUnlock()

			return nil, fmt.Errorf("CF endpoint '%s' is circuit breaker open - requests blocked for %v more: %w",
				endpointName, circuitBreakerTimeout-time.Since(client.circuitOpenTime), ErrCFEndpointCircuitBreakerOpen)
		}
		// Circuit breaker timeout passed, allow one test request
		client.mutex.RUnlock()
	} else {
		client.mutex.RUnlock()
	}

	if client.IsHealthy() {
		return client.client, nil
	}

	// Don't attempt reconnection during request processing - fail fast
	// Reconnection attempts are handled by the background health check loop
	return nil, fmt.Errorf("CF endpoint '%s' is currently unhealthy: %w", endpointName, ErrCFEndpointUnhealthy)
}

// GetHealthyClients returns all healthy CF clients.
func (m *Manager) GetHealthyClients() map[string]capi.Client {
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

// createEndpointClient creates a new CF endpoint client.
func (m *Manager) createEndpointClient(config CFAPIConfig) *EndpointClient {
	logger := m.logger.Named("cf-client-" + config.Name)

	if config.SkipSSLValidation {
		allowInsecureTLS(logger)
	}

	return &EndpointClient{
		config: config,
		logger: logger,
	}
}

// attemptInitialConnectionAsync tries to connect to a CF endpoint asynchronously during initialization.
func (m *Manager) attemptInitialConnectionAsync(name string, client *EndpointClient, _ CFAPIConfig, wg *sync.WaitGroup) {
	defer wg.Done() // Signal completion when this function returns

	m.logger.Debug("starting async connection attempt for CF endpoint: %s", name)

	ctx, cancel := context.WithTimeout(context.Background(), connectionTimeout)
	defer cancel()

	err := client.connect(ctx)
	if err != nil {
		m.handleConnectionError(name, client, err)

		return
	}

	client.markHealthy()
	m.logger.Info("successfully connected to CF endpoint: %s", name)
}

// handleConnectionError handles connection errors during initialization.
func (m *Manager) handleConnectionError(name string, client *EndpointClient, err error) {
	client.markUnhealthy(err)
	m.logger.Error("failed to connect to CF endpoint %s: %v", name, err)

	// Don't block startup for CF connection failures
	m.logger.Debug("CF endpoint %s will be retried in background health checks", name)
}
