package upgrade

import (
	"encoding/json"
	"fmt"
	"strconv"
)

// commandAuditVaultPath is the index of the command audit records, and each
// record is saved under it by its ID.
const commandAuditVaultPath = "command-audit"

// The events a command audit record names, besides the task_started and
// instance_settled events a certificate task has too.
const (
	AuditNodeStaged    = "node_staged"
	AuditNodeSettled   = "node_settled"
	AuditTaskFinished  = "task_finished"
	AuditTaskCancelled = "task_cancelled"
)

// CommandAudit is what a command audit record says about the task's
// command. It is the one place the command text is kept outside the task
// record, and it is never written to a log.
type CommandAudit struct {
	Text                string `json:"text,omitempty"`
	Digest              string `json:"digest"`
	Length              int    `json:"length"`
	RunAs               string `json:"run_as"`
	Shell               string `json:"shell,omitempty"`
	TimeoutSeconds      int    `json:"timeout_seconds"`
	Parallel            bool   `json:"parallel"`
	MaxInFlight         int    `json:"max_in_flight,omitempty"`
	PreviewID           string `json:"preview_id,omitempty"`
	CredentialPresented bool   `json:"credential_presented"`
	// Targets lists the deployments and node tuples the task was made
	// for, and every record of the task carries it.
	Targets []CommandAuditTarget `json:"targets,omitempty"`
}

// CommandAuditTarget is one deployment of the task and the nodes it was
// reviewed with.
type CommandAuditTarget struct {
	Deployment string              `json:"deployment"`
	InstanceID string              `json:"instance_id"`
	Nodes      []CommandVMIdentity `json:"nodes"`
}

// CommandNodeAudit is what a node event says about the node. It holds
// counts and flags and never a byte of output.
type CommandNodeAudit struct {
	Reviewed        CommandVMIdentity `json:"reviewed"`
	Matched         CommandVMIdentity `json:"matched"`
	Phase           CommandPhase      `json:"phase"`
	Status          string            `json:"status,omitempty"`
	ExitCode        *int              `json:"exit_code,omitempty"`
	DurationMillis  int64             `json:"duration_ms,omitempty"`
	StdoutKept      int               `json:"stdout_kept,omitempty"`
	StdoutReceived  int               `json:"stdout_received,omitempty"`
	StderrKept      int               `json:"stderr_kept,omitempty"`
	StderrReceived  int               `json:"stderr_received,omitempty"`
	StdoutTruncated bool              `json:"stdout_truncated,omitempty"`
	StderrTruncated bool              `json:"stderr_truncated,omitempty"`
	Killed          bool              `json:"killed,omitempty"`
	Survived        bool              `json:"survived,omitempty"`
	// Reason says why the node was skipped or stopped, and Error what
	// failed. Both are the broker's own words and are cleaned the way the
	// record's other free text is.
	Reason string `json:"reason,omitempty"`
	Error  string `json:"error,omitempty"`
}

// auditVaultPath returns the index a record is saved under.
func auditVaultPath(rec AuditRecord) string {
	if rec.Command != nil {
		return commandAuditVaultPath
	}

	return certificateAuditVaultPath
}

// auditLabel names the audit trail a record belongs to in the log.
func auditLabel(rec AuditRecord) string {
	return auditPathLabel(auditVaultPath(rec))
}

// auditPathLabel names the audit trail an index holds in the log.
func auditPathLabel(path string) string {
	if path == commandAuditVaultPath {
		return "command-run"
	}

	return "certificate-rotation"
}

// logCommandAudit writes the structured log line of a command record. The
// line holds the digest and the length of the command and never its text,
// and a command record holds no output, so the log answers who ran what
// digest where and Vault answers what the text was.
func (m *Manager) logCommandAudit(rec AuditRecord) {
	redacted := rec

	command := *rec.Command
	command.Text = ""
	redacted.Command = &command

	encoded := "{}"

	if raw, err := json.Marshal(redacted); err == nil {
		encoded = string(raw)
	}

	node := ""
	if rec.Node != nil {
		identity := rec.Node.Reviewed
		node = identity.Group + "/" + strconv.Itoa(identity.Index)
	}

	m.logger.Info("audit=command-run id=%s event=%s task=%s instance=%s node=%s actor=%q from=%q credential=%t digest=%s length=%d outcome=%q record=%s",
		rec.ID, rec.Event, rec.TaskID, rec.InstanceID, node, rec.Actor, rec.From, command.CredentialPresented, command.Digest, command.Length, rec.Outcome, encoded)
}

// commandNodeKey names a node within its task by the VM the operator
// reviewed.
func commandNodeKey(vm *CommandVM) string {
	return vm.Reviewed.Group + "/" + strconv.Itoa(vm.Reviewed.Index) + "/" + vm.Reviewed.ID
}

