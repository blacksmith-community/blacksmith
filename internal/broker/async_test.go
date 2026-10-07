package broker_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/broker"
	"blacksmith/internal/config"
	"blacksmith/internal/services"
	internalVault "blacksmith/internal/vault"
	"blacksmith/pkg/testutil"
	vaultPkg "blacksmith/pkg/vault"
	"blacksmith/shield"

	"github.com/fivetwenty-io/osbapi/v2/pkg/osbapi"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var errDeleteFailed = errors.New("director refused the delete")

// contextDirector reports the context each director call received, which the
// scripted director's context-free hooks cannot do.
type contextDirector struct {
	*testutil.ScriptedBOSHDirector

	onGetInfo          func(ctx context.Context) (*bosh.Info, error)
	onGetDeployment    func(ctx context.Context, name string) (*bosh.DeploymentDetail, error)
	onDeleteDeployment func(ctx context.Context, name string) (*bosh.Task, error)
	onCreateDeployment func(ctx context.Context, manifest string) (*bosh.Task, error)
	onGetEvents        func(ctx context.Context, deployment string) ([]bosh.Event, error)

	onFindTaskByContextID func(ctx context.Context, contextID string) (*bosh.Task, error)
}

func (d *contextDirector) FindTaskByContextID(ctx context.Context, contextID string) (*bosh.Task, error) {
	if d.onFindTaskByContextID != nil {
		return d.onFindTaskByContextID(ctx, contextID)
	}

	return d.ScriptedBOSHDirector.FindTaskByContextID(ctx, contextID) //nolint:wrapcheck // the double passes the scripted error through
}

func (d *contextDirector) CreateDeployment(ctx context.Context, manifest string) (*bosh.Task, error) {
	if d.onCreateDeployment != nil {
		return d.onCreateDeployment(ctx, manifest)
	}

	return d.ScriptedBOSHDirector.CreateDeployment(ctx, manifest)
}

func (d *contextDirector) GetEvents(ctx context.Context, deployment string) ([]bosh.Event, error) {
	if d.onGetEvents != nil {
		return d.onGetEvents(ctx, deployment)
	}

	return d.ScriptedBOSHDirector.GetEvents(ctx, deployment)
}

func (d *contextDirector) GetInfo(ctx context.Context) (*bosh.Info, error) {
	if d.onGetInfo != nil {
		return d.onGetInfo(ctx)
	}

	return d.ScriptedBOSHDirector.GetInfo(ctx)
}

func (d *contextDirector) GetDeployment(ctx context.Context, name string) (*bosh.DeploymentDetail, error) {
	if d.onGetDeployment != nil {
		return d.onGetDeployment(ctx, name)
	}

	return d.ScriptedBOSHDirector.GetDeployment(ctx, name)
}

func (d *contextDirector) DeleteDeployment(ctx context.Context, name string) (*bosh.Task, error) {
	if d.onDeleteDeployment != nil {
		return d.onDeleteDeployment(ctx, name)
	}

	return d.ScriptedBOSHDirector.DeleteDeployment(ctx, name)
}

