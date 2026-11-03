package graft

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/fivetwenty-io/graft/internal/features"
)

// BackendCall records one Get call a MockEngine's seeded vault/awsparam/
// awssecret/nats backend observed, for VaultCalls/AWSCalls/NATSCalls/
// WasVaultCalled assertions. Target is always "" today: MockEngine's
// backends do not implement TargetedBackend, so "@target" operator calls
// against a MockEngine fall through to a configuration error rather than
// being recorded here (see custom-backends.md's TargetedBackend note).
type BackendCall struct {
	Backend string
	Path    string
	Key     string
	Target  string
	Time    time.Time
}

// MockEngine is a graft.Engine for tests that must not reach real Vault,
// AWS, or NATS services. MockVault/MockAWSParam/MockAWSSecret/MockNATS seed
// canned responses, MockVaultError/MockAWSParamError inject failures, and
// VaultCalls/AWSCalls/NATSCalls/WasVaultCalled expose what was looked up
// during Evaluate.
//
// MockEngine embeds a real *DefaultEngine, so ParseYAML, Merge, Evaluate,
// and every other Engine method behave exactly as they do on a real
// engine; only vault/awsparam/awssecret/nats resolution is intercepted.
// The interception is not special-cased inside MockEngine: it registers
// four ordinary graft.Backend implementations (see backend.go) under the
// operator names "vault", "awsparam", "awssecret", "nats", and force-
// enables features.FeatureBackendRegistry so the real operators consult
// them instead of internal/backends. NewMockEngine clones the
// *features.FeatureFlags it force-enables the flag on (see NewMockEngine),
// so a production Engine built through NewEngine from an
// options-supplied *features.FeatureFlags never observes this flip even
// when that same *features.FeatureFlags value was also passed to
// NewMockEngine - MockEngine's interception is unreachable from any
// non-MockEngine code path.
type MockEngine struct {
	*DefaultEngine

	mu sync.Mutex

	vaultValues map[string]interface{}
	vaultErrors map[string]error

	awsParamValues map[string]string
	awsParamErrors map[string]error

	awsSecretValues map[string]interface{}

	natsValues map[string]interface{}

	vaultCalls []BackendCall
	awsCalls   []BackendCall
	natsCalls  []BackendCall
}

// NewMockEngine creates a MockEngine. opts configures the underlying engine
// exactly as they would for NewEngine (parallelism, caching, custom
// operators, and so on). NewMockEngine has no *testing.T to report a
// construction failure through (matching this cluster's target
// signature, NewMockEngine(opts ...Option) *MockEngine with no error
// return) - an option that makes NewEngine itself fail (e.g. negative
// concurrency) panics here, the same contract template.Must documents for
// the same reason.
func NewMockEngine(opts ...Option) *MockEngine {
	engine, err := NewEngine(opts...)
	if err != nil {
		panic(fmt.Sprintf("graft.NewMockEngine: %v", err))
	}

	de, ok := engine.(*DefaultEngine)
	if !ok {
		panic(fmt.Sprintf("graft.NewMockEngine: internal error: engine is not *DefaultEngine (%T)", engine))
	}

	m := &MockEngine{
		DefaultEngine:   de,
		vaultValues:     make(map[string]interface{}),
		vaultErrors:     make(map[string]error),
		awsParamValues:  make(map[string]string),
		awsParamErrors:  make(map[string]error),
		awsSecretValues: make(map[string]interface{}),
		natsValues:      make(map[string]interface{}),
	}

	// Clone before enabling: de.Features may be the exact
	// *features.FeatureFlags value a caller passed in via
	// WithFeatureFlags (createEngineFromOptions stores it by pointer,
	// without cloning), so enabling FeatureBackendRegistry in place would
	// leak into every other Engine/MockEngine built from that same
	// caller-supplied value - see this type's doc comment.
	de.Features = de.Features.Clone()
	de.Features.Enable(features.FeatureBackendRegistry)
	m.registerMockBackends()

	return m
}

// registerMockBackends registers this MockEngine's four backends. Called
// once from NewMockEngine; Reset does not re-call this; it only clears the
// seeded data the already-registered backends read from.
func (m *MockEngine) registerMockBackends() {
	backends := []Backend{
		&mockVaultBackend{m: m},
		&mockAWSParamBackend{m: m},
		&mockAWSSecretBackend{m: m},
		&mockNATSBackend{m: m},
	}
	for _, b := range backends {
		if err := m.RegisterBackend(b); err != nil {
			// Name() is a compile-time-known non-empty literal for every
			// backend above, so RegisterBackend's only two failure modes
			// (nil backend, empty Name()) cannot occur here.
			panic(fmt.Sprintf("graft.NewMockEngine: internal error: RegisterBackend(%q): %v", b.Name(), err))
		}
	}
}

