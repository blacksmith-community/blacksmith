package graft

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	vaultapi "github.com/hashicorp/vault/api"
)

// VaultConfig carries per-engine Vault connection configuration for
// WithVault and WithVaultTarget. Every field is optional except Address and
// Token, which must both be set (directly, or via ${ENV_VAR} references -
// see the Address/Token doc comments below) before the first Get/
// GetWithTarget call against the configuration they belong to; an engine
// constructed with WithVault/WithVaultTarget never validates connectivity
// eagerly, so a misconfigured VaultConfig only surfaces as an error from
// the first `(( vault ... ))` evaluation that reaches it.
type VaultConfig struct {
	// Address is the Vault server URL (e.g. "https://vault.example.com").
	// Expanded with os.ExpandEnv before use, so "$VAULT_ADDR" or
	// "${VAULT_ADDR}" work exactly as they would in a shell, matching the
	// expansion internal/backends/vault already applies to its
	// environment-sourced configuration.
	Address string
	// Token is the Vault authentication token. Expanded with os.ExpandEnv,
	// same as Address.
	Token string
	// Namespace is the Vault Enterprise namespace, or "" for none.
	// Expanded with os.ExpandEnv, same as Address.
	Namespace string
	// SkipVerify disables TLS certificate verification. Only meaningful
	// when Address uses "https://"; ignored otherwise.
	SkipVerify bool
	// Timeout bounds each Vault request. Non-positive leaves the
	// hashicorp/vault/api client's own default (60s) in effect.
	Timeout time.Duration
	// PoolSize sets the HTTP transport's MaxIdleConnsPerHost/MaxIdleConns.
	// Non-positive leaves Go's http.Transport zero-value default (2 idle
	// connections per host) in effect. This is PoolSize's entire effect -
	// there is no separate connection-pool abstraction to configure.
	PoolSize int
}

// vaultOptionBackend is the graft.Backend registered under the name
// "vault" by WithVault/WithVaultTarget. It is a from-scratch, minimal
// Vault KV reader built directly on github.com/hashicorp/vault/api rather
// than internal/backends/vault: internal/backends/vault imports pkg/graft
// (for graft.Engine, graft.DEBUG), so pkg/graft cannot import it back
// without an import cycle (see RegisterBackend's "Design note" in
// backend.go). Concretely this means vaultOptionBackend does not share
// internal/backends/vault's process-global client pool, its SecretCache,
// or its .svtoken-file fallback; it builds one hashicorp/vault/api.Client
// per configured target (WithVault's "" target, plus one per
// WithVaultTarget name), lazily, on first use, and reuses it afterward.
// Retry and caching behavior beyond the vault SDK's own built-in 5xx retry
// (2 retries by default) are available by layering WithBackendRetry("vault",
// ...) and WithBackendCache("vault", ...) on top - vaultOptionBackend
// itself does neither, by design, so the two concerns are not duplicated.
type vaultOptionBackend struct {
	mu      sync.RWMutex
	configs map[string]VaultConfig      // "" is the WithVault default; other keys are WithVaultTarget names.
	clients map[string]*vaultapi.Client // lazily built, same keys as configs.
}

func newVaultOptionBackend() *vaultOptionBackend {
	return &vaultOptionBackend{
		configs: make(map[string]VaultConfig),
		clients: make(map[string]*vaultapi.Client),
	}
}

// Name implements Backend.
func (b *vaultOptionBackend) Name() string { return "vault" }

// Get implements Backend using the "" (WithVault default) configuration.
func (b *vaultOptionBackend) Get(ctx context.Context, path string) (interface{}, error) {
	return b.get(ctx, "", path)
}

// GetWithTarget implements TargetedBackend using the WithVaultTarget
// configuration registered under target. An empty target is treated the
// same as Get (falls back to the "" default) rather than erroring, since a
// direct caller (not a graft operator, which never passes an empty target
// to GetWithTarget - see TargetedBackend's doc comment) may reasonably
// call it that way.
func (b *vaultOptionBackend) GetWithTarget(ctx context.Context, target, path string) (interface{}, error) {
	return b.get(ctx, target, path)
}

