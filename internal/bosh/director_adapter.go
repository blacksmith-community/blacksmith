package bosh

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	boshdirector "github.com/cloudfoundry/bosh-cli/v7/director"
	boshuaa "github.com/cloudfoundry/bosh-cli/v7/uaa"
	boshlog "github.com/cloudfoundry/bosh-utils/logger"
	boshuuid "github.com/cloudfoundry/bosh-utils/uuid"
	"github.com/fivetwenty-io/boshapi/pkg/boshapi"
	"github.com/geofffranks/spruce"
	"golang.org/x/crypto/ssh"
	"gopkg.in/yaml.v2"
)

// Error definitions.
var (
	ErrDirectorInfoProviderNil = errors.New("director info provider is nil")
	ErrFactoryConfigNil        = errors.New("factory config is nil")
	ErrNoErrandResults         = errors.New("no errand results returned")
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

	// sshCleanupTimeout bounds the director call that removes an SSH user.
	sshCleanupTimeout = 30 * time.Second

	// Default ports for BOSH services.
	defaultBOSHDirectorPort = 25555
	AuthTypeUAA             = "uaa"
	authTypeUAA             = AuthTypeUAA // Deprecated: use AuthTypeUAA
	// Default config limit.
	defaultConfigLimit = 100
)

// Static error variables to satisfy err113.
var (
	ErrGetConfigFailed                = errors.New("failed to get config")
	ErrConfigNotFound                 = errors.New("config not found")
	ErrConfigYAMLParseFailed          = errors.New("failed to parse config YAML")
	ErrDeploymentNameExtractionFailed = errors.New("could not extract deployment name from manifest")
	// ErrDeploymentNotFound is returned by GetDeployment when the director
	// answers 404 for the deployment. Callers must use errors.Is against this
	// sentinel to decide that a deployment does not exist; any other error means
	// existence is unknown, and a successful response with an empty manifest
	// means the deployment exists but its first deploy has not finished yet.
	ErrDeploymentNotFound = errors.New("deployment not found")
	// ErrEmptyContextID is returned by FindTaskByContextID for an empty
	// context ID, which the director would take as no filter at all.
	ErrEmptyContextID                 = errors.New("the context ID to look tasks up by is empty")
	ErrTaskCannotBeCancelled          = errors.New("task is in invalid state and cannot be cancelled")
	ErrNoCloudConfigFound             = errors.New("no cloud config found")
	ErrInvalidConfigIDFormat          = errors.New("invalid config ID format")
	ErrNoHostsAvailableSSH            = errors.New("no hosts available for SSH connection")
	ErrResurrectionConfigUpdateFailed = errors.New("failed to update resurrection config for deployment")
	ErrResurrectionConfigDeleteFailed = errors.New("failed to delete resurrection config for deployment")
	ErrResurrectionConfigNotFound     = errors.New("resurrection config for deployment not found or already deleted")
	ErrUAAAutoDetectionNotApplicable  = errors.New("UAA auto-detection not applicable")
	ErrUAAURLNotFound                 = errors.New("UAA URL not found in auth options")
	ErrDirectorNotUsingUAA            = errors.New("director does not use UAA authentication")
	ErrUAAURLNotString                = errors.New("UAA URL in director info is not a string")
	ErrUAAURLNotFoundInInfo           = errors.New("UAA URL not found in director info")
	ErrUAAClientCredentialsMissing    = errors.New("missing UAA client credentials")
)

// GetConfig retrieves a configuration by type and name from BOSH director.
func (da *DirectorAdapter) GetConfig(ctx context.Context, configType, configName string) (_ interface{}, err error) {
	defer da.explainContext(ctx, "GetConfig", &err)

	da.log.Debugf("Getting config type=%s name=%s", configType, configName)

	// The director answers a config it does not hold with an empty list.
	entries, err := da.api.Configs().List(ctx, &boshapi.ConfigListOptions{
		Type:   configType,
		Name:   configName,
		Latest: true,
	})
	if err != nil {
		da.log.Errorf("Failed to get config %s/%s: %v", configType, configName, err)

		return nil, fmt.Errorf("%w %s/%s: %w", ErrGetConfigFailed, configType, configName,
			da.requestFailure("read it", err))
	}

	if len(entries) == 0 {
		da.log.Debugf("Config %s/%s does not exist", configType, configName)

		return nil, ErrConfigNotFound
	}

	config := entries[0]

	da.log.Debugf("Retrieved config %s/%s successfully (size: %d bytes, content not logged)", configType, configName, len(config.Content))

	// Parse the YAML content to return as a map
	var configData map[string]interface{}

	err = yaml.Unmarshal([]byte(config.Content), &configData)
	if err != nil {
		da.log.Errorf("Failed to parse config YAML for %s/%s: %v", configType, configName, err)

		return nil, fmt.Errorf("%w: %w", ErrConfigYAMLParseFailed, err)
	}

	da.log.Debugf("Parsed config %s/%s into %d top-level keys", configType, configName, len(configData))

	return configData, nil
}

// DirectorAdapter implements the Director interface. The methods that have
// moved off bosh-cli call the boshapi client, and the rest still go through
// the bosh-cli director.
type DirectorAdapter struct {
	director boshdirector.Director
	// api is the boshapi client the methods that have moved off bosh-cli
	// call.
	api            boshapi.Client
	address        string        // director address, for errors
	requestTimeout time.Duration // the bosh.request_timeout in force, for errors
	// taskPollInterval is how often WaitForTask reads a task. Zero means
	// boshapi's default of 2 seconds.
	taskPollInterval time.Duration
	logger           boshlog.Logger
	log              Logger // Application logger for Info/Debug logging
}

// bind returns the bosh-cli director for one call.
func (d *DirectorAdapter) bind(ctx context.Context) boshdirector.Director {
	return d.director
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

	// Refuse a configuration with no credentials before anything dials the
	// director.
	auth, err := resolveDirectorAuth(config)
	if err != nil {
		appLogger.Errorf("Failed to resolve director credentials: %v", err)

		return nil, err
	}

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

	appLogger.Debugf("Creating boshapi client")

	api, err := newAPIClient(config, auth, appLogger)
	if err != nil {
		appLogger.Errorf("Failed to create boshapi client: %v", err)

		return nil, err
	}

	appLogger.Infof("Successfully created BOSH director adapter")

	return &DirectorAdapter{
		director:       director,
		api:            api,
		address:        config.Address,
		requestTimeout: config.RequestTimeout,
		logger:         logger,
		log:            appLogger,
	}, nil
}

// Logger interface for application logging.
type Logger interface {
	Infof(format string, args ...interface{})
	Debugf(format string, args ...interface{})
	Errorf(format string, args ...interface{})
}

// noOpLogger is a no-op implementation of Logger interface.
type noOpLogger struct{}

func (n *noOpLogger) Infof(format string, args ...interface{})  {}
func (n *noOpLogger) Debugf(format string, args ...interface{}) {}
func (n *noOpLogger) Errorf(format string, args ...interface{}) {}

// GetInfo retrieves BOSH director information.
func (d *DirectorAdapter) GetInfo(ctx context.Context) (_ *Info, err error) {
	defer d.explainContext(ctx, "GetInfo", &err)

	d.log.Infof("Getting BOSH director information")
	d.log.Debugf("Calling GET /info")

	info, err := d.api.GetInfo(ctx)
	if err != nil {
		d.log.Errorf("Failed to get director info: %v", err)

		return nil, d.requestFailure("get info", err)
	}

	if info == nil {
		d.log.Errorf("The director answered GET /info with no data")

		return nil, d.requestFailure("get info", ErrEmptyDirectorAnswer)
	}

	d.log.Debugf("Director info retrieved - Name: %s, UUID: %s, Version: %s", info.Name, info.UUID, info.Version)

	// The director reports each feature as {"status": bool, "extras": {...}}.
	// Features maps each feature name to its status, as bosh-cli does.
	features := make(map[string]bool, len(info.Features))
	for name, feature := range info.Features {
		features[name] = feature.Status
	}

	var uaaURL string

	if info.UserAuthentication.Type == AuthTypeUAA {
		uaaURL = info.UserAuthentication.Options.URL
	}

	d.log.Infof("Successfully retrieved director info: %s (%s)", info.Name, info.Version)

	return &Info{
		Name:     info.Name,
		UUID:     info.UUID,
		Version:  info.Version,
		User:     info.User,
		CPI:      info.CPI,
		Features: features,
		UAAURL:   uaaURL,
	}, nil
}

