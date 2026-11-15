package certinventory

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
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

// TestFleetCredHubObservationShowsLatest checks that the CredHub
// observation describes CredHub's latest version, while the expiry,
// findings, health, and summary still follow the deployed version, both
// when the deployment runs the latest version and after a patch in place
// that BOSH has not yet deployed.
func TestFleetCredHubObservationShowsLatest(t *testing.T) {
	t.Parallel()

	const name = "/ws-bosh/valkey-i1/valkey_cluster_crt"

	deployed := issue(t, issueSpec{cn: "valkey", serial: 0x1c4cf777, notAfter: 2000 * time.Hour})
	latest := issue(t, issueSpec{cn: "valkey", serial: 0x29b11348, notAfter: 3000 * time.Hour})

	rows := []struct {
		name         string
		latest       issued
		latestID     string
		notAfter     bool // whether the scan recorded DeployedNotAfter
		divergence   bool
		matches      string
		findings     []string
		health       Health
		unconverged  int
		attention    int
		wantNotAfter time.Time
	}{
		{name: "converged", latest: deployed, latestID: "24528b18", notAfter: true,
			matches: MatchDeployed, health: HealthOK},
		{name: "patched", latest: latest, latestID: "ae5bcf1e", notAfter: true, divergence: true,
			matches: MatchLatest, findings: []string{FindingUnconverged, FindingDeployPending},
			health: HealthWarning, unconverged: 1, attention: 1},
		{name: "patched without a deployed expiry", latest: latest, latestID: "ae5bcf1e", divergence: true,
			matches: MatchLatest, findings: []string{FindingUnconverged, FindingDeployPending},
			health: HealthWarning, unconverged: 1, attention: 1},
	}

	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			t.Parallel()

			s := fleetScan("i1", fleetNow)
			ch := CredHubCert{
				Name: name, Owner: instOwner("i1"), Deployed: deployed.cert, Latest: r.latest.cert,
				Versions: Versions{DeployedVersionID: "24528b18", LatestVersionID: r.latestID},
			}

			if r.notAfter {
				dn, ln := deployed.cert.NotAfter, r.latest.cert.NotAfter
				ch.Versions.DeployedNotAfter, ch.Versions.LatestNotAfter = &dn, &ln
			}

			s.Material.CredHub = []CredHubCert{ch}

			var div map[string]*Divergence
			if r.divergence {
				div = map[string]*Divergence{"i1": {InstanceID: "i1", Revision: 1, Variables: []DivergentVariable{{
					Name: name, BOSHVersionID: "24528b18", AppliedVersionID: r.latestID, TaskID: "task-1",
					Since: fleetNow.Add(-time.Hour), State: DivergenceUnconverged,
					VMs: map[string]DivergentVM{"vm-a": {State: DivergentVMApplied}},
				}}}}
			}

			fleet := Aggregate([]InstanceScan{s}, DefaultSettings(), fleetCaps, div, fleetNow)
			c := fleet.Certificates()[0]

			var obs []Observation

			for _, o := range c.Observations {
				if o.Source == SourceCredHub {
					obs = append(obs, o)
				}
			}

			if len(obs) != 1 {
				t.Fatalf("got %d CredHub observations, want 1: %+v", len(obs), obs)
			}

			o := obs[0]
			if o.VersionID != r.latestID || o.Serial != r.latest.cert.SerialNumber.Text(16) ||
				o.Fingerprint != Fingerprint(r.latest.cert) || o.Matches != r.matches {
				t.Errorf("CredHub observation version %q, serial %q, matches %q, want %q, %q, %q",
					o.VersionID, o.Serial, o.Matches, r.latestID, r.latest.cert.SerialNumber.Text(16), r.matches)
			}

			if o.NotAfter == nil || !o.NotAfter.Equal(r.latest.cert.NotAfter) {
				t.Errorf("CredHub observation not_after %v, want the latest version's %v", o.NotAfter, r.latest.cert.NotAfter)
			}

			var codes []string
			for _, f := range c.Findings {
				codes = append(codes, f.Code)
			}

			if !reflect.DeepEqual(codes, r.findings) {
				t.Errorf("findings %v, want %v", codes, r.findings)
			}

			if c.Health != r.health {
				t.Errorf("health %q, want %q", c.Health, r.health)
			}

			if c.EffectiveNotAfter == nil || !c.EffectiveNotAfter.Equal(deployed.cert.NotAfter) {
				t.Errorf("effective expiry %v, want the deployed version's %v", c.EffectiveNotAfter, deployed.cert.NotAfter)
			}

			sum := BuildResponse(fleet, Filter{}, DefaultSettings(), fleetCaps, ScanState{}, fleetNow).Summary
			if sum.Unconverged != r.unconverged || sum.NeedsAttention != r.attention || sum.ExpiryOK != 1 {
				t.Errorf("summary %+v", sum)
			}
		})
	}
}

