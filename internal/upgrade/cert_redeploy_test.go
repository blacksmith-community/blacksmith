//nolint:testpackage // the tests drive the manager's unexported redeploy runner
package upgrade

import (
	"context"
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"blacksmith/internal/apierr"
	"blacksmith/internal/bosh"
	"blacksmith/internal/certinventory"
	"blacksmith/internal/config"
	"blacksmith/internal/credhub"
	"blacksmith/internal/mutation"
	"blacksmith/pkg/logger"
)

const (
	// redeployTaskID is the BOSH task the test director names for a deploy.
	redeployTaskID = 4711
	// redeployVMCID is the cloud ID of the instance's one VM.
	redeployVMCID = "vm-cid-1"
	// redeployDirectorName is the director's name in every CredHub path.
	redeployDirectorName = "ws-bosh"
	// redeployManifest is the director's manifest of the instance. Its one
	// job serves the leaf on a TLS port, which the test fills in.
	redeployManifest = `name: redis-instance-1
instance_groups:
- name: redis
  instances: 1
  jobs:
  - name: rabbitmq-server
    properties:
      rabbitmq:
        tls:
          enabled: true
          port: %d
          crt: ((redis_crt.certificate))
`
)

// tlsListener is a local TLS listener that presents the certificate the
// test gives it. While it is held, each connection waits before its
// handshake until the test releases it.
type tlsListener struct {
	ln net.Listener

	mu       sync.Mutex
	pair     tls.Certificate
	gate     chan struct{}
	accepted chan struct{}
	signal   sync.Once
}

func startTLSListener(t *testing.T, cert *x509.Certificate, key *ecdsa.PrivateKey) *tlsListener {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}

	l := &tlsListener{ln: ln}
	l.serve(cert, key)

	go l.acceptLoop()

	t.Cleanup(func() {
		_ = ln.Close()

		l.mu.Lock()
		gate := l.gate
		l.gate = nil
		l.mu.Unlock()

		if gate != nil {
			close(gate)
		}
	})

	return l
}

func (l *tlsListener) acceptLoop() {
	for {
		conn, err := l.ln.Accept()
		if err != nil {
			return
		}

		go l.handle(conn)
	}
}

func (l *tlsListener) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()

	l.mu.Lock()
	gate, accepted := l.gate, l.accepted
	l.mu.Unlock()

	if gate != nil {
		l.signal.Do(func() { close(accepted) })
		<-gate
	}

	l.mu.Lock()
	pair := l.pair
	l.mu.Unlock()

	server := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12})

	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
	defer cancel()

	_ = server.HandshakeContext(ctx)
}

// serve makes the listener present the certificate from now on.
func (l *tlsListener) serve(cert *x509.Certificate, key *ecdsa.PrivateKey) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.pair = tls.Certificate{Certificate: [][]byte{cert.Raw}, PrivateKey: key, Leaf: cert}
}

// hold makes each connection wait before its handshake. The channel it
// returns closes when the first held connection arrives, and release lets
// every held connection go on.
func (l *tlsListener) hold() (<-chan struct{}, func()) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.gate = make(chan struct{})
	l.accepted = make(chan struct{})
	l.signal = sync.Once{}

	gate := l.gate

	return l.accepted, func() {
		l.mu.Lock()
		defer l.mu.Unlock()

		if l.gate == gate {
			l.gate = nil

			close(gate)
		}
	}
}

func (l *tlsListener) port() int {
	addr, ok := l.ln.Addr().(*net.TCPAddr)
	if !ok {
		return 0
	}

	return addr.Port
}

// redeploySent is one deploy the test director received.
type redeploySent struct {
	manifest string
	opts     bosh.DeployOptions
}

// redeployDirector is the director of one deployment whose one VM runs at
// 127.0.0.1. Its deploy names task 4711, and the wait on a task ends as
// the test's finish says, done by default. A done task applies the new
// variable version, which ListVariables then names, and runs onDone. A
// call it does not script panics through the nil embedded Director.
type redeployDirector struct {
	bosh.Director

	manifest string

	mu       sync.Mutex
	deployed string
	sent     []redeploySent
	waits    []int
	cancels  []int
	finish   func(ctx context.Context, taskID int) (*bosh.Task, error)
	applies  string
	onDone   func()

	// listFails is how many ListVariables calls fail before one succeeds,
	// and a negative count fails every call. lists counts the calls.
	listFails int
	lists     int

	// nameless and empty make GetInfo and GetDeployment answer with
	// nothing and no error.
	nameless bool
	empty    bool

	// taskState is the state GetTask reports, and done when it is empty.
	taskState string

	// onGetDeployment runs on each GetDeployment call.
	onGetDeployment func()
}

func (d *redeployDirector) GetTask(_ context.Context, taskID int) (*bosh.Task, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	state := d.taskState
	if state == "" {
		state = boshTaskDone
	}

	return &bosh.Task{ID: taskID, State: state, Deployment: waitDeployment}, nil
}

// runTask makes GetTask report the director task as running, or as done
// when running is false.
func (d *redeployDirector) runTask(running bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.taskState = boshTaskDone
	if running {
		d.taskState = waitStateRunning
	}
}

func (d *redeployDirector) GetInfo(context.Context) (*bosh.Info, error) {
	if d.nameless {
		return nil, nil
	}

	return &bosh.Info{Name: redeployDirectorName, Version: "280.0.4"}, nil
}

func (d *redeployDirector) GetDeployment(_ context.Context, name string) (*bosh.DeploymentDetail, error) {
	d.mu.Lock()
	hook := d.onGetDeployment
	d.mu.Unlock()

	if hook != nil {
		hook()
	}

	if d.empty {
		return nil, nil
	}

	return &bosh.DeploymentDetail{Name: name, Manifest: d.manifest}, nil
}

func (d *redeployDirector) ListVariables(context.Context, string) ([]bosh.Variable, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.lists++

	if d.listFails != 0 {
		if d.listFails > 0 {
			d.listFails--
		}

		return nil, errors.New("director 503")
	}

	return []bosh.Variable{{ID: d.deployed, Name: regenLeaf}}, nil
}

func (d *redeployDirector) GetDeploymentVMs(context.Context, string) ([]bosh.VM, error) {
	return []bosh.VM{{ID: "vm-1", CID: redeployVMCID, Job: "redis", Index: 0, IPs: []string{"127.0.0.1"}}}, nil
}

func (d *redeployDirector) FindRunningTasksForDeployment(context.Context, string) ([]bosh.Task, error) {
	return nil, nil
}

func (d *redeployDirector) FindRunningTaskForDeployment(context.Context, string) (*bosh.Task, error) {
	return nil, nil
}

func (d *redeployDirector) UpdateDeployment(ctx context.Context, name, manifest string) (*bosh.Task, error) {
	return d.UpdateDeploymentWithOptions(ctx, name, manifest, bosh.DeployOptions{})
}

func (d *redeployDirector) UpdateDeploymentWithOptions(_ context.Context, name, manifest string, opts bosh.DeployOptions) (*bosh.Task, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.sent = append(d.sent, redeploySent{manifest: manifest, opts: opts})

	return &bosh.Task{ID: redeployTaskID, State: waitStateRunning, Deployment: name}, nil
}

func (d *redeployDirector) WaitForTask(ctx context.Context, taskID int) (*bosh.Task, error) {
	d.mu.Lock()
	d.waits = append(d.waits, taskID)
	finish := d.finish
	d.mu.Unlock()

	if finish != nil {
		return finish(ctx, taskID)
	}

	d.mu.Lock()
	if d.applies != "" {
		d.deployed = d.applies
	}
	onDone := d.onDone
	d.mu.Unlock()

	if onDone != nil {
		onDone()
	}

	return &bosh.Task{ID: taskID, State: boshTaskDone, Deployment: waitDeployment}, nil
}

func (d *redeployDirector) CancelTask(_ context.Context, taskID int) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.cancels = append(d.cancels, taskID)

	return nil
}

// failLists makes the next n ListVariables calls fail, or every call when
// n is negative.
func (d *redeployDirector) failLists(n int) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.listFails = n
}

func (d *redeployDirector) listCalls() int {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.lists
}

func (d *redeployDirector) deploys() []redeploySent {
	d.mu.Lock()
	defer d.mu.Unlock()

	return slices.Clone(d.sent)
}

