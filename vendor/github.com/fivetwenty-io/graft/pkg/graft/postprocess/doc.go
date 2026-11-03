// Package postprocess provides post-processing handlers that run on a
// merged document's raw map[string]interface{}/[]interface{} data, after
// operator evaluation. This package is deliberately independent of
// pkg/graft (imports only the standard library), so it defines its own
// interface{}-based Processor contract rather than depending on
// pkg/graft.Document.
//
// Direct use of this package's exported types is unusual: pkg/graft wraps
// it behind a Document-typed public API - see graft.PostProcessor,
// graft.WithPostProcessors, and the graft.New* constructors
// (NewPruner, NewCherryPicker, NewKeySorter, NewSecurityRedactor) - which
// most callers should use instead. This package's own exports remain
// public for callers building a custom pipeline directly against
// map[string]interface{} data outside a graft.MergeBuilder, and because
// the graft-level adapter is built on top of them.
//
// # Processor interface
//
// Implement Processor to add a custom handler:
//
//	type Processor interface {
//	    Name() string
//	    Phase() Phase
//	    Process(ctx context.Context, doc interface{}, meta *Metadata) (interface{}, error)
//	}
//
// A Processor that also implements PriorityProcessor (adding
// Priority() int) controls its execution order among other processors
// declaring the same Phase; one that does not gets a default priority
// based on Phase alone (see sortProcessorsByPriority/getProcessorPriority
// in pipeline.go).
//
// # Phases
//
// Processors run in Phase order, then Priority order within a Phase:
//
//	const (
//	    PhaseEarly  Phase = iota // runs immediately after evaluation
//	    PhaseNormal              // runs during standard post-processing
//	    PhaseLate                // runs just before output
//	)
//
// There is no parallel execution model: Pipeline.ProcessWithContext runs
// every processor sequentially, in the single Phase-then-Priority order
// described above, checking ctx.Done() between processors.
//
// # Built-in processors
//
//   - PruneProcessor (NewPruneProcessor) removes values marked with the
//     "(( prune ))" string marker or a PruneMarker value.
//   - InjectProcessor (NewInjectProcessor) expands InjectMarker values and
//     "<<"-prefixed keys into their parent map.
//   - PathPruner (NewPathPruner) removes a fixed list of dot-separated
//     paths.
//   - CherryPickProcessor (NewCherryPickProcessor) keeps only a fixed list
//     of dot-separated paths, discarding everything else.
//   - KeySorter (NewKeySorter) marks maps for sorted-key output by
//     wrapping them in a SortedMap; see graft.NewKeySorter's doc comment
//     (pkg/graft/postprocessor.go) for why that SortedMap wrapper makes
//     this type unsuitable for direct use as a graft.PostProcessor -
//     KeySorter's own doc comment here says only what it does, not why
//     graft wraps it differently.
//   - SecurityRedactor (NewSecurityRedactor) replaces the value at any key
//     matching a caller-supplied pattern with a mask string.
//   - TransformProcessor (NewTransformProcessor) wraps an arbitrary
//     caller-supplied function as a Processor.
//
// DefaultPipeline builds a Pipeline with InjectProcessor and
// PruneProcessor; FullPipeline additionally adds an empty
// CherryPickProcessor and a disabled KeySorter.
//
// # Registering with graft
//
// graft.WithPostProcessors accepts graft.PostProcessor values - Document-
// typed, not the interface{}-typed Processor above:
//
//	engine, _ := graft.NewEngine(
//	    graft.WithPostProcessors(graft.NewSecurityRedactor(
//	        []string{"password", "secret"}, "",
//	    )),
//	)
package postprocess
