package certinventory

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"blacksmith/internal/credhub"
)

func brokerScanner(ch CredHubReader, state credhub.CapabilityState) *Scanner {
	return NewScanner(ScannerDeps{
		CredHub:      ch,
		CredHubState: func() (credhub.CapabilityState, string) { return state, "" },
		Prober:       Prober{Timeout: 2 * time.Second},
		DirectorName: testDirector,
		Settings:     DefaultSettings,
		Now:          time.Now,
	})
}

func writeFile(t *testing.T, path, content string) string {
	t.Helper()

	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

func rawByLocator(scan InstanceScan) map[string]RawObservation {
	out := map[string]RawObservation{}
	for _, r := range scan.Material.Raw {
		out[r.Locator] = r
	}

	return out
}

func TestScanBrokerSourcesAndOwners(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	https := issue(t, issueSpec{cn: "blacksmith.example", serial: 1})
	services := issue(t, issueSpec{cn: "services-ca", serial: 2, isCA: true})
	boshCA := issue(t, issueSpec{cn: "bosh-ca", serial: 3, isCA: true})
	credhubCA := issue(t, issueSpec{cn: "credhub-ca", serial: 4, isCA: true})
	vaultCA := issue(t, issueSpec{cn: "vault-ca", serial: 5, isCA: true})
	cfCA := issue(t, issueSpec{cn: "cf-ca", serial: 6, isCA: true})
	trusted := issue(t, issueSpec{cn: "trusted", serial: 7, isCA: true})

	servicesPath := writeFile(t, filepath.Join(dir, "blacksmith_services_ca"), certPEM(services.cert))
	vaultPath := writeFile(t, filepath.Join(dir, "vault-ca.pem"), certPEM(vaultCA.cert))
	trustPath := writeFile(t, filepath.Join(dir, "bosh-trusted-cert-1.pem"), certPEM(trusted.cert))
	writeFile(t, filepath.Join(dir, "unrelated.pem"), certPEM(trusted.cert))

	src := BrokerSources{
		HTTPSCertPEM:   certPEM(https.cert),
		ServicesCAPath: servicesPath,
		BOSHCACert:     certPEM(boshCA.cert),
		CredHubCACert:  certPEM(credhubCA.cert),
		VaultCACert:    vaultPath,
		CFAPICAs:       map[string]string{"prod": certPEM(cfCA.cert)},
		TrustStoreGlob: filepath.Join(dir, "bosh-trusted-cert-*.pem"),
	}

	scan := brokerScanner(nil, credhub.StateNotConfigured).ScanBroker(context.Background(), src)
	if scan.Err != "" {
		t.Fatalf("broker scan failed: %s", scan.Err)
	}

	if scan.Ref.ID != BrokerScanID || BrokerScanID != "_broker" {
		t.Errorf("Ref.ID = %q, want _broker", scan.Ref.ID)
	}

	want := map[string]struct {
		source Source
		fp     string
	}{
		"config:broker.tls.certificate":     {SourceBroker, Fingerprint(https.cert)},
		servicesPath:                        {SourceBroker, Fingerprint(services.cert)},
		"config:bosh.cacert":                {SourceBroker, Fingerprint(boshCA.cert)},
		"config:credhub.ca_cert":            {SourceBroker, Fingerprint(credhubCA.cert)},
		"config:vault.cacert":               {SourceBroker, Fingerprint(vaultCA.cert)},
		"config:broker.cf.apis.prod.cacert": {SourceBroker, Fingerprint(cfCA.cert)},
		trustPath:                           {SourceTrustStore, Fingerprint(trusted.cert)},
	}

	if len(scan.Material.Raw) != len(want) {
		t.Errorf("raw observations = %d, want %d", len(scan.Material.Raw), len(want))
	}

	got := rawByLocator(scan)

	for locator, w := range want {
		r, ok := got[locator]
		if !ok {
			t.Errorf("no observation at %s", locator)

			continue
		}

		if r.Source != w.source || r.Fingerprint != w.fp || r.State != StateOK || r.Cert == nil {
			t.Errorf("%s: source %q state %q fingerprint match %v", locator, r.Source, r.State, r.Fingerprint == w.fp)
		}

		if r.Owner != (Owner{Kind: OwnerBroker}) || r.ObservedAt.IsZero() {
			t.Errorf("%s: owner %+v observed %v", locator, r.Owner, r.ObservedAt)
		}
	}

	if scan.LastSuccessAt.IsZero() || len(scan.Material.CredHub) != 0 || scan.Material.ServicesCADrift {
		t.Errorf("last success %v, CredHub %d, drift %v", scan.LastSuccessAt, len(scan.Material.CredHub), scan.Material.ServicesCADrift)
	}
}

func TestScanBrokerUnreadableSourceFails(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	missing := filepath.Join(dir, "missing.pem")
	empty := writeFile(t, filepath.Join(dir, "empty.pem"), "no certificate here\n")

	src := BrokerSources{
		HTTPSCertPEM:   missing,
		VaultCACert:    empty,
		ServicesCAPath: filepath.Join(dir, "no-services-ca"),
		TrustStoreGlob: filepath.Join(dir, "none-*.pem"),
	}

	scan := brokerScanner(nil, credhub.StateNotConfigured).ScanBroker(context.Background(), src)
	got := rawByLocator(scan)

	// A configured services CA path that holds no file is a failed read.
	for _, locator := range []string{"config:broker.tls.certificate", "config:vault.cacert", src.ServicesCAPath} {
		r, ok := got[locator]
		if !ok || r.State != StateFailed || r.Error == "" || r.Cert != nil {
			t.Errorf("%s: %+v, want a failed observation", locator, r)
		}
	}

	if len(scan.Material.Raw) != 3 {
		t.Errorf("raw = %d, want 3", len(scan.Material.Raw))
	}
}

func TestScanBrokerPinnedCAMismatch(t *testing.T) {
	t.Parallel()

	credhubSrv := httptest.NewTLSServer(http.NotFoundHandler())
	t.Cleanup(credhubSrv.Close)

	directorSrv := httptest.NewTLSServer(http.NotFoundHandler())
	t.Cleanup(directorSrv.Close)

	unrelated := issue(t, issueSpec{cn: "some-other-ca", serial: 9, isCA: true})

	src := BrokerSources{
		ServicesCAPath: filepath.Join(t.TempDir(), "absent"),
		TrustStoreGlob: filepath.Join(t.TempDir(), "none-*.pem"),
		CredHubURL:     credhubSrv.URL,
		CredHubCACert:  certPEM(unrelated.cert),
		DirectorURL:    directorSrv.URL,
		BOSHCACert:     certPEM(directorSrv.Certificate()),
	}

	scan := brokerScanner(nil, credhub.StateNotConfigured).ScanBroker(context.Background(), src)
	if scan.Err != "" {
		t.Fatalf("broker scan failed: %s", scan.Err)
	}

	if msg := scan.Material.PinnedFailures[EndpointCredHub]; msg == "" {
		t.Errorf("PinnedFailures = %v, want a credhub failure", scan.Material.PinnedFailures)
	}

	if _, failed := scan.Material.PinnedFailures[EndpointDirector]; failed {
		t.Errorf("the director's chain verifies against its pin, yet failed: %v", scan.Material.PinnedFailures)
	}

	names := map[string]RawObservation{}

	for _, r := range scan.Material.Raw {
		if r.Source == SourceProbe {
			names[r.Owner.InstanceName] = r
		}
	}

	for _, name := range []string{EndpointCredHub, EndpointDirector} {
		r, ok := names[name]
		if !ok {
			t.Errorf("no probe observation owned by %s", name)

			continue
		}

		if r.Owner.Kind != OwnerPlatform || r.State != StateOK || r.Cert == nil {
			t.Errorf("%s probe: owner %+v state %q", name, r.Owner, r.State)
		}

		if r.Fingerprint != Fingerprint(credhubSrv.Certificate()) {
			t.Errorf("%s probe did not record the served leaf", name)
		}

		if !strings.HasPrefix(r.Locator, name+" 127.0.0.1:") {
			t.Errorf("%s probe locator = %q", name, r.Locator)
		}
	}

	if _, ok := names[EndpointUAA]; ok {
		t.Error("UAA was probed without a URL")
	}
}

func TestScanBrokerUnreachableEndpoint(t *testing.T) {
	t.Parallel()

	s := brokerScanner(nil, credhub.StateNotConfigured)
	leaf := &fakeLeaf{}
	s.leaf = leaf.leaf

	scan := s.ScanBroker(context.Background(), BrokerSources{
		ServicesCAPath: filepath.Join(t.TempDir(), "absent"),
		TrustStoreGlob: filepath.Join(t.TempDir(), "none-*.pem"),
		UAAURL:         "https://uaa.example:8443",
		BOSHCACert:     certPEM(issue(t, issueSpec{cn: "bosh-ca", serial: 3, isCA: true}).cert),
	})

	if got := leaf.dialed(); len(got) != 1 || got[0] != "uaa.example:8443" {
		t.Errorf("dialed %v, want uaa.example:8443", got)
	}

	var probe *RawObservation

	for i := range scan.Material.Raw {
		if scan.Material.Raw[i].Source == SourceProbe {
			probe = &scan.Material.Raw[i]
		}
	}

	if probe == nil || probe.State != StateFailed || probe.Owner != (Owner{Kind: OwnerPlatform, InstanceName: EndpointUAA}) {
		t.Fatalf("UAA probe = %+v, want a failed observation owned by uaa", probe)
	}

	if len(scan.Material.PinnedFailures) != 0 {
		t.Errorf("an unreachable endpoint is a probe failure, not a pin failure: %v", scan.Material.PinnedFailures)
	}
}

func servicesCACredHub(name string, ca issued) *fakeCredHub {
	return &fakeCredHub{
		meta: map[string]*credhub.CertificateMeta{
			name: {ID: "ca-id", Name: name, Versions: []credhub.CertificateVersionMeta{
				{ID: "ca-v2", ExpiryDate: ca.cert.NotAfter, CertificateAuthority: true},
			}},
		},
		versions: map[string]*credhub.CertificatePublic{
			"ca-v2": {VersionID: "ca-v2", Name: name, Certificate: certPEM(ca.cert), CA: certPEM(ca.cert)},
		},
	}
}

func TestScanBrokerServicesCADrift(t *testing.T) {
	t.Parallel()

	name := "/ws-bosh/ws-blacksmith/blacksmith_services_ca"
	inFile := issue(t, issueSpec{cn: "services-ca", serial: 1, isCA: true})
	inCredHub := issue(t, issueSpec{cn: "services-ca", serial: 2, isCA: true})

	for _, tc := range []struct {
		name      string
		credhubCA issued
		drift     bool
	}{
		{"different CA", inCredHub, true},
		{"same CA", inFile, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			src := BrokerSources{
				ServicesCAPath: writeFile(t, filepath.Join(dir, "blacksmith_services_ca"), certPEM(inFile.cert)),
				TrustStoreGlob: filepath.Join(dir, "none-*.pem"),
				ServicesCAName: name,
			}

			scan := brokerScanner(servicesCACredHub(name, tc.credhubCA), credhub.StateConfigured).ScanBroker(context.Background(), src)

			if scan.Material.ServicesCADrift != tc.drift {
				t.Errorf("ServicesCADrift = %v, want %v", scan.Material.ServicesCADrift, tc.drift)
			}

			if len(scan.Material.CredHub) != 1 {
				t.Fatalf("CredHub = %d entries, want the services CA", len(scan.Material.CredHub))
			}

			c := scan.Material.CredHub[0]
			if c.Name != name || c.Owner != (Owner{Kind: OwnerBroker}) || c.Err != "" {
				t.Errorf("services CA entry: name %q owner %+v err %q", c.Name, c.Owner, c.Err)
			}

			if c.Deployed == nil || Fingerprint(c.Deployed) != Fingerprint(tc.credhubCA.cert) ||
				c.Versions.LatestVersionID != "ca-v2" || c.Versions.CertificateID != "ca-id" {
				t.Errorf("services CA versions = %+v", c.Versions)
			}
		})
	}
}

