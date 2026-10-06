package broker_test

import (
	"context"
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

// The provision and deprovision goroutines record each step in Vault. Every
// record is written under a context of its own, which keeps the step's values
// but neither its cancellation nor its deadline, and has a short bound of its
// own. These specs run the goroutines through the paths that make each record
// and end the goroutine's context, by shutting the broker down, just before
// the record is written. A record written under the goroutine's context would
// fail there. The specs that hold a write at the Vault proxy check the other
// half, that a stalled write gives up within its own bound and does not hold
// the goroutine for as long as the goroutine's context lives.
var _ = Describe("Record writes of the provision and deprovision goroutines", func() {
	const (
		// recordBound is the record write bound these specs set. A held
		// write gives up after it, while one under the goroutine's context
		// stays held for the proxy's full limit.
		recordBound = 500 * time.Millisecond

		// pastRecordBound is how long a spec waits for the step after a held
		// write. It is well past recordBound and well short of the proxy's
		// limit on a held request.
		pastRecordBound = 5 * time.Second
	)

	var (
		ctx            context.Context
		director       *contextDirector
		vaultClient    *internalVault.Vault
		brokerInstance *broker.Broker
		instanceID     string
		serviceID      string
		planID         string
		deploymentName string
		shutdown       context.CancelFunc
		dir            string
		cleanups       []func()
	)

	readSecret := func(path string) map[string]interface{} {
		data, err := suite.vault.ReadSecret(path)
		ExpectWithOffset(1, err).ToNot(HaveOccurred(), "reading %s", path)

		return data
	}

	// history returns the descriptions in the instance's history, oldest
	// first.
	history := func() []string {
		metadata, err := suite.vault.ReadSecret(instanceID + "/metadata")
		ExpectWithOffset(1, err).ToNot(HaveOccurred())

		entries, ok := metadata["history"].([]interface{})
		ExpectWithOffset(1, ok).To(BeTrue(), "the metadata has no history")

		descriptions := make([]string, 0, len(entries))

		for _, raw := range entries {
			entry, isMap := raw.(map[string]interface{})
			if !isMap {
				continue
			}

			description, isString := entry["description"].(string)
			if isString {
				descriptions = append(descriptions, description)
			}
		}

		return descriptions
	}

	taskDescription := func() string {
		description, _ := readSecret(instanceID + "/task")["description"].(string)

		return description
	}

	stillIndexed := func() bool {
		_, exists, err := vaultClient.FindInstance(ctx, instanceID)
		ExpectWithOffset(1, err).ToNot(HaveOccurred())

		return exists
	}

	indexInstance := func() {
		ExpectWithOffset(1, vaultClient.Index(ctx, instanceID, vaultPkg.Instance{
			ID: instanceID, ServiceID: serviceID, PlanID: planID, DeploymentName: deploymentName,
		})).To(Succeed())
	}

	provision := func() {
		_, async, err := brokerInstance.Provision(ctx, instanceID, osbapi.ProvisionRequest{
			ServiceID: serviceID, PlanID: planID, OrganizationGUID: "org-guid", SpaceGUID: "space-guid",
		}, true)
		ExpectWithOffset(1, err).ToNot(HaveOccurred())
		ExpectWithOffset(1, async).To(BeTrue())
	}

	deprovision := func() {
		_, async, err := brokerInstance.Deprovision(ctx, instanceID, osbapi.DeprovisionRequest{ServiceID: serviceID, PlanID: planID}, true)
		ExpectWithOffset(1, err).ToNot(HaveOccurred())
		ExpectWithOffset(1, async).To(BeTrue())
	}

	settled := func() bool {
		return !brokerInstance.ProvisionActive(instanceID) && !brokerInstance.DeprovisionActive(instanceID)
	}

	// useProxy puts a fault proxy between the broker and Vault and bounds
	// each record write by recordBound.
	useProxy := func() *testutil.VaultFaultProxy {
		proxy, err := testutil.NewVaultFaultProxy(suite.vault.Addr)
		ExpectWithOffset(1, err).ToNot(HaveOccurred())

		cleanups = append(cleanups, proxy.Close, broker.SetRecordWriteTimeout(recordBound))

		brokerInstance.Vault = internalVault.New(proxy.Addr, suite.vault.RootToken, true)

		return proxy
	}

	deploymentExists := func(context.Context, string) (*bosh.DeploymentDetail, error) {
		return &bosh.DeploymentDetail{Name: deploymentName, Manifest: "name: " + deploymentName}, nil
	}

	deploymentGone := func(context.Context, string) (*bosh.DeploymentDetail, error) {
		return nil, fmt.Errorf("%w: %s", bosh.ErrDeploymentNotFound, deploymentName)
	}

	BeforeEach(func() {
		ctx = context.Background()
		instanceID = fmt.Sprintf("record-writes-%d", time.Now().UnixNano())
		serviceID = "valkey"
		planID = "standalone"
		deploymentName = planID + "-" + instanceID

		var serviceCtx context.Context

		serviceCtx, shutdown = context.WithCancel(ctx)

		director = &contextDirector{ScriptedBOSHDirector: testutil.NewScriptedBOSHDirector()}
		director.GetInfoFn = func() (*bosh.Info, error) { return &bosh.Info{UUID: "director-uuid"}, nil }
		director.GetReleasesFn = func() ([]bosh.Release, error) { return []bosh.Release{}, nil }
		director.FindRunningTaskForDeploymentFn = func(string) (*bosh.Task, error) { return nil, nil } //nolint:nilnil // no running task is a nil task without an error

		vaultClient = internalVault.New(suite.vault.Addr, suite.vault.RootToken, true)

		var err error

		dir, err = os.MkdirTemp("", "blacksmith-record-writes")
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
	})

	AfterEach(func() {
		shutdown()

		Eventually(settled, 40*time.Second).Should(BeTrue())

		for _, cleanup := range cleanups {
			cleanup()
		}

		cleanups = nil

		_ = vaultClient.Index(ctx, instanceID, nil)
		_ = os.RemoveAll(dir)
	})

	Describe("the provision goroutine", func() {
		It("records its start, its steps, and its failure when the broker shut down before it began", func() {
			director.onGetInfo = func(callCtx context.Context) (*bosh.Info, error) {
				return nil, fmt.Errorf("director not asked: %w", callCtx.Err())
			}

			shutdown()
			provision()

			Eventually(settled, 10*time.Second).Should(BeTrue())

			Expect(history()).To(ContainElements("Starting provisioning process", "Parsing service parameters"))

			state, _, _, err := vaultClient.State(ctx, instanceID)
			Expect(err).ToNot(HaveOccurred())
			Expect(state).To(Equal("failed"))
			Expect(taskDescription()).To(HavePrefix("Provision stopped by broker shutdown: "))
		})

		It("records the deploy task when the broker shuts down as the deploy returns", func() {
			director.onCreateDeployment = func(context.Context, string) (*bosh.Task, error) {
				shutdown()

				return &bosh.Task{ID: 77, State: "done", Description: "create deployment"}, nil
			}
			director.onGetEvents = func(context.Context, string) ([]bosh.Event, error) {
				return deploymentEvents(boshEventCreate, deploymentName, 77), nil
			}

			provision()

			Eventually(settled, 10*time.Second).Should(BeTrue())

			state, task, _, err := vaultClient.State(ctx, instanceID)
			Expect(err).ToNot(HaveOccurred())
			Expect(task).To(Equal(77), "the deploy task was not recorded")
			Expect(state).To(Equal("in_progress"))
		})

		It("gives up a stalled write of the init script and goes on to deploy", func() {
			proxy := useProxy()
			proxy.HoldWrites(instanceID+"/init", 1)

			deployStarted := make(chan struct{}, 1)

			director.onCreateDeployment = func(context.Context, string) (*bosh.Task, error) {
				deployStarted <- struct{}{}

				return &bosh.Task{ID: 77, State: "done", Description: "create deployment"}, nil
			}
			director.onGetEvents = func(context.Context, string) ([]bosh.Event, error) {
				return deploymentEvents(boshEventCreate, deploymentName, 77), nil
			}

			provision()

			Eventually(deployStarted, pastRecordBound).Should(Receive(), "the provision is still waiting on the init script write")
			Expect(proxy.WritesTo(instanceID + "/init")).To(HaveLen(1))
		})

		It("gives up a stalled write of the manifest and records the failure", func() {
			proxy := useProxy()
			proxy.HoldWrites(instanceID+"/manifest", 1)

			provision()

			Eventually(taskDescription, pastRecordBound).Should(HavePrefix("Failed to store manifest in Vault"), "the provision is still waiting on the manifest write")
			Expect(director.Calls("CreateDeployment")).To(BeEmpty())
		})
	})

	Describe("the deprovision goroutine", func() {
		BeforeEach(func() {
			indexInstance()
		})

		It("records its start, its steps, and the removal of a deployment already gone when the broker shut down before it began", func() {
			director.onGetDeployment = deploymentGone

			shutdown()
			deprovision()

			Eventually(settled, 10*time.Second).Should(BeTrue())

			Expect(stillIndexed()).To(BeFalse(), "the instance was left in the index")
			Expect(history()).To(ContainElements("Starting deprovisioning process", "Checking BOSH deployment status", "Deprovisioning completed (no deployment)"))
			Expect(taskDescription()).To(Equal("Operation deprovision in progress"), "the completion was not recorded")
		})

		It("records the removal of a deployment that went away during the delete when the broker shuts down then", func() {
			var (
				readsMu sync.Mutex
				reads   int
			)

			director.onGetDeployment = func(callCtx context.Context, name string) (*bosh.DeploymentDetail, error) {
				readsMu.Lock()
				reads++
				read := reads
				readsMu.Unlock()

				// The first read finds the deployment. The retry's read
				// finds it gone, and the broker shuts down as it does.
				if read == 1 {
					return deploymentExists(callCtx, name)
				}

				shutdown()

				return deploymentGone(callCtx, name)
			}

			deprovision()

			Eventually(settled, 10*time.Second).Should(BeTrue())

			Expect(stillIndexed()).To(BeFalse(), "the instance was left in the index")
			Expect(history()).To(ContainElement("Deprovisioning completed (cleanup during retry)"))
			Expect(taskDescription()).To(Equal("Operation deprovision in progress"), "the completion was not recorded")
		})

		It("records a failed delete attempt when the broker shuts down as it fails", func() {
			director.onGetDeployment = deploymentExists
			director.onDeleteDeployment = func(context.Context, string) (*bosh.Task, error) {
				shutdown()

				return nil, errDeleteFailed
			}

			deprovision()

			Eventually(settled, 10*time.Second).Should(BeTrue())

			Expect(taskDescription()).To(HavePrefix("Deletion retry 1/"), "the failed attempt was not recorded")
		})

		It("records the delete task when the broker shuts down as the delete returns", func() {
			director.onGetDeployment = deploymentExists
			director.onDeleteDeployment = func(context.Context, string) (*bosh.Task, error) {
				shutdown()

				return &bosh.Task{ID: 300, State: "done", Description: "delete deployment"}, nil
			}
			director.onGetEvents = func(context.Context, string) ([]bosh.Event, error) {
				return deploymentEvents(boshEventDelete, deploymentName, 300), nil
			}

			deprovision()

			Eventually(settled, 10*time.Second).Should(BeTrue())

			state, task, _, err := vaultClient.State(ctx, instanceID)
			Expect(err).ToNot(HaveOccurred())
			Expect(task).To(Equal(300), "the delete task was not recorded")
			Expect(state).To(Equal("in_progress"))
		})
	})

	It("gives up a stalled first record of an abandoned deployment's teardown", func() {
		proxy := useProxy()
		proxy.HoldWrites(instanceID+"/task", 1)

		director.onGetDeployment = deploymentGone

		done := make(chan struct{})

		go func() {
			defer GinkgoRecover()
			defer close(done)

			brokerInstance.TearDownAbandonedDeployment(ctx, instanceID, deploymentName)
		}()

		Eventually(done, pastRecordBound).Should(BeClosed(), "the teardown is still waiting on its first record")
		Expect(taskDescription()).To(Equal("Operation deprovision in progress"))
	})

	It("dates deleted_at from the index when the background deprovision's context has ended", func() {
		indexed := map[string]interface{}{
			"instance_id":              instanceID,
			"service_id":               serviceID,
			"plan_id":                  planID,
			"deployment_name":          deploymentName,
			"deprovision_requested_at": "2026-03-01T00:00:00Z",
		}
		Expect(vaultClient.Index(ctx, instanceID, indexed)).To(Succeed())
		Expect(suite.vault.WriteSecret(instanceID+"/metadata", map[string]interface{}{
			"delete_requested_at": "2026-01-01T00:00:00Z",
			"deleted_at":          "2026-01-01T00:05:00Z",
		})).To(Succeed())

		ended, end := context.WithCancel(ctx)
		end()

		brokerInstance.StoreDeletedTimestampFromMonitor(ended, instanceID)

		raw, ok := readSecret(instanceID + "/metadata")["deleted_at"].(string)
		Expect(ok).To(BeTrue())

		recorded, err := time.Parse(time.RFC3339, raw)
		Expect(err).ToNot(HaveOccurred())
		Expect(recorded).To(BeTemporally(">", time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)), "the deletion on record was kept though the index dates a later deprovision")
	})
})
