package certinventory

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"reflect"
	"sort"
	"strconv"
	"sync"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/credhub"
	"blacksmith/internal/interfaces"
	"blacksmith/internal/upgrade/certscripts"
)

// Deadlines for one scan. The instance deadline comes from the settings;
// these bound each call inside it.
const (
	DirectorCallTimeout = 20 * time.Second
	CredHubCallTimeout  = 10 * time.Second
	vaultCallTimeout    = 10 * time.Second

	// maxProbesInFlight bounds the probes one instance runs at once, so a
	// cluster whose listeners all hang still finishes inside its deadline.
	maxProbesInFlight = 8

	msgCredHubNotConfigured = "CredHub not configured" //nolint:gosec // A status message, not a credential.
	msgCredHubUnavailable   = "CredHub unavailable"    //nolint:gosec // A status message, not a credential.
)

// DirectorReader is the part of the BOSH director the scanner reads.
type DirectorReader interface {
	GetInfo(ctx context.Context) (*bosh.Info, error)
	GetDeployment(ctx context.Context, name string) (*bosh.DeploymentDetail, error)
	GetDeploymentVMs(ctx context.Context, deployment string) ([]bosh.VM, error)
	ListVariables(ctx context.Context, deployment string) ([]bosh.Variable, error)
}

// CredHubReader is the part of the CredHub client the scanner reads. No
// method ever returns a private key. CertificateMetadata keeps CredHub's
// order of versions, which says nothing about age, so the newest version
// always comes from CredentialVersions, which sorts by creation time.
type CredHubReader interface {
	CertificateMetadata(ctx context.Context, name string) (*credhub.CertificateMeta, error)
	CertificateVersion(ctx context.Context, versionID string) (*credhub.CertificatePublic, error)
	CredentialVersions(ctx context.Context, name string, limit int) ([]credhub.CredentialVersion, error)
}

// InstanceRef names one service instance and what the catalog calls its
// service and plan.
type InstanceRef struct {
	ID          string
	Name        string
	Deployment  string
	ServiceID   string
	PlanID      string
	ServiceName string
	PlanName    string
}

// ExpectedListener is one TLS listener the manifest says should exist. Key
// is the absolute CredHub name of the certificate the listener should serve,
// and it is empty when the listener rules cannot tell which variable that is.
type ExpectedListener struct {
	Key     string
	Locator string
}

// ScanMaterial is what one scan saw, before any grouping. Everything in it
// is public: certificates, names, and messages, never a key or a password.
type ScanMaterial struct {
	CredHub []CredHubCert
	Raw     []RawObservation
	// Expected lists every listener the manifest promises, one per instance
	// of the group. It is empty when probing is turned off.
	Expected []ExpectedListener
	// SigningCAs maps the absolute CredHub name of each signing CA to its
	// current certificate.
	SigningCAs map[string]*x509.Certificate
	// Hosts holds the names the instance's binding credentials hand to apps.
	Hosts []string
	// PinnedFailures maps a platform endpoint name to the message of its
	// failed pinned-CA check. Only the broker scan fills it.
	PinnedFailures map[string]string
	// ServicesCADrift says the broker's services CA file differs from the
	// copy in CredHub. Only the broker scan sets it.
	ServicesCADrift bool
}

// InstanceScan is the result of scanning one instance. A failed scan carries
// Err and no material.
type InstanceScan struct {
	Ref      InstanceRef
	Material ScanMaterial
	// Facts is never marshalled, because job properties can hold passwords.
	Facts     *ManifestFacts `json:"-"`
	VMs       []bosh.VM
	Variables []bosh.Variable
	// HealthGate names the health check that patch mode runs between the
	// VMs of a cluster, as certscripts.GateFor gives it for the service
	// and the VM count. It is empty for a multi-VM service with no gate,
	// and when the VM list could not be read.
	HealthGate string
	// ScannedAt is when the scan began.
	ScannedAt     time.Time
	Err           string
	LastSuccessAt time.Time
	Stale         bool
}

