package manifest

import (
	"reflect"
	"testing"

	"blacksmith/internal/parity"
	"blacksmith/internal/services"
	"blacksmith/pkg/utils"
)

// TestJobsMapMatchesTheYAMLRoundTrip checks that the jobs map credentials
// templates read is the one the jobs used to become after a trip through
// YAML text, a yaml.v3 marshal read back by yaml.v2. The jobs carry names
// a YAML 1.1 reader could take for another type, and a job with no IPs or
// DNS names, which the round trip made empty lists.
func TestJobsMapMatchesTheYAMLRoundTrip(t *testing.T) {
	t.Parallel()

	jobs := []*services.Job{
		{
			Name: "node/0", Deployment: "plan-instance", ID: "11111111-1111-4111-8111-111111111111",
			PlanID: "08", PlanName: "y", FQDN: "1e3",
			IPs: []string{"10.0.0.11", "10.0.0.12"}, DNS: []string{"2001-12-14"},
		},
		{Name: "node", Deployment: "plan-instance", ID: "~", PlanID: "1_000", PlanName: "no", FQDN: "0x1F"},
		{Name: "standalone", IPs: []string{}, DNS: []string{}},
	}

	decoded, err := parity.LegacyRoundTrip(map[string][]*services.Job{"jobs": jobs})
	if err != nil {
		t.Fatal(err)
	}

	want := utils.DeinterfaceMap(decoded)
	got := jobsMap(jobs)

	if !reflect.DeepEqual(want, got) {
		t.Errorf("the jobs map differs from the round trip\n--- want\n%s--- got\n%s", parity.Dump(want), parity.Dump(got))
	}
}
