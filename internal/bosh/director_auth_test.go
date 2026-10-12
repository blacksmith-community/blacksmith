package bosh_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/bosh/boshtest"
	"github.com/fivetwenty-io/boshapi/pkg/boshapi"
)

// privateCA is a certificate authority made for one test. It signs the
// certificates of the test's TLS servers, so a client that trusts only its
// PEM reaches every one of them, and nothing else.
type privateCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	// PEM is the CA certificate, to give the adapter as the director CA.
	PEM string
}

func newPrivateCA(t *testing.T) *privateCA {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate the CA key: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "blacksmith-test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create the CA certificate: %v", err)
	}

	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse the CA certificate: %v", err)
	}

	return &privateCA{cert: cert, key: key, PEM: string(pem.EncodeToMemory(&pem.Block{Type: pemCertificate, Bytes: der}))}
}

// serve starts a TLS server on the loopback whose certificate the CA signed,
// and closes it when the test ends.
func (ca *privateCA) serve(t *testing.T, serial int64, handler http.Handler) *httptest.Server {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate the server key: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(serial),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}

	der, err := x509.CreateCertificate(rand.Reader, template, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatalf("sign the server certificate: %v", err)
	}

	server := httptest.NewUnstartedServer(handler)
	server.TLS = &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
	}
	server.StartTLS()
	t.Cleanup(server.Close)

	return server
}

// The bosh block settings an adapter error names.
const (
	addressSetting = "bosh.address"
	caSetting      = "bosh.cacert (the release's bosh.ca_cert)"
	// allowHTTPSetting is how the plain HTTP refusal names the override.
	allowHTTPSetting = "bosh.allow_http (the release's bosh.allow_http)"
)

// loopbackDirector is a director address on the loopback that nothing
// serves. A test that only builds an adapter sends it nothing.
const loopbackDirector = "https://127.0.0.1:1"

// The routes a test director and a test UAA answer.
const (
	infoRoute  = "/info"
	grantRoute = "/oauth/token"
)

// directorInfoAnswer is a director's GET /info answer.
type directorInfoAnswer struct {
	Name               string                 `json:"name"`
	UUID               string                 `json:"uuid"`
	Version            string                 `json:"version"`
	User               *string                `json:"user"`
	CPI                string                 `json:"cpi"`
	UserAuthentication map[string]interface{} `json:"user_authentication"`
}

// directorInfo writes a director's GET /info answer that names how it
// authenticates.
func directorInfo(writer http.ResponseWriter, userAuth map[string]interface{}) {
	boshtest.WriteJSON(writer, http.StatusOK, directorInfoAnswer{
		Name: "test-director", UUID: "00000000-0000-0000-0000-000000000000",
		Version: "282.0.0 (00000000)", CPI: "test_cpi", UserAuthentication: userAuth,
	})
}

// authTypeField is the field of a director's user_authentication that
// names how it authenticates.
const authTypeField = "type"

// uaaAuthentication is the user_authentication of a director that sends its
// clients to the UAA at url.
func uaaAuthentication(url string) map[string]interface{} {
	return map[string]interface{}{authTypeField: bosh.AuthTypeUAA, "options": map[string]string{"url": url}}
}

// basicAuthentication is the user_authentication of a director that takes
// HTTP Basic credentials.
func basicAuthentication() map[string]interface{} {
	return map[string]interface{}{authTypeField: "basic"}
}

// uaaError is a UAA's JSON answer to a token request it refused.
type uaaError struct {
	Error       string `json:"error"`
	Description string `json:"error_description"`
}

// grantingUAA is a test UAA that grants a numbered client-credentials token,
// the nth of which lives for lifetimes[n-1] seconds, and records how each
// token request authenticated. A grant past the end of lifetimes lives as
// long as the last entry.
type grantingUAA struct {
	server    *httptest.Server
	grants    atomic.Int32
	clientFor sync.Map
}

