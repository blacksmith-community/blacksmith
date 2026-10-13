package testutil

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/hashicorp/vault/api"
)

// Define static errors.
var (
	// ErrKVMountResponseError is kept for callers that reference it.
	ErrKVMountResponseError = errors.New("failed to mount KV v2 at secret/: response error")
	// ErrVaultBinaryNotFound means no bao or vault executable could be located.
	ErrVaultBinaryNotFound = errors.New(
		"no Vault server binary found: set BLACKSMITH_TEST_VAULT_BIN to its path, " +
			"or install OpenBao (bao) and put it on PATH")
	// ErrVaultNotReady means the dev server never answered its health endpoint.
	ErrVaultNotReady = errors.New("vault dev server did not become ready")
	// errBindFailure marks a start that failed because the port was taken.
	errBindFailure = errors.New("vault dev server could not bind its port")
)

const (
	vaultBinEnv         = "BLACKSMITH_TEST_VAULT_BIN"
	reservedPortLow     = 8200
	reservedPortHigh    = 8236
	maxStartAttempts    = 3
	maxPortPicks        = 100
	readinessDeadline   = 30 * time.Second
	readinessInterval   = 50 * time.Millisecond
	gracefulStopTimeout = 2 * time.Second
	outputTailBytes     = 4096
	rootTokenBytes      = 16
	clientTimeout       = 60 * time.Second
	idleConnTimeout     = 30 * time.Second
	defaultRetries      = 2
)

// VaultDevServer represents a real Vault (or OpenBao) dev server running as a
// subprocess for tests.
type VaultDevServer struct {
	Addr      string
	RootToken string
	Client    *api.Client

	cmd      *exec.Cmd
	done     chan struct{}
	out      *lockedBuffer
	homeDir  string
	closeOne sync.Once
}

// lockedBuffer is a bytes.Buffer safe for concurrent write and read.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(p)
}

func (b *lockedBuffer) tail(n int) string {
	b.mu.Lock()
	defer b.mu.Unlock()

	data := b.buf.Bytes()
	if len(data) > n {
		data = data[len(data)-n:]
	}

	return string(data)
}

// findVaultBinary locates the server binary: BLACKSMITH_TEST_VAULT_BIN first,
// then bao, then vault on PATH.
func findVaultBinary() (string, error) {
	if bin := os.Getenv(vaultBinEnv); bin != "" {
		return bin, nil
	}

	for _, name := range []string{"bao", "vault"} {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}

	return "", ErrVaultBinaryNotFound
}

// loopbackPort asks the kernel for a free loopback port.
func loopbackPort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("failed to find a free port: %w", err)
	}

	defer func() { _ = l.Close() }()

	addr, ok := l.Addr().(*net.TCPAddr)
	if !ok {
		return 0, fmt.Errorf("%w: unexpected listener address %v", ErrVaultNotReady, l.Addr())
	}

	return addr.Port, nil
}

// pickFreePort draws ports from next (the kernel by default) until one falls
// outside the 8200-8236 range that live Vault systems use.
func pickFreePort(next func() (int, error)) (int, error) {
	if next == nil {
		next = loopbackPort
	}

	for range maxPortPicks {
		port, err := next()
		if err != nil {
			return 0, err
		}

		if port < reservedPortLow || port > reservedPortHigh {
			return port, nil
		}
	}

	return 0, fmt.Errorf("%w: no port outside %d-%d after %d picks",
		ErrVaultNotReady, reservedPortLow, reservedPortHigh, maxPortPicks)
}

// devServerEnv builds the subprocess environment: a fresh HOME, PATH kept, and
// every VAULT_* and BAO_* variable dropped. Nothing else is inherited.
func devServerEnv(environ []string, home string) []string {
	env := []string{"HOME=" + home}

	for _, kv := range environ {
		if strings.HasPrefix(kv, "PATH=") {
			env = append(env, kv)
		}
	}

	return env
}

func randomToken() (string, error) {
	b := make([]byte, rootTokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("failed to generate root token: %w", err)
	}

	return "bs-test-" + hex.EncodeToString(b), nil
}

// NewVaultDevServer starts `bao server -dev` (or `vault server -dev`) as a
// subprocess on a free loopback port, with KV v2 at secret/, and returns its
// address, root token, and an API client. It registers cleanup on t.
//
//nolint:thelper // t.Helper is called conditionally
func NewVaultDevServer(t *testing.T) (*VaultDevServer, error) {
	if t != nil {
		t.Helper()
	}

	bin, err := findVaultBinary()
	if err != nil {
		return nil, err
	}

	var lastErr error

	for range maxStartAttempts {
		srv, err := startDevServer(bin)
		if err == nil {
			if t != nil {
				t.Cleanup(srv.Close)
			}

			return srv, nil
		}

		if !errors.Is(err, errBindFailure) {
			return nil, err
		}

		lastErr = err
	}

	return nil, fmt.Errorf("vault dev server failed to start after %d attempts: %w", maxStartAttempts, lastErr)
}

