package graft

import (
	"context"
	"errors"
	"time"

	"github.com/fivetwenty-io/graft/pkg/graft/postprocess"
)

// PostProcessPhase controls when a PostProcessor runs relative to other
// registered processors. It is an alias for postprocess.Phase - the two
// types are interchangeable - so the constants below are exactly the
// values postprocess.Phase already defines; nothing is duplicated.
type PostProcessPhase = postprocess.Phase

const (
	// PhaseEarly runs first, immediately after graft's own evaluation,
	// pruning, and cherry-picking finish (see applyPostProcessing).
	PhaseEarly = postprocess.PhaseEarly

	// PhaseNormal is the default phase for a processor with no strong
	// ordering requirement.
	PhaseNormal = postprocess.PhaseNormal

	// PhaseLate runs last, immediately before Execute() returns.
	PhaseLate = postprocess.PhaseLate
)

// ProcessMetadata is passed to every PostProcessor's Process call,
// describing the merge that produced the document being processed.
//
// Two fields are always zero-valued in this release, for structural
// reasons rather than oversight:
//
//   - Sources: the merge builder does not record the file paths documents
//     were loaded from anywhere Execute can read them back (OverlayFile/
//     MergeFiles/MergeReaders take paths/readers but nothing threads a
//     path onto the resulting Document or the builder). Populating this
//     requires builder-level path tracking that does not exist yet.
//   - ParseDuration: parsing happens before a MergeBuilder exists at all
//     (ParseFile/ParseYAML/ParseReader/OverlayFile all run ahead of
//     Execute), so Execute has no parse-phase interval to measure.
//
// MergeDuration and EvalDuration are both measured directly in Execute
// and applyPostProcessing and are accurate. EvalCount is always 0: the
// evaluator does not report an operator-evaluation count anywhere Execute
// can read it. Custom is always nil: nothing populates it in this
// release.
type ProcessMetadata struct {
	// Sources lists the input file paths that were merged. Always empty
	// in this release; see the type doc comment.
	Sources []string

	// MergeCount is the number of documents merged to produce the input
	// to the post-processor pipeline (len of the builder's document
	// list at Execute time).
	MergeCount int

	// EvalCount is always 0 in this release; see the type doc comment.
	EvalCount int

	// StartTime is when Execute began processing this merge.
	StartTime time.Time

	// Duration is the elapsed time since StartTime as of the moment this
	// particular PostProcessor's Process method was invoked. It is
	// refreshed before each processor in the pipeline runs, so two
	// processors in the same Execute() call observe different (growing)
	// values rather than a single pipeline-wide snapshot.
	Duration time.Duration

	// ParseDuration is always zero in this release; see the type doc
	// comment.
	ParseDuration time.Duration

	// MergeDuration is how long the merge phase took: combining the
	// builder's documents into a single Document, before evaluation.
	MergeDuration time.Duration

	// EvalDuration is how long operator evaluation took. Zero if the
	// builder's SkipEvaluation was set (evaluation did not run).
	EvalDuration time.Duration

	// Custom carries operator- or caller-supplied metadata. Always nil
	// in this release; nothing populates it yet.
	Custom map[string]interface{}
}

// PostProcessor is a caller-supplied hook that runs after a merge's
// evaluation, pruning, and cherry-picking, in Phase-then-Priority order
// (see PostProcessPhase and PriorityPostProcessor). Register one with
// WithPostProcessors, either as an EngineOption (applies to every merge
// on that engine) or as a MergeBuilder method (applies to that one merge
// chain only); both sets combine when both are used.
//
// Unlike Document, Engine, and MergeBuilder, PostProcessor is exactly the
// kind of interface callers are expected to implement themselves -
// WithPostProcessors exists to accept exactly that, so it carries no
// "not intended to be implemented outside this package" restriction. Its
// stability contract runs the other way: PostProcessor's three methods
// are fixed for this interface. A capability that needs a new method
// (matching PriorityPostProcessor's pattern) will arrive as a new,
// separate optional interface a processor can additionally implement,
// never as a fourth method added to PostProcessor itself - so an existing
// implementation keeps compiling and keeps working across minor releases
// without changes.
type PostProcessor interface {
	// Name identifies the processor. It appears in the error Execute()
	// returns when Process fails ("post-processor %q failed: %w") and
	// nowhere else - it does not need to be unique, but a descriptive
	// name makes failures easier to diagnose.
	Name() string

	// Phase reports when this processor runs relative to others; see
	// PostProcessPhase.
	Phase() PostProcessPhase

	// Process transforms doc and returns the result. Returning a
	// non-nil error aborts Execute(): no later processor runs, and
	// Execute returns fmt.Errorf("post-processor %q failed: %w",
	// Name(), err). Process must not retain doc, its RawData(), or its
	// returned Document past the call: the merge builder may reuse or
	// discard the underlying data once Process returns.
	Process(ctx context.Context, doc Document, meta *ProcessMetadata) (Document, error)
}

