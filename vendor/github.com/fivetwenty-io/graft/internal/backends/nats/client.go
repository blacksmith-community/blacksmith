package natsbackend

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// PooledConnection represents a pooled NATS connection with metadata.
type PooledConnection struct {
	Conn     *nats.Conn
	JS       jetstream.JetStream
	LastUsed time.Time
	RefCount int
}

// ConnectionPool manages pooled NATS connections.
type ConnectionPool struct {
	mu          sync.RWMutex
	connections map[string]*PooledConnection
	stopCleanup chan struct{}
}

// NewConnectionPool creates a new connection pool.
func NewConnectionPool() *ConnectionPool {
	return &ConnectionPool{
		connections: make(map[string]*PooledConnection),
		stopCleanup: make(chan struct{}),
	}
}

// Connection pool settings.
var (
	PoolMaxIdleTime     = 5 * time.Minute
	PoolCleanupInterval = 1 * time.Minute
)

// ConnPool is the global NATS connection pool.
var ConnPool = NewConnectionPool()

// CleanupLoop periodically removes idle connections.
func (p *ConnectionPool) CleanupLoop() {
	ticker := time.NewTicker(PoolCleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			p.Cleanup()
		case <-p.stopCleanup:
			return
		}
	}
}

// Cleanup removes idle connections from the pool.
func (p *ConnectionPool) Cleanup() {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := time.Now()
	for key, pc := range p.connections {
		if pc.RefCount == 0 && now.Sub(pc.LastUsed) > PoolMaxIdleTime {
			pc.Conn.Close()
			delete(p.connections, key)
			debugLogf("closed idle NATS connection to %s", key)
		}
	}
}

// GetConnection retrieves or creates a pooled connection.
func (p *ConnectionPool) GetConnection(config *Config) (*PooledConnection, error) {
	key := config.URL

	p.mu.Lock()
	defer p.mu.Unlock()

	// Check if we have an existing connection
	if pc, ok := p.connections[key]; ok {
		if pc.Conn.IsConnected() {
			pc.RefCount++
			pc.LastUsed = time.Now()
			return pc, nil
		}
		// Connection is dead, remove it
		delete(p.connections, key)
	}

	// Create new connection with retry logic
	conn, js, err := CreateConnectionWithRetry(config)
	if err != nil {
		return nil, err
	}

	pc := &PooledConnection{
		Conn:     conn,
		JS:       js,
		LastUsed: time.Now(),
		RefCount: 1,
	}

	p.connections[key] = pc
	debugLogf("created new NATS connection to %s", key)

	return pc, nil
}

// ReleaseConnection decrements the reference count.
func (p *ConnectionPool) ReleaseConnection(config *Config) {
	p.mu.Lock()
	defer p.mu.Unlock()

	key := config.URL
	if pc, ok := p.connections[key]; ok {
		pc.RefCount--
		pc.LastUsed = time.Now()
	}
}

// ConnectionCount returns the number of connections in the pool.
func (p *ConnectionPool) ConnectionCount() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.connections)
}

// StopCleanup signals the cleanup goroutine to stop.
func (p *ConnectionPool) StopCleanup() {
	close(p.stopCleanup)
}

// CloseAll closes all connections in the pool.
func (p *ConnectionPool) CloseAll() {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, pc := range p.connections {
		if pc.Conn != nil {
			pc.Conn.Close()
		}
	}
	p.connections = make(map[string]*PooledConnection)
}

// ClientPool manages NATS connections for different targets.
type ClientPool struct {
	mu          sync.RWMutex
	connections map[string]*PooledConnection
	configs     map[string]*Target
}

// DefaultPool is the global client pool for target-aware NATS connections.
var DefaultPool = &ClientPool{
	connections: make(map[string]*PooledConnection),
	configs:     make(map[string]*Target),
}

