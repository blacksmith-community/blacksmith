package redis_test

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"blacksmith/pkg/services/common"
	. "blacksmith/pkg/services/redis"

	goredis "github.com/redis/go-redis/v9"
)

const testPassword = "s3cret-test-password"

var errBadRESP = errors.New("bad RESP frame")

// testPKI is a throwaway CA and a server certificate it signed for
// localhost and 127.0.0.1.
type testPKI struct {
	caPEM      string
	serverCert tls.Certificate
}

func newTestPKI(t *testing.T) *testPKI {
	t.Helper()

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}

	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "redis-test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create CA: %v", err)
	}

	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatalf("parse CA: %v", err)
	}

	serverKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate server key: %v", err)
	}

	serverTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "localhost"},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}

	serverDER, err := x509.CreateCertificate(rand.Reader, serverTemplate, caCert, &serverKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create server certificate: %v", err)
	}

	return &testPKI{
		caPEM:      string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})),
		serverCert: tls.Certificate{Certificate: [][]byte{serverDER}, PrivateKey: serverKey},
	}
}

// fakeRedis is a TLS listener that speaks just enough RESP for a client to
// AUTH, PING, and ask for INFO. It records every command it receives.
type fakeRedis struct {
	listener net.Listener
	mu       sync.Mutex
	commands [][]string
	accepted int
}

func newFakeRedis(t *testing.T, pki *testPKI) *fakeRedis {
	t.Helper()

	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{pki.serverCert},
		MinVersion:   tls.VersionTLS12,
	})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	server := &fakeRedis{listener: listener}

	go server.serve()

	t.Cleanup(func() { _ = listener.Close() })

	return server
}

func (s *fakeRedis) port() int {
	addr, ok := s.listener.Addr().(*net.TCPAddr)
	if !ok {
		return 0
	}

	return addr.Port
}

func (s *fakeRedis) serve() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}

		s.mu.Lock()
		s.accepted++
		s.mu.Unlock()

		go s.handle(conn)
	}
}

func (s *fakeRedis) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()

	reader := bufio.NewReader(conn)

	for {
		command, err := readCommand(reader)
		if err != nil {
			return
		}

		s.mu.Lock()
		s.commands = append(s.commands, command)
		s.mu.Unlock()

		reply := "+OK\r\n"

		switch strings.ToUpper(command[0]) {
		case "HELLO":
			// go-redis v9 opens every connection with HELLO <proto> and
			// expects the map a real server returns.
			reply = "*14\r\n$6\r\nserver\r\n$5\r\nredis\r\n$7\r\nversion\r\n$5\r\n7.2.0\r\n" +
				"$5\r\nproto\r\n:2\r\n$2\r\nid\r\n:1\r\n$4\r\nmode\r\n$10\r\nstandalone\r\n" +
				"$4\r\nrole\r\n$6\r\nmaster\r\n$7\r\nmodules\r\n*0\r\n"
		case "PING":
			reply = "+PONG\r\n"
		case "INFO":
			info := "# Server\r\nredis_version:7.2.0\r\n"
			reply = fmt.Sprintf("$%d\r\n%s\r\n", len(info), info)
		}

		_, err = io.WriteString(conn, reply)
		if err != nil {
			return
		}
	}
}

func (s *fakeRedis) recorded() ([][]string, int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([][]string(nil), s.commands...), s.accepted
}

func readCommand(reader *bufio.Reader) ([]string, error) {
	header, err := reader.ReadString('\n')
	if err != nil {
		return nil, fmt.Errorf("read array header: %w", err)
	}

	count, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(header, "*")))
	if err != nil || count < 1 {
		return nil, fmt.Errorf("%w: array header %q", errBadRESP, header)
	}

	args := make([]string, 0, count)

	for range count {
		lengthLine, err := reader.ReadString('\n')
		if err != nil {
			return nil, fmt.Errorf("read bulk header: %w", err)
		}

		length, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(lengthLine, "$")))
		if err != nil {
			return nil, fmt.Errorf("%w: bulk header %q", errBadRESP, lengthLine)
		}

		buf := make([]byte, length+2)

		_, err = io.ReadFull(reader, buf)
		if err != nil {
			return nil, fmt.Errorf("read bulk string: %w", err)
		}

		args = append(args, string(buf[:length]))
	}

	return args, nil
}

