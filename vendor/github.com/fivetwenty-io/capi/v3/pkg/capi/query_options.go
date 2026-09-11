package capi

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

// QueryOption is the common behavior shared by all typed query options.
// The apply method is unexported so each resource's option set is closed:
// only values defined in this package satisfy the per-resource interfaces.
type QueryOption interface {
	applyQuery(v url.Values)
}

// ApplyQueryOptions merges typed options into values and returns it. values
// is mutated in place; when values is nil and options are present a new map
// is allocated and returned (append-style semantics — use the return value).
// Include options append to the comma-joined include parameter, skipping
// duplicates; scalar options overwrite, so a typed option wins over the
// same key set via QueryParams.
func ApplyQueryOptions[O QueryOption](values url.Values, opts []O) url.Values {
	if len(opts) == 0 {
		return values
	}

	if values == nil {
		values = url.Values{}
	}

	for _, o := range opts {
		o.applyQuery(values)
	}

	return values
}

// appendInclude adds value to the comma-joined include parameter,
// skipping values already present.
func appendInclude(values url.Values, value string) {
	current := values.Get("include")
	if current == "" {
		values.Set("include", value)

		return
	}

	for _, existing := range strings.Split(current, ",") {
		if existing == value {
			return
		}
	}

	values.Set("include", current+","+value)
}

// RoleGetOption configures GET /v3/roles/{guid}.
type RoleGetOption interface {
	QueryOption
	roleGet()
}

// RoleListOption configures GET /v3/roles.
type RoleListOption interface {
	QueryOption
	roleList()
}

type roleInclude string

func (roleInclude) roleGet()                  {}
func (roleInclude) roleList()                 {}
func (r roleInclude) applyQuery(v url.Values) { appendInclude(v, string(r)) }

// Valid include values for roles (CF v3 3.222.0).
const (
	RoleIncludeUser         roleInclude = "user"
	RoleIncludeSpace        roleInclude = "space"
	RoleIncludeOrganization roleInclude = "organization"
)

// ---- shared option value kinds ----

// scalarOption sets a single query key, overwriting any prior value.
type scalarOption struct {
	key, value string
}

func (s scalarOption) applyQuery(v url.Values) { v.Set(s.key, s.value) }

// fieldsOption encodes fields[<key>]=f1,f2.
type fieldsOption struct {
	key    string
	fields []string
}

func (f fieldsOption) applyQuery(v url.Values) {
	v.Set("fields["+f.key+"]", strings.Join(f.fields, ","))
}

// ---- apps ----

// AppGetOption configures GET /v3/apps/{guid}.
type AppGetOption interface {
	QueryOption
	appGet()
}

// AppListOption configures GET /v3/apps.
type AppListOption interface {
	QueryOption
	appList()
}

type appInclude string

func (appInclude) appGet()                   {}
func (appInclude) appList()                  {}
func (a appInclude) applyQuery(v url.Values) { appendInclude(v, string(a)) }

// Valid include values for apps (CF v3 3.222.0).
const (
	AppIncludeSpace             appInclude = "space"
	AppIncludeSpaceOrganization appInclude = "space.organization"
)

// ---- routes ----

// RouteGetOption configures GET /v3/routes/{guid}.
type RouteGetOption interface {
	QueryOption
	routeGet()
}

// RouteListOption configures GET /v3/routes.
type RouteListOption interface {
	QueryOption
	routeList()
}

type routeInclude string

func (routeInclude) routeGet()                 {}
func (routeInclude) routeList()                {}
func (r routeInclude) applyQuery(v url.Values) { appendInclude(v, string(r)) }

// Valid include values for routes (CF v3 3.225.0).
const (
	RouteIncludeDomain            routeInclude = "domain"
	RouteIncludeSpace             routeInclude = "space"
	RouteIncludeSpaceOrganization routeInclude = "space.organization"
	// RouteIncludeRoutePolicies includes the route policies attached to
	// each route (CF v3 3.225.0, experimental).
	RouteIncludeRoutePolicies routeInclude = "route_policies"
)

// ---- route policies ----

// RoutePolicyGetOption configures GET /v3/route_policies/{guid}.
type RoutePolicyGetOption interface {
	QueryOption
	routePolicyGet()
}

