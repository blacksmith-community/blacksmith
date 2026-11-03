package operators

import (
	"errors"
	"fmt"
	"strings"

	"github.com/fivetwenty-io/graft/internal/backends/vault"
	"github.com/fivetwenty-io/graft/internal/errtext"
	"github.com/fivetwenty-io/graft/pkg/graft"
	"github.com/fivetwenty-io/graft/pkg/graft/tree"
)

// vaultArgProcessor handles argument processing for vault operator with LogicalOr support.
type vaultArgProcessor struct {
	args         []*Expr
	hasDefault   bool
	defaultExpr  *Expr
	defaultIndex int
	hasSubOps    bool // Track if sub-operators are used
}

// newVaultArgProcessor creates a processor that extracts defaults from any position.
func newVaultArgProcessor(args []*Expr) *vaultArgProcessor {
	processor := &vaultArgProcessor{
		args:         make([]*Expr, len(args)),
		hasDefault:   false,
		defaultIndex: -1,
		hasSubOps:    false,
	}

	// Check for sub-operators and parse if needed
	parsedArgs, hasSubOps, err := ParseVaultArgs(args)
	if err != nil {
		// If parsing fails, fall back to original args
		parsedArgs = args
		hasSubOps = false
	}
	processor.hasSubOps = hasSubOps

	// Copy args and extract any LogicalOr
	for i, arg := range parsedArgs {
		if arg.Type == LogicalOr {
			processor.hasDefault = true
			processor.defaultExpr = arg.Right
			processor.defaultIndex = i
			// Use the left side of LogicalOr for vault path
			processor.args[i] = arg.Left
		} else {
			processor.args[i] = arg
		}
	}

	return processor
}

// isVaultPathString checks if an expression looks like a vault path (contains colon).
//
//nolint:unparam // ev reserved for future expression resolution
func isVaultPathString(ev *Evaluator, expr *Expr) bool {
	// Try to resolve to string without error propagation
	if expr.Type == Literal {
		if str, ok := expr.Literal.(string); ok {
			return strings.Contains(str, ":")
		}
	}
	return false
}

// detectMultiplePathArgs checks if we have multiple vault path arguments.
func (p *vaultArgProcessor) detectMultiplePathArgs(ev *Evaluator) bool {
	// If we have LogicalOr, we're in classic mode
	if p.hasDefault {
		return false
	}

	// Check if we have multiple arguments that look like vault paths
	pathCount := 0
	for _, arg := range p.args {
		if isVaultPathString(ev, arg) {
			pathCount++
		}
	}

	return pathCount > 1
}

// symbolicPlaceholder reports the symbolic "<path/to/secret:key>" form
// for an argument that leads to a tree path a skip-vault sentinel was
// written to (RecordVaultPlaceholder, performVaultLookup): a direct
// reference to such a path, or a single-argument nested (( grab ... ))
// call routed to one - grab's Run forwards the source's placeholder to
// its own tree path (op_grab.go), so tree-level aliases resolve through
// the reference case, while this recursion covers a grab nested inline
// in the vault arguments without evaluating it. Anything else (notably a
// concat-built segment) is not covered and resolves normally.
func (p *vaultArgProcessor) symbolicPlaceholder(ev *Evaluator, expr *Expr) (string, bool) {
	if expr == nil {
		return "", false
	}
	if expr.Type == Reference && expr.Reference != nil {
		if key, ok := graft.GetEngine(ev).GetOperatorState().VaultPlaceholderFor(expr.Reference.String()); ok {
			return "<" + key + ">", true
		}
		return "", false
	}
	if expr.Type == OperatorCall && expr.Op() == "grab" {
		if args := expr.Args(); len(args) == 1 {
			return p.symbolicPlaceholder(ev, args[0])
		}
	}
	return "", false
}

