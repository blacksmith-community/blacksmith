package constants

import "time"

// File and directory permissions.
const (
	// ConfigDirPerm is the permission for configuration directories.
	ConfigDirPerm = 0750

	// ConfigFilePerm is the permission for configuration files.
	ConfigFilePerm = 0600
)

// HTTP and network timeouts.
const (
	// DefaultHTTPTimeout is the default timeout for HTTP requests.
	DefaultHTTPTimeout = 30 * time.Second

	// ExtendedHTTPTimeout is used for longer operations.
	ExtendedHTTPTimeout = 45 * time.Second

	// ShortHTTPTimeout is used for quick operations.
	ShortHTTPTimeout = 10 * time.Second
)

// Retry and concurrency limits.
const (
	// DefaultRetryMax is the default maximum number of retries.
	DefaultRetryMax = 5

	// LowRetryMax is used for operations that should retry fewer times.
	LowRetryMax = 3

	// DefaultRetryWaitMax is the maximum wait time between retries.
	DefaultRetryWaitMax = 10 * time.Second

	// ExtendedRetryWaitMax is used for operations that need longer waits.
	ExtendedRetryWaitMax = 30 * time.Second
)

// Concurrency and batching limits.
const (
	// DefaultConcurrencyLimit limits concurrent operations.
	DefaultConcurrencyLimit = 3

	// BufferSize is the default buffer size for channels.
	BufferSize = 100

	// SmallBufferSize is used for smaller buffers.
	SmallBufferSize = 10
)

// HTTP status codes commonly used.
const (
	// HTTPStatusOK represents a successful HTTP response.
	HTTPStatusOK = 200

	// HTTPStatusBadRequest represents a client error.
	HTTPStatusBadRequest = 400

	// HTTPStatusInternalServerError represents server errors.
	HTTPStatusInternalServerError = 500
)

// Cloud Foundry specific error codes.
const (
	// CFErrorCodeNotFound represents CF resource not found.
	CFErrorCodeNotFound = 10010

	// CFErrorCodeServerError represents CF server errors (>= 50000).
	CFErrorCodeServerError = 50000
)

// Time intervals and delays.
const (
	// DefaultPollInterval is used for polling operations.
	DefaultPollInterval = 2 * time.Second

	// QuickPollInterval is used for fast polling.
	QuickPollInterval = 10 * time.Millisecond

	// LongPollInterval is used for slower polling.
	LongPollInterval = 5 * time.Second

	// VeryLongPollInterval is used for very slow operations.
	VeryLongPollInterval = 10 * time.Second
)

// Pagination and display limits.
const (
	// DefaultPageSize is the default number of items per page.
	DefaultPageSize = 10

	// SmallPageSize is used for demonstrations or small lists.
	SmallPageSize = 5

	// DemoDisplayLimit limits items shown in examples.
	DemoDisplayLimit = 3

	// StandardPageSize is the common page size for API responses.
	StandardPageSize = 50
)

// Memory and size constants.
const (
	// DefaultMemorySize is a default memory allocation.
	DefaultMemorySize = 512

	// SmallMemorySize is for small memory allocations.
	SmallMemorySize = 512

	// MediumMemorySize is for medium memory allocations.
	MediumMemorySize = 1024

	// LargeMemorySize is for large memory allocations.
	LargeMemorySize = 2048
)

// Mathematical and calculation constants.
const (
	// PercentageMultiplier converts decimals to percentages.
	PercentageMultiplier = 100

	// ExponentialBackoffBase is the base for exponential backoff.
	ExponentialBackoffBase = 2

	// TokenExpirationBuffer is the buffer time before token expiration.
	TokenExpirationBuffer = 30 * time.Second
)

// Validation and limits.
const (
	// MinimumArgumentCount is the minimum number of command line arguments.
	MinimumArgumentCount = 2

	// StringTruncationLimit is used when truncating strings.
	StringTruncationLimit = 4

	// MaxDemoItems limits items shown in demo scenarios.
	MaxDemoItems = 5
)

// UI and display constants.
const (
	// CheckMarkSymbol is used to indicate current/active items.
	CheckMarkSymbol = "✓"

	// NotAvailable is used when information is not available.
	NotAvailable = "N/A"

	// None is used when no value is present.
	None = "none"

	// Unlimited is used for unlimited quotas.
	Unlimited = "unlimited"

	// MaskedSecret is used to hide sensitive information.
	MaskedSecret = "***"
)

