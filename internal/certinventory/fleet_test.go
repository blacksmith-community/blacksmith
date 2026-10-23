package certinventory

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	mrand "math/rand/v2"
	"reflect"
	"strings"
	"testing"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/credhub"
)

var (
	fleetNow  = time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	fleetCaps = Capabilities{
		CredHub: credhub.StateConfigured, ForceLatest: true, RotationEnabled: true,
		DirectorName: "ws-bosh", ProbeEnabled: true,
	}
)

const servicesCAName = "/ws-bosh/blacksmith_services_ca"

// sharedBrokerAt is the broker scan's own time in sharedCAFleet, distinct
// from the time of every instance scan.
var sharedBrokerAt = fleetNow.Add(-7 * time.Minute)

func fleetScan(id string, at time.Time) InstanceScan {
	return InstanceScan{
		Ref:       InstanceRef{ID: id, Name: "node-" + id, Deployment: "redis-" + id, ServiceName: "redis", PlanName: "cluster-7"},
		ScannedAt: at, LastSuccessAt: at,
	}
}

func brokerScan(at time.Time) InstanceScan {
	return InstanceScan{Ref: InstanceRef{ID: BrokerScanID, Name: "broker"}, ScannedAt: at, LastSuccessAt: at}
}

// sharedCAFleet builds 40 instance scans that each carry the services CA as
// a manifest literal, plus a broker scan with the CA file and its CredHub
// copy.
func sharedCAFleet(t *testing.T) ([]InstanceScan, issued) {
	t.Helper()

	ca := issue(t, issueSpec{cn: "services-ca", serial: 9, isCA: true, notAfter: 400 * time.Hour})
	at := fleetNow.Add(-time.Minute)

	var scans []InstanceScan

	for n := 0; n < 40; n++ {
		id := fmt.Sprintf("i%02d", n)
		s := fleetScan(id, at)
		s.Material.Raw = []RawObservation{rawObs(SourceManifest, instOwner(id), "instance_groups[0].properties.ca", ca.cert)}
		scans = append(scans, s)
	}

	b := brokerScan(sharedBrokerAt)
	broker := Owner{Kind: OwnerBroker}
	b.Material.Raw = []RawObservation{rawObs(SourceBroker, broker, "/var/vcap/jobs/blacksmith/config/tls/blacksmith_services_ca", ca.cert)}
	b.Material.CredHub = []CredHubCert{{Name: servicesCAName, Owner: broker, Deployed: ca.cert}}

	return append(scans, b), ca
}

func TestFleetGroupsSharedCAAcrossScans(t *testing.T) {
	t.Parallel()

	scans, _ := sharedCAFleet(t)
	fleet := Aggregate(scans, DefaultSettings(), fleetCaps, nil, fleetNow)

	certs := fleet.Certificates()
	if len(certs) != 1 {
		t.Fatalf("got %d certificates, want 1", len(certs))
	}

	c := certs[0]
	if c.Key != servicesCAName {
		t.Errorf("key %q, want the CredHub name", c.Key)
	}

	if c.AffectedInstances != 40 {
		t.Errorf("affected instances %d, want 40", c.AffectedInstances)
	}

	hasBroker := false

	for _, o := range c.Owners {
		hasBroker = hasBroker || o.Kind == OwnerBroker
	}

	if !hasBroker {
		t.Errorf("no broker owner in %v", c.Owners[:min(2, len(c.Owners))])
	}
}

func TestFleetStampsCredHubObservedAt(t *testing.T) {
	t.Parallel()

	scans, _ := sharedCAFleet(t)
	fleet := Aggregate(scans, DefaultSettings(), fleetCaps, nil, fleetNow)

	credhubSeen := 0

	for _, o := range fleet.Certificates()[0].Observations {
		if o.Source != SourceCredHub {
			continue
		}

		credhubSeen++

		// Only the broker scan read the CredHub copy, and its time differs
		// from every instance scan, so stamping from another scan fails.
		if !o.ObservedAt.Equal(sharedBrokerAt) {
			t.Errorf("credhub observation ObservedAt %v, want the broker scan time %v", o.ObservedAt, sharedBrokerAt)
		}
	}

	if credhubSeen == 0 {
		t.Fatal("the certificate has no CredHub observation to check")
	}
}

