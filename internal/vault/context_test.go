package vault_test

import (
	"context"
	"errors"
	"fmt"
	"time"

	"blacksmith/internal/vault"
	"blacksmith/pkg/testutil"
	vaultPkg "blacksmith/pkg/vault"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

// The client ends a Vault call when the call's context ends. Each spec runs a
// call on a context that has already ended, against the in-process Vault, and
// checks both the error and that the call changed nothing.
var _ = Describe("Vault calls and their context", func() {
	var (
		ctx      context.Context
		endedCtx context.Context
		server   *testutil.VaultDevServer
		client   *vault.Vault
		path     string
	)

	BeforeEach(func() {
		ctx = context.Background()

		cancelled, cancel := context.WithCancel(ctx)
		cancel()

		endedCtx = cancelled

		var err error

		server, err = testutil.NewVaultDevServer(nil)
		Expect(err).ToNot(HaveOccurred())

		client = vault.New(server.Addr, server.RootToken, true)
		path = fmt.Sprintf("ctx-%d", time.Now().UnixNano())

		Expect(server.WriteSecret(path, map[string]interface{}{"name": "kept"})).To(Succeed())
	})

	AfterEach(func() {
		server.Close()
	})

	It("fails a Get on a context that has ended", func() {
		var out map[string]interface{}

		exists, err := client.Get(endedCtx, path, &out)
		Expect(err).To(HaveOccurred())
		Expect(errors.Is(err, context.Canceled)).To(BeTrue(), "the error does not carry the context's: %v", err)
		Expect(exists).To(BeFalse())
	})

	It("fails a Put on a context that has ended, and writes nothing", func() {
		err := client.Put(endedCtx, path, map[string]interface{}{"name": "overwritten"})
		Expect(err).To(HaveOccurred())
		Expect(errors.Is(err, context.Canceled)).To(BeTrue(), "the error does not carry the context's: %v", err)

		stored, readErr := server.ReadSecret(path)
		Expect(readErr).ToNot(HaveOccurred())
		Expect(stored).To(HaveKeyWithValue("name", "kept"))
	})

	It("fails a Delete on a context that has ended, and deletes nothing", func() {
		err := client.Delete(endedCtx, path)
		Expect(err).To(HaveOccurred())
		Expect(errors.Is(err, context.Canceled)).To(BeTrue(), "the error does not carry the context's: %v", err)

		stored, readErr := server.ReadSecret(path)
		Expect(readErr).ToNot(HaveOccurred())
		Expect(stored).To(HaveKeyWithValue("name", "kept"))
	})

	It("fails a listing on a context that has ended", func() {
		_, err := client.ListCFRegistrations(endedCtx)
		Expect(err).To(HaveOccurred())
		Expect(errors.Is(err, context.Canceled)).To(BeTrue(), "the error does not carry the context's: %v", err)
	})

	It("does not unseal or retry for a caller whose context ended during the call", func() {
		callerCtx, endCaller := context.WithCancel(ctx)
		defer endCaller()

		unsealAttempts, calls := 0, 0

		client.SetAutoUnsealEnabled(true)
		client.SetAutoUnsealHook(func(context.Context) error {
			unsealAttempts++

			return nil
		})

		err := client.WithAutoUnseal(callerCtx, func() error {
			calls++

			endCaller()

			return vaultPkg.ErrIsSealed
		})
		Expect(err).To(MatchError(vaultPkg.ErrIsSealed))
		Expect(unsealAttempts).To(BeZero())
		Expect(calls).To(Equal(1))
	})

	It("checks the seal state under the caller's context", func() {
		Expect(client.AutoUnsealIfSealed(endedCtx)).ToNot(Succeed())
		Expect(client.AutoUnsealIfSealed(ctx)).To(Succeed())
	})

	It("sets and reads the KV max_versions under the caller's context", func() {
		Expect(client.SetKVMaxVersions(endedCtx, 7)).ToNot(Succeed())

		_, err := client.GetKVMaxVersions(endedCtx)
		Expect(err).To(HaveOccurred())

		Expect(client.SetKVMaxVersions(ctx, 7)).To(Succeed())

		maxVersions, err := client.GetKVMaxVersions(ctx)
		Expect(err).ToNot(HaveOccurred())
		Expect(maxVersions).To(Equal(7))
	})
})
