package credhub_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"blacksmith/internal/credhub"
)

const (
	fakeKeyText = "-----BEGIN RSA PRIVATE KEY-----SECRET"
	redisCrt    = "/d/dep/redis_crt"
)

// readServer is a TLS CredHub fake that answers every request with what
// respond returns, and records the request it saw.
type readServer struct {
	server *httptest.Server
	mu     sync.Mutex
	paths  []string
	query  []url.Values
}

func newReadServer(t *testing.T, respond func(request *http.Request) (int, string)) *readServer {
	t.Helper()

	fake := &readServer{}
	fake.server = httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		fake.mu.Lock()
		fake.paths = append(fake.paths, request.URL.Path)
		fake.query = append(fake.query, request.URL.Query())
		fake.mu.Unlock()

		status, body := respond(request)

		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(status)
		_, _ = writer.Write([]byte(body))
	}))
	t.Cleanup(fake.server.Close)

	return fake
}

func (f *readServer) client(t *testing.T) *credhub.Client {
	t.Helper()

	client, err := credhub.NewClient(credhub.ClientConfig{URL: f.server.URL, CACert: serverCA(f.server)}, &fakeTokens{}, nil)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	return client
}

func (f *readServer) lastQuery(t *testing.T) url.Values {
	t.Helper()

	f.mu.Lock()
	defer f.mu.Unlock()

	if len(f.query) == 0 {
		t.Fatal("CredHub saw no request")
	}

	return f.query[len(f.query)-1]
}

func answer(body string) func(*http.Request) (int, string) {
	return func(*http.Request) (int, string) { return http.StatusOK, body }
}

func TestCertificateMetadataParsesVersions(t *testing.T) {
	t.Parallel()

	fake := newReadServer(t, answer(`{"certificates":[{"id":"c-1","name":"/d/dep/redis_crt","signed_by":"/d/dep-blacksmith/blacksmith_services_ca",
"versions":[{"id":"v2","expiry_date":"2027-10-21T00:00:00Z","transitional":false,"certificate_authority":false},
{"id":"v1","expiry_date":"2026-10-27T09:12:00Z","transitional":true,"certificate_authority":false,"self_signed":true,"generated":true}]}]}`))

	got, err := fake.client(t).CertificateMetadata(context.Background(), redisCrt)
	if err != nil {
		t.Fatalf("CertificateMetadata: %v", err)
	}

	if got.ID != "c-1" || got.Name != redisCrt || got.SignedBy != "/d/dep-blacksmith/blacksmith_services_ca" || len(got.Versions) != 2 {
		t.Fatalf("unexpected metadata %+v", got)
	}

	want := time.Date(2026, 10, 27, 9, 12, 0, 0, time.UTC)
	if second := got.Versions[1]; !second.ExpiryDate.Equal(want) || second.ID != "v1" || !second.Transitional || !second.SelfSigned || !second.Generated {
		t.Fatalf("unexpected second version %+v", second)
	}

	if fake.paths[0] != "/api/v1/certificates" || fake.lastQuery(t).Get("name") != redisCrt {
		t.Fatalf("unexpected request %v %v", fake.paths, fake.query)
	}
}

