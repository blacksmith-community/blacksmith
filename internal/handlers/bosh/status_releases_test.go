package bosh_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"blacksmith/internal/bosh"
	boshhandler "blacksmith/internal/handlers/bosh"
	"blacksmith/internal/interfaces"
	"blacksmith/internal/services"
	"blacksmith/pkg/logger"
)

const (
	redisInstance    = "redis-instance"
	valkeyInstance   = "valkey-instance"
	redisPlan        = "redis-plan"
	redisDeployment  = "redis-deployment"
	valkeyDeployment = "valkey-deployment"
	deployedForge    = "redis-forge/9.9.8"
	deployedValkey   = "valkey-forge/1.4.3"
	deployedBPM      = "bpm/1.4.20"
	planIDField      = "plan_id"
	deploymentField  = "deployment_name"

	// waitLimit bounds how long a test waits for an answer that should
	// come at once.
	waitLimit = 2 * time.Second
)

// statusVault answers the vault reads behind /b/status: the db index, and
// each instance's full record. A call it does not script panics through
// the nil embedded Vault.
type statusVault struct {
	interfaces.Vault

	records map[string]map[string]interface{}
}

func (v *statusVault) Get(_ context.Context, path string, result interface{}) (bool, error) {
	var data map[string]interface{}

	if path == "db" {
		data = map[string]interface{}{}
		for id, record := range v.records {
			data[id] = map[string]interface{}{planIDField: record[planIDField]}
		}
	} else {
		record, ok := v.records[path]
		if !ok {
			return false, nil
		}

		data = record
	}

	encoded, err := json.Marshal(data)
	if err != nil {
		return false, fmt.Errorf("encoding %s: %w", path, err)
	}

	err = json.Unmarshal(encoded, result)
	if err != nil {
		return false, fmt.Errorf("decoding %s: %w", path, err)
	}

	return true, nil
}

// statusConfig names the environment and panics, through the nil embedded
// Config, on any other call.
type statusConfig struct {
	interfaces.Config
}

func (statusConfig) GetEnvironment() string { return "test" }

// planBroker serves one plan whose manifest declares the releases given.
type planBroker struct {
	releases []string
}

func (planBroker) IsBroker() bool { return true }

func (planBroker) GetVault() interfaces.Vault { return nil }

func (b planBroker) GetPlans() map[string]services.Plan {
	releases := make([]interface{}, 0, len(b.releases))
	for _, name := range b.releases {
		releases = append(releases, map[interface{}]interface{}{"name": name, "version": "latest"})
	}

	return map[string]services.Plan{redisPlan: {
		ID:       redisPlan,
		Name:     "standalone",
		Manifest: map[interface{}]interface{}{"releases": releases},
	}}
}

// blockingDirector is a director whose deployment listing blocks until
// its context ends, and which counts the listings it was asked for. A call
// it does not script panics through the nil embedded Director.
type blockingDirector struct {
	bosh.Director

	listings atomic.Int32
}

func (d *blockingDirector) GetDeployments(ctx context.Context) ([]bosh.Deployment, error) {
	d.listings.Add(1)
	<-ctx.Done()

	return nil, fmt.Errorf("listing deployments: %w", ctx.Err())
}

// statusHandler returns a handler over the director, with two instances
// in the vault and one plan that declares redis-forge and bpm.
func statusHandler(director bosh.Director, timeout time.Duration) *boshhandler.Handler {
	return boshhandler.NewHandler(boshhandler.Dependencies{
		Logger:                    &logger.NoOpLogger{},
		Config:                    statusConfig{},
		Director:                  director,
		Broker:                    planBroker{releases: []string{forgeRelease, bpmRelease}},
		DeploymentReleasesTimeout: timeout,
		Vault: &statusVault{records: map[string]map[string]interface{}{
			redisInstance:  {planIDField: redisPlan, deploymentField: redisDeployment},
			valkeyInstance: {planIDField: "valkey-plan", deploymentField: valkeyDeployment},
		}},
	})
}

// getWithin sends a GET of the path to the handler function, and fails
// the test when the answer takes longer than the limit.
func getWithin(t *testing.T, limit time.Duration, serve http.HandlerFunc, path string) *httptest.ResponseRecorder {
	t.Helper()

	answered := make(chan *httptest.ResponseRecorder, 1)

	go func() {
		answered <- get(t, serve, path)
	}()

	select {
	case recorder := <-answered:
		return recorder
	case <-time.After(limit):
		t.Fatalf("expected %s to answer within %s", path, limit)

		return nil
	}
}

