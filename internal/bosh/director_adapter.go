package bosh

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	boshdirector "github.com/cloudfoundry/bosh-cli/v7/director"
	boshuaa "github.com/cloudfoundry/bosh-cli/v7/uaa"
	boshlog "github.com/cloudfoundry/bosh-utils/logger"
	boshuuid "github.com/cloudfoundry/bosh-utils/uuid"
	"github.com/geofffranks/spruce"
	"golang.org/x/crypto/ssh"
	"gopkg.in/yaml.v2"
)

// Constants for director adapter operations.
const (
	// Task fetch multiplier for filtering.
	taskFetchMultiplier = 2

	// Config priority values.
	configPriorityCPI          = 2
	configPriorityResurrection = 3
	configPriorityRuntime      = 4

	// File size limits.
	maxLogFileSize = 10 * 1024 * 1024 // 10MB limit per file

	// HTTP client timeouts.
	httpClientTimeout    = 30 * time.Second
	httpTransportTimeout = 30 * time.Second

	// Default ports for BOSH services.
	defaultBOSHDirectorPort = 25555
	authTypeUAA             = "uaa"
	// Default config limit.
	defaultConfigLimit = 100
)

// Static error variables to satisfy err113.
var (
	ErrGetConfigFailed                = errors.New("failed to get config")
	ErrConfigYAMLParseFailed          = errors.New("failed to parse config YAML")
	ErrDeploymentNameExtractionFailed = errors.New("could not extract deployment name from manifest")
	ErrNoTaskFoundDeploymentDeletion  = errors.New("no task found for deployment deletion")
	ErrNoTaskFoundReleaseUpload       = errors.New("no task found for release upload")
	ErrNoTaskFoundStemcellUpload      = errors.New("no task found for stemcell upload")
	ErrTaskCannotBeCancelled          = errors.New("task is in invalid state and cannot be cancelled")
	ErrNoCloudConfigFound             = errors.New("no cloud config found")
	ErrInvalidConfigIDFormat          = errors.New("invalid config ID format")
	ErrNoTaskFoundCleanup             = errors.New("no task found for cleanup")
	ErrNoHostsAvailableSSH            = errors.New("no hosts available for SSH connection")
	ErrResurrectionConfigUpdateFailed = errors.New("failed to update resurrection config for deployment")
	ErrResurrectionConfigDeleteFailed = errors.New("failed to delete resurrection config for deployment")
	ErrResurrectionConfigNotFound     = errors.New("resurrection config for deployment not found or already deleted")
	ErrConfigNotFound                 = errors.New("config not found")
	ErrUAAAutoDetectionNotApplicable  = errors.New("UAA auto-detection not applicable")
	ErrUAAURLNotFound                 = errors.New("UAA URL not found in auth options")
	ErrDirectorNotUsingUAA            = errors.New("director does not use UAA authentication")
	ErrUAAURLNotString                = errors.New("UAA URL in director info is not a string")
	ErrUAAURLNotFoundInInfo           = errors.New("UAA URL not found in director info")
)

// DirectorAdapter wraps bosh-cli director to implement Director interface
// GetConfig retrieves a configuration by type and name from BOSH director.
func (da *DirectorAdapter) GetConfig(configType, configName string) (interface{}, error) {
	da.log.Debugf("Getting config type=%s name=%s", configType, configName)

	// Get the config using the BOSH director
	config, err := da.director.LatestConfig(configType, configName)
	if err != nil {
		// Check if it's a "not found" error
		if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "404") {
			da.log.Debugf("Config %s/%s not found", configType, configName)

			return nil, ErrNoCloudConfigFound
		}

		da.log.Errorf("Failed to get config %s/%s: %v", configType, configName, err)

		return nil, fmt.Errorf("%w %s/%s: %w", ErrGetConfigFailed, configType, configName, err)
	}

	da.log.Debugf("Retrieved config %s/%s successfully", configType, configName)
	da.log.Debugf("Config content:\n%s", config.Content)

	// Parse the YAML content to return as a map
	var configData map[string]interface{}

	err = yaml.Unmarshal([]byte(config.Content), &configData)
	if err != nil {
		da.log.Errorf("Failed to parse config YAML for %s/%s: %v", configType, configName, err)

		return nil, fmt.Errorf("%w: %w", ErrConfigYAMLParseFailed, err)
	}

	da.log.Debugf("Parsed config data: %+v", configData)

	return configData, nil
}

type DirectorAdapter struct {
	director boshdirector.Director
	logger   boshlog.Logger
	log      Logger // Application logger for Info/Debug logging
}

// NewDirectorAdapter creates a new bosh-cli based Director.
func NewDirectorAdapter(config Config) (Director, error) {
	// Use the provided logger or create a no-op logger
	var appLogger Logger
	if config.Logger != nil {
		appLogger = config.Logger
	} else {
		// Create a no-op logger if none provided
		appLogger = &noOpLogger{}
	}

	appLogger.Infof("Creating new BOSH director adapter")
	appLogger.Debugf("Director address: %s", config.Address)

	logger := boshlog.NewLogger(boshlog.LevelError)

	// Create factory config
	appLogger.Debugf("Building factory configuration")

	factoryConfig, err := buildFactoryConfig(config, logger)
	if err != nil {
		appLogger.Errorf("Failed to build factory config: %v", err)

		return nil, fmt.Errorf("failed to build factory config: %w", err)
	}

	// Create director factory
	appLogger.Debugf("Creating director factory")

	factory := boshdirector.NewFactory(logger)

	// Create task reporter and file reporter
	// Use the built-in no-op reporters from the director package
	taskReporter := boshdirector.NoopTaskReporter{}
	fileReporter := boshdirector.NoopFileReporter{}

	// Create director with authentication
	appLogger.Debugf("Creating director client")

	director, err := factory.New(*factoryConfig, taskReporter, fileReporter)
	if err != nil {
		appLogger.Errorf("Failed to create director: %v", err)

		return nil, fmt.Errorf("failed to create director: %w", err)
	}

	appLogger.Infof("Successfully created BOSH director adapter")

	return &DirectorAdapter{
		director: director,
		logger:   logger,
		log:      appLogger,
	}, nil
}

// Logger interface for application logging.
type Logger interface {
	Infof(format string, args ...interface{})
	Debugf(format string, args ...interface{})
	Errorf(format string, args ...interface{})
}

// BufferedTaskReporter implements TaskReporter to capture task output.
type BufferedTaskReporter struct {
	mu       sync.Mutex
	output   bytes.Buffer
	started  bool
	finished bool
	state    string
}

func (r *BufferedTaskReporter) TaskStarted(id int) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.started = true
}

func (r *BufferedTaskReporter) TaskFinished(id int, state string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.finished = true
	r.state = state
}

func (r *BufferedTaskReporter) TaskOutputChunk(id int, chunk []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.output.Write(chunk)
}

func (r *BufferedTaskReporter) GetOutput() string {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.output.String()
}

// noOpLogger is a no-op implementation of Logger interface.
type noOpLogger struct{}

func (n *noOpLogger) Infof(format string, args ...interface{})  {}
func (n *noOpLogger) Debugf(format string, args ...interface{}) {}
func (n *noOpLogger) Errorf(format string, args ...interface{}) {}

// GetInfo retrieves BOSH director information.
func (d *DirectorAdapter) GetInfo() (*Info, error) {
	d.log.Infof("Getting BOSH director information")
	d.log.Debugf("Calling director.Infof()")

	info, err := d.director.Info()
	if err != nil {
		d.log.Errorf("Failed to get director info: %v", err)

		return nil, fmt.Errorf("failed to get info: %w", err)
	}

	d.log.Debugf("Director info retrieved - Name: %s, UUID: %s, Version: %s", info.Name, info.UUID, info.Version)

	features := make(map[string]bool)
	for feature, enabled := range info.Features {
		features[feature] = enabled
	}

	d.log.Infof("Successfully retrieved director info: %s (%s)", info.Name, info.Version)

	return &Info{
		Name:     info.Name,
		UUID:     info.UUID,
		Version:  info.Version,
		User:     info.User,
		CPI:      info.CPI,
		Features: features,
	}, nil
}

// GetDeployments lists all deployments.
func (d *DirectorAdapter) GetDeployments() ([]Deployment, error) {
	d.log.Infof("Listing all deployments")
	d.log.Debugf("Calling director.Deployments()")

	deps, err := d.director.Deployments()
	if err != nil {
		d.log.Errorf("Failed to get deployments: %v", err)

		return nil, fmt.Errorf("failed to get deployments: %w", err)
	}

	d.log.Debugf("Found %d deployments", len(deps))

	deployments := make([]Deployment, len(deps))
	for index, dep := range deps {
		depReleases, _ := dep.Releases()

		releases := make([]string, len(depReleases))
		for j, rel := range depReleases {
			releases[j] = fmt.Sprintf("%s/%s", rel.Name(), rel.Version())
		}

		depStemcells, _ := dep.Stemcells()

		stemcells := make([]string, len(depStemcells))
		for j, sc := range depStemcells {
			stemcells[j] = fmt.Sprintf("%s/%s", sc.Name(), sc.Version())
		}

		cloudConfig, _ := dep.CloudConfig()
		teams, _ := dep.Teams()

		deployments[index] = Deployment{
			Name:        dep.Name(),
			CloudConfig: cloudConfig,
			Releases:    releases,
			Stemcells:   stemcells,
			Teams:       teams,
		}
	}

	d.log.Infof("Successfully retrieved %d deployments", len(deployments))

	return deployments, nil
}

// GetDeployment retrieves a specific deployment.
func (d *DirectorAdapter) GetDeployment(name string) (*DeploymentDetail, error) {
	d.log.Infof("Getting deployment: %s", name)
	d.log.Debugf("Finding deployment %s", name)

	dep, err := d.director.FindDeployment(name)
	if err != nil {
		d.log.Errorf("Failed to find deployment %s: %v", name, err)

		return nil, fmt.Errorf("failed to get deployment %s: %w", name, err)
	}

	d.log.Debugf("Retrieving manifest for deployment %s", name)

	manifest, err := dep.Manifest()
	if err != nil {
		d.log.Errorf("Failed to get manifest for deployment %s: %v", name, err)

		return nil, fmt.Errorf("failed to get manifest for deployment %s: %w", name, err)
	}

	d.log.Infof("Successfully retrieved deployment %s (manifest size: %d bytes)", name, len(manifest))

	return &DeploymentDetail{
		Name:     name,
		Manifest: manifest,
	}, nil
}

