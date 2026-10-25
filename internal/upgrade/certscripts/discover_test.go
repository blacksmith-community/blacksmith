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
	"sort"
	"strings"
	"testing"
	"time"

	"blacksmith/internal/certpatch"
	"blacksmith/internal/ops/scripts"
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
	redisJob      = "redis"
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

// runDiscoverFile runs discover.sh's _dc_file on file in the tree, with
// hash wanted, and returns its output and exit code. find lists only
// regular files, so a file that changes after find has listed it is the
// only way _dc_file sees anything else, and the test calls _dc_file on
// such a file directly. It runs lib.sh, certlib.sh, and discover.sh without the
// dispatch at the end of discover.sh, and then the one call.
func runDiscoverFile(t *testing.T, tree *vmTree, file, hash string) (string, int) {
	t.Helper()

	body, _, found := strings.Cut(readSource(t, "discover"), "\ncase ${1:-} in\n")
	if !found {
		t.Fatal("discover.sh no longer ends with its dispatch")
	}

	driver := `declare -A wanted=(["$1"]=1); _dc_file "$CERTPATCH_ROOT" valkey "$2" "$CERTPATCH_ROOT$3"` + "\n"
	argv := []string{"-s", "--", hash, pinnedJobDir, file}

	var stdout, stderr bytes.Buffer

	cmd := exec.CommandContext(t.Context(), "/bin/bash", argv...) //nolint:gosec // the script under test
	cmd.Env = []string{"CERTPATCH_ROOT=" + tree.root}
	cmd.Stdin = strings.NewReader(libText(t) + body + "\n" + driver)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err == nil {
		return stdout.String(), 0
	}

	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("running _dc_file: %v", err)
	}

	t.Logf("_dc_file exited %d; stderr: %s", exit.ExitCode(), stderr.String())

	return stdout.String(), exit.ExitCode()
}

