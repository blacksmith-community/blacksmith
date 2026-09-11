package capi

import (
	"strconv"
	"strings"
)

// This file defines typed List filter options for the CF v3 collection
// endpoints. Each resource exposes a sealed XListOption interface (declared
// here for filter-only endpoints, or in query_options.go for endpoints that
// also support include/fields) so only options defined in this package may be
// passed to that resource's List method.
//
// The first half covers endpoints whose only typed options are entity and
// enumerated-value filters. The second half ("parity filters") adds the same
// entity/enum filter constructors to the endpoints that already expose
// include/fields options in query_options.go, so every List endpoint offers
// the full typed filter surface.
//
// Cross-cutting parameters (order_by, label_selector, created_ats,
// updated_ats, pagination) are intentionally NOT duplicated here: they are
// expressed through the *QueryParams argument that every List method also
// accepts (QueryParams.WithOrderBy, WithLabelSelector, WithFilter, and the
// package-level WithTimestampFilter helper). The options below cover the
// resource-specific entity and enumerated-value filters where typing
// prevents the most mistakes.

// CF API v3 query parameter names shared by the List filter constructors in
// this file and in query_options.go.
const (
	filterKeyGUIDs                = "guids"
	filterKeyAppGUIDs             = "app_guids"
	filterKeyStates               = "states"
	filterKeySpaceGUIDs           = "space_guids"
	filterKeyOrganizationGUIDs    = "organization_guids"
	filterKeyTypes                = "types"
	filterKeyNames                = "names"
	filterKeyServiceOfferingGUIDs = "service_offering_guids"
	filterKeyServiceInstanceGUIDs = "service_instance_guids"
)

// joinKind comma-joins a slice of string-kinded values (typed enums) into a
// single CF filter value.
func joinKind[T ~string](vals []T) string {
	parts := make([]string, len(vals))
	for i, v := range vals {
		parts[i] = string(v)
	}

	return strings.Join(parts, ",")
}

// joinInts comma-joins a slice of ints into a single CF filter value, used by
// filters such as route ports.
func joinInts(vals []int) string {
	parts := make([]string, len(vals))
	for i, v := range vals {
		parts[i] = strconv.Itoa(v)
	}

	return strings.Join(parts, ",")
}

// ---- builds ----

// BuildListOption configures GET /v3/builds.
type BuildListOption interface {
	QueryOption
	buildList()
}

type buildListScalar struct{ scalarOption }

func (buildListScalar) buildList() {}

// BuildState is a CF v3 build lifecycle state.
type BuildState string

// Valid build states (CF v3).
const (
	BuildStateStaging BuildState = "STAGING"
	BuildStateStaged  BuildState = "STAGED"
	BuildStateFailed  BuildState = "FAILED"
)

// WithBuildGUIDs filters builds by GUID.
func WithBuildGUIDs(guids ...string) BuildListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return buildListScalar{scalarOption{filterKeyGUIDs, strings.Join(guids, ",")}}
}

// WithBuildAppGUIDs filters builds by app GUID.
func WithBuildAppGUIDs(guids ...string) BuildListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return buildListScalar{scalarOption{filterKeyAppGUIDs, strings.Join(guids, ",")}}
}

// WithBuildPackageGUIDs filters builds by package GUID.
func WithBuildPackageGUIDs(guids ...string) BuildListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return buildListScalar{scalarOption{"package_guids", strings.Join(guids, ",")}}
}

// WithBuildStates filters builds by lifecycle state.
func WithBuildStates(states ...BuildState) BuildListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return buildListScalar{scalarOption{filterKeyStates, joinKind(states)}}
}

// ---- droplets ----

// DropletListOption configures GET /v3/droplets.
type DropletListOption interface {
	QueryOption
	dropletList()
}

type dropletListScalar struct{ scalarOption }

func (dropletListScalar) dropletList() {}

// DropletState is a CF v3 droplet lifecycle state.
type DropletState string

// Valid droplet states (CF v3).
const (
	DropletStateAwaitingUpload   DropletState = "AWAITING_UPLOAD"
	DropletStateProcessingUpload DropletState = "PROCESSING_UPLOAD"
	DropletStateCopying          DropletState = "COPYING"
	DropletStateStaging          DropletState = "STAGING"
	DropletStateStaged           DropletState = "STAGED"
	DropletStateFailed           DropletState = "FAILED"
	DropletStateExpired          DropletState = "EXPIRED"
)

// WithDropletGUIDs filters droplets by GUID.
func WithDropletGUIDs(guids ...string) DropletListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return dropletListScalar{scalarOption{filterKeyGUIDs, strings.Join(guids, ",")}}
}

// WithDropletAppGUIDs filters droplets by app GUID.
func WithDropletAppGUIDs(guids ...string) DropletListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return dropletListScalar{scalarOption{filterKeyAppGUIDs, strings.Join(guids, ",")}}
}

// WithDropletPackageGUIDs filters droplets by package GUID.
func WithDropletPackageGUIDs(guids ...string) DropletListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return dropletListScalar{scalarOption{"package_guids", strings.Join(guids, ",")}}
}

// WithDropletSpaceGUIDs filters droplets by space GUID.
func WithDropletSpaceGUIDs(guids ...string) DropletListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return dropletListScalar{scalarOption{filterKeySpaceGUIDs, strings.Join(guids, ",")}}
}

// WithDropletOrganizationGUIDs filters droplets by organization GUID.
func WithDropletOrganizationGUIDs(guids ...string) DropletListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return dropletListScalar{scalarOption{filterKeyOrganizationGUIDs, strings.Join(guids, ",")}}
}

// WithDropletStates filters droplets by lifecycle state.
func WithDropletStates(states ...DropletState) DropletListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return dropletListScalar{scalarOption{filterKeyStates, joinKind(states)}}
}

// WithDropletCurrent restricts results to droplets that are the current
// droplet of an app, combinable with the other droplet filters such as
// WithDropletAppGUIDs (CF API 3.229.0).
func WithDropletCurrent() DropletListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return dropletListScalar{scalarOption{"current", "true"}}
}

// ---- packages ----

// PackageListOption configures GET /v3/packages.
type PackageListOption interface {
	QueryOption
	packageList()
}

type packageListScalar struct{ scalarOption }

func (packageListScalar) packageList() {}

// PackageState is a CF v3 package lifecycle state.
type PackageState string

