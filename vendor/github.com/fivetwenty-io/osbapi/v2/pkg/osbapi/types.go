package osbapi

// ProvisionRequest represents a service instance provisioning request.
type ProvisionRequest struct {
	ServiceID        string           `json:"service_id"`
	PlanID           string           `json:"plan_id"`
	Context          map[string]any   `json:"context,omitempty"`
	OrganizationGUID string           `json:"organization_guid"`
	SpaceGUID        string           `json:"space_guid"`
	Parameters       map[string]any   `json:"parameters,omitempty"`
	MaintenanceInfo  *MaintenanceInfo `json:"maintenance_info,omitempty"`
}

// ProvisionResponse represents the response to a provisioning request.
type ProvisionResponse struct {
	DashboardURL string            `json:"dashboard_url,omitempty"`
	Operation    string            `json:"operation,omitempty"`
	Metadata     *InstanceMetadata `json:"metadata,omitempty"`
}

// InstanceMetadata represents metadata returned with instance operations.
type InstanceMetadata struct {
	Labels     map[string]string `json:"labels,omitempty"`
	Attributes map[string]string `json:"attributes,omitempty"`
}

// UpdateRequest represents a service instance update request.
type UpdateRequest struct {
	Context         map[string]any   `json:"context,omitempty"`
	ServiceID       string           `json:"service_id"`
	PlanID          string           `json:"plan_id,omitempty"`
	Parameters      map[string]any   `json:"parameters,omitempty"`
	PreviousValues  *PreviousValues  `json:"previous_values,omitempty"`
	MaintenanceInfo *MaintenanceInfo `json:"maintenance_info,omitempty"`
}

// UpdateResponse represents the response to an update request.
type UpdateResponse struct {
	DashboardURL string            `json:"dashboard_url,omitempty"`
	Operation    string            `json:"operation,omitempty"`
	Metadata     *InstanceMetadata `json:"metadata,omitempty"`
}

// PreviousValues represents the values of an instance before an update.
type PreviousValues struct {
	ServiceID       string           `json:"service_id,omitempty"`
	PlanID          string           `json:"plan_id,omitempty"`
	OrganizationID  string           `json:"organization_id,omitempty"`
	SpaceID         string           `json:"space_id,omitempty"`
	MaintenanceInfo *MaintenanceInfo `json:"maintenance_info,omitempty"`
}

// DeprovisionResponse represents the response to a deprovision request.
type DeprovisionResponse struct {
	Operation string `json:"operation,omitempty"`
}

// FetchInstanceResponse represents the response when fetching an instance.
type FetchInstanceResponse struct {
	ServiceID    string            `json:"service_id,omitempty"`
	PlanID       string            `json:"plan_id,omitempty"`
	DashboardURL string            `json:"dashboard_url,omitempty"`
	Parameters   map[string]any    `json:"parameters,omitempty"`
	Metadata     *InstanceMetadata `json:"metadata,omitempty"`
}
