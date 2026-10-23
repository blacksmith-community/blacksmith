package upgrade

import (
	"bytes"
	"context"
	"crypto"
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
	"time"

	gossh "golang.org/x/crypto/ssh"

	"blacksmith/internal/apierr"
	"blacksmith/internal/bosh"
	"blacksmith/internal/bosh/ssh"
	"blacksmith/internal/bosh/sshconn"
	"blacksmith/internal/certinventory"
	"blacksmith/internal/certpatch"
	"blacksmith/internal/credhub"
	"blacksmith/internal/upgrade/certscripts"
	"blacksmith/pkg/logger"
)

// PatchSession runs the helper scripts on the VMs of one instance group
// through one SSH user. VMs lists the hosts the user was created on, and
// Run runs one operation on the VM at that place in the list. op is the
// script operation and args its arguments, which carry only operation
// names, validated paths, hashes, and task IDs. Every script, and every
// file's content, travels on stdin. Close ends every connection and
// removes the user.
type PatchSession interface {
	VMs() []sshconn.Host
	Run(ctx context.Context, vm int, op string, args []string, stdin io.Reader) (ssh.Result, error)
	Close() error
}

// opSudoCheck is the operation a PatchSession runs as the fixed
// certscripts.SudoCheckCommand, with no arguments and no stdin, rather
// than as a script.
const opSudoCheck = "sudo-check"

// The helper scripts discovery sends.
const (
	discoverScript = "discover"
	gateScript     = "gate"
)

// opDiscover is discover.sh's one operation.
const opDiscover = "discover"

// opRestart is patch.sh's restart, which runs under a longer timeout.
const opRestart = "restart"

const (
	// patchOutputLimit bounds what one command may print.
	patchOutputLimit = 64 << 10
	// patchCommandTimeout bounds one command on a VM.
	patchCommandTimeout = 2 * time.Minute
	// patchGateWaitDefault is how long a patched VM's health gate may
	// take to pass after its restart, and patchGateIntervalDefault the
	// time between two runs of it.
	patchGateWaitDefault     = 5 * time.Minute
	patchGateIntervalDefault = 10 * time.Second
	// patchRestartTimeout bounds a restart, which waits up to 300 seconds
	// on the VM for every process to run with a new pid.
	patchRestartTimeout = 6 * time.Minute
	// patchCleanupTimeout bounds the removal of a group's SSH user.
	patchCleanupTimeout = time.Minute
	// patchWorkRoot holds each task's private work directory on a VM.
	patchWorkRoot = "/var/vcap/data/certpatch/"
)

// errNoPatchRunner is what discovery returns when the broker cannot open
// SSH sessions.
var errNoPatchRunner = errors.New("blacksmith has no SSH runner for patch mode")

// patchDirector is what NewPatchRunner needs from the director.
type patchDirector interface {
	SSHGroupSession(ctx context.Context, deployment, group string, indices []int) (*sshconn.Session, error)
	DialSSHHost(ctx context.Context, session *sshconn.Session, host sshconn.Host) (*gossh.Client, error)
}

// NewPatchRunner returns the PatchRunner a broker uses. Each session sets
// up one SSH user on the instances of a group, connects to each VM the
// first time a command runs on it, and connects again after a command
// whose deadline closed the connection. A nil director gives a nil
// runner.
func NewPatchRunner(director patchDirector, log logger.Logger) func(ctx context.Context, deployment, group string, indices []int) (PatchSession, error) {
	if director == nil {
		return nil
	}

	return func(ctx context.Context, deployment, group string, indices []int) (PatchSession, error) {
		session, err := director.SSHGroupSession(ctx, deployment, group, indices)
		if err != nil {
			return nil, fmt.Errorf("setting up SSH on %s of %s: %w", group, deployment, err)
		}

		return &sshPatchSession{director: director, session: session, log: log, runners: make(map[int]*ssh.Runner)}, nil
	}
}

// sshPatchSession is a PatchSession over one SSH group session.
type sshPatchSession struct {
	director patchDirector
	session  *sshconn.Session
	log      logger.Logger

	mu      sync.Mutex
	runners map[int]*ssh.Runner
	closed  bool
}

func (s *sshPatchSession) VMs() []sshconn.Host {
	return slices.Clone(s.session.Hosts)
}

func (s *sshPatchSession) Run(ctx context.Context, vm int, op string, args []string, stdin io.Reader) (ssh.Result, error) {
	runner, err := s.runner(ctx, vm)
	if err != nil {
		return ssh.Result{}, err
	}

	program, argv := certscripts.Invocation(op, args...)
	if op == opSudoCheck {
		program, argv = certscripts.SudoCheckCommand, nil
	}

	result, err := runner.Run(ctx, program, argv, stdin, patchOutputLimit)
	if err != nil && (errors.Is(err, ssh.ErrRunnerClosed) || ctx.Err() != nil) {
		// A deadline closes the runner's connection, so the next command
		// on this VM connects again.
		s.drop(vm, runner)
	}

	return result, err
}

// runner returns the VM's runner, connecting to the VM when it has none.
func (s *sshPatchSession) runner(ctx context.Context, vm int) (*ssh.Runner, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil, ssh.ErrRunnerClosed
	}

	if vm < 0 || vm >= len(s.session.Hosts) {
		return nil, fmt.Errorf("the session has no VM %d", vm)
	}

	if runner, ok := s.runners[vm]; ok {
		return runner, nil
	}

	client, err := s.director.DialSSHHost(ctx, s.session, s.session.Hosts[vm])
	if err != nil {
		return nil, fmt.Errorf("connecting to %s: %w", s.session.Hosts[vm].Instance(), err)
	}

	runner := ssh.NewRunner(client, s.log)
	s.runners[vm] = runner

	return runner, nil
}

// drop closes a runner whose connection ended and forgets it.
func (s *sshPatchSession) drop(vm int, runner *ssh.Runner) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.runners[vm] == runner {
		delete(s.runners, vm)
	}

	_ = runner.Close()
}

// Close closes every connection and then removes the SSH user, on a
// context of its own so a caller whose context ended still removes it.
func (s *sshPatchSession) Close() error {
	s.mu.Lock()

	if s.closed {
		s.mu.Unlock()

		return nil
	}

	s.closed = true
	runners := s.runners
	s.runners = nil

	s.mu.Unlock()

	var errs []error

	for _, runner := range runners {
		err := runner.Close()
		if err != nil && !errors.Is(err, ssh.ErrRunnerClosed) {
			errs = append(errs, err)
		}
	}

	if s.session.Cleanup != nil {
		ctx, cancel := context.WithTimeout(context.Background(), patchCleanupTimeout)
		defer cancel()

		err := s.session.Cleanup(ctx)
		if err != nil {
			errs = append(errs, fmt.Errorf("removing the SSH user: %w", err))
		}
	}

	return errors.Join(errs...)
}

// patchSessions holds the sessions one rotation or preview opened, one
// per instance group, so every step of an instance shares them.
type patchSessions struct {
	open       func(ctx context.Context, deployment, group string, indices []int) (PatchSession, error)
	deployment string
	log        logger.Logger

	mu      sync.Mutex
	byGroup map[string]PatchSession
}

func (m *Manager) newPatchSessions(deployment string) *patchSessions {
	return &patchSessions{open: m.certDeps().PatchRunner, deployment: deployment, log: m.logger, byGroup: make(map[string]PatchSession)}
}

// group returns the group's session, opening it with the indices given
// the first time.
func (s *patchSessions) group(ctx context.Context, group string, indices []int) (PatchSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if sess, ok := s.byGroup[group]; ok {
		return sess, nil
	}

	if s.open == nil {
		return nil, errNoPatchRunner
	}

	sess, err := s.open(ctx, s.deployment, group, indices)
	if err != nil {
		return nil, err
	}

	if sess == nil {
		return nil, errNoPatchRunner
	}

	s.byGroup[group] = sess

	return sess, nil
}

// close closes every session it opened.
func (s *patchSessions) close() {
	s.mu.Lock()
	sessions := s.byGroup
	s.byGroup = make(map[string]PatchSession)
	s.mu.Unlock()

	for group, sess := range sessions {
		err := sess.Close()
		if err != nil && s.log != nil {
			s.log.Error("Closing the patch session of %s in %s: %v", group, s.deployment, err)
		}
	}
}

// vmID is the ID SSH hosts name a VM by, which is its instance ID, or its
// index when it has none.
func vmID(vm bosh.VM) string {
	if vm.ID != "" {
		return vm.ID
	}

	return strconv.Itoa(vm.Index)
}

// vmName names the VM as an operator does, group/id.
func vmName(vm bosh.VM) string {
	return vm.Job + "/" + vmID(vm)
}

// hostIndex finds the VM's host in the session by group and instance ID,
// never by its place in the list.
func hostIndex(sess PatchSession, group, id string) (int, error) {
	for index, host := range sess.VMs() {
		if host.Job == group && host.IndexOrID == id {
			return index, nil
		}
	}

	return 0, fmt.Errorf("the SSH session has no host for %s/%s", group, id)
}

// runScript sends the named script on stdin and runs its operation, under
// the operation's own timeout.
func runScript(ctx context.Context, sess PatchSession, vm int, script, op string, args ...string) (ssh.Result, error) {
	text, err := certscripts.Script(script)
	if err != nil {
		return ssh.Result{}, err
	}

	ctx, cancel := context.WithTimeout(ctx, commandTimeout(op))
	defer cancel()

	return sess.Run(ctx, vm, op, args, strings.NewReader(text))
}

// commandTimeout is how long one operation may run.
func commandTimeout(op string) time.Duration {
	if op == opRestart {
		return patchRestartTimeout
	}

	return patchCommandTimeout
}

// hashPairs reads the certificate and private key of each version,
// keyed by the variable's name, and returns their normalised hashes. It
// zeroes every pair it read before it returns, on every path. The key is
// read only to hash it.
func hashPairs(ctx context.Context, api CredHubAPI, versions map[string]string) (map[string]string, map[string]string, error) {
	if api == nil {
		return nil, nil, apierr.New(http.StatusForbidden, apierr.CodeCredHubNotConfigured, "CredHub is not configured, so certificates can't be read.")
	}

	names := make([]string, 0, len(versions))
	for name := range versions {
		names = append(names, name)
	}

	sort.Strings(names)

	certs := make(map[string]string, len(names))
	keys := make(map[string]string, len(names))

	for _, name := range names {
		certHash, keyHash, err := hashPair(ctx, api, name, versions[name])
		if err != nil {
			return nil, nil, err
		}

		certs[name] = certHash
		keys[name] = keyHash
	}

	return certs, keys, nil
}

// hashPair reads one version's pair, hashes both halves, and zeroes it.
func hashPair(ctx context.Context, api CredHubAPI, name, versionID string) (string, string, error) {
	pair, err := api.CertificateKeyPair(ctx, versionID)
	defer pair.Zero()

	switch {
	case err != nil:
		return "", "", fmt.Errorf("reading version %s of %s: %w", versionID, shortName(name), err)
	case pair == nil || len(pair.Certificate) == 0 || len(pair.PrivateKey) == 0:
		return "", "", fmt.Errorf("CredHub returned no certificate and key for version %s of %s", versionID, shortName(name))
	}

	return certpatch.NormalisedHash(pair.Certificate), certpatch.NormalisedHash(pair.PrivateKey), nil
}

// oldHashes reads each selected variable's deployed version and returns
// the normalised hashes of its certificate and its key, keyed by the
// variable's full name. Every pair it read is zeroed before it returns.
func (m *Manager) oldHashes(ctx context.Context, inst *InstanceUpgrade) (map[string]string, map[string]string, error) {
	m.mu.RLock()

	versions := make(map[string]string)

	if inst.Certificates != nil {
		for _, variable := range inst.Certificates.Variables {
			versions[variable.Name] = variable.OldVersionID
		}
	}

	m.mu.RUnlock()

	return hashPairs(ctx, m.certDeps().CredHub, versions)
}

// Consumer is one job on one VM that holds a selected variable's
// certificate and key files. Verification says how the patch checks it
// afterwards: "probe <port>" for a listener, or the unverified detail for
// a consumer no listener rule covers.
type Consumer struct {
	VM string
	certscripts.Consumer
	Verification string
}

// patchScope is what discovery reads about one instance.
type patchScope struct {
	// taskID names the work directory on each VM. A preview has none, and
	// its journals then carry no backup paths.
	taskID     string
	director   string
	deployment string
	gate       string
	facts      *certinventory.ManifestFacts
	vms        []bosh.VM
	// variables are the selected variables' full names.
	variables []string
}

// patchEligible checks what discovery needs before it reads CredHub or
// opens SSH: every VM runs with a CID and an address, and a cluster has a
// health gate.
func patchEligible(scope patchScope) error {
	if len(scope.vms) == 0 {
		return apierr.New(http.StatusConflict, apierr.CodeVMsNotRunning, "The instance has no VMs, so there is nothing to patch.")
	}

	for _, vm := range scope.vms {
		if vm.CID == "" || len(vm.IPs) == 0 {
			return apierr.New(http.StatusConflict, apierr.CodeVMsNotRunning,
				fmt.Sprintf("The VM %s is not running, so the instance can't be patched in place. Start it or redeploy the instance.", vmName(vm)))
		}
	}

	if len(scope.vms) > 1 && (scope.gate == "" || scope.gate == gateSingleVM) {
		return apierr.New(http.StatusConflict, apierr.CodeNoHealthGate,
			fmt.Sprintf("This service has no health gate for a cluster of %d VMs, so Blacksmith can't tell when one VM is safe to leave. Redeploy the instance instead.", len(scope.vms)))
	}

	return nil
}

// patchUnsupported reports a discovery failure under patch_unsupported.
func patchUnsupported(err error) error {
	var apiErr *apierr.Error
	if errors.As(err, &apiErr) {
		return err
	}

	return apierr.New(http.StatusConflict, apierr.CodePatchUnsupported,
		fmt.Sprintf("The instance can't be patched in place, so redeploy it instead: %v", err))
}

// sortedVMs returns the VMs in group and index order.
func sortedVMs(vms []bosh.VM) []bosh.VM {
	out := slices.Clone(vms)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Job != out[j].Job {
			return out[i].Job < out[j].Job
		}

		return out[i].Index < out[j].Index
	})

	return out
}

// groupIndices returns the indices of the group's VMs.
func groupIndices(vms []bosh.VM, group string) []int {
	var indices []int

	for _, vm := range vms {
		if vm.Job == group {
			indices = append(indices, vm.Index)
		}
	}

	return indices
}

