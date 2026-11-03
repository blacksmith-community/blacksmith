package aws

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// ClientPool manages AWS configs and clients for different targets.
type ClientPool struct {
	mu           sync.RWMutex
	awsConfigs   map[string]aws.Config
	configs      map[string]*Target
	secretsCache map[string]map[string]string // target -> secret -> value
	paramsCache  map[string]map[string]string // target -> param -> value
}

// DefaultPool is the global client pool for target-aware AWS connections.
var DefaultPool = &ClientPool{
	awsConfigs:   make(map[string]aws.Config),
	configs:      make(map[string]*Target),
	secretsCache: make(map[string]map[string]string),
	paramsCache:  make(map[string]map[string]string),
}

// GetConfig returns an aws.Config for the specified target, building and
// caching it on first use via BuildConfig.
func (acp *ClientPool) GetConfig(ctx context.Context, targetName string) (aws.Config, error) {
	acp.mu.RLock()
	if cfg, exists := acp.awsConfigs[targetName]; exists {
		acp.mu.RUnlock()
		return cfg, nil
	}
	acp.mu.RUnlock()

	// Get target configuration
	targetConfig, err := acp.GetTargetConfig(targetName)
	if err != nil {
		return aws.Config{}, fmt.Errorf("AWS target '%s' not found: %w", targetName, err)
	}

	// Build an aws.Config from target config
	cfg, err := acp.BuildConfig(ctx, targetConfig)
	if err != nil {
		return aws.Config{}, fmt.Errorf("failed to build AWS config for target '%s': %w", targetName, err)
	}

	// Store config for reuse
	acp.mu.Lock()
	acp.awsConfigs[targetName] = cfg
	acp.configs[targetName] = targetConfig
	acp.mu.Unlock()

	return cfg, nil
}

// GetTargetConfig retrieves target configuration from environment variables.
func (acp *ClientPool) GetTargetConfig(targetName string) (*Target, error) {
	// Check if we have cached config
	acp.mu.RLock()
	if targetConfig, exists := acp.configs[targetName]; exists {
		acp.mu.RUnlock()
		return targetConfig, nil
	}
	acp.mu.RUnlock()

	// Use environment variables with target suffix
	envPrefix := fmt.Sprintf("AWS_%s_", strings.ToUpper(targetName))

	// Check if any AWS target-specific environment variables are set
	region := os.Getenv(envPrefix + "REGION")
	profile := os.Getenv(envPrefix + "PROFILE")
	role := os.Getenv(envPrefix + "ROLE")
	accessKeyID := os.Getenv(envPrefix + "ACCESS_KEY_ID")

	// Require at least one target-specific configuration
	if region == "" && profile == "" && role == "" && accessKeyID == "" {
		return nil, fmt.Errorf("AWS target '%s' configuration incomplete (expected %sREGION, %sPROFILE, %sROLE, or %sACCESS_KEY_ID environment variable)",
			targetName, envPrefix, envPrefix, envPrefix, envPrefix)
	}

	mfaSerial, mfaToken := resolveTargetMFAEnv(targetName, envPrefix)

	targetConfig := &Target{
		Region:             GetEnvOrDefault(envPrefix+"REGION", ""),
		Profile:            GetEnvOrDefault(envPrefix+"PROFILE", ""),
		Role:               GetEnvOrDefault(envPrefix+"ROLE", ""),
		AccessKeyID:        GetEnvOrDefault(envPrefix+"ACCESS_KEY_ID", ""),
		SecretAccessKey:    GetEnvOrDefault(envPrefix+"SECRET_ACCESS_KEY", ""),
		SessionToken:       GetEnvOrDefault(envPrefix+"SESSION_TOKEN", ""),
		Endpoint:           GetEnvOrDefault(envPrefix+"ENDPOINT", ""),
		DisableSSL:         ParseBoolOrDefault(GetEnvOrDefault(envPrefix+"DISABLE_SSL", "false")),
		MaxRetries:         ParseIntOrDefault(GetEnvOrDefault(envPrefix+"MAX_RETRIES", "3"), 3),
		HTTPTimeout:        ParseDurationOrDefault(GetEnvOrDefault(envPrefix+"HTTP_TIMEOUT", "30s"), 30*time.Second),
		AssumeRoleDuration: ParseDurationOrDefault(GetEnvOrDefault(envPrefix+"ASSUME_ROLE_DURATION", "1h"), 1*time.Hour),
		ExternalID:         GetEnvOrDefault(envPrefix+"EXTERNAL_ID", ""),
		SessionName:        GetEnvOrDefault(envPrefix+"SESSION_NAME", "graft-"+targetName),
		MfaSerial:          mfaSerial,
		MfaToken:           mfaToken,
		EnvPrefix:          envPrefix,
	}

	return targetConfig, nil
}

