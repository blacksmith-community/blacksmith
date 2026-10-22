package certinventory

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"
)

type issued struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

type issueSpec struct {
	cn       string
	serial   int64
	isCA     bool
	dns      []string
	notAfter time.Duration // from now
	parent   *issued
	sigAlgo  x509.SignatureAlgorithm
	ext      []x509.ExtKeyUsage
}

func issue(t *testing.T, s issueSpec) issued {
	t.Helper()

	key := ecKey(t, elliptic.P256())
	if s.notAfter == 0 {
		s.notAfter = 24 * time.Hour
	}

	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(s.serial),
		Subject:               pkix.Name{CommonName: s.cn},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(s.notAfter),
		DNSNames:              s.dns,
		ExtKeyUsage:           s.ext,
		BasicConstraintsValid: true,
		IsCA:                  s.isCA,
		SubjectKeyId:          []byte{0x01, byte(s.serial)}, //nolint:gosec // Test serials are small.
		SignatureAlgorithm:    s.sigAlgo,
	}
	if s.isCA {
		tmpl.KeyUsage = x509.KeyUsageCertSign
	}

	parent, signer := tmpl, crypto.Signer(key)
	if s.parent != nil {
		parent, signer = s.parent.cert, s.parent.key
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, key.Public(), signer)
	if err != nil {
		t.Fatal(err)
	}

	c, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}

	return issued{cert: c, key: key}
}

func hasFinding(c Certificate, code string) bool {
	for _, f := range c.Findings {
		if f.Code == code {
			return true
		}
	}

	return false
}

// credhubLeaf builds one grouped CredHub certificate with its representative.
func credhubLeaf(t *testing.T, leaf *x509.Certificate, signingCA string, v Versions) ([]Certificate, map[string]*x509.Certificate) {
	t.Helper()

	v.SigningCA = signingCA
	certs, reps := Group([]CredHubCert{{Name: "/d/dep/crt", Owner: instOwner("i1"), Versions: v, Deployed: leaf}}, nil)

	return certs, reps
}

func TestFindingChainBroken(t *testing.T) {
	t.Parallel()

	ca := issue(t, issueSpec{cn: "ca", serial: 1, isCA: true})
	other := issue(t, issueSpec{cn: "other-ca", serial: 2, isCA: true})
	good := issue(t, issueSpec{cn: "leaf", serial: 3, parent: &ca})
	expired := issue(t, issueSpec{cn: "old", serial: 4, parent: &ca, notAfter: -time.Minute})

	rows := []struct {
		name string
		leaf *x509.Certificate
		ca   *x509.Certificate
		want bool
	}{
		{"verifies", good.cert, ca.cert, false},
		{"signed by another CA", good.cert, other.cert, true},
		{"expired leaf is an expiry matter", expired.cert, ca.cert, false},
		{"no CA held", good.cert, nil, false},
	}

	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			t.Parallel()

			certs, reps := credhubLeaf(t, r.leaf, "/d/dep/ca", Versions{})
			cas := map[string]*x509.Certificate{}

			if r.ca != nil {
				cas["/d/dep/ca"] = r.ca
			}

			ApplyFindings(certs, FindingInput{CAs: cas, Reps: reps, Now: time.Now()})

			if got := hasFinding(certs[0], FindingChainBroken); got != r.want {
				t.Errorf("chain_broken %v, want %v: %+v", got, r.want, certs[0].Findings)
			}
		})
	}
}

func TestFindingServicesCADrift(t *testing.T) {
	t.Parallel()

	ca := issue(t, issueSpec{cn: "services", serial: 1, isCA: true})
	leaf := issue(t, issueSpec{cn: "leaf", serial: 2})
	certs, reps := Group([]CredHubCert{
		{Name: "/d/dep/blacksmith_services_ca", Owner: Owner{Kind: OwnerBroker}, Deployed: ca.cert},
		{Name: "/d/dep/crt", Owner: instOwner("i1"), Deployed: leaf.cert},
	}, nil)

	ApplyFindings(certs, FindingInput{Reps: reps, ServicesCADrift: true})

	for _, c := range certs {
		want := c.Variable == "blacksmith_services_ca"
		if got := hasFinding(c, FindingServicesCADrift); got != want {
			t.Errorf("%s: services_ca_drift %v, want %v", c.Key, got, want)
		}
	}

	ApplyFindings(certs, FindingInput{Reps: reps})

	for _, c := range certs {
		if hasFinding(c, FindingServicesCADrift) {
			t.Errorf("%s: drift reported with no drift", c.Key)
		}
	}
}

