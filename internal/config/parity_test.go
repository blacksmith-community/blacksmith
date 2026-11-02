package config_test

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"blacksmith/internal/config"
	"blacksmith/internal/parity"
)

const (
	kitCloudConfigPath = "/var/vcap/jobs/blacksmith/config/cloud-config.yml"
	cloudConfigToken   = "@CLOUD_CONFIG@"
)

// TestReadConfigParity records the config ReadConfig makes of every sample
// file, and of the config the kit's blacksmith job renders, or the error
// it fails with. ReadConfig exports the Vault address and the BOSH network
// to the environment, so the test restores all three when it ends.
//
//nolint:paralleltest // ReadConfig writes environment variables
func TestReadConfigParity(t *testing.T) {
	testdata, err := filepath.Abs("testdata")
	if err != nil {
		t.Fatal(err)
	}

	cloudConfig := filepath.Join(testdata, "cloud-config.yml")

	var paths []string

	for _, pattern := range []string{"kit/*.conf", "sample/*.yml"} {
		matches, err := filepath.Glob(filepath.Join("testdata", pattern))
		if err != nil || len(matches) == 0 {
			t.Fatalf("list %s: %v (%d found)", pattern, err, len(matches))
		}

		paths = append(paths, matches...)
	}

	sort.Strings(paths)

	for _, path := range paths {
		rel, _ := filepath.Rel("testdata", path)
		name := strings.ReplaceAll(strings.TrimSuffix(rel, filepath.Ext(rel)), string(filepath.Separator), "_")

		t.Run(name, func(t *testing.T) {
			for _, key := range []string{"BOSH_NETWORK", "VAULT_ADDR", "VAULT_TOKEN"} {
				t.Setenv(key, "")
			}

			body, err := os.ReadFile(path) //nolint:gosec // a fixture file
			if err != nil {
				t.Fatal(err)
			}

			text := strings.NewReplacer(kitCloudConfigPath, cloudConfig, cloudConfigToken, cloudConfig).Replace(string(body))
			copyPath := filepath.Join(t.TempDir(), "blacksmith.conf")

			if err := os.WriteFile(copyPath, []byte(text), 0o600); err != nil {
				t.Fatal(err)
			}

			golden := filepath.Join("testdata", "parity", name+".dump")

			cfg, err := config.ReadConfig(copyPath)
			if err != nil {
				recorded := "ERROR: " + strings.ReplaceAll(err.Error(), copyPath, "<config>") + "\n"
				parity.Golden(t, golden, strings.ReplaceAll(recorded, testdata, "<testdata>"))

				return
			}

			recorded := strings.ReplaceAll(parity.Dump(cfg), testdata, "<testdata>")
			recorded += "env\tBOSH_NETWORK\t" + os.Getenv("BOSH_NETWORK") + "\n"
			recorded += "env\tVAULT_ADDR\t" + os.Getenv("VAULT_ADDR") + "\n"
			recorded += "env\tVAULT_TOKEN\t" + os.Getenv("VAULT_TOKEN") + "\n"

			parity.Golden(t, golden, recorded)
		})
	}
}
