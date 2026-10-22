package certinventory

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/credhub"
)

const (
	testDirector   = "ws-bosh"
	testInstanceID = "inst-1"
)

// fakeDirector answers from fixed values. A non-nil block makes
// GetDeployment wait for it to close and ignore its context, the way a
// stuck director connection would.
type fakeDirector struct {
	manifest string
	vars     []bosh.Variable
	vms      []bosh.VM
	varsErr  error
	vmsErr   error
	block    chan struct{}

	mu    sync.Mutex
	calls []string
}

func (f *fakeDirector) record(call string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.calls = append(f.calls, call)
}

func (f *fakeDirector) GetInfo(context.Context) (*bosh.Info, error) {
	f.record("info")

	return &bosh.Info{Name: testDirector, Version: "282.0.0"}, nil
}

func (f *fakeDirector) GetDeployment(_ context.Context, name string) (*bosh.DeploymentDetail, error) {
	f.record("deployment " + name)

	if f.block != nil {
		<-f.block
	}

	return &bosh.DeploymentDetail{Name: name, Manifest: f.manifest}, nil
}

func (f *fakeDirector) GetDeploymentVMs(_ context.Context, name string) ([]bosh.VM, error) {
	f.record("vms " + name)

	if f.vmsErr != nil {
		return nil, f.vmsErr
	}

	return f.vms, nil
}

func (f *fakeDirector) ListVariables(_ context.Context, name string) ([]bosh.Variable, error) {
	f.record("variables " + name)

	if f.varsErr != nil {
		return nil, f.varsErr
	}

	return f.vars, nil
}

// fakeCredHub answers metadata by name and versions by ID. A non-nil err
// fails every call. created gives each name's versions with their creation
// times; a name without an entry lists its metadata versions in the order
// given, newest first.
type fakeCredHub struct {
	meta     map[string]*credhub.CertificateMeta
	versions map[string]*credhub.CertificatePublic
	created  map[string][]credhub.CredentialVersion
	err      error

	mu    sync.Mutex
	calls []string
}

func (f *fakeCredHub) record(call string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.calls = append(f.calls, call)
}

func (f *fakeCredHub) CertificateMetadata(_ context.Context, name string) (*credhub.CertificateMeta, error) {
	f.record("meta " + name)

	if f.err != nil {
		return nil, f.err
	}

	m, ok := f.meta[name]
	if !ok {
		return nil, fmt.Errorf("%w: %q", credhub.ErrNotFound, name)
	}

	return m, nil
}

func (f *fakeCredHub) CredentialVersions(_ context.Context, name string, limit int) ([]credhub.CredentialVersion, error) {
	f.record("versions-of " + name)

	if f.err != nil {
		return nil, f.err
	}

	var out []credhub.CredentialVersion

	if list, ok := f.created[name]; ok {
		out = append(out, list...)
		sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	} else if m, ok := f.meta[name]; ok {
		for _, v := range m.Versions {
			out = append(out, credhub.CredentialVersion{ID: v.ID, Type: "certificate"})
		}
	} else {
		return nil, fmt.Errorf("%w: %q", credhub.ErrNotFound, name)
	}

	if len(out) > limit {
		out = out[:limit]
	}

	return out, nil
}

func (f *fakeCredHub) CertificateVersion(_ context.Context, id string) (*credhub.CertificatePublic, error) {
	f.record("version " + id)

	if f.err != nil {
		return nil, f.err
	}

	if id == "" {
		return nil, credhub.ErrEmptyVersionID
	}

	v, ok := f.versions[id]
	if !ok {
		return nil, fmt.Errorf("%w: %q", credhub.ErrNotFound, id)
	}

	return v, nil
}

// fakeLeaf stands in for the TLS dialer and records every address.
type fakeLeaf struct {
	mu    sync.Mutex
	addrs []string
	cert  *x509.Certificate
}

func (f *fakeLeaf) leaf(_ context.Context, addr, _ string) (*x509.Certificate, []*x509.Certificate, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.addrs = append(f.addrs, addr)

	if f.cert == nil {
		return nil, nil, fmt.Errorf("probe %s: connection refused", addr)
	}

	return f.cert, nil, nil
}

func (f *fakeLeaf) dialed() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]string(nil), f.addrs...)
}

func certPEM(c *x509.Certificate) string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Raw}))
}

func keyPEM(t *testing.T, i issued) string {
	t.Helper()

	der, err := x509.MarshalECPrivateKey(i.key)
	if err != nil {
		t.Fatal(err)
	}

	return string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}))
}

func testRef(deployment string) InstanceRef {
	return InstanceRef{
		ID: testInstanceID, Name: "my-cache", Deployment: deployment,
		ServiceID: "svc-1", PlanID: "plan-1", ServiceName: "redis", PlanName: "cluster-7",
	}
}

func wantOwner(deployment string) Owner {
	return Owner{
		Kind: OwnerInstance, InstanceID: testInstanceID, InstanceName: "my-cache",
		Deployment: deployment, ServiceName: "redis", PlanName: "cluster-7",
	}
}

func redisVMs(n int) []bosh.VM {
	vms := make([]bosh.VM, 0, n)
	for i := 0; i < n; i++ {
		vms = append(vms, bosh.VM{CID: fmt.Sprintf("vm-%d", i), Job: "redis", Index: i, IPs: []string{fmt.Sprintf("10.0.0.%d", i+1)}})
	}

	return vms
}

