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

	const (
		instanceID = "instance-1"
		builtName  = "standalone-" + instanceID
	)

	cases := []struct {
		name  string
		entry string
		want  string
	}{
		{"a recorded name", `{"plan_id":"standalone","deployment_name":"standalone-classic-instance-1"}`, "standalone-classic-instance-1"},
		{"a missing name", `{"plan_id":"standalone"}`, builtName},
		{"an empty name", `{"plan_id":"standalone","deployment_name":""}`, builtName},
		{"a null name", `{"plan_id":"standalone","deployment_name":null}`, builtName},
		{"no name and no plan ID", `{}`, "-instance-1"},
	}

	for _, entryCase := range cases {
		t.Run(entryCase.name, func(t *testing.T) {
			t.Parallel()

			var instance vault.Instance

			err := json.Unmarshal([]byte(entryCase.entry), &instance)
			if err != nil {
				t.Fatalf("failed to decode %s: %v", entryCase.entry, err)
			}

			got := instance.DeploymentNameFor(instanceID)
			if got != entryCase.want {
				t.Errorf("DeploymentNameFor(%q) on %s = %q, want %q", instanceID, entryCase.entry, got, entryCase.want)
			}
		})
	}
}

// An index entry the reconciler marked deleted is a tombstone, by either of
// the two marks it writes, and every other entry is live.
func TestInstanceIsATombstoneWhenMarkedDeleted(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		entry string
		want  bool
	}{
		{"a live entry", `{"plan_id":"standalone"}`, false},
		{"an active status", `{"plan_id":"standalone","status":"active"}`, false},
		{"a deleted status", `{"plan_id":"standalone","status":"deleted"}`, true},
		{"a deleted flag", `{"plan_id":"standalone","deleted":true}`, true},
		{"a false flag", `{"plan_id":"standalone","deleted":false}`, false},
	}

	for _, entryCase := range cases {
		t.Run(entryCase.name, func(t *testing.T) {
			t.Parallel()

			var instance vault.Instance

			err := json.Unmarshal([]byte(entryCase.entry), &instance)
			if err != nil {
				t.Fatalf("decoding %s: %v", entryCase.entry, err)
			}

			if got := instance.IsTombstone(); got != entryCase.want {
				t.Errorf("IsTombstone() = %v, want %v", got, entryCase.want)
			}
		})
	}
}

// The same rule reads a raw index entry, as the credential sweep sees it.
func TestEntryIsATombstoneReadsARawEntry(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		entry interface{}
		want  bool
	}{
		"nil":             {entry: nil, want: false},
		"not a map":       {entry: "x", want: false},
		"a live map":      {entry: map[string]interface{}{"plan_id": "p"}, want: false},
		"a deleted map":   {entry: map[string]interface{}{"status": "deleted"}, want: true},
		"a flagged map":   {entry: map[string]interface{}{"deleted": true}, want: true},
		"a status number": {entry: map[string]interface{}{"status": 3}, want: false},
	}

	for name, entryCase := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if got := vault.EntryIsTombstone(entryCase.entry); got != entryCase.want {
				t.Errorf("EntryIsTombstone() = %v, want %v", got, entryCase.want)
			}
		})
	}
}
