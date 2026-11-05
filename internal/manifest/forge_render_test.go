package manifest

import (
	"fmt"
	"strings"
	"testing"

	"blacksmith/internal/parity"
	"blacksmith/internal/services"
	"blacksmith/pkg/logger"
	"blacksmith/pkg/utils"

	"github.com/fivetwenty-io/graft/pkg/yaml"
)

// forgeTuningKeys are the request parameters the redis and valkey plans
// read through a fallback chain of the request, the plan, and a default.
var forgeTuningKeys = []string{"maxmemory-policy", "notify-keyspace-events", "slowlog-log-slower-than", "slowlog-max-len"}

// forgeFixture returns the fixture of a forge's variant and its plan
// planID.
func forgeFixture(t *testing.T, forge, variant, planID string) (parity.Fixture, services.Plan) {
	t.Helper()

	for _, fixture := range parity.Fixtures(t) {
		if fixture.Forge != forge || fixture.Variant != variant {
			continue
		}

		for _, plan := range readParityService(t, fixture).Plans {
			if plan.ID == planID {
				return fixture, plan
			}
		}
	}

	t.Fatalf("no plan %s in the %s/%s fixture", planID, forge, variant)

	return parity.Fixture{}, services.Plan{}
}

// forgeTuning returns the properties under the forge's own key of the
// standalone job of the standalone instance group in a rendered manifest.
func forgeTuning(t *testing.T, rendered, forge string) map[string]interface{} {
	t.Helper()

	var decoded map[string]interface{}

	err := yaml.Unmarshal([]byte(rendered), &decoded)
	if err != nil {
		t.Fatalf("decode the render: %s", err)
	}

	groups, _ := decoded["instance_groups"].([]interface{})
	for _, group := range groups {
		group, _ := group.(map[string]interface{})
		if group["name"] != "standalone" {
			continue
		}

		jobs, _ := group["jobs"].([]interface{})
		for _, job := range jobs {
			job, _ := job.(map[string]interface{})
			if job["name"] != "standalone" {
				continue
			}

			properties, _ := job["properties"].(map[string]interface{})
			tuning, ok := properties[forge].(map[string]interface{})

			if ok {
				return tuning
			}
		}
	}

	t.Fatalf("the render has no %s properties on the standalone job", forge)

	return nil
}

// TestForgeTuningFallbackChains renders the redis and valkey standalone
// plans with the tuning parameters set by the request, by the plan, and by
// neither, and checks that each link of the fallback chain still wins in
// its turn. The request strings reach the render as graft literals.
//
//nolint:paralleltest // GenManifest holds a process-wide lock, and the test sets environment variables
func TestForgeTuningFallbackChains(t *testing.T) {
	request := map[string]interface{}{
		"maxmemory-policy":        "volatile-lru",
		"notify-keyspace-events":  "Ex",
		"slowlog-log-slower-than": float64(5000),
		"slowlog-max-len":         float64(64),
	}
	planValues := map[string]interface{}{
		"maxmemory-policy":        "noeviction",
		"notify-keyspace-events":  "Kg",
		"slowlog-log-slower-than": 20000,
		"slowlog-max-len":         256,
	}
	defaults := map[string]string{
		"maxmemory-policy":        "allkeys-lru",
		"notify-keyspace-events":  "",
		"slowlog-log-slower-than": "10000",
		"slowlog-max-len":         "128",
	}

	cases := []struct {
		name    string
		request bool
		plan    bool
		want    func(key string) string
	}{
		{"request wins over the plan", true, true, func(key string) string { return fmt.Sprint(request[key]) }},
		{"plan wins over the default", false, true, func(key string) string { return fmt.Sprint(planValues[key]) }},
		{"default when neither is set", false, false, func(key string) string { return defaults[key] }},
	}

	for _, forge := range []string{"redis", "valkey"} {
		fixture, plan := forgeFixture(t, forge, "base", forge+"-standalone")

		for _, tc := range cases {
			t.Run(forge+"/"+tc.name, func(t *testing.T) {
				var meta, params map[string]interface{}

				if tc.plan {
					meta = planValues
				}

				if tc.request {
					params = request
				}

				tuning := renderForgeTuning(t, fixture, plan, meta, params)
				for _, key := range forgeTuningKeys {
					if got := fmt.Sprint(tuning[key]); got != tc.want(key) {
						t.Errorf("%s = %q, want %q", key, got, tc.want(key))
					}
				}
			})
		}
	}
}

// renderForgeTuning renders a redis or valkey plan with planValues as the
// plan's own meta.<forge>_<key> settings and request as the request
// parameters, and returns the forge's job properties.
func renderForgeTuning(t *testing.T, fixture parity.Fixture, plan services.Plan, planValues, request map[string]interface{}) map[string]interface{} {
	t.Helper()

	instanceID := prepareParityFixture(t, fixture)

	meta := map[string]interface{}{}
	for key, value := range planValues {
		meta[fixture.Forge+"_"+key] = value
	}

	params := map[string]interface{}{"instance_id": instanceID}
	for key, value := range request {
		params[key] = value
	}

	names := map[string]interface{}{"name": plan.ID + "-" + instanceID, "director_uuid": parityDirectorUUID}

	rendered, err := GenManifest(plan, instanceID, names, map[string]interface{}{"meta": meta}, utils.Wrap("meta.params", params))
	if err != nil {
		t.Fatalf("render: %s", err)
	}

	return forgeTuning(t, rendered, fixture.Forge)
}

