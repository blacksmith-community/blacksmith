//nolint:testpackage // the tests drive the manager's unexported settle path
package upgrade

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"blacksmith/internal/audit"
	"blacksmith/internal/credhub"
)

// auditTrail keeps the audit records a vault saved, in the order they
// were saved.
type auditTrail struct {
	mu      sync.Mutex
	records []AuditRecord
}

// watchAudit has the env's vault keep every audit record it saves.
func (e *redeployEnv) watchAudit() *auditTrail {
	trail := &auditTrail{}

	e.vault.waitVault.onPut = func(path string, raw []byte) {
		if !strings.HasPrefix(path, certificateAuditVaultPath+"/") {
			return
		}

		var rec AuditRecord

		err := json.Unmarshal(raw, &rec)
		if err != nil {
			e.t.Errorf("an audit record is not valid JSON: %v", err)

			return
		}

		trail.mu.Lock()
		trail.records = append(trail.records, rec)
		trail.mu.Unlock()
	}

	return trail
}

func (a *auditTrail) all() []AuditRecord {
	a.mu.Lock()
	defer a.mu.Unlock()

	return append([]AuditRecord(nil), a.records...)
}

// events lists the events of the records saved so far.
func (a *auditTrail) events() []string {
	var events []string
	for _, rec := range a.all() {
		events = append(events, rec.Event)
	}

	return events
}

// find returns the first record of the event.
func (a *auditTrail) find(t *testing.T, event string) AuditRecord {
	t.Helper()

	for _, rec := range a.all() {
		if rec.Event == event {
			return rec
		}
	}

	t.Fatalf("no %s record in %v", event, a.events())

	return AuditRecord{}
}

// waitForAudit waits until the trail holds a record of the event.
func (a *auditTrail) waitFor(t *testing.T, event string) {
	t.Helper()

	waitUntil(t, "the "+event+" audit record", func() bool {
		for _, got := range a.events() {
			if got == event {
				return true
			}
		}

		return false
	})
}

// A redeploy task whose regeneration needs an adoption records its start,
// the adoption, and the settle of its instance, in that order, with the
// actor of the adopting request on the adoption.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestAuditOnStartSettleAndDecisions(t *testing.T) {
	env := newRedeployEnv(t)
	trail := env.watchAudit()

	env.credhub.answer(func(string) (*credhub.RegenerateResult, error) {
		env.credhub.addVersion(regenLeaf, "v2", regenLater, env.newLeaf)

		return nil, &credhub.UncertainError{Op: "regenerate", Cause: "deadline passed after the request was sent"}
	})

	task := env.task()
	task.CreatedBy, task.CreatedFrom = "alice", "10.0.0.5:4000 via 203.0.113.9"
	task.Certificates.Reason = "CHG-1234"

	manager := env.load(task)

	err := manager.ResumeTask(context.Background(), certTaskID)
	if err != nil {
		t.Fatalf("ResumeTask: %v", err)
	}

	waitUntil(t, "the reconcile decision", func() bool { return current(t, manager).Instances[0].Status == StatusAwaitingDecision })

	ctx := audit.WithActor(context.Background(), audit.Actor{Username: "bob", RemoteAddr: "10.0.0.9:5000", ForwardedFor: "198.51.100.7"})

	err = manager.ReconcileRegeneration(ctx, certTaskID, waitInstanceID, ReconcileRequest{
		Variable: regenLeaf, Action: "adopt", VersionID: "v2", ClientRequestID: "adopt-1",
	})
	if err != nil {
		t.Fatalf("ReconcileRegeneration: %v", err)
	}

	eventuallyInstance(t, manager, InstanceStatusSuccess)
	trail.waitFor(t, AuditInstanceSettled)

	want := []string{AuditTaskStarted, AuditAdopted, AuditInstanceSettled}
	if got := trail.events(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("expected the records %v, got %v", want, got)
	}

	started := trail.find(t, AuditTaskStarted)
	if started.Actor != "alice" || started.From != "10.0.0.5:4000 via 203.0.113.9" || started.Reason != "CHG-1234" || started.Mode != RotationModeRedeploy {
		t.Fatalf("unexpected start record: %+v", started)
	}

	adopted := trail.find(t, AuditAdopted)
	if adopted.Actor != "bob" || adopted.From != "10.0.0.9:5000 via 198.51.100.7" || adopted.InstanceID != waitInstanceID ||
		!strings.Contains(adopted.Detail, "v2") {
		t.Fatalf("unexpected adoption record: %+v", adopted)
	}

	settled := trail.find(t, AuditInstanceSettled)
	if settled.Outcome != string(InstanceStatusSuccess) || settled.InstanceID != waitInstanceID || settled.Actor != "alice" || settled.Reason != "CHG-1234" {
		t.Fatalf("unexpected settle record: %+v", settled)
	}

	if len(settled.Variables) != 1 {
		t.Fatalf("expected one variable, got %+v", settled.Variables)
	}

	variable := settled.Variables[0]
	if variable.Name != regenLeaf || variable.OldVersionID != "v1" || variable.NewVersionID != "v2" {
		t.Fatalf("unexpected variable: %+v", variable)
	}

	if settled.Approved == nil || len(settled.Approved.VMCIDs) != 0 || len(settled.Approved.Selected) != 1 {
		t.Fatalf("expected the approval without VM CIDs, got %+v", settled.Approved)
	}

	// Each record was indexed.
	var index map[string]interface{}

	found, err := env.vault.Get(context.Background(), certificateAuditVaultPath, &index)
	if err != nil || !found || len(index) != 3 {
		t.Fatalf("expected an index of 3 records, got %d (found %v, %v)", len(index), found, err)
	}
}