// Valid package states (CF v3).
const (
	PackageStateAwaitingUpload   PackageState = "AWAITING_UPLOAD"
	PackageStateProcessingUpload PackageState = "PROCESSING_UPLOAD"
	PackageStateCopying          PackageState = "COPYING"
	PackageStateReady            PackageState = "READY"
	PackageStateFailed           PackageState = "FAILED"
	PackageStateExpired          PackageState = "EXPIRED"
)

// PackageType is a CF v3 package type. CF uses lowercase values here.
type PackageType string

// Valid package types (CF v3).
const (
	PackageTypeBits   PackageType = "bits"
	PackageTypeDocker PackageType = "docker"
)

// WithPackageGUIDs filters packages by GUID.
func WithPackageGUIDs(guids ...string) PackageListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return packageListScalar{scalarOption{filterKeyGUIDs, strings.Join(guids, ",")}}
}

// WithPackageAppGUIDs filters packages by app GUID.
func WithPackageAppGUIDs(guids ...string) PackageListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return packageListScalar{scalarOption{filterKeyAppGUIDs, strings.Join(guids, ",")}}
}

// WithPackageSpaceGUIDs filters packages by space GUID.
func WithPackageSpaceGUIDs(guids ...string) PackageListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return packageListScalar{scalarOption{filterKeySpaceGUIDs, strings.Join(guids, ",")}}
}

// WithPackageOrganizationGUIDs filters packages by organization GUID.
func WithPackageOrganizationGUIDs(guids ...string) PackageListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return packageListScalar{scalarOption{filterKeyOrganizationGUIDs, strings.Join(guids, ",")}}
}

// WithPackageStates filters packages by lifecycle state.
func WithPackageStates(states ...PackageState) PackageListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return packageListScalar{scalarOption{filterKeyStates, joinKind(states)}}
}

// WithPackageTypes filters packages by type (bits or docker).
func WithPackageTypes(types ...PackageType) PackageListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return packageListScalar{scalarOption{filterKeyTypes, joinKind(types)}}
}

// ---- tasks ----

// TaskListOption configures GET /v3/tasks.
type TaskListOption interface {
	QueryOption
	taskList()
}

type taskListScalar struct{ scalarOption }

func (taskListScalar) taskList() {}

// TaskState is a CF v3 task state. Note CF has no CANCELED state, only
// CANCELING.
type TaskState string

// Valid task states (CF v3).
const (
	TaskStatePending   TaskState = "PENDING"
	TaskStateRunning   TaskState = "RUNNING"
	TaskStateCanceling TaskState = "CANCELING"
	TaskStateSucceeded TaskState = "SUCCEEDED"
	TaskStateFailed    TaskState = "FAILED"
)

// WithTaskGUIDs filters tasks by GUID.
func WithTaskGUIDs(guids ...string) TaskListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return taskListScalar{scalarOption{filterKeyGUIDs, strings.Join(guids, ",")}}
}

// WithTaskAppGUIDs filters tasks by app GUID.
func WithTaskAppGUIDs(guids ...string) TaskListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return taskListScalar{scalarOption{filterKeyAppGUIDs, strings.Join(guids, ",")}}
}

// WithTaskSpaceGUIDs filters tasks by space GUID.
func WithTaskSpaceGUIDs(guids ...string) TaskListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return taskListScalar{scalarOption{filterKeySpaceGUIDs, strings.Join(guids, ",")}}
}

// WithTaskOrganizationGUIDs filters tasks by organization GUID.
func WithTaskOrganizationGUIDs(guids ...string) TaskListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return taskListScalar{scalarOption{filterKeyOrganizationGUIDs, strings.Join(guids, ",")}}
}

// WithTaskNames filters tasks by name.
func WithTaskNames(names ...string) TaskListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return taskListScalar{scalarOption{filterKeyNames, strings.Join(names, ",")}}
}

// WithTaskStates filters tasks by state.
func WithTaskStates(states ...TaskState) TaskListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return taskListScalar{scalarOption{filterKeyStates, joinKind(states)}}
}

// ---- deployments ----

// DeploymentListOption configures GET /v3/deployments.
type DeploymentListOption interface {
	QueryOption
	deploymentList()
}

type deploymentListScalar struct{ scalarOption }

func (deploymentListScalar) deploymentList() {}

// DeploymentState is a CF v3 deployment state.
type DeploymentState string

// Valid deployment states (CF v3).
const (
	DeploymentStateDeploying DeploymentState = "DEPLOYING"
	DeploymentStatePrepaused DeploymentState = "PREPAUSED"
	DeploymentStatePaused    DeploymentState = "PAUSED"
	DeploymentStateDeployed  DeploymentState = "DEPLOYED"
	DeploymentStateCanceling DeploymentState = "CANCELING"
	DeploymentStateCanceled  DeploymentState = "CANCELED"
)

// DeploymentStatusValue is a CF v3 deployment status.value.
type DeploymentStatusValue string

// Valid deployment status values (CF v3).
const (
	DeploymentStatusValueActive    DeploymentStatusValue = "ACTIVE"
	DeploymentStatusValueFinalized DeploymentStatusValue = "FINALIZED"
)

// DeploymentStatusReason is a CF v3 deployment status.reason.
type DeploymentStatusReason string

// Valid deployment status reasons (CF v3).
const (
	DeploymentStatusReasonDeploying  DeploymentStatusReason = "DEPLOYING"
	DeploymentStatusReasonPaused     DeploymentStatusReason = "PAUSED"
	DeploymentStatusReasonDeployed   DeploymentStatusReason = "DEPLOYED"
	DeploymentStatusReasonCanceled   DeploymentStatusReason = "CANCELED"
	DeploymentStatusReasonCanceling  DeploymentStatusReason = "CANCELING"
	DeploymentStatusReasonSuperseded DeploymentStatusReason = "SUPERSEDED"
)

// WithDeploymentAppGUIDs filters deployments by app GUID.
func WithDeploymentAppGUIDs(guids ...string) DeploymentListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return deploymentListScalar{scalarOption{filterKeyAppGUIDs, strings.Join(guids, ",")}}
}

// WithDeploymentStates filters deployments by state.
func WithDeploymentStates(states ...DeploymentState) DeploymentListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return deploymentListScalar{scalarOption{filterKeyStates, joinKind(states)}}
}

// WithDeploymentStatusValues filters deployments by status value.
func WithDeploymentStatusValues(values ...DeploymentStatusValue) DeploymentListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return deploymentListScalar{scalarOption{"status_values", joinKind(values)}}
}

// WithDeploymentStatusReasons filters deployments by status reason.
func WithDeploymentStatusReasons(reasons ...DeploymentStatusReason) DeploymentListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return deploymentListScalar{scalarOption{"status_reasons", joinKind(reasons)}}
}

