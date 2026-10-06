package vault_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"blacksmith/pkg/vault"

	vapi "github.com/hashicorp/vault/api"
)

type unsealContextKey struct{}

// contextRecorder records the paths of the requests that reached the server,
// split by whether they carried the caller's context value.
type contextRecorder struct {
	mu      sync.Mutex
	carried []string
	lost    []string
}

func (rec *contextRecorder) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		return nil, err //nolint:wrapcheck // a pass-through transport
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()

	if req.Context().Value(unsealContextKey{}) == "caller" {
		rec.carried = append(rec.carried, req.URL.Path)
	} else {
		rec.lost = append(rec.lost, req.URL.Path)
	}

	return resp, nil
}

func newSealedVaultClient(t *testing.T, rec *contextRecorder) *vault.Client {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, req *http.Request) {
		writer.Header().Set("Content-Type", "application/json")

		switch req.URL.Path {
		case "/v1/sys/seal-status":
			_, _ = writer.Write([]byte(`{"sealed":true,"t":1,"n":1,"progress":0}`))
		case "/v1/sys/unseal":
			_, _ = writer.Write([]byte(`{"sealed":false,"t":1,"n":1,"progress":0}`))
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	config := vapi.DefaultConfig()
	config.Address = server.URL
	config.HttpClient = &http.Client{Transport: rec}
	config.MaxRetries = 0

	apiClient, err := vapi.NewClient(config)
	if err != nil {
		t.Fatalf("failed to create the API client: %v", err)
	}

	return &vault.Client{Client: apiClient}
}

// UnsealVault has to make both of its calls under the caller's context, so a
// caller that gives up stops the unseal.
func TestUnsealVaultMakesBothCallsUnderTheCallersContext(t *testing.T) {
	t.Parallel()

	rec := &contextRecorder{}
	client := newSealedVaultClient(t, rec)

	ctx := context.WithValue(context.Background(), unsealContextKey{}, "caller")

	err := client.UnsealVault(ctx, "key")
	if err != nil {
		t.Fatalf("UnsealVault failed: %v", err)
	}

	if len(rec.lost) != 0 {
		t.Fatalf("these requests did not carry the caller's context: %v", rec.lost)
	}

	want := []string{"/v1/sys/seal-status", "/v1/sys/unseal"}
	if len(rec.carried) != len(want) || rec.carried[0] != want[0] || rec.carried[1] != want[1] {
		t.Fatalf("the requests that carried the caller's context were %v, want %v", rec.carried, want)
	}
}

// A caller whose context has ended sends nothing to Vault.
func TestUnsealVaultOnAnEndedContextSendsNothing(t *testing.T) {
	t.Parallel()

	rec := &contextRecorder{}
	client := newSealedVaultClient(t, rec)

	ended, cancel := context.WithCancel(context.Background())
	cancel()

	err := client.UnsealVault(ended, "key")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("UnsealVault returned %v, want an error that wraps context.Canceled", err)
	}

	if len(rec.carried)+len(rec.lost) != 0 {
		t.Fatalf("a request reached Vault: carried %v, lost %v", rec.carried, rec.lost)
	}
}
