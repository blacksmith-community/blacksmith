package boshapi

// Info represents the BOSH Director /info response.
type Info struct {
	Name               string             `json:"name"`
	UUID               string             `json:"uuid"`
	Version            string             `json:"version"`
	User               string             `json:"user"`
	CPI                string             `json:"cpi"`
	StemcellOS         string             `json:"stemcell_os"`
	StemcellVersion    string             `json:"stemcell_version"`
	UserAuthentication UserAuthentication `json:"user_authentication"`
	Features           Features           `json:"features"`
}

// UserAuthentication represents auth configuration from /info.
type UserAuthentication struct {
	Type    string     `json:"type"`
	Options AuthOption `json:"options"`
}

// AuthOption represents authentication options (UAA URL, etc.).
type AuthOption struct {
	URL string `json:"url"`
}

// Features maps a Director feature name, such as "local_dns", "power_dns",
// "snapshots", or "config_server", to its state. Older Directors also send
// "dns" and "compiled_package_cache".
type Features map[string]Feature

// Feature is the state of one Director feature as /info reports it.
type Feature struct {
	// Status is true when the Director has the feature turned on.
	Status bool `json:"status"`
	// Extras holds feature-specific details, such as the "domain_name" of
	// local_dns or the "urls" of config_server. It is nil when the Director
	// sends none.
	Extras map[string]any `json:"extras,omitempty"`
}

// Enabled reports whether the named feature is present and has a true status.
// It returns false for a name the Director did not send.
func (f Features) Enabled(name string) bool {
	return f[name].Status
}

// CertificateExpiry represents a certificate expiry entry from the Director.
type CertificateExpiry struct {
	Path     string `json:"path"`
	Expiry   string `json:"expiry"`
	DaysLeft int    `json:"days_left"`
}