// discoverInstance checks the instance can be patched, then on each VM in
// turn checks passwordless sudo, runs discover.sh with the old hashes,
// reads its consumers, and runs the gate's pre-check. Every variable must
// have a consumer on some VM, and a VM with none is left out. It returns
// a planned journal for each VM it would patch and every consumer. It
// only reads, and it opens one session per group through sessions.
func (m *Manager) discoverInstance(ctx context.Context, scope patchScope, sessions *patchSessions, oldCert, oldKey map[string]string) ([]VMJournal, []Consumer, error) {
	err := patchEligible(scope)
	if err != nil {
		return nil, nil, err
	}

	hashes := wantedHashes(oldCert, oldKey)
	listeners := certinventory.ListenerRules(scope.facts)
	vms := sortedVMs(scope.vms)
	perVM := make(map[string][]certscripts.Consumer, len(vms))

	var (
		journals  []VMJournal
		consumers []Consumer
	)

	for _, vm := range vms {
		disc, found, err := m.discoverVM(ctx, scope, sessions, vm, hashes, oldCert, oldKey)
		if err != nil {
			return nil, nil, patchUnsupported(err)
		}

		perVM[vmName(vm)] = found

		if len(found) == 0 {
			continue
		}

		journal := planJournal(scope, vm, disc, found, oldCert, oldKey, listeners)
		journals = append(journals, journal)

		for _, consumer := range found {
			consumers = append(consumers, Consumer{VM: vmName(vm), Consumer: consumer, Verification: verificationOf(journal, consumer)})
		}
	}

	err = certscripts.RequireConsumers(scope.variables, perVM)
	if err != nil {
		return nil, nil, patchUnsupported(err)
	}

	return journals, consumers, nil
}

// discoverVM runs discovery and the gate's pre-check on one VM.
func (m *Manager) discoverVM(ctx context.Context, scope patchScope, sessions *patchSessions, vm bosh.VM, hashes []string, oldCert, oldKey map[string]string) (*certscripts.Discovery, []certscripts.Consumer, error) {
	name := vmName(vm)

	sess, err := sessions.group(ctx, vm.Job, groupIndices(scope.vms, vm.Job))
	if err != nil {
		return nil, nil, err
	}

	host, err := hostIndex(sess, vm.Job, vmID(vm))
	if err != nil {
		return nil, nil, err
	}

	sudoCtx, cancel := context.WithTimeout(ctx, patchCommandTimeout)
	result, err := sess.Run(sudoCtx, host, opSudoCheck, nil, nil)

	cancel()

	switch {
	case err != nil:
		return nil, nil, fmt.Errorf("the sudo check on %s did not finish: %w", name, err)
	case result.ExitCode != 0:
		return nil, nil, fmt.Errorf("%w on %s", certscripts.ErrNoPasswordlessSudo, name)
	}

	result, err = runScript(ctx, sess, host, discoverScript, opDiscover, hashes...)

	switch {
	case err != nil:
		return nil, nil, fmt.Errorf("discovery on %s did not finish: %w", name, err)
	case result.ExitCode != 0:
		return nil, nil, fmt.Errorf("discovery on %s failed with exit status %d", name, result.ExitCode)
	case result.Truncated:
		return nil, nil, fmt.Errorf("discovery on %s printed more than it should", name)
	}

	disc, err := certscripts.ParseDiscovery(result.Stdout)
	if err != nil {
		return nil, nil, fmt.Errorf("discovery on %s: %w", name, err)
	}

	disc.VM = name

	found, err := certscripts.BuildConsumers(disc, oldCert, oldKey)
	if err != nil {
		return nil, nil, err
	}

	err = m.runGate(ctx, sess, host, scope.gate, len(scope.vms))
	if err != nil {
		return nil, nil, fmt.Errorf("the health gate did not pass on %s before the patch: %w", name, err)
	}

	return disc, found, nil
}

// runGate runs the instance's health gate on one VM. A single VM has no
// gate to run.
func (m *Manager) runGate(ctx context.Context, sess PatchSession, host int, gate string, vmCount int) error {
	if gate == "" || gate == gateSingleVM {
		return nil
	}

	result, err := runScript(ctx, sess, host, gateScript, gate)

	switch {
	case err != nil:
		return fmt.Errorf("the gate did not finish: %w", err)
	case result.Truncated:
		return fmt.Errorf("%w: the gate printed more than it should", certscripts.ErrGateFailed)
	case gate == certscripts.GateRabbitMQCluster:
		return certscripts.ParseRabbitGate(result.Stdout)
	case result.ExitCode != 0:
		return fmt.Errorf("%w: the gate exited %d", certscripts.ErrGateFailed, result.ExitCode)
	default:
		return certscripts.ParseClusterInfo(result.Stdout, vmCount)
	}
}

// wantedHashes returns every old hash once, sorted.
func wantedHashes(oldCert, oldKey map[string]string) []string {
	seen := make(map[string]bool)

	var hashes []string

	for _, set := range []map[string]string{oldCert, oldKey} {
		for _, hash := range set {
			if !seen[hash] {
				seen[hash] = true
				hashes = append(hashes, hash)
			}
		}
	}

	sort.Strings(hashes)

	return hashes
}

// planJournal builds a planned journal for one VM from its consumers.
// Each consumer adds its certificate file and then its key file, and the
// file at place n is staged at s/n and backed up at b/n of the task's
// work directory.
func planJournal(scope patchScope, vm bosh.VM, disc *certscripts.Discovery, found []certscripts.Consumer, oldCert, oldKey map[string]string, listeners []certinventory.Listener) VMJournal {
	journal := VMJournal{
		VM:      vmName(vm),
		CID:     vm.CID,
		Group:   vm.Job,
		Index:   vm.Index,
		JobDirs: make(map[string]string),
		Phase:   VMPhasePlanned,
	}

	matches := make(map[string]certscripts.Match, len(disc.Matches))
	for _, match := range disc.Matches {
		matches[match.Path] = match
	}

	processes := make(map[string]bool)
	jobs := make(map[string]bool)

	for _, consumer := range found {
		jobs[consumer.Job] = true
		journal.JobDirs[consumer.Job] = disc.JobDirs[consumer.Job]

		for _, process := range consumer.Processes {
			processes[process] = true
		}

		for _, file := range []struct{ path, role, hash string }{
			{consumer.CertPath, FileRoleCertificate, oldCert[consumer.Variable]},
			{consumer.KeyPath, FileRoleKey, oldKey[consumer.Variable]},
		} {
			match := matches[file.path]
			journal.Files = append(journal.Files, FileEntry{
				Path:       file.path,
				Job:        consumer.Job,
				Role:       file.role,
				Variable:   consumer.Variable,
				Owner:      match.Owner,
				Mode:       match.Mode,
				OldHash:    file.hash,
				BackupPath: workPath(scope.taskID, "b", len(journal.Files)),
				State:      FileStatePlanned,
			})
		}
	}

	for process := range processes {
		journal.Processes = append(journal.Processes, process)
	}

	sort.Strings(journal.Processes)

	journal.Targets = planTargets(scope, vm.Job, jobs, found, listeners)

	return journal
}

// planTargets lists the listeners of the VM's group whose jobs consume a
// selected variable on it.
func planTargets(scope patchScope, group string, jobs map[string]bool, found []certscripts.Consumer, listeners []certinventory.Listener) []TargetResult {
	held := make(map[string]bool)
	for _, consumer := range found {
		held[consumer.Variable] = true
	}

	seen := make(map[string]bool)

	var targets []TargetResult

	for _, listener := range listeners {
		if listener.Group != group || !jobs[listener.Job] || listener.Variable == "" || listener.Port <= 0 {
			continue
		}

		variable := certinventory.ResolveCredHubName(scope.director, scope.deployment, listener.Variable)
		key := variable + ":" + strconv.Itoa(listener.Port)

		if !held[variable] || seen[key] {
			continue
		}

		seen[key] = true
		targets = append(targets, TargetResult{Variable: variable, Port: listener.Port, Method: verifyMethodProbe})
	}

	return targets
}

// workPath is the place-n staging ("s") or backup ("b") path of the
// task's work directory, or "" for a preview, which has no task.
func workPath(taskID, kind string, n int) string {
	if taskID == "" {
		return ""
	}

	return patchWorkRoot + taskID + "/" + kind + "/" + strconv.Itoa(n)
}

// verificationOf says how the patch checks a consumer afterwards.
func verificationOf(journal VMJournal, consumer certscripts.Consumer) string {
	var ports []string

	for _, target := range journal.Targets {
		if target.Variable == consumer.Variable {
			ports = append(ports, strconv.Itoa(target.Port))
		}
	}

	if len(ports) == 0 {
		return unverifiedDetail
	}

	return "probe " + strings.Join(ports, ", ")
}

// previewConsumers turns discovery's consumers into the preview's list.
func previewConsumers(consumers []Consumer) []PreviewConsumer {
	out := make([]PreviewConsumer, 0, len(consumers))

	for _, consumer := range consumers {
		out = append(out, PreviewConsumer{VM: consumer.VM, Job: consumer.Job, Files: 2, Verification: consumer.Verification})
	}

	return out
}

// patchImpact says in one sentence what a patch does to the instance:
// how many VMs restart, which processes, and what the next VM waits for.
func patchImpact(vms int, consumers []Consumer, gate string) string {
	seen := make(map[string]bool)

	var processes []string

	for _, consumer := range consumers {
		for _, process := range consumer.Processes {
			if !seen[process] {
				seen[process] = true
				processes = append(processes, process)
			}
		}
	}

	sort.Strings(processes)

	restarted := strings.Join(processes, ", ")

	if vms == 1 {
		return fmt.Sprintf("Restarting %s on the only VM causes a brief outage, because no other VM serves while it restarts.", restarted)
	}

	return fmt.Sprintf("Each of %d VMs restarts %s in turn, and the next VM waits until %s.", vms, restarted, gateCondition(gate))
}

// gateCondition says what a gate waits for.
func gateCondition(gate string) string {
	switch gate {
	case certscripts.GateRabbitMQCluster:
		return "RabbitMQ reports every disc node running"
	default:
		return "CLUSTER INFO reports a healthy cluster"
	}
}

// previewPatch runs discovery for a patch preview, which only reads, and
// adds the consumers and the impact, or blocks the instance.
func (m *Manager) previewPatch(ctx context.Context, inst *PreviewInstance, directorName string, scan certinventory.InstanceScan) {
	scope := patchScope{
		director:   directorName,
		deployment: scan.Ref.Deployment,
		gate:       inst.gate,
		facts:      scan.Facts,
		vms:        scan.VMs,
	}

	versions := make(map[string]string, len(inst.Selected))

	for _, variable := range inst.Selected {
		scope.variables = append(scope.variables, variable.Name)
		versions[variable.Name] = variable.DeployedVersionID
	}

	err := patchEligible(scope)
	if err != nil {
		*inst = blockWith(inst, err)

		return
	}

	oldCert, oldKey, err := hashPairs(ctx, m.certDeps().CredHub, versions)
	if err != nil {
		inst.failure = credhubUnavailable(purposePreview, err)

		return
	}

	sessions := m.newPatchSessions(scope.deployment)
	defer sessions.close()

	_, consumers, err := m.discoverInstance(ctx, scope, sessions, oldCert, oldKey)
	if err != nil {
		*inst = blockWith(inst, err)

		return
	}

	inst.Consumers = previewConsumers(consumers)
	inst.Impact = patchImpact(len(scan.VMs), consumers, inst.gate)
}

// blockWith blocks the instance with an API error's code and message.
func blockWith(inst *PreviewInstance, err error) PreviewInstance {
	var apiErr *apierr.Error
	if errors.As(patchUnsupported(err), &apiErr) {
		return inst.block(apiErr.Code, apiErr.Message)
	}

	return inst.block(apierr.CodePatchUnsupported, err.Error())
}

// patchPlan is what the patch preflight found.
type patchPlan struct {
	journals  []VMJournal
	consumers []Consumer
	gate      string
	vms       int
	// oldKeys maps each selected variable to the normalised hash of its
	// deployed private key.
	oldKeys map[string]string
}

// preflightPatch is the patch part of the preflight. It checks every VM
// runs and a cluster has a gate, reads the old hashes, and runs discovery
// with the gate's pre-check, which must find a consumer of every selected
// variable.
func (m *Manager) preflightPatch(ctx context.Context, task *UpgradeTask, inst *InstanceUpgrade, directorName string, scan certinventory.InstanceScan, names []string, sessions *patchSessions) (*patchPlan, error) {
	gate := scan.HealthGate
	if gate == "" && len(scan.VMs) == 1 {
		gate = gateSingleVM
	}

	scope := patchScope{
		taskID:     task.ID,
		director:   directorName,
		deployment: scan.Ref.Deployment,
		gate:       gate,
		facts:      scan.Facts,
		vms:        scan.VMs,
		variables:  names,
	}

	err := patchEligible(scope)
	if err != nil {
		return nil, err
	}

	oldCert, oldKey, err := m.oldHashes(ctx, inst)
	if err != nil {
		return nil, credhubUnavailable(purposePreflight, err)
	}

	journals, consumers, err := m.discoverInstance(ctx, scope, sessions, oldCert, oldKey)
	if err != nil {
		return nil, err
	}

	return &patchPlan{journals: journals, consumers: consumers, gate: gate, vms: len(scan.VMs), oldKeys: oldKey}, nil
}

// The script that changes files on a VM, and the operations the executor
// runs through it.
const (
	patchScript  = "patch"
	opPrepare    = "prepare"
	opStage      = "stage"
	opCheck      = "check"
	opStageCheck = "stagecheck"
	opBackup     = "backup"
	opReplace    = "replace"
	opRestore    = "restore"
	opCleanup    = "cleanup"
)

// errVMStopped marks a step on a VM that already paused or failed the
// task and saved why, so the caller stops the instance as it stands.
var errVMStopped = errors.New("the patch stopped on this VM")

// errKeyUnreadable is the only error a private key's parse returns, so no
// parser's message about the key's bytes travels further.
var errKeyUnreadable = errors.New("the private key could not be parsed")

// patchKinds is every kind of line patch.sh prints, with the keys each
// kind may carry.
var patchKinds = map[string]map[string]bool{
	"ok":          {"path": true, "state": true, "backup": true, "action": true, "name": true},
	"drift":       {"path": true, "name": true},
	"unrecovered": {"path": true},
}

// redeployToRepair ends the message of an instance a patch could not
// finish.
const redeployToRepair = "Redeploy this instance, which renders its jobs from BOSH state and repairs the VM."

// cancelledBetweenVMs is the message of an instance whose task was
// cancelled between two VMs of a patch.
const cancelledBetweenVMs = "The task was cancelled between two VMs of the patch. Any VM already patched keeps the new certificate, which the divergence record lists until the instance is redeployed."

