package certinventory

import (
	"crypto/elliptic"
	"crypto/x509"
	"fmt"
	"reflect"
	"testing"
)

func groupLeaf(t *testing.T, cn string, serial int64) *x509.Certificate {
	t.Helper()

	key := ecKey(t, elliptic.P256())
	c, _ := makeCert(t, certSpec{cn: cn, subject: key.Public(), selfKey: key, serial: serial})

	return c
}

func groupCA(t *testing.T, cn string, serial int64) *x509.Certificate {
	t.Helper()

	key := ecKey(t, elliptic.P256())
	c, _ := makeCert(t, certSpec{cn: cn, isCA: true, subject: key.Public(), selfKey: key, serial: serial})

	return c
}

func instOwner(id string) Owner {
	return Owner{Kind: OwnerInstance, InstanceID: id, InstanceName: "node", Deployment: "redis-" + id}
}

func rawObs(src Source, o Owner, locator string, c *x509.Certificate) RawObservation {
	return RawObservation{
		Observation: Observation{Source: src, Owner: o, Locator: locator, State: StateOK},
		Cert:        c,
	}
}

func findCert(t *testing.T, certs []Certificate, key string) Certificate {
	t.Helper()

	for _, c := range certs {
		if c.Key == key {
			return c
		}
	}

	t.Fatalf("no certificate with key %q", key)

	return Certificate{}
}

func TestGroupProbeJoinsCredHubLeaf(t *testing.T) {
	t.Parallel()

	leaf := groupLeaf(t, "redis", 1)
	owner := instOwner("i1")
	ch := CredHubCert{Name: "/d/redis-i1/redis_cluster_crt", Owner: owner, Deployed: leaf}

	var raw []RawObservation

	for n := 0; n < 3; n++ {
		raw = append(raw, rawObs(SourceProbe, owner, fmt.Sprintf("10.0.0.%d:6379", n), leaf))
	}

	certs, _ := Group([]CredHubCert{ch}, raw)
	if len(certs) != 1 {
		t.Fatalf("got %d certificates, want 1", len(certs))
	}

	c := certs[0]
	if len(c.Observations) != 4 {
		t.Fatalf("got %d observations, want 4", len(c.Observations))
	}

	for _, o := range c.Observations {
		if o.Source == SourceProbe && o.Matches != MatchDeployed {
			t.Errorf("probe %s Matches %q, want deployed", o.Locator, o.Matches)
		}
	}

	if c.Key != ch.Name || c.Variable != "redis_cluster_crt" || c.Versions == nil {
		t.Errorf("unexpected identity: key %q variable %q", c.Key, c.Variable)
	}

	if len(c.Owners) != 1 || c.AffectedInstances != 1 {
		t.Errorf("owners %v, affected %d", c.Owners, c.AffectedInstances)
	}
}

func TestGroupLiteralCAAcrossManyInstances(t *testing.T) {
	t.Parallel()

	ca := groupCA(t, "literal-ca", 5)

	var raw []RawObservation

	for n := 0; n < 40; n++ {
		raw = append(raw, rawObs(SourceManifest, instOwner(fmt.Sprintf("i%02d", n)), "instance_groups[0].properties.ca", ca))
	}

	certs, _ := Group(nil, raw)
	if len(certs) != 1 {
		t.Fatalf("got %d certificates, want 1", len(certs))
	}

	c := certs[0]
	if c.AffectedInstances != 40 || len(c.Owners) != 40 || len(c.Observations) != 40 {
		t.Errorf("affected %d, owners %d, observations %d", c.AffectedInstances, len(c.Owners), len(c.Observations))
	}

	if c.Key != "sha256:"+Fingerprint(ca) || c.Kind != KindCA {
		t.Errorf("key %q kind %q", c.Key, c.Kind)
	}
}