// GetConnection returns a NATS connection for the specified target. The
// cache-hit fast path takes the write lock (not RLock): it mutates the
// shared *PooledConnection's RefCount/LastUsed fields, and RLock only
// guarantees the map itself is not concurrently written, not that other
// RLock holders cannot race on values reached through it.
func (ncp *ClientPool) GetConnection(targetName string) (*PooledConnection, error) {
	ncp.mu.Lock()
	if conn, exists := ncp.connections[targetName]; exists {
		conn.RefCount++
		conn.LastUsed = time.Now()
		ncp.mu.Unlock()
		return conn, nil
	}
	ncp.mu.Unlock()

	// Get target configuration
	config, err := ncp.GetTargetConfig(targetName)
	if err != nil {
		return nil, fmt.Errorf("NATS target '%s' not found: %w", targetName, err)
	}

	// Create NATS configuration from target config
	cfg := configFromTarget(config)

	// Create new connection
	conn, err := CreateConnectionFromConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create NATS connection for target '%s': %w", targetName, err)
	}

	pooledConn := &PooledConnection{
		Conn:     conn,
		LastUsed: time.Now(),
		RefCount: 1,
	}

	// Create JetStream context
	js, err := jetstream.New(conn)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to create JetStream context for target '%s': %w", targetName, err)
	}
	pooledConn.JS = js

	// Store for reuse, but re-check under the write lock first: another
	// goroutine racing this same cold target may have already stored its
	// own connection while this one was still connecting - op_nats.go
	// calls GetConnection from computeOp, so two nats@target operators in
	// the same scheduler wave can race a cold pool. If so, converge onto
	// the existing winner and close this goroutine's
	// now-redundant connection instead of silently overwriting the map
	// entry, which would leave it unreachable from CloseAll and never
	// closed - a real leaked TCP connection, not just wasted work.
	return ncp.storeOrDiscard(targetName, pooledConn, config, conn.Close), nil
}

// storeOrDiscard stores candidate as the pooled connection for targetName,
// unless another goroutine already stored one first, in which case it
// closes candidate's connection via closeLoser and returns the existing
// winner (with its RefCount incremented for this caller, matching the
// fast-path hit in GetConnection - every successful return here pairs
// with exactly one later ReleaseConnection call). Extracted as its own
// method, taking a closeLoser callback instead of calling
// candidate.Conn.Close() directly, so the race-convergence logic is
// testable without a real NATS connection.
func (ncp *ClientPool) storeOrDiscard(targetName string, candidate *PooledConnection, config *Target, closeLoser func()) *PooledConnection {
	ncp.mu.Lock()
	if existing, exists := ncp.connections[targetName]; exists {
		existing.RefCount++
		existing.LastUsed = time.Now()
		ncp.mu.Unlock()
		closeLoser()
		return existing
	}
	ncp.connections[targetName] = candidate
	ncp.configs[targetName] = config
	ncp.mu.Unlock()
	return candidate
}

// GetTargetConfig retrieves target configuration from environment variables.
func (ncp *ClientPool) GetTargetConfig(targetName string) (*Target, error) {
	// Check if we have cached config
	ncp.mu.RLock()
	if config, exists := ncp.configs[targetName]; exists {
		ncp.mu.RUnlock()
		return config, nil
	}
	ncp.mu.RUnlock()

	// Use environment variables with target suffix
	envPrefix := fmt.Sprintf("NATS_%s_", strings.ToUpper(targetName))

	// Check if the URL environment variable is set (required for target configurations)
	urlEnvVar := envPrefix + "URL"
	url := os.Getenv(urlEnvVar)
	if url == "" {
		return nil, fmt.Errorf("NATS target '%s' configuration incomplete (expected %s environment variable)",
			targetName, urlEnvVar)
	}

	config := &Target{
		URL:                url,
		Timeout:            parseDurationOrDefault(getEnvOrDefault(envPrefix+"TIMEOUT", "5s"), 5*time.Second),
		Retries:            parseIntOrDefault(getEnvOrDefault(envPrefix+"RETRIES", "3"), 3),
		RetryInterval:      parseDurationOrDefault(getEnvOrDefault(envPrefix+"RETRY_INTERVAL", "1s"), 1*time.Second),
		RetryBackoff:       ParseFloatOrDefault(getEnvOrDefault(envPrefix+"RETRY_BACKOFF", "2.0"), 2.0),
		MaxRetryInterval:   parseDurationOrDefault(getEnvOrDefault(envPrefix+"MAX_RETRY_INTERVAL", "30s"), 30*time.Second),
		TLS:                parseBoolOrDefault(getEnvOrDefault(envPrefix+"TLS", "false")),
		CertFile:           getEnvOrDefault(envPrefix+"CERT_FILE", ""),
		KeyFile:            getEnvOrDefault(envPrefix+"KEY_FILE", ""),
		CAFile:             getEnvOrDefault(envPrefix+"CA_FILE", ""),
		InsecureSkipVerify: parseBoolOrDefault(getEnvOrDefault(envPrefix+"INSECURE_SKIP_VERIFY", "false")),
		CacheTTL:           parseDurationOrDefault(getEnvOrDefault(envPrefix+"CACHE_TTL", "5m"), 5*time.Minute),
		StreamingThreshold: ParseInt64OrDefault(getEnvOrDefault(envPrefix+"STREAMING_THRESHOLD", "10485760"), 10*1024*1024),
		AuditLogging:       parseBoolOrDefault(getEnvOrDefault(envPrefix+"AUDIT_LOGGING", "false")),
		Token:              os.Getenv(envPrefix + "TOKEN"),
		User:               os.Getenv(envPrefix + "USER"),
		Password:           os.Getenv(envPrefix + "PASSWORD"),
		NkeySeedFile:       os.Getenv(envPrefix + "NKEY"),
		CredsFile:          os.Getenv(envPrefix + "CREDS"),
	}

	return config, nil
}

