package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	droppedDeployment = "doomed"
	otherDeployment   = "spared"
	droppedContextID  = "2b1f3c4d-0e5f-4a6b-8c7d-9e0f1a2b3c4d"
	resentContextID   = "7c6b5a49-3827-4615-9a8b-7c6d5e4f3a2b"
	taskLocation      = "/tasks/7"
)

// seenRequest is a request the director behind the proxy received.
type seenRequest struct {
	method    string
	path      string
	query     string
	contextID string
}

// fakeDirector is a TLS director on a loopback address that records each
// request and answers a delete with a redirect to its task.
type fakeDirector struct {
	server *httptest.Server

	// abortDeletes is how many deletes from now on the director drops
	// with no answer.
	abortDeletes atomic.Int32

	mu   sync.Mutex
	seen []seenRequest
}

func newFakeDirector(t *testing.T) *fakeDirector {
	t.Helper()

	director := &fakeDirector{}
	director.server = httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		director.mu.Lock()
		director.seen = append(director.seen, seenRequest{
			method: request.Method, path: request.URL.Path, query: request.URL.RawQuery,
			contextID: request.Header.Get(contextIDHeader),
		})
		director.mu.Unlock()

		if request.Method == http.MethodDelete && director.abortDeletes.Add(-1) >= 0 {
			panic(http.ErrAbortHandler)
		}

		if request.Method == http.MethodDelete {
			writer.Header().Set("Location", taskLocation)
			writer.WriteHeader(http.StatusFound)

			return
		}

		_, _ = io.WriteString(writer, "[]")
	}))
	t.Cleanup(director.server.Close)

	return director
}

func (d *fakeDirector) requests() []seenRequest {
	d.mu.Lock()
	defer d.mu.Unlock()

	return append([]seenRequest(nil), d.seen...)
}

// syncBuffer is a log destination that tests can read while the proxy
// writes to it.
type syncBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buffer.Write(p) //nolint:wrapcheck // a bytes.Buffer write never fails
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buffer.String()
}

// proxyClient sends requests through a proxy that serves TLS on a
// loopback address with the certificate it generated.
type proxyClient struct {
	t      *testing.T
	client *http.Client
	base   string
	logs   *syncBuffer
}

func startProxy(t *testing.T, director *fakeDirector, hold time.Duration, now func() time.Time) *proxyClient {
	t.Helper()

	directorURL, err := url.Parse(director.server.URL)
	if err != nil {
		t.Fatalf("parse the director URL: %v", err)
	}

	directorCAs := x509.NewCertPool()
	directorCAs.AddCert(director.server.Certificate())

	logs := &syncBuffer{}

	proxy, err := NewProxy(Config{
		Director: directorURL, DirectorCAs: directorCAs, DropDelete: droppedDeployment,
		HoldLookups: hold, Now: now, Log: log.New(logs, "", 0),
	})
	if err != nil {
		t.Fatalf("build the proxy: %v", err)
	}

	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen on loopback: %v", err)
	}

	certificate, certificatePEM, err := generateCertificate("127.0.0.1", time.Now())
	if err != nil {
		t.Fatalf("generate the certificate: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)

	go func() { served <- serve(ctx, listener, newServer(proxy, certificate)) }()

	t.Cleanup(func() {
		cancel()

		err := <-served
		if err != nil {
			t.Errorf("the proxy did not shut down cleanly: %v", err)
		}
	})

	proxyCAs := x509.NewCertPool()
	if !proxyCAs.AppendCertsFromPEM(certificatePEM) {
		t.Fatal("the proxy's certificate PEM holds no certificate")
	}

	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig:   &tls.Config{RootCAs: proxyCAs, MinVersion: tls.VersionTLS12},
			DisableKeepAlives: true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Timeout:       10 * time.Second,
	}

	return &proxyClient{t: t, client: client, base: "https://" + listener.Addr().String(), logs: logs}
}

// send sends a request through the proxy, with contextID in the context ID
// header unless it is empty.
func (c *proxyClient) send(method, target, contextID string) (*http.Response, error) {
	c.t.Helper()

	request, err := http.NewRequestWithContext(c.t.Context(), method, c.base+target, nil)
	if err != nil {
		c.t.Fatalf("build %s %s: %v", method, target, err)
	}

	if contextID != "" {
		request.Header.Set(contextIDHeader, contextID)
	}

	return c.client.Do(request) //nolint:wrapcheck // the test reads the client's own error
}

// expectStatus sends a request through the proxy and checks its status.
func (c *proxyClient) expectStatus(method, target, contextID string, want int) {
	c.t.Helper()

	response, err := c.send(method, target, contextID)
	if err != nil {
		c.t.Fatalf("%s %s through the proxy failed: %v", method, target, err)
	}

	_ = response.Body.Close()

	if response.StatusCode != want {
		c.t.Fatalf("%s %s through the proxy answered %d, want %d", method, target, response.StatusCode, want)
	}
}

