package graft

import (
	"context"
	"io"
	"time"

	"github.com/fivetwenty-io/graft/internal/cache"
	"github.com/fivetwenty-io/graft/internal/config"
	"github.com/fivetwenty-io/graft/internal/features"
	"github.com/fivetwenty-io/graft/internal/metrics"
	"github.com/fivetwenty-io/graft/internal/parallel"
	"github.com/fivetwenty-io/graft/pkg/graft/interfaces"
)

// Document represents a YAML/JSON document in a more user-friendly format
// This abstraction provides type-safe access to the internal map[string]interface{} representation.
//
// Document is not intended to be implemented outside this package. The
// two in-repo implementations (the map-backed default and the go-patch
// operation holder returned by NewGoPatchDocument) are exhaustive; new
// methods may be added to this interface in minor releases.
type Document interface {
	// Get retrieves a value at the given path (e.g., "meta.instance_groups.0.name").
	// A map or list it returns is the document's own, so a change to it
	// changes the document, and that holds for a value read from inside a
	// LiteralValue too. Get, GetMap, and GetSlice remove any wrapper from
	// the value they return, copying only the containers on the way to it.
	Get(path string) (interface{}, error)

	// GetString retrieves a string value at the given path
	GetString(path string) (string, error)

	// GetInt retrieves an integer value at the given path
	GetInt(path string) (int, error)

	// GetBool retrieves a boolean value at the given path
	GetBool(path string) (bool, error)

	// GetSlice retrieves a slice value at the given path
	GetSlice(path string) ([]interface{}, error)

	// GetMap retrieves a map value at the given path
	GetMap(path string) (map[string]interface{}, error)

	// Set sets a value at the given path
	Set(path string, value interface{}) error

	// Delete removes a value at the given path
	Delete(path string) error

	// Keys returns all top-level keys
	Keys() []string

	// ToYAML converts the document to YAML bytes
	ToYAML() ([]byte, error)

	// ToJSON converts the document to JSON bytes
	ToJSON() ([]byte, error)

	// RawData returns the underlying data structure, with every
	// LiteralValue replaced by the value it wraps. When the document
	// holds no wrapper, the result is the document's own map. When it
	// holds one, the maps and lists on the way to each wrapper are
	// copies, so a change made through the result may or may not reach
	// the document. Use Set and Delete to change a document, and Clone
	// for a copy that is safe to change. In particular, put a
	// LiteralValue into a document with Set or NewDocument. The document
	// remembers whether it holds one, so that RawData and the other
	// output calls can skip the walk when it does not, and it does not
	// notice a wrapper written straight into this map.
	RawData() interface{}

	// Clone creates a deep copy of the document
	Clone() Document

	// Prune removes one or more keys from the document, returning a new
	// document with those keys removed. A key that does not resolve is
	// silently skipped.
	Prune(keys ...string) Document

	// CherryPick creates a new document with only the specified keys
	CherryPick(keys ...string) Document

	// GetData returns the underlying data (for backward compatibility)
	GetData() interface{}

	// Additional type-safe getters
	GetInt64(path string) (int64, error)
	GetFloat64(path string) (float64, error)
	GetStringSlice(path string) ([]string, error)
	GetMapStringString(path string) (map[string]string, error)

	// Checked getters return the zero value on any failure (missing path
	// or wrong type) instead of an error. Use the Get*/GetString/GetInt/
	// etc. forms above when the distinction between "missing" and
	// "present but zero" matters.
	String(path string) string
	Int(path string) int
	Int64(path string) int64
	Float64(path string) float64
	Bool(path string) bool

	// Has reports whether path resolves to a value in the document.
	Has(path string) bool

	// Paths returns every leaf path in the document, in canonical dotted
	// form (no "$" prefix), sorted for stable output.
	Paths() []string

	// SortKeys returns a new Document with map keys sorted recursively.
	// See the *document implementation for what "sorted" guarantees.
	SortKeys() Document

	// ToJSONIndent converts the document to indented JSON bytes using
	// indent as the per-level indentation string. See also
	// Engine.ToJSONIndent, which additionally passes through evaluation.
	ToJSONIndent(indent string) ([]byte, error)

	// History returns this document's recorded change history:
	// emptyHistory{} (a valid, empty History - never a nil interface)
	// unless this exact Document value was returned by Execute() on a
	// merge chain where document-memory tracking was active (see
	// MergeBuilder.TrackHistory and WithHistoryTracking/
	// WithHistoryConfig). See the History interface's own doc comment
	// (history.go) for what is and is not recorded.
	History() History
}