// ScannerDeps is everything a Scanner reads from.
type ScannerDeps struct {
	Director DirectorReader
	// CredHub may be nil when the broker has no CredHub block.
	CredHub      CredHubReader
	CredHubState func() (credhub.CapabilityState, string)
	Vault        interfaces.Vault
	Prober       Prober
	// DirectorName is the name in every CredHub path the director owns.
	// When it is empty the scanner asks the director once.
	DirectorName string
	Settings     func() Settings
	Now          func() time.Time
}

type leafFunc func(ctx context.Context, addr, serverName string) (*x509.Certificate, []*x509.Certificate, error)

// Scanner reads one instance, or the broker, into an InstanceScan. It is
// safe for concurrent use.
type Scanner struct {
	deps ScannerDeps
	leaf leafFunc
	// timeout replaces the instance deadline from the settings when set.
	timeout time.Duration

	mu           sync.Mutex
	directorName string
}

// NewScanner returns a scanner over deps.
func NewScanner(deps ScannerDeps) *Scanner {
	return &Scanner{deps: deps, leaf: deps.Prober.Leaf, directorName: deps.DirectorName}
}

func (s *Scanner) now() time.Time {
	if s.deps.Now != nil {
		return s.deps.Now()
	}

	return time.Now()
}

func (s *Scanner) settings() Settings {
	if s.deps.Settings != nil {
		return s.deps.Settings()
	}

	return DefaultSettings()
}

func (s *Scanner) scanTimeout(st Settings) time.Duration {
	if s.timeout > 0 {
		return s.timeout
	}

	if st.InstanceScanTimeoutSeconds > 0 {
		return time.Duration(st.InstanceScanTimeoutSeconds) * time.Second
	}

	return time.Duration(DefaultSettings().InstanceScanTimeoutSeconds) * time.Second
}

// ScanInstance reads one instance's certificates from the director, CredHub,
// Vault, and its TLS listeners. The whole scan runs under the instance
// deadline. When the scan fails or the deadline passes, the result carries
// Err and none of the material read so far, so a caller keeps what it had.
func (s *Scanner) ScanInstance(ctx context.Context, ref InstanceRef) InstanceScan {
	started := s.now()
	st := s.settings()
	timeout := s.scanTimeout(st)

	scanCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	scan, err := s.scanInstance(scanCtx, ref, st)
	if err == nil {
		// A deadline that passed while probes ran leaves a partial scan.
		err = scanCtx.Err()
	}

	if err != nil {
		return InstanceScan{Ref: ref, ScannedAt: started, Err: scanFailure(ctx, scanCtx, err, timeout)}
	}

	scan.Ref = ref
	scan.ScannedAt = started
	scan.LastSuccessAt = started

	return scan
}

// scanFailure words a failed scan. The instance deadline and a cancelled
// caller get their own messages, because the call error alone only says
// "context deadline exceeded".
func scanFailure(parent, scanCtx context.Context, err error, timeout time.Duration) string {
	switch {
	case parent.Err() != nil:
		return "the scan was cancelled"
	case errors.Is(scanCtx.Err(), context.DeadlineExceeded):
		return fmt.Sprintf("the instance scan timed out after %s", timeout)
	}

	return err.Error()
}

func instanceOwner(ref InstanceRef, deployment string) Owner {
	return Owner{
		Kind:         OwnerInstance,
		InstanceID:   ref.ID,
		InstanceName: ref.Name,
		Deployment:   deployment,
		ServiceName:  ref.ServiceName,
		PlanName:     ref.PlanName,
	}
}

func deploymentOf(ref InstanceRef) string {
	if ref.Deployment != "" {
		return ref.Deployment
	}

	return ref.PlanID + "-" + ref.ID
}