// expectDropped sends the delete whose answer the proxy drops, and checks
// that the client saw its connection close with no answer.
func (c *proxyClient) expectDropped(contextID string) {
	c.t.Helper()

	response, err := c.send(http.MethodDelete, "/deployments/"+droppedDeployment, contextID)
	if err == nil {
		_ = response.Body.Close()
		c.t.Fatalf("the dropped delete got the answer %d", response.StatusCode)
	}

	if !errors.Is(err, io.EOF) && !strings.Contains(err.Error(), "connection reset") {
		c.t.Fatalf("the dropped delete failed with %v, want a closed connection", err)
	}

	if !strings.Contains(c.logs.String(), "dropped") || !strings.Contains(c.logs.String(), contextID) {
		c.t.Fatalf("the proxy did not log the drop with context ID %s: %q", contextID, c.logs.String())
	}
}

func TestFaultProxyDropsOnlyTheDeleteAnswer(t *testing.T) {
	t.Parallel()

	director := newFakeDirector(t)
	proxy := startProxy(t, director, 0, time.Now)

	proxy.expectStatus(http.MethodGet, "/deployments/"+droppedDeployment, "", http.StatusOK)
	proxy.expectStatus(http.MethodDelete, "/deployments/"+otherDeployment, "", http.StatusFound)

	proxy.expectDropped(droppedContextID)

	proxy.expectStatus(http.MethodDelete, "/deployments/"+droppedDeployment, resentContextID, http.StatusFound)
	proxy.expectStatus(http.MethodDelete, "/deployments/"+otherDeployment, "", http.StatusFound)
	proxy.expectStatus(http.MethodGet, "/deployments/"+droppedDeployment, "", http.StatusOK)
	proxy.expectStatus(http.MethodGet, "/tasks?context_id="+droppedContextID, "", http.StatusOK)
	proxy.expectStatus(http.MethodPost, "/deployments", "", http.StatusOK)

	want := []seenRequest{
		{method: http.MethodGet, path: "/deployments/" + droppedDeployment},
		{method: http.MethodDelete, path: "/deployments/" + otherDeployment},
		{method: http.MethodDelete, path: "/deployments/" + droppedDeployment, contextID: droppedContextID},
		{method: http.MethodDelete, path: "/deployments/" + droppedDeployment, contextID: resentContextID},
		{method: http.MethodDelete, path: "/deployments/" + otherDeployment},
		{method: http.MethodGet, path: "/deployments/" + droppedDeployment},
		{method: http.MethodGet, path: "/tasks", query: "context_id=" + droppedContextID},
		{method: http.MethodPost, path: "/deployments"},
	}

	got := director.requests()
	if len(got) != len(want) {
		t.Fatalf("the director received %+v, want %+v", got, want)
	}

	for index := range want {
		if got[index] != want[index] {
			t.Errorf("request %d reached the director as %+v, want %+v", index, got[index], want[index])
		}
	}
}

func TestFaultProxyHoldsContextIDLookups(t *testing.T) {
	t.Parallel()

	const hold = 3 * time.Minute

	var offset atomic.Int64

	now := func() time.Time { return time.Now().Add(time.Duration(offset.Load())) }

	director := newFakeDirector(t)
	proxy := startProxy(t, director, hold, now)
	lookup := "/tasks?context_id=" + droppedContextID

	// The hold starts at the drop.
	proxy.expectStatus(http.MethodGet, lookup, "", http.StatusOK)

	proxy.expectDropped(droppedContextID)

	proxy.expectStatus(http.MethodGet, lookup, "", http.StatusServiceUnavailable)

	if !strings.Contains(proxy.logs.String(), "refused GET "+lookup+" with context ID "+droppedContextID) {
		t.Fatalf("the proxy did not log the refused lookup with its context ID: %q", proxy.logs.String())
	}

	proxy.expectStatus(http.MethodGet, "/tasks?deployment="+droppedDeployment, "", http.StatusOK)
	proxy.expectStatus(http.MethodGet, taskLocation+"?context_id="+droppedContextID, "", http.StatusOK)
	proxy.expectStatus(http.MethodDelete, lookup, "", http.StatusFound)

	offset.Store(int64(hold + time.Second))

	proxy.expectStatus(http.MethodGet, lookup, "", http.StatusOK)

	lookups := 0

	for _, request := range director.requests() {
		if request.method == http.MethodGet && request.path == "/tasks" &&
			request.query == "context_id="+droppedContextID {
			lookups++
		}
	}

	if lookups != 2 {
		t.Fatalf("the director received %d lookups by context ID, want the one before the drop and the one after the hold", lookups)
	}
}

