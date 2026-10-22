package certinventory

import (
	"context"
	"crypto/elliptic"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// serverLeaf makes a self-signed server certificate the server can present.
func serverLeaf(t *testing.T, cn string) (*x509.Certificate, tls.Certificate) {
	t.Helper()

	key := ecKey(t, elliptic.P256())
	cert, _ := makeCert(t, certSpec{
		cn: cn, subject: &key.PublicKey, selfKey: key, serial: 11,
		dns: []string{cn},
	})

	return cert, tls.Certificate{Certificate: [][]byte{cert.Raw}, PrivateKey: key}
}

func TestProberCapturesLeafWhenClientCertRequired(t *testing.T) {
	leaf, pair := serverLeaf(t, "ws-redis-cluster.bosh")

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.TLS = &tls.Config{
		Certificates: []tls.Certificate{pair},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    x509.NewCertPool(),
	}
	srv.StartTLS()
	defer srv.Close()

	got, chain, err := Prober{Timeout: 2 * time.Second}.Leaf(context.Background(), srv.Listener.Addr().String(), "ws-redis-cluster.bosh")
	if err != nil {
		t.Fatalf("Leaf: %v", err)
	}

	if Fingerprint(got) != Fingerprint(leaf) {
		t.Errorf("fingerprint %s, want %s", Fingerprint(got), Fingerprint(leaf))
	}

	if len(chain) != 0 {
		t.Errorf("chain has %d extra certificates, want 0", len(chain))
	}
}

func TestProberTimeout(t *testing.T) {
	ln, err := listenLocal()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	var (
		mu    sync.Mutex
		conns []net.Conn
	)

	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}

			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
		}
	}()

	defer func() {
		mu.Lock()
		defer mu.Unlock()

		for _, c := range conns {
			_ = c.Close()
		}
	}()

	start := time.Now()

	leaf, _, err := Prober{Timeout: 200 * time.Millisecond}.Leaf(context.Background(), ln.Addr().String(), "x")
	if err == nil || leaf != nil {
		t.Fatalf("want an error and no leaf, got leaf=%v err=%v", leaf, err)
	}

	if time.Since(start) > time.Second {
		t.Errorf("took %s, want under 1s", time.Since(start))
	}

	var ne net.Error

	if !errors.Is(err, context.DeadlineExceeded) && (!errors.As(err, &ne) || !ne.Timeout()) {
		t.Errorf("error is not a timeout: %v", err)
	}
}

func TestProberSendsServerName(t *testing.T) {
	_, pair := serverLeaf(t, "ws-redis-cluster.bosh")

	names := make(chan string, 4)

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.TLS = &tls.Config{
		Certificates: []tls.Certificate{pair},
		GetConfigForClient: func(h *tls.ClientHelloInfo) (*tls.Config, error) {
			names <- h.ServerName

			return nil, nil //nolint:nilnil // A nil config tells the server to keep its own.
		},
	}
	srv.StartTLS()
	defer srv.Close()

	if _, _, err := (Prober{}).Leaf(context.Background(), srv.Listener.Addr().String(), "ws-redis-cluster.bosh"); err != nil {
		t.Fatalf("Leaf: %v", err)
	}

	select {
	case got := <-names:
		if got != "ws-redis-cluster.bosh" {
			t.Errorf("server name = %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("the server never saw a hello")
	}
}

func TestProberFailsWhenNothingSpeaksTLS(t *testing.T) {
	ln, err := listenLocal()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	go func() {
		c, err := ln.Accept()
		if err == nil {
			_, _ = c.Write([]byte("-ERR not tls\r\n"))
			_ = c.Close()
		}
	}()

	leaf, chain, err := Prober{Timeout: time.Second}.Leaf(context.Background(), ln.Addr().String(), "x")
	if err == nil {
		t.Fatalf("want an error, got leaf=%v chain=%v", leaf, chain)
	}
}

func TestProberFailsWhenRefused(t *testing.T) {
	ln, err := listenLocal()
	if err != nil {
		t.Fatal(err)
	}

	addr := ln.Addr().String()
	_ = ln.Close()

	if _, _, err := (Prober{Timeout: time.Second}).Leaf(context.Background(), addr, "x"); err == nil {
		t.Fatal("want an error")
	}
}

func TestVerifyChain(t *testing.T) {
	caKey := ecKey(t, elliptic.P256())
	ca, _ := makeCert(t, certSpec{
		cn: "root", isCA: true, subject: &caKey.PublicKey, selfKey: caKey, serial: 1,
		keyUsage: x509.KeyUsageCertSign,
	})

	leafKey := ecKey(t, elliptic.P256())
	leaf, _ := makeCert(t, certSpec{
		cn: "leaf", subject: &leafKey.PublicKey, parent: ca, parentK: caKey, serial: 2,
		ext: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	})

	otherKey := ecKey(t, elliptic.P256())
	other, _ := makeCert(t, certSpec{
		cn: "other", isCA: true, subject: &otherKey.PublicKey, selfKey: otherKey, serial: 3,
		keyUsage: x509.KeyUsageCertSign,
	})

	roots := x509.NewCertPool()
	roots.AddCert(ca)

	wrong := x509.NewCertPool()
	wrong.AddCert(other)

	now := time.Now()

	if err := VerifyChain(leaf, nil, roots, now); err != nil {
		t.Errorf("good chain: %v", err)
	}

	if err := VerifyChain(leaf, nil, wrong, now); err == nil {
		t.Error("a chain to the wrong root should fail")
	}

	if err := VerifyChain(leaf, nil, roots, now.Add(10*24*time.Hour)); err == nil {
		t.Error("an expired leaf should fail")
	}

	if err := VerifyChain(nil, nil, roots, now); err == nil {
		t.Error("a nil leaf should fail")
	}
}

// listenLocal opens a TCP listener on a free loopback port.
func listenLocal() (net.Listener, error) {
	return (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
}
