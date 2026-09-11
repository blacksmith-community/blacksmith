package capi

import (
	"context"
	"io"
)

// AppsClient defines operations for apps.
// AppCRUDClient provides basic CRUD operations for apps.
type AppCRUDClient interface {
	Create(ctx context.Context, request *AppCreateRequest) (*App, error)
	Get(ctx context.Context, guid string, opts ...AppGetOption) (*App, error)
	List(ctx context.Context, params *QueryParams, opts ...AppListOption) (*ListResponse[App], error)
	Update(ctx context.Context, guid string, request *AppUpdateRequest) (*App, error)
	// Delete issues DELETE /v3/apps/{guid}. CF v3 returns 202 Accepted with a
	// Job resource describing the async deletion; callers poll Jobs().Get
	// (or Jobs().PollUntilComplete) until the job is terminal. Matches the
	// pattern used by OrganizationsClient.Delete and SpacesClient.Delete.
	Delete(ctx context.Context, guid string) (*Job, error)
}

// AppLifecycleClient provides app lifecycle operations.
//
// start/stop/restart/restage all POST to /v3/apps/{guid}/actions/{action}
// which CF v3 treats as async: 202 + Location → /v3/jobs/{jobGuid}.
// Each method returns a Job with GUID populated from the Location
// header; callers poll via Jobs().Get / Jobs().PollUntilComplete for
// terminal state.
//
// Restage is deliberately absent: CF v3 has no /v3/apps/{guid}/actions/restage
// endpoint — restage was replaced by the builds resource (see v3 API docs).
// Callers needing restage semantics must compose: find READY package →
// Builds.Create → poll Builds.Get → Apps.Stop → Apps.SetCurrentDroplet →
// Apps.Start. The cf-cli `shared.AppStager` is the reference composition.
type AppLifecycleClient interface {
	Start(ctx context.Context, guid string) (*Job, error)
	Stop(ctx context.Context, guid string) (*Job, error)
	Restart(ctx context.Context, guid string) (*Job, error)
}

// AppEnvironmentClient provides app environment operations.
type AppEnvironmentClient interface {
	GetEnv(ctx context.Context, guid string) (*AppEnvironment, error)
	GetEnvVars(ctx context.Context, guid string) (map[string]any, error)
	UpdateEnvVars(ctx context.Context, guid string, envVars map[string]any) (map[string]any, error)
}

// AppDropletClient provides app droplet operations.
type AppDropletClient interface {
	GetCurrentDroplet(ctx context.Context, guid string) (*Droplet, error)
	SetCurrentDroplet(ctx context.Context, guid string, dropletGUID string) (*Relationship, error)
}

// AppFeatureClient provides app feature operations.
type AppFeatureClient interface {
	GetFeatures(ctx context.Context, guid string) (*AppFeatures, error)
	GetFeature(ctx context.Context, guid, featureName string) (*AppFeature, error)
	UpdateFeature(ctx context.Context, guid, featureName string, request *AppFeatureUpdateRequest) (*AppFeature, error)
}

// AppLogClient provides app logging operations.
type AppLogClient interface {
	GetRecentLogs(ctx context.Context, guid string, lines int) (*AppLogs, error)
	StreamLogs(ctx context.Context, guid string) (<-chan LogMessage, error)
}

// AppMiscClient provides miscellaneous app operations.
type AppMiscClient interface {
	GetSSHEnabled(ctx context.Context, guid string) (*AppSSHEnabled, error)
	GetPermissions(ctx context.Context, guid string) (*AppPermissions, error)
	ClearBuildpackCache(ctx context.Context, guid string) error
	GetManifest(ctx context.Context, guid string) (string, error)
}

type AppsClient interface {
	// Composite interfaces for app operations
	AppCRUDClient
	AppLifecycleClient
	AppEnvironmentClient
	AppDropletClient
	AppFeatureClient
	AppLogClient
	AppMiscClient
}