func TestFleetStaleScanMarksCredHubObservation(t *testing.T) {
	t.Parallel()

	leaf := issue(t, issueSpec{cn: "redis", serial: 3})
	old := fleetNow.Add(-6 * time.Hour)
	s := fleetScan("i1", old)
	s.Stale = true
	s.Err = "scan timed out after 60s"
	s.Material.CredHub = []CredHubCert{{Name: "/ws-bosh/redis-i1/redis_crt", Owner: instOwner("i1"), Deployed: leaf.cert}}

	c := Aggregate([]InstanceScan{s}, DefaultSettings(), fleetCaps, nil, fleetNow).Certificates()[0]

	obs := c.Observations[0]
	if obs.State != StateStale || !obs.ObservedAt.Equal(old) {
		t.Errorf("observation state %q at %v, want stale at %v", obs.State, obs.ObservedAt, old)
	}

	if !c.Coverage.Stale {
		t.Error("coverage does not say stale")
	}
}

func TestFleetMergesCredHubNameAcrossScans(t *testing.T) {
	t.Parallel()

	oldCert := issue(t, issueSpec{cn: "old", serial: 1})
	newCert := issue(t, issueSpec{cn: "new", serial: 2})
	name := "/ws-bosh/shared/crt"

	a := fleetScan("a", fleetNow.Add(-2*time.Hour))
	a.Material.CredHub = []CredHubCert{{Name: name, Owner: instOwner("a"), Deployed: oldCert.cert, Versions: Versions{DeployedVersionID: "v1", LatestVersionID: "v1"}}}
	b := fleetScan("b", fleetNow.Add(-time.Minute))
	b.Material.CredHub = []CredHubCert{{Name: name, Owner: instOwner("b"), Deployed: newCert.cert, Versions: Versions{DeployedVersionID: "v2", LatestVersionID: "v2"}}}

	for _, order := range [][]InstanceScan{{a, b}, {b, a}} {
		certs := Aggregate(order, DefaultSettings(), fleetCaps, nil, fleetNow).Certificates()
		if len(certs) != 1 {
			t.Fatalf("got %d certificates, want 1", len(certs))
		}

		c := certs[0]
		if c.Versions.DeployedVersionID != "v2" || c.CommonName != "new" {
			t.Errorf("kept versions %q (%s), want the newest scan's", c.Versions.DeployedVersionID, c.CommonName)
		}

		if len(c.Owners) != 2 || c.AffectedInstances != 2 || len(c.Observations) != 2 {
			t.Errorf("owners %d, affected %d, observations %d, want 2 each", len(c.Owners), c.AffectedInstances, len(c.Observations))
		}

		for _, o := range c.Observations {
			if o.ObservedAt.IsZero() {
				t.Errorf("observation of %s has no time", o.Owner.InstanceID)
			}
		}
	}
}

func TestFleetIDsStableAcrossScanOrder(t *testing.T) {
	t.Parallel()

	scans, _ := sharedCAFleet(t)

	for n := 0; n < 5; n++ {
		leaf := issue(t, issueSpec{cn: "leaf", serial: int64(20 + n)})
		id := fmt.Sprintf("i%02d", n)
		scans[n].Material.CredHub = []CredHubCert{{Name: "/ws-bosh/redis-" + id + "/crt", Owner: instOwner(id), Deployed: leaf.cert}}
	}

	render := func(in []InstanceScan) string {
		resp := BuildResponse(Aggregate(in, DefaultSettings(), fleetCaps, nil, fleetNow), Filter{}, DefaultSettings(), fleetCaps, ScanState{}, fleetNow)

		raw, err := json.Marshal(resp)
		if err != nil {
			t.Fatal(err)
		}

		return string(raw)
	}

	want := render(scans)

	rng := mrand.New(mrand.NewPCG(1, 2)) //nolint:gosec // A seeded generator makes the shuffle repeatable.

	for n := 0; n < 5; n++ {
		shuffled := append([]InstanceScan(nil), scans...)
		rng.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })

		if got := render(shuffled); got != want {
			t.Fatalf("shuffle %d changed the response", n)
		}
	}
}

