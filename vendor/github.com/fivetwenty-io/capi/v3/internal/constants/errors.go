package constants

import "errors"

// API and configuration errors.
var (
	ErrNoAPIsConfigured    = errors.New("no APIs configured, use 'capi apis add' to add one")
	ErrNoDomainForAPI      = errors.New("could not determine API domain")
	ErrNoRefreshToken      = errors.New("no refresh token available for this API, please run 'capi login' again")
	ErrInvalidJWTFormat    = errors.New("invalid JWT format")
	ErrNoExpirationClaim   = errors.New("no expiration claim found")
	ErrFailedRetrieveToken = errors.New("failed to retrieve refreshed token")
	ErrAPIConfigNotFound   = errors.New("API configuration not found")
)

// UAA errors.
var (
	ErrNoUAAEndpoint      = errors.New("no UAA endpoint configured and unable to discover from CF API endpoint")
	ErrNoCFAPIEndpoint    = errors.New("no CF API endpoint provided")
	ErrSSLOnlyInDev       = errors.New("skipSSL is only allowed in development environments (set CAPI_DEV_MODE=true)")
	ErrCFAPIRequestFailed = errors.New("CF API root info request failed")
	ErrNoUAAInCFLinks     = errors.New("no UAA endpoint found in CF API links")
	ErrNoUAASpecified     = errors.New("no UAA endpoint specified")
	ErrUAAClientNotInit   = errors.New("UAA client not initialized")
	ErrNoUAAConfigured    = errors.New("no UAA endpoint configured. Use 'capi uaa target <url>' to set one")
	ErrNotAuthenticated   = errors.New("not authenticated. Use a token command to authenticate first")
)

// Validation errors.
var (
	ErrInvalidAutoApprove = errors.New("invalid value for --auto-approve")
	ErrInvalidAllowPublic = errors.New("invalid value for --allow-public")
	ErrInvalidActive      = errors.New("invalid value for --active")
	ErrInvalidVerified    = errors.New("invalid value for --verified")
)

// Required field errors.
var (
	ErrGroupRequired         = errors.New("--group flag is required")
	ErrExternalGroupRequired = errors.New("--external-group flag is required")
	ErrOriginRequired        = errors.New("--origin flag is required")
)

// Operation errors.
var (
	ErrUnsupportedResource                  = errors.New("unsupported resource type")
	ErrInvalidUserData                      = errors.New("invalid user data for operation")
	ErrInvalidGroupData                     = errors.New("invalid group data for operation")
	ErrInvalidClientData                    = errors.New("invalid client data for operation")
	ErrInvalidClientType                    = errors.New("invalid client type")
	ErrInvalidTypeAssertion                 = errors.New("invalid type assertion")
	ErrInvalidClientTypeForEnvVarGroups     = errors.New("invalid client type for environment variable groups")
	ErrInvalidEnvVarGroupsClientType        = errors.New("invalid environment variable groups client type")
	ErrInvalidRequestType                   = errors.New("invalid request type")
	ErrInvalidRequestTypeForApplyVisibility = errors.New("invalid request type for ApplyVisibility")
	ErrInvalidDataTypeExpectedUAAUser       = errors.New("invalid data type, expected uaa.User")
	ErrUserIDRequired                       = errors.New("user ID required for delete operation")
	ErrGroupIDRequired                      = errors.New("group ID required for delete operation")
	ErrClientIDRequired                     = errors.New("client ID required for delete operation")
	ErrUnsupportedOperation                 = errors.New("unsupported operation")
	ErrGroupNotFound                        = errors.New("group not found")
	ErrSpaceNotFound                        = errors.New("space not found")
	ErrApplicationNotFound                  = errors.New("application not found")
	ErrResourceNotFound                     = errors.New("resource not found")
	ErrInvalidResourceType                  = errors.New("invalid resource type")
	ErrNoOrganizationsFound                 = errors.New("no organizations found")
	ErrUnexpectedServiceCredentialBinding   = errors.New("unexpected return type from ServiceCredentialBindings().Create()")
)

// Task resource errors.
var (
	ErrInvalidResourceTypeForTasks    = errors.New("invalid resource type for tasks")
	ErrInvalidResourceTypeForDroplets = errors.New("invalid resource type for droplets")
	ErrInvalidResourceTypeForBuilds   = errors.New("invalid resource type for builds")
)

// UAA type errors.
var (
	ErrExpectedJWKPointer = errors.New("expected *uaa.JWK")
	ErrExpectedJWKSlice   = errors.New("expected []uaa.JWK")
)

// Implicit flow error.
var (
	ErrImplicitFlowManual = errors.New("implicit grant flow requires manual implementation.\n\nTo use implicit grant:\n1. Navigate to the UAA authorization URL\n2. Extract the token from the redirect URL\n3. Use 'capi config set uaa_token <token>' to store it")
)

// Client support errors.
var (
	ErrClientNoFeatureFlagsSupport      = errors.New("client does not support FeatureFlags")
	ErrFeatureFlagsNoListSupport        = errors.New("FeatureFlags client does not support List")
	ErrFeatureFlagsNoGetSupport         = errors.New("FeatureFlags client does not support Get")
	ErrFeatureFlagsNoUpdateSupport      = errors.New("FeatureFlags client does not support Update")
	ErrClientNotCAPIClient              = errors.New("client is not a capi.Client")
	ErrClientNoOrganizationsSupport     = errors.New("client does not support Organizations")
	ErrOrganizationsNoListSupport       = errors.New("organizations client does not support List")
	ErrOrganizationsNoGetSupport        = errors.New("organizations client does not support Get")
	ErrUnexpectedMockReturnType         = errors.New("unexpected return type from mock")
	ErrClientNoIsolationSegmentsSupport = errors.New("client does not support IsolationSegments")
	ErrIsolationSegmentsNoRevokeSupport = errors.New("IsolationSegments client does not support RevokeOrganization")
)

// Service credential binding errors.
var (
	ErrNotServiceCredentialBindingsClient = errors.New("client is not a *ServiceCredentialBindingsClient")
)

// Validation errors (additional).
var (
	ErrInvalidEnabledFlag         = errors.New("enabled flag must be 'true' or 'false'")
	ErrDirectoryTraversalDetected = errors.New("directory traversal detected in file path")
)

// File system errors.
var (
	ErrNotRegularFile = errors.New("path is not a regular file")
)
