package certinventory

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"blacksmith/internal/credhub"
)

// BrokerScanID is the reserved instance ID of the broker and platform scan.
const BrokerScanID = "_broker"

// Where the broker's own files live on its VM.
const (
	DefaultServicesCAPath = "/var/vcap/jobs/blacksmith/config/tls/blacksmith_services_ca"
	DefaultTrustStoreGlob = "/etc/ssl/certs/bosh-trusted-cert-*.pem"
)

// Platform endpoint names. Each is the InstanceName of its probe
// observation's owner and the key of its entry in PinnedFailures.
const (
	EndpointDirector = "director"
	EndpointUAA      = "uaa"
	EndpointCredHub  = "credhub"
)

// BrokerSources says where the broker's own certificates are. Each
// certificate field takes PEM text or the path of a PEM file, and an empty
// field is skipped.
type BrokerSources struct {
	HTTPSCertPEM  string
	BOSHCACert    string
	CredHubCACert string
	VaultCACert   string
	// CFAPICAs maps each CF API's name to its CA.
	CFAPICAs map[string]string
	// ServicesCAPath and TrustStoreGlob take their defaults when empty.
	ServicesCAPath string
	TrustStoreGlob string
	DirectorURL    string
	UAAURL         string
	CredHubURL     string
	// ServicesCAName is the absolute CredHub name of the services CA, read
	// from any instance manifest's options.ca, or empty when none names it.
	ServicesCAName string
}

// ScanBroker reads the broker's own certificates and probes the director,
// its UAA, and its CredHub. Each endpoint's chain is checked against the CA
// the broker pins for it, and a failure lands in PinnedFailures under the
// endpoint's name. The services CA file is compared with its CredHub copy.
// The scan runs under the same deadline as an instance scan.
func (s *Scanner) ScanBroker(ctx context.Context, src BrokerSources) InstanceScan {
	started := s.now()
	st := s.settings()
	timeout := s.scanTimeout(st)
	ref := InstanceRef{ID: BrokerScanID, Name: "broker"}

	scanCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	material := s.scanBroker(scanCtx, src, st)

	if err := scanCtx.Err(); err != nil {
		return InstanceScan{Ref: ref, ScannedAt: started, Err: scanFailure(ctx, scanCtx, err, timeout)}
	}

	return InstanceScan{Ref: ref, Material: material, ScannedAt: started, LastSuccessAt: started}
}

func (s *Scanner) scanBroker(ctx context.Context, src BrokerSources, st Settings) ScanMaterial {
	broker := Owner{Kind: OwnerBroker}

	var m ScanMaterial

	add := func(src Source, locator, value string) []*x509.Certificate {
		obs, certs := s.readSource(src, broker, locator, value)
		m.Raw = append(m.Raw, obs...)

		return certs
	}

	add(SourceBroker, "config:broker.tls.certificate", src.HTTPSCertPEM)

	servicesPath := src.ServicesCAPath
	if servicesPath == "" {
		servicesPath = DefaultServicesCAPath
	}

	// Outside a broker VM the services CA file does not exist, which is not
	// a failure worth an observation.
	var servicesCAs []*x509.Certificate
	if _, err := os.Stat(servicesPath); !errors.Is(err, fs.ErrNotExist) {
		servicesCAs = add(SourceBroker, servicesPath, servicesPath)
	}

	add(SourceBroker, "config:bosh.cacert", src.BOSHCACert)
	add(SourceBroker, "config:credhub.ca_cert", src.CredHubCACert)
	add(SourceBroker, "config:vault.cacert", src.VaultCACert)

	cfNames := make([]string, 0, len(src.CFAPICAs))
	for name := range src.CFAPICAs {
		cfNames = append(cfNames, name)
	}

	sort.Strings(cfNames)

	for _, name := range cfNames {
		add(SourceBroker, "config:broker.cf.apis."+name+".cacert", src.CFAPICAs[name])
	}

	glob := src.TrustStoreGlob
	if glob == "" {
		glob = DefaultTrustStoreGlob
	}

	// A malformed pattern matches nothing.
	files, _ := filepath.Glob(glob)
	for _, f := range files {
		add(SourceTrustStore, f, f)
	}

	if src.ServicesCAName != "" && s.credhubBlocked() == "" {
		ch := s.readServicesCA(ctx, src.ServicesCAName, broker)
		m.CredHub = append(m.CredHub, ch)
		m.ServicesCADrift = ch.Deployed != nil && len(servicesCAs) > 0 && !holds(servicesCAs, ch.Deployed)
	}

	if st.ProbeEnabled {
		obs, pinned := s.probeEndpoints(ctx, src)
		m.Raw = append(m.Raw, obs...)

		if len(pinned) > 0 {
			m.PinnedFailures = pinned
		}
	}

	return m
}

