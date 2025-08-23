package capi

import (
	"context"
	"io"
)

// AppsClient defines operations for apps
type AppsClient interface {
	Create(ctx context.Context, request *AppCreateRequest) (*App, error)
	Get(ctx context.Context, guid string) (*App, error)
	List(ctx context.Context, params *QueryParams) (*ListResponse[App], error)
	Update(ctx context.Context, guid string, request *AppUpdateRequest) (*App, error)
	Delete(ctx context.Context, guid string) error

	// Lifecycle operations
	Start(ctx context.Context, guid string) (*App, error)
	Stop(ctx context.Context, guid string) (*App, error)
	Restart(ctx context.Context, guid string) (*App, error)
	Restage(ctx context.Context, guid string) (*Build, error)

	// Environment operations
	GetEnv(ctx context.Context, guid string) (*AppEnvironment, error)
	GetEnvVars(ctx context.Context, guid string) (map[string]interface{}, error)
	UpdateEnvVars(ctx context.Context, guid string, envVars map[string]interface{}) (map[string]interface{}, error)

	// Other operations
	GetCurrentDroplet(ctx context.Context, guid string) (*Droplet, error)
	SetCurrentDroplet(ctx context.Context, guid string, dropletGUID string) (*Relationship, error)
	GetSSHEnabled(ctx context.Context, guid string) (*AppSSHEnabled, error)
	GetPermissions(ctx context.Context, guid string) (*AppPermissions, error)
	ClearBuildpackCache(ctx context.Context, guid string) error
	GetManifest(ctx context.Context, guid string) (string, error)

	// Logs operations
	GetRecentLogs(ctx context.Context, guid string, lines int) (*AppLogs, error)
	StreamLogs(ctx context.Context, guid string) (<-chan LogMessage, error)

	// Features operations
	GetFeatures(ctx context.Context, guid string) (*AppFeatures, error)
	GetFeature(ctx context.Context, guid, featureName string) (*AppFeature, error)
	UpdateFeature(ctx context.Context, guid, featureName string, request *AppFeatureUpdateRequest) (*AppFeature, error)
}

// OrganizationsClient defines operations for organizations
type OrganizationsClient interface {
	Create(ctx context.Context, request *OrganizationCreateRequest) (*Organization, error)
	Get(ctx context.Context, guid string) (*Organization, error)
	List(ctx context.Context, params *QueryParams) (*ListResponse[Organization], error)
	Update(ctx context.Context, guid string, request *OrganizationUpdateRequest) (*Organization, error)
	Delete(ctx context.Context, guid string) (*Job, error)

	// Relationships
	GetDefaultIsolationSegment(ctx context.Context, guid string) (*Relationship, error)
	SetDefaultIsolationSegment(ctx context.Context, guid string, isolationSegmentGUID string) (*Relationship, error)
	GetDefaultDomain(ctx context.Context, guid string) (*Domain, error)
	GetUsageSummary(ctx context.Context, guid string) (*OrganizationUsageSummary, error)
	ListUsers(ctx context.Context, guid string, params *QueryParams) (*ListResponse[User], error)
	ListDomains(ctx context.Context, guid string, params *QueryParams) (*ListResponse[Domain], error)
}