func handshake(t *testing.T, server *fakeRedis, config *tls.Config) error {
	t.Helper()

	dialer := &tls.Dialer{NetDialer: &net.Dialer{Timeout: 5 * time.Second}, Config: config}

	conn, err := dialer.DialContext(context.Background(), "tcp", fmt.Sprintf("127.0.0.1:%d", server.port()))
	if err != nil {
		return err //nolint:wrapcheck // the callers only check whether the handshake failed
	}

	return conn.Close() //nolint:wrapcheck // as above
}

func TestClientOptionsCarriesUsername(t *testing.T) {
	t.Parallel()

	opts, err := ClientOptions(&Credentials{
		Host:     "valkey.example.com",
		Port:     6379,
		Username: "default",
		Password: testPassword,
	}, false)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if opts.Username != "default" {
		t.Errorf("Username = %q, expected %q", opts.Username, "default")
	}

	if opts.Password != testPassword {
		t.Errorf("Password was not carried into the client options")
	}

	if opts.Addr != "valkey.example.com:6379" {
		t.Errorf("Addr = %q, expected valkey.example.com:6379", opts.Addr)
	}

	if opts.TLSConfig != nil {
		t.Errorf("a plain-text connection got a TLS config")
	}
}

func TestClientOptionsWithoutUsernameSendsPasswordOnly(t *testing.T) {
	t.Parallel()

	opts, err := ClientOptions(&Credentials{Host: "redis.example.com", Port: 6379, Password: testPassword}, false)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if opts.Username != "" {
		t.Errorf("Username = %q, expected none", opts.Username)
	}

	if opts.Password != testPassword {
		t.Errorf("Password was not carried into the client options")
	}
}

func TestClientOptionsTLSTrustsCACertificate(t *testing.T) {
	t.Parallel()

	pki := newTestPKI(t)
	server := newFakeRedis(t, pki)

	// The dial address is an IP here, so the server name is that IP and the
	// certificate must carry it as an IP SAN.
	opts, err := ClientOptions(&Credentials{
		Host:          "127.0.0.1",
		TLSPort:       server.port(),
		Password:      testPassword,
		CACertificate: pki.caPEM,
	}, true)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if opts.TLSConfig == nil {
		t.Fatal("TLS connection has no TLS config")
	}

	if opts.TLSConfig.InsecureSkipVerify {
		t.Error("TLS config skips verification")
	}

	if opts.TLSConfig.RootCAs == nil {
		t.Error("TLS config does not carry the instance CA")
	}

	if opts.TLSConfig.ServerName != "127.0.0.1" {
		t.Errorf("ServerName = %q, expected the host", opts.TLSConfig.ServerName)
	}

	if opts.Addr != fmt.Sprintf("127.0.0.1:%d", server.port()) {
		t.Errorf("Addr = %q, expected the TLS port", opts.Addr)
	}

	err = handshake(t, server, opts.TLSConfig)
	if err != nil {
		t.Errorf("handshake against a server signed by ca_certificate failed: %v", err)
	}
}

func TestClientOptionsTLSChecksServerName(t *testing.T) {
	t.Parallel()

	pki := newTestPKI(t)
	server := newFakeRedis(t, pki)

	opts, err := ClientOptions(&Credentials{
		Host:          "other.example.com",
		TLSPort:       server.port(),
		CACertificate: pki.caPEM,
	}, true)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	err = handshake(t, server, opts.TLSConfig)
	if err == nil {
		t.Error("handshake succeeded although the certificate does not name the host")
	}
}

func TestClientOptionsTLSWithoutCAUsesSystemRoots(t *testing.T) {
	t.Parallel()

	pki := newTestPKI(t)
	server := newFakeRedis(t, pki)

	opts, err := ClientOptions(&Credentials{Host: "127.0.0.1", TLSPort: server.port()}, true)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if opts.TLSConfig == nil {
		t.Fatal("TLS connection has no TLS config")
	}

	if opts.TLSConfig.RootCAs != nil {
		t.Error("RootCAs is set, expected nil so the system roots apply")
	}

	if opts.TLSConfig.InsecureSkipVerify {
		t.Error("TLS config skips verification")
	}

	err = handshake(t, server, opts.TLSConfig)
	if err == nil {
		t.Error("handshake trusted a private CA that is not in the system roots")
	}
}

