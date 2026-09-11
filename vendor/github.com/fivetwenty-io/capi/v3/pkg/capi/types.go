package capi

import (
	"encoding/json"
	"time"
)

// Resource represents the base structure for all CF API resources.
type Resource struct {
	GUID      string    `json:"guid"       yaml:"guid"`
	CreatedAt time.Time `json:"created_at" yaml:"created_at"`
	UpdatedAt time.Time `json:"updated_at" yaml:"updated_at"`
	Links     Links     `json:"links"      yaml:"links"`
}

// Links represents resource links.
type Links map[string]Link

// Link represents a single link.
//
// Meta is the optional per-link metadata sub-object that CF uses to
// carry context-specific data alongside an href:
//
//   - links.cloud_controller_v{2,3}.meta.version  → API semver
//     (e.g. "2.245.0", "3.180.0")
//   - links.app_ssh.meta.host_key_fingerprint     → SSH proxy host-key
//     fingerprint for client-side verification
//   - links.app_ssh.meta.oauth_client             → UAA client_id used to
//     mint short-lived SSH access codes ("ssh-proxy" by convention)
//
// The shape of meta varies by link, so we model it as an open map.
// Callers that don't reference Meta are unaffected (zero value, omitted
// from JSON).
type Link struct {
	Href   string         `json:"href"             yaml:"href"`
	Method string         `json:"method,omitempty" yaml:"method,omitempty"`
	Meta   map[string]any `json:"meta,omitempty"   yaml:"meta,omitempty"`
}

// Metadata represents labels and annotations.
//
// Values are pointers so update requests can distinguish setting a key
// (non-nil), leaving it untouched (key absent), and deleting it (nil,
// which marshals to JSON null per the CF v3 metadata contract).
type Metadata struct {
	Labels      map[string]*string `json:"labels,omitempty"      yaml:"labels,omitempty"`
	Annotations map[string]*string `json:"annotations,omitempty" yaml:"annotations,omitempty"`
}

// Relationship represents a to-one relationship.
type Relationship struct {
	Data *RelationshipData `json:"data,omitempty" yaml:"data,omitempty"`
}

// RelationshipData contains the GUID of the related resource.
type RelationshipData struct {
	GUID string `json:"guid" yaml:"guid"`
}

// ToManyRelationship represents a to-many relationship.
type ToManyRelationship struct {
	Data []RelationshipData `json:"data" yaml:"data"`
}

// Pagination represents pagination information.
type Pagination struct {
	TotalResults int   `json:"total_results"      yaml:"total_results"`
	TotalPages   int   `json:"total_pages"        yaml:"total_pages"`
	First        Link  `json:"first"              yaml:"first"`
	Last         Link  `json:"last"               yaml:"last"`
	Next         *Link `json:"next,omitempty"     yaml:"next,omitempty"`
	Previous     *Link `json:"previous,omitempty" yaml:"previous,omitempty"`
}

// ListResponse represents a paginated list response.
//
// Included carries v3's `included` block when the request used
// `?include=...`. v3 returns the joined resources as a top-level
// object whose keys are resource-type plural names (e.g.
// `service_brokers`, `service_plans`) and values are arrays of
// resources of that type. Included resources are heterogeneous, so
// they're held as raw JSON; callers re-decode the bucket they want
// using `json.Unmarshal(raw, &[]ServiceBroker{})` etc.
//
// `omitempty` keeps existing tests that asserted absence of an
// `included` key passing for responses without one.
type ListResponse[T any] struct {
	Pagination Pagination                   `json:"pagination"         yaml:"pagination"`
	Resources  []T                          `json:"resources"          yaml:"resources"`
	Included   map[string][]json.RawMessage `json:"included,omitempty" yaml:"included,omitempty"`
}

// AppEnv is an alias for AppEnvironment to maintain backward compatibility.
type AppEnv = AppEnvironment

// ProcessList represents a paginated list of Process resources.
type ProcessList = ListResponse[Process]

// AuditEventsList represents a paginated list of AuditEvent resources.
type AuditEventsList = ListResponse[AuditEvent]

// ProcessStat is an alias for ProcessStatsDetail to maintain backward compatibility.
type ProcessStat = ProcessStatsDetail

// InstancePort is an alias for ProcessInstancePort to maintain backward compatibility.
type InstancePort = ProcessInstancePort

// Actor is an alias for AuditEventActor to maintain backward compatibility.
type Actor = AuditEventActor

// Target is an alias for AuditEventTarget to maintain backward compatibility.
type Target = AuditEventTarget

// BuildpacksList represents a paginated list of Buildpack resources.
type BuildpacksList = ListResponse[Buildpack]

// DomainsList represents a paginated list of Domain resources.
type DomainsList = ListResponse[Domain]

// Include represents include parameters for API requests.
type Include []string

// Fields represents field selection parameters.
type Fields map[string][]string
