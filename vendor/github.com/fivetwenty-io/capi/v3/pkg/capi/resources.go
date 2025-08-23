package capi

import "time"

// App represents a Cloud Foundry application
type App struct {
	Resource
	Name                 string                 `json:"name"`
	State                string                 `json:"state"`
	Lifecycle            Lifecycle              `json:"lifecycle"`
	Metadata             *Metadata              `json:"metadata,omitempty"`
	Relationships        AppRelationships       `json:"relationships"`
	EnvironmentVariables map[string]interface{} `json:"environment_variables,omitempty"`
}

// AppCreateRequest represents a request to create an app
type AppCreateRequest struct {
	Name                 string                 `json:"name"`
	Relationships        AppRelationships       `json:"relationships"`
	Lifecycle            *Lifecycle             `json:"lifecycle,omitempty"`
	EnvironmentVariables map[string]interface{} `json:"environment_variables,omitempty"`
	Metadata             *Metadata              `json:"metadata,omitempty"`
}

// AppUpdateRequest represents a request to update an app
type AppUpdateRequest struct {
	Name      *string    `json:"name,omitempty"`
	Lifecycle *Lifecycle `json:"lifecycle,omitempty"`
	Metadata  *Metadata  `json:"metadata,omitempty"`
}

// AppRelationships represents app relationships
type AppRelationships struct {
	Space Relationship `json:"space"`
}

// Lifecycle represents app lifecycle configuration
type Lifecycle struct {
	Type string                 `json:"type"`
	Data map[string]interface{} `json:"data"`
}

// AppEnvironment represents app environment information
type AppEnvironment struct {
	StagingEnvJSON       map[string]interface{} `json:"staging_env_json"`
	RunningEnvJSON       map[string]interface{} `json:"running_env_json"`
	EnvironmentVariables map[string]interface{} `json:"environment_variables"`
	SystemEnvJSON        map[string]interface{} `json:"system_env_json"`
	ApplicationEnvJSON   map[string]interface{} `json:"application_env_json"`
}

// AppSSHEnabled represents SSH enablement status
type AppSSHEnabled struct {
	Enabled bool   `json:"enabled"`
	Reason  string `json:"reason,omitempty"`
}

// AppPermissions represents app permissions
type AppPermissions struct {
	ReadBasicData     bool `json:"read_basic_data"`
	ReadSensitiveData bool `json:"read_sensitive_data"`
}

// AppFeature represents a single app feature
type AppFeature struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Enabled     bool   `json:"enabled"`
}

// AppFeatures represents a collection of app features
type AppFeatures struct {
	Resources []AppFeature `json:"resources"`
}

// AppFeatureUpdateRequest represents a request to update an app feature
type AppFeatureUpdateRequest struct {
	Enabled bool `json:"enabled"`
}

// Organization represents a Cloud Foundry organization
type Organization struct {
	Resource
	Name          string            `json:"name"`
	Suspended     bool              `json:"suspended"`
	Metadata      *Metadata         `json:"metadata,omitempty"`
	Relationships *OrgRelationships `json:"relationships,omitempty"`
}

// OrganizationCreateRequest represents a request to create an organization
type OrganizationCreateRequest struct {
	Name     string    `json:"name"`
	Metadata *Metadata `json:"metadata,omitempty"`
}

// OrganizationUpdateRequest represents a request to update an organization
type OrganizationUpdateRequest struct {
	Name      *string   `json:"name,omitempty"`
	Suspended *bool     `json:"suspended,omitempty"`
	Metadata  *Metadata `json:"metadata,omitempty"`
}

// OrgRelationships represents organization relationships
type OrgRelationships struct {
	Quota Relationship `json:"quota,omitempty"`
}

// OrganizationUsageSummary represents organization usage summary
type OrganizationUsageSummary struct {
	UsageSummary struct {
		StartedInstances int `json:"started_instances"`
		MemoryInMB       int `json:"memory_in_mb"`
	} `json:"usage_summary"`
}

// Space represents a Cloud Foundry space
type Space struct {
	Resource
	Name          string             `json:"name"`
	Metadata      *Metadata          `json:"metadata,omitempty"`
	Relationships SpaceRelationships `json:"relationships"`
}

// SpaceCreateRequest represents a request to create a space
type SpaceCreateRequest struct {
	Name          string             `json:"name"`
	Relationships SpaceRelationships `json:"relationships"`
	Metadata      *Metadata          `json:"metadata,omitempty"`
}

// SpaceUpdateRequest represents a request to update a space
type SpaceUpdateRequest struct {
	Name     *string   `json:"name,omitempty"`
	Metadata *Metadata `json:"metadata,omitempty"`
}

// SpaceRelationships represents space relationships
type SpaceRelationships struct {
	Organization Relationship  `json:"organization"`
	Quota        *Relationship `json:"quota,omitempty"`
}

// SpaceFeatures represents space features
type SpaceFeatures struct {
	SSHEnabled bool `json:"ssh_enabled"`
}

// SpaceFeature represents a single space feature
type SpaceFeature struct {
	Name        string `json:"name"`
	Enabled     bool   `json:"enabled"`
	Description string `json:"description,omitempty"`
}

// SpaceUsageSummary represents space usage summary
type SpaceUsageSummary struct {
	UsageSummary struct {
		StartedInstances int `json:"started_instances"`
		MemoryInMB       int `json:"memory_in_mb"`
	} `json:"usage_summary"`
}

// Domain represents a domain
type Domain struct {
	Resource
	Name               string              `json:"name"`
	Internal           bool                `json:"internal"`
	RouterGroup        *string             `json:"router_group,omitempty"`
	SupportedProtocols []string            `json:"supported_protocols"`
	Metadata           *Metadata           `json:"metadata,omitempty"`
	Relationships      DomainRelationships `json:"relationships"`
}

// DomainCreateRequest represents a request to create a domain
type DomainCreateRequest struct {
	Name          string               `json:"name"`
	Internal      *bool                `json:"internal,omitempty"`
	RouterGroup   *string              `json:"router_group,omitempty"`
	Relationships *DomainRelationships `json:"relationships,omitempty"`
	Metadata      *Metadata            `json:"metadata,omitempty"`
}

// DomainUpdateRequest represents a request to update a domain
type DomainUpdateRequest struct {
	Metadata *Metadata `json:"metadata,omitempty"`
}

// DomainRelationships represents domain relationships
type DomainRelationships struct {
	Organization        *Relationship       `json:"organization,omitempty"`
	SharedOrganizations *ToManyRelationship `json:"shared_organizations,omitempty"`
}

// Route represents a route
type Route struct {
	Resource
	Protocol      string             `json:"protocol"`
	Host          string             `json:"host"`
	Path          string             `json:"path"`
	Port          *int               `json:"port,omitempty"`
	URL           string             `json:"url"`
	Destinations  []RouteDestination `json:"destinations"`
	Metadata      *Metadata          `json:"metadata,omitempty"`
	Relationships RouteRelationships `json:"relationships"`
}

// RouteCreateRequest represents a request to create a route
type RouteCreateRequest struct {
	Host          *string            `json:"host,omitempty"`
	Path          *string            `json:"path,omitempty"`
	Port          *int               `json:"port,omitempty"`
	Relationships RouteRelationships `json:"relationships"`
	Metadata      *Metadata          `json:"metadata,omitempty"`
}

// RouteUpdateRequest represents a request to update a route
type RouteUpdateRequest struct {
	Metadata *Metadata `json:"metadata,omitempty"`
}

// RouteRelationships represents route relationships
type RouteRelationships struct {
	Space  Relationship `json:"space"`
	Domain Relationship `json:"domain"`
}

// RouteDestination represents a route destination
type RouteDestination struct {
	GUID     string              `json:"guid"`
	App      RouteDestinationApp `json:"app"`
	Port     *int                `json:"port,omitempty"`
	Protocol *string             `json:"protocol,omitempty"`
	Weight   *int                `json:"weight,omitempty"`
}

