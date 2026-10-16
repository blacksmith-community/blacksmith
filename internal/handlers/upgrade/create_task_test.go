package upgrade_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/bosh/boshtest"
	upgradehandler "blacksmith/internal/handlers/upgrade"
	"blacksmith/internal/interfaces"
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
		"a body that is not JSON": {
			body:   `{"instance_ids":`,
			status: http.StatusBadRequest, says: "invalid upgrade request",
		},
		"no instances": {
			body:   `{"instance_ids":[],"target_stemcell":{"os":"ubuntu-noble","version":"1.2"}}`,
			status: http.StatusBadRequest, says: "no instances",
		},
		"no target": {
			body:   `{"instance_ids":["i-1"],"target_stemcell":{"os":"","version":""}}`,
			status: http.StatusBadRequest, says: "no upgrade target",
		},
		"a half-set stemcell": {
			body:   `{"instance_ids":["i-1"],"target_stemcell":{"os":"ubuntu-noble","version":""}}`,
			status: http.StatusBadRequest, says: "invalid stemcell target",
		},
		"a release with no version": {
			body:   `{"instance_ids":["i-1"],"target_releases":[{"name":"redis-forge"}]}`,
			status: http.StatusBadRequest, says: "invalid release target",
		},
		"a version the director lacks": {
			body:   `{"instance_ids":["i-1"],"target_releases":[{"name":"redis-forge","version":"9.9.10"}]}`,
			status: http.StatusBadRequest, says: "redis-forge/9.9.10", listings: 1,
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

// loadTimeout bounds a wait for a handler's manager to load the stored
// upgrades.
const loadTimeout = 5 * time.Second

// startHandler starts a handler over the director and the vault, which
// stops when the test ends.
func startHandler(t *testing.T, director bosh.Director, vault interfaces.Vault) *upgradehandler.Handler {
	t.Helper()

	handler := upgradehandler.NewHandler(upgradehandler.Dependencies{
		Logger:   &logger.NoOpLogger{},
		Director: director,
		Vault:    vault,
	})
	t.Cleanup(handler.Stop)

	return handler
}

// loadedHandler starts a handler and waits until its manager has loaded
// the stored upgrades.
func loadedHandler(t *testing.T, director bosh.Director, vault interfaces.Vault) *upgradehandler.Handler {
	t.Helper()

	handler := startHandler(t, director, vault)

	select {
	case <-handler.GetManager().Loaded():
	case <-time.After(loadTimeout):
		t.Fatal("the manager never finished loading the stored upgrades")
	}

	return handler
}

// postUpgrade sends the body to the handler as a request to create an
// upgrade, and returns the answer.
func postUpgrade(t *testing.T, handler *upgradehandler.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/b/upgrade/tasks", strings.NewReader(body)))

	return recorder
}

// expectError fails the test unless the answer has the status and an
// error message that contains the text given.
func expectError(t *testing.T, recorder *httptest.ResponseRecorder, status int, says string) {
	t.Helper()

	var answer struct {
		Error string `json:"error"`
	}

	err := json.Unmarshal(recorder.Body.Bytes(), &answer)
	if recorder.Code != status || err != nil || !strings.Contains(answer.Error, says) {
		t.Fatalf("expected status %d with an error saying %q, got %d: %s", status, says, recorder.Code, recorder.Body.String())
	}
}

// heldIndexVault is a memoryVault whose read of the upgrade tasks index
// waits until the test lets it go.
type heldIndexVault struct {
	*memoryVault

	release chan struct{}
	once    sync.Once
}

func (v *heldIndexVault) Get(ctx context.Context, path string, result interface{}) (bool, error) {
	if path == "upgrade-tasks" {
		select {
		case <-v.release:
		case <-ctx.Done():
			return false, fmt.Errorf("reading %s: %w", path, ctx.Err())
		}
	}

	return v.memoryVault.Get(ctx, path, result)
}

// letGo lets the read of the index, and every later one, go on.
func (v *heldIndexVault) letGo() { v.once.Do(func() { close(v.release) }) }

// expectCreateAnswer sends the case's request to a new handler and checks
// its answer, the director requests it made, and the upgrades it left.
func expectCreateAnswer(t *testing.T, test createCase) {
	t.Helper()

	director, adapter := releasesDirector(t)
	handler := loadedHandler(t, adapter, newMemoryVault())
	recorder := postUpgrade(t, handler, test.body)

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

// A request for an instance a stored upgrade holds is a conflict.
func TestCreateTaskAnswersABusyInstanceWithAConflict(t *testing.T) {
	t.Parallel()

	paused := runningUpgrade("paused-upgrade", upgrade.InstanceUpgrade{InstanceID: "i-1", DeploymentName: "redis-i-1", Status: upgrade.InstanceStatusPending})
	paused.Status = upgrade.TaskStatusPaused
	paused.Paused = true

	vault := newMemoryVault()
	storeUpgrades(t, vault, paused)

	_, adapter := releasesDirector(t)
	handler := loadedHandler(t, adapter, vault)

	recorder := postUpgrade(t, handler, `{"instance_ids":["i-1"],"target_stemcell":{"os":"ubuntu-noble","version":"1.2"}}`)
	expectError(t, recorder, http.StatusConflict, "paused-upgrade")
}

// A request made while the stored upgrades load is answered as a service
// that is not ready yet.
func TestCreateTaskAnswersWhileLoadingWithServiceUnavailable(t *testing.T) {
	t.Parallel()

	vault := &heldIndexVault{memoryVault: newMemoryVault(), release: make(chan struct{})}

	_, adapter := releasesDirector(t)
	handler := startHandler(t, adapter, vault)
	t.Cleanup(vault.letGo)

	recorder := postUpgrade(t, handler, `{"instance_ids":["i-1"],"target_stemcell":{"os":"ubuntu-noble","version":"1.2"}}`)
	expectError(t, recorder, http.StatusServiceUnavailable, "still loading")
}
