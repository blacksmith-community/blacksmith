package upgrade

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"blacksmith/internal/apierr"
	"blacksmith/internal/bosh/ssh"
	"blacksmith/internal/bosh/sshconn"
	"blacksmith/internal/certinventory"
	"blacksmith/internal/certpatch"
	"blacksmith/internal/credhub"
	"blacksmith/internal/upgrade/certscripts"
	"blacksmith/pkg/logger"
)

// testPair is a certificate and its private key, both as PEM.
type testPair struct {
	cert, key []byte
}

// issuePair issues a throwaway leaf and returns it with its key as PEM.
func issuePair(t *testing.T, ca testCA, spec certSpec) (testPair, *x509.Certificate) {
	t.Helper()

	cert, key := ca.issue(t, spec)

	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}

	return testPair{
		cert: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}),
		key:  pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}),
	}, cert
}

// pairCredHub adds key pairs to another CredHub fake. It hands out a
// fresh copy of a version's pair on each read and keeps every pair it
// handed out, so a test can check each was zeroed.
type pairCredHub struct {
	CredHubAPI

	mu     sync.Mutex
	pairs  map[string]testPair
	fail   map[string]error
	handed []*credhub.KeyPair
	reads  []string
}

func newPairCredHub(api CredHubAPI) *pairCredHub {
	return &pairCredHub{CredHubAPI: api, pairs: make(map[string]testPair), fail: make(map[string]error)}
}

func (p *pairCredHub) set(versionID string, pair testPair) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.pairs[versionID] = pair
}

func (p *pairCredHub) failRead(versionID string, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.fail[versionID] = err
}

func (p *pairCredHub) CertificateKeyPair(ctx context.Context, versionID string) (*credhub.KeyPair, error) {
	err := ctx.Err()
	if err != nil {
		return nil, fmt.Errorf("read pair %s: %w", versionID, err)
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	p.reads = append(p.reads, versionID)

	if err := p.fail[versionID]; err != nil {
		return nil, err
	}

	pair, ok := p.pairs[versionID]
	if !ok {
		return nil, fmt.Errorf("%w: %q", credhub.ErrNotFound, versionID)
	}

	handed := &credhub.KeyPair{VersionID: versionID, Certificate: slices.Clone(pair.cert), PrivateKey: slices.Clone(pair.key)}
	p.handed = append(p.handed, handed)

	return handed, nil
}

// allZeroed reports whether every pair handed out is zero.
func (p *pairCredHub) allZeroed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, pair := range p.handed {
		for _, b := range append(slices.Clone(pair.Certificate), pair.PrivateKey...) {
			if b != 0 {
				return false
			}
		}
	}

	return true
}

// expectAllZeroed fails unless every pair handed out is zero.
func (p *pairCredHub) expectAllZeroed(t *testing.T, at string) {
	t.Helper()

	p.mu.Lock()
	defer p.mu.Unlock()

	if len(p.handed) == 0 {
		t.Fatalf("%s: no pair was read", at)
	}

	for _, pair := range p.handed {
		for _, b := range append(slices.Clone(pair.Certificate), pair.PrivateKey...) {
			if b != 0 {
				t.Fatalf("%s: the pair of %s was not zeroed", at, pair.VersionID)
			}
		}
	}
}

// fakeVM is one VM the fake patch session runs scripts on. Its files,
// staged files, and backups map each path to the normalised hash of what
// it holds, and raws holds the raw hash of each, as rawOf reads it.
type fakeVM struct {
	deployment, group, id string
	index                 int
	jobDir                string
	files                 map[string]string
	staged                map[string]string
	backups               map[string]string
	raws                  map[string]rawEntry
	// lead and tail are what discovery prints for every match, in its
	// letters. They default to - and n.
	lead, tail string
	// discovery, when set, is what discovery prints.
	discovery string
}

// rawEntry is the raw hash of a fake path's bytes, which holds while the
// path keeps the normalised hash norm.
type rawEntry struct{ norm, raw string }

// fakeRaw is the raw hash of a fake file that no operation wrote, such
// as a target as the test set it up or changed it.
func fakeRaw(norm string) string { return certpatch.RawHash([]byte("raw:" + norm)) }