// rotateInstancePatch rotates the instance's selected certificates by
// writing the new versions onto its VMs. It runs the preflight, which
// discovers every VM's consumers, and the BeforeRegenerate re-check,
// unless the regeneration already started, regenerates the variables,
// reads and validates each new key pair, and runs the BeforeMutation
// re-check. It then patches the VMs one at a time through one SSH session
// per instance group, which it opens once and closes when the instance
// ends. Every key pair it read is zeroed on every way out, after the
// sessions closed, so no command still under way reads a zeroed buffer.
// An instance whose VMs a pass already began to patch resumes, as
// resumePatch describes. It reports whether the instance settled.
func (m *Manager) rotateInstancePatch(ctx context.Context, task *UpgradeTask, inst *InstanceUpgrade, cancelCh <-chan struct{}) bool {
	m.mu.RLock()
	resume := inst.Certificates != nil && inst.Certificates.Phase == RotationPhasePatch
	m.mu.RUnlock()

	if resume {
		return m.resumePatch(ctx, task, inst, cancelCh)
	}

	pairs := make(map[string]*credhub.KeyPair)
	defer zeroPairs(pairs)

	sessions := m.newPatchSessions(inst.DeploymentName)
	defer sessions.close()

	if m.refuseWrites(ctx, task, inst) {
		return m.instanceSettled(inst)
	}

	if m.stopIfCancelled(ctx, task, inst, cancelledUnchanged) {
		return true
	}

	m.markRotating(ctx, task, inst)

	if !m.regenerationStarted(inst) {
		if m.patchPreflightStops(ctx, task, inst, sessions) {
			return m.instanceSettled(inst)
		}

		if m.stopIfCancelled(ctx, task, inst, cancelledUnchanged) {
			return true
		}

		if m.stopForChanges(ctx, task, inst, BeforeRegenerate) {
			return m.instanceSettled(inst)
		}

		if m.stopIfCancelled(ctx, task, inst, cancelledUnchanged) {
			return true
		}
	}

	stop, err := m.regenerateVariables(ctx, task, inst)
	if err != nil {
		m.logger.Error("Certificate task %s stopped regenerating instance %s: %v", task.ID, inst.InstanceID, err)
	}

	if stop || err != nil {
		return m.instanceSettled(inst)
	}

	if m.readNewPairs(ctx, task, inst, pairs) {
		return m.instanceSettled(inst)
	}

	if m.stopForChanges(ctx, task, inst, BeforeMutation) {
		return m.instanceSettled(inst)
	}

	return m.patchVMs(ctx, task, inst, sessions, pairs, cancelCh)
}

// zeroPairs zeroes every key pair in the map.
func zeroPairs(pairs map[string]*credhub.KeyPair) {
	for _, pair := range pairs {
		pair.Zero()
	}
}

// patchPreflightStops runs the preflight with the instance's sessions and
// saves the journals it planned, with the gate and the old key hashes,
// before anything regenerates. It reports whether the rotation stopped.
func (m *Manager) patchPreflightStops(ctx context.Context, task *UpgradeTask, inst *InstanceUpgrade, sessions *patchSessions) bool {
	m.setPhase(inst, RotationPhasePreflight)

	plan, err := m.certPreflightPlan(ctx, task, inst, sessions)
	if err != nil {
		return m.preflightFailed(ctx, task, inst, err)
	}

	if plan == nil || len(plan.journals) == 0 {
		m.failRotation(ctx, task, inst, "The preflight found no VM to patch, so nothing changed.") //nolint:errcheck,gosec // failRotation logs and saves it

		return true
	}

	m.mu.Lock()

	inst.Certificates.Phase = RotationPhaseDiscover
	inst.Certificates.VMs = plan.journals
	inst.Certificates.Gate = plan.gate

	for i := range inst.Certificates.Variables {
		v := &inst.Certificates.Variables[i]
		v.OldKeyHash = plan.oldKeys[v.Name]
	}

	m.mu.Unlock()

	err = m.persistCertConfirmed(ctx, task)
	if err != nil {
		m.pauseUnconfirmed(task, inst, "regenerate the certificates")

		return true
	}

	return false
}

// readNewPairs reads the key pair of each variable's new version into
// pairs, checks that its certificate is the one the regeneration
// validated, and validates it again with its private key, so key_match
// runs. It records the new key hash and each file's new hash, and reports
// whether the rotation stopped. Each pair goes into pairs the moment it is
// read, so the caller zeroes it on every path.
func (m *Manager) readNewPairs(ctx context.Context, task *UpgradeTask, inst *InstanceUpgrade, pairs map[string]*credhub.KeyPair) bool {
	api := m.certDeps().CredHub
	if api == nil {
		m.pauseWith(ctx, task, inst, apierr.New(http.StatusForbidden, apierr.CodeCredHubNotConfigured, //nolint:errcheck,gosec // pauseWith logs and saves it
			"CredHub is not configured, so the new certificates can't be read."))

		return true
	}

	m.mu.RLock()
	count := len(inst.Certificates.Variables)
	m.mu.RUnlock()

	for index := range count {
		variable, ok := m.patchVariable(inst, index)
		if !ok {
			m.failRotation(ctx, task, inst, errVariableMissing) //nolint:errcheck,gosec // failRotation logs and saves it

			return true
		}

		pair, err := api.CertificateKeyPair(ctx, variable.NewVersionID)
		if err != nil {
			m.pauseForCredHub(ctx, task, inst, "read the new key pair of "+shortName(variable.Name), err) //nolint:errcheck,gosec // pauseForCredHub logs and saves it

			return true
		}

		pairs[variable.Name] = pair

		if m.validatePair(ctx, api, task, inst, index, pair) {
			return true
		}
	}

	m.mu.Lock()

	hashes := make(map[string][2]string, count)
	for _, v := range inst.Certificates.Variables {
		hashes[v.Name] = [2]string{v.NewCertHash, v.NewKeyHash}
	}

	for i := range inst.Certificates.VMs {
		files := inst.Certificates.VMs[i].Files
		for n := range files {
			pick := 0
			if files[n].Role == FileRoleKey {
				pick = 1
			}

			files[n].NewHash = hashes[files[n].Variable][pick]
		}
	}

	m.mu.Unlock()

	err := m.persistCertConfirmed(ctx, task)
	if err != nil {
		m.pauseUnconfirmed(task, inst, "patch the VMs")

		return true
	}

	return false
}

// errVariableMissing is the message of an instance whose rotation lost a
// variable the patch was reading.
const errVariableMissing = "The rotation no longer lists a certificate the patch was reading, so nothing was patched."

// patchVariable copies the rotation's variable at index, and reports false
// when there is none, so a caller never reads a zero variable.
func (m *Manager) patchVariable(inst *InstanceUpgrade, index int) (VariableRotation, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if inst.Certificates == nil || index < 0 || index >= len(inst.Certificates.Variables) {
		return VariableRotation{}, false
	}

	return inst.Certificates.Variables[index].Clone(), true
}

// validatePair checks one variable's new key pair and records the checks
// and the key's hash. It reports whether the rotation stopped.
func (m *Manager) validatePair(ctx context.Context, api CredHubAPI, task *UpgradeTask, inst *InstanceUpgrade, index int, pair *credhub.KeyPair) bool {
	variable, ok := m.patchVariable(inst, index)
	if !ok {
		m.failRotation(ctx, task, inst, errVariableMissing) //nolint:errcheck,gosec // failRotation logs and saves it

		return true
	}

	short := shortName(variable.Name)
	step := "validate the new key pair of " + short

	if certpatch.NormalisedHash(pair.Certificate) != variable.NewCertHash {
		m.failRotation(ctx, task, inst, fmt.Sprintf( //nolint:errcheck,gosec // failRotation logs and saves it
			"CredHub answered version %s of %s with a certificate other than the one Blacksmith validated, so nothing was patched.", variable.NewVersionID, short))

		return true
	}

	key, err := parsePrivateKey(pair.PrivateKey)
	if err != nil {
		m.failRotation(ctx, task, inst, fmt.Sprintf( //nolint:errcheck,gosec // failRotation logs and saves it
			"The private key of version %s of %s could not be parsed, so nothing was patched.", variable.NewVersionID, short))

		return true
	}

	oldPublic, err := api.CertificateVersion(ctx, variable.OldVersionID)
	if err != nil {
		m.pauseForCredHub(ctx, task, inst, step, err) //nolint:errcheck,gosec // pauseForCredHub logs and saves it

		return true
	}

	newPublic, err := api.CertificateVersion(ctx, variable.NewVersionID)
	if err != nil {
		m.pauseForCredHub(ctx, task, inst, step, err) //nolint:errcheck,gosec // pauseForCredHub logs and saves it

		return true
	}

	signingCA, err := m.signingCA(ctx, api, variable.Name, newPublic)
	if err != nil {
		m.pauseForCredHub(ctx, task, inst, step, err) //nolint:errcheck,gosec // pauseForCredHub logs and saves it

		return true
	}

	checks := ValidateNew(firstCertificate(oldPublic), parseFirst(string(pair.Certificate)), signingCA, key, m.certNow())

	m.changeVariable(inst, index, func(v *VariableRotation) {
		v.Validation = checks
		v.NewKeyHash = certpatch.NormalisedHash(pair.PrivateKey)
	})

	if !AllPassed(checks) {
		m.failRotation(ctx, task, inst, fmt.Sprintf( //nolint:errcheck,gosec // failRotation logs and saves it
			"The new key pair failed validation: version %s of %s %s. CredHub keeps the new version, which is not patched onto any VM.",
			variable.NewVersionID, short, failedChecks(checks)))

		return true
	}

	return false
}

// parsePrivateKey parses the first PEM block of a private key as PKCS #8,
// PKCS #1, or SEC 1. It clears the decoded block before it returns, and
// its only error is errKeyUnreadable.
func parsePrivateKey(data []byte) (crypto.PrivateKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errKeyUnreadable
	}

	defer clear(block.Bytes)

	if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		return key, nil
	}

	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}

	if key, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
		return key, nil
	}

	return nil, errKeyUnreadable
}

// patchVMs patches the instance's VMs one at a time, in journal order. A
// pause or a cancel takes effect only between two VMs. The gate runs once
// before the first VM of a pass. Once every VM is done, finishPatch ends
// the instance as the task's converge policy says, and cancelCh cancels
// the converge deploy that the immediate policy sends. It reports whether
// the instance settled.
func (m *Manager) patchVMs(ctx context.Context, task *UpgradeTask, inst *InstanceUpgrade, sessions *patchSessions, pairs map[string]*credhub.KeyPair, cancelCh <-chan struct{}) bool {
	m.setPhase(inst, RotationPhasePatch)

	err := m.persistCertConfirmed(ctx, task)
	if err != nil {
		m.pauseUnconfirmed(task, inst, "patch the VMs")

		return m.instanceSettled(inst)
	}

	m.mu.RLock()
	count := len(inst.Certificates.VMs)
	gate := inst.Certificates.Gate
	vmCount := len(inst.Certificates.Approved.VMCIDs)
	m.mu.RUnlock()

	gated := false
	leaves := leavesOf(pairs)

	for i := range count {
		j := &inst.Certificates.VMs[i]

		if done, stop := m.vmClosed(ctx, task, inst, j); stop {
			return m.instanceSettled(inst)
		} else if done {
			continue
		}

		if m.stopBetweenVMs(ctx, task, inst) {
			return m.instanceSettled(inst)
		}

		sess, err := m.journalSession(ctx, sessions, inst, j)
		if err != nil {
			m.pauseWith(ctx, task, inst, fmt.Errorf("Blacksmith could not open an SSH session on %s, so it was not patched: %w", j.VM, err)) //nolint:errcheck,gosec // pauseWith logs and saves it

			return m.instanceSettled(inst)
		}

		if !gated {
			if m.gateBeforeVM(ctx, task, inst, j, sess, gate, vmCount) {
				return m.instanceSettled(inst)
			}

			gated = true
		}

		if m.patchVMForward(ctx, task, inst, j, pairs, sess) != nil {
			return m.instanceSettled(inst)
		}

		if m.patchVMFinish(ctx, task, inst, j, sess, leaves) != nil {
			return m.instanceSettled(inst)
		}
	}

	return m.finishPatch(ctx, task, inst, cancelCh)
}

// finishPatch ends an instance whose VMs all reached done, as the task's
// converge policy says. The immediate policy converges the instance on
// the reservation the patch holds, so nothing else can deploy it between
// the last VM and the converge deploy. The pending policy settles the
// instance as a success and leaves its divergence record unconverged for
// a later converge. Only a patch whose every VM is done reaches here, so
// a partial patch never converges on its own. It reports whether the
// instance settled.
func (m *Manager) finishPatch(ctx context.Context, task *UpgradeTask, inst *InstanceUpgrade, cancelCh <-chan struct{}) bool {
	m.mu.RLock()
	policy := taskConverge(task)
	m.mu.RUnlock()

	if policy == ConvergeImmediate {
		m.logger.Info("Certificate task %s patched every VM of instance %s, so it converges the instance now", task.ID, inst.InstanceID)

		return m.convergeInstance(ctx, task, inst, cancelCh)
	}

	m.settleCertificateInstance(task, inst, true, "") //nolint:contextcheck // The follow-up scan runs on the manager's lifecycle context, so a finished request never cancels it.

	err := m.persistCertConfirmed(ctx, task)
	if err != nil {
		m.logger.Error("Certificate task %s: %v", task.ID, err)
	}

	return true
}

// vmClosed reads the VM's phase before a pass reaches it. A VM that is
// done needs nothing more. A VM another pass left unrecovered or reverted
// fails the instance, and one left part way pauses the task until the
// resume that checks its files. It reports whether the VM is done and
// whether the instance stopped.
func (m *Manager) vmClosed(ctx context.Context, task *UpgradeTask, inst *InstanceUpgrade, j *VMJournal) (bool, bool) {
	m.mu.RLock()
	phase, cleanup, name := j.Phase, j.Cleanup, j.VM
	m.mu.RUnlock()

	switch {
	case phase == VMPhaseDone:
		return true, false
	case phase == VMPhaseUnrecovered || phase == VMPhaseReverted:
		m.failRotation(ctx, task, inst, fmt.Sprintf("VM %s is %s. %s", name, phase, redeployToRepair)) //nolint:errcheck,gosec // failRotation logs and saves it

		return false, true
	case phase == VMPhaseRolledBack:
		m.failRotation(ctx, task, inst, fmt.Sprintf( //nolint:errcheck,gosec // failRotation logs and saves it
			"VM %s was rolled back to its old certificate, so the instance is only partly patched. %s", name, redeployToRepair))

		return false, true
	case phase != VMPhasePlanned || cleanup != "":
		m.pauseWith(ctx, task, inst, fmt.Errorf( //nolint:errcheck,gosec // pauseWith logs and saves it
			"VM %s was left %s by an earlier pass, so it waits for the resume that checks its files.", name, phase))

		return false, true
	default:
		return false, false
	}
}

// stopBetweenVMs ends a cancelled task's instance, or leaves a paused
// task's instance as it stands, before the next VM. It reports whether it
// stopped.
func (m *Manager) stopBetweenVMs(ctx context.Context, task *UpgradeTask, inst *InstanceUpgrade) bool {
	if m.stopIfCancelled(ctx, task, inst, cancelledBetweenVMs) {
		return true
	}

	m.mu.RLock()
	paused := task.Paused
	m.mu.RUnlock()

	return paused
}

