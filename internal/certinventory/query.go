package certinventory

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"blacksmith/internal/apierr"
	"blacksmith/internal/credhub"
)

// Filter limits the certificates in an inventory response. Repeated values
// of one field combine with OR, and different fields combine with AND.
type Filter struct {
	Expiry, Health, Kind, Source, Owner, Service, Plan, Instance, Finding []string

	Attention     bool
	ExpiresAfter  *time.Time
	ExpiresBefore *time.Time
	Q             string
	SummaryOnly   bool
}

var (
	expiryValues = []string{string(ExpiryExpired), string(ExpiryCritical), string(ExpiryWarning), string(ExpiryOK), string(ExpiryUnknown)}
	healthValues = []string{string(HealthError), string(HealthWarning), string(HealthOK), string(HealthUnknown)}
	kindValues   = []string{string(KindCA), string(KindIntermediate), string(KindLeaf)}
	// findingValues is the closed set of finding codes from the spec.
	findingValues = []string{
		FindingChainBroken, FindingServicesCADrift, FindingPinnedCAMismatch, FindingSigningCAChanged,
		"patch_reverted", "unconverged", FindingIssuerExpiresFirst, FindingSANMismatch, FindingServedMismatch,
		FindingWeakKey, FindingWeakSignature, FindingDeployPending, FindingProbeFailed,
	}
	sourceValues = []string{
		string(SourceCredHub), string(SourceManifest), string(SourceProbe), string(SourceBroker),
		string(SourceTrustStore), string(SourcePlatform), string(SourceVault),
	}
)

func invalidFilter(format string, args ...any) error {
	return apierr.New(http.StatusBadRequest, apierr.CodeInvalidFilter, fmt.Sprintf(format, args...))
}

// ParseFilter reads the filter parameters of an inventory or export
// request. It ignores the format key, which the export reads itself, and
// rejects every other key that is not a filter.
func ParseFilter(v url.Values) (Filter, error) {
	keys := make([]string, 0, len(v))
	for k := range v {
		keys = append(keys, k)
	}

	sort.Strings(keys)

	var f Filter

	for _, key := range keys {
		values := v[key]

		var err error

		switch key {
		case "format":
		case "expiry":
			f.Expiry, err = enumValues(key, values, expiryValues)
		case "health":
			f.Health, err = enumValues(key, values, healthValues)
		case "kind":
			f.Kind, err = enumValues(key, values, kindValues)
		case "source":
			f.Source, err = enumValues(key, values, sourceValues)
		case "owner":
			f.Owner = values
		case "service":
			f.Service = values
		case "plan":
			f.Plan = values
		case "instance":
			f.Instance = values
		case "finding":
			f.Finding, err = enumValues(key, values, findingValues)
		case "attention":
			f.Attention, err = boolValue(key, values)
		case "summary_only":
			f.SummaryOnly, err = boolValue(key, values)
		case "expires_after":
			f.ExpiresAfter, err = timeValue(key, values)
		case "expires_before":
			f.ExpiresBefore, err = timeValue(key, values)
		case "q":
			f.Q = values[0]
		default:
			err = invalidFilter("%q is not a filter parameter", key)
		}

		if err != nil {
			return Filter{}, err
		}
	}

	return f, nil
}

func enumValues(key string, values, allowed []string) ([]string, error) {
	for _, v := range values {
		if !slices.Contains(allowed, v) {
			return nil, invalidFilter("%q is not a value of %s; use one of %s", v, key, strings.Join(allowed, ", "))
		}
	}

	return slices.Clone(values), nil
}

func boolValue(key string, values []string) (bool, error) {
	b, err := strconv.ParseBool(values[len(values)-1])
	if err != nil {
		return false, invalidFilter("%q is not a value of %s; use true or false", values[len(values)-1], key)
	}

	return b, nil
}

func timeValue(key string, values []string) (*time.Time, error) {
	t, err := time.Parse(time.RFC3339, values[0])
	if err != nil {
		return nil, invalidFilter("%q is not a value of %s; use an RFC 3339 time such as 2026-10-27T00:00:00Z", values[0], key)
	}

	return &t, nil
}