func TestGroupServicesCAJoinsCredHubCopy(t *testing.T) {
	t.Parallel()

	ca := groupCA(t, "services-ca", 7)
	broker := Owner{Kind: OwnerBroker}
	ch := CredHubCert{Name: "/d/dep/blacksmith_services_ca", Owner: broker, Deployed: ca}

	raw := []RawObservation{
		rawObs(SourceBroker, broker, "/etc/ca.pem", ca),
		rawObs(SourceManifest, instOwner("r1"), "a.b", ca),
		rawObs(SourceManifest, instOwner("r2"), "a.b", ca),
	}

	certs, _ := Group([]CredHubCert{ch}, raw)
	if len(certs) != 1 || certs[0].Key != ch.Name {
		t.Fatalf("got %d certificates, first key %q", len(certs), certs[0].Key)
	}

	if len(certs[0].Observations) != 4 || certs[0].AffectedInstances != 2 {
		t.Errorf("observations %d, affected %d", len(certs[0].Observations), certs[0].AffectedInstances)
	}
}

func TestGroupLatestVersionMatch(t *testing.T) {
	t.Parallel()

	deployed, latest := groupLeaf(t, "old", 1), groupLeaf(t, "new", 2)
	owner := instOwner("i1")
	ch := CredHubCert{Name: "/d/dep/crt", Owner: owner, Deployed: deployed, Latest: latest}

	certs, _ := Group([]CredHubCert{ch}, []RawObservation{rawObs(SourceProbe, owner, "p", latest)})
	if len(certs) != 1 {
		t.Fatalf("got %d certificates", len(certs))
	}

	for _, o := range certs[0].Observations {
		if o.Source == SourceProbe && o.Matches != MatchLatest {
			t.Errorf("Matches %q, want latest", o.Matches)
		}
	}
}

func TestGroupUnmatchedProbeJoinsListenerVariable(t *testing.T) {
	t.Parallel()

	expected, served := groupLeaf(t, "expected", 1), groupLeaf(t, "served", 2)
	owner := instOwner("i1")
	other := instOwner("i2")
	chs := []CredHubCert{
		{Name: "/d/redis-i1/redis_cluster_crt", Owner: owner, Deployed: expected},
		{Name: "/d/redis-i2/redis_cluster_crt", Owner: other, Deployed: groupLeaf(t, "other", 3)},
	}

	r := rawObs(SourceProbe, owner, "10.0.0.1:6379", served)
	r.Listener = &Listener{Group: "redis", Job: "redis", Variable: "redis_cluster_crt", Port: 6379, Name: "tls"}

	certs, _ := Group(chs, []RawObservation{r})
	if len(certs) != 2 {
		t.Fatalf("got %d certificates, want 2 (no floating certificate)", len(certs))
	}

	c := findCert(t, certs, "/d/redis-i1/redis_cluster_crt")

	var got *Observation

	for i := range c.Observations {
		if c.Observations[i].Source == SourceProbe {
			got = &c.Observations[i]
		}
	}

	if got == nil || got.Matches != MatchOther {
		t.Fatalf("probe observation %+v, want Matches other", got)
	}

	if len(findCert(t, certs, "/d/redis-i2/redis_cluster_crt").Observations) != 1 {
		t.Error("the other instance's leaf took the probe")
	}
}

func TestGroupFailedProbeJoinsListenerVariable(t *testing.T) {
	t.Parallel()

	owner := instOwner("i1")
	ch := CredHubCert{Name: "/d/redis-i1/crt", Owner: owner, Deployed: groupLeaf(t, "x", 1)}
	r := RawObservation{
		Observation: Observation{Source: SourceProbe, Owner: owner, Locator: "10.0.0.1:6379", State: StateFailed, Error: "refused"},
		Listener:    &Listener{Variable: "crt"},
	}

	certs, _ := Group([]CredHubCert{ch}, []RawObservation{r})
	if len(certs) != 1 || len(certs[0].Observations) != 2 {
		t.Fatalf("got %d certificates, observations %d", len(certs), len(certs[0].Observations))
	}
}

