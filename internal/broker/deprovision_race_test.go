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

var errDirectorUnreachable = errors.New("connection refused")

// Deprovision arriving while a provision is still running used to report
// success, orphan the BOSH deployment, and let the finishing provision recreate
// the vault index entry. These specs pin the corrected behaviour.
var _ = Describe("Deprovision racing an in-flight provision", func() {
	var (
		ctx            context.Context
		brokerInstance *broker.Broker
		vaultClient    *internalVault.Vault
		director       *testutil.ScriptedBOSHDirector
		instanceID     string
		serviceID      string
		planID         string
		deploymentName string
		provisionSpec  osbapi.ProvisionRequest
		deprovisionArg osbapi.DeprovisionRequest
	)

	indexEntry := func() (*vaultPkg.Instance, bool) {
		instance, exists, err := vaultClient.FindInstance(ctx, instanceID)
		Expect(err).ToNot(HaveOccurred())

		return instance, exists
	}

	indexEntryExists := func() bool {
		_, exists := indexEntry()

		return exists
	}

	taskState := func() string {
		state, _, _, err := vaultClient.State(ctx, instanceID)
		Expect(err).ToNot(HaveOccurred())

		return state
	}

	taskAction := func() string {
		_, _, task, err := vaultClient.State(ctx, instanceID)
		Expect(err).ToNot(HaveOccurred())

		action, _ := task["action"].(string)

		return action
	}

	writeInitScript := func() string {
		dir, err := os.MkdirTemp("", "blacksmith-race")
		Expect(err).ToNot(HaveOccurred())

		path := filepath.Join(dir, "init")
		Expect(os.WriteFile(path, []byte("#!/bin/bash\nexit 0\n"), 0o600)).To(Succeed())

		return path
	}

	BeforeEach(func() {
		ctx = context.Background()
		instanceID = fmt.Sprintf("race-%d", time.Now().UnixNano())
		serviceID = "valkey"
		planID = "standalone"
		deploymentName = planID + "-" + instanceID

		director = testutil.NewScriptedBOSHDirector()
		vaultClient = internalVault.New(suite.vault.Addr, suite.vault.RootToken, true)

		plan := services.Plan{
			ID:             planID,
			Name:           planID,
			Type:           "valkey",
			InitScriptPath: writeInitScript(),
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

		provisionSpec = osbapi.ProvisionRequest{
			ServiceID:        serviceID,
			PlanID:           planID,
			OrganizationGUID: "org-guid",
			SpaceGUID:        "space-guid",
		}
		deprovisionArg = osbapi.DeprovisionRequest{ServiceID: serviceID, PlanID: planID}
	})

	AfterEach(func() {
		_ = vaultClient.Index(ctx, instanceID, nil)
	})

	Context("while the provision goroutine has not finished", func() {
		var releaseDirector chan struct{}

		BeforeEach(func() {
			releaseDirector = make(chan struct{})
			director.GetInfoFn = func() (*bosh.Info, error) {
				select {
				case <-releaseDirector:
				case <-time.After(10 * time.Second):
				}

				return nil, errDirectorUnreachable
			}
		})

		AfterEach(func() {
			select {
			case <-releaseDirector:
			default:
				close(releaseDirector)
			}
		})

		It("answers with the OSB concurrency error and leaves the index intact", func() {
			_, _, err := brokerInstance.Provision(ctx, instanceID, provisionSpec, true)
			Expect(err).ToNot(HaveOccurred())

			Eventually(func() []string { return director.Calls("GetInfo") }).ShouldNot(BeEmpty())

			_, _, err = brokerInstance.Deprovision(ctx, instanceID, deprovisionArg, true)
			Expect(err).To(MatchError(osbapi.ErrConcurrencyError))

			_, exists := indexEntry()
			Expect(exists).To(BeTrue())
			Expect(director.Calls("DeleteDeployment")).To(BeEmpty())
			Expect(director.Calls("GetDeployment")).To(BeEmpty())
		})

		It("accepts the deprovision once the provision goroutine has ended", func() {
			_, _, err := brokerInstance.Provision(ctx, instanceID, provisionSpec, true)
			Expect(err).ToNot(HaveOccurred())

			Eventually(func() []string { return director.Calls("GetInfo") }).ShouldNot(BeEmpty())

			close(releaseDirector)
			Eventually(taskState).Should(Equal("failed"))

			director.FindRunningTaskForDeploymentFn = func(string) (*bosh.Task, error) { return nil, nil } //nolint:nilnil // no running task is a nil task without an error
			director.GetDeploymentFn = func(name string) (*bosh.DeploymentDetail, error) {
				return nil, fmt.Errorf("%w: %s", bosh.ErrDeploymentNotFound, name)
			}

			_, _, err = brokerInstance.Deprovision(ctx, instanceID, deprovisionArg, true)
			Expect(err).ToNot(HaveOccurred())

			Eventually(indexEntryExists).Should(BeFalse())
		})
	})

	Context("while BOSH still runs the create deployment task", func() {
		BeforeEach(func() {
			Expect(vaultClient.Index(ctx, instanceID, vaultPkg.Instance{
				ID: instanceID, ServiceID: serviceID, PlanID: planID, DeploymentName: deploymentName,
			})).To(Succeed())

			director.FindRunningTaskForDeploymentFn = func(deployment string) (*bosh.Task, error) {
				Expect(deployment).To(Equal(deploymentName))

				return &bosh.Task{ID: 189, State: "processing", Description: "create deployment"}, nil
			}
		})

		It("answers with the OSB concurrency error and leaves the index intact", func() {
			_, _, err := brokerInstance.Deprovision(ctx, instanceID, deprovisionArg, true)
			Expect(err).To(MatchError(osbapi.ErrConcurrencyError))

			instance, exists := indexEntry()
			Expect(exists).To(BeTrue())
			Expect(instance.DeploymentName).To(Equal(deploymentName))
			Expect(director.Calls("DeleteDeployment")).To(BeEmpty())
		})
	})

	Context("when the deployment exists but has no manifest yet", func() {
		BeforeEach(func() {
			Expect(vaultClient.Index(ctx, instanceID, vaultPkg.Instance{
				ID: instanceID, ServiceID: serviceID, PlanID: planID, DeploymentName: deploymentName,
			})).To(Succeed())

			director.FindRunningTaskForDeploymentFn = func(string) (*bosh.Task, error) { return nil, nil } //nolint:nilnil // no running task is a nil task without an error
			director.GetDeploymentFn = func(name string) (*bosh.DeploymentDetail, error) {
				return &bosh.DeploymentDetail{Name: name, Manifest: ""}, nil
			}
			director.DeleteDeploymentFn = func(string) (*bosh.Task, error) {
				return &bosh.Task{ID: 200, State: "processing", Description: "delete deployment"}, nil
			}
			director.GetTaskFn = func(taskID int) (*bosh.Task, error) {
				return &bosh.Task{ID: taskID, State: "processing"}, nil
			}
		})

		It("deletes the deployment instead of treating it as already gone", func() {
			_, _, err := brokerInstance.Deprovision(ctx, instanceID, deprovisionArg, true)
			Expect(err).ToNot(HaveOccurred())

			Eventually(func() []string { return director.Calls("DeleteDeployment") }).Should(Equal([]string{deploymentName}))

			_, exists := indexEntry()
			Expect(exists).To(BeTrue(), "index entry must survive until the delete task completes")
		})
	})

	Context("when the director reports the deployment missing", func() {
		BeforeEach(func() {
			Expect(vaultClient.Index(ctx, instanceID, vaultPkg.Instance{
				ID: instanceID, ServiceID: serviceID, PlanID: planID, DeploymentName: deploymentName,
			})).To(Succeed())

			director.FindRunningTaskForDeploymentFn = func(string) (*bosh.Task, error) { return nil, nil } //nolint:nilnil // no running task is a nil task without an error
			director.GetDeploymentFn = func(name string) (*bosh.DeploymentDetail, error) {
				return nil, fmt.Errorf("%w: %s", bosh.ErrDeploymentNotFound, name)
			}
		})

		It("removes the index entry without issuing a delete", func() {
			_, _, err := brokerInstance.Deprovision(ctx, instanceID, deprovisionArg, true)
			Expect(err).ToNot(HaveOccurred())

			Eventually(indexEntryExists).Should(BeFalse())
			Expect(director.Calls("DeleteDeployment")).To(BeEmpty())
		})
	})

	Context("when the director cannot be reached", func() {
		BeforeEach(func() {
			Expect(vaultClient.Index(ctx, instanceID, vaultPkg.Instance{
				ID: instanceID, ServiceID: serviceID, PlanID: planID, DeploymentName: deploymentName,
			})).To(Succeed())

			director.FindRunningTaskForDeploymentFn = func(string) (*bosh.Task, error) { return nil, nil } //nolint:nilnil // no running task is a nil task without an error
			director.GetDeploymentFn = func(string) (*bosh.DeploymentDetail, error) {
				return nil, errDirectorUnreachable
			}
		})

		It("fails the deprovision and keeps the index entry", func() {
			_, _, err := brokerInstance.Deprovision(ctx, instanceID, deprovisionArg, true)
			Expect(err).ToNot(HaveOccurred())

			Eventually(taskState).Should(Equal("failed"))

			_, exists := indexEntry()
			Expect(exists).To(BeTrue())
			Expect(director.Calls("DeleteDeployment")).To(BeEmpty())
		})
	})

	Context("when the instance is deleted while the create deployment task runs", func() {
		var (
			deployStarted chan struct{}
			finishDeploy  chan struct{}
		)

		BeforeEach(func() {
			deployStarted = make(chan struct{})
			finishDeploy = make(chan struct{})

			director.GetInfoFn = func() (*bosh.Info, error) { return &bosh.Info{UUID: "director-uuid"}, nil }
			director.GetReleasesFn = func() ([]bosh.Release, error) { return []bosh.Release{}, nil }
			director.CreateDeploymentFn = func(string) (*bosh.Task, error) {
				close(deployStarted)

				select {
				case <-finishDeploy:
				case <-time.After(10 * time.Second):
				}

				return &bosh.Task{ID: 189, State: "done", Description: "create deployment"}, nil
			}
			director.GetDeploymentFn = func(name string) (*bosh.DeploymentDetail, error) {
				return &bosh.DeploymentDetail{Name: name, Manifest: "name: " + name}, nil
			}
			director.DeleteDeploymentFn = func(string) (*bosh.Task, error) {
				return &bosh.Task{ID: 190, State: "processing", Description: "delete deployment"}, nil
			}
			director.GetTaskFn = func(taskID int) (*bosh.Task, error) {
				return &bosh.Task{ID: taskID, State: "processing"}, nil
			}
		})

		It("deletes the finished deployment instead of recording it as provisioned", func() {
			_, _, err := brokerInstance.Provision(ctx, instanceID, provisionSpec, true)
			Expect(err).ToNot(HaveOccurred())

			Eventually(deployStarted, 10*time.Second).Should(BeClosed())

			// The deprovision path removed the instance while BOSH was deploying.
			Expect(vaultClient.Index(ctx, instanceID, nil)).To(Succeed())

			close(finishDeploy)

			Eventually(func() []string { return director.Calls("DeleteDeployment") }).Should(Equal([]string{deploymentName}))
			Eventually(taskAction).Should(Equal("deprovision"))

			_, exists := indexEntry()
			Expect(exists).To(BeFalse(), "provision completion must not recreate the index entry")
		})
	})
})
