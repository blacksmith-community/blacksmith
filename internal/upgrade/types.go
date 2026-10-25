package upgrade

import (
	"strings"
	"time"
)

// TaskStatus represents the status of an upgrade task.
type TaskStatus string

const (
	// TaskStatusPending indicates the task is waiting to be processed.
	TaskStatusPending TaskStatus = "pending"
	// TaskStatusRunning indicates the task is currently running.
	TaskStatusRunning TaskStatus = "running"
	// TaskStatusPaused indicates the task is paused.
	TaskStatusPaused TaskStatus = "paused"
	// TaskStatusCompleted indicates the task completed successfully.
	TaskStatusCompleted TaskStatus = "completed"
	// TaskStatusFailed indicates the task failed.
	TaskStatusFailed TaskStatus = "failed"
	// TaskStatusCancelled indicates the task was cancelled.
	TaskStatusCancelled TaskStatus = "cancelled"
)

// InstanceStatus represents the status of a single instance upgrade.
type InstanceStatus string

const (
	// InstanceStatusPending indicates the instance upgrade is pending.
	InstanceStatusPending InstanceStatus = "pending"
	// InstanceStatusRunning indicates the instance upgrade is in progress.
	InstanceStatusRunning InstanceStatus = "running"
	// InstanceStatusSuccess indicates the instance upgrade succeeded.
	InstanceStatusSuccess InstanceStatus = "success"
	// InstanceStatusFailed indicates the instance upgrade failed.
	InstanceStatusFailed InstanceStatus = "failed"
	// InstanceStatusCancelled indicates the instance upgrade was cancelled.
	InstanceStatusCancelled InstanceStatus = "cancelled"
	// InstanceStatusSkipped indicates the instance upgrade was skipped
	// because the job was cancelled before it started, or because a later
	// deploy of its deployment replaced a deploy whose answer was lost.
	InstanceStatusSkipped InstanceStatus = "skipped"
	// InstanceStatusOutcomeUnknown indicates Blacksmith stopped following
	// the instance's deploy before its outcome was known. It counts as
	// finished, so the upgrade moves on, and it neither fails the upgrade
	// nor counts as a success. The operator checks the director for the
	// deploy by its context ID.
	InstanceStatusOutcomeUnknown InstanceStatus = "outcome_unknown"
	// InstanceStatusInterrupted indicates Blacksmith stopped at shutdown
	// while it waited for the instance's deploy. The deploy may still run
	// on the director, and the instance keeps its task ID or context ID so
	// it can be found after a restart.
	InstanceStatusInterrupted InstanceStatus = "interrupted"
	// StatusAwaitingDecision indicates a certificate instance waits on an
	// operator's answer before it goes on. It keeps its reservation and
	// counts as unfinished.
	StatusAwaitingDecision InstanceStatus = "awaiting_decision"
)

// settled reports whether an instance in the status has settled, which
// counted it in its upgrade's totals.
func (s InstanceStatus) settled() bool {
	switch s {
	case InstanceStatusSuccess, InstanceStatusFailed, InstanceStatusCancelled, InstanceStatusSkipped, InstanceStatusOutcomeUnknown:
		return true
	case InstanceStatusPending, InstanceStatusRunning, InstanceStatusInterrupted, StatusAwaitingDecision:
		return false
	default:
		return false
	}
}

// InstanceUpgrade represents the upgrade status for a single instance.
type InstanceUpgrade struct {
	InstanceID     string         `json:"instance_id"`
	InstanceName   string         `json:"instance_name,omitempty"`
	DeploymentName string         `json:"deployment_name"`
	ServiceID      string         `json:"service_id,omitempty"`
	PlanID         string         `json:"plan_id,omitempty"`
	Status         InstanceStatus `json:"status"`
	BOSHTaskID     int            `json:"bosh_task_id,omitempty"`
	// ContextID is the context ID the instance's deploy carried, saved
	// before the deploy is sent.
	ContextID string `json:"context_id,omitempty"`
	// UnknownSince is when the deploy's answer was lost, and is nil while
	// no answer has been lost.
	UnknownSince *time.Time `json:"unknown_since,omitempty"`
	// AppliedReleases holds the release targets the instance's deploy
	// applied, which are those its manifest held.
	AppliedReleases []ReleaseTarget `json:"applied_releases,omitempty"`
	Error           string          `json:"error,omitempty"`
	StartedAt       *time.Time      `json:"started_at,omitempty"`
	CompletedAt     *time.Time      `json:"completed_at,omitempty"`
	// Certificates is the instance's rotation record, and is set on a
	// certificate task only.
	Certificates *InstanceRotation `json:"certificates,omitempty"`
	// Command is the instance's command record, and is set on a command
	// task only.
	Command *InstanceCommand `json:"command,omitempty"`
	// ApprovedAdditional holds, for an instance that had a divergence
	// record when its upgrade was created, the variables the upgrade
	// preview showed its deploy would apply at their latest versions. It
	// is nil for every other instance, and a non-nil empty list means the
	// preview showed nothing besides the divergence. omitzero keeps that
	// difference in the saved record while leaving the field out of an
	// ordinary upgrade's record.
	ApprovedAdditional []VersionPair `json:"approved_additional,omitzero"`
}

