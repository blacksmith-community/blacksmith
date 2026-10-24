package upgrade

import (
	"bytes"
	"strconv"
	"time"
)

// TaskKind says what a task does. The empty value is an upgrade, because
// records saved before tasks had a kind are all upgrades.
type TaskKind string

// The kinds of task the manager runs.
const (
	// TaskKindUpgrade upgrades stemcells, releases, or both.
	TaskKindUpgrade TaskKind = "upgrade"
	// TaskKindCertificates rotates certificates.
	TaskKindCertificates TaskKind = "certificates"
)

// KindOrDefault returns the task's kind, reading an empty kind as an
// upgrade.
func (t *UpgradeTask) KindOrDefault() TaskKind {
	if t.Kind == "" {
		return TaskKindUpgrade
	}

	return t.Kind
}

// RotationMode says how a certificate task applies the new certificates.
type RotationMode string

// The rotation modes.
const (
	// RotationModeRedeploy regenerates the certificates and redeploys.
	RotationModeRedeploy RotationMode = "redeploy"
	// RotationModePatch regenerates the certificates and writes them onto
	// the VMs in place.
	RotationModePatch RotationMode = "patch"
	// RotationModeConverge redeploys so the VMs pick up versions that
	// already exist in CredHub.
	RotationModeConverge RotationMode = "converge"
)

// ConvergePolicy says when a patch task redeploys to bring BOSH in line
// with the patched VMs. Patch mode requires one, and it has no default.
type ConvergePolicy string

// The converge policies.
const (
	// ConvergePending leaves the redeploy to a later task or deploy.
	ConvergePending ConvergePolicy = "pending"
	// ConvergeImmediate redeploys as soon as the VMs are patched.
	ConvergeImmediate ConvergePolicy = "immediate"
)

// RotationPhase is the step a certificate instance has reached.
type RotationPhase string

// The rotation phases, in the order an instance passes through them.
const (
	RotationPhasePreflight  RotationPhase = "preflight"
	RotationPhaseDiscover   RotationPhase = "discover"
	RotationPhaseRegenerate RotationPhase = "regenerate"
	RotationPhaseDeploy     RotationPhase = "deploy"
	RotationPhasePatch      RotationPhase = "patch"
	RotationPhaseConverge   RotationPhase = "converge"
	RotationPhaseDone       RotationPhase = "done"
)

// The states of a variable's regeneration request.
const (
	// RegenerateNotSent means the request has not been sent.
	RegenerateNotSent = "not_sent"
	// RegenerateSending is saved before the request is sent, so a restart
	// knows the request may have reached CredHub.
	RegenerateSending = "sending"
	// RegenerateConfirmed means CredHub answered with the new version.
	RegenerateConfirmed = "confirmed"
	// RegenerateOutcomeUnknown means the request may or may not have made a
	// new version, and an operator decides which.
	RegenerateOutcomeUnknown = "outcome_unknown"
	// RegenerateAdopted means an operator adopted a version found after an
	// uncertain request.
	RegenerateAdopted = "adopted"
	// RegenerateAbandoned means an operator chose not to use any version
	// found after an uncertain request.
	RegenerateAbandoned = "abandoned"
)

// The kinds of decision a certificate instance can wait for.
const (
	// DecisionReconcileRegeneration asks which version an uncertain
	// regeneration made, if any.
	DecisionReconcileRegeneration = "reconcile_regeneration"
	// DecisionApproveChangedEffects asks for approval of effects that
	// changed since the preview.
	DecisionApproveChangedEffects = "approve_changed_effects"
	// DecisionResolveDeployOutcome asks what became of a deploy whose BOSH
	// outcome Blacksmith could not learn.
	DecisionResolveDeployOutcome = "resolve_deploy_outcome"
)

// CertificateTaskSpec is what a certificate task was asked to do.
type CertificateTaskSpec struct {
	Mode      RotationMode   `json:"mode"`
	Converge  ConvergePolicy `json:"converge,omitempty"`
	Reason    string         `json:"reason,omitempty"`
	PreviewID string         `json:"preview_id"`
}