// rawOf returns the raw hash of path in norms, the files, staged, or
// backups map: the one an operation recorded while the path still holds
// what it wrote, and fakeRaw of its normalised hash otherwise.
func (v *fakeVM) rawOf(norms map[string]string, path string) string {
	norm, ok := norms[path]
	if !ok {
		return ""
	}

	if entry, ok := v.raws[path]; ok && entry.norm == norm {
		return entry.raw
	}

	return fakeRaw(norm)
}

// put records that path in norms now holds content with the normalised
// hash norm and the raw hash raw.
func (v *fakeVM) put(norms map[string]string, path, norm, raw string) {
	norms[path] = norm
	v.raws[path] = rawEntry{norm: norm, raw: raw}
}

func (v *fakeVM) name() string { return v.group + "/" + v.id }

func (v *fakeVM) certPath() string { return v.jobDir + "/config/tls/redis.crt" }

func (v *fakeVM) keyPath() string { return v.jobDir + "/config/tls/redis.key" }

// patchCall is one command a fake session ran.
type patchCall struct {
	session int
	vm      string
	op      string
	args    []string
	// line is the command line, as the runner would build it.
	line  string
	stdin int
}

// fakePatch is a scripted PatchRunner. Each session it opens runs
// commands against the fake VMs of the deployment and records them.
type fakePatch struct {
	mu        sync.Mutex
	vms       map[string][]*fakeVM
	opens     []string
	closes    int
	calls     []patchCall
	gateNodes int
	// fingerprint is what verify.sh prints.
	fingerprint string
	// hook, when it reports true, answers a command in place of the fake.
	hook func(call patchCall) (ssh.Result, error, bool)
	// openErr fails the next open.
	openErr error
}

func newFakePatch() *fakePatch {
	return &fakePatch{vms: make(map[string][]*fakeVM)}
}

// addVM adds a VM whose certificate and key files hold the hashes given.
func (f *fakePatch) addVM(deployment, group, id string, index int, certHash, keyHash string) *fakeVM {
	f.mu.Lock()
	defer f.mu.Unlock()

	vm := &fakeVM{
		deployment: deployment, group: group, id: id, index: index,
		jobDir:  "/var/vcap/data/jobs/redis/0a1b2c3d",
		files:   make(map[string]string),
		staged:  make(map[string]string),
		backups: make(map[string]string),
		raws:    make(map[string]rawEntry),
		lead:    "-",
		tail:    "n",
	}
	vm.files[vm.certPath()] = certHash
	vm.files[vm.keyPath()] = keyHash
	f.vms[deployment] = append(f.vms[deployment], vm)

	return vm
}

func (f *fakePatch) runner(_ context.Context, deployment, group string, indices []int) (PatchSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if err := f.openErr; err != nil {
		f.openErr = nil

		return nil, err
	}

	f.opens = append(f.opens, group)
	sess := &fakeSession{fake: f, id: len(f.opens)}

	for _, index := range indices {
		for _, vm := range f.vms[deployment] {
			if vm.group == group && vm.index == index {
				sess.vms = append(sess.vms, vm)
				sess.hosts = append(sess.hosts, sshconn.Host{Job: group, IndexOrID: vm.id, Address: "10.0.0." + strconv.Itoa(10+index)})
			}
		}
	}

	return sess, nil
}

func (f *fakePatch) openCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return len(f.opens)
}

func (f *fakePatch) closeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.closes
}

func (f *fakePatch) recorded() []patchCall {
	f.mu.Lock()
	defer f.mu.Unlock()

	return slices.Clone(f.calls)
}

// ops returns the operations run on one VM, in order.
func (f *fakePatch) ops(vm string) []string {
	var ops []string

	for _, call := range f.recorded() {
		if call.vm == vm {
			ops = append(ops, call.op)
		}
	}

	return ops
}

func (f *fakePatch) setHook(hook func(call patchCall) (ssh.Result, error, bool)) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.hook = hook
}

// fakeSession is one session of the fake.
type fakeSession struct {
	fake  *fakePatch
	id    int
	hosts []sshconn.Host
	vms   []*fakeVM
}

func (s *fakeSession) VMs() []sshconn.Host { return slices.Clone(s.hosts) }

func (s *fakeSession) Close() error {
	s.fake.mu.Lock()
	defer s.fake.mu.Unlock()

	s.fake.closes++

	return nil
}

