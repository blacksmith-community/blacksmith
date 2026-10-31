package cf_test

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	. "blacksmith/pkg/services/cf"
)

// Values that must never appear in a check's message.
const (
	probeUser     = "probe-admin-x9"
	probePassword = "pw-Zq8-never-echoed"
	probeToken    = "tok-7f3a-never-shown"
)

// rootDocument is the Cloud Foundry v3 root answer, advertising authURL as
// both the login and UAA endpoints.
func rootDocument(apiURL, authURL string) string {
	return fmt.Sprintf(`{"links":{"self":{"href":%q},"cloud_controller_v3":{"href":%q},"login":{"href":%q},"uaa":{"href":%q}}}`,
		apiURL, apiURL+"/v3", authURL, authURL)
}

// fakeUAA answers the password grant that registration sends, and refuses
// anything else.
type fakeUAA struct {
	*httptest.Server

	hits atomic.Int32
}

func newFakeUAA(t *testing.T) *fakeUAA {
	t.Helper()

	uaa := &fakeUAA{}
	uaa.Server = httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, req *http.Request) {
		uaa.hits.Add(1)

		clientID, clientSecret, basicOK := req.BasicAuth()

		parseErr := req.ParseForm()
		if parseErr != nil || req.Method != http.MethodPost || req.URL.Path != "/oauth/token" ||
			!basicOK || clientID != "cf" || clientSecret != "" ||
			req.PostForm.Get("grant_type") != "password" ||
			req.PostForm.Get("username") != probeUser || req.PostForm.Get("password") != probePassword {
			writer.WriteHeader(http.StatusUnauthorized)
			// A refusal that quotes the request, which the check must not repeat.
			_, _ = fmt.Fprintf(writer, `{"error":"unauthorized","error_description":"bad %s / %s"}`, probeUser, probePassword)

			return
		}

		writer.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(writer, `{"access_token":%q,"token_type":"bearer","expires_in":600}`, probeToken)
	}))
	t.Cleanup(uaa.Close)

	return uaa
}

// newFakeAPI serves the root document at / and counts its requests.
func newFakeAPI(t *testing.T, authURL string) (*httptest.Server, *atomic.Int32) {
	t.Helper()

	var hits atomic.Int32

	var api *httptest.Server

	api = httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, req *http.Request) {
		hits.Add(1)

		if req.URL.Path != "/" {
			http.NotFound(writer, req)

			return
		}

		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(rootDocument(api.URL, authURL)))
	}))
	t.Cleanup(api.Close)

	return api, &hits
}

// trust returns a pool holding the httptest certificate, which every
// httptest TLS server shares.
func trust(server *httptest.Server) *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AddCert(server.Certificate())

	return pool
}

func probe(apiURL string) *RegistrationTest {
	return &RegistrationTest{APIURL: apiURL, Username: probeUser, Password: probePassword}
}

// runChecks runs the tester and fails the test if any message repeats a
// credential or the token.
func runChecks(t *testing.T, tester *ConnectionTester, req *RegistrationTest) []ConnectionCheck {
	t.Helper()

	checks := tester.Run(context.Background(), req)

	encoded, err := json.Marshal(checks)
	if err != nil {
		t.Fatalf("checks do not encode: %v", err)
	}

	for _, secret := range []string{probeUser, probePassword, probeToken} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("a check repeats %q: %s", secret, encoded)
		}
	}

	return checks
}

// outcome sums the checks up as name=pass, name=fail, or name=skip.
func outcome(checks []ConnectionCheck) string {
	parts := make([]string, 0, len(checks))

	for _, check := range checks {
		state := "fail"

		switch {
		case check.Skipped:
			state = "skip"
		case check.Passed:
			state = "pass"
		}

		parts = append(parts, check.Name+"="+state)
	}

	return strings.Join(parts, " ")
}

func failed(checks []ConnectionCheck) ConnectionCheck {
	for _, check := range checks {
		if !check.Passed && !check.Skipped {
			return check
		}
	}

	return ConnectionCheck{}
}

func expectOutcome(t *testing.T, checks []ConnectionCheck, want string) {
	t.Helper()

	got := outcome(checks)
	if got != want {
		t.Fatalf("outcome = %q, want %q; checks: %+v", got, want, checks)
	}

	for _, check := range checks {
		if check.Message == "" {
			t.Fatalf("check %s has no message", check.Name)
		}
	}
}

