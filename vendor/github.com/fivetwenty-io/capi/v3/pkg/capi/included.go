package capi

import (
	"encoding/json"
	"fmt"
)

// decodeIncluded decodes the named key from the raw included map.
// Missing key returns nil, nil. Decode errors are wrapped with the key name.
// Each slice entry is one raw JSON object (not an array).
func decodeIncluded[R any](raw map[string][]json.RawMessage, key string) ([]R, error) {
	messages, ok := raw[key]
	if !ok {
		return nil, nil
	}

	out := make([]R, 0, len(messages))

	for _, message := range messages {
		var single R

		err := json.Unmarshal(message, &single)
		if err != nil {
			return nil, fmt.Errorf("decoding included %s: %w", key, err)
		}

		out = append(out, single)
	}

	return out, nil
}

// RoleIncludedResources carries the included block of role list responses.
type RoleIncludedResources struct {
	Users         []User         `json:"users,omitempty"         yaml:"users,omitempty"`
	Spaces        []Space        `json:"spaces,omitempty"        yaml:"spaces,omitempty"`
	Organizations []Organization `json:"organizations,omitempty" yaml:"organizations,omitempty"`
}

// RoleIncludedFrom decodes list.Included into typed slices. The returned struct is never nil; a nil list or absent included block yields empty slices.
func RoleIncludedFrom(list *ListResponse[Role]) (*RoleIncludedResources, error) {
	out := &RoleIncludedResources{}
	if list == nil || list.Included == nil {
		return out, nil
	}

	var err error

	out.Users, err = decodeIncluded[User](list.Included, "users")
	if err != nil {
		return nil, err
	}

	out.Spaces, err = decodeIncluded[Space](list.Included, "spaces")
	if err != nil {
		return nil, err
	}

	out.Organizations, err = decodeIncluded[Organization](list.Included, "organizations")
	if err != nil {
		return nil, err
	}

	return out, nil
}

// AppIncludedResources carries the included block of app list responses.
type AppIncludedResources struct {
	Spaces        []Space        `json:"spaces,omitempty"        yaml:"spaces,omitempty"`
	Organizations []Organization `json:"organizations,omitempty" yaml:"organizations,omitempty"`
}

// AppIncludedFrom decodes list.Included into typed slices. The returned struct is never nil; a nil list or absent included block yields empty slices.
func AppIncludedFrom(list *ListResponse[App]) (*AppIncludedResources, error) {
	out := &AppIncludedResources{}
	if list == nil || list.Included == nil {
		return out, nil
	}

	var err error

	out.Spaces, err = decodeIncluded[Space](list.Included, "spaces")
	if err != nil {
		return nil, err
	}

	out.Organizations, err = decodeIncluded[Organization](list.Included, "organizations")
	if err != nil {
		return nil, err
	}

	return out, nil
}

// RouteIncludedResources carries the included block of route list responses.
type RouteIncludedResources struct {
	Domains       []Domain       `json:"domains,omitempty"        yaml:"domains,omitempty"`
	Spaces        []Space        `json:"spaces,omitempty"         yaml:"spaces,omitempty"`
	Organizations []Organization `json:"organizations,omitempty"  yaml:"organizations,omitempty"`
	RoutePolicies []RoutePolicy  `json:"route_policies,omitempty" yaml:"route_policies,omitempty"`
}

// RouteIncludedFrom decodes list.Included into typed slices. The returned struct is never nil; a nil list or absent included block yields empty slices.
func RouteIncludedFrom(list *ListResponse[Route]) (*RouteIncludedResources, error) {
	out := &RouteIncludedResources{}
	if list == nil || list.Included == nil {
		return out, nil
	}

	var err error

	out.Domains, err = decodeIncluded[Domain](list.Included, "domains")
	if err != nil {
		return nil, err
	}

	out.Spaces, err = decodeIncluded[Space](list.Included, "spaces")
	if err != nil {
		return nil, err
	}

	out.Organizations, err = decodeIncluded[Organization](list.Included, "organizations")
	if err != nil {
		return nil, err
	}

	out.RoutePolicies, err = decodeIncluded[RoutePolicy](list.Included, "route_policies")
	if err != nil {
		return nil, err
	}

	return out, nil
}

