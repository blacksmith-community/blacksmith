//nolint:testpackage // the test reads the manager's unexported instance list
package upgrade

import "testing"

// An instance's deployment keeps the name it was created under, so the
// name its record holds need not start with the plan the record names. The
// fixture's i1 records plan "small" and deployment "redis-i1". Both the
// upgrade's instance list and the command preview must name the recorded
// deployment, not "small-i1", which the director doesn't have.
func TestDeploymentName_ReadsRecordedName(t *testing.T) {
	t.Parallel()

	t.Run("upgrade instance list", func(t *testing.T) {
		t.Parallel()

		env := newCmdEnv(t, nil)

		instances, err := env.manager.buildInstanceList(t.Context(), []string{"i1"})
		if err != nil {
			t.Fatalf("buildInstanceList: %v", err)
		}

		if len(instances) != 1 || instances[0].DeploymentName != redisI1 {
			t.Fatalf("the upgrade lists %+v, want instance i1 on the recorded deployment %s", instances, redisI1)
		}
	})

	t.Run("command preview", func(t *testing.T) {
		t.Parallel()

		env := newCmdEnv(t, nil)

		preview, err := env.manager.PreviewCommand(t.Context(), cmdReq("i1"))
		if err != nil {
			t.Fatalf("PreviewCommand: %v", err)
		}

		if len(preview.Instances) != 1 || preview.Instances[0].Deployment != redisI1 {
			t.Fatalf("the preview shows %+v, want instance i1 on the recorded deployment %s", preview.Instances, redisI1)
		}
	})
}
