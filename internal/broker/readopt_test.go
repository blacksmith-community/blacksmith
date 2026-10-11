package broker_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/broker"
	"blacksmith/internal/config"
	"blacksmith/internal/services"
	internalVault "blacksmith/internal/vault"
	"blacksmith/pkg/testutil"

	"github.com/fivetwenty-io/boshapi/pkg/boshapi"
	"github.com/fivetwenty-io/osbapi/v2/pkg/osbapi"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

// errReadoptVaultDown is the answer of a Vault that cannot be reached.
var errReadoptVaultDown = errors.New("vault answered 503 Service Unavailable")

// readoptTaskNotFound is the director's answer for a task it has no
// record of, such as one it has pruned.
func readoptTaskNotFound(taskID int) error {
	return &boshapi.APIError{
		StatusCode:  http.StatusNotFound,
		Method:      http.MethodGet,
		Path:        fmt.Sprintf("/tasks/%d", taskID),
		Code:        boshapi.ErrorCodeTaskNotFound,
		Description: fmt.Sprintf("Task %d could not be found", taskID),
	}
}

// The keys of the index entries, metadata, upgrade tasks, and plan
// credentials the specs write, and the ID and address of the
// deployment's VM.
const (
	indexPlanIDKey          = "plan_id"
	upgradeStatusKey        = "status"
	organizationGUIDKey     = "organization_guid"
	spaceGUIDKey            = "space_guid"
	detailsKey              = "details"
	credentialsKey          = "credentials"
	createdAtKey            = "created_at"
	provisionRequestedAtKey = "provision_requested_at"
	deleteRequestedAtKey    = "delete_requested_at"
	credentialsHostKey      = "host"
	readoptVMID             = "vm-0"
	readoptVMAddress        = "10.0.0.10"
)

// secretFolder is the folder at the root of the mount that holds the
// broker's registrations.
const secretFolder = "secret"

// failingReadoptStore is the re-adopter's Vault seam over a real Vault.
// It fails a given number of listings and then a given number of record
// reads. Each failing call reports itself on failed and waits for proceed,
// so a spec can look at the broker while the failure lasts.
type failingReadoptStore struct {
	*internalVault.Vault

	failed   chan string
	proceed  chan struct{}
	released chan struct{}

	mu            sync.Mutex
	listFailures  int
	stateFailures int
}

func (s *failingReadoptStore) ListOperationRecordIDs(ctx context.Context) ([]string, bool, error) {
	s.mu.Lock()
	failing := s.listFailures > 0

	if failing {
		s.listFailures--
	}
	s.mu.Unlock()

	if failing {
		s.fail("list")

		return nil, false, errReadoptVaultDown
	}

	return s.Vault.ListOperationRecordIDs(ctx) //nolint:wrapcheck // the double passes the Vault's own error through
}

func (s *failingReadoptStore) State(ctx context.Context, instanceID string) (string, int, map[string]interface{}, error) {
	s.mu.Lock()
	failing := s.listFailures == 0 && s.stateFailures > 0

	if failing {
		s.stateFailures--
	}
	s.mu.Unlock()

	if failing {
		s.fail("read " + instanceID)

		return "", 0, nil, errReadoptVaultDown
	}

	return s.Vault.State(ctx, instanceID) //nolint:wrapcheck // the double passes the Vault's own error through
}

// fail reports the failing call on failed and waits until the spec lets
// it go on or ends.
func (s *failingReadoptStore) fail(call string) {
	s.failed <- call

	select {
	case <-s.proceed:
	case <-s.released:
	}
}

// errShieldDown is the answer of a SHIELD that cannot be reached.
var errShieldDown = errors.New("shield answered 502 Bad Gateway")

// failingShield is a SHIELD whose every schedule fails.
type failingShield struct {
	recordingShield
}

func (s *failingShield) CreateSchedule(string, string, string, string, interface{}) error {
	return errShieldDown
}