// The index only grows. A record that is saved later never removes or
// rewrites an ID the index already names.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestAuditIndexIsAppendOnly(t *testing.T) {
	env := newRedeployEnv(t)
	manager := env.newManager()

	err := env.vault.Put(context.Background(), certificateAuditVaultPath, map[string]interface{}{"old-id": map[string]interface{}{"event": "adopted"}})
	if err != nil {
		t.Fatalf("seeding the index: %v", err)
	}

	manager.audit(context.Background(), AuditRecord{ID: "first", At: validateNow, Event: AuditTaskStarted, TaskID: certTaskID})
	manager.audit(context.Background(), AuditRecord{ID: "second", At: validateNow, Event: AuditSkipped, TaskID: certTaskID})
	manager.audit(context.Background(), AuditRecord{ID: "second", At: validateNow, Event: AuditSkipped, TaskID: certTaskID})

	var index map[string]map[string]interface{}

	_, err = env.vault.Get(context.Background(), certificateAuditVaultPath, &index)
	if err != nil {
		t.Fatalf("reading the index: %v", err)
	}

	for _, id := range []string{"old-id", "first", "second"} {
		if _, named := index[id]; !named {
			t.Fatalf("the index lost %s: %v", id, index)
		}
	}

	if index["old-id"]["event"] != "adopted" || len(index) != 3 {
		t.Fatalf("the index rewrote an entry or grew wrongly: %v", index)
	}

	var record AuditRecord

	found, err := env.vault.Get(context.Background(), certificateAuditVaultPath+"/first", &record)
	if err != nil || !found || record.Event != AuditTaskStarted {
		t.Fatalf("expected the first record saved, got %+v (found %v, %v)", record, found, err)
	}
}

// The log line carries the marker, the reason, and who acted.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestAuditLogLine(t *testing.T) {
	env := newRedeployEnv(t)
	manager := env.newManager()

	manager.audit(context.Background(), AuditRecord{
		ID: "r1", At: validateNow, Event: AuditApproved, TaskID: certTaskID, InstanceID: waitInstanceID,
		Actor: "alice", From: "10.0.0.5:4000", Reason: "CHG-1234 leaf renewal", Mode: RotationModeRedeploy,
	})

	lines := env.logs.auditLines()
	if len(lines) != 1 {
		t.Fatalf("expected one audit line, got %q", lines)
	}

	for _, want := range []string{"audit=certificate-rotation", "event=approved", `reason="CHG-1234 leaf renewal"`, `actor="alice"`, "task=" + certTaskID} {
		if !strings.Contains(lines[0], want) {
			t.Fatalf("the line lacks %q: %s", want, lines[0])
		}
	}
}