// patchCancelEnd is how an idle instance of a cancelled patch task ends,
// given the message it ends with when no VM was patched. A VM that may
// be part way through a swap or a rollback keeps the instance open, since
// only the resume that checks its files can tell what it holds, and the
// resume finishes it. A VM left
// unrecovered or reverted fails the instance with the advice to redeploy
// it. Otherwise the instance is cancelled, as it is between two VMs once
// any VM was patched or rolled back. The caller holds mu.
func patchCancelEnd(inst *InstanceUpgrade, untouched string) (InstanceStatus, string, bool) {
	if inst.Certificates == nil {
		return InstanceStatusCancelled, untouched, true
	}

	status, message := InstanceStatusCancelled, untouched

	for _, j := range inst.Certificates.VMs {
		switch j.Phase {
		case VMPhaseSwapping, VMPhaseSwapped, VMPhaseRestarted, VMPhaseVerified, VMPhaseRollingBack:
			return "", "", false
		case VMPhaseUnrecovered, VMPhaseReverted:
			status = InstanceStatusFailed
			message = fmt.Sprintf("The task was cancelled with VM %s %s. %s", j.VM, j.Phase, redeployToRepair)
		case VMPhaseDone, VMPhaseRolledBack:
			if status != InstanceStatusFailed {
				message = cancelledBetweenVMs
			}
		case VMPhasePlanned, VMPhaseStaged, VMPhaseBackedUp:
		}
	}

	return status, message, true
}

// journalSession returns the session of the VM's instance group, opened
// on every VM of the group the journals name.
func (m *Manager) journalSession(ctx context.Context, sessions *patchSessions, inst *InstanceUpgrade, j *VMJournal) (PatchSession, error) {
	m.mu.RLock()

	group := j.Group

	var indices []int

	for _, journal := range inst.Certificates.VMs {
		if journal.Group == group {
			indices = append(indices, journal.Index)
		}
	}

	m.mu.RUnlock()

	sort.Ints(indices)

	return sessions.group(ctx, group, indices)
}

// journalHost finds the journal's VM in the session.
func journalHost(sess PatchSession, j *VMJournal) (int, error) {
	return hostIndex(sess, j.Group, strings.TrimPrefix(j.VM, j.Group+"/"))
}

// gateBeforeVM runs the gate on the VM before any VM of the pass is
// changed. It reports whether the task paused.
func (m *Manager) gateBeforeVM(ctx context.Context, task *UpgradeTask, inst *InstanceUpgrade, j *VMJournal, sess PatchSession, gate string, vmCount int) bool {
	host, err := journalHost(sess, j)
	if err == nil {
		err = m.runGate(ctx, sess, host, gate, vmCount)
	}

	if err == nil {
		return false
	}

	if ctx.Err() != nil {
		return true
	}

	m.pauseWith(ctx, task, inst, fmt.Errorf("The health gate did not pass before the patch, so no VM was changed: %w", err)) //nolint:errcheck,gosec // pauseWith logs and saves it

	return true
}

// patchVMForward takes one planned VM to swapped through sess, the
// session of its group, and never opens one of its own. It re-checks the
// director, the VM's CID, and its files, stages every new file, backs up
// every file, records the VM in the divergence record, and replaces each
// file. Every command that changes the VM is sent only once the switches
// allow it and the journal's intent is saved for certain, and its result
// is saved after it. It returns errVMStopped once it paused or failed the
// task, and nil when the VM reached swapped.
func (m *Manager) patchVMForward(ctx context.Context, task *UpgradeTask, inst *InstanceUpgrade, j *VMJournal, pairs map[string]*credhub.KeyPair, sess PatchSession) error {
	host, err := journalHost(sess, j)
	if err != nil {
		m.failRotation(ctx, task, inst, fmt.Sprintf("VM %s was not patched: %v", j.VM, err)) //nolint:errcheck,gosec // failRotation logs and saves it

		return errVMStopped
	}

	run := vmRun{manager: m, ctx: ctx, task: task, inst: inst, j: j, sess: sess, host: host}

	for _, step := range []func() error{
		run.recheck,
		func() error { return run.stage(pairs) },
		run.backup,
		func() error { return run.diverge(pairs, certinventory.DivergentVMUnknown) },
		func() error { return run.advance(VMPhaseSwapping, "replace the files on "+j.VM) },
		run.replace,
		func() error { return run.diverge(pairs, certinventory.DivergentVMApplied) },
		func() error { return run.advance(VMPhaseSwapped, "restart the processes on "+j.VM) },
	} {
		err := step()
		if err != nil {
			return err
		}
	}

	return nil
}

// vmRun is one pass of the executor over one VM.
type vmRun struct {
	manager *Manager
	//nolint:containedctx // a pass over one VM lives as long as this context
	ctx  context.Context
	task *UpgradeTask
	inst *InstanceUpgrade
	j    *VMJournal
	sess PatchSession
	host int
}

// change applies a change to the journal under mu.
func (r *vmRun) change(change func(j *VMJournal)) {
	r.manager.mu.Lock()
	defer r.manager.mu.Unlock()

	change(r.j)
}

// files copies the journal's files.
func (r *vmRun) files() []FileEntry {
	r.manager.mu.RLock()
	defer r.manager.mu.RUnlock()

	return slices.Clone(r.j.Files)
}

// save saves the task for certain. An unconfirmed save pauses the task,
// which then does not step, and returns errVMStopped.
func (r *vmRun) save(step string) error {
	err := r.manager.persistCertConfirmed(r.ctx, r.task)
	if err != nil {
		r.manager.pauseUnconfirmed(r.task, r.inst, step)

		return errVMStopped
	}

	return nil
}

// advance moves the journal to the phase and saves it for certain before
// the next step's side effect. An unconfirmed save puts the phase back.
func (r *vmRun) advance(phase VMPhase, step string) error {
	var before VMPhase

	var err error

	r.change(func(j *VMJournal) {
		before = j.Phase
		err = j.Advance(phase)
	})

	if err != nil {
		r.manager.failRotation(r.ctx, r.task, r.inst, fmt.Sprintf("VM %s could not move on: %v", r.j.VM, err)) //nolint:errcheck,gosec // failRotation logs and saves it

		return errVMStopped
	}

	err = r.save(step)
	if err != nil {
		r.change(func(j *VMJournal) { j.Phase = before })
	}

	return err
}

// mutate sends one command that changes the VM. Until the VM's first
// replace is sent it checks the switches, and after that it doesn't. It
// applies the intent to the journal and saves it for certain, and only
// then sends the command. A refusal or an unconfirmed save sends nothing,
// puts the journal back, and returns errVMStopped. Any other error is the
// command's own.
func (r *vmRun) mutate(step string, intent func(j *VMJournal), send func() (ssh.Result, error)) (ssh.Result, error) {
	err := r.writesAllowed()
	if err != nil {
		r.manager.pauseWith(r.ctx, r.task, r.inst, writesRefused(err)) //nolint:errcheck,gosec // pauseWith logs and saves it

		return ssh.Result{}, errVMStopped
	}

	return r.commit(step, intent, send)
}

// writesAllowed checks the switches for a patch write to the VM, until
// its first replace was sent. From then on the VM's swap completes, or it
// rolls back, whatever the switches say, so turning patching off never
// leaves a new certificate beside an old key or a VM swapped and not
// restarted. The switches then stop the task before the next VM.
func (r *vmRun) writesAllowed() error {
	if r.swapBegun() {
		return nil
	}

	return r.manager.certWritesAllowed(RotationModePatch)
}

// swapBegun reports whether a replace was sent to the VM, as
// journalSwapBegun reads its journal.
func (r *vmRun) swapBegun() bool {
	r.manager.mu.RLock()
	defer r.manager.mu.RUnlock()

	return journalSwapBegun(r.j)
}

// journalSwapBegun reports whether a replace may have been sent to the
// VM, as its journal shows by a file that is or was being replaced or a
// phase after swapping. The caller holds mu, or owns the journal.
func journalSwapBegun(j *VMJournal) bool {
	switch j.Phase {
	case VMPhaseSwapped, VMPhaseRestarted, VMPhaseVerified, VMPhaseDone, VMPhaseRollingBack, VMPhaseRolledBack:
		return true
	case VMPhasePlanned, VMPhaseStaged, VMPhaseBackedUp, VMPhaseSwapping, VMPhaseUnrecovered, VMPhaseReverted:
	}

	return slices.ContainsFunc(j.Files, func(file FileEntry) bool {
		return file.State != FileStatePlanned && file.State != FileStateBackedUp && file.State != ""
	})
}

// commit applies the intent to the journal and saves it for certain, and
// only then sends the command. An unconfirmed save sends nothing, puts
// the journal back, and returns errVMStopped. A rollback commits its
// steps without the switches, because putting the old files back is
// never a write they forbid.
func (r *vmRun) commit(step string, intent func(j *VMJournal), send func() (ssh.Result, error)) (ssh.Result, error) {
	r.manager.mu.RLock()
	before := copyJournal(*r.j)
	r.manager.mu.RUnlock()

	if intent != nil {
		r.change(intent)
	}

	err := r.save(step)
	if err != nil {
		r.change(func(j *VMJournal) { *j = before })

		return ssh.Result{}, err
	}

	return send()
}

// copyJournal copies a journal with its slices and map.
func copyJournal(j VMJournal) VMJournal {
	j.Files = slices.Clone(j.Files)
	j.Processes = slices.Clone(j.Processes)
	j.Targets = slices.Clone(j.Targets)

	dirs := make(map[string]string, len(j.JobDirs))
	for job, dir := range j.JobDirs {
		dirs[job] = dir
	}

	j.JobDirs = dirs

	return j
}

// script runs one patch.sh operation on the VM under the operation's
// timeout.
func (r *vmRun) script(op string, args ...string) (ssh.Result, error) {
	return runScript(r.ctx, r.sess, r.host, patchScript, op, append([]string{r.task.ID}, args...)...)
}

// okLines returns the lines of a result that exited 0 and printed only ok
// lines, and false for any other result.
func okLines(result ssh.Result) ([]map[string]string, bool) {
	if result.ExitCode != certscripts.ExitOK || result.Truncated {
		return nil, false
	}

	records, err := certscripts.ParseRecords(result.Stdout, patchKinds)
	if err != nil || len(records) == 0 {
		return nil, false
	}

	lines := make([]map[string]string, 0, len(records))

	for _, record := range records {
		if record.Kind != "ok" {
			return nil, false
		}

		lines = append(lines, record.Fields)
	}

	return lines, true
}

// exitMeaning says in words why a script exited as it did. It never
// repeats the script's output.
func exitMeaning(op string, result ssh.Result) string {
	switch result.ExitCode {
	case certscripts.ExitOK:
		return fmt.Sprintf("patch.sh %s printed an answer outside its contract", op)
	case certscripts.ExitDrift:
		return fmt.Sprintf("patch.sh %s found a file that changed under it", op)
	case certscripts.ExitBackup:
		return fmt.Sprintf("patch.sh %s found a backup that no longer holds the old content", op)
	case certscripts.ExitRestart:
		return fmt.Sprintf("patch.sh %s could not restart every process with a new pid", op)
	case certscripts.ExitRefused:
		return fmt.Sprintf("patch.sh %s refused a path or directory that failed its safety checks", op)
	default:
		return fmt.Sprintf("patch.sh %s exited %d", op, result.ExitCode)
	}
}

// jobDir is the job directory of the file's job.
func (r *vmRun) jobDir(file FileEntry) string {
	r.manager.mu.RLock()
	defer r.manager.mu.RUnlock()

	return r.j.JobDirs[file.Job]
}

// stopUnchanged stops the VM after a failure before any of its files was
// replaced, so every file still holds the old content. A command that did
// not finish fails the instance. A command that refused or found drift
// leaves the VM unrecovered and pauses the task, so an operator can look
// at it. It returns errVMStopped.
func (r *vmRun) stopUnchanged(why string, finished bool) error {
	if r.ctx.Err() != nil {
		return errVMStopped
	}

	message := fmt.Sprintf("VM %s was not patched, and every file on it keeps the old certificate: %s.", r.j.VM, why)

	if !finished {
		r.change(func(j *VMJournal) { j.Error = message })
		r.manager.failRotation(r.ctx, r.task, r.inst, message) //nolint:errcheck,gosec // failRotation logs and saves it

		return errVMStopped
	}

	var refused error

	r.change(func(j *VMJournal) {
		j.Error = message
		refused = j.Advance(VMPhaseUnrecovered)
	})

	r.manager.logRefusedMove(r.task, r.j.VM, refused)

	r.manager.pauseWith(r.ctx, r.task, r.inst, errors.New(message)) //nolint:errcheck,gosec // pauseWith logs and saves it

	return errVMStopped
}

// recheck makes sure the VM is still the one discovery planned for, with
// no BOSH task running on its deployment and every file holding the old
// content, before anything is sent to it. A VM that changed is handled as
// DecideResume says, and the task pauses.
func (r *vmRun) recheck() error {
	cid, err := r.directorCID("was not patched")
	if err != nil {
		return err
	}

	live := LiveState{CID: cid, FileHashes: map[string]string{}, BackupHashes: map[string]string{}}

	if live.CID == r.j.CID {
		err = r.checkFiles(&live)
		if err != nil {
			return err
		}
	}

	action, why := DecideResume(r.j, live)
	if action == ResumeContinueStaging {
		return nil
	}

	var refused error

	r.change(func(j *VMJournal) {
		j.Error = why

		switch action { //nolint:exhaustive // every other action leaves the VM where it is
		case ResumeMarkReverted:
			refused = j.Advance(VMPhaseReverted)
		case ResumeUnrecovered:
			refused = j.Advance(VMPhaseUnrecovered)
		}
	})

	r.manager.logRefusedMove(r.task, r.j.VM, refused)

	return r.pause(fmt.Errorf("VM %s was not patched: %s", r.j.VM, why))
}

// directorCID makes sure no BOSH task runs on the VM's deployment and
// returns the CID the director lists for the VM, or "" when it lists
// none. A read that fails, or a running task, pauses the task with a
// message that says the VM then outcome, and returns errVMStopped.
func (r *vmRun) directorCID(outcome string) (string, error) {
	m := r.manager
	deployment := r.inst.DeploymentName

	running, err := directorCall(r.ctx, func(c context.Context) ([]bosh.Task, error) {
		return m.director.FindRunningTasksForDeployment(c, deployment)
	})

	switch {
	case err != nil:
		return "", r.pause(fmt.Errorf("The director did not list the running tasks of %s, so VM %s %s: %w", deployment, r.j.VM, outcome, err))
	case len(running) > 0:
		return "", r.pause(apierr.New(http.StatusConflict, apierr.CodeDeploymentLocked,
			fmt.Sprintf("BOSH task %d is running on deployment %s, so VM %s %s. Resume once it ends.", running[0].ID, deployment, r.j.VM, outcome)))
	}

	vms, err := directorCall(r.ctx, func(c context.Context) ([]bosh.VM, error) {
		return m.director.GetDeploymentVMs(c, deployment)
	})
	if err != nil {
		return "", r.pause(fmt.Errorf("The director did not list the VMs of %s, so VM %s %s: %w", deployment, r.j.VM, outcome, err))
	}

	return liveCID(vms, r.j), nil
}

// pause pauses the task with the error and returns errVMStopped.
func (r *vmRun) pause(err error) error {
	if r.ctx.Err() == nil {
		r.manager.pauseWith(r.ctx, r.task, r.inst, err) //nolint:errcheck,gosec // pauseWith logs and saves it
	}

	return errVMStopped
}