func (d *redeployDirector) waited() []int {
	d.mu.Lock()
	defer d.mu.Unlock()

	return slices.Clone(d.waits)
}

func (d *redeployDirector) cancelled() []int {
	d.mu.Lock()
	defer d.mu.Unlock()

	return slices.Clone(d.cancels)
}

// redeployEnv is the world of one redeploy rotation: CredHub holds the
// leaf at v1, signed by the services CA, the director deploys v1, and the
// instance's listener serves v1. A regenerate makes v2, and a done deploy
// applies v2 to the director and the listener.
type redeployEnv struct {
	t        *testing.T
	vault    *regenVault
	credhub  *regenCredHub
	log      *regenLog
	ca       testCA
	director *redeployDirector
	listener *tlsListener
	manifest string

	oldLeaf, newLeaf *x509.Certificate
	newKey           *ecdsa.PrivateKey

	// settings, when set, is the manager's certificate settings.
	settings func() certinventory.Settings

	mu       sync.Mutex
	switches config.CertificatesConfig
	scans    []string
}

func newRedeployEnv(t *testing.T) *redeployEnv {
	t.Helper()

	log := &regenLog{}
	env := &redeployEnv{
		t:     t,
		vault: &regenVault{waitVault: newWaitVault(), log: log},
		credhub: &regenCredHub{
			log:      log,
			meta:     make(map[string]credhub.CertificateMeta),
			versions: make(map[string][]credhub.CredentialVersion),
			pems:     make(map[string]credhub.CertificatePublic),
		},
		log:      log,
		ca:       newTestCA(t, "services-ca"),
		switches: switches(true, false),
	}

	env.credhub.addVersion(regenCAName, "ca1", regenCreated, env.ca.cert)

	oldLeaf, oldKey := env.ca.issue(t, baseSpec())
	env.oldLeaf = oldLeaf
	env.credhub.meta[regenLeaf] = credhub.CertificateMeta{
		ID: "cert-1", Name: regenLeaf, SignedBy: regenCAName,
		Versions: []credhub.CertificateVersionMeta{{ID: "v1", ExpiryDate: oldLeaf.NotAfter}},
	}
	env.credhub.addVersion(regenLeaf, "v1", regenCreated, oldLeaf)

	spec := baseSpec()
	spec.notAfter = validateNow.Add(365 * 24 * time.Hour)
	env.newLeaf, env.newKey = env.ca.issue(t, spec)

	// CredHub answers a regenerate with v2, a valid reissue.
	env.credhub.answer(func(string) (*credhub.RegenerateResult, error) {
		env.credhub.addVersion(regenLeaf, "v2", regenLater, env.newLeaf)

		return &credhub.RegenerateResult{VersionID: "v2", Name: regenLeaf}, nil
	})

	env.listener = startTLSListener(t, oldLeaf, oldKey)
	env.manifest = fmt.Sprintf(redeployManifest, env.listener.port())

	env.director = &redeployDirector{manifest: env.manifest, deployed: "v1", applies: "v2"}
	env.director.onDone = func() { env.listener.serve(env.newLeaf, env.newKey) }

	return env
}

// madeNewVersion stores v2 in CredHub, as a regenerate before a restart
// left it.
func (e *redeployEnv) madeNewVersion() {
	e.credhub.addVersion(regenLeaf, "v2", regenLater, e.newLeaf)
}

// deployedNewVersion makes the director and the listener show v2, as a
// deploy that finished before a restart left them.
func (e *redeployEnv) deployedNewVersion() {
	e.director.mu.Lock()
	e.director.deployed = "v2"
	e.director.mu.Unlock()

	e.listener.serve(e.newLeaf, e.newKey)
}

func (e *redeployEnv) currentSwitches() config.CertificatesConfig {
	e.mu.Lock()
	defer e.mu.Unlock()

	return e.switches
}

func (e *redeployEnv) setSwitches(rotation bool) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.switches = switches(rotation, false)
}

// scan is the inventory's scan of the instance, which shows v1 deployed
// on its one VM.
func (e *redeployEnv) scan(_ context.Context, instanceID string) (certinventory.InstanceScan, error) {
	e.mu.Lock()
	e.scans = append(e.scans, instanceID)
	e.mu.Unlock()

	return certinventory.InstanceScan{
		Ref: certinventory.InstanceRef{ID: instanceID, Deployment: waitDeployment},
		VMs: []bosh.VM{{ID: "vm-1", CID: redeployVMCID, Job: "redis", Index: 0, IPs: []string{"127.0.0.1"}}},
		Material: certinventory.ScanMaterial{
			CredHub:    []certinventory.CredHubCert{{Name: regenLeaf, Deployed: e.oldLeaf, Versions: certinventory.Versions{SigningCA: regenCAName}}},
			SigningCAs: map[string]*x509.Certificate{regenCAName: e.ca.cert},
		},
	}, nil
}

func (e *redeployEnv) scanned() []string {
	e.mu.Lock()
	defer e.mu.Unlock()

	return slices.Clone(e.scans)
}

// newManager returns a configured manager over the env's director and
// vault, which the caller starts or runs directly.
func (e *redeployEnv) newManager() *Manager {
	e.t.Helper()

	manager := NewManager(&logger.NoOpLogger{}, e.director, e.vault)
	e.t.Cleanup(manager.Stop)

	err := manager.Configure(Deps{
		CredHub:      e.credhub,
		Switches:     e.currentSwitches,
		Now:          func() time.Time { return validateNow },
		ScanInstance: e.scan,
		Prober:       certinventory.Prober{Timeout: 2 * time.Second},
		Settings:     e.settings,
	})
	if err != nil {
		e.t.Fatalf("Configure: %v", err)
	}

	manager.verifyInterval = 20 * time.Millisecond

	return manager
}

// task returns a confirmed redeploy task whose one instance has not
// started, approved against the director as it stands.
func (e *redeployEnv) task() UpgradeTask {
	return UpgradeTask{
		ID:                certTaskID,
		Name:              "rotate the redis leaf",
		Kind:              TaskKindCertificates,
		Status:            TaskStatusRunning,
		CreationConfirmed: true,
		Certificates:      &CertificateTaskSpec{Mode: RotationModeRedeploy, PreviewID: "pv-redeploy"},
		TotalCount:        1,
		CreatedAt:         validateNow,
		Instances: []InstanceUpgrade{{
			InstanceID:     waitInstanceID,
			DeploymentName: waitDeployment,
			Status:         InstanceStatusPending,
			Certificates: &InstanceRotation{
				Phase: RotationPhasePreflight,
				Approved: ApprovedEffects{
					ManifestDigest: manifestDigest(e.manifest),
					Selected:       []VersionPair{{Name: regenLeaf, DeployedVersionID: "v1", LatestVersionID: "v1"}},
					VMCIDs:         []string{redeployVMCID},
				},
				Variables: []VariableRotation{{Name: regenLeaf, CertificateID: "cert-1", OldVersionID: "v1", RegenerateState: RegenerateNotSent}},
			},
		}},
	}
}

// deployedTask returns the task as a restart finds it once its deploy was
// sent as BOSH task 4711, after the regeneration made v2.
func (e *redeployEnv) deployedTask() UpgradeTask {
	task := e.task()
	started := validateNow

	inst := &task.Instances[0]
	inst.Status = InstanceStatusRunning
	inst.StartedAt = &started
	inst.BOSHTaskID = redeployTaskID
	inst.ContextID = loadedContextID
	inst.Certificates.Phase = RotationPhaseDeploy
	inst.Certificates.Variables[0].RegenerateState = RegenerateConfirmed
	inst.Certificates.Variables[0].NewVersionID = "v2"

	return task
}

// directRun reserves the task in the manager and runs its one instance
// through the redeploy runner, as the task's processor would.
func (e *redeployEnv) directRun(manager *Manager, task *UpgradeTask) bool {
	e.t.Helper()

	err := manager.reserveCertificateTask(task)
	if err != nil {
		e.t.Fatalf("reserving the task: %v", err)
	}

	return manager.rotateInstanceRedeploy(context.Background(), task, &task.Instances[0], make(chan struct{}))
}

