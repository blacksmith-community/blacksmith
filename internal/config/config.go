package config

import (
	"errors"
	"fmt"
	"os"

	"blacksmith/pkg/logger"
	"blacksmith/pkg/utils"
	"gopkg.in/yaml.v2"
)

// Static errors for err113 compliance.
var (
	ErrVaultAddressNotSet = errors.New("vault address is not set")
	ErrBOSHAddressNotSet  = errors.New("BOSH address is not set")
	ErrBOSHUsernameNotSet = errors.New("BOSH username is not set")
	ErrBOSHPasswordNotSet = errors.New("BOSH password is not set")
)

type Config struct {
	Broker       BrokerConfig       `yaml:"broker"`
	Vault        VaultConfig        `yaml:"vault"`
	Shield       ShieldConfig       `yaml:"shield"`
	BOSH         BOSHConfig         `yaml:"bosh"`
	SSH          SSHConfig          `yaml:"ssh"` // Top-level SSH configuration
	Services     ServicesConfig     `yaml:"services"`
	Reconciler   ReconcilerConfig   `yaml:"reconciler"`
	VMMonitoring VMMonitoringConfig `yaml:"vm_monitoring"`
	Debug        bool               `yaml:"debug"`
	WebRoot      string             `yaml:"web-root"`
	Env          string             `yaml:"env"`
	Shareable    bool               `yaml:"shareable"`
	Forges       ForgesConfig       `yaml:"forges"`
}

// ServicesConfig configures service-specific behavior.
type ServicesConfig struct {
	SkipTLSVerify []string `yaml:"skip_tls_verify"` // List of services to skip TLS verification for (e.g., ["rabbitmq", "redis"] or ["all"])
}

// ShouldSkipTLSVerify checks if TLS verification should be skipped for the given service.
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

// HistoryRetentionConfig holds configuration for history retention policy.
type HistoryRetentionConfig struct {
	Enabled       bool `yaml:"enabled"`
	RetentionDays int  `yaml:"retention_days"`
	MaxEntries    int  `yaml:"max_entries"`
}

// ReconcilerBackupConfig holds backup configuration for the reconciler.
type ReconcilerBackupConfig struct {
	Enabled          bool `yaml:"enabled"`
	RetentionCount   int  `yaml:"retention_count"`
	RetentionDays    int  `yaml:"retention_days"`
	CompressionLevel int  `yaml:"compression_level"`
	CleanupEnabled   bool `yaml:"cleanup_enabled"`
	BackupOnUpdate   bool `yaml:"backup_on_update"`
	BackupOnDelete   bool `yaml:"backup_on_delete"`
}

// ReconcilerConfig holds configuration for the deployment reconciler.
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

// VMMonitoringConfig holds configuration for VM monitoring.
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
	Username     string            `yaml:"username"`
	Password     string            `yaml:"password"`
	Port         string            `yaml:"port"`
	BindIP       string            `yaml:"bind_ip"`
	ReadTimeout  int               `yaml:"read_timeout"`  // HTTP server read timeout in seconds (default: 120)
	WriteTimeout int               `yaml:"write_timeout"` // HTTP server write timeout in seconds (default: 120)
	IdleTimeout  int               `yaml:"idle_timeout"`  // HTTP server idle timeout in seconds (default: 300)
	TLS          TLSConfig         `yaml:"tls"`
	CF           CFBrokerConfig    `yaml:"cf"`          // CF registration configuration
	Compression  CompressionConfig `yaml:"compression"` // HTTP compression configuration
}

// CFBrokerConfig holds configuration for CF broker registration.
type CFBrokerConfig struct {
	Enabled     bool                   `yaml:"enabled"`      // Whether CF registration is enabled
	BrokerURL   string                 `yaml:"broker_url"`   // Public URL for this broker that CF can reach
	BrokerUser  string                 `yaml:"broker_user"`  // Username for CF to authenticate with this broker
	BrokerPass  string                 `yaml:"broker_pass"`  // Password for CF to authenticate with this broker
	DefaultName string                 `yaml:"default_name"` // Default broker name for registrations
	APIs        map[string]CFAPIConfig `yaml:"apis"`         // CF API endpoints configuration
}