func TestFleetItemServesPublicPEM(t *testing.T) {
	t.Parallel()

	scans, ca := sharedCAFleet(t)
	fleet := Aggregate(scans, DefaultSettings(), fleetCaps, nil, fleetNow)

	detail, ok := fleet.Item(fleet.Certificates()[0].ID)
	if !ok {
		t.Fatal("Item did not find the certificate")
	}

	if strings.Contains(detail.PEM, "PRIVATE KEY") || !strings.Contains(detail.PEM, "BEGIN CERTIFICATE") {
		t.Fatalf("unexpected PEM %q", detail.PEM)
	}

	parsed, err := ParsePEMCertificates([]byte(detail.PEM))
	if err != nil || len(parsed) != 1 || !parsed[0].Equal(ca.cert) {
		t.Fatalf("PEM does not parse back to the CA: %v", err)
	}

	if detail.Facts.CommonName != "services-ca" {
		t.Errorf("facts common name %q", detail.Facts.CommonName)
	}

	if _, ok := fleet.Item("nope"); ok {
		t.Error("Item found an unknown ID")
	}
}

func TestFleetRemovingOneInstance(t *testing.T) {
	t.Parallel()

	scans, _ := sharedCAFleet(t)
	full := Aggregate(scans, DefaultSettings(), fleetCaps, nil, fleetNow).Certificates()[0]
	less := Aggregate(scans[1:], DefaultSettings(), fleetCaps, nil, fleetNow).Certificates()[0]

	if less.AffectedInstances != 39 || less.ID != full.ID {
		t.Errorf("affected %d, id %q, want 39 and %q", less.AffectedInstances, less.ID, full.ID)
	}
}

func TestFleetDeployPendingFinding(t *testing.T) {
	t.Parallel()

	leaf := issue(t, issueSpec{cn: "redis", serial: 3})
	s := fleetScan("i1", fleetNow)
	s.Material.CredHub = []CredHubCert{{
		Name: "/ws-bosh/redis-i1/crt", Owner: instOwner("i1"), Deployed: leaf.cert,
		Versions: Versions{DeployedVersionID: "v1", LatestVersionID: "v2"},
	}}

	c := Aggregate([]InstanceScan{s}, DefaultSettings(), fleetCaps, nil, fleetNow).Certificates()[0]
	if !hasFinding(c, FindingDeployPending) {
		t.Fatalf("no deploy_pending in %+v", c.Findings)
	}
}

func TestFleetCoverageFromExpectedListeners(t *testing.T) {
	t.Parallel()

	leaf := issue(t, issueSpec{cn: "redis", serial: 3})
	owner := instOwner("i1")
	name := "/ws-bosh/redis-i1/redis_crt"
	listener := &Listener{Group: "redis", Job: "redis", Variable: "redis_crt", Port: 6379, Name: "tls"}

	s := fleetScan("i1", fleetNow)
	s.Material.CredHub = []CredHubCert{{Name: name, Owner: owner, Deployed: leaf.cert}}

	for n := 0; n < 3; n++ {
		s.Material.Expected = append(s.Material.Expected, ExpectedListener{Key: name, Locator: fmt.Sprintf("redis/%d:6379", n)})
	}

	for n := 0; n < 2; n++ {
		r := rawObs(SourceProbe, owner, fmt.Sprintf("redis/%d 10.0.0.%d:6379", n, n), leaf.cert)
		r.Listener = listener
		s.Material.Raw = append(s.Material.Raw, r)
	}

	failed := RawObservation{
		Observation: Observation{Source: SourceProbe, Owner: owner, Locator: "redis/2 10.0.0.9:6379", State: StateFailed, Error: "timeout"},
		Listener:    listener,
	}
	s.Material.Raw = append(s.Material.Raw, failed)

	c := Aggregate([]InstanceScan{s}, DefaultSettings(), fleetCaps, nil, fleetNow).Certificates()[0]
	if c.Coverage.ExpectedListeners != 3 || c.Coverage.ObservedListeners != 2 || c.Coverage.FailedListeners != 1 {
		t.Errorf("coverage %+v, want expected 3, observed 2, failed 1", c.Coverage)
	}

	if c.Coverage.Complete {
		t.Error("coverage reads complete with a failed probe")
	}
}

