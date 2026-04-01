package broker_test

import (
	"blacksmith/internal/broker"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var _ = Describe("Valkey ACL Credentials", func() {
	Describe("isValkeyService", func() {
		It("returns true for maps with service_type valkey", func() {
			credMap := map[string]interface{}{
				"host":           "10.0.0.5",
				"port":           6379,
				"password":       "secret",
				"admin_password": "secret",
				"username":       "default",
				"service_type":   "valkey",
			}
			Expect(broker.IsValkeyService(credMap)).To(BeTrue())
		})

		It("returns false for maps without service_type", func() {
			credMap := map[string]interface{}{
				"host":     "10.0.0.5",
				"port":     6379,
				"password": "secret",
			}
			Expect(broker.IsValkeyService(credMap)).To(BeFalse())
		})

		It("returns false for non-valkey service_type", func() {
			credMap := map[string]interface{}{
				"host":         "10.0.0.5",
				"port":         5672,
				"service_type": "rabbitmq",
			}
			Expect(broker.IsValkeyService(credMap)).To(BeFalse())
		})

		It("returns false for non-string service_type", func() {
			credMap := map[string]interface{}{
				"service_type": 42,
			}
			Expect(broker.IsValkeyService(credMap)).To(BeFalse())
		})
	})

	Describe("extractHosts", func() {
		It("returns nil for standalone credentials without hosts", func() {
			credMap := map[string]interface{}{
				"host": "10.0.0.5",
				"port": 6379,
			}
			Expect(broker.ExtractHosts(credMap)).To(BeNil())
		})

		It("returns host list for cluster credentials", func() {
			credMap := map[string]interface{}{
				"host":  "10.0.0.5",
				"hosts": []interface{}{"10.0.0.5", "10.0.0.6", "10.0.0.7"},
				"port":  6379,
			}
			Expect(broker.ExtractHosts(credMap)).To(Equal([]string{"10.0.0.5", "10.0.0.6", "10.0.0.7"}))
		})

		It("returns nil for empty hosts list", func() {
			credMap := map[string]interface{}{
				"host":  "10.0.0.5",
				"hosts": []interface{}{},
			}
			Expect(broker.ExtractHosts(credMap)).To(BeNil())
		})
	})
})
