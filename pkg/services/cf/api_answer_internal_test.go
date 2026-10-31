package cf

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// answerMarker is in every answer below that a message must not quote.
const answerMarker = "424242424242"

// recordingLogger keeps what the client logs.
type recordingLogger struct {
	mu    sync.Mutex
	lines []string
}

func (l *recordingLogger) Warnf(format string, args ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *recordingLogger) text() string {
	l.mu.Lock()
	defer l.mu.Unlock()

	return strings.Join(l.lines, "\n")
}

// brokerList is a broker list of about size bytes whose last broker is
// named "blacksmith".
func brokerList(size int) string {
	const entry = `{"guid":"guid-x","name":"other-broker","url":"https://other.example.com","state":"available"},`

	return `{"resources":[` + strings.Repeat(entry, size/len(entry)) + `{"guid":"guid-blacksmith","name":"blacksmith"}]}`
}

func serveAnswer(answer string) http.HandlerFunc {
	return func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(answer))
	}
}

// A broker list bigger than the transport buffers is read only after the
// request returns, so it must outlive the request's own deadline.
func TestAuthenticatedRequestsReadALargeBrokerList(t *testing.T) {
	t.Parallel()

	foundation := newV3OnlyFoundation(t)
	foundation.brokersAnswer = serveAnswer(brokerList(2 << 20))

	broker, err := brokerClient(foundation).FindServiceBroker("blacksmith")
	if err != nil || broker == nil || broker.ID != "guid-blacksmith" {
		t.Fatalf("FindServiceBroker() over a 2 MiB list = %+v, %v, want guid-blacksmith", broker, err)
	}
}

func TestAuthenticatedRequestsRefuseAnAnswerOverTheLimit(t *testing.T) {
	t.Parallel()

	foundation := newV3OnlyFoundation(t)
	foundation.brokersAnswer = serveAnswer(brokerList(maxAPIAnswerBytes + 1024))

	_, err := brokerClient(foundation).GetServiceBrokers()
	if !errors.Is(err, ErrAPIAnswerTooLarge) {
		t.Fatalf("GetServiceBrokers() over the limit = %v, want ErrAPIAnswerTooLarge", err)
	}
}

// Neither an error nor a log line repeats the API's answer or where the
// broker connected, whether the answer is the wrong JSON, cut short, or an
// error the API explains in its own words.
func TestCFClientErrorsDoNotQuoteTheAnswer(t *testing.T) {
	t.Parallel()

	cut := func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Length", "100000")
		_, _ = writer.Write([]byte(`{"resources":[{"guid":"` + answerMarker + ``))

		if flusher, ok := writer.(http.Flusher); ok {
			flusher.Flush()
		}

		conn, _, err := writer.(http.Hijacker).Hijack()
		if err == nil {
			_ = conn.Close()
		}
	}

	refusal := func(status int, body string) http.HandlerFunc {
		return func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(status)
			_, _ = writer.Write([]byte(body))
		}
	}

	cases := []struct {
		name   string
		answer http.HandlerFunc
		call   func(*Client) error
		want   string
	}{
		{"a list of the wrong JSON", serveAnswer(`{"resources":[{"guid":` + answerMarker + `}]}`),
			func(c *Client) error { _, err := c.GetServiceBrokers(); return err }, "failed to decode brokers response"},
		{"a list that is not JSON", serveAnswer(`<html>` + answerMarker + `</html>`),
			func(c *Client) error { _, err := c.GetServiceBrokers(); return err }, "failed to decode brokers response"},
		{"a list cut short", cut,
			func(c *Client) error { _, err := c.GetServiceBrokers(); return err }, "request failed"},
		{"a created broker of the wrong JSON", refusal(http.StatusCreated, `{"guid":`+answerMarker+`}`),
			func(c *Client) error { _, err := c.CreateServiceBroker("blacksmith"); return err }, "failed to decode broker creation response"},
		{"a creation refused in the API's words", refusal(http.StatusUnprocessableEntity,
			`{"errors":[{"title":"CF-UnprocessableEntity","code":10008,"detail":"broker-pw-7d1f is not allowed"}]}`),
			func(c *Client) error { _, err := c.CreateServiceBroker("blacksmith"); return err }, "CF-UnprocessableEntity (10008)"},
		{"a creation refused with a code too big to read", refusal(http.StatusUnprocessableEntity,
			`{"errors":[{"title":"CF-UnprocessableEntity","code":`+answerMarker+answerMarker+answerMarker+`}]}`),
			func(c *Client) error { _, err := c.CreateServiceBroker("blacksmith"); return err }, "(422)"},
		{"a creation refused without JSON", refusal(http.StatusBadGateway, `<html>`+answerMarker+`</html>`),
			func(c *Client) error { _, err := c.CreateServiceBroker("blacksmith"); return err }, "(502)"},
		{"an update refused in the API's words", refusal(http.StatusUnprocessableEntity,
			`{"errors":[{"title":"CF-ServiceBrokerNameTaken","code":270002,"detail":"Name must be unique"}]}`),
			func(c *Client) error { _, err := c.UpdateServiceBroker("guid-1", "blacksmith"); return err }, "CF-ServiceBrokerNameTaken (270002)"},
		{"an update refused with a title that is free text", refusal(http.StatusUnprocessableEntity,
			`{"errors":[{"title":"`+answerMarker+` says no","code":1}]}`),
			func(c *Client) error { _, err := c.UpdateServiceBroker("guid-1", "blacksmith"); return err }, "(422)"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			foundation := newV3OnlyFoundation(t)
			foundation.brokersAnswer = tc.answer
			logs := &recordingLogger{}
			client := brokerClient(foundation)
			client.logger = logs

			err := tc.call(client)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("the call = %v, want an error containing %q", err, tc.want)
			}

			for where, text := range map[string]string{"error": err.Error(), "log": logs.text()} {
				for _, quoted := range []string{answerMarker, brokerPassword, "127.0.0.1", foundation.Listener.Addr().String()} {
					if strings.Contains(text, quoted) {
						t.Errorf("the %s repeats %q: %s", where, quoted, text)
					}
				}
			}
		})
	}
}