func TestFleetFindingsSeeRepresentatives(t *testing.T) {
	t.Parallel()

	short := issue(t, issueSpec{cn: "short-ca", serial: 1, isCA: true, notAfter: 12 * time.Hour})
	weakKey, err := rsa.GenerateKey(rand.Reader, 1024) //nolint:gosec // A deliberately weak key exercises the weak-key finding.

	if err != nil {
		t.Fatal(err)
	}

	leaf, _ := makeCert(t, certSpec{cn: "weak-leaf", subject: &weakKey.PublicKey, parent: short.cert, parentK: short.key, serial: 7})

	s := fleetScan("i1", fleetNow)
	s.Material.Raw = []RawObservation{
		rawObs(SourceManifest, instOwner("i1"), "instance_groups[0].properties.ca", short.cert),
		rawObs(SourceManifest, instOwner("i1"), "instance_groups[0].properties.crt", leaf),
	}

	fleet := Aggregate([]InstanceScan{s}, DefaultSettings(), fleetCaps, nil, fleetNow)
	c := findCert(t, fleet.Certificates(), "sha256:"+Fingerprint(leaf))

	for _, code := range []string{FindingWeakKey, FindingIssuerExpiresFirst} {
		if !hasFinding(c, code) {
			t.Errorf("leaf lacks %s: %+v", code, c.Findings)
		}
	}
}

func TestFleetRotatabilityUsesOwningInstance(t *testing.T) {
	t.Parallel()

	leaf := issue(t, issueSpec{cn: "redis", serial: 3})
	s := fleetScan("i1", fleetNow)
	s.VMs = []bosh.VM{{Job: "redis", Index: 0}, {Job: "redis", Index: 1}, {Job: "redis", Index: 2}}
	s.Material.CredHub = []CredHubCert{{Name: "/ws-bosh/redis-i1/crt", Owner: instOwner("i1"), Deployed: leaf.cert}}

	caps := fleetCaps
	caps.PatchEnabled = true

	c := Aggregate([]InstanceScan{s}, DefaultSettings(), caps, nil, fleetNow).Certificates()[0]
	if !c.Rotation.Allowed || !reflect.DeepEqual(c.Rotation.Modes, []string{ModeRedeploy}) {
		t.Fatalf("rotation %+v, want redeploy only on a three-VM instance with no gate", c.Rotation)
	}

	s.HealthGate = "redis-cluster"

	c = Aggregate([]InstanceScan{s}, DefaultSettings(), caps, nil, fleetNow).Certificates()[0]
	if !reflect.DeepEqual(c.Rotation.Modes, []string{ModeRedeploy, ModePatch}) {
		t.Fatalf("rotation %+v, want patch with a gate", c.Rotation)
	}
}

