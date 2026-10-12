package bosh_test

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/bosh/boshtest"
	"github.com/fivetwenty-io/boshapi/pkg/boshapi"
)

// newReadAdapter builds an adapter against a test director with the
// credentials every test here shares.
func newReadAdapter(t *testing.T, server *boshtest.Director, mutate func(*bosh.Config)) bosh.Director {
	t.Helper()

	config := bosh.Config{
		Address:  server.URL,
		Username: basicAuthCredential,
		Password: basicAuthCredential,
		CACert:   server.CACert,
	}

	if mutate != nil {
		mutate(&config)
	}

	director, err := bosh.NewDirectorAdapter(config)
	if err != nil {
		t.Fatalf("failed to create director adapter: %v", err)
	}

	return director
}

// The director's JSON "deployment not found" answer, code 70000, is the only
// answer that means a deployment does not exist. The error keeps the
// director's own answer so a caller can still see its description.
func TestGetDeploymentMapsCode70000ToErrDeploymentNotFound(t *testing.T) {
	server := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
		if request.URL.Path != "/deployments/gone" {
			return false
		}

		boshtest.WriteError(writer, http.StatusNotFound, boshtest.CodeDeploymentNotFound, "Deployment 'gone' is not known to this director")

		return true
	})

	director := newReadAdapter(t, server, nil)

	_, err := director.GetDeployment(t.Context(), "gone")
	if !errors.Is(err, bosh.ErrDeploymentNotFound) {
		t.Fatalf("expected ErrDeploymentNotFound for code 70000, got: %v", err)
	}

	var apiErr *boshapi.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != boshtest.CodeDeploymentNotFound || !strings.Contains(apiErr.Description, "is not known to this director") {
		t.Fatalf("expected the error to keep the director's code 70000 answer, got: %v", err)
	}
}

// A 404 that says an endpoint was not found is a routing problem, such as a
// proxy in front of the director. It must never read as a missing deployment,
// because callers delete credentials and index entries for a missing one.
func TestGetDeploymentKeepsARoute404AsAnError(t *testing.T) {
	server := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
		if request.URL.Path != "/deployments/behind-proxy" {
			return false
		}

		boshtest.WriteRouteNotFound(writer, request)

		return true
	})

	director := newReadAdapter(t, server, nil)

	_, err := director.GetDeployment(t.Context(), "behind-proxy")
	if err == nil {
		t.Fatal("expected an error for a route 404")
	}

	if errors.Is(err, bosh.ErrDeploymentNotFound) {
		t.Fatalf("a route 404 must not read as ErrDeploymentNotFound, got: %v", err)
	}

	message := err.Error()
	for _, want := range []string{server.URL, "behind-proxy", "404", "Endpoint '/deployments/behind-proxy' not found", "bosh.address"} {
		if !strings.Contains(message, want) {
			t.Errorf("expected the error to mention %q, got: %s", want, message)
		}
	}
}

// A 404 with another of the director's "not found" codes is not a missing
// deployment either. Code 20000 is a missing user and 10000 is a missing
// task, and a caller that deletes credentials for a missing deployment
// must not act on them.
func TestGetDeploymentKeepsOtherNotFoundCodesAsErrors(t *testing.T) {
	for _, code := range []int{boshtest.CodeDirectorNotFound, boshtest.CodeTaskNotFound} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			server := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
				if request.URL.Path != "/deployments/other" {
					return false
				}

				boshtest.WriteError(writer, http.StatusNotFound, code, "Something else is not known to this director")

				return true
			})

			director := newReadAdapter(t, server, nil)

			_, err := director.GetDeployment(t.Context(), "other")
			if err == nil {
				t.Fatalf("expected an error for a 404 with code %d", code)
			}

			if errors.Is(err, bosh.ErrDeploymentNotFound) {
				t.Fatalf("a 404 with code %d must not read as ErrDeploymentNotFound, got: %v", code, err)
			}

			var apiErr *boshapi.APIError
			if !errors.As(err, &apiErr) || apiErr.Code != code {
				t.Errorf("expected the error to keep the director's code %d answer, got: %v", code, err)
			}
		})
	}
}