// matches reports whether c passes every field of the filter.
func (f Filter) matches(c *Certificate) bool {
	if f.Attention && !c.Attention {
		return false
	}

	if len(f.Expiry) > 0 && !slices.Contains(f.Expiry, string(c.Expiry)) {
		return false
	}

	if len(f.Health) > 0 && !slices.Contains(f.Health, string(c.Health)) {
		return false
	}

	if len(f.Kind) > 0 && !slices.Contains(f.Kind, string(c.Kind)) {
		return false
	}

	if len(f.Source) > 0 && !anyObservation(c, func(o Observation) bool { return slices.Contains(f.Source, string(o.Source)) }) {
		return false
	}

	if !f.ownersMatch(c) {
		return false
	}

	if len(f.Finding) > 0 && !slices.ContainsFunc(c.Findings, func(fd Finding) bool { return slices.Contains(f.Finding, fd.Code) }) {
		return false
	}

	if f.ExpiresAfter != nil && (c.EffectiveNotAfter == nil || c.EffectiveNotAfter.Before(*f.ExpiresAfter)) {
		return false
	}

	if f.ExpiresBefore != nil && (c.EffectiveNotAfter == nil || !c.EffectiveNotAfter.Before(*f.ExpiresBefore)) {
		return false
	}

	return f.Q == "" || matchesQuery(c, strings.ToLower(f.Q))
}

// ownersMatch checks the owner, service, plan, and instance fields. Each
// field needs some owner of the certificate to match one of its values.
func (f Filter) ownersMatch(c *Certificate) bool {
	checks := []struct {
		want []string
		have func(Owner) []string
	}{
		{f.Owner, func(o Owner) []string { return []string{string(o.Kind), o.InstanceName, o.Deployment} }},
		{f.Service, func(o Owner) []string { return []string{o.ServiceName} }},
		{f.Plan, func(o Owner) []string { return []string{o.PlanName} }},
		{f.Instance, func(o Owner) []string { return []string{o.InstanceID, o.InstanceName} }},
	}

	for _, check := range checks {
		if len(check.want) == 0 {
			continue
		}

		if !slices.ContainsFunc(c.Owners, func(o Owner) bool { return overlaps(check.want, check.have(o)) }) {
			return false
		}
	}

	return true
}

func overlaps(want, have []string) bool {
	for _, w := range want {
		for _, h := range have {
			if h != "" && strings.EqualFold(w, h) {
				return true
			}
		}
	}

	return false
}

func anyObservation(c *Certificate, test func(Observation) bool) bool {
	return slices.ContainsFunc(c.Observations, test)
}

// matchesQuery looks for a lowercase substring in the common name, the
// variable, every locator, and every owner name.
func matchesQuery(c *Certificate, q string) bool {
	fields := []string{c.CommonName, c.Variable}

	for _, o := range c.Observations {
		fields = append(fields, o.Locator)
	}

	for _, o := range c.Owners {
		fields = append(fields, o.InstanceName, o.ServiceName, o.Deployment)
	}

	return slices.ContainsFunc(fields, func(s string) bool { return strings.Contains(strings.ToLower(s), q) })
}

// CapabilitiesView is the capabilities block of an inventory response.
type CapabilitiesView struct {
	CredHub      CredHubView  `json:"credhub"`
	Director     DirectorView `json:"director"`
	Rotation     SwitchView   `json:"rotation"`
	PatchInPlace SwitchView   `json:"patch_in_place"`
	Probe        ProbeView    `json:"probe"`
}

// CredHubView says whether CredHub can be used.
type CredHubView struct {
	State  credhub.CapabilityState `json:"state"`
	Reason string                  `json:"reason,omitempty"`
}

// DirectorView says what the director reports and accepts.
type DirectorView struct {
	Version              string `json:"version"`
	ForceLatestVariables bool   `json:"force_latest_variables"`
	Reason               string `json:"reason,omitempty"`
}

// SwitchView says whether a feature is on and, when it is not, why.
type SwitchView struct {
	Enabled bool   `json:"enabled"`
	Reason  string `json:"reason,omitempty"`
}

// ProbeView says whether TLS probing is on.
type ProbeView struct {
	Enabled bool `json:"enabled"`
}

const msgCredHubMissing = "This broker has no credhub block, so CredHub certificates show an unknown expiry."