// CompressionConfig defines HTTP compression settings.
type CompressionConfig struct {
	Enabled      bool     `yaml:"enabled"`       // Whether compression is enabled (default: true)
	Types        []string `yaml:"types"`         // Compression types to support: gzip, deflate, brotli (default: ["gzip"])
	Level        int      `yaml:"level"`         // Compression level 1-9, -1 for default (default: -1)
	MinSize      int      `yaml:"min_size"`      // Minimum response size to compress in bytes (default: 1024)
	ContentTypes []string `yaml:"content_types"` // MIME types to compress (default: text/*, application/json, etconfig.)
}

// CFAPIConfig represents a Cloud Foundry API endpoint configuration.
type CFAPIConfig struct {
	Name     string `yaml:"name"`     // Display name for the CF endpoint
	Endpoint string `yaml:"endpoint"` // CF API endpoint URL
	Username string `yaml:"username"` // CF API username
	Password string `yaml:"password"` // CF API password
}

type VaultConfig struct {
	Address  string `yaml:"address"`
	Token    string `yaml:"token"`
	Insecure bool   `yaml:"skip_ssl_validation"`
	CredPath string `yaml:"credentials"`
	CACert   string `yaml:"cacert"`
	// Auto-unseal behavior (optional)
	AutoUnseal          bool                   `yaml:"auto_unseal"`
	HealthCheckInterval string                 `yaml:"health_check_interval"` // Go duration, e.g., "15s"
	UnsealCooldown      string                 `yaml:"unseal_cooldown"`       // Go duration, e.g., "30s"
	HistoryRetention    HistoryRetentionConfig `yaml:"history_retention"`
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
	MaxConnections      int       `yaml:"max_connections"`       // Max concurrent BOSH API connections
	MaxBatchConnections int       `yaml:"max_batch_connections"` // Max concurrent batch upgrade connections (separate pool)
	ConnectionTimeout   int       `yaml:"connection_timeout"`    // Timeout waiting for connection slot (seconds)
	SSH               SSHConfig `yaml:"ssh,omitempty"`      // Deprecated: Use top-level SSH config instead
}

// SSHConfig holds SSH-related configuration.
type SSHConfig struct {
	Enabled               bool            `yaml:"enabled"`
	UITerminalEnabled     bool            `yaml:"ui_terminal_enabled"`      // Enable/disable SSH terminal UI functionality
	Timeout               int             `yaml:"timeout"`                  // Default timeout for SSH commands in seconds
	ConnectTimeout        int             `yaml:"connect_timeout"`          // SSH connection timeout in seconds
	SessionInitTimeout    int             `yaml:"session_init_timeout"`     // SSH session initialization timeout in seconds
	OutputReadTimeout     int             `yaml:"output_read_timeout"`      // Timeout for reading output in seconds
	MaxConcurrent         int             `yaml:"max_concurrent"`           // Maximum concurrent SSH sessions
	MaxOutputSize         int             `yaml:"max_output_size"`          // Maximum output size in bytes
	KeepAlive             int             `yaml:"keep_alive"`               // Keep-alive interval in seconds
	RetryAttempts         int             `yaml:"retry_attempts"`           // Number of retry attempts for failed operations
	RetryDelay            int             `yaml:"retry_delay"`              // Delay between retries in seconds
	InsecureIgnoreHostKey bool            `yaml:"insecure_ignore_host_key"` // Skip host key verification (not recommended for production)
	KnownHostsFile        string          `yaml:"known_hosts_file"`         // Path to SSH known_hosts file (auto-discovers hosts on first connection)
	WebSocket             WebSocketConfig `yaml:"websocket"`                // WebSocket configuration
}

