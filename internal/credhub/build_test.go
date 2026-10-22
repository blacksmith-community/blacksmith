package credhub_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"blacksmith/internal/config"
	"blacksmith/internal/credhub"
)

func TestBuildClientWithoutCleanup(t *testing.T) {
	t.Parallel()

	cfg := validWireConfig(t)
	cfg.Cleanup.Enabled = false
	cfg.UAAURL = wireUAAURL
	director := newWireDirector(wireUAAURL)

	client, problems := credhub.BuildClient(context.Background(), cfg, unrelatedCA(t), director, newCaptureLogger())
	if client == nil || len(problems) != 0 {
		t.Fatalf("expected a client and no problems, got %v and %q", client, problems)
	}

	if director.infoCalls.Load() != 0 {
		t.Fatalf("expected no /info call when uaa_url is set, got %d", director.infoCalls.Load())
	}
}

func TestBuildClientReadsTheUAAFromInfo(t *testing.T) {
	t.Parallel()

	cfg := validWireConfig(t)
	cfg.Cleanup.Enabled = false
	director := newWireDirector(wireUAAURL)

	client, problems := credhub.BuildClient(context.Background(), cfg, unrelatedCA(t), director, newCaptureLogger())
	if client == nil || len(problems) != 0 {
		t.Fatalf("expected a client and no problems, got %v and %q", client, problems)
	}

	if director.infoCalls.Load() != 1 {
		t.Fatalf("expected one /info call, got %d", director.infoCalls.Load())
	}
}

func TestBuildClientEmptyBlockBuildsNothing(t *testing.T) {
	t.Parallel()

	client, problems := credhub.BuildClient(context.Background(), config.CredHubConfig{}, "", nil, newCaptureLogger())
	if client != nil || len(problems) != 0 {
		t.Fatalf("expected nil and no warnings, got %v and %q", client, problems)
	}
}

func TestBuildClientListsProblemsOfAPartialBlock(t *testing.T) {
	t.Parallel()

	cfg := config.CredHubConfig{}
	cfg.URL = wireCredHubURL

	client, problems := credhub.BuildClient(context.Background(), cfg, "", nil, newCaptureLogger())
	if client != nil {
		t.Fatalf("expected no client, got %v", client)
	}

	joined := strings.Join(problems, "\n")
	for _, want := range []string{"credhub.ca_cert is required", "credhub.client_secret is required", "credhub.director_name is required"} {
		if !strings.Contains(joined, want) {
			t.Errorf("problems do not mention %q: %s", want, joined)
		}
	}
}

func TestBuildClientNeedsADirectorWhenTheUAAIsUnset(t *testing.T) {
	t.Parallel()

	cfg := validWireConfig(t)
	cfg.Cleanup.Enabled = false

	client, problems := credhub.BuildClient(context.Background(), cfg, unrelatedCA(t), nil, newCaptureLogger())
	if client != nil || len(problems) == 0 {
		t.Fatalf("expected no client and a problem, got %v and %q", client, problems)
	}
}

func TestClassifyProbe(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		err  error
		want credhub.CapabilityState
	}{
		"success":      {nil, credhub.StateConfigured},
		"forbidden":    {&credhub.APIError{Op: "find", Status: http.StatusForbidden}, credhub.StateRefused},
		"unauthorized": {&credhub.APIError{Op: "find", Status: http.StatusUnauthorized}, credhub.StateRefused},
		"wrapped 403":  {errors.Join(errors.New("probe"), &credhub.APIError{Status: http.StatusForbidden}), credhub.StateRefused},
		"server error": {&credhub.APIError{Op: "find", Status: http.StatusInternalServerError}, credhub.StateUnreachable},
		"deadline":     {context.DeadlineExceeded, credhub.StateUnreachable},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if got := credhub.ClassifyProbe(test.err); got != test.want {
				t.Fatalf("ClassifyProbe(%v) = %q, want %q", test.err, got, test.want)
			}
		})
	}

	if credhub.StateNotConfigured != "not_configured" || credhub.StateConfigured != "configured" ||
		credhub.StateUnreachable != "unreachable" || credhub.StateRefused != "refused" {
		t.Fatal("capability state values changed")
	}
}