// Engine is the enhanced interface for using graft as a library.
//
// Engine is not intended to be implemented outside this package. NewEngine
// only ever returns *DefaultEngine; new methods may be added to this
// interface in minor releases.
//
// One Engine may be shared by any number of goroutines that parse, merge,
// evaluate, and diff at the same time. Each Merge().Execute() and Evaluate
// call keeps its working state to itself (static IP claims, queued prunes
// and sorts, and the skip and redact settings REDACT=1 forces), so
// concurrent calls produce the same results they would on separate
// engines. Three rules go with that. First, Configure and UpdateOptions
// rewrite the engine's configuration, so they must not run while another
// call on the engine is in progress. Second, a Document belongs to one
// call at a time, because Evaluate resolves operators in the Document it
// is given. Third, the engine's own operator state holds the outputs of
// finished runs. The vault references, deferred paths, and vault
// placeholders it reports accumulate across every run that has finished,
// and GetLastEvaluatedPrunedPaths reports the run that finished last, so
// a caller that needs exactly one run's outputs uses an engine of its own.
//
// During evaluation, operators see an Engine that wraps the shared one and
// answers GetOperatorState with the run's own state. Every Engine method
// works on that wrapper, but it is not a *DefaultEngine, so an operator
// must not type-assert GetEngine(ev) to *DefaultEngine.
type Engine interface {
	// Document operations
	ParseYAML(data []byte) (Document, error)
	ParseJSON(data []byte) (Document, error)
	ParseFile(path string) (Document, error)
	ParseReader(reader io.Reader) (Document, error)

	// Context variants stop with an error that wraps ctx.Err() once ctx
	// is done, checking on entry and again after parsing. They return what
	// the plain forms return while ctx is live.
	ParseYAMLContext(ctx context.Context, data []byte) (Document, error)
	ParseJSONContext(ctx context.Context, data []byte) (Document, error)
	ParseFileContext(ctx context.Context, path string) (Document, error)
	ParseReaderContext(ctx context.Context, r io.Reader) (Document, error)

	// Merge operations with builder pattern options
	Merge(ctx context.Context, docs ...Document) MergeBuilder
	MergeFiles(ctx context.Context, paths ...string) MergeBuilder
	MergeReaders(ctx context.Context, readers ...io.Reader) MergeBuilder

	// Evaluate processes operators in a document. It first does to
	// doc's lists what a merge of doc alone does, so it resolves each
	// array marker, such as (( append )), and removes each (( prune ))
	// entry, and an index into a list counts only the entries that
	// stay. It returns the merger's error for a marker a merge refuses,
	// and the cycle error a merge gives for a document that holds
	// itself.
	// It resolves the operators in doc itself, and removes the keys
	// they prune from doc itself, so doc must not be used by another
	// call while Evaluate runs, and holds the evaluated result once it
	// returns. A caller that needs the document as it was should pass
	// doc.Clone(). Concurrent Evaluate calls on one engine are safe;
	// each keeps its own operator state (see the Engine doc comment).
	Evaluate(ctx context.Context, doc Document) (Document, error)

	// Diff computes the differences between a and b using
	// DefaultDiffOptions(). See DiffDocuments (diff_changes.go) for the
	// underlying implementation.
	Diff(a, b Document) DiffResult
	// DiffWithOptions computes the differences between a and b using
	// opts (nil selects DefaultDiffOptions()).
	DiffWithOptions(a, b Document, opts *DiffOptions) DiffResult

	// Output operations
	ToYAML(doc Document) ([]byte, error)
	ToJSON(doc Document) ([]byte, error)
	ToJSONIndent(doc Document, indent string) ([]byte, error)

	// DiffContext is DiffWithOptions under ctx. It returns a nil
	// DiffResult and an error that wraps ctx.Err() once ctx is done. It
	// also returns a nil DiffResult and an error for a nil document,
	// where DiffWithOptions gives an empty result.
	DiffContext(ctx context.Context, a, b Document, opts *DiffOptions) (DiffResult, error)
	// ToYAMLContext, ToJSONContext, and ToJSONIndentContext are the
	// output operations under ctx. ctx reaches the evaluation, so a
	// cancel stops it between operators, and the error wraps ctx.Err().
	ToYAMLContext(ctx context.Context, doc Document) ([]byte, error)
	ToJSONContext(ctx context.Context, doc Document) ([]byte, error)
	ToJSONIndentContext(ctx context.Context, doc Document, indent string) ([]byte, error)

	// Operator management
	RegisterOperator(name string, op Operator) error
	UnregisterOperator(name string) error
	ListOperators() []string
	GetOperator(name string) (Operator, bool)

	// Configuration
	WithLogger(logger Logger) Engine
	WithVaultClient(client VaultClient) Engine
	WithAWSConfig(config AWSConfig) Engine

	// State access for operators
	GetOperatorState() OperatorState

	// Memory tracking
	GetMemoryTracker() interfaces.MemoryTracker

	// IsFeatureEnabled reports whether flag (an internal/features.Feature*
	// constant) is enabled on this engine.
	IsFeatureEnabled(flag string) bool

	// Backend registry (C7): custom secret/parameter backends, consulted
	// by the vault/awsparam/awssecret/nats operators only when
	// features.FeatureBackendRegistry is enabled. See backend.go and
	// docs/developer-guide/custom-backends.md.
	RegisterBackend(b Backend) error
	GetBackend(name string) (Backend, bool)
	ListBackends() []string
	UnregisterBackend(name string) error
}

