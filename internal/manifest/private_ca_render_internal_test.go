package manifest

import (
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A template's (( vault )) lookup has to trust vault.cacert, as every other
// Vault connection of the broker does. graft builds that client from the
// environment and cannot yet be given a CA, so this test waits for the graft
// change that adds one, and then for the render engine to be built with it.
//
//nolint:paralleltest // GenManifest holds a process-wide lock, and the test sets process environment
func TestGenManifestTrustsThePrivateCAOfVault(t *testing.T) {
	t.Skip("pending the graft change that lets a render engine trust a private CA")

	vaultServer := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, req *http.Request) {
		writer.Header().Set("Content-Type", "application/json")

		if strings.HasPrefix(req.URL.Path, "/v1/sys/internal/ui/mounts") {
			_, _ = writer.Write([]byte(`{"data":{"path":"secret/","type":"kv","options":{"version":"2"}}}`))

			return
		}

		_, _ = writer.Write([]byte(`{"data":{"data":{"value":"vm-from-vault"},"metadata":{"version":1}}}`))
	}))
	t.Cleanup(vaultServer.Close)

	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: vaultServer.Certificate().Raw})

	t.Setenv("CREDENTIALS", "")
	t.Setenv("VAULT_ADDR", vaultServer.URL)
	t.Setenv("VAULT_TOKEN", "render-test-token")
	t.Setenv("VAULT_SKIP_VERIFY", "")
	t.Setenv("VAULT_CACERT", "")

	cleanup, err := SetVaultCACert(string(caPEM))
	if err != nil {
		t.Fatalf("SetVaultCACert: %v", err)
	}

	t.Cleanup(cleanup)

	rendered, err := GenManifest(refusedPlan(`(( vault "secret/render/probe:value" ))`), "private-ca-instance")
	if err != nil {
		t.Fatalf("the render failed against a Vault whose CA is configured: %v", err)
	}

	if !strings.Contains(rendered, "vm-from-vault") {
		t.Errorf("the render does not hold the value Vault returned")
	}
}