// TestDiscoverReadsOnlyRegularFiles shows that discovery checks a file is
// still a regular file, and not a symlink, right before it reads it, so
// a file swapped for a symlink after find listed it fails discovery
// instead of being read through the link.
func TestDiscoverReadsOnlyRegularFiles(t *testing.T) {
	t.Parallel()

	tree := newPinnedTree(t, pinnedTarget, pinnedJobDir)
	tree.mkdir(t, "/etc")
	tree.write(t, "/etc/bundle.pem", "preamble\n-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n")

	if out, code := runDiscoverFile(t, tree, pinnedTarget, textHash(oldText)); code != 0 || !strings.HasPrefix(out, "match ") {
		t.Fatalf("_dc_file on a regular file printed %q and exited %d, want a match and 0", out, code)
	}

	for name, target := range map[string]string{
		"a whole-file match": pinnedTarget,
		"an embedded block":  "/etc/bundle.pem",
	} {
		link := targetDir + "/link.pem"
		tree.replace(t, link)
		symlink(t, tree.at(target), tree.at(link))

		hash := textHash(oldText)
		if target != pinnedTarget {
			hash = textHash("-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n")
		}

		out, code := runDiscoverFile(t, tree, link, hash)
		if code != 1 {
			t.Errorf("%s through a symlink: _dc_file exited %d, want 1", name, code)
		}

		if out != "" {
			t.Errorf("%s through a symlink: _dc_file printed %q, want nothing", name, out)
		}
	}
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

	crlf := strings.ReplaceAll(old.cert, "\n", "\r\n")
	want := []certscripts.Match{
		{
			Job: valkeyJob, Path: valkeyCert, Hash: old.certHash(), RawHash: certpatch.RawHash([]byte(crlf)),
			Tail: "\r\n", Owner: "root:root", Mode: "0644", RootDirs: true,
		},
		{
			Job: valkeyJob, Path: valkeyKey, Hash: old.keyHash(), RawHash: certpatch.RawHash([]byte(old.key)),
			Tail: "\n", Owner: "nobody:nogroup", Mode: "0600", RootDirs: true,
		},
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

// TestDiscoverMatchesPaddedFiles shows that a file holding the old PEM
// with blank lines before it and whitespace after it, as a job template
// renders a blank line before <%= p(...) %> and more after it, is a
// whole-file match that patching can replace. The match carries the
// file's raw hash and the bytes around its PEM.
func TestDiscoverMatchesPaddedFiles(t *testing.T) {
	t.Parallel()

	old := newPair(t, valkeyJob)
	tree := newDiscoverTree(t, old)
	certBytes := "\n" + old.cert + "\n\n"
	keyBytes := "\r\n\n" + old.key + "\r\n\t \n"

	tree.writeMode(t, valkeyCert, certBytes, 0o644)
	tree.write(t, valkeyKey, keyBytes)

	found := tree.discover(t, old.certHash(), old.keyHash())

	if len(found.Embedded) != 0 {
		t.Fatalf("Embedded = %+v, want none", found.Embedded)
	}

	want := []certscripts.Match{
		{
			Job: valkeyJob, Path: valkeyCert, Hash: old.certHash(), RawHash: certpatch.RawHash([]byte(certBytes)),
			Lead: "\n", Tail: "\n\n\n", Owner: "root:root", Mode: "0644", RootDirs: true,
		},
		{
			Job: valkeyJob, Path: valkeyKey, Hash: old.keyHash(), RawHash: certpatch.RawHash([]byte(keyBytes)),
			Lead: "\r\n\n", Tail: "\n\r\n\t \n", Owner: "nobody:nogroup", Mode: "0600", RootDirs: true,
		},
	}

	if !reflect.DeepEqual(found.Matches, want) {
		t.Errorf("Matches = %+v\nwant      %+v", found.Matches, want)
	}

	found.VM = testVM

	_, err := certscripts.BuildConsumers(found,
		map[string]string{testVariable: old.certHash()},
		map[string]string{testVariable: old.keyHash()})
	if err != nil {
		t.Errorf("BuildConsumers refused a padded file: %v", err)
	}
}

// TestDiscoverStillRefusesRealEmbedding shows that only blank lines
// before the PEM and whitespace after it are accepted. A file that holds
// anything else besides the old PEM, a space or tab before its BEGIN
// marker included, is embedded, and BuildConsumers refuses it with the
// usual message.
func TestDiscoverStillRefusesRealEmbedding(t *testing.T) {
	t.Parallel()

	old := newPair(t, valkeyJob)
	chain := newPair(t, "ca")
	bundle := valkeyJobDir + "/config/bundle.pem"

	cases := map[string]struct {
		content string
		hashes  []string
	}{
		"a certificate and a chain":   {"\n" + old.cert + chain.cert + "\n\n", []string{old.certHash()}},
		"a certificate and a key":     {"\n" + old.cert + old.key + "\n", []string{old.certHash(), old.keyHash()}},
		"text before the PEM":         {"# managed\n" + old.cert, []string{old.certHash()}},
		"a character before the PEM":  {"\nx" + old.cert + "\n\n", []string{old.certHash()}},
		"text after the PEM":          {"\n" + old.cert + "tls: on\n", []string{old.certHash()}},
		"a vertical tab before a PEM": {"\v\n" + old.cert, []string{old.certHash()}},
		"a space before the BEGIN":    {" " + old.cert, []string{old.certHash()}},
		"a tab before the BEGIN":      {"\n\t" + old.cert, []string{old.certHash()}},
		"a blank line of spaces":      {"  \n" + old.cert, []string{old.certHash()}},
		"a lone carriage return":      {"\r" + old.cert, []string{old.certHash()}},
		"too many blank lines":        {strings.Repeat("\n", 33) + old.cert, []string{old.certHash()}},
		"too much after the PEM":      {old.cert + strings.Repeat(" ", 64), []string{old.certHash()}},
	}

	for name, embedding := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			tree := newDiscoverTree(t, old)
			tree.write(t, bundle, embedding.content)

			found := tree.discover(t, old.certHash(), old.keyHash())

			embedded := make([]string, 0, len(found.Embedded))

			for _, match := range found.Embedded {
				if match.Path != bundle {
					t.Errorf("Embedded holds %s, want only %s", match.Path, bundle)
				}

				embedded = append(embedded, match.Hash)
			}

			want := append([]string(nil), embedding.hashes...)
			sort.Strings(want)

			if !reflect.DeepEqual(embedded, want) {
				t.Errorf("embedded hashes = %v, want %v", embedded, want)
			}

			for _, match := range found.Matches {
				if match.Path == bundle {
					t.Errorf("%s is a whole-file match, want it embedded", bundle)
				}
			}

			found.VM = testVM

			_, err := certscripts.BuildConsumers(found,
				map[string]string{testVariable: old.certHash()},
				map[string]string{testVariable: old.keyHash()})
			if !errors.Is(err, certscripts.ErrPatchUnsafe) ||
				!strings.Contains(err.Error(), "is embedded in larger content in "+bundle+", which patching cannot replace") {
				t.Errorf("BuildConsumers error = %v, want the embedded refusal for %s", err, bundle)
			}
		})
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
		"match job=valkey path=" + valkeyCert + " hash=" + certHash + edges + " owner=root:root mode=0644 rootdirs=yes",
		"match job=valkey path=" + valkeyKey + " hash=" + keyHash + edges + " owner=vcap:vcap mode=0600 rootdirs=yes",
		"proc job=valkey name=valkey",
		"proc job=bpm name=bpm",
	}, "\n") + "\n"
}

