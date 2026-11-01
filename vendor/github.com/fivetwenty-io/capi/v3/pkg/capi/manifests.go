package capi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
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
	// Version is the manifest schema version. Cloud Controller accepts only
	// 1 and defaults to it when the key is absent, so the zero value is
	// omitted rather than sent as 0, which the API rejects.
	Version      int                   `json:"version,omitempty" yaml:"version,omitempty"`
	Applications []ManifestApplication `json:"applications"      yaml:"applications"`
}

// ManifestApplication represents an application in a manifest.
type ManifestApplication struct {
	Name string `json:"name" yaml:"name"`
	// Lifecycle is buildpack, cnb, or docker. Cloud Controller defaults to
	// buildpack, or to docker when Docker is set.
	Lifecycle string `json:"lifecycle,omitempty"  yaml:"lifecycle,omitempty"`
	Path      string `json:"path,omitempty"       yaml:"path,omitempty"`
	Memory    string `json:"memory,omitempty"     yaml:"memory,omitempty"`
	Disk      string `json:"disk_quota,omitempty" yaml:"disk_quota,omitempty"`
	Instances *int   `json:"instances,omitempty"  yaml:"instances,omitempty"`
	Command   string `json:"command,omitempty"    yaml:"command,omitempty"`
	// Buildpack is the legacy single-buildpack key.
	//
	// Deprecated: the app manifest reference replaces it with Buildpacks.
	// It is kept so manifests that still use it decode and round-trip.
	Buildpack                    string   `json:"buildpack,omitempty"                       yaml:"buildpack,omitempty"`
	Buildpacks                   []string `json:"buildpacks,omitempty"                      yaml:"buildpacks,omitempty"`
	Stack                        string   `json:"stack,omitempty"                           yaml:"stack,omitempty"`
	Timeout                      *int     `json:"timeout,omitempty"                         yaml:"timeout,omitempty"`
	HealthCheckType              string   `json:"health_check_type,omitempty"               yaml:"health-check-type,omitempty"`
	HealthCheckHTTPEndpoint      string   `json:"health_check_http_endpoint,omitempty"      yaml:"health-check-http-endpoint,omitempty"`
	HealthCheckInterval          *int     `json:"health_check_interval,omitempty"           yaml:"health-check-interval,omitempty"`
	HealthCheckInvocationTimeout *int     `json:"health_check_invocation_timeout,omitempty" yaml:"health-check-invocation-timeout,omitempty"`
	// Deprecated: Cloud Controller accepts health-check-timeout at the app
	// level but does not apply it to any process. Use Timeout.
	HealthCheckTimeout *int `json:"health_check_timeout,omitempty" yaml:"health-check-timeout,omitempty"`
	// Readiness health checks determine when an app is ready to receive
	// traffic (CF v3 3.223.0 manifest schema). Valid types: http, port,
	// process.
	ReadinessHealthCheckType              string            `json:"readiness_health_check_type,omitempty"               yaml:"readiness-health-check-type,omitempty"`
	ReadinessHealthCheckHTTPEndpoint      string            `json:"readiness_health_check_http_endpoint,omitempty"      yaml:"readiness-health-check-http-endpoint,omitempty"`
	ReadinessHealthCheckInterval          *int              `json:"readiness_health_check_interval,omitempty"           yaml:"readiness-health-check-interval,omitempty"`
	ReadinessHealthCheckInvocationTimeout *int              `json:"readiness_health_check_invocation_timeout,omitempty" yaml:"readiness-health-check-invocation-timeout,omitempty"`
	Env                                   map[string]any    `json:"env,omitempty"                                       yaml:"env,omitempty"`
	Features                              map[string]bool   `json:"features,omitempty"                                  yaml:"features,omitempty"`
	Services                              []ManifestService `json:"services,omitempty"                                  yaml:"services,omitempty"`
	Routes                                []ManifestRoute   `json:"routes,omitempty"                                    yaml:"routes,omitempty"`
	RandomRoute                           *bool             `json:"random_route,omitempty"                              yaml:"random-route,omitempty"`
	DefaultRoute                          *bool             `json:"default_route,omitempty"                             yaml:"default-route,omitempty"`
	NoRoute                               *bool             `json:"no_route,omitempty"                                  yaml:"no-route,omitempty"`
	Processes                             []ManifestProcess `json:"processes,omitempty"                                 yaml:"processes,omitempty"`
	Sidecars                              []ManifestSidecar `json:"sidecars,omitempty"                                  yaml:"sidecars,omitempty"`
	Metadata                              *ManifestMetadata `json:"metadata,omitempty"                                  yaml:"metadata,omitempty"`
	// Docker gives the app the docker lifecycle. Cloud Controller ignores
	// the value on apply, and the generate manifest endpoint returns the
	// image and username.
	Docker       *ManifestDocker `json:"docker,omitempty"                    yaml:"docker,omitempty"`
	LogRateLimit string          `json:"log_rate_limit_per_second,omitempty" yaml:"log-rate-limit-per-second,omitempty"`
	// Credentials used to fetch private buildpacks (buildpack lifecycle)
	// or buildpack images (cnb lifecycle), keyed by host (CF v3 3.236.0).
	// The zero value omits the key, which keeps stored credentials. See
	// ManifestCredentials for the other states and for how a YAML null
	// decodes.
	Credentials ManifestCredentials `json:"credentials,omitzero" yaml:"credentials,omitempty"`
	// CNBCredentials is the backward-compatible alias for Credentials.
	// Specifying both keys is rejected by the API.
	CNBCredentials ManifestCredentials `json:"cnb_credentials,omitzero" yaml:"cnb-credentials,omitempty"`
}