// RouteDestinationApp represents the app in a route destination
type RouteDestinationApp struct {
	GUID    string   `json:"guid"`
	Process *Process `json:"process,omitempty"`
}

// RouteDestinations represents a list of route destinations
type RouteDestinations struct {
	Destinations []RouteDestination `json:"destinations"`
	Links        Links              `json:"links"`
}

// RouteReservation represents a route reservation check
type RouteReservation struct {
	MatchingRoute *Route `json:"matching_route"`
}

// RouteReservationRequest represents a request to check route reservation
type RouteReservationRequest struct {
	Host string `json:"host,omitempty"`
	Path string `json:"path,omitempty"`
	Port *int   `json:"port,omitempty"`
}

// User represents a user
type User struct {
	Resource
	Username         string    `json:"username"`
	PresentationName string    `json:"presentation_name"`
	Origin           string    `json:"origin"`
	Metadata         *Metadata `json:"metadata,omitempty"`
}

// UserCreateRequest represents a request to create a user
type UserCreateRequest struct {
	GUID     string    `json:"guid,omitempty"`
	Username string    `json:"username,omitempty"`
	Origin   string    `json:"origin,omitempty"`
	Metadata *Metadata `json:"metadata,omitempty"`
}

// UserUpdateRequest represents a request to update a user
type UserUpdateRequest struct {
	Metadata *Metadata `json:"metadata,omitempty"`
}

// Role represents a role
type Role struct {
	Resource
	Type          string            `json:"type"`
	Relationships RoleRelationships `json:"relationships"`
}

// RoleCreateRequest represents a request to create a role
type RoleCreateRequest struct {
	Type          string            `json:"type"`
	Relationships RoleRelationships `json:"relationships"`
}

// RoleRelationships represents role relationships
type RoleRelationships struct {
	User         Relationship  `json:"user"`
	Organization *Relationship `json:"organization,omitempty"`
	Space        *Relationship `json:"space,omitempty"`
}

// SecurityGroup represents a security group
type SecurityGroup struct {
	Resource
	Name            string                       `json:"name"`
	GloballyEnabled SecurityGroupGloballyEnabled `json:"globally_enabled"`
	Rules           []SecurityGroupRule          `json:"rules"`
	Relationships   SecurityGroupRelationships   `json:"relationships"`
}

// SecurityGroupGloballyEnabled represents globally enabled settings for a security group
type SecurityGroupGloballyEnabled struct {
	Running bool `json:"running"`
	Staging bool `json:"staging"`
}

// SecurityGroupRule represents a security group rule
type SecurityGroupRule struct {
	Protocol    string  `json:"protocol"`
	Destination string  `json:"destination"`
	Ports       *string `json:"ports,omitempty"`
	Type        *int    `json:"type,omitempty"`
	Code        *int    `json:"code,omitempty"`
	Description *string `json:"description,omitempty"`
	Log         *bool   `json:"log,omitempty"`
}

// SecurityGroupRelationships represents security group relationships
type SecurityGroupRelationships struct {
	RunningSpaces ToManyRelationship `json:"running_spaces"`
	StagingSpaces ToManyRelationship `json:"staging_spaces"`
}

// SecurityGroupCreateRequest represents a request to create a security group
type SecurityGroupCreateRequest struct {
	Name            string                        `json:"name"`
	GloballyEnabled *SecurityGroupGloballyEnabled `json:"globally_enabled,omitempty"`
	Rules           []SecurityGroupRule           `json:"rules,omitempty"`
	Relationships   *SecurityGroupRelationships   `json:"relationships,omitempty"`
}

// SecurityGroupUpdateRequest represents a request to update a security group
type SecurityGroupUpdateRequest struct {
	Name            *string                       `json:"name,omitempty"`
	GloballyEnabled *SecurityGroupGloballyEnabled `json:"globally_enabled,omitempty"`
	Rules           []SecurityGroupRule           `json:"rules,omitempty"`
}

// SecurityGroupBindRequest represents a request to bind a security group to spaces
type SecurityGroupBindRequest struct {
	Data []RelationshipData `json:"data"`
}

// Package represents a Cloud Foundry package
type Package struct {
	Resource
	Type          string                `json:"type"`
	Data          *PackageData          `json:"data"`
	State         string                `json:"state"`
	Metadata      *Metadata             `json:"metadata,omitempty"`
	Relationships *PackageRelationships `json:"relationships,omitempty"`
}

// PackageData represents package-specific data
type PackageData struct {
	Checksum *PackageChecksum `json:"checksum,omitempty"`
	Error    *string          `json:"error,omitempty"`
	Image    *string          `json:"image,omitempty"`    // For Docker packages
	Username *string          `json:"username,omitempty"` // For Docker packages
	Password *string          `json:"password,omitempty"` // For Docker packages
}

// PackageChecksum represents package checksum information
type PackageChecksum struct {
	Type  string  `json:"type"` // e.g., "sha256"
	Value *string `json:"value"`
}

// PackageRelationships represents the relationships for a package
type PackageRelationships struct {
	App *Relationship `json:"app,omitempty"`
}

// PackageCreateRequest represents a request to create a package
type PackageCreateRequest struct {
	Type          string               `json:"type"`
	Relationships PackageRelationships `json:"relationships"`
	Data          *PackageCreateData   `json:"data,omitempty"`
	Metadata      *Metadata            `json:"metadata,omitempty"`
}

// PackageCreateData represents data for creating a package
type PackageCreateData struct {
	Image    *string `json:"image,omitempty"`    // For Docker packages
	Username *string `json:"username,omitempty"` // For Docker packages
	Password *string `json:"password,omitempty"` // For Docker packages
}

// PackageUpdateRequest represents a request to update a package
type PackageUpdateRequest struct {
	Metadata *Metadata `json:"metadata,omitempty"`
}

// PackageUploadRequest represents a request to upload package bits
type PackageUploadRequest struct {
	Bits      []byte            `json:"-"` // The actual file bits
	Resources []PackageResource `json:"resources,omitempty"`
}

// PackageResource represents a resource in a package upload
type PackageResource struct {
	SHA1 string `json:"sha1"`
	Size int64  `json:"size"`
	Path string `json:"path"`
	Mode string `json:"mode"`
}

// PackageCopyRequest represents a request to copy a package
type PackageCopyRequest struct {
	Relationships PackageRelationships `json:"relationships"`
}

// Droplet represents a Cloud Foundry droplet
type Droplet struct {
	Resource
	State             string                `json:"state"`
	Error             *string               `json:"error"`
	Lifecycle         Lifecycle             `json:"lifecycle"`
	ExecutionMetadata string                `json:"execution_metadata"`
	ProcessTypes      map[string]string     `json:"process_types"`
	Checksum          *DropletChecksum      `json:"checksum,omitempty"`
	Buildpacks        []DetectedBuildpack   `json:"buildpacks,omitempty"`
	Stack             *string               `json:"stack,omitempty"`
	Image             *string               `json:"image,omitempty"`
	Metadata          *Metadata             `json:"metadata,omitempty"`
	Relationships     *DropletRelationships `json:"relationships,omitempty"`
}

// DropletChecksum represents droplet checksum information
type DropletChecksum struct {
	Type  string `json:"type"` // e.g., "sha256" or "sha1"
	Value string `json:"value"`
}

// DetectedBuildpack represents a buildpack detected during staging
type DetectedBuildpack struct {
	Name          string  `json:"name"`
	DetectOutput  string  `json:"detect_output"`
	Version       *string `json:"version,omitempty"`
	BuildpackName *string `json:"buildpack_name,omitempty"`
}

// DropletRelationships represents the relationships for a droplet
type DropletRelationships struct {
	App *Relationship `json:"app,omitempty"`
}

// DropletCreateRequest represents a request to create a droplet
type DropletCreateRequest struct {
	Relationships DropletRelationships `json:"relationships"`
	ProcessTypes  map[string]string    `json:"process_types,omitempty"`
}

