package operators

import (
	"fmt"
	"os"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	natsbackend "github.com/fivetwenty-io/graft/internal/backends/nats"
	"github.com/fivetwenty-io/graft/internal/utils/ansi"
	"github.com/fivetwenty-io/graft/pkg/graft"
	"github.com/fivetwenty-io/graft/pkg/graft/tree"
)

// NatsOperator provides the (( nats "store_type:path" )) operator
// It will fetch values from NATS JetStream KV or Object stores.
//
// NatsOperator supports the `@target` operator-call syntax (e.g.
// `(( nats@myserver "kv:path" ))`): Opcall.Run sets Evaluator.Target from
// the parsed Expr's target before calling Run, and Run selects a pooled,
// target-specific connection from internal/backends/nats.DefaultPool
// (NATS_<TARGET>_URL, etc.) when it is non-empty, falling back to the
// existing single-config connection pool otherwise.
type NatsOperator struct{}

// SupportsTarget reports that nats honors "@target".
func (NatsOperator) SupportsTarget() bool {
	return true
}

// fetchFromKV retrieves a value from a NATS KV store using
// natsbackend.FetchFromKVCached, which namespaces the cache key by target
// so the same store path on two different NATS clusters never collides
// (mirroring the same fix in op_vault.go's performVaultLookup) and
// coalesces concurrent identical requests into one backend call.
// skipCache (the ":nocache" modifier) bypasses the cache read, the
// coalescing, and the cache write for this one lookup.
func (n NatsOperator) fetchFromKV(js jetstream.JetStream, target, storePath string, config *natsbackend.Config, skipCache bool) (interface{}, error) {
	return natsbackend.FetchFromKVCached(target, js, storePath, config, skipCache)
}

// fetchFromObject retrieves a value from a NATS Object store; see
// fetchFromKV for the cache/dedup behavior.
func (n NatsOperator) fetchFromObject(js jetstream.JetStream, target, storePath string, config *natsbackend.Config, skipCache bool) (interface{}, error) {
	return natsbackend.FetchFromObjectCached(target, js, storePath, config, skipCache)
}

