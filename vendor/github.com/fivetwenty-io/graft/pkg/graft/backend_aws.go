package graft

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	smtypes "github.com/aws/aws-sdk-go-v2/service/secretsmanager/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/fivetwenty-io/graft/internal/utils/mfa"
)

// mfaPromptIO is the interactive-prompt IO buildAWSConfig's MFA fallback
// uses when AWSConfig.MFATokenProvider is nil, package-level and
// swappable in tests (restore via t.Cleanup, since it is shared,
// unsynchronized package state) for a fake reader/writer/isTTY so MFA
// tests never touch a real terminal. Production code leaves it at
// mfa.DefaultPromptIO(): stdin for input, stderr for the prompt (stdout
// carries a QuickMerge/engine caller's own output and must never receive
// a prompt), golang.org/x/term for the TTY check.
var mfaPromptIO = mfa.DefaultPromptIO()

// AWSConfig.AccessKeyID/SecretAccessKey/SessionToken/PoolSize are added
// here (rather than in the AWSConfig struct's own doc comment above, in
// api.go, where AWSConfig is declared) to keep the WithAWS/WithAWSTarget
// machinery that gives them an effect next to their documentation. Adding
// fields to an already-shipped exported struct is non-breaking as long as
// no caller uses an unkeyed composite literal; the only two literals in
// this repo are both keyed - examples.go:221 and engine_test.go:997.

// awsOptionBackendKinds enumerates the two Backend names WithAWS/
// WithAWSTarget register - one per AWS operator variant
// (pkg/graft/operators/op_aws.go's AwsOperator.variant). Both share one
// underlying *awsConfigStore (one WithAWS call configures both), but are
// registered as two independent Backend entries because the vault/AWS/NATS
// operators look a custom backend up by their own operator name (see
// Backend.Name's doc comment).
var awsOptionBackendKinds = [2]string{"awsparam", "awssecret"}

// awsSSMAPI and awsSecretsAPI and awsSTSAPI are unexported mirrors of
// internal/backends/aws.SSMClient/SecretsManagerClient and the sts
// client's GetCallerIdentity method, existing only because this package
// cannot import internal/backends/aws (see awsOptionBackend's doc comment
// for the import-cycle reason). The real aws-sdk-go-v2 clients
// (ssm.NewFromConfig, secretsmanager.NewFromConfig, sts.NewFromConfig)
// satisfy these implicitly; tests swap the newAWS*Client factory vars
// below for internal/backends/aws/awsfakes fakes, which also satisfy
// these interfaces structurally (same method signatures) without either
// package importing the other.
type awsSSMAPI interface {
	GetParameter(ctx context.Context, params *ssm.GetParameterInput, optFns ...func(*ssm.Options)) (*ssm.GetParameterOutput, error)
}

type awsSecretsAPI interface {
	GetSecretValue(ctx context.Context, params *secretsmanager.GetSecretValueInput, optFns ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error)
}

type awsSTSAPI interface {
	GetCallerIdentity(ctx context.Context, params *sts.GetCallerIdentityInput, optFns ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error)
}

// newAWSSSMClient, newAWSSecretsClient, and newAWSSTSClient are the
// production client factories, swappable in tests (with a t.Cleanup
// restore, since they are shared, unsynchronized package state) for a
// fake satisfying the corresponding awsSSMAPI/awsSecretsAPI/awsSTSAPI
// interface.
var (
	newAWSSSMClient     = func(cfg aws.Config) awsSSMAPI { return ssm.NewFromConfig(cfg) }
	newAWSSecretsClient = func(cfg aws.Config) awsSecretsAPI { return secretsmanager.NewFromConfig(cfg) }
	newAWSSTSClient     = func(cfg aws.Config) awsSTSAPI { return sts.NewFromConfig(cfg) }
)