// TestFleetItemFingerprintIsRepresentative checks that after a patch in
// place the item's facts carry the fingerprint of the certificate whose PEM
// and serial they show, which is the deployed version, and not the latest
// version that the CredHub observation describes.
func TestFleetItemFingerprintIsRepresentative(t *testing.T) {
	t.Parallel()

	deployed := issue(t, issueSpec{cn: "valkey", serial: 1})
	latest := issue(t, issueSpec{cn: "valkey", serial: 2})

	s := fleetScan("i1", fleetNow)
	s.Material.CredHub = []CredHubCert{{
		Name: "/ws-bosh/valkey-i1/valkey_crt", Owner: instOwner("i1"), Deployed: deployed.cert, Latest: latest.cert,
		Versions: Versions{DeployedVersionID: "v1", LatestVersionID: "v2"},
	}}

	fleet := Aggregate([]InstanceScan{s}, DefaultSettings(), fleetCaps, nil, fleetNow)

	detail, ok := fleet.Item(fleet.Certificates()[0].ID)
	if !ok {
		t.Fatal("no item")
	}

	if detail.Facts.SHA256Fingerprint != Fingerprint(deployed.cert) || detail.Facts.Serial != deployed.cert.SerialNumber.Text(16) {
		t.Errorf("facts fingerprint %q, serial %q, want the deployed version's", detail.Facts.SHA256Fingerprint, detail.Facts.Serial)
	}
}

