package graft

import "time"

// Option configures a new Engine (see NewEngine) or an incremental change
// to an existing one (see DefaultEngine.Configure). It is an alias for
// EngineOption, not a new type: []graft.Option and []graft.EngineOption are
// the same type, so every existing EngineOption-returning function (and
// every existing call site that names EngineOption explicitly) keeps
// compiling unchanged.
type Option = EngineOption

// WithCacheSize sets the maximum number of entries the engine's operator
// result cache can hold. It does not itself enable caching - see WithCache
// or the engine's default (caching enabled) - it only affects the size of
// the cache when one is built. A non-positive size is ignored, leaving the
// current size unchanged, matching internal/cache's own WithMaxSize.
func WithCacheSize(size int) Option {
	return func(opts *EngineOptions) {
		if size > 0 {
			opts.CacheSize = size
		}
	}
}

// WithCacheDisabled disables the engine's operator result cache. It is
// equivalent to WithCache(false, 0) but does not require passing a
// meaningless size.
func WithCacheDisabled() Option {
	return func(opts *EngineOptions) {
		opts.EnableCache = false
	}
}

// WithCacheTTL sets a default time-to-live for entries in the engine's
// operator result cache: an entry set after this option takes effect
// expires and is evicted ttl after it was written. A zero or negative ttl
// disables expiration (entries live until evicted for capacity reasons),
// matching internal/cache's own WithTTL default. WithCacheTTL has no
// effect if caching ends up disabled (see WithCache/WithCacheDisabled and
// the feature-flag interaction documented on EngineOptions.EnableCache).
func WithCacheTTL(ttl time.Duration) Option {
	return func(opts *EngineOptions) {
		opts.CacheTTL = ttl
	}
}

// WithOperators registers a set of custom operators for the engine, merging
// them into any operators already configured (via WithCustomOperator or an
// earlier WithOperators) rather than replacing the set. Each entry behaves
// exactly like a WithCustomOperator(name, op) call: the operator is visible
// under name during evaluation on this engine, shadowing a built-in
// operator of the same name if one exists, once the engine is constructed
// (or, via Configure, applied to an existing one).
func WithOperators(ops map[string]Operator) Option {
	return func(opts *EngineOptions) {
		if len(ops) == 0 {
			return
		}
		if opts.CustomOperators == nil {
			opts.CustomOperators = make(map[string]Operator, len(ops))
		}
		for name, op := range ops {
			opts.CustomOperators[name] = op
		}
	}
}
