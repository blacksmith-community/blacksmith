package http

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/fivetwenty-io/boshapi/pkg/boshapi"
	"github.com/hashicorp/go-retryablehttp"
)

// requestState follows one logical request across its attempts. CheckRetry
// reads it from the request context, which go-retryablehttp passes in.
type requestState struct {
	// mutation is true when a resend could repeat a side effect on the
	// director: any method but GET and HEAD, and a GET that starts a task.
	mutation bool
	// noRetry is true when the caller asked for one attempt only, or the
	// body is a stream that cannot be replayed.
	noRetry bool
	// sent is set once the request headers were handed to the connection
	// on any attempt, which is before they are flushed.
	sent atomic.Bool
	// attempts counts the attempts made so far.
	attempts atomic.Int32
	// lastStatus and lastBody hold the director's answer to the last
	// attempt that checkRetry sent again, with the body cut to
	// maxErrorBody bytes. checkRetry writes them and
	// newTransportError reads them, both on the caller's goroutine.
	lastStatus int
	lastBody   []byte
}

type requestStateKey struct{}

// isMutation reports whether a request is unsafe to resend blindly.
func isMutation(req *Request) bool {
	if req.CreatesTask {
		return true
	}

	return req.Method != http.MethodGet && req.Method != http.MethodHead
}

// withRequestState attaches a fresh requestState to ctx, along with an
// httptrace hook that records when the request headers were handed to the
// connection.
func withRequestState(ctx context.Context, req *Request) (context.Context, *requestState) {
	state := &requestState{
		mutation: isMutation(req),
		noRetry:  boshapi.RetriesDisabled(ctx) || req.Stream != nil,
	}

	ctx = context.WithValue(ctx, requestStateKey{}, state)
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		WroteHeaders: func() { state.sent.Store(true) },
	})

	return ctx, state
}

// checkRetry decides whether go-retryablehttp sends the request again.
//
// Reads (GET and HEAD) are retried on dial errors, connection resets,
// timeouts, and HTTP 429, 502, 503, and 504. A mutation, which includes a
// GET that starts a director task, is retried only when its headers were
// never written and the failure was a dial, DNS, or TLS handshake failure,
// because only then is it certain the director never saw it. A request whose
// context came from boshapi.WithoutRetries is never retried. checkRetry
// never returns an error, so the caller always gets the attempt's own error
// or response.
func checkRetry(ctx context.Context, resp *http.Response, err error) (bool, error) {
	state, ok := ctx.Value(requestStateKey{}).(*requestState)
	if !ok {
		return false, nil
	}

	state.attempts.Add(1)

	if ctx.Err() != nil || state.noRetry {
		return false, nil //nolint:nilerr // the caller gets the attempt's own error
	}

	if err != nil {
		resend := isTransientTransportError(err) && (!state.mutation || !state.sent.Load())

		return resend, nil //nolint:nilerr // the caller gets the attempt's own error
	}

	if state.mutation {
		return false, nil
	}

	switch resp.StatusCode {
	case http.StatusTooManyRequests,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		state.recordAnswer(resp)

		return true, nil
	}

	return false, nil
}

// recordAnswer keeps the status and the first maxErrorBody bytes of an
// answer that checkRetry is about to send again, so the error can still name
// it if the caller's context ends during the backoff. It keeps far more than
// the description shows, because newAPIError hides the credentials first and
// cuts afterwards, and a credential that the cut split would not match. It
// puts the body back together, because the caller gets this response whole
// when the retries run out.
func (s *requestState) recordAnswer(resp *http.Response) {
	// A body that cannot be read in full only shortens the description, so
	// the read error is not reported.
	prefix, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))

	resp.Body = struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(prefix), resp.Body), resp.Body}

	s.lastStatus = resp.StatusCode
	s.lastBody = prefix
}

// cappedBackoff is go-retryablehttp's default backoff, held to at most
// waitMax. The default honours a Retry-After header on 429 and 503 with no
// ceiling, so a gateway asking for an hour would stall the request that long.
func cappedBackoff(waitMin, waitMax time.Duration, attempt int, resp *http.Response) time.Duration {
	return min(retryablehttp.DefaultBackoff(waitMin, waitMax, attempt, resp), waitMax)
}

// isTransientTransportError reports a connection failure that a later
// attempt may not meet: a dial or DNS failure, a reset or closed connection,
// or a timeout. A certificate the client does not trust is not transient.
func isTransientTransportError(err error) bool {
	var (
		verifyErr    *tls.CertificateVerificationError
		authorityErr x509.UnknownAuthorityError
		hostnameErr  x509.HostnameError
		invalidErr   x509.CertificateInvalidError
	)

	if errors.As(err, &verifyErr) || errors.As(err, &authorityErr) ||
		errors.As(err, &hostnameErr) || errors.As(err, &invalidErr) {
		return false
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}

	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == "dial" {
		return true
	}

	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}

	return errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.ECONNABORTED) ||
		errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF)
}

