package upgrade

import (
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
)

// settled reports whether an instance in the status has settled, which
// counted it in its upgrade's totals.
func (s InstanceStatus) settled() bool {
	switch s {
	case InstanceStatusSuccess, InstanceStatusFailed, InstanceStatusCancelled, InstanceStatusSkipped, InstanceStatusOutcomeUnknown:
		return true
	case InstanceStatusPending, InstanceStatusRunning, InstanceStatusInterrupted:
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
	Error        string     `json:"error,omitempty"`
	StartedAt    *time.Time `json:"started_at,omitempty"`
	CompletedAt  *time.Time `json:"completed_at,omitempty"`
}

// UpgradeTask represents a batch upgrade task.
type UpgradeTask struct {
	ID             string            `json:"id"`
	Name           string            `json:"name,omitempty"`
	Status         TaskStatus        `json:"status"`
	Paused         bool              `json:"paused"`
	TargetStemcell StemcellTarget    `json:"target_stemcell"`
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
}

// StemcellTarget represents the target stemcell for an upgrade.
type StemcellTarget struct {
	OS      string `json:"os"`
	Version string `json:"version"`
}

// CreateTaskRequest represents a request to create an upgrade task.
type CreateTaskRequest struct {
	Name           string         `json:"name,omitempty"`
	InstanceIDs    []string       `json:"instance_ids"`
	TargetStemcell StemcellTarget `json:"target_stemcell"`
}

// TaskSummary represents a summary of an upgrade task for list views.
type TaskSummary struct {
	ID             string     `json:"id"`
	Name           string     `json:"name,omitempty"`
	Status         TaskStatus `json:"status"`
	TargetStemcell string     `json:"target_stemcell"`
	TotalCount     int        `json:"total_count"`
	CompletedCount int        `json:"completed_count"`
	FailedCount    int        `json:"failed_count"`
	UnknownCount   int        `json:"unknown_count"`
	CreatedAt      time.Time  `json:"created_at"`
}

// ToSummary converts an UpgradeTask to a TaskSummary.
func (t *UpgradeTask) ToSummary() TaskSummary {
	return TaskSummary{
		ID:             t.ID,
		Name:           t.Name,
		Status:         t.Status,
		TargetStemcell: t.TargetStemcell.OS + "/" + t.TargetStemcell.Version,
		TotalCount:     t.TotalCount,
		CompletedCount: t.CompletedCount,
		FailedCount:    t.FailedCount,
		UnknownCount:   t.UnknownCount,
		CreatedAt:      t.CreatedAt,
	}
}

// Settings represents upgrade manager settings.
type Settings struct {
	MaxBatchJobs int `json:"max_batch_jobs"` // 0 means no limit
}

// SettingsResponse represents the settings API response.
type SettingsResponse struct {
	MaxBatchJobs int `json:"max_batch_jobs"`
}

// UpdateSettingsRequest represents a request to update settings.
type UpdateSettingsRequest struct {
	MaxBatchJobs int `json:"max_batch_jobs"`
}
