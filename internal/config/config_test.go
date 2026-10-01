package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
