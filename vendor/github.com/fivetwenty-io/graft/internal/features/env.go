package features

import (
	"os"
	"strconv"
	"strings"
)

// Environment variable names for feature flags.
const (
	// EnvFeatureParallel controls parallel evaluation.
	// Set to "true", "1", or "yes" to enable.
	EnvFeatureParallel = "GRAFT_FEATURE_PARALLEL"

	// EnvFeatureCache controls operator result caching.
	// Set to "true", "1", or "yes" to enable.
	EnvFeatureCache = "GRAFT_FEATURE_CACHE"

	// EnvFeatureMetrics controls metrics collection.
	// Set to "true", "1", or "yes" to enable.
	EnvFeatureMetrics = "GRAFT_FEATURE_METRICS"

	// EnvFeatureDebug controls debug logging.
	// Set to "true", "1", or "yes" to enable.
	EnvFeatureDebug = "GRAFT_FEATURE_DEBUG"

	// EnvFeatureStrictTypes controls strict type checking.
	// Set to "true", "1", or "yes" to enable.
	EnvFeatureStrictTypes = "GRAFT_FEATURE_STRICT_TYPES"

	// EnvFeaturePools controls memory pooling.
	// Set to "true", "1", or "yes" to enable.
	EnvFeaturePools = "GRAFT_FEATURE_POOLS"

	// EnvFeatureBackendRegistry controls the pkg/graft custom-backend
	// registry (FeatureBackendRegistry). Set to "true", "1", or "yes" to
	// enable.
	EnvFeatureBackendRegistry = "GRAFT_FEATURE_BACKEND_REGISTRY"
)

// envMapping maps environment variable names to feature flag constants.
var envMapping = map[string]string{
	EnvFeatureParallel:        FeatureParallelEvaluation,
	EnvFeatureCache:           FeatureCaching,
	EnvFeatureMetrics:         FeatureMetrics,
	EnvFeatureDebug:           FeatureDebugLogging,
	EnvFeatureStrictTypes:     FeatureStrictTypeChecking,
	EnvFeaturePools:           FeatureMemoryPools,
	EnvFeatureBackendRegistry: FeatureBackendRegistry,
}

// LoadFromEnv loads feature flag settings from environment variables.
// Environment variables take the form GRAFT_FEATURE_* and accept
// boolean values: "true", "false", "1", "0", "yes", "no".
//
// If an environment variable is not set, the existing flag value is preserved.
// If an environment variable has an invalid value, it is ignored.
//
// Environment variable to flag mapping:
//
//   - GRAFT_FEATURE_PARALLEL          -> FeatureParallelEvaluation
//   - GRAFT_FEATURE_CACHE             -> FeatureCaching
//   - GRAFT_FEATURE_METRICS           -> FeatureMetrics
//   - GRAFT_FEATURE_DEBUG             -> FeatureDebugLogging
//   - GRAFT_FEATURE_STRICT_TYPES      -> FeatureStrictTypeChecking
//   - GRAFT_FEATURE_POOLS             -> FeatureMemoryPools
//   - GRAFT_FEATURE_BACKEND_REGISTRY  -> FeatureBackendRegistry
func (ff *FeatureFlags) LoadFromEnv() {
	ff.mu.Lock()
	defer ff.mu.Unlock()

	for envVar, flag := range envMapping {
		if value := os.Getenv(envVar); value != "" {
			if enabled, ok := parseBool(value); ok {
				ff.flags[flag] = enabled
			}
		}
	}
}

// EnvOverride reports whether envVar carries a valid boolean in the
// environment, and its parsed value, using the same value parsing as
// LoadFromEnv. It lets callers distinguish an explicit setting from a
// flag left at its default - needed for flags whose package default
// disagrees with a config-tier default, where the merged FeatureFlags
// value alone cannot tell "explicitly disabled" from "never set".
func EnvOverride(envVar string) (value, explicit bool) {
	raw := os.Getenv(envVar)
	if raw == "" {
		return false, false
	}
	v, ok := parseBool(raw)
	return v, ok
}

// LoadFromEnvWithPrefix loads feature flags from environment variables
// with a custom prefix. This is useful for testing or when running
// multiple graft instances with different configurations.
//
// For example, with prefix "TEST_", the function looks for:
//
//   - TEST_FEATURE_PARALLEL
//   - TEST_FEATURE_CACHE
//   - etc.
func (ff *FeatureFlags) LoadFromEnvWithPrefix(prefix string) {
	ff.mu.Lock()
	defer ff.mu.Unlock()

	// Build custom mapping with the prefix
	customMapping := map[string]string{
		prefix + "FEATURE_PARALLEL":         FeatureParallelEvaluation,
		prefix + "FEATURE_CACHE":            FeatureCaching,
		prefix + "FEATURE_METRICS":          FeatureMetrics,
		prefix + "FEATURE_DEBUG":            FeatureDebugLogging,
		prefix + "FEATURE_STRICT_TYPES":     FeatureStrictTypeChecking,
		prefix + "FEATURE_POOLS":            FeatureMemoryPools,
		prefix + "FEATURE_BACKEND_REGISTRY": FeatureBackendRegistry,
	}

	for envVar, flag := range customMapping {
		if value := os.Getenv(envVar); value != "" {
			if enabled, ok := parseBool(value); ok {
				ff.flags[flag] = enabled
			}
		}
	}
}

// parseBool parses a string into a boolean value.
// It accepts common boolean representations and returns (value, ok).
// Returns (false, false) if the string cannot be parsed.
func parseBool(s string) (value, ok bool) {
	s = strings.ToLower(strings.TrimSpace(s))

	switch s {
	case "true", "1", "yes", "on", "enabled":
		return true, true
	case "false", "0", "no", "off", "disabled":
		return false, true
	default:
		// Try standard parsing as fallback
		if b, err := strconv.ParseBool(s); err == nil {
			return b, true
		}
		return false, false
	}
}

// GetEnvMapping returns a copy of the environment variable to flag mapping.
// This is useful for documentation or configuration validation.
func GetEnvMapping() map[string]string {
	result := make(map[string]string, len(envMapping))
	for env, flag := range envMapping {
		result[env] = flag
	}
	return result
}

// FlagToEnvVar returns the environment variable name for a given flag.
// Returns an empty string if the flag is not found.
func FlagToEnvVar(flag string) string {
	for env, f := range envMapping {
		if f == flag {
			return env
		}
	}
	return ""
}
