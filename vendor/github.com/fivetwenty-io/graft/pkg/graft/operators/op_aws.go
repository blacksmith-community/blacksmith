package operators

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/goccy/go-yaml"

	awsbackend "github.com/fivetwenty-io/graft/internal/backends/aws"
	"github.com/fivetwenty-io/graft/internal/utils/ansi"
	"github.com/fivetwenty-io/graft/pkg/graft"
	"github.com/fivetwenty-io/graft/pkg/graft/tree"
)

// Helper function wrappers that delegate to the aws backend package.
// These are kept as package-level functions so other operators (e.g. NATS)
// that shared these helpers continue to compile.

func parseDurationOrDefault(value string, defaultValue time.Duration) time.Duration {
	return awsbackend.ParseDurationOrDefault(value, defaultValue)
}

func parseIntOrDefault(value string, defaultValue int) int {
	return awsbackend.ParseIntOrDefault(value, defaultValue)
}

func parseBoolOrDefault(value string) bool {
	return awsbackend.ParseBoolOrDefault(value)
}

// AwsOperator provides two operators;  (( awsparam "path" )) and (( awssecret "name_or_arn" ))
// It will fetch parameters / secrets from the respective AWS service.
//
// AwsOperator supports the `@target` operator-call syntax (e.g.
// `(( awsparam@myaccount "path" ))`): Opcall.Run sets Evaluator.Target from
// the parsed Expr's target before calling Run, and Run selects a
// target-specific aws.Config from internal/backends/aws.ClientPool
// (AWS_<TARGET>_REGION, etc.) when it is non-empty, falling back to the
// existing plain-environment config otherwise.
type AwsOperator struct {
	variant string
}

// SupportsTarget reports that awsparam/awssecret honor "@target" (spec
// cluster A7 §7).
func (AwsOperator) SupportsTarget() bool {
	return true
}

// Setup ...
func (AwsOperator) Setup() error {
	return nil
}

// Phase ...
func (AwsOperator) Phase() OperatorPhase {
	return EvalPhase
}

// Dependencies is not used by AwsOperator.
func (AwsOperator) Dependencies(_ *Evaluator, _ []*Expr, _ []*tree.Cursor, auto []*tree.Cursor) []*tree.Cursor {
	return auto
}

// Run will invoke the appropriate getAws* function for each instance of the AwsOperator
// and extract the specified key (if provided).
//
//nolint:gocyclo // AWS operator handles multiple secret types and argument formats
func (o AwsOperator) Run(ev *Evaluator, args []*Expr) (*Response, error) {
	var err error
	DEBUG("running (( %s ... )) operation at $.%s", o.variant, ev.Here)
	defer DEBUG("done with (( %s ... )) operation at $.%s\n", o.variant, ev.Here)

	if len(args) < 1 {
		return nil, fmt.Errorf("%s operator requires at least one argument", o.variant)
	}

	// --skip-aws (not REDACT=1 - see IsRedactMode) defers this call
	// entirely, without resolving any argument, instead of contacting
	// AWS or substituting "REDACTED". Checked here, before argument
	// resolution, so a value this call's own arguments depend on need
	// not resolve either. The REDACT-mode "return REDACTED" substitution
	// (further below) is unchanged, and still resolves arguments first,
	// exactly as before this flag existed - see op_skip_defer.go.
	engine := graft.GetEngine(ev)
	if engine.GetOperatorState().IsAWSSkipped() && !engine.GetOperatorState().IsRedactMode() {
		return deferSkippedCall(ev, engine, o.variant, args), nil
	}

	var l []string
	for i, arg := range args {
		// Use ResolveOperatorArgument to support nested expressions
		val, resolveErr := ResolveOperatorArgument(ev, arg)
		if resolveErr != nil {
			DEBUG("  arg[%d]: failed to resolve expression to a concrete value", i)
			DEBUG("     [%d]: error was: %s", i, resolveErr)
			return nil, resolveErr
		}

		if val == nil {
			DEBUG("  arg[%d]: resolved to nil", i)
			return nil, fmt.Errorf("%s operator argument cannot be nil", o.variant)
		}

		switch v := val.(type) {
		case string:
			DEBUG("  arg[%d]: using string value '%v'", i, v)
			l = append(l, v)

		case int, int64, float64, bool:
			DEBUG("  arg[%d]: converting %T to string", i, v)
			l = append(l, fmt.Sprintf("%v", v))

		case map[string]interface{}:
			DEBUG("  arg[%d]: %v is not a string scalar", i, v)
			return nil, ansi.Errorf("@R{%s operator argument is a map; only scalars are supported here}", o.variant)

		case []interface{}:
			DEBUG("  arg[%d]: %v is not a string scalar", i, v)
			return nil, ansi.Errorf("@R{%s operator argument is a list; only scalars are supported here}", o.variant)

		default:
			DEBUG("  arg[%d]: using value of type %T as string", i, val)
			l = append(l, fmt.Sprintf("%v", val))
		}
	}

	key, params, err := parseAwsOpKey(strings.Join(l, ""))
	if err != nil {
		return nil, err
	}

	DEBUG("     [0]: Using %s key '%s'\n", o.variant, key)

	var value string
	if !engine.GetOperatorState().IsAWSSkipped() {
		// When features.FeatureBackendRegistry is enabled on ev's engine
		// and a custom backend is registered under this operator's own
		// name ("awsparam" or "awssecret"), it is consulted instead of
		// internal/backends/aws below, using the same key the built-in
		// path would fetch (before any "?key=..." subkey extraction,
		// which - like the built-in path - runs uniformly afterward
		// regardless of which source produced value).
		if backend, ok := resolveCustomBackend(ev, o.variant); ok {
			val, fetchErr := fetchFromBackend(ev, backend, ev.Target, key)
			if fetchErr != nil {
				return nil, fmt.Errorf("$.%s error fetching %s: %w", key, o.variant, wrapBackendError(o.variant, ev.Target, key, fetchErr))
			}
			value = stringifyBackendValue(val)
		} else {
			ctx := context.Background()
			cfg, cacheTarget, cfgErr := o.resolveConfig(ctx, ev.Target)
			if cfgErr != nil {
				return nil, cfgErr
			}

			switch o.variant {
			case "awsparam":
				value, err = o.getAwsParam(ctx, cfg, cacheTarget, key, ShouldSkipCache(ev))
			case "awssecret":
				value, err = o.getAwsSecret(ctx, cfg, cacheTarget, key, params, ShouldSkipCache(ev))
			}

			if err != nil {
				return nil, fmt.Errorf("$.%s error fetching %s: %w", key, o.variant, err)
			}
		}

		subkey := params.Get("key")
		if subkey != "" {
			tmp := make(map[string]interface{})
			err := yaml.Unmarshal([]byte(value), &tmp)

			if err != nil {
				return nil, fmt.Errorf("$.%s error extracting key: %w", key, err)
			}

			tmp = graft.NormalizeMap(tmp)

			if _, ok := tmp[subkey]; !ok {
				return nil, fmt.Errorf("$.%s invalid key '%s'", key, subkey)
			}

			value = fmt.Sprintf("%v", tmp[subkey])
		}
	} else {
		// When AWS is skipped (including via the REDACT environment variable,
		// see evaluator.go and engine.go), return the literal "REDACTED"
		// without making a backend call, matching spruce's op_aws.go and this
		// package's vault/NATS operators (op_vault.go, op_nats.go).
		value = redactedValue
	}

	return &Response{
		Type:  Replace,
		Value: value,
	}, nil
}