// InstanceRotation is the record of one instance's rotation. It holds
// hashes, serials, version IDs, and check results, and never certificate
// or key content.
type InstanceRotation struct {
	Phase     RotationPhase      `json:"phase"`
	Approved  ApprovedEffects    `json:"approved"`
	Variables []VariableRotation `json:"variables"`
	VMs       []VMJournal        `json:"vms,omitempty"`
	// Gate names the health gate: "redis-cluster", "valkey-cluster",
	// "rabbitmq-cluster", or "single-vm".
	Gate     string          `json:"gate,omitempty"`
	Converge *ConvergeDeploy `json:"converge,omitempty"`
	// Deploy is the BOSH outcome of the instance's latest deploy or
	// converge deploy, once its task reached a final state.
	Deploy  *DeployOutcome   `json:"deploy_outcome,omitempty"`
	Pending *PendingDecision `json:"pending_decision,omitempty"`
	// DecisionRequestID is the client request ID of the latest operator
	// decision applied to the instance, and DecisionDigest is the digest
	// of its body, so a repeat of it applies once.
	DecisionRequestID string `json:"decision_request_id,omitempty"`
	DecisionDigest    string `json:"decision_digest,omitempty"`
	// Decision is what the audit record of that decision says, saved with
	// the decision itself so a record lost to a crash is rebuilt with the
	// operator who decided.
	Decision *DecisionAudit `json:"decision_audit,omitempty"`
}

// DecisionAudit is the part of a decision's audit record that only the
// request knows. It holds the event, the operator's choice, who decided and
// from where, and when, and never a certificate or key.
type DecisionAudit struct {
	// ID is minted when the decision is applied, and the decision's audit
	// record is keyed on it, so every decision has a record of its own
	// whether or not the client sent a request ID.
	ID      string    `json:"id,omitempty"`
	Event   string    `json:"event"`
	Detail  string    `json:"detail,omitempty"`
	Actor   string    `json:"actor,omitempty"`
	From    string    `json:"from,omitempty"`
	Outcome string    `json:"outcome,omitempty"`
	At      time.Time `json:"at"`
}

// DeployOutcome is how the BOSH task of a rotation's deploy ended. A done
// task does not finish the instance, which succeeds only once the
// deployed variable versions and the listeners show the new
// certificates.
type DeployOutcome struct {
	BOSHTaskID int `json:"bosh_task_id"`
	// State is the task's final state on the director, such as done,
	// error, timeout, or cancelled.
	State string `json:"state"`
	// Result is the director's result text for the task.
	Result     string    `json:"result,omitempty"`
	FinishedAt time.Time `json:"finished_at"`
}

// ApprovedEffects is what the operator approved for an instance in the
// preview, which the task checks again before it changes anything.
type ApprovedEffects struct {
	// ManifestDigest is the SHA-256 of the director's manifest text.
	ManifestDigest string        `json:"manifest_digest"`
	Selected       []VersionPair `json:"selected"`
	// Additional lists the variables whose latest version differs from the
	// deployed one, which a deploy also applies.
	Additional []VersionPair `json:"additional"`
	VMCIDs     []string      `json:"vm_cids"`
}

// VersionPair names a variable with its deployed and latest versions.
type VersionPair struct {
	Name              string `json:"name"`
	DeployedVersionID string `json:"deployed_version_id"`
	LatestVersionID   string `json:"latest_version_id"`
}

// VariableRotation is the record of one variable's rotation on an
// instance.
type VariableRotation struct {
	Name          string `json:"name"`
	CertificateID string `json:"certificate_id"`
	// Baseline holds every version that existed before the request.
	Baseline     []string  `json:"baseline_version_ids"`
	OldVersionID string    `json:"old_version_id"`
	OldSerial    string    `json:"old_serial"`
	OldNotAfter  time.Time `json:"old_not_after"`
	OldCertHash  string    `json:"old_cert_sha256"`
	// OldKeyHash is recorded in patch mode only.
	OldKeyHash string `json:"old_key_sha256,omitempty"`
	// RegenerateState is one of the Regenerate constants.
	RegenerateState string     `json:"regenerate_state"`
	NewVersionID    string     `json:"new_version_id,omitempty"`
	NewSerial       string     `json:"new_serial,omitempty"`
	NewNotAfter     *time.Time `json:"new_not_after,omitempty"`
	NewCertHash     string     `json:"new_cert_sha256,omitempty"`
	NewKeyHash      string     `json:"new_key_sha256,omitempty"`
	Validation      []Check    `json:"validation,omitempty"`
}

