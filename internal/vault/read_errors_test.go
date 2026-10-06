package vault_test

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"blacksmith/internal/vault"
	"blacksmith/pkg/testutil"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

// A read that fails is not a read that found nothing. These specs fail the
// read through a proxy in front of the in-process Vault and check that the
// error reaches the caller and that no write follows it.
var _ = Describe("Vault read errors", func() {
	var (
		ctx    context.Context
		server *testutil.VaultDevServer
		proxy  *testutil.VaultFaultProxy
		client *vault.Vault
		suffix string
	)

	BeforeEach(func() {
		ctx = context.Background()

		var err error

		server, err = testutil.NewVaultDevServer(nil)
		Expect(err).ToNot(HaveOccurred())

		proxy, err = testutil.NewVaultFaultProxy(server.Addr)
		Expect(err).ToNot(HaveOccurred())

		client = vault.New(proxy.Addr, server.RootToken, true)
		suffix = fmt.Sprintf("%d", time.Now().UnixNano())
	})

	AfterEach(func() {
		proxy.Close()
		server.Close()
	})

	Describe("Get", func() {
		It("returns the data it read after an auto-unseal retry succeeds", func() {
			path := "get-retry-" + suffix
			Expect(server.WriteSecret(path, map[string]interface{}{"name": "kept"})).To(Succeed())

			proxy.FailReads(path, http.StatusBadRequest, "Vault is sealed", 1)
			client.SetAutoUnsealEnabled(true)
			client.SetAutoUnsealHook(func(context.Context) error { return nil })

			var out map[string]interface{}

			exists, err := client.Get(ctx, path, &out)
			Expect(err).ToNot(HaveOccurred())
			Expect(exists).To(BeTrue())
			Expect(out).To(HaveKeyWithValue("name", "kept"))
		})

		It("returns the read error when auto-unseal is off", func() {
			path := "get-error-" + suffix
			Expect(server.WriteSecret(path, map[string]interface{}{"name": "kept"})).To(Succeed())

			proxy.FailReads(path, http.StatusForbidden, "permission denied", -1)

			var out map[string]interface{}

			exists, err := client.Get(ctx, path, &out)
			Expect(err).To(HaveOccurred())
			Expect(exists).To(BeFalse())
		})

		It("reports a missing secret as not found, without an error", func() {
			var out map[string]interface{}

			exists, err := client.Get(ctx, "get-missing-"+suffix, &out)
			Expect(err).ToNot(HaveOccurred())
			Expect(exists).To(BeFalse())
		})
	})

	Describe("ListCFRegistrations", func() {
		It("returns the error when the listing fails", func() {
			Expect(client.SaveCFRegistration(ctx, map[string]interface{}{"id": "reg-" + suffix, "status": "active"})).To(Succeed())

			proxy.FailReads("registrations", http.StatusForbidden, "permission denied", -1)

			registrations, err := client.ListCFRegistrations(ctx)
			Expect(err).To(HaveOccurred())
			Expect(registrations).To(BeEmpty())
		})

		It("returns the error when one registration cannot be read", func() {
			readable, unreadable := "reg-ok-"+suffix, "reg-bad-"+suffix
			Expect(client.SaveCFRegistration(ctx, map[string]interface{}{"id": readable, "status": "active"})).To(Succeed())
			Expect(client.SaveCFRegistration(ctx, map[string]interface{}{"id": unreadable, "status": "active"})).To(Succeed())

			proxy.FailReads("registrations/"+unreadable, http.StatusForbidden, "permission denied", -1)

			registrations, err := client.ListCFRegistrations(ctx)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring(unreadable))
			Expect(registrations).To(BeNil(), "a partial list must not be returned as if it were complete")
		})

		It("skips a registration that was deleted, without an error", func() {
			kept, removed := "reg-kept-"+suffix, "reg-removed-"+suffix
			Expect(client.SaveCFRegistration(ctx, map[string]interface{}{"id": kept, "status": "active"})).To(Succeed())
			Expect(client.SaveCFRegistration(ctx, map[string]interface{}{"id": removed, "status": "active"})).To(Succeed())
			Expect(client.DeleteCFRegistration(ctx, removed)).To(Succeed())

			registrations, err := client.ListCFRegistrations(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(registrations).To(HaveLen(1))
			Expect(registrations[0]).To(HaveKeyWithValue("id", kept))
		})

		It("lists a registration that has a progress record once, without an error", func() {
			id := "reg-progress-" + suffix
			Expect(client.SaveCFRegistration(ctx, map[string]interface{}{"id": id, "status": "active"})).To(Succeed())
			Expect(client.SaveCFRegistrationProgress(ctx, id, map[string]interface{}{"step": "done"})).To(Succeed())

			registrations, err := client.ListCFRegistrations(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(registrations).To(HaveLen(1))
			Expect(registrations[0]).To(HaveKeyWithValue("id", id))
		})

		It("returns an empty list, without an error, when none are registered", func() {
			registrations, err := client.ListCFRegistrations(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(registrations).To(BeEmpty())
		})

		It("returns the registrations that exist", func() {
			Expect(client.SaveCFRegistration(ctx, map[string]interface{}{"id": "reg-" + suffix, "status": "active"})).To(Succeed())

			registrations, err := client.ListCFRegistrations(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(registrations).To(HaveLen(1))
		})
	})

	Describe("AppendHistory", func() {
		It("stops without writing when the metadata cannot be read", func() {
			instanceID := "history-" + suffix
			Expect(server.WriteSecret(instanceID+"/metadata", map[string]interface{}{
				"created_at": "2026-01-01T00:00:00Z",
				"history": []interface{}{
					map[string]interface{}{"timestamp": time.Now().Unix(), "action": "provision", "description": "first"},
				},
			})).To(Succeed())

			proxy.FailReads(instanceID+"/metadata", http.StatusForbidden, "permission denied", -1)

			err := client.AppendHistory(ctx, instanceID, "provision", "second")
			Expect(err).To(HaveOccurred())
			Expect(proxy.WritesTo(instanceID + "/metadata")).To(BeEmpty())

			stored, err := server.ReadSecret(instanceID + "/metadata")
			Expect(err).ToNot(HaveOccurred())
			Expect(stored).To(HaveKeyWithValue("created_at", "2026-01-01T00:00:00Z"))
			Expect(stored["history"]).To(HaveLen(1))
		})

		It("appends to the history when the metadata reads", func() {
			instanceID := "history-ok-" + suffix

			Expect(client.AppendHistory(ctx, instanceID, "provision", "first")).To(Succeed())
			Expect(client.AppendHistory(ctx, instanceID, "provision", "second")).To(Succeed())

			stored, err := server.ReadSecret(instanceID + "/metadata")
			Expect(err).ToNot(HaveOccurred())
			Expect(stored["history"]).To(HaveLen(2))
		})
	})
})
