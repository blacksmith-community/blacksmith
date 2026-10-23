package upgrade

import (
	"context"
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

	return &patchPlan{journals: journals, consumers: consumers, gate: gate, vms: len(scan.VMs)}, nil
}