func (b *vaultOptionBackend) get(ctx context.Context, target, path string) (interface{}, error) {
	client, err := b.clientFor(target)
	if err != nil {
		return nil, err
	}

	secretPath, subkey := splitVaultOptionPath(path)
	if secretPath == "" || subkey == "" {
		return nil, fmt.Errorf("vault: invalid path %q: must be in the form path/to/secret:key", path)
	}

	DEBUG("vault backend: reading `%s' (target=%q)", secretPath, target)
	secret, err := client.Logical().ReadWithContext(ctx, secretPath)
	if err != nil {
		return nil, fmt.Errorf("vault: reading %q: %w", secretPath, err)
	}
	if secret == nil || secret.Data == nil {
		return nil, fmt.Errorf("%w: %s", ErrBackendNotFound, path)
	}

	// KV v2 responses nest the secret under "data" alongside a sibling
	// "metadata" key; KV v1 responses are the secret map directly. Mirrors
	// internal/backends/vault/reader.go's detection exactly, so a
	// vaultOptionBackend and the built-in reader agree on the same mount.
	data := secret.Data
	if inner, ok := secret.Data["data"].(map[string]interface{}); ok {
		if _, hasMetadata := secret.Data["metadata"]; hasMetadata {
			data = inner
		}
	}

	v, ok := data[subkey]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrBackendNotFound, path)
	}
	s, ok := v.(string)
	if !ok {
		return nil, fmt.Errorf("vault: value at %q is not a string (got %T)", path, v)
	}
	return s, nil
}

// GetBatch implements Backend using SequentialGetBatch - see Backend.GetBatch's
// doc comment for why (no call site to design real batching against).
func (b *vaultOptionBackend) GetBatch(ctx context.Context, paths []string) (map[string]interface{}, error) {
	return SequentialGetBatch(ctx, paths, b.Get)
}

// Health implements Backend by checking the "" (WithVault default)
// target's connectivity. A vaultOptionBackend configured only via
// WithVaultTarget (no WithVault) has no default to check, and Health
// returns an error saying so rather than silently checking an arbitrary
// target.
func (b *vaultOptionBackend) Health(ctx context.Context) error {
	client, err := b.clientFor("")
	if err != nil {
		return err
	}
	_, err = client.Sys().HealthWithContext(ctx)
	return err
}

// Close implements Backend. There is no persistent connection or
// background goroutine to release: hashicorp/vault/api.Client is a thin
// wrapper over *http.Client, whose idle connections close themselves on
// Go's normal transport idle timeout.
func (b *vaultOptionBackend) Close() error { return nil }

// setConfig stores cfg under target ("" for WithVault's default) and
// drops any already-built client for that target so the next Get/
// GetWithTarget call rebuilds one from the new configuration - this makes
// a second WithVault/WithVaultTarget call for the same target (last
// registered wins, matching every other With* option in this package)
// take effect even if a client was already built and used.
func (b *vaultOptionBackend) setConfig(target string, cfg VaultConfig) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.configs[target] = cfg
	delete(b.clients, target)
}

func (b *vaultOptionBackend) clientFor(target string) (*vaultapi.Client, error) {
	b.mu.RLock()
	if c, ok := b.clients[target]; ok {
		b.mu.RUnlock()
		return c, nil
	}
	cfg, configured := b.configs[target]
	b.mu.RUnlock()

	if !configured {
		if target == "" {
			return nil, fmt.Errorf("vault: no configuration set; call WithVault")
		}
		return nil, fmt.Errorf("vault: target %q not configured; call WithVaultTarget(%q, ...)", target, target)
	}

	client, err := buildVaultClient(cfg)
	if err != nil {
		return nil, err
	}

	b.mu.Lock()
	// Re-check: a concurrent caller may have built (and stored) a client
	// for the same target while this one was under construction; keep
	// whichever was stored first so all callers converge on one client.
	if existing, ok := b.clients[target]; ok {
		b.mu.Unlock()
		return existing, nil
	}
	b.clients[target] = client
	b.mu.Unlock()

	return client, nil
}

// buildVaultClient constructs a hashicorp/vault/api.Client from cfg.
// Address and Token are required (after os.ExpandEnv); everything else is
// optional.
func buildVaultClient(cfg VaultConfig) (*vaultapi.Client, error) {
	addr := os.ExpandEnv(cfg.Address)
	if addr == "" {
		return nil, fmt.Errorf("vault: VaultConfig.Address must not be empty")
	}
	token := os.ExpandEnv(cfg.Token)
	if token == "" {
		return nil, fmt.Errorf("vault: VaultConfig.Token must not be empty")
	}
	namespace := os.ExpandEnv(cfg.Namespace)

	parsedURL, err := url.Parse(addr)
	if err != nil {
		return nil, fmt.Errorf("vault: could not parse VaultConfig.Address %q: %w", addr, err)
	}

	roots, err := x509.SystemCertPool()
	if err != nil {
		return nil, fmt.Errorf("vault: unable to retrieve system root certificate authorities: %w", err)
	}

	httpClient := &http.Client{
		Transport: buildVaultTransport(cfg.PoolSize, cfg.SkipVerify, roots),
	}
	if cfg.Timeout > 0 {
		httpClient.Timeout = cfg.Timeout
	}

	apiCfg := &vaultapi.Config{
		Address:    parsedURL.String(),
		HttpClient: httpClient,
	}
	client, err := vaultapi.NewClient(apiCfg)
	if err != nil {
		return nil, fmt.Errorf("vault: failed to create client: %w", err)
	}
	client.SetToken(token)
	if namespace != "" {
		client.SetNamespace(namespace)
	}
	return client, nil
}

