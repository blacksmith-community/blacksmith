package main

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v2"
)

type Config struct {
	Broker       BrokerConfig       `yaml:"broker"`
	Vault        VaultConfig        `yaml:"vault"`
	Shield       ShieldConfig       `yaml:"shield"`
	BOSH         BOSHConfig         `yaml:"bosh"`
	Services     ServicesConfig     `yaml:"services"`
	Reconciler   ReconcilerConfig   `yaml:"reconciler"`
	VMMonitoring VMMonitoringConfig `yaml:"vm_monitoring"`
	Debug        bool               `yaml:"debug"`
	WebRoot      string             `yaml:"web-root"`
	Env          string             `yaml:"env"`
	Shareable    bool               `yaml:"shareable"`
	Forges       ForgesConfig       `yaml:"forges"`
}

// ServicesConfig configures service-specific behavior
type ServicesConfig struct {
	SkipTLSVerify []string `yaml:"skip_tls_verify"` // List of services to skip TLS verification for (e.g., ["rabbitmq", "redis"] or ["all"])
}

// ShouldSkipTLSVerify checks if TLS verification should be skipped for the given service
func (s *ServicesConfig) ShouldSkipTLSVerify(serviceName string) bool {
	for _, service := range s.SkipTLSVerify {
		if service == "all" || service == serviceName {
			return true
		}
	}
	return false
}

type ForgesConfig struct {
	AutoScan     bool     `yaml:"auto-scan"`
	ScanPaths    []string `yaml:"scan-paths"`
	ScanPatterns []string `yaml:"scan-patterns"`
}

// ReconcilerBackupConfig holds backup configuration for the reconciler
type ReconcilerBackupConfig struct {
	Enabled   bool   `yaml:"enabled"`
	Retention int    `yaml:"retention"`
	Cleanup   bool   `yaml:"cleanup"`
	Path      string `yaml:"path"`
}

// ReconcilerConfig holds configuration for the deployment reconciler
type ReconcilerConfig struct {
	Enabled        bool                   `yaml:"enabled"`
	Interval       string                 `yaml:"interval"`
	MaxConcurrency int                    `yaml:"max_concurrency"`
	BatchSize      int                    `yaml:"batch_size"`
	RetryAttempts  int                    `yaml:"retry_attempts"`
	RetryDelay     string                 `yaml:"retry_delay"`
	CacheTTL       string                 `yaml:"cache_ttl"`
	Debug          bool                   `yaml:"debug"`
	Backup         ReconcilerBackupConfig `yaml:"backup"`
}

// VMMonitoringConfig holds configuration for VM monitoring
type VMMonitoringConfig struct {
	Enabled        *bool `yaml:"enabled"`         // Pointer to distinguish between false and unset
	NormalInterval int   `yaml:"normal_interval"` // Seconds between checks for healthy deployments
	FailedInterval int   `yaml:"failed_interval"` // Seconds between checks for unhealthy deployments
	MaxRetries     int   `yaml:"max_retries"`
	Timeout        int   `yaml:"timeout"`
	MaxConcurrent  int   `yaml:"max_concurrent"`
}

type TLSConfig struct {
	Enabled     bool   `yaml:"enabled"`
	Port        string `yaml:"port"`
	Certificate string `yaml:"certificate"`
	Key         string `yaml:"key"`
	Protocols   string `yaml:"protocols"`
	Ciphers     string `yaml:"ciphers"`
	ReuseAfter  int    `yaml:"reuse_after"`
}

type BrokerConfig struct {
	Username     string         `yaml:"username"`
	Password     string         `yaml:"password"`
	Port         string         `yaml:"port"`
	BindIP       string         `yaml:"bind_ip"`
	ReadTimeout  int            `yaml:"read_timeout"`  // HTTP server read timeout in seconds (default: 120)
	WriteTimeout int            `yaml:"write_timeout"` // HTTP server write timeout in seconds (default: 120)
	IdleTimeout  int            `yaml:"idle_timeout"`  // HTTP server idle timeout in seconds (default: 300)
	TLS          TLSConfig      `yaml:"tls"`
	CF           CFBrokerConfig `yaml:"cf"` // CF registration configuration
}