// Detached broker work runs under deadlines, and waits give way to the context
// instead of sleeping through a shutdown.
var _ = Describe("Detached broker work", func() {
	var (
		ctx            context.Context
		director       *contextDirector
		vaultClient    *internalVault.Vault
		brokerInstance *broker.Broker
		instanceID     string
		serviceID      string
		planID         string
		deploymentName string
	)

	BeforeEach(func() {
		ctx = context.Background()
		instanceID = fmt.Sprintf("detached-%d", time.Now().UnixNano())
		serviceID = "valkey"
		planID = "standalone"
		deploymentName = planID + "-" + instanceID

		director = &contextDirector{ScriptedBOSHDirector: testutil.NewScriptedBOSHDirector()}
		vaultClient = internalVault.New(suite.vault.Addr, suite.vault.RootToken, true)

		dir, err := os.MkdirTemp("", "blacksmith-detached")
		Expect(err).ToNot(HaveOccurred())

		initScript := filepath.Join(dir, "init")
		Expect(os.WriteFile(initScript, []byte("#!/bin/bash\nexit 0\n"), 0o600)).To(Succeed())

		plan := services.Plan{
			ID:             planID,
			Name:           planID,
			Type:           "valkey",
			InitScriptPath: initScript,
			Manifest: map[interface{}]interface{}{
				"name":            "placeholder",
				"instance_groups": []interface{}{},
			},
			Service: &services.Service{ID: serviceID, Name: serviceID},
		}

		brokerInstance = &broker.Broker{
			Plans:         map[string]services.Plan{serviceID + "/" + planID: plan},
			BOSH:          director,
			Vault:         vaultClient,
			Shield:        &shield.NoopClient{},
			Config:        &config.Config{},
			InstanceLocks: make(map[string]*sync.Mutex),
		}
	})

	AfterEach(func() {
		_ = vaultClient.Index(ctx, instanceID, nil)
	})

	It("retry delete stops waiting when the context ends", func() {
		retryCtx, cancel := context.WithCancel(ctx)
		defer cancel()

		director.onGetDeployment = func(context.Context, string) (*bosh.DeploymentDetail, error) {
			return &bosh.DeploymentDetail{Name: deploymentName}, nil
		}
		director.onDeleteDeployment = func(context.Context, string) (*bosh.Task, error) {
			cancel()

			return nil, errDeleteFailed
		}

		finished := make(chan error, 1)

		go func() { finished <- brokerInstance.RetryDeleteDeployment(retryCtx, deploymentName, instanceID, 3) }()

		var err error
		Eventually(finished, 2*time.Second).Should(Receive(&err))
		Expect(err).To(MatchError(context.Canceled))
		Expect(err.Error()).To(ContainSubstring(deploymentName))
	})

	It("deprovision monitor stops at shutdown", func() {
		defer broker.SetTaskPollInterval(10 * time.Millisecond)()

		serviceCtx, shutdown := context.WithCancel(ctx)
		defer shutdown()

		brokerInstance.ServiceContext = serviceCtx

		monitorExited := make(chan string, 4)
		brokerInstance.SetMonitorExited(func(id string) { monitorExited <- id })

		Expect(vaultClient.Index(ctx, instanceID, vaultPkg.Instance{
			ID: instanceID, ServiceID: serviceID, PlanID: planID, DeploymentName: deploymentName,
		})).To(Succeed())

		director.FindRunningTaskForDeploymentFn = func(string) (*bosh.Task, error) { return nil, nil } //nolint:nilnil // no running task is a nil task without an error
		director.GetDeploymentFn = func(name string) (*bosh.DeploymentDetail, error) {
			return &bosh.DeploymentDetail{Name: name, Manifest: "name: " + name}, nil
		}
		director.DeleteDeploymentFn = func(string) (*bosh.Task, error) {
			return &bosh.Task{ID: 300, State: "processing", Description: "delete deployment"}, nil
		}
		director.GetEventsFn = func(string) ([]bosh.Event, error) {
			return deploymentEvents(boshEventDelete, deploymentName, 300), nil
		}
		director.GetTaskFn = func(taskID int) (*bosh.Task, error) {
			return &bosh.Task{ID: taskID, State: "processing"}, nil
		}

		_, _, err := brokerInstance.Deprovision(ctx, instanceID, osbapi.DeprovisionRequest{ServiceID: serviceID, PlanID: planID}, true)
		Expect(err).ToNot(HaveOccurred())

		Eventually(func() int { return len(director.Calls("GetTask")) }, 5*time.Second).Should(BeNumerically(">=", 3))

		shutdown()

		// The monitor returning is the signal that no poll is still under way.
		Eventually(monitorExited, 5*time.Second).Should(Receive(Equal(instanceID)))

		polled := len(director.Calls("GetTask"))

		Consistently(func() int { return len(director.Calls("GetTask")) }, 300*time.Millisecond, 20*time.Millisecond).Should(Equal(polled))
	})

	Context("when the broker shuts down during a deprovision", func() {
		var (
			shutdown      context.CancelFunc
			releaseDelete chan struct{}
			releaseOnce   sync.Once
		)

		startDeprovision := func() {
			var serviceCtx context.Context

			serviceCtx, shutdown = context.WithCancel(ctx)
			brokerInstance.ServiceContext = serviceCtx
			releaseDelete = make(chan struct{})

			Expect(vaultClient.Index(ctx, instanceID, vaultPkg.Instance{
				ID: instanceID, ServiceID: serviceID, PlanID: planID, DeploymentName: deploymentName,
			})).To(Succeed())

			director.FindRunningTaskForDeploymentFn = func(string) (*bosh.Task, error) { return nil, nil } //nolint:nilnil // no running task is a nil task without an error

			if director.onGetDeployment == nil {
				director.onGetDeployment = func(context.Context, string) (*bosh.DeploymentDetail, error) {
					return &bosh.DeploymentDetail{Name: deploymentName, Manifest: "name: " + deploymentName}, nil
				}
			}

			_, _, err := brokerInstance.Deprovision(ctx, instanceID, osbapi.DeprovisionRequest{ServiceID: serviceID, PlanID: planID}, true)
			Expect(err).ToNot(HaveOccurred())
		}

		// expectLeftForLastOperation checks that the stopped deprovision left no
		// failure behind, and that LastOperation adopts the delete task the
		// director kept running.
		expectLeftForLastOperation := func() {
			Eventually(func() bool { return brokerInstance.DeprovisionActive(instanceID) }, 10*time.Second).Should(BeFalse())

			state, task, _, err := vaultClient.State(ctx, instanceID)
			Expect(err).ToNot(HaveOccurred())
			Expect(state).ToNot(Equal("failed"), "the stopped deprovision was recorded as failed")
			Expect(task).ToNot(Equal(-1), "the stopped deprovision was recorded with the failure task")

			director.onGetDeployment = func(context.Context, string) (*bosh.DeploymentDetail, error) {
				return &bosh.DeploymentDetail{Name: deploymentName, Manifest: "name: " + deploymentName}, nil
			}
			director.onGetEvents = func(context.Context, string) ([]bosh.Event, error) {
				return deploymentEvents(boshEventDelete, deploymentName, 300), nil
			}
			director.GetTaskFn = func(taskID int) (*bosh.Task, error) {
				return &bosh.Task{ID: taskID, State: "processing"}, nil
			}

			response, err := brokerInstance.LastOperation(ctx, instanceID, osbapi.LastOperationRequest{
				ServiceID: serviceID, PlanID: planID, Operation: "deprovision",
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(response.State).To(Equal(osbapi.StateInProgress))
			Expect(response.Description).To(ContainSubstring("task 300"))
		}

		AfterEach(func() {
			releaseOnce.Do(func() {
				if releaseDelete != nil {
					close(releaseDelete)
				}
			})

			if shutdown != nil {
				shutdown()
			}
		})

		It("a shutdown during the retry wait leaves the deprovision unfailed", func() {
			director.onDeleteDeployment = func(context.Context, string) (*bosh.Task, error) {
				shutdown()

				return nil, errDeleteFailed
			}

			startDeprovision()

			expectLeftForLastOperation()
		})

		It("a shutdown ends a delete the director is still running", func() {
			deleteStarted := make(chan struct{})

			director.onDeleteDeployment = func(callCtx context.Context, _ string) (*bosh.Task, error) {
				close(deleteStarted)

				select {
				case <-callCtx.Done():
					return nil, fmt.Errorf("delete stopped: %w", callCtx.Err())
				case <-releaseDelete:
					return nil, errDeleteFailed
				}
			}

			startDeprovision()

			Eventually(deleteStarted, 10*time.Second).Should(BeClosed())
			Expect(brokerInstance.DeprovisionActive(instanceID)).To(BeTrue())

			shutdown()

			expectLeftForLastOperation()
		})

		It("a shutdown while the deployment status is read leaves the deprovision unfailed", func() {
			statusRequested := make(chan struct{})

			director.onGetDeployment = func(callCtx context.Context, _ string) (*bosh.DeploymentDetail, error) {
				close(statusRequested)
				<-callCtx.Done()

				return nil, fmt.Errorf("status not read: %w", callCtx.Err())
			}

			startDeprovision()

			Eventually(statusRequested, 10*time.Second).Should(BeClosed())
			Expect(brokerInstance.DeprovisionActive(instanceID)).To(BeTrue())

			shutdown()

			expectLeftForLastOperation()
		})

		It("the delete runs under a deadline of two hours", func() {
			var (
				mu       sync.Mutex
				deadline time.Time
				hasLimit bool
			)

			director.onDeleteDeployment = func(callCtx context.Context, _ string) (*bosh.Task, error) {
				mu.Lock()
				deadline, hasLimit = callCtx.Deadline()
				mu.Unlock()

				shutdown()

				return nil, errDeleteFailed
			}

			started := time.Now()

			startDeprovision()

			Eventually(func() bool { return brokerInstance.DeprovisionActive(instanceID) }, 10*time.Second).Should(BeFalse())

			mu.Lock()
			defer mu.Unlock()

			Expect(hasLimit).To(BeTrue(), "the deprovision context has no deadline")
			Expect(deadline).To(BeTemporally("~", started.Add(2*time.Hour), 10*time.Second))
		})
	})

	It("the teardown of an abandoned deployment has its own deadline", func() {
		var (
			mu         sync.Mutex
			contextErr error
			deadline   time.Time
			hasLimit   bool
		)

		director.onGetDeployment = func(context.Context, string) (*bosh.DeploymentDetail, error) {
			return &bosh.DeploymentDetail{Name: deploymentName, Manifest: "name: " + deploymentName}, nil
		}
		director.onDeleteDeployment = func(callCtx context.Context, _ string) (*bosh.Task, error) {
			mu.Lock()
			contextErr = callCtx.Err()
			deadline, hasLimit = callCtx.Deadline()
			mu.Unlock()

			return &bosh.Task{ID: 300, State: "done"}, nil
		}
		director.onGetEvents = func(context.Context, string) ([]bosh.Event, error) { return nil, nil }

		// The provision context has ended by the time the teardown runs.
		provisionCtx, cancel := context.WithCancel(ctx)
		cancel()

		started := time.Now()
		brokerInstance.TearDownAbandonedDeployment(provisionCtx, instanceID, deploymentName)

		mu.Lock()
		defer mu.Unlock()

		Expect(contextErr).ToNot(HaveOccurred(), "the teardown context ended with the provision context")
		Expect(hasLimit).To(BeTrue(), "the teardown context has no deadline of its own")
		Expect(deadline).To(BeTemporally("~", started.Add(2*time.Hour), 10*time.Second))
	})

	It("cleanup of a failed deployment has a deadline", func() {
		var (
			mu       sync.Mutex
			deadline time.Time
			hasLimit bool
		)

		director.onGetDeployment = func(callCtx context.Context, _ string) (*bosh.DeploymentDetail, error) {
			mu.Lock()
			deadline, hasLimit = callCtx.Deadline()
			mu.Unlock()

			return nil, errDirectorUnreachable
		}

		started := time.Now()
		brokerInstance.CleanupFailedDeployment(ctx, deploymentName)

		mu.Lock()
		defer mu.Unlock()

		Expect(hasLimit).To(BeTrue(), "the cleanup context has no deadline")
		Expect(deadline).To(BeTemporally("~", started.Add(5*time.Minute), 10*time.Second))
	})

	It("cleanup of a failed deployment ignores the caller's cancellation", func() {
		var (
			mu         sync.Mutex
			contextErr error
			deadline   time.Time
			hasLimit   bool
		)

		director.onGetDeployment = func(callCtx context.Context, _ string) (*bosh.DeploymentDetail, error) {
			mu.Lock()
			contextErr = callCtx.Err()
			deadline, hasLimit = callCtx.Deadline()
			mu.Unlock()

			return nil, errDirectorUnreachable
		}

		// The poll that starts the cleanup returns, and the server cancels its
		// context, before the cleanup runs.
		callerCtx, cancel := context.WithCancel(ctx)
		cancel()

		started := time.Now()
		brokerInstance.CleanupFailedDeployment(callerCtx, deploymentName)

		mu.Lock()
		defer mu.Unlock()

		Expect(contextErr).ToNot(HaveOccurred(), "the cleanup context ended with the caller's")
		Expect(hasLimit).To(BeTrue(), "the cleanup context has no deadline of its own")
		Expect(deadline).To(BeTemporally("~", started.Add(5*time.Minute), 10*time.Second))
	})

	Context("when the provision context ends during the deploy", func() {
		var (
			shutdown      context.CancelFunc
			deployStarted chan struct{}
		)

		provisionUntilShutdown := func() {
			var serviceCtx context.Context

			serviceCtx, shutdown = context.WithCancel(ctx)
			brokerInstance.ServiceContext = serviceCtx
			deployStarted = make(chan struct{})

			director.GetInfoFn = func() (*bosh.Info, error) { return &bosh.Info{UUID: "director-uuid"}, nil }
			director.GetReleasesFn = func() ([]bosh.Release, error) { return []bosh.Release{}, nil }
			director.onCreateDeployment = func(callCtx context.Context, _ string) (*bosh.Task, error) {
				close(deployStarted)
				<-callCtx.Done()

				return nil, fmt.Errorf("deploy stopped: %w", callCtx.Err())
			}

			_, _, err := brokerInstance.Provision(ctx, instanceID, osbapi.ProvisionRequest{
				ServiceID: serviceID, PlanID: planID, OrganizationGUID: "org-guid", SpaceGUID: "space-guid",
			}, true)
			Expect(err).ToNot(HaveOccurred())

			Eventually(deployStarted, 10*time.Second).Should(BeClosed())

			shutdown()

			Eventually(func() bool { return brokerInstance.ProvisionActive(instanceID) }, 10*time.Second).Should(BeFalse())
		}

		AfterEach(func() {
			if shutdown != nil {
				shutdown()
			}
		})

		It("a failed deploy still finds its task after the provision context ends", func() {
			// A director that honours the context cannot answer on the ended one.
			director.onGetEvents = func(callCtx context.Context, _ string) ([]bosh.Event, error) {
				if err := callCtx.Err(); err != nil {
					return nil, fmt.Errorf("events not read: %w", err)
				}

				return deploymentEvents(boshEventCreate, deploymentName, 77), nil
			}

			provisionUntilShutdown()

			state, task, _, err := vaultClient.State(ctx, instanceID)
			Expect(err).ToNot(HaveOccurred())
			Expect(task).To(Equal(77), "the deploy task was not recorded")
			Expect(state).To(Equal("in_progress"))
		})

		It("shutdown ends a running provision", func() {
			director.onGetEvents = func(context.Context, string) ([]bosh.Event, error) { return nil, nil }

			provisionUntilShutdown()

			// The goroutine returned, and the poll that follows can settle the
			// instance from what was recorded.
			response, err := brokerInstance.LastOperation(ctx, instanceID, osbapi.LastOperationRequest{
				ServiceID: serviceID, PlanID: planID, Operation: "provision",
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(response.State).To(Equal(osbapi.StateFailed))
			Expect(response.Description).To(HavePrefix("Provision stopped by broker shutdown: "))
			Expect(response.Description).ToNot(ContainSubstring("Release upload failed"))
		})
	})

	It("provisioning works without a service context", func() {
		Expect(brokerInstance.ServiceContext).To(BeNil())

		var (
			mu       sync.Mutex
			deadline time.Time
			hasLimit bool
		)

		director.onGetInfo = func(callCtx context.Context) (*bosh.Info, error) {
			mu.Lock()
			deadline, hasLimit = callCtx.Deadline()
			mu.Unlock()

			return nil, errDirectorUnreachable
		}

		started := time.Now()

		_, _, err := brokerInstance.Provision(ctx, instanceID, osbapi.ProvisionRequest{
			ServiceID: serviceID, PlanID: planID, OrganizationGUID: "org-guid", SpaceGUID: "space-guid",
		}, true)
		Expect(err).ToNot(HaveOccurred())

		Eventually(func() bool { return brokerInstance.ProvisionActive(instanceID) }, 10*time.Second).Should(BeFalse())

		state, _, _, err := vaultClient.State(ctx, instanceID)
		Expect(err).ToNot(HaveOccurred())
		Expect(state).To(Equal("failed"))

		mu.Lock()
		defer mu.Unlock()

		Expect(hasLimit).To(BeTrue(), "the provision context has no deadline")
		Expect(deadline).To(BeTemporally("~", started.Add(60*time.Minute), 10*time.Second))
	})
})
