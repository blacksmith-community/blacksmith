package cf

import (
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"blacksmith/internal/apierr"
	"blacksmith/pkg/logger"
	cfservices "blacksmith/pkg/services/cf"
)

// Values that must never reach a log line or the answer.
const (
	connUser     = "conn-admin-q4"
	connPassword = "conn-pw-never-logged"
	connToken    = "conn-token-never-shown"
)

// captureLogger keeps every line, whatever its level.
type captureLogger struct {
	*logger.NoOpLogger

	mu    sync.Mutex
	lines []string
}

func newCaptureLogger() *captureLogger {
	return &captureLogger{NoOpLogger: &logger.NoOpLogger{}}
}

// record keeps the format and every argument, so an argument the format has
// no verb for is kept as well.
func (c *captureLogger) record(msg string, args []interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.lines = append(c.lines, fmt.Sprintf(msg, args...)+" "+fmt.Sprint(args...))
}

func (c *captureLogger) Debug(msg string, args ...interface{})    { c.record(msg, args) }
func (c *captureLogger) Info(msg string, args ...interface{})     { c.record(msg, args) }
func (c *captureLogger) Warn(msg string, args ...interface{})     { c.record(msg, args) }
func (c *captureLogger) Warning(msg string, args ...interface{})  { c.record(msg, args) }
func (c *captureLogger) Error(msg string, args ...interface{})    { c.record(msg, args) }
func (c *captureLogger) Debugf(msg string, args ...interface{})   { c.record(msg, args) }
func (c *captureLogger) Infof(msg string, args ...interface{})    { c.record(msg, args) }
func (c *captureLogger) Warnf(msg string, args ...interface{})    { c.record(msg, args) }
func (c *captureLogger) Warningf(msg string, args ...interface{}) { c.record(msg, args) }
func (c *captureLogger) Errorf(msg string, args ...interface{})   { c.record(msg, args) }

func (c *captureLogger) Named(string) logger.Logger { return c }

func (c *captureLogger) output() string {
	c.mu.Lock()
	defer c.mu.Unlock()

	return strings.Join(c.lines, "\n")
}

// newConnFakeCF starts a token endpoint and an API root that advertises it.
// The token endpoint answers connPassword with connToken and refuses anything
// else with a body that quotes the request.
func newConnFakeCF(t *testing.T) *httptest.Server {
	t.Helper()

	uaa := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, req *http.Request) {
		_ = req.ParseForm()
		if req.PostForm.Get("username") != connUser || req.PostForm.Get("password") != connPassword {
			writer.WriteHeader(http.StatusUnauthorized)
			_, _ = fmt.Fprintf(writer, `{"error":"unauthorized","error_description":"%s/%s"}`,
				req.PostForm.Get("username"), req.PostForm.Get("password"))

			return
		}

		_, _ = fmt.Fprintf(writer, `{"access_token":%q,"token_type":"bearer"}`, connToken)
	}))
	t.Cleanup(uaa.Close)

	var api *httptest.Server

	api = httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(writer, `{"links":{"self":{"href":%q},"cloud_controller_v3":{"href":%q},"uaa":{"href":%q}}}`,
			api.URL, api.URL+"/v3", uaa.URL)
	}))
	t.Cleanup(api.Close)

	return api
}

// newConnHandler builds a handler that trusts pool, or the system roots when
// pool is nil.
func newConnHandler(log logger.Logger, pool *x509.CertPool) *Handler {
	handler := NewHandler(log, nil, nil, nil)
	handler.connectionTester.RootCAs = pool

	return handler
}

func trustServer(server *httptest.Server) *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AddCert(server.Certificate())

	return pool
}

type connAnswer struct {
	Success bool                         `json:"success"`
	Message string                       `json:"message"`
	Error   string                       `json:"error"`
	Code    string                       `json:"code"`
	Checks  []cfservices.ConnectionCheck `json:"checks"`
}

