package bosh

import (
	"context"
	"io"
	"time"
)

// Director defines the interface for BOSH operations
// This provides a clean abstraction over the BOSH CLI director client.
type Director interface {
	// Director operations
	GetInfo(ctx context.Context) (*Info, error)

	// Deployment operations
	GetDeployments(ctx context.Context) ([]Deployment, error)
	GetDeployment(ctx context.Context, name string) (*DeploymentDetail, error)
	CreateDeployment(ctx context.Context, manifest string) (*Task, error)
	DeleteDeployment(ctx context.Context, name string) (*Task, error)
	GetDeploymentVMs(ctx context.Context, deployment string) ([]VM, error)

	// Release operations
	GetReleases(ctx context.Context) ([]Release, error)
	UploadRelease(ctx context.Context, url, sha1 string) (*Task, error)

	// Stemcell operations
	GetStemcells(ctx context.Context) ([]Stemcell, error)
	UploadStemcell(ctx context.Context, url, sha1 string) (*Task, error)

	// Task operations
	GetTask(ctx context.Context, id int) (*Task, error)
	GetTasks(ctx context.Context, taskType string, limit int, states []string, team string) ([]Task, error)
	GetAllTasks(ctx context.Context, limit int) ([]Task, error)
	CancelTask(ctx context.Context, taskID int) error
	GetTaskOutput(ctx context.Context, id int, outputType string) (string, error)
	GetTaskEvents(ctx context.Context, id int) ([]TaskEvent, error)
	// WaitForTask waits until the task reaches a terminal state and returns
	// it with a nil error for every terminal state, so callers judge the
	// state themselves. When the wait stops first, because the context
	// ended or a read of the task failed, the task is left running and the
	// error is a *DirectorCallError with the task's ID and TaskLeftRunning.
	WaitForTask(ctx context.Context, id int) (*Task, error)

	// Event operations
	GetEvents(ctx context.Context, deployment string) ([]Event, error)

	// Log operations
	FetchLogs(ctx context.Context, deployment string, jobName string, jobIndex string) (string, error)

	// Config operations
	// UpdateCloudConfig writes the content as the cloud config named
	// blacksmith. The director merges named cloud configs alongside its
	// default one, so the write adds to the director's cloud config and
	// never replaces it.
	UpdateCloudConfig(ctx context.Context, config string) error
	GetCloudConfig(ctx context.Context) (string, error)
	// ListCloudConfigs returns the latest version of every cloud config on
	// the director, the default one included. The director stores its
	// default cloud config under the name default.
	ListCloudConfigs(ctx context.Context) ([]CloudConfigEntry, error)
	GetConfig(ctx context.Context, configType, configName string) (interface{}, error)
	GetConfigs(ctx context.Context, limit int, configTypes []string) ([]BoshConfig, error)
	GetConfigVersions(ctx context.Context, configType, name string, limit int) ([]BoshConfig, error)
	GetConfigByID(ctx context.Context, configID string) (*BoshConfigDetail, error)
	GetConfigContent(ctx context.Context, configID string) (string, error)
	ComputeConfigDiff(ctx context.Context, fromID, toID string) (*ConfigDiff, error)

	// Cleanup operations
	Cleanup(ctx context.Context, removeAll bool) (*Task, error)

	// SSH operations
	SSHCommand(ctx context.Context, deployment, instance string, index int, command string, args []string, options map[string]interface{}) (string, error)
	SSHSession(ctx context.Context, deployment, instance string, index int, options map[string]interface{}) (interface{}, error)

	// Resurrection operations
	EnableResurrection(ctx context.Context, deployment string, enabled bool) error
	DeleteResurrectionConfig(ctx context.Context, deployment string) error

	// Deployment control operations
	RestartDeployment(ctx context.Context, name string, opts RestartOpts) (*Task, error)
	StopDeployment(ctx context.Context, name string, opts StopOpts) (*Task, error)
	StartDeployment(ctx context.Context, name string, opts StartOpts) (*Task, error)
	RecreateDeployment(ctx context.Context, name string, opts RecreateOpts) (*Task, error)

	// Errand operations
	ListErrands(ctx context.Context, deployment string) ([]Errand, error)
	RunErrand(ctx context.Context, deployment, errand string, opts ErrandOpts) (*ErrandResult, error)

	// Instance operations
	GetInstances(ctx context.Context, deployment string) ([]Instance, error)

	// Deployment update
	UpdateDeployment(ctx context.Context, name, manifest string) (*Task, error)

	// FindRunningTaskForDeployment finds a currently running task for the given deployment.
	// Returns nil if no running task is found.
	FindRunningTaskForDeployment(ctx context.Context, deploymentName string) (*Task, error)

	// FindRunningTasksForDeployment returns every task on the deployment
	// that has not finished, newest first, or an empty list when none runs.
	FindRunningTasksForDeployment(ctx context.Context, deploymentName string) ([]Task, error)

	// FindTaskByContextID returns the newest task the director recorded
	// with the context ID, or nil when it has none. It looks across every
	// deployment, because an upload carries no deployment name, and asks at
	// verbose level 2, because the director hides orphan cleanup tasks at
	// the default level. Each context ID goes on one request, so more than
	// one match is logged. An empty context ID is refused, because the
	// director would answer it with every task.
	FindTaskByContextID(ctx context.Context, contextID string) (*Task, error)

	// Pool statistics (for PooledDirector)
	GetPoolStats() (*PoolStats, error)
}