// parseNatsConfig extracts configuration from arguments.
//
//nolint:gocyclo // NATS config parsing handles many configuration options
func parseNatsConfig(ev *graft.Evaluator, args []*graft.Expr) (*natsbackend.Config, error) {
	// Default URL from environment or fallback to NATS default
	defaultURL := os.Getenv("NATS_URL")
	if defaultURL == "" {
		defaultURL = nats.DefaultURL
	}

	// Parse environment variables for default configuration
	defaultTimeout := parseDurationOrDefault(os.Getenv("NATS_TIMEOUT"), 5*time.Second)
	defaultRetries := parseIntOrDefault(os.Getenv("NATS_RETRIES"), 3)
	defaultRetryInterval := parseDurationOrDefault(os.Getenv("NATS_RETRY_INTERVAL"), 1*time.Second)
	defaultRetryBackoff := parseFloatOrDefault(os.Getenv("NATS_RETRY_BACKOFF"), 2.0)
	defaultMaxRetryInterval := parseDurationOrDefault(os.Getenv("NATS_MAX_RETRY_INTERVAL"), 30*time.Second)
	defaultTLS := parseBoolOrDefault(os.Getenv("NATS_TLS"))
	defaultCacheTTLEnv := parseDurationOrDefault(os.Getenv("NATS_CACHE_TTL"), natsbackend.DefaultCacheTTL)
	defaultStreamingThreshold := parseInt64OrDefault(os.Getenv("NATS_STREAMING_THRESHOLD"), 10*1024*1024)
	defaultAuditLogging := parseBoolOrDefault(os.Getenv("NATS_AUDIT_LOGGING"))

	config := &natsbackend.Config{
		URL:                defaultURL,
		Timeout:            defaultTimeout,
		Retries:            defaultRetries,
		RetryInterval:      defaultRetryInterval,
		RetryBackoff:       defaultRetryBackoff,
		MaxRetryInterval:   defaultMaxRetryInterval,
		TLS:                defaultTLS,
		CertFile:           os.Getenv("NATS_CERT_FILE"),
		KeyFile:            os.Getenv("NATS_KEY_FILE"),
		CAFile:             os.Getenv("NATS_CA_FILE"),
		InsecureSkipVerify: parseBoolOrDefault(os.Getenv("NATS_INSECURE_SKIP_VERIFY")),
		CacheTTL:           defaultCacheTTLEnv,
		StreamingThreshold: defaultStreamingThreshold,
		AuditLogging:       defaultAuditLogging,
		Token:              os.Getenv("NATS_TOKEN"),
		User:               os.Getenv("NATS_USER"),
		Password:           os.Getenv("NATS_PASSWORD"),
		NkeySeedFile:       os.Getenv("NATS_NKEY"),
		CredsFile:          os.Getenv("NATS_CREDS"),
	}

	// If we have a second argument, it could be URL string or config map
	if len(args) > 1 {
		val, err := ResolveOperatorArgument(ev, args[1])
		if err != nil {
			return nil, err
		}

		switch v := val.(type) {
		case string:
			// Simple URL string
			config.URL = v
		case map[string]interface{}:
			// Configuration map
			if url, ok := v["url"]; ok {
				if urlStr, ok := url.(string); ok {
					config.URL = urlStr
				}
			}
			if timeout, ok := v["timeout"]; ok {
				if timeoutStr, ok := timeout.(string); ok {
					if d, err := time.ParseDuration(timeoutStr); err == nil {
						config.Timeout = d
					}
				}
			}
			if retries, ok := v["retries"]; ok {
				switch r := retries.(type) {
				case int:
					config.Retries = r
				case float64:
					config.Retries = int(r)
				}
			}
			if tlsVal, ok := v["tls"]; ok {
				if tlsBool, ok := tlsVal.(bool); ok {
					config.TLS = tlsBool
				}
			}
			if cert, ok := v["cert_file"]; ok {
				if certStr, ok := cert.(string); ok {
					config.CertFile = certStr
				}
			}
			if key, ok := v["key_file"]; ok {
				if keyStr, ok := key.(string); ok {
					config.KeyFile = keyStr
				}
			}
			if ca, ok := v["ca_file"]; ok {
				if caStr, ok := ca.(string); ok {
					config.CAFile = caStr
				}
			}
			if insecure, ok := v["insecure_skip_verify"]; ok {
				if insecureBool, ok := insecure.(bool); ok {
					config.InsecureSkipVerify = insecureBool
				}
			}
			if cacheTTL, ok := v["cache_ttl"]; ok {
				if ttlStr, ok := cacheTTL.(string); ok {
					if d, err := time.ParseDuration(ttlStr); err == nil {
						config.CacheTTL = d
					}
				}
			}
			if streamingThreshold, ok := v["streaming_threshold"]; ok {
				switch st := streamingThreshold.(type) {
				case int:
					config.StreamingThreshold = int64(st)
				case int64:
					config.StreamingThreshold = st
				case float64:
					config.StreamingThreshold = int64(st)
				}
			}
			if auditLogging, ok := v["audit_logging"]; ok {
				if auditBool, ok := auditLogging.(bool); ok {
					config.AuditLogging = auditBool
				}
			}
			if retryInterval, ok := v["retry_interval"]; ok {
				if intervalStr, ok := retryInterval.(string); ok {
					if d, err := time.ParseDuration(intervalStr); err == nil {
						config.RetryInterval = d
					}
				}
			}
			if retryBackoff, ok := v["retry_backoff"]; ok {
				switch b := retryBackoff.(type) {
				case float64:
					config.RetryBackoff = b
				case int:
					config.RetryBackoff = float64(b)
				}
			}
			if maxRetryInterval, ok := v["max_retry_interval"]; ok {
				if intervalStr, ok := maxRetryInterval.(string); ok {
					if d, err := time.ParseDuration(intervalStr); err == nil {
						config.MaxRetryInterval = d
					}
				}
			}
			if token, ok := v["token"]; ok {
				if tokenStr, ok := token.(string); ok {
					config.Token = tokenStr
				}
			}
			if user, ok := v["user"]; ok {
				if userStr, ok := user.(string); ok {
					config.User = userStr
				}
			}
			if password, ok := v["password"]; ok {
				if passwordStr, ok := password.(string); ok {
					config.Password = passwordStr
				}
			}
			if nkey, ok := v["nkey_seed_file"]; ok {
				if nkeyStr, ok := nkey.(string); ok {
					config.NkeySeedFile = nkeyStr
				}
			}
			if creds, ok := v["creds_file"]; ok {
				if credsStr, ok := creds.(string); ok {
					config.CredsFile = credsStr
				}
			}
		default:
			return nil, fmt.Errorf("second argument must be URL string or configuration map")
		}
	}

	return config, nil
}

