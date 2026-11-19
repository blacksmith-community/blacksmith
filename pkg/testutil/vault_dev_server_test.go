package testutil_test

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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

	// A dev server also listens on the port after its own, so 8199 is
	// refused for the 8200 that its cluster listener would take.
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

	if port != 9000 {
		t.Errorf("expected the first port that keeps it and the next port outside 8200-8236 (9000), got %d", port)
	}

	// The real source must never land in the reserved range either.
	for range 50 {
		p, err := testutil.PickFreePort(nil)
		if err != nil {
			t.Fatalf("PickFreePort(nil): %v", err)
		}

		if p+1 >= 8200 && p <= 8236 {
			t.Fatalf("port %d or the port after it is in the reserved range", p)
		}
	}
}

// sequentialPorts hands out free ports the way macOS does, in sequence from
// where its last draw left off, and refuses a port that is held.
type sequentialPorts struct {
	mu   sync.Mutex
	next int
	held map[int]bool
}

func newSequentialPorts(start int, held ...int) *sequentialPorts {
	ports := &sequentialPorts{next: start, held: map[int]bool{}}
	for _, port := range held {
		ports.held[port] = true
	}

	return ports
}

var errPortHeld = errors.New("port is held")

func (s *sequentialPorts) listen(port int) (net.Listener, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if port == 0 {
		for s.held[s.next] {
			s.next++
		}

		port = s.next
		s.next++
	} else if s.held[port] {
		return nil, errPortHeld
	}

	s.held[port] = true

	return &heldPort{ports: s, port: port}, nil
}

func (s *sequentialPorts) isHeld(port int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.held[port]
}

// heldPort is a listener that holds a port of sequentialPorts until closed.
type heldPort struct {
	ports *sequentialPorts
	port  int
}

func (h *heldPort) Accept() (net.Conn, error) { return nil, errPortHeld }

func (h *heldPort) Close() error {
	h.ports.mu.Lock()
	defer h.ports.mu.Unlock()

	delete(h.ports.held, h.port)

	return nil
}

func (h *heldPort) Addr() net.Addr { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: h.port} }

// A dev server listens on its port and, for cluster traffic, on the port
// after it. Where the kernel hands out free ports in sequence, a plain pick
// hands the dev server that starts next the cluster port of the one before
// it, so each pick must move the kernel past its cluster port.
func TestPickLoopbackPortMovesPastTheClusterPort(t *testing.T) {
	t.Parallel()

	ports := newSequentialPorts(40000)
	previous := 0

	for range 5 {
		port, err := testutil.PickLoopbackPort(ports.listen)
		if err != nil {
			t.Fatalf("PickLoopbackPort: %v", err)
		}

		if previous != 0 && port == previous+1 {
			t.Fatalf("port %d is the cluster port of the port %d picked before it", port, previous)
		}

		if ports.isHeld(port) || ports.isHeld(port+1) {
			t.Fatalf("the pick of port %d left it or its cluster port held", port)
		}

		previous = port
	}
}

// A port whose cluster port another server holds is refused.
func TestPickLoopbackPortRefusesAHeldClusterPort(t *testing.T) {
	t.Parallel()

	// 40000 is drawn first, but 40001 is held, so 40002 comes second and
	// the draw is refused. The next draw is 40003 with 40004 after it.
	ports := newSequentialPorts(40000, 40001)

	port, err := testutil.PickLoopbackPort(ports.listen)
	if err != nil {
		t.Fatalf("PickLoopbackPort: %v", err)
	}

	if port != 40003 {
		t.Errorf("expected 40003, the first port whose cluster port is free, got %d", port)
	}

	if !ports.isHeld(40001) {
		t.Error("the pick released the port another server holds")
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

// A dev server answers its health check before the KV v2 backend at secret/
// has finished its first-use upgrade, and it refuses writes until then. The
// helper must hand the server over only once a write goes through.
func TestAwaitKVWritableWaitsOutTheBackendUpgrade(t *testing.T) {
	t.Parallel()

	var writes atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && writes.Add(1) <= 3 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"errors":["Upgrading from non-versioned to versioned data. This backend will be unavailable for a brief period and will resume service shortly."]}`))

			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"version":1}}`))
	}))
	defer server.Close()

	if err := testutil.AwaitKVWritable(server.URL, 5*time.Second, time.Millisecond); err != nil {
		t.Fatalf("AwaitKVWritable = %v, want nil once the upgrade is done", err)
	}

	if got := writes.Load(); got < 4 {
		t.Errorf("the helper wrote %d times, want it to retry until the fourth write went through", got)
	}
}