func postConnectionTest(t *testing.T, handler *Handler, body string) (*httptest.ResponseRecorder, connAnswer) {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, "/b/cf/test-connection", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)

	var answer connAnswer

	err := json.Unmarshal(recorder.Body.Bytes(), &answer)
	if err != nil {
		t.Fatalf("answer is not JSON: %v: %s", err, recorder.Body.String())
	}

	return recorder, answer
}

func connBody(apiURL string, extra string) string {
	return fmt.Sprintf(`{"api_url":%q,"username":%q,"password":%q%s}`, apiURL, connUser, connPassword, extra)
}

func TestConnectionTestRouteAnswersEveryCheck(t *testing.T) {
	t.Parallel()

	api := newConnFakeCF(t)
	recorder, answer := postConnectionTest(t, newConnHandler(newCaptureLogger(), trustServer(api)), connBody(api.URL, ""))

	if recorder.Code != http.StatusOK || !answer.Success || answer.Code != "" || answer.Error != "" {
		t.Fatalf("answer = %d %+v, want 200 and success", recorder.Code, answer)
	}

	if got := recorder.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q", got)
	}

	names := []string{}
	for _, check := range answer.Checks {
		if !check.Passed {
			t.Fatalf("check %s did not pass: %+v", check.Name, check)
		}

		names = append(names, check.Name)
	}

	if strings.Join(names, ",") != "reachable,tls,cloud_foundry,token" {
		t.Fatalf("checks = %v", names)
	}
}

func TestConnectionTestRouteReportsAFailedCheckWithItsCode(t *testing.T) {
	t.Parallel()

	api := newConnFakeCF(t)
	// No extra roots, so the httptest certificate is untrusted.
	recorder, answer := postConnectionTest(t, newConnHandler(newCaptureLogger(), nil), connBody(api.URL, ""))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for a failed check", recorder.Code)
	}

	if answer.Success || answer.Code != apierr.CodeCFTLSFailed || answer.Error == "" {
		t.Fatalf("answer = %+v, want a TLS failure", answer)
	}

	if len(answer.Checks) != 4 || answer.Checks[1].Name != "tls" || answer.Checks[1].Passed {
		t.Fatalf("checks = %+v", answer.Checks)
	}
}

// Registration offers no way to skip certificate verification, so a request
// that asks for it is checked exactly like one that does not.
func TestConnectionTestRouteIgnoresASkipVerificationRequest(t *testing.T) {
	t.Parallel()

	api := newConnFakeCF(t)

	for _, extra := range []string{"", `,"skip_ssl_validation":true`, `,"skip_tls_verify":true,"insecure":true`} {
		_, answer := postConnectionTest(t, newConnHandler(newCaptureLogger(), nil), connBody(api.URL, extra))

		if answer.Success || answer.Code != apierr.CodeCFTLSFailed {
			t.Fatalf("body extra %q: answer = %+v, want a TLS failure", extra, answer)
		}
	}
}

func TestConnectionTestRouteMapsEachFailureToACode(t *testing.T) {
	t.Parallel()

	api := newConnFakeCF(t)
	closed := httptest.NewTLSServer(http.NotFoundHandler())
	closedURL := closed.URL
	closed.Close()

	notCF := httptest.NewTLSServer(http.NotFoundHandler())
	t.Cleanup(notCF.Close)

	cases := map[string]struct {
		body string
		want string
	}{
		"unreachable": {body: connBody(closedURL, ""), want: apierr.CodeCFUnreachable},
		"not cf":      {body: connBody(notCF.URL, ""), want: apierr.CodeCFNotCloudFoundry},
		"refused": {
			body: fmt.Sprintf(`{"api_url":%q,"username":%q,"password":"wrong-pw"}`, api.URL, connUser),
			want: apierr.CodeCFAuthFailed,
		},
	}

	for name, testCase := range cases {
		_, answer := postConnectionTest(t, newConnHandler(newCaptureLogger(), trustServer(api)), testCase.body)

		if answer.Success || answer.Code != testCase.want {
			t.Fatalf("%s: answer = %+v, want code %s", name, answer, testCase.want)
		}
	}
}