// OrganizationsClient defines operations for organizations.
// OrganizationCRUDClient provides basic CRUD operations for organizations.
type OrganizationCRUDClient interface {
	Create(ctx context.Context, request *OrganizationCreateRequest) (*Organization, error)
	Get(ctx context.Context, guid string) (*Organization, error)
	List(ctx context.Context, params *QueryParams, opts ...OrganizationListOption) (*ListResponse[Organization], error)
	Update(ctx context.Context, guid string, request *OrganizationUpdateRequest) (*Organization, error)
	Delete(ctx context.Context, guid string) (*Job, error)
}

// OrganizationRelationshipClient provides organization relationship operations.
type OrganizationRelationshipClient interface {
	GetDefaultIsolationSegment(ctx context.Context, guid string) (*Relationship, error)
	SetDefaultIsolationSegment(ctx context.Context, guid string, isolationSegmentGUID string) (*Relationship, error)
	GetDefaultDomain(ctx context.Context, guid string) (*Domain, error)
	GetUsageSummary(ctx context.Context, guid string) (*OrganizationUsageSummary, error)
	ListUsers(ctx context.Context, guid string, params *QueryParams) (*ListResponse[User], error)
	ListDomains(ctx context.Context, guid string, params *QueryParams) (*ListResponse[Domain], error)
}

type OrganizationsClient interface {
	// Composite interfaces for organization operations
	OrganizationCRUDClient
	OrganizationRelationshipClient
}

// SpacesClient defines operations for spaces.
// SpaceCRUDClient provides basic CRUD operations for spaces.
type SpaceCRUDClient interface {
	Create(ctx context.Context, request *SpaceCreateRequest) (*Space, error)
	Get(ctx context.Context, guid string, opts ...SpaceGetOption) (*Space, error)
	List(ctx context.Context, params *QueryParams, opts ...SpaceListOption) (*ListResponse[Space], error)
	Update(ctx context.Context, guid string, request *SpaceUpdateRequest) (*Space, error)
	Delete(ctx context.Context, guid string) (*Job, error)
}

// SpaceFeatureClient provides space feature operations.
type SpaceFeatureClient interface {
	GetFeatures(ctx context.Context, guid string) (*SpaceFeatures, error)
	GetFeature(ctx context.Context, guid string, name string) (*SpaceFeature, error)
	UpdateFeature(ctx context.Context, guid string, name string, enabled bool) (*SpaceFeature, error)
}

// SpaceRelationshipClient provides space relationship operations.
type SpaceRelationshipClient interface {
	GetIsolationSegment(ctx context.Context, guid string) (*Relationship, error)
	SetIsolationSegment(ctx context.Context, guid string, isolationSegmentGUID string) (*Relationship, error)
	GetUsageSummary(ctx context.Context, guid string) (*SpaceUsageSummary, error)
	ListUsers(ctx context.Context, guid string, params *QueryParams) (*ListResponse[User], error)
	ListManagers(ctx context.Context, guid string, params *QueryParams) (*ListResponse[User], error)
	ListDevelopers(ctx context.Context, guid string, params *QueryParams) (*ListResponse[User], error)
	ListAuditors(ctx context.Context, guid string, params *QueryParams) (*ListResponse[User], error)
	ListSupporters(ctx context.Context, guid string, params *QueryParams) (*ListResponse[User], error)
}

// SpaceQuotaClient provides space quota operations.
type SpaceQuotaClient interface {
	GetQuota(ctx context.Context, guid string) (*SpaceQuota, error)
	ApplyQuota(ctx context.Context, guid string, quotaGUID string) (*Relationship, error)
	RemoveQuota(ctx context.Context, guid string) error
}

// SpaceSecurityClient provides space security group operations.
type SpaceSecurityClient interface {
	ListRunningSecurityGroups(ctx context.Context, guid string, params *QueryParams) (*ListResponse[SecurityGroup], error)
	ListStagingSecurityGroups(ctx context.Context, guid string, params *QueryParams) (*ListResponse[SecurityGroup], error)
}

