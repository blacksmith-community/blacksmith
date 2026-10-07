package vault_test

import (
	"encoding/json"
	"testing"

	"blacksmith/pkg/vault"
)

// An index entry names its deployment by the deployment_name it records,
// and by its plan ID and the instance ID when it records none. The entries
// are decoded from JSON, as an index entry read from Vault is.
func TestDeploymentNameForPrefersTheRecordedName(t *testing.T) {
	t.Parallel()

	const instanceID = "instance-1"

	cases := []struct {
		name  string
		entry string
		want  string
	}{
		{"a recorded name", `{"plan_id":"standalone","deployment_name":"standalone-classic-instance-1"}`, "standalone-classic-instance-1"},
		{"a missing name", `{"plan_id":"standalone"}`, "standalone-instance-1"},
		{"an empty name", `{"plan_id":"standalone","deployment_name":""}`, "standalone-instance-1"},
		{"a null name", `{"plan_id":"standalone","deployment_name":null}`, "standalone-instance-1"},
		{"no name and no plan ID", `{}`, "-instance-1"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var instance vault.Instance

			err := json.Unmarshal([]byte(tc.entry), &instance)
			if err != nil {
				t.Fatalf("failed to decode %s: %v", tc.entry, err)
			}

			got := instance.DeploymentNameFor(instanceID)
			if got != tc.want {
				t.Errorf("DeploymentNameFor(%q) on %s = %q, want %q", instanceID, tc.entry, got, tc.want)
			}
		})
	}
}