func rabbitManifest(port int) string {
	return fmt.Sprintf(`name: rabbitmq-1
variables:
- name: rabbitmq_crt
  type: certificate
  options:
    ca: /ws-bosh/ws-blacksmith/blacksmith_services_ca
- name: admin_password
  type: password
instance_groups:
- name: standalone
  instances: 1
  jobs:
  - name: rabbitmq
    properties:
      rabbitmq:
        tls:
          enabled: true
          port: %d
          crt: ((rabbitmq_crt.certificate))
          key: ((rabbitmq_crt.private_key))
`, port)
}

// serveTLS starts a local TLS listener that presents leaf and returns its
// port. The listener closes when the test ends.
func serveTLS(t *testing.T, leaf issued) int {
	t.Helper()

	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{leaf.cert.Raw}, PrivateKey: leaf.key}},
		MinVersion:   tls.VersionTLS12,
	})
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup

	t.Cleanup(func() {
		_ = ln.Close()
		wg.Wait()
	})

	wg.Add(1)

	go func() {
		defer wg.Done()

		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}

			if tc, ok := conn.(*tls.Conn); ok {
				_ = tc.HandshakeContext(context.Background())
			}

			_ = conn.Close()
		}
	}()

	tcpAddr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("listener address is %T, want *net.TCPAddr", ln.Addr())
	}

	return tcpAddr.Port
}

type scannerFixture struct {
	director *fakeDirector
	credhub  CredHubReader
	state    credhub.CapabilityState
	reason   string
	vault    *fakeVault
	settings Settings
	leaf     *fakeLeaf // nil means the real prober
}

func (f scannerFixture) scanner() *Scanner {
	s := NewScanner(ScannerDeps{
		Director:     f.director,
		CredHub:      f.credhub,
		CredHubState: func() (credhub.CapabilityState, string) { return f.state, f.reason },
		Vault:        f.vault,
		Prober:       Prober{Timeout: 2 * time.Second},
		DirectorName: testDirector,
		Settings:     func() Settings { return f.settings },
		Now:          time.Now,
	})

	if f.leaf != nil {
		s.leaf = f.leaf.leaf
	}

	return s
}

func probeObservations(scan InstanceScan) []RawObservation {
	var out []RawObservation

	for _, r := range scan.Material.Raw {
		if r.Source == SourceProbe {
			out = append(out, r)
		}
	}

	return out
}

func TestScanInstanceWithoutCredHub(t *testing.T) {
	t.Parallel()

	served := issue(t, issueSpec{cn: "rabbitmq.bosh", serial: 7})
	port := serveTLS(t, served)

	dep := "rabbitmq-1"
	f := scannerFixture{
		director: &fakeDirector{
			manifest: rabbitManifest(port),
			vars: []bosh.Variable{
				{ID: "v1", Name: "/ws-bosh/rabbitmq-1/rabbitmq_crt"},
				{ID: "p1", Name: "/ws-bosh/rabbitmq-1/admin_password"},
			},
			vms: []bosh.VM{{CID: "vm-0", Job: "standalone", Index: 0, IPs: []string{"127.0.0.1"}}},
		},
		state:    credhub.StateNotConfigured,
		settings: DefaultSettings(),
	}

	scan := f.scanner().ScanInstance(context.Background(), testRef(dep))
	if scan.Err != "" {
		t.Fatalf("scan failed: %s", scan.Err)
	}

	if len(scan.Material.CredHub) != 1 {
		t.Fatalf("CredHub entries = %d, want 1 (the certificate only): %+v", len(scan.Material.CredHub), scan.Material.CredHub)
	}

	ch := scan.Material.CredHub[0]
	if ch.Name != "/ws-bosh/rabbitmq-1/rabbitmq_crt" || ch.Deployed != nil || ch.Err != "CredHub not configured" {
		t.Errorf("CredHub entry = name %q deployed %v err %q", ch.Name, ch.Deployed != nil, ch.Err)
	}

	if ch.Versions.DeployedVersionID != "v1" {
		t.Errorf("DeployedVersionID = %q, want v1 from the director", ch.Versions.DeployedVersionID)
	}

	if ch.Owner != wantOwner(dep) {
		t.Errorf("CredHub owner = %+v", ch.Owner)
	}

	probes := probeObservations(scan)
	if len(probes) != 1 {
		t.Fatalf("probe observations = %d, want 1", len(probes))
	}

	p := probes[0]
	if p.State != StateOK || p.Cert == nil || p.Fingerprint != Fingerprint(served.cert) {
		t.Errorf("probe = state %q err %q fingerprint %q", p.State, p.Error, p.Fingerprint)
	}

	if p.Listener == nil || p.Listener.Variable != "rabbitmq_crt" {
		t.Errorf("probe listener = %+v, want variable rabbitmq_crt", p.Listener)
	}

	if p.Owner != wantOwner(dep) || p.ObservedAt.IsZero() {
		t.Errorf("probe owner %+v observed %v", p.Owner, p.ObservedAt)
	}

	if want := fmt.Sprintf("standalone/0 127.0.0.1:%d", port); p.Locator != want {
		t.Errorf("probe locator = %q, want %q", p.Locator, want)
	}

	if len(scan.Material.Expected) != 1 || scan.Material.Expected[0].Key != "/ws-bosh/rabbitmq-1/rabbitmq_crt" {
		t.Errorf("expected listeners = %+v", scan.Material.Expected)
	}

	if scan.ScannedAt.IsZero() || !scan.LastSuccessAt.Equal(scan.ScannedAt) || scan.Stale {
		t.Errorf("times: scanned %v last success %v stale %v", scan.ScannedAt, scan.LastSuccessAt, scan.Stale)
	}

	if scan.Facts == nil || len(scan.VMs) != 1 || len(scan.Variables) != 2 || scan.HealthGate != "" {
		t.Errorf("facts %v vms %d variables %d gate %q", scan.Facts != nil, len(scan.VMs), len(scan.Variables), scan.HealthGate)
	}
}