// ManifestDocker is the docker key of a manifest application.
type ManifestDocker struct {
	Image    string `json:"image,omitempty"    yaml:"image,omitempty"`
	Username string `json:"username,omitempty" yaml:"username,omitempty"`
}

// ManifestCredential holds one host's credentials. Buildpack-lifecycle
// entries use Username and Password; cnb-lifecycle entries use either
// Username and Password or Token.
type ManifestCredential struct {
	Username string `json:"username,omitempty" yaml:"username,omitempty"`
	Password string `json:"password,omitempty" yaml:"password,omitempty"`
	Token    string `json:"token,omitempty"    yaml:"token,omitempty"`
}

// RedactedManifestCredentials is the placeholder Cloud Controller returns
// in place of stored credentials, for example in the manifest that
// GET /v3/apps/:guid/manifest generates.
const RedactedManifestCredentials = "***"

// ErrManifestCredentialsUnset is returned when the zero ManifestCredentials
// is marshaled. The zero value means "omit the key", which only a field
// tagged omitzero (JSON) or omitempty (YAML) can express; emitting null
// instead would clear the stored credentials.
var ErrManifestCredentialsUnset = errors.New(
	"manifest credentials are unset: omit the key with omitzero or omitempty")

// ManifestCredentials is the value of the manifest credentials and
// cnb-credentials keys. It has four states:
//
//   - The zero value omits the key, which keeps stored credentials. It only
//     marshals inside a field tagged omitzero (JSON) or omitempty (YAML),
//     as ManifestApplication does; marshaling it anywhere else returns
//     ErrManifestCredentialsUnset rather than an accidental null.
//   - A non-nil Hosts map sets the credentials per host. An empty map is
//     sent as {}, which Cloud Controller stores as an empty set of
//     credentials, unlike an omitted key.
//   - Clear sends an explicit null, which clears stored credentials.
//   - Redacted records that the server returned the placeholder "***" for
//     stored credentials. It marshals back to "***" so a generated
//     manifest round-trips, but Cloud Controller rejects the string on
//     apply, because credentials must be an object. Before applying a
//     generated manifest, reset the field to the zero value to keep the
//     stored credentials, or set Hosts to replace them.
//
// Clear wins over Redacted, which wins over Hosts.
//
// Decoding limitation: a YAML decoder never calls a field's unmarshaler for
// a null value, so "credentials: null" (and ~, an empty value, or a null
// reached through an alias or merge key) decodes as the zero value, not as
// Clear, and re-marshals with the key omitted. A manifest decoded from YAML
// therefore cannot carry a clear request through to ApplyManifest; set
// Clear explicitly, or apply the original bytes. JSON decoding does report
// null as Clear.
//
// The methods use the UnmarshalYAML(func(any) error) form, which
// go.yaml.in/yaml/v3, gopkg.in/yaml.v3, and gopkg.in/yaml.v2 all call, so
// decoding behaves the same whichever of them the caller uses.
type ManifestCredentials struct {
	// Hosts maps a host to its credentials.
	Hosts map[string]ManifestCredential `json:"-" yaml:",inline"`
	// Clear marshals the key as an explicit null.
	Clear bool `json:"-" yaml:"-"`
	// Redacted marshals the key as the "***" placeholder.
	Redacted bool `json:"-" yaml:"-"`
}