// View words the capabilities for the API. Rotation is off whenever its
// switch is off, CredHub cannot be used, or the director does not accept
// latest variables, and CredHub is named first because it is the cause an
// operator can fix soonest.
func (c Capabilities) View() CapabilitiesView {
	v := CapabilitiesView{
		CredHub:  CredHubView{State: c.CredHub, Reason: c.CredHubReason},
		Director: DirectorView{Version: c.DirectorVersion, ForceLatestVariables: c.ForceLatest, Reason: c.ForceLatestReason},
		Probe:    ProbeView{Enabled: c.ProbeEnabled},
	}

	if c.CredHub == credhub.StateNotConfigured && v.CredHub.Reason == "" {
		v.CredHub.Reason = msgCredHubMissing
	}

	v.Rotation.Enabled, v.Rotation.Reason = c.rotationState()

	switch {
	case !c.PatchEnabled:
		v.PatchInPlace.Reason = "certificates.patch_in_place.enabled is false"
	case !v.Rotation.Enabled:
		v.PatchInPlace.Reason = v.Rotation.Reason
	default:
		v.PatchInPlace.Enabled = true
	}

	return v
}

func (c Capabilities) rotationState() (bool, string) {
	if c.DirectorUnavailable {
		return false, "BOSH director unavailable"
	}

	//nolint:exhaustive // The default branch covers the unreachable and refused states.
	switch c.CredHub {
	case credhub.StateConfigured:
	case credhub.StateNotConfigured:
		return false, "CredHub not configured"
	default:
		reason := "CredHub unavailable"
		if c.CredHubReason != "" {
			reason += ": " + c.CredHubReason
		}

		return false, reason
	}

	if !c.ForceLatest {
		if c.ForceLatestReason != "" {
			return false, c.ForceLatestReason
		}

		return false, "The director does not accept latest variables."
	}

	if !c.RotationEnabled {
		return false, "certificates.rotation.enabled is false"
	}

	return true, ""
}

// ThresholdsView is the part of the settings an inventory response repeats.
type ThresholdsView struct {
	WarningDays  int `json:"warning_days"`
	CriticalDays int `json:"critical_days"`
}

// InventoryResponse is the body of GET /b/certificates/inventory.
type InventoryResponse struct {
	GeneratedAt  time.Time        `json:"generated_at"`
	Summary      Summary          `json:"summary"`
	Filtered     Summary          `json:"filtered"`
	Scan         ScanState        `json:"scan"`
	Capabilities CapabilitiesView `json:"capabilities"`
	Settings     ThresholdsView   `json:"settings"`
	Certificates []Certificate    `json:"certificates"`
}

// BuildResponse summarises the whole fleet, filters it, and orders the
// result with certificates that need attention first and then by days left,
// with an unknown expiry after the known ones. Expiry is worked out again
// from now, so a fleet built an hour ago still answers with today's days
// left. When the latest scan failed and none has ever succeeded, the
// response holds no certificates and an empty summary, and the scan block
// carries the error.
func BuildResponse(f *Fleet, filter Filter, s Settings, caps Capabilities, state ScanState, now time.Time) InventoryResponse {
	resp := InventoryResponse{
		GeneratedAt:  now,
		Scan:         state,
		Capabilities: caps.View(),
		Settings:     ThresholdsView{WarningDays: s.WarningDays, CriticalDays: s.CriticalDays},
		Certificates: []Certificate{},
	}

	if state.State == ScanFailed && (f == nil || !f.everScanned) {
		if len(resp.Scan.Errors) == 0 {
			resp.Scan.Errors = []ScanError{{Error: "The first scan failed, so the broker has no certificates to show yet."}}
		}

		return resp
	}

	all := slices.Clone(f.Certificates())

	for i := range all {
		c := &all[i]
		applyExpiry(c, s, now)
		c.Attention = NeedsAttention(c)
	}

	resp.Summary = Summarize(all)

	var picked []Certificate

	for i := range all {
		if filter.matches(&all[i]) {
			picked = append(picked, all[i])
		}
	}

	resp.Filtered = Summarize(picked)

	if filter.SummaryOnly {
		return resp
	}

	sort.SliceStable(picked, func(a, b int) bool { return urgencyLess(&picked[a], &picked[b]) })

	if picked != nil {
		resp.Certificates = picked
	}

	return resp
}

// urgencyLess orders by attention, then by effective expiry with unknown
// last, then by ID.
func urgencyLess(a, b *Certificate) bool {
	if a.Attention != b.Attention {
		return a.Attention
	}

	switch {
	case a.EffectiveNotAfter == nil && b.EffectiveNotAfter != nil:
		return false
	case a.EffectiveNotAfter != nil && b.EffectiveNotAfter == nil:
		return true
	case a.EffectiveNotAfter != nil && !a.EffectiveNotAfter.Equal(*b.EffectiveNotAfter):
		return a.EffectiveNotAfter.Before(*b.EffectiveNotAfter)
	}

	return a.ID < b.ID
}
