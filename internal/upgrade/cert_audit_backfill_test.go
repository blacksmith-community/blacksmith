//nolint:testpackage // the tests drive the manager's unexported loader
package upgrade

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"blacksmith/internal/audit"
)

var (
	backfillStarted   = validateNow.Add(time.Minute)
	backfillCompleted = validateNow.Add(10 * time.Minute)
)

// countingTrail counts the audit records a vault is asked to save.
type countingTrail struct {
	mu   sync.Mutex
	puts []string
}

func (c *countingTrail) record(path string) {
	if !strings.HasPrefix(path, certificateAuditVaultPath+"/") {
		return
	}

	c.mu.Lock()
	c.puts = append(c.puts, strings.TrimPrefix(path, certificateAuditVaultPath+"/"))
	c.mu.Unlock()
}

func (c *countingTrail) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return len(c.puts)
}

// settledTask returns the task as a restart finds it after its one
// instance settled and the broker went down before the audit write.
func (e *redeployEnv) settledTask() UpgradeTask {
	task := e.deployedTask()
	task.Status = TaskStatusCompleted
	task.CreatedBy, task.CreatedFrom = "alice", "10.0.0.5:4000"
	task.StartedAt = &backfillStarted
	task.CompletedAt = &backfillCompleted

	inst := &task.Instances[0]
	inst.Status = InstanceStatusSuccess
	inst.CompletedAt = &backfillCompleted

	return task
}

// A broker that went down after a settle was saved and before its audit
// record was written rebuilds the record at the next load, once, with the
// ID, the actor, and the time the task has on record. A second load finds
// the record and writes nothing.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestAuditBackfillsASettleLostToACrash(t *testing.T) {
	env := newRedeployEnv(t)
	trail := env.watchAudit()
	puts := &countingTrail{}

	onPut := env.vault.waitVault.onPut
	env.vault.waitVault.onPut = func(path string, raw []byte) {
		puts.record(path)
		onPut(path, raw)
	}

	first := env.load(env.settledTask())
	waitUntil(t, "the rebuilt records", func() bool { return first.auditIdle() && puts.count() >= 2 })

	settled := trail.find(t, AuditInstanceSettled)
	if settled.ID != auditID(certTaskID, waitInstanceID, AuditInstanceSettled, "") || settled.Actor != "alice" || settled.From != "10.0.0.5:4000" {
		t.Fatalf("unexpected rebuilt settle record: %+v", settled)
	}

	if !settled.At.Equal(backfillCompleted) || settled.Outcome != string(InstanceStatusSuccess) {
		t.Fatalf("expected the persisted completion time and outcome, got %+v", settled)
	}

	started := trail.find(t, AuditTaskStarted)
	if started.ID != auditID(certTaskID, "", AuditTaskStarted, "") || !started.At.Equal(backfillStarted) {
		t.Fatalf("unexpected rebuilt start record: %+v", started)
	}

	if got := puts.count(); got != 2 {
		t.Fatalf("expected exactly 2 records written, got %d (%v)", got, trail.events())
	}

	first.Stop()

	second := env.newManager()
	second.Start()
	waitForLoad(t, second)
	waitUntil(t, "the second load's audit queue", second.auditIdle)

	if got := puts.count(); got != 2 {
		t.Fatalf("expected the second load to write nothing new, got %d records in all", got)
	}
}

// A decision whose record was lost is rebuilt with the operator who made
// it, because the actor is saved with the decision, and it comes before
// the settle it caused.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestAuditBackfillsADecisionWithItsOperator(t *testing.T) {
	env := newRedeployEnv(t)
	trail := env.watchAudit()

	decided := validateNow.Add(9 * time.Minute)
	task := env.settledTask()
	inst := &task.Instances[0]
	inst.Status = InstanceStatusOutcomeUnknown
	inst.Certificates.Phase = RotationPhaseDone
	inst.Certificates.DecisionRequestID = "abandon-1"
	inst.Certificates.Decision = &DecisionAudit{
		ID: "decision-1", Event: AuditAbandoned, Detail: "resolve_deploy_outcome: abandon", Actor: "carol", From: "10.0.0.7:1234 via 198.51.100.4",
		Outcome: string(InstanceStatusOutcomeUnknown), At: decided,
	}

	manager := env.load(task)
	waitUntil(t, "the rebuilt records", func() bool { return manager.auditIdle() && len(trail.events()) >= 3 })

	abandoned := trail.find(t, AuditAbandoned)
	if abandoned.ID != auditID(certTaskID, waitInstanceID, AuditAbandoned, "decision-1") || abandoned.Actor != "carol" ||
		abandoned.From != "10.0.0.7:1234 via 198.51.100.4" || !abandoned.At.Equal(decided) {
		t.Fatalf("unexpected rebuilt decision record: %+v", abandoned)
	}

	want := []string{AuditTaskStarted, AuditAbandoned, AuditInstanceSettled}
	if got := trail.events(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("expected %v, got %v", want, got)
	}
}