// ClearManifestCredentials returns the value that clears stored credentials.
func ClearManifestCredentials() ManifestCredentials {
	return ManifestCredentials{Clear: true}
}

// NewManifestCredentials returns a value that sets credentials per host. A
// nil map is treated as an empty one, so the result is never the zero
// value.
func NewManifestCredentials(hosts map[string]ManifestCredential) ManifestCredentials {
	if hosts == nil {
		hosts = map[string]ManifestCredential{}
	}

	return ManifestCredentials{Hosts: hosts}
}

// IsZero reports whether the key should be omitted from a manifest.
func (c ManifestCredentials) IsZero() bool {
	return !c.Clear && !c.Redacted && c.Hosts == nil
}

// MarshalJSON encodes Clear as null, Redacted as "***", and otherwise the
// host map. The zero value returns ErrManifestCredentialsUnset.
func (c ManifestCredentials) MarshalJSON() ([]byte, error) {
	value, err := c.wireValue()
	if err != nil {
		return nil, err
	}

	data, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshaling manifest credentials: %w", err)
	}

	return data, nil
}

// UnmarshalJSON decodes null as Clear, "***" as Redacted, and an object as
// the host map. Any other value is an error.
func (c *ManifestCredentials) UnmarshalJSON(data []byte) error {
	var value any

	err := json.Unmarshal(data, &value)
	if err != nil {
		return fmt.Errorf("unmarshaling manifest credentials: %w", err)
	}

	switch value {
	case nil:
		*c = ManifestCredentials{Clear: true}

		return nil
	case RedactedManifestCredentials:
		*c = ManifestCredentials{Redacted: true}

		return nil
	}

	var hosts map[string]ManifestCredential

	err = json.Unmarshal(data, &hosts)
	if err != nil {
		return fmt.Errorf("unmarshaling manifest credentials: %w", err)
	}

	*c = NewManifestCredentials(hosts)

	return nil
}

// MarshalYAML encodes Clear as null, Redacted as "***", and otherwise the
// host map. The zero value returns ErrManifestCredentialsUnset.
func (c ManifestCredentials) MarshalYAML() (any, error) {
	return c.wireValue()
}

// UnmarshalYAML decodes "***" as Redacted and a mapping as the host map.
// Errors from the decoder come back unwrapped so that it can keep
// aggregating type errors across the rest of the document.
func (c *ManifestCredentials) UnmarshalYAML(unmarshal func(any) error) error {
	var scalar string
	if unmarshal(&scalar) == nil && scalar == RedactedManifestCredentials {
		*c = ManifestCredentials{Redacted: true}

		return nil
	}

	var hosts map[string]ManifestCredential

	err := unmarshal(&hosts)
	if err != nil {
		return err //nolint:wrapcheck // yaml aggregates *yaml.TypeError only when it is returned as is
	}

	*c = NewManifestCredentials(hosts)

	return nil
}

