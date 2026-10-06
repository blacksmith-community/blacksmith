package boshapi

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/fivetwenty-io/boshapi/internal/redact"
)

// AuthStage names the step of authentication that failed.
type AuthStage string

const (
	// AuthStageDiscovery is the read of the director's GET /info that finds
	// out how the director authenticates.
	AuthStageDiscovery AuthStage = "discovery"
	// AuthStageToken is the UAA token request.
	AuthStageToken AuthStage = "token"
)

// oauthInvalidClient is the OAuth error code UAA answers when it does not
// accept the client ID and secret.
const oauthInvalidClient = "invalid_client"

// AuthError reports that a request could not be authenticated, so it was
// never sent to the director. It never holds the client secret or a token.
// It unwraps to Err.
type AuthError struct {
	// Stage is the step that failed: discovery or token.
	Stage AuthStage
	// Director is the director URL.
	Director string
	// TokenURL is the UAA token endpoint. It is empty when discovery failed
	// before it was known.
	TokenURL string
	// StatusCode is the HTTP status UAA answered a token request with, or 0
	// when no answer came. The director's answer to a failed discovery is
	// in Err, as an *APIError.
	StatusCode int
	// Code is UAA's OAuth error code, such as invalid_client, when its
	// answer carried one.
	Code string
	// Description is UAA's error_description, or its answer cut to 1 KiB
	// when that was not OAuth's JSON error shape. For discovery, it says
	// what the director's GET /info answer lacked.
	Description string
	// Err is the underlying error, such as a *TransportError, an *APIError,
	// a network error, or the caller's context error.
	Err error
}

// Error implements the error interface.
func (e *AuthError) Error() string {
	var msg strings.Builder

	if e.Stage == AuthStageDiscovery {
		fmt.Fprintf(&msg, "could not read how BOSH director %s authenticates from GET /info", e.Director)
	} else {
		fmt.Fprintf(&msg, "could not get a UAA token for BOSH director %s from %s", e.Director, redact.URL(e.TokenURL))
	}

	if e.StatusCode != 0 {
		fmt.Fprintf(&msg, ": UAA answered HTTP %d", e.StatusCode)
	}

	if e.Code != "" {
		fmt.Fprintf(&msg, " with %s", e.Code)
	}

	if e.Description != "" {
		fmt.Fprintf(&msg, ": %s", e.Description)
	}

	if e.Err != nil {
		fmt.Fprintf(&msg, ": %v", e.Err)
	}

	msg.WriteString(". ")
	msg.WriteString(e.advice())

	return msg.String()
}

// Unwrap returns the underlying error.
func (e *AuthError) Unwrap() error {
	return e.Err
}

// advice names the likely causes of the failure and what to check.
func (e *AuthError) advice() string {
	switch {
	case e.StatusCode == 0 && (errors.Is(e.Err, context.Canceled) || errors.Is(e.Err, context.DeadlineExceeded)):
		return "The caller's context ended before authentication finished, so the request itself was never " +
			"sent to the director. Try again with a longer deadline."
	case e.Stage == AuthStageDiscovery && e.Description != "":
		return "Check the director's user_authentication settings, or the auth mode in Config.Auth."
	case e.Stage == AuthStageDiscovery:
		return "Check that the director URL is right, that the director is running and reachable from here, " +
			"and that CACert holds the CA that signed the director's certificate."
	case e.Code == oauthInvalidClient || e.StatusCode == http.StatusUnauthorized:
		return "UAA did not accept the client credentials. Check that ClientID names a UAA client and that " +
			"ClientSecret is its current secret."
	case e.StatusCode >= http.StatusInternalServerError:
		return "UAA itself is failing, so the client credentials are probably fine. Check the health and logs " +
			"of UAA at " + redact.URL(e.TokenURL) + ", and of any proxy in front of it, then try again."
	case isCertificateError(e.Err):
		return "This client does not trust UAA's certificate. Add the CA that signed it to CACert, " +
			"alongside the director's CA."
	default:
		return "The likely causes are a wrong client ID or secret (UAA answers invalid_client), a UAA " +
			"certificate that the CA in CACert does not trust, or a UAA that is down or unreachable from here. " +
			"Check the client credentials, CACert, and that " + redact.URL(e.TokenURL) + " answers."
	}
}

// isCertificateError reports a TLS certificate that failed verification.
func isCertificateError(err error) bool {
	var (
		verifyErr    *tls.CertificateVerificationError
		authorityErr x509.UnknownAuthorityError
		hostnameErr  x509.HostnameError
		invalidErr   x509.CertificateInvalidError
	)

	return errors.As(err, &verifyErr) || errors.As(err, &authorityErr) ||
		errors.As(err, &hostnameErr) || errors.As(err, &invalidErr)
}
