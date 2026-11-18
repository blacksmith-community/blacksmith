package broker_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"

	"blacksmith/internal/bosh"
	internalVault "blacksmith/internal/vault"
	"blacksmith/pkg/testutil"

	"github.com/fivetwenty-io/osbapi/v2/pkg/osbapi"
)

// The operation that lands on the record while a provision is settled.
const (
	racingUpdateContextID    = "racing-update-context"
	racingProvisionContextID = "racing-provision-context"
)

// racingVault passes every request through to a Vault server. Once armed,
// it lets skip reads of one instance's task record through, and before
// the next one it runs land, once. That puts land's write between the
// reads the broker has made and the read the broker's next write makes.
type racingVault struct {
	proxy    *httputil.ReverseProxy
	taskPath string
	land     func()

	mu     sync.Mutex
	armed  bool
	skip   int
	landed bool
	// refusing makes Vault refuse every write of the task record.
	refusing bool
}

func newRacingVault(t *testing.T, target, instanceID string, land func()) *httptest.Server {
	t.Helper()

	targetURL, err := url.Parse(target)
	if err != nil {
		t.Fatalf("failed to parse the Vault address: %v", err)
	}

	racing := &racingVault{proxy: httputil.NewSingleHostReverseProxy(targetURL), taskPath: "/" + instanceID + "/task", land: land}

	server := httptest.NewServer(racing)
	t.Cleanup(server.Close)

	racingVaults.Store(server.URL, racing)

	return server
}

//nolint:gochecknoglobals // maps a test's proxy address to its proxy
var racingVaults sync.Map

// armRacingVault arms the proxy at address to land its write after skip
// more reads of the task record.
func armRacingVault(address string, skip int) {
	value, _ := racingVaults.Load(address)

	racing, _ := value.(*racingVault)

	racing.mu.Lock()
	defer racing.mu.Unlock()

	racing.armed = true
	racing.skip = skip
}

// refuseTaskWrites makes the proxy at address refuse, or pass again,
// every write of the task record.
func refuseTaskWrites(address string, refuse bool) {
	value, _ := racingVaults.Load(address)

	racing, _ := value.(*racingVault)

	racing.mu.Lock()
	defer racing.mu.Unlock()

	racing.refusing = refuse
}

func (r *racingVault) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet && strings.HasSuffix(request.URL.Path, r.taskPath) && r.refuses() {
		http.Error(writer, `{"errors":["write refused by the test"]}`, http.StatusBadRequest)

		return
	}

	if request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, r.taskPath) && r.due() {
		r.land()
	}

	r.proxy.ServeHTTP(writer, request)
}

// refuses reports whether writes of the task record are refused.
func (r *racingVault) refuses() bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.refusing
}

// due reports whether the write lands before this read.
func (r *racingVault) due() bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	if !r.armed || r.landed {
		return false
	}

	if r.skip > 0 {
		r.skip--

		return false
	}

	r.landed = true

	return true
}

// racingRecordPath is a provision whose record another operation replaces
// while the provision is settled.
type racingRecordPath struct {
	name string
	// record writes the provision the instance holds.
	record func(ctx context.Context, t *testing.T, client *internalVault.Vault, instanceID string)
	// arm wires the director so the proxy arms at the right call.
	arm func(director *testutil.ScriptedBOSHDirector, address string)
	// settle runs the settle step. Without one, the step is a poll, which
	// must not answer failed.
	settle func(ctx context.Context, t *testing.T, failure *updateFailure, client *internalVault.Vault)
}

// recordProvisionTask records a provision accepted with
// racingProvisionContextID whose create task is running.
func recordProvisionTask(ctx context.Context, t *testing.T, client *internalVault.Vault, instanceID string) {
	t.Helper()

	err := client.TrackProgressWithContextID(ctx, instanceID, outcomeProvision, "Starting provisioning process", racingProvisionContextID, 0, nil)
	if err == nil {
		err = client.TrackProgress(ctx, instanceID, outcomeProvision, "BOSH deployment in progress (task "+strconv.Itoa(updateFailureCreateTaskID)+")", updateFailureCreateTaskID, nil)
	}

	if err != nil {
		t.Fatalf("failed to record the provision: %v", err)
	}
}