func (c ManifestCredentials) wireValue() (any, error) {
	switch {
	case c.Clear:
		return nil, nil //nolint:nilnil // a nil value is the null that clears credentials
	case c.Redacted:
		return RedactedManifestCredentials, nil
	case c.Hosts != nil:
		return c.Hosts, nil
	default:
		return nil, ErrManifestCredentialsUnset
	}
}

// ManifestService represents a service binding in a manifest. The services
// key takes either a service instance name or an object, and both decode
// into this type. A value with only Name set marshals as the plain name,
// which is the form the generate manifest endpoint returns.
type ManifestService struct {
	Name        string         `json:"name,omitempty"         yaml:"name,omitempty"`
	BindingName string         `json:"binding_name,omitempty" yaml:"binding_name,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"   yaml:"parameters,omitempty"`
}

// manifestServiceFields has the fields of ManifestService without its
// methods, so the methods can encode and decode the object form.
type manifestServiceFields ManifestService

// MarshalJSON encodes a name-only service as a string and any other
// service as an object.
func (s ManifestService) MarshalJSON() ([]byte, error) {
	var value any = manifestServiceFields(s)
	if s.nameOnly() {
		value = s.Name
	}

	data, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshaling manifest service: %w", err)
	}

	return data, nil
}

// UnmarshalJSON decodes a string as the service name and an object field
// by field.
func (s *ManifestService) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && trimmed[0] == '"' {
		err := json.Unmarshal(trimmed, &s.Name)
		if err != nil {
			return fmt.Errorf("unmarshaling manifest service name: %w", err)
		}

		return nil
	}

	fields := manifestServiceFields(*s)

	err := json.Unmarshal(data, &fields)
	if err != nil {
		return fmt.Errorf("unmarshaling manifest service: %w", err)
	}

	*s = ManifestService(fields)

	return nil
}

// MarshalYAML encodes a name-only service as a string and any other
// service as a mapping.
func (s ManifestService) MarshalYAML() (any, error) {
	if s.nameOnly() {
		return s.Name, nil
	}

	return manifestServiceFields(s), nil
}

// UnmarshalYAML decodes a scalar as the service name and a mapping field by
// field. Decoder errors come back unwrapped so that type errors keep
// aggregating.
func (s *ManifestService) UnmarshalYAML(unmarshal func(any) error) error {
	var name string
	if unmarshal(&name) == nil {
		s.Name = name

		return nil
	}

	fields := manifestServiceFields(*s)

	err := unmarshal(&fields)
	if err != nil {
		return err //nolint:wrapcheck // yaml aggregates *yaml.TypeError only when it is returned as is
	}

	*s = ManifestService(fields)

	return nil
}

func (s ManifestService) nameOnly() bool {
	return s.BindingName == "" && len(s.Parameters) == 0
}

// ManifestRoute represents a route in a manifest.
type ManifestRoute struct {
	Route    string `json:"route,omitempty"    yaml:"route,omitempty"`
	Protocol string `json:"protocol,omitempty" yaml:"protocol,omitempty"`
	// Options are per-route options such as loadbalancing, hash_header,
	// and hash_balance. The map keeps values as written, because
	// hash_balance may be a number or a string.
	Options map[string]any `json:"options,omitempty" yaml:"options,omitempty"`
}

