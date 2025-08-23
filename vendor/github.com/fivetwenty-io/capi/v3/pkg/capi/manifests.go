package capi

import (
	"context"
	"time"
)

// ManifestsClient provides manifest management operations
type ManifestsClient interface {
	// ApplyManifest applies a manifest to a space
	ApplyManifest(ctx context.Context, spaceGUID string, manifest []byte) (*Job, error)

	// GenerateManifest generates a manifest for an app
	GenerateManifest(ctx context.Context, appGUID string) ([]byte, error)

	// CreateManifestDiff creates a diff between current and proposed manifest
	CreateManifestDiff(ctx context.Context, spaceGUID string, manifest []byte) (*ManifestDiff, error)
}

// Manifest represents a Cloud Foundry application manifest
type Manifest struct {
	Version      int                   `yaml:"version" json:"version"`
	Applications []ManifestApplication `yaml:"applications" json:"applications"`
}

// ManifestApplication represents an application in a manifest
type ManifestApplication struct {
	Name                    string                 `yaml:"name" json:"name"`
	Path                    string                 `yaml:"path,omitempty" json:"path,omitempty"`
	Memory                  string                 `yaml:"memory,omitempty" json:"memory,omitempty"`
	Disk                    string                 `yaml:"disk_quota,omitempty" json:"disk_quota,omitempty"`
	Instances               *int                   `yaml:"instances,omitempty" json:"instances,omitempty"`
	Command                 string                 `yaml:"command,omitempty" json:"command,omitempty"`
	Buildpacks              []string               `yaml:"buildpacks,omitempty" json:"buildpacks,omitempty"`
	Stack                   string                 `yaml:"stack,omitempty" json:"stack,omitempty"`
	Timeout                 *int                   `yaml:"timeout,omitempty" json:"timeout,omitempty"`
	HealthCheckType         string                 `yaml:"health-check-type,omitempty" json:"health_check_type,omitempty"`
	HealthCheckHTTPEndpoint string                 `yaml:"health-check-http-endpoint,omitempty" json:"health_check_http_endpoint,omitempty"`
	HealthCheckInterval     *int                   `yaml:"health-check-interval,omitempty" json:"health_check_interval,omitempty"`
	HealthCheckTimeout      *int                   `yaml:"health-check-timeout,omitempty" json:"health_check_timeout,omitempty"`
	Env                     map[string]interface{} `yaml:"env,omitempty" json:"env,omitempty"`
	Services                []ManifestService      `yaml:"services,omitempty" json:"services,omitempty"`
	Routes                  []ManifestRoute        `yaml:"routes,omitempty" json:"routes,omitempty"`
	RandomRoute             *bool                  `yaml:"random-route,omitempty" json:"random_route,omitempty"`
	NoRoute                 *bool                  `yaml:"no-route,omitempty" json:"no_route,omitempty"`
	Processes               []ManifestProcess      `yaml:"processes,omitempty" json:"processes,omitempty"`
	Sidecars                []ManifestSidecar      `yaml:"sidecars,omitempty" json:"sidecars,omitempty"`
	Metadata                *ManifestMetadata      `yaml:"metadata,omitempty" json:"metadata,omitempty"`
	DockerImage             string                 `yaml:"docker,omitempty" json:"docker,omitempty"`
	DockerUsername          string                 `yaml:"docker-username,omitempty" json:"docker_username,omitempty"`
	LogRateLimit            string                 `yaml:"log-rate-limit-per-second,omitempty" json:"log_rate_limit_per_second,omitempty"`
}

// ManifestService represents a service binding in a manifest
type ManifestService struct {
	Name        string                 `yaml:"name,omitempty" json:"name,omitempty"`
	BindingName string                 `yaml:"binding_name,omitempty" json:"binding_name,omitempty"`
	Parameters  map[string]interface{} `yaml:"parameters,omitempty" json:"parameters,omitempty"`
}

// ManifestRoute represents a route in a manifest
type ManifestRoute struct {
	Route    string `yaml:"route,omitempty" json:"route,omitempty"`
	Protocol string `yaml:"protocol,omitempty" json:"protocol,omitempty"`
}

// ManifestProcess represents a process in a manifest
type ManifestProcess struct {
	Type                    string `yaml:"type" json:"type"`
	Command                 string `yaml:"command,omitempty" json:"command,omitempty"`
	Memory                  string `yaml:"memory,omitempty" json:"memory,omitempty"`
	Disk                    string `yaml:"disk_quota,omitempty" json:"disk_quota,omitempty"`
	Instances               *int   `yaml:"instances,omitempty" json:"instances,omitempty"`
	HealthCheckType         string `yaml:"health-check-type,omitempty" json:"health_check_type,omitempty"`
	HealthCheckHTTPEndpoint string `yaml:"health-check-http-endpoint,omitempty" json:"health_check_http_endpoint,omitempty"`
	HealthCheckInterval     *int   `yaml:"health-check-interval,omitempty" json:"health_check_interval,omitempty"`
	HealthCheckTimeout      *int   `yaml:"health-check-timeout,omitempty" json:"health_check_timeout,omitempty"`
	LogRateLimit            string `yaml:"log-rate-limit-per-second,omitempty" json:"log_rate_limit_per_second,omitempty"`
}

// ManifestSidecar represents a sidecar in a manifest
type ManifestSidecar struct {
	Name         string   `yaml:"name" json:"name"`
	Command      string   `yaml:"command" json:"command"`
	ProcessTypes []string `yaml:"process_types" json:"process_types"`
	Memory       string   `yaml:"memory,omitempty" json:"memory,omitempty"`
}

// ManifestMetadata represents metadata in a manifest
type ManifestMetadata struct {
	Labels      map[string]string `yaml:"labels,omitempty" json:"labels,omitempty"`
	Annotations map[string]string `yaml:"annotations,omitempty" json:"annotations,omitempty"`
}

// ManifestDiff represents a diff between manifests
type ManifestDiff struct {
	Diff []ManifestDiffEntry `json:"diff"`
}

// ManifestDiffEntry represents a single diff entry
type ManifestDiffEntry struct {
	Op    string      `json:"op"`
	Path  string      `json:"path"`
	Was   interface{} `json:"was,omitempty"`
	Value interface{} `json:"value,omitempty"`
}

// ManifestDiffResponse represents the API response for manifest diff
type ManifestDiffResponse struct {
	Diff      []ManifestDiffEntry `json:"diff"`
	CreatedAt time.Time           `json:"created_at"`
	UpdatedAt time.Time           `json:"updated_at"`
	Links     Links               `json:"links"`
}
