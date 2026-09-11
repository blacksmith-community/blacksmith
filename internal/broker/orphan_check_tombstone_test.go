package broker_test

import (
	"context"
	"strconv"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/broker"
	"blacksmith/internal/config"
	internalVault "blacksmith/internal/vault"
	"blacksmith/pkg/testutil"
	vaultPkg "blacksmith/pkg/vault"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

// standalonePlanID is the plan the seeded instances use; the broker derives
// each deployment name from it.
const (
	standalonePlanID = "standalone"
	valkeyServiceID  = "valkey"
)

// The orphan check compares every index entry's deployment against the
// director's deployment list. A deleted tombstone has no service or plan
// fields; it must be skipped, not reported as a parse error.
var _ = Describe("ServiceWithNoDeploymentCheck with a deleted tombstone", func() {
	var (
		ctx            context.Context
		brokerInstance *broker.Broker
		vaultClient    *internalVault.Vault
		director       *testutil.ScriptedBOSHDirector
		tombstoneID    string
		legacyID       string
		orphanID       string
		liveID         string
	)

	BeforeEach(func() {
		ctx = context.Background()
		suffix := strconv.FormatInt(time.Now().UnixNano(), 10)
		tombstoneID = "tomb-" + suffix
		legacyID = "legacy-" + suffix
		orphanID = "orphan-" + suffix
		liveID = "live-" + suffix

		director = testutil.NewScriptedBOSHDirector()
		director.GetDeploymentsFn = func() ([]bosh.Deployment, error) {
			return []bosh.Deployment{{Name: standalonePlanID + "-" + liveID}}, nil
		}

		vaultClient = internalVault.New(suite.vault.Addr, suite.vault.RootToken, true)
		brokerInstance = &broker.Broker{BOSH: director, Vault: vaultClient, Config: &config.Config{}}

		Expect(vaultClient.Index(ctx, tombstoneID, map[string]interface{}{
			"status": "deleted", "deleted_at": time.Now().Format(time.RFC3339), "deleted_by": "vm-monitor",
		})).To(Succeed())
		Expect(vaultClient.Index(ctx, legacyID, map[string]interface{}{
			"deleted": true, "deleted_at": time.Now().Format(time.RFC3339),
		})).To(Succeed())
		Expect(vaultClient.Index(ctx, orphanID, vaultPkg.Instance{ID: orphanID, ServiceID: valkeyServiceID, PlanID: standalonePlanID})).To(Succeed())
		Expect(vaultClient.Index(ctx, liveID, vaultPkg.Instance{ID: liveID, ServiceID: valkeyServiceID, PlanID: standalonePlanID})).To(Succeed())
	})

	AfterEach(func() {
		for _, id := range []string{tombstoneID, legacyID, orphanID, liveID} {
			_ = vaultClient.Index(ctx, id, nil)
		}
	})

	It("skips both tombstone shapes and still removes a real orphan", func() {
		// The suite shares one Vault index, so other specs may leave entries of
		// their own behind. Assert on this spec's instances rather than on the
		// whole removal list.
		removed, err := brokerInstance.ServiceWithNoDeploymentCheck(ctx)
		Expect(err).ToNot(HaveOccurred())
		Expect(removed).To(ContainElement(standalonePlanID + "-" + orphanID))
		Expect(removed).ToNot(ContainElement(ContainSubstring(tombstoneID)))
		Expect(removed).ToNot(ContainElement(ContainSubstring(legacyID)))
		Expect(removed).ToNot(ContainElement(standalonePlanID + "-" + liveID))

		idx, err := vaultClient.GetIndex(ctx, "db")
		Expect(err).ToNot(HaveOccurred())
		Expect(idx.Data).To(HaveKey(tombstoneID), "status tombstone is left for the reconciler sweep")
		Expect(idx.Data).To(HaveKey(legacyID), "legacy deleted-flag tombstone is left for the reconciler sweep")
		Expect(idx.Data).To(HaveKey(liveID))
		Expect(idx.Data).ToNot(HaveKey(orphanID))
	})
})