// liveCID returns the CID the director lists for the journal's VM, found
// by instance group and index, or "" when it lists none.
func liveCID(vms []bosh.VM, j *VMJournal) string {
	for _, vm := range vms {
		if vm.Job == j.Group && vm.Index == j.Index {
			return vm.CID
		}
	}

	return ""
}

// checkFiles runs patch.sh check on every file and records what it found
// in live, as hashes DecideResume compares with the journal's.
func (r *vmRun) checkFiles(live *LiveState) error {
	for _, file := range r.files() {
		result, err := r.script(opCheck, r.jobDir(file), file.Path, file.OldHash, file.NewHash, file.BackupPath)
		if err != nil {
			return r.pause(fmt.Errorf("The check of %s on VM %s did not finish, so the VM was not patched: %w", file.Path, r.j.VM, err))
		}

		lines, ok := okLines(result)

		switch {
		case ok && len(lines) == 1 && lines[0]["path"] == file.Path:
			live.FileHashes[file.Path] = map[string]string{"old": file.OldHash, "new": file.NewHash}[lines[0]["state"]]
			if lines[0]["backup"] == "old" {
				live.BackupHashes[file.BackupPath] = file.OldHash
			}
		case result.ExitCode == certscripts.ExitDrift:
			live.FileHashes[file.Path] = "drift"
		case result.ExitCode == certscripts.ExitBackup:
			live.BackupHashes[file.BackupPath] = "drift"
		default:
			return r.pause(fmt.Errorf("VM %s was not patched: %s", r.j.VM, exitMeaning(opCheck, result)))
		}
	}

	return nil
}

// stage makes the task's private work directory, streams each new file
// to its staging path over stdin, checks every staged copy, and advances
// to staged.
func (r *vmRun) stage(pairs map[string]*credhub.KeyPair) error {
	result, err := r.mutate("prepare VM "+r.j.VM, func(j *VMJournal) { j.Cleanup = CleanupPending }, func() (ssh.Result, error) {
		return r.script(opPrepare)
	})

	switch {
	case errors.Is(err, errVMStopped):
		return err
	case err != nil:
		return r.stopUnchanged("the work directory was not made, because the command did not finish", false)
	}

	if _, ok := okLines(result); !ok {
		return r.stopUnchanged(exitMeaning(opPrepare, result), true)
	}

	files := r.files()

	for n, file := range files {
		err := r.stageFile(n, file, pairs, r.stopUnchanged)
		if err != nil {
			return err
		}
	}

	for n, file := range files {
		result, err := r.script(opStageCheck, workPath(r.task.ID, "s", n), file.NewHash)

		switch {
		case err != nil:
			return r.stopUnchanged("the check of a staged copy did not finish", false)
		case !isOK(result):
			return r.stopUnchanged("the staged copy of "+file.Path+" does not hold the new content: "+exitMeaning(opStageCheck, result), true)
		}
	}

	return r.reach(VMPhaseStaged, "back up the files on "+r.j.VM)
}

// isOK reports whether a result exited 0 with only ok lines.
func isOK(result ssh.Result) bool {
	_, ok := okLines(result)

	return ok
}

// stageFile streams one file's new content to staging path n. The content
// travels only on stdin, after patch.sh itself. A failure stops the VM
// through fail, which gets why and whether the command finished.
func (r *vmRun) stageFile(n int, file FileEntry, pairs map[string]*credhub.KeyPair, fail func(why string, finished bool) error) error {
	staged := workPath(r.task.ID, "s", n)

	_, err := certscripts.StageCommand(staged, r.task.ID)
	if err != nil {
		return fail(fmt.Sprintf("the staging path of %s was refused: %v", file.Path, err), false)
	}

	pair := pairs[file.Variable]
	if pair == nil {
		return fail("there is no new key pair for "+shortName(file.Variable), false)
	}

	content := pair.Certificate
	if file.Role == FileRoleKey {
		content = pair.PrivateKey
	}

	result, err := r.mutate("stage "+file.Path+" on "+r.j.VM, nil, func() (ssh.Result, error) {
		input, err := certscripts.StageInput(bytes.NewReader(content))
		if err != nil {
			return ssh.Result{}, err
		}

		ctx, cancel := context.WithTimeout(r.ctx, patchCommandTimeout)
		defer cancel()

		return r.sess.Run(ctx, r.host, opStage, []string{r.task.ID, staged}, input)
	})

	switch {
	case errors.Is(err, errVMStopped):
		return err
	case err != nil:
		return fail("the staging of "+file.Path+" did not finish", false)
	case !isOK(result):
		return fail(exitMeaning(opStage, result), true)
	}

	return nil
}

// backup backs up every file before any is replaced, and advances to
// backed_up.
func (r *vmRun) backup() error {
	for n, file := range r.files() {
		result, err := r.mutate("back up "+file.Path+" on "+r.j.VM, nil, func() (ssh.Result, error) {
			return r.script(opBackup, r.jobDir(file), file.Path, file.OldHash, file.BackupPath)
		})

		switch {
		case errors.Is(err, errVMStopped):
			return err
		case err != nil:
			return r.stopUnchanged("the backup of "+file.Path+" did not finish", false)
		case !isOK(result):
			return r.stopUnchanged(exitMeaning(opBackup, result), true)
		}

		r.change(func(j *VMJournal) { j.Files[n].State = FileStateBackedUp })

		err = r.save("go on after backing up " + file.Path)
		if err != nil {
			return err
		}
	}

	return r.reach(VMPhaseBackedUp, "record the divergence of "+r.j.VM)
}

// diverge writes this VM's state for each variable it holds to the
// divergence record, and waits until the write is confirmed. It checks
// the switches as mutate does. With no store, or a write that is not
// confirmed, the task pauses.
func (r *vmRun) diverge(pairs map[string]*credhub.KeyPair, state string) error {
	m := r.manager

	err := r.writesAllowed()
	if err != nil {
		return r.pause(writesRefused(err))
	}

	err = m.recordDivergence(r.ctx, r.task, r.inst, r.j, fingerprints(pairs), state)
	if err != nil {
		return r.pause(fmt.Errorf("The divergence record of VM %s could not be saved, so the patch stopped there: %w", r.j.VM, err))
	}

	return nil
}

// fingerprints maps each variable to its new leaf's fingerprint.
func fingerprints(pairs map[string]*credhub.KeyPair) map[string]string {
	out := make(map[string]string, len(pairs))

	for name, pair := range pairs {
		if leaf := parseFirst(string(pair.Certificate)); leaf != nil {
			out[name] = certinventory.Fingerprint(leaf)
		}
	}

	return out
}

// errNoDivergenceStore is the error a patch gets when the broker has no
// divergence store.
var errNoDivergenceStore = errors.New("Blacksmith has no divergence store") //nolint:staticcheck // the message names the product

// recordDivergence sets the VM's state for every variable its files hold.
// An entry gets the versions and the task when it is written, and keeps
// the time it began, and its fingerprint when prints has none for it. Setting a VM old drops an entry that no VM holds
// anything new of any more, and the record when it has no entry left.
func (m *Manager) recordDivergence(ctx context.Context, task *UpgradeTask, inst *InstanceUpgrade, j *VMJournal, prints map[string]string, state string) error {
	store := m.certDeps().Divergence
	if store == nil {
		return errNoDivergenceStore
	}

	m.mu.RLock()

	cid, group, index := j.CID, j.Group, j.Index
	held := make(map[string]bool)

	for _, file := range j.Files {
		held[file.Variable] = true
	}

	var variables []VariableRotation

	for _, v := range inst.Certificates.Variables {
		if held[v.Name] {
			variables = append(variables, v.Clone())
		}
	}

	m.mu.RUnlock()

	now := m.certNow()

	return store.Update(ctx, inst.InstanceID, func(cur *certinventory.Divergence) (*certinventory.Divergence, error) {
		if cur == nil {
			cur = &certinventory.Divergence{InstanceID: inst.InstanceID}
		}

		for _, v := range variables {
			entry := divergenceEntry(cur, v.Name)
			entry.BOSHVersionID = v.OldVersionID
			entry.AppliedVersionID = v.NewVersionID

			if fp := prints[v.Name]; fp != "" {
				entry.AppliedFingerprint = fp
			}

			entry.TaskID = task.ID
			entry.State = certinventory.DivergenceUnconverged

			if entry.Since.IsZero() {
				entry.Since = now
			}

			entry.SetVM(cid, state)

			vm := entry.VMs[cid]
			vm.Group = group
			vm.Index = index
			entry.VMs[cid] = vm
		}

		return pruneDivergence(cur), nil
	})
}

// divergenceEntry returns the record's entry for the variable, adding it
// when there is none.
func divergenceEntry(d *certinventory.Divergence, name string) *certinventory.DivergentVariable {
	for i := range d.Variables {
		if d.Variables[i].Name == name {
			return &d.Variables[i]
		}
	}

	d.Variables = append(d.Variables, certinventory.DivergentVariable{Name: name})

	return &d.Variables[len(d.Variables)-1]
}

// pruneDivergence drops each entry no VM holds anything new of, and
// returns nil for a record with no entry left.
func pruneDivergence(d *certinventory.Divergence) *certinventory.Divergence {
	kept := d.Variables[:0]

	for _, entry := range d.Variables {
		for _, vm := range entry.VMs {
			if vm.State == certinventory.DivergentVMApplied || vm.State == certinventory.DivergentVMUnknown {
				kept = append(kept, entry)

				break
			}
		}
	}

	d.Variables = kept

	if len(kept) == 0 {
		return nil
	}

	return d
}

// replace replaces each file with its staged copy. Each file is marked
// replacing and saved before its command, and replaced and saved after.
func (r *vmRun) replace() error {
	for n, file := range r.files() {
		result, err := r.mutate("replace "+file.Path+" on "+r.j.VM, func(j *VMJournal) { j.Files[n].State = FileStateReplacing }, func() (ssh.Result, error) {
			return r.script(opReplace, r.jobDir(file), file.Path, workPath(r.task.ID, "s", n), file.OldHash, file.NewHash)
		})

		switch {
		case errors.Is(err, errVMStopped):
			return err
		case err != nil:
			return r.replaceFailed("the replacement of " + file.Path + " did not finish")
		}

		lines, ok := okLines(result)
		if !ok || len(lines) != 1 || lines[0]["state"] != "new" {
			return r.replaceFailed(exitMeaning(opReplace, result))
		}

		r.change(func(j *VMJournal) { j.Files[n].State = FileStateReplaced })

		err = r.save("go on after replacing " + file.Path)
		if err != nil {
			return err
		}
	}

	return nil
}

// replaceFailed rolls the VM back after a replacement failed, when a file
// may hold the new content. A manager that is stopping leaves the VM
// swapping for the resume that checks its files.
func (r *vmRun) replaceFailed(why string) error {
	if r.ctx.Err() != nil {
		return errVMStopped
	}

	return r.rollback(fmt.Sprintf("a replacement failed: %s", why))
}

// leafFacts is what the checks after a patch need to know of one
// variable's new certificate: its fingerprint and the name to send for
// SNI. It holds no key material.
type leafFacts struct {
	fingerprint string
	serverName  string
}

// leavesOf reads the new certificate of each pair.
func leavesOf(pairs map[string]*credhub.KeyPair) map[string]leafFacts {
	out := make(map[string]leafFacts, len(pairs))

	for name, pair := range pairs {
		leaf := parseFirst(string(pair.Certificate))
		if leaf == nil {
			continue
		}

		facts := leafFacts{fingerprint: certinventory.Fingerprint(leaf), serverName: validServerName(leaf.Subject.CommonName)}
		if facts.serverName == "" && len(leaf.DNSNames) > 0 {
			facts.serverName = validServerName(leaf.DNSNames[0])
		}

		out[name] = facts
	}

	return out
}

// patchVMFinish takes a swapped VM to done through sess, the session of
// its group. It restarts the processes that use the files, verifies each
// listener serves the new certificate, and runs the health gate until it
// passes or patchGateWait ends. Each phase is saved for certain before
// the step that follows it. A restart that fails or times out, a failed
// verification, or a gate that does not pass rolls the VM back. Once done
// is confirmed it removes the task's files from the VM, and a cleanup
// that fails marks the journal without changing the VM's result. leaves
// gives each variable's new fingerprint and server name. It returns
// errVMStopped once it paused or failed the task, and nil when the VM is
// done.
func (m *Manager) patchVMFinish(ctx context.Context, task *UpgradeTask, inst *InstanceUpgrade, j *VMJournal, sess PatchSession, leaves map[string]leafFacts) error {
	host, err := journalHost(sess, j)
	if err != nil {
		m.failRotation(ctx, task, inst, fmt.Sprintf("VM %s was not restarted: %v. %s", j.VM, err, redeployToRepair)) //nolint:errcheck,gosec // failRotation logs and saves it

		return errVMStopped
	}

	r := vmRun{manager: m, ctx: ctx, task: task, inst: inst, j: j, sess: sess, host: host}

	return r.finish(leaves)
}

// finish takes a swapped VM to done, as patchVMFinish describes.
func (r *vmRun) finish(leaves map[string]leafFacts) error {
	result, err := r.mutate("restart the processes on "+r.j.VM, nil, r.restart)

	switch {
	case errors.Is(err, errVMStopped):
		return err
	case r.ctx.Err() != nil:
		return errVMStopped
	case err != nil:
		return r.rollback("the restart did not finish in time")
	case !r.restarted(result):
		return r.rollback(exitMeaning(opRestart, result))
	}

	err = r.reach(VMPhaseRestarted, "verify the listeners on "+r.j.VM)
	if err != nil {
		return err
	}

	return r.finishVerify(leaves)
}

// finishVerify takes a restarted VM to done. It verifies each listener,
// runs the health gate, saves done, and cleans up, and it rolls the VM
// back when a check fails.
func (r *vmRun) finishVerify(leaves map[string]leafFacts) error {
	failed := r.verify(leaves)
	if r.ctx.Err() != nil {
		return errVMStopped
	}

	if failed != "" {
		return r.rollback(failed)
	}

	err := r.reach(VMPhaseVerified, "run the health gate on "+r.j.VM)
	if err != nil {
		return err
	}

	err = r.gateAfter()
	if r.ctx.Err() != nil {
		return errVMStopped
	}

	if err != nil {
		return r.rollback(fmt.Sprintf("the health gate did not pass within %s: %v", r.manager.patchGateWait, err))
	}

	err = r.advance(VMPhaseDone, "clean up "+r.j.VM)
	if err != nil {
		return err
	}

	return r.cleanup()
}

// restart restarts the journal's processes.
func (r *vmRun) restart() (ssh.Result, error) {
	r.manager.mu.RLock()
	processes := slices.Clone(r.j.Processes)
	r.manager.mu.RUnlock()

	return r.script(opRestart, processes...)
}

// restarted reports whether a restart named every process ok.
func (r *vmRun) restarted(result ssh.Result) bool {
	lines, ok := okLines(result)
	if !ok {
		return false
	}

	named := make(map[string]bool, len(lines))
	for _, line := range lines {
		named[line["name"]] = true
	}

	r.manager.mu.RLock()
	defer r.manager.mu.RUnlock()

	for _, process := range r.j.Processes {
		if !named[process] {
			return false
		}
	}

	return true
}

