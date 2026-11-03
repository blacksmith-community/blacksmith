package aws

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
)

// SecretsManagerClient abstracts the subset of the aws-sdk-go-v2 Secrets
// Manager client this package needs. The real *secretsmanager.Client
// satisfies this interface implicitly (it already has this method
// signature); tests swap in a fake instead.
type SecretsManagerClient interface {
	GetSecretValue(ctx context.Context, params *secretsmanager.GetSecretValueInput, optFns ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error)
}

// SSMClient abstracts the subset of the aws-sdk-go-v2 SSM client this
// package needs. The real *ssm.Client satisfies this interface implicitly;
// tests swap in a fake instead.
type SSMClient interface {
	GetParameter(ctx context.Context, params *ssm.GetParameterInput, optFns ...func(*ssm.Options)) (*ssm.GetParameterOutput, error)
}

// NewSecretsManagerClient builds the production Secrets Manager client
// from cfg. It is a package-level var (not a plain function) so tests can
// swap it for a fake that returns a stub implementing
// SecretsManagerClient; callers MUST restore the original value (e.g. via
// t.Cleanup) after swapping it, since it is shared, unsynchronized package
// state.
var NewSecretsManagerClient = func(cfg aws.Config) SecretsManagerClient {
	return secretsmanager.NewFromConfig(cfg)
}

// NewSSMClient builds the production SSM client from cfg. See
// NewSecretsManagerClient's doc comment for why this is a swappable
// package-level var rather than a function, and the same restore
// obligation on callers that swap it.
var NewSSMClient = func(cfg aws.Config) SSMClient {
	return ssm.NewFromConfig(cfg)
}
