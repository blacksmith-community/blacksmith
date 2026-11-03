package config

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// Environment variable names for configuration overrides.
const (
	EnvEngineStrictMode   = "GRAFT_ENGINE_STRICT_MODE"
	EnvEngineMaxRecursion = "GRAFT_ENGINE_MAX_RECURSION"
	EnvEngineTimeout      = "GRAFT_ENGINE_TIMEOUT"
	EnvCacheEnabled       = "GRAFT_CACHE_ENABLED"
	EnvCacheMaxSize       = "GRAFT_CACHE_MAX_SIZE"
	EnvCacheTTL           = "GRAFT_CACHE_TTL"
	EnvCacheL2Enabled     = "GRAFT_CACHE_L2_ENABLED"
	EnvCacheL2Path        = "GRAFT_CACHE_L2_PATH"
	EnvParallelEnabled    = "GRAFT_PARALLEL_ENABLED"
	EnvParallelMinWorkers = "GRAFT_PARALLEL_MIN_WORKERS"
	EnvParallelMaxWorkers = "GRAFT_PARALLEL_MAX_WORKERS"
	EnvMetricsEnabled     = "GRAFT_METRICS_ENABLED"
	EnvMetricsFormat      = "GRAFT_METRICS_FORMAT"
	EnvMetricsEndpoint    = "GRAFT_METRICS_ENDPOINT"
	EnvLoggingLevel       = "GRAFT_LOGGING_LEVEL"
	EnvLoggingFormat      = "GRAFT_LOGGING_FORMAT"
)

// EnvVar represents an environment variable with its current value.
type EnvVar struct {
	Name        string
	Description string
	Value       string
	IsSet       bool
}

// ApplyEnv applies environment variable overrides to the configuration.
// Environment variables take precedence over file-based configuration.
func ApplyEnv(cfg *Config) {
	applyEngineEnv(cfg)
	applyCacheEnv(cfg)
	applyParallelEnv(cfg)
	applyMetricsEnv(cfg)
	applyLoggingEnv(cfg)
}

// applyEngineEnv applies engine-related environment variables.
func applyEngineEnv(cfg *Config) {
	if val, ok := getBoolEnv(EnvEngineStrictMode); ok {
		cfg.Engine.StrictMode = val
	}
	if val, ok := getIntEnv(EnvEngineMaxRecursion); ok {
		cfg.Engine.MaxRecursion = val
	}
	if val, ok := getDurationEnv(EnvEngineTimeout); ok {
		cfg.Engine.Timeout = val
	}
}

// applyCacheEnv applies cache-related environment variables.
func applyCacheEnv(cfg *Config) {
	if val, ok := getBoolEnv(EnvCacheEnabled); ok {
		cfg.Cache.Enabled = val
	}
	if val, ok := getIntEnv(EnvCacheMaxSize); ok {
		cfg.Cache.MaxSize = val
	}
	if val, ok := getDurationEnv(EnvCacheTTL); ok {
		cfg.Cache.TTL = val
	}
	if val, ok := getBoolEnv(EnvCacheL2Enabled); ok {
		cfg.Cache.L2Enabled = val
	}
	if val := os.Getenv(EnvCacheL2Path); val != "" {
		cfg.Cache.L2Path = val
	}
}

// applyParallelEnv applies parallel-related environment variables.
func applyParallelEnv(cfg *Config) {
	if val, ok := getBoolEnv(EnvParallelEnabled); ok {
		cfg.Parallel.Enabled = val
	}
	if val, ok := getIntEnv(EnvParallelMinWorkers); ok {
		cfg.Parallel.MinWorkers = val
	}
	if val, ok := getIntEnv(EnvParallelMaxWorkers); ok {
		cfg.Parallel.MaxWorkers = val
	}
}

// applyMetricsEnv applies metrics-related environment variables.
func applyMetricsEnv(cfg *Config) {
	if val, ok := getBoolEnv(EnvMetricsEnabled); ok {
		cfg.Metrics.Enabled = val
	}
	if val := os.Getenv(EnvMetricsFormat); val != "" {
		cfg.Metrics.Format = val
	}
	if val := os.Getenv(EnvMetricsEndpoint); val != "" {
		cfg.Metrics.Endpoint = val
	}
}

// applyLoggingEnv applies logging-related environment variables.
func applyLoggingEnv(cfg *Config) {
	if val := os.Getenv(EnvLoggingLevel); val != "" {
		cfg.Logging.Level = val
	}
	if val := os.Getenv(EnvLoggingFormat); val != "" {
		cfg.Logging.Format = val
	}
}