func (s *Scanner) scanInstance(ctx context.Context, ref InstanceRef, st Settings) (InstanceScan, error) {
	deployment := deploymentOf(ref)

	director, err := s.director(ctx)
	if err != nil {
		return InstanceScan{}, err
	}

	detail, err := within(ctx, DirectorCallTimeout, func(c context.Context) (*bosh.DeploymentDetail, error) {
		return s.deps.Director.GetDeployment(c, deployment)
	})
	if err != nil {
		return InstanceScan{}, fmt.Errorf("read the manifest of %s: %w", deployment, err)
	}

	if detail == nil {
		return InstanceScan{}, fmt.Errorf("read the manifest of %s: the director returned nothing", deployment)
	}

	facts, err := WalkManifest(detail.Manifest)
	if err != nil {
		return InstanceScan{}, fmt.Errorf("read the manifest of %s: %w", deployment, err)
	}

	// Without the manifest nothing else can be read, but a failed variable
	// or VM list only marks its own sources failed, and the scan answers
	// with the rest.
	vars, varsErr := within(ctx, DirectorCallTimeout, func(c context.Context) ([]bosh.Variable, error) {
		return s.deps.Director.ListVariables(c, deployment)
	})

	vms, vmsErr := within(ctx, DirectorCallTimeout, func(c context.Context) ([]bosh.VM, error) {
		return s.deps.Director.GetDeploymentVMs(c, deployment)
	})

	owner := instanceOwner(ref, deployment)
	scan := InstanceScan{Facts: facts, VMs: vms, Variables: vars, HealthGate: certscripts.GateFor(ref.ServiceName, len(vms))}

	scan.Material.Raw = literalObservations(facts, owner, s.now())

	if varsErr != nil {
		scan.Material.CredHub = unreadCertificates(facts, director, deployment, owner,
			"the director's variable list could not be read: "+varsErr.Error())
	} else {
		var failed []RawObservation

		scan.Material.CredHub, scan.Material.SigningCAs, failed = s.readCredHub(ctx, director, deployment, facts, vars, owner)
		scan.Material.Raw = append(scan.Material.Raw, failed...)
	}

	hosts, hostsFailed := s.readHosts(ctx, ref.ID, owner)
	scan.Material.Hosts = hosts

	if hostsFailed != nil {
		scan.Material.Raw = append(scan.Material.Raw, *hostsFailed)
	}

	if st.ProbeEnabled {
		listeners := ListenerRules(facts)
		scan.Material.Expected = expectedListeners(listeners, facts, vms, director, deployment)

		if vmsErr != nil {
			scan.Material.Raw = append(scan.Material.Raw, s.unprobed(listeners, facts, owner,
				errors.New("the director's VM list could not be read: "+vmsErr.Error()))...)
		} else {
			scan.Material.Raw = append(scan.Material.Raw, s.probeVMs(ctx, listeners, vms, owner)...)
		}
	}

	// The listener rules were the last reader of the job properties, which
	// can hold passwords, so the cached facts never keep them.
	for i := range facts.Jobs {
		facts.Jobs[i].Properties = nil
	}

	return scan, nil
}

// director returns the director's name, asking the director once when the
// deps did not give it.
func (s *Scanner) director(ctx context.Context) (string, error) {
	s.mu.Lock()
	name := s.directorName
	s.mu.Unlock()

	if name != "" {
		return name, nil
	}

	info, err := within(ctx, DirectorCallTimeout, s.deps.Director.GetInfo)
	if err != nil {
		return "", fmt.Errorf("read the director's name: %w", err)
	}

	if info == nil || info.Name == "" {
		return "", errors.New("read the director's name: the director did not give one")
	}

	s.mu.Lock()
	s.directorName = info.Name
	s.mu.Unlock()

	return info.Name, nil
}

// within runs call under its own deadline and returns when either the call
// or the deadline finishes. A call that ignores its context keeps running in
// the background, but the scan no longer waits for it, and its result is
// dropped.
func within[T any](ctx context.Context, d time.Duration, call func(context.Context) (T, error)) (T, error) {
	callCtx, cancel := context.WithTimeout(ctx, d)
	defer cancel()

	type result struct {
		v   T
		err error
	}

	done := make(chan result, 1)

	go func() {
		v, err := call(callCtx)
		done <- result{v, err}
	}()

	select {
	case r := <-done:
		return r.v, r.err
	case <-callCtx.Done():
		var zero T

		return zero, callCtx.Err()
	}
}

