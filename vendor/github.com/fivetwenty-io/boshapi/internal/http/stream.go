package http

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/fivetwenty-io/boshapi/internal/constants"
	"github.com/fivetwenty-io/boshapi/pkg/boshapi"
	"github.com/hashicorp/go-retryablehttp"
)

// maxErrorBody is the most of an error answer's body that the client reads
// to build an APIError. A director's JSON error is far smaller, and the
// description is cut to maxErrorDescription after the credentials are
// hidden. Reading more than the description keeps matters, because a token
// that straddles the cut would otherwise survive in part.
const maxErrorBody = 64 * 1024

// GetStream performs a GET whose body is handed back unread, for a download
// that may be large or slow. The caller must close the returned body.
//
// The request goes through the stream client, so HTTPTimeout bounds only the
// wait for the response headers, and ctx bounds the body read. It is a plain
// read: until the headers arrive, it is retried under the read rules, which
// are the same as for Get. Once GetStream returns, the caller holds the body
// and nothing is retried, so a Read that fails midway returns the error and
// the caller starts the download again. A non-2xx answer returns an
// *boshapi.APIError, a redirect returns a *boshapi.RedirectError, and a
// request that gets no answer returns a *boshapi.TransportError. None of them
// returns a body.
func (c *Client) GetStream(ctx context.Context, path string, query url.Values) (io.ReadCloser, error) {
	return c.doStream(ctx, &Request{
		Method:  http.MethodGet,
		Path:    path,
		Query:   query,
		Headers: map[string]string{"Accept": "*/*"},
	})
}

