package certificates_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"blacksmith/internal/handlers/certificates"
	pkgCertificates "blacksmith/pkg/certificates"
)

const (
	pemCertificateType = "CERTIFICATE"
	vaultCAName        = "Vault CA Certificate"
)

// leafPEM returns a certificate that is not a CA, as PEM text.
func leafPEM(t *testing.T, commonName string) string {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}

	return string(pem.EncodeToMemory(&pem.Block{Type: pemCertificateType, Bytes: der}))
}

func blacksmithResponse(t *testing.T, cfg *testConfig) pkgCertificates.CertificateResponse {
	t.Helper()

	api := certificates.NewHandler(cfg, NewTestLogger("test", "INFO"), nil)

	recorder := httptest.NewRecorder()
	api.HandleBlacksmithCertificates(recorder, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/b/internal/certificates/blacksmith", nil))

	var response pkgCertificates.CertificateResponse

	err := json.Unmarshal(recorder.Body.Bytes(), &response)
	if err != nil {
		t.Fatalf("parse response: %v", err)
	}

	return response
}

// The Vault client trusts every certificate in the bundle that parses and
// skips every block that doesn't. The route does the same, so a bad block
// no longer hides the certificates after it. It counts the blocks it
// skipped and marks a certificate that is not a CA, since the client
// trusts it all the same.
func TestVaultCABundleSkipsBadBlocksAndMarksNonCAs(t *testing.T) {
	t.Parallel()

	garbage := string(pem.EncodeToMemory(&pem.Block{Type: pemCertificateType, Bytes: []byte("not a der certificate")}))
	privateKey := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("secret")}))
	bundle := selfSignedCAPEM(t, "First CA") + garbage + leafPEM(t, "A Leaf") + privateKey + selfSignedCAPEM(t, "Last CA")

	response := blacksmithResponse(t, &testConfig{vaultCACert: bundle})

	type listed struct {
		name, common string
		notCA        bool
	}

	got := make([]listed, 0, len(response.Data.Certificates))

	for _, item := range response.Data.Certificates {
		got = append(got, listed{item.Name, item.Details.Subject.CommonName, item.NotCA})
	}

	want := []listed{
		{vaultCAName, "First CA", false},
		{vaultCAName + " 2", "A Leaf", true},
		{vaultCAName + " 3", "Last CA", false},
	}

	if len(got) != len(want) {
		t.Fatalf("listed %+v, want %+v", got, want)
	}

	for index := range want {
		if got[index] != want[index] {
			t.Errorf("item %d = %+v, want %+v", index, got[index], want[index])
		}
	}

	if response.Data.Metadata.SkippedBlocks != 2 {
		t.Errorf("SkippedBlocks = %d, want 2", response.Data.Metadata.SkippedBlocks)
	}

	if response.Data.Metadata.Count != 3 {
		t.Errorf("Count = %d, want 3", response.Data.Metadata.Count)
	}
}

// A bundle that is all good reports no skipped blocks and marks no
// certificate.
func TestVaultCABundleWithNoBadBlocks(t *testing.T) {
	t.Parallel()

	response := blacksmithResponse(t, &testConfig{vaultCACert: selfSignedCAPEM(t, "Only CA")})

	if response.Data.Metadata.SkippedBlocks != 0 || len(response.Data.Certificates) != 1 || response.Data.Certificates[0].NotCA {
		t.Errorf("response = %+v, want one unmarked certificate and no skipped blocks", response.Data)
	}
}
