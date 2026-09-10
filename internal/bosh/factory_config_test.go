package bosh_test

import (
	"testing"

	. "blacksmith/internal/bosh"
)

// TestCreateBasicFactoryConfigCarriesCACert verifies the configured CA reaches
// the director HTTP client, not only the UAA client.
func TestCreateBasicFactoryConfigCarriesCACert(t *testing.T) {
	t.Parallel()

	const caCert = "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n"

	factoryConfig := CreateBasicFactoryConfig(Config{
		Address: "https://10.0.0.5:25555",
		CACert:  caCert,
	})

	if factoryConfig.Host != "10.0.0.5" {
		t.Fatalf("host = %q, want 10.0.0.5", factoryConfig.Host)
	}

	if factoryConfig.Port != 25555 {
		t.Fatalf("port = %d, want 25555", factoryConfig.Port)
	}

	if factoryConfig.CACert != caCert {
		t.Fatalf("CACert not carried into FactoryConfig: %q", factoryConfig.CACert)
	}
}