func TestScanInstanceTypedNilCredHubIsNotConfigured(t *testing.T) {
	t.Parallel()

	var client *credhub.Client

	f := scannerFixture{
		director: &fakeDirector{
			manifest: loadManifest(t, "redis-cluster-7.yml"),
			vars:     []bosh.Variable{{ID: "v1", Name: "/ws-bosh/redis-7a1b/redis_cluster_crt"}},
			vms:      redisVMs(3),
		},
		credhub:  client,
		state:    credhub.StateConfigured,
		settings: DefaultSettings(),
		leaf:     &fakeLeaf{},
	}

	scan := f.scanner().ScanInstance(context.Background(), testRef("redis-7a1b"))
	if len(scan.Material.CredHub) != 1 || scan.Material.CredHub[0].Err != "CredHub not configured" {
		t.Fatalf("CredHub = %+v, want one entry not configured", scan.Material.CredHub)
	}
}

func TestScanInstanceCredHubUnavailable(t *testing.T) {
	t.Parallel()

	ch := &fakeCredHub{err: &credhub.APIError{Op: "certificate metadata", Status: http.StatusServiceUnavailable}}
	f := scannerFixture{
		director: &fakeDirector{
			manifest: loadManifest(t, "rabbitmq-standalone.yml"),
			vars: []bosh.Variable{
				{ID: "v1", Name: "/ws-bosh/rabbitmq-9e8f/rabbitmq_standalone_crt"},
				{ID: "v2", Name: "/ws-bosh/rabbitmq-9e8f/rabbitmq_metrics_emitter_crt"},
			},
			vms: []bosh.VM{{CID: "vm-0", Job: "standalone", Index: 0, IPs: []string{"10.0.0.9"}}},
		},
		credhub:  ch,
		state:    credhub.StateConfigured,
		settings: DefaultSettings(),
		leaf:     &fakeLeaf{},
	}

	scan := f.scanner().ScanInstance(context.Background(), testRef("rabbitmq-9e8f"))
	if scan.Err != "" {
		t.Fatalf("scan failed: %s", scan.Err)
	}

	if len(scan.Material.CredHub) != 2 {
		t.Fatalf("CredHub entries = %d, want 2", len(scan.Material.CredHub))
	}

	for _, c := range scan.Material.CredHub {
		if !strings.HasPrefix(c.Err, "CredHub unavailable") || c.Deployed != nil {
			t.Errorf("%s: Err = %q, deployed %v", c.Name, c.Err, c.Deployed != nil)
		}
	}

	raw, err := json.Marshal(scan) //nolint:musttag // The test checks the default encoding for key material.
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(raw), "CredHub unavailable") {
		t.Errorf("marshalled scan lacks the CredHub error: %s", raw)
	}

	// The manifest's literal services CA is still observed.
	var literals int

	for _, r := range scan.Material.Raw {
		if r.Source == SourceManifest {
			literals++
		}
	}

	if literals != 1 {
		t.Errorf("manifest literals = %d, want 1", literals)
	}
}

func TestScanInstanceVersions(t *testing.T) {
	t.Parallel()

	ca := issue(t, issueSpec{cn: "services-ca", serial: 1, isCA: true, notAfter: 48 * time.Hour})
	v1 := issue(t, issueSpec{cn: "redis.bosh", serial: 2, parent: &ca})
	v2 := issue(t, issueSpec{cn: "redis.bosh", serial: 3, parent: &ca, notAfter: 72 * time.Hour})

	name := "/ws-bosh/redis-7a1b/redis_cluster_crt"
	caName := "/ws-bosh/ws-blacksmith/blacksmith_services_ca"

	ch := &fakeCredHub{
		meta: map[string]*credhub.CertificateMeta{
			name: {ID: "cert-id", Name: name, SignedBy: caName, Versions: []credhub.CertificateVersionMeta{
				{ID: "v2", ExpiryDate: v2.cert.NotAfter},
				{ID: "v1", ExpiryDate: v1.cert.NotAfter, Transitional: true},
			}},
			caName: {ID: "ca-id", Name: caName, Versions: []credhub.CertificateVersionMeta{{ID: "ca1", CertificateAuthority: true}}},
		},
		versions: map[string]*credhub.CertificatePublic{
			"v1":  {VersionID: "v1", Name: name, Certificate: certPEM(v1.cert), CA: certPEM(ca.cert)},
			"v2":  {VersionID: "v2", Name: name, Certificate: certPEM(v2.cert), CA: certPEM(ca.cert)},
			"ca1": {VersionID: "ca1", Name: caName, Certificate: certPEM(ca.cert), CA: certPEM(ca.cert)},
		},
	}

	f := scannerFixture{
		director: &fakeDirector{
			manifest: loadManifest(t, "redis-cluster-7.yml"),
			vars:     []bosh.Variable{{ID: "v1", Name: name}},
			vms:      redisVMs(3),
		},
		credhub:  ch,
		state:    credhub.StateConfigured,
		settings: DefaultSettings(),
		leaf:     &fakeLeaf{},
	}

	scan := f.scanner().ScanInstance(context.Background(), testRef("redis-7a1b"))
	if scan.Err != "" {
		t.Fatalf("scan failed: %s", scan.Err)
	}

	if len(scan.Material.CredHub) != 1 {
		t.Fatalf("CredHub entries = %d, want 1", len(scan.Material.CredHub))
	}

	c := scan.Material.CredHub[0]
	if c.Err != "" {
		t.Fatalf("Err = %q", c.Err)
	}

	v := c.Versions
	if v.DeployedVersionID != "v1" || v.LatestVersionID != "v2" || v.CertificateID != "cert-id" {
		t.Errorf("versions = %+v, want deployed v1, latest v2, certificate cert-id", v)
	}

	if !v.Transitional || v.SigningCA != caName {
		t.Errorf("transitional %v signing CA %q", v.Transitional, v.SigningCA)
	}

	if v.DeployedNotAfter == nil || !v.DeployedNotAfter.Equal(v1.cert.NotAfter) ||
		v.LatestNotAfter == nil || !v.LatestNotAfter.Equal(v2.cert.NotAfter) {
		t.Errorf("not-after dates = %v / %v", v.DeployedNotAfter, v.LatestNotAfter)
	}

	if c.Deployed == nil || Fingerprint(c.Deployed) != Fingerprint(v1.cert) {
		t.Error("Deployed is not version v1")
	}

	if c.Latest == nil || Fingerprint(c.Latest) != Fingerprint(v2.cert) {
		t.Error("Latest is not version v2")
	}

	if len(c.Chain) != 1 || Fingerprint(c.Chain[0]) != Fingerprint(ca.cert) {
		t.Errorf("chain = %d certificates, want the CA", len(c.Chain))
	}

	if got := scan.Material.SigningCAs[caName]; got == nil || Fingerprint(got) != Fingerprint(ca.cert) {
		t.Errorf("signing CAs = %v, want the services CA", scan.Material.SigningCAs)
	}
}

