package credhub

import (
	"context"
	"errors"
	"net/http"

	"blacksmith/internal/config"
	"blacksmith/pkg/logger"
)

// CapabilityState says whether Blacksmith can use the director's CredHub.
type CapabilityState string

// The states a CredHub capability can be in.
const (
	// StateConfigured means CredHub answered the probe.
	StateConfigured CapabilityState = "configured"
	// StateNotConfigured means the broker has no CredHub connection.
	StateNotConfigured CapabilityState = "not_configured"
	// StateUnreachable means the probe failed for a reason other than a
	// refusal, such as a timeout or a server error.
	StateUnreachable CapabilityState = "unreachable"
	// StateRefused means CredHub or its UAA answered 401 or 403.
	StateRefused CapabilityState = "refused"
)

// ClassifyProbe maps the result of a probe to a capability state. A nil
// error is configured, a 401 or 403 answer is refused, and every other
// error is unreachable.
func ClassifyProbe(err error) CapabilityState {
	if err == nil {
		return StateConfigured
	}

	var apiErr *APIError
	if errors.As(err, &apiErr) && (apiErr.Status == http.StatusUnauthorized || apiErr.Status == http.StatusForbidden) {
		return StateRefused
	}

	return StateUnreachable
}

// BuildClient builds the CredHub client from the credhub configuration,
// whether or not cleanup is enabled. It returns no client and no warnings
// when the block has no connection at all, and no client and every problem
// it found when the connection is incomplete or invalid, or when the UAA
// URL cannot be found. The UAA URL is credhub.uaa_url, or else the one the
// director reports in /info, and the UAA is trusted through boshCACert.
// BuildClient makes at most one /info call and never calls UAA or CredHub,
// so it cannot hold up startup. No problem ever carries the client secret.
func BuildClient(ctx context.Context, cfg config.CredHubConfig, boshCACert string, director directorProber, log logger.Logger) (*Client, []string) {
	if !cfg.Configured() {
		return nil, nil
	}

	// The connection rules are the ones cleanup applies. The sweep setting
	// belongs to cleanup alone, so it is left out.
	connection := cfg
	connection.Cleanup = config.CredHubCleanupConfig{Enabled: true}

	problems := connection.Validate()
	if len(problems) > 0 {
		return nil, problems
	}

	if director == nil && cfg.UAAURL == "" {
		return nil, []string{"credhub.uaa_url is empty and no BOSH director is configured to report the UAA, so the UAA that issues CredHub tokens is not known. Set credhub.uaa_url or configure the director"}
	}

	tokens, infoName, problem := buildTokenSource(ctx, cfg, boshCACert, director, log)
	if problem != "" {
		return nil, []string{problem}
	}

	protected := ProtectedPrefixes(Policy{
		DirectorName:         cfg.DirectorName,
		InfoDirectorName:     infoName,
		ProtectedDeployments: cfg.Cleanup.ProtectedDeployments,
	})

	client, err := NewClient(ClientConfig{URL: cfg.URL, CACert: cfg.CACert}, tokens, protected)
	if err != nil {
		return nil, []string{"the CredHub client for " + redactURL(cfg.URL) + " could not be built (" + err.Error() + ")"}
	}

	return client, nil
}