// resolveToString resolves an expression and converts it to a string.
func (p *vaultArgProcessor) resolveToString(ev *Evaluator, expr *Expr) (string, error) {
	// A path segment carrying a skip-vault sentinel renders symbolically
	// instead of resolving normally - resolving it normally would return
	// the literal "REDACTED" text, silently concatenating that word into
	// a new, corrupted vault path (docs/user-guide/secrets/vault.md's
	// former "known limitation").
	if key, ok := p.symbolicPlaceholder(ev, expr); ok {
		return key, nil
	}

	// Check if we need to handle sub-operators
	if p.hasSubOps {
		result, err := p.resolveWithSubOperators(ev, expr)
		if err != nil {
			return "", err
		}
		// Convert result to string
		return p.convertToString(result, expr)
	}

	// Use ResolveOperatorArgument to support nested expressions
	value, err := ResolveOperatorArgument(ev, expr)
	if err != nil {
		// Maintain backward compatibility with error messages
		if expr.Type == Reference {
			return "", fmt.Errorf("unable to resolve `%s`: %w", expr.Reference, err)
		}
		return "", err
	}

	if value == nil {
		return "", fmt.Errorf("cannot use nil as vault path component")
	}

	// Convert resolved value to string with vault-specific error messages
	return p.convertToString(value, expr)
}

// convertToString converts a value to string with vault-specific error messages.
func (p *vaultArgProcessor) convertToString(value interface{}, expr *Expr) (string, error) {
	if value == nil {
		return "", fmt.Errorf("cannot use nil as vault path component")
	}

	switch v := value.(type) {
	case string:
		return v, nil
	case int, int64, float32, float64, bool:
		return fmt.Sprintf("%v", v), nil
	case map[string]interface{}:
		if expr != nil && expr.Type == Reference {
			return "", fmt.Errorf("$.%s is a map; only scalars are supported for vault paths", expr.Reference)
		}
		return "", fmt.Errorf("value is a map; only scalars are supported for vault paths")
	case []interface{}:
		if expr != nil && expr.Type == Reference {
			return "", fmt.Errorf("$.%s is a list; only scalars are supported for vault paths", expr.Reference)
		}
		return "", fmt.Errorf("value is a list; only scalars are supported for vault paths")
	default:
		return fmt.Sprintf("%v", v), nil
	}
}

// resolveWithSubOperators resolves expressions with sub-operator support.
func (p *vaultArgProcessor) resolveWithSubOperators(ev *Evaluator, expr *Expr) (interface{}, error) {
	if expr == nil {
		return nil, fmt.Errorf("cannot resolve nil expression")
	}

	switch expr.Type {
	case graft.VaultGroup:
		// Resolve grouped expression
		return p.resolveGroup(ev, expr)
	case graft.VaultChoice:
		// Resolve choice expression (try alternatives)
		return p.resolveChoice(ev, expr)
	case graft.Literal, graft.Reference, graft.List, graft.Or, graft.Negate,
		graft.Addition, graft.Subtraction, graft.Multiplication, graft.Division, graft.Modulo,
		graft.Equal, graft.NotEqual, graft.LessThan, graft.LessThanOrEqual, graft.GreaterThan, graft.GreaterThanOrEqual,
		graft.LogicalAnd, graft.LogicalOr, graft.RegexpMatch, graft.EnvVar, graft.BoshVar, graft.OperatorCall:
		// Fall back to standard resolution
		return ResolveOperatorArgument(ev, expr)
	}
	return ResolveOperatorArgument(ev, expr)
}

// resolveGroup resolves a grouped expression.
func (p *vaultArgProcessor) resolveGroup(ev *Evaluator, expr *Expr) (interface{}, error) {
	if expr.Left == nil {
		return nil, fmt.Errorf("empty group expression")
	}

	// Recursively resolve the inner expression
	return p.resolveWithSubOperators(ev, expr.Left)
}

// resolveChoice resolves a choice expression (try alternatives).
func (p *vaultArgProcessor) resolveChoice(ev *Evaluator, expr *Expr) (interface{}, error) {
	if expr.Left == nil && expr.Right == nil {
		return nil, fmt.Errorf("empty choice expression")
	}

	// Try left side first
	if expr.Left != nil {
		result, err := p.resolveWithSubOperators(ev, expr.Left)
		if err == nil && result != nil {
			// Left side succeeded
			return result, nil
		}
		// Left side failed or returned nil, try right side
		DEBUG("vault choice: left alternative failed (%v), trying right", err)
	}

	// Try right side
	if expr.Right != nil {
		result, err := p.resolveWithSubOperators(ev, expr.Right)
		if err == nil && result != nil {
			// Right side succeeded
			return result, nil
		}
		DEBUG("vault choice: right alternative failed (%v)", err)
	}

	// Both sides failed
	return nil, fmt.Errorf("all choice alternatives failed")
}