// CFBrokerConfig holds configuration for CF broker registration
type CFBrokerConfig struct {
	Enabled     bool   `yaml:"enabled"`      // Whether CF registration is enabled
	BrokerURL   string `yaml:"broker_url"`   // Public URL for this broker that CF can reach
	BrokerUser  string `yaml:"broker_user"`  // Username for CF to authenticate with this broker
	BrokerPass  string `yaml:"broker_pass"`  // Password for CF to authenticate with this broker
	DefaultName string `yaml:"default_name"` // Default broker name for registrations
}

type VaultConfig struct {
	Address  string `yaml:"address"`
	Token    string `yaml:"token"`
	Insecure bool   `yaml:"skip_ssl_validation"`
	CredPath string `yaml:"credentials"`
	CACert   string `yaml:"cacert"`
}

type ShieldConfig struct {
	Enabled  bool   `yaml:"enabled"`
	Address  string `yaml:"address"`
	Insecure bool   `yaml:"skip_ssl_validation"`

	Agent string `yaml:"agent"`

	AuthMethod string `yaml:"auth_method"` // "token", "local"
	Token      string `yaml:"token"`
	Username   string `yaml:"username"`
	Password   string `yaml:"password"`

	Tenant string `yaml:"tenant"` // Full UUID or exact name
	Store  string `yaml:"store"`  // Full UUID or exact name

	Schedule string `yaml:"schedule"` // daily, weekly, daily at 11:00
	Retain   string `yaml:"retain"`   // 7d, 7w, ...

	EnabledOnTargets []string `yaml:"enabled_on_targets"` // rabbitmq, redis, ...
}

type Uploadable struct {
	Name    string `yaml:"name"`
	Version string `yaml:"version"`
	URL     string `yaml:"url"`
	SHA1    string `yaml:"sha1"`
}

type BOSHConfig struct {
	Address           string       `yaml:"address"`
	Username          string       `yaml:"username"`
	Password          string       `yaml:"password"`
	SkipSslValidation bool         `yaml:"skip_ssl_validation"`
	CACert            string       `yaml:"cacert"`
	Stemcells         []Uploadable `yaml:"stemcells"`
	Releases          []Uploadable `yaml:"releases"`
	CCPath            string       `yaml:"cloud-config"` // TODO: CCPath vs CloudConfig & yaml???
	CloudConfig       string
	Network           string    `yaml:"network"`
	SSH               SSHConfig `yaml:"ssh"`
}

// SSHConfig holds SSH-related configuration
type SSHConfig struct {
	Enabled        bool            `yaml:"enabled"`
	Timeout        int             `yaml:"timeout"`         // Default timeout for SSH commands in seconds
	ConnectTimeout int             `yaml:"connect_timeout"` // SSH connection timeout in seconds
	MaxConcurrent  int             `yaml:"max_concurrent"`  // Maximum concurrent SSH sessions
	MaxOutputSize  int             `yaml:"max_output_size"` // Maximum output size in bytes
	KeepAlive      int             `yaml:"keep_alive"`      // Keep-alive interval in seconds
	RetryAttempts  int             `yaml:"retry_attempts"`  // Number of retry attempts for failed operations
	RetryDelay     int             `yaml:"retry_delay"`     // Delay between retries in seconds
	WebSocket      WebSocketConfig `yaml:"websocket"`       // WebSocket configuration
}

