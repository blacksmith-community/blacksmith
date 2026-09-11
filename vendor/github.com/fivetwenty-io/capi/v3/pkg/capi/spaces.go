package capi

import (
	"time"
)

// SpaceIncludedResources represents included resources in a space response.
type SpaceIncludedResources struct {
	Organizations []Organization `json:"organizations,omitempty" yaml:"organizations,omitempty"`
	Spaces        []Space        `json:"spaces,omitempty"        yaml:"spaces,omitempty"`
}

// SpaceQuota represents a space quota returned by legacy (pre-v3) quota endpoints.
// For the v3 API, use SpaceQuotaV3 instead.
type SpaceQuota struct {
	Resource

	Name          string         `json:"name"`
	Apps          *AppsQuota     `json:"apps,omitempty"`
	Services      *ServicesQuota `json:"services,omitempty"`
	Routes        *RoutesQuota   `json:"routes,omitempty"`
	Relationships map[string]any `json:"relationships,omitempty"`
	Links         Links          `json:"links,omitempty"`
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
}

// AppsQuota represents app quota limits.
type AppsQuota struct {
	TotalMemoryInMB      *int `json:"total_memory_in_mb,omitempty"`
	PerProcessMemoryInMB *int `json:"per_process_memory_in_mb,omitempty"`
	TotalInstances       *int `json:"total_instances,omitempty"`
	PerAppTasks          *int `json:"per_app_tasks,omitempty"`
}

// ServicesQuota represents service quota limits.
type ServicesQuota struct {
	PaidServicesAllowed   *bool `json:"paid_services_allowed,omitempty"`
	TotalServiceInstances *int  `json:"total_service_instances,omitempty"`
	TotalServiceKeys      *int  `json:"total_service_keys,omitempty"`
}

// RoutesQuota represents route quota limits.
type RoutesQuota struct {
	TotalRoutes        *int `json:"total_routes,omitempty"`
	TotalReservedPorts *int `json:"total_reserved_ports,omitempty"`
}
