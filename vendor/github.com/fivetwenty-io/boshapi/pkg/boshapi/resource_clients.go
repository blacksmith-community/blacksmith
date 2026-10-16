package boshapi

import (
	"context"
	"io"
)

// DeploymentsClient provides access to deployment operations.
type DeploymentsClient interface {
	List(ctx context.Context, opts *DeploymentListOptions) ([]Deployment, error)
	Get(ctx context.Context, name string) (*DeploymentManifest, error)
	Deploy(ctx context.Context, manifest string, opts *DeployOptions) (*Task, error)
	Delete(ctx context.Context, name string, opts *DeleteDeploymentOptions) (*Task, error)
	Diff(ctx context.Context, name string, manifest string, opts *DiffOptions) (*DeploymentDiff, error)
	// CertificateExpiry reads GET /deployments/{name}/certificate_expiry,
	// which lists the certificates among the deployment's variables from the
	// last successful deploy, with the expiry time and the days left for each.
	// A deployment with no certificate variables returns an empty list. It
	// needs read access to the deployment, and a missing deployment returns an
	// *APIError with code 70000. Client.GetCertificateExpiry reads the
	// director's own certificates instead.
	CertificateExpiry(ctx context.Context, name string) ([]DeploymentCertificateExpiry, error)
}

// InstancesClient provides access to instance operations.
type InstancesClient interface { //nolint:interfacebloat // public API; splitting breaks consumers
	List(ctx context.Context, deployment string) ([]Instance, error)
	ListDetailed(ctx context.Context, deployment string) (*Task, error)
	// Get reads one instance with GET
	// /deployments/{deployment}/jobs/{job}/{indexOrID}; indexOrID is the
	// instance index or its UUID. It returns the short record that the route
	// serves, which holds the state and the persistent disk CIDs but no VM,
	// network, or vitals data, so use List or ListDetailed for those. A
	// director 404 with code 70001 returns an *InstanceNotFoundError, and a
	// missing deployment stays an *APIError with code 70000.
	Get(ctx context.Context, deployment, job, indexOrID string) (*InstanceSummary, error)
	UpdateState(ctx context.Context, deployment, job, indexOrID string, opts *UpdateStateOptions) (*Task, error)
	// UpdateJobState changes the state of all instances of a job; job may be
	// "*" to target every job in the deployment. Unlike UpdateState, this
	// job-level route accepts UpdateStateOptions.RecreateVMsCreatedBefore.
	UpdateJobState(ctx context.Context, deployment, job string, opts *UpdateStateOptions) (*Task, error)
	Stop(ctx context.Context, deployment, job, indexOrID string, opts *UpdateStateOptions) (*Task, error)
	Start(ctx context.Context, deployment, job, indexOrID string, opts *UpdateStateOptions) (*Task, error)
	Restart(ctx context.Context, deployment, job, indexOrID string, opts *UpdateStateOptions) (*Task, error)
	Recreate(ctx context.Context, deployment, job, indexOrID string, opts *UpdateStateOptions) (*Task, error)
	// StopInstance, StartInstance, RestartInstance, and RecreateInstance use
	// the per-instance action routes
	// (POST /deployments/{deployment}/instance_groups/{group}/{id}/actions/*).
	// Unlike the state-change methods above, they act on exactly one instance
	// and enqueue a single-instance director task. All operations return a
	// task via 302 or 303 redirect.
	StopInstance(ctx context.Context, deployment, instanceGroup, indexOrID string, opts *StopInstanceOptions) (*Task, error)
	StartInstance(ctx context.Context, deployment, instanceGroup, indexOrID string) (*Task, error)
	RestartInstance(ctx context.Context, deployment, instanceGroup, indexOrID string, opts *RestartInstanceOptions) (*Task, error)
	RecreateInstance(ctx context.Context, deployment, instanceGroup, indexOrID string, opts *RecreateInstanceOptions) (*Task, error)
	Ignore(ctx context.Context, deployment, instanceGroup, indexOrID string, ignore bool) error
	FetchLogs(ctx context.Context, deployment, job, indexOrID string, opts *LogOptions) (*Task, error)
}

// VMsClient provides access to VM operations.
type VMsClient interface {
	List(ctx context.Context, deployment string) ([]VM, error)
	ListDetailed(ctx context.Context, deployment string) (*Task, error)
	DeleteVM(ctx context.Context, vmCID string) (*Task, error)
}

