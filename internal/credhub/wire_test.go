package credhub_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/config"
	"blacksmith/internal/credhub"
)

const (
	wireUAAURL     = "https://10.0.0.6:8443"
	wireInfoName   = "ocfp-cf1-lab-ocf-bosh-info"
	wireCredHubURL = "https://10.0.0.6:8844" //nolint:gosec // a CredHub address, not a credential
	emptyFind      = `{"credentials":[]}`
)

var errInfoDown = errors.New("director answered 502")

// wireDirector answers /info with a configurable UAA URL and counts the calls.
type wireDirector struct {
	*fakeDirector

	infoCalls atomic.Int64
	uaaURL    string
}

func newWireDirector(uaaURL string) *wireDirector {
	return &wireDirector{fakeDirector: newFakeDirector(), uaaURL: uaaURL}
}

func (d *wireDirector) GetInfo() (*bosh.Info, error) {
	d.infoCalls.Add(1)

	info, err := d.fakeDirector.GetInfo()
	if err != nil {
		return nil, err
	}

	info.Name = wireInfoName
	info.UAAURL = d.uaaURL

	return info, nil
}

func wirePlans() []string { return []string{"valkey-standalone"} }

// validWireConfig is an enabled, valid credhub block whose CredHub and UAA
// are never contacted.
func validWireConfig(t *testing.T) config.CredHubConfig {
	t.Helper()

	return config.CredHubConfig{
		URL:          wireCredHubURL,
		CACert:       unrelatedCA(t),
		ClientID:     testClientID,
		ClientSecret: secretMarker,
		DirectorName: labDirector,
		Cleanup: config.CredHubCleanupConfig{
			Enabled:              true,
			Sweep:                config.CredHubSweepOff,
			ProtectedDeployments: []string{labBroker},
		},
	}
}

func assertNotBuilt(t *testing.T, cleaner *credhub.Cleaner, client *credhub.Client, problems []string) {
	t.Helper()

	if cleaner != nil || client != nil {
		t.Fatalf("expected no cleaner and no client, got %v and %v", cleaner, client)
	}

	if len(problems) == 0 {
		t.Fatal("expected at least one problem")
	}

	assertNoMarkers(t, strings.Join(problems, "\n"))
}

func TestBuildCleanerDisabledConfig(t *testing.T) {
	t.Parallel()

	cfg := validWireConfig(t)
	cfg.Cleanup.Enabled = false
	director := newWireDirector(wireUAAURL)

	cleaner, client, problems := credhub.BuildCleaner(cfg, unrelatedCA(t), director, wirePlans, newCaptureLogger())
	assertNotBuilt(t, cleaner, client, problems)

	if director.infoCalls.Load() != 0 {
		t.Fatalf("expected no /info call for a disabled config, got %d", director.infoCalls.Load())
	}
}

func TestBuildCleanerInvalidConfigListsEveryProblem(t *testing.T) {
	t.Parallel()

	cfg := validWireConfig(t)
	cfg.URL = "http://10.0.0.6:8844"
	cfg.ClientID = ""
	cfg.DirectorName = "lab/*"
	director := newWireDirector(wireUAAURL)

	cleaner, client, problems := credhub.BuildCleaner(cfg, unrelatedCA(t), director, wirePlans, newCaptureLogger())
	assertNotBuilt(t, cleaner, client, problems)

	joined := strings.Join(problems, "\n")
	for _, want := range []string{"credhub.url", "credhub.client_id", "credhub.director_name"} {
		if !strings.Contains(joined, want) {
			t.Errorf("expected a problem naming %s, got:\n%s", want, joined)
		}
	}

	if director.infoCalls.Load() != 0 {
		t.Fatalf("expected no /info call for an invalid config, got %d", director.infoCalls.Load())
	}
}

