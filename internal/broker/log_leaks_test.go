package broker_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"

	"blacksmith/internal/broker"
	"blacksmith/pkg/logger"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

const (
	leakSentinelPassword = "sentinel-password-do-not-log"     //nolint:gosec // fake credential the tests prove stays out of logs
	leakSentinelAuth     = "c2VudGluZWwtYXV0aC1kby1ub3QtbG9n" //nolint:gosec // fake credential the tests prove stays out of logs
)

// captureLogger records every message at every level, formatted the way the
// broker's printf-style logger formats them.
type captureLogger struct {
	*logger.NoOpLogger

	mu    sync.Mutex
	lines []string
}

func newCaptureLogger() *captureLogger {
	return &captureLogger{NoOpLogger: &logger.NoOpLogger{}}
}

func (c *captureLogger) record(msg string, args []interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.lines = append(c.lines, fmt.Sprintf(msg, args...))
}

func (c *captureLogger) Debug(msg string, args ...interface{}) { c.record(msg, args) }
func (c *captureLogger) Info(msg string, args ...interface{})  { c.record(msg, args) }
func (c *captureLogger) Warn(msg string, args ...interface{})  { c.record(msg, args) }
func (c *captureLogger) Error(msg string, args ...interface{}) { c.record(msg, args) }

func (c *captureLogger) Named(string) logger.Logger { return c }

func (c *captureLogger) output() string {
	c.mu.Lock()
	defer c.mu.Unlock()

	return strings.Join(c.lines, "\n")
}

var _ = Describe("Log leak prevention", func() {
	var (
		capture  *captureLogger
		previous logger.Logger
	)

	BeforeEach(func() {
		capture = newCaptureLogger()
		previous = logger.Get()

		logger.Set(capture)
	})

	AfterEach(func() {
		logger.Set(previous)
	})

	It("logs credential key names for a binding and never their values", func() {
		broker.LogBindingCredentialKeys(capture, "binding-1", map[string]interface{}{
			"username": "acl-user",
			"password": leakSentinelPassword,
		})

		Expect(capture.output()).To(ContainSubstring("password, username"))
		Expect(capture.output()).NotTo(ContainSubstring(leakSentinelPassword))
		Expect(capture.output()).NotTo(ContainSubstring("acl-user"))
	})

	It("logs only sizes when a YAML file fails to parse", func() {
		err := broker.WriteYamlFile("inst-1", []byte("password: "+leakSentinelPassword+"\n  bad: [unclosed\n"))

		Expect(err).To(HaveOccurred())
		Expect(capture.output()).To(ContainSubstring("Failed to unmarshal"))
		Expect(capture.output()).NotTo(ContainSubstring(leakSentinelPassword))
		Expect(err.Error()).NotTo(ContainSubstring(leakSentinelPassword))
	})

	It("does not log the RabbitMQ password in the user creation payload", func() {
		data, err := broker.PrepareUserCreationPayload(leakSentinelPassword, capture)

		Expect(err).NotTo(HaveOccurred())
		Expect(string(data)).To(ContainSubstring(leakSentinelPassword))
		Expect(capture.output()).NotTo(BeEmpty())
		Expect(capture.output()).NotTo(ContainSubstring(leakSentinelPassword))
	})

	It("redacts the Authorization header in the request header debug log", func() {
		api := broker.API{Logger: capture}
		req := httptest.NewRequest(http.MethodGet, "/v2/catalog", nil)
		req.Header.Set("Authorization", "Basic "+leakSentinelAuth)
		req.Header.Set("X-Broker-API-Version", "2.17")

		api.ServeHTTP(httptest.NewRecorder(), req)

		Expect(capture.output()).To(ContainSubstring("X-Broker-Api-Version"))
		Expect(capture.output()).NotTo(ContainSubstring(leakSentinelAuth))
	})
})