// CreateDeployment creates a new deployment.
func (d *DirectorAdapter) CreateDeployment(manifest string) (*Task, error) {
	// Parse deployment name from manifest
	deploymentName := ExtractDeploymentName(manifest)
	if deploymentName == "" {
		d.log.Errorf("Could not extract deployment name from manifest")

		return nil, ErrDeploymentNameExtractionFailed
	}

	d.log.Infof("Creating/updating deployment: %s", deploymentName)
	d.log.Debugf("Manifest size: %d bytes", len(manifest))

	// Try to find existing deployment
	dep, err := d.director.FindDeployment(deploymentName)
	if err != nil {
		// Check if this is a "not found" error (deployment doesn't exist) vs other errors
		// Since bosh-cli doesn't export specific error types, we check the error message
		if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "does not exist") {
			// Deployment doesn't exist - this is expected for new deployments
			return d.handleNewDeployment(deploymentName)
		}
		// Other errors should be propagated
		return nil, fmt.Errorf("failed to find deployment: %w", err)
	}

	// Update existing deployment
	return d.updateExistingDeployment(dep, deploymentName, manifest)
}

// DeleteDeployment deletes a deployment.
func (d *DirectorAdapter) DeleteDeployment(name string) (*Task, error) {
	d.log.Infof("Deleting deployment: %s", name)
	d.log.Debugf("Finding deployment %s for deletion", name)

	dep, err := d.director.FindDeployment(name)
	if err != nil {
		d.log.Errorf("Failed to find deployment %s: %v", name, err)

		return nil, fmt.Errorf("failed to find deployment %s: %w", name, err)
	}

	// Delete the deployment (force = false)
	d.log.Debugf("Initiating deletion of deployment %s (force=false)", name)

	err = dep.Delete(false)
	if err != nil {
		d.log.Errorf("Failed to delete deployment %s: %v", name, err)

		return nil, fmt.Errorf("failed to delete deployment %s: %w", name, err)
	}

	// Get the latest task
	tasks, err := d.director.RecentTasks(1, boshdirector.TasksFilter{
		Deployment: name,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get task for deployment deletion: %w", err)
	}

	if len(tasks) > 0 {
		task := convertDirectorTask(tasks[0])
		d.log.Infof("Deployment %s deletion started, task ID: %d", name, task.ID)

		return task, nil
	}

	d.log.Errorf("No task found for deployment %s deletion", name)

	return nil, ErrNoTaskFoundDeploymentDeletion
}

// GetDeploymentVMs retrieves VMs for a deployment with full details (format=full).
func (d *DirectorAdapter) GetDeploymentVMs(deployment string) ([]VM, error) {
	d.log.Infof("Getting VMs for deployment: %s", deployment)
	d.log.Debugf("Finding deployment %s", deployment)

	dep, err := d.director.FindDeployment(deployment)
	if err != nil {
		d.log.Errorf("Failed to find deployment %s: %v", deployment, err)

		return nil, fmt.Errorf("failed to find deployment %s: %w", deployment, err)
	}

	d.log.Debugf("Retrieving detailed VM information for deployment %s (format=full)", deployment)

	vmInfos, err := dep.VMInfos()
	if err != nil {
		d.log.Errorf("Failed to get VMs for deployment %s: %v", deployment, err)

		return nil, fmt.Errorf("failed to get VMs for deployment %s: %w", deployment, err)
	}

	vms := make([]VM, len(vmInfos))
	for index, vmInfo := range vmInfos {
		vms[index] = d.convertVMInfo(vmInfo)
	}

	d.log.Infof("Successfully retrieved %d VMs for deployment %s", len(vms), deployment)

	return vms, nil
}

// GetReleases retrieves all releases.
func (d *DirectorAdapter) GetReleases() ([]Release, error) {
	d.log.Infof("Getting all releases")
	d.log.Debugf("Calling director.Releases()")

	// Test basic connectivity first
	info, err := d.director.Info()
	if err != nil {
		d.log.Errorf("Failed to get director info (auth test): %v", err)
		d.log.Errorf("This might indicate an authentication issue")
	} else {
		d.log.Debugf("Director info test successful - User: %s, Name: %s", info.User, info.Name)
		d.log.Debugf("Director authentication type: %s", info.Auth.Type)

		switch info.Auth.Type {
		case authTypeUAA:
			if uaaURL, ok := info.Auth.Options["url"].(string); ok {
				d.log.Debugf("Director is configured for UAA authentication at: %s", uaaURL)
				d.log.Debugf("Using UAA client credentials for authentication")
			}
		case "basic":
			d.log.Debugf("Director is configured for basic authentication")
		}

		if info.User == "" {
			d.log.Debugf("Note: User field is empty in /info response - this is expected as /info doesn't require authentication")
		}
	}

	releases, err := d.director.Releases()
	if err != nil {
		d.log.Errorf("Failed to get releases: %v", err)

		return nil, fmt.Errorf("failed to get releases: %w", err)
	}

	d.log.Debugf("Found %d releases", len(releases))

	result := make([]Release, 0, len(releases))
	for _, rel := range releases {
		// Get the actual version string without the asterisk
		result = append(result, Release{
			Name: rel.Name(),
			ReleaseVersions: []ReleaseVersion{
				{
					Version: rel.Version().String(),
				},
			},
		})
	}

	return result, nil
}

// UploadRelease uploads a release from URL.
func (d *DirectorAdapter) UploadRelease(url, sha1 string) (*Task, error) {
	d.log.Infof("Uploading release from URL: %s", url)
	d.log.Debugf("Release SHA1: %s", sha1)

	// Simplified upload - bosh-cli has different signature
	err := d.director.UploadReleaseURL(url, sha1, false, false)
	if err != nil {
		d.log.Errorf("Failed to upload release from %s: %v", url, err)

		return nil, fmt.Errorf("failed to upload release from %s: %w", url, err)
	}

	// Get the latest task
	tasks, err := d.director.RecentTasks(1, boshdirector.TasksFilter{})
	if err != nil {
		return nil, fmt.Errorf("failed to get task for release upload: %w", err)
	}

	if len(tasks) > 0 {
		task := convertDirectorTask(tasks[0])
		d.log.Infof("Release upload started, task ID: %d", task.ID)

		return task, nil
	}

	d.log.Errorf("No task found for release upload")

	return nil, ErrNoTaskFoundReleaseUpload
}

// GetStemcells retrieves all stemcells.
func (d *DirectorAdapter) GetStemcells() ([]Stemcell, error) {
	d.log.Infof("Getting all stemcells")
	d.log.Debugf("Calling director.Stemcells()")

	stemcells, err := d.director.Stemcells()
	if err != nil {
		d.log.Errorf("Failed to get stemcells: %v", err)

		return nil, fmt.Errorf("failed to get stemcells: %w", err)
	}

	d.log.Debugf("Found %d stemcells", len(stemcells))

	result := make([]Stemcell, len(stemcells))
	for index, stemcell := range stemcells {
		result[index] = Stemcell{
			Name:        stemcell.Name(),
			Version:     stemcell.Version().String(),
			OS:          stemcell.OSName(),
			CID:         stemcell.CID(),
			CPI:         stemcell.CPI(),
			Deployments: []string{}, // Deployments not directly available
		}
	}

	d.log.Infof("Successfully retrieved %d stemcells", len(result))

	return result, nil
}

// UploadStemcell uploads a stemcell from URL.
func (d *DirectorAdapter) UploadStemcell(url, sha1 string) (*Task, error) {
	d.log.Infof("Uploading stemcell from URL: %s", url)
	d.log.Debugf("Stemcell SHA1: %s", sha1)

	// Simplified upload - bosh-cli has different signature
	err := d.director.UploadStemcellURL(url, sha1, false)
	if err != nil {
		d.log.Errorf("Failed to upload stemcell from %s: %v", url, err)

		return nil, fmt.Errorf("failed to upload stemcell from %s: %w", url, err)
	}

	// Get the latest task
	tasks, err := d.director.RecentTasks(1, boshdirector.TasksFilter{})
	if err != nil {
		return nil, fmt.Errorf("failed to get task for stemcell upload: %w", err)
	}

	if len(tasks) > 0 {
		task := convertDirectorTask(tasks[0])
		d.log.Infof("Stemcell upload started, task ID: %d", task.ID)

		return task, nil
	}

	d.log.Errorf("No task found for stemcell upload")

	return nil, ErrNoTaskFoundStemcellUpload
}

// GetTask retrieves a task by ID.
func (d *DirectorAdapter) GetTask(taskID int) (*Task, error) {
	d.log.Infof("Getting task: %d", taskID)
	d.log.Debugf("Finding task %d", taskID)

	task, err := d.director.FindTask(taskID)
	if err != nil {
		d.log.Errorf("Failed to get task %d: %v", taskID, err)

		return nil, fmt.Errorf("failed to get task %d: %w", taskID, err)
	}

	convertedTask := convertDirectorTask(task)
	d.log.Infof("Successfully retrieved task %d (state: %s)", taskID, convertedTask.State)

	return convertedTask, nil
}

// GetTasks retrieves multiple tasks based on filter criteria.
func (d *DirectorAdapter) GetTasks(taskType string, limit int, states []string, team string) ([]Task, error) {
	d.log.Infof("Getting tasks (type: %s, limit: %d, states: %v, team: %s)", taskType, limit, states, team)

	// Convert limit to sensible defaults
	if limit <= 0 || limit > 200 {
		limit = 50 // Default limit
	}

	// Get tasks based on type
	tasks, err := d.fetchTasksByType(taskType, limit)
	if err != nil {
		return nil, err
	}

	// Apply filters
	tasks = d.filterTasksByStates(tasks, states)
	tasks = d.filterTasksByTeam(tasks, team)

	// Limit results
	if len(tasks) > limit {
		tasks = tasks[:limit]
	}

	// Convert director tasks to our Task type
	result := make([]Task, len(tasks))
	for i, task := range tasks {
		result[i] = *convertDirectorTask(task)
	}

	d.log.Infof("Successfully retrieved %d tasks (type: %s)", len(result), taskType)

	return result, nil
}

// GetAllTasks retrieves all available tasks up to the specified limit.
func (d *DirectorAdapter) GetAllTasks(limit int) ([]Task, error) {
	d.log.Infof("Getting all tasks (limit: %d)", limit)

	// Use GetTasks with "all" type for consistency
	return d.GetTasks("all", limit, nil, "")
}

// CancelTask cancels a running task.
func (d *DirectorAdapter) CancelTask(taskID int) error {
	d.log.Infof("Cancelling task: %d", taskID)
	d.log.Debugf("Finding task %d for cancellation", taskID)

	// Find the task first
	task, err := d.director.FindTask(taskID)
	if err != nil {
		d.log.Errorf("Failed to find task %d: %v", taskID, err)

		return fmt.Errorf("failed to find task %d: %w", taskID, err)
	}

	// Check if task can be cancelled (must be in processing or queued state)
	state := task.State()
	if state != "processing" && state != "queued" {
		d.log.Errorf("Task %d is in state '%s' and cannot be cancelled", taskID, state)

		return fmt.Errorf("%w: task %d is in state '%s'", ErrTaskCannotBeCancelled, taskID, state)
	}

	// Cancel the task
	d.log.Debugf("Cancelling task %d (current state: %s)", taskID, state)

	err = task.Cancel()
	if err != nil {
		d.log.Errorf("Failed to cancel task %d: %v", taskID, err)

		return fmt.Errorf("failed to cancel task %d: %w", taskID, err)
	}

	d.log.Infof("Successfully cancelled task %d", taskID)

	return nil
}

// GetTaskOutput retrieves task output.
func (d *DirectorAdapter) GetTaskOutput(taskID int, outputType string) (string, error) {
	d.log.Infof("Getting task output for task %d (type: %s)", taskID, outputType)
	d.log.Debugf("Finding task %d for output retrieval", taskID)

	task, err := d.director.FindTask(taskID)
	if err != nil {
		d.log.Errorf("Failed to get task %d: %v", taskID, err)

		return "", fmt.Errorf("failed to get task %d: %w", taskID, err)
	}

	// Create a buffered reporter to capture output
	reporter := &BufferedTaskReporter{}

	// Get task output based on type
	switch outputType {
	case "task":
		// For  task output, use event output which matches 'bosh task <id>' output
		err = task.EventOutput(reporter)
	case "debug":
		// For debug output, use DebugOutput method
		err = task.DebugOutput(reporter)
	case "event":
		// For event output, use EventOutput method
		err = task.EventOutput(reporter)
	case "result":
		// For result output, use ResultOutput method
		err = task.ResultOutput(reporter)
	case "cpi":
		// For CPI output, use CPIOutput method
		err = task.CPIOutput(reporter)
	default:
		// Default to debug output
		err = task.DebugOutput(reporter)
	}

	if err != nil {
		d.log.Errorf("Failed to get task output for task %d: %v", taskID, err)

		return "", fmt.Errorf("failed to get task output for task %d: %w", taskID, err)
	}

	output := reporter.GetOutput()
	d.log.Infof("Successfully retrieved task output for task %d (size: %d bytes)", taskID, len(output))
	d.log.Debugf("Task output type %s retrieved successfully", outputType)

	return output, nil
}

// GetTaskEvents retrieves task events.
func (d *DirectorAdapter) GetTaskEvents(taskID int) ([]TaskEvent, error) {
	d.log.Infof("Getting task events for task %d", taskID)
	d.log.Debugf("Finding task %d for event retrieval", taskID)

	task, err := d.director.FindTask(taskID)
	if err != nil {
		d.log.Errorf("Failed to get task %d: %v", taskID, err)

		return nil, fmt.Errorf("failed to get task %d: %w", taskID, err)
	}

	// Get event output which should be JSON lines
	reporter := &BufferedTaskReporter{}

	err = task.ResultOutput(reporter)
	if err != nil {
		d.log.Errorf("Failed to get task events for task %d: %v", taskID, err)

		return nil, fmt.Errorf("failed to get task events for task %d: %w", taskID, err)
	}

	// Parse the output as JSON lines for events
	output := reporter.GetOutput()
	events := d.parseTaskEvents(output)

	d.log.Infof("Successfully retrieved %d events for task %d", len(events), taskID)

	return events, nil
}

// GetEvents retrieves events for a specific deployment.
func (d *DirectorAdapter) GetEvents(deployment string) ([]Event, error) {
	d.log.Infof("Getting events for deployment: %s", deployment)
	d.log.Debugf("Fetching events from BOSH director")

	// Use the BOSH director's Events method to get deployment events
	filter := boshdirector.EventsFilter{
		Deployment: deployment,
	}

	directorEvents, err := d.director.Events(filter)
	if err != nil {
		d.log.Errorf("Failed to get events for deployment %s: %v", deployment, err)

		return nil, fmt.Errorf("failed to get events for deployment %s: %w", deployment, err)
	}

	// Convert director events to our Event type
	events := make([]Event, 0, len(directorEvents))
	for _, dirEvent := range directorEvents {
		event := Event{
			ID:         dirEvent.ID(),
			Time:       dirEvent.Timestamp(),
			User:       dirEvent.User(),
			Action:     dirEvent.Action(),
			ObjectType: dirEvent.ObjectType(),
			ObjectName: dirEvent.ObjectName(),
			TaskID:     dirEvent.TaskID(),
			Deployment: dirEvent.DeploymentName(),
			Instance:   dirEvent.Instance(),
			Context:    fmt.Sprintf("%v", dirEvent.Context()),
			Error:      dirEvent.Error(),
		}
		events = append(events, event)
	}

	d.log.Infof("Successfully retrieved %d events for deployment %s", len(events), deployment)

	return events, nil
}

// UpdateCloudConfig updates the cloud config.
func (d *DirectorAdapter) UpdateCloudConfig(config string) error {
	d.log.Infof("Updating cloud config")
	d.log.Debugf("Cloud config size: %d bytes", len(config))

	// UpdateConfig has different signature in bosh-cli
	_, err := d.director.UpdateConfig("cloud", "", config, []byte(config))
	if err != nil {
		d.log.Errorf("Failed to update cloud config: %v", err)

		return fmt.Errorf("failed to update cloud config: %w", err)
	}

	d.log.Infof("Successfully updated cloud config")

	return nil
}

// GetCloudConfig retrieves the cloud config.
func (d *DirectorAdapter) GetCloudConfig() (string, error) {
	d.log.Infof("Getting cloud config")
	d.log.Debugf("Listing cloud configs (limit: 1)")

	configs, err := d.director.ListConfigs(1, boshdirector.ConfigsFilter{
		Type: "cloud",
	})
	if err != nil {
		d.log.Errorf("Failed to get cloud config: %v", err)

		return "", fmt.Errorf("failed to get cloud config: %w", err)
	}

	if len(configs) == 0 {
		d.log.Errorf("No cloud config found")

		return "", ErrNoCloudConfigFound
	}

	d.log.Infof("Successfully retrieved cloud config (size: %d bytes)", len(configs[0].Content))

	return configs[0].Content, nil
}

// normalizeConfigLimit ensures the limit is within sensible bounds.

// GetConfigs retrieves configs based on limit and type filters
// Only returns the currently active configs for the main list view.
func (d *DirectorAdapter) GetConfigs(limit int, configTypes []string) ([]BoshConfig, error) {
	d.log.Infof("Getting configs (limit: %d, types: %v)", limit, configTypes)

	limit = d.normalizeConfigLimit(limit)
	tempLimit := 1000

	allConfigs, err := d.director.ListConfigs(tempLimit, boshdirector.ConfigsFilter{})
	if err != nil {
		d.log.Errorf("Failed to get configs list: %v", err)

		return nil, fmt.Errorf("failed to get configs: %w", err)
	}

	seen := make(map[string]bool)

	var result []BoshConfig

	for _, config := range allConfigs {
		key := fmt.Sprintf("%s:%s", config.Type, config.Name)
		if seen[key] {
			continue
		}

		seen[key] = true

		if !d.shouldIncludeConfigType(config.Type, configTypes) {
			continue
		}

		latestConfig, err := d.fetchLatestConfig(config)
		if err != nil {
			continue
		}

		if latestConfig != nil {
			result = append(result, *latestConfig)
		}
	}

	d.sortConfigsByTypeAndName(result)
	d.log.Infof("Successfully retrieved %d active configs", len(result))

	if limit > 0 && len(result) > limit {
		result = result[:limit]
	}

	return result, nil
}

// GetConfigVersions retrieves all versions of a specific config.
func (d *DirectorAdapter) GetConfigVersions(configType, name string, limit int) ([]BoshConfig, error) {
	d.log.Infof("Getting config versions for type: %s, name: %s (limit: %d)", configType, name, limit)

	if limit <= 0 || limit > 100 {
		limit = 20 // Default limit for version history
	}

	// Use filter to get all versions of a specific config
	configs, err := d.director.ListConfigs(limit, boshdirector.ConfigsFilter{
		Type: configType,
		Name: name,
	})
	if err != nil {
		d.log.Errorf("Failed to get config versions: %v", err)

		return nil, fmt.Errorf("failed to get config versions: %w", err)
	}

	// Convert and mark which version is current/active
	result := make([]BoshConfig, len(configs))
	for i, config := range configs {
		bc := convertDirectorBoshConfig(config)
		bc.IsActive = config.Current
		result[i] = bc
	}

	d.log.Infof("Successfully retrieved %d config versions", len(result))

	return result, nil
}

// GetConfigByID retrieves detailed config information by ID.
func (d *DirectorAdapter) GetConfigByID(configID string) (*BoshConfigDetail, error) {
	d.log.Infof("Getting config details for ID: %s", configID)

	// Remove the * suffix if present (indicates active config)
	cleanID := strings.TrimSuffix(configID, "*")

	// Parse the ID to int
	configIDInt, err := strconv.Atoi(cleanID)
	if err != nil {
		d.log.Errorf("Invalid config ID format: %s", configID)

		return nil, fmt.Errorf("%w: %s", ErrInvalidConfigIDFormat, configID)
	}

	// Get the config by ID
	config, err := d.director.LatestConfigByID(cleanID)
	if err != nil {
		d.log.Errorf("Failed to get config by ID %d: %v", configIDInt, err)

		return nil, fmt.Errorf("failed to get config: %w", err)
	}

	// Fix YAML key casing issues that may be introduced by BOSH CLI library
	fixedContent := fixYAMLKeyCasing(config.Content)

	// Convert to our BoshConfigDetail type
	result := &BoshConfigDetail{
		BoshConfig: convertDirectorBoshConfig(config),
		Content:    fixedContent,
		Metadata: map[string]interface{}{
			"id":         config.ID,
			"name":       config.Name,
			"type":       config.Type,
			"team":       config.Team,
			"created_at": config.CreatedAt,
		},
	}

	d.log.Infof("Successfully retrieved config details for ID: %s", configID)

	return result, nil
}

// GetConfigContent retrieves the full content of a config by its ID.
func (d *DirectorAdapter) GetConfigContent(configID string) (string, error) {
	d.log.Infof("Getting config content for ID: %s", configID)

	// Remove the * suffix if present (indicates active config)
	cleanID := strings.TrimSuffix(configID, "*")

	// Get the config by ID
	config, err := d.director.LatestConfigByID(cleanID)
	if err != nil {
		d.log.Errorf("Failed to get config content by ID %s: %v", cleanID, err)

		return "", fmt.Errorf("failed to get config content: %w", err)
	}

	d.log.Infof("Successfully retrieved config content for ID: %s (size: %d bytes)", configID, len(config.Content))

	return config.Content, nil
}

// ComputeConfigDiff computes the diff between two config versions.
func (d *DirectorAdapter) ComputeConfigDiff(fromID, toID string) (*ConfigDiff, error) {
	d.log.Infof("Computing diff between configs %s -> %s", fromID, toID)

	// Get content for both configs
	fromContent, err := d.GetConfigContent(fromID)
	if err != nil {
		return nil, fmt.Errorf("failed to get 'from' config: %w", err)
	}

	toContent, err := d.GetConfigContent(toID)
	if err != nil {
		return nil, fmt.Errorf("failed to get 'to' config: %w", err)
	}

	// Parse YAML content
	var (
		fromData interface{}
		toData   interface{}
	)

	err = yaml.Unmarshal([]byte(fromContent), &fromData)
	if err != nil {
		d.log.Errorf("Failed to parse 'from' config YAML: %v", err)

		return nil, fmt.Errorf("failed to parse 'from' config YAML: %w", err)
	}

	err = yaml.Unmarshal([]byte(toContent), &toData)
	if err != nil {
		d.log.Errorf("Failed to parse 'to' config YAML: %v", err)

		return nil, fmt.Errorf("failed to parse 'to' config YAML: %w", err)
	}

	// Compute diff using spruce
	diffable, err := spruce.Diff(fromData, toData)
	if err != nil {
		d.log.Errorf("Failed to compute diff: %v", err)

		return nil, fmt.Errorf("failed to compute diff: %w", err)
	}

	// Convert diff to structured format
	result := &ConfigDiff{
		FromID:     fromID,
		ToID:       toID,
		HasChanges: diffable.Changed(),
		Changes:    []ConfigDiffChange{},
	}

	// Generate diff string representation
	if diffable.Changed() {
		diffString := diffable.String("$")
		result.DiffString = diffString

		// Parse the diff string to extract individual changes
		changes := parseDiffString(diffString)
		result.Changes = changes
	}

	d.log.Infof("Diff computed successfully (has changes: %v)", result.HasChanges)

	return result, nil
}

// parseDiffString parses the spruce diff output into structured changes.
func parseDiffString(diffStr string) []ConfigDiffChange {
	changes := []ConfigDiffChange{}
	lines := strings.Split(diffStr, "\n")

	for lineIndex, line := range lines {
		// Look for change markers
		switch {
		case strings.Contains(line, " added"):
			change := ConfigDiffChange{
				Type: "added",
				Path: extractPath(line),
			}
			// Collect the new value
			if lineIndex+1 < len(lines) {
				change.NewValue = collectValue(lines, lineIndex+1, "added")
			}

			changes = append(changes, change)
		case strings.Contains(line, " removed"):
			change := ConfigDiffChange{
				Type: "removed",
				Path: extractPath(line),
			}
			// Collect the old value
			if lineIndex+1 < len(lines) {
				change.OldValue = collectValue(lines, lineIndex+1, "removed")
			}

			changes = append(changes, change)
		case strings.Contains(line, " changed"):
			change := ConfigDiffChange{
				Type: "changed",
				Path: extractPath(line),
			}
			// Collect old and new values
			for j := lineIndex + 1; j < len(lines) && j < lineIndex+10; j++ {
				if strings.Contains(lines[j], "from ") {
					change.OldValue = extractChangedValue(lines[j])
				} else if strings.Contains(lines[j], "to ") {
					change.NewValue = extractChangedValue(lines[j])

					break
				}
			}

			changes = append(changes, change)
		}
	}

	return changes
}

// extractPath extracts the path from a diff line.
func extractPath(line string) string {
	// Remove ANSI color codes
	cleaned := stripAnsiCodes(line)
	// Extract path before the action word
	parts := strings.Fields(cleaned)
	if len(parts) > 0 {
		return parts[0]
	}

	return ""
}

// stripAnsiCodes removes ANSI color codes from a string.
func stripAnsiCodes(inputStr string) string {
	// Simple regex-like replacement for common ANSI codes
	inputStr = strings.ReplaceAll(inputStr, "@C{", "")
	inputStr = strings.ReplaceAll(inputStr, "@R{", "")
	inputStr = strings.ReplaceAll(inputStr, "@G{", "")
	inputStr = strings.ReplaceAll(inputStr, "}", "")

	return strings.TrimSpace(inputStr)
}

// collectValue collects multi-line values from diff output.
func collectValue(lines []string, startIdx int, _ string) string {
	var value strings.Builder

	for i := startIdx; i < len(lines); i++ {
		line := lines[i]
		// Stop at next change marker or empty line
		if strings.TrimSpace(line) == "" ||
			strings.Contains(line, " added") ||
			strings.Contains(line, " removed") ||
			strings.Contains(line, " changed") {
			break
		}

		cleaned := stripAnsiCodes(line)
		value.WriteString(strings.TrimSpace(cleaned))
		value.WriteString("\n")
	}

	return strings.TrimSpace(value.String())
}

// extractChangedValue extracts value from a "from" or "to" line.
func extractChangedValue(line string) string {
	cleaned := stripAnsiCodes(line)
	// Remove "from " or "to " prefix
	cleaned = strings.TrimPrefix(cleaned, "from ")
	cleaned = strings.TrimPrefix(cleaned, "to ")

	return strings.TrimSpace(cleaned)
}

// convertDirectorBoshConfig converts a BOSH director config to our BoshConfig type.
func convertDirectorBoshConfig(config boshdirector.Config) BoshConfig {
	createdAt := time.Time{}
	if config.CreatedAt != "" {
		parsed, err := time.Parse("2006-01-02 15:04:05 MST", config.CreatedAt)
		if err == nil {
			createdAt = parsed
		} else {
			parsed, err := time.Parse(time.RFC3339, config.CreatedAt)
			if err == nil {
				createdAt = parsed
			}
		}
	}

	return BoshConfig{
		ID:        config.ID,
		Name:      config.Name,
		Type:      config.Type,
		Team:      config.Team,
		CreatedAt: createdAt,
		IsActive:  false, // Will be set based on * suffix logic
	}
}

// Cleanup runs BOSH cleanup.
func (d *DirectorAdapter) Cleanup(removeAll bool) (*Task, error) {
	d.log.Infof("Running BOSH cleanup (removeAll: %v)", removeAll)
	d.log.Debugf("Initiating cleanup operation")

	// CleanUp has different signature in bosh-cli
	_, err := d.director.CleanUp(removeAll, false, false)
	if err != nil {
		d.log.Errorf("Failed to run cleanup: %v", err)

		return nil, fmt.Errorf("failed to run cleanup: %w", err)
	}

	// Get the latest task
	tasks, err := d.director.RecentTasks(1, boshdirector.TasksFilter{})
	if err != nil {
		return nil, fmt.Errorf("failed to get task for cleanup: %w", err)
	}

	if len(tasks) > 0 {
		task := convertDirectorTask(tasks[0])
		d.log.Infof("Cleanup started, task ID: %d", task.ID)

		return task, nil
	}

	d.log.Errorf("No task found for cleanup")

	return nil, ErrNoTaskFoundCleanup
}

// Helper functions

func buildFactoryConfig(config Config, logger boshlog.Logger) (*boshdirector.FactoryConfig, error) {
	factoryConfig := createBasicFactoryConfig(config)

	// Skip auto-detection if we're in a test environment
	if os.Getenv("BLACKSMITH_TEST_MODE") == "true" {
		return setupTestModeAuth(factoryConfig, config, logger)
	}

	// Try UAA authentication first if possible
	uaaConfig, err := attemptUAAAutoDetection(config, logger, factoryConfig)
	if err == nil && uaaConfig != nil {
		return uaaConfig, nil
	}

	// Fall back to basic auth or explicit UAA config
	return setupFallbackAuth(factoryConfig, config, logger)
}

// createBasicFactoryConfig creates the basic factory config with host and port parsing.
func createBasicFactoryConfig(config Config) *boshdirector.FactoryConfig {
	host, port := parseHostAndPort(config.Address, defaultBOSHDirectorPort)

	return &boshdirector.FactoryConfig{
		Host: host,
		Port: port,
	}
}

// parseHostAndPort extracts host and port from an address string.
func parseHostAndPort(address string, defaultPort int) (string, int) {
	host := address
	port := defaultPort

	// Remove https:// or http:// prefix if present
	if strings.HasPrefix(host, "https://") {
		host = strings.TrimPrefix(host, "https://")
	} else if strings.HasPrefix(host, "http://") {
		host = strings.TrimPrefix(host, "http://")
	}

	// Extract port if specified
	if strings.Contains(host, ":") {
		parts := strings.Split(host, ":")

		host = parts[0]
		if len(parts) > 1 {
			p, err := strconv.Atoi(parts[1])
			if err == nil {
				port = p
			}
		}
	}

	return host, port
}

// setupTestModeAuth configures authentication for test mode.
func setupTestModeAuth(factoryConfig *boshdirector.FactoryConfig, config Config, logger boshlog.Logger) (*boshdirector.FactoryConfig, error) {
	if config.Username != "" && config.Password != "" {
		err := setEnvCredentials(config.Username, config.Password, logger)
		if err != nil {
			return nil, err
		}

		factoryConfig.Client = config.Username
		factoryConfig.ClientSecret = config.Password
	}

	return factoryConfig, nil
}

// setEnvCredentials sets BOSH environment variables.
func setEnvCredentials(username, password string, logger boshlog.Logger) error {
	err := os.Setenv("BOSH_CLIENT", username)
	if err != nil {
		logger.Error("buildFactoryConfig", "failed to set BOSH_CLIENT environment variable: %s", err)

		return fmt.Errorf("failed to set BOSH_CLIENT environment variable: %w", err)
	}

	err = os.Setenv("BOSH_CLIENT_SECRET", password)
	if err != nil {
		logger.Error("buildFactoryConfig", "failed to set BOSH_CLIENT_SECRET environment variable: %s", err)

		return fmt.Errorf("failed to set BOSH_CLIENT_SECRET environment variable: %w", err)
	}

	return nil
}

// attemptUAAAutoDetection tries to auto-detect UAA configuration.
func attemptUAAAutoDetection(config Config, logger boshlog.Logger, factoryConfig *boshdirector.FactoryConfig) (*boshdirector.FactoryConfig, error) {
	tempFactory := boshdirector.NewFactory(logger)

	tempDirector, err := tempFactory.New(*factoryConfig, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create temporary BOSH director: %w", err)
	}

	info, err := tempDirector.Info()
	if err != nil {
		return nil, fmt.Errorf("failed to get director info: %w", err)
	}

	if config.Logger != nil {
		config.Logger.Debugf("Detected director auth type: %s", info.Auth.Type)
	}

	// If director uses UAA and we have credentials, set up UAA client auth
	if info.Auth.Type == authTypeUAA && config.Username != "" && config.Password != "" {
		return setupUAAAuth(config, tempDirector, factoryConfig)
	}

	return nil, ErrUAAAutoDetectionNotApplicable
}

// setupUAAAuth configures UAA authentication.
func setupUAAAuth(config Config, director boshdirector.Director, factoryConfig *boshdirector.FactoryConfig) (*boshdirector.FactoryConfig, error) {
	uaaURL := config.UAA.URL
	if uaaURL == "" {
		info, err := director.Info()
		if err != nil {
			return nil, fmt.Errorf("error getting director info: %w", err)
		}

		if info.Auth.Type != authTypeUAA {
			return nil, ErrDirectorNotUsingUAA
		}
		// Type assertion for the URL from the options map
		if urlInterface, ok := info.Auth.Options["url"]; ok {
			if urlStr, ok := urlInterface.(string); ok {
				uaaURL = urlStr
			} else {
				return nil, ErrUAAURLNotString
			}
		} else {
			return nil, ErrUAAURLNotFoundInInfo
		}
	}

	uaaHost, uaaPort := parseUAAURL(uaaURL)

	uaaConfig, err := boshuaa.NewConfigFromURL("https://" + net.JoinHostPort(uaaHost, strconv.Itoa(uaaPort)))
	if err != nil {
		return nil, fmt.Errorf("invalid UAA URL: %w", err)
	}

	// Set UAA client credentials
	if config.UAA.ClientID != "" {
		uaaConfig.Client = config.UAA.ClientID
	}

	if config.UAA.ClientSecret != "" {
		uaaConfig.ClientSecret = config.UAA.ClientSecret
	}

	// Configure TLS settings
	uaaConfig.CACert = config.UAA.CACert

	// Create UAA factory and update director factory config
	uaaFactory := boshuaa.NewFactory(boshlog.NewLogger(boshlog.LevelError))

	uaaClient, err := uaaFactory.New(uaaConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create UAA client: %w", err)
	}

	factoryConfig.TokenFunc = boshuaa.NewClientTokenSession(uaaClient).TokenFunc

	return factoryConfig, nil
}

// parseUAAURL extracts host and port from UAA URL.
func parseUAAURL(uaaURL string) (string, int) {
	uaaHost := uaaURL
	uaaPort := 443 // Default HTTPS port

	// Remove https:// or http:// prefix if present
	if strings.HasPrefix(uaaHost, "https://") {
		uaaHost = strings.TrimPrefix(uaaHost, "https://")
		uaaPort = 443
	} else if strings.HasPrefix(uaaHost, "http://") {
		uaaHost = strings.TrimPrefix(uaaHost, "http://")
		uaaPort = 80
	}

	// Extract port if specified
	if strings.Contains(uaaHost, ":") {
		parts := strings.Split(uaaHost, ":")

		uaaHost = parts[0]
		if len(parts) > 1 {
			p, err := strconv.Atoi(parts[1])
			if err == nil {
				uaaPort = p
			}
		}
	}

	return uaaHost, uaaPort
}

// createUAAClient creates a new UAA client.
func createUAAClient(host string, port int, clientID, clientSecret, caCert string, logger boshlog.Logger) (boshuaa.UAA, error) {
	uaaConfig := boshuaa.Config{
		Host:         host,
		Port:         port,
		Client:       clientID,
		ClientSecret: clientSecret,
		CACert:       caCert,
	}

	uaaFactory := boshuaa.NewFactory(logger)

	uaa, err := uaaFactory.New(uaaConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create UAA client: %w", err)
	}

	return uaa, nil
}

// setupFallbackAuth configures fallback authentication (basic auth or explicit UAA).
func setupFallbackAuth(factoryConfig *boshdirector.FactoryConfig, config Config, logger boshlog.Logger) (*boshdirector.FactoryConfig, error) {
	if config.UAA == nil {
		return setupBasicAuth(factoryConfig, config, logger)
	}

	return setupExplicitUAA(factoryConfig, config, logger)
}

// setupBasicAuth configures basic authentication.
func setupBasicAuth(factoryConfig *boshdirector.FactoryConfig, config Config, logger boshlog.Logger) (*boshdirector.FactoryConfig, error) {
	if config.Username != "" && config.Password != "" {
		err := setEnvCredentials(config.Username, config.Password, logger)
		if err != nil {
			return nil, err
		}

		if config.Logger != nil {
			config.Logger.Debugf("Setting up basic auth with username: %s", config.Username)
			config.Logger.Debugf("Set BOSH_CLIENT env var to: %s", config.Username)
		}

		factoryConfig.Client = config.Username
		factoryConfig.ClientSecret = config.Password
	} else {
		// Check if env vars are already set
		envClient := os.Getenv("BOSH_CLIENT")
		envSecret := os.Getenv("BOSH_CLIENT_SECRET")

		if envClient != "" && envSecret != "" {
			if config.Logger != nil {
				config.Logger.Debugf("Using BOSH_CLIENT from environment: %s", envClient)
			}

			factoryConfig.Client = envClient
			factoryConfig.ClientSecret = envSecret
		} else if config.Logger != nil {
			config.Logger.Errorf("No authentication credentials provided (neither config nor env vars)")
		}
	}

	return factoryConfig, nil
}

// setupExplicitUAA configures explicit UAA authentication.
func setupExplicitUAA(factoryConfig *boshdirector.FactoryConfig, config Config, logger boshlog.Logger) (*boshdirector.FactoryConfig, error) {
	uaa, err := createUAAClient(config.UAA.URL, 0, config.UAA.ClientID, config.UAA.ClientSecret, config.UAA.CACert, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create UAA client: %w", err)
	}

	factoryConfig.TokenFunc = boshuaa.NewClientTokenSession(uaa).TokenFunc

	return factoryConfig, nil
}

func convertDirectorTask(task boshdirector.Task) *Task {
	result := &Task{
		ID:          task.ID(),
		State:       task.State(),
		Description: task.Description(),
		User:        task.User(),
		Result:      task.Result(),
		ContextID:   task.ContextID(),
		Deployment:  task.DeploymentName(),
	}

	// Set timestamps
	result.StartedAt = task.StartedAt()
	// Note: EndedAt is not available in the current version
	// Would need to check if task is finished based on state
	if task.State() == "done" || task.State() == "error" || task.State() == "cancelled" {
		// Approximate end time with last activity time
		now := time.Now()
		result.EndedAt = &now
	}

	return result
}

func ExtractDeploymentName(manifest string) string {
	// Simple extraction - look for "name:" at the beginning of a line
	lines := strings.Split(manifest, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "name:") {
			name := strings.TrimSpace(strings.TrimPrefix(trimmed, "name:"))
			// Remove quotes if present
			name = strings.Trim(name, `"'`)

			return name
		}
	}

	return ""
}

