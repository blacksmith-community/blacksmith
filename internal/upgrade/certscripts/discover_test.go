package certscripts_test

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"blacksmith/internal/certpatch"
	"blacksmith/internal/upgrade/certscripts"
)

const (
	valkeyJobDir = "/var/vcap/data/jobs/valkey/0a1b2c3d"
	bpmJobDir    = "/var/vcap/data/jobs/bpm/9f8e7d6c"
	valkeyTLS    = valkeyJobDir + "/config/tls"
	valkeyCert   = valkeyTLS + "/server.crt"
	valkeyKey    = valkeyTLS + "/server.key"
	nobodyUID    = 65534
	testVM       = "valkey/0"
	testVariable = "valkey_tls"

	valkeyJob     = "valkey"
	bpmJob        = "bpm"
	otherVariable = "other_tls"
	noConsumer    = "no consumer"
)

// pair is a throwaway certificate and its key, in PEM form.
type pair struct {
	cert string
	key  string
}

// newPair generates a self-signed certificate and its key. Nothing in it
// is ever used outside the test that made it.
func newPair(t *testing.T, common string) pair {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: common},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}

	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}

	return pair{
		cert: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		key:  string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})),
	}
}

func (p pair) certHash() string { return certpatch.NormalisedHash([]byte(p.cert)) }

func (p pair) keyHash() string { return certpatch.NormalisedHash([]byte(p.key)) }

// runScript sends Script(name) to bash on stdin, the way the broker sends
// it to `sudo -n /bin/bash -s --`, with op and args after it. The test
// root goes in CERTPATCH_ROOT, and env adds any other settings. It
// returns stdout and the exit code.
func runScript(t *testing.T, root string, env []string, name, operation string, args ...string) (string, int) {
	t.Helper()

	text, err := certscripts.Script(name)
	if err != nil {
		t.Fatal(err)
	}

	argv := append([]string{"-s", "--", operation}, args...)

	var stdout, stderr bytes.Buffer

	cmd := exec.CommandContext(t.Context(), "/bin/bash", argv...) //nolint:gosec // the script under test

	cmd.Env = append([]string{"CERTPATCH_ROOT=" + root}, env...)
	cmd.Stdin = strings.NewReader(text)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err = cmd.Run()
	if err == nil {
		return stdout.String(), 0
	}

	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("running %s %s: %v", name, operation, err)
	}

	t.Logf("%s %s exited %d; stderr: %s", name, operation, exit.ExitCode(), stderr.String())

	return stdout.String(), exit.ExitCode()
}

// newDiscoverTree copies testdata/vm under a fresh root owned by root,
// points /var/vcap/jobs/valkey and /var/vcap/jobs/bpm at their resolved
// directories, and writes the certificate, with CRLF endings, and its
// key into the valkey job.
func newDiscoverTree(t *testing.T, old pair) *vmTree {
	t.Helper()
	requireRoot(t)

	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	tree := &vmTree{root: root}

	err = filepath.WalkDir("testdata/vm", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		rel := strings.TrimPrefix(path, "testdata/vm")
		if entry.IsDir() {
			return os.MkdirAll(tree.at(rel), 0o755) //nolint:gosec // a test tree
		}

		content, err := os.ReadFile(path) //nolint:gosec // a fixture in this package
		if err != nil {
			return fmt.Errorf("read the fixture: %w", err)
		}

		return os.WriteFile(tree.at(rel), content, 0o644) //nolint:gosec // a test tree
	})
	if err != nil {
		t.Fatal(err)
	}

	tree.mkdir(t, "/var/vcap/jobs")
	tree.mkdir(t, valkeyTLS)
	tree.mkdir(t, "/etc")
	symlink(t, tree.at(valkeyJobDir), tree.at("/var/vcap/jobs/valkey"))
	symlink(t, tree.at(bpmJobDir), tree.at("/var/vcap/jobs/bpm"))

	tree.writeMode(t, valkeyCert, strings.ReplaceAll(old.cert, "\n", "\r\n"), 0o644)
	tree.writeMode(t, valkeyKey, old.key, 0o600)

	err = os.Chown(tree.at(valkeyKey), nobodyUID, nobodyUID)
	if err != nil {
		t.Fatal(err)
	}

	return tree
}

// writeMode writes a fixture file and sets its mode exactly.
func (v *vmTree) writeMode(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()

	v.write(t, path, content)
	v.chmod(t, path, mode)
}