func TestScanInstanceExpectedListeners(t *testing.T) {
	t.Parallel()

	f := scannerFixture{
		director: &fakeDirector{
			manifest: loadManifest(t, "redis-cluster-7.yml"),
			vars:     []bosh.Variable{{ID: "v1", Name: "/ws-bosh/redis-7a1b/redis_cluster_crt"}},
			vms:      redisVMs(3),
		},
		state:    credhub.StateNotConfigured,
		settings: DefaultSettings(),
		leaf:     &fakeLeaf{},
	}

	scan := f.scanner().ScanInstance(context.Background(), testRef("redis-7a1b"))
	if scan.Err != "" {
		t.Fatalf("scan failed: %s", scan.Err)
	}

	exp := scan.Material.Expected
	if len(exp) != 3 {
		t.Fatalf("expected listeners = %+v, want 3", exp)
	}

	seen := map[string]bool{}

	for _, e := range exp {
		if e.Key != "/ws-bosh/redis-7a1b/redis_cluster_crt" {
			t.Errorf("expected listener key = %q", e.Key)
		}

		if seen[e.Locator] {
			t.Errorf("duplicate locator %q", e.Locator)
		}

		seen[e.Locator] = true
	}

	probes := probeObservations(scan)
	if len(probes) != 3 {
		t.Fatalf("probe observations = %d, want 3", len(probes))
	}

	for _, p := range probes {
		if p.State != StateFailed || p.Listener == nil || p.Listener.Variable != "redis_cluster_crt" {
			t.Errorf("failed probe %s: state %q listener %+v", p.Locator, p.State, p.Listener)
		}
	}
}

func TestScanInstanceProbeDisabled(t *testing.T) {
	t.Parallel()

	leaf := &fakeLeaf{}
	settings := DefaultSettings()
	settings.ProbeEnabled = false

	f := scannerFixture{
		director: &fakeDirector{
			manifest: loadManifest(t, "redis-cluster-7.yml"),
			vars:     []bosh.Variable{{ID: "v1", Name: "/ws-bosh/redis-7a1b/redis_cluster_crt"}},
			vms:      redisVMs(3),
		},
		state:    credhub.StateNotConfigured,
		settings: settings,
		leaf:     leaf,
	}

	scan := f.scanner().ScanInstance(context.Background(), testRef("redis-7a1b"))
	if len(probeObservations(scan)) != 0 || len(scan.Material.Expected) != 0 || len(leaf.dialed()) != 0 {
		t.Errorf("probes ran while disabled: %d observations, %d expected, dialed %v",
			len(probeObservations(scan)), len(scan.Material.Expected), leaf.dialed())
	}
}

func TestScanInstanceTimeoutKeepsNothingNew(t *testing.T) {
	t.Parallel()

	block := make(chan struct{})
	t.Cleanup(func() { close(block) })

	f := scannerFixture{
		director: &fakeDirector{manifest: loadManifest(t, "redis-cluster-7.yml"), block: block},
		state:    credhub.StateNotConfigured,
		settings: DefaultSettings(),
		leaf:     &fakeLeaf{},
	}

	s := f.scanner()
	s.timeout = 200 * time.Millisecond

	start := time.Now()
	scan := s.ScanInstance(context.Background(), testRef("redis-7a1b"))

	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("scan took %v despite a 200ms timeout", elapsed)
	}

	if !strings.Contains(scan.Err, "timed out") {
		t.Errorf("Err = %q, want a timeout", scan.Err)
	}

	m := scan.Material
	if len(m.CredHub) != 0 || len(m.Raw) != 0 || len(m.Expected) != 0 || len(m.SigningCAs) != 0 || len(m.Hosts) != 0 ||
		scan.Facts != nil || scan.VMs != nil || scan.Variables != nil {
		t.Errorf("a timed-out scan kept material: %+v", scan)
	}

	if !scan.LastSuccessAt.IsZero() || scan.ScannedAt.IsZero() || scan.Ref.ID != testInstanceID {
		t.Errorf("scan times or ref wrong: %+v", scan)
	}
}