func (s *fakeSession) Run(ctx context.Context, vm int, op string, args []string, stdin io.Reader) (ssh.Result, error) {
	var input []byte

	if stdin != nil {
		read, err := io.ReadAll(stdin)
		if err != nil {
			return ssh.Result{}, fmt.Errorf("reading stdin: %w", err)
		}

		input = read
	}

	if vm < 0 || vm >= len(s.vms) {
		return ssh.Result{}, fmt.Errorf("no VM %d", vm)
	}

	program, argv := certscripts.Invocation(op, args...)
	if op == opSudoCheck {
		program, argv = certscripts.SudoCheckCommand, nil
	}

	call := patchCall{session: s.id, vm: s.vms[vm].name(), op: op, args: slices.Clone(args), line: ssh.Command(program, argv), stdin: len(input)}

	s.fake.mu.Lock()
	s.fake.calls = append(s.fake.calls, call)
	hook := s.fake.hook
	s.fake.mu.Unlock()

	if hook != nil {
		if result, err, ok := hook(call); ok {
			return result, err
		}
	}

	if err := ctx.Err(); err != nil {
		return ssh.Result{}, fmt.Errorf("%w: %w", ssh.ErrRunnerClosed, err)
	}

	s.fake.mu.Lock()
	defer s.fake.mu.Unlock()

	return s.fake.answer(s.vms[vm], op, args, input), nil
}

func okResult(lines ...string) ssh.Result {
	return ssh.Result{Stdout: []byte(strings.Join(lines, "\n") + "\n")}
}

func exitResult(code int, lines ...string) ssh.Result {
	result := okResult(lines...)
	result.ExitCode = code

	return result
}

// answer runs one operation against the VM's state. The caller holds mu.
//
//nolint:cyclop,funlen // one case per operation keeps the fake readable
func (f *fakePatch) answer(vm *fakeVM, op string, args []string, input []byte) ssh.Result {
	arg := func(i int) string {
		if i < len(args) {
			return args[i]
		}

		return ""
	}

	switch op {
	case opSudoCheck:
		return ssh.Result{}
	case "discover":
		return okResult(f.discover(vm, args))
	case certscripts.GateRedisCluster:
		nodes := strconv.Itoa(f.gateNodes)

		return okResult("cluster_state:ok", "cluster_slots_assigned:16384", "cluster_slots_ok:16384",
			"cluster_slots_pfail:0", "cluster_slots_fail:0", "cluster_known_nodes:"+nodes, "cluster_size:"+nodes)
	case "verify":
		return okResult("fingerprint=" + f.fingerprint)
	case "prepare":
		return okResult("ok path=" + patchWorkRoot + arg(0))
	case "cleanup":
		// The task's work directory goes, with every staged copy and
		// backup in it.
		dropUnder(vm.staged, patchWorkRoot+arg(0)+"/")
		dropUnder(vm.backups, patchWorkRoot+arg(0)+"/")

		return okResult("ok path=" + patchWorkRoot + arg(0))
	case "stage":
		// Staging refuses a name that already exists.
		if _, exists := vm.staged[arg(1)]; exists {
			return exitResult(certscripts.ExitRefused)
		}

		script, _ := certscripts.Script("patch")
		content := input[min(len(script), len(input)):]
		vm.put(vm.staged, arg(1), certpatch.NormalisedHash(content), certpatch.RawHash(content))

		return okResult("ok path=" + arg(1))
	}

	return f.answerFile(vm, op, args)
}