// GetEnvVars returns all configuration environment variables with their current values.
func GetEnvVars() []EnvVar {
	return []EnvVar{
		{
			Name:        EnvEngineStrictMode,
			Description: "Enable strict validation mode (true/false)",
			Value:       os.Getenv(EnvEngineStrictMode),
			IsSet:       os.Getenv(EnvEngineStrictMode) != "",
		},
		{
			Name:        EnvEngineMaxRecursion,
			Description: "Maximum recursion depth for nested operations",
			Value:       os.Getenv(EnvEngineMaxRecursion),
			IsSet:       os.Getenv(EnvEngineMaxRecursion) != "",
		},
		{
			Name:        EnvEngineTimeout,
			Description: "Operation timeout (e.g., 30s, 1m)",
			Value:       os.Getenv(EnvEngineTimeout),
			IsSet:       os.Getenv(EnvEngineTimeout) != "",
		},
		{
			Name:        EnvCacheEnabled,
			Description: "Enable caching (true/false)",
			Value:       os.Getenv(EnvCacheEnabled),
			IsSet:       os.Getenv(EnvCacheEnabled) != "",
		},
		{
			Name:        EnvCacheMaxSize,
			Description: "Maximum cache size (number of entries)",
			Value:       os.Getenv(EnvCacheMaxSize),
			IsSet:       os.Getenv(EnvCacheMaxSize) != "",
		},
		{
			Name:        EnvCacheTTL,
			Description: "Cache entry time-to-live (e.g., 5m, 1h)",
			Value:       os.Getenv(EnvCacheTTL),
			IsSet:       os.Getenv(EnvCacheTTL) != "",
		},
		{
			Name:        EnvCacheL2Enabled,
			Description: "Enable L2 (disk-based) cache (true/false)",
			Value:       os.Getenv(EnvCacheL2Enabled),
			IsSet:       os.Getenv(EnvCacheL2Enabled) != "",
		},
		{
			Name:        EnvCacheL2Path,
			Description: "Path for L2 cache storage",
			Value:       os.Getenv(EnvCacheL2Path),
			IsSet:       os.Getenv(EnvCacheL2Path) != "",
		},
		{
			Name:        EnvParallelEnabled,
			Description: "Enable parallel processing (true/false)",
			Value:       os.Getenv(EnvParallelEnabled),
			IsSet:       os.Getenv(EnvParallelEnabled) != "",
		},
		{
			Name:        EnvParallelMinWorkers,
			Description: "Minimum number of worker goroutines",
			Value:       os.Getenv(EnvParallelMinWorkers),
			IsSet:       os.Getenv(EnvParallelMinWorkers) != "",
		},
		{
			Name:        EnvParallelMaxWorkers,
			Description: "Maximum number of worker goroutines (0 = auto)",
			Value:       os.Getenv(EnvParallelMaxWorkers),
			IsSet:       os.Getenv(EnvParallelMaxWorkers) != "",
		},
		{
			Name:        EnvMetricsEnabled,
			Description: "Enable metrics collection (true/false)",
			Value:       os.Getenv(EnvMetricsEnabled),
			IsSet:       os.Getenv(EnvMetricsEnabled) != "",
		},
		{
			Name:        EnvMetricsFormat,
			Description: "Metrics format (prometheus, json, text)",
			Value:       os.Getenv(EnvMetricsFormat),
			IsSet:       os.Getenv(EnvMetricsFormat) != "",
		},
		{
			Name:        EnvMetricsEndpoint,
			Description: "HTTP endpoint for metrics",
			Value:       os.Getenv(EnvMetricsEndpoint),
			IsSet:       os.Getenv(EnvMetricsEndpoint) != "",
		},
		{
			Name:        EnvLoggingLevel,
			Description: "Log level (debug, info, warn, error)",
			Value:       os.Getenv(EnvLoggingLevel),
			IsSet:       os.Getenv(EnvLoggingLevel) != "",
		},
		{
			Name:        EnvLoggingFormat,
			Description: "Log format (json, text)",
			Value:       os.Getenv(EnvLoggingFormat),
			IsSet:       os.Getenv(EnvLoggingFormat) != "",
		},
	}
}

// GetSetEnvVars returns only the environment variables that are currently set.
func GetSetEnvVars() []EnvVar {
	all := GetEnvVars()
	set := make([]EnvVar, 0)
	for _, v := range all {
		if v.IsSet {
			set = append(set, v)
		}
	}
	return set
}

// getBoolEnv retrieves a boolean value from an environment variable.
func getBoolEnv(key string) (value, found bool) {
	val := os.Getenv(key)
	if val == "" {
		return false, false
	}

	val = strings.ToLower(strings.TrimSpace(val))
	switch val {
	case "true", "1", "yes", "on":
		return true, true
	case "false", "0", "no", "off":
		return false, true
	default:
		return false, false
	}
}

// getIntEnv retrieves an integer value from an environment variable.
func getIntEnv(key string) (int, bool) {
	val := os.Getenv(key)
	if val == "" {
		return 0, false
	}

	intVal, err := strconv.Atoi(strings.TrimSpace(val))
	if err != nil {
		return 0, false
	}

	return intVal, true
}

// getDurationEnv retrieves a duration value from an environment variable.
func getDurationEnv(key string) (time.Duration, bool) {
	val := os.Getenv(key)
	if val == "" {
		return 0, false
	}

	duration, err := time.ParseDuration(strings.TrimSpace(val))
	if err != nil {
		return 0, false
	}

	return duration, true
}
