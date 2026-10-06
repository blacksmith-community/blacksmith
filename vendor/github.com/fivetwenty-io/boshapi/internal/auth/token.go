package auth

import (
	"context"
	"fmt"
	"time"

	"github.com/fivetwenty-io/boshapi/pkg/boshapi"
)

// tokenExpirationBuffer is how long before its expiry a token is replaced.
const tokenExpirationBuffer = 30 * time.Second

// TokenManager hands out bearer tokens for director requests.
type TokenManager interface {
	// GetToken returns a token that is valid for at least 30 more seconds,
	// fetching a new one when the cached one is missing or about to expire.
	GetToken(ctx context.Context) (string, error)
	// RefreshToken replaces stale, the token a request carried when the
	// director answered 401, and returns the replacement. When another
	// caller has already replaced stale, it returns that token and sends
	// nothing. An empty stale asks only for a token that has not expired.
	RefreshToken(ctx context.Context, stale string) (string, error)
}

// Credentials say how a request to the director authenticates: with a
// bearer token from Tokens when it is set, with HTTP Basic when Username is
// set, and not at all otherwise.
type Credentials struct {
	Tokens   TokenManager
	Username string
	Password string
}

// Resolver returns the credentials for a request. It may find them out on
// first use, under the context of the request that needs them.
type Resolver func(ctx context.Context) (*Credentials, error)

// Fixed returns a Resolver that always returns creds.
func Fixed(creds Credentials) Resolver {
	return func(context.Context) (*Credentials, error) {
		return &creds, nil
	}
}

// StaticToken is a bearer token given in the configuration. It is never
// refreshed.
type StaticToken string

// GetToken returns the token.
func (s StaticToken) GetToken(context.Context) (string, error) {
	return string(s), nil
}

// RefreshToken fails with an error that wraps
// boshapi.ErrStaticTokenCannotRefresh, because a static token has no source
// to refresh it from. The error says what to do next, since the director
// rejected the token and nothing here can replace it.
func (s StaticToken) RefreshToken(context.Context, string) (string, error) {
	return "", fmt.Errorf("%w: the director rejected the AccessToken in the configuration. "+
		"Check that it has not expired, then supply a fresh AccessToken, or drop AccessToken "+
		"so that ClientID and ClientSecret are used", boshapi.ErrStaticTokenCannotRefresh)
}

// Token represents an OAuth2 token.
type Token struct {
	AccessToken string    `json:"access_token"`
	TokenType   string    `json:"token_type"`
	ExpiresIn   int       `json:"expires_in,omitempty"`
	ExpiresAt   time.Time `json:"-"`
}

// Valid returns true if the token is set and does not expire within the
// next 30 seconds.
func (t *Token) Valid() bool {
	if t == nil || t.AccessToken == "" {
		return false
	}

	if t.ExpiresAt.IsZero() {
		return true
	}

	return time.Now().Add(tokenExpirationBuffer).Before(t.ExpiresAt)
}
