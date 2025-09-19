package rabbitmq

import (
	"fmt"
	"net/url"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"blacksmith/pkg/services/common"
)

// ConnectionManager manages RabbitMQ AMQP connections.
type ConnectionManager struct {
	connections map[string]*amqp.Connection
	channels    map[string]*amqp.Channel
	mu          sync.RWMutex
	ttl         time.Duration
}

// NewConnectionManager creates a new RabbitMQ connection manager.
func NewConnectionManager(ttl time.Duration) *ConnectionManager {
	if ttl == 0 {
		ttl = DefaultTTL // Default TTL
	}

	return &ConnectionManager{
		connections: make(map[string]*amqp.Connection),
		channels:    make(map[string]*amqp.Channel),
		ttl:         ttl,
	}
}

// GetConnection retrieves or creates a RabbitMQ connection and channel for the given instance.
func (cm *ConnectionManager) GetConnection(instanceID string, creds *Credentials, useAMQPS bool) (*amqp.Connection, *amqp.Channel, error) {
	// Create default connection options for backward compatibility
	opts := common.ConnectionOptions{
		UseAMQPS: useAMQPS,
		Timeout:  HTTPTimeout,
	}

	return cm.GetConnectionWithOptions(instanceID, creds, opts)
}

// GetConnectionWithOptions retrieves or creates a RabbitMQ connection with connection options.
//
//nolint:funlen
func (cm *ConnectionManager) GetConnectionWithOptions(instanceID string, creds *Credentials, opts common.ConnectionOptions) (*amqp.Connection, *amqp.Channel, error) {
	// Create key that includes override parameters to separate different connection types
	key := fmt.Sprintf("%s-%v-%s-%s", instanceID, opts.UseAMQPS, opts.OverrideUser, opts.OverrideVHost)

	cm.mu.RLock()

	if conn, exists := cm.connections[key]; exists {
		if ch, exists := cm.channels[key]; exists {
			cm.mu.RUnlock()
			// Check connection health
			if !conn.IsClosed() && !ch.IsClosed() {
				return conn, ch, nil
			}
		}
	}

	cm.mu.RUnlock()

	// Clean up stale connections
	cm.mu.Lock()

	if conn, exists := cm.connections[key]; exists {
		_ = conn.Close()

		delete(cm.connections, key)
	}

	if ch, exists := cm.channels[key]; exists {
		_ = ch.Close()

		delete(cm.channels, key)
	}

	cm.mu.Unlock()

	// Build connection URI with overrides if provided
	var uri string

	if opts.OverrideUser != "" || opts.OverridePassword != "" || opts.OverrideVHost != "" {
		// Create a copy of credentials with overrides applied
		testCreds := *creds
		if opts.OverrideUser != "" {
			testCreds.Username = opts.OverrideUser
		}

		if opts.OverridePassword != "" {
			testCreds.Password = opts.OverridePassword
		}

		if opts.OverrideVHost != "" {
			testCreds.VHost = opts.OverrideVHost
		}
		// Clear protocol URIs when using overrides to force manual URI building
		testCreds.Protocols = nil
		testCreds.URI = ""
		testCreds.TLSURI = ""
		uri = cm.buildURI(&testCreds, opts.UseAMQPS)
	} else {
		uri = cm.buildURI(creds, opts.UseAMQPS)
	}

	// Connect
	conn, err := amqp.Dial(uri)
	if err != nil {
		return nil, nil, common.NewRetryableError(
			fmt.Errorf("RabbitMQ connection failed: %w", err),
			true,
			HealthCheckInterval,
		)
	}

	channel, err := conn.Channel()
	if err != nil {
		_ = conn.Close()

		const channelRetryDelaySeconds = 2

		return nil, nil, common.NewRetryableError(
			fmt.Errorf("failed to open channel: %w", err),
			true,
			channelRetryDelaySeconds*time.Second,
		)
	}

	// Cache connection and channel
	cm.mu.Lock()
	cm.connections[key] = conn
	cm.channels[key] = channel
	cm.mu.Unlock()

	return conn, channel, nil
}

// TestConnection tests the connection without caching.
func (cm *ConnectionManager) TestConnection(creds *Credentials, useAMQPS bool) error {
	uri := cm.buildURI(creds, useAMQPS)

	conn, err := amqp.Dial(uri)
	if err != nil {
		return fmt.Errorf("connection failed: %w", err)
	}

	defer func() { _ = conn.Close() }()

	channel, err := conn.Channel()
	if err != nil {
		return fmt.Errorf("failed to open channel: %w", err)
	}

	defer func() { _ = channel.Close() }()

	// Test basic operation
	err = channel.ExchangeDeclare(
		"blacksmith.test", // name
		"direct",          // type
		false,             // durable
		true,              // auto-delete
		false,             // internal
		false,             // no-wait
		nil,               // arguments
	)
	if err != nil {
		return fmt.Errorf("failed to declare test exchange: %w", err)
	}

	return nil
}