// parseAwsOpKey parsed the parameters passed to AwsOperator.
// Primarily it splits the key from the extra arguments (specified as a query string).
func parseAwsOpKey(key string) (string, url.Values, error) {
	split := strings.SplitN(key, "?", 2)
	if len(split) == 1 {
		split = append(split, "")
	}

	values, err := url.ParseQuery(split[1])
	if err != nil {
		return "", values, fmt.Errorf("invalid argument string: %w", err)
	}

	return split[0], values, nil
}

// resolveConfig returns the aws.Config to use and the pool cache namespace
// it was resolved under. A non-empty target selects a pooled,
// target-specific config and namespaces the cache under the target name,
// erroring if that target has no configuration: unlike
// the no-target path, there is no fallback, since silently falling back to
// the default config is exactly the wrong-account-read risk this wiring
// closes. An empty target keeps the existing behavior verbatim: try the
// "default" pooled config, then fall back to plain AWS_* environment
// variables.
func (o AwsOperator) resolveConfig(ctx context.Context, target string) (aws.Config, string, error) {
	if target != "" {
		cfg, err := awsbackend.DefaultPool.GetConfig(ctx, target)
		if err != nil {
			return aws.Config{}, "", fmt.Errorf("error selecting AWS target %q: %w", target, err)
		}
		return cfg, target, nil
	}

	cfg, cfgErr := awsbackend.DefaultPool.GetConfig(ctx, "default")
	if cfgErr != nil {
		// Fall back to initializing from environment
		cfg, cfgErr = awsbackend.InitializeConfig(ctx, os.Getenv("AWS_PROFILE"), os.Getenv("AWS_REGION"), os.Getenv("AWS_ROLE"), os.Getenv("AWS_MFA_SERIAL"))
		if cfgErr != nil {
			return aws.Config{}, "", fmt.Errorf("error during AWS config initialization: %w", cfgErr)
		}
	}
	return cfg, "default", nil
}