// ---- organizations ----

// OrganizationListOption configures GET /v3/organizations.
type OrganizationListOption interface {
	QueryOption
	organizationList()
}

type organizationListScalar struct{ scalarOption }

func (organizationListScalar) organizationList() {}

// WithOrganizationNames filters organizations by name.
func WithOrganizationNames(names ...string) OrganizationListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return organizationListScalar{scalarOption{filterKeyNames, strings.Join(names, ",")}}
}

// WithOrganizationGUIDs filters organizations by GUID.
func WithOrganizationGUIDs(guids ...string) OrganizationListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return organizationListScalar{scalarOption{filterKeyGUIDs, strings.Join(guids, ",")}}
}

// ---- domains ----

// DomainListOption configures GET /v3/domains.
type DomainListOption interface {
	QueryOption
	domainList()
}

type domainListScalar struct{ scalarOption }

func (domainListScalar) domainList() {}

// WithDomainNames filters domains by name.
func WithDomainNames(names ...string) DomainListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return domainListScalar{scalarOption{filterKeyNames, strings.Join(names, ",")}}
}

// WithDomainGUIDs filters domains by GUID.
func WithDomainGUIDs(guids ...string) DomainListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return domainListScalar{scalarOption{filterKeyGUIDs, strings.Join(guids, ",")}}
}

// WithDomainOrganizationGUIDs filters domains by owning organization GUID.
func WithDomainOrganizationGUIDs(guids ...string) DomainListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return domainListScalar{scalarOption{filterKeyOrganizationGUIDs, strings.Join(guids, ",")}}
}

// ---- organization quotas ----

// OrganizationQuotaListOption configures GET /v3/organization_quotas.
type OrganizationQuotaListOption interface {
	QueryOption
	organizationQuotaList()
}

type organizationQuotaListScalar struct{ scalarOption }

func (organizationQuotaListScalar) organizationQuotaList() {}

// WithOrganizationQuotaNames filters organization quotas by name.
func WithOrganizationQuotaNames(names ...string) OrganizationQuotaListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return organizationQuotaListScalar{scalarOption{filterKeyNames, strings.Join(names, ",")}}
}

// WithOrganizationQuotaGUIDs filters organization quotas by GUID.
func WithOrganizationQuotaGUIDs(guids ...string) OrganizationQuotaListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return organizationQuotaListScalar{scalarOption{filterKeyGUIDs, strings.Join(guids, ",")}}
}

// WithOrganizationQuotaOrganizationGUIDs filters organization quotas by
// associated organization GUID.
func WithOrganizationQuotaOrganizationGUIDs(guids ...string) OrganizationQuotaListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return organizationQuotaListScalar{scalarOption{filterKeyOrganizationGUIDs, strings.Join(guids, ",")}}
}

// ---- space quotas ----

// SpaceQuotaListOption configures GET /v3/space_quotas.
type SpaceQuotaListOption interface {
	QueryOption
	spaceQuotaList()
}

type spaceQuotaListScalar struct{ scalarOption }

func (spaceQuotaListScalar) spaceQuotaList() {}

// WithSpaceQuotaNames filters space quotas by name.
func WithSpaceQuotaNames(names ...string) SpaceQuotaListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return spaceQuotaListScalar{scalarOption{filterKeyNames, strings.Join(names, ",")}}
}

// WithSpaceQuotaGUIDs filters space quotas by GUID.
func WithSpaceQuotaGUIDs(guids ...string) SpaceQuotaListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return spaceQuotaListScalar{scalarOption{filterKeyGUIDs, strings.Join(guids, ",")}}
}

// WithSpaceQuotaOrganizationGUIDs filters space quotas by owning
// organization GUID.
func WithSpaceQuotaOrganizationGUIDs(guids ...string) SpaceQuotaListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return spaceQuotaListScalar{scalarOption{filterKeyOrganizationGUIDs, strings.Join(guids, ",")}}
}

// WithSpaceQuotaSpaceGUIDs filters space quotas by associated space GUID.
func WithSpaceQuotaSpaceGUIDs(guids ...string) SpaceQuotaListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return spaceQuotaListScalar{scalarOption{filterKeySpaceGUIDs, strings.Join(guids, ",")}}
}

// ---- security groups ----

// SecurityGroupListOption configures GET /v3/security_groups.
type SecurityGroupListOption interface {
	QueryOption
	securityGroupList()
}

type securityGroupListScalar struct{ scalarOption }

func (securityGroupListScalar) securityGroupList() {}

// WithSecurityGroupGUIDs filters security groups by GUID.
func WithSecurityGroupGUIDs(guids ...string) SecurityGroupListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return securityGroupListScalar{scalarOption{filterKeyGUIDs, strings.Join(guids, ",")}}
}

// WithSecurityGroupNames filters security groups by name.
func WithSecurityGroupNames(names ...string) SecurityGroupListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return securityGroupListScalar{scalarOption{filterKeyNames, strings.Join(names, ",")}}
}

// WithSecurityGroupRunningSpaceGUIDs filters security groups by the spaces
// where they are bound to the running lifecycle.
func WithSecurityGroupRunningSpaceGUIDs(guids ...string) SecurityGroupListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return securityGroupListScalar{scalarOption{"running_space_guids", strings.Join(guids, ",")}}
}

// WithSecurityGroupStagingSpaceGUIDs filters security groups by the spaces
// where they are bound to the staging lifecycle.
func WithSecurityGroupStagingSpaceGUIDs(guids ...string) SecurityGroupListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return securityGroupListScalar{scalarOption{"staging_space_guids", strings.Join(guids, ",")}}
}

// WithSecurityGroupGloballyEnabledRunning filters security groups by whether
// they apply globally to the running lifecycle.
func WithSecurityGroupGloballyEnabledRunning(enabled bool) SecurityGroupListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return securityGroupListScalar{scalarOption{"globally_enabled_running", strconv.FormatBool(enabled)}}
}

// WithSecurityGroupGloballyEnabledStaging filters security groups by whether
// they apply globally to the staging lifecycle.
func WithSecurityGroupGloballyEnabledStaging(enabled bool) SecurityGroupListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return securityGroupListScalar{scalarOption{"globally_enabled_staging", strconv.FormatBool(enabled)}}
}

// ---- isolation segments ----

// IsolationSegmentListOption configures GET /v3/isolation_segments.
type IsolationSegmentListOption interface {
	QueryOption
	isolationSegmentList()
}

type isolationSegmentListScalar struct{ scalarOption }

