// Package features provides feature flag management for graft.
//
// Feature flags allow runtime control over experimental features and
// performance optimizations. Flags can be configured programmatically
// or via environment variables.
//
// Example usage:
//
//	flags := features.DefaultFlags()
//	if flags.IsEnabled(features.FeatureParallelEvaluation) {
//	    // Use parallel processing
//	}
//
//	// Enable a feature at runtime
//	flags.Enable(features.FeatureCaching)
//
//	// Load from environment
//	flags.LoadFromEnv()
package features

import (
	"sync"
)

// Feature flag constants define the available feature toggles.
const (
	// FeatureParallelEvaluation enables parallel processing of independent operations.
	// When enabled, graft will attempt to evaluate non-dependent operations concurrently.
	FeatureParallelEvaluation = "parallel_evaluation"

	// FeatureCaching enables operator result caching.
	// Cached results are reused when the same operation is performed on identical inputs.
	FeatureCaching = "caching"

	// FeatureMetrics enables metrics collection for performance monitoring.
	// Metrics include operation counts, timing data, and resource usage.
	FeatureMetrics = "metrics"

	// FeatureDebugLogging enables verbose debug output.
	// This provides detailed logging of internal operations for troubleshooting.
	FeatureDebugLogging = "debug_logging"

	// FeatureStrictTypeChecking enables strict type validation.
	// When enabled, type mismatches result in errors rather than coercion attempts.
	FeatureStrictTypeChecking = "strict_type_checking"

	// FeatureMemoryPools enables memory pooling for reduced allocations.
	// This can improve performance in high-throughput scenarios.
	FeatureMemoryPools = "memory_pools"

	// FeatureBackendRegistry enables the pkg/graft custom-backend registry
	// (graft.Backend/graft.WithBackend/Engine.RegisterBackend). When
	// disabled (the default), the vault/awsparam/awssecret/nats operators
	// resolve exclusively through internal/backends as before this flag
	// existed. When enabled, those operators first consult the engine's
	// backend registry for a custom backend registered under their name,
	// falling back to internal/backends when none is registered. See
	// docs/developer-guide/custom-backends.md.
	FeatureBackendRegistry = "backend_registry"
)

// AllFeatures returns a slice of all defined feature flag names.
func AllFeatures() []string {
	return []string{
		FeatureParallelEvaluation,
		FeatureCaching,
		FeatureMetrics,
		FeatureDebugLogging,
		FeatureStrictTypeChecking,
		FeatureMemoryPools,
		FeatureBackendRegistry,
	}
}

// FeatureFlags provides a thread-safe registry for feature flag management.
// It supports runtime enabling/disabling of features and environment-based configuration.
type FeatureFlags struct {
	mu    sync.RWMutex
	flags map[string]bool
}

// New creates a new FeatureFlags instance with no flags enabled.
func New() *FeatureFlags {
	return &FeatureFlags{
		flags: make(map[string]bool),
	}
}

// DefaultFlags returns a FeatureFlags instance with sensible default settings.
//
// Default enabled flags:
//   - FeatureCaching: Enabled for performance
//   - FeatureMemoryPools: Enabled for reduced allocations
//
// Default disabled flags:
//   - FeatureParallelEvaluation: Disabled by default for predictable behavior
//   - FeatureMetrics: Disabled to avoid overhead
//   - FeatureDebugLogging: Disabled for production use
//   - FeatureStrictTypeChecking: Disabled for backward compatibility
//   - FeatureBackendRegistry: Disabled for one release while the
//     package-global backend path and the registry path both exist; see
//     docs/developer-guide/custom-backends.md.
func DefaultFlags() *FeatureFlags {
	ff := New()

	// Performance features enabled by default
	ff.flags[FeatureCaching] = true
	ff.flags[FeatureMemoryPools] = true

	// Features disabled by default
	ff.flags[FeatureParallelEvaluation] = false
	ff.flags[FeatureMetrics] = false
	ff.flags[FeatureDebugLogging] = false
	ff.flags[FeatureStrictTypeChecking] = false
	ff.flags[FeatureBackendRegistry] = false

	return ff
}

// IsEnabled returns true if the specified feature flag is enabled.
// Returns false for unknown flags.
func (ff *FeatureFlags) IsEnabled(flag string) bool {
	ff.mu.RLock()
	defer ff.mu.RUnlock()

	return ff.flags[flag]
}

// Enable enables the specified feature flag.
func (ff *FeatureFlags) Enable(flag string) {
	ff.mu.Lock()
	defer ff.mu.Unlock()

	ff.flags[flag] = true
}

// Disable disables the specified feature flag.
func (ff *FeatureFlags) Disable(flag string) {
	ff.mu.Lock()
	defer ff.mu.Unlock()

	ff.flags[flag] = false
}

// Set sets the specified feature flag to the given enabled state.
func (ff *FeatureFlags) Set(flag string, enabled bool) {
	ff.mu.Lock()
	defer ff.mu.Unlock()

	ff.flags[flag] = enabled
}

// SetAll sets multiple feature flags at once.
func (ff *FeatureFlags) SetAll(flags map[string]bool) {
	ff.mu.Lock()
	defer ff.mu.Unlock()

	for flag, enabled := range flags {
		ff.flags[flag] = enabled
	}
}

// GetAll returns a copy of all current flag settings.
func (ff *FeatureFlags) GetAll() map[string]bool {
	ff.mu.RLock()
	defer ff.mu.RUnlock()

	result := make(map[string]bool, len(ff.flags))
	for flag, enabled := range ff.flags {
		result[flag] = enabled
	}

	return result
}

// Reset clears all flags and optionally reinitializes with defaults.
func (ff *FeatureFlags) Reset(useDefaults bool) {
	ff.mu.Lock()
	defer ff.mu.Unlock()

	ff.flags = make(map[string]bool)

	if useDefaults {
		ff.flags[FeatureCaching] = true
		ff.flags[FeatureMemoryPools] = true
		ff.flags[FeatureParallelEvaluation] = false
		ff.flags[FeatureMetrics] = false
		ff.flags[FeatureDebugLogging] = false
		ff.flags[FeatureStrictTypeChecking] = false
		ff.flags[FeatureBackendRegistry] = false
	}
}

// Clone creates a deep copy of the FeatureFlags instance.
func (ff *FeatureFlags) Clone() *FeatureFlags {
	ff.mu.RLock()
	defer ff.mu.RUnlock()

	clone := New()
	for flag, enabled := range ff.flags {
		clone.flags[flag] = enabled
	}

	return clone
}

// global holds the global feature flags instance.
var (
	global     *FeatureFlags
	globalOnce sync.Once
)

// Global returns the global FeatureFlags instance.
// The instance is lazily initialized with default settings on first access.
func Global() *FeatureFlags {
	globalOnce.Do(func() {
		global = DefaultFlags()
		global.LoadFromEnv()
	})
	return global
}

// ResetGlobal resets the global instance for testing purposes.
// This should not be used in production code.
func ResetGlobal() {
	globalOnce = sync.Once{}
	global = nil
}