// configFromTarget copies every field of a resolved *Target (as produced
// by GetTargetConfig) into the *Config shape BuildConnectionOptions and
// CreateConnectionFromConfig consume. It exists as its own function,
// rather than inlined at GetConnection's one call site, so tests can
// verify the field-by-field copy directly - Target and Config are
// separate, independently-editable types (Target carries YAML tags for
// file-based config, Config does not), so a field added to one and
// missed in this mapping would silently vanish with no compiler error.
func configFromTarget(t *Target) *Config {
	return &Config{
		URL:                t.URL,
		Timeout:            t.Timeout,
		Retries:            t.Retries,
		RetryInterval:      t.RetryInterval,
		RetryBackoff:       t.RetryBackoff,
		MaxRetryInterval:   t.MaxRetryInterval,
		TLS:                t.TLS,
		CertFile:           t.CertFile,
		KeyFile:            t.KeyFile,
		CAFile:             t.CAFile,
		InsecureSkipVerify: t.InsecureSkipVerify,
		CacheTTL:           t.CacheTTL,
		StreamingThreshold: t.StreamingThreshold,
		AuditLogging:       t.AuditLogging,
		Token:              t.Token,
		User:               t.User,
		Password:           t.Password,
		NkeySeedFile:       t.NkeySeedFile,
		CredsFile:          t.CredsFile,
	}
}

// ReleaseConnection decreases the reference count for a target connection.
func (ncp *ClientPool) ReleaseConnection(targetName string) {
	ncp.mu.Lock()
	defer ncp.mu.Unlock()

	if conn, exists := ncp.connections[targetName]; exists {
		conn.RefCount--
		if conn.RefCount <= 0 {
			// Connection no longer in use, but keep it cached for reuse
			conn.RefCount = 0
		}
	}
}

// CloseAll closes all target pool connections.
func (ncp *ClientPool) CloseAll() {
	ncp.mu.Lock()
	defer ncp.mu.Unlock()

	for _, pc := range ncp.connections {
		if pc.Conn != nil {
			pc.Conn.Close()
		}
	}
	ncp.connections = make(map[string]*PooledConnection)
}

// CreateConnectionWithRetry creates a NATS connection with retry logic.
func CreateConnectionWithRetry(config *Config) (*nats.Conn, jetstream.JetStream, error) {
	opts, err := BuildConnectionOptions(config)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid NATS connection configuration: %w", err)
	}

	var conn *nats.Conn

	retryInterval := config.RetryInterval
	for attempt := 0; attempt <= config.Retries; attempt++ {
		if attempt > 0 {
			debugLogf("retrying NATS connection (attempt %d/%d) after %v", attempt, config.Retries, retryInterval)
			time.Sleep(retryInterval)

			// Apply backoff
			if config.RetryBackoff > 1 {
				retryInterval = time.Duration(float64(retryInterval) * config.RetryBackoff)
				if config.MaxRetryInterval > 0 && retryInterval > config.MaxRetryInterval {
					retryInterval = config.MaxRetryInterval
				}
			}
		}

		conn, err = nats.Connect(config.URL, opts...)
		if err == nil {
			break
		}

		debugLogf("failed to connect to NATS: %v", err)
	}

	if err != nil {
		return nil, nil, fmt.Errorf("failed to connect to NATS after %d attempts: %w", config.Retries+1, err)
	}

	// Create JetStream context
	js, err := jetstream.New(conn)
	if err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("failed to create JetStream context: %w", err)
	}

	return conn, js, nil
}

