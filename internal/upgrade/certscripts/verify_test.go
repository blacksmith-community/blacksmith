package certscripts_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"blacksmith/internal/upgrade/certscripts"
)

// verifyName is the common name of the test's leaves.
const verifyName = "orders.ws.example"

func TestScriptVerify(t *testing.T) {
	t.Parallel()

	body, err := certscripts.Script("verify")
	if err != nil {
		t.Fatalf("Script(verify): %v", err)
	}

	if body != readSource(t, libScript)+readSource(t, "verify") {
		t.Error("Script(verify) is not lib.sh followed by verify.sh")
	}
}

// fakeOpenSSL logs its arguments beside itself, prints the answer file
// beside itself, and exits 1, as s_client does when a handshake that
// already showed the certificate then fails.
const fakeOpenSSL = `#!/bin/sh
dir=${0%/*}
printf '%s\n' "$@" > "$dir/argv.log"
[ -f "$dir/answer" ] && cat "$dir/answer"
exit 1
`

// verifyTree is a root holding a fake /usr/bin/openssl that prints
// answer.
type verifyTree struct {
	root string
}

func newVerifyTree(t *testing.T, answer string) *verifyTree {
	t.Helper()
	requireLinux(t)

	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	bin := filepath.Join(root, "usr", "bin")

	err = os.MkdirAll(bin, 0o755) //nolint:gosec // a test tree
	if err != nil {
		t.Fatal(err)
	}

	err = os.WriteFile(filepath.Join(bin, "openssl"), []byte(fakeOpenSSL), 0o755) //nolint:gosec // the fake must run
	if err != nil {
		t.Fatal(err)
	}

	if answer != "" {
		err = os.WriteFile(filepath.Join(bin, "answer"), []byte(answer), 0o600)
		if err != nil {
			t.Fatal(err)
		}
	}

	return &verifyTree{root: root}
}

// argv returns the arguments the fake was called with, or "" when it
// never ran.
func (v *verifyTree) argv(t *testing.T) string {
	t.Helper()

	body, err := os.ReadFile(filepath.Join(v.root, "usr", "bin", "argv.log"))
	if os.IsNotExist(err) {
		return ""
	}

	if err != nil {
		t.Fatal(err)
	}

	return string(body)
}

// servedLeaf returns a fresh self-signed certificate's PEM, with CRLF
// line endings when crlf is set, and the hex SHA-256 of its DER.
func servedLeaf(t *testing.T, crlf bool) (string, string) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(42),
		Subject:      pkix.Name{CommonName: verifyName},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}

	sum := sha256.Sum256(der)
	text := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))

	if crlf {
		text = strings.ReplaceAll(text, "\n", "\r\n")
	}

	return text, hex.EncodeToString(sum[:])
}

// sClientAnswer wraps the leaf in the text s_client prints around it, and
// adds a second certificate after it that verify must not read.
func sClientAnswer(t *testing.T, leaf string) string {
	t.Helper()

	other, _ := servedLeaf(t, false)

	return "CONNECTED(00000003)\r\n---\r\nCertificate chain\r\n 0 s:CN = orders.ws.example\r\n---\r\n" +
		"Server certificate\r\n" + leaf + "subject=CN = orders.ws.example\r\n---\r\n" + other +
		"SSL handshake has read 1024 bytes\r\n"
}

func TestVerifyPrintsLeafFingerprint(t *testing.T) {
	t.Parallel()

	leaf, want := servedLeaf(t, true)
	tree := newVerifyTree(t, sClientAnswer(t, leaf))

	out, code := runScript(t, tree.root, nil, "verify", "verify", "6380", verifyName)
	if code != 0 {
		t.Fatalf("verify exited %d, want 0", code)
	}

	if out != "fingerprint="+want+"\n" {
		t.Errorf("output = %q, want only the fingerprint %s", out, want)
	}

	wantArgv := "s_client\n-connect\n127.0.0.1:6380\n-servername\norders.ws.example\n"
	if got := tree.argv(t); got != wantArgv {
		t.Errorf("argv = %q, want %q", got, wantArgv)
	}

	lines, err := certscripts.ParseLines([]byte(out), map[string]bool{"fingerprint": true})
	if err != nil || len(lines) != 1 || lines[0]["fingerprint"] != want {
		t.Errorf("ParseLines = %v, %v", lines, err)
	}
}

