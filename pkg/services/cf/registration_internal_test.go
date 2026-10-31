package cf

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// plainReceiver is a plain-http host the credentials must never reach. It
// serves a working root document that names itself, so following anything
// to it would get as far as a token request.
type plainReceiver struct {
	*httptest.Server

	hits atomic.Int32
}

func newPlainReceiver(t *testing.T) *plainReceiver {
	t.Helper()

	receiver := &plainReceiver{}
	receiver.Server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, req *http.Request) {
		receiver.hits.Add(1)
		_, _ = io.Copy(io.Discard, req.Body)

		if req.URL.Path == "/" {
			_, _ = fmt.Fprintf(writer, `{"links":{"cloud_controller_v3":{"href":%q},"uaa":{"href":%q}}}`,
				receiver.URL+"/v3", receiver.URL)

			return
		}

		_, _ = fmt.Fprint(writer, `{"access_token":"stolen","token_type":"bearer","expires_in":600}`)
	}))
	t.Cleanup(receiver.Close)

	return receiver
}

// expectNoCredentials fails the test if err repeats a credential.
func expectNoCredentials(t *testing.T, err error) {
	t.Helper()

	if err == nil {
		return
	}

	for _, secret := range []string{foundationUser, foundationPassword, foundationToken} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("the error repeats %q: %v", secret, err)
		}
	}
}

func TestRegistrationDoesNotFollowARedirectFromTheTokenEndpoint(t *testing.T) {
	t.Parallel()

	receiver := newPlainReceiver(t)
	foundation := newV3OnlyFoundation(t)
	foundation.tokenAnswer = func(writer http.ResponseWriter, req *http.Request) {
		// 307 would resend the password in the body.
		http.Redirect(writer, req, receiver.URL+"/oauth/token", http.StatusTemporaryRedirect)
	}

	err := foundation.client().Authenticate()
	if !errors.Is(err, ErrTokenEndpointRedirected) {
		t.Fatalf("Authenticate() after a token redirect = %v, want ErrTokenEndpointRedirected", err)
	}

	expectNoCredentials(t, err)

	if receiver.hits.Load() != 0 {
		t.Fatalf("the redirect target got %d requests, want 0", receiver.hits.Load())
	}
}

func TestRegistrationSendsNoCredentialsToAnHTTPTokenEndpoint(t *testing.T) {
	t.Parallel()

	receiver := newPlainReceiver(t)
	foundation := newV3OnlyFoundation(t)
	foundation.root = fmt.Sprintf(`{"links":{"cloud_controller_v3":{"href":%q},"uaa":{"href":%q}}}`,
		foundation.URL+"/v3", receiver.URL)

	err := foundation.client().Authenticate()
	if !errors.Is(err, ErrTokenEndpointNotHTTPS) {
		t.Fatalf("Authenticate() with an http uaa link = %v, want ErrTokenEndpointNotHTTPS", err)
	}

	if receiver.hits.Load() != 0 {
		t.Fatalf("the http token endpoint got %d requests, want 0", receiver.hits.Load())
	}
}

func TestRegistrationDoesNotFollowARootRedirectToAnotherHost(t *testing.T) {
	t.Parallel()

	receiver := newPlainReceiver(t)
	foundation := newV3OnlyFoundation(t)
	foundation.rootRedirect = receiver.URL + "/"

	err := foundation.client().Authenticate()
	if !errors.Is(err, ErrAPIRedirectedOffHost) {
		t.Fatalf("Authenticate() after a root redirect to another host = %v, want ErrAPIRedirectedOffHost", err)
	}

	if strings.Contains(err.Error(), receiver.URL) || strings.Contains(err.Error(), foundation.URL) {
		t.Fatalf("the error names the request: %v", err)
	}

	if receiver.hits.Load() != 0 || foundation.uaaHits.Load() != 0 {
		t.Fatalf("registration followed the redirect: receiver=%d uaa=%d", receiver.hits.Load(), foundation.uaaHits.Load())
	}
}