// answerFile runs one operation on the VM's files, by the raw hashes the
// broker sent or, without them, by normalised hashes, as patch.sh does.
// The caller holds mu.
//
//nolint:cyclop,funlen,gocognit // one case per operation keeps the fake readable
func (f *fakePatch) answerFile(fake *fakeVM, operation string, args []string) ssh.Result {
	arg := func(i int) string {
		if i < len(args) {
			return args[i]
		}

		return ""
	}

	// The argument counts without and with raw hashes, and where the
	// raw hashes start.
	counts := map[string][3]int{"check": {6, 8, 6}, "stagecheck": {3, 5, 3}, "backup": {5, 6, 5}, "replace": {6, 9, 6}, "restore": {6, 8, 6}}

	count, known := counts[operation]
	if !known {
		return f.answerRest(fake, operation, args)
	}

	raw := len(args) == count[1]
	if !raw && len(args) != count[0] {
		return exitResult(certscripts.ExitUsage)
	}

	// hashOf is what the operation compares for path in norms, and want
	// the hash it compares with, the raw one at i or the normalised one
	// at n.
	hashOf := func(norms map[string]string, path string) string {
		if raw {
			return fake.rawOf(norms, path)
		}

		return norms[path]
	}
	want := func(n, i int) string {
		if raw {
			return arg(count[2] + i)
		}

		return arg(n)
	}

	switch operation {
	case "stagecheck":
		if fake.staged[arg(1)] != arg(2) || (raw && fake.rawOf(fake.staged, arg(1)) != arg(3)) {
			return exitResult(certscripts.ExitDrift, "drift path="+arg(1))
		}

		return okResult("ok path=" + arg(1))
	case "check":
		state := map[string]string{want(3, 0): "old", want(4, 1): "new"}[hashOf(fake.files, arg(2))]
		if state == "" {
			return exitResult(certscripts.ExitDrift, "drift path="+arg(2))
		}

		_, ok := fake.backups[arg(5)]

		switch {
		case !ok:
			return okResult("ok path=" + arg(2) + " state=" + state + " backup=none")
		case hashOf(fake.backups, arg(5)) != want(3, 0):
			return exitResult(certscripts.ExitBackup, "drift path="+arg(5))
		}

		return okResult("ok path=" + arg(2) + " state=" + state + " backup=old")
	case "backup":
		// A backup already made is kept when it holds the old content.
		if _, exists := fake.backups[arg(4)]; exists {
			if hashOf(fake.backups, arg(4)) != want(3, 0) {
				return exitResult(certscripts.ExitBackup, "drift path="+arg(4))
			}

			return okResult("ok path=" + arg(4) + " action=exists")
		}

		if hashOf(fake.files, arg(2)) != want(3, 0) {
			return exitResult(certscripts.ExitDrift, "drift path="+arg(2))
		}

		fake.put(fake.backups, arg(4), fake.files[arg(2)], fake.rawOf(fake.files, arg(2)))

		return okResult("ok path=" + arg(4) + " action=created")
	case "replace":
		switch hashOf(fake.files, arg(2)) {
		case want(5, 1):
			return okResult("ok path=" + arg(2) + " state=new action=unchanged")
		case want(4, 0):
			if fake.staged[arg(3)] != arg(5) || (raw && fake.rawOf(fake.staged, arg(3)) != want(5, 1)) {
				return exitResult(certscripts.ExitDrift, "drift path="+arg(3))
			}

			fake.put(fake.files, arg(2), fake.staged[arg(3)], fake.rawOf(fake.staged, arg(3)))

			return okResult("ok path=" + arg(2) + " state=new action=replaced")
		}

		return exitResult(certscripts.ExitDrift, "drift path="+arg(2))
	}

	// restore. A file that holds the old content again needs no backup.
	switch hashOf(fake.files, arg(2)) {
	case want(4, 0):
		return okResult("ok path=" + arg(2) + " state=old action=unchanged")
	case want(5, 1):
	default:
		return exitResult(certscripts.ExitDrift, "unrecovered path="+arg(2))
	}

	if fake.backups[arg(3)] != arg(4) || hashOf(fake.backups, arg(3)) != want(4, 0) {
		return exitResult(certscripts.ExitBackup, "unrecovered path="+arg(2))
	}

	fake.put(fake.files, arg(2), fake.backups[arg(3)], fake.rawOf(fake.backups, arg(3)))

	return okResult("ok path=" + arg(2) + " state=old action=restored")
}

// answerRest runs the operations that are neither staging nor a file's,
// which leaves restart.
func (f *fakePatch) answerRest(_ *fakeVM, op string, args []string) ssh.Result {
	if op != "restart" {
		return exitResult(certscripts.ExitUsage)
	}

	lines := make([]string, 0, len(args))
	for _, process := range args[1:] {
		lines = append(lines, "ok name="+process)
	}

	return okResult(lines...)
}

// dropUnder deletes every path under the prefix.
func dropUnder(paths map[string]string, prefix string) {
	for path := range paths {
		if strings.HasPrefix(path, prefix) {
			delete(paths, path)
		}
	}
}

