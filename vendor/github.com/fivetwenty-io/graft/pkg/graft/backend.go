package graft

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"
)

// Backend is the public extension point for custom secret/parameter
// sources. Register an implementation with WithBackend (at engine
// construction) or Engine.RegisterBackend (at runtime); the
// vault/awsparam/awssecret/nats operators consult the registered backend
// for their name (see docs/developer-guide/custom-backends.md) instead of
// internal/backends when features.FeatureBackendRegistry is enabled on the
// engine.
//
// A Backend implementation must be safe for concurrent use: Get/GetBatch/
// Health may be called concurrently by parallel operator evaluation
// (see pkg/graft/evaluator_parallel.go).
type Backend interface {
	// Name returns the backend's registry identifier. The
	// vault/awsparam/awssecret/nats operators look up a backend under
	// their own operator name ("vault", "awsparam", "awssecret", "nats");
	// a backend registered under any other name is never consulted by
	// those operators (it is still reachable via Engine.GetBackend for
	// custom operators that want it).
	Name() string

	// Get retrieves the value at path. For the vault and nats operators,
	// path is passed exactly as it appears in the operator call (e.g. the
	// full `"secret/db:password"` string for a
	// `(( vault "secret/db:password" ))` call). For awsparam/awssecret,
	// any `?stage=`/`?key=` query suffix is stripped by graft before the
	// backend sees the path (see custom-backends.md); beyond that, the
	// backend, not graft, owns how to interpret it.
	//
	// A missing path must be reported by returning an error for which
	// errors.Is(err, ErrBackendNotFound) is true (returning
	// ErrBackendNotFound itself, or a wrapped copy, both satisfy this).
	// The vault operator maps that specifically into the same
	// "secret <path> not found" shape the built-in Vault backend
	// produces, keeping the Genesis compatibility contract's
	// not-found detection working for custom backends too.
	Get(ctx context.Context, path string) (interface{}, error)

	// GetBatch retrieves multiple paths. No graft operator calls this
	// today - there is no batching call site in internal/backends for it
	// to be designed against (see the "Batching" section of
	// docs/developer-guide/custom-backends.md) - so implementations are
	// free to implement it as a simple loop over Get. SequentialGetBatch
	// does exactly that and is the recommended default.
	GetBatch(ctx context.Context, paths []string) (map[string]interface{}, error)

	// Health reports whether the backend is currently reachable. Not
	// called by any graft operator; it exists for callers (health-check
	// endpoints, readiness probes) that hold an Engine reference and want
	// to check backend connectivity directly via Engine.GetBackend.
	Health(ctx context.Context) error

	// Close releases backend resources. Not called automatically by
	// Engine.UnregisterBackend or engine teardown (the engine has no
	// teardown method) - callers that construct a Backend with resources
	// to release own its Close call, exactly as they own the
	// resource-owning value returned by, e.g., NewRedisBackend in the
	// docs.
	Close() error
}

// TargetedBackend is an optional capability, checked with a type
// assertion (`if tb, ok := backend.(TargetedBackend); ok`), for backends
// that support the "@target" operator-call syntax (e.g.
// `(( vault@production "secret/path:key" ))`). A backend that does not
// implement TargetedBackend cannot be used with "@target": the operator
// returns a configuration error rather than silently using the wrong
// instance or silently ignoring the target.
type TargetedBackend interface {
	Backend

	// GetWithTarget retrieves path using the named target's
	// configuration. target is never empty when called by a graft
	// operator (an empty target uses Get instead).
	GetWithTarget(ctx context.Context, target, path string) (interface{}, error)
}

// ErrBackendNotFound is the sentinel a Backend implementation returns (or
// wraps) from Get/GetWithTarget to report a missing path. See Backend.Get's
// doc comment for how the vault operator maps this into the Genesis
// compatibility contract's "secret <path> not found" shape.
var ErrBackendNotFound = errors.New("backend: path not found")