// ReleasesClient provides access to release operations.
type ReleasesClient interface {
	List(ctx context.Context) ([]Release, error)
	Get(ctx context.Context, name string, opts *ReleaseGetOptions) (*ReleaseDetail, error)
	// Upload asks the director to fetch a release tarball from opts.URL. A nil
	// option or an empty URL returns ErrUploadLocationRequired before
	// anything is sent.
	Upload(ctx context.Context, opts *UploadReleaseOptions) (*Task, error)
	// UploadFile uploads a release tarball from opts.Body with POST /releases.
	// The body streams from the reader and is never held in memory. A stream
	// cannot be replayed, so the request is sent at most once: it is never
	// retried, and a 401 is never resent, though it replaces the rejected token
	// so that the caller's next upload carries a new one. A missing option,
	// reader, or size returns ErrUploadFileInvalid before anything is sent.
	// HTTPTimeout bounds only the wait for the director's answer after the body
	// is written, and ctx bounds the upload, even while a Read on Body blocks.
	// When UploadFile returns, nothing reads Body any more, with one exception:
	// if ctx ended while a Read on Body was blocked, that Read may still be
	// running, and no further Read follows it. A reader that does not return
	// from a blocked Read when it is closed keeps that one goroutine parked
	// until the Read returns. When the request ends before the whole body is
	// sent, because the director answers early, as its nginx does with a 413,
	// or because the connection drops with no answer, UploadFile stops
	// sending and waits for a Read in progress to return before it returns.
	// A reader that blocks at that moment holds the call until the Read
	// returns or ctx ends, so give ctx a deadline when the reader can stall.
	UploadFile(ctx context.Context, opts *UploadReleaseFileOptions) (*Task, error)
	Delete(ctx context.Context, name string, opts *DeleteReleaseOptions) (*Task, error)
	ExportRelease(ctx context.Context, req *ExportReleaseRequest) (*Task, error)
	// GetJob reads one release job, with its spec, from GET /jobs. All three
	// arguments are required, and an empty one returns ErrJobSelectorRequired
	// before anything is sent. The director matches the job by release name,
	// job name, and fingerprint, and answers a one-element list, which GetJob
	// unwraps. A 404, which means the director has no such release or no job
	// with that fingerprint, returns a *ReleaseJobNotFoundError. The route
	// needs the director read scope.
	GetJob(ctx context.Context, releaseName, jobName, fingerprint string) (*ReleaseJobDetail, error)
}

// StemcellsClient provides access to stemcell operations.
type StemcellsClient interface {
	List(ctx context.Context) ([]Stemcell, error)
	// Upload asks the director to fetch a stemcell tarball from opts.URL. A
	// nil option or an empty URL returns ErrUploadLocationRequired before
	// anything is sent.
	Upload(ctx context.Context, opts *UploadStemcellOptions) (*Task, error)
	// UploadFile uploads a stemcell tarball from opts.Body with POST
	// /stemcells. It follows the rules of Releases().UploadFile: the body
	// streams from the reader, the request is sent at most once, ctx ends the
	// upload even while a Read on Body blocks, nothing reads Body once the
	// call returns except a Read that was blocked when ctx ended, a request
	// that ends before the whole body is sent, by an early answer or a
	// dropped connection, waits for a Read in progress to return or for ctx
	// to end, and a missing option, reader, or size returns
	// ErrUploadFileInvalid before anything is sent.
	UploadFile(ctx context.Context, opts *UploadStemcellFileOptions) (*Task, error)
	Delete(ctx context.Context, name, version string, opts *DeleteStemcellOptions) (*Task, error)
}

// StemcellUploadsClient provides access to stemcell upload check operations.
type StemcellUploadsClient interface {
	CheckUploadNeeded(ctx context.Context, name, version string) (bool, error)
}

// TasksClient provides access to task operations.
type TasksClient interface {
	List(ctx context.Context, opts *TaskListOptions) ([]Task, error)
	Get(ctx context.Context, id int) (*Task, error)
	GetOutput(ctx context.Context, id int, opts *TaskOutputOptions) (string, error)
	// Cancel sends DELETE /task/{id}. A task that is no longer queued or
	// processing returns a *TaskStateError naming its state.
	Cancel(ctx context.Context, id int) error
	// CancelMatching sends POST /tasks/cancel with a JSON selector and cancels
	// every task that matches it, in one request. The director answers 204
	// whether or not any task matched, and it skips a task that has left a
	// cancellable state in the meantime, so the call reports no count. Cancel
	// targets one task by ID instead. A nil request, or one that sets no
	// States, no Types, and no Deployment, returns ErrCancelSelectorRequired
	// before anything is sent, because the director would cancel every queued
	// task of every deployment. Pass States: []string{"queued"} to do that
	// on purpose. States may hold only "queued" and
	// "processing", and the director answers 400 for any other state. Any
	// 3xx answer is returned as a *RedirectError, because only a 2xx confirms
	// the cancel. The route needs the director admin scope.
	CancelMatching(ctx context.Context, req *CancelTasksRequest) error
	// PollUntilComplete waits for a task to reach done, error, cancelled, or
	// timeout. It returns the final task, with a *TaskError for any terminal
	// state but done, or a *TaskWaitError when the wait ends first. It never
	// cancels the task.
	PollUntilComplete(ctx context.Context, id int, opts *PollOptions) (*Task, error)
}

