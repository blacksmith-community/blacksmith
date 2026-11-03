// Package natsbackend provides NATS client management, configuration,
// caching, and metrics for the NATS operator backend.
package natsbackend

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Store type constants.
const (
	StoreKV  = "kv"
	StoreObj = "obj"
)

// Target represents a NATS target configuration.
type Target struct {
	URL                string        `yaml:"url"`
	Timeout            time.Duration `yaml:"timeout"`
	Retries            int           `yaml:"retries"`
	RetryInterval      time.Duration `yaml:"retry_interval"`
	RetryBackoff       float64       `yaml:"retry_backoff"`
	MaxRetryInterval   time.Duration `yaml:"max_retry_interval"`
	TLS                bool          `yaml:"tls"`
	CertFile           string        `yaml:"cert_file"`
	KeyFile            string        `yaml:"key_file"`
	CAFile             string        `yaml:"ca_file"`
	InsecureSkipVerify bool          `yaml:"insecure_skip_verify"`
	CacheTTL           time.Duration `yaml:"cache_ttl"`
	StreamingThreshold int64         `yaml:"streaming_threshold"`
	AuditLogging       bool          `yaml:"audit_logging"`

	// Auth holds credential material used to authenticate the NATS
	// connection. Precedence when more than one is set (highest first):
	// CredsFile, NkeySeedFile, Token, then User/Password. See
	// BuildConnectionOptions for the resolution order.
	Token        string `yaml:"token"`
	User         string `yaml:"user"`
	Password     string `yaml:"password"`
	NkeySeedFile string `yaml:"nkey_seed_file"`
	CredsFile    string `yaml:"creds_file"`
}

// Config holds connection configuration with enhanced retry and TLS options.
type Config struct {
	URL                string
	Timeout            time.Duration
	Retries            int
	RetryInterval      time.Duration
	RetryBackoff       float64
	MaxRetryInterval   time.Duration
	TLS                bool
	CertFile           string
	KeyFile            string
	CAFile             string
	InsecureSkipVerify bool
	CacheTTL           time.Duration
	StreamingThreshold int64 // Size threshold for streaming objects (bytes)
	AuditLogging       bool  // Enable audit logging for access

	// Auth holds credential material used to authenticate the NATS
	// connection. Precedence when more than one is set (highest first):
	// CredsFile, NkeySeedFile, Token, then User/Password. See
	// BuildConnectionOptions for the resolution order.
	Token        string // NATS_TOKEN / NATS_<TARGET>_TOKEN
	User         string // NATS_USER / NATS_<TARGET>_USER
	Password     string // NATS_PASSWORD / NATS_<TARGET>_PASSWORD
	NkeySeedFile string // NATS_NKEY / NATS_<TARGET>_NKEY (path to an nkey seed file)
	CredsFile    string // NATS_CREDS / NATS_<TARGET>_CREDS (path to a .creds file)
}

// ParsePath extracts store type (kv/obj) and path from the argument.
func ParsePath(path string) (storeType, storePath string, err error) {
	parts := strings.SplitN(path, ":", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid NATS path format, expected 'kv:store/key' or 'obj:bucket/object'")
	}

	storeType = strings.ToLower(parts[0])
	if storeType != StoreKV && storeType != StoreObj {
		return "", "", fmt.Errorf("invalid store type '%s', must be 'kv' or 'obj'", storeType)
	}

	storePath = parts[1]
	if storePath == "" {
		return "", "", fmt.Errorf("empty path after store type")
	}

	return storeType, storePath, nil
}

// ParseInt64OrDefault parses int64 string or returns default.
func ParseInt64OrDefault(value string, defaultValue int64) int64 {
	if i, err := strconv.ParseInt(value, 10, 64); err == nil {
		return i
	}
	return defaultValue
}

// ParseFloatOrDefault parses float64 string or returns default.
func ParseFloatOrDefault(value string, defaultValue float64) float64 {
	if f, err := strconv.ParseFloat(value, 64); err == nil {
		return f
	}
	return defaultValue
}