// DropletUpdateRequest represents a request to update a droplet
type DropletUpdateRequest struct {
	Metadata     *Metadata         `json:"metadata,omitempty"`
	Image        *string           `json:"image,omitempty"`
	ProcessTypes map[string]string `json:"process_types,omitempty"`
}

// DropletCopyRequest represents a request to copy a droplet
type DropletCopyRequest struct {
	Relationships DropletRelationships `json:"relationships"`
}

// Build represents a Cloud Foundry build
type Build struct {
	Resource
	State                             string              `json:"state"`
	StagingMemoryInMB                 int                 `json:"staging_memory_in_mb"`
	StagingDiskInMB                   int                 `json:"staging_disk_in_mb"`
	StagingLogRateLimitBytesPerSecond *int                `json:"staging_log_rate_limit_bytes_per_second"`
	Error                             *string             `json:"error"`
	Lifecycle                         *Lifecycle          `json:"lifecycle,omitempty"`
	Package                           *BuildPackageRef    `json:"package"`
	Droplet                           *BuildDropletRef    `json:"droplet"`
	CreatedBy                         *UserRef            `json:"created_by"`
	Relationships                     *BuildRelationships `json:"relationships,omitempty"`
	Metadata                          *Metadata           `json:"metadata,omitempty"`
}

// BuildPackageRef represents a package reference in a build
type BuildPackageRef struct {
	GUID string `json:"guid"`
}

// BuildDropletRef represents a droplet reference in a build
type BuildDropletRef struct {
	GUID string `json:"guid"`
}