func TestScanInstanceSkipsVMsWithoutIPs(t *testing.T) {
	t.Parallel()

	leaf := &fakeLeaf{}
	f := scannerFixture{
		director: &fakeDirector{
			manifest: loadManifest(t, "redis-cluster-7.yml"),
			vars:     []bosh.Variable{{ID: "v1", Name: "/ws-bosh/redis-7a1b/redis_cluster_crt"}},
			vms: []bosh.VM{
				{CID: "vm-0", Job: "redis", Index: 0, IPs: []string{"10.0.0.1"}},
				{CID: "", Job: "redis", Index: 1},
			},
		},
		state:    credhub.StateNotConfigured,
		settings: DefaultSettings(),
		leaf:     leaf,
	}

	scan := f.scanner().ScanInstance(context.Background(), testRef("redis-7a1b"))
	if scan.Err != "" {
		t.Fatalf("scan failed: %s", scan.Err)
	}

	if got := leaf.dialed(); len(got) != 1 || got[0] != "10.0.0.1:6379" {
		t.Errorf("dialed %v, want only 10.0.0.1:6379", got)
	}

	var found bool

	for _, p := range probeObservations(scan) {
		if !strings.HasPrefix(p.Locator, "redis/1") {
			continue
		}

		found = true

		if p.State != StateFailed || !strings.Contains(p.Error, "no address") {
			t.Errorf("VM without IPs: state %q error %q", p.State, p.Error)
		}

		if p.Listener == nil || p.Listener.Variable != "redis_cluster_crt" {
			t.Errorf("VM without IPs: listener %+v", p.Listener)
		}
	}

	if !found {
		t.Error("no observation for the VM without an address")
	}
}

func TestScanInstanceReadsOnlyHostFields(t *testing.T) {
	t.Parallel()

	v := newFakeVault()
	v.docs[testInstanceID+"/credentials"] = []byte(`{"host":"10.0.0.1","hosts":["a.example","10.0.0.1","b.example"],"password":"hunter2","port":6379}`)

	f := scannerFixture{
		director: &fakeDirector{
			manifest: loadManifest(t, "redis-cluster-7.yml"),
			vars:     []bosh.Variable{{ID: "v1", Name: "/ws-bosh/redis-7a1b/redis_cluster_crt"}},
			vms:      redisVMs(3),
		},
		state:    credhub.StateNotConfigured,
		vault:    v,
		settings: DefaultSettings(),
		leaf:     &fakeLeaf{},
	}

	scan := f.scanner().ScanInstance(context.Background(), testRef("redis-7a1b"))
	if scan.Err != "" {
		t.Fatalf("scan failed: %s", scan.Err)
	}

	want := []string{"10.0.0.1", "a.example", "b.example"}
	if fmt.Sprint(scan.Material.Hosts) != fmt.Sprint(want) {
		t.Errorf("Hosts = %v, want %v", scan.Material.Hosts, want)
	}

	raw, err := json.Marshal(scan) //nolint:musttag // The test checks the default encoding for key material.
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(string(raw), "hunter2") || strings.Contains(fmt.Sprintf("%+v", scan), "hunter2") {
		t.Error("the scan kept the binding password")
	}
}

func TestScanInstanceHostsAcceptsSingleString(t *testing.T) {
	t.Parallel()

	v := newFakeVault()
	v.docs[testInstanceID+"/credentials"] = []byte(`{"hosts":"only.example","host":{"not":"a string"}}`)

	f := scannerFixture{
		director: &fakeDirector{manifest: loadManifest(t, "redis-cluster-7.yml"), vms: redisVMs(1)},
		state:    credhub.StateNotConfigured,
		vault:    v,
		settings: DefaultSettings(),
		leaf:     &fakeLeaf{},
	}

	scan := f.scanner().ScanInstance(context.Background(), testRef("redis-7a1b"))
	if fmt.Sprint(scan.Material.Hosts) != "[only.example]" {
		t.Errorf("Hosts = %v", scan.Material.Hosts)
	}
}

func TestScanInstanceNeverRetainsKeys(t *testing.T) {
	t.Parallel()

	ca := issue(t, issueSpec{cn: "services-ca", serial: 1, isCA: true})
	leaf := issue(t, issueSpec{cn: "redis.bosh", serial: 2, parent: &ca})
	name := "/ws-bosh/redis-7a1b/redis_cluster_crt"

	ch := &fakeCredHub{
		meta: map[string]*credhub.CertificateMeta{
			name: {ID: "cert-id", Name: name, Versions: []credhub.CertificateVersionMeta{{ID: "v1", ExpiryDate: leaf.cert.NotAfter}}},
		},
		versions: map[string]*credhub.CertificatePublic{
			"v1": {VersionID: "v1", Name: name, Certificate: certPEM(leaf.cert) + keyPEM(t, leaf), CA: certPEM(ca.cert) + keyPEM(t, ca)},
		},
	}

	f := scannerFixture{
		director: &fakeDirector{
			manifest: loadManifest(t, "redis-cluster-7.yml"),
			vars:     []bosh.Variable{{ID: "v1", Name: name}},
			vms:      redisVMs(3),
		},
		credhub:  ch,
		state:    credhub.StateConfigured,
		settings: DefaultSettings(),
		leaf:     &fakeLeaf{cert: leaf.cert},
	}

	scan := f.scanner().ScanInstance(context.Background(), testRef("redis-7a1b"))
	if scan.Err != "" {
		t.Fatalf("scan failed: %s", scan.Err)
	}

	if len(scan.Material.CredHub) != 1 || scan.Material.CredHub[0].Deployed == nil {
		t.Fatalf("CredHub = %+v, want the deployed certificate", scan.Material.CredHub)
	}

	raw, err := json.Marshal(scan) //nolint:musttag // The test checks the default encoding for key material.
	if err != nil {
		t.Fatal(err)
	}

	for _, text := range []string{string(raw), fmt.Sprintf("%+v", scan), fmt.Sprintf("%+v", scan.Material)} {
		if strings.Contains(text, "PRIVATE KEY") {
			t.Fatal("the scan holds a private key")
		}
	}
}