func TestFindingPinnedCAMismatch(t *testing.T) {
	t.Parallel()

	leaf := issue(t, issueSpec{cn: "credhub", serial: 1})
	platform := Owner{Kind: OwnerPlatform, InstanceName: "credhub"}
	certs, reps := Group(nil, []RawObservation{rawObs(SourceProbe, platform, "credhub:8844", leaf.cert)})

	ApplyFindings(certs, FindingInput{Reps: reps, PinnedFailures: map[string]string{"credhub": "unknown authority"}})

	if !hasFinding(certs[0], FindingPinnedCAMismatch) {
		t.Fatalf("no pinned_ca_mismatch: %+v", certs[0].Findings)
	}

	if f := certs[0].Findings[0]; f.Severity != SeverityError || !strings.Contains(f.Message, "unknown authority") {
		t.Errorf("finding %+v", f)
	}

	ApplyFindings(certs, FindingInput{Reps: reps, PinnedFailures: map[string]string{"uaa": "x"}})

	if hasFinding(certs[0], FindingPinnedCAMismatch) {
		t.Error("another endpoint's failure was attributed to this certificate")
	}
}

func TestFindingSigningCAChanged(t *testing.T) {
	t.Parallel()

	oldCA := issue(t, issueSpec{cn: "ca", serial: 1, isCA: true})
	newCA := issue(t, issueSpec{cn: "ca", serial: 2, isCA: true})
	leaf := issue(t, issueSpec{cn: "leaf", serial: 3, parent: &oldCA})

	for _, r := range []struct {
		name string
		ca   *x509.Certificate
		want bool
	}{{"same CA", oldCA.cert, false}, {"CA replaced", newCA.cert, true}} {
		certs, reps := credhubLeaf(t, leaf.cert, "/d/dep/ca", Versions{})
		ApplyFindings(certs, FindingInput{CAs: map[string]*x509.Certificate{"/d/dep/ca": r.ca}, Reps: reps, Now: time.Now()})

		if got := hasFinding(certs[0], FindingSigningCAChanged); got != r.want {
			t.Errorf("%s: signing_ca_changed %v, want %v", r.name, got, r.want)
		}
	}
}

func TestFindingIssuerExpiresFirst(t *testing.T) {
	t.Parallel()

	shortCA := issue(t, issueSpec{cn: "short", serial: 1, isCA: true, notAfter: 48 * time.Hour})
	longCA := issue(t, issueSpec{cn: "long", serial: 2, isCA: true, notAfter: 400 * time.Hour})
	later := issue(t, issueSpec{cn: "leaf2", serial: 4, parent: &shortCA, notAfter: 24 * time.Hour})

	run := func(leaf issued, others []issued, cas map[string]*x509.Certificate) Certificate {
		credhub := []CredHubCert{{Name: "/d/dep/crt", Owner: instOwner("i1"), Deployed: leaf.cert}}
		certs, reps := Group(credhub, nil)

		// Manifest literals appear only in Reps.
		for _, o := range others {
			lc, lreps := Group(nil, []RawObservation{rawObs(SourceManifest, instOwner("i2"), "m", o.cert)})
			for id, c := range lreps {
				reps[id] = c
			}

			certs = append(certs, lc...)
		}

		ApplyFindings(certs, FindingInput{CAs: cas, Reps: reps, Now: time.Now()})

		for _, c := range certs {
			if c.Key == "/d/dep/crt" {
				return c
			}
		}

		t.Fatal("leaf missing")

		return Certificate{}
	}

	// shortCA expires at 48h; boundary outlives it and later does not.
	boundary := issue(t, issueSpec{cn: "boundary", serial: 5, parent: &shortCA, notAfter: 72 * time.Hour})

	if c := run(boundary, []issued{shortCA}, nil); !hasFinding(c, FindingIssuerExpiresFirst) {
		t.Errorf("issuer found only in Reps did not fire: %+v", c.Findings)
	}

	if c := run(boundary, nil, map[string]*x509.Certificate{"/d/dep/ca": shortCA.cert}); !hasFinding(c, FindingIssuerExpiresFirst) {
		t.Errorf("issuer found only in CAs did not fire: %+v", c.Findings)
	}

	if c := run(later, []issued{shortCA}, nil); hasFinding(c, FindingIssuerExpiresFirst) {
		t.Error("fired although the issuer outlives the leaf")
	}

	if c := run(boundary, []issued{longCA}, nil); hasFinding(c, FindingIssuerExpiresFirst) {
		t.Error("fired for an unrelated certificate")
	}

	if c := run(boundary, nil, nil); hasFinding(c, FindingIssuerExpiresFirst) {
		t.Error("fired with no issuer in the inventory")
	}
}