func (isolationSegmentListScalar) isolationSegmentList() {}

// WithIsolationSegmentGUIDs filters isolation segments by GUID.
func WithIsolationSegmentGUIDs(guids ...string) IsolationSegmentListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return isolationSegmentListScalar{scalarOption{filterKeyGUIDs, strings.Join(guids, ",")}}
}

// WithIsolationSegmentNames filters isolation segments by name.
func WithIsolationSegmentNames(names ...string) IsolationSegmentListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return isolationSegmentListScalar{scalarOption{filterKeyNames, strings.Join(names, ",")}}
}

// WithIsolationSegmentOrganizationGUIDs filters isolation segments by the
// organizations entitled to them.
func WithIsolationSegmentOrganizationGUIDs(guids ...string) IsolationSegmentListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return isolationSegmentListScalar{scalarOption{filterKeyOrganizationGUIDs, strings.Join(guids, ",")}}
}

// ---- service brokers ----

// ServiceBrokerListOption configures GET /v3/service_brokers.
type ServiceBrokerListOption interface {
	QueryOption
	serviceBrokerList()
}

type serviceBrokerListScalar struct{ scalarOption }

func (serviceBrokerListScalar) serviceBrokerList() {}

// WithServiceBrokerGUIDs filters service brokers by GUID.
func WithServiceBrokerGUIDs(guids ...string) ServiceBrokerListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return serviceBrokerListScalar{scalarOption{filterKeyGUIDs, strings.Join(guids, ",")}}
}

// WithServiceBrokerNames filters service brokers by name.
func WithServiceBrokerNames(names ...string) ServiceBrokerListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return serviceBrokerListScalar{scalarOption{filterKeyNames, strings.Join(names, ",")}}
}

// WithServiceBrokerSpaceGUIDs filters service brokers by the space they are
// scoped to (space-scoped brokers).
func WithServiceBrokerSpaceGUIDs(guids ...string) ServiceBrokerListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return serviceBrokerListScalar{scalarOption{filterKeySpaceGUIDs, strings.Join(guids, ",")}}
}

// ---- buildpacks ----

// BuildpackListOption configures GET /v3/buildpacks.
type BuildpackListOption interface {
	QueryOption
	buildpackList()
}

type buildpackListScalar struct{ scalarOption }

func (buildpackListScalar) buildpackList() {}

// BuildpackLifecycle is a CF v3 buildpack lifecycle.
type BuildpackLifecycle string

// Valid buildpack lifecycles (CF v3).
const (
	BuildpackLifecycleBuildpack BuildpackLifecycle = "buildpack"
	BuildpackLifecycleCNB       BuildpackLifecycle = "cnb"
)

// WithBuildpackGUIDs filters buildpacks by GUID.
func WithBuildpackGUIDs(guids ...string) BuildpackListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return buildpackListScalar{scalarOption{filterKeyGUIDs, strings.Join(guids, ",")}}
}

// WithBuildpackNames filters buildpacks by name.
func WithBuildpackNames(names ...string) BuildpackListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return buildpackListScalar{scalarOption{filterKeyNames, strings.Join(names, ",")}}
}

// WithBuildpackStacks filters buildpacks by stack. An empty string matches
// buildpacks with no stack.
func WithBuildpackStacks(stacks ...string) BuildpackListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return buildpackListScalar{scalarOption{"stacks", strings.Join(stacks, ",")}}
}

// WithBuildpackLifecycle filters buildpacks by lifecycle (buildpack or cnb).
func WithBuildpackLifecycle(lifecycle BuildpackLifecycle) BuildpackListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return buildpackListScalar{scalarOption{"lifecycle", string(lifecycle)}}
}

// ---- stacks ----

// StackListOption configures GET /v3/stacks.
type StackListOption interface {
	QueryOption
	stackList()
}

type stackListScalar struct{ scalarOption }

func (stackListScalar) stackList() {}

// WithStackGUIDs filters stacks by GUID.
func WithStackGUIDs(guids ...string) StackListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return stackListScalar{scalarOption{filterKeyGUIDs, strings.Join(guids, ",")}}
}

// WithStackNames filters stacks by name.
func WithStackNames(names ...string) StackListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return stackListScalar{scalarOption{filterKeyNames, strings.Join(names, ",")}}
}

// WithStackDefault filters stacks by whether they are the default stack.
func WithStackDefault(isDefault bool) StackListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return stackListScalar{scalarOption{"default", strconv.FormatBool(isDefault)}}
}

// ---- users ----

// UserListOption configures GET /v3/users.
type UserListOption interface {
	QueryOption
	userList()
}

type userListScalar struct{ scalarOption }

func (userListScalar) userList() {}

// WithUserGUIDs filters users by GUID.
func WithUserGUIDs(guids ...string) UserListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return userListScalar{scalarOption{filterKeyGUIDs, strings.Join(guids, ",")}}
}

// WithUserUsernames filters users by exact username. Mutually exclusive with
// WithUserPartialUsernames per CF.
func WithUserUsernames(usernames ...string) UserListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return userListScalar{scalarOption{"usernames", strings.Join(usernames, ",")}}
}

// WithUserPartialUsernames filters users by partial (substring) username.
// Mutually exclusive with WithUserUsernames per CF.
func WithUserPartialUsernames(partials ...string) UserListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return userListScalar{scalarOption{"partial_usernames", strings.Join(partials, ",")}}
}

// WithUserOrigins filters users by identity-provider origin. CF requires this
// alongside WithUserUsernames or WithUserPartialUsernames.
func WithUserOrigins(origins ...string) UserListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return userListScalar{scalarOption{"origins", strings.Join(origins, ",")}}
}

// ---- audit events ----

// AuditEventListOption configures GET /v3/audit_events.
type AuditEventListOption interface {
	QueryOption
	auditEventList()
}

type auditEventListScalar struct{ scalarOption }

func (auditEventListScalar) auditEventList() {}

// WithAuditEventTypes filters audit events by event type (e.g.
// "audit.app.create").
func WithAuditEventTypes(types ...string) AuditEventListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return auditEventListScalar{scalarOption{filterKeyTypes, strings.Join(types, ",")}}
}

// WithAuditEventTargetGUIDs filters audit events by target GUID.
func WithAuditEventTargetGUIDs(guids ...string) AuditEventListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return auditEventListScalar{scalarOption{"target_guids", strings.Join(guids, ",")}}
}

// WithAuditEventSpaceGUIDs filters audit events by space GUID.
func WithAuditEventSpaceGUIDs(guids ...string) AuditEventListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return auditEventListScalar{scalarOption{filterKeySpaceGUIDs, strings.Join(guids, ",")}}
}