// load stores the task, starts a manager that loads it, and waits for the
// load.
func (e *redeployEnv) load(task UpgradeTask) *Manager {
	e.t.Helper()

	storeCertificateTask(e.t, e.vault.waitVault, task)

	manager := e.newManager()
	manager.Start()
	waitForLoad(e.t, manager)

	return manager
}

// current returns a copy of the task as the manager holds it.
func current(t *testing.T, manager *Manager) UpgradeTask {
	t.Helper()

	task, err := manager.GetTask(certTaskID)
	if err != nil {
		t.Fatalf("reading the task: %v", err)
	}

	return *task
}

// eventuallyInstance waits until the test instance reaches the status.
func eventuallyInstance(t *testing.T, manager *Manager, status InstanceStatus) UpgradeTask {
	t.Helper()

	waitUntil(t, "instance status "+string(status), func() bool {
		return current(t, manager).Instances[0].Status == status
	})

	return current(t, manager)
}

// expectReleased waits until the guard still holds the instance.
func expectReleased(t *testing.T, manager *Manager) {
	t.Helper()

	waitUntil(t, "the release of the instance", func() bool {
		_, held := manager.Guard().Holder(waitInstanceID, waitDeployment)

		return !held
	})
}

// A redeploy regenerates the leaf, deploys the director's own manifest
// with the latest variables, and completes the instance once the director
// names v2 and the listener serves it.
//
//nolint:paralleltest // the runner shares the manager's test seams
func TestRedeployHappyPath(t *testing.T) {
	env := newRedeployEnv(t)
	manager := env.newManager()
	task := env.task()

	if !env.directRun(manager, &task) {
		t.Fatalf("expected the instance to settle, got %+v", current(t, manager).Instances[0])
	}

	inst := current(t, manager).Instances[0]
	if inst.Status != InstanceStatusSuccess || inst.Certificates.Phase != RotationPhaseDone {
		t.Fatalf("expected a completed instance at phase done, got %s at %s: %s", inst.Status, inst.Certificates.Phase, inst.Error)
	}

	deploys := env.director.deploys()
	if len(deploys) != 1 {
		t.Fatalf("expected one deploy, got %d", len(deploys))
	}

	if deploys[0].manifest != env.manifest || !deploys[0].opts.ForceLatestVariables {
		t.Fatalf("expected the director's manifest byte for byte with latest variables, got opts %+v and manifest %q", deploys[0].opts, deploys[0].manifest)
	}

	if got := current(t, manager).CompletedCount; got != 1 {
		t.Fatalf("expected one completed instance, got %d", got)
	}
}

// The instance stays running and reserved at phase deploy while its
// listener is probed, and only the probe's answer completes it.
//
//nolint:paralleltest // the runner shares the manager's test seams
func TestRedeployHoldsGuardUntilVerified(t *testing.T) {
	env := newRedeployEnv(t)
	manager := env.newManager()
	task := env.task()

	accepted, release := env.listener.hold()
	defer release()

	done := make(chan bool, 1)

	go func() { done <- env.directRun(manager, &task) }()

	select {
	case <-accepted:
	case <-time.After(waitTimeout):
		t.Fatal("the listener was never probed")
	}

	inst := current(t, manager).Instances[0]
	if inst.Status != InstanceStatusRunning || inst.Certificates.Phase != RotationPhaseDeploy || inst.Certificates.Deploy == nil {
		t.Fatalf("expected a running instance at phase deploy with its deploy recorded, got %s at %s", inst.Status, inst.Certificates.Phase)
	}

	expectHeld(t, manager)

	release()

	select {
	case <-done:
	case <-time.After(waitTimeout):
		t.Fatal("the runner never returned")
	}

	if inst := current(t, manager).Instances[0]; inst.Status != InstanceStatusSuccess {
		t.Fatalf("expected the instance to complete, got %s: %s", inst.Status, inst.Error)
	}

	expectReleased(t, manager)
}

// A restart after BOSH finished but before the verification takes the
// deploy's end from the record and then verifies, with no new deploy.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestRedeployRestartAfterDeployBeforeVerify(t *testing.T) {
	env := newRedeployEnv(t)
	env.madeNewVersion()
	env.deployedNewVersion()

	stored := env.deployedTask()
	stored.Instances[0].Certificates.Deploy = &DeployOutcome{BOSHTaskID: redeployTaskID, State: boshTaskDone, FinishedAt: validateNow}

	manager := env.load(stored)

	task := eventuallyInstance(t, manager, InstanceStatusSuccess)
	if task.Instances[0].Certificates.Phase != RotationPhaseDone {
		t.Fatalf("expected phase done, got %s", task.Instances[0].Certificates.Phase)
	}

	if len(env.director.deploys()) != 0 {
		t.Fatalf("expected no new deploy, got %d", len(env.director.deploys()))
	}

	expectReleased(t, manager)
}

// The same holds for a cancelled task, because the deploy already ran and
// only its verification is left.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestRedeployVerifiesLoadedDeployOfCancelledTask(t *testing.T) {
	env := newRedeployEnv(t)
	env.madeNewVersion()
	env.deployedNewVersion()

	stored := env.deployedTask()
	stored.Status = TaskStatusCancelled
	stored.Instances[0].Certificates.Deploy = &DeployOutcome{BOSHTaskID: redeployTaskID, State: boshTaskDone, FinishedAt: validateNow}

	manager := env.load(stored)

	task := eventuallyInstance(t, manager, InstanceStatusSuccess)
	if task.Status != TaskStatusCancelled {
		t.Fatalf("expected the task to stay cancelled, got %s", task.Status)
	}

	if cancels := env.director.cancelled(); len(cancels) != 0 {
		t.Fatalf("expected no cancel of the finished deploy, got %v", cancels)
	}

	if len(env.director.deploys()) != 0 {
		t.Fatalf("expected no new deploy, got %d", len(env.director.deploys()))
	}

	expectReleased(t, manager)
}

// A context ID that can't be saved for certain stops the deploy before it
// is sent, and the task pauses at phase deploy.
//
//nolint:paralleltest // the runner shares the manager's test seams
func TestRedeployContextIDSaveUnconfirmed(t *testing.T) {
	env := newRedeployEnv(t)
	env.vault.refuse = func(task UpgradeTask) bool {
		return len(task.Instances) > 0 && task.Instances[0].ContextID != ""
	}

	manager := env.newManager()
	task := env.task()

	if env.directRun(manager, &task) {
		t.Fatal("expected the instance not to settle")
	}

	if deploys := env.director.deploys(); len(deploys) != 0 {
		t.Fatalf("expected no deploy, got %d", len(deploys))
	}

	got := current(t, manager)
	if got.Status != TaskStatusPaused || !got.Paused {
		t.Fatalf("expected the task to pause, got %s", got.Status)
	}

	if phase := got.Instances[0].Certificates.Phase; phase != RotationPhaseDeploy {
		t.Fatalf("expected phase deploy, got %s", phase)
	}

	expectHeld(t, manager)
}

// A version of the leaf that the task did not make stops the rotation
// before the deploy, waiting for a new approval.
//
//nolint:paralleltest // the runner shares the manager's test seams
func TestRedeployStopsOnForeignVersion(t *testing.T) {
	env := newRedeployEnv(t)
	foreign, _ := env.ca.issue(t, baseSpec())

	env.credhub.answer(func(string) (*credhub.RegenerateResult, error) {
		env.credhub.addVersion(regenLeaf, "v2", regenLater, env.newLeaf)
		env.credhub.addVersion(regenLeaf, "v3", validateNow, foreign)

		return &credhub.RegenerateResult{VersionID: "v2", Name: regenLeaf}, nil
	})

	manager := env.newManager()
	task := env.task()

	if env.directRun(manager, &task) {
		t.Fatal("expected the instance not to settle")
	}

	inst := current(t, manager).Instances[0]
	if inst.Status != StatusAwaitingDecision || inst.Certificates.Pending == nil || inst.Certificates.Pending.Kind != DecisionApproveChangedEffects {
		t.Fatalf("expected the instance to await approve_changed_effects, got %s with %+v", inst.Status, inst.Certificates.Pending)
	}

	if !strings.Contains(strings.Join(inst.Certificates.Pending.Changes, " "), "v3") {
		t.Fatalf("expected the changes to name v3, got %v", inst.Certificates.Pending.Changes)
	}

	if deploys := env.director.deploys(); len(deploys) != 0 {
		t.Fatalf("expected no deploy, got %d", len(deploys))
	}

	expectHeld(t, manager)
}

