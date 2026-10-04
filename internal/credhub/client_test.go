package credhub_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"blacksmith/internal/credhub"
)

const brokerPrefix = "/" + labDirector + "/" + labBroker + "/"

// fakeTokens hands out numbered tokens built from tokenMarker and records
// every Invalidate call.
type fakeTokens struct {
	mu          sync.Mutex
	generation  int
	invalidated []string
	err         error
}

func (f *fakeTokens) Token(context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.err != nil {
		return "", f.err
	}

	return f.current(), nil
}

func (f *fakeTokens) Invalidate(stale string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.invalidated = append(f.invalidated, stale)

	if stale == f.current() {
		f.generation++
	}
}

func (f *fakeTokens) current() string {
	return fmt.Sprintf("%s-%d", tokenMarker, f.generation)
}

func (f *fakeTokens) invalidations() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return slices.Clone(f.invalidated)
}

type recordedRequest struct {
	method string
	query  string
	auth   string
}

// fakeCredHub allows only find by path and delete by name, and fails the
// test on any other request.
type fakeCredHub struct {
	t       *testing.T
	server  *httptest.Server
	mu      sync.Mutex
	seen    []recordedRequest
	respond func(n int, request *http.Request) (int, string)
}

func newFakeCredHub(t *testing.T, respond func(n int, request *http.Request) (int, string)) *fakeCredHub {
	t.Helper()

	fake := &fakeCredHub{t: t, respond: respond}
	fake.server = httptest.NewTLSServer(http.HandlerFunc(fake.handle))
	t.Cleanup(fake.server.Close)

	return fake
}

func (f *fakeCredHub) handle(writer http.ResponseWriter, request *http.Request) {
	f.mu.Lock()
	f.seen = append(f.seen, recordedRequest{method: request.Method, query: request.URL.RawQuery, auth: request.Header.Get("Authorization")})
	count := len(f.seen)
	f.mu.Unlock()

	query := request.URL.Query()
	allowed := request.URL.Path == "/api/v1/data" && len(query) == 1 &&
		((request.Method == http.MethodGet && query.Has("path")) || (request.Method == http.MethodDelete && query.Has("name")))

	if !allowed {
		f.t.Errorf("CredHub received a request outside find by path and delete by name: %s %s?%s", request.Method, request.URL.Path, request.URL.RawQuery)
		writer.WriteHeader(http.StatusTeapot)

		return
	}

	status, body := f.respond(count, request)

	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_, _ = writer.Write([]byte(body))
}

func (f *fakeCredHub) requests() []recordedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()

	return slices.Clone(f.seen)
}

func newTestClient(t *testing.T, fake *fakeCredHub, tokens *fakeTokens) *credhub.Client {
	t.Helper()

	client, err := credhub.NewClient(credhub.ClientConfig{URL: fake.server.URL, CACert: serverCA(fake.server)}, tokens, []string{brokerPrefix})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	return client
}

func TestClientFindByPath(t *testing.T) {
	t.Parallel()

	fake := newFakeCredHub(t, func(int, *http.Request) (int, string) {
		return http.StatusOK, `{"credentials":[{"name":"` + labPrefix + `valkey_standalone_crt","version_created_at":"2026-10-01T00:00:00Z"},{"name":"` + labPrefix + `valkey_password"}]}`
	})
	tokens := &fakeTokens{}
	client := newTestClient(t, fake, tokens)

	names, err := client.FindByPath(context.Background(), labPrefix)
	if err != nil {
		t.Fatalf("FindByPath: %v", err)
	}

	if want := []string{labPrefix + "valkey_standalone_crt", labPrefix + "valkey_password"}; !slices.Equal(names, want) {
		t.Fatalf("names = %q, want %q", names, want)
	}

	seen := fake.requests()
	if len(seen) != 1 || seen[0].method != http.MethodGet || seen[0].query != "path="+url.QueryEscape(labPrefix) {
		t.Fatalf("unexpected requests %+v", seen)
	}

	if seen[0].auth != "Bearer "+tokenMarker+"-0" {
		t.Fatal("FindByPath did not send the token as a bearer token")
	}
}

func TestClientFindByPathEmpty(t *testing.T) {
	t.Parallel()

	fake := newFakeCredHub(t, func(int, *http.Request) (int, string) { return http.StatusOK, `{"credentials":[]}` })
	client := newTestClient(t, fake, &fakeTokens{})

	names, err := client.FindByPath(context.Background(), labPrefix)
	if err != nil || len(names) != 0 {
		t.Fatalf("expected no names and no error, got %q, %v", names, err)
	}
}

func TestClientFindByPathRejectsABadAnswer(t *testing.T) {
	t.Parallel()

	fake := newFakeCredHub(t, func(int, *http.Request) (int, string) { return http.StatusOK, `not json` })
	client := newTestClient(t, fake, &fakeTokens{})

	_, err := client.FindByPath(context.Background(), labPrefix)
	if err == nil {
		t.Fatal("expected an error for an answer that is not JSON")
	}
}