// UpgradeTask represents a batch upgrade task.
type UpgradeTask struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
	// Kind is empty on records saved before tasks had a kind, which are
	// upgrades. KindOrDefault reads it.
	Kind           TaskKind       `json:"kind,omitempty"`
	Status         TaskStatus     `json:"status"`
	Paused         bool           `json:"paused"`
	TargetStemcell StemcellTarget `json:"target_stemcell"`
	// TargetReleases holds the releases to upgrade, and is empty when the
	// upgrade keeps each instance's releases.
	TargetReleases []ReleaseTarget   `json:"target_releases,omitempty"`
	Instances      []InstanceUpgrade `json:"instances"`
	TotalCount     int               `json:"total_count"`
	CompletedCount int               `json:"completed_count"`
	FailedCount    int               `json:"failed_count"`
	CancelledCount int               `json:"cancelled_count"`
	SkippedCount   int               `json:"skipped_count"`
	// UnknownCount is how many instances settled as outcome_unknown.
	UnknownCount int        `json:"unknown_count"`
	CreatedAt    time.Time  `json:"created_at"`
	StartedAt    *time.Time `json:"started_at,omitempty"`
	CompletedAt  *time.Time `json:"completed_at,omitempty"`
	CreatedBy    string     `json:"created_by,omitempty"`
	// Certificates is what a certificate task was asked to do, and is nil
	// on an upgrade.
	Certificates *CertificateTaskSpec `json:"certificates,omitempty"`
	// Command is what a command task was asked to do, and is nil on every
	// other kind.
	Command *CommandTaskSpec `json:"command,omitempty"`
	// ClientRequestID is the ID the client sent with the create request,
	// so a retried request finds the task it already made.
	ClientRequestID string `json:"client_request_id,omitempty"`
	// RequestDigest is the SHA-256 of the create request's body without
	// its client request ID.
	RequestDigest string `json:"request_digest,omitempty"`
	// CreatedFrom is the address the create request came from, as
	// "<remote>" or "<remote> via <forwarded-for>". The forwarded-for part
	// is what the client reported, so it is for the record only.
	CreatedFrom string `json:"created_from,omitempty"`
	// Revision counts the task's confirmed saves.
	Revision uint64 `json:"revision,omitempty"`
	// CreationConfirmed is true on a certificate task once a save that
	// carries it is confirmed, which happens only after the task's first
	// save was confirmed. The loader fails a certificate task without it,
	// since its create never finished.
	CreationConfirmed bool `json:"creation_confirmed,omitempty"`
}

// StemcellTarget represents the target stemcell for an upgrade.
type StemcellTarget struct {
	OS      string `json:"os"`
	Version string `json:"version"`
}

// IsSet reports whether the target names a stemcell. An upgrade of
// releases alone leaves its stemcell target empty.
func (s StemcellTarget) IsSet() bool {
	return s.OS != "" && s.Version != ""
}

// ReleaseTarget names a BOSH release and the version to upgrade it to. The
// version must already be on the director.
type ReleaseTarget struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// CreateTaskRequest represents a request to create an upgrade task. It
// sets a stemcell target, release targets, or both. An empty stemcell
// target keeps each instance's stemcell, and no release targets keep each
// instance's releases.
type CreateTaskRequest struct {
	// Kind is empty or "upgrade" for an upgrade. A certificate task is
	// created through CreateCertificateTask instead.
	Kind           TaskKind        `json:"kind,omitempty"`
	Name           string          `json:"name,omitempty"`
	InstanceIDs    []string        `json:"instance_ids"`
	TargetStemcell StemcellTarget  `json:"target_stemcell"`
	TargetReleases []ReleaseTarget `json:"target_releases,omitempty"`
	// UpgradePreviewID names the upgrade preview the operator confirmed.
	// An upgrade that includes an instance with a divergence record needs
	// one, and any other upgrade ignores it.
	UpgradePreviewID string `json:"upgrade_preview_id,omitempty"`
}