// buildVaultPath resolves all arguments and concatenates them into a vault path.
func (p *vaultArgProcessor) buildVaultPath(ev *Evaluator) (string, error) {
	parts := make([]string, 0, len(p.args))

	for i, arg := range p.args {
		DEBUG("  processing arg[%d] for concatenation", i)

		part, err := p.resolveToString(ev, arg)
		if err != nil {
			DEBUG("    failed to resolve arg[%d]: %s", i, err)
			return "", err
		}

		DEBUG("    resolved to: '%s'", part)
		parts = append(parts, part)
	}

	path := strings.Join(parts, "")
	DEBUG("  final concatenated path: '%s'", path)

	return path, nil
}

// splitVaultPaths splits a path string by semicolons to support multiple vault paths.
func (p *vaultArgProcessor) splitVaultPaths(path string) []string {
	// Check if the path contains semicolons for multiple paths
	if !strings.Contains(path, ";") {
		return []string{path}
	}

	// Split by semicolon and trim whitespace
	rawPaths := strings.Split(path, ";")
	paths := make([]string, 0, len(rawPaths))

	for _, p := range rawPaths {
		trimmed := strings.TrimSpace(p)
		if trimmed != "" {
			paths = append(paths, trimmed)
		}
	}

	return paths
}

// buildVaultPaths builds and returns all vault paths to try.
func (p *vaultArgProcessor) buildVaultPaths(ev *Evaluator) ([]string, error) {
	// Check if we have multiple path arguments (vault-try style)
	if p.detectMultiplePathArgs(ev) && len(p.args) >= 2 {
		// Multiple arguments mode - each arg is a separate path
		// Last arg is the default unless there's a LogicalOr
		paths := make([]string, 0)

		argsToProcess := p.args
		if !p.hasDefault {
			// Last argument might be a default value, check if it's a path
			lastArg := p.args[len(p.args)-1]
			if !isVaultPathString(ev, lastArg) {
				// Last arg is not a path, treat it as default
				argsToProcess = p.args[:len(p.args)-1]
				p.hasDefault = true
				p.defaultExpr = lastArg
			}
		}

		// Process each argument as a separate path
		for i, arg := range argsToProcess {
			path, err := p.resolveToString(ev, arg)
			if err != nil {
				DEBUG("  failed to resolve path arg[%d]: %s", i, err)
				return nil, err
			}
			paths = append(paths, path)
		}

		DEBUG("  vault paths to try (multi-arg mode): %v", paths)
		return paths, nil
	}

	// Single concatenated path mode (classic)
	path, err := p.buildVaultPath(ev)
	if err != nil {
		return nil, err
	}

	// Then split it into multiple paths if needed (semicolon mode)
	paths := p.splitVaultPaths(path)

	DEBUG("  vault paths to try: %v", paths)
	return paths, nil
}

// evaluateDefault evaluates the default expression if one exists.
func (p *vaultArgProcessor) evaluateDefault(ev *Evaluator) (interface{}, error) {
	if !p.hasDefault || p.defaultExpr == nil {
		return nil, fmt.Errorf("no default value available")
	}

	DEBUG("  evaluating default expression")
	// Use ResolveOperatorArgument to support nested expressions in defaults
	value, err := ResolveOperatorArgument(ev, p.defaultExpr)
	if err != nil {
		return nil, fmt.Errorf("unable to evaluate default value: %w", err)
	}

	return value, nil
}

// isVaultNotFound checks if an error indicates a missing secret.
func isVaultNotFound(err error) bool {
	if err == nil {
		return false
	}
	// The detailed text is the text this check has always read.
	errMsg := errtext.Detail(err)
	return strings.Contains(errMsg, "not found") ||
		strings.Contains(errMsg, "404") ||
		strings.Contains(errMsg, "secret not found")
}

// The VaultOperator provides a means of injecting credentials and
// other secrets from a Vault (vaultproject.io) Secure Key Storage
// instance.
//
// VaultOperator supports the `@target` operator-call syntax (e.g.
// `(( vault@production "secret/path:key" ))`): Opcall.Run sets
// Evaluator.Target from the parsed Expr's target before calling Run, and
// performVaultLookup selects a pooled, target-specific client from
// internal/backends/vault.DefaultPool.GetClient when it is non-empty,
// falling back to the default environment-initialized client otherwise.
type VaultOperator struct{}

// SupportsTarget reports that vault honors "@target".
func (VaultOperator) SupportsTarget() bool {
	return true
}

