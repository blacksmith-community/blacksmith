package credhub_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	otherDirector    = "other-bosh"
	findOp           = "find"
	deleteOp         = "delete"
	uaaUnauthorized  = "unauthorized"
	credhubURLField  = "credhub.url" //nolint:gosec // a config key name, not a credential
	insufficientPerm = "insufficient permissions"
)

const (
	secretMarker = "client-secret-marker-do-not-log" //nolint:gosec // fake secret the tests prove stays out of errors and logs
	tokenMarker  = "bearer-token-marker-do-not-log"  //nolint:gosec // fake token the tests prove stays out of errors and logs
)

// serverCA returns the PEM of a TLS test server's certificate, which the
// clients trust as their only root.
func serverCA(server *httptest.Server) string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}))
}

// unrelatedCA returns the PEM of a freshly generated CA that signed nothing
// the test servers present. Every httptest server shares one certificate, so
// a second server cannot stand in for a wrong CA.
func unrelatedCA(t *testing.T) string {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber:          big.NewInt(7),
		Subject:               pkix.Name{CommonName: "unrelated-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}

	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

// fakeClock is a clock the tests move by hand.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.now = c.now.Add(d)
}

func assertNoMarkers(t *testing.T, text string) {
	t.Helper()

	for _, marker := range []string{secretMarker, tokenMarker} {
		if strings.Contains(text, marker) {
			t.Errorf("text carries a credential marker %q: %q", marker, text)
		}
	}
}