func TestBuildCleanerInfoFailureWithoutUAAURL(t *testing.T) {
	t.Parallel()

	director := newWireDirector(wireUAAURL)
	director.infoErr = errInfoDown

	cleaner, client, problems := credhub.BuildCleaner(validWireConfig(t), unrelatedCA(t), director, wirePlans, newCaptureLogger())
	assertNotBuilt(t, cleaner, client, problems)

	if !strings.Contains(problems[0], "credhub.uaa_url is empty") || !strings.Contains(problems[0], errInfoDown.Error()) {
		t.Fatalf("expected the problem to name the empty uaa_url and the /info error, got %q", problems[0])
	}

	if director.infoCalls.Load() != 1 {
		t.Fatalf("expected exactly one /info call, got %d", director.infoCalls.Load())
	}
}

func TestBuildCleanerInfoWithoutUAA(t *testing.T) {
	t.Parallel()

	director := newWireDirector("")

	cleaner, client, problems := credhub.BuildCleaner(validWireConfig(t), unrelatedCA(t), director, wirePlans, newCaptureLogger())
	assertNotBuilt(t, cleaner, client, problems)

	if !strings.Contains(problems[0], "names no UAA") {
		t.Fatalf("expected the problem to say /info names no UAA, got %q", problems[0])
	}
}

func TestBuildCleanerExplicitUAAURLSkipsInfo(t *testing.T) {
	t.Parallel()

	cfg := validWireConfig(t)
	cfg.UAAURL = wireUAAURL
	director := newWireDirector("")
	director.infoErr = errInfoDown

	cleaner, client, problems := credhub.BuildCleaner(cfg, unrelatedCA(t), director, wirePlans, newCaptureLogger())
	if len(problems) != 0 || cleaner == nil || client == nil {
		t.Fatalf("expected a cleaner and a client, got problems %v", problems)
	}

	if director.infoCalls.Load() != 0 {
		t.Fatalf("expected an explicit uaa_url to skip /info, got %d calls", director.infoCalls.Load())
	}
}

func TestBuildCleanerMissingBOSHCACert(t *testing.T) {
	t.Parallel()

	cleaner, client, problems := credhub.BuildCleaner(validWireConfig(t), "", newWireDirector(wireUAAURL), wirePlans, newCaptureLogger())
	assertNotBuilt(t, cleaner, client, problems)

	if !strings.Contains(problems[0], "bosh.cacert") {
		t.Fatalf("expected the problem to name bosh.cacert, got %q", problems[0])
	}
}

func TestBuildCleanerNoDirectorOrCatalog(t *testing.T) {
	t.Parallel()

	cleaner, client, problems := credhub.BuildCleaner(validWireConfig(t), unrelatedCA(t), nil, nil, newCaptureLogger())
	assertNotBuilt(t, cleaner, client, problems)

	if len(problems) != 2 {
		t.Fatalf("expected one problem for the director and one for the catalog, got %v", problems)
	}
}

// A valid config builds a cleaner and a client without calling UAA or
// CredHub, and the client refuses the broker's own variables under both the
// configured and the reported director name.
func TestBuildCleanerValidConfig(t *testing.T) {
	t.Parallel()

	uaa := newFakeUAA(t, nil)
	fake := newFakeCredHub(t, func(int, *http.Request) (int, string) { return http.StatusOK, emptyFind })

	cfg := validWireConfig(t)
	cfg.URL = fake.server.URL
	cfg.CACert = serverCA(fake.server)
	director := newWireDirector(uaa.server.URL)

	cleaner, client, problems := credhub.BuildCleaner(cfg, serverCA(uaa.server), director, wirePlans, newCaptureLogger())
	if len(problems) != 0 || cleaner == nil || client == nil {
		t.Fatalf("expected a cleaner and a client, got problems %v", problems)
	}

	if director.infoCalls.Load() != 1 {
		t.Fatalf("expected exactly one /info call, got %d", director.infoCalls.Load())
	}

	if uaa.requests.Load() != 0 || len(fake.requests()) != 0 {
		t.Fatalf("expected no UAA or CredHub call while building, got %d and %d", uaa.requests.Load(), len(fake.requests()))
	}

	for _, director := range []string{labDirector, wireInfoName} {
		name := "/" + director + "/" + labBroker + "/blacksmith_services_ca"

		err := client.Delete(context.Background(), name)
		if !errors.Is(err, credhub.ErrProtectedName) {
			t.Errorf("expected Delete of %s to be refused as protected, got %v", name, err)
		}
	}

	if uaa.requests.Load() != 0 || len(fake.requests()) != 0 {
		t.Fatalf("expected the protected deletes to send nothing, got %d UAA and %d CredHub requests", uaa.requests.Load(), len(fake.requests()))
	}
}