// awsConfigStore holds the AWSConfig and lazily-built aws.Config per
// target ("" is WithAWS's own default; other keys are WithAWSTarget
// names), shared by both of a NewEngine call's "awsparam" and "awssecret"
// awsOptionBackend instances so a single WithAWS/WithAWSTarget call
// configures both operators' backends at once.
type awsConfigStore struct {
	mu         sync.RWMutex
	configs    map[string]AWSConfig
	awsConfigs map[string]aws.Config
}

func newAWSConfigStore() *awsConfigStore {
	return &awsConfigStore{
		configs:    make(map[string]AWSConfig),
		awsConfigs: make(map[string]aws.Config),
	}
}

func (s *awsConfigStore) setConfig(target string, cfg AWSConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.configs[target] = cfg
	delete(s.awsConfigs, target)
}

func (s *awsConfigStore) configFor(ctx context.Context, target string) (aws.Config, error) {
	s.mu.RLock()
	if cfg, ok := s.awsConfigs[target]; ok {
		s.mu.RUnlock()
		return cfg, nil
	}
	cfg, configured := s.configs[target]
	s.mu.RUnlock()

	if !configured {
		if target == "" {
			return aws.Config{}, fmt.Errorf("aws: no configuration set; call WithAWS")
		}
		return aws.Config{}, fmt.Errorf("aws: target %q not configured; call WithAWSTarget(%q, ...)", target, target)
	}

	awsCfg, err := buildAWSConfig(ctx, cfg)
	if err != nil {
		return aws.Config{}, err
	}

	s.mu.Lock()
	// Re-check: a concurrent caller may have built (and stored) a config
	// for the same target while this one was under construction; keep
	// whichever was stored first so all callers converge on one config.
	if existing, ok := s.awsConfigs[target]; ok {
		s.mu.Unlock()
		return existing, nil
	}
	s.awsConfigs[target] = awsCfg
	s.mu.Unlock()

	return awsCfg, nil
}

// buildAWSConfig constructs an aws.Config from cfg. Region is left unset
// (deferring to the SDK's own environment/shared-config resolution) when
// cfg.Region is "" - the AWS SDK errors out per-call with "MissingRegion"
// if nothing supplies one, which is the same failure the built-in
// internal/backends/aws path has when neither a target env var nor
// AWS_REGION is set.
//
// Not carried over from internal/backends/aws.Target (see
// awsOptionBackend's doc comment for why this is a from-scratch builder,
// not an import of that package): MaxRetries, HTTPTimeout,
// AssumeRoleDuration/ExternalID/SessionName (role-assumption tuning
// beyond MFA). Role assumption itself IS carried
// over (a bare sts:AssumeRole, optionally MFA-protected via
// cfg.MFASerial/cfg.MFATokenProvider) since it is a real, commonly-needed
// capability; the rest are cut because they would be silent no-ops on
// AWSConfig's current field set without adding fields the plan never
// asked for - flagged in the WithAWS doc comment rather than added
// speculatively.
//
// Endpoint reaches config.WithBaseEndpoint verbatim: unlike
// internal/backends/aws.BuildConfig, there is no DisableSSL field on
// AWSConfig to rewrite an "https://" scheme from, so whatever scheme the
// caller wrote in cfg.Endpoint is what the SDK is given.
func buildAWSConfig(ctx context.Context, cfg AWSConfig) (aws.Config, error) {
	var opts []func(*config.LoadOptions) error

	if cfg.Region != "" {
		opts = append(opts, config.WithRegion(cfg.Region))
	}
	if cfg.Profile != "" {
		opts = append(opts, config.WithSharedConfigProfile(cfg.Profile))
	}
	if cfg.Endpoint != "" {
		opts = append(opts, config.WithBaseEndpoint(cfg.Endpoint))
	}
	if cfg.PoolSize > 0 {
		opts = append(opts, config.WithHTTPClient(
			awshttp.NewBuildableClient().WithTransportOptions(func(tr *http.Transport) {
				tr.MaxIdleConnsPerHost = cfg.PoolSize
				tr.MaxIdleConns = cfg.PoolSize
			}),
		))
	}

	switch {
	case cfg.SkipAuth:
		opts = append(opts, config.WithCredentialsProvider(aws.AnonymousCredentials{}))
	case cfg.AccessKeyID != "" && cfg.SecretAccessKey != "":
		opts = append(opts, config.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, cfg.SessionToken),
		))
	}

	// Build the MFA token provider once (if MFASerial is set) so it is
	// used consistently wherever MFA-protected role assumption happens.
	// cfg.MFATokenProvider is used verbatim when supplied; otherwise this
	// falls back to the same env-token/interactive-prompt/clear-error
	// resolution internal/backends/aws.BuildConfig's environment-driven
	// path uses (via the shared internal/utils/mfa package), reading the
	// one-shot code from AWS_MFA_TOKEN since AWSConfig, unlike
	// internal/backends/aws.Target, has no per-target env-var namespace
	// to read a prefixed spelling from.
	var mfaProvider func() (string, error)
	if cfg.MFASerial != "" {
		mfaProvider = cfg.MFATokenProvider
		if mfaProvider == nil {
			mfaProvider = mfa.TokenProvider(cfg.MFASerial, os.Getenv("AWS_MFA_TOKEN"), "AWS_MFA_TOKEN", mfaPromptIO)
		}
		opts = append(opts, config.WithAssumeRoleCredentialOptions(func(o *stscreds.AssumeRoleOptions) {
			o.SerialNumber = aws.String(cfg.MFASerial)
			o.TokenProvider = mfaProvider
		}))
	}

	awsCfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return aws.Config{}, fmt.Errorf("aws: failed to load config: %w", err)
	}

	if cfg.Role != "" {
		stsClient := sts.NewFromConfig(awsCfg)
		provider := stscreds.NewAssumeRoleProvider(stsClient, cfg.Role, func(o *stscreds.AssumeRoleOptions) {
			if cfg.MFASerial != "" {
				o.SerialNumber = aws.String(cfg.MFASerial)
				o.TokenProvider = mfaProvider
			}
		})
		// REQUIRED: without this cache, every SSM/Secrets Manager call
		// through awsCfg would re-run sts:AssumeRole.
		awsCfg.Credentials = aws.NewCredentialsCache(provider)
	}

	return awsCfg, nil
}

