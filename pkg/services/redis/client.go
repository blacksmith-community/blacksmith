package redis

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"sync"
	"time"

	"github.com/go-redis/redis/v8"

	"blacksmith/pkg/services/common"
)

// ClientManager manages Redis client connections.
type ClientManager struct {
	clients map[string]*redis.Client
	mu      sync.RWMutex
	ttl     time.Duration
}

// NewClientManager creates a new Redis client manager.
func NewClientManager(ttl time.Duration) *ClientManager {
	if ttl == 0 {
		ttl = DefaultTTL // Default TTL
	}

	return &ClientManager{
		clients: make(map[string]*redis.Client),
		ttl:     ttl,
	}
}

// Errors for connection settings the credentials cannot satisfy. None of
// them carries a credential value.
var (
	ErrNoPlainTextPort  = common.NewServiceError("redis", "E002", "this instance accepts TLS connections only; choose TLS to connect", false)
	ErrNoTLSEndpoint    = common.NewServiceError("redis", "E003", "this instance has no TLS port; choose Standard to connect", false)
	ErrInvalidTLSURI    = common.NewServiceError("redis", "E011", "tls_uri in the credentials could not be parsed", false)
	ErrInvalidCACertPEM = common.NewServiceError("redis", "E012", "ca_certificate in the credentials holds no PEM certificate", false)
)

// GetClient retrieves or creates a Redis client for the given instance.
func (cm *ClientManager) GetClient(ctx context.Context, instanceID string, creds *Credentials, useTLS bool) (*redis.Client, error) {
	key := fmt.Sprintf("%s-%v", instanceID, useTLS)

	cm.mu.RLock()

	if client, exists := cm.clients[key]; exists {
		cm.mu.RUnlock()
		// Validate connection is still alive
		pingCtx, cancel := context.WithTimeout(ctx, PingTimeout)
		defer cancel()

		err := client.Ping(pingCtx).Err()
		if err == nil {
			return client, nil
		}

		// Connection is dead, remove it
		cm.mu.Lock()

		_ = client.Close()

		delete(cm.clients, key)
		cm.mu.Unlock()
	} else {
		cm.mu.RUnlock()
	}

	client, err := cm.newClient(ctx, creds, useTLS)
	if err != nil {
		return nil, err
	}

	// Cache client
	cm.mu.Lock()
	cm.clients[key] = client
	cm.mu.Unlock()

	return client, nil
}

// clientOptions builds the client options for a plain-text or a TLS
// connection. It never falls back from one to the other: Standard needs a
// plain-text port and TLS needs a TLS port or tls_uri.
func clientOptions(creds *Credentials, useTLS bool) (*redis.Options, error) {
	opts := &redis.Options{
		Addr:         fmt.Sprintf("%s:%d", creds.Host, creds.Port),
		Username:     creds.Username,
		Password:     creds.Password,
		DB:           0,
		DialTimeout:  DialTimeout,
		ReadTimeout:  ReadTimeout,
		WriteTimeout: WriteTimeout,
		PoolSize:     DefaultPoolSize,
		MinIdleConns: 1,
		MaxRetries:   DefaultMaxRetries,
	}

	if !useTLS {
		if creds.Port <= 0 {
			return nil, ErrNoPlainTextPort
		}

		return opts, nil
	}

	switch {
	case creds.TLSPort > 0:
		opts.Addr = fmt.Sprintf("%s:%d", creds.Host, creds.TLSPort)
	case creds.TLSURI != "":
		// The parse error is dropped because it repeats the URI, password
		// included.
		tlsOpts, err := redis.ParseURL(creds.TLSURI)
		if err != nil {
			return nil, ErrInvalidTLSURI
		}

		opts = tlsOpts
		if opts.Username == "" {
			opts.Username = creds.Username
		}
	default:
		return nil, ErrNoTLSEndpoint
	}

	tlsConfig, err := creds.tlsConfig()
	if err != nil {
		return nil, err
	}

	opts.TLSConfig = tlsConfig

	return opts, nil
}

// tlsConfig verifies the server against the instance's own CA when the
// credentials carry ca_certificate, and against the system roots when they
// do not. The server name is the credentials' host, so an IP host has to
// appear as an IP SAN in the server certificate.
func (c *Credentials) tlsConfig() (*tls.Config, error) {
	config := &tls.Config{
		ServerName: c.Host,
		MinVersion: tls.VersionTLS12,
	}

	if c.CACertificate == "" {
		return config, nil
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(c.CACertificate)) {
		return nil, ErrInvalidCACertPEM
	}

	config.RootCAs = pool

	return config, nil
}

// CloseClient closes and removes a specific client.
func (cm *ClientManager) CloseClient(instanceID string, useTLS bool) {
	key := fmt.Sprintf("%s-%v", instanceID, useTLS)

	cm.mu.Lock()
	defer cm.mu.Unlock()

	if client, exists := cm.clients[key]; exists {
		_ = client.Close()

		delete(cm.clients, key)
	}
}

// CloseAll closes all Redis clients.
func (cm *ClientManager) CloseAll() {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	for key, client := range cm.clients {
		_ = client.Close()

		delete(cm.clients, key)
	}
}

// CleanupStale removes clients that haven't been used recently.
func (cm *ClientManager) CleanupStale() {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	for key, client := range cm.clients {
		// Try to ping the client
		ctx, cancel := context.WithTimeout(context.Background(), ShortContextTimeout)
		err := client.Ping(ctx).Err()

		cancel()

		if err != nil {
			_ = client.Close()

			delete(cm.clients, key)
		}
	}
}

// GetConnectionInfo returns information about active connections.
func (cm *ClientManager) GetConnectionInfo() map[string]ConnectionInfo {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	info := make(map[string]ConnectionInfo)

	for key, client := range cm.clients {
		ctx, cancel := context.WithTimeout(context.Background(), ShortContextTimeout)
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

// newClient creates an uncached Redis client and checks it with PING. The
// caller is responsible for closing it.
func (cm *ClientManager) newClient(ctx context.Context, creds *Credentials, useTLS bool) (*redis.Client, error) {
	opts, err := clientOptions(creds, useTLS)
	if err != nil {
		return nil, err
	}

	client := redis.NewClient(opts)

	// Test connection
	pingCtx, cancel := context.WithTimeout(ctx, LongPingTimeout)
	defer cancel()

	err = client.Ping(pingCtx).Err()
	if err != nil {
		_ = client.Close()

		return nil, common.NewRetryableError(
			fmt.Errorf("redis connection failed: %w", err),
			true,
			HealthCheckInterval,
		)
	}

	return client, nil
}