func TestFaultProxyListensOnlyOnLoopback(t *testing.T) {
	t.Parallel()

	for _, listen := range []string{"127.0.0.1:25556", "[::1]:25556", "localhost:25556", "[::ffff:127.0.0.1]:25556"} {
		_, err := loopbackHost(listen)
		if err != nil {
			t.Errorf("the loopback address %s was refused: %v", listen, err)
		}
	}

	for _, listen := range []string{
		"0.0.0.0:25556", "10.0.0.6:25556", ":25556", "director.example:25556",
		"[::]:25556", "[::1%lo0]:25556", "[fe80::1%lo0]:25556",
	} {
		_, err := loopbackHost(listen)
		if !errors.Is(err, errNotLoopback) {
			t.Errorf("the address %s was not refused as not loopback: %v", listen, err)
		}
	}
}

func TestFaultProxyKeepsTheDropWhenTheDirectorDoesNotAnswer(t *testing.T) {
	t.Parallel()

	director := newFakeDirector(t)
	director.abortDeletes.Store(1)

	client := startProxy(t, director, 0, time.Now)

	client.expectStatus(http.MethodDelete, "/deployments/"+droppedDeployment, droppedContextID, http.StatusBadGateway)

	if !strings.Contains(client.logs.String(), "the drop stays for the next one") {
		t.Fatalf("the proxy did not log that the drop stays: %q", client.logs.String())
	}

	client.expectDropped(resentContextID)
	client.expectStatus(http.MethodDelete, "/deployments/"+droppedDeployment, resentContextID, http.StatusFound)
}

// offLoopbackListener is a loopback listener that reports an address that
// is not a loopback address, as a listen on a name that resolves off
// loopback would.
type offLoopbackListener struct {
	net.Listener

	closed atomic.Bool
}

func (l *offLoopbackListener) Addr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("192.0.2.1"), Port: 25556}
}

func (l *offLoopbackListener) Close() error {
	l.closed.Store(true)

	return l.Listener.Close() //nolint:wrapcheck // the test only closes its own listener
}

// runArgs are the flags of a run that forwards to an address nothing
// answers on, and writes its certificate to certOut.
func runArgs(listenAddress, certOut string) []string {
	return []string{"-listen", listenAddress, "-cert-out", certOut, "-director", "https://127.0.0.1:1"}
}

func TestRunRefusesAnAddressThatIsNotLoopback(t *testing.T) {
	t.Parallel()

	certOut := t.TempDir() + "/proxy.pem"

	err := run(t.Context(), runArgs("192.0.2.1:0", certOut), listenTCP, log.New(io.Discard, "", 0))
	if !errors.Is(err, errNotLoopback) {
		t.Fatalf("run on 192.0.2.1 returned %v, want %v", err, errNotLoopback)
	}

	_, statErr := os.Stat(certOut)
	if !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("run wrote a certificate for an address it refused: %v", statErr)
	}
}

func TestRunRefusesAListenerBoundOffLoopback(t *testing.T) {
	t.Parallel()

	certOut := t.TempDir() + "/proxy.pem"

	var bound *offLoopbackListener

	listenOffLoopback := func(ctx context.Context, _ string) (net.Listener, error) {
		listener, err := listenTCP(ctx, "127.0.0.1:0")
		if err != nil {
			return nil, err
		}

		bound = &offLoopbackListener{Listener: listener}

		return bound, nil
	}

	// A run that serves on the refused listener ends with the deadline.
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	err := run(ctx, runArgs("localhost:0", certOut), listenOffLoopback, log.New(io.Discard, "", 0))
	if !errors.Is(err, errNotLoopback) {
		t.Fatalf("run on a listener bound off loopback returned %v, want %v", err, errNotLoopback)
	}

	if bound == nil || !bound.closed.Load() {
		t.Fatal("run left open the listener it refused")
	}

	_, statErr := os.Stat(certOut)
	if !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("run wrote a certificate for a listener it refused: %v", statErr)
	}
}

func TestRunWritesACertificateOnlyItsUserCanRead(t *testing.T) {
	t.Parallel()

	certOut := t.TempDir() + "/proxy.pem"

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	finished := make(chan error, 1)

	go func() { finished <- run(ctx, runArgs("127.0.0.1:0", certOut), listenTCP, log.New(io.Discard, "", 0)) }()

	deadline := time.Now().Add(10 * time.Second)

	var info os.FileInfo

	for {
		var err error

		info, err = os.Stat(certOut)
		if err == nil && info.Size() > 0 {
			break
		}

		if time.Now().After(deadline) {
			t.Fatalf("run wrote no certificate: %v", err)
		}

		time.Sleep(10 * time.Millisecond)
	}

	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("the certificate has mode %o, want 600", mode)
	}

	certificatePEM, err := os.ReadFile(certOut) //nolint:gosec // the test reads the file it named in its own temporary directory
	if err != nil {
		t.Fatalf("read the certificate: %v", err)
	}

	if !x509.NewCertPool().AppendCertsFromPEM(certificatePEM) {
		t.Fatal("the certificate file holds no PEM certificate")
	}

	cancel()

	err = <-finished
	if err != nil {
		t.Fatalf("run did not stop cleanly: %v", err)
	}
}