// awsOptionBackend is the graft.Backend registered under "awsparam" or
// "awssecret" by WithAWS/WithAWSTarget - see awsConfigStore's doc comment
// for why one WithAWS call produces two Backend registrations sharing one
// store, and buildAWSConfig's doc comment for the internal/backends/aws
// import-cycle reason this is a from-scratch client builder rather than a
// reuse of that package (the same reason vaultOptionBackend gives - see
// backend_vault.go).
//
// A key reaches Get/GetWithTarget already stripped of its "?stage=...&
// key=..." query suffix by AwsOperator.Run (see op_aws.go's
// resolveCustomBackend call site) - stage/version secret-selection and the
// "?key=..." post-fetch subkey extraction are handled uniformly by the
// operator for both the built-in path and any custom backend, so
// awsOptionBackend never sees them and always fetches the latest/default
// version.
type awsOptionBackend struct {
	store *awsConfigStore
	kind  string // "awsparam" or "awssecret" - see awsOptionBackendKinds.
}

// Name implements Backend.
func (b *awsOptionBackend) Name() string { return b.kind }

// Get implements Backend using the "" (WithAWS default) configuration.
func (b *awsOptionBackend) Get(ctx context.Context, key string) (interface{}, error) {
	return b.fetch(ctx, "", key)
}

// GetWithTarget implements TargetedBackend using the WithAWSTarget
// configuration registered under target. An empty target falls back to
// Get, matching vaultOptionBackend.GetWithTarget's same defensive
// behavior for direct (non-operator) callers.
func (b *awsOptionBackend) GetWithTarget(ctx context.Context, target, key string) (interface{}, error) {
	return b.fetch(ctx, target, key)
}