// PendingDecision is a question an instance waits on an operator to
// answer.
type PendingDecision struct {
	// Kind is DecisionReconcileRegeneration,
	// DecisionApproveChangedEffects, or DecisionResolveDeployOutcome.
	Kind       string      `json:"kind"`
	Variable   string      `json:"variable,omitempty"`
	Candidates []Candidate `json:"candidates,omitempty"`
	// CandidatesCheckedAt is when Candidates were last listed from
	// CredHub. It is nil for a decision whose candidates were never
	// listed, as one loaded after a crash, so a reader can tell "not
	// checked yet" from "CredHub holds none".
	CandidatesCheckedAt *time.Time `json:"candidates_checked_at,omitempty"`
	// Changes holds a readable sentence for each effect that differs.
	Changes []string  `json:"changes,omitempty"`
	Since   time.Time `json:"since"`
}

// Check is the result of one check on a certificate.
type Check struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail,omitempty"`
}

// Candidate is a version that an uncertain regeneration may have made.
type Candidate struct {
	VersionID string    `json:"version_id"`
	CreatedAt time.Time `json:"created_at"`
	Serial    string    `json:"serial"`
	NotAfter  time.Time `json:"not_after"`
	Checks    []Check   `json:"checks,omitempty"`
	Valid     bool      `json:"valid"`
}

// ConvergeDeploy records the deploy that brings BOSH in line with the
// rotated certificates.
type ConvergeDeploy struct {
	ContextID  string `json:"context_id"`
	BOSHTaskID int    `json:"bosh_task_id,omitempty"`
}

// VMPhase is the step one VM has reached in a patch.
type VMPhase string

// The VM phases. Done, rolled back, unrecovered, and reverted are closed,
// and every other phase is open.
const (
	VMPhasePlanned     VMPhase = "planned"
	VMPhaseStaged      VMPhase = "staged"
	VMPhaseBackedUp    VMPhase = "backed_up"
	VMPhaseSwapping    VMPhase = "swapping"
	VMPhaseSwapped     VMPhase = "swapped"
	VMPhaseRestarted   VMPhase = "restarted"
	VMPhaseVerified    VMPhase = "verified"
	VMPhaseDone        VMPhase = "done"
	VMPhaseRollingBack VMPhase = "rolling_back"
	VMPhaseRolledBack  VMPhase = "rolled_back"
	VMPhaseUnrecovered VMPhase = "unrecovered"
	VMPhaseReverted    VMPhase = "reverted"
)

// The states of one file in a VM's journal.
const (
	FileStatePlanned   = "planned"
	FileStateBackedUp  = "backed_up"
	FileStateReplacing = "replacing"
	FileStateReplaced  = "replaced"
	FileStateRestoring = "restoring"
	FileStateRestored  = "restored"
)

// The roles of a journaled file.
const (
	FileRoleCertificate = "certificate"
	FileRoleKey         = "key"
)

// The states of a VM's cleanup of its staging and backup files.
const (
	CleanupPending = "pending"
	CleanupDone    = "done"
	CleanupFailed  = "failed"
)

// VMJournal is the progress of a patch on one VM, saved before and after
// every change the patch makes to it.
type VMJournal struct {
	// VM is the VM's group/id.
	VM    string `json:"vm"`
	CID   string `json:"cid"`
	Group string `json:"group"`
	Index int    `json:"index"`
	// JobDirs maps each job name to its resolved directory.
	JobDirs   map[string]string `json:"job_dirs"`
	Phase     VMPhase           `json:"phase"`
	Files     []FileEntry       `json:"files"`
	Processes []string          `json:"processes"`
	Targets   []TargetResult    `json:"targets"`
	GateAfter string            `json:"gate_after,omitempty"`
	// Cleanup is one of the Cleanup constants.
	Cleanup string `json:"cleanup,omitempty"`
	Error   string `json:"error,omitempty"`
}

