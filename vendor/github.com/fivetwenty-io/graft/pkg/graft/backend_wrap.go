package graft

import (
	"context"
	"errors"
	"time"
)

// wrappedBackend applies the registry's generic cache/retry/audit wrappers
// around inner. It implements Backend but never TargetedBackend directly -
// see wrapBackendForRegistry, which promotes to wrappedTargetedBackend when
// inner itself implements TargetedBackend, so a wrapped backend's
// capability set exactly matches the backend it wraps.
type wrappedBackend struct {
	name  string
	inner Backend
	cache BackendCache
	retry *RetryConfig
	audit AuditLogger
}

func (w *wrappedBackend) Name() string { return w.name }

func (w *wrappedBackend) GetBatch(ctx context.Context, paths []string) (map[string]interface{}, error) {
	// GetBatch is intentionally not cached/retried/audited - see
	// BackendCache and RetryConfig's doc comments: no graft operator
	// calls GetBatch today, so there is no observed access pattern to
	// design a batch-aware cache/retry policy against.
	return w.inner.GetBatch(ctx, paths)
}

func (w *wrappedBackend) Health(ctx context.Context) error {
	return w.inner.Health(ctx)
}

func (w *wrappedBackend) Close() error {
	return w.inner.Close()
}

func (w *wrappedBackend) Get(ctx context.Context, path string) (interface{}, error) {
	return w.fetch(ctx, "", path, func() (interface{}, error) {
		return w.inner.Get(ctx, path)
	})
}

// fetch implements the shared cache -> retry -> audit pipeline for both
// Get and wrappedTargetedBackend.GetWithTarget. cacheKey is path for Get,
// "target\x00path" for GetWithTarget (see BackendCache's doc comment).
// doFetch performs the real, unwrapped call.
//
// A ctx marked by WithNoCacheContext (the "(( op:nocache ... ))" modifier)
// bypasses both the cache read and the cache write: a nocache fetch must
// neither be served from nor poison/refresh the shared entry. Retry and
// audit still apply. The cache key itself is never altered by the
// modifier, so it cannot fragment or collide entries.
func (w *wrappedBackend) fetch(ctx context.Context, target, path string, doFetch func() (interface{}, error)) (interface{}, error) {
	cacheKey := path
	if target != "" {
		cacheKey = target + "\x00" + path
	}

	skipCache := noCacheFromContext(ctx)

	if w.cache != nil && !skipCache {
		if v, ok := w.cache.Get(cacheKey); ok {
			w.logAccess(ctx, path, true, nil)
			return v, nil
		}
	}

	var val interface{}
	var err error
	if w.retry != nil {
		val, err = retryFetch(ctx, *w.retry, doFetch)
	} else {
		val, err = doFetch()
	}

	w.logAccess(ctx, path, err == nil, err)

	if err != nil {
		return nil, err
	}

	if w.cache != nil && !skipCache {
		w.cache.Set(cacheKey, val, DefaultBackendCacheTTL)
	}
	return val, nil
}

// noCacheCtxKey marks a context produced by WithNoCacheContext.
type noCacheCtxKey struct{}

// WithNoCacheContext returns a context that instructs the registry's
// caching wrapper to bypass both the cache read and the cache write for
// backend calls made with it — the registry-side counterpart of the
// "(( op:nocache ... ))" expression modifier. Retry and audit wrapping
// are unaffected.
func WithNoCacheContext(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, noCacheCtxKey{}, true)
}

// noCacheFromContext reports whether ctx was marked by WithNoCacheContext.
func noCacheFromContext(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, _ := ctx.Value(noCacheCtxKey{}).(bool)
	return v
}

func (w *wrappedBackend) logAccess(ctx context.Context, path string, success bool, err error) {
	if w.audit != nil {
		w.audit.LogAccess(ctx, w.name, path, success, err)
	}
}

// wrappedTargetedBackend adds GetWithTarget on top of wrappedBackend, for
// the case where the wrapped backend implements TargetedBackend.
type wrappedTargetedBackend struct {
	wrappedBackend
	targetedInner TargetedBackend
}

func (w *wrappedTargetedBackend) GetWithTarget(ctx context.Context, target, path string) (interface{}, error) {
	return w.fetch(ctx, target, path, func() (interface{}, error) {
		return w.targetedInner.GetWithTarget(ctx, target, path)
	})
}

// wrapBackendForRegistry composes the registry's generic cache/retry/audit
// wrappers around b. cache and retry may each be nil (no wrapping for that
// concern); audit may be nil the same way. Returns a TargetedBackend when
// b itself implements TargetedBackend, and a plain Backend otherwise -
// wrapping never adds or removes the @target capability.
func wrapBackendForRegistry(name string, b Backend, cache BackendCache, retry *RetryConfig, audit AuditLogger) Backend {
	base := wrappedBackend{name: name, inner: b, cache: cache, retry: retry, audit: audit}
	if tb, ok := b.(TargetedBackend); ok {
		return &wrappedTargetedBackend{wrappedBackend: base, targetedInner: tb}
	}
	return &base
}

// retryFetch runs doFetch up to cfg.MaxAttempts times (see RetryConfig's
// doc comment for how each field defaults when non-positive/nil),
// returning the first successful result or the last error. It stops early,
// without consuming remaining attempts, when ctx is canceled during the
// inter-attempt delay, or when cfg.RetryableErrors (or the default
// not-ErrBackendNotFound policy) reports an error as non-retryable.
func retryFetch(ctx context.Context, cfg RetryConfig, doFetch func() (interface{}, error)) (interface{}, error) {
	attempts := cfg.MaxAttempts
	if attempts <= 0 {
		attempts = 1
	}
	interval := cfg.InitialInterval
	if interval <= 0 {
		interval = 100 * time.Millisecond
	}
	maxInterval := cfg.MaxInterval
	if maxInterval <= 0 {
		maxInterval = interval
	}
	multiplier := cfg.Multiplier
	if multiplier <= 0 {
		multiplier = 1
	}
	retryable := cfg.RetryableErrors
	if retryable == nil {
		retryable = func(err error) bool { return !errors.Is(err, ErrBackendNotFound) }
	}

	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(interval):
			}
			interval = time.Duration(float64(interval) * multiplier)
			if interval > maxInterval {
				interval = maxInterval
			}
		}

		val, err := doFetch()
		if err == nil {
			return val, nil
		}
		lastErr = err
		if !retryable(err) {
			return nil, err
		}
	}
	return nil, lastErr
}
