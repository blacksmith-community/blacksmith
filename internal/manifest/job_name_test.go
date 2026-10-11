package manifest_test

import (
	"testing"

	"blacksmith/internal/bosh"
	manifestPkg "blacksmith/internal/manifest"
	"blacksmith/internal/services"
)

// A job is named group/index, and a VM the director lists without an index,
// which the listing gives index -1, is named group/ID instead, because
// group/-1 names no instance.
func TestCredentialsJobNameUsesTheIDWhenTheIndexIsMissing(t *testing.T) {
	t.Parallel()

	plan := services.Plan{ID: "valkey-standalone", Name: "standalone", Type: "standalone"}

	tests := []struct {
		name  string
		index int
		want  string
	}{
		{name: "an index", index: 2, want: "node/2"},
		{name: "no index", index: -1, want: "node/uuid-0"},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			vm := bosh.VM{ID: "uuid-0", Job: "node", Index: testCase.index, IPs: []string{"10.0.0.5"}}

			job := manifestPkg.CreateJobFromVM(vm, plan, "valkey-1", "default")
			if job.Name != testCase.want {
				t.Errorf("job name = %q, want %q", job.Name, testCase.want)
			}
		})
	}
}
