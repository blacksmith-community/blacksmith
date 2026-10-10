package upgrade_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
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

const (
	// adoptedTaskID is the deploy of upgrade A that was in flight when
	// the broker stopped, which the test director holds processing.
	adoptedTaskID = 7
	// pendingTaskID is the task the test director gives a new deploy.
	pendingTaskID = 8
	// adoptedDeployment and pendingDeployment are the deployments of the
	// two upgrades.
	adoptedDeployment = "redis-adopted"
	pendingDeployment = "redis-pending"
	// deployWait is how long the limit callback waits for a deploy before
	// it applies the limit.
	deployWait = 2 * time.Second
	// settleTimeout bounds a wait for the batch director's counts.
	settleTimeout = 10 * time.Second
	// directorCredential is the test director's username and password.
	directorCredential = "admin"
)

// memoryVault is an interfaces.Vault that keeps a JSON copy of each value
// saved. A call it does not script panics through the nil embedded Vault.
type memoryVault struct {
	interfaces.Vault

	mu    sync.Mutex
	saved map[string][]byte
}

func newMemoryVault() *memoryVault { return &memoryVault{saved: make(map[string][]byte)} }

func (v *memoryVault) Get(_ context.Context, path string, result interface{}) (bool, error) {
	v.mu.Lock()
	raw, ok := v.saved[path]
	v.mu.Unlock()

	if !ok {
		return false, nil
	}

	err := json.Unmarshal(raw, result)
	if err != nil {
		return true, fmt.Errorf("unmarshal %s: %w", path, err)
	}

	return true, nil
}

func (v *memoryVault) Put(_ context.Context, path string, data interface{}) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", path, err)
	}

	v.mu.Lock()
	v.saved[path] = raw
	v.mu.Unlock()

	return nil
}

// store saves the value at the path, and fails the test when it cannot.
func (v *memoryVault) store(t *testing.T, path string, data interface{}) {
	t.Helper()

	err := v.Put(context.Background(), path, data)
	if err != nil {
		t.Fatalf("storing %s: %v", path, err)
	}
}

// storeUpgrades saves the upgrades and the tasks index that names them, as
// a broker that stopped mid-upgrade left them.
func storeUpgrades(t *testing.T, vault *memoryVault, tasks ...upgrade.UpgradeTask) {
	t.Helper()

	index := map[string]interface{}{}

	for _, task := range tasks {
		vault.store(t, "upgrade-tasks/"+task.ID, task)
		index[task.ID] = map[string]interface{}{"status": task.Status, "created_at": task.CreatedAt.Unix()}
	}

	vault.store(t, "upgrade-tasks", index)
}

// runningUpgrade returns a running upgrade of the one instance given.
func runningUpgrade(taskID string, instance upgrade.InstanceUpgrade) upgrade.UpgradeTask {
	started := time.Now().Add(-time.Hour)

	return upgrade.UpgradeTask{
		ID:             taskID,
		Status:         upgrade.TaskStatusRunning,
		TargetStemcell: upgrade.StemcellTarget{OS: "ubuntu-noble", Version: "1.2"},
		Instances:      []upgrade.InstanceUpgrade{instance},
		TotalCount:     1,
		CreatedAt:      started,
		StartedAt:      &started,
	}
}

// processingTask answers a read of the task as still processing.
func processingTask(writer http.ResponseWriter, taskID int, deployment string) {
	boshtest.WriteJSON(writer, http.StatusOK, map[string]interface{}{
		"id":          taskID,
		"state":       "processing",
		"description": "create deployment",
		"timestamp":   1759766400,
		"started_at":  1759766000,
		"result":      "",
		"user":        directorCredential,
		"deployment":  deployment,
		"context_id":  "",
	})
}