// A real resolve whose audit writes all failed leaves the persisted task
// with the decision and its operator, so the next load writes the same
// records the live path would have, under the same IDs.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestAuditBackfillsAfterAResolveWhoseWritesFailed(t *testing.T) {
	env := newRedeployEnv(t)
	trail := env.watchAudit()

	storeCertificateTask(t, env.vault.waitVault, env.awaitingOutcome())

	manager := env.newManager()
	manager.auditRetry = time.Millisecond
	manager.vault = &auditFlakyVault{waitVault: env.vault.waitVault, failures: 1000}
	manager.Start()
	waitForLoad(t, manager)

	ctx := audit.WithActor(context.Background(), audit.Actor{Username: "carol", RemoteAddr: "10.0.0.7:1234"})

	err := manager.ResolveDeployOutcome(ctx, certTaskID, waitInstanceID, ResolveRequest{Action: "abandon", ClientRequestID: "abandon-1"})
	if err != nil {
		t.Fatalf("ResolveDeployOutcome: %v", err)
	}

	waitUntil(t, "the failed writes", manager.auditIdle)

	if len(trail.events()) != 0 {
		t.Fatalf("expected no record to land, got %v", trail.events())
	}

	manager.Stop()

	second := env.newManager()
	second.Start()
	waitForLoad(t, second)
	waitUntil(t, "the rebuilt records", func() bool { return second.auditIdle() && len(trail.events()) >= 2 })

	abandoned := trail.find(t, AuditAbandoned)
	if abandoned.ID == "" || abandoned.Actor != "carol" || abandoned.From != "10.0.0.7:1234" {
		t.Fatalf("expected the operator on the rebuilt record, got %+v", abandoned)
	}

	trail.find(t, AuditInstanceSettled)
}

// An instance settled while the task loads, because its creation was
// never confirmed, is recorded by the same rebuild.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestAuditBackfillsAnOrphanFailedAtLoad(t *testing.T) {
	env := newRedeployEnv(t)
	trail := env.watchAudit()

	task := env.task()
	task.CreationConfirmed = false
	task.Status = TaskStatusPending
	task.CreatedBy = "alice"

	manager := env.load(task)
	waitUntil(t, "the orphan's record", func() bool { return manager.auditIdle() && len(trail.events()) >= 1 })

	settled := trail.find(t, AuditInstanceSettled)
	if settled.Outcome != string(InstanceStatusFailed) || settled.Actor != "alice" {
		t.Fatalf("unexpected record for the orphan: %+v", settled)
	}
}

// Decisions come before the settles they cause, on the live path as well.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestAuditResolveRecordsTheDecisionBeforeTheSettle(t *testing.T) {
	env := newRedeployEnv(t)
	trail := env.watchAudit()
	manager := env.load(env.awaitingOutcome())

	ctx := audit.WithActor(context.Background(), audit.Actor{Username: "carol", RemoteAddr: "10.0.0.7:1234"})

	err := manager.ResolveDeployOutcome(ctx, certTaskID, waitInstanceID, ResolveRequest{Action: "abandon", ClientRequestID: "abandon-2"})
	if err != nil {
		t.Fatalf("ResolveDeployOutcome: %v", err)
	}

	trail.waitFor(t, AuditInstanceSettled)

	events := trail.events()
	decided, settled := slices.Index(events, AuditAbandoned), slices.Index(events, AuditInstanceSettled)

	if decided < 0 || settled < 0 || decided > settled {
		t.Fatalf("expected the decision before the settle, got %v", events)
	}
}

// Two decisions of one event on an instance, sent without request IDs, each
// get their own record, and a replay of a decision that did carry an ID
// writes none.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestAuditGivesEveryDecisionItsOwnRecord(t *testing.T) {
	env := newRedeployEnv(t)
	trail := env.watchAudit()
	manager := env.load(env.task())

	ctx := audit.WithActor(context.Background(), audit.Actor{Username: "bob", RemoteAddr: "10.0.0.9:5000"})

	manager.mu.Lock()
	live := manager.tasks[certTaskID]
	inst := &live.Instances[0]

	for _, variable := range []string{"v1", "v2"} {
		recordDecisionLocked(inst, "", "", manager.decisionAuditLocked(ctx, DecisionReconcileRegeneration, actionAdopt, "variable "+variable))
		manager.auditDecisionLocked(live, inst)
	}

	manager.mu.Unlock()

	waitUntil(t, "the audit queue", manager.auditIdle)

	adopted := 0

	for _, event := range trail.events() {
		if event == AuditAdopted {
			adopted++
		}
	}

	if adopted != 2 {
		t.Fatalf("expected two adopted records, got %v", trail.events())
	}
}

// A replay of a resolve with the same client request ID applies nothing
// new, so it writes no new record.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestAuditReplayWritesNoNewDecisionRecord(t *testing.T) {
	env := newRedeployEnv(t)
	trail := env.watchAudit()
	manager := env.load(env.awaitingOutcome())

	ctx := audit.WithActor(context.Background(), audit.Actor{Username: "carol", RemoteAddr: "10.0.0.7:1234"})

	for range 2 {
		err := manager.ResolveDeployOutcome(ctx, certTaskID, waitInstanceID, ResolveRequest{Action: "abandon", ClientRequestID: "same"})
		if err != nil {
			t.Fatalf("ResolveDeployOutcome: %v", err)
		}
	}

	waitUntil(t, "the audit queue", manager.auditIdle)

	count := 0

	for _, event := range trail.events() {
		if event == AuditAbandoned {
			count++
		}
	}

	if count != 1 {
		t.Fatalf("expected one abandoned record for the replayed request, got %v", trail.events())
	}
}