// discover runs discover.sh in the tree with the given hashes and parses
// its output.
func (v *vmTree) discover(t *testing.T, hashes ...string) *certscripts.Discovery {
	t.Helper()

	out, code := runScript(t, v.root, nil, "discover", "discover", hashes...)
	if code != 0 {
		t.Fatalf("discover exited %d", code)
	}

	found, err := certscripts.ParseDiscovery([]byte(out))
	if err != nil {
		t.Fatalf("ParseDiscovery: %v\noutput:\n%s", err, out)
	}

	return found
}

func TestMonitNameMapping(t *testing.T) {
	t.Parallel()

	old := newPair(t, valkeyJob)
	tree := newDiscoverTree(t, old)
	found := tree.discover(t, old.certHash())

	want := map[string][]string{
		"blacksmith": {"blacksmith", "blacksmith-ui"},
		bpmJob:       {bpmJob},
		"redis":      {"redis", "redis-exporter"},
		valkeyJob:    {valkeyJob},
	}

	if !reflect.DeepEqual(found.Processes, want) {
		t.Errorf("Processes = %v, want %v", found.Processes, want)
	}
}

func TestDiscoverFindsCertAndKey(t *testing.T) {
	t.Parallel()

	old := newPair(t, valkeyJob)
	other := newPair(t, "other")
	tree := newDiscoverTree(t, old)

	found := tree.discover(t, old.certHash(), old.keyHash(), other.certHash())

	wantDirs := map[string]string{valkeyJob: valkeyJobDir, bpmJob: bpmJobDir}
	if !reflect.DeepEqual(found.JobDirs, wantDirs) {
		t.Errorf("JobDirs = %v, want %v", found.JobDirs, wantDirs)
	}

	want := []certscripts.Match{
		{Job: valkeyJob, Path: valkeyCert, Hash: old.certHash(), Owner: "root:root", Mode: "0644", RootDirs: true},
		{Job: valkeyJob, Path: valkeyKey, Hash: old.keyHash(), Owner: "nobody:nogroup", Mode: "0600", RootDirs: true},
	}

	if !reflect.DeepEqual(found.Matches, want) {
		t.Errorf("Matches = %+v\nwant      %+v", found.Matches, want)
	}

	if len(found.Embedded) != 0 {
		t.Errorf("Embedded = %+v, want none", found.Embedded)
	}

	found.VM = testVM

	consumers, err := certscripts.BuildConsumers(found,
		map[string]string{testVariable: old.certHash()},
		map[string]string{testVariable: old.keyHash()})
	if err != nil {
		t.Fatalf("BuildConsumers: %v", err)
	}

	wantConsumers := []certscripts.Consumer{{
		Job: valkeyJob, Variable: testVariable, CertPath: valkeyCert, KeyPath: valkeyKey, Processes: []string{valkeyJob},
	}}

	if !reflect.DeepEqual(consumers, wantConsumers) {
		t.Errorf("consumers = %+v, want %+v", consumers, wantConsumers)
	}
}

func TestDiscoverReportsUnsafeDirectories(t *testing.T) {
	t.Parallel()

	old := newPair(t, valkeyJob)
	tree := newDiscoverTree(t, old)
	tree.chmod(t, valkeyTLS, 0o777)

	found := tree.discover(t, old.certHash(), old.keyHash())

	if len(found.Matches) != 2 {
		t.Fatalf("Matches = %+v, want the certificate and the key", found.Matches)
	}

	for _, m := range found.Matches {
		if m.RootDirs {
			t.Errorf("%s under a 0777 directory reports rootdirs=yes", m.Path)
		}
	}

	// A second name for the key fails the checks a write would make too.
	tree = newDiscoverTree(t, old)

	err := os.Link(tree.at(valkeyKey), tree.at(valkeyJobDir+"/config/copy.key"))
	if err != nil {
		t.Fatal(err)
	}

	for _, m := range tree.discover(t, old.keyHash()).Matches {
		if m.RootDirs {
			t.Errorf("%s with two links reports rootdirs=yes", m.Path)
		}
	}
}

func TestDiscoverSkipsSymlinkedFiles(t *testing.T) {
	t.Parallel()

	old := newPair(t, valkeyJob)
	tree := newDiscoverTree(t, old)
	tree.replace(t, valkeyCert)
	tree.write(t, "/etc/server.crt", old.cert)
	symlink(t, tree.at("/etc/server.crt"), tree.at(valkeyCert))

	// A symlinked directory is not followed either.
	tree.mkdir(t, "/etc/elsewhere")
	tree.write(t, "/etc/elsewhere/server.crt", old.cert)
	symlink(t, tree.at("/etc/elsewhere"), tree.at(valkeyJobDir+"/config/linked"))

	found := tree.discover(t, old.certHash())

	if len(found.Matches) != 0 || len(found.Embedded) != 0 {
		t.Errorf("a symlinked file was reported: matches %+v embedded %+v", found.Matches, found.Embedded)
	}
}