func startDevServer(bin string) (*VaultDevServer, error) {
	port, err := pickFreePort(nil)
	if err != nil {
		return nil, err
	}

	token, err := randomToken()
	if err != nil {
		return nil, err
	}

	home, err := os.MkdirTemp("", "blacksmith-vault-home-")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp HOME: %w", err)
	}

	listen := fmt.Sprintf("127.0.0.1:%d", port)
	out := &lockedBuffer{}

	cmd := exec.Command(bin, "server", "-dev", //nolint:gosec // binary is chosen by the test environment
		"-dev-listen-address="+listen, "-dev-root-token-id="+token)
	cmd.Env = devServerEnv(os.Environ(), home)
	cmd.Stdout = out
	cmd.Stderr = out
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		_ = os.RemoveAll(home)

		return nil, fmt.Errorf("failed to start %s: %w", bin, err)
	}

	srv := &VaultDevServer{
		Addr:      "http://" + listen,
		RootToken: token,
		cmd:       cmd,
		done:      make(chan struct{}),
		out:       out,
		homeDir:   home,
	}

	go func() {
		_ = cmd.Wait()

		close(srv.done)
	}()

	if err := srv.waitReady(); err != nil {
		srv.Close()

		return nil, err
	}

	return srv, nil
}

// newIsolatedClient builds an API client that ignores the test process's
// VAULT_* environment: explicit address, no agent, no namespace, no proxy,
// and no CA file.
func newIsolatedClient(addr, token string, maxRetries int) (*api.Client, error) {
	cfg := api.DefaultConfig()
	if cfg.Error != nil {
		return nil, fmt.Errorf("failed to build vault client config: %w", cfg.Error)
	}

	cfg.Address = addr
	cfg.AgentAddress = ""
	cfg.MaxRetries = maxRetries
	cfg.Limiter = nil
	cfg.HttpClient = &http.Client{
		Timeout: clientTimeout,
		Transport: &http.Transport{
			Proxy:               nil,
			DialContext:         (&net.Dialer{Timeout: clientTimeout}).DialContext,
			MaxIdleConnsPerHost: 4,
			IdleConnTimeout:     idleConnTimeout,
		},
	}

	client, err := api.NewClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create vault client: %w", err)
	}

	client.ClearNamespace()
	client.SetToken(token)

	return client, nil
}

func (v *VaultDevServer) exited() bool {
	select {
	case <-v.done:
		return true
	default:
		return false
	}
}

func (v *VaultDevServer) exitError() error {
	tail := v.out.tail(outputTailBytes)
	if strings.Contains(tail, "address already in use") {
		return fmt.Errorf("%w: %w: %s", errBindFailure, ErrVaultNotReady, tail)
	}

	return fmt.Errorf("%w: process exited: %s", ErrVaultNotReady, tail)
}

func (v *VaultDevServer) waitReady() error {
	probe, err := newIsolatedClient(v.Addr, v.RootToken, 0)
	if err != nil {
		return err
	}

	deadline := time.Now().Add(readinessDeadline)

	var lastErr error

	for time.Now().Before(deadline) {
		if v.exited() {
			return v.exitError()
		}

		if _, lastErr = probe.Sys().Health(); lastErr == nil {
			// Another server could have answered if ours lost the port.
			if v.exited() {
				return v.exitError()
			}

			client, cerr := newIsolatedClient(v.Addr, v.RootToken, defaultRetries)
			if cerr != nil {
				return cerr
			}

			v.Client = client

			return v.ensureKVMount()
		}

		time.Sleep(readinessInterval)
	}

	return fmt.Errorf("%w within %s (last error: %v): %s",
		ErrVaultNotReady, readinessDeadline, lastErr, v.out.tail(outputTailBytes))
}

// ensureKVMount makes sure KV v2 is mounted at secret/, tolerating an existing mount.
func (v *VaultDevServer) ensureKVMount() error {
	mounts, err := v.Client.Sys().ListMounts()
	if err == nil {
		if m, ok := mounts["secret/"]; ok && m.Options["version"] == "2" {
			return nil
		}
	}

	err = v.Client.Sys().Mount("secret", &api.MountInput{
		Type:        "kv",
		Description: "key/value secret storage",
		Options:     map[string]string{"version": "2"},
	})
	if err != nil && !strings.Contains(err.Error(), "already in use") {
		return fmt.Errorf("failed to mount KV v2 at secret/: %w", err)
	}

	return nil
}

// NewVaultDevServerNoCleanup starts a server without registering test cleanup.
// The caller must call Close.
func NewVaultDevServerNoCleanup() (*VaultDevServer, error) {
	return NewVaultDevServer(nil)
}

// Close stops the Vault process and its process group. It is safe to call more than once.
func (v *VaultDevServer) Close() {
	v.closeOne.Do(func() {
		if v.cmd != nil && v.cmd.Process != nil {
			pgid := v.cmd.Process.Pid

			_ = syscall.Kill(-pgid, syscall.SIGTERM)

			select {
			case <-v.done:
			case <-time.After(gracefulStopTimeout):
			}

			// Probe the group: signal 0 succeeds while any member lives, even
			// after the leader has been reaped.
			if syscall.Kill(-pgid, 0) == nil {
				_ = syscall.Kill(-pgid, syscall.SIGKILL)
			}

			<-v.done
		}

		if v.homeDir != "" {
			_ = os.RemoveAll(v.homeDir)
		}
	})
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

	// Check if the secret was deleted (data field might be nil or empty)
	if sec.Data == nil {
		return nil, ErrSecretNotFound
	}

	data, hasData := sec.Data["data"]
	if !hasData || data == nil {
		return nil, ErrSecretNotFound
	}

	if m, ok := data.(map[string]interface{}); ok {
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
