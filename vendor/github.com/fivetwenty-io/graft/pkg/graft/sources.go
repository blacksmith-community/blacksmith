package graft

import "context"

// SourceRef identifies one merge input for provenance reporting.
//
// It exists so a failed merge can name the file and line an operator was
// written at. Nothing reads a SourceRef on a successful merge.
//
// Memory: Bytes aliases the slice the caller already read; there is no
// copy. The refs live on the context the caller supplies and on the
// merge builder's own copy of it, so an operator-bearing input stays
// reachable for as long as the caller keeps that builder alive. A CLI
// run drops both when the merge returns; a long-lived library caller
// that retains a builder retains every operator-bearing input with it.
type SourceRef struct {
	// Name is the input's display name, e.g. a file path or "STDIN".
	Name string

	// Bytes are the input's contents as the engine's parse-time rewrites
	// will see them, or nil when this input contributes no positions.
	// Callers should populate it only for inputs that actually contain
	// operator text.
	Bytes []byte

	// Opaque marks an input whose merged content cannot be mapped back to
	// any bytes we hold: a control-flow document (the expander rewrites
	// it wholesale, so no mapping to original lines survives), a JSON or
	// go-patch input, or an input of unknown origin.
	//
	// Opaque is not the same as an empty Bytes. An ordinary YAML file
	// with no operator text yields a complete, legitimately empty index,
	// and can therefore be ruled out as a node's origin. An Opaque input
	// cannot be ruled out, which is why its presence disables the
	// expression-matching fallback for the whole merge.
	Opaque bool
}

// sourceRefsKey is the context key for merge input references. Threading
// them through context, rather than through a new MergeBuilder method,
// matches the existing cherry-pick and prior-calc-values precedents and
// avoids changing an exported interface's shape.
type sourceRefsKey struct{}

// WithSourceRefs attaches merge input references to ctx. The engine
// copies them onto the evaluator, which uses them only when reporting an
// operator data-flow cycle.
func WithSourceRefs(ctx context.Context, refs []SourceRef) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, sourceRefsKey{}, refs)
}

// GetSourceRefs extracts merge input references from ctx, or nil if none
// were attached.
func GetSourceRefs(ctx context.Context) []SourceRef {
	if ctx == nil {
		return nil
	}
	if refs, ok := ctx.Value(sourceRefsKey{}).([]SourceRef); ok {
		return refs
	}
	return nil
}
