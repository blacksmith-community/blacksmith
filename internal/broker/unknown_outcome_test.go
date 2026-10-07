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

	"github.com/fivetwenty-io/boshapi/pkg/boshapi"
	"github.com/fivetwenty-io/osbapi/v2/pkg/osbapi"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

const (
	outcomeDirectorUUID     = "director-uuid"
	outcomeOrgGUID          = "org-guid"
	outcomeSpaceGUID        = "space-guid"
	outcomeCreateDeployment = "create deployment"
	outcomeDeleteDeployment = "delete deployment"
	outcomeManifestName     = "placeholder"
	outcomeTaskDone         = "done"
	outcomeTaskProcessing   = "processing"
)

// newOutcomeBroker returns a broker for the valkey standalone plan whose
// init script does nothing, and the directory that holds the script.
func newOutcomeBroker(director bosh.Director, vaultClient *internalVault.Vault, serviceCtx context.Context) (*broker.Broker, string) {
	dir, err := os.MkdirTemp("", "blacksmith-outcome")
	ExpectWithOffset(1, err).ToNot(HaveOccurred())

	initScript := filepath.Join(dir, "init")
	ExpectWithOffset(1, os.WriteFile(initScript, []byte("#!/bin/bash\nexit 0\n"), 0o600)).To(Succeed())

	manifest := map[interface{}]interface{}{}
	manifest[manifestNameKey] = outcomeManifestName
	manifest[manifestInstanceGroupsKey] = []interface{}{}

	plan := services.Plan{
		ID:             standalonePlanID,
		Name:           standalonePlanID,
		Type:           valkeyServiceID,
		InitScriptPath: initScript,
		Manifest:       manifest,
		Service:        &services.Service{ID: valkeyServiceID, Name: valkeyServiceID},
	}

	return &broker.Broker{
		Plans:          map[string]services.Plan{valkeyServiceID + "/" + standalonePlanID: plan},
		BOSH:           director,
		Vault:          vaultClient,
		Shield:         &shield.NoopClient{},
		Config:         &config.Config{},
		InstanceLocks:  make(map[string]*sync.Mutex),
		ServiceContext: serviceCtx,
	}, dir
}

// Keys of a service plan's manifest.
const (
	manifestNameKey           = "name"
	manifestInstanceGroupsKey = "instance_groups"
)

// sentRequest is what a director double saw when the broker sent it a
// mutation: the context ID the request carried, and the instance's task
// record as it stood while the request was being sent.
type sentRequest struct {
	contextID string
	record    map[string]interface{}
}