// WithAuditEventOrganizationGUIDs filters audit events by organization GUID.
func WithAuditEventOrganizationGUIDs(guids ...string) AuditEventListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return auditEventListScalar{scalarOption{filterKeyOrganizationGUIDs, strings.Join(guids, ",")}}
}

// ---- app usage events ----

// AppUsageEventListOption configures GET /v3/app_usage_events.
type AppUsageEventListOption interface {
	QueryOption
	appUsageEventList()
}

type appUsageEventListScalar struct{ scalarOption }

func (appUsageEventListScalar) appUsageEventList() {}

// WithAppUsageEventGUIDs filters app usage events by GUID.
func WithAppUsageEventGUIDs(guids ...string) AppUsageEventListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return appUsageEventListScalar{scalarOption{filterKeyGUIDs, strings.Join(guids, ",")}}
}

// WithAppUsageEventAfterGUID returns only events recorded after the event
// with the given GUID. CF accepts a single value here.
func WithAppUsageEventAfterGUID(guid string) AppUsageEventListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return appUsageEventListScalar{scalarOption{"after_guid", guid}}
}

// ---- service usage events ----

// ServiceUsageEventListOption configures GET /v3/service_usage_events.
type ServiceUsageEventListOption interface {
	QueryOption
	serviceUsageEventList()
}

type serviceUsageEventListScalar struct{ scalarOption }

func (serviceUsageEventListScalar) serviceUsageEventList() {}

// ServiceInstanceType is a CF v3 service instance type used to filter service
// usage events.
type ServiceInstanceType string

// Valid service instance types (CF v3).
const (
	ServiceInstanceTypeManaged      ServiceInstanceType = "managed_service_instance"
	ServiceInstanceTypeUserProvided ServiceInstanceType = "user_provided_service_instance"
)

// WithServiceUsageEventGUIDs filters service usage events by GUID.
func WithServiceUsageEventGUIDs(guids ...string) ServiceUsageEventListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return serviceUsageEventListScalar{scalarOption{filterKeyGUIDs, strings.Join(guids, ",")}}
}

// WithServiceUsageEventAfterGUID returns only events recorded after the event
// with the given GUID. CF accepts a single value here.
func WithServiceUsageEventAfterGUID(guid string) ServiceUsageEventListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return serviceUsageEventListScalar{scalarOption{"after_guid", guid}}
}

// WithServiceUsageEventServiceInstanceTypes filters service usage events by
// service instance type.
func WithServiceUsageEventServiceInstanceTypes(types ...ServiceInstanceType) ServiceUsageEventListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return serviceUsageEventListScalar{scalarOption{"service_instance_types", joinKind(types)}}
}

// WithServiceUsageEventServiceOfferingGUIDs filters service usage events by
// service offering GUID.
func WithServiceUsageEventServiceOfferingGUIDs(guids ...string) ServiceUsageEventListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return serviceUsageEventListScalar{scalarOption{filterKeyServiceOfferingGUIDs, strings.Join(guids, ",")}}
}

// ========================================================================
// Parity filters
//
// The constructors below add entity and enumerated-value filters to the
// endpoints whose XListOption interfaces are declared in query_options.go
// alongside their include/fields options. Each filter wrapper seals to the
// same per-resource list method (appList, routeList, ...) so the new options
// compose freely with the existing include options on a single List call.
// ========================================================================

// ---- apps (filters) ----

type appListScalar struct{ scalarOption }

func (appListScalar) appList() {}

// AppLifecycleType is a CF v3 app lifecycle used to filter GET /v3/apps. Note
// this set includes docker, unlike the buildpack lifecycle filter.
type AppLifecycleType string

// Valid app lifecycle types (CF v3).
const (
	AppLifecycleTypeBuildpack AppLifecycleType = "buildpack"
	AppLifecycleTypeCNB       AppLifecycleType = "cnb"
	AppLifecycleTypeDocker    AppLifecycleType = "docker"
)

// WithAppNames filters apps by name.
func WithAppNames(names ...string) AppListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return appListScalar{scalarOption{filterKeyNames, strings.Join(names, ",")}}
}

// WithAppGUIDs filters apps by GUID.
func WithAppGUIDs(guids ...string) AppListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return appListScalar{scalarOption{filterKeyGUIDs, strings.Join(guids, ",")}}
}

// WithAppSpaceGUIDs filters apps by space GUID.
func WithAppSpaceGUIDs(guids ...string) AppListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return appListScalar{scalarOption{filterKeySpaceGUIDs, strings.Join(guids, ",")}}
}

// WithAppOrganizationGUIDs filters apps by organization GUID.
func WithAppOrganizationGUIDs(guids ...string) AppListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return appListScalar{scalarOption{filterKeyOrganizationGUIDs, strings.Join(guids, ",")}}
}

// WithAppStacks filters apps by stack name.
func WithAppStacks(stacks ...string) AppListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return appListScalar{scalarOption{"stacks", strings.Join(stacks, ",")}}
}

// WithAppLifecycleType filters apps by lifecycle type (buildpack, cnb, or
// docker). CF accepts a single value here.
func WithAppLifecycleType(lifecycle AppLifecycleType) AppListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return appListScalar{scalarOption{"lifecycle_type", string(lifecycle)}}
}

// ---- routes (filters) ----

type routeListScalar struct{ scalarOption }

func (routeListScalar) routeList() {}

// WithRouteGUIDs filters routes by GUID.
func WithRouteGUIDs(guids ...string) RouteListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return routeListScalar{scalarOption{filterKeyGUIDs, strings.Join(guids, ",")}}
}

// WithRouteHosts filters routes by host.
func WithRouteHosts(hosts ...string) RouteListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return routeListScalar{scalarOption{"hosts", strings.Join(hosts, ",")}}
}

// WithRoutePaths filters routes by path.
func WithRoutePaths(paths ...string) RouteListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return routeListScalar{scalarOption{"paths", strings.Join(paths, ",")}}
}

// WithRoutePorts filters routes by port.
func WithRoutePorts(ports ...int) RouteListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return routeListScalar{scalarOption{"ports", joinInts(ports)}}
}

// WithRouteDomainGUIDs filters routes by domain GUID.
func WithRouteDomainGUIDs(guids ...string) RouteListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return routeListScalar{scalarOption{"domain_guids", strings.Join(guids, ",")}}
}

