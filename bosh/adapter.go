package bosh

import (
	"io"
	"time"
)

// Director defines the interface for BOSH operations
// This provides a clean abstraction over the BOSH CLI director client
type Director interface {
	// Director operations
	GetInfo() (*Info, error)

	// Deployment operations
	GetDeployments() ([]Deployment, error)
	GetDeployment(name string) (*DeploymentDetail, error)
	CreateDeployment(manifest string) (*Task, error)
	DeleteDeployment(name string) (*Task, error)
	GetDeploymentVMs(deployment string) ([]VM, error)

	// Release operations
	GetReleases() ([]Release, error)
	UploadRelease(url, sha1 string) (*Task, error)

	// Stemcell operations
	GetStemcells() ([]Stemcell, error)
	UploadStemcell(url, sha1 string) (*Task, error)

	// Task operations
	GetTask(id int) (*Task, error)
	GetTasks(taskType string, limit int, states []string, team string) ([]Task, error)
	GetAllTasks(limit int) ([]Task, error)
	CancelTask(taskID int) error
	GetTaskOutput(id int, outputType string) (string, error)
	GetTaskEvents(id int) ([]TaskEvent, error)

	// Event operations
	GetEvents(deployment string) ([]Event, error)

	// Log operations
	FetchLogs(deployment string, jobName string, jobIndex string) (string, error)

	// Config operations
	UpdateCloudConfig(config string) error
	GetCloudConfig() (string, error)
	GetConfig(configType, configName string) (interface{}, error)
	GetConfigs(limit int, configTypes []string) ([]BoshConfig, error)
	GetConfigVersions(configType, name string, limit int) ([]BoshConfig, error)
	GetConfigByID(configID string) (*BoshConfigDetail, error)
	GetConfigContent(configID string) (string, error)
	ComputeConfigDiff(fromID, toID string) (*ConfigDiff, error)

	// Cleanup operations
	Cleanup(removeAll bool) (*Task, error)

	// SSH operations
	SSHCommand(deployment, instance string, index int, command string, args []string, options map[string]interface{}) (string, error)
	SSHSession(deployment, instance string, index int, options map[string]interface{}) (interface{}, error)

	// Resurrection operations
	EnableResurrection(deployment string, enabled bool) error
	DeleteResurrectionConfig(deployment string) error
}

// Info represents BOSH director information
type Info struct {
	Name     string          `json:"name"`
	UUID     string          `json:"uuid"`
	Version  string          `json:"version"`
	User     string          `json:"user"`
	CPI      string          `json:"cpi"`
	Features map[string]bool `json:"features"`
}

// Deployment represents a BOSH deployment
type Deployment struct {
	Name        string   `json:"name"`
	CloudConfig string   `json:"cloud_config"`
	Releases    []string `json:"releases"`
	Stemcells   []string `json:"stemcells"`
	Teams       []string `json:"teams"`
}

// DeploymentDetail represents detailed deployment information
type DeploymentDetail struct {
	Name     string `json:"name"`
	Manifest string `json:"manifest"`
}

// VM represents a BOSH VM
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

// VMProcess represents a process running on a VM
type VMProcess struct {
	Name   string         `json:"name"`
	State  string         `json:"state"`
	CPU    VMVitalsCPU    `json:"cpu"`
	Memory VMVitalsMemory `json:"mem"`
	Uptime VMVitalsUptime `json:"uptime"`
}

// VMVitals represents VM vital statistics
type VMVitals struct {
	CPU    VMVitalsCPU             `json:"cpu"`
	Memory VMVitalsMemory          `json:"mem"`
	Swap   VMVitalsMemory          `json:"swap"`
	Load   []string                `json:"load"`
	Disk   map[string]VMVitalsDisk `json:"disk"`
	Uptime VMVitalsUptime          `json:"uptime"`
}