// resolveTargetMFAEnv resolves a target's MFA serial and one-shot MFA
// token from the environment. Both support the target-prefixed spelling
// (envPrefix+"MFA_SERIAL"/"MFA_TOKEN"), which always wins when set. The
// "default" target (case-insensitive) additionally falls back to the
// plain "AWS_MFA_SERIAL"/"AWS_MFA_TOKEN" spelling when the prefixed one
// is unset (D1): op_aws.go resolves an empty "@target" through
// GetTargetConfig("default") before falling back to InitializeConfig's
// own un-namespaced AWS_PROFILE/AWS_REGION/AWS_ROLE path, so a user who
// already set the plain MFA variables (matching that fallback path's own
// naming) gets the same MFA support without also having to set the
// AWS_DEFAULT_ prefixed spelling. Every other target name only ever
// reads its own prefixed spelling, never the plain one - a target named
// e.g. "prod" must not pick up a plain AWS_MFA_SERIAL some other part of
// the environment set for an unrelated purpose.
func resolveTargetMFAEnv(targetName, envPrefix string) (serial, token string) {
	serial = os.Getenv(envPrefix + "MFA_SERIAL")
	token = os.Getenv(envPrefix + "MFA_TOKEN")
	if !strings.EqualFold(targetName, "default") {
		return serial, token
	}
	if serial == "" {
		serial = os.Getenv("AWS_MFA_SERIAL")
	}
	if token == "" {
		token = os.Getenv("AWS_MFA_TOKEN")
	}
	return serial, token
}

// endpointFor returns the endpoint to configure on aws.Config for t. An
// empty Endpoint returns "", telling BuildConfig not to call
// config.WithBaseEndpoint at all. A scheme-less Endpoint (no "://") gets
// "http://" or "https://" prepended depending on DisableSSL - v1 ran every
// custom endpoint through endpoints.AddScheme before use, so a bare
// "host:port" (the common AWS_{TARGET}_ENDPOINT spelling for a local
// stand-in like LocalStack) worked; v2's config.WithBaseEndpoint takes the
// string verbatim, and a scheme-less value fails with "unsupported
// protocol scheme", so this restores v1's behavior ahead of that call. A
// DisableSSL target additionally rewrites an explicit "https://" Endpoint's
// scheme to "http://" (the narrowed v2 meaning of the v1 DisableSSL flag -
// v2 has no direct equivalent, and AWS proper does not serve plaintext, so
// DisableSSL alone with no Endpoint set is a documented no-op). Any other
// Endpoint (one already using "http://", or one using "https://" with
// DisableSSL unset) is returned verbatim.
func endpointFor(t *Target) string {
	if t.Endpoint == "" {
		return ""
	}
	if !strings.Contains(t.Endpoint, "://") {
		if t.DisableSSL {
			return "http://" + t.Endpoint
		}
		return "https://" + t.Endpoint
	}
	if t.DisableSSL && strings.HasPrefix(t.Endpoint, "https://") {
		return "http://" + strings.TrimPrefix(t.Endpoint, "https://")
	}
	return t.Endpoint
}

// mfaTokenEnvVarName returns the environment variable name BuildConfig's
// MFA error messages should name for target, using target.EnvPrefix (set
// by GetTargetConfig) when available. The "default" target additionally
// accepts the plain "AWS_MFA_TOKEN" spelling (see GetTargetConfig's D1
// comment), so its message names both. A Target built directly rather
// than through GetTargetConfig (as most BuildConfig-level tests do) has
// an empty EnvPrefix, and falls back to the generic "AWS_MFA_TOKEN" name.
func mfaTokenEnvVarName(target *Target) string {
	if target.EnvPrefix == "" {
		return "AWS_MFA_TOKEN"
	}
	name := target.EnvPrefix + "MFA_TOKEN"
	if strings.EqualFold(target.EnvPrefix, "AWS_DEFAULT_") {
		return name + " (or AWS_MFA_TOKEN)"
	}
	return name
}

