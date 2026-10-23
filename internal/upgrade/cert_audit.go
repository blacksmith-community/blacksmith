package upgrade

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// certificateAuditVaultPath is the index of the audit records, and each
// record is saved under it by its ID.
const certificateAuditVaultPath = "certificate-audit"

// The events an audit record names.
const (
	AuditTaskStarted     = "task_started"
	AuditInstanceSettled = "instance_settled"
	AuditAdopted         = "adopted"
	AuditAbandoned       = "abandoned"
	AuditApproved        = "approved"
	AuditSkipped         = "skipped"
	AuditVerified        = "verified"
	AuditMarkedFailed    = "marked_failed"
)

const (
	// auditAttempts is how many times a record's write is tried.
	auditAttempts = 3
	// auditRetryDefault is the wait between two tries.
	auditRetryDefault = 500 * time.Millisecond
	// auditWriteTimeout bounds one try.
	auditWriteTimeout = 10 * time.Second
	// auditRedacted replaces a value that looked like a certificate or key.
	auditRedacted = "[redacted]"
)

// AuditRecord is one entry of the certificate audit trail. It holds
// variable names, version IDs, serials, and fingerprints, and never a
// private key, a certificate body, or any other secret value. A patch
// names the VMs it touched by group and index, never by CID or address.
type AuditRecord struct {
	ID         string    `json:"id"`
	At         time.Time `json:"at"`
	Event      string    `json:"event"`
	TaskID     string    `json:"task_id"`
	InstanceID string    `json:"instance_id,omitempty"`
	// Actor is the user who acted, and From is where the request came
	// from as the client reported it. Both are for the record alone.
	Actor     string           `json:"actor,omitempty"`
	From      string           `json:"from,omitempty"`
	Reason    string           `json:"reason,omitempty"`
	Mode      RotationMode     `json:"mode,omitempty"`
	Approved  *ApprovedEffects `json:"approved,omitempty"`
	Variables []AuditVariable  `json:"variables,omitempty"`
	Outcome   string           `json:"outcome,omitempty"`
	// Detail names the operator's choice or the task's size.
	Detail string `json:"detail,omitempty"`
	// VMs lists the VMs a patch touched, by group and index.
	VMs []AuditVM `json:"vms,omitempty"`
	// DivergenceCleared is set on an instance a converge deploy settled,
	// and says whether the divergence record was confirmed cleared.
	DivergenceCleared *bool `json:"divergence_cleared,omitempty"`
	// DeployFinishedAt is when the instance's last deploy ended on the
	// director, as the record of that deploy has it.
	DeployFinishedAt *time.Time `json:"deploy_finished_at,omitempty"`
}

// AuditVariable is what a record says about one variable.
type AuditVariable struct {
	Name          string     `json:"name"`
	OldSerial     string     `json:"old_serial,omitempty"`
	NewSerial     string     `json:"new_serial,omitempty"`
	OldVersionID  string     `json:"old_version_id,omitempty"`
	NewVersionID  string     `json:"new_version_id,omitempty"`
	OldNotAfter   time.Time  `json:"old_not_after"`
	NewNotAfter   *time.Time `json:"new_not_after,omitempty"`
	OldCertSHA256 string     `json:"old_cert_sha256,omitempty"`
	NewCertSHA256 string     `json:"new_cert_sha256,omitempty"`
}

// AuditVM names a VM a patch touched by its instance group and index.
type AuditVM struct {
	Group string `json:"group"`
	Index int    `json:"index"`
	Phase string `json:"phase"`
}

// auditQueue holds the records waiting to be written, so callers that hold
// mu never wait on Vault and the records are written in the order they
// were made.
type auditQueue struct {
	mu      sync.Mutex
	pending []AuditRecord
	running bool
}

