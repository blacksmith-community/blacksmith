package config_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"blacksmith/internal/config"
	"gopkg.in/yaml.v2"
)

func TestCFAPIConfigTLSFields(t *testing.T) {
	t.Parallel()

	raw := `
broker:
  cf:
    apis:
      lab:
        name: lab-cf
        endpoint: https://api.system.lab.example.com
        username: blacksmith
        password: secret
        cacert: |
          -----BEGIN CERTIFICATE-----
          MIIBszCCAVmgAwIBAgIUXtest
          -----END CERTIFICATE-----
        skip_ssl_validation: true
      prod:
        name: prod-cf
        endpoint: https://api.system.prod.example.com
        username: blacksmith
        password: secret
`

	var cfg config.Config

	err := yaml.Unmarshal([]byte(raw), &cfg)
	if err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}

	lab, found := cfg.Broker.CF.APIs["lab"]
	if !found {
		t.Fatal("expected the lab CF API entry to be loaded")
	}

	if !strings.HasPrefix(lab.CACert, "-----BEGIN CERTIFICATE-----") {
		t.Errorf("expected cacert to be loaded as PEM, got %q", lab.CACert)
	}

	if !lab.SkipSSLValidation {
		t.Error("expected skip_ssl_validation to be loaded as true")
	}

	prod, found := cfg.Broker.CF.APIs["prod"]
	if !found {
		t.Fatal("expected the prod CF API entry to be loaded")
	}

	if prod.CACert != "" {
		t.Errorf("expected cacert to default to empty, got %q", prod.CACert)
	}

	if prod.SkipSSLValidation {
		t.Error("expected skip_ssl_validation to default to false")
	}
}

func TestReadConfigRejectsVaultAddressWithCredentials(t *testing.T) {
	t.Parallel()

	const fakeVaultPassword = "hunter2-vault"

	tests := []struct {
		name    string
		address string
		want    error
	}{
		{name: "userinfo with password", address: "https://vault-user:" + fakeVaultPassword + "@vault.example.com:8200", want: config.ErrVaultAddressHasCredentials},
		{name: "userinfo without password", address: "https://vault-user@vault.example.com:8200", want: config.ErrVaultAddressHasCredentials},
		{name: "unparseable address", address: "https://vault-user:" + fakeVaultPassword + "@vault example.com:bad", want: config.ErrVaultAddressInvalid},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			raw := "vault:\n  address: \"" + testCase.address + "\"\nbosh:\n  address: https://10.0.0.6:25555\n  username: admin\n  password: bosh-secret\n"
			path := filepath.Join(t.TempDir(), "blacksmith.yml")

			err := os.WriteFile(path, []byte(raw), 0o600)
			if err != nil {
				t.Fatalf("write config: %v", err)
			}

			_, err = config.ReadConfig(path)
			if !errors.Is(err, testCase.want) {
				t.Fatalf("expected %v, got %v", testCase.want, err)
			}

			if strings.Contains(err.Error(), fakeVaultPassword) {
				t.Errorf("error message carries the vault password: %q", err.Error())
			}
		})
	}
}

const (
	credhubSecretMarker      = "credhub-secret-marker-do-not-print" //nolint:gosec // fake secret the tests prove stays out of problems
	directorNameCharsProblem = "credhub.director_name must not contain '/', '*', or '%'"
)

func testCredHubCA(t *testing.T) string {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-credhub-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}

	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func validCredHubConfig(t *testing.T) config.CredHubConfig {
	t.Helper()

	return config.CredHubConfig{
		URL:          "https://10.0.0.6:8844",
		CACert:       testCredHubCA(t),
		ClientID:     "blacksmith_credhub",
		ClientSecret: credhubSecretMarker,
		DirectorName: "ocfp-cf1-lab-ocf-bosh",
		Cleanup:      config.CredHubCleanupConfig{Enabled: true},
	}
}

func TestCredHubConfigValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*config.CredHubConfig)
		want   []string
	}{
		{name: "every field set", mutate: func(*config.CredHubConfig) {}, want: nil},
		{name: "explicit sweep off", mutate: func(c *config.CredHubConfig) { c.Cleanup.Sweep = config.CredHubSweepOff }, want: nil},
		{name: "sweep dry-run", mutate: func(c *config.CredHubConfig) { c.Cleanup.Sweep = config.CredHubSweepDryRun }, want: nil},
		{name: "sweep delete", mutate: func(c *config.CredHubConfig) { c.Cleanup.Sweep = config.CredHubSweepDelete }, want: nil},
		{name: "https uaa_url", mutate: func(c *config.CredHubConfig) { c.UAAURL = "https://10.0.0.6:8443" }, want: nil},
		{name: "missing url", mutate: func(c *config.CredHubConfig) { c.URL = "" }, want: []string{"credhub.url is required"}},
		{name: "missing ca_cert", mutate: func(c *config.CredHubConfig) { c.CACert = "" }, want: []string{"credhub.ca_cert is required"}},
		{name: "missing client_id", mutate: func(c *config.CredHubConfig) { c.ClientID = "" }, want: []string{"credhub.client_id is required"}},
		{name: "missing client_secret", mutate: func(c *config.CredHubConfig) { c.ClientSecret = "" }, want: []string{"credhub.client_secret is required"}},
		{name: "missing director_name", mutate: func(c *config.CredHubConfig) { c.DirectorName = "" }, want: []string{"credhub.director_name is required"}},
		{name: "http url", mutate: func(c *config.CredHubConfig) { c.URL = "http://10.0.0.6:8844" }, want: []string{"credhub.url must be an https URL"}},
		{name: "url without host", mutate: func(c *config.CredHubConfig) { c.URL = "https://" }, want: []string{"credhub.url must be an https URL"}},
		{name: "ca_cert not PEM", mutate: func(c *config.CredHubConfig) { c.CACert = "not a certificate" }, want: []string{"credhub.ca_cert does not contain a PEM certificate"}},
		{name: "director_name with slash", mutate: func(c *config.CredHubConfig) { c.DirectorName = "lab/bosh" }, want: []string{directorNameCharsProblem}},
		{name: "director_name with star", mutate: func(c *config.CredHubConfig) { c.DirectorName = "lab*" }, want: []string{directorNameCharsProblem}},
		{name: "director_name with percent", mutate: func(c *config.CredHubConfig) { c.DirectorName = "lab%" }, want: []string{directorNameCharsProblem}},
		{name: "http uaa_url", mutate: func(c *config.CredHubConfig) { c.UAAURL = "http://10.0.0.6:8443" }, want: []string{"credhub.uaa_url must be an https URL when it is set"}},
		{name: "unknown sweep", mutate: func(c *config.CredHubConfig) { c.Cleanup.Sweep = "sometimes" }, want: []string{"credhub.cleanup.sweep must be off, dry-run, or delete"}},
		{
			name: "every required field missing",
			mutate: func(c *config.CredHubConfig) {
				*c = config.CredHubConfig{Cleanup: config.CredHubCleanupConfig{Enabled: true}}
			},
			want: []string{
				"credhub.url is required",
				"credhub.ca_cert is required",
				"credhub.client_id is required",
				"credhub.client_secret is required",
				"credhub.director_name is required",
			},
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			cfg := validCredHubConfig(t)
			testCase.mutate(&cfg)

			got := cfg.Validate()
			if !slices.Equal(got, testCase.want) {
				t.Fatalf("Validate() = %q, want %q", got, testCase.want)
			}

			for _, problem := range got {
				if strings.Contains(problem, credhubSecretMarker) {
					t.Errorf("problem carries the client secret: %q", problem)
				}
			}
		})
	}
}

func TestCredHubConfigValidateSkipsDisabledCleanup(t *testing.T) {
	t.Parallel()

	if problems := (config.CredHubConfig{}).Validate(); len(problems) != 0 {
		t.Fatalf("a missing block must have no problems, got %q", problems)
	}

	disabled := config.CredHubConfig{URL: "http://wrong", Cleanup: config.CredHubCleanupConfig{Sweep: "sometimes"}}
	if problems := disabled.Validate(); len(problems) != 0 {
		t.Fatalf("a disabled block must not be validated, got %q", problems)
	}
}