func TestFleetExpiryAndAttention(t *testing.T) {
	t.Parallel()

	soon := issue(t, issueSpec{cn: "soon", serial: 3, notAfter: 3*24*time.Hour + time.Hour})

	s := fleetScan("i1", time.Now())
	s.Material.CredHub = []CredHubCert{{Name: "/ws-bosh/redis-i1/crt", Owner: instOwner("i1"), Deployed: soon.cert}}

	c := Aggregate([]InstanceScan{s}, DefaultSettings(), fleetCaps, nil, time.Now()).Certificates()[0]
	if c.Expiry != ExpiryCritical || c.DaysLeft == nil || *c.DaysLeft != 3 || !c.Attention {
		t.Errorf("expiry %q days %v attention %v, want critical, 3, true", c.Expiry, c.DaysLeft, c.Attention)
	}

	unread := fleetScan("i2", time.Now())
	unread.Material.CredHub = []CredHubCert{{Name: "/ws-bosh/redis-i2/crt", Owner: instOwner("i2"), Err: "CredHub not configured"}}

	c = Aggregate([]InstanceScan{unread}, DefaultSettings(), fleetCaps, nil, time.Now()).Certificates()[0]
	if c.Expiry != ExpiryUnknown || c.ExpiryReason != "CredHub not configured" {
		t.Errorf("expiry %q reason %q, want unknown with the read error", c.Expiry, c.ExpiryReason)
	}
}

func TestFleetCredHubTieGoesToLowestInstanceID(t *testing.T) {
	t.Parallel()

	first := issue(t, issueSpec{cn: "from-a", serial: 1})
	second := issue(t, issueSpec{cn: "from-b", serial: 2})
	name := "/ws-bosh/shared/crt"
	at := fleetNow.Add(-time.Minute)

	a := fleetScan("a", at)
	a.Material.CredHub = []CredHubCert{{Name: name, Owner: instOwner("a"), Deployed: first.cert}}
	b := fleetScan("b", at)
	b.Material.CredHub = []CredHubCert{{Name: name, Owner: instOwner("b"), Deployed: second.cert}}

	// The order of the input must not matter either.
	for _, scans := range [][]InstanceScan{{a, b}, {b, a}} {
		certs := Aggregate(scans, DefaultSettings(), fleetCaps, nil, fleetNow).Certificates()
		if len(certs) != 1 {
			t.Fatalf("got %d certificates, want 1", len(certs))
		}

		if got := certs[0].CommonName; got != "from-a" {
			t.Errorf("tie chose %q, want the certificate from the lowest instance ID", got)
		}

		if len(certs[0].Owners) != 2 {
			t.Errorf("owners %v, want both instances", certs[0].Owners)
		}
	}
}

func TestFleetStaleScanMarksRawObservations(t *testing.T) {
	t.Parallel()

	leaf := issue(t, issueSpec{cn: "literal", serial: 4})
	old := fleetNow.Add(-6 * time.Hour)

	stale := fleetScan("i1", old)
	stale.Stale = true
	stale.Material.Raw = []RawObservation{rawObs(SourceManifest, instOwner("i1"), "instance_groups[0].properties.crt", leaf.cert)}

	certs := Aggregate([]InstanceScan{stale}, DefaultSettings(), fleetCaps, nil, fleetNow).Certificates()
	if len(certs) != 1 {
		t.Fatalf("got %d certificates, want 1", len(certs))
	}

	states := map[Source]string{}
	for _, o := range certs[0].Observations {
		states[o.Source] = o.State
	}

	if states[SourceManifest] != StateStale {
		t.Errorf("manifest observation state %q, want stale", states[SourceManifest])
	}

	// A fresh scan of the same certificate keeps its own state.
	fresh := fleetScan("i2", fleetNow.Add(-time.Minute))
	fresh.Material.Raw = []RawObservation{rawObs(SourceManifest, instOwner("i2"), "instance_groups[0].properties.crt", leaf.cert)}

	merged := Aggregate([]InstanceScan{stale, fresh}, DefaultSettings(), fleetCaps, nil, fleetNow).Certificates()[0]

	counts := map[string]int{}
	for _, o := range merged.Observations {
		counts[o.State]++
	}

	if counts[StateStale] != 1 || counts[StateOK] != 1 {
		t.Errorf("states after merging a stale and a fresh scan = %v, want one stale and one ok", counts)
	}
}