// EventsClient provides access to event operations.
type EventsClient interface {
	List(ctx context.Context, opts *EventListOptions) ([]Event, error)
	Get(ctx context.Context, id string) (*Event, error)
	Create(ctx context.Context, req *CreateEventRequest) error
}

// ConfigsClient provides access to generic config operations.
type ConfigsClient interface {
	List(ctx context.Context, opts *ConfigListOptions) ([]ConfigEntry, error)
	// Get reads one config by ID with GET /configs/<id>. An ID that is not a
	// decimal integer returns ErrConfigIDInvalid before anything is sent, and
	// a director 404 returns a *ConfigNotFoundError.
	Get(ctx context.Context, id string) (*ConfigEntry, error)
	Create(ctx context.Context, req *CreateConfigRequest) (*ConfigEntry, error)
	// Delete removes one config by ID with DELETE /configs/<id>. An ID that
	// is not a decimal integer returns ErrConfigIDInvalid before anything is
	// sent, and a director 404 returns a *ConfigNotFoundError.
	Delete(ctx context.Context, id string) error
	DeleteByTypeAndName(ctx context.Context, configType, name string) error
	Diff(ctx context.Context, req *ConfigDiffRequest) (*ConfigDiff, error)
}

// CloudConfigsClient provides access to cloud config operations.
type CloudConfigsClient interface {
	Get(ctx context.Context, opts *CloudConfigListOptions) ([]ConfigRevision, error)
	Update(ctx context.Context, yaml string, opts *UpdateCloudConfigOptions) error
	Diff(ctx context.Context, yaml string, opts *DiffOptions) (*ConfigDiff, error)
}

// RuntimeConfigsClient provides access to runtime config operations.
type RuntimeConfigsClient interface {
	Get(ctx context.Context, opts *RuntimeConfigListOptions) ([]ConfigRevision, error)
	Update(ctx context.Context, yaml string, opts *UpdateRuntimeConfigOptions) error
	Diff(ctx context.Context, yaml string, opts *DiffOptions) (*ConfigDiff, error)
}

// CPIConfigsClient provides access to CPI config operations.
type CPIConfigsClient interface {
	Get(ctx context.Context, opts *CPIConfigListOptions) ([]ConfigRevision, error)
	Update(ctx context.Context, yaml string) error
	Diff(ctx context.Context, yaml string, opts *DiffOptions) (*ConfigDiff, error)
}

// ErrandsClient provides access to errand operations.
type ErrandsClient interface {
	List(ctx context.Context, deployment string) ([]Errand, error)
	Run(ctx context.Context, deployment, errandName string, opts *RunErrandOptions) (*Task, error)
}

// CloudCheckClient provides access to cloud check operations.
type CloudCheckClient interface {
	Scan(ctx context.Context, deployment string) (*Task, error)
	ListProblems(ctx context.Context, deployment string) ([]Problem, error)
	ResolveProblems(ctx context.Context, deployment string, req *ResolveProblemsRequest) (*Task, error)
	ScanAndFix(ctx context.Context, deployment string, req *ScanAndFixRequest) (*Task, error)
}

// DisksClient provides access to disk operations.
type DisksClient interface {
	ListOrphanDisks(ctx context.Context) ([]OrphanDisk, error)
	DeleteOrphanDisk(ctx context.Context, diskCID string) (*Task, error)
	AttachDisk(ctx context.Context, opts *AttachDiskOptions) (*Task, error)
}

// DynamicDisksClient provides access to dynamic disk operations. All methods
// except List enqueue a director task. Provide, Detach, and Delete require
// BOSH Director v283.0.0 or later; List, Create, and Attach require v283.1.2
// or later.
type DynamicDisksClient interface {
	List(ctx context.Context) ([]DynamicDisk, error)
	Create(ctx context.Context, req *CreateDynamicDiskRequest) (*Task, error)
	Attach(ctx context.Context, diskName string, req *AttachDynamicDiskRequest) (*Task, error)
	Provide(ctx context.Context, req *ProvideDynamicDiskRequest) (*Task, error)
	Detach(ctx context.Context, diskName string) (*Task, error)
	Delete(ctx context.Context, diskName string) (*Task, error)
}

