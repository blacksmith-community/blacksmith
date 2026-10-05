package broker_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/broker"
	"blacksmith/internal/config"
	"blacksmith/internal/credhub"
	"blacksmith/internal/services"
	internalVault "blacksmith/internal/vault"
	"blacksmith/pkg/logger"
	"blacksmith/pkg/testutil"
	vaultPkg "blacksmith/pkg/vault"

	"github.com/fivetwenty-io/osbapi/v2/pkg/osbapi"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var (
	errCleanupDirectorDown = errors.New("director answered 502 Bad Gateway")
	errCleanupDeleteDenied = errors.New("CredHub delete answered 500")
)

//nolint:gochecknoglobals // a counter that keeps every spec's instance GUID unique
var cleanupGUIDCounter atomic.Uint64

// cleanupGUID returns a GUID in the 8-4-4-4-12 form the guard requires.
func cleanupGUID() string {
	return fmt.Sprintf("%08x-0000-4000-8000-%012x", cleanupGUIDCounter.Add(1), time.Now().UnixNano()&0xffffffffffff)
}

// cleanerBehaviour is how a recording cleaner misbehaves.
type cleanerBehaviour string

const (
	cleanerSucceeds cleanerBehaviour = "succeeds"
	cleanerFails    cleanerBehaviour = "fails"
	cleanerBlocks   cleanerBehaviour = "blocks"
	cleanerPanics   cleanerBehaviour = "panics"
)

// recordingCleaner records every cleanup the broker asks for. It can fail,
// block until released, or panic.
type recordingCleaner struct {
	mu        sync.Mutex
	targets   []credhub.Target
	behaviour cleanerBehaviour
	release   chan struct{}
	once      sync.Once
}

func newRecordingCleaner(behaviour cleanerBehaviour) *recordingCleaner {
	return &recordingCleaner{behaviour: behaviour, release: make(chan struct{})}
}

func (c *recordingCleaner) CleanupDeployment(_ context.Context, target credhub.Target) credhub.Result {
	c.mu.Lock()
	c.targets = append(c.targets, target)
	c.mu.Unlock()

	switch c.behaviour {
	case cleanerBlocks:
		<-c.release
	case cleanerPanics:
		panic("cleaner exploded for " + target.DeploymentName)
	case cleanerFails:
		return credhub.Result{Target: target, Failed: []credhub.Failure{{Name: target.DeploymentName, Op: "delete", Err: errCleanupDeleteDenied}}}
	case cleanerSucceeds:
	}

	return credhub.Result{Target: target}
}

// Release unblocks a blocking cleaner. It is safe to call more than once.
func (c *recordingCleaner) Release() {
	c.once.Do(func() { close(c.release) })
}

func (c *recordingCleaner) Targets() []credhub.Target {
	c.mu.Lock()
	defer c.mu.Unlock()

	return append([]credhub.Target(nil), c.targets...)
}

// blockingCredHub is a CredHub fake for the real cleaner. FindByPath blocks
// until released and records how many finds run at once.
type blockingCredHub struct {
	mu        sync.Mutex
	finds     int
	active    int
	maxActive int
	deleted   []string
	names     []string
	release   chan struct{}
}

func (f *blockingCredHub) FindByPath(_ context.Context, _ string) ([]string, error) {
	f.mu.Lock()
	f.finds++
	f.active++
	f.maxActive = max(f.maxActive, f.active)
	f.mu.Unlock()

	<-f.release

	f.mu.Lock()
	f.active--
	f.mu.Unlock()

	return append([]string(nil), f.names...), nil
}

func (f *blockingCredHub) Delete(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.deleted = append(f.deleted, name)

	return nil
}

func (f *blockingCredHub) counts() (int, int, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.finds, f.maxActive, append([]string(nil), f.deleted...)
}

// resultRecorder wraps the real cleaner and records each run's result.
type resultRecorder struct {
	inner   *credhub.Cleaner
	mu      sync.Mutex
	results []credhub.Result
}