func newGrantingUAA(t *testing.T, authority *privateCA, lifetimes ...int) *grantingUAA {
	t.Helper()

	if len(lifetimes) == 0 {
		t.Fatal("newGrantingUAA needs at least one token lifetime")
	}

	uaa := &grantingUAA{}
	uaa.server = authority.serve(t, 2, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != grantRoute {
			http.NotFound(writer, request)

			return
		}

		err := request.ParseForm()
		if err != nil || request.PostForm.Get("grant_type") != "client_credentials" {
			t.Errorf("token request form %v (%v), want grant_type client_credentials", request.PostForm, err)
		}

		grant := uaa.grants.Add(1)
		uaa.clientFor.Store(grant, request.Header.Get("Authorization"))

		lifetime := lifetimes[min(int(grant), len(lifetimes))-1]

		boshtest.WriteJSON(writer, http.StatusOK, map[string]interface{}{
			"access_token": "uaa-token-" + strconv.Itoa(int(grant)),
			"token_type":   "bearer",
			"expires_in":   lifetime,
		})
	}))

	return uaa
}

// headerLog records the Authorization header of each request in order.
type headerLog struct {
	lock    sync.Mutex
	headers []string
}

func (l *headerLog) add(request *http.Request) {
	l.lock.Lock()
	defer l.lock.Unlock()

	l.headers = append(l.headers, request.Header.Get("Authorization"))
}

func (l *headerLog) all() []string {
	l.lock.Lock()
	defer l.lock.Unlock()

	return append([]string(nil), l.headers...)
}

// Building an adapter sends nothing to the director. Authentication is
// worked out on the first call, under that call's context.
func TestNewDirectorAdapterMakesNoRequests(t *testing.T) {
	t.Parallel()

	server := boshtest.New(t, nil)

	_, err := bosh.NewDirectorAdapter(bosh.Config{
		Address:  server.URL,
		Username: basicAuthCredential,
		Password: basicAuthCredential,
		CACert:   server.CACert,
	})
	if err != nil {
		t.Fatalf("NewDirectorAdapter: %v", err)
	}

	if requests := server.Requests(); len(requests) != 0 {
		t.Fatalf("building the adapter sent %+v, want nothing", requests)
	}
}

// A director whose GET /info names UAA gets a bearer token that the
// adapter took from that UAA with bosh.username and bosh.password as the
// client credentials. The director and UAA sit behind one private CA, the
// only one the adapter trusts. A token is reused only while it has more
// than 30 seconds left. The first token lives 29 seconds, so it serves the
// call that fetched it and is replaced for the second call. The second
// lives an hour, so the third call reuses it. Neither outcome depends on
// how long the calls take.
func TestDirectorUsesUAAWhenInfoAdvertisesIt(t *testing.T) {
	t.Parallel()

	authority := newPrivateCA(t)
	uaa := newGrantingUAA(t, authority, 29, 3600)
	directorGot := &headerLog{}

	director := authority.serve(t, 3, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case infoRoute:
			directorInfo(writer, uaaAuthentication(uaa.server.URL))
		case deploymentsRoute:
			directorGot.add(request)
			boshtest.WriteJSON(writer, http.StatusOK, []map[string]interface{}{})
		default:
			http.NotFound(writer, request)
		}
	}))

	adapter, err := bosh.NewDirectorAdapter(bosh.Config{
		Address:  director.URL,
		Username: "uaa-client",
		Password: "uaa-secret",
		CACert:   authority.PEM,
	})
	if err != nil {
		t.Fatalf("NewDirectorAdapter: %v", err)
	}

	for call := range 3 {
		_, err = adapter.GetDeployments(t.Context())
		if err != nil {
			t.Fatalf("call %d: GetDeployments: %v", call+1, err)
		}
	}

	if got := uaa.grants.Load(); got != 2 {
		t.Fatalf("UAA granted %d tokens, want 2: one for the first call, whose token was already within 30 seconds of expiring, and one for the other two calls", got)
	}

	wantClient := "Basic " + base64.StdEncoding.EncodeToString([]byte("uaa-client:uaa-secret"))

	for grant := int32(1); grant <= 2; grant++ {
		if got, _ := uaa.clientFor.Load(grant); got != wantClient {
			t.Errorf("token request %d authenticated as %v, want the configured client ID and secret", grant, got)
		}
	}

	want := []string{"Bearer uaa-token-1", "Bearer uaa-token-2", "Bearer uaa-token-2"}
	if got := directorGot.all(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("the director got %q, want %q", got, want)
	}
}