// A deploy that fails leaves the new version in CredHub, pending a deploy,
// and nothing more is written to CredHub.
//
//nolint:paralleltest // the runner shares the manager's test seams
func TestRedeployDeployFailureLeavesPending(t *testing.T) {
	env := newRedeployEnv(t)
	env.director.finish = func(_ context.Context, taskID int) (*bosh.Task, error) {
		return &bosh.Task{ID: taskID, State: boshTaskError, Result: "compilation failed"}, nil
	}

	manager := env.newManager()
	task := env.task()

	if !env.directRun(manager, &task) {
		t.Fatal("expected the failed instance to settle")
	}

	inst := current(t, manager).Instances[0]
	if inst.Status != InstanceStatusFailed || !strings.Contains(inst.Error, "deploy_pending") {
		t.Fatalf("expected a failed instance whose message names deploy_pending, got %s: %s", inst.Status, inst.Error)
	}

	if count := env.credhub.regenerateCount(); count != 1 {
		t.Fatalf("expected one regenerate and no CredHub write after it, got %d", count)
	}

	expectReleased(t, manager)
}

// A restart that finds the deploy's task ID follows that task, and sends
// no new deploy.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestRedeployResumeAdoptsSavedTask(t *testing.T) {
	env := newRedeployEnv(t)
	env.madeNewVersion()

	manager := env.load(env.deployedTask())

	eventuallyInstance(t, manager, InstanceStatusSuccess)

	if waits := env.director.waited(); len(waits) == 0 || waits[0] != redeployTaskID {
		t.Fatalf("expected the wait to follow task %d, got %v", redeployTaskID, waits)
	}

	if len(env.director.deploys()) != 0 {
		t.Fatalf("expected no new deploy, got %d", len(env.director.deploys()))
	}
}

// A deploy that leaves the director on the old version fails the
// instance, naming the variable.
//
//nolint:paralleltest // the runner shares the manager's test seams
func TestRedeployVerifyNeedsNewVersion(t *testing.T) {
	env := newRedeployEnv(t)
	env.director.applies = ""

	manager := env.newManager()
	task := env.task()

	if !env.directRun(manager, &task) {
		t.Fatal("expected the failed instance to settle")
	}

	inst := current(t, manager).Instances[0]
	if inst.Status != InstanceStatusFailed || !strings.Contains(inst.Error, "redis_crt") {
		t.Fatalf("expected a failed instance naming redis_crt, got %s: %s", inst.Status, inst.Error)
	}

	if inst.Certificates.Phase != RotationPhaseDone {
		t.Fatalf("expected phase done, got %s", inst.Certificates.Phase)
	}

	expectReleased(t, manager)
}

// With rotation off, a loaded task pauses before its next write, while a
// deploy that was already running is still followed to its end.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestRedeployRestartWithRotationOff(t *testing.T) {
	t.Run("before the regeneration", func(t *testing.T) {
		env := newRedeployEnv(t)
		env.setSwitches(false)

		stored := env.task()
		started := validateNow
		stored.Instances[0].Status = InstanceStatusRunning
		stored.Instances[0].StartedAt = &started
		stored.Instances[0].Certificates.Phase = RotationPhaseRegenerate

		manager := env.load(stored)

		waitUntil(t, "the task to pause", func() bool { return current(t, manager).Status == TaskStatusPaused })

		inst := current(t, manager).Instances[0]
		if inst.Error != rotationOffMessage {
			t.Fatalf("expected the rotation-off message, got %q", inst.Error)
		}

		if count := env.credhub.regenerateCount(); count != 0 {
			t.Fatalf("expected no regenerate, got %d", count)
		}

		if len(env.director.deploys()) != 0 {
			t.Fatalf("expected no deploy, got %d", len(env.director.deploys()))
		}
	})

	t.Run("during the deploy", func(t *testing.T) {
		env := newRedeployEnv(t)
		env.setSwitches(false)
		env.madeNewVersion()

		manager := env.load(env.deployedTask())

		eventuallyInstance(t, manager, InstanceStatusSuccess)

		if len(env.director.deploys()) != 0 {
			t.Fatalf("expected no new deploy, got %d", len(env.director.deploys()))
		}
	})
}

// A completed instance frees the guard and is scanned again, so the
// inventory shows the new certificate.
//
//nolint:paralleltest // the runner shares the manager's test seams
func TestRedeployReleasesGuardAndRescans(t *testing.T) {
	env := newRedeployEnv(t)
	manager := env.newManager()
	task := env.task()

	env.directRun(manager, &task)

	if inst := current(t, manager).Instances[0]; inst.Status != InstanceStatusSuccess {
		t.Fatalf("expected the instance to complete, got %s: %s", inst.Status, inst.Error)
	}

	expectReleased(t, manager)

	// The preflight scans once, and the completion scans again.
	waitUntil(t, "the rescan of the instance", func() bool {
		scans := env.scanned()

		return len(scans) >= 2 && scans[len(scans)-1] == waitInstanceID
	})
}

// holdForDeprovision returns a guard in which a deprovision holds the
// test instance, and the function that frees it.
func holdForDeprovision(t *testing.T) (*mutation.Guard, func()) {
	t.Helper()

	guard := mutation.NewGuard(time.Now)

	free, err := guard.Acquire(waitInstanceID, waitDeployment, mutation.Holder{Kind: mutation.KindDeprovision, Description: "deprovision of redis-instance-1"})
	if err != nil {
		t.Fatalf("holding the instance: %v", err)
	}

	return guard, free
}

// loadWaiting stores the task, and starts a manager over the guard that
// loads it, with short restore waits. The loaded task's reservation waits
// for the guard's holder.
func (e *redeployEnv) loadWaiting(guard *mutation.Guard, task UpgradeTask) *Manager {
	e.t.Helper()

	storeCertificateTask(e.t, e.vault.waitVault, task)

	manager := e.newManager()
	manager.SetGuard(guard)
	manager.restoreWait = 20 * time.Millisecond
	manager.restoreRetry = 20 * time.Millisecond
	manager.Start()
	waitForLoad(e.t, manager)

	if _, waiting := manager.WaitingFor(waitInstanceID, ""); !waiting {
		e.t.Fatal("expected the task to wait for the instance")
	}

	return manager
}

// The processor does not run an instance whose reservation waits for
// another holder until that holder frees it. The test runs the processor
// itself, since the task is never queued while it waits.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestRedeployWaitsForTheRestoreWaitingList(t *testing.T) {
	env := newRedeployEnv(t)
	guard, free := holdForDeprovision(t)
	manager := env.loadWaiting(guard, env.runningTask())

	manager.mu.Lock()
	task := manager.tasks[certTaskID]
	manager.owned[certTaskID] = true
	manager.mu.Unlock()

	done := make(chan bool, 1)

	go func() { done <- manager.processCertificateInstance(task, &task.Instances[0], make(chan struct{})) }()

	time.Sleep(200 * time.Millisecond)

	if count := env.credhub.regenerateCount(); count != 0 {
		t.Fatalf("expected nothing to run while the instance waits, got %d regenerates", count)
	}

	free()

	select {
	case goOn := <-done:
		if !goOn {
			t.Fatalf("expected the processor to go on, got %+v", current(t, manager).Instances[0])
		}
	case <-time.After(waitTimeout):
		t.Fatal("the processor never returned")
	}

	if inst := current(t, manager).Instances[0]; inst.Status != InstanceStatusSuccess {
		t.Fatalf("expected the instance to complete, got %s: %s", inst.Status, inst.Error)
	}
}

// The verification of a loaded done deploy waits while the instance's
// reservation waits for another holder, and runs once that holder frees
// it.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestRedeployVerifyWaitsForTheRestoreWaitingList(t *testing.T) {
	env := newRedeployEnv(t)
	env.madeNewVersion()
	env.deployedNewVersion()

	stored := env.deployedTask()
	stored.Instances[0].Certificates.Deploy = &DeployOutcome{BOSHTaskID: redeployTaskID, State: boshTaskDone, FinishedAt: validateNow}

	guard, free := holdForDeprovision(t)
	manager := env.loadWaiting(guard, stored)

	time.Sleep(200 * time.Millisecond)

	if calls := env.director.listCalls(); calls != 0 {
		t.Fatalf("expected no verification while the instance waits, got %d variable reads", calls)
	}

	free()

	eventuallyInstance(t, manager, InstanceStatusSuccess)
	expectReleased(t, manager)
}