// SnapshotsClient provides access to snapshot operations.
type SnapshotsClient interface {
	List(ctx context.Context, deployment string) ([]Snapshot, error)
	ListForInstance(ctx context.Context, deployment, job string, index int) ([]Snapshot, error)
	Create(ctx context.Context, deployment string) (*Task, error)
	CreateForInstance(ctx context.Context, deployment, job, indexOrID string) (*Task, error)
	DeleteAll(ctx context.Context, deployment string) (*Task, error)
	Delete(ctx context.Context, deployment, snapshotCID string) (*Task, error)
}

// LinksClient provides access to link operations.
type LinksClient interface {
	List(ctx context.Context, deployment string) ([]Link, error)
	Create(ctx context.Context, req *CreateLinkRequest) (*Link, error)
	Delete(ctx context.Context, linkID string) error
	GetAddress(ctx context.Context, opts *LinkAddressOptions) (*LinkAddress, error)
}

// LinkProvidersClient provides access to link provider operations.
type LinkProvidersClient interface {
	List(ctx context.Context, deployment string) ([]LinkProvider, error)
}

// LinkConsumersClient provides access to link consumer operations.
type LinkConsumersClient interface {
	List(ctx context.Context, deployment string) ([]LinkConsumer, error)
}

// LocksClient provides access to lock operations.
type LocksClient interface {
	List(ctx context.Context) ([]Lock, error)
}

// NetworksClient provides access to network operations.
type NetworksClient interface {
	ListOrphaned(ctx context.Context) ([]OrphanedNetwork, error)
	DeleteOrphaned(ctx context.Context, name string) (*Task, error)
}

// OrphanedVMsClient provides access to orphaned VM operations.
type OrphanedVMsClient interface {
	List(ctx context.Context) ([]OrphanedVM, error)
}

// ResurrectionClient provides access to resurrection operations.
type ResurrectionClient interface {
	Get(ctx context.Context) (bool, error)
	Set(ctx context.Context, paused bool) error
}

// CleanupClient provides access to cleanup operations.
type CleanupClient interface {
	Cleanup(ctx context.Context, req *CleanupRequest) (*Task, error)
	DryRun(ctx context.Context, opts *CleanupDryRunOptions) (*CleanupDryRunResult, error)
}

// ResourcesClient provides access to the blobstore resources that the
// director serves, such as the log bundle a log-fetch task produced.
type ResourcesClient interface {
	// Download sends GET /resources/{blobstoreID} and returns the body as it
	// arrives, so a large resource is never held in memory. The caller must
	// close the body. HTTPTimeout bounds the wait for the director's answer
	// but not the transfer, so ctx bounds the body read: when ctx ends, the
	// next Read fails. A non-2xx answer returns an *APIError that carries the
	// director's own description. An answer that arrives before the body, such
	// as a 503, is retried up to RetryMax times. Once the body has reached
	// the caller nothing is retried, so a Read that fails midway returns the
	// error and the caller starts the download again from the beginning.
	Download(ctx context.Context, blobstoreID string) (io.ReadCloser, error)
}

// SSHClient provides access to SSH operations.
type SSHClient interface {
	Setup(ctx context.Context, deployment string, req *SSHRequest) (*Task, error)
	Cleanup(ctx context.Context, deployment string, req *SSHRequest) (*Task, error)
}

// PackagesClient provides access to package matching operations.
type PackagesClient interface {
	MatchPackages(ctx context.Context, yaml string) ([]string, error)
	MatchCompiledPackages(ctx context.Context, yaml string) ([]string, error)
	// DownloadBlob sends GET /packages/blobs/{blobstoreID} and returns the
	// package or compiled package archive as it arrives, so a large blob is
	// never held in memory. The blobstore ID is the one a release's package
	// or compiled package record carries. It behaves like Resources().Download
	// for streaming, retries, timeouts, and closing, and the caller must close
	// the body. An ID that matches no package or compiled package returns an
	// *APIError with status 400, and an empty ID, or one made of dots, returns
	// an error before anything is sent.
	DownloadBlob(ctx context.Context, blobstoreID string) (io.ReadCloser, error)
}

// VariablesClient provides access to deployment variable operations.
type VariablesClient interface {
	List(ctx context.Context, deployment string) ([]Variable, error)
}

// DeployedVariablesClient provides access to deployed variable operations.
type DeployedVariablesClient interface {
	Get(ctx context.Context, variableName string) (*DeployedVariableUsage, error)
}

// DeploymentConfigsClient provides access to deployment config operations.
type DeploymentConfigsClient interface {
	List(ctx context.Context, opts *DeploymentConfigListOptions) ([]DeploymentConfig, error)
}
