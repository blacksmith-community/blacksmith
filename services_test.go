package main_test

import (
	. "github.com/cloudfoundry-community/blacksmith"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var _ = Describe("Services", func() {
	Describe("Plan Metadata Retrieval", func() {
		Context("with a valid plan.yml file", func() {
			It("can read the plan metadata", func() {
				p, err := ReadPlan("test/ok/redis/small")
				Ω(err).ShouldNot(HaveOccurred())
				Ω(p).ShouldNot(BeNil())
				Ω(p.ID).Should(Equal("redis-small-1"))
				Ω(p.Name).Should(Equal("redis-small"))
				Ω(p.Description).Should(Equal("a really small redis"))
			})
		})

		Context("without a plan.yml file", func() {
			It("throws an error", func() {
				_, err := ReadPlan("test/bad/missing/small")
				Ω(err).Should(HaveOccurred())
			})
		})

		Context("without a plan directory", func() {
			It("throws an error", func() {
				_, err := ReadPlan("test/bad/missing/enoent")
				Ω(err).Should(HaveOccurred())
			})
		})
	})

	Describe("Service Metadata Retrieval", func() {
		Context("with a valid service.yml file", func() {
			It("can read the service metadata", func() {
				s, err := ReadService("test/ok/redis")
				Ω(err).ShouldNot(HaveOccurred())
				Ω(s).ShouldNot(BeNil())
				Ω(s.ID).Should(Equal("redis-1"))
				Ω(s.Name).Should(Equal("redis"))
				Ω(s.Description).Should(Equal("something awesome"))
				Ω(s.Bindable).Should(BeTrue())

				Ω(len(s.Tags)).Should(Equal(2))
				Ω(s.Tags[0]).Should(Equal("foo"))
				Ω(s.Tags[1]).Should(Equal("bar"))

				Ω(len(s.Plans)).Should(Equal(3))
				// FIXME: test the contents of the Plans
			})
		})

		Context("without a service.yml file", func() {
			It("throws an error", func() {
				_, err := ReadPlan("test/bad/empty")
				Ω(err).Should(HaveOccurred())
			})
		})

		Context("without a service directory", func() {
			It("throws an error", func() {
				_, err := ReadPlan("test/bad/absent")
				Ω(err).Should(HaveOccurred())
			})
		})

		Context("with a malformed child plan", func() {
			It("throws an error", func() {
				_, err := ReadPlan("test/bad/missing")
				Ω(err).Should(HaveOccurred())
			})
		})
	})

	Describe("Catalog Metadata Retrieval", func() {
		Context("with valid service paths", func() {
			It("can read all service/plan metadata", func() {
				ss, err := ReadServices("test/ok", "test/also/ok")
				Ω(err).ShouldNot(HaveOccurred())
				Ω(len(ss)).Should(Equal(3))
			})

			It("converts to brokerapi.* objects", func() {
				ss, err := ReadServices("test/ok", "test/also/ok")
				Ω(err).ShouldNot(HaveOccurred())

				catalog := Catalog(ss)
				Ω(catalog).ShouldNot(BeNil())
				Ω(len(catalog)).Should(Equal(3))
			})
		})

		Context("with invalid search paths", func() {
			It("throws an error", func() {
				_, err := ReadServices("test/ok", "test/enoent")
				Ω(err).Should(HaveOccurred())
			})
		})

		Context("with no search paths", func() {
			It("throws an error", func() {
				_, err := ReadServices()
				Ω(err).Should(HaveOccurred())
			})
		})
	})
})
