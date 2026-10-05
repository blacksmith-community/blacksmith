package broker_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/broker"
	"blacksmith/internal/config"
	"blacksmith/internal/services"
	internalVault "blacksmith/internal/vault"
	"blacksmith/internal/vmmonitor"
	"blacksmith/pkg/testutil"
	vaultPkg "blacksmith/pkg/vault"

	"github.com/fivetwenty-io/osbapi/v2/pkg/osbapi"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var (
	errDeleteRejected   = errors.New("director rejected the delete")
	errDeployTaskFailed = errors.New("updating deployment: expected task '189' to succeed but state is 'error'")
)

const (
	boshEventCreate = "create"
	boshEventDelete = "delete"
)

// recordingVMMonitor records which instances the broker hands to vm-monitor.
type recordingVMMonitor struct {
	mu    sync.Mutex
	added []string
}

func (m *recordingVMMonitor) IsVMMonitor() bool { return true }

func (m *recordingVMMonitor) GetServiceVMStatus(context.Context, string) (*vmmonitor.VMStatus, error) {
	return &vmmonitor.VMStatus{}, nil
}

func (m *recordingVMMonitor) GetStatus() vmmonitor.MonitorStatus { return vmmonitor.MonitorStatus{} }

func (m *recordingVMMonitor) AddService(_ context.Context, instanceID, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.added = append(m.added, instanceID)

	return nil
}

func (m *recordingVMMonitor) Added() []string {
	m.mu.Lock()
	defer m.mu.Unlock()

	return append([]string(nil), m.added...)
}

// recordingShield records the backup schedules the broker creates and removes.
type recordingShield struct {
	mu          sync.Mutex
	scheduled   []string
	descheduled []string
}

func (s *recordingShield) Close() error { return nil }

func (s *recordingShield) CreateSchedule(instance, _, _, _ string, _ interface{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.scheduled = append(s.scheduled, instance)

	return nil
}

func (s *recordingShield) DeleteSchedule(instance, _, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.descheduled = append(s.descheduled, instance)

	return nil
}

func (s *recordingShield) Scheduled() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]string(nil), s.scheduled...)
}

// scriptedTasks holds the BOSH tasks and deployment existence one spec's
// director reports. Each spec gets its own, so a goroutine a previous spec
// left behind cannot change what the next spec's director answers.
type scriptedTasks struct {
	mu                sync.Mutex
	tasks             map[int]*bosh.Task
	events            []bosh.Event
	deploymentDeleted bool
	deleteAttempts    int
}

// addEvents records deployment events, newest first, as the director lists them.
func (s *scriptedTasks) addEvents(events ...bosh.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.events = append(append([]bosh.Event(nil), events...), s.events...)
}

func (s *scriptedTasks) listEvents() []bosh.Event {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]bosh.Event(nil), s.events...)
}

func (s *scriptedTasks) set(task *bosh.Task) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.tasks[task.ID] = task
}

func (s *scriptedTasks) get(taskID int) (*bosh.Task, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	task, ok := s.tasks[taskID]
	if !ok {
		return nil, false
	}

	copied := *task

	return &copied, true
}

func (s *scriptedTasks) markDeploymentDeleted() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.deploymentDeleted = true
}

func (s *scriptedTasks) isDeploymentDeleted() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.deploymentDeleted
}

func (s *scriptedTasks) nextDeleteAttempt() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.deleteAttempts++

	return s.deleteAttempts
}

