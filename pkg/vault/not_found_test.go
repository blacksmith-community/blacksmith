package vault_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"blacksmith/pkg/vault"

	vapi "github.com/hashicorp/vault/api"
)

// guidWith404 is an instance GUID that contains "404". GUIDs are hex, so real
// ones do this, and Vault's error text carries the request URL.
const guidWith404 = "c0ffee40-4404-4abc-9404-0123456789ab"

// newAnsweringVaultClient returns a client for a Vault that answers every
// request with status and body. The client does not retry, so a 5xx answer is
// seen once.
func newAnsweringVaultClient(t *testing.T, status int, body string) *vault.Client {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(status)

		if body != "" {
			_, _ = writer.Write([]byte(body))
		}
	}))
	t.Cleanup(server.Close)

	config := vapi.DefaultConfig()
	config.Address = server.URL
	config.MaxRetries = 0

	apiClient, err := vapi.NewClient(config)
	if err != nil {
		t.Fatalf("failed to create the API client: %v", err)
	}

	apiClient.SetToken("test-token")

	return &vault.Client{Client: apiClient, KVVersion: "2"}
}

// An error whose text contains "404", because the URL does, is still an error
// when Vault did not answer 404.
func TestDeleteAndListReturnAnErrorWhoseTextContains404(t *testing.T) {
	t.Parallel()

	for _, status := range []int{http.StatusInternalServerError, http.StatusForbidden} {
		client := newAnsweringVaultClient(t, status, `{"errors":["something went wrong"]}`)

		err := client.DeleteSecret(context.Background(), guidWith404)
		if err == nil {
			t.Fatalf("DeleteSecret on a %d answer returned no error", status)
		}

		if !strings.Contains(err.Error(), "404") {
			t.Fatalf("the %d error text does not contain 404, so it proves nothing: %v", status, err)
		}

		keys, err := client.ListSecrets(context.Background(), guidWith404+"/")
		if err == nil {
			t.Fatalf("ListSecrets on a %d answer returned no error and keys %v", status, keys)
		}

		if !strings.Contains(err.Error(), "404") {
			t.Fatalf("the %d error text does not contain 404, so it proves nothing: %v", status, err)
		}
	}
}

// A real 404 still reads as "already deleted" and as an empty list, with an
// error body or with none.
func TestDeleteAndListTreatAReal404AsNotFound(t *testing.T) {
	t.Parallel()

	for _, body := range []string{`{"errors":[]}`, ""} {
		client := newAnsweringVaultClient(t, http.StatusNotFound, body)

		err := client.DeleteSecret(context.Background(), guidWith404)
		if err != nil {
			t.Fatalf("DeleteSecret on a 404 with body %q returned %v", body, err)
		}

		keys, err := client.ListSecrets(context.Background(), guidWith404+"/")
		if err != nil {
			t.Fatalf("ListSecrets on a 404 with body %q returned %v", body, err)
		}

		if len(keys) != 0 {
			t.Fatalf("ListSecrets on a 404 with body %q returned keys %v", body, keys)
		}
	}
}
