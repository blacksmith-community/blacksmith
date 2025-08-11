package bosh

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	boshdirector "github.com/cloudfoundry/bosh-cli/v7/director"
	boshuaa "github.com/cloudfoundry/bosh-cli/v7/uaa"
	boshlog "github.com/cloudfoundry/bosh-utils/logger"
)

// DirectorAdapter wraps bosh-cli director to implement Director interface
type DirectorAdapter struct {
	director boshdirector.Director
	logger   boshlog.Logger
	log      Logger // Application logger for Info/Debug logging
}

// Logger interface for application logging
type Logger interface {
	Info(format string, args ...interface{})
	Debug(format string, args ...interface{})
	Error(format string, args ...interface{})
}

// BufferedTaskReporter implements TaskReporter to capture task output
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

// NewDirectorAdapter creates a new bosh-cli based Director
func NewDirectorAdapter(config Config) (Director, error) {
	// Use the provided logger or create a no-op logger
	var appLogger Logger
	if config.Logger != nil {
		appLogger = config.Logger
	} else {
		// Create a no-op logger if none provided
		appLogger = &noOpLogger{}
	}

	appLogger.Info("Creating new BOSH director adapter")
	appLogger.Debug("Director address: %s", config.Address)

	logger := boshlog.NewLogger(boshlog.LevelError)

	// Create factory config
	appLogger.Debug("Building factory configuration")
	factoryConfig, err := buildFactoryConfig(config, logger)
	if err != nil {
		appLogger.Error("Failed to build factory config: %v", err)
		return nil, fmt.Errorf("failed to build factory config: %w", err)
	}

	// Create director factory
	appLogger.Debug("Creating director factory")
	factory := boshdirector.NewFactory(logger)

	// Create task reporter and file reporter
	// Use the built-in no-op reporters from the director package
	taskReporter := boshdirector.NoopTaskReporter{}
	fileReporter := boshdirector.NoopFileReporter{}

	// Create director with authentication
	appLogger.Debug("Creating director client")
	director, err := factory.New(*factoryConfig, taskReporter, fileReporter)
	if err != nil {
		appLogger.Error("Failed to create director: %v", err)
		return nil, fmt.Errorf("failed to create director: %w", err)
	}

	appLogger.Info("Successfully created BOSH director adapter")
	return &DirectorAdapter{
		director: director,
		logger:   logger,
		log:      appLogger,
	}, nil
}

// noOpLogger is a no-op implementation of Logger interface
type noOpLogger struct{}

func (n *noOpLogger) Info(format string, args ...interface{})  {}
func (n *noOpLogger) Debug(format string, args ...interface{}) {}
func (n *noOpLogger) Error(format string, args ...interface{}) {}

// GetInfo retrieves BOSH director information
func (d *DirectorAdapter) GetInfo() (*Info, error) {
	d.log.Info("Getting BOSH director information")
	d.log.Debug("Calling director.Info()")

	info, err := d.director.Info()
	if err != nil {
		d.log.Error("Failed to get director info: %v", err)
		return nil, fmt.Errorf("failed to get info: %w", err)
	}

	d.log.Debug("Director info retrieved - Name: %s, UUID: %s, Version: %s", info.Name, info.UUID, info.Version)

	features := make(map[string]bool)
	for feature, enabled := range info.Features {
		features[feature] = enabled
	}

	d.log.Info("Successfully retrieved director info: %s (%s)", info.Name, info.Version)
	return &Info{
		Name:     info.Name,
		UUID:     info.UUID,
		Version:  info.Version,
		User:     info.User,
		CPI:      info.CPI,
		Features: features,
	}, nil
}

// GetDeployments lists all deployments
func (d *DirectorAdapter) GetDeployments() ([]Deployment, error) {
	d.log.Info("Listing all deployments")
	d.log.Debug("Calling director.Deployments()")

	deps, err := d.director.Deployments()
	if err != nil {
		d.log.Error("Failed to get deployments: %v", err)
		return nil, fmt.Errorf("failed to get deployments: %w", err)
	}

	d.log.Debug("Found %d deployments", len(deps))

	deployments := make([]Deployment, len(deps))
	for i, dep := range deps {
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

		deployments[i] = Deployment{
			Name:        dep.Name(),
			CloudConfig: cloudConfig,
			Releases:    releases,
			Stemcells:   stemcells,
			Teams:       teams,
		}
	}

	d.log.Info("Successfully retrieved %d deployments", len(deployments))
	return deployments, nil
}