// verify checks every target of the VM against the new fingerprints and
// records the results. It returns why a target failed, or "" when all
// passed. mu is not held while verifyTargets runs.
func (r *vmRun) verify(leaves map[string]leafFacts) string {
	prints := make(map[string]string, len(leaves))
	for name, facts := range leaves {
		prints[name] = facts.fingerprint
	}

	r.change(func(j *VMJournal) {
		for i := range j.Targets {
			j.Targets[i].ServerName = leaves[j.Targets[i].Variable].serverName
		}
	})

	run := func(op string, args ...string) (ssh.Result, error) {
		return runScript(r.ctx, r.sess, r.host, op, op, args...)
	}

	results := r.manager.verifyTargets(r.ctx, r.inst, r.j, prints, run)

	r.change(func(j *VMJournal) { j.Targets = results })

	if len(results) == 0 {
		return "no listener or file was left to verify"
	}

	for _, result := range results {
		if !result.OK {
			return fmt.Sprintf("the check of %s on port %d failed: %s", shortName(result.Variable), result.Port, result.Detail)
		}
	}

	return ""
}

// gateAfter runs the health gate on the VM until it passes or
// patchGateWait ends, and records the last answer in the journal.
func (r *vmRun) gateAfter() error {
	m := r.manager

	m.mu.RLock()
	gate := r.inst.Certificates.Gate
	vmCount := len(r.inst.Certificates.Approved.VMCIDs)
	m.mu.RUnlock()

	deadline := time.Now().Add(m.patchGateWait)

	for {
		err := m.runGate(r.ctx, r.sess, r.host, gate, vmCount)
		if err == nil {
			r.change(func(j *VMJournal) { j.GateAfter = "passed" })

			return nil
		}

		if r.ctx.Err() != nil || !time.Now().Add(m.patchGateInterval).Before(deadline) {
			r.change(func(j *VMJournal) { j.GateAfter = "failed" })

			return err
		}

		select {
		case <-r.ctx.Done():
			return r.ctx.Err()
		case <-time.After(m.patchGateInterval):
		}
	}
}

// cleanup removes the task's files from a VM that is done. A cleanup
// that fails is recorded and leaves the VM done.
func (r *vmRun) cleanup() error {
	args := r.cleanupArgs()

	result, err := r.script(opCleanup, args...)

	state := CleanupDone
	if err != nil || !isOK(result) {
		state = CleanupFailed

		if r.ctx.Err() != nil {
			return errVMStopped
		}

		r.manager.logger.Error("Certificate task %s could not clean up VM %s, which keeps the new certificate", r.task.ID, r.j.VM)
	}

	r.change(func(j *VMJournal) { j.Cleanup = state })

	return r.save("go on after cleaning up " + r.j.VM)
}

// cleanupArgs is the job directory and path of every file.
func (r *vmRun) cleanupArgs() []string {
	files := r.files()
	args := make([]string, 0, 2*len(files))

	for _, file := range files {
		args = append(args, r.jobDir(file), file.Path)
	}

	return args
}

// rollback records why the VM rolls back and rolls it back.
func (r *vmRun) rollback(why string) error {
	r.change(func(j *VMJournal) { j.Error = fmt.Sprintf("VM %s was rolled back because %s.", j.VM, why) })

	return r.manager.rollbackVM(r.ctx, r.task, r.inst, r.j, r.sess)
}

// rollbackVM puts a VM's old files back after its restart, verification,
// gate, or a replacement failed. It saves rolling_back before the first
// restore, and marks each file it restores restoring, then restored, with
// a save before and after each command. It restarts the processes and
// runs the gate. It never stages or replaces anything. A VM that comes
// back is rolled_back, its entry in the divergence record turns old, its
// backups are removed only once rolled_back is saved, and the task
// pauses. A VM that does not is unrecovered, keeps its backups, and fails
// the instance. It always returns errVMStopped, since the VM did not take
// the new certificate.
func (m *Manager) rollbackVM(ctx context.Context, task *UpgradeTask, inst *InstanceUpgrade, j *VMJournal, sess PatchSession) error {
	host, err := journalHost(sess, j)
	if err != nil {
		return m.unrecovered(ctx, task, inst, j, err.Error())
	}

	r := vmRun{manager: m, ctx: ctx, task: task, inst: inst, j: j, sess: sess, host: host}

	err = r.advance(VMPhaseRollingBack, "restore the old files on "+j.VM)
	if err != nil {
		return err
	}

	return r.restoreAndClose()
}

// restoreAndClose restores every file a replacement touched, restarts
// the processes, runs the gate, and closes the VM rolled_back, or leaves
// it unrecovered, as rollbackVM describes. The VM is rolling_back.
func (r *vmRun) restoreAndClose() error {
	m, ctx, j := r.manager, r.ctx, r.j

	failed, err := r.restoreAll()
	if err != nil {
		return err
	}

	if failed != "" {
		return m.unrecovered(ctx, r.task, r.inst, j, failed)
	}

	result, err := r.commit("restart the processes on "+j.VM+" after the restore", nil, r.restart)

	switch {
	case errors.Is(err, errVMStopped):
		return err
	case ctx.Err() != nil:
		return errVMStopped
	case err != nil:
		return m.unrecovered(ctx, r.task, r.inst, j, "the restart after the restore did not finish in time")
	case !r.restarted(result):
		return m.unrecovered(ctx, r.task, r.inst, j, "the restart after the restore failed: "+exitMeaning(opRestart, result))
	}

	err = r.gateAfter()
	if ctx.Err() != nil {
		return errVMStopped
	}

	if err != nil {
		return m.unrecovered(ctx, r.task, r.inst, j, fmt.Sprintf("the health gate did not pass after the restore: %v", err))
	}

	return r.rolledBack()
}

// restoreAll restores every file a replacement or an earlier restore
// touched, in order. A file that already holds the old content is left as
// it is by patch.sh. It returns why a restore failed, or "" when all came
// back.
func (r *vmRun) restoreAll() (string, error) {
	for n, file := range r.files() {
		if file.State != FileStateReplaced && file.State != FileStateReplacing && file.State != FileStateRestoring {
			continue
		}

		result, err := r.commit("restore "+file.Path+" on "+r.j.VM, func(j *VMJournal) { j.Files[n].State = FileStateRestoring }, func() (ssh.Result, error) {
			return r.script(opRestore, r.jobDir(file), file.Path, file.BackupPath, file.OldHash, file.NewHash)
		})

		switch {
		case errors.Is(err, errVMStopped):
			return "", err
		case r.ctx.Err() != nil:
			return "", errVMStopped
		case err != nil:
			return "the restore of " + file.Path + " did not finish", nil
		}

		lines, ok := okLines(result)
		if !ok || len(lines) != 1 || lines[0]["state"] != "old" {
			return "the restore of " + file.Path + " failed: " + exitMeaning(opRestore, result), nil
		}

		r.change(func(j *VMJournal) { j.Files[n].State = FileStateRestored })

		err = r.save("go on after restoring " + file.Path)
		if err != nil {
			return "", err
		}
	}

	return "", nil
}

// rolledBack closes a VM whose old files are back and whose gate passed.
// It turns the VM old in the divergence record, removes its backups once
// rolled_back is saved, and pauses the task.
func (r *vmRun) rolledBack() error {
	m := r.manager

	err := r.advance(VMPhaseRolledBack, "clean up "+r.j.VM+" after its rollback")
	if err != nil {
		return err
	}

	m.mu.RLock()
	why := r.j.Error
	m.mu.RUnlock()

	err = m.recordDivergence(r.ctx, r.task, r.inst, r.j, nil, certinventory.DivergentVMOld)
	if err != nil {
		m.logger.Error("Certificate task %s could not mark VM %s old in the divergence record: %v", r.task.ID, r.j.VM, err)
	}

	err = r.cleanup()
	if err != nil {
		return err
	}

	return r.pause(rolledBackPause(why))
}

// rolledBackPause is the error a task pauses with once a VM rolled back,
// given why it rolled back.
func rolledBackPause(why string) error {
	return fmt.Errorf("%s It holds its old certificate again. Any VM already patched keeps the new one, so resuming fails the instance, which then needs a redeploy", why)
}

// logRefusedMove logs a move the VM's journal refused, which leaves the
// journal in its earlier phase beside an error that names the move. A
// nil err logs nothing.
func (m *Manager) logRefusedMove(task *UpgradeTask, vm string, err error) {
	if err != nil {
		m.logger.Error("Certificate task %s kept VM %s in its phase, because its journal refused the move: %v", task.ID, vm, err)
	}
}

// unrecovered leaves a VM whose rollback did not finish unrecovered, with
// its backups, and fails the instance.
func (m *Manager) unrecovered(ctx context.Context, task *UpgradeTask, inst *InstanceUpgrade, j *VMJournal, why string) error {
	if ctx.Err() != nil {
		return errVMStopped
	}

	message := fmt.Sprintf("VM %s could not be rolled back, because %s. %s", j.VM, why, redeployToRepair)

	m.mu.Lock()
	j.Error = message
	refused := j.Advance(VMPhaseUnrecovered)
	m.mu.Unlock()

	m.logRefusedMove(task, j.VM, refused)

	err := m.recordDivergence(ctx, task, inst, j, nil, certinventory.DivergentVMUnknown)
	if err != nil {
		m.logger.Error("Certificate task %s could not mark VM %s unknown in the divergence record: %v", task.ID, j.VM, err)
	}

	m.failRotation(ctx, task, inst, message) //nolint:errcheck,gosec // failRotation logs and saves it

	return errVMStopped
}

// forwardRank orders the phases of a VM's forward pass.
var forwardRank = map[VMPhase]int{
	VMPhasePlanned:   0,
	VMPhaseStaged:    1,
	VMPhaseBackedUp:  2,
	VMPhaseSwapping:  3,
	VMPhaseSwapped:   4,
	VMPhaseRestarted: 5,
	VMPhaseVerified:  6,
	VMPhaseDone:      7,
}

// reach advances the journal to a phase of the forward pass, as advance
// does, unless it is already there or past it, as a resumed VM can be.
func (r *vmRun) reach(phase VMPhase, step string) error {
	r.manager.mu.RLock()
	current, known := forwardRank[r.j.Phase]
	r.manager.mu.RUnlock()

	if known && current >= forwardRank[phase] {
		return nil
	}

	return r.advance(phase, step)
}

// taskSwapOpenLocked reports whether any instance of the task has a VM
// that is open and whose swap began. The caller holds mu.
func taskSwapOpenLocked(task *UpgradeTask) bool {
	for i := range task.Instances {
		if swapOpenLocked(&task.Instances[i]) {
			return true
		}
	}

	return false
}

// swapOpenLocked reports whether the instance has a VM that is open and
// whose swap began, which only a resume finishes or rolls back. The
// caller holds mu.
func swapOpenLocked(inst *InstanceUpgrade) bool {
	if inst.Certificates == nil {
		return false
	}

	for i := range inst.Certificates.VMs {
		j := &inst.Certificates.VMs[i]
		if !closedPhase(j.Phase) && journalSwapBegun(j) {
			return true
		}
	}

	return false
}

// patchResume is one resume of a patch instance whose VMs a pass already
// began to patch.
type patchResume struct {
	m *Manager
	//nolint:containedctx // a resume lives as long as this context
	ctx      context.Context
	task     *UpgradeTask
	inst     *InstanceUpgrade
	sessions *patchSessions
	pairs    map[string]*credhub.KeyPair

	// cancelCh cancels the converge deploy the immediate policy sends once
	// the resume patched every VM.
	cancelCh <-chan struct{}

	// refused is the switches' refusal at the start, and cancelled whether
	// the task was cancelled then.
	refused   error
	cancelled bool

	// loaded and loadFailed record the one read of the new key pairs, and
	// loadWhy why it failed.
	loaded, loadFailed bool
	loadWhy            string

	// gated and rechecked record that the gate and the BeforeMutation
	// re-check ran in this resume.
	gated, rechecked bool
}

// resumePatch resumes an instance whose VMs a pass already began to
// patch, after a restart, a pause, a panic, or a cancel. With the patch
// switch off and no VM part way through its swap, it opens no SSH session,
// changes no journal and not the divergence record, and pauses the task.
// Otherwise it first puts every VM a replacement may have touched in the
// divergence record, as unknown unless the record already lists it. It
// then takes each VM in journal order: a closed VM whose cleanup is not
// done is cleaned up, an open one is checked against the director and its
// files with patch.sh check and handled as DecideResume says, through the
// functions a live pass uses. A VM whose swap began finishes or rolls back
// whatever the switches say. A cancelled task handles only such VMs and
// those part way through a swap or a rollback, which a cancel never
// interrupts. The VMs no pass touched yet are then patched as a live pass
// does. Every key pair it read is zeroed after the sessions closed. It
// reports whether the instance settled.
func (m *Manager) resumePatch(ctx context.Context, task *UpgradeTask, inst *InstanceUpgrade, cancelCh <-chan struct{}) bool {
	pairs := make(map[string]*credhub.KeyPair)
	defer zeroPairs(pairs)

	sessions := m.newPatchSessions(inst.DeploymentName)
	defer sessions.close()

	r := &patchResume{
		m: m, ctx: ctx, task: task, inst: inst, sessions: sessions, pairs: pairs, cancelCh: cancelCh,
		refused:   m.certWritesAllowed(RotationModePatch),
		cancelled: m.taskCancelled(task),
	}

	m.mu.RLock()
	open := swapOpenLocked(inst)
	m.mu.RUnlock()

	if r.refused != nil && !open {
		if !r.cancelled {
			m.pauseWith(ctx, task, inst, writesRefused(r.refused)) //nolint:errcheck,gosec // pauseWith logs and saves it
		}

		return m.instanceSettled(inst)
	}

	if !r.cancelled {
		m.markRotating(ctx, task, inst)
	}

	if r.rebuildDivergence() {
		return m.instanceSettled(inst)
	}

	m.mu.RLock()
	count := len(inst.Certificates.VMs)
	m.mu.RUnlock()

	for i := range count {
		if r.resumeVM(&inst.Certificates.VMs[i]) {
			return m.instanceSettled(inst)
		}
	}

	return r.patchRest()
}

// patchRest patches the VMs no pass touched yet, once every VM the
// resume found open is closed. A cancelled task leaves them as they are,
// and switches that refuse pause the task first.
func (r *patchResume) patchRest() bool {
	m := r.m

	if r.cancelled {
		return m.instanceSettled(r.inst)
	}

	err := m.certWritesAllowed(RotationModePatch)
	if err != nil {
		m.pauseWith(r.ctx, r.task, r.inst, writesRefused(err)) //nolint:errcheck,gosec // pauseWith logs and saves it

		return m.instanceSettled(r.inst)
	}

	m.mu.RLock()

	untouchedLeft := slices.ContainsFunc(r.inst.Certificates.VMs, func(j VMJournal) bool { return untouched(&j) })

	m.mu.RUnlock()

	if untouchedLeft {
		if !r.loadPairs() {
			m.pauseWith(r.ctx, r.task, r.inst, errors.New(r.loadWhy)) //nolint:errcheck,gosec // pauseWith logs and saves it

			return m.instanceSettled(r.inst)
		}

		if !r.rechecked && m.stopForChanges(r.ctx, r.task, r.inst, BeforeMutation) {
			return m.instanceSettled(r.inst)
		}
	}

	return m.patchVMs(r.ctx, r.task, r.inst, r.sessions, r.pairs, r.cancelCh)
}

