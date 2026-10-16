package bosh_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"blacksmith/internal/bosh"
	boshhandler "blacksmith/internal/handlers/bosh"
	"blacksmith/pkg/logger"
)

const (
	forgeRelease = "redis-forge"
	bpmRelease   = "bpm"
)

// requestKey marks the context of a test request, so a test can tell that
// a director call ran under it.
type requestKey struct{}

var errDirectorInTest = errors.New("the director is unreachable")

// releaseDirector is a director whose release and deployment listings
// answer with the values or the error the test sets, and which records
// whether each listing ran under the request's context. A call it does not
// script panics through the nil embedded Director.
type releaseDirector struct {
	bosh.Director

	releases    []bosh.Release
	deployments []bosh.Deployment
	err         error

	underRequest []bool
}

func (d *releaseDirector) GetReleases(ctx context.Context) ([]bosh.Release, error) {
	d.underRequest = append(d.underRequest, ctx.Value(requestKey{}) != nil)

	return d.releases, d.err
}

func (d *releaseDirector) GetDeployments(ctx context.Context) ([]bosh.Deployment, error) {
	d.underRequest = append(d.underRequest, ctx.Value(requestKey{}) != nil)

	return d.deployments, d.err
}

// get sends a GET of the path to the handler function under a request
// context, and returns the answer.
func get(t *testing.T, serve http.HandlerFunc, path string) *httptest.ResponseRecorder {
	t.Helper()

	ctx := context.WithValue(t.Context(), requestKey{}, true)
	recorder := httptest.NewRecorder()

	serve(recorder, httptest.NewRequestWithContext(ctx, http.MethodGet, path, nil))

	return recorder
}

// versions returns the versions of the release, in order.
func versions(release bosh.Release) []string {
	names := make([]string, len(release.ReleaseVersions))
	for i, version := range release.ReleaseVersions {
		names[i] = version.Version
	}

	return names
}

func TestGetReleasesGroupsEachReleaseNewestFirst(t *testing.T) {
	t.Parallel()

	director := &releaseDirector{releases: []bosh.Release{
		{Name: forgeRelease, ReleaseVersions: []bosh.ReleaseVersion{{Version: "1.9.0"}, {Version: "1.10.0"}, {Version: "1.4.3"}}},
		{Name: bpmRelease, ReleaseVersions: []bosh.ReleaseVersion{{Version: "1.4.20"}, {Version: "1.4.3"}}},
		{Name: forgeRelease, ReleaseVersions: []bosh.ReleaseVersion{{Version: "1.10.1"}}},
	}}
	handler := boshhandler.NewHandler(boshhandler.Dependencies{Logger: &logger.NoOpLogger{}, Director: director})

	recorder := get(t, handler.GetReleases, "/b/bosh/releases")
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", recorder.Code, recorder.Body.String())
	}

	var releases []bosh.Release

	err := json.Unmarshal(recorder.Body.Bytes(), &releases)
	if err != nil {
		t.Fatalf("decoding the releases: %v", err)
	}

	if len(releases) != 2 || releases[0].Name != forgeRelease || releases[1].Name != bpmRelease {
		t.Fatalf("expected redis-forge and then bpm, got %+v", releases)
	}

	if got := strings.Join(versions(releases[0]), ","); got != "1.10.1,1.10.0,1.9.0,1.4.3" {
		t.Fatalf("expected redis-forge's versions newest first, got %s", got)
	}

	if got := strings.Join(versions(releases[1]), ","); got != "1.4.20,1.4.3" {
		t.Fatalf("expected bpm's versions newest first, got %s", got)
	}

	if len(director.underRequest) != 1 || !director.underRequest[0] {
		t.Fatalf("expected one release listing under the request's context, got %v", director.underRequest)
	}
}

func TestGetReleasesOrdersVersionsWithSuffixesByTheirNumbers(t *testing.T) {
	t.Parallel()

	director := &releaseDirector{releases: []bosh.Release{{Name: forgeRelease, ReleaseVersions: []bosh.ReleaseVersion{
		{Version: "1a"}, {Version: "9"}, {Version: "1.10"}, {Version: "10"}, {Version: "1b"},
		{Version: "2"}, {Version: "9+dev.1"}, {Version: "1"},
	}}}}
	handler := boshhandler.NewHandler(boshhandler.Dependencies{Logger: &logger.NoOpLogger{}, Director: director})

	recorder := get(t, handler.GetReleases, "/b/bosh/releases")
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", recorder.Code, recorder.Body.String())
	}

	var releases []bosh.Release

	err := json.Unmarshal(recorder.Body.Bytes(), &releases)
	if err != nil {
		t.Fatalf("decoding the releases: %v", err)
	}

	if len(releases) != 1 {
		t.Fatalf("expected one release, got %+v", releases)
	}

	if got := strings.Join(versions(releases[0]), ","); got != "10,9+dev.1,9,2,1b,1a,1.10,1" {
		t.Fatalf("expected each part ordered by its number and then its suffix, got %s", got)
	}
}

func TestGetReleasesReportsADirectorError(t *testing.T) {
	t.Parallel()

	director := &releaseDirector{err: errDirectorInTest}
	handler := boshhandler.NewHandler(boshhandler.Dependencies{Logger: &logger.NoOpLogger{}, Director: director})

	recorder := get(t, handler.GetReleases, "/b/bosh/releases")
	if recorder.Code != http.StatusInternalServerError || !strings.Contains(recorder.Body.String(), errDirectorInTest.Error()) {
		t.Fatalf("expected status 500 naming the director's error, got %d: %s", recorder.Code, recorder.Body.String())
	}
}