const (
	allPass     = "reachable=pass tls=pass cloud_foundry=pass token=pass"
	tlsFails    = "reachable=pass tls=fail cloud_foundry=skip token=skip"
	cfFails     = "reachable=pass tls=pass cloud_foundry=fail token=skip"
	tokenFails  = "reachable=pass tls=pass cloud_foundry=pass token=fail"
	reachFails  = "reachable=fail tls=skip cloud_foundry=skip token=skip"
	testTimeout = 200 * time.Millisecond
)

func TestConnectionTestPassesEveryCheck(t *testing.T) {
	t.Parallel()

	uaa := newFakeUAA(t)
	api, _ := newFakeAPI(t, uaa.URL)

	checks := runChecks(t, &ConnectionTester{RootCAs: trust(api)}, probe(api.URL))
	expectOutcome(t, checks, allPass)

	if uaa.hits.Load() != 1 {
		t.Fatalf("token endpoint got %d requests, want 1", uaa.hits.Load())
	}
}

func TestConnectionTestTrimsTheTypedURL(t *testing.T) {
	t.Parallel()

	uaa := newFakeUAA(t)
	api, _ := newFakeAPI(t, uaa.URL)

	checks := runChecks(t, &ConnectionTester{RootCAs: trust(api)}, probe("  "+api.URL+"/  "))
	expectOutcome(t, checks, allPass)
}

func TestConnectionTestReportsAnAPIThatDoesNotAnswer(t *testing.T) {
	t.Parallel()

	closed := httptest.NewTLSServer(http.NotFoundHandler())
	url := closed.URL
	pool := trust(closed)
	closed.Close()

	checks := runChecks(t, &ConnectionTester{RootCAs: pool}, probe(url))
	expectOutcome(t, checks, reachFails)
}

func TestConnectionTestRejectsAnUntrustedCertificate(t *testing.T) {
	t.Parallel()

	uaa := newFakeUAA(t)
	api, hits := newFakeAPI(t, uaa.URL)

	// An empty pool trusts nothing, so the httptest certificate fails.
	checks := runChecks(t, &ConnectionTester{RootCAs: x509.NewCertPool()}, probe(api.URL))
	expectOutcome(t, checks, tlsFails)

	if !strings.Contains(failed(checks).Message, "certificate") {
		t.Fatalf("TLS message does not name the certificate: %q", failed(checks).Message)
	}

	if hits.Load() != 0 || uaa.hits.Load() != 0 {
		t.Fatalf("an unverified connection carried a request: api=%d uaa=%d", hits.Load(), uaa.hits.Load())
	}
}

func TestConnectionTestRejectsACertificateForAnotherHost(t *testing.T) {
	t.Parallel()

	uaa := newFakeUAA(t)
	api, _ := newFakeAPI(t, uaa.URL)

	// The httptest certificate names 127.0.0.1 and example.com, not localhost.
	local := strings.Replace(api.URL, "127.0.0.1", "localhost", 1)

	checks := runChecks(t, &ConnectionTester{RootCAs: trust(api)}, probe(local))
	expectOutcome(t, checks, tlsFails)
}

func TestConnectionTestFailsTLSForPlainHTTP(t *testing.T) {
	t.Parallel()

	var hits atomic.Int32

	plain := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = writer.Write([]byte(rootDocument("http://unused", "https://unused")))
	}))
	t.Cleanup(plain.Close)

	checks := runChecks(t, &ConnectionTester{}, probe(plain.URL))
	expectOutcome(t, checks, tlsFails)

	if !strings.Contains(failed(checks).Message, "HTTPS") {
		t.Fatalf("TLS message does not say HTTPS: %q", failed(checks).Message)
	}
}

func TestConnectionTestReportsAnAPIThatIsNotCloudFoundry(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		status int
		body   string
		want   string
	}{
		"not found":     {status: http.StatusNotFound, body: `{"error":"nope"}`, want: "404"},
		"html page":     {status: http.StatusOK, body: "<html><body>Welcome</body></html>", want: "Cloud Foundry"},
		"other json":    {status: http.StatusOK, body: `{"name":"something else"}`, want: "Cloud Foundry"},
		"no auth link":  {status: http.StatusOK, body: `{"links":{"cloud_controller_v3":{"href":"https://x/v3"}}}`, want: "authentication"},
		"server failed": {status: http.StatusBadGateway, body: "<h1>Bad Gateway</h1>", want: "502"},
	}

	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			api := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(testCase.status)
				_, _ = writer.Write([]byte(testCase.body))
			}))
			t.Cleanup(api.Close)

			checks := runChecks(t, &ConnectionTester{RootCAs: trust(api)}, probe(api.URL))
			expectOutcome(t, checks, cfFails)

			message := failed(checks).Message
			if !strings.Contains(message, testCase.want) {
				t.Fatalf("message %q does not contain %q", message, testCase.want)
			}

			if strings.Contains(message, "<") {
				t.Fatalf("message repeats the response body: %q", message)
			}
		})
	}
}

