package client

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/fivetwenty-io/boshapi/internal/auth"
	"github.com/fivetwenty-io/boshapi/internal/redact"
	"github.com/fivetwenty-io/boshapi/pkg/boshapi"
)

// User authentication types a director's GET /info can name.
const (
	authTypeUAA   = "uaa"
	authTypeBasic = "basic"
)

// authDiscovery finds out how the director authenticates, the first time a
// request needs credentials. It reads GET /info without credentials, under
// the context of that request, through the client's own transport, so a
// private CA is trusted. The credentials it finds are kept for the life of
// the client. A discovery that fails, including one whose context ended, is
// not kept, so the next request tries again, and nothing falls back to
// another kind of credentials.
type authDiscovery struct {
	discoveryConfig

	// state holds the discovered credentials once discovery succeeds.
	// Requests read it while the first discovery may still be writing it.
	state atomic.Pointer[auth.Credentials]
	// slot lets one discovery run at a time. A caller waits for it only as
	// long as its own context lasts.
	slot chan struct{}
}

// discoveryConfig is what an authDiscovery needs to know.
type discoveryConfig struct {
	director string
	clientID string
	secret   string
	// allowBasic sends the client ID and secret as HTTP Basic when the
	// director takes HTTP Basic. Only AuthDiscover allows it.
	allowBasic bool
	// readInfo reads the director's GET /info without credentials.
	readInfo func(ctx context.Context) (*boshapi.Info, error)
	// newTokens returns the token manager for a UAA token URL.
	newTokens func(tokenURL string) auth.TokenManager
	logger    boshapi.Logger
}

func newAuthDiscovery(config discoveryConfig) *authDiscovery {
	return &authDiscovery{discoveryConfig: config, slot: make(chan struct{}, 1)}
}

// credentials returns the discovered credentials, discovering them under
// ctx when no discovery has succeeded yet. A caller whose context has ended
// never starts a discovery.
func (d *authDiscovery) credentials(ctx context.Context) (*auth.Credentials, error) {
	if creds := d.state.Load(); creds != nil {
		return creds, nil
	}

	err := d.acquire(ctx)
	if err != nil {
		return nil, err
	}

	defer d.release()

	// Another caller may have finished a discovery while this one waited.
	if creds := d.state.Load(); creds != nil {
		return creds, nil
	}

	creds, err := d.discover(ctx)
	if err != nil {
		return nil, err
	}

	d.state.Store(creds)

	return creds, nil
}

// acquire takes the slot, or returns an AuthError wrapping the context error
// when ctx ends first.
func (d *authDiscovery) acquire(ctx context.Context) error {
	if ctx.Err() != nil {
		return d.failure(context.Cause(ctx))
	}

	select {
	case d.slot <- struct{}{}:
	case <-ctx.Done():
		return d.failure(context.Cause(ctx))
	}

	// Both cases may have been ready, and select picks one at random.
	if ctx.Err() != nil {
		d.release()

		return d.failure(context.Cause(ctx))
	}

	return nil
}

func (d *authDiscovery) release() {
	<-d.slot
}

// discover reads GET /info and returns the credentials it calls for.
func (d *authDiscovery) discover(ctx context.Context) (*auth.Credentials, error) {
	info, err := d.readInfo(ctx)
	if err != nil {
		return nil, d.failure(err)
	}

	// An answer that came after the context ended is not kept, because the
	// caller has already been told its request failed.
	if ctx.Err() != nil {
		return nil, d.failure(context.Cause(ctx))
	}

	userAuth := info.UserAuthentication

	switch userAuth.Type {
	case authTypeUAA:
		return d.uaaCredentials(userAuth.Options.URL)
	case authTypeBasic:
		return d.basicCredentials()
	default:
		return nil, &boshapi.AuthError{
			Stage:    boshapi.AuthStageDiscovery,
			Director: d.director,
			Description: fmt.Sprintf("the director's user_authentication type is %q, "+
				"but this client supports only %q and %q", userAuth.Type, authTypeUAA, authTypeBasic),
		}
	}
}

func (d *authDiscovery) uaaCredentials(uaaURL string) (*auth.Credentials, error) {
	if uaaURL == "" {
		return nil, &boshapi.AuthError{
			Stage:       boshapi.AuthStageDiscovery,
			Director:    d.director,
			Description: "the director's user_authentication type is \"uaa\", but it gives no options.url for UAA",
			Err:         boshapi.ErrNoUAAEndpoint,
		}
	}

	tokenURL := strings.TrimSuffix(uaaURL, "/") + "/oauth/token"
	d.log("the director authenticates with UAA", map[string]interface{}{"token_url": redact.URL(tokenURL)})

	return &auth.Credentials{Tokens: d.newTokens(tokenURL)}, nil
}

func (d *authDiscovery) basicCredentials() (*auth.Credentials, error) {
	if !d.allowBasic {
		return nil, &boshapi.AuthError{
			Stage:    boshapi.AuthStageDiscovery,
			Director: d.director,
			Description: "the director's user_authentication type is \"basic\", but ClientID and ClientSecret " +
				"are UAA client credentials. Set Config.Auth to AuthDiscover to send them as HTTP Basic, " +
				"or set Username and Password instead",
		}
	}

	d.log("the director authenticates with HTTP Basic", map[string]interface{}{})

	return &auth.Credentials{Username: d.clientID, Password: d.secret}, nil
}

// failure returns the AuthError for a discovery that did not finish.
func (d *authDiscovery) failure(err error) *boshapi.AuthError {
	return &boshapi.AuthError{Stage: boshapi.AuthStageDiscovery, Director: d.director, Err: err}
}

func (d *authDiscovery) log(msg string, fields map[string]interface{}) {
	if d.logger == nil {
		return
	}

	fields["director"] = d.director
	d.logger.Debug(msg, fields)
}
