package certinventory

import (
	"reflect"
	"testing"
)

func factsWith(job JobFacts) *ManifestFacts {
	return &ManifestFacts{Jobs: []JobFacts{job}}
}

func TestListenerRules(t *testing.T) {
	redisTLS := func(dual bool) map[string]interface{} {
		return map[string]interface{}{"redis": map[string]interface{}{"tls": map[string]interface{}{
			"enabled": true, "dual-mode": dual,
		}}}
	}

	cases := []struct {
		name  string
		facts *ManifestFacts
		want  []Listener
	}{
		{
			name:  "redis fixture without dual mode",
			facts: walkFixture(t, "redis-cluster-7.yml"),
			want:  []Listener{{Group: "redis", Job: "cluster-7", Variable: "redis_cluster_crt", Port: 6379, Name: "tls"}},
		},
		{
			name: "redis dual mode",
			facts: factsWith(JobFacts{
				Group: "redis", Job: "cluster-7", Properties: redisTLS(true),
				CertRefs: map[string]string{"redis.tls.ca_cert": "redis_cluster_crt"},
			}),
			want: []Listener{{Group: "redis", Job: "cluster-7", Variable: "redis_cluster_crt", Port: 16379, Name: "tls"}},
		},
		{
			name:  "valkey fixture",
			facts: walkFixture(t, "valkey-cluster.yml"),
			want:  []Listener{{Group: "valkey", Job: "cluster", Variable: "valkey_cluster_crt", Port: 6379, Name: "tls"}},
		},
		{
			name: "valkey dual mode",
			facts: factsWith(JobFacts{
				Group: "valkey", Job: "cluster",
				Properties: map[string]interface{}{"valkey": map[string]interface{}{"tls": map[string]interface{}{
					"enabled": true, "dual-mode": true,
				}}},
				CertRefs: map[string]string{"valkey.tls.ca_cert": "valkey_cluster_crt"},
			}),
			want: []Listener{{Group: "valkey", Job: "cluster", Variable: "valkey_cluster_crt", Port: 16379, Name: "tls"}},
		},
		{
			name:  "rabbitmq fixture with management",
			facts: walkFixture(t, "rabbitmq-standalone.yml"),
			want: []Listener{
				{Group: "standalone", Job: "rabbitmq", Variable: "rabbitmq_standalone_crt", Port: 5671, Name: "amqps"},
				{Group: "standalone", Job: "rabbitmq", Variable: "rabbitmq_standalone_crt", Port: 15671, Name: "management"},
			},
		},
		{
			name: "rabbitmq custom port and management off",
			facts: factsWith(JobFacts{
				Group: "standalone", Job: "rabbitmq",
				Properties: map[string]interface{}{
					"rabbitmq":   map[string]interface{}{"tls": map[string]interface{}{"enabled": true, "port": 5672}},
					"management": map[string]interface{}{"enabled": false},
				},
				CertRefs: map[string]string{"rabbitmq.tls.crt": "rabbitmq_standalone_crt"},
			}),
			want: []Listener{{Group: "standalone", Job: "rabbitmq", Variable: "rabbitmq_standalone_crt", Port: 5672, Name: "amqps"}},
		},
		{
			name: "rabbitmq management port override",
			facts: factsWith(JobFacts{
				Group: "standalone", Job: "rabbitmq",
				Properties: map[string]interface{}{
					"rabbitmq":   map[string]interface{}{"tls": map[string]interface{}{"enabled": true}},
					"management": map[string]interface{}{"enabled": true, "tls": map[string]interface{}{"port": 16000}},
				},
				CertRefs: map[string]string{"rabbitmq.tls.crt": "rabbitmq_standalone_crt"},
			}),
			want: []Listener{
				{Group: "standalone", Job: "rabbitmq", Variable: "rabbitmq_standalone_crt", Port: 5671, Name: "amqps"},
				{Group: "standalone", Job: "rabbitmq", Variable: "rabbitmq_standalone_crt", Port: 16000, Name: "management"},
			},
		},
		{
			name: "redis without tls",
			facts: factsWith(JobFacts{
				Group: "redis", Job: "cluster-7",
				Properties: map[string]interface{}{"redis": map[string]interface{}{"tls": map[string]interface{}{"enabled": false}}},
				CertRefs:   map[string]string{"redis.tls.ca_cert": "redis_cluster_crt"},
			}),
			want: nil,
		},
		{
			name: "redis with no tls block",
			facts: factsWith(JobFacts{
				Group: "redis", Job: "standalone",
				Properties: map[string]interface{}{"redis": map[string]interface{}{"maxmemory": 0}},
			}),
			want: nil,
		},
		{
			name: "metrics emitter gets none",
			facts: factsWith(JobFacts{
				Group: "standalone", Job: "rabbitmq-metrics-emitter",
				Properties: map[string]interface{}{"rabbitmq_metrics_emitter": map[string]interface{}{"client": map[string]interface{}{}}},
				CertRefs:   map[string]string{"rabbitmq_metrics_emitter.client.cert": "rabbitmq_metrics_emitter_crt"},
			}),
			want: nil,
		},
		{
			name: "enabled given as a string",
			facts: factsWith(JobFacts{
				Group: "redis", Job: "cluster-7",
				Properties: map[string]interface{}{"redis": map[string]interface{}{"tls": map[string]interface{}{"enabled": "true"}}},
				CertRefs:   map[string]string{"other.cert": "redis_cluster_crt"},
			}),
			want: []Listener{{Group: "redis", Job: "cluster-7", Variable: "redis_cluster_crt", Port: 6379, Name: "tls"}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ListenerRules(tc.facts)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

func TestListenerRulesNilFacts(t *testing.T) {
	if got := ListenerRules(nil); got != nil {
		t.Errorf("got %+v, want nil", got)
	}
}