func literalObservations(facts *ManifestFacts, owner Owner, at time.Time) []RawObservation {
	out := make([]RawObservation, 0, len(facts.Literals))

	for _, lit := range facts.Literals {
		if lit.Cert == nil {
			continue
		}

		out = append(out, RawObservation{
			Observation: certObservation(SourceManifest, owner, lit.Path, lit.Cert, at),
			Cert:        lit.Cert,
		})
	}

	return out
}

// readFailure records a read that failed, so grouping keeps it and coverage
// reads incomplete. When the read belongs to one certificate variable, the
// observation names that variable, which places it in the variable's group;
// otherwise it gets a group of its own. msg must never hold a secret value.
func (s *Scanner) readFailure(src Source, owner Owner, locator, variable, msg string) RawObservation {
	r := RawObservation{Observation: Observation{
		Source: src, Owner: owner, Locator: locator, State: StateFailed, Error: msg, ObservedAt: s.now(),
	}}

	if variable != "" {
		r.Listener = &Listener{Variable: lastSegment(variable)}
	}

	return r
}

func certObservation(src Source, owner Owner, locator string, c *x509.Certificate, at time.Time) Observation {
	notAfter := c.NotAfter

	return Observation{
		Source:      src,
		Owner:       owner,
		Locator:     locator,
		Fingerprint: Fingerprint(c),
		Serial:      c.SerialNumber.Text(16),
		NotAfter:    &notAfter,
		State:       StateOK,
		ObservedAt:  at,
	}
}

// isNil reports a nil interface, and also a nil pointer stored in one,
// which compares unequal to nil but panics on the first call.
func isNil(v any) bool {
	if v == nil {
		return true
	}

	rv := reflect.ValueOf(v)

	//nolint:exhaustive // Only the nilable kinds matter; every other kind is never nil.
	switch rv.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan, reflect.Interface:
		return rv.IsNil()
	}

	return false
}

// credhubBlocked returns the reason CredHub cannot be read at all, or an
// empty string when it can.
func (s *Scanner) credhubBlocked() string {
	if isNil(s.deps.CredHub) {
		return msgCredHubNotConfigured
	}

	if s.deps.CredHubState == nil {
		return ""
	}

	state, reason := s.deps.CredHubState()

	switch state {
	case credhub.StateConfigured:
		return ""
	case credhub.StateNotConfigured:
		return msgCredHubNotConfigured
	case credhub.StateUnreachable, credhub.StateRefused:
		// These fall through to the unavailable message below.
	}

	if reason == "" {
		return msgCredHubUnavailable
	}

	return msgCredHubUnavailable + ": " + reason
}

// describeCredHubError words a failed CredHub read for a certificate, and
// says whether CredHub as a whole looks down, in which case the scan stops
// calling it for the rest of the instance.
func describeCredHubError(err error) (string, bool) {
	if errors.Is(err, credhub.ErrNotFound) {
		return "CredHub has no such certificate", false
	}

	var apiErr *credhub.APIError
	if errors.As(err, &apiErr) {
		outage := apiErr.Status >= http.StatusInternalServerError || apiErr.Status == http.StatusUnauthorized

		return msgCredHubUnavailable + ": " + apiErr.Error(), outage
	}

	return msgCredHubUnavailable + ": " + err.Error(), true
}

// certificateNames returns the absolute names of every certificate variable
// the manifest declares or references, and the signing CA of each.
func certificateNames(facts *ManifestFacts, director, deployment string) (map[string]bool, map[string]string) {
	names := map[string]bool{}

	for _, v := range facts.CertVariables {
		names[ResolveCredHubName(director, deployment, v)] = true
	}

	for _, job := range facts.Jobs {
		for _, v := range job.CertRefs {
			names[ResolveCredHubName(director, deployment, v)] = true
		}
	}

	signers := map[string]string{}

	for v, ca := range facts.SigningCAs {
		if ca != "" {
			signers[ResolveCredHubName(director, deployment, v)] = ResolveCredHubName(director, deployment, ca)
		}
	}

	return names, signers
}