// WithRouteSpaceGUIDs filters routes by space GUID.
func WithRouteSpaceGUIDs(guids ...string) RouteListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return routeListScalar{scalarOption{filterKeySpaceGUIDs, strings.Join(guids, ",")}}
}

// WithRouteOrganizationGUIDs filters routes by organization GUID.
func WithRouteOrganizationGUIDs(guids ...string) RouteListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return routeListScalar{scalarOption{filterKeyOrganizationGUIDs, strings.Join(guids, ",")}}
}

// WithRouteServiceInstanceGUIDs filters routes by bound service instance GUID.
func WithRouteServiceInstanceGUIDs(guids ...string) RouteListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return routeListScalar{scalarOption{filterKeyServiceInstanceGUIDs, strings.Join(guids, ",")}}
}

// WithRouteAppGUIDs filters routes by destination app GUID. Accepted by the
// CF v3 source though not listed in the API docs for GET /v3/routes.
func WithRouteAppGUIDs(guids ...string) RouteListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return routeListScalar{scalarOption{filterKeyAppGUIDs, strings.Join(guids, ",")}}
}

// ---- route policies (filters) ----

type routePolicyListScalar struct{ scalarOption }

func (routePolicyListScalar) routePolicyList() {}

// WithRoutePolicyGUIDs filters route policies by GUID.
func WithRoutePolicyGUIDs(guids ...string) RoutePolicyListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return routePolicyListScalar{scalarOption{filterKeyGUIDs, strings.Join(guids, ",")}}
}

// WithRoutePolicyRouteGUIDs filters route policies by route GUID.
func WithRoutePolicyRouteGUIDs(guids ...string) RoutePolicyListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return routePolicyListScalar{scalarOption{"route_guids", strings.Join(guids, ",")}}
}

// WithRoutePolicySpaceGUIDs filters route policies by the route's space GUID.
func WithRoutePolicySpaceGUIDs(guids ...string) RoutePolicyListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return routePolicyListScalar{scalarOption{filterKeySpaceGUIDs, strings.Join(guids, ",")}}
}

// WithRoutePolicySources filters route policies by exact source string
// (e.g. "cf:any", "cf:app:<guid>").
func WithRoutePolicySources(sources ...string) RoutePolicyListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return routePolicyListScalar{scalarOption{"sources", strings.Join(sources, ",")}}
}

// WithRoutePolicySourceGUIDs filters route policies by the GUID portion of
// the source (the app, space, or org GUID).
func WithRoutePolicySourceGUIDs(guids ...string) RoutePolicyListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return routePolicyListScalar{scalarOption{"source_guids", strings.Join(guids, ",")}}
}

// ---- spaces (filters) ----

type spaceListScalar struct{ scalarOption }

func (spaceListScalar) spaceList() {}

// WithSpaceNames filters spaces by name.
func WithSpaceNames(names ...string) SpaceListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return spaceListScalar{scalarOption{filterKeyNames, strings.Join(names, ",")}}
}

// WithSpaceGUIDs filters spaces by GUID.
func WithSpaceGUIDs(guids ...string) SpaceListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return spaceListScalar{scalarOption{filterKeyGUIDs, strings.Join(guids, ",")}}
}

// WithSpaceOrganizationGUIDs filters spaces by owning organization GUID.
func WithSpaceOrganizationGUIDs(guids ...string) SpaceListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return spaceListScalar{scalarOption{filterKeyOrganizationGUIDs, strings.Join(guids, ",")}}
}

// ---- roles (filters) ----

type roleListScalar struct{ scalarOption }

func (roleListScalar) roleList() {}

// RoleType is a CF v3 role type used to filter GET /v3/roles.
type RoleType string

// Valid role types (CF v3).
const (
	RoleTypeOrganizationUser           RoleType = "organization_user"
	RoleTypeOrganizationAuditor        RoleType = "organization_auditor"
	RoleTypeOrganizationManager        RoleType = "organization_manager"
	RoleTypeOrganizationBillingManager RoleType = "organization_billing_manager"
	RoleTypeSpaceAuditor               RoleType = "space_auditor"
	RoleTypeSpaceDeveloper             RoleType = "space_developer"
	RoleTypeSpaceManager               RoleType = "space_manager"
	RoleTypeSpaceSupporter             RoleType = "space_supporter"
)

// WithRoleGUIDs filters roles by GUID.
func WithRoleGUIDs(guids ...string) RoleListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return roleListScalar{scalarOption{filterKeyGUIDs, strings.Join(guids, ",")}}
}

// WithRoleTypes filters roles by role type.
func WithRoleTypes(types ...RoleType) RoleListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return roleListScalar{scalarOption{filterKeyTypes, joinKind(types)}}
}

// WithRoleSpaceGUIDs filters roles by space GUID.
func WithRoleSpaceGUIDs(guids ...string) RoleListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return roleListScalar{scalarOption{filterKeySpaceGUIDs, strings.Join(guids, ",")}}
}

// WithRoleOrganizationGUIDs filters roles by organization GUID.
func WithRoleOrganizationGUIDs(guids ...string) RoleListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return roleListScalar{scalarOption{filterKeyOrganizationGUIDs, strings.Join(guids, ",")}}
}

// WithRoleUserGUIDs filters roles by user GUID.
func WithRoleUserGUIDs(guids ...string) RoleListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return roleListScalar{scalarOption{"user_guids", strings.Join(guids, ",")}}
}

// ---- service instances (filters) ----

type serviceInstanceListScalar struct{ scalarOption }

func (serviceInstanceListScalar) serviceInstanceList() {}

// ServiceInstanceFilterType is a CF v3 service instance type used to filter
// GET /v3/service_instances. Note user-provided uses a hyphen, distinct from
// the underscore-and-suffix form used by ServiceInstanceType for usage events.
type ServiceInstanceFilterType string

// Valid service instance filter types (CF v3).
const (
	ServiceInstanceFilterTypeManaged      ServiceInstanceFilterType = "managed"
	ServiceInstanceFilterTypeUserProvided ServiceInstanceFilterType = "user-provided"
)

// WithServiceInstanceNames filters service instances by name.
func WithServiceInstanceNames(names ...string) ServiceInstanceListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return serviceInstanceListScalar{scalarOption{filterKeyNames, strings.Join(names, ",")}}
}

// WithServiceInstanceGUIDs filters service instances by GUID.
func WithServiceInstanceGUIDs(guids ...string) ServiceInstanceListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return serviceInstanceListScalar{scalarOption{filterKeyGUIDs, strings.Join(guids, ",")}}
}

