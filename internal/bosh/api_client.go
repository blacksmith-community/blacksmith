package bosh

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/fivetwenty-io/boshapi/pkg/boshapi"
	"github.com/fivetwenty-io/boshapi/pkg/boshclient"
)

// Errors the boshapi client path returns for conditions Blacksmith itself
// detects.
var (
	// ErrDirectorCredentialsMissing is returned when no client ID and secret
	// are configured, so the director client would have nothing to
	// authenticate with.
	ErrDirectorCredentialsMissing = errors.New("no BOSH director credentials are configured")
	// ErrEmptyDirectorAnswer is returned when the director answered a read with
	// a success status and the client handed back no data for it.
	ErrEmptyDirectorAnswer = errors.New("the BOSH director answered with no data")
)

// directorAuth is how the boshapi client authenticates to the director.
type directorAuth struct {
	clientID     string
	clientSecret string
}

// resolveDirectorAuth picks the credentials the boshapi client uses, and it
// refuses when there are none. It does no network setup, so a caller checks
// the credentials before it builds anything that dials the director.
//
// The credentials are bosh.username and bosh.password. To a director that
// uses UAA they are the UAA client ID and secret, and to one that uses HTTP
// Basic they are the user name and password. A value made only of
// whitespace counts as missing, as it does for the address and the CA. The
// credentials are passed on as they were configured.
func resolveDirectorAuth(config Config) (directorAuth, error) {
	auth := directorAuth{clientID: config.Username, clientSecret: config.Password}

	if strings.TrimSpace(auth.clientID) == "" || strings.TrimSpace(auth.clientSecret) == "" {
		return directorAuth{}, fmt.Errorf("%w%s. Set bosh.username and bosh.password to the director's UAA client ID and secret, "+
			"or to its user name and password when it uses HTTP Basic, and check that neither is empty or only whitespace",
			ErrDirectorCredentialsMissing, forDirector(config.Address))
	}

	return auth, nil
}

// forDirector names the director at address for an error message, or
// returns nothing when no address is configured, so a message never reads
// "for the director at ." with the address missing.
func forDirector(address string) string {
	if strings.TrimSpace(address) == "" {
		return ""
	}

	return " for the director at " + address
}

// newAPIClient builds the boshapi client the adapter reads and writes the
// director through. It sends no request. The client authenticates with the
// credentials Blacksmith was configured with, and it asks the director how
// at the first authenticated request, so a director that uses UAA and one
// that uses HTTP Basic both work. A director that uses UAA must name it in
// its GET /info, and the token comes from there. The client never skips TLS
// verification, and it trusts config.CACert for the director and for UAA.
//
// config.RequestTimeout bounds each attempt. Factory.New sets it to 60
// seconds when it is zero, which is the one place that default lives. An
// adapter built without the factory leaves it zero, and boshapi then applies
// its own default.
func newAPIClient(config Config, auth directorAuth, log Logger) (boshapi.Client, error) {
	client, err := boshclient.New(context.Background(), &boshapi.Config{
		DirectorURL:  config.Address,
		ClientID:     auth.clientID,
		ClientSecret: auth.clientSecret,
		Auth:         boshapi.AuthDiscover,
		CACert:       config.CACert,
		HTTPTimeout:  config.RequestTimeout,
		RetryMax:     config.MaxRetries,
		Logger:       apiLogger{log: log},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create the boshapi client for the director at %s: %w", config.Address, err)
	}

	return client, nil
}

// apiLogger adapts Blacksmith's printf-style Logger to boshapi's structured
// one.
type apiLogger struct {
	log Logger
}

func (l apiLogger) Debug(msg string, fields map[string]interface{}) {
	l.log.Debugf("boshapi: %s%s", msg, formatLogFields(fields))
}

func (l apiLogger) Info(msg string, fields map[string]interface{}) {
	l.log.Infof("boshapi: %s%s", msg, formatLogFields(fields))
}

// Warn logs at the error level, the nearest level Blacksmith's Logger has.
func (l apiLogger) Warn(msg string, fields map[string]interface{}) {
	l.log.Errorf("boshapi: %s%s", msg, formatLogFields(fields))
}

func (l apiLogger) Error(msg string, fields map[string]interface{}) {
	l.log.Errorf("boshapi: %s%s", msg, formatLogFields(fields))
}

// formatLogFields renders fields as " key=value" pairs in key order, so a log
// line is the same every time.
func formatLogFields(fields map[string]interface{}) string {
	if len(fields) == 0 {
		return ""
	}

	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}

	sort.Strings(keys)

	var out strings.Builder

	for _, key := range keys {
		fmt.Fprintf(&out, " %s=%v", key, fields[key])
	}

	return out.String()
}

