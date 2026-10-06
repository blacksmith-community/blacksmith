package broker_test

import (
	"context"
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

// panicLogger panics when the broker logs a message that contains trigger, so
// a spec can make an accept chain panic part way.
type panicLogger struct {
	*logger.NoOpLogger

	trigger string
}

func (p *panicLogger) Info(msg string, args ...interface{}) {
	if strings.Contains(fmt.Sprintf(msg, args...), p.trigger) {
		panic("injected accept chain panic")
	}
}

func (p *panicLogger) Named(string) logger.Logger { return p }

// One operation at a time owns an instance ID in this process. Provision and
// Deprovision take the same claim in one atomic step, so neither can slip in
// while the other is still being accepted, and a claim is given up on every
// way out of an accept chain.
var _ = Describe("The operation claim on an instance", func() {
	var (
		ctx            context.Context
		shutdown       context.CancelFunc
		director       *contextDirector
		vaultClient    *internalVault.Vault
		brokerInstance *broker.Broker
		instanceID     string
		serviceID      string
		planID         string
		deploymentName string
		provisionArg   osbapi.ProvisionRequest
		deprovisionArg osbapi.DeprovisionRequest
		findStarted    chan struct{}
		releaseFind    chan struct{}
		releaseOnce    sync.Once
		dir            string
	)

	release := func() { releaseOnce.Do(func() { close(releaseFind) }) }

	// holdFind holds the deprovision accept chain at its check for a running
	// BOSH task, the first time only, until the spec releases it.
	holdFind := func() {
		var finds atomic.Int32

		director.FindRunningTaskForDeploymentFn = func(string) (*bosh.Task, error) {
			if finds.Add(1) == 1 {
				findStarted <- struct{}{}

				select {
				case <-releaseFind:
				case <-time.After(10 * time.Second):
				}
			}

			return nil, nil //nolint:nilnil // no running task is a nil task without an error
		}
	}

	startDeprovision := func() chan error {
		done := make(chan error, 1)

		go func() {
			defer GinkgoRecover()

			_, _, err := brokerInstance.Deprovision(ctx, instanceID, deprovisionArg, true)
			done <- err
		}()

		return done
	}

	BeforeEach(func() {
		ctx = context.Background()
		serviceCtx, cancelService := context.WithCancel(ctx)
		shutdown = cancelService
		instanceID = fmt.Sprintf("claim-%d", time.Now().UnixNano())
		serviceID = "valkey"
		planID = "standalone"
		deploymentName = planID + "-" + instanceID
		provisionArg = osbapi.ProvisionRequest{
			ServiceID: serviceID, PlanID: planID, OrganizationGUID: "org-guid", SpaceGUID: "space-guid",
		}
		deprovisionArg = osbapi.DeprovisionRequest{ServiceID: serviceID, PlanID: planID}

		findStarted = make(chan struct{}, 4)
		releaseFind = make(chan struct{})
		releaseOnce = sync.Once{}

		director = &contextDirector{ScriptedBOSHDirector: testutil.NewScriptedBOSHDirector()}
		director.GetInfoFn = func() (*bosh.Info, error) { return &bosh.Info{UUID: "director-uuid"}, nil }
		director.GetReleasesFn = func() ([]bosh.Release, error) { return []bosh.Release{}, nil }
		// The deploy and the delete run until the broker shuts down, so the
		// operation that won the claim keeps it for the rest of the spec.
		director.onCreateDeployment = func(callCtx context.Context, _ string) (*bosh.Task, error) {
			<-callCtx.Done()

			return nil, fmt.Errorf("deploy stopped: %w", callCtx.Err())
		}
		director.onDeleteDeployment = func(callCtx context.Context, _ string) (*bosh.Task, error) {
			<-callCtx.Done()

			return nil, fmt.Errorf("delete stopped: %w", callCtx.Err())
		}

		vaultClient = internalVault.New(suite.vault.Addr, suite.vault.RootToken, true)

		var err error

		dir, err = os.MkdirTemp("", "blacksmith-claim")
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
			Plans:          map[string]services.Plan{serviceID + "/" + planID: plan},
			BOSH:           director,
			Vault:          vaultClient,
			Shield:         &shield.NoopClient{},
			Config:         &config.Config{},
			InstanceLocks:  make(map[string]*sync.Mutex),
			ServiceContext: serviceCtx,
		}

		Expect(vaultClient.Index(ctx, instanceID, vaultPkg.Instance{
			ID: instanceID, ServiceID: serviceID, PlanID: planID, DeploymentName: deploymentName,
		})).To(Succeed())
	})

	AfterEach(func() {
		release()
		shutdown()

		Eventually(func() bool {
			return brokerInstance.ProvisionActive(instanceID) || brokerInstance.DeprovisionActive(instanceID)
		}, 20*time.Second).Should(BeFalse())

		_ = vaultClient.Index(ctx, instanceID, nil)
		_ = os.RemoveAll(dir)
	})

	It("refuses a provision while a deprovision of the instance is still being accepted", func() {
		holdFind()

		deprovisioned := startDeprovision()

		Eventually(findStarted, 10*time.Second).Should(Receive())

		_, async, err := brokerInstance.Provision(ctx, instanceID, provisionArg, true)
		Expect(err).To(MatchError(osbapi.ErrConcurrencyError), "a provision ran alongside the deprovision being accepted")
		Expect(async).To(BeFalse())

		release()

		Eventually(deprovisioned, 10*time.Second).Should(Receive(BeNil()))
		Expect(director.Calls("CreateDeployment")).To(BeEmpty())
	})

	It("refuses a second deprovision while the first is still being accepted", func() {
		holdFind()

		first := startDeprovision()

		Eventually(findStarted, 10*time.Second).Should(Receive())

		_, async, err := brokerInstance.Deprovision(ctx, instanceID, deprovisionArg, true)
		Expect(err).To(MatchError(osbapi.ErrConcurrencyError), "a second deprovision ran alongside the first")
		Expect(async).To(BeFalse())
		Expect(director.Calls("FindRunningTaskForDeployment")).To(HaveLen(1), "the refused deprovision ran an accept chain of its own")

		release()

		Eventually(first, 10*time.Second).Should(Receive(BeNil()), "the first deprovision was not accepted")
	})

	It("gives the claim up when the deprovision's accept chain fails", func() {
		director.FindRunningTaskForDeploymentFn = func(string) (*bosh.Task, error) {
			return nil, errDirectorUnreachable
		}

		_, _, err := brokerInstance.Deprovision(ctx, instanceID, deprovisionArg, true)
		Expect(err).To(HaveOccurred())
		Expect(brokerInstance.DeprovisionActive(instanceID)).To(BeFalse(), "a failed deprovision left its claim behind")

		_, async, err := brokerInstance.Provision(ctx, instanceID, provisionArg, true)
		Expect(err).ToNot(HaveOccurred(), "a provision was refused by the claim of a failed deprovision")
		Expect(async).To(BeTrue())
	})

	It("gives the claim up when the deprovision's accept chain panics", func() {
		director.FindRunningTaskForDeploymentFn = func(string) (*bosh.Task, error) {
			panic("injected director panic")
		}

		Expect(func() {
			_, _, _ = brokerInstance.Deprovision(ctx, instanceID, deprovisionArg, true)
		}).To(PanicWith("injected director panic"))
		Expect(brokerInstance.DeprovisionActive(instanceID)).To(BeFalse(), "a deprovision that panicked left its claim behind")

		_, async, err := brokerInstance.Provision(ctx, instanceID, provisionArg, true)
		Expect(err).ToNot(HaveOccurred(), "a provision was refused by the claim of a deprovision that panicked")
		Expect(async).To(BeTrue())
	})

	It("gives the claim up when the provision's accept chain panics", func() {
		previous := logger.Get()

		logger.Set(&panicLogger{NoOpLogger: &logger.NoOpLogger{}, trigger: "Looking up plan"})

		Expect(func() {
			defer logger.Set(previous)

			_, _, _ = brokerInstance.Provision(ctx, instanceID, provisionArg, true)
		}).To(PanicWith("injected accept chain panic"))
		Expect(brokerInstance.ProvisionActive(instanceID)).To(BeFalse(), "a provision that panicked left its claim behind")

		director.FindRunningTaskForDeploymentFn = func(string) (*bosh.Task, error) {
			return nil, nil //nolint:nilnil // no running task is a nil task without an error
		}

		_, async, err := brokerInstance.Deprovision(ctx, instanceID, deprovisionArg, true)
		Expect(err).ToNot(HaveOccurred(), "a deprovision was refused by the claim of a provision that panicked")
		Expect(async).To(BeTrue())
	})
})
