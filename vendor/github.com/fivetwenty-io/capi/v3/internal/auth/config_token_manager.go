package auth

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"
)

// ConfigPersister defines the interface for persisting config changes.
// Static errors for err113 compliance.
var (
	ErrNoConfigPersister = errors.New("no config persister configured")
)

type ConfigPersister interface {
	UpdateAPIToken(apiDomain, token string, expiresAt time.Time, refreshToken string) error
}

// ConfigTokenManager wraps OAuth2TokenManager and automatically persists tokens to config.
type ConfigTokenManager struct {
	oauth2Manager   *OAuth2TokenManager
	configPersister ConfigPersister
	apiDomain       string
	mutex           sync.RWMutex
	initialToken    string
	initialExpiry   time.Time
}

// NewConfigTokenManager creates a new config-persisting token manager.
func NewConfigTokenManager(config *OAuth2Config, configPersister ConfigPersister, apiDomain string, initialToken string, initialExpiry time.Time) *ConfigTokenManager {
	oauth2Manager := NewOAuth2TokenManager(config)

	// If we have an initial token, set it in the OAuth2 manager
	if initialToken != "" {
		oauth2Manager.SetToken(initialToken, initialExpiry)
	}

	return &ConfigTokenManager{
		oauth2Manager:   oauth2Manager,
		configPersister: configPersister,
		apiDomain:       apiDomain,
		initialToken:    initialToken,
		initialExpiry:   initialExpiry,
	}
}

// GetToken returns a valid access token, refreshing if necessary.
//
// Holds the full write lock for its duration: when the underlying OAuth2
// manager refreshes the token, GetToken updates the cached initialToken/
// initialExpiry fields, which RefreshToken also writes under the write lock.
// A read lock here would race those writes between concurrent GetToken calls.
func (m *ConfigTokenManager) GetToken(ctx context.Context) (string, error) {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	token, err := m.oauth2Manager.GetToken(ctx)
	if err != nil {
		return "", err
	}

	// Check if the token was refreshed and persist it
	currentToken := m.oauth2Manager.store.Get()
	if currentToken != nil && (currentToken.AccessToken != m.initialToken || !currentToken.ExpiresAt.Equal(m.initialExpiry)) {
		// Token was refreshed, persist it
		go func() {
			persistErr := m.persistToken(currentToken)
			if persistErr != nil {
				// Log error but don't fail the request
				_, _ = fmt.Fprintf(os.Stderr, "Warning: failed to persist refreshed token: %v\n", persistErr)
			}
		}()

		// Update our cached values
		m.initialToken = currentToken.AccessToken
		m.initialExpiry = currentToken.ExpiresAt
	}

	return token, nil
}

// RefreshToken forces a token refresh.
func (m *ConfigTokenManager) RefreshToken(ctx context.Context) error {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	err := m.oauth2Manager.RefreshToken(ctx)
	if err != nil {
		return err
	}

	// Persist the refreshed token
	currentToken := m.oauth2Manager.store.Get()
	if currentToken != nil {
		persistErr := m.persistToken(currentToken)
		if persistErr != nil {
			_, _ = fmt.Fprintf(os.Stderr, "Warning: failed to persist refreshed token: %v\n", persistErr)
		}

		// Update our cached values
		m.initialToken = currentToken.AccessToken
		m.initialExpiry = currentToken.ExpiresAt
	}

	return nil
}

// SetToken manually sets the access token.
func (m *ConfigTokenManager) SetToken(token string, expiresAt time.Time) {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	m.oauth2Manager.SetToken(token, expiresAt)
	m.initialToken = token
	m.initialExpiry = expiresAt
}

// IsTokenExpiringSoon returns true if the token expires within the given duration.
func (m *ConfigTokenManager) IsTokenExpiringSoon(within time.Duration) bool {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	token := m.oauth2Manager.store.Get()
	if token == nil {
		return true
	}

	return time.Now().Add(within).After(token.ExpiresAt)
}

// GetTokenExpiry returns the current token's expiration time.
func (m *ConfigTokenManager) GetTokenExpiry() time.Time {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	token := m.oauth2Manager.store.Get()
	if token == nil {
		return time.Time{}
	}

	return token.ExpiresAt
}

// persistToken saves the token to config.
func (m *ConfigTokenManager) persistToken(token *Token) error {
	if m.configPersister == nil {
		return ErrNoConfigPersister
	}

	err := m.configPersister.UpdateAPIToken(m.apiDomain, token.AccessToken, token.ExpiresAt, token.RefreshToken)
	if err != nil {
		return fmt.Errorf("failed to update API token: %w", err)
	}

	return nil
}