func (b *awsOptionBackend) fetch(ctx context.Context, target, key string) (interface{}, error) {
	cfg, err := b.store.configFor(ctx, target)
	if err != nil {
		return nil, err
	}

	switch b.kind {
	case "awsparam":
		return getAWSOptionParam(ctx, cfg, key)
	case "awssecret":
		return getAWSOptionSecret(ctx, cfg, key)
	default:
		// Unreachable outside this file: only newAWSOptionBackends
		// constructs awsOptionBackend, and only with a kind from
		// awsOptionBackendKinds.
		return nil, fmt.Errorf("aws: backend has unknown kind %q", b.kind)
	}
}

// GetBatch implements Backend using SequentialGetBatch - see
// Backend.GetBatch's doc comment for why (no call site to design real
// batching against).
func (b *awsOptionBackend) GetBatch(ctx context.Context, keys []string) (map[string]interface{}, error) {
	return SequentialGetBatch(ctx, keys, b.Get)
}

// Health implements Backend by calling sts:GetCallerIdentity against the
// "" (WithAWS default) target's config - the AWS equivalent of "can this
// credential authenticate at all", since neither SSM nor Secrets Manager
// has a service-level health/ping call. A store configured only via
// WithAWSTarget (no WithAWS) has no default to check, matching
// vaultOptionBackend.Health's same behavior.
func (b *awsOptionBackend) Health(ctx context.Context) error {
	cfg, err := b.store.configFor(ctx, "")
	if err != nil {
		return err
	}
	_, err = newAWSSTSClient(cfg).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	return err
}

// Close implements Backend. There is no persistent connection or
// background goroutine to release: aws-sdk-go-v2 clients are also thin
// wrappers over an HTTPClient, whose idle connections close themselves on
// Go's normal transport idle timeout.
func (b *awsOptionBackend) Close() error { return nil }

// getAWSOptionParam fetches an SSM Parameter Store value (with
// decryption, matching internal/backends/aws's own WithDecryption:true
// default). Error mapping via errors.As against the typed
// *ssmtypes.ParameterNotFound/*ssmtypes.ResourceNotFoundException errors
// the v2 SDK's JSON deserializer produces from the service's "__type"
// response field is load-bearing: it feeds ErrBackendNotFound to
// SequentialGetBatch (GetBatch, above) and to WithBackendRetry's
// non-retryable classification - see op_aws.go's "Who consumes
// ErrBackendNotFound for AWS" research note for the full trace.
func getAWSOptionParam(ctx context.Context, cfg aws.Config, name string) (string, error) {
	out, err := newAWSSSMClient(cfg).GetParameter(ctx, &ssm.GetParameterInput{
		Name:           aws.String(name),
		WithDecryption: aws.Bool(true),
	})
	if err != nil {
		var notFound *ssmtypes.ParameterNotFound
		var resourceNotFound *ssmtypes.ResourceNotFoundException
		if errors.As(err, &notFound) || errors.As(err, &resourceNotFound) {
			return "", fmt.Errorf("%w: %s", ErrBackendNotFound, name)
		}
		return "", fmt.Errorf("aws: GetParameter %q: %w", name, err)
	}
	if out.Parameter == nil || out.Parameter.Value == nil {
		return "", fmt.Errorf("%w: %s", ErrBackendNotFound, name)
	}
	return *out.Parameter.Value, nil
}

// getAWSOptionSecret fetches a Secrets Manager value. A binary secret
// (SecretBinary, no SecretString) is returned as its raw bytes converted
// to a string. This differs from the built-in awssecret path: getAwsSecret
// in op_aws.go returns only aws.ToString(output.SecretString), with no
// SecretBinary fallback, so a binary-only secret comes back as an empty
// string there, matching spruce's own behavior. See getAWSOptionParam's
// doc comment for why the *smtypes.ResourceNotFoundException mapping
// below is load-bearing.
func getAWSOptionSecret(ctx context.Context, cfg aws.Config, id string) (string, error) {
	out, err := newAWSSecretsClient(cfg).GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{
		SecretId: aws.String(id),
	})
	if err != nil {
		var resourceNotFound *smtypes.ResourceNotFoundException
		if errors.As(err, &resourceNotFound) {
			return "", fmt.Errorf("%w: %s", ErrBackendNotFound, id)
		}
		return "", fmt.Errorf("aws: GetSecretValue %q: %w", id, err)
	}
	if out.SecretString != nil {
		return *out.SecretString, nil
	}
	if out.SecretBinary != nil {
		return string(out.SecretBinary), nil
	}
	return "", fmt.Errorf("%w: %s", ErrBackendNotFound, id)
}