func TestScanBrokerServicesCAWithoutCredHub(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	ca := issue(t, issueSpec{cn: "services-ca", serial: 1, isCA: true})
	src := BrokerSources{
		ServicesCAPath: writeFile(t, filepath.Join(dir, "blacksmith_services_ca"), certPEM(ca.cert)),
		TrustStoreGlob: filepath.Join(dir, "none-*.pem"),
		ServicesCAName: "/ws-bosh/ws-blacksmith/blacksmith_services_ca",
	}

	scan := brokerScanner(nil, credhub.StateNotConfigured).ScanBroker(context.Background(), src)
	if scan.Material.ServicesCADrift {
		t.Error("drift reported without a CredHub copy to compare")
	}

	if len(scan.Material.CredHub) != 1 {
		t.Fatalf("CredHub = %d entries, want the unread services CA", len(scan.Material.CredHub))
	}

	c := scan.Material.CredHub[0]
	if c.Name != src.ServicesCAName || c.Err != "CredHub not configured" || c.Owner != (Owner{Kind: OwnerBroker}) || c.Deployed != nil {
		t.Errorf("services CA entry = %+v", c)
	}

	if cov := coverageFor(t, scan.Material, src.ServicesCAName); cov.Complete {
		t.Errorf("coverage = %+v, want incomplete", cov)
	}
}

