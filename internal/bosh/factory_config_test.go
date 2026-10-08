package bosh_test

import (
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	. "blacksmith/internal/bosh"
	"blacksmith/internal/config"
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

// pemCertificate is the PEM block type of a certificate.
const pemCertificate = "CERTIFICATE"

// writeFactoryConfig writes a Blacksmith configuration whose bosh block
// points at the director given and carries the extra bosh setting, and
// reads it back.
func writeFactoryConfig(t *testing.T, director, setting string) config.Config {
	t.Helper()

	raw := "vault:\n  address: https://vault.example.com\nbosh:\n  address: " + director +
		"\n  username: admin\n  password: bosh-secret\n" + setting
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

// The factory hands bosh.task_watch_max to the pooled director and to the
// batch director, so both followers hold a lost answer and read a silent
// task for the configured time. Building them in test mode, which skips
// the UAA detection, sends no request.
func TestFactoryPassesTaskWatchMaxToBothFollowers(t *testing.T) {
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		t.Errorf("building the directors sent %s %s", request.Method, request.URL.Path)
		writer.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	caPEM := string(pem.EncodeToMemory(&pem.Block{Type: pemCertificate, Bytes: server.Certificate().Raw}))

	cases := []struct {
		name    string
		setting string
		want    time.Duration
	}{
		{name: "set", setting: "  task_watch_max: 60\n", want: time.Minute},
		{name: "unset", setting: "", want: 6 * time.Hour},
	}

	for _, test := range cases {
		cfg := writeFactoryConfig(t, server.URL, test.setting)

		pool, batch, err := CreatePooledAndBatchDirectors(cfg.BOSH.Address, cfg.BOSH.Username, cfg.BOSH.Password, caPEM, false,
			time.Duration(cfg.BOSH.RequestTimeout)*time.Second, time.Duration(cfg.BOSH.TaskWatchMax)*time.Second,
			cfg.BOSH.MaxConnections, cfg.BOSH.MaxBatchConnections, time.Duration(cfg.BOSH.ConnectionTimeout)*time.Second, nil)
		if err != nil {
			t.Fatalf("%s: CreatePooledAndBatchDirectors: %v", test.name, err)
		}

		t.Cleanup(pool.Stop)
		t.Cleanup(batch.Stop)

		if got := PoolWatchMaxOf(pool); got != test.want {
			t.Errorf("%s: the pool's follower uses %v, want %v", test.name, got, test.want)
		}

		if got := BatchWatchMaxOf(batch); got != test.want {
			t.Errorf("%s: the batch director's follower uses %v, want %v", test.name, got, test.want)
		}
	}
}