// TestConnectionWithOverrides tests connection with optional user/vhost overrides.
func (cm *ConnectionManager) TestConnectionWithOverrides(creds *Credentials, useAMQPS bool, overrideUser, overridePassword, overrideVHost string) error {
	// Create a copy of credentials with overrides applied
	testCreds := *creds

	if overrideUser != "" {
		testCreds.Username = overrideUser
	}

	if overridePassword != "" {
		testCreds.Password = overridePassword
	}

	if overrideVHost != "" {
		testCreds.VHost = overrideVHost
	}

	// CRITICAL: Clear protocol URIs when using overrides to force manual URI building
	// Otherwise buildURI will use the original protocol URI and ignore our overrides
	if overrideUser != "" || overridePassword != "" || overrideVHost != "" {
		testCreds.Protocols = nil
		testCreds.URI = ""
		testCreds.TLSURI = ""
	}

	uri := cm.buildURI(&testCreds, useAMQPS)

	conn, err := amqp.Dial(uri)
	if err != nil {
		return fmt.Errorf("connection failed: %w", err)
	}

	defer func() { _ = conn.Close() }()

	channel, err := conn.Channel()
	if err != nil {
		return fmt.Errorf("failed to open channel: %w", err)
	}

	defer func() { _ = channel.Close() }()

	// Test basic operation
	err = channel.ExchangeDeclare(
		"blacksmith.test", // name
		"direct",          // type
		false,             // durable
		true,              // auto-delete
		false,             // internal
		false,             // no-wait
		nil,               // arguments
	)
	if err != nil {
		return fmt.Errorf("failed to declare test exchange: %w", err)
	}

	return nil
}

// CloseConnection closes and removes a specific connection.
func (cm *ConnectionManager) CloseConnection(instanceID string, useAMQPS bool) {
	key := fmt.Sprintf("%s-%v", instanceID, useAMQPS)

	cm.mu.Lock()
	defer cm.mu.Unlock()

	if ch, exists := cm.channels[key]; exists {
		_ = ch.Close()

		delete(cm.channels, key)
	}

	if conn, exists := cm.connections[key]; exists {
		_ = conn.Close()

		delete(cm.connections, key)
	}
}

// CloseAll closes all RabbitMQ connections.
func (cm *ConnectionManager) CloseAll() {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	for key, ch := range cm.channels {
		_ = ch.Close()

		delete(cm.channels, key)
	}

	for key, conn := range cm.connections {
		_ = conn.Close()

		delete(cm.connections, key)
	}
}

// CleanupStale removes connections that are no longer active.
func (cm *ConnectionManager) CleanupStale() {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	for key, conn := range cm.connections {
		if conn.IsClosed() {
			delete(cm.connections, key)

			if ch, exists := cm.channels[key]; exists {
				_ = ch.Close()

				delete(cm.channels, key)
			}
		}
	}
}

// GetConnectionInfo returns information about active connections.
func (cm *ConnectionManager) GetConnectionInfo() map[string]ConnectionInfo {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	info := make(map[string]ConnectionInfo)

	for key, conn := range cm.connections {
		connected := !conn.IsClosed()

		info[key] = ConnectionInfo{
			Connected: connected,
			LastPing:  time.Now(),
		}
	}

	return info
}

// buildURI constructs the AMQP connection URI.
func (cm *ConnectionManager) buildURI(creds *Credentials, useAMQPS bool) string {
	protocol := "amqp"
	if useAMQPS {
		protocol = "amqps"
	}

	// Check for protocol-specific URIs first
	if creds.Protocols != nil {
		if proto, ok := creds.Protocols[protocol]; ok {
			if uri, ok := proto["uri"].(string); ok && uri != "" {
				return uri
			}
		}
	}

	// Check for direct URI fields
	if useAMQPS && creds.TLSURI != "" {
		return creds.TLSURI
	}

	if !useAMQPS && creds.URI != "" {
		return creds.URI
	}

	// Build URI manually
	vhost := creds.VHost
	if vhost == "" {
		vhost = "/"
	}

	port := creds.Port
	if useAMQPS && creds.TLSPort > 0 {
		port = creds.TLSPort
	} else if useAMQPS {
		port = 5671 // Default AMQPS port
	}

	return fmt.Sprintf("%s://%s:%s@%s:%d/%s",
		protocol,
		url.QueryEscape(creds.Username),
		url.QueryEscape(creds.Password),
		creds.Host,
		port,
		url.QueryEscape(vhost))
}
