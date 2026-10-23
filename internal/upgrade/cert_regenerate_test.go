package upgrade

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"blacksmith/internal/apierr"
	"blacksmith/internal/bosh"
	"blacksmith/internal/certpatch"
	"blacksmith/internal/config"
	"blacksmith/internal/credhub"
	"blacksmith/pkg/logger"
)

const (
	regenLeaf   = "/ws-bosh/redis-instance-1/redis_crt"
	regenLeaf2  = "/ws-bosh/redis-instance-1/redis_peer_crt"
	regenCAName = "/ws-bosh/redis-instance-1/services_ca"
)

// regenCreated is when the test's old versions were made, and regenLater
// is when a regenerated version is made.
var (
	regenCreated = validateNow.Add(-30 * 24 * time.Hour)
	regenLater   = validateNow.Add(-time.Minute)
)

// regenLog records, in order, the saves and CredHub calls of a test, so
// it can check which came first.
type regenLog struct {
	mu     sync.Mutex
	events []string
}

func (l *regenLog) add(event string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.events = append(l.events, event)
}

func (l *regenLog) list() []string {
	l.mu.Lock()
	defer l.mu.Unlock()

	return slices.Clone(l.events)
}

// firstVariable returns the first variable of the task's first instance.
func firstVariable(task UpgradeTask) VariableRotation {
	if len(task.Instances) == 0 || task.Instances[0].Certificates == nil || len(task.Instances[0].Certificates.Variables) == 0 {
		return VariableRotation{}
	}

	return task.Instances[0].Certificates.Variables[0]
}

// regenVault is a waitVault that logs each write of a certificate task
// record as "save:<state>:<new version>" of its first variable. A write
// the test's refuse rule picks fails before it stores anything, and
// onSaved sees each record that was stored.
type regenVault struct {
	*waitVault

	log *regenLog

	script  sync.Mutex
	refuse  func(task UpgradeTask) bool
	onSaved func(task UpgradeTask)
	records []UpgradeTask
}

func (v *regenVault) Put(ctx context.Context, path string, data interface{}) error {
	if !strings.HasPrefix(path, certificateTasksVaultPath+"/") {
		return v.waitVault.Put(ctx, path, data)
	}

	raw, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", path, err)
	}

	var task UpgradeTask

	err = json.Unmarshal(raw, &task)
	if err != nil {
		return fmt.Errorf("unmarshal %s: %w", path, err)
	}

	variable := firstVariable(task)
	event := variable.RegenerateState + ":" + variable.NewVersionID

	v.script.Lock()
	refused := v.refuse != nil && v.refuse(task)
	v.script.Unlock()

	if refused {
		v.log.add("refused:" + event)

		return errPutRefused
	}

	err = v.waitVault.Put(ctx, path, data)
	if err != nil {
		return err
	}

	v.script.Lock()
	v.records = append(v.records, task)
	hook := v.onSaved
	v.script.Unlock()

	v.log.add("save:" + event)

	if hook != nil {
		hook(task)
	}

	return nil
}

// lastRecord returns the record the vault stored last.
func (v *regenVault) lastRecord(t *testing.T) UpgradeTask {
	t.Helper()

	v.script.Lock()
	defer v.script.Unlock()

	if len(v.records) == 0 {
		t.Fatal("no certificate task record was saved")
	}

	return v.records[len(v.records)-1]
}

// regenCredHub is an in-memory CredHub that logs each certificate read as
// "read:<version>" and each regenerate request as "regenerate", and counts
// the requests. A read under an ended context fails, as the real client's
// would, and failVersions fails the version listing of a name.
type regenCredHub struct {
	log *regenLog

	mu           sync.Mutex
	meta         map[string]credhub.CertificateMeta
	versions     map[string][]credhub.CredentialVersion
	pems         map[string]credhub.CertificatePublic
	failVersions map[string]error
	regenerate   func(certificateID string) (*credhub.RegenerateResult, error)
	regenerates  int
	reads        []string
}

func (f *regenCredHub) CertificateMetadata(ctx context.Context, name string) (*credhub.CertificateMeta, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("find %s: %w", name, err)
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	meta, ok := f.meta[name]
	if !ok {
		return nil, fmt.Errorf("%w: %q", credhub.ErrNotFound, name)
	}

	return &meta, nil
}

func (f *regenCredHub) CredentialVersions(ctx context.Context, name string, limit int) ([]credhub.CredentialVersion, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("versions of %s: %w", name, err)
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	if err := f.failVersions[name]; err != nil {
		return nil, err
	}

	versions, ok := f.versions[name]
	if !ok {
		return nil, fmt.Errorf("%w: %q", credhub.ErrNotFound, name)
	}

	versions = slices.Clone(versions)
	sort.SliceStable(versions, func(i, j int) bool { return versions[i].CreatedAt.After(versions[j].CreatedAt) })

	return versions[:min(limit, len(versions))], nil
}

func (f *regenCredHub) CertificateVersion(ctx context.Context, versionID string) (*credhub.CertificatePublic, error) {
	f.log.add("read:" + versionID)

	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", versionID, err)
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	f.reads = append(f.reads, versionID)

	public, ok := f.pems[versionID]
	if !ok {
		return nil, fmt.Errorf("%w: %q", credhub.ErrNotFound, versionID)
	}

	return &public, nil
}

