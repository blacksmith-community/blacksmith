package certinventory

import (
	"strconv"
	"strings"
)

// Listener is a TLS port that a job serves with a manifest certificate.
type Listener struct {
	Group string
	Job   string
	// Variable is the certificate variable the listener serves. An empty
	// value means the variable is unknown, because the job holds several
	// certificate references and none is named for this service.
	Variable string
	Port     int
	// Name is "amqps", "management", or "tls".
	Name string
}

// ListenerRules applies the per-service listener rules to each job's
// properties. A job that no rule covers yields nothing.
func ListenerRules(facts *ManifestFacts) []Listener {
	if facts == nil {
		return nil
	}

	var out []Listener

	for _, job := range facts.Jobs {
		out = append(out, redisLike(job, "redis", "redis.tls.ca_cert")...)
		out = append(out, redisLike(job, "valkey", "valkey.tls.ca_cert")...)
		out = append(out, rabbitmq(job)...)
	}

	return out
}

// redisLike covers Redis and Valkey, which share one rule under their own
// property prefix.
func redisLike(job JobFacts, prefix, certProp string) []Listener {
	if !propBool(job.Properties, prefix+".tls.enabled") {
		return nil
	}

	port := 6379
	if propBool(job.Properties, prefix+".tls.dual-mode") {
		port = 16379
	}

	return []Listener{{
		Group: job.Group, Job: job.Job,
		Variable: variableFor(job, certProp), Port: port, Name: "tls",
	}}
}

func rabbitmq(job JobFacts) []Listener {
	if !propBool(job.Properties, "rabbitmq.tls.enabled") {
		return nil
	}

	variable := variableFor(job, "rabbitmq.tls.crt")

	out := []Listener{{
		Group: job.Group, Job: job.Job, Variable: variable,
		Port: propInt(job.Properties, "rabbitmq.tls.port", 5671), Name: "amqps",
	}}

	if propBool(job.Properties, "management.enabled") {
		out = append(out, Listener{
			Group: job.Group, Job: job.Job, Variable: variable,
			Port: propInt(job.Properties, "management.tls.port", 15671), Name: "management",
		})
	}

	return out
}

// variableFor picks the variable behind the service's certificate property.
// When the job holds exactly one certificate reference, that one is used.
func variableFor(job JobFacts, certProp string) string {
	if v, ok := job.CertRefs[certProp]; ok {
		return v
	}

	if len(job.CertRefs) == 1 {
		for _, v := range job.CertRefs {
			return v
		}
	}

	return ""
}

func propLookup(props map[string]interface{}, path string) (interface{}, bool) {
	var cur interface{} = props

	for _, part := range strings.Split(path, ".") {
		m, ok := cur.(map[string]interface{})
		if !ok {
			return nil, false
		}

		cur, ok = m[part]
		if !ok {
			return nil, false
		}
	}

	return cur, true
}

func propBool(props map[string]interface{}, path string) bool {
	v, ok := propLookup(props, path)
	if !ok {
		return false
	}

	switch x := v.(type) {
	case bool:
		return x
	case string:
		b, err := strconv.ParseBool(strings.TrimSpace(x))

		return err == nil && b
	}

	return false
}

func propInt(props map[string]interface{}, path string, def int) int {
	v, ok := propLookup(props, path)
	if !ok {
		return def
	}

	switch x := v.(type) {
	case int:
		return x
	case int64:
		return int(x)
	case float64:
		return int(x)
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(x)); err == nil {
			return n
		}
	}

	return def
}