// audit writes the record to Vault and to the log. The record is saved at
// certificate-audit/<id> and its ID is then added to the index
// certificate-audit, which is read, extended, and written back and never
// has an ID removed or rewritten. A failed try is repeated, up to three
// tries in all, and a record that never lands is logged as an error. A
// failure never fails the rotation.
func (m *Manager) audit(ctx context.Context, rec AuditRecord) bool {
	rec = sanitizeAudit(rec)

	m.logAudit(rec)

	m.auditWrite.Lock()
	defer m.auditWrite.Unlock()

	retry := m.auditRetry
	if retry <= 0 {
		retry = auditRetryDefault
	}

	var err error

	for attempt := 1; attempt <= auditAttempts; attempt++ {
		err = m.writeAudit(ctx, rec)
		if err == nil {
			return true
		}

		m.logger.Error("audit=certificate-rotation record=%s event=%s could not be saved on try %d of %d: %v", rec.ID, rec.Event, attempt, auditAttempts, err)

		// Once the manager is stopping, a failed first try is the last. The
		// record comes back from the persisted task at the next load.
		if attempt < auditAttempts {
			select {
			case <-ctx.Done():
				attempt = auditAttempts
			case <-m.ctx.Done():
				attempt = auditAttempts
			case <-time.After(retry):
			}
		}
	}

	m.logger.Error("audit=certificate-rotation record=%s event=%s task=%s was never saved to Vault: %v", rec.ID, rec.Event, rec.TaskID, err)

	return false
}

// writeAudit makes one try at saving the record and its index entry.
func (m *Manager) writeAudit(ctx context.Context, rec AuditRecord) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), auditWriteTimeout)
	defer cancel()

	err := m.vault.Put(ctx, certificateAuditVaultPath+"/"+rec.ID, rec)
	if err != nil {
		return fmt.Errorf("writing the record: %w", err)
	}

	var index map[string]interface{}

	_, err = m.vault.Get(ctx, certificateAuditVaultPath, &index)
	if err != nil {
		return fmt.Errorf("reading the index: %w", err)
	}

	if _, named := index[rec.ID]; named {
		return nil
	}

	// A copy keeps every ID the index already names, and adds this one.
	extended := make(map[string]interface{}, len(index)+1)
	for id, entry := range index {
		extended[id] = entry
	}

	extended[rec.ID] = map[string]interface{}{
		"at":      rec.At.UTC().Format(time.RFC3339Nano),
		"event":   rec.Event,
		"task_id": rec.TaskID,
	}

	err = m.vault.Put(ctx, certificateAuditVaultPath, extended)
	if err != nil {
		return fmt.Errorf("writing the index: %w", err)
	}

	return nil
}

// logAudit writes the record's structured log line.
// The line ends with the whole sanitised record as one JSON value, so the
// log alone says what changed if Vault never takes the record. JSON escapes
// line breaks, so the line stays a single line.
func (m *Manager) logAudit(rec AuditRecord) {
	encoded := "{}"

	if raw, err := json.Marshal(rec); err == nil {
		encoded = string(raw)
	}

	m.logger.Info("audit=certificate-rotation id=%s event=%s task=%s instance=%s mode=%s actor=%q from=%q reason=%q outcome=%q detail=%q variables=%s record=%s",
		rec.ID, rec.Event, rec.TaskID, rec.InstanceID, rec.Mode, rec.Actor, rec.From, rec.Reason, rec.Outcome, rec.Detail, auditVariableNames(rec.Variables), encoded)
}

func auditVariableNames(variables []AuditVariable) string {
	names := make([]string, 0, len(variables))
	for _, variable := range variables {
		names = append(names, variable.Name)
	}

	return strings.Join(names, ",")
}

// sanitizeAudit cuts any PEM block out of a text field and keeps the text
// around it, so even a reason an operator pasted a certificate into never
// reaches Vault or the log. Line breaks are dropped from the free-text fields, so a value can't
// forge a second log line.
func sanitizeAudit(rec AuditRecord) AuditRecord {
	rec.Actor = auditText(rec.Actor)
	rec.From = auditText(rec.From)
	rec.Reason = auditText(rec.Reason)
	rec.Outcome = auditText(rec.Outcome)
	rec.Detail = auditText(rec.Detail)
	rec.Variables = append([]AuditVariable(nil), rec.Variables...)

	for i := range rec.Variables {
		rec.Variables[i].Name = auditText(rec.Variables[i].Name)
	}

	return rec
}

