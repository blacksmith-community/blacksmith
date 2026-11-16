package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"blacksmith/internal/broker"
	internalCF "blacksmith/internal/cf"
	"blacksmith/internal/config"
	loggerPkg "blacksmith/pkg/logger"
)

// Without a BOSH director the broker has no reconciler, so setup hands back
// nil and the callers skip it rather than start an empty adapter.
func TestSetupReconcilerReturnsNilWithoutBOSH(t *testing.T) {
	t.Parallel()

	got := setupReconciler(&config.Config{}, nil, nil, nil, nil, loggerPkg.Get().Named("test"))
	if got != nil {
		t.Fatalf("setupReconciler without a director returned %+v, want nil", got)
	}
}

// A nil adapter must reach the API as a nil interface. A nil pointer inside a
// non-nil interface would pass the handler's nil check and then fail on the
// first settings request.
func TestAPIReconcilerIsNilWithoutAnAdapter(t *testing.T) {
	t.Parallel()

	if got := apiReconciler(nil); got != nil {
		t.Fatalf("apiReconciler(nil) = %#v, want a nil interface", got)
	}
}

// A nil monitor must reach the API as a nil interface. A nil pointer inside
// a non-nil interface would pass the status handler's nil check and then
// panic on the first call.
func TestAPIVMMonitorIsNilWithoutAMonitor(t *testing.T) {
	t.Parallel()

	if got := apiVMMonitor(nil); got != nil {
		t.Fatalf("apiVMMonitor(nil) = %#v, want a nil interface", got)
	}
}

// The API built with no VM monitor answers its status route with the
// monitor-not-configured body. A nil monitor pointer wrapped in the
// interface at the call site would panic on the first call instead.
//
//nolint:paralleltest // the in-process vault is heavy enough to run alone
func TestAPIWithoutAVMMonitorAnswersItsStatusRoute(t *testing.T) {
	vault := startShutdownVault(t)
	logger := newShutdownLog()
	cfg := &config.Config{Broker: config.BrokerConfig{Username: "operator", Password: "secret"}}
	brokerInstance := &broker.Broker{Vault: vault, Config: cfg}

	handler, upgradeHandler, _ := createAPIHandler(cfg, brokerInstance, vault, nil, nil, internalCF.NewManagerFromExternal(nil, logger),
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, logger)
	t.Cleanup(upgradeHandler.Stop)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/b/bosh/vm-monitor", nil)
	req.SetBasicAuth("operator", "secret")

	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "VM monitor not configured") {
		t.Fatalf("got %d %s, want 200 with the monitor-not-configured body", recorder.Code, recorder.Body.String())
	}
}
