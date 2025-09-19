package testutil

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/vault/api"
	"github.com/hashicorp/vault/helper/namespace"
	vaulthttp "github.com/hashicorp/vault/http"
	"github.com/hashicorp/vault/sdk/helper/consts"
	vlogical "github.com/hashicorp/vault/sdk/logical"
	"github.com/hashicorp/vault/sdk/physical/inmem"
	"github.com/hashicorp/vault/vault"
	"github.com/hashicorp/vault/vault/plugincatalog"
)

// Define static errors.
var (
	ErrKVMountResponseError = errors.New("failed to mount KV v2 at secret/: response error")
)

// VaultDevServer represents a real in-memory Vault server instance for tests.
type VaultDevServer struct {
	Addr      string
	RootToken string
	Client    *api.Client
	server    *http.Server
	listener  net.Listener
}

// noopBuiltinRegistry implements plugincatalog.BuiltinRegistry with no built-ins.
type noopBuiltinRegistry struct{}

func (noopBuiltinRegistry) Contains(name string, pluginType consts.PluginType) bool { return false }
func (noopBuiltinRegistry) Get(name string, pluginType consts.PluginType) (func() (interface{}, error), bool) {
	return nil, false
}
func (noopBuiltinRegistry) Keys(pluginType consts.PluginType) []string { return []string{} }
func (noopBuiltinRegistry) DeprecationStatus(name string, pluginType consts.PluginType) (consts.DeprecationStatus, bool) {
	return consts.Unknown, false
}
func (noopBuiltinRegistry) IsBuiltinEntPlugin(name string, pluginType consts.PluginType) bool {
	return false
}

// NewVaultDevServer starts a real Vault core with in-memory storage (KV v2 mounted)
// and returns its address, root token, and an initialized API client.
//
//nolint:cyclop,funlen,thelper // Initialization requires multiple setup steps, t.Helper called conditionally
func NewVaultDevServer(t *testing.T) (*VaultDevServer, error) {
	if t != nil {
		t.Helper()
	}

	// 1) Build a Vault core with in-memory storage
	phys, err := inmem.NewInmem(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("failed creating in-memory storage: %w", err)
	}

	core, err := vault.NewCore(&vault.CoreConfig{
		Physical:        phys,
		DisableMlock:    true,
		BuiltinRegistry: noopBuiltinRegistry{},
	})

	_ = plugincatalog.BuiltinRegistry(nil) // keep import referenced
	if err != nil {
		return nil, fmt.Errorf("failed creating vault core: %w", err)
	}

	// 2) Start HTTP handler using Vault's test helper
	listener, addr := vaulthttp.TestServer(nil, core)
	// TestServer already starts serving on listener

	// 3) Initialize and unseal via HTTP API to get a usable root token
	cfg := api.DefaultConfig()
	cfg.Address = addr

	client, err := api.NewClient(cfg)
	if err != nil {
		_ = listener.Close()

		return nil, fmt.Errorf("failed to create vault client: %w", err)
	}

	// Initialize with one key share
	initResp, err := client.Sys().Init(&api.InitRequest{SecretShares: 1, SecretThreshold: 1})
	if err != nil {
		_ = listener.Close()

		return nil, fmt.Errorf("failed to initialize vault: %w", err)
	}

	// Unseal
	_, err = client.Sys().Unseal(initResp.Keys[0])
	if err != nil {
		_ = listener.Close()

		return nil, fmt.Errorf("failed to unseal vault: %w", err)
	}

	// Set root token
	client.SetToken(initResp.RootToken)

	// 4) Ensure KV v2 is mounted at secret/ directly via core (avoids early HTTP races)
	innerToken, err := core.DecodeSSCToken(initResp.RootToken)
	if err != nil || innerToken == "" {
		_ = listener.Close()

		return nil, fmt.Errorf("failed to decode root token for mounting: %w", err)
	}

	mountReq := &vlogical.Request{
		Operation:   vlogical.UpdateOperation,
		ClientToken: innerToken,
		Path:        "sys/mounts/secret",
		Data: map[string]interface{}{
			"type":        "kv",
			"path":        "secret/",
			"description": "key/value secret storage",
			"options": map[string]string{
				"version": "2",
			},
		},
	}

	resp, err := core.HandleRequest(namespace.RootContext(context.TODO()), mountReq)
	if err != nil || (resp != nil && resp.IsError()) {
		_ = listener.Close()

		if err != nil {
			return nil, fmt.Errorf("failed to mount KV v2 at secret/: %w", err)
		}

		return nil, ErrKVMountResponseError
	}

	vaultServer := &VaultDevServer{
		Addr:      addr,
		RootToken: initResp.RootToken,
		Client:    client,
		server:    nil,
		listener:  listener,
	}

	if t != nil {
		t.Cleanup(func() { vaultServer.Close() })
	}

	// Final health verification
	const (
		maxHealthChecks  = 20
		healthCheckDelay = 50 * time.Millisecond
	)

	for i := range maxHealthChecks {
		_, err := client.Sys().Health()
		if err == nil {
			break
		} else if i == maxHealthChecks-1 {
			vaultServer.Close()

			return nil, fmt.Errorf("vault server did not become healthy: %w", err)
		}

		time.Sleep(healthCheckDelay)
	}

	return vaultServer, nil
}

// NewVaultDevServerNoCleanup is kept for backward-compat, and simply delegates to NewVaultDevServer.
// Call Close() manually in tests that don't pass a *testing.T.
func NewVaultDevServerNoCleanup() (*VaultDevServer, error) {
	return NewVaultDevServer(nil)
}

// Close stops the Vault server.
func (v *VaultDevServer) Close() {
	const shutdownTimeout = 2 * time.Second

	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	if v.server != nil {
		_ = v.server.Shutdown(ctx)
	}

	if v.listener != nil {
		_ = v.listener.Close()
	}
}

// WriteSecret writes a secret under secret/<path> using KV v2 data envelope.
func (v *VaultDevServer) WriteSecret(path string, data map[string]interface{}) error {
	cleanPath := strings.TrimPrefix(path, "/")

	_, err := v.Client.Logical().Write("secret/data/"+cleanPath, map[string]interface{}{"data": data})
	if err != nil {
		return fmt.Errorf("failed to write secret at %s: %w", path, err)
	}

	return nil
}

// ErrSecretNotFound indicates that a secret was not found in Vault.
var ErrSecretNotFound = errors.New("secret not found")

// ErrUnexpectedSecretFormat indicates unexpected secret format.
var ErrUnexpectedSecretFormat = errors.New("unexpected secret format")

// ReadSecret reads a secret from secret/<path> using KV v2 envelope.
func (v *VaultDevServer) ReadSecret(path string) (map[string]interface{}, error) {
	cleanPath := strings.TrimPrefix(path, "/")

	sec, err := v.Client.Logical().Read("secret/data/" + cleanPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read secret at %s: %w", path, err)
	}

	if sec == nil {
		return nil, ErrSecretNotFound
	}

	if m, ok := sec.Data["data"].(map[string]interface{}); ok {
		return m, nil
	}

	return nil, fmt.Errorf("%w at %s", ErrUnexpectedSecretFormat, path)
}

// DeleteSecret deletes a secret at secret/<path>.
func (v *VaultDevServer) DeleteSecret(path string) error {
	cleanPath := strings.TrimPrefix(path, "/")

	_, err := v.Client.Logical().Delete("secret/data/" + cleanPath)
	if err != nil {
		return fmt.Errorf("failed to delete secret at %s: %w", path, err)
	}

	return nil
}