// A director whose GET /info names HTTP Basic gets bosh.username and
// bosh.password as HTTP Basic credentials, and no UAA is asked.
func TestDirectorUsesBasicWhenInfoAdvertisesBasic(t *testing.T) {
	t.Parallel()

	var got atomic.Pointer[string]

	server := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
		if request.URL.Path != deploymentsRoute {
			return false
		}

		header := request.Header.Get("Authorization")
		got.Store(&header)

		boshtest.WriteJSON(writer, http.StatusOK, []map[string]interface{}{})

		return true
	})

	director := newReadAdapter(t, server, nil)

	_, err := director.GetDeployments(t.Context())
	if err != nil {
		t.Fatalf("GetDeployments: %v", err)
	}

	want := "Basic " + base64.StdEncoding.EncodeToString([]byte(basicAuthCredential+":"+basicAuthCredential))
	if header := got.Load(); header == nil || *header != want {
		t.Fatalf("the director got Authorization %v, want HTTP Basic with bosh.username and bosh.password", header)
	}

	for _, request := range server.Requests() {
		if request.Path == grantRoute {
			t.Errorf("a Basic director should not be asked for a token, got %+v", request)
		}
	}
}

// When UAA refuses the client, the call fails before anything reaches the
// director, and the error names the token URL and what to check.
func TestAuthDiscoveryFailureIsExplained(t *testing.T) {
	t.Parallel()

	uaa := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
		if request.URL.Path != grantRoute {
			return false
		}

		boshtest.WriteJSON(writer, http.StatusUnauthorized, uaaError{Error: "invalid_client", Description: "Bad credentials"})

		return true
	})

	server := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
		if request.URL.Path != infoRoute {
			return false
		}

		directorInfo(writer, uaaAuthentication(uaa.URL))

		return true
	})

	director := newReadAdapter(t, server, nil)

	_, err := director.GetDeployments(t.Context())

	var authErr *boshapi.AuthError
	if !errors.As(err, &authErr) || authErr.Stage != boshapi.AuthStageToken {
		t.Fatalf("expected a failed UAA token request, got %v", err)
	}

	for _, want := range []string{
		server.URL,
		uaa.URL + "/oauth/token",
		"HTTP 401",
		"invalid_client",
		"UAA did not accept the client credentials",
		"Check that ClientID names a UAA client",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("expected the error to say %q, got: %s", want, err.Error())
		}
	}

	if requests := server.APIRequests(); len(requests) != 0 {
		t.Errorf("an unauthenticated call reached the director: %+v", requests)
	}
}

// Each bosh block setting the adapter cannot work without is refused when
// the adapter is built, with an error that names the setting, so the broker
// never boots against a director it cannot reach or cannot verify.
func TestNewDirectorAdapterNamesTheBadSetting(t *testing.T) {
	t.Parallel()

	server := boshtest.New(t, nil)

	// The subtests run after this function returns, and cleanups run after
	// the subtests.
	t.Cleanup(func() {
		if requests := server.Requests(); len(requests) != 0 {
			t.Errorf("a refused configuration reached the director: %+v", requests)
		}
	})

	tests := []struct {
		name    string
		mutate  func(*bosh.Config)
		want    error
		setting string
	}{
		{name: "an empty address", mutate: func(config *bosh.Config) { config.Address = "" },
			want: bosh.ErrDirectorAddressMissing, setting: addressSetting},
		{name: "a blank address", mutate: func(config *bosh.Config) { config.Address = "  \n" },
			want: bosh.ErrDirectorAddressMissing, setting: addressSetting},
		{name: "an empty CA", mutate: func(config *bosh.Config) { config.CACert = "" },
			want: bosh.ErrDirectorCACertMissing, setting: caSetting},
		{name: "a blank CA", mutate: func(config *bosh.Config) { config.CACert = " \n\t" },
			want: bosh.ErrDirectorCACertMissing, setting: caSetting},
		{name: "a CA with no PEM block", mutate: func(config *bosh.Config) { config.CACert = "/var/vcap/jobs/blacksmith/config/bosh.ca" },
			want: bosh.ErrDirectorCACertInvalid, setting: caSetting},
		{name: "a PEM block that is not a certificate", mutate: func(config *bosh.Config) {
			config.CACert = "-----BEGIN CERTIFICATE-----\nbm90IGEgY2VydGlmaWNhdGU=\n-----END CERTIFICATE-----\n"
		}, want: bosh.ErrDirectorCACertInvalid, setting: caSetting},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			config := bosh.Config{Address: server.URL, Username: basicAuthCredential, Password: basicAuthCredential, CACert: server.CACert}
			testCase.mutate(&config)

			director, err := bosh.NewDirectorAdapter(config)
			if !errors.Is(err, testCase.want) {
				t.Fatalf("expected %v, got director %v and error %v", testCase.want, director, err)
			}

			if !strings.Contains(err.Error(), testCase.setting) {
				t.Errorf("expected the error to name %s, got: %s", testCase.setting, err.Error())
			}
		})
	}
}