// LastOperation used to answer from whichever task was newest in the
// deployment's BOSH events and treated anything that was not a delete as a
// provision. A finished `bosh ssh` task therefore finished deprovisions and
// provisions alike, and ran the provision completion hook on the way. These
// specs replay that lab sequence and pin the answer to the operation the
// broker accepted and the task it started.
var _ = Describe("LastOperation", func() {
	const (
		serviceID      = "valkey"
		planID         = "standalone"
		createTaskID   = 189
		sshTaskID      = 230
		deleteTaskID   = 233
		vitalsTaskID   = 240
		sshDescription = "ssh: setup:{\"ids\":[\"0\"],\"indexes\":[],\"job\":\"standalone\"}"
	)

	var (
		ctx            context.Context
		brokerInstance *broker.Broker
		vaultClient    *internalVault.Vault
		director       *testutil.ScriptedBOSHDirector
		monitor        *recordingVMMonitor
		backups        *recordingShield
		instanceID     string
		deploymentName string
		world          *scriptedTasks
		scriptDir      string
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

	recordedDescription := func() string {
		_, _, task, err := vaultClient.State(ctx, instanceID)
		Expect(err).ToNot(HaveOccurred())

		description, _ := task["description"].(string)

		return description
	}

	poll := func(operation string) osbapi.LastOperationResponse {
		response, err := brokerInstance.LastOperation(ctx, instanceID, osbapi.LastOperationRequest{
			ServiceID: serviceID,
			PlanID:    planID,
			Operation: operation,
		})
		Expect(err).ToNot(HaveOccurred())

		return response
	}

	pollState := func(operation string) func() osbapi.OperationState {
		return func() osbapi.OperationState { return poll(operation).State }
	}

	expectNoProvisionSideEffects := func() {
		Expect(director.Calls("GetDeploymentVMs")).To(BeEmpty(), "a poll must not fetch VMs for the completion hook")
		Expect(monitor.Added()).To(BeEmpty(), "a poll must not add the instance to vm-monitor")
		Expect(backups.Scheduled()).To(BeEmpty(), "a poll must not schedule a SHIELD backup")
	}

	// indexProvisionedInstance records an instance the way a finished
	// provision leaves it: indexed, with its provision details stored, and
	// with the create deployment task recorded as the last tracked task.
	indexProvisionedInstance := func() {
		Expect(vaultClient.Index(ctx, instanceID, vaultPkg.Instance{
			ID: instanceID, ServiceID: serviceID, PlanID: planID, DeploymentName: deploymentName,
		})).To(Succeed())
		Expect(vaultClient.Put(ctx, instanceID+"/"+deploymentName, map[string]interface{}{
			"details": map[string]interface{}{"service_id": serviceID, "plan_id": planID},
		})).To(Succeed())
		Expect(vaultClient.TrackProgress(ctx, instanceID, "provision",
			fmt.Sprintf("BOSH deployment in progress (task %d)", createTaskID), createTaskID, nil)).To(Succeed())
	}

	writeInitScript := func() string {
		dir, err := os.MkdirTemp("", "blacksmith-lastop")
		Expect(err).ToNot(HaveOccurred())

		scriptDir = dir
		path := filepath.Join(dir, "init")
		Expect(os.WriteFile(path, []byte("#!/bin/bash\nexit 0\n"), 0o600)).To(Succeed())

		return path
	}

	BeforeEach(func() {
		ctx = context.Background()
		instanceID = fmt.Sprintf("lastop-%d", time.Now().UnixNano())
		deploymentName = planID + "-" + instanceID

		// The deployment's events look like the lab's: the create deployment
		// task, then a finished `bosh ssh` task run by hand, which is newest.
		world = &scriptedTasks{tasks: map[int]*bosh.Task{
			createTaskID: {ID: createTaskID, State: "done", Description: "create deployment"},
			sshTaskID:    {ID: sshTaskID, State: "done", Description: sshDescription},
		}}
		world.addEvents(deploymentEvents(boshEventCreate, deploymentName, createTaskID)...)
		world.addEvents(
			bosh.Event{ID: "4", Action: "cleanup ssh", ObjectType: "instance", TaskID: strconv.Itoa(sshTaskID)},
			bosh.Event{ID: "3", Action: "setup ssh", ObjectType: "instance", TaskID: strconv.Itoa(sshTaskID)},
		)
		specWorld := world

		director = testutil.NewScriptedBOSHDirector()
		vaultClient = internalVault.New(suite.vault.Addr, suite.vault.RootToken, true)
		monitor = &recordingVMMonitor{}
		backups = &recordingShield{}

		director.GetEventsFn = func(string) ([]bosh.Event, error) { return specWorld.listEvents(), nil }
		director.GetTaskFn = func(taskID int) (*bosh.Task, error) {
			task, ok := specWorld.get(taskID)
			if !ok {
				return nil, fmt.Errorf("task %d not found", taskID) //nolint:err113 // test double
			}

			return task, nil
		}
		director.GetDeploymentFn = func(name string) (*bosh.DeploymentDetail, error) {
			if specWorld.isDeploymentDeleted() {
				return nil, fmt.Errorf("%w: %s", bosh.ErrDeploymentNotFound, name)
			}

			return &bosh.DeploymentDetail{Name: name, Manifest: "name: " + name}, nil
		}
		director.GetDeploymentVMsFn = func(string) ([]bosh.VM, error) {
			return []bosh.VM{{ID: "vm-0", Job: planID, Index: 0, State: "started", IPs: []string{"10.0.0.10"}}}, nil
		}
		director.FindRunningTaskForDeploymentFn = func(string) (*bosh.Task, error) { return nil, nil } //nolint:nilnil // no running task is a nil task without an error

		plan := services.Plan{
			ID:             planID,
			Name:           planID,
			Type:           serviceID,
			InitScriptPath: writeInitScript(),
			Manifest: map[interface{}]interface{}{
				"name":            "placeholder",
				"instance_groups": []interface{}{},
			},
			Credentials: map[interface{}]interface{}{
				"credentials": map[interface{}]interface{}{"host": "10.0.0.10"},
			},
			Service: &services.Service{ID: serviceID, Name: serviceID},
		}

		brokerInstance = &broker.Broker{
			Plans:         map[string]services.Plan{serviceID + "/" + planID: plan},
			BOSH:          director,
			Vault:         vaultClient,
			Shield:        backups,
			Config:        &config.Config{},
			InstanceLocks: make(map[string]*sync.Mutex),
		}
		brokerInstance.SetVMMonitor(monitor)
	})

	AfterEach(func() {
		_ = vaultClient.Index(ctx, instanceID, nil)
		_ = os.RemoveAll(scriptDir)
	})

	Context("for a deprovision", func() {
		var releaseDelete chan struct{}

		deprovision := func() osbapi.DeprovisionResponse {
			response, async, err := brokerInstance.Deprovision(ctx, instanceID, osbapi.DeprovisionRequest{ServiceID: serviceID, PlanID: planID}, true)
			Expect(err).ToNot(HaveOccurred())
			Expect(async).To(BeTrue())

			return response
		}

		BeforeEach(func() {
			indexProvisionedInstance()

			releaseDelete = make(chan struct{})
			specWorld, release, name := world, releaseDelete, deploymentName
			director.DeleteDeploymentFn = func(string) (*bosh.Task, error) {
				// BOSH records the delete event when the task starts, but
				// bosh-cli waits for the task to finish before it returns,
				// so the broker learns the task ID only at the end.
				specWorld.addEvents(deploymentEvents(boshEventDelete, name, deleteTaskID)...)

				select {
				case <-release:
				case <-time.After(10 * time.Second):
				}

				specWorld.set(&bosh.Task{ID: deleteTaskID, State: "done", Description: "delete deployment " + name})
				specWorld.markDeploymentDeleted()

				return &bosh.Task{ID: deleteTaskID, State: "done", Description: "delete deployment " + name}, nil
			}
		})

		AfterEach(func() {
			select {
			case <-releaseDelete:
			default:
				close(releaseDelete)
			}

			Eventually(func() bool { return brokerInstance.DeprovisionActive(instanceID) }, 20*time.Second).Should(BeFalse())
		})

		It("returns operation data that identifies the deprovision", func() {
			Expect(deprovision().Operation).To(Equal("deprovision"))
		})

		It("answers in progress while the delete call has not returned, even though the newest task is a finished ssh task", func() {
			operation := deprovision().Operation

			Eventually(func() []string { return director.Calls("DeleteDeployment") }).Should(Equal([]string{deploymentName}))

			Expect(poll(operation).State).To(Equal(osbapi.StateInProgress))
			Expect(poll("").State).To(Equal(osbapi.StateInProgress), "a poll without operation data must fall back to the recorded deprovision")
			Expect(indexEntryExists()).To(BeTrue())
			expectNoProvisionSideEffects()
		})

		It("succeeds once the delete task it started is done and the deployment is gone", func() {
			operation := deprovision().Operation

			Eventually(func() []string { return director.Calls("DeleteDeployment") }).Should(Equal([]string{deploymentName}))
			close(releaseDelete)
			Eventually(recordedTask).Should(Equal(deleteTaskID))

			Expect(poll(operation).State).To(Equal(osbapi.StateSucceeded))
			Expect(indexEntryExists()).To(BeFalse())
			expectNoProvisionSideEffects()
		})

		It("fails when the delete task it started errors", func() {
			specWorld, name := world, deploymentName
			director.DeleteDeploymentFn = func(string) (*bosh.Task, error) {
				specWorld.addEvents(deploymentEvents(boshEventDelete, name, deleteTaskID)...)
				specWorld.set(&bosh.Task{ID: deleteTaskID, State: "error", Description: "delete deployment " + name})

				return &bosh.Task{ID: deleteTaskID, State: "error", Description: "delete deployment " + name}, nil
			}

			operation := deprovision().Operation

			Eventually(recordedTask).Should(Equal(deleteTaskID))

			Expect(poll(operation).State).To(Equal(osbapi.StateFailed))
			Expect(indexEntryExists()).To(BeTrue(), "a failed delete must keep the instance")
			expectNoProvisionSideEffects()
		})

		It("stays in progress while a failed delete attempt waits to be retried", func() {
			specWorld, release, name := world, releaseDelete, deploymentName
			director.DeleteDeploymentFn = func(string) (*bosh.Task, error) {
				if specWorld.nextDeleteAttempt() == 1 {
					return nil, errDeleteRejected
				}

				specWorld.addEvents(deploymentEvents(boshEventDelete, name, deleteTaskID)...)

				<-release

				specWorld.set(&bosh.Task{ID: deleteTaskID, State: "done", Description: "delete deployment " + name})
				specWorld.markDeploymentDeleted()

				return &bosh.Task{ID: deleteTaskID, State: "done", Description: "delete deployment " + name}, nil
			}

			operation := deprovision().Operation

			Eventually(recordedDescription).Should(ContainSubstring("retry 1/3"))

			Expect(poll(operation).State).To(Equal(osbapi.StateInProgress))
			Expect(indexEntryExists()).To(BeTrue())
		})

		It("records the delete task its delete event names rather than the task the director adapter reports", func() {
			specWorld, name := world, deploymentName
			director.DeleteDeploymentFn = func(string) (*bosh.Task, error) {
				specWorld.addEvents(deploymentEvents(boshEventDelete, name, deleteTaskID)...)
				specWorld.set(&bosh.Task{ID: deleteTaskID, State: "done", Description: "delete deployment " + name})
				// vm-monitor's vitals request created a task on the deployment
				// while the delete ran, and it is the newest one.
				specWorld.set(&bosh.Task{ID: vitalsTaskID, State: "error", Description: "retrieve vm-stats"})
				specWorld.markDeploymentDeleted()

				return &bosh.Task{ID: vitalsTaskID, State: "error", Description: "retrieve vm-stats"}, nil
			}

			operation := deprovision().Operation

			Eventually(recordedTask).Should(Equal(deleteTaskID))
			Expect(poll(operation).State).To(Equal(osbapi.StateSucceeded))
			Expect(indexEntryExists()).To(BeFalse())
		})

		It("accepts a repeated delete while its deprovision runs without starting a second one", func() {
			operation := deprovision().Operation

			Eventually(func() []string { return director.Calls("DeleteDeployment") }).Should(HaveLen(1))

			Expect(deprovision().Operation).To(Equal(operation))
			Consistently(func() []string { return director.Calls("DeleteDeployment") }, 200*time.Millisecond).Should(HaveLen(1))
		})

		It("never finishes on the provision task when no deprovision was recorded", func() {
			// The last recorded task is still the finished create deployment
			// task, and no deprovision runs in this process.
			response := poll("deprovision")

			Expect(response.State).To(Equal(osbapi.StateFailed))
			Expect(strings.ToLower(response.Description)).To(ContainSubstring("interrupted"))
			Expect(indexEntryExists()).To(BeTrue())
			expectNoProvisionSideEffects()
		})
	})

	Context("for a provision", func() {
		provisionAndWait := func(createDeployment func(string) (*bosh.Task, error)) {
			director.GetInfoFn = func() (*bosh.Info, error) { return &bosh.Info{UUID: "director-uuid"}, nil }
			director.GetReleasesFn = func() ([]bosh.Release, error) { return []bosh.Release{}, nil }
			director.CreateDeploymentFn = createDeployment

			_, _, err := brokerInstance.Provision(ctx, instanceID, osbapi.ProvisionRequest{
				ServiceID: serviceID, PlanID: planID, OrganizationGUID: "org-guid", SpaceGUID: "space-guid",
			}, true)
			Expect(err).ToNot(HaveOccurred())

			Eventually(func() bool { return brokerInstance.ProvisionActive(instanceID) }, 10*time.Second).Should(BeFalse())
		}

		It("returns operation data that identifies the provision and stays in progress while BOSH deploys", func() {
			deployStarted := make(chan struct{})
			finishDeploy := make(chan struct{})

			director.GetInfoFn = func() (*bosh.Info, error) { return &bosh.Info{UUID: "director-uuid"}, nil }
			director.GetReleasesFn = func() ([]bosh.Release, error) { return []bosh.Release{}, nil }
			director.CreateDeploymentFn = func(string) (*bosh.Task, error) {
				close(deployStarted)

				select {
				case <-finishDeploy:
				case <-time.After(10 * time.Second):
				}

				return &bosh.Task{ID: createTaskID, State: "done", Description: "create deployment"}, nil
			}

			response, async, err := brokerInstance.Provision(ctx, instanceID, osbapi.ProvisionRequest{
				ServiceID: serviceID, PlanID: planID, OrganizationGUID: "org-guid", SpaceGUID: "space-guid",
			}, true)
			Expect(err).ToNot(HaveOccurred())
			Expect(async).To(BeTrue())
			Expect(response.Operation).To(Equal("provision"))

			Eventually(deployStarted, 10*time.Second).Should(BeClosed())

			Expect(poll(response.Operation).State).To(Equal(osbapi.StateInProgress))
			Expect(poll("").State).To(Equal(osbapi.StateInProgress))
			expectNoProvisionSideEffects()

			close(finishDeploy)
			Eventually(func() bool { return brokerInstance.ProvisionActive(instanceID) }, 10*time.Second).Should(BeFalse())
			Expect(recordedTask()).To(Equal(createTaskID))
		})

		It("records provision_requested_at in the metadata before the deploy starts", func() {
			var (
				seenMu       sync.Mutex
				seenMetadata map[string]interface{}
			)

			before := time.Now().Add(-time.Second)

			provisionAndWait(func(string) (*bosh.Task, error) {
				var metadata map[string]interface{}

				_, err := vaultClient.Get(ctx, instanceID+"/metadata", &metadata)
				Expect(err).ToNot(HaveOccurred())

				seenMu.Lock()
				seenMetadata = metadata
				seenMu.Unlock()

				return &bosh.Task{ID: createTaskID, State: "done", Description: "create deployment"}, nil
			})

			seenMu.Lock()
			defer seenMu.Unlock()

			raw, _ := seenMetadata["provision_requested_at"].(string)
			Expect(raw).ToNot(BeEmpty(), "the deploy started before the provision request was recorded")

			recorded, err := time.Parse(time.RFC3339, raw)
			Expect(err).ToNot(HaveOccurred())
			Expect(recorded).To(BeTemporally(">=", before.Truncate(time.Second)))
			Expect(recorded).To(BeTemporally("<=", time.Now().Add(time.Second)))
		})

		It("keeps the delete request and the earlier creation time in the metadata when the GUID is provisioned again", func() {
			Expect(vaultClient.Put(ctx, instanceID+"/metadata", map[string]interface{}{
				"created_at":          "2026-10-01T10:00:00Z",
				"delete_requested_at": "2026-10-01T11:00:00Z",
			})).To(Succeed())

			provisionAndWait(func(string) (*bosh.Task, error) {
				return &bosh.Task{ID: createTaskID, State: "done", Description: "create deployment"}, nil
			})

			var metadata map[string]interface{}

			_, err := vaultClient.Get(ctx, instanceID+"/metadata", &metadata)
			Expect(err).ToNot(HaveOccurred())
			Expect(metadata).To(HaveKeyWithValue("delete_requested_at", "2026-10-01T11:00:00Z"))
			Expect(metadata).To(HaveKeyWithValue("created_at", "2026-10-01T10:00:00Z"))
			Expect(metadata).To(HaveKey("provision_requested_at"))
		})

		It("records the create task its create event names rather than the task the director adapter reports", func() {
			provisionAndWait(func(string) (*bosh.Task, error) {
				// Someone ran `bosh ssh` while bosh-cli waited on the deploy,
				// so the adapter reports that task as the newest one.
				return &bosh.Task{ID: sshTaskID, State: "done", Description: sshDescription}, nil
			})

			Expect(recordedTask()).To(Equal(createTaskID))
			Expect(poll("provision").State).To(Equal(osbapi.StateSucceeded))
		})

		It("fails, and cleans up the deployment once, when the deploy task fails inside the create call", func() {
			world.set(&bosh.Task{ID: createTaskID, State: "error", Description: "create deployment"})

			director.DeleteDeploymentFn = func(string) (*bosh.Task, error) {
				return &bosh.Task{ID: deleteTaskID, State: "done", Description: "delete deployment"}, nil
			}

			provisionAndWait(func(string) (*bosh.Task, error) {
				return nil, errDeployTaskFailed
			})

			Expect(recordedTask()).To(Equal(createTaskID))
			Expect(poll("provision").State).To(Equal(osbapi.StateFailed))
			Eventually(func() []string { return director.Calls("DeleteDeployment") }).Should(HaveLen(1))

			Expect(poll("provision").State).To(Equal(osbapi.StateFailed))
			Consistently(func() []string { return director.Calls("DeleteDeployment") }, 200*time.Millisecond).Should(HaveLen(1))
			expectNoProvisionSideEffects()
		})

		It("fails when the provision task it recorded errored, even though the newest task is a finished ssh task", func() {
			indexProvisionedInstance()
			world.set(&bosh.Task{ID: createTaskID, State: "error", Description: "create deployment"})

			Expect(poll("provision").State).To(Equal(osbapi.StateFailed))
			expectNoProvisionSideEffects()
		})

		It("succeeds and runs the completion hook when the provision task it recorded is done", func() {
			indexProvisionedInstance()

			Expect(poll("provision").State).To(Equal(osbapi.StateSucceeded))
			Expect(monitor.Added()).To(Equal([]string{instanceID}))
			Expect(backups.Scheduled()).To(Equal([]string{instanceID}))
		})

		It("stays in progress while the provision task it recorded is still running", func() {
			indexProvisionedInstance()
			world.set(&bosh.Task{ID: createTaskID, State: "processing", Description: "create deployment"})

			Expect(poll("provision").State).To(Equal(osbapi.StateInProgress))
			expectNoProvisionSideEffects()
		})

		It("finds the create deployment task from the deployment's events when the broker lost track of it", func() {
			// The broker restarted while bosh-cli waited on the create task,
			// so the task ID was never recorded.
			Expect(vaultClient.Index(ctx, instanceID, vaultPkg.Instance{
				ID: instanceID, ServiceID: serviceID, PlanID: planID, DeploymentName: deploymentName,
			})).To(Succeed())
			Expect(vaultClient.Put(ctx, instanceID+"/"+deploymentName, map[string]interface{}{
				"details": map[string]interface{}{"service_id": serviceID, "plan_id": planID},
			})).To(Succeed())
			Expect(vaultClient.TrackProgress(ctx, instanceID, "provision", "Creating BOSH deployment", 0, nil)).To(Succeed())
			world.set(&bosh.Task{ID: createTaskID, State: "processing", Description: "create deployment"})

			Expect(poll("provision").State).To(Equal(osbapi.StateInProgress))
			Expect(recordedTask()).To(Equal(createTaskID))
			expectNoProvisionSideEffects()

			world.set(&bosh.Task{ID: createTaskID, State: "done", Description: "create deployment"})

			Expect(poll("provision").State).To(Equal(osbapi.StateSucceeded))
			Expect(monitor.Added()).To(Equal([]string{instanceID}))
		})

		It("fails when the broker lost track of a provision that never reached BOSH", func() {
			Expect(vaultClient.Index(ctx, instanceID, vaultPkg.Instance{
				ID: instanceID, ServiceID: serviceID, PlanID: planID, DeploymentName: deploymentName,
			})).To(Succeed())
			Expect(vaultClient.TrackProgress(ctx, instanceID, "provision", "Uploading BOSH releases", 0, nil)).To(Succeed())

			director.GetEventsFn = func(string) ([]bosh.Event, error) { return []bosh.Event{}, nil }

			response := poll("provision")

			Expect(response.State).To(Equal(osbapi.StateFailed))
			Expect(strings.ToLower(response.Description)).To(ContainSubstring("interrupted"))
			expectNoProvisionSideEffects()
		})
	})

	Context("for a deprovision the broker lost track of", func() {
		BeforeEach(func() {
			// The broker restarted while bosh-cli waited on the delete task.
			Expect(vaultClient.Index(ctx, instanceID, vaultPkg.Instance{
				ID: instanceID, ServiceID: serviceID, PlanID: planID, DeploymentName: deploymentName,
			})).To(Succeed())
			Expect(vaultClient.TrackProgress(ctx, instanceID, "deprovision", "Initiating BOSH deployment deletion with retry", 0, nil)).To(Succeed())
		})

		It("stays in progress while the delete task from the deployment's events runs", func() {
			world.set(&bosh.Task{ID: deleteTaskID, State: "processing", Description: "delete deployment " + deploymentName})
			world.addEvents(deploymentEvents(boshEventDelete, deploymentName, deleteTaskID)...)

			Expect(pollState("deprovision")()).To(Equal(osbapi.StateInProgress))
			Expect(recordedTask()).To(Equal(deleteTaskID))
			Expect(indexEntryExists()).To(BeTrue())
			expectNoProvisionSideEffects()
		})

		It("succeeds once that delete task is done and the deployment is gone", func() {
			world.set(&bosh.Task{ID: deleteTaskID, State: "done", Description: "delete deployment " + deploymentName})
			world.markDeploymentDeleted()
			world.addEvents(deploymentEvents(boshEventDelete, deploymentName, deleteTaskID)...)

			Expect(pollState("deprovision")()).To(Equal(osbapi.StateSucceeded))
			Expect(indexEntryExists()).To(BeFalse())
			expectNoProvisionSideEffects()
		})

		It("fails when no delete was ever started and nothing is running", func() {
			response := poll("deprovision")

			Expect(response.State).To(Equal(osbapi.StateFailed))
			Expect(strings.ToLower(response.Description)).To(ContainSubstring("interrupted"))
			Expect(indexEntryExists()).To(BeTrue())
			expectNoProvisionSideEffects()
		})
	})

	It("keeps answering succeeded for an instance that is no longer indexed", func() {
		Expect(poll("deprovision").State).To(Equal(osbapi.StateSucceeded))
		Expect(poll("").State).To(Equal(osbapi.StateSucceeded))
	})
})

// deploymentEvents returns the pair of deployment-level events BOSH records for
// a deploy or delete task, one when the task starts and one when it ends.
func deploymentEvents(action, deploymentName string, taskID int) []bosh.Event {
	return []bosh.Event{
		{ID: action + "-end", Action: action, ObjectType: "deployment", ObjectName: deploymentName, TaskID: strconv.Itoa(taskID)},
		{ID: action + "-start", Action: action, ObjectType: "deployment", ObjectName: deploymentName, TaskID: strconv.Itoa(taskID)},
	}
}