func (f *regenCredHub) Regenerate(_ context.Context, certificateID string) (*credhub.RegenerateResult, error) {
	f.log.add("regenerate")

	f.mu.Lock()
	f.regenerates++
	answer := f.regenerate
	f.mu.Unlock()

	return answer(certificateID)
}

// CertificateKeyPair records the read as "pair:<version>" and finds no
// pair, because no regeneration test reads a private key.
func (f *regenCredHub) CertificateKeyPair(ctx context.Context, versionID string) (*credhub.KeyPair, error) {
	f.log.add("pair:" + versionID)

	err := ctx.Err()
	if err != nil {
		return nil, fmt.Errorf("read pair %s: %w", versionID, err)
	}

	return nil, fmt.Errorf("%w: %q", credhub.ErrNotFound, versionID)
}

// addVersion stores cert as a version of the named credential.
func (f *regenCredHub) addVersion(name, versionID string, createdAt time.Time, cert *x509.Certificate) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.versions[name] = append(f.versions[name], credhub.CredentialVersion{ID: versionID, Type: "certificate", CreatedAt: createdAt})
	f.pems[versionID] = credhub.CertificatePublic{
		VersionID:   versionID,
		Name:        name,
		Certificate: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})),
		CreatedAt:   createdAt,
	}
}

// failVersionsOf makes the version listing of name fail with err, or
// succeed again with a nil err.
func (f *regenCredHub) failVersionsOf(name string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.failVersions == nil {
		f.failVersions = make(map[string]error)
	}

	f.failVersions[name] = err
}

func (f *regenCredHub) regenerateCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.regenerates
}

func (f *regenCredHub) answer(answer func(certificateID string) (*credhub.RegenerateResult, error)) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.regenerate = answer
}

// noCallDirector panics on any call through its nil embedded Director, so
// a test that passes with it shows the step never deployed.
type noCallDirector struct {
	bosh.Director
}

// regenEnv is a manager with a running redeploy task whose one instance
// selects one leaf at v1, signed by the services CA at ca1.
type regenEnv struct {
	manager *Manager
	vault   *regenVault
	credhub *regenCredHub
	log     *regenLog
	ca      testCA
	task    *UpgradeTask
	oldPEM  string

	mu       sync.Mutex
	switches config.CertificatesConfig
}

func newRegenEnv(t *testing.T) *regenEnv {
	t.Helper()

	log := &regenLog{}
	env := &regenEnv{
		vault: &regenVault{waitVault: newWaitVault(), log: log},
		credhub: &regenCredHub{
			log:      log,
			meta:     make(map[string]credhub.CertificateMeta),
			versions: make(map[string][]credhub.CredentialVersion),
			pems:     make(map[string]credhub.CertificatePublic),
		},
		log:      log,
		ca:       newTestCA(t, "services-ca"),
		switches: switches(true, true),
	}

	env.credhub.addVersion(regenCAName, "ca1", regenCreated, env.ca.cert)
	env.addLeaf(t, regenLeaf, "cert-1", "v1")
	env.oldPEM = env.credhub.pems["v1"].Certificate

	// By default CredHub answers a regenerate with v2, a valid reissue.
	env.credhub.answer(func(string) (*credhub.RegenerateResult, error) {
		env.reissue(t, regenLeaf, "v2", func(*certSpec) {})

		return &credhub.RegenerateResult{VersionID: "v2", Name: regenLeaf}, nil
	})

	env.manager = NewManager(&logger.NoOpLogger{}, noCallDirector{}, env.vault)
	t.Cleanup(env.manager.Stop)

	err := env.manager.Configure(Deps{
		CredHub:  env.credhub,
		Switches: env.currentSwitches,
		Now:      func() time.Time { return validateNow },
	})
	if err != nil {
		t.Fatalf("Configure: %v", err)
	}

	env.task = &UpgradeTask{
		ID:                uuid.NewString(),
		Name:              "rotate the redis leaf",
		Kind:              TaskKindCertificates,
		Status:            TaskStatusRunning,
		CreationConfirmed: true,
		Certificates:      &CertificateTaskSpec{Mode: RotationModeRedeploy, PreviewID: "pv-regen"},
		TotalCount:        1,
		CreatedAt:         validateNow,
		Instances: []InstanceUpgrade{{
			InstanceID:     waitInstanceID,
			DeploymentName: waitDeployment,
			Status:         InstanceStatusRunning,
			Certificates: &InstanceRotation{
				Phase:     RotationPhaseRegenerate,
				Approved:  ApprovedEffects{Selected: []VersionPair{{Name: regenLeaf, DeployedVersionID: "v1", LatestVersionID: "v1"}}},
				Variables: []VariableRotation{{Name: regenLeaf, CertificateID: "cert-1", OldVersionID: "v1", RegenerateState: RegenerateNotSent}},
			},
		}},
	}

	err = env.manager.reserveCertificateTask(env.task)
	if err != nil {
		t.Fatalf("reserving the task: %v", err)
	}

	return env
}

// addLeaf stores a leaf at its first version, signed by the services CA.
func (e *regenEnv) addLeaf(t *testing.T, name, certificateID, versionID string) {
	t.Helper()

	leaf, _ := e.ca.issue(t, baseSpec())

	e.credhub.mu.Lock()
	e.credhub.meta[name] = credhub.CertificateMeta{
		ID: certificateID, Name: name, SignedBy: regenCAName,
		Versions: []credhub.CertificateVersionMeta{{ID: versionID, ExpiryDate: leaf.NotAfter}},
	}
	e.credhub.mu.Unlock()

	e.credhub.addVersion(name, versionID, regenCreated, leaf)
}