// GetDeployment retrieves a specific deployment
func (d *DirectorAdapter) GetDeployment(name string) (*DeploymentDetail, error) {
	d.log.Info("Getting deployment: %s", name)
	d.log.Debug("Finding deployment %s", name)

	dep, err := d.director.FindDeployment(name)
	if err != nil {
		d.log.Error("Failed to find deployment %s: %v", name, err)
		return nil, fmt.Errorf("failed to get deployment %s: %w", name, err)
	}

	d.log.Debug("Retrieving manifest for deployment %s", name)
	manifest, err := dep.Manifest()
	if err != nil {
		d.log.Error("Failed to get manifest for deployment %s: %v", name, err)
		return nil, fmt.Errorf("failed to get manifest for deployment %s: %w", name, err)
	}

	d.log.Info("Successfully retrieved deployment %s (manifest size: %d bytes)", name, len(manifest))
	return &DeploymentDetail{
		Name:     name,
		Manifest: manifest,
	}, nil
}

// CreateDeployment creates a new deployment
func (d *DirectorAdapter) CreateDeployment(manifest string) (*Task, error) {
	// Parse deployment name from manifest
	deploymentName := extractDeploymentName(manifest)
	if deploymentName == "" {
		d.log.Error("Could not extract deployment name from manifest")
		return nil, fmt.Errorf("could not extract deployment name from manifest")
	}

	d.log.Info("Creating/updating deployment: %s", deploymentName)
	d.log.Debug("Manifest size: %d bytes", len(manifest))

	// Try to find existing deployment
	d.log.Debug("Checking if deployment %s exists", deploymentName)
	dep, err := d.director.FindDeployment(deploymentName)
	if err != nil {
		// Deployment doesn't exist
		// In bosh-cli, there's no direct "create" method
		// Deployments are created by updating with a manifest
		// We'll return an error for now and document this limitation
		d.log.Error("Deployment %s not found; bosh-cli adapter requires deployment to exist before update", deploymentName)
		return nil, fmt.Errorf("deployment %s not found; bosh-cli adapter requires deployment to exist before update", deploymentName)
	}

	// Update existing deployment with new manifest
	d.log.Debug("Updating deployment %s with new manifest", deploymentName)
	updateOpts := boshdirector.UpdateOpts{
		Recreate:    false,
		Fix:         false,
		SkipDrain:   boshdirector.SkipDrains{},
		Canaries:    "",
		MaxInFlight: "",
		DryRun:      false,
	}

	err = dep.Update([]byte(manifest), updateOpts)
	if err != nil {
		d.log.Error("Failed to update deployment %s: %v", deploymentName, err)
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
		d.log.Info("Deployment %s update started, task ID: %d", deploymentName, task.ID)
		return task, nil
	}

	// If no task found, create a dummy task response
	d.log.Debug("No task found for deployment update, creating placeholder task")
	return &Task{
		ID:          0,
		State:       "running",
		Description: fmt.Sprintf("Creating deployment %s", deploymentName),
		User:        "admin",
		Deployment:  deploymentName,
		StartedAt:   time.Now(),
	}, nil
}

// DeleteDeployment deletes a deployment
func (d *DirectorAdapter) DeleteDeployment(name string) (*Task, error) {
	d.log.Info("Deleting deployment: %s", name)
	d.log.Debug("Finding deployment %s for deletion", name)

	dep, err := d.director.FindDeployment(name)
	if err != nil {
		d.log.Error("Failed to find deployment %s: %v", name, err)
		return nil, fmt.Errorf("failed to find deployment %s: %w", name, err)
	}

	// Delete the deployment (force = false)
	d.log.Debug("Initiating deletion of deployment %s (force=false)", name)
	err = dep.Delete(false)
	if err != nil {
		d.log.Error("Failed to delete deployment %s: %v", name, err)
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
		d.log.Info("Deployment %s deletion started, task ID: %d", name, task.ID)
		return task, nil
	}

	d.log.Error("No task found for deployment %s deletion", name)
	return nil, fmt.Errorf("no task found for deployment deletion")
}