// OperatorState provides state access for operators during evaluation.
// Backend-specific clients and caches (Vault KV, AWS session, Secrets Manager,
// Parameter Store) are accessed through their respective backend packages
// (internal/backends/vault and internal/backends/aws) rather than through
// this interface.
type OperatorState interface {
	// Vault refs
	AddVaultRef(path string, keys []string)
	IsVaultSkipped() bool

	// Vault refs (for vaultinfo command)
	GetVaultRefs() map[string][]string
	ResetVaultRefs()

	// Vault skip-lookup placeholder tracking. When IsVaultSkipped() and
	// IsRedactMode() (REDACT=1, or vaultinfo's own internal skip - see
	// WithRedact), a (( vault ... ))/(( vault-try ... )) lookup still
	// returns the literal string "REDACTED" as its document value -
	// unchanged, so document output stays byte-identical - but
	// RecordVaultPlaceholder also remembers, out of band, which tree
	// path that lookup wrote to and which vault key it would have
	// looked up. A later vault-path-building argument that directly
	// references that same tree path (see op_vault.go's
	// vaultArgProcessor) consults VaultPlaceholderFor to render a
	// symbolic "<path/to/secret:key>" reference instead of
	// concatenating the literal "REDACTED" text into a new, corrupted
	// vault path. When IsVaultSkipped() but not IsRedactMode() (the
	// --skip-vault/--skip-aws/--skip-nats CLI flags), the operator
	// defers itself instead (see AddDeferredPath) and this placeholder
	// path is not used.
	RecordVaultPlaceholder(treePath, vaultKey string)
	VaultPlaceholderFor(treePath string) (vaultKey string, ok bool)
	ResetVaultPlaceholders()

	// Skip setters (for REDACT mode and the --skip-vault/--skip-aws/
	// --skip-nats flags alike; IsRedactMode below distinguishes which)
	SetSkipVault(v bool)
	SetSkipAws(v bool)
	SetSkipNats(v bool)

	// Redact mode selects which of the two behaviors a skipped backend
	// (SetSkipVault/SetSkipAws/SetSkipNats above) produces: true (set by
	// REDACT=1 - engine.go's evaluate - or explicitly via WithRedact,
	// which vaultinfo's own internal skip uses to keep its exact prior
	// behavior) means "return the literal REDACTED sentinel", matching
	// graft's behavior before the --skip-<backend> flags existed. false
	// (the default a --skip-<backend> flag alone leaves it at) means
	// "defer": each affected operator leaves its own "(( ... ))"
	// expression intact in the output instead, so a later merge with
	// the backend reachable can still evaluate it (see op_skip_defer.go
	// and AddDeferredPath). REDACT=1 always wins when both are active
	// (SetRedactMode(true) is unconditional in engine.go's evaluate),
	// which is what keeps REDACT's behavior byte-for-byte regardless of
	// any --skip-<backend> flag also given.
	SetRedactMode(v bool)
	IsRedactMode() bool

	// Deferred-path bookkeeping: every tree path that ended up deferred -
	// left with its own "(( ... ))" expression intact instead of a
	// resolved value - and why. Two sources feed this, both read back
	// together by cmd/graft's --report-deferred comment rendering
	// (graft merge --defer-on-error/--adaptive, and any of --skip-vault/
	// --skip-aws/--skip-nats): op_skip_defer.go's deferSkippedCall
	// records a skip-flag deferral the moment an operator defers itself
	// during evaluation; the adaptive-merge loop (cmd/graft) records its
	// own deferrals as it discovers them across retry rounds.
	AddDeferredPath(path, reason string)
	GetDeferredPaths() []DeferredPath
	ResetDeferredPaths()

	// AWS skip
	IsAWSSkipped() bool

	// NATS operations
	IsNATSSkipped() bool

	// Static IPs
	GetUsedIPs() map[string]string
	SetUsedIP(key, ip string)

	// Prune operations
	AddKeyToPrune(key string)
	GetKeysToPrune() []string
	ResetKeysToPrune()
	// GetLastEvaluatedPrunedPaths returns the operator-queued (( prune ))
	// paths the most recently finished Evaluate() call actually removed,
	// surviving past that call's own reset of GetKeysToPrune. On an
	// engine shared by concurrent calls, that is whichever call finished
	// last, not necessarily the caller's own.
	GetLastEvaluatedPrunedPaths() []string

	// Sort operations
	AddPathToSort(path, order string)
	GetPathsToSort() map[string]string
	ResetPathsToSort()

	// Reset operations (for cleanup between evaluations)
	ResetUsedIPs()

	// Warning suppression
	SuppressWarnings() bool
	SetSuppressWarnings(v bool)
}

// DeferredPath is one deferred tree path recorded via OperatorState.
// AddDeferredPath: Path is the dotted document path (Cursor.String()
// form, no leading "$."), and Reason is a short, human-readable
// explanation - either the original operator error (graft merge
// --defer-on-error/--adaptive) or a fixed "skipped (--skip-<backend>)"
// string (the --skip-vault/--skip-aws/--skip-nats flags) - suitable for
// direct display, e.g. in cmd/graft's --report-deferred comments.
type DeferredPath struct {
	Path   string
	Reason string
}

// ArrayMergeStrategy defines how arrays are merged.
type ArrayMergeStrategy int

const (
	// InlineArrays is the default - arrays are merged inline by index.
	InlineArrays ArrayMergeStrategy = iota
	// AppendArrays appends arrays instead of merging inline.
	AppendArrays
	// ReplaceArrays replaces the entire array.
	ReplaceArrays
	// PrependArrays prepends new array elements.
	PrependArrays
)