// reissue stores a new version of the leaf, made a minute ago, that
// expires a year from now, with change applied to its details.
func (e *regenEnv) reissue(t *testing.T, name, versionID string, change func(*certSpec)) {
	t.Helper()

	spec := baseSpec()
	spec.notAfter = validateNow.Add(365 * 24 * time.Hour)
	change(&spec)

	cert, _ := e.ca.issue(t, spec)
	e.credhub.addVersion(name, versionID, regenLater, cert)
}

func (e *regenEnv) currentSwitches() config.CertificatesConfig {
	e.mu.Lock()
	defer e.mu.Unlock()

	return e.switches
}

func (e *regenEnv) setSwitches(rotation, patch bool) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.switches = switches(rotation, patch)
}

func (e *regenEnv) inst() *InstanceUpgrade { return &e.task.Instances[0] }

func (e *regenEnv) run() (bool, error) {
	return e.runWith(context.Background())
}

func (e *regenEnv) runWith(ctx context.Context) (bool, error) {
	return e.manager.regenerateVariables(ctx, e.task, e.inst())
}

// current returns a copy of the task as the manager holds it.
func (e *regenEnv) current() UpgradeTask {
	e.manager.mu.RLock()
	defer e.manager.mu.RUnlock()

	return *copyTask(e.task)
}

// setVariable changes the stored state of the first variable, as a
// loaded task would hold it.
func (e *regenEnv) setVariable(change func(v *VariableRotation)) {
	e.manager.mu.Lock()
	defer e.manager.mu.Unlock()

	change(&e.inst().Certificates.Variables[0])
}

func expectFailedValidation(t *testing.T, env *regenEnv, stop bool) {
	t.Helper()

	if !stop {
		t.Fatal("expected the step to stop the instance")
	}

	inst := env.current().Instances[0]
	if inst.Status != InstanceStatusFailed || !strings.Contains(inst.Error, "failed validation") {
		t.Fatalf("expected the instance to fail validation, got %s: %q", inst.Status, inst.Error)
	}
}

func TestRegenerateSavesSendingBeforeRequest(t *testing.T) {
	t.Parallel()

	env := newRegenEnv(t)

	var atRequest UpgradeTask

	env.credhub.answer(func(string) (*credhub.RegenerateResult, error) {
		atRequest = env.vault.lastRecord(t)
		env.reissue(t, regenLeaf, "v2", func(*certSpec) {})

		return &credhub.RegenerateResult{VersionID: "v2"}, nil
	})

	stop, err := env.run()
	if stop || err != nil {
		t.Fatalf("expected the step to go on, got stop %v and %v", stop, err)
	}

	variable := firstVariable(atRequest)
	if variable.RegenerateState != RegenerateSending || !slices.Equal(variable.Baseline, []string{"v1"}) {
		t.Fatalf("expected the save before the request to carry sending and baseline [v1], got %s %v", variable.RegenerateState, variable.Baseline)
	}

	old, err := x509.ParseCertificate(mustDecodePEM(t, env.oldPEM))
	if err != nil {
		t.Fatalf("parse the old certificate: %v", err)
	}

	if variable.OldSerial != old.SerialNumber.Text(16) || !variable.OldNotAfter.Equal(old.NotAfter) ||
		variable.OldCertHash != certpatch.NormalisedHash([]byte(env.oldPEM)) {
		t.Fatalf("expected the old identity in the save, got serial %q, expiry %s, hash %q", variable.OldSerial, variable.OldNotAfter, variable.OldCertHash)
	}

	// The baseline was saved on its own before sending was.
	events := env.log.list()

	baseline := slices.Index(events, "save:"+RegenerateNotSent+":")
	sending := slices.Index(events, "save:"+RegenerateSending+":")
	request := slices.Index(events, "regenerate")

	if baseline < 0 || sending < baseline || request < sending {
		t.Fatalf("expected a baseline save, then a sending save, then the request, got %v", events)
	}
}

func mustDecodePEM(t *testing.T, text string) []byte {
	t.Helper()

	block, _ := pem.Decode([]byte(text))
	if block == nil {
		t.Fatal("no PEM block")
	}

	return block.Bytes
}

func TestRegenerateNotSentWhenSaveUnconfirmed(t *testing.T) {
	t.Parallel()

	env := newRegenEnv(t)
	env.vault.refuse = func(task UpgradeTask) bool { return firstVariable(task).RegenerateState == RegenerateSending }

	stop, err := env.run()
	if !stop || !errors.Is(err, ErrSaveUnconfirmed) {
		t.Fatalf("expected the step to stop with an unconfirmed save, got stop %v and %v", stop, err)
	}

	if n := env.credhub.regenerateCount(); n != 0 {
		t.Fatalf("expected no regenerate request, got %d", n)
	}

	task := env.current()
	if task.Status != TaskStatusPaused || !task.Paused {
		t.Fatalf("expected the task paused, got %s", task.Status)
	}

	inst := task.Instances[0]
	if !strings.Contains(inst.Error, "could not confirm") || !strings.Contains(inst.Error, "regenerate redis_crt") {
		t.Fatalf("expected the message to name the step, got %q", inst.Error)
	}

	if state := firstVariable(task).RegenerateState; state != RegenerateNotSent {
		t.Fatalf("expected the variable back at not_sent, got %s", state)
	}
}

