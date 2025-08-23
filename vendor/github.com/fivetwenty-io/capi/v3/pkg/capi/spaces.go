package capi

import (
	"time"
)

// SpaceWithIncludes represents a space with included resources
type SpaceWithIncludes struct {
	Space
	Included *SpaceIncludedResources `json:"included,omitempty"`
}

// SpaceIncludedResources represents included resources in a space response
type SpaceIncludedResources struct {
	Organizations []Organization `json:"organizations,omitempty"`
	Spaces        []Space        `json:"spaces,omitempty"`
}

// SpaceQuota represents a space quota
type SpaceQuota struct {
	Resource
	Name          string                 `json:"name"`
	Apps          *AppsQuota             `json:"apps"`
	Services      *ServicesQuota         `json:"services"`
	Routes        *RoutesQuota           `json:"routes"`
	Relationships map[string]interface{} `json:"relationships,omitempty"`
	Links         Links                  `json:"links,omitempty"`
	CreatedAt     time.Time              `json:"created_at"`
	UpdatedAt     time.Time              `json:"updated_at"`
}

// AppsQuota represents app quota limits
type AppsQuota struct {
	TotalMemoryInMB      *int `json:"total_memory_in_mb"`
	PerProcessMemoryInMB *int `json:"per_process_memory_in_mb"`
	TotalInstances       *int `json:"total_instances"`
	PerAppTasks          *int `json:"per_app_tasks"`
}

// ServicesQuota represents service quota limits
type ServicesQuota struct {
	PaidServicesAllowed   *bool `json:"paid_services_allowed"`
	TotalServiceInstances *int  `json:"total_service_instances"`
	TotalServiceKeys      *int  `json:"total_service_keys"`
}

// RoutesQuota represents route quota limits
type RoutesQuota struct {
	TotalRoutes        *int `json:"total_routes"`
	TotalReservedPorts *int `json:"total_reserved_ports"`
}
