package cf

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// Connection test limits.
const (
	// ConnectionTestTimeout is the longest a whole connection test may take.
	ConnectionTestTimeout = 10 * time.Second

	// ConnectionTestBodyLimit is the most of any one response the test reads.
	ConnectionTestBodyLimit = 64 << 10

	// connectionTestRedirects is how many same-host redirects the API root may take.
	connectionTestRedirects = 3
)

// The checks a connection test runs, in order.
const (
	CheckReachable    = "reachable"
	CheckTLS          = "tls"
	CheckCloudFoundry = "cloud_foundry"
	CheckToken        = "token"
)

// Static errors for err113 compliance.
var (
	ErrAPIURLMalformed      = errors.New("API URL is not a valid URL")
	ErrAPIURLScheme         = errors.New("API URL must start with https:// or http://")
	ErrAPIURLHasCredentials = errors.New("API URL must not contain a username or password")

	errRedirectedOffHost = errors.New("redirected to a different host")
	errTooManyRedirects  = errors.New("too many redirects")
	errResponseTooLarge  = errors.New("response too large")
	errNoAuthEndpoint    = errors.New("no authentication endpoint")
)

// ConnectionCheck is the result of one step of a connection test. The
// message names the step and any HTTP status, and never the request.
type ConnectionCheck struct {
	Name    string `json:"name"`
	Passed  bool   `json:"passed"`
	Skipped bool   `json:"skipped,omitempty"`
	Message string `json:"message"`
}

// ConnectionTester checks that a Cloud Foundry API answers, that its
// certificate verifies, that it is Cloud Foundry, and that a username and
// password get a token from its authentication endpoint.
type ConnectionTester struct {
	// RootCAs is the pool certificates verify against. Nil means the system
	// roots, which registration uses.
	RootCAs *x509.CertPool
	// Timeout bounds the whole test. Zero means ConnectionTestTimeout.
	Timeout time.Duration
	// BodyLimit bounds each response read. Zero means ConnectionTestBodyLimit.
	BodyLimit int64
}

// ValidateRegistrationTest applies registration's required fields to a
// connection test, then checks that the API URL is one the test can reach.
// Plain http passes here and fails the TLS check, so the operator sees why.
func ValidateRegistrationTest(req *RegistrationTest) error {
	if req.APIURL == "" {
		return ErrCFAPIURLRequired
	}

	if req.Username == "" {
		return ErrUsernameRequired
	}

	if req.Password == "" {
		return ErrPasswordRequired
	}

	_, err := parseAPIURL(req.APIURL)

	return err
}

func parseAPIURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSuffix(strings.TrimSpace(raw), "/"))
	if err != nil {
		return nil, ErrAPIURLMalformed
	}

	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return nil, ErrAPIURLScheme
	}

	if parsed.Hostname() == "" {
		return nil, ErrAPIURLMustHaveValidHostname
	}

	if parsed.User != nil {
		return nil, ErrAPIURLHasCredentials
	}

	// The test asks for the root, so a query or fragment has no place.
	parsed.RawQuery = ""
	parsed.Fragment = ""

	return parsed, nil
}

