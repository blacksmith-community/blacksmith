package testutil_test

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"blacksmith/pkg/testutil"
)

func TestVaultDevServer(t *testing.T) {
	t.Parallel()
	// Create a new vault dev server
	vaultServer, err := testutil.NewVaultDevServer(t)
	if err != nil {
		t.Fatalf("Failed to create vault dev server: %v", err)
	}
	// Cleanup is automatic via t.Cleanup()

	// Test that we can write a secret
	testData := map[string]interface{}{
		"username": "testuser",
		"password": "testpass",
	}

	err = vaultServer.WriteSecret("test/credentials", testData)
	if err != nil {
		t.Fatalf("Failed to write secret: %v", err)
	}

	// Test that we can read the secret back
	readData, err := vaultServer.ReadSecret("test/credentials")
	if err != nil {
		t.Fatalf("Failed to read secret: %v", err)
	}

	if readData == nil {
		t.Fatal("Expected data but got nil")
	}

	// Verify the data matches
	if readData["username"] != testData["username"] {
		t.Errorf("Username mismatch: expected %v, got %v", testData["username"], readData["username"])
	}

	if readData["password"] != testData["password"] {
		t.Errorf("Password mismatch: expected %v, got %v", testData["password"], readData["password"])
	}

	// Test that we can delete the secret
	err = vaultServer.DeleteSecret("test/credentials")
	if err != nil {
		t.Fatalf("Failed to delete secret: %v", err)
	}

	// Verify it's deleted
	readData, err = vaultServer.ReadSecret("test/credentials")
	if !errors.Is(err, testutil.ErrSecretNotFound) {
		t.Fatalf("Expected ErrSecretNotFound after delete, got: %v", err)
	}

	if readData != nil {
		t.Error("Expected nil data after delete but got data")
	}

	// Test the Vault client is functional
	health, err := vaultServer.Client.Sys().Health()
	if err != nil {
		t.Fatalf("Failed to check health: %v", err)
	}

	if health.Sealed {
		t.Error("Vault should not be sealed")
	}

	if !health.Initialized {
		t.Error("Vault should be initialized")
	}
}

func TestVaultDevServerNoCleanup(t *testing.T) {
	t.Parallel()
	// Test the no-cleanup variant
	vaultServer, err := testutil.NewVaultDevServerNoCleanup()
	if err != nil {
		t.Fatalf("Failed to create vault dev server: %v", err)
	}
	defer vaultServer.Close() // Manual cleanup

	// Test basic operations
	testData := map[string]interface{}{
		"key": "value",
	}

	err = vaultServer.WriteSecret("simple/test", testData)
	if err != nil {
		t.Fatalf("Failed to write secret: %v", err)
	}

	readData, err := vaultServer.ReadSecret("simple/test")
	if err != nil {
		t.Fatalf("Failed to read secret: %v", err)
	}

	if readData["key"] != testData["key"] {
		t.Errorf("Data mismatch: expected %v, got %v", testData["key"], readData["key"])
	}
}

func TestPickFreePortAvoidsReservedRange(t *testing.T) {
	t.Parallel()

	candidates := []int{8200, 8210, 8236, 8199, 9000}
	idx := 0
	next := func() (int, error) {
		p := candidates[idx]
		idx++

		return p, nil
	}

	port, err := testutil.PickFreePort(next)
	if err != nil {
		t.Fatalf("PickFreePort: %v", err)
	}

	if port != 8199 {
		t.Errorf("expected first port outside 8200-8236 (8199), got %d", port)
	}

	// The real source must never land in the reserved range either.
	for range 50 {
		p, err := testutil.PickFreePort(nil)
		if err != nil {
			t.Fatalf("PickFreePort(nil): %v", err)
		}

		if p >= 8200 && p <= 8236 {
			t.Fatalf("port %d is in the reserved range", p)
		}
	}
}

func TestDevServerEnv(t *testing.T) {
	t.Parallel()

	realHome := "/home/real"
	environ := []string{
		"HOME=" + realHome,
		"PATH=/usr/bin:/bin",
		"VAULT_TOKEN=secret",
		"VAULT_ADDR=http://x",
		"BAO_TOKEN=secret",
		"BAO_ADDR=http://y",
		"LANG=C",
	}

	got := testutil.DevServerEnv(environ, "/tmp/fresh-home")
	env := map[string]string{}

	for _, kv := range got {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = v
	}

	if env["HOME"] != "/tmp/fresh-home" || env["HOME"] == realHome {
		t.Errorf("HOME should be the fresh temp dir, got %q", env["HOME"])
	}

	if env["PATH"] != "/usr/bin:/bin" {
		t.Errorf("PATH should be kept, got %q", env["PATH"])
	}

	for _, k := range []string{"VAULT_TOKEN", "VAULT_ADDR", "BAO_TOKEN", "BAO_ADDR"} {
		if _, ok := env[k]; ok {
			t.Errorf("%s should be dropped", k)
		}
	}
}

func TestFindVaultBinaryHonoursOverride(t *testing.T) { //nolint:paralleltest // uses t.Setenv
	fake := filepath.Join(t.TempDir(), "fakebao")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\n"), 0o755); err != nil { //nolint:gosec // test executable
		t.Fatal(err)
	}

	t.Setenv("BLACKSMITH_TEST_VAULT_BIN", fake)
	t.Setenv("PATH", "")

	got, err := testutil.FindVaultBinary()
	if err != nil {
		t.Fatalf("FindVaultBinary: %v", err)
	}

	if got != fake {
		t.Errorf("expected %s, got %s", fake, got)
	}
}