// A director that answers more slowly than bosh.request_timeout gets an error
// that names the setting and its value, so an operator knows what bounded the
// call and what to change. The timeout bounds each attempt, and a read is
// retried, so when more than one attempt ran the cause says how many.
func TestRequestTimeoutIsExplained(t *testing.T) {
	tests := []struct {
		name       string
		timeout    time.Duration
		maxRetries int
		want       string
	}{
		{name: "whole seconds read as seconds", timeout: time.Second, want: "no answer within the 1s bosh.request_timeout"},
		{name: "finer timeouts keep their unit", timeout: 150 * time.Millisecond, want: "no answer within the 150ms bosh.request_timeout"},
		{
			name: "retried reads say how many attempts timed out", timeout: 150 * time.Millisecond, maxRetries: 2,
			want: "no answer within the 150ms bosh.request_timeout on each of 3 attempts",
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			release := make(chan struct{})

			server := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
				if request.URL.Path != "/deployments" {
					return false
				}

				select {
				case <-request.Context().Done():
				case <-release:
				}

				return true
			})
			t.Cleanup(func() { close(release) })

			director := newReadAdapter(t, server, func(config *bosh.Config) {
				config.RequestTimeout = testCase.timeout
				config.MaxRetries = testCase.maxRetries
			})

			_, err := director.GetDeployments(t.Context())

			var callErr *bosh.DirectorCallError
			if !errors.As(err, &callErr) {
				t.Fatalf("expected a *bosh.DirectorCallError, got %T: %v", err, err)
			}

			if callErr.Operation != "GetDeployments" || callErr.Director != server.URL {
				t.Errorf("DirectorCallError names operation %q and director %q, want GetDeployments and %s", callErr.Operation, callErr.Director, server.URL)
			}

			if callErr.Cause == nil || callErr.Cause.Error() != testCase.want {
				t.Errorf("Cause = %v, want %q", callErr.Cause, testCase.want)
			}

			if !strings.Contains(err.Error(), testCase.want) {
				t.Errorf("the message does not carry the cause %q: %s", testCase.want, err.Error())
			}

			var transportErr *boshapi.TransportError
			if !errors.As(err, &transportErr) || !transportErr.Timeout {
				t.Errorf("expected the error to keep the TransportError that timed out, got: %v", err)
			}
		})
	}
}

// The director's list answer carries each deployment's releases, stemcells,
// and teams, so listing deployments takes one request and none per
// deployment.
func TestGetDeploymentsReadsReleasesFromTheList(t *testing.T) {
	server := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
		if request.URL.Path != "/deployments" {
			return false
		}

		boshtest.WriteJSON(writer, http.StatusOK, []map[string]interface{}{
			{
				"name":         "redis-1",
				"cloud_config": "latest",
				"releases":     []map[string]string{{"name": "redis-forge", "version": "1.4.3"}, {"name": "bpm", "version": "1.4.26"}},
				"stemcells":    []map[string]string{{"name": "bosh-lite-ubuntu-noble", "version": "1.25"}},
				"teams":        []string{"blacksmith"},
			},
			{
				"name":         "rabbit-1",
				"cloud_config": "outdated",
				"releases":     []map[string]string{{"name": "rabbitmq-forge", "version": "2.0.0"}},
				"stemcells":    []map[string]string{},
				"teams":        []string{},
			},
			{
				// A version that is not a semantic version still belongs in
				// the list, as the director reports it.
				"name":         "dev-1",
				"cloud_config": "none",
				"releases":     []map[string]string{{"name": "dev-release", "version": "unparseable-version"}},
				"stemcells":    []map[string]string{{"name": "dev-stemcell", "version": "also unparseable"}},
				"teams":        []string{},
			},
		})

		return true
	})

	director := newReadAdapter(t, server, nil)

	deployments, err := director.GetDeployments(t.Context())
	if err != nil {
		t.Fatalf("GetDeployments: %v", err)
	}

	requests := server.APIRequests()
	if len(requests) != 1 || requests[0].Method != http.MethodGet || requests[0].Path != "/deployments" {
		t.Fatalf("expected exactly one GET /deployments, got %+v", requests)
	}

	if len(deployments) != 3 {
		t.Fatalf("expected 3 deployments, got %d", len(deployments))
	}

	first := deployments[0]
	if first.Name != "redis-1" || first.CloudConfig != "latest" ||
		strings.Join(first.Releases, ",") != "redis-forge/1.4.3,bpm/1.4.26" ||
		strings.Join(first.Stemcells, ",") != "bosh-lite-ubuntu-noble/1.25" ||
		strings.Join(first.Teams, ",") != "blacksmith" {
		t.Errorf("unexpected first deployment: %+v", first)
	}

	if deployments[1].Name != "rabbit-1" || strings.Join(deployments[1].Releases, ",") != "rabbitmq-forge/2.0.0" || len(deployments[1].Stemcells) != 0 {
		t.Errorf("unexpected second deployment: %+v", deployments[1])
	}

	if strings.Join(deployments[2].Releases, ",") != "dev-release/unparseable-version" || strings.Join(deployments[2].Stemcells, ",") != "dev-stemcell/also unparseable" {
		t.Errorf("expected the third deployment to keep the versions as the director reports them: %+v", deployments[2])
	}
}

