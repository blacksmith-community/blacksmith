package redis

import (
	"context"
	"crypto/tls"
	"fmt"
	"sync"
	"time"

	"github.com/go-redis/redis/v8"

	"blacksmith/pkg/services/common"
)

// ClientManager manages Redis client connections
type ClientManager struct {
	clients map[string]*redis.Client
	mu      sync.RWMutex
	ttl     time.Duration
}

// NewClientManager creates a new Redis client manager
func NewClientManager(ttl time.Duration) *ClientManager {
	if ttl == 0 {
		ttl = 5 * time.Minute // Default TTL
	}

	return &ClientManager{
		clients: make(map[string]*redis.Client),
		ttl:     ttl,
	}
}

// GetClient retrieves or creates a Redis client for the given instance
func (cm *ClientManager) GetClient(instanceID string, creds *Credentials, useTLS bool) (*redis.Client, error) {
	return cm.GetClientWithTLSConfig(instanceID, creds, useTLS, false)
}

// GetClientWithTLSConfig retrieves or creates a Redis client with TLS verification control
func (cm *ClientManager) GetClientWithTLSConfig(instanceID string, creds *Credentials, useTLS bool, skipTLSVerify bool) (*redis.Client, error) {
	key := fmt.Sprintf("%s-%v", instanceID, useTLS)

	cm.mu.RLock()
	if client, exists := cm.clients[key]; exists {
		cm.mu.RUnlock()
		// Validate connection is still alive
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := client.Ping(ctx).Err(); err == nil {
			return client, nil
		}

		// Connection is dead, remove it
		cm.mu.Lock()
		if closeErr := client.Close(); closeErr != nil {
			// Log close error but continue cleanup
		}
		delete(cm.clients, key)
		cm.mu.Unlock()
	} else {
		cm.mu.RUnlock()
	}

	// Create new client
	opts := &redis.Options{
		Addr:         fmt.Sprintf("%s:%d", creds.Host, creds.Port),
		Password:     creds.Password,
		DB:           0,
		DialTimeout:  10 * time.Second,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		PoolSize:     10,
		MinIdleConns: 1,
		MaxRetries:   3,
	}

	// Configure TLS if requested and TLS port is available
	if useTLS && creds.TLSPort > 0 {
		opts.Addr = fmt.Sprintf("%s:%d", creds.Host, creds.TLSPort)
		opts.TLSConfig = &tls.Config{
			InsecureSkipVerify: skipTLSVerify, // #nosec G402 - Only skip verification when explicitly requested for development
			ServerName:         creds.Host,
			MinVersion:         tls.VersionTLS12,
		}
	} else if useTLS && creds.TLSURI != "" {
		// Try to use TLS URI if available
		tlsOpts, err := redis.ParseURL(creds.TLSURI)
		if err == nil {
			opts = tlsOpts
			opts.TLSConfig = &tls.Config{
				InsecureSkipVerify: skipTLSVerify, // #nosec G402 - Only skip verification when explicitly requested for development
				ServerName:         creds.Host,
				MinVersion:         tls.VersionTLS12,
			}
		}
	}

	client := redis.NewClient(opts)

	// Test connection
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		if closeErr := client.Close(); closeErr != nil {
			// Log close error but don't return it as it's secondary
		}
		return nil, common.NewRetryableError(
			fmt.Errorf("redis connection failed: %w", err),
			true,
			5*time.Second,
		)
	}

	// Cache client
	cm.mu.Lock()
	cm.clients[key] = client
	cm.mu.Unlock()

	return client, nil
}

// CloseClient closes and removes a specific client
func (cm *ClientManager) CloseClient(instanceID string, useTLS bool) {
	key := fmt.Sprintf("%s-%v", instanceID, useTLS)

	cm.mu.Lock()
	defer cm.mu.Unlock()

	if client, exists := cm.clients[key]; exists {
		if closeErr := client.Close(); closeErr != nil {
			// Log close error but continue cleanup
		}
		delete(cm.clients, key)
	}
}

// CloseAll closes all Redis clients
func (cm *ClientManager) CloseAll() {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	for key, client := range cm.clients {
		if closeErr := client.Close(); closeErr != nil {
			// Log close error but continue cleanup
		}
		delete(cm.clients, key)
	}
}

// CleanupStale removes clients that haven't been used recently
func (cm *ClientManager) CleanupStale() {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	for key, client := range cm.clients {
		// Try to ping the client
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		err := client.Ping(ctx).Err()
		cancel()

		if err != nil {
			if closeErr := client.Close(); closeErr != nil {
				// Log close error but continue cleanup
			}
			delete(cm.clients, key)
		}
	}
}

// GetConnectionInfo returns information about active connections
func (cm *ClientManager) GetConnectionInfo() map[string]ConnectionInfo {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	info := make(map[string]ConnectionInfo)

	for key, client := range cm.clients {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		err := client.Ping(ctx).Err()
		cancel()

		opts := client.Options()
		connected := err == nil

		info[key] = ConnectionInfo{
			Host:      opts.Addr,
			TLS:       opts.TLSConfig != nil,
			Database:  opts.DB,
			Connected: connected,
			LastPing:  time.Now(),
		}
	}

	return info
}