// Info represents BOSH director information.
type Info struct {
	Name     string          `json:"name"`
	UUID     string          `json:"uuid"`
	Version  string          `json:"version"`
	User     string          `json:"user"`
	CPI      string          `json:"cpi"`
	Features map[string]bool `json:"features"`
	// UAAURL is the director's UAA URL from /info, empty unless the
	// director authenticates through UAA.
	UAAURL string `json:"uaa_url"`
}

// Deployment represents a BOSH deployment.
type Deployment struct {
	Name        string   `json:"name"`
	CloudConfig string   `json:"cloud_config"`
	Releases    []string `json:"releases"`
	Stemcells   []string `json:"stemcells"`
	Teams       []string `json:"teams"`
}

// DeploymentDetail represents detailed deployment information.
type DeploymentDetail struct {
	Name     string `json:"name"`
	Manifest string `json:"manifest"`
}

// VM represents a BOSH VM.
type VM struct {
	// Core VM identity
	ID      string `json:"id"`
	AgentID string `json:"agent_id"`
	CID     string `json:"vm_cid"` // Cloud ID of the VM

	// Job information
	Job      string `json:"job_name"`  // Name of the job
	Index    int    `json:"index"`     // Numeric job index
	JobState string `json:"job_state"` // Aggregate state of job (running, etc.)

	// VM state and properties
	State                    string    `json:"state"`                      // State of the VM
	Active                   *bool     `json:"active"`                     // Whether the VM is active
	Bootstrap                bool      `json:"bootstrap"`                  // Bootstrap property of VM
	Ignore                   bool      `json:"ignore"`                     // Ignore this VM if set to true
	ResurrectionPaused       bool      `json:"resurrection_paused"`        // Resurrection state
	ResurrectionConfigExists bool      `json:"resurrection_config_exists"` // Whether resurrection config exists
	VMCreatedAt              time.Time `json:"vm_created_at"`              // Time when the VM was created

	// Network and placement
	IPs []string `json:"ips"` // List of IPs
	DNS []string `json:"dns"` // DNS entries (often empty)
	AZ  string   `json:"az"`  // Name of availability zone

	// Resource allocation
	VMType       string `json:"vm_type"`       // Name of VM type
	ResourcePool string `json:"resource_pool"` // Name of the resource pool used for the VM

	// Disk information
	DiskCID  string   `json:"disk_cid"`  // Cloud ID of the associated persistent disk
	DiskCIDs []string `json:"disk_cids"` // List of Cloud IDs of the VM's disks

	// Complex data structures
	CloudProperties interface{} `json:"cloud_properties"` // Cloud properties of the VM
	Processes       []VMProcess `json:"processes"`        // List of processes running as part of the job
	Vitals          VMVitals    `json:"vitals"`           // VM vitals
	Stemcell        VMStemcell  `json:"stemcell"`         // Information of the Stemcell used for the VM
}

// VMProcess represents a process running on a VM.
type VMProcess struct {
	Name   string         `json:"name"`
	State  string         `json:"state"`
	CPU    VMVitalsCPU    `json:"cpu"`
	Memory VMVitalsMemory `json:"mem"`
	Uptime VMVitalsUptime `json:"uptime"`
}

// VMVitals represents VM vital statistics.
type VMVitals struct {
	CPU    VMVitalsCPU             `json:"cpu"`
	Memory VMVitalsMemory          `json:"mem"`
	Swap   VMVitalsMemory          `json:"swap"`
	Load   []string                `json:"load"`
	Disk   map[string]VMVitalsDisk `json:"disk"`
	Uptime VMVitalsUptime          `json:"uptime"`
}