func TestRegistrationRefusesAnHTTPAPIURL(t *testing.T) {
	t.Parallel()

	receiver := newPlainReceiver(t)
	req := &RegistrationRequest{Name: "lab", APIURL: receiver.URL, Username: foundationUser, Password: foundationPassword}

	err := NewHandler("https://broker.example.com", "broker", "secret", t.Logf).validateRegistrationRequest(req)
	if !errors.Is(err, ErrAPIURLNotHTTPS) {
		t.Fatalf("validateRegistrationRequest() with an http API URL = %v, want ErrAPIURLNotHTTPS", err)
	}

	progress := make(chan RegistrationProgress, 32)

	err = NewHandler("https://broker.example.com", "broker", "secret", t.Logf).PerformRegistration(req, progress)
	if !errors.Is(err, ErrAPIURLNotHTTPS) {
		t.Fatalf("PerformRegistration() with an http API URL = %v, want ErrAPIURLNotHTTPS", err)
	}

	client := NewCFAuthClient(receiver.URL, foundationUser, foundationPassword)

	err = client.Authenticate()
	if !errors.Is(err, ErrAPIURLNotHTTPS) {
		t.Fatalf("Authenticate() with an http API URL = %v, want ErrAPIURLNotHTTPS", err)
	}

	if receiver.hits.Load() != 0 {
		t.Fatalf("the http API got %d requests, want 0", receiver.hits.Load())
	}
}

func TestValidateRegistrationRequestChecksTheAPIURL(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		apiURL string
		want   error
	}{
		"https":       {apiURL: "https://api.example.com"},
		"padded":      {apiURL: " https://api.example.com/ "},
		"plain http":  {apiURL: "http://api.example.com", want: ErrAPIURLNotHTTPS},
		"no scheme":   {apiURL: "api.example.com", want: ErrAPIURLScheme},
		"no host":     {apiURL: "https://", want: ErrAPIURLMustHaveValidHostname},
		"credentials": {apiURL: "https://u:p@api.example.com", want: ErrAPIURLHasCredentials},
	}

	handler := NewHandler("https://broker.example.com", "broker", "secret", t.Logf)

	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			req := &RegistrationRequest{Name: "lab", APIURL: testCase.apiURL, Username: "u", Password: "p"}

			err := handler.validateRegistrationRequest(req)
			if !errors.Is(err, testCase.want) || (testCase.want == nil) != (err == nil) {
				t.Fatalf("validateRegistrationRequest(%q) = %v, want %v", testCase.apiURL, err, testCase.want)
			}
		})
	}
}

func TestRegistrationLimitsTheRootItReads(t *testing.T) {
	t.Parallel()

	foundation := newV3OnlyFoundation(t)
	foundation.root += strings.Repeat(" ", ConnectionTestBodyLimit)

	err := foundation.client().Authenticate()
	if !errors.Is(err, errResponseTooLarge) {
		t.Fatalf("Authenticate() with an oversized root = %v, want errResponseTooLarge", err)
	}

	if foundation.uaaHits.Load() != 0 {
		t.Fatalf("registration sent the credentials %d times after an oversized root, want 0", foundation.uaaHits.Load())
	}
}

func TestRegistrationLimitsTheTokenAnswerItReads(t *testing.T) {
	t.Parallel()

	foundation := newV3OnlyFoundation(t)
	foundation.tokenAnswer = func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(writer, `{"access_token":%q,"token_type":"bearer","expires_in":600}%s`,
			foundationToken, strings.Repeat(" ", ConnectionTestBodyLimit))
	}

	err := foundation.client().Authenticate()
	if !errors.Is(err, errResponseTooLarge) {
		t.Fatalf("Authenticate() with an oversized token answer = %v, want errResponseTooLarge", err)
	}
}

func TestRegistrationNeverSendsAnEmptyPassword(t *testing.T) {
	t.Parallel()

	foundation := newV3OnlyFoundation(t)
	client := NewCFAuthClient(foundation.URL, foundationUser, "")
	client.httpClient = foundation.Client()

	err := client.Authenticate()
	if !errors.Is(err, ErrPasswordRequired) {
		t.Fatalf("Authenticate() without a password = %v, want ErrPasswordRequired", err)
	}

	if hits := foundation.uaaHits.Load() + foundation.loginHits.Load(); hits != 0 {
		t.Fatalf("registration sent %d password grants without a password, want 0", hits)
	}
}

func TestRegistrationErrorsDoNotNameTheRequest(t *testing.T) {
	t.Parallel()

	closed := httptest.NewTLSServer(http.NotFoundHandler())
	closed.Close()

	client := NewCFAuthClient(closed.URL, foundationUser, foundationPassword)

	err := client.Authenticate()
	if !errors.Is(err, ErrCFAPIUnreachable) {
		t.Fatalf("Authenticate() against a closed port = %v, want ErrCFAPIUnreachable", err)
	}

	host := strings.TrimPrefix(closed.URL, "https://")
	if strings.Contains(err.Error(), host) {
		t.Fatalf("the error names the request: %v", err)
	}
}

