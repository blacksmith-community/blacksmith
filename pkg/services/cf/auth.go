package cf

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Static errors for err113 compliance.
var (
	ErrCFInfoRequestFailed  = errors.New("CF info request failed with status")
	ErrAuthenticationFailed = errors.New("authentication failed")
	ErrCFAPIReturnedError   = errors.New("CF API returned error status")
	ErrCFAPIUnreachable     = errors.New("the CF API did not answer")
	ErrAPIURLNotHTTPS       = errors.New("API URL must start with https://, because the API's answer decides where the password is sent")
)

// oauthErrorCodes are the token endpoint error codes RFC 6749 section 5.2
// fixes. An error is repeated only when it is one of these, because any
// other text the endpoint sends, such as error_description, could echo the
// request.
var oauthErrorCodes = map[string]bool{
	"invalid_request":        true,
	"invalid_client":         true,
	"invalid_grant":          true,
	"unauthorized_client":    true,
	"unsupported_grant_type": true,
	"invalid_scope":          true,
}

// AuthInfo represents CF authentication information.
type AuthInfo struct {
	AccessToken  string    `json:"access_token"`
	TokenType    string    `json:"token_type"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresIn    int       `json:"expires_in"`
	Scope        string    `json:"scope"`
	JTI          string    `json:"jti"`
	ExpiresAt    time.Time `json:"-"`
}

// CFAuthClient handles Cloud Foundry authentication.
type CFAuthClient struct {
	APIURL     string
	Username   string
	Password   string
	httpClient *http.Client
	authInfo   *AuthInfo
}

// NewCFAuthClient creates a new CF authentication client.
func NewCFAuthClient(apiURL, username, password string) *CFAuthClient {
	return &CFAuthClient{
		APIURL:   strings.TrimSuffix(apiURL, "/"),
		Username: username,
		Password: password,
		httpClient: &http.Client{
			Timeout: DefaultHTTPTimeout,
		},
	}
}

// Authenticate performs CF authentication and obtains access token.
func (c *CFAuthClient) Authenticate() error {
	authURL, err := c.connect()
	if err != nil {
		return err
	}

	return c.requestToken(authURL)
}

// connect asks the API root for its authentication endpoint, within
// AuthContextTimeout, and sends no credentials.
func (c *CFAuthClient) connect() (*url.URL, error) {
	ctx, cancel := context.WithTimeout(context.Background(), AuthContextTimeout)
	defer cancel()

	return c.authEndpoint(ctx)
}

// registrationAPIURL parses the API URL registration connects to. It must
// use https, because the root document it answers names the endpoint the
// password goes to.
func registrationAPIURL(raw string) (*url.URL, error) {
	parsed, err := parseAPIURL(raw)
	if err != nil {
		return nil, err
	}

	if parsed.Scheme != "https" {
		return nil, ErrAPIURLNotHTTPS
	}

	return parsed, nil
}

// authEndpoint asks the API root where the API signs operators in, the
// same way the connection test does.
func (c *CFAuthClient) authEndpoint(ctx context.Context) (*url.URL, error) {
	body, err := c.getPublic(ctx, "/")
	if err != nil {
		return nil, fmt.Errorf("failed to get CF API root: %w", err)
	}

	authURL, err := authEndpointFromRoot(body)
	if err != nil {
		return nil, fmt.Errorf("failed to find the CF authentication endpoint: %w", err)
	}

	return authURL, nil
}

// getPublic reads an endpoint of the API that needs no token. It follows
// redirects the way the connection test does, only on the typed scheme and
// host.
func (c *CFAuthClient) getPublic(ctx context.Context, path string) ([]byte, error) {
	origin, err := registrationAPIURL(c.APIURL)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, origin.String()+path, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Accept", "application/json")

	resp, err := rootClient(c.httpClient, origin).Do(req)
	if err != nil {
		return nil, rootRequestError(ctx, err)
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: %d", ErrCFAPIReturnedError, resp.StatusCode)
	}

	return readLimited(resp.Body, ConnectionTestBodyLimit)
}

// rootRequestError says why a request to the API root got no answer,
// without quoting the request. A redirect the client refused comes back as
// it is, and anything else as ErrCFAPIUnreachable with a reason.
func rootRequestError(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, ErrAPIRedirectedOffHost):
		return ErrAPIRedirectedOffHost
	case errors.Is(err, ErrAPITooManyRedirects):
		return ErrAPITooManyRedirects
	default:
		return fmt.Errorf("%w: %s", ErrCFAPIUnreachable, endpointReason(ctx, err, AuthContextTimeout))
	}
}

// requestToken sends the password grant to authURL and keeps the token. It
// sends nothing without a password or to an http endpoint, and it follows
// no redirect, the same rules the connection test applies.
func (c *CFAuthClient) requestToken(authURL *url.URL) error {
	if c.Password == "" {
		return ErrPasswordRequired
	}

	err := checkTokenEndpoint(authURL)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), AuthContextTimeout)
	defer cancel()

	req, err := newPasswordGrant(ctx, authURL, c.Username, c.Password)
	if err != nil {
		return fmt.Errorf("failed to create auth request: %w", err)
	}

	resp, err := tokenClient(c.httpClient).Do(req)
	if err != nil {
		return fmt.Errorf("%w: the authentication endpoint did not answer: %s", ErrAuthenticationFailed, endpointReason(ctx, err, AuthContextTimeout))
	}

	defer func() { _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode == http.StatusOK:
	case resp.StatusCode >= http.StatusMultipleChoices && resp.StatusCode < http.StatusBadRequest:
		return fmt.Errorf("%w (HTTP %d)", ErrTokenEndpointRedirected, resp.StatusCode)
	default:
		return tokenRefusal(resp)
	}

	body, err := readLimited(resp.Body, ConnectionTestBodyLimit)
	if err != nil {
		return fmt.Errorf("failed to read auth response: %w", err)
	}

	var authInfo AuthInfo

	err = json.Unmarshal(body, &authInfo)
	if err != nil {
		return fmt.Errorf("failed to decode auth response: %w", err)
	}

	// Calculate expiration time
	authInfo.ExpiresAt = time.Now().Add(time.Duration(authInfo.ExpiresIn) * time.Second)
	c.authInfo = &authInfo

	return nil
}

// tokenRefusal names the HTTP status of a refused password grant, and the
// OAuth error code when it is one RFC 6749 fixes.
func tokenRefusal(resp *http.Response) error {
	var refusal struct {
		Error string `json:"error"`
	}

	body, err := readLimited(resp.Body, ConnectionTestBodyLimit)
	if err == nil && json.Unmarshal(body, &refusal) == nil && oauthErrorCodes[refusal.Error] {
		return fmt.Errorf("%w: the authentication endpoint answered HTTP %d (%s)", ErrAuthenticationFailed, resp.StatusCode, refusal.Error)
	}

	return fmt.Errorf("%w: the authentication endpoint answered HTTP %d", ErrAuthenticationFailed, resp.StatusCode)
}

// GetAuthToken returns a valid access token, refreshing if necessary.
func (c *CFAuthClient) GetAuthToken() (string, error) {
	if c.authInfo == nil || time.Now().After(c.authInfo.ExpiresAt.Add(-5*time.Minute)) {
		err := c.Authenticate()
		if err != nil {
			return "", err
		}
	}

	return fmt.Sprintf("%s %s", c.authInfo.TokenType, c.authInfo.AccessToken), nil
}

// IsAuthenticated checks if the client has valid authentication.
func (c *CFAuthClient) IsAuthenticated() bool {
	return c.authInfo != nil && time.Now().Before(c.authInfo.ExpiresAt.Add(-5*time.Minute))
}

// TestConnection tests the CF connection and authentication.
func (c *CFAuthClient) TestConnection() (*CFInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), AuthContextTimeout)
	defer cancel()

	// Test basic connectivity to CF API
	authURL, err := c.authEndpoint(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to CF API: %w", err)
	}

	// Test authentication
	err = c.requestToken(authURL)
	if err != nil {
		return nil, err
	}

	cfInfo := c.info(ctx)
	cfInfo.APIURL = c.APIURL

	return cfInfo, nil
}

// info reads the name, version, and description from /v3/info. They are
// for display only, so an API that does not answer leaves them empty.
func (c *CFAuthClient) info(ctx context.Context) *CFInfo {
	var rawInfo struct {
		Name        string          `json:"name"`
		Version     json.RawMessage `json:"version"`
		Description string          `json:"description"`
	}

	body, err := c.getPublic(ctx, "/v3/info")
	if err != nil || json.Unmarshal(body, &rawInfo) != nil {
		return &CFInfo{}
	}

	// /v3/info gives the version as a number, where /v2/info gave a string.
	version := string(rawInfo.Version)

	var quoted string
	if json.Unmarshal(rawInfo.Version, &quoted) == nil {
		version = quoted
	}

	return &CFInfo{
		Name:        rawInfo.Name,
		Version:     version,
		Description: rawInfo.Description,
	}
}

// MakeAuthenticatedRequest makes an authenticated request to CF API.
func (c *CFAuthClient) MakeAuthenticatedRequest(method, path string, body interface{}) (*http.Response, error) {
	token, err := c.GetAuthToken()
	if err != nil {
		return nil, fmt.Errorf("failed to get auth token: %w", err)
	}

	var reqBody []byte
	if body != nil {
		reqBody, err = json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal request body: %w", err)
		}
	}

	url := fmt.Sprintf("%s%s", c.APIURL, path)

	ctx, cancel := context.WithTimeout(context.Background(), AuthContextTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}

	return resp, nil
}