// SpacesClient defines operations for spaces
type SpacesClient interface {
	Create(ctx context.Context, request *SpaceCreateRequest) (*Space, error)
	Get(ctx context.Context, guid string) (*Space, error)
	List(ctx context.Context, params *QueryParams) (*ListResponse[Space], error)
	Update(ctx context.Context, guid string, request *SpaceUpdateRequest) (*Space, error)
	Delete(ctx context.Context, guid string) (*Job, error)

	// Features
	GetFeatures(ctx context.Context, guid string) (*SpaceFeatures, error)
	GetFeature(ctx context.Context, guid string, name string) (*SpaceFeature, error)
	UpdateFeature(ctx context.Context, guid string, name string, enabled bool) (*SpaceFeature, error)

	// Relationships
	GetIsolationSegment(ctx context.Context, guid string) (*Relationship, error)
	SetIsolationSegment(ctx context.Context, guid string, isolationSegmentGUID string) (*Relationship, error)
	GetUsageSummary(ctx context.Context, guid string) (*SpaceUsageSummary, error)
	ListUsers(ctx context.Context, guid string, params *QueryParams) (*ListResponse[User], error)
	ListManagers(ctx context.Context, guid string, params *QueryParams) (*ListResponse[User], error)
	ListDevelopers(ctx context.Context, guid string, params *QueryParams) (*ListResponse[User], error)
	ListAuditors(ctx context.Context, guid string, params *QueryParams) (*ListResponse[User], error)
	ListSupporters(ctx context.Context, guid string, params *QueryParams) (*ListResponse[User], error)

	// Space quota
	GetQuota(ctx context.Context, guid string) (*SpaceQuota, error)
	ApplyQuota(ctx context.Context, guid string, quotaGUID string) (*Relationship, error)
	RemoveQuota(ctx context.Context, guid string) error

	// Security Groups
	ListRunningSecurityGroups(ctx context.Context, guid string, params *QueryParams) (*ListResponse[SecurityGroup], error)
	ListStagingSecurityGroups(ctx context.Context, guid string, params *QueryParams) (*ListResponse[SecurityGroup], error)

	// Manifest operations
	ApplyManifest(ctx context.Context, guid string, manifest string) (*Job, error)
	CreateManifestDiff(ctx context.Context, guid string, manifest string) (*ManifestDiff, error)

	// Routes
	DeleteUnmappedRoutes(ctx context.Context, guid string) (*Job, error)
}

// DomainsClient defines operations for domains
type DomainsClient interface {
	Create(ctx context.Context, request *DomainCreateRequest) (*Domain, error)
	Get(ctx context.Context, guid string) (*Domain, error)
	List(ctx context.Context, params *QueryParams) (*ListResponse[Domain], error)
	Update(ctx context.Context, guid string, request *DomainUpdateRequest) (*Domain, error)
	Delete(ctx context.Context, guid string) (*Job, error)

	// Sharing
	ShareWithOrganization(ctx context.Context, guid string, orgGUIDs []string) (*ToManyRelationship, error)
	UnshareFromOrganization(ctx context.Context, guid string, orgGUID string) error
	CheckRouteReservations(ctx context.Context, guid string, request *RouteReservationRequest) (*RouteReservation, error)
}

// RoutesClient defines operations for routes
type RoutesClient interface {
	Create(ctx context.Context, request *RouteCreateRequest) (*Route, error)
	Get(ctx context.Context, guid string) (*Route, error)
	List(ctx context.Context, params *QueryParams) (*ListResponse[Route], error)
	Update(ctx context.Context, guid string, request *RouteUpdateRequest) (*Route, error)
	Delete(ctx context.Context, guid string) (*Job, error)

	// Destinations
	ListDestinations(ctx context.Context, guid string) (*RouteDestinations, error)
	InsertDestinations(ctx context.Context, guid string, destinations []RouteDestination) (*RouteDestinations, error)
	ReplaceDestinations(ctx context.Context, guid string, destinations []RouteDestination) (*RouteDestinations, error)
	UpdateDestination(ctx context.Context, guid string, destGUID string, protocol string) (*RouteDestination, error)
	RemoveDestination(ctx context.Context, guid string, destGUID string) error

	// Sharing
	ListSharedSpaces(ctx context.Context, guid string) (*ListResponse[Space], error)
	ShareWithSpace(ctx context.Context, guid string, spaceGUIDs []string) (*ToManyRelationship, error)
	UnshareFromSpace(ctx context.Context, guid string, spaceGUID string) error
	TransferOwnership(ctx context.Context, guid string, spaceGUID string) (*Route, error)
}