// A loaded certificate task that never started may be one whose creation
// was reported as failed, so it loads paused and waits for an operator.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestRedeployNeverStartedTaskLoadsPaused(t *testing.T) {
	env := newRedeployEnv(t)

	stored := env.task()
	stored.Status = TaskStatusPending

	manager := env.load(stored)

	waitUntil(t, "the task to pause", func() bool { return current(t, manager).Status == TaskStatusPaused })

	task := current(t, manager)
	if !task.Paused || !strings.Contains(task.Instances[0].Error, "resume") {
		t.Fatalf("expected a paused task with a message to resume or cancel it, got %+v", task.Instances[0])
	}

	time.Sleep(100 * time.Millisecond)

	if count := env.credhub.regenerateCount(); count != 0 {
		t.Fatalf("expected no regenerate, got %d", count)
	}

	if len(env.director.deploys()) != 0 {
		t.Fatalf("expected no deploy, got %d", len(env.director.deploys()))
	}

	var saved UpgradeTask

	found, err := env.vault.Get(context.Background(), certificateTasksVaultPath+"/"+certTaskID, &saved)
	if err != nil || !found || saved.Status != TaskStatusPaused {
		t.Fatalf("expected the paused task to be saved, got %s (found %v, %v)", saved.Status, found, err)
	}

	expectHeld(t, manager)
}

// Resuming a task that loaded paused because it never started clears the
// never-started message from the instances still waiting their turn.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestRedeployResumeClearsNeverStartedMessage(t *testing.T) {
	env := newRedeployEnv(t)

	stored := env.task()
	stored.Status = TaskStatusPending
	second := env.task().Instances[0]
	second.InstanceID = "instance-2"
	second.DeploymentName = "redis-instance-2"
	stored.Instances = append(stored.Instances, second)
	stored.TotalCount = 2

	manager := env.load(stored)

	waitUntil(t, "the task to pause", func() bool { return current(t, manager).Status == TaskStatusPaused })

	if waiting := current(t, manager).Instances[1]; waiting.Error != neverStartedMessage {
		t.Fatalf("expected the paused task to show the never-started message, got %q", waiting.Error)
	}

	accepted, release := env.listener.hold()
	t.Cleanup(release)

	if err := manager.ResumeTask(context.Background(), certTaskID); err != nil {
		t.Fatalf("ResumeTask: %v", err)
	}

	select {
	case <-accepted:
	case <-time.After(waitTimeout):
		t.Fatal("the listener was never probed")
	}

	task := current(t, manager)
	if task.Instances[1].Status != InstanceStatusPending || task.Instances[1].Error != "" {
		t.Fatalf("expected the waiting instance to be pending with no message, got %s: %q", task.Instances[1].Status, task.Instances[1].Error)
	}

	if saved := env.stored(); saved.Instances[1].Error != "" {
		t.Fatalf("expected the saved waiting instance to have no message, got %q", saved.Instances[1].Error)
	}
}

// runningTask returns the task as a restart finds it once the instance
// started, before its regeneration, so the loaded task runs at once.
func (e *redeployEnv) runningTask() UpgradeTask {
	task := e.task()
	started := validateNow

	task.Instances[0].Status = InstanceStatusRunning
	task.Instances[0].StartedAt = &started
	task.Instances[0].Certificates.Phase = RotationPhaseRegenerate

	return task
}

// stored reads the task as Vault holds it.
func (e *redeployEnv) stored() UpgradeTask {
	e.t.Helper()

	var saved UpgradeTask

	found, err := e.vault.Get(context.Background(), certificateTasksVaultPath+"/"+certTaskID, &saved)
	if err != nil || !found {
		e.t.Fatalf("reading the saved task: found %v, %v", found, err)
	}

	return saved
}

// A restart while the deploy runs saves the instance interrupted. Once the
// next start sees the deploy finish, the instance is verified, completes,
// and frees its reservation.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestRedeployRestartMidDeployVerifies(t *testing.T) {
	env := newRedeployEnv(t)

	env.director.mu.Lock()
	env.director.finish = func(ctx context.Context, _ int) (*bosh.Task, error) {
		<-ctx.Done()

		return nil, ctx.Err()
	}
	env.director.mu.Unlock()

	first := env.load(env.runningTask())

	waitUntil(t, "the deploy's wait", func() bool { return len(env.director.waited()) > 0 })

	first.Stop()

	if inst := env.stored().Instances[0]; inst.Status != InstanceStatusInterrupted || inst.BOSHTaskID != redeployTaskID {
		t.Fatalf("expected the instance saved interrupted with task %d, got %s with task %d", redeployTaskID, inst.Status, inst.BOSHTaskID)
	}

	env.director.mu.Lock()
	env.director.finish = nil
	env.director.mu.Unlock()

	second := env.newManager()
	second.Start()
	waitForLoad(t, second)

	task := eventuallyInstance(t, second, InstanceStatusSuccess)
	if task.Instances[0].Certificates.Phase != RotationPhaseDone {
		t.Fatalf("expected phase done, got %s", task.Instances[0].Certificates.Phase)
	}

	if len(env.director.deploys()) != 1 {
		t.Fatalf("expected the one deploy from before the restart, got %d", len(env.director.deploys()))
	}

	expectReleased(t, second)
}

// A restart that finds the deploy's end on record verifies from that
// record, so a director that can no longer answer for the task does not
// turn the done deploy into an unknown one.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestRedeployKeepsRecordedDeployEnd(t *testing.T) {
	env := newRedeployEnv(t)
	env.madeNewVersion()
	env.deployedNewVersion()

	env.director.finish = func(context.Context, int) (*bosh.Task, error) {
		return nil, errors.New("director 500")
	}

	stored := env.deployedTask()
	stored.Instances[0].Certificates.Deploy = &DeployOutcome{BOSHTaskID: redeployTaskID, State: boshTaskDone, FinishedAt: validateNow}

	manager := env.load(stored)

	task := eventuallyInstance(t, manager, InstanceStatusSuccess)
	if task.Instances[0].Certificates.Phase != RotationPhaseDone {
		t.Fatalf("expected phase done, got %s", task.Instances[0].Certificates.Phase)
	}

	if waits := env.director.waited(); len(waits) != 0 {
		t.Fatalf("expected no wait on the finished task, got %v", waits)
	}

	expectReleased(t, manager)
}

// cancelledDeployedTask returns a cancelled task whose deploy BOSH
// finished before the restart, with the director and listener on v2.
func (e *redeployEnv) cancelledDeployedTask() UpgradeTask {
	e.madeNewVersion()
	e.deployedNewVersion()

	task := e.deployedTask()
	task.Status = TaskStatusCancelled
	task.Instances[0].Certificates.Deploy = &DeployOutcome{BOSHTaskID: redeployTaskID, State: boshTaskDone, FinishedAt: validateNow}

	return task
}

// A cancelled task can't be resumed, so a read that fails while it
// verifies a done deploy is retried, and the instance still completes and
// frees its reservation.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestRedeployCancelledVerifyRetries(t *testing.T) {
	env := newRedeployEnv(t)
	env.director.failLists(1)

	manager := env.load(env.cancelledDeployedTask())

	task := eventuallyInstance(t, manager, InstanceStatusSuccess)
	if task.Status != TaskStatusCancelled {
		t.Fatalf("expected the task to stay cancelled, got %s", task.Status)
	}

	if calls := env.director.listCalls(); calls < 2 {
		t.Fatalf("expected the failed read to be retried, got %d calls", calls)
	}

	expectReleased(t, manager)
}

