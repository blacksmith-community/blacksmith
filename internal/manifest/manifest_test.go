package manifest_test

import (
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	manifestPkg "blacksmith/internal/manifest"
	"blacksmith/internal/services"
	"gopkg.in/yaml.v2"
)

var _ = Describe("Manifests", func() {
	Describe("Generation", func() {
		manifest := `---
meta:
  user: redis
foo: bar
`
		plan := services.Plan{}

		BeforeEach(func() {
			err := yaml.Unmarshal([]byte(manifest), &plan.Manifest)
			Ω(err).ShouldNot(HaveOccurred())
		})

		Context("without user parameters", func() {
			It("can generate a manifest", func() {
				_, err := manifestPkg.GenManifest(plan, map[interface{}]interface{}{})
				Ω(err).ShouldNot(HaveOccurred())
				Ω(manifest).Should(Equal(`foo: bar
meta:
  user: redis
`))
			})
		})

		Context("with user parameters", func() {
			It("can generate a manifest", func() {
				_, err := manifestPkg.GenManifest(plan, map[interface{}]interface{}{
					"meta": map[interface{}]interface{}{
						"params": map[interface{}]interface{}{
							"extra": "value",
						},
					},
				})
				Ω(err).ShouldNot(HaveOccurred())
				Ω(manifest).Should(Equal(`foo: bar
meta:
  params:
    extra: value
  user: redis
`))
			})
		})

		Context("with multi-level user parameters", func() {
			It("can generate a manifest", func() {
				manifest, err := manifestPkg.GenManifest(plan, map[interface{}]interface{}{
					"meta": map[interface{}]interface{}{
						"params": map[interface{}]interface{}{
							"extra": map[interface{}]interface{}{
								"second": "value",
							},
						},
					},
				})
				Ω(err).ShouldNot(HaveOccurred())
				Ω(manifest).Should(Equal(`foo: bar
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