// MergeBuilder provides a fluent interface for merge operations.
//
// MergeBuilder is not intended to be implemented outside this package.
// Engine.Merge/MergeFiles/MergeReaders only ever return the package's own
// implementation; new methods may be added to this interface in minor
// releases.
type MergeBuilder interface {
	// Base sets the base document for the merge, replacing position 0 in
	// the builder's document list - including a document supplied via
	// engine.Merge(ctx, docs...)'s first argument. Calling Base more than
	// once on the same chain replaces the previous base rather than
	// accumulating; use Overlay/OverlayFile to add further documents. A
	// nil doc is not validated here: Execute() panics on it later, the
	// same pre-existing hazard as passing a nil Document to Engine.Merge
	// directly.
	Base(doc Document) MergeBuilder

	// Overlay appends one or more documents to be merged, in call order,
	// on top of the base and any earlier overlays (from Merge's own
	// arguments, prior Overlay calls, or prior OverlayFile calls). A nil
	// entry is not validated here; see Base's doc comment for the same
	// pre-existing nil hazard.
	Overlay(docs ...Document) MergeBuilder

	// OverlayFile loads each path via the engine's ParseFile (the same
	// extension-based YAML/JSON/go-patch auto-detection ParseFile
	// documents, including the "-" == STDIN convention) and appends the
	// resulting documents as overlays, in path order. A load failure does
	// not panic and is not returned directly: it is captured on the
	// returned builder and reported by Execute(), matching
	// Engine.MergeFiles/MergeReaders' error-carrying-builder convention.
	OverlayFile(paths ...string) MergeBuilder

	// WithPrune specifies keys to remove from the final output
	WithPrune(keys ...string) MergeBuilder

	// WithCherryPick specifies keys to keep in the final output (all others removed)
	WithCherryPick(keys ...string) MergeBuilder

	// WithArrayMergeStrategy sets how arrays are merged
	WithArrayMergeStrategy(strategy ArrayMergeStrategy) MergeBuilder

	// WithPostProcessors appends procs to the processors that run after
	// evaluation, pruning, and cherry-picking - see applyPostProcessing's
	// ordering, documented on the package-level graft.WithPostProcessors
	// EngineOption. Processors supplied here run alongside, not instead
	// of, any registered on the engine via that EngineOption: both sets
	// are combined and ordered together by Phase-then-Priority (see
	// PostProcessPhase, PriorityPostProcessor), not by which of the two
	// registered them.
	WithPostProcessors(procs ...PostProcessor) MergeBuilder

	// SkipEvaluation skips operator evaluation after merging
	SkipEvaluation() MergeBuilder

	// EnableGoPatch enables go-patch format parsing
	EnableGoPatch() MergeBuilder

	// FallbackAppend uses append instead of inline for arrays by default
	FallbackAppend() MergeBuilder

	// TrackHistory activates document-memory tracking for this merge
	// chain, lazily calling the engine's EnableMemoryTracking if it is
	// not already active (only possible when the underlying Engine is
	// *DefaultEngine - a no-op otherwise). The resulting Document's
	// History() reflects every change DocumentMemory recorded on that
	// engine, which is scoped to the whole engine rather than to this
	// one merge if history tracking is also active for other merges on
	// it - see the History interface's doc comment (history.go). On an
	// engine shared by concurrent merges, the recorded history therefore
	// mixes the changes of every merge running at the same time, so a
	// caller that needs one merge's history uses an engine of its own.
	TrackHistory() MergeBuilder

	// Execute performs the merge operation
	Execute() (Document, error)
}