func TestClientOptionsRejectsUnusableCACertificate(t *testing.T) {
	t.Parallel()

	_, err := ClientOptions(&Credentials{
		Host:          "valkey.example.com",
		TLSPort:       6379,
		CACertificate: "not-a-certificate",
	}, true)
	if err == nil {
		t.Fatal("expected an error for a ca_certificate with no PEM certificate")
	}

	if strings.Contains(err.Error(), "not-a-certificate") {
		t.Errorf("error repeats the ca_certificate value: %v", err)
	}
}

func TestClientOptionsStandardOnTLSOnlyInstance(t *testing.T) {
	t.Parallel()

	creds, err := NewCredentials(common.Credentials{
		"host":     "valkey.example.com",
		"port":     0,
		"tls_port": 6379,
		"username": "default",
		"password": testPassword,
	})
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	_, err = ClientOptions(creds, false)
	if err == nil {
		t.Fatal("expected Standard to refuse a TLS-only instance instead of dialing its TLS port in plain text")
	}

	if !strings.Contains(err.Error(), "TLS") {
		t.Errorf("error does not point at TLS: %v", err)
	}

	if strings.Contains(err.Error(), testPassword) {
		t.Errorf("error repeats the password: %v", err)
	}
}

func TestClientOptionsTLSWithoutTLSEndpoint(t *testing.T) {
	t.Parallel()

	_, err := ClientOptions(&Credentials{Host: "redis.example.com", Port: 6379, Password: testPassword}, true)
	if err == nil {
		t.Fatal("expected TLS to refuse an instance with no TLS port instead of dialing plain text")
	}
}

func valkeyTLSCreds(pki *testPKI, server *fakeRedis) common.Credentials {
	return common.Credentials{
		"host":           "localhost",
		"port":           0,
		"tls_port":       server.port(),
		"username":       "default",
		"password":       testPassword,
		"admin_password": testPassword,
		"ca_certificate": pki.caPEM,
		"service_type":   "valkey",
	}
}

func TestTestConnectionValkeyOverTLS(t *testing.T) {
	t.Parallel()

	pki := newTestPKI(t)
	server := newFakeRedis(t, pki)
	handler := NewHandler(nil)

	t.Cleanup(func() { _ = handler.Close() })

	result, err := handler.TestConnection(context.Background(), valkeyTLSCreds(pki, server), common.ConnectionOptions{UseTLS: true})
	if err != nil {
		t.Fatalf("TestConnection failed: %v", err)
	}

	if !result.Success {
		t.Fatalf("TestConnection did not succeed: %s", result.Error)
	}

	connection, _ := result.Data["connection"].(map[string]interface{})
	if connection["port"] != server.port() {
		t.Errorf("connection port = %v, expected the TLS port %d", connection["port"], server.port())
	}

	commands, _ := server.recorded()

	// go-redis v9 authenticates inside its opening HELLO 2 rather than with a
	// separate AUTH command, so the credentials ride on that handshake.
	var hello []string

	for _, command := range commands {
		if strings.EqualFold(command[0], "HELLO") {
			hello = command

			break
		}
	}

	if len(hello) != 5 || hello[1] != "2" || !strings.EqualFold(hello[2], "AUTH") ||
		hello[3] != "default" || hello[4] != testPassword {
		t.Errorf("HELLO did not ask for RESP2 with the username and password, got %d args", len(hello))
	}
}

func TestTestConnectionDoesNotReuseAnotherInstancesClient(t *testing.T) {
	t.Parallel()

	pki := newTestPKI(t)
	first := newFakeRedis(t, pki)
	second := newFakeRedis(t, pki)
	handler := NewHandler(nil)

	t.Cleanup(func() { _ = handler.Close() })

	for _, server := range []*fakeRedis{first, second} {
		_, err := handler.TestConnection(context.Background(), valkeyTLSCreds(pki, server), common.ConnectionOptions{UseTLS: true})
		if err != nil {
			t.Fatalf("TestConnection failed: %v", err)
		}
	}

	_, accepted := second.recorded()
	if accepted == 0 {
		t.Error("testing the second instance never dialed it")
	}
}