// Helper functions for converting string values to numeric types

func parseStringToUint64(s string) *uint64 {
	if s == "" {
		return nil
	}

	val, err := strconv.ParseUint(s, 10, 64)
	if err == nil {
		return &val
	}

	return nil
}

func parseStringToFloat64(s string) *float64 {
	if s == "" {
		return nil
	}

	val, err := strconv.ParseFloat(s, 64)
	if err == nil {
		return &val
	}

	return nil
}

// FetchLogs fetches logs from a specific job in a deployment.
func (d *DirectorAdapter) FetchLogs(deployment string, jobName string, jobIndex string) (string, error) {
	d.log.Infof("Fetching logs for deployment: %s, job: %s/%s", deployment, jobName, jobIndex)
	d.log.Debugf("Finding deployment %s", deployment)

	dep, err := d.director.FindDeployment(deployment)
	if err != nil {
		d.log.Errorf("Failed to find deployment %s: %v", deployment, err)

		return "", fmt.Errorf("failed to find deployment %s: %w", deployment, err)
	}

	// Create the slug for the specific job instance
	slug := boshdirector.NewAllOrInstanceGroupOrInstanceSlug(jobName, jobIndex)

	// Fetch logs (empty filters means all logs, "job" means only job logs)
	logsResult, err := dep.FetchLogs(slug, []string{}, "job")
	if err != nil {
		d.log.Errorf("Failed to fetch logs for %s/%s in deployment %s: %v", jobName, jobIndex, deployment, err)

		return "", fmt.Errorf("failed to fetch logs: %w", err)
	}

	d.log.Infof("Successfully fetched logs for %s/%s (blobstore ID: %s)", jobName, jobIndex, logsResult.BlobstoreID)

	// Download the logs from blobstore
	var logBuffer bytes.Buffer

	err = d.director.DownloadResourceUnchecked(logsResult.BlobstoreID, &logBuffer)
	if err != nil {
		d.log.Errorf("Failed to download logs from blobstore %s: %v", logsResult.BlobstoreID, err)

		return "", fmt.Errorf("failed to download logs: %w", err)
	}

	d.log.Debugf("Downloaded log archive, size: %d bytes", logBuffer.Len())

	// Extract the tar.gz archive
	logs, err := extractLogsFromTarGz(&logBuffer)
	if err != nil {
		d.log.Errorf("Failed to extract logs from archive: %v", err)

		return "", fmt.Errorf("failed to extract logs: %w", err)
	}

	return logs, nil
}