// Setup initializes the NATS operator.
func (NatsOperator) Setup() error {
	return nil
}

// Phase returns the phase when this operator runs.
func (NatsOperator) Phase() graft.OperatorPhase {
	return graft.EvalPhase
}

// Dependencies returns the dependencies for this operator.
func (NatsOperator) Dependencies(ev *graft.Evaluator, args []*graft.Expr, locs []*tree.Cursor, auto []*tree.Cursor) []*tree.Cursor {
	return auto
}

// Run executes the NATS operator.
func (n NatsOperator) Run(ev *graft.Evaluator, args []*graft.Expr) (*graft.Response, error) {
	DEBUG("running (( nats ... )) operation at $.%s", ev.Here)
	defer DEBUG("done with (( nats ... )) operation at $%s\n", ev.Here)

	engine := graft.GetEngine(ev)
	if engine.GetOperatorState().IsNATSSkipped() {
		if !engine.GetOperatorState().IsRedactMode() {
			// --skip-nats (not REDACT=1): defer this call entirely,
			// without resolving any argument, instead of contacting
			// NATS or substituting "REDACTED" - see op_skip_defer.go.
			return deferSkippedCall(ev, engine, "nats", args), nil
		}
		return &graft.Response{
			Type:  graft.Replace,
			Value: redactedValue,
		}, nil
	}

	// Validate arguments
	if len(args) < 1 {
		return nil, fmt.Errorf("nats operator requires at least one argument")
	}

	// Resolve the path argument
	pathVal, err := ResolveOperatorArgument(ev, args[0])
	if err != nil {
		return nil, err
	}

	path, ok := pathVal.(string)
	if !ok {
		return nil, ansi.Errorf("@R{first argument to nats operator must be a string}")
	}

	// When features.FeatureBackendRegistry is enabled on ev's engine and
	// a custom backend is registered under the name "nats", it is
	// consulted instead of internal/backends/nats below.
	if resp, handled, backendErr := runNatsCustomBackend(ev, args, path); handled {
		return resp, backendErr
	}

	// Parse the path to get store type and path
	storeType, storePath, err := natsbackend.ParsePath(path)
	if err != nil {
		return nil, err
	}

	// Parse configuration
	config, err := parseNatsConfig(ev, args)
	if err != nil {
		return nil, err
	}

	// A non-empty target selects a pooled, target-specific connection; an
	// empty target keeps the existing
	// single-config connection pool behavior verbatim. Unlike the no-target
	// path, a target that cannot be resolved is a hard error — there is no
	// fallback to the default connection, since that would silently read
	// from the wrong NATS cluster.
	var pc *natsbackend.PooledConnection
	if ev.Target != "" {
		pc, err = natsbackend.DefaultPool.GetConnection(ev.Target)
		if err != nil {
			return nil, fmt.Errorf("error selecting NATS target %q: %w", ev.Target, err)
		}
		defer natsbackend.DefaultPool.ReleaseConnection(ev.Target)
	} else {
		pc, err = natsbackend.ConnPool.GetConnection(config)
		if err != nil {
			return nil, err
		}
		defer natsbackend.ConnPool.ReleaseConnection(config)
	}

	// Fetch the value based on store type
	var value interface{}
	switch storeType {
	case natsbackend.StoreKV:
		value, err = n.fetchFromKV(pc.JS, ev.Target, storePath, config, ShouldSkipCache(ev))
	case natsbackend.StoreObj:
		value, err = n.fetchFromObject(pc.JS, ev.Target, storePath, config, ShouldSkipCache(ev))
	}

	if err != nil {
		return nil, err
	}

	return &graft.Response{
		Type:  graft.Replace,
		Value: value,
	}, nil
}