func TestRegenerateNotSentWhenRotationTurnedOff(t *testing.T) {
	t.Parallel()

	env := newRegenEnv(t)
	env.vault.onSaved = func(task UpgradeTask) {
		if firstVariable(task).RegenerateState == RegenerateSending {
			env.setSwitches(false, false)
		}
	}

	stop, err := env.run()
	if !stop {
		t.Fatal("expected the step to stop")
	}

	expectAPIError(t, err, http.StatusForbidden, apierr.CodeRotationDisabled)

	if n := env.credhub.regenerateCount(); n != 0 {
		t.Fatalf("expected no regenerate request, got %d", n)
	}

	task := env.current()
	if task.Status != TaskStatusPaused || !strings.Contains(task.Instances[0].Error, "Certificate rotation is turned off") {
		t.Fatalf("expected the task paused with the switch's message, got %s: %q", task.Status, task.Instances[0].Error)
	}

	if state := firstVariable(env.vault.lastRecord(t)).RegenerateState; state != RegenerateNotSent {
		t.Fatalf("expected the saved variable back at not_sent, got %s", state)
	}
}

// Rotation turned off while the baseline was being saved stops the
// request before the record ever announces it as sending, so no restart
// can mistake it for a request whose outcome is unknown.
func TestRegenerateRotationOffBeforeSendingSave(t *testing.T) {
	t.Parallel()

	env := newRegenEnv(t)
	env.vault.onSaved = func(task UpgradeTask) {
		if variable := firstVariable(task); variable.RegenerateState == RegenerateNotSent && len(variable.Baseline) > 0 {
			env.setSwitches(false, false)
		}
	}

	stop, err := env.run()
	if !stop {
		t.Fatal("expected the step to stop")
	}

	expectAPIError(t, err, http.StatusForbidden, apierr.CodeRotationDisabled)

	if n := env.credhub.regenerateCount(); n != 0 {
		t.Fatalf("expected no regenerate request, got %d", n)
	}

	for _, event := range env.log.list() {
		if strings.HasPrefix(event, "save:"+RegenerateSending) {
			t.Fatalf("expected no save of sending, got %v", env.log.list())
		}
	}

	if task := env.current(); task.Status != TaskStatusPaused || firstVariable(task).RegenerateState != RegenerateNotSent {
		t.Fatalf("expected the task paused with the variable not sent, got %s and %s", task.Status, firstVariable(task).RegenerateState)
	}
}

// When rotation is turned off after the sending save and the save that
// puts the variable back at not_sent can't be confirmed, Vault may still
// hold sending. The variable is not sent in memory, and the instance's
// message tells the operator that a restart may ask to reconcile a
// request that never went out.
func TestRegenerateRefusedAfterSendingUnsavedReset(t *testing.T) {
	t.Parallel()

	env := newRegenEnv(t)

	var sent atomic.Bool

	env.vault.onSaved = func(task UpgradeTask) {
		if firstVariable(task).RegenerateState == RegenerateSending {
			sent.Store(true)
			env.setSwitches(false, false)
		}
	}

	env.vault.refuse = func(UpgradeTask) bool { return sent.Load() }

	stop, err := env.run()
	if !stop {
		t.Fatal("expected the step to stop")
	}

	expectAPIError(t, err, http.StatusForbidden, apierr.CodeRotationDisabled)

	if n := env.credhub.regenerateCount(); n != 0 {
		t.Fatalf("expected no regenerate request, got %d", n)
	}

	task := env.current()
	if task.Status != TaskStatusPaused || firstVariable(task).RegenerateState != RegenerateNotSent {
		t.Fatalf("expected the task paused with the variable not sent, got %s and %s", task.Status, firstVariable(task).RegenerateState)
	}

	message := task.Instances[0].Error
	if !strings.Contains(message, "Certificate rotation is turned off") || !strings.Contains(message, "never sent the request to regenerate redis_crt") {
		t.Fatalf("expected the switch's message and the unsaved reset, got %q", message)
	}
}

func TestRegenerateConfirmedSavedBeforeValidation(t *testing.T) {
	t.Parallel()

	env := newRegenEnv(t)

	stop, err := env.run()
	if stop || err != nil {
		t.Fatalf("expected the step to go on, got stop %v and %v", stop, err)
	}

	events := env.log.list()

	request := slices.Index(events, "regenerate")
	if request < 0 || request+1 >= len(events) {
		t.Fatalf("expected events after the request, got %v", events)
	}

	if next := events[request+1]; next != "save:"+RegenerateConfirmed+":v2" {
		t.Fatalf("expected the confirmed save of v2 right after the request, got %q in %v", next, events)
	}

	if read := slices.Index(events, "read:v2"); read < request+1 {
		t.Fatalf("expected v2 to be read only after the confirmed save, got %v", events)
	}

	variable := firstVariable(env.vault.lastRecord(t))
	if len(variable.Validation) == 0 || !AllPassed(variable.Validation) || variable.NewSerial == "" || variable.NewCertHash == "" {
		t.Fatalf("expected the passing checks saved with the new identity, got %+v", variable)
	}
}