// awsConfigStoreFor returns the *awsConfigStore shared by opts.Backends'
// "awsparam"/"awssecret" entries. Behavior depends on what is already
// registered under those two names:
//   - If either name already holds an *awsOptionBackend, its store is
//     returned as-is - neither name is touched, so an earlier
//     WithBackend(myCustomAWSBackend) call under the *other* name (e.g. an
//     "awsparam" *awsOptionBackend alongside a hand-registered "awssecret"
//     Backend) is left exactly as the caller registered it, not replaced.
//     This is the one case that looks like "both replaced" is what should
//     happen (WithBackend/RegisterBackend's "last registration wins" rule)
//     but is not: this function only ever creates or leaves entries alone,
//     it never overwrites one that is already an *awsOptionBackend, and it
//     never touches a name it did not itself just create.
//   - Otherwise (neither name is already an *awsOptionBackend - including
//     when one or both hold an unrelated Backend from an earlier
//     WithBackend call), both names are (re)created backed by one new
//     store - this is the only branch that replaces an existing
//     non-*awsOptionBackend entry, and it always replaces both names
//     together, never just one.
func awsConfigStoreFor(opts *EngineOptions) *awsConfigStore {
	if opts.Backends == nil {
		opts.Backends = make(map[string]Backend)
	}
	for _, kind := range awsOptionBackendKinds {
		if existing, ok := opts.Backends[kind]; ok {
			if ob, ok := existing.(*awsOptionBackend); ok {
				return ob.store
			}
		}
	}

	store := newAWSConfigStore()
	for _, kind := range awsOptionBackendKinds {
		opts.Backends[kind] = &awsOptionBackend{store: store, kind: kind}
	}
	return store
}

// WithAWS registers Backends named "awsparam" and "awssecret", both built
// from config, that the awsparam/awssecret operators consult when
// features.FeatureBackendRegistry is enabled (see WithBackendRegistry) -
// see awsOptionBackend's and buildAWSConfig's doc comments for exactly
// what they do and do not carry over from internal/backends/aws's
// environment-configured path. Calling WithAWS more than once, or
// combining it with WithAWSTarget, applies to the same underlying store:
// the last call for a given target ("" for WithAWS, a name for
// WithAWSTarget) wins.
//
// WithAWS alone does not enable FeatureBackendRegistry: pair it with
// WithBackendRegistry(true) (or a supplied *features.FeatureFlags that
// already enables it) or the registered backends are built but never
// consulted.
//
// Also works with DefaultEngine.Configure - it registers through
// WithBackend under the hood, so it is subject to the same Configure
// wiring WithBackend's doc comment describes.
func WithAWS(cfg AWSConfig) EngineOption {
	return func(opts *EngineOptions) {
		awsConfigStoreFor(opts).setConfig("", cfg)
	}
}

// WithAWSTarget registers per-"@target" AWS configuration (e.g.
// `(( awsparam@name "/path" ))`) on the same "awsparam"/"awssecret"
// backends WithAWS registers, reachable via TargetedBackend.GetWithTarget.
// name must be non-empty ("" is WithAWS's own default target and is reset
// by calling WithAWS, not this); an empty name is a no-op. WithAWSTarget
// may be called before WithAWS; the backends are created on first use by
// either.
func WithAWSTarget(name string, cfg AWSConfig) EngineOption {
	return func(opts *EngineOptions) {
		if name == "" {
			return
		}
		awsConfigStoreFor(opts).setConfig(name, cfg)
	}
}