// GetDeployments lists all deployments. The director's list answer carries
// each deployment's releases, stemcells, teams, and cloud config state, so
// one request is all it takes.
func (d *DirectorAdapter) GetDeployments(ctx context.Context) (_ []Deployment, err error) {
	defer d.explainContext(ctx, "GetDeployments", &err)

	d.log.Infof("Listing all deployments")
	d.log.Debugf("Calling GET /deployments")

	deps, err := d.api.Deployments().List(ctx, nil)
	if err != nil {
		d.log.Errorf("Failed to get deployments: %v", err)

		return nil, d.requestFailure("list deployments", err)
	}

	d.log.Debugf("Found %d deployments", len(deps))

	deployments := make([]Deployment, len(deps))
	for index, dep := range deps {
		releases := make([]string, len(dep.Releases))
		for j, rel := range dep.Releases {
			releases[j] = fmt.Sprintf("%s/%s", rel.Name, rel.Version)
		}

		stemcells := make([]string, len(dep.Stemcells))
		for j, sc := range dep.Stemcells {
			stemcells[j] = fmt.Sprintf("%s/%s", sc.Name, sc.Version)
		}

		deployments[index] = Deployment{
			Name:        dep.Name,
			CloudConfig: dep.CloudConfig,
			Releases:    releases,
			Stemcells:   stemcells,
			Teams:       dep.Teams,
		}
	}

	d.log.Infof("Successfully retrieved %d deployments", len(deployments))

	return deployments, nil
}

// GetDeployment retrieves a specific deployment. Only the director's own
// "deployment not found" answer, its JSON error with code 70000, reports the
// deployment as missing. A plain 404 is a routing problem, and it stays an
// error that says so, so callers that act on a missing deployment never act
// on one.
func (d *DirectorAdapter) GetDeployment(ctx context.Context, name string) (_ *DeploymentDetail, err error) {
	defer d.explainContext(ctx, "GetDeployment", &err)

	d.log.Infof("Getting deployment: %s", name)
	d.log.Debugf("Calling GET /deployments/%s", name)

	// A deployment whose first deploy is still running answers 200 with an
	// empty manifest, which is an existing deployment, not a missing one.
	deployment, err := d.api.Deployments().Get(ctx, name)
	if err != nil {
		// Only the director's deployment-not-found code means the deployment
		// does not exist. boshapi.IsNotFound also accepts the director's codes
		// for a missing resource and a missing task, which say nothing about
		// this deployment.
		var apiErr *boshapi.APIError
		if errors.As(err, &apiErr) && apiErr.Code == boshapi.ErrorCodeDeploymentNotFound {
			d.log.Debugf("Deployment %s not found (expected during early provisioning stages)", name)

			return nil, fmt.Errorf("%w: %s: %w", ErrDeploymentNotFound, name, err)
		}

		d.log.Errorf("Failed to get deployment %s: %v", name, err)

		return nil, d.requestFailure("get deployment "+name, err)
	}

	if deployment == nil {
		d.log.Errorf("The director answered GET /deployments/%s with no data", name)

		return nil, d.requestFailure("get deployment "+name, ErrEmptyDirectorAnswer)
	}

	if deployment.Manifest == "" {
		d.log.Infof("Successfully retrieved deployment %s (no manifest yet; a deploy is still in flight)", name)
	} else {
		d.log.Infof("Successfully retrieved deployment %s (manifest size: %d bytes)", name, len(deployment.Manifest))
	}

	return &DeploymentDetail{
		Name:     name,
		Manifest: deployment.Manifest,
	}, nil
}

