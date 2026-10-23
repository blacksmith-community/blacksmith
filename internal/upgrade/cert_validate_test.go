package upgrade

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"net/url"
	"strings"
	"testing"
	"time"
)

var validateNow = time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

type certSpec struct {
	cn        string
	dns       []string
	ips       []net.IP
	uris      []*url.URL
	emails    []string
	usages    []x509.ExtKeyUsage
	isCA      bool
	notBefore time.Time
	notAfter  time.Time
}

type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

func newTestCA(t *testing.T, name string) testCA {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: name},
		NotBefore:             validateNow.Add(-24 * time.Hour),
		NotAfter:              validateNow.Add(10 * 365 * 24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create CA: %v", err)
	}

	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse CA: %v", err)
	}

	return testCA{cert: cert, key: key}
}

func (ca testCA) issue(t *testing.T, spec certSpec) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: spec.cn},
		DNSNames:              spec.dns,
		IPAddresses:           spec.ips,
		URIs:                  spec.uris,
		EmailAddresses:        spec.emails,
		ExtKeyUsage:           spec.usages,
		NotBefore:             spec.notBefore,
		NotAfter:              spec.notAfter,
		IsCA:                  spec.isCA,
		BasicConstraintsValid: true,
	}

	if spec.isCA {
		template.KeyUsage = x509.KeyUsageCertSign
	}

	der, err := x509.CreateCertificate(rand.Reader, template, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatalf("issue certificate: %v", err)
	}

	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse certificate: %v", err)
	}

	return cert, key
}

func baseSpec() certSpec {
	return certSpec{
		cn:        "orders.ws.example",
		dns:       []string{"orders.ws.example", "b.ws.example"},
		ips:       []net.IP{net.ParseIP("10.0.0.5")},
		usages:    []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		notBefore: validateNow.Add(-time.Hour),
		notAfter:  validateNow.Add(30 * 24 * time.Hour),
	}
}

func findCheck(checks []Check, name string) (Check, bool) {
	for _, check := range checks {
		if check.Name == name {
			return check, true
		}
	}

	return Check{}, false
}

func TestValidateNew(t *testing.T) {
	t.Parallel()

	ca := newTestCA(t, "signing-ca")
	otherCA := newTestCA(t, "other-ca")

	oldCert, _ := ca.issue(t, baseSpec())

	reissue := func(change func(*certSpec)) certSpec {
		spec := baseSpec()
		spec.notAfter = validateNow.Add(365 * 24 * time.Hour)
		change(&spec)

		return spec
	}

	tests := []struct {
		name       string
		signer     testCA
		spec       certSpec
		wrongKey   bool
		nilKey     bool
		failedName string
		detailHas  string
	}{
		{name: "correct reissue", signer: ca, spec: reissue(func(*certSpec) {})},
		{name: "reissue in redeploy mode", signer: ca, spec: reissue(func(*certSpec) {}), nilKey: true},
		{name: "different common name", signer: ca, spec: reissue(func(s *certSpec) { s.cn = "other.ws.example" }), failedName: CheckCommonName},
		{name: "added SAN", signer: ca, spec: reissue(func(s *certSpec) { s.dns = append(s.dns, "orders2.ws.example") }), failedName: CheckSANs, detailHas: "orders2.ws.example"},
		{name: "missing clientAuth", signer: ca, spec: reissue(func(s *certSpec) { s.usages = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth} }), failedName: CheckExtKeyUsage},
		{name: "CA certificate", signer: ca, spec: reissue(func(s *certSpec) { s.isCA = true }), failedName: CheckNotCA},
		{name: "not before six minutes ahead", signer: ca, spec: reissue(func(s *certSpec) { s.notBefore = validateNow.Add(6 * time.Minute) }), failedName: CheckNotBefore},
		{name: "not before four minutes ahead", signer: ca, spec: reissue(func(s *certSpec) { s.notBefore = validateNow.Add(4 * time.Minute) })},
		{name: "earlier not after", signer: ca, spec: reissue(func(s *certSpec) { s.notAfter = validateNow.Add(24 * time.Hour) }), failedName: CheckNotAfter},
		{name: "signed by another CA", signer: otherCA, spec: reissue(func(*certSpec) {}), failedName: CheckCAVerify},
		{name: "mismatched key", signer: ca, spec: reissue(func(*certSpec) {}), wrongKey: true, failedName: CheckKeyMatch},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			newCert, newKey := test.signer.issue(t, test.spec)

			var key crypto.PrivateKey = newKey

			if test.wrongKey {
				other, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
				if err != nil {
					t.Fatalf("generate key: %v", err)
				}

				key = other
			}

			if test.nilKey {
				key = nil
			}

			checks := ValidateNew(oldCert, newCert, ca.cert, key, validateNow)

			for _, check := range checks {
				if want := check.Name != test.failedName; check.Passed != want {
					t.Errorf("check %s passed = %v, want %v (%s)", check.Name, check.Passed, want, check.Detail)
				}
			}

			if AllPassed(checks) != (test.failedName == "") {
				t.Errorf("AllPassed = %v for failed check %q", AllPassed(checks), test.failedName)
			}

			if test.failedName != "" {
				got, ok := findCheck(checks, test.failedName)
				if !ok || !strings.Contains(got.Detail, test.detailHas) {
					t.Errorf("detail %q should contain %q", got.Detail, test.detailHas)
				}
			}

			_, hasKey := findCheck(checks, CheckKeyMatch)
			if hasKey == test.nilKey {
				t.Errorf("key_match present = %v with nil key = %v", hasKey, test.nilKey)
			}
		})
	}
}