// CreateConnectionFromConfig creates a NATS connection from target configuration.
func CreateConnectionFromConfig(config *Config) (*nats.Conn, error) {
	opts, err := BuildConnectionOptions(config)
	if err != nil {
		return nil, fmt.Errorf("invalid NATS connection configuration: %w", err)
	}

	var conn *nats.Conn

	retryInterval := config.RetryInterval
	for attempt := 0; attempt <= config.Retries; attempt++ {
		if attempt > 0 {
			debugLogf("retrying NATS connection (attempt %d/%d) after %v", attempt, config.Retries, retryInterval)
			time.Sleep(retryInterval)

			// Apply backoff
			if config.RetryBackoff > 1 {
				retryInterval = time.Duration(float64(retryInterval) * config.RetryBackoff)
				if config.MaxRetryInterval > 0 && retryInterval > config.MaxRetryInterval {
					retryInterval = config.MaxRetryInterval
				}
			}
		}

		conn, err = nats.Connect(config.URL, opts...)
		if err == nil {
			break
		}

		debugLogf("failed to connect to NATS: %v", err)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to connect to NATS after %d attempts: %w", config.Retries+1, err)
	}

	return conn, nil
}

// BuildConnectionOptions builds NATS connection options with enhanced TLS
// and authentication support. It returns an error instead of silently
// degrading to an anonymous/unauthenticated or unencrypted connection when
// the configured credential or certificate material cannot be loaded -
// deferring that failure to connect time would surface it as an opaque
// server-side auth/TLS rejection far from its actual cause.
func BuildConnectionOptions(config *Config) ([]nats.Option, error) {
	opts := []nats.Option{
		nats.Timeout(config.Timeout),
		nats.MaxReconnects(config.Retries),
		nats.ReconnectWait(config.RetryInterval),
		nats.DisconnectErrHandler(func(nc *nats.Conn, err error) {
			if err != nil {
				debugLogf("NATS disconnected: %v", err)
			}
		}),
		nats.ReconnectHandler(func(nc *nats.Conn) {
			debugLogf("NATS reconnected to %s", nc.ConnectedUrl())
		}),
		nats.ErrorHandler(func(nc *nats.Conn, sub *nats.Subscription, err error) {
			debugLogf("NATS error: %v", err)
		}),
	}

	// TLS configuration
	if config.TLS {
		tlsConfig := &tls.Config{
			InsecureSkipVerify: config.InsecureSkipVerify, // #nosec G402 - controlled by user configuration
		}

		if config.CertFile != "" && config.KeyFile != "" {
			cert, err := tls.LoadX509KeyPair(config.CertFile, config.KeyFile)
			if err != nil {
				return nil, fmt.Errorf("failed to load NATS client certificate/key pair (%s, %s): %w", config.CertFile, config.KeyFile, err)
			}
			tlsConfig.Certificates = []tls.Certificate{cert}
		}

		if config.CAFile != "" {
			opts = append(opts, nats.RootCAs(config.CAFile))
		}

		opts = append(opts, nats.Secure(tlsConfig))
	}

	authOpt, err := buildAuthOption(config)
	if err != nil {
		return nil, err
	}
	if authOpt != nil {
		opts = append(opts, authOpt)
	}

	return opts, nil
}