// Setup ...
func (VaultOperator) Setup() error {
	return nil
}

// Phase identifies what phase of document management the vault
// operator should be evaluated in.  Vault lives in the Eval phase.
func (VaultOperator) Phase() OperatorPhase {
	return EvalPhase
}

// Dependencies collects implicit dependencies that a given `(( vault ... ))`
// call has. There are no dependencies other that those given as args to the
// command.
func (VaultOperator) Dependencies(_ *Evaluator, _ []*Expr, _ []*tree.Cursor, auto []*tree.Cursor) []*tree.Cursor {
	return auto
}

// Run executes the `(( vault ... ))` operator call, which entails
// interacting with the (unsealed) Vault instance to retrieve the
// given secrets.
func (o VaultOperator) Run(ev *Evaluator, args []*Expr) (*Response, error) {
	DEBUG("running (( vault ... )) operation at $.%s", ev.Here)
	defer DEBUG("done with (( vault ... )) operation at $.%s\n", ev.Here)

	// Get engine
	engine := graft.GetEngine(ev)

	// syntax: (( vault "secret/path:key" ))
	// syntax: (( vault path.object "to concat with" other.object ))
	// syntax: (( vault "secret/path:key" || "default" ))
	// syntax: (( vault prefix "/" key ":password" || "default" ))
	// syntax: (( vault ( meta.vault_path meta.stub  ":" ("key1" | "key2" ) | meta.exodus_path "subpath:key1") || "default"))
	//
	if len(args) < 1 {
		return nil, fmt.Errorf("vault operator requires at least one argument")
	}

	// --skip-vault (not REDACT=1 - see IsRedactMode) defers this call
	// entirely, without resolving any argument, instead of contacting
	// Vault or substituting "REDACTED". Checked at this entry point
	// (rather than deep inside performVaultLookup, where the REDACT-mode
	// substitution below still happens unchanged) so a value this call's
	// own arguments depend on need not resolve either - see
	// op_skip_defer.go.
	if engine.GetOperatorState().IsVaultSkipped() && !engine.GetOperatorState().IsRedactMode() {
		return deferSkippedCall(ev, engine, "vault", args), nil
	}

	// Detect if we need enhanced parsing for sub-operators
	if o.needsEnhancedParsing(args) {
		DEBUG("vault: using enhanced parsing with sub-operators")
		return o.runWithSubOperators(ev, args, engine)
	}

	// Use classic implementation for backward compatibility
	DEBUG("vault: using classic parsing")
	return o.runClassic(ev, args, engine)
}

// needsEnhancedParsing checks if any arguments contain vault sub-operators.
func (o VaultOperator) needsEnhancedParsing(args []*Expr) bool {
	for _, arg := range args {
		if arg == nil {
			continue
		}

		switch arg.Type {
		case graft.VaultGroup, graft.VaultChoice:
			return true
		case graft.Literal:
			// Check if literal contains sub-operator syntax
			if str, ok := arg.Literal.(string); ok && ContainsSubOperators(str) {
				return true
			}
		case graft.Reference, graft.List, graft.Or, graft.Negate,
			graft.Addition, graft.Subtraction, graft.Multiplication, graft.Division, graft.Modulo,
			graft.Equal, graft.NotEqual, graft.LessThan, graft.LessThanOrEqual, graft.GreaterThan, graft.GreaterThanOrEqual,
			graft.LogicalAnd, graft.LogicalOr, graft.RegexpMatch, graft.EnvVar, graft.BoshVar, graft.OperatorCall:
			// These types don't need enhanced parsing
		}
	}
	return false
}

// runClassic executes vault operator with classic logic (backward compatibility).
func (o VaultOperator) runClassic(ev *Evaluator, args []*Expr, engine graft.Engine) (*Response, error) {
	// Use the existing argument processor
	processor := newVaultArgProcessor(args)

	// Build all vault paths from arguments
	paths, err := processor.buildVaultPaths(ev)
	if err != nil {
		// Failed to build paths, check if we have a default
		if processor.hasDefault {
			DEBUG("vault: failed to build paths (%s), evaluating default value", err)
			defaultValue, evalErr := processor.evaluateDefault(ev)
			if evalErr != nil {
				return nil, fmt.Errorf("unable to evaluate default value: %w", evalErr)
			}
			return &Response{
				Type:  Replace,
				Value: defaultValue,
			}, nil
		}
		return nil, err
	}

	return o.tryVaultPaths(ev, engine, paths, processor)
}