// EngineOptions configures a new engine instance using functional options.
type EngineOptions struct {
	Logger               Logger
	VaultClient          VaultClient
	AWSConfig            *AWSConfig
	EnableCache          bool
	CacheSize            int
	MaxConcurrency       int
	EnableMetrics        bool
	CustomOperators      map[string]Operator
	VaultAddress         string
	VaultToken           string
	DebugLogging         bool
	AWSRegion            string
	DataflowOrder        string // "alphabetical" (default) or "insertion"
	EnableMemoryTracking bool   // Enable document memory tracking

	// New infrastructure system options

	// FeatureFlags provides feature flag configuration (nil uses defaults)
	FeatureFlags *features.FeatureFlags

	// CacheInstance provides a custom cache implementation (nil uses default if caching enabled)
	CacheInstance cache.Cache

	// MetricsRegistry provides a custom metrics registry (nil uses default if metrics enabled)
	MetricsRegistry *metrics.Registry

	// ConfigInstance provides unified configuration (nil uses defaults)
	ConfigInstance *config.Config

	// WorkerPool provides a custom worker pool for parallel evaluation (nil uses default if parallel enabled)
	WorkerPool *parallel.WorkerPool

	// EnableParallel enables parallel evaluation (shorthand for feature flag)
	EnableParallel bool

	// YAMLCompat controls YAML 1.1 backward compatibility behavior (nil uses defaults)
	YAMLCompat *YAMLCompat

	// VaultSkipTLS disables TLS verification for Vault connections.
	VaultSkipTLS bool

	// AWSProfile sets the AWS credentials profile to use.
	AWSProfile string

	// MemoryConfig configures document memory tracking behavior.
	MemoryConfig *MemoryConfig

	// Skip flags for external service operators
	SkipVault bool
	SkipAws   bool
	SkipNats  bool

	// Redact selects the skipped-backend behavior for the SkipVault/
	// SkipAws/SkipNats flags above: true reproduces graft's original
	// "return the literal REDACTED sentinel" behavior (see WithRedact);
	// false (the default) makes a skipped operator defer itself instead,
	// leaving its own "(( ... ))" expression intact in the output. REDACT=1
	// forces this to true at evaluation time regardless of what Redact
	// was constructed with (see engine.go's evaluate).
	Redact bool

	// CacheTTL sets a default time-to-live for entries in the engine's
	// operator result cache (see WithCacheTTL). Zero means no expiration.
	CacheTTL time.Duration

	// PostProcessors run after evaluation, pruning, and cherry-picking
	// on every merge executed by this engine (see WithPostProcessors).
	// A MergeBuilder's own WithPostProcessors call adds to this set for
	// that one merge chain rather than replacing it.
	PostProcessors []PostProcessor

	// TraceOutput, when set (via WithTraceOutput), redirects graft's
	// DEBUG/TRACE output. See WithTraceOutput for the process-wide-sink
	// caveat.
	TraceOutput io.Writer

	// TraceLevel selects which of graft's DEBUG/TRACE output is produced
	// when set via WithTraceLevel. Read traceLevelSet, not the zero value
	// of TraceLevel, to tell "unset" apart from "explicitly set to
	// TraceLevelNone".
	TraceLevel TraceLevel

	// traceLevelSet is true once WithTraceLevel has been applied,
	// distinguishing "never configured" (leave process logging state
	// untouched) from "explicitly set to TraceLevelNone" (turn output
	// off). Unexported: only WithTraceLevel sets it.
	traceLevelSet bool

	// debugLoggingSet is true once WithDebugLogging has been applied,
	// distinguishing "never configured" from "explicitly set", the same
	// way traceLevelSet does for TraceLevel. Unexported: only
	// WithDebugLogging sets it.
	debugLoggingSet bool

	// Backends registers custom secret/parameter backends at construction
	// time (see WithBackend). Consulted by the vault/awsparam/awssecret/
	// nats operators only when features.FeatureBackendRegistry is
	// enabled.
	Backends map[string]Backend

	// BackendRetryConfigs configures the registry's generic retry
	// wrapper per backend name (see WithBackendRetry).
	BackendRetryConfigs map[string]RetryConfig

	// BackendCaches configures the registry's generic caching wrapper
	// per backend name (see WithBackendCache).
	BackendCaches map[string]BackendCache

	// AuditLoggerInstance receives a LogAccess call for every registry-
	// mediated backend Get/GetWithTarget call (see WithAuditLogger).
	AuditLoggerInstance AuditLogger

	// backendRegistryEnabled overrides features.FeatureBackendRegistry
	// when non-nil (see WithBackendRegistry). nil means "leave whatever
	// WithFeatureFlags/the default computed alone." Unexported: only
	// WithBackendRegistry sets it.
	backendRegistryEnabled *bool
}

// EngineOption is a functional option for configuring an engine.
type EngineOption func(*EngineOptions)

// WithLogger sets the logger the engine reports evaluation activity to.
// A nil logger disables reporting (the default: an engine constructed
// without WithLogger reports nothing).
func WithLogger(logger Logger) EngineOption {
	return func(opts *EngineOptions) {
		opts.Logger = logger
	}
}

// WithVaultClient sets the vault client for the engine.
//
// Deprecated: has no effect and never will - the VaultClient interface
// (Get/List/Put) has no implementation anywhere in this module to satisfy
// it, and there is no code path that would call it if one existed. Use
// WithVault(VaultConfig{...}) instead: it registers a real, working Vault
// backend (github.com/hashicorp/vault/api underneath) consulted when
// WithBackendRegistry(true) is set.
func WithVaultClient(client VaultClient) EngineOption {
	return func(opts *EngineOptions) {
		opts.VaultClient = client
	}
}

// WithAWSConfig sets the AWS configuration.
//
// Deprecated: has no effect - the stored EngineOptions.AWSConfig is never
// read by engine construction or by any AWS operator. Use
// WithAWS(AWSConfig{...}) instead: despite the identical struct type
// (AWSConfig is shared between both options), WithAWS actually threads its
// argument into a real AWS config (github.com/aws/aws-sdk-go-v2 underneath)
// registered as the "awsparam"/"awssecret" backends, consulted when
// WithBackendRegistry(true) is set.
func WithAWSConfig(cfg *AWSConfig) EngineOption {
	return func(opts *EngineOptions) {
		opts.AWSConfig = cfg
	}
}

// WithCache enables caching with the specified size.
func WithCache(enabled bool, size int) EngineOption {
	return func(opts *EngineOptions) {
		opts.EnableCache = enabled
		opts.CacheSize = size
	}
}

// WithConcurrency sets the maximum number of concurrent operations.
func WithConcurrency(maxConcur int) EngineOption {
	return func(opts *EngineOptions) {
		opts.MaxConcurrency = maxConcur
	}
}

// WithMetrics enables metrics collection.
func WithMetrics(enabled bool) EngineOption {
	return func(opts *EngineOptions) {
		opts.EnableMetrics = enabled
	}
}