// No record and no log line holds PEM text, even when the operator's
// reason or the actor holds some, and a record shows only names, IDs,
// serials, and fingerprints.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestAuditHoldsNoPEM(t *testing.T) {
	env := newRedeployEnv(t)
	trail := env.watchAudit()

	pem := "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----"
	key := "-----BEGIN RSA PRIVATE KEY-----\nMIIE\n-----END RSA PRIVATE KEY-----"

	task := env.task()
	task.CreatedBy = "alice"
	task.Certificates.Reason = "renew " + pem

	manager := env.load(task)

	manager.mu.Lock()
	inst := &manager.tasks[certTaskID].Instances[0]
	inst.Certificates.Variables[0].OldSerial = "01ab"
	inst.Certificates.Variables[0].OldCertHash = "f00d"
	manager.mu.Unlock()

	ctx := audit.WithActor(context.Background(), audit.Actor{Username: "bob\n" + key, RemoteAddr: "10.0.0.9:5000"})

	manager.auditDecision(ctx, certTaskID, inst, DecisionApproveChangedEffects, actionSkip, pem)
	manager.settleCertificateInstance(manager.tasks[certTaskID], inst, true, "")
	trail.waitFor(t, AuditInstanceSettled)
	waitUntil(t, "the audit records", manager.auditIdle)

	if len(trail.all()) < 2 {
		t.Fatalf("expected the decision and the settle recorded, got %v", trail.events())
	}

	for _, rec := range trail.all() {
		raw, err := json.Marshal(rec)
		if err != nil {
			t.Fatalf("encoding %s: %v", rec.Event, err)
		}

		if strings.Contains(string(raw), "BEGIN") || strings.Contains(string(raw), "PRIVATE KEY") {
			t.Fatalf("the %s record holds PEM: %s", rec.Event, raw)
		}
	}

	for _, line := range env.logs.auditLines() {
		if strings.Contains(line, "BEGIN") || strings.Contains(line, "PRIVATE KEY") {
			t.Fatalf("an audit line holds PEM: %s", line)
		}
	}

	settled := trail.find(t, AuditInstanceSettled)
	if settled.Reason != auditRedacted || settled.Variables[0].OldSerial != "01ab" || settled.Variables[0].OldCertSHA256 != "f00d" {
		t.Fatalf("expected the reason redacted and the serial and fingerprint kept, got %+v", settled)
	}
}

// Vault failing makes the writer try three times and give up quietly. A
// failing first try still lands on the second.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestAuditRetriesAndNeverFailsTheRotation(t *testing.T) {
	env := newRedeployEnv(t)
	manager := env.newManager()
	manager.auditRetry = time.Millisecond

	failing := &auditFlakyVault{waitVault: env.vault.waitVault, failures: 1}
	manager.vault = failing

	manager.audit(context.Background(), AuditRecord{ID: "flaky", At: validateNow, Event: AuditSkipped, TaskID: certTaskID})

	var record AuditRecord

	found, err := env.vault.Get(context.Background(), certificateAuditVaultPath+"/flaky", &record)
	if err != nil || !found {
		t.Fatalf("expected the record saved on the second try, found %v, %v", found, err)
	}

	always := &auditFlakyVault{waitVault: env.vault.waitVault, failures: 100}
	manager.vault = always

	manager.audit(context.Background(), AuditRecord{ID: "doomed", At: validateNow, Event: AuditSkipped, TaskID: certTaskID})

	if always.attempts() != auditAttempts {
		t.Fatalf("expected %d tries, got %d", auditAttempts, always.attempts())
	}
}

// auditFlakyVault refuses the first failures writes of audit records.
type auditFlakyVault struct {
	*waitVault

	mu       sync.Mutex
	failures int
	writes   int
}

func (v *auditFlakyVault) Put(ctx context.Context, path string, data interface{}) error {
	if !strings.HasPrefix(path, certificateAuditVaultPath+"/") {
		return v.waitVault.Put(ctx, path, data)
	}

	v.mu.Lock()
	v.writes++
	refuse := v.writes <= v.failures
	v.mu.Unlock()

	if refuse {
		return errPutRefused
	}

	return v.waitVault.Put(ctx, path, data)
}