func TestScanInstanceDirectorFailure(t *testing.T) {
	t.Parallel()

	f := scannerFixture{
		director: &fakeDirector{manifest: "not: [valid"},
		state:    credhub.StateNotConfigured,
		settings: DefaultSettings(),
		leaf:     &fakeLeaf{},
	}

	scan := f.scanner().ScanInstance(context.Background(), testRef("redis-7a1b"))
	if scan.Err == "" || len(scan.Material.Raw) != 0 {
		t.Errorf("Err = %q raw %d, want a failure with no material", scan.Err, len(scan.Material.Raw))
	}
}

func TestScanInstanceDeploymentFromPlanAndID(t *testing.T) {
	t.Parallel()

	d := &fakeDirector{manifest: loadManifest(t, "redis-cluster-7.yml")}
	f := scannerFixture{director: d, state: credhub.StateNotConfigured, settings: DefaultSettings(), leaf: &fakeLeaf{}}

	ref := testRef("")
	_ = f.scanner().ScanInstance(context.Background(), ref)

	d.mu.Lock()
	defer d.mu.Unlock()

	if len(d.calls) == 0 || d.calls[0] != "deployment plan-1-inst-1" {
		t.Errorf("director calls = %v, want the deployment plan-1-inst-1", d.calls)
	}
}

func TestDescribeCredHubError(t *testing.T) {
	t.Parallel()

	rows := []struct {
		err    error
		prefix string
		outage bool
	}{
		{&credhub.APIError{Op: "x", Status: http.StatusServiceUnavailable}, "CredHub unavailable", true},
		{&credhub.APIError{Op: "x", Status: http.StatusForbidden}, "CredHub unavailable", false},
		{fmt.Errorf("%w: %q", credhub.ErrNotFound, "n"), "CredHub has no", false},
		{context.DeadlineExceeded, "CredHub unavailable", true},
		{errors.New("dial tcp: connection refused"), "CredHub unavailable", true},
	}

	for _, r := range rows {
		msg, outage := describeCredHubError(r.err)
		if !strings.HasPrefix(msg, r.prefix) || outage != r.outage {
			t.Errorf("%v: %q outage %v, want prefix %q outage %v", r.err, msg, outage, r.prefix, r.outage)
		}
	}
}

func TestScanInstanceLatestByCreationTime(t *testing.T) {
	t.Parallel()

	caOld := issue(t, issueSpec{cn: "services-ca", serial: 1, isCA: true, notAfter: 48 * time.Hour})
	caNew := issue(t, issueSpec{cn: "services-ca", serial: 2, isCA: true, notAfter: 96 * time.Hour})
	v1 := issue(t, issueSpec{cn: "redis.bosh", serial: 3, parent: &caOld})
	v2 := issue(t, issueSpec{cn: "redis.bosh", serial: 4, parent: &caNew, notAfter: 72 * time.Hour})

	name := "/ws-bosh/redis-7a1b/redis_cluster_crt"
	caName := "/ws-bosh/ws-blacksmith/blacksmith_services_ca"
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

	// The metadata lists every version oldest first, so only the creation
	// times say which one is newest.
	ch := &fakeCredHub{
		meta: map[string]*credhub.CertificateMeta{
			name: {ID: "cert-id", Name: name, SignedBy: caName, Versions: []credhub.CertificateVersionMeta{
				{ID: "v1", ExpiryDate: v1.cert.NotAfter},
				{ID: "v2", ExpiryDate: v2.cert.NotAfter},
			}},
			caName: {ID: "ca-id", Name: caName, Versions: []credhub.CertificateVersionMeta{
				{ID: "ca1", CertificateAuthority: true},
				{ID: "ca2", CertificateAuthority: true},
			}},
		},
		created: map[string][]credhub.CredentialVersion{
			name:   {{ID: "v1", CreatedAt: base}, {ID: "v2", CreatedAt: base.Add(time.Hour)}},
			caName: {{ID: "ca1", CreatedAt: base}, {ID: "ca2", CreatedAt: base.Add(time.Hour)}},
		},
		versions: map[string]*credhub.CertificatePublic{
			"v1":  {VersionID: "v1", Name: name, Certificate: certPEM(v1.cert), CA: certPEM(caOld.cert)},
			"v2":  {VersionID: "v2", Name: name, Certificate: certPEM(v2.cert), CA: certPEM(caNew.cert)},
			"ca1": {VersionID: "ca1", Name: caName, Certificate: certPEM(caOld.cert), CA: certPEM(caOld.cert)},
			"ca2": {VersionID: "ca2", Name: caName, Certificate: certPEM(caNew.cert), CA: certPEM(caNew.cert)},
		},
	}

	f := scannerFixture{
		director: &fakeDirector{
			manifest: loadManifest(t, "redis-cluster-7.yml"),
			vars:     []bosh.Variable{{ID: "v1", Name: name}},
			vms:      redisVMs(3),
		},
		credhub:  ch,
		state:    credhub.StateConfigured,
		settings: DefaultSettings(),
		leaf:     &fakeLeaf{},
	}

	scan := f.scanner().ScanInstance(context.Background(), testRef("redis-7a1b"))
	if scan.Err != "" || len(scan.Material.CredHub) != 1 {
		t.Fatalf("scan err %q, CredHub %d entries", scan.Err, len(scan.Material.CredHub))
	}

	c := scan.Material.CredHub[0]
	if c.Versions.LatestVersionID != "v2" || c.Versions.DeployedVersionID != "v1" {
		t.Errorf("versions = %+v, want deployed v1 and latest v2", c.Versions)
	}

	if c.Versions.LatestNotAfter == nil || !c.Versions.LatestNotAfter.Equal(v2.cert.NotAfter) {
		t.Errorf("latest not-after = %v, want v2's", c.Versions.LatestNotAfter)
	}

	if c.Latest == nil || Fingerprint(c.Latest) != Fingerprint(v2.cert) {
		t.Error("Latest is not version v2")
	}

	if got := scan.Material.SigningCAs[caName]; got == nil || Fingerprint(got) != Fingerprint(caNew.cert) {
		t.Error("the signing CA's current certificate is not its newest version")
	}
}

