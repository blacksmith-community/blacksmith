package main

import (
	"context"
	"errors"

	"github.com/hashicorp/vault/api"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var _ = Describe("Vault Auto-Unseal", func() {
	Context("error classification", func() {
		It("detects sealed via ResponseError 503", func() {
			v := &Vault{}
			err := &api.ResponseError{StatusCode: 503, Errors: []string{"sealed"}}
			Expect(v.isSealedOrUnavailable(err)).To(BeTrue())
		})

		It("detects sealed via error text", func() {
			v := &Vault{}
			err := ErrVaultIsSealed
			Expect(v.isSealedOrUnavailable(err)).To(BeTrue())
		})
	})

	Context("withAutoUnseal behavior", func() {
		It("does not retry when auto-unseal is disabled", func() {
			v := &Vault{autoUnsealEnabled: false}
			calls := 0
			op := func() error {
				calls++

				return ErrVaultIsSealed
			}
			err := v.withAutoUnseal(context.Background(), op)
			Expect(err).To(HaveOccurred())
			Expect(calls).To(Equal(1))
		})

		It("retries once after successful auto-unseal", func() {
			v := &Vault{autoUnsealEnabled: true}
			unsealed := false
			v.autoUnsealHook = func(_ context.Context) error {
				unsealed = true

				return nil
			}
			calls := 0
			op := func() error {
				calls++
				if !unsealed {
					return ErrVaultIsSealed
				}

				return nil
			}
			err := v.withAutoUnseal(context.Background(), op)
			Expect(err).ToNot(HaveOccurred())
			Expect(calls).To(Equal(2)) // initial + retry
		})
	})
})