// WebSocketConfig holds WebSocket-specific configuration.
type WebSocketConfig struct {
	Enabled           *bool `yaml:"enabled"` // Pointer to distinguish between false and unset
	ReadBufferSize    int   `yaml:"read_buffer_size"`
	WriteBufferSize   int   `yaml:"write_buffer_size"`
	HandshakeTimeout  int   `yaml:"handshake_timeout"` // In seconds
	MaxMessageSize    int   `yaml:"max_message_size"`  // In bytes
	PingInterval      int   `yaml:"ping_interval"`     // In seconds
	PongTimeout       int   `yaml:"pong_timeout"`      // In seconds
	MaxSessions       int   `yaml:"max_sessions"`
	SessionTimeout    int   `yaml:"session_timeout"` // In seconds
	EnableCompression bool  `yaml:"enable_compression"`
}

// ReadConfig reads and validates configuration from file.
func ReadConfig(path string) (Config, error) {
	config, err := loadConfigFromFile(path)
	if err != nil {
		return config, err
	}

	setBrokerDefaults(&config)
	setTLSDefaults(&config)
	setCompressionDefaults(&config)

	err = validateRequiredFields(&config)
	if err != nil {
		return config, err
	}

	err = setBOSHDefaults(&config)
	if err != nil {
		return config, err
	}

	err = setEnvironmentVariables(&config)
	if err != nil {
		return config, err
	}

	setVaultDefaults(&config)
	setVMMonitoringDefaults(&config)
	setReconcilerDefaults(&config)
	setSSHDefaults(&config)

	return config, nil
}

// loadConfigFromFile reads YAML config file.
func loadConfigFromFile(path string) (Config, error) {
	var config Config

	b, err := utils.SafeReadFile(path)
	if err != nil {
		return config, fmt.Errorf("failed to read config file: %w", err)
	}

	err = yaml.Unmarshal(b, &config)
	if err != nil {
		return config, fmt.Errorf("failed to unmarshal config YAML: %w", err)
	}

	return config, nil
}

// setBrokerDefaults sets default values for broker configuration.
func setBrokerDefaults(config *Config) {
	if config.Broker.Username == "" {
		config.Broker.Username = utils.ServiceTypeBlacksmith
	}

	if config.Broker.Password == "" {
		config.Broker.Password = utils.ServiceTypeBlacksmith
	}

	if config.Broker.Port == "" {
		config.Broker.Port = "3000"
	}

	if config.Broker.BindIP == "" {
		config.Broker.BindIP = "0.0.0.0"
	}
}

// setTLSDefaults sets default values for TLS configuration.
func setTLSDefaults(config *Config) {
	if config.Broker.TLS.Port == "" {
		config.Broker.TLS.Port = "443"
	}

	if config.Broker.TLS.Protocols == "" {
		config.Broker.TLS.Protocols = "TLSv1.2 TLSv1.3"
	}

	if config.Broker.TLS.Ciphers == "" {
		config.Broker.TLS.Ciphers = "ECDHE-RSA-AES128-SHA256:AES128-GCM-SHA256:HIGH:!MD5:!aNULL:!EDH"
	}

	if config.Broker.TLS.ReuseAfter == 0 {
		config.Broker.TLS.ReuseAfter = 2
	}
}

// setCompressionDefaults sets default values for compression configuration.
func setCompressionDefaults(config *Config) {
	if len(config.Broker.Compression.Types) == 0 {
		config.Broker.Compression.Types = []string{"gzip"}
	}

	if config.Broker.Compression.Level == 0 {
		config.Broker.Compression.Level = -1 // Use default compression level
	}

	if config.Broker.Compression.MinSize == 0 {
		config.Broker.Compression.MinSize = 1024 // 1KB minimum size
	}

	if len(config.Broker.Compression.ContentTypes) == 0 {
		config.Broker.Compression.ContentTypes = []string{
			"text/html",
			"text/css",
			"text/javascript",
			"text/plain",
			"text/xml",
			"application/json",
			"application/javascript",
			"application/xml",
			"application/rss+xml",
			"application/atom+xml",
			"image/svg+xml",
		}
	}
	// Enable compression by default
	config.Broker.Compression.Enabled = true
}