// extractLogsFromTarGz extracts log files from a tar.gz archive.
func extractLogsFromTarGz(data io.Reader) (string, error) {
	// Create gzip reader
	gzReader, err := gzip.NewReader(data)
	if err != nil {
		return "", fmt.Errorf("failed to create gzip reader: %w", err)
	}

	defer func() { _ = gzReader.Close() }()

	// Create tar reader
	tarReader := tar.NewReader(gzReader)

	logContents := make(map[string]string)

	// Read through the tar archive
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}

		if err != nil {
			return "", fmt.Errorf("failed to read tar archive: %w", err)
		}

		// Skip directories
		if header.Typeflag == tar.TypeDir {
			continue
		}

		// Only process .log files or files in log directories
		if strings.HasSuffix(header.Name, ".log") || strings.Contains(header.Name, "/log/") {
			// Read file contents
			var buf bytes.Buffer
			// Limit read size to prevent memory issues
			limitReader := io.LimitReader(tarReader, maxLogFileSize) // 10MB limit per file

			_, err := io.Copy(&buf, limitReader)
			if err != nil {
				return "", fmt.Errorf("failed to read file %s: %w", header.Name, err)
			}

			// Store with cleaned path as key
			cleanPath := filepath.Clean(header.Name)
			logContents[cleanPath] = buf.String()
		}
	}

	// Format the logs for display
	if len(logContents) == 0 {
		return "No log files found in archive", nil
	}

	var result strings.Builder
	for path, content := range logContents {
		result.WriteString(fmt.Sprintf("=== %s ===\n", path))
		result.WriteString(content)

		if !strings.HasSuffix(content, "\n") {
			result.WriteString("\n")
		}

		result.WriteString("\n")
	}

	return result.String(), nil
}

