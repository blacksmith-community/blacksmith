package credhub_test

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"blacksmith/internal/credhub"
)

const (
	keyPairVersion = "6f1c2b9e-3d4a-4b5c-8d7e-9f0a1b2c3d4e"
	certificatePEM = "CERTIFICATE"
)

// base64Run finds a run of base64 text long enough to be part of a key or
// a certificate.
var base64Run = regexp.MustCompile(`[A-Za-z0-9+/=]{20,}`)

// throwawayPair returns the PEM of a fresh self-signed certificate and of
// its EC private key, made for one test.
func throwawayPair(t *testing.T) ([]byte, []byte) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(42),
		Subject:      pkix.Name{CommonName: "redis.service.internal"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}

	return pem.EncodeToMemory(&pem.Block{Type: certificatePEM, Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}

// jsonString returns s as a JSON string literal the way CredHub writes
// it, with every slash escaped as \/.
func jsonString(t *testing.T, s []byte) string {
	t.Helper()

	encoded, err := json.Marshal(string(s))
	if err != nil {
		t.Fatal(err)
	}

	return strings.ReplaceAll(string(encoded), "/", `\/`)
}

// keyPairAnswer returns a CredHub data answer for one certificate
// version, with the private key ahead of the certificate and other
// members around them.
func keyPairAnswer(t *testing.T, cert, key []byte) string {
	t.Helper()

	return `{"type":"certificate","version_created_at":"2026-10-22T10:00:00Z","id":"` + keyPairVersion +
		`","name":"\/d\/dep\/redis_crt","metadata":{"labels":["a",1,true,null,{"x":[]}]},` +
		`"value":{"ca":` + jsonString(t, cert) + `,"private_key":` + jsonString(t, key) +
		`,"certificate":` + jsonString(t, cert) + `,"transitional":false}}`
}

func TestCertificateKeyPairDecodesEscapedPEM(t *testing.T) {
	t.Parallel()

	cert, key := throwawayPair(t)
	fake := newReadServer(t, answer(keyPairAnswer(t, cert, key)))

	pair, err := fake.client(t).CertificateKeyPair(t.Context(), keyPairVersion)
	if err != nil {
		t.Fatalf("CertificateKeyPair: %v", err)
	}

	if pair.VersionID != keyPairVersion {
		t.Errorf("VersionID = %q, want %q", pair.VersionID, keyPairVersion)
	}

	if !bytes.Equal(pair.Certificate, cert) {
		t.Error("the certificate differs from the original PEM")
	}

	if !bytes.Equal(pair.PrivateKey, key) {
		t.Error("the private key differs from the original PEM")
	}

	_, err = tls.X509KeyPair(pair.Certificate, pair.PrivateKey)
	if err != nil {
		t.Errorf("tls.X509KeyPair refused the decoded pair: %v", err)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()

	if len(fake.paths) != 1 || fake.paths[0] != "/api/v1/data/"+keyPairVersion {
		t.Errorf("requests = %v, want one GET of /api/v1/data/%s", fake.paths, keyPairVersion)
	}
}

func TestCertificateKeyPairRefusesEmptyVersion(t *testing.T) {
	t.Parallel()

	fake := newReadServer(t, answer("{}"))

	_, err := fake.client(t).CertificateKeyPair(t.Context(), "")
	if !errors.Is(err, credhub.ErrEmptyVersionID) {
		t.Fatalf("err = %v, want ErrEmptyVersionID", err)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()

	if len(fake.paths) != 0 {
		t.Errorf("an empty version ID still sent %v", fake.paths)
	}
}

func TestCertificateKeyPairRedactsInFormatting(t *testing.T) {
	t.Parallel()

	cert, key := throwawayPair(t)
	pair := &credhub.KeyPair{VersionID: keyPairVersion, Certificate: cert, PrivateKey: key}

	texts := []string{
		fmt.Sprintf("%v %+v %#v", pair, pair, pair),
		fmt.Sprintf("%v %+v %#v %s %q %x %d", *pair, *pair, *pair, *pair, *pair, *pair, *pair),
		fmt.Sprint(pair, *pair),
		pair.String(),
		pair.GoString(),
	}

	for _, text := range texts {
		if strings.Contains(text, "PRIVATE") || strings.Contains(text, certificatePEM) || base64Run.MatchString(text) {
			t.Errorf("formatting shows key or certificate text: %q", text)
		}

		if !strings.Contains(text, "KeyPair{<redacted>}") {
			t.Errorf("formatting is not redacted: %q", text)
		}
	}

	encoded, err := json.Marshal(pair)
	if err == nil || bytes.Contains(encoded, []byte("PRIVATE")) {
		t.Errorf("json.Marshal of a key pair = %q, %v, want an error and no content", encoded, err)
	}
}

func TestCertificateKeyPairZero(t *testing.T) {
	t.Parallel()

	cert, key := throwawayPair(t)
	pair := &credhub.KeyPair{VersionID: keyPairVersion, Certificate: cert, PrivateKey: key}

	pair.Zero()

	for name, buf := range map[string][]byte{"certificate": pair.Certificate, "private key": pair.PrivateKey} {
		if len(buf) == 0 {
			t.Errorf("Zero emptied the %s slice instead of overwriting it", name)
		}

		for index, b := range buf {
			if b != 0 {
				t.Errorf("byte %d of the %s is %#x after Zero", index, name, b)

				break
			}
		}
	}

	var empty *credhub.KeyPair

	empty.Zero()
}

// TestCertificateKeyPairClearsAnswer shows that the answer buffer is all
// zeros once the call returns, after a good answer and after one that
// cannot be decoded.
func TestCertificateKeyPairClearsAnswer(t *testing.T) {
	t.Parallel()

	cert, key := throwawayPair(t)

	for name, body := range map[string]string{
		"good":      keyPairAnswer(t, cert, key),
		"truncated": strings.TrimSuffix(keyPairAnswer(t, cert, key), `,"transitional":false}}`)[:600],
	} {
		fake := newReadServer(t, answer(body))
		client := fake.client(t)

		var seen []byte

		credhub.SetKeyPairBufferHook(client, func(buf []byte) { seen = buf })

		pair, err := client.CertificateKeyPair(t.Context(), keyPairVersion)
		if name == "good" && err != nil {
			t.Fatalf("%s: %v", name, err)
		}

		if name == "truncated" && (err == nil || pair != nil) {
			t.Fatalf("%s: pair = %v, err = %v, want only an error", name, pair, err)
		}

		if len(seen) != len(body) {
			t.Fatalf("%s: the hook saw %d bytes, want the %d-byte answer", name, len(seen), len(body))
		}

		if !bytes.Equal(seen, make([]byte, len(seen))) {
			t.Errorf("%s: the answer buffer still holds data after the call", name)
		}
	}
}

func TestDecodeKeyPairRejects(t *testing.T) {
	t.Parallel()

	cert, key := throwawayPair(t)
	good := keyPairAnswer(t, cert, key)
	keyStart := strings.Index(good, `"private_key":`) + len(`"private_key":`) + 40
	bigValue := `"` + strings.Repeat("A", 17<<10) + `"`

	// The bodies hold text shaped like a key, never a real one.
	tests := map[string]string{ //nolint:gosec // fake key-shaped text that the test proves stays out of errors
		"truncated inside private_key": good[:keyStart],
		"non-ASCII escape":             `{"value":{"certificate":"-----BEGIN\u00e9","private_key":"k"}}`,
		"surrogate escape":             `{"value":{"certificate":"c","private_key":"\ud83d\ude00"}}`,
		"backspace escape":             `{"value":{"certificate":"c","private_key":"MHc\bAA"}}`,
		"raw newline in a string":      "{\"value\":{\"certificate\":\"c\",\"private_key\":\"-----BEGIN EC PRIVATE KEY-----\nMHcCAQEEIBase64Base64Base64Base64\"}}",
		"raw newline in a skipped one": "{\"value\":{\"ca\":\"a\nb\",\"certificate\":\"c\",\"private_key\":\"k\"}}",
		"duplicate private_key":        `{"value":{"private_key":"MHcCAQEEIBase64Base64Base64Base64","certificate":"c","private_key":"k"}}`,
		"duplicate certificate":        `{"value":{"certificate":"c","private_key":"k","certificate":"d"}}`,
		"duplicate across values":      `{"value":{"certificate":"c","private_key":"k"},"value":{"certificate":"d","private_key":"e"}}`,
		"repeated value":               `{"value":{"id":"x"},"value":{"certificate":"c","private_key":"k"}}`,
		"repeated non-object value":    `{"value":{"certificate":"c","private_key":"k"},"value":null}`,
		"17 KiB value":                 `{"value":{"certificate":"c","private_key":` + bigValue + `}}`,
		"non-object value":             `{"value":"-----BEGIN EC PRIVATE KEY-----MHcCAQEEIBase64Base64Base64Base64"}`,
		"non-object answer":            `["-----BEGIN EC PRIVATE KEY-----MHcCAQEEIBase64Base64Base64Base64"]`,
		"non-string private_key":       `{"value":{"certificate":"c","private_key":{"pem":"MHcCAQEEIBase64Base64Base64Base64"}}}`,
		"no private_key":               `{"value":{"certificate":"c"}}`,
		"no certificate":               `{"value":{"private_key":"MHcCAQEEIBase64Base64Base64Base64"}}`,
		"empty private_key":            `{"value":{"certificate":"c","private_key":""}}`,
		"no value":                     `{"id":"x"}`,
		"trailing data":                `{"value":{"certificate":"c","private_key":"k"}} {"MHcCAQEEIBase64Base64Base64Base64":1}`,
		"missing colon":                `{"value"{"certificate":"c","private_key":"k"}}`,
		"bad literal":                  `{"value":{"certificate":"c","private_key":"k","x":nul}}`,
		"bad number":                   `{"value":{"certificate":"c","private_key":"k","x":-}}`,
		"trailing comma":               `{"value":{"certificate":"c","private_key":"k",}}`,
		"deep nesting":                 `{"x":` + strings.Repeat("[", 100) + strings.Repeat("]", 100) + `,"value":{"certificate":"c","private_key":"k"}}`,
		"empty":                        ``,
	}

	for name, body := range tests {
		gotCert, gotKey, err := credhub.DecodeKeyPair([]byte(body))
		if err == nil {
			t.Errorf("%s: decoded with no error", name)

			continue
		}

		if gotCert != nil || gotKey != nil {
			t.Errorf("%s: returned content with its error", name)
		}

		if !errors.Is(err, credhub.ErrBadKeyPairAnswer) {
			t.Errorf("%s: error %v is not ErrBadKeyPairAnswer", name, err)
		}

		text := err.Error()
		if strings.Contains(text, "PRIVATE") || strings.Contains(text, "BEGIN") || base64Run.MatchString(text) {
			t.Errorf("%s: error quotes the input: %q", name, text)
		}

		if !strings.Contains(text, "key pair answer") {
			t.Errorf("%s: error %q does not name the key pair answer", name, text)
		}
	}
}

func TestDecodeKeyPairAcceptsASCIIEscapes(t *testing.T) {
	t.Parallel()

	body := `{"value":{"certificate":"a\nb\rc\td\"e\\f\/g\u0041\u007e","private_key":"\u002d-k"}}`

	cert, key, err := credhub.DecodeKeyPair([]byte(body))
	if err != nil {
		t.Fatalf("DecodeKeyPair: %v", err)
	}

	if string(cert) != "a\nb\rc\td\"e\\f/gA~" {
		t.Errorf("certificate = %q", cert)
	}

	if string(key) != "--k" {
		t.Errorf("private key = %q", key)
	}
}

func TestCertificateKeyPairErrorsHideBody(t *testing.T) {
	t.Parallel()

	_, key := throwawayPair(t)

	failing := newReadServer(t, func(*http.Request) (int, string) {
		return http.StatusInternalServerError, `{"error":` + jsonString(t, key) + `}`
	})

	_, err := failing.client(t).CertificateKeyPair(t.Context(), keyPairVersion)

	var apiErr *credhub.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusInternalServerError || apiErr.Message != "" {
		t.Fatalf("err = %v, want an APIError with status 500 and no message", err)
	}

	assertNoContent(t, err)

	large := newReadServer(t, answer(`{"value":{"private_key":"`+strings.Repeat("A", 70<<10)+`"}}`))

	_, err = large.client(t).CertificateKeyPair(t.Context(), keyPairVersion)
	if !errors.Is(err, credhub.ErrAnswerTooLarge) {
		t.Fatalf("err = %v, want ErrAnswerTooLarge", err)
	}

	assertNoContent(t, err)

	missing := newReadServer(t, func(*http.Request) (int, string) {
		return http.StatusNotFound, `{"error":` + jsonString(t, key) + `}`
	})

	_, err = missing.client(t).CertificateKeyPair(t.Context(), keyPairVersion)
	if !errors.Is(err, credhub.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}

	assertNoContent(t, err)
}

func assertNoContent(t *testing.T, err error) {
	t.Helper()

	text := err.Error()
	if strings.Contains(text, "PRIVATE") || strings.Contains(text, "BEGIN") || strings.Contains(text, "AAAA") || base64Run.MatchString(text) {
		t.Errorf("error carries answer text: %q", text)
	}
}
