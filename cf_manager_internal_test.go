package main

import (
	"testing"

	"blacksmith/internal/config"
	loggerPkg "blacksmith/pkg/logger"
)

// initializeCFManager copies each endpoint's settings into the CF manager,
// so a field dropped there would silently disable the TLS configuration.
//
//nolint:paralleltest // skip_ssl_validation mutates CAPI_DEV_MODE
func TestInitializeCFManagerCarriesTLSSettings(t *testing.T) {
	t.Setenv("CAPI_DEV_MODE", "")

	want := config.CFAPIConfig{
		Name:              "lab",
		Endpoint:          "https://127.0.0.1:1",
		Username:          "blacksmith",
		Password:          "secret",
		CACert:            "-----BEGIN CERTIFICATE-----\nnot-a-certificate\n-----END CERTIFICATE-----\n",
		SkipSSLValidation: true,
	}

	cfg := &config.Config{}
	cfg.Broker.CF.APIs = map[string]config.CFAPIConfig{"lab": want}

	manager := initializeCFManager(cfg, loggerPkg.Get().Named("test"))
	if manager == nil {
		t.Fatal("expected a CF manager when endpoints are configured")
	}

	clients := manager.GetClients()
	if len(clients) != 1 {
		t.Fatalf("expected one endpoint client, got %d", len(clients))
	}

	got := clients[0].GetConfig()

	if got.CACert != want.CACert {
		t.Errorf("cacert not carried into the CF manager: got %q", got.CACert)
	}

	if got.SkipSSLValidation != want.SkipSSLValidation {
		t.Errorf("skip_ssl_validation not carried into the CF manager: got %v", got.SkipSSLValidation)
	}
}