// Releases keep every version the director holds, and listing them takes one
// request, with no extra call to /info.
func TestGetReleasesReadsEveryVersionInOneRequest(t *testing.T) {
	server := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
		if request.URL.Path != "/releases" {
			return false
		}

		boshtest.WriteJSON(writer, http.StatusOK, []map[string]interface{}{
			{
				"name": "redis-forge",
				"release_versions": []map[string]interface{}{
					{"version": "1.4.3", "commit_hash": "abc123", "uncommitted_changes": false, "currently_deployed": true, "job_names": []string{"redis"}},
					{"version": "1.4.2", "commit_hash": "def456", "uncommitted_changes": true, "currently_deployed": false, "job_names": []string{"redis"}},
				},
			},
		})

		return true
	})

	director := newReadAdapter(t, server, nil)

	releases, err := director.GetReleases(t.Context())
	if err != nil {
		t.Fatalf("GetReleases: %v", err)
	}

	if len(releases) != 1 || releases[0].Name != "redis-forge" || len(releases[0].ReleaseVersions) != 2 {
		t.Fatalf("unexpected releases: %+v", releases)
	}

	deployed := releases[0].ReleaseVersions[0]
	if deployed.Version != "1.4.3" || deployed.CommitHash != "abc123" || !deployed.CurrentlyDeployed || len(deployed.JobNames) != 1 {
		t.Errorf("unexpected first version: %+v", deployed)
	}

	if !releases[0].ReleaseVersions[1].UncommittedChanges {
		t.Errorf("expected the second version to keep its uncommitted_changes flag: %+v", releases[0].ReleaseVersions[1])
	}

	if requests := server.APIRequests(); len(requests) != 1 || requests[0].Path != "/releases" {
		t.Fatalf("expected exactly one GET /releases, got %+v", requests)
	}
}

// Stemcells carry the deployments that use them, as names.
func TestGetStemcellsMapsDeploymentsToNames(t *testing.T) {
	server := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
		if request.URL.Path != "/stemcells" {
			return false
		}

		boshtest.WriteJSON(writer, http.StatusOK, []map[string]interface{}{
			{"name": "bosh-lite-ubuntu-noble", "operating_system": "ubuntu-noble", "version": "1.25", "cid": "stemcell-1", "cpi": "pve", "deployments": []map[string]string{{"name": "redis-1"}}},
			{"name": "bosh-lite-ubuntu-noble", "operating_system": "ubuntu-noble", "version": "1.26", "cid": "stemcell-2", "cpi": "pve"},
		})

		return true
	})

	director := newReadAdapter(t, server, nil)

	stemcells, err := director.GetStemcells(t.Context())
	if err != nil {
		t.Fatalf("GetStemcells: %v", err)
	}

	if len(stemcells) != 2 {
		t.Fatalf("expected 2 stemcells, got %d", len(stemcells))
	}

	if stemcells[0].OS != "ubuntu-noble" || stemcells[0].CID != "stemcell-1" || stemcells[0].CPI != "pve" || strings.Join(stemcells[0].Deployments, ",") != "redis-1" {
		t.Errorf("unexpected first stemcell: %+v", stemcells[0])
	}

	if stemcells[1].Deployments == nil || len(stemcells[1].Deployments) != 0 {
		t.Errorf("expected an empty, non-nil deployment list for an unused stemcell, got %#v", stemcells[1].Deployments)
	}
}