// UserRef represents a user reference
type UserRef struct {
	GUID  string `json:"guid"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

// BuildRelationships represents the relationships for a build
type BuildRelationships struct {
	App *Relationship `json:"app,omitempty"`
}

// BuildCreateRequest represents a request to create a build
type BuildCreateRequest struct {
	Package                           *BuildPackageRef `json:"package"`
	Lifecycle                         *Lifecycle       `json:"lifecycle,omitempty"`
	StagingMemoryInMB                 *int             `json:"staging_memory_in_mb,omitempty"`
	StagingDiskInMB                   *int             `json:"staging_disk_in_mb,omitempty"`
	StagingLogRateLimitBytesPerSecond *int             `json:"staging_log_rate_limit_bytes_per_second,omitempty"`
	Metadata                          *Metadata        `json:"metadata,omitempty"`
}

// BuildUpdateRequest represents a request to update a build
type BuildUpdateRequest struct {
	Metadata *Metadata `json:"metadata,omitempty"`
	State    *string   `json:"state,omitempty"`
}

// Buildpack represents a Cloud Foundry buildpack
type Buildpack struct {
	Resource
	Name      string    `json:"name"`
	State     string    `json:"state"`
	Filename  *string   `json:"filename"`
	Stack     *string   `json:"stack"`
	Position  int       `json:"position"`
	Lifecycle string    `json:"lifecycle"`
	Enabled   bool      `json:"enabled"`
	Locked    bool      `json:"locked"`
	Metadata  *Metadata `json:"metadata,omitempty"`
	Links     Links     `json:"links,omitempty"`
}

// BuildpackCreateRequest represents a request to create a buildpack
type BuildpackCreateRequest struct {
	Name      string    `json:"name"`
	Stack     *string   `json:"stack,omitempty"`
	Position  *int      `json:"position,omitempty"`
	Lifecycle *string   `json:"lifecycle,omitempty"`
	Enabled   *bool     `json:"enabled,omitempty"`
	Locked    *bool     `json:"locked,omitempty"`
	Metadata  *Metadata `json:"metadata,omitempty"`
}

// BuildpackUpdateRequest represents a request to update a buildpack
type BuildpackUpdateRequest struct {
	Name     *string   `json:"name,omitempty"`
	Stack    *string   `json:"stack,omitempty"`
	Position *int      `json:"position,omitempty"`
	Enabled  *bool     `json:"enabled,omitempty"`
	Locked   *bool     `json:"locked,omitempty"`
	Metadata *Metadata `json:"metadata,omitempty"`
}

// Deployment represents a Cloud Foundry deployment
type Deployment struct {
	Resource
	State           string                   `json:"state"`
	Status          DeploymentStatus         `json:"status"`
	Strategy        string                   `json:"strategy"`
	Options         *DeploymentOptions       `json:"options,omitempty"`
	Droplet         *DeploymentDropletRef    `json:"droplet"`
	PreviousDroplet *DeploymentDropletRef    `json:"previous_droplet"`
	NewProcesses    []DeploymentProcess      `json:"new_processes"`
	Revision        *DeploymentRevisionRef   `json:"revision,omitempty"`
	Metadata        *Metadata                `json:"metadata,omitempty"`
	Relationships   *DeploymentRelationships `json:"relationships,omitempty"`
}

// DeploymentStatus represents the status of a deployment
type DeploymentStatus struct {
	Value   string                   `json:"value"`
	Reason  string                   `json:"reason"`
	Details *DeploymentStatusDetails `json:"details,omitempty"`
	Canary  *DeploymentCanaryStatus  `json:"canary,omitempty"`
}

// DeploymentStatusDetails provides details about deployment status
type DeploymentStatusDetails struct {
	LastHealthyAt    *time.Time `json:"last_healthy_at,omitempty"`
	LastStatusChange *time.Time `json:"last_status_change,omitempty"`
	Error            *string    `json:"error,omitempty"`
}

// DeploymentCanaryStatus represents canary deployment status
type DeploymentCanaryStatus struct {
	Steps DeploymentCanarySteps `json:"steps"`
}

// DeploymentCanarySteps represents canary deployment step info
type DeploymentCanarySteps struct {
	Current int `json:"current"`
	Total   int `json:"total"`
}

// DeploymentOptions represents deployment options
type DeploymentOptions struct {
	MaxInFlight                  *int                     `json:"max_in_flight,omitempty"`
	WebInstances                 *int                     `json:"web_instances,omitempty"`
	MemoryInMB                   *int                     `json:"memory_in_mb,omitempty"`
	DiskInMB                     *int                     `json:"disk_in_mb,omitempty"`
	LogRateLimitInBytesPerSecond *int                     `json:"log_rate_limit_in_bytes_per_second,omitempty"`
	Canary                       *DeploymentCanaryOptions `json:"canary,omitempty"`
}

// DeploymentCanaryOptions represents canary deployment options
type DeploymentCanaryOptions struct {
	Steps []DeploymentCanaryStep `json:"steps"`
}

// DeploymentCanaryStep represents a canary deployment step
type DeploymentCanaryStep struct {
	Instances int `json:"instances"`
	WaitTime  int `json:"wait_time,omitempty"`
}

// DeploymentDropletRef represents a droplet reference in a deployment
type DeploymentDropletRef struct {
	GUID string `json:"guid"`
}

// DeploymentRevisionRef represents a revision reference in a deployment
type DeploymentRevisionRef struct {
	GUID    string `json:"guid"`
	Version int    `json:"version"`
}

// DeploymentProcess represents a process created during deployment
type DeploymentProcess struct {
	GUID string `json:"guid"`
	Type string `json:"type"`
}

// DeploymentRelationships represents the relationships for a deployment
type DeploymentRelationships struct {
	App *Relationship `json:"app,omitempty"`
}

// DeploymentCreateRequest represents a request to create a deployment
type DeploymentCreateRequest struct {
	Droplet       *DeploymentDropletRef   `json:"droplet,omitempty"`
	Revision      *DeploymentRevisionRef  `json:"revision,omitempty"`
	Strategy      *string                 `json:"strategy,omitempty"`
	Options       *DeploymentOptions      `json:"options,omitempty"`
	Relationships DeploymentRelationships `json:"relationships"`
	Metadata      *Metadata               `json:"metadata,omitempty"`
}

// DeploymentUpdateRequest represents a request to update a deployment
type DeploymentUpdateRequest struct {
	Metadata *Metadata `json:"metadata,omitempty"`
}

// Process represents a Cloud Foundry process
type Process struct {
	Resource
	Type                         string                `json:"type"`
	Command                      *string               `json:"command"`
	User                         string                `json:"user,omitempty"`
	Instances                    int                   `json:"instances"`
	MemoryInMB                   int                   `json:"memory_in_mb"`
	DiskInMB                     int                   `json:"disk_in_mb"`
	LogRateLimitInBytesPerSecond *int                  `json:"log_rate_limit_in_bytes_per_second"`
	HealthCheck                  *HealthCheck          `json:"health_check"`
	ReadinessHealthCheck         *ReadinessHealthCheck `json:"readiness_health_check"`
	Version                      string                `json:"version,omitempty"`
	Metadata                     *Metadata             `json:"metadata,omitempty"`
	Relationships                *ProcessRelationships `json:"relationships,omitempty"`
}

// ProcessRelationships represents the relationships for a process
type ProcessRelationships struct {
	App      *Relationship `json:"app,omitempty"`
	Revision *Relationship `json:"revision,omitempty"`
}

// HealthCheck represents a process health check
type HealthCheck struct {
	Type string           `json:"type"` // "port", "process", or "http"
	Data *HealthCheckData `json:"data,omitempty"`
}

// HealthCheckData represents health check configuration data
type HealthCheckData struct {
	Timeout           *int    `json:"timeout,omitempty"`
	InvocationTimeout *int    `json:"invocation_timeout,omitempty"`
	Interval          *int    `json:"interval,omitempty"`
	Endpoint          *string `json:"endpoint,omitempty"` // For HTTP health checks
}

// ReadinessHealthCheck represents a process readiness health check
type ReadinessHealthCheck struct {
	Type string                    `json:"type"` // "process", "port", or "http"
	Data *ReadinessHealthCheckData `json:"data,omitempty"`
}

// ReadinessHealthCheckData represents readiness health check configuration data
type ReadinessHealthCheckData struct {
	InvocationTimeout *int    `json:"invocation_timeout,omitempty"`
	Interval          *int    `json:"interval,omitempty"`
	Endpoint          *string `json:"endpoint,omitempty"` // For HTTP readiness checks
}

// ProcessUpdateRequest represents a request to update a process
type ProcessUpdateRequest struct {
	Command              *string               `json:"command,omitempty"`
	HealthCheck          *HealthCheck          `json:"health_check,omitempty"`
	ReadinessHealthCheck *ReadinessHealthCheck `json:"readiness_health_check,omitempty"`
	Metadata             *Metadata             `json:"metadata,omitempty"`
}

// ProcessScaleRequest represents a request to scale a process
type ProcessScaleRequest struct {
	Instances                    *int `json:"instances,omitempty"`
	MemoryInMB                   *int `json:"memory_in_mb,omitempty"`
	DiskInMB                     *int `json:"disk_in_mb,omitempty"`
	LogRateLimitInBytesPerSecond *int `json:"log_rate_limit_in_bytes_per_second,omitempty"`
}

// ProcessStats represents statistics for a process
type ProcessStats struct {
	Pagination *Pagination          `json:"pagination"`
	Resources  []ProcessStatsDetail `json:"resources"`
}

// ProcessStatsDetail represents detailed statistics for a process instance
type ProcessStatsDetail struct {
	Type             string                `json:"type"`
	Index            int                   `json:"index"`
	State            string                `json:"state"`
	Usage            *ProcessUsage         `json:"usage,omitempty"`
	Host             string                `json:"host,omitempty"`
	InstancePorts    []ProcessInstancePort `json:"instance_ports,omitempty"`
	Uptime           int                   `json:"uptime,omitempty"`
	MemQuota         int64                 `json:"mem_quota,omitempty"`
	DiskQuota        int64                 `json:"disk_quota,omitempty"`
	FdsQuota         int                   `json:"fds_quota,omitempty"`
	IsolationSegment *string               `json:"isolation_segment,omitempty"`
	Details          *string               `json:"details,omitempty"`
}

// ProcessUsage represents CPU and memory usage for a process instance
type ProcessUsage struct {
	Time           string  `json:"time"`
	CPU            float64 `json:"cpu"`
	CPUEntitlement float64 `json:"cpu_entitlement,omitempty"`
	Mem            int64   `json:"mem"`
	Disk           int64   `json:"disk"`
	LogRate        int     `json:"log_rate"`
}

// ProcessInstancePort represents port mappings for a process instance
type ProcessInstancePort struct {
	External             int `json:"external"`
	Internal             int `json:"internal"`
	ExternalTLSProxyPort int `json:"external_tls_proxy_port,omitempty"`
	InternalTLSProxyPort int `json:"internal_tls_proxy_port,omitempty"`
}

// Task represents a Cloud Foundry task
type Task struct {
	Resource
	SequenceID                   int                `json:"sequence_id"`
	Name                         string             `json:"name"`
	Command                      string             `json:"command,omitempty"`
	User                         *string            `json:"user"`
	State                        string             `json:"state"`
	MemoryInMB                   int                `json:"memory_in_mb"`
	DiskInMB                     int                `json:"disk_in_mb"`
	LogRateLimitInBytesPerSecond *int               `json:"log_rate_limit_in_bytes_per_second"`
	Result                       *TaskResult        `json:"result,omitempty"`
	DropletGUID                  string             `json:"droplet_guid"`
	Metadata                     *Metadata          `json:"metadata,omitempty"`
	Relationships                *TaskRelationships `json:"relationships,omitempty"`
}

// TaskResult represents the result of a task execution
type TaskResult struct {
	FailureReason *string `json:"failure_reason"`
}

// TaskRelationships represents the relationships for a task
type TaskRelationships struct {
	App *Relationship `json:"app,omitempty"`
}

// TaskCreateRequest represents a request to create a task
type TaskCreateRequest struct {
	Command                      *string       `json:"command,omitempty"`
	Name                         *string       `json:"name,omitempty"`
	MemoryInMB                   *int          `json:"memory_in_mb,omitempty"`
	DiskInMB                     *int          `json:"disk_in_mb,omitempty"`
	LogRateLimitInBytesPerSecond *int          `json:"log_rate_limit_in_bytes_per_second,omitempty"`
	Template                     *TaskTemplate `json:"template,omitempty"`
	Metadata                     *Metadata     `json:"metadata,omitempty"`
	DropletGUID                  *string       `json:"droplet_guid,omitempty"`
}

// TaskTemplate represents a template for creating a task from a process
type TaskTemplate struct {
	Process *TaskTemplateProcess `json:"process,omitempty"`
}

// TaskTemplateProcess represents a process reference in a task template
type TaskTemplateProcess struct {
	GUID string `json:"guid"`
}

// TaskUpdateRequest represents a request to update a task
type TaskUpdateRequest struct {
	Metadata *Metadata `json:"metadata,omitempty"`
}

// Stack represents a Cloud Foundry stack (a pre-built rootfs and associated executables)
type Stack struct {
	Resource
	Name             string    `json:"name"`
	Description      string    `json:"description"`
	BuildRootfsImage string    `json:"build_rootfs_image"`
	RunRootfsImage   string    `json:"run_rootfs_image"`
	Default          bool      `json:"default"`
	Metadata         *Metadata `json:"metadata,omitempty"`
	Links            Links     `json:"links,omitempty"`
}

// StackCreateRequest is the request for creating a stack
type StackCreateRequest struct {
	Name             string    `json:"name"`
	Description      string    `json:"description,omitempty"`
	BuildRootfsImage string    `json:"build_rootfs_image,omitempty"`
	RunRootfsImage   string    `json:"run_rootfs_image,omitempty"`
	Metadata         *Metadata `json:"metadata,omitempty"`
}

// StackUpdateRequest is the request for updating a stack
type StackUpdateRequest struct {
	Metadata *Metadata `json:"metadata,omitempty"`
}

// IsolationSegment represents an isolation segment
type IsolationSegment struct {
	Resource
	Name     string    `json:"name"`
	Metadata *Metadata `json:"metadata,omitempty"`
}

// IsolationSegmentCreateRequest represents a request to create an isolation segment
type IsolationSegmentCreateRequest struct {
	Name     string    `json:"name"`
	Metadata *Metadata `json:"metadata,omitempty"`
}

// IsolationSegmentUpdateRequest represents a request to update an isolation segment
type IsolationSegmentUpdateRequest struct {
	Name     *string   `json:"name,omitempty"`
	Metadata *Metadata `json:"metadata,omitempty"`
}

// IsolationSegmentEntitleOrganizationsRequest represents a request to entitle organizations
type IsolationSegmentEntitleOrganizationsRequest = ToManyRelationship

// FeatureFlag represents a feature flag
type FeatureFlag struct {
	Name               string     `json:"name"`
	Enabled            bool       `json:"enabled"`
	UpdatedAt          *time.Time `json:"updated_at"`
	CustomErrorMessage *string    `json:"custom_error_message"`
	Links              Links      `json:"links,omitempty"`
}

// FeatureFlagUpdateRequest represents a request to update a feature flag
type FeatureFlagUpdateRequest struct {
	Enabled            bool    `json:"enabled"`
	CustomErrorMessage *string `json:"custom_error_message,omitempty"`
}

// ServiceBroker represents a service broker
type ServiceBroker struct {
	Resource
	Name          string                     `json:"name"`
	URL           string                     `json:"url"`
	Relationships ServiceBrokerRelationships `json:"relationships"`
	Metadata      *Metadata                  `json:"metadata,omitempty"`
}

// ServiceBrokerRelationships represents service broker relationships
type ServiceBrokerRelationships struct {
	Space *Relationship `json:"space,omitempty"`
}

// ServiceBrokerAuthentication represents authentication for a service broker
type ServiceBrokerAuthentication struct {
	Type        string                                 `json:"type"`
	Credentials ServiceBrokerAuthenticationCredentials `json:"credentials"`
}

// ServiceBrokerAuthenticationCredentials represents authentication credentials
type ServiceBrokerAuthenticationCredentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// ServiceBrokerCreateRequest represents a request to create a service broker
type ServiceBrokerCreateRequest struct {
	Name           string                      `json:"name"`
	URL            string                      `json:"url"`
	Authentication ServiceBrokerAuthentication `json:"authentication"`
	Relationships  *ServiceBrokerRelationships `json:"relationships,omitempty"`
	Metadata       *Metadata                   `json:"metadata,omitempty"`
}

// ServiceBrokerUpdateRequest represents a request to update a service broker
type ServiceBrokerUpdateRequest struct {
	Name           *string                      `json:"name,omitempty"`
	URL            *string                      `json:"url,omitempty"`
	Authentication *ServiceBrokerAuthentication `json:"authentication,omitempty"`
	Metadata       *Metadata                    `json:"metadata,omitempty"`
}

// ServiceOffering represents a service offering
type ServiceOffering struct {
	Resource
	Name             string                       `json:"name"`
	Description      string                       `json:"description"`
	Available        bool                         `json:"available"`
	Tags             []string                     `json:"tags"`
	Requires         []string                     `json:"requires"`
	Shareable        bool                         `json:"shareable"`
	DocumentationURL *string                      `json:"documentation_url,omitempty"`
	BrokerCatalog    ServiceOfferingCatalog       `json:"broker_catalog"`
	Relationships    ServiceOfferingRelationships `json:"relationships"`
	Metadata         *Metadata                    `json:"metadata,omitempty"`
}

// ServiceOfferingCatalog represents catalog information for a service offering
type ServiceOfferingCatalog struct {
	ID       string                         `json:"id"`
	Metadata map[string]interface{}         `json:"metadata,omitempty"`
	Features ServiceOfferingCatalogFeatures `json:"features"`
}

// ServiceOfferingCatalogFeatures represents features of a service offering catalog
type ServiceOfferingCatalogFeatures struct {
	PlanUpdateable       bool `json:"plan_updateable"`
	Bindable             bool `json:"bindable"`
	InstancesRetrievable bool `json:"instances_retrievable"`
	BindingsRetrievable  bool `json:"bindings_retrievable"`
	AllowContextUpdates  bool `json:"allow_context_updates"`
}

// ServiceOfferingRelationships represents service offering relationships
type ServiceOfferingRelationships struct {
	ServiceBroker Relationship `json:"service_broker"`
}

// ServiceOfferingUpdateRequest represents a request to update a service offering
type ServiceOfferingUpdateRequest struct {
	Metadata *Metadata `json:"metadata,omitempty"`
}

// ServicePlan represents a service plan
type ServicePlan struct {
	Resource
	Name            string                   `json:"name"`
	Description     string                   `json:"description"`
	Available       bool                     `json:"available"`
	VisibilityType  string                   `json:"visibility_type"`
	Free            bool                     `json:"free"`
	Costs           []ServicePlanCost        `json:"costs"`
	MaintenanceInfo *ServicePlanMaintenance  `json:"maintenance_info,omitempty"`
	BrokerCatalog   ServicePlanCatalog       `json:"broker_catalog"`
	Schemas         ServicePlanSchemas       `json:"schemas"`
	Relationships   ServicePlanRelationships `json:"relationships"`
	Metadata        *Metadata                `json:"metadata,omitempty"`
}

// ServicePlanCost represents the cost information for a service plan
type ServicePlanCost struct {
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency"`
	Unit     string  `json:"unit"`
}