// renderForgeCredentials renders the credentials of a fixture's plan the
// way a bind does, with the parity VMs, and returns the credentials map,
// the instance ID, the deployment's DNS name, and the VMs' addresses in
// order.
func renderForgeCredentials(t *testing.T, forge, variant, planID string) (map[string]interface{}, string, string, []string) {
	t.Helper()

	return renderForgeCredentialsWith(t, forge, variant, planID, true)
}

// renderForgeCredentialsWith renders as renderForgeCredentials does. When
// withInstanceID is false, it leaves instance_id out of the params the
// credentials read, which a bind always sets, so that a fallback after
// params.instance_id is evaluated.
func renderForgeCredentialsWith(t *testing.T, forge, variant, planID string, withInstanceID bool) (map[string]interface{}, string, string, []string) {
	t.Helper()

	fixture, plan := forgeFixture(t, forge, variant, planID)
	instanceID := prepareParityFixture(t, fixture)
	deployment := plan.ID + "-" + instanceID
	log := &logger.NoOpLogger{}

	deployed, err := genParityManifest(t, fixture, instanceID, plan.ID, "none")
	if err != nil {
		t.Fatalf("render the deployment manifest: %s", err)
	}

	vms := parityVMs(t, plan, deployment)
	jobs, dnsname := buildJobsFromVMs(vms, plan, deployment, extractNetworksByJob(deployed), log)

	var rendered string

	if withInstanceID {
		rendered, err = generateCredentialsManifest(plan, jobsMap(jobs), deployment, instanceID, dnsname, log)
	} else {
		params := map[string]interface{}{"hostname": dnsname}
		rendered, err = GenManifest(plan, instanceID, jobsMap(jobs), plan.Credentials, map[string]interface{}{"name": deployment}, utils.Wrap("params", params))
	}

	if err != nil {
		t.Fatalf("render the credentials: %s", err)
	}

	credentials, err := extractCredentialsFromManifest(rendered, log)
	if err != nil {
		t.Fatalf("read the credentials: %s", err)
	}

	ips := make([]string, 0, len(vms))
	for _, vm := range vms {
		ips = append(ips, vm.IPs...)
	}

	credentialsMap, _ := credentials.(map[string]interface{})

	return credentialsMap, instanceID, dnsname, ips
}

// TestForgeCredentialsGrabs renders the credentials of each forge's plans
// and checks the values that come from the request-side params, the VM
// jobs, and the credentials themselves. No assertion prints a value that
// holds a seeded secret.
//
//nolint:paralleltest // GenManifest holds a process-wide lock, and the test sets environment variables
func TestForgeCredentialsGrabs(t *testing.T) {
	expect := func(t *testing.T, credentials map[string]interface{}, key, want string) {
		t.Helper()

		if got := fmt.Sprint(credentials[key]); got != want {
			t.Errorf("credentials.%s = %q, want %q", key, got, want)
		}
	}

	t.Run("redis", func(t *testing.T) {
		credentials, _, dnsname, _ := renderForgeCredentials(t, "redis", "base", "redis-standalone")
		expect(t, credentials, "host", dnsname)
	})

	t.Run("valkey", func(t *testing.T) {
		credentials, _, dnsname, ips := renderForgeCredentials(t, "valkey", "base", "valkey-cluster")
		expect(t, credentials, "host", dnsname)
		expect(t, credentials, "hosts", "["+strings.Join(ips, " ")+"]")
	})

	t.Run("rabbitmq", func(t *testing.T) {
		credentials, instanceID, dnsname, ips := renderForgeCredentials(t, "rabbitmq", "base", "rabbitmq-standalone")
		expect(t, credentials, "vhost", instanceID)
		expect(t, credentials, "dnsname", dnsname)
		expect(t, credentials, "host", dnsname)
		expect(t, credentials, "api_url", "http://"+ips[0]+":15672/api")

		protocols, _ := credentials["protocols"].(map[string]interface{})
		amqp, _ := protocols["amqp"].(map[string]interface{})
		expect(t, amqp, "vhost", instanceID)

		dashboard := fmt.Sprint(credentials["dashboard_url"])
		if !strings.HasPrefix(dashboard, "http://"+ips[0]+":15672/#/login/") {
			t.Error("credentials.dashboard_url does not begin with the first VM's management address")
		}

		uris, _ := credentials["uris"].([]interface{})
		if len(uris) != 1 || !strings.HasSuffix(fmt.Sprint(uris[0]), "@"+dnsname+":5672") {
			t.Error("credentials.uris does not hold one amqp URI that ends at the deployment's DNS name")
		}
	})

	t.Run("rabbitmq without an instance ID", func(t *testing.T) {
		credentials, _, _, _ := renderForgeCredentialsWith(t, "rabbitmq", "base", "rabbitmq-standalone", false)
		expect(t, credentials, "vhost", "/")

		protocols, _ := credentials["protocols"].(map[string]interface{})
		amqp, _ := protocols["amqp"].(map[string]interface{})
		expect(t, amqp, "vhost", "/")
	})

	t.Run("rabbitmq dashboard registration", func(t *testing.T) {
		credentials, _, dnsname, _ := renderForgeCredentials(t, "rabbitmq", "dashboard-registration", "rabbitmq-standalone")
		expect(t, credentials, "api_url", "https://"+dnsname+":15671/api")
	})

	t.Run("postgresql", func(t *testing.T) {
		credentials, _, _, ips := renderForgeCredentials(t, "postgresql", "base", "postgresql-cluster")
		expect(t, credentials, "host", ips[0])
		expect(t, credentials, "hostname", ips[0])
		expect(t, credentials, "read_host", ips[1])
		expect(t, credentials, "hosts", "["+strings.Join(ips, " ")+"]")
	})
}
