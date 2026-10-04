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
	"strings"
	"sync"
	"time"
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
	clientID     string
	clientSecret string
	httpClient   *http.Client
	now          func() time.Time

	mu       sync.Mutex
	token    string
	expiry   time.Time
	inflight *tokenFetch
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

// Token returns a token with more than refreshMargin of validity left,
// fetching one when the cache has none. Callers that arrive during a fetch
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
	token, expiresIn, err := s.fetch(ctx)

	s.mu.Lock()

	if err == nil {
		s.token = token
		s.expiry = issued.Add(time.Duration(expiresIn) * time.Second)
	}

	s.inflight = nil
	fetch.token = token
	fetch.err = err
	s.mu.Unlock()

	close(fetch.done)
}

func (s *TokenSource) fetch(ctx context.Context) (string, int64, error) {
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
