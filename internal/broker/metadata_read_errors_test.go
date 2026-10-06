package broker_test

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/broker"
	"blacksmith/internal/config"
	"blacksmith/internal/services"
	internalVault "blacksmith/internal/vault"
	"blacksmith/pkg/testutil"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

// When the broker cannot read an instance's metadata it must leave the
// metadata alone. Writing a fresh map over it would erase the history and the
// timestamps that the credential sweep depends on.
var _ = Describe("Vault read errors in the broker", func() {
	var (
		ctx        context.Context
		proxy      *testutil.VaultFaultProxy
		b          *broker.Broker
		instanceID string
		cleanup    []string
	)

	existingMetadata := func() map[string]interface{} {
		return map[string]interface{}{
			"provision_requested_at": "2026-01-01T00:00:00Z",
			"delete_requested_at":    "2026-01-02T00:00:00Z",
			"history": []interface{}{
				map[string]interface{}{"timestamp": time.Now().Unix(), "action": "provision", "description": "first"},
			},
		}
	}

	BeforeEach(func() {
		ctx = context.Background()
		instanceID = fmt.Sprintf("read-errors-%d", time.Now().UnixNano())
		cleanup = []string{instanceID + "/metadata"}

		var err error

		proxy, err = testutil.NewVaultFaultProxy(suite.vault.Addr)
		Expect(err).ToNot(HaveOccurred())

		b = &broker.Broker{
			Plans:  map[string]services.Plan{},
			BOSH:   testutil.NewMockBOSHDirector(),
			Vault:  internalVault.New(proxy.Addr, suite.vault.RootToken, true),
			Config: &config.Config{},
		}

		Expect(suite.vault.WriteSecret(instanceID+"/metadata", existingMetadata())).To(Succeed())
		proxy.FailReads(instanceID+"/metadata", http.StatusForbidden, "permission denied", -1)
	})

	AfterEach(func() {
		proxy.Close()

		for _, path := range cleanup {
			_ = suite.vault.DeleteSecret(path)
		}
	})

	expectMetadataUntouched := func() {
		Expect(proxy.WritesTo(instanceID + "/metadata")).To(BeEmpty())

		stored, err := suite.vault.ReadSecret(instanceID + "/metadata")
		Expect(err).ToNot(HaveOccurred())
		Expect(stored).To(HaveLen(3), "the metadata must gain no field")
		Expect(stored).To(HaveKeyWithValue("provision_requested_at", "2026-01-01T00:00:00Z"))
		Expect(stored).To(HaveKeyWithValue("delete_requested_at", "2026-01-02T00:00:00Z"))
		Expect(stored["history"]).To(HaveLen(1))
	}

	It("keeps the metadata when delete_requested_at cannot be recorded", func() {
		b.StoreDeleteRequestedTimestamp(ctx, instanceID)
		expectMetadataUntouched()
	})

	It("keeps the metadata when deleted_at cannot be recorded after a deprovision", func() {
		b.StoreDeletedTimestamp(ctx, instanceID)
		expectMetadataUntouched()
	})

	It("keeps the metadata when the background deprovision cannot record deleted_at", func() {
		b.StoreDeletedTimestampFromMonitor(ctx, instanceID)
		expectMetadataUntouched()
	})

	It("keeps the metadata and reports the error when created_at cannot be recorded", func() {
		err := b.UpdateInstanceTimestamp(ctx, instanceID)
		Expect(err).To(HaveOccurred())
		expectMetadataUntouched()
	})

	It("still records created_at in the index, then reports the error, when the metadata cannot be read", func() {
		Expect(b.Vault.Index(ctx, instanceID, map[string]interface{}{"id": instanceID, "status": "succeeded"})).To(Succeed())

		defer func() { _ = b.Vault.Index(ctx, instanceID, nil) }()

		err := b.UpdateInstanceTimestamp(ctx, instanceID)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("metadata"))
		expectMetadataUntouched()

		idx, idxErr := b.Vault.GetIndex(ctx, "db")
		Expect(idxErr).ToNot(HaveOccurred())

		entry, lookupErr := idx.Lookup(instanceID)
		Expect(lookupErr).ToNot(HaveOccurred())
		Expect(entry).To(HaveKey("created_at"), "the index entry must carry created_at even though the metadata read failed")
		Expect(entry).To(HaveKeyWithValue("status", "succeeded"))
	})

	It("writes the timestamps when the metadata reads", func() {
		proxy.ClearFaults()

		Expect(b.Vault.Index(ctx, instanceID, map[string]interface{}{"id": instanceID})).To(Succeed())

		removeFromIndex := func() { _ = b.Vault.Index(ctx, instanceID, nil) }
		defer removeFromIndex()

		b.StoreDeleteRequestedTimestamp(ctx, instanceID)
		b.StoreDeletedTimestamp(ctx, instanceID)
		Expect(b.UpdateInstanceTimestamp(ctx, instanceID)).To(Succeed())

		stored, err := suite.vault.ReadSecret(instanceID + "/metadata")
		Expect(err).ToNot(HaveOccurred())
		Expect(stored).To(HaveKey("deleted_at"))
		Expect(stored).To(HaveKey("created_at"))
		Expect(stored).To(HaveKeyWithValue("provision_requested_at", "2026-01-01T00:00:00Z"))
		Expect(stored["history"]).To(HaveLen(1))
	})

	Describe("GetBindingCredentials", func() {
		var (
			bindingID string
			mockBOSH  *testutil.MockBOSHDirector
		)

		BeforeEach(func() {
			bindingID = "binding-" + instanceID
			planID, serviceID := "small-plan", "redis-service"
			credPath := instanceID + "/bindings/" + bindingID + "/credentials"
			cleanup = append(cleanup, credPath, instanceID+"/deployment")

			Expect(suite.vault.WriteSecret(instanceID+"/deployment", map[string]interface{}{
				"service_id": serviceID, "plan_id": planID,
			})).To(Succeed())
			Expect(suite.vault.WriteSecret(credPath, map[string]interface{}{
				"host": "stored.example.com", "username": "stored-user", "password": "stored-pass",
			})).To(Succeed())

			mockBOSH = testutil.NewMockBOSHDirector()
			mockBOSH.SetVMs(planID+"-"+instanceID, []bosh.VM{{ID: "vm-0", Job: "redis", Index: 0, IPs: []string{"10.0.0.10"}}})

			b.BOSH = mockBOSH
			b.Plans[serviceID+"/"+planID] = services.Plan{
				ID: planID, Name: "Small", Type: "redis",
				Credentials: toInterfaceMap(map[string]interface{}{
					"credentials": map[string]interface{}{"host": "rebuilt.example.com", "port": 6379, "username": "u", "password": "p"},
				}),
			}

			proxy.FailReads(credPath, http.StatusForbidden, "permission denied", -1)
		})

		It("returns the read error and does not rebuild the credentials", func() {
			credentials, err := b.GetBindingCredentials(ctx, instanceID, bindingID)
			Expect(err).To(HaveOccurred())
			Expect(credentials).To(BeNil())
			Expect(proxy.WritesTo(bindingID + "/credentials")).To(BeEmpty())

			stored, readErr := suite.vault.ReadSecret(instanceID + "/bindings/" + bindingID + "/credentials")
			Expect(readErr).ToNot(HaveOccurred())
			Expect(stored).To(HaveKeyWithValue("host", "stored.example.com"))
		})

		It("returns the stored credentials when the read works", func() {
			proxy.ClearFaults()

			credentials, err := b.GetBindingCredentials(ctx, instanceID, bindingID)
			Expect(err).ToNot(HaveOccurred())
			Expect(credentials.Raw).To(HaveKeyWithValue("host", "stored.example.com"))
		})
	})
})