// SpaceManifestClient provides space manifest operations.
type SpaceManifestClient interface {
	ApplyManifest(ctx context.Context, guid string, manifest string) (*Job, error)
	CreateManifestDiff(ctx context.Context, guid string, manifest string) (*ManifestDiff, error)
}

// SpaceRouteClient provides space route operations.
type SpaceRouteClient interface {
	DeleteUnmappedRoutes(ctx context.Context, guid string) (*Job, error)
}

type SpacesClient interface {
	// Composite interfaces for space operations
	SpaceCRUDClient
	SpaceFeatureClient
	SpaceRelationshipClient
	SpaceQuotaClient
	SpaceSecurityClient
	SpaceManifestClient
	SpaceRouteClient
}

// DomainsClient defines operations for domains.
type DomainsClient interface {
	Create(ctx context.Context, request *DomainCreateRequest) (*Domain, error)
	Get(ctx context.Context, guid string) (*Domain, error)
	List(ctx context.Context, params *QueryParams, opts ...DomainListOption) (*ListResponse[Domain], error)
	Update(ctx context.Context, guid string, request *DomainUpdateRequest) (*Domain, error)
	Delete(ctx context.Context, guid string) (*Job, error)

	// Sharing
	ShareWithOrganization(ctx context.Context, guid string, orgGUIDs []string) (*ToManyRelationship, error)
	UnshareFromOrganization(ctx context.Context, guid string, orgGUID string) error
	CheckRouteReservations(ctx context.Context, guid string, request *RouteReservationRequest) (*RouteReservation, error)
}

// RoutesClient defines operations for routes.
// RouteCRUDClient provides basic CRUD operations for routes.
type RouteCRUDClient interface {
	Create(ctx context.Context, request *RouteCreateRequest) (*Route, error)
	Get(ctx context.Context, guid string, opts ...RouteGetOption) (*Route, error)
	List(ctx context.Context, params *QueryParams, opts ...RouteListOption) (*ListResponse[Route], error)
	Update(ctx context.Context, guid string, request *RouteUpdateRequest) (*Route, error)
	Delete(ctx context.Context, guid string) (*Job, error)
}

// RouteDestinationClient provides route destination operations.
type RouteDestinationClient interface {
	ListDestinations(ctx context.Context, guid string, opts ...RouteDestinationsOption) (*RouteDestinations, error)
	InsertDestinations(ctx context.Context, guid string, destinations []RouteDestination) (*RouteDestinations, error)
	ReplaceDestinations(ctx context.Context, guid string, destinations []RouteDestination) (*RouteDestinations, error)
	UpdateDestination(ctx context.Context, guid string, destGUID string, protocol string) (*RouteDestination, error)
	RemoveDestination(ctx context.Context, guid string, destGUID string) error
}

// RouteSharingClient provides route sharing operations.
type RouteSharingClient interface {
	ListSharedSpaces(ctx context.Context, guid string) (*ListResponse[Space], error)
	ShareWithSpace(ctx context.Context, guid string, spaceGUIDs []string) (*ToManyRelationship, error)
	UnshareFromSpace(ctx context.Context, guid string, spaceGUID string) error
	TransferOwnership(ctx context.Context, guid string, spaceGUID string) (*Route, error)
}

type RoutesClient interface {
	// Composite interfaces for route operations
	RouteCRUDClient
	RouteDestinationClient
	RouteSharingClient
}

// RoutePoliciesClient defines operations for route policies
// (CF v3 3.225.0, experimental).
type RoutePoliciesClient interface {
	Create(ctx context.Context, request *RoutePolicyCreateRequest) (*RoutePolicy, error)
	Get(ctx context.Context, guid string, opts ...RoutePolicyGetOption) (*RoutePolicy, error)
	List(ctx context.Context, params *QueryParams, opts ...RoutePolicyListOption) (*ListResponse[RoutePolicy], error)
	Update(ctx context.Context, guid string, request *RoutePolicyUpdateRequest) (*RoutePolicy, error)
	Delete(ctx context.Context, guid string) error
}