// BackendError carries structured context about a Backend failure.
// Operators never return *BackendError as the outermost error: it is
// always wrapped inside a *GraftError (Type: ExternalError), reachable via
// errors.As(err, &backendErr) - see NewExternalError. This keeps
// *BackendError's own Error() text out of the Genesis-compatibility-pinned
// error surface: only PathError/MultiError/the vault "secret ... not
// found"/"invalid argument ..." strings are contract-pinned (see
// docs/spruce/genesis-compat-contract.md), and BackendError is new surface
// with no such constraint, so it is free to carry as much diagnostic
// detail as useful without risking that contract.
type BackendError struct {
	// Backend is the registry name the failing backend was registered
	// under (Backend.Name()).
	Backend string
	// Target is the "@target" name in effect, or "" if none.
	Target string
	// Path is the path argument passed to Get/GetWithTarget.
	Path string
	// Message is a human-readable description of the failure.
	Message string
	// Cause is the underlying error, reachable via Unwrap.
	Cause error
}

// Error renders "backend %q[@target][ path %q]: message".
func (e *BackendError) Error() string {
	s := fmt.Sprintf("backend %q", e.Backend)
	if e.Target != "" {
		s += "@" + e.Target
	}
	if e.Path != "" {
		s += fmt.Sprintf(" path %q", e.Path)
	}
	s += ": " + e.Message
	return s
}

// Unwrap returns Cause, so errors.Is(err, ErrBackendNotFound) and
// errors.As see through BackendError to the underlying error.
func (e *BackendError) Unwrap() error {
	return e.Cause
}

// RetryConfig configures the registry's generic retry wrapper, applied by
// WithBackendRetry to a single named backend's Get/GetWithTarget calls (not
// GetBatch - see BackendCache's doc comment for the same limitation and
// why). It never touches the retry logic already built into
// internal/backends (e.g. nats.CreateConnectionWithRetry): that remains
// exactly as before regardless of RetryConfig, both because it operates at
// a different layer (connection setup, not per-Get retry) and because
// built-in backends are never placed in the registry (see the "no
// eager built-in adapters" note on DefaultEngine.RegisterBackend).
type RetryConfig struct {
	// MaxAttempts is the total number of attempts (including the first),
	// not the number of retries. Non-positive values are treated as 1
	// (no retry).
	MaxAttempts int
	// InitialInterval is the delay before the second attempt.
	// Non-positive values default to 100ms.
	InitialInterval time.Duration
	// MaxInterval caps the delay after Multiplier backoff is applied.
	// Non-positive values default to InitialInterval (i.e. no growth).
	MaxInterval time.Duration
	// Multiplier scales the delay after each failed attempt.
	// Non-positive values default to 1 (constant delay).
	Multiplier float64
	// RetryableErrors decides whether a failed attempt's error should be
	// retried. A nil func treats every error as retryable except one for
	// which errors.Is(err, ErrBackendNotFound) is true (retrying a
	// definite "not found" wastes MaxAttempts attempts on an outcome
	// that will not change).
	RetryableErrors func(error) bool
}

// TLSConfig is a reusable TLS parameter bag for Backend implementations
// that need to establish their own TLS connections (e.g. to a custom
// secret store). graft's registry and operators never read this type; it
// exists as a documented, reusable shape so custom backends do not each
// invent their own, mirroring the fields internal/backends/{vault,nats}
// already carry natively (VAULT_SKIP_VERIFY, NATS_CERT_FILE, etc.).
type TLSConfig struct {
	CertFile   string
	KeyFile    string
	CAFile     string
	SkipVerify bool
	ServerName string
}

// BackendCache is a pluggable cache implementation for the registry's
// generic caching wrapper (WithBackendCache). It caches Get/GetWithTarget
// results only, keyed by path (GetWithTarget results are keyed by
// "target\x00path", mirroring the namespacing internal/backends/vault
// already uses to keep two targets' entries from colliding) - GetBatch is
// unaffected, since no graft operator calls it (see Backend.GetBatch).
type BackendCache interface {
	Get(key string) (interface{}, bool)
	Set(key string, value interface{}, ttl time.Duration)
	Delete(key string)
	Clear()
}

// DefaultBackendCacheTTL is the TTL the registry's caching wrapper passes
// to BackendCache.Set. There is no WithBackendCache parameter for this
// today - callers that need a different TTL should implement BackendCache
// with their own fixed or per-key TTL policy instead of relying on the
// value the wrapper passes in.
const DefaultBackendCacheTTL = 5 * time.Minute

