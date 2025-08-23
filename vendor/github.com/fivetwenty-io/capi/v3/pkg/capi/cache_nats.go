package capi

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// NATSKVCache implements Cache interface using NATS JetStream KV
type NATSKVCache struct {
	kv     jetstream.KeyValue
	bucket string
}

// NATSKVConfig configures NATS KV cache
type NATSKVConfig struct {
	// URL is the NATS server URL
	URL string

	// Bucket is the KV bucket name
	Bucket string

	// TTL is the default time-to-live for entries
	TTL time.Duration

	// MaxValueSize is the maximum size of a single value
	MaxValueSize int32

	// History is the number of historical values to keep
	History uint8

	// Replicas is the number of replicas for the KV store
	Replicas int

	// Token for authentication
	Token string

	// Username for authentication
	Username string

	// Password for authentication
	Password string

	// TLSConfig for TLS connection
	TLSCertFile string
	TLSKeyFile  string
	TLSCAFile   string
}

// DefaultNATSKVConfig returns default NATS KV configuration
func DefaultNATSKVConfig() *NATSKVConfig {
	return &NATSKVConfig{
		URL:          "nats://localhost:4222",
		Bucket:       "capi_cache",
		TTL:          5 * time.Minute,
		MaxValueSize: 1024 * 1024, // 1MB
		History:      1,
		Replicas:     1,
	}
}

// NewNATSKVCache creates a new NATS KV cache
func NewNATSKVCache(config *NATSKVConfig) (*NATSKVCache, error) {
	if config == nil {
		config = DefaultNATSKVConfig()
	}

	// Configure connection options
	opts := []nats.Option{
		nats.Name("capi-client"),
	}

	// Add authentication if provided
	if config.Token != "" {
		opts = append(opts, nats.Token(config.Token))
	} else if config.Username != "" && config.Password != "" {
		opts = append(opts, nats.UserInfo(config.Username, config.Password))
	}

	// Add TLS if configured
	if config.TLSCertFile != "" && config.TLSKeyFile != "" {
		opts = append(opts, nats.ClientCert(config.TLSCertFile, config.TLSKeyFile))
	}
	if config.TLSCAFile != "" {
		opts = append(opts, nats.RootCAs(config.TLSCAFile))
	}

	// Connect to NATS
	nc, err := nats.Connect(config.URL, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to NATS: %w", err)
	}

	// Create JetStream context
	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("failed to create JetStream context: %w", err)
	}

	// Create or get KV bucket
	kvConfig := jetstream.KeyValueConfig{
		Bucket:       config.Bucket,
		Description:  "Cloud Foundry API client cache",
		TTL:          config.TTL,
		MaxValueSize: config.MaxValueSize,
		History:      config.History,
		Replicas:     config.Replicas,
	}

	kv, err := js.CreateOrUpdateKeyValue(context.Background(), kvConfig)
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("failed to create/update KV bucket: %w", err)
	}

	return &NATSKVCache{
		kv:     kv,
		bucket: config.Bucket,
	}, nil
}

// cacheEntryWrapper wraps CacheEntry for JSON serialization
type cacheEntryWrapper struct {
	Data      []byte    `json:"data"`
	ExpiresAt time.Time `json:"expires_at"`
	ETag      string    `json:"etag,omitempty"`
}

// Get retrieves an item from the cache
func (c *NATSKVCache) Get(ctx context.Context, key string) (*CacheEntry, error) {
	// Get entry from KV store
	entry, err := c.kv.Get(ctx, key)
	if err != nil {
		if err == jetstream.ErrKeyNotFound {
			return nil, fmt.Errorf("key not found: %s", key)
		}
		return nil, fmt.Errorf("failed to get key: %w", err)
	}

	// Deserialize the entry
	var wrapper cacheEntryWrapper
	if err := json.Unmarshal(entry.Value(), &wrapper); err != nil {
		return nil, fmt.Errorf("failed to unmarshal cache entry: %w", err)
	}

	// Check if expired
	cacheEntry := &CacheEntry{
		Data:      wrapper.Data,
		ExpiresAt: wrapper.ExpiresAt,
		ETag:      wrapper.ETag,
	}

	if cacheEntry.IsExpired() {
		// Delete expired entry
		_ = c.kv.Delete(ctx, key)
		return nil, fmt.Errorf("entry expired: %s", key)
	}

	return cacheEntry, nil
}

// Set stores an item in the cache
func (c *NATSKVCache) Set(ctx context.Context, key string, entry *CacheEntry) error {
	// Serialize the entry
	wrapper := cacheEntryWrapper{
		Data:      entry.Data,
		ExpiresAt: entry.ExpiresAt,
		ETag:      entry.ETag,
	}

	data, err := json.Marshal(wrapper)
	if err != nil {
		return fmt.Errorf("failed to marshal cache entry: %w", err)
	}

	// Store in KV
	if _, err := c.kv.Put(ctx, key, data); err != nil {
		return fmt.Errorf("failed to put key: %w", err)
	}

	return nil
}

// Delete removes an item from the cache
func (c *NATSKVCache) Delete(ctx context.Context, key string) error {
	if err := c.kv.Delete(ctx, key); err != nil && err != jetstream.ErrKeyNotFound {
		return fmt.Errorf("failed to delete key: %w", err)
	}
	return nil
}

// Clear removes all items from the cache
func (c *NATSKVCache) Clear(ctx context.Context) error {
	// List all keys
	entries, err := c.kv.ListKeys(ctx)
	if err != nil {
		return fmt.Errorf("failed to list keys: %w", err)
	}

	// Delete each key
	for key := range entries.Keys() {
		if err := c.kv.Delete(ctx, key); err != nil && err != jetstream.ErrKeyNotFound {
			// Continue deleting other keys even if one fails
			continue
		}
	}

	return nil
}

// Has checks if a key exists in the cache
func (c *NATSKVCache) Has(ctx context.Context, key string) bool {
	entry, err := c.Get(ctx, key)
	if err != nil {
		return false
	}
	return !entry.IsExpired()
}

// Close closes the NATS connection
func (c *NATSKVCache) Close() error {
	// NATS KV doesn't have a direct close method
	// The connection is managed separately
	return nil
}

// NATSKVCacheWithConnection creates a NATS KV cache with an existing connection
func NATSKVCacheWithConnection(nc *nats.Conn, bucket string, ttl time.Duration) (*NATSKVCache, error) {
	// Create JetStream context
	js, err := jetstream.New(nc)
	if err != nil {
		return nil, fmt.Errorf("failed to create JetStream context: %w", err)
	}

	// Create or get KV bucket
	kvConfig := jetstream.KeyValueConfig{
		Bucket:       bucket,
		Description:  "Cloud Foundry API client cache",
		TTL:          ttl,
		MaxValueSize: 1024 * 1024, // 1MB
		History:      1,
		Replicas:     1,
	}

	kv, err := js.CreateOrUpdateKeyValue(context.Background(), kvConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create/update KV bucket: %w", err)
	}

	return &NATSKVCache{
		kv:     kv,
		bucket: bucket,
	}, nil
}