// WithCustomOperator registers a custom operator.
func WithCustomOperator(name string, op Operator) EngineOption {
	return func(opts *EngineOptions) {
		if opts.CustomOperators == nil {
			opts.CustomOperators = make(map[string]Operator)
		}
		opts.CustomOperators[name] = op
	}
}

// WithVaultConfig configures vault settings.
//
// Deprecated: has no effect - EngineOptions.VaultAddress/VaultToken are
// never read. Use WithVault(VaultConfig{Address: address, Token: token})
// instead - same two values, but it actually reaches a Vault server. See
// WithVaultClient's doc comment for what WithVault provides.
func WithVaultConfig(address, token string) EngineOption {
	return func(opts *EngineOptions) {
		opts.VaultAddress = address
		opts.VaultToken = token
	}
}

// WithDebugLogging enables or disables graft's DEBUG output (log.DebugOn),
// the same knob the CLI's -d/--debug flag sets. See also WithTraceLevel,
// which offers the same control plus TRACE output; if both are applied to
// the same engine, WithTraceLevel wins. Like WithTraceOutput/
// WithTraceLevel, this reaches into process-global logging state (DEBUG is
// a package-level function, not per-engine) - see WithTraceOutput's doc
// comment for the full caveat.
func WithDebugLogging(enabled bool) EngineOption {
	return func(opts *EngineOptions) {
		opts.DebugLogging = enabled
		opts.debugLoggingSet = true
	}
}

// WithAWSRegion sets the AWS region.
//
// Deprecated: has no effect - EngineOptions.AWSRegion is never read. Use
// WithAWS(AWSConfig{Region: region}) instead. See WithAWSConfig's doc
// comment for what WithAWS provides.
func WithAWSRegion(region string) EngineOption {
	return func(opts *EngineOptions) {
		opts.AWSRegion = region
	}
}

// WithDataflowOrder sets the order of operations in dataflow output
// Valid values are "alphabetical" (default) or "insertion".
func WithDataflowOrder(order string) EngineOption {
	return func(opts *EngineOptions) {
		opts.DataflowOrder = order
	}
}

// New infrastructure system options

// WithFeatureFlags sets the feature flags for the engine.
func WithFeatureFlags(ff *features.FeatureFlags) EngineOption {
	return func(opts *EngineOptions) {
		opts.FeatureFlags = ff
	}
}

// WithCacheInstance sets a custom cache implementation for the engine.
func WithCacheInstance(c cache.Cache) EngineOption {
	return func(opts *EngineOptions) {
		opts.CacheInstance = c
		opts.EnableCache = c != nil
	}
}

// WithMetricsRegistry sets a custom metrics registry for the engine.
func WithMetricsRegistry(r *metrics.Registry) EngineOption {
	return func(opts *EngineOptions) {
		opts.MetricsRegistry = r
		opts.EnableMetrics = r != nil
	}
}

// WithConfigInstance sets the unified configuration for the engine.
func WithConfigInstance(cfg *config.Config) EngineOption {
	return func(opts *EngineOptions) {
		opts.ConfigInstance = cfg
	}
}

// WithWorkerPool sets a custom worker pool for parallel evaluation.
func WithWorkerPool(pool *parallel.WorkerPool) EngineOption {
	return func(opts *EngineOptions) {
		opts.WorkerPool = pool
		opts.EnableParallel = pool != nil
	}
}

// WithCaching is a shorthand for enabling/disabling caching via feature flags.
func WithCaching(enabled bool) EngineOption {
	return func(opts *EngineOptions) {
		opts.EnableCache = enabled
		if opts.FeatureFlags == nil {
			opts.FeatureFlags = features.DefaultFlags()
		}
		opts.FeatureFlags.Set(features.FeatureCaching, enabled)
	}
}

// WithParallel is a shorthand for enabling/disabling parallel evaluation via feature flags.
func WithParallel(enabled bool) EngineOption {
	return func(opts *EngineOptions) {
		opts.EnableParallel = enabled
		if opts.FeatureFlags == nil {
			opts.FeatureFlags = features.DefaultFlags()
		}
		opts.FeatureFlags.Set(features.FeatureParallelEvaluation, enabled)
	}
}

// WithMemoryPools is a shorthand for enabling/disabling memory pools via
// feature flags.
//
// Deprecated: sets features.FeatureMemoryPools, but nothing in graft reads
// that flag today - there is no pooling implementation to gate. The flag
// is still set (and observable via Engine's feature-flag accessors) so
// this option keeps compiling and remains forward-compatible with a future
// pooling implementation.
func WithMemoryPools(enabled bool) EngineOption {
	return func(opts *EngineOptions) {
		if opts.FeatureFlags == nil {
			opts.FeatureFlags = features.DefaultFlags()
		}
		opts.FeatureFlags.Set(features.FeatureMemoryPools, enabled)
	}
}

// WithYAMLCompat sets YAML 1.1 backward-compatibility behavior (see
// YAMLCompat) used when parsing YAML with ParseYAML. A nil compat is
// ignored, leaving the engine's default (DefaultYAMLCompat(), which
// converts yes/no/on/off-style scalars to booleans) in effect. Files
// read by (( load )) and --go-patch ops files ignore it and always read
// YAML with the default compat.
func WithYAMLCompat(compat *YAMLCompat) EngineOption {
	return func(opts *EngineOptions) {
		if compat != nil {
			opts.YAMLCompat = compat
		}
	}
}