// AuditLogger receives one LogAccess call per registry-mediated
// Get/GetWithTarget call (not GetBatch), whether or not WithBackendCache
// or WithBackendRetry also apply to that backend. Registering an
// AuditLogger via WithAuditLogger does not touch
// internal/backends/nats.Config.AuditLogging, which is a separate,
// pre-existing, live mechanism for NATS's own audit trail
// (nats/client.go); the two can be used together without conflict.
type AuditLogger interface {
	LogAccess(ctx context.Context, backend, path string, success bool, err error)
}

// SequentialGetBatch is a ready-made GetBatch implementation for Backend
// authors who do not need real batching: it calls get once per path and
// collects the results, stopping and returning the first error
// encountered (matching Backend.GetBatch's single (map, error) return -
// there is no way to report a per-path partial failure through that
// signature). A path for which get returns
// errors.Is(err, ErrBackendNotFound) is treated as "no entry" rather than
// a hard failure: it is omitted from the result map instead of aborting
// the batch, matching GetBatch's doc comment ("only entries that exist").
func SequentialGetBatch(ctx context.Context, paths []string, get func(ctx context.Context, path string) (interface{}, error)) (map[string]interface{}, error) {
	results := make(map[string]interface{}, len(paths))
	for _, path := range paths {
		val, err := get(ctx, path)
		if err != nil {
			if errors.Is(err, ErrBackendNotFound) {
				continue
			}
			return nil, err
		}
		results[path] = val
	}
	return results, nil
}

// registerBackendLocked stores b in e.backends under its own Name(),
// wrapped with whatever cache/retry/audit configuration is already set for
// that name (via WithBackendCache/WithBackendRetry/WithAuditLogger). The
// caller must hold e.opMutex for writing.
func (e *DefaultEngine) registerBackendLocked(b Backend) {
	name := b.Name()
	var cache BackendCache
	if e.backendCaches != nil {
		cache = e.backendCaches[name]
	}
	var retry *RetryConfig
	if e.backendRetry != nil {
		if cfg, ok := e.backendRetry[name]; ok {
			cfgCopy := cfg
			retry = &cfgCopy
		}
	}
	e.backends[name] = wrapBackendForRegistry(name, b, cache, retry, e.auditLogger)
}

// RegisterBackend registers b under its own Name(), replacing any backend
// previously registered under that name (re-registration is deliberately
// silent rather than an error: MockEngine-style test seeding re-registers
// backends routinely - see the C6 mock-engine cluster). b is wrapped with
// any cache/retry configuration already set for its name via
// WithBackendCache/WithBackendRetry, and with the engine-wide AuditLogger
// if one is set via WithAuditLogger.
//
// Only backends explicitly registered here (or via WithBackend at
// construction) are ever placed in the registry: built-in vault/AWS/NATS
// support is never auto-registered as an adapter (see this method's
// design note below), so GetBackend("vault") returns (nil, false) on an
// engine that has not had a custom vault backend registered, even though
// the vault operator works normally against internal/backends.
//
// Design note: the original plan called for seeding the registry with
// adapters wrapping internal/backends/{vault,aws,nats}. Those packages
// import pkg/graft (for graft.Engine/graft.DEBUG), so pkg/graft cannot
// import them back to construct such adapters without an import cycle.
// Rather than restructure that import direction, the operators
// (pkg/graft/operators, which already imports both) consult the registry
// first and fall back to their existing internal/backends call unchanged
// when the registry has nothing registered for their name - observably
// identical to "seeded with a pass-through adapter, which was never
// overridden".
func (e *DefaultEngine) RegisterBackend(b Backend) error {
	if b == nil {
		return NewValidationError("RegisterBackend: backend must not be nil")
	}
	if b.Name() == "" {
		return NewValidationError("RegisterBackend: backend Name() must not be empty")
	}

	e.opMutex.Lock()
	defer e.opMutex.Unlock()

	if e.backends == nil {
		e.backends = make(map[string]Backend)
	}
	e.registerBackendLocked(b)
	return nil
}

// GetBackend returns the backend registered under name (already wrapped
// with any configured cache/retry/audit behavior), or (nil, false) if none
// is registered.
func (e *DefaultEngine) GetBackend(name string) (Backend, bool) {
	e.opMutex.RLock()
	defer e.opMutex.RUnlock()

	b, ok := e.backends[name]
	return b, ok
}

