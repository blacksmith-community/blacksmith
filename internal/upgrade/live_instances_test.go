//nolint:testpackage // the tests read the manager's unexported state
package upgrade

import (
	"errors"
	"net/http"
	"testing"

	"blacksmith/internal/apierr"
)

const (
	deploymentNameKey = "deployment_name"
	serviceIDKey      = "service_id"
	planIDKey         = "plan_id"
	smallPlan         = "small"
)

var errLiveIndexDown = errors.New("index unreadable")

// A record in Vault under a deprovisioned or unusable ID is not an
// instance, so the lists the upgrade builds leave it out.
func TestUpgradeListUsesOnlyLiveInstances(t *testing.T) {
	t.Parallel()

	env := newCmdEnv(t, nil)
	vault := env.manager.vault.(*cmdVault) //nolint:forcetypeassert // the fixture's own vault
	vault.records[goneInstance] = map[string]any{deploymentNameKey: "redis-gone", serviceIDKey: certJob, planIDKey: smallPlan}
	vault.tombstones = map[string]bool{goneInstance: true}
	vault.records["a..b"] = map[string]any{deploymentNameKey: redisI1, serviceIDKey: certJob, planIDKey: smallPlan}

	instances, err := env.manager.buildInstanceList(t.Context(), []string{"i1", goneInstance, "unknown", "a..b", "x/y"})
	if err != nil {
		t.Fatalf("buildInstanceList: %v", err)
	}

	for _, instance := range instances {
		failed := instance.Status == InstanceStatusFailed

		if instance.InstanceID == "i1" && failed {
			t.Errorf("the live instance failed: %s", instance.Error)
		}

		if instance.InstanceID != "i1" && (!failed || instance.Error != instanceNotInVault || instance.DeploymentName != "") {
			t.Errorf("instance %q = %+v, want failed as not found with no deployment", instance.InstanceID, instance)
		}
	}
}

func TestCommandPreviewBlocksInstancesThatAreNotLive(t *testing.T) {
	t.Parallel()

	env := newCmdEnv(t, nil)
	vault := env.manager.vault.(*cmdVault) //nolint:forcetypeassert // the fixture's own vault
	vault.records[goneInstance] = map[string]any{deploymentNameKey: redisI1, serviceIDKey: certJob, planIDKey: smallPlan}
	vault.tombstones = map[string]bool{goneInstance: true}

	preview, err := env.manager.PreviewCommand(t.Context(), cmdReq("i1", goneInstance, "unknown", "a..b"))
	if err != nil {
		t.Fatalf("PreviewCommand: %v", err)
	}

	for _, instance := range preview.Instances {
		if instance.InstanceID == "i1" {
			if instance.Blocked != nil {
				t.Errorf("the live instance is blocked: %+v", instance.Blocked)
			}

			continue
		}

		if instance.Blocked == nil || instance.Blocked.Code != apierr.CodeInstanceNotFound || len(instance.VMs) != 0 || instance.Deployment != "" {
			t.Errorf("instance %q = %+v, want blocked as not found with no VMs", instance.InstanceID, instance)
		}
	}
}

func TestCommandPreviewFailsWhenTheIndexIsUnreadable(t *testing.T) {
	t.Parallel()

	env := newCmdEnv(t, nil)
	env.manager.vault.(*cmdVault).indexErr = errLiveIndexDown //nolint:forcetypeassert // the fixture's own vault

	_, err := env.manager.PreviewCommand(t.Context(), cmdReq("i1"))

	var apiErr *apierr.Error
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusServiceUnavailable || apiErr.Code != apierr.CodeVaultUnavailable {
		t.Fatalf("error = %v, want 503 vault_unavailable", err)
	}
}
