package graft

import (
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/goccy/go-yaml"
)

// HistoryPhase aliases the existing ChangePhase (document_memory.go) -
// zero new vocabulary, zero breakage. PhaseLoad is an alternate, more
// public-API-friendly name for the existing PhaseInitial (no producer
// emits PhaseInitial today - see the doc comment on PhaseLoad below).
// PhasePostProcess is appended after PhaseManual so no existing constant
// is renumbered; ChangePhase values are not currently persisted anywhere,
// but treating them as stable avoids a future compatibility trap.
type HistoryPhase = ChangePhase

// HistoryOperation aliases the existing ChangeOperation.
type HistoryOperation = ChangeOperation

const (
	// PhaseLoad is an alias for PhaseInitial. Neither has a producer in
	// this release: grep of merger/merge.go, merge_builder_impl.go, and
	// evaluator.go shows only PhaseMerge (merge_builder_impl.go,
	// merger/merge.go) and PhaseEval (evaluator.go) are ever recorded.
	// PhaseLoad exists so callers switching on HistoryEntry.Phase can
	// handle it without a build failure if a future release adds a
	// load-phase producer; it will never appear in a HistoryEntry today.
	PhaseLoad = PhaseInitial

	// PhasePostProcess has no producer in this release: no code path
	// calls RecordChange with a phase after PhaseEval. Pruning, cherry-
	// picking, and post-processors (WithPostProcessors) run after
	// evaluation but do not record history at all - see the package doc
	// comment on History for the full list of gaps. Declared for the
	// same forward-compatibility reason as PhaseLoad.
	PhasePostProcess ChangePhase = PhaseManual + 1
)

// History operation aliases. HistorySet/HistoryOverwrite/HistoryDelete/
// HistoryPrune have no producer today (see the History doc comment);
// HistoryMerge and HistoryTransform are the only two ever recorded.
const (
	HistorySet       = OpSet
	HistoryMerge     = OpMerge
	HistoryOverwrite = OpReplace
	HistoryDelete    = OpDelete
	HistoryTransform = OpTransform
	HistoryPrune     = OpPrune
)

// HistoryEntry is one recorded change to one path, converted from
// DocumentMemory's internal ChangeEvent (Timeline/Query results) or
// NodeVersion (ForPath results). Every field is populated from data
// DocumentMemory actually stores; nothing here is synthesized.
//
// Two fields are permanently zero-valued in this release, not merely
// unpopulated by an oversight:
//
//   - Source is reserved for input-file provenance (e.g. "base.yml").
//     Nothing in the merge or evaluation path threads a file identity
//     down to DocumentMemory.RecordChange, so no HistoryEntry can ever
//     carry one; adding that plumbing is a separate, unstarted piece of
//     work. Use Operator (below) for the string DocumentMemory does
//     record at the recording site.
//   - Line is reserved the same way; graft's only line/column tracking
//     (pkg/graft/interfaces/position.go) is scoped to tokens inside a
//     single "(( ... ))" expression and never reaches merged values.
type HistoryEntry struct {
	// Index is this entry's position within the slice it was returned
	// in (0-based): the global Timeline()/Query() order for those
	// methods, or the per-path Versions order for ForPath. It is not a
	// single counter shared across every method - two calls returning
	// different slices both start their own Index at 0.
	Index int

	// Path is the canonical dotted path (no "$" prefix - see
	// history_path_vocab_test.go) that changed.
	Path string

	// Version is DocumentMemory's per-path version number for this
	// entry (NodeVersion.Version / ChangeEvent.Version), starting at 1
	// for a path's first recorded change.
	Version int

	// Timestamp is when DocumentMemory recorded the change (time.Now()
	// at the RecordChange call), not when the source file was read or
	// when the merge as a whole started.
	Timestamp time.Time

	// Phase is when in the pipeline the change happened. Only
	// PhaseMerge and PhaseEval are ever recorded in this release; see
	// the History doc comment for the full list of un-recorded phases.
	Phase HistoryPhase

	// Operation is the kind of change. Only OpMerge (merge-phase
	// map-key writes) and OpTransform (operator evaluation) are ever
	// recorded in this release.
	Operation HistoryOperation

	// OldValue is the value immediately before this change. Timeline()
	// and Query() populate it from DocumentMemory's ChangeEvent, which
	// carries whatever the recording call site itself passed as the
	// prior value - accurate even for a path's very first recorded
	// change (e.g. "localhost" the first time an existing key is
	// overwritten). ForPath() populates it differently: DocumentMemory's
	// NodeVersion has no old-value field of its own, so ForPath
	// reconstructs OldValue from the previous *recorded* version for the
	// same path, exactly as DocumentMemory.Compare does - nil for a
	// path's first recorded change, even when Timeline()/Query() report
	// a real prior value for that same entry. The two methods can
	// therefore disagree about OldValue on a path's first entry; prefer
	// Timeline()/Query() when the caller-supplied prior value matters.
	OldValue interface{}

	// NewValue is the value this change produced.
	NewValue interface{}

	// Source is always "" in this release; see the HistoryEntry doc
	// comment.
	Source string

	// Line is always 0 in this release; see the HistoryEntry doc
	// comment.
	Line int

	// Operator carries the raw string DocumentMemory recorded at this
	// entry: the literal merge verb ("merge", "add", or "delete") for a
	// PhaseMerge entry, or the operator name (e.g. "grab", "vault") for
	// a PhaseEval entry. It is not exclusively an operator name despite
	// the field name - the name is kept for source compatibility with
	// the documented target API - so check Phase before assuming it is
	// one.
	Operator string

	// Evaluated is true when Phase is PhaseEval: this entry's NewValue
	// is a post-evaluation result rather than raw merged text.
	Evaluated bool

	// Metadata is DocumentMemory's per-version metadata map, populated
	// only for ForPath results (NodeVersion carries it; ChangeEvent,
	// which backs Timeline/Query, does not - Metadata is always nil for
	// entries returned by those two methods). Compression, when it
	// runs, adds "compressed"/"compressed_key" entries here and clears
	// the version's Value; this field is the only place that is
	// visible.
	Metadata map[string]interface{}
}