// resumeWithBadSANs stores the leaf in state at v2, whose SANs differ from
// v1's.
func resumeWithBadSANs(t *testing.T, state string) {
	t.Helper()

	env := newRegenEnv(t)
	env.reissue(t, regenLeaf, "v2", func(spec *certSpec) { spec.dns = []string{"orders.ws.example", "elsewhere.example"} })
	env.setVariable(func(v *VariableRotation) {
		v.Baseline = []string{"v1"}
		v.RegenerateState = state
		v.NewVersionID = "v2"
	})

	stop, err := env.run()
	if err == nil {
		t.Fatal("expected the failed validation to be reported")
	}

	if n := env.credhub.regenerateCount(); n != 0 {
		t.Fatalf("expected no regenerate request, got %d", n)
	}

	if !slices.Contains(env.credhub.reads, "v2") {
		t.Fatalf("expected v2 to be read, got reads %v", env.credhub.reads)
	}

	expectFailedValidation(t, env, stop)
}

func TestRegenerateResumeRevalidatesConfirmed(t *testing.T) {
	t.Parallel()

	resumeWithBadSANs(t, RegenerateConfirmed)
}

func TestRegenerateResumeRevalidatesAdopted(t *testing.T) {
	t.Parallel()

	resumeWithBadSANs(t, RegenerateAdopted)
}

func TestRegenerateUncertainPausesWithCandidates(t *testing.T) {
	t.Parallel()

	env := newRegenEnv(t)
	env.credhub.answer(func(string) (*credhub.RegenerateResult, error) {
		env.reissue(t, regenLeaf, "v2", func(*certSpec) {})

		return nil, &credhub.UncertainError{Op: "regenerate", Cause: "deadline passed after the request was sent"}
	})

	stop, err := env.run()
	if !stop || err != nil {
		t.Fatalf("expected the step to stop for a decision, got stop %v and %v", stop, err)
	}

	expectReconcile(t, env, 1)
}

// expectReconcile checks that the instance waits on a reconcile decision
// with v2 as its one valid candidate, after requests regenerate requests.
func expectReconcile(t *testing.T, env *regenEnv, requests int) {
	t.Helper()

	if n := env.credhub.regenerateCount(); n != requests {
		t.Fatalf("expected %d regenerate requests, got %d", requests, n)
	}

	task := env.current()
	if task.Status != TaskStatusPaused || !task.Paused {
		t.Fatalf("expected the task paused, got %s", task.Status)
	}

	inst := task.Instances[0]
	if inst.Status != StatusAwaitingDecision {
		t.Fatalf("expected the instance awaiting a decision, got %s", inst.Status)
	}

	if state := firstVariable(task).RegenerateState; state != RegenerateOutcomeUnknown {
		t.Fatalf("expected outcome_unknown, got %s", state)
	}

	pending := inst.Certificates.Pending
	if pending == nil || pending.Kind != DecisionReconcileRegeneration || pending.Variable != regenLeaf {
		t.Fatalf("expected a reconcile decision for the leaf, got %+v", pending)
	}

	if len(pending.Candidates) != 1 {
		t.Fatalf("expected one candidate, got %+v", pending.Candidates)
	}

	candidate := pending.Candidates[0]
	if candidate.VersionID != "v2" || !candidate.CreatedAt.Equal(regenLater) || len(candidate.Checks) == 0 || !candidate.Valid || candidate.Serial == "" {
		t.Fatalf("expected v2 with its creation time and passing checks, got %+v", candidate)
	}

	if state := firstVariable(env.vault.lastRecord(t)).RegenerateState; state != RegenerateOutcomeUnknown {
		t.Fatalf("expected outcome_unknown saved, got %s", state)
	}
}

// A restart while the request may have been in flight never sends it
// again, and asks the operator.
func TestRegenerateResumeFromSendingReconciles(t *testing.T) {
	t.Parallel()

	env := newRegenEnv(t)
	env.reissue(t, regenLeaf, "v2", func(*certSpec) {})
	env.setVariable(func(v *VariableRotation) {
		v.Baseline = []string{"v1"}
		v.RegenerateState = RegenerateSending
	})

	stop, err := env.run()
	if !stop || err != nil {
		t.Fatalf("expected the step to stop for a decision, got stop %v and %v", stop, err)
	}

	expectReconcile(t, env, 0)
}

func TestRegenerateNeverRepeatsOnResume(t *testing.T) {
	t.Parallel()

	env := newRegenEnv(t)
	env.setVariable(func(v *VariableRotation) {
		v.Baseline = []string{"v1"}
		v.RegenerateState = RegenerateOutcomeUnknown
	})

	env.manager.mu.Lock()
	env.inst().Status = StatusAwaitingDecision
	env.inst().Certificates.Pending = &PendingDecision{Kind: DecisionReconcileRegeneration, Variable: regenLeaf, Since: validateNow}
	env.manager.mu.Unlock()

	stop, err := env.run()
	if !stop || err != nil {
		t.Fatalf("expected the step to stop, got stop %v and %v", stop, err)
	}

	if n := env.credhub.regenerateCount(); n != 0 {
		t.Fatalf("expected no regenerate request, got %d", n)
	}

	if status := env.current().Instances[0].Status; status != StatusAwaitingDecision {
		t.Fatalf("expected the instance still awaiting a decision, got %s", status)
	}
}