func TestFindingSANMismatchWildcard(t *testing.T) {
	t.Parallel()

	wild := issue(t, issueSpec{cn: "w", serial: 1, dns: []string{"*.ws.example"}})
	plain := issue(t, issueSpec{cn: "p", serial: 2, dns: []string{"other.example"}})
	client := issue(t, issueSpec{cn: "c", serial: 3, dns: []string{"other.example"}, ext: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}})

	rows := []struct {
		name  string
		leaf  *x509.Certificate
		hosts []string
		want  bool
	}{
		{"wildcard covers one label", wild.cert, []string{"orders.ws.example"}, false},
		{"wildcard does not cover two labels", wild.cert, []string{"a.b.ws.example"}, true},
		{"any host is enough", wild.cert, []string{"a.b.ws.example", "orders.ws.example"}, false},
		{"no SAN covers the host", plain.cert, []string{"orders.ws.example"}, true},
		{"no hosts known", plain.cert, nil, false},
		{"client certificates carry no host", client.cert, []string{"orders.ws.example"}, false},
	}

	for _, r := range rows {
		certs, reps := Group([]CredHubCert{{Name: "/d/dep/crt", Owner: instOwner("i1"), Deployed: r.leaf}}, nil)
		ApplyFindings(certs, FindingInput{Reps: reps, Hosts: map[string][]string{"i1": r.hosts}})

		if got := hasFinding(certs[0], FindingSANMismatch); got != r.want {
			t.Errorf("%s: san_mismatch %v, want %v", r.name, got, r.want)
		}
	}
}

func TestFindingServedMismatch(t *testing.T) {
	t.Parallel()

	deployed, other := issue(t, issueSpec{cn: "d", serial: 1}), issue(t, issueSpec{cn: "o", serial: 2})
	owner := instOwner("i1")
	build := func() ([]Certificate, map[string]*x509.Certificate) {
		r := rawObs(SourceProbe, owner, "10.0.0.1:6379", other.cert)
		r.Listener = &Listener{Variable: "crt"}

		return Group([]CredHubCert{{Name: "/d/dep/crt", Owner: owner, Deployed: deployed.cert}}, []RawObservation{r})
	}

	certs, reps := build()
	ApplyFindings(certs, FindingInput{Reps: reps})

	if !hasFinding(certs[0], FindingServedMismatch) {
		t.Fatalf("no served_mismatch: %+v", certs[0].Findings)
	}

	certs, reps = build()
	certs[0].Divergence = &DivergenceRef{State: DivergenceUnconverged}
	ApplyFindings(certs, FindingInput{Reps: reps})

	if hasFinding(certs[0], FindingServedMismatch) {
		t.Error("served_mismatch raised although a divergence record explains it")
	}
}

func TestFindingDeployPending(t *testing.T) {
	t.Parallel()

	leaf := issue(t, issueSpec{cn: "d", serial: 1})

	for _, r := range []struct {
		deployed, latest string
		want             bool
	}{{"v1", "v2", true}, {"v1", "v1", false}, {"v1", "", false}} {
		certs, reps := credhubLeaf(t, leaf.cert, "", Versions{DeployedVersionID: r.deployed, LatestVersionID: r.latest})
		ApplyFindings(certs, FindingInput{Reps: reps})

		f := certs[0].Findings
		if got := hasFinding(certs[0], FindingDeployPending); got != r.want {
			t.Errorf("%+v: deploy_pending %v, want %v", r, got, r.want)
		}

		if r.want && f[0].Severity != SeverityInfo {
			t.Errorf("severity %q", f[0].Severity)
		}
	}
}

func TestFindingWeakKeyAndSignature(t *testing.T) {
	t.Parallel()

	sha1Cert, _ := ParsePEMCertificates(mustRead(t, "testdata/sha1.pem"))
	strong := issue(t, issueSpec{cn: "ok", serial: 1})

	certs, reps := Group([]CredHubCert{
		{Name: "/d/dep/weak", Owner: instOwner("i1"), Deployed: sha1Cert[0]},
		{Name: "/d/dep/strong", Owner: instOwner("i2"), Deployed: strong.cert},
	}, nil)
	ApplyFindings(certs, FindingInput{Reps: reps})

	for _, c := range certs {
		weak := c.Key == "/d/dep/weak"
		if hasFinding(c, FindingWeakSignature) != weak {
			t.Errorf("%s: weak_signature %v", c.Key, !weak)
		}
	}
}

func TestFindingProbeFailed(t *testing.T) {
	t.Parallel()

	leaf := issue(t, issueSpec{cn: "d", serial: 1})
	owner := instOwner("i1")
	failed := RawObservation{
		Observation: Observation{Source: SourceProbe, Owner: owner, Locator: "node/2 10.0.0.3:6379", State: StateFailed, Error: "timed out after 5s"},
		Listener:    &Listener{Variable: "crt"},
	}
	certs, reps := Group([]CredHubCert{{Name: "/d/dep/crt", Owner: owner, Deployed: leaf.cert}}, []RawObservation{failed})

	ApplyFindings(certs, FindingInput{Reps: reps})

	if !hasFinding(certs[0], FindingProbeFailed) {
		t.Fatalf("no probe_failed: %+v", certs[0].Findings)
	}

	for _, f := range certs[0].Findings {
		if f.Code == FindingProbeFailed && (f.Severity != SeverityInfo || !strings.Contains(f.Message, "node/2")) {
			t.Errorf("finding %+v", f)
		}
	}
}