func TestFleetDivergenceFields(t *testing.T) {
	t.Parallel()

	const (
		pending  = "/ws-bosh/redis-i1/pending_crt"
		reverted = "/ws-bosh/redis-i1/reverted_crt"
	)

	since := fleetNow.Add(-2 * time.Hour)
	owner := instOwner("i1")
	s := fleetScan("i1", fleetNow)

	for n, name := range []string{pending, reverted} {
		leaf := issue(t, issueSpec{cn: lastSegment(name), serial: int64(10 + n)})
		s.Material.CredHub = append(s.Material.CredHub, CredHubCert{Name: name, Owner: owner, Deployed: leaf.cert,
			Versions: Versions{DeployedVersionID: "v1", LatestVersionID: "v2"}})
	}

	div := map[string]*Divergence{"i1": {InstanceID: "i1", Revision: 4, Variables: []DivergentVariable{
		{Name: pending, BOSHVersionID: "v1", AppliedVersionID: "v2", TaskID: "task-7", Since: since, State: DivergenceUnconverged,
			VMs: map[string]DivergentVM{"vm-a": {State: DivergentVMApplied}}},
		{Name: reverted, BOSHVersionID: "v1", AppliedVersionID: "v2", TaskID: "task-8", Since: since, State: DivergenceUnconverged,
			VMs: map[string]DivergentVM{"vm-a": {State: DivergentVMApplied}, "vm-b": {State: DivergentVMReverted}}},
	}}}

	fleet := Aggregate([]InstanceScan{s}, DefaultSettings(), fleetCaps, div, fleetNow)

	byName := map[string]Certificate{}
	for _, c := range fleet.Certificates() {
		byName[c.Key] = c
	}

	p, r := byName[pending], byName[reverted]

	if p.Divergence == nil || !reflect.DeepEqual(*p.Divergence, DivergenceRef{State: DivergenceUnconverged, TaskID: "task-7", Since: since}) {
		t.Errorf("pending divergence %+v", p.Divergence)
	}

	if !hasFinding(p, FindingUnconverged) || hasFinding(p, FindingPatchReverted) {
		t.Errorf("pending findings %+v", p.Findings)
	}

	if r.Divergence == nil || r.Divergence.State != DivergenceReverted || r.Divergence.TaskID != "task-8" {
		t.Errorf("reverted divergence %+v", r.Divergence)
	}

	if !hasFinding(r, FindingPatchReverted) {
		t.Errorf("reverted findings %+v", r.Findings)
	}

	for _, f := range append(append([]Finding(nil), p.Findings...), r.Findings...) {
		switch f.Code {
		case FindingUnconverged:
			if f.Severity != SeverityWarning {
				t.Errorf("unconverged severity %q", f.Severity)
			}
		case FindingPatchReverted:
			if f.Severity != SeverityError {
				t.Errorf("patch_reverted severity %q", f.Severity)
			}
		}
	}

	if !p.Attention || !r.Attention {
		t.Errorf("attention %v and %v, want both", p.Attention, r.Attention)
	}

	resp := BuildResponse(fleet, Filter{}, DefaultSettings(), fleetCaps, ScanState{}, fleetNow)

	// Summarize counts the unconverged state only, so the reverted
	// certificate counts as needing attention and not as unconverged.
	if resp.Summary.Unconverged != 1 || resp.Summary.NeedsAttention != 2 {
		t.Errorf("summary %+v", resp.Summary)
	}

	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}

	if n := strings.Count(string(raw), `"divergence":`); n != 2 {
		t.Errorf("the response carries %d divergence fields, want 2", n)
	}

	if !strings.Contains(string(raw), `"unconverged":1`) {
		t.Errorf("the response summary lacks the unconverged count: %s", raw)
	}

	if c := Aggregate([]InstanceScan{s}, DefaultSettings(), fleetCaps, nil, fleetNow).Certificates()[0]; c.Divergence != nil || hasFinding(c, FindingUnconverged) {
		t.Errorf("a fleet without records gave %+v", c)
	}
}