// ServicePlanMaintenance represents maintenance information for a service plan
type ServicePlanMaintenance struct {
	Version     string `json:"version"`
	Description string `json:"description"`
}

// ServicePlanCatalog represents catalog information for a service plan
type ServicePlanCatalog struct {
	ID                     string                     `json:"id"`
	Metadata               map[string]interface{}     `json:"metadata,omitempty"`
	MaximumPollingDuration *int                       `json:"maximum_polling_duration,omitempty"`
	Features               ServicePlanCatalogFeatures `json:"features"`
}

// ServicePlanCatalogFeatures represents features of a service plan catalog
type ServicePlanCatalogFeatures struct {
	PlanUpdateable bool `json:"plan_updateable"`
	Bindable       bool `json:"bindable"`
}

// ServicePlanSchemas represents the schemas for a service plan
type ServicePlanSchemas struct {
	ServiceInstance ServiceInstanceSchema `json:"service_instance"`
	ServiceBinding  ServiceBindingSchema  `json:"service_binding"`
}

// ServiceInstanceSchema represents the schema for service instance operations
type ServiceInstanceSchema struct {
	Create SchemaDefinition `json:"create"`
	Update SchemaDefinition `json:"update"`
}

// ServiceBindingSchema represents the schema for service binding operations
type ServiceBindingSchema struct {
	Create SchemaDefinition `json:"create"`
}

// SchemaDefinition represents a schema definition
type SchemaDefinition struct {
	Parameters map[string]interface{} `json:"parameters,omitempty"`
}

// ServicePlanRelationships represents service plan relationships
type ServicePlanRelationships struct {
	ServiceOffering Relationship  `json:"service_offering"`
	Space           *Relationship `json:"space,omitempty"`
}

// ServicePlanUpdateRequest represents a request to update a service plan
type ServicePlanUpdateRequest struct {
	Metadata *Metadata `json:"metadata,omitempty"`
}

// ServicePlanVisibility represents service plan visibility
type ServicePlanVisibility struct {
	Type          string                      `json:"type"`
	Organizations []ServicePlanVisibilityOrg  `json:"organizations,omitempty"`
	Space         *ServicePlanVisibilitySpace `json:"space,omitempty"`
}

// ServicePlanVisibilityOrg represents an organization in service plan visibility
type ServicePlanVisibilityOrg struct {
	GUID string `json:"guid"`
	Name string `json:"name,omitempty"`
}