// readSource reads one configured value, which is PEM text or a path, and
// returns an observation for each certificate in it, or one failed
// observation when it holds none. An empty value gives nothing.
func (s *Scanner) readSource(src Source, owner Owner, locator, value string) ([]RawObservation, []*x509.Certificate) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}

	at := s.now()

	certs, err := loadPEM(value)
	if err == nil && len(certs) == 0 {
		err = errors.New("it holds no certificate")
	}

	if err != nil {
		return []RawObservation{{Observation: Observation{
			Source: src, Owner: owner, Locator: locator, State: StateFailed, Error: err.Error(), ObservedAt: at,
		}}}, nil
	}

	out := make([]RawObservation, 0, len(certs))
	for _, c := range certs {
		out = append(out, RawObservation{Observation: certObservation(src, owner, locator, c, at), Cert: c})
	}

	return out, certs
}

// loadPEM parses value as PEM text when it holds a PEM block, and otherwise
// reads it as the path of a PEM file. Only certificate blocks are kept, and
// the file's bytes are cleared once parsed in case it also holds a key.
func loadPEM(value string) ([]*x509.Certificate, error) {
	if strings.Contains(value, "-----BEGIN") {
		return ParsePEMCertificates([]byte(value))
	}

	data, err := os.ReadFile(filepath.Clean(value))
	if err != nil {
		var pathErr *fs.PathError
		if errors.As(err, &pathErr) {
			return nil, fmt.Errorf("read %s: %w", pathErr.Path, pathErr.Err)
		}

		return nil, fmt.Errorf("read the file: %w", err)
	}

	defer clear(data)

	return ParsePEMCertificates(data)
}

func holds(certs []*x509.Certificate, want *x509.Certificate) bool {
	fp := Fingerprint(want)
	for _, c := range certs {
		if Fingerprint(c) == fp {
			return true
		}
	}

	return false
}

// readServicesCA reads the current version of the services CA from CredHub.
// No instance deploys it, so its current version stands as the deployed one.
func (s *Scanner) readServicesCA(ctx context.Context, name string, owner Owner) CredHubCert {
	ch := CredHubCert{Name: name, Owner: owner}

	meta, err := s.metadata(ctx, name)
	if err != nil {
		ch.Err, _ = describeCredHubError(err)

		return ch
	}

	ch.Versions.CertificateID = meta.ID
	ch.Versions.SigningCA = meta.SignedBy

	if len(meta.Versions) == 0 || meta.Versions[0].ID == "" {
		ch.Err = "CredHub lists no version of this certificate"

		return ch
	}

	current := meta.Versions[0]
	ch.Versions.DeployedVersionID = current.ID
	ch.Versions.LatestVersionID = current.ID
	ch.Versions.Transitional = current.Transitional

	cert, chain, msg, _ := s.readVersion(ctx, current.ID)
	if msg != "" {
		ch.Err = msg

		return ch
	}

	notAfter := cert.NotAfter
	ch.Deployed, ch.Latest, ch.Chain = cert, cert, chain
	ch.Versions.DeployedNotAfter = &notAfter
	ch.Versions.LatestNotAfter = &notAfter

	return ch
}