func auditText(s string) string {
	s = pemBlock.ReplaceAllString(s, auditRedacted)

	return strings.NewReplacer("\r", " ", "\n", " ").Replace(s)
}

// pemBlock matches a PEM block, from its BEGIN line to its END line, or to
// the end of the text when the block is unfinished.
var pemBlock = regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]+-----.*?(?:-----END [A-Z0-9 ]+-----|$)`)

// enqueueAuditLocked queues the record to be written in the background, in
// the order records were queued. The caller holds mu, which is what lets
// the writer join the WaitGroup while Stop may be running. A stopping
// manager still writes the record, on a goroutine Stop doesn't wait for.
func (m *Manager) enqueueAuditLocked(rec AuditRecord) {
	m.auditQ.mu.Lock()
	m.auditQ.pending = append(m.auditQ.pending, rec)
	start := !m.auditQ.running
	m.auditQ.running = true
	m.auditQ.mu.Unlock()

	if !start {
		return
	}

	if m.ctx.Err() == nil {
		m.wg.Add(1)

		go func() {
			defer m.wg.Done()

			m.drainAudit() //nolint:contextcheck // the writes run on the manager's lifecycle context, so a finished request never cancels them
		}()

		return
	}

	go m.drainAudit() //nolint:contextcheck // the writes run on the manager's lifecycle context, so a finished request never cancels them
}

// drainAudit writes the queued records until none is left.
func (m *Manager) drainAudit() {
	for {
		m.auditQ.mu.Lock()

		if len(m.auditQ.pending) == 0 {
			m.auditQ.running = false
			m.auditQ.mu.Unlock()

			return
		}

		rec := m.auditQ.pending[0]
		m.auditQ.pending = m.auditQ.pending[1:]
		m.auditQ.mu.Unlock()

		saved := m.audit(context.WithoutCancel(m.ctx), rec)
		if !saved && m.ctx.Err() != nil {
			m.dropPendingAudit()

			return
		}
	}
}

// dropPendingAudit gives up the records still queued while the manager
// stops with a Vault that refuses them. The next load writes them again
// from the persisted tasks.
func (m *Manager) dropPendingAudit() {
	m.auditQ.mu.Lock()
	dropped := len(m.auditQ.pending)
	m.auditQ.pending = nil
	m.auditQ.running = false
	m.auditQ.mu.Unlock()

	m.logger.Error("audit=certificate-rotation %d queued records were not saved before shutdown, and the next start rebuilds them", dropped)
}

// auditIdle reports whether every queued record has been written.
func (m *Manager) auditIdle() bool {
	m.auditQ.mu.Lock()
	defer m.auditQ.mu.Unlock()

	return !m.auditQ.running && len(m.auditQ.pending) == 0
}

// auditRecordLocked builds the record of an event on the task's instance
// from what the task holds now, with the actor and reason the task
// recorded. inst may be nil for an event of the task as a whole. The
// caller holds mu.
func (m *Manager) auditRecordLocked(task *UpgradeTask, inst *InstanceUpgrade, event string) AuditRecord {
	instanceID := ""
	if inst != nil {
		instanceID = inst.InstanceID
	}

	rec := AuditRecord{
		ID:     auditID(task.ID, instanceID, event, ""),
		At:     m.certNowLocked(),
		Event:  event,
		TaskID: task.ID,
		Actor:  task.CreatedBy,
		From:   task.CreatedFrom,
		Mode:   certTaskMode(task),
	}

	if task.Certificates != nil {
		rec.Reason = task.Certificates.Reason
	}

	if inst == nil {
		return rec
	}

	rec.InstanceID = inst.InstanceID

	rotation := inst.Certificates
	if rotation == nil {
		return rec
	}

	approved := rotation.Approved.Clone()
	// The CIDs name VMs, and a record names them by group and index only.
	approved.VMCIDs = nil
	rec.Approved = &approved

	for _, variable := range rotation.Variables {
		rec.Variables = append(rec.Variables, AuditVariable{
			Name:          variable.Name,
			OldSerial:     variable.OldSerial,
			NewSerial:     variable.NewSerial,
			OldVersionID:  variable.OldVersionID,
			NewVersionID:  variable.NewVersionID,
			OldNotAfter:   variable.OldNotAfter,
			NewNotAfter:   copyTime(variable.NewNotAfter),
			OldCertSHA256: variable.OldCertHash,
			NewCertSHA256: variable.NewCertHash,
		})
	}

	if rotation.Deploy != nil && !rotation.Deploy.FinishedAt.IsZero() {
		finished := rotation.Deploy.FinishedAt
		rec.DeployFinishedAt = &finished
	}

	return rec
}

// auditNamespace is the fixed namespace of the audit record IDs.
var auditNamespace = uuid.MustParse("6f1c2a52-8d0e-4b6e-9a43-0c7b1d5e2f90")

// auditID derives a record's ID from what it records, so the record of
// one event has the same ID however many times it is built. A settle is
// keyed by the task, the instance, and the event, a start by the task and
// the event, and a decision also by its request ID.
func auditID(taskID, instanceID, event, requestID string) string {
	return uuid.NewSHA1(auditNamespace, []byte(strings.Join([]string{taskID, instanceID, event, requestID}, "/"))).String()
}

// enqueueAuditOnceLocked queues the record unless one with its ID was
// queued or found in the index already. The caller holds mu.
func (m *Manager) enqueueAuditOnceLocked(rec AuditRecord) {
	if m.audited[rec.ID] {
		return
	}

	if m.audited == nil {
		m.audited = make(map[string]bool)
	}

	m.audited[rec.ID] = true

	m.enqueueAuditLocked(rec)
}

// settleRecordLocked builds the instance_settled record of a settled
// instance from what the task holds. The caller holds mu.
func (m *Manager) settleRecordLocked(task *UpgradeTask, inst *InstanceUpgrade) AuditRecord {
	rec := m.auditRecordLocked(task, inst, AuditInstanceSettled)
	rec.Outcome = string(inst.Status)

	if inst.Certificates != nil {
		for _, journal := range inst.Certificates.VMs {
			rec.VMs = append(rec.VMs, AuditVM{Group: journal.Group, Index: journal.Index, Phase: string(journal.Phase)})
		}
	}

	// Without a divergence store nothing is confirmed cleared.
	if convergesLocked(task, inst) && m.deps.Divergence != nil {
		cleared := inst.Status == InstanceStatusSuccess
		rec.DivergenceCleared = &cleared
	}

	return rec
}

// auditSettledLocked queues an instance_settled record for each instance
// of the certificate task that has settled and has none yet. An instance
// that still waits for a decision has not settled. The caller holds mu.
func (m *Manager) auditSettledLocked(task *UpgradeTask) {
	for i := range task.Instances {
		inst := &task.Instances[i]
		if !inst.Status.settled() || waitsForDecision(inst) {
			continue
		}

		m.enqueueAuditOnceLocked(m.settleRecordLocked(task, inst))
	}
}

// decisionRecordLocked builds the record of the instance's latest decision
// from the details saved with it, or reports false when it has none. The
// caller holds mu.
func (m *Manager) decisionRecordLocked(task *UpgradeTask, inst *InstanceUpgrade) (AuditRecord, bool) {
	if inst.Certificates == nil || inst.Certificates.Decision == nil || inst.Certificates.Decision.Event == "" {
		return AuditRecord{}, false
	}

	decision := inst.Certificates.Decision

	rec := m.auditRecordLocked(task, inst, decision.Event)
	rec.ID = auditID(task.ID, inst.InstanceID, decision.Event, inst.Certificates.DecisionRequestID)
	rec.At = decision.At
	rec.Detail = decision.Detail
	rec.Outcome = decision.Outcome

	if decision.Actor != "" || decision.From != "" {
		rec.Actor = decision.Actor
		rec.From = decision.From
	}

	return rec, true
}

// auditDecisionLocked queues the record of the decision just applied to
// the instance, with the operator and address of the request. The caller
// holds mu.
func (m *Manager) auditDecisionLocked(task *UpgradeTask, inst *InstanceUpgrade) {
	rec, ok := m.decisionRecordLocked(task, inst)
	if !ok {
		return
	}

	m.enqueueAuditOnceLocked(rec)
}

// startRecordLocked builds the task_started record. The caller holds mu.
func (m *Manager) startRecordLocked(task *UpgradeTask) AuditRecord {
	rec := m.auditRecordLocked(task, nil, AuditTaskStarted)
	rec.Outcome = "started"
	rec.Detail = fmt.Sprintf("%d instances", len(task.Instances))

	return rec
}

// auditStartLocked queues the task_started record. The caller holds mu.
func (m *Manager) auditStartLocked(task *UpgradeTask) {
	m.enqueueAuditOnceLocked(m.startRecordLocked(task))
}

// backfillAudit writes the audit records that the loaded certificate tasks
// lack, rebuilt from what the tasks persisted, so a crash between a settle
// and its audit write loses nothing. It reads the audit index once. A
// record's ID comes from what it records, so a record that is in the index
// is not written again, and a second load writes nothing new. A record
// carries the time its event persisted: the start, the decision, or the
// instance's completion. This covers the instances that settle while the
// tasks load, such as those of a task whose creation was never confirmed.
// It reports false when Stop ended the read of the index.
func (m *Manager) backfillAudit(loaded []*UpgradeTask) bool {
	var certificateTasks []*UpgradeTask

	for _, task := range loaded {
		if task.KindOrDefault() == TaskKindCertificates {
			certificateTasks = append(certificateTasks, task)
		}
	}

	if len(certificateTasks) == 0 {
		return true
	}

	var index map[string]interface{}

	_, ok := m.readWithRetry("the certificate audit index from Vault", func(ctx context.Context) (bool, error) {
		index = nil

		return m.vault.Get(ctx, certificateAuditVaultPath, &index)
	})
	if !ok {
		return false
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	for id := range index {
		if m.audited == nil {
			m.audited = make(map[string]bool)
		}

		m.audited[id] = true
	}

	for _, task := range certificateTasks {
		m.backfillTaskLocked(task)
	}

	return true
}

// backfillTaskLocked queues the records of one loaded task that are not
// in the index, in the order the events happened. The caller holds mu.
func (m *Manager) backfillTaskLocked(task *UpgradeTask) {
	if task.StartedAt != nil && !neverStarted(task) {
		rec := m.startRecordLocked(task)
		rec.At = *task.StartedAt

		m.enqueueAuditOnceLocked(rec)
	}

	for i := range task.Instances {
		inst := &task.Instances[i]

		if rec, ok := m.decisionRecordLocked(task, inst); ok {
			m.enqueueAuditOnceLocked(rec)
		}

		if !inst.Status.settled() || waitsForDecision(inst) {
			continue
		}

		rec := m.settleRecordLocked(task, inst)

		switch {
		case inst.CompletedAt != nil:
			rec.At = *inst.CompletedAt
		case task.CompletedAt != nil:
			rec.At = *task.CompletedAt
		}

		m.enqueueAuditOnceLocked(rec)
	}
}

// neverStarted reports whether the task was cancelled before any of its
// instances started, so it never got a start record.
func neverStarted(task *UpgradeTask) bool {
	if task.Status != TaskStatusCancelled {
		return false
	}

	return !slices.ContainsFunc(task.Instances, func(inst InstanceUpgrade) bool { return inst.StartedAt != nil })
}