// Run tests the connection and returns every check in order. It stops at the
// first failure and marks the checks after it skipped. The token is read only
// to see that one was issued, and is never kept.
func (t *ConnectionTester) Run(ctx context.Context, req *RegistrationTest) []ConnectionCheck {
	timeout := t.Timeout
	if timeout <= 0 {
		timeout = ConnectionTestTimeout
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	run := &connectionRun{tester: t, timeout: timeout}

	apiURL, err := parseAPIURL(req.APIURL)
	if err != nil {
		run.fail(CheckReachable, "The API URL is not valid: "+err.Error()+".")

		return run.finish()
	}

	transport := t.transport()
	defer transport.CloseIdleConnections()

	root, ok := run.reach(ctx, transport, apiURL)
	if !ok {
		return run.finish()
	}

	authURL, ok := run.identify(root)
	if !ok {
		return run.finish()
	}

	run.authenticate(ctx, transport, authURL, req.Username, req.Password)

	return run.finish()
}

func (t *ConnectionTester) bodyLimit() int64 {
	if t.BodyLimit <= 0 {
		return ConnectionTestBodyLimit
	}

	return t.BodyLimit
}

// transport is the default transport, as registration's client uses, with
// the tester's roots.
func (t *ConnectionTester) transport() *http.Transport {
	base, _ := http.DefaultTransport.(*http.Transport)
	transport := base.Clone()
	transport.TLSClientConfig = &tls.Config{
		RootCAs:    t.RootCAs,
		MinVersion: tls.VersionTLS12,
	}
	transport.DisableKeepAlives = true

	return transport
}

type connectionRun struct {
	tester  *ConnectionTester
	timeout time.Duration
	checks  []ConnectionCheck
	failed  bool
}

func (r *connectionRun) pass(name, message string) {
	r.checks = append(r.checks, ConnectionCheck{Name: name, Passed: true, Message: message})
}

func (r *connectionRun) fail(name, message string) {
	r.checks = append(r.checks, ConnectionCheck{Name: name, Message: message})
	r.failed = true
}

// finish adds every check that did not run, marked skipped.
func (r *connectionRun) finish() []ConnectionCheck {
	ran := make(map[string]bool, len(r.checks))
	for _, check := range r.checks {
		ran[check.Name] = true
	}

	for _, name := range []string{CheckReachable, CheckTLS, CheckCloudFoundry, CheckToken} {
		if !ran[name] {
			r.checks = append(r.checks, ConnectionCheck{
				Name:    name,
				Skipped: true,
				Message: "Not run, because an earlier check failed.",
			})
		}
	}

	return r.checks
}

// reach asks the API root for its document, and settles the reachable and
// TLS checks on the way. A certificate that does not verify still means the
// API answered, so it fails TLS and not reachable.
func (r *connectionRun) reach(ctx context.Context, transport *http.Transport, apiURL *url.URL) (*http.Response, bool) {
	client := &http.Client{Transport: transport, CheckRedirect: sameOrigin(apiURL)}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL.String()+"/", nil)
	if err != nil {
		r.fail(CheckReachable, "The API URL is not valid.")

		return nil, false
	}

	req.Header.Set("Accept", "application/json")

	// The operator typed this host, and registration connects to it too.
	resp, err := client.Do(req)

	switch {
	case err == nil:
	case errors.Is(err, errRedirectedOffHost), errors.Is(err, errTooManyRedirects):
		r.pass(CheckReachable, "The API answered.")
		r.passTLS(apiURL)

		if !r.failed {
			r.fail(CheckCloudFoundry, "The API answered with a redirect to a different host, which the test does not follow.")
		}

		return nil, false
	case isCertificateError(err):
		r.pass(CheckReachable, "The API answered.")
		r.fail(CheckTLS, "The API's certificate did not verify: "+certificateReason(err)+".")

		return nil, false
	case isHandshakeError(err):
		r.pass(CheckReachable, "The API answered.")
		r.fail(CheckTLS, "The API did not complete a TLS handshake.")

		return nil, false
	default:
		r.fail(CheckReachable, "The API did not answer: "+r.networkReason(ctx, err)+".")

		return nil, false
	}

	r.pass(CheckReachable, fmt.Sprintf("The API answered with HTTP %d.", resp.StatusCode))
	r.passTLS(apiURL)

	if r.failed {
		_ = resp.Body.Close()

		return nil, false
	}

	return resp, true
}

func (r *connectionRun) passTLS(apiURL *url.URL) {
	if apiURL.Scheme != "https" {
		r.fail(CheckTLS, "The API URL uses http, so the connection is not encrypted. Use an HTTPS URL.")

		return
	}

	r.pass(CheckTLS, "The API's certificate verified.")
}

// identify reads the root document and returns the authentication endpoint
// it advertises.
func (r *connectionRun) identify(resp *http.Response) (*url.URL, bool) {
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		r.fail(CheckCloudFoundry, fmt.Sprintf("The API root answered HTTP %d, so this is not a Cloud Foundry API.", resp.StatusCode))

		return nil, false
	}

	body, err := readLimited(resp.Body, r.tester.bodyLimit())
	if errors.Is(err, errResponseTooLarge) {
		r.fail(CheckCloudFoundry, fmt.Sprintf("The API root's answer is larger than %d bytes, so it is not a Cloud Foundry root document.", r.tester.bodyLimit()))

		return nil, false
	}

	authURL, err := authEndpointFromRoot(body)

	switch {
	case errors.Is(err, errNoAuthEndpoint):
		r.fail(CheckCloudFoundry, "The API is Cloud Foundry, but it names no authentication endpoint.")

		return nil, false
	case errors.Is(err, ErrAPIURLMalformed):
		r.fail(CheckCloudFoundry, "The API is Cloud Foundry, but its authentication endpoint is not a valid URL.")

		return nil, false
	case err != nil:
		r.fail(CheckCloudFoundry, "The API root's answer is not a Cloud Foundry root document.")

		return nil, false
	}

	r.pass(CheckCloudFoundry, "The API identifies itself as Cloud Foundry.")

	return authURL, true
}