func TestFindVaultBinaryMissingNamesVariable(t *testing.T) { //nolint:paralleltest // uses t.Setenv
	t.Setenv("BLACKSMITH_TEST_VAULT_BIN", "")
	t.Setenv("PATH", "")

	_, err := testutil.FindVaultBinary()
	if err == nil {
		t.Fatal("expected an error with no binary available")
	}

	if !strings.Contains(err.Error(), "BLACKSMITH_TEST_VAULT_BIN") || !strings.Contains(err.Error(), "bao") {
		t.Errorf("error should name BLACKSMITH_TEST_VAULT_BIN and bao, got: %v", err)
	}
}

func TestVaultDevServerProcessGoneAfterCleanup(t *testing.T) {
	t.Parallel()

	srv, err := testutil.NewVaultDevServerNoCleanup()
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	pid := srv.Pid()
	if pid <= 0 {
		t.Fatalf("expected a subprocess pid, got %d", pid)
	}

	if err := srv.WriteSecret("rt/a", map[string]interface{}{"k": "v"}); err != nil {
		t.Fatalf("write: %v", err)
	}

	srv.Close()
	srv.Close() // idempotent

	if err := syscall.Kill(pid, 0); err == nil {
		t.Errorf("process %d still alive after Close", pid)
	}

	if err := syscall.Kill(-pid, 0); err == nil {
		t.Errorf("process group %d still alive after Close", pid)
	}
}

func TestVaultDevServerLeavesUserTokenFileAlone(t *testing.T) { //nolint:paralleltest // reads real HOME
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}

	tokenPath := filepath.Join(home, ".vault-token")

	before, statErr := os.Stat(tokenPath)
	existed := statErr == nil

	var beforeContent []byte
	if existed {
		beforeContent, _ = os.ReadFile(tokenPath) //nolint:gosec // fixed path under real HOME
	}

	srv, err := testutil.NewVaultDevServerNoCleanup()
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	time.Sleep(200 * time.Millisecond)
	srv.Close()

	after, statErr := os.Stat(tokenPath)
	if !existed {
		if statErr == nil {
			t.Fatal("~/.vault-token was created by the dev server")
		}

		return
	}

	if statErr != nil {
		t.Fatalf("~/.vault-token vanished: %v", statErr)
	}

	if !after.ModTime().Equal(before.ModTime()) {
		t.Errorf("~/.vault-token mtime changed: %v -> %v", before.ModTime(), after.ModTime())
	}

	afterContent, _ := os.ReadFile(tokenPath) //nolint:gosec // fixed path under real HOME
	if string(afterContent) != string(beforeContent) {
		t.Error("~/.vault-token content changed")
	}
}

func TestVaultDevServerIgnoresClientEnvironment(t *testing.T) { //nolint:paralleltest // uses t.Setenv
	closedPort, err := testutil.PickFreePort(nil)
	if err != nil {
		t.Fatal(err)
	}

	bogus := "http://127.0.0.1:" + strconv.Itoa(closedPort)
	t.Setenv("VAULT_AGENT_ADDR", bogus)
	t.Setenv("VAULT_PROXY_ADDR", bogus)
	t.Setenv("VAULT_NAMESPACE", "bogus-namespace")
	t.Setenv("VAULT_ADDR", bogus)
	t.Setenv("VAULT_TOKEN", "bogus-token")

	srv, err := testutil.NewVaultDevServer(t)
	if err != nil {
		t.Fatalf("start with hostile VAULT_* environment: %v", err)
	}

	if srv.Client.Address() != srv.Addr {
		t.Errorf("client address %q, want %q", srv.Client.Address(), srv.Addr)
	}

	if ns := srv.Client.Namespace(); ns != "" {
		t.Errorf("client namespace %q, want empty", ns)
	}

	if err := srv.WriteSecret("env/a", map[string]interface{}{"k": "v"}); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := srv.ReadSecret("env/a")
	if err != nil || got["k"] != "v" {
		t.Fatalf("read: %v %v", got, err)
	}
}

func TestCloseKillsStubbornChildOfExitedLeader(t *testing.T) {
	t.Parallel()

	pidFile := filepath.Join(t.TempDir(), "child.pid")
	// The child ignores TERM and sleeps; the leader records its pid and exits.
	script := `(trap '' TERM; exec sleep 300) >/dev/null 2>&1 & echo $! > "` + pidFile + `.tmp"; mv "` + pidFile + `.tmp" "` + pidFile + `"`

	srv, err := testutil.NewFakeGroupServer(script)
	if err != nil {
		t.Fatal(err)
	}

	var childPid int

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if data, rerr := os.ReadFile(pidFile); rerr == nil { //nolint:gosec // test path
			childPid, _ = strconv.Atoi(strings.TrimSpace(string(data)))
		}

		if childPid > 0 && srv.Exited() {
			break
		}

		time.Sleep(10 * time.Millisecond)
	}

	if childPid <= 0 || !srv.Exited() {
		t.Fatalf("setup failed: child=%d exited=%v", childPid, srv.Exited())
	}

	if err := syscall.Kill(childPid, 0); err != nil {
		t.Fatalf("child should still be alive before Close: %v", err)
	}

	srv.Close()

	// SIGKILL is delivered synchronously; allow the kernel to reap the zombie.
	for time.Now().Before(deadline) {
		if syscall.Kill(childPid, 0) != nil {
			return
		}

		time.Sleep(10 * time.Millisecond)
	}

	_ = syscall.Kill(childPid, syscall.SIGKILL)

	t.Errorf("stubborn child %d survived Close", childPid)
}