func TestDiscoverReportsEmbedded(t *testing.T) {
	t.Parallel()

	old := newPair(t, valkeyJob)
	other := newPair(t, "other")
	bundle := valkeyJobDir + "/config/bundle.pem"

	tree := newDiscoverTree(t, old)
	tree.write(t, bundle, old.cert+other.cert)

	found := tree.discover(t, old.certHash(), old.keyHash())

	want := []certscripts.Match{{Job: valkeyJob, Path: bundle, Hash: old.certHash()}}
	if !reflect.DeepEqual(found.Embedded, want) {
		t.Errorf("Embedded = %+v, want %+v", found.Embedded, want)
	}

	if len(found.Matches) != 2 {
		t.Errorf("Matches = %+v, want only the certificate and the key", found.Matches)
	}

	// An old certificate indented inside YAML, or escaped inside JSON, is
	// embedded too.
	indented := "tls:\n  cert: |\n    " + strings.ReplaceAll(strings.TrimSuffix(old.cert, "\n"), "\n", "\n    ") + "\n"
	escaped := `{"cert":"` + strings.ReplaceAll(old.cert, "\n", `\n`) + `"}` + "\n"

	for name, content := range map[string]string{"props.yml": indented, "props.json": escaped} {
		tree := newDiscoverTree(t, old)
		path := valkeyJobDir + "/config/" + name
		tree.write(t, path, content)

		found := tree.discover(t, old.certHash())

		want := []certscripts.Match{{Job: valkeyJob, Path: path, Hash: old.certHash()}}
		if !reflect.DeepEqual(found.Embedded, want) {
			t.Errorf("%s: Embedded = %+v, want %+v", name, found.Embedded, want)
		}
	}
}

func TestDiscoverRefusesBadHashes(t *testing.T) {
	t.Parallel()

	old := newPair(t, valkeyJob)
	tree := newDiscoverTree(t, old)

	for _, args := range [][]string{
		{},
		{"not-a-hash"},
		{strings.ToUpper(old.certHash())},
		{old.certHash(), old.certHash() + "0"},
		{"$(id)"},
	} {
		out, code := runScript(t, tree.root, nil, "discover", "discover", args...)
		if code != usageExit {
			t.Errorf("discover %q exited %d, want 2", args, code)
		}

		if out != "" {
			t.Errorf("discover %q printed %q", args, out)
		}
	}

	if _, code := runScript(t, tree.root, nil, "discover", "nonsense"); code != usageExit {
		t.Errorf("an unknown operation exited %d, want 2", code)
	}
}

// discoveryText is a well-formed discovery output for the Go-only tests.
func discoveryText(certHash, keyHash string) string {
	return strings.Join([]string{
		"jobdir job=valkey path=" + valkeyJobDir,
		"jobdir job=bpm path=" + bpmJobDir,
		"match job=valkey path=" + valkeyCert + " hash=" + certHash + " owner=root:root mode=0644 rootdirs=yes",
		"match job=valkey path=" + valkeyKey + " hash=" + keyHash + " owner=vcap:vcap mode=0600 rootdirs=yes",
		"proc job=valkey name=valkey",
		"proc job=bpm name=bpm",
	}, "\n") + "\n"
}

const (
	hashA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	hashB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	hashC = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	hashD = "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
)

func TestParseDiscovery(t *testing.T) {
	t.Parallel()

	found, err := certscripts.ParseDiscovery([]byte(discoveryText(hashA, hashB)))
	if err != nil {
		t.Fatalf("ParseDiscovery: %v", err)
	}

	if found.JobDirs[valkeyJob] != valkeyJobDir || found.JobDirs[bpmJob] != bpmJobDir || len(found.JobDirs) != 2 {
		t.Errorf("JobDirs = %v", found.JobDirs)
	}

	if len(found.Matches) != 2 || found.Matches[1].Owner != "vcap:vcap" || !found.Matches[1].RootDirs {
		t.Errorf("Matches = %+v", found.Matches)
	}

	if !reflect.DeepEqual(found.Processes, map[string][]string{valkeyJob: {valkeyJob}, bpmJob: {bpmJob}}) {
		t.Errorf("Processes = %v", found.Processes)
	}

	if found.VM != "" {
		t.Errorf("VM = %q, want it left for the caller", found.VM)
	}
}

