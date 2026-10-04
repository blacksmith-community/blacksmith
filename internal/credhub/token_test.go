package credhub_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"blacksmith/internal/credhub"
)

const (
	testClientID = "blacksmith_credhub"
	firstToken   = "token-1"
	secondToken  = "token-2"
)

// fakeUAA is a UAA token endpoint that records every request and issues
// numbered tokens.
type fakeUAA struct {
	t         *testing.T
	server    *httptest.Server
	requests  atomic.Int64
	expiresIn atomic.Int64
	delay     time.Duration
	status    int
	body      string
	block     chan struct{}
	arrived   chan struct{}
}

func newFakeUAA(t *testing.T, configure func(*fakeUAA)) *fakeUAA {
	t.Helper()

	uaa := &fakeUAA{t: t, status: http.StatusOK}
	uaa.expiresIn.Store(300)

	if configure != nil {
		configure(uaa)
	}

	uaa.server = httptest.NewTLSServer(http.HandlerFunc(uaa.handle))

	t.Cleanup(func() {
		if uaa.block != nil {
			select {
			case <-uaa.block:
			default:
				close(uaa.block)
			}
		}

		uaa.server.Close()
	})

	return uaa
}

func (u *fakeUAA) handle(writer http.ResponseWriter, request *http.Request) {
	count := u.requests.Add(1)

	if u.arrived != nil {
		select {
		case u.arrived <- struct{}{}:
		default:
		}
	}

	if u.block != nil {
		<-u.block
	}

	if u.delay > 0 {
		time.Sleep(u.delay)
	}

	if request.Method != http.MethodPost || request.URL.Path != "/oauth/token" {
		u.t.Errorf("unexpected UAA request %s %s", request.Method, request.URL.Path)
		writer.WriteHeader(http.StatusNotFound)

		return
	}

	user, pass, ok := request.BasicAuth()
	if !ok || user != testClientID || pass != secretMarker {
		u.t.Errorf("UAA request without the expected basic auth")
	}

	err := request.ParseForm()
	if err != nil || request.PostForm.Get("grant_type") != "client_credentials" {
		u.t.Errorf("UAA request without grant_type=client_credentials: %v", request.PostForm)
	}

	writer.Header().Set("Content-Type", "application/json")

	if u.status != http.StatusOK || u.body != "" {
		writer.WriteHeader(u.status)
		_, _ = writer.Write([]byte(u.body))

		return
	}

	err = json.NewEncoder(writer).Encode(struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int64  `json:"expires_in"`
	}{AccessToken: fmt.Sprintf("token-%d", count), TokenType: "bearer", ExpiresIn: u.expiresIn.Load()})
	if err != nil {
		u.t.Errorf("encode token answer: %v", err)
	}
}

func newTestTokenSource(t *testing.T, uaa *fakeUAA, clock *fakeClock) *credhub.TokenSource {
	t.Helper()

	source, err := credhub.NewTokenSource(uaa.server.URL, testClientID, secretMarker, serverCA(uaa.server), clock.Now)
	if err != nil {
		t.Fatalf("NewTokenSource: %v", err)
	}

	return source
}

func mustToken(t *testing.T, source *credhub.TokenSource) string {
	t.Helper()

	token, err := source.Token(context.Background())
	if err != nil {
		t.Fatalf("Token: %v", err)
	}

	return token
}

func TestTokenSourceFetchesOnceAndReuses(t *testing.T) {
	t.Parallel()

	uaa := newFakeUAA(t, nil)
	clock := newFakeClock()
	source := newTestTokenSource(t, uaa, clock)

	first := mustToken(t, source)

	clock.Advance(100 * time.Second)

	second := mustToken(t, source)

	if first != firstToken || second != firstToken {
		t.Fatalf("expected the cached token-1 twice, got %q and %q", first, second)
	}

	if got := uaa.requests.Load(); got != 1 {
		t.Fatalf("expected one UAA request, got %d", got)
	}
}

func TestTokenSourceRefreshesNearExpiry(t *testing.T) {
	t.Parallel()

	t.Run("expired token", func(t *testing.T) {
		t.Parallel()

		uaa := newFakeUAA(t, nil)
		uaa.expiresIn.Store(1)

		clock := newFakeClock()
		source := newTestTokenSource(t, uaa, clock)

		_ = mustToken(t, source)

		clock.Advance(2 * time.Second)

		if got := mustToken(t, source); got != secondToken {
			t.Fatalf("expected a fresh token after expiry, got %q", got)
		}
	})

	t.Run("inside the refresh margin", func(t *testing.T) {
		t.Parallel()

		uaa := newFakeUAA(t, nil)
		clock := newFakeClock()
		source := newTestTokenSource(t, uaa, clock)

		_ = mustToken(t, source)

		clock.Advance(239 * time.Second)

		if got := mustToken(t, source); got != firstToken {
			t.Fatalf("expected the cached token with 61 seconds left, got %q", got)
		}

		clock.Advance(2 * time.Second)

		if got := mustToken(t, source); got != secondToken {
			t.Fatalf("expected a fresh token with 59 seconds left, got %q", got)
		}
	})
}