// State and status constants.
const (
	// StatusEnabled indicates an enabled state.
	StatusEnabled = "enabled"

	// StatusDisabled indicates a disabled state.
	StatusDisabled = "disabled"

	// StatusReady indicates a ready state.
	StatusReady = "ready"

	// StatusOpen indicates an open state.
	StatusOpen = "open"

	// StatusHalfOpen indicates a half-open state.
	StatusHalfOpen = "half-open"

	// JobStateFailed indicates a failed job state.
	JobStateFailed = "FAILED"
)

// Boolean string constants.
const (
	// BooleanTrue string representation.
	BooleanTrue = "true"

	// BooleanFalse string representation.
	BooleanFalse = "false"
)

// Format constants.
const (
	// FormatJSON for JSON output format.
	FormatJSON = "json"

	// FormatYAML for YAML output format.
	FormatYAML = "yaml"
)

// Compatibility constants.
const (
	// CompatibilityCompatible indicates full compatibility.
	CompatibilityCompatible = "compatible"

	// CompatibilityIncompatible indicates no compatibility.
	CompatibilityIncompatible = "incompatible"

	// CompatibilityPartial indicates partial compatibility.
	CompatibilityPartial = "partial"

	// CompatibilityUnknown indicates unknown compatibility.
	CompatibilityUnknown = "unknown"
)

// Confirmation constants.
const (
	// ConfirmationYes for positive confirmations.
	ConfirmationYes = "yes"
)

// Service constants.
const (
	// ServiceTypeUserProvided for user-provided services.
	ServiceTypeUserProvided = "user-provided"
)

// Sort order constants.
const (
	// SortOrderDescending for descending sort.
	SortOrderDescending = "descending"

	// SortOrderDesc short form for descending.
	SortOrderDesc = "desc"
)

// CRUD operation constants.
const (
	// OperationCreate for create operations.
	OperationCreate = "create"

	// OperationUpdate for update operations.
	OperationUpdate = "update"

	// OperationDelete for delete operations.
	OperationDelete = "delete"

	// OperationList for list operations.
	OperationList = "list"
)

// API path constants.
const (
	// APIPathDroplets for droplets endpoint.
	APIPathDroplets = "/v3/droplets"

	// APIPathPackages for packages endpoint.
	APIPathPackages = "/v3/packages"
)

// Filter field constants.
const (
	// FilterFieldOrgGUID for organization GUID filters.
	FilterFieldOrgGUID = "org_guid"
)

// Stack constants.
const (
	// StackCFLinuxFS4 for cflinuxfs4 stack.
	StackCFLinuxFS4 = "cflinuxfs4"
)

// File constants.
const (
	// BuildpackFilename for ruby buildpack.
	BuildpackFilename = "ruby_buildpack-v1.0.0.zip"
)

// Additional pagination and display limits.
const (
	// DefaultMaxEvents is the default maximum number of events to show.
	DefaultMaxEvents = 50

	// DefaultLogLines is the default number of log lines to show.
	DefaultLogLines = 50
)

// Additional memory and cache size constants.
const (
	// DefaultCacheSize is the default cache size limit.
	DefaultCacheSize = 1000

	// DefaultCacheTTL is the default cache time-to-live.
	DefaultCacheTTL = 5 * time.Minute

	// CacheMinTTL is the minimum cache time-to-live.
	CacheMinTTL = 30 * time.Second

	// MaxCacheValueSize is the maximum size for cached values (1MB).
	MaxCacheValueSize = 1024 * 1024

	// OrganizationsCacheTTL is the TTL for organizations cache.
	OrganizationsCacheTTL = 10 * time.Minute

	// AppsCacheTTL is the TTL for apps cache.
	AppsCacheTTL = 2 * time.Minute

	// TasksCacheTTL is the TTL for tasks cache.
	TasksCacheTTL = 30 * time.Second
)

// Additional mathematical and calculation constants.
const (
	// JSONIndentSize is the number of spaces for JSON indentation.
	JSONIndentSize = 2

	// StringTruncationLength is the default length for truncating strings.
	StringTruncationLength = 80

	// CircuitBreakerThreshold is the failure threshold for circuit breaker.
	CircuitBreakerThreshold = 5

	// CircuitBreakerSuccessThreshold is the success threshold for circuit breaker.
	CircuitBreakerSuccessThreshold = 2

	// CircuitBreakerTimeout is the timeout for circuit breaker.
	CircuitBreakerTimeout = 30 * time.Second

	// DefaultCacheSetTTL is the default TTL when setting cache values.
	DefaultCacheSetTTL = 10 * time.Minute
)