// ServicePlanVisibilitySpace represents a space in service plan visibility
type ServicePlanVisibilitySpace struct {
	GUID string `json:"guid"`
	Name string `json:"name,omitempty"`
}

// ServicePlanVisibilityUpdateRequest represents a request to update service plan visibility
type ServicePlanVisibilityUpdateRequest struct {
	Type          string   `json:"type"`
	Organizations []string `json:"organizations,omitempty"`
}

// ServicePlanVisibilityApplyRequest represents a request to apply service plan visibility
type ServicePlanVisibilityApplyRequest struct {
	Type          string   `json:"type"`
	Organizations []string `json:"organizations,omitempty"`
}

// ServiceInstance represents a service instance
type ServiceInstance struct {
	Resource
	Name             string                        `json:"name"`
	Type             string                        `json:"type"` // "managed" or "user-provided"
	Tags             []string                      `json:"tags"`
	MaintenanceInfo  *ServiceInstanceMaintenance   `json:"maintenance_info,omitempty"`
	UpgradeAvailable bool                          `json:"upgrade_available"`
	DashboardURL     *string                       `json:"dashboard_url,omitempty"`
	LastOperation    *ServiceInstanceLastOperation `json:"last_operation"`
	SyslogDrainURL   *string                       `json:"syslog_drain_url,omitempty"`  // For user-provided
	RouteServiceURL  *string                       `json:"route_service_url,omitempty"` // For user-provided
	Relationships    ServiceInstanceRelationships  `json:"relationships"`
	Metadata         *Metadata                     `json:"metadata,omitempty"`
}

// ServiceInstanceMaintenance represents maintenance information for a service instance
type ServiceInstanceMaintenance struct {
	Version     string `json:"version"`
	Description string `json:"description,omitempty"`
}

// ServiceInstanceLastOperation represents the last operation performed on a service instance
type ServiceInstanceLastOperation struct {
	Type        string     `json:"type"`  // "create", "update", "delete"
	State       string     `json:"state"` // "initial", "in progress", "succeeded", "failed"
	Description string     `json:"description"`
	CreatedAt   *time.Time `json:"created_at"`
	UpdatedAt   *time.Time `json:"updated_at"`
}

// ServiceInstanceRelationships represents service instance relationships
type ServiceInstanceRelationships struct {
	Space       Relationship  `json:"space"`
	ServicePlan *Relationship `json:"service_plan,omitempty"` // For managed instances
}

// ServiceInstanceCreateRequest represents a request to create a service instance
type ServiceInstanceCreateRequest struct {
	Type            string                       `json:"type"` // "managed" or "user-provided"
	Name            string                       `json:"name"`
	Tags            []string                     `json:"tags,omitempty"`
	Parameters      map[string]interface{}       `json:"parameters,omitempty"`        // For managed
	Credentials     map[string]interface{}       `json:"credentials,omitempty"`       // For user-provided
	SyslogDrainURL  *string                      `json:"syslog_drain_url,omitempty"`  // For user-provided
	RouteServiceURL *string                      `json:"route_service_url,omitempty"` // For user-provided
	Relationships   ServiceInstanceRelationships `json:"relationships"`
	Metadata        *Metadata                    `json:"metadata,omitempty"`
}

// ServiceInstanceUpdateRequest represents a request to update a service instance
type ServiceInstanceUpdateRequest struct {
	Name            *string                       `json:"name,omitempty"`
	Tags            []string                      `json:"tags,omitempty"`
	Parameters      map[string]interface{}        `json:"parameters,omitempty"`        // For managed
	Credentials     map[string]interface{}        `json:"credentials,omitempty"`       // For user-provided
	SyslogDrainURL  *string                       `json:"syslog_drain_url,omitempty"`  // For user-provided
	RouteServiceURL *string                       `json:"route_service_url,omitempty"` // For user-provided
	MaintenanceInfo *ServiceInstanceMaintenance   `json:"maintenance_info,omitempty"`  // For managed
	Metadata        *Metadata                     `json:"metadata,omitempty"`
	Relationships   *ServiceInstanceRelationships `json:"relationships,omitempty"`
}

// ServiceInstanceParameters represents parameters for a managed service instance
type ServiceInstanceParameters struct {
	Parameters map[string]interface{} `json:"parameters"`
}

// ServiceInstanceCredentials represents credentials for a user-provided service instance
type ServiceInstanceCredentials struct {
	Credentials map[string]interface{} `json:"credentials"`
}

// ServiceInstanceSharedSpacesRelationships represents shared spaces relationships
type ServiceInstanceSharedSpacesRelationships struct {
	Data  []Relationship `json:"data"`
	Links Links          `json:"links,omitempty"`
}

// ServiceInstanceShareRequest represents a request to share a service instance
type ServiceInstanceShareRequest struct {
	Data []Relationship `json:"data"`
}

// ServiceInstanceUsageSummary represents usage summary for service instances
type ServiceInstanceUsageSummary struct {
	UsageSummary ServiceInstanceUsageData `json:"usage_summary"`
	Links        Links                    `json:"links,omitempty"`
}

// ServiceInstanceUsageData represents usage data for service instances
type ServiceInstanceUsageData struct {
	StartedInstances int `json:"started_instances"`
	MemoryInMB       int `json:"memory_in_mb"`
}

// ServiceInstancePermissions represents permissions for a service instance
type ServiceInstancePermissions struct {
	Read   bool `json:"read"`
	Manage bool `json:"manage"`
}

// ServiceCredentialBinding represents a service credential binding (new name for service binding)
type ServiceCredentialBinding struct {
	Resource
	Name          string                                 `json:"name"`
	Type          string                                 `json:"type"` // "app" or "key"
	LastOperation *ServiceCredentialBindingLastOperation `json:"last_operation,omitempty"`
	Metadata      *Metadata                              `json:"metadata,omitempty"`
	Relationships ServiceCredentialBindingRelationships  `json:"relationships"`
	Links         Links                                  `json:"links,omitempty"`
}

// ServiceCredentialBindingLastOperation represents the last operation for a service credential binding
type ServiceCredentialBindingLastOperation struct {
	Type        string     `json:"type"` // "create", "update", "delete"
	State       string     `json:"state"`
	Description *string    `json:"description,omitempty"`
	CreatedAt   *time.Time `json:"created_at,omitempty"`
	UpdatedAt   *time.Time `json:"updated_at,omitempty"`
}

// ServiceCredentialBindingRelationships represents relationships for a service credential binding
type ServiceCredentialBindingRelationships struct {
	App             *Relationship `json:"app,omitempty"` // Only for type="app"
	ServiceInstance Relationship  `json:"service_instance"`
}

// ServiceCredentialBindingCreateRequest represents a request to create a service credential binding
type ServiceCredentialBindingCreateRequest struct {
	Type          string                                `json:"type"` // "app" or "key"
	Name          *string                               `json:"name,omitempty"`
	Parameters    map[string]interface{}                `json:"parameters,omitempty"`
	Metadata      *Metadata                             `json:"metadata,omitempty"`
	Relationships ServiceCredentialBindingRelationships `json:"relationships"`
}

// ServiceCredentialBindingUpdateRequest represents a request to update a service credential binding
type ServiceCredentialBindingUpdateRequest struct {
	Metadata *Metadata `json:"metadata,omitempty"`
}

// ServiceCredentialBindingDetails represents the details of a service credential binding
type ServiceCredentialBindingDetails struct {
	Credentials    map[string]interface{} `json:"credentials"`
	SyslogDrainURL *string                `json:"syslog_drain_url,omitempty"`
	VolumeMounts   []interface{}          `json:"volume_mounts,omitempty"`
}

// ServiceCredentialBindingParameters represents the parameters of a service credential binding
type ServiceCredentialBindingParameters struct {
	Parameters map[string]interface{} `json:"parameters,omitempty"`
}

// ServiceBinding is an alias for ServiceCredentialBinding for backward compatibility
type ServiceBinding = ServiceCredentialBinding