// When several settings are wrong, the adapter reports the first of them in
// a fixed order: the credentials, then an empty address, then an http://
// address that is not allowed, then an empty CA, and then a CA that does
// not parse. Each row breaks the setting it expects
// to hear about and every setting checked after it, so a reordered check
// reports a different error.
func TestNewDirectorAdapterChecksTheSettingsInOrder(t *testing.T) {
	t.Parallel()

	const (
		address      = loopbackDirector
		plainAddress = "http://127.0.0.1:1"
		unparsedPEM  = "this is not a certificate"
	)

	tests := []struct {
		name   string
		config bosh.Config
		want   error
	}{
		{name: "the credentials before an empty address", config: bosh.Config{},
			want: bosh.ErrDirectorCredentialsMissing},
		{name: "the credentials before a CA that does not parse",
			config: bosh.Config{Address: address, CACert: unparsedPEM},
			want:   bosh.ErrDirectorCredentialsMissing},
		{name: "the credentials before an http address",
			config: bosh.Config{Address: plainAddress, CACert: unparsedPEM},
			want:   bosh.ErrDirectorCredentialsMissing},
		{name: "an empty address before an empty CA",
			config: bosh.Config{Username: basicAuthCredential, Password: basicAuthCredential},
			want:   bosh.ErrDirectorAddressMissing},
		{name: "an empty address before a CA that does not parse",
			config: bosh.Config{Username: basicAuthCredential, Password: basicAuthCredential, CACert: unparsedPEM},
			want:   bosh.ErrDirectorAddressMissing},
		{name: "an http address before an empty CA",
			config: bosh.Config{Address: plainAddress, Username: basicAuthCredential, Password: basicAuthCredential},
			want:   bosh.ErrDirectorPlainHTTP},
		{name: "an http address before a CA that does not parse",
			config: bosh.Config{Address: plainAddress, Username: basicAuthCredential, Password: basicAuthCredential, CACert: unparsedPEM},
			want:   bosh.ErrDirectorPlainHTTP},
		{name: "an empty CA before a CA that does not parse",
			config: bosh.Config{Address: address, Username: basicAuthCredential, Password: basicAuthCredential},
			want:   bosh.ErrDirectorCACertMissing},
		{name: "a CA that does not parse",
			config: bosh.Config{Address: address, Username: basicAuthCredential, Password: basicAuthCredential, CACert: unparsedPEM},
			want:   bosh.ErrDirectorCACertInvalid},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			director, err := bosh.NewDirectorAdapter(testCase.config)
			if !errors.Is(err, testCase.want) {
				t.Fatalf("expected %v, got director %v and error %v", testCase.want, director, err)
			}
		})
	}
}