// FileEntry is one certificate or key file a patch replaces on a VM.
type FileEntry struct {
	// Path is the resolved path under /var/vcap/data/jobs.
	Path string `json:"path"`
	Job  string `json:"job"`
	// Role is FileRoleCertificate or FileRoleKey.
	Role     string `json:"role"`
	Variable string `json:"variable"`
	Owner    string `json:"owner"`
	Mode     string `json:"mode"`
	OldHash  string `json:"old_sha256"`
	NewHash  string `json:"new_sha256"`
	// OldRawHash is the SHA-256 of the target's bytes as discovery read
	// them, which its backup must keep too, and NewRawHash the SHA-256 of
	// the exact bytes that replace them: Lead, the new PEM, and Tail. The
	// normalised hashes find the files, and when a record has both raw
	// hashes, the patch compares raw bytes everywhere else. A record from
	// before raw hashes existed has neither and is patched by its
	// normalised hashes.
	OldRawHash string `json:"old_raw_sha256,omitempty"`
	NewRawHash string `json:"new_raw_sha256,omitempty"`
	// Lead is the blank lines before the target's PEM and Tail the
	// whitespace after it, which the replacement keeps.
	Lead string `json:"lead,omitempty"`
	Tail string `json:"tail,omitempty"`
	// BackupPath is /var/vcap/data/certpatch/<task id>/b/<n>.
	BackupPath string `json:"backup_path"`
	// State is one of the FileState constants.
	State string `json:"state"`
}

// rawHashes reports whether the record holds both raw hashes, so the
// patch compares the file's raw bytes rather than its normalised hash.
func (f FileEntry) rawHashes() bool {
	return f.OldRawHash != "" && f.NewRawHash != ""
}

// replacement returns the bytes that replace the file, from the new PEM
// content. A record with raw hashes gets the target's Lead and Tail
// around the PEM with its own leading newlines and trailing whitespace
// removed, so the file keeps the layout BOSH renders, and its
// normalised hash is still the PEM's. A record without them gets the PEM
// as it is. The result is always a new buffer, because content can be a
// private key, and the caller clears it.
func (f FileEntry) replacement(content []byte) []byte {
	if f.OldRawHash == "" {
		return bytes.Clone(content)
	}

	pem := bytes.TrimRight(bytes.TrimLeft(content, "\r\n"), " \t\r\n")
	out := make([]byte, 0, len(f.Lead)+len(pem)+len(f.Tail))
	out = append(out, f.Lead...)
	out = append(out, pem...)

	return append(out, f.Tail...)
}

// TargetResult is the check of one listener after a patch.
type TargetResult struct {
	Variable string `json:"variable,omitempty"`
	Port     int    `json:"port,omitempty"`
	// Method is "probe", "openssl", or "none" for a consumer with no
	// listener.
	Method string `json:"method"`
	// Fingerprint is the SHA-256 fingerprint of the leaf the listener
	// served.
	Fingerprint string `json:"fingerprint,omitempty"`
	OK          bool   `json:"ok"`
	Detail      string `json:"detail,omitempty"`
	// ServerName is the name the checks send for SNI. The caller sets it
	// from the validated new certificate before each verification, and it
	// is never saved, so the journal holds no certificate content.
	ServerName string `json:"-"`
}

// certificateTarget describes a certificate task for the task list, such
// as "Patch, 2 certificates", from its mode and the number of variables
// selected across its instances.
func certificateTarget(task *UpgradeTask) string {
	mode := "Certificates"

	if task.Certificates != nil {
		switch task.Certificates.Mode {
		case RotationModeRedeploy:
			mode = "Redeploy"
		case RotationModePatch:
			mode = "Patch"
		case RotationModeConverge:
			mode = "Converge"
		}
	}

	selected := 0

	for i := range task.Instances {
		if rotation := task.Instances[i].Certificates; rotation != nil {
			selected += len(rotation.Approved.Selected)
		}
	}

	noun := "certificates"
	if selected == 1 {
		noun = "certificate"
	}

	return mode + ", " + strconv.Itoa(selected) + " " + noun
}