// buildVaultTransport builds the *http.Transport a vaultOptionBackend's
// client uses. poolSize non-positive leaves Go's http.Transport
// zero-value idle-connection defaults in effect; positive sets both
// MaxIdleConnsPerHost and MaxIdleConns to poolSize - this is
// VaultConfig.PoolSize's entire, sole effect, kept in its own function so
// it is directly unit-testable without a network call.
func buildVaultTransport(poolSize int, skipVerify bool, roots *x509.CertPool) *http.Transport {
	t := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		TLSClientConfig: &tls.Config{
			RootCAs:            roots,
			InsecureSkipVerify: skipVerify, // #nosec G402 - controlled by caller-supplied VaultConfig.SkipVerify
		},
	}
	if poolSize > 0 {
		t.MaxIdleConnsPerHost = poolSize
		t.MaxIdleConns = poolSize
	}
	return t
}

// splitVaultOptionPath splits path into a secret path and a subkey at the
// last colon, mirroring internal/backends/vault.ParsePath exactly (same
// algorithm, duplicated rather than imported - see vaultOptionBackend's
// doc comment for why pkg/graft cannot import internal/backends/vault).
func splitVaultOptionPath(path string) (secret, key string) {
	secret = path
	if idx := strings.LastIndex(path, ":"); idx >= 0 {
		secret = path[:idx]
		key = path[idx+1:]
	}
	return
}

// vaultOptionBackendFor returns the *vaultOptionBackend already registered
// under the name "vault" in opts.Backends, or creates and registers a new
// one. If a non-*vaultOptionBackend is already registered under "vault"
// (i.e. an earlier WithBackend(myCustomVaultBackend) call in the same
// NewEngine call), it is replaced - the same "last registration for a
// given name wins" rule WithBackend/RegisterBackend already document,
// applied consistently here rather than as a special case.
func vaultOptionBackendFor(opts *EngineOptions) *vaultOptionBackend {
	if opts.Backends == nil {
		opts.Backends = make(map[string]Backend)
	}
	if existing, ok := opts.Backends["vault"]; ok {
		if vb, ok := existing.(*vaultOptionBackend); ok {
			return vb
		}
	}
	vb := newVaultOptionBackend()
	opts.Backends["vault"] = vb
	return vb
}

// WithVault registers a Backend named "vault", built from config, that the
// vault/vault-try operators consult when features.FeatureBackendRegistry
// is enabled (see WithBackendRegistry) - see the vaultOptionBackend type
// doc comment for exactly what it does and does not share with
// internal/backends/vault's environment-configured path. Calling WithVault
// more than once, or combining it with WithVaultTarget, applies to the
// same underlying backend: the last call for a given target ("" for
// WithVault, a name for WithVaultTarget) wins.
//
// WithVault alone does not enable FeatureBackendRegistry: pair it with
// WithBackendRegistry(true) (or a supplied *features.FeatureFlags that
// already enables it) or the registered backend is built but never
// consulted.
//
// Also works with DefaultEngine.Configure - it registers through
// WithBackend under the hood, so it is subject to the same Configure
// wiring WithBackend's doc comment describes.
func WithVault(config VaultConfig) EngineOption {
	return func(opts *EngineOptions) {
		vaultOptionBackendFor(opts).setConfig("", config)
	}
}

// WithVaultTarget registers per-"@target" Vault configuration (e.g.
// `(( vault@name "secret/path:key" ))`) on the same "vault" backend
// WithVault registers, reachable via TargetedBackend.GetWithTarget. name
// must be non-empty ("" is WithVault's own default target and is reset by
// calling WithVault, not this); an empty name is a no-op. WithVaultTarget
// may be called before WithVault; the backend is created on first use by
// either.
func WithVaultTarget(name string, config VaultConfig) EngineOption {
	return func(opts *EngineOptions) {
		if name == "" {
			return
		}
		vaultOptionBackendFor(opts).setConfig(name, config)
	}
}