// rebuildDivergence makes sure the divergence record lists every open VM
// a replacement may have touched, setting each it does not list as
// applied or unknown to unknown. It reports whether the task paused.
func (r *patchResume) rebuildDivergence() bool {
	m := r.m

	m.mu.RLock()

	var touched []int

	for i := range r.inst.Certificates.VMs {
		j := &r.inst.Certificates.VMs[i]
		if !closedPhase(j.Phase) && slices.ContainsFunc(j.Files, replacementTouched) {
			touched = append(touched, i)
		}
	}

	m.mu.RUnlock()

	if len(touched) == 0 {
		return false
	}

	store := m.certDeps().Divergence
	if store == nil {
		m.pauseWith(r.ctx, r.task, r.inst, errNoDivergenceStore) //nolint:errcheck,gosec // pauseWith logs and saves it

		return true
	}

	record, _, err := store.Get(r.ctx, r.inst.InstanceID)
	if err != nil {
		m.pauseWith(r.ctx, r.task, r.inst, fmt.Errorf("The divergence record could not be read, so the resume changed nothing: %w", err)) //nolint:errcheck,gosec // pauseWith logs and saves it

		return true
	}

	for _, i := range touched {
		j := &r.inst.Certificates.VMs[i]
		if r.listed(record, j) {
			continue
		}

		var prints map[string]string
		if r.loadPairs() {
			prints = fingerprints(r.pairs)
		}

		err := m.recordDivergence(r.ctx, r.task, r.inst, j, prints, certinventory.DivergentVMUnknown)
		if err != nil {
			m.pauseWith(r.ctx, r.task, r.inst, fmt.Errorf("The divergence record of VM %s could not be saved, so the resume changed nothing: %w", j.VM, err)) //nolint:errcheck,gosec // pauseWith logs and saves it

			return true
		}
	}

	return false
}

// replacementTouched reports whether a replacement may have changed the
// file.
func replacementTouched(file FileEntry) bool {
	switch file.State {
	case FileStateReplacing, FileStateReplaced, FileStateRestoring, FileStateRestored:
		return true
	default:
		return false
	}
}

// listed reports whether the record lists the VM as applied or unknown
// for every variable its files hold.
func (r *patchResume) listed(record *certinventory.Divergence, j *VMJournal) bool {
	if record == nil {
		return false
	}

	r.m.mu.RLock()
	cid := j.CID
	files := slices.Clone(j.Files)
	r.m.mu.RUnlock()

	for _, file := range files {
		entry := slices.IndexFunc(record.Variables, func(v certinventory.DivergentVariable) bool { return v.Name == file.Variable })
		if entry < 0 {
			return false
		}

		state := record.Variables[entry].VMs[cid].State
		if state != certinventory.DivergentVMApplied && state != certinventory.DivergentVMUnknown {
			return false
		}
	}

	return true
}

// resumeVM takes one VM as resumePatch describes. It reports whether the
// instance stopped.
func (r *patchResume) resumeVM(j *VMJournal) bool {
	r.m.mu.RLock()
	phase, cleanup := j.Phase, j.Cleanup
	begun, fresh := journalSwapBegun(j), untouched(j)
	r.m.mu.RUnlock()

	switch {
	case phase == VMPhaseReverted, closedPhase(phase) && cleanup == CleanupDone:
		return false
	case closedPhase(phase):
		return r.closeVM(j)
	case fresh, !begun && (r.refused != nil || r.cancelled && !midVM(phase)):
		return false
	default:
		return r.openVM(j, begun)
	}
}

// midVM reports whether a VM in the phase is part way through its swap
// or its rollback, which a cancel never interrupts.
func midVM(phase VMPhase) bool {
	switch phase {
	case VMPhaseSwapping, VMPhaseSwapped, VMPhaseRestarted, VMPhaseVerified, VMPhaseRollingBack:
		return true
	default:
		return false
	}
}

// session opens the session of the VM's group and finds the VM in it. A
// failure pauses the task and returns errVMStopped.
func (r *patchResume) session(run *vmRun) error {
	sess, err := r.m.journalSession(r.ctx, r.sessions, r.inst, run.j)
	if err == nil {
		run.sess = sess
		run.host, err = journalHost(sess, run.j)
	}

	if err != nil {
		return run.pause(fmt.Errorf("Blacksmith could not open an SSH session on %s, so the resume left it as it was: %w", run.j.VM, err))
	}

	return nil
}

// closeVM finishes a closed VM whose cleanup is not done. A rolled_back
// VM whose cleanup a crash cut off is marked old in the divergence record,
// cleaned up, and pauses the task as its rollback would have. Any other
// closed VM is cleaned up and the resume goes on. With the switches
// refusing, only the rolled_back VM's record and pause are done, and the
// cleanups wait for a resume they allow. It reports whether the instance
// stopped.
func (r *patchResume) closeVM(j *VMJournal) bool {
	m := r.m

	m.mu.RLock()
	phase, cleanup, why := j.Phase, j.Cleanup, j.Error
	m.mu.RUnlock()

	rolledBack := phase == VMPhaseRolledBack && cleanup != CleanupFailed
	if r.refused != nil && !rolledBack {
		return false
	}

	run := &vmRun{manager: m, ctx: r.ctx, task: r.task, inst: r.inst, j: j}

	if rolledBack {
		err := m.recordDivergence(r.ctx, r.task, r.inst, j, nil, certinventory.DivergentVMOld)
		if err != nil {
			m.logger.Error("Certificate task %s could not mark VM %s old in the divergence record: %v", r.task.ID, j.VM, err)
		}
	}

	if r.refused == nil {
		if r.session(run) != nil || run.cleanup() != nil {
			return true
		}
	}

	if !rolledBack {
		return false
	}

	if why == "" {
		why = fmt.Sprintf("VM %s was rolled back.", j.VM)
	}

	run.pause(rolledBackPause(why)) //nolint:errcheck,gosec // pause logs and saves it

	return true
}

// openVM reads what an open VM holds and runs the action DecideResume
// picks for it. It reports whether the instance stopped.
func (r *patchResume) openVM(j *VMJournal, begun bool) bool {
	run := &vmRun{manager: r.m, ctx: r.ctx, task: r.task, inst: r.inst, j: j}

	cid, err := run.directorCID("was left as it was")
	if err != nil {
		return true
	}

	live := LiveState{CID: cid, FileHashes: map[string]string{}, BackupHashes: map[string]string{}}

	r.m.mu.RLock()
	journalCID := j.CID
	r.m.mu.RUnlock()

	if cid != "" && cid == journalCID {
		if r.session(run) != nil || run.checkFiles(&live) != nil {
			return true
		}
	}

	r.m.mu.RLock()
	action, why := DecideResume(j, live)
	r.m.mu.RUnlock()

	r.m.logger.Info("Certificate task %s resumes VM %s with %s: %s", r.task.ID, j.VM, action, why)

	return r.act(run, action, why, live, begun) != nil
}

// act runs one resume action on the VM. It returns nil when the VM is
// closed and the resume goes on, and errVMStopped once the task paused
// or the instance stopped.
func (r *patchResume) act(run *vmRun, action ResumeAction, why string, live LiveState, begun bool) error {
	switch action { //nolint:exhaustive // the default case closes every other action unrecovered
	case ResumeContinueStaging, ResumeContinueBackups:
		return r.continueForward(run)
	case ResumeCompleteForward:
		return r.completeForward(run, live, begun)
	case ResumeRestartForward:
		return r.restartForward(run, begun)
	case ResumeVerifyAgain:
		if !r.loadPairs() {
			return run.rollback("the new key pair could not be read again: " + r.loadWhy)
		}

		return run.finishVerify(leavesOf(r.pairs))
	case ResumeRestoreAll, ResumeCompleteRestore, ResumeRestartRollback:
		run.change(func(j *VMJournal) {
			if j.Error == "" {
				j.Error = fmt.Sprintf("VM %s was rolled back because a resume found its rollback unfinished.", j.VM)
			}
		})

		return run.restoreAndClose()
	case ResumeMarkReverted:
		return r.reverted(run, why, begun)
	case ResumeRetryLater:
		return run.pause(fmt.Errorf("VM %s was left as it was: %s", run.j.VM, why))
	default:
		return r.unrecovered(run, why, begun)
	}
}

// continueForward takes a VM whose files all hold the old content from
// staging to done, as a live pass does. It runs the gate and the
// BeforeMutation re-check first, once per resume, reads the new key pairs
// again, and stages again only the copies that are gone or wrong. Its
// backups are reused.
func (r *patchResume) continueForward(run *vmRun) error {
	if !r.gated {
		r.gated = true

		r.m.mu.RLock()
		gate := r.inst.Certificates.Gate
		vmCount := len(r.inst.Certificates.Approved.VMCIDs)
		r.m.mu.RUnlock()

		if r.m.gateBeforeVM(r.ctx, r.task, r.inst, run.j, run.sess, gate, vmCount) {
			return errVMStopped
		}
	}

	if !r.rechecked {
		r.rechecked = true

		if r.m.stopForChanges(r.ctx, r.task, r.inst, BeforeMutation) {
			return errVMStopped
		}
	}

	if !r.loadPairs() {
		return run.pause(fmt.Errorf("VM %s was left as it was: %s", run.j.VM, r.loadWhy))
	}

	for _, step := range []func() error{
		func() error { return r.restage(run, nil, run.stopUnchanged) },
		func() error { return run.reach(VMPhaseStaged, "back up the files on "+run.j.VM) },
		run.backup,
	} {
		err := step()
		if err != nil {
			return err
		}
	}

	return r.swapForward(run)
}

// swapForward takes a backed-up VM, or one whose swap a crash cut off,
// through its replacements and on to done.
func (r *patchResume) swapForward(run *vmRun) error {
	for _, step := range []func() error{
		func() error { return run.diverge(r.pairs, certinventory.DivergentVMUnknown) },
		func() error { return run.reach(VMPhaseSwapping, "replace the files on "+run.j.VM) },
		run.replace,
		func() error { return run.diverge(r.pairs, certinventory.DivergentVMApplied) },
		func() error { return run.reach(VMPhaseSwapped, "restart the processes on "+run.j.VM) },
	} {
		err := step()
		if err != nil {
			return err
		}
	}

	return run.finish(leavesOf(r.pairs))
}

// completeForward completes the replacements of a VM whose swap a crash
// cut off, with every backup in place. It stages again each copy a file
// that still holds the old content needs, when the copy is gone. A VM
// whose swap began rolls back when the key pairs can't be read again or
// staged, and one whose swap had not begun stops unchanged.
func (r *patchResume) completeForward(run *vmRun, live LiveState, begun bool) error {
	fail := func(why string, finished bool) error {
		if begun {
			return run.rollback(why)
		}

		return run.stopUnchanged(why, finished)
	}

	if !r.loadPairs() {
		return fail("the new key pair could not be read again: "+r.loadWhy, true)
	}

	old := func(file FileEntry) bool { return live.FileHashes[file.Path] == file.OldHash }

	err := r.restage(run, old, fail)
	if err != nil {
		return err
	}

	return r.swapForward(run)
}

// restartForward finishes a VM whose files all hold the new content. Each
// file is marked replaced, so a rollback restores it, and the VM restarts
// and goes on to done.
func (r *patchResume) restartForward(run *vmRun, begun bool) error {
	if !r.loadPairs() {
		if begun {
			return run.rollback("the new key pair could not be read again: " + r.loadWhy)
		}

		return run.pause(fmt.Errorf("VM %s was left as it was: %s", run.j.VM, r.loadWhy))
	}

	run.change(func(j *VMJournal) {
		for n := range j.Files {
			j.Files[n].State = FileStateReplaced
		}
	})

	err := run.save("go on with the restart of " + run.j.VM)
	if err != nil {
		return err
	}

	err = run.diverge(r.pairs, certinventory.DivergentVMApplied)
	if err != nil {
		return err
	}

	err = run.reach(VMPhaseSwapped, "restart the processes on "+run.j.VM)
	if err != nil {
		return err
	}

	return run.finish(leavesOf(r.pairs))
}

// restage prepares the work directory and stages again each file that
// needs, for which need says so (every file when need is nil), and whose
// staged copy is gone or wrong. A failure stops the VM through fail.
func (r *patchResume) restage(run *vmRun, need func(FileEntry) bool, fail func(why string, finished bool) error) error {
	files := run.files()

	var missing []int

	for n, file := range files {
		if need != nil && !need(file) {
			continue
		}

		result, err := run.script(opStageCheck, workPath(r.task.ID, "s", n), file.NewHash)

		switch {
		case err != nil && r.ctx.Err() != nil:
			return errVMStopped
		case err != nil:
			return fail("the check of a staged copy did not finish", false)
		case !isOK(result):
			missing = append(missing, n)
		}
	}

	if len(missing) == 0 {
		return nil
	}

	result, err := run.mutate("prepare VM "+run.j.VM, func(j *VMJournal) { j.Cleanup = CleanupPending }, func() (ssh.Result, error) {
		return run.script(opPrepare)
	})

	switch {
	case errors.Is(err, errVMStopped):
		return err
	case err != nil:
		return fail("the work directory was not made, because the command did not finish", false)
	case !isOK(result):
		return fail(exitMeaning(opPrepare, result), true)
	}

	for _, n := range missing {
		file := files[n]

		err := run.stageFile(n, file, r.pairs, fail)
		if err != nil {
			return err
		}

		result, err := run.script(opStageCheck, workPath(r.task.ID, "s", n), file.NewHash)

		switch {
		case err != nil:
			return fail("the check of a staged copy did not finish", false)
		case !isOK(result):
			return fail("the staged copy of "+file.Path+" does not hold the new content: "+exitMeaning(opStageCheck, result), true)
		}
	}

	return nil
}

// reverted closes a VM that BOSH recreated, which holds the old files
// again. It notes the VM in the divergence record when a replacement may
// have reached it, and pauses the task.
func (r *patchResume) reverted(run *vmRun, why string, begun bool) error {
	var refused error

	run.change(func(j *VMJournal) {
		j.Error = why
		refused = j.Advance(VMPhaseReverted)
	})

	r.m.logRefusedMove(r.task, run.j.VM, refused)

	err := run.save("record that VM " + run.j.VM + " was recreated")
	if err != nil {
		return err
	}

	if begun {
		err := r.m.recordDivergence(r.ctx, r.task, r.inst, run.j, nil, certinventory.DivergentVMReverted)
		if err != nil {
			r.m.logger.Error("Certificate task %s could not mark VM %s reverted in the divergence record: %v", r.task.ID, run.j.VM, err)
		}
	}

	return run.pause(fmt.Errorf("VM %s was not patched further: %s", run.j.VM, why))
}