// commandRecordLocked builds a command record of the task, or of one of its
// instances, from what the task holds now, with the targets of the task. The
// caller holds mu.
func (m *Manager) commandRecordLocked(task *UpgradeTask, inst *InstanceUpgrade, event string) AuditRecord {
	rec := AuditRecord{
		ID:      auditID(task.ID, "", event, ""),
		At:      m.certNowLocked(),
		Event:   event,
		TaskID:  task.ID,
		Actor:   task.CreatedBy,
		From:    task.CreatedFrom,
		Command: &CommandAudit{},
	}

	if spec := task.Command; spec != nil {
		rec.Reason = spec.Reason
		rec.Command = &CommandAudit{
			Text:                spec.Command,
			Digest:              spec.CommandDigest,
			Length:              spec.CommandLength,
			RunAs:               spec.RunAs,
			Shell:               spec.Shell,
			TimeoutSeconds:      spec.TimeoutSeconds,
			Parallel:            spec.Parallel,
			MaxInFlight:         spec.MaxInFlight,
			PreviewID:           spec.PreviewID,
			CredentialPresented: spec.CredentialPresented,
		}
	}

	rec.Command.Targets = commandTargetsLocked(task)

	if inst != nil {
		rec.InstanceID = inst.InstanceID
		rec.ID = auditID(task.ID, inst.InstanceID, event, "")
	}

	return rec
}

// commandTargetsLocked lists the deployments of the task with the nodes
// each was reviewed with. The caller holds mu.
func commandTargetsLocked(task *UpgradeTask) []CommandAuditTarget {
	var targets []CommandAuditTarget

	for i := range task.Instances {
		inst := &task.Instances[i]
		if inst.Command == nil {
			continue
		}

		target := CommandAuditTarget{Deployment: inst.Command.Deployment, InstanceID: inst.InstanceID}
		for _, vm := range inst.Command.VMs {
			target.Nodes = append(target.Nodes, vm.Reviewed.clone())
		}

		targets = append(targets, target)
	}

	return targets
}

// commandStartRecordLocked builds the task_started record. The caller holds
// mu.
func (m *Manager) commandStartRecordLocked(task *UpgradeTask) AuditRecord {
	rec := m.commandRecordLocked(task, nil, AuditTaskStarted)
	rec.Outcome = "started"
	rec.Detail = fmt.Sprintf("%d instances", len(task.Instances))
	return rec
}

// commandEndRecordLocked builds the record that closes the task, which is
// task_cancelled for a cancelled task and task_finished for one that ran to
// its end, or reports false for a task that is neither. A task that was
// never started, as one failed at load is, finished nothing. The caller
// holds mu.
func (m *Manager) commandEndRecordLocked(task *UpgradeTask) (AuditRecord, bool) {
	var event string

	switch {
	case task.Status == TaskStatusCancelled:
		event = AuditTaskCancelled
	case (task.Status == TaskStatusCompleted || task.Status == TaskStatusFailed) && task.StartedAt != nil:
		event = AuditTaskFinished
	default:
		return AuditRecord{}, false
	}

	rec := m.commandRecordLocked(task, nil, event)
	rec.Outcome = string(task.Status)
	rec.Detail = fmt.Sprintf("%d of %d instances succeeded, %d failed, %d cancelled", task.CompletedCount, task.TotalCount, task.FailedCount, task.CancelledCount)
	if task.CompletedAt != nil {
		rec.At = *task.CompletedAt
	}

	return rec, true
}

// commandInstanceRecordLocked builds the instance_settled record of a
// settled instance. The caller holds mu.
func (m *Manager) commandInstanceRecordLocked(task *UpgradeTask, inst *InstanceUpgrade) AuditRecord {
	rec := m.commandRecordLocked(task, inst, AuditInstanceSettled)
	rec.Outcome = string(inst.Status)
	rec.ID = auditID(task.ID, inst.InstanceID, AuditInstanceSettled, rec.Outcome)

	if inst.Command != nil {
		rec.Detail = fmt.Sprintf("%d nodes", len(inst.Command.VMs))
	}

	if inst.CompletedAt != nil {
		rec.At = *inst.CompletedAt
	}

	return rec
}

