package cf

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// AuthInfo represents CF authentication information
type AuthInfo struct {
	AccessToken  string    `json:"access_token"`
	TokenType    string    `json:"token_type"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresIn    int       `json:"expires_in"`
	Scope        string    `json:"scope"`
	JTI          string    `json:"jti"`
	ExpiresAt    time.Time `json:"-"`
}

// CFAuthClient handles Cloud Foundry authentication
type CFAuthClient struct {
	APIURL     string
	Username   string
	Password   string
	httpClient *http.Client
	authInfo   *AuthInfo
}

// NewCFAuthClient creates a new CF authentication client
func NewCFAuthClient(apiURL, username, password string) *CFAuthClient {
	return &CFAuthClient{
		APIURL:   strings.TrimSuffix(apiURL, "/"),
		Username: username,
		Password: password,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// Authenticate performs CF authentication and obtains access token
func (c *CFAuthClient) Authenticate() error {
	// First, get the authorization endpoint from CF info
	infoURL := fmt.Sprintf("%s/v2/info", c.APIURL)
	resp, err := c.httpClient.Get(infoURL)
	if err != nil {
		return fmt.Errorf("failed to get CF info: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("CF info request failed with status: %d", resp.StatusCode)
	}

	var info struct {
		AuthorizationEndpoint string `json:"authorization_endpoint"`
		TokenEndpoint         string `json:"token_endpoint"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return fmt.Errorf("failed to decode CF info response: %w", err)
	}

	// Authenticate with the token endpoint
	tokenURL := fmt.Sprintf("%s/oauth/token", strings.TrimSuffix(info.TokenEndpoint, "/"))

	data := url.Values{}
	data.Set("grant_type", "password")
	data.Set("username", c.Username)
	data.Set("password", c.Password)

	req, err := http.NewRequest("POST", tokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return fmt.Errorf("failed to create auth request: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.SetBasicAuth("cf", "")

	resp, err = c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to authenticate: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		var errorResp struct {
			Error            string `json:"error"`
			ErrorDescription string `json:"error_description"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&errorResp); err != nil {
			return fmt.Errorf("authentication failed (%d): failed to decode error response: %w", resp.StatusCode, err)
		}
		return fmt.Errorf("authentication failed (%d): %s - %s", resp.StatusCode, errorResp.Error, errorResp.ErrorDescription)
	}

	var authInfo AuthInfo
	if err := json.NewDecoder(resp.Body).Decode(&authInfo); err != nil {
		return fmt.Errorf("failed to decode auth response: %w", err)
	}

	// Calculate expiration time
	authInfo.ExpiresAt = time.Now().Add(time.Duration(authInfo.ExpiresIn) * time.Second)
	c.authInfo = &authInfo

	return nil
}

// GetAuthToken returns a valid access token, refreshing if necessary
func (c *CFAuthClient) GetAuthToken() (string, error) {
	if c.authInfo == nil || time.Now().After(c.authInfo.ExpiresAt.Add(-5*time.Minute)) {
		if err := c.Authenticate(); err != nil {
			return "", err
		}
	}
	return fmt.Sprintf("%s %s", c.authInfo.TokenType, c.authInfo.AccessToken), nil
}

// IsAuthenticated checks if the client has valid authentication
func (c *CFAuthClient) IsAuthenticated() bool {
	return c.authInfo != nil && time.Now().Before(c.authInfo.ExpiresAt.Add(-5*time.Minute))
}

// TestConnection tests the CF connection and authentication
func (c *CFAuthClient) TestConnection() (*CFInfo, error) {
	// Test basic connectivity to CF API
	infoURL := fmt.Sprintf("%s/v2/info", c.APIURL)
	resp, err := c.httpClient.Get(infoURL)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to CF API: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("CF API returned status: %d", resp.StatusCode)
	}

	var rawInfo struct {
		Name        string `json:"name"`
		Version     string `json:"version"`
		Description string `json:"description"`
		Build       string `json:"build"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&rawInfo); err != nil {
		return nil, fmt.Errorf("failed to decode CF info: %w", err)
	}

	// Test authentication
	if err := c.Authenticate(); err != nil {
		return nil, fmt.Errorf("authentication failed: %w", err)
	}

	cfInfo := &CFInfo{
		Name:        rawInfo.Name,
		Version:     rawInfo.Version,
		Description: rawInfo.Description,
		APIURL:      c.APIURL,
	}

	return cfInfo, nil
}

// MakeAuthenticatedRequest makes an authenticated request to CF API
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
	req, err := http.NewRequest(method, url, bytes.NewReader(reqBody))
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
