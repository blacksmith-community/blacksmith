package parity

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"

	"blacksmith/pkg/testutil"
)

// hostileValues are credential values that are hard on a YAML emitter. The
// plain ones read as another type in some YAML versions, and the rest need
// quoting, escaping, or care around the marker for an operator.
//
//nolint:gochecknoglobals // constant test data
var hostileValues = []string{
	"yes",
	"y",
	"1e3",
	"0755",
	"2026-01-02",
	":leading-colon",
	"-----BEGIN CERTIFICATE-----\nFIXTURE-PLACEHOLDER-NOT-A-REAL-CERTIFICATE\n-----END CERTIFICATE-----\n",
	"value with a ((marker)) inside",
	"fixture-password-not-real",
}

var vaultReference = regexp.MustCompile(`\(\(\s*vault\s+\$CREDENTIALS\s+"(/[^":]+):([^"]+)"\s*\)\)`)

//nolint:gochecknoglobals // the spruce library keeps one Vault client for the life of the process
var (
	sharedVaultOnce sync.Once
	sharedVault     *testutil.VaultDevServer
	sharedVaultErr  error
)

// SharedVault starts one OpenBao dev server for the whole test process and
// points VAULT_ADDR and VAULT_TOKEN at it. The spruce library builds its
// Vault client once per process, so every test in the process has to reach
// the same server. A package that calls it must call CloseSharedVault from
// TestMain.
func SharedVault(t *testing.T) *testutil.VaultDevServer {
	t.Helper()

	sharedVaultOnce.Do(func() {
		sharedVault, sharedVaultErr = testutil.NewVaultDevServer(nil)
		if sharedVaultErr != nil {
			return
		}

		_ = os.Setenv("VAULT_ADDR", sharedVault.Addr)
		_ = os.Setenv("VAULT_TOKEN", sharedVault.RootToken)
	})

	if sharedVaultErr != nil {
		t.Fatalf("start the OpenBao dev server: %s", sharedVaultErr)
	}

	return sharedVault
}

// CloseSharedVault stops the server SharedVault started, if it did.
func CloseSharedVault() {
	if sharedVault != nil {
		sharedVault.Close()
	}
}

// SeedCredentials writes a value for every secret the fixture's plans read
// through `(( vault $CREDENTIALS "/path:key" ))`, under secret/<instanceID>.
// Values come from hostileValues in the order of the sorted references, so
// they are the same on every run.
func SeedCredentials(t *testing.T, server *testutil.VaultDevServer, fixture Fixture, instanceID string) {
	t.Helper()

	references := credentialReferences(t, fixture)
	byPath := map[string]map[string]interface{}{}

	for index, reference := range references {
		path, key, _ := strings.Cut(reference, ":")
		if byPath[path] == nil {
			byPath[path] = map[string]interface{}{}
		}

		byPath[path][key] = hostileValues[index%len(hostileValues)]
	}

	for path, data := range byPath {
		err := server.WriteSecret(instanceID+path, data)
		if err != nil {
			t.Fatalf("seed %s: %s", path, err)
		}
	}
}

// CredentialsPath returns the value CREDENTIALS holds while a plan of the
// instance renders.
func CredentialsPath(instanceID string) string { return "secret/" + instanceID }

// InstanceID returns the instance ID the fixture's secrets are seeded under.
func InstanceID(fixture Fixture) string {
	return fmt.Sprintf("parity-%s-%s", fixture.Forge, fixture.Variant)
}

func credentialReferences(t *testing.T, fixture Fixture) []string {
	t.Helper()

	files, err := globAll(fixture.ServicesDir(), "*/manifest.yml", "*/params.yml", "*/credentials.yml")
	if err != nil {
		t.Fatalf("list plan files: %s", err)
	}

	seen := map[string]bool{}

	for _, file := range files {
		data, err := os.ReadFile(file) //nolint:gosec // a fixture file
		if err != nil {
			t.Fatalf("read %s: %s", file, err)
		}

		for _, match := range vaultReference.FindAllStringSubmatch(string(data), -1) {
			seen[match[1]+":"+match[2]] = true
		}
	}

	references := make([]string, 0, len(seen))
	for reference := range seen {
		references = append(references, reference)
	}

	sort.Strings(references)

	return references
}