// When the reads keep failing, the cancelled task's instance waits for an
// operator to resolve the deploy's outcome, stays reserved, and is never
// told to resume a task that can't be resumed.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestRedeployCancelledVerifyAwaitsDecision(t *testing.T) {
	env := newRedeployEnv(t)
	env.director.failLists(-1)

	manager := env.load(env.cancelledDeployedTask())

	task := eventuallyInstance(t, manager, InstanceStatusOutcomeUnknown)
	inst := task.Instances[0]

	if inst.Certificates.Pending == nil || inst.Certificates.Pending.Kind != DecisionResolveDeployOutcome {
		t.Fatalf("expected a resolve_deploy_outcome decision, got %+v", inst.Certificates.Pending)
	}

	if strings.Contains(strings.ToLower(inst.Error), "resume") {
		t.Fatalf("expected no advice to resume a cancelled task, got %q", inst.Error)
	}

	if task.Status != TaskStatusCancelled {
		t.Fatalf("expected the task to stay cancelled, got %s", task.Status)
	}

	if calls := env.director.listCalls(); calls != cancelledVerifyAttempts {
		t.Fatalf("expected %d attempts, got %d", cancelledVerifyAttempts, calls)
	}

	// The decision is saved after it is made, so the test waits for the
	// save rather than reading the store at once.
	waitUntil(t, "the decision to be saved", func() bool {
		return env.stored().Instances[0].Status == InstanceStatusOutcomeUnknown
	})

	expectHeld(t, manager)
}

// A live task pauses when its verification can't read, and a cancel of
// the paused task still verifies the done deploy and frees the instance.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestRedeployCancelAfterVerifyPause(t *testing.T) {
	env := newRedeployEnv(t)

	// The reads fail from the end of the deploy on, so only the
	// verification meets them.
	serveNew := env.director.onDone
	env.director.onDone = func() {
		serveNew()
		env.director.failLists(-1)
	}

	manager := env.load(env.runningTask())

	waitUntil(t, "the task to pause", func() bool { return current(t, manager).Status == TaskStatusPaused })

	if inst := current(t, manager).Instances[0]; inst.Status != InstanceStatusRunning || !strings.Contains(inst.Error, ". The deploy is done") {
		t.Fatalf("expected a running instance paused to verify again, got %s: %q", inst.Status, inst.Error)
	}

	env.director.failLists(0)

	err := manager.CancelTask(context.Background(), certTaskID)
	if err != nil {
		t.Fatalf("CancelTask: %v", err)
	}

	task := eventuallyInstance(t, manager, InstanceStatusSuccess)
	if task.Status != TaskStatusCancelled {
		t.Fatalf("expected the task to stay cancelled, got %s", task.Status)
	}

	expectReleased(t, manager)
}

// A BOSH task that fails ends the instance as a deploy the director
// rejected does, at phase done with the inventory scanning it again, so
// the deploy_pending finding shows at once.
//
//nolint:paralleltest // the runner shares the manager's test seams
func TestRedeployFailedTaskRescans(t *testing.T) {
	env := newRedeployEnv(t)
	env.director.finish = func(_ context.Context, taskID int) (*bosh.Task, error) {
		return &bosh.Task{ID: taskID, State: boshTaskError, Result: "compilation failed"}, nil
	}

	manager := env.newManager()
	task := env.task()

	env.directRun(manager, &task)

	inst := current(t, manager).Instances[0]
	if inst.Status != InstanceStatusFailed || inst.Certificates.Phase != RotationPhaseDone {
		t.Fatalf("expected a failed instance at phase done, got %s at %s", inst.Status, inst.Certificates.Phase)
	}

	if got := current(t, manager).FailedCount; got != 1 {
		t.Fatalf("expected one failed instance, got %d", got)
	}

	// The preflight scans once, and the failure scans again.
	waitUntil(t, "the rescan of the instance", func() bool {
		scans := env.scanned()

		return len(scans) >= 2 && scans[len(scans)-1] == waitInstanceID
	})

	if saved := env.stored().Instances[0]; saved.Certificates.Phase != RotationPhaseDone {
		t.Fatalf("expected phase done to be saved, got %s", saved.Certificates.Phase)
	}
}

// A director that answers with nothing and no error leaves the
// verification unreadable, with a message that reads cleanly.
//
//nolint:paralleltest // the runner shares the manager's test seams
func TestRedeployVerifyEmptyAnswers(t *testing.T) {
	cases := map[string]func(d *redeployDirector){
		"no director name": func(d *redeployDirector) { d.nameless = true },
		"no deployment":    func(d *redeployDirector) { d.empty = true },
	}

	for name, empty := range cases {
		t.Run(name, func(t *testing.T) {
			env := newRedeployEnv(t)
			env.madeNewVersion()
			env.deployedNewVersion()
			empty(env.director)

			manager := env.newManager()
			task := env.deployedTask()

			err := manager.verifyRedeploy(context.Background(), &task.Instances[0])
			if !errors.Is(err, errVerifyUnreadable) {
				t.Fatalf("expected an unreadable verification, got %v", err)
			}

			if strings.Contains(err.Error(), "%!") {
				t.Fatalf("expected a clean message, got %q", err.Error())
			}
		})
	}
}

// missingLeaf is a variable the director does not deploy, so a preflight
// that checks it fails and changes nothing.
const missingLeaf = "/ws-bosh/redis-instance-0/missing_leaf"

// twoInstanceTask returns a confirmed redeploy task of the env's real
// instance followed by a second instance on its own deployment.
func (e *redeployEnv) twoInstanceTask() UpgradeTask {
	task := e.task()

	second := e.task().Instances[0]
	second.InstanceID = "instance-2"
	second.DeploymentName = "redis-instance-2"

	task.Instances = append(task.Instances, second)
	task.TotalCount = 2

	return task
}

// failingFirstTask returns a confirmed redeploy task whose first instance
// selects a variable the director does not deploy, so its preflight fails,
// followed by the env's real instance.
func (e *redeployEnv) failingFirstTask() UpgradeTask {
	task := e.task()

	failing := e.task().Instances[0]
	failing.InstanceID = "instance-0"
	failing.DeploymentName = "redis-instance-0"
	failing.Certificates.Approved.Selected = []VersionPair{{Name: missingLeaf, DeployedVersionID: "v1", LatestVersionID: "v1"}}
	failing.Certificates.Variables = []VariableRotation{{Name: missingLeaf, CertificateID: "cert-0", OldVersionID: "v1", RegenerateState: RegenerateNotSent}}

	task.Instances = append([]InstanceUpgrade{failing}, task.Instances...)
	task.TotalCount = 2

	return task
}

// startLoaded loads a task that never started, which loads paused, and
// resumes it.
func (e *redeployEnv) startLoaded(task UpgradeTask) *Manager {
	e.t.Helper()

	manager := e.load(task)

	waitUntil(e.t, "the task to load paused", func() bool { return current(e.t, manager).Status == TaskStatusPaused })

	err := manager.ResumeTask(context.Background(), certTaskID)
	if err != nil {
		e.t.Fatalf("ResumeTask: %v", err)
	}

	return manager
}

// gateDeploys makes the wait on a deploy report that it started and hold
// until the returned release, after which the deploy ends done and applies
// v2, as the default wait does. A stop ends the hold.
func (e *redeployEnv) gateDeploys() (<-chan struct{}, func()) {
	started := make(chan struct{})
	gate := make(chan struct{})

	var startOnce, releaseOnce sync.Once

	e.director.mu.Lock()
	e.director.finish = func(ctx context.Context, taskID int) (*bosh.Task, error) {
		startOnce.Do(func() { close(started) })

		select {
		case <-gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}

		e.deployedNewVersion()

		return &bosh.Task{ID: taskID, State: boshTaskDone, Deployment: waitDeployment}, nil
	}
	e.director.mu.Unlock()

	release := func() { releaseOnce.Do(func() { close(gate) }) }
	e.t.Cleanup(release)

	return started, release
}

