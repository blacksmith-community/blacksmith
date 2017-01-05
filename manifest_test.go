package main_test

import (
	. "github.com/cloudfoundry-community/blacksmith"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	"gopkg.in/yaml.v2"
)

var _ = Describe("Manifests", func() {
	Describe("Generation", func() {
		manifest := `---
meta:
  user: redis
foo: bar
`
		plan := Plan{}

		BeforeEach(func() {
			err := yaml.Unmarshal([]byte(manifest), &plan.Manifest)
			Ω(err).ShouldNot(HaveOccurred())
		})

		Context("without user parameters", func() {
			It("can generate a manifest", func() {
				s, err := GenManifest(plan, map[interface{}]interface{}{})
				Ω(err).ShouldNot(HaveOccurred())
				Ω(s).Should(Equal(`foo: bar
meta:
  user: redis
`))
			})
		})

		Context("with user parameters", func() {
			It("can generate a manifest", func() {
				s, err := GenManifest(plan, map[interface{}]interface{}{
					"meta": map[interface{}]interface{}{
						"params": map[interface{}]interface{}{
							"extra": "value",
						},
					},
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
					"meta": map[interface{}]interface{}{
						"params": map[interface{}]interface{}{
							"extra": map[interface{}]interface{}{
								"second": "value",
							},
						},
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
