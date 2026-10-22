package certinventory

import (
	"crypto/elliptic"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadManifest(t *testing.T, name string) string {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join("testdata", "manifests", name))
	if err != nil {
		t.Fatal(err)
	}

	return string(raw)
}

func walkFixture(t *testing.T, name string) *ManifestFacts {
	t.Helper()

	facts, err := WalkManifest(loadManifest(t, name))
	if err != nil {
		t.Fatalf("WalkManifest(%s): %v", name, err)
	}

	return facts
}

func TestWalkManifestFindsRabbitMQLiteralCA(t *testing.T) {
	facts := walkFixture(t, "rabbitmq-standalone.yml")

	if len(facts.Literals) != 1 {
		t.Fatalf("got %d literals, want 1: %+v", len(facts.Literals), facts.Literals)
	}

	lit := facts.Literals[0]
	if lit.Path != "instance_groups[0].jobs[1].properties.rabbitmq.tls.ca" {
		t.Errorf("path = %q", lit.Path)
	}

	if lit.Cert.Subject.CommonName != "test-services-ca" {
		t.Errorf("CN = %q", lit.Cert.Subject.CommonName)
	}

	if lit.Group != "standalone" {
		t.Errorf("group = %q", lit.Group)
	}

	if facts.Name != "rabbitmq-9e8f" {
		t.Errorf("name = %q", facts.Name)
	}
}

func TestWalkManifestSkipsPrivateKeys(t *testing.T) {
	manifest := `name: d
instance_groups:
- name: g
  instances: 1
  jobs:
  - name: j
    properties:
      tls:
        key: |
          -----BEGIN PRIVATE KEY-----
          MIIBVQIBADANBgkqhkiG9w0BAQEFAASCAT8wggE7AgEAAkEA
          -----END PRIVATE KEY-----
        both: |
          -----BEGIN CERTIFICATE-----
          AAAA
          -----END CERTIFICATE-----
          -----BEGIN PRIVATE KEY-----
          MIIBVQIBADANBgkqhkiG9w0BAQEFAASCAT8wggE7AgEAAkEA
          -----END PRIVATE KEY-----
        name: kept
`

	facts, err := WalkManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}

	if len(facts.Literals) != 0 {
		t.Errorf("literals = %+v, want none", facts.Literals)
	}

	for _, j := range facts.Jobs {
		if strings.Contains(strings.ToLower(dump(j.Properties)), "private key") {
			t.Errorf("job %s properties hold a private key: %v", j.Job, j.Properties)
		}
	}

	if len(facts.Jobs) != 1 || facts.Jobs[0].Properties["tls"].(map[string]interface{})["name"] != "kept" {
		t.Errorf("other properties should survive: %+v", facts.Jobs)
	}
}

func dump(v interface{}) string {
	switch x := v.(type) {
	case string:
		return x
	case map[string]interface{}:
		var b strings.Builder
		for k, e := range x {
			b.WriteString(k)
			b.WriteString(dump(e))
		}

		return b.String()
	case []interface{}:
		var b strings.Builder
		for _, e := range x {
			b.WriteString(dump(e))
		}

		return b.String()
	}

	return ""
}

func testCAPEM(t *testing.T, cn string, serial int64) string {
	t.Helper()

	key := ecKey(t, elliptic.P256())
	_, pemBytes := makeCert(t, certSpec{cn: cn, isCA: true, subject: &key.PublicKey, selfKey: key, serial: serial})

	return string(pemBytes)
}

func indent(s, prefix string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i := range lines {
		lines[i] = prefix + lines[i]
	}

	return strings.Join(lines, "\n")
}