// validateRequiredFields validates that required configuration fields are set.
func validateRequiredFields(config *Config) error {
	if config.Vault.Address == "" {
		return ErrVaultAddressNotSet
	}

	if config.BOSH.Address == "" {
		return ErrBOSHAddressNotSet
	}

	if config.BOSH.Username == "" {
		return ErrBOSHUsernameNotSet
	}

	if config.BOSH.Password == "" {
		return ErrBOSHPasswordNotSet
	}

	return nil
}

// setBOSHDefaults sets default values for BOSH configuration.
func setBOSHDefaults(config *Config) error {
	if config.BOSH.CCPath != "" {
		/* cloud-config provided; try to read it. */
		b, err := utils.SafeReadFile(config.BOSH.CCPath)
		if err != nil {
			return fmt.Errorf("BOSH cloud-config file '%s': %w", config.BOSH.CCPath, err)
		}

		config.BOSH.CloudConfig = string(b)
	}

	if config.BOSH.Network == "" {
		config.BOSH.Network = utils.ServiceTypeBlacksmith // Default
	}

	// BOSH connection pool defaults
	if config.BOSH.MaxConnections == 0 {
		config.BOSH.MaxConnections = 4 // Default to 4 concurrent connections
	}

	if config.BOSH.MaxBatchConnections == 0 {
		config.BOSH.MaxBatchConnections = 10 // Default to 10 concurrent batch upgrade connections
	}

	if config.BOSH.ConnectionTimeout == 0 {
		config.BOSH.ConnectionTimeout = 300 // Default 5 minutes timeout
	}

	return nil
}

// setEnvironmentVariables sets required environment variables.
func setEnvironmentVariables(config *Config) error {
	err := os.Setenv("BOSH_NETWORK", config.BOSH.Network)
	if err != nil {
		return fmt.Errorf("failed to set BOSH_NETWORK environment variable: %w", err)
	}

	err = os.Setenv("VAULT_ADDR", config.Vault.Address)
	if err != nil {
		return fmt.Errorf("failed to set VAULT_ADDR environment variable: %w", err)
	}

	// Set BOSH environment variables for CLI tools
	err = os.Setenv("BOSH_ENVIRONMENT", config.BOSH.Address)
	if err != nil {
		return fmt.Errorf("failed to set BOSH_ENVIRONMENT environment variable: %w", err)
	}

	err = os.Setenv("BOSH_CLIENT", config.BOSH.Username)
	if err != nil {
		return fmt.Errorf("failed to set BOSH_CLIENT environment variable: %w", err)
	}

	err = os.Setenv("BOSH_CLIENT_SECRET", config.BOSH.Password)
	if err != nil {
		return fmt.Errorf("failed to set BOSH_CLIENT_SECRET environment variable: %w", err)
	}

	// Set BOSH CA certificate if provided
	if config.BOSH.CACert != "" {
		err = os.Setenv("BOSH_CA_CERT", config.BOSH.CACert)
		if err != nil {
			return fmt.Errorf("failed to set BOSH_CA_CERT environment variable: %w", err)
		}
	}

	// Set Vault token if provided
	if config.Vault.Token != "" {
		err = os.Setenv("VAULT_TOKEN", config.Vault.Token)
		if err != nil {
			return fmt.Errorf("failed to set VAULT_TOKEN environment variable: %w", err)
		}
	}

	return nil
}

// setVaultDefaults sets default values for Vault configuration.
func setVaultDefaults(config *Config) {
	if !config.Vault.AutoUnseal {
		config.Vault.AutoUnseal = true // default enabled
	}

	if config.Vault.HealthCheckInterval == "" {
		config.Vault.HealthCheckInterval = "15s"
	}

	if config.Vault.UnsealCooldown == "" {
		config.Vault.UnsealCooldown = "30s"
	}
}