// discover prints what discover.sh would for the VM's files that hold one
// of the hashes asked for.
func (f *fakePatch) discover(vm *fakeVM, hashes []string) string {
	if vm.discovery != "" {
		return vm.discovery
	}

	lines := []string{"jobdir job=redis path=" + vm.jobDir}

	paths := make([]string, 0, len(vm.files))
	for path := range vm.files {
		paths = append(paths, path)
	}

	sort.Strings(paths)

	for _, path := range paths {
		if slices.Contains(hashes, vm.files[path]) {
			lines = append(lines, "match job=redis path="+path+" hash="+vm.files[path]+" raw="+vm.rawOf(vm.files, path)+
				" lead="+vm.lead+" tail="+vm.tail+" owner=root:vcap mode=0640 rootdirs=yes")
		}
	}

	lines = append(lines, "proc job=redis name=redis")

	return strings.Join(lines, "\n")
}

// patchPreviewEnv is a preview environment set up for patch mode. Each
// instance's VMs run, have addresses, and hold the leaf's deployed pair,
// and the 3-VM instance has the Redis cluster gate and a TLS listener.
type patchPreviewEnv struct {
	*previewEnv

	patch *fakePatch
	pairs *pairCredHub
	old   testPair
}

func newPatchPreviewEnv(t *testing.T) *patchPreviewEnv {
	t.Helper()

	env := &patchPreviewEnv{previewEnv: newPreviewEnv(t), patch: newFakePatch()}
	env.pairs = newPairCredHub(env.deps.CredHub)
	env.old, _ = issuePair(t, env.ca, baseSpec())
	env.pairs.set("v1", env.old)
	env.patch.gateNodes = 3

	certHash, keyHash := certpatch.NormalisedHash(env.old.cert), certpatch.NormalisedHash(env.old.key)

	env.mu.Lock()

	for id, scan := range env.scans {
		for i := range scan.VMs {
			vm := &scan.VMs[i]
			vm.ID = "id-" + vm.CID
			vm.Index = i
			vm.IPs = []string{"10.0.0." + strconv.Itoa(10+i)}
			env.patch.addVM(scan.Ref.Deployment, vm.Job, vm.ID, i, certHash, keyHash)
		}

		scan.HealthGate = certscripts.GateFor("redis", len(scan.VMs))
		scan.Facts = redisFacts()
		env.scans[id] = scan
	}

	env.mu.Unlock()

	env.deps.CredHub = env.pairs
	env.deps.PatchRunner = env.patch.runner
	env.configure(t, nil)

	return env
}

// redisFacts is a manifest whose redis job serves redis_crt over TLS.
func redisFacts() *certinventory.ManifestFacts {
	return &certinventory.ManifestFacts{Jobs: []certinventory.JobFacts{{
		Group: "redis", Job: "redis",
		Properties: map[string]interface{}{"redis": map[string]interface{}{"tls": map[string]interface{}{"enabled": true}}},
		CertRefs:   map[string]string{"redis.tls.certificate": "redis_crt"},
	}}}
}

// editScan changes the stored scan of an instance.
func (e *patchPreviewEnv) editScan(instanceID string, change func(scan *certinventory.InstanceScan)) {
	e.mu.Lock()
	defer e.mu.Unlock()

	scan := e.scans[instanceID]
	change(&scan)
	e.scans[instanceID] = scan
}

// patchPreview asks for a patch of the leaf of each instance.
func patchPreview(instanceIDs ...string) PreviewRequest {
	req := redeploy(instanceIDs...)
	req.Mode = RotationModePatch
	req.Converge = ConvergePending

	return req
}

