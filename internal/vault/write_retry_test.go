package vault_test

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"

	"blacksmith/internal/vault"
	"blacksmith/pkg/testutil"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

// A write whose answer is an error may still have landed, so it is sent again
// only when Vault said it was sealed or an unseal has changed Vault since. The
// proxy in front of the in-process Vault fails the first matching write and
// counts every write that reaches it. The API client's own retries of a 5xx
// are turned off, so each count is the broker's attempts alone.
var _ = Describe("Vault write retries", func() {
	const field = "value"

	firstWrite := map[string]interface{}{field: "first"}

	var (
		ctx     context.Context
		server  *testutil.VaultDevServer
		proxy   *testutil.VaultFaultProxy
		client  *vault.Vault
		path    string
		unseals atomic.Int32
	)

	BeforeEach(func() {
		ctx = context.Background()

		var err error

		server, err = testutil.NewVaultDevServer(nil)
		Expect(err).ToNot(HaveOccurred())

		proxy, err = testutil.NewVaultFaultProxy(server.Addr)
		Expect(err).ToNot(HaveOccurred())

		client = vault.New(proxy.Addr, server.RootToken, true)

		apiClient, err := client.GetAPIClient()
		Expect(err).ToNot(HaveOccurred())
		apiClient.SetMaxRetries(0)

		path = fmt.Sprintf("write-retry-%d", time.Now().UnixNano())

		unseals.Store(0)
	})

	AfterEach(func() {
		proxy.Close()
		server.Close()
	})

	// countUnseals turns auto-unseal on with a hook in place of the unseal,
	// which counts the unseals the client asks for.
	countUnseals := func() {
		client.SetAutoUnsealEnabled(true)
		client.SetAutoUnsealHook(func(context.Context) error {
			unseals.Add(1)

			return nil
		})
	}

	Describe("Put", func() {
		It("returns a server error after one attempt", func() {
			proxy.FailWrites(path, http.StatusInternalServerError, "internal error", 1)

			err := client.Put(ctx, path, firstWrite)
			Expect(err).To(MatchError(ContainSubstring("internal error")))
			Expect(proxy.WritesTo(path)).To(HaveLen(1), "the failed write was sent again")
		})

		It("returns a server error after one attempt with auto-unseal on", func() {
			countUnseals()
			proxy.FailWrites(path, http.StatusInternalServerError, "internal error", 1)

			Expect(client.Put(ctx, path, firstWrite)).ToNot(Succeed())
			Expect(proxy.WritesTo(path)).To(HaveLen(1), "the failed write was sent again")
			Expect(unseals.Load()).To(BeZero())
		})

		It("unseals and writes once more when Vault answers that it is sealed", func() {
			countUnseals()
			proxy.FailWrites(path, http.StatusServiceUnavailable, "Vault is sealed", 1)

			Expect(client.Put(ctx, path, map[string]interface{}{field: "retried"})).To(Succeed())
			Expect(unseals.Load()).To(Equal(int32(1)), "the sealed answer did not lead to an unseal")
			Expect(proxy.WritesTo(path)).To(HaveLen(2))

			stored, err := server.ReadSecret(path)
			Expect(err).ToNot(HaveOccurred())
			Expect(stored).To(HaveKeyWithValue(field, "retried"))
		})

		// Vault answered that it was sealed, and by the time the health check
		// runs, something else has unsealed it, so no unseal runs here. The
		// sealed answer means the write never landed, so it is sent again.
		It("writes once more when Vault answered that it was sealed and is found unsealed", func() {
			client.SetAutoUnsealEnabled(true)
			proxy.FailWrites(path, http.StatusServiceUnavailable, "Vault is sealed", 1)

			Expect(client.Put(ctx, path, map[string]interface{}{field: "retried"})).To(Succeed(), "the sealed answer was returned although Vault was found unsealed")
			Expect(proxy.WritesTo(path)).To(HaveLen(2))

			stored, err := server.ReadSecret(path)
			Expect(err).ToNot(HaveOccurred())
			Expect(stored).To(HaveKeyWithValue(field, "retried"))
		})

		// A 503 that is not a seal, against a Vault that the health check
		// finds unsealed, leads to no unseal, so nothing has changed that a
		// second write could depend on.
		It("returns an unavailable answer after one attempt when Vault is not sealed", func() {
			client.SetAutoUnsealEnabled(true)
			proxy.FailWrites(path, http.StatusServiceUnavailable, "standby node", 1)

			err := client.Put(ctx, path, firstWrite)
			Expect(err).To(MatchError(ContainSubstring("standby node")))
			Expect(proxy.WritesTo(path)).To(HaveLen(1), "the failed write was sent again")
		})
	})

	Describe("Delete", func() {
		It("returns a server error after one attempt", func() {
			Expect(server.WriteSecret(path, map[string]interface{}{field: "stored"})).To(Succeed())

			proxy.FailWrites(path, http.StatusInternalServerError, "internal error", 1)

			err := client.Delete(ctx, path)
			Expect(err).To(MatchError(ContainSubstring("internal error")))
			Expect(proxy.WritesTo(path)).To(HaveLen(1), "the failed delete was sent again")
		})

		It("unseals and deletes once more when Vault answers that it is sealed", func() {
			Expect(server.WriteSecret(path, map[string]interface{}{field: "gone"})).To(Succeed())

			countUnseals()
			proxy.FailWrites(path, http.StatusServiceUnavailable, "Vault is sealed", 1)

			Expect(client.Delete(ctx, path)).To(Succeed())
			Expect(unseals.Load()).To(Equal(int32(1)), "the sealed answer did not lead to an unseal")
			Expect(proxy.WritesTo(path)).To(HaveLen(2))
		})
	})

	Describe("Get", func() {
		It("returns an unavailable answer without a retry when Vault is not sealed", func() {
			Expect(server.WriteSecret(path, map[string]interface{}{field: "stored"})).To(Succeed())

			client.SetAutoUnsealEnabled(true)
			proxy.FailReads(path, http.StatusServiceUnavailable, "standby node", 1)

			var out map[string]interface{}

			_, err := client.Get(ctx, path, &out)
			Expect(err).To(MatchError(ContainSubstring("standby node")), "the read was retried against a Vault that was never sealed")
		})
	})
})
