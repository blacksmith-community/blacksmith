package manifest_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"blacksmith/internal/manifest"
)

// initEnvValue returns the value of VAULT_CACERT in the init script
// environment, and whether it is set.
func initEnvValue() (string, bool) {
	prefix := "VAULT_CACERT="
	value, found := "", false

	for _, entry := range manifest.BuildInitEnvironment("inst", "plan") {
		if strings.HasPrefix(entry, prefix) {
			value, found = strings.TrimPrefix(entry, prefix), true
		}
	}

	return value, found
}

// The init scripts reach Vault through safe, which reads VAULT_CACERT as the
// path of a PEM file. A configured path is handed to each script's own
// environment, and the broker's environment is left alone.
func TestInitEnvironmentCarriesTheVaultCAPath(t *testing.T) { //nolint:paralleltest // sets package state
	t.Setenv("VAULT_CACERT", "")

	cleanup, err := manifest.SetVaultCACert("/etc/blacksmith/vault-ca.pem")
	if err != nil {
		t.Fatalf("SetVaultCACert: %v", err)
	}

	t.Cleanup(cleanup)

	got, found := initEnvValue()
	if !found || got != "/etc/blacksmith/vault-ca.pem" {
		t.Errorf("VAULT_CACERT = %q (set %t), want the configured path", got, found)
	}

	if os.Getenv("VAULT_CACERT") != "" {
		t.Error("the broker's own environment was changed")
	}
}

// PEM text goes to a private file, since safe needs a path, and the file is
// removed by the cleanup.
func TestInitEnvironmentWritesPEMTextToAPrivateFile(t *testing.T) { //nolint:paralleltest // sets package state
	t.Setenv("VAULT_CACERT", "")

	const pemText = "-----BEGIN CERTIFICATE-----\nbm90IGEgcmVhbCBjZXJ0\n-----END CERTIFICATE-----\n"

	cleanup, err := manifest.SetVaultCACert(pemText)
	if err != nil {
		t.Fatalf("SetVaultCACert: %v", err)
	}

	path, found := initEnvValue()
	if !found || strings.Contains(path, "BEGIN") {
		t.Fatalf("VAULT_CACERT = %q (set %t), want a file path", path, found)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("the CA file is missing: %v", err)
	}

	if info.Mode().Perm() != 0o600 {
		t.Errorf("CA file mode = %v, want 0600", info.Mode().Perm())
	}

	dir, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}

	if dir.Mode().Perm() != 0o700 {
		t.Errorf("CA directory mode = %v, want 0700", dir.Mode().Perm())
	}

	data, err := os.ReadFile(path) //nolint:gosec // the path is the one the init environment named
	if err != nil || string(data) != pemText {
		t.Errorf("CA file holds %q (err %v), want the PEM text", data, err)
	}

	cleanup()

	_, err = os.Stat(path)
	if !os.IsNotExist(err) {
		t.Errorf("the CA file remains after cleanup: %v", err)
	}

	after, _ := initEnvValue()
	if after != "" {
		t.Error("VAULT_CACERT is still set after cleanup")
	}
}

// With no CA configured, the init environment leaves VAULT_CACERT as the
// operator set it.
func TestInitEnvironmentLeavesAnOperatorCAAlone(t *testing.T) { //nolint:paralleltest // sets package state
	t.Setenv("VAULT_CACERT", "/operator/ca.pem")

	cleanup, err := manifest.SetVaultCACert("")
	if err != nil {
		t.Fatalf("SetVaultCACert: %v", err)
	}

	t.Cleanup(cleanup)

	got, _ := initEnvValue()
	if got != "/operator/ca.pem" {
		t.Errorf("VAULT_CACERT = %q, want the operator's value", got)
	}
}

// A path with a trailing newline, as a YAML block scalar leaves it, reaches
// the init scripts without the whitespace.
func TestInitEnvironmentTrimsWhitespaceAroundTheVaultCAPath(t *testing.T) { //nolint:paralleltest // sets package state
	t.Setenv("VAULT_CACERT", "")

	cleanup, err := manifest.SetVaultCACert("  /etc/blacksmith/vault-ca.pem\n")
	if err != nil {
		t.Fatalf("SetVaultCACert: %v", err)
	}

	t.Cleanup(cleanup)

	got, _ := initEnvValue()
	if got != "/etc/blacksmith/vault-ca.pem" {
		t.Errorf("VAULT_CACERT = %q, want the path without whitespace", got)
	}
}