// WebSocketConfig holds WebSocket-specific configuration
type WebSocketConfig struct {
	Enabled           bool `yaml:"enabled"`
	ReadBufferSize    int  `yaml:"read_buffer_size"`
	WriteBufferSize   int  `yaml:"write_buffer_size"`
	HandshakeTimeout  int  `yaml:"handshake_timeout"` // In seconds
	MaxMessageSize    int  `yaml:"max_message_size"`  // In bytes
	PingInterval      int  `yaml:"ping_interval"`     // In seconds
	PongTimeout       int  `yaml:"pong_timeout"`      // In seconds
	MaxSessions       int  `yaml:"max_sessions"`
	SessionTimeout    int  `yaml:"session_timeout"` // In seconds
	EnableCompression bool `yaml:"enable_compression"`
}

func ReadConfig(path string) (c Config, err error) {
	b, err := safeReadFile(path)
	if err != nil {
		return
	}

	err = yaml.Unmarshal(b, &c)

	if err != nil {
		return
	}

	if c.Broker.Username == "" {
		c.Broker.Username = "blacksmith"
	}

	if c.Broker.Password == "" {
		c.Broker.Password = "blacksmith"
	}

	if c.Broker.Port == "" {
		c.Broker.Port = "3000"
	}

	if c.Broker.BindIP == "" {
		c.Broker.BindIP = "0.0.0.0"
	}

	// TLS configuration defaults
	if c.Broker.TLS.Port == "" {
		c.Broker.TLS.Port = "443"
	}
	if c.Broker.TLS.Protocols == "" {
		c.Broker.TLS.Protocols = "TLSv1.2 TLSv1.3"
	}
	if c.Broker.TLS.Ciphers == "" {
		c.Broker.TLS.Ciphers = "ECDHE-RSA-AES128-SHA256:AES128-GCM-SHA256:HIGH:!MD5:!aNULL:!EDH"
	}
	if c.Broker.TLS.ReuseAfter == 0 {
		c.Broker.TLS.ReuseAfter = 2
	}

	if c.Vault.Address == "" {
		return c, fmt.Errorf("Vault Address is not set")
	}

	if c.BOSH.Address == "" {
		return c, fmt.Errorf("BOSH Address is not set")
	}

	if c.BOSH.Username == "" {
		return c, fmt.Errorf("BOSH Username is not set")
	}

	if c.BOSH.Password == "" {
		return c, fmt.Errorf("BOSH Password is not set")
	}

	if c.BOSH.CCPath != "" {
		/* cloud-config provided; try to read it. */
		b, err := safeReadFile(c.BOSH.CCPath)
		if err != nil {
			return c, fmt.Errorf("BOSH cloud-config file '%s': %s", c.BOSH.CCPath, err)
		}
		c.BOSH.CloudConfig = string(b)
	}

	if c.BOSH.Network == "" {
		c.BOSH.Network = "blacksmith" // Default
	}

	if err := os.Setenv("BOSH_NETWORK", c.BOSH.Network); err != nil { // Required by manifest.go
		return Config{}, fmt.Errorf("failed to set BOSH_NETWORK environment variable: %s", err)
	}

	if err := os.Setenv("VAULT_ADDR", c.Vault.Address); err != nil {
		return Config{}, fmt.Errorf("failed to set VAULT_ADDR environment variable: %s", err)
	}

	// VM monitoring defaults
	if c.VMMonitoring.NormalInterval == 0 {
		c.VMMonitoring.NormalInterval = 3600 // 1 hour
	}
	if c.VMMonitoring.FailedInterval == 0 {
		c.VMMonitoring.FailedInterval = 300 // 5 minutes
	}
	if c.VMMonitoring.MaxRetries == 0 {
		c.VMMonitoring.MaxRetries = 3
	}
	if c.VMMonitoring.Timeout == 0 {
		c.VMMonitoring.Timeout = 30
	}
	if c.VMMonitoring.MaxConcurrent == 0 {
		c.VMMonitoring.MaxConcurrent = 3
	}
	// VM monitoring is enabled by default
	if c.VMMonitoring.Enabled == nil {
		enabled := true
		c.VMMonitoring.Enabled = &enabled
	}

	return
}