// The probe lists a path that never exists and logs that cleanup is on.
func TestProbeSucceeds(t *testing.T) {
	t.Parallel()

	uaa := newFakeUAA(t, nil)
	fake := newFakeCredHub(t, func(int, *http.Request) (int, string) { return http.StatusOK, emptyFind })

	cfg := validWireConfig(t)
	cfg.URL = fake.server.URL
	cfg.CACert = serverCA(fake.server)
	cfg.UAAURL = uaa.server.URL

	log := newCaptureLogger()

	_, client, problems := credhub.BuildCleaner(cfg, serverCA(uaa.server), newWireDirector(""), wirePlans, log)
	if len(problems) != 0 {
		t.Fatalf("unexpected problems %v", problems)
	}

	err := credhub.Probe(context.Background(), client, labDirector, log)
	if err != nil {
		t.Fatalf("expected the probe to succeed, got %v", err)
	}

	requests := fake.requests()
	if len(requests) != 1 || requests[0].method != http.MethodGet || requests[0].query != "path=%2F"+labDirector+"%2Fblacksmith-credhub-probe%2F" {
		t.Fatalf("expected one find of the probe path, got %+v", requests)
	}

	if len(log.matching("CredHub cleanup enabled for director "+labDirector+", probe succeeded")) != 1 {
		t.Fatalf("expected the success line, log was:\n%s", log.output())
	}

	assertNoMarkers(t, log.output())
}

// A probe against a UAA that never answers gives up at its deadline and logs
// an operator-grade failure that carries no secret.
func TestProbeReturnsAtItsDeadlineWhenUAAHangs(t *testing.T) {
	t.Parallel()

	uaa := newFakeUAA(t, func(u *fakeUAA) { u.block = make(chan struct{}) })
	fake := newFakeCredHub(t, func(int, *http.Request) (int, string) { return http.StatusOK, emptyFind })

	cfg := validWireConfig(t)
	cfg.URL = fake.server.URL
	cfg.CACert = serverCA(fake.server)
	cfg.UAAURL = uaa.server.URL

	log := newCaptureLogger()

	_, client, problems := credhub.BuildCleaner(cfg, serverCA(uaa.server), newWireDirector(""), wirePlans, log)
	if len(problems) != 0 {
		t.Fatalf("unexpected problems %v", problems)
	}

	started := time.Now()
	err := credhub.ProbeWithin(context.Background(), client, labDirector, log, 200*time.Millisecond)
	elapsed := time.Since(started)

	if err == nil {
		t.Fatal("expected the probe to fail against a UAA that never answers")
	}

	if elapsed > 2*time.Second {
		t.Fatalf("expected the probe to return at its 200ms deadline, it took %s", elapsed)
	}

	if len(fake.requests()) != 0 {
		t.Fatalf("expected no CredHub request without a token, got %d", len(fake.requests()))
	}

	failures := log.matching("ERROR", "startup probe", "/"+labDirector+"/blacksmith-credhub-probe/", "likely cause", "Cleanup stays enabled")
	if len(failures) != 1 {
		t.Fatalf("expected one operator-grade failure line, log was:\n%s", log.output())
	}

	assertNoMarkers(t, log.output())
	assertNoMarkers(t, err.Error())
}

func TestProbeTimeoutIsThirtySeconds(t *testing.T) {
	t.Parallel()

	if credhub.ProbeTimeout != 30*time.Second {
		t.Fatalf("expected a 30-second probe deadline, got %s", credhub.ProbeTimeout)
	}
}
