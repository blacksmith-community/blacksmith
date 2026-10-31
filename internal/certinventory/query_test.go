package certinventory

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"blacksmith/internal/apierr"
	"blacksmith/internal/credhub"
)

func wantInvalidFilter(t *testing.T, err error, contains string) {
	t.Helper()

	var apiErr *apierr.Error
	if !errors.As(err, &apiErr) || apiErr.Code != apierr.CodeInvalidFilter || apiErr.Status != http.StatusBadRequest {
		t.Fatalf("got %v, want a 400 invalid_filter error", err)
	}

	if !strings.Contains(apiErr.Message, contains) {
		t.Errorf("message %q does not name %q", apiErr.Message, contains)
	}
}

func TestParseFilterRepeatsAndErrors(t *testing.T) {
	t.Parallel()

	f, err := ParseFilter(url.Values{"expiry": {"critical", "warning"}, "service": {"redis"}})
	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(f.Expiry, []string{"critical", "warning"}) || !reflect.DeepEqual(f.Service, []string{"redis"}) {
		t.Errorf("parsed %+v", f)
	}

	_, err = ParseFilter(url.Values{"expiry": {"soon"}})
	wantInvalidFilter(t, err, "soon")

	_, err = ParseFilter(url.Values{"expires_after": {"yesterday"}})
	wantInvalidFilter(t, err, "expires_after")

	_, err = ParseFilter(url.Values{"attention": {"maybe"}})
	wantInvalidFilter(t, err, "attention")

	f, err = ParseFilter(url.Values{"attention": {"true"}, "summary_only": {"true"}, "q": {"node/2"}, "expires_before": {"2026-11-03T00:00:00Z"}})
	if err != nil || !f.Attention || !f.SummaryOnly || f.Q != "node/2" || f.ExpiresBefore == nil {
		t.Errorf("parsed %+v, %v", f, err)
	}
}

func TestParseFilterFindingIsClosedSet(t *testing.T) {
	t.Parallel()

	for _, code := range []string{"chain_broken", "services_ca_drift", "pinned_ca_mismatch", "signing_ca_changed", "patch_reverted", "unconverged",
		"issuer_expires_first", "san_mismatch", "served_mismatch", "weak_key", "weak_signature", "deploy_pending", "probe_failed"} {
		if _, err := ParseFilter(url.Values{"finding": {code}}); err != nil {
			t.Errorf("finding=%s rejected: %v", code, err)
		}
	}

	_, err := ParseFilter(url.Values{"finding": {"chain_broken", "made_up"}})
	wantInvalidFilter(t, err, "made_up")
}

func TestParseFilterUnknownKeys(t *testing.T) {
	t.Parallel()

	f, err := ParseFilter(url.Values{"format": {"csv"}, "expiry": {"warning"}})
	if err != nil || len(f.Expiry) != 1 {
		t.Fatalf("format should be ignored, got %+v, %v", f, err)
	}

	_, err = ParseFilter(url.Values{"colour": {"red"}})
	wantInvalidFilter(t, err, "colour")
}

// queryFleet holds five certificates that differ in every filtered field.
func queryFleet() *Fleet {
	day := func(n int) *time.Time {
		t := fleetNow.Add(time.Duration(n)*24*time.Hour + time.Hour)

		return &t
	}

	owner := func(id, svc, plan string) []Owner {
		return []Owner{{Kind: OwnerInstance, InstanceID: id, InstanceName: "name-" + id, Deployment: "dep-" + id, ServiceName: svc, PlanName: plan}}
	}

	certs := []Certificate{
		{
			ID: "a", Variable: "redis_crt", Kind: KindLeaf, Owners: owner("i1", "redis", "cluster-7"), EffectiveNotAfter: day(2),
			Health: HealthWarning, Findings: []Finding{{Code: FindingProbeFailed}},
			Observations: []Observation{{Source: SourceProbe, Locator: "node/2 10.0.0.3:6379"}},
			Coverage:     Coverage{Complete: true},
		},
		{
			ID: "b", Variable: "redis_ca", Kind: KindCA, Owners: owner("i1", "redis", "cluster-7"), EffectiveNotAfter: day(6),
			Health: HealthOK, Observations: []Observation{{Source: SourceCredHub, Locator: "/d/dep/redis_ca"}}, Coverage: Coverage{Complete: true},
		},
		{
			ID: "c", Variable: "amqp_crt", Kind: KindLeaf, Owners: owner("i2", "rabbitmq", "single"), EffectiveNotAfter: day(20),
			Health: HealthError, Observations: []Observation{{Source: SourceCredHub, Locator: "/d/dep2/amqp_crt"}}, Coverage: Coverage{Complete: true},
		},
		{
			ID: "d", Variable: "amqp_ca", Kind: KindCA, Owners: owner("i2", "rabbitmq", "single"), EffectiveNotAfter: day(90),
			Health: HealthOK, Observations: []Observation{{Source: SourceManifest, Locator: "m"}}, Coverage: Coverage{Complete: true},
		},
		{
			ID: "e", Variable: "mystery", Kind: KindLeaf, Owners: owner("i3", "redis", "single"),
			Health: HealthUnknown, Observations: []Observation{{Source: SourceCredHub, Locator: "/d/dep3/mystery"}},
		},
	}

	return &Fleet{certs: certs, everScanned: true}
}