// failingVault fails every read with an error that quotes a secret, the way
// a decoder error might.
type failingVault struct {
	*fakeVault
}

func (failingVault) Get(context.Context, string, interface{}) (bool, error) {
	return false, errors.New(`decode {"password":"hunter2"}: unexpected end`)
}

// coverageFor groups a scan's material and returns the coverage of the
// certificate with the given key.
func coverageFor(t *testing.T, m ScanMaterial, key string) Coverage {
	t.Helper()

	certs, _ := Group(m.CredHub, m.Raw)
	for i := range certs {
		if certs[i].Key == key {
			return coverageOf(&certs[i], 0)
		}
	}

	t.Fatalf("no certificate has key %q", key)

	return Coverage{}
}

func findRaw(scan InstanceScan, src Source, locator string) (RawObservation, bool) {
	for _, r := range scan.Material.Raw {
		if r.Source == src && r.Locator == locator {
			return r, true
		}
	}

	return RawObservation{}, false
}

func TestScanInstanceRecordsFailedReads(t *testing.T) {
	t.Parallel()

	ca := issue(t, issueSpec{cn: "services-ca", serial: 1, isCA: true, notAfter: 48 * time.Hour})
	v1 := issue(t, issueSpec{cn: "redis.bosh", serial: 2, parent: &ca})

	name := "/ws-bosh/redis-7a1b/redis_cluster_crt"
	caName := "/ws-bosh/ws-blacksmith/blacksmith_services_ca"
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

	// v2 is newest but cannot be read, and the signing CA is unknown to
	// CredHub.
	ch := &fakeCredHub{
		meta: map[string]*credhub.CertificateMeta{
			name: {ID: "cert-id", Name: name, SignedBy: caName, Versions: []credhub.CertificateVersionMeta{
				{ID: "v1", ExpiryDate: v1.cert.NotAfter},
				{ID: "v2"},
			}},
		},
		created: map[string][]credhub.CredentialVersion{
			name: {{ID: "v1", CreatedAt: base}, {ID: "v2", CreatedAt: base.Add(time.Hour)}},
		},
		versions: map[string]*credhub.CertificatePublic{
			"v1": {VersionID: "v1", Name: name, Certificate: certPEM(v1.cert), CA: certPEM(ca.cert)},
		},
	}

	f := scannerFixture{
		director: &fakeDirector{
			manifest: loadManifest(t, "redis-cluster-7.yml"),
			vars:     []bosh.Variable{{ID: "v1", Name: name}},
			vms:      redisVMs(3),
		},
		credhub:  ch,
		state:    credhub.StateConfigured,
		settings: DefaultSettings(),
		leaf:     &fakeLeaf{},
	}

	s := f.scanner()
	s.deps.Vault = failingVault{newFakeVault()}

	scan := s.ScanInstance(context.Background(), testRef("redis-7a1b"))
	if scan.Err != "" {
		t.Fatalf("scan failed: %s", scan.Err)
	}

	if c := scan.Material.CredHub[0]; c.Err != "" || c.Deployed == nil {
		t.Errorf("the deployed version should still read: err %q", c.Err)
	}

	latest, ok := findRaw(scan, SourceCredHub, name)
	if !ok || latest.State != StateFailed || !strings.Contains(latest.Error, "latest version") ||
		latest.Listener == nil || latest.Listener.Variable != "redis_cluster_crt" || latest.Owner != wantOwner("redis-7a1b") {
		t.Errorf("latest-version failure = %+v, %v", latest, ok)
	}

	signer, ok := findRaw(scan, SourceCredHub, caName)
	if !ok || signer.State != StateFailed || !strings.Contains(signer.Error, "signing CA") ||
		signer.Listener == nil || signer.Listener.Variable != "redis_cluster_crt" {
		t.Errorf("signing-CA failure = %+v, %v", signer, ok)
	}

	hosts, ok := findRaw(scan, SourceVault, testInstanceID+"/credentials")
	if !ok || hosts.State != StateFailed || hosts.Error == "" || hosts.Owner != wantOwner("redis-7a1b") {
		t.Errorf("Vault hosts failure = %+v, %v", hosts, ok)
	}

	raw, err := json.Marshal(scan) //nolint:musttag // The test checks the default encoding for key material.
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(string(raw), "hunter2") {
		t.Error("a Vault error leaked a secret value into the scan")
	}

	if cov := coverageFor(t, scan.Material, name); cov.Complete || cov.SourceStates[SourceCredHub] != StateFailed {
		t.Errorf("coverage = %+v, want incomplete with CredHub failed", cov)
	}
}