func TestCertificateMetadataNotFound(t *testing.T) {
	t.Parallel()

	fake := newReadServer(t, answer(`{"certificates":[]}`))

	_, err := fake.client(t).CertificateMetadata(context.Background(), redisCrt)
	if !errors.Is(err, credhub.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestCredentialVersionsDiscardsValues(t *testing.T) {
	t.Parallel()

	fake := newReadServer(t, answer(`{"data":[{"id":"p1","type":"password","version_created_at":"2026-10-01T00:00:00Z","value":"hunter2"},
{"id":"p2","type":"password","version_created_at":"2026-10-20T00:00:00Z","value":"SECRET2"}]}`))

	got, err := fake.client(t).CredentialVersions(context.Background(), "/d/dep/pw", 5)
	if err != nil {
		t.Fatalf("CredentialVersions: %v", err)
	}

	if len(got) != 2 || got[0].ID != "p2" || got[1].ID != "p1" || got[0].Type != "password" || got[0].CreatedAt.IsZero() {
		t.Fatalf("unexpected versions %+v", got)
	}

	text := fmt.Sprintf("%+v", got)
	if strings.Contains(text, "hunter2") || strings.Contains(text, "SECRET2") {
		t.Fatalf("a value survived in %s", text)
	}

	query := fake.lastQuery(t)
	if query.Get("name") != "/d/dep/pw" || query.Get("versions") != "5" {
		t.Fatalf("unexpected query %v", query)
	}
}

func TestCredentialVersionsClampsLimit(t *testing.T) {
	t.Parallel()

	fake := newReadServer(t, answer(`{"data":[]}`))
	client := fake.client(t)

	for limit, want := range map[int]string{0: "1", -3: "1", 500: "20", 7: "7"} {
		_, err := client.CredentialVersions(context.Background(), "/d/dep/pw", limit)
		if err != nil {
			t.Fatalf("CredentialVersions(%d): %v", limit, err)
		}

		if got := fake.lastQuery(t).Get("versions"); got != want {
			t.Fatalf("limit %d sent versions=%s, want %s", limit, got, want)
		}
	}
}

func TestCredentialVersionsNotFound(t *testing.T) {
	t.Parallel()

	fake := newReadServer(t, func(*http.Request) (int, string) { return http.StatusNotFound, `{"error":"` + fakeKeyText + `"}` })

	_, err := fake.client(t).CredentialVersions(context.Background(), "/d/dep/pw", 1)
	if !errors.Is(err, credhub.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}

	if strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("error carries server text: %v", err)
	}
}

const keyBearingAnswer = `{"id":"v1","name":"/d/dep/redis_crt","version_created_at":"2026-10-01T00:00:00Z",
"value":{"certificate":"CERT-PEM","ca":"CA-PEM","private_key":"` + fakeKeyText + `"}}`

func TestCertificateVersionNeverRetainsKey(t *testing.T) {
	t.Parallel()

	fake := newReadServer(t, answer(keyBearingAnswer))

	got, err := fake.client(t).CertificateVersion(context.Background(), "v1")
	if err != nil {
		t.Fatalf("CertificateVersion: %v", err)
	}

	if got.VersionID != "v1" || got.Name != redisCrt || got.Certificate != "CERT-PEM" || got.CA != "CA-PEM" || got.CreatedAt.IsZero() {
		t.Fatalf("unexpected answer %+v", got)
	}

	text := fmt.Sprintf("%+v", got)
	if strings.Contains(text, "SECRET") || strings.Contains(text, "PRIVATE KEY") {
		t.Fatalf("a key survived in %s", text)
	}

	if fake.paths[0] != "/api/v1/data/v1" {
		t.Fatalf("unexpected path %v", fake.paths)
	}
}

func TestCertificateVersionDecodeErrorHidesBody(t *testing.T) {
	t.Parallel()

	fake := newReadServer(t, answer(`{"value": "`+fakeKeyText+`"}`))

	_, err := fake.client(t).CertificateVersion(context.Background(), "v1")
	if err == nil {
		t.Fatal("expected an error")
	}

	if !strings.Contains(err.Error(), "*json.UnmarshalTypeError") || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("unexpected error text %q", err.Error())
	}
}

func TestCertificateVersionTruncatedAnswerHidesBody(t *testing.T) {
	t.Parallel()

	fake := newReadServer(t, answer(keyBearingAnswer[:strings.Index(keyBearingAnswer, "RSA")+5]))

	_, err := fake.client(t).CertificateVersion(context.Background(), "v1")
	if err == nil {
		t.Fatal("expected an error")
	}

	if strings.Contains(err.Error(), "PRIVATE KEY") || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("error carries the body: %q", err.Error())
	}
}

func TestCertificateVersionBodyOverLimit(t *testing.T) {
	t.Parallel()

	fake := newReadServer(t, answer(`{"value":{"private_key":"`+strings.Repeat("SECRET", 12<<10)+`"}}`))

	_, err := fake.client(t).CertificateVersion(context.Background(), "v1")
	if !errors.Is(err, credhub.ErrAnswerTooLarge) {
		t.Fatalf("err = %v, want ErrAnswerTooLarge", err)
	}

	if !strings.Contains(err.Error(), "65536") || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("unexpected error text %q", err.Error())
	}
}

func TestOpaqueErrorDropsServerText(t *testing.T) {
	t.Parallel()

	fake := newReadServer(t, func(*http.Request) (int, string) {
		return http.StatusInternalServerError, `{"error":"` + fakeKeyText + `"}`
	})
	client := fake.client(t)

	_, versionErr := client.CertificateVersion(context.Background(), "v1")
	_, listErr := client.CredentialVersions(context.Background(), "/d/dep/pw", 1)

	for name, err := range map[string]error{"CertificateVersion": versionErr, "CredentialVersions": listErr} {
		var apiErr *credhub.APIError
		if !errors.As(err, &apiErr) || apiErr.Status != http.StatusInternalServerError || apiErr.Message != "" {
			t.Fatalf("%s: err = %#v, want an APIError 500 with no message", name, err)
		}

		if strings.Contains(err.Error(), "SECRET") {
			t.Fatalf("%s: error carries server text: %v", name, err)
		}
	}
}

func TestCleanupStillUsesPathQuery(t *testing.T) {
	t.Parallel()

	fake := newFakeCredHub(t, func(int, *http.Request) (int, string) {
		return http.StatusForbidden, `{"error":"denied"}`
	})
	client := newTestClient(t, fake, &fakeTokens{})

	_, err := client.FindByPath(context.Background(), labPrefix)

	var apiErr *credhub.APIError
	if !errors.As(err, &apiErr) || apiErr.Message != "denied" || apiErr.Status != http.StatusForbidden {
		t.Fatalf("err = %#v, want an APIError 403 with message denied", err)
	}

	seen := fake.requests()
	if len(seen) != 1 || seen[0].query != "path="+url.QueryEscape(labPrefix) {
		t.Fatalf("unexpected requests %+v", seen)
	}
}
