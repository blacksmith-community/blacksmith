package credhub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"time"
)

const (
	opRegenerate = "regenerate"
	// regenerateTimeout is the deadline for one regenerate call. It is
	// longer than a read because CredHub signs a new certificate.
	regenerateTimeout = 30 * time.Second
	// maxRegenerateBody bounds a regenerate answer, which carries a private
	// key.
	maxRegenerateBody = 64 << 10
	// regenerateBody asks CredHub for a new current version, never a
	// transitional one.
	regenerateBody = `{"set_as_transitional":false}`
)

// Fixed phrases that explain why an outcome is uncertain. None of them
// carries server text.
const (
	causeDeadline  = "deadline passed after the request was sent"
	causeConn      = "connection failed after the request was sent"
	causeServer    = "CredHub answered with a server error"
	causeStatus    = "CredHub answered with an unexpected status"
	causeTooLarge  = "the answer was larger than the limit"
	causeUnread    = "the answer could not be read"
	causeUndecoded = "the answer could not be decoded"
)

// RegenerateResult names the version CredHub created. It never holds a
// private key.
type RegenerateResult struct {
	VersionID string
	Name      string
}

// UncertainError says that a regenerate call may or may not have created a
// new version, so the caller must look at CredHub before it tries again.
type UncertainError struct {
	// Op names the operation.
	Op string
	// Cause is a fixed phrase chosen by the client, never server text.
	Cause string
}

func (e *UncertainError) Error() string {
	return fmt.Sprintf("CredHub %s has an uncertain outcome: %s", e.Op, e.Cause)
}

// IsUncertain reports whether err says a regenerate may have taken effect.
func IsUncertain(err error) bool {
	var uncertain *UncertainError

	return errors.As(err, &uncertain)
}

type regenerateAnswer struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Regenerate asks CredHub once to make a new version of the certificate with
// this ID. It never retries, not even after a 401, because a second request
// could make a second version. An answer that leaves the outcome open, such
// as a deadline or a dropped connection after the request was sent, a 5xx, or
// an answer that cannot be read, comes back as an UncertainError. A 4xx
// answer and a failure before the connection opened are certain.
func (c *Client) Regenerate(ctx context.Context, certificateID string) (*RegenerateResult, error) {
	if certificateID == "" {
		return nil, ErrEmptyCertificateID
	}

	token, err := c.token(ctx)
	if err != nil {
		return nil, fmt.Errorf("CredHub %s: %w", opRegenerate, err)
	}

	ctx, cancel := context.WithTimeout(ctx, regenerateTimeout)
	defer cancel()

	var written bool

	trace := &httptrace.ClientTrace{WroteRequest: func(info httptrace.WroteRequestInfo) { written = info.Err == nil }}

	endpoint := c.baseURL + certificatesPath + "/" + url.PathEscape(certificateID) + "/regenerate"

	httpReq, err := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, trace), http.MethodPost, endpoint, bytes.NewReader([]byte(regenerateBody)))
	if err != nil {
		return nil, fmt.Errorf("CredHub %s: building the request: %w", opRegenerate, err)
	}

	httpReq.Header.Set("Authorization", bearerPrefix+token)
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("Content-Type", "application/json")

	// The shared client caps every request at 15 s. This call has its own
	// 30 s deadline, so it uses a copy without the client-wide cap.
	longClient := *c.httpClient
	longClient.Timeout = 0

	response, err := longClient.Do(httpReq)
	if err != nil {
		if !written {
			return nil, fmt.Errorf("CredHub %s: %w", opRegenerate, scrubbed(err, token))
		}

		cause := causeConn
		if ctx.Err() != nil {
			cause = causeDeadline
		}

		return nil, &UncertainError{Op: opRegenerate, Cause: cause}
	}

	defer func() { _ = response.Body.Close() }()

	return c.regenerateOutcome(ctx, response, token)
}

func (c *Client) regenerateOutcome(ctx context.Context, response *http.Response, token string) (*RegenerateResult, error) {
	status := response.StatusCode

	switch {
	case status >= http.StatusInternalServerError:
		drain(response.Body)

		return nil, &UncertainError{Op: opRegenerate, Cause: causeServer}
	case status >= http.StatusBadRequest:
		drain(response.Body)

		if status == http.StatusUnauthorized {
			c.tokens.Invalidate(token)
		}

		return nil, &APIError{Op: opRegenerate, Status: status}
	case status < http.StatusOK || status >= http.StatusMultipleChoices:
		drain(response.Body)

		return nil, &UncertainError{Op: opRegenerate, Cause: causeStatus}
	}

	// One byte past the limit shows that the answer was longer.
	body, err := io.ReadAll(io.LimitReader(response.Body, maxRegenerateBody+1))
	if err != nil {
		clear(body)

		cause := causeUnread
		if ctx.Err() != nil {
			cause = causeDeadline
		}

		return nil, &UncertainError{Op: opRegenerate, Cause: cause}
	}

	if len(body) > maxRegenerateBody {
		clear(body)

		return nil, &UncertainError{Op: opRegenerate, Cause: fmt.Sprintf("%s (%d bytes)", causeTooLarge, maxRegenerateBody)}
	}

	var parsed regenerateAnswer

	err = json.Unmarshal(body, &parsed)

	clear(body)

	if err != nil || parsed.ID == "" {
		return nil, &UncertainError{Op: opRegenerate, Cause: causeUndecoded}
	}

	return &RegenerateResult{VersionID: parsed.ID, Name: parsed.Name}, nil
}

// drain reads and drops a bounded part of an answer body, so the text it
// holds is never kept.
func drain(body io.Reader) {
	raw, _ := io.ReadAll(io.LimitReader(body, maxErrorBody))
	clear(raw)
}