// unreadCertificates lists every certificate variable the manifest names,
// each failed with msg, for a scan that cannot tell which versions are
// deployed.
func unreadCertificates(facts *ManifestFacts, director, deployment string, owner Owner, msg string) []CredHubCert {
	names, signers := certificateNames(facts, director, deployment)

	sorted := make([]string, 0, len(names))
	for name := range names {
		sorted = append(sorted, name)
	}

	sort.Strings(sorted)

	out := make([]CredHubCert, 0, len(sorted))
	for _, name := range sorted {
		out = append(out, CredHubCert{Name: name, Owner: owner, Versions: Versions{SigningCA: signers[name]}, Err: msg})
	}

	return out
}

// readCredHub builds one CredHubCert for each certificate variable the
// director says the deployment uses. The director's list works without
// CredHub, so every certificate is present even when CredHub is not. It also
// returns a failed observation for each latest version or signing CA that
// could not be read.
func (s *Scanner) readCredHub(ctx context.Context, director, deployment string, facts *ManifestFacts,
	vars []bosh.Variable, owner Owner,
) ([]CredHubCert, map[string]*x509.Certificate, []RawObservation) {
	names, signers := certificateNames(facts, director, deployment)
	blocked := s.credhubBlocked()

	sorted := append([]bosh.Variable(nil), vars...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	var (
		out    []CredHubCert
		failed []RawObservation
		down   string
		seen   = map[string]bool{}
	)

	for _, v := range sorted {
		name := ResolveCredHubName(director, deployment, v.Name)
		if !names[name] || seen[name] {
			continue
		}

		seen[name] = true

		ch := CredHubCert{
			Name:     name,
			Owner:    owner,
			Versions: Versions{DeployedVersionID: v.ID, SigningCA: signers[name]},
		}

		switch {
		case blocked != "":
			ch.Err = blocked
		case down != "":
			ch.Err = down
		default:
			msg, outage := s.readCertificate(ctx, &ch)
			if outage {
				down = msg
			}

			// A message without Err means only the latest version failed.
			if msg != "" && ch.Err == "" {
				failed = append(failed, s.readFailure(SourceCredHub, owner, name, name, "the latest version could not be read: "+msg))
			}
		}

		out = append(out, ch)
	}

	if blocked != "" {
		return out, nil, failed
	}

	cas, caFailed := s.readSigningCAs(ctx, out, down)

	return out, cas, append(failed, caFailed...)
}

// readCertificate fills ch from CredHub. It returns the failure message and
// whether CredHub looks down.
func (s *Scanner) readCertificate(ctx context.Context, ch *CredHubCert) (string, bool) {
	meta, err := within(ctx, CredHubCallTimeout, func(c context.Context) (*credhub.CertificateMeta, error) {
		return s.deps.CredHub.CertificateMetadata(c, ch.Name)
	})
	if err == nil && meta == nil {
		err = fmt.Errorf("%w: %q", credhub.ErrNotFound, ch.Name)
	}

	if err != nil {
		msg, outage := describeCredHubError(err)
		ch.Err = msg

		return msg, outage
	}

	ch.Versions.CertificateID = meta.ID
	if meta.SignedBy != "" {
		ch.Versions.SigningCA = meta.SignedBy
	}

	if mv, ok := versionMeta(meta, ch.Versions.DeployedVersionID); ok {
		ch.Versions.Transitional = mv.Transitional
	}

	latestID, latestMsg, outage := s.newestVersion(ctx, ch.Name)
	if outage {
		ch.Err = latestMsg

		return latestMsg, true
	}

	ch.Versions.LatestVersionID = latestID

	if mv, ok := versionMeta(meta, latestID); ok && !mv.ExpiryDate.IsZero() {
		t := mv.ExpiryDate
		ch.Versions.LatestNotAfter = &t
	}

	if ch.Versions.DeployedVersionID == "" {
		ch.Err = "the director names no deployed version of this certificate"

		return ch.Err, false
	}

	deployed, chain, msg, outage := s.readVersion(ctx, ch.Versions.DeployedVersionID)
	if msg != "" {
		ch.Err = msg

		return msg, outage
	}

	ch.Deployed, ch.Chain = deployed, chain
	t := deployed.NotAfter
	ch.Versions.DeployedNotAfter = &t

	// When the newest version cannot be named, Latest stays empty. The
	// deployed version still gives the expiry, so the certificate is not
	// failed.
	if latestID == "" {
		return latestMsg, false
	}

	if latestID == ch.Versions.DeployedVersionID {
		ch.Latest = deployed
		ch.Versions.LatestNotAfter = &t

		return "", false
	}

	latest, _, msg, outage := s.readVersion(ctx, latestID)
	if msg != "" {
		return msg, outage
	}

	ch.Latest = latest
	lt := latest.NotAfter
	ch.Versions.LatestNotAfter = &lt

	return "", false
}

// readVersion reads one version and parses its public certificates. The
// strings CredHub returned go out of scope here, and only parsed
// certificates are kept, so any other PEM block in them is dropped.
func (s *Scanner) readVersion(ctx context.Context, id string) (*x509.Certificate, []*x509.Certificate, string, bool) {
	pub, err := within(ctx, CredHubCallTimeout, func(c context.Context) (*credhub.CertificatePublic, error) {
		return s.deps.CredHub.CertificateVersion(c, id)
	})
	if err == nil && pub == nil {
		err = fmt.Errorf("%w: %q", credhub.ErrNotFound, id)
	}

	if err != nil {
		msg, outage := describeCredHubError(err)

		return nil, nil, msg, outage
	}

	certs, _ := ParsePEMCertificates([]byte(pub.Certificate))
	if len(certs) == 0 {
		return nil, nil, fmt.Sprintf("CredHub version %s holds no readable certificate", id), false
	}

	chain := certs[1:]

	if cas, _ := ParsePEMCertificates([]byte(pub.CA)); len(cas) > 0 {
		chain = append(chain, cas...)
	}

	return certs[0], chain, "", false
}

// readSigningCAs reads the current certificate of every CA that signs one of
// certs. A CA that cannot be read is left out, which skips the findings that
// compare against it, and each certificate it signs gets a failed
// observation so its coverage reads incomplete.
func (s *Scanner) readSigningCAs(ctx context.Context, certs []CredHubCert, down string) (map[string]*x509.Certificate, []RawObservation) {
	cas := map[string]*x509.Certificate{}
	unread := map[string]string{}

	known := map[string]*CredHubCert{}
	for i := range certs {
		known[certs[i].Name] = &certs[i]
	}

	var failed []RawObservation

	for _, c := range certs {
		name := c.Versions.SigningCA
		if name == "" || name == c.Name {
			continue
		}

		if _, done := cas[name]; !done {
			if _, tried := unread[name]; !tried {
				cur, msg := s.signingCA(ctx, name, known, &down)
				if cur != nil {
					cas[name] = cur
				} else {
					unread[name] = msg
				}
			}
		}

		if msg, bad := unread[name]; bad {
			failed = append(failed, s.readFailure(SourceCredHub, c.Owner, name, c.Name, "the signing CA could not be read: "+msg))
		}
	}

	if len(cas) == 0 {
		return nil, failed
	}

	return cas, failed
}

// signingCA returns a CA's current certificate, from the certificates
// already read when it is one of them, or why it cannot. A CredHub outage
// is recorded in down, and later CAs are not read.
func (s *Scanner) signingCA(ctx context.Context, name string, known map[string]*CredHubCert, down *string) (*x509.Certificate, string) {
	if k, ok := known[name]; ok {
		if cur := firstNonNil(k.Latest, k.Deployed); cur != nil {
			return cur, ""
		}

		if k.Err != "" {
			return nil, k.Err
		}

		return nil, "CredHub returned no certificate"
	}

	if *down != "" {
		return nil, *down
	}

	cur, msg, outage := s.readCurrent(ctx, name)
	if outage {
		*down = msg
	}

	if cur == nil && msg == "" {
		msg = "CredHub returned no certificate"
	}

	return cur, msg
}

// readCurrent reads the newest version of the named certificate.
func (s *Scanner) readCurrent(ctx context.Context, name string) (*x509.Certificate, string, bool) {
	id, msg, outage := s.newestVersion(ctx, name)
	if id == "" {
		return nil, msg, outage
	}

	cur, _, msg, outage := s.readVersion(ctx, id)

	return cur, msg, outage
}

// newestVersion returns the ID of the named credential's newest version by
// CredHub's creation time, or why it cannot, and whether CredHub looks down.
func (s *Scanner) newestVersion(ctx context.Context, name string) (string, string, bool) {
	versions, err := within(ctx, CredHubCallTimeout, func(c context.Context) ([]credhub.CredentialVersion, error) {
		return s.deps.CredHub.CredentialVersions(c, name, 1)
	})
	if err != nil {
		msg, outage := describeCredHubError(err)

		return "", msg, outage
	}

	if len(versions) == 0 || versions[0].ID == "" {
		return "", "CredHub lists no version of " + name, false
	}

	return versions[0].ID, "", false
}

// versionMeta finds one version in a certificate's metadata.
func versionMeta(meta *credhub.CertificateMeta, id string) (credhub.CertificateVersionMeta, bool) {
	if meta == nil || id == "" {
		return credhub.CertificateVersionMeta{}, false
	}

	for _, mv := range meta.Versions {
		if mv.ID == id {
			return mv, true
		}
	}

	return credhub.CertificateVersionMeta{}, false
}

func firstNonNil(certs ...*x509.Certificate) *x509.Certificate {
	for _, c := range certs {
		if c != nil {
			return c
		}
	}

	return nil
}

// bindingHosts decodes only the host fields of an instance's credentials.
// Every other field, passwords included, is skipped by the decoder and never
// held.
type bindingHosts struct {
	Host  json.RawMessage `json:"host"`
	Hosts json.RawMessage `json:"hosts"`
}

// readHosts returns the names the instance's binding credentials hand to
// apps. An instance with no stored credentials has none. A failed read gives
// none and a failed observation, whose message never quotes the error,
// because a decoder error can echo the secret it was reading.
func (s *Scanner) readHosts(ctx context.Context, instanceID string, owner Owner) ([]string, *RawObservation) {
	if isNil(s.deps.Vault) {
		return nil, nil
	}

	path := instanceID + "/credentials"

	creds, err := within(ctx, vaultCallTimeout, func(c context.Context) (bindingHosts, error) {
		var out bindingHosts

		found, err := s.deps.Vault.Get(c, path, &out)
		if err != nil || !found {
			return bindingHosts{}, err
		}

		return out, nil
	})
	if err != nil {
		msg := "the binding credentials could not be read from Vault"
		if errors.Is(err, context.DeadlineExceeded) {
			msg += ": the read timed out"
		}

		failed := s.readFailure(SourceVault, owner, path, "", msg)

		return nil, &failed
	}

	var hosts []string

	seen := map[string]bool{}
	add := func(h string) {
		if h != "" && !seen[h] {
			seen[h] = true
			hosts = append(hosts, h)
		}
	}

	for _, raw := range []json.RawMessage{creds.Host, creds.Hosts} {
		for _, h := range stringOrList(raw) {
			add(h)
		}
	}

	return hosts, nil
}

// stringOrList reads a JSON string or a list of strings. Anything else gives
// nothing.
func stringOrList(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}

	var one string
	if json.Unmarshal(raw, &one) == nil {
		return []string{one}
	}

	var many []string
	if json.Unmarshal(raw, &many) == nil {
		return many
	}

	return nil
}

