package credhub

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"blacksmith/pkg/logger"
)

const (
	// refreshMargin is how much validity a cached token must have left before
	// Token hands it out.
	refreshMargin = 60 * time.Second
	// requestTimeout bounds every UAA and CredHub request.
	requestTimeout = 15 * time.Second
	// maxErrorText is how many characters of a server's error text an error
	// carries.
	maxErrorText = 200
	// maxUAABody bounds how much of a UAA answer is read.
	maxUAABody = 64 << 10
	redacted   = "[REDACTED]"
)

// Token source errors.
var (
	ErrUAAUnreachable  = errors.New("UAA token request failed")
	ErrUAANoToken      = errors.New("UAA answered without an access token")
	ErrInvalidUAAURL   = errors.New("UAA URL must be an https URL")
	ErrInvalidCACert   = errors.New("CA certificate does not contain a PEM certificate")
	ErrMissingClientID = errors.New("client ID is empty")
	ErrMissingSecret   = errors.New("client secret is empty")
)

// UAAError is a UAA token endpoint answer other than 200. It carries UAA's
// error and error_description, each cut to 200 characters, and never the
// client secret.
type UAAError struct {
	Status      int
	Code        string
	Description string
}

func (e *UAAError) Error() string {
	message := fmt.Sprintf("UAA token request answered %d %s", e.Status, http.StatusText(e.Status))

	if e.Code != "" {
		message += ": " + e.Code
	}

	if e.Description != "" {
		message += ": " + e.Description
	}

	return message
}

// TokenSource fetches client credentials tokens from the director's UAA. It
// caches the token, refreshes it when less than refreshMargin of validity is
// left, and lets concurrent callers share one in-flight fetch. It is safe for
// concurrent use.
type TokenSource struct {
	tokenURL     string
	resolveURL   func() (string, error)
	log          logger.Logger
	clientID     string
	clientSecret string
	httpClient   *http.Client
	now          func() time.Time

	mu       sync.Mutex
	token    string
	expiry   time.Time
	inflight *tokenFetch

	// outage tracks a run of failed refreshes the cached token covered for,
	// so the log gets one warning when it starts and one notice when it ends.
	outageSince    time.Time
	outageFailures int
}

// tokenFetch is one fetch that any number of callers wait on.
type tokenFetch struct {
	done  chan struct{}
	token string
	err   error
}

type tokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int64  `json:"expires_in"`
}

type uaaErrorResponse struct {
	Error       string `json:"error"`
	Description string `json:"error_description"`
}

// NewTokenSource builds a token source for the UAA at uaaURL, trusting only
// caCert. A nil now uses time.Now.
func NewTokenSource(uaaURL, clientID, clientSecret, caCert string, now func() time.Time) (*TokenSource, error) {
	if !isHTTPSURL(uaaURL) {
		return nil, ErrInvalidUAAURL
	}

	if clientID == "" {
		return nil, ErrMissingClientID
	}

	if clientSecret == "" {
		return nil, ErrMissingSecret
	}

	httpClient, err := pinnedHTTPClient(caCert)
	if err != nil {
		return nil, err
	}

	if now == nil {
		now = time.Now
	}

	return &TokenSource{
		tokenURL:     strings.TrimRight(uaaURL, "/") + "/oauth/token",
		clientID:     clientID,
		clientSecret: clientSecret,
		httpClient:   httpClient,
		now:          now,
	}, nil
}

// NewLazyTokenSource builds a token source whose UAA URL comes from resolve,
// which runs on a fetch until it succeeds and is not called again afterwards.
// It lets a source be built while the URL is still unknown, such as when the
// director's /info was unreachable at boot, and recover on a later fetch.
func NewLazyTokenSource(resolve func() (string, error), clientID, clientSecret, caCert string, now func() time.Time) (*TokenSource, error) {
	if resolve == nil {
		return nil, ErrInvalidUAAURL
	}

	source, err := NewTokenSource("https://unresolved.invalid", clientID, clientSecret, caCert, now)
	if err != nil {
		return nil, err
	}

	source.tokenURL = ""
	source.resolveURL = resolve

	return source, nil
}