// VMVitalsCPU represents CPU vitals
type VMVitalsCPU struct {
	Total *float64 `json:"total,omitempty"` // used by VMProcess
	Sys   string   `json:"sys"`
	User  string   `json:"user"`
	Wait  string   `json:"wait"`
}

// VMVitalsMemory represents memory vitals
type VMVitalsMemory struct {
	KB      *uint64  `json:"kb,omitempty"`
	Percent *float64 `json:"percent,omitempty"`
}

// VMVitalsDisk represents disk vitals
type VMVitalsDisk struct {
	InodePercent string `json:"inode_percent"`
	Percent      string `json:"percent"`
}

// VMVitalsUptime represents uptime vitals
type VMVitalsUptime struct {
	Seconds *uint64 `json:"secs,omitempty"`
}

// VMStemcell represents stemcell information for a VM
type VMStemcell struct {
	Name       string `json:"name"`
	Version    string `json:"version"`
	ApiVersion int    `json:"api_version"`
}

// Release represents a BOSH release
type Release struct {
	Name            string           `json:"name"`
	ReleaseVersions []ReleaseVersion `json:"release_versions"`
}

// ReleaseVersion represents a specific version of a release
type ReleaseVersion struct {
	Version            string   `json:"version"`
	CommitHash         string   `json:"commit_hash"`
	UncommittedChanges bool     `json:"uncommitted_changes"`
	CurrentlyDeployed  bool     `json:"currently_deployed"`
	JobNames           []string `json:"job_names"`
}

// Stemcell represents a BOSH stemcell
type Stemcell struct {
	Name        string   `json:"name"`
	Version     string   `json:"version"`
	OS          string   `json:"operating_system"`
	CID         string   `json:"cid"`
	CPI         string   `json:"cpi"`
	Deployments []string `json:"deployments"`
}

// Task represents a BOSH task
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

// BoshConfig represents a BOSH config
type BoshConfig struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Type      string    `json:"type"`
	Team      string    `json:"team"`
	CreatedAt time.Time `json:"created_at"`
	IsActive  bool      `json:"is_active"`
}

// BoshConfigDetail represents detailed config information
type BoshConfigDetail struct {
	BoshConfig
	Content  string                 `json:"content"`
	Metadata map[string]interface{} `json:"metadata"`
}

// ConfigDiff represents the diff between two config versions
type ConfigDiff struct {
	FromID     string             `json:"from_id"`
	ToID       string             `json:"to_id"`
	HasChanges bool               `json:"has_changes"`
	Changes    []ConfigDiffChange `json:"changes"`
	DiffString string             `json:"diff_string"`
}

// ConfigDiffChange represents a single change in a config diff
type ConfigDiffChange struct {
	Type     string `json:"type"` // "added", "removed", "changed"
	Path     string `json:"path"` // YAML path to the changed field
	OldValue string `json:"old_value,omitempty"`
	NewValue string `json:"new_value,omitempty"`
}

// TaskEvent represents an event from a BOSH task
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

// Event represents a BOSH event
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

// TaskEventError represents an error in a task event
type TaskEventError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// TaskReporter provides task progress reporting
type TaskReporter interface {
	TaskStarted(taskID int)
	TaskFinished(taskID int, state string)
	TaskOutputChunk(taskID int, chunk []byte)
}

// EventLogger provides event logging capabilities
type EventLogger interface {
	io.Writer
	Stage(stage string)
	BeginLinef(pattern string, args ...interface{})
	EndLinef(pattern string, args ...interface{})
	Errorf(pattern string, args ...interface{})
}

// DirectorFactory creates Director instances
type DirectorFactory interface {
	New(config Config) (Director, error)
}

// Config represents the configuration for creating a Director
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

	// Logging
	EventLogger EventLogger // For event logging (legacy)
	Logger      Logger      // For Info/Debug/Error logging
}

// UAAConfig represents UAA authentication configuration
type UAAConfig struct {
	URL               string
	ClientID          string
	ClientSecret      string
	SkipSSLValidation bool
	CACert            string
}
