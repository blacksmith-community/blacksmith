package credhub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const (
	opFind   = "find"
	opDelete = "delete"
	dataPath = "/api/v1/data"
	// maxFindBody bounds a find answer, which lists names only.
	maxFindBody = 32 << 20
	// maxErrorBody bounds how much of an error answer is read.
	maxErrorBody = 64 << 10
	bearerPrefix = "Bearer "
)

// Client errors.
var (
	ErrNotFound           = errors.New("credential does not exist")
	ErrEmptyVersionID     = errors.New("refusing to read a certificate version with an empty ID")
	ErrProtectedName      = errors.New("refusing to delete a credential under a protected prefix")
	ErrEmptyName          = errors.New("refusing to delete a credential with an empty name")
	ErrEmptyCertificateID = errors.New("refusing to regenerate a certificate with an empty ID")
	ErrInvalidCredHubURL  = errors.New("CredHub URL must be an https URL")
	ErrNoTokenProvider    = errors.New("CredHub client needs a token provider")
	ErrBadFindAnswer      = errors.New("CredHub find answer is not valid JSON")
	ErrAnswerTooLarge     = errors.New("CredHub answer is larger than the limit")
)

// tokenProvider is what the client needs from a TokenSource.
type tokenProvider interface {
	Token(ctx context.Context) (string, error)
	Invalidate(stale string)
}

// ClientConfig locates the director's CredHub and the certificate that
// anchors trust in it.
type ClientConfig struct {
	URL    string
	CACert string
}

// APIError is a CredHub answer the client could not treat as success. It
// carries CredHub's error text cut to 200 characters, never a token, and
// never a request or response body beyond that text.
type APIError struct {
	// Op names the operation that failed, such as "find" or "delete".
	Op string
	// Status is the HTTP status CredHub answered.
	Status int
	// Refreshed records that the request was already retried once with a
	// fresh token after a 401.
	Refreshed bool
	// Message is CredHub's error text.
	Message string
}

func (e *APIError) Error() string {
	message := fmt.Sprintf("CredHub %s answered %d %s", e.Op, e.Status, http.StatusText(e.Status))

	if e.Refreshed {
		message += " after a token refresh"
	}

	if e.Message != "" {
		message += ": " + e.Message
	}

	return message
}

// Client is a CredHub client that can find credential names under a path,
// delete one credential by its exact name, read certificate metadata,
// version lists, and the public half of a certificate, and regenerate one
// certificate by its ID. CertificateKeyPair, which patch mode alone uses,
// is its only read of a private key. It has no way to read any other
// secret value, to set or generate a credential, or to change a
// permission.
type Client struct {
	baseURL    string
	tokens     tokenProvider
	protected  []string
	httpClient *http.Client
	// bufferSeen lets a test look at the answer buffer after it was cleared.
	bufferSeen func([]byte)
}

// request describes one CredHub call.
type request struct {
	operation string
	method    string
	path      string
	query     url.Values
	body      []byte
	// maxBody bounds the answer. Zero means the answer is not read.
	maxBody int64
	// opaqueErrors drops all of CredHub's error text from a failure. Every
	// call that can carry a secret value sets it.
	opaqueErrors bool
	// oneBuffer reads the answer into one buffer of maxBody+1 bytes that
	// is allocated before the read, so no smaller buffer that a growing
	// read would drop is left holding part of a key. It suits a small
	// maxBody only.
	oneBuffer bool
}

type findResponse struct {
	Credentials []struct {
		Name string `json:"name"`
	} `json:"credentials"`
}

type credhubErrorResponse struct {
	Error string `json:"error"`
}

// NewClient builds a client for the CredHub at cfg.URL, trusting only
// cfg.CACert. Delete refuses any name that starts with one of
// protectedPrefixes.
func NewClient(cfg ClientConfig, tokens tokenProvider, protectedPrefixes []string) (*Client, error) {
	if !isHTTPSURL(cfg.URL) {
		return nil, ErrInvalidCredHubURL
	}

	if tokens == nil {
		return nil, ErrNoTokenProvider
	}

	httpClient, err := pinnedHTTPClient(cfg.CACert)
	if err != nil {
		return nil, err
	}

	var protected []string

	for _, prefix := range protectedPrefixes {
		if prefix != "" {
			protected = append(protected, prefix)
		}
	}

	return &Client{
		baseURL:    strings.TrimRight(cfg.URL, "/"),
		tokens:     tokens,
		protected:  protected,
		httpClient: httpClient,
	}, nil
}