func TestGroupOwnersCoverEveryObservation(t *testing.T) {
	t.Parallel()

	leaf := groupLeaf(t, "x", 1)
	ownerA, ownerB := instOwner("a"), instOwner("b")
	platform := Owner{Kind: OwnerPlatform, InstanceName: "director"}
	ch := CredHubCert{Name: "/d/dep/crt", Owner: ownerA, Deployed: leaf}

	certs, _ := Group([]CredHubCert{ch}, []RawObservation{
		rawObs(SourceProbe, ownerB, "p", leaf),
		rawObs(SourceProbe, platform, "q", leaf),
	})

	for _, o := range certs[0].Observations {
		found := false

		for _, w := range certs[0].Owners {
			found = found || w == o.Owner
		}

		if !found {
			t.Errorf("owner %+v of %s is not in Owners", o.Owner, o.Locator)
		}
	}

	if certs[0].AffectedInstances != 2 {
		t.Errorf("affected %d, want 2", certs[0].AffectedInstances)
	}
}

func TestGroupCredHubReadFailureIsFailedObservation(t *testing.T) {
	t.Parallel()

	ch := CredHubCert{Name: "/d/dep/crt", Owner: instOwner("a"), Err: "credhub timed out"}

	certs, reps := Group([]CredHubCert{ch}, nil)
	if len(certs) != 1 || len(certs[0].Observations) != 1 {
		t.Fatalf("got %+v", certs)
	}

	if o := certs[0].Observations[0]; o.State != StateFailed || o.Error != "credhub timed out" || o.NotAfter != nil {
		t.Errorf("observation %+v", o)
	}

	if _, ok := reps[certs[0].ID]; ok {
		t.Error("a certificate with nothing to show has a representative")
	}
}

func TestGroupIDsStable(t *testing.T) {
	t.Parallel()

	leaf, ca := groupLeaf(t, "leaf", 1), groupCA(t, "ca", 2)
	owner := instOwner("i1")
	chs := []CredHubCert{
		{Name: "/d/dep/b", Owner: owner, Deployed: leaf},
		{Name: "/d/dep/a", Owner: owner, Deployed: groupLeaf(t, "a", 3)},
	}
	raw := []RawObservation{
		rawObs(SourceProbe, instOwner("i2"), "p2", leaf),
		rawObs(SourceProbe, owner, "p1", leaf),
		rawObs(SourceManifest, instOwner("i3"), "m", ca),
		rawObs(SourceManifest, instOwner("i4"), "m", ca),
	}

	first, _ := Group(chs, raw)

	revCH := []CredHubCert{chs[1], chs[0]}
	revRaw := make([]RawObservation, len(raw))

	for i, r := range raw {
		revRaw[len(raw)-1-i] = r
	}

	second, _ := Group(revCH, revRaw)

	if !reflect.DeepEqual(first, second) {
		t.Fatalf("grouping changed with input order:\n%+v\n%+v", first, second)
	}

	for i := 1; i < len(first); i++ {
		if first[i-1].ID >= first[i].ID {
			t.Errorf("not sorted by ID at %d", i)
		}
	}

	if want := hashID("credhub:/d/dep/b"); findCert(t, first, "/d/dep/b").ID != want || len(want) != 16 {
		t.Errorf("credhub ID %q", findCert(t, first, "/d/dep/b").ID)
	}
}

func TestGroupKeepsRepresentativeCertificate(t *testing.T) {
	t.Parallel()

	deployed, latest, ca := groupLeaf(t, "dep", 1), groupLeaf(t, "lat", 2), groupCA(t, "ca", 3)
	owner := instOwner("i1")
	ch := CredHubCert{Name: "/d/dep/crt", Owner: owner, Deployed: deployed, Latest: latest}

	certs, reps := Group([]CredHubCert{ch}, []RawObservation{
		rawObs(SourceProbe, owner, "p", latest),
		rawObs(SourceManifest, owner, "m", ca),
	})

	credCert := findCert(t, certs, "/d/dep/crt")
	if got := reps[credCert.ID]; got == nil || !reflect.DeepEqual(got.Raw, deployed.Raw) {
		t.Error("the CredHub leaf's representative is not its deployed version")
	}

	if credCert.CommonName != "dep" {
		t.Errorf("identity read from %q, want the deployed certificate", credCert.CommonName)
	}

	literal := findCert(t, certs, "sha256:"+Fingerprint(ca))
	if got := reps[literal.ID]; got == nil || !reflect.DeepEqual(got.Raw, ca.Raw) {
		t.Error("the literal CA's representative is not the observed certificate")
	}
}