// The broker chooses the context ID of each deploy and delete it sends, and
// it records the ID before the request goes out, so the outcome of a request
// whose answer is lost can be looked up by that ID.
var _ = Describe("Context IDs of director mutations", func() {
	const (
		createTaskID = 77
		deleteTaskID = 300
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
	)

	readRecord := func() map[string]interface{} {
		record, err := suite.vault.ReadSecret(instanceID + "/task")
		ExpectWithOffset(1, err).ToNot(HaveOccurred())

		return record
	}

	settled := func() bool {
		return !brokerInstance.ProvisionActive(instanceID) && !brokerInstance.DeprovisionActive(instanceID)
	}

	indexInstance := func() {
		ExpectWithOffset(1, vaultClient.Index(ctx, instanceID, vaultPkg.Instance{
			ID: instanceID, ServiceID: serviceID, PlanID: planID, DeploymentName: deploymentName,
		})).To(Succeed())
	}

	provision := func() {
		_, async, err := brokerInstance.Provision(ctx, instanceID, osbapi.ProvisionRequest{
			ServiceID: serviceID, PlanID: planID, OrganizationGUID: outcomeOrgGUID, SpaceGUID: outcomeSpaceGUID,
		}, true)
		ExpectWithOffset(1, err).ToNot(HaveOccurred())
		ExpectWithOffset(1, async).To(BeTrue())
	}

	deprovision := func() {
		_, async, err := brokerInstance.Deprovision(ctx, instanceID, osbapi.DeprovisionRequest{ServiceID: serviceID, PlanID: planID}, true)
		ExpectWithOffset(1, err).ToNot(HaveOccurred())
		ExpectWithOffset(1, async).To(BeTrue())
	}

	// capture returns what the director saw when the request was sent. The
	// record is read while the request is in flight, so it is the record a
	// broker restart at that moment would leave.
	capture := func(callCtx context.Context) sentRequest {
		record, _ := suite.vault.ReadSecret(instanceID + "/task")

		return sentRequest{contextID: boshapi.ContextIDFrom(callCtx), record: record}
	}

	// expectRecordedBeforeSending checks that the request carried a fresh
	// context ID and that the record already held it.
	expectRecordedBeforeSending := func(sent sentRequest, action string) {
		ExpectWithOffset(1, sent.contextID).ToNot(BeEmpty(), "the request carried no context ID")

		_, err := uuid.Parse(sent.contextID)
		ExpectWithOffset(1, err).ToNot(HaveOccurred(), "the context ID %q is not a UUID", sent.contextID)

		ExpectWithOffset(1, sent.record).To(HaveKeyWithValue("action", action))
		ExpectWithOffset(1, sent.record).To(HaveKeyWithValue("context_id", sent.contextID),
			"the record did not hold the context ID while the request was being sent")
	}

	// scriptDeploy makes the deploy return task 77 and sends what the
	// director saw to the returned channel.
	scriptDeploy := func() chan sentRequest {
		sent := make(chan sentRequest, 1)

		director.onCreateDeployment = func(callCtx context.Context, _ string) (*bosh.Task, error) {
			sent <- capture(callCtx)

			return &bosh.Task{ID: createTaskID, State: outcomeTaskDone, Description: outcomeCreateDeployment}, nil
		}
		director.onGetEvents = func(context.Context, string) ([]bosh.Event, error) {
			return deploymentEvents(boshEventCreate, deploymentName, createTaskID), nil
		}

		return sent
	}

	// scriptDelete makes the deployment exist and the delete return task
	// 300, and sends what the director saw to the returned channel.
	scriptDelete := func() chan sentRequest {
		sent := make(chan sentRequest, 1)

		director.onGetDeployment = func(context.Context, string) (*bosh.DeploymentDetail, error) {
			return &bosh.DeploymentDetail{Name: deploymentName, Manifest: "name: " + deploymentName}, nil
		}
		director.onDeleteDeployment = func(callCtx context.Context, _ string) (*bosh.Task, error) {
			sent <- capture(callCtx)

			return &bosh.Task{ID: deleteTaskID, State: outcomeTaskProcessing, Description: outcomeDeleteDeployment}, nil
		}
		director.onGetEvents = func(context.Context, string) ([]bosh.Event, error) {
			return deploymentEvents(boshEventDelete, deploymentName, deleteTaskID), nil
		}
		director.GetTaskFn = func(taskID int) (*bosh.Task, error) {
			return &bosh.Task{ID: taskID, State: outcomeTaskProcessing}, nil
		}

		return sent
	}

	BeforeEach(func() {
		ctx = context.Background()
		instanceID = fmt.Sprintf("context-id-%d", time.Now().UnixNano())
		serviceID = valkeyServiceID
		planID = standalonePlanID
		deploymentName = planID + "-" + instanceID

		var serviceCtx context.Context

		serviceCtx, shutdown = context.WithCancel(ctx)

		director = &contextDirector{ScriptedBOSHDirector: testutil.NewScriptedBOSHDirector()}
		director.GetInfoFn = func() (*bosh.Info, error) { return &bosh.Info{UUID: outcomeDirectorUUID}, nil }
		director.GetReleasesFn = func() ([]bosh.Release, error) { return []bosh.Release{}, nil }
		director.FindRunningTaskForDeploymentFn = func(string) (*bosh.Task, error) { return nil, nil } //nolint:nilnil // no running task is a nil task without an error

		vaultClient = internalVault.New(suite.vault.Addr, suite.vault.RootToken, true)
		brokerInstance, dir = newOutcomeBroker(director, vaultClient, serviceCtx)
	})

	AfterEach(func() {
		stopBrokerGoroutines(shutdown)

		_ = vaultClient.Index(ctx, instanceID, nil)
		_ = os.RemoveAll(dir)
	})

	Describe("the accepted record carries the context ID sent to the director", func() {
		It("for a deploy", func() {
			sent := scriptDeploy()

			provision()

			var deploy sentRequest
			Eventually(sent, 10*time.Second).Should(Receive(&deploy))
			expectRecordedBeforeSending(deploy, "provision")
		})

		It("for a delete", func() {
			indexInstance()

			sent := scriptDelete()

			deprovision()

			var deleteRequest sentRequest
			Eventually(sent, 10*time.Second).Should(Receive(&deleteRequest))
			expectRecordedBeforeSending(deleteRequest, "deprovision")
		})

		It("for the delete of a deployment whose instance was deprovisioned during the deploy", func() {
			sent := scriptDelete()

			brokerInstance.TearDownAbandonedDeployment(ctx, instanceID, deploymentName)

			var deleteRequest sentRequest
			Eventually(sent, 10*time.Second).Should(Receive(&deleteRequest))
			expectRecordedBeforeSending(deleteRequest, "deprovision")
		})

		It("with an ID no earlier operation used", func() {
			indexInstance()

			first := scriptDelete()

			deprovision()

			var firstDelete sentRequest
			Eventually(first, 10*time.Second).Should(Receive(&firstDelete))
			Eventually(settled, 10*time.Second).Should(BeTrue())

			second := scriptDelete()

			brokerInstance.TearDownAbandonedDeployment(ctx, instanceID, deploymentName)

			var secondDelete sentRequest
			Eventually(second, 10*time.Second).Should(Receive(&secondDelete))
			Expect(secondDelete.contextID).ToNot(Equal(firstDelete.contextID), "two operations sent the same context ID")
			expectRecordedBeforeSending(secondDelete, "deprovision")
		})
	})

	Describe("progress written after acceptance keeps the context ID", func() {
		It("when the deploy task is recorded", func() {
			sent := scriptDeploy()

			provision()

			var deploy sentRequest
			Eventually(sent, 10*time.Second).Should(Receive(&deploy))
			Eventually(settled, 10*time.Second).Should(BeTrue())

			recorded, err := brokerInstance.RecordedOperation(ctx, instanceID)
			Expect(err).ToNot(HaveOccurred())
			Expect(recorded.TaskID).To(Equal(createTaskID))
			Expect(recorded.ContextID).To(Equal(deploy.contextID))
		})

		It("when the delete task is recorded", func() {
			indexInstance()

			sent := scriptDelete()

			deprovision()

			var deleteRequest sentRequest
			Eventually(sent, 10*time.Second).Should(Receive(&deleteRequest))
			Eventually(func() int {
				recorded, err := brokerInstance.RecordedOperation(ctx, instanceID)
				Expect(err).ToNot(HaveOccurred())

				return recorded.TaskID
			}, 10*time.Second).Should(Equal(deleteTaskID))
			Expect(readRecord()).To(HaveKeyWithValue("context_id", deleteRequest.contextID))
		})

		It("while other progress writes of the operation run at the same time", func() {
			const (
				rounds  = 12
				writers = 8
			)

			for round := range rounds {
				contextID := uuid.NewString()

				var (
					waitGroup sync.WaitGroup
					start     = make(chan struct{})
					recordErr error
				)

				waitGroup.Add(1)

				go func() {
					defer waitGroup.Done()

					<-start

					recordErr = vaultClient.TrackProgressWithContextID(ctx, instanceID, "provision", fmt.Sprintf("Round %d", round), contextID, 0, nil)
				}()

				for writer := range writers {
					waitGroup.Add(1)

					go func() {
						defer waitGroup.Done()

						<-start

						brokerInstance.RecordProgress(ctx, instanceID, "provision", fmt.Sprintf("Round %d, step %d", round, writer))
					}()
				}

				close(start)
				waitGroup.Wait()

				Expect(recordErr).ToNot(HaveOccurred())
				Expect(readRecord()).To(HaveKeyWithValue("context_id", contextID),
					"a progress write in round %d put back the context ID it read before the new one was recorded", round)
			}
		})

		It("only for the same action", func() {
			Expect(vaultClient.TrackProgressWithContextID(ctx, instanceID, "provision", "Creating BOSH deployment", uuid.NewString(), 0, nil)).To(Succeed())

			brokerInstance.RecordProgress(ctx, instanceID, "deprovision", "Starting deprovisioning process")

			Expect(readRecord()).ToNot(HaveKey("context_id"), "a deprovision took over the context ID of the provision before it")
		})
	})
})