// runWithSubOperators executes vault operator with sub-operator support.
func (o VaultOperator) runWithSubOperators(ev *Evaluator, args []*Expr, engine graft.Engine) (*Response, error) {
	// Use enhanced argument processor
	processor := newVaultArgProcessor(args)

	// Build all vault paths from arguments (with sub-operator support)
	paths, err := processor.buildVaultPaths(ev)
	if err != nil {
		// Failed to build paths, check if we have a default
		if processor.hasDefault {
			DEBUG("vault: failed to build paths (%s), evaluating default value", err)
			defaultValue, evalErr := processor.evaluateDefault(ev)
			if evalErr != nil {
				return nil, fmt.Errorf("unable to evaluate default value: %w", evalErr)
			}
			return &Response{
				Type:  Replace,
				Value: defaultValue,
			}, nil
		}
		return nil, err
	}

	return o.tryVaultPaths(ev, engine, paths, processor)
}

// tryVaultPaths attempts to retrieve secrets from a list of vault paths.
func (o VaultOperator) tryVaultPaths(ev *Evaluator, engine graft.Engine, paths []string, processor *vaultArgProcessor) (*Response, error) {
	// Try each path in order
	var lastErr error
	for i, key := range paths {
		DEBUG("vault: trying path %d of %d: %s", i+1, len(paths), key)

		// Track vault references using engine context
		engine.GetOperatorState().AddVaultRef(key, []string{ev.Here.String()})

		// Perform the vault lookup
		secret, err := o.performVaultLookup(ev, engine, ev.Target, key)
		if err == nil {
			// Success!
			DEBUG("vault: path %d succeeded", i+1)
			return &Response{
				Type:  Replace,
				Value: secret,
			}, nil
		}

		// Remember the last error
		lastErr = err
		DEBUG("vault: path %d failed: %s", i+1, err)

		// For non-404 errors on single path, fail immediately
		if len(paths) == 1 && !isVaultNotFound(err) {
			break
		}
	}

	// All paths failed, check if we should try the default
	if processor.hasDefault && (lastErr == nil || isVaultNotFound(lastErr)) {
		DEBUG("vault: all paths failed, evaluating default value")
		defaultValue, evalErr := processor.evaluateDefault(ev)
		if evalErr != nil {
			return nil, fmt.Errorf("unable to evaluate default value: %w", evalErr)
		}
		return &Response{
			Type:  Replace,
			Value: defaultValue,
		}, nil
	}

	// Return the last error
	if lastErr != nil {
		return nil, lastErr
	}

	// This shouldn't happen, but just in case
	return nil, fmt.Errorf("vault operator failed to retrieve secret")
}