func TestGroupKeepsFailedObservationWithoutListener(t *testing.T) {
	t.Parallel()

	owner := instOwner("i9")
	failed := []RawObservation{
		{Observation: Observation{Source: SourceProbe, Owner: owner, Locator: "10.0.0.9:6379", State: StateFailed, Error: "refused"}},
		{Observation: Observation{Source: SourceProbe, Owner: owner, Locator: "10.0.0.8:6379", State: StateFailed, Error: "refused"}, Listener: &Listener{}},
		{Observation: Observation{Source: SourceBroker, Owner: Owner{Kind: OwnerBroker}, Locator: "/etc/ca.pem", State: StateFailed, Error: "unreadable"}},
	}

	certs, _ := Group(nil, failed)
	if len(certs) != 3 {
		t.Fatalf("got %d certificates, want one per failed observation", len(certs))
	}

	ApplyFindings(certs, FindingInput{})

	c := findCert(t, certs, "10.0.0.9:6379")
	if c.ID != hashID("probe:"+"i9"+"10.0.0.9:6379") {
		t.Errorf("ID %q", c.ID)
	}

	if len(c.Owners) != 1 || c.Owners[0] != owner || c.AffectedInstances != 1 {
		t.Errorf("owners %+v", c.Owners)
	}

	for _, c := range certs {
		if c.Coverage.Complete {
			t.Errorf("%s: coverage complete after a failure", c.Key)
		}
	}
}

func TestGroupFailedProbeWithVariableJoinsVariableGroup(t *testing.T) {
	t.Parallel()

	owner := instOwner("i1")
	ch := CredHubCert{Name: "/d/redis-i1/crt", Owner: owner, Deployed: groupLeaf(t, "x", 1)}
	r := RawObservation{
		Observation: Observation{Source: SourceProbe, Owner: owner, Locator: "p", State: StateFailed, Error: "refused"},
		Listener:    &Listener{Variable: "crt"},
	}

	certs, _ := Group([]CredHubCert{ch}, []RawObservation{r})
	if len(certs) != 1 {
		t.Fatalf("got %d certificates, want the failure inside the variable's group", len(certs))
	}

	ApplyFindings(certs, FindingInput{})

	if certs[0].Coverage.Complete {
		t.Error("coverage complete after a failed probe")
	}
}

func TestGroupFingerprintOwnershipIgnoresInputOrder(t *testing.T) {
	t.Parallel()

	shared := groupLeaf(t, "shared", 1)
	a := CredHubCert{Name: "/d/dep/a", Owner: instOwner("i1"), Deployed: shared}
	b := CredHubCert{Name: "/d/dep/b", Owner: instOwner("i2"), Deployed: shared}
	raw := []RawObservation{rawObs(SourceProbe, instOwner("i3"), "p", shared)}

	one, _ := Group([]CredHubCert{a, b}, raw)
	two, _ := Group([]CredHubCert{b, a}, raw)

	if !reflect.DeepEqual(one, two) {
		t.Fatalf("membership depends on input order:\n%+v\n%+v", one, two)
	}

	if len(findCert(t, one, "/d/dep/a").Observations) != 2 {
		t.Error("the first name in sorted order should take the shared fingerprint")
	}
}

func TestGroupRepresentativeWithoutDeployedIsFirstObservation(t *testing.T) {
	t.Parallel()

	latest, seen := groupLeaf(t, "latest", 1), groupLeaf(t, "seen", 2)
	owner := instOwner("i1")
	ch := CredHubCert{Name: "/d/dep/crt", Owner: owner, Latest: latest}
	r := rawObs(SourceProbe, owner, "p", seen)
	r.Listener = &Listener{Variable: "crt"}

	certs, reps := Group([]CredHubCert{ch}, []RawObservation{r})
	if got := reps[certs[0].ID]; got == nil || !reflect.DeepEqual(got.Raw, seen.Raw) {
		t.Error("representative should be the first observation when nothing is deployed")
	}
}
