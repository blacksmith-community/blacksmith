package credhub

import (
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
	ErrNotFound          = errors.New("credential does not exist")
	ErrProtectedName     = errors.New("refusing to delete a credential under a protected prefix")
	ErrEmptyName         = errors.New("refusing to delete a credential with an empty name")
	ErrInvalidCredHubURL = errors.New("CredHub URL must be an https URL")
	ErrNoTokenProvider   = errors.New("CredHub client needs a token provider")
	ErrBadFindAnswer     = errors.New("CredHub find answer is not valid JSON")
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
	// Op is "find" or "delete".
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

// Client is a CredHub client that can find credential names under a path and
// delete one credential by its exact name. It has no way to read a value,
// set, generate, or regenerate a credential, or change a permission.
type Client struct {
	baseURL    string
	tokens     tokenProvider
	protected  []string
	httpClient *http.Client
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
	body, err := c.send(ctx, opFind, http.MethodGet, url.Values{"path": {path}}, maxFindBody)
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

	_, err := c.send(ctx, opDelete, http.MethodDelete, url.Values{"name": {name}}, 0)

	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
		return fmt.Errorf("%w: %q", ErrNotFound, name)
	}

	return err
}

// send makes one request and, when CredHub answers 401, invalidates the
// rejected token and makes it once more with a fresh one.
func (c *Client) send(ctx context.Context, operation, method string, query url.Values, maxBody int64) ([]byte, error) {
	token, err := c.token(ctx)
	if err != nil {
		return nil, fmt.Errorf("CredHub %s: %w", operation, err)
	}

	body, err := c.attempt(ctx, operation, method, query, token, maxBody)

	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusUnauthorized {
		return body, err
	}

	c.tokens.Invalidate(token)

	token, err = c.token(ctx)
	if err != nil {
		return nil, fmt.Errorf("CredHub %s after a 401: %w", operation, err)
	}

	body, err = c.attempt(ctx, operation, method, query, token, maxBody)
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

func (c *Client) attempt(ctx context.Context, operation, method string, query url.Values, token string, maxBody int64) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+dataPath+"?"+query.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("CredHub %s: building the request: %w", operation, err)
	}

	request.Header.Set("Authorization", bearerPrefix+token)
	request.Header.Set("Accept", "application/json")

	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("CredHub %s: %w", operation, scrubbed(err, token))
	}

	defer func() { _ = response.Body.Close() }()

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		raw, _ := io.ReadAll(io.LimitReader(response.Body, maxErrorBody))

		var parsed credhubErrorResponse

		_ = json.Unmarshal(raw, &parsed)

		return nil, &APIError{
			Op:      operation,
			Status:  response.StatusCode,
			Message: cut(redact(parsed.Error, token)),
		}
	}

	if maxBody == 0 {
		return nil, nil
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, maxBody))
	if err != nil {
		return nil, fmt.Errorf("CredHub %s: reading the answer: %w", operation, scrubbed(err, token))
	}

	return body, nil
}
