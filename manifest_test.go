package main_test

import (
	. "github.com/cloudfoundry-community/blacksmith"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var _ = Describe("Manifests", func() {
	Describe("Generation", func() {
		manifest := `---
meta:
  user: redis
foo: bar
`
		plan := Plan{
			RawManifest: manifest,
		}

		Context("without user parameters", func() {
			It("can generate a manifest", func() {
				s, err := GenManifest(plan, map[interface{}]interface{}{})
				Ω(err).ShouldNot(HaveOccurred())
				Ω(s).Should(Equal(`foo: bar
meta:
  params: {}
  user: redis
`))
			})
		})

		Context("with user parameters", func() {
			It("can generate a manifest", func() {
				s, err := GenManifest(plan, map[interface{}]interface{}{
					"extra": "value",
				})
				Ω(err).ShouldNot(HaveOccurred())
				Ω(s).Should(Equal(`foo: bar
meta:
  params:
    extra: value
  user: redis
`))
			})
		})

		Context("with multi-level user parameters", func() {
			It("can generate a manifest", func() {
				s, err := GenManifest(plan, map[interface{}]interface{}{
					"extra": map[interface{}]interface{}{
						"second": "value",
					},
				})
				Ω(err).ShouldNot(HaveOccurred())
				Ω(s).Should(Equal(`foo: bar
meta:
  params:
    extra:
      second: value
  user: redis
`))
			})
		})
	})
})
