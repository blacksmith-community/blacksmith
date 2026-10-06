package broker_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"

	"blacksmith/internal/broker"
	"blacksmith/internal/config"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

// Unbind treats a RabbitMQ user that is already gone as deleted. Only a 404
// answer means that. Another failure whose text happens to contain "404" is
// still a failed delete.
var _ = Describe("Deleting a binding's RabbitMQ user", func() {
	var (
		server   *httptest.Server
		status   int
		body     string
		requests atomic.Int32
		b        *broker.Broker
	)

	BeforeEach(func() {
		requests.Store(0)

		server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			requests.Add(1)
			writer.WriteHeader(status)
			_, _ = writer.Write([]byte(body))
		}))

		b = &broker.Broker{Config: &config.Config{}}
	})

	AfterEach(func() {
		server.Close()
	})

	deleteUser := func() error {
		serverURL, err := url.Parse(server.URL)
		Expect(err).ToNot(HaveOccurred())

		return b.DeleteRabbitMQUser(context.Background(), "binding-1", map[string]interface{}{
			"admin_username": "admin",
			"admin_password": "admin-pass",
			"api_url":        server.URL + "/api",
			"hostname":       serverURL.Host,
		})
	}

	It("keeps a failure whose text contains 404 as an error", func() {
		status = http.StatusForbidden
		body = `{"error":"not_authorised","reason":"policy 404 denies user deletion"}`

		err := deleteUser()
		Expect(err).To(HaveOccurred(), "a 403 answer was taken for a user that is already gone")
		Expect(err.Error()).To(ContainSubstring("403"))
		Expect(requests.Load()).To(BeNumerically(">", 1), "the delete was not retried")
	})

	It("takes a 404 answer as a user that is already gone", func() {
		status = http.StatusNotFound
		body = `{"error":"Object Not Found","reason":"Not Found"}`

		Expect(deleteUser()).To(Succeed())
		Expect(requests.Load()).To(Equal(int32(1)))
	})
})
