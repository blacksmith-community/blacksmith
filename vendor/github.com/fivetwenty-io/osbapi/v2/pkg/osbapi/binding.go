package osbapi

// BindRequest represents a service binding creation request.
type BindRequest struct {
	Context      map[string]any `json:"context,omitempty"`
	ServiceID    string         `json:"service_id"`
	PlanID       string         `json:"plan_id"`
	AppGUID      string         `json:"app_guid,omitempty"`
	BindResource *BindResource  `json:"bind_resource,omitempty"`
	Parameters   map[string]any `json:"parameters,omitempty"`
}

// BindResource represents the resource being bound to.
type BindResource struct {
	AppGUID     string `json:"app_guid,omitempty"`
	Route       string `json:"route,omitempty"`
	BackupAgent bool   `json:"backup_agent,omitempty"`
}

// BindResponse represents the response to a bind request.
type BindResponse struct {
	Credentials     map[string]any   `json:"credentials,omitempty"`
	SyslogDrainURL  string           `json:"syslog_drain_url,omitempty"`
	RouteServiceURL string           `json:"route_service_url,omitempty"`
	VolumeMounts    []VolumeMount    `json:"volume_mounts,omitempty"`
	Endpoints       []Endpoint       `json:"endpoints,omitempty"`
	Operation       string           `json:"operation,omitempty"`
	Metadata        *BindingMetadata `json:"metadata,omitempty"`
}

// BindingMetadata represents metadata returned with binding operations.
type BindingMetadata struct {
	ExpiresAt   string            `json:"expires_at,omitempty"`
	RenewBefore string            `json:"renew_before,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
}

// VolumeMount represents a volume mount specification.
type VolumeMount struct {
	Driver       string            `json:"driver"`
	ContainerDir string            `json:"container_dir"`
	Mode         string            `json:"mode"`
	DeviceType   string            `json:"device_type"`
	Device       VolumeMountDevice `json:"device"`
}

// VolumeMountDevice represents the device configuration for a volume mount.
type VolumeMountDevice struct {
	VolumeID    string         `json:"volume_id"`
	MountConfig map[string]any `json:"mount_config,omitempty"`
}

// Endpoint represents a service endpoint.
type Endpoint struct {
	Host     string   `json:"host"`
	Ports    []string `json:"ports"`
	Protocol string   `json:"protocol,omitempty"`
}

// UnbindRequest holds query parameters for unbind operations.
type UnbindRequest struct {
	ServiceID string `json:"service_id"`
	PlanID    string `json:"plan_id"`
}

// UnbindResponse represents the response to an unbind request.
type UnbindResponse struct {
	Operation string `json:"operation,omitempty"`
}

// FetchBindingRequest holds query parameters for fetching a binding.
type FetchBindingRequest struct {
	ServiceID string `json:"service_id,omitempty"`
	PlanID    string `json:"plan_id,omitempty"`
}

// FetchBindingResponse represents the response when fetching a binding.
type FetchBindingResponse struct {
	Credentials     map[string]any   `json:"credentials,omitempty"`
	SyslogDrainURL  string           `json:"syslog_drain_url,omitempty"`
	RouteServiceURL string           `json:"route_service_url,omitempty"`
	VolumeMounts    []VolumeMount    `json:"volume_mounts,omitempty"`
	Endpoints       []Endpoint       `json:"endpoints,omitempty"`
	Parameters      map[string]any   `json:"parameters,omitempty"`
	Metadata        *BindingMetadata `json:"metadata,omitempty"`
}