func TestParseDiscoveryRejects(t *testing.T) {
	t.Parallel()

	good := discoveryText(hashA, hashB)
	match := func(rest string) string { return "match job=valkey " + rest + "\n" }
	fields := " hash=" + hashA + " owner=root:root mode=0644 rootdirs=yes"

	bad := map[string]string{
		"match outside its job":      match("path=" + bpmJobDir + "/x.crt" + fields),
		"match through ..":           match("path=" + valkeyJobDir + "/../x.crt" + fields),
		"match with a refused path":  match("path=!" + fields),
		"match with no jobdir":       "match job=redis path=/var/vcap/data/jobs/redis/1/x.crt" + fields + "\n",
		"match with a short hash":    match("path=" + valkeyCert + " hash=abc owner=root:root mode=0644 rootdirs=yes"),
		"match with a bad mode":      match("path=" + valkeyCert + " hash=" + hashA + " owner=root:root mode=644 rootdirs=yes"),
		"match with a bad rootdirs":  match("path=" + valkeyCert + " hash=" + hashA + " owner=root:root mode=0644 rootdirs=maybe"),
		"match with a bad owner":     match("path=" + valkeyCert + " hash=" + hashA + " owner=root mode=0644 rootdirs=yes"),
		"match missing a field":      match("path=" + valkeyCert + " hash=" + hashA + " owner=root:root mode=0644"),
		"embedded outside its job":   "embedded job=valkey path=/etc/x.pem hash=" + hashA + "\n",
		"jobdir with a refused path": "jobdir job=redis path=!\n",
		"jobdir of another job":      "jobdir job=redis path=" + bpmJobDir + "\n",
		"jobdir with a colon":        "jobdir job=redis path=/var/vcap/data/jobs/redis/a:b\n",
		"jobdir repeated":            "jobdir job=valkey path=" + valkeyJobDir + "\n",
		"jobdir one level up":        "jobdir job=redis path=/var/vcap/data/jobs/redis\n",
		"unknown kind":               "secret job=valkey\n",
		"bare kind":                  "match\n",
		"proc with a bad name":       "proc job=valkey name=-c\n",
		"control character":          "proc job=valkey name=a\x1bb\n",
	}

	for name, extra := range bad {
		found, err := certscripts.ParseDiscovery([]byte(good + extra))
		if err == nil {
			t.Errorf("%s: ParseDiscovery accepted %q", name, extra)

			continue
		}

		if found != nil {
			t.Errorf("%s: ParseDiscovery returned a result with its error", name)
		}

		if !errors.Is(err, certscripts.ErrMalformedOutput) {
			t.Errorf("%s: error %v is not ErrMalformedOutput", name, err)
		}
	}
}

// consumersFor builds the consumers of testVariable from text, with the
// VM set.
func consumersFor(t *testing.T, text string, certs, keys map[string]string) ([]certscripts.Consumer, error) {
	t.Helper()

	found, err := certscripts.ParseDiscovery([]byte(text))
	if err != nil {
		t.Fatalf("ParseDiscovery: %v", err)
	}

	found.VM = testVM

	return certscripts.BuildConsumers(found, certs, keys) //nolint:wrapcheck // checked by the caller
}

func TestBuildConsumers(t *testing.T) {
	t.Parallel()

	certs := map[string]string{testVariable: hashA, otherVariable: hashC}
	keys := map[string]string{testVariable: hashB, otherVariable: hashD}
	text := discoveryText(hashA, hashB) +
		"match job=bpm path=" + bpmJobDir + "/b.crt hash=" + hashC + " owner=root:root mode=0644 rootdirs=yes\n" +
		"match job=bpm path=" + bpmJobDir + "/b.key hash=" + hashD + " owner=root:root mode=0600 rootdirs=yes\n" +
		"match job=bpm path=" + bpmJobDir + "/a.crt hash=" + hashC + " owner=root:root mode=0644 rootdirs=yes\n" +
		"match job=bpm path=" + bpmJobDir + "/a.key hash=" + hashD + " owner=root:root mode=0600 rootdirs=yes\n"

	got, err := consumersFor(t, text, certs, keys)
	if err != nil {
		t.Fatalf("BuildConsumers: %v", err)
	}

	want := []certscripts.Consumer{
		{Job: bpmJob, Variable: otherVariable, CertPath: bpmJobDir + "/a.crt", KeyPath: bpmJobDir + "/a.key", Processes: []string{bpmJob}},
		{Job: bpmJob, Variable: otherVariable, CertPath: bpmJobDir + "/b.crt", KeyPath: bpmJobDir + "/b.key", Processes: []string{bpmJob}},
		{Job: valkeyJob, Variable: testVariable, CertPath: valkeyCert, KeyPath: valkeyKey, Processes: []string{valkeyJob}},
	}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("consumers = %+v\nwant        %+v", got, want)
	}
}

