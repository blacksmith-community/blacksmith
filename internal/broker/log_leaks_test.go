package broker_test

import (
	"context"
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

	It("keeps the generated Valkey binding password out of the log while completing a bind", func() {
		fake := newFakeValkey(nil)
		defer fake.close()

		creds, err := (&broker.Broker{}).CompleteBind(context.Background(), valkeyTestInstanceID, "binding-leak-1", valkeyCredMap(fake.port()), capture)

		Expect(err).NotTo(HaveOccurred())

		password, ok := creds["password"].(string)
		Expect(ok).To(BeTrue())
		Expect(password).NotTo(BeEmpty())
		Expect(creds).NotTo(HaveKey("admin_password"))

		Expect(capture.output()).To(ContainSubstring("binding-leak-1"))
		Expect(capture.output()).NotTo(ContainSubstring(password))
		Expect(capture.output()).NotTo(ContainSubstring(valkeyTestAdminPassword))
	})

	It("logs only sizes when a YAML file fails to parse", func() {
		err := broker.WriteYamlFile("inst-1", []byte("password: "+leakSentinelPassword+"\n  bad: [unclosed\n"))

		Expect(err).To(HaveOccurred())
		Expect(capture.output()).To(ContainSubstring("Failed to unmarshal"))
		Expect(capture.output()).NotTo(ContainSubstring(leakSentinelPassword))
		Expect(err.Error()).NotTo(ContainSubstring(leakSentinelPassword))
	})

	It("keeps the value fragment of a YAML type error out of the log and the error", func() {
		err := broker.WriteYamlFile("inst-1", []byte("hunter2-not-a-mapping"))

		Expect(err).To(HaveOccurred())
		Expect(err).To(MatchError(broker.ErrUnmarshalYAMLData))
		Expect(capture.output()).To(ContainSubstring("Failed to unmarshal"))
		Expect(capture.output()).NotTo(ContainSubstring("hunter2"))
		Expect(err.Error()).NotTo(ContainSubstring("hunter2"))
	})

	It("does not log the RabbitMQ password in the user creation payload", func() {
		data, err := broker.PrepareUserCreationPayload(leakSentinelPassword, capture)

		Expect(err).NotTo(HaveOccurred())
		Expect(string(data)).To(ContainSubstring(leakSentinelPassword))
		Expect(capture.output()).NotTo(BeEmpty())
		Expect(capture.output()).NotTo(ContainSubstring(leakSentinelPassword))
	})

	for _, header := range []struct {
		label, name, value string
		canonical          bool
	}{
		{"Authorization", "Authorization", "Basic " + leakSentinelAuth, true},
		{"Cookie", "Cookie", "session=" + leakSentinelAuth, true},
		{"Set-Cookie", "Set-Cookie", "session=" + leakSentinelAuth, true},
		{"Proxy-Authorization", "Proxy-Authorization", "Basic " + leakSentinelAuth, true},
		{"X-Vault-Token", "X-Vault-Token", leakSentinelAuth, true},
		{"lowercase x-vault-token", "x-vault-token", leakSentinelAuth, false},
		{"lowercase authorization", "authorization", "Basic " + leakSentinelAuth, false},
	} {
		header := header

		It("redacts the "+header.label+" header in the request debug log", func() {
			api := broker.API{Logger: capture}
			req := httptest.NewRequest(http.MethodGet, "/v2/catalog", nil)

			if header.canonical {
				req.Header.Set(header.name, header.value)
			} else {
				req.Header[header.name] = []string{header.value}
			}

			req.Header.Set("X-Broker-API-Version", "2.17")

			api.ServeHTTP(httptest.NewRecorder(), req)

			Expect(capture.output()).To(ContainSubstring("X-Broker-Api-Version"))
			Expect(capture.output()).NotTo(ContainSubstring(header.value))
		})
	}

	for _, path := range []string{"/b/rabbitmq/test", "/v2/catalog"} {
		path := path

		It("redacts credential query parameters in the debug log for "+path, func() {
			api := broker.API{
				Logger: capture, Username: "broker", Password: "broker-pw",
				Internal: broker.NullHandler{}, Primary: broker.NullHandler{},
			}
			req := httptest.NewRequest(http.MethodGet, path+"?connection_password="+leakSentinelPassword+
				"&API_Token="+leakSentinelPassword+"&client_secret="+leakSentinelPassword+"&ssh%5Fkey="+leakSentinelPassword+"&operation=test", nil)
			req.SetBasicAuth("broker", "broker-pw")

			api.ServeHTTP(httptest.NewRecorder(), req)

			Expect(capture.output()).To(ContainSubstring("connection_password=<redacted>"))
			Expect(capture.output()).To(ContainSubstring("operation=test"))
			Expect(capture.output()).NotTo(ContainSubstring(leakSentinelPassword))
		})
	}

	It("masks key parameters by whole word or known suffix and leaves keyword visible", func() {
		for _, masked := range []string{"api_key", "API_KEY", "apiKey", "apikey", "ssh-key", "key", "access_key", "private.key"} {
			Expect(broker.RedactedQuery(masked+"=v")).To(Equal(masked+"=<redacted>"), masked)
		}

		for _, visible := range []string{"keyword", "keywords", "monkeys", "keyboard", "hockey", "turkey"} {
			Expect(broker.RedactedQuery(visible+"=v")).To(Equal(visible+"=v"), visible)
		}

		Expect(broker.RedactedQuery("keyword=a&api_key=b&q=c")).To(Equal("keyword=a&api_key=<redacted>&q=c"))
	})

	It("leaves query strings without credential names alone", func() {
		Expect(broker.RedactedQuery("a=1&b=2")).To(Equal("a=1&b=2"))
		Expect(broker.RedactedQuery("")).To(Equal(""))
	})
})