func idsOf(resp InventoryResponse) []string {
	var ids []string
	for _, c := range resp.Certificates {
		ids = append(ids, c.ID)
	}

	return ids
}

func respond(t *testing.T, f *Fleet, query string) InventoryResponse {
	t.Helper()

	values, err := url.ParseQuery(query)
	if err != nil {
		t.Fatal(err)
	}

	filter, err := ParseFilter(values)
	if err != nil {
		t.Fatal(err)
	}

	return BuildResponse(f, filter, DefaultSettings(), fleetCaps, ScanState{State: ScanIdle}, fleetNow)
}

func TestBuildResponseFilterSemantics(t *testing.T) {
	t.Parallel()

	f := queryFleet()

	cases := []struct {
		query string
		want  []string
	}{
		{"", []string{"a", "b", "c", "d", "e"}},
		{"expiry=critical&expiry=warning", []string{"a", "b", "c"}},
		{"expiry=critical&expiry=warning&service=redis", []string{"a", "b"}},
		{"expiry=critical&kind=leaf", []string{"a"}},
		{"health=error&health=warning", []string{"a", "c"}},
		{"source=manifest", []string{"d"}},
		{"plan=single", []string{"c", "d", "e"}},
		{"service=rabbitmq&plan=cluster-7", nil},
		{"instance=i3", []string{"e"}},
		{"instance=name-i2", []string{"c", "d"}},
		{"owner=instance&finding=probe_failed", []string{"a"}},
		{"attention=true", []string{"a", "b", "c"}},
	}

	for _, tc := range cases {
		if got := idsOf(respond(t, f, tc.query)); !reflect.DeepEqual(got, tc.want) && (len(got) != 0 || len(tc.want) != 0) {
			t.Errorf("%q gave %v, want %v", tc.query, got, tc.want)
		}
	}
}

func TestBuildResponseOrder(t *testing.T) {
	t.Parallel()

	got := idsOf(respond(t, queryFleet(), ""))
	if want := []string{"a", "b", "c", "d", "e"}; !reflect.DeepEqual(got, want) {
		t.Errorf("order %v, want %v (attention first, then soonest, unknown last)", got, want)
	}
}

func TestBuildResponseSummaryIsWholeFleet(t *testing.T) {
	t.Parallel()

	resp := respond(t, queryFleet(), "expiry=critical&kind=leaf")
	if resp.Summary.Certificates != 5 || resp.Filtered.Certificates != 1 {
		t.Errorf("summary %d, filtered %d, want 5 and 1", resp.Summary.Certificates, resp.Filtered.Certificates)
	}

	if resp.Summary.Critical != 2 || resp.Summary.ExpiryUnknown != 1 {
		t.Errorf("summary %+v", resp.Summary)
	}

	only := respond(t, queryFleet(), "summary_only=true&expiry=ok")
	if len(only.Certificates) != 0 || only.Summary.Certificates != 5 || only.Filtered.Certificates != 1 {
		t.Errorf("summary_only gave %d certificates, summary %d, filtered %d", len(only.Certificates), only.Summary.Certificates, only.Filtered.Certificates)
	}
}

func TestBuildResponseExpiresWindow(t *testing.T) {
	t.Parallel()

	// The fixture expires at now + 6 days + 1 hour, which is 2026-10-28.
	resp := respond(t, queryFleet(), "expires_after=2026-10-27T00:00:00Z&expires_before=2026-11-03T00:00:00Z")
	if got := idsOf(resp); !reflect.DeepEqual(got, []string{"b"}) {
		t.Errorf("window gave %v, want [b]", got)
	}
}

func TestBuildResponseQMatchesLocator(t *testing.T) {
	t.Parallel()

	if got := idsOf(respond(t, queryFleet(), "q=node/2")); !reflect.DeepEqual(got, []string{"a"}) {
		t.Errorf("q=node/2 gave %v", got)
	}

	if got := idsOf(respond(t, queryFleet(), "q=MYSTERY")); !reflect.DeepEqual(got, []string{"e"}) {
		t.Errorf("q=MYSTERY gave %v, want a case-blind variable match", got)
	}

	if got := idsOf(respond(t, queryFleet(), "q=dep-i2")); !reflect.DeepEqual(got, []string{"c", "d"}) {
		t.Errorf("q=dep-i2 gave %v, want an owner match", got)
	}
}