// edges is the raw hash, lead, and tail of a match line in the Go-only
// tests.
const edges = " raw=" + hashD + " lead=- tail=n"

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
	fields := " hash=" + hashA + edges + " owner=root:root mode=0644 rootdirs=yes"

	bad := map[string]string{
		"match with no jobdir":      "match job=redis path=/var/vcap/data/jobs/redis/1/x.crt" + fields + "\n",
		"match with a short hash":   match("path=" + valkeyCert + " hash=abc" + edges + " owner=root:root mode=0644 rootdirs=yes"),
		"match with a bad mode":     match("path=" + valkeyCert + " hash=" + hashA + edges + " owner=root:root mode=644 rootdirs=yes"),
		"match with a bad rootdirs": match("path=" + valkeyCert + " hash=" + hashA + edges + " owner=root:root mode=0644 rootdirs=maybe"),
		"match with a bad owner":    match("path=" + valkeyCert + " hash=" + hashA + edges + " owner=root mode=0644 rootdirs=yes"),
		"match missing a field":     match("path=" + valkeyCert + " hash=" + hashA + edges + " owner=root:root mode=0644"),
		"match with no raw hash":    match("path=" + valkeyCert + " hash=" + hashA + " lead=- tail=n owner=root:root mode=0644 rootdirs=yes"),
		"match with a short raw":    match("path=" + valkeyCert + " hash=" + hashA + " raw=abc lead=- tail=n owner=root:root mode=0644 rootdirs=yes"),
		"match with a space lead":   match("path=" + valkeyCert + " hash=" + hashA + " raw=" + hashD + " lead=s tail=n owner=root:root mode=0644 rootdirs=yes"),
		"match with a lone CR lead": match("path=" + valkeyCert + " hash=" + hashA + " raw=" + hashD + " lead=r tail=n owner=root:root mode=0644 rootdirs=yes"),
		"match with an empty lead":  match("path=" + valkeyCert + " hash=" + hashA + " raw=" + hashD + " lead= tail=n owner=root:root mode=0644 rootdirs=yes"),
		"match with a bad tail":     match("path=" + valkeyCert + " hash=" + hashA + " raw=" + hashD + " lead=n tail=x owner=root:root mode=0644 rootdirs=yes"),
		"jobdir repeated":           "jobdir job=valkey path=" + valkeyJobDir + "\n",
		"unknown kind":              "secret job=valkey\n",
		"bare kind":                 "match\n",
		"proc with a bad name":      "proc job=valkey name=-c\n",
		"control character":         "proc job=valkey name=a\x1bb\n",
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

		if !errors.Is(err, scripts.ErrMalformedOutput) {
			t.Errorf("%s: error %v is not ErrMalformedOutput", name, err)
		}
	}
}

