package deployments_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"blacksmith/internal/apierr"
	"blacksmith/internal/bosh"
	"blacksmith/internal/config"
	"blacksmith/internal/handlers/deployments"
	"blacksmith/internal/middleware"
	"blacksmith/pkg/logger"
	"blacksmith/pkg/testutil"
)

const (
	errandDeployment = "valkey-1"
	errandPath       = "/b/deployments/" + errandDeployment + "/errands/smoke-tests"
)

// slowErrandDirector runs an errand for as long as run says, or until its
// context ends, as a director task that is still going does.
type slowErrandDirector struct {
	*testutil.MockBOSHDirector

	run time.Duration
}

func (d *slowErrandDirector) RunErrand(ctx context.Context, _, _ string, _ bosh.ErrandOpts) (*bosh.ErrandResult, error) {
	select {
	case <-time.After(d.run):
		return &bosh.ErrandResult{InstanceGroup: errandGroup, InstanceID: "abc"}, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("running the errand: %w", context.Cause(ctx))
	}
}

func errandHandler(run time.Duration, writeTimeoutSeconds int) *deployments.Handler {
	return deployments.NewHandler(deployments.Dependencies{
		Logger:   logger.NewNoOpLogger(),
		Config:   writeTimeoutConfig{seconds: writeTimeoutSeconds},
		Director: &slowErrandDirector{MockBOSHDirector: testutil.NewMockBOSHDirector(), run: run},
		Vault:    managingVault(errandDeployment),
	})
}

// An errand that runs past the server's write timeout still answers. The
// server runs the real write timeout and the real logging middleware, and
// the route lifts the connection's write deadline to cover the errand, so
// the client gets the result and not a bare EOF.
func TestErrandRunningPastTheWriteTimeoutStillAnswers(t *testing.T) {
	t.Parallel()

	handler := errandHandler(1500*time.Millisecond, 1)

	server := httptest.NewUnstartedServer(middleware.LoggingMiddleware(logger.NewNoOpLogger())(handler))
	server.Config.WriteTimeout = time.Second
	server.Start()
	t.Cleanup(server.Close)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+errandPath, nil)
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}

	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("the errand's answer never arrived: %v", err)
	}

	defer func() { _ = resp.Body.Close() }()

	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the answer: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, payload)
	}

	var body map[string]interface{}

	err = json.Unmarshal(payload, &body)
	if err != nil || body["errand"] != "smoke-tests" {
		t.Errorf("body = %s (%v), want the errand's result", payload, err)
	}
}

// When the connection's write deadline can't be moved, the wait ends
// before the write timeout instead, and the broker answers 504 with the
// code outcome_unknown, because the errand may still be running.
func TestErrandWaitsLessThanTheWriteTimeoutWhenTheDeadlineCannotMove(t *testing.T) {
	t.Parallel()

	handler := errandHandler(time.Minute, 1)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, errandPath, nil)
	recorder := httptest.NewRecorder()

	start := time.Now()

	handler.ServeHTTP(recorder, req)

	if elapsed := time.Since(start); elapsed >= time.Second {
		t.Errorf("the errand held the request for %v, want less than the 1s write timeout", elapsed)
	}

	if recorder.Code != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want 504: %s", recorder.Code, recorder.Body.String())
	}

	if code := decodeCode(t, recorder); code != apierr.CodeOutcomeUnknown {
		t.Errorf("code = %q, want %q", code, apierr.CodeOutcomeUnknown)
	}
}

// errandTimeoutConfig is a config whose broker.errand_timeout is set.
type errandTimeoutConfig struct {
	writeTimeoutConfig

	timeout time.Duration
}

func (c errandTimeoutConfig) GetBrokerConfig() config.BrokerConfig {
	return config.BrokerConfig{ErrandTimeout: &c.timeout}
}

// The wait for an errand ends at broker.errand_timeout, as the server's
// write deadline moves to match, and the broker answers 504 with the code
// outcome_unknown.
func TestErrandWaitEndsAtTheConfiguredTimeout(t *testing.T) {
	t.Parallel()

	handler := deployments.NewHandler(deployments.Dependencies{
		Logger:   logger.NewNoOpLogger(),
		Config:   errandTimeoutConfig{timeout: 300 * time.Millisecond},
		Director: &slowErrandDirector{MockBOSHDirector: testutil.NewMockBOSHDirector(), run: time.Minute},
		Vault:    managingVault(errandDeployment),
	})

	server := httptest.NewUnstartedServer(handler)
	server.Config.WriteTimeout = time.Second
	server.Start()
	t.Cleanup(server.Close)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+errandPath, nil)
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}

	start := time.Now()

	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("no answer: %v", err)
	}

	defer func() { _ = resp.Body.Close() }()

	if elapsed := time.Since(start); elapsed > 900*time.Millisecond {
		t.Errorf("the wait lasted %v, want about 300ms", elapsed)
	}

	if resp.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want 504", resp.StatusCode)
	}
}