// A refusal's detail is the reason an operator can act on, so it is shown,
// but only on one line, at most refusalDetailLimit characters, and with
// every secret the client holds replaced, in case CF echoes the request.
func TestCFRefusalsShowASafeDetail(t *testing.T) {
	t.Parallel()

	refuse := func(detail string) http.HandlerFunc {
		return func(writer http.ResponseWriter, _ *http.Request) {
			answer, _ := json.Marshal(map[string]any{"errors": []map[string]any{{"title": "CF-UnprocessableEntity", "code": 10008, "detail": detail}}})

			writer.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = writer.Write(answer)
		}
	}

	create := func(c *Client) error { _, err := c.CreateServiceBroker("blacksmith"); return err }
	update := func(c *Client) error { _, err := c.UpdateServiceBroker("guid-1", "blacksmith"); return err }
	long := strings.Repeat("a", 300) + "tail-marker"

	cases := []struct {
		name   string
		detail string
		call   func(*Client) error
		want   string
		absent []string
	}{
		{"a creation's detail", "Name must be unique", create, "CF-UnprocessableEntity (10008): Name must be unique", nil},
		{"an update's detail", "Name must be unique", update, "CF-UnprocessableEntity (10008): Name must be unique", nil},
		{"a long detail", long, create, ": " + strings.Repeat("a", refusalDetailLimit), []string{strings.Repeat("a", refusalDetailLimit+1), "tail-marker"}},
		{"a detail over several lines", "Name\r\nX-Injected: 1\tnow\x1b[31m red", create, ": NameX-Injected: 1now[31m red", []string{"\n", "\r", "\t", "\x1b"}},
		{"the CF password", "bad login " + foundationPassword, create, ": bad login [redacted]", []string{foundationPassword}},
		{"the broker password", "bad url " + brokerPassword + " given", update, ": bad url [redacted] given", []string{brokerPassword}},
		{"the token", "token bearer " + foundationToken + " seen", create, ": token bearer [redacted] seen", []string{foundationToken}},
		{"a secret split by a newline", "pw " + brokerPassword[:4] + "\n" + brokerPassword[4:], update, ": pw [redacted]", []string{brokerPassword}},
		{"a secret past the limit", strings.Repeat("b", refusalDetailLimit-4) + brokerPassword, create, ": " + strings.Repeat("b", refusalDetailLimit-4), []string{"b" + brokerPassword[:4]}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			foundation := newV3OnlyFoundation(t)
			foundation.brokersAnswer = refuse(tc.detail)

			err := tc.call(brokerClient(foundation))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("the call = %v, want an error containing %q", err, tc.want)
			}

			for _, quoted := range tc.absent {
				if strings.Contains(err.Error(), quoted) {
					t.Errorf("the error contains %q: %q", quoted, err.Error())
				}
			}
		})
	}
}