// ManifestProcess represents a process in a manifest.
type ManifestProcess struct {
	Type    string `json:"type"              yaml:"type"`
	Command string `json:"command,omitempty" yaml:"command,omitempty"`
	// User is the user the process runs as.
	User      string `json:"user,omitempty"       yaml:"user,omitempty"`
	Memory    string `json:"memory,omitempty"     yaml:"memory,omitempty"`
	Disk      string `json:"disk_quota,omitempty" yaml:"disk_quota,omitempty"`
	Instances *int   `json:"instances,omitempty"  yaml:"instances,omitempty"`
	// Timeout is the time in seconds at which the health check reports
	// failure.
	Timeout                      *int   `json:"timeout,omitempty"                         yaml:"timeout,omitempty"`
	HealthCheckType              string `json:"health_check_type,omitempty"               yaml:"health-check-type,omitempty"`
	HealthCheckHTTPEndpoint      string `json:"health_check_http_endpoint,omitempty"      yaml:"health-check-http-endpoint,omitempty"`
	HealthCheckInterval          *int   `json:"health_check_interval,omitempty"           yaml:"health-check-interval,omitempty"`
	HealthCheckInvocationTimeout *int   `json:"health_check_invocation_timeout,omitempty" yaml:"health-check-invocation-timeout,omitempty"`
	// Deprecated: health-check-timeout is not a process-level manifest key,
	// and Cloud Controller rejects a manifest that sets it. Use Timeout.
	HealthCheckTimeout *int `json:"health_check_timeout,omitempty" yaml:"health-check-timeout,omitempty"`
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
	Name         string                `json:"name"                    yaml:"name"`
	Command      string                `json:"command,omitempty"       yaml:"command,omitempty"`
	ProcessTypes []string              `json:"process_types,omitempty" yaml:"process_types,omitempty"`
	Memory       ManifestSidecarMemory `json:"memory,omitempty"        yaml:"memory,omitempty"`
}

// ManifestSidecarMemory is a sidecar memory limit. The app manifest
// reference types it as an integer number of megabytes, and Cloud
// Controller also accepts a string with a unit such as "800M", which is
// the form the generate manifest endpoint returns. A value made only of
// digits marshals as an integer and any other value as a string, so both
// forms round-trip.
type ManifestSidecarMemory string

// MarshalJSON encodes a digits-only value as a number and any other value
// as a string.
func (m ManifestSidecarMemory) MarshalJSON() ([]byte, error) {
	megabytes, ok := m.megabytes()
	if ok {
		return []byte(strconv.Itoa(megabytes)), nil
	}

	data, err := json.Marshal(string(m))
	if err != nil {
		return nil, fmt.Errorf("marshaling sidecar memory: %w", err)
	}

	return data, nil
}

// UnmarshalJSON accepts a number of megabytes or a string with a unit. A
// null leaves the value unchanged.
func (m *ManifestSidecarMemory) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if bytes.Equal(trimmed, []byte("null")) {
		return nil
	}

	if len(trimmed) > 0 && trimmed[0] == '"' {
		var value string

		err := json.Unmarshal(trimmed, &value)
		if err != nil {
			return fmt.Errorf("unmarshaling sidecar memory: %w", err)
		}

		*m = ManifestSidecarMemory(value)

		return nil
	}

	var number json.Number

	err := json.Unmarshal(trimmed, &number)
	if err != nil {
		return fmt.Errorf("unmarshaling sidecar memory: %w", err)
	}

	*m = ManifestSidecarMemory(number)

	return nil
}

// MarshalYAML encodes a digits-only value as an integer and any other
// value as a string.
func (m ManifestSidecarMemory) MarshalYAML() (any, error) {
	megabytes, ok := m.megabytes()
	if ok {
		return megabytes, nil
	}

	return string(m), nil
}

// megabytes parses a digits-only value. Signs, units, and values that
// overflow an int report false.
func (m ManifestSidecarMemory) megabytes() (int, bool) {
	if m == "" {
		return 0, false
	}

	for _, r := range m {
		if r < '0' || r > '9' {
			return 0, false
		}
	}

	megabytes, err := strconv.Atoi(string(m))

	return megabytes, err == nil
}

// ManifestMetadata represents metadata in a manifest. A nil value marshals
// as null, which removes that label or annotation from the app; build the
// maps with StringMap and read them with StringValue.
type ManifestMetadata struct {
	Labels      map[string]*string `json:"labels,omitempty"      yaml:"labels,omitempty"`
	Annotations map[string]*string `json:"annotations,omitempty" yaml:"annotations,omitempty"`
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