// buildAuthOption resolves the single nats.Option carrying whichever auth
// method is configured, using the documented precedence order (highest
// first): CredsFile, NkeySeedFile, Token, then User/Password. Only one
// auth method is ever applied - mixing, e.g., a creds file and a token
// makes no sense to the NATS server, so the higher-precedence field wins
// outright rather than being combined with the others. Returns (nil, nil)
// when no auth field is set, preserving the pre-existing anonymous
// connection behavior.
func buildAuthOption(config *Config) (nats.Option, error) {
	switch {
	case config.CredsFile != "":
		return nats.UserCredentials(config.CredsFile), nil
	case config.NkeySeedFile != "":
		opt, err := nats.NkeyOptionFromSeed(config.NkeySeedFile)
		if err != nil {
			return nil, fmt.Errorf("failed to load NATS nkey seed file %s: %w", config.NkeySeedFile, err)
		}
		return opt, nil
	case config.Token != "":
		return nats.Token(config.Token), nil
	case config.User != "" || config.Password != "":
		return nats.UserInfo(config.User, config.Password), nil
	default:
		return nil, nil
	}
}

// FetchFromKV retrieves a value from a NATS KV store with retry logic. It
// always performs the real JetStream read - callers wanting a cache in
// front of it (target-namespaced, request-deduped) use FetchFromKVCached
// instead of calling this directly. FetchFromKV used to check/populate the
// shared TTL cache itself, keyed only by storePath with no target
// component; since op_nats.go's caller already applied its own
// target-namespaced cache in front of this call, that inner cache was
// both redundant and a cross-target data leak (a second target's request
// for the same storePath could be served the first target's cached
// value) - see TestFetchFromKVCached_TargetNamespaced.
//
//nolint:gocyclo // KV fetch includes retry logic and YAML parsing
func FetchFromKV(js jetstream.JetStream, storePath string, config *Config) (interface{}, error) {
	startTime := time.Now()
	operationType := "kv"

	// The "AUDIT: Accessing" line is emitted by the caching layer
	// (FetchFromKVCachedWith, internal/backends/nats/cached_fetch.go)
	// before it checks the cache, so both a cache hit and a miss are
	// audited exactly once - logging it here too would double it on
	// every miss (this function only ever runs on a miss).

	// Parse store name and key
	parts := strings.SplitN(storePath, "/", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid KV path format, expected 'store/key'")
	}
	storeName, key := parts[0], parts[1]

	var result interface{}
	var err error

	retryInterval := config.RetryInterval
	for attempt := 0; attempt <= config.Retries; attempt++ {
		if attempt > 0 {
			debugLogf("retrying KV fetch (attempt %d/%d) after %v", attempt, config.Retries, retryInterval)
			time.Sleep(retryInterval)

			// Apply backoff
			if config.RetryBackoff > 1 {
				retryInterval = time.Duration(float64(retryInterval) * config.RetryBackoff)
				if config.MaxRetryInterval > 0 && retryInterval > config.MaxRetryInterval {
					retryInterval = config.MaxRetryInterval
				}
			}
		}

		// Get KV store
		kv, kvErr := js.KeyValue(context.Background(), storeName)
		if kvErr != nil {
			err = kvErr
			continue
		}

		// Get the entry
		entry, entryErr := kv.Get(context.Background(), key)
		if entryErr != nil {
			err = entryErr
			continue
		}

		// Determine the value type and process accordingly
		value := entry.Value()

		// Handle empty values explicitly
		if len(value) == 0 {
			result = ""
		} else {
			// For KV store, check if it looks like YAML/JSON that should be parsed
			valueStr := string(value)

			// Try parsing as YAML if it looks like structured data
			// Be conservative to avoid parsing simple strings with colons as YAML
			trimmed := strings.TrimSpace(valueStr)
			looksLikeYAML := false

			if trimmed != "" {
				// For KV store, only parse multi-line YAML content
				// Single-line values (even JSON) are preserved as strings
				// This allows storing JSON strings, URLs, and other text with special characters
				if strings.Contains(trimmed, "\n") {
					// Multi-line content is likely YAML, try to parse it
					looksLikeYAML = true
				}
			}

			if looksLikeYAML {
				// Try to parse as YAML
				var parsed interface{}
				err = unmarshalGuarded(value, &parsed)
				if err == nil && parsed != nil {
					parsed = normalizeYAMLValue(parsed)
					// Successfully parsed and got non-string result
					if _, isString := parsed.(string); !isString {
						// Normalize result from goccy/go-yaml (uint64 → int normalization)
						result = parsed
					} else {
						// Parsed but still a string, keep original
						result = valueStr
					}
				} else {
					// Failed to parse, keep as string
					result = valueStr
				}
			} else {
				// Simple string value
				result = valueStr
			}
		}

		// Audit logging for successful KV access
		if config.AuditLogging {
			debugLogf("AUDIT: Successfully retrieved KV data from %s", storePath)
		}

		duration := time.Since(startTime)
		GlobalMetrics.RecordOperation(operationType, duration, false, false)
		return result, nil
	}

	// Audit logging for failed KV access
	if config.AuditLogging {
		debugLogf("AUDIT: Failed to retrieve KV data from %s after %d attempts", storePath, config.Retries+1)
	}

	duration := time.Since(startTime)
	GlobalMetrics.RecordOperation(operationType, duration, true, false)
	return nil, fmt.Errorf("failed to get key '%s' from store '%s' after %d attempts: %w", key, storeName, config.Retries+1, err)
}