// ServiceBrokersClient defines operations for service brokers.
type ServiceBrokersClient interface {
	Create(ctx context.Context, request *ServiceBrokerCreateRequest) (*Job, error)
	Get(ctx context.Context, guid string) (*ServiceBroker, error)
	List(ctx context.Context, params *QueryParams, opts ...ServiceBrokerListOption) (*ListResponse[ServiceBroker], error)
	Update(ctx context.Context, guid string, request *ServiceBrokerUpdateRequest) (*Job, error)
	Delete(ctx context.Context, guid string) (*Job, error)
}

// ServiceOfferingsClient defines operations for service offerings.
type ServiceOfferingsClient interface {
	Get(ctx context.Context, guid string, opts ...ServiceOfferingGetOption) (*ServiceOffering, error)
	List(ctx context.Context, params *QueryParams, opts ...ServiceOfferingListOption) (*ListResponse[ServiceOffering], error)
	Update(ctx context.Context, guid string, request *ServiceOfferingUpdateRequest) (*ServiceOffering, error)
	Delete(ctx context.Context, guid string, opts ...ServiceOfferingDeleteOption) error
}

// ServicePlansClient defines operations for service plans.
type ServicePlansClient interface {
	Get(ctx context.Context, guid string, opts ...ServicePlanGetOption) (*ServicePlan, error)
	List(ctx context.Context, params *QueryParams, opts ...ServicePlanListOption) (*ListResponse[ServicePlan], error)
	Update(ctx context.Context, guid string, request *ServicePlanUpdateRequest) (*ServicePlan, error)
	Delete(ctx context.Context, guid string) error

	// Visibility
	GetVisibility(ctx context.Context, guid string) (*ServicePlanVisibility, error)
	UpdateVisibility(ctx context.Context, guid string, request *ServicePlanVisibilityUpdateRequest) (*ServicePlanVisibility, error)
	ApplyVisibility(ctx context.Context, guid string, request *ServicePlanVisibilityApplyRequest) (*ServicePlanVisibility, error)
	RemoveOrgFromVisibility(ctx context.Context, guid string, orgGUID string) error
}

// ServiceInstancesClient defines operations for service instances.
type ServiceInstancesClient interface {
	Create(ctx context.Context, request *ServiceInstanceCreateRequest) (any, error) // Returns *ServiceInstance for user-provided, *Job for managed
	Get(ctx context.Context, guid string, opts ...ServiceInstanceGetOption) (*ServiceInstance, error)
	List(ctx context.Context, params *QueryParams, opts ...ServiceInstanceListOption) (*ListResponse[ServiceInstance], error)
	Update(ctx context.Context, guid string, request *ServiceInstanceUpdateRequest) (any, error) // Returns *ServiceInstance for user-provided, *Job for managed
	Delete(ctx context.Context, guid string, opts ...DeleteOption) (*Job, error)

	// Parameters for managed instances
	GetParameters(ctx context.Context, guid string) (*ServiceInstanceParameters, error)

	// Credentials for user-provided instances
	GetCredentials(ctx context.Context, guid string) (*ServiceInstanceCredentials, error)

	// Sharing operations
	ListSharedSpaces(ctx context.Context, guid string) (*ServiceInstanceSharedSpacesRelationships, error)
	ShareWithSpaces(ctx context.Context, guid string, request *ServiceInstanceShareRequest) (*ServiceInstanceSharedSpacesRelationships, error)
	UnshareFromSpace(ctx context.Context, guid string, spaceGUID string) error
}

