package constants

import "time"

// Retry and concurrency limits.
const (
	DefaultRetryWaitMin  = 1 * time.Second
	DefaultRetryWaitMax  = 30 * time.Second
	ExtendedRetryWaitMax = 30 * time.Second
)

// HTTP status codes.
const (
	HTTPStatusOK                  = 200
	HTTPStatusFound               = 302
	HTTPStatusBadRequest          = 400
	HTTPStatusUnauthorized        = 401
	HTTPStatusForbidden           = 403
	HTTPStatusNotFound            = 404
	HTTPStatusInternalServerError = 500
)

// Token management.
const (
	TokenExpirationBuffer = 30 * time.Second
)

// Task polling.
const (
	DefaultTaskPollInterval = 2 * time.Second
)

// Task states.
const (
	TaskStateQueued     = "queued"
	TaskStateProcessing = "processing"
	TaskStateDone       = "done"
	TaskStateError      = "error"
	TaskStateCancelling = "cancelling"
	TaskStateCancelled  = "cancelled"
	TaskStateTimeout    = "timeout"
)

// API paths.
const (
	PathInfo              = "/info"
	PathCertificateExpiry = "/director/certificate_expiry"
	PathDeployments       = "/deployments"
	PathTasks             = "/tasks"
	PathReleases          = "/releases"
	PathStemcells         = "/stemcells"
	PathStemcellUploads   = "/stemcell_uploads"
	PathConfigs           = "/configs"
	PathCloudConfigs      = "/cloud_configs"
	PathRuntimeConfigs    = "/runtime_configs"
	PathCPIConfigs        = "/cpi_configs"
	PathEvents            = "/events"
	PathLocks             = "/locks"
	PathOrphanDisks       = "/orphan_disks"
	PathOrphanedVMs       = "/orphaned_vms"
	PathLinks             = "/links"
	PathLinkProviders     = "/link_providers"
	PathLinkConsumers     = "/link_consumers"
	PathLinkAddress       = "/link_address"
	PathResurrection      = "/resurrection"
	PathCleanup           = "/cleanup"
	PathNetworks          = "/networks"
	PathVMs               = "/vms"
	PathDisks             = "/disks"
	PathPackagesMatches   = "/packages/matches"
	PathDeploymentConfigs = "/deployment_configs"
	PathDeployedVariables = "/deployed_variables"
)

// Content types.
const (
	ContentTypeJSON = "application/json"
	ContentTypeYAML = "text/yaml"
)