// TestParseDiscoveryRefusesLayout shows that a job laid out in a way
// patching cannot follow, as opposed to output that breaks the contract,
// is refused as unsafe to patch, with the job named, so the operator is
// told to redeploy.
func TestParseDiscoveryRefusesLayout(t *testing.T) {
	t.Parallel()

	good := discoveryText(hashA, hashB)
	match := func(rest string) string { return "match job=valkey " + rest + "\n" }
	fields := " hash=" + hashA + edges + " owner=root:root mode=0644 rootdirs=yes"

	for name, row := range map[string]struct {
		extra string
		job   string
	}{
		"match outside its job":      {match("path=" + bpmJobDir + "/x.crt" + fields), valkeyJob},
		"match through ..":           {match("path=" + valkeyJobDir + "/../x.crt" + fields), valkeyJob},
		"match with a refused path":  {match("path=!" + fields), valkeyJob},
		"embedded outside its job":   {"embedded job=valkey path=/etc/x.pem hash=" + hashA + "\n", valkeyJob},
		"jobdir with a refused path": {"jobdir job=redis path=!\n", redisJob},
		"jobdir of another job":      {"jobdir job=redis path=" + bpmJobDir + "\n", redisJob},
		"jobdir with a colon":        {"jobdir job=redis path=/var/vcap/data/jobs/redis/a:b\n", redisJob},
		"jobdir one level up":        {"jobdir job=redis path=/var/vcap/data/jobs/redis\n", redisJob},
	} {
		found, err := certscripts.ParseDiscovery([]byte(good + row.extra))
		if found != nil {
			t.Errorf("%s: ParseDiscovery returned a result with its error", name)
		}

		if !errors.Is(err, certscripts.ErrPatchUnsafe) {
			t.Errorf("%s: error %v is not ErrPatchUnsafe", name, err)

			continue
		}

		if errors.Is(err, scripts.ErrMalformedOutput) {
			t.Errorf("%s: error %v is ErrMalformedOutput too", name, err)
		}

		if !strings.Contains(err.Error(), "the job "+row.job+" ") {
			t.Errorf("%s: error %q does not name the job %s", name, err, row.job)
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
		"match job=bpm path=" + bpmJobDir + "/b.crt hash=" + hashC + edges + " owner=root:root mode=0644 rootdirs=yes\n" +
		"match job=bpm path=" + bpmJobDir + "/b.key hash=" + hashD + edges + " owner=root:root mode=0600 rootdirs=yes\n" +
		"match job=bpm path=" + bpmJobDir + "/a.crt hash=" + hashC + edges + " owner=root:root mode=0644 rootdirs=yes\n" +
		"match job=bpm path=" + bpmJobDir + "/a.key hash=" + hashD + edges + " owner=root:root mode=0600 rootdirs=yes\n"

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
	certLine := "match job=valkey path=" + valkeyCert + " hash=" + hashA + edges + " owner=root:root mode=0644 rootdirs=yes\n"
	keyLine := "match job=valkey path=" + valkeyKey + " hash=" + hashB + edges + " owner=root:root mode=0600 rootdirs=yes\n"

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
			"match job=bpm path=" + bpmJobDir + "/k.key hash=" + hashB + edges + " owner=root:root mode=0600 rootdirs=yes\n",
			certs, keys, []string{testVM, testVariable}},
		{"two certificates and one key", jobdirs + certLine + keyLine +
			"match job=valkey path=" + valkeyJobDir + "/copy.crt hash=" + hashA + edges + " owner=root:root mode=0644 rootdirs=yes\n",
			certs, keys, []string{testVM, testVariable}},
		{"embedded", jobdirs + certLine + keyLine + "embedded job=valkey path=" + valkeyJobDir + "/bundle.pem hash=" + hashA + "\n",
			certs, keys, []string{testVM, testVariable, valkeyJobDir + "/bundle.pem"}},
		{"embedded key", jobdirs + certLine + keyLine + "embedded job=valkey path=" + valkeyJobDir + "/bundle.pem hash=" + hashB + "\n",
			certs, keys, []string{testVM, testVariable}},
		{"rootdirs=no", jobdirs + strings.Replace(certLine, "rootdirs=yes", "rootdirs=no why=many-links at="+valkeyCert, 1) + keyLine,
			certs, keys, []string{testVM, testVariable, valkeyCert}},
		{"a hash nobody asked for", jobdirs + certLine + keyLine +
			"match job=valkey path=" + valkeyJobDir + "/x.crt hash=" + hashC + edges + " owner=root:root mode=0644 rootdirs=yes\n",
			certs, keys, []string{testVM}},
		{"a variable without a key hash", jobdirs + certLine + keyLine,
			certs, map[string]string{}, []string{testVariable}},
		{"a job with no monit process", jobdirs + certLine + keyLine + "proc job=bpm name=bpm\n",
			certs, keys, []string{testVM, valkeyJob, testVariable, "no monit process"}},
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

// TestBuildConsumersAllowsVMWithoutConsumer shows that a VM that holds
// none of a variable's files simply has nothing to patch for it, because
// another instance group can hold them.
func TestBuildConsumersAllowsVMWithoutConsumer(t *testing.T) {
	t.Parallel()

	jobdirs := "jobdir job=valkey path=" + valkeyJobDir + "\n"

	got, err := consumersFor(t, jobdirs, map[string]string{testVariable: hashA}, map[string]string{testVariable: hashB})
	if err != nil {
		t.Fatalf("BuildConsumers on a VM with no consumer: %v", err)
	}

	if len(got) != 0 {
		t.Errorf("consumers = %+v, want none", got)
	}

	got, err = consumersFor(t, discoveryText(hashC, hashD),
		map[string]string{testVariable: hashA, otherVariable: hashC}, map[string]string{testVariable: hashB, otherVariable: hashD})
	if err != nil {
		t.Fatalf("BuildConsumers with one of two variables on the VM: %v", err)
	}

	if len(got) != 1 || got[0].Variable != otherVariable {
		t.Errorf("consumers = %+v, want one of %s", got, otherVariable)
	}
}

// TestRequireConsumers runs discovery on two instance groups, where only
// the valkey VM holds the certificate, and shows that a variable needs a
// consumer on some VM, not on every VM.
func TestRequireConsumers(t *testing.T) {
	t.Parallel()

	certs := map[string]string{testVariable: hashA}
	keys := map[string]string{testVariable: hashB}

	onValkey, err := consumersFor(t, discoveryText(hashA, hashB), certs, keys)
	if err != nil {
		t.Fatalf("BuildConsumers on the valkey VM: %v", err)
	}

	onProxy, err := consumersFor(t, "jobdir job=bpm path="+bpmJobDir+"\n", certs, keys)
	if err != nil {
		t.Fatalf("BuildConsumers on the proxy VM: %v", err)
	}

	perVM := map[string][]certscripts.Consumer{testVM: onValkey, "proxy/0": onProxy}

	err = certscripts.RequireConsumers([]string{testVariable}, perVM)
	if err != nil {
		t.Errorf("RequireConsumers with a consumer on one VM: %v", err)
	}

	for name, row := range map[string]struct {
		variables []string
		perVM     map[string][]certscripts.Consumer
		missing   []string
	}{
		"a variable on neither VM": {[]string{testVariable, otherVariable}, perVM, []string{otherVariable}},
		"no VM has a consumer":     {[]string{otherVariable, testVariable}, map[string][]certscripts.Consumer{"proxy/0": onProxy}, []string{otherVariable, testVariable}},
		"no VMs at all":            {[]string{testVariable}, nil, []string{testVariable}},
	} {
		err := certscripts.RequireConsumers(row.variables, row.perVM)
		if !errors.Is(err, certscripts.ErrPatchUnsafe) {
			t.Errorf("%s: RequireConsumers error = %v, want ErrPatchUnsafe", name, err)

			continue
		}

		for _, variable := range row.missing {
			if !strings.Contains(err.Error(), variable) {
				t.Errorf("%s: error %q does not name %s", name, err, variable)
			}
		}

		if !strings.Contains(err.Error(), noConsumer) {
			t.Errorf("%s: error %q does not say %q", name, err, noConsumer)
		}

		if len(row.missing) == 1 && row.missing[0] == otherVariable && strings.Contains(err.Error(), testVariable) {
			t.Errorf("%s: error %q names %s, which has a consumer", name, err, testVariable)
		}
	}
}

func TestScriptDiscover(t *testing.T) {
	t.Parallel()

	body, err := certscripts.Script("discover")
	if err != nil {
		t.Fatalf("Script(discover): %v", err)
	}

	if body != libText(t)+readSource(t, "discover") {
		t.Error("Script(discover) is not lib.sh, certlib.sh, then discover.sh")
	}
}

func TestSudoCheck(t *testing.T) {
	t.Parallel()

	if scripts.SudoCheckCommand != "sudo -n true" {
		t.Errorf("SudoCheckCommand = %q, want sudo -n true", scripts.SudoCheckCommand)
	}

	if got := certscripts.ErrNoPasswordlessSudo.Error(); got != "passwordless sudo is not available for BOSH SSH users" {
		t.Errorf("ErrNoPasswordlessSudo = %q", got)
	}
}