// SetLogger sets where the source logs a refresh failure it papered over with
// a still-valid cached token. Without one it logs nothing.
func (s *TokenSource) SetLogger(log logger.Logger) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.log = log
}

// Token returns a token with more than refreshMargin of validity left,
// fetching one when the cache has none. When that fetch fails and the cached
// token has not yet expired, the cached token is returned and the failure is
// logged. Callers that arrive during a fetch
// share it, and each returns early when its own context ends.
func (s *TokenSource) Token(ctx context.Context) (string, error) {
	s.mu.Lock()

	if s.token != "" && s.expiry.Sub(s.now()) > refreshMargin {
		token := s.token
		s.mu.Unlock()

		return token, nil
	}

	fetch := s.inflight
	if fetch == nil {
		fetch = &tokenFetch{done: make(chan struct{})}
		s.inflight = fetch

		go s.runFetch(ctx, fetch)
	}

	s.mu.Unlock()

	select {
	case <-fetch.done:
		return fetch.token, fetch.err
	case <-ctx.Done():
		return "", fmt.Errorf("waiting for a UAA token: %w", ctx.Err())
	}
}

// Invalidate drops the cached token only when it still equals stale, so many
// callers rejected with the same token cause one fetch rather than many.
func (s *TokenSource) Invalidate(stale string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if stale != "" && s.token == stale {
		s.token = ""
		s.expiry = time.Time{}
	}
}

// runFetch runs one fetch under its own timeout, detached from the starting
// caller's cancellation so that caller giving up never fails the other
// waiters, and publishes the result to every waiter.
func (s *TokenSource) runFetch(callerCtx context.Context, fetch *tokenFetch) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(callerCtx), requestTimeout)
	defer cancel()

	issued := s.now()
	token, expiresIn, err := s.safeFetch(ctx)

	s.mu.Lock()

	switch {
	case err == nil:
		s.token = token
		s.expiry = issued.Add(time.Duration(expiresIn) * time.Second)
		s.endOutage()
	case s.token != "" && s.expiry.After(s.now()):
		s.noteCoveredFailure(err)

		token, err = s.token, nil
	default:
		s.countOutageFailure()
	}

	s.inflight = nil
	fetch.token = token
	fetch.err = err
	s.mu.Unlock()

	close(fetch.done)
}

// noteCoveredFailure logs a refresh failure the still-valid cached token
// covered for. The first failure of an outage is a warning and the rest are
// debug lines, so an outage does not fill the log with warnings. The caller
// holds s.mu.
func (s *TokenSource) noteCoveredFailure(err error) {
	first := s.outageFailures == 0
	if first {
		s.outageSince = s.now()
	}

	s.outageFailures++

	if s.log == nil {
		return
	}

	remaining := s.expiry.Sub(s.now()).Round(time.Second)

	if first {
		s.log.Warnf("the UAA token refresh failed (%v), so the cached token, which is valid for another %s, is still in use. Every request tries the refresh again, and only the first failure of this outage is a warning. A notice follows when a refresh works again. "+
			"Likely causes are a UAA that is down or not reachable from the broker, a CA certificate the UAA no longer matches, or client credentials the UAA now refuses",
			err, remaining)

		return
	}

	s.log.Debugf("the UAA token refresh failed again (%v), failure %d of this outage, so the cached token, which is valid for another %s, is still in use", err, s.outageFailures, remaining)
}

// countOutageFailure counts a failure that no cached token covered for, which
// the caller sees as an error and logs. It starts an outage when none is
// running, so the refresh that works again is still reported. The caller holds
// s.mu.
func (s *TokenSource) countOutageFailure() {
	if s.outageFailures == 0 {
		s.outageSince = s.now()
	}

	s.outageFailures++
}

// endOutage logs one notice when a refresh works after a run of failures, and
// clears the run. The caller holds s.mu.
func (s *TokenSource) endOutage() {
	if s.outageFailures == 0 {
		return
	}

	if s.log != nil {
		s.log.Infof("the UAA token refresh recovered, because a refresh succeeded after %d failed refreshes over %s", s.outageFailures, s.now().Sub(s.outageSince).Round(time.Second))
	}

	s.outageFailures = 0
	s.outageSince = time.Time{}
}

