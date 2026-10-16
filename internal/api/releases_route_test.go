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
