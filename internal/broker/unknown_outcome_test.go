package broker_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
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
	outcomeTaskError        = "error"
	outcomeTaskProcessing   = "processing"
	outcomeTaskQueued       = "queued"
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
// errorLogger records the messages logged at the Error level.
type errorLogger struct {
	*logger.NoOpLogger

	mu    sync.Mutex
	lines []string
}

func (e *errorLogger) Error(msg string, args ...interface{}) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.lines = append(e.lines, fmt.Sprintf(msg, args...))
}

func (e *errorLogger) Named(string) logger.Logger { return e }

func (e *errorLogger) errors() []string {
	e.mu.Lock()
	defer e.mu.Unlock()

	return append([]string(nil), e.lines...)
}

// deploymentEventsAt returns the events deploymentEvents does, dated at.
func deploymentEventsAt(at time.Time, action, deploymentName string, taskID int) []bosh.Event {
	events := deploymentEvents(action, deploymentName, taskID)
	for index := range events {
		events[index].Time = at
	}

	return events
}

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

	// loseDeleteAnswers makes the deployment exist with no events and
	// every delete lose its answer. It returns the context IDs the deletes
	// carried.
	loseDeleteAnswers := func() func() []string {
		var (
			sentMu sync.Mutex
			sent   []string
		)

		director.onGetEvents = func(context.Context, string) ([]bosh.Event, error) { return nil, nil }

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

	// A deployment whose name is not the one the plan and the instance ID
	// give, so a name built from the plan cannot pass for it.
	renameDeployment := func() {
		deploymentName = "renamed-" + instanceID
	}

	Describe("the delete's record names the deployment the delete names", func() {
		const refusal = "permission denied"

		It("for a deprovision, from its first record on", func() {
			renameDeployment()
			indexInstance()

			sent := scriptDelete()

			var checked atomic.Pointer[map[string]interface{}]

			director.onGetDeployment = func(context.Context, string) (*bosh.DeploymentDetail, error) {
				record, _ := suite.vault.ReadSecret(instanceID + "/task")
				checked.CompareAndSwap(nil, &record)

				return &bosh.DeploymentDetail{Name: deploymentName, Manifest: "name: " + deploymentName}, nil
			}

			deprovision()

			var deleteRequest sentRequest
			Eventually(sent, 10*time.Second).Should(Receive(&deleteRequest))
			Expect(checked.Load()).ToNot(BeNil(), "the deployment was never checked")
			Expect(*checked.Load()).To(HaveKeyWithValue(internalVault.TaskRecordDeploymentNameKey, deploymentName),
				"the record did not name the deployment while its existence was checked")
			Expect(deleteRequest.record).To(HaveKeyWithValue(internalVault.TaskRecordDeploymentNameKey, deploymentName),
				"the record did not name the deployment while the delete was being sent")
		})

		It("for a deprovision, when the read of the record it replaces fails", func() {
			renameDeployment()
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
			Expect(deleteRequest.record).To(HaveKeyWithValue(internalVault.TaskRecordDeploymentNameKey, deploymentName),
				"the write just before the delete did not name the deployment")
		})

		It("for a teardown, from its first record on", func() {
			renameDeployment()

			proxy := useProxy()

			// The teardown's first record goes through, and the write just
			// before the delete fails, so the first record is the one left.
			proxy.FailWritesAfter(instanceID+"/task", 1, http.StatusForbidden, refusal, -1)

			director.onGetDeployment = func(context.Context, string) (*bosh.DeploymentDetail, error) {
				return &bosh.DeploymentDetail{Name: deploymentName, Manifest: "name: " + deploymentName}, nil
			}

			brokerInstance.TearDownAbandonedDeployment(ctx, instanceID, deploymentName)

			Expect(readRecord()).To(HaveKeyWithValue("description", "Deleting deployment that finished after the instance was deprovisioned"))
			Expect(readRecord()).To(HaveKeyWithValue(internalVault.TaskRecordDeploymentNameKey, deploymentName))
		})

		It("for a teardown, while the delete is being sent", func() {
			renameDeployment()

			sent := scriptDelete()

			brokerInstance.TearDownAbandonedDeployment(ctx, instanceID, deploymentName)

			var deleteRequest sentRequest
			Eventually(sent, 10*time.Second).Should(Receive(&deleteRequest))
			Expect(deleteRequest.record).To(HaveKeyWithValue(internalVault.TaskRecordDeploymentNameKey, deploymentName))
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
				return deploymentEventsAt(time.Now(), boshEventCreate, deploymentName, createTaskID), nil
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

	// An unknown outcome is settled only by what the director says. A task
	// found by the context ID, or for a deploy a task on its deployment, is
	// followed. A delete whose deployment the director says is missing is
	// deleted. A request is failed only once a lookup that succeeded after
	// the grace period found nothing, and while lookups fail it stays in
	// progress.
	Describe("an unknown outcome is settled from what the director says", func() {
		const (
			refusal = "permission denied"
			// taskListPath is the director route a lookup by context ID
			// reads.
			taskListPath = "/tasks"
		)

		var (
			clockOffset atomic.Int64
			// frozenAt is the time the clock stands at, in Unix
			// nanoseconds, or zero when it runs.
			frozenAt atomic.Int64
		)

		BeforeEach(func() {
			clockOffset.Store(0)
			frozenAt.Store(0)
			brokerInstance.SetUnknownOutcomeClock(func() time.Time {
				if frozen := frozenAt.Load(); frozen != 0 {
					return time.Unix(0, frozen)
				}

				return time.Now().Add(time.Duration(clockOffset.Load()))
			})

			director.FindTaskByContextIDFn = func(string) (*bosh.Task, error) { return nil, nil } //nolint:nilnil // no task is a nil task without an error
		})

		// passGrace moves the clock past the grace period of an unknown
		// outcome recorded now.
		passGrace := func() {
			clockOffset.Store(int64(bosh.UnknownOutcomeGrace + time.Second))
		}

		poll := func(operation string) osbapi.LastOperationResponse {
			response, err := brokerInstance.LastOperation(ctx, instanceID, osbapi.LastOperationRequest{
				ServiceID: serviceID, PlanID: planID, Operation: operation,
			})
			ExpectWithOffset(1, err).ToNot(HaveOccurred())

			return response
		}

		// loseDelete deprovisions the indexed instance with a delete whose
		// answer is lost, and returns the context ID the delete carried.
		loseDelete := func() string {
			indexInstance()

			sent := loseDeleteAnswers()

			deprovision()

			EventuallyWithOffset(1, sent, 10*time.Second).Should(HaveLen(1))
			EventuallyWithOffset(1, settled, 10*time.Second).Should(BeTrue())

			return sent()[0]
		}

		// loseDeploy provisions the instance with a deploy whose answer is
		// lost, and returns the context ID the deploy carried.
		loseDeploy := func() string {
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
			director.onGetEvents = func(context.Context, string) ([]bosh.Event, error) { return nil, nil }

			provision()

			EventuallyWithOffset(1, settled, 10*time.Second).Should(BeTrue())

			sentMu.Lock()
			defer sentMu.Unlock()

			ExpectWithOffset(1, sent).ToNot(BeEmpty(), "the deploy was never sent")
			ExpectWithOffset(1, readRecord()).To(HaveKeyWithValue("state", outcomeStateUnknown))

			return sent
		}

		deploymentMissing := func() {
			director.onGetDeployment = func(_ context.Context, name string) (*bosh.DeploymentDetail, error) {
				return nil, fmt.Errorf("%w: %s", bosh.ErrDeploymentNotFound, name)
			}
		}

		routeNotFound := func() error {
			return fmt.Errorf("get deployment %s: %w", deploymentName, &boshapi.APIError{
				StatusCode: http.StatusNotFound, Method: http.MethodGet, Path: "/deployments/" + deploymentName,
			})
		}

		recorded := func() broker.RecordedOperation {
			operation, err := brokerInstance.RecordedOperation(ctx, instanceID)
			ExpectWithOffset(1, err).ToNot(HaveOccurred())

			return operation
		}

		It("an unknown delete is reported in progress with the context ID", func() {
			contextID := loseDelete()

			response := poll(outcomeDeprovision)

			Expect(response.State).To(Equal(osbapi.StateInProgress), "an unknown delete answered %+v", response)
			Expect(response.Description).To(ContainSubstring(contextID))
			Expect(director.Calls("FindTaskByContextID")).To(ContainElement(contextID), "the poll did not look the task up by its context ID")
			Expect(stillIndexed()).To(BeTrue())
			Expect(recorded().State).To(Equal(outcomeStateUnknown))
		})

		It("an unknown delete is settled when its task turns up by context ID", func() {
			contextID := loseDelete()

			director.FindTaskByContextIDFn = func(id string) (*bosh.Task, error) {
				if id != contextID {
					return nil, nil //nolint:nilnil // no task is a nil task without an error
				}

				return &bosh.Task{ID: deleteTaskID, State: outcomeTaskProcessing, Description: outcomeDeleteDeployment, ContextID: contextID}, nil
			}
			director.GetTaskFn = func(taskID int) (*bosh.Task, error) {
				return &bosh.Task{ID: taskID, State: outcomeTaskProcessing, Description: outcomeDeleteDeployment}, nil
			}

			response := poll(outcomeDeprovision)

			Expect(response.State).To(Equal(osbapi.StateInProgress), "a running delete answered %+v", response)
			Expect(recorded().TaskID).To(Equal(deleteTaskID), "the task found by the context ID was not recorded")
			Expect(recorded().State).ToNot(Equal(outcomeStateUnknown))
			Expect(recorded().ContextID).To(Equal(contextID))
			Expect(stillIndexed()).To(BeTrue())
		})

		// replaceDuringLookup records another unknown outcome while the
		// pass looks the task up, and returns the new context ID.
		replaceDuringLookup := func(action string, task *bosh.Task) string {
			replacement := uuid.NewString()

			director.onFindTaskByContextID = func(context.Context, string) (*bosh.Task, error) {
				ExpectWithOffset(1, vaultClient.TrackOutcomeUnknown(context.Background(), instanceID, action,
					"Another request's answer was lost", replacement, time.Now())).To(Succeed())

				return task, nil
			}

			return replacement
		}

		It("a task found for an unknown delete is not recorded over a record that changed during the pass", func() {
			contextID := loseDelete()
			replacement := replaceDuringLookup(outcomeDeprovision, &bosh.Task{ID: deleteTaskID, State: outcomeTaskProcessing, Description: outcomeDeleteDeployment})

			response := poll(outcomeDeprovision)

			Expect(response.State).To(Equal(osbapi.StateInProgress), "a pass over a changed record answered %+v", response)
			Expect(response.Description).To(ContainSubstring(contextID))
			Expect(recorded().State).To(Equal(outcomeStateUnknown), "the found task was written over the changed record")
			Expect(recorded().ContextID).To(Equal(replacement))
			Expect(director.Calls("GetTask")).To(BeEmpty(), "the pass followed a task it could not record")
		})

		It("an unknown deploy is not failed over a record that changed during the pass", func() {
			loseDeploy()
			passGrace()

			// The cleanup of a failed provision starts by asking for the
			// deployment.
			var checks atomic.Int32

			director.onGetDeployment = func(_ context.Context, name string) (*bosh.DeploymentDetail, error) {
				checks.Add(1)

				return nil, fmt.Errorf("%w: %s", bosh.ErrDeploymentNotFound, name)
			}

			replacement := replaceDuringLookup(outcomeProvision, nil)

			response := poll(outcomeProvision)

			Expect(response.State).To(Equal(osbapi.StateInProgress), "a pass over a changed record answered %+v", response)
			Expect(recorded().State).To(Equal(outcomeStateUnknown), "the failure was written over the changed record")
			Expect(recorded().ContextID).To(Equal(replacement))

			polled := checks.Load()
			Consistently(checks.Load, 300*time.Millisecond).Should(Equal(polled), "the cleanup of a failure that was not recorded started")
		})

		It("a provision poll over an unknown delete answers in progress and keeps the record", func() {
			contextID := loseDelete()

			unknown := recorded()

			// The instance's old create event would name a task for a
			// provision that lost track of its own.
			director.onGetEvents = func(context.Context, string) ([]bosh.Event, error) {
				return deploymentEventsAt(time.Now(), boshEventCreate, deploymentName, createTaskID), nil
			}
			director.GetTaskFn = func(taskID int) (*bosh.Task, error) {
				return &bosh.Task{ID: taskID, State: outcomeTaskProcessing}, nil
			}

			response := poll(outcomeProvision)

			Expect(response.State).To(Equal(osbapi.StateInProgress), "a provision poll over an unknown delete answered %+v", response)
			Expect(response.Description).To(ContainSubstring(contextID))
			Expect(recorded()).To(Equal(unknown), "the provision poll changed the unknown delete's record")
			Expect(director.Calls("GetTask")).To(BeEmpty(), "the provision poll followed a task")
			Expect(stillIndexed()).To(BeTrue())
		})

		It("a deprovision poll over an unknown deploy answers in progress and keeps the instance", func() {
			contextID := loseDeploy()

			unknown := recorded()
			cleaner := useCleaner()

			deploymentMissing()

			response := poll(outcomeDeprovision)

			Expect(response.State).To(Equal(osbapi.StateInProgress), "a deprovision poll over an unknown deploy answered %+v", response)
			Expect(response.Description).To(ContainSubstring(contextID))
			Expect(stillIndexed()).To(BeTrue(), "a deprovision poll over an unknown deploy removed the index entry")
			Expect(recorded()).To(Equal(unknown), "the deprovision poll changed the unknown deploy's record")

			brokerInstance.WaitForCredentialCleanups()
			Expect(cleaner.Targets()).To(BeEmpty(), "a deprovision poll over an unknown deploy started the CredHub cleanup")
		})

		It("an unknown delete is settled as deleted when the director says the deployment is missing", func() {
			contextID := loseDelete()
			cleaner := useCleaner()

			deploymentMissing()

			response := poll(outcomeDeprovision)

			Expect(response.State).To(Equal(osbapi.StateSucceeded), "a delete whose deployment is missing answered %+v", response)
			Expect(stillIndexed()).To(BeFalse(), "the deleted instance is still indexed")

			brokerInstance.WaitForCredentialCleanups()
			Expect(cleaner.Targets()).ToNot(BeEmpty(), "the deleted instance's CredHub cleanup did not start")
			Expect(recorded().State).ToNot(Equal(outcomeStateUnknown), "the settled delete is still recorded as unknown")
			Expect(director.Calls("FindTaskByContextID")).To(ContainElement(contextID))
		})

		Describe("the state that keeps resolver passes apart", func() {
			passState := func() (bool, string) {
				return brokerInstance.UnknownOutcomePassState(instanceID)
			}

			It("keeps the last error while lookups fail, and is dropped once a found task settles the outcome", func() {
				loseDelete()

				director.FindTaskByContextIDFn = func(string) (*bosh.Task, error) { return nil, routeNotFound() }

				Expect(poll(outcomeDeprovision).State).To(Equal(osbapi.StateInProgress))

				kept, lastErr := passState()
				Expect(kept).To(BeTrue(), "a pass whose lookup failed kept no state")
				Expect(lastErr).To(ContainSubstring("404"), "the pass state lost the lookup's error")

				director.FindTaskByContextIDFn = func(string) (*bosh.Task, error) {
					return &bosh.Task{ID: deleteTaskID, State: outcomeTaskProcessing, Description: outcomeDeleteDeployment}, nil
				}
				director.GetTaskFn = func(taskID int) (*bosh.Task, error) {
					return &bosh.Task{ID: taskID, State: outcomeTaskProcessing, Description: outcomeDeleteDeployment}, nil
				}

				Expect(poll(outcomeDeprovision).State).To(Equal(osbapi.StateInProgress))
				Expect(recorded().TaskID).To(Equal(deleteTaskID))

				kept, _ = passState()
				Expect(kept).To(BeFalse(), "the pass state outlived the settled outcome")
			})

			It("is dropped once a pass fails an unknown deploy", func() {
				loseDeploy()
				deploymentMissing()
				passGrace()

				Expect(poll(outcomeProvision).State).To(Equal(osbapi.StateFailed))

				kept, _ := passState()
				Expect(kept).To(BeFalse(), "the pass state outlived the failed deploy")
			})

			It("is dropped once a pass settles an unknown delete as deleted", func() {
				loseDelete()
				deploymentMissing()

				Expect(poll(outcomeDeprovision).State).To(Equal(osbapi.StateSucceeded))

				kept, _ := passState()
				Expect(kept).To(BeFalse(), "the pass state outlived the deleted instance")
			})

			It("is kept, with its last error, while the outcome stays unknown", func() {
				loseDeploy()

				director.onGetDeployment = func(context.Context, string) (*bosh.DeploymentDetail, error) {
					return nil, routeNotFound()
				}

				passGrace()

				Expect(poll(outcomeProvision).State).To(Equal(osbapi.StateInProgress))

				kept, lastErr := passState()
				Expect(kept).To(BeTrue())
				Expect(lastErr).To(ContainSubstring("404"))
			})

			It("is dropped by a pass that finds the outcome settled since the poll read it", func() {
				contextID := loseDelete()

				unknown := recorded()

				director.FindTaskByContextIDFn = func(string) (*bosh.Task, error) { return nil, routeNotFound() }

				Expect(poll(outcomeDeprovision).State).To(Equal(osbapi.StateInProgress))

				kept, lastErr := passState()
				Expect(kept).To(BeTrue())
				Expect(lastErr).ToNot(BeEmpty())

				// A poll that loaded the pass state before it was dropped
				// still reads its last error.
				heldLastErr := brokerInstance.UnknownOutcomePassReader(instanceID)

				// Another writer settles the outcome after a poll read it
				// and before its pass reads it again.
				Expect(vaultClient.SettleOutcomeUnknown(ctx, instanceID, outcomeDeprovision, "BOSH deletion in progress (task 300)", contextID, deleteTaskID)).To(Succeed())

				response, err := brokerInstance.ResolveUnknownOutcome(ctx, instanceID, deploymentName, unknown)
				Expect(err).ToNot(HaveOccurred())
				Expect(response.State).To(Equal(osbapi.StateInProgress))
				Expect(response.Description).To(ContainSubstring("was just settled"))

				kept, _ = passState()
				Expect(kept).To(BeFalse(), "the pass state outlived the settled outcome")
				Expect(heldLastErr()).To(BeEmpty(), "the dropped pass state kept an error from before the outcome settled")
			})

			It("is kept when the settling write fails", func() {
				loseDeploy()
				deploymentMissing()
				passGrace()

				proxy := useProxy()

				proxy.FailWrites(instanceID+"/task", http.StatusForbidden, refusal, -1)

				Expect(poll(outcomeProvision).State).To(Equal(osbapi.StateInProgress))

				kept, _ := passState()
				Expect(kept).To(BeTrue(), "the pass state was dropped while the outcome is still unknown")
			})
		})

		It("an unknown delete stays in progress when the deployment is missing and then present", func() {
			contextID := loseDelete()

			unknown := recorded()
			cleaner := useCleaner()

			var checks atomic.Int32

			director.onGetDeployment = func(_ context.Context, name string) (*bosh.DeploymentDetail, error) {
				if checks.Add(1) == 1 {
					return nil, fmt.Errorf("%w: %s", bosh.ErrDeploymentNotFound, name)
				}

				return &bosh.DeploymentDetail{Name: name, Manifest: "name: " + name}, nil
			}

			response := poll(outcomeDeprovision)

			Expect(response.State).To(Equal(osbapi.StateInProgress), "a delete whose deployment came back answered %+v", response)
			Expect(response.Description).To(ContainSubstring(contextID))
			Expect(response.Description).To(ContainSubstring("still exists"), "the answer does not carry the reason")
			Expect(recorded()).To(Equal(unknown), "the record of the unknown delete changed")
			Expect(stillIndexed()).To(BeTrue())

			brokerInstance.WaitForCredentialCleanups()
			Expect(cleaner.Targets()).To(BeEmpty(), "a delete that was not confirmed started the CredHub cleanup")
		})

		It("an unknown delete stays in progress when the second deployment check fails", func() {
			contextID := loseDelete()

			unknown := recorded()

			var checks atomic.Int32

			director.onGetDeployment = func(_ context.Context, name string) (*bosh.DeploymentDetail, error) {
				if checks.Add(1) == 1 {
					return nil, fmt.Errorf("%w: %s", bosh.ErrDeploymentNotFound, name)
				}

				return nil, routeNotFound()
			}

			response := poll(outcomeDeprovision)

			Expect(response.State).To(Equal(osbapi.StateInProgress), "a delete whose second check failed answered %+v", response)
			Expect(response.Description).To(ContainSubstring(contextID))
			Expect(response.Description).To(ContainSubstring("/deployments/"+deploymentName), "the answer does not carry the failed check")
			Expect(recorded()).To(Equal(unknown))
			Expect(stillIndexed()).To(BeTrue())
		})

		It("an unknown delete is not settled by a route 404", func() {
			contextID := loseDelete()
			cleaner := useCleaner()

			director.onGetDeployment = func(context.Context, string) (*bosh.DeploymentDetail, error) {
				return nil, routeNotFound()
			}

			passGrace()

			response := poll(outcomeDeprovision)

			Expect(response.State).To(Equal(osbapi.StateInProgress), "a route 404 settled an unknown delete as %+v", response)
			Expect(response.Description).To(ContainSubstring(contextID))
			Expect(response.Description).To(ContainSubstring("/deployments/"+deploymentName), "the answer does not carry the failed check")
			Expect(stillIndexed()).To(BeTrue(), "a route 404 removed the index entry")

			brokerInstance.WaitForCredentialCleanups()
			Expect(cleaner.Targets()).To(BeEmpty(), "a route 404 started the CredHub cleanup")
			Expect(recorded().State).To(Equal(outcomeStateUnknown))
		})

		It("an unknown delete fails after the grace period and keeps the index entry", func() {
			contextID := loseDelete()
			cleaner := useCleaner()

			response := poll(outcomeDeprovision)
			Expect(response.State).To(Equal(osbapi.StateInProgress), "an unknown delete in its grace period answered %+v", response)

			passGrace()

			response = poll(outcomeDeprovision)

			Expect(response.State).To(Equal(osbapi.StateFailed), "an unknown delete past its grace period answered %+v", response)
			Expect(response.Description).To(ContainSubstring(contextID))
			Expect(stillIndexed()).To(BeTrue(), "a failed delete removed the index entry")
			Expect(recorded().State).To(Equal(outcomeStateFailed))

			brokerInstance.WaitForCredentialCleanups()
			Expect(cleaner.Targets()).To(BeEmpty(), "a failed delete started the CredHub cleanup")
		})

		Describe("an unknown delete past the grace period whose deployment still exists", func() {
			var contextID string

			BeforeEach(func() {
				contextID = loseDelete()

				passGrace()
			})

			It("adopts a delete task running on its deployment", func() {
				director.FindRunningTaskForDeploymentFn = func(name string) (*bosh.Task, error) {
					return &bosh.Task{ID: deleteTaskID, State: outcomeTaskProcessing, Description: outcomeDeleteDeployment, Deployment: name}, nil
				}
				director.GetTaskFn = func(taskID int) (*bosh.Task, error) {
					return &bosh.Task{ID: taskID, State: outcomeTaskProcessing, Description: outcomeDeleteDeployment}, nil
				}

				response := poll(outcomeDeprovision)

				Expect(response.State).To(Equal(osbapi.StateInProgress), "a delete whose task runs answered %+v", response)
				Expect(recorded().TaskID).To(Equal(deleteTaskID), "the running delete task was not adopted")
				Expect(recorded().State).ToNot(Equal(outcomeStateUnknown))
				Expect(recorded().ContextID).To(Equal(contextID))
				Expect(stillIndexed()).To(BeTrue())
			})

			It("adopts the task a delete event on its deployment names", func() {
				director.onGetEvents = func(context.Context, string) ([]bosh.Event, error) {
					return deploymentEventsAt(time.Now(), boshEventDelete, deploymentName, deleteTaskID), nil
				}
				director.GetTaskFn = func(taskID int) (*bosh.Task, error) {
					return &bosh.Task{ID: taskID, State: outcomeTaskProcessing, Description: outcomeDeleteDeployment}, nil
				}

				response := poll(outcomeDeprovision)

				Expect(response.State).To(Equal(osbapi.StateInProgress), "a delete whose event names a task answered %+v", response)
				Expect(recorded().TaskID).To(Equal(deleteTaskID), "the delete event's task was not adopted")
				Expect(recorded().State).ToNot(Equal(outcomeStateUnknown))
			})

			It("does not adopt a running task that is not a delete, nor a create event", func() {
				director.FindRunningTaskForDeploymentFn = func(name string) (*bosh.Task, error) {
					return &bosh.Task{ID: createTaskID, State: outcomeTaskProcessing, Description: outcomeCreateDeployment, Deployment: name}, nil
				}
				director.onGetEvents = func(context.Context, string) ([]bosh.Event, error) {
					return deploymentEvents(boshEventCreate, deploymentName, createTaskID), nil
				}

				response := poll(outcomeDeprovision)

				Expect(response.State).To(Equal(osbapi.StateFailed), "a delete with no delete task answered %+v", response)
				Expect(response.Description).To(ContainSubstring(contextID))
				Expect(director.Calls("GetTask")).To(BeEmpty(), "the poll followed a task that is not a delete")
			})

			It("stays in progress while the check for a running task fails", func() {
				director.FindRunningTaskForDeploymentFn = func(string) (*bosh.Task, error) {
					return nil, routeNotFound()
				}

				response := poll(outcomeDeprovision)

				Expect(response.State).To(Equal(osbapi.StateInProgress), "a delete whose task check failed answered %+v", response)
				Expect(response.Description).To(ContainSubstring(contextID))
				Expect(recorded().State).To(Equal(outcomeStateUnknown))
			})
		})

		It("an unknown delete is not failed while lookups error past the grace period", func() {
			contextID := loseDelete()

			director.FindTaskByContextIDFn = func(string) (*bosh.Task, error) {
				return nil, &boshapi.APIError{StatusCode: http.StatusServiceUnavailable, Method: http.MethodGet, Path: taskListPath}
			}

			passGrace()

			for range 2 {
				response := poll(outcomeDeprovision)

				Expect(response.State).To(Equal(osbapi.StateInProgress), "an unknown delete whose lookup failed answered %+v", response)
				Expect(response.Description).To(ContainSubstring(contextID))
				Expect(response.Description).To(ContainSubstring(taskListPath), "the answer does not carry the failed lookup")
			}

			Expect(stillIndexed()).To(BeTrue())
			Expect(recorded().State).To(Equal(outcomeStateUnknown))
		})

		It("an unknown deploy is adopted when a running task on its deployment turns up", func() {
			loseDeploy()

			director.FindRunningTaskForDeploymentFn = func(name string) (*bosh.Task, error) {
				if name != deploymentName {
					return nil, nil //nolint:nilnil // no running task is a nil task without an error
				}

				return &bosh.Task{ID: createTaskID, State: outcomeTaskProcessing, Description: outcomeCreateDeployment, Deployment: name}, nil
			}
			director.GetTaskFn = func(taskID int) (*bosh.Task, error) {
				return &bosh.Task{ID: taskID, State: outcomeTaskProcessing, Description: outcomeCreateDeployment}, nil
			}

			response := poll(outcomeProvision)

			Expect(response.State).To(Equal(osbapi.StateInProgress), "a running deploy answered %+v", response)
			Expect(recorded().TaskID).To(Equal(createTaskID), "the running task was not adopted")
			Expect(recorded().State).ToNot(Equal(outcomeStateUnknown))
		})

		It("an unknown deploy does not adopt a running task that is not a deploy of its deployment", func() {
			contextID := loseDeploy()

			for _, running := range []bosh.Task{
				{ID: createTaskID, State: outcomeTaskProcessing, Description: "run ssh", Deployment: deploymentName},
				{ID: createTaskID, State: outcomeTaskProcessing, Description: outcomeCreateDeployment, Deployment: "another-deployment"},
			} {
				director.FindRunningTaskForDeploymentFn = func(string) (*bosh.Task, error) {
					return &running, nil
				}

				response := poll(outcomeProvision)

				Expect(response.State).To(Equal(osbapi.StateInProgress), "a deploy with task %+v running answered %+v", running, response)
				Expect(response.Description).To(ContainSubstring(contextID))
				Expect(recorded().State).To(Equal(outcomeStateUnknown), "task %+v was adopted", running)
				Expect(director.Calls("GetTask")).To(BeEmpty(), "the poll followed task %+v", running)
			}
		})

		It("an unknown deploy fails the provision after the grace period", func() {
			contextID := loseDeploy()

			deploymentMissing()

			response := poll(outcomeProvision)
			Expect(response.State).To(Equal(osbapi.StateInProgress), "an unknown deploy in its grace period answered %+v", response)

			passGrace()

			response = poll(outcomeProvision)

			Expect(response.State).To(Equal(osbapi.StateFailed), "an unknown deploy past its grace period answered %+v", response)
			Expect(response.Description).To(ContainSubstring(contextID))
			Expect(recorded().State).To(Equal(outcomeStateFailed))
		})

		It("an unknown deploy is not failed while lookups error past the grace period", func() {
			contextID := loseDeploy()

			deploymentMissing()
			passGrace()

			director.FindTaskByContextIDFn = func(string) (*bosh.Task, error) {
				return nil, &boshapi.APIError{StatusCode: http.StatusServiceUnavailable, Method: http.MethodGet, Path: taskListPath}
			}

			response := poll(outcomeProvision)
			Expect(response.State).To(Equal(osbapi.StateInProgress), "an unknown deploy whose lookup failed answered %+v", response)
			Expect(response.Description).To(ContainSubstring(contextID))
			Expect(response.Description).To(ContainSubstring(taskListPath), "the answer does not carry the failed lookup")

			director.FindTaskByContextIDFn = func(string) (*bosh.Task, error) { return nil, nil } //nolint:nilnil // no task is a nil task without an error
			director.onGetDeployment = func(context.Context, string) (*bosh.DeploymentDetail, error) {
				return nil, routeNotFound()
			}

			response = poll(outcomeProvision)
			Expect(response.State).To(Equal(osbapi.StateInProgress), "an unknown deploy whose deployment check failed answered %+v", response)
			Expect(response.Description).To(ContainSubstring("/deployments/"+deploymentName), "the answer does not carry the failed check")

			Expect(recorded().State).To(Equal(outcomeStateUnknown))
		})

		It("a LastOperation poll answers within 10 seconds while the context-ID lookup hangs", func() {
			contextID := loseDelete()

			lookupFailure := &boshapi.APIError{StatusCode: http.StatusServiceUnavailable, Method: http.MethodGet, Path: taskListPath}

			type lookupSeen struct {
				deadline        time.Time
				hasDeadline     bool
				retriesDisabled bool
			}

			var (
				lookups  atomic.Int32
				seen     = make(chan lookupSeen, 1)
				released = make(chan struct{})
			)

			cleanups = append(cleanups, func() { close(released) })

			// The first lookup fails at once. The second hangs until its
			// context ends, or until the spec ends.
			director.onFindTaskByContextID = func(callCtx context.Context, _ string) (*bosh.Task, error) {
				if lookups.Add(1) == 1 {
					return nil, lookupFailure
				}

				deadline, hasDeadline := callCtx.Deadline()
				seen <- lookupSeen{deadline: deadline, hasDeadline: hasDeadline, retriesDisabled: boshapi.RetriesDisabled(callCtx)}

				select {
				case <-callCtx.Done():
					return nil, fmt.Errorf("get /tasks: %w", callCtx.Err())
				case <-released:
					return nil, errAnswerLost
				}
			}

			response := poll(outcomeDeprovision)
			Expect(response.State).To(Equal(osbapi.StateInProgress))
			Expect(response.Description).To(ContainSubstring(taskListPath))

			started := time.Now()
			answered := make(chan osbapi.LastOperationResponse, 1)

			go func() {
				defer GinkgoRecover()

				answered <- poll(outcomeDeprovision)
			}()

			var lookup lookupSeen

			Eventually(seen, 5*time.Second).Should(Receive(&lookup), "the second poll never looked the task up")
			Expect(lookup.retriesDisabled).To(BeTrue(), "the lookup ran with the director client's retries on")
			Expect(lookup.hasDeadline).To(BeTrue(), "the lookup ran without a deadline")
			// The pass's deadline starts once it holds the instance's lock,
			// a record read or two after the poll began.
			Expect(lookup.deadline.Sub(started)).To(BeNumerically("<=", 10*time.Second+500*time.Millisecond), "the lookup's deadline is more than 10 seconds away")

			// A poll while the pass runs answers at once, with the error
			// the last pass ended with.
			concurrentStart := time.Now()
			concurrent := poll(outcomeDeprovision)

			Expect(time.Since(concurrentStart)).To(BeNumerically("<", 2*time.Second), "a poll waited for the running pass")
			Expect(concurrent.State).To(Equal(osbapi.StateInProgress))
			Expect(concurrent.Description).To(ContainSubstring(contextID))
			Expect(concurrent.Description).To(ContainSubstring("already running"))
			Expect(concurrent.Description).To(ContainSubstring(taskListPath), "the poll does not carry the last pass's error")

			var hung osbapi.LastOperationResponse

			Eventually(answered, 11*time.Second).Should(Receive(&hung), "the poll whose lookup hangs did not answer")
			Expect(time.Since(started)).To(BeNumerically("<=", 10*time.Second+500*time.Millisecond))
			Expect(hung.State).To(Equal(osbapi.StateInProgress), "the poll whose lookup hung answered %+v", hung)
			Expect(hung.Description).To(ContainSubstring(contextID))
			Expect(hung.Description).To(ContainSubstring(context.DeadlineExceeded.Error()))

			Expect(stillIndexed()).To(BeTrue())
			Expect(recorded().State).To(Equal(outcomeStateUnknown))
		})

		It("a LastOperation poll answers within 10 seconds while the read of the task found hangs", func() {
			contextID := loseDelete()

			director.onFindTaskByContextID = func(context.Context, string) (*bosh.Task, error) {
				return &bosh.Task{ID: deleteTaskID, State: outcomeTaskProcessing, Description: outcomeDeleteDeployment, ContextID: contextID}, nil
			}

			type readSeen struct {
				hasDeadline     bool
				retriesDisabled bool
			}

			var (
				seen     = make(chan readSeen, 1)
				released = make(chan struct{})
			)

			cleanups = append(cleanups, func() { close(released) })

			// The read of the task hangs until its context ends, or until
			// the spec ends.
			director.onGetTask = func(callCtx context.Context, _ int) (*bosh.Task, error) {
				_, hasDeadline := callCtx.Deadline()
				seen <- readSeen{hasDeadline: hasDeadline, retriesDisabled: boshapi.RetriesDisabled(callCtx)}

				select {
				case <-callCtx.Done():
					return nil, fmt.Errorf("get /tasks/%d: %w", deleteTaskID, callCtx.Err())
				case <-released:
					return nil, errAnswerLost
				}
			}

			started := time.Now()
			answered := make(chan osbapi.LastOperationResponse, 1)

			go func() {
				defer GinkgoRecover()

				answered <- poll(outcomeDeprovision)
			}()

			var read readSeen

			Eventually(seen, 5*time.Second).Should(Receive(&read), "the poll never read the task it found")
			Expect(read.retriesDisabled).To(BeTrue(), "the read of the task ran with the director client's retries on")
			Expect(read.hasDeadline).To(BeTrue(), "the read of the task ran without a deadline")

			var response osbapi.LastOperationResponse

			Eventually(answered, 11*time.Second).Should(Receive(&response), "the poll whose task read hangs did not answer")
			Expect(time.Since(started)).To(BeNumerically("<=", 10*time.Second+500*time.Millisecond))
			Expect(response.State).To(Equal(osbapi.StateInProgress), "the poll whose task read hung answered %+v", response)
			Expect(response.Description).To(ContainSubstring(fmt.Sprintf("task %d", deleteTaskID)))
			Expect(response.Description).To(ContainSubstring("could not be read"))

			// The task was recorded before the read, so the next poll
			// follows it.
			Expect(recorded().TaskID).To(Equal(deleteTaskID))
			Expect(recorded().State).ToNot(Equal(outcomeStateUnknown))
			Expect(stillIndexed()).To(BeTrue())
		})

		deprovisionRefused := func() error {
			_, _, err := brokerInstance.Deprovision(ctx, instanceID, osbapi.DeprovisionRequest{ServiceID: serviceID, PlanID: planID}, true)

			return err //nolint:wrapcheck // the spec matches the broker's own error
		}

		// refusedFirst runs a deprovision that the recorded unknown outcome
		// must refuse before it asks the director anything.
		refusedFirst := func() {
			checks := len(director.Calls("FindRunningTaskForDeployment"))

			ExpectWithOffset(1, deprovisionRefused()).To(MatchError(osbapi.ErrConcurrencyError))
			ExpectWithOffset(1, director.Calls("FindRunningTaskForDeployment")).To(HaveLen(checks),
				"the deprovision was not refused before it asked the director for running tasks")
		}

		// countDeletes counts the deletes sent from now on, which answer
		// with a running task.
		countDeletes := func() *atomic.Int32 {
			var deletes atomic.Int32

			director.onDeleteDeployment = func(context.Context, string) (*bosh.Task, error) {
				deletes.Add(1)

				return &bosh.Task{ID: deleteTaskID, State: outcomeTaskProcessing, Description: outcomeDeleteDeployment}, nil
			}

			return &deletes
		}

		Describe("a deprovision over a provision whose outcome is unknown", func() {
			var (
				contextID string
				unknown   broker.RecordedOperation
			)

			BeforeEach(func() {
				contextID = loseDeploy()
				unknown = recorded()

				Expect(unknown.UnknownSince).ToNot(BeZero())
			})

			It("is refused a second before the grace period ends", func() {
				frozenAt.Store(unknown.UnknownSince.Add(bosh.UnknownOutcomeGrace - time.Second).UnixNano())

				deletes := countDeletes()

				refusedFirst()

				Expect(recorded()).To(Equal(unknown), "the refused deprovision changed the record")
				Expect(deletes.Load()).To(BeZero(), "the refused deprovision sent a delete")
			})

			It("is accepted once the grace period ends, and its own delete replaces the record", func() {
				frozenAt.Store(unknown.UnknownSince.Add(bosh.UnknownOutcomeGrace).UnixNano())

				deletes := countDeletes()

				director.onGetDeployment = func(context.Context, string) (*bosh.DeploymentDetail, error) {
					return &bosh.DeploymentDetail{Name: deploymentName, Manifest: "name: " + deploymentName}, nil
				}

				deprovision()

				Eventually(deletes.Load, 10*time.Second).Should(BeEquivalentTo(1), "the accepted deprovision sent no delete")
				Eventually(settled, 10*time.Second).Should(BeTrue())

				Expect(recorded().Action).To(Equal(outcomeDeprovision))
				Expect(recorded().State).ToNot(Equal(outcomeStateUnknown))
				Expect(recorded().ContextID).ToNot(BeEmpty(), "the delete's record carries no context ID")
				Expect(recorded().ContextID).ToNot(Equal(contextID), "the delete's record kept the deploy's context ID")
				Expect(recorded().DeploymentName).To(Equal(deploymentName))
			})

			It("is refused past the grace period while a task runs on the deployment", func() {
				passGrace()

				deletes := countDeletes()

				director.FindRunningTaskForDeploymentFn = func(name string) (*bosh.Task, error) {
					return &bosh.Task{ID: createTaskID, State: outcomeTaskProcessing, Description: outcomeCreateDeployment, Deployment: name}, nil
				}

				Expect(deprovisionRefused()).To(MatchError(osbapi.ErrConcurrencyError))

				Expect(recorded()).To(Equal(unknown), "the refused deprovision changed the record")
				Expect(deletes.Load()).To(BeZero(), "the refused deprovision sent a delete")
				Expect(stillIndexed()).To(BeTrue())
			})

			It("is refused past the grace period when the record does not say when the outcome became unknown", func() {
				record := readRecord()
				delete(record, "unknown_since")
				Expect(vaultClient.Put(ctx, instanceID+"/task", record)).To(Succeed())
				Expect(recorded().UnknownSince).To(BeZero())

				passGrace()

				deletes := countDeletes()

				refusedFirst()

				Expect(recorded().ContextID).To(Equal(contextID))
				Expect(recorded().State).To(Equal(outcomeStateUnknown))
				Expect(deletes.Load()).To(BeZero(), "the refused deprovision sent a delete")
			})

			It("past the grace period is refused when the record changed to another unknown provision", func() {
				passGrace()

				deletes := countDeletes()
				replacement := uuid.NewString()

				var changed sync.Once

				// Another unknown deploy is recorded after the deprovision
				// read the record and before it writes its own.
				director.FindRunningTaskForDeploymentFn = func(string) (*bosh.Task, error) {
					changed.Do(func() {
						defer GinkgoRecover()

						Expect(vaultClient.TrackOutcomeUnknown(context.Background(), instanceID, outcomeProvision,
							"The deploy's answer was lost", replacement, time.Now())).To(Succeed())
					})

					return nil, nil //nolint:nilnil // no running task is a nil task without an error
				}

				Expect(deprovisionRefused()).To(MatchError(osbapi.ErrConcurrencyError))

				Expect(recorded().State).To(Equal(outcomeStateUnknown))
				Expect(recorded().ContextID).To(Equal(replacement))
				Expect(deletes.Load()).To(BeZero(), "the refused deprovision sent a delete")
			})
		})

		Describe("a poll over an outcome unknown for a day", func() {
			var (
				errorLog *errorLogger
				previous logger.Logger
			)

			BeforeEach(func() {
				errorLog = &errorLogger{NoOpLogger: &logger.NoOpLogger{}}

				previous = logger.Get()

				logger.Set(errorLog)
			})

			AfterEach(func() {
				logger.Set(previous)
			})

			// alarm reports whether line is the error a day-old unknown
			// outcome logs.
			alarm := func(line, contextID string) bool {
				return strings.Contains(line, contextID) && strings.Contains(line, "more than 24h0m0s ago")
			}

			It("logs an error once for the pass", func() {
				contextID := loseDeploy()

				unknown := recorded()

				frozenAt.Store(unknown.UnknownSince.Add(24 * time.Hour).UnixNano())

				poll(outcomeProvision)

				alarms := 0

				for _, line := range errorLog.errors() {
					if alarm(line, contextID) {
						alarms++
					}
				}

				Expect(alarms).To(Equal(1), "the pass logged %v", errorLog.errors())
			})

			It("logs no such error a second before the day ends", func() {
				contextID := loseDeploy()

				unknown := recorded()

				frozenAt.Store(unknown.UnknownSince.Add(24*time.Hour - time.Second).UnixNano())

				poll(outcomeProvision)

				for _, line := range errorLog.errors() {
					Expect(alarm(line, contextID)).To(BeFalse(), "the pass logged %q", line)
				}
			})
		})

		Describe("the grace period of an unknown deploy", func() {
			var (
				contextID string
				unknown   broker.RecordedOperation
			)

			BeforeEach(func() {
				contextID = loseDeploy()
				unknown = recorded()

				Expect(unknown.UnknownSince).ToNot(BeZero())

				deploymentMissing()
			})

			It("keeps the deploy in progress a second before it ends", func() {
				frozenAt.Store(unknown.UnknownSince.Add(bosh.UnknownOutcomeGrace - time.Second).UnixNano())

				response := poll(outcomeProvision)

				Expect(response.State).To(Equal(osbapi.StateInProgress), "a poll a second before the grace period ends answered %+v", response)
				Expect(response.Description).To(ContainSubstring(contextID))
				Expect(recorded()).To(Equal(unknown), "the poll changed the record")
			})

			It("fails the deploy once it ends", func() {
				frozenAt.Store(unknown.UnknownSince.Add(bosh.UnknownOutcomeGrace).UnixNano())

				response := poll(outcomeProvision)

				Expect(response.State).To(Equal(osbapi.StateFailed), "a poll as the grace period ends answered %+v", response)
				Expect(response.Description).To(ContainSubstring(contextID))
				Expect(recorded().State).To(Equal(outcomeStateFailed))
			})

			It("never ends for a record that does not say when the outcome became unknown", func() {
				record := readRecord()
				delete(record, "unknown_since")
				Expect(vaultClient.Put(ctx, instanceID+"/task", record)).To(Succeed())
				Expect(recorded().UnknownSince).To(BeZero())

				passGrace()

				response := poll(outcomeProvision)

				Expect(response.State).To(Equal(osbapi.StateInProgress), "a deploy with no unknown-since time answered %+v", response)
				Expect(response.Description).To(ContainSubstring("does not say when the outcome became unknown"))
				Expect(recorded().State).To(Equal(outcomeStateUnknown))
				Expect(recorded().ContextID).To(Equal(contextID))
			})

			It("does not fail a deploy whose deployment exists with no task past it", func() {
				director.onGetDeployment = func(context.Context, string) (*bosh.DeploymentDetail, error) {
					return &bosh.DeploymentDetail{Name: deploymentName, Manifest: "name: " + deploymentName}, nil
				}

				passGrace()

				response := poll(outcomeProvision)

				Expect(response.State).To(Equal(osbapi.StateInProgress), "a deploy whose deployment exists answered %+v", response)
				Expect(response.Description).To(ContainSubstring("deployment " + deploymentName + " exists, and no task the director lists names it"))
				Expect(recorded()).To(Equal(unknown), "the poll changed the record")
			})

			It("starts the cleanup of a failed provision once the deploy fails", func() {
				var checks atomic.Int32

				director.onGetDeployment = func(_ context.Context, name string) (*bosh.DeploymentDetail, error) {
					checks.Add(1)

					return nil, fmt.Errorf("%w: %s", bosh.ErrDeploymentNotFound, name)
				}

				passGrace()

				response := poll(outcomeProvision)
				Expect(response.State).To(Equal(osbapi.StateFailed), "an unknown deploy past its grace period answered %+v", response)

				// The pass checks the deployment once, and the cleanup asks
				// for it again.
				Eventually(checks.Load, 10*time.Second).Should(BeNumerically(">=", 2), "the cleanup of the failed provision did not start")
			})
		})

		Describe("an event dated before the request", func() {
			// failedTask makes any task the poll follows a failed one, so a
			// task adopted from an event shows as a failure.
			failedTask := func() {
				director.GetTaskFn = func(taskID int) (*bosh.Task, error) {
					return &bosh.Task{ID: taskID, State: outcomeTaskError}, nil
				}
			}

			It("is not adopted by an unknown delete inside its grace period", func() {
				contextID := loseDelete()

				unknown := recorded()

				director.onGetEvents = func(context.Context, string) ([]bosh.Event, error) {
					return deploymentEventsAt(unknown.UnknownSince.Add(-time.Hour), boshEventDelete, deploymentName, deleteTaskID), nil
				}

				failedTask()

				response := poll(outcomeDeprovision)

				Expect(response.State).To(Equal(osbapi.StateInProgress), "an unknown delete over an older delete event answered %+v", response)
				Expect(response.Description).To(ContainSubstring(contextID))
				Expect(director.Calls("GetTask")).To(BeEmpty(), "the poll followed the task of an older delete")
				Expect(recorded()).To(Equal(unknown), "the poll changed the record")
			})

			It("is adopted by an unknown delete when it falls inside the window", func() {
				loseDelete()

				unknown := recorded()

				director.onGetEvents = func(context.Context, string) ([]bosh.Event, error) {
					return deploymentEventsAt(unknown.UnknownSince.Add(-time.Minute), boshEventDelete, deploymentName, deleteTaskID), nil
				}
				director.GetTaskFn = func(taskID int) (*bosh.Task, error) {
					return &bosh.Task{ID: taskID, State: outcomeTaskProcessing, Description: outcomeDeleteDeployment}, nil
				}

				response := poll(outcomeDeprovision)

				Expect(response.State).To(Equal(osbapi.StateInProgress), "an unknown delete over a recent delete event answered %+v", response)
				Expect(recorded().TaskID).To(Equal(deleteTaskID), "the recent delete event's task was not adopted")
			})

			It("is not adopted by an unknown deploy inside its grace period", func() {
				contextID := loseDeploy()

				unknown := recorded()

				director.onGetEvents = func(context.Context, string) ([]bosh.Event, error) {
					return deploymentEventsAt(unknown.UnknownSince.Add(-time.Hour), boshEventCreate, deploymentName, createTaskID), nil
				}

				failedTask()

				response := poll(outcomeProvision)

				Expect(response.State).To(Equal(osbapi.StateInProgress), "an unknown deploy over an older create event answered %+v", response)
				Expect(response.Description).To(ContainSubstring(contextID))
				Expect(director.Calls("GetTask")).To(BeEmpty(), "the poll followed the task of an older deploy")
				Expect(recorded()).To(Equal(unknown), "the poll changed the record")
			})

			It("is adopted by an unknown deploy when it falls inside the window", func() {
				loseDeploy()

				unknown := recorded()

				director.onGetEvents = func(context.Context, string) ([]bosh.Event, error) {
					return deploymentEventsAt(unknown.UnknownSince.Add(-time.Minute), boshEventCreate, deploymentName, createTaskID), nil
				}
				director.GetTaskFn = func(taskID int) (*bosh.Task, error) {
					return &bosh.Task{ID: taskID, State: outcomeTaskProcessing, Description: outcomeCreateDeployment}, nil
				}

				response := poll(outcomeProvision)

				Expect(response.State).To(Equal(osbapi.StateInProgress), "an unknown deploy over a recent create event answered %+v", response)
				Expect(recorded().TaskID).To(Equal(createTaskID), "the recent create event's task was not adopted")
			})
		})

		It("cleans up a found provision task that failed with the director client's retries on", func() {
			loseDeploy()

			director.FindTaskByContextIDFn = func(string) (*bosh.Task, error) {
				return &bosh.Task{ID: createTaskID, State: outcomeTaskError, Description: outcomeCreateDeployment}, nil
			}
			director.GetTaskFn = func(taskID int) (*bosh.Task, error) {
				return &bosh.Task{ID: taskID, State: outcomeTaskError, Description: outcomeCreateDeployment}, nil
			}

			type cleanupSeen struct {
				retriesDisabled bool
				hasDeadline     bool
				deadline        time.Duration
			}

			seen := make(chan cleanupSeen, 4)

			// The task is found by its context ID, so the pass asks nothing
			// about the deployment, and the first question is the cleanup's.
			director.onGetDeployment = func(callCtx context.Context, name string) (*bosh.DeploymentDetail, error) {
				deadline, hasDeadline := callCtx.Deadline()
				seen <- cleanupSeen{retriesDisabled: boshapi.RetriesDisabled(callCtx), hasDeadline: hasDeadline, deadline: time.Until(deadline)}

				return nil, fmt.Errorf("%w: %s", bosh.ErrDeploymentNotFound, name)
			}

			response := poll(outcomeProvision)
			Expect(response.State).To(Equal(osbapi.StateFailed), "a provision whose task failed answered %+v", response)

			var cleanup cleanupSeen

			Eventually(seen, 10*time.Second).Should(Receive(&cleanup), "the cleanup of the failed provision did not start")
			Expect(cleanup.retriesDisabled).To(BeFalse(), "the cleanup ran with the director client's retries off")
			Expect(cleanup.hasDeadline).To(BeTrue())
			Expect(cleanup.deadline).To(BeNumerically(">", time.Minute), "the cleanup ran under the pass's bound")
		})

		It("an unknown deploy adopts the task a create event on its deployment names", func() {
			contextID := loseDeploy()

			director.onGetEvents = func(context.Context, string) ([]bosh.Event, error) {
				return deploymentEventsAt(time.Now(), boshEventCreate, deploymentName, createTaskID), nil
			}
			director.GetTaskFn = func(taskID int) (*bosh.Task, error) {
				return &bosh.Task{ID: taskID, State: outcomeTaskProcessing, Description: outcomeCreateDeployment}, nil
			}

			response := poll(outcomeProvision)

			Expect(response.State).To(Equal(osbapi.StateInProgress), "a deploy whose create event names a task answered %+v", response)
			Expect(recorded().TaskID).To(Equal(createTaskID), "the create event's task was not adopted")
			Expect(recorded().State).ToNot(Equal(outcomeStateUnknown))
			Expect(recorded().ContextID).To(Equal(contextID))
		})

		Describe("a pass handed an operation the record no longer holds", func() {
			var unknown broker.RecordedOperation

			BeforeEach(func() {
				loseDeploy()

				unknown = recorded()

				deploymentMissing()
				passGrace()
			})

			resolve := func(operation broker.RecordedOperation) {
				response, err := brokerInstance.ResolveUnknownOutcome(ctx, instanceID, deploymentName, operation)
				ExpectWithOffset(1, err).ToNot(HaveOccurred())

				ExpectWithOffset(1, response.State).To(Equal(osbapi.StateInProgress), "a pass over a changed record answered %+v", response)
				ExpectWithOffset(1, response.Description).To(ContainSubstring("was just settled"))
				ExpectWithOffset(1, recorded()).To(Equal(unknown), "the pass changed a record it was not handed")
			}

			It("leaves the record when it names another context ID", func() {
				operation := unknown
				operation.ContextID = uuid.NewString()

				resolve(operation)
			})

			It("leaves the record when it names another action", func() {
				operation := unknown
				operation.Action = outcomeDeprovision

				resolve(operation)
			})
		})

		It("a pass asks the director about the deployment its record names", func() {
			loseDelete()

			unknown := recorded()

			Expect(unknown.DeploymentName).To(Equal(deploymentName))

			var (
				namesMu sync.Mutex
				names   []string
			)

			director.onGetDeployment = func(_ context.Context, name string) (*bosh.DeploymentDetail, error) {
				namesMu.Lock()
				defer namesMu.Unlock()

				names = append(names, name)

				return &bosh.DeploymentDetail{Name: name, Manifest: "name: " + name}, nil
			}

			_, err := brokerInstance.ResolveUnknownOutcome(ctx, instanceID, "a-deployment-the-poll-names", unknown)
			Expect(err).ToNot(HaveOccurred())

			namesMu.Lock()
			defer namesMu.Unlock()

			Expect(names).ToNot(BeEmpty(), "the pass did not check the deployment")
			Expect(names).To(HaveEach(deploymentName), "the pass checked a deployment its record does not name")
		})

		It("a pass reads the record again under its own bound", func() {
			contextID := loseDeploy()

			unknown := recorded()
			proxy := useProxy()

			// The pass is handed the poll's read, so the read it makes
			// itself is the one held.
			proxy.HoldReads(instanceID+"/task", 1)

			answered := make(chan osbapi.LastOperationResponse, 1)

			go func() {
				defer GinkgoRecover()

				response, err := brokerInstance.ResolveUnknownOutcome(context.Background(), instanceID, deploymentName, unknown)
				Expect(err).ToNot(HaveOccurred())

				answered <- response
			}()

			var response osbapi.LastOperationResponse

			Eventually(answered, 15*time.Second).Should(Receive(&response), "the pass waited on a held read of the record past its bound")
			Expect(response.State).To(Equal(osbapi.StateInProgress), "a pass whose read of the record was held answered %+v", response)
			Expect(response.Description).To(ContainSubstring(contextID))
			Expect(director.Calls("FindTaskByContextID")).ToNot(ContainElement(contextID), "the pass asked the director without the record")
		})

		It("a deprovision is refused when the record cannot be read", func() {
			indexInstance()

			proxy := useProxy()
			deletes := countDeletes()

			director.onGetDeployment = func(context.Context, string) (*bosh.DeploymentDetail, error) {
				return &bosh.DeploymentDetail{Name: deploymentName, Manifest: "name: " + deploymentName}, nil
			}

			// The first read of the record is the check before the
			// deprovision is accepted.
			proxy.FailReads(instanceID+"/task", http.StatusForbidden, refusal, 1)

			Expect(deprovisionRefused()).To(MatchError(ContainSubstring(refusal)))

			Expect(proxy.WritesTo(instanceID+"/task")).To(BeEmpty(), "the refused deprovision wrote a record")
			Consistently(deletes.Load, 300*time.Millisecond).Should(BeZero(), "the refused deprovision sent a delete")
		})

		Describe("a deprovision is refused while the provision's outcome is unknown", func() {
			It("when the record shows it before the deprovision starts", func() {
				contextID := loseDeploy()

				unknown := recorded()
				deletes := countDeletes()

				refusedFirst()

				Expect(recorded()).To(Equal(unknown), "the refused deprovision changed the record")
				Expect(recorded().ContextID).To(Equal(contextID))
				Expect(deletes.Load()).To(BeZero(), "the refused deprovision sent a delete")
				Expect(stillIndexed()).To(BeTrue())
				Expect(settled()).To(BeTrue(), "the refused deprovision left its claim behind")
			})

			// rawIndexEntry returns the instance's index entry as stored,
			// and false when the index has none.
			rawIndexEntry := func() (map[string]interface{}, bool) {
				index, err := suite.vault.ReadSecret("db")
				ExpectWithOffset(1, err).ToNot(HaveOccurred())

				entry, found := index[instanceID].(map[string]interface{})

				return entry, found
			}

			It("when the outcome becomes unknown while the deprovision is being accepted", func() {
				indexInstance()

				before, found := rawIndexEntry()
				Expect(found).To(BeTrue())

				contextID := uuid.NewString()
				deletes := countDeletes()

				var lost sync.Once

				// The provision's answer is lost after the deprovision read
				// the record and before it writes its own.
				director.FindRunningTaskForDeploymentFn = func(string) (*bosh.Task, error) {
					lost.Do(func() {
						defer GinkgoRecover()

						Expect(vaultClient.TrackOutcomeUnknown(context.Background(), instanceID, outcomeProvision,
							"The deploy's answer was lost", contextID, time.Now())).To(Succeed())
					})

					return nil, nil //nolint:nilnil // no running task is a nil task without an error
				}

				Expect(deprovisionRefused()).To(MatchError(osbapi.ErrConcurrencyError))

				Expect(recorded().State).To(Equal(outcomeStateUnknown), "the accepted deprovision replaced the unknown outcome")
				Expect(recorded().Action).To(Equal(outcomeProvision))
				Expect(recorded().ContextID).To(Equal(contextID))
				Expect(deletes.Load()).To(BeZero(), "the refused deprovision sent a delete")
				Expect(stillIndexed()).To(BeTrue())

				after, found := rawIndexEntry()
				Expect(found).To(BeTrue())
				Expect(after).To(Equal(before), "the refused deprovision left its request on the index entry")
			})

			It("takes its request back off the index entry when its record write fails", func() {
				indexInstance()

				before, found := rawIndexEntry()
				Expect(found).To(BeTrue())

				deletes := countDeletes()
				proxy := useProxy()

				proxy.FailWrites(instanceID+"/task", http.StatusForbidden, refusal, -1)

				err := deprovisionRefused()
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring(refusal))
				Expect(deletes.Load()).To(BeZero(), "the refused deprovision sent a delete")

				after, found := rawIndexEntry()
				Expect(found).To(BeTrue())
				Expect(after).To(Equal(before), "the refused deprovision left its request on the index entry")
			})

			It("when the outcome becomes unknown while the deprovision of an unindexed instance is being accepted", func() {
				contextID := uuid.NewString()
				deletes := countDeletes()

				director.onGetDeployment = func(context.Context, string) (*bosh.DeploymentDetail, error) {
					return &bosh.DeploymentDetail{Name: deploymentName, Manifest: "name: " + deploymentName}, nil
				}

				var lost sync.Once

				director.FindRunningTaskForDeploymentFn = func(string) (*bosh.Task, error) {
					lost.Do(func() {
						defer GinkgoRecover()

						Expect(vaultClient.TrackOutcomeUnknown(context.Background(), instanceID, outcomeProvision,
							"The deploy's answer was lost", contextID, time.Now())).To(Succeed())
					})

					return nil, nil //nolint:nilnil // no running task is a nil task without an error
				}

				Expect(deprovisionRefused()).To(MatchError(osbapi.ErrConcurrencyError))

				Expect(recorded().ContextID).To(Equal(contextID))
				Expect(deletes.Load()).To(BeZero(), "the refused deprovision sent a delete")

				Expect(stillIndexed()).To(BeFalse(), "the refused deprovision left the index entry it created")
			})
		})

		It("a provision of an instance whose unknown delete has no index entry is refused", func() {
			contextID := loseDelete()

			Expect(vaultClient.Index(ctx, instanceID, nil)).To(Succeed())

			_, _, err := brokerInstance.Provision(ctx, instanceID, osbapi.ProvisionRequest{
				ServiceID: serviceID, PlanID: planID, OrganizationGUID: outcomeOrgGUID, SpaceGUID: outcomeSpaceGUID,
			}, true)
			Expect(err).To(MatchError(osbapi.ErrConcurrencyError))

			Expect(recorded().State).To(Equal(outcomeStateUnknown))
			Expect(recorded().Action).To(Equal(outcomeDeprovision))
			Expect(recorded().ContextID).To(Equal(contextID))
			Expect(stillIndexed()).To(BeFalse(), "the refused provision indexed the instance")
		})

		It("progress written during an unknown outcome keeps the unknown-since time", func() {
			contextID := loseDelete()

			unknown := recorded()

			Expect(unknown.UnknownSince).ToNot(BeZero())

			brokerInstance.RecordProgress(ctx, instanceID, outcomeDeprovision, "Still waiting for the director")

			Expect(recorded().State).To(Equal(outcomeStateUnknown), "progress settled the unknown outcome")
			Expect(recorded().UnknownSince).To(Equal(unknown.UnknownSince), "progress dropped the unknown-since time")
			Expect(recorded().ContextID).To(Equal(contextID))
			Expect(readRecord()).ToNot(HaveKey("task"), "progress gave the unknown outcome a task ID")

			// The same outcome recorded again keeps the time it first became
			// unknown.
			Expect(vaultClient.TrackOutcomeUnknown(ctx, instanceID, outcomeDeprovision, "The delete's answer was lost again", contextID,
				time.Now().Add(time.Hour))).To(Succeed())
			Expect(recorded().UnknownSince).To(Equal(unknown.UnknownSince), "a second unknown-outcome write moved the unknown-since time")

			// A task recorded outside the resolver does not settle it, for
			// either action.
			for _, action := range []string{outcomeDeprovision, outcomeProvision} {
				brokerInstance.RecordTask(ctx, instanceID, action, "BOSH task in progress (task 300)", deleteTaskID)

				Expect(recorded().State).To(Equal(outcomeStateUnknown), "a %s task recorded outside the resolver settled the unknown outcome", action)
				Expect(recorded().UnknownSince).To(Equal(unknown.UnknownSince))
			}

			// The resolver's write that names the delete's task settles it.
			Expect(vaultClient.SettleOutcomeUnknown(ctx, instanceID, outcomeDeprovision, "BOSH deletion in progress (task 300)", contextID, deleteTaskID)).To(Succeed())

			Expect(recorded().State).ToNot(Equal(outcomeStateUnknown))
			Expect(recorded().TaskID).To(Equal(deleteTaskID))
			Expect(readRecord()).ToNot(HaveKey("unknown_since"), "the settling write kept the unknown-since time")
		})

		Describe("a repeat deprovision is refused while the delete's outcome is unknown", func() {
			It("past the grace period", func() {
				contextID := loseDelete()

				unknown := recorded()
				deletes := countDeletes()

				passGrace()

				refusedFirst()

				Expect(recorded()).To(Equal(unknown), "the refused deprovision changed the record")
				Expect(recorded().ContextID).To(Equal(contextID))
				Expect(deletes.Load()).To(BeZero(), "the refused deprovision sent a delete")
			})

			It("for a deprovision", func() {
				contextID := loseDelete()

				unknown := recorded()
				deletes := countDeletes()

				refusedFirst()

				Expect(recorded()).To(Equal(unknown), "the refused deprovision changed the record")
				Expect(recorded().ContextID).To(Equal(contextID))
				Expect(deletes.Load()).To(BeZero(), "the refused deprovision sent a delete")
				Expect(stillIndexed()).To(BeTrue())
			})

			It("for the teardown of an abandoned deploy, which has no index entry", func() {
				sent := loseDeleteAnswers()

				brokerInstance.TearDownAbandonedDeployment(ctx, instanceID, deploymentName)

				Expect(sent()).To(HaveLen(1))
				Expect(stillIndexed()).To(BeFalse())

				unknown := recorded()
				deletes := countDeletes()

				refusedFirst()

				Expect(recorded()).To(Equal(unknown), "the refused deprovision changed the record")
				Expect(deletes.Load()).To(BeZero(), "the refused deprovision sent a delete")
				Expect(stillIndexed()).To(BeFalse(), "the refused deprovision indexed the instance")
			})
		})

		It("an unknown teardown delete is settled from its record without an index entry", func() {
			sent := loseDeleteAnswers()

			brokerInstance.TearDownAbandonedDeployment(ctx, instanceID, deploymentName)

			Expect(sent()).To(HaveLen(1))
			Expect(stillIndexed()).To(BeFalse())

			response := pollUnindexed(outcomeDeprovision)
			Expect(response.State).To(Equal(osbapi.StateInProgress), "the teardown's unknown delete answered %+v", response)

			deploymentMissing()

			response = pollUnindexed(outcomeDeprovision)

			Expect(response.State).To(Equal(osbapi.StateSucceeded), "the teardown's delete whose deployment is missing answered %+v", response)
			Expect(director.Calls("FindTaskByContextID")).To(ContainElement(sent()[0]), "the poll did not look the task up by its context ID")
			Expect(recorded().State).ToNot(Equal(outcomeStateUnknown), "the settled delete is still recorded as unknown")
		})

		Describe("an unknown deploy whose index entry is gone", func() {
			var contextID string

			BeforeEach(func() {
				contextID = loseDeploy()

				Expect(vaultClient.Index(ctx, instanceID, nil)).To(Succeed())
			})

			It("is failed only after the grace period, when the director says the deployment is missing", func() {
				deploymentMissing()

				response := pollUnindexed(outcomeProvision)
				Expect(response.State).To(Equal(osbapi.StateInProgress), "an index-less unknown deploy in its grace period answered %+v", response)
				Expect(response.Description).To(ContainSubstring(contextID))
				Expect(director.Calls("FindTaskByContextID")).To(ContainElement(contextID), "the poll did not look the task up by its context ID")
				Expect(recorded().State).To(Equal(outcomeStateUnknown))

				passGrace()

				response = pollUnindexed(outcomeProvision)
				Expect(response.State).To(Equal(osbapi.StateFailed), "an index-less unknown deploy past its grace period answered %+v", response)
				Expect(response.Description).To(ContainSubstring(contextID))
				Expect(recorded().State).To(Equal(outcomeStateFailed))

				// The platform's deprovision after the failure is no longer
				// refused as an unknown outcome.
				Expect(deprovisionRefused()).To(MatchError(osbapi.ErrGoneError))
			})

			// provisionAgain sends a provision with the instance's ID and
			// returns a count of the deploys it sends, and its error.
			provisionAgain := func() (*atomic.Int32, error) {
				var deploys atomic.Int32

				director.onCreateDeployment = func(context.Context, string) (*bosh.Task, error) {
					deploys.Add(1)

					return &bosh.Task{ID: createTaskID, State: outcomeTaskProcessing, Description: outcomeCreateDeployment}, nil
				}

				_, _, err := brokerInstance.Provision(ctx, instanceID, osbapi.ProvisionRequest{
					ServiceID: serviceID, PlanID: planID, OrganizationGUID: outcomeOrgGUID, SpaceGUID: outcomeSpaceGUID,
				}, true)

				return &deploys, err //nolint:wrapcheck // the spec matches the broker's own error
			}

			It("refuses a provision with the same ID and keeps the record", func() {
				unknown := recorded()

				deploys, err := provisionAgain()
				Expect(err).To(MatchError(osbapi.ErrConcurrencyError))

				Consistently(deploys.Load, 200*time.Millisecond).Should(BeZero(), "the refused provision sent a deploy")
				Expect(recorded()).To(Equal(unknown), "the refused provision changed the record")
				Expect(recorded().ContextID).To(Equal(contextID))
				Expect(stillIndexed()).To(BeFalse(), "the refused provision indexed the instance")
			})

			It("refuses a provision with the same ID when the record cannot be read", func() {
				proxy := useProxy()

				proxy.FailReads(instanceID+"/task", http.StatusForbidden, refusal, -1)

				deploys, err := provisionAgain()
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring(refusal))

				Consistently(deploys.Load, 200*time.Millisecond).Should(BeZero(), "the refused provision sent a deploy")
				Expect(stillIndexed()).To(BeFalse(), "the refused provision indexed the instance")
			})

			It("refuses a deprovision past the grace period while a task runs on the deployment the director lacks", func() {
				unknown := recorded()

				deploymentMissing()
				passGrace()

				director.FindRunningTaskForDeploymentFn = func(name string) (*bosh.Task, error) {
					return &bosh.Task{ID: createTaskID, State: outcomeTaskQueued, Description: outcomeCreateDeployment, Deployment: name}, nil
				}

				Expect(deprovisionRefused()).To(MatchError(osbapi.ErrConcurrencyError), "a queued deploy was taken for a deployment that is gone")
				Expect(director.Calls("FindRunningTaskForDeployment")).To(ContainElement(deploymentName))
				Expect(recorded()).To(Equal(unknown), "the refused deprovision changed the record")
			})

			It("stays in progress past the grace period while the deployment check fails", func() {
				director.onGetDeployment = func(context.Context, string) (*bosh.DeploymentDetail, error) {
					return nil, routeNotFound()
				}

				passGrace()

				response := pollUnindexed(outcomeProvision)
				Expect(response.State).To(Equal(osbapi.StateInProgress), "an index-less unknown deploy whose check failed answered %+v", response)
				Expect(response.Description).To(ContainSubstring(contextID))
				Expect(recorded().State).To(Equal(outcomeStateUnknown))
			})

			It("answers in progress without asking when no plan names the deployment", func() {
				passGrace()

				response, err := brokerInstance.LastOperation(ctx, instanceID, osbapi.LastOperationRequest{Operation: outcomeProvision})
				Expect(err).ToNot(HaveOccurred())
				Expect(response.State).To(Equal(osbapi.StateInProgress), "an index-less unknown deploy with no plan answered %+v", response)
				Expect(response.Description).To(ContainSubstring(contextID))
				Expect(director.Calls("FindTaskByContextID")).To(BeEmpty(), "the broker asked about a deployment it could not name")
				Expect(recorded().State).To(Equal(outcomeStateUnknown))
			})

			It("answers a deprovision poll in progress and keeps the record", func() {
				unknown := recorded()

				deploymentMissing()
				passGrace()

				response := pollUnindexed(outcomeDeprovision)
				Expect(response.State).To(Equal(osbapi.StateInProgress), "a deprovision poll over an index-less unknown deploy answered %+v", response)
				Expect(response.Description).To(ContainSubstring(contextID))
				Expect(recorded()).To(Equal(unknown))
			})
		})

		It("a provision poll over an index-less unknown delete answers in progress and keeps the record", func() {
			sent := loseDeleteAnswers()

			brokerInstance.TearDownAbandonedDeployment(ctx, instanceID, deploymentName)

			Expect(sent()).To(HaveLen(1))

			unknown := recorded()

			response := pollUnindexed(outcomeProvision)
			Expect(response.State).To(Equal(osbapi.StateInProgress), "a provision poll over an index-less unknown delete answered %+v", response)
			Expect(response.Description).To(ContainSubstring(sent()[0]))
			Expect(recorded()).To(Equal(unknown))
		})

		It("a lost unknown-outcome write is tried again", func() {
			indexInstance()

			proxy := useProxy()

			var (
				sentMu sync.Mutex
				sent   string
			)

			director.onGetDeployment = func(context.Context, string) (*bosh.DeploymentDetail, error) {
				return &bosh.DeploymentDetail{Name: deploymentName, Manifest: "name: " + deploymentName}, nil
			}
			director.onDeleteDeployment = func(callCtx context.Context, name string) (*bosh.Task, error) {
				sentMu.Lock()
				defer sentMu.Unlock()

				sent = boshapi.ContextIDFrom(callCtx)

				// The next task record write is the one that records
				// the unknown outcome.
				proxy.FailWrites(instanceID+"/task", http.StatusForbidden, refusal, 1)

				return nil, &bosh.OutcomeUnknownError{
					Operation:  "DeleteDeployment",
					Director:   outcomeDirectorAddress,
					Deployment: name,
					ContextID:  sent,
					Err:        errAnswerLost,
				}
			}

			before := time.Now()
			writesBefore := len(proxy.WritesTo(instanceID + "/task"))

			deprovision()

			Eventually(settled, 10*time.Second).Should(BeTrue())

			sentMu.Lock()
			contextID := sent
			sentMu.Unlock()

			Expect(contextID).ToNot(BeEmpty(), "the delete was never sent")
			expectRecordedUnknown(outcomeDeprovision, contextID, before)
			Expect(len(proxy.WritesTo(instanceID+"/task")) - writesBefore).To(BeNumerically(">=", 2))
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

		// askedFor makes the director say any deployment is gone, and
		// returns the names the broker asked it about.
		askedFor := func() func() []string {
			var (
				askedMu sync.Mutex
				asked   []string
			)

			director.onGetDeployment = func(_ context.Context, name string) (*bosh.DeploymentDetail, error) {
				askedMu.Lock()
				defer askedMu.Unlock()

				asked = append(asked, name)

				return nil, fmt.Errorf("%w: %s", bosh.ErrDeploymentNotFound, name)
			}

			return func() []string {
				askedMu.Lock()
				defer askedMu.Unlock()

				return append([]string(nil), asked...)
			}
		}

		// recordNamedTeardown writes the record a teardown leaves when it
		// has recorded its context ID and the deployment's name.
		recordNamedTeardown := func() {
			ExpectWithOffset(1, vaultClient.TrackProgressWithDeployment(ctx, instanceID, outcomeDeprovision,
				"Initiating BOSH deployment deletion with retry", uuid.NewString(), deploymentName, 0, nil)).To(Succeed())
		}

		It("asks the director about the deployment the record names", func() {
			renameDeployment()
			recordNamedTeardown()

			asked := askedFor()

			response := pollUnindexed(outcomeDeprovision)
			Expect(response.State).To(Equal(osbapi.StateSucceeded), "a confirmed delete was reported as %+v", response)
			Expect(asked()).To(Equal([]string{deploymentName}))
		})

		It("asks about the deployment the plan names when the record names none", func() {
			recordTeardown()

			asked := askedFor()

			response := pollUnindexed(outcomeDeprovision)
			Expect(response.State).To(Equal(osbapi.StateSucceeded), "a confirmed delete was reported as %+v", response)
			Expect(asked()).To(Equal([]string{planID + "-" + instanceID}))
		})

		It("asks about the deployment the record names when the poll names no plan", func() {
			renameDeployment()
			recordNamedTeardown()

			asked := askedFor()

			response, err := brokerInstance.LastOperation(ctx, instanceID, osbapi.LastOperationRequest{Operation: outcomeDeprovision})
			Expect(err).ToNot(HaveOccurred())
			Expect(response.State).To(Equal(osbapi.StateSucceeded), "a confirmed delete was reported as %+v", response)
			Expect(asked()).To(Equal([]string{deploymentName}))
		})

		It("answers succeeded when the delete failed and the director says the deployment is gone", func() {
			renameDeployment()
			recordNamedTeardown()
			brokerInstance.RecordFailure(ctx, instanceID, outcomeDeprovision, "Deployment deletion failed: director refused the delete")

			asked := askedFor()

			response := pollUnindexed(outcomeDeprovision)
			Expect(response.State).To(Equal(osbapi.StateSucceeded), "a deployment the director says is gone was reported as %+v", response)
			Expect(asked()).To(Equal([]string{deploymentName}))
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

	Describe("a poll with no index entry over a deprovision record", func() {
		const deletedDuringProvision = "was deleted while it was being provisioned"

		// recordFinishedDeprovision writes the record a deprovision leaves
		// when the deployment was gone before its delete, which names no
		// context ID.
		recordFinishedDeprovision := func() {
			ExpectWithOffset(1, vaultClient.Track(ctx, instanceID, outcomeDeprovision, 1, nil)).To(Succeed())
		}

		// recordDelete writes the record of a delete with a context ID.
		recordDelete := func() {
			ExpectWithOffset(1, vaultClient.TrackProgressWithDeployment(ctx, instanceID, outcomeDeprovision,
				"Initiating BOSH deployment deletion with retry", uuid.NewString(), deploymentName, 0, nil)).To(Succeed())
		}

		// deploymentGone makes the director say the deployment is gone, and
		// returns how often the broker asked.
		deploymentGone := func() *atomic.Int32 {
			var asked atomic.Int32

			director.onGetDeployment = func(_ context.Context, name string) (*bosh.DeploymentDetail, error) {
				asked.Add(1)

				return nil, fmt.Errorf("%w: %s", bosh.ErrDeploymentNotFound, name)
			}

			return &asked
		}

		deploymentStays := func() {
			director.onGetDeployment = func(context.Context, string) (*bosh.DeploymentDetail, error) {
				return &bosh.DeploymentDetail{Name: deploymentName, Manifest: "name: " + deploymentName}, nil
			}
		}

		Context("when the poll asks about a provision", func() {
			It("answers failed for a finished deprovision", func() {
				recordFinishedDeprovision()

				response := pollUnindexed(outcomeProvision)
				Expect(response.State).To(Equal(osbapi.StateFailed), "a provision poll over a deprovision answered %+v", response)
				Expect(response.Description).To(ContainSubstring(deletedDuringProvision))
			})

			It("answers failed even when the director says the deployment is gone", func() {
				recordDelete()
				deploymentGone()

				response := pollUnindexed(outcomeProvision)
				Expect(response.State).To(Equal(osbapi.StateFailed), "a provision poll over a deprovision answered %+v", response)
				Expect(response.Description).To(ContainSubstring(deletedDuringProvision))
			})
		})

		Context("when the poll names no operation", func() {
			It("follows a finished deprovision to succeeded", func() {
				recordFinishedDeprovision()

				response := pollUnindexed("")
				Expect(response.State).To(Equal(osbapi.StateSucceeded), "a finished deprovision answered %+v", response)
				Expect(response.Description).To(Equal("Instance has been deleted"))
			})

			It("answers succeeded for a delete only once the director says the deployment is gone", func() {
				recordDelete()

				asked := deploymentGone()

				response := pollUnindexed("")
				Expect(response.State).To(Equal(osbapi.StateSucceeded), "a confirmed delete answered %+v", response)
				Expect(asked.Load()).To(BeNumerically(">", 0), "the broker answered succeeded without asking the director")
			})

			It("answers in progress for a delete whose deployment is still there", func() {
				recordDelete()
				deploymentStays()

				response := pollUnindexed("")
				Expect(response.State).To(Equal(osbapi.StateInProgress), "an unconfirmed delete answered %+v", response)
			})
		})

		Context("when the poll asks about a deprovision", func() {
			It("answers succeeded once the director says the deployment is gone", func() {
				recordDelete()
				deploymentGone()

				response := pollUnindexed(outcomeDeprovision)
				Expect(response.State).To(Equal(osbapi.StateSucceeded), "a confirmed delete answered %+v", response)
			})

			It("answers in progress while the deployment is still there", func() {
				recordDelete()
				deploymentStays()

				response := pollUnindexed(outcomeDeprovision)
				Expect(response.State).To(Equal(osbapi.StateInProgress), "an unconfirmed delete answered %+v", response)
			})
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
			Expect(readRecord()["description"]).To(ContainSubstring(broker.ErrContextIDNotRecorded.Error()))

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

		It("returns an error that wraps ErrContextIDNotRecorded and the cause", func() {
			proxy := useProxy()
			proxy.FailWrites(instanceID+"/task", http.StatusForbidden, refusal, -1)

			var deletes atomic.Int32

			director.onDeleteDeployment = func(context.Context, string) (*bosh.Task, error) {
				deletes.Add(1)

				return &bosh.Task{ID: deleteTaskID, State: outcomeTaskProcessing, Description: outcomeDeleteDeployment}, nil
			}

			err := brokerInstance.DeleteDeploymentWithRetry(ctx, instanceID, deploymentName, uuid.NewString())

			Expect(err).To(MatchError(broker.ErrContextIDNotRecorded))
			Expect(err.Error()).To(ContainSubstring(refusal))
			Expect(deletes.Load()).To(BeZero(), "the delete was sent without its context ID on record")
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
