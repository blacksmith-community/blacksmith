package vault_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"blacksmith/internal/vault"
)

// tlsVault is a Vault health endpoint served over TLS with a certificate
// that a private test CA signed.
type tlsVault struct {
	server *httptest.Server
	caPEM  []byte
}

func newTLSVault(t *testing.T) *tlsVault {
	t.Helper()

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test vault ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}

	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}

	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	leafTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}

	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}

	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"initialized":true,"sealed":false,"standby":false}`))
	}))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{leafDER}, PrivateKey: leafKey}}, MinVersion: tls.VersionTLS12}
	server.StartTLS()
	t.Cleanup(server.Close)

	return &tlsVault{server: server, caPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})}
}

func (tv *tlsVault) writeCA(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "ca.pem")

	err := os.WriteFile(path, tv.caPEM, 0o600)
	if err != nil {
		t.Fatal(err)
	}

	return path
}

// A Vault behind a certificate that a private CA signed is refused until
// vault.cacert names that CA, and the CA may be a file path or PEM text.
func TestVaultClientTrustsTheConfiguredCACert(t *testing.T) {
	t.Setenv("VAULT_CACERT", "")
	t.Setenv("VAULT_CAPATH", "")

	remote := newTLSVault(t)

	check := func(caCert string) error {
		client := vault.New(remote.server.URL, "", false)

		if caCert != "" {
			err := client.SetCACert(caCert)
			if err != nil {
				t.Fatalf("SetCACert: %v", err)
			}
		}

		_, err := client.HealthCheck()
		if err != nil {
			return fmt.Errorf("health check: %w", err)
		}

		return nil
	}

	err := check("")
	if err == nil {
		t.Fatal("a Vault signed by an unknown CA answered without vault.cacert")
	}

	err = check(remote.writeCA(t))
	if err != nil {
		t.Errorf("with the CA as a file path, the health check failed: %v", err)
	}

	err = check(string(remote.caPEM))
	if err != nil {
		t.Errorf("with the CA as PEM text, the health check failed: %v", err)
	}
}

// skip_ssl_validation keeps skipping verification whether or not a CA is set.
func TestVaultClientInsecureStillSkipsVerification(t *testing.T) {
	t.Setenv("VAULT_CACERT", "")

	remote := newTLSVault(t)

	_, err := vault.New(remote.server.URL, "", true).HealthCheck()
	if err != nil {
		t.Errorf("an insecure client refused the certificate: %v", err)
	}
}

// A CA that cannot be read, or holds no certificate, is an error, and never
// a silent fallback to the system roots.
func TestVaultClientRejectsAnUnusableCACert(t *testing.T) {
	t.Parallel()

	empty := filepath.Join(t.TempDir(), "empty.pem")

	err := os.WriteFile(empty, []byte("not a certificate\n"), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	for name, value := range map[string]string{
		"a missing file":        filepath.Join(t.TempDir(), "missing.pem"),
		"a file with no cert":   empty,
		"PEM with no cert":      "-----BEGIN CERTIFICATE-----\nbm90IGEgY2VydA==\n-----END CERTIFICATE-----\n",
		"a body without header": "TUlJQmtUQ0IrNkFEQWdFQ0FoUUJhc2U2NGJvZHlsb3N0aGVhZGVy\nc2Vjb25kbGluZW9mdGhlYm9keQ==\n",
		"a short fragment":      "TUlJQmtUQ0IrNkFEQWdFQ0FoUUJhc2U2NGJvZHlsb3N0aGVhZGVy",
	} {
		err := vault.New("https://127.0.0.1:1", "", false).SetCACert(value)
		if err == nil {
			t.Errorf("%s was accepted as vault.cacert", name)

			continue
		}

		if strings.Contains(err.Error(), "BEGIN") {
			t.Errorf("the error for %s repeats the certificate text: %v", name, err)
		}

		// No part of the value may appear in the error, because a
		// short value may be a piece of a certificate as easily as a
		// path.
		for _, line := range strings.Fields(value) {
			for start := 0; start+16 <= len(line); start += 8 {
				if strings.Contains(err.Error(), line[start:start+16]) {
					t.Errorf("the error for %s repeats part of the value: %v", name, err)

					break
				}
			}
		}
	}
}

// The readiness check trusts the configured CA too, so a broker in front of
// a private-CA Vault becomes ready.
func TestWaitForVaultReadyTrustsTheConfiguredCACert(t *testing.T) {
	t.Setenv("VAULT_CACERT", "")
	t.Setenv("VAULT_CAPATH", "")

	remote := newTLSVault(t)
	client := vault.New(remote.server.URL, "", false)

	err := client.SetCACert(string(remote.caPEM))
	if err != nil {
		t.Fatalf("SetCACert: %v", err)
	}

	err = client.WaitForVaultReady()
	if err != nil {
		t.Errorf("WaitForVaultReady: %v", err)
	}
}

// A path that a YAML block scalar leaves with a trailing newline, or that
// carries stray spaces, still names the file.
func TestSetCACertTrimsWhitespaceAroundAPath(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "ca.pem")

	err := os.WriteFile(path, newTLSVault(t).caPEM, 0o600)
	if err != nil {
		t.Fatal(err)
	}

	for _, value := range []string{path + "\n", "  " + path, "\t" + path + " \n"} {
		err := vault.New("https://127.0.0.1:1", "", false).SetCACert(value)
		if err != nil {
			t.Errorf("SetCACert(%q) = %v, want the path to be found", value, err)
		}
	}
}