// authenticate asks the authentication endpoint for a token with the
// password grant that registration sends. It follows no redirect, so the
// credentials go to the advertised endpoint and nowhere else.
func (r *connectionRun) authenticate(ctx context.Context, transport *http.Transport, authURL *url.URL, username, password string) {
	if authURL.Scheme != "https" {
		r.fail(CheckToken, "The API's authentication endpoint does not use HTTPS, so the test did not send the credentials.")

		return
	}

	req, err := newPasswordGrant(ctx, authURL, username, password)
	if err != nil {
		r.fail(CheckToken, "The API's authentication endpoint is not a valid URL.")

		return
	}

	client := &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	// The API the operator typed names this endpoint, and registration uses it too.
	resp, err := client.Do(req)
	if err != nil {
		r.fail(CheckToken, "The authentication endpoint did not answer: "+r.endpointReason(ctx, err)+".")

		return
	}

	defer func() { _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode == http.StatusOK:
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusBadRequest:
		r.fail(CheckToken, fmt.Sprintf("The authentication endpoint answered HTTP %d, so it did not accept the username and password.", resp.StatusCode))

		return
	case resp.StatusCode >= http.StatusMultipleChoices && resp.StatusCode < http.StatusBadRequest:
		r.fail(CheckToken, fmt.Sprintf("The authentication endpoint answered HTTP %d, a redirect the test does not follow.", resp.StatusCode))

		return
	default:
		r.fail(CheckToken, fmt.Sprintf("The authentication endpoint answered HTTP %d.", resp.StatusCode))

		return
	}

	var token struct {
		AccessToken string `json:"access_token"`
	}

	err = r.readJSON(resp, &token)
	issued := err == nil && token.AccessToken != ""
	token.AccessToken = ""

	if !issued {
		r.fail(CheckToken, "The authentication endpoint answered HTTP 200 without a token.")

		return
	}

	r.pass(CheckToken, "The credentials got a token from the API's authentication endpoint.")
}

// readJSON decodes at most the body limit of resp into target.
func (r *connectionRun) readJSON(resp *http.Response, target interface{}) error {
	body, err := readLimited(resp.Body, r.tester.bodyLimit())
	if err != nil {
		return err
	}

	err = json.Unmarshal(body, target)
	if err != nil {
		return fmt.Errorf("decoding the response: %w", err)
	}

	return nil
}

// sameOrigin follows a redirect only to the scheme and host the operator typed.
func sameOrigin(origin *url.URL) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != origin.Scheme || req.URL.Host != origin.Host {
			return errRedirectedOffHost
		}

		if len(via) >= connectionTestRedirects {
			return errTooManyRedirects
		}

		return nil
	}
}

func isCertificateError(err error) bool {
	var verification *tls.CertificateVerificationError

	var unknown x509.UnknownAuthorityError

	var hostname x509.HostnameError

	var invalid x509.CertificateInvalidError

	return errors.As(err, &verification) || errors.As(err, &unknown) ||
		errors.As(err, &hostname) || errors.As(err, &invalid)
}

func certificateReason(err error) string {
	var hostname x509.HostnameError
	if errors.As(err, &hostname) {
		return "the certificate is for a different host"
	}

	var invalid x509.CertificateInvalidError
	if errors.As(err, &invalid) {
		if invalid.Reason == x509.Expired {
			return "the certificate has expired or is not yet valid"
		}

		return "the certificate is not valid"
	}

	return "the certificate is not signed by a trusted authority"
}

func isHandshakeError(err error) bool {
	var record tls.RecordHeaderError

	var alert tls.AlertError

	return errors.As(err, &record) || errors.As(err, &alert)
}

// networkReason says why a request got no answer, without quoting the error,
// whose text holds the request URL.
func (r *connectionRun) networkReason(ctx context.Context, err error) string {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return "no answer within " + r.timeout.String()
	}

	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return "the host name did not resolve"
	}

	if errors.Is(err, syscall.ECONNREFUSED) {
		return "the connection was refused"
	}

	if errors.Is(err, syscall.ECONNRESET) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return "the connection was closed"
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "the connection timed out"
	}

	return "the connection failed"
}

// endpointReason adds certificate and handshake failures to networkReason,
// because the authentication endpoint is a host of its own.
func (r *connectionRun) endpointReason(ctx context.Context, err error) string {
	switch {
	case isCertificateError(err):
		return "its certificate did not verify, because " + certificateReason(err)
	case isHandshakeError(err):
		return "it did not complete a TLS handshake"
	default:
		return r.networkReason(ctx, err)
	}
}
