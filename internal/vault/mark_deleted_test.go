package vault_test

import (
	"context"
	"fmt"
	"time"

	"blacksmith/internal/vault"
	"blacksmith/pkg/testutil"
	vaultPkg "blacksmith/pkg/vault"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

// MarkInstanceDeleted used to build a new index entry when the instance was
// not indexed, leaving a tombstone with no service, plan, or deployment
// fields that nothing ever removed.
var _ = Describe("Vault MarkInstanceDeleted", func() {
	var (
		ctx        context.Context
		server     *testutil.VaultDevServer
		client     *vault.Vault
		instanceID string
	)

	BeforeEach(func() {
		ctx = context.Background()

		var err error

		server, err = testutil.NewVaultDevServer(nil)
		Expect(err).ToNot(HaveOccurred())

		client = vault.New(server.Addr, server.RootToken, true)
		instanceID = fmt.Sprintf("mark-%d", time.Now().UnixNano())
	})

	AfterEach(func() {
		server.Close()
	})

	It("does not create a tombstone for an instance that is not indexed", func() {
		Expect(client.MarkInstanceDeleted(ctx, instanceID)).To(Succeed())

		_, exists, err := client.FindInstance(ctx, instanceID)
		Expect(err).ToNot(HaveOccurred())
		Expect(exists).To(BeFalse())
	})

	It("marks an indexed instance deleted and keeps its fields", func() {
		Expect(client.Index(ctx, instanceID, vaultPkg.Instance{
			ID: instanceID, ServiceID: "valkey", PlanID: "standalone", DeploymentName: "standalone-" + instanceID,
		})).To(Succeed())

		Expect(client.MarkInstanceDeleted(ctx, instanceID)).To(Succeed())

		idx, err := client.GetIndex(ctx, "db")
		Expect(err).ToNot(HaveOccurred())

		entry, ok := idx.Data[instanceID].(map[string]interface{})
		Expect(ok).To(BeTrue())
		Expect(entry).To(HaveKeyWithValue("status", "deleted"))
		Expect(entry).To(HaveKeyWithValue("service_id", "valkey"))
		Expect(entry).To(HaveKeyWithValue("deployment_name", "standalone-"+instanceID))
	})
})