// ServiceBrokersClient defines operations for service brokers
type ServiceBrokersClient interface {
	Create(ctx context.Context, request *ServiceBrokerCreateRequest) (*Job, error)
	Get(ctx context.Context, guid string) (*ServiceBroker, error)
	List(ctx context.Context, params *QueryParams) (*ListResponse[ServiceBroker], error)
	Update(ctx context.Context, guid string, request *ServiceBrokerUpdateRequest) (*Job, error)
	Delete(ctx context.Context, guid string) (*Job, error)
}

// ServiceOfferingsClient defines operations for service offerings
type ServiceOfferingsClient interface {
	Get(ctx context.Context, guid string) (*ServiceOffering, error)
	List(ctx context.Context, params *QueryParams) (*ListResponse[ServiceOffering], error)
	Update(ctx context.Context, guid string, request *ServiceOfferingUpdateRequest) (*ServiceOffering, error)
	Delete(ctx context.Context, guid string) error
}

// ServicePlansClient defines operations for service plans
type ServicePlansClient interface {
	Get(ctx context.Context, guid string) (*ServicePlan, error)
	List(ctx context.Context, params *QueryParams) (*ListResponse[ServicePlan], error)
	Update(ctx context.Context, guid string, request *ServicePlanUpdateRequest) (*ServicePlan, error)
	Delete(ctx context.Context, guid string) error

	// Visibility
	GetVisibility(ctx context.Context, guid string) (*ServicePlanVisibility, error)
	UpdateVisibility(ctx context.Context, guid string, request *ServicePlanVisibilityUpdateRequest) (*ServicePlanVisibility, error)
	ApplyVisibility(ctx context.Context, guid string, request *ServicePlanVisibilityApplyRequest) (*ServicePlanVisibility, error)
	RemoveOrgFromVisibility(ctx context.Context, guid string, orgGUID string) error
}

// ServiceInstancesClient defines operations for service instances
type ServiceInstancesClient interface {
	Create(ctx context.Context, request *ServiceInstanceCreateRequest) (interface{}, error) // Returns *ServiceInstance for user-provided, *Job for managed
	Get(ctx context.Context, guid string) (*ServiceInstance, error)
	List(ctx context.Context, params *QueryParams) (*ListResponse[ServiceInstance], error)
	Update(ctx context.Context, guid string, request *ServiceInstanceUpdateRequest) (interface{}, error) // Returns *ServiceInstance for user-provided, *Job for managed
	Delete(ctx context.Context, guid string) (*Job, error)

	// Parameters for managed instances
	GetParameters(ctx context.Context, guid string) (*ServiceInstanceParameters, error)

	// Sharing operations
	ListSharedSpaces(ctx context.Context, guid string) (*ServiceInstanceSharedSpacesRelationships, error)
	ShareWithSpaces(ctx context.Context, guid string, request *ServiceInstanceShareRequest) (*ServiceInstanceSharedSpacesRelationships, error)
	UnshareFromSpace(ctx context.Context, guid string, spaceGUID string) error
}

// ServiceCredentialBindingsClient provides operations for Service Credential Bindings (v3 name for service bindings)
type ServiceCredentialBindingsClient interface {
	Create(ctx context.Context, request *ServiceCredentialBindingCreateRequest) (interface{}, error) // Returns *ServiceCredentialBinding or *Job
	Get(ctx context.Context, guid string) (*ServiceCredentialBinding, error)
	List(ctx context.Context, params *QueryParams) (*ListResponse[ServiceCredentialBinding], error)
	Update(ctx context.Context, guid string, request *ServiceCredentialBindingUpdateRequest) (*ServiceCredentialBinding, error)
	Delete(ctx context.Context, guid string) (*Job, error)
	GetDetails(ctx context.Context, guid string) (*ServiceCredentialBindingDetails, error)
	GetParameters(ctx context.Context, guid string) (*ServiceCredentialBindingParameters, error)
}

// ServiceBindingsClient is an alias for ServiceCredentialBindingsClient for backward compatibility
type ServiceBindingsClient = ServiceCredentialBindingsClient