func TestScanBrokerMissingServicesCAFile(t *testing.T) {
	t.Parallel()

	if _, err := os.Stat(DefaultServicesCAPath); err == nil {
		t.Skip("this host has a real services CA file")
	}

	dir := t.TempDir()
	custom := filepath.Join(dir, "missing_services_ca")

	for _, tc := range []struct {
		name    string
		path    string
		caName  string
		locator string
		want    bool
	}{
		{"default path and no CredHub name", "", "", DefaultServicesCAPath, false},
		{"default path with a CredHub name", "", "/ws-bosh/ws-blacksmith/blacksmith_services_ca", DefaultServicesCAPath, true},
		{"configured path", custom, "", custom, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			src := BrokerSources{
				ServicesCAPath: tc.path,
				ServicesCAName: tc.caName,
				TrustStoreGlob: filepath.Join(dir, "none-*.pem"),
			}

			scan := brokerScanner(nil, credhub.StateNotConfigured).ScanBroker(context.Background(), src)

			r, ok := rawByLocator(scan)[tc.locator]
			if ok != tc.want {
				t.Fatalf("observation at %s = %v, want %v", tc.locator, ok, tc.want)
			}

			if ok && (r.State != StateFailed || r.Source != SourceBroker || r.Error == "") {
				t.Errorf("missing file observation = %+v", r)
			}
		})
	}
}