// safeFetch runs fetch and turns a panic into an error, so a fault in the
// fetch fails its waiters instead of crashing the broker.
func (s *TokenSource) safeFetch(ctx context.Context) (token string, expiresIn int64, err error) {
	defer func() {
		recovered := recover()
		if recovered != nil {
			token, expiresIn = "", 0
			err = fmt.Errorf("%w: the token fetch panicked: %s\n%s", ErrUAAUnreachable, s.scrub(fmt.Sprint(recovered)), debug.Stack())
		}
	}()

	return s.fetch(ctx)
}

func (s *TokenSource) fetch(ctx context.Context) (string, int64, error) {
	if s.tokenURL == "" {
		resolved, err := s.resolveURL()
		if err != nil {
			return "", 0, fmt.Errorf("%w: finding the UAA URL: %w", ErrUAAUnreachable, err)
		}

		if !isHTTPSURL(resolved) {
			return "", 0, fmt.Errorf("%w: %s", ErrInvalidUAAURL, redactURL(resolved))
		}

		s.tokenURL = strings.TrimRight(resolved, "/") + "/oauth/token"
	}

	form := url.Values{"grant_type": {"client_credentials"}}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", 0, fmt.Errorf("%w: building the request: %w", ErrUAAUnreachable, err)
	}

	request.SetBasicAuth(s.clientID, s.clientSecret)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")

	response, err := s.httpClient.Do(request)
	if err != nil {
		return "", 0, fmt.Errorf("%w: %w", ErrUAAUnreachable, scrubbed(err, s.clientSecret))
	}

	defer func() { _ = response.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(response.Body, maxUAABody))
	if err != nil {
		return "", 0, fmt.Errorf("%w: reading the answer: %w", ErrUAAUnreachable, scrubbed(err, s.clientSecret))
	}

	if response.StatusCode != http.StatusOK {
		var uaaErr uaaErrorResponse

		_ = json.Unmarshal(body, &uaaErr)

		return "", 0, &UAAError{
			Status:      response.StatusCode,
			Code:        cut(s.scrub(uaaErr.Error)),
			Description: cut(s.scrub(uaaErr.Description)),
		}
	}

	var parsed tokenResponse

	err = json.Unmarshal(body, &parsed)
	if err != nil || parsed.AccessToken == "" {
		return "", 0, ErrUAANoToken
	}

	return parsed.AccessToken, parsed.ExpiresIn, nil
}

// scrub removes the client secret from text that came back from UAA, in case
// it ever echoes it.
func (s *TokenSource) scrub(text string) string {
	return redact(text, s.clientSecret)
}

// redact replaces every occurrence of credential in text.
func redact(text, credential string) string {
	if credential == "" {
		return text
	}

	return strings.ReplaceAll(text, credential, redacted)
}

// scrubbedError keeps an error's chain for errors.Is and errors.As while its
// text has every occurrence of a credential replaced.
type scrubbedError struct {
	text  string
	cause error
}

func (e *scrubbedError) Error() string { return e.text }

func (e *scrubbedError) Unwrap() error { return e.cause }

// scrubbed wraps err so that its text never carries credential.
func scrubbed(err error, credential string) error {
	return &scrubbedError{text: redact(err.Error(), credential), cause: err}
}

// pinnedHTTPClient builds an HTTP client that trusts only caCert, requires
// TLS 1.2 or newer, never follows a redirect, and bounds each request.
func pinnedHTTPClient(caCert string) (*http.Client, error) {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(caCert)) {
		return nil, ErrInvalidCACert
	}

	transport := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		TLSClientConfig:     &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout: requestTimeout,
		ForceAttemptHTTP2:   true,
	}

	return &http.Client{
		Transport: transport,
		Timeout:   requestTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}, nil
}

// redactURL returns raw without any userinfo, so a URL that carries a
// password is safe to put in a log line or a problem string. A URL that does
// not parse is replaced by a placeholder rather than echoed.
func redactURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "[unparseable URL]"
	}

	parsed.User = nil

	return parsed.String()
}

func isHTTPSURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}

	return parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil
}

// cut shortens text to maxErrorText characters.
func cut(text string) string {
	runes := []rune(text)
	if len(runes) <= maxErrorText {
		return text
	}

	return string(runes[:maxErrorText])
}