func TestConnectionTestLimitsTheBodyItReads(t *testing.T) {
	t.Parallel()

	uaa := newFakeUAA(t)

	var api *httptest.Server

	api = httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		doc := rootDocument(api.URL, uaa.URL)
		// Valid JSON, padded past the limit with whitespace.
		_, _ = writer.Write([]byte(doc + strings.Repeat(" ", 4096)))
	}))
	t.Cleanup(api.Close)

	checks := runChecks(t, &ConnectionTester{RootCAs: trust(api), BodyLimit: 1024}, probe(api.URL))
	expectOutcome(t, checks, cfFails)

	if uaa.hits.Load() != 0 {
		t.Fatal("the token check ran after an oversized answer")
	}
}

func TestConnectionTestDoesNotFollowARedirectToAnotherHost(t *testing.T) {
	t.Parallel()

	uaa := newFakeUAA(t)
	// elsewhere is a working Cloud Foundry root, so following the redirect
	// would pass every check.
	elsewhere, elsewhereHits := newFakeAPI(t, uaa.URL)

	api := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, req *http.Request) {
		http.Redirect(writer, req, elsewhere.URL+"/", http.StatusFound)
	}))
	t.Cleanup(api.Close)

	checks := runChecks(t, &ConnectionTester{RootCAs: trust(api)}, probe(api.URL))
	expectOutcome(t, checks, cfFails)

	if !strings.Contains(failed(checks).Message, "redirect") {
		t.Fatalf("message does not mention the redirect: %q", failed(checks).Message)
	}

	if elsewhereHits.Load() != 0 || uaa.hits.Load() != 0 {
		t.Fatalf("the test followed the redirect: elsewhere=%d uaa=%d", elsewhereHits.Load(), uaa.hits.Load())
	}
}

func TestConnectionTestFollowsARedirectOnTheSameHost(t *testing.T) {
	t.Parallel()

	uaa := newFakeUAA(t)

	var api *httptest.Server

	api = httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/" {
			http.Redirect(writer, req, "/root", http.StatusFound)

			return
		}

		_, _ = writer.Write([]byte(rootDocument(api.URL, uaa.URL)))
	}))
	t.Cleanup(api.Close)

	checks := runChecks(t, &ConnectionTester{RootCAs: trust(api)}, probe(api.URL))
	expectOutcome(t, checks, allPass)
}

func TestConnectionTestReportsRefusedCredentials(t *testing.T) {
	t.Parallel()

	uaa := newFakeUAA(t)
	api, _ := newFakeAPI(t, uaa.URL)

	req := probe(api.URL)
	req.Password = "wrong-password"

	checks := runChecks(t, &ConnectionTester{RootCAs: trust(api)}, req)
	expectOutcome(t, checks, tokenFails)

	message := failed(checks).Message
	if !strings.Contains(message, "401") {
		t.Fatalf("message does not name the status: %q", message)
	}

	if strings.Contains(message, "wrong-password") || strings.Contains(message, "unauthorized") {
		t.Fatalf("message repeats the request or the refusal body: %q", message)
	}
}

func TestConnectionTestDoesNotFollowARedirectFromTheTokenEndpoint(t *testing.T) {
	t.Parallel()

	elsewhere := newFakeUAA(t)

	redirecting := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, req *http.Request) {
		// 307 would resend the password in the body.
		http.Redirect(writer, req, elsewhere.URL+"/oauth/token", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirecting.Close)

	api, _ := newFakeAPI(t, redirecting.URL)

	checks := runChecks(t, &ConnectionTester{RootCAs: trust(api)}, probe(api.URL))
	expectOutcome(t, checks, tokenFails)

	if elsewhere.hits.Load() != 0 {
		t.Fatal("the credentials followed the token endpoint's redirect")
	}
}