func TestFindingsWithoutRepresentative(t *testing.T) {
	t.Parallel()

	ca := issue(t, issueSpec{cn: "ca", serial: 1, isCA: true})
	leaf := issue(t, issueSpec{cn: "leaf", serial: 2, parent: &ca, dns: []string{"x.example"}})
	certs, _ := credhubLeaf(t, leaf.cert, "/d/dep/ca", Versions{DeployedVersionID: "v1", LatestVersionID: "v2"})

	ApplyFindings(certs, FindingInput{
		CAs:   map[string]*x509.Certificate{"/d/dep/ca": issue(t, issueSpec{cn: "ca2", serial: 9, isCA: true}).cert},
		Hosts: map[string][]string{"i1": {"y.example"}},
		Now:   time.Now(),
	})

	for _, f := range certs[0].Findings {
		switch f.Code {
		case FindingChainBroken, FindingSANMismatch, FindingIssuerExpiresFirst, FindingSigningCAChanged, FindingWeakKey, FindingWeakSignature:
			t.Errorf("x509 finding %s raised without a representative", f.Code)
		}
	}

	if !hasFinding(certs[0], FindingDeployPending) {
		t.Error("findings that need no x509 data are still expected")
	}
}

func TestCoverageCounts(t *testing.T) {
	t.Parallel()

	leaf := issue(t, issueSpec{cn: "d", serial: 1})
	owner := instOwner("i1")
	at := time.Now().Add(-time.Hour)
	okObs := func(loc string) RawObservation {
		r := rawObs(SourceProbe, owner, loc, leaf.cert)
		r.Listener = &Listener{Variable: "crt"}
		r.ObservedAt = at

		return r
	}
	failed := RawObservation{
		Observation: Observation{Source: SourceProbe, Owner: owner, Locator: "c", State: StateFailed, Error: "refused"},
		Listener:    &Listener{Variable: "crt"},
	}

	certs, reps := Group([]CredHubCert{{Name: "/d/dep/crt", Owner: owner, Deployed: leaf.cert}},
		[]RawObservation{okObs("a"), okObs("b"), failed})
	ApplyFindings(certs, FindingInput{Reps: reps, Expected: map[string]int{certs[0].ID: 3}})

	cov := certs[0].Coverage
	if cov.ExpectedListeners != 3 || cov.ObservedListeners != 2 || cov.FailedListeners != 1 || cov.Complete {
		t.Errorf("coverage %+v", cov)
	}

	if !cov.OldestObservation.Equal(at) || cov.SourceStates[SourceProbe] != StateFailed || cov.SourceStates[SourceCredHub] != StateOK {
		t.Errorf("oldest %v, states %v", cov.OldestObservation, cov.SourceStates)
	}

	// Every expected listener answered, so coverage is complete.
	certs, reps = Group([]CredHubCert{{Name: "/d/dep/crt", Owner: owner, Deployed: leaf.cert}},
		[]RawObservation{okObs("a"), okObs("b")})
	ApplyFindings(certs, FindingInput{Reps: reps, Expected: map[string]int{certs[0].ID: 2}})

	if !certs[0].Coverage.Complete {
		t.Errorf("coverage %+v, want complete", certs[0].Coverage)
	}

	// A failed probe alone makes coverage incomplete, even with no expectation.
	certs, reps = Group([]CredHubCert{{Name: "/d/dep/crt", Owner: owner, Deployed: leaf.cert}},
		[]RawObservation{okObs("a"), failed})
	ApplyFindings(certs, FindingInput{Reps: reps})

	if certs[0].Coverage.Complete {
		t.Error("coverage complete although a probe failed")
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()

	b, err := os.ReadFile(path) //nolint:gosec // The test reads its own testdata.
	if err != nil {
		t.Fatal(err)
	}

	return b
}

func TestFindingSigningCAChangedIgnoresCAFlag(t *testing.T) {
	t.Parallel()

	// A signing certificate that lacks the CA flag.
	plain := issue(t, issueSpec{cn: "plain", serial: 1})
	leaf := issue(t, issueSpec{cn: "leaf", serial: 2, parent: &plain})

	certs, reps := credhubLeaf(t, leaf.cert, "/d/dep/ca", Versions{})
	ApplyFindings(certs, FindingInput{CAs: map[string]*x509.Certificate{"/d/dep/ca": plain.cert}, Reps: reps, Now: time.Now()})

	if hasFinding(certs[0], FindingSigningCAChanged) {
		t.Errorf("signing_ca_changed fired for the right signer: %+v", certs[0].Findings)
	}
}
