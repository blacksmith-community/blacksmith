package manifest

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"blacksmith/internal/bosh"
	"blacksmith/internal/parity"
	"blacksmith/internal/services"
	"blacksmith/pkg/logger"
	"blacksmith/pkg/utils"

	"go.yaml.in/yaml/v2"
)

// TestMain stops the OpenBao dev server that the parity tests share.
func TestMain(m *testing.M) {
	code := m.Run()

	parity.CloseSharedVault()
	os.Exit(code)
}

const (
	parityDirectorUUID = "00000000-0000-4000-8000-000000000000"
	parityNetwork      = "fixture-net"
)

// parityParams are the provision parameters a plan renders with. The redis
// forges grab four of them into their job configuration, so those carry the
// values whose YAML form is a choice of the library. The tuned set has the
// integral numbers JSON delivers as float64. The strings set has strings
// that read as numbers, dates, and booleans. The types set has real values of
// other kinds, which are a bool, a float that is not whole, a list, and a
// nested map.
func parityParams(paramSet string, instanceID string) map[string]interface{} {
	params := map[string]interface{}{"instance_id": instanceID}

	switch paramSet {
	case "tuned":
		params["slowlog-max-len"] = float64(128)
		params["slowlog-log-slower-than"] = float64(10000)
		params["notify-keyspace-events"] = "KEA"
		params["maxmemory-policy"] = "allkeys-lru"
	case "strings":
		params["slowlog-max-len"] = "0755"
		params["slowlog-log-slower-than"] = "2026-01-02"
		params["notify-keyspace-events"] = "yes"
		params["maxmemory-policy"] = "1e3"
	case "types":
		params["slowlog-max-len"] = 3.5
		params["slowlog-log-slower-than"] = true
		params["notify-keyspace-events"] = []interface{}{"K", "E", "yes", 0755}
		params["maxmemory-policy"] = map[string]interface{}{
			"policy": "allkeys-lru",
			"nested": map[string]interface{}{"deep": "0755", "flag": false, "date": "2026-01-02"},
		}
	}

	return params
}

func parityParamSets(forge string) []string {
	if forge == "redis" {
		return []string{"none", "tuned", "strings", "types"}
	}

	return []string{"none"}
}

// parityGolden records the manifest bytes and the canonical dump of what a
// YAML reader makes of them, or the error that came out instead.
func parityGolden(t *testing.T, base string, manifestYAML string, err error) {
	t.Helper()

	if err != nil {
		parity.Golden(t, base+".yml", "ERROR: "+err.Error()+"\n")

		return
	}

	parity.GoldenYAML(t, base+".yml", manifestYAML)

	var decoded map[interface{}]interface{}

	decodeErr := yaml.Unmarshal([]byte(manifestYAML), &decoded)
	if decodeErr != nil {
		t.Fatalf("decode %s: %s", base, decodeErr)
	}

	parity.Golden(t, base+".dump", parity.Dump(decoded))
}

func readParityService(t *testing.T, fixture parity.Fixture) services.Service {
	t.Helper()

	service, err := services.ReadService(fixture.ServicesDir())
	if err != nil {
		t.Fatalf("read %s: %s", fixture.Name(), err)
	}

	return service
}

func prepareParityFixture(t *testing.T, fixture parity.Fixture) string {
	t.Helper()

	server := parity.SharedVault(t)
	instanceID := parity.InstanceID(fixture)

	parity.SeedCredentials(t, server, fixture, instanceID)
	t.Setenv("CREDENTIALS", parity.CredentialsPath(instanceID))
	t.Setenv("SPRUCE_FILE_BASE_PATH", fixture.Dir)
	t.Setenv("BOSH_NETWORK", parityNetwork)

	return instanceID
}

// genParityManifest renders a plan of a fixture with a set of parameters.
// It reads the plan again, because a merge may reuse its maps.
func genParityManifest(t *testing.T, fixture parity.Fixture, instanceID, planID, paramSet string) (string, error) {
	t.Helper()

	var current services.Plan

	for _, candidate := range readParityService(t, fixture).Plans {
		if candidate.ID == planID {
			current = candidate
		}
	}

	defaults := map[string]interface{}{
		"name":          current.ID + "-" + instanceID,
		"director_uuid": parityDirectorUUID,
	}

	return GenManifest(current, instanceID, utils.InterfaceMap(defaults), utils.Wrap("meta.params", parityParams(paramSet, instanceID)))
}

//nolint:paralleltest // spruce keeps global state, and the test sets environment variables
func TestGenManifestParity(t *testing.T) {
	for _, fixture := range parity.Fixtures(t) {
		t.Run(fixture.Name(), func(t *testing.T) {
			instanceID := prepareParityFixture(t, fixture)

			for _, plan := range readParityService(t, fixture).Plans {
				for _, paramSet := range parityParamSets(fixture.Forge) {
					rendered, err := genParityManifest(t, fixture, instanceID, plan.ID, paramSet)
					base := filepath.Join("testdata", "manifests", fixture.Forge, fixture.Variant, plan.ID+"."+paramSet)
					parityGolden(t, base, rendered, err)
				}
			}
		})
	}
}

var parityJobReference = regexp.MustCompile(`(?m)^[^#\n]*\bjobs\.([A-Za-z0-9_\-]+)(?:/\d+)?\.ips`)