// RoutePolicyIncludedResources carries the included block of route policy
// list responses. include=route fills Routes; include=source fills Apps,
// Spaces, and Organizations with the resources referenced by each policy's
// source selector.
type RoutePolicyIncludedResources struct {
	Routes        []Route        `json:"routes,omitempty"        yaml:"routes,omitempty"`
	Apps          []App          `json:"apps,omitempty"          yaml:"apps,omitempty"`
	Spaces        []Space        `json:"spaces,omitempty"        yaml:"spaces,omitempty"`
	Organizations []Organization `json:"organizations,omitempty" yaml:"organizations,omitempty"`
}

// RoutePolicyIncludedFrom decodes list.Included into typed slices. The returned struct is never nil; a nil list or absent included block yields empty slices.
func RoutePolicyIncludedFrom(list *ListResponse[RoutePolicy]) (*RoutePolicyIncludedResources, error) {
	out := &RoutePolicyIncludedResources{}
	if list == nil || list.Included == nil {
		return out, nil
	}

	var err error

	out.Routes, err = decodeIncluded[Route](list.Included, "routes")
	if err != nil {
		return nil, err
	}

	out.Apps, err = decodeIncluded[App](list.Included, "apps")
	if err != nil {
		return nil, err
	}

	out.Spaces, err = decodeIncluded[Space](list.Included, "spaces")
	if err != nil {
		return nil, err
	}

	out.Organizations, err = decodeIncluded[Organization](list.Included, "organizations")
	if err != nil {
		return nil, err
	}

	return out, nil
}

// SpaceIncludedFrom decodes list.Included into typed slices. The returned struct is never nil; a nil list or absent included block yields empty slices.
// Populates the existing SpaceIncludedResources struct (defined in spaces.go).
func SpaceIncludedFrom(list *ListResponse[Space]) (*SpaceIncludedResources, error) {
	out := &SpaceIncludedResources{}
	if list == nil || list.Included == nil {
		return out, nil
	}

	var err error

	out.Organizations, err = decodeIncluded[Organization](list.Included, "organizations")
	if err != nil {
		return nil, err
	}

	out.Spaces, err = decodeIncluded[Space](list.Included, "spaces")
	if err != nil {
		return nil, err
	}

	return out, nil
}

// ServiceCredentialBindingIncludedResources carries the included block of
// service credential binding list responses.
type ServiceCredentialBindingIncludedResources struct {
	Apps             []App             `json:"apps,omitempty"              yaml:"apps,omitempty"`
	ServiceInstances []ServiceInstance `json:"service_instances,omitempty" yaml:"service_instances,omitempty"`
}

// ServiceCredentialBindingIncludedFrom decodes list.Included into typed slices. The returned struct is never nil; a nil list or absent included block yields empty slices.
func ServiceCredentialBindingIncludedFrom(list *ListResponse[ServiceCredentialBinding]) (*ServiceCredentialBindingIncludedResources, error) {
	out := &ServiceCredentialBindingIncludedResources{}
	if list == nil || list.Included == nil {
		return out, nil
	}

	var err error

	out.Apps, err = decodeIncluded[App](list.Included, "apps")
	if err != nil {
		return nil, err
	}

	out.ServiceInstances, err = decodeIncluded[ServiceInstance](list.Included, "service_instances")
	if err != nil {
		return nil, err
	}

	return out, nil
}

// ServicePlanIncludedResources carries the included block of service plan list responses.
type ServicePlanIncludedResources struct {
	Spaces           []Space           `json:"spaces,omitempty"            yaml:"spaces,omitempty"`
	Organizations    []Organization    `json:"organizations,omitempty"     yaml:"organizations,omitempty"`
	ServiceOfferings []ServiceOffering `json:"service_offerings,omitempty" yaml:"service_offerings,omitempty"`
	ServiceBrokers   []ServiceBroker   `json:"service_brokers,omitempty"   yaml:"service_brokers,omitempty"`
}