// FetchFromObject retrieves a value from a NATS Object store with retry logic.
// It always performs the real JetStream read - see FetchFromObjectCached
// for the target-namespaced, request-deduped cache in front of it (see
// FetchFromKV's doc comment for why the caching used to live here too).
//
//nolint:gocyclo // Object fetch includes retry logic and content-type handling
func FetchFromObject(js jetstream.JetStream, storePath string, config *Config) (interface{}, error) {
	startTime := time.Now()
	operationType := StoreObj

	// The "AUDIT: Accessing" line is emitted by the caching layer
	// (FetchFromObjectCachedWith, internal/backends/nats/cached_fetch.go)
	// before it checks the cache - see FetchFromKV's comment above.

	// Parse bucket name and object name
	parts := strings.SplitN(storePath, "/", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid Object path format, expected 'bucket/object'")
	}
	bucketName, objectName := parts[0], parts[1]

	var result interface{}
	var err error

	retryInterval := config.RetryInterval
	for attempt := 0; attempt <= config.Retries; attempt++ {
		if attempt > 0 {
			debugLogf("retrying Object fetch (attempt %d/%d) after %v", attempt, config.Retries, retryInterval)
			time.Sleep(retryInterval)

			// Apply backoff
			if config.RetryBackoff > 1 {
				retryInterval = time.Duration(float64(retryInterval) * config.RetryBackoff)
				if config.MaxRetryInterval > 0 && retryInterval > config.MaxRetryInterval {
					retryInterval = config.MaxRetryInterval
				}
			}
		}

		// Get Object store
		obj, objErr := js.ObjectStore(context.Background(), bucketName)
		if objErr != nil {
			err = objErr
			continue
		}

		// Get the object info first to check content type
		info, infoErr := obj.GetInfo(context.Background(), objectName)
		if infoErr != nil {
			err = infoErr
			continue
		}

		// Get the object data using streaming for large objects
		data, dataErr := StreamLargeObject(obj, objectName, config.StreamingThreshold)
		if dataErr != nil {
			debugLogf("streaming error for object %s: %v", objectName, dataErr)
			err = dataErr
			continue
		}

		// Process based on content type from headers
		contentType := ""
		if info.Headers != nil {
			contentType = info.Headers.Get("Content-Type")
		}

		switch contentType {
		case "text/yaml", "text/x-yaml", "application/x-yaml", "application/yaml":
			// Parse as YAML
			var yamlResult interface{}
			err = unmarshalGuarded(data, &yamlResult)
			if err != nil {
				return nil, fmt.Errorf("failed to parse YAML from object '%s': %w", objectName, err)
			}
			// Normalize result from goccy/go-yaml (uint64 → int normalization)
			result = normalizeYAMLValue(yamlResult)
		case "application/json", "text/json":
			// Parse as JSON (YAML parser handles JSON too)
			err = unmarshalGuarded(data, &result)
			if err != nil {
				return nil, fmt.Errorf("failed to parse JSON from object '%s': %w", objectName, err)
			}
			result = normalizeYAMLValue(result)
		case "text/plain", "":
			// Check file extension if no content type
			if contentType == "" && (strings.HasSuffix(objectName, ".yaml") || strings.HasSuffix(objectName, ".yml")) {
				// Parse as YAML for .yaml/.yml files
				var yamlResult interface{}
				err = unmarshalGuarded(data, &yamlResult)
				if err != nil {
					// If parsing fails, return as string
					result = string(data)
				} else {
					// Normalize result from goccy/go-yaml (uint64 → int normalization)
					result = normalizeYAMLValue(yamlResult)
				}
			} else {
				// Return as string if text or no content type
				result = string(data)
			}
		default:
			// For any other content type, base64 encode
			result = base64.StdEncoding.EncodeToString(data)
		}

		// Audit logging for successful Object access
		if config.AuditLogging {
			debugLogf("AUDIT: Successfully retrieved Object data from %s (content-type: %s)", storePath, contentType)
		}

		duration := time.Since(startTime)
		GlobalMetrics.RecordOperation(operationType, duration, false, false)
		return result, nil
	}

	// Audit logging for failed Object access
	if config.AuditLogging {
		debugLogf("AUDIT: Failed to retrieve Object data from %s after %d attempts", storePath, config.Retries+1)
	}

	duration := time.Since(startTime)
	GlobalMetrics.RecordOperation(operationType, duration, true, false)
	return nil, fmt.Errorf("failed to get object '%s' from bucket '%s' after %d attempts: %w", objectName, bucketName, config.Retries+1, err)
}