// WithServiceInstanceSpaceGUIDs filters service instances by space GUID.
func WithServiceInstanceSpaceGUIDs(guids ...string) ServiceInstanceListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return serviceInstanceListScalar{scalarOption{filterKeySpaceGUIDs, strings.Join(guids, ",")}}
}

// WithServiceInstanceOrganizationGUIDs filters service instances by
// organization GUID.
func WithServiceInstanceOrganizationGUIDs(guids ...string) ServiceInstanceListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return serviceInstanceListScalar{scalarOption{filterKeyOrganizationGUIDs, strings.Join(guids, ",")}}
}

// WithServiceInstanceServicePlanGUIDs filters service instances by service
// plan GUID.
func WithServiceInstanceServicePlanGUIDs(guids ...string) ServiceInstanceListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return serviceInstanceListScalar{scalarOption{"service_plan_guids", strings.Join(guids, ",")}}
}

// WithServiceInstanceServicePlanNames filters service instances by service
// plan name.
func WithServiceInstanceServicePlanNames(names ...string) ServiceInstanceListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return serviceInstanceListScalar{scalarOption{"service_plan_names", strings.Join(names, ",")}}
}

// WithServiceInstanceType filters service instances by type (managed or
// user-provided). CF accepts a single value here.
func WithServiceInstanceType(instanceType ServiceInstanceFilterType) ServiceInstanceListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return serviceInstanceListScalar{scalarOption{"type", string(instanceType)}}
}

// ---- service plans (filters) ----

type servicePlanListScalar struct{ scalarOption }

func (servicePlanListScalar) servicePlanList() {}

// WithServicePlanGUIDs filters service plans by GUID.
func WithServicePlanGUIDs(guids ...string) ServicePlanListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return servicePlanListScalar{scalarOption{filterKeyGUIDs, strings.Join(guids, ",")}}
}

// WithServicePlanNames filters service plans by name.
func WithServicePlanNames(names ...string) ServicePlanListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return servicePlanListScalar{scalarOption{filterKeyNames, strings.Join(names, ",")}}
}

// WithServicePlanAvailable filters service plans by availability.
func WithServicePlanAvailable(available bool) ServicePlanListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return servicePlanListScalar{scalarOption{"available", strconv.FormatBool(available)}}
}

// WithServicePlanBrokerCatalogIDs filters service plans by broker catalog ID.
func WithServicePlanBrokerCatalogIDs(ids ...string) ServicePlanListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return servicePlanListScalar{scalarOption{"broker_catalog_ids", strings.Join(ids, ",")}}
}

// WithServicePlanServiceBrokerGUIDs filters service plans by service broker
// GUID.
func WithServicePlanServiceBrokerGUIDs(guids ...string) ServicePlanListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return servicePlanListScalar{scalarOption{"service_broker_guids", strings.Join(guids, ",")}}
}

// WithServicePlanServiceBrokerNames filters service plans by service broker
// name.
func WithServicePlanServiceBrokerNames(names ...string) ServicePlanListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return servicePlanListScalar{scalarOption{"service_broker_names", strings.Join(names, ",")}}
}

// WithServicePlanServiceOfferingGUIDs filters service plans by service
// offering GUID.
func WithServicePlanServiceOfferingGUIDs(guids ...string) ServicePlanListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return servicePlanListScalar{scalarOption{filterKeyServiceOfferingGUIDs, strings.Join(guids, ",")}}
}

// WithServicePlanServiceOfferingNames filters service plans by service
// offering name.
func WithServicePlanServiceOfferingNames(names ...string) ServicePlanListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return servicePlanListScalar{scalarOption{"service_offering_names", strings.Join(names, ",")}}
}

// WithServicePlanServiceInstanceGUIDs filters service plans by service
// instance GUID.
func WithServicePlanServiceInstanceGUIDs(guids ...string) ServicePlanListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return servicePlanListScalar{scalarOption{filterKeyServiceInstanceGUIDs, strings.Join(guids, ",")}}
}

// WithServicePlanSpaceGUIDs filters service plans by space GUID.
func WithServicePlanSpaceGUIDs(guids ...string) ServicePlanListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return servicePlanListScalar{scalarOption{filterKeySpaceGUIDs, strings.Join(guids, ",")}}
}

// WithServicePlanOrganizationGUIDs filters service plans by organization GUID.
func WithServicePlanOrganizationGUIDs(guids ...string) ServicePlanListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return servicePlanListScalar{scalarOption{filterKeyOrganizationGUIDs, strings.Join(guids, ",")}}
}

// ---- service offerings (filters) ----

type serviceOfferingListScalar struct{ scalarOption }

func (serviceOfferingListScalar) serviceOfferingList() {}

// WithServiceOfferingGUIDs filters service offerings by GUID.
func WithServiceOfferingGUIDs(guids ...string) ServiceOfferingListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return serviceOfferingListScalar{scalarOption{filterKeyGUIDs, strings.Join(guids, ",")}}
}

// WithServiceOfferingNames filters service offerings by name.
func WithServiceOfferingNames(names ...string) ServiceOfferingListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return serviceOfferingListScalar{scalarOption{filterKeyNames, strings.Join(names, ",")}}
}

// WithServiceOfferingAvailable filters service offerings by availability.
func WithServiceOfferingAvailable(available bool) ServiceOfferingListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return serviceOfferingListScalar{scalarOption{"available", strconv.FormatBool(available)}}
}

// WithServiceOfferingBrokerCatalogIDs filters service offerings by broker
// catalog ID.
func WithServiceOfferingBrokerCatalogIDs(ids ...string) ServiceOfferingListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return serviceOfferingListScalar{scalarOption{"broker_catalog_ids", strings.Join(ids, ",")}}
}

// WithServiceOfferingServiceBrokerGUIDs filters service offerings by service
// broker GUID.
func WithServiceOfferingServiceBrokerGUIDs(guids ...string) ServiceOfferingListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return serviceOfferingListScalar{scalarOption{"service_broker_guids", strings.Join(guids, ",")}}
}

// WithServiceOfferingServiceBrokerNames filters service offerings by service
// broker name.
func WithServiceOfferingServiceBrokerNames(names ...string) ServiceOfferingListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return serviceOfferingListScalar{scalarOption{"service_broker_names", strings.Join(names, ",")}}
}

// WithServiceOfferingSpaceGUIDs filters service offerings by space GUID.
func WithServiceOfferingSpaceGUIDs(guids ...string) ServiceOfferingListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return serviceOfferingListScalar{scalarOption{filterKeySpaceGUIDs, strings.Join(guids, ",")}}
}

