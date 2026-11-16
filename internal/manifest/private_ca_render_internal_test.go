package manifest

import (
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A template's (( vault )) lookup has to trust vault.cacert, as every other
// Vault connection of the broker does, and has to read a KV v1 mount as well
// as a KV v2 one.
//
//nolint:paralleltest // GenManifest holds a process-wide lock, and the test sets process environment
func TestGenManifestTrustsThePrivateCAOfVault(t *testing.T) {
	for name, testCase := range map[string]struct{ mount, response string }{
		"KV v2": {
			mount:    `{"data":{"path":"secret/","type":"kv","options":{"version":"2"}}}`,
			response: `{"data":{"data":{"value":"vm-from-vault"},"metadata":{"version":1}}}`,
		},
		"KV v1": {
			mount:    `{"data":{"path":"secret/","type":"kv","options":{"version":"1"}}}`,
			response: `{"data":{"value":"vm-from-vault"}}`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			renderAgainstPrivateCAVault(t, testCase.mount, testCase.response)
		})
	}
}

//nolint:paralleltest // GenManifest holds a process-wide lock, and the test sets process environment
func renderAgainstPrivateCAVault(t *testing.T, mount, response string) {
	t.Helper()

	vaultServer := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, req *http.Request) {
		writer.Header().Set("Content-Type", "application/json")

		if strings.HasPrefix(req.URL.Path, "/v1/sys/internal/ui/mounts") {
			_, _ = writer.Write([]byte(mount))

			return
		}

		_, _ = writer.Write([]byte(response))
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
