package manifest

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"blacksmith/internal/bosh"
	"blacksmith/internal/parity"
	"blacksmith/internal/services"
	"blacksmith/pkg/logger"
	"blacksmith/pkg/utils"

	"github.com/fivetwenty-io/graft/pkg/yaml"
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

// The spruce-era recordings of the renders are frozen in testdata/manifests
// and testdata/credentials, and the graft recordings sit beside them in
// testdata/manifests-graft and testdata/credentials-graft, under the same
// relative paths.
const (
	spruceManifests   = "manifests"
	graftManifests    = "manifests-graft"
	spruceCredentials = "credentials"
	graftCredentials  = "credentials-graft"
	parityTestdata    = "testdata"
	parityErrorPrefix = "ERROR: "
)

// parityGolden records the manifest bytes and the canonical dump of what a
// YAML reader makes of them, or the error that came out instead, and then
// compares the render with the frozen spruce-era one.
func parityGolden(t *testing.T, rel string, manifestYAML string, err error) {
	t.Helper()

	base := filepath.Join(parityTestdata, graftManifests, rel)

	if err != nil {
		parity.Golden(t, base+".yml", parityErrorPrefix+err.Error()+"\n")
		compareWithSpruce(t, spruceManifests, rel, "", err)

		return
	}

	parity.GoldenYAML(t, base+".yml", manifestYAML)

	var decoded map[string]interface{}

	decodeErr := yaml.Unmarshal([]byte(manifestYAML), &decoded)
	if decodeErr != nil {
		t.Fatalf("decode %s: %s", base, decodeErr)
	}

	parity.Golden(t, base+".dump", parity.Dump(decoded))
	compareWithSpruce(t, spruceManifests, rel, manifestYAML, nil)
}

// pemLeadingNewline names the values the redis tls and dual-mode fixtures
// read with (( file )) from PEM files that begin with an empty line. Spruce
// wrote each as a |4+ block that dropped that newline, and graft keeps the
// file byte for byte, so these values differ from the spruce-era recording
// by design. PEM readers skip leading whitespace, so the deployed value
// works either way.
var pemLeadingNewline = []string{"$.meta.redis.tls.ca", "$.meta.redis.tls.ca_cert", "$.meta.redis.tls.ca_key"}

// acceptedPaths returns the paths whose values may differ from the
// spruce-era recording of the render at rel, a path relative to the
// recording directory.
func acceptedPaths(rel string) []string {
	if strings.HasPrefix(rel, filepath.Join("redis", "tls")+string(filepath.Separator)) ||
		strings.HasPrefix(rel, filepath.Join("redis", "dual-mode")+string(filepath.Separator)) {
		return pemLeadingNewline
	}

	return nil
}

// compareWithSpruce checks a render against the spruce-era recording of the
// same plan, at rel under the directory spruceDir. A render that failed must
// have failed under spruce too, though the error text is new. A render that
// worked must hold the same tree, except at the accepted paths, each of
// which must differ, so that an exemption cannot go stale.
func compareWithSpruce(t *testing.T, spruceDir, rel string, manifestYAML string, err error) {
	t.Helper()

	sprucePath := filepath.Join(parityTestdata, spruceDir, rel+".yml")

	want, readErr := os.ReadFile(sprucePath) //nolint:gosec // a test reads its own golden
	if readErr != nil {
		t.Fatalf("read the spruce-era recording %s: %s", sprucePath, readErr)
	}

	spruceFailed := strings.HasPrefix(string(want), parityErrorPrefix)

	switch {
	case spruceFailed && err != nil:
		return
	case spruceFailed:
		t.Errorf("%s: spruce failed to render it, and graft rendered it", sprucePath)

		return
	case err != nil:
		t.Errorf("%s: spruce rendered it, and graft failed to", sprucePath)

		return
	}

	accepted := acceptedPaths(rel)
	seen := map[string]bool{}

	for _, difference := range parity.YAMLDifferences(want, []byte(manifestYAML)) {
		if path, ok := differencePath(difference); ok && slices.Contains(accepted, path) {
			seen[path] = true

			continue
		}

		t.Errorf("%s: %s", sprucePath, difference)
	}

	for _, path := range accepted {
		if !seen[path] {
			t.Errorf("%s: the accepted difference at %s did not occur", sprucePath, path)
		}
	}
}

// differencePath returns the path a YAMLDifferences line reports, which
// follows its tree or decode prefix and ends at the first colon after it.
func differencePath(difference string) (string, bool) {
	_, rest, ok := strings.Cut(difference, " ")
	if !ok {
		return "", false
	}

	path, _, ok := strings.Cut(rest, ": ")

	return path, ok
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

	return GenManifest(current, instanceID, defaults, utils.Wrap("meta.params", parityParams(paramSet, instanceID)))
}

//nolint:paralleltest // GenManifest holds a process-wide lock, and the test sets environment variables
func TestGenManifestParity(t *testing.T) {
	for _, fixture := range parity.Fixtures(t) {
		t.Run(fixture.Name(), func(t *testing.T) {
			instanceID := prepareParityFixture(t, fixture)

			for _, plan := range readParityService(t, fixture).Plans {
				for _, paramSet := range parityParamSets(fixture.Forge) {
					rendered, err := genParityManifest(t, fixture, instanceID, plan.ID, paramSet)
					parityGolden(t, filepath.Join(fixture.Forge, fixture.Variant, plan.ID+"."+paramSet), rendered, err)
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

//nolint:paralleltest // GenManifest holds a process-wide lock, and the test sets environment variables
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
				rel := filepath.Join(fixture.Forge, fixture.Variant, plan.ID)
				base := filepath.Join(parityTestdata, graftCredentials, rel)

				jobsTree := jobsMap(jobs)

				manifestYAML, err := generateCredentialsManifest(plan, jobsTree, deployment, instanceID, dnsname, log)
				if err != nil {
					parity.Golden(t, base+".yml", parityErrorPrefix+err.Error()+"\n")
					compareWithSpruce(t, spruceCredentials, rel, "", err)

					continue
				}

				parity.GoldenYAML(t, base+".yml", manifestYAML)
				compareWithSpruce(t, spruceCredentials, rel, manifestYAML, nil)

				credentials, err := extractCredentialsFromManifest(manifestYAML, log)

				recorded := map[string]interface{}{"networks": networks, "dnsname": dnsname, "jobs": jobsTree, "credentials": credentials}
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
//nolint:paralleltest // GenManifest holds a process-wide lock, and the test sets environment variables
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

		golden := parity.ScalarsPath(filepath.Join(parityTestdata, graftManifests, "redis", "base", "redis-standalone-7.strings.yml"))

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

		var kept, dropped map[string]interface{}

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
