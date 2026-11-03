package operators

import (
	"context"
	"errors"
	"fmt"

	"github.com/fivetwenty-io/graft/internal/features"
	"github.com/fivetwenty-io/graft/pkg/graft"
)

// redactedValue is what the vault, AWS, and NATS operators substitute for
// a secret when their backend is skipped, so a document can be merged
// without credentials and without reaching a backend. Spruce emits the
// same literal.
const redactedValue = "REDACTED"

// resolveCustomBackend looks up a custom backend registered under name on
// the real engine bound to ev.
//
// It uses graft.EngineOf, not graft.GetEngine: EngineOf never constructs a
// throwaway default engine when ev.engine is nil, so a caller that never
// wired an engine onto ev (a wiring bug, or deliberate direct-Evaluator
// library use) always falls through to the existing internal/backends
// resolution unchanged, rather than resolving against a fresh, always-
// empty registry and reporting that as "no custom backend registered" -
// see the "Hazard in GetEngine" discussion this resolves
// (~/.agents/plans/graft-library-api-plan.md, C7 §4). Concretely, since
// CreateDefaultEngine's fallback engine always uses
// features.DefaultFlags() with no environment loading (only the CLI's
// explicit graft.NewEngine call in cmd/graft/main.go loads
// GRAFT_FEATURE_* via FeatureFlags.LoadFromEnv), a throwaway engine would
// have FeatureBackendRegistry off anyway and reach the same fallback by a
// different route; EngineOf makes that outcome true by construction
// instead of by that (accidental, changeable) invariant.
//
// Returns ok=false - meaning "fall back to the built-in resolution path
// unchanged" - whenever any of the following hold: ev has no real engine,
// the real engine does not have features.FeatureBackendRegistry enabled,
// or no backend is registered under name.
func resolveCustomBackend(ev *graft.Evaluator, name string) (graft.Backend, bool) {
	engine := graft.EngineOf(ev)
	if engine == nil {
		return nil, false
	}
	if !engine.IsFeatureEnabled(features.FeatureBackendRegistry) {
		return nil, false
	}
	return engine.GetBackend(name)
}

// fetchFromBackend calls backend.Get(ctx, path), or backend.GetWithTarget
// when target is non-empty. A non-empty target on a backend that does not
// implement graft.TargetedBackend is a hard configuration error (mirroring
// the existing hard-error behavior for "@target" against a target that
// cannot be resolved in op_vault.go's resolveReader and op_aws.go's
// resolveSession: silently ignoring the target risks reading from the
// wrong instance, which is worse than failing).
//
// When the running call carries the ":nocache" modifier (ShouldSkipCache
// on ev), the context is marked with graft.WithNoCacheContext so the
// registry's caching wrapper skips both its cache read and its cache
// write for this one call.
func fetchFromBackend(ev *graft.Evaluator, backend graft.Backend, target, path string) (interface{}, error) {
	ctx := context.Background()
	if ShouldSkipCache(ev) {
		ctx = graft.WithNoCacheContext(ctx)
	}

	if target == "" {
		return backend.Get(ctx, path)
	}

	tb, ok := backend.(graft.TargetedBackend)
	if !ok {
		return nil, fmt.Errorf("backend %q does not support @target selection", backend.Name())
	}
	return tb.GetWithTarget(ctx, target, path)
}

// stringifyBackendValue converts a custom backend's Get/GetWithTarget
// result to the string type the vault/awsparam/awssecret operators use for
// their Response.Value (matching the built-in vault/AWS resolution paths,
// which always produce a string - see performVaultLookup, getAwsParam,
// getAwsSecret). A string result passes through unchanged; any other type
// is formatted with fmt.Sprintf("%v", ...), the same fallback
// buildVaultPath/AwsOperator.Run already use for non-string argument
// values.
func stringifyBackendValue(val interface{}) string {
	if s, ok := val.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", val)
}

// wrapBackendError wraps a non-not-found backend failure as a
// *graft.GraftError{Type: ExternalError} carrying a *graft.BackendError as
// its Cause, reachable via errors.As. This matches the plan's error-shape
// requirement: *graft.BackendError is never the outermost error an
// operator returns, only reachable by unwrapping - see
// graft.BackendError's doc comment.
func wrapBackendError(name, target, path string, err error) error {
	be := &graft.BackendError{
		Backend: name,
		Target:  target,
		Path:    path,
		Message: err.Error(),
		Cause:   err,
	}
	return graft.NewExternalError(name, err.Error(), be)
}

// isBackendNotFound reports whether err (or anything it wraps) is
// graft.ErrBackendNotFound.
func isBackendNotFound(err error) bool {
	return errors.Is(err, graft.ErrBackendNotFound)
}