// runNatsCustomBackend consults a custom backend registered under the
// name "nats" (features.FeatureBackendRegistry), reporting handled=false
// when there is none and the built-in NATS path should run instead. The
// raw path is passed exactly as received - the "kv:"/"obj:" store-type
// prefix natsbackend.ParsePath validates is a built-in-backend
// convention, not a Backend interface requirement, so a custom backend is
// never subjected to it. Unlike the vault/AWS operators, the result is
// returned as-is (not stringified): the built-in NATS path already
// returns arbitrary interface{} values from KV/Object stores, so a custom
// NATS backend gets the same latitude.
func runNatsCustomBackend(ev *graft.Evaluator, args []*graft.Expr, path string) (*graft.Response, bool, error) {
	backend, ok := resolveCustomBackend(ev, "nats")
	if !ok {
		return nil, false, nil
	}

	// A second argument (`(( nats "kv:b/k" {"url": "..."} ))`) is the
	// built-in internal/backends/nats connection config that
	// parseNatsConfig parses - a Backend implementation owns its own
	// connection setup and has no parameter to receive it through
	// (Backend.Get takes only ctx and path), so silently discarding it
	// would accept a call whose second argument the custom backend can
	// never honor. Reject it instead of ignoring it - see
	// docs/developer-guide/custom-backends.md's "Which name to register
	// under" section.
	if len(args) > 1 {
		return nil, true, ansi.Errorf("@R{nats operator's config argument is not supported against a custom \"nats\" backend - remove the second argument or configure the backend directly}")
	}

	val, err := fetchFromBackend(ev, backend, ev.Target, path)
	if err != nil {
		return nil, true, wrapBackendError("nats", ev.Target, path, err)
	}

	return &graft.Response{
		Type:  graft.Replace,
		Value: val,
	}, true, nil
}

// ClearNatsCache clears the NATS cache (useful for testing).
func ClearNatsCache() {
	natsbackend.ClearCache()
}

// GetNatsMetrics returns current NATS operator metrics.
func GetNatsMetrics() map[string]interface{} {
	return natsbackend.GetMetrics()
}

// ShutdownNatsOperator gracefully shuts down NATS connections and goroutines.
func ShutdownNatsOperator() {
	natsbackend.Shutdown()
}

// parseInt64OrDefault delegates to the natsbackend package.
func parseInt64OrDefault(value string, defaultValue int64) int64 {
	return natsbackend.ParseInt64OrDefault(value, defaultValue)
}

// parseFloatOrDefault delegates to the natsbackend package.
func parseFloatOrDefault(value string, defaultValue float64) float64 {
	return natsbackend.ParseFloatOrDefault(value, defaultValue)
}

//nolint:gochecknoinits // Operator registration and background cleanup must start at package load time
func init() {
	// Wire up debug logging for the backend
	natsbackend.DebugFunc = func(format string, args ...interface{}) {
		DEBUG(format, args...)
	}

	go natsbackend.ConnPool.CleanupLoop()
	go func() {
		ticker := time.NewTicker(natsbackend.CacheCleanupInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				natsbackend.Cache.Cleanup()
			case <-natsbackend.CacheStopCleanup:
				return
			}
		}
	}()
	RegisterOp("nats", NatsOperator{})
}