func TestTokenSourceInvalidate(t *testing.T) {
	t.Parallel()

	uaa := newFakeUAA(t, nil)
	clock := newFakeClock()
	source := newTestTokenSource(t, uaa, clock)

	first := mustToken(t, source)
	source.Invalidate("some-other-token")

	if got := mustToken(t, source); got != first {
		t.Fatalf("Invalidate with a stale token must leave the cache alone, got %q", got)
	}

	source.Invalidate(first)

	second := mustToken(t, source)
	if second == first {
		t.Fatal("Invalidate with the cached token must force a fetch")
	}

	source.Invalidate(first)

	if got := mustToken(t, source); got != second {
		t.Fatalf("Invalidate with an older token must leave the newer one cached, got %q", got)
	}

	if got := uaa.requests.Load(); got != 2 {
		t.Fatalf("expected two UAA requests, got %d", got)
	}
}

func TestTokenSourceConcurrentCallersShareOneFetch(t *testing.T) {
	t.Parallel()

	uaa := newFakeUAA(t, func(u *fakeUAA) { u.delay = 50 * time.Millisecond })
	source := newTestTokenSource(t, uaa, newFakeClock())

	tokens := runConcurrently(t, 50, func() (string, error) { return source.Token(context.Background()) })

	for _, token := range tokens {
		if token != firstToken {
			t.Fatalf("expected every caller to get token-1, got %q", token)
		}
	}

	if got := uaa.requests.Load(); got != 1 {
		t.Fatalf("expected one UAA request for 50 concurrent callers, got %d", got)
	}
}

func TestTokenSourceConcurrentInvalidateFetchesOnce(t *testing.T) {
	t.Parallel()

	uaa := newFakeUAA(t, func(u *fakeUAA) { u.delay = 20 * time.Millisecond })
	source := newTestTokenSource(t, uaa, newFakeClock())
	rejected := mustToken(t, source)

	tokens := runConcurrently(t, 50, func() (string, error) {
		source.Invalidate(rejected)

		return source.Token(context.Background())
	})

	for _, token := range tokens {
		if token != secondToken {
			t.Fatalf("expected every caller to get token-2, got %q", token)
		}
	}

	if got := uaa.requests.Load(); got != 2 {
		t.Fatalf("expected one fetch for the first token and one after 50 invalidations, got %d", got)
	}
}

func TestTokenSourceWaiterHonorsItsOwnContext(t *testing.T) {
	t.Parallel()

	uaa := newFakeUAA(t, func(u *fakeUAA) {
		u.block = make(chan struct{})
		u.arrived = make(chan struct{}, 1)
	})
	source := newTestTokenSource(t, uaa, newFakeClock())

	type result struct {
		token string
		err   error
	}

	first := make(chan result, 1)

	go func() {
		token, err := source.Token(context.Background())
		first <- result{token: token, err: err}
	}()

	<-uaa.arrived

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	started := time.Now()
	_, err := source.Token(ctx)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected the waiter to stop at its own deadline, got %v", err)
	}

	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("waiter took %v to give up", elapsed)
	}

	close(uaa.block)

	got := <-first
	if got.err != nil || got.token != firstToken {
		t.Fatalf("expected the in-flight fetch to finish for the first caller, got %q, %v", got.token, got.err)
	}

	if requests := uaa.requests.Load(); requests != 1 {
		t.Fatalf("expected one UAA request, got %d", requests)
	}
}

func TestTokenSourceUAAErrorIsSafe(t *testing.T) {
	t.Parallel()

	uaa := newFakeUAA(t, func(u *fakeUAA) {
		u.status = http.StatusUnauthorized
		u.body = `{"error":"unauthorized","error_description":"Bad credentials for ` + secretMarker + `"}`
	})
	source := newTestTokenSource(t, uaa, newFakeClock())

	_, err := source.Token(context.Background())
	if err == nil {
		t.Fatal("expected an error from a UAA 401")
	}

	var uaaErr *credhub.UAAError
	if !errors.As(err, &uaaErr) || uaaErr.Status != http.StatusUnauthorized {
		t.Fatalf("expected a *UAAError with status 401, got %#v", err)
	}

	message := err.Error()
	for _, want := range []string{"401", "unauthorized", "Bad credentials"} {
		if !strings.Contains(message, want) {
			t.Errorf("error %q does not contain %q", message, want)
		}
	}

	assertNoMarkers(t, message)
}