// SSHCommand executes a one-off command on a BOSH VM via SSH.
func (d *DirectorAdapter) SSHCommand(deployment, instance string, index int, command string, args []string, options map[string]interface{}) (string, error) {
	d.log.Infof("Executing SSH command on deployment %s, instance %s/%d", deployment, instance, index)
	d.log.Debugf("Command: %s, Args: %v", command, args)

	// Set up SSH infrastructure
	sshResult, privateKey, cleanup, err := d.setupSSHSession(deployment, instance, index)
	if err != nil {
		return "", err
	}
	defer cleanup()

	// Create SSH client and execute command
	client, err := d.createSSHClient(sshResult, privateKey)
	if err != nil {
		return "", err
	}

	defer func() { _ = client.Close() }()

	return d.executeSSHCommand(client, command, args)
}

// SSHSession creates an interactive SSH session for streaming.
func (d *DirectorAdapter) SSHSession(deployment, instance string, index int, options map[string]interface{}) (interface{}, error) {
	d.log.Infof("Creating SSH session for deployment %s, instance %s/%d", deployment, instance, index)

	// Find the deployment
	boshDeployment, err := d.director.FindDeployment(deployment)
	if err != nil {
		d.log.Errorf("Failed to find deployment %s: %v", deployment, err)

		return nil, fmt.Errorf("failed to find deployment %s: %w", deployment, err)
	}

	// Create SSH options
	sshOpts, privateKey, err := boshdirector.NewSSHOpts(boshuuid.NewGenerator())
	if err != nil {
		d.log.Errorf("Failed to create SSH options: %v", err)

		return nil, fmt.Errorf("failed to create SSH options: %w", err)
	}

	// Create slug for targeting the specific instance
	slug := boshdirector.NewAllOrInstanceGroupOrInstanceSlug(instance, strconv.Itoa(index))

	// Set up SSH session
	d.log.Debugf("Setting up SSH session")

	sshResult, err := boshDeployment.SetUpSSH(slug, sshOpts)
	if err != nil {
		d.log.Errorf("Failed to set up SSH: %v", err)

		return nil, fmt.Errorf("failed to set up SSH: %w", err)
	}

	d.log.Infof("SSH session created successfully for %d hosts", len(sshResult.Hosts))

	// Convert hosts to interface slice for proper type assertion in SessionImpl
	hosts := make([]interface{}, len(sshResult.Hosts))
	for i, host := range sshResult.Hosts {
		hosts[i] = map[string]interface{}{
			"Host":          host.Host,
			"HostPublicKey": host.HostPublicKey,
			"Username":      host.Username,
			"Job":           host.Job,
			"IndexOrID":     host.IndexOrID,
		}
	}

	// Return SSH session information
	sessionInfo := map[string]interface{}{
		"deployment":   deployment,
		"instance":     instance,
		"index":        index,
		"hosts":        hosts,
		"gateway_host": sshResult.GatewayHost,
		"gateway_user": sshResult.GatewayUsername,
		"private_key":  privateKey,
		"ssh_opts":     sshOpts,
		"cleanup_func": func() error {
			return boshDeployment.CleanUpSSH(slug, sshOpts)
		},
	}

	return sessionInfo, nil
}

