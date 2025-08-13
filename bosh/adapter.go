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
	GetTaskOutput(id int, outputType string) (string, error)
	GetTaskEvents(id int) ([]TaskEvent, error)

	// Event operations
	GetEvents(deployment string) ([]Event, error)

	// Config operations
	UpdateCloudConfig(config string) error
	GetCloudConfig() (string, error)

	// Cleanup operations
	Cleanup(removeAll bool) (*Task, error)
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
	ID                 string   `json:"id"`
	CID                string   `json:"cid"`
	Job                string   `json:"job"`
	Index              int      `json:"index"`
	State              string   `json:"state"`
	IPs                []string `json:"ips"`
	DNS                []string `json:"dns"`
	AZ                 string   `json:"az"`
	VMType             string   `json:"vm_type"`
	ResourcePool       string   `json:"resource_pool"`
	ResurrectionPaused bool     `json:"resurrection_paused"`
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
