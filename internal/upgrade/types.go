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
	// InstanceStatusSkipped indicates the instance upgrade was skipped (due to job cancellation).
	InstanceStatusSkipped InstanceStatus = "skipped"
)

// InstanceUpgrade represents the upgrade status for a single instance.
type InstanceUpgrade struct {
	InstanceID     string         `json:"instance_id"`
	InstanceName   string         `json:"instance_name,omitempty"`
	DeploymentName string         `json:"deployment_name"`
	ServiceID      string         `json:"service_id,omitempty"`
	PlanID         string         `json:"plan_id,omitempty"`
	Status         InstanceStatus `json:"status"`
	BOSHTaskID     int            `json:"bosh_task_id,omitempty"`
	Error          string         `json:"error,omitempty"`
	StartedAt      *time.Time     `json:"started_at,omitempty"`
	CompletedAt    *time.Time     `json:"completed_at,omitempty"`
}

// UpgradeTask represents a batch upgrade task.
type UpgradeTask struct {
	ID              string            `json:"id"`
	Name            string            `json:"name,omitempty"`
	Status          TaskStatus        `json:"status"`
	Paused          bool              `json:"paused"`
	TargetStemcell  StemcellTarget    `json:"target_stemcell"`
	Instances       []InstanceUpgrade `json:"instances"`
	TotalCount      int               `json:"total_count"`
	CompletedCount  int               `json:"completed_count"`
	FailedCount     int               `json:"failed_count"`
	CancelledCount  int               `json:"cancelled_count"`
	SkippedCount    int               `json:"skipped_count"`
	CreatedAt       time.Time         `json:"created_at"`
	StartedAt       *time.Time        `json:"started_at,omitempty"`
	CompletedAt     *time.Time        `json:"completed_at,omitempty"`
	CreatedBy       string            `json:"created_by,omitempty"`
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
		CreatedAt:      t.CreatedAt,
	}
}