// performVaultLookup performs the actual vault lookup. target selects a
// pooled, target-specific client when non-empty; an
// empty target uses the default environment-initialized client, unchanged
// from before target support existed.
//
// When features.FeatureBackendRegistry is enabled on ev's engine and a
// custom backend is registered under the name "vault"
// (resolveCustomBackend), that backend is consulted instead of the
// internal/backends/vault path below, and key is passed to it exactly as
// received - the "path/to/secret:key" colon convention validated a few
// lines down is a built-in-Vault-reader concern, not a Backend interface
// requirement, so a custom backend is never subjected to it.
func (o VaultOperator) performVaultLookup(ev *graft.Evaluator, engine graft.Engine, target, key string) (string, error) {
	// By the time execution reaches here, IsVaultSkipped() can only be
	// true because of redact mode (REDACT=1, or vaultinfo's own
	// WithRedact(true)): the non-redact "--skip-vault flag" case already
	// returned a deferred "(( vault ... ))" string from VaultOperator.Run/
	// VaultTryOperator.Run's own entry-point check, before either ever
	// calls this method. This function itself does not need to check
	// IsRedactMode(), only IsVaultSkipped().
	if engine.GetOperatorState().IsVaultSkipped() {
		// Remember which vault key this skip-vault sentinel stands in
		// for, keyed by the tree path it is about to be written to
		// (ev.Here), so a later vault-path-building reference to this
		// same path can render "<key>" instead of the literal
		// "REDACTED" text (see vaultArgProcessor.resolveToString).
		// Shared by vault-try too (it calls this same method).
		engine.GetOperatorState().RecordVaultPlaceholder(ev.Here.String(), key)
		return redactedValue, nil
	}

	if backend, ok := resolveCustomBackend(ev, "vault"); ok {
		val, fetchErr := fetchFromBackend(ev, backend, target, key)
		if fetchErr != nil {
			if isBackendNotFound(fetchErr) {
				// Match the built-in path's exact "secret <key> not
				// found" shape (below) so the Genesis compatibility
				// contract's not-found detection - "starts with `secret
				// `, ends with ` not found`" - keeps working for custom
				// backends.
				return "", graft.WithCode(secretNotFound(key), graft.CodeSecretNotFound)
			}
			return "", wrapBackendError("vault", target, key, fetchErr)
		}
		return stringifyBackendValue(val), nil
	}

	reader, err := o.resolveReader(engine, target)
	if err != nil {
		return "", err
	}

	leftPart, rightPart := vault.ParsePath(key)
	if leftPart == "" || rightPart == "" {
		return "", errtext.WithPlain(errtext.Colorf("@R{invalid argument} @c{%s}@R{; must be in the form} @m{path/to/secret:key}", key), "invalid argument; must be in the form path/to/secret:key")
	}

	// Cache key is namespaced by target so the same path on two different
	// Vault instances never collides: without this, a
	// cached lookup made against the default instance could silently
	// satisfy a later "@target" lookup for the same path against a
	// different instance, or vice versa. GetOrFetch both serves cached
	// values without a network call and coalesces concurrent requests for
	// the same cache key into one Vault request.
	cacheKey := leftPart
	if target != "" {
		cacheKey = target + "\x00" + leftPart
	}
	fetch := func() (map[string]interface{}, error) {
		DEBUG("vault: Cache MISS for `%s`", leftPart)
		secretData, secretErr := vault.GetSecretWithReader(reader, leftPart)
		if secretErr != nil {
			// Normalize the error messages
			var notFoundErr *vault.ErrNotFound
			if errors.As(secretErr, &notFoundErr) {
				secretErr = graft.WithCode(secretNotFound(key), graft.CodeSecretNotFound)
			} else {
				// The Vault client's errors quote the request URL,
				// which holds the secret path.
				secretErr = errtext.WithPlain(secretErr, "unable to read the secret from vault")
			}
			return nil, secretErr
		}
		return secretData, nil
	}

	var fullSecret map[string]interface{}
	var fetchErr error
	if ShouldSkipCache(ev) {
		// ":nocache" bypasses both the cache read and the cache write: a
		// nocache fetch must neither be served from nor poison/refresh
		// the shared entry. The cache key above is never altered by the
		// modifier.
		DEBUG("vault: :nocache - bypassing secret cache for `%s`", leftPart)
		fullSecret, fetchErr = fetch()
	} else {
		fullSecret, fetchErr = vault.SecretCache.GetOrFetch(cacheKey, fetch)
	}
	if fetchErr != nil {
		return "", fetchErr
	}

	secret, extractErr := vault.ExtractSubkey(fullSecret, leftPart, rightPart)
	if extractErr != nil {
		return "", extractErr
	}
	return secret, nil
}

// resolveReader returns the Reader to use for a lookup: the pooled,
// target-specific reader when target is non-empty, otherwise the default
// environment-initialized reader. A non-empty target
// that cannot be resolved is a hard error — unlike the no-target path,
// there is no fallback, since silently falling back to the default
// instance is exactly the wrong-instance-read bug this wiring fixes.
func (o VaultOperator) resolveReader(engine graft.Engine, target string) (vault.Reader, error) {
	if target != "" {
		reader, err := vault.DefaultPool.GetClient(target, engine)
		if err != nil {
			return nil, fmt.Errorf("error selecting Vault target %q: %w", target, err)
		}
		DEBUG("vault: using pooled client for target %q", target)
		return reader, nil
	}

	reader, err := vault.DefaultReader()
	if err != nil {
		return nil, fmt.Errorf("Error during Vault client initialization: %w", err)
	}
	DEBUG("vault: using default client")
	return reader, nil
}

// VaultTryOperator is a deprecated alias for VaultOperator
// It maintains backward compatibility but logs a deprecation warning.
type VaultTryOperator struct{}

// SupportsTarget reports that vault-try honors "@target", matching vault:
// it shares performVaultLookup and the same pooled-client selection.
func (VaultTryOperator) SupportsTarget() bool {
	return true
}