// A broker that restarts finds the operations its previous run left in
// flight in Vault, claims them before it serves a request, and settles
// them as LastOperation would.
var _ = Describe("Re-adopting service operations at boot", func() {
	const (
		serviceID    = "valkey"
		planID       = "standalone"
		orgGUID      = "org-1"
		spaceGUID    = "space-1"
		createTaskID = 401
		deleteTaskID = 402
		cleanupTask  = 490
		otherTask    = 491
		contextID    = "context-readopt"
	)

	var (
		ctx            context.Context
		vaultServer    *testutil.VaultDevServer
		vaultClient    *internalVault.Vault
		director       *testutil.ScriptedBOSHDirector
		world          *scriptedTasks
		contextTasks   sync.Map
		monitor        *recordingVMMonitor
		backups        *recordingShield
		plan           services.Plan
		scriptDir      string
		released       chan struct{}
		stops          []context.CancelFunc
		pools          []*bosh.PooledDirector
		restore        []func()
		instanceID     string
		deploymentName string
	)

	directorMethods := []string{
		"GetDeployment", "GetDeploymentVMs", directorCreateDeployment, directorDeleteDeployment, "GetTask",
		directorWaitForTask, "GetEvents", "FindRunningTaskForDeployment", "FindTaskByContextID",
	}

	directorCalls := func() int {
		total := 0
		for _, method := range directorMethods {
			total += len(director.Calls(method))
		}

		return total
	}

	newBroker := func(boshDirector bosh.Director) *broker.Broker {
		serviceCtx, stop := context.WithCancel(ctx)
		stops = append(stops, stop)

		brokerInstance := &broker.Broker{
			Plans:          map[string]services.Plan{serviceID + "/" + planID: plan},
			BOSH:           boshDirector,
			Vault:          vaultClient,
			Shield:         backups,
			Config:         &config.Config{},
			InstanceLocks:  make(map[string]*sync.Mutex),
			ServiceContext: serviceCtx,
		}
		brokerInstance.SetVMMonitor(monitor)

		return brokerInstance
	}

	boot := func(boshDirector bosh.Director) *broker.Broker {
		brokerInstance := newBroker(boshDirector)
		Expect(brokerInstance.ReadoptInFlightOperations(ctx)).To(Succeed())

		return brokerInstance
	}

	newPool := func(limit int) *bosh.PooledDirector {
		pool := bosh.NewPooledDirector(director, limit, 10*time.Second, nil)
		pools = append(pools, pool)

		return pool
	}

	// indexInstance indexes the instance as an accepted provision leaves
	// it, with its provision details stored, and with the extra fields.
	indexInstance := func(extra map[string]interface{}) {
		entry := map[string]interface{}{
			serviceIDKey:        serviceID,
			indexPlanIDKey:      planID,
			organizationGUIDKey: orgGUID,
			spaceGUIDKey:        spaceGUID,
			deploymentNameKey:   deploymentName,
		}
		for key, value := range extra {
			entry[key] = value
		}

		Expect(vaultClient.Index(ctx, instanceID, entry)).To(Succeed())
		Expect(vaultClient.Put(ctx, instanceID+"/"+deploymentName, map[string]interface{}{
			detailsKey: map[string]interface{}{
				serviceIDKey: serviceID, indexPlanIDKey: planID,
				organizationGUIDKey: orgGUID, spaceGUIDKey: spaceGUID,
			},
		})).To(Succeed())
	}

	recordProvision := func(taskID int) {
		Expect(vaultClient.TrackProgressWithContextID(ctx, instanceID, outcomeProvision, "Starting provisioning process", contextID, 0, nil)).To(Succeed())

		if taskID != 0 {
			Expect(vaultClient.TrackProgress(ctx, instanceID, outcomeProvision, fmt.Sprintf("BOSH deployment in progress (task %d)", taskID), taskID, nil)).To(Succeed())
		}
	}

	recordDeprovision := func(taskID int) {
		Expect(vaultClient.TrackProgressWithDeployment(ctx, instanceID, outcomeDeprovision, "Starting deprovisioning process", contextID, deploymentName, 0, nil)).To(Succeed())

		if taskID != 0 {
			Expect(vaultClient.TrackProgress(ctx, instanceID, outcomeDeprovision, fmt.Sprintf("BOSH deletion in progress (task %d)", taskID), taskID, nil)).To(Succeed())
		}
	}

	recordState := func() string {
		state, _, _, err := vaultClient.State(ctx, instanceID)
		Expect(err).ToNot(HaveOccurred())

		return state
	}

	recorded := func() broker.RecordedOperation {
		operation, err := newBroker(director).RecordedOperation(ctx, instanceID)
		Expect(err).ToNot(HaveOccurred())

		return operation
	}

	indexed := func() bool {
		_, exists, err := vaultClient.FindInstance(ctx, instanceID)
		Expect(err).ToNot(HaveOccurred())

		return exists
	}

	// createOther waits until the pool counts the adopted task, then
	// starts a pooled deploy of another deployment, and closes the channel
	// it returns once the pool has let that deploy through.
	createOther := func(pool *bosh.PooledDirector) chan struct{} {
		Eventually(func() int32 {
			stats, err := pool.GetPoolStats()
			Expect(err).ToNot(HaveOccurred())

			return stats.ActiveConnections
		}, 10*time.Second).Should(Equal(int32(1)), "the pool must count the adopted task")

		created := make(chan struct{})

		go func() {
			defer GinkgoRecover()
			defer close(created)

			_, err := pool.CreateDeployment(ctx, "name: other-deployment\n")
			Expect(err).ToNot(HaveOccurred())
		}()

		return created
	}

	BeforeEach(func() {
		ctx = context.Background()
		stops, pools = nil, nil
		released = make(chan struct{})
		contextTasks = sync.Map{}
		instanceID = fmt.Sprintf("readopt-%d", time.Now().UnixNano())
		deploymentName = planID + "-" + instanceID

		// Each spec gets a Vault of its own, because re-adoption lists
		// the whole mount.
		var err error

		vaultServer, err = testutil.NewVaultDevServer(nil)
		Expect(err).ToNot(HaveOccurred())

		vaultClient = internalVault.New(vaultServer.Addr, vaultServer.RootToken, true)

		world = &scriptedTasks{tasks: map[int]*bosh.Task{
			cleanupTask: {ID: cleanupTask, State: outcomeTaskDone, Description: outcomeDeleteDeployment},
			otherTask:   {ID: otherTask, State: outcomeTaskDone, Description: outcomeCreateDeployment},
		}}
		specWorld, specReleased := world, released

		director = testutil.NewScriptedBOSHDirector()
		director.GetTaskFn = func(taskID int) (*bosh.Task, error) {
			task, ok := specWorld.get(taskID)
			if !ok {
				return nil, readoptTaskNotFound(taskID)
			}

			return task, nil
		}
		director.WaitForTaskFn = func(taskID int) (*bosh.Task, error) {
			for {
				task, ok := specWorld.get(taskID)
				if !ok {
					return nil, readoptTaskNotFound(taskID)
				}

				if task.State == outcomeTaskDone || task.State == outcomeTaskError || task.State == "cancelled" || task.State == "timeout" {
					return task, nil
				}

				select {
				case <-specReleased:
					return nil, fmt.Errorf("the spec ended while task %d ran", taskID) //nolint:err113 // test double
				case <-time.After(5 * time.Millisecond):
				}
			}
		}
		director.GetDeploymentFn = func(name string) (*bosh.DeploymentDetail, error) {
			if specWorld.isDeploymentDeleted() {
				return nil, fmt.Errorf("%w: %s", bosh.ErrDeploymentNotFound, name)
			}

			return &bosh.DeploymentDetail{Name: name, Manifest: "name: " + name}, nil
		}
		director.GetEventsFn = func(string) ([]bosh.Event, error) { return specWorld.listEvents(), nil }
		director.FindRunningTaskForDeploymentFn = func(string) (*bosh.Task, error) { return nil, nil } //nolint:nilnil // no running task is a nil task without an error
		director.FindTaskByContextIDFn = func(id string) (*bosh.Task, error) {
			taskID, found := contextTasks.Load(id)
			if !found {
				return nil, nil //nolint:nilnil // no task with the context ID is a nil task without an error
			}

			task, _ := specWorld.get(taskID.(int)) //nolint:forcetypeassert // the spec stores only task IDs

			return task, nil
		}
		director.GetDeploymentVMsFn = func(string) ([]bosh.VM, error) {
			return []bosh.VM{{ID: readoptVMID, Job: planID, Index: 0, State: "started", IPs: []string{readoptVMAddress}}}, nil
		}
		director.DeleteDeploymentFn = func(string) (*bosh.Task, error) {
			return &bosh.Task{ID: cleanupTask, State: outcomeTaskDone, Description: outcomeDeleteDeployment}, nil
		}
		director.CreateDeploymentFn = func(string) (*bosh.Task, error) {
			return &bosh.Task{ID: otherTask, State: outcomeTaskDone, Description: outcomeCreateDeployment}, nil
		}

		dir, err := os.MkdirTemp("", "blacksmith-readopt")
		Expect(err).ToNot(HaveOccurred())

		scriptDir = dir
		script := filepath.Join(dir, "init")
		Expect(os.WriteFile(script, []byte("#!/bin/bash\nexit 0\n"), 0o600)).To(Succeed())

		plan = services.Plan{
			ID:             planID,
			Name:           planID,
			Type:           serviceID,
			InitScriptPath: script,
			Manifest: map[interface{}]interface{}{
				manifestNameKey:           outcomeManifestName,
				manifestInstanceGroupsKey: []interface{}{},
			},
			Credentials: map[interface{}]interface{}{
				credentialsKey: map[interface{}]interface{}{credentialsHostKey: readoptVMAddress},
			},
			Service: &services.Service{ID: serviceID, Name: serviceID},
		}

		monitor = &recordingVMMonitor{}
		backups = &recordingShield{}

		restore = []func(){broker.SetReadoptIntervals(20 * time.Millisecond)}
	})

	AfterEach(func() {
		close(released)

		for _, stop := range stops {
			stop()
		}

		expectNoBrokerGoroutines()

		for _, pool := range pools {
			pool.Stop()
		}

		for _, undo := range restore {
			undo()
		}

		vaultServer.Close()

		_ = os.RemoveAll(scriptDir)
	})

	It("boot re-adoption settles a deprovision whose task finished while the broker was down", func() {
		indexInstance(map[string]interface{}{createdAtKey: time.Now().Add(-time.Hour).Format(time.RFC3339)})
		recordDeprovision(deleteTaskID)
		world.set(&bosh.Task{ID: deleteTaskID, State: outcomeTaskDone, Description: outcomeDeleteDeployment})
		world.markDeploymentDeleted()

		brokerInstance := boot(director)

		Eventually(indexed, 10*time.Second).Should(BeFalse())
		Eventually(func() bool { return brokerInstance.OperationClaimed(instanceID) }, 10*time.Second).Should(BeFalse())
		Expect(director.Calls(directorDeleteDeployment)).To(BeEmpty(), "re-adoption never sends a delete")
	})

	It("boot re-adoption recovers a deprovision recorded without a task", func() {
		indexInstance(nil)
		recordDeprovision(0)
		world.set(&bosh.Task{ID: deleteTaskID, State: outcomeTaskDone, Description: outcomeDeleteDeployment})
		world.addEvents(deploymentEvents(boshEventDelete, deploymentName, deleteTaskID)...)
		world.markDeploymentDeleted()

		brokerInstance := boot(director)

		Eventually(indexed, 10*time.Second).Should(BeFalse())
		Expect(recorded().TaskID).To(Equal(deleteTaskID))
		Eventually(func() bool { return brokerInstance.OperationClaimed(instanceID) }, 10*time.Second).Should(BeFalse())
	})

	It("boot re-adoption resolves an unknown outcome by context ID", func() {
		indexInstance(nil)
		Expect(vaultClient.TrackOutcomeUnknown(ctx, instanceID, outcomeDeprovision, "The answer to the delete was lost", contextID, time.Now().Add(-time.Minute))).To(Succeed())
		world.set(&bosh.Task{ID: deleteTaskID, State: outcomeTaskDone, Description: outcomeDeleteDeployment})
		contextTasks.Store(contextID, deleteTaskID)
		world.markDeploymentDeleted()

		brokerInstance := boot(director)

		Eventually(indexed, 10*time.Second).Should(BeFalse())
		Expect(recorded().TaskID).To(Equal(deleteTaskID))
		Expect(director.Calls("FindTaskByContextID")).ToNot(BeEmpty())
		Eventually(func() bool { return brokerInstance.OperationClaimed(instanceID) }, 10*time.Second).Should(BeFalse())
	})

	It("boot re-adoption counts an adopted delete against the pool", func() {
		indexInstance(nil)
		recordDeprovision(deleteTaskID)
		world.set(&bosh.Task{ID: deleteTaskID, State: outcomeTaskProcessing, Description: outcomeDeleteDeployment})

		pool := newPool(1)
		boot(pool)

		created := createOther(pool)
		Consistently(created, 300*time.Millisecond).ShouldNot(BeClosed(), "a pooled mutation must wait while the adopted delete runs")
		Expect(director.Calls(directorCreateDeployment)).To(BeEmpty())

		world.set(&bosh.Task{ID: deleteTaskID, State: outcomeTaskDone, Description: outcomeDeleteDeployment})
		world.markDeploymentDeleted()

		Eventually(created, 10*time.Second).Should(BeClosed())
		Eventually(indexed, 10*time.Second).Should(BeFalse())
	})

	It("boot re-adoption counts a create task found in the deployment's events against the pool at once", func() {
		// No pass waits on the retry interval before the found task counts.
		defer broker.SetReadoptIntervals(time.Hour)()

		indexInstance(nil)
		recordProvision(0)
		world.set(&bosh.Task{ID: createTaskID, State: outcomeTaskProcessing, Description: outcomeCreateDeployment})
		world.addEvents(deploymentEvents(boshEventCreate, deploymentName, createTaskID)...)

		pool := newPool(1)
		boot(pool)

		created := createOther(pool)
		Consistently(created, 300*time.Millisecond).ShouldNot(BeClosed(), "a pooled mutation must wait while the recovered create task runs")

		world.set(&bosh.Task{ID: createTaskID, State: outcomeTaskDone, Description: outcomeCreateDeployment})

		Eventually(created, 10*time.Second).Should(BeClosed())
		Eventually(recordState, 10*time.Second).Should(Equal(internalVault.TaskStateSucceeded))
	})

	It("boot re-adoption counts a task running on the deployment with no event against the pool", func() {
		indexInstance(nil)
		recordProvision(0)
		world.set(&bosh.Task{ID: createTaskID, State: outcomeTaskProcessing, Description: outcomeCreateDeployment})

		specWorld := world
		director.FindRunningTaskForDeploymentFn = func(string) (*bosh.Task, error) {
			task, ok := specWorld.get(createTaskID)
			if !ok || task.State != outcomeTaskProcessing {
				return nil, nil //nolint:nilnil // no running task is a nil task without an error
			}

			return task, nil
		}

		pool := newPool(1)
		brokerInstance := boot(pool)

		created := createOther(pool)
		Consistently(created, 300*time.Millisecond).ShouldNot(BeClosed(), "a pooled mutation must wait while the task on the deployment runs")

		world.set(&bosh.Task{ID: createTaskID, State: outcomeTaskDone, Description: outcomeCreateDeployment})

		Eventually(created, 10*time.Second).Should(BeClosed())
		Eventually(recordState, 10*time.Second).Should(Equal("failed"))
		Eventually(func() bool { return brokerInstance.OperationClaimed(instanceID) }, 10*time.Second).Should(BeFalse())
	})

	It("boot re-adoption counts the task the resolver finds by context ID against the pool at once", func() {
		// No pass waits on the retry interval before the found task counts.
		defer broker.SetReadoptIntervals(time.Hour)()

		indexInstance(nil)
		Expect(vaultClient.TrackOutcomeUnknown(ctx, instanceID, outcomeProvision, "The answer to the deploy was lost", contextID, time.Now().Add(-time.Minute))).To(Succeed())
		world.set(&bosh.Task{ID: createTaskID, State: outcomeTaskProcessing, Description: outcomeCreateDeployment})
		contextTasks.Store(contextID, createTaskID)

		pool := newPool(1)
		boot(pool)

		created := createOther(pool)
		Consistently(created, 300*time.Millisecond).ShouldNot(BeClosed(), "a pooled mutation must wait while the found create task runs")

		world.set(&bosh.Task{ID: createTaskID, State: outcomeTaskDone, Description: outcomeCreateDeployment})

		Eventually(created, 10*time.Second).Should(BeClosed())
		Eventually(recordState, 10*time.Second).Should(Equal(internalVault.TaskStateSucceeded))
	})

	It("ReadoptInFlightOperations holds every claim when it returns", func() {
		provisionID, deprovisionID, unknownID := instanceID+"-p", instanceID+"-d", instanceID+"-u"

		for _, id := range []string{provisionID, deprovisionID, unknownID} {
			Expect(vaultClient.Index(ctx, id, map[string]interface{}{
				serviceIDKey: serviceID, indexPlanIDKey: planID, deploymentNameKey: planID + "-" + id,
			})).To(Succeed())
		}

		Expect(vaultClient.TrackProgress(ctx, provisionID, outcomeProvision, "BOSH deployment in progress (task 401)", createTaskID, nil)).To(Succeed())
		Expect(vaultClient.TrackProgressWithDeployment(ctx, deprovisionID, outcomeDeprovision, "BOSH deletion in progress (task 402)", contextID, planID+"-"+deprovisionID, deleteTaskID, nil)).To(Succeed())
		Expect(vaultClient.TrackOutcomeUnknown(ctx, unknownID, outcomeProvision, "The answer to the deploy was lost", contextID+"-u", time.Now())).To(Succeed())
		world.set(&bosh.Task{ID: createTaskID, State: outcomeTaskProcessing, Description: outcomeCreateDeployment})
		world.set(&bosh.Task{ID: deleteTaskID, State: outcomeTaskProcessing, Description: outcomeDeleteDeployment})

		brokerInstance := boot(director)

		Expect(brokerInstance.ProvisionActive(provisionID)).To(BeTrue())
		Expect(brokerInstance.DeprovisionActive(deprovisionID)).To(BeTrue())
		Expect(brokerInstance.ProvisionActive(unknownID)).To(BeTrue())
	})

	It("a repeated provision is accepted while boot re-adoption owns the instance", func() {
		indexInstance(nil)
		recordProvision(createTaskID)
		world.set(&bosh.Task{ID: createTaskID, State: outcomeTaskProcessing, Description: outcomeCreateDeployment})

		brokerInstance := boot(director)
		Expect(brokerInstance.ProvisionActive(instanceID)).To(BeTrue())

		response, async, err := brokerInstance.Provision(ctx, instanceID, osbapi.ProvisionRequest{
			ServiceID: serviceID, PlanID: planID, OrganizationGUID: orgGUID, SpaceGUID: spaceGUID,
		}, true)
		Expect(err).ToNot(HaveOccurred())
		Expect(async).To(BeTrue())
		Expect(response.Operation).To(Equal(outcomeProvision))
		Expect(director.Calls(directorCreateDeployment)).To(BeEmpty())
	})

	It("a provision with different attributes gets 409 while boot re-adoption owns the instance", func() {
		indexInstance(nil)
		recordProvision(createTaskID)
		world.set(&bosh.Task{ID: createTaskID, State: outcomeTaskProcessing, Description: outcomeCreateDeployment})

		brokerInstance := boot(director)
		Expect(brokerInstance.ProvisionActive(instanceID)).To(BeTrue())

		_, _, err := brokerInstance.Provision(ctx, instanceID, osbapi.ProvisionRequest{
			ServiceID: serviceID, PlanID: planID, OrganizationGUID: orgGUID, SpaceGUID: "space-2",
		}, true)
		Expect(err).To(MatchError(osbapi.ErrInstanceAlreadyExists))
		Expect(director.Calls(directorCreateDeployment)).To(BeEmpty())
	})

	It("a deprovision is refused while boot re-adoption owns a provision", func() {
		indexInstance(nil)
		recordProvision(createTaskID)
		world.set(&bosh.Task{ID: createTaskID, State: outcomeTaskProcessing, Description: outcomeCreateDeployment})

		brokerInstance := boot(director)
		Expect(brokerInstance.ProvisionActive(instanceID)).To(BeTrue())

		_, _, err := brokerInstance.Deprovision(ctx, instanceID, osbapi.DeprovisionRequest{ServiceID: serviceID, PlanID: planID}, true)
		Expect(err).To(MatchError(osbapi.ErrConcurrencyError))
		Expect(director.Calls(directorDeleteDeployment)).To(BeEmpty())
	})

	It("boot re-adoption and LastOperation settle a deprovision once", func() {
		instanceID = cleanupGUID()
		deploymentName = planID + "-" + instanceID

		indexInstance(nil)
		recordDeprovision(deleteTaskID)
		world.set(&bosh.Task{ID: deleteTaskID, State: outcomeTaskDone, Description: outcomeDeleteDeployment})
		world.markDeploymentDeleted()

		// Both settlers read the finished task before they settle. The
		// barrier lets each in once the other arrives, and a lone one in
		// after a second.
		barrier := newSettleBarrier(2, time.Second)
		specWorld := world
		director.GetTaskFn = func(taskID int) (*bosh.Task, error) {
			barrier.Arrive()

			task, ok := specWorld.get(taskID)
			if !ok {
				return nil, readoptTaskNotFound(taskID)
			}

			return task, nil
		}

		cleaner := newRecordingCleaner(cleanerSucceeds)
		brokerInstance := newBroker(director)
		brokerInstance.CredentialCleaner = cleaner

		Expect(brokerInstance.ReadoptInFlightOperations(ctx)).To(Succeed())

		response, err := brokerInstance.LastOperation(ctx, instanceID, osbapi.LastOperationRequest{
			ServiceID: serviceID, PlanID: planID, Operation: outcomeDeprovision,
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(response.State).To(Equal(osbapi.StateSucceeded))

		Eventually(func() bool { return brokerInstance.OperationClaimed(instanceID) }, 10*time.Second).Should(BeFalse())
		brokerInstance.WaitForCredentialCleanups()

		Expect(cleaner.Targets()).To(HaveLen(1), "the confirmed delete of deployment %s must clean its credentials once", deploymentName)
		Expect(indexed()).To(BeFalse())
	})

	It("boot re-adoption records an interrupted provision once", func() {
		indexInstance(nil)
		recordProvision(0)

		first := boot(director)

		Eventually(recordState, 10*time.Second).Should(Equal("failed"))
		Eventually(func() bool { return first.OperationClaimed(instanceID) }, 10*time.Second).Should(BeFalse())

		lookups := len(director.Calls("GetEvents"))
		Expect(lookups).To(Equal(1))

		second := boot(director)
		Expect(second.OperationClaimed(instanceID)).To(BeFalse())
		Consistently(func() int { return len(director.Calls("GetEvents")) }, 200*time.Millisecond).Should(Equal(lookups), "the second boot must not look the task up again")
	})

	It("boot re-adoption gives up after its bound and releases its claim", func() {
		defer broker.SetReadoptBound(300 * time.Millisecond)()

		indexInstance(nil)
		recordDeprovision(deleteTaskID)

		director.WaitForTaskFn = func(int) (*bosh.Task, error) { return nil, errReadoptVaultDown }
		director.GetTaskFn = func(int) (*bosh.Task, error) { return nil, errReadoptVaultDown }

		brokerInstance := boot(director)
		Expect(brokerInstance.DeprovisionActive(instanceID)).To(BeTrue())

		Eventually(func() bool { return brokerInstance.OperationClaimed(instanceID) }, 10*time.Second).Should(BeFalse())

		operation := recorded()
		Expect(operation.State).To(Equal("in_progress"))
		Expect(operation.TaskID).To(Equal(deleteTaskID))
		Expect(indexed()).To(BeTrue())
	})

	It("boot re-adoption settles an old provision that has created_at and stored credentials without calling the director", func() {
		indexInstance(map[string]interface{}{createdAtKey: time.Now().Add(-24 * time.Hour).Format(time.RFC3339)})
		Expect(vaultClient.Put(ctx, instanceID+"/"+credentialsKey, map[string]interface{}{credentialsHostKey: readoptVMAddress})).To(Succeed())
		recordProvision(createTaskID)

		brokerInstance := boot(director)
		Expect(brokerInstance.OperationClaimed(instanceID)).To(BeFalse())

		Eventually(recordState, 10*time.Second).Should(Equal(internalVault.TaskStateSucceeded))
		Expect(recorded().TaskID).To(Equal(createTaskID))
		Expect(directorCalls()).To(BeZero())
		Expect(monitor.Added()).To(BeEmpty())
	})

	It("boot re-adoption runs the completion steps of an old provision that has created_at and no stored credentials", func() {
		indexInstance(map[string]interface{}{createdAtKey: time.Now().Add(-24 * time.Hour).Format(time.RFC3339)})
		recordProvision(createTaskID)
		world.set(&bosh.Task{ID: createTaskID, State: outcomeTaskDone, Description: outcomeCreateDeployment})

		brokerInstance := boot(director)
		Expect(brokerInstance.ProvisionActive(instanceID)).To(BeTrue(), "a provision with no stored credentials must be re-adopted")

		Eventually(recordState, 10*time.Second).Should(Equal(internalVault.TaskStateSucceeded))
		Eventually(func() bool { return brokerInstance.OperationClaimed(instanceID) }, 10*time.Second).Should(BeFalse())

		Expect(monitor.Added()).To(Equal([]string{instanceID}))
		Expect(backups.Scheduled()).To(Equal([]string{instanceID}))

		var stored map[string]interface{}

		exists, err := vaultClient.Get(ctx, instanceID+"/"+credentialsKey, &stored)
		Expect(err).ToNot(HaveOccurred())
		Expect(exists).To(BeTrue(), "the completion steps must store the credentials")
	})

	It("boot re-adoption runs the completion steps again after they failed before a restart", func() {
		indexInstance(nil)
		recordProvision(createTaskID)
		world.set(&bosh.Task{ID: createTaskID, State: outcomeTaskDone, Description: outcomeCreateDeployment})

		// The first run's backup schedule fails after the credentials are
		// stored, and the broker then restarts.
		first := newBroker(director)
		first.Shield = &failingShield{}

		_, err := first.LastOperation(ctx, instanceID, osbapi.LastOperationRequest{
			ServiceID: serviceID, PlanID: planID, Operation: outcomeProvision,
		})
		Expect(err).To(MatchError(broker.ErrProvisionTaskCompletedPostHookFailed))
		Expect(recordState()).To(Equal("in_progress"))

		idx, err := vaultClient.GetIndex(ctx, "db")
		Expect(err).ToNot(HaveOccurred())

		entry, err := idx.Lookup(instanceID)
		Expect(err).ToNot(HaveOccurred())
		Expect(entry).ToNot(HaveKey(createdAtKey), "created_at must wait until every completion step has succeeded")

		repeat := osbapi.ProvisionRequest{ServiceID: serviceID, PlanID: planID, OrganizationGUID: orgGUID, SpaceGUID: spaceGUID}

		response, async, err := first.Provision(ctx, instanceID, repeat, true)
		Expect(err).ToNot(HaveOccurred(), "a repeat must not answer 200 before the completion steps finish")
		Expect(async).To(BeTrue())
		Expect(response.Operation).To(Equal(outcomeProvision))

		second := boot(director)

		Eventually(recordState, 10*time.Second).Should(Equal(internalVault.TaskStateSucceeded))
		Eventually(func() bool { return second.OperationClaimed(instanceID) }, 10*time.Second).Should(BeFalse())
		Expect(backups.Scheduled()).To(Equal([]string{instanceID}), "the completion steps must run again after the restart")

		idx, err = vaultClient.GetIndex(ctx, "db")
		Expect(err).ToNot(HaveOccurred())

		entry, err = idx.Lookup(instanceID)
		Expect(err).ToNot(HaveOccurred())
		Expect(entry).To(HaveKey(createdAtKey))

		_, _, err = second.Provision(ctx, instanceID, repeat, true)
		Expect(err).To(MatchError(osbapi.ErrInstanceAlreadyProvisioned), "a repeat answers 200 once the completion steps have finished")
		Expect(director.Calls(directorCreateDeployment)).To(BeEmpty())
	})

	It("boot re-adoption completes an old provision whose task was pruned once", func() {
		indexInstance(nil)
		recordProvision(createTaskID)

		brokerInstance := boot(director)

		Eventually(recordState, 10*time.Second).Should(Equal(internalVault.TaskStateSucceeded))
		Eventually(func() bool { return brokerInstance.OperationClaimed(instanceID) }, 10*time.Second).Should(BeFalse())

		Expect(monitor.Added()).To(Equal([]string{instanceID}))
		Expect(backups.Scheduled()).To(Equal([]string{instanceID}))

		response, err := brokerInstance.LastOperation(ctx, instanceID, osbapi.LastOperationRequest{
			ServiceID: serviceID, PlanID: planID, Operation: outcomeProvision,
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(response.State).To(Equal(osbapi.StateSucceeded))
		Expect(monitor.Added()).To(HaveLen(1), "the completion steps must run once")
	})

	It("boot re-adoption records the failure of an old provision whose task ended in error", func() {
		indexInstance(nil)
		recordProvision(createTaskID)
		world.set(&bosh.Task{ID: createTaskID, State: outcomeTaskError, Description: outcomeCreateDeployment})

		brokerInstance := boot(director)

		Eventually(recordState, 10*time.Second).Should(Equal("failed"))
		Eventually(func() bool { return brokerInstance.OperationClaimed(instanceID) }, 10*time.Second).Should(BeFalse())
		Expect(monitor.Added()).To(BeEmpty())
		Expect(backups.Scheduled()).To(BeEmpty())
	})

	It("boot re-adoption counts a delete without an index entry against the pool", func() {
		recordDeprovision(deleteTaskID)
		world.set(&bosh.Task{ID: deleteTaskID, State: outcomeTaskProcessing, Description: outcomeDeleteDeployment})

		Expect(vaultClient.Put(ctx, "upgrade-tasks/task-1", map[string]interface{}{upgradeStatusKey: "running"})).To(Succeed())
		Expect(vaultClient.Put(ctx, "blacksmith/settings", map[string]interface{}{"max_batch_jobs": 2})).To(Succeed())
		Expect(vaultClient.Put(ctx, "secret/blacksmith/registrations/r1", map[string]interface{}{manifestNameKey: "cf"})).To(Succeed())

		clearedID := instanceID + "-cleared"
		Expect(vaultClient.TrackProgressWithDeployment(ctx, clearedID, outcomeDeprovision, "Starting deprovisioning process", contextID+"-cleared", planID+"-"+clearedID, 0, nil)).To(Succeed())
		vaultClient.Clear(clearedID)

		pool := newPool(1)
		brokerInstance := boot(pool)

		Expect(brokerInstance.DeprovisionActive(instanceID)).To(BeTrue())

		for _, id := range []string{clearedID, "upgrade-tasks", "blacksmith", secretFolder} {
			Expect(brokerInstance.OperationClaimed(id)).To(BeFalse(), "%s must not be claimed", id)
		}

		created := createOther(pool)
		Consistently(created, 300*time.Millisecond).ShouldNot(BeClosed(), "a pooled mutation must wait while the adopted teardown delete runs")

		world.set(&bosh.Task{ID: deleteTaskID, State: outcomeTaskDone, Description: outcomeDeleteDeployment})
		world.markDeploymentDeleted()

		Eventually(created, 10*time.Second).Should(BeClosed())
		Eventually(recordState, 10*time.Second).Should(Equal(internalVault.TaskStateDeleted))
		Expect(director.Calls(directorDeleteDeployment)).To(BeEmpty())
	})

	It("boot re-adoption marks a delete without an index entry deleted once its deployment is gone", func() {
		recordDeprovision(0)
		world.markDeploymentDeleted()

		first := boot(director)

		Eventually(recordState, 10*time.Second).Should(Equal(internalVault.TaskStateDeleted))
		Eventually(func() bool { return first.OperationClaimed(instanceID) }, 10*time.Second).Should(BeFalse())

		calls := directorCalls()

		response, err := first.LastOperation(ctx, instanceID, osbapi.LastOperationRequest{
			ServiceID: serviceID, PlanID: planID, Operation: outcomeDeprovision,
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(response.State).To(Equal(osbapi.StateSucceeded))
		Expect(directorCalls()).To(Equal(calls), "LastOperation must answer from the record")

		second := boot(director)
		Expect(second.OperationClaimed(instanceID)).To(BeFalse())
		Consistently(directorCalls, 200*time.Millisecond).Should(Equal(calls), "the second boot must ask the director nothing")
	})

	It("boot re-adoption marks a confirmed delete deleted from its deleted_at without calling the director", func() {
		recordDeprovision(deleteTaskID)
		Expect(vaultClient.Put(ctx, instanceID+"/metadata", map[string]interface{}{
			provisionRequestedAtKey: time.Now().Add(-48 * time.Hour).Format(time.RFC3339),
			deleteRequestedAtKey:    time.Now().Add(-2 * time.Hour).Format(time.RFC3339),
			deletedAtKey:            time.Now().Add(-time.Hour).Format(time.RFC3339),
		})).To(Succeed())

		brokerInstance := boot(director)
		Expect(brokerInstance.OperationClaimed(instanceID)).To(BeFalse())

		Eventually(recordState, 10*time.Second).Should(Equal(internalVault.TaskStateDeleted))
		Expect(directorCalls()).To(BeZero())
	})

	It("boot re-adoption records the failure of a delete without an index entry whose task ended and left its deployment", func() {
		recordDeprovision(deleteTaskID)
		world.set(&bosh.Task{ID: deleteTaskID, State: outcomeTaskError, Description: outcomeDeleteDeployment})

		brokerInstance := boot(director)

		Eventually(recordState, 10*time.Second).Should(Equal("failed"))
		Eventually(func() bool { return brokerInstance.OperationClaimed(instanceID) }, 10*time.Second).Should(BeFalse())

		description := recorded().Description
		Expect(description).To(ContainSubstring(deploymentName))
		Expect(description).To(ContainSubstring(contextID))
		Expect(description).To(ContainSubstring(fmt.Sprintf("task %d", deleteTaskID)))
		Expect(description).To(ContainSubstring(outcomeTaskError))
		Expect(director.Calls(directorDeleteDeployment)).To(BeEmpty(), "re-adoption never sends a delete")
	})

	It("boot re-adoption follows the create task of a provision whose index entry is gone", func() {
		recordProvision(createTaskID)
		world.set(&bosh.Task{ID: createTaskID, State: outcomeTaskProcessing, Description: outcomeCreateDeployment})

		pool := newPool(1)
		brokerInstance := boot(pool)
		Expect(brokerInstance.ProvisionActive(instanceID)).To(BeTrue())

		created := createOther(pool)
		Consistently(created, 300*time.Millisecond).ShouldNot(BeClosed(), "a pooled mutation must wait while the adopted create task runs")

		world.set(&bosh.Task{ID: createTaskID, State: outcomeTaskDone, Description: outcomeCreateDeployment})

		Eventually(created, 10*time.Second).Should(BeClosed())
		Eventually(recordState, 10*time.Second).Should(Equal("failed"))
		Eventually(func() bool { return brokerInstance.OperationClaimed(instanceID) }, 10*time.Second).Should(BeFalse())
	})

	It("ReadoptInFlightOperations retries a failed listing and claims nothing until it succeeds", func() {
		recordDeprovision(deleteTaskID)
		world.set(&bosh.Task{ID: deleteTaskID, State: outcomeTaskProcessing, Description: outcomeDeleteDeployment})

		// The deployment check waits until the spec ends, so the claim the
		// teardown delete takes stays held while the spec looks at it.
		specReleased := released
		director.GetDeploymentFn = func(name string) (*bosh.DeploymentDetail, error) {
			<-specReleased

			return &bosh.DeploymentDetail{Name: name, Manifest: "name: " + name}, nil
		}

		store := &failingReadoptStore{
			Vault:         vaultClient,
			failed:        make(chan string, 4),
			proceed:       make(chan struct{}),
			released:      released,
			listFailures:  2,
			stateFailures: 1,
		}

		brokerInstance := newBroker(director)
		brokerInstance.SetReadoptStore(store)

		bootCtx, stopBoot := context.WithCancel(ctx)
		stops = append(stops, stopBoot)

		var returned atomic.Bool

		done := make(chan error, 1)

		go func() {
			err := brokerInstance.ReadoptInFlightOperations(bootCtx)

			returned.Store(true)

			done <- err
		}()

		for _, call := range []string{"list", "list", "read " + instanceID} {
			Eventually(store.failed, 10*time.Second).Should(Receive(Equal(call)))
			Consistently(returned.Load, 50*time.Millisecond).Should(BeFalse())
			Expect(brokerInstance.OperationClaimed(instanceID)).To(BeFalse(), "no claim may be held while the %s fails", call)

			store.proceed <- struct{}{}
		}

		Eventually(done, 10*time.Second).Should(Receive(BeNil()))
		Expect(brokerInstance.DeprovisionActive(instanceID)).To(BeTrue())
	})
})
