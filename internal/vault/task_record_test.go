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

		It("is refused by TrackOutcomeUnknown before anything is written", func() {
			err := client.TrackOutcomeUnknown(ctx, instanceID, deprovision, "The delete's answer was lost", "", time.Now())

			Expect(err).To(MatchError(vault.ErrEmptyContextID))
			Expect(proxy.Writes()).To(BeEmpty())
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