// History queries DocumentMemory's recorded changes for one document.
// Query/ForPath/Timeline results reflect the engine's DocumentMemory as
// of the call, not a frozen snapshot from when Document.History() was
// obtained - a live veneer, not a second history engine (see the
// package's Wave B2 coordination note: internal/history and
// internal/histdiff are a separate, CLI-only snapshot-diff mechanism and
// share no code or data with this type).
//
// DocumentMemory is scoped to the whole engine that produced it, not to
// one merge: if TrackHistory() or engine-level WithHistoryTracking/
// WithMemoryConfig is used across more than one merge on the same
// *DefaultEngine, every tracked merge's Document.History() reflects
// every path any of those merges (or evaluations on that engine) has
// ever recorded, not just its own. Build a fresh engine per merge to
// isolate history between them. This scope is also an unbounded-memory
// concern, not only a correctness one: every tracked merge on that engine
// keeps adding to one DocumentMemory.timeline that HistoryConfig's
// MaxEntriesPerPath does not trim (see HistoryConfig.MaxEntriesPerPath's
// doc comment) and RetentionPeriod/CompressValues do not evict or
// compress by themselves (see HistoryConfig.RetentionPeriod's and
// .CompressValues' doc comments) - a long-lived, tracked engine's memory
// use grows without bound unless the caller uses WithMemoryConfig
// directly with MaxTotalVersions, MaxMemoryMB, and CleanupInterval set.
//
// Known, permanent gaps (not fixable by a veneer over the existing
// recording sites):
//
//   - List-element mutations are never recorded. merger computes
//     list-element paths but only ever calls RecordMergeChange for map
//     keys; AllPaths/ChangedPaths/Timeline/Query/ForPath will never
//     contain a list-index path such as "servers.0.name".
//   - A newly added nested subtree is recorded only at its top-level key,
//     not at every descendant path. Merging {"added": {"nested": {"leaf":
//     v}}} onto a document that lacks "added" records exactly one entry,
//     at path "added", carrying the whole subtree as NewValue;
//     ForPath("added.nested.leaf") returns nil. This differs from
//     overwriting an existing deep leaf, which does record at every
//     level ("a", "a.b", "a.b.c") - performSimpleMergeAtPath
//     (merge_builder_impl.go) only recurses into mergeValuesAtPath, and
//     so only records a per-key entry, when overlay's value has a
//     corresponding entry in base; a wholly new key skips that recursion
//     and is recorded once, as a whole-value add, at the point it first
//     appears.
//   - Only PhaseMerge/OpMerge and PhaseEval/OpTransform are ever
//     recorded. PhaseLoad, PhaseManual, PhasePostProcess, OpSet,
//     OpDelete, OpPrune, and OpReplace have no producer - pruning,
//     cherry-picking, and WithPostProcessors post-processors run after
//     evaluation but record nothing.
//   - HistoryEntry.Source and .Line are always zero; see HistoryEntry's
//     doc comment.
type History interface {
	// AllPaths returns every path with at least one recorded entry,
	// sorted lexicographically. Not the same as Document.Paths(): a
	// path can appear here without existing in the final document (if
	// it was later pruned) and Document.Paths() includes paths that
	// were never touched after their initial merge, which AllPaths
	// still reports (a single recorded entry is enough).
	AllPaths() []string

	// ChangedPaths returns paths with more than one recorded entry
	// (i.e. touched by at least two of: an overlay overwrite, and
	// operator evaluation), sorted lexicographically. A path set once
	// and never touched again is in AllPaths but not ChangedPaths.
	ChangedPaths() []string

	// ForPath returns every recorded entry for path, oldest first, with
	// Metadata populated from DocumentMemory's own per-version records
	// (see NodeVersion) and OldValue reconstructed from the previous
	// recorded version rather than the value DocumentMemory.RecordChange
	// itself was called with - see HistoryEntry.OldValue's doc comment
	// for the resulting discrepancy with Timeline()/Query() on a path's
	// first entry. Returns nil if path has no recorded history -
	// including a path that resolves in the final document but was
	// never independently recorded (see the History doc comment's gap
	// list).
	ForPath(path string) []HistoryEntry

	// Query returns entries matching filter, applying HistoryFilter's
	// fields exactly as DocumentMemory.Query does. Path matches on exact
	// equality, PathMatches wildcards (*, **, [0], [*], [key=value]), or
	// a segment-aware path prefix - Path: "db" matches "db.host" but not
	// "dbextra". Prefix matching applies to literal paths only; a
	// wildcard pattern matches at exactly its own depth ("db.*" matches
	// "db.host" but not "db.host.port" - use "db.**" for any depth).
	// Limit caps the earliest N matches.
	Query(filter HistoryFilter) []HistoryEntry

	// Timeline returns every recorded entry across every path, in
	// recording order.
	Timeline() []HistoryEntry

	// TimelineAfter returns entries recorded strictly after t.
	TimelineAfter(t time.Time) []HistoryEntry

	// TimelineBefore returns entries recorded strictly before t.
	TimelineBefore(t time.Time) []HistoryEntry

	// ToJSON serializes Timeline() plus a summary block (total entry
	// count, count of paths with more than one entry, and a per-phase
	// entry count) to JSON.
	ToJSON() ([]byte, error)

	// ToYAML serializes the same document ToJSON does, as YAML.
	ToYAML() ([]byte, error)
}