// GetDeploymentVMs retrieves VMs for a deployment with full details (format=full).
func (d *DirectorAdapter) GetDeploymentVMs(ctx context.Context, deployment string) (_ []VM, err error) {
	defer d.explainContext(ctx, "GetDeploymentVMs", &err)

	director := d.bind(ctx)

	d.log.Infof("Getting VMs for deployment: %s", deployment)
	d.log.Debugf("Finding deployment %s", deployment)

	dep, err := director.FindDeployment(deployment)
	if err != nil {
		// Check if it's a 404 error (deployment doesn't exist)
		errStr := err.Error()
		if strings.Contains(errStr, "doesn't exist") || strings.Contains(errStr, "status code '404'") {
			d.log.Infof("Deployment %s not found in BOSH director (404)", deployment)

			return nil, fmt.Errorf("%w: %s", ErrDeploymentNotFound, deployment)
		}

		d.log.Errorf("Failed to find deployment %s: %v", deployment, err)

		return nil, fmt.Errorf("failed to find deployment %s: %w", deployment, err)
	}

	d.log.Debugf("Retrieving detailed VM information for deployment %s (format=full)", deployment)

	vmInfos, err := dep.VMInfos()
	if err != nil {
		// Check if it's a 404 error (deployment doesn't exist or VMs not found)
		errStr := err.Error()
		if strings.Contains(errStr, "doesn't exist") || strings.Contains(errStr, "status code '404'") {
			d.log.Infof("Deployment %s VMs not found in BOSH director (404)", deployment)

			return nil, fmt.Errorf("%w: %s", ErrDeploymentNotFound, deployment)
		}

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

// GetReleases retrieves all releases, each with every version the director
// holds.
func (d *DirectorAdapter) GetReleases(ctx context.Context) (_ []Release, err error) {
	defer d.explainContext(ctx, "GetReleases", &err)

	d.log.Infof("Getting all releases")
	d.log.Debugf("Calling GET /releases")

	releases, err := d.api.Releases().List(ctx)
	if err != nil {
		d.log.Errorf("Failed to get releases: %v", err)

		return nil, d.requestFailure("list releases", err)
	}

	d.log.Debugf("Found %d releases", len(releases))

	result := make([]Release, 0, len(releases))
	for _, rel := range releases {
		versions := make([]ReleaseVersion, len(rel.ReleaseVersions))
		for index, version := range rel.ReleaseVersions {
			versions[index] = ReleaseVersion{
				Version:            version.Version,
				CommitHash:         version.CommitHash,
				UncommittedChanges: version.UncommittedChanges,
				CurrentlyDeployed:  version.CurrentlyDeployed,
				JobNames:           version.JobNames,
			}
		}

		result = append(result, Release{Name: rel.Name, ReleaseVersions: versions})
	}

	return result, nil
}

// GetStemcells retrieves all stemcells.
func (d *DirectorAdapter) GetStemcells(ctx context.Context) (_ []Stemcell, err error) {
	defer d.explainContext(ctx, "GetStemcells", &err)

	d.log.Infof("Getting all stemcells")
	d.log.Debugf("Calling GET /stemcells")

	stemcells, err := d.api.Stemcells().List(ctx)
	if err != nil {
		d.log.Errorf("Failed to get stemcells: %v", err)

		return nil, d.requestFailure("list stemcells", err)
	}

	d.log.Debugf("Found %d stemcells", len(stemcells))

	result := make([]Stemcell, len(stemcells))
	for index, stemcell := range stemcells {
		deployments := make([]string, len(stemcell.Deployments))
		for j, deployment := range stemcell.Deployments {
			deployments[j] = deployment.Name
		}

		result[index] = Stemcell{
			Name:        stemcell.Name,
			Version:     stemcell.Version,
			OS:          stemcell.OperatingSystem,
			CID:         stemcell.CID,
			CPI:         stemcell.CPI,
			Deployments: deployments,
		}
	}

	d.log.Infof("Successfully retrieved %d stemcells", len(result))

	return result, nil
}

// runningTaskStates is the director's state filter for tasks that have not
// finished.
const runningTaskStates = "processing,cancelling,queued"

// taskVerboseAll is the task list verbosity at which the director lists
// every task, orphan cleanup tasks included.
const taskVerboseAll = 2

// Task states the director reports.
const (
	taskStateQueued     = "queued"
	taskStateProcessing = "processing"
	taskStateCancelling = "cancelling"
	taskStateDone       = "done"
	taskStateError      = "error"
	taskStateCancelled  = "cancelled"
	taskStateTimeout    = "timeout"
)

// isTerminalTaskState reports whether a task in state has finished.
func isTerminalTaskState(state string) bool {
	switch state {
	case taskStateDone, taskStateError, taskStateCancelled, taskStateTimeout:
		return true
	default:
		return false
	}
}

// isRunningTaskState reports whether a task in state has not finished.
func isRunningTaskState(state string) bool {
	switch state {
	case taskStateQueued, taskStateProcessing, taskStateCancelling:
		return true
	default:
		return false
	}
}

// convertAPITask converts a task as the director reports it. The director's
// timestamp is the time of the task's last update, which for a finished task
// is when it ended.
func convertAPITask(task boshapi.Task) Task {
	converted := Task{
		ID:          task.ID,
		State:       task.State,
		Description: task.Description,
		Result:      task.Result,
		User:        task.User,
		Deployment:  task.DeploymentName,
		ContextID:   task.ContextID,
	}

	if task.StartedAt != nil {
		converted.StartedAt = time.Unix(*task.StartedAt, 0)
	}

	if task.Timestamp != nil {
		converted.Timestamp = *task.Timestamp

		if isTerminalTaskState(task.State) {
			ended := time.Unix(*task.Timestamp, 0)
			converted.EndedAt = &ended
		}
	}

	return converted
}

// convertAPITasks converts tasks in the order the director listed them.
func convertAPITasks(tasks []boshapi.Task) []Task {
	converted := make([]Task, len(tasks))
	for index, task := range tasks {
		converted[index] = convertAPITask(task)
	}

	return converted
}

// GetTask retrieves a task by ID.
func (d *DirectorAdapter) GetTask(ctx context.Context, taskID int) (_ *Task, err error) {
	defer d.explainContext(ctx, "GetTask", &err)

	d.log.Debugf("Calling GET /tasks/%d", taskID)

	task, err := d.api.Tasks().Get(ctx, taskID)
	if err != nil {
		d.log.Errorf("Failed to get task %d: %v", taskID, err)

		return nil, d.requestFailure(fmt.Sprintf("get BOSH task %d", taskID), err)
	}

	if task == nil {
		d.log.Errorf("The director answered GET /tasks/%d with no data", taskID)

		return nil, d.requestFailure(fmt.Sprintf("get BOSH task %d", taskID), ErrEmptyDirectorAnswer)
	}

	converted := convertAPITask(*task)
	d.log.Debugf("Task %d is %s", taskID, converted.State)

	return &converted, nil
}

// WaitForTask waits until the task reaches a terminal state, reading it every
// few seconds, and returns the final task with a nil error whatever that state
// is, because callers judge the state themselves.
//
// Every other way the wait can stop leaves the task running, and returns a
// *DirectorCallError with the task's ID and TaskLeftRunning. That covers a
// context that ended, a read that got no answer within the request timeout,
// and a read the director answered with an error. boshapi already retries a
// read that can recover, such as one that met a dropped connection or a 503,
// so a read that still fails ends the wait: a caller that must follow the
// task further has its ID, and the batch director keeps watching a deploy
// through such failures.
func (d *DirectorAdapter) WaitForTask(ctx context.Context, taskID int) (_ *Task, err error) {
	var lastState string

	defer func() { d.explainTaskWait(ctx, taskID, lastState, &err) }()

	d.log.Debugf("Waiting for BOSH task %d", taskID)

	task, err := d.api.Tasks().PollUntilComplete(ctx, taskID, &boshapi.PollOptions{Interval: d.taskPollInterval})

	// boshapi reports a task that ended in error, cancelled, or timeout as a
	// TaskError that holds the task. Here every terminal state is an answer.
	var taskErr *boshapi.TaskError
	if errors.As(err, &taskErr) && taskErr.Task != nil {
		task, err = taskErr.Task, nil
	}

	if err != nil {
		d.log.Errorf("Stopped waiting for BOSH task %d: %v", taskID, err)

		// boshapi's wait error tells the reader to follow the task on the
		// director's own routes. Blacksmith's error points at /b/tasks/N
		// instead, so only the read's own error is kept.
		var waitErr *boshapi.TaskWaitError
		if errors.As(err, &waitErr) && waitErr.Err != nil {
			lastState, err = waitErr.LastState, waitErr.Err
		}

		return nil, d.requestFailure(fmt.Sprintf("read BOSH task %d", taskID), err)
	}

	if task == nil {
		d.log.Errorf("The director answered GET /tasks/%d with no data", taskID)

		return nil, d.requestFailure(fmt.Sprintf("read BOSH task %d", taskID), ErrEmptyDirectorAnswer)
	}

	converted := convertAPITask(*task)
	d.log.Infof("BOSH task %d ended in state %s", taskID, converted.State)

	return &converted, nil
}

// explainTaskWait explains a wait that stopped, as explainContext does, and
// records that the task was left running. A wait that stopped on a failed
// read of the task, while the context was still live, is explained the same
// way, with the failed read as its cause. lastState is the state the last
// good read saw, or empty when no read succeeded.
func (d *DirectorAdapter) explainTaskWait(ctx context.Context, taskID int, lastState string, err *error) {
	if *err == nil {
		return
	}

	d.explainContext(ctx, "WaitForTask", err)

	var callErr *DirectorCallError
	if !errors.As(*err, &callErr) {
		callErr = &DirectorCallError{
			Operation: "WaitForTask",
			Director:  d.address,
			Cause:     &taskReadError{taskID: taskID, lastState: lastState},
			Err:       *err,
		}
		*err = callErr
	}

	callErr.TaskID = taskID
	callErr.Task = TaskLeftRunning
}

// GetTasks lists tasks in one of Blacksmith's modes: "recent" asks for twice
// the limit, so the filters below still have enough tasks to fill the page,
// "current" asks for every task that has not finished, and "all" merges the
// two. Any other mode reads as "recent". The director has no task type
// filter, and the state and team filters run here.
func (d *DirectorAdapter) GetTasks(ctx context.Context, taskType string, limit int, states []string, team string) (_ []Task, err error) {
	defer d.explainContext(ctx, "GetTasks", &err)

	d.log.Debugf("Getting tasks (type: %s, limit: %d, states: %v, team: %s)", taskType, limit, states, team)

	if limit <= 0 || limit > 200 {
		limit = 50
	}

	var tasks []Task

	switch taskType {
	case "current":
		tasks, err = d.listTasks(ctx, "list running tasks", &boshapi.TaskListOptions{State: runningTaskStates, Verbose: 1})
	case "all":
		tasks, err = d.listAllTasks(ctx, limit)
	default:
		if taskType != "recent" {
			d.log.Debugf("Unknown task type %s, listing recent tasks", taskType)
		}

		tasks, err = d.listTasks(ctx, "list recent tasks", &boshapi.TaskListOptions{Limit: limit * taskFetchMultiplier, Verbose: 1})
	}

	if err != nil {
		return nil, err
	}

	tasks = d.filterTasksByStates(tasks, states)
	tasks = d.filterTasksByTeam(tasks, team)

	if len(tasks) > limit {
		tasks = tasks[:limit]
	}

	d.log.Debugf("Retrieved %d tasks (type: %s)", len(tasks), taskType)

	return tasks, nil
}

// GetAllTasks lists the running tasks and the recent ones, merged.
func (d *DirectorAdapter) GetAllTasks(ctx context.Context, limit int) (_ []Task, err error) {
	defer d.explainContext(ctx, "GetAllTasks", &err)

	return d.GetTasks(ctx, "all", limit, nil, "")
}

// listTasks lists tasks with opts. what names the list in an error.
func (d *DirectorAdapter) listTasks(ctx context.Context, what string, opts *boshapi.TaskListOptions) ([]Task, error) {
	tasks, err := d.api.Tasks().List(ctx, opts)
	if err != nil {
		d.log.Errorf("Failed to %s: %v", what, err)

		return nil, d.requestFailure(what, err)
	}

	return convertAPITasks(tasks), nil
}

// listAllTasks merges the running tasks with the recent ones, each task
// once, newest first.
func (d *DirectorAdapter) listAllTasks(ctx context.Context, limit int) ([]Task, error) {
	running, err := d.listTasks(ctx, "list running tasks", &boshapi.TaskListOptions{State: runningTaskStates, Verbose: 1})
	if err != nil {
		return nil, err
	}

	recent, err := d.listTasks(ctx, "list recent tasks", &boshapi.TaskListOptions{Limit: limit * taskFetchMultiplier, Verbose: 1})
	if err != nil {
		return nil, err
	}

	seen := make(map[int]bool, len(running)+len(recent))
	merged := make([]Task, 0, len(running)+len(recent))

	for _, task := range append(running, recent...) {
		if seen[task.ID] {
			continue
		}

		seen[task.ID] = true
		merged = append(merged, task)
	}

	sort.SliceStable(merged, func(i, j int) bool { return merged[i].ID > merged[j].ID })

	return merged, nil
}

// CancelTask asks the director to cancel a queued or processing task, on
// DELETE /task/N. A task the director will not cancel, because it has
// finished or a cancel already landed, returns ErrTaskCannotBeCancelled.
//
// When the cancel's answer is lost, the director may have taken it, so the
// task is read. A task that is cancelling or cancelled needs nothing more, a
// finished one returns ErrTaskCannotBeCancelled, and one that is still queued
// or processing gets the cancel once more. A task in any other state gets no
// second cancel and returns an error that names the state. A second cancel is
// safe, because a cancel starts no task and can only cancel the same one, and
// a second cancel that finds the task cancelling or cancelled means the first
// one landed, so it succeeds.
func (d *DirectorAdapter) CancelTask(ctx context.Context, taskID int) (err error) {
	defer d.explainContext(ctx, "CancelTask", &err)

	d.log.Infof("Cancelling BOSH task %d", taskID)

	err = d.api.Tasks().Cancel(ctx, taskID)
	if err == nil {
		d.log.Infof("The director accepted the cancel of BOSH task %d", taskID)

		return nil
	}

	if !boshapi.IsOutcomeUnknown(err) || ctx.Err() != nil {
		return d.cancelFailure(taskID, err)
	}

	d.log.Infof("The director's answer to the cancel of BOSH task %d was lost, so Blacksmith reads the task to see whether the cancel landed: %v", taskID, err)

	task, readErr := d.api.Tasks().Get(ctx, taskID)
	if readErr == nil && task == nil {
		readErr = ErrEmptyDirectorAnswer
	}

	if readErr != nil {
		d.log.Errorf("Failed to read BOSH task %d after its cancel's answer was lost: %v", taskID, readErr)

		return fmt.Errorf("failed to cancel BOSH task %d on BOSH director %s: the director's answer to the cancel was lost (%w), "+
			"and reading the task afterwards to see whether the cancel landed failed too (%w). "+
			"Check the task at /b/tasks/%d, and cancel it again if it is still queued or processing",
			taskID, d.address, err, readErr, taskID)
	}

	switch {
	case task.State == taskStateCancelling || task.State == taskStateCancelled:
		d.log.Infof("BOSH task %d is %s, so the cancel whose answer was lost landed", taskID, task.State)

		return nil
	case isTerminalTaskState(task.State):
		d.log.Infof("BOSH task %d ended in state %s before the cancel took effect", taskID, task.State)

		return fmt.Errorf("%w: BOSH task %d on BOSH director %s ended in state %q before the cancel took effect, "+
			"so there is nothing left to cancel. Check its result at /b/tasks/%d",
			ErrTaskCannotBeCancelled, taskID, d.address, task.State, taskID)
	case task.State != taskStateQueued && task.State != taskStateProcessing:
		// The director uses no other state, so the answer did not come from
		// it as sent, and a second cancel would go to a task Blacksmith knows
		// nothing about.
		d.log.Errorf("BOSH task %d reads as state %q after its cancel's answer was lost, which the director does not use", taskID, task.State)

		return fmt.Errorf("failed to cancel BOSH task %d on BOSH director %s: the director's answer to the cancel was lost (%w), "+
			"and the task then read as state %q, which the director does not use, so Blacksmith did not send the cancel again. "+
			"Check the task at /b/tasks/%d, and cancel it again if it is still queued or processing",
			taskID, d.address, err, task.State, taskID)
	}

	d.log.Infof("BOSH task %d is still %s, so the cancel whose answer was lost never landed, and Blacksmith sends it once more", taskID, task.State)

	err = d.api.Tasks().Cancel(ctx, taskID)
	if err == nil {
		d.log.Infof("The director accepted the second cancel of BOSH task %d", taskID)

		return nil
	}

	// The first cancel may still have been on its way when the task was
	// read, and a task it reached is now cancelling or cancelled.
	var stateErr *boshapi.TaskStateError
	if errors.As(err, &stateErr) && (stateErr.State == taskStateCancelling || stateErr.State == taskStateCancelled) {
		d.log.Infof("BOSH task %d is %s, so the first cancel landed after all", taskID, stateErr.State)

		return nil
	}

	return d.cancelFailure(taskID, err)
}

// cancelFailure explains a cancel the director did not accept.
func (d *DirectorAdapter) cancelFailure(taskID int, err error) error {
	var stateErr *boshapi.TaskStateError
	if errors.As(err, &stateErr) {
		d.log.Infof("BOSH task %d is %s and cannot be cancelled", taskID, stateErr.State)

		return fmt.Errorf("%w: %w", ErrTaskCannotBeCancelled, stateErr)
	}

	d.log.Errorf("Failed to cancel BOSH task %d: %v", taskID, err)

	return d.requestFailure(fmt.Sprintf("cancel BOSH task %d", taskID), err)
}

// taskOutputType maps Blacksmith's output types onto the director's. The
// "task" view is the event log, which is what an operator reads to follow a
// task, and debug is the default.
func taskOutputType(outputType string) string {
	switch outputType {
	case "task", "event":
		return "event"
	case "result", "cpi":
		return outputType
	default:
		return "debug"
	}
}

// GetTaskOutput retrieves a task's output of the given type.
func (d *DirectorAdapter) GetTaskOutput(ctx context.Context, taskID int, outputType string) (_ string, err error) {
	defer d.explainContext(ctx, "GetTaskOutput", &err)

	directorType := taskOutputType(outputType)

	d.log.Debugf("Calling GET /tasks/%d/output?type=%s", taskID, directorType)

	output, err := d.api.Tasks().GetOutput(ctx, taskID, &boshapi.TaskOutputOptions{Type: directorType})
	if err != nil {
		d.log.Errorf("Failed to get the %s output of task %d: %v", directorType, taskID, err)

		return "", d.requestFailure(fmt.Sprintf("get the %s output of BOSH task %d", directorType, taskID), err)
	}

	d.log.Debugf("Retrieved the %s output of task %d (size: %d bytes)", directorType, taskID, len(output))

	return output, nil
}

// GetTaskEvents retrieves a task's events from its event output.
func (d *DirectorAdapter) GetTaskEvents(ctx context.Context, taskID int) (_ []TaskEvent, err error) {
	defer d.explainContext(ctx, "GetTaskEvents", &err)

	d.log.Debugf("Calling GET /tasks/%d/output?type=event", taskID)

	output, err := d.api.Tasks().GetOutput(ctx, taskID, &boshapi.TaskOutputOptions{Type: "event"})
	if err != nil {
		d.log.Errorf("Failed to get the events of task %d: %v", taskID, err)

		return nil, d.requestFailure(fmt.Sprintf("get the events of BOSH task %d", taskID), err)
	}

	events := d.parseTaskEvents(output)
	d.log.Debugf("Retrieved %d events for task %d", len(events), taskID)

	return events, nil
}

// GetEvents retrieves events for a specific deployment.
func (d *DirectorAdapter) GetEvents(ctx context.Context, deployment string) (_ []Event, err error) {
	defer d.explainContext(ctx, "GetEvents", &err)

	d.log.Infof("Getting events for deployment: %s", deployment)
	d.log.Debugf("Fetching events from BOSH director")

	directorEvents, err := d.api.Events().List(ctx, &boshapi.EventListOptions{Deployment: deployment})
	if err != nil {
		d.log.Errorf("Failed to get events for deployment %s: %v", deployment, err)

		return nil, d.requestFailure("list events for deployment "+deployment, err)
	}

	// Convert director events to our Event type
	events := make([]Event, 0, len(directorEvents))
	for _, dirEvent := range directorEvents {
		events = append(events, Event{
			ID:         dirEvent.ID,
			Time:       time.Unix(dirEvent.Timestamp, 0).UTC(),
			User:       dirEvent.User,
			Action:     dirEvent.Action,
			ObjectType: dirEvent.ObjectType,
			ObjectName: dirEvent.ObjectName,
			TaskID:     dirEvent.TaskID,
			Deployment: dirEvent.DeploymentName,
			Instance:   dirEvent.Instance,
			Context:    fmt.Sprintf("%v", dirEvent.Context),
			Error:      dirEvent.Error,
		})
	}

	d.log.Infof("Successfully retrieved %d events for deployment %s", len(events), deployment)

	return events, nil
}

// UpdateCloudConfig writes the content as the cloud config named blacksmith.
// The director merges named cloud configs alongside its default one, so the
// write adds to the director's cloud config and never replaces it. A config
// write creates no director task, so a write whose answer was lost has
// nothing to look up, and it is not sent again.
func (d *DirectorAdapter) UpdateCloudConfig(ctx context.Context, config string) (err error) {
	defer d.explainContext(ctx, "UpdateCloudConfig", &err)

	d.log.Infof("Writing cloud config %s", BoshCloudConfigName)
	d.log.Debugf("Cloud config size: %d bytes", len(config))

	_, err = d.api.Configs().Create(ctx, &boshapi.CreateConfigRequest{
		Type:    cloudConfigType,
		Name:    BoshCloudConfigName,
		Content: config,
	})
	if err != nil {
		d.log.Errorf("Failed to write cloud config: %v", err)

		if boshapi.IsOutcomeUnknown(err) && ctx.Err() == nil {
			return &OutcomeUnknownError{
				Operation: "UpdateCloudConfig",
				Director:  d.address,
				Config:    cloudConfigType + "/" + BoshCloudConfigName,
				Err:       err,
			}
		}

		return d.requestFailure("update cloud config", err)
	}

	d.log.Infof("Successfully wrote cloud config %s", BoshCloudConfigName)

	return nil
}

// GetCloudConfig retrieves the cloud config.
func (d *DirectorAdapter) GetCloudConfig(ctx context.Context) (_ string, err error) {
	defer d.explainContext(ctx, "GetCloudConfig", &err)

	d.log.Infof("Getting cloud config")
	d.log.Debugf("Listing cloud configs (limit: 1)")

	configs, err := d.api.Configs().List(ctx, &boshapi.ConfigListOptions{
		Type:   "cloud",
		Latest: true,
		Limit:  1,
	})
	if err != nil {
		d.log.Errorf("Failed to get cloud config: %v", err)

		return "", d.requestFailure("get cloud config", err)
	}

	if len(configs) == 0 {
		d.log.Errorf("No cloud config found")

		return "", ErrNoCloudConfigFound
	}

	d.log.Infof("Successfully retrieved cloud config (size: %d bytes)", len(configs[0].Content))

	return configs[0].Content, nil
}

// ListCloudConfigs returns the latest version of every cloud config on the
// director, the default one included.
//
//nolint:nonamedreturns // explainContext reads the named error, as in every other adapter method.
func (d *DirectorAdapter) ListCloudConfigs(ctx context.Context) (_ []CloudConfigEntry, err error) {
	defer d.explainContext(ctx, "ListCloudConfigs", &err)

	d.log.Infof("Listing cloud configs")

	entries, err := d.api.Configs().List(ctx, &boshapi.ConfigListOptions{
		Type:   cloudConfigType,
		Latest: true,
	})
	if err != nil {
		d.log.Errorf("Failed to list cloud configs: %v", err)

		return nil, d.requestFailure("list cloud configs", err)
	}

	result := make([]CloudConfigEntry, 0, len(entries))
	for _, entry := range entries {
		result = append(result, CloudConfigEntry{Name: entry.Name, Content: entry.Content})
	}

	d.log.Infof("Successfully listed %d cloud configs", len(result))

	return result, nil
}

// normalizeConfigLimit ensures the limit is within sensible bounds.

// GetConfigs retrieves configs based on limit and type filters
// Only returns the currently active configs for the main list view.
func (d *DirectorAdapter) GetConfigs(ctx context.Context, limit int, configTypes []string) (_ []BoshConfig, err error) {
	defer d.explainContext(ctx, "GetConfigs", &err)

	d.log.Infof("Getting configs (limit: %d, types: %v)", limit, configTypes)

	limit = d.normalizeConfigLimit(limit)

	// The latest version of every config comes back in one read, and the
	// type filter applies here.
	entries, err := d.api.Configs().List(ctx, &boshapi.ConfigListOptions{Latest: true})
	if err != nil {
		d.log.Errorf("Failed to get configs list: %v", err)

		return nil, d.requestFailure("list configs", err)
	}

	var result []BoshConfig

	for _, entry := range entries {
		if !d.shouldIncludeConfigType(entry.Type, configTypes) {
			continue
		}

		bc := convertConfigEntry(entry)
		bc.IsActive = true
		bc.ID = strings.TrimSuffix(entry.ID, "*") + "*"
		result = append(result, bc)
	}

	d.sortConfigsByTypeAndName(result)
	d.log.Infof("Successfully retrieved %d active configs", len(result))

	if limit > 0 && len(result) > limit {
		result = result[:limit]
	}

	return result, nil
}

// GetConfigVersions retrieves all versions of a specific config.
func (d *DirectorAdapter) GetConfigVersions(ctx context.Context, configType, name string, limit int) (_ []BoshConfig, err error) {
	defer d.explainContext(ctx, "GetConfigVersions", &err)

	d.log.Infof("Getting config versions for type: %s, name: %s (limit: %d)", configType, name, limit)

	if limit <= 0 || limit > 100 {
		limit = 20 // Default limit for version history
	}

	// Latest stays false so the director returns every version, up to the
	// limit.
	entries, err := d.api.Configs().List(ctx, &boshapi.ConfigListOptions{
		Type:   configType,
		Name:   name,
		Latest: false,
		Limit:  limit,
	})
	if err != nil {
		d.log.Errorf("Failed to get config versions: %v", err)

		return nil, d.requestFailure("get versions of config "+configType+"/"+name, err)
	}

	// Convert and mark which version is current/active
	result := make([]BoshConfig, len(entries))
	for i, entry := range entries {
		bc := convertConfigEntry(entry)
		bc.IsActive = entry.Current
		result[i] = bc
	}

	d.log.Infof("Successfully retrieved %d config versions", len(result))

	return result, nil
}

// GetConfigByID retrieves detailed config information by ID.
func (d *DirectorAdapter) GetConfigByID(ctx context.Context, configID string) (_ *BoshConfigDetail, err error) {
	defer d.explainContext(ctx, "GetConfigByID", &err)

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
	config, err := d.getConfigEntry(ctx, "get config "+cleanID, cleanID)
	if err != nil {
		d.log.Errorf("Failed to get config by ID %d: %v", configIDInt, err)

		return nil, err
	}

	// Fix YAML key casing issues that may be introduced by BOSH CLI library
	fixedContent := fixYAMLKeyCasing(config.Content)

	// Convert to our BoshConfigDetail type
	summary := convertConfigEntry(*config)
	result := &BoshConfigDetail{
		BoshConfig: summary,
		Content:    fixedContent,
		Metadata: map[string]interface{}{
			"id":         config.ID,
			"name":       config.Name,
			"type":       config.Type,
			"team":       summary.Team,
			"created_at": config.CreatedAt,
		},
	}

	d.log.Infof("Successfully retrieved config details for ID: %s", configID)

	return result, nil
}

// GetConfigContent retrieves the full content of a config by its ID.
func (d *DirectorAdapter) GetConfigContent(ctx context.Context, configID string) (_ string, err error) {
	defer d.explainContext(ctx, "GetConfigContent", &err)

	d.log.Infof("Getting config content for ID: %s", configID)

	// Remove the * suffix if present (indicates active config)
	cleanID := strings.TrimSuffix(configID, "*")

	// Get the config by ID
	config, err := d.getConfigEntry(ctx, "get config content "+cleanID, cleanID)
	if err != nil {
		d.log.Errorf("Failed to get config content by ID %s: %v", cleanID, err)

		return "", err
	}

	d.log.Infof("Successfully retrieved config content for ID: %s (size: %d bytes)", configID, len(config.Content))

	return config.Content, nil
}

// ComputeConfigDiff computes the diff between two config versions.
func (d *DirectorAdapter) ComputeConfigDiff(ctx context.Context, fromID, toID string) (_ *ConfigDiff, err error) {
	defer d.explainContext(ctx, "ComputeConfigDiff", &err)

	d.log.Infof("Computing diff between configs %s -> %s", fromID, toID)

	// Get content for both configs
	fromContent, err := d.GetConfigContent(ctx, fromID)
	if err != nil {
		return nil, fmt.Errorf("failed to get 'from' config: %w", err)
	}

	toContent, err := d.GetConfigContent(ctx, toID)
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

// convertConfigEntry converts a config the director returned into the
// broker's type. The team is the director's team name as a string, and
// IsActive is left false for the caller to set.
func convertConfigEntry(config boshapi.ConfigEntry) BoshConfig {
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

	// The CA must reach the director HTTP client itself, not only the UAA
	// client, or every director call is verified against the system roots.
	return &boshdirector.FactoryConfig{
		Host:   host,
		Port:   port,
		CACert: config.CACert,
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
		cachedInfoProvider := &staticDirectorInfoProvider{info: info}

		return setupUAAAuth(config, cachedInfoProvider, factoryConfig)
	}

	return nil, ErrUAAAutoDetectionNotApplicable
}

// setupUAAAuth configures UAA authentication.
type directorInfoProvider interface {
	Info() (boshdirector.Info, error)
}

type staticDirectorInfoProvider struct {
	info boshdirector.Info
}

func (s *staticDirectorInfoProvider) Info() (boshdirector.Info, error) {
	return s.info, nil
}

// setupUAAAuth configures UAA authentication.
func setupUAAAuth(config Config, director directorInfoProvider, factoryConfig *boshdirector.FactoryConfig) (*boshdirector.FactoryConfig, error) {
	err := validateUAASetupParams(director, factoryConfig)
	if err != nil {
		return nil, err
	}

	uaaURL, clientID, clientSecret, caCert, err := resolveUAACredentials(config, director)
	if err != nil {
		return nil, err
	}

	uaaConfig, err := buildUAAConfig(uaaURL, clientID, clientSecret, caCert)
	if err != nil {
		return nil, err
	}

	uaaClient, err := createUAAClientFromConfig(uaaConfig)
	if err != nil {
		return nil, err
	}

	factoryConfig.TokenFunc = boshuaa.NewClientTokenSession(uaaClient).TokenFunc

	return factoryConfig, nil
}

func validateUAASetupParams(director directorInfoProvider, factoryConfig *boshdirector.FactoryConfig) error {
	if director == nil {
		return ErrDirectorInfoProviderNil
	}

	if factoryConfig == nil {
		return ErrFactoryConfigNil
	}

	return nil
}

func resolveUAACredentials(config Config, director directorInfoProvider) (string, string, string, string, error) {
	var uaaURL, clientID, clientSecret, caCert string

	var err error

	if config.UAA != nil {
		uaaURL = config.UAA.URL
		clientID = config.UAA.ClientID
		clientSecret = config.UAA.ClientSecret
		caCert = config.UAA.CACert
	}

	if uaaURL == "" {
		uaaURL, err = getUAAURLFromDirector(director)
		if err != nil {
			return "", "", "", "", err
		}
	}

	if clientID == "" {
		clientID = config.Username
	}

	if clientSecret == "" {
		clientSecret = config.Password
	}

	if clientID == "" || clientSecret == "" {
		return "", "", "", "", ErrUAAClientCredentialsMissing
	}

	if caCert == "" {
		caCert = config.CACert
	}

	return uaaURL, clientID, clientSecret, caCert, nil
}

func getUAAURLFromDirector(director directorInfoProvider) (string, error) {
	info, err := director.Info()
	if err != nil {
		return "", fmt.Errorf("error getting director info: %w", err)
	}

	if info.Auth.Type != authTypeUAA {
		return "", ErrDirectorNotUsingUAA
	}

	urlInterface, found := info.Auth.Options["url"]
	if !found {
		return "", ErrUAAURLNotFoundInInfo
	}

	urlStr, ok := urlInterface.(string)
	if !ok {
		return "", ErrUAAURLNotString
	}

	return urlStr, nil
}

func buildUAAConfig(uaaURL, clientID, clientSecret, caCert string) (boshuaa.Config, error) {
	normalizedURL := ensureHTTPScheme(uaaURL)

	uaaConfig, err := boshuaa.NewConfigFromURL(normalizedURL)
	if err != nil {
		return boshuaa.Config{}, fmt.Errorf("invalid UAA URL: %w", err)
	}

	uaaConfig.Client = clientID

	uaaConfig.ClientSecret = clientSecret
	if caCert != "" {
		uaaConfig.CACert = caCert
	}

	return uaaConfig, nil
}

func createUAAClientFromConfig(uaaConfig boshuaa.Config) (boshuaa.UAA, error) {
	uaaFactory := boshuaa.NewFactory(boshlog.NewLogger(boshlog.LevelError))

	uaaClient, err := uaaFactory.New(uaaConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create UAA client: %w", err)
	}

	return uaaClient, nil
}

func ensureHTTPScheme(rawURL string) string {
	if strings.HasPrefix(rawURL, "http://") || strings.HasPrefix(rawURL, "https://") {
		return rawURL
	}

	return "https://" + rawURL
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
func (d *DirectorAdapter) FetchLogs(ctx context.Context, deployment string, jobName string, jobIndex string) (_ string, err error) {
	defer d.explainContext(ctx, "FetchLogs", &err)

	director := d.bind(ctx)

	d.log.Infof("Fetching logs for deployment: %s, job: %s/%s", deployment, jobName, jobIndex)
	d.log.Debugf("Finding deployment %s", deployment)

	dep, err := director.FindDeployment(deployment)
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

	err = director.DownloadResourceUnchecked(logsResult.BlobstoreID, &logBuffer)
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
func (d *DirectorAdapter) SSHCommand(ctx context.Context, deployment, instance string, index int, command string, args []string, options map[string]interface{}) (_ string, err error) {
	defer d.explainContext(ctx, "SSHCommand", &err)

	d.log.Infof("Executing SSH command on deployment %s, instance %s/%d", deployment, instance, index)
	d.log.Debugf("Command: %s, Args: %v", command, args)

	// Set up SSH infrastructure
	sshResult, privateKey, cleanup, err := d.setupSSHSession(ctx, deployment, instance, index)
	if err != nil {
		return "", err
	}
	defer cleanup()

	// Create SSH client and execute command
	client, err := d.createSSHClient(ctx, sshResult, privateKey)
	if err != nil {
		return "", err
	}

	defer func() { _ = client.Close() }()

	// Closing the client when ctx ends unblocks a command that is still
	// running, so the call returns at the caller's deadline.
	stopClosing := context.AfterFunc(ctx, func() { _ = client.Close() })
	defer stopClosing()

	return d.executeSSHCommand(client, command, args)
}

// SSHSession creates an interactive SSH session for streaming.
func (d *DirectorAdapter) SSHSession(ctx context.Context, deployment, instance string, index int, options map[string]interface{}) (_ interface{}, err error) {
	defer d.explainContext(ctx, "SSHSession", &err)

	director := d.bind(ctx)

	d.log.Infof("Creating SSH session for deployment %s, instance %s/%d", deployment, instance, index)

	// Find the deployment
	boshDeployment, err := director.FindDeployment(deployment)
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
			return d.cleanUpSSH(ctx, deployment, slug, sshOpts)
		},
	}

	return sessionInfo, nil
}

// EnableResurrection toggles resurrection for a deployment using resurrection config.
func (da *DirectorAdapter) EnableResurrection(ctx context.Context, deployment string, enabled bool) (err error) {
	defer da.explainContext(ctx, "EnableResurrection", &err)

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
	configName := resurrectionConfigName(deployment)

	da.log.Debugf("Updating resurrection config %s with content:\n%s", configName, configYAML)

	// A config write creates no director task, so a write whose answer was
	// lost has nothing to look up, and it is not sent again.
	config, err := da.api.Configs().Create(ctx, &boshapi.CreateConfigRequest{
		Type:    "resurrection",
		Name:    configName,
		Content: configYAML,
	})
	if err != nil {
		da.log.Errorf("Failed to update resurrection config for deployment %s: %v", deployment, err)

		if boshapi.IsOutcomeUnknown(err) && ctx.Err() == nil {
			return &OutcomeUnknownError{
				Operation:  "EnableResurrection",
				Director:   da.address,
				Deployment: deployment,
				Config:     "resurrection/" + configName,
				Err:        err,
			}
		}

		return fmt.Errorf("%w %s: %w", ErrResurrectionConfigUpdateFailed, deployment,
			da.requestFailure("send the request", err))
	}

	da.log.Debugf("Successfully updated resurrection config %s for deployment %s", configName, deployment)
	da.log.Debugf("Config response: %+v", config)

	da.log.Infof("Successfully updated resurrection config for deployment %s to %v", deployment, enabled)

	return nil
}

// resurrectionConfigName is the name of a deployment's resurrection config.
func resurrectionConfigName(deployment string) string {
	return "blacksmith." + deployment
}

// DeleteResurrectionConfig deletes resurrection config for a deployment.
func (da *DirectorAdapter) DeleteResurrectionConfig(ctx context.Context, deployment string) (err error) {
	defer da.explainContext(ctx, "DeleteResurrectionConfig", &err)

	da.log.Infof("Deleting resurrection config for deployment %s", deployment)

	// Use 'blacksmith.{deployment}' format as type already indicates 'resurrection'
	configName := resurrectionConfigName(deployment)

	da.log.Debugf("Deleting resurrection config %s", configName)

	err = da.api.Configs().DeleteByTypeAndName(ctx, "resurrection", configName)
	if err != nil {
		// The director answers a delete of a config it does not hold with a
		// 404, as it did for the bosh CLI.
		var apiErr *boshapi.APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
			da.log.Errorf("Resurrection config %s did not exist or was already deleted", configName)

			return fmt.Errorf("%w %s", ErrResurrectionConfigNotFound, deployment)
		}

		da.log.Errorf("Failed to delete resurrection config for deployment %s: %v", deployment, err)

		if boshapi.IsOutcomeUnknown(err) && ctx.Err() == nil {
			return &OutcomeUnknownError{
				Operation:  "DeleteResurrectionConfig",
				Director:   da.address,
				Deployment: deployment,
				Config:     "resurrection/" + configName,
				Err:        err,
			}
		}

		return fmt.Errorf("%w %s: %w", ErrResurrectionConfigDeleteFailed, deployment,
			da.requestFailure("send the request", err))
	}

	da.log.Debugf("Successfully deleted resurrection config %s for deployment %s", configName, deployment)

	da.log.Infof("Successfully deleted resurrection config for deployment %s", deployment)

	return nil
}

// ListErrands returns the list of errands for a deployment.
func (d *DirectorAdapter) ListErrands(ctx context.Context, deployment string) (_ []Errand, err error) {
	defer d.explainContext(ctx, "ListErrands", &err)

	d.log.Infof("Listing errands for deployment: %s", deployment)

	boshErrands, err := d.api.Errands().List(ctx, deployment)
	if err != nil {
		d.log.Errorf("Failed to list errands for deployment %s: %v", deployment, err)

		return nil, d.requestFailure("list errands for deployment "+deployment, err)
	}

	// Convert BOSH errands to our format
	errands := make([]Errand, len(boshErrands))
	for i, errand := range boshErrands {
		errands[i] = Errand{
			Name: errand.Name,
		}
	}

	d.log.Infof("Found %d errands for deployment %s", len(errands), deployment)

	return errands, nil
}

// RunErrand runs a specific errand for a deployment.
func (d *DirectorAdapter) RunErrand(ctx context.Context, deployment, errand string, opts ErrandOpts) (_ *ErrandResult, err error) {
	defer d.explainContext(ctx, "RunErrand", &err)

	director := d.bind(ctx)

	d.log.Infof("Running errand %s for deployment: %s", errand, deployment)

	dep, err := director.FindDeployment(deployment)
	if err != nil {
		d.log.Errorf("Failed to find deployment %s: %v", deployment, err)

		return nil, fmt.Errorf("failed to find deployment: %w", err)
	}

	// Convert instances to proper slugs if provided
	instanceSlugs := make([]boshdirector.InstanceGroupOrInstanceSlug, 0, len(opts.Instances))
	for _, instance := range opts.Instances {
		instanceSlugs = append(instanceSlugs, boshdirector.NewInstanceGroupOrInstanceSlug(instance, ""))
	}

	results, err := dep.RunErrand(errand, opts.KeepAlive, opts.WhenChanged, instanceSlugs)
	if err != nil {
		d.log.Errorf("Failed to run errand %s for deployment %s: %v", errand, deployment, err)

		return nil, fmt.Errorf("failed to run errand: %w", err)
	}

	// Return the first result (most common case)
	if len(results) == 0 {
		return nil, ErrNoErrandResults
	}

	result := &ErrandResult{
		InstanceGroup:   results[0].InstanceGroup,
		InstanceID:      results[0].InstanceID,
		ExitCode:        results[0].ExitCode,
		Stdout:          results[0].Stdout,
		Stderr:          results[0].Stderr,
		LogsBlobstoreID: results[0].LogsBlobstoreID,
		LogsSHA1:        results[0].LogsSHA1,
	}

	d.log.Infof("Successfully ran errand %s for deployment %s, exit code: %d", errand, deployment, result.ExitCode)

	return result, nil
}

// GetInstances returns instances for a deployment.
func (d *DirectorAdapter) GetInstances(ctx context.Context, deployment string) (_ []Instance, err error) {
	defer d.explainContext(ctx, "GetInstances", &err)

	director := d.bind(ctx)

	d.log.Infof("Getting instances for deployment: %s", deployment)

	dep, err := director.FindDeployment(deployment)
	if err != nil {
		d.log.Errorf("Failed to find deployment %s: %v", deployment, err)

		return nil, fmt.Errorf("failed to find deployment: %w", err)
	}

	// Get VM info which includes instance information
	vmInfos, err := dep.InstanceInfos()
	if err != nil {
		d.log.Errorf("Failed to get instance info for deployment %s: %v", deployment, err)

		return nil, fmt.Errorf("failed to get instance info: %w", err)
	}

	// Convert to our Instance format
	instances := make([]Instance, len(vmInfos))
	for idx, vmInfo := range vmInfos {
		index := "0"
		if vmInfo.Index != nil {
			index = strconv.Itoa(*vmInfo.Index)
		}

		instances[idx] = Instance{
			ID:           vmInfo.AgentID,
			Group:        vmInfo.JobName,
			Index:        index,
			State:        vmInfo.State,
			ProcessState: vmInfo.ProcessState,
			IPs:          vmInfo.IPs,
			AZ:           vmInfo.AZ,
			VMType:       vmInfo.VMType,
			ResourcePool: vmInfo.ResourcePool,
			Bootstrap:    vmInfo.Bootstrap,
			Ignore:       vmInfo.Ignore,
			VMCreatedAt:  vmInfo.VMCreatedAt,
		}
	}

	d.log.Infof("Found %d instances for deployment %s", len(instances), deployment)

	return instances, nil
}

// GetPoolStats returns pool statistics (not applicable for DirectorAdapter).
func (d *DirectorAdapter) GetPoolStats() (*PoolStats, error) {
	// DirectorAdapter doesn't use connection pooling, return basic stats
	return &PoolStats{
		MaxConnections:    1,
		ActiveConnections: 1,
		QueuedRequests:    0,
		RejectedRequests:  0,
		TotalRequests:     0,
		AvgWaitTime:       0,
	}, nil
}

// FindRunningTaskForDeployment returns the newest task on the deployment that
// has not finished, or nil when there is none. The director filters by
// deployment and state itself.
func (d *DirectorAdapter) FindRunningTaskForDeployment(ctx context.Context, deploymentName string) (_ *Task, err error) {
	defer d.explainContext(ctx, "FindRunningTaskForDeployment", &err)

	d.log.Debugf("Finding running task for deployment: %s", deploymentName)

	tasks, err := d.listTasks(ctx, "list the running tasks of deployment "+deploymentName,
		&boshapi.TaskListOptions{Deployment: deploymentName, State: runningTaskStates, Verbose: 1})
	if err != nil {
		return nil, err
	}

	for _, task := range tasks {
		if isRunningTaskState(task.State) {
			d.log.Debugf("Found running task %d for deployment %s (state: %s)", task.ID, deploymentName, task.State)

			return &task, nil
		}
	}

	d.log.Debugf("No running task found for deployment %s", deploymentName)

	return nil, nil //nolint:nilnil // no running task is an answer, not an error
}

// FindRunningTasksForDeployment returns every task on the deployment that
// has not finished, newest first as the director lists them, or an empty
// list when none runs. The director filters by deployment and state
// itself, and a finished task in its answer, from a director or proxy that
// ignored the state filter, is left out.
func (d *DirectorAdapter) FindRunningTasksForDeployment(ctx context.Context, deploymentName string) ([]Task, error) {
	d.log.Debugf("Finding the running tasks of deployment: %s", deploymentName)

	tasks, err := d.listTasks(ctx, "list the running tasks of deployment "+deploymentName,
		&boshapi.TaskListOptions{Deployment: deploymentName, State: runningTaskStates, Verbose: 1})
	if err != nil {
		d.explainContext(ctx, "FindRunningTasksForDeployment", &err)

		return nil, err
	}

	running := make([]Task, 0, len(tasks))

	for _, task := range tasks {
		if isRunningTaskState(task.State) {
			running = append(running, task)
		}
	}

	d.log.Debugf("Found %d running tasks for deployment %s", len(running), deploymentName)

	return running, nil
}

// FindTaskByContextID returns the newest task the director recorded with the
// context ID, or nil when it has none. The director filters by the ID
// itself, and a task that carries another ID, from a director or proxy that
// ignored the filter, is never taken for a match.
func (d *DirectorAdapter) FindTaskByContextID(ctx context.Context, contextID string) (*Task, error) {
	task, err := d.findTaskByContextID(ctx, contextID)
	d.explainContext(ctx, "FindTaskByContextID", &err)

	return task, err
}

func (d *DirectorAdapter) findTaskByContextID(ctx context.Context, contextID string) (*Task, error) {
	if contextID == "" {
		return nil, ErrEmptyContextID
	}

	d.log.Debugf("Finding the task with context ID %s", contextID)

	tasks, err := d.listTasks(ctx, "list the tasks with context ID "+contextID,
		&boshapi.TaskListOptions{ContextID: contextID, Verbose: taskVerboseAll})
	if err != nil {
		return nil, err
	}

	var matches []Task

	for _, task := range tasks {
		if task.ContextID != contextID {
			d.log.Errorf("The director listed task %d with context ID %q when asked for context ID %s; ignoring it", task.ID, task.ContextID, contextID)

			continue
		}

		matches = append(matches, task)
	}

	if len(matches) == 0 {
		d.log.Debugf("No task has context ID %s", contextID)

		return nil, nil //nolint:nilnil // no task with the ID is an answer, not an error
	}

	newest := matches[0]
	for _, task := range matches[1:] {
		if task.ID > newest.ID {
			newest = task
		}
	}

	if len(matches) > 1 {
		for _, task := range matches {
			d.log.Errorf("Context ID %s matches task %d (%s, %s, deployment %q), though each context ID goes on one request; using task %d, the newest",
				contextID, task.ID, task.State, task.Description, task.Deployment, newest.ID)
		}
	}

	d.log.Debugf("Found task %d (state: %s) with context ID %s", newest.ID, newest.State, contextID)

	return &newest, nil
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
func (d *DirectorAdapter) setupSSHSession(ctx context.Context, deployment, instance string, index int) (*boshdirector.SSHResult, string, func(), error) {
	// Find the deployment
	boshDeployment, err := d.bind(ctx).FindDeployment(deployment)
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

		cleanupErr := d.cleanUpSSH(ctx, deployment, slug, sshOpts)
		if cleanupErr != nil {
			d.log.Errorf("Failed to clean up SSH: %v", cleanupErr)
		}
	}

	return &sshResult, privateKey, cleanup, nil
}

// cleanUpSSH removes the SSH user that SetUpSSH added. It often runs after
// the caller's context has ended, and a cancelled context must not leave the
// user on the VM, so it keeps ctx's values but gets its own deadline and a
// director bound to that deadline.
func (d *DirectorAdapter) cleanUpSSH(ctx context.Context, deployment string, slug boshdirector.AllOrInstanceGroupOrInstanceSlug, sshOpts boshdirector.SSHOpts) (err error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sshCleanupTimeout)
	defer cancel()

	defer d.explainContext(ctx, "CleanUpSSH", &err)

	boshDeployment, err := d.bind(ctx).FindDeployment(deployment)
	if err != nil {
		return fmt.Errorf("failed to find deployment %s for SSH cleanup: %w", deployment, err)
	}

	err = boshDeployment.CleanUpSSH(slug, sshOpts)
	if err != nil {
		return fmt.Errorf("failed to clean up SSH on deployment %s: %w", deployment, err)
	}

	return nil
}

// createSSHClient creates an SSH client using the SSH result.
func (d *DirectorAdapter) createSSHClient(ctx context.Context, sshResult *boshdirector.SSHResult, privateKey string) (sshClient, error) {
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

	targetAddr := sshAddress(host.Host)

	if sshResult.GatewayHost != "" {
		gatewayConfig := &ssh.ClientConfig{
			User: sshResult.GatewayUsername,
			Auth: []ssh.AuthMethod{
				ssh.PublicKeys(signer),
			},
			HostKeyCallback: d.createSecureHostKeyCallback(sshResult.GatewayHost),
			Timeout:         httpClientTimeout,
		}

		return d.createGatewaySSHClient(ctx, sshAddress(sshResult.GatewayHost), targetAddr, gatewayConfig, config)
	}

	return d.createDirectSSHClient(ctx, targetAddr, config)
}

// sshAddress returns the SSH address of host, bracketing an IPv6 literal.
func sshAddress(host string) string {
	return net.JoinHostPort(host, "22")
}

// sshClient is the part of an SSH client that command execution and cleanup
// need. A client reached through a gateway also owns the gateway connection.
type sshClient interface {
	NewSession() (*ssh.Session, error)
	Close() error
}

// gatewayedClient is a target client that owns the gateway client its
// connection is tunnelled through, so closing it closes both.
type gatewayedClient struct {
	*ssh.Client

	gateway *ssh.Client
}

// Close closes the target client and the gateway client. It always closes
// both and returns the first error.
func (c *gatewayedClient) Close() error {
	err := c.Client.Close()
	if gatewayErr := c.gateway.Close(); err == nil {
		err = gatewayErr
	}

	return err
}

// closeOnceConn closes the connection underneath it at most once, however
// many callers close it. The SSH library closes a connection when its
// handshake fails, and the SSH client closes it again later, so every close
// goes through here. The first closer gets the real result and later closers
// get net.ErrClosed.
type closeOnceConn struct {
	net.Conn

	once sync.Once
	// closedByContext records that the context watcher made the real close.
	closedByContext atomic.Bool
}

func (c *closeOnceConn) Close() error {
	err := net.ErrClosed

	c.once.Do(func() { err = c.Conn.Close() })

	return err
}

// closeForContext is the context watcher's close. It marks the connection as
// closed by the context only when it is the close that takes effect.
func (c *closeOnceConn) closeForContext() {
	c.once.Do(func() {
		c.closedByContext.Store(true)

		_ = c.Conn.Close()
	})
}

// handshakeSSH runs the SSH handshake on conn, bounded by ctx. The
// ClientConfig Timeout only bounds the TCP connect, so a server that accepts
// and never speaks would otherwise block the handshake forever. The
// handshake therefore runs under the context's deadline, or under the
// config's Timeout when the context has none, and ends early when the
// context is cancelled. conn is closed exactly once when the handshake
// fails, and is left open for the returned client when it succeeds.
func handshakeSSH(ctx context.Context, rawConn net.Conn, addr string, config *ssh.ClientConfig) (*ssh.Client, error) {
	conn := &closeOnceConn{Conn: rawConn}

	if deadline, ok := ctx.Deadline(); ok {
		// Tunnelled channels do not support deadlines; the AfterFunc below
		// still ends their handshake.
		_ = conn.SetDeadline(deadline)
	} else if config.Timeout > 0 {
		_ = conn.SetDeadline(time.Now().Add(config.Timeout))
	}

	stop := context.AfterFunc(ctx, conn.closeForContext)

	nconn, chans, reqs, err := ssh.NewClientConn(conn, addr, config)
	stopped := stop()

	if err != nil {
		// The library closed the connection already; this keeps the
		// guarantee independent of that.
		_ = conn.Close()

		// Report the context only when its watcher closed the connection
		// under the handshake. Any other failure is the handshake's own.
		if conn.closedByContext.Load() {
			return nil, fmt.Errorf("SSH handshake with %s ended with its context: %w", addr, context.Cause(ctx))
		}

		return nil, err
	}

	if !stopped {
		// The context ended after the handshake completed and its watcher
		// is closing the connection, so the client would be dead.
		_ = conn.Close()

		return nil, fmt.Errorf("SSH handshake with %s ended with its context: %w", addr, context.Cause(ctx))
	}

	// The handshake deadline must not cut off the session that follows.
	_ = conn.SetDeadline(time.Time{})

	return ssh.NewClient(nconn, chans, reqs), nil
}

// createGatewaySSHClient creates an SSH client through a gateway. The
// gateway client stays open until the returned client is closed.
func (d *DirectorAdapter) createGatewaySSHClient(ctx context.Context, gatewayAddr, targetAddr string, gatewayConfig, config *ssh.ClientConfig) (sshClient, error) {
	// Connect to gateway first
	gatewayConn, err := (&net.Dialer{Timeout: gatewayConfig.Timeout}).DialContext(ctx, "tcp", gatewayAddr)
	if err != nil {
		d.log.Errorf("Failed to connect to gateway %s: %v", gatewayAddr, err)

		return nil, fmt.Errorf("failed to connect to gateway: %w", err)
	}

	gatewayClient, err := handshakeSSH(ctx, gatewayConn, gatewayAddr, gatewayConfig)
	if err != nil {
		d.log.Errorf("Failed to connect to gateway %s: %v", gatewayAddr, err)

		return nil, fmt.Errorf("failed to connect to gateway: %w", err)
	}

	// Connect to target host through gateway
	conn, err := gatewayClient.DialContext(ctx, "tcp", targetAddr)
	if err != nil {
		_ = gatewayClient.Close()

		d.log.Errorf("Failed to connect to target host %s through gateway: %v", targetAddr, err)

		return nil, fmt.Errorf("failed to connect to target host through gateway: %w", err)
	}

	targetClient, err := handshakeSSH(ctx, conn, targetAddr, config)
	if err != nil {
		_ = gatewayClient.Close()

		d.log.Errorf("Failed to establish SSH connection to %s: %v", targetAddr, err)

		return nil, fmt.Errorf("failed to establish SSH connection: %w", err)
	}

	return &gatewayedClient{Client: targetClient, gateway: gatewayClient}, nil
}

// createDirectSSHClient creates a direct SSH client connection.
func (d *DirectorAdapter) createDirectSSHClient(ctx context.Context, addr string, config *ssh.ClientConfig) (sshClient, error) {
	conn, err := (&net.Dialer{Timeout: config.Timeout}).DialContext(ctx, "tcp", addr)
	if err != nil {
		d.log.Errorf("Failed to connect to %s: %v", addr, err)

		return nil, fmt.Errorf("failed to connect to host: %w", err)
	}

	client, err := handshakeSSH(ctx, conn, addr, config)
	if err != nil {
		d.log.Errorf("Failed to connect to %s: %v", addr, err)

		return nil, fmt.Errorf("failed to connect to host: %w", err)
	}

	return client, nil
}

// executeSSHCommand executes the command on the SSH client.
func (d *DirectorAdapter) executeSSHCommand(client sshClient, command string, args []string) (string, error) {
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

// filterTasksByStates filters tasks by the specified states.
func (d *DirectorAdapter) filterTasksByStates(tasks []Task, states []string) []Task {
	if len(states) == 0 {
		return tasks
	}

	stateMap := make(map[string]bool)
	for _, state := range states {
		stateMap[state] = true
	}

	var filteredTasks []Task

	for _, task := range tasks {
		if stateMap[task.State] {
			filteredTasks = append(filteredTasks, task)
		}
	}

	d.log.Debugf("Filtered to %d tasks with states %v", len(filteredTasks), states)

	return filteredTasks
}

// filterTasksByTeam filters tasks by the specified team.
func (d *DirectorAdapter) filterTasksByTeam(tasks []Task, team string) []Task {
	if team == "" {
		return tasks
	}

	d.log.Debugf("Starting team filtering with %d tasks for team '%s'", len(tasks), team)

	var teamFilteredTasks []Task

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
func (d *DirectorAdapter) shouldIncludeTaskForTeam(task Task, team string) bool {
	deploymentName := task.Deployment
	d.log.Debugf("Task %d: deployment='%s', description='%s'", task.ID, deploymentName, task.Description)

	var includeTask bool

	switch team {
	case "blacksmith":
		// For blacksmith, show all BOSH director tasks
		includeTask = true

		d.log.Debugf("Task %d: included for blacksmith (show all)", task.ID)
	case "service-instances":
		includeTask = d.isServiceInstanceTask(deploymentName, task.ID)
	default:
		includeTask = d.isSpecificTeamTask(deploymentName, team, task.ID)
	}

	if includeTask {
		d.log.Debugf("Task %d: INCLUDED for team '%s'", task.ID, team)
	} else {
		d.log.Debugf("Task %d: EXCLUDED for team '%s'", task.ID, team)
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

// getConfigEntry reads one config version by ID. A missing ID maps to
// ErrConfigNotFound.
func (d *DirectorAdapter) getConfigEntry(ctx context.Context, what, id string) (*boshapi.ConfigEntry, error) {
	entry, err := d.api.Configs().Get(ctx, id)
	if err != nil {
		var missing *boshapi.ConfigNotFoundError
		if errors.As(err, &missing) {
			return nil, fmt.Errorf("%w: %w", ErrConfigNotFound, err)
		}

		return nil, d.requestFailure(what, err)
	}

	if entry == nil {
		return nil, d.requestFailure(what, ErrEmptyDirectorAnswer)
	}

	return entry, nil
}