func TestRegenerateValidationFailureFailsInstance(t *testing.T) {
	t.Parallel()

	env := newRegenEnv(t)
	env.credhub.answer(func(string) (*credhub.RegenerateResult, error) {
		env.reissue(t, regenLeaf, "v2", func(spec *certSpec) { spec.cn = "someone-else.example" })

		return &credhub.RegenerateResult{VersionID: "v2"}, nil
	})

	stop, err := env.run()
	if err == nil {
		t.Fatal("expected the failed validation to be reported")
	}

	expectFailedValidation(t, env, stop)

	check, found := findCheck(firstVariable(env.current()).Validation, CheckCommonName)
	if !found || check.Passed {
		t.Fatalf("expected a failed common name check on the variable, got %+v", firstVariable(env.current()).Validation)
	}
}

// A refused request fails the instance and leaves the later variables
// untouched.
func TestRegenerateCertainErrorStopsBeforeLaterVariables(t *testing.T) {
	t.Parallel()

	env := newRegenEnv(t)
	env.addLeaf(t, regenLeaf2, "cert-2", "w1")

	env.manager.mu.Lock()
	env.inst().Certificates.Variables = append(env.inst().Certificates.Variables,
		VariableRotation{Name: regenLeaf2, CertificateID: "cert-2", OldVersionID: "w1", RegenerateState: RegenerateNotSent})
	env.manager.mu.Unlock()

	env.credhub.answer(func(string) (*credhub.RegenerateResult, error) {
		return nil, &credhub.APIError{Op: "regenerate", Status: http.StatusUnprocessableEntity}
	})

	stop, err := env.run()
	if !stop || err == nil {
		t.Fatalf("expected the step to stop with the error, got stop %v and %v", stop, err)
	}

	if n := env.credhub.regenerateCount(); n != 1 {
		t.Fatalf("expected one regenerate request, got %d", n)
	}

	task := env.current()
	if inst := task.Instances[0]; inst.Status != InstanceStatusFailed || !strings.Contains(inst.Error, "redis_crt") {
		t.Fatalf("expected the instance failed naming the leaf, got %s: %q", inst.Status, inst.Error)
	}

	variables := task.Instances[0].Certificates.Variables
	if variables[0].RegenerateState != RegenerateNotSent {
		t.Fatalf("expected a refused request to leave the leaf at not_sent, got %s", variables[0].RegenerateState)
	}

	if later := variables[1]; later.RegenerateState != RegenerateNotSent || later.Baseline != nil {
		t.Fatalf("expected the later variable untouched, got %+v", later)
	}
}

// expectSavedState checks the first variable's state in memory and in the
// record the vault stored last.
func expectSavedState(t *testing.T, env *regenEnv, memory, saved string) {
	t.Helper()

	if state := firstVariable(env.current()).RegenerateState; state != memory {
		t.Fatalf("expected the variable at %s, got %s", memory, state)
	}

	if state := firstVariable(env.vault.lastRecord(t)).RegenerateState; state != saved {
		t.Fatalf("expected %s saved, got %s", saved, state)
	}
}

// A broker that stops while the request is in flight gets an uncertain
// error under an ended context. That must reconcile and never go back to
// not_sent, which would send the request again on the next start.
func TestRegenerateUncertainDuringStopNeverResends(t *testing.T) {
	t.Parallel()

	env := newRegenEnv(t)
	ctx, cancel := context.WithCancel(context.Background())

	t.Cleanup(cancel)

	env.credhub.answer(func(string) (*credhub.RegenerateResult, error) {
		env.reissue(t, regenLeaf, "v2", func(*certSpec) {})
		cancel()

		return nil, &credhub.UncertainError{Op: "regenerate", Cause: "context canceled after the request was sent"}
	})

	stop, err := env.runWith(ctx)
	if !stop || err == nil {
		t.Fatalf("expected the step to stop with the ended context, got stop %v and %v", stop, err)
	}

	if n := env.credhub.regenerateCount(); n != 1 {
		t.Fatalf("expected one regenerate request, got %d", n)
	}

	// The candidate listing can't run under the ended context, so Vault
	// keeps sending, which the next start reconciles too.
	expectSavedState(t, env, RegenerateOutcomeUnknown, RegenerateSending)

	if status := env.current().Instances[0].Status; status == InstanceStatusFailed {
		t.Fatal("expected the instance not to fail")
	}

	// Resuming lists the candidates and still sends nothing.
	stop, err = env.run()
	if !stop || err != nil {
		t.Fatalf("expected the resumed step to stop for a decision, got stop %v and %v", stop, err)
	}

	expectReconcile(t, env, 1)
}

// A request that never reached CredHub because the broker was stopping
// is certain, so the variable goes back to not_sent without failing the
// instance.
func TestRegenerateCertainErrorDuringStopGoesBackToNotSent(t *testing.T) {
	t.Parallel()

	env := newRegenEnv(t)
	ctx, cancel := context.WithCancel(context.Background())

	t.Cleanup(cancel)

	env.credhub.answer(func(string) (*credhub.RegenerateResult, error) {
		cancel()

		return nil, fmt.Errorf("regenerate: %w", context.Canceled)
	})

	stop, err := env.runWith(ctx)
	if !stop || !errors.Is(err, context.Canceled) {
		t.Fatalf("expected the step to stop with the ended context, got stop %v and %v", stop, err)
	}

	if n := env.credhub.regenerateCount(); n != 1 {
		t.Fatalf("expected one regenerate request, got %d", n)
	}

	expectSavedState(t, env, RegenerateNotSent, RegenerateNotSent)

	if inst := env.current().Instances[0]; inst.Status != InstanceStatusRunning {
		t.Fatalf("expected the instance still running, got %s: %q", inst.Status, inst.Error)
	}
}

