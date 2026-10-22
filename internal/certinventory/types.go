// Package certinventory holds the certificate inventory model and the rules
// that turn observations into expiry status, health, and attention.
package certinventory

import "time"

// ExpiryStatus is "expired", "critical", "warning", "ok", or "unknown".
type ExpiryStatus string

// Expiry status values.
const (
	ExpiryExpired  ExpiryStatus = "expired"
	ExpiryCritical ExpiryStatus = "critical"
	ExpiryWarning  ExpiryStatus = "warning"
	ExpiryOK       ExpiryStatus = "ok"
	ExpiryUnknown  ExpiryStatus = "unknown"
)

// Health is "error", "warning", "ok", or "unknown".
type Health string

// Health values.
const (
	HealthError   Health = "error"
	HealthWarning Health = "warning"
	HealthOK      Health = "ok"
	HealthUnknown Health = "unknown"
)

// Kind is "ca", "intermediate", or "leaf".
type Kind string

// Kind values.
const (
	KindCA           Kind = "ca"
	KindIntermediate Kind = "intermediate"
	KindLeaf         Kind = "leaf"
)

// Source is "credhub", "manifest", "probe", "broker", "trust-store",
// "platform", or "vault".
type Source string

// Source values. SourceVault only marks a failed read of an instance's
// binding credentials; no certificate is ever observed from Vault.
const (
	SourceCredHub    Source = "credhub"
	SourceManifest   Source = "manifest"
	SourceProbe      Source = "probe"
	SourceBroker     Source = "broker"
	SourceTrustStore Source = "trust-store"
	SourcePlatform   Source = "platform"
	SourceVault      Source = "vault"
)

// OwnerKind is "broker", "instance", or "platform".
type OwnerKind string

// OwnerKind values.
const (
	OwnerBroker   OwnerKind = "broker"
	OwnerInstance OwnerKind = "instance"
	OwnerPlatform OwnerKind = "platform"
)

// Finding severities.
const (
	SeverityError   = "error"
	SeverityWarning = "warning"
	SeverityInfo    = "info"
)

// Divergence states.
const (
	DivergenceUnconverged = "unconverged"
	DivergenceReverted    = "reverted"
)

// Owner says who a certificate belongs to.
type Owner struct {
	Kind         OwnerKind `json:"kind"`
	InstanceID   string    `json:"instance_id,omitempty"`
	InstanceName string    `json:"instance_name,omitempty"`
	Deployment   string    `json:"deployment,omitempty"`
	ServiceName  string    `json:"service_name,omitempty"`
	PlanName     string    `json:"plan_name,omitempty"`
}

// Certificate is one logical certificate.
type Certificate struct {
	ID                string  `json:"id"`  // stable hash of the grouping key
	Key               string  `json:"key"` // CredHub name, or "sha256:<fingerprint>"
	Owners            []Owner `json:"owners"`
	AffectedInstances int     `json:"affected_instances"`
	Variable          string  `json:"variable,omitempty"`
	Kind              Kind    `json:"kind"`

	CommonName     string   `json:"common_name,omitempty"`
	Issuer         string   `json:"issuer,omitempty"`
	SubjectKeyID   string   `json:"subject_key_id,omitempty"`
	AuthorityKeyID string   `json:"authority_key_id,omitempty"`
	DNSNames       []string `json:"dns_names,omitempty"`
	IPAddresses    []string `json:"ip_addresses,omitempty"`
	ExtKeyUsages   []string `json:"ext_key_usages,omitempty"`
	KeyAlgorithm   string   `json:"key_algorithm,omitempty"`
	KeyBits        int      `json:"key_bits,omitempty"`

	EffectiveNotAfter *time.Time   `json:"effective_not_after,omitempty"`
	DaysLeft          *int         `json:"days_left,omitempty"` // computed at response time
	Expiry            ExpiryStatus `json:"expiry"`              // computed at response time
	ExpiryReason      string       `json:"expiry_reason,omitempty"`
	Health            Health       `json:"health"`
	Attention         bool         `json:"attention"`

	Versions     *Versions      `json:"versions,omitempty"`
	Observations []Observation  `json:"observations"`
	Findings     []Finding      `json:"findings,omitempty"`
	Coverage     Coverage       `json:"coverage"`
	Rotation     Rotatability   `json:"rotation"`
	Divergence   *DivergenceRef `json:"divergence,omitempty"`
}

// Observation is one place a certificate was seen.
type Observation struct {
	Source      Source     `json:"source"`
	Owner       Owner      `json:"owner"`
	Locator     string     `json:"locator"` // YAML path, vm/job ip:port, file path, or config key
	Fingerprint string     `json:"sha256_fingerprint,omitempty"`
	Serial      string     `json:"serial,omitempty"`
	NotAfter    *time.Time `json:"not_after,omitempty"`
	Matches     string     `json:"matches,omitempty"` // "deployed", "latest", "other"
	State       string     `json:"state"`             // "ok", "stale", "failed"
	Error       string     `json:"error,omitempty"`
	ObservedAt  time.Time  `json:"observed_at"`
}

// Versions records the CredHub versions of a certificate.
type Versions struct {
	CertificateID     string     `json:"certificate_id"`
	DeployedVersionID string     `json:"deployed_version_id"`
	DeployedNotAfter  *time.Time `json:"deployed_not_after,omitempty"`
	LatestVersionID   string     `json:"latest_version_id"`
	LatestNotAfter    *time.Time `json:"latest_not_after,omitempty"`
	Transitional      bool       `json:"transitional"`
	SigningCA         string     `json:"signing_ca,omitempty"`
}

// Coverage says how much of the expected surface was observed.
type Coverage struct {
	ExpectedListeners int               `json:"expected_listeners"`
	ObservedListeners int               `json:"observed_listeners"`
	FailedListeners   int               `json:"failed_listeners"`
	Complete          bool              `json:"complete"`
	Stale             bool              `json:"stale"`
	OldestObservation time.Time         `json:"oldest_observation"`
	SourceStates      map[Source]string `json:"source_states"` // "ok", "stale", "failed", "disabled", "not_configured"
}

// Finding is one problem found with a certificate.
type Finding struct {
	Code     string `json:"code"`
	Severity string `json:"severity"` // "error", "warning", "info"
	Message  string `json:"message"`
}

// Rotatability says whether and how a certificate can be rotated.
type Rotatability struct {
	Allowed bool     `json:"allowed"`
	Modes   []string `json:"modes,omitempty"`   // "redeploy", "patch"
	Reasons []string `json:"reasons,omitempty"` // why not, or why a mode is missing
}

// DivergenceRef points at a record of a patch that BOSH does not know about.
type DivergenceRef struct {
	State  string    `json:"state"` // "unconverged", "reverted"
	TaskID string    `json:"task_id"`
	Since  time.Time `json:"since"`
}

// Summary counts certificates by status.
type Summary struct {
	Certificates       int `json:"certificates"`
	AffectedInstances  int `json:"affected_instances"`
	Expired            int `json:"expired"`
	Critical           int `json:"critical"`
	Warning            int `json:"warning"`
	ExpiryOK           int `json:"expiry_ok"`
	ExpiryUnknown      int `json:"expiry_unknown"`
	HealthErrors       int `json:"health_errors"`
	NeedsAttention     int `json:"needs_attention"`
	Unconverged        int `json:"unconverged"`
	Stale              int `json:"stale"`
	IncompleteCoverage int `json:"incomplete_coverage"`
}