// TestFleetStaleCredHubObservationStaysWithItsCertificate checks that the
// stale observation a failed rescan carries stays with the certificate it
// was read for, even when its fingerprint is another certificate's
// deployed version. Instance i1 deploys X with Y as its latest version,
// and instance i2 deploys Y.
func TestFleetStaleCredHubObservationStaysWithItsCertificate(t *testing.T) {
	t.Parallel()

	const (
		nameA = "/ws-bosh/valkey-i1/valkey_crt"
		nameB = "/ws-bosh/valkey-i2/valkey_crt"
	)

	x := issue(t, issueSpec{cn: "valkey", serial: 1})
	y := issue(t, issueSpec{cn: "valkey", serial: 2})
	good := fleetNow.Add(-time.Hour)

	prev := fleetScan("i1", good)
	prev.Material.CredHub = []CredHubCert{{
		Name: nameA, Owner: instOwner("i1"), Deployed: x.cert, Latest: y.cert,
		Versions: Versions{DeployedVersionID: "v1", LatestVersionID: "v2"},
	}}

	a := fleetScan("i1", fleetNow)
	a.Material = keepLastGood(ScanMaterial{CredHub: []CredHubCert{{Name: nameA, Owner: instOwner("i1"), Err: msgCredHubUnavailable}}}, prev)

	b := fleetScan("i2", fleetNow)
	b.Material.CredHub = []CredHubCert{{
		Name: nameB, Owner: instOwner("i2"), Deployed: y.cert, Latest: y.cert,
		Versions: Versions{DeployedVersionID: "w1", LatestVersionID: "w1"},
	}}

	fleet := Aggregate([]InstanceScan{a, b}, DefaultSettings(), fleetCaps, nil, fleetNow)

	byName := map[string]Certificate{}
	for _, c := range fleet.Certificates() {
		byName[c.Key] = c
	}

	stale := func(c Certificate) int {
		n := 0

		for _, o := range c.Observations {
			if o.Source == SourceCredHub && o.State == StateStale {
				n++
			}
		}

		return n
	}

	certA, certB := byName[nameA], byName[nameB]

	if stale(certA) != 1 || !certA.Coverage.Stale {
		t.Errorf("i1's certificate has %d stale observations, coverage %+v, want 1 and stale", stale(certA), certA.Coverage)
	}

	if stale(certB) != 0 || certB.Coverage.Stale || len(certB.Owners) != 1 || certB.Owners[0].InstanceID != "i2" {
		t.Errorf("i2's certificate took %d stale observations, owners %+v", stale(certB), certB.Owners)
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

const (
	chainLeafName  = "/ws-bosh/redis-i1/leaf_crt"
	chainMidName   = "mid-ca"
	chainRootName  = "root-ca"
	chainOtherName = "other-ca"
	chainCAVar     = "/ws-bosh/ca"
	chainLeafCN    = "leaf"
)

// chainFleet aggregates one instance scan whose leaf names signer as its
// signing CA, with cas as the CAs that scan read.
func chainFleet(t *testing.T, leaf *x509.Certificate, signer string, cas map[string]*x509.Certificate, read bool) (*Fleet, string) {
	t.Helper()

	scan := fleetScan("i1", fleetNow.Add(-time.Minute))
	scan.Material.CAsRead = read
	scan.Material.SigningCAs = cas
	scan.Material.CredHub = []CredHubCert{{
		Name: chainLeafName, Owner: instOwner("i1"), Deployed: leaf,
		Versions: Versions{SigningCA: signer},
	}}

	fleet := Aggregate([]InstanceScan{scan}, DefaultSettings(), fleetCaps, nil, fleetNow)

	for _, cert := range fleet.Certificates() {
		if cert.Variable == "leaf_crt" || strings.HasSuffix(cert.Key, "leaf_crt") {
			return fleet, cert.ID
		}
	}

	t.Fatal("leaf not in fleet")

	return nil, ""
}

func chainSubjects(t *testing.T, pems []string) []string {
	t.Helper()

	out := make([]string, 0, len(pems))

	for _, pemText := range pems {
		if strings.Contains(pemText, "PRIVATE KEY") {
			t.Fatal("chain holds key material")
		}

		certs, err := ParsePEMCertificates([]byte(pemText))
		if err != nil || len(certs) != 1 {
			t.Fatalf("chain entry does not parse: %v", err)
		}

		out = append(out, certs[0].Subject.CommonName)
	}

	return out
}

func TestItemIssuerChainVerifiedOrdered(t *testing.T) {
	t.Parallel()

	root := issue(t, issueSpec{cn: chainRootName, serial: 1, isCA: true})
	mid := issue(t, issueSpec{cn: chainMidName, serial: 2, isCA: true, parent: &root})
	stray := issue(t, issueSpec{cn: "stray-ca", serial: 3, isCA: true})
	leaf := issue(t, issueSpec{cn: chainLeafCN, serial: 4, parent: &mid})

	cas := map[string]*x509.Certificate{
		"/ws-bosh/mid": mid.cert, "/ws-bosh/root": root.cert, "/ws-bosh/stray": stray.cert,
	}

	fleet, id := chainFleet(t, leaf.cert, "/ws-bosh/mid", cas, true)

	item, ok := fleet.Item(id)
	if !ok || item.IssuerChain == nil {
		t.Fatalf("chain not known: ok=%v", ok)
	}

	if item.IssuerChainIncomplete {
		t.Error("a chain ending in a self-signed root is complete")
	}

	if item.IssuerChainFacts == nil || len(*item.IssuerChainFacts) != 2 ||
		(*item.IssuerChainFacts)[0].CommonName != chainMidName || (*item.IssuerChainFacts)[1].NotAfter.IsZero() {
		t.Fatalf("facts do not describe the chain: %v", item.IssuerChainFacts)
	}

	got := chainSubjects(t, *item.IssuerChain)
	if len(got) != 2 || got[0] != chainMidName || got[1] != chainRootName {
		t.Fatalf("chain = %v, want [mid-ca root-ca]", got)
	}
}

func TestItemIssuerChainMissingCAIsUnknown(t *testing.T) {
	t.Parallel()

	ca := issue(t, issueSpec{cn: "ca", serial: 1, isCA: true})
	leaf := issue(t, issueSpec{cn: chainLeafCN, serial: 2, parent: &ca})

	fleet, id := chainFleet(t, leaf.cert, chainCAVar, nil, true)

	item, _ := fleet.Item(id)
	if item.IssuerChain != nil || item.IssuerChainFacts != nil {
		t.Fatalf("unreadable CA must leave the chain unknown, got %v", *item.IssuerChain)
	}

	raw, err := json.Marshal(item)
	if err != nil || strings.Contains(string(raw), "issuer_chain") {
		t.Fatalf("unknown chain must be omitted from JSON: %v %s", err, raw)
	}
}

func TestItemIssuerChainNotScannedUnderNewCodeIsUnknown(t *testing.T) {
	t.Parallel()

	ca := issue(t, issueSpec{cn: "ca", serial: 1, isCA: true})
	leaf := issue(t, issueSpec{cn: chainLeafCN, serial: 2, parent: &ca})

	fleet, id := chainFleet(t, leaf.cert, chainCAVar, map[string]*x509.Certificate{chainCAVar: ca.cert}, false)

	if item, _ := fleet.Item(id); item.IssuerChain != nil {
		t.Fatal("a scan that did not record CA reads must leave the chain unknown")
	}
}

func TestItemIssuerChainCAThatDoesNotSignLeafIsUnknown(t *testing.T) {
	t.Parallel()

	ca := issue(t, issueSpec{cn: "ca", serial: 1, isCA: true})
	other := issue(t, issueSpec{cn: chainOtherName, serial: 2, isCA: true})
	leaf := issue(t, issueSpec{cn: chainLeafCN, serial: 3, parent: &ca})

	fleet, id := chainFleet(t, leaf.cert, "/ws-bosh/other", map[string]*x509.Certificate{"/ws-bosh/other": other.cert}, true)

	if item, _ := fleet.Item(id); item.IssuerChain != nil {
		t.Fatalf("no verifying CA and no proof of none must leave the chain unknown, got %v", *item.IssuerChain)
	}
}

func TestItemIssuerChainProvenNoneIsEmpty(t *testing.T) {
	t.Parallel()

	root := issue(t, issueSpec{cn: "self", serial: 1, isCA: true})

	// A variable that names no signing CA, read by a scan that reached CredHub.
	fleet, id := chainFleet(t, root.cert, "", nil, true)

	item, _ := fleet.Item(id)
	if item.IssuerChain == nil || len(*item.IssuerChain) != 0 {
		t.Fatalf("a variable with no signer must give a known empty chain, got %v", item.IssuerChain)
	}

	raw, err := json.Marshal(item)
	if err != nil || !strings.Contains(string(raw), `"issuer_chain":[]`) {
		t.Fatalf("known empty chain must serialise as []: %v %s", err, raw)
	}
}

func TestItemIssuerChainVerifiesAgainstEveryCAVersion(t *testing.T) {
	t.Parallel()

	oldCA := issue(t, issueSpec{cn: "old-ca", serial: 1, isCA: true})
	newCA := issue(t, issueSpec{cn: "new-ca", serial: 2, isCA: true})
	leaf := issue(t, issueSpec{cn: chainLeafCN, serial: 3, parent: &oldCA})

	scan := fleetScan("i1", fleetNow.Add(-time.Minute))
	scan.Material.CAsRead = true
	scan.Material.SigningCAs = map[string]*x509.Certificate{chainCAVar: newCA.cert}
	scan.Material.CAVersions = map[string][]*x509.Certificate{chainCAVar: {newCA.cert, oldCA.cert}}
	scan.Material.CredHub = []CredHubCert{{
		Name: chainLeafName, Owner: instOwner("i1"), Deployed: leaf.cert, Versions: Versions{SigningCA: chainCAVar},
	}}

	fleet := Aggregate([]InstanceScan{scan}, DefaultSettings(), fleetCaps, nil, fleetNow)

	item, _ := fleet.Item(fleet.Certificates()[0].ID)
	if item.IssuerChain == nil {
		t.Fatal("a leaf signed by the previous CA version must find it")
	}

	if got := chainSubjects(t, *item.IssuerChain); len(got) != 1 || got[0] != "old-ca" {
		t.Fatalf("chain = %v, want [old-ca]", got)
	}

	if item.IssuerChainIncomplete {
		t.Error("a chain ending in a self-signed root is complete")
	}
}

func TestItemIssuerChainEndingShortOfRootIsIncomplete(t *testing.T) {
	t.Parallel()

	root := issue(t, issueSpec{cn: chainRootName, serial: 1, isCA: true})
	mid := issue(t, issueSpec{cn: chainMidName, serial: 2, isCA: true, parent: &root})
	leaf := issue(t, issueSpec{cn: chainLeafCN, serial: 3, parent: &mid})

	fleet, id := chainFleet(t, leaf.cert, "/ws-bosh/mid", map[string]*x509.Certificate{"/ws-bosh/mid": mid.cert}, true)

	item, _ := fleet.Item(id)
	if item.IssuerChain == nil || len(*item.IssuerChain) != 1 || !item.IssuerChainIncomplete {
		t.Fatalf("chain %v incomplete %v, want one entry marked incomplete", item.IssuerChain, item.IssuerChainIncomplete)
	}

	raw, err := json.Marshal(item)
	if err != nil || !strings.Contains(string(raw), `"issuer_chain_incomplete":true`) {
		t.Fatalf("incomplete flag missing from JSON: %s", raw)
	}
}

// carriedChain aggregates the scan the scheduler would store after prev when
// next could not read the CAs.
func carriedChain(t *testing.T, next ScanMaterial) *[]string {
	t.Helper()

	root := issue(t, issueSpec{cn: chainRootName, serial: 1, isCA: true})
	leaf := issue(t, issueSpec{cn: chainLeafCN, serial: 2, parent: &root})
	name := "/ws-bosh/root"

	prev := fleetScan("i1", fleetNow.Add(-2*time.Minute))
	prev.Material.CAsRead = true
	prev.Material.SigningCAs = map[string]*x509.Certificate{name: root.cert}
	prev.Material.CAVersions = map[string][]*x509.Certificate{name: {root.cert}}
	prev.Material.CredHub = []CredHubCert{{Name: chainLeafName, Owner: instOwner("i1"), Deployed: leaf.cert, Versions: Versions{SigningCA: name}}}

	// The new scan still names the leaf, but could not read its certificates.
	next.CredHub = []CredHubCert{{Name: chainLeafName, Owner: instOwner("i1"), Err: msgCredHubUnavailable, Versions: Versions{SigningCA: name}}}

	scan := fleetScan("i1", fleetNow.Add(-time.Minute))
	scan.Material = keepLastGood(next, prev)

	fleet := Aggregate([]InstanceScan{scan}, DefaultSettings(), fleetCaps, nil, fleetNow)
	if len(fleet.Certificates()) != 1 {
		t.Fatalf("certificates = %d, want 1", len(fleet.Certificates()))
	}

	item, _ := fleet.Item(fleet.Certificates()[0].ID)

	return item.IssuerChain
}

func TestKeepLastGoodKeepsChainWhenVariableListFailed(t *testing.T) {
	t.Parallel()

	// No CAs, and CAsRead false, as when the director's variable list failed.
	if got := carriedChain(t, ScanMaterial{}); got == nil || len(*got) != 1 {
		t.Fatalf("chain after a failed variable list = %v, want the carried chain", got)
	}
}

func TestKeepLastGoodKeepsChainWhenCredHubWentDown(t *testing.T) {
	t.Parallel()

	// CredHub reachable at the start, down for the CA reads: nothing read.
	if got := carriedChain(t, ScanMaterial{CAsRead: false, SigningCAs: map[string]*x509.Certificate{}}); got == nil || len(*got) != 1 {
		t.Fatalf("chain after a CredHub outage = %v, want the carried chain", got)
	}
}

func TestItemIssuerChainSelfSignedLeafWithoutCAConstraintIsEmpty(t *testing.T) {
	t.Parallel()

	other := issue(t, issueSpec{cn: chainOtherName, serial: 1, isCA: true})
	// No parent and not a CA: a self-signed end-entity certificate.
	leaf := issue(t, issueSpec{cn: chainLeafCN, serial: 2})

	fleet, id := chainFleet(t, leaf.cert, "/ws-bosh/other", map[string]*x509.Certificate{"/ws-bosh/other": other.cert}, true)

	item, _ := fleet.Item(id)
	if item.IssuerChain == nil || len(*item.IssuerChain) != 0 {
		t.Fatalf("a self-signed leaf must give a known empty chain, got %v", item.IssuerChain)
	}
}
