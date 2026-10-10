package broker_test

import (
	"context"
	"sync"
	"sync/atomic"
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

// settleOnceDeleteTaskID is the delete task the specs' director starts.
const settleOnceDeleteTaskID = 300

// settleBarrier holds each caller until a given number of callers have
// arrived or a wait has passed, so two settling paths that do not exclude
// each other overlap, and two that do run one after the other.
type settleBarrier struct {
	mu      sync.Mutex
	arrived int
	want    int
	all     chan struct{}
	wait    time.Duration
}

func newSettleBarrier(want int, wait time.Duration) *settleBarrier {
	return &settleBarrier{want: want, all: make(chan struct{}), wait: wait}
}

// Arrive counts the caller and holds it until every caller has arrived or
// the wait has passed.
func (s *settleBarrier) Arrive() {
	s.mu.Lock()
	s.arrived++

	if s.arrived == s.want {
		close(s.all)
	}
	s.mu.Unlock()

	select {
	case <-s.all:
	case <-time.After(s.wait):
	}
}

// Every path that settles an operation does it once, however many of them
// reach the same record at the same time.
var _ = Describe("Settling a service operation", func() {
	var (
		ctx            context.Context
		shutdown       context.CancelFunc
		director       *testutil.ScriptedBOSHDirector
		vaultClient    *internalVault.Vault
		brokerInstance *broker.Broker
		cleaner        *recordingCleaner
		instanceID     string
		serviceID      string
		planID         string
		deploymentName string
	)

	BeforeEach(func() {
		ctx = context.Background()
		instanceID = cleanupGUID()
		serviceID = valkeyServiceID
		planID = standalonePlanID
		deploymentName = planID + "-" + instanceID

		director = testutil.NewScriptedBOSHDirector()
		vaultClient = internalVault.New(suite.vault.Addr, suite.vault.RootToken, true)
		cleaner = newRecordingCleaner(cleanerSucceeds)

		var serviceCtx context.Context

		serviceCtx, shutdown = context.WithCancel(ctx)

		brokerInstance = &broker.Broker{
			Plans: map[string]services.Plan{serviceID + "/" + planID: {
				ID:      planID,
				Name:    planID,
				Type:    valkeyServiceID,
				Service: &services.Service{ID: serviceID, Name: serviceID},
			}},
			BOSH:              director,
			Vault:             vaultClient,
			Shield:            &shield.NoopClient{},
			Config:            &config.Config{},
			InstanceLocks:     make(map[string]*sync.Mutex),
			ServiceContext:    serviceCtx,
			CredentialCleaner: cleaner,
		}
	})

	AfterEach(func() {
		stopBrokerGoroutines(shutdown)
		brokerInstance.WaitForCredentialCleanups()

		_ = vaultClient.Index(ctx, instanceID, nil)
	})

	It("the deprovision monitor and LastOperation settle a delete once", func() {
		defer broker.SetTaskPollInterval(20 * time.Millisecond)()

		monitorExited := make(chan string, 4)

		brokerInstance.SetMonitorExited(func(id string) { monitorExited <- id })

		Expect(vaultClient.Index(ctx, instanceID, vaultPkg.Instance{
			ID: instanceID, ServiceID: serviceID, PlanID: planID, DeploymentName: deploymentName,
		})).To(Succeed())

		var deleteSent, taskDone atomic.Bool

		// Both settling paths confirm the deployment is gone before they
		// settle. The barrier lets each of them in once the other arrives,
		// and lets a lone one in after a second.
		barrier := newSettleBarrier(2, time.Second)

		director.FindRunningTaskForDeploymentFn = func(string) (*bosh.Task, error) { return nil, nil } //nolint:nilnil // no running task is a nil task without an error
		director.GetDeploymentFn = func(name string) (*bosh.DeploymentDetail, error) {
			if !taskDone.Load() {
				return &bosh.DeploymentDetail{Name: name, Manifest: "name: " + name}, nil
			}

			barrier.Arrive()

			return nil, bosh.ErrDeploymentNotFound
		}
		director.DeleteDeploymentFn = func(string) (*bosh.Task, error) {
			deleteSent.Store(true)

			return &bosh.Task{ID: settleOnceDeleteTaskID, State: outcomeTaskProcessing, Description: outcomeDeleteDeployment}, nil
		}
		director.GetEventsFn = func(string) ([]bosh.Event, error) {
			return deploymentEvents(boshEventDelete, deploymentName, settleOnceDeleteTaskID), nil
		}
		director.GetTaskFn = func(taskID int) (*bosh.Task, error) {
			if taskDone.Load() {
				return &bosh.Task{ID: taskID, State: outcomeTaskDone}, nil
			}

			return &bosh.Task{ID: taskID, State: outcomeTaskProcessing}, nil
		}

		_, _, err := brokerInstance.Deprovision(ctx, instanceID, osbapi.DeprovisionRequest{ServiceID: serviceID, PlanID: planID}, true)
		Expect(err).ToNot(HaveOccurred())

		Eventually(func() int {
			recorded, readErr := brokerInstance.RecordedOperation(ctx, instanceID)
			if readErr != nil {
				return 0
			}

			return recorded.TaskID
		}, 5*time.Second).Should(Equal(settleOnceDeleteTaskID))
		Eventually(func() bool { return brokerInstance.DeprovisionActive(instanceID) }, 5*time.Second).Should(BeFalse())
		Expect(deleteSent.Load()).To(BeTrue())

		taskDone.Store(true)

		polled := make(chan osbapi.LastOperationResponse, 1)

		go func() {
			defer GinkgoRecover()

			response, pollErr := brokerInstance.LastOperation(ctx, instanceID, osbapi.LastOperationRequest{
				ServiceID: serviceID, PlanID: planID, Operation: outcomeDeprovision,
			})
			Expect(pollErr).ToNot(HaveOccurred())

			polled <- response
		}()

		var response osbapi.LastOperationResponse
		Eventually(polled, 10*time.Second).Should(Receive(&response))
		Expect(response.State).To(Equal(osbapi.StateSucceeded))

		Eventually(monitorExited, 10*time.Second).Should(Receive(Equal(instanceID)))

		brokerInstance.WaitForCredentialCleanups()

		Expect(cleaner.Targets()).To(HaveLen(1), "the confirmed delete of deployment %s must clean its credentials once", deploymentName)

		_, indexed, err := vaultClient.FindInstance(ctx, instanceID)
		Expect(err).ToNot(HaveOccurred())
		Expect(indexed).To(BeFalse())
	})
})
