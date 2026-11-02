package manifest

import (
	"path/filepath"
	"strings"
	"testing"

	"blacksmith/internal/parity"
	"blacksmith/pkg/logger"
)

// TestParseManifestReleasesParity records the releases parseManifestReleases
// reads from each manifest. Its fields are strings, so a bare 1.10, 0755, or
// 1.0 version, and a sha1 that looks like a number, show what the decoder
// does when a YAML scalar is not a string.
func TestParseManifestReleasesParity(t *testing.T) {
	t.Parallel()

	var recorded strings.Builder

	for _, doc := range parity.Corpus(t) {
		releases, err := parseManifestReleases(doc.Body)
		if err != nil {
			recorded.WriteString(parity.Section(doc.Name, "ERROR: "+err.Error()+"\n"))

			continue
		}

		recorded.WriteString(parity.Section(doc.Name, parity.Dump(releases)))
	}

	parity.Golden(t, filepath.Join("testdata", "parity", "releases.dump"), recorded.String())
}

// TestExtractNetworksByJobParity records the network each instance group of
// a manifest maps to, which decides the DNS names in bind credentials.
func TestExtractNetworksByJobParity(t *testing.T) {
	t.Parallel()

	var recorded strings.Builder

	for _, doc := range parity.Corpus(t) {
		recorded.WriteString(parity.Section(doc.Name, parity.Dump(extractNetworksByJob(doc.Body))))
	}

	parity.Golden(t, filepath.Join("testdata", "parity", "networks.dump"), recorded.String())
}

// TestBuildJobsFallsBackToTheDefaultNetwork records the DNS names a bind
// builds when the deployment manifest gives no network for a job, because it
// is empty, unreadable, or names none. The names then use BOSH_NETWORK.
//
//nolint:paralleltest // the test sets an environment variable
func TestBuildJobsFallsBackToTheDefaultNetwork(t *testing.T) {
	t.Setenv("BOSH_NETWORK", "fallback.net")

	log := &logger.NoOpLogger{}

	var recorded strings.Builder

	for _, fixture := range parity.Fixtures(t) {
		plan := readParityService(t, fixture).Plans[0]
		deployment := plan.ID + "-fallback"

		for _, doc := range parity.OddDocuments() {
			switch doc.Name {
			case "empty", "unterminated", "list-root", "scalar-root", "instance-group-networks", "blacksmith-metadata":
			default:
				continue
			}

			networks := extractNetworksByJob(doc.Body)
			jobs, dnsname := buildJobsFromVMs(parityVMs(t, plan, deployment), plan, deployment, networks, log)

			recorded.WriteString(parity.Section(fixture.Name()+" / "+plan.ID+" / "+doc.Name,
				parity.Dump(map[string]interface{}{"networks": networks, "dnsname": dnsname, "jobs": jobs})))
		}
	}

	parity.Golden(t, filepath.Join("testdata", "parity", "fallback-network.dump"), recorded.String())
}