// WithVaultSkipTLS disables TLS verification for Vault connections.
//
// Deprecated: has no effect - EngineOptions.VaultSkipTLS is never read.
// Use WithVault(VaultConfig{SkipVerify: skip, ...}) instead. See
// WithVaultClient's doc comment for what WithVault provides.
func WithVaultSkipTLS(skip bool) EngineOption {
	return func(opts *EngineOptions) {
		opts.VaultSkipTLS = skip
	}
}

// WithAWSProfile sets the AWS credentials profile.
//
// Deprecated: has no effect - EngineOptions.AWSProfile is never read. Use
// WithAWS(AWSConfig{Profile: profile}) instead. See WithAWSConfig's doc
// comment for what WithAWS provides.
func WithAWSProfile(profile string) EngineOption {
	return func(opts *EngineOptions) {
		opts.AWSProfile = profile
	}
}

// WithMemoryConfig sets the document memory tracking configuration.
func WithMemoryConfig(cfg MemoryConfig) EngineOption {
	return func(opts *EngineOptions) {
		opts.MemoryConfig = &cfg
	}
}

// WithMaxWorkers sets the maximum number of worker goroutines.
//
// Deprecated: use WithConcurrency instead; the two are functionally
// identical (both set only EngineOptions.MaxConcurrency).
func WithMaxWorkers(n int) EngineOption {
	return func(opts *EngineOptions) {
		opts.MaxConcurrency = n
	}
}

// WithSkipVault configures the engine to skip vault operations.
func WithSkipVault(skip bool) EngineOption {
	return func(opts *EngineOptions) {
		opts.SkipVault = skip
	}
}

// WithSkipAws configures the engine to skip AWS operations.
func WithSkipAws(skip bool) EngineOption {
	return func(opts *EngineOptions) {
		opts.SkipAws = skip
	}
}

// WithSkipNats configures the engine to skip NATS operations.
func WithSkipNats(skip bool) EngineOption {
	return func(opts *EngineOptions) {
		opts.SkipNats = skip
	}
}

// WithRedact selects the "return the literal REDACTED sentinel" behavior
// for any of WithSkipVault/WithSkipAws/WithSkipNats also given, instead
// of the default "defer" behavior (leave the operator's own
// "(( ... ))" expression intact) a --skip-<backend> flag alone produces.
// vaultinfo's own internal use of WithSkipVault(true)
// (cmd/graft/main.go's handleVaultInfo) pairs it with WithRedact(true)
// to keep its pre-existing behavior unchanged; REDACT=1 achieves the
// same effect at evaluation time regardless of this option (see
// engine.go's evaluate), so redact=false here does not defeat REDACT=1.
func WithRedact(redact bool) EngineOption {
	return func(opts *EngineOptions) {
		opts.Redact = redact
	}
}

// WithPostProcessors registers procs to run, in Phase-then-Priority
// order (see PostProcessPhase and PriorityPostProcessor), after
// evaluation, pruning, and cherry-picking on every merge this engine
// executes. Calling WithPostProcessors more than once, or combining it
// with MergeBuilder.WithPostProcessors on individual merges, appends
// rather than replaces - every processor from every call runs, ordered
// by Phase-then-Priority rather than by which call registered it. A nil
// entry in procs is ignored rather than causing a panic or an error.
func WithPostProcessors(procs ...PostProcessor) EngineOption {
	return func(opts *EngineOptions) {
		opts.PostProcessors = append(opts.PostProcessors, procs...)
	}
}

// Logger interface for structured logging.
type Logger interface {
	Debug(msg string, fields ...interface{})
	Info(msg string, fields ...interface{})
	Warn(msg string, fields ...interface{})
	Error(msg string, fields ...interface{})
}

// VaultClient interface for vault operations.
//
// Deprecated: nothing in graft consumes a VaultClient - WithVaultClient
// stores it without effect. Use WithVault (backend_vault.go), which
// registers a real Vault-backed Backend from a VaultConfig, or register
// your own Backend implementation via WithBackend.
type VaultClient interface {
	Get(path string) (map[string]interface{}, error)
	List(path string) ([]string, error)
	Put(path string, data map[string]interface{}) error
}

