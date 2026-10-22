package credhub_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"blacksmith/internal/credhub"
)

const regenerateAnswerBody = `{"id":"v3","name":"/d/dep/redis_crt","type":"certificate","value":{"certificate":"CERT","private_key":"-----BEGIN RSA PRIVATE KEY-----SECRET"}}`

// regenerateFake records each regenerate request and answers with handler.
type regenerateFake struct {
	server *httptest.Server
	mu     sync.Mutex
	bodies []string
	paths  []string
	count  int
}

func newRegenerateFake(t *testing.T, handler func(n int, writer http.ResponseWriter, request *http.Request)) *regenerateFake {
	t.Helper()

	fake := &regenerateFake{}
	fake.server = httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)

		fake.mu.Lock()
		fake.count++
		n := fake.count
		fake.bodies = append(fake.bodies, string(body))
		fake.paths = append(fake.paths, request.Method+" "+request.URL.Path)
		fake.mu.Unlock()

		handler(n, writer, request)
	}))
	t.Cleanup(fake.server.Close)

	return fake
}

func (f *regenerateFake) client(t *testing.T) *credhub.Client {
	t.Helper()

	client, err := credhub.NewClient(credhub.ClientConfig{URL: f.server.URL, CACert: serverCA(f.server)}, &fakeTokens{}, nil)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	return client
}

func (f *regenerateFake) requests() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.count
}

func reply(status int, body string) func(int, http.ResponseWriter, *http.Request) {
	return func(_ int, writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(status)
		_, _ = writer.Write([]byte(body))
	}
}

func TestRegenerateSendsNotTransitional(t *testing.T) {
	t.Parallel()

	fake := newRegenerateFake(t, reply(http.StatusOK, regenerateAnswerBody))

	got, err := fake.client(t).Regenerate(context.Background(), "c-1")
	if err != nil {
		t.Fatalf("Regenerate: %v", err)
	}

	if got.VersionID != "v3" || got.Name != "/d/dep/redis_crt" {
		t.Fatalf("unexpected result %+v", got)
	}

	if len(fake.bodies) != 1 || fake.bodies[0] != `{"set_as_transitional":false}` {
		t.Fatalf("unexpected body %q", fake.bodies)
	}

	if fake.paths[0] != "POST /api/v1/certificates/c-1/regenerate" {
		t.Fatalf("unexpected request %q", fake.paths)
	}
}

func TestRegenerateRefusesEmptyID(t *testing.T) {
	t.Parallel()

	fake := newRegenerateFake(t, reply(http.StatusOK, regenerateAnswerBody))

	_, err := fake.client(t).Regenerate(context.Background(), "")
	if !errors.Is(err, credhub.ErrEmptyCertificateID) || fake.requests() != 0 {
		t.Fatalf("err = %v, requests = %d", err, fake.requests())
	}
}

func TestRegenerateTimeoutIsUncertain(t *testing.T) {
	t.Parallel()

	fake := newRegenerateFake(t, func(_ int, _ http.ResponseWriter, _ *http.Request) { time.Sleep(600 * time.Millisecond) })

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err := fake.client(t).Regenerate(ctx, "c-1")
	if !credhub.IsUncertain(err) {
		t.Fatalf("err = %v, want an uncertain error", err)
	}
}

func TestRegenerate502IsUncertain(t *testing.T) {
	t.Parallel()

	fake := newRegenerateFake(t, reply(http.StatusBadGateway, `{"error":"bad gateway"}`))

	_, err := fake.client(t).Regenerate(context.Background(), "c-1")
	if !credhub.IsUncertain(err) {
		t.Fatalf("err = %v, want an uncertain error", err)
	}
}

func TestRegenerate404IsCertain(t *testing.T) {
	t.Parallel()

	fake := newRegenerateFake(t, reply(http.StatusNotFound, `{"error":"no such certificate"}`))

	_, err := fake.client(t).Regenerate(context.Background(), "c-1")
	if credhub.IsUncertain(err) {
		t.Fatalf("a 404 must be certain: %v", err)
	}

	var apiErr *credhub.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusNotFound {
		t.Fatalf("err = %v, want an APIError with status 404", err)
	}
}

