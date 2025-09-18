package vault_test

import (
	"context"

	"blacksmith/internal/vault"
	vaultPkg "blacksmith/pkg/vault"
	"github.com/hashicorp/vault/api"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var _ = Describe("Vault Auto-Unseal", func() {
	Context("error classification", func() {
		It("detects sealed via ResponseError 503", func() {
			v := &vault.Vault{}
			err := &api.ResponseError{StatusCode: 503, Errors: []string{"sealed"}}
			Expect(v.IsSealedOrUnavailable(err)).To(BeTrue())
		})

		It("detects sealed via error text", func() {
			v := &vault.Vault{}
			err := vaultPkg.ErrIsSealed
			Expect(v.IsSealedOrUnavailable(err)).To(BeTrue())
		})
	})

	Context("withAutoUnseal behavior", func() {
		It("does not retry when auto-unseal is disabled", func() {
			vaultClient := &vault.Vault{}
			vaultClient.SetAutoUnsealEnabled(false)
			calls := 0
			operation := func() error {
				calls++

				return vaultPkg.ErrIsSealed
			}
			err := vaultClient.WithAutoUnseal(context.Background(), operation)
			Expect(err).To(HaveOccurred())
			Expect(calls).To(Equal(1))
		})

		It("retries once after successful auto-unseal", func() {
			vaultClient := &vault.Vault{}
			vaultClient.SetAutoUnsealEnabled(true)
			unsealed := false
			vaultClient.SetAutoUnsealHook(func(_ context.Context) error {
				unsealed = true

				return nil
			})
			calls := 0
			operation := func() error {
				calls++
				if !unsealed {
					return vaultPkg.ErrIsSealed
				}

				return nil
			}
			err := vaultClient.WithAutoUnseal(context.Background(), operation)
			Expect(err).ToNot(HaveOccurred())
			Expect(calls).To(Equal(2)) // initial + retry
		})
	})
})