// WithServiceOfferingOrganizationGUIDs filters service offerings by
// organization GUID.
func WithServiceOfferingOrganizationGUIDs(guids ...string) ServiceOfferingListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return serviceOfferingListScalar{scalarOption{filterKeyOrganizationGUIDs, strings.Join(guids, ",")}}
}

// ---- service credential bindings (filters) ----

type scbListScalar struct{ scalarOption }

func (scbListScalar) scbList() {}

// ServiceCredentialBindingType is a CF v3 service credential binding type used
// to filter GET /v3/service_credential_bindings.
type ServiceCredentialBindingType string

// Valid service credential binding types (CF v3).
const (
	ServiceCredentialBindingTypeApp ServiceCredentialBindingType = "app"
	ServiceCredentialBindingTypeKey ServiceCredentialBindingType = "key"
)

// WithServiceCredentialBindingGUIDs filters bindings by GUID.
func WithServiceCredentialBindingGUIDs(guids ...string) ServiceCredentialBindingListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return scbListScalar{scalarOption{filterKeyGUIDs, strings.Join(guids, ",")}}
}

// WithServiceCredentialBindingNames filters bindings by name.
func WithServiceCredentialBindingNames(names ...string) ServiceCredentialBindingListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return scbListScalar{scalarOption{filterKeyNames, strings.Join(names, ",")}}
}

// WithServiceCredentialBindingServiceInstanceGUIDs filters bindings by service
// instance GUID.
func WithServiceCredentialBindingServiceInstanceGUIDs(guids ...string) ServiceCredentialBindingListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return scbListScalar{scalarOption{filterKeyServiceInstanceGUIDs, strings.Join(guids, ",")}}
}

// WithServiceCredentialBindingServiceInstanceNames filters bindings by service
// instance name.
func WithServiceCredentialBindingServiceInstanceNames(names ...string) ServiceCredentialBindingListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return scbListScalar{scalarOption{"service_instance_names", strings.Join(names, ",")}}
}

// WithServiceCredentialBindingServicePlanGUIDs filters bindings by service
// plan GUID.
func WithServiceCredentialBindingServicePlanGUIDs(guids ...string) ServiceCredentialBindingListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return scbListScalar{scalarOption{"service_plan_guids", strings.Join(guids, ",")}}
}

// WithServiceCredentialBindingServicePlanNames filters bindings by service
// plan name.
func WithServiceCredentialBindingServicePlanNames(names ...string) ServiceCredentialBindingListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return scbListScalar{scalarOption{"service_plan_names", strings.Join(names, ",")}}
}

// WithServiceCredentialBindingServiceOfferingGUIDs filters bindings by service
// offering GUID.
func WithServiceCredentialBindingServiceOfferingGUIDs(guids ...string) ServiceCredentialBindingListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return scbListScalar{scalarOption{filterKeyServiceOfferingGUIDs, strings.Join(guids, ",")}}
}

// WithServiceCredentialBindingServiceOfferingNames filters bindings by service
// offering name.
func WithServiceCredentialBindingServiceOfferingNames(names ...string) ServiceCredentialBindingListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return scbListScalar{scalarOption{"service_offering_names", strings.Join(names, ",")}}
}

// WithServiceCredentialBindingAppGUIDs filters bindings by app GUID.
func WithServiceCredentialBindingAppGUIDs(guids ...string) ServiceCredentialBindingListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return scbListScalar{scalarOption{filterKeyAppGUIDs, strings.Join(guids, ",")}}
}

// WithServiceCredentialBindingAppNames filters bindings by app name.
func WithServiceCredentialBindingAppNames(names ...string) ServiceCredentialBindingListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return scbListScalar{scalarOption{"app_names", strings.Join(names, ",")}}
}

// WithServiceCredentialBindingType filters bindings by type (app or key). CF
// accepts a single value here.
func WithServiceCredentialBindingType(bindingType ServiceCredentialBindingType) ServiceCredentialBindingListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return scbListScalar{scalarOption{"type", string(bindingType)}}
}

// ---- service route bindings (filters) ----

type srbListScalar struct{ scalarOption }

func (srbListScalar) srbList() {}

// WithServiceRouteBindingGUIDs filters route bindings by GUID.
func WithServiceRouteBindingGUIDs(guids ...string) ServiceRouteBindingListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return srbListScalar{scalarOption{filterKeyGUIDs, strings.Join(guids, ",")}}
}

// WithServiceRouteBindingServiceInstanceGUIDs filters route bindings by
// service instance GUID.
func WithServiceRouteBindingServiceInstanceGUIDs(guids ...string) ServiceRouteBindingListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return srbListScalar{scalarOption{filterKeyServiceInstanceGUIDs, strings.Join(guids, ",")}}
}

// WithServiceRouteBindingServiceInstanceNames filters route bindings by
// service instance name.
func WithServiceRouteBindingServiceInstanceNames(names ...string) ServiceRouteBindingListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return srbListScalar{scalarOption{"service_instance_names", strings.Join(names, ",")}}
}

// WithServiceRouteBindingRouteGUIDs filters route bindings by route GUID.
func WithServiceRouteBindingRouteGUIDs(guids ...string) ServiceRouteBindingListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return srbListScalar{scalarOption{"route_guids", strings.Join(guids, ",")}}
}

// ---- processes (filters) ----

type processListScalar struct{ scalarOption }

func (processListScalar) processList() {}

// WithProcessGUIDs filters processes by GUID.
func WithProcessGUIDs(guids ...string) ProcessListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return processListScalar{scalarOption{filterKeyGUIDs, strings.Join(guids, ",")}}
}

// WithProcessTypes filters processes by process type (e.g. web, worker). CF
// does not restrict this to a fixed set.
func WithProcessTypes(types ...string) ProcessListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return processListScalar{scalarOption{filterKeyTypes, strings.Join(types, ",")}}
}

// WithProcessAppGUIDs filters processes by app GUID.
func WithProcessAppGUIDs(guids ...string) ProcessListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return processListScalar{scalarOption{filterKeyAppGUIDs, strings.Join(guids, ",")}}
}

// WithProcessSpaceGUIDs filters processes by space GUID.
func WithProcessSpaceGUIDs(guids ...string) ProcessListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return processListScalar{scalarOption{filterKeySpaceGUIDs, strings.Join(guids, ",")}}
}

// WithProcessOrganizationGUIDs filters processes by organization GUID.
func WithProcessOrganizationGUIDs(guids ...string) ProcessListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return processListScalar{scalarOption{filterKeyOrganizationGUIDs, strings.Join(guids, ",")}}
}
