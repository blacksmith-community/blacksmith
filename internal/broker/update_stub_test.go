package broker_test

import (
	"strings"
	"testing"

	"blacksmith/internal/broker"
	"blacksmith/pkg/logger"

	"github.com/fivetwenty-io/osbapi/v2/pkg/osbapi"
)

// The update request's debug line names the plan the request asks for as
// the new plan, and the plan in its previous values as the previous plan.
//
//nolint:paralleltest // the test swaps the process-wide logger
func TestUpdateLogsEachPlanUnderItsOwnLabel(t *testing.T) {
	capture := newCaptureLogger()
	previous := logger.Get()

	logger.Set(capture)
	t.Cleanup(func() { logger.Set(previous) })

	_, _, _ = (&broker.Broker{}).Update(t.Context(), "instance-1", osbapi.UpdateRequest{
		ServiceID:      "service-1",
		PlanID:         "plan-requested",
		PreviousValues: &osbapi.PreviousValues{PlanID: "plan-before"},
	}, true)

	output := capture.output()

	for _, want := range []string{"NewPlanID: plan-requested", "PreviousPlanID: plan-before"} {
		if !strings.Contains(output, want) {
			t.Errorf("expected the update log to hold %q, got:\n%s", want, output)
		}
	}
}