// ListBackends returns the names of every currently registered backend, in
// sorted order.
func (e *DefaultEngine) ListBackends() []string {
	e.opMutex.RLock()
	defer e.opMutex.RUnlock()

	names := make([]string, 0, len(e.backends))
	for name := range e.backends {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// UnregisterBackend removes the backend registered under name. Returns an
// error if no backend is registered under name. It does not call the
// backend's Close - see Backend.Close's doc comment.
func (e *DefaultEngine) UnregisterBackend(name string) error {
	e.opMutex.Lock()
	defer e.opMutex.Unlock()

	if _, ok := e.backends[name]; !ok {
		return NewValidationError(fmt.Sprintf("UnregisterBackend: no backend registered under %q", name))
	}
	delete(e.backends, name)
	return nil
}

// WithBackend registers a custom backend at engine construction time,
// equivalent to calling Engine.RegisterBackend(b) immediately after
// NewEngine returns. Passing a nil b is a no-op (silently ignored,
// mirroring the nil-tolerance of WithBackendCache/WithAuditLogger below).
//
// Also works with DefaultEngine.Configure, which registers every pending
// backend the same way NewEngine does - validated up front (nil backend,
// empty Name()) so a failing registration leaves the engine's
// configuration untouched, exactly like Configure's pending-custom-
// operator validation.
func WithBackend(b Backend) EngineOption {
	return func(opts *EngineOptions) {
		if b == nil {
			return
		}
		if opts.Backends == nil {
			opts.Backends = make(map[string]Backend)
		}
		opts.Backends[b.Name()] = b
	}
}

// WithBackendRetry configures the registry's generic retry wrapper for the
// backend registered under name (whether registered before or after this
// option is applied - see RegisterBackend). Has no effect until a backend
// with a matching Name() is registered. Also works with
// DefaultEngine.Configure, applied before that call's pending backend
// registrations so a backend registered in the same Configure call picks
// up this retry configuration - same as NewEngine.
func WithBackendRetry(name string, cfg RetryConfig) EngineOption {
	return func(opts *EngineOptions) {
		if opts.BackendRetryConfigs == nil {
			opts.BackendRetryConfigs = make(map[string]RetryConfig)
		}
		opts.BackendRetryConfigs[name] = cfg
	}
}

// WithBackendCache configures the registry's generic caching wrapper for
// the backend registered under name. Has no effect until a backend with a
// matching Name() is registered. Passing a nil c is a no-op. Also works
// with DefaultEngine.Configure - see WithBackendRetry's doc comment for
// the ordering guarantee relative to Configure's pending backend
// registrations.
func WithBackendCache(name string, c BackendCache) EngineOption {
	return func(opts *EngineOptions) {
		if c == nil {
			return
		}
		if opts.BackendCaches == nil {
			opts.BackendCaches = make(map[string]BackendCache)
		}
		opts.BackendCaches[name] = c
	}
}

// WithAuditLogger sets the engine-wide AuditLogger applied to every
// registry-mediated backend call (see AuditLogger's doc comment). Passing
// a nil l is a no-op. Also works with DefaultEngine.Configure - see
// WithBackendRetry's doc comment for the ordering guarantee relative to
// Configure's pending backend registrations.
func WithAuditLogger(l AuditLogger) EngineOption {
	return func(opts *EngineOptions) {
		if l == nil {
			return
		}
		opts.AuditLoggerInstance = l
	}
}

// WithBackendRegistry explicitly enables or disables
// features.FeatureBackendRegistry on the constructed engine, overriding
// whatever WithFeatureFlags (or the default, off) computed - the same
// override relationship WithSkipVault/WithSkipAws/WithSkipNats already
// have with a supplied FeatureFlags for their own concerns.
//
// This is the only way to toggle FeatureBackendRegistry available to
// callers outside this module: WithFeatureFlags takes a
// *features.FeatureFlags, and internal/features cannot be imported from
// outside github.com/fivetwenty-io/graft. Every other option in this file
// (WithBackend, WithBackendRetry, WithBackendCache, WithAuditLogger) is
// already flag-agnostic and works the same regardless of how the flag
// ends up set.
//
// Also works with DefaultEngine.Configure, which applies it the same way
// NewEngine does.
func WithBackendRegistry(enabled bool) EngineOption {
	return func(opts *EngineOptions) {
		opts.backendRegistryEnabled = &enabled
	}
}