// setVMMonitoringDefaults sets default values for VM monitoring configuration.
func setVMMonitoringDefaults(config *Config) {
	if config.VMMonitoring.NormalInterval == 0 {
		config.VMMonitoring.NormalInterval = 3600 // 1 hour
	}

	if config.VMMonitoring.FailedInterval == 0 {
		config.VMMonitoring.FailedInterval = 300 // 5 minutes
	}

	if config.VMMonitoring.MaxRetries == 0 {
		config.VMMonitoring.MaxRetries = 3
	}

	if config.VMMonitoring.Timeout == 0 {
		config.VMMonitoring.Timeout = 30
	}

	if config.VMMonitoring.MaxConcurrent == 0 {
		config.VMMonitoring.MaxConcurrent = 3
	}
	// VM monitoring is enabled by default
	if config.VMMonitoring.Enabled == nil {
		enabled := true
		config.VMMonitoring.Enabled = &enabled
	}
}

// setReconcilerDefaults sets default values for reconciler configuration.
func setReconcilerDefaults(config *Config) {
	// Reconciler is enabled by default
	if !config.Reconciler.Enabled {
		config.Reconciler.Enabled = true
	}

	// Reconciler backup configuration with sane defaults
	if !config.Reconciler.Backup.Enabled {
		config.Reconciler.Backup.Enabled = true
	}

	if config.Reconciler.Backup.RetentionCount == 0 {
		config.Reconciler.Backup.RetentionCount = 5
	}

	if config.Reconciler.Backup.RetentionDays == 0 {
		config.Reconciler.Backup.RetentionDays = 0 // Disabled by default
	}

	if config.Reconciler.Backup.CompressionLevel == 0 {
		config.Reconciler.Backup.CompressionLevel = 9 // Maximum compression
	}

	if !config.Reconciler.Backup.CleanupEnabled {
		config.Reconciler.Backup.CleanupEnabled = true
	}

	if !config.Reconciler.Backup.BackupOnUpdate {
		config.Reconciler.Backup.BackupOnUpdate = true
	}

	if !config.Reconciler.Backup.BackupOnDelete {
		config.Reconciler.Backup.BackupOnDelete = true
	}
}

// setSSHDefaults sets default values for SSH configuration.
func setSSHDefaults(config *Config) {
	migrateSSHConfiguration(config)
	setSSHEnabledDefaults(config)
	setSSHTimeoutDefaults(config)
	setSSHLimitDefaults(config)
	setSSHRetryDefaults(config)
	clearDeprecatedSSHConfig(config)
}

// migrateSSHConfiguration migrates SSH configuration from old location to new location.
func migrateSSHConfiguration(config *Config) {
	// Support both bosh.ssh (deprecated) and top-level ssh
	if config.BOSH.SSH.Enabled || config.BOSH.SSH.Timeout > 0 || config.BOSH.SSH.MaxConcurrent > 0 {
		// Old configuration exists under bosh.ssh
		if !config.SSH.Enabled && config.SSH.Timeout == 0 {
			// New location is empty, migrate from old location
			config.SSH = config.BOSH.SSH

			logger.Get().Named("config").Info("Migrated SSH configuration from bosh.ssh to top-level ssh. Please update your configuration file.")
		}
	}
}

// setSSHEnabledDefaults sets default enabled flags for SSH features.
func setSSHEnabledDefaults(config *Config) {
	// SSH is enabled by default
	if !config.SSH.Enabled {
		config.SSH.Enabled = true
	}
	// SSH UI Terminal is enabled by default
	if !config.SSH.UITerminalEnabled {
		config.SSH.UITerminalEnabled = true
	}
	// SSH WebSocket is enabled by default
	if config.SSH.WebSocket.Enabled == nil {
		enabled := true
		config.SSH.WebSocket.Enabled = &enabled
	}
}