// VMVitalsCPU represents CPU vitals.
type VMVitalsCPU struct {
	Total *float64 `json:"total,omitempty"` // used by VMProcess
	Sys   string   `json:"sys"`
	User  string   `json:"user"`
	Wait  string   `json:"wait"`
}

// VMVitalsMemory represents memory vitals.
type VMVitalsMemory struct {
	KB      *uint64  `json:"kb,omitempty"`
	Percent *float64 `json:"percent,omitempty"`
}

// VMVitalsDisk represents disk vitals.
type VMVitalsDisk struct {
	InodePercent string `json:"inode_percent"`
	Percent      string `json:"percent"`
}

// VMVitalsUptime represents uptime vitals.
type VMVitalsUptime struct {
	Seconds *uint64 `json:"secs,omitempty"`
}

// VMStemcell represents stemcell information for a VM.
type VMStemcell struct {
	Name       string `json:"name"`
	Version    string `json:"version"`
	ApiVersion int    `json:"api_version"`
}

// Release represents a BOSH release.
type Release struct {
	Name            string           `json:"name"`
	ReleaseVersions []ReleaseVersion `json:"release_versions"`
}

// ReleaseVersion represents a specific version of a release.
type ReleaseVersion struct {
	Version            string   `json:"version"`
	CommitHash         string   `json:"commit_hash"`
	UncommittedChanges bool     `json:"uncommitted_changes"`
	CurrentlyDeployed  bool     `json:"currently_deployed"`
	JobNames           []string `json:"job_names"`
}

// Stemcell represents a BOSH stemcell.
type Stemcell struct {
	Name        string   `json:"name"`
	Version     string   `json:"version"`
	OS          string   `json:"operating_system"`
	CID         string   `json:"cid"`
	CPI         string   `json:"cpi"`
	Deployments []string `json:"deployments"`
}

// DeployTaskDescription is the description the director gives the task
// of a deploy. A dry run of a deploy is described as "create deployment
// (dry run)", so a deploy is matched on the whole description. The
// director gives a restart, stop, start, or recreate of a deployment the
// same description, so a match on it alone can take a job-state change
// for a deploy. A deploy's task is found by its context ID first, and a
// match on the description is only the fallback.
const DeployTaskDescription = "create deployment"

// Task represents a BOSH task.
type Task struct {
	ID          int        `json:"id"`
	State       string     `json:"state"`
	Description string     `json:"description"`
	Timestamp   int64      `json:"timestamp"`
	StartedAt   time.Time  `json:"started_at"`
	EndedAt     *time.Time `json:"ended_at,omitempty"`
	Result      string     `json:"result"`
	User        string     `json:"user"`
	Deployment  string     `json:"deployment"`
	ContextID   string     `json:"context_id"`
}

// BoshConfig represents a BOSH config.
type BoshConfig struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Type      string    `json:"type"`
	Team      string    `json:"team"`
	CreatedAt time.Time `json:"created_at"`
	IsActive  bool      `json:"is_active"`
}

// CloudConfigEntry is one cloud config the director holds, with the name it
// was uploaded under and its content. The director stores its default
// cloud config under the name default.
type CloudConfigEntry struct {
	Name    string
	Content string
}

// BoshConfigDetail represents detailed config information.
type BoshConfigDetail struct {
	BoshConfig

	Content  string                 `json:"content"`
	Metadata map[string]interface{} `json:"metadata"`
}

// ConfigDiff represents the diff between two config versions.
type ConfigDiff struct {
	FromID     string             `json:"from_id"`
	ToID       string             `json:"to_id"`
	HasChanges bool               `json:"has_changes"`
	Changes    []ConfigDiffChange `json:"changes"`
	DiffString string             `json:"diff_string"`
}

// ConfigDiffChange represents a single change in a config diff.
type ConfigDiffChange struct {
	Type     string `json:"type"` // "added", "removed", "changed"
	Path     string `json:"path"` // YAML path to the changed field
	OldValue string `json:"old_value,omitempty"`
	NewValue string `json:"new_value,omitempty"`
}

// TaskEvent represents an event from a BOSH task.
type TaskEvent struct {
	Time     time.Time              `json:"time"`
	Stage    string                 `json:"stage"`
	Tags     []string               `json:"tags"`
	Total    int                    `json:"total"`
	Task     string                 `json:"task"`
	Index    int                    `json:"index"`
	State    string                 `json:"state"`
	Progress int                    `json:"progress"`
	Data     map[string]interface{} `json:"data"`
	Error    *TaskEventError        `json:"error,omitempty"`
}

