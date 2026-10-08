package vault_test

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"blacksmith/internal/vault"
	"blacksmith/pkg/testutil"

	"github.com/google/uuid"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

// The task record holds the context ID of the operation's director
// request. A write must never record an empty ID, and a write whose read
// of the record it replaces fails still records the ID it was given.
var _ = Describe("Task record writes", func() {
	const (
		provision   = "provision"
		deprovision = "deprovision"
	)

	var (
		ctx        context.Context
		server     *testutil.VaultDevServer
		proxy      *testutil.VaultFaultProxy
		client     *vault.Vault
		instanceID string
	)

	BeforeEach(func() {
		ctx = context.Background()

		var err error

		server, err = testutil.NewVaultDevServer(nil)
		Expect(err).ToNot(HaveOccurred())

		proxy, err = testutil.NewVaultFaultProxy(server.Addr)
		Expect(err).ToNot(HaveOccurred())

		client = vault.New(proxy.Addr, server.RootToken, true)
		instanceID = fmt.Sprintf("task-record-%d", time.Now().UnixNano())
	})

	AfterEach(func() {
		proxy.Close()
		server.Close()
	})

	readRecord := func() map[string]interface{} {
		record, err := server.ReadSecret(instanceID + "/task")
		ExpectWithOffset(1, err).ToNot(HaveOccurred())

		return record
	}

	Describe("an empty context ID", func() {
		It("is refused by TrackProgressWithContextID before anything is written", func() {
			err := client.TrackProgressWithContextID(ctx, instanceID, provision, "Creating BOSH deployment", "", 0, nil)

			Expect(err).To(MatchError(vault.ErrEmptyContextID))
			Expect(proxy.Writes()).To(BeEmpty())
		})

		It("is refused by TrackProgressWithDeployment before anything is written", func() {
			err := client.TrackProgressWithDeployment(ctx, instanceID, deprovision, "Initiating BOSH deployment deletion with retry", "", "a-deployment", 0, nil)

			Expect(err).To(MatchError(vault.ErrEmptyContextID))
			Expect(proxy.Writes()).To(BeEmpty())
		})

		It("is refused by TrackOutcomeUnknown before anything is written", func() {
			err := client.TrackOutcomeUnknown(ctx, instanceID, deprovision, "The delete's answer was lost", "", time.Now())

			Expect(err).To(MatchError(vault.ErrEmptyContextID))
			Expect(proxy.Writes()).To(BeEmpty())
		})
	})

	Describe("the deployment name a delete records", func() {
		const (
			deploymentName = "renamed-deployment"
			deleting       = "Initiating BOSH deployment deletion with retry"
		)

		BeforeEach(func() {
			Expect(client.TrackProgressWithDeployment(ctx, instanceID, deprovision, deleting, uuid.NewString(), deploymentName, 0, nil)).To(Succeed())
		})

		It("is recorded with the context ID", func() {
			Expect(readRecord()).To(HaveKeyWithValue(vault.TaskRecordDeploymentNameKey, deploymentName))
			Expect(readRecord()).To(HaveKey("context_id"))
		})

		It("is kept by later progress of the same action", func() {
			Expect(client.TrackProgress(ctx, instanceID, deprovision, "BOSH deletion in progress (task 300)", 300, nil)).To(Succeed())

			Expect(readRecord()).To(HaveKeyWithValue(vault.TaskRecordDeploymentNameKey, deploymentName))
		})

		It("is kept by a later write with a new context ID and no name", func() {
			Expect(client.TrackProgressWithContextID(ctx, instanceID, deprovision, "Deletion retry 1/3 failed", uuid.NewString(), 0, nil)).To(Succeed())

			Expect(readRecord()).To(HaveKeyWithValue(vault.TaskRecordDeploymentNameKey, deploymentName))
		})

		It("is replaced by a later write that names another deployment", func() {
			Expect(client.TrackProgressWithDeployment(ctx, instanceID, deprovision, deleting, uuid.NewString(), "another-deployment", 0, nil)).To(Succeed())

			Expect(readRecord()).To(HaveKeyWithValue(vault.TaskRecordDeploymentNameKey, "another-deployment"))
		})

		It("is kept by an unknown outcome of the same action", func() {
			Expect(client.TrackOutcomeUnknown(ctx, instanceID, deprovision, "The delete's answer was lost", uuid.NewString(), time.Now())).To(Succeed())

			Expect(readRecord()).To(HaveKeyWithValue(vault.TaskRecordDeploymentNameKey, deploymentName))
		})

		It("is not taken by a record for another action", func() {
			Expect(client.TrackProgress(ctx, instanceID, provision, "Starting provisioning process", 0, nil)).To(Succeed())

			Expect(readRecord()).ToNot(HaveKey(vault.TaskRecordDeploymentNameKey))
		})
	})

	Describe("TrackOutcomeUnknown", func() {
		const keptParam = "the provision's parameter"

		params := map[string]interface{}{"instance_id": keptParam}

		It("keeps the params of a record for the same action", func() {
			Expect(client.TrackProgress(ctx, instanceID, provision, "Creating BOSH deployment", 0, params)).To(Succeed())

			Expect(client.TrackOutcomeUnknown(ctx, instanceID, provision, "The deploy's answer was lost", uuid.NewString(), time.Now())).To(Succeed())

			Expect(readRecord()).To(HaveKeyWithValue("params", HaveKeyWithValue("instance_id", keptParam)))
		})

		It("does not take the params of a record for another action", func() {
			Expect(client.TrackProgress(ctx, instanceID, provision, "Creating BOSH deployment", 0, params)).To(Succeed())

			Expect(client.TrackOutcomeUnknown(ctx, instanceID, deprovision, "The delete's answer was lost", uuid.NewString(), time.Now())).To(Succeed())

			Expect(readRecord()["params"]).To(BeNil())
		})

		It("records the unknown-since time in UTC with nanoseconds", func() {
			since := time.Date(2026, 10, 7, 12, 30, 45, 123456789, time.FixedZone("UTC-4", -4*60*60))

			Expect(client.TrackOutcomeUnknown(ctx, instanceID, deprovision, "The delete's answer was lost", uuid.NewString(), since)).To(Succeed())

			Expect(readRecord()).To(HaveKeyWithValue(vault.TaskRecordUnknownSinceKey, "2026-10-07T16:30:45.123456789Z"))
		})
	})

	// Only the write that settles an unknown outcome may replace it, and
	// only while the record still holds that outcome.
	Describe("a write that settles an unknown outcome", func() {
		const (
			lost     = "The delete's answer was lost"
			deleting = "BOSH deletion in progress (task 300)"
			taskID   = 300
		)

		var contextID string

		BeforeEach(func() {
			contextID = uuid.NewString()
		})

		recordUnknown := func(action string) {
			ExpectWithOffset(1, client.TrackOutcomeUnknown(ctx, instanceID, action, lost, contextID, time.Now())).To(Succeed())
		}

		taskWrites := func() int {
			return len(proxy.WritesTo(instanceID + "/task"))
		}

		It("records the task over the unknown outcome of the same action and context ID", func() {
			recordUnknown(deprovision)

			Expect(client.SettleOutcomeUnknown(ctx, instanceID, deprovision, deleting, contextID, taskID)).To(Succeed())

			Expect(readRecord()).To(HaveKeyWithValue("state", "in_progress"))
			Expect(fmt.Sprint(readRecord()["task"])).To(Equal("300"))
			Expect(readRecord()).To(HaveKeyWithValue("context_id", contextID))
			Expect(readRecord()).ToNot(HaveKey(vault.TaskRecordUnknownSinceKey))
		})

		It("records the failure -1 over the unknown outcome", func() {
			recordUnknown(provision)

			Expect(client.SettleOutcomeUnknown(ctx, instanceID, provision, "The deploy never reached the director", contextID, -1)).To(Succeed())

			Expect(readRecord()).To(HaveKeyWithValue("state", "failed"))
		})

		It("refuses a write with no task, which would keep the outcome unknown", func() {
			recordUnknown(deprovision)

			writes := taskWrites()

			Expect(client.SettleOutcomeUnknown(ctx, instanceID, deprovision, deleting, contextID, 0)).To(MatchError(vault.ErrOutcomeUnknownSettled))
			Expect(taskWrites()).To(Equal(writes))
		})

		It("refuses when the record names another context ID", func() {
			recordUnknown(deprovision)

			writes := taskWrites()

			Expect(client.SettleOutcomeUnknown(ctx, instanceID, deprovision, deleting, uuid.NewString(), taskID)).To(MatchError(vault.ErrOutcomeUnknownSettled))
			Expect(taskWrites()).To(Equal(writes))
			Expect(readRecord()).To(HaveKeyWithValue("state", vault.TaskStateOutcomeUnknown))
		})

		It("refuses when the record names another action", func() {
			recordUnknown(provision)

			writes := taskWrites()

			Expect(client.SettleOutcomeUnknown(ctx, instanceID, deprovision, deleting, contextID, taskID)).To(MatchError(vault.ErrOutcomeUnknownSettled))
			Expect(taskWrites()).To(Equal(writes))
			Expect(readRecord()).To(HaveKeyWithValue("action", provision))
		})

		It("refuses when the outcome was already settled", func() {
			recordUnknown(deprovision)
			Expect(client.SettleOutcomeUnknown(ctx, instanceID, deprovision, deleting, contextID, taskID)).To(Succeed())

			writes := taskWrites()

			Expect(client.SettleOutcomeUnknown(ctx, instanceID, deprovision, "failed", contextID, -1)).To(MatchError(vault.ErrOutcomeUnknownSettled))
			Expect(taskWrites()).To(Equal(writes))
			Expect(readRecord()).To(HaveKeyWithValue("state", "in_progress"))
		})

		It("refuses when there is no record", func() {
			Expect(client.SettleOutcomeUnknown(ctx, instanceID, deprovision, deleting, contextID, taskID)).To(MatchError(vault.ErrOutcomeUnknownSettled))
			Expect(proxy.WritesTo(instanceID + "/task")).To(BeEmpty())
		})

		It("refuses when the record cannot be read", func() {
			recordUnknown(deprovision)

			writes := taskWrites()

			proxy.FailReads(instanceID+"/task", http.StatusForbidden, "permission denied", 1)

			err := client.SettleOutcomeUnknown(ctx, instanceID, deprovision, deleting, contextID, taskID)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("permission denied"))
			Expect(taskWrites()).To(Equal(writes))
		})

		It("records a deleted instance with no context ID", func() {
			recordUnknown(deprovision)

			Expect(client.SettleOutcomeUnknownAsDeleted(ctx, instanceID, deprovision, contextID)).To(Succeed())

			Expect(readRecord()).To(HaveKeyWithValue("action", deprovision))
			Expect(readRecord()).To(HaveKeyWithValue("state", "in_progress"))
			Expect(readRecord()).ToNot(HaveKey("context_id"))
			Expect(readRecord()).ToNot(HaveKey(vault.TaskRecordUnknownSinceKey))
		})

		It("records a deleted instance only over the same unknown outcome", func() {
			recordUnknown(deprovision)

			writes := taskWrites()

			Expect(client.SettleOutcomeUnknownAsDeleted(ctx, instanceID, deprovision, uuid.NewString())).To(MatchError(vault.ErrOutcomeUnknownSettled))
			Expect(taskWrites()).To(Equal(writes))
			Expect(readRecord()).To(HaveKeyWithValue("context_id", contextID))
		})
	})

	// The write that refuses to replace an unknown outcome cannot judge a
	// record it cannot read, so it writes nothing.
	Describe("a write that must not replace an unknown outcome", func() {
		It("writes nothing when the record cannot be read", func() {
			contextID := uuid.NewString()
			Expect(client.TrackOutcomeUnknown(ctx, instanceID, provision, "The deploy's answer was lost", contextID, time.Now())).To(Succeed())

			writes := len(proxy.WritesTo(instanceID + "/task"))

			proxy.FailReads(instanceID+"/task", http.StatusForbidden, "permission denied", 1)

			err := client.TrackProgressUnlessOutcomeUnknown(ctx, instanceID, deprovision, "Deprovision request accepted", 0, nil)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("permission denied"))
			Expect(proxy.WritesTo(instanceID + "/task")).To(HaveLen(writes))
			Expect(readRecord()).To(HaveKeyWithValue("context_id", contextID))
		})
	})

	Describe("a write that replaces a named unknown outcome", func() {
		var contextID string

		BeforeEach(func() {
			contextID = uuid.NewString()
			Expect(client.TrackOutcomeUnknown(ctx, instanceID, provision, "The deploy's answer was lost", contextID, time.Now())).To(Succeed())
		})

		It("replaces the unknown outcome it names", func() {
			Expect(client.TrackProgressReplacingOutcomeUnknown(ctx, instanceID, deprovision, "Deprovision request accepted", provision, contextID, 0, nil)).To(Succeed())

			Expect(readRecord()).To(HaveKeyWithValue("action", deprovision))
			Expect(readRecord()).ToNot(HaveKeyWithValue("state", vault.TaskStateOutcomeUnknown))
			Expect(readRecord()).ToNot(HaveKey("context_id"))
			Expect(readRecord()).ToNot(HaveKey("unknown_since"))
		})

		It("refuses an unknown outcome with another context ID", func() {
			err := client.TrackProgressReplacingOutcomeUnknown(ctx, instanceID, deprovision, "Deprovision request accepted", provision, uuid.NewString(), 0, nil)
			Expect(err).To(MatchError(vault.ErrOutcomeUnknownRecorded))
			Expect(readRecord()).To(HaveKeyWithValue("context_id", contextID))
		})

		It("refuses an unknown outcome of another action", func() {
			err := client.TrackProgressReplacingOutcomeUnknown(ctx, instanceID, deprovision, "Deprovision request accepted", deprovision, contextID, 0, nil)
			Expect(err).To(MatchError(vault.ErrOutcomeUnknownRecorded))
			Expect(readRecord()).To(HaveKeyWithValue("state", vault.TaskStateOutcomeUnknown))
		})

		It("refuses every unknown outcome when it names no context ID", func() {
			err := client.TrackProgressReplacingOutcomeUnknown(ctx, instanceID, deprovision, "Deprovision request accepted", provision, "", 0, nil)
			Expect(err).To(MatchError(vault.ErrOutcomeUnknownRecorded))
			Expect(readRecord()).To(HaveKeyWithValue("context_id", contextID))

			// A record that carries no context ID is not replaced either.
			record := readRecord()
			delete(record, "context_id")
			Expect(client.Put(ctx, instanceID+"/task", record)).To(Succeed())

			err = client.TrackProgressReplacingOutcomeUnknown(ctx, instanceID, deprovision, "Deprovision request accepted", provision, "", 0, nil)
			Expect(err).To(MatchError(vault.ErrOutcomeUnknownRecorded))
			Expect(readRecord()).To(HaveKeyWithValue("state", vault.TaskStateOutcomeUnknown))
		})

		It("writes over a record whose outcome is settled", func() {
			Expect(client.SettleOutcomeUnknown(ctx, instanceID, provision, "Create deployment task found", contextID, -1)).To(Succeed())

			Expect(client.TrackProgressReplacingOutcomeUnknown(ctx, instanceID, deprovision, "Deprovision request accepted", provision, uuid.NewString(), 0, nil)).To(Succeed())
			Expect(readRecord()).To(HaveKeyWithValue("action", deprovision))
		})

		It("writes nothing when the record cannot be read", func() {
			writes := len(proxy.WritesTo(instanceID + "/task"))

			proxy.FailReads(instanceID+"/task", http.StatusForbidden, "permission denied", 1)

			err := client.TrackProgressReplacingOutcomeUnknown(ctx, instanceID, deprovision, "Deprovision request accepted", provision, contextID, 0, nil)
			Expect(err).To(HaveOccurred())
			Expect(proxy.WritesTo(instanceID + "/task")).To(HaveLen(writes))
		})
	})

	Describe("a write whose read of the record it replaces fails", func() {
		It("still records the context ID it was given", func() {
			Expect(client.TrackProgressWithContextID(ctx, instanceID, provision, "Starting provisioning process", uuid.NewString(), 0, nil)).To(Succeed())

			proxy.FailReads(instanceID+"/task", http.StatusForbidden, "permission denied", 1)

			contextID := uuid.NewString()
			Expect(client.TrackProgressWithContextID(ctx, instanceID, provision, "Creating BOSH deployment", contextID, 0, nil)).To(Succeed())

			Expect(readRecord()).To(HaveKeyWithValue("context_id", contextID))
			Expect(readRecord()).To(HaveKeyWithValue("description", "Creating BOSH deployment"))
		})

		It("still records the progress, without the context ID it could not read", func() {
			Expect(client.TrackProgressWithContextID(ctx, instanceID, provision, "Starting provisioning process", uuid.NewString(), 0, nil)).To(Succeed())

			proxy.FailReads(instanceID+"/task", http.StatusForbidden, "permission denied", 1)

			Expect(client.TrackProgress(ctx, instanceID, provision, "Uploading BOSH releases", 0, nil)).To(Succeed())

			Expect(readRecord()).To(HaveKeyWithValue("description", "Uploading BOSH releases"))
			Expect(readRecord()).ToNot(HaveKey("context_id"))
		})

		It("still records an unknown outcome", func() {
			Expect(client.TrackProgress(ctx, instanceID, deprovision, "Initiating BOSH deployment deletion with retry", 0, nil)).To(Succeed())

			proxy.FailReads(instanceID+"/task", http.StatusForbidden, "permission denied", 1)

			contextID := uuid.NewString()
			Expect(client.TrackOutcomeUnknown(ctx, instanceID, deprovision, "The delete's answer was lost", contextID, time.Now())).To(Succeed())

			Expect(readRecord()).To(HaveKeyWithValue("state", vault.TaskStateOutcomeUnknown))
			Expect(readRecord()).To(HaveKeyWithValue("context_id", contextID))
		})
	})
})
