package upgrade

import (
	"crypto"
	"crypto/x509"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"
)

// Names of the checks ValidateNew runs.
const (
	CheckParse       = "parse"
	CheckKeyMatch    = "key_match"
	CheckCAVerify    = "ca_verify"
	CheckCommonName  = "common_name"
	CheckSANs        = "sans"
	CheckExtKeyUsage = "ext_key_usage"
	CheckNotCA       = "not_ca"
	CheckNotBefore   = "not_before"
	CheckNotAfter    = "not_after"
)

// notBeforeSlack is how far ahead of now a new certificate may start, which
// allows for a little clock skew between CredHub and the broker.
const notBeforeSlack = 5 * time.Minute

// publicKeyer is what every standard private key type offers.
type publicKeyer interface {
	Public() crypto.PublicKey
}

// ValidateNew checks a reissued certificate against the one it replaces and
// against its signing CA. key is the new private key, or nil in redeploy
// mode, which has no key to hold. Then the key_match check is left out
// instead of failed.
func ValidateNew(oldCert, newCert, signingCA *x509.Certificate, key crypto.PrivateKey, now time.Time) []Check {
	if oldCert == nil || newCert == nil {
		return []Check{{Name: CheckParse, Detail: "The old or the new certificate is missing, so nothing can be compared."}}
	}

	var checks []Check

	if key != nil {
		checks = append(checks, keyMatchCheck(newCert, key))
	}

	return append(checks,
		caVerifyCheck(newCert, signingCA, now),
		commonNameCheck(oldCert, newCert),
		sanCheck(oldCert, newCert),
		extKeyUsageCheck(oldCert, newCert),
		notCACheck(newCert),
		notBeforeCheck(newCert, now),
		notAfterCheck(oldCert, newCert),
	)
}

// AllPassed reports whether every check passed. An empty list passes.
func AllPassed(checks []Check) bool {
	for _, check := range checks {
		if !check.Passed {
			return false
		}
	}

	return true
}

func pass(name, detail string) Check { return Check{Name: name, Passed: true, Detail: detail} }

func fail(name, detail string) Check { return Check{Name: name, Passed: false, Detail: detail} }

func keyMatchCheck(newCert *x509.Certificate, key crypto.PrivateKey) Check {
	holder, ok := key.(publicKeyer)
	if !ok {
		return fail(CheckKeyMatch, "The private key is of a type that cannot be compared with the certificate.")
	}

	matcher, ok := holder.Public().(interface{ Equal(crypto.PublicKey) bool })
	if !ok || !matcher.Equal(newCert.PublicKey) {
		return fail(CheckKeyMatch, "The private key does not match the new certificate's public key.")
	}

	return pass(CheckKeyMatch, "The private key matches the new certificate.")
}

func caVerifyCheck(newCert, signingCA *x509.Certificate, now time.Time) Check {
	if signingCA == nil {
		return fail(CheckCAVerify, "The signing CA is not available, so the new certificate cannot be verified.")
	}

	roots := x509.NewCertPool()
	roots.AddCert(signingCA)

	// A certificate that starts a few minutes from now is judged at its
	// start, because the not_before check owns that question.
	at := now
	if at.Before(newCert.NotBefore) {
		at = newCert.NotBefore
	}

	_, err := newCert.Verify(x509.VerifyOptions{
		Roots:       roots,
		CurrentTime: at,
		KeyUsages:   []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	})
	if err != nil {
		return fail(CheckCAVerify, "The new certificate does not verify against the current signing CA.")
	}

	return pass(CheckCAVerify, "The new certificate verifies against the current signing CA.")
}

func commonNameCheck(oldCert, newCert *x509.Certificate) Check {
	if oldCert.Subject.CommonName != newCert.Subject.CommonName {
		return fail(CheckCommonName, fmt.Sprintf("The common name changed from %q to %q.", oldCert.Subject.CommonName, newCert.Subject.CommonName))
	}

	return pass(CheckCommonName, "The common name is unchanged.")
}

// sanSet returns the certificate's SANs as a sorted, de-duplicated set of
// typed entries such as "DNS a.example" or "IP 10.0.0.1". The type prefix
// keeps a SAN of one kind from matching a SAN of another kind with the
// same text. Each value is normalised the way its kind compares: DNS names
// ignore case and a trailing dot, IP addresses compare by value, URIs
// ignore the case of the scheme and host, and email addresses ignore the
// case of the domain.
func sanSet(cert *x509.Certificate) []string {
	set := make([]string, 0, len(cert.DNSNames)+len(cert.IPAddresses)+len(cert.URIs)+len(cert.EmailAddresses))

	for _, name := range cert.DNSNames {
		set = append(set, "DNS "+strings.TrimSuffix(strings.ToLower(name), "."))
	}

	for _, ip := range cert.IPAddresses {
		set = append(set, "IP "+ip.String())
	}

	for _, uri := range cert.URIs {
		set = append(set, "URI "+normaliseURI(uri))
	}

	for _, email := range cert.EmailAddresses {
		set = append(set, "email "+normaliseEmail(email))
	}

	slices.Sort(set)

	return slices.Compact(set)
}