// DescribeTargets names the request's targets for the log. A request that
// keeps each instance's stemcell names no stemcell.
func (r CreateTaskRequest) DescribeTargets() string {
	parts := make([]string, 0, 1+len(r.TargetReleases))

	if r.TargetStemcell.OS != "" || r.TargetStemcell.Version != "" {
		parts = append(parts, "stemcell "+r.TargetStemcell.OS+"/"+r.TargetStemcell.Version)
	}

	for _, target := range r.TargetReleases {
		parts = append(parts, "release "+target.Name+"/"+target.Version)
	}

	if len(parts) == 0 {
		return "no target"
	}

	return strings.Join(parts, ", ")
}

// TaskSummary represents a summary of an upgrade task for list views.
type TaskSummary struct {
	ID     string     `json:"id"`
	Name   string     `json:"name,omitempty"`
	Kind   TaskKind   `json:"kind"`
	Status TaskStatus `json:"status"`
	// Target describes what the task does, such as the stemcell and
	// releases of an upgrade or "Patch, 2 certificates".
	Target         string   `json:"target"`
	TargetStemcell string   `json:"target_stemcell"`
	TargetReleases []string `json:"target_releases,omitempty"`
	TotalCount     int      `json:"total_count"`
	CompletedCount int      `json:"completed_count"`
	FailedCount    int      `json:"failed_count"`
	UnknownCount   int      `json:"unknown_count"`
	SkippedCount   int      `json:"skipped_count"`
	CancelledCount int      `json:"cancelled_count"`
	// AwaitingDecisionCount is how many of the task's instances wait on an
	// operator decision. A cancelled task still counts them, because its
	// decisions can be resolved.
	AwaitingDecisionCount int       `json:"awaiting_decision_count"`
	CreatedAt             time.Time `json:"created_at"`
	// Command holds a command task's settings, without the command text.
	Command *CommandSummary `json:"command,omitempty"`
}

// awaitingDecisionCount counts the instances with a pending decision.
func (t *UpgradeTask) awaitingDecisionCount() int {
	count := 0

	for index := range t.Instances {
		rotation := t.Instances[index].Certificates
		if rotation != nil && rotation.Pending != nil {
			count++
		}
	}

	return count
}

// ToSummary converts an UpgradeTask to a TaskSummary. Each target reads
// as name/version, and an upgrade of releases alone has no stemcell.
func (t *UpgradeTask) ToSummary() TaskSummary {
	summary := TaskSummary{
		ID:             t.ID,
		Name:           t.Name,
		Kind:           t.KindOrDefault(),
		Status:         t.Status,
		TotalCount:     t.TotalCount,
		CompletedCount: t.CompletedCount,
		FailedCount:    t.FailedCount,
		UnknownCount:   t.UnknownCount,
		SkippedCount:   t.SkippedCount,
		CancelledCount: t.CancelledCount,
		CreatedAt:      t.CreatedAt,

		AwaitingDecisionCount: t.awaitingDecisionCount(),
	}

	if summary.Kind == TaskKindCertificates {
		summary.Target = certificateTarget(t)

		return summary
	}

	if summary.Kind == TaskKindCommand {
		summary.Target = commandTarget(t)
		summary.Command = commandSummary(t.Command)

		return summary
	}

	stemcell := ""
	if t.TargetStemcell.IsSet() {
		stemcell = t.TargetStemcell.OS + "/" + t.TargetStemcell.Version
	}

	releases := make([]string, 0, len(t.TargetReleases))
	for _, target := range t.TargetReleases {
		releases = append(releases, target.Name+"/"+target.Version)
	}

	summary.TargetStemcell = stemcell
	summary.TargetReleases = releases

	parts := releases
	if stemcell != "" {
		parts = append([]string{stemcell}, releases...)
	}

	summary.Target = strings.Join(parts, ", ")

	return summary
}

// Settings represents upgrade manager settings.
type Settings struct {
	MaxBatchJobs int `json:"max_batch_jobs"` // 0 means the configured default
}

// SettingsResponse represents the settings API response.
type SettingsResponse struct {
	MaxBatchJobs int `json:"max_batch_jobs"`
}

// UpdateSettingsRequest represents a request to update settings.
type UpdateSettingsRequest struct {
	MaxBatchJobs int `json:"max_batch_jobs"`
}