func TestRegenerateDroppedAfterWriteIsUncertain(t *testing.T) {
	t.Parallel()

	fake := newRegenerateFake(t, func(_ int, writer http.ResponseWriter, _ *http.Request) {
		conn, _, err := writer.(http.Hijacker).Hijack()
		if err == nil {
			_ = conn.Close()
		}
	})

	_, err := fake.client(t).Regenerate(context.Background(), "c-1")
	if !credhub.IsUncertain(err) {
		t.Fatalf("err = %v, want an uncertain error", err)
	}
}

func TestRegenerateFailureBeforeConnectIsCertain(t *testing.T) {
	t.Parallel()

	fake := newRegenerateFake(t, reply(http.StatusOK, regenerateAnswerBody))
	client := fake.client(t)

	fake.server.Close()

	_, err := client.Regenerate(context.Background(), "c-1")
	if err == nil || credhub.IsUncertain(err) {
		t.Fatalf("err = %v, want a certain error", err)
	}
}

func TestRegenerateNeverRetries(t *testing.T) {
	t.Parallel()

	fake := newRegenerateFake(t, func(n int, writer http.ResponseWriter, request *http.Request) {
		if n == 1 {
			reply(http.StatusUnauthorized, `{"error":"expired"}`)(n, writer, request)

			return
		}

		reply(http.StatusOK, regenerateAnswerBody)(n, writer, request)
	})

	_, err := fake.client(t).Regenerate(context.Background(), "c-1")
	if err == nil || credhub.IsUncertain(err) {
		t.Fatalf("err = %v, want a certain error", err)
	}

	if fake.requests() != 1 {
		t.Fatalf("saw %d requests, want 1", fake.requests())
	}
}

func TestRegenerateResponseKeyNotRetained(t *testing.T) {
	t.Parallel()

	fake := newRegenerateFake(t, reply(http.StatusOK, regenerateAnswerBody))

	got, err := fake.client(t).Regenerate(context.Background(), "c-1")
	if err != nil {
		t.Fatalf("Regenerate: %v", err)
	}

	if text := fmt.Sprintf("%+v", got); strings.Contains(text, "PRIVATE") || strings.Contains(text, "SECRET") {
		t.Fatalf("result carries key text: %s", text)
	}
}

func TestRegenerateErrorTextOpaque(t *testing.T) {
	t.Parallel()

	for _, status := range []int{http.StatusInternalServerError, http.StatusBadRequest} {
		fake := newRegenerateFake(t, reply(status, `{"error":"-----BEGIN RSA PRIVATE KEY-----X"}`))

		_, err := fake.client(t).Regenerate(context.Background(), "c-1")
		if err == nil || strings.Contains(err.Error(), "PRIVATE") {
			t.Fatalf("status %d: err = %v, want an error without key text", status, err)
		}
	}
}

func TestRegenerateTruncatedAnswer(t *testing.T) {
	t.Parallel()

	fake := newRegenerateFake(t, reply(http.StatusOK, `{"id":"v3","value":{"private_key":"-----BEGIN RSA PRIVATE KEY-----SECRET`))

	_, err := fake.client(t).Regenerate(context.Background(), "c-1")
	if !credhub.IsUncertain(err) {
		t.Fatalf("err = %v, want an uncertain error", err)
	}

	if strings.Contains(err.Error(), "PRIVATE") || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("error carries key text: %v", err)
	}
}

func TestRegenerateOversizedAnswer(t *testing.T) {
	t.Parallel()

	fake := newRegenerateFake(t, reply(http.StatusOK, `{"id":"v3","pad":"`+strings.Repeat("x", 70<<10)+`"}`))

	_, err := fake.client(t).Regenerate(context.Background(), "c-1")
	if !credhub.IsUncertain(err) {
		t.Fatalf("err = %v, want an uncertain error", err)
	}

	if !strings.Contains(err.Error(), "65536") {
		t.Fatalf("error should name the limit: %v", err)
	}
}