// HistoryConfig is a smaller, documented-field-only view onto
// MemoryConfig (document_memory.go), for callers who want history
// tracking without MemoryConfig's full 11-field surface. WithMemoryConfig
// remains available directly for anything HistoryConfig does not expose.
type HistoryConfig struct {
	// MaxEntriesPerPath maps directly to MemoryConfig.MaxVersionsPerNode:
	// once a path has recorded this many versions, DocumentMemory drops
	// its oldest versions to stay at the cap on every further
	// RecordChange call. 0 (the zero value) means unlimited, matching
	// MemoryConfig's own default.
	//
	// This caps ONLY NodeHistory.Versions, the per-path storage
	// History.ForPath reads. DocumentMemory's timeline - the storage
	// History.Timeline, History.Query, History.AllPaths, and
	// History.ChangedPaths all read - is never trimmed by this field (or
	// by anything except DocumentMemory.Clear): a path capped at
	// ForPath's count 1 can still show a larger count from ChangedPaths,
	// and Timeline/Query keep growing with every recorded change
	// regardless of this setting. MaxEntriesPerPath does not bound
	// engine-wide memory; use WithMemoryConfig's MaxTotalVersions,
	// MaxMemoryMB, and CleanupInterval directly for a real bound (see the
	// "Engine-Wide Scope" note on the History interface's doc comment).
	MaxEntriesPerPath int

	// RetentionPeriod maps to MemoryConfig.CompressAfter only - the age
	// threshold compression uses once compression runs. It deliberately
	// does NOT set MemoryConfig.CleanupInterval, because doing so would
	// start a background goroutine ticker (DocumentMemory.
	// startBackgroundCleanup) for the lifetime of the process from a
	// single engine option, with no way to stop it short of a type
	// assertion to *DocumentMemory. Without CleanupInterval (or a
	// MaxTotalVersions/MaxMemoryMB limit, neither of which
	// HistoryConfig exposes), DocumentMemory never calls performCleanup
	// on its own, so compression never actually runs and old entries
	// are never evicted by age - RetentionPeriod has no observable
	// effect in this release. Use WithMemoryConfig directly, with
	// CleanupInterval set deliberately, for real age-based cleanup.
	RetentionPeriod time.Duration

	// CompressValues maps directly to MemoryConfig.EnableCompression -
	// but, like RetentionPeriod above, has no observable effect through
	// WithHistoryConfig in this release: EnableCompression only takes
	// effect inside DocumentMemory.performCleanup, and performCleanup
	// only ever runs from shouldRunCleanup (gated on MaxTotalVersions or
	// MaxMemoryMB, neither exposed by HistoryConfig) or the background
	// cleanup ticker (gated on CleanupInterval, which RetentionPeriod
	// deliberately does not set - see its own doc comment). Setting
	// CompressValues through WithHistoryConfig alone compresses nothing.
	// Use WithMemoryConfig directly, with MaxTotalVersions/MaxMemoryMB/
	// CleanupInterval set deliberately, for compression that actually
	// runs.
	CompressValues bool
}

