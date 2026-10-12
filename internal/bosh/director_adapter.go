package bosh

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"blacksmith/internal/bosh/sshconn"
	"github.com/fivetwenty-io/boshapi/pkg/boshapi"
	"github.com/geofffranks/spruce"
	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
	"gopkg.in/yaml.v2"
)

// Error definitions.
var (
	ErrNoErrandResults       = errors.New("no errand results returned")
	ErrInvalidErrandInstance = errors.New("the errand instance is not a group or a group/ID")
)

// Constants for director adapter operations.
const (
	// Task fetch multiplier for filtering.
	taskFetchMultiplier = 2

	// Config priority values.
	configPriorityCPI          = 2
	configPriorityResurrection = 3
	configPriorityRuntime      = 4

	// HTTP client timeouts.
	httpClientTimeout    = 30 * time.Second
	httpTransportTimeout = 30 * time.Second

	// sshCleanupTimeout bounds the director call that removes an SSH user.
	sshCleanupTimeout = 30 * time.Second
	// sshSetupTimeout bounds the wait for an SSH setup task and the read
	// of its result, and nothing after it. A caller whose context never
	// ends, as the async SSH command's does, still gets an answer, and a
	// setup task still queued at the bound is cancelled.
	sshSetupTimeout = 3 * time.Minute

	// AuthTypeUAA is the user_authentication type of a director that uses
	// UAA.
	AuthTypeUAA = "uaa"
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
	ErrResurrectionConfigUpdateFailed = errors.New("failed to update resurrection config for deployment")
	ErrResurrectionConfigDeleteFailed = errors.New("failed to delete resurrection config for deployment")
	ErrResurrectionConfigNotFound     = errors.New("resurrection config for deployment not found or already deleted")
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

// DirectorAdapter implements the Director interface. Every method calls the
// director through the boshapi client.
type DirectorAdapter struct {
	// api is the boshapi client every method calls the director through.
	api            boshapi.Client
	address        string        // director address, for errors
	requestTimeout time.Duration // the bosh.request_timeout in force, for errors
	// taskPollInterval is how often WaitForTask reads a task. Zero means
	// boshapi's default of 2 seconds.
	taskPollInterval time.Duration
	// sshPort is the port SSHCommand connects to on a VM. It is
	// sshconn.Port everywhere but in tests, which point it at a loopback
	// server.
	sshPort string
	// ssh holds the store SSH setups record their users in, the users
	// whose setup, session, or cleanup this process is running, and the
	// shutdown gate that SSH setups check before they are sent.
	ssh sshUsers
	// lostSetupGrace and setupSettleTimeout bound the wait for a failed
	// SSH setup to end before its cleanup. Zero means lostSetupGrace and
	// sshSetupSettleTimeout.
	lostSetupGrace     time.Duration
	setupSettleTimeout time.Duration
	// setupTimeout bounds the wait for an SSH setup task. Zero means
	// sshSetupTimeout.
	setupTimeout time.Duration
	// lateCleanupTimeout bounds the send of the cleanup of a setup that
	// ended done after ShutdownSSH closed the gate. Zero means
	// lateSSHCleanupTimeout.
	lateCleanupTimeout time.Duration
	log                Logger // Application logger for Info/Debug logging
}

// NewDirectorAdapter creates a Director that calls the director at
// config.Address through boshapi. It sends no request. How to authenticate
// is read from the director's GET /info on the first call, under that call's
// context, so a director that uses UAA and one that uses HTTP Basic both
// work with bosh.username and bosh.password.
//
// It refuses missing credentials, then an empty address, then an http://
// address that config.AllowHTTP does not allow, then an empty CA, and then a
// CA that holds no PEM certificate, and each error names the bosh setting
// at fault. A director that does not answer is not refused
// here, and the first call explains that failure.
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

	err = validateDirectorSettings(config)
	if err != nil {
		appLogger.Errorf("Refused the director settings: %v", err)

		return nil, err
	}

	appLogger.Debugf("Creating boshapi client")

	api, err := newAPIClient(config, auth, appLogger)
	if err != nil {
		appLogger.Errorf("Failed to create boshapi client: %v", err)

		return nil, err
	}

	appLogger.Infof("Successfully created BOSH director adapter")

	return &DirectorAdapter{
		api:            api,
		address:        config.Address,
		requestTimeout: config.RequestTimeout,
		sshPort:        sshconn.Port,
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
	// Features maps each feature name to its status.
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

// GetDeploymentVMs lists a deployment's VMs with full details. The director
// lists them in a read-only task, so the listing waits for that task as
// waitOrCancel does and reads the task's result. Only the director's
// deployment-not-found answer, code 70000, returns ErrDeploymentNotFound.
// A plain 404 is a routing problem and stays an error that says so.
func (d *DirectorAdapter) GetDeploymentVMs(ctx context.Context, deployment string) (_ []VM, err error) {
	defer d.explainContext(ctx, "GetDeploymentVMs", &err)

	d.log.Infof("Getting VMs for deployment: %s", deployment)

	// The logs endpoint lists VMs under the deadline it keeps before the
	// write timeout, so the cancel of the listing's task gets the shorter
	// bound.
	details, err := d.listDetails(ctx, detailsListing{
		operation:   "GetDeploymentVMs",
		resource:    "vms",
		list:        d.api.VMs().ListDetailed,
		cancelBound: deadlineCancelTimeout,
	}, deployment)
	if err != nil {
		return nil, err
	}

	vms := make([]VM, len(details))
	for index, vm := range details {
		vms[index] = d.convertVMInfo(vm)
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

// taskOutputResult is the task output that holds a task's result.
const taskOutputResult = "result"

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

		return &cancelRefusedError{
			message: fmt.Sprintf("%v: BOSH task %d on BOSH director %s ended in state %q before the cancel took effect, "+
				"so there is nothing left to cancel. Check its result at /b/tasks/%d",
				ErrTaskCannotBeCancelled, taskID, d.address, task.State, taskID),
			state: &boshapi.TaskStateError{TaskID: taskID, State: task.State},
		}
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

// cancelRefusedError is a cancel that found its task already ended after
// the cancel's answer was lost. It matches ErrTaskCannotBeCancelled and
// carries the state the read found as a *boshapi.TaskStateError, as the
// director's own refusal does. Its message is its own, because the
// director never answered the cancel.
type cancelRefusedError struct {
	message string
	state   *boshapi.TaskStateError
}

func (e *cancelRefusedError) Error() string {
	return e.message
}

func (e *cancelRefusedError) Unwrap() []error {
	return []error{ErrTaskCannotBeCancelled, e.state}
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

	// Convert to our BoshConfigDetail type
	summary := convertConfigEntry(*config)
	result := &BoshConfigDetail{
		BoshConfig: summary,
		Content:    config.Content,
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

// fetchLogsOperation is the operation a log fetch's errors name.
const fetchLogsOperation = "FetchLogs"

// FetchLogs fetches the job logs of one instance of a deployment. It starts
// the director's fetch_logs task, waits for it, and streams the log bundle
// the task produced from the director's blobstore, keeping only the regular
// log files in it. A context that ends cancels the task and stops the
// download. budget is what the caller's request may still keep, and every
// byte a file keeps comes off it; a nil budget leaves the limit on each file
// as the only one. The director prunes the bundles in its blobstore itself,
// so Blacksmith deletes nothing.
//
//nolint:nonamedreturns // explainContext reads the named error, as in every other adapter method.
func (d *DirectorAdapter) FetchLogs(ctx context.Context, deployment, jobName, instanceID string, budget *LogBudget) (_ *LogBundle, err error) {
	defer d.explainContext(ctx, fetchLogsOperation, &err)

	d.log.Infof("Fetching logs for deployment: %s, job: %s/%s", deployment, jobName, instanceID)

	task, err := d.api.Instances().FetchLogs(ctx, deployment, jobName, instanceID, &boshapi.LogOptions{Type: "job"})
	if err == nil && task == nil {
		err = ErrEmptyDirectorAnswer
	}

	if err != nil {
		return nil, d.startFailure(ctx, taskStart{
			operation:  fetchLogsOperation,
			deployment: deployment,
			method:     http.MethodGet,
			path:       "/deployments/" + deployment + "/jobs/" + jobName + "/" + instanceID + "/logs?type=job",
			what:       fmt.Sprintf("fetch the logs of %s/%s in deployment %s", jobName, instanceID, deployment),
		}, err)
	}

	d.log.Debugf("The director fetches the logs of %s/%s in deployment %s in BOSH task %d", jobName, instanceID, deployment, task.ID)

	// A log fetch runs under the logs endpoint's deadline, so the cancel of
	// its task gets the shorter bound.
	finished, err := d.waitOrCancel(ctx, fetchLogsOperation, task.ID, deadlineCancelTimeout)
	if err != nil {
		return nil, err
	}

	if finished.State != taskStateDone {
		return nil, d.taskFailed("Log fetch", fetchLogsOperation, deployment, finished)
	}

	// The blobstore ID is the result field of the task record. The task's
	// result output is a different thing, and it is never read here.
	blobID := strings.TrimSpace(finished.Result)
	if blobID == "" {
		d.log.Errorf("BOSH task %d for the logs of %s/%s in deployment %s finished in state done with no blobstore ID",
			finished.ID, jobName, instanceID, deployment)

		return nil, &noLogBlobError{director: d.address, deployment: deployment, job: jobName, instance: instanceID, taskID: finished.ID}
	}

	d.log.Infof("Fetched the logs of %s/%s in deployment %s (blobstore ID: %s)", jobName, instanceID, deployment, blobID)

	body, err := d.api.Resources().Download(ctx, blobID)
	if err != nil {
		d.log.Errorf("Failed to download logs from blobstore %s: %v", blobID, err)

		return nil, d.requestFailure(fmt.Sprintf("download the log bundle %s of BOSH task %d", blobID, finished.ID), err)
	}

	defer func() { _ = body.Close() }()

	bundle, err := extractLogsFromTarGz(body, budget)
	if err != nil {
		d.log.Errorf("Failed to extract logs from archive %s: %v", blobID, err)

		return nil, fmt.Errorf("failed to extract the log bundle %s of BOSH task %d from BOSH director %s: %w", blobID, finished.ID, d.address, err)
	}

	d.log.Debugf("Read the log bundle %s: %d files kept, %d skipped, stopped early: %t",
		blobID, len(bundle.Files), bundle.SkippedCount, bundle.StoppedEarly)

	return bundle, nil
}

// extractLogsFromTarGz reads a gzipped tar of log files from data as it
// streams. Nothing in the archive is written to disk. It keeps only regular
// files whose name ends in .log or holds /log/, and of those at most
// maxLogFileSize bytes of each, the first ones, and it records each file it
// cut. It reads at most maxLogReadBytes of the decompressed tar, and a
// bundle that holds more is marked as stopped early. A name that is not
// UTF-8 is made valid, and a name longer than maxLogNameBytes is cut and
// marked. A log file that would overrun budget, that no longer fits in
// maxLogFiles, or whose name repeats an earlier file's is skipped and
// listed. The budget is charged for the files the bundle returns, and a
// read that fails returns no bundle and charges nothing.
func extractLogsFromTarGz(data io.Reader, budget *LogBudget) (*LogBundle, error) {
	gzReader, err := gzip.NewReader(data)
	if err != nil {
		return nil, fmt.Errorf("failed to create gzip reader: %w", err)
	}

	defer func() { _ = gzReader.Close() }()

	tarReader := tar.NewReader(&logReadLimiter{reader: gzReader, remaining: maxLogReadBytes})
	extraction := &logExtraction{bundle: &LogBundle{Files: make(map[string]string)}, budget: budget}

	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			return extraction.finish(), nil
		}

		if errors.Is(err, errLogReadLimit) {
			extraction.bundle.StoppedEarly = true

			return extraction.finish(), nil
		}

		if err != nil {
			return nil, fmt.Errorf("failed to read tar archive: %w", err)
		}

		name, isLog := logFileName(header)
		if !isLog || !extraction.admit(name, header.Size) {
			continue
		}

		// Limit the read size to prevent memory issues.
		content, err := io.ReadAll(io.LimitReader(tarReader, min(header.Size, maxLogFileSize)))
		if errors.Is(err, errLogReadLimit) {
			extraction.bundle.skip(name)
			extraction.bundle.StoppedEarly = true

			return extraction.finish(), nil
		}

		if err != nil {
			return nil, fmt.Errorf("failed to read file %s: %w", name, err)
		}

		extraction.keep(name, string(content), header.Size > maxLogFileSize)
	}
}

// logFileName returns the name a tar entry is kept under, and whether the
// entry is a log file at all: a regular file whose name ends in .log or
// holds /log/. Links, devices, FIFOs, and directories carry no file content.
// The name is cleaned, made valid UTF-8, and cut to maxLogNameBytes.
func logFileName(header *tar.Header) (string, bool) {
	if header.Typeflag != tar.TypeReg {
		return "", false
	}

	if !strings.HasSuffix(header.Name, ".log") && !strings.Contains(header.Name, "/log/") {
		return "", false
	}

	cleaned := path.Clean(header.Name)
	if cleaned == "." || cleaned == "/" {
		return "", false
	}

	// encoding/json writes each byte that is not UTF-8 as U+FFFD, so two
	// names that differ only there would be one key in the response. Made
	// valid here, they meet as a repeated name.
	return cutLogName(strings.ToValidUTF8(cleaned, "\uFFFD")), true
}

// logExtraction is one bundle as it is read. spent is what the files kept so
// far take of budget, which is charged only once the bundle is read.
type logExtraction struct {
	bundle *LogBundle
	budget *LogBudget
	spent  int64
}

// admit says whether a log file of the given size may be read, and records
// the file as skipped when it may not. A file is weighed at the size it
// keeps, which is no more than maxLogFileSize, and as it would take no more
// than one byte of JSON for each byte, which keep then checks.
func (e *logExtraction) admit(name string, size int64) bool {
	_, repeated := e.bundle.Files[name]

	switch {
	case repeated, len(e.bundle.Files) >= maxLogFiles:
		e.bundle.skip(name)

		return false
	case e.budget != nil && min(size, maxLogFileSize) > e.budget.Remaining-e.spent:
		e.bundle.skip(name)

		return false
	default:
		return true
	}
}

// keep adds a file that was read to the bundle, unless its size as JSON
// would overrun budget, and then it is skipped and listed. cut says the file
// was larger than what was read.
func (e *logExtraction) keep(name, content string, cut bool) {
	charge := logFileCharge(name, content)

	if e.budget != nil && charge > e.budget.Remaining-e.spent {
		e.bundle.skip(name)

		return
	}

	e.spent += charge
	e.bundle.Files[name] = content

	if cut {
		e.bundle.cut(name)
	}
}

// finish charges budget for the files the bundle kept and returns it.
func (e *logExtraction) finish() *LogBundle {
	if e.budget != nil {
		e.budget.Remaining -= e.spent
	}

	return e.bundle
}

// SSHCommand runs a one-off command on one instance over SSH. It sets up a
// throwaway user through the director, runs the command as that user, and
// removes the user before it returns.
func (d *DirectorAdapter) SSHCommand(ctx context.Context, deployment, instance string, index int, command string, args []string, options map[string]interface{}) (_ string, err error) {
	defer d.explainContext(ctx, "SSHCommand", &err)

	target, err := sshTarget(instance, index)
	if err != nil {
		return "", err
	}

	d.log.Infof("Executing SSH command on deployment %s, instance %s/%d", deployment, instance, index)
	d.log.Debugf("Command: %s, Args: %v", command, args)

	session, err := d.setUpSSH(ctx, "SSHCommand", deployment, target)
	if err != nil {
		return "", err
	}

	// cleanUpSSH logs every way the removal can fail, with the user and
	// the task, and the command's own result is what the caller asked for.
	defer func() { _ = session.Cleanup(ctx) }()

	client, err := d.createSSHClient(ctx, session)
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

// SSHSession sets up a throwaway SSH user on one instance for an
// interactive terminal. The answer is always a *sshconn.Session, whose
// Cleanup removes the user. The return type stays interface{} because the
// Director interface and its wrappers share this signature.
func (d *DirectorAdapter) SSHSession(ctx context.Context, deployment, instance string, index int, options map[string]interface{}) (_ interface{}, err error) {
	defer d.explainContext(ctx, "SSHSession", &err)

	target, err := sshTarget(instance, index)
	if err != nil {
		return nil, err
	}

	d.log.Infof("Creating SSH session for deployment %s, instance %s/%d", deployment, instance, index)

	session, err := d.setUpSSH(ctx, "SSHSession", deployment, target)
	if err != nil {
		return nil, err
	}

	return session, nil
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
		// 404.
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

// runErrandOperation is the operation an errand run's errors name.
const runErrandOperation = "RunErrand"

// RunErrand runs an errand on a deployment and waits for it to finish. The
// run carries a context ID, and a lost answer is looked up by that ID once,
// as a deploy's is, and never resent, because a second run of an errand
// may not be safe. A task that is found is followed as usual. When the
// caller's context ends during the wait, the errand's task is left running,
// because an errand can change state. A task that ends in any state but
// done returns a TaskFailedError with the director's result text. When the
// task is done but its result cannot be read, because the caller's context
// ended or the director refused, the error is a DirectorCallError with the
// task's ID and TaskFinished, since the errand has run.
func (d *DirectorAdapter) RunErrand(ctx context.Context, deployment, errand string, opts ErrandOpts) (_ *ErrandResult, err error) {
	defer d.explainContext(ctx, runErrandOperation, &err)

	instances, err := errandInstances(opts.Instances)
	if err != nil {
		d.log.Errorf("Refused to run errand %s for deployment %s: %v", errand, deployment, err)

		return nil, err
	}

	ctx, contextID, err := mutationContext(ctx, runErrandOperation)
	if err != nil {
		return nil, err
	}

	d.log.Infof("Running errand %s for deployment %s (context ID %s)", errand, deployment, contextID)

	apiTask, err := d.api.Errands().Run(ctx, deployment, errand, &boshapi.RunErrandOptions{
		KeepAlive:   opts.KeepAlive,
		WhenChanged: opts.WhenChanged,
		Instances:   instances,
	})

	task, err := d.accepted(ctx, mutation{
		operation:  runErrandOperation,
		deployment: deployment,
		contextID:  contextID,
		what:       "run errand " + errand + " on deployment " + deployment,
		lookup:     true,
	}, apiTask, err)
	if err != nil {
		return nil, err
	}

	finished, err := d.WaitForTask(ctx, task.ID)
	if err != nil {
		var callErr *DirectorCallError
		if errors.As(err, &callErr) {
			callErr.Operation = runErrandOperation
		}

		return nil, err
	}

	if finished.State != taskStateDone {
		d.log.Errorf("Errand %s for deployment %s ended in state %s in BOSH task %d: %s", errand, deployment, finished.State, task.ID, finished.Result)

		return nil, &TaskFailedError{
			Operation:  runErrandOperation,
			Director:   d.address,
			Deployment: deployment,
			TaskID:     task.ID,
			State:      finished.State,
			Result:     finished.Result,
		}
	}

	results, err := d.errandResults(ctx, deployment, errand, task.ID)
	if err != nil {
		return nil, err
	}

	if len(results) == 0 {
		return d.noErrandResults(deployment, errand, opts, finished)
	}

	result := worstErrandResult(results)

	d.log.Infof("Ran errand %s for deployment %s in BOSH task %d on %d instances, worst exit code: %d (%s/%s)",
		errand, deployment, task.ID, len(result.Results), result.ExitCode, result.InstanceGroup, result.InstanceID)

	return result, nil
}

// errandInstances turns the instances an errand run names into the
// director's form. Each entry is trimmed and split on its first slash, and
// both halves are trimmed, so node/0 and node/<uuid> become a group and an
// ID, and a bare name stays a group. An empty entry, such as a trailing
// comma leaves, is dropped, because the director would read it as an
// instance with no name. An entry with a slash but no group, such as "/" or
// "/0", names nothing the director can match. It returns
// ErrInvalidErrandInstance, because dropping it could run the errand on
// instances the caller did not name.
func errandInstances(instances []string) ([]boshapi.ErrandInstance, error) {
	converted := make([]boshapi.ErrandInstance, 0, len(instances))

	for _, instance := range instances {
		entry := strings.TrimSpace(instance)
		if entry == "" {
			continue
		}

		group, id, _ := strings.Cut(entry, "/")

		group = strings.TrimSpace(group)
		if group == "" {
			return nil, fmt.Errorf("%w: %q has no instance group", ErrInvalidErrandInstance, instance)
		}

		converted = append(converted, boshapi.ErrandInstance{Group: group, ID: strings.TrimSpace(id)})
	}

	return converted, nil
}

// worstErrandResult converts every errand result, sorts them by instance
// group and then by instance ID, and returns the worst of them with every
// result in Results. A result with no usable exit code ranks worst,
// because Blacksmith cannot tell whether the errand succeeded. A failure
// ranks next, and a higher exit code ranks above a lower one, so a code
// above 128, which the director gives a cancelled errand, outranks an
// ordinary failure. A success ranks last. Between results of the same
// rank, the first in sorted order wins, so the choice never depends on the
// order in which the instances finished.
func worstErrandResult(results []boshapi.ErrandResult) *ErrandResult {
	converted := make([]ErrandResult, len(results))
	for index, result := range results {
		converted[index] = convertErrandResult(result)
	}

	sort.SliceStable(converted, func(i, j int) bool {
		if converted[i].InstanceGroup != converted[j].InstanceGroup {
			return converted[i].InstanceGroup < converted[j].InstanceGroup
		}

		return converted[i].InstanceID < converted[j].InstanceID
	})

	worst := converted[0]
	for _, result := range converted[1:] {
		if errandOutranks(result.ExitCode, worst.ExitCode) {
			worst = result
		}
	}

	worst.Results = converted

	return &worst
}

// errandOutranks reports whether an errand exit code is strictly worse than
// another. Codes are as convertErrandResult gives them, so -1 stands for
// no usable code.
func errandOutranks(code, other int) bool {
	switch {
	case other < 0:
		return false
	case code < 0:
		return true
	default:
		return code > other
	}
}

// convertErrandResult converts one errand result. A missing or negative
// exit code means the director reported none Blacksmith can use, so it
// becomes -1, which never reads as a success.
func convertErrandResult(result boshapi.ErrandResult) ErrandResult {
	code := -1
	if result.ExitCode != nil && *result.ExitCode >= 0 {
		code = *result.ExitCode
	}

	return ErrandResult{
		InstanceGroup:   result.Instance.Group,
		InstanceID:      result.Instance.ID,
		ExitCode:        code,
		Stdout:          result.Stdout,
		Stderr:          result.Stderr,
		LogsBlobstoreID: result.Logs.BlobstoreID,
		LogsSHA1:        result.Logs.SHA1,
	}
}

// GetInstances lists a deployment's instances, including those without a
// VM. It sends the format=full listing, waits for the read-only task the
// director starts, and reads the task's result, as GetDeploymentVMs does,
// so it shares that listing's handling of a missing deployment, a lost
// answer, a failed task, and a context that ends during the wait.
func (d *DirectorAdapter) GetInstances(ctx context.Context, deployment string) (_ []Instance, err error) {
	defer d.explainContext(ctx, "GetInstances", &err)

	d.log.Infof("Getting instances for deployment: %s", deployment)

	details, err := d.listDetails(ctx, detailsListing{
		operation: "GetInstances",
		resource:  "instances",
		list:      d.api.Instances().ListDetailed,
	}, deployment)
	if err != nil {
		return nil, err
	}

	instances := make([]Instance, len(details))
	for index, instance := range details {
		instances[index] = d.convertInstanceInfo(instance)
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

// detailsIndex is a row's index, or -1 when the director wrote a null
// index, which it does for an instance with no VM.
func detailsIndex(index *int) int {
	if index == nil {
		return -1
	}

	return *index
}

// parseVMCreatedAt parses a row's vm_created_at. An empty value, which is
// what a null decodes as, gives the zero time, and so does a value that
// does not parse, which is logged with the row's ID.
func (d *DirectorAdapter) parseVMCreatedAt(rowID, value string) time.Time {
	if value == "" {
		return time.Time{}
	}

	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		d.log.Debugf("The vm_created_at %q of %s does not parse as RFC 3339, so it is left empty: %v", value, rowID, err)

		return time.Time{}
	}

	return parsed
}

// convertVMInfo converts one row of a format=full VM listing. The job's
// state comes from job_state, which the director writes, and never from
// process_state, which it does not. The director sends no per-VM
// resurrection state either, so ResurrectionPaused stays false here, and
// the handlers fill it from the resurrection config.
func (d *DirectorAdapter) convertVMInfo(details boshapi.VMDetails) VM {
	index := detailsIndex(details.Index)

	dns := details.DNS
	if dns == nil {
		dns = []string{}
	}

	d.log.Debugf("VM %s/%d - JobState: %s, State: %s", details.JobName, index, details.JobState, details.State)

	return VM{
		ID:      details.ID,
		AgentID: details.AgentID,
		CID:     details.VMCID,

		Job:      details.JobName,
		Index:    index,
		JobState: details.JobState,

		State:     details.State,
		Active:    details.Active,
		Bootstrap: details.Bootstrap,
		Ignore:    details.Ignore,

		IPs: details.IPs,
		DNS: dns,
		AZ:  details.AZ,

		VMType:       details.VMType,
		ResourcePool: details.ResourcePool,

		DiskCID:  details.DiskCID,
		DiskCIDs: details.DiskCIDs,

		VMCreatedAt: d.parseVMCreatedAt(details.ID, details.VMCreatedAt),

		CloudProperties: details.CloudProperties,
		Processes:       convertProcesses(details.Processes),
		Vitals:          convertVitals(details.Vitals),
		Stemcell:        convertStemcell(details.Stemcell),
	}
}

// finishedUnread explains a failed read of the result of a task that ended
// in state done. The task ran its work, so the error carries its ID and
// TaskFinished, whether the caller's context ended or the director refused
// the read, and a caller never mistakes it for a call that started nothing.
func (d *DirectorAdapter) finishedUnread(ctx context.Context, operation string, taskID int, err error) error {
	callErr := &DirectorCallError{
		Operation: operation,
		Director:  d.address,
		Cause:     &taskReadError{taskID: taskID, lastState: taskStateDone},
		Err:       err,
		TaskID:    taskID,
		Task:      TaskFinished,
	}

	if ctx.Err() != nil {
		callErr.Deadline, _ = ctx.Deadline()
		callErr.Cause = ctx.Err()
	}

	return callErr
}

// errandResults reads and parses the result output of an errand's task.
func (d *DirectorAdapter) errandResults(ctx context.Context, deployment, errand string, taskID int) ([]boshapi.ErrandResult, error) {
	output, err := d.api.Tasks().GetOutput(ctx, taskID, &boshapi.TaskOutputOptions{Type: taskOutputResult})
	if err != nil {
		d.log.Errorf("Failed to read the result of BOSH task %d, which finished: %v", taskID, err)

		return nil, d.finishedUnread(ctx, runErrandOperation, taskID, d.requestFailure(fmt.Sprintf("read the result of BOSH task %d", taskID), err))
	}

	results, err := boshapi.ParseErrandResults(output)
	if err != nil {
		d.log.Errorf("Failed to parse the results of errand %s for deployment %s from BOSH task %d: %v", errand, deployment, taskID, err)

		return nil, &taskResultError{resource: "results of errand " + errand, deployment: deployment, director: d.address, taskID: taskID, err: err}
	}

	return results, nil
}

// noErrandResults answers an errand task that finished and wrote no
// results. The director skips a when_changed run when the last run
// succeeded and nothing has changed since, so that run succeeded and is
// reported as skipped, and the director's result text goes to the log,
// because a run that matched no instance looks the same. Any other run
// with no results has nothing to report.
func (d *DirectorAdapter) noErrandResults(deployment, errand string, opts ErrandOpts, task *Task) (*ErrandResult, error) {
	if opts.WhenChanged {
		d.log.Infof("The director skipped errand %s for deployment %s in BOSH task %d, which wrote no results for a when_changed run. "+
			"It answers the same way when nothing changed since the last successful run as when no instance matched, so the director's result text is the only way to tell them apart: %q",
			errand, deployment, task.ID, task.Result)

		return &ErrandResult{Skipped: true}, nil
	}

	d.log.Errorf("Errand %s for deployment %s wrote no results in BOSH task %d: %s", errand, deployment, task.ID, task.Result)

	return nil, fmt.Errorf("%w: errand %s for deployment %s finished in BOSH task %d with the result %q",
		ErrNoErrandResults, errand, deployment, task.ID, task.Result)
}

// convertInstanceInfo converts one row of a format=full instance listing.
// The instance is identified by its UUID, because an instance without a VM
// has no agent, and a null index becomes "-1", as it does for a VM. The
// process state comes from job_state, the key the director writes.
func (d *DirectorAdapter) convertInstanceInfo(details boshapi.VMDetails) Instance {
	return Instance{
		ID:           details.ID,
		Group:        details.JobName,
		Index:        strconv.Itoa(detailsIndex(details.Index)),
		State:        details.State,
		ProcessState: details.JobState,
		IPs:          details.IPs,
		AZ:           details.AZ,
		VMType:       details.VMType,
		ResourcePool: details.ResourcePool,
		Bootstrap:    details.Bootstrap,
		Ignore:       details.Ignore,
		VMCreatedAt:  d.parseVMCreatedAt(details.ID, details.VMCreatedAt),
	}
}

// nonNegative returns the address of value as a uint64, or nil when it is
// negative, so a nonsense count never wraps to a huge one.
func nonNegative(value int64) *uint64 {
	if value < 0 {
		return nil
	}

	converted := uint64(value)

	return &converted
}

// convertProcesses converts a row's processes. A figure the agent did not
// report stays nil, so it reads differently from a zero.
func convertProcesses(processes []boshapi.Process) []VMProcess {
	result := make([]VMProcess, len(processes))

	for index, process := range processes {
		converted := VMProcess{
			Name:  process.Name,
			State: process.State,
		}

		if process.CPU != nil {
			total := process.CPU.Total
			converted.CPU = VMVitalsCPU{
				Total: &total,
				Sys:   process.CPU.Sys,
				User:  process.CPU.User,
				Wait:  process.CPU.Wait,
			}
		}

		if process.Mem != nil {
			percent := process.Mem.Percent
			converted.Memory = VMVitalsMemory{
				KB:      nonNegative(process.Mem.KB),
				Percent: &percent,
			}
		}

		if process.Uptime != nil {
			converted.Uptime = VMVitalsUptime{Seconds: nonNegative(process.Uptime.Secs)}
		}

		result[index] = converted
	}

	return result
}

// convertVitals converts a row's vitals. The director writes null vitals for
// an agent that did not answer, which gives empty vitals with an empty disk
// map.
func convertVitals(vitals *boshapi.VMVitals) VMVitals {
	result := VMVitals{Disk: make(map[string]VMVitalsDisk)}

	if vitals == nil {
		return result
	}

	result.CPU = VMVitalsCPU{
		Sys:  vitals.CPU.Sys,
		User: vitals.CPU.User,
		Wait: vitals.CPU.Wait,
	}
	result.Memory = VMVitalsMemory{
		KB:      parseStringToUint64(vitals.Mem.KB),
		Percent: parseStringToFloat64(vitals.Mem.Percent),
	}
	result.Swap = VMVitalsMemory{
		KB:      parseStringToUint64(vitals.Swap.KB),
		Percent: parseStringToFloat64(vitals.Swap.Percent),
	}
	result.Load = vitals.Load

	if vitals.Uptime != nil {
		result.Uptime = VMVitalsUptime{Seconds: nonNegative(vitals.Uptime.Secs)}
	}

	for diskName, disk := range vitals.Disk {
		result.Disk[diskName] = VMVitalsDisk{
			InodePercent: disk.InodePercent,
			Percent:      disk.Percent,
		}
	}

	return result
}

// convertStemcell converts the stemcell a row's VM runs.
func convertStemcell(stemcell boshapi.VMStemcell) VMStemcell {
	return VMStemcell{
		Name:       stemcell.Name,
		Version:    stemcell.Version,
		ApiVersion: stemcell.APIVersion,
	}
}

// sshTarget names one instance by its group and index, which is the only
// target SSHCommand and SSHSession take. An empty group would select every
// instance in the deployment, so it is refused before anything is sent.
func sshTarget(instance string, index int) (boshapi.SSHTarget, error) {
	if instance == "" || index < 0 {
		return boshapi.SSHTarget{}, fmt.Errorf("%w: got group %q and index %d, so nothing was sent",
			ErrSSHTargetInvalid, instance, index)
	}

	return boshapi.SSHTarget{Job: instance, Indices: []int{index}}, nil
}

// setUpSSH asks the director to create a throwaway user with a fresh
// ed25519 key on the target's instances, waits for the setup task, and
// returns the session that connects as that user. The request goes
// through accepted() without a lookup, because the director's SSH route
// never stores the context ID on its task. The director's
// deployment-not-found answer, code 70000, wraps ErrDeploymentNotFound,
// and its refusal of the broker's client wraps ErrSSHNotPermitted.
//
// The user is recorded in the cleanup store before the setup is sent, and
// a setup whose record cannot be written is never sent. Once ShutdownSSH
// has begun, a setup that has not claimed its user is not sent, and
// neither is one that finds the gate closed when it checks again after
// its record is written. That record is cleared, and the error wraps
// ErrBrokerShuttingDown. A setup that ends done after the shutdown began
// opens no session, sends the user's cleanup, and returns an error that
// wraps it too. A setup the director refused created no user, so its
// record is cleared at once. Every other failure after the setup was sent
// returns at once, and the user's cleanup follows in the background once
// the setup task has ended.
func (d *DirectorAdapter) setUpSSH(ctx context.Context, operation, deployment string, target boshapi.SSHTarget) (*sshconn.Session, error) {
	ctx, contextID, err := mutationContext(ctx, operation)
	if err != nil {
		return nil, err
	}

	credentials, err := sshconn.NewCredentials()
	if err != nil {
		d.log.Errorf("Failed to make the SSH credentials for deployment %s: %v", deployment, err)

		return nil, fmt.Errorf("failed to make the SSH credentials for deployment %s, so nothing was sent: %w", deployment, err)
	}

	pending := sshconn.PendingCleanup{
		User:           credentials.User,
		Deployment:     deployment,
		Target:         target,
		SetupContextID: contextID,
		CreatedAt:      time.Now().UTC(),
	}

	err = d.recordSSHCleanup(ctx, pending)
	if err != nil {
		return nil, err
	}

	task, err := d.sendSSHSetup(ctx, operation, pending, credentials.PublicKey)
	if err != nil {
		return nil, err
	}

	hosts, err := d.sshSetupHosts(ctx, operation, deployment, task.ID, credentials.User)
	if err != nil {
		return nil, d.afterFailedSSHSetup(ctx, operation, pending, task.ID, err)
	}

	d.log.Infof("BOSH task %d set up SSH user %s on %d host(s) of deployment %s", task.ID, credentials.User, len(hosts), deployment)

	session := &sshconn.Session{
		Deployment: deployment,
		Target:     target,
		User:       credentials.User,
		Hosts:      hosts,
		Signer:     credentials.Signer,
	}

	session.Cleanup, err = d.openSSHSessionCleanup(ctx, session, task.ID, contextID)
	if err != nil {
		return nil, err
	}

	return session, nil
}

// sendSSHSetup sends the setup of the pending user and returns the task
// the director started. It first checks the shutdown gate, and sends
// nothing once ShutdownSSH has closed it. A setup the director refused
// clears the user's record. A setup whose answer was lost hands the user
// to afterFailedSSHSetup, which removes it once no setup is running.
func (d *DirectorAdapter) sendSSHSetup(ctx context.Context, operation string, pending sshconn.PendingCleanup, publicKey string) (*Task, error) {
	deployment, user := pending.Deployment, pending.User
	instance := sshconn.TargetName(pending.Target)

	err := d.checkSSHSetupGate(ctx, pending)
	if err != nil {
		return nil, err
	}

	d.log.Infof("Setting up SSH user %s on %s of deployment %s (context ID %s)", user, instance, deployment, pending.SetupContextID)

	sent, err := d.api.SSHClient().Setup(ctx, deployment,
		sshconn.SetupRequest(deployment, pending.Target, user, publicKey))
	if err != nil && !boshapi.IsOutcomeUnknown(err) {
		// The director refused the setup, so it created no user.
		d.dropSSHCleanup(ctx, user)

		refused := d.sshSetupRefused(deployment, err)
		if refused != nil {
			return nil, refused
		}
	}

	task, err := d.accepted(ctx, mutation{
		operation:  operation,
		deployment: deployment,
		contextID:  pending.SetupContextID,
		what:       fmt.Sprintf("set up SSH user %s on %s of deployment %s", user, instance, deployment),
	}, sent, err)
	if errors.As(err, new(*OutcomeUnknownError)) {
		return nil, d.afterFailedSSHSetup(ctx, operation, pending, 0, err)
	}

	if err != nil {
		d.releaseSSHUser(user)

		return nil, err
	}

	return task, nil
}

// sshSetupRefused explains the director's answers to an SSH setup that
// mean it will never succeed as sent, and returns nil for any other error.
func (d *DirectorAdapter) sshSetupRefused(deployment string, err error) error {
	var apiErr *boshapi.APIError

	switch {
	case errors.As(err, &apiErr) && apiErr.Code == boshapi.ErrorCodeDeploymentNotFound:
		d.log.Infof("The director has no deployment %s to set up SSH on", deployment)

		return fmt.Errorf("%w: %s: %w", ErrDeploymentNotFound, deployment, err)
	case boshapi.IsForbidden(err):
		d.log.Errorf("The director refused to set up SSH on deployment %s for the broker's client: %v", deployment, err)

		return fmt.Errorf("%w on deployment %s on BOSH director %s, because the director refused the request (%w). "+
			"Give the broker's UAA client the bosh.admin authority, or a team admin authority that covers the deployment",
			ErrSSHNotPermitted, deployment, d.address, err)
	default:
		return nil
	}
}

// sshSetupHosts waits for the setup task and reads the hosts from its
// result. A result that names a gateway is refused like any other result
// Blacksmith cannot connect with, so the caller removes the user.
func (d *DirectorAdapter) sshSetupHosts(ctx context.Context, operation, deployment string, taskID int, user string) ([]sshconn.Host, error) {
	output, err := d.sshSetupResult(ctx, operation, deployment, taskID)
	if err != nil {
		return nil, err
	}

	results, err := boshapi.ParseSSHResults(output)
	if err != nil {
		d.log.Errorf("Failed to parse the result of SSH setup task %d for user %s: %v", taskID, user, err)

		return nil, &taskResultError{resource: "SSH hosts", deployment: deployment, director: d.address, taskID: taskID, err: err}
	}

	hosts, err := sshconn.ReadHosts(results, user)
	if err != nil {
		d.log.Errorf("SSH setup task %d for deployment %s returned hosts Blacksmith cannot use: %v", taskID, deployment, err)

		return nil, fmt.Errorf("SSH setup for deployment %s in BOSH task %d on BOSH director %s: %w",
			deployment, taskID, d.address, err)
	}

	return hosts, nil
}

// cleanUpSSH removes the SSH user that setUpSSH created from the same
// target, waits for the cleanup task, and settles the user's record. A
// cleanup task that ends done, the director's answer that the deployment
// is gone, and its answer that the target has no VM each clear the
// record. Any other failure keeps it for the sweep and is logged at Error
// with the user and the context IDs. The user leaves the live set when
// the cleanup ends, so the sweep may then retry it.
func (d *DirectorAdapter) cleanUpSSH(ctx context.Context, deployment string, target boshapi.SSHTarget, user, setupContextID string) error {
	d.markSSHUser(user, sshUserCleaningUp)
	defer d.releaseSSHUser(user)

	err := d.sendSSHCleanup(ctx, deployment, target, user, setupContextID, d.log.Errorf)
	if sshCleanupSettled(err) {
		d.clearSSHCleanup(ctx, d.sshCleanupStore(), user)
	}

	return err
}

// sendSSHCleanup sends the cleanup of user on target and waits for its
// task. It often runs after the caller's context has ended, and a
// cancelled context must not leave the user on the VM, so it keeps ctx's
// values but gets its own deadline of sshCleanupTimeout. It sends a fresh
// context ID of its own. A cleanup task that ends in any state but done,
// and a wait the deadline cuts off, are reported through report with the
// user and the context IDs. A lost answer is logged at Error by accepted.
//
//nolint:nonamedreturns // explainContext reads the named error, as in every other adapter method.
func (d *DirectorAdapter) sendSSHCleanup(ctx context.Context, deployment string, target boshapi.SSHTarget, user, setupContextID string,
	report func(format string, args ...interface{}),
) (err error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sshCleanupTimeout)
	defer cancel()

	defer d.explainContext(ctx, "CleanUpSSH", &err)

	instance := sshconn.TargetName(target)

	task, contextID, err := d.requestSSHCleanup(ctx, deployment, target, user, setupContextID)
	if err != nil {
		return err
	}

	final, err := d.WaitForTask(ctx, task.ID)
	if err != nil {
		report("Stopped waiting for BOSH task %d, which removes SSH user %s from %s of deployment %s "+
			"(context ID %s, setup context ID %s), so the user may remain on the VM: %v",
			task.ID, user, instance, deployment, contextID, setupContextID, err)

		return err
	}

	if final.State != taskStateDone {
		report("BOSH task %d, which removes SSH user %s from %s of deployment %s (context ID %s, setup context ID %s), "+
			"ended in state %s, so the user may remain on the VM: %s",
			task.ID, user, instance, deployment, contextID, setupContextID, final.State, final.Result)

		return &TaskFailedError{
			Operation:  "CleanUpSSH",
			Director:   d.address,
			Deployment: deployment,
			TaskID:     task.ID,
			State:      final.State,
			Result:     final.Result,
		}
	}

	d.log.Infof("BOSH task %d removed SSH user %s from %s of deployment %s", task.ID, user, instance, deployment)

	return nil
}

// requestSSHCleanup sends the cleanup of user on target with a fresh
// context ID of its own, and returns the task the director started and
// that context ID. It does not wait for the task. A lost answer is logged
// at Error by accepted.
func (d *DirectorAdapter) requestSSHCleanup(ctx context.Context, deployment string, target boshapi.SSHTarget, user, setupContextID string) (*Task, string, error) {
	contextID := uuid.NewString()
	ctx = boshapi.WithContextID(ctx, contextID)
	instance := sshconn.TargetName(target)

	d.log.Infof("Removing SSH user %s from %s of deployment %s (context ID %s, setup context ID %s)",
		user, instance, deployment, contextID, setupContextID)

	sent, err := d.api.SSHClient().Cleanup(ctx, deployment, sshconn.CleanupRequest(deployment, target, user))

	task, err := d.accepted(ctx, mutation{
		operation:  "CleanUpSSH",
		deployment: deployment,
		contextID:  contextID,
		what:       fmt.Sprintf("remove SSH user %s from %s of deployment %s", user, instance, deployment),
	}, sent, err)

	return task, contextID, err
}

// createSSHClient connects to the session's first host as the session's
// user. The connection to the host accepts only the host key the director
// reported for it.
func (d *DirectorAdapter) createSSHClient(ctx context.Context, session *sshconn.Session) (*ssh.Client, error) {
	host := session.Hosts[0]

	config, err := sshconn.HostConfig(session.Deployment, host, session.Signer)
	if err != nil {
		d.log.Errorf("Refused to connect to %s of deployment %s: %v", host.Instance(), session.Deployment, err)

		return nil, fmt.Errorf("failed to connect to host: %w", err)
	}

	config.Timeout = httpClientTimeout

	targetAddr := net.JoinHostPort(host.Address, d.sshPort)

	client, err := sshconn.DialDirect(ctx, targetAddr, config)
	if err != nil {
		err = sshconn.ExplainHostKeyRefusal(err, session.Deployment, host, targetAddr)
		d.log.Errorf("Failed to connect to %s of deployment %s at %s: %v", host.Instance(), session.Deployment, targetAddr, err)

		return nil, fmt.Errorf("failed to connect to host: %w", err)
	}

	return client, nil
}

// executeSSHCommand executes the command on the SSH client.
func (d *DirectorAdapter) executeSSHCommand(client sshconn.Client, command string, args []string) (string, error) {
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

// Ensure DirectorAdapter implements Director interface.
var _ Director = (*DirectorAdapter)(nil)

// getConfigEntry reads one config version by ID. A missing ID, or one that
// is not a decimal integer and so can never name a config, maps to
// ErrConfigNotFound.
func (d *DirectorAdapter) getConfigEntry(ctx context.Context, what, id string) (*boshapi.ConfigEntry, error) {
	entry, err := d.api.Configs().Get(ctx, id)
	if err != nil {
		if errors.Is(err, boshapi.ErrConfigIDInvalid) {
			return nil, fmt.Errorf("%w: %q is not a decimal integer: %w", ErrConfigNotFound, id, err)
		}

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