// assumeRoleCredentialOptions returns the callback passed to
// config.WithAssumeRoleCredentialOptions. It is always registered
// (regardless of whether target.MfaSerial is set) because a profile-
// driven "mfa_serial" is resolved by the shared-config loader itself, not
// by target: the callback only learns of it via o.SerialNumber, already
// populated by the loader by the time the callback runs, so that case's
// provider cannot be built up front the way mfaProvider (target.MfaSerial's
// provider, or nil) is.
func assumeRoleCredentialOptions(target *Target, mfaProvider func() (string, error)) func(*stscreds.AssumeRoleOptions) {
	return func(o *stscreds.AssumeRoleOptions) {
		switch {
		case target.MfaSerial != "":
			o.SerialNumber = aws.String(target.MfaSerial)
			o.TokenProvider = mfaProvider
		case o.SerialNumber != nil && *o.SerialNumber != "":
			// A profile's "mfa_serial" with no target.MfaSerial override:
			// there is no target-prefixed MFA_TOKEN env var to read here
			// (this Target carries no serial to derive one from), only
			// the un-namespaced AWS_MFA_TOKEN.
			o.TokenProvider = mfaTokenProvider(*o.SerialNumber, os.Getenv("AWS_MFA_TOKEN"), "AWS_MFA_TOKEN")
		}
	}
}

// roleAssumeOptions returns the callback passed to
// stscreds.NewAssumeRoleProvider for target's explicit Role assumption:
// AssumeRoleDuration/ExternalID/SessionName when set, plus target.MfaSerial's
// SerialNumber/mfaProvider when target.MfaSerial is set.
func roleAssumeOptions(target *Target, mfaProvider func() (string, error)) func(*stscreds.AssumeRoleOptions) {
	return func(o *stscreds.AssumeRoleOptions) {
		if target.AssumeRoleDuration > 0 {
			o.Duration = target.AssumeRoleDuration
		}
		if target.ExternalID != "" {
			o.ExternalID = aws.String(target.ExternalID)
		}
		if target.SessionName != "" {
			o.RoleSessionName = target.SessionName
		}
		if target.MfaSerial != "" {
			o.SerialNumber = aws.String(target.MfaSerial)
			o.TokenProvider = mfaProvider
		}
	}
}