// FindByPath returns the names of the credentials CredHub lists under path.
// CredHub matches the path with SQL LIKE, so callers must filter the names
// again with an exact prefix.
func (c *Client) FindByPath(ctx context.Context, path string) ([]string, error) {
	body, err := c.send(ctx, request{
		operation: opFind,
		method:    http.MethodGet,
		path:      dataPath,
		query:     url.Values{"path": {path}},
		maxBody:   maxFindBody,
	})
	if err != nil {
		return nil, err
	}

	var parsed findResponse

	err = json.Unmarshal(body, &parsed)
	if err != nil {
		return nil, ErrBadFindAnswer
	}

	names := make([]string, 0, len(parsed.Credentials))
	for _, credential := range parsed.Credentials {
		names = append(names, credential.Name)
	}

	return names, nil
}

// Delete deletes every version of the credential with exactly this name. It
// returns ErrNotFound when CredHub has no such credential, and refuses a name
// under a protected prefix before it sends anything.
func (c *Client) Delete(ctx context.Context, name string) error {
	if name == "" {
		return ErrEmptyName
	}

	for _, prefix := range c.protected {
		if strings.HasPrefix(name, prefix) {
			return fmt.Errorf("%w: %q is under %q", ErrProtectedName, name, prefix)
		}
	}

	_, err := c.send(ctx, request{
		operation: opDelete,
		method:    http.MethodDelete,
		path:      dataPath,
		query:     url.Values{"name": {name}},
	})

	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
		return fmt.Errorf("%w: %q", ErrNotFound, name)
	}

	return err
}

// send makes one request and, when CredHub answers 401, invalidates the
// rejected token and makes it once more with a fresh one.
func (c *Client) send(ctx context.Context, req request) ([]byte, error) {
	token, err := c.token(ctx)
	if err != nil {
		return nil, fmt.Errorf("CredHub %s: %w", req.operation, err)
	}

	body, err := c.attempt(ctx, req, token)

	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusUnauthorized {
		return body, err
	}

	c.tokens.Invalidate(token)

	token, err = c.token(ctx)
	if err != nil {
		return nil, fmt.Errorf("CredHub %s after a 401: %w", req.operation, err)
	}

	body, err = c.attempt(ctx, req, token)
	if errors.As(err, &apiErr) {
		apiErr.Refreshed = true
	}

	return body, err
}

func (c *Client) token(ctx context.Context) (string, error) {
	token, err := c.tokens.Token(ctx)
	if err != nil {
		return "", fmt.Errorf("getting a token: %w", err)
	}

	if token == "" {
		return "", ErrUAANoToken
	}

	return token, nil
}

func (c *Client) attempt(ctx context.Context, req request, token string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	var payload io.Reader
	if req.body != nil {
		payload = bytes.NewReader(req.body)
	}

	request, err := http.NewRequestWithContext(ctx, req.method, c.baseURL+req.path+"?"+req.query.Encode(), payload)
	if err != nil {
		return nil, fmt.Errorf("CredHub %s: building the request: %w", req.operation, err)
	}

	request.Header.Set("Authorization", bearerPrefix+token)
	request.Header.Set("Accept", "application/json")

	if req.body != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("CredHub %s: %w", req.operation, scrubbed(err, token))
	}

	defer func() { _ = response.Body.Close() }()

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, failure(req, response, token)
	}

	if req.maxBody == 0 {
		return nil, nil
	}

	// One byte past the limit shows that the answer was longer, which a
	// plain LimitReader would hide by cutting it short.
	body, err := readAnswer(response.Body, req)
	if err != nil {
		clear(body)

		return nil, fmt.Errorf("CredHub %s: reading the answer: %w", req.operation, scrubbed(err, token))
	}

	if int64(len(body)) > req.maxBody {
		clear(body)

		return nil, fmt.Errorf("CredHub %s: %w (%d bytes)", req.operation, ErrAnswerTooLarge, req.maxBody)
	}

	return body, nil
}

// readAnswer reads at most req.maxBody+1 bytes of an answer. With
// req.oneBuffer it reads them into a buffer allocated once at that size.
func readAnswer(body io.Reader, req request) ([]byte, error) {
	if !req.oneBuffer {
		return io.ReadAll(io.LimitReader(body, req.maxBody+1)) //nolint:wrapcheck // attempt wraps it
	}

	buf := make([]byte, req.maxBody+1)

	n, err := io.ReadFull(body, buf)
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		err = nil
	}

	return buf[:n], err //nolint:wrapcheck // attempt wraps it
}

// failure turns a non-2xx answer into an APIError. For an opaque request it
// reads the error body only to drain it and keeps none of its text, because
// a request that can carry a value can get an error that echoes one.
func failure(req request, response *http.Response, token string) error {
	raw, _ := io.ReadAll(io.LimitReader(response.Body, maxErrorBody))

	if req.opaqueErrors {
		clear(raw)

		return &APIError{Op: req.operation, Status: response.StatusCode}
	}

	var parsed credhubErrorResponse

	_ = json.Unmarshal(raw, &parsed)

	return &APIError{
		Op:      req.operation,
		Status:  response.StatusCode,
		Message: cut(redact(parsed.Error, token)),
	}
}