// MockVault seeds the value the vault backend returns for a
// `(( vault "path" ))` lookup, where path is the full "secret/db:password"
// string as it appears in the operator call. Seeding a path that
// previously had an injected error via MockVaultError clears that error.
func (m *MockEngine) MockVault(path string, value interface{}) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.vaultValues[path] = value
	delete(m.vaultErrors, path)
}

// MockVaultPath is MockVault for callers who have path and key as separate
// strings rather than the combined "path:key" form; it seeds the same
// value MockVault(path+":"+key, value) would.
func (m *MockEngine) MockVaultPath(path, key string, value interface{}) {
	m.MockVault(path+":"+key, value)
}

// MockVaultError seeds an error the vault backend returns for path.
// Seeding an error for a path that previously had a value via MockVault or
// MockVaultPath clears that value.
func (m *MockEngine) MockVaultError(path string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.vaultErrors[path] = err
	delete(m.vaultValues, path)
}

// MockAWSParam seeds the value the awsparam backend returns for a
// `(( awsparam "name" ))` lookup. Seeding a name that previously had an
// injected error via MockAWSParamError clears that error.
func (m *MockEngine) MockAWSParam(name string, value string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.awsParamValues[name] = value
	delete(m.awsParamErrors, name)
}

// MockAWSParamJSON is MockAWSParam for a structured value: value is
// JSON-encoded and the resulting string is seeded as the parameter's
// value, matching how AWS Parameter Store itself only ever stores strings
// - "JSON" describes the content, not a different storage shape. A value
// that fails to JSON-encode (e.g. a channel or a cyclic structure) seeds
// an error instead, surfaced the same way MockAWSParamError's error is,
// the first time name is looked up.
func (m *MockEngine) MockAWSParamJSON(name string, value interface{}) {
	encoded, err := json.Marshal(value)
	if err != nil {
		m.MockAWSParamError(name, fmt.Errorf("graft: MockAWSParamJSON(%q): %w", name, err))
		return
	}
	m.MockAWSParam(name, string(encoded))
}

// MockAWSParamError seeds an error the awsparam backend returns for name.
// Seeding an error for a name that previously had a value via
// MockAWSParam or MockAWSParamJSON clears that value.
func (m *MockEngine) MockAWSParamError(name string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.awsParamErrors[name] = err
	delete(m.awsParamValues, name)
}

// MockAWSSecret seeds the value the awssecret backend returns for a
// `(( awssecret "name" ))` lookup. value may be a plain string (a scalar
// secret) or a structured value such as map[string]string (a multi-field
// secret); it is returned to the caller unmodified - unlike
// MockAWSParamJSON, there is no JSON-encoding step, since AWS Secrets
// Manager secrets are not restricted to strings the way parameters are.
func (m *MockEngine) MockAWSSecret(name string, value interface{}) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.awsSecretValues[name] = value
}

// MockNATS seeds the value the nats backend returns for a
// `(( nats "subject" ))` lookup, where subject carries whatever prefix
// convention the real nats operator's argument does (e.g. "kv:bucket/key",
// "obj:bucket/file").
func (m *MockEngine) MockNATS(subject string, value interface{}) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.natsValues[subject] = value
}

// VaultCalls returns every Get the vault backend has observed, in call
// order, since construction or the last Reset.
func (m *MockEngine) VaultCalls() []BackendCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]BackendCall, len(m.vaultCalls))
	copy(out, m.vaultCalls)
	return out
}

// AWSCalls returns every Get the awsparam and awssecret backends have
// observed combined, in call order, since construction or the last Reset.
// BackendCall.Backend distinguishes which of the two served each call.
func (m *MockEngine) AWSCalls() []BackendCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]BackendCall, len(m.awsCalls))
	copy(out, m.awsCalls)
	return out
}

// NATSCalls returns every Get the nats backend has observed, in call
// order, since construction or the last Reset.
func (m *MockEngine) NATSCalls() []BackendCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]BackendCall, len(m.natsCalls))
	copy(out, m.natsCalls)
	return out
}

// WasVaultCalled reports whether the vault backend has observed a Get for
// path since construction or the last Reset.
func (m *MockEngine) WasVaultCalled(path string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.vaultCalls {
		if c.Path == path {
			return true
		}
	}
	return false
}

// Reset clears every seeded value, seeded error, and recorded call for all
// four backends, returning the MockEngine to its just-constructed state.
// It does not unregister or re-register the backends themselves, and does
// not affect the underlying DefaultEngine's own state (document memory,
// operator registrations, and so on).
func (m *MockEngine) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.vaultValues = make(map[string]interface{})
	m.vaultErrors = make(map[string]error)
	m.awsParamValues = make(map[string]string)
	m.awsParamErrors = make(map[string]error)
	m.awsSecretValues = make(map[string]interface{})
	m.natsValues = make(map[string]interface{})
	m.vaultCalls = nil
	m.awsCalls = nil
	m.natsCalls = nil
}