// listenerFor returns a copy of l whose Variable is the bare variable name,
// which is what grouping compares against a CredHub name's last segment.
func listenerFor(l Listener) *Listener {
	c := l
	c.Variable = lastSegment(l.Variable)

	return &c
}

// expectedListeners lists one entry per listener and per instance of its
// group. The count is the larger of the manifest's instance count and the
// VMs the director reports, so a missing VM still counts as expected.
func expectedListeners(listeners []Listener, facts *ManifestFacts, vms []bosh.VM, director, deployment string) []ExpectedListener {
	vmCount := map[string]int{}
	for _, vm := range vms {
		vmCount[vm.Job]++
	}

	var out []ExpectedListener

	for _, l := range listeners {
		key := ""
		if l.Variable != "" {
			key = ResolveCredHubName(director, deployment, l.Variable)
		}

		n := max(facts.Groups[l.Group], vmCount[l.Group])
		for i := 0; i < n; i++ {
			out = append(out, ExpectedListener{
				Key:     key,
				Locator: l.Group + "/" + strconv.Itoa(i) + ":" + strconv.Itoa(l.Port),
			})
		}
	}

	return out
}

// unprobed fails every listener the manifest promises, one per instance of
// its group, for a scan that has no VMs to dial.
func (s *Scanner) unprobed(listeners []Listener, facts *ManifestFacts, owner Owner, err error) []RawObservation {
	var out []RawObservation

	for _, l := range listeners {
		for i := 0; i < facts.Groups[l.Group]; i++ {
			task := probeTask{vm: bosh.VM{Job: l.Group, Index: i}, listener: listenerFor(l)}
			out = append(out, s.failedProbe(task, owner, err))
		}
	}

	return out
}

