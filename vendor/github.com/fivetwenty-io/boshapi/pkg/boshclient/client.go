package boshclient

import (
	"context"
	"fmt"
	"strings"

	"github.com/fivetwenty-io/boshapi/internal/client"
	"github.com/fivetwenty-io/boshapi/pkg/boshapi"
)

// New creates a new BOSH Director API client. It sends no request: when the
// config calls for it, the first request that needs credentials reads how
// the director authenticates. New works on a copy of config, so it never
// changes the caller's struct. A DirectorURL with no scheme gets https://,
// and a trailing slash is dropped.
func New(ctx context.Context, config *boshapi.Config) (boshapi.Client, error) {
	if config == nil {
		return nil, boshapi.ErrConfigRequired
	}

	if config.DirectorURL == "" {
		return nil, boshapi.ErrDirectorURLRequired
	}

	own := *config

	own.DirectorURL = strings.TrimSuffix(own.DirectorURL, "/")
	if !strings.HasPrefix(own.DirectorURL, "http://") && !strings.HasPrefix(own.DirectorURL, "https://") {
		own.DirectorURL = "https://" + own.DirectorURL
	}

	c, err := client.New(ctx, &own)
	if err != nil {
		return nil, fmt.Errorf("failed to create client: %w", err)
	}

	return c, nil
}

// constructorRetries is the RetryMax the convenience constructors set: one
// attempt and up to three retries, which suits most directors.
const constructorRetries = 3

// NewWithToken creates a new client with a static access token. It sets
// RetryMax to 3; use New with a Config to choose another value.
func NewWithToken(ctx context.Context, directorURL, token string) (boshapi.Client, error) {
	return New(ctx, &boshapi.Config{
		DirectorURL: directorURL,
		AccessToken: token,
		RetryMax:    constructorRetries,
	})
}

// NewWithClientCredentials creates a new client using OAuth2 client
// credentials. It sets RetryMax to 3; use New with a Config to choose
// another value.
func NewWithClientCredentials(ctx context.Context, directorURL, clientID, clientSecret string) (boshapi.Client, error) {
	return New(ctx, &boshapi.Config{
		DirectorURL:  directorURL,
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RetryMax:     constructorRetries,
	})
}

// NewWithBasicAuth creates a new client using HTTP Basic Authentication. It
// sets RetryMax to 3; use New with a Config to choose another value.
func NewWithBasicAuth(ctx context.Context, directorURL, username, password string) (boshapi.Client, error) {
	return New(ctx, &boshapi.Config{
		DirectorURL: directorURL,
		Username:    username,
		Password:    password,
		RetryMax:    constructorRetries,
	})
}
