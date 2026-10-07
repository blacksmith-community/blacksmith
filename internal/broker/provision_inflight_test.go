package broker_test

import (
	"context"
	"errors"
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
	"blacksmith/pkg/testutil"
	"blacksmith/shield"

	"github.com/fivetwenty-io/osbapi/v2/pkg/osbapi"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

// A platform retries a provision whose first answer it did not see. While the
// first provision is still in flight in this process, the retry must not start
// a second deploy of the same deployment.
var _ = Describe("Provision repeated while the first is in flight", func() {
	var (
		ctx            context.Context
		director       *contextDirector
		vaultClient    *internalVault.Vault
		brokerInstance *broker.Broker
		instanceID     string
		serviceID      string
		planID         string
		deploymentName string
		request        osbapi.ProvisionRequest
		deploys        atomic.Int32
		deployStarted  chan struct{}
		releaseDeploy  chan struct{}
		releaseOnce    sync.Once
		dir            string
	)

	release := func() { releaseOnce.Do(func() { close(releaseDeploy) }) }

	BeforeEach(func() {
		ctx = context.Background()
		instanceID = fmt.Sprintf("inflight-%d", time.Now().UnixNano())
		serviceID = "valkey"
		planID = "standalone"
		deploymentName = planID + "-" + instanceID
		request = osbapi.ProvisionRequest{
			ServiceID: serviceID, PlanID: planID, OrganizationGUID: "org-guid", SpaceGUID: "space-guid",
		}

		deploys.Store(0)

		deployStarted = make(chan struct{}, 4)
		releaseDeploy = make(chan struct{})
		releaseOnce = sync.Once{}

		director = &contextDirector{ScriptedBOSHDirector: testutil.NewScriptedBOSHDirector()}
		director.GetInfoFn = func() (*bosh.Info, error) { return &bosh.Info{UUID: "director-uuid"}, nil }
		director.GetReleasesFn = func() ([]bosh.Release, error) { return []bosh.Release{}, nil }
		director.onGetEvents = func(context.Context, string) ([]bosh.Event, error) {
			return deploymentEvents(boshEventCreate, deploymentName, 77), nil
		}
		// The deploy is held until the spec releases it, so the first
		// provision stays in flight while the second request arrives.
		director.onCreateDeployment = func(callCtx context.Context, _ string) (*bosh.Task, error) {
			deploys.Add(1)

			deployStarted <- struct{}{}

			select {
			case <-releaseDeploy:
			case <-callCtx.Done():
				return nil, fmt.Errorf("deploy stopped: %w", callCtx.Err())
			}

			return &bosh.Task{ID: 77, State: "done", Description: "create deployment"}, nil
		}

		vaultClient = internalVault.New(suite.vault.Addr, suite.vault.RootToken, true)

		var err error

		dir, err = os.MkdirTemp("", "blacksmith-inflight")
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
		release()

		Eventually(func() bool { return brokerInstance.ProvisionActive(instanceID) }, 10*time.Second).Should(BeFalse())

		_ = vaultClient.Index(ctx, instanceID, nil)
		_ = os.RemoveAll(dir)
	})

	It("answers the repeated request as accepted and deploys once", func() {
		response, async, err := brokerInstance.Provision(ctx, instanceID, request, true)
		Expect(err).ToNot(HaveOccurred())
		Expect(async).To(BeTrue())
		Expect(response.Operation).To(Equal("provision"))

		Eventually(deployStarted, 10*time.Second).Should(Receive())

		repeated, repeatedAsync, err := brokerInstance.Provision(ctx, instanceID, request, true)
		Expect(err).ToNot(HaveOccurred())
		Expect(repeatedAsync).To(BeTrue(), "the repeated request was not answered as accepted")
		Expect(repeated).To(Equal(response), "the repeated request did not carry the first answer's operation")

		// A second goroutine would reach the director within milliseconds.
		Consistently(deploys.Load, 500*time.Millisecond).Should(Equal(int32(1)), "the repeated request started a second deploy")

		release()

		Eventually(func() bool { return brokerInstance.ProvisionActive(instanceID) }, 10*time.Second).Should(BeFalse())
		Expect(deploys.Load()).To(Equal(int32(1)))

		_, taskID, _, err := vaultClient.State(ctx, instanceID)
		Expect(err).ToNot(HaveOccurred())
		Expect(taskID).To(Equal(77))
	})

	It("refuses a request with different attributes while the first is in flight", func() {
		_, _, err := brokerInstance.Provision(ctx, instanceID, request, true)
		Expect(err).ToNot(HaveOccurred())

		Eventually(deployStarted, 10*time.Second).Should(Receive())

		different := request
		different.SpaceGUID = "another-space"

		_, async, err := brokerInstance.Provision(ctx, instanceID, different, true)
		Expect(errors.Is(err, osbapi.ErrInstanceAlreadyExists)).To(BeTrue(), "the conflicting request was not refused as a conflict: %v", err)
		Expect(async).To(BeFalse())

		Consistently(deploys.Load, 500*time.Millisecond).Should(Equal(int32(1)))
	})

	It("treats an omitted parameters or context object as an empty one", func() {
		_, _, err := brokerInstance.Provision(ctx, instanceID, request, true)
		Expect(err).ToNot(HaveOccurred())

		Eventually(deployStarted, 10*time.Second).Should(Receive())

		empty := request
		empty.Parameters = map[string]any{}
		empty.Context = map[string]any{}

		_, async, err := brokerInstance.Provision(ctx, instanceID, empty, true)
		Expect(err).ToNot(HaveOccurred(), "an empty object was taken for different attributes")
		Expect(async).To(BeTrue())

		Consistently(deploys.Load, 500*time.Millisecond).Should(Equal(int32(1)))
	})

	It("compares a repeat with the request as it arrived, not with maps its caller changed later", func() {
		original := request
		original.Parameters = map[string]any{"memory": map[string]any{"size": "small"}}

		_, _, err := brokerInstance.Provision(ctx, instanceID, original, true)
		Expect(err).ToNot(HaveOccurred())

		Eventually(deployStarted, 10*time.Second).Should(Receive())

		// The caller reuses its maps after the call returned.
		memory, ok := original.Parameters["memory"].(map[string]any)
		Expect(ok).To(BeTrue())

		memory["size"] = "large"

		repeated := request
		repeated.Parameters = map[string]any{"memory": map[string]any{"size": "small"}}

		_, async, err := brokerInstance.Provision(ctx, instanceID, repeated, true)
		Expect(err).ToNot(HaveOccurred(), "the in-flight request shares its maps with the caller")
		Expect(async).To(BeTrue())
	})

	It("refuses an identical repeat while the first request is still being accepted", func() {
		proxy, err := testutil.NewVaultFaultProxy(suite.vault.Addr)
		Expect(err).ToNot(HaveOccurred())

		defer proxy.Close()

		brokerInstance.Vault = internalVault.New(proxy.Addr, suite.vault.RootToken, true)

		// The first request's index write hangs until the record bound runs
		// out, so the first request fails after the repeat has arrived.
		proxy.HoldWrites("secret/data/db", 1)

		defer broker.SetRecordWriteTimeout(1500 * time.Millisecond)()

		type answer struct {
			async bool
			err   error
		}

		first := make(chan answer, 1)

		go func() {
			defer GinkgoRecover()

			_, async, provisionErr := brokerInstance.Provision(ctx, instanceID, request, true)
			first <- answer{async: async, err: provisionErr}
		}()

		Eventually(func() []string { return proxy.WritesTo("secret/data/db") }, 10*time.Second).ShouldNot(BeEmpty(), "the first request never reached its index write")

		_, repeatAsync, repeatErr := brokerInstance.Provision(ctx, instanceID, request, true)

		var firstAnswer answer

		Eventually(first, 10*time.Second).Should(Receive(&firstAnswer))
		Expect(firstAnswer.err).To(HaveOccurred(), "the first request was meant to fail at its held index write")

		// A platform polls only for a request it saw accepted. No such poll
		// may report a provision that the broker never carried out.
		for _, accepted := range []bool{firstAnswer.async && firstAnswer.err == nil, repeatAsync && repeatErr == nil} {
			if !accepted {
				continue
			}

			response, pollErr := brokerInstance.LastOperation(ctx, instanceID, osbapi.LastOperationRequest{
				ServiceID: serviceID, PlanID: planID, Operation: "provision",
			})
			Expect(pollErr).ToNot(HaveOccurred())
			Expect(response.State).ToNot(Equal(osbapi.StateSucceeded), "a poll reported a provision that failed as succeeded (%q)", response.Description)
		}

		Expect(errors.Is(repeatErr, osbapi.ErrConcurrencyError)).To(BeTrue(), "the repeat was not refused as a concurrent request: async=%t err=%v", repeatAsync, repeatErr)
		Expect(repeatAsync).To(BeFalse())
		Expect(deploys.Load()).To(BeZero())

		// The refused platform retries, and the retry runs its own accept chain.
		_, async, err := brokerInstance.Provision(ctx, instanceID, request, true)
		Expect(err).ToNot(HaveOccurred())
		Expect(async).To(BeTrue())

		Eventually(deployStarted, 10*time.Second).Should(Receive())

		release()

		Eventually(func() bool { return brokerInstance.ProvisionActive(instanceID) }, 10*time.Second).Should(BeFalse())
	})

	It("clears the claim when the goroutine ends", func() {
		_, _, err := brokerInstance.Provision(ctx, instanceID, request, true)
		Expect(err).ToNot(HaveOccurred())

		Eventually(deployStarted, 10*time.Second).Should(Receive())
		release()

		Eventually(func() bool { return brokerInstance.ProvisionActive(instanceID) }, 10*time.Second).Should(BeFalse(), "the claim outlived the goroutine")

		// An identical request that arrives later is not answered from a
		// stale claim, so it is not refused.
		_, async, err := brokerInstance.Provision(ctx, instanceID, request, true)
		Expect(err).ToNot(HaveOccurred())
		Expect(async).To(BeTrue())
	})

	It("clears the claim when the accept chain fails, so a retry is not refused", func() {
		// An unknown plan fails the accept chain after the claim is taken.
		unknown := request
		unknown.PlanID = "no-such-plan"

		_, _, err := brokerInstance.Provision(ctx, instanceID, unknown, true)
		Expect(err).To(HaveOccurred())
		Expect(brokerInstance.ProvisionActive(instanceID)).To(BeFalse(), "a refused provision left its claim behind")

		// The refused request left a partial index entry, so the retry asks
		// the director for its deployment, which does not exist.
		director.GetDeploymentFn = deploymentNotFound

		_, async, err := brokerInstance.Provision(ctx, instanceID, request, true)
		Expect(err).ToNot(HaveOccurred())
		Expect(async).To(BeTrue())

		Eventually(deployStarted, 10*time.Second).Should(Receive())
	})
})