func TestTestConnectionStandardOnTLSOnlyInstance(t *testing.T) {
	t.Parallel()

	pki := newTestPKI(t)
	server := newFakeRedis(t, pki)
	handler := NewHandler(nil)

	t.Cleanup(func() { _ = handler.Close() })

	_, err := handler.TestConnection(context.Background(), valkeyTLSCreds(pki, server), common.ConnectionOptions{UseTLS: false})
	if err == nil {
		t.Fatal("expected Standard to fail on a TLS-only instance")
	}

	if strings.Contains(err.Error(), testPassword) || strings.Contains(err.Error(), "BEGIN CERTIFICATE") {
		t.Errorf("error leaks a credential: %v", err)
	}

	_, accepted := server.recorded()
	if accepted != 0 {
		t.Errorf("Standard dialed the instance %d times", accepted)
	}
}

func TestClientOptionsPinsRESP2(t *testing.T) {
	t.Parallel()

	pki := newTestPKI(t)

	cases := map[string]struct {
		creds  *Credentials
		useTLS bool
	}{
		"plain text": {
			creds: &Credentials{Host: "valkey.example.com", Port: 6379, Password: testPassword},
		},
		"tls port": {
			creds:  &Credentials{Host: "127.0.0.1", TLSPort: 6380, Password: testPassword, CACertificate: pki.caPEM},
			useTLS: true,
		},
		"tls uri asking for resp3": {
			creds: &Credentials{
				Host:          "127.0.0.1",
				TLSURI:        "rediss://127.0.0.1:6380?protocol=3",
				Password:      testPassword,
				CACertificate: pki.caPEM,
			},
			useTLS: true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			opts, err := ClientOptions(tc.creds, tc.useTLS)
			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}

			if opts.Protocol != 2 {
				t.Errorf("Protocol = %d, expected 2", opts.Protocol)
			}

			if !opts.DisableIdentity {
				t.Errorf("CLIENT SETINFO would be sent on connect")
			}
		})
	}
}

// silentServer accepts connections and never answers, the way a stopped or
// wedged node does.
func silentServer(t *testing.T) int {
	t.Helper()

	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	done := make(chan struct{})

	t.Cleanup(func() {
		close(done)

		_ = listener.Close()
	})

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}

			go func() {
				<-done

				_ = conn.Close()
			}()
		}
	}()

	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatal("listener has no TCP address")
	}

	return addr.Port
}

func TestClientGivesUpAtTheCallersDeadline(t *testing.T) {
	t.Parallel()

	port := silentServer(t)

	opts, err := ClientOptions(&Credentials{Host: "127.0.0.1", Port: port, Password: testPassword}, false)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if opts.ReadTimeout < 10*time.Second {
		t.Fatalf("ReadTimeout = %v, the test needs it well above the deadline", opts.ReadTimeout)
	}

	client := goredis.NewClient(opts)

	t.Cleanup(func() { _ = client.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	started := time.Now()

	err = client.Ping(ctx).Err()
	if err == nil {
		t.Fatal("Ping to a silent server succeeded")
	}

	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Errorf("Ping took %v, expected it to stop near the 300ms deadline", elapsed)
	}
}

func TestClientOptionsHoldV8Dialing(t *testing.T) {
	t.Parallel()

	pki := newTestPKI(t)

	for name, creds := range map[string]*Credentials{
		"tls uri": {Host: "127.0.0.1", TLSURI: "rediss://127.0.0.1:6380", CACertificate: pki.caPEM},
		"tls":     {Host: "127.0.0.1", TLSPort: 6380, CACertificate: pki.caPEM},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			opts, err := ClientOptions(creds, true)
			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}

			if !opts.ContextTimeoutEnabled {
				t.Error("socket I/O would ignore the caller's context deadline")
			}

			if opts.DialerRetries != 1 {
				t.Errorf("DialerRetries = %d, expected 1", opts.DialerRetries)
			}
		})
	}
}