func TestBrokerSourceDefaults(t *testing.T) {
	t.Parallel()

	if DefaultServicesCAPath != "/var/vcap/jobs/blacksmith/config/tls/blacksmith_services_ca" ||
		DefaultTrustStoreGlob != "/etc/ssl/certs/bosh-trusted-cert-*.pem" {
		t.Errorf("defaults = %q, %q", DefaultServicesCAPath, DefaultTrustStoreGlob)
	}
}

func TestScanBrokerServicesCAByCreationTime(t *testing.T) {
	t.Parallel()

	name := "/ws-bosh/ws-blacksmith/blacksmith_services_ca"
	older := issue(t, issueSpec{cn: "services-ca", serial: 1, isCA: true})
	newer := issue(t, issueSpec{cn: "services-ca", serial: 2, isCA: true, notAfter: 96 * time.Hour})
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

	ch := &fakeCredHub{
		meta: map[string]*credhub.CertificateMeta{
			name: {ID: "ca-id", Name: name, Versions: []credhub.CertificateVersionMeta{
				{ID: "ca-v1", ExpiryDate: older.cert.NotAfter, CertificateAuthority: true},
				{ID: "ca-v2", ExpiryDate: newer.cert.NotAfter, CertificateAuthority: true, Transitional: true},
			}},
		},
		created: map[string][]credhub.CredentialVersion{
			name: {{ID: "ca-v1", CreatedAt: base}, {ID: "ca-v2", CreatedAt: base.Add(time.Hour)}},
		},
		versions: map[string]*credhub.CertificatePublic{
			"ca-v1": {VersionID: "ca-v1", Name: name, Certificate: certPEM(older.cert), CA: certPEM(older.cert)},
			"ca-v2": {VersionID: "ca-v2", Name: name, Certificate: certPEM(newer.cert), CA: certPEM(newer.cert)},
		},
	}

	dir := t.TempDir()
	src := BrokerSources{
		ServicesCAPath: writeFile(t, filepath.Join(dir, "blacksmith_services_ca"), certPEM(newer.cert)),
		TrustStoreGlob: filepath.Join(dir, "none-*.pem"),
		ServicesCAName: name,
	}

	scan := brokerScanner(ch, credhub.StateConfigured).ScanBroker(context.Background(), src)
	if len(scan.Material.CredHub) != 1 {
		t.Fatalf("CredHub = %d entries, want the services CA", len(scan.Material.CredHub))
	}

	c := scan.Material.CredHub[0]
	if c.Versions.DeployedVersionID != "ca-v2" || c.Versions.LatestVersionID != "ca-v2" || !c.Versions.Transitional {
		t.Errorf("versions = %+v, want ca-v2 current and transitional", c.Versions)
	}

	if c.Deployed == nil || Fingerprint(c.Deployed) != Fingerprint(newer.cert) {
		t.Error("the services CA's current certificate is not its newest version")
	}

	if scan.Material.ServicesCADrift {
		t.Error("the file matches the newest version, so there is no drift")
	}
}