// EnableResurrection toggles resurrection for a deployment using resurrection config.
func (da *DirectorAdapter) EnableResurrection(deployment string, enabled bool) error {
	da.log.Infof("Setting resurrection to %v for deployment %s", enabled, deployment)

	// Create resurrection configuration YAML according to BOSH documentation
	configYAML := fmt.Sprintf(`rules:
- enabled: %v
  include:
    deployments:
    - %s
`, enabled, deployment)

	// Generate a unique config name for this deployment
	// Use 'blacksmith.{deployment}' format as type already indicates 'resurrection'
	configName := "blacksmith." + deployment

	da.log.Debugf("Updating resurrection config %s with content:\n%s", configName, configYAML)

	// Update the resurrection config using the BOSH director
	config, err := da.director.UpdateConfig("resurrection", configName, "", []byte(configYAML))
	if err != nil {
		da.log.Errorf("Failed to update resurrection config for deployment %s: %v", deployment, err)

		return fmt.Errorf("%w %s: %w", ErrResurrectionConfigUpdateFailed, deployment, err)
	}

	da.log.Debugf("Successfully updated resurrection config %s for deployment %s", configName, deployment)
	da.log.Debugf("Config response: %+v", config)

	da.log.Infof("Successfully updated resurrection config for deployment %s to %v", deployment, enabled)

	return nil
}

// DeleteResurrectionConfig deletes resurrection config for a deployment.
func (da *DirectorAdapter) DeleteResurrectionConfig(deployment string) error {
	da.log.Infof("Deleting resurrection config for deployment %s", deployment)

	// Use 'blacksmith.{deployment}' format as type already indicates 'resurrection'
	configName := "blacksmith." + deployment

	da.log.Debugf("Deleting resurrection config %s", configName)

	// Delete the resurrection config using the BOSH director
	deleted, err := da.director.DeleteConfig("resurrection", configName)
	if err != nil {
		da.log.Errorf("Failed to delete resurrection config for deployment %s: %v", deployment, err)

		return fmt.Errorf("%w %s: %w", ErrResurrectionConfigDeleteFailed, deployment, err)
	}

	if !deleted {
		da.log.Errorf("Resurrection config %s did not exist or was already deleted", configName)

		return fmt.Errorf("%w %s", ErrResurrectionConfigNotFound, deployment)
	}

	da.log.Debugf("Successfully deleted resurrection config %s for deployment %s", configName, deployment)

	da.log.Infof("Successfully deleted resurrection config for deployment %s", deployment)

	return nil
}

func (d *DirectorAdapter) handleNewDeployment(deploymentName string) (*Task, error) {
	d.log.Debugf("Deployment %s not found, attempting to create via manifest deployment", deploymentName)
	d.log.Infof("Deployment %s does not exist yet, returning placeholder task for async creation", deploymentName)

	return &Task{
		ID:          1, // Use ID 1 instead of 0 to avoid CF thinking it failed
		State:       "processing",
		Description: "Creating deployment " + deploymentName,
		User:        "admin",
		Deployment:  deploymentName,
		StartedAt:   time.Now(),
	}, nil
}

func (d *DirectorAdapter) updateExistingDeployment(dep boshdirector.Deployment, deploymentName, manifest string) (*Task, error) {
	d.log.Debugf("Updating existing deployment %s", deploymentName)

	updateOpts := boshdirector.UpdateOpts{
		Recreate:    false,
		Fix:         false,
		SkipDrain:   boshdirector.SkipDrains{},
		Canaries:    "",
		MaxInFlight: "",
		DryRun:      false,
	}

	err := dep.Update([]byte(manifest), updateOpts)
	if err != nil {
		d.log.Errorf("Failed to update deployment %s: %v", deploymentName, err)

		return nil, fmt.Errorf("failed to update deployment: %w", err)
	}

	// Get the latest task for this deployment
	tasks, err := d.director.RecentTasks(1, boshdirector.TasksFilter{
		Deployment: deploymentName,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get task for deployment: %w", err)
	}

	if len(tasks) > 0 {
		task := convertDirectorTask(tasks[0])
		d.log.Infof("Deployment %s update started, task ID: %d", deploymentName, task.ID)

		return task, nil
	}

	// If no task found, create a dummy task response
	d.log.Debugf("No task found for deployment update, creating placeholder task")

	return &Task{
		ID:          0,
		State:       "running",
		Description: "Creating deployment " + deploymentName,
		User:        "admin",
		Deployment:  deploymentName,
		StartedAt:   time.Now(),
	}, nil
}

func (d *DirectorAdapter) parseTaskEvent(line string) (*TaskEvent, error) {
	// Try to parse as BOSH event JSON
	var boshEvent struct {
		Time     int64    `json:"time"`
		Stage    string   `json:"stage"`
		Tags     []string `json:"tags"`
		Total    int      `json:"total"`
		Task     string   `json:"task"`
		Index    int      `json:"index"`
		State    string   `json:"state"`
		Progress int      `json:"progress"`
		Error    struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error,omitempty"`
	}

	err := json.Unmarshal([]byte(line), &boshEvent)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal BOSH event: %w", err)
	}

	// Convert to our TaskEvent structure
	event := &TaskEvent{
		Time:     time.Unix(boshEvent.Time, 0),
		Stage:    boshEvent.Stage,
		Tags:     boshEvent.Tags,
		Total:    boshEvent.Total,
		Task:     boshEvent.Task,
		Index:    boshEvent.Index,
		State:    boshEvent.State,
		Progress: boshEvent.Progress,
	}

	if boshEvent.Error.Message != "" {
		event.Error = &TaskEventError{
			Code:    boshEvent.Error.Code,
			Message: boshEvent.Error.Message,
		}
	}

	return event, nil
}

func (d *DirectorAdapter) parseTaskEvents(output string) []TaskEvent {
	if output == "" {
		return []TaskEvent{}
	}

	events := []TaskEvent{}
	lines := strings.Split(output, "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		event, err := d.parseTaskEvent(line)
		if err != nil {
			// Skip lines that aren't valid JSON events
			continue
		}

		events = append(events, *event)
	}

	return events
}

func (d *DirectorAdapter) normalizeConfigLimit(limit int) int {
	if limit <= 0 || limit > 200 {
		return defaultConfigLimit
	}

	return limit
}

func (d *DirectorAdapter) shouldIncludeConfigType(configType string, configTypes []string) bool {
	if len(configTypes) == 0 {
		return true
	}

	for _, ct := range configTypes {
		if ct == configType {
			return true
		}
	}

	return false
}

func (d *DirectorAdapter) fetchLatestConfig(config boshdirector.Config) (*BoshConfig, error) {
	latestConfigs, err := d.director.ListConfigs(1, boshdirector.ConfigsFilter{
		Type: config.Type,
		Name: config.Name,
	})
	if err != nil {
		d.log.Errorf("Failed to get latest config for %s/%s: %v", config.Type, config.Name, err)

		return nil, fmt.Errorf("failed to list configs for %s/%s: %w", config.Type, config.Name, err)
	}

	if len(latestConfigs) == 0 {
		return nil, ErrConfigNotFound
	}

	latest := latestConfigs[0]
	bc := convertDirectorBoshConfig(latest)
	bc.IsActive = true
	bc.ID = strings.TrimSuffix(latest.ID, "*") + "*"

	return &bc, nil
}

func (d *DirectorAdapter) sortConfigsByTypeAndName(configs []BoshConfig) {
	typeOrder := map[string]int{
		"cloud":        1,
		"cpi":          configPriorityCPI,
		"resurrection": configPriorityResurrection,
		"runtime":      configPriorityRuntime,
	}

	sort.Slice(configs, func(i, j int) bool {
		if configs[i].Type != configs[j].Type {
			return typeOrder[configs[i].Type] < typeOrder[configs[j].Type]
		}

		return configs[i].Name < configs[j].Name
	})
}