// Setup initializes the operator.
func (VaultTryOperator) Setup() error {
	return nil
}

// Phase identifies when this operator runs.
func (VaultTryOperator) Phase() OperatorPhase {
	return EvalPhase
}

// Dependencies returns the dependencies for this operator.
func (VaultTryOperator) Dependencies(_ *Evaluator, _ []*Expr, _ []*tree.Cursor, auto []*tree.Cursor) []*tree.Cursor {
	return auto
}

// Run executes vault-try by maintaining its original behavior.
func (o VaultTryOperator) Run(ev *Evaluator, args []*Expr) (*Response, error) {
	DEBUG("running (( vault-try ... )) operation at $.%s", ev.Here)
	DEBUG("WARNING: vault-try is deprecated. Consider using vault with semicolon-separated paths: (( vault \"path1:key; path2:key\" || \"default\" ))")
	defer DEBUG("done with (( vault-try ... )) operation at $.%s\n", ev.Here)

	// Minimum 2 arguments: at least one vault path and a default
	if len(args) < 2 {
		return nil, fmt.Errorf("vault-try operator requires at least 2 arguments (one or more vault paths, followed by a default value)")
	}

	// The last argument is always the default
	vaultPaths := args[:len(args)-1]
	defaultExpr := args[len(args)-1]

	// Get engine
	engine := graft.GetEngine(ev)

	// --skip-vault (not REDACT=1) defers this call entirely, without
	// resolving any path or the default - see VaultOperator.Run's
	// identical check and op_skip_defer.go.
	if engine.GetOperatorState().IsVaultSkipped() && !engine.GetOperatorState().IsRedactMode() {
		return deferSkippedCall(ev, engine, "vault-try", args), nil
	}

	// Try each vault path in order
	for i, pathExpr := range vaultPaths {
		DEBUG("vault-try: attempting path %d of %d", i+1, len(vaultPaths))

		// Resolve the path expression to a string
		val, err := ResolveOperatorArgument(ev, pathExpr)
		if err != nil {
			DEBUG("vault-try: path %d failed to resolve: %s", i+1, err)
			continue // Skip to next path
		}

		if val == nil {
			DEBUG("vault-try: path %d resolved to nil", i+1)
			continue // Skip to next path
		}

		// Convert to string
		path, err := AsString(val)
		if err != nil {
			DEBUG("vault-try: path %d is not a string: %s", i+1, err)
			continue // Skip to next path
		}

		if path == "" {
			DEBUG("vault-try: path %d is empty", i+1)
			continue // Skip to next path
		}

		// Validate path format (forgiving - just continue on malformed)
		if !strings.Contains(path, ":") {
			DEBUG("vault-try: path %d is malformed (no colon)", i+1)
			continue // Skip to next path
		}

		parts := strings.Split(path, ":")
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			DEBUG("vault-try: path %d is malformed", i+1)
			continue // Skip to next path
		}

		// Track this vault reference
		engine.GetOperatorState().AddVaultRef(path, []string{ev.Here.String()})

		// Use the shared vault infrastructure
		vaultOp := VaultOperator{}
		secret, err := vaultOp.performVaultLookup(ev, engine, ev.Target, path)
		if err == nil {
			// Success!
			DEBUG("vault-try: path %d succeeded", i+1)
			return &Response{
				Type:  Replace,
				Value: secret,
			}, nil
		}

		// Log the error but continue to next path
		DEBUG("vault-try: path %d failed: %s", i+1, err)
	}

	// All vault paths failed, use the default value
	DEBUG("vault-try: all paths failed, evaluating default value")
	defaultValue, err := ResolveOperatorArgument(ev, defaultExpr)
	if err != nil {
		return nil, fmt.Errorf("unable to evaluate default value: %w", err)
	}

	return &Response{
		Type:  Replace,
		Value: defaultValue,
	}, nil
}

// secretNotFound is the vault operator's error for a secret or key that
// does not exist. Error() keeps the "secret ... not found" shape without
// the path, and the detailed text names the path as the CLI always has.
func secretNotFound(key string) error {
	return errtext.WithPlain(fmt.Errorf("secret %s not found", key), "secret not found")
}

//nolint:gochecknoinits // Operator registration must happen at package load time
func init() {
	RegisterOp("vault", VaultOperator{})
	RegisterOp("vault-try", VaultTryOperator{})
}