// ServiceRouteBindingsClient defines operations for service route bindings
type ServiceRouteBindingsClient interface {
	Create(ctx context.Context, request *ServiceRouteBindingCreateRequest) (interface{}, error) // Returns *ServiceRouteBinding or *Job
	Get(ctx context.Context, guid string) (*ServiceRouteBinding, error)
	List(ctx context.Context, params *QueryParams) (*ListResponse[ServiceRouteBinding], error)
	Update(ctx context.Context, guid string, request *ServiceRouteBindingUpdateRequest) (*ServiceRouteBinding, error)
	Delete(ctx context.Context, guid string) (*Job, error)
	GetParameters(ctx context.Context, guid string) (*ServiceRouteBindingParameters, error)
}

// BuildpacksClient provides operations for managing buildpacks
type BuildpacksClient interface {
	Create(ctx context.Context, request *BuildpackCreateRequest) (*Buildpack, error)
	Get(ctx context.Context, guid string) (*Buildpack, error)
	List(ctx context.Context, params *QueryParams) (*ListResponse[Buildpack], error)
	Update(ctx context.Context, guid string, request *BuildpackUpdateRequest) (*Buildpack, error)
	Delete(ctx context.Context, guid string) (*Job, error)
	Upload(ctx context.Context, guid string, bits io.Reader) (*Buildpack, error)
}

// Additional client interfaces for other resources...
type BuildsClient interface {
	Create(ctx context.Context, request *BuildCreateRequest) (*Build, error)
	Get(ctx context.Context, guid string) (*Build, error)
	List(ctx context.Context, params *QueryParams) (*ListResponse[Build], error)
	ListForApp(ctx context.Context, appGUID string, params *QueryParams) (*ListResponse[Build], error)
	Update(ctx context.Context, guid string, request *BuildUpdateRequest) (*Build, error)
}

type DeploymentsClient interface {
	Create(ctx context.Context, request *DeploymentCreateRequest) (*Deployment, error)
	Get(ctx context.Context, guid string) (*Deployment, error)
	List(ctx context.Context, params *QueryParams) (*ListResponse[Deployment], error)
	Update(ctx context.Context, guid string, request *DeploymentUpdateRequest) (*Deployment, error)
	Cancel(ctx context.Context, guid string) error
	Continue(ctx context.Context, guid string) error
}

type DropletsClient interface {
	Create(ctx context.Context, request *DropletCreateRequest) (*Droplet, error)
	Get(ctx context.Context, guid string) (*Droplet, error)
	List(ctx context.Context, params *QueryParams) (*ListResponse[Droplet], error)
	ListForApp(ctx context.Context, appGUID string, params *QueryParams) (*ListResponse[Droplet], error)
	ListForPackage(ctx context.Context, packageGUID string, params *QueryParams) (*ListResponse[Droplet], error)
	Update(ctx context.Context, guid string, request *DropletUpdateRequest) (*Droplet, error)
	Delete(ctx context.Context, guid string) error
	Copy(ctx context.Context, sourceGUID string, request *DropletCopyRequest) (*Droplet, error)
	Download(ctx context.Context, guid string) ([]byte, error)
	Upload(ctx context.Context, guid string, bits []byte) (*Droplet, error)
}

type PackagesClient interface {
	Create(ctx context.Context, request *PackageCreateRequest) (*Package, error)
	Get(ctx context.Context, guid string) (*Package, error)
	List(ctx context.Context, params *QueryParams) (*ListResponse[Package], error)
	Update(ctx context.Context, guid string, request *PackageUpdateRequest) (*Package, error)
	Delete(ctx context.Context, guid string) error
	Upload(ctx context.Context, guid string, zipFile []byte) (*Package, error)
	Download(ctx context.Context, guid string) ([]byte, error)
	Copy(ctx context.Context, sourceGUID string, request *PackageCopyRequest) (*Package, error)
}