// limitTestDirector starts a test director that holds the adopted deploy
// and every new deploy processing, serves the pending upgrade's
// deployment, and sends on deployed as each deploy arrives. It returns
// an adapter over it.
func limitTestDirector(t *testing.T, deployed chan<- struct{}) (*boshtest.Director, bosh.Director) {
	t.Helper()

	director := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/deployments/"+pendingDeployment:
			boshtest.WriteJSON(writer, http.StatusOK, map[string]interface{}{
				"name":      pendingDeployment,
				"manifest":  "name: " + pendingDeployment + "\nstemcells:\n- alias: default\n  os: ubuntu-noble\n  version: \"1.1\"\n",
				"releases":  []string{},
				"stemcells": []string{},
				"teams":     []string{},
			})
		case request.Method == http.MethodPost && request.URL.Path == "/deployments":
			select {
			case deployed <- struct{}{}:
			default:
			}

			writer.Header().Set("Location", fmt.Sprintf("/tasks/%d", pendingTaskID))
			writer.WriteHeader(http.StatusFound)
		case request.Method == http.MethodGet && request.URL.Path == fmt.Sprintf("/tasks/%d", adoptedTaskID):
			processingTask(writer, adoptedTaskID, adoptedDeployment)
		case request.Method == http.MethodGet && request.URL.Path == fmt.Sprintf("/tasks/%d", pendingTaskID):
			processingTask(writer, pendingTaskID, pendingDeployment)
		default:
			return false
		}

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

// deploysSent counts the deploys the test director received.
func deploysSent(director *boshtest.Director) int {
	count := 0

	for _, request := range director.APIRequests() {
		if request.Method == http.MethodPost && request.Path == "/deployments" {
			count++
		}
	}

	return count
}

func TestResumeNeverDeploysAboveThePersistedLimit(t *testing.T) {
	t.Parallel()

	deployed := make(chan struct{}, 1)
	director, adapter := limitTestDirector(t, deployed)

	batch := bosh.NewBatchDirector(adapter, 10, time.Minute, nil)
	t.Cleanup(batch.Stop)

	vault := newMemoryVault()
	vault.store(t, "upgrade-settings", upgrade.Settings{MaxBatchJobs: 1})

	storeUpgrades(t, vault,
		runningUpgrade("upgrade-a", upgrade.InstanceUpgrade{
			InstanceID:     "instance-a",
			DeploymentName: adoptedDeployment,
			Status:         upgrade.InstanceStatusInterrupted,
			BOSHTaskID:     adoptedTaskID,
		}),
		runningUpgrade("upgrade-b", upgrade.InstanceUpgrade{
			InstanceID:     "instance-b",
			DeploymentName: pendingDeployment,
			Status:         upgrade.InstanceStatusPending,
		}))

	// The callback gives a resume that runs before the limit applies the
	// time to send its deploy, and only then applies the limit.
	handler := upgradehandler.NewHandler(upgradehandler.Dependencies{
		Logger:   &logger.NoOpLogger{},
		Director: batch,
		Vault:    vault,
		OnMaxBatchJobsChanged: func(maxJobs int) {
			select {
			case <-deployed:
			case <-time.After(deployWait):
			}

			batch.UpdateMaxBatchJobs(maxJobs)
		},
	})
	t.Cleanup(handler.Stop)

	// Upgrade B's deploy waits for the one slot that A's adopted deploy
	// holds.
	deadline := time.Now().Add(settleTimeout)

	for {
		inFlight, waiting, limit := batch.GetBatchPoolStats()
		if inFlight == 1 && waiting == 1 && limit == 1 {
			break
		}

		if time.Now().After(deadline) {
			t.Fatalf("expected A's deploy in flight and B's waiting under a limit of 1, got %d in flight, %d waiting, and a limit of %d, with %d deploys sent",
				inFlight, waiting, limit, deploysSent(director))
		}

		time.Sleep(10 * time.Millisecond)
	}

	if sent := deploysSent(director); sent != 0 {
		t.Fatalf("expected no deploy sent while A's deploy runs, got %d", sent)
	}
}
