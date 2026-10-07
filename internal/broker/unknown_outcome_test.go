package broker_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
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
	outcomeStateFailed      = "failed"
	outcomeStateUnknown     = "outcome_unknown"
	outcomeDeprovision      = "deprovision"
	outcomeProvision        = "provision"
	outcomeDirectorAddress  = "https://director.example:25555"
)

// errAnswerLost is what a request whose answer never came back saw.
var errAnswerLost = errors.New("the connection closed after the request was sent")

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
// whose answer is lost can be looked up by that ID. Only the director's word
// that a deployment is gone lets a delete remove the instance.
var _ = Describe("Deploys and deletes sent to the director", func() {
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
		cleanups       []func()
	)

	readRecord := func() map[string]interface{} {
		record, err := suite.vault.ReadSecret(instanceID + "/task")
		ExpectWithOffset(1, err).ToNot(HaveOccurred())

		return record
	}

	history := func() []string {
		metadata, err := suite.vault.ReadSecret(instanceID + "/metadata")
		ExpectWithOffset(1, err).ToNot(HaveOccurred())

		entries, _ := metadata["history"].([]interface{})
		descriptions := make([]string, 0, len(entries))

		for _, raw := range entries {
			entry, _ := raw.(map[string]interface{})
			if description, isString := entry["description"].(string); isString {
				descriptions = append(descriptions, description)
			}
		}

		return descriptions
	}

	stillIndexed := func() bool {
		_, exists, err := vaultClient.FindInstance(ctx, instanceID)
		ExpectWithOffset(1, err).ToNot(HaveOccurred())

		return exists
	}

	useCleaner := func() *recordingCleaner {
		cleaner := newRecordingCleaner(cleanerSucceeds)
		brokerInstance.CredentialCleaner = cleaner

		return cleaner
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

	// pollUnindexed polls the deprovision of the instance, which has no
	// index entry, with the plan the platform names in each poll.
	pollUnindexed := func(operation string) osbapi.LastOperationResponse {
		response, err := brokerInstance.LastOperation(ctx, instanceID, osbapi.LastOperationRequest{
			ServiceID: serviceID, PlanID: planID, Operation: operation,
		})
		ExpectWithOffset(1, err).ToNot(HaveOccurred())

		return response
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

		for _, cleanup := range cleanups {
			cleanup()
		}

		cleanups = nil

		_ = vaultClient.Index(ctx, instanceID, nil)
		_ = os.RemoveAll(dir)
	})

	// useProxy puts a fault proxy between the broker and Vault, and bounds
	// each record write, so a write the proxy holds gives up.
	useProxy := func() *testutil.VaultFaultProxy {
		proxy, err := testutil.NewVaultFaultProxy(suite.vault.Addr)
		ExpectWithOffset(1, err).ToNot(HaveOccurred())

		cleanups = append(cleanups, proxy.Close, broker.SetRecordWriteTimeout(time.Second))

		brokerInstance.Vault = internalVault.New(proxy.Addr, suite.vault.RootToken, true)

		return proxy
	}

	Describe("the accepted record carries the context ID sent to the director", func() {
		It("for a deploy", func() {
			sent := scriptDeploy()

			provision()

			var deploy sentRequest
			Eventually(sent, 10*time.Second).Should(Receive(&deploy))
			expectRecordedBeforeSending(deploy, outcomeProvision)
		})

		It("for a delete", func() {
			indexInstance()

			sent := scriptDelete()

			deprovision()

			var deleteRequest sentRequest
			Eventually(sent, 10*time.Second).Should(Receive(&deleteRequest))
			expectRecordedBeforeSending(deleteRequest, outcomeDeprovision)
		})

		It("for the delete of a deployment whose instance was deprovisioned during the deploy", func() {
			sent := scriptDelete()

			brokerInstance.TearDownAbandonedDeployment(ctx, instanceID, deploymentName)

			var deleteRequest sentRequest
			Eventually(sent, 10*time.Second).Should(Receive(&deleteRequest))
			expectRecordedBeforeSending(deleteRequest, outcomeDeprovision)
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
			expectRecordedBeforeSending(secondDelete, outcomeDeprovision)
		})
	})

	Describe("the context ID is written again just before the request is sent", func() {
		const refusal = "permission denied"

		It("for a deploy, after an earlier progress write lost it", func() {
			proxy := useProxy()

			// The progress write after the director check reads the record
			// it replaces and fails to, so it keeps no context ID.
			director.GetInfoFn = func() (*bosh.Info, error) {
				proxy.FailReads(instanceID+"/task", http.StatusForbidden, refusal, 1)

				return &bosh.Info{UUID: outcomeDirectorUUID}, nil
			}

			var beforeRewrite map[string]interface{}

			director.GetReleasesFn = func() ([]bosh.Release, error) {
				beforeRewrite, _ = suite.vault.ReadSecret(instanceID + "/task")

				return []bosh.Release{}, nil
			}

			sent := scriptDeploy()

			provision()

			var deploy sentRequest
			Eventually(sent, 10*time.Second).Should(Receive(&deploy))
			Expect(beforeRewrite).ToNot(HaveKey("context_id"), "no progress write lost the context ID")
			expectRecordedBeforeSending(deploy, outcomeProvision)
		})

		It("for a delete, when the read of the record it replaces fails", func() {
			indexInstance()

			sent := scriptDelete()
			proxy := useProxy()

			var checked atomic.Bool

			director.onGetDeployment = func(context.Context, string) (*bosh.DeploymentDetail, error) {
				if !checked.Swap(true) {
					proxy.FailReads(instanceID+"/task", http.StatusForbidden, refusal, 1)
				}

				return &bosh.DeploymentDetail{Name: deploymentName, Manifest: "name: " + deploymentName}, nil
			}

			deprovision()

			var deleteRequest sentRequest
			Eventually(sent, 10*time.Second).Should(Receive(&deleteRequest))
			expectRecordedBeforeSending(deleteRequest, outcomeDeprovision)
		})

		It("for a teardown, whose first record already names its own context ID", func() {
			previous := uuid.NewString()
			Expect(vaultClient.TrackProgressWithContextID(ctx, instanceID, outcomeDeprovision, "Deprovisioning completed", previous, 0, nil)).To(Succeed())

			proxy := useProxy()

			// The teardown's first record goes through, and the write just
			// before the delete fails, so the delete is not sent and the
			// first record is the one left.
			proxy.FailWritesAfter(instanceID+"/task", 1, http.StatusForbidden, refusal, -1)

			var deletes atomic.Int32

			director.onGetDeployment = func(context.Context, string) (*bosh.DeploymentDetail, error) {
				return &bosh.DeploymentDetail{Name: deploymentName, Manifest: "name: " + deploymentName}, nil
			}
			director.onDeleteDeployment = func(context.Context, string) (*bosh.Task, error) {
				deletes.Add(1)

				return &bosh.Task{ID: deleteTaskID, State: outcomeTaskProcessing, Description: outcomeDeleteDeployment}, nil
			}

			brokerInstance.TearDownAbandonedDeployment(ctx, instanceID, deploymentName)

			Expect(deletes.Load()).To(BeZero())

			record := readRecord()
			Expect(record).To(HaveKeyWithValue("description", "Deleting deployment that finished after the instance was deprovisioned"))
			Expect(record["context_id"]).ToNot(Equal(previous), "the teardown's first record kept the context ID of the deprovision before it")

			contextID, _ := record["context_id"].(string)
			_, err := uuid.Parse(contextID)
			Expect(err).ToNot(HaveOccurred(), "the teardown's first record names no context ID")
		})
	})

	It("fails the read of a record whose unknown-since time is not RFC 3339", func() {
		indexInstance()

		Expect(vaultClient.TrackOutcomeUnknown(ctx, instanceID, outcomeDeprovision, "The delete's answer was lost", uuid.NewString(), time.Now())).To(Succeed())

		record := readRecord()
		record[internalVault.TaskRecordUnknownSinceKey] = "yesterday"
		Expect(suite.vault.WriteSecret(instanceID+"/task", record)).To(Succeed())

		_, err := brokerInstance.RecordedOperation(ctx, instanceID)
		Expect(err).To(MatchError(ContainSubstring("not RFC 3339")))

		_, err = brokerInstance.LastOperation(ctx, instanceID, osbapi.LastOperationRequest{
			ServiceID: serviceID, PlanID: planID, Operation: outcomeDeprovision,
		})
		Expect(err).To(MatchError(ContainSubstring("not RFC 3339")))
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

					recordErr = vaultClient.TrackProgressWithContextID(ctx, instanceID, outcomeProvision, fmt.Sprintf("Round %d", round), contextID, 0, nil)
				}()

				for writer := range writers {
					waitGroup.Add(1)

					go func() {
						defer waitGroup.Done()

						<-start

						brokerInstance.RecordProgress(ctx, instanceID, outcomeProvision, fmt.Sprintf("Round %d, step %d", round, writer))
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
			Expect(vaultClient.TrackProgressWithContextID(ctx, instanceID, outcomeProvision, "Creating BOSH deployment", uuid.NewString(), 0, nil)).To(Succeed())

			brokerInstance.RecordProgress(ctx, instanceID, outcomeDeprovision, "Starting deprovisioning process")

			Expect(readRecord()).ToNot(HaveKey("context_id"), "a deprovision took over the context ID of the provision before it")
		})
	})

	Describe("a gone deployment during delete retries cleans up as before", func() {
		// goneAfterDeletes makes the deployment exist until the broker has
		// sent the given number of deletes, each of which fails, and be gone,
		// by the director's word, from then on. It returns a count of the
		// deletes sent.
		goneAfterDeletes := func(failedDeletes int) func() int {
			var (
				deletesMu sync.Mutex
				deletes   int
			)

			sent := func() int {
				deletesMu.Lock()
				defer deletesMu.Unlock()

				return deletes
			}

			director.onGetDeployment = func(context.Context, string) (*bosh.DeploymentDetail, error) {
				if sent() < failedDeletes {
					return &bosh.DeploymentDetail{Name: deploymentName, Manifest: "name: " + deploymentName}, nil
				}

				return nil, fmt.Errorf("%w: %s", bosh.ErrDeploymentNotFound, deploymentName)
			}
			director.onDeleteDeployment = func(context.Context, string) (*bosh.Task, error) {
				deletesMu.Lock()
				defer deletesMu.Unlock()

				deletes++

				return nil, errDeleteFailed
			}

			return sent
		}

		expectCleanedUp := func() {
			Eventually(settled, 10*time.Second).Should(BeTrue())

			ExpectWithOffset(1, history()).To(ContainElement("Deprovisioning completed (cleanup during retry)"))
			ExpectWithOffset(1, readRecord()).To(HaveKeyWithValue("action", outcomeDeprovision))
			ExpectWithOffset(1, readRecord()).ToNot(HaveKeyWithValue("state", outcomeStateFailed), "the delete of a gone deployment was recorded as failed")
		}

		It("when the deployment is gone by the retry after a failed delete", func() {
			indexInstance()

			cleaner := useCleaner()
			deletes := goneAfterDeletes(1)

			deprovision()

			Eventually(stillIndexed, 15*time.Second).Should(BeFalse(), "the instance was left in the index")
			expectCleanedUp()

			brokerInstance.WaitForCredentialCleanups()
			Expect(cleaner.Targets()).To(HaveLen(1), "the CredHub cleanup did not run")
			Expect(deletes()).To(Equal(1), "the broker deleted a deployment the director said was gone")
		})

		It("when the deployment of an abandoned deploy is gone before its delete", func() {
			cleaner := useCleaner()
			deletes := goneAfterDeletes(0)

			brokerInstance.TearDownAbandonedDeployment(ctx, instanceID, deploymentName)

			expectCleanedUp()

			brokerInstance.WaitForCredentialCleanups()
			Expect(cleaner.Targets()).To(HaveLen(1), "the CredHub cleanup did not run")
			Expect(deletes()).To(BeZero(), "the broker deleted a deployment the director said was gone")
		})

		It("reports the deployment as not found rather than as a task", func() {
			goneAfterDeletes(0)

			err := brokerInstance.RetryDeleteDeployment(ctx, deploymentName, instanceID, 1)

			Expect(err).To(MatchError(bosh.ErrDeploymentNotFound))
			Expect(err.Error()).To(ContainSubstring(deploymentName))
		})

		It("refuses a delete with no context ID rather than sending one", func() {
			var asked atomic.Int32

			director.onGetDeployment = func(context.Context, string) (*bosh.DeploymentDetail, error) {
				asked.Add(1)

				return &bosh.DeploymentDetail{Name: deploymentName, Manifest: "name: " + deploymentName}, nil
			}
			director.onDeleteDeployment = func(context.Context, string) (*bosh.Task, error) {
				asked.Add(1)

				return &bosh.Task{ID: 1, State: outcomeTaskProcessing}, nil
			}

			var err error

			Expect(func() {
				err = brokerInstance.RetryDeleteDeploymentWithContextID(ctx, deploymentName, instanceID, "", 3)
			}).NotTo(Panic())
			Expect(err).To(MatchError(broker.ErrNoDeleteContextID))
			Expect(err.Error()).To(ContainSubstring(deploymentName))
			Expect(asked.Load()).To(BeZero(), "the broker asked the director about a delete it cannot name")
		})
	})

	Describe("an unknown delete outcome never removes the index entry", func() {
		// loseDeleteAnswers makes the deployment exist and every delete
		// lose its answer. It returns the context IDs the deletes carried.
		loseDeleteAnswers := func() func() []string {
			var (
				sentMu sync.Mutex
				sent   []string
			)

			director.onGetDeployment = func(context.Context, string) (*bosh.DeploymentDetail, error) {
				return &bosh.DeploymentDetail{Name: deploymentName, Manifest: "name: " + deploymentName}, nil
			}
			director.onDeleteDeployment = func(callCtx context.Context, name string) (*bosh.Task, error) {
				sentMu.Lock()
				defer sentMu.Unlock()

				sent = append(sent, boshapi.ContextIDFrom(callCtx))

				return nil, &bosh.OutcomeUnknownError{
					Operation:  "DeleteDeployment",
					Director:   outcomeDirectorAddress,
					Deployment: name,
					ContextID:  boshapi.ContextIDFrom(callCtx),
					Err:        errAnswerLost,
				}
			}

			return func() []string {
				sentMu.Lock()
				defer sentMu.Unlock()

				return append([]string(nil), sent...)
			}
		}

		// expectRecordedUnknown checks that the record says the outcome of
		// the request that carried contextID is unknown, with no task.
		expectRecordedUnknown := func(action, contextID string, before time.Time) {
			record := readRecord()

			ExpectWithOffset(1, record).To(HaveKeyWithValue("action", action))
			ExpectWithOffset(1, record).To(HaveKeyWithValue("state", outcomeStateUnknown))
			ExpectWithOffset(1, record).To(HaveKeyWithValue("context_id", contextID))
			ExpectWithOffset(1, record).ToNot(HaveKey("task"), "the record of an unknown outcome carries a task ID")
			ExpectWithOffset(1, record["description"]).To(ContainSubstring(contextID), "the description does not name the context ID")

			recorded, err := brokerInstance.RecordedOperation(ctx, instanceID)
			ExpectWithOffset(1, err).ToNot(HaveOccurred())
			ExpectWithOffset(1, recorded.State).To(Equal(outcomeStateUnknown))
			ExpectWithOffset(1, recorded.ContextID).To(Equal(contextID))
			ExpectWithOffset(1, recorded.TaskID).To(BeZero())
			ExpectWithOffset(1, recorded.UnknownSince).To(BeTemporally(">=", before.Truncate(time.Second)))
			ExpectWithOffset(1, recorded.UnknownSince).To(BeTemporally("<=", time.Now()))

			ExpectWithOffset(1, history()).To(ContainElement(record["description"]), "the unknown outcome is not in the history")
		}

		It("for a deprovision", func() {
			indexInstance()

			cleaner := useCleaner()
			sent := loseDeleteAnswers()
			before := time.Now()

			deprovision()

			Eventually(sent, 10*time.Second).Should(HaveLen(1))
			Eventually(settled, 10*time.Second).Should(BeTrue())

			Expect(stillIndexed()).To(BeTrue(), "an unknown delete removed the index entry")

			brokerInstance.WaitForCredentialCleanups()
			Expect(cleaner.Targets()).To(BeEmpty(), "an unknown delete started the CredHub cleanup")

			expectRecordedUnknown(outcomeDeprovision, sent()[0], before)
			Expect(sent()).To(HaveLen(1), "the broker resent a delete whose answer was lost")
		})

		It("for the teardown of an abandoned deploy", func() {
			cleaner := useCleaner()
			sent := loseDeleteAnswers()
			before := time.Now()

			brokerInstance.TearDownAbandonedDeployment(ctx, instanceID, deploymentName)

			brokerInstance.WaitForCredentialCleanups()
			Expect(cleaner.Targets()).To(BeEmpty(), "an unknown delete started the CredHub cleanup")

			Expect(sent()).To(HaveLen(1), "the broker resent a delete whose answer was lost")
			expectRecordedUnknown(outcomeDeprovision, sent()[0], before)
		})

		It("nor is the teardown's unknown delete reported as a deletion", func() {
			sent := loseDeleteAnswers()

			brokerInstance.TearDownAbandonedDeployment(ctx, instanceID, deploymentName)

			Expect(sent()).To(HaveLen(1))
			Expect(stillIndexed()).To(BeFalse())

			for _, operation := range []string{outcomeDeprovision, ""} {
				response := pollUnindexed(operation)
				Expect(response.State).To(Equal(osbapi.StateInProgress), "a poll with operation %q answered %+v", operation, response)
				Expect(response.Description).To(ContainSubstring(sent()[0]), "the answer does not name the context ID")
			}
		})

		It("nor is the teardown's unknown delete settled by a poll that finds the deployment gone", func() {
			sent := loseDeleteAnswers()

			brokerInstance.TearDownAbandonedDeployment(ctx, instanceID, deploymentName)

			Expect(sent()).To(HaveLen(1))

			director.onGetDeployment = func(context.Context, string) (*bosh.DeploymentDetail, error) {
				return nil, fmt.Errorf("%w: %s", bosh.ErrDeploymentNotFound, deploymentName)
			}

			response := pollUnindexed(outcomeDeprovision)
			Expect(response.State).To(Equal(osbapi.StateInProgress), "a poll settled an unknown delete as %+v", response)
			Expect(response.Description).To(ContainSubstring(sent()[0]))
		})

		It("and the retry loop stops at the first unknown outcome", func() {
			sent := loseDeleteAnswers()

			err := brokerInstance.RetryDeleteDeployment(ctx, deploymentName, instanceID, 3)

			Expect(err).To(MatchError(bosh.ErrOutcomeUnknown))
			Expect(sent()).To(HaveLen(1), "the broker resent a delete whose answer was lost")
		})

		It("nor does an unknown deploy outcome", func() {
			var (
				sentMu sync.Mutex
				sent   string
			)

			director.onCreateDeployment = func(callCtx context.Context, _ string) (*bosh.Task, error) {
				sentMu.Lock()
				defer sentMu.Unlock()

				sent = boshapi.ContextIDFrom(callCtx)

				return nil, &bosh.OutcomeUnknownError{
					Operation:  "CreateDeployment",
					Director:   outcomeDirectorAddress,
					Deployment: deploymentName,
					ContextID:  sent,
					Err:        errAnswerLost,
				}
			}
			director.onGetEvents = func(context.Context, string) ([]bosh.Event, error) {
				return deploymentEvents(boshEventCreate, deploymentName, createTaskID), nil
			}

			before := time.Now()

			provision()

			Eventually(settled, 10*time.Second).Should(BeTrue())

			sentMu.Lock()
			contextID := sent
			sentMu.Unlock()

			Expect(contextID).ToNot(BeEmpty(), "the deploy was never sent")
			Expect(stillIndexed()).To(BeTrue(), "an unknown deploy removed the index entry")
			expectRecordedUnknown(outcomeProvision, contextID, before)
		})
	})

	Describe("a poll after the teardown of an abandoned deploy", func() {
		// recordTeardown writes the record a teardown leaves when it has
		// recorded its context ID, and returns the ID.
		recordTeardown := func() string {
			contextID := uuid.NewString()

			ExpectWithOffset(1, vaultClient.TrackProgressWithContextID(ctx, instanceID, outcomeDeprovision,
				"Initiating BOSH deployment deletion with retry", contextID, 0, nil)).To(Succeed())

			return contextID
		}

		existingDeployment := func() *bosh.DeploymentDetail {
			return &bosh.DeploymentDetail{Name: deploymentName, Manifest: "name: " + deploymentName}
		}

		// deploymentExists makes the director say the deployment exists.
		deploymentExists := func() {
			director.onGetDeployment = func(context.Context, string) (*bosh.DeploymentDetail, error) {
				return existingDeployment(), nil
			}
		}

		It("answers in progress while the delete task runs", func() {
			sent := scriptDelete()

			brokerInstance.TearDownAbandonedDeployment(ctx, instanceID, deploymentName)

			var deleteRequest sentRequest
			Eventually(sent, 10*time.Second).Should(Receive(&deleteRequest))
			Expect(readRecord()).To(HaveKeyWithValue("state", "in_progress"))

			response := pollUnindexed(outcomeDeprovision)
			Expect(response.State).To(Equal(osbapi.StateInProgress), "a running delete was reported as %+v", response)
			Expect(response.Description).To(ContainSubstring(deleteRequest.contextID))
		})

		It("answers in progress before the delete is sent", func() {
			contextID := recordTeardown()

			deploymentExists()

			response := pollUnindexed(outcomeDeprovision)
			Expect(response.State).To(Equal(osbapi.StateInProgress), "a delete not yet sent was reported as %+v", response)
			Expect(response.Description).To(ContainSubstring(contextID))
		})

		It("answers failed when the delete failed and the deployment is still there", func() {
			recordTeardown()
			brokerInstance.RecordFailure(ctx, instanceID, outcomeDeprovision, "Deployment deletion failed: director refused the delete")

			deploymentExists()

			response := pollUnindexed(outcomeDeprovision)
			Expect(response.State).To(Equal(osbapi.StateFailed), "a failed delete was reported as %+v", response)
			Expect(response.Description).To(ContainSubstring("director refused the delete"))
		})

		It("answers in progress without a plan to name the deployment by", func() {
			contextID := recordTeardown()

			var asked []string

			director.onGetDeployment = func(_ context.Context, name string) (*bosh.DeploymentDetail, error) {
				asked = append(asked, name)

				return existingDeployment(), nil
			}

			response, err := brokerInstance.LastOperation(ctx, instanceID, osbapi.LastOperationRequest{Operation: outcomeDeprovision})
			Expect(err).ToNot(HaveOccurred())
			Expect(response.State).To(Equal(osbapi.StateInProgress), "a delete that could not be checked was reported as %+v", response)
			Expect(response.Description).To(ContainSubstring(contextID))
			Expect(asked).To(BeEmpty(), "the broker asked for a deployment it named without a plan")
		})

		It("returns an error when the director cannot say whether the deployment exists", func() {
			recordTeardown()

			director.onGetDeployment = func(context.Context, string) (*bosh.DeploymentDetail, error) {
				return nil, errAnswerLost
			}

			_, err := brokerInstance.LastOperation(ctx, instanceID, osbapi.LastOperationRequest{
				ServiceID: serviceID, PlanID: planID, Operation: outcomeDeprovision,
			})
			Expect(err).To(MatchError(errAnswerLost))
		})

		It("answers succeeded once the director says the deployment is gone", func() {
			sent := scriptDelete()

			// The delete task finishes, and the deployment is gone, once
			// the spec says so.
			var gone atomic.Bool

			director.onGetDeployment = func(context.Context, string) (*bosh.DeploymentDetail, error) {
				if gone.Load() {
					return nil, fmt.Errorf("%w: %s", bosh.ErrDeploymentNotFound, deploymentName)
				}

				return existingDeployment(), nil
			}
			director.GetTaskFn = func(taskID int) (*bosh.Task, error) {
				if gone.Load() {
					return &bosh.Task{ID: taskID, State: outcomeTaskDone}, nil
				}

				return &bosh.Task{ID: taskID, State: outcomeTaskProcessing}, nil
			}

			monitorExited := make(chan string, 1)

			brokerInstance.SetMonitorExited(func(id string) { monitorExited <- id })

			defer broker.SetTaskPollInterval(10 * time.Millisecond)()

			brokerInstance.TearDownAbandonedDeployment(ctx, instanceID, deploymentName)
			Eventually(sent, 10*time.Second).Should(Receive())

			gone.Store(true)

			Eventually(monitorExited, 10*time.Second).Should(Receive(Equal(instanceID)))
			Expect(readRecord()).To(HaveKey("context_id"), "the finished teardown's record lost its context ID")

			for _, operation := range []string{outcomeDeprovision, ""} {
				response := pollUnindexed(operation)
				Expect(response.State).To(Equal(osbapi.StateSucceeded), "a poll with operation %q answered %+v", operation, response)
			}
		})
	})

	Describe("a context ID that cannot be recorded stops the request before it is sent", func() {
		const refusal = "permission denied"

		It("for a deploy, which fails the provision", func() {
			proxy := useProxy()

			// The task record write after the release upload is the one
			// that records the deploy's context ID.
			director.GetReleasesFn = func() ([]bosh.Release, error) {
				proxy.FailWrites(instanceID+"/task", http.StatusForbidden, refusal, 1)

				return []bosh.Release{}, nil
			}

			var deploys atomic.Int32

			director.onCreateDeployment = func(context.Context, string) (*bosh.Task, error) {
				deploys.Add(1)

				return &bosh.Task{ID: createTaskID, State: outcomeTaskDone, Description: outcomeCreateDeployment}, nil
			}

			provision()

			Eventually(settled, 10*time.Second).Should(BeTrue())

			Expect(deploys.Load()).To(BeZero(), "the deploy was sent without its context ID on record")
			Expect(readRecord()).To(HaveKeyWithValue("state", outcomeStateFailed))
			Expect(readRecord()["description"]).To(ContainSubstring(refusal))

			response, err := brokerInstance.LastOperation(ctx, instanceID, osbapi.LastOperationRequest{
				ServiceID: serviceID, PlanID: planID, Operation: outcomeProvision,
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(response.State).To(Equal(osbapi.StateFailed))
		})

		It("for a delete, which fails the deprovision and keeps the index entry", func() {
			indexInstance()

			cleaner := useCleaner()
			proxy := useProxy()

			// The task record write after the deployment check is the one
			// that records the delete's context ID.
			var checked atomic.Bool

			director.onGetDeployment = func(context.Context, string) (*bosh.DeploymentDetail, error) {
				if !checked.Swap(true) {
					proxy.FailWrites(instanceID+"/task", http.StatusForbidden, refusal, 1)
				}

				return &bosh.DeploymentDetail{Name: deploymentName, Manifest: "name: " + deploymentName}, nil
			}

			var deletes atomic.Int32

			director.onDeleteDeployment = func(context.Context, string) (*bosh.Task, error) {
				deletes.Add(1)

				return &bosh.Task{ID: deleteTaskID, State: outcomeTaskProcessing, Description: outcomeDeleteDeployment}, nil
			}

			deprovision()

			Eventually(settled, 10*time.Second).Should(BeTrue())

			Expect(deletes.Load()).To(BeZero(), "the delete was sent without its context ID on record")
			Expect(stillIndexed()).To(BeTrue(), "a delete that was never sent removed the index entry")
			Expect(readRecord()).To(HaveKeyWithValue("action", outcomeDeprovision))
			Expect(readRecord()).To(HaveKeyWithValue("state", outcomeStateFailed))
			Expect(readRecord()["description"]).To(ContainSubstring(refusal))

			brokerInstance.WaitForCredentialCleanups()
			Expect(cleaner.Targets()).To(BeEmpty(), "a delete that was never sent started the CredHub cleanup")
		})

		It("for the delete of an abandoned deploy's teardown", func() {
			cleaner := useCleaner()
			proxy := useProxy()

			proxy.FailWrites(instanceID+"/task", http.StatusForbidden, refusal, -1)

			director.onGetDeployment = func(context.Context, string) (*bosh.DeploymentDetail, error) {
				return &bosh.DeploymentDetail{Name: deploymentName, Manifest: "name: " + deploymentName}, nil
			}

			var deletes atomic.Int32

			director.onDeleteDeployment = func(context.Context, string) (*bosh.Task, error) {
				deletes.Add(1)

				return &bosh.Task{ID: deleteTaskID, State: outcomeTaskProcessing, Description: outcomeDeleteDeployment}, nil
			}

			brokerInstance.TearDownAbandonedDeployment(ctx, instanceID, deploymentName)

			Expect(deletes.Load()).To(BeZero(), "the delete was sent without its context ID on record")

			brokerInstance.WaitForCredentialCleanups()
			Expect(cleaner.Targets()).To(BeEmpty(), "a delete that was never sent started the CredHub cleanup")
		})
	})
})