func TestBuildResponseRecomputesExpiryFromNow(t *testing.T) {
	t.Parallel()

	f := queryFleet()
	f.certs[0].Expiry, f.certs[0].DaysLeft = ExpiryOK, nil

	resp := respond(t, f, "")
	if c := resp.Certificates[0]; c.Expiry != ExpiryCritical || c.DaysLeft == nil || *c.DaysLeft != 2 {
		t.Errorf("expiry %q days %v, want critical and 2", c.Expiry, c.DaysLeft)
	}

	if f.certs[0].Expiry != ExpiryOK {
		t.Error("BuildResponse changed the fleet")
	}
}

func TestBuildResponseHasNoPEM(t *testing.T) {
	t.Parallel()

	scans, _ := sharedCAFleet(t)
	fleet := Aggregate(scans, DefaultSettings(), fleetCaps, nil, fleetNow)

	raw, err := json.Marshal(BuildResponse(fleet, Filter{}, DefaultSettings(), fleetCaps, ScanState{}, fleetNow))
	if err != nil {
		t.Fatal(err)
	}

	for _, banned := range []string{"BEGIN CERTIFICATE", "PRIVATE KEY"} {
		if strings.Contains(string(raw), banned) {
			t.Errorf("response contains %q", banned)
		}
	}
}

func TestBuildResponseFirstScanFailed(t *testing.T) {
	t.Parallel()

	state := ScanState{State: ScanFailed}

	for _, fleet := range []*Fleet{nil, Aggregate(nil, DefaultSettings(), fleetCaps, nil, fleetNow)} {
		resp := BuildResponse(fleet, Filter{}, DefaultSettings(), fleetCaps, state, fleetNow)
		if resp.Certificates == nil || len(resp.Certificates) != 0 || resp.Summary != (Summary{}) || len(resp.Scan.Errors) == 0 {
			t.Errorf("first-scan failure gave %+v", resp)
		}
	}

	// Once a scan has succeeded, a failed run still shows what is known.
	ok := fleetScan("i1", fleetNow)
	leaf := issue(t, issueSpec{cn: "x", serial: 2})
	ok.Material.CredHub = []CredHubCert{{Name: "/ws-bosh/redis-i1/crt", Owner: instOwner("i1"), Deployed: leaf.cert}}

	resp := BuildResponse(Aggregate([]InstanceScan{ok}, DefaultSettings(), fleetCaps, nil, fleetNow), Filter{}, DefaultSettings(), fleetCaps, state, fleetNow)
	if len(resp.Certificates) != 1 {
		t.Errorf("a failed run after a good one dropped the certificates")
	}
}

func TestResponseJSONShape(t *testing.T) {
	t.Parallel()

	caps := Capabilities{CredHub: credhub.StateNotConfigured, DirectorVersion: "282.0.5", ForceLatest: true, ProbeEnabled: true}
	resp := BuildResponse(nil, Filter{}, DefaultSettings(), caps, ScanState{State: ScanIdle}, fleetNow)

	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}

	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}

	for _, key := range []string{"generated_at", "summary", "filtered", "scan", "capabilities", "settings", "certificates"} {
		if _, ok := got[key]; !ok {
			t.Errorf("response lacks %q", key)
		}
	}

	c := asMap(t, got["capabilities"])

	cred := asMap(t, c["credhub"])
	if cred["state"] != "not_configured" || cred["reason"] == "" {
		t.Errorf("credhub block %v", cred)
	}

	rot := asMap(t, c["rotation"])
	if rot["enabled"] != false || rot["reason"] != "CredHub not configured" {
		t.Errorf("rotation block %v", rot)
	}

	if d := asMap(t, c["director"]); d["version"] != "282.0.5" || d["force_latest_variables"] != true {
		t.Errorf("director block %v", d)
	}

	if p := asMap(t, c["patch_in_place"]); p["enabled"] != false || p["reason"] != "certificates.patch_in_place.enabled is false" {
		t.Errorf("patch block %v", p)
	}

	if s := asMap(t, got["settings"]); s["warning_days"] != float64(30) || s["critical_days"] != float64(7) || s["pause_on_failure"] != true {
		t.Errorf("settings block %v", s)
	}

	// The settings block repeats pause_on_failure as it stands, so the
	// batch flow starts its checkbox from it.
	off := DefaultSettings()
	off.PauseOnFailure = false

	if resp := BuildResponse(nil, Filter{}, off, caps, ScanState{State: ScanIdle}, fleetNow); resp.Settings.PauseOnFailure {
		t.Errorf("settings block %+v, want pause_on_failure off", resp.Settings)
	}
}

// asMap returns v as a JSON object, and fails the test when it is not one.
func asMap(t *testing.T, v any) map[string]any {
	t.Helper()

	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("got %T, want a JSON object", v)
	}

	return m
}
