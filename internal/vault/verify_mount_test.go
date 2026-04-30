package vault_test

import (
	"context"

	"blacksmith/internal/vault"
	"github.com/hashicorp/vault/api"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var _ = Describe("Vault VerifyMount", func() {
	Context("auto-unseal retry on sealed vault", func() {
		It("retries VerifyMount once after auto-unseal succeeds", func() {
			v := vault.New("http://127.0.0.1:8200", "test-token", false)

			unsealed := false
			v.SetAutoUnsealEnabled(true)
			v.SetAutoUnsealHook(func(_ context.Context) error {
				unsealed = true
				return nil
			})

			calls := 0
			v.SetVerifyMountFn(func(store string, createIfMissing bool) error {
				calls++
				if !unsealed {
					return &api.ResponseError{StatusCode: 503, Errors: []string{"sealed"}}
				}
				return nil
			})

			err := v.VerifyMount("secret", true)
			Expect(err).ToNot(HaveOccurred())
			Expect(calls).To(Equal(2), "VerifyMount should be called twice: once sealed, once after unseal")
		})
	})
})