type endpoint struct {
	name string
	url  string
	pin  string
}

// probeEndpoints probes the platform endpoints at once and checks each
// served chain against its pinned CA. An endpoint that does not answer is a
// failed probe, not a pin failure, and an endpoint with no pin is not
// checked.
func (s *Scanner) probeEndpoints(ctx context.Context, src BrokerSources) ([]RawObservation, map[string]string) {
	var endpoints []endpoint

	for _, e := range []endpoint{
		{EndpointDirector, src.DirectorURL, src.BOSHCACert},
		{EndpointUAA, src.UAAURL, src.BOSHCACert},
		{EndpointCredHub, src.CredHubURL, src.CredHubCACert},
	} {
		if strings.TrimSpace(e.url) != "" {
			endpoints = append(endpoints, e)
		}
	}

	obs := make([]RawObservation, len(endpoints))
	pinned := make([]string, len(endpoints))

	var wg sync.WaitGroup

	for i, e := range endpoints {
		wg.Add(1)

		go func() {
			defer wg.Done()

			obs[i], pinned[i] = s.probeEndpoint(ctx, e)
		}()
	}

	wg.Wait()

	failures := map[string]string{}

	for i, e := range endpoints {
		if pinned[i] != "" {
			failures[e.name] = pinned[i]
		}
	}

	return obs, failures
}

func (s *Scanner) probeEndpoint(ctx context.Context, e endpoint) (RawObservation, string) {
	owner := Owner{Kind: OwnerPlatform, InstanceName: e.name}

	failed := func(locator string, err error) RawObservation {
		return RawObservation{Observation: Observation{
			Source: SourceProbe, Owner: owner, Locator: locator,
			State: StateFailed, Error: err.Error(), ObservedAt: s.now(),
		}}
	}

	host, addr, err := endpointAddr(e.url)
	if err != nil {
		return failed(e.name, err), ""
	}

	locator := e.name + " " + addr

	leaf, intermediates, err := s.leaf(ctx, addr, host)
	if err != nil {
		return failed(locator, err), ""
	}

	obs := RawObservation{Observation: certObservation(SourceProbe, owner, locator, leaf, s.now()), Cert: leaf}

	return obs, s.checkPin(leaf, intermediates, e.pin)
}

// checkPin returns why leaf does not chain to the pinned CA, or an empty
// string when it does or when nothing is pinned.
func (s *Scanner) checkPin(leaf *x509.Certificate, intermediates []*x509.Certificate, pin string) string {
	if strings.TrimSpace(pin) == "" {
		return ""
	}

	cas, err := loadPEM(pin)
	if err != nil {
		return "the pinned CA could not be read: " + err.Error()
	}

	if len(cas) == 0 {
		return "the pinned CA holds no certificate"
	}

	roots := x509.NewCertPool()
	for _, c := range cas {
		roots.AddCert(c)
	}

	if err := VerifyChain(leaf, intermediates, roots, s.now()); err != nil {
		return err.Error()
	}

	return ""
}

// endpointAddr turns a URL into the host for SNI and the address to dial.
// An https URL without a port uses 443.
func endpointAddr(raw string) (string, string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", "", errors.New("the endpoint URL could not be parsed")
	}

	host := u.Hostname()
	if host == "" {
		return "", "", errors.New("the endpoint URL has no host")
	}

	port := u.Port()
	if port == "" {
		port = "443"
	}

	return host, net.JoinHostPort(host, port), nil
}

// metadata reads a certificate's metadata under the CredHub call deadline.
func (s *Scanner) metadata(ctx context.Context, name string) (*credhub.CertificateMeta, error) {
	meta, err := within(ctx, CredHubCallTimeout, func(c context.Context) (*credhub.CertificateMeta, error) {
		return s.deps.CredHub.CertificateMetadata(c, name)
	})
	if err == nil && meta == nil {
		return nil, fmt.Errorf("%w: %q", credhub.ErrNotFound, name)
	}

	return meta, err
}
