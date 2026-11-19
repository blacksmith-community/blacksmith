package broker_test

import (
	"context"
	"strings"
	"testing"

	"blacksmith/internal/broker"
)

// A cleanup deletes only after a case has shown that the broker minted the
// name for the instance. A name lookup that returns no name and forgets to
// say why is still a refusal, with a reason of its own, and never a delete
// of the name the caller asked for.
func TestJudgeCleanupClaim_RefusesUnlessOwnershipIsShown(t *testing.T) {
	t.Parallel()

	const instanceID = "cleanup-judge"

	own := standalonePlanID + "-" + instanceID

	cases := []struct {
		name, asked, claimed, planID, reason string
		deletes                              bool
		wantReason                           string
	}{
		{name: "a name minted under the recorded plan", asked: own, claimed: own, planID: standalonePlanID, deletes: true},
		{name: "a name the recorded plan does not mint", asked: own, claimed: own, planID: clusterPlanID, wantReason: "was not named for this instance"},
		{name: "no name and a reason", asked: own, reason: "the director has no task", wantReason: "the director has no task"},
		{name: "no name and no reason", asked: own, wantReason: "could not show that it named a deployment for this instance"},
		{name: "no name, no reason, and no name asked", wantReason: "could not show that it named a deployment for this instance"},
	}

	for _, entry := range cases {
		t.Run(entry.name, func(t *testing.T) {
			t.Parallel()

			judge := &broker.Broker{}

			deployment, deletes := judge.JudgeCleanupClaim(context.Background(), instanceID, entry.asked, entry.claimed, entry.planID, entry.reason)
			attention := judge.CleanupAttention()

			switch {
			case entry.deletes && (!deletes || deployment != entry.claimed || len(attention) != 0):
				t.Fatalf("the cleanup would delete %q (%v) and left %+v, want %s deleted and nothing left", deployment, deletes, attention, entry.claimed)
			case entry.deletes:
				return
			case deletes || deployment != "":
				t.Fatalf("the cleanup would delete %q, want nothing deleted", deployment)
			case len(attention) != 1 || !strings.Contains(attention[0].Reason, entry.wantReason):
				t.Fatalf("the refusal left %+v for an operator, want one row that says %q", attention, entry.wantReason)
			}
		})
	}
}