// An answer that names no version may still have made one, so it is
// reconciled like an uncertain error.
func TestRegenerateAnswerWithoutVersionReconciles(t *testing.T) {
	t.Parallel()

	env := newRegenEnv(t)
	env.credhub.answer(func(string) (*credhub.RegenerateResult, error) {
		env.reissue(t, regenLeaf, "v2", func(*certSpec) {})

		return &credhub.RegenerateResult{Name: regenLeaf}, nil
	})

	stop, err := env.run()
	if !stop || err != nil {
		t.Fatalf("expected the step to stop for a decision, got stop %v and %v", stop, err)
	}

	expectReconcile(t, env, 1)
}

// A confirmed save that can't be confirmed pauses the task, and resuming
// in the same process validates v2 without a second request.
func TestRegenerateUnconfirmedConfirmedSaveResumesWithoutResending(t *testing.T) {
	t.Parallel()

	env := newRegenEnv(t)
	env.vault.refuse = func(task UpgradeTask) bool { return firstVariable(task).RegenerateState == RegenerateConfirmed }

	stop, err := env.run()
	if !stop || !errors.Is(err, ErrSaveUnconfirmed) {
		t.Fatalf("expected the step to stop with an unconfirmed save, got stop %v and %v", stop, err)
	}

	if task := env.current(); task.Status != TaskStatusPaused || !strings.Contains(task.Instances[0].Error, "could not confirm") {
		t.Fatalf("expected the task paused for the save, got %s: %q", task.Status, task.Instances[0].Error)
	}

	expectSavedState(t, env, RegenerateConfirmed, RegenerateSending)

	env.vault.script.Lock()
	env.vault.refuse = nil
	env.vault.script.Unlock()

	stop, err = env.run()
	if stop || err != nil {
		t.Fatalf("expected the resumed step to go on, got stop %v and %v", stop, err)
	}

	if n := env.credhub.regenerateCount(); n != 1 {
		t.Fatalf("expected one regenerate request in all, got %d", n)
	}

	saved := firstVariable(env.vault.lastRecord(t))
	if saved.RegenerateState != RegenerateConfirmed || saved.NewVersionID != "v2" || !AllPassed(saved.Validation) || len(saved.Validation) == 0 {
		t.Fatalf("expected v2 confirmed and validated, got %+v", saved)
	}
}

// A baseline save that can't be confirmed sends nothing.
func TestRegenerateUnconfirmedBaselineSendsNothing(t *testing.T) {
	t.Parallel()

	env := newRegenEnv(t)
	env.vault.refuse = func(task UpgradeTask) bool { return firstVariable(task).RegenerateState == RegenerateNotSent }

	stop, err := env.run()
	if !stop || !errors.Is(err, ErrSaveUnconfirmed) {
		t.Fatalf("expected the step to stop with an unconfirmed save, got stop %v and %v", stop, err)
	}

	if n := env.credhub.regenerateCount(); n != 0 {
		t.Fatalf("expected no regenerate request, got %d", n)
	}

	if events := env.log.list(); slices.ContainsFunc(events, func(e string) bool { return strings.HasPrefix(e, "save:") }) {
		t.Fatalf("expected nothing saved, got %v", events)
	}

	task := env.current()
	if task.Status != TaskStatusPaused || firstVariable(task).RegenerateState != RegenerateNotSent {
		t.Fatalf("expected the task paused with the leaf at not_sent, got %s and %s", task.Status, firstVariable(task).RegenerateState)
	}
}

// A reconcile whose candidate listing fails stays in outcome_unknown, and
// no later pass sends the request again.
func TestRegenerateFailedCandidateListingNeverResends(t *testing.T) {
	t.Parallel()

	env := newRegenEnv(t)
	env.reissue(t, regenLeaf, "v2", func(*certSpec) {})
	env.setVariable(func(v *VariableRotation) {
		v.Baseline = []string{"v1"}
		v.RegenerateState = RegenerateSending
	})
	env.credhub.failVersionsOf(regenLeaf, &credhub.APIError{Op: "versions", Status: http.StatusServiceUnavailable})

	for pass := range 2 {
		stop, err := env.run()
		if !stop {
			t.Fatalf("pass %d: expected the step to stop", pass)
		}

		expectAPIError(t, err, http.StatusServiceUnavailable, apierr.CodeCredHubUnavailable)
		expectSavedState(t, env, RegenerateOutcomeUnknown, RegenerateOutcomeUnknown)

		if task := env.current(); task.Status != TaskStatusPaused || task.Instances[0].Certificates.Pending != nil {
			t.Fatalf("pass %d: expected the task paused with no decision yet, got %s and %+v", pass, task.Status, task.Instances[0].Certificates.Pending)
		}
	}

	env.credhub.failVersionsOf(regenLeaf, nil)

	stop, err := env.run()
	if !stop || err != nil {
		t.Fatalf("expected the step to stop for a decision, got stop %v and %v", stop, err)
	}

	expectReconcile(t, env, 0)
}