// RoutePolicyListOption configures GET /v3/route_policies.
type RoutePolicyListOption interface {
	QueryOption
	routePolicyList()
}

type routePolicyInclude string

func (routePolicyInclude) routePolicyGet()           {}
func (routePolicyInclude) routePolicyList()          {}
func (r routePolicyInclude) applyQuery(v url.Values) { appendInclude(v, string(r)) }

// Valid include values for route policies (CF v3 3.225.0).
// RoutePolicyIncludeSource batch-loads the app, space, or organization
// referenced by each policy's source into included.apps/spaces/organizations.
const (
	RoutePolicyIncludeRoute  routePolicyInclude = "route"
	RoutePolicyIncludeSource routePolicyInclude = "source"
)

// RouteDestinationsOption configures GET /v3/routes/{guid}/destinations.
type RouteDestinationsOption interface {
	QueryOption
	routeDestinations()
}

type routeDestinationsScalar struct{ scalarOption }

func (routeDestinationsScalar) routeDestinations() {}

// WithDestinationGUIDs filters destinations by destination GUIDs.
func WithDestinationGUIDs(guids ...string) RouteDestinationsOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return routeDestinationsScalar{scalarOption{filterKeyGUIDs, strings.Join(guids, ",")}}
}

// WithDestinationAppGUIDs filters destinations by app GUIDs.
func WithDestinationAppGUIDs(guids ...string) RouteDestinationsOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return routeDestinationsScalar{scalarOption{filterKeyAppGUIDs, strings.Join(guids, ",")}}
}

// ---- spaces ----

// SpaceGetOption configures GET /v3/spaces/{guid}.
type SpaceGetOption interface {
	QueryOption
	spaceGet()
}

// SpaceListOption configures GET /v3/spaces.
type SpaceListOption interface {
	QueryOption
	spaceList()
}

type spaceInclude string

func (spaceInclude) spaceGet()                 {}
func (spaceInclude) spaceList()                {}
func (s spaceInclude) applyQuery(v url.Values) { appendInclude(v, string(s)) }

// Valid include value for spaces (CF v3 3.222.0): organization only.
const SpaceIncludeOrganization spaceInclude = "organization"

// ---- service credential bindings ----

// ServiceCredentialBindingGetOption configures GET /v3/service_credential_bindings/{guid}.
type ServiceCredentialBindingGetOption interface {
	QueryOption
	scbGet()
}

// ServiceCredentialBindingListOption configures GET /v3/service_credential_bindings.
type ServiceCredentialBindingListOption interface {
	QueryOption
	scbList()
}

type scbInclude string

func (scbInclude) scbGet()                   {}
func (scbInclude) scbList()                  {}
func (s scbInclude) applyQuery(v url.Values) { appendInclude(v, string(s)) }

// Valid include values for service credential bindings (CF v3 3.222.0).
const (
	ServiceCredentialBindingIncludeApp             scbInclude = "app"
	ServiceCredentialBindingIncludeServiceInstance scbInclude = "service_instance"
)

// ---- service plans ----

// ServicePlanGetOption configures GET /v3/service_plans/{guid}.
type ServicePlanGetOption interface {
	QueryOption
	servicePlanGet()
}

// ServicePlanListOption configures GET /v3/service_plans.
type ServicePlanListOption interface {
	QueryOption
	servicePlanList()
}

type servicePlanInclude string

func (servicePlanInclude) servicePlanGet()           {}
func (servicePlanInclude) servicePlanList()          {}
func (s servicePlanInclude) applyQuery(v url.Values) { appendInclude(v, string(s)) }

// Valid include values for service plans (CF v3 3.222.0).
const (
	ServicePlanIncludeSpaceOrganization servicePlanInclude = "space.organization"
	ServicePlanIncludeServiceOffering   servicePlanInclude = "service_offering"
)

// ServicePlanGetListOption is satisfied by options valid on both the
// service plan Get and List endpoints (e.g. fields[] selectors).
type ServicePlanGetListOption interface {
	ServicePlanGetOption
	ServicePlanListOption
}

// ServicePlanFieldsKey names a fields[...] selector for service plans
// (CF v3 fields parameter).
type ServicePlanFieldsKey string

