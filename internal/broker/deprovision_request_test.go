package broker_test

import (
	"blacksmith/internal/broker"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var _ = Describe("Withdrawing a refused deprovision request from the index", func() {
	const (
		instanceID  = "instance-1"
		requested   = "deprovision_requested"
		requestAt   = "2026-10-07T10:00:00Z"
		other       = "other"
		plan1       = "plan-1"
		plan2       = "plan-2"
		planIDKey   = "plan_id"
		statusKey   = "status"
		provisioned = "provisioned"
		deployment1 = "plan-1-instance-1"
	)

	var fields map[string]interface{}

	BeforeEach(func() {
		fields = map[string]interface{}{
			planIDKey:                 plan1,
			statusKey:                 requested,
			deprovisionRequestedAtKey: requestAt,
		}
	})

	It("puts back the fields the request changed and removes the ones it added", func() {
		previous := map[string]interface{}{planIDKey: plan1, statusKey: provisioned, deploymentNameKey: deployment1}
		entries := map[string]interface{}{instanceID: map[string]interface{}{
			planIDKey: plan1, statusKey: requested, deprovisionRequestedAtKey: requestAt, deploymentNameKey: deployment1,
		}}

		Expect(broker.WithdrawIndexFields(entries, instanceID, fields, previous, true)).To(BeTrue())
		Expect(entries[instanceID]).To(Equal(previous))
	})

	It("keeps a field another writer changed since the request", func() {
		previous := map[string]interface{}{planIDKey: plan1, statusKey: provisioned}
		entries := map[string]interface{}{instanceID: map[string]interface{}{
			planIDKey: plan1, statusKey: indexStatusDeleted, deprovisionRequestedAtKey: requestAt,
		}}

		Expect(broker.WithdrawIndexFields(entries, instanceID, fields, previous, true)).To(BeTrue())
		Expect(entries[instanceID]).To(Equal(map[string]interface{}{planIDKey: plan1, statusKey: indexStatusDeleted}))
	})

	It("keeps an entry that existed before, even when it holds only the request's fields", func() {
		previous := map[string]interface{}{planIDKey: plan1, statusKey: provisioned}
		entries := map[string]interface{}{instanceID: map[string]interface{}{
			planIDKey: plan1, statusKey: requested, deprovisionRequestedAtKey: requestAt,
		}}

		Expect(broker.WithdrawIndexFields(entries, instanceID, fields, previous, true)).To(BeTrue())
		Expect(entries[instanceID]).To(Equal(previous))
	})

	It("removes an entry the request created that holds nothing else", func() {
		entries := map[string]interface{}{
			instanceID: map[string]interface{}{planIDKey: plan1, statusKey: requested, deprovisionRequestedAtKey: requestAt},
			other:      map[string]interface{}{planIDKey: plan2},
		}

		Expect(broker.WithdrawIndexFields(entries, instanceID, fields, map[string]interface{}{}, false)).To(BeTrue())
		Expect(entries).ToNot(HaveKey(instanceID))
		Expect(entries).To(HaveKey(other))
	})

	It("keeps an entry the request created once another writer added to it", func() {
		entries := map[string]interface{}{instanceID: map[string]interface{}{
			planIDKey: plan1, statusKey: requested, deprovisionRequestedAtKey: requestAt, deletedAtKey: requestAt,
		}}

		Expect(broker.WithdrawIndexFields(entries, instanceID, fields, map[string]interface{}{}, false)).To(BeTrue())
		Expect(entries[instanceID]).To(Equal(map[string]interface{}{deletedAtKey: requestAt}))
	})

	It("keeps an entry the request created once another writer changed one of its fields", func() {
		entries := map[string]interface{}{instanceID: map[string]interface{}{
			planIDKey: plan1, statusKey: indexStatusDeleted, deprovisionRequestedAtKey: requestAt,
		}}

		Expect(broker.WithdrawIndexFields(entries, instanceID, fields, map[string]interface{}{}, false)).To(BeTrue())
		Expect(entries[instanceID]).To(Equal(map[string]interface{}{statusKey: indexStatusDeleted}))
	})

	It("changes nothing when the entry is gone", func() {
		entries := map[string]interface{}{other: map[string]interface{}{planIDKey: plan2}}

		Expect(broker.WithdrawIndexFields(entries, instanceID, fields, map[string]interface{}{}, true)).To(BeFalse())
		Expect(entries).To(Equal(map[string]interface{}{other: map[string]interface{}{planIDKey: plan2}}))
	})

	It("changes nothing when every field was changed since the request", func() {
		entries := map[string]interface{}{instanceID: map[string]interface{}{
			planIDKey: "plan-3", statusKey: indexStatusDeleted, deprovisionRequestedAtKey: "2026-10-07T11:00:00Z",
		}}

		Expect(broker.WithdrawIndexFields(entries, instanceID, fields, map[string]interface{}{}, true)).To(BeFalse())
	})
})
