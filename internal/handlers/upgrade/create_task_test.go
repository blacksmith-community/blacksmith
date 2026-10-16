package upgrade_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"blacksmith/internal/bosh"
	"blacksmith/internal/bosh/boshtest"
	upgradehandler "blacksmith/internal/handlers/upgrade"
	"blacksmith/internal/upgrade"
	"blacksmith/pkg/logger"
)

// releasesDirector starts a test director that holds redis-forge 9.9.9
// and returns an adapter over it.
func releasesDirector(t *testing.T) (*boshtest.Director, bosh.Director) {
	t.Helper()

	director := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
		if request.Method != http.MethodGet || request.URL.Path != "/releases" {
			return false
		}

		boshtest.WriteJSON(writer, http.StatusOK, []map[string]interface{}{{
			"name":             "redis-forge",
			"release_versions": []map[string]interface{}{{"version": "9.9.9"}},
		}})

		return true
	})

	adapter, err := bosh.NewDirectorAdapter(bosh.Config{
		Address:  director.URL,
		Username: directorCredential,
		Password: directorCredential,
		CACert:   director.CACert,
	})
	if err != nil {
		t.Fatalf("creating the director adapter: %v", err)
	}

	return director, adapter
}

// requestsTo counts the requests the test director received for the path.
func requestsTo(director *boshtest.Director, method, path string) int {
	count := 0

	for _, request := range director.APIRequests() {
		if request.Method == method && request.Path == path {
			count++
		}
	}

	return count
}

// createCase is a request to create an upgrade, the status and text its
// answer must carry, and how many release listings it must make.
type createCase struct {
	body     string
	status   int
	says     string
	listings int
}

// createCases returns a request for each combination of targets.
func createCases() map[string]createCase {
	return map[string]createCase{
		"no target": {
			body:   `{"instance_ids":["i-1"],"target_stemcell":{"os":"","version":""}}`,
			status: http.StatusInternalServerError, says: "no upgrade target",
		},
		"a half-set stemcell": {
			body:   `{"instance_ids":["i-1"],"target_stemcell":{"os":"ubuntu-noble","version":""}}`,
			status: http.StatusInternalServerError, says: "invalid stemcell target",
		},
		"a release with no version": {
			body:   `{"instance_ids":["i-1"],"target_releases":[{"name":"redis-forge"}]}`,
			status: http.StatusInternalServerError, says: "invalid release target",
		},
		"a version the director lacks": {
			body:   `{"instance_ids":["i-1"],"target_releases":[{"name":"redis-forge","version":"9.9.10"}]}`,
			status: http.StatusInternalServerError, says: "redis-forge/9.9.10", listings: 1,
		},
		"a stemcell alone": {
			body:   `{"instance_ids":["i-1"],"target_stemcell":{"os":"ubuntu-noble","version":"1.2"}}`,
			status: http.StatusOK,
		},
		"releases alone": {
			body:   `{"instance_ids":["i-1"],"target_stemcell":{"os":"","version":""},"target_releases":[{"name":"redis-forge","version":"9.9.9"}]}`,
			status: http.StatusOK, says: `"target_releases":[{"name":"redis-forge","version":"9.9.9"}]`, listings: 1,
		},
		"a stemcell and releases": {
			body:   `{"instance_ids":["i-1"],"target_stemcell":{"os":"ubuntu-noble","version":"1.2"},"target_releases":[{"name":"redis-forge","version":"9.9.9"}]}`,
			status: http.StatusOK, says: `"target_releases":[{"name":"redis-forge","version":"9.9.9"}]`, listings: 1,
		},
	}
}

// expectCreateAnswer sends the case's request to a new handler and checks
// its answer, the director requests it made, and the upgrades it left.
func expectCreateAnswer(t *testing.T, test createCase) {
	t.Helper()

	director, adapter := releasesDirector(t)

	handler := upgradehandler.NewHandler(upgradehandler.Dependencies{
		Logger:   &logger.NoOpLogger{},
		Director: adapter,
		Vault:    newMemoryVault(),
	})
	t.Cleanup(handler.Stop)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/b/upgrade/tasks", strings.NewReader(test.body)))

	if recorder.Code != test.status || !strings.Contains(recorder.Body.String(), test.says) {
		t.Fatalf("expected status %d saying %q, got %d: %s", test.status, test.says, recorder.Code, recorder.Body.String())
	}

	if listings := requestsTo(director, http.MethodGet, "/releases"); listings != test.listings {
		t.Fatalf("expected %d release listings, got %d", test.listings, listings)
	}

	if deploys := requestsTo(director, http.MethodPost, "/deployments"); deploys != 0 {
		t.Fatalf("expected no deploy, got %d", deploys)
	}

	if test.status != http.StatusOK {
		if tasks := handler.GetManager().ListTasks(); len(tasks) != 0 {
			t.Fatalf("expected no upgrade to be created, got %+v", tasks)
		}

		return
	}

	var task upgrade.UpgradeTask

	err := json.Unmarshal(recorder.Body.Bytes(), &task)
	if err != nil || task.ID == "" {
		t.Fatalf("expected the created upgrade, got %s (%v)", recorder.Body.String(), err)
	}
}

func TestCreateTaskValidatesEachTargetCombination(t *testing.T) {
	t.Parallel()

	for name, test := range createCases() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			expectCreateAnswer(t, test)
		})
	}
}