// awsSecretCacheKey builds the cache/dedup identity for a Secrets Manager
// fetch from the base secret ID and its query qualifiers. Only "stage" and
// "version" change what Secrets Manager returns for a given secret ID (see
// buildAwsSecretInput), so those are the only qualifiers folded into the
// identity: "db?version=1" and "db?version=2" must never share a cache
// entry, but "db?version=1&key=username" and "db?version=1&key=password"
// should, since "key" only selects a field from the already-fetched value
// (see AwsOperator.Run's subkey extraction, which runs after this cache
// lookup) and does not change the request sent to AWS. Reading fields with
// params.Get rather than using the raw query string keeps param order from
// mattering, so "db?version=1&stage=x" and "db?stage=x&version=1" produce
// the same identity. An unqualified secret keeps the exact bare-key
// identity it had before this fix, so existing cache entries for
// unqualified specs are unaffected.
//
// version is folded in only when stage is absent, mirroring
// buildAwsSecretInput's own precedence: stage wins when both are present,
// and buildAwsSecretInput never looks at version in that case, so
// "db?stage=x&version=1" and "db?stage=x&version=2" send the identical
// request to Secrets Manager and must share one cache entry, not fragment
// into two independent fetches for what is really the same request.
func awsSecretCacheKey(secret string, params url.Values) string {
	stage := params.Get("stage")
	version := params.Get("version")
	if stage == "" {
		if version == "" {
			return secret
		}
		return secret + "\x00version=" + version
	}
	return secret + "\x00stage=" + stage
}

// buildAwsSecretInput builds the Secrets Manager GetSecretValueInput for a
// getAwsSecret fetch, applying the same stage/version precedence
// (stage wins when both are present) that awsSecretCacheKey's identity
// covers.
func buildAwsSecretInput(secret string, params url.Values) *secretsmanager.GetSecretValueInput {
	input := &secretsmanager.GetSecretValueInput{
		SecretId: aws.String(secret),
	}

	if stage := params.Get("stage"); stage != "" {
		input.VersionStage = aws.String(stage)
	} else if version := params.Get("version"); version != "" {
		input.VersionId = aws.String(version)
	}

	return input
}

// getAwsSecret fetches a secret using the AWS backend cache and the given
// aws.Config. cacheTarget namespaces the secret cache (see
// resolveConfig). GetOrFetchSecret both serves cached values without a
// network call and coalesces concurrent requests for the same (target,
// cache identity) into one backend request, rather than exposing the raw
// cache map for an unsynchronized read as the previous implementation did.
// The cache identity passed to GetOrFetchSecret is computed by
// awsSecretCacheKey, not the bare secret string, so two specs for the same
// secret ID that request different stage/version qualifiers (e.g.
// "db?version=1" vs "db?version=2") get independent cache entries and
// independent fetches instead of colliding on one.
// skipCache (the ":nocache" modifier) bypasses GetOrFetchSecret entirely -
// no cache read, no cache write, no request coalescing - so the lookup
// neither serves from nor refreshes the shared entry.
//
// The Secrets Manager client is built fresh from cfg on every fetch
// (rather than cached alongside cfg itself), through
// awsbackend.NewSecretsManagerClient so tests can swap that factory var
// for a fake.
func (o AwsOperator) getAwsSecret(ctx context.Context, cfg aws.Config, cacheTarget, secret string, params url.Values, skipCache bool) (string, error) {
	fetch := func() (string, error) {
		client := awsbackend.NewSecretsManagerClient(cfg)

		output, err := client.GetSecretValue(ctx, buildAwsSecretInput(secret, params))
		if err != nil {
			return "", err
		}

		return aws.ToString(output.SecretString), nil
	}
	if skipCache {
		return fetch()
	}
	return awsbackend.DefaultPool.GetOrFetchSecret(cacheTarget, awsSecretCacheKey(secret, params), fetch)
}

// getAwsParam fetches a parameter using the AWS backend cache and the
// given aws.Config. cacheTarget namespaces the parameter cache (see
// resolveConfig); see getAwsSecret for the GetOrFetchParam cache+dedup
// behavior, the skipCache (":nocache") bypass semantics, and why the SSM
// client is built fresh from cfg on every fetch through
// awsbackend.NewSSMClient rather than cached.
func (o AwsOperator) getAwsParam(ctx context.Context, cfg aws.Config, cacheTarget, param string, skipCache bool) (string, error) {
	fetch := func() (string, error) {
		client := awsbackend.NewSSMClient(cfg)

		input := &ssm.GetParameterInput{
			Name:           aws.String(param),
			WithDecryption: aws.Bool(true),
		}

		output, err := client.GetParameter(ctx, input)
		if err != nil {
			return "", err
		}
		if output.Parameter == nil || output.Parameter.Value == nil {
			return "", fmt.Errorf("parameter %s has no value", param)
		}

		return aws.ToString(output.Parameter.Value), nil
	}
	if skipCache {
		return fetch()
	}
	return awsbackend.DefaultPool.GetOrFetchParam(cacheTarget, param, fetch)
}

// NewAwsParamOperator creates a new AWS Parameter Store operator.
func NewAwsParamOperator() *AwsOperator {
	return &AwsOperator{variant: "awsparam"}
}

// NewAwsSecretOperator creates a new AWS Secrets Manager operator.
func NewAwsSecretOperator() *AwsOperator {
	return &AwsOperator{variant: "awssecret"}
}

//nolint:gochecknoinits // Operator registration must happen at package load time
func init() {
	// Register the two variants of the AwsOperator
	RegisterOp("awsparam", AwsOperator{variant: "awsparam"})
	RegisterOp("awssecret", AwsOperator{variant: "awssecret"})
}