// mockVaultBackend implements Backend, backed by MockEngine's seeded vault
// data. Registered under "vault".
type mockVaultBackend struct{ m *MockEngine }

func (b *mockVaultBackend) Name() string { return "vault" }

func (b *mockVaultBackend) Get(_ context.Context, path string) (interface{}, error) {
	b.m.mu.Lock()
	defer b.m.mu.Unlock()

	b.m.vaultCalls = append(b.m.vaultCalls, BackendCall{Backend: "vault", Path: path, Time: time.Now()})

	if err, hasErr := b.m.vaultErrors[path]; hasErr {
		return nil, err
	}
	value, found := b.m.vaultValues[path]
	if !found {
		return nil, fmt.Errorf("%w: vault path %q", ErrBackendNotFound, path)
	}
	return value, nil
}

func (b *mockVaultBackend) GetBatch(ctx context.Context, paths []string) (map[string]interface{}, error) {
	return SequentialGetBatch(ctx, paths, b.Get)
}

func (b *mockVaultBackend) Health(_ context.Context) error { return nil }
func (b *mockVaultBackend) Close() error                   { return nil }

// mockAWSParamBackend implements Backend, backed by MockEngine's seeded
// awsparam data. Registered under "awsparam".
type mockAWSParamBackend struct{ m *MockEngine }

func (b *mockAWSParamBackend) Name() string { return "awsparam" }

func (b *mockAWSParamBackend) Get(_ context.Context, path string) (interface{}, error) {
	b.m.mu.Lock()
	defer b.m.mu.Unlock()

	b.m.awsCalls = append(b.m.awsCalls, BackendCall{Backend: "awsparam", Path: path, Time: time.Now()})

	if err, hasErr := b.m.awsParamErrors[path]; hasErr {
		return nil, err
	}
	value, found := b.m.awsParamValues[path]
	if !found {
		return nil, fmt.Errorf("%w: awsparam %q", ErrBackendNotFound, path)
	}
	return value, nil
}

func (b *mockAWSParamBackend) GetBatch(ctx context.Context, paths []string) (map[string]interface{}, error) {
	return SequentialGetBatch(ctx, paths, b.Get)
}

func (b *mockAWSParamBackend) Health(_ context.Context) error { return nil }
func (b *mockAWSParamBackend) Close() error                   { return nil }

// mockAWSSecretBackend implements Backend, backed by MockEngine's seeded
// awssecret data. Registered under "awssecret". There is no
// MockAWSSecretError (see the plan's target API); a lookup for an
// unseeded name reports ErrBackendNotFound the same as an unseeded
// awsparam name.
type mockAWSSecretBackend struct{ m *MockEngine }

func (b *mockAWSSecretBackend) Name() string { return "awssecret" }

func (b *mockAWSSecretBackend) Get(_ context.Context, path string) (interface{}, error) {
	b.m.mu.Lock()
	defer b.m.mu.Unlock()

	b.m.awsCalls = append(b.m.awsCalls, BackendCall{Backend: "awssecret", Path: path, Time: time.Now()})

	value, found := b.m.awsSecretValues[path]
	if !found {
		return nil, fmt.Errorf("%w: awssecret %q", ErrBackendNotFound, path)
	}
	return value, nil
}

func (b *mockAWSSecretBackend) GetBatch(ctx context.Context, paths []string) (map[string]interface{}, error) {
	return SequentialGetBatch(ctx, paths, b.Get)
}

func (b *mockAWSSecretBackend) Health(_ context.Context) error { return nil }
func (b *mockAWSSecretBackend) Close() error                   { return nil }

// mockNATSBackend implements Backend, backed by MockEngine's seeded nats
// data. Registered under "nats". There is no MockNATSError (see the
// plan's target API); a lookup for an unseeded subject reports
// ErrBackendNotFound the same as an unseeded vault path.
type mockNATSBackend struct{ m *MockEngine }

func (b *mockNATSBackend) Name() string { return "nats" }

func (b *mockNATSBackend) Get(_ context.Context, subject string) (interface{}, error) {
	b.m.mu.Lock()
	defer b.m.mu.Unlock()

	b.m.natsCalls = append(b.m.natsCalls, BackendCall{Backend: "nats", Path: subject, Time: time.Now()})

	value, found := b.m.natsValues[subject]
	if !found {
		return nil, fmt.Errorf("%w: nats subject %q", ErrBackendNotFound, subject)
	}
	return value, nil
}

func (b *mockNATSBackend) GetBatch(ctx context.Context, paths []string) (map[string]interface{}, error) {
	return SequentialGetBatch(ctx, paths, b.Get)
}

func (b *mockNATSBackend) Health(_ context.Context) error { return nil }
func (b *mockNATSBackend) Close() error                   { return nil }
