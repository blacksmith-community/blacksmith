package bosh_test

import (
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	. "blacksmith/internal/bosh"
	"blacksmith/internal/bosh/boshtest"
	"blacksmith/internal/config"
)

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
// task for the configured time. Building them sends no request.
func TestFactoryPassesTaskWatchMaxToBothFollowers(t *testing.T) {
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

// The factory keeps the director client's retries on. A read the director
// answers with 503 is sent four times, the first attempt and three retries,
// before the caller gets the answer. boshapi takes a RetryMax of zero as one
// attempt, so the factory's default of three is what keeps the retries.
func TestFactoryRetriesThreeTimesByDefault(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32

	server := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
		if request.Method != http.MethodGet || request.URL.Path != deploymentsRoute {
			return false
		}

		attempts.Add(1)

		// Retry-After: 0 holds the backoff between attempts to nothing.
		writer.Header().Set("Retry-After", "0")
		boshtest.WriteError(writer, http.StatusServiceUnavailable, 100, "the director is busy")

		return true
	})

	pool, batch, err := CreatePooledAndBatchDirectors(server.URL, basicAuthCredential, basicAuthCredential, server.CACert, false,
		0, 0, 1, 1, time.Minute, nil)
	if err != nil {
		t.Fatalf("CreatePooledAndBatchDirectors: %v", err)
	}

	t.Cleanup(pool.Stop)
	t.Cleanup(batch.Stop)

	_, err = pool.GetDeployments(t.Context())
	if err == nil {
		t.Fatal("expected the 503 to reach the caller once the retries ran out")
	}

	if got := attempts.Load(); got != 4 {
		t.Fatalf("the director got %d attempts at GET /deployments, want 4: the first and three retries", got)
	}
}
