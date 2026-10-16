package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"blacksmith/internal/api"
	"blacksmith/internal/bosh"
)

// releasesOnlyDirector answers the director's release listing and panics,
// through the nil embedded Director, on any other call.
type releasesOnlyDirector struct {
	bosh.Director
}

func (d *releasesOnlyDirector) GetReleases(_ context.Context) ([]bosh.Release, error) {
	return []bosh.Release{{Name: "redis-forge", ReleaseVersions: []bosh.ReleaseVersion{{Version: "9.9.9"}}}}, nil
}

func TestReleasesRouteServesTheDirectorsReleases(t *testing.T) {
	t.Parallel()

	deps := createTestDependencies(t)
	deps.Director = &releasesOnlyDirector{}
	apiHandler := api.NewInternalAPI(deps)

	responseWriter := httptest.NewRecorder()
	apiHandler.ServeHTTP(responseWriter, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/b/bosh/releases", nil))
	validateResponse(t, responseWriter, http.StatusOK)

	var releases []bosh.Release

	err := json.Unmarshal(responseWriter.Body.Bytes(), &releases)
	if err != nil {
		t.Fatalf("decoding the releases: %v", err)
	}

	if len(releases) != 1 || releases[0].Name != "redis-forge" {
		t.Fatalf("expected the director's redis-forge release, got %+v", releases)
	}
}

// deploymentsOnlyDirector answers the director's deployment listing and
// panics, through the nil embedded Director, on any other call.
type deploymentsOnlyDirector struct {
	bosh.Director
}

func (d *deploymentsOnlyDirector) GetDeployments(_ context.Context) ([]bosh.Deployment, error) {
	return []bosh.Deployment{{Name: "redis-1", Releases: []string{"os-conf/22.1.2", "redis-forge/9.9.8"}}}, nil
}

func TestDeploymentReleasesRouteServesTheManagedReleases(t *testing.T) {
	t.Parallel()

	deps := createTestDependencies(t)
	deps.Director = &deploymentsOnlyDirector{}
	apiHandler := api.NewInternalAPI(deps)

	responseWriter := httptest.NewRecorder()
	apiHandler.ServeHTTP(responseWriter, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/b/bosh/deployment-releases", nil))
	validateResponse(t, responseWriter, http.StatusOK)

	var releases map[string][]string

	err := json.Unmarshal(responseWriter.Body.Bytes(), &releases)
	if err != nil {
		t.Fatalf("decoding the deployment releases: %v", err)
	}

	if got := releases["redis-1"]; len(got) != 1 || got[0] != "redis-forge/9.9.8" {
		t.Fatalf("expected redis-1's forge release alone, got %v", releases)
	}
}