func TestBuildConsumersFailures(t *testing.T) {
	t.Parallel()

	certs := map[string]string{testVariable: hashA}
	keys := map[string]string{testVariable: hashB}
	jobdirs := "jobdir job=valkey path=" + valkeyJobDir + "\njobdir job=bpm path=" + bpmJobDir + "\n"
	certLine := "match job=valkey path=" + valkeyCert + " hash=" + hashA + " owner=root:root mode=0644 rootdirs=yes\n"
	keyLine := "match job=valkey path=" + valkeyKey + " hash=" + hashB + " owner=root:root mode=0600 rootdirs=yes\n"

	rows := []struct {
		name  string
		text  string
		certs map[string]string
		keys  map[string]string
		words []string
	}{
		{"certificate without key", jobdirs + certLine, certs, keys, []string{testVM, testVariable, "no key file"}},
		{"key without certificate", jobdirs + keyLine, certs, keys, []string{testVM, testVariable, "no certificate file"}},
		{"key in another job", jobdirs + certLine +
			"match job=bpm path=" + bpmJobDir + "/k.key hash=" + hashB + " owner=root:root mode=0600 rootdirs=yes\n",
			certs, keys, []string{testVM, testVariable}},
		{"two certificates and one key", jobdirs + certLine + keyLine +
			"match job=valkey path=" + valkeyJobDir + "/copy.crt hash=" + hashA + " owner=root:root mode=0644 rootdirs=yes\n",
			certs, keys, []string{testVM, testVariable}},
		{"embedded", jobdirs + certLine + keyLine + "embedded job=valkey path=" + valkeyJobDir + "/bundle.pem hash=" + hashA + "\n",
			certs, keys, []string{testVM, testVariable, valkeyJobDir + "/bundle.pem"}},
		{"embedded key", jobdirs + certLine + keyLine + "embedded job=valkey path=" + valkeyJobDir + "/bundle.pem hash=" + hashB + "\n",
			certs, keys, []string{testVM, testVariable}},
		{"rootdirs=no", jobdirs + strings.Replace(certLine, "rootdirs=yes", "rootdirs=no", 1) + keyLine,
			certs, keys, []string{testVM, testVariable, valkeyCert}},
		{noConsumer, jobdirs, certs, keys, []string{testVM, testVariable, noConsumer}},
		{"one of two variables has no consumer", jobdirs + certLine + keyLine,
			map[string]string{testVariable: hashA, otherVariable: hashC}, map[string]string{testVariable: hashB, otherVariable: hashD},
			[]string{testVM, otherVariable, noConsumer}},
		{"a hash nobody asked for", jobdirs + certLine + keyLine +
			"match job=valkey path=" + valkeyJobDir + "/x.crt hash=" + hashC + " owner=root:root mode=0644 rootdirs=yes\n",
			certs, keys, []string{testVM}},
		{"a variable without a key hash", jobdirs + certLine + keyLine,
			certs, map[string]string{}, []string{testVariable}},
	}

	for _, row := range rows {
		got, err := consumersFor(t, row.text, row.certs, row.keys)
		if err == nil {
			t.Errorf("%s: BuildConsumers returned %+v and no error", row.name, got)

			continue
		}

		if !errors.Is(err, certscripts.ErrPatchUnsafe) {
			t.Errorf("%s: error %v is not ErrPatchUnsafe", row.name, err)
		}

		if got != nil {
			t.Errorf("%s: BuildConsumers returned consumers with its error", row.name)
		}

		for _, word := range row.words {
			if !strings.Contains(err.Error(), word) {
				t.Errorf("%s: error %q does not name %q", row.name, err, word)
			}
		}
	}
}

func TestScriptDiscover(t *testing.T) {
	t.Parallel()

	body, err := certscripts.Script("discover")
	if err != nil {
		t.Fatalf("Script(discover): %v", err)
	}

	if body != readSource(t, libScript)+readSource(t, "discover") {
		t.Error("Script(discover) is not lib.sh followed by discover.sh")
	}
}

func TestSudoCheck(t *testing.T) {
	t.Parallel()

	if certscripts.SudoCheckCommand != "sudo -n true" {
		t.Errorf("SudoCheckCommand = %q, want sudo -n true", certscripts.SudoCheckCommand)
	}

	if got := certscripts.ErrNoPasswordlessSudo.Error(); got != "passwordless sudo is not available for BOSH SSH users" {
		t.Errorf("ErrNoPasswordlessSudo = %q", got)
	}
}
