package main

import (
	"testing"

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