// ServicePlanFieldsServiceOfferingServiceBroker selects fields of the
// service offering's service broker related resource.
const ServicePlanFieldsServiceOfferingServiceBroker ServicePlanFieldsKey = "service_offering.service_broker"

type servicePlanFields struct{ fieldsOption }

func (servicePlanFields) servicePlanGet()  {}
func (servicePlanFields) servicePlanList() {}

// WithServicePlanFields selects fields of a related resource.
func WithServicePlanFields(key ServicePlanFieldsKey, fields ...string) ServicePlanGetListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return servicePlanFields{fieldsOption{string(key), fields}}
}

// ---- service route bindings ----

// ServiceRouteBindingGetOption configures GET /v3/service_route_bindings/{guid}.
type ServiceRouteBindingGetOption interface {
	QueryOption
	srbGet()
}

// ServiceRouteBindingListOption configures GET /v3/service_route_bindings.
type ServiceRouteBindingListOption interface {
	QueryOption
	srbList()
}

type srbInclude string

func (srbInclude) srbGet()                   {}
func (srbInclude) srbList()                  {}
func (s srbInclude) applyQuery(v url.Values) { appendInclude(v, string(s)) }

// Valid include values for service route bindings (CF v3 3.222.0).
const (
	ServiceRouteBindingIncludeRoute           srbInclude = "route"
	ServiceRouteBindingIncludeServiceInstance srbInclude = "service_instance"
)

// ---- processes ----

// ProcessGetOption configures GET /v3/processes/{guid}.
type ProcessGetOption interface {
	QueryOption
	processGet()
}

// ProcessListOption configures GET /v3/processes.
type ProcessListOption interface {
	QueryOption
	processList()
}

type processEmbed string

func (processEmbed) processGet()               {}
func (processEmbed) processList()              {}
func (p processEmbed) applyQuery(v url.Values) { v.Set("embed", string(p)) }

// ProcessEmbedInstances embeds process instance details (embed=process_instances).
const ProcessEmbedInstances processEmbed = "process_instances"

// ---- service instances ----

// ServiceInstanceGetOption configures GET /v3/service_instances/{guid}.
type ServiceInstanceGetOption interface {
	QueryOption
	serviceInstanceGet()
}

// ServiceInstanceListOption configures GET /v3/service_instances.
type ServiceInstanceListOption interface {
	QueryOption
	serviceInstanceList()
}

// ServiceInstanceFieldsKey names a fields[...] selector for service instances
// (CF v3 fields parameter).
type ServiceInstanceFieldsKey string

// Valid fields[] keys for service instances (CF v3 3.222.0).
const (
	ServiceInstanceFieldsSpace                            ServiceInstanceFieldsKey = "space"
	ServiceInstanceFieldsSpaceOrganization                ServiceInstanceFieldsKey = "space.organization"
	ServiceInstanceFieldsServicePlan                      ServiceInstanceFieldsKey = "service_plan"
	ServiceInstanceFieldsServicePlanServiceOffering       ServiceInstanceFieldsKey = "service_plan.service_offering"
	ServiceInstanceFieldsServicePlanServiceOfferingBroker ServiceInstanceFieldsKey = "service_plan.service_offering.service_broker"
)

type serviceInstanceInclude string

func (serviceInstanceInclude) serviceInstanceGet()       {}
func (serviceInstanceInclude) serviceInstanceList()      {}
func (s serviceInstanceInclude) applyQuery(v url.Values) { appendInclude(v, string(s)) }

// Valid include values for service instances (CF v3 3.222.0).
const (
	ServiceInstanceIncludeSpace                            serviceInstanceInclude = "space"
	ServiceInstanceIncludeServicePlan                      serviceInstanceInclude = "service_plan"
	ServiceInstanceIncludeServicePlanServiceOffering       serviceInstanceInclude = "service_plan.service_offering"
	ServiceInstanceIncludeServicePlanServiceOfferingBroker serviceInstanceInclude = "service_plan.service_offering.service_broker"
)

// ServiceInstanceGetListOption is satisfied by options valid on both the
// service instance Get and List endpoints (e.g. fields[] selectors).
type ServiceInstanceGetListOption interface {
	ServiceInstanceGetOption
	ServiceInstanceListOption
}

type serviceInstanceFields struct{ fieldsOption }

func (serviceInstanceFields) serviceInstanceGet()  {}
func (serviceInstanceFields) serviceInstanceList() {}

