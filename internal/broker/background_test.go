package broker_test

import (
	"context"
	"fmt"
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
	"blacksmith/pkg/logger"
	"blacksmith/pkg/testutil"
	vaultPkg "blacksmith/pkg/vault"
	"blacksmith/shield"

	"github.com/fivetwenty-io/osbapi/v2/pkg/osbapi"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

// backgroundDeleteTaskID is the delete task the director starts for the
// deprovisions in these specs.
const backgroundDeleteTaskID = 300

// A shutdown waits for the goroutines the broker starts that outlive a
// request and write operation records.
var _ = Describe("Broker background work", func() {
	var (
		ctx            context.Context
		director       *contextDirector
		vaultClient    *internalVault.Vault
		brokerInstance *broker.Broker
		instanceID     string
		serviceID      string
		planID         string
		deploymentName string
		capture        *captureLogger
		previousLogger logger.Logger
		stopService    context.CancelFunc
		restorePoll    func()
	)

	BeforeEach(func() {
		ctx = context.Background()
		instanceID = fmt.Sprintf("background-%d", time.Now().UnixNano())
		serviceID = valkeyServiceID
		planID = standalonePlanID
		deploymentName = planID + "-" + instanceID

		capture = newCaptureLogger()
		previousLogger = logger.Get()

		logger.Set(capture)

		restorePoll = broker.SetTaskPollInterval(10 * time.Millisecond)

		director = &contextDirector{ScriptedBOSHDirector: testutil.NewScriptedBOSHDirector()}
		vaultClient = internalVault.New(suite.vault.Addr, suite.vault.RootToken, true)

		dir, err := os.MkdirTemp("", "blacksmith-background")
		Expect(err).ToNot(HaveOccurred())

		initScript := filepath.Join(dir, "init")
		Expect(os.WriteFile(initScript, []byte("#!/bin/bash\nexit 0\n"), 0o600)).To(Succeed())

		plan := services.Plan{
			ID:             planID,
			Name:           planID,
			Type:           valkeyServiceID,
			InitScriptPath: initScript,
			Manifest: map[interface{}]interface{}{
				manifestNameKey:           outcomeManifestName,
				manifestInstanceGroupsKey: []interface{}{},
			},
			Service: &services.Service{ID: serviceID, Name: serviceID},
		}

		var serviceCtx context.Context

		serviceCtx, stopService = context.WithCancel(ctx)

		brokerInstance = &broker.Broker{
			Plans:          map[string]services.Plan{serviceID + "/" + planID: plan},
			BOSH:           director,
			Vault:          vaultClient,
			Shield:         &shield.NoopClient{},
			Config:         &config.Config{},
			InstanceLocks:  make(map[string]*sync.Mutex),
			ServiceContext: serviceCtx,
		}
	})

	AfterEach(func() {
		stopService()
		restorePoll()
		logger.Set(previousLogger)

		_ = vaultClient.Index(ctx, instanceID, nil)
	})

	// deprovision starts a deprovision whose delete task the monitor polls
	// through GetTask, and returns once the monitor has polled the task.
	deprovision := func(getTask func(ctx context.Context, taskID int) (*bosh.Task, error)) {
		Expect(vaultClient.Index(ctx, instanceID, vaultPkg.Instance{
			ID: instanceID, ServiceID: serviceID, PlanID: planID, DeploymentName: deploymentName,
		})).To(Succeed())

		polled := make(chan struct{})

		var pollOnce sync.Once

		director.FindRunningTaskForDeploymentFn = func(string) (*bosh.Task, error) { return nil, nil } //nolint:nilnil // no running task is a nil task without an error
		director.onDeleteDeployment = func(context.Context, string) (*bosh.Task, error) {
			return &bosh.Task{ID: backgroundDeleteTaskID, State: outcomeTaskProcessing, Description: outcomeDeleteDeployment}, nil
		}
		director.GetEventsFn = func(string) ([]bosh.Event, error) {
			return deploymentEvents(boshEventDelete, deploymentName, backgroundDeleteTaskID), nil
		}
		director.onGetTask = func(callCtx context.Context, taskID int) (*bosh.Task, error) {
			pollOnce.Do(func() { close(polled) })

			return getTask(callCtx, taskID)
		}

		_, _, err := brokerInstance.Deprovision(ctx, instanceID, osbapi.DeprovisionRequest{ServiceID: serviceID, PlanID: planID}, true)
		Expect(err).ToNot(HaveOccurred())

		Eventually(polled, 5*time.Second).Should(BeClosed(), "the deprovision monitor never polled its task")
	}

	It("WaitForBackground returns only after a deprovision monitor writes its record", func() {
		var deleted atomic.Bool

		director.onGetDeployment = func(context.Context, string) (*bosh.DeploymentDetail, error) {
			if deleted.Load() {
				return nil, fmt.Errorf("deployment %s: %w", deploymentName, bosh.ErrDeploymentNotFound)
			}

			return &bosh.DeploymentDetail{Name: deploymentName, Manifest: "name: " + deploymentName}, nil
		}

		// The monitor's first poll holds until the spec releases it, and
		// then finds the delete done and the deployment gone.
		release := make(chan struct{})

		var releaseOnce sync.Once

		defer releaseOnce.Do(func() { close(release) })

		deprovision(func(context.Context, int) (*bosh.Task, error) {
			<-release
			deleted.Store(true)

			return &bosh.Task{ID: backgroundDeleteTaskID, State: outcomeTaskDone}, nil
		})

		waited := make(chan struct{})

		go func() {
			defer GinkgoRecover()
			defer close(waited)

			waitCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()

			brokerInstance.WaitForBackground(waitCtx)
		}()

		Consistently(waited, 200*time.Millisecond).ShouldNot(BeClosed(), "WaitForBackground returned while the monitor was still running")

		releaseOnce.Do(func() { close(release) })

		Eventually(waited, 10*time.Second).Should(BeClosed())

		var metadata map[string]interface{}

		exists, err := vaultClient.Get(ctx, instanceID+"/metadata", &metadata)
		Expect(err).ToNot(HaveOccurred())
		Expect(exists).To(BeTrue())
		Expect(metadata).To(HaveKey("deleted_at"), "the monitor had not written deleted_at when WaitForBackground returned")

		_, indexed, err := vaultClient.FindInstance(ctx, instanceID)
		Expect(err).ToNot(HaveOccurred())
		Expect(indexed).To(BeFalse(), "the monitor had not removed the index entry when WaitForBackground returned")
	})

	It("WaitForBackground returns at its deadline and names the goroutines still running", func() {
		director.onGetDeployment = func(context.Context, string) (*bosh.DeploymentDetail, error) {
			return &bosh.DeploymentDetail{Name: deploymentName, Manifest: "name: " + deploymentName}, nil
		}

		deprovision(func(_ context.Context, taskID int) (*bosh.Task, error) {
			return &bosh.Task{ID: taskID, State: outcomeTaskProcessing}, nil
		})

		waitCtx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
		defer cancel()

		started := time.Now()

		brokerInstance.WaitForBackground(waitCtx)

		Expect(time.Since(started)).To(BeNumerically(">=", 300*time.Millisecond))
		Expect(time.Since(started)).To(BeNumerically("<", 3*time.Second))
		Expect(capture.output()).To(ContainSubstring("deprovision monitor of instance " + instanceID))
	})

	It("a goroutine launched after WaitForBackground runs untracked and is logged", func() {
		brokerInstance.WaitForBackground(ctx)

		ran := make(chan struct{})

		brokerInstance.Track("late work in the spec", func() { close(ran) })

		Eventually(ran, 5*time.Second).Should(BeClosed())
		Expect(capture.output()).To(ContainSubstring("late work in the spec"))
	})
})