// Events are filtered to the deployment by the director, and their times are
// UTC.
func TestGetEventsFiltersByDeploymentAndMapsFields(t *testing.T) {
	server := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
		if request.URL.Path != "/events" {
			return false
		}

		boshtest.WriteJSON(writer, http.StatusOK, []map[string]interface{}{
			{"id": "7", "timestamp": 1760000000, "user": "admin", "action": "create", "object_type": "deployment", "object_name": "redis-1", "task": "42", "deployment": "redis-1", "instance": "redis/0", "context": map[string]string{"k": "v"}, "error": "boom"},
		})

		return true
	})

	director := newReadAdapter(t, server, nil)

	events, err := director.GetEvents(t.Context(), "redis-1")
	if err != nil {
		t.Fatalf("GetEvents: %v", err)
	}

	requests := server.APIRequests()
	if len(requests) != 1 || !strings.Contains(requests[0].RawQuery, "deployment=redis-1") {
		t.Fatalf("expected one GET /events for the deployment, got %+v", requests)
	}

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}

	event := events[0]
	if event.ID != "7" || event.TaskID != "42" || event.Deployment != "redis-1" || event.Instance != "redis/0" ||
		event.Action != "create" || event.ObjectType != "deployment" || event.ObjectName != "redis-1" ||
		event.User != "admin" || event.Error != "boom" || event.Context != "map[k:v]" {
		t.Errorf("unexpected event: %+v", event)
	}

	if !event.Time.Equal(time.Unix(1760000000, 0)) || event.Time.Location() != time.UTC {
		t.Errorf("expected the event time to be 1760000000 in UTC, got %v", event.Time)
	}
}

// Errands are listed with the one request the director serves for them.
func TestListErrandsReadsTheDeploymentsErrands(t *testing.T) {
	server := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
		if request.URL.Path != "/deployments/redis-1/errands" {
			return false
		}

		boshtest.WriteJSON(writer, http.StatusOK, []map[string]string{{"name": "smoke-tests"}, {"name": "cleanup"}})

		return true
	})

	director := newReadAdapter(t, server, nil)

	errands, err := director.ListErrands(t.Context(), "redis-1")
	if err != nil {
		t.Fatalf("ListErrands: %v", err)
	}

	if len(errands) != 2 || errands[0].Name != "smoke-tests" || errands[1].Name != "cleanup" {
		t.Fatalf("unexpected errands: %+v", errands)
	}

	if requests := server.APIRequests(); len(requests) != 1 || requests[0].Path != "/deployments/redis-1/errands" {
		t.Fatalf("expected exactly one GET for the errands, got %+v", requests)
	}
}

// GetInfo reads /info without credentials, so it works against a director
// that rejects every authenticated request. The director reports each feature
// as {"status": bool, "extras": {...}}, and GetInfo maps each feature name to
// its status.
func TestGetInfoNeedsNoCredentials(t *testing.T) {
	var server *boshtest.Director

	server = boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
		if request.URL.Path == "/info" {
			boshtest.WriteJSON(writer, http.StatusOK, map[string]interface{}{
				"name": "lab-bosh", "uuid": "u-1", "version": "282.0.0", "user": nil, "cpi": "pve",
				"user_authentication": map[string]interface{}{"type": "uaa", "options": map[string]string{"url": server.URL}},
				"features": map[string]interface{}{
					"local_dns":     map[string]interface{}{"status": true, "extras": map[string]interface{}{"domain_name": "bosh"}},
					"snapshots":     map[string]interface{}{"status": false},
					"config_server": map[string]interface{}{"status": true},
				},
			})

			return true
		}

		boshtest.WriteError(writer, http.StatusUnauthorized, 10000, "Not authenticated")

		return true
	})

	director := newReadAdapter(t, server, nil)

	info, err := director.GetInfo(t.Context())
	if err != nil {
		t.Fatalf("GetInfo: %v", err)
	}

	if info.Name != "lab-bosh" || info.UUID != "u-1" || info.CPI != "pve" || info.UAAURL != server.URL {
		t.Errorf("unexpected info: %+v", info)
	}

	wantFeatures := map[string]bool{"local_dns": true, "snapshots": false, "config_server": true}
	if !reflect.DeepEqual(info.Features, wantFeatures) {
		t.Errorf("Features = %v, want %v", info.Features, wantFeatures)
	}
}