// AWSConfig holds AWS-specific configuration, consumed both by the
// existing (deprecated, no-op) WithAWSConfig engine option and by
// WithAWS/WithAWSTarget (backend_aws.go), which do give it an observable
// effect - see WithAWS's doc comment.
type AWSConfig struct {
	Region   string
	Profile  string
	Role     string
	SkipAuth bool
	Endpoint string // For testing with localstack

	// AccessKeyID, SecretAccessKey, and SessionToken set static AWS
	// credentials directly, bypassing the SDK's default provider chain
	// (environment, shared config, EC2/ECS role). Only consumed by
	// WithAWS/WithAWSTarget; the deprecated WithAWSConfig option still
	// ignores every field, including these. Ignored entirely when SkipAuth
	// is true.
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string

	// PoolSize sets the underlying HTTP transport's
	// MaxIdleConnsPerHost/MaxIdleConns. Non-positive leaves Go's
	// http.Transport zero-value default (2 idle connections per host) in
	// effect. Only consumed by WithAWS/WithAWSTarget.
	PoolSize int

	// MFASerial is the ARN or serial number of an MFA device protecting
	// the assumed Role above. Ignored when Role is "". Only consumed by
	// WithAWS/WithAWSTarget.
	MFASerial string

	// MFATokenProvider supplies the current MFA code for MFASerial on
	// every STS AssumeRole call: it must return a fresh code each time it
	// is invoked (aws.NewCredentialsCache wraps the resulting credentials
	// provider, so this is called only when the cache needs to refresh,
	// not on every SSM/Secrets Manager request). If nil while MFASerial is
	// set, buildAWSConfig falls back to a one-shot code read from the
	// AWS_MFA_TOKEN environment variable, or - when stdin is a terminal -
	// an interactive prompt on stderr; with neither available, credential
	// resolution fails with a clear error naming AWS_MFA_TOKEN. Ignored
	// when MFASerial is "".
	MFATokenProvider func() (string, error)
}

// NewEngine creates a new engine instance with the given options, applied
// over the library's one documented default configuration
// (defaultEngineOpts: caching enabled with a 10000-entry cache, parallel
// evaluation disabled, 4 max concurrent workers, alphabetical dataflow
// order). Earlier versions of NewEngine used a second, different default
// set (1000-entry cache, 10 max workers); that drift is gone as of this
// version - NewEngine and NewDefaultEngine now start from the same
// defaults.
//
// The returned engine may be built once and shared by concurrent
// goroutines; see the Engine doc comment for the rules that go with
// sharing it.
func NewEngine(options ...EngineOption) (Engine, error) {
	opts := defaultEngineOpts()

	for _, option := range options {
		option(&opts)
	}

	return createEngineFromOptions(&opts)
}

// CreateDefaultEngine creates an engine with the library's default
// configuration (see NewEngine). It is equivalent to NewEngine() with no
// options; it exists as a discoverable, explicitly-named entry point for
// callers who want "just give me a working engine" without needing to know
// that an empty options list already does that.
func CreateDefaultEngine() (Engine, error) {
	return NewEngine()
}

// QuickMerge parses each YAML source, merges them in order (later sources
// override earlier ones), evaluates operators, and returns the result as
// YAML bytes. It is the one-call form of
// CreateDefaultEngine + ParseYAML + Merge().Execute() + ToYAML for callers
// who need nothing but a merged document.
//
// Each call constructs a fresh default engine, so no engine-local state
// carries over between calls and no custom operators, backends, or
// options apply. The built-in vault, AWS, and NATS secret caches are
// package-global, not engine state: they persist across calls (as they do
// across any two engines in one process), so a secret fetched by one call
// can be served from cache to the next. Callers who want custom
// configuration should build an engine with NewEngine and use its Merge
// builder directly. With no sources it returns an empty document
// ("{}\n").
//
// Like every use of this package that evaluates operators, the consuming
// program must blank-import the operators package once, or every source
// containing an (( ... )) expression fails with "parser not initialized -
// operators package must be imported":
//
//	import _ "github.com/fivetwenty-io/graft/pkg/graft/operators"
func QuickMerge(yamlSources ...string) ([]byte, error) {
	engine, err := CreateDefaultEngine()
	if err != nil {
		return nil, err
	}
	defer releaseThrowawayEngine(engine)

	ctx := context.Background()
	docs := make([]Document, 0, len(yamlSources))
	for _, source := range yamlSources {
		doc, err := engine.ParseYAML([]byte(source))
		if err != nil {
			return nil, err
		}
		docs = append(docs, doc)
	}

	result, err := engine.Merge(ctx, docs...).Execute()
	if err != nil {
		return nil, err
	}

	return engine.ToYAML(result)
}

// releaseThrowawayEngine stops the background resources held by an engine
// that QuickMerge/QuickMergeFiles built for a single call and is about to
// drop. Today that is the engine cache: cache.NewCache (ShardedCache)
// starts a cleanupLoop goroutine that only Close() stops, so an unclosed
// throwaway engine leaks one goroutine per call for the process lifetime.
// closeOutgoingCache's nil-replacement form performs the same optional
// Close() type assertion Configure uses when it swaps a cache out.
func releaseThrowawayEngine(engine Engine) {
	if de, ok := engine.(*DefaultEngine); ok {
		closeOutgoingCache(de.Cache, nil)
	}
}

// QuickMergeFiles is QuickMerge for files on disk: it loads each path,
// merges them in order, evaluates operators, and returns the result as
// YAML bytes. Load errors (missing or unparseable files) are reported by
// Execute. Like QuickMerge, each call constructs a fresh default engine,
// and the consuming program must blank-import the operators package (see
// QuickMerge); with no paths it returns an empty document ("{}\n").
func QuickMergeFiles(paths ...string) ([]byte, error) {
	engine, err := CreateDefaultEngine()
	if err != nil {
		return nil, err
	}
	defer releaseThrowawayEngine(engine)

	ctx := context.Background()
	result, err := engine.MergeFiles(ctx, paths...).Execute()
	if err != nil {
		return nil, err
	}

	return engine.ToYAML(result)
}