// PriorityPostProcessor is an optional extension of PostProcessor: a
// processor that also implements it controls its execution order among
// other processors declaring the same Phase (lower Priority runs first).
// A processor that does not implement PriorityPostProcessor gets a
// default priority based on its Phase alone (0 for PhaseEarly, 50 for
// PhaseNormal, 100 for PhaseLate), matching
// postprocess.Pipeline's own default exactly.
type PriorityPostProcessor interface {
	PostProcessor
	Priority() int
}

// defaultPriorityForPhase mirrors postprocess.getProcessorPriority's own
// phase-based fallback (pipeline.go), used by postProcessorAdapter and
// wrappedProcessor so wrapping a processor in either direction never
// changes its effective priority relative to one that implements
// PriorityPostProcessor/postprocess.PriorityProcessor directly.
func defaultPriorityForPhase(phase PostProcessPhase) int {
	switch phase {
	case postprocess.PhaseEarly:
		return 0
	case postprocess.PhaseLate:
		return 100
	default:
		return 50
	}
}

// postProcessorAdapter adapts a PostProcessor (Document-based) to
// postprocess.Processor (interface{}-based), so user-supplied processors
// can run inside the existing, already-tested postprocess.Pipeline - see
// (*mergeBuilderImpl).runPostProcessors, the sole constructor of this
// type. meta is shared by every adapter built for one Execute() call;
// Process refreshes meta.Duration immediately before invoking pp, giving
// each processor an accurate elapsed-so-far reading instead of a single
// pipeline-wide snapshot taken before any processor ran.
type postProcessorAdapter struct {
	pp   PostProcessor
	meta *ProcessMetadata
}

func (a *postProcessorAdapter) Name() string { return a.pp.Name() }

func (a *postProcessorAdapter) Phase() postprocess.Phase { return a.pp.Phase() }

// Priority is always implemented on the adapter (never conditionally),
// so postprocess.getProcessorPriority's own type assertion always
// succeeds; defaultPriorityForPhase reproduces its fallback exactly when
// pp does not implement PriorityPostProcessor, so wrapping a processor in
// this adapter never changes its effective priority.
func (a *postProcessorAdapter) Priority() int {
	if pp, ok := a.pp.(PriorityPostProcessor); ok {
		return pp.Priority()
	}
	return defaultPriorityForPhase(a.pp.Phase())
}

func (a *postProcessorAdapter) Process(ctx context.Context, doc interface{}, _ *postprocess.Metadata) (interface{}, error) {
	d, err := NewDocumentFromInterface(doc)
	if err != nil {
		return doc, err
	}

	a.meta.Duration = time.Since(a.meta.StartTime)

	result, err := a.pp.Process(ctx, d, a.meta)
	if err != nil {
		return doc, err
	}
	if result == nil {
		return nil, errors.New("post-processor returned a nil document")
	}

	return result.RawData(), nil
}

// runPostProcessors executes m.postProcessors, in Phase-then-Priority
// order, against result. startTime is Execute's overall start (becomes
// ProcessMetadata.StartTime); mergeDuration and evalDuration become
// ProcessMetadata.MergeDuration/EvalDuration. It is a true no-op - no
// pipeline constructed, no allocation beyond the empty-processors check
// itself - when m.postProcessors is empty, so a merge that never calls
// WithPostProcessors pays nothing for this feature (see
// BenchmarkExecute_NoPostProcessors in postprocessor_test.go).
//
// Errors surface exactly as postprocess.Pipeline.ProcessWithContext
// formats them: fmt.Errorf("post-processor %q failed: %w", name, err).
func (m *mergeBuilderImpl) runPostProcessors(result Document, startTime time.Time, mergeDuration, evalDuration time.Duration) (Document, error) {
	if len(m.postProcessors) == 0 {
		return result, nil
	}

	meta := &ProcessMetadata{
		MergeCount:    len(m.docs),
		StartTime:     startTime,
		Duration:      time.Since(startTime),
		MergeDuration: mergeDuration,
		EvalDuration:  evalDuration,
	}

	pipeline := postprocess.NewPipeline()
	for _, pp := range m.postProcessors {
		if pp == nil {
			// A nil entry can only reach here via a variadic
			// WithPostProcessors(nil) call; skip it rather than
			// letting the adapter panic dereferencing a nil
			// interface's methods.
			continue
		}
		pipeline.Add(&postProcessorAdapter{pp: pp, meta: meta})
	}

	processed, err := pipeline.ProcessWithContext(m.ctx, result.RawData(), nil)
	if err != nil {
		return nil, err
	}

	return NewDocumentFromInterface(processed)
}

