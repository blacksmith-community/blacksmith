package broker_test

import (
	"context"
	"fmt"
	"net/http"
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

// The Vault client ends a call when its context ends. These specs show that the
// writes which must land, because the work they describe already happened or
// has begun, run under a context of their own and survive the end of the
// request or goroutine context that started them.
var _ = Describe("Vault writes that outlive their context", func() {
	var (
		ctx            context.Context
		endedCtx       context.Context
		director       *contextDirector
		vaultClient    *internalVault.Vault
		brokerInstance *broker.Broker
		instanceID     string
		serviceID      string
		planID         string
		deploymentName string
		initScript     string
		release        chan struct{}
		releaseOnce    sync.Once
		cleanupDirs    []string
	)

	releaseAsync := func() { releaseOnce.Do(func() { close(release) }) }

	readSecret := func(path string) map[string]interface{} {
		data, err := suite.vault.ReadSecret(path)
		ExpectWithOffset(1, err).ToNot(HaveOccurred(), "reading %s", path)

		return data
	}

	indexEntry := func() (*vaultPkg.Instance, bool) {
		instance, exists, err := vaultClient.FindInstance(ctx, instanceID)
		ExpectWithOffset(1, err).ToNot(HaveOccurred())

		return instance, exists
	}

	// mergeMetadata adds fields to the instance's metadata and keeps what is
	// already there.
	mergeMetadata := func(fields map[string]interface{}) {
		metadata, err := suite.vault.ReadSecret(instanceID + "/metadata")
		if err != nil {
			metadata = map[string]interface{}{}
		}

		for key, value := range fields {
			metadata[key] = value
		}

		ExpectWithOffset(1, suite.vault.WriteSecret(instanceID+"/metadata", metadata)).To(Succeed())
	}

	BeforeEach(func() {
		ctx = context.Background()

		cancelled, cancel := context.WithCancel(ctx)
		cancel()

		endedCtx = cancelled
		release = make(chan struct{})
		releaseOnce = sync.Once{}

		instanceID = fmt.Sprintf("vault-ctx-%d", time.Now().UnixNano())
		serviceID = "valkey"
		planID = "standalone"
		deploymentName = planID + "-" + instanceID

		director = &contextDirector{ScriptedBOSHDirector: testutil.NewScriptedBOSHDirector()}
		vaultClient = internalVault.New(suite.vault.Addr, suite.vault.RootToken, true)

		dir, err := os.MkdirTemp("", "blacksmith-vault-ctx")
		Expect(err).ToNot(HaveOccurred())

		cleanupDirs = append(cleanupDirs, dir)

		initScript = filepath.Join(dir, "init")
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
		releaseAsync()

		Eventually(func() bool {
			return brokerInstance.ProvisionActive(instanceID) || brokerInstance.DeprovisionActive(instanceID)
		}, 10*time.Second).Should(BeFalse())

		_ = vaultClient.Index(ctx, instanceID, nil)

		for _, dir := range cleanupDirs {
			_ = os.RemoveAll(dir)
		}

		cleanupDirs = nil
	})

	Describe("the record writes of the provision and deprovision goroutines", func() {
		It("lands a progress record after its parent context has ended", func() {
			brokerInstance.RecordProgress(endedCtx, instanceID, "provision", "Checking the plan")

			Expect(readSecret(instanceID + "/task")).To(HaveKeyWithValue("description", "Checking the plan"))
		})

		It("lands a failure record after its parent context has ended", func() {
			brokerInstance.RecordFailure(endedCtx, instanceID, "provision", "The deploy failed")

			Expect(readSecret(instanceID + "/task")).To(HaveKeyWithValue("state", "failed"))
		})

		It("lands a task record after its parent context has ended", func() {
			brokerInstance.RecordTask(endedCtx, instanceID, "deprovision", "BOSH deletion in progress (task 300)", 300)

			_, taskID, _, err := vaultClient.State(ctx, instanceID)
			Expect(err).ToNot(HaveOccurred())
			Expect(taskID).To(Equal(300))
		})

		It("lands the deleted_at timestamp of the background deprovision after its parent context has ended", func() {
			brokerInstance.StoreDeletedTimestampFromMonitor(endedCtx, instanceID)

			Expect(readSecret(instanceID + "/metadata")).To(HaveKey("deleted_at"))
		})

		It("keeps the deleted_at on record when the background deprovision records it again", func() {
			mergeMetadata(map[string]interface{}{
				"delete_requested_at": "2026-01-01T00:00:00Z",
				"deleted_at":          "2026-01-01T00:05:00Z",
			})

			brokerInstance.StoreDeletedTimestampFromMonitor(ctx, instanceID)

			Expect(readSecret(instanceID + "/metadata")).To(HaveKeyWithValue("deleted_at", "2026-01-01T00:05:00Z"))
		})

		It("records a fresh deleted_at from the background deprovision when the GUID was provisioned again", func() {
			mergeMetadata(map[string]interface{}{
				"delete_requested_at":    "2026-01-01T00:00:00Z",
				"deleted_at":             "2026-01-01T00:05:00Z",
				"provision_requested_at": "2026-02-01T00:00:00Z",
			})

			brokerInstance.StoreDeletedTimestampFromMonitor(ctx, instanceID)

			raw, ok := readSecret(instanceID + "/metadata")["deleted_at"].(string)
			Expect(ok).To(BeTrue())

			recorded, err := time.Parse(time.RFC3339, raw)
			Expect(err).ToNot(HaveOccurred())
			Expect(recorded).To(BeTemporally(">", time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)), "the deletion of the earlier use of this GUID was kept as this one's")
		})

		It("lands the record of an abandoned deployment's teardown after its parent context has ended", func() {
			director.onGetDeployment = func(context.Context, string) (*bosh.DeploymentDetail, error) {
				return nil, fmt.Errorf("%w: %s", bosh.ErrDeploymentNotFound, deploymentName)
			}

			brokerInstance.TearDownAbandonedDeployment(endedCtx, instanceID, deploymentName)

			Expect(readSecret(instanceID + "/task")).To(HaveKeyWithValue("action", "deprovision"))
		})
	})

	Describe("the other writes that follow work already done", func() {
		It("stores a binding's credentials after the request has ended", func() {
			creds := map[string]interface{}{"username": "dynamic-user", "password": "dynamic-pass"}
			bindingID := "binding-" + instanceID

			Expect(brokerInstance.StoreBindingCredentials(endedCtx, instanceID, bindingID, creds)).To(Succeed())

			Expect(readSecret(instanceID + "/bindings/" + bindingID + "/credentials")).To(HaveKeyWithValue("username", "dynamic-user"))
		})

		It("records created_at after the poll that completed the provision has ended", func() {
			Expect(vaultClient.Index(ctx, instanceID, vaultPkg.Instance{
				ID: instanceID, ServiceID: serviceID, PlanID: planID, DeploymentName: deploymentName,
			})).To(Succeed())

			Expect(brokerInstance.UpdateInstanceTimestamp(endedCtx, instanceID)).To(Succeed())

			Expect(readSecret(instanceID + "/metadata")).To(HaveKey("created_at"))

			raw, err := vaultClient.GetIndex(ctx, "db")
			Expect(err).ToNot(HaveOccurred())
			Expect(raw.Data[instanceID]).To(HaveKey("created_at"))
		})
	})

	Describe("Provision", func() {
		It("accepts the request and writes complete records when the request context has ended", func() {
			director.onGetInfo = func(callCtx context.Context) (*bosh.Info, error) {
				select {
				case <-release:
				case <-callCtx.Done():
				}

				return nil, errDirectorUnreachable
			}

			_, async, err := brokerInstance.Provision(endedCtx, instanceID, osbapi.ProvisionRequest{
				ServiceID: serviceID, PlanID: planID, OrganizationGUID: "org-guid", SpaceGUID: "space-guid",
			}, true)
			Expect(err).ToNot(HaveOccurred())
			Expect(async).To(BeTrue())

			instance, exists := indexEntry()
			Expect(exists).To(BeTrue(), "the instance is not indexed")
			Expect(instance.DeploymentName).To(Equal(deploymentName))
			Expect(instance.ServiceID).To(Equal(serviceID))

			Expect(readSecret(instanceID + "/metadata")).To(HaveKey("provision_requested_at"))
			Expect(readSecret(instanceID + "/deployment")).To(HaveKeyWithValue("deployment_name", deploymentName))
			Expect(readSecret(instanceID)).To(HaveKeyWithValue("deployment_name", deploymentName))
			Expect(readSecret(instanceID + "/" + deploymentName)).To(HaveKey("details"))
			Expect(readSecret(instanceID + "/plans/sha256")).To(HaveKey("init"))
			Expect(readSecret(instanceID + "/task")).To(HaveKeyWithValue("action", "provision"))

			releaseAsync()
		})
	})

	Describe("the cleanup after the provision's details cannot be stored", func() {
		It("removes the instance from the index even when the failed write used up its deadline", func() {
			proxy, err := testutil.NewVaultFaultProxy(suite.vault.Addr)
			Expect(err).ToNot(HaveOccurred())

			defer proxy.Close()

			brokerInstance.Vault = internalVault.New(proxy.Addr, suite.vault.RootToken, true)

			Expect(vaultClient.Index(ctx, instanceID, vaultPkg.Instance{
				ID: instanceID, ServiceID: serviceID, PlanID: planID,
			})).To(Succeed())

			// The details write hangs until the record bound runs out. The
			// cleanup that follows cannot reuse that context, or it would fail
			// at once and leave the stale entry counting against the plan limit.
			proxy.HoldWrites(instanceID+"/"+deploymentName, -1)
			defer broker.SetRecordWriteTimeout(300 * time.Millisecond)()

			err = brokerInstance.StoreDeploymentDetails(ctx, instanceID, deploymentName, osbapi.ProvisionRequest{ServiceID: serviceID, PlanID: planID})
			Expect(err).To(MatchError(broker.ErrFailedToStoreServiceMetadata))

			_, exists := indexEntry()
			Expect(exists).To(BeFalse(), "the stale index entry was left behind")
		})
	})

	Describe("the created_at record when the metadata read is slow", func() {
		It("still records created_at in the index when the metadata read used up its deadline", func() {
			proxy, err := testutil.NewVaultFaultProxy(suite.vault.Addr)
			Expect(err).ToNot(HaveOccurred())

			defer proxy.Close()

			brokerInstance.Vault = internalVault.New(proxy.Addr, suite.vault.RootToken, true)

			Expect(vaultClient.Index(ctx, instanceID, vaultPkg.Instance{
				ID: instanceID, ServiceID: serviceID, PlanID: planID, DeploymentName: deploymentName,
			})).To(Succeed())

			// The metadata read hangs until the record bound runs out. The index
			// write that follows cannot reuse that context, or it would fail at
			// once and created_at would be lost from the index.
			proxy.HoldReads(instanceID+"/metadata", -1)
			defer broker.SetRecordWriteTimeout(300 * time.Millisecond)()

			err = brokerInstance.UpdateInstanceTimestamp(ctx, instanceID)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("metadata"))

			raw, err := vaultClient.GetIndex(ctx, "db")
			Expect(err).ToNot(HaveOccurred())
			Expect(raw.Data[instanceID]).To(HaveKey("created_at"), "created_at was lost from the index")
		})
	})

	Describe("the deleted_at record when the index read is slow", func() {
		It("still records deleted_at when the index read used up its deadline", func() {
			proxy, err := testutil.NewVaultFaultProxy(suite.vault.Addr)
			Expect(err).ToNot(HaveOccurred())

			defer proxy.Close()

			brokerInstance.Vault = internalVault.New(proxy.Addr, suite.vault.RootToken, true)

			mergeMetadata(map[string]interface{}{
				"provision_requested_at": time.Now().Add(-time.Hour).Format(time.RFC3339),
			})

			// The index read that dates the lifecycle hangs until its bound
			// runs out. The metadata write that follows cannot share that
			// bound, or it would fail at once and deleted_at would be lost.
			proxy.HoldReads("secret/data/db", 1)

			defer broker.SetRecordWriteTimeout(800 * time.Millisecond)()

			brokerInstance.StoreDeletedTimestamp(ctx, instanceID)

			Expect(readSecret(instanceID+"/metadata")).To(HaveKey("deleted_at"), "the stalled index read dropped deleted_at")
		})
	})

	Describe("Deprovision", func() {
		It("accepts the request and writes complete records when the request context ends while the broker checks the director", func() {
			Expect(vaultClient.Index(ctx, instanceID, vaultPkg.Instance{
				ID: instanceID, ServiceID: serviceID, PlanID: planID, DeploymentName: deploymentName,
			})).To(Succeed())

			requestCtx, endRequest := context.WithCancel(ctx)
			defer endRequest()

			// The request ends right after the broker has read the instance and
			// asked the director whether a task is running.
			director.FindRunningTaskForDeploymentFn = func(string) (*bosh.Task, error) {
				endRequest()

				return nil, nil //nolint:nilnil // no running task is a nil task without an error
			}
			director.onGetDeployment = func(callCtx context.Context, _ string) (*bosh.DeploymentDetail, error) {
				select {
				case <-release:
				case <-callCtx.Done():
				}

				return nil, errDirectorUnreachable
			}

			_, async, err := brokerInstance.Deprovision(requestCtx, instanceID, osbapi.DeprovisionRequest{ServiceID: serviceID, PlanID: planID}, true)
			Expect(err).ToNot(HaveOccurred())
			Expect(async).To(BeTrue())
			Expect(requestCtx.Err()).To(HaveOccurred(), "the request context was meant to end during Deprovision")

			Expect(readSecret(instanceID + "/metadata")).To(HaveKey("delete_requested_at"))
			Expect(readSecret(instanceID + "/task")).To(HaveKeyWithValue("action", "deprovision"))

			instance, exists := indexEntry()
			Expect(exists).To(BeTrue())
			Expect(instance.DeploymentName).To(Equal(deploymentName))

			idx, err := vaultClient.GetIndex(ctx, "db")
			Expect(err).ToNot(HaveOccurred())

			raw, err := idx.Lookup(instanceID)
			Expect(err).ToNot(HaveOccurred())
			Expect(raw).To(HaveKey("deprovision_requested_at"), "the request's index record was lost")

			releaseAsync()
		})
	})

	Describe("LastOperation for a deprovision whose delete task is done", func() {
		var proxy *testutil.VaultFaultProxy

		BeforeEach(func() {
			Expect(vaultClient.Index(ctx, instanceID, vaultPkg.Instance{
				ID: instanceID, ServiceID: serviceID, PlanID: planID, DeploymentName: deploymentName,
			})).To(Succeed())
			Expect(vaultClient.TrackProgress(ctx, instanceID, "deprovision", "BOSH deletion in progress (task 300)", 300, nil)).To(Succeed())

			director.GetTaskFn = func(taskID int) (*bosh.Task, error) {
				return &bosh.Task{ID: taskID, State: "done", Description: "delete deployment"}, nil
			}
			director.onGetDeployment = func(context.Context, string) (*bosh.DeploymentDetail, error) {
				return nil, fmt.Errorf("%w: %s", bosh.ErrDeploymentNotFound, deploymentName)
			}

			var err error

			proxy, err = testutil.NewVaultFaultProxy(suite.vault.Addr)
			Expect(err).ToNot(HaveOccurred())
		})

		AfterEach(func() { proxy.Close() })

		poll := func(pollCtx context.Context) (osbapi.LastOperationResponse, error) {
			return brokerInstance.LastOperation(pollCtx, instanceID, osbapi.LastOperationRequest{
				ServiceID: serviceID, PlanID: planID, Operation: "deprovision",
			})
		}

		It("answers in progress, not succeeded, while the index entry cannot be removed", func() {
			brokerInstance.Vault = internalVault.New(proxy.Addr, suite.vault.RootToken, true)
			proxy.FailWrites("secret/data/db", http.StatusForbidden, "permission denied", -1)

			response, err := poll(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(response.State).To(Equal(osbapi.StateInProgress))

			_, exists := indexEntry()
			Expect(exists).To(BeTrue(), "the entry was removed, so the test did not exercise the failure")

			// The next poll retries the removal, and now it works.
			proxy.ClearFaults()

			response, err = poll(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(response.State).To(Equal(osbapi.StateSucceeded))

			_, exists = indexEntry()
			Expect(exists).To(BeFalse())
		})

		It("keeps the deleted_at it recorded when a poll retries after the index removal failed", func() {
			mergeMetadata(map[string]interface{}{
				"delete_requested_at": "2026-01-01T00:00:00Z",
				"deleted_at":          "2026-01-01T00:05:00Z",
			})

			brokerInstance.Vault = internalVault.New(proxy.Addr, suite.vault.RootToken, true)
			proxy.FailWrites("secret/data/db", http.StatusForbidden, "permission denied", -1)

			response, err := poll(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(response.State).To(Equal(osbapi.StateInProgress))
			Expect(readSecret(instanceID + "/metadata")).To(HaveKeyWithValue("deleted_at", "2026-01-01T00:05:00Z"))

			proxy.ClearFaults()

			response, err = poll(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(response.State).To(Equal(osbapi.StateSucceeded))
			Expect(readSecret(instanceID + "/metadata")).To(HaveKeyWithValue("deleted_at", "2026-01-01T00:05:00Z"))
		})

		It("records a new deleted_at when the one on record is from before the delete request", func() {
			mergeMetadata(map[string]interface{}{
				"delete_requested_at": "2026-02-01T00:00:00Z",
				"deleted_at":          "2026-01-01T00:05:00Z",
			})

			response, err := poll(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(response.State).To(Equal(osbapi.StateSucceeded))

			deletedAt, ok := readSecret(instanceID + "/metadata")["deleted_at"].(string)
			Expect(ok).To(BeTrue())

			recorded, parseErr := time.Parse(time.RFC3339, deletedAt)
			Expect(parseErr).ToNot(HaveOccurred())
			Expect(recorded).To(BeTemporally(">", time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)), "the deletion of the earlier use of this GUID was kept as this deprovision's")
		})

		// indexWithDeprovisionRequest rewrites the instance's index entry with
		// the time Deprovision recorded the request there.
		indexWithDeprovisionRequest := func(requestedAt string) {
			ExpectWithOffset(1, vaultClient.Index(ctx, instanceID, map[string]interface{}{
				"instance_id": instanceID, "service_id": serviceID, "plan_id": planID,
				"deployment_name": deploymentName, "deprovision_requested_at": requestedAt,
			})).To(Succeed())
		}

		// recordedDeletedAt reads deleted_at from the instance's metadata.
		recordedDeletedAt := func() time.Time {
			raw, ok := readSecret(instanceID + "/metadata")["deleted_at"].(string)
			ExpectWithOffset(1, ok).To(BeTrue(), "no deleted_at is recorded")

			recorded, err := time.Parse(time.RFC3339, raw)
			ExpectWithOffset(1, err).ToNot(HaveOccurred())

			return recorded
		}

		It("keeps the first deleted_at when delete_requested_at was never written and a poll retries", func() {
			// The delete_requested_at write failed when the deprovision was
			// accepted, so only the provision times and the index's request
			// time date this lifecycle.
			indexWithDeprovisionRequest("2026-01-01T00:04:00Z")
			mergeMetadata(map[string]interface{}{
				"provision_requested_at": "2026-01-01T00:00:00Z",
				"created_at":             "2026-01-01T00:01:00Z",
				"deleted_at":             "2026-01-01T00:05:00Z",
			})

			brokerInstance.Vault = internalVault.New(proxy.Addr, suite.vault.RootToken, true)
			proxy.FailWrites("secret/data/db", http.StatusForbidden, "permission denied", -1)

			response, err := poll(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(response.State).To(Equal(osbapi.StateInProgress))
			Expect(readSecret(instanceID+"/metadata")).To(HaveKeyWithValue("deleted_at", "2026-01-01T00:05:00Z"), "the retried poll moved deleted_at")

			proxy.ClearFaults()

			response, err = poll(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(response.State).To(Equal(osbapi.StateSucceeded))
			Expect(readSecret(instanceID+"/metadata")).To(HaveKeyWithValue("deleted_at", "2026-01-01T00:05:00Z"), "the retried poll moved deleted_at")
		})

		It("records a fresh deleted_at when the GUID was provisioned again after the deletion on record", func() {
			// The first life of this GUID was requested deleted and deleted.
			// The second life was provisioned later, and the delete_requested_at
			// write of its own deprovision failed.
			mergeMetadata(map[string]interface{}{
				"delete_requested_at":    "2026-01-01T00:00:00Z",
				"deleted_at":             "2026-01-01T00:05:00Z",
				"provision_requested_at": "2026-02-01T00:00:00Z",
			})

			response, err := poll(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(response.State).To(Equal(osbapi.StateSucceeded))
			Expect(recordedDeletedAt()).To(BeTemporally(">", time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)), "the deletion of the earlier use of this GUID was kept as this one's")
		})

		It("records a fresh deleted_at when only the index dates the deprovision after the deletion on record", func() {
			indexWithDeprovisionRequest("2026-02-01T00:00:00Z")
			mergeMetadata(map[string]interface{}{
				"delete_requested_at": "2026-01-01T00:00:00Z",
				"deleted_at":          "2026-01-01T00:05:00Z",
			})

			response, err := poll(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(response.State).To(Equal(osbapi.StateSucceeded))
			Expect(recordedDeletedAt()).To(BeTemporally(">", time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)), "the index's deprovision request was not taken into account")
		})

		It("keeps a deleted_at when no lifecycle start is on record", func() {
			mergeMetadata(map[string]interface{}{
				"deleted_at": "2026-01-01T00:05:00Z",
			})

			response, err := poll(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(response.State).To(Equal(osbapi.StateSucceeded))
			Expect(readSecret(instanceID + "/metadata")).To(HaveKeyWithValue("deleted_at", "2026-01-01T00:05:00Z"))
		})

		It("answers succeeded once the removal lands, although the poll's context ends after the director confirms the deletion", func() {
			pollCtx, endPoll := context.WithCancel(ctx)
			defer endPoll()

			// The poll ends once the director has confirmed the deployment is gone.
			confirm := director.onGetDeployment
			director.onGetDeployment = func(callCtx context.Context, name string) (*bosh.DeploymentDetail, error) {
				detail, err := confirm(callCtx, name)

				endPoll()

				return detail, err
			}

			response, err := poll(pollCtx)
			Expect(err).ToNot(HaveOccurred())

			_, exists := indexEntry()

			Expect(response.State).To(Equal(osbapi.StateSucceeded), "the removal runs detached, so it lands and the poll succeeds")
			Expect(exists).To(BeFalse())
			Expect(readSecret(instanceID + "/metadata")).To(HaveKey("deleted_at"))
		})
	})
})