func racingRecordPaths() []racingRecordPath {
	return []racingRecordPath{
		// The poll reads the provision and its failed create task under
		// the settle lock, twice, and the update lands before the failure
		// is written.
		{"failure written after an update is accepted", recordProvisionTask, func(director *testutil.ScriptedBOSHDirector, address string) {
			getTask := director.GetTaskFn
			director.GetTaskFn = func(taskID int) (*bosh.Task, error) {
				armRacingVault(address, 2)

				return getTask(taskID)
			}
		}, nil},
		// The poll finds the provision's create task in the deployment's
		// events, and the update lands before that task is recorded.
		{"found task written after an update is accepted", func(ctx context.Context, t *testing.T, client *internalVault.Vault, instanceID string) {
			t.Helper()

			err := client.TrackProgressWithContextID(ctx, instanceID, outcomeProvision, "Starting provisioning process", racingProvisionContextID, 0, nil)
			if err != nil {
				t.Fatalf("failed to record the provision: %v", err)
			}
		}, func(director *testutil.ScriptedBOSHDirector, address string) {
			getEvents := director.GetEventsFn
			director.GetEventsFn = func(deployment string) ([]bosh.Event, error) {
				armRacingVault(address, 0)

				return getEvents(deployment)
			}
		}, nil},
		// Re-adoption follows the create task of a provision whose index
		// entry is gone and reads the record again under the settle lock,
		// which reads it twice, and the update lands before the failure
		// is written.
		{"re-adopted failure written after an update is accepted", recordProvisionTask, func(director *testutil.ScriptedBOSHDirector, address string) {
			waitForTask := director.WaitForTaskFn
			director.WaitForTaskFn = func(taskID int) (*bosh.Task, error) {
				armRacingVault(address, 2)

				return waitForTask(taskID)
			}
		}, func(ctx context.Context, t *testing.T, failure *updateFailure, client *internalVault.Vault) {
			t.Helper()

			err := client.Index(ctx, failure.instanceID, nil)
			if err != nil {
				t.Fatalf("failed to remove the index entry: %v", err)
			}

			err = failure.broker.ReadoptInFlightOperations(ctx)
			if err != nil {
				t.Fatalf("re-adoption failed: %v", err)
			}
		}},
	}
}

// A provision is settled from a record read under the settle lock, but
// an operation is accepted without that lock, so one can land on the
// record between that read and the provision's next write. Each write
// that settles the provision must check the record it replaces, or it
// records a failed provision over the newer operation and the cleanup
// deletes the deployment that operation is changing.
func TestProvisionSettle_LeavesARacingOperationAlone(t *testing.T) {
	t.Parallel()

	for index, path := range racingRecordPaths() {
		t.Run(path.name, func(t *testing.T) {
			t.Parallel()

			// Each subtest has a Vault of its own, because it proxies
			// Vault through a Vault value of its own, and the index lock
			// belongs to a Vault value.
			server, err := testutil.NewVaultDevServer(t)
			if err != nil {
				t.Fatalf("failed to start Vault: %v", err)
			}

			t.Cleanup(server.Close)

			ctx := t.Context()
			direct := internalVault.New(server.Addr, server.RootToken, true)
			instanceID := "update-failure-racing-" + strconv.Itoa(index)

			land := func() {
				landErr := direct.TrackProgressWithContextID(context.WithoutCancel(ctx), instanceID, updateFailureAction, "Update request accepted", racingUpdateContextID, 0, nil)
				if landErr != nil {
					t.Errorf("failed to land the update: %v", landErr)
				}
			}

			proxy := newRacingVault(t, server.Addr, instanceID, land)
			proxied := internalVault.New(proxy.URL, server.RootToken, true)

			failure := newUpdateFailure(ctx, t, proxied, "racing-"+strconv.Itoa(index))
			path.record(ctx, t, direct, instanceID)
			path.arm(failure.director, proxy.URL)

			if path.settle != nil {
				path.settle(ctx, t, failure, direct)
			} else {
				response, pollErr := failure.broker.LastOperation(ctx, instanceID, osbapi.LastOperationRequest{
					ServiceID: valkeyServiceID, PlanID: standalonePlanID, Operation: outcomeProvision,
				})
				if pollErr != nil {
					t.Fatalf("the poll failed: %v", pollErr)
				}

				if response.State == osbapi.StateFailed {
					t.Errorf("the poll answered failed over the update: %s", response.Description)
				}
			}

			failure.requireNothingDeleted(ctx, t, updateFailureAction)
		})
	}
}