// A pause during a redeploy lets the running deploy and its verification
// finish, and the next instance does not start until a resume.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestCertificatePauseBetweenInstances(t *testing.T) {
	env := newRedeployEnv(t)
	started, release := env.gateDeploys()

	manager := env.startLoaded(env.twoInstanceTask())

	select {
	case <-started:
	case <-time.After(waitTimeout):
		t.Fatal("the deploy never started")
	}

	err := manager.PauseTask(context.Background(), certTaskID)
	if err != nil {
		t.Fatalf("PauseTask: %v", err)
	}

	if inst := current(t, manager).Instances[0]; inst.Status != InstanceStatusRunning {
		t.Fatalf("expected the pause to leave the deploying instance running, got %s", inst.Status)
	}

	release()

	task := eventuallyInstance(t, manager, InstanceStatusSuccess)
	if task.Status != TaskStatusPaused {
		t.Fatalf("expected the task to stay paused, got %s", task.Status)
	}

	time.Sleep(100 * time.Millisecond)

	task = current(t, manager)
	if task.Instances[1].Status != InstanceStatusPending {
		t.Fatalf("expected the next instance to wait for a resume, got %s", task.Instances[1].Status)
	}

	if sent := len(env.director.deploys()); sent != 1 {
		t.Fatalf("expected one deploy, got %d", sent)
	}

	if saved := env.stored(); saved.Status != TaskStatusPaused || saved.Instances[0].Status != InstanceStatusSuccess {
		t.Fatalf("expected the saved task paused with the instance done, got %s and %s", saved.Status, saved.Instances[0].Status)
	}
}

// A cancel during a redeploy's deploy cancels its BOSH task. The instance
// stays reserved until its follower sees the task end, and then fails
// with the new versions left deploy_pending.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestCertificateCancelCancelsDeploy(t *testing.T) {
	env := newRedeployEnv(t)

	started := make(chan struct{})
	gate := make(chan struct{})

	var startOnce, releaseOnce sync.Once

	release := func() { releaseOnce.Do(func() { close(gate) }) }
	t.Cleanup(release)

	env.director.finish = func(ctx context.Context, taskID int) (*bosh.Task, error) {
		startOnce.Do(func() { close(started) })

		for !slices.Contains(env.director.cancelled(), taskID) {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(5 * time.Millisecond):
			}
		}

		select {
		case <-gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}

		return &bosh.Task{ID: taskID, State: boshTaskCancelled, Deployment: waitDeployment}, nil
	}

	manager := env.startLoaded(env.task())

	select {
	case <-started:
	case <-time.After(waitTimeout):
		t.Fatal("the deploy never started")
	}

	err := manager.CancelTask(context.Background(), certTaskID)
	if err != nil {
		t.Fatalf("CancelTask: %v", err)
	}

	if cancels := env.director.cancelled(); !slices.Equal(cancels, []int{redeployTaskID}) {
		t.Fatalf("expected BOSH task %d to be cancelled once, got %v", redeployTaskID, cancels)
	}

	if inst := current(t, manager).Instances[0]; inst.Status != InstanceStatusRunning {
		t.Fatalf("expected the instance to stay running until its task ends, got %s", inst.Status)
	}

	expectHeld(t, manager)

	release()

	task := eventuallyInstance(t, manager, InstanceStatusFailed)
	if task.Status != TaskStatusCancelled {
		t.Fatalf("expected the task to stay cancelled, got %s", task.Status)
	}

	if !strings.Contains(task.Instances[0].Error, "was cancelled") || !strings.Contains(task.Instances[0].Error, "deploy_pending") {
		t.Fatalf("expected the cancelled deploy's message, got %q", task.Instances[0].Error)
	}

	expectReleased(t, manager)
}

// waitForInstance waits until the task's instance at index has the status.
func waitForInstance(t *testing.T, manager *Manager, index int, status InstanceStatus) UpgradeTask {
	t.Helper()

	waitUntil(t, fmt.Sprintf("instance %d status %s", index, status), func() bool {
		return current(t, manager).Instances[index].Status == status
	})

	return current(t, manager)
}

// With pause_on_failure on, as it is by default, a failed instance pauses
// the task, and the next instance waits for the operator.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestCertificatePauseOnFailure(t *testing.T) {
	env := newRedeployEnv(t)

	manager := env.startLoaded(env.failingFirstTask())

	waitForInstance(t, manager, 0, InstanceStatusFailed)
	waitUntil(t, "the task to pause", func() bool { return current(t, manager).Status == TaskStatusPaused })

	time.Sleep(100 * time.Millisecond)

	task := current(t, manager)
	if !task.Paused || task.Instances[1].Status != InstanceStatusPending {
		t.Fatalf("expected a paused task with the next instance pending, got paused %v and %s", task.Paused, task.Instances[1].Status)
	}

	if count := env.credhub.regenerateCount(); count != 0 {
		t.Fatalf("expected no regenerate, got %d", count)
	}

	if saved := env.stored(); saved.Status != TaskStatusPaused {
		t.Fatalf("expected the paused task to be saved, got %s", saved.Status)
	}
}

// With pause_on_failure off, a failed instance does not pause the task,
// and the next instance runs.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestCertificateNoPauseOnFailureWhenOff(t *testing.T) {
	env := newRedeployEnv(t)
	env.settings = func() certinventory.Settings {
		settings := certinventory.DefaultSettings()
		settings.PauseOnFailure = false

		return settings
	}

	manager := env.startLoaded(env.failingFirstTask())

	task := waitForInstance(t, manager, 1, InstanceStatusSuccess)
	if task.Instances[0].Status != InstanceStatusFailed {
		t.Fatalf("expected the first instance to fail, got %s", task.Instances[0].Status)
	}

	waitUntil(t, "the task to complete", func() bool { return current(t, manager).Status == TaskStatusCompleted })
}

// A resume after a failure pause goes on with the remaining instances and
// never retries the failed one.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestCertificateResumeSkipsFailed(t *testing.T) {
	env := newRedeployEnv(t)

	manager := env.startLoaded(env.failingFirstTask())

	waitForInstance(t, manager, 0, InstanceStatusFailed)
	waitUntil(t, "the task to pause", func() bool { return current(t, manager).Status == TaskStatusPaused })

	failed := current(t, manager).Instances[0]

	err := manager.ResumeTask(context.Background(), certTaskID)
	if err != nil {
		t.Fatalf("ResumeTask: %v", err)
	}

	waitForInstance(t, manager, 1, InstanceStatusSuccess)
	waitUntil(t, "the task to complete", func() bool { return current(t, manager).Status == TaskStatusCompleted })

	task := current(t, manager)
	if task.FailedCount != 1 || task.CompletedCount != 1 {
		t.Fatalf("expected one failed and one completed instance, got %d and %d", task.FailedCount, task.CompletedCount)
	}

	again := task.Instances[0]
	if again.Status != InstanceStatusFailed || again.Error != failed.Error || !again.CompletedAt.Equal(*failed.CompletedAt) {
		t.Fatalf("expected the failed instance to stay as it was, got %s: %q", again.Status, again.Error)
	}

	if sent := len(env.director.deploys()); sent != 1 {
		t.Fatalf("expected only the remaining instance to deploy, got %d deploys", sent)
	}
}

// A failure that a loaded deploy's follower records pauses the task too,
// so the next instance waits for the operator.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestCertificatePauseOnFailureOfLoadedDeploy(t *testing.T) {
	env := newRedeployEnv(t)
	env.madeNewVersion()
	env.director.finish = func(_ context.Context, taskID int) (*bosh.Task, error) {
		return &bosh.Task{ID: taskID, State: boshTaskError, Result: "compilation failed"}, nil
	}

	stored := env.deployedTask()
	second := env.task().Instances[0]
	second.InstanceID = "instance-2"
	second.DeploymentName = "redis-instance-2"
	stored.Instances = append(stored.Instances, second)
	stored.TotalCount = 2

	manager := env.load(stored)

	eventuallyInstance(t, manager, InstanceStatusFailed)
	waitUntil(t, "the task to pause", func() bool { return current(t, manager).Status == TaskStatusPaused })

	time.Sleep(100 * time.Millisecond)

	if task := current(t, manager); task.Instances[1].Status != InstanceStatusPending {
		t.Fatalf("expected the next instance to stay pending, got %s", task.Instances[1].Status)
	}
}