// A backend that never finishes upgrading is an error, not a hang.
func TestAwaitKVWritableGivesUpOnAStuckUpgrade(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"errors":["Upgrading from non-versioned to versioned data. This backend will be unavailable for a brief period and will resume service shortly."]}`))
	}))
	defer server.Close()

	err := testutil.AwaitKVWritable(server.URL, 50*time.Millisecond, time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "Upgrading") {
		t.Fatalf("AwaitKVWritable = %v, want an error that carries the backend's message", err)
	}
}

// foreignServer stands in for a dev server that another test process
// started on the port this one picked. It answers the health check, which
// needs no token, and refuses every token, so it refuses ours too.
type foreignServer struct {
	*httptest.Server

	mu       sync.Mutex
	requests []string
}

func newForeignServer(t *testing.T) *foreignServer {
	t.Helper()

	foreign := &foreignServer{}
	foreign.Server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		foreign.mu.Lock()
		foreign.requests = append(foreign.requests, request.Method+" "+request.URL.Path)
		foreign.mu.Unlock()

		writer.Header().Set("Content-Type", "application/json")

		if request.URL.Path == "/v1/sys/health" {
			_, _ = writer.Write([]byte(`{"initialized":true,"sealed":false,"standby":false}`))

			return
		}

		writer.WriteHeader(http.StatusForbidden)
		_, _ = writer.Write([]byte(`{"errors":["permission denied"]}`))
	}))
	t.Cleanup(foreign.Close)

	return foreign
}

func (f *foreignServer) port(t *testing.T) int {
	t.Helper()

	addr, ok := f.Listener.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("unexpected stand-in address %v", f.Listener.Addr())
	}

	return addr.Port
}

// beyondIdentityCheck lists the requests that a start sent the stand-in
// after it should have seen that the server is not its own.
func (f *foreignServer) beyondIdentityCheck() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	var sent []string

	for _, request := range f.requests {
		if request != "GET /v1/sys/health" && request != "GET /v1/auth/token/lookup-self" {
			sent = append(sent, request)
		}
	}

	return sent
}

// The picked port is closed before the dev server binds it, so a server
// that another test process started can take it in between. That server
// answers the health check, and the helper once went on to send it our
// root token and failed on a 403 when it mounted secret/. The helper must
// see that the server refuses our token and start again on a new port.
func TestVaultDevServerMovesOnFromAServerThatRefusesItsToken(t *testing.T) {
	t.Parallel()

	foreign := newForeignServer(t)

	var picks atomic.Int32

	pick := func() (int, error) {
		if picks.Add(1) == 1 {
			return foreign.port(t), nil
		}

		return testutil.PickFreePort(nil)
	}

	srv, err := testutil.NewVaultDevServerOnPorts(t, pick)
	if err != nil {
		t.Fatalf("NewVaultDevServer = %v, want it to move on to a new port", err)
	}

	if srv.Addr == foreign.URL {
		t.Fatalf("the helper handed over the stand-in at %s", foreign.URL)
	}

	if got := picks.Load(); got != 2 {
		t.Errorf("the helper picked %d ports, want 2", got)
	}

	if sent := foreign.beyondIdentityCheck(); len(sent) != 0 {
		t.Errorf("the helper went on to send the stand-in %v", sent)
	}

	secret, err := srv.Client.Auth().Token().LookupSelf()
	if err != nil {
		t.Fatalf("lookup-self on the server handed over: %v", err)
	}

	if id := secret.Data["id"]; id != srv.RootToken {
		t.Errorf("the server handed over knows token %v, want %s", id, srv.RootToken)
	}
}

// A port that keeps going to a server that refuses our token uses up the
// start attempts and ends in an error, never in a server handed over.
func TestVaultDevServerGivesUpOnAPortThatIsNeverItsOwn(t *testing.T) {
	t.Parallel()

	foreign := newForeignServer(t)

	var picks atomic.Int32

	pick := func() (int, error) {
		picks.Add(1)

		return foreign.port(t), nil
	}

	srv, err := testutil.NewVaultDevServerOnPorts(t, pick)
	if err == nil {
		t.Fatalf("NewVaultDevServer handed over %s, want an error", srv.Addr)
	}

	// Our server can exit on its failed bind before the stand-in refuses
	// the token, so either loss of the port may end the last attempt.
	if !errors.Is(err, testutil.ErrForeignServer) && !strings.Contains(err.Error(), "address already in use") {
		t.Errorf("NewVaultDevServer = %v, want the port lost to another server", err)
	}

	if got := picks.Load(); got != 3 {
		t.Errorf("the helper picked %d ports, want one per start attempt (3)", got)
	}

	if sent := foreign.beyondIdentityCheck(); len(sent) != 0 {
		t.Errorf("the helper went on to send the stand-in %v", sent)
	}
}