func TestTokenSourceUAAErrorIsCut(t *testing.T) {
	t.Parallel()

	uaa := newFakeUAA(t, func(u *fakeUAA) {
		u.status = http.StatusBadRequest
		u.body = `{"error":"invalid_request","error_description":"` + strings.Repeat("d", 2000) + `"}`
	})
	source := newTestTokenSource(t, uaa, newFakeClock())

	_, err := source.Token(context.Background())

	var uaaErr *credhub.UAAError
	if !errors.As(err, &uaaErr) {
		t.Fatalf("expected a *UAAError, got %v", err)
	}

	if got := len([]rune(uaaErr.Description)); got != 200 {
		t.Fatalf("expected the description cut to 200 characters, got %d", got)
	}
}

func TestTokenSourceRejectsAnEmptyToken(t *testing.T) {
	t.Parallel()

	uaa := newFakeUAA(t, func(u *fakeUAA) { u.body = `{"token_type":"bearer","expires_in":300}` })
	source := newTestTokenSource(t, uaa, newFakeClock())

	_, err := source.Token(context.Background())
	if err == nil {
		t.Fatal("expected an error for an answer without a token")
	}
}

func TestTokenSourceUnresponsiveUAA(t *testing.T) {
	t.Parallel()

	uaa := newFakeUAA(t, func(u *fakeUAA) { u.block = make(chan struct{}) })
	source := newTestTokenSource(t, uaa, newFakeClock())

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	started := time.Now()
	_, err := source.Token(ctx)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected a deadline error, got %v", err)
	}

	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("Token took %v against a UAA that never answers", elapsed)
	}
}

func TestTokenSourceWrongCA(t *testing.T) {
	t.Parallel()

	uaa := newFakeUAA(t, nil)

	source, err := credhub.NewTokenSource(uaa.server.URL, testClientID, secretMarker, unrelatedCA(t), newFakeClock().Now)
	if err != nil {
		t.Fatalf("NewTokenSource: %v", err)
	}

	_, err = source.Token(context.Background())
	if err == nil || !strings.Contains(err.Error(), "x509") {
		t.Fatalf("expected an x509 error, got %v", err)
	}

	if !errors.Is(err, credhub.ErrUAAUnreachable) {
		t.Fatalf("expected the error to wrap ErrUAAUnreachable, got %v", err)
	}

	assertNoMarkers(t, err.Error())
}

func TestNewTokenSourceValidatesInput(t *testing.T) {
	t.Parallel()

	uaa := newFakeUAA(t, nil)
	caPEM := serverCA(uaa.server)

	tests := map[string][]string{
		"empty url":     {"", testClientID, secretMarker, caPEM},
		"http url":      {"http://10.0.0.6:8443", testClientID, secretMarker, caPEM},
		"empty client":  {uaa.server.URL, "", secretMarker, caPEM},
		"empty secret":  {uaa.server.URL, testClientID, "", caPEM},
		"no PEM in CA":  {uaa.server.URL, testClientID, secretMarker, "not a certificate"},
		"empty CA text": {uaa.server.URL, testClientID, secretMarker, ""},
	}

	for name, args := range tests {
		_, err := credhub.NewTokenSource(args[0], args[1], args[2], args[3], nil)
		if err == nil {
			t.Errorf("%s: expected an error", name)

			continue
		}

		assertNoMarkers(t, err.Error())
	}
}

// runConcurrently starts n goroutines at once and returns what each got.
func runConcurrently(t *testing.T, callers int, call func() (string, error)) []string {
	t.Helper()

	var (
		start sync.WaitGroup
		done  sync.WaitGroup
		lock  sync.Mutex
		out   []string
	)

	start.Add(1)

	for range callers {
		done.Add(1)

		go func() {
			defer done.Done()

			start.Wait()

			token, err := call()
			if err != nil {
				t.Errorf("concurrent call: %v", err)

				return
			}

			lock.Lock()

			out = append(out, token)

			lock.Unlock()
		}()
	}

	start.Done()
	done.Wait()

	if len(out) != callers {
		t.Fatalf("expected %d results, got %d", callers, len(out))
	}

	return out
}