// unrecovered leaves an open VM whose live state no resume rule covers
// unrecovered, notes it as unknown in the divergence record when a
// replacement may have reached it, and pauses the task. Nothing on the VM
// is overwritten.
func (r *patchResume) unrecovered(run *vmRun, why string, begun bool) error {
	message := fmt.Sprintf("VM %s was left unrecovered: %s %s", run.j.VM, why, redeployToRepair)

	var refused error

	run.change(func(j *VMJournal) {
		j.Error = message
		refused = j.Advance(VMPhaseUnrecovered)
	})

	r.m.logRefusedMove(r.task, run.j.VM, refused)

	err := run.save("record that VM " + run.j.VM + " is unrecovered")
	if err != nil {
		return err
	}

	if begun {
		err := r.m.recordDivergence(r.ctx, r.task, r.inst, run.j, nil, certinventory.DivergentVMUnknown)
		if err != nil {
			r.m.logger.Error("Certificate task %s could not mark VM %s unknown in the divergence record: %v", r.task.ID, run.j.VM, err)
		}
	}

	return run.pause(errors.New(message))
}

// loadPairs reads the key pair of each variable's new version from
// CredHub once per resume, and checks its certificate and key against
// the hashes the regeneration saved. A pair that does not match is never
// used. It reports whether every pair was read and matched, and on a
// failure loadWhy says why, with no hash or key in it.
func (r *patchResume) loadPairs() bool {
	switch {
	case r.loaded:
		return true
	case r.loadFailed:
		return false
	}

	r.loadFailed = true

	api := r.m.certDeps().CredHub
	if api == nil {
		r.loadWhy = "CredHub is not configured, so the new key pairs can't be read again."

		return false
	}

	r.m.mu.RLock()

	variables := make([]VariableRotation, 0, len(r.inst.Certificates.Variables))
	for _, v := range r.inst.Certificates.Variables {
		variables = append(variables, v.Clone())
	}

	r.m.mu.RUnlock()

	for _, v := range variables {
		pair, err := api.CertificateKeyPair(r.ctx, v.NewVersionID)
		if err != nil {
			r.loadWhy = fmt.Sprintf("CredHub did not answer the read of version %s of %s: %v", v.NewVersionID, shortName(v.Name), err)

			return false
		}

		if old := r.pairs[v.Name]; old != nil {
			old.Zero()
		}

		r.pairs[v.Name] = pair

		if v.NewCertHash == "" || v.NewKeyHash == "" ||
			certpatch.NormalisedHash(pair.Certificate) != v.NewCertHash || certpatch.NormalisedHash(pair.PrivateKey) != v.NewKeyHash {
			r.loadWhy = fmt.Sprintf("CredHub answered version %s of %s with a key pair other than the one Blacksmith validated.", v.NewVersionID, shortName(v.Name))

			return false
		}
	}

	r.loaded, r.loadFailed = true, false

	return true
}

// openPatchVMLocked finds the first running instance of a patch task with
// a VM that a pass touched and left open, and names that VM. It returns
// nil for any other task. The caller holds mu.
func openPatchVMLocked(task *UpgradeTask) (*InstanceUpgrade, string) {
	if task.KindOrDefault() != TaskKindCertificates || certTaskMode(task) != RotationModePatch {
		return nil, ""
	}

	for i := range task.Instances {
		inst := &task.Instances[i]
		if inst.Status != InstanceStatusRunning || inst.Certificates == nil {
			continue
		}

		for n := range inst.Certificates.VMs {
			j := &inst.Certificates.VMs[n]
			if !closedPhase(j.Phase) && !untouched(j) {
				return inst, j.VM
			}
		}
	}

	return nil, ""
}

// resumeCancelledLocked starts the resume of each running instance of a
// cancelled patch task with a VM part way through its swap or its
// rollback, as patchCancelEnd finds, when nothing processes or settles
// the task and it does not wait for a resume. A cancel stops only new VMs
// from starting, so such a VM is resumed as a live task's is, and without
// it the VM would stay half swapped and its instance reserved with
// nothing to follow it. Each instance counts as a settle of the task, so
// nothing else ends it meanwhile, and the function it returns starts the
// resumes, which the caller runs once it lets go of mu. A manager that is
// stopping, or has no director, starts none, since the next load does
// it. The caller holds mu.
func (m *Manager) resumeCancelledLocked(task *UpgradeTask) func() {
	if task.Status != TaskStatusCancelled || task.Paused || task.KindOrDefault() != TaskKindCertificates || certTaskMode(task) != RotationModePatch ||
		!m.configured || m.director == nil || m.owned[task.ID] || m.settling[task.ID] > 0 || m.ctx.Err() != nil {
		return func() {}
	}

	var open, clean []*InstanceUpgrade

	for i := range task.Instances {
		inst := &task.Instances[i]

		switch {
		case cancelledVMOpenLocked(inst):
			open = append(open, inst)
		case m.cancelledCleanupDueLocked(task, inst):
			m.cancelCleaned[cleanupKey(task, inst)] = true
			clean = append(clean, inst)
		}
	}

	if len(open)+len(clean) == 0 {
		return func() {}
	}

	m.settling[task.ID] += len(open) + len(clean)
	m.wg.Add(len(open) + len(clean))

	return func() {
		for _, inst := range open {
			go m.resumeCancelledPatch(task, inst)
		}

		for _, inst := range clean {
			go m.cleanupCancelledPatch(task, inst)
		}
	}
}

// cleanupKey names an instance of a task in cancelCleaned.
func cleanupKey(task *UpgradeTask, inst *InstanceUpgrade) string {
	return task.ID + "/" + inst.InstanceID
}

// cancelledCleanupDueLocked reports whether a running instance of a
// cancelled patch task, with no VM part way through its swap, has a VM
// whose staging and backup files still need removing, and whether this
// manager can remove them now: it has a director, patch in place is on,
// and it has not run the instance's cleanup pass yet. Each pass runs
// once, so a cleanup that fails is recorded and never holds the instance.
// The caller holds mu.
func (m *Manager) cancelledCleanupDueLocked(task *UpgradeTask, inst *InstanceUpgrade) bool {
	if task.Status != TaskStatusCancelled || certTaskMode(task) != RotationModePatch || inst.Status != InstanceStatusRunning ||
		inst.Certificates == nil || !m.configured || m.director == nil || m.ctx.Err() != nil || m.cancelCleaned[cleanupKey(task, inst)] {
		return false
	}

	if cancelledVMOpenLocked(inst) || !slices.ContainsFunc(inst.Certificates.VMs, cleanupDue) {
		return false
	}

	return m.certWritesAllowedLocked(RotationModePatch) == nil
}

// cleanupDue reports whether a VM a cancelled task leaves as it is still
// holds the task's staging and backup files: its cleanup is pending or
// failed, and it is neither part way through its swap nor recreated.
func cleanupDue(j VMJournal) bool {
	return (j.Cleanup == CleanupPending || j.Cleanup == CleanupFailed) && !midVM(j.Phase) && j.Phase != VMPhaseReverted
}

// cleanupCancelledPatch removes the task's staging and backup files from
// each VM of a cancelled task's instance that still holds them, as spec
// step 8 has a later pass do. Nothing else changes on the VMs. A
// rolled-back VM is marked old in the divergence record first, as the
// resume of one does. A VM whose session can't be opened, or whose
// cleanup fails, has its cleanup recorded as failed. The instance ends
// once the pass is over, however the cleanups went.
func (m *Manager) cleanupCancelledPatch(task *UpgradeTask, inst *InstanceUpgrade) {
	defer m.wg.Done()
	defer m.endSettling(loadedDeploy{task: task, instance: inst})

	defer func() {
		if r := recover(); r != nil {
			m.logger.Error("PANIC while certificate task %s cleaned up the VMs of instance %s: %v", task.ID, inst.InstanceID, r)
		}
	}()

	m.logger.Info("Certificate task %s is cancelled, so it removes its staging and backup files from the VMs of instance %s", task.ID, inst.InstanceID)

	sessions := m.newPatchSessions(inst.DeploymentName)
	defer sessions.close()

	m.mu.RLock()
	count := len(inst.Certificates.VMs)
	m.mu.RUnlock()

	for i := range count {
		j := &inst.Certificates.VMs[i]

		m.mu.RLock()
		due, rolledBack := cleanupDue(*j), j.Phase == VMPhaseRolledBack
		m.mu.RUnlock()

		if !due {
			continue
		}

		if m.cleanupCancelledVM(sessions, task, inst, j, rolledBack) != nil {
			return
		}
	}
}

// cleanupCancelledVM runs the cleanup on one VM of a cancelled task. It
// returns an error only when the manager stops or a save is not
// confirmed, which ends the pass.
func (m *Manager) cleanupCancelledVM(sessions *patchSessions, task *UpgradeTask, inst *InstanceUpgrade, j *VMJournal, rolledBack bool) error {
	run := &vmRun{manager: m, ctx: m.ctx, task: task, inst: inst, j: j}

	if rolledBack {
		err := m.recordDivergence(m.ctx, task, inst, j, nil, certinventory.DivergentVMOld)
		if err != nil {
			m.logger.Error("Certificate task %s could not mark VM %s old in the divergence record: %v", task.ID, j.VM, err)
		}
	}

	sess, err := m.journalSession(m.ctx, sessions, inst, j)
	if err == nil {
		run.sess = sess
		run.host, err = journalHost(sess, j)
	}

	if err != nil {
		if m.ctx.Err() != nil {
			return errVMStopped
		}

		m.logger.Error("Certificate task %s could not open an SSH session on VM %s, so its staging and backup files stay: %v", task.ID, j.VM, err)
		run.change(func(j *VMJournal) { j.Cleanup = CleanupFailed })

		return run.save("record the failed cleanup of " + j.VM)
	}

	return run.cleanup()
}

// waitsCancelledLocked reports whether the task is a cancelled patch task
// whose open VM waits for a resume, as awaitCancelledResume leaves it. The
// caller holds mu.
func waitsCancelledLocked(task *UpgradeTask) bool {
	return task.Status == TaskStatusCancelled && task.Paused && task.KindOrDefault() == TaskKindCertificates &&
		certTaskMode(task) == RotationModePatch
}

// cancelledVMOpenLocked reports whether a running instance of a cancelled
// task has a VM part way through its swap or its rollback, which keeps
// the instance open until a resume closes it. The caller holds mu.
func cancelledVMOpenLocked(inst *InstanceUpgrade) bool {
	if inst.Status != InstanceStatusRunning {
		return false
	}

	_, _, end := patchCancelEnd(inst, cancelledUnchanged)

	return !end
}

// resumeCancelledPatch resumes the VMs of one instance that
// resumeCancelledLocked counted as a settle of its cancelled task, as a
// live task's resume does, and ends that settle. A resume that leaves a
// VM open, because a read failed or the resume table says to retry later,
// runs again, verifyInterval apart at first and twice as long each time
// after, up to maxVerifyBackoff, until no VM is open or
// cancelledResumeWait passes. Only an unrecovered outcome of the resume
// table closes a VM the resume could not finish. A VM still open at the
// deadline, after a panic, or while patch in place is off with its swap
// not begun stays as it is, its instance stays reserved, and the task
// waits for a resume. A manager that is stopping leaves it for the next
// load.
func (m *Manager) resumeCancelledPatch(task *UpgradeTask, inst *InstanceUpgrade) {
	defer m.wg.Done()
	defer m.endSettling(loadedDeploy{task: task, instance: inst})

	m.logger.Info("Certificate task %s is cancelled with a VM of instance %s part way through its swap, so it finishes that VM now", task.ID, inst.InstanceID)

	deadline := time.Now().Add(m.cancelledResumeWait)
	backoff := m.verifyInterval

	for {
		panicked := !m.resumeCancelledOnce(task, inst)

		m.mu.RLock()
		open, begun, last := cancelledVMOpenLocked(inst), swapOpenLocked(inst), inst.Error
		m.mu.RUnlock()

		if m.ctx.Err() != nil || !open {
			return
		}

		refused := m.certWritesAllowed(RotationModePatch)

		switch {
		case panicked:
			m.awaitCancelledResume(task, inst, "A panic stopped its resume.")

			return
		case refused != nil && !begun:
			m.awaitCancelledResume(task, inst, refusalMessage(writesRefused(refused)))

			return
		case !time.Now().Add(backoff).Before(deadline):
			m.awaitCancelledResume(task, inst, fmt.Sprintf("Blacksmith could not finish it within %v. The last attempt said: %s",
				m.cancelledResumeWait, sentence(last)))

			return
		}

		m.logger.Error("Certificate task %s is cancelled, so instance %s resumes its open VM again in %s: %s", task.ID, inst.InstanceID, backoff, last)

		select {
		case <-m.ctx.Done():
			return
		case <-time.After(backoff):
		}

		backoff = min(2*backoff, maxVerifyBackoff)
	}
}

// resumeCancelledOnce runs one resume of a cancelled task's instance. It
// reports whether the resume ran to its end, which it does not when it
// panics, and it recovers from the panic.
func (m *Manager) resumeCancelledOnce(task *UpgradeTask, inst *InstanceUpgrade) bool {
	defer func() {
		if r := recover(); r != nil {
			m.logger.Error("PANIC while certificate task %s finished the VMs of instance %s: %v", task.ID, inst.InstanceID, r)
		}
	}()

	// A cancelled task patches no VM a pass left untouched, so it never
	// reaches the converge that a cancel channel would stop.
	m.resumePatch(m.ctx, task, inst, nil)

	return true
}

// sentence ends the text with a period unless it ends a sentence already.
func sentence(text string) string {
	text = strings.TrimSpace(text)
	if text == "" || strings.HasSuffix(text, ".") {
		return text
	}

	return text + "."
}

// refusalMessage is the message of a refusal, without its code.
func refusalMessage(err error) string {
	var apiErr *apierr.Error
	if errors.As(err, &apiErr) {
		return apiErr.Message
	}

	return err.Error()
}

// awaitCancelledResume leaves the open VM of a cancelled task's instance
// as it is, with the instance reserved, and has the task wait for a
// resume, saying why. The task stays cancelled.
func (m *Manager) awaitCancelledResume(task *UpgradeTask, inst *InstanceUpgrade, why string) {
	m.mu.Lock()

	openVM := ""

	for _, j := range inst.Certificates.VMs {
		if midVM(j.Phase) {
			openVM = j.VM

			break
		}
	}

	task.Paused = true
	inst.Error = fmt.Sprintf("The task was cancelled with VM %s part way through its swap or its rollback. %s "+
		"The VM stays as it is and the instance stays reserved. Resume the task to finish the VM.", openVM, sentence(why))

	message := inst.Error

	m.mu.Unlock()

	m.logger.Error("Certificate task %s waits for a resume: %s", task.ID, message)

	err := m.persistCertConfirmed(context.WithoutCancel(m.ctx), task)
	if err != nil {
		m.logger.Error("Certificate task %s: %v", task.ID, err)
	}
}