// newTransportError builds the error for a request that got no answer.
// Timeout is true only when the HTTP client's or the transport's own
// deadline fired, not when the caller's context ended. An error that does
// not already name the director URL, such as the bare context error that
// go-retryablehttp returns when the context ends during a backoff, is
// wrapped in a *url.Error that does.
func newTransportError(httpReq *http.Request, req *Request, state *requestState, err error) *boshapi.TransportError {
	contextErr := httpReq.Context().Err()

	// Look for the timeout before any wrapping: a *url.Error reports only
	// whether the error it wraps directly is a timeout, so it would hide one
	// under a fmt.Errorf wrapper such as the body-read error.
	var netErr net.Error

	timedOut := errors.As(err, &netErr) && netErr.Timeout() && contextErr == nil

	var urlErr *url.Error
	if !errors.As(err, &urlErr) {
		err = &url.Error{Op: urlErrorOp(httpReq.Method), URL: httpReq.URL.Redacted(), Err: err}
	}

	var cutShort *bodyCutShortError

	bodyCutShort := errors.As(err, &cutShort)

	var lastAnswer *boshapi.APIError
	if state.lastStatus != 0 {
		lastAnswer = newAPIError(state.lastStatus, state.lastBody, req, sentWith(httpReq))
	}

	return &boshapi.TransportError{
		Method:       req.Method,
		Path:         req.Path,
		ContextID:    httpReq.Header.Get(contextIDHeader),
		Sent:         state.sent.Load(),
		BodyCutShort: bodyCutShort,
		Timeout:      timedOut,
		Mutation:     state.mutation,
		Attempts:     int(state.attempts.Load()),
		ContextErr:   contextErr,
		LastAnswer:   lastAnswer,
		Err:          err,
	}
}

// sentWith reads back the credentials the request carried from its
// Authorization header, which is a bearer token, HTTP Basic, or absent.
func sentWith(httpReq *http.Request) sentAuth {
	token, found := strings.CutPrefix(httpReq.Header.Get("Authorization"), "Bearer ")
	if found {
		return sentAuth{token: token}
	}

	username, password, found := httpReq.BasicAuth()
	if found {
		return sentAuth{username: username, password: password}
	}

	return sentAuth{}
}

// urlErrorOp returns the Op that net/http gives a *url.Error for method,
// such as "Get" for GET.
func urlErrorOp(method string) string {
	if method == "" {
		return "Get"
	}

	return method[:1] + strings.ToLower(method[1:])
}

// NewTokenClient returns the HTTP client that asks UAA for tokens. It sends
// through the JSON client in clients, so it trusts the same CA and has the
// same timeout, and it never follows a redirect. A token grant has no side
// effects, so a request is sent again, up to retryMax times, after a dial
// error, a reset or closed connection, a timeout, or a 5xx answer, with a
// backoff from waitMin to waitMax. A context from boshapi.WithoutRetries
// gets one attempt. When the retries run out, the caller gets the last
// answer or error.
func NewTokenClient(clients *Clients, retryMax int, waitMin, waitMax time.Duration) *http.Client {
	retryClient := retryablehttp.NewClient()
	retryClient.HTTPClient = withoutRedirects(clients.JSON)
	retryClient.Logger = nil
	retryClient.RetryMax = retryMax
	retryClient.RetryWaitMin = waitMin
	retryClient.RetryWaitMax = waitMax
	retryClient.ErrorHandler = retryablehttp.PassthroughErrorHandler
	retryClient.CheckRetry = checkTokenRetry
	retryClient.Backoff = cappedBackoff

	return &http.Client{
		Transport:     &retryablehttp.RoundTripper{Client: retryClient},
		CheckRedirect: noRedirect,
	}
}

// checkTokenRetry decides whether a token request is sent again, as
// NewTokenClient describes. It never returns an error, so the caller always
// gets the attempt's own error or answer.
func checkTokenRetry(ctx context.Context, resp *http.Response, err error) (bool, error) {
	if ctx.Err() != nil || boshapi.RetriesDisabled(ctx) {
		return false, nil //nolint:nilerr // the caller gets the attempt's own error
	}

	if err != nil {
		return isTransientTransportError(err), nil
	}

	return resp.StatusCode >= http.StatusInternalServerError, nil
}