func TestRegistrationKeepsOnlyTheStatusAndTheOAuthErrorCode(t *testing.T) {
	t.Parallel()

	echo := fmt.Sprintf("Bad credentials for %s / %s", foundationUser, foundationPassword)

	cases := map[string]struct {
		body string
		want string
		not  string
	}{
		"a standard code": {
			body: fmt.Sprintf(`{"error":"invalid_grant","error_description":%q}`, echo),
			want: "HTTP 401 (invalid_grant)",
			not:  "Bad credentials",
		},
		"a code outside RFC 6749": {
			body: fmt.Sprintf(`{"error":%q,"error_description":"no"}`, foundationPassword),
			want: "HTTP 401",
			not:  "()",
		},
		"not JSON": {
			body: "<html>" + echo + "</html>",
			want: "HTTP 401",
			not:  "html",
		},
	}

	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			foundation := newV3OnlyFoundation(t)
			foundation.tokenAnswer = func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(http.StatusUnauthorized)
				_, _ = writer.Write([]byte(testCase.body))
			}

			err := foundation.client().Authenticate()
			if !errors.Is(err, ErrAuthenticationFailed) {
				t.Fatalf("Authenticate() after a refusal = %v, want ErrAuthenticationFailed", err)
			}

			expectNoCredentials(t, err)

			if !strings.Contains(err.Error(), testCase.want) || strings.Contains(err.Error(), testCase.not) {
				t.Fatalf("Authenticate() error = %q, want it to contain %q and not %q", err, testCase.want, testCase.not)
			}
		})
	}
}

// registrationHandler is an operations handler whose clients trust the
// foundation's test certificate.
func registrationHandler(t *testing.T, foundation *v3OnlyFoundation) *Handler {
	t.Helper()

	handler := NewHandler("https://broker.example.com", "broker", "secret", t.Logf)
	handler.httpClient = foundation.Client()

	return handler
}

func collectProgress(progress <-chan RegistrationProgress) map[string]RegistrationProgress {
	last := map[string]RegistrationProgress{}
	for step := range progress {
		last[step.Step] = step
	}

	return last
}

func TestPerformRegistrationSendsOnePasswordGrant(t *testing.T) {
	t.Parallel()

	foundation := newV3OnlyFoundation(t)
	progress := make(chan RegistrationProgress, 64)
	req := &RegistrationRequest{Name: "lab", APIURL: foundation.URL, Username: foundationUser, Password: foundationPassword}

	// The fake has no /v3/service_brokers, so registration stops at the
	// broker check, after both sign-in steps.
	_ = registrationHandler(t, foundation).PerformRegistration(req, progress)

	steps := collectProgress(progress)
	for _, step := range []string{StepConnecting, StepAuthenticating} {
		if steps[step].Status != ProgressStatusSuccess {
			t.Fatalf("step %s = %+v, want success", step, steps[step])
		}
	}

	if hits := foundation.uaaHits.Load(); hits != 1 {
		t.Fatalf("registration sent %d password grants, want 1", hits)
	}
}

func TestPerformRegistrationReportsAConnectionFailure(t *testing.T) {
	t.Parallel()

	foundation := newV3OnlyFoundation(t)
	foundation.root = `{"links":{}}`
	progress := make(chan RegistrationProgress, 64)
	req := &RegistrationRequest{Name: "lab", APIURL: foundation.URL, Username: foundationUser, Password: foundationPassword}

	err := registrationHandler(t, foundation).PerformRegistration(req, progress)
	if !errors.Is(err, errNotCloudFoundry) {
		t.Fatalf("PerformRegistration() against an API that is not Cloud Foundry = %v, want errNotCloudFoundry", err)
	}

	steps := collectProgress(progress)
	if steps[StepConnecting].Status != ProgressStatusError || !strings.Contains(steps[StepConnecting].Message, errNotCloudFoundry.Error()) {
		t.Fatalf("connecting step = %+v, want an error naming the root document", steps[StepConnecting])
	}

	if _, ran := steps[StepAuthenticating]; ran {
		t.Fatalf("the authenticating step ran after the connection failed: %+v", steps[StepAuthenticating])
	}

	if foundation.uaaHits.Load() != 0 {
		t.Fatalf("registration sent the credentials %d times, want 0", foundation.uaaHits.Load())
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestBaseTransportFallsBackWhenTheDefaultIsReplaced(t *testing.T) {
	t.Parallel()

	replaced := roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("unused") })

	transport := baseTransport(replaced)
	if transport == nil || transport.Proxy == nil {
		t.Fatalf("baseTransport() with a replaced default = %+v, want a transport that honors the proxy settings", transport)
	}

	if cloned := baseTransport(http.DefaultTransport); cloned == nil || cloned == http.DefaultTransport {
		t.Fatal("baseTransport() with the stock default must return a clone")
	}
}
