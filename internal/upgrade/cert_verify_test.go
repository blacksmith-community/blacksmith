//nolint:testpackage // the tests call the unexported verifyTargets
package upgrade

import (
	"context"
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"blacksmith/internal/bosh"
	"blacksmith/internal/bosh/ssh"
	"blacksmith/internal/certinventory"
)

const (
	// verifyServerName is the common name of the env's leaves.
	verifyServerName = "orders.ws.example"
	// loopback is the address of the test's VM and listeners.
	loopback = "127.0.0.1"
)

// Errors the fake runner returns.
var (
	errFallbackRan = errors.New("the fallback must not run")
	errLostSession = errors.New("ssh: connection lost")
)

// sniListener is a local TLS listener that serves one certificate and
// records the server name each client sent.
type sniListener struct {
	ln net.Listener

	mu    sync.Mutex
	names []string
}

func startSNIListener(t *testing.T, cert *x509.Certificate, key *ecdsa.PrivateKey) *sniListener {
	t.Helper()

	inner, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", loopback+":0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}

	listener := &sniListener{}
	pair := &tls.Certificate{Certificate: [][]byte{cert.Raw}, PrivateKey: key, Leaf: cert}

	listener.ln = tls.NewListener(inner, &tls.Config{
		MinVersion: tls.VersionTLS12,
		GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
			listener.mu.Lock()
			listener.names = append(listener.names, hello.ServerName)
			listener.mu.Unlock()

			return pair, nil
		},
	})

	go func() {
		for {
			conn, err := listener.ln.Accept()
			if err != nil {
				return
			}

			go func() {
				defer func() { _ = conn.Close() }()

				_ = conn.(*tls.Conn).HandshakeContext(context.Background()) //nolint:forcetypeassert // tls.NewListener returns *tls.Conn
			}()
		}
	}()

	t.Cleanup(func() { _ = listener.ln.Close() })

	return listener
}

func (l *sniListener) port() int {
	return l.ln.Addr().(*net.TCPAddr).Port //nolint:forcetypeassert // a TCP listener
}

func (l *sniListener) serverNames() []string {
	l.mu.Lock()
	defer l.mu.Unlock()

	return append([]string(nil), l.names...)
}

// closedPort returns a local port that nothing listens on.
func closedPort(t *testing.T) int {
	t.Helper()

	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", loopback+":0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}

	port := listener.Addr().(*net.TCPAddr).Port //nolint:forcetypeassert // a TCP listener

	err = listener.Close()
	if err != nil {
		t.Fatal(err)
	}

	return port
}

// fakeVerifyRun is the run function the executor would hand verifyTargets.
// It records each call and answers with the result and error the test
// sets.
type fakeVerifyRun struct {
	mu     sync.Mutex
	calls  [][]string
	result ssh.Result
	err    error
}

func (f *fakeVerifyRun) run(op string, args ...string) (ssh.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.calls = append(f.calls, append([]string{op}, args...))

	return f.result, f.err
}

func (f *fakeVerifyRun) recorded() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([][]string(nil), f.calls...)
}

// verifyEnv is a redeploy env whose CredHub holds v2 of the leaf, with
// one instance whose rotation made v2.
type verifyEnv struct {
	*redeployEnv

	manager *Manager
	inst    *InstanceUpgrade
	newFP   string
	oldFP   string
}

func newVerifyEnv(t *testing.T) *verifyEnv {
	t.Helper()

	base := newRedeployEnv(t)
	base.madeNewVersion()

	return &verifyEnv{
		redeployEnv: base,
		manager:     base.newManager(),
		inst: &InstanceUpgrade{
			InstanceID:     waitInstanceID,
			DeploymentName: waitDeployment,
			Certificates: &InstanceRotation{
				Phase:     RotationPhasePatch,
				Variables: []VariableRotation{{Name: regenLeaf, OldVersionID: "v1", NewVersionID: "v2"}},
			},
		},
		newFP: certinventory.Fingerprint(base.newLeaf),
		oldFP: certinventory.Fingerprint(base.oldLeaf),
	}
}

// journal returns a journal for the env's VM with the leaf's certificate
// and key files and one listener target on port.
func (e *verifyEnv) journal(port int) *VMJournal {
	return &VMJournal{
		VM:    "redis/0",
		CID:   redeployVMCID,
		Group: certJob,
		Phase: VMPhaseRestarted,
		Files: []FileEntry{
			{Path: "/var/vcap/data/jobs/redis/abc/config/tls/redis.crt", Job: certJob, Role: FileRoleCertificate, Variable: regenLeaf},
			{Path: "/var/vcap/data/jobs/redis/abc/config/tls/redis.key", Job: certJob, Role: FileRoleKey, Variable: regenLeaf},
		},
		Targets: []TargetResult{{Variable: regenLeaf, Port: port, ServerName: verifyServerName}},
	}
}

