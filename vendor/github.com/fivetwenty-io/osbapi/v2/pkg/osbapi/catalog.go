package osbapi

// Catalog represents the service broker catalog response.
type Catalog struct {
	Services []Service `json:"services"`
}

// Service represents a service offering.
type Service struct {
	ID                   string           `json:"id"`
	Name                 string           `json:"name"`
	Description          string           `json:"description"`
	Tags                 []string         `json:"tags,omitempty"`
	Requires             []string         `json:"requires,omitempty"`
	Bindable             bool             `json:"bindable"`
	InstancesRetrievable bool             `json:"instances_retrievable,omitempty"`
	BindingsRetrievable  bool             `json:"bindings_retrievable,omitempty"`
	AllowContextUpdates  bool             `json:"allow_context_updates,omitempty"`
	Metadata             map[string]any   `json:"metadata,omitempty"`
	DashboardClient      *DashboardClient `json:"dashboard_client,omitempty"`
	PlanUpdateable       *bool            `json:"plan_updateable,omitempty"`
	Plans                []Plan           `json:"plans"`
	MaintenanceInfo      *MaintenanceInfo `json:"maintenance_info,omitempty"`
}

// Plan represents a service plan.
type Plan struct {
	ID                     string           `json:"id"`
	Name                   string           `json:"name"`
	Description            string           `json:"description"`
	Metadata               map[string]any   `json:"metadata,omitempty"`
	Free                   *bool            `json:"free,omitempty"`
	Bindable               *bool            `json:"bindable,omitempty"`
	PlanUpdateable         *bool            `json:"plan_updateable,omitempty"`
	Schemas                *Schemas         `json:"schemas,omitempty"`
	MaximumPollingDuration int              `json:"maximum_polling_duration,omitempty"`
	MaintenanceInfo        *MaintenanceInfo `json:"maintenance_info,omitempty"`
}

// Schemas represents the schemas for service instance and binding operations.
type Schemas struct {
	ServiceInstance *ServiceInstanceSchema `json:"service_instance,omitempty"`
	ServiceBinding  *ServiceBindingSchema  `json:"service_binding,omitempty"`
}

// ServiceInstanceSchema represents schemas for service instance operations.
type ServiceInstanceSchema struct {
	Create *InputParametersSchema `json:"create,omitempty"`
	Update *InputParametersSchema `json:"update,omitempty"`
}

// ServiceBindingSchema represents schemas for service binding operations.
type ServiceBindingSchema struct {
	Create *InputParametersSchema `json:"create,omitempty"`
}

// InputParametersSchema represents a JSON schema for input parameters.
type InputParametersSchema struct {
	Parameters map[string]any `json:"parameters,omitempty"`
}

// DashboardClient represents the OAuth2 client for the service dashboard.
type DashboardClient struct {
	ID          string `json:"id"`
	Secret      string `json:"secret"`
	RedirectURI string `json:"redirect_uri"`
}

// MaintenanceInfo represents version information for maintenance operations.
type MaintenanceInfo struct {
	Version     string `json:"version"`
	Description string `json:"description,omitempty"`
}