type ProcessesClient interface {
	Get(ctx context.Context, guid string) (*Process, error)
	List(ctx context.Context, params *QueryParams) (*ListResponse[Process], error)
	Update(ctx context.Context, guid string, request *ProcessUpdateRequest) (*Process, error)
	Scale(ctx context.Context, guid string, request *ProcessScaleRequest) (*Process, error)
	GetStats(ctx context.Context, guid string) (*ProcessStats, error)
	TerminateInstance(ctx context.Context, guid string, index int) error
}

type TasksClient interface {
	Create(ctx context.Context, appGUID string, request *TaskCreateRequest) (*Task, error)
	Get(ctx context.Context, guid string) (*Task, error)
	List(ctx context.Context, params *QueryParams) (*ListResponse[Task], error)
	Update(ctx context.Context, guid string, request *TaskUpdateRequest) (*Task, error)
	Cancel(ctx context.Context, guid string) (*Task, error)
}

type StacksClient interface {
	Create(ctx context.Context, request *StackCreateRequest) (*Stack, error)
	Get(ctx context.Context, guid string) (*Stack, error)
	List(ctx context.Context, params *QueryParams) (*ListResponse[Stack], error)
	Update(ctx context.Context, guid string, request *StackUpdateRequest) (*Stack, error)
	Delete(ctx context.Context, guid string) error
	ListApps(ctx context.Context, guid string, params *QueryParams) (*ListResponse[App], error)
}

type UsersClient interface {
	Create(ctx context.Context, request *UserCreateRequest) (*User, error)
	Get(ctx context.Context, guid string) (*User, error)
	List(ctx context.Context, params *QueryParams) (*ListResponse[User], error)
	Update(ctx context.Context, guid string, request *UserUpdateRequest) (*User, error)
	Delete(ctx context.Context, guid string) (*Job, error)
}

type RolesClient interface {
	Create(ctx context.Context, request *RoleCreateRequest) (*Role, error)
	Get(ctx context.Context, guid string) (*Role, error)
	List(ctx context.Context, params *QueryParams) (*ListResponse[Role], error)
	Delete(ctx context.Context, guid string) error
}

type SecurityGroupsClient interface {
	Create(ctx context.Context, request *SecurityGroupCreateRequest) (*SecurityGroup, error)
	Get(ctx context.Context, guid string) (*SecurityGroup, error)
	List(ctx context.Context, params *QueryParams) (*ListResponse[SecurityGroup], error)
	Update(ctx context.Context, guid string, request *SecurityGroupUpdateRequest) (*SecurityGroup, error)
	Delete(ctx context.Context, guid string) (*Job, error)

	// Space bindings
	BindRunningSpaces(ctx context.Context, guid string, spaceGUIDs []string) (*ToManyRelationship, error)
	UnbindRunningSpace(ctx context.Context, guid string, spaceGUID string) error
	BindStagingSpaces(ctx context.Context, guid string, spaceGUIDs []string) (*ToManyRelationship, error)
	UnbindStagingSpace(ctx context.Context, guid string, spaceGUID string) error
}

type IsolationSegmentsClient interface {
	Create(ctx context.Context, request *IsolationSegmentCreateRequest) (*IsolationSegment, error)
	Get(ctx context.Context, guid string) (*IsolationSegment, error)
	List(ctx context.Context, params *QueryParams) (*ListResponse[IsolationSegment], error)
	Update(ctx context.Context, guid string, request *IsolationSegmentUpdateRequest) (*IsolationSegment, error)
	Delete(ctx context.Context, guid string) error

	// Organization entitlements
	EntitleOrganizations(ctx context.Context, guid string, orgGUIDs []string) (*ToManyRelationship, error)
	RevokeOrganization(ctx context.Context, guid string, orgGUID string) error
	ListOrganizations(ctx context.Context, guid string) (*ListResponse[Organization], error)
	ListSpaces(ctx context.Context, guid string) (*ListResponse[Space], error)
}

// FeatureFlagsClient provides access to Feature Flags resources
type FeatureFlagsClient interface {
	Get(ctx context.Context, name string) (*FeatureFlag, error)
	List(ctx context.Context, params *QueryParams) (*ListResponse[FeatureFlag], error)
	Update(ctx context.Context, name string, request *FeatureFlagUpdateRequest) (*FeatureFlag, error)
}