// WithServiceInstanceFields selects fields of a related resource.
func WithServiceInstanceFields(key ServiceInstanceFieldsKey, fields ...string) ServiceInstanceGetListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return serviceInstanceFields{fieldsOption{string(key), fields}}
}

// ---- service offerings ----

// ServiceOfferingGetOption configures GET /v3/service_offerings/{guid}.
type ServiceOfferingGetOption interface {
	QueryOption
	serviceOfferingGet()
}

// ServiceOfferingListOption configures GET /v3/service_offerings.
type ServiceOfferingListOption interface {
	QueryOption
	serviceOfferingList()
}

// ServiceOfferingDeleteOption configures DELETE /v3/service_offerings/{guid}.
type ServiceOfferingDeleteOption interface {
	QueryOption
	serviceOfferingDelete()
}

// ServiceOfferingFieldsKey names a fields[...] selector for service offerings
// (CF v3 fields parameter).
type ServiceOfferingFieldsKey string

// ServiceOfferingFieldsServiceBroker selects fields of the service broker
// related resource.
const ServiceOfferingFieldsServiceBroker ServiceOfferingFieldsKey = "service_broker"

type serviceOfferingInclude string

func (serviceOfferingInclude) serviceOfferingGet()       {}
func (serviceOfferingInclude) serviceOfferingList()      {}
func (s serviceOfferingInclude) applyQuery(v url.Values) { appendInclude(v, string(s)) }

// Valid include values for service offerings (CF v3 3.222.0).
const ServiceOfferingIncludeServiceBroker serviceOfferingInclude = "service_broker"

// ServiceOfferingGetListOption is satisfied by options valid on both the
// service offering Get and List endpoints (e.g. fields[] selectors).
type ServiceOfferingGetListOption interface {
	ServiceOfferingGetOption
	ServiceOfferingListOption
}

type serviceOfferingFields struct{ fieldsOption }

func (serviceOfferingFields) serviceOfferingGet()  {}
func (serviceOfferingFields) serviceOfferingList() {}

// WithServiceOfferingFields selects fields of a related resource.
func WithServiceOfferingFields(key ServiceOfferingFieldsKey, fields ...string) ServiceOfferingGetListOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	return serviceOfferingFields{fieldsOption{string(key), fields}}
}

type serviceOfferingPurge struct{ scalarOption }

func (serviceOfferingPurge) serviceOfferingDelete() {}

// PurgeServiceOffering deletes the offering and all associated records
// from the database without broker interaction (?purge=true).
var PurgeServiceOffering ServiceOfferingDeleteOption = serviceOfferingPurge{scalarOption{"purge", "true"}} //nolint:gochecknoglobals // sealed-option sentinel; part of the public option API

// ---- timestamp filters ----

// validTimestampOps lists the operators accepted by CF v3 timestamp filters.
var validTimestampOps = map[string]struct{}{ //nolint:gochecknoglobals // package-level lookup table; not a mutable singleton
	"gt": {}, "gte": {}, "lt": {}, "lte": {},
}

// noopOption is returned when a constructor receives invalid input.
// It applies nothing to url.Values.
type noopOption struct{}

func (noopOption) applyQuery(_ url.Values) {}

// timestampFilterOption wraps key+value for a timestamp filter.
type timestampFilterOption struct {
	key, value string
}

func (f timestampFilterOption) applyQuery(v url.Values) { v.Set(f.key, f.value) }

// WithTimestampFilter returns a QueryOption that sets field[op]=<RFC3339>.
// Valid operators: gt, gte, lt, lte (CF v3 timestamp filter syntax).
// Returns a no-op option when op is not one of the four valid values,
// matching the pattern of other constructors in this file that do not panic
// on bad input.
func WithTimestampFilter(field string, operator string, timestamp time.Time) QueryOption { //nolint:ireturn // sealed-option pattern: typed option composed by callers
	if _, ok := validTimestampOps[operator]; !ok {
		// Invalid operator: return no-op option. Callers must use gt, gte, lt, or lte.
		return noopOption{}
	}

	return timestampFilterOption{
		key:   fmt.Sprintf("%s[%s]", field, operator),
		value: timestamp.UTC().Format(time.RFC3339),
	}
}