func TestPatchPreviewListsConsumersAndGate(t *testing.T) {
	t.Parallel()

	env := newPatchPreviewEnv(t)

	inst := previewOf(t, env.previewEnv, patchPreview(waitInstanceID)).Instances[0]
	if inst.Blocked != nil {
		t.Fatalf("expected the instance to be patchable, got %+v", inst.Blocked)
	}

	if inst.Gate != certscripts.GateRedisCluster || len(inst.Consumers) != 3 {
		t.Fatalf("expected the cluster gate and three consumers, got %q and %+v", inst.Gate, inst.Consumers)
	}

	// Consumers follow BOSH index order, and vm-c holds index 0.
	want := PreviewConsumer{VM: "redis/id-vm-c", Job: "redis", Files: 2, Verification: "probe 6379"}
	if inst.Consumers[0] != want {
		t.Fatalf("expected %+v, got %+v", want, inst.Consumers[0])
	}

	if !strings.Contains(inst.Impact, "Each of 3 VMs restarts redis in turn") || !strings.Contains(inst.Impact, "CLUSTER INFO") {
		t.Fatalf("expected the impact to name the VMs, the process, and the gate, got %q", inst.Impact)
	}

	if env.patch.openCount() != 1 || env.patch.closeCount() != 1 {
		t.Fatalf("expected one session opened and closed, got %d and %d", env.patch.openCount(), env.patch.closeCount())
	}

	for _, call := range env.patch.recorded() {
		if call.op != opSudoCheck && call.op != "discover" && call.op != certscripts.GateRedisCluster {
			t.Fatalf("expected the preview only to read, got %s on %s", call.op, call.vm)
		}
	}

	env.pairs.expectAllZeroed(t, "after the preview")
}

func TestPatchPreviewBlocksStoppedVMs(t *testing.T) {
	t.Parallel()

	env := newPatchPreviewEnv(t)
	env.editScan(waitInstanceID, func(scan *certinventory.InstanceScan) { scan.VMs[1].CID = "" })

	inst := previewOf(t, env.previewEnv, patchPreview(waitInstanceID)).Instances[0]
	if inst.Blocked == nil || inst.Blocked.Code != apierr.CodeVMsNotRunning {
		t.Fatalf("expected vms_not_running, got %+v", inst.Blocked)
	}

	redeployed := previewOf(t, env.previewEnv, redeploy(waitInstanceID)).Instances[0]
	if redeployed.Blocked != nil {
		t.Fatalf("expected the redeploy preview not to be blocked, got %+v", redeployed.Blocked)
	}

	if env.patch.openCount() != 0 {
		t.Fatalf("expected no SSH session for a stopped VM, got %d", env.patch.openCount())
	}
}

func TestPatchPreviewBlocksVMWithoutAddress(t *testing.T) {
	t.Parallel()

	env := newPatchPreviewEnv(t)
	env.editScan(waitInstanceID, func(scan *certinventory.InstanceScan) { scan.VMs[2].IPs = nil })

	inst := previewOf(t, env.previewEnv, patchPreview(waitInstanceID)).Instances[0]
	if inst.Blocked == nil || inst.Blocked.Code != apierr.CodeVMsNotRunning {
		t.Fatalf("expected vms_not_running, got %+v", inst.Blocked)
	}
}

func TestPatchPreviewNoHealthGate(t *testing.T) {
	t.Parallel()

	env := newPatchPreviewEnv(t)
	env.editScan(waitInstanceID, func(scan *certinventory.InstanceScan) { scan.HealthGate = "" })

	inst := previewOf(t, env.previewEnv, patchPreview(waitInstanceID)).Instances[0]
	if inst.Blocked == nil || inst.Blocked.Code != apierr.CodeNoHealthGate {
		t.Fatalf("expected no_health_gate, got %+v", inst.Blocked)
	}

	if env.patch.openCount() != 0 {
		t.Fatalf("expected no SSH session without a gate, got %d", env.patch.openCount())
	}
}

func TestPatchPreviewEmbeddedBlocks(t *testing.T) {
	t.Parallel()

	env := newPatchPreviewEnv(t)
	certHash := certpatch.NormalisedHash(env.old.cert)
	conf := "/var/vcap/data/jobs/redis/0a1b2c3d/config/redis.conf"

	env.patch.mu.Lock()
	vm := env.patch.vms[waitDeployment][1]
	vm.discovery = strings.Join([]string{
		"jobdir job=redis path=" + vm.jobDir,
		"embedded job=redis path=" + conf + " hash=" + certHash,
		"proc job=redis name=redis",
	}, "\n")
	env.patch.mu.Unlock()

	inst := previewOf(t, env.previewEnv, patchPreview(waitInstanceID)).Instances[0]
	if inst.Blocked == nil || inst.Blocked.Code != apierr.CodePatchUnsupported {
		t.Fatalf("expected patch_unsupported, got %+v", inst.Blocked)
	}

	if !strings.Contains(inst.Blocked.Message, conf) {
		t.Fatalf("expected the message to name %s, got %q", conf, inst.Blocked.Message)
	}

	if env.patch.closeCount() != env.patch.openCount() {
		t.Fatalf("expected every session closed, got %d opened and %d closed", env.patch.openCount(), env.patch.closeCount())
	}
}