// refusingClient is brokerClient with its own broker password, against a
// foundation whose every broker request is refused with detail.
func refusingClient(t *testing.T, brokerPass, detail string) *Client {
	t.Helper()

	foundation := newV3OnlyFoundation(t)
	foundation.brokersAnswer = func(writer http.ResponseWriter, _ *http.Request) {
		answer, _ := json.Marshal(map[string]any{"errors": []map[string]any{{"title": "CF-UnprocessableEntity", "code": 10008, "detail": detail}}})

		writer.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = writer.Write(answer)
	}

	client := NewClient(foundation.URL, foundationUser, foundationPassword, "https://broker.example.com", "broker", brokerPass)
	client.authClient.httpClient = foundation.Client()

	return client
}

func TestCFRefusalsRedactSecretsAsTheDetailIsCleaned(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		brokerPass string
		detail     string
		want       string
		absent     []string
	}{
		{"a secret holding a tab", "broker\tpw-77", "bad broker\tpw-77 here", ": bad [redacted] here", []string{"brokerpw-77", "pw-77"}},
		{"a secret inside a longer one", foundationPassword + "-broker", "echo " + foundationPassword + "-broker end", ": echo [redacted] end", []string{"-broker end"}},
		{"an empty broker password", "", "a bad name", "CF-UnprocessableEntity (10008): a bad name", []string{"[redacted]"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := refusingClient(t, tc.brokerPass, tc.detail).CreateServiceBroker("blacksmith")
			if err == nil || !strings.HasSuffix(err.Error(), tc.want) {
				t.Fatalf("the call = %v, want an error ending %q", err, tc.want)
			}

			for _, quoted := range tc.absent {
				if strings.Contains(err.Error(), quoted) {
					t.Errorf("the error contains %q: %q", quoted, err.Error())
				}
			}
		})
	}
}

// Redaction stops once the limit is written, so a short secret in a large
// answer can't grow the text far past the answer itself.
func TestSafeDetailStopsAtTheLimit(t *testing.T) {
	client := NewClient("https://api.cf.example.com", "admin", "a", "https://broker.example.com", "broker", "e")
	detail := strings.Repeat("e", 2<<20)

	var before, after runtime.MemStats

	runtime.GC()
	runtime.ReadMemStats(&before)

	cleaned := client.safeDetail(detail)

	runtime.ReadMemStats(&after)

	if got := after.TotalAlloc - before.TotalAlloc; got > 16<<20 {
		t.Errorf("safeDetail allocated %d bytes for a 2 MiB detail, want at most 16 MiB", got)
	}

	if want := strings.Repeat("[redacted]", 25) + "[redac"; cleaned != want {
		t.Errorf("safeDetail() = %q, want %q", cleaned, want)
	}
}

// A detail that repeats a long secret past the limit shows only markers,
// because each secret is matched against the whole remaining detail, not a
// copy already cut short.
func TestSafeDetailNeverShowsTheFrontOfACutSecret(t *testing.T) {
	t.Parallel()

	secret := "cf-pw-0123456789-abcdefghijklm"
	if len(secret) != 30 {
		t.Fatalf("the secret is %d characters, want 30", len(secret))
	}

	client := NewClient("https://api.cf.example.com", "admin", secret, "https://broker.example.com", "broker", "broker-pw-7d1f")

	cases := []struct {
		name   string
		detail string
		want   string
	}{
		{"ten copies of a 30-character secret", strings.Repeat(secret, 10), strings.Repeat("[redacted]", 10)},
		{"a secret straddling character 256", strings.Repeat("x", 250) + secret + "tail", strings.Repeat("x", 250) + "[redac"},
		{"a secret starting at character 256", strings.Repeat("x", 256) + secret, strings.Repeat("x", 256)},
		{"a secret starting at character 255", strings.Repeat("x", 255) + secret, strings.Repeat("x", 255) + "["},
		{"copies after a long prefix", strings.Repeat("y", 200) + strings.Repeat(secret, 9), strings.Repeat("y", 200) + strings.Repeat("[redacted]", 5) + "[redac"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := client.safeDetail(tc.detail)
			if got != tc.want {
				t.Errorf("safeDetail() = %q, want %q", got, tc.want)
			}

			if strings.Contains(got, secret[:3]) {
				t.Errorf("safeDetail() = %q, which shows the front of the secret", got)
			}
		})
	}
}