// Event represents a BOSH event.
type Event struct {
	ID         string    `json:"id"`
	Time       time.Time `json:"time"`
	User       string    `json:"user"`
	Action     string    `json:"action"`
	ObjectType string    `json:"object_type"`
	ObjectName string    `json:"object_name"`
	Task       string    `json:"task"`
	TaskID     string    `json:"task_id"`
	Deployment string    `json:"deployment"`
	Instance   string    `json:"instance"`
	Context    string    `json:"context"`
	Error      string    `json:"error"`
}

// TaskEventError represents an error in a task event.
type TaskEventError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// EventLogger provides event logging capabilities.
type EventLogger interface {
	io.Writer
	Stage(stage string)
	BeginLinef(pattern string, args ...interface{})
	EndLinef(pattern string, args ...interface{})
	Errorf(pattern string, args ...interface{})
}

// DirectorFactory creates Director instances.
type DirectorFactory interface {
	New(config Config) (Director, error)
}

// Config represents the configuration for creating a Director.
type Config struct {
	// Connection details
	Address           string
	Username          string
	Password          string
	SkipSSLValidation bool

	// UAA authentication (for bosh-cli)
	UAA *UAAConfig

	// Certificate authentication
	CACert     string
	ClientCert string
	ClientKey  string

	// Connection options
	Timeout        time.Duration
	ConnectTimeout time.Duration
	MaxRetries     int
	RetryInterval  time.Duration

	// RequestTimeout bounds one HTTP request to the director, and the wait
	// for the director to start answering it. Zero means 60 seconds. It is
	// the bosh.request_timeout setting.
	RequestTimeout time.Duration

	// Logging
	EventLogger EventLogger // For event logging (legacy)
	Logger      Logger      // For Info/Debug/Error logging
}

// UAAConfig represents UAA authentication configuration.
type UAAConfig struct {
	URL               string
	ClientID          string
	ClientSecret      string
	SkipSSLValidation bool
	CACert            string
}

// Operation options structures. A job-state change always converges, and
// the director ignores force on job-state routes, so neither is an option.
type RestartOpts struct {
	Canaries    string `json:"canaries,omitempty"`
	MaxInFlight string `json:"max_in_flight,omitempty"`
	SkipDrain   bool   `json:"skip_drain"`
}

type StopOpts struct {
	Canaries    string `json:"canaries,omitempty"`
	MaxInFlight string `json:"max_in_flight,omitempty"`
	SkipDrain   bool   `json:"skip_drain"`
	Hard        bool   `json:"hard"`
}

type StartOpts struct {
	Canaries    string `json:"canaries,omitempty"`
	MaxInFlight string `json:"max_in_flight,omitempty"`
}

type RecreateOpts struct {
	Canaries    string `json:"canaries,omitempty"`
	MaxInFlight string `json:"max_in_flight,omitempty"`
	SkipDrain   bool   `json:"skip_drain"`
	Fix         bool   `json:"fix"`
	DryRun      bool   `json:"dry_run"`
}

type ErrandOpts struct {
	KeepAlive   bool     `json:"keep_alive"`
	WhenChanged bool     `json:"when_changed"`
	Instances   []string `json:"instances,omitempty"`
}

// Result structures.
type Errand struct {
	Name string `json:"name"`
}

type ErrandResult struct {
	InstanceGroup   string `json:"instance_group"`
	InstanceID      string `json:"instance_id"`
	ExitCode        int    `json:"exit_code"`
	Stdout          string `json:"stdout"`
	Stderr          string `json:"stderr"`
	LogsBlobstoreID string `json:"logs_blobstore_id"`
	LogsSHA1        string `json:"logs_sha1"`
}

type Instance struct {
	ID           string    `json:"id"`
	Group        string    `json:"group"`
	Index        string    `json:"index"`
	State        string    `json:"state"`
	ProcessState string    `json:"process_state"`
	IPs          []string  `json:"ips"`
	AZ           string    `json:"az"`
	VMType       string    `json:"vm_type"`
	ResourcePool string    `json:"resource_pool"`
	Bootstrap    bool      `json:"bootstrap"`
	Ignore       bool      `json:"ignore"`
	VMCreatedAt  time.Time `json:"vm_created_at"`
}

type PoolStats struct {
	MaxConnections    int           `json:"max_connections"`
	ActiveConnections int32         `json:"active_connections"`
	QueuedRequests    int32         `json:"queued_requests"`
	RejectedRequests  int64         `json:"rejected_requests"`
	TotalRequests     int64         `json:"total_requests"`
	AvgWaitTime       time.Duration `json:"avg_wait_time"`
}