// A failed read says what failed, where, and what the director said, and for
// the answers that carry no advice of their own it says what to check.
func TestReadFailuresNameTheDirectorAndWhatToCheck(t *testing.T) {
	tests := []struct {
		name   string
		status int
		code   int
		body   string
		wants  []string
	}{
		{
			name: "forbidden", status: http.StatusForbidden, code: 10001, body: "Not authorized: '/deployments'",
			wants: []string{"HTTP 403", "Not authorized: '/deployments'", "bosh.admin"},
		},
		{
			name: "internal error", status: http.StatusInternalServerError, code: 50000, body: "Sequel::DatabaseError",
			wants: []string{"HTTP 500", "Sequel::DatabaseError", "monit summary"},
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			server := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
				if request.URL.Path != "/deployments" {
					return false
				}

				boshtest.WriteError(writer, testCase.status, testCase.code, testCase.body)

				return true
			})

			director := newReadAdapter(t, server, nil)

			_, err := director.GetDeployments(t.Context())
			if err == nil {
				t.Fatal("expected an error")
			}

			for _, want := range append([]string{"list deployments", server.URL}, testCase.wants...) {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("expected the error to mention %q, got: %s", want, err.Error())
				}
			}
		})
	}
}

// Without credentials the adapter has nothing to authenticate with, and it
// says so when it is built, not at the first request.
func TestNewDirectorAdapterRequiresCredentials(t *testing.T) {
	_, err := bosh.NewDirectorAdapter(bosh.Config{Address: "https://127.0.0.1:1"})
	if !errors.Is(err, bosh.ErrDirectorCredentialsMissing) {
		t.Fatalf("expected ErrDirectorCredentialsMissing, got: %v", err)
	}

	if !strings.Contains(err.Error(), "https://127.0.0.1:1") || !strings.Contains(err.Error(), "bosh.username") {
		t.Errorf("expected the error to name the director and the setting, got: %s", err.Error())
	}
}

// A username or password made only of whitespace is as missing as an empty
// one, and with no address the error still reads as a sentence rather than
// naming a director at an empty address.
func TestBlankCredentialsAreReportedAsMissing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		config bosh.Config
		wants  []string
	}{
		{name: "a blank username", config: bosh.Config{Address: "https://127.0.0.1:1", Username: " \t", Password: basicAuthCredential},
			wants: []string{"for the director at https://127.0.0.1:1.", "bosh.username"}},
		{name: "a blank password", config: bosh.Config{Address: "https://127.0.0.1:1", Username: basicAuthCredential, Password: "\n"},
			wants: []string{"for the director at https://127.0.0.1:1.", "bosh.password"}},
		{name: "no address", config: bosh.Config{},
			wants: []string{"no BOSH director credentials are configured. Set bosh.username and bosh.password"}},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			_, err := bosh.NewDirectorAdapter(testCase.config)
			if !errors.Is(err, bosh.ErrDirectorCredentialsMissing) {
				t.Fatalf("expected ErrDirectorCredentialsMissing, got: %v", err)
			}

			for _, want := range testCase.wants {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("expected the error to contain %q, got: %s", want, err.Error())
				}
			}
		})
	}
}

// An adapter with no credentials reports that before it does any network
// setup, so it never reaches for the director first. The CA below cannot be
// parsed, so any setup that runs before the credentials check would fail with
// its own error instead of ErrDirectorCredentialsMissing. The address is the
// loopback, so nothing here could reach a real director anyway.
func TestMissingCredentialsAreReportedBeforeAnyNetworkSetup(t *testing.T) {
	_, err := bosh.NewDirectorAdapter(bosh.Config{Address: "https://127.0.0.1:1", CACert: "this is not a certificate"})
	if !errors.Is(err, bosh.ErrDirectorCredentialsMissing) {
		t.Fatalf("expected ErrDirectorCredentialsMissing before any network setup, got: %v", err)
	}
}