func TestConnectionTestSendsNoCredentialsOverPlainHTTP(t *testing.T) {
	t.Parallel()

	var plainHits atomic.Int32

	plain := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		plainHits.Add(1)
	}))
	t.Cleanup(plain.Close)

	api, _ := newFakeAPI(t, plain.URL)

	checks := runChecks(t, &ConnectionTester{RootCAs: trust(api)}, probe(api.URL))
	expectOutcome(t, checks, tokenFails)

	if plainHits.Load() != 0 {
		t.Fatal("the credentials went to an http token endpoint")
	}
}

func TestConnectionTestNeedsAnAccessToken(t *testing.T) {
	t.Parallel()

	uaa := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"token_type":"bearer"}`))
	}))
	t.Cleanup(uaa.Close)

	api, _ := newFakeAPI(t, uaa.URL)

	checks := runChecks(t, &ConnectionTester{RootCAs: trust(api)}, probe(api.URL))
	expectOutcome(t, checks, tokenFails)
}

// hang holds a request until the client gives up. It reads the body first,
// because the server only notices a closed connection once the body is read.
func hang(_ http.ResponseWriter, req *http.Request) {
	_, _ = io.Copy(io.Discard, req.Body)
	<-req.Context().Done()
}

func TestConnectionTestStopsAtItsTimeout(t *testing.T) {
	t.Parallel()

	api := httptest.NewTLSServer(http.HandlerFunc(hang))
	t.Cleanup(api.Close)

	started := time.Now()
	checks := runChecks(t, &ConnectionTester{RootCAs: trust(api), Timeout: testTimeout}, probe(api.URL))

	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("the test took %s, past its %s limit", elapsed, testTimeout)
	}

	expectOutcome(t, checks, reachFails)

	if !strings.Contains(failed(checks).Message, testTimeout.String()) {
		t.Fatalf("message does not name the limit: %q", failed(checks).Message)
	}
}

func TestConnectionTestTimeoutCoversTheTokenRequest(t *testing.T) {
	t.Parallel()

	uaa := httptest.NewTLSServer(http.HandlerFunc(hang))
	t.Cleanup(uaa.Close)

	api, _ := newFakeAPI(t, uaa.URL)

	started := time.Now()
	checks := runChecks(t, &ConnectionTester{RootCAs: trust(api), Timeout: testTimeout}, probe(api.URL))

	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("the test took %s, past its %s limit", elapsed, testTimeout)
	}

	expectOutcome(t, checks, tokenFails)
}

func TestConnectionTestTimeoutDefaultsToTenSeconds(t *testing.T) {
	t.Parallel()

	if ConnectionTestTimeout != 10*time.Second {
		t.Fatalf("ConnectionTestTimeout = %s, want 10s", ConnectionTestTimeout)
	}
}

func TestValidateRegistrationTest(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		req  RegistrationTest
		want error
	}{
		"valid":            {req: RegistrationTest{APIURL: "https://api.example.com", Username: "u", Password: "p"}},
		"plain http":       {req: RegistrationTest{APIURL: "http://api.example.com", Username: "u", Password: "p"}},
		"no url":           {req: RegistrationTest{Username: "u", Password: "p"}, want: ErrCFAPIURLRequired},
		"no username":      {req: RegistrationTest{APIURL: "https://api.example.com", Password: "p"}, want: ErrUsernameRequired},
		"no password":      {req: RegistrationTest{APIURL: "https://api.example.com", Username: "u"}, want: ErrPasswordRequired},
		"no scheme":        {req: RegistrationTest{APIURL: "api.example.com", Username: "u", Password: "p"}, want: ErrAPIURLScheme},
		"other scheme":     {req: RegistrationTest{APIURL: "ftp://api.example.com", Username: "u", Password: "p"}, want: ErrAPIURLScheme},
		"no host":          {req: RegistrationTest{APIURL: "https://", Username: "u", Password: "p"}, want: ErrAPIURLMustHaveValidHostname},
		"credentials":      {req: RegistrationTest{APIURL: "https://u:p@api.example.com", Username: "u", Password: "p"}, want: ErrAPIURLHasCredentials},
		"malformed":        {req: RegistrationTest{APIURL: "https://api example.com/%zz", Username: "u", Password: "p"}, want: ErrAPIURLMalformed},
		"padded but valid": {req: RegistrationTest{APIURL: "  https://api.example.com/  ", Username: "u", Password: "p"}},
	}

	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := ValidateRegistrationTest(&testCase.req)
			if !errors.Is(err, testCase.want) || (testCase.want == nil) != (err == nil) {
				t.Fatalf("ValidateRegistrationTest() = %v, want %v", err, testCase.want)
			}
		})
	}
}