func TestAllPassedEmptyAndFailing(t *testing.T) {
	t.Parallel()

	if !AllPassed(nil) {
		t.Error("an empty list passes")
	}

	if AllPassed([]Check{{Name: "a", Passed: true}, {Name: "b"}}) {
		t.Error("a failing check must fail the list")
	}
}

func TestValidateNewMissingCertificateIsAParseFailure(t *testing.T) {
	t.Parallel()

	ca := newTestCA(t, "signing-ca")
	cert, _ := ca.issue(t, baseSpec())

	for name, pair := range map[string][2]*x509.Certificate{"no old": {nil, cert}, "no new": {cert, nil}} {
		checks := ValidateNew(pair[0], pair[1], ca.cert, nil, validateNow)

		if len(checks) != 1 || checks[0].Name != CheckParse || checks[0].Passed {
			t.Errorf("%s: checks = %+v, want one failed parse check", name, checks)
		}
	}
}

// mustURL parses a URI SAN for a test certificate.
func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()

	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}

	return u
}

// The SAN check compares typed, normalised sets of DNS, IP, URI, and
// email SANs, so a SAN of one type never stands in for another with the
// same text, and a SAN of any type that is added or dropped fails.
func TestSANCheckTypedSets(t *testing.T) {
	t.Parallel()

	ca := newTestCA(t, "signing-ca")

	spiffe := "spiffe://ws.example/orders"

	tests := []struct {
		name       string
		old, new   func(*certSpec)
		pass       bool
		detailHas  []string
		detailMiss string
	}{
		{
			name: "same SANs in another order",
			old: func(s *certSpec) {
				s.dns = []string{"a.ws.example", "b.ws.example"}
				s.ips = []net.IP{net.ParseIP("10.0.0.1"), net.ParseIP("10.0.0.2")}
			},
			new: func(s *certSpec) {
				s.dns = []string{"b.ws.example", "a.ws.example"}
				s.ips = []net.IP{net.ParseIP("10.0.0.2"), net.ParseIP("10.0.0.1")}
			},
			pass: true,
		},
		{
			name: "DNS names differ only in case and a trailing dot",
			old:  func(s *certSpec) { s.dns = []string{"Orders.WS.example"} },
			new:  func(s *certSpec) { s.dns = []string{"orders.ws.example."} },
			pass: true,
		},
		{
			name: "IPv4 and its IPv4-in-IPv6 form",
			old:  func(s *certSpec) { s.ips = []net.IP{net.ParseIP("10.0.0.5").To4()} },
			new:  func(s *certSpec) { s.ips = []net.IP{net.ParseIP("::ffff:10.0.0.5")} },
			pass: true,
		},
		{
			name: "an IP SAN replaced by a DNS SAN with the same text",
			old: func(s *certSpec) {
				s.dns = nil
				s.ips = []net.IP{net.ParseIP("10.0.0.1")}
			},
			new: func(s *certSpec) {
				s.dns = []string{"10.0.0.1"}
				s.ips = nil
			},
			detailHas: []string{"adds DNS 10.0.0.1", "drops IP 10.0.0.1"},
		},
		{
			name:      "a dropped URI SAN",
			old:       func(s *certSpec) { s.uris = []*url.URL{mustURL(t, spiffe)} },
			new:       func(s *certSpec) { s.uris = nil },
			detailHas: []string{"drops URI " + spiffe},
		},
		{
			name:      "an added URI SAN",
			old:       func(s *certSpec) { s.uris = nil },
			new:       func(s *certSpec) { s.uris = []*url.URL{mustURL(t, spiffe)} },
			detailHas: []string{"adds URI " + spiffe},
		},
		{
			name:      "a changed URI path",
			old:       func(s *certSpec) { s.uris = []*url.URL{mustURL(t, spiffe)} },
			new:       func(s *certSpec) { s.uris = []*url.URL{mustURL(t, spiffe+"-v2")} },
			detailHas: []string{"adds URI " + spiffe + "-v2", "drops URI " + spiffe},
		},
		{
			name: "URI scheme and host differ only in case",
			old:  func(s *certSpec) { s.uris = []*url.URL{mustURL(t, "SPIFFE://WS.example/orders")} },
			new:  func(s *certSpec) { s.uris = []*url.URL{mustURL(t, spiffe)} },
			pass: true,
		},
		{
			name:      "a dropped email SAN",
			old:       func(s *certSpec) { s.emails = []string{"ops@ws.example"} },
			new:       func(s *certSpec) { s.emails = nil },
			detailHas: []string{"drops email ops@ws.example"},
		},
		{
			name: "an email domain that differs only in case",
			old:  func(s *certSpec) { s.emails = []string{"ops@WS.example"} },
			new:  func(s *certSpec) { s.emails = []string{"ops@ws.example"} },
			pass: true,
		},
		{
			name:      "an email local part that differs in case",
			old:       func(s *certSpec) { s.emails = []string{"Ops@ws.example"} },
			new:       func(s *certSpec) { s.emails = []string{"ops@ws.example"} },
			detailHas: []string{"adds email ops@ws.example", "drops email Ops@ws.example"},
		},
		{
			name:      "an email SAN replaced by a DNS SAN with the same text",
			old:       func(s *certSpec) { s.emails = []string{"ops@ws.example"} },
			new:       func(s *certSpec) { s.dns = append(s.dns, "ops@ws.example") },
			detailHas: []string{"adds DNS ops@ws.example", "drops email ops@ws.example"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			oldSpec, newSpec := baseSpec(), baseSpec()
			newSpec.notAfter = validateNow.Add(365 * 24 * time.Hour)
			test.old(&oldSpec)
			test.new(&newSpec)

			oldCert, _ := ca.issue(t, oldSpec)
			newCert, _ := ca.issue(t, newSpec)

			got, ok := findCheck(ValidateNew(oldCert, newCert, ca.cert, nil, validateNow), CheckSANs)
			if !ok {
				t.Fatal("expected a SAN check")
			}

			if got.Passed != test.pass {
				t.Fatalf("SAN check passed = %v, want %v (%s)", got.Passed, test.pass, got.Detail)
			}

			for _, want := range test.detailHas {
				if !strings.Contains(got.Detail, want) {
					t.Errorf("detail %q should contain %q", got.Detail, want)
				}
			}
		})
	}
}