func (e *verifyEnv) fingerprints() map[string]string {
	return map[string]string{regenLeaf: e.newFP}
}

func (e *verifyEnv) verify(journal *VMJournal, run func(string, ...string) (ssh.Result, error)) []TargetResult {
	e.t.Helper()

	before := cloneJournal(journal)
	results := e.manager.verifyTargets(e.t.Context(), e.inst, journal, e.fingerprints(), run)

	if !reflect.DeepEqual(before, journal) {
		e.t.Fatalf("verifyTargets changed the journal from %+v to %+v", before, journal)
	}

	return results
}

func cloneJournal(journal *VMJournal) *VMJournal {
	out := *journal
	out.Files = append([]FileEntry(nil), journal.Files...)
	out.Targets = append([]TargetResult(nil), journal.Targets...)

	return &out
}

func onlyResult(t *testing.T, results []TargetResult) TargetResult {
	t.Helper()

	if len(results) != 1 {
		t.Fatalf("expected one result, got %+v", results)
	}

	return results[0]
}

func TestVerifyTargetsProbeThenFallback(t *testing.T) {
	t.Parallel()

	env := newVerifyEnv(t)
	port := closedPort(t)
	run := &fakeVerifyRun{result: ssh.Result{Stdout: []byte("fingerprint=" + env.newFP + "\n")}}

	got := onlyResult(t, env.verify(env.journal(port), run.run))

	want := TargetResult{Variable: regenLeaf, Port: port, Method: verifyMethodOpenSSL, Fingerprint: env.newFP, OK: true}
	if got.Variable != want.Variable || got.Port != want.Port || got.Method != want.Method || got.Fingerprint != want.Fingerprint || !got.OK {
		t.Fatalf("expected %+v, got %+v", want, got)
	}

	calls := run.recorded()
	if len(calls) != 1 || !reflect.DeepEqual(calls[0], []string{verifyScript, strconv.Itoa(port), verifyServerName}) {
		t.Fatalf("expected one verify run with the port and the server name, got %q", calls)
	}
}

func TestVerifyTargetsProbeSendsServerName(t *testing.T) {
	t.Parallel()

	env := newVerifyEnv(t)
	listener := startSNIListener(t, env.newLeaf, env.newKey)
	run := &fakeVerifyRun{err: errFallbackRan}

	got := onlyResult(t, env.verify(env.journal(listener.port()), run.run))
	if got.Method != verifyMethodProbe || !got.OK || got.Fingerprint != env.newFP || got.Port != listener.port() {
		t.Fatalf("expected a passing probe, got %+v", got)
	}

	if names := listener.serverNames(); len(names) != 1 || names[0] != verifyServerName {
		t.Fatalf("expected one handshake naming %s, got %q", verifyServerName, names)
	}

	if calls := run.recorded(); len(calls) != 0 {
		t.Fatalf("the fallback ran after a probe that worked: %q", calls)
	}
}

func TestVerifyTargetsOldFingerprintFails(t *testing.T) {
	t.Parallel()

	t.Run("probe", func(t *testing.T) {
		t.Parallel()

		env := newVerifyEnv(t)
		oldKey := env.listenerKey(t)
		listener := startSNIListener(t, env.oldLeaf, oldKey)

		got := onlyResult(t, env.verify(env.journal(listener.port()), (&fakeVerifyRun{}).run))
		if got.Method != verifyMethodProbe || got.OK || got.Fingerprint != env.oldFP || got.Detail == "" {
			t.Fatalf("expected a failing probe that names the old fingerprint, got %+v", got)
		}
	})

	t.Run("openssl", func(t *testing.T) {
		t.Parallel()

		env := newVerifyEnv(t)
		run := &fakeVerifyRun{result: ssh.Result{Stdout: []byte("fingerprint=" + env.oldFP + "\n")}}

		got := onlyResult(t, env.verify(env.journal(closedPort(t)), run.run))
		if got.Method != verifyMethodOpenSSL || got.OK || got.Fingerprint != env.oldFP || got.Detail == "" {
			t.Fatalf("expected a failing openssl check that names the old fingerprint, got %+v", got)
		}
	})
}

// listenerKey returns the key of the env's old leaf, which the redeploy
// env's listener holds.
func (e *verifyEnv) listenerKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()

	e.listener.mu.Lock()
	defer e.listener.mu.Unlock()

	key, ok := e.listener.pair.PrivateKey.(*ecdsa.PrivateKey)
	if !ok {
		t.Fatal("the redeploy listener holds no ECDSA key")
	}

	return key
}