func TestClientDelete(t *testing.T) {
	t.Parallel()

	name := labPrefix + "valkey_standalone_crt"

	t.Run("204", func(t *testing.T) {
		t.Parallel()

		fake := newFakeCredHub(t, func(int, *http.Request) (int, string) { return http.StatusNoContent, "" })
		client := newTestClient(t, fake, &fakeTokens{})

		err := client.Delete(context.Background(), name)
		if err != nil {
			t.Fatalf("Delete: %v", err)
		}

		seen := fake.requests()
		if len(seen) != 1 || seen[0].method != http.MethodDelete || seen[0].query != "name="+url.QueryEscape(name) {
			t.Fatalf("unexpected requests %+v", seen)
		}
	})

	t.Run("404", func(t *testing.T) {
		t.Parallel()

		fake := newFakeCredHub(t, func(int, *http.Request) (int, string) {
			return http.StatusNotFound, `{"error":"The request could not be completed because the credential does not exist or you do not have sufficient authorization."}`
		})
		client := newTestClient(t, fake, &fakeTokens{})

		err := client.Delete(context.Background(), name)
		if !errors.Is(err, credhub.ErrNotFound) {
			t.Fatalf("expected ErrNotFound, got %v", err)
		}
	})

	t.Run("exact name with query characters", func(t *testing.T) {
		t.Parallel()

		odd := labPrefix + "a+b&path=/x"

		var got string

		fake := newFakeCredHub(t, func(_ int, request *http.Request) (int, string) {
			got = request.URL.Query().Get("name")

			return http.StatusNoContent, ""
		})
		client := newTestClient(t, fake, &fakeTokens{})

		err := client.Delete(context.Background(), odd)
		if err != nil || got != odd {
			t.Fatalf("expected CredHub to receive the exact name %q, got %q, %v", odd, got, err)
		}
	})
}

func TestClientRetriesOnceAfter401(t *testing.T) {
	t.Parallel()

	fake := newFakeCredHub(t, func(n int, _ *http.Request) (int, string) {
		if n == 1 {
			return http.StatusUnauthorized, `{"error":"invalid_token"}`
		}

		return http.StatusNoContent, ""
	})
	tokens := &fakeTokens{}
	client := newTestClient(t, fake, tokens)

	err := client.Delete(context.Background(), labPrefix+"valkey_standalone_crt")
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if got := tokens.invalidations(); !slices.Equal(got, []string{tokenMarker + "-0"}) {
		t.Fatal("expected exactly one Invalidate carrying the rejected token")
	}

	seen := fake.requests()
	if len(seen) != 2 || seen[0].auth == seen[1].auth {
		t.Fatalf("expected two requests with different tokens, got %d", len(seen))
	}
}

func TestClientReturnsASecond401(t *testing.T) {
	t.Parallel()

	fake := newFakeCredHub(t, func(int, *http.Request) (int, string) {
		return http.StatusUnauthorized, `{"error":"invalid_token"}`
	})
	tokens := &fakeTokens{}
	client := newTestClient(t, fake, tokens)

	err := client.Delete(context.Background(), labPrefix+"valkey_standalone_crt")

	var apiErr *credhub.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusUnauthorized || !apiErr.Refreshed {
		t.Fatalf("expected an *APIError with status 401 after a refresh, got %v", err)
	}

	if got := len(fake.requests()); got != 2 {
		t.Fatalf("expected exactly two requests, got %d", got)
	}

	if got := len(tokens.invalidations()); got != 1 {
		t.Fatalf("expected one Invalidate, got %d", got)
	}

	assertNoMarkers(t, err.Error())
}

func TestClientRefusesProtectedNames(t *testing.T) {
	t.Parallel()

	fake := newFakeCredHub(t, func(int, *http.Request) (int, string) { return http.StatusNoContent, "" })
	client := newTestClient(t, fake, &fakeTokens{})

	for _, name := range []string{brokerPrefix + "blacksmith_services_ca", brokerPrefix} {
		err := client.Delete(context.Background(), name)
		if !errors.Is(err, credhub.ErrProtectedName) {
			t.Errorf("Delete(%q) = %v, want ErrProtectedName", name, err)
		}
	}

	err := client.Delete(context.Background(), "")
	if !errors.Is(err, credhub.ErrEmptyName) {
		t.Errorf("Delete(\"\") = %v, want ErrEmptyName", err)
	}

	if got := len(fake.requests()); got != 0 {
		t.Fatalf("expected no request for a protected name, got %d", got)
	}
}

func TestClient403IsNotRetried(t *testing.T) {
	t.Parallel()

	fake := newFakeCredHub(t, func(int, *http.Request) (int, string) {
		return http.StatusForbidden, `{"error":"insufficient permissions"}`
	})
	tokens := &fakeTokens{}
	client := newTestClient(t, fake, tokens)

	err := client.Delete(context.Background(), labPrefix+"valkey_standalone_crt")

	var apiErr *credhub.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusForbidden || apiErr.Message != insufficientPerm {
		t.Fatalf("expected an *APIError 403 with CredHub's text, got %#v", err)
	}

	if apiErr.Refreshed || apiErr.Op != deleteOp {
		t.Fatalf("unexpected APIError fields %+v", apiErr)
	}

	if got := len(fake.requests()); got != 1 || len(tokens.invalidations()) != 0 {
		t.Fatalf("expected one request and no refresh, got %d requests", got)
	}
}