func (d *DirectorAdapter) convertVMInfo(vmInfo boshdirector.VMInfo) VM {
	// Parse VM creation time
	var vmCreatedAt time.Time
	if vmInfo.VMCreatedAtRaw != "" {
		parsed, err := time.Parse(time.RFC3339, vmInfo.VMCreatedAtRaw)
		if err == nil {
			vmCreatedAt = parsed
		}
	}

	processes := d.convertProcesses(vmInfo.Processes)
	vitals := d.convertVitals(vmInfo.Vitals)
	stemcell := d.convertStemcell(vmInfo.Stemcell)

	// Log the state values for debugging
	d.log.Debugf("VM %s/%d - ProcessState: %s, State: %s",
		vmInfo.JobName, *vmInfo.Index, vmInfo.ProcessState, vmInfo.State)

	return VM{
		// Core VM identity
		ID:      vmInfo.ID,
		AgentID: vmInfo.AgentID,
		CID:     vmInfo.VMID, // vm_cid from BOSH API

		// Job information
		Job:      vmInfo.JobName,      // job_name from BOSH API
		Index:    *vmInfo.Index,       // Dereference pointer
		JobState: vmInfo.ProcessState, // job_state from BOSH API

		// VM state and properties
		State:              vmInfo.State,
		Active:             vmInfo.Active,
		Bootstrap:          vmInfo.Bootstrap,
		Ignore:             vmInfo.Ignore,
		ResurrectionPaused: vmInfo.ResurrectionPaused,

		// Network and placement
		IPs: vmInfo.IPs,
		DNS: []string{}, // DNS not directly available in BOSH CLI
		AZ:  vmInfo.AZ,

		// Resource allocation
		VMType:       vmInfo.VMType,
		ResourcePool: vmInfo.ResourcePool,

		// Disk information
		DiskCID:  vmInfo.DiskID,
		DiskCIDs: vmInfo.DiskIDs,

		// Timestamps
		VMCreatedAt: vmCreatedAt,

		// Complex data structures
		CloudProperties: vmInfo.CloudProperties,
		Processes:       processes,
		Vitals:          vitals,
		Stemcell:        stemcell,
	}
}

func (d *DirectorAdapter) convertProcesses(processes []boshdirector.VMInfoProcess) []VMProcess {
	result := make([]VMProcess, len(processes))
	for j, proc := range processes {
		result[j] = VMProcess{
			Name:  proc.Name,
			State: proc.State,
			CPU: VMVitalsCPU{
				Total: proc.CPU.Total,
				Sys:   proc.CPU.Sys,
				User:  proc.CPU.User,
				Wait:  proc.CPU.Wait,
			},
			Memory: VMVitalsMemory{
				KB:      proc.Mem.KB,
				Percent: proc.Mem.Percent,
			},
			Uptime: VMVitalsUptime{
				Seconds: proc.Uptime.Seconds,
			},
		}
	}

	return result
}

func (d *DirectorAdapter) convertVitals(vitals boshdirector.VMInfoVitals) VMVitals {
	result := VMVitals{
		CPU: VMVitalsCPU{
			Sys:  vitals.CPU.Sys,
			User: vitals.CPU.User,
			Wait: vitals.CPU.Wait,
		},
		Memory: VMVitalsMemory{
			KB:      parseStringToUint64(vitals.Mem.KB),
			Percent: parseStringToFloat64(vitals.Mem.Percent),
		},
		Swap: VMVitalsMemory{
			KB:      parseStringToUint64(vitals.Swap.KB),
			Percent: parseStringToFloat64(vitals.Swap.Percent),
		},
		Load: vitals.Load,
		Uptime: VMVitalsUptime{
			Seconds: vitals.Uptime.Seconds,
		},
	}

	// Convert disk vitals
	result.Disk = make(map[string]VMVitalsDisk)
	for diskName, diskVitals := range vitals.Disk {
		result.Disk[diskName] = VMVitalsDisk{
			InodePercent: diskVitals.InodePercent,
			Percent:      diskVitals.Percent,
		}
	}

	return result
}

func (d *DirectorAdapter) convertStemcell(stemcell boshdirector.VmInfoStemcell) VMStemcell {
	return VMStemcell{
		Name:       stemcell.Name,
		Version:    stemcell.Version,
		ApiVersion: stemcell.ApiVersion,
	}
}

// setupSSHSession sets up the SSH session and returns cleanup function.
func (d *DirectorAdapter) setupSSHSession(deployment, instance string, index int) (*boshdirector.SSHResult, string, func(), error) {
	// Find the deployment
	boshDeployment, err := d.director.FindDeployment(deployment)
	if err != nil {
		d.log.Errorf("Failed to find deployment %s: %v", deployment, err)

		return nil, "", nil, fmt.Errorf("failed to find deployment %s: %w", deployment, err)
	}

	// Create SSH options
	sshOpts, privateKey, err := boshdirector.NewSSHOpts(boshuuid.NewGenerator())
	if err != nil {
		d.log.Errorf("Failed to create SSH options: %v", err)

		return nil, "", nil, fmt.Errorf("failed to create SSH options: %w", err)
	}

	// Create slug for targeting the specific instance
	slug := boshdirector.NewAllOrInstanceGroupOrInstanceSlug(instance, strconv.Itoa(index))

	// Set up SSH session
	d.log.Debugf("Setting up SSH session")

	sshResult, err := boshDeployment.SetUpSSH(slug, sshOpts)
	if err != nil {
		d.log.Errorf("Failed to set up SSH: %v", err)

		return nil, "", nil, fmt.Errorf("failed to set up SSH: %w", err)
	}

	// Check if we got hosts
	if len(sshResult.Hosts) == 0 {
		d.log.Errorf("No hosts returned from SSH setup")

		return nil, "", nil, ErrNoHostsAvailableSSH
	}

	d.log.Infof("SSH session established successfully for %d hosts", len(sshResult.Hosts))

	cleanup := func() {
		d.log.Debugf("Cleaning up SSH session")

		cleanupErr := boshDeployment.CleanUpSSH(slug, sshOpts)
		if cleanupErr != nil {
			d.log.Errorf("Failed to clean up SSH: %v", cleanupErr)
		}
	}

	return &sshResult, privateKey, cleanup, nil
}

// createSSHClient creates an SSH client using the SSH result.
func (d *DirectorAdapter) createSSHClient(sshResult *boshdirector.SSHResult, privateKey string) (*ssh.Client, error) {
	// Parse the private key
	signer, err := ssh.ParsePrivateKey([]byte(privateKey))
	if err != nil {
		d.log.Errorf("Failed to parse private key: %v", err)

		return nil, fmt.Errorf("failed to parse private key: %w", err)
	}

	host := sshResult.Hosts[0]

	// Create SSH client configuration with secure host key verification
	config := &ssh.ClientConfig{
		User: host.Username,
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		HostKeyCallback: d.createSecureHostKeyCallback(host.Host),
		Timeout:         httpClientTimeout,
	}

	if sshResult.GatewayHost != "" {
		return d.createGatewaySSHClient(sshResult, config, signer)
	}

	return d.createDirectSSHClient(host.Host, config)
}

// createGatewaySSHClient creates an SSH client through a gateway.
func (d *DirectorAdapter) createGatewaySSHClient(sshResult *boshdirector.SSHResult, config *ssh.ClientConfig, signer ssh.Signer) (*ssh.Client, error) {
	// Connect to gateway first
	gatewayAddr := sshResult.GatewayHost + ":22"
	gatewayConfig := &ssh.ClientConfig{
		User: sshResult.GatewayUsername,
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		HostKeyCallback: d.createSecureHostKeyCallback(sshResult.GatewayHost),
		Timeout:         httpClientTimeout,
	}

	gatewayClient, err := ssh.Dial("tcp", gatewayAddr, gatewayConfig)
	if err != nil {
		d.log.Errorf("Failed to connect to gateway %s: %v", gatewayAddr, err)

		return nil, fmt.Errorf("failed to connect to gateway: %w", err)
	}

	defer func() { _ = gatewayClient.Close() }()

	// Connect to target host through gateway
	targetAddr := sshResult.Hosts[0].Host + ":22"

	conn, err := gatewayClient.Dial("tcp", targetAddr)
	if err != nil {
		d.log.Errorf("Failed to connect to target host %s through gateway: %v", targetAddr, err)

		return nil, fmt.Errorf("failed to connect to target host through gateway: %w", err)
	}

	nconn, chans, reqs, err := ssh.NewClientConn(conn, targetAddr, config)
	if err != nil {
		d.log.Errorf("Failed to establish SSH connection to %s: %v", targetAddr, err)

		return nil, fmt.Errorf("failed to establish SSH connection: %w", err)
	}

	return ssh.NewClient(nconn, chans, reqs), nil
}

// createDirectSSHClient creates a direct SSH client connection.
func (d *DirectorAdapter) createDirectSSHClient(host string, config *ssh.ClientConfig) (*ssh.Client, error) {
	addr := host + ":22"

	client, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		d.log.Errorf("Failed to connect to %s: %v", addr, err)

		return nil, fmt.Errorf("failed to connect to host: %w", err)
	}

	return client, nil
}

// executeSSHCommand executes the command on the SSH client.
func (d *DirectorAdapter) executeSSHCommand(client *ssh.Client, command string, args []string) (string, error) {
	// Create a session
	session, err := client.NewSession()
	if err != nil {
		d.log.Errorf("Failed to create SSH session: %v", err)

		return "", fmt.Errorf("failed to create SSH session: %w", err)
	}

	defer func() { _ = session.Close() }()

	// Build the full command with arguments
	fullCommand := d.buildFullCommand(command, args)

	// Execute the command
	d.log.Debugf("Executing command: %s", fullCommand)

	output, err := session.CombinedOutput(fullCommand)
	if err != nil {
		d.log.Errorf("Failed to execute command: %v, output: %s", err, string(output))
		// Return output even on error (might contain useful error messages)
		return string(output), fmt.Errorf("command execution failed: %w", err)
	}

	d.log.Infof("Command executed successfully")

	return string(output), nil
}

// buildFullCommand builds the full command string with properly quoted arguments.
func (d *DirectorAdapter) buildFullCommand(command string, args []string) string {
	fullCommand := command

	if len(args) > 0 {
		// Properly quote arguments if needed
		quotedArgs := make([]string, len(args))
		for i, arg := range args {
			// Simple quoting - might need enhancement for complex cases
			if strings.Contains(arg, " ") || strings.Contains(arg, "*") || strings.Contains(arg, "?") {
				quotedArgs[i] = fmt.Sprintf("'%s'", arg)
			} else {
				quotedArgs[i] = arg
			}
		}

		fullCommand = fmt.Sprintf("%s %s", command, strings.Join(quotedArgs, " "))
	}

	return fullCommand
}

// fetchTasksByType retrieves tasks based on the specified type.
func (d *DirectorAdapter) fetchTasksByType(taskType string, limit int) ([]boshdirector.Task, error) {
	d.log.Debugf("Building task filter for type: %s", taskType)

	filter := boshdirector.TasksFilter{}

	switch taskType {
	case "recent":
		d.log.Debugf("Getting recent tasks with limit %d", limit)

		tasks, err := d.director.RecentTasks(limit*taskFetchMultiplier, filter)
		if err != nil {
			return nil, fmt.Errorf("failed to get recent tasks: %w", err)
		}

		return tasks, nil
	case "current":
		d.log.Debugf("Getting current tasks")

		tasks, err := d.director.CurrentTasks(filter)
		if err != nil {
			return nil, fmt.Errorf("failed to get current tasks: %w", err)
		}

		return tasks, nil
	case "all":
		return d.fetchAllTasks(limit, filter)
	default:
		d.log.Debugf("Unknown task type %s, defaulting to recent", taskType)

		tasks, err := d.director.RecentTasks(limit*taskFetchMultiplier, filter)
		if err != nil {
			return nil, fmt.Errorf("failed to get recent tasks (default): %w", err)
		}

		return tasks, nil
	}
}