// ServiceCredentialBindingsClient provides operations for Service Credential Bindings (v3 name for service bindings).
type ServiceCredentialBindingsClient interface {
	Create(ctx context.Context, request *ServiceCredentialBindingCreateRequest) (any, error) // Returns *ServiceCredentialBinding or *Job
	Get(ctx context.Context, guid string, opts ...ServiceCredentialBindingGetOption) (*ServiceCredentialBinding, error)
	List(ctx context.Context, params *QueryParams, opts ...ServiceCredentialBindingListOption) (*ListResponse[ServiceCredentialBinding], error)
	Update(ctx context.Context, guid string, request *ServiceCredentialBindingUpdateRequest) (*ServiceCredentialBinding, error)
	Delete(ctx context.Context, guid string) (*Job, error)
	GetDetails(ctx context.Context, guid string) (*ServiceCredentialBindingDetails, error)
	GetParameters(ctx context.Context, guid string) (*ServiceCredentialBindingParameters, error)
}

// ServiceBindingsClient is an alias for ServiceCredentialBindingsClient for backward compatibility.
type ServiceBindingsClient = ServiceCredentialBindingsClient

// ServiceRouteBindingsClient defines operations for service route bindings.
type ServiceRouteBindingsClient interface {
	Create(ctx context.Context, request *ServiceRouteBindingCreateRequest) (any, error) // Returns *ServiceRouteBinding or *Job
	Get(ctx context.Context, guid string, opts ...ServiceRouteBindingGetOption) (*ServiceRouteBinding, error)
	List(ctx context.Context, params *QueryParams, opts ...ServiceRouteBindingListOption) (*ListResponse[ServiceRouteBinding], error)
	Update(ctx context.Context, guid string, request *ServiceRouteBindingUpdateRequest) (*ServiceRouteBinding, error)
	Delete(ctx context.Context, guid string) (*Job, error)
	GetParameters(ctx context.Context, guid string) (*ServiceRouteBindingParameters, error)
}

// BuildpacksClient provides operations for managing buildpacks.
type BuildpacksClient interface {
	Create(ctx context.Context, request *BuildpackCreateRequest) (*Buildpack, error)
	Get(ctx context.Context, guid string) (*Buildpack, error)
	List(ctx context.Context, params *QueryParams, opts ...BuildpackListOption) (*ListResponse[Buildpack], error)
	Update(ctx context.Context, guid string, request *BuildpackUpdateRequest) (*Buildpack, error)
	Delete(ctx context.Context, guid string) (*Job, error)
	Upload(ctx context.Context, guid string, bits io.Reader) (*Buildpack, error)
}

// Additional client interfaces for other resources...
type BuildsClient interface {
	Create(ctx context.Context, request *BuildCreateRequest) (*Build, error)
	Get(ctx context.Context, guid string) (*Build, error)
	List(ctx context.Context, params *QueryParams, opts ...BuildListOption) (*ListResponse[Build], error)
	ListForApp(ctx context.Context, appGUID string, params *QueryParams) (*ListResponse[Build], error)
	Update(ctx context.Context, guid string, request *BuildUpdateRequest) (*Build, error)
}

type DeploymentsClient interface {
	Create(ctx context.Context, request *DeploymentCreateRequest) (*Deployment, error)
	Get(ctx context.Context, guid string) (*Deployment, error)
	List(ctx context.Context, params *QueryParams, opts ...DeploymentListOption) (*ListResponse[Deployment], error)
	Update(ctx context.Context, guid string, request *DeploymentUpdateRequest) (*Deployment, error)
	Cancel(ctx context.Context, guid string) error
	Continue(ctx context.Context, guid string) error
}