func (r *resultRecorder) CleanupDeployment(ctx context.Context, target credhub.Target) credhub.Result {
	result := r.inner.CleanupDeployment(ctx, target)

	r.mu.Lock()
	r.results = append(r.results, result)
	r.mu.Unlock()

	return result
}

func (r *resultRecorder) Results() []credhub.Result {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]credhub.Result(nil), r.results...)
}

func (r *resultRecorder) Duplicates() int {
	count := 0

	for _, result := range r.Results() {
		if result.Duplicate {
			count++
		}
	}

	return count
}

var _ = Describe("CredHub cleanup", func() {
	const (
		serviceID    = "valkey"
		planID       = "standalone"
		createTaskID = 189
		deleteTaskID = 233
		directorName = "lab-bosh"
	)

	var (
		ctx              context.Context
		brokerInstance   *broker.Broker
		vaultClient      *internalVault.Vault
		director         *testutil.ScriptedBOSHDirector
		instanceID       string
		deploymentName   string
		world            *scriptedTasks
		scriptDir        string
		releaseDelete    chan struct{}
		directorFails    atomic.Bool
		restorePoll      func()
		capture          *captureLogger
		previousLogger   logger.Logger
		cleanersToFinish []*recordingCleaner
	)

	indexEntryExists := func() bool {
		_, exists, err := vaultClient.FindInstance(ctx, instanceID)
		Expect(err).ToNot(HaveOccurred())

		return exists
	}

	recordedTask := func() int {
		_, taskID, _, err := vaultClient.State(ctx, instanceID)
		Expect(err).ToNot(HaveOccurred())

		return taskID
	}

	poll := func() osbapi.LastOperationResponse {
		response, err := brokerInstance.LastOperation(ctx, instanceID, osbapi.LastOperationRequest{
			ServiceID: serviceID, PlanID: planID, Operation: "deprovision",
		})
		Expect(err).ToNot(HaveOccurred())

		return response
	}

	deprovision := func() {
		_, async, err := brokerInstance.Deprovision(ctx, instanceID, osbapi.DeprovisionRequest{ServiceID: serviceID, PlanID: planID}, true)
		Expect(err).ToNot(HaveOccurred())
		Expect(async).To(BeTrue())
	}

	useCleaner := func(behaviour cleanerBehaviour) *recordingCleaner {
		cleaner := newRecordingCleaner(behaviour)
		cleanersToFinish = append(cleanersToFinish, cleaner)
		brokerInstance.CredentialCleaner = cleaner

		return cleaner
	}

	expectedTarget := func() credhub.Target {
		return credhub.Target{InstanceID: instanceID, DeploymentName: deploymentName}
	}

	BeforeEach(func() {
		ctx = context.Background()
		instanceID = cleanupGUID()
		deploymentName = planID + "-" + instanceID
		cleanersToFinish = nil
		restorePoll = func() {}

		directorFails.Store(false)

		capture = newCaptureLogger()
		previousLogger = logger.Get()

		logger.Set(capture)

		world = &scriptedTasks{tasks: map[int]*bosh.Task{
			createTaskID: {ID: createTaskID, State: "done", Description: "create deployment"},
		}}
		world.addEvents(deploymentEvents(boshEventCreate, deploymentName, createTaskID)...)
		specWorld := world

		director = testutil.NewScriptedBOSHDirector()
		vaultClient = internalVault.New(suite.vault.Addr, suite.vault.RootToken, true)

		director.GetEventsFn = func(string) ([]bosh.Event, error) { return specWorld.listEvents(), nil }
		director.GetTaskFn = func(taskID int) (*bosh.Task, error) {
			task, ok := specWorld.get(taskID)
			if !ok {
				return nil, fmt.Errorf("task %d not found", taskID) //nolint:err113 // test double
			}

			return task, nil
		}

		failing := &directorFails
		director.GetDeploymentFn = func(name string) (*bosh.DeploymentDetail, error) {
			if failing.Load() {
				return nil, errCleanupDirectorDown
			}

			if specWorld.isDeploymentDeleted() {
				return nil, fmt.Errorf("%w: %s", bosh.ErrDeploymentNotFound, name)
			}

			return &bosh.DeploymentDetail{Name: name, Manifest: "name: " + name}, nil
		}
		director.FindRunningTaskForDeploymentFn = func(string) (*bosh.Task, error) { return nil, nil } //nolint:nilnil // no running task is a nil task without an error
		director.GetInfoFn = func() (*bosh.Info, error) { return &bosh.Info{Name: directorName}, nil }

		releaseDelete = make(chan struct{})
		release, name := releaseDelete, deploymentName
		director.DeleteDeploymentFn = func(string) (*bosh.Task, error) {
			specWorld.addEvents(deploymentEvents(boshEventDelete, name, deleteTaskID)...)

			select {
			case <-release:
			case <-time.After(10 * time.Second):
			}

			specWorld.set(&bosh.Task{ID: deleteTaskID, State: "done", Description: "delete deployment " + name})
			specWorld.markDeploymentDeleted()

			return &bosh.Task{ID: deleteTaskID, State: "done", Description: "delete deployment " + name}, nil
		}

		dir, err := os.MkdirTemp("", "blacksmith-credhub-cleanup")
		Expect(err).ToNot(HaveOccurred())

		scriptDir = dir
		initScript := filepath.Join(dir, "init")
		Expect(os.WriteFile(initScript, []byte("#!/bin/bash\nexit 0\n"), 0o600)).To(Succeed())

		plan := services.Plan{
			ID: planID, Name: planID, Type: serviceID, InitScriptPath: initScript,
			Manifest: map[interface{}]interface{}{"name": "placeholder", "instance_groups": []interface{}{}},
			Service:  &services.Service{ID: serviceID, Name: serviceID},
		}

		brokerInstance = &broker.Broker{
			Plans:         map[string]services.Plan{serviceID + "/" + planID: plan},
			BOSH:          director,
			Vault:         vaultClient,
			Shield:        &recordingShield{},
			Config:        &config.Config{CredHub: config.CredHubConfig{DirectorName: directorName}},
			InstanceLocks: make(map[string]*sync.Mutex),
		}

		Expect(vaultClient.Index(ctx, instanceID, vaultPkg.Instance{
			ID: instanceID, ServiceID: serviceID, PlanID: planID, DeploymentName: deploymentName,
		})).To(Succeed())
		Expect(vaultClient.Put(ctx, instanceID+"/"+deploymentName, map[string]interface{}{
			"details": map[string]interface{}{"service_id": serviceID, "plan_id": planID},
		})).To(Succeed())
		Expect(vaultClient.TrackProgress(ctx, instanceID, "provision",
			fmt.Sprintf("BOSH deployment in progress (task %d)", createTaskID), createTaskID, nil)).To(Succeed())
	})

	AfterEach(func() {
		select {
		case <-releaseDelete:
		default:
			close(releaseDelete)
		}

		for _, cleaner := range cleanersToFinish {
			cleaner.Release()
		}

		Eventually(func() bool { return brokerInstance.DeprovisionActive(instanceID) }, 20*time.Second).Should(BeFalse())
		brokerInstance.WaitForCredentialCleanups()

		restorePoll()
		logger.Set(previousLogger)

		_ = vaultClient.Index(ctx, instanceID, nil)
		_ = os.RemoveAll(scriptDir)
	})

	// finishDeleteAndPoll releases the delete call, waits until the broker
	// records the delete task, and answers one LastOperation poll, timing it.
	finishDeleteAndPoll := func() (osbapi.LastOperationResponse, time.Duration) {
		deprovision()
		Eventually(func() []string { return director.Calls("DeleteDeployment") }).Should(Equal([]string{deploymentName}))
		close(releaseDelete)
		Eventually(recordedTask).Should(Equal(deleteTaskID))

		started := time.Now()
		response := poll()

		return response, time.Since(started)
	}

	Context("when LastOperation confirms the delete", func() {
		BeforeEach(func() {
			// The background monitor must not reach the deployment first.
			restorePoll = broker.SetTaskPollInterval(time.Hour)
		})

		It("hands the cleaner exactly the instance and its plan-derived deployment name", func() {
			cleaner := useCleaner(cleanerSucceeds)

			response, _ := finishDeleteAndPoll()

			Expect(response.State).To(Equal(osbapi.StateSucceeded))
			Eventually(cleaner.Targets).Should(Equal([]credhub.Target{expectedTarget()}))
			Expect(indexEntryExists()).To(BeFalse())
		})

		for _, behaviour := range []cleanerBehaviour{cleanerFails, cleanerBlocks, cleanerPanics} {
			It(fmt.Sprintf("still answers succeeded at once and removes the index entry when the cleaner %s", behaviour), func() {
				cleaner := useCleaner(behaviour)

				response, took := finishDeleteAndPoll()

				Expect(response.State).To(Equal(osbapi.StateSucceeded))
				Expect(took).To(BeNumerically("<", time.Second), "the cleanup must not delay the poll")
				Expect(indexEntryExists()).To(BeFalse())
				Eventually(cleaner.Targets).Should(Equal([]credhub.Target{expectedTarget()}))
			})
		}

		It("logs a cleaner panic with the deployment name and a stack, and keeps running", func() {
			useCleaner(cleanerPanics)

			response, _ := finishDeleteAndPoll()

			Expect(response.State).To(Equal(osbapi.StateSucceeded))
			brokerInstance.WaitForCredentialCleanups()
			Expect(capture.output()).To(ContainSubstring("CredHub cleanup for deployment " + deploymentName))
			Expect(capture.output()).To(ContainSubstring("panicked"))
			Expect(capture.output()).To(ContainSubstring("goroutine"))
		})

		It("never calls the cleaner when the delete task is done but the deployment still exists", func() {
			cleaner := useCleaner(cleanerSucceeds)
			specWorld, release, name := world, releaseDelete, deploymentName
			director.DeleteDeploymentFn = func(string) (*bosh.Task, error) {
				specWorld.addEvents(deploymentEvents(boshEventDelete, name, deleteTaskID)...)
				<-release
				specWorld.set(&bosh.Task{ID: deleteTaskID, State: "done", Description: "delete deployment " + name})

				return &bosh.Task{ID: deleteTaskID, State: "done", Description: "delete deployment " + name}, nil
			}

			response, _ := finishDeleteAndPoll()

			Expect(response.State).To(Equal(osbapi.StateFailed))
			Consistently(cleaner.Targets, 200*time.Millisecond).Should(BeEmpty())
			Expect(indexEntryExists()).To(BeTrue())
		})

		It("never calls the cleaner and keeps the index entry when the director errors instead of answering 404", func() {
			cleaner := useCleaner(cleanerSucceeds)

			deprovision()
			Eventually(func() []string { return director.Calls("DeleteDeployment") }).Should(Equal([]string{deploymentName}))
			close(releaseDelete)
			Eventually(recordedTask).Should(Equal(deleteTaskID))
			directorFails.Store(true)

			_, err := brokerInstance.LastOperation(ctx, instanceID, osbapi.LastOperationRequest{ServiceID: serviceID, PlanID: planID, Operation: "deprovision"})

			Expect(err).To(HaveOccurred())
			Consistently(cleaner.Targets, 200*time.Millisecond).Should(BeEmpty())
			Expect(indexEntryExists()).To(BeTrue())
		})

		It("behaves exactly as before when no cleaner is configured", func() {
			Expect(brokerInstance.CredentialCleaner).To(BeNil())

			response, _ := finishDeleteAndPoll()

			Expect(response.State).To(Equal(osbapi.StateSucceeded))
			Expect(indexEntryExists()).To(BeFalse())
		})
	})

	Context("when the background monitor confirms the delete", func() {
		BeforeEach(func() {
			restorePoll = broker.SetTaskPollInterval(10 * time.Millisecond)
		})

		for _, behaviour := range []cleanerBehaviour{cleanerSucceeds, cleanerFails, cleanerBlocks, cleanerPanics} {
			It(fmt.Sprintf("calls the cleaner without any LastOperation poll and removes the index entry when the cleaner %s", behaviour), func() {
				cleaner := useCleaner(behaviour)

				deprovision()
				Eventually(func() []string { return director.Calls("DeleteDeployment") }).Should(Equal([]string{deploymentName}))
				close(releaseDelete)

				Eventually(cleaner.Targets, 5*time.Second).Should(Equal([]credhub.Target{expectedTarget()}))
				Eventually(indexEntryExists, 5*time.Second).Should(BeFalse())
			})
		}

		It("never calls the cleaner when the task is done but the deployment still exists", func() {
			cleaner := useCleaner(cleanerSucceeds)
			specWorld, release, name := world, releaseDelete, deploymentName
			director.DeleteDeploymentFn = func(string) (*bosh.Task, error) {
				specWorld.addEvents(deploymentEvents(boshEventDelete, name, deleteTaskID)...)
				<-release
				specWorld.set(&bosh.Task{ID: deleteTaskID, State: "done", Description: "delete deployment " + name})

				return &bosh.Task{ID: deleteTaskID, State: "done", Description: "delete deployment " + name}, nil
			}

			deprovision()
			close(releaseDelete)
			Eventually(recordedTask).Should(Equal(deleteTaskID))

			Eventually(func() []string { return director.Calls("GetTask") }, 5*time.Second).ShouldNot(BeEmpty())
			Consistently(cleaner.Targets, 300*time.Millisecond).Should(BeEmpty())
			Expect(indexEntryExists()).To(BeTrue())
		})

		It("never calls the cleaner and keeps the index entry when the director errors instead of answering 404", func() {
			cleaner := useCleaner(cleanerSucceeds)
			specWorld, release, name, failing := world, releaseDelete, deploymentName, &directorFails
			director.DeleteDeploymentFn = func(string) (*bosh.Task, error) {
				specWorld.addEvents(deploymentEvents(boshEventDelete, name, deleteTaskID)...)
				<-release
				specWorld.set(&bosh.Task{ID: deleteTaskID, State: "done", Description: "delete deployment " + name})
				failing.Store(true)

				return &bosh.Task{ID: deleteTaskID, State: "done", Description: "delete deployment " + name}, nil
			}

			deprovision()
			close(releaseDelete)
			Eventually(recordedTask).Should(Equal(deleteTaskID))

			Eventually(func() []string { return director.Calls("GetTask") }, 5*time.Second).ShouldNot(BeEmpty())
			Consistently(cleaner.Targets, 300*time.Millisecond).Should(BeEmpty())
			Expect(indexEntryExists()).To(BeTrue())
		})
	})

	Context("when the deployment is already gone as the deprovision starts", func() {
		BeforeEach(func() {
			world.markDeploymentDeleted()
		})

		for _, behaviour := range []cleanerBehaviour{cleanerSucceeds, cleanerFails, cleanerBlocks, cleanerPanics} {
			It(fmt.Sprintf("calls the cleaner and finishes the deprovision when the cleaner %s", behaviour), func() {
				cleaner := useCleaner(behaviour)

				deprovision()

				Eventually(cleaner.Targets, 5*time.Second).Should(Equal([]credhub.Target{expectedTarget()}))
				Eventually(indexEntryExists, 5*time.Second).Should(BeFalse())
				Expect(poll().State).To(Equal(osbapi.StateSucceeded))
				Expect(director.Calls("DeleteDeployment")).To(BeEmpty())
			})
		}
	})

	Context("when a delete retry finds the deployment gone", func() {
		BeforeEach(func() {
			// The existence check sees the deployment; the retry loop's own
			// check, one call later, finds it gone.
			var calls atomic.Int32

			name := deploymentName
			director.GetDeploymentFn = func(string) (*bosh.DeploymentDetail, error) {
				if calls.Add(1) == 1 {
					return &bosh.DeploymentDetail{Name: name, Manifest: "name: " + name}, nil
				}

				return nil, fmt.Errorf("%w: %s", bosh.ErrDeploymentNotFound, name)
			}
		})

		for _, behaviour := range []cleanerBehaviour{cleanerSucceeds, cleanerFails, cleanerBlocks, cleanerPanics} {
			It(fmt.Sprintf("calls the cleaner and finishes the deprovision when the cleaner %s", behaviour), func() {
				cleaner := useCleaner(behaviour)

				deprovision()

				Eventually(cleaner.Targets, 5*time.Second).Should(Equal([]credhub.Target{expectedTarget()}))
				Eventually(indexEntryExists, 5*time.Second).Should(BeFalse())
				Expect(director.Calls("DeleteDeployment")).To(BeEmpty())
			})
		}
	})

	Context("when LastOperation and the monitor confirm at the same moment", func() {
		It("runs the cleaner at most once at a time", func() {
			restorePoll = broker.SetTaskPollInterval(10 * time.Millisecond)

			credhubFake := &blockingCredHub{
				release: make(chan struct{}),
				names:   []string{"/" + directorName + "/" + deploymentName + "/valkey_standalone_crt"},
			}
			releaseFinds := func() {
				select {
				case <-credhubFake.release:
				default:
					close(credhubFake.release)
				}
			}

			defer releaseFinds()

			policy := func() credhub.Policy {
				return credhub.Policy{DirectorName: directorName, PlanIDs: []string{planID}}
			}
			recorder := &resultRecorder{inner: credhub.NewCleaner(credhubFake, director, policy, logger.Get())}
			brokerInstance.CredentialCleaner = recorder

			// The delete task stays queued until both confirmations are armed.
			specWorld, name := world, deploymentName
			director.DeleteDeploymentFn = func(string) (*bosh.Task, error) {
				specWorld.addEvents(deploymentEvents(boshEventDelete, name, deleteTaskID)...)
				specWorld.set(&bosh.Task{ID: deleteTaskID, State: "queued", Description: "delete deployment " + name})
				specWorld.markDeploymentDeleted()

				return &bosh.Task{ID: deleteTaskID, State: "queued", Description: "delete deployment " + name}, nil
			}

			// The first two not-found answers wait for each other, so the
			// monitor and LastOperation confirm the delete together.
			var (
				armed     atomic.Bool
				confirmed atomic.Int32
			)

			rendezvous := make(chan struct{})
			director.GetDeploymentFn = func(deployment string) (*bosh.DeploymentDetail, error) {
				if !specWorld.isDeploymentDeleted() {
					return &bosh.DeploymentDetail{Name: deployment, Manifest: "name: " + deployment}, nil
				}

				if armed.Load() {
					switch confirmed.Add(1) {
					case 1:
						select {
						case <-rendezvous:
						case <-time.After(5 * time.Second):
						}
					case 2:
						close(rendezvous)
					}
				}

				return nil, fmt.Errorf("%w: %s", bosh.ErrDeploymentNotFound, deployment)
			}

			deprovision()
			Eventually(recordedTask).Should(Equal(deleteTaskID))
			Eventually(func() int { return len(director.Calls("GetTask")) }, 5*time.Second).Should(BeNumerically(">", 0))

			armed.Store(true)
			specWorld.set(&bosh.Task{ID: deleteTaskID, State: "done", Description: "delete deployment " + name})

			Expect(poll().State).To(Equal(osbapi.StateSucceeded))
			Eventually(recorder.Duplicates, 5*time.Second).Should(Equal(1), "the second confirmation must find the first run in flight")

			close(credhubFake.release)
			Eventually(func() int { return len(recorder.Results()) }, 5*time.Second).Should(Equal(2))

			finds, maxActive, deleted := credhubFake.counts()
			Expect(finds).To(Equal(1))
			Expect(maxActive).To(Equal(1))
			Expect(deleted).To(Equal(credhubFake.names))
			Expect(indexEntryExists()).To(BeFalse())
		})
	})

	Context("when the reconciler asks for a cleanup", func() {
		It("starts the cleaner when the metadata records Cloud Foundry's deprovision request", func() {
			cleaner := useCleaner(cleanerSucceeds)

			Expect(vaultClient.Put(ctx, instanceID+"/metadata", map[string]interface{}{
				"delete_requested_at": time.Now().Add(-3 * time.Hour).Format(time.RFC3339),
			})).To(Succeed())

			brokerInstance.CleanupDeploymentCredentials(ctx, instanceID, deploymentName)

			Eventually(cleaner.Targets, 5*time.Second).Should(Equal([]credhub.Target{expectedTarget()}))
		})

		It("leaves the credentials alone when the delete request predates the instance's creation", func() {
			cleaner := useCleaner(cleanerSucceeds)

			Expect(vaultClient.Put(ctx, instanceID+"/metadata", map[string]interface{}{
				"delete_requested_at": time.Now().Add(-5 * time.Hour).Format(time.RFC3339),
				"created_at":          time.Now().Add(-3 * time.Hour).Format(time.RFC3339),
			})).To(Succeed())

			brokerInstance.CleanupDeploymentCredentials(ctx, instanceID, deploymentName)
			brokerInstance.WaitForCredentialCleanups()

			Expect(cleaner.Targets()).To(BeEmpty())
			Expect(capture.output()).To(ContainSubstring("predates"))
		})

		It("leaves the credentials alone and says so when only the reconciler marked the instance deleted", func() {
			cleaner := useCleaner(cleanerSucceeds)

			Expect(vaultClient.Put(ctx, instanceID+"/metadata", map[string]interface{}{
				"deleted_at": time.Now().Add(-3 * time.Hour).Format(time.RFC3339),
				"deleted_by": "reconciler",
				"status":     "deleted",
			})).To(Succeed())

			brokerInstance.CleanupDeploymentCredentials(ctx, instanceID, deploymentName)
			brokerInstance.WaitForCredentialCleanups()

			Expect(cleaner.Targets()).To(BeEmpty())
			Expect(capture.output()).To(ContainSubstring("was not deprovisioned through the broker"))
			Expect(capture.output()).To(ContainSubstring("/" + directorName + "/" + deploymentName + "/"))
		})

		It("leaves the credentials alone when the instance has no metadata at all", func() {
			cleaner := useCleaner(cleanerSucceeds)

			brokerInstance.CleanupDeploymentCredentials(ctx, instanceID, deploymentName)
			brokerInstance.WaitForCredentialCleanups()

			Expect(cleaner.Targets()).To(BeEmpty())
			Expect(capture.output()).To(ContainSubstring("was not deprovisioned through the broker"))
		})

		It("returns at once even when the cleaner blocks forever", func() {
			cleaner := useCleaner(cleanerBlocks)

			Expect(vaultClient.Put(ctx, instanceID+"/metadata", map[string]interface{}{
				"delete_requested_at": time.Now().Add(-3 * time.Hour).Format(time.RFC3339),
			})).To(Succeed())

			started := time.Now()

			brokerInstance.CleanupDeploymentCredentials(ctx, instanceID, deploymentName)
			Expect(time.Since(started)).To(BeNumerically("<", 50*time.Millisecond))

			Eventually(cleaner.Targets, 5*time.Second).Should(HaveLen(1))
		})

		It("counts a panic in the vault read as no deprovision request instead of crashing", func() {
			cleaner := useCleaner(cleanerSucceeds)

			// A nil vault makes the read panic.
			brokerInstance.Vault = nil

			brokerInstance.CleanupDeploymentCredentials(ctx, instanceID, deploymentName)
			brokerInstance.WaitForCredentialCleanups()

			Expect(cleaner.Targets()).To(BeEmpty())
			Expect(capture.output()).To(ContainSubstring("panicked"))
			Expect(capture.output()).To(ContainSubstring("was not deprovisioned through the broker"))
		})

		It("does nothing when no cleaner is configured", func() {
			Expect(brokerInstance.CredentialCleaner).To(BeNil())

			brokerInstance.CleanupDeploymentCredentials(ctx, instanceID, deploymentName)
			brokerInstance.WaitForCredentialCleanups()

			Expect(strings.Contains(capture.output(), "CredHub cleanup")).To(BeFalse())
		})
	})
})
