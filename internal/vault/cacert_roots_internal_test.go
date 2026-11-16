package vault

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"
)

// testCAPEM returns a self-signed CA certificate as PEM text.
func testCAPEM(t *testing.T) []byte {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test vault ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}

	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// The configured CA is the only root the client trusts, so a public CA
// that issues a certificate for the Vault host name is not trusted.
func TestConfiguredCACertReplacesTheSystemRoots(t *testing.T) {
	t.Parallel()

	caPEM := testCAPEM(t)

	client := New("https://127.0.0.1:1", "", false)

	err := client.SetCACert(string(caPEM))
	if err != nil {
		t.Fatalf("SetCACert: %v", err)
	}

	block, _ := pem.Decode(caPEM)

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}

	want := x509.NewCertPool()
	want.AddCert(cert)

	if !client.roots.Equal(want) {
		t.Error("the client trusts more than the configured CA")
	}
}
