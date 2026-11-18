package broker_test

import (
	"testing"

	"blacksmith/internal/bosh"
	"blacksmith/internal/broker"
	"blacksmith/pkg/testutil"
)

// The cleanup of a failed deployment deletes the deployment, which is right
// only for a provision that never produced a usable instance. Any other
// action is refused before the director is asked anything, so a failed
// update or deprovision can never delete a tenant's deployment.
func TestCleanupFailedDeployment_RefusesNonProvision(t *testing.T) {
	t.Parallel()

	const deploymentName = "redis-standalone-cleanup-guard"

	for _, action := range []string{"update", outcomeDeprovision, "", "unknown"} {
		t.Run("refuses "+action, func(t *testing.T) {
			t.Parallel()

			director := testutil.NewScriptedBOSHDirector()
			director.GetDeploymentFn = func(name string) (*bosh.DeploymentDetail, error) {
				return &bosh.DeploymentDetail{Name: name}, nil
			}
			director.DeleteDeploymentFn = func(string) (*bosh.Task, error) {
				return &bosh.Task{ID: 1}, nil
			}

			(&broker.Broker{BOSH: director}).CleanupFailedDeployment(t.Context(), action, deploymentName)

			if calls := director.Calls("DeleteDeployment"); len(calls) != 0 {
				t.Errorf("a cleanup for action %q deleted %v", action, calls)
			}

			if calls := director.Calls("GetDeployment"); len(calls) != 0 {
				t.Errorf("a cleanup for action %q asked the director for %v", action, calls)
			}
		})
	}

	t.Run("deletes for provision", func(t *testing.T) {
		t.Parallel()

		director := testutil.NewScriptedBOSHDirector()
		director.GetDeploymentFn = func(name string) (*bosh.DeploymentDetail, error) {
			return &bosh.DeploymentDetail{Name: name}, nil
		}
		director.DeleteDeploymentFn = func(string) (*bosh.Task, error) {
			return &bosh.Task{ID: 1}, nil
		}

		(&broker.Broker{BOSH: director}).CleanupFailedDeployment(t.Context(), "provision", deploymentName)

		if calls := director.Calls("DeleteDeployment"); len(calls) != 1 || calls[0] != deploymentName {
			t.Errorf("expected the failed provision's deployment to be deleted once, got %v", calls)
		}
	})
}