func TestVerifyWithoutServerName(t *testing.T) {
	t.Parallel()

	leaf, want := servedLeaf(t, false)
	tree := newVerifyTree(t, sClientAnswer(t, leaf))

	out, code := runScript(t, tree.root, nil, "verify", "verify", "443", "")
	if code != 0 {
		t.Fatalf("verify exited %d, want 0", code)
	}

	if out != "fingerprint="+want+"\n" {
		t.Errorf("output = %q, want %q", out, "fingerprint="+want)
	}

	if got, wantArgv := tree.argv(t), "s_client\n-connect\n127.0.0.1:443\n"; got != wantArgv {
		t.Errorf("argv = %q, want %q", got, wantArgv)
	}
}

func TestVerifyNoCertificate(t *testing.T) {
	t.Parallel()

	answers := map[string]string{
		"nothing":       "",
		"no block":      "CONNECTED(00000003)\r\nwrite:errno=104\r\nno peer certificate available\r\n",
		"an open block": "-----BEGIN CERTIFICATE-----\r\nMIIB\r\n",
		"an empty body": "-----BEGIN CERTIFICATE-----\r\n\r\n-----END CERTIFICATE-----\r\n",
		"not base64":    "-----BEGIN CERTIFICATE-----\r\n!!!!\r\n-----END CERTIFICATE-----\r\n",
	}

	for name, answer := range answers {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			tree := newVerifyTree(t, answer)

			out, code := runScript(t, tree.root, nil, "verify", "verify", "6380", verifyName)
			if code != 1 {
				t.Errorf("verify exited %d, want 1", code)
			}

			if out != "" {
				t.Errorf("verify printed %q", out)
			}
		})
	}
}

func TestVerifyRefusesBadArguments(t *testing.T) {
	t.Parallel()

	leaf, _ := servedLeaf(t, false)
	answer := sClientAnswer(t, leaf)

	calls := map[string][]string{
		"no arguments":      {},
		"one argument":      {"6380"},
		"three arguments":   {"6380", verifyName, "extra"},
		"port zero":         {"0", verifyName},
		"port too high":     {"65536", verifyName},
		"leading zero":      {"06380", verifyName},
		"port not a number": {"six", verifyName},
		"empty port":        {"", verifyName},
		"name with a space": {"6380", "orders ws"},
		"name with a colon": {"6380", "orders:443"},
		"option as a name":  {"6380", "-connect"},
		"wildcard name":     {"6380", "*.ws.example"},
		"name too long":     {"6380", strings.Repeat("a", 254)},
	}

	for name, args := range calls {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			tree := newVerifyTree(t, answer)

			out, code := runScript(t, tree.root, nil, "verify", "verify", args...)
			if code != 2 {
				t.Errorf("verify exited %d, want 2", code)
			}

			if out != "" || tree.argv(t) != "" {
				t.Errorf("verify printed %q or ran openssl", out)
			}
		})
	}

	tree := newVerifyTree(t, answer)

	_, code := runScript(t, tree.root, nil, "verify", "check", "6380", verifyName)
	if code != 2 || tree.argv(t) != "" {
		t.Errorf("an unknown operation exited %d, want 2 with no openssl run", code)
	}
}

func TestVerifyWithoutOpenSSL(t *testing.T) {
	t.Parallel()
	requireLinux(t)

	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	out, code := runScript(t, root, nil, "verify", "verify", "6380", verifyName)
	if code != 1 || out != "" {
		t.Errorf("verify exited %d and printed %q, want 1 and nothing", code, out)
	}
}
