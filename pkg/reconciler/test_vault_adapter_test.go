package reconciler_test

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	vaultpkg "blacksmith/pkg/vault"
	"github.com/hashicorp/vault/api"
)

// Define static errors to avoid dynamic error creation.
var (
	ErrSecretNotFound = errors.New("secret not found")
)

// RealTestVault implements reconciler.VaultInterface using the shared in-memory Vault.
// Each instance gets an isolated prefix to avoid cross-test interference when run in parallel.
type RealTestVault struct {
	prefix   string
	wrapper  *vaultpkg.Client
	mu       sync.Mutex
	getCalls []string
	putCalls []string
	errors   map[string]error
}

// NewTestVault creates a new adapter backed by the shared suite Vault.
// A unique prefix is created per test so paths do not collide across t.Parallel() tests.
func NewTestVault(t *testing.T) *RealTestVault {
	t.Helper()

	if suite.vault == nil {
		t.Fatalf("test vault not initialized")
	}
	// Build a Vault wrapper client against the suite server
	vaultClient, err := vaultpkg.NewClient(suite.vault.Client.Address(), suite.vault.RootToken, true)
	if err != nil {
		t.Fatalf("failed to create vault wrapper client: %v", err)
	}
	// Ensure KV v2 mount is detected
	_ = vaultClient.VerifyMount("secret", true)
	// Unique per-test namespace
	prefix := strings.ReplaceAll(t.Name(), "/", "_")
	prefix = strings.ReplaceAll(prefix, " ", "_")

	return &RealTestVault{prefix: prefix, wrapper: vaultClient, errors: map[string]error{}}
}

// NewTestVaultWithPrefix creates a test vault adapter using an explicit prefix.
func NewTestVaultWithPrefix(prefix string) *RealTestVault {
	if suite.vault == nil {
		panic("test vault not initialized")
	}

	vaultClient, err := vaultpkg.NewClient(suite.vault.Client.Address(), suite.vault.RootToken, true)
	if err != nil {
		panic(err)
	}

	_ = vaultClient.VerifyMount("secret", true)

	if prefix == "" {
		prefix = "reconciler"
	}
	// add a timestamp suffix to avoid collisions across parallel specs
	prefix = prefix + "_" + time.Now().Format("20060102T150405.000000000")

	return &RealTestVault{prefix: prefix, wrapper: vaultClient, errors: map[string]error{}}
}

// VaultInterface implementation.
func (v *RealTestVault) Get(path string) (map[string]interface{}, error) {
	prefixedPath := v.pref(path)

	v.mu.Lock()
	v.getCalls = append(v.getCalls, prefixedPath)
	err := v.errors[prefixedPath]
	v.mu.Unlock()

	if err != nil {
		return nil, err
	}

	data, exists, err := v.wrapper.GetSecret(prefixedPath)
	if err != nil {
		return nil, fmt.Errorf("failed to get secret at %s: %w", prefixedPath, err)
	}

	if !exists {
		return nil, fmt.Errorf("%w: %s", ErrSecretNotFound, path)
	}

	return data, nil
}

func (v *RealTestVault) Put(path string, secret map[string]interface{}) error {
	prefixedPath := v.pref(path)

	v.mu.Lock()
	v.putCalls = append(v.putCalls, prefixedPath)
	err := v.errors[prefixedPath]
	v.mu.Unlock()

	if err != nil {
		return err
	}

	err = v.wrapper.PutSecret(prefixedPath, secret)
	if err != nil {
		return fmt.Errorf("failed to put secret at %s: %w", prefixedPath, err)
	}

	return nil
}

func (v *RealTestVault) GetSecret(path string) (map[string]interface{}, error) {
	return v.Get(path)
}

func (v *RealTestVault) SetSecret(path string, secret map[string]interface{}) error {
	return v.Put(path, secret)
}

func (v *RealTestVault) DeleteSecret(path string) error {
	prefixedPath := v.pref(path)

	v.mu.Lock()
	err := v.errors[prefixedPath]
	v.mu.Unlock()

	if err != nil {
		return err
	}

	err = v.wrapper.DeleteSecret(prefixedPath)
	if err != nil {
		return fmt.Errorf("failed to delete secret at %s: %w", prefixedPath, err)
	}

	return nil
}

func (v *RealTestVault) ListSecrets(path string) ([]string, error) {
	prefixedPath := v.pref(path)

	v.mu.Lock()
	err := v.errors[prefixedPath]
	v.mu.Unlock()

	if err != nil {
		return nil, err
	}

	secrets, err := v.wrapper.ListSecrets(prefixedPath)
	if err != nil {
		return nil, fmt.Errorf("failed to list secrets at %s: %w", prefixedPath, err)
	}

	return secrets, nil
}

// Allow updater backup/cleanup paths to access the underlying API client.
func (v *RealTestVault) GetClient() *api.Client {
	return v.wrapper.Client
}

// Test helpers for compatibility with legacy tests.
func (v *RealTestVault) SetError(path string, err error) {
	v.mu.Lock()
	defer v.mu.Unlock()

	v.errors[v.pref(path)] = err
}

func (v *RealTestVault) SetData(path string, data map[string]interface{}) {
	_ = v.wrapper.PutSecret(v.pref(path), data)
}

func (v *RealTestVault) pref(path string) string {
	p := strings.TrimPrefix(path, "/")
	if v.prefix == "" {
		return p
	}

	return v.prefix + "/" + p
}