// A request that never reached CredHub, or that CredHub turned away for
// its token or for a missing permission, made no new version, so the task
// pauses with the leaf back at not_sent and sends it again on resume. The
// instance does not fail with the message of a refused regeneration.
func TestRegenerateCredHubOutagePausesForRetry(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		err     error
		message string
	}{
		{
			name:    "the connection could not be made",
			err:     errors.New("CredHub regenerate: dial tcp 10.0.0.9:8844: connect: connection refused"),
			message: "Blacksmith could not reach CredHub to regenerate redis_crt",
		},
		{
			name:    "the token could not be fetched",
			err:     errors.New("CredHub regenerate: fetching a UAA token: 503 Service Unavailable"),
			message: "Blacksmith could not reach CredHub to regenerate redis_crt",
		},
		{
			name:    "CredHub forbade the regenerate",
			err:     &credhub.APIError{Op: "regenerate", Status: http.StatusForbidden},
			message: "CredHub refused to regenerate redis_crt because Blacksmith's client lacks permission",
		},
		{
			name:    "CredHub did not accept the token",
			err:     &credhub.APIError{Op: "regenerate", Status: http.StatusUnauthorized},
			message: "CredHub did not accept Blacksmith's token when asked to regenerate redis_crt",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			env := newRegenEnv(t)
			env.credhub.answer(func(string) (*credhub.RegenerateResult, error) { return nil, tc.err })

			stop, err := env.run()
			if !stop {
				t.Fatal("expected the step to stop")
			}

			expectAPIError(t, err, http.StatusServiceUnavailable, apierr.CodeCredHubUnavailable)
			expectSavedState(t, env, RegenerateNotSent, RegenerateNotSent)

			task := env.current()
			inst := task.Instances[0]

			if task.Status != TaskStatusPaused || inst.Status != InstanceStatusRunning {
				t.Fatalf("expected the task paused and the instance resumable, got %s and %s", task.Status, inst.Status)
			}

			if !strings.Contains(inst.Error, tc.message) || !strings.Contains(inst.Error, "made no new version") || strings.Contains(inst.Error, "so it made no new version: ") {
				t.Fatalf("expected an accurate message, got %q", inst.Error)
			}

			// Once CredHub answers, resuming sends the request again.
			env.credhub.answer(func(string) (*credhub.RegenerateResult, error) {
				env.reissue(t, regenLeaf, "v2", func(*certSpec) {})

				return &credhub.RegenerateResult{VersionID: "v2"}, nil
			})

			stop, err = env.run()
			if stop || err != nil {
				t.Fatalf("expected the resumed step to go on, got stop %v and %v", stop, err)
			}

			if n := env.credhub.regenerateCount(); n != 2 {
				t.Fatalf("expected a second request on resume, got %d", n)
			}
		})
	}
}

// The step reads the task's rotation mode under mu, so a writer that
// holds mu never races it.
func TestRegenerateReadsModeUnderLock(t *testing.T) {
	t.Parallel()

	env := newRegenEnv(t)
	done := make(chan struct{})
	stopped := make(chan struct{})

	go func() {
		defer close(stopped)

		for {
			select {
			case <-done:
				return
			default:
			}

			env.manager.mu.Lock()
			env.task.Certificates.Mode = RotationModeRedeploy
			env.manager.mu.Unlock()
		}
	}()

	stop, err := env.run()

	close(done)
	<-stopped

	if stop || err != nil {
		t.Fatalf("expected the step to go on, got stop %v and %v", stop, err)
	}
}

// The decision prompt says that Blacksmith, not CredHub, won't send the
// request again, and a decision whose save can't be confirmed says so.
func TestRegenerateReconcileMessage(t *testing.T) {
	t.Parallel()

	uncertain := func(env *regenEnv) {
		env.credhub.answer(func(string) (*credhub.RegenerateResult, error) {
			env.reissue(t, regenLeaf, "v2", func(*certSpec) {})

			return nil, &credhub.UncertainError{Op: "regenerate", Cause: "deadline passed after the request was sent"}
		})
	}

	t.Run("saved", func(t *testing.T) {
		t.Parallel()

		env := newRegenEnv(t)
		uncertain(env)

		_, err := env.run()
		if err != nil {
			t.Fatalf("expected the decision saved, got %v", err)
		}

		message := env.current().Instances[0].Error
		if !strings.Contains(message, "Blacksmith won't send the CredHub request again.") || strings.Contains(message, "could not confirm") {
			t.Fatalf("expected the prompt to name Blacksmith, got %q", message)
		}
	})

	t.Run("unconfirmed", func(t *testing.T) {
		t.Parallel()

		env := newRegenEnv(t)
		uncertain(env)
		env.vault.refuse = func(task UpgradeTask) bool {
			return firstVariable(task).RegenerateState == RegenerateOutcomeUnknown
		}

		stop, err := env.run()
		if !stop || !errors.Is(err, ErrSaveUnconfirmed) {
			t.Fatalf("expected the step to stop with an unconfirmed save, got stop %v and %v", stop, err)
		}

		inst := env.current().Instances[0]
		if inst.Status != StatusAwaitingDecision || inst.Certificates.Pending == nil {
			t.Fatalf("expected the instance still awaiting the decision, got %s and %+v", inst.Status, inst.Certificates.Pending)
		}

		if !strings.Contains(inst.Error, "Blacksmith won't send the CredHub request again.") ||
			!strings.Contains(inst.Error, "Blacksmith could not confirm that it saved this task's progress to Vault") {
			t.Fatalf("expected the prompt and the unconfirmed save, got %q", inst.Error)
		}
	})
}