func TestPatchPreviewNoPasswordlessSudoBlocks(t *testing.T) {
	t.Parallel()

	env := newPatchPreviewEnv(t)
	env.patch.setHook(func(call patchCall) (ssh.Result, error, bool) {
		if call.op == opSudoCheck {
			return exitResult(1), nil, true
		}

		return ssh.Result{}, nil, false
	})

	inst := previewOf(t, env.previewEnv, patchPreview(waitInstanceID)).Instances[0]
	if inst.Blocked == nil || inst.Blocked.Code != apierr.CodePatchUnsupported || !strings.Contains(inst.Blocked.Message, "passwordless sudo") {
		t.Fatalf("expected patch_unsupported for sudo, got %+v", inst.Blocked)
	}
}

func TestPatchPreviewSingleVMImpact(t *testing.T) {
	t.Parallel()

	env := newPatchPreviewEnv(t)

	inst := previewOf(t, env.previewEnv, patchPreview(previewInstance2)).Instances[0]
	if inst.Blocked != nil || inst.Gate != gateSingleVM {
		t.Fatalf("expected a clear single-vm patch, got %+v", inst)
	}

	if !strings.Contains(inst.Impact, "only VM") || !strings.Contains(inst.Impact, "brief outage") {
		t.Fatalf("expected the impact to warn of a brief outage, got %q", inst.Impact)
	}

	for _, call := range env.patch.recorded() {
		if call.op == certscripts.GateRedisCluster {
			t.Fatal("expected no cluster gate on a single VM")
		}
	}
}

func TestPatchPreviewWithPatchOff(t *testing.T) {
	t.Parallel()

	env := newPatchPreviewEnv(t)
	env.setSwitches(true, false)

	_, err := env.manager.PreviewRotation(context.Background(), patchPreview(waitInstanceID))
	expectAPIError(t, err, http.StatusForbidden, apierr.CodePatchDisabled)

	if env.patch.openCount() != 0 {
		t.Fatalf("expected the runner factory never to be called, got %d", env.patch.openCount())
	}
}

// twoVariableInstance is an instance whose two selected variables are at
// v1 and v2.
func twoVariableInstance() *InstanceUpgrade {
	return &InstanceUpgrade{
		InstanceID: waitInstanceID, DeploymentName: waitDeployment,
		Certificates: &InstanceRotation{Variables: []VariableRotation{
			{Name: regenLeaf, OldVersionID: "v1"},
			{Name: regenLeaf2, OldVersionID: "v2"},
		}},
	}
}

func TestOldHashesZeroesPairs(t *testing.T) {
	t.Parallel()

	ca := newTestCA(t, "services-ca")
	first, _ := issuePair(t, ca, baseSpec())
	second, _ := issuePair(t, ca, baseSpec())

	for _, failSecond := range []bool{false, true} {
		pairs := newPairCredHub(nil)
		pairs.set("v1", first)
		pairs.set("v2", second)

		if failSecond {
			pairs.failRead("v2", errors.New("credhub is down"))
		}

		manager := NewManager(&logger.NoOpLogger{}, noCallDirector{}, newWaitVault())
		t.Cleanup(manager.Stop)

		err := manager.Configure(Deps{CredHub: pairs})
		if err != nil {
			t.Fatalf("Configure: %v", err)
		}

		certs, keys, err := manager.oldHashes(context.Background(), twoVariableInstance())

		switch {
		case failSecond && err == nil:
			t.Fatal("expected the failed read to fail the call")
		case !failSecond && err != nil:
			t.Fatalf("oldHashes: %v", err)
		case !failSecond && (certs[regenLeaf2] != certpatch.NormalisedHash(second.cert) || keys[regenLeaf] != certpatch.NormalisedHash(first.key)):
			t.Fatalf("expected the hashes of the pairs, got %v and %v", certs, keys)
		}

		if failSecond && strings.Contains(err.Error(), "PRIVATE") {
			t.Fatalf("expected no key in the error, got %q", err.Error())
		}

		pairs.expectAllZeroed(t, fmt.Sprintf("failSecond=%v", failSecond))
	}
}