// Additional command argument counts.
const (
	// ThreeArgumentsRequired indicates commands requiring exactly 3 arguments.
	ThreeArgumentsRequired = 3

	// TwoArgumentsMax indicates commands allowing up to 2 arguments.
	TwoArgumentsMax = 2
)

// Additional time intervals.
const (
	// DefaultJobPollTimeout is the default timeout for job polling.
	DefaultJobPollTimeout = 5 * time.Minute

	// DefaultJobPollInterval is the default interval for job polling.
	DefaultJobPollInterval = 5 * time.Second

	// DefaultJobPollTimeout10 is the default timeout for job polling (10 minutes).
	DefaultJobPollTimeout10 = 10 * time.Minute
)

// Additional UI and display constants.
const (
	// PercentageMultiplierFloat converts decimals to percentages for display.
	PercentageMultiplierFloat = 100.0

	// BytesToMB converts bytes to megabytes.
	BytesToMB = 1024 * 1024

	// CommandDisplayLength is the default length for displaying commands.
	CommandDisplayLength = 50

	// ShortCommandDisplayLength is the length for displaying commands in compact mode.
	ShortCommandDisplayLength = 40

	// DescriptionDisplayLength is the default length for displaying descriptions.
	DescriptionDisplayLength = 60

	// ShortDescriptionDisplayLength is the length for displaying descriptions in compact mode.
	ShortDescriptionDisplayLength = 50

	// ActorInfoDisplayLength is the length for displaying actor info.
	ActorInfoDisplayLength = 30

	// TargetInfoDisplayLength is the length for displaying target info.
	TargetInfoDisplayLength = 30

	// GrantTypesDisplayLength is the length for displaying grant types.
	GrantTypesDisplayLength = 30

	// ProcessTypesDisplayLength is the length for displaying process types.
	ProcessTypesDisplayLength = 50

	// UUIDLength is the standard UUID length.
	UUIDLength = 36

	// Base64PaddingLength is used for base64 padding calculations.
	Base64PaddingLength = 4

	// TokenPartsCount is the expected number of parts in a JWT token.
	TokenPartsCount = 3

	// FilePathPartsCount is the expected number of parts in a file path.
	FilePathPartsCount = 4

	// FilePermissionReadWrite is the read-write file permission.
	FilePermissionReadWrite = 0600

	// CaseArgumentsCount indicates commands that switch on 2 arguments.
	CaseArgumentsCount = 2
)

// Additional timeout and performance constants.
const (
	// UAACacheTimeout is the default timeout for UAA cache.
	UAACacheTimeout = 10 * time.Minute

	// UAACacheTTL is the time-to-live for UAA cache entries.
	UAACacheTTL = 5 * time.Minute

	// UAAClientTimeout is the timeout for UAA client operations.
	UAAClientTimeout = 30 * time.Second

	// QuickOperationTimeout is the timeout for quick UAA operations.
	QuickOperationTimeout = 10 * time.Second

	// DefaultAccessTokenValidity is the default validity for access tokens (12 hours).
	DefaultAccessTokenValidity = 43200

	// DefaultRefreshTokenValidity is the default validity for refresh tokens (30 days).
	DefaultRefreshTokenValidity = 2592000

	// UIUpdateInterval is the interval for updating UI elements.
	UIUpdateInterval = 100 * time.Millisecond

	// UIMessageSpacing is the spacing for UI messages.
	UIMessageSpacing = 5

	// MaxWorkers is the default number of workers for concurrent operations.
	MaxWorkers = 10

	// LargePageSize is used for efficient bulk operations.
	LargePageSize = 100

	// MaxPages is used to prevent infinite loops in pagination.
	MaxPages = 50

	// MinimumVersionCompatibility is the minimum major version for compatibility.
	MinimumVersionCompatibility = 4

	// CompatibilityThresholdHigh indicates high compatibility (80%).
	CompatibilityThresholdHigh = 0.8

	// CompatibilityThresholdMedium indicates medium compatibility (50%).
	CompatibilityThresholdMedium = 0.5

	// MinimumVersionMatches is the minimum number of version matches required.
	MinimumVersionMatches = 3

	// KeyValueSplitParts is the number of parts when splitting key=value strings.
	KeyValueSplitParts = 2
)
