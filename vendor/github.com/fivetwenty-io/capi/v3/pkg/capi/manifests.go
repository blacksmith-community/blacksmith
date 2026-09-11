package capi

import (
	"context"
	"time"
)

// ManifestsClient provides manifest management operations.
type ManifestsClient interface {
	// ApplyManifest applies a manifest to a space
	ApplyManifest(ctx context.Context, spaceGUID string, manifest []byte) (*Job, error)

	// GenerateManifest generates a manifest for an app
	GenerateManifest(ctx context.Context, appGUID string) ([]byte, error)

	// CreateManifestDiff creates a diff between current and proposed manifest
	CreateManifestDiff(ctx context.Context, spaceGUID string, manifest []byte) (*ManifestDiff, error)
}

// Manifest represents a Cloud Foundry application manifest.
type Manifest struct {
	Version      int                   `json:"version"      yaml:"version"`
	Applications []ManifestApplication `json:"applications" yaml:"applications"`
}

// ManifestApplication represents an application in a manifest.
type ManifestApplication struct {
	Name                    string   `json:"name"                                 yaml:"name"`
	Path                    string   `json:"path,omitempty"                       yaml:"path,omitempty"`
	Memory                  string   `json:"memory,omitempty"                     yaml:"memory,omitempty"`
	Disk                    string   `json:"disk_quota,omitempty"                 yaml:"disk_quota,omitempty"`
	Instances               *int     `json:"instances,omitempty"                  yaml:"instances,omitempty"`
	Command                 string   `json:"command,omitempty"                    yaml:"command,omitempty"`
	Buildpacks              []string `json:"buildpacks,omitempty"                 yaml:"buildpacks,omitempty"`
	Stack                   string   `json:"stack,omitempty"                      yaml:"stack,omitempty"`
	Timeout                 *int     `json:"timeout,omitempty"                    yaml:"timeout,omitempty"`
	HealthCheckType         string   `json:"health_check_type,omitempty"          yaml:"health-check-type,omitempty"`
	HealthCheckHTTPEndpoint string   `json:"health_check_http_endpoint,omitempty" yaml:"health-check-http-endpoint,omitempty"`
	HealthCheckInterval     *int     `json:"health_check_interval,omitempty"      yaml:"health-check-interval,omitempty"`
	HealthCheckTimeout      *int     `json:"health_check_timeout,omitempty"       yaml:"health-check-timeout,omitempty"`
	// Readiness health checks determine when an app is ready to receive
	// traffic (CF v3 3.223.0 manifest schema). Valid types: http, port,
	// process.
	ReadinessHealthCheckType              string            `json:"readiness_health_check_type,omitempty"               yaml:"readiness-health-check-type,omitempty"`
	ReadinessHealthCheckHTTPEndpoint      string            `json:"readiness_health_check_http_endpoint,omitempty"      yaml:"readiness-health-check-http-endpoint,omitempty"`
	ReadinessHealthCheckInterval          *int              `json:"readiness_health_check_interval,omitempty"           yaml:"readiness-health-check-interval,omitempty"`
	ReadinessHealthCheckInvocationTimeout *int              `json:"readiness_health_check_invocation_timeout,omitempty" yaml:"readiness-health-check-invocation-timeout,omitempty"`
	Env                                   map[string]any    `json:"env,omitempty"                                       yaml:"env,omitempty"`
	Services                              []ManifestService `json:"services,omitempty"                                  yaml:"services,omitempty"`
	Routes                                []ManifestRoute   `json:"routes,omitempty"                                    yaml:"routes,omitempty"`
	RandomRoute                           *bool             `json:"random_route,omitempty"                              yaml:"random-route,omitempty"`
	NoRoute                               *bool             `json:"no_route,omitempty"                                  yaml:"no-route,omitempty"`
	Processes                             []ManifestProcess `json:"processes,omitempty"                                 yaml:"processes,omitempty"`
	Sidecars                              []ManifestSidecar `json:"sidecars,omitempty"                                  yaml:"sidecars,omitempty"`
	Metadata                              *ManifestMetadata `json:"metadata,omitempty"                                  yaml:"metadata,omitempty"`
	DockerImage                           string            `json:"docker,omitempty"                                    yaml:"docker,omitempty"`
	DockerUsername                        string            `json:"docker_username,omitempty"                           yaml:"docker-username,omitempty"`
	LogRateLimit                          string            `json:"log_rate_limit_per_second,omitempty"                 yaml:"log-rate-limit-per-second,omitempty"`
}

