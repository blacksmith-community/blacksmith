package reconciler_test

import (
	"path/filepath"
	"strings"
	"testing"

	"blacksmith/internal/parity"
	. "blacksmith/pkg/reconciler"
)

const parityInstanceID = "00000000-0000-0000-0000-000000000001"

func parityServices() []Service {
	return []Service{
		{ID: "fixture-service", Name: "Fixture", Plans: []Plan{{ID: "fixture-plan", Name: "Plan"}, {ID: "other-plan", Name: "Other"}}},
	}
}

// TestParseManifestPropertiesParity records the properties, blacksmith
// metadata and meta ParseManifestProperties pulls out of every manifest.
func TestParseManifestPropertiesParity(t *testing.T) {
	t.Parallel()

	scanner := &BoshScanner{}

	var recorded strings.Builder

	for _, doc := range parity.Corpus(t) {
		parsed, err := scanner.ParseManifestProperties(doc.Body)
		if err != nil {
			recorded.WriteString(parity.Section(doc.Name, "ERROR: "+err.Error()+"\n"))

			continue
		}

		recorded.WriteString(parity.Section(doc.Name, parity.Dump(parsed)))
	}

	parity.Golden(t, filepath.Join("testdata", "parity", "properties.dump"), recorded.String())
}

// TestMatchDeploymentParity records how the matcher reads a manifest, both
// to score a name match and to find blacksmith metadata when the name does
// not match.
func TestMatchDeploymentParity(t *testing.T) {
	t.Parallel()

	matcher := NewServiceMatcher(&mockBroker{services: parityServices()}, NewMockLogger())

	var recorded strings.Builder

	for _, doc := range parity.Corpus(t) {
		for _, name := range []string{"fixture-service-fixture-plan-" + parityInstanceID, "unmatched-deployment"} {
			detail := DeploymentDetail{Manifest: doc.Body}
			detail.Name = name

			result, err := matcher.MatchDeployment(detail, parityServices())

			body := "ERROR: " + errText(err) + "\n"
			if err == nil {
				body = parity.Dump(result)
			}

			recorded.WriteString(parity.Section(doc.Name+" as "+name, body))
		}
	}

	parity.Golden(t, filepath.Join("testdata", "parity", "match.dump"), recorded.String())
}

func errText(err error) string {
	if err == nil {
		return ""
	}

	return err.Error()
}