// A resume with rotation off is refused with rotation_disabled, and the
// task stays paused with nothing changed.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestCertificateResumeRefusedWithRotationOff(t *testing.T) {
	env := newRedeployEnv(t)

	manager := env.load(env.task())

	waitUntil(t, "the task to load paused", func() bool { return current(t, manager).Status == TaskStatusPaused })

	env.setSwitches(false)

	err := manager.ResumeTask(context.Background(), certTaskID)

	var apiErr *apierr.Error
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusForbidden || apiErr.Code != apierr.CodeRotationDisabled {
		t.Fatalf("expected a 403 rotation_disabled refusal, got %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	task := current(t, manager)
	if task.Status != TaskStatusPaused || !task.Paused {
		t.Fatalf("expected the task to stay paused, got %s", task.Status)
	}

	if saved := env.stored(); saved.Status != TaskStatusPaused {
		t.Fatalf("expected the saved task to stay paused, got %s", saved.Status)
	}

	if count := env.credhub.regenerateCount(); count != 0 {
		t.Fatalf("expected no regenerate, got %d", count)
	}
}

// A cancel never interrupts a verification that is running. The deploy is
// done, so no BOSH cancel is sent, and the instance completes and frees
// its reservation.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestCertificateCancelLetsVerificationFinish(t *testing.T) {
	env := newRedeployEnv(t)

	accepted, release := env.listener.hold()
	t.Cleanup(release)

	manager := env.startLoaded(env.task())

	select {
	case <-accepted:
	case <-time.After(waitTimeout):
		t.Fatal("the verification never probed the listener")
	}

	if sent := len(env.director.deploys()); sent != 1 {
		t.Fatalf("expected the deploy to be sent before the probe, got %d deploys", sent)
	}

	err := manager.CancelTask(context.Background(), certTaskID)
	if err != nil {
		t.Fatalf("CancelTask: %v", err)
	}

	if inst := current(t, manager).Instances[0]; inst.Status != InstanceStatusRunning {
		t.Fatalf("expected the verifying instance to stay running, got %s", inst.Status)
	}

	expectHeld(t, manager)

	release()

	task := eventuallyInstance(t, manager, InstanceStatusSuccess)
	if task.Status != TaskStatusCancelled {
		t.Fatalf("expected the task to stay cancelled, got %s", task.Status)
	}

	if cancels := env.director.cancelled(); len(cancels) != 0 {
		t.Fatalf("expected no BOSH cancel for a done deploy, got %v", cancels)
	}

	expectReleased(t, manager)
}

// idleTask returns a task paused with its instance running and nothing in
// flight, as a pause before a write leaves it. A regenerated instance has
// v2 in CredHub and paused before its deploy phase began.
func (e *redeployEnv) idleTask(regenerated bool) UpgradeTask {
	task := e.runningTask()
	task.Status = TaskStatusPaused
	task.Paused = true
	task.Instances[0].Error = rotationOffMessage

	if regenerated {
		e.madeNewVersion()

		task.Instances[0].Certificates.Variables[0].RegenerateState = RegenerateConfirmed
		task.Instances[0].Certificates.Variables[0].NewVersionID = "v2"
	}

	return task
}

// A cancel of a task that nothing processes ends its running instance
// that has nothing in flight, so no instance stays reserved with nothing
// following it. A regenerated instance says its new versions are
// deploy_pending.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestCertificateCancelEndsIdleInstance(t *testing.T) {
	for _, regenerated := range []bool{false, true} {
		t.Run(fmt.Sprintf("regenerated=%v", regenerated), func(t *testing.T) {
			env := newRedeployEnv(t)

			manager := env.load(env.idleTask(regenerated))
			expectHeld(t, manager)

			err := manager.CancelTask(context.Background(), certTaskID)
			if err != nil {
				t.Fatalf("CancelTask: %v", err)
			}

			task := eventuallyInstance(t, manager, InstanceStatusCancelled)
			if task.CancelledCount != 1 || task.Status != TaskStatusCancelled {
				t.Fatalf("expected one cancelled instance of a cancelled task, got %d and %s", task.CancelledCount, task.Status)
			}

			if pending := strings.Contains(task.Instances[0].Error, "deploy_pending"); pending != regenerated {
				t.Fatalf("expected deploy_pending in the message only when regenerated, got %q", task.Instances[0].Error)
			}

			expectReleased(t, manager)

			waitUntil(t, "the cancelled instance to be saved", func() bool {
				return env.stored().Instances[0].Status == InstanceStatusCancelled
			})

			if sent := len(env.director.deploys()); sent != 0 {
				t.Fatalf("expected no deploy, got %d", sent)
			}
		})
	}
}

// A cancel of a task whose processor waits out a pause ends the idle
// instance once the processor stops.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestCertificateCancelEndsPausedProcessorsInstance(t *testing.T) {
	env := newRedeployEnv(t)
	env.setSwitches(false)

	manager := env.load(env.runningTask())

	waitUntil(t, "the task to pause", func() bool { return current(t, manager).Status == TaskStatusPaused })

	err := manager.CancelTask(context.Background(), certTaskID)
	if err != nil {
		t.Fatalf("CancelTask: %v", err)
	}

	task := eventuallyInstance(t, manager, InstanceStatusCancelled)
	if task.Status != TaskStatusCancelled {
		t.Fatalf("expected the task to stay cancelled, got %s", task.Status)
	}

	expectReleased(t, manager)

	waitUntil(t, "the cancelled instance to be saved", func() bool {
		return env.stored().Instances[0].Status == InstanceStatusCancelled
	})
}

// A task cancelled while the rotation reads the director ends the
// instance as cancelled, even when the effects changed, rather than
// stopping it for an approval that a cancelled task can't act on.
//
//nolint:paralleltest // the runner shares the manager's test seams
func TestCertificateCancelBeforeApprovalStop(t *testing.T) {
	cases := []struct {
		name        string
		regenerated bool
		message     string
	}{
		{name: "before the regeneration", message: cancelledUnchanged},
		{name: "before the deploy", regenerated: true, message: cancelledUndeployed},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newRedeployEnv(t)
			manager := env.newManager()

			task := env.task()
			if tc.regenerated {
				task = env.idleTask(true)
				task.Status = TaskStatusRunning
				task.Paused = false
			}

			task.Instances[0].Certificates.Approved.ManifestDigest = "stale-digest"

			var once sync.Once

			env.director.mu.Lock()
			env.director.onGetDeployment = func() {
				once.Do(func() {
					manager.mu.Lock()
					task.Status = TaskStatusCancelled
					manager.mu.Unlock()
				})
			}
			env.director.mu.Unlock()

			if !env.directRun(manager, &task) {
				t.Fatal("expected the instance to settle")
			}

			manager.mu.RLock()
			inst := task.Instances[0]
			manager.mu.RUnlock()

			if inst.Status != InstanceStatusCancelled || inst.Error != tc.message || inst.Certificates.Pending != nil {
				t.Fatalf("expected the instance cancelled with %q, got %s: %q with %+v", tc.message, inst.Status, inst.Error, inst.Certificates.Pending)
			}

			if deploys := env.director.deploys(); len(deploys) != 0 {
				t.Fatalf("expected no deploy, got %d", len(deploys))
			}
		})
	}
}

// Once a step returns, a running instance whose decision was answered and
// whose task resumed reads its next step again rather than stopping the
// processor of a running task.
func TestStepEnd(t *testing.T) {
	t.Parallel()

	waiting := &InstanceRotation{Pending: &PendingDecision{Kind: DecisionApproveChangedEffects}}

	cases := []struct {
		name     string
		task     UpgradeTask
		instance InstanceUpgrade
		want     stepEnd
	}{
		{"settled", UpgradeTask{Status: TaskStatusRunning}, InstanceUpgrade{Status: InstanceStatusSuccess}, endSettled},
		{"paused", UpgradeTask{Status: TaskStatusPaused, Paused: true}, InstanceUpgrade{Status: InstanceStatusRunning}, endAgain},
		{"cancelled", UpgradeTask{Status: TaskStatusCancelled}, InstanceUpgrade{Status: InstanceStatusRunning}, endAgain},
		{"decision answered and resumed", UpgradeTask{Status: TaskStatusRunning}, InstanceUpgrade{Status: InstanceStatusRunning, Certificates: &InstanceRotation{}}, endAgain},
		{"waiting on a running task", UpgradeTask{Status: TaskStatusRunning}, InstanceUpgrade{Status: StatusAwaitingDecision, Certificates: waiting}, endStop},
		{"interrupted", UpgradeTask{Status: TaskStatusRunning}, InstanceUpgrade{Status: InstanceStatusInterrupted}, endStop},
	}

	for _, tc := range cases {
		if got := stepEndLocked(&tc.task, &tc.instance); got != tc.want {
			t.Errorf("%s: got %d, want %d", tc.name, got, tc.want)
		}
	}
}