// ManifestService represents a service binding in a manifest.
type ManifestService struct {
	Name        string         `json:"name,omitempty"         yaml:"name,omitempty"`
	BindingName string         `json:"binding_name,omitempty" yaml:"binding_name,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"   yaml:"parameters,omitempty"`
}

// ManifestRoute represents a route in a manifest.
type ManifestRoute struct {
	Route    string `json:"route,omitempty"    yaml:"route,omitempty"`
	Protocol string `json:"protocol,omitempty" yaml:"protocol,omitempty"`
}

// ManifestProcess represents a process in a manifest.
type ManifestProcess struct {
	Type                    string `json:"type"                                 yaml:"type"`
	Command                 string `json:"command,omitempty"                    yaml:"command,omitempty"`
	Memory                  string `json:"memory,omitempty"                     yaml:"memory,omitempty"`
	Disk                    string `json:"disk_quota,omitempty"                 yaml:"disk_quota,omitempty"`
	Instances               *int   `json:"instances,omitempty"                  yaml:"instances,omitempty"`
	HealthCheckType         string `json:"health_check_type,omitempty"          yaml:"health-check-type,omitempty"`
	HealthCheckHTTPEndpoint string `json:"health_check_http_endpoint,omitempty" yaml:"health-check-http-endpoint,omitempty"`
	HealthCheckInterval     *int   `json:"health_check_interval,omitempty"      yaml:"health-check-interval,omitempty"`
	HealthCheckTimeout      *int   `json:"health_check_timeout,omitempty"       yaml:"health-check-timeout,omitempty"`
	// Readiness health checks determine when the process is ready to
	// receive traffic (CF v3 3.223.0 manifest schema). Valid types: http,
	// port, process.
	ReadinessHealthCheckType              string `json:"readiness_health_check_type,omitempty"               yaml:"readiness-health-check-type,omitempty"`
	ReadinessHealthCheckHTTPEndpoint      string `json:"readiness_health_check_http_endpoint,omitempty"      yaml:"readiness-health-check-http-endpoint,omitempty"`
	ReadinessHealthCheckInterval          *int   `json:"readiness_health_check_interval,omitempty"           yaml:"readiness-health-check-interval,omitempty"`
	ReadinessHealthCheckInvocationTimeout *int   `json:"readiness_health_check_invocation_timeout,omitempty" yaml:"readiness-health-check-invocation-timeout,omitempty"`
	LogRateLimit                          string `json:"log_rate_limit_per_second,omitempty"                 yaml:"log-rate-limit-per-second,omitempty"`
}

// ManifestSidecar represents a sidecar in a manifest.
type ManifestSidecar struct {
	Name         string   `json:"name"             yaml:"name"`
	Command      string   `json:"command"          yaml:"command"`
	ProcessTypes []string `json:"process_types"    yaml:"process_types"`
	Memory       string   `json:"memory,omitempty" yaml:"memory,omitempty"`
}

// ManifestMetadata represents metadata in a manifest.
type ManifestMetadata struct {
	Labels      map[string]string `json:"labels,omitempty"      yaml:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty" yaml:"annotations,omitempty"`
}

// ManifestDiff represents a diff between manifests.
type ManifestDiff struct {
	Diff []ManifestDiffEntry `json:"diff" yaml:"diff"`
}

// ManifestDiffEntry represents a single diff entry.
type ManifestDiffEntry struct {
	Op    string `json:"op"              yaml:"op"`
	Path  string `json:"path"            yaml:"path"`
	Was   any    `json:"was,omitempty"   yaml:"was,omitempty"`
	Value any    `json:"value,omitempty" yaml:"value,omitempty"`
}

// ManifestDiffResponse represents the API response for manifest diff.
type ManifestDiffResponse struct {
	Diff      []ManifestDiffEntry `json:"diff"`
	CreatedAt time.Time           `json:"created_at"`
	UpdatedAt time.Time           `json:"updated_at"`
	Links     Links               `json:"links"`
}