// normaliseURI lowercases the scheme and host of a URI SAN and leaves the
// rest, which is case-sensitive, as it is.
func normaliseURI(uri *url.URL) string {
	if uri == nil {
		return ""
	}

	norm := *uri
	norm.Scheme = strings.ToLower(norm.Scheme)
	norm.Host = strings.ToLower(norm.Host)

	return norm.String()
}

// normaliseEmail lowercases the domain of an email SAN. The local part is
// case-sensitive, so it stays as it is.
func normaliseEmail(email string) string {
	at := strings.LastIndex(email, "@")
	if at < 0 {
		return email
	}

	return email[:at] + "@" + strings.ToLower(email[at+1:])
}

func sanCheck(oldCert, newCert *x509.Certificate) Check {
	before, after := sanSet(oldCert), sanSet(newCert)
	if slices.Equal(before, after) {
		return pass(CheckSANs, "The SANs are unchanged.")
	}

	var added, removed []string

	for _, name := range after {
		if !slices.Contains(before, name) {
			added = append(added, name)
		}
	}

	for _, name := range before {
		if !slices.Contains(after, name) {
			removed = append(removed, name)
		}
	}

	detail := "SANs differ."
	if len(added) > 0 {
		detail += " The new certificate adds " + strings.Join(added, ", ") + "."
	}

	if len(removed) > 0 {
		detail += " The new certificate drops " + strings.Join(removed, ", ") + "."
	}

	return fail(CheckSANs, detail)
}

var extKeyUsageNames = map[x509.ExtKeyUsage]string{
	x509.ExtKeyUsageAny:             "any",
	x509.ExtKeyUsageServerAuth:      "serverAuth",
	x509.ExtKeyUsageClientAuth:      "clientAuth",
	x509.ExtKeyUsageCodeSigning:     "codeSigning",
	x509.ExtKeyUsageEmailProtection: "emailProtection",
	x509.ExtKeyUsageTimeStamping:    "timeStamping",
	x509.ExtKeyUsageOCSPSigning:     "ocspSigning",
}

// usageSet returns the sorted names of the extended key usages, with the
// OIDs of any the standard library does not know.
func usageSet(cert *x509.Certificate) []string {
	names := make([]string, 0, len(cert.ExtKeyUsage)+len(cert.UnknownExtKeyUsage))

	for _, usage := range cert.ExtKeyUsage {
		if name, ok := extKeyUsageNames[usage]; ok {
			names = append(names, name)
		} else {
			names = append(names, fmt.Sprintf("usage-%d", int(usage)))
		}
	}

	for _, oid := range cert.UnknownExtKeyUsage {
		names = append(names, oid.String())
	}

	slices.Sort(names)

	return names
}

func extKeyUsageCheck(oldCert, newCert *x509.Certificate) Check {
	before, after := usageSet(oldCert), usageSet(newCert)
	if slices.Equal(before, after) {
		return pass(CheckExtKeyUsage, "The extended key usages are unchanged.")
	}

	return fail(CheckExtKeyUsage, fmt.Sprintf("The extended key usages differ. The old certificate has %s and the new one has %s.", listOrNone(before), listOrNone(after)))
}

func listOrNone(items []string) string {
	if len(items) == 0 {
		return "none"
	}

	return strings.Join(items, ", ")
}

func notCACheck(newCert *x509.Certificate) Check {
	if newCert.IsCA {
		return fail(CheckNotCA, "The new certificate is a CA certificate, and a leaf rotation never issues one.")
	}

	return pass(CheckNotCA, "The new certificate is not a CA.")
}

func notBeforeCheck(newCert *x509.Certificate, now time.Time) Check {
	if newCert.NotBefore.After(now.Add(notBeforeSlack)) {
		return fail(CheckNotBefore, fmt.Sprintf("The new certificate is not valid until %s, which is more than five minutes from now.", newCert.NotBefore.UTC().Format(time.RFC3339)))
	}

	return pass(CheckNotBefore, "The new certificate is already valid.")
}

func notAfterCheck(oldCert, newCert *x509.Certificate) Check {
	if !newCert.NotAfter.After(oldCert.NotAfter) {
		return fail(CheckNotAfter, fmt.Sprintf("The new certificate expires at %s, which is no later than the old one at %s.", newCert.NotAfter.UTC().Format(time.RFC3339), oldCert.NotAfter.UTC().Format(time.RFC3339)))
	}

	return pass(CheckNotAfter, "The new certificate expires later than the old one.")
}