// setSSHTimeoutDefaults sets default timeout values for SSH.
func setSSHTimeoutDefaults(config *Config) {
	if config.SSH.Timeout == 0 {
		config.SSH.Timeout = 600 // 10 minutes
	}

	if config.SSH.ConnectTimeout == 0 {
		config.SSH.ConnectTimeout = 30
	}

	if config.SSH.SessionInitTimeout == 0 {
		config.SSH.SessionInitTimeout = 60
	}

	if config.SSH.OutputReadTimeout == 0 {
		config.SSH.OutputReadTimeout = 2
	}
}

// setSSHLimitDefaults sets default limits for SSH operations.
func setSSHLimitDefaults(config *Config) {
	if config.SSH.MaxConcurrent == 0 {
		config.SSH.MaxConcurrent = 10
	}

	if config.SSH.MaxOutputSize == 0 {
		config.SSH.MaxOutputSize = 1048576 // 1MB
	}

	if config.SSH.KeepAlive == 0 {
		config.SSH.KeepAlive = 10
	}
}

// setSSHRetryDefaults sets default retry values for SSH.
func setSSHRetryDefaults(config *Config) {
	if config.SSH.RetryAttempts == 0 {
		config.SSH.RetryAttempts = 3
	}

	if config.SSH.RetryDelay == 0 {
		config.SSH.RetryDelay = 5
	}
}

// clearDeprecatedSSHConfig clears the deprecated BOSH.SSH configuration.
func clearDeprecatedSSHConfig(config *Config) {
	config.BOSH.SSH = SSHConfig{}
}

// IsSSHUITerminalEnabled returns whether SSH UI Terminal is enabled.
func (c *Config) IsSSHUITerminalEnabled() bool {
	return c.SSH.UITerminalEnabled
}

// GetEnvironment returns the configured environment label.
func (c *Config) GetEnvironment() string {
	return c.Env
}

// GetBOSHConfig exposes a copy of the BOSH configuration.
func (c *Config) GetBOSHConfig() BOSHConfig {
	return c.BOSH
}

// GetVaultConfig exposes a copy of the Vault configuration.
func (c *Config) GetVaultConfig() VaultConfig {
	return c.Vault
}

// GetBrokerConfig exposes a copy of the broker configuration.
func (c *Config) GetBrokerConfig() BrokerConfig {
	return c.Broker
}

// GetCFConfig exposes a copy of the CF broker configuration.
func (c *Config) GetCFConfig() CFBrokerConfig {
	return c.Broker.CF
}

// GetBrokerTLSEnabled returns whether TLS is enabled for the broker.
func (c *Config) GetBrokerTLSEnabled() bool {
	return c.Broker.TLS.Enabled
}

// GetBrokerTLSCertificate returns the path to the broker's TLS certificate.
func (c *Config) GetBrokerTLSCertificate() string {
	return c.Broker.TLS.Certificate
}

// GetVaultCACert returns the path to the Vault CA certificate.
func (c *Config) GetVaultCACert() string {
	return c.Vault.CACert
}

// GetBOSHCACert returns the path to the BOSH CA certificate.
func (c *Config) GetBOSHCACert() string {
	return c.BOSH.CACert
}

// GetVMMonitoringNormalInterval returns the normal interval for VM monitoring.
func (c *Config) GetVMMonitoringNormalInterval() int {
	return c.VMMonitoring.NormalInterval
}

// GetVMMonitoringFailedInterval returns the failed interval for VM monitoring.
func (c *Config) GetVMMonitoringFailedInterval() int {
	return c.VMMonitoring.FailedInterval
}

// IsVMMonitoringEnabled returns whether VM monitoring is enabled.
func (c *Config) IsVMMonitoringEnabled() bool {
	return c.VMMonitoring.Enabled != nil && *c.VMMonitoring.Enabled
}
