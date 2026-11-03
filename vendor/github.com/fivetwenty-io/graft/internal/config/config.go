// Package config provides a unified configuration system for graft.
// It supports loading configuration from YAML files, environment variables,
// and provides hot-reload capabilities via file watching.
package config

import (
	"sync"
	"time"
)

// Config represents the complete graft configuration.
type Config struct {
	// Engine configuration for core graft engine settings.
	Engine EngineConfig `yaml:"engine"`

	// Cache configuration for caching behavior.
	Cache CacheConfig `yaml:"cache"`

	// Parallel configuration for parallel processing.
	Parallel ParallelConfig `yaml:"parallel"`

	// Metrics configuration for metrics collection.
	Metrics MetricsConfig `yaml:"metrics"`

	// Logging configuration for log output.
	Logging LoggingConfig `yaml:"logging"`
}

// EngineConfig contains core engine settings.
type EngineConfig struct {
	// StrictMode enables strict validation of graft operations.
	StrictMode bool `yaml:"strict_mode"`

	// MaxRecursion sets the maximum recursion depth for nested operations.
	MaxRecursion int `yaml:"max_recursion"`

	// Timeout sets the maximum duration for operations.
	Timeout time.Duration `yaml:"timeout"`
}

// CacheConfig contains cache-related settings.
type CacheConfig struct {
	// Enabled determines whether caching is enabled.
	Enabled bool `yaml:"enabled"`

	// MaxSize sets the maximum number of items in the cache.
	MaxSize int `yaml:"max_size"`

	// TTL sets the time-to-live for cache entries.
	TTL time.Duration `yaml:"ttl"`

	// L2Enabled enables the secondary (disk-based) cache.
	L2Enabled bool `yaml:"l2_enabled"`

	// L2Path sets the path for the L2 cache storage.
	L2Path string `yaml:"l2_path"`
}

// ParallelConfig contains parallel processing settings.
type ParallelConfig struct {
	// Enabled determines whether parallel processing is enabled.
	Enabled bool `yaml:"enabled"`

	// MinWorkers sets the minimum number of worker goroutines.
	MinWorkers int `yaml:"min_workers"`

	// MaxWorkers sets the maximum number of worker goroutines.
	MaxWorkers int `yaml:"max_workers"`
}

// MetricsConfig contains metrics collection settings.
type MetricsConfig struct {
	// Enabled determines whether metrics collection is enabled.
	Enabled bool `yaml:"enabled"`

	// Format sets the metrics output format (prometheus, json, text).
	Format string `yaml:"format"`

	// Endpoint sets the HTTP endpoint for metrics exposition.
	Endpoint string `yaml:"endpoint"`
}

// LoggingConfig contains logging settings.
type LoggingConfig struct {
	// Level sets the log level (debug, info, warn, error).
	Level string `yaml:"level"`

	// Format sets the log format (json, text).
	Format string `yaml:"format"`
}

// DefaultConfig returns a new Config with sensible defaults.
func DefaultConfig() *Config {
	return &Config{
		Engine: EngineConfig{
			StrictMode:   false,
			MaxRecursion: 100,
			Timeout:      30 * time.Second,
		},
		Cache: CacheConfig{
			Enabled:   true,
			MaxSize:   10000,
			TTL:       5 * time.Minute,
			L2Enabled: false,
			L2Path:    "",
		},
		Parallel: ParallelConfig{
			Enabled:    true,
			MinWorkers: 1,
			MaxWorkers: 0, // 0 means auto-detect based on CPU count
		},
		Metrics: MetricsConfig{
			Enabled:  false,
			Format:   formatPrometheus,
			Endpoint: "/metrics",
		},
		Logging: LoggingConfig{
			Level:  levelInfo,
			Format: formatText,
		},
	}
}

// Clone returns a deep copy of the configuration.
func (c *Config) Clone() *Config {
	if c == nil {
		return nil
	}

	return &Config{
		Engine: EngineConfig{
			StrictMode:   c.Engine.StrictMode,
			MaxRecursion: c.Engine.MaxRecursion,
			Timeout:      c.Engine.Timeout,
		},
		Cache: CacheConfig{
			Enabled:   c.Cache.Enabled,
			MaxSize:   c.Cache.MaxSize,
			TTL:       c.Cache.TTL,
			L2Enabled: c.Cache.L2Enabled,
			L2Path:    c.Cache.L2Path,
		},
		Parallel: ParallelConfig{
			Enabled:    c.Parallel.Enabled,
			MinWorkers: c.Parallel.MinWorkers,
			MaxWorkers: c.Parallel.MaxWorkers,
		},
		Metrics: MetricsConfig{
			Enabled:  c.Metrics.Enabled,
			Format:   c.Metrics.Format,
			Endpoint: c.Metrics.Endpoint,
		},
		Logging: LoggingConfig{
			Level:  c.Logging.Level,
			Format: c.Logging.Format,
		},
	}
}

// Manager manages configuration loading, validation, and hot-reloading.
type Manager struct {
	config      *Config
	configPath  string
	mu          sync.RWMutex
	changeHooks []func(*Config)
}

// NewManager creates a new configuration manager with default configuration.
func NewManager() *Manager {
	return &Manager{
		config:      DefaultConfig(),
		changeHooks: make([]func(*Config), 0),
	}
}

// Get returns a copy of the current configuration.
func (m *Manager) Get() *Config {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.config.Clone()
}

// Set updates the configuration after validation.
func (m *Manager) Set(cfg *Config) error {
	if err := Validate(cfg); err != nil {
		return err
	}

	m.mu.Lock()
	m.config = cfg.Clone()
	hooks := make([]func(*Config), len(m.changeHooks))
	copy(hooks, m.changeHooks)
	m.mu.Unlock()

	// Notify change hooks outside the lock.
	for _, hook := range hooks {
		go hook(cfg.Clone())
	}

	return nil
}

// OnChange registers a callback to be invoked when configuration changes.
func (m *Manager) OnChange(hook func(*Config)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.changeHooks = append(m.changeHooks, hook)
}

// GetConfigPath returns the path of the loaded configuration file.
func (m *Manager) GetConfigPath() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.configPath
}