// ServicePlanIncludedFrom decodes list.Included into typed slices. The returned struct is never nil; a nil list or absent included block yields empty slices.
func ServicePlanIncludedFrom(list *ListResponse[ServicePlan]) (*ServicePlanIncludedResources, error) {
	out := &ServicePlanIncludedResources{}
	if list == nil || list.Included == nil {
		return out, nil
	}

	var err error

	out.Spaces, err = decodeIncluded[Space](list.Included, "spaces")
	if err != nil {
		return nil, err
	}

	out.Organizations, err = decodeIncluded[Organization](list.Included, "organizations")
	if err != nil {
		return nil, err
	}

	out.ServiceOfferings, err = decodeIncluded[ServiceOffering](list.Included, "service_offerings")
	if err != nil {
		return nil, err
	}

	out.ServiceBrokers, err = decodeIncluded[ServiceBroker](list.Included, "service_brokers")
	if err != nil {
		return nil, err
	}

	return out, nil
}

// ServiceRouteBindingIncludedResources carries the included block of
// service route binding list responses.
type ServiceRouteBindingIncludedResources struct {
	Routes           []Route           `json:"routes,omitempty"            yaml:"routes,omitempty"`
	ServiceInstances []ServiceInstance `json:"service_instances,omitempty" yaml:"service_instances,omitempty"`
}

// ServiceRouteBindingIncludedFrom decodes list.Included into typed slices. The returned struct is never nil; a nil list or absent included block yields empty slices.
func ServiceRouteBindingIncludedFrom(list *ListResponse[ServiceRouteBinding]) (*ServiceRouteBindingIncludedResources, error) {
	out := &ServiceRouteBindingIncludedResources{}
	if list == nil || list.Included == nil {
		return out, nil
	}

	var err error

	out.Routes, err = decodeIncluded[Route](list.Included, "routes")
	if err != nil {
		return nil, err
	}

	out.ServiceInstances, err = decodeIncluded[ServiceInstance](list.Included, "service_instances")
	if err != nil {
		return nil, err
	}

	return out, nil
}

// ServiceInstanceIncludedResources carries the included block of
// service instance list responses.
type ServiceInstanceIncludedResources struct {
	Spaces           []Space           `json:"spaces,omitempty"            yaml:"spaces,omitempty"`
	Organizations    []Organization    `json:"organizations,omitempty"     yaml:"organizations,omitempty"`
	ServicePlans     []ServicePlan     `json:"service_plans,omitempty"     yaml:"service_plans,omitempty"`
	ServiceOfferings []ServiceOffering `json:"service_offerings,omitempty" yaml:"service_offerings,omitempty"`
	ServiceBrokers   []ServiceBroker   `json:"service_brokers,omitempty"   yaml:"service_brokers,omitempty"`
}

// ServiceInstanceIncludedFrom decodes list.Included into typed slices. The returned struct is never nil; a nil list or absent included block yields empty slices.
func ServiceInstanceIncludedFrom(list *ListResponse[ServiceInstance]) (*ServiceInstanceIncludedResources, error) {
	out := &ServiceInstanceIncludedResources{}
	if list == nil || list.Included == nil {
		return out, nil
	}

	var err error

	out.Spaces, err = decodeIncluded[Space](list.Included, "spaces")
	if err != nil {
		return nil, err
	}

	out.Organizations, err = decodeIncluded[Organization](list.Included, "organizations")
	if err != nil {
		return nil, err
	}

	out.ServicePlans, err = decodeIncluded[ServicePlan](list.Included, "service_plans")
	if err != nil {
		return nil, err
	}

	out.ServiceOfferings, err = decodeIncluded[ServiceOffering](list.Included, "service_offerings")
	if err != nil {
		return nil, err
	}

	out.ServiceBrokers, err = decodeIncluded[ServiceBroker](list.Included, "service_brokers")
	if err != nil {
		return nil, err
	}

	return out, nil
}

// ServiceOfferingIncludedResources carries the included block of
// service offering list responses.
type ServiceOfferingIncludedResources struct {
	ServiceBrokers []ServiceBroker `json:"service_brokers,omitempty" yaml:"service_brokers,omitempty"`
}

// ServiceOfferingIncludedFrom decodes list.Included into typed slices. The returned struct is never nil; a nil list or absent included block yields empty slices.
func ServiceOfferingIncludedFrom(list *ListResponse[ServiceOffering]) (*ServiceOfferingIncludedResources, error) {
	out := &ServiceOfferingIncludedResources{}
	if list == nil || list.Included == nil {
		return out, nil
	}

	var err error

	out.ServiceBrokers, err = decodeIncluded[ServiceBroker](list.Included, "service_brokers")
	if err != nil {
		return nil, err
	}

	return out, nil
}