// parityJobName returns the instance group the plan's credentials read from
// the VMs, which is standalone or node unless the credentials name another.
func parityJobName(t *testing.T, plan services.Plan) string {
	t.Helper()

	data, err := os.ReadFile(plan.CredentialsPath)
	if err != nil {
		t.Fatalf("read %s: %s", plan.CredentialsPath, err)
	}

	if match := parityJobReference.FindSubmatch(data); match != nil {
		return string(match[1])
	}

	if strings.Contains(plan.Type, "standalone") {
		return "standalone"
	}

	return "node"
}

// parityVMs returns the VMs a plan's deployment has, one for a standalone
// plan and three nodes for any other.
func parityVMs(t *testing.T, plan services.Plan, deployment string) []bosh.VM {
	t.Helper()

	job := parityJobName(t, plan)
	ids := []string{
		"11111111-1111-4111-8111-111111111111",
		"22222222-2222-4222-8222-222222222222",
		"33333333-3333-4333-8333-333333333333",
	}

	if strings.Contains(plan.Type, "standalone") {
		ids = ids[:1]
	}

	vms := make([]bosh.VM, 0, len(ids))

	for index, id := range ids {
		vms = append(vms, bosh.VM{
			ID: id, Job: job, Index: index,
			IPs: []string{fmt.Sprintf("10.0.0.%d", 11+index)},
			DNS: []string{id + "." + job + "." + parityNetwork + "." + deployment + ".bosh"},
		})
	}

	return vms
}

//nolint:paralleltest // spruce keeps global state, and the test sets environment variables
func TestCredentialsManifestParity(t *testing.T) {
	log := &logger.NoOpLogger{}

	for _, fixture := range parity.Fixtures(t) {
		t.Run(fixture.Name(), func(t *testing.T) {
			instanceID := prepareParityFixture(t, fixture)

			for _, plan := range readParityService(t, fixture).Plans {
				deployment := plan.ID + "-" + instanceID

				// The networks come from the deployment's own manifest, as they do
				// when a bind reads the credentials of a running deployment.
				deployed, err := genParityManifest(t, fixture, instanceID, plan.ID, "none")
				if err != nil {
					t.Fatalf("%s: render the deployment manifest: %s", plan.ID, err)
				}

				networks := extractNetworksByJob(deployed)
				jobs, dnsname := buildJobsFromVMs(parityVMs(t, plan, deployment), plan, deployment, networks, log)
				base := filepath.Join("testdata", "credentials", fixture.Forge, fixture.Variant, plan.ID)

				jobsIfc, err := marshalJobsToInterface(jobs, log)
				if err != nil {
					t.Fatalf("%s: %s", plan.ID, err)
				}

				manifestYAML, err := generateCredentialsManifest(plan, jobsIfc, deployment, instanceID, dnsname, log)
				if err != nil {
					parity.Golden(t, base+".yml", "ERROR: "+err.Error()+"\n")

					continue
				}

				parity.GoldenYAML(t, base+".yml", manifestYAML)

				credentials, err := extractCredentialsFromManifest(manifestYAML, log)

				recorded := map[string]interface{}{"networks": networks, "dnsname": dnsname, "jobs": jobsIfc, "credentials": credentials}
				if err != nil {
					recorded["error"] = err.Error()
				}

				parity.Golden(t, base+".dump", parity.Dump(recorded))
			}
		})
	}
}

// TestRenderedScalarsGoldenFailsWhenAQuoteIsLost shows that the scalars
// golden of a render catches a date that loses its quotes, which the decoded
// dump cannot, because the decoder reads the date as a string either way.
//
//nolint:paralleltest // spruce keeps global state, and the test sets environment variables
func TestRenderedScalarsGoldenFailsWhenAQuoteIsLost(t *testing.T) {
	for _, fixture := range parity.Fixtures(t) {
		if fixture.Forge != "redis" || fixture.Variant != "base" {
			continue
		}

		instanceID := prepareParityFixture(t, fixture)

		rendered, err := genParityManifest(t, fixture, instanceID, "redis-standalone-7", "strings")
		if err != nil {
			t.Fatalf("render: %s", err)
		}

		golden := parity.ScalarsPath(filepath.Join("testdata", "manifests", "redis", "base", "redis-standalone-7.strings.yml"))

		if diff := parity.GoldenDiff(golden, parity.Scalars(rendered)); diff != "" && !parity.Updating() {
			t.Fatalf("the unchanged render does not match its golden: %s", diff)
		}

		lost := strings.Replace(rendered, `"2026-01-02"`, `2026-01-02`, 1)
		if lost == rendered {
			t.Fatal("the render holds no quoted date to lose")
		}

		if parity.Updating() {
			return
		}

		if parity.GoldenDiff(golden, parity.Scalars(lost)) == "" {
			t.Error("the scalars golden does not change when a quoted date loses its quotes")
		}

		var kept, dropped map[interface{}]interface{}

		if yaml.Unmarshal([]byte(rendered), &kept) != nil || yaml.Unmarshal([]byte(lost), &dropped) != nil {
			t.Fatal("the render does not decode")
		}

		if parity.Dump(kept) != parity.Dump(dropped) {
			t.Error("the decoded dumps differ, so the scalars golden is no longer the only check that sees the lost quote")
		}

		return
	}

	t.Fatal("no redis base fixture")
}