// GetDeploymentVMs retrieves VMs for a deployment
func (d *DirectorAdapter) GetDeploymentVMs(deployment string) ([]VM, error) {
	d.log.Info("Getting VMs for deployment: %s", deployment)
	d.log.Debug("Finding deployment %s", deployment)

	dep, err := d.director.FindDeployment(deployment)
	if err != nil {
		d.log.Error("Failed to find deployment %s: %v", deployment, err)
		return nil, fmt.Errorf("failed to find deployment %s: %w", deployment, err)
	}

	d.log.Debug("Retrieving VM information for deployment %s", deployment)
	vmInfos, err := dep.VMInfos()
	if err != nil {
		d.log.Error("Failed to get VMs for deployment %s: %v", deployment, err)
		return nil, fmt.Errorf("failed to get VMs for deployment %s: %w", deployment, err)
	}

	vms := make([]VM, len(vmInfos))
	for i, vmInfo := range vmInfos {
		vms[i] = VM{
			ID:                 vmInfo.ID,
			CID:                vmInfo.AgentID, // Using AgentID as CID approximation
			Job:                vmInfo.JobName,
			Index:              *vmInfo.Index, // Dereference pointer
			State:              vmInfo.ProcessState,
			IPs:                vmInfo.IPs,
			DNS:                []string{}, // DNS not directly available
			AZ:                 vmInfo.AZ,
			VMType:             vmInfo.VMType,
			ResourcePool:       vmInfo.ResourcePool,
			ResurrectionPaused: vmInfo.ResurrectionPaused,
		}
	}

	d.log.Info("Successfully retrieved %d VMs for deployment %s", len(vms), deployment)
	return vms, nil
}