// WithHistoryTracking is a discoverable wrapper over
// EngineOptions.EnableMemoryTracking, which existed before this release
// with no dedicated option function. enabled=true causes
// createEngineFromOptions to call DefaultEngine.EnableMemoryTracking()
// at construction, using any MemoryConfig separately supplied via
// WithMemoryConfig/WithHistoryConfig, or a zero-value MemoryConfig (no
// per-path cap, no cleanup ticker) otherwise.
//
// enabled=false is a genuine no-op, not a way to turn tracking back off:
// createEngineFromOptions only ever calls EnableMemoryTracking when the
// flag is true, never DisableMemoryTracking. If a WithMemoryConfig or
// WithHistoryConfig call earlier or later in the same NewEngine(...) call
// already set MemoryConfig.Enabled = true, WithHistoryTracking(false)
// does not undo it - options.md documents each option as an independent
// field write, and this one is no exception. Call
// engine.(*DefaultEngine).DisableMemoryTracking() after construction to
// turn tracking off once it is on.
func WithHistoryTracking(enabled bool) EngineOption {
	return func(opts *EngineOptions) {
		opts.EnableMemoryTracking = enabled
	}
}

// WithHistoryConfig enables history tracking using HistoryConfig's
// smaller field set, translated onto MemoryConfig per HistoryConfig's own
// field docs. Equivalent to WithMemoryConfig(cfg) with cfg built from
// config, plus WithHistoryTracking(true).
func WithHistoryConfig(config HistoryConfig) EngineOption {
	return func(opts *EngineOptions) {
		cfg := MemoryConfig{
			Enabled:            true,
			MaxVersionsPerNode: config.MaxEntriesPerPath,
			CompressAfter:      config.RetentionPeriod,
			EnableCompression:  config.CompressValues,
		}
		opts.MemoryConfig = &cfg
		opts.EnableMemoryTracking = true
	}
}

// historyImpl is the concrete History for a document produced by a
// tracked merge: a thin, live veneer over dm, never a copy of its state.
// dm is never nil for a *historyImpl - the nil/untracked case is
// represented by emptyHistory instead (see document.go's History
// method), so no method here needs to guard against a nil dm.
type historyImpl struct {
	dm *DocumentMemory
}

// newHistoryFromMemory returns a History backed by dm. dm must not be
// nil; callers (attachHistory in merge_builder_impl.go) only call this
// after confirming dm != nil && dm.IsEnabled().
func newHistoryFromMemory(dm *DocumentMemory) *historyImpl {
	return &historyImpl{dm: dm}
}