// BuildConfig builds an aws.Config from target configuration, composing
// config.LoadDefaultConfig with the target's region, profile, endpoint,
// retry, and credential settings, and wrapping the result in a role
// assumption provider (backed by aws.NewCredentialsCache, so repeated
// calls do not each re-run sts:AssumeRole) when target.Role is set. When
// target.MfaSerial is also set, both that explicit role-assumption
// provider and config.WithAssumeRoleCredentialOptions (which supplies the
// same TokenProvider to any AssumeRole the shared-config loader itself
// performs for a profile-driven "mfa_serial", independent of target.Role)
// are given an MFA token provider built by mfaTokenProvider.
func (acp *ClientPool) BuildConfig(ctx context.Context, target *Target) (aws.Config, error) {
	var opts []func(*config.LoadOptions) error

	// Configure region
	if target.Region != "" {
		opts = append(opts, config.WithRegion(target.Region))
	}

	// Configure profile
	if target.Profile != "" {
		opts = append(opts, config.WithSharedConfigProfile(target.Profile))
	}

	// Configure endpoint (for testing or custom endpoints)
	if ep := endpointFor(target); ep != "" {
		opts = append(opts, config.WithBaseEndpoint(ep))
	}

	// Configure retries: MaxRetries preserves its documented "number of
	// retries" meaning by mapping to RetryMaxAttempts = MaxRetries+1 (v2's
	// "total attempts" semantics). The "if > 0" guard below matches v1's
	// own guard, but the fallback default it leaves in effect does not: a
	// non-positive MaxRetries here leaves v2's own default of 3 total
	// attempts in effect, one fewer than v1's fall-through default of 3
	// retries (4 attempts). In practice GetTargetConfig's
	// AWS_{TARGET}_MAX_RETRIES parsing defaults an unset env var to the
	// string "3" rather than leaving MaxRetries at its zero value, so
	// that path still reaches RetryMaxAttempts(4), matching v1 - this
	// non-positive branch is only reached by a Target built directly with
	// MaxRetries left unset (e.g. most BuildConfig-level tests), or with
	// MaxRetries explicitly set to 0 or a negative value.
	if target.MaxRetries > 0 {
		opts = append(opts, config.WithRetryMaxAttempts(target.MaxRetries+1))
	}

	// Configure the HTTP client's request timeout. A non-positive value
	// (Target's zero value, or an explicit 0/negative override) leaves
	// cfg.HTTPClient unset, so the SDK's own default HTTP client - which
	// has no client-side deadline of its own - stays in effect.
	if target.HTTPTimeout > 0 {
		opts = append(opts, config.WithHTTPClient(awshttp.NewBuildableClient().WithTimeout(target.HTTPTimeout)))
	}

	// Configure credentials if provided
	if target.AccessKeyID != "" && target.SecretAccessKey != "" {
		opts = append(opts, config.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(target.AccessKeyID, target.SecretAccessKey, target.SessionToken),
		))
	}

	// Build the MFA token provider once (if MfaSerial is set) so both the
	// shared-config loader's own AssumeRole handling (a profile-driven
	// "mfa_serial") and the explicit target.Role branch below use the
	// identical provider - and therefore the identical one-shot env
	// token, if any - rather than each independently trying to consume it.
	//
	// WithAssumeRoleCredentialOptions is registered unconditionally
	// (not just when target.MfaSerial != "") because a profile-driven
	// "mfa_serial" is resolved by the shared-config loader itself, not by
	// this Target - see assumeRoleCredentialOptions's doc comment.
	var mfaProvider func() (string, error)
	if target.MfaSerial != "" {
		mfaProvider = mfaTokenProvider(target.MfaSerial, target.MfaToken, mfaTokenEnvVarName(target))
	}
	opts = append(opts, config.WithAssumeRoleCredentialOptions(assumeRoleCredentialOptions(target, mfaProvider)))

	cfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return aws.Config{}, err
	}

	// Configure role assumption if provided
	if target.Role != "" {
		stsClient := sts.NewFromConfig(cfg) // inherits BaseEndpoint, matching v1's global Endpoint reaching STS
		provider := stscreds.NewAssumeRoleProvider(stsClient, target.Role, roleAssumeOptions(target, mfaProvider))
		// REQUIRED: without this cache, every SSM/Secrets Manager call
		// through cfg would re-run sts:AssumeRole.
		cfg.Credentials = aws.NewCredentialsCache(provider)
	}

	return cfg, nil
}

// InitializeConfig builds an aws.Config honoring shared config (e.g.
// ~/.aws/credentials), an optional profile, an optional region, and an
// optional STS AssumeRole, matching the plain-environment ("AWS_PROFILE"/
// "AWS_REGION"/"AWS_ROLE", not the "AWS_<TARGET>_*" family) resolution
// path op_aws.go falls back to when no pooled "default" target config is
// available. mfaSerial, when non-empty, protects that AssumeRole (and any
// profile-driven "mfa_serial" the shared-config loader resolves on its
// own) with an MFA token read once from AWS_MFA_TOKEN, or - failing that
// - an interactive stderr prompt when stdin is a terminal, or a clear
// error naming AWS_MFA_TOKEN; this is the un-namespaced counterpart to
// BuildConfig's target-prefixed MFA support (D1).
func InitializeConfig(ctx context.Context, profile string, region string, role string, mfaSerial string) (aws.Config, error) {
	var opts []func(*config.LoadOptions) error

	if region != "" {
		opts = append(opts, config.WithRegion(region))
	}

	if profile != "" {
		opts = append(opts, config.WithSharedConfigProfile(profile))
	}

	var mfaProvider func() (string, error)
	if mfaSerial != "" {
		mfaProvider = mfaTokenProvider(mfaSerial, os.Getenv("AWS_MFA_TOKEN"), "AWS_MFA_TOKEN")
		opts = append(opts, config.WithAssumeRoleCredentialOptions(func(o *stscreds.AssumeRoleOptions) {
			o.SerialNumber = aws.String(mfaSerial)
			o.TokenProvider = mfaProvider
		}))
	}

	cfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return aws.Config{}, err
	}

	if role != "" {
		stsClient := sts.NewFromConfig(cfg)
		provider := stscreds.NewAssumeRoleProvider(stsClient, role, func(o *stscreds.AssumeRoleOptions) {
			if mfaSerial != "" {
				o.SerialNumber = aws.String(mfaSerial)
				o.TokenProvider = mfaProvider
			}
		})
		cfg.Credentials = aws.NewCredentialsCache(provider)
	}

	return cfg, nil
}
