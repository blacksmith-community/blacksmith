package rabbitmq

import (
	"fmt"
	"net/url"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"blacksmith/pkg/services/common"
)

// ConnectionManager manages RabbitMQ AMQP connections
type ConnectionManager struct {
	connections map[string]*amqp.Connection
	channels    map[string]*amqp.Channel
	mu          sync.RWMutex
	ttl         time.Duration
}

// NewConnectionManager creates a new RabbitMQ connection manager
func NewConnectionManager(ttl time.Duration) *ConnectionManager {
	if ttl == 0 {
		ttl = 5 * time.Minute // Default TTL
	}

	return &ConnectionManager{
		connections: make(map[string]*amqp.Connection),
		channels:    make(map[string]*amqp.Channel),
		ttl:         ttl,
	}
}

// GetConnection retrieves or creates a RabbitMQ connection and channel for the given instance
func (cm *ConnectionManager) GetConnection(instanceID string, creds *Credentials, useAMQPS bool) (*amqp.Connection, *amqp.Channel, error) {
	key := fmt.Sprintf("%s-%v", instanceID, useAMQPS)

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
		conn.Close()
		delete(cm.connections, key)
	}
	if ch, exists := cm.channels[key]; exists {
		ch.Close()
		delete(cm.channels, key)
	}
	cm.mu.Unlock()

	// Build connection URI
	uri := cm.buildURI(creds, useAMQPS)

	// Connect
	conn, err := amqp.Dial(uri)
	if err != nil {
		return nil, nil, common.NewRetryableError(
			fmt.Errorf("RabbitMQ connection failed: %w", err),
			true,
			5*time.Second,
		)
	}

	ch, err := conn.Channel()
	if err != nil {
		conn.Close()
		return nil, nil, common.NewRetryableError(
			fmt.Errorf("failed to open channel: %w", err),
			true,
			2*time.Second,
		)
	}

	// Cache connection and channel
	cm.mu.Lock()
	cm.connections[key] = conn
	cm.channels[key] = ch
	cm.mu.Unlock()

	return conn, ch, nil
}

// TestConnection tests the connection without caching
func (cm *ConnectionManager) TestConnection(creds *Credentials, useAMQPS bool) error {
	uri := cm.buildURI(creds, useAMQPS)

	conn, err := amqp.Dial(uri)
	if err != nil {
		return fmt.Errorf("connection failed: %w", err)
	}
	defer conn.Close()

	ch, err := conn.Channel()
	if err != nil {
		return fmt.Errorf("failed to open channel: %w", err)
	}
	defer ch.Close()

	// Test basic operation
	err = ch.ExchangeDeclare(
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

// buildURI constructs the AMQP connection URI
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

// CloseConnection closes and removes a specific connection
func (cm *ConnectionManager) CloseConnection(instanceID string, useAMQPS bool) {
	key := fmt.Sprintf("%s-%v", instanceID, useAMQPS)

	cm.mu.Lock()
	defer cm.mu.Unlock()

	if ch, exists := cm.channels[key]; exists {
		ch.Close()
		delete(cm.channels, key)
	}

	if conn, exists := cm.connections[key]; exists {
		conn.Close()
		delete(cm.connections, key)
	}
}

// CloseAll closes all RabbitMQ connections
func (cm *ConnectionManager) CloseAll() {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	for key, ch := range cm.channels {
		ch.Close()
		delete(cm.channels, key)
	}

	for key, conn := range cm.connections {
		conn.Close()
		delete(cm.connections, key)
	}
}

// CleanupStale removes connections that are no longer active
func (cm *ConnectionManager) CleanupStale() {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	for key, conn := range cm.connections {
		if conn.IsClosed() {
			delete(cm.connections, key)
			if ch, exists := cm.channels[key]; exists {
				ch.Close()
				delete(cm.channels, key)
			}
		}
	}
}

// GetConnectionInfo returns information about active connections
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
