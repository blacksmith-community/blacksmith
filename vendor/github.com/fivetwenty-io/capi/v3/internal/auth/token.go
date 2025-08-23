package auth

import (
	"context"
	"sync"
	"time"
)

// TokenManager manages authentication tokens
type TokenManager interface {
	// GetToken returns a valid access token, refreshing if necessary
	GetToken(ctx context.Context) (string, error)
	// RefreshToken forces a token refresh
	RefreshToken(ctx context.Context) error
	// SetToken manually sets the access token
	SetToken(token string, expiresAt time.Time)
}

// Token represents an OAuth2 token
type Token struct {
	AccessToken  string    `json:"access_token"`
	TokenType    string    `json:"token_type"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	ExpiresIn    int       `json:"expires_in,omitempty"`
	Scope        string    `json:"scope,omitempty"`
	ExpiresAt    time.Time `json:"-"`
}

// Valid returns true if the token is valid and not expired
func (t *Token) Valid() bool {
	if t == nil || t.AccessToken == "" {
		return false
	}
	if t.ExpiresAt.IsZero() {
		return true // No expiry set, assume valid
	}
	// Add 30 second buffer before expiry
	return time.Now().Add(30 * time.Second).Before(t.ExpiresAt)
}

// TokenStore provides thread-safe token storage
type TokenStore struct {
	mu    sync.RWMutex
	token *Token
}

// NewTokenStore creates a new token store
func NewTokenStore() *TokenStore {
	return &TokenStore{}
}

// Get returns the current token
func (s *TokenStore) Get() *Token {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.token
}

// Set stores a new token
func (s *TokenStore) Set(token *Token) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.token = token
}

// Clear removes the stored token
func (s *TokenStore) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.token = nil
}