type DropletsClient interface {
	Create(ctx context.Context, request *DropletCreateRequest) (*Droplet, error)
	Get(ctx context.Context, guid string) (*Droplet, error)
	List(ctx context.Context, params *QueryParams, opts ...DropletListOption) (*ListResponse[Droplet], error)
	ListForApp(ctx context.Context, appGUID string, params *QueryParams) (*ListResponse[Droplet], error)
	ListForPackage(ctx context.Context, packageGUID string, params *QueryParams) (*ListResponse[Droplet], error)
	Update(ctx context.Context, guid string, request *DropletUpdateRequest) (*Droplet, error)
	// Delete issues DELETE /v3/droplets/{guid}. CF v3 returns 202 Accepted with a
	// Location header pointing at /v3/jobs/{jobGuid}. The returned Job has its GUID
	// populated from that header; callers use Jobs().Get or Jobs().PollUntilComplete
	// for full async state. Same pattern as Apps().Delete and Roles().Delete.
	Delete(ctx context.Context, guid string) (*Job, error)
	Copy(ctx context.Context, sourceGUID string, request *DropletCopyRequest) (*Droplet, error)
	Download(ctx context.Context, guid string) ([]byte, error)
	Upload(ctx context.Context, guid string, bits []byte) (*Droplet, error)
}

type PackagesClient interface {
	Create(ctx context.Context, request *PackageCreateRequest) (*Package, error)
	Get(ctx context.Context, guid string) (*Package, error)
	List(ctx context.Context, params *QueryParams, opts ...PackageListOption) (*ListResponse[Package], error)
	Update(ctx context.Context, guid string, request *PackageUpdateRequest) (*Package, error)
	// Delete issues DELETE /v3/packages/{guid}. CF v3 returns 202 Accepted with a
	// Location header pointing at /v3/jobs/{jobGuid}. The returned Job has its GUID
	// populated from that header; callers use Jobs().Get or Jobs().PollUntilComplete
	// for full async state. Same pattern as Apps().Delete and Roles().Delete.
	Delete(ctx context.Context, guid string) (*Job, error)
	Upload(ctx context.Context, guid string, zipFile []byte) (*Package, error)
	Download(ctx context.Context, guid string) ([]byte, error)
	Copy(ctx context.Context, sourceGUID string, request *PackageCopyRequest) (*Package, error)
}

type ProcessesClient interface {
	Get(ctx context.Context, guid string, opts ...ProcessGetOption) (*Process, error)
	List(ctx context.Context, params *QueryParams, opts ...ProcessListOption) (*ListResponse[Process], error)
	Update(ctx context.Context, guid string, request *ProcessUpdateRequest) (*Process, error)
	// Scale adjusts instances/memory/disk/log rate for a process. CF v3
	// responds 202 + Location → /v3/jobs/{jobGuid}; the returned Job has
	// its GUID populated from that header. Callers poll via Jobs().Get.
	Scale(ctx context.Context, guid string, request *ProcessScaleRequest) (*Job, error)
	GetStats(ctx context.Context, guid string) (*ProcessStats, error)
	ListInstances(ctx context.Context, guid string) (*ListResponse[ProcessInstance], error)
	TerminateInstance(ctx context.Context, guid string, index int) error
}

type TasksClient interface {
	Create(ctx context.Context, appGUID string, request *TaskCreateRequest) (*Task, error)
	Get(ctx context.Context, guid string) (*Task, error)
	List(ctx context.Context, params *QueryParams, opts ...TaskListOption) (*ListResponse[Task], error)
	Update(ctx context.Context, guid string, request *TaskUpdateRequest) (*Task, error)
	Cancel(ctx context.Context, guid string) (*Task, error)
}

type StacksClient interface {
	Create(ctx context.Context, request *StackCreateRequest) (*Stack, error)
	Get(ctx context.Context, guid string) (*Stack, error)
	List(ctx context.Context, params *QueryParams, opts ...StackListOption) (*ListResponse[Stack], error)
	Update(ctx context.Context, guid string, request *StackUpdateRequest) (*Stack, error)
	Delete(ctx context.Context, guid string) error
	ListApps(ctx context.Context, guid string, params *QueryParams) (*ListResponse[App], error)
}