type probeTask struct {
	vm       bosh.VM
	listener *Listener
}

// probeVMs dials every listener on every VM of its group, a few at a time,
// and returns one observation per pair in a stable order. A VM without an
// address gets a failed observation and is never dialled.
func (s *Scanner) probeVMs(ctx context.Context, listeners []Listener, vms []bosh.VM, owner Owner) []RawObservation {
	sorted := append([]bosh.VM(nil), vms...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Job != sorted[j].Job {
			return sorted[i].Job < sorted[j].Job
		}

		return sorted[i].Index < sorted[j].Index
	})

	var tasks []probeTask

	for _, vm := range sorted {
		for _, l := range listeners {
			if l.Group == vm.Job {
				tasks = append(tasks, probeTask{vm: vm, listener: listenerFor(l)})
			}
		}
	}

	out := make([]RawObservation, len(tasks))
	slots := make(chan struct{}, maxProbesInFlight)

	var wg sync.WaitGroup

	for i, task := range tasks {
		wg.Add(1)

		go func() {
			defer wg.Done()

			select {
			case slots <- struct{}{}:
				defer func() { <-slots }()
			case <-ctx.Done():
				out[i] = s.failedProbe(task, owner, ctx.Err())

				return
			}

			out[i] = s.probe(ctx, task, owner)
		}()
	}

	wg.Wait()

	return out
}