// divergedMismatches aggregates one redis instance whose redis/0 and
// redis/1 serve servedA and servedB, with a divergence entry that lists
// only vm-a, and returns the certificate's served_mismatch messages.
func divergedMismatches(t *testing.T, deployed, servedA, servedB issued, appliedFingerprint string) []string {
	t.Helper()

	const name = "/ws-bosh/redis-i1/redis_crt"

	owner := instOwner("i1")
	listener := &Listener{Group: "redis", Job: "redis", Variable: "redis_crt", Port: 6379, Name: "tls"}

	s := fleetScan("i1", fleetNow)
	s.VMs = []bosh.VM{
		{CID: "vm-a", Job: "redis", Index: 0, IPs: []string{"10.0.0.1"}},
		{CID: "vm-b", Job: "redis", Index: 1, IPs: []string{"10.0.0.2"}},
	}
	s.Material.CredHub = []CredHubCert{{Name: name, Owner: owner, Deployed: deployed.cert,
		Versions: Versions{DeployedVersionID: "v1", LatestVersionID: "v2"}}}
	s.Material.Expected = []ExpectedListener{{Key: name, Locator: "redis/0:6379"}, {Key: name, Locator: "redis/1:6379"}}

	for _, served := range []struct {
		locator string
		cert    issued
	}{{"redis/0 10.0.0.1:6379", servedA}, {"redis/1 10.0.0.2:6379", servedB}} {
		r := rawObs(SourceProbe, owner, served.locator, served.cert.cert)
		r.Listener = listener
		s.Material.Raw = append(s.Material.Raw, r)
	}

	div := map[string]*Divergence{"i1": {InstanceID: "i1", Variables: []DivergentVariable{{
		Name: name, BOSHVersionID: "v1", AppliedVersionID: "v2", AppliedFingerprint: appliedFingerprint,
		TaskID: "task-7", State: DivergenceUnconverged,
		VMs: map[string]DivergentVM{"vm-a": {State: DivergentVMApplied}},
	}}}}

	var c Certificate

	for _, cert := range Aggregate([]InstanceScan{s}, DefaultSettings(), fleetCaps, div, fleetNow).Certificates() {
		if cert.Key == name {
			c = cert
		}
	}

	if !hasFinding(c, FindingUnconverged) {
		t.Errorf("no unconverged finding in %+v", c.Findings)
	}

	var mismatched []string

	for _, f := range c.Findings {
		if f.Code == FindingServedMismatch {
			mismatched = append(mismatched, f.Message)
		}
	}

	return mismatched
}

func TestFleetDivergenceSuppressesServedMismatch(t *testing.T) {
	t.Parallel()

	deployed := issue(t, issueSpec{cn: "redis", serial: 1})
	patched := issue(t, issueSpec{cn: "redis", serial: 2})
	stranger := issue(t, issueSpec{cn: "stranger", serial: 3})

	got := divergedMismatches(t, deployed, patched, stranger, Fingerprint(patched.cert))
	if len(got) != 1 || !strings.Contains(got[0], "10.0.0.2") {
		t.Fatalf("served_mismatch findings %q, want one for the VM the record does not list", got)
	}

	// A listed VM that serves neither the deployed nor the applied
	// certificate is still a mismatch.
	got = divergedMismatches(t, deployed, stranger, deployed, Fingerprint(patched.cert))
	if len(got) != 1 || !strings.Contains(got[0], "10.0.0.1") {
		t.Errorf("served_mismatch findings %q, want one for the listed VM serving a third certificate", got)
	}

	// Without the applied fingerprint, nothing shows the listed VM serves
	// the patch, so its mismatch stays.
	got = divergedMismatches(t, deployed, patched, deployed, "")
	if len(got) != 1 || !strings.Contains(got[0], "10.0.0.1") {
		t.Errorf("served_mismatch findings %q, want the listed VM's with no applied fingerprint", got)
	}
}