func (v *auditFlakyVault) attempts() int {
	v.mu.Lock()
	defer v.mu.Unlock()

	return v.writes
}

// A patch instance's record names the VMs it touched by group and index,
// and never holds a CID or an address.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestAuditPatchRecordNamesVMsByGroupAndIndex(t *testing.T) {
	env := newRedeployEnv(t)
	trail := env.watchAudit()

	task := env.task()
	task.Certificates.Mode = RotationModePatch
	task.Instances[0].Status = InstanceStatusRunning
	task.Instances[0].Certificates.VMs = []VMJournal{
		{VM: "redis/uuid-a", CID: "vm-cid-a", Group: "redis", Index: 0, Phase: VMPhaseDone, Error: "dial 10.244.0.5:22"},
		{VM: "redis/uuid-b", CID: "vm-cid-b", Group: "redis", Index: 1, Phase: VMPhaseDone},
	}

	manager := env.load(task)
	manager.mu.Lock()
	live := manager.tasks[certTaskID]
	manager.mu.Unlock()

	manager.settleCertificateInstance(live, &live.Instances[0], true, "")
	trail.waitFor(t, AuditInstanceSettled)

	settled := trail.find(t, AuditInstanceSettled)
	if settled.Mode != RotationModePatch || len(settled.VMs) != 2 ||
		settled.VMs[0] != (AuditVM{Group: "redis", Index: 0, Phase: "done"}) || settled.VMs[1].Index != 1 {
		t.Fatalf("expected the VMs by group and index, got %+v", settled.VMs)
	}

	raw, err := json.Marshal(settled)
	if err != nil {
		t.Fatalf("encoding the record: %v", err)
	}

	for _, leak := range []string{"vm-cid", "10.244", "uuid-a"} {
		if strings.Contains(string(raw), leak) {
			t.Fatalf("the record holds %q: %s", leak, raw)
		}
	}
}

// A converge record says whether the divergence record was confirmed
// cleared: a converge that settled as a success confirmed it, and one that
// failed did not.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestAuditConvergeRecordSaysIfDivergenceCleared(t *testing.T) {
	for _, ok := range []bool{true, false} {
		env := newRedeployEnv(t)
		trail := env.watchAudit()

		task := env.task()
		task.Certificates.Mode = RotationModeConverge
		task.Instances[0].Status = InstanceStatusRunning
		task.Instances[0].Certificates.Phase = RotationPhaseConverge

		manager := env.load(task)
		manager.mu.Lock()
		live := manager.tasks[certTaskID]
		manager.mu.Unlock()

		manager.settleCertificateInstance(live, &live.Instances[0], ok, "the record stays")
		trail.waitFor(t, AuditInstanceSettled)

		settled := trail.find(t, AuditInstanceSettled)
		if settled.DivergenceCleared == nil || *settled.DivergenceCleared != ok {
			t.Fatalf("for success %v expected the cleared flag to match, got %v", ok, settled.DivergenceCleared)
		}
	}
}

// An instance finished from its recorded done deploy after a restart
// keeps the deploy's recorded end time in the task and in the audit
// record, never the time of the restart.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestAuditKeepsRecordedDeployEndTime(t *testing.T) {
	env := newRedeployEnv(t)
	trail := env.watchAudit()

	env.madeNewVersion()
	env.deployedNewVersion()

	recorded := validateNow.Add(-90 * time.Minute)

	task := env.deployedTask()
	task.Instances[0].Certificates.Deploy = &DeployOutcome{BOSHTaskID: redeployTaskID, State: boshTaskDone, FinishedAt: recorded}

	manager := env.load(task)

	finished := eventuallyInstance(t, manager, InstanceStatusSuccess)
	trail.waitFor(t, AuditInstanceSettled)

	if got := finished.Instances[0].Certificates.Deploy.FinishedAt; !got.Equal(recorded) {
		t.Fatalf("expected the recorded end %v kept on the task, got %v", recorded, got)
	}

	settled := trail.find(t, AuditInstanceSettled)
	if settled.DeployFinishedAt == nil || !settled.DeployFinishedAt.Equal(recorded) {
		t.Fatalf("expected the recorded end %v in the record, got %v", recorded, settled.DeployFinishedAt)
	}
}