// ServiceRouteBinding represents a service route binding
type ServiceRouteBinding struct {
	Resource
	RouteServiceURL *string                           `json:"route_service_url,omitempty"`
	LastOperation   *ServiceRouteBindingLastOperation `json:"last_operation,omitempty"`
	Metadata        *Metadata                         `json:"metadata,omitempty"`
	Relationships   ServiceRouteBindingRelationships  `json:"relationships"`
	Links           Links                             `json:"links,omitempty"`
}

// ServiceRouteBindingLastOperation represents the last operation for a service route binding
type ServiceRouteBindingLastOperation struct {
	Type        string     `json:"type"`  // "create", "update", "delete"
	State       string     `json:"state"` // "initial", "in_progress", "succeeded", "failed"
	Description *string    `json:"description,omitempty"`
	CreatedAt   *time.Time `json:"created_at,omitempty"`
	UpdatedAt   *time.Time `json:"updated_at,omitempty"`
}

// ServiceRouteBindingRelationships represents the relationships for a service route binding
type ServiceRouteBindingRelationships struct {
	ServiceInstance Relationship `json:"service_instance"`
	Route           Relationship `json:"route"`
}

// ServiceRouteBindingCreateRequest represents a request to create a service route binding
type ServiceRouteBindingCreateRequest struct {
	Parameters    map[string]interface{}           `json:"parameters,omitempty"`
	Metadata      *Metadata                        `json:"metadata,omitempty"`
	Relationships ServiceRouteBindingRelationships `json:"relationships"`
}

// ServiceRouteBindingUpdateRequest represents a request to update a service route binding
type ServiceRouteBindingUpdateRequest struct {
	Metadata *Metadata `json:"metadata,omitempty"`
}

// ServiceRouteBindingParameters represents parameters for a service route binding
type ServiceRouteBindingParameters struct {
	Parameters map[string]interface{} `json:"parameters,omitempty"`
}

// LogMessage represents a single log message
type LogMessage struct {
	Message     string    `json:"message"`
	MessageType string    `json:"message_type"`
	Timestamp   time.Time `json:"timestamp"`
	AppID       string    `json:"app_id"`
	SourceType  string    `json:"source_type"`
	SourceID    string    `json:"source_id"`
}

// AppLogs represents a collection of log messages for an app
type AppLogs struct {
	Messages []LogMessage `json:"messages"`
}

// LogCacheEnvelope represents a log cache response envelope
type LogCacheEnvelope struct {
	Timestamp  string               `json:"timestamp"`
	SourceID   string               `json:"source_id"`
	InstanceID string               `json:"instance_id"`
	Tags       map[string]string    `json:"tags"`
	Log        *LogCacheLogEnvelope `json:"log,omitempty"`
}

// LogCacheLogEnvelope represents the log content within a log cache envelope
type LogCacheLogEnvelope struct {
	Payload []byte `json:"payload"`
	Type    string `json:"type"`
}

// LogCacheResponse represents the response from log cache API
type LogCacheResponse struct {
	Envelopes LogCacheEnvelopesWrapper `json:"envelopes"`
}

// LogCacheEnvelopesWrapper wraps the batch array in the log cache response
type LogCacheEnvelopesWrapper struct {
	Batch []LogCacheEnvelope `json:"batch"`
}

// OrganizationQuota represents an organization quota
type OrganizationQuota struct {
	Resource
	Name          string                          `json:"name"`
	Apps          *OrganizationQuotaApps          `json:"apps,omitempty"`
	Services      *OrganizationQuotaServices      `json:"services,omitempty"`
	Routes        *OrganizationQuotaRoutes        `json:"routes,omitempty"`
	Domains       *OrganizationQuotaDomains       `json:"domains,omitempty"`
	Relationships *OrganizationQuotaRelationships `json:"relationships,omitempty"`
	Metadata      *Metadata                       `json:"metadata,omitempty"`
}

// OrganizationQuotaApps represents app limits in an organization quota
type OrganizationQuotaApps struct {
	TotalMemoryInMB              *int `json:"total_memory_in_mb,omitempty"`
	TotalInstanceMemoryInMB      *int `json:"total_instance_memory_in_mb,omitempty"`
	LogRateLimitInBytesPerSecond *int `json:"log_rate_limit_in_bytes_per_second,omitempty"`
	TotalInstances               *int `json:"total_instances,omitempty"`
	TotalAppTasks                *int `json:"total_app_tasks,omitempty"`
}

// OrganizationQuotaServices represents service limits in an organization quota
type OrganizationQuotaServices struct {
	PaidServicesAllowed   *bool `json:"paid_services_allowed,omitempty"`
	TotalServiceInstances *int  `json:"total_service_instances,omitempty"`
	TotalServiceKeys      *int  `json:"total_service_keys,omitempty"`
}

// OrganizationQuotaRoutes represents route limits in an organization quota
type OrganizationQuotaRoutes struct {
	TotalRoutes        *int `json:"total_routes,omitempty"`
	TotalReservedPorts *int `json:"total_reserved_ports,omitempty"`
}

// OrganizationQuotaDomains represents domain limits in an organization quota
type OrganizationQuotaDomains struct {
	TotalDomains *int `json:"total_domains,omitempty"`
}

// OrganizationQuotaRelationships represents organization quota relationships
type OrganizationQuotaRelationships struct {
	Organizations ToManyRelationship `json:"organizations"`
}

// OrganizationQuotaCreateRequest represents a request to create an organization quota
type OrganizationQuotaCreateRequest struct {
	Name     string                     `json:"name"`
	Apps     *OrganizationQuotaApps     `json:"apps,omitempty"`
	Services *OrganizationQuotaServices `json:"services,omitempty"`
	Routes   *OrganizationQuotaRoutes   `json:"routes,omitempty"`
	Domains  *OrganizationQuotaDomains  `json:"domains,omitempty"`
	Metadata *Metadata                  `json:"metadata,omitempty"`
}

// OrganizationQuotaUpdateRequest represents a request to update an organization quota
type OrganizationQuotaUpdateRequest struct {
	Name     *string                    `json:"name,omitempty"`
	Apps     *OrganizationQuotaApps     `json:"apps,omitempty"`
	Services *OrganizationQuotaServices `json:"services,omitempty"`
	Routes   *OrganizationQuotaRoutes   `json:"routes,omitempty"`
	Domains  *OrganizationQuotaDomains  `json:"domains,omitempty"`
	Metadata *Metadata                  `json:"metadata,omitempty"`
}

// SpaceQuotaV3 represents a space quota (v3 API)
type SpaceQuotaV3 struct {
	Resource
	Name          string                   `json:"name"`
	Apps          *SpaceQuotaApps          `json:"apps,omitempty"`
	Services      *SpaceQuotaServices      `json:"services,omitempty"`
	Routes        *SpaceQuotaRoutes        `json:"routes,omitempty"`
	Relationships *SpaceQuotaRelationships `json:"relationships,omitempty"`
	Metadata      *Metadata                `json:"metadata,omitempty"`
}

// SpaceQuotaApps represents app limits in a space quota
type SpaceQuotaApps struct {
	TotalMemoryInMB              *int `json:"total_memory_in_mb,omitempty"`
	TotalInstanceMemoryInMB      *int `json:"total_instance_memory_in_mb,omitempty"`
	LogRateLimitInBytesPerSecond *int `json:"log_rate_limit_in_bytes_per_second,omitempty"`
	TotalInstances               *int `json:"total_instances,omitempty"`
	TotalAppTasks                *int `json:"total_app_tasks,omitempty"`
}

// SpaceQuotaServices represents service limits in a space quota
type SpaceQuotaServices struct {
	PaidServicesAllowed   *bool `json:"paid_services_allowed,omitempty"`
	TotalServiceInstances *int  `json:"total_service_instances,omitempty"`
	TotalServiceKeys      *int  `json:"total_service_keys,omitempty"`
}