func (h *historyImpl) AllPaths() []string {
	seen := make(map[string]struct{})
	for _, e := range h.dm.GetTimeline() {
		seen[e.Path] = struct{}{}
	}
	paths := make([]string, 0, len(seen))
	for p := range seen {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths
}

func (h *historyImpl) ChangedPaths() []string {
	counts := make(map[string]int)
	for _, e := range h.dm.GetTimeline() {
		counts[e.Path]++
	}
	paths := make([]string, 0)
	for p, c := range counts {
		if c > 1 {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	return paths
}

func (h *historyImpl) ForPath(path string) []HistoryEntry {
	nh, err := h.dm.GetHistory(path)
	if err != nil {
		return nil
	}

	entries := make([]HistoryEntry, 0, len(nh.Versions))
	for i, v := range nh.Versions {
		entries = append(entries, nodeVersionToEntry(nh, i, path, v))
	}
	return entries
}

func (h *historyImpl) Query(filter HistoryFilter) []HistoryEntry {
	return changeEventsToEntries(h.dm.Query(filter))
}

func (h *historyImpl) Timeline() []HistoryEntry {
	return changeEventsToEntries(h.dm.GetTimeline())
}

func (h *historyImpl) TimelineAfter(t time.Time) []HistoryEntry {
	events := h.dm.GetTimeline()
	filtered := make([]ChangeEvent, 0, len(events))
	for _, e := range events {
		if e.Timestamp.After(t) {
			filtered = append(filtered, e)
		}
	}
	return changeEventsToEntries(filtered)
}

func (h *historyImpl) TimelineBefore(t time.Time) []HistoryEntry {
	events := h.dm.GetTimeline()
	filtered := make([]ChangeEvent, 0, len(events))
	for _, e := range events {
		if e.Timestamp.Before(t) {
			filtered = append(filtered, e)
		}
	}
	return changeEventsToEntries(filtered)
}

func (h *historyImpl) ToJSON() ([]byte, error) {
	return json.Marshal(buildHistoryDocumentDTO(h.Timeline()))
}

func (h *historyImpl) ToYAML() ([]byte, error) {
	return yaml.Marshal(buildHistoryDocumentDTO(h.Timeline()))
}

// emptyHistory is the History Document.History() returns when no tracked
// DocumentMemory is attached: a document produced by a merge where
// tracking was never enabled, or any Document implementation other than
// *document (every goPatchDocument, whose embedded *document.history
// field is always nil - see gopatch_document.go). Every method returns
// an empty, non-nil result, so callers never need to nil-check the
// History interface itself.
type emptyHistory struct{}

func (emptyHistory) AllPaths() []string                     { return nil }
func (emptyHistory) ChangedPaths() []string                 { return nil }
func (emptyHistory) ForPath(string) []HistoryEntry          { return nil }
func (emptyHistory) Query(HistoryFilter) []HistoryEntry     { return nil }
func (emptyHistory) Timeline() []HistoryEntry               { return nil }
func (emptyHistory) TimelineAfter(time.Time) []HistoryEntry { return nil }
func (emptyHistory) TimelineBefore(time.Time) []HistoryEntry {
	return nil
}

func (emptyHistory) ToJSON() ([]byte, error) {
	return json.Marshal(buildHistoryDocumentDTO(nil))
}

func (emptyHistory) ToYAML() ([]byte, error) {
	return yaml.Marshal(buildHistoryDocumentDTO(nil))
}

// nodeVersionToEntry converts one NodeVersion from nh (DocumentMemory's
// per-path history for path) into a HistoryEntry, reconstructing OldValue
// from the previous version the same way DocumentMemory.Compare does.
func nodeVersionToEntry(nh *NodeHistory, idx int, path string, v NodeVersion) HistoryEntry {
	var oldValue interface{}
	if v.PrevVersion != nil {
		if prevIdx, ok := nh.VersionIndex[*v.PrevVersion]; ok {
			oldValue = nh.Versions[prevIdx].Value
		}
	}

	return HistoryEntry{
		Index:     idx,
		Path:      path,
		Version:   v.Version,
		Timestamp: v.Timestamp,
		Phase:     v.Phase,
		Operation: v.Operation,
		OldValue:  oldValue,
		NewValue:  v.Value,
		Operator:  v.Source,
		Evaluated: v.Phase == PhaseEval,
		Metadata:  v.Metadata,
	}
}

// changeEventsToEntries converts a []ChangeEvent (Timeline/Query results)
// into []HistoryEntry, Index set to each event's position in events.
func changeEventsToEntries(events []ChangeEvent) []HistoryEntry {
	entries := make([]HistoryEntry, 0, len(events))
	for i, e := range events {
		entries = append(entries, HistoryEntry{
			Index:     i,
			Path:      e.Path,
			Version:   e.Version,
			Timestamp: e.Timestamp,
			Phase:     e.Phase,
			Operation: e.Operation,
			OldValue:  e.OldValue,
			NewValue:  e.NewValue,
			Operator:  e.Source,
			Evaluated: e.Phase == PhaseEval,
		})
	}
	return entries
}

// historyEntryDTO is HistoryEntry's ToJSON/ToYAML wire shape: Phase and
// Operation render as their lowercased String() form ("merge", "eval",
// "transform", ...) instead of a bare int, and zero-valued optional
// fields are omitted rather than serialized as "", 0, or null.
type historyEntryDTO struct {
	Index     int                    `json:"index" yaml:"index"`
	Path      string                 `json:"path" yaml:"path"`
	Version   int                    `json:"version" yaml:"version"`
	Timestamp time.Time              `json:"timestamp" yaml:"timestamp"`
	Phase     string                 `json:"phase" yaml:"phase"`
	Operation string                 `json:"operation" yaml:"operation"`
	OldValue  interface{}            `json:"old_value,omitempty" yaml:"old_value,omitempty"`
	NewValue  interface{}            `json:"new_value,omitempty" yaml:"new_value,omitempty"`
	Source    string                 `json:"source,omitempty" yaml:"source,omitempty"`
	Line      int                    `json:"line,omitempty" yaml:"line,omitempty"`
	Operator  string                 `json:"operator,omitempty" yaml:"operator,omitempty"`
	Evaluated bool                   `json:"evaluated" yaml:"evaluated"`
	Metadata  map[string]interface{} `json:"metadata,omitempty" yaml:"metadata,omitempty"`
}

// historyDocumentDTO is the full ToJSON/ToYAML output shape: the entry
// list plus a summary block.
type historyDocumentDTO struct {
	Entries []historyEntryDTO `json:"entries" yaml:"entries"`
	Summary historySummaryDTO `json:"summary" yaml:"summary"`
}

// historySummaryDTO.ChangedPaths is a count (paths with more than one
// entry among Entries), not a path list - History.ChangedPaths() returns
// the list form; this field answers "how many", matching what an audit
// report's summary line needs without repeating the full path list a
// second time.
type historySummaryDTO struct {
	TotalEntries int            `json:"total_entries" yaml:"total_entries"`
	ChangedPaths int            `json:"changed_paths" yaml:"changed_paths"`
	ByPhase      map[string]int `json:"by_phase" yaml:"by_phase"`
}

func buildHistoryDocumentDTO(entries []HistoryEntry) historyDocumentDTO {
	dtoEntries := make([]historyEntryDTO, 0, len(entries))
	byPhase := make(map[string]int)
	pathCounts := make(map[string]int)

	for i := range entries {
		e := &entries[i]
		phase := strings.ToLower(e.Phase.String())
		dtoEntries = append(dtoEntries, historyEntryDTO{
			Index:     e.Index,
			Path:      e.Path,
			Version:   e.Version,
			Timestamp: e.Timestamp,
			Phase:     phase,
			Operation: strings.ToLower(e.Operation.String()),
			OldValue:  e.OldValue,
			NewValue:  e.NewValue,
			Source:    e.Source,
			Line:      e.Line,
			Operator:  e.Operator,
			Evaluated: e.Evaluated,
			Metadata:  e.Metadata,
		})
		byPhase[phase]++
		pathCounts[e.Path]++
	}

	changedPaths := 0
	for _, c := range pathCounts {
		if c > 1 {
			changedPaths++
		}
	}

	return historyDocumentDTO{
		Entries: dtoEntries,
		Summary: historySummaryDTO{
			TotalEntries: len(entries),
			ChangedPaths: changedPaths,
			ByPhase:      byPhase,
		},
	}
}
