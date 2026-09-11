package config_test

import (
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