// A resolution by an operator is recorded with the operator, and an
// abandon names the abandoned event.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestAuditResolveAbandonRecordsActor(t *testing.T) {
	env := newRedeployEnv(t)
	trail := env.watchAudit()
	manager := env.load(env.awaitingOutcome())

	ctx := audit.WithActor(context.Background(), audit.Actor{Username: "carol", RemoteAddr: "10.0.0.7:1234"})

	err := manager.ResolveDeployOutcome(ctx, certTaskID, waitInstanceID, ResolveRequest{Action: "abandon", ClientRequestID: "abandon-1"})
	if err != nil {
		t.Fatalf("ResolveDeployOutcome: %v", err)
	}

	trail.waitFor(t, AuditAbandoned)

	abandoned := trail.find(t, AuditAbandoned)
	if abandoned.Actor != "carol" || abandoned.From != "10.0.0.7:1234" || abandoned.InstanceID != waitInstanceID {
		t.Fatalf("unexpected record: %+v", abandoned)
	}
}

// A patch the runner settles is recorded with the VMs it patched, by
// group and index.
//
//nolint:paralleltest // the runner shares the manager's test seams
func TestAuditPatchRunRecordsPatchedVMs(t *testing.T) {
	env := newPatchEnv(t)
	trail := env.watchAudit()
	manager := env.newManager()
	task := env.task()

	if !env.run(manager, &task) {
		t.Fatalf("expected the instance to settle, got %+v", current(t, manager).Instances[0])
	}

	trail.waitFor(t, AuditInstanceSettled)

	settled := trail.find(t, AuditInstanceSettled)
	if settled.Mode != RotationModePatch || settled.Outcome != string(InstanceStatusSuccess) || len(settled.VMs) != len(patchCIDs) {
		t.Fatalf("unexpected patch record: %+v", settled)
	}

	raw, err := json.Marshal(settled)
	if err != nil {
		t.Fatalf("encoding the record: %v", err)
	}

	for _, cid := range patchCIDs {
		if strings.Contains(string(raw), cid) {
			t.Fatalf("the record holds the CID %s: %s", cid, raw)
		}
	}

	if settled.DivergenceCleared != nil {
		t.Fatalf("a patch that left its converge for later has no cleared flag, got %v", *settled.DivergenceCleared)
	}
}

// A converge the runner settles says whether it confirmed the divergence
// record cleared.
//
//nolint:paralleltest // the runner shares the manager's test seams
func TestAuditConvergeRunReportsDivergenceCleared(t *testing.T) {
	env := newConvergeEnv(t)
	env.diverge()

	trail := env.watchAudit()
	manager := env.newManager()
	task := env.task()

	if !env.run(manager, &task) {
		t.Fatalf("expected the instance to settle, got %+v", current(t, manager).Instances[0])
	}

	trail.waitFor(t, AuditInstanceSettled)

	settled := trail.find(t, AuditInstanceSettled)
	if settled.Mode != RotationModeConverge || settled.DivergenceCleared == nil || !*settled.DivergenceCleared {
		t.Fatalf("expected a converge record with the divergence cleared, got %+v", settled)
	}

	failing := newConvergeEnv(t)
	failing.diverge()
	failing.director.deployErr = errDirectorRefused

	failTrail := failing.watchAudit()
	failManager := failing.newManager()
	failTask := failing.task()

	if !failing.run(failManager, &failTask) {
		t.Fatalf("expected the refused converge to settle, got %+v", current(t, failManager).Instances[0])
	}

	failTrail.waitFor(t, AuditInstanceSettled)

	failed := failTrail.find(t, AuditInstanceSettled)
	if failed.Outcome != string(InstanceStatusFailed) || failed.DivergenceCleared == nil || *failed.DivergenceCleared {
		t.Fatalf("expected a failed converge with the divergence not cleared, got %+v", failed)
	}
}
