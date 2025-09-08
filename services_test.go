package main

import (
	"fmt"

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

				Ω(s.Tags).Should(HaveLen(2))
				Ω(s.Tags[0]).Should(Equal("foo"))
				Ω(s.Tags[1]).Should(Equal("bar"))

				Ω(s.Plans).Should(HaveLen(3))
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
				Ω(ss).Should(HaveLen(3))
			})

			It("converts to brokerapi.* objects", func() {
				ss, err := ReadServices("test/ok", "test/also/ok")
				Ω(err).ShouldNot(HaveOccurred())

				catalog := Catalog(ss)
				Ω(catalog).ShouldNot(BeNil())
				Ω(catalog).Should(HaveLen(3))
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

	Describe("Service Limits", func() {
		GiveMeAPlan := func(serviceLimit, planLimit int) Plan {
			return Plan{
				ID:    "test-plan",
				Name:  "Test Plan",
				Limit: planLimit,
				Service: &Service{
					ID:    "test-service",
					Name:  "Test Service",
					Limit: serviceLimit,
				},
			}
		}
		GiveMeAnIndex := func(inPlan, outOfPlan, outOfService int) *VaultIndex {
			db := &VaultIndex{
				Data: make(map[string]interface{}),
			}

			for n := range inPlan {
				name := fmt.Sprintf("in-plan-%d", n)
				db.Data[name] = map[string]interface{}{
					"service_id": "test-service",
					"plan_id":    "test-plan",
				}
			}

			for n := range outOfPlan {
				name := fmt.Sprintf("out-of-plan-%d", n)
				db.Data[name] = map[string]interface{}{
					"service_id": "test-service",
					"plan_id":    "not-our-plan",
				}
			}

			for n := range outOfService {
				name := fmt.Sprintf("out-of-service-%d", n)
				db.Data[name] = map[string]interface{}{
					"service_id": "not-our-service",
					"plan_id":    "not-our-plan",
				}
			}

			return db
		}

		Context("with no limits", func() {
			It("can handle any number of existing instances without going over-limit", func() {
				plan := GiveMeAPlan(0, 0)
				for i := 0; i < 100; i = 100 {
					for j := 0; j < 100; j = 100 {
						for k := 0; k < 100; k = 100 {
							db := GiveMeAnIndex(i, j, k)
							Ω(plan.OverLimit(db)).Should(BeFalse())
						}
					}
				}
			})
		})

		Context("with only plan limits", func() {
			var plan Plan
			BeforeEach(func() {
				plan = GiveMeAPlan(0, 5)
			})

			It("can handle the zero case", func() {
				db := GiveMeAnIndex(0, 0, 0)
				Ω(plan.OverLimit(db)).Should(BeFalse())
			})

			It("is not over-limit when the number of plan-instances is less than the limit", func() {
				db := GiveMeAnIndex(2, 0, 0)
				Ω(plan.OverLimit(db)).Should(BeFalse())
			})

			It("is over-limit when the number of plan-instances equals the limit", func() {
				db := GiveMeAnIndex(5, 0, 0)
				Ω(plan.OverLimit(db)).Should(BeTrue())
			})

			It("is over-limit when the number of plan-instances exceeds the limit", func() {
				db := GiveMeAnIndex(6, 0, 0)
				Ω(plan.OverLimit(db)).Should(BeTrue())
			})

			It("is not over-limit when another plan exceeds the limit", func() {
				db := GiveMeAnIndex(2, 200, 0)
				Ω(plan.OverLimit(db)).Should(BeFalse())
			})

			It("is not over-limit when another service exceeds the limit", func() {
				db := GiveMeAnIndex(2, 0, 200)
				Ω(plan.OverLimit(db)).Should(BeFalse())
			})
		})

		Context("with only service limits", func() {
			var plan Plan
			BeforeEach(func() {
				plan = GiveMeAPlan(5, 0)
			})

			It("can handle the zero case", func() {
				db := GiveMeAnIndex(0, 0, 0)
				Ω(plan.OverLimit(db)).Should(BeFalse())
			})

			It("is not over-limit when the combined number of service-instances is less than the limit", func() {
				db := GiveMeAnIndex(2, 0, 0)
				Ω(plan.OverLimit(db)).Should(BeFalse())

				db = GiveMeAnIndex(0, 2, 0)
				Ω(plan.OverLimit(db)).Should(BeFalse())

				db = GiveMeAnIndex(2, 2, 0)
				Ω(plan.OverLimit(db)).Should(BeFalse())
			})

			It("is over-limit when the combined number of service-instances equals the limit", func() {
				db := GiveMeAnIndex(5, 0, 0)
				Ω(plan.OverLimit(db)).Should(BeTrue())

				db = GiveMeAnIndex(0, 5, 0)
				Ω(plan.OverLimit(db)).Should(BeTrue())

				db = GiveMeAnIndex(3, 2, 0)
				Ω(plan.OverLimit(db)).Should(BeTrue())
			})

			It("is over-limit when the combined number of service-instances exceeds the limit", func() {
				db := GiveMeAnIndex(6, 0, 0)
				Ω(plan.OverLimit(db)).Should(BeTrue())

				db = GiveMeAnIndex(0, 6, 0)
				Ω(plan.OverLimit(db)).Should(BeTrue())

				db = GiveMeAnIndex(3, 3, 0)
				Ω(plan.OverLimit(db)).Should(BeTrue())
			})

			It("is not over-limit when another service exceeds the limit", func() {
				db := GiveMeAnIndex(2, 0, 200)
				Ω(plan.OverLimit(db)).Should(BeFalse())
			})
		})

		Context("with service and plan limits", func() {
			var plan Plan
			BeforeEach(func() {
				plan = GiveMeAPlan(7, 5)
			})

			It("can handle the zero case", func() {
				db := GiveMeAnIndex(0, 0, 0)
				Ω(plan.OverLimit(db)).Should(BeFalse())
			})

			It("is not over-limit when the number of plan-instances is less than the plan-limit", func() {
				db := GiveMeAnIndex(2, 0, 0)
				Ω(plan.OverLimit(db)).Should(BeFalse())
			})

			It("is not over-limit when the number of service-instances is less than the service-limit", func() {
				db := GiveMeAnIndex(2, 4, 0)
				Ω(plan.OverLimit(db)).Should(BeFalse())
			})

			It("is over-limit when the number of plan-instances equals the plan-limit", func() {
				db := GiveMeAnIndex(5, 0, 0)
				Ω(plan.OverLimit(db)).Should(BeTrue())
			})

			It("is over-limit when the number of service-instances equals the service-limit", func() {
				db := GiveMeAnIndex(4, 3, 0)
				Ω(plan.OverLimit(db)).Should(BeTrue())
			})

			It("is over-limit when the number of plan-instances exceeds the plan-limit", func() {
				db := GiveMeAnIndex(6, 0, 0)
				Ω(plan.OverLimit(db)).Should(BeTrue())
			})

			It("is over-limit when the number of service-instances exceeds the service-limit", func() {
				db := GiveMeAnIndex(4, 4, 0)
				Ω(plan.OverLimit(db)).Should(BeTrue())
			})

			It("is not over-limit when another service exceeds the limit", func() {
				db := GiveMeAnIndex(2, 2, 200)
				Ω(plan.OverLimit(db)).Should(BeFalse())
			})
		})
	})
})