// target returns where a probe dials and how its observation is located.
// A VM without an address has an empty addr.
func (t probeTask) target() (locator, addr string) {
	vmName := t.vm.Job + "/" + strconv.Itoa(t.vm.Index)
	port := strconv.Itoa(t.listener.Port)

	if len(t.vm.IPs) == 0 || t.vm.IPs[0] == "" {
		return vmName + " port " + port, ""
	}

	addr = net.JoinHostPort(t.vm.IPs[0], port)

	return vmName + " " + addr, addr
}

func (s *Scanner) probe(ctx context.Context, task probeTask, owner Owner) RawObservation {
	locator, addr := task.target()
	if addr == "" {
		return s.failedProbe(task, owner, errors.New("the VM has no address to probe"))
	}

	leaf, _, err := s.leaf(ctx, addr, "")
	if err != nil {
		return s.failedProbe(task, owner, err)
	}

	return RawObservation{
		Observation: certObservation(SourceProbe, owner, locator, leaf, s.now()),
		Cert:        leaf,
		Listener:    task.listener,
	}
}

func (s *Scanner) failedProbe(task probeTask, owner Owner, err error) RawObservation {
	locator, _ := task.target()

	return RawObservation{
		Observation: Observation{
			Source:     SourceProbe,
			Owner:      owner,
			Locator:    locator,
			State:      StateFailed,
			Error:      err.Error(),
			ObservedAt: s.now(),
		},
		Listener: task.listener,
	}
}