type UsersClient interface {
	Create(ctx context.Context, request *UserCreateRequest) (*User, error)
	Get(ctx context.Context, guid string) (*User, error)
	List(ctx context.Context, params *QueryParams, opts ...UserListOption) (*ListResponse[User], error)
	Update(ctx context.Context, guid string, request *UserUpdateRequest) (*User, error)
	Delete(ctx context.Context, guid string) (*Job, error)
}

type RolesClient interface {
	Create(ctx context.Context, request *RoleCreateRequest) (*Role, error)
	Get(ctx context.Context, guid string, opts ...RoleGetOption) (*Role, error)
	List(ctx context.Context, params *QueryParams, opts ...RoleListOption) (*ListResponse[Role], error)
	Delete(ctx context.Context, guid string) (*Job, error)
}

type SecurityGroupsClient interface {
	Create(ctx context.Context, request *SecurityGroupCreateRequest) (*SecurityGroup, error)
	Get(ctx context.Context, guid string) (*SecurityGroup, error)
	List(ctx context.Context, params *QueryParams, opts ...SecurityGroupListOption) (*ListResponse[SecurityGroup], error)
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
	List(ctx context.Context, params *QueryParams, opts ...IsolationSegmentListOption) (*ListResponse[IsolationSegment], error)
	Update(ctx context.Context, guid string, request *IsolationSegmentUpdateRequest) (*IsolationSegment, error)
	Delete(ctx context.Context, guid string) error

	// Organization entitlements
	EntitleOrganizations(ctx context.Context, guid string, orgGUIDs []string) (*ToManyRelationship, error)
	RevokeOrganization(ctx context.Context, guid string, orgGUID string) error
	ListOrganizations(ctx context.Context, guid string, params *QueryParams) (*ListResponse[Organization], error)
	ListSpaces(ctx context.Context, guid string, params *QueryParams) (*ListResponse[Space], error)
}

// FeatureFlagsClient provides access to Feature Flags resources.
type FeatureFlagsClient interface {
	Get(ctx context.Context, name string) (*FeatureFlag, error)
	List(ctx context.Context, params *QueryParams) (*ListResponse[FeatureFlag], error)
	Update(ctx context.Context, name string, request *FeatureFlagUpdateRequest) (*FeatureFlag, error)
}

type JobsClient interface {
	Get(ctx context.Context, guid string) (*Job, error)
	PollUntilComplete(ctx context.Context, guid string) (*Job, error)
}

// OrganizationQuotasClient defines operations for organization quotas.
type OrganizationQuotasClient interface {
	Create(ctx context.Context, request *OrganizationQuotaCreateRequest) (*OrganizationQuota, error)
	Get(ctx context.Context, guid string) (*OrganizationQuota, error)
	List(ctx context.Context, params *QueryParams, opts ...OrganizationQuotaListOption) (*ListResponse[OrganizationQuota], error)
	Update(ctx context.Context, guid string, request *OrganizationQuotaUpdateRequest) (*OrganizationQuota, error)
	// Delete issues DELETE /v3/organization_quotas/{guid}. CF v3 returns 202 Accepted
	// with a Location header pointing at /v3/jobs/{jobGuid}. The returned Job has its
	// GUID populated from that header; callers use Jobs().Get or Jobs().PollUntilComplete
	// for full async state. Same pattern as Apps().Delete and Roles().Delete.
	Delete(ctx context.Context, guid string) (*Job, error)
	ApplyToOrganizations(ctx context.Context, quotaGUID string, orgGUIDs []string) (*ToManyRelationship, error)
}