func TestConnectionTestRouteRejectsInvalidFields(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"not json":     `{"api_url":`,
		"no url":       `{"username":"admin","password":"secret"}`,
		"no username":  `{"api_url":"https://api.example.com","password":"secret"}`,
		"no password":  `{"api_url":"https://api.example.com","username":"admin"}`,
		"bad scheme":   `{"api_url":"api.example.com","username":"admin","password":"secret"}`,
		"has userinfo": `{"api_url":"https://admin:secret@api.example.com","username":"admin","password":"secret"}`,
		"wrong type":   `{"api_url":"https://api.example.com","username":"admin","password":12345678}`,
	}

	for name, body := range cases {
		recorder, answer := postConnectionTest(t, newConnHandler(newCaptureLogger(), nil), body)

		if recorder.Code != http.StatusBadRequest || answer.Code != apierr.CodeInvalidRequest || answer.Error == "" {
			t.Fatalf("%s: answer = %d %+v, want 400 invalid_request", name, recorder.Code, answer)
		}

		if strings.Contains(answer.Error, "secret") || strings.Contains(answer.Error, "12345678") {
			t.Fatalf("%s: error repeats the request: %q", name, answer.Error)
		}
	}
}

func TestConnectionTestRouteOnlyAnswersPost(t *testing.T) {
	t.Parallel()

	handler := newConnHandler(newCaptureLogger(), nil)
	req := httptest.NewRequest(http.MethodGet, "/b/cf/test-connection", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("GET status = %d, want 404", recorder.Code)
	}
}

func TestConnectionTestRouteLimitsTheRequestBody(t *testing.T) {
	t.Parallel()

	body := `{"api_url":"https://api.example.com","username":"admin","password":"` + strings.Repeat("x", 128<<10) + `"}`
	recorder, answer := postConnectionTest(t, newConnHandler(newCaptureLogger(), nil), body)

	if recorder.Code != http.StatusBadRequest || answer.Code != apierr.CodeInvalidRequest {
		t.Fatalf("answer = %d %+v, want 400 invalid_request", recorder.Code, answer)
	}
}

func TestConnectionTestRouteNeverLogsOrAnswersTheCredentials(t *testing.T) {
	t.Parallel()

	api := newConnFakeCF(t)
	capture := newCaptureLogger()

	bodies := []string{
		connBody(api.URL, ""), // passes, so the token is issued
		fmt.Sprintf(`{"api_url":%q,"username":%q,"password":"%s-x"}`, api.URL, connUser, connPassword), // refused, and the refusal quotes it
	}

	answers := []string{}

	for _, body := range bodies {
		for _, pool := range []*x509.CertPool{trustServer(api), nil} {
			recorder, _ := postConnectionTest(t, newConnHandler(capture, pool), body)
			answers = append(answers, recorder.Body.String())
		}
	}

	logs := capture.output()
	if !strings.Contains(logs, "test-connection") && !strings.Contains(logs, "connection test") {
		t.Fatalf("the route logged nothing, so the check proves nothing: %q", logs)
	}

	for _, secret := range []string{connUser, connPassword, connToken} {
		if strings.Contains(logs, secret) {
			t.Fatalf("logs contain %q:\n%s", secret, logs)
		}

		for _, answer := range answers {
			if strings.Contains(answer, secret) {
				t.Fatalf("answer contains %q: %s", secret, answer)
			}
		}
	}
}

func TestConnectionTestHandlerUsesTheDefaultLimit(t *testing.T) {
	t.Parallel()

	handler := NewHandler(newCaptureLogger(), nil, nil, nil)
	if handler.connectionTester == nil || handler.connectionTester.Timeout != cfservices.ConnectionTestTimeout {
		t.Fatalf("connection tester = %+v, want the %s limit", handler.connectionTester, cfservices.ConnectionTestTimeout)
	}
}