func TestCredHubConfigSweepMode(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"":                        config.CredHubSweepOff,
		config.CredHubSweepOff:    config.CredHubSweepOff,
		config.CredHubSweepDryRun: config.CredHubSweepDryRun,
		config.CredHubSweepDelete: config.CredHubSweepDelete,
	}

	for sweep, want := range tests {
		got := config.CredHubCleanupConfig{Sweep: sweep}.SweepMode()
		if got != want {
			t.Errorf("SweepMode() for %q = %q, want %q", sweep, got, want)
		}
	}
}

func writeCredHubTestConfig(t *testing.T, credhubBlock string) config.Config {
	t.Helper()

	raw := "vault:\n  address: https://vault.example.com:8200\nbosh:\n  address: https://10.0.0.6:25555\n  username: admin\n  password: bosh-secret\n" + credhubBlock
	path := filepath.Join(t.TempDir(), "blacksmith.yml")

	err := os.WriteFile(path, []byte(raw), 0o600)
	if err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := config.ReadConfig(path)
	if err != nil {
		t.Fatalf("ReadConfig: %v", err)
	}

	return cfg
}

func TestCredHubConfigReadConfig(t *testing.T) {
	t.Parallel()

	t.Run("missing block leaves cleanup disabled", func(t *testing.T) {
		t.Parallel()

		cfg := writeCredHubTestConfig(t, "")
		if cfg.CredHub.Cleanup.Enabled {
			t.Fatal("expected cleanup to be disabled when the credhub block is missing")
		}

		if problems := cfg.CredHub.Validate(); len(problems) != 0 {
			t.Fatalf("expected no problems, got %q", problems)
		}

		if cfg.CredHub.Cleanup.Sweep != config.CredHubSweepOff {
			t.Fatalf("expected the sweep to default to off, got %q", cfg.CredHub.Cleanup.Sweep)
		}
	})

	t.Run("empty sweep becomes off and fields load", func(t *testing.T) {
		t.Parallel()

		block := "credhub:\n  url: https://10.0.0.6:8844\n  client_id: blacksmith_credhub\n  client_secret: " + credhubSecretMarker +
			"\n  director_name: ocfp-cf1-lab-ocf-bosh\n  uaa_url: https://10.0.0.6:8443\n  cleanup:\n    enabled: true\n    protected_deployments:\n    - ocfp-cf1-lab-ocf-blacksmith\n"
		cfg := writeCredHubTestConfig(t, block)

		if !cfg.CredHub.Cleanup.Enabled {
			t.Fatal("expected cleanup to be enabled")
		}

		if cfg.CredHub.Cleanup.Sweep != config.CredHubSweepOff {
			t.Fatalf("expected an empty sweep to become off, got %q", cfg.CredHub.Cleanup.Sweep)
		}

		if cfg.CredHub.URL != "https://10.0.0.6:8844" || cfg.CredHub.ClientID != "blacksmith_credhub" ||
			cfg.CredHub.ClientSecret != credhubSecretMarker || cfg.CredHub.DirectorName != "ocfp-cf1-lab-ocf-bosh" ||
			cfg.CredHub.UAAURL != "https://10.0.0.6:8443" {
			t.Fatal("credhub fields did not load as written")
		}

		if !slices.Equal(cfg.CredHub.Cleanup.ProtectedDeployments, []string{"ocfp-cf1-lab-ocf-blacksmith"}) {
			t.Fatalf("unexpected protected deployments %q", cfg.CredHub.Cleanup.ProtectedDeployments)
		}
	})

	t.Run("invalid block does not fail ReadConfig", func(t *testing.T) {
		t.Parallel()

		cfg := writeCredHubTestConfig(t, "credhub:\n  url: http://10.0.0.6:8844\n  cleanup:\n    enabled: true\n    sweep: sometimes\n")
		if len(cfg.CredHub.Validate()) == 0 {
			t.Fatal("expected the invalid block to report problems")
		}
	})
}
