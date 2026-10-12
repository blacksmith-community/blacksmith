package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"blacksmith/internal/bosh/boshtest"
	"blacksmith/internal/config"
)

// boshEnvironment returns the BOSH CLI variables the broker no longer
// exports, because nothing in it reads them.
func boshEnvironment() []string {
	return []string{"BOSH_ENVIRONMENT", "BOSH_CLIENT", "BOSH_CLIENT_SECRET", "BOSH_CA_CERT"}
}

// clearEnvironment unsets each variable for the rest of the test, and puts
// back the value it had, if any, when the test ends.
func clearEnvironment(t *testing.T, names ...string) {
	t.Helper()

	for _, name := range names {
		t.Setenv(name, "")

		err := os.Unsetenv(name)
		if err != nil {
			t.Fatalf("unset %s: %v", name, err)
		}
	}
}

// keepEnvironment puts each variable back as it was when the test ends, for
// the variables the configuration loader still exports.
func keepEnvironment(t *testing.T, names ...string) {
	t.Helper()

	for _, name := range names {
		value, set := os.LookupEnv(name)
		t.Setenv(name, value)

		if !set {
			err := os.Unsetenv(name)
			if err != nil {
				t.Fatalf("unset %s: %v", name, err)
			}
		}
	}
}

// Loading the configuration and booting the BOSH layer put none of the
// director's address, credentials, or CA into the environment, where any
// child process the broker starts would inherit the client secret.
//
//nolint:paralleltest // the test reads and restores the process environment
func TestBOSHCredentialsStayOutOfTheEnvironment(t *testing.T) {
	clearEnvironment(t, boshEnvironment()...)
	keepEnvironment(t, "BOSH_NETWORK", "VAULT_ADDR", "VAULT_TOKEN")

	director := boshtest.New(t, nil)

	raw := "vault:\n  address: https://vault.example.com\nbosh:\n  address: " + director.URL +
		"\n  username: " + bootCredential + "\n  password: bosh-secret\n  cacert: |\n" + indent(director.CACert, "    ")
	path := filepath.Join(t.TempDir(), "blacksmith.yml")

	err := os.WriteFile(path, []byte(raw), 0o600)
	if err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := config.ReadConfig(path)
	if err != nil {
		t.Fatalf("ReadConfig: %v", err)
	}

	pool, batch := initializeBOSH(&cfg, newShutdownLog())
	if pool == nil || batch == nil {
		t.Fatal("expected the BOSH layer to boot with the configured director")
	}

	t.Cleanup(pool.Stop)
	t.Cleanup(batch.Stop)

	for _, name := range boshEnvironment() {
		if _, set := os.LookupEnv(name); set {
			t.Errorf("booting set %s, want it left unset", name)
		}
	}
}

// indent puts prefix before every line of text, for a YAML block scalar.
func indent(text, prefix string) string {
	return prefix + strings.ReplaceAll(strings.TrimSuffix(text, "\n"), "\n", "\n"+prefix) + "\n"
}