func TestWalkManifestBundleYieldsOneObservationPerBlock(t *testing.T) {
	bundle := testCAPEM(t, "ca-one", 1) + testCAPEM(t, "ca-two", 2)
	manifest := "name: d\ninstance_groups:\n- name: g\n  instances: 2\n  jobs:\n  - name: j\n    properties:\n      tls:\n        ca: |\n" +
		indent(bundle, "          ") + "\n"

	facts, err := WalkManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}

	if len(facts.Literals) != 2 {
		t.Fatalf("got %d literals, want 2", len(facts.Literals))
	}

	if facts.Literals[0].Path != facts.Literals[1].Path {
		t.Errorf("paths differ: %q vs %q", facts.Literals[0].Path, facts.Literals[1].Path)
	}

	if facts.Literals[0].Cert.Subject.CommonName != "ca-one" || facts.Literals[1].Cert.Subject.CommonName != "ca-two" {
		t.Errorf("order wrong: %s, %s", facts.Literals[0].Cert.Subject.CommonName, facts.Literals[1].Cert.Subject.CommonName)
	}
}

func TestWalkManifestSigningCAsAndCertRefs(t *testing.T) {
	facts := walkFixture(t, "redis-cluster-7.yml")

	if got := facts.SigningCAs["redis_cluster_crt"]; got != "/ws-bosh/ws-blacksmith/blacksmith_services_ca" {
		t.Errorf("SigningCAs = %q", got)
	}

	if len(facts.CertVariables) != 1 || facts.CertVariables[0] != "redis_cluster_crt" {
		t.Errorf("CertVariables = %v", facts.CertVariables)
	}

	if facts.Groups["redis"] != 3 {
		t.Errorf("Groups = %v", facts.Groups)
	}

	var job *JobFacts

	for i := range facts.Jobs {
		if facts.Jobs[i].Job == "cluster-7" {
			job = &facts.Jobs[i]
		}
	}

	if job == nil {
		t.Fatalf("no cluster-7 job in %+v", facts.Jobs)
	}

	if job.Group != "redis" {
		t.Errorf("job group = %q", job.Group)
	}

	if got := job.CertRefs["redis.tls.ca_cert"]; got != "redis_cluster_crt" {
		t.Errorf("CertRefs = %v", job.CertRefs)
	}

	if _, ok := job.CertRefs["redis.tls.ca_key"]; ok {
		t.Errorf("a private-key reference must not appear: %v", job.CertRefs)
	}
}

func TestWalkManifestAliases(t *testing.T) {
	ca := testCAPEM(t, "alias-ca", 7)
	manifest := "name: d\ninstance_groups:\n" +
		"- name: a\n  instances: 1\n  jobs:\n  - name: j\n    properties: &props\n      tls:\n        ca: |\n" +
		indent(ca, "          ") + "\n" +
		"- name: b\n  instances: 1\n  jobs:\n  - name: j\n    properties: *props\n"

	facts, err := WalkManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}

	if len(facts.Literals) != 2 {
		t.Fatalf("got %d literals, want one per use: %+v", len(facts.Literals), facts.Literals)
	}

	if facts.Literals[0].Path != "instance_groups[0].jobs[0].properties.tls.ca" ||
		facts.Literals[1].Path != "instance_groups[1].jobs[0].properties.tls.ca" {
		t.Errorf("paths = %q, %q", facts.Literals[0].Path, facts.Literals[1].Path)
	}

	if facts.Literals[1].Group != "b" {
		t.Errorf("second group = %q", facts.Literals[1].Group)
	}
}

func TestWalkManifestRejectsBadYAML(t *testing.T) {
	if _, err := WalkManifest("name: [unclosed"); err == nil {
		t.Fatal("expected an error")
	}
}

func TestResolveCredHubNameRelativeAndAbsolute(t *testing.T) {
	cases := []struct{ name, want string }{
		{"/ws-bosh/ws-blacksmith/blacksmith_services_ca", "/ws-bosh/ws-blacksmith/blacksmith_services_ca"},
		{"blacksmith_services_ca", "/ws-bosh/redis-7a1b/blacksmith_services_ca"},
		{"redis_cluster_crt", "/ws-bosh/redis-7a1b/redis_cluster_crt"},
	}

	for _, c := range cases {
		if got := ResolveCredHubName("ws-bosh", "redis-7a1b", c.name); got != c.want {
			t.Errorf("ResolveCredHubName(%q) = %q, want %q", c.name, got, c.want)
		}
	}
}