// requestTimeoutError is the Cause of a DirectorCallError for a request that
// got no answer within the configured request timeout, while the caller's own
// context was still live. The timeout bounds each attempt, and boshapi retries
// a read, so attempts says how many attempts each had that long to answer.
type requestTimeoutError struct {
	timeout  time.Duration
	attempts int
}

func (e *requestTimeoutError) Error() string {
	msg := "no answer within the "
	if e.timeout > 0 {
		msg += formatTimeout(e.timeout) + " "
	}

	msg += "bosh.request_timeout"

	if e.attempts > 1 {
		msg += fmt.Sprintf(" on each of %d attempts", e.attempts)
	}

	return msg
}

// formatTimeout writes a whole number of seconds as "60s", which is how the
// setting is written, and anything finer the way time.Duration writes it.
func formatTimeout(timeout time.Duration) string {
	if timeout > 0 && timeout%time.Second == 0 {
		return fmt.Sprintf("%ds", int64(timeout/time.Second))
	}

	return timeout.String()
}

// directorRequestError is a failed director request with what Blacksmith was
// doing, the director's address, and advice for the director answers that
// carry none of their own. boshapi's transport and authentication errors
// already say what to check, so they pass through with only the context
// added.
type directorRequestError struct {
	what     string
	director string
	err      error
}

func (e *directorRequestError) Error() string {
	msg := fmt.Sprintf("failed to %s on BOSH director %s: %v", e.what, e.director, e.err)

	advice := directorAnswerAdvice(e.err)
	if advice == "" {
		return msg
	}

	// The advice starts a sentence of its own, and the error before it may
	// already end one.
	return strings.TrimSuffix(msg, ".") + ". " + advice
}

func (e *directorRequestError) Unwrap() error {
	return e.err
}

// directorAnswerAdvice names the likely causes of an error answer from the
// director, and what to check. It returns "" for an error that already
// explains itself, such as a failed authentication, and for one that is not
// an answer from the director.
func directorAnswerAdvice(err error) string {
	var (
		authErr      *boshapi.AuthError
		transportErr *boshapi.TransportError
		apiErr       *boshapi.APIError
	)

	if errors.As(err, &authErr) || errors.As(err, &transportErr) || !errors.As(err, &apiErr) {
		return ""
	}

	switch {
	case boshapi.IsUnauthorized(err):
		return "The director did not accept Blacksmith's credentials. The likely causes are a wrong or rotated " +
			"bosh.username and bosh.password, or a UAA client that was deleted. Check that the UAA client still " +
			"exists with that secret, and that UAA is reachable from the director"
	case boshapi.IsForbidden(err):
		return "The director refused the request for this UAA client. The likely cause is a client without the " +
			"authority to see this resource. Check that the client has the bosh.admin authority"
	case boshapi.IsRouteNotFound(err):
		return "The director does not serve that route. The likely causes are a bosh.address that points at a " +
			"proxy or another service, or a director older than the route. Check bosh.address and the director's version"
	case boshapi.IsNotFound(err):
		return "The director has no record of it. Check the name, and that it was not deleted"
	case apiErr.StatusCode >= http.StatusInternalServerError:
		return "The director reported an internal error. The likely causes are a director that is overloaded, " +
			"restarting, or failing its own database or workers. Check monit summary and the director's logs on the director VM"
	default:
		return ""
	}
}

// requestFailure wraps a failed read with what Blacksmith was doing and where.
func (d *DirectorAdapter) requestFailure(what string, err error) error {
	return &directorRequestError{what: what, director: d.address, err: err}
}