// SpaceQuotaRoutes represents route limits in a space quota
type SpaceQuotaRoutes struct {
	TotalRoutes        *int `json:"total_routes,omitempty"`
	TotalReservedPorts *int `json:"total_reserved_ports,omitempty"`
}

// SpaceQuotaRelationships represents space quota relationships
type SpaceQuotaRelationships struct {
	Organization Relationship       `json:"organization"`
	Spaces       ToManyRelationship `json:"spaces"`
}

// SpaceQuotaV3CreateRequest represents a request to create a space quota
type SpaceQuotaV3CreateRequest struct {
	Name          string                  `json:"name"`
	Apps          *SpaceQuotaApps         `json:"apps,omitempty"`
	Services      *SpaceQuotaServices     `json:"services,omitempty"`
	Routes        *SpaceQuotaRoutes       `json:"routes,omitempty"`
	Relationships SpaceQuotaRelationships `json:"relationships"`
	Metadata      *Metadata               `json:"metadata,omitempty"`
}

// SpaceQuotaV3UpdateRequest represents a request to update a space quota
type SpaceQuotaV3UpdateRequest struct {
	Name     *string             `json:"name,omitempty"`
	Apps     *SpaceQuotaApps     `json:"apps,omitempty"`
	Services *SpaceQuotaServices `json:"services,omitempty"`
	Routes   *SpaceQuotaRoutes   `json:"routes,omitempty"`
	Metadata *Metadata           `json:"metadata,omitempty"`
}

// Sidecar represents a sidecar
type Sidecar struct {
	Resource
	Name          string               `json:"name"`
	Command       string               `json:"command"`
	ProcessTypes  []string             `json:"process_types"`
	MemoryInMB    *int                 `json:"memory_in_mb,omitempty"`
	Origin        string               `json:"origin"`
	Relationships SidecarRelationships `json:"relationships"`
}

// SidecarRelationships represents sidecar relationships
type SidecarRelationships struct {
	App Relationship `json:"app"`
}

// SidecarCreateRequest represents a request to create a sidecar
type SidecarCreateRequest struct {
	Name         string   `json:"name"`
	Command      string   `json:"command"`
	ProcessTypes []string `json:"process_types"`
	MemoryInMB   *int     `json:"memory_in_mb,omitempty"`
}

// SidecarUpdateRequest represents a request to update a sidecar
type SidecarUpdateRequest struct {
	Name         *string  `json:"name,omitempty"`
	Command      *string  `json:"command,omitempty"`
	ProcessTypes []string `json:"process_types,omitempty"`
	MemoryInMB   *int     `json:"memory_in_mb,omitempty"`
}

// Revision represents a revision
type Revision struct {
	Resource
	Version       int                   `json:"version"`
	Droplet       RevisionDropletRef    `json:"droplet"`
	Processes     map[string]Process    `json:"processes"`
	Sidecars      []Sidecar             `json:"sidecars"`
	Relationships RevisionRelationships `json:"relationships"`
	Metadata      *Metadata             `json:"metadata,omitempty"`
	Description   *string               `json:"description,omitempty"`
	Deployable    bool                  `json:"deployable"`
}

// RevisionDropletRef represents a droplet reference in a revision
type RevisionDropletRef struct {
	GUID string `json:"guid"`
}

// RevisionRelationships represents revision relationships
type RevisionRelationships struct {
	App Relationship `json:"app"`
}

// RevisionUpdateRequest represents a request to update a revision
type RevisionUpdateRequest struct {
	Metadata *Metadata `json:"metadata,omitempty"`
}

// EnvironmentVariableGroup represents an environment variable group
type EnvironmentVariableGroup struct {
	Name      string                 `json:"name"`
	Var       map[string]interface{} `json:"var"`
	UpdatedAt *time.Time             `json:"updated_at,omitempty"`
	Links     Links                  `json:"links,omitempty"`
}

// AppUsageEvent represents an app usage event
type AppUsageEvent struct {
	Resource
	State                         string               `json:"state"`
	PreviousState                 *string              `json:"previous_state,omitempty"`
	MemoryInMBPerInstance         int                  `json:"memory_in_mb_per_instance"`
	PreviousMemoryInMBPerInstance *int                 `json:"previous_memory_in_mb_per_instance,omitempty"`
	InstanceCount                 int                  `json:"instance_count"`
	PreviousInstanceCount         *int                 `json:"previous_instance_count,omitempty"`
	AppName                       string               `json:"app_name"`
	AppGUID                       string               `json:"app_guid"`
	SpaceName                     string               `json:"space_name"`
	SpaceGUID                     string               `json:"space_guid"`
	OrganizationName              string               `json:"organization_name"`
	OrganizationGUID              string               `json:"organization_guid"`
	BuildpackName                 *string              `json:"buildpack_name,omitempty"`
	BuildpackGUID                 *string              `json:"buildpack_guid,omitempty"`
	Package                       AppUsageEventPackage `json:"package"`
	ParentAppName                 *string              `json:"parent_app_name,omitempty"`
	ParentAppGUID                 *string              `json:"parent_app_guid,omitempty"`
	ProcessType                   string               `json:"process_type"`
	TaskName                      *string              `json:"task_name,omitempty"`
	TaskGUID                      *string              `json:"task_guid,omitempty"`
}

// AppUsageEventPackage represents package information in an app usage event
type AppUsageEventPackage struct {
	State string `json:"state"`
}

// ServiceUsageEvent represents a service usage event
type ServiceUsageEvent struct {
	Resource
	State               string  `json:"state"`
	PreviousState       *string `json:"previous_state,omitempty"`
	ServiceInstanceName string  `json:"service_instance_name"`
	ServiceInstanceGUID string  `json:"service_instance_guid"`
	ServiceInstanceType string  `json:"service_instance_type"`
	ServicePlanName     string  `json:"service_plan_name"`
	ServicePlanGUID     string  `json:"service_plan_guid"`
	ServiceOfferingName string  `json:"service_offering_name"`
	ServiceOfferingGUID string  `json:"service_offering_guid"`
	ServiceBrokerName   string  `json:"service_broker_name"`
	ServiceBrokerGUID   string  `json:"service_broker_guid"`
	SpaceName           string  `json:"space_name"`
	SpaceGUID           string  `json:"space_guid"`
	OrganizationName    string  `json:"organization_name"`
	OrganizationGUID    string  `json:"organization_guid"`
}

// AuditEvent represents an audit event
type AuditEvent struct {
	Resource
	Type         string                  `json:"type"`
	Actor        AuditEventActor         `json:"actor"`
	Target       AuditEventTarget        `json:"target"`
	Data         map[string]interface{}  `json:"data"`
	Space        *AuditEventSpace        `json:"space,omitempty"`
	Organization *AuditEventOrganization `json:"organization,omitempty"`
}

// AuditEventActor represents the actor in an audit event
type AuditEventActor struct {
	GUID string `json:"guid"`
	Type string `json:"type"`
	Name string `json:"name"`
}

// AuditEventTarget represents the target in an audit event
type AuditEventTarget struct {
	GUID string `json:"guid"`
	Type string `json:"type"`
	Name string `json:"name"`
}

// AuditEventSpace represents space information in an audit event
type AuditEventSpace struct {
	GUID string `json:"guid"`
	Name string `json:"name"`
}

// AuditEventOrganization represents organization information in an audit event
type AuditEventOrganization struct {
	GUID string `json:"guid"`
	Name string `json:"name"`
}

// ResourceMatches represents resource matches
type ResourceMatches struct {
	Resources []ResourceMatch `json:"resources"`
}

// ResourceMatch represents a single resource match
type ResourceMatch struct {
	SHA1 string `json:"sha1"`
	Size int64  `json:"size"`
	Path string `json:"path"`
	Mode string `json:"mode"`
}

// ResourceMatchesRequest represents a request to create resource matches
type ResourceMatchesRequest struct {
	Resources []ResourceMatch `json:"resources"`
}
