package config_test

import (
	"os"
	"testing"
)

// graft's vault operator and the forge init scripts read VAULT_SKIP_VERIFY
// from the environment, so a config that skips verification exports it.
func TestSkipSSLValidationIsExportedForGraftAndInitScripts(t *testing.T) { //nolint:paralleltest // sets process environment
	t.Setenv("VAULT_SKIP_VERIFY", "")

	readRawWithWarnings(t, "vault:\n  address: https://vault.example.com:8200\n  skip_ssl_validation: true\nbosh:\n  address: https://10.0.0.6:25555\n  username: admin\n  password: bosh-secret\n")

	if got := os.Getenv("VAULT_SKIP_VERIFY"); got != "true" {
		t.Errorf("VAULT_SKIP_VERIFY = %q, want true", got)
	}
}

// A config that verifies leaves the variable as the operator set it.
func TestVerifyingConfigLeavesVaultSkipVerifyAlone(t *testing.T) { //nolint:paralleltest // sets process environment
	t.Setenv("VAULT_SKIP_VERIFY", "operator")

	readRawWithWarnings(t, "vault:\n  address: https://vault.example.com:8200\nbosh:\n  address: https://10.0.0.6:25555\n  username: admin\n  password: bosh-secret\n")

	if got := os.Getenv("VAULT_SKIP_VERIFY"); got != "operator" {
		t.Errorf("VAULT_SKIP_VERIFY = %q, want the operator's value", got)
	}
}
