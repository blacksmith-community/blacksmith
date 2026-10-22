package certinventory

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"
)

type certSpec struct {
	cn       string
	isCA     bool
	ext      []x509.ExtKeyUsage
	dns      []string
	subject  crypto.PublicKey // key in the certificate
	parent   *x509.Certificate
	parentK  crypto.Signer // signer; self-signed when parent is nil
	selfKey  crypto.Signer // signer for a self-signed certificate
	serial   int64
	sigAlgo  x509.SignatureAlgorithm
	keyUsage x509.KeyUsage
}

func ecKey(t *testing.T, c elliptic.Curve) *ecdsa.PrivateKey {
	t.Helper()

	k, err := ecdsa.GenerateKey(c, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	return k
}

func makeCert(t *testing.T, s certSpec) (*x509.Certificate, []byte) {
	t.Helper()

	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(s.serial + 1),
		Subject:               pkix.Name{CommonName: s.cn},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		ExtKeyUsage:           s.ext,
		DNSNames:              s.dns,
		BasicConstraintsValid: s.isCA,
		IsCA:                  s.isCA,
		KeyUsage:              s.keyUsage,
		SubjectKeyId:          []byte{0xab, 0xcd, byte(s.serial)}, //nolint:gosec // Test serials are small.
		SignatureAlgorithm:    s.sigAlgo,
	}

	parent, signer := s.parent, s.parentK
	if parent == nil {
		parent, signer = tmpl, s.selfKey
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, s.subject, signer)
	if err != nil {
		t.Fatal(err)
	}

	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}

	return cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func TestParsePEMCertificatesChainOrder(t *testing.T) {
	t.Parallel()

	caKey := ecKey(t, elliptic.P256())
	ca, caPEM := makeCert(t, certSpec{cn: "root", isCA: true, subject: caKey.Public(), selfKey: caKey, serial: 1})
	leafKey := ecKey(t, elliptic.P256())
	_, leafPEM := makeCert(t, certSpec{cn: "leaf", subject: leafKey.Public(), parent: ca, parentK: caKey, serial: 2})
	keyPEM := []byte("-----BEGIN RSA PRIVATE KEY-----\nc2VjcmV0\n-----END RSA PRIVATE KEY-----\n")

	input := append(append(append([]byte{}, leafPEM...), caPEM...), keyPEM...)

	certs, err := ParsePEMCertificates(input)
	if err != nil {
		t.Fatal(err)
	}

	if len(certs) != 2 || certs[0].Subject.CommonName != "leaf" || certs[1].Subject.CommonName != "root" {
		t.Fatalf("unexpected result: %d certificates", len(certs))
	}

	if _, err := ParsePEMCertificates(keyPEM); err == nil {
		t.Fatal("expected an error when no certificate parses")
	}

	if _, err := ParsePEMCertificates([]byte("not pem")); err == nil {
		t.Fatal("expected an error for non-PEM input")
	}
}

func TestKindOf(t *testing.T) {
	t.Parallel()

	rootKey := ecKey(t, elliptic.P256())
	root, _ := makeCert(t, certSpec{cn: "root", isCA: true, subject: rootKey.Public(), selfKey: rootKey, serial: 1})
	interKey := ecKey(t, elliptic.P256())
	inter, _ := makeCert(t, certSpec{cn: "inter", isCA: true, subject: interKey.Public(), parent: root, parentK: rootKey, serial: 2})
	leafKey := ecKey(t, elliptic.P256())
	leaf, _ := makeCert(t, certSpec{cn: "leaf", subject: leafKey.Public(), parent: inter, parentK: interKey, serial: 3})

	if KindOf(root) != KindCA || KindOf(inter) != KindIntermediate || KindOf(leaf) != KindLeaf {
		t.Fatalf("kinds = %s %s %s", KindOf(root), KindOf(inter), KindOf(leaf))
	}
}

func TestFactsExtKeyUsageNames(t *testing.T) {
	t.Parallel()

	k := ecKey(t, elliptic.P256())
	c, _ := makeCert(t, certSpec{
		cn: "svc", subject: k.Public(), selfKey: k, serial: 10,
		ext: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		dns: []string{"a.example", "b.example"},
	})

	f := Facts(c)

	if strings.Join(f.ExtKeyUsages, ",") != "serverAuth,clientAuth" {
		t.Errorf("ext key usages = %v", f.ExtKeyUsages)
	}

	if f.CommonName != "svc" || f.KeyAlgorithm != "ECDSA" || f.KeyBits != 256 {
		t.Errorf("facts = %+v", f)
	}

	if strings.Join(f.DNSNames, ",") != "a.example,b.example" {
		t.Errorf("dns = %v", f.DNSNames)
	}

	if f.Serial != "b" || f.SubjectKeyID != "abcd0a" {
		t.Errorf("serial %q, ski %q", f.Serial, f.SubjectKeyID)
	}

	if !f.NotAfter.Equal(c.NotAfter) || !f.NotBefore.Equal(c.NotBefore) || f.Issuer == "" {
		t.Errorf("dates or issuer wrong: %+v", f)
	}
}

func TestWeaknessFindings(t *testing.T) {
	t.Parallel()

	signer := ecKey(t, elliptic.P256())
	ca, _ := makeCert(t, certSpec{cn: "ca", isCA: true, subject: signer.Public(), selfKey: signer, serial: 1})

	rsa1024, err := rsa.GenerateKey(rand.Reader, 1024) //nolint:gosec // A deliberately weak key exercises the weak-key finding.
	if err != nil {
		t.Fatal(err)
	}

	rsa2048, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	weakRSA, _ := makeCert(t, certSpec{cn: "rsa1024", subject: rsa1024.Public(), parent: ca, parentK: signer, serial: 2})
	weakEC, _ := makeCert(t, certSpec{cn: "p224", subject: ecKey(t, elliptic.P224()).Public(), parent: ca, parentK: signer, serial: 3})
	good, _ := makeCert(t, certSpec{cn: "rsa2048", subject: rsa2048.Public(), parent: ca, parentK: signer, serial: 4})

	pemBytes, err := os.ReadFile("testdata/sha1.pem") // openssl req -x509 -sha1 -newkey rsa:2048 -nodes -days 3650 -subj /CN=sha1-fixture
	if err != nil {
		t.Fatal(err)
	}

	sha1Certs, err := ParsePEMCertificates(pemBytes)
	if err != nil {
		t.Fatal(err)
	}

	rows := []struct {
		name string
		c    *x509.Certificate
		want []string
	}{
		{"rsa 1024", weakRSA, []string{"weak_key"}},
		{"p224", weakEC, []string{"weak_key"}},
		{"rsa 2048 sha256", good, nil},
		{"sha1 fixture", sha1Certs[0], []string{"weak_signature"}},
	}

	for _, row := range rows {
		got := WeaknessFindings(row.c)

		var codes []string

		for _, f := range got {
			codes = append(codes, f.Code)

			if f.Severity != SeverityWarning || f.Message == "" {
				t.Errorf("%s: finding %+v", row.name, f)
			}
		}

		if strings.Join(codes, ",") != strings.Join(row.want, ",") {
			t.Errorf("%s: codes %v, want %v", row.name, codes, row.want)
		}
	}
}

func TestFingerprintStable(t *testing.T) {
	t.Parallel()

	k := ecKey(t, elliptic.P256())
	c, _ := makeCert(t, certSpec{cn: "fp", subject: k.Public(), selfKey: k, serial: 5})

	sum := sha256.Sum256(c.Raw)
	if got, want := Fingerprint(c), hex.EncodeToString(sum[:]); got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}