// GetReleases retrieves all releases
func (d *DirectorAdapter) GetReleases() ([]Release, error) {
	d.log.Info("Getting all releases")
	d.log.Debug("Calling director.Releases()")

	// Test basic connectivity first
	info, err := d.director.Info()
	if err != nil {
		d.log.Error("Failed to get director info (auth test): %v", err)
		d.log.Error("This might indicate an authentication issue")
	} else {
		d.log.Debug("Director info test successful - User: %s, Name: %s", info.User, info.Name)
		d.log.Debug("Director authentication type: %s", info.Auth.Type)
		if info.Auth.Type == "uaa" {
			if uaaURL, ok := info.Auth.Options["url"].(string); ok {
				d.log.Debug("Director is configured for UAA authentication at: %s", uaaURL)
				d.log.Error("ERROR: Director is configured for UAA authentication but we're using basic auth credentials")
				d.log.Error("The director expects UAA client credentials, not basic username/password")
			}
		} else if info.Auth.Type == "basic" {
			d.log.Debug("Director is configured for basic authentication")
		}
		if info.User == "" {
			d.log.Debug("Note: User field is empty in /info response - this is expected as /info doesn't require authentication")
		}
	}

	releases, err := d.director.Releases()
	if err != nil {
		d.log.Error("Failed to get releases: %v", err)
		return nil, fmt.Errorf("failed to get releases: %w", err)
	}

	d.log.Debug("Found %d releases", len(releases))

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

// UploadRelease uploads a release from URL
func (d *DirectorAdapter) UploadRelease(url, sha1 string) (*Task, error) {
	d.log.Info("Uploading release from URL: %s", url)
	d.log.Debug("Release SHA1: %s", sha1)

	// Simplified upload - bosh-cli has different signature
	err := d.director.UploadReleaseURL(url, sha1, false, false)
	if err != nil {
		d.log.Error("Failed to upload release from %s: %v", url, err)
		return nil, fmt.Errorf("failed to upload release from %s: %w", url, err)
	}

	// Get the latest task
	tasks, err := d.director.RecentTasks(1, boshdirector.TasksFilter{})
	if err != nil {
		return nil, fmt.Errorf("failed to get task for release upload: %w", err)
	}

	if len(tasks) > 0 {
		task := convertDirectorTask(tasks[0])
		d.log.Info("Release upload started, task ID: %d", task.ID)
		return task, nil
	}

	d.log.Error("No task found for release upload")
	return nil, fmt.Errorf("no task found for release upload")
}

// GetStemcells retrieves all stemcells
func (d *DirectorAdapter) GetStemcells() ([]Stemcell, error) {
	d.log.Info("Getting all stemcells")
	d.log.Debug("Calling director.Stemcells()")

	stemcells, err := d.director.Stemcells()
	if err != nil {
		d.log.Error("Failed to get stemcells: %v", err)
		return nil, fmt.Errorf("failed to get stemcells: %w", err)
	}

	d.log.Debug("Found %d stemcells", len(stemcells))

	result := make([]Stemcell, len(stemcells))
	for i, sc := range stemcells {
		result[i] = Stemcell{
			Name:        sc.Name(),
			Version:     sc.Version().String(),
			OS:          sc.OSName(),
			CID:         sc.CID(),
			CPI:         sc.CPI(),
			Deployments: []string{}, // Deployments not directly available
		}
	}

	d.log.Info("Successfully retrieved %d stemcells", len(result))
	return result, nil
}

// UploadStemcell uploads a stemcell from URL
func (d *DirectorAdapter) UploadStemcell(url, sha1 string) (*Task, error) {
	d.log.Info("Uploading stemcell from URL: %s", url)
	d.log.Debug("Stemcell SHA1: %s", sha1)

	// Simplified upload - bosh-cli has different signature
	err := d.director.UploadStemcellURL(url, sha1, false)
	if err != nil {
		d.log.Error("Failed to upload stemcell from %s: %v", url, err)
		return nil, fmt.Errorf("failed to upload stemcell from %s: %w", url, err)
	}

	// Get the latest task
	tasks, err := d.director.RecentTasks(1, boshdirector.TasksFilter{})
	if err != nil {
		return nil, fmt.Errorf("failed to get task for stemcell upload: %w", err)
	}

	if len(tasks) > 0 {
		task := convertDirectorTask(tasks[0])
		d.log.Info("Stemcell upload started, task ID: %d", task.ID)
		return task, nil
	}

	d.log.Error("No task found for stemcell upload")
	return nil, fmt.Errorf("no task found for stemcell upload")
}

// GetTask retrieves a task by ID
func (d *DirectorAdapter) GetTask(id int) (*Task, error) {
	d.log.Info("Getting task: %d", id)
	d.log.Debug("Finding task %d", id)

	task, err := d.director.FindTask(id)
	if err != nil {
		d.log.Error("Failed to get task %d: %v", id, err)
		return nil, fmt.Errorf("failed to get task %d: %w", id, err)
	}

	convertedTask := convertDirectorTask(task)
	d.log.Info("Successfully retrieved task %d (state: %s)", id, convertedTask.State)
	return convertedTask, nil
}

// GetTaskOutput retrieves task output
func (d *DirectorAdapter) GetTaskOutput(id int, outputType string) (string, error) {
	d.log.Info("Getting task output for task %d (type: %s)", id, outputType)
	d.log.Debug("Finding task %d for output retrieval", id)

	task, err := d.director.FindTask(id)
	if err != nil {
		d.log.Error("Failed to get task %d: %v", id, err)
		return "", fmt.Errorf("failed to get task %d: %w", id, err)
	}

	// Create a buffered reporter to capture output
	reporter := &BufferedTaskReporter{}

	// Get task output based on type
	switch outputType {
	case "debug":
		// For debug output, we need the full task result
		err = task.ResultOutput(reporter)
	case "result":
		// For result output, use the same method
		err = task.ResultOutput(reporter)
	case "event":
		// Event output is typically JSON lines of task events
		// This might need special handling
		err = task.ResultOutput(reporter)
	default:
		// Default to result output
		err = task.ResultOutput(reporter)
	}

	if err != nil {
		d.log.Error("Failed to get task output for task %d: %v", id, err)
		return "", fmt.Errorf("failed to get task output for task %d: %w", id, err)
	}

	output := reporter.GetOutput()
	d.log.Info("Successfully retrieved task output for task %d (size: %d bytes)", id, len(output))
	d.log.Debug("Task output type %s retrieved successfully", outputType)
	return output, nil
}

// GetTaskEvents retrieves task events
func (d *DirectorAdapter) GetTaskEvents(id int) ([]TaskEvent, error) {
	d.log.Info("Getting task events for task %d", id)
	d.log.Debug("Finding task %d for event retrieval", id)

	task, err := d.director.FindTask(id)
	if err != nil {
		d.log.Error("Failed to get task %d: %v", id, err)
		return nil, fmt.Errorf("failed to get task %d: %w", id, err)
	}

	// Get event output which should be JSON lines
	reporter := &BufferedTaskReporter{}
	err = task.ResultOutput(reporter)
	if err != nil {
		d.log.Error("Failed to get task events for task %d: %v", id, err)
		return nil, fmt.Errorf("failed to get task events for task %d: %w", id, err)
	}

	// Parse the output as JSON lines for events
	output := reporter.GetOutput()
	if output == "" {
		return []TaskEvent{}, nil
	}

	events := []TaskEvent{}
	lines := strings.Split(output, "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

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

		if err := json.Unmarshal([]byte(line), &boshEvent); err != nil {
			// Skip lines that aren't valid JSON events
			continue
		}

		// Convert to our TaskEvent structure
		event := TaskEvent{
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

		events = append(events, event)
	}

	d.log.Info("Successfully retrieved %d events for task %d", len(events), id)
	return events, nil
}

// UpdateCloudConfig updates the cloud config
func (d *DirectorAdapter) UpdateCloudConfig(config string) error {
	d.log.Info("Updating cloud config")
	d.log.Debug("Cloud config size: %d bytes", len(config))

	// UpdateConfig has different signature in bosh-cli
	_, err := d.director.UpdateConfig("cloud", "", config, []byte(config))
	if err != nil {
		d.log.Error("Failed to update cloud config: %v", err)
		return fmt.Errorf("failed to update cloud config: %w", err)
	}

	d.log.Info("Successfully updated cloud config")
	return nil
}

// GetCloudConfig retrieves the cloud config
func (d *DirectorAdapter) GetCloudConfig() (string, error) {
	d.log.Info("Getting cloud config")
	d.log.Debug("Listing cloud configs (limit: 1)")

	configs, err := d.director.ListConfigs(1, boshdirector.ConfigsFilter{
		Type: "cloud",
	})
	if err != nil {
		d.log.Error("Failed to get cloud config: %v", err)
		return "", fmt.Errorf("failed to get cloud config: %w", err)
	}

	if len(configs) == 0 {
		d.log.Error("No cloud config found")
		return "", fmt.Errorf("no cloud config found")
	}

	d.log.Info("Successfully retrieved cloud config (size: %d bytes)", len(configs[0].Content))
	return configs[0].Content, nil
}

// Cleanup runs BOSH cleanup
func (d *DirectorAdapter) Cleanup(removeAll bool) (*Task, error) {
	d.log.Info("Running BOSH cleanup (removeAll: %v)", removeAll)
	d.log.Debug("Initiating cleanup operation")

	// CleanUp has different signature in bosh-cli
	_, err := d.director.CleanUp(removeAll, false, false)
	if err != nil {
		d.log.Error("Failed to run cleanup: %v", err)
		return nil, fmt.Errorf("failed to run cleanup: %w", err)
	}

	// Get the latest task
	tasks, err := d.director.RecentTasks(1, boshdirector.TasksFilter{})
	if err != nil {
		return nil, fmt.Errorf("failed to get task for cleanup: %w", err)
	}

	if len(tasks) > 0 {
		task := convertDirectorTask(tasks[0])
		d.log.Info("Cleanup started, task ID: %d", task.ID)
		return task, nil
	}

	d.log.Error("No task found for cleanup")
	return nil, fmt.Errorf("no task found for cleanup")
}

// Helper functions

func buildFactoryConfig(config Config, logger boshlog.Logger) (*boshdirector.FactoryConfig, error) {
	// Parse the address to extract host and port
	host := config.Address
	port := 25555 // Default BOSH director port

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
			if p, err := strconv.Atoi(parts[1]); err == nil {
				port = p
			}
		}
	}

	factoryConfig := &boshdirector.FactoryConfig{
		Host:   host,
		Port:   port,
		CACert: config.CACert,
	}

	// First, try to detect if the director uses UAA by making an unauthenticated info call
	// Create a temporary director to check authentication type
	tempFactory := boshdirector.NewFactory(logger)
	tempDirector, err := tempFactory.New(*factoryConfig, nil, nil)
	if err == nil {
		if info, err := tempDirector.Info(); err == nil {
			if config.Logger != nil {
				config.Logger.Debug("Detected director auth type: %s", info.Auth.Type)
			}

			// If director uses UAA and we have username/password, set up UAA client auth
			if info.Auth.Type == "uaa" && config.Username != "" && config.Password != "" {
				// Extract UAA URL from auth options
				if uaaURL, ok := info.Auth.Options["url"].(string); ok {
					if config.Logger != nil {
						config.Logger.Info("Director uses UAA authentication, configuring UAA client")
						config.Logger.Debug("UAA URL: %s", uaaURL)
					}
					
					// Parse UAA URL to extract host and port
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
							if p, err := strconv.Atoi(parts[1]); err == nil {
								uaaPort = p
							}
						}
					}
					
					if config.Logger != nil {
						config.Logger.Debug("UAA Host: %s, Port: %d", uaaHost, uaaPort)
					}
					
					// Create UAA client using the username/password as client credentials
					uaaConfig := boshuaa.Config{
						Host:         uaaHost,
						Port:         uaaPort,
						Client:       config.Username,
						ClientSecret: config.Password,
						CACert:       config.CACert,
					}
					
					uaaFactory := boshuaa.NewFactory(logger)
					uaa, err := uaaFactory.New(uaaConfig)
					if err != nil {
						if config.Logger != nil {
							config.Logger.Error("Failed to create UAA client: %v", err)
						}
						return nil, fmt.Errorf("failed to create UAA client: %w", err)
					}
					
					// Set up token function for UAA authentication
					factoryConfig.TokenFunc = boshuaa.NewClientTokenSession(uaa).TokenFunc
					
					// Also set environment variables for compatibility
					os.Setenv("BOSH_CLIENT", config.Username)
					os.Setenv("BOSH_CLIENT_SECRET", config.Password)
					
					return factoryConfig, nil
				}
			}
		}
	}

	// Fall back to basic auth or explicit UAA config
	if config.UAA == nil {
		if config.Username != "" && config.Password != "" {
			// Set environment variables for BOSH CLI
			os.Setenv("BOSH_CLIENT", config.Username)
			os.Setenv("BOSH_CLIENT_SECRET", config.Password)

			// Also set in factory config for direct usage
			if config.Logger != nil {
				config.Logger.Debug("Setting up basic auth with username: %s", config.Username)
				config.Logger.Debug("Set BOSH_CLIENT env var to: %s", config.Username)
			}
			factoryConfig.Client = config.Username
			factoryConfig.ClientSecret = config.Password
		} else {
			// Check if env vars are already set
			envClient := os.Getenv("BOSH_CLIENT")
			envSecret := os.Getenv("BOSH_CLIENT_SECRET")

			if envClient != "" && envSecret != "" {
				if config.Logger != nil {
					config.Logger.Debug("Using BOSH_CLIENT from environment: %s", envClient)
				}
				factoryConfig.Client = envClient
				factoryConfig.ClientSecret = envSecret
			} else {
				// Log warning if no credentials provided
				if config.Logger != nil {
					config.Logger.Error("No authentication credentials provided (neither config nor env vars)")
				}
			}
		}
	}

	// Set up UAA if explicitly configured
	if config.UAA != nil {
		uaaConfig := boshuaa.Config{
			Host:         config.UAA.URL,
			Client:       config.UAA.ClientID,
			ClientSecret: config.UAA.ClientSecret,
			CACert:       config.UAA.CACert,
		}

		uaaFactory := boshuaa.NewFactory(logger)
		uaa, err := uaaFactory.New(uaaConfig)
		if err != nil {
			return nil, fmt.Errorf("failed to create UAA client: %w", err)
		}

		factoryConfig.TokenFunc = boshuaa.NewClientTokenSession(uaa).TokenFunc
	}

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

func extractDeploymentName(manifest string) string {
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

// Ensure DirectorAdapter implements Director interface
var _ Director = (*DirectorAdapter)(nil)