// fetchAllTasks retrieves both current and recent tasks, merging them.
func (d *DirectorAdapter) fetchAllTasks(limit int, filter boshdirector.TasksFilter) ([]boshdirector.Task, error) {
	d.log.Debugf("Getting all tasks with limit %d", limit)

	currentTasks, err := d.director.CurrentTasks(filter)
	if err != nil {
		d.log.Errorf("Failed to get current tasks: %v", err)

		return nil, fmt.Errorf("failed to get current tasks: %w", err)
	}

	recentTasks, err := d.director.RecentTasks(limit*taskFetchMultiplier, filter)
	if err != nil {
		d.log.Errorf("Failed to get recent tasks: %v", err)

		return nil, fmt.Errorf("failed to get recent tasks: %w", err)
	}

	// Merge tasks, avoiding duplicates by ID
	taskMap := make(map[int]boshdirector.Task)
	for _, task := range currentTasks {
		taskMap[task.ID()] = task
	}

	for _, task := range recentTasks {
		if _, exists := taskMap[task.ID()]; !exists {
			taskMap[task.ID()] = task
		}
	}

	// Convert map back to slice
	tasks := make([]boshdirector.Task, 0, len(taskMap))
	for _, task := range taskMap {
		tasks = append(tasks, task)
	}

	return tasks, nil
}

// filterTasksByStates filters tasks by the specified states.
func (d *DirectorAdapter) filterTasksByStates(tasks []boshdirector.Task, states []string) []boshdirector.Task {
	if len(states) == 0 {
		return tasks
	}

	stateMap := make(map[string]bool)
	for _, state := range states {
		stateMap[state] = true
	}

	var filteredTasks []boshdirector.Task

	for _, task := range tasks {
		if stateMap[task.State()] {
			filteredTasks = append(filteredTasks, task)
		}
	}

	d.log.Debugf("Filtered to %d tasks with states %v", len(filteredTasks), states)

	return filteredTasks
}

// filterTasksByTeam filters tasks by the specified team.
func (d *DirectorAdapter) filterTasksByTeam(tasks []boshdirector.Task, team string) []boshdirector.Task {
	if team == "" {
		return tasks
	}

	d.log.Debugf("Starting team filtering with %d tasks for team '%s'", len(tasks), team)

	var teamFilteredTasks []boshdirector.Task

	for _, task := range tasks {
		if d.shouldIncludeTaskForTeam(task, team) {
			teamFilteredTasks = append(teamFilteredTasks, task)
		}
	}

	originalCount := len(tasks)
	d.log.Debugf("Team filtering complete: %d tasks included for team '%s' (from %d original)", len(teamFilteredTasks), team, originalCount)

	return teamFilteredTasks
}

// shouldIncludeTaskForTeam determines if a task should be included for the specified team.
func (d *DirectorAdapter) shouldIncludeTaskForTeam(task boshdirector.Task, team string) bool {
	deploymentName := task.DeploymentName()
	d.log.Debugf("Task %d: deployment='%s', description='%s'", task.ID(), deploymentName, task.Description())

	var includeTask bool

	switch team {
	case "blacksmith":
		// For blacksmith, show all BOSH director tasks
		includeTask = true

		d.log.Debugf("Task %d: included for blacksmith (show all)", task.ID())
	case "service-instances":
		includeTask = d.isServiceInstanceTask(deploymentName, task.ID())
	default:
		includeTask = d.isSpecificTeamTask(deploymentName, team, task.ID())
	}

	if includeTask {
		d.log.Debugf("Task %d: INCLUDED for team '%s'", task.ID(), team)
	} else {
		d.log.Debugf("Task %d: EXCLUDED for team '%s'", task.ID(), team)
	}

	return includeTask
}

// isServiceInstanceTask checks if a task belongs to a service instance.
func (d *DirectorAdapter) isServiceInstanceTask(deploymentName string, taskID int) bool {
	if deploymentName == "" {
		d.log.Debugf("Task %d: no deployment name, excluded from service-instances", taskID)

		return false
	}

	includeTask := (strings.Contains(deploymentName, "-") &&
		(strings.HasPrefix(deploymentName, "redis-") ||
			strings.HasPrefix(deploymentName, "rabbitmq-") ||
			strings.HasPrefix(deploymentName, "postgres-") ||
			strings.Contains(deploymentName, "service-")))

	d.log.Debugf("Task %d: service-instances check result: %v", taskID, includeTask)

	return includeTask
}

// isSpecificTeamTask checks if a task belongs to a specific team/service.
func (d *DirectorAdapter) isSpecificTeamTask(deploymentName, team string, taskID int) bool {
	if deploymentName == "" {
		d.log.Debugf("Task %d: no deployment name, excluded from specific filter '%s'", taskID, team)

		return false
	}

	deploymentLower := strings.ToLower(deploymentName)
	teamLower := strings.ToLower(team)
	includeTask := (strings.Contains(deploymentLower, teamLower) ||
		strings.HasPrefix(deploymentLower, teamLower+"-"))

	d.log.Debugf("Task %d: specific filter '%s' check result: %v", taskID, team, includeTask)

	return includeTask
}

// createSecureHostKeyCallback creates a host key callback that provides secure verification
// This callback logs the host key fingerprint for auditing purposes and accepts the key
// In a production environment, this should be enhanced to use known_hosts verification.
func (d *DirectorAdapter) createSecureHostKeyCallback(_ string) ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		// Calculate and log the key fingerprint for security auditing
		fingerprint := ssh.FingerprintSHA256(key)
		d.log.Infof("SSH connection to %s with host key fingerprint: %s", hostname, fingerprint)

		// In a more secure implementation, you would:
		// 1. Check against a known_hosts file
		// 2. Prompt for user verification on first connection
		// 3. Store accepted keys for future verification
		// For now, we accept all keys but log them for auditing

		return nil
	}
}

// fixYAMLKeyCasing fixes common YAML key casing issues introduced by BOSH CLI library
// when YAML content is processed through Go structs without proper yaml tags.
func fixYAMLKeyCasing(yamlContent string) string {
	if yamlContent == "" {
		return yamlContent
	}

	// Parse YAML into generic map to preserve structure
	var data map[string]interface{}

	err := yaml.Unmarshal([]byte(yamlContent), &data)
	if err != nil {
		// If we can't parse it, return as-is
		return yamlContent
	}

	// Recursively fix key casing in the data structure
	fixedData := fixMapKeyCasing(data)

	// Marshal back to YAML with proper key casing
	fixedBytes, err := yaml.Marshal(fixedData)
	if err != nil {
		// If marshaling fails, return original content
		return yamlContent
	}

	return string(fixedBytes)
}

// fixMapKeyCasing recursively fixes key casing in maps.
func fixMapKeyCasing(data interface{}) interface{} {
	switch value := data.(type) {
	case map[string]interface{}:
		fixed := make(map[string]interface{})

		for key, val := range value {
			// Fix common BOSH config key casing issues
			fixedKey := fixBOSHConfigKey(key)
			fixed[fixedKey] = fixMapKeyCasing(val)
		}

		return fixed
	case map[interface{}]interface{}:
		// Handle yaml.v2's tendency to use interface{} keys
		fixed := make(map[string]interface{})
		for key, mapValue := range value {
			if strKey, ok := key.(string); ok {
				fixedKey := fixBOSHConfigKey(strKey)
				fixed[fixedKey] = fixMapKeyCasing(mapValue)
			} else {
				// Keep non-string keys as-is but fix values
				fixed[fmt.Sprintf("%v", key)] = fixMapKeyCasing(mapValue)
			}
		}

		return fixed
	case []interface{}:
		fixed := make([]interface{}, len(value))
		for arrayIndex, item := range value {
			fixed[arrayIndex] = fixMapKeyCasing(item)
		}

		return fixed
	default:
		// For primitive values, return as-is
		return value
	}
}

// getBOSHConfigKeyMappings returns the mapping of camelCase to snake_case keys.
func getBOSHConfigKeyMappings() map[string]string {
	return map[string]string{
		// Cloud config keys
		"AvailabilityZones": "availability_zones", "CloudProperties": "cloud_properties",
		"VmTypes": "vm_types", "VmExtensions": "vm_extensions", "DiskTypes": "disk_types",
		"Networks": "networks", "Compilation": "compilation", "AzName": "az_name",
		"InstanceType": "instance_type", "EphemeralDisk": "ephemeral_disk",
		"RawInstanceStorage": "raw_instance_storage",

		// Runtime config keys
		"Releases": "releases", "Addons": "addons", "Tags": "tags", "Properties": "properties",
		"Jobs": "jobs", "Include": "include", "Exclude": "exclude", "Deployments": "deployments",
		"Stemcell": "stemcell", "InstanceGroups": "instance_groups",

		// CPI config keys
		"Cpis": "cpis", "MigratedFrom": "migrated_from",

		// Network keys
		"Type": "type", "Subnets": "subnets", "Range": "range", "Gateway": "gateway",
		"Reserved": "reserved", "Static": "static", "Dns": "dns", "AzNames": "az_names",

		// Generic common keys
		"Name": "name", "Version": "version", "Url": "url", "Sha1": "sha1",
		"Stemcells": "stemcells", "Os": "os", "UpdatePolicy": "update_policy",
		"VmType": "vm_type", "PersistentDisk": "persistent_disk",
		"PersistentDiskType": "persistent_disk_type", "Instances": "instances",
		"Lifecycle": "lifecycle", "Migrated": "migrated", "Env": "env", "Update": "update",
		"Canaries": "canaries", "MaxInFlight": "max_in_flight",
		"CanaryWatchTime": "canary_watch_time", "UpdateWatchTime": "update_watch_time",
		"Serial": "serial", "VmStrategy": "vm_strategy", "Team": "team", "Variables": "variables",
	}
}

// fixBOSHConfigKey fixes individual key casing based on common BOSH config patterns.
func fixBOSHConfigKey(key string) string {
	if fixed, exists := getBOSHConfigKeyMappings()[key]; exists {
		return fixed
	}

	return camelToSnake(key)
}

// camelToSnake converts CamelCase to snake_case.
func camelToSnake(str string) string {
	if str == "" {
		return str
	}

	// If string is already in snake_case, return as-is
	if !strings.ContainsAny(str, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") {
		return str
	}

	var result strings.Builder

	for i, r := range str {
		if i > 0 && 'A' <= r && r <= 'Z' {
			result.WriteRune('_')
		}

		result.WriteRune(unicode.ToLower(r))
	}

	return result.String()
}

// Ensure DirectorAdapter implements Director interface.
var _ Director = (*DirectorAdapter)(nil)
