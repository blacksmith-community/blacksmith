package reconciler_test

import (
	"path/filepath"
	"strings"
	"testing"

	"blacksmith/internal/parity"
	. "blacksmith/pkg/reconciler"
	"go.yaml.in/yaml/v2"
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

// TestParseManifestDataParity records the releases and stemcells a scan
// lists for each manifest.
func TestParseManifestDataParity(t *testing.T) {
	t.Parallel()

	scanner := &BoshScanner{}

	var recorded strings.Builder

	for _, doc := range parity.Corpus(t) {
		detail := scanner.ParseManifestData(doc.Body)
		recorded.WriteString(parity.Section(doc.Name, parity.Dump(map[string]interface{}{
			"releases": detail.Releases, "stemcells": detail.Stemcells,
		})))
	}

	parity.Golden(t, filepath.Join("testdata", "parity", "manifest-data.dump"), recorded.String())
}

// TestExtractCredentialsFromManifestParity records the credentials the
// populator takes from each manifest, for each service type it knows.
//
// getDeploymentManifest, which decodes the manifest before this step, cannot
// be reached from a test. It asks the BOSH client for a type declared inside
// the function, so no outside type satisfies the interface. The decode here
// is the same call, into the same kind of map, with the same library.
func TestExtractCredentialsFromManifestParity(t *testing.T) {
	t.Parallel()

	populator := NewCredentialPopulator(nil, NewMockLogger())

	var recorded strings.Builder

	for _, doc := range parity.Corpus(t) {
		var manifest map[string]interface{}

		// A frozen copy of the decode at credential_populator.go:243.
		err := yaml.Unmarshal([]byte(doc.Body), &manifest)
		if err != nil {
			recorded.WriteString(parity.Section(doc.Name, "DECODE ERROR: "+err.Error()+"\n"))

			continue
		}

		for _, name := range []string{"rabbitmq-standalone-" + parityInstanceID, "redis-standalone-" + parityInstanceID, "postgresql-cluster-" + parityInstanceID} {
			creds, err := populator.ExtractCredentialsFromManifest(manifest, name)

			body := parity.Dump(creds)
			if err != nil {
				body = "ERROR: " + err.Error() + "\n" + body
			}

			recorded.WriteString(parity.Section(doc.Name+" as "+name, body))
		}
	}

	parity.Golden(t, filepath.Join("testdata", "parity", "extract-credentials.dump"), recorded.String())
}

// handBuiltManifest returns a manifest with string keys all the way down,
// the shape the extractors accept, in place of what the v2 decode gives them.
func handBuiltManifest(job, propertiesKey string, properties map[string]interface{}, withHost bool) map[string]interface{} {
	group := map[string]interface{}{
		"name": "standalone",
		"jobs": []interface{}{
			map[string]interface{}{"name": "other-job", "properties": map[string]interface{}{}},
			map[string]interface{}{
				"name":       job,
				"properties": map[string]interface{}{propertiesKey: properties},
			},
		},
	}

	if withHost {
		group["networks"] = []interface{}{
			map[string]interface{}{"name": "fixture-net", "static_ips": []interface{}{"10.0.0.11", "10.0.0.12"}},
		}
	}

	return map[string]interface{}{"name": "fixture", "instance_groups": []interface{}{group}}
}

// TestExtractCredentialsFromHandBuiltManifestParity records the credentials
// the populator takes from manifests whose maps all have string keys, which
// reach the success paths of the extractors. The values include strings that
// read as other YAML types, because a graft decode may type them differently.
func TestExtractCredentialsFromHandBuiltManifestParity(t *testing.T) {
	t.Parallel()

	populator := NewCredentialPopulator(nil, NewMockLogger())

	cases := []struct {
		name       string
		deployment string
		manifest   map[string]interface{}
	}{
		{"redis", "redis-standalone-" + parityInstanceID, handBuiltManifest("redis", "redis", map[string]interface{}{"password": "fixture-password-not-real"}, true)},
		{"redis-yes-password", "redis-standalone-" + parityInstanceID, handBuiltManifest("redis", "redis", map[string]interface{}{"password": "yes"}, true)},
		{"redis-numeric-password", "redis-standalone-" + parityInstanceID, handBuiltManifest("redis", "redis", map[string]interface{}{"password": 12345}, true)},
		{"redis-no-host", "redis-standalone-" + parityInstanceID, handBuiltManifest("redis", "redis", map[string]interface{}{"password": "0755"}, false)},
		{"redis-wrong-job", "redis-standalone-" + parityInstanceID, handBuiltManifest("valkey", "redis", map[string]interface{}{"password": "x"}, true)},
		{"rabbitmq", "rabbitmq-standalone-" + parityInstanceID, handBuiltManifest("rabbitmq", "rabbitmq", map[string]interface{}{
			"admin_user": "fixture-admin", "admin_pass": "fixture-password-not-real", "app_user": "fixture-app", "app_pass": "2026-01-02",
			"vhost": "/", "erlang_cookie": "1e3",
		}, true)},
		{"rabbitmq-no-host", "rabbitmq-standalone-" + parityInstanceID, handBuiltManifest("rabbitmq", "rabbitmq", map[string]interface{}{"admin_user": "a", "admin_pass": "b"}, false)},
		{"postgresql", "postgresql-cluster-" + parityInstanceID, handBuiltManifest("postgres", "postgres", map[string]interface{}{
			"username": "fixture-user", "password": "fixture-password-not-real", "database": "fixture_db",
		}, true)},
		{"postgresql-odd-values", "postgresql-cluster-" + parityInstanceID, handBuiltManifest("postgres", "postgres", map[string]interface{}{
			"username": "yes", "password": "0755", "database": "2026-01-02",
		}, true)},
		{"postgresql-no-host", "postgresql-cluster-" + parityInstanceID, handBuiltManifest("postgres", "postgres", map[string]interface{}{"username": "u", "password": "p", "database": "d"}, false)},
		{"mysql-unsupported", "mysql-standalone-" + parityInstanceID, handBuiltManifest("mysql", "mysql", map[string]interface{}{"password": "x"}, true)},
	}

	var recorded strings.Builder

	for _, item := range cases {
		creds, err := populator.ExtractCredentialsFromManifest(item.manifest, item.deployment)

		body := parity.Dump(creds)
		if err != nil {
			body = "ERROR: " + err.Error() + "\n" + body
		}

		recorded.WriteString(parity.Section(item.name, body))
	}

	parity.Golden(t, filepath.Join("testdata", "parity", "extract-credentials-hand-built.dump"), recorded.String())
}