func TestScanInstanceMissingCredentialsIsNotAFailure(t *testing.T) {
	t.Parallel()

	f := scannerFixture{
		director: &fakeDirector{
			manifest: loadManifest(t, "redis-cluster-7.yml"),
			vars:     []bosh.Variable{{ID: "v1", Name: "/ws-bosh/redis-7a1b/redis_cluster_crt"}},
			vms:      redisVMs(3),
		},
		state:    credhub.StateNotConfigured,
		vault:    newFakeVault(),
		settings: DefaultSettings(),
		leaf:     &fakeLeaf{},
	}

	scan := f.scanner().ScanInstance(context.Background(), testRef("redis-7a1b"))
	if _, ok := findRaw(scan, SourceVault, testInstanceID+"/credentials"); ok {
		t.Error("an instance without stored credentials got a failed Vault read")
	}
}

func TestScanInstanceVariablesFailureKeepsTheRest(t *testing.T) {
	t.Parallel()

	served := issue(t, issueSpec{cn: "rabbitmq.bosh", serial: 7})
	port := serveTLS(t, served)

	dep := "rabbitmq-1"
	name := "/ws-bosh/rabbitmq-1/rabbitmq_crt"
	ch := &fakeCredHub{}
	f := scannerFixture{
		director: &fakeDirector{
			manifest: rabbitManifest(port),
			varsErr:  errors.New("director answered 500"),
			vms:      []bosh.VM{{CID: "vm-0", Job: "standalone", Index: 0, IPs: []string{"127.0.0.1"}}},
		},
		credhub:  ch,
		state:    credhub.StateConfigured,
		settings: DefaultSettings(),
	}

	scan := f.scanner().ScanInstance(context.Background(), testRef(dep))
	if scan.Err != "" {
		t.Fatalf("a failed variable list failed the whole scan: %s", scan.Err)
	}

	if len(scan.Material.CredHub) != 1 {
		t.Fatalf("CredHub = %+v, want the manifest's certificate marked failed", scan.Material.CredHub)
	}

	c := scan.Material.CredHub[0]
	if c.Name != name || c.Deployed != nil || !strings.Contains(c.Err, "variable") || !strings.Contains(c.Err, "director answered 500") {
		t.Errorf("CredHub entry = name %q err %q", c.Name, c.Err)
	}

	ch.mu.Lock()
	calls := len(ch.calls)
	ch.mu.Unlock()

	if calls != 0 {
		t.Errorf("CredHub was read %d times without the deployed versions", calls)
	}

	probes := probeObservations(scan)
	if len(probes) != 1 || probes[0].State != StateOK {
		t.Errorf("probes = %+v, want the listener still probed", probes)
	}

	if cov := coverageFor(t, scan.Material, name); cov.Complete || cov.SourceStates[SourceCredHub] != StateFailed {
		t.Errorf("coverage = %+v, want incomplete with CredHub failed", cov)
	}
}

func TestScanInstanceVMsFailureKeepsExpected(t *testing.T) {
	t.Parallel()

	leaf := &fakeLeaf{}
	f := scannerFixture{
		director: &fakeDirector{
			manifest: loadManifest(t, "redis-cluster-7.yml"),
			vars:     []bosh.Variable{{ID: "v1", Name: "/ws-bosh/redis-7a1b/redis_cluster_crt"}},
			vmsErr:   errors.New("director answered 502"),
		},
		state:    credhub.StateNotConfigured,
		settings: DefaultSettings(),
		leaf:     leaf,
	}

	scan := f.scanner().ScanInstance(context.Background(), testRef("redis-7a1b"))
	if scan.Err != "" {
		t.Fatalf("a failed VM list failed the whole scan: %s", scan.Err)
	}

	if len(scan.Material.Expected) != 3 {
		t.Errorf("expected listeners = %+v, want 3 from the manifest", scan.Material.Expected)
	}

	if got := leaf.dialed(); len(got) != 0 {
		t.Errorf("dialled %v without a VM list", got)
	}

	probes := probeObservations(scan)
	if len(probes) != 3 {
		t.Fatalf("probe observations = %d, want 3 failed", len(probes))
	}

	for _, p := range probes {
		if p.State != StateFailed || !strings.Contains(p.Error, "director answered 502") ||
			p.Listener == nil || p.Listener.Variable != "redis_cluster_crt" {
			t.Errorf("probe %s = state %q err %q listener %+v", p.Locator, p.State, p.Error, p.Listener)
		}
	}

	if len(scan.Material.CredHub) != 1 {
		t.Errorf("CredHub = %d entries, want the variable still listed", len(scan.Material.CredHub))
	}
}

func TestScanInstanceDropsJobProperties(t *testing.T) {
	t.Parallel()

	for _, probe := range []bool{true, false} {
		settings := DefaultSettings()
		settings.ProbeEnabled = probe

		f := scannerFixture{
			director: &fakeDirector{
				manifest: loadManifest(t, "redis-cluster-7.yml"),
				vars:     []bosh.Variable{{ID: "v1", Name: "/ws-bosh/redis-7a1b/redis_cluster_crt"}},
				vms:      redisVMs(3),
			},
			state:    credhub.StateNotConfigured,
			settings: settings,
			leaf:     &fakeLeaf{},
		}

		scan := f.scanner().ScanInstance(context.Background(), testRef("redis-7a1b"))
		if scan.Err != "" || scan.Facts == nil || len(scan.Facts.Jobs) == 0 {
			t.Fatalf("probe %v: scan = %q with facts %+v", probe, scan.Err, scan.Facts)
		}

		if probe && len(scan.Material.Expected) != 3 {
			t.Errorf("probe %v: expected listeners = %d, want 3 from the properties", probe, len(scan.Material.Expected))
		}

		for _, j := range scan.Facts.Jobs {
			if j.Properties != nil {
				t.Errorf("probe %v: job %s/%s still holds its properties", probe, j.Group, j.Job)
			}
		}
	}
}