// status requests /b/status from the handler and decodes its answer.
func status(t *testing.T, handler *boshhandler.Handler) statusAnswer {
	t.Helper()

	recorder := getWithin(t, waitLimit, handler.GetStatus, "/b/status")
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", recorder.Code, recorder.Body.String())
	}

	var answer statusAnswer

	err := json.Unmarshal(recorder.Body.Bytes(), &answer)
	if err != nil {
		t.Fatalf("decoding the status: %v", err)
	}

	return answer
}

// statusAnswer holds the parts of /b/status the tests read.
type statusAnswer struct {
	Instances map[string]map[string]interface{} `json:"instances"`
	Plans     map[string]map[string]interface{} `json:"plans"`
}

func TestStatusAnswersWithoutCallingTheDirector(t *testing.T) {
	t.Parallel()

	director := &blockingDirector{}
	answer := status(t, statusHandler(director, time.Hour))

	if answer.Instances[redisInstance][deploymentField] != redisDeployment {
		t.Fatalf("expected the redis instance with its vault data, got %v", answer.Instances[redisInstance])
	}

	if _, named := answer.Instances[redisInstance]["release_versions"]; named {
		t.Fatalf("expected no release versions in the status, got %v", answer.Instances[redisInstance])
	}

	if listings := director.listings.Load(); listings != 0 {
		t.Fatalf("expected no deployment listing, got %d", listings)
	}
}

func TestStatusNamesEachPlansReleases(t *testing.T) {
	t.Parallel()

	answer := status(t, statusHandler(&blockingDirector{}, time.Hour))

	if got := answer.Plans[redisPlan]["releases"]; !reflect.DeepEqual(got, []interface{}{forgeRelease, bpmRelease}) {
		t.Fatalf("expected the plan's releases in manifest order, got %v", got)
	}
}

func TestDeploymentReleasesNamesEachDeploymentsManagedReleases(t *testing.T) {
	t.Parallel()

	director := &releaseDirector{deployments: []bosh.Deployment{
		{Name: redisDeployment, Releases: []string{"os-conf/22.1.2", deployedForge, deployedBPM, "bosh-dns/1.36.0"}},
		{Name: valkeyDeployment, Releases: []string{"syslog/12.2.0", deployedValkey}},
		{Name: "cf", Releases: []string{"os-conf/22.1.2"}},
	}}

	recorder := get(t, statusHandler(director, time.Hour).GetDeploymentReleases, "/b/bosh/deployment-releases")
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", recorder.Code, recorder.Body.String())
	}

	var releases map[string][]string

	err := json.Unmarshal(recorder.Body.Bytes(), &releases)
	if err != nil {
		t.Fatalf("decoding the deployment releases: %v", err)
	}

	expected := map[string][]string{
		redisDeployment:  {deployedForge, deployedBPM},
		valkeyDeployment: {deployedValkey},
	}
	if !reflect.DeepEqual(releases, expected) {
		t.Fatalf("expected only the releases the plans declare or forges, got %v", releases)
	}

	if len(director.underRequest) != 1 || !director.underRequest[0] {
		t.Fatalf("expected one deployment listing under the request's context, got %v", director.underRequest)
	}
}

func TestDeploymentReleasesReportsADirectorError(t *testing.T) {
	t.Parallel()

	handler := statusHandler(&releaseDirector{err: errDirectorInTest}, time.Hour)

	recorder := get(t, handler.GetDeploymentReleases, "/b/bosh/deployment-releases")
	if recorder.Code != http.StatusInternalServerError || !strings.Contains(recorder.Body.String(), errDirectorInTest.Error()) {
		t.Fatalf("expected status 500 naming the director's error, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestDeploymentReleasesGivesUpOnASlowDirector(t *testing.T) {
	t.Parallel()

	director := &blockingDirector{}
	handler := statusHandler(director, 50*time.Millisecond)

	recorder := getWithin(t, waitLimit, handler.GetDeploymentReleases, "/b/bosh/deployment-releases")
	if recorder.Code != http.StatusGatewayTimeout {
		t.Fatalf("expected status 504 from a director that doesn't answer, got %d: %s", recorder.Code, recorder.Body.String())
	}

	if listings := director.listings.Load(); listings != 1 {
		t.Fatalf("expected one deployment listing, got %d", listings)
	}
}