func TestVerifyTargetsUnverifiedConsumer(t *testing.T) {
	t.Parallel()

	const other = "/ws-bosh/redis-instance-1/client_crt"

	env := newVerifyEnv(t)
	listener := startSNIListener(t, env.newLeaf, env.newKey)

	journal := env.journal(listener.port())
	journal.Files = append(journal.Files,
		FileEntry{Path: "/var/vcap/data/jobs/redis/abc/config/tls/client.crt", Job: certJob, Role: FileRoleCertificate, Variable: other},
		FileEntry{Path: "/var/vcap/data/jobs/redis/abc/config/tls/client.key", Job: certJob, Role: FileRoleKey, Variable: other},
	)

	results := env.verify(journal, (&fakeVerifyRun{}).run)
	if len(results) != 2 {
		t.Fatalf("expected the listener and one unverified consumer, got %+v", results)
	}

	if results[0].Variable != regenLeaf || results[0].Method != verifyMethodProbe || !results[0].OK {
		t.Fatalf("expected the listener's probe first, got %+v", results[0])
	}

	want := TargetResult{Variable: other, Method: verifyMethodNone, OK: true, Detail: unverifiedDetail}
	if results[1] != want {
		t.Fatalf("expected %+v, got %+v", want, results[1])
	}
}

func TestVerifyTargetsFallbackFailures(t *testing.T) {
	t.Parallel()

	cases := map[string]*fakeVerifyRun{
		"the run fails":       {err: errLostSession},
		"the script exits 1":  {result: ssh.Result{ExitCode: 1}},
		"the output is cut":   {result: ssh.Result{Stdout: []byte("fingerprint=" + strings.Repeat("a", 64) + "\n"), Truncated: true}},
		"two fingerprints":    {result: ssh.Result{Stdout: []byte("fingerprint=" + strings.Repeat("a", 64) + "\nfingerprint=" + strings.Repeat("b", 64) + "\n")}},
		"a short fingerprint": {result: ssh.Result{Stdout: []byte("fingerprint=abc\n")}},
		"an unknown key":      {result: ssh.Result{Stdout: []byte("subject=CN\n")}},
		"nothing printed":     {result: ssh.Result{}},
	}

	for name, run := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			env := newVerifyEnv(t)

			got := onlyResult(t, env.verify(env.journal(closedPort(t)), run.run))
			if got.Method != verifyMethodOpenSSL || got.OK || got.Fingerprint != "" || got.Detail == "" {
				t.Fatalf("expected a failing openssl check with no fingerprint, got %+v", got)
			}

			if strings.Contains(got.Detail, "connection lost") {
				t.Fatalf("the detail %q repeats the runner's error", got.Detail)
			}
		})
	}

	t.Run("no runner", func(t *testing.T) {
		t.Parallel()

		env := newVerifyEnv(t)

		got := onlyResult(t, env.verify(env.journal(closedPort(t)), nil))
		if got.Method != verifyMethodOpenSSL || got.OK || got.Detail == "" {
			t.Fatalf("expected a failing openssl check, got %+v", got)
		}
	})
}

// A VM the director no longer lists, or lists with no address, is checked
// from the VM itself.
func TestVerifyTargetsWithoutAddress(t *testing.T) {
	t.Parallel()

	for name, vms := range map[string][]bosh.VM{
		"another CID": {{CID: "vm-cid-9", IPs: []string{loopback}}},
		"no address":  {{CID: redeployVMCID}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			env := newVerifyEnv(t)
			env.manager.director = &listedDirector{redeployDirector: env.director, vms: vms}
			listener := startSNIListener(t, env.newLeaf, env.newKey)
			run := &fakeVerifyRun{result: ssh.Result{Stdout: []byte("fingerprint=" + env.newFP + "\n")}}

			got := onlyResult(t, env.verify(env.journal(listener.port()), run.run))
			if got.Method != verifyMethodOpenSSL || !got.OK {
				t.Fatalf("expected a passing openssl check, got %+v", got)
			}

			if names := listener.serverNames(); len(names) != 0 {
				t.Fatalf("the broker probed a VM it has no address for: %q", names)
			}
		})
	}
}

// listedDirector is the redeploy director with the VM list the test sets.
type listedDirector struct {
	*redeployDirector

	vms []bosh.VM
}

func (d *listedDirector) GetDeploymentVMs(context.Context, string) ([]bosh.VM, error) {
	return append([]bosh.VM(nil), d.vms...), nil
}