// wrappedProcessor adapts a postprocess.Processor (the postprocess
// package's raw, interface{}-based processors) to the Document-based
// PostProcessor contract, so postprocess's built-ins can be handed to
// WithPostProcessors without exposing postprocess.Processor's
// interface{} signature as public API. Used by NewPruner, NewCherryPicker,
// and NewSecurityRedactor below; NewKeySorter deliberately does not use
// it (see that function's doc comment).
type wrappedProcessor struct {
	inner postprocess.Processor
}

func (w *wrappedProcessor) Name() string { return w.inner.Name() }

func (w *wrappedProcessor) Phase() PostProcessPhase { return w.inner.Phase() }

func (w *wrappedProcessor) Priority() int {
	if pp, ok := w.inner.(postprocess.PriorityProcessor); ok {
		return pp.Priority()
	}
	return defaultPriorityForPhase(w.inner.Phase())
}

func (w *wrappedProcessor) Process(ctx context.Context, doc Document, meta *ProcessMetadata) (Document, error) {
	ppMeta := &postprocess.Metadata{
		Sources:    meta.Sources,
		MergeCount: meta.MergeCount,
		EvalCount:  meta.EvalCount,
		StartTime:  meta.StartTime,
		Duration:   meta.Duration,
	}

	result, err := w.inner.Process(ctx, doc.RawData(), ppMeta)
	if err != nil {
		return nil, err
	}

	return NewDocumentFromInterface(result)
}

// NewPruner returns a PostProcessor that removes the given dot-separated
// paths from the document, wrapping postprocess.PathPruner. Unlike
// MergeBuilder.WithPrune (which runs before any WithPostProcessors
// processor - see applyPostProcessing's ordering), a pruner registered
// this way runs at the Phase/Priority position it declares (PhaseLate),
// interleaved with any other post-processors at that phase.
func NewPruner(paths ...string) PostProcessor {
	return &wrappedProcessor{inner: postprocess.NewPathPruner(paths)}
}

// NewCherryPicker returns a PostProcessor that keeps only the given
// dot-separated paths in the document (discarding everything else),
// wrapping postprocess.CherryPickProcessor. Like NewPruner, it runs at
// its declared Phase/Priority position rather than at the fixed point
// MergeBuilder.WithCherryPick uses.
func NewCherryPicker(paths ...string) PostProcessor {
	return &wrappedProcessor{inner: postprocess.NewCherryPickProcessor(paths)}
}

// keySorterProcessor sorts a Document's map keys via Document.SortKeys.
type keySorterProcessor struct {
	enabled bool
}

// NewKeySorter returns a PostProcessor that sorts the document's map keys
// recursively when enabled is true (a no-op otherwise, so it can be left
// registered and toggled by the enabled argument alone). It is
// implemented directly against Document.SortKeys rather than by wrapping
// postprocess.KeySorter: that type's Process method returns a
// *postprocess.SortedMap serialization hint at the document root, which
// is not a map[string]interface{} and so cannot be handed to
// NewDocumentFromInterface - Document has no concept of that wrapper.
// Document.SortKeys already solves the same problem inside the Document
// implementation, so NewKeySorter reuses it instead of introducing a
// second, incompatible sorted-map representation into the public API.
func NewKeySorter(enabled bool) PostProcessor {
	return &keySorterProcessor{enabled: enabled}
}

func (k *keySorterProcessor) Name() string { return "key-sorter" }

func (k *keySorterProcessor) Phase() PostProcessPhase { return PhaseLate }

func (k *keySorterProcessor) Priority() int { return 100 }

func (k *keySorterProcessor) Process(_ context.Context, doc Document, _ *ProcessMetadata) (Document, error) {
	if !k.enabled {
		return doc, nil
	}
	return doc.SortKeys(), nil
}

// NewSecurityRedactor returns a PostProcessor that replaces the value of
// any map entry whose key matches one of patterns with mask, wrapping
// postprocess.SecurityRedactor. An empty mask defaults to
// postprocess.DefaultRedactionMask ("***REDACTED***"). See
// postprocess.SecurityRedactor's doc comment for the exact matching
// rules (case-insensitive regular expressions matched against key names,
// not values).
func NewSecurityRedactor(patterns []string, mask string) PostProcessor {
	return &wrappedProcessor{inner: postprocess.NewSecurityRedactor(patterns, mask)}
}