// commandNodeRecordLocked builds the record of an event on one node. The
// caller holds mu.
func (m *Manager) commandNodeRecordLocked(task *UpgradeTask, inst *InstanceUpgrade, vm int, event string) AuditRecord {
	node := &inst.Command.VMs[vm]

	rec := m.commandRecordLocked(task, inst, event)
	rec.ID = auditID(task.ID, inst.InstanceID, event, commandNodeKey(node))
	rec.Outcome = node.Status
	rec.Node = &CommandNodeAudit{
		Reviewed:        node.Reviewed.clone(),
		Matched:         node.Matched.clone(),
		Phase:           node.Phase,
		Status:          node.Status,
		StdoutKept:      node.StdoutKept,
		StdoutReceived:  node.StdoutReceived,
		StderrKept:      node.StderrKept,
		StderrReceived:  node.StderrReceived,
		StdoutTruncated: node.StdoutTruncated,
		StderrTruncated: node.StderrTruncated,
		Killed:          node.Killed,
		Survived:        node.Survived,
		Reason:          auditText(node.Reason),
		Error:           auditText(node.Error),
	}

	if node.ExitCode != nil {
		code := *node.ExitCode
		rec.Node.ExitCode = &code
	}

	if node.StartedAt != nil && node.EndedAt != nil {
		rec.Node.DurationMillis = node.EndedAt.Sub(*node.StartedAt).Milliseconds()
	}

	return rec
}

// auditCommandNodeLocked queues the record of the node's latest confirmed
// move, which is node_staged for a staged node and node_settled for a
// settled one. Every move that settles a node reaches it, including the
// cleanup that settles a stranded node without sending the command again.
// A node that is in neither phase has nothing to record. The caller holds
// mu.
func (m *Manager) auditCommandNodeLocked(task *UpgradeTask, inst *InstanceUpgrade, vm int) {
	switch inst.Command.VMs[vm].Phase {
	case CommandPhaseStaged:
		m.enqueueAuditOnceLocked(m.commandNodeRecordLocked(task, inst, vm, AuditNodeStaged))
	case CommandPhaseSettled:
		m.enqueueAuditOnceLocked(m.commandNodeRecordLocked(task, inst, vm, AuditNodeSettled))
	case CommandPhasePending, CommandPhaseSent, CommandPhaseSkipped:
	}
}

// auditCommandLocked queues the records of the task as a whole, and of each
// instance that settled, that have none yet. The caller holds mu.
func (m *Manager) auditCommandLocked(task *UpgradeTask) {
	for i := range task.Instances {
		inst := &task.Instances[i]
		if inst.Command == nil || !inst.Status.settled() {
			continue
		}

		m.enqueueAuditOnceLocked(m.commandInstanceRecordLocked(task, inst))
	}

	if rec, ok := m.commandEndRecordLocked(task); ok {
		m.enqueueAuditOnceLocked(rec)
	}
}

// backfillCommandLocked queues the records of one loaded command task that
// are not in the index, in the order the events happened, so a crash
// between a save and its audit write loses nothing. Each ID comes from what
// the record says, so a record that landed is never written again. A node
// that settled without ever being staged has no start, and the node_staged
// of a settled node is rebuilt only when the node was sent. The caller holds
// mu.
func (m *Manager) backfillCommandLocked(task *UpgradeTask) {
	if task.StartedAt != nil && !neverStarted(task) {
		rec := m.commandStartRecordLocked(task)
		rec.At = *task.StartedAt
		rec.Rebuilt = true

		m.enqueueAuditOnceLocked(rec)
	}

	for i := range task.Instances {
		inst := &task.Instances[i]
		if inst.Command == nil {
			continue
		}

		for vm := range inst.Command.VMs {
			m.backfillCommandNodeLocked(task, inst, vm)
		}

		if inst.Status.settled() {
			rec := m.commandInstanceRecordLocked(task, inst)
			rec.Rebuilt = true

			m.enqueueAuditOnceLocked(rec)
		}
	}

	if rec, ok := m.commandEndRecordLocked(task); ok {
		rec.Rebuilt = true

		m.enqueueAuditOnceLocked(rec)
	}
}

// backfillCommandNodeLocked queues the lost records of one node.
func (m *Manager) backfillCommandNodeLocked(task *UpgradeTask, inst *InstanceUpgrade, vm int) {
	node := &inst.Command.VMs[vm]

	if node.StagedAt != nil {
		rec := m.commandNodeRecordLocked(task, inst, vm, AuditNodeStaged)
		rec.Rebuilt = true
		rec.At = *node.StagedAt

		rec.Node.Phase = CommandPhaseStaged
		rec.Node.Status = ""
		rec.Outcome = ""

		m.enqueueAuditOnceLocked(rec)
	}

	if node.Phase == CommandPhaseSettled {
		rec := m.commandNodeRecordLocked(task, inst, vm, AuditNodeSettled)
		rec.Rebuilt = true

		if node.EndedAt != nil {
			rec.At = *node.EndedAt
		} else if task.CompletedAt != nil {
			rec.At = *task.CompletedAt
		}

		m.enqueueAuditOnceLocked(rec)
	}
}