// The checks send the server name the caller put on the target, and they
// never read CredHub, whose version answer carries the private key.
func TestVerifyTargetsUseTheTargetsServerName(t *testing.T) {
	t.Parallel()

	const name = "redis-0.service.internal"

	env := newVerifyEnv(t)
	listener := startSNIListener(t, env.newLeaf, env.newKey)

	journal := env.journal(listener.port())
	journal.Targets[0].ServerName = name

	got := onlyResult(t, env.verify(journal, (&fakeVerifyRun{}).run))
	if got.Method != verifyMethodProbe || !got.OK {
		t.Fatalf("expected a passing probe, got %+v", got)
	}

	if names := listener.serverNames(); len(names) != 1 || names[0] != name {
		t.Fatalf("expected one handshake naming %s, got %q", name, names)
	}

	run := &fakeVerifyRun{result: ssh.Result{Stdout: []byte("fingerprint=" + env.newFP + "\n")}}
	port := closedPort(t)

	journal = env.journal(port)
	journal.Targets[0].ServerName = name

	got = onlyResult(t, env.verify(journal, run.run))
	if !got.OK || !reflect.DeepEqual(run.recorded(), [][]string{{verifyScript, strconv.Itoa(port), name}}) {
		t.Fatalf("expected the fallback with the target's server name, got %+v and %q", got, run.recorded())
	}

	if reads := env.credhub.readList(); len(reads) != 0 {
		t.Fatalf("verification read CredHub versions %q", reads)
	}
}

// A target with no server name, or one verify.sh would refuse, gets no
// SNI, because the fingerprint decides the result either way. The new
// version's common name is never read in its place.
func TestVerifyTargetsWithoutServerName(t *testing.T) {
	t.Parallel()

	for label, name := range map[string]string{"no name": "", "a wildcard": "*.ws.example", "an option": "-connect"} {
		t.Run(label, func(t *testing.T) {
			t.Parallel()

			env := newVerifyEnv(t)
			listener := startSNIListener(t, env.newLeaf, env.newKey)

			journal := env.journal(listener.port())
			journal.Targets[0].ServerName = name

			got := onlyResult(t, env.verify(journal, (&fakeVerifyRun{}).run))
			if got.Method != verifyMethodProbe || !got.OK {
				t.Fatalf("expected a passing probe, got %+v", got)
			}

			if names := listener.serverNames(); len(names) != 1 || names[0] != "" {
				t.Fatalf("expected one handshake with no server name, got %q", names)
			}

			run := &fakeVerifyRun{result: ssh.Result{Stdout: []byte("fingerprint=" + env.newFP + "\n")}}
			port := closedPort(t)

			journal = env.journal(port)
			journal.Targets[0].ServerName = name

			got = onlyResult(t, env.verify(journal, run.run))
			if !got.OK || !reflect.DeepEqual(run.recorded(), [][]string{{verifyScript, strconv.Itoa(port), ""}}) {
				t.Fatalf("expected the fallback with an empty server name, got %+v and %q", got, run.recorded())
			}

			if reads := env.credhub.readList(); len(reads) != 0 {
				t.Fatalf("verification read CredHub versions %q", reads)
			}
		})
	}
}

// A target with no valid port fails, and its variable never becomes an
// unverified consumer that passes.
func TestVerifyTargetsWithoutPort(t *testing.T) {
	t.Parallel()

	for _, port := range []int{0, -1, 65536} {
		t.Run(strconv.Itoa(port), func(t *testing.T) {
			t.Parallel()

			env := newVerifyEnv(t)
			run := &fakeVerifyRun{result: ssh.Result{Stdout: []byte("fingerprint=" + env.newFP + "\n")}}

			got := onlyResult(t, env.verify(env.journal(port), run.run))
			if got.Variable != regenLeaf || got.OK || got.Fingerprint != "" || got.Detail == "" {
				t.Fatalf("expected a failed target with a reason, got %+v", got)
			}

			if calls := run.recorded(); len(calls) != 0 {
				t.Fatalf("a target with no port ran a check on the VM: %q", calls)
			}
		})
	}
}

// readList returns the version IDs the fake has served so far.
func (f *regenCredHub) readList() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return slices.Clone(f.reads)
}

// A variable with no fingerprint to compare against never passes.
func TestVerifyTargetsWithoutNewFingerprint(t *testing.T) {
	t.Parallel()

	env := newVerifyEnv(t)
	listener := startSNIListener(t, env.newLeaf, env.newKey)

	results := env.manager.verifyTargets(t.Context(), env.inst, env.journal(listener.port()), map[string]string{}, (&fakeVerifyRun{}).run)

	got := onlyResult(t, results)
	if got.OK || !strings.Contains(got.Detail, "No new fingerprint") {
		t.Fatalf("expected a failure with no fingerprint to compare, got %+v", got)
	}
}

func TestServerNameFor(t *testing.T) {
	t.Parallel()

	for name, want := range map[string]string{
		verifyServerName:           verifyServerName,
		"*.ws.example":             "",
		"-connect":                 "",
		"orders ws":                "",
		"":                         "",
		strings.Repeat("a", 253):   strings.Repeat("a", 253),
		strings.Repeat("a", 254):   "",
		"redis_0.internal-network": "redis_0.internal-network",
	} {
		if got := validServerName(name); got != want {
			t.Errorf("validServerName(%q) = %q, want %q", name, got, want)
		}
	}
}