// SpaceQuotasClient defines operations for space quotas.
type SpaceQuotasClient interface {
	Create(ctx context.Context, request *SpaceQuotaV3CreateRequest) (*SpaceQuotaV3, error)
	Get(ctx context.Context, guid string) (*SpaceQuotaV3, error)
	List(ctx context.Context, params *QueryParams, opts ...SpaceQuotaListOption) (*ListResponse[SpaceQuotaV3], error)
	Update(ctx context.Context, guid string, request *SpaceQuotaV3UpdateRequest) (*SpaceQuotaV3, error)
	// Delete issues DELETE /v3/space_quotas/{guid}. CF v3 returns 202 Accepted with a
	// Location header pointing at /v3/jobs/{jobGuid}. The returned Job has its GUID
	// populated from that header; callers use Jobs().Get or Jobs().PollUntilComplete
	// for full async state. Same pattern as Apps().Delete and Roles().Delete.
	Delete(ctx context.Context, guid string) (*Job, error)
	ApplyToSpaces(ctx context.Context, quotaGUID string, spaceGUIDs []string) (*ToManyRelationship, error)
	RemoveFromSpace(ctx context.Context, quotaGUID string, spaceGUID string) error
}

// SidecarsClient defines operations for sidecars.
type SidecarsClient interface {
	Get(ctx context.Context, guid string) (*Sidecar, error)
	Update(ctx context.Context, guid string, request *SidecarUpdateRequest) (*Sidecar, error)
	Delete(ctx context.Context, guid string) error
	ListForProcess(ctx context.Context, processGUID string, params *QueryParams) (*ListResponse[Sidecar], error)
}

// RevisionsClient defines operations for revisions.
type RevisionsClient interface {
	Get(ctx context.Context, guid string) (*Revision, error)
	Update(ctx context.Context, guid string, request *RevisionUpdateRequest) (*Revision, error)
	GetEnvironmentVariables(ctx context.Context, guid string) (map[string]any, error)
	ListForApp(ctx context.Context, appGUID string, params *QueryParams) (*ListResponse[Revision], error)
	GetDeployedForApp(ctx context.Context, appGUID string) (*ListResponse[Revision], error)
}

// EnvironmentVariableGroupsClient defines operations for environment variable groups.
type EnvironmentVariableGroupsClient interface {
	Get(ctx context.Context, name string) (*EnvironmentVariableGroup, error)
	Update(ctx context.Context, name string, envVars map[string]any) (*EnvironmentVariableGroup, error)
}

// AppUsageEventsClient defines operations for app usage events.
type AppUsageEventsClient interface {
	Get(ctx context.Context, guid string) (*AppUsageEvent, error)
	List(ctx context.Context, params *QueryParams, opts ...AppUsageEventListOption) (*ListResponse[AppUsageEvent], error)
	PurgeAndReseed(ctx context.Context) error
}

// ServiceUsageEventsClient defines operations for service usage events.
type ServiceUsageEventsClient interface {
	Get(ctx context.Context, guid string) (*ServiceUsageEvent, error)
	List(ctx context.Context, params *QueryParams, opts ...ServiceUsageEventListOption) (*ListResponse[ServiceUsageEvent], error)
	PurgeAndReseed(ctx context.Context) error
}

// AuditEventsClient defines operations for audit events.
type AuditEventsClient interface {
	Get(ctx context.Context, guid string) (*AuditEvent, error)
	List(ctx context.Context, params *QueryParams, opts ...AuditEventListOption) (*ListResponse[AuditEvent], error)
}

// ResourceMatchesClient defines operations for resource matches.
type ResourceMatchesClient interface {
	Create(ctx context.Context, request *ResourceMatchesRequest) (*ResourceMatches, error)
}

// RoutingClient provides access to the CF Routing API (/routing/v1/).
// The Routing API is a separate microservice from the Cloud Controller (CF API v3),
// but typically shares the same base URL and UAA authentication in most CF deployments.
type RoutingClient interface {
	ListRouterGroups(ctx context.Context) ([]RouterGroup, error)
	GetRouterGroupByType(ctx context.Context, groupType string) (*RouterGroup, error)
}