// StreamLargeObject handles streaming of large objects to reduce memory usage.
func StreamLargeObject(obj jetstream.ObjectStore, objectName string, maxSize int64) ([]byte, error) {
	// Get object info first to check size
	info, err := obj.GetInfo(context.Background(), objectName)
	if err != nil {
		return nil, err
	}

	// Safely compare with bounds checking
	if maxSize < 0 || (maxSize >= 0 && info.Size <= uint64(maxSize)) {
		// Object is small enough, use normal method
		return obj.GetBytes(context.Background(), objectName)
	}

	// Object is large, use streaming approach
	reader, err := obj.Get(context.Background(), objectName)
	if err != nil {
		return nil, err
	}
	defer func() { _ = reader.Close() }()

	// Use a buffer to read in chunks
	var result []byte
	buffer := make([]byte, 64*1024) // 64KB chunks

	for {
		n, err := reader.Read(buffer)
		if n > 0 {
			result = append(result, buffer[:n]...)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}

		// Safety check to prevent excessive memory usage
		if int64(len(result)) > maxSize*2 {
			return nil, fmt.Errorf("object too large for processing: %d bytes", len(result))
		}
	}

	return result, nil
}

// Shutdown gracefully shuts down NATS connections and goroutines.
func Shutdown() {
	// Stop cleanup goroutines
	ConnPool.StopCleanup()
	close(CacheStopCleanup)

	// Close all pooled connections
	ConnPool.CloseAll()

	// Close target pool connections
	DefaultPool.CloseAll()

	// Clear cache
	ClearCache()
}

// Helper functions for environment variable parsing.

func getEnvOrDefault(key, defaultValue string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultValue
}

func parseDurationOrDefault(value string, defaultValue time.Duration) time.Duration {
	if d, err := time.ParseDuration(value); err == nil {
		return d
	}
	return defaultValue
}

func parseIntOrDefault(value string, defaultValue int) int {
	var result int
	if n, err := fmt.Sscanf(value, "%d", &result); err == nil && n == 1 {
		return result
	}
	return defaultValue
}

func parseBoolOrDefault(value string) bool {
	return strings.EqualFold(value, "true") || value == "1"
}

// DebugFunc is a function type for debug logging.
// Set this to integrate with the operator's DEBUG function.
var DebugFunc func(format string, args ...interface{})

func debugLogf(format string, args ...interface{}) {
	if DebugFunc != nil {
		DebugFunc(format, args...)
	}
}

// normalizeYAMLValue converts goccy/go-yaml integer types (uint64) to int
// so callers receive the same Go types the codebase expects.
func normalizeYAMLValue(v interface{}) interface{} {
	switch val := v.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(val))
		for k, elem := range val {
			out[k] = normalizeYAMLValue(elem)
		}
		return out
	case []interface{}:
		for i, elem := range val {
			val[i] = normalizeYAMLValue(elem)
		}
		return val
	case uint64:
		if val > math.MaxInt {
			// Too large for int on this platform; leave it as uint64
			// rather than wrapping to a negative number.
			return val
		}
		return int(val)
	default:
		return v
	}
}