// The factory owns the 60 second default for bosh.request_timeout, and the
// adapter takes whatever timeout it is given. An adapter built without the
// factory does not invent a figure of its own, so the default cannot live in
// two places and the factory cannot lose it unnoticed.
func TestRequestTimeoutDefaultsToSixtySeconds(t *testing.T) {
	base := bosh.Config{Address: "https://127.0.0.1:1", Username: basicAuthCredential, Password: basicAuthCredential}

	withTimeout := base
	withTimeout.RequestTimeout = 90 * time.Second

	direct, err := bosh.NewDirectorAdapter(base)
	if err != nil {
		t.Fatalf("NewDirectorAdapter: %v", err)
	}

	factoryDefault, err := bosh.NewDefaultFactory().New(base)
	if err != nil {
		t.Fatalf("Factory.New: %v", err)
	}

	factorySet, err := bosh.NewDefaultFactory().New(withTimeout)
	if err != nil {
		t.Fatalf("Factory.New: %v", err)
	}

	if got := bosh.RequestTimeoutOf(direct); got != 0 {
		t.Errorf("an adapter built without the factory has timeout %v, want it left at 0 for boshapi to default", got)
	}

	if got := bosh.RequestTimeoutOf(factoryDefault); got != 60*time.Second {
		t.Errorf("Factory default timeout = %v, want 1m0s", got)
	}

	if got := bosh.RequestTimeoutOf(factorySet); got != 90*time.Second {
		t.Errorf("configured timeout = %v, want 1m30s", got)
	}
}

// The cause names the timeout and the attempts when it knows them, and it
// does not print a figure it does not know.
func TestRequestTimeoutCauseText(t *testing.T) {
	tests := []struct {
		timeout  time.Duration
		attempts int
		want     string
	}{
		{timeout: 60 * time.Second, attempts: 1, want: "no answer within the 60s bosh.request_timeout"},
		{timeout: 60 * time.Second, attempts: 4, want: "no answer within the 60s bosh.request_timeout on each of 4 attempts"},
		{timeout: 0, attempts: 1, want: "no answer within the bosh.request_timeout"},
	}

	for _, testCase := range tests {
		if got := bosh.RequestTimeoutCause(testCase.timeout, testCase.attempts).Error(); got != testCase.want {
			t.Errorf("RequestTimeoutCause(%v, %d) = %q, want %q", testCase.timeout, testCase.attempts, got, testCase.want)
		}
	}
}

// nilAnswerClient answers the reads this package moved with a nil value and
// no error, which a well-behaved boshapi never does.
type nilAnswerClient struct {
	boshapi.Client
}

func (nilAnswerClient) GetInfo(context.Context) (*boshapi.Info, error) { return nil, nil }

func (nilAnswerClient) Deployments() boshapi.DeploymentsClient { return nilDeployments{} }

type nilDeployments struct {
	boshapi.DeploymentsClient
}

func (nilDeployments) Get(context.Context, string) (*boshapi.DeploymentManifest, error) {
	return nil, nil
}

// A nil answer from the client must come back as an error that names the
// director, not as a nil dereference.
func TestNilClientAnswersBecomeErrors(t *testing.T) {
	server := boshtest.New(t, nil)
	director := newReadAdapter(t, server, nil)

	bosh.WithAPI(director, nilAnswerClient{})

	_, err := director.GetInfo(t.Context())
	if !errors.Is(err, bosh.ErrEmptyDirectorAnswer) || !strings.Contains(err.Error(), server.URL) {
		t.Errorf("GetInfo: expected ErrEmptyDirectorAnswer naming the director, got: %v", err)
	}

	_, err = director.GetDeployment(t.Context(), "redis-1")
	if !errors.Is(err, bosh.ErrEmptyDirectorAnswer) || !strings.Contains(err.Error(), "redis-1") {
		t.Errorf("GetDeployment: expected ErrEmptyDirectorAnswer naming the deployment, got: %v", err)
	}
}