// An http:// bosh.address is refused unless bosh.allow_http is set, because
// the director's credentials would cross the network unencrypted. The error
// names the address and the override, and the CA is still required.
func TestPlainHTTPAddressIsRefusedUnlessAllowed(t *testing.T) {
	t.Parallel()

	authority := newPrivateCA(t)

	for _, address := range []string{"http://127.0.0.1:1", "HTTP://127.0.0.1:1", " http://127.0.0.1:1\n"} {
		t.Run(strconv.Quote(address), func(t *testing.T) {
			t.Parallel()

			director, err := bosh.NewDirectorAdapter(bosh.Config{
				Address: address, Username: basicAuthCredential, Password: basicAuthCredential, CACert: authority.PEM,
			})
			if !errors.Is(err, bosh.ErrDirectorPlainHTTP) {
				t.Fatalf("expected %v, got director %v and error %v", bosh.ErrDirectorPlainHTTP, director, err)
			}

			for _, want := range []string{addressSetting, allowHTTPSetting} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("expected the error to name %s, got: %s", want, err.Error())
				}
			}
		})
	}

	_, err := bosh.NewDirectorAdapter(bosh.Config{
		Address: "http://127.0.0.1:1", Username: basicAuthCredential, Password: basicAuthCredential, AllowHTTP: true,
	})
	if !errors.Is(err, bosh.ErrDirectorCACertMissing) {
		t.Errorf("expected an allowed http address to still need a CA, got: %v", err)
	}
}

// With bosh.allow_http set, an adapter calls a director at an http://
// address, and the director gets bosh.username and bosh.password.
func TestAllowedPlainHTTPDirectorIsCalled(t *testing.T) {
	t.Parallel()

	var got atomic.Pointer[string]

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case infoRoute:
			directorInfo(writer, basicAuthentication())
		case deploymentsRoute:
			header := request.Header.Get("Authorization")
			got.Store(&header)
			boshtest.WriteJSON(writer, http.StatusOK, []map[string]interface{}{})
		default:
			http.NotFound(writer, request)
		}
	}))
	t.Cleanup(server.Close)

	if !strings.HasPrefix(server.URL, "http://") {
		t.Fatalf("the test director is at %s, want an http:// address", server.URL)
	}

	director, err := bosh.NewDirectorAdapter(bosh.Config{
		Address:   server.URL,
		Username:  basicAuthCredential,
		Password:  basicAuthCredential,
		CACert:    newPrivateCA(t).PEM,
		AllowHTTP: true,
	})
	if err != nil {
		t.Fatalf("NewDirectorAdapter: %v", err)
	}

	_, err = director.GetDeployments(t.Context())
	if err != nil {
		t.Fatalf("GetDeployments: %v", err)
	}

	want := "Basic " + base64.StdEncoding.EncodeToString([]byte(basicAuthCredential+":"+basicAuthCredential))
	if header := got.Load(); header == nil || *header != want {
		t.Fatalf("the director got Authorization %v, want HTTP Basic with bosh.username and bosh.password", header)
	}
}

// bosh.allow_http changes nothing for an address that does not use plain
// HTTP. An https:// address, one with no scheme, and one that does not
// parse as a URL give the same result with the setting on and off.
func TestAllowHTTPChangesNothingForOtherAddresses(t *testing.T) {
	t.Parallel()

	authority := newPrivateCA(t)

	for _, address := range []string{loopbackDirector, "127.0.0.1:25555", "10.0.0.6", "https://[::1"} {
		t.Run(strconv.Quote(address), func(t *testing.T) {
			t.Parallel()

			if bosh.UsesPlainHTTP(address) {
				t.Fatalf("UsesPlainHTTP(%q) is true, want false", address)
			}

			config := bosh.Config{Address: address, Username: basicAuthCredential, Password: basicAuthCredential, CACert: authority.PEM}

			_, refused := bosh.NewDirectorAdapter(config)

			config.AllowHTTP = true

			_, allowed := bosh.NewDirectorAdapter(config)

			if errors.Is(refused, bosh.ErrDirectorPlainHTTP) {
				t.Fatalf("an address that is not http:// was refused as plain HTTP: %v", refused)
			}

			if fmt.Sprint(refused) != fmt.Sprint(allowed) {
				t.Errorf("bosh.allow_http changed the result for %q from %v to %v", address, refused, allowed)
			}
		})
	}
}
