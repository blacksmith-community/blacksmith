package credhub

import (
	"context"
	"time"

	"blacksmith/internal/config"
	"blacksmith/pkg/logger"
)

const (
	// ProbeTimeout bounds the startup probe.
	ProbeTimeout = 30 * time.Second
	// probeDeployment names a deployment path that never exists, so the probe
	// lists nothing and touches nothing.
	probeDeployment = "blacksmith-credhub-probe"
)

// BuildCleaner builds the cleaner and its CredHub client from the credhub
// configuration. It returns no cleaner and every problem it found when the
// configuration is invalid, cleanup is disabled, or the UAA URL cannot be
// found. The UAA URL is credhub.uaa_url, or else the one the director reports
// in /info, and the UAA is trusted through boshCACert. BuildCleaner makes at
// most one /info call and never calls UAA or CredHub, so it cannot hold up
// startup. plans is read on every cleanup run, so the catalog's plan IDs are
// always current. No problem ever carries the client secret.
func BuildCleaner(cfg config.CredHubConfig, boshCACert string, director directorProber, plans func() []string, log logger.Logger) (*Cleaner, *Client, []string) {
	if !cfg.Cleanup.Enabled {
		return nil, nil, []string{"credhub.cleanup.enabled is false, so there is no cleaner to build"}
	}

	problems := cfg.Validate()

	if director == nil {
		problems = append(problems, "no BOSH director is configured, and the cleaner needs one to prove a deployment is gone")
	}

	if plans == nil {
		problems = append(problems, "no service catalog was given, and the cleaner needs its plan IDs to recognise Blacksmith deployments")
	}

	if len(problems) > 0 {
		return nil, nil, problems
	}

	uaaURL, infoName, problem := findUAAURL(cfg, director)
	if problem != "" {
		return nil, nil, []string{problem}
	}

	tokens, err := NewTokenSource(uaaURL, cfg.ClientID, cfg.ClientSecret, boshCACert, nil)
	if err != nil {
		return nil, nil, []string{"the UAA token source for " + uaaURL + " could not be built (" + err.Error() + "). Check bosh.cacert, which is the trust anchor for the director's UAA"}
	}

	protected := ProtectedPrefixes(Policy{
		DirectorName:         cfg.DirectorName,
		InfoDirectorName:     infoName,
		ProtectedDeployments: cfg.Cleanup.ProtectedDeployments,
	})

	client, err := NewClient(ClientConfig{URL: cfg.URL, CACert: cfg.CACert}, tokens, protected)
	if err != nil {
		return nil, nil, []string{"the CredHub client for " + cfg.URL + " could not be built (" + err.Error() + ")"}
	}

	policy := func() Policy {
		return Policy{
			DirectorName:         cfg.DirectorName,
			PlanIDs:              plans(),
			ProtectedDeployments: cfg.Cleanup.ProtectedDeployments,
		}
	}

	return NewCleaner(client, director, policy, log), client, nil
}

// findUAAURL returns credhub.uaa_url when it is set and otherwise asks the
// director's /info, returning the director's reported name as well. A
// non-empty problem says why no URL was found.
func findUAAURL(cfg config.CredHubConfig, director directorProber) (string, string, string) {
	if cfg.UAAURL != "" {
		return cfg.UAAURL, "", ""
	}

	info, err := director.GetInfo()
	if err != nil {
		return "", "", "credhub.uaa_url is empty and the director's /info failed (" + err.Error() + "), so the UAA that issues CredHub tokens is unknown. Set credhub.uaa_url or check that the director is reachable"
	}

	if info == nil || info.UAAURL == "" {
		return "", "", "credhub.uaa_url is empty and the director's /info names no UAA, which happens when the director does not use UAA authentication. Set credhub.uaa_url to the UAA that CredHub trusts"
	}

	return info.UAAURL, info.Name, ""
}

// Probe lists /<director>/blacksmith-credhub-probe/, a path that never
// exists, under a 30-second deadline, and logs whether CredHub and its UAA
// accepted the client. A failed probe leaves cleanup enabled, because every
// cleanup run tries again and logs its own failure.
func Probe(ctx context.Context, client *Client, directorName string, log logger.Logger) error {
	return probeWithin(ctx, client, directorName, log, ProbeTimeout)
}

func probeWithin(ctx context.Context, client *Client, directorName string, log logger.Logger, timeout time.Duration) error {
	path := "/" + directorName + "/" + probeDeployment + "/"

	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	_, err := client.FindByPath(probeCtx, path)
	if err != nil {
		causes, check := explainFailure(opFind, err)
		log.Errorf("CredHub cleanup is enabled for director %s, but the startup probe, a find under %s, failed. The error was %v. The likely cause is that %s. To investigate, check %s. Cleanup stays enabled, and each cleanup tries again and logs its own failure.",
			directorName, path, err, causes, check)

		return err
	}

	log.Infof("CredHub cleanup enabled for director %s, probe succeeded", directorName)

	return nil
}
