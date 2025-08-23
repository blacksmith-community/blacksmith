package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// OAuth2Config represents OAuth2 configuration
type OAuth2Config struct {
	TokenURL     string
	ClientID     string
	ClientSecret string
	Username     string
	Password     string
	RefreshToken string
	AccessToken  string
	Scopes       []string
	HTTPClient   *http.Client
}

// OAuth2TokenManager implements TokenManager using OAuth2
type OAuth2TokenManager struct {
	config *OAuth2Config
	store  *TokenStore
}

// NewOAuth2TokenManager creates a new OAuth2 token manager
func NewOAuth2TokenManager(config *OAuth2Config) *OAuth2TokenManager {
	if config.HTTPClient == nil {
		config.HTTPClient = &http.Client{
			Timeout: 30 * time.Second,
		}
	}

	manager := &OAuth2TokenManager{
		config: config,
		store:  NewTokenStore(),
	}

	// If access token is provided, store it
	if config.AccessToken != "" {
		manager.store.Set(&Token{
			AccessToken: config.AccessToken,
			TokenType:   "bearer",
		})
	}

	return manager
}

// GetToken returns a valid access token, refreshing if necessary
func (m *OAuth2TokenManager) GetToken(ctx context.Context) (string, error) {
	token := m.store.Get()

	// If token is valid, return it
	if token != nil && token.Valid() {
		return token.AccessToken, nil
	}

	// Need to refresh or get new token
	return m.refreshToken(ctx)
}

// RefreshToken forces a token refresh
func (m *OAuth2TokenManager) RefreshToken(ctx context.Context) error {
	_, err := m.refreshToken(ctx)
	return err
}

// SetToken manually sets the access token
func (m *OAuth2TokenManager) SetToken(token string, expiresAt time.Time) {
	m.store.Set(&Token{
		AccessToken: token,
		TokenType:   "bearer",
		ExpiresAt:   expiresAt,
	})
}

// refreshToken performs the actual token refresh/acquisition
func (m *OAuth2TokenManager) refreshToken(ctx context.Context) (string, error) {
	// Check what credentials we have available
	token := m.store.Get()

	var newToken *Token
	var err error

	switch {
	case token != nil && token.RefreshToken != "":
		// Use refresh token
		newToken, err = m.doRefreshTokenGrant(ctx, token.RefreshToken)
	case m.config.RefreshToken != "":
		// Use configured refresh token
		newToken, err = m.doRefreshTokenGrant(ctx, m.config.RefreshToken)
	case m.config.ClientID != "" && m.config.ClientSecret != "":
		// Use client credentials
		newToken, err = m.doClientCredentialsGrant(ctx)
	case m.config.Username != "" && m.config.Password != "":
		// Use password grant
		newToken, err = m.doPasswordGrant(ctx)
	default:
		return "", fmt.Errorf("no valid credentials available for token refresh")
	}

	if err != nil {
		return "", err
	}

	// Calculate expiry time
	if newToken.ExpiresIn > 0 {
		newToken.ExpiresAt = time.Now().Add(time.Duration(newToken.ExpiresIn) * time.Second)
	}

	// Store the new token
	m.store.Set(newToken)

	return newToken.AccessToken, nil
}

// doClientCredentialsGrant performs client credentials OAuth2 flow
func (m *OAuth2TokenManager) doClientCredentialsGrant(ctx context.Context) (*Token, error) {
	data := url.Values{
		"grant_type": {"client_credentials"},
	}

	if len(m.config.Scopes) > 0 {
		data.Set("scope", strings.Join(m.config.Scopes, " "))
	}

	return m.doTokenRequest(ctx, data)
}

// doPasswordGrant performs password OAuth2 flow
func (m *OAuth2TokenManager) doPasswordGrant(ctx context.Context) (*Token, error) {
	data := url.Values{
		"grant_type": {"password"},
		"username":   {m.config.Username},
		"password":   {m.config.Password},
	}

	if len(m.config.Scopes) > 0 {
		data.Set("scope", strings.Join(m.config.Scopes, " "))
	}

	return m.doTokenRequest(ctx, data)
}

// doRefreshTokenGrant performs refresh token OAuth2 flow
func (m *OAuth2TokenManager) doRefreshTokenGrant(ctx context.Context, refreshToken string) (*Token, error) {
	data := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
	}

	return m.doTokenRequest(ctx, data)
}

// doTokenRequest performs the actual HTTP request to get a token
func (m *OAuth2TokenManager) doTokenRequest(ctx context.Context, data url.Values) (*Token, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", m.config.TokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("creating token request: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	// Add client credentials if available
	if m.config.ClientID != "" {
		req.SetBasicAuth(m.config.ClientID, m.config.ClientSecret)
	}

	resp, err := m.config.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("executing token request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading token response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errResp struct {
			Error            string `json:"error"`
			ErrorDescription string `json:"error_description"`
		}
		if err := json.Unmarshal(body, &errResp); err == nil && errResp.Error != "" {
			return nil, fmt.Errorf("token request failed: %s - %s", errResp.Error, errResp.ErrorDescription)
		}
		return nil, fmt.Errorf("token request failed with status %d: %s", resp.StatusCode, string(body))
	}

	var token Token
	if err := json.Unmarshal(body, &token); err != nil {
		return nil, fmt.Errorf("parsing token response: %w", err)
	}

	return &token, nil
}

// UAATokenManager provides UAA-specific token management
type UAATokenManager struct {
	*OAuth2TokenManager
	uaaURL string
}

// NewUAATokenManager creates a token manager for UAA
func NewUAATokenManager(uaaURL, clientID, clientSecret string) *UAATokenManager {
	tokenURL := strings.TrimSuffix(uaaURL, "/") + "/oauth/token"

	return &UAATokenManager{
		OAuth2TokenManager: NewOAuth2TokenManager(&OAuth2Config{
			TokenURL:     tokenURL,
			ClientID:     clientID,
			ClientSecret: clientSecret,
			Scopes:       []string{"cloud_controller.read", "cloud_controller.write"},
		}),
		uaaURL: uaaURL,
	}
}

// NewUAATokenManagerWithPassword creates a token manager for UAA with username/password
func NewUAATokenManagerWithPassword(uaaURL, clientID, clientSecret, username, password string) *UAATokenManager {
	tokenURL := strings.TrimSuffix(uaaURL, "/") + "/oauth/token"

	return &UAATokenManager{
		OAuth2TokenManager: NewOAuth2TokenManager(&OAuth2Config{
			TokenURL:     tokenURL,
			ClientID:     clientID,
			ClientSecret: clientSecret,
			Username:     username,
			Password:     password,
			Scopes:       []string{"cloud_controller.read", "cloud_controller.write"},
		}),
		uaaURL: uaaURL,
	}
}
