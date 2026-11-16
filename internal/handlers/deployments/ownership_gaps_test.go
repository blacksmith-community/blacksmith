package deployments_test

import (
	"net/http"
	"testing"

	"blacksmith/pkg/testutil"
)

// A missing index, as a fresh broker has, manages nothing.
func TestAMissingIndexManagesNoDeployment(t *testing.T) {
	t.Parallel()

	handler, director := ownershipHandler(&testutil.InstanceVault{})

	for _, action := range deploymentActions() {
		recorder := serveAction(t, handler, action)

		if recorder.Code != http.StatusForbidden {
			t.Errorf("%s status = %d, want 403: %s", action.name, recorder.Code, recorder.Body.String())
		}
	}

	if calls, _ := director.recorded(); len(calls) != 0 {
		t.Errorf("director calls = %v, want none", calls)
	}
}
