package services_test

import (
	. "github.com/cloudfoundry-community/blacksmith/services"

	. "github.com/onsi/ginkgo"
	//	. "github.com/onsi/gomega"
)

var _ = Describe("Services", func() {
	Describe("Get service", func() {
		It("can single service", func() {
			GetService("test-fixtures/redis")
		})
	})
})
