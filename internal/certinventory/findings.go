package certinventory

import (
	"bytes"
	"crypto/x509"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Finding codes that ApplyFindings raises. The weak key and weak signature
// codes live in parse.go.
const (
	FindingChainBroken        = "chain_broken"
	FindingServicesCADrift    = "services_ca_drift"
	FindingPinnedCAMismatch   = "pinned_ca_mismatch"
	FindingSigningCAChanged   = "signing_ca_changed"
	FindingIssuerExpiresFirst = "issuer_expires_first"
	FindingSANMismatch        = "san_mismatch"
	FindingServedMismatch     = "served_mismatch"
	FindingDeployPending      = "deploy_pending"
	FindingProbeFailed        = "probe_failed"

	servicesCAVariable = "blacksmith_services_ca"
	timeLayoutDate     = "2006-01-02"
)

// FindingInput is everything ApplyFindings needs beyond the certificates.
type FindingInput struct {
	// CAs maps a resolved CredHub name to that CA's current certificate.
	CAs map[string]*x509.Certificate
	// Reps maps a certificate ID to its representative public certificate,
	// which is the second value Group returns. It is the only place
	// ApplyFindings reads x509 data for a certificate.
	Reps map[string]*x509.Certificate
	// Hosts maps an instance ID to the names its binding credentials hand
	// to apps.
	Hosts map[string][]string
	// PinnedFailures maps a platform endpoint name ("director", "uaa", or
	// "credhub") to the message of its failed pinned-CA check.
	PinnedFailures map[string]string
	// ServicesCADrift says the broker's services CA file differs from the
	// director's CredHub copy.
	ServicesCADrift bool
	// Expected maps a certificate ID to its expected listener count.
	Expected map[string]int
	Now      time.Time
}

// ApplyFindings fills Findings and Coverage on every certificate. It raises
// every finding in spec 7.7 except patch_reverted and unconverged. A
// certificate with no entry in Reps gets none of the findings that need
// x509 data.
func ApplyFindings(certs []Certificate, in FindingInput) {
	for i := range certs {
		c := &certs[i]
		rep := in.Reps[c.ID]

		c.Coverage = coverageOf(c, in.Expected[c.ID])
		c.Findings = findingsOf(c, rep, in)
	}
}

func coverageOf(c *Certificate, expected int) Coverage {
	cov := Coverage{
		ExpectedListeners: expected,
		SourceStates:      map[Source]string{},
	}

	anyFailed := false

	for _, o := range c.Observations {
		if o.Source == SourceProbe {
			if o.State == StateFailed {
				cov.FailedListeners++
			} else {
				cov.ObservedListeners++
			}
		}

		if o.State == StateFailed {
			anyFailed = true
		}

		if o.State == StateStale {
			cov.Stale = true
		}

		cov.SourceStates[o.Source] = mergeState(cov.SourceStates[o.Source], o.State)

		if !o.ObservedAt.IsZero() && (cov.OldestObservation.IsZero() || o.ObservedAt.Before(cov.OldestObservation)) {
			cov.OldestObservation = o.ObservedAt
		}
	}

	// A failed probe has no NotAfter, so the effective expiry would ignore
	// it. Coverage must say so.
	cov.Complete = !anyFailed && cov.ObservedListeners >= expected

	return cov
}

// mergeState keeps the worst state seen for a source.
func mergeState(have, next string) string {
	rank := map[string]int{"": 0, StateOK: 1, StateStale: 2, StateFailed: 3}

	if next != StateStale && next != StateFailed {
		next = StateOK
	}

	if rank[next] > rank[have] {
		return next
	}

	return have
}

func findingsOf(c *Certificate, rep *x509.Certificate, in FindingInput) []Finding {
	var out []Finding

	if rep != nil {
		out = append(out, chainFindings(c, rep, in)...)
	}

	if in.ServicesCADrift && c.Variable == servicesCAVariable {
		out = append(out, Finding{
			Code: FindingServicesCADrift, Severity: SeverityError,
			Message: "The services CA in the broker's file differs from the copy in the director's CredHub.",
		})
	}

	out = append(out, pinnedFindings(c, in)...)

	if rep != nil {
		out = append(out, issuerFindings(c, rep, in)...)
		out = append(out, sanFindings(c, rep, in)...)
	}

	out = append(out, servedFindings(c)...)

	if rep != nil {
		out = append(out, WeaknessFindings(rep)...)
	}

	if f := deployPending(c); f != nil {
		out = append(out, *f)
	}

	out = append(out, probeFailures(c)...)

	return out
}

// chainFindings raises chain_broken and signing_ca_changed from the
// certificate's signing CA.
func chainFindings(c *Certificate, rep *x509.Certificate, in FindingInput) []Finding {
	if c.Versions == nil || c.Versions.SigningCA == "" || c.Versions.SigningCA == c.Key {
		return nil
	}

	ca := in.CAs[c.Versions.SigningCA]
	if ca == nil {
		return nil
	}

	var out []Finding

	roots := x509.NewCertPool()
	roots.AddCert(ca)

	if err := VerifyChain(rep, nil, roots, in.Now); err != nil && !onlyExpired(err) {
		out = append(out, Finding{
			Code: FindingChainBroken, Severity: SeverityError,
			Message: fmt.Sprintf("The certificate does not verify against its signing CA %s.", c.Versions.SigningCA),
		})
	}

	// CheckSignature skips the CA-flag checks that CheckSignatureFrom makes,
	// since this rule asks only who signed the leaf.
	if err := ca.CheckSignature(rep.SignatureAlgorithm, rep.RawTBSCertificate, rep.Signature); err != nil {
		out = append(out, Finding{
			Code: FindingSigningCAChanged, Severity: SeverityError,
			Message: fmt.Sprintf("The CA %s would sign a regenerated certificate, but it is not the CA that issued the deployed one.", c.Versions.SigningCA),
		})
	}

	return out
}

// onlyExpired reports a verification failure that is about validity dates.
// The expiry status already reports those, so they are not a broken chain.
func onlyExpired(err error) bool {
	var invalid x509.CertificateInvalidError

	return errors.As(err, &invalid) && invalid.Reason == x509.Expired
}

func pinnedFindings(c *Certificate, in FindingInput) []Finding {
	if len(in.PinnedFailures) == 0 {
		return nil
	}

	seen := map[string]bool{}

	var out []Finding

	for _, o := range c.Observations {
		if o.Source != SourceProbe || o.Owner.Kind != OwnerPlatform {
			continue
		}

		msg, failed := in.PinnedFailures[o.Owner.InstanceName]
		if !failed || seen[o.Owner.InstanceName] {
			continue
		}

		seen[o.Owner.InstanceName] = true

		out = append(out, Finding{
			Code: FindingPinnedCAMismatch, Severity: SeverityError,
			Message: fmt.Sprintf("The %s endpoint's certificate chain does not verify against the CA the broker pins for it (%s).", o.Owner.InstanceName, msg),
		})
	}

	return out
}

func issuerFindings(c *Certificate, rep *x509.Certificate, in FindingInput) []Finding {
	var issuer *x509.Certificate

	consider := func(cand *x509.Certificate) {
		if cand == nil || bytes.Equal(cand.Raw, rep.Raw) || !isIssuerOf(cand, rep) {
			return
		}

		if issuer == nil || cand.NotAfter.Before(issuer.NotAfter) {
			issuer = cand
		}
	}

	for _, id := range sortedKeys(in.Reps) {
		if id != c.ID {
			consider(in.Reps[id])
		}
	}

	for _, name := range sortedKeys(in.CAs) {
		consider(in.CAs[name])
	}

	if issuer == nil || !issuer.NotAfter.Before(rep.NotAfter) {
		return nil
	}

	return []Finding{{
		Code: FindingIssuerExpiresFirst, Severity: SeverityWarning,
		Message: fmt.Sprintf("The issuer %q expires on %s, before this certificate does on %s.",
			issuer.Subject.CommonName, issuer.NotAfter.UTC().Format(timeLayoutDate), rep.NotAfter.UTC().Format(timeLayoutDate)),
	}}
}

// isIssuerOf matches by key IDs when both exist and by names otherwise, then
// confirms the match with the signature.
func isIssuerOf(cand, leaf *x509.Certificate) bool {
	if len(leaf.AuthorityKeyId) > 0 && len(cand.SubjectKeyId) > 0 {
		if !bytes.Equal(leaf.AuthorityKeyId, cand.SubjectKeyId) {
			return false
		}
	} else if !bytes.Equal(leaf.RawIssuer, cand.RawSubject) {
		return false
	}

	return leaf.CheckSignatureFrom(cand) == nil
}

func sanFindings(c *Certificate, rep *x509.Certificate, in FindingInput) []Finding {
	if KindOf(rep) != KindLeaf || !servesTLS(rep) {
		return nil
	}

	var names []string

	for _, o := range c.Owners {
		if o.InstanceID != "" {
			names = append(names, in.Hosts[o.InstanceID]...)
		}
	}

	if len(names) == 0 {
		return nil
	}

	for _, n := range names {
		if rep.VerifyHostname(n) == nil {
			return nil
		}
	}

	return []Finding{{
		Code: FindingSANMismatch, Severity: SeverityWarning,
		Message: fmt.Sprintf("The names on this certificate cover none of the hosts that apps are told to use (%s).", strings.Join(names, ", ")),
	}}
}

// servesTLS is false for a leaf that is limited to client authentication,
// since a client certificate carries no host name.
func servesTLS(c *x509.Certificate) bool {
	if len(c.ExtKeyUsage) == 0 {
		return true
	}

	for _, u := range c.ExtKeyUsage {
		if u == x509.ExtKeyUsageServerAuth || u == x509.ExtKeyUsageAny {
			return true
		}
	}

	return false
}

func servedFindings(c *Certificate) []Finding {
	if c.Versions == nil || c.Divergence != nil {
		return nil
	}

	var out []Finding

	for _, o := range c.Observations {
		if o.Source == SourceProbe && o.State != StateFailed && o.Matches == MatchOther {
			out = append(out, Finding{
				Code: FindingServedMismatch, Severity: SeverityWarning,
				Message: fmt.Sprintf("%s serves a certificate other than the deployed one.", o.Locator),
			})
		}
	}

	return out
}

func deployPending(c *Certificate) *Finding {
	v := c.Versions
	if v == nil || v.DeployedVersionID == "" || v.LatestVersionID == "" || v.DeployedVersionID == v.LatestVersionID {
		return nil
	}

	return &Finding{
		Code: FindingDeployPending, Severity: SeverityInfo,
		Message: fmt.Sprintf("CredHub holds version %s, but the deployment still uses version %s.", v.LatestVersionID, v.DeployedVersionID),
	}
}

func probeFailures(c *Certificate) []Finding {
	var out []Finding

	for _, o := range c.Observations {
		if o.Source != SourceProbe || o.State != StateFailed {
			continue
		}

		msg := fmt.Sprintf("%s did not answer.", o.Locator)
		if o.Error != "" {
			msg = fmt.Sprintf("%s did not answer (%s).", o.Locator, o.Error)
		}

		out = append(out, Finding{Code: FindingProbeFailed, Severity: SeverityInfo, Message: msg})
	}

	return out
}

func sortedKeys(m map[string]*x509.Certificate) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}

	sort.Strings(keys)

	return keys
}