func TestClientCutsLongErrors(t *testing.T) {
	t.Parallel()

	fake := newFakeCredHub(t, func(int, *http.Request) (int, string) {
		return http.StatusInternalServerError, `{"error":"` + strings.Repeat("e", 2000) + `"}`
	})
	client := newTestClient(t, fake, &fakeTokens{})

	_, err := client.FindByPath(context.Background(), labPrefix)

	var apiErr *credhub.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusInternalServerError || apiErr.Op != findOp {
		t.Fatalf("expected an *APIError 500 from find, got %v", err)
	}

	if got := len([]rune(apiErr.Message)); got != 200 {
		t.Fatalf("expected the message cut to 200 characters, got %d", got)
	}

	if got := len(fake.requests()); got != 1 {
		t.Fatalf("the client itself must not retry a 500, got %d requests", got)
	}
}

func TestClientDoesNotFollowRedirects(t *testing.T) {
	t.Parallel()

	fake := newFakeCredHub(t, func(int, *http.Request) (int, string) { return http.StatusFound, "" })
	client := newTestClient(t, fake, &fakeTokens{})

	err := client.Delete(context.Background(), labPrefix+"valkey_standalone_crt")

	var apiErr *credhub.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusFound {
		t.Fatalf("expected an *APIError 302, got %v", err)
	}

	if got := len(fake.requests()); got != 1 {
		t.Fatalf("expected one request, got %d", got)
	}
}

func TestClientWrongCA(t *testing.T) {
	t.Parallel()

	fake := newFakeCredHub(t, func(int, *http.Request) (int, string) { return http.StatusOK, `{"credentials":[]}` })

	client, err := credhub.NewClient(credhub.ClientConfig{URL: fake.server.URL, CACert: unrelatedCA(t)}, &fakeTokens{}, nil)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	_, err = client.FindByPath(context.Background(), labPrefix)
	if err == nil || !strings.Contains(err.Error(), "x509") {
		t.Fatalf("expected an x509 error, got %v", err)
	}

	assertNoMarkers(t, err.Error())
}

func TestClientTokenFailureSendsNothing(t *testing.T) {
	t.Parallel()

	fake := newFakeCredHub(t, func(int, *http.Request) (int, string) { return http.StatusNoContent, "" })
	tokenErr := &credhub.UAAError{Status: http.StatusUnauthorized, Code: uaaUnauthorized}
	client := newTestClient(t, fake, &fakeTokens{err: tokenErr})

	err := client.Delete(context.Background(), labPrefix+"valkey_standalone_crt")

	var uaaErr *credhub.UAAError
	if !errors.As(err, &uaaErr) {
		t.Fatalf("expected the UAA error to come through, got %v", err)
	}

	if got := len(fake.requests()); got != 0 {
		t.Fatalf("expected no CredHub request without a token, got %d", got)
	}
}

func TestClientErrorsNeverCarryTheToken(t *testing.T) {
	t.Parallel()

	fake := newFakeCredHub(t, func(_ int, request *http.Request) (int, string) {
		echoed := strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer ")

		return http.StatusForbidden, `{"error":"token ` + echoed + ` is not allowed"}`
	})
	client := newTestClient(t, fake, &fakeTokens{})

	err := client.Delete(context.Background(), labPrefix+"valkey_standalone_crt")
	if err == nil {
		t.Fatal("expected an error")
	}

	assertNoMarkers(t, err.Error())

	_, err = client.FindByPath(context.Background(), labPrefix)
	if err == nil {
		t.Fatal("expected an error")
	}

	assertNoMarkers(t, err.Error())
}

func TestClientHasOnlyFindAndDelete(t *testing.T) {
	t.Parallel()

	clientType := reflect.TypeFor[*credhub.Client]()

	methods := make([]string, 0, clientType.NumMethod())
	for i := range clientType.NumMethod() {
		methods = append(methods, clientType.Method(i).Name)
	}

	if want := []string{"Delete", "FindByPath"}; !slices.Equal(methods, want) {
		t.Fatalf("Client methods = %q, want only %q", methods, want)
	}
}

func TestNewClientValidatesInput(t *testing.T) {
	t.Parallel()

	fake := newFakeCredHub(t, func(int, *http.Request) (int, string) { return http.StatusOK, "" })
	caPEM := serverCA(fake.server)

	tests := map[string]credhub.ClientConfig{
		"http url":   {URL: "http://10.0.0.6:8844", CACert: caPEM},
		"empty url":  {URL: "", CACert: caPEM},
		"bad CA":     {URL: fake.server.URL, CACert: "not a certificate"},
		"missing CA": {URL: fake.server.URL},
	}

	for name, cfg := range tests {
		_, err := credhub.NewClient(cfg, &fakeTokens{}, nil)
		if err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}

	_, err := credhub.NewClient(credhub.ClientConfig{URL: fake.server.URL, CACert: caPEM}, nil, nil)
	if err == nil {
		t.Error("expected an error for a nil token provider")
	}
}