type JobsClient interface {
	Get(ctx context.Context, guid string) (*Job, error)
	PollUntilComplete(ctx context.Context, guid string) (*Job, error)
}

// OrganizationQuotasClient defines operations for organization quotas
type OrganizationQuotasClient interface {
	Create(ctx context.Context, request *OrganizationQuotaCreateRequest) (*OrganizationQuota, error)
	Get(ctx context.Context, guid string) (*OrganizationQuota, error)
	List(ctx context.Context, params *QueryParams) (*ListResponse[OrganizationQuota], error)
	Update(ctx context.Context, guid string, request *OrganizationQuotaUpdateRequest) (*OrganizationQuota, error)
	Delete(ctx context.Context, guid string) error
	ApplyToOrganizations(ctx context.Context, quotaGUID string, orgGUIDs []string) (*ToManyRelationship, error)
}

// SpaceQuotasClient defines operations for space quotas
type SpaceQuotasClient interface {
	Create(ctx context.Context, request *SpaceQuotaV3CreateRequest) (*SpaceQuotaV3, error)
	Get(ctx context.Context, guid string) (*SpaceQuotaV3, error)
	List(ctx context.Context, params *QueryParams) (*ListResponse[SpaceQuotaV3], error)
	Update(ctx context.Context, guid string, request *SpaceQuotaV3UpdateRequest) (*SpaceQuotaV3, error)
	Delete(ctx context.Context, guid string) error
	ApplyToSpaces(ctx context.Context, quotaGUID string, spaceGUIDs []string) (*ToManyRelationship, error)
	RemoveFromSpace(ctx context.Context, quotaGUID string, spaceGUID string) error
}

// SidecarsClient defines operations for sidecars
type SidecarsClient interface {
	Get(ctx context.Context, guid string) (*Sidecar, error)
	Update(ctx context.Context, guid string, request *SidecarUpdateRequest) (*Sidecar, error)
	Delete(ctx context.Context, guid string) error
	ListForProcess(ctx context.Context, processGUID string, params *QueryParams) (*ListResponse[Sidecar], error)
}

// RevisionsClient defines operations for revisions
type RevisionsClient interface {
	Get(ctx context.Context, guid string) (*Revision, error)
	Update(ctx context.Context, guid string, request *RevisionUpdateRequest) (*Revision, error)
	GetEnvironmentVariables(ctx context.Context, guid string) (map[string]interface{}, error)
}

// EnvironmentVariableGroupsClient defines operations for environment variable groups
type EnvironmentVariableGroupsClient interface {
	Get(ctx context.Context, name string) (*EnvironmentVariableGroup, error)
	Update(ctx context.Context, name string, envVars map[string]interface{}) (*EnvironmentVariableGroup, error)
}

// AppUsageEventsClient defines operations for app usage events
type AppUsageEventsClient interface {
	Get(ctx context.Context, guid string) (*AppUsageEvent, error)
	List(ctx context.Context, params *QueryParams) (*ListResponse[AppUsageEvent], error)
	PurgeAndReseed(ctx context.Context) error
}

// ServiceUsageEventsClient defines operations for service usage events
type ServiceUsageEventsClient interface {
	Get(ctx context.Context, guid string) (*ServiceUsageEvent, error)
	List(ctx context.Context, params *QueryParams) (*ListResponse[ServiceUsageEvent], error)
	PurgeAndReseed(ctx context.Context) error
}

// AuditEventsClient defines operations for audit events
type AuditEventsClient interface {
	Get(ctx context.Context, guid string) (*AuditEvent, error)
	List(ctx context.Context, params *QueryParams) (*ListResponse[AuditEvent], error)
}

// ResourceMatchesClient defines operations for resource matches
type ResourceMatchesClient interface {
	Create(ctx context.Context, request *ResourceMatchesRequest) (*ResourceMatches, error)
}