// doStream sends req through the stream client and returns the body of a 2xx
// answer. It sets up the request as Do does. Any other answer closes its body
// and returns an error.
func (c *Client) doStream(ctx context.Context, req *Request) (io.ReadCloser, error) {
	err := checkContextID(boshapi.ContextIDFrom(ctx), req)
	if err != nil {
		return nil, err
	}

	fullURL, err := c.buildURL(req.Path, req.Query)
	if err != nil {
		return nil, fmt.Errorf("building URL: %w", err)
	}

	reqCtx, state := withRequestState(ctx, req)

	httpReq, err := retryablehttp.NewRequestWithContext(reqCtx, req.Method, fullURL, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	sent, err := c.setupHeaders(ctx, httpReq, req)
	if err != nil {
		return nil, err
	}

	if c.debug && c.logger != nil {
		c.logRequest(httpReq)
	}

	httpResp, err := c.streamClient.Do(httpReq)
	if err != nil {
		if httpResp != nil {
			_ = httpResp.Body.Close()
		}

		return nil, newTransportError(httpReq.Request, req, state, err)
	}

	if c.debug && c.logger != nil {
		c.logger.Debug("HTTP Response", map[string]interface{}{
			"status_code": httpResp.StatusCode,
			"streaming":   true,
			"headers":     firstValues(httpResp.Header),
		})
	}

	if httpResp.StatusCode >= http.StatusOK && httpResp.StatusCode < http.StatusMultipleChoices {
		return &streamBody{
			body:    httpResp.Body,
			ctx:     ctx,
			method:  req.Method,
			path:    req.Path,
			baseURL: c.baseURL,
		}, nil
	}

	return c.streamFailure(ctx, httpResp, httpReq, req, sent)
}

// streamFailure turns an answer that is not 2xx into the error doStream
// returns, and closes the answer's body. A 401 to a request that carried a
// bearer token refreshes the token and sends the request once more, as Do
// does, so its result is the resend's body or error.
func (c *Client) streamFailure(
	ctx context.Context, httpResp *http.Response, httpReq *retryablehttp.Request, req *Request, sent sentAuth,
) (io.ReadCloser, error) {
	response := c.readFailure(httpResp, httpReq, req)

	if response.StatusCode < constants.HTTPStatusBadRequest {
		// A GET that is not a task-creating read has no task to read from
		// the redirect, so handleRedirect always returns the error.
		_, err := c.handleRedirect(response, req)

		return nil, err
	}

	apiErr := newAPIError(response.StatusCode, response.Body, req, sent)

	// A 401 to a request that carried a bearer token refreshes the token and
	// sends the request once more, as Do does. A 401 with code 600000 refuses
	// a valid token that lacks a scope, which a new token cannot fix.
	if response.StatusCode != http.StatusUnauthorized || sent.tokens == nil || req.isRetry ||
		!boshapi.IsUnauthorized(apiErr) {
		return nil, apiErr
	}

	err := refreshRejectedToken(ctx, apiErr, sent)
	if err != nil {
		return nil, err
	}

	resend := *req
	resend.isRetry = true

	body, err := c.doStream(ctx, &resend)

	return body, explainFreshTokenRejected(err)
}

// readFailure reads the answer that is not 2xx, up to maxErrorBody
// bytes of its body, and closes the body.
func (c *Client) readFailure(httpResp *http.Response, httpReq *retryablehttp.Request, req *Request) *Response {
	// A body that cannot be read in full only shortens the director's
	// description in the error, so the read error is not reported.
	body, _ := io.ReadAll(io.LimitReader(httpResp.Body, maxErrorBody))

	closeErr := httpResp.Body.Close()
	if closeErr != nil && c.logger != nil {
		c.logger.Warn("failed to close response body", map[string]interface{}{"error": closeErr.Error()})
	}

	return &Response{
		StatusCode: httpResp.StatusCode,
		Body:       body,
		Headers:    httpResp.Header,
		method:     req.Method,
		path:       req.Path,
		baseURL:    c.baseURL,
		contextID:  httpReq.Header.Get(contextIDHeader),
	}
}

// firstValues returns the first value of each header, for the debug log.
func firstValues(headers http.Header) map[string]string {
	values := make(map[string]string, len(headers))

	for key, list := range headers {
		if len(list) > 0 {
			values[key] = list[0]
		}
	}

	return values
}

// streamBody is the body of a download. It counts the bytes it hands out, so
// a read that fails midway can say how far the download got and what to do.
type streamBody struct {
	body    io.ReadCloser
	ctx     context.Context //nolint:containedctx // the context ends the read, and the error names it
	method  string
	path    string
	baseURL string
	read    int64
}

// Read reads from the director's body. io.EOF is returned as it is, and any
// other error is wrapped with what failed, how many bytes had arrived, and
// what to check. The wrapped error still matches the underlying one under
// errors.Is, so context.Canceled and io.ErrUnexpectedEOF stay detectable.
func (b *streamBody) Read(buf []byte) (int, error) {
	count, err := b.body.Read(buf)
	b.read += int64(count)

	if err == nil || errors.Is(err, io.EOF) {
		return count, err //nolint:wrapcheck // io.EOF must reach the caller unchanged
	}

	return count, b.explain(err)
}

// explain wraps a body read error with its specifics and the next step. It
// says the caller's context ended the read only when err is that context's
// own error.
func (b *streamBody) explain(err error) error {
	prefix := fmt.Sprintf("reading the body of %s %s from the BOSH director at %s failed after %d bytes",
		b.method, b.path, b.baseURL, b.read)

	// The read is blamed on the context only when the error is the context's
	// own, or the cause it was cancelled with. A reset or a short body that
	// came first, before the caller's context ended, is reported as itself.
	contextErr := b.ctx.Err()
	if contextErr != nil && !errors.Is(err, contextErr) && !errors.Is(err, context.Cause(b.ctx)) {
		contextErr = nil
	}

	switch {
	case errors.Is(contextErr, context.DeadlineExceeded):
		return fmt.Errorf("%s: %w. The caller's context deadline passed, so the client stopped reading. "+
			"If the deadline is too short for this download, give the context a longer one", prefix, err)
	case contextErr != nil:
		return fmt.Errorf("%s: %w. The caller's context was cancelled, so the client stopped reading. "+
			"Check what cancelled the context, such as a shutdown of the calling process", prefix, err)
	default:
		return fmt.Errorf("%s: %w. The connection broke or stalled before the whole body arrived. Check that "+
			"the director is running, the network path between this client and the director, and the "+
			"blobstore the director reads from. The request was not resent, because bytes had already "+
			"reached the caller; start the download again from the beginning", prefix, err)
	}
}

// Close closes the director's body.
func (b *streamBody) Close() error {
	err := b.body.Close()
	if err != nil {
		return fmt.Errorf("closing the body of %s %s: %w", b.method, b.path, err)
	}

	return nil
}
