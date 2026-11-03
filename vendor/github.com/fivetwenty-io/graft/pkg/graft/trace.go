package graft

import (
	"fmt"
	"io"

	"github.com/fivetwenty-io/graft/log"
)

// TraceLevel selects how verbose graft's DEBUG/TRACE output is once an
// engine is constructed with WithTraceLevel. The underlying log package
// (github.com/fivetwenty-io/graft/log) only distinguishes two output
// levels today - debug and trace - so TraceLevelError, TraceLevelWarn, and
// TraceLevelInfo are accepted for forward compatibility with a more
// granular logging pipeline but currently behave the same as
// TraceLevelNone: both DEBUG and TRACE output disabled.
type TraceLevel int

const (
	// TraceLevelNone disables both DEBUG and TRACE output.
	TraceLevelNone TraceLevel = iota
	// TraceLevelError is reserved for a future error-only output level.
	// It currently behaves the same as TraceLevelNone.
	TraceLevelError
	// TraceLevelWarn is reserved for a future warn-and-above output level.
	// It currently behaves the same as TraceLevelNone.
	TraceLevelWarn
	// TraceLevelInfo is reserved for a future info-and-above output level.
	// It currently behaves the same as TraceLevelNone.
	TraceLevelInfo
	// TraceLevelDebug enables DEBUG output (log.DebugOn), matching the
	// CLI's -d/--debug flag.
	TraceLevelDebug
	// TraceLevelTrace enables both DEBUG and TRACE output (log.DebugOn and
	// log.TraceOn), matching the CLI's -t/--trace flag.
	TraceLevelTrace
)

// String returns the name of the trace level.
func (l TraceLevel) String() string {
	switch l {
	case TraceLevelNone:
		return "none"
	case TraceLevelError:
		return "error"
	case TraceLevelWarn:
		return "warn"
	case TraceLevelInfo:
		return "info"
	case TraceLevelDebug:
		return "debug"
	case TraceLevelTrace:
		return "trace"
	default:
		return fmt.Sprintf("TraceLevel(%d)", int(l))
	}
}

// WithTraceOutput routes graft's DEBUG/TRACE output to w instead of the
// default os.Stderr. This affects every DEBUG/TRACE call in the process
// (pkg/graft.DEBUG/TRACE and pkg/graft/operators.DEBUG/TRACE both funnel
// into the same github.com/fivetwenty-io/graft/log package), not only
// calls made through this engine: the underlying sink (log.Writer) is a
// package-level variable, since DEBUG/TRACE are themselves package-level
// functions with no per-engine routing today. If a process constructs more
// than one engine with WithTraceOutput, the last one applied (at
// construction or via Configure) wins for the whole process. When
// WithTraceOutput is never used, output goes to os.Stderr exactly as it
// does today.
//
// A nil w is a no-op: it leaves any previously configured output
// destination (or the os.Stderr default) unchanged, rather than disabling
// output.
func WithTraceOutput(w io.Writer) Option {
	return func(opts *EngineOptions) {
		if w != nil {
			opts.TraceOutput = w
		}
	}
}

// WithTraceLevel sets which of graft's DEBUG/TRACE output is produced. See
// TraceLevel for the level semantics and WithTraceOutput for the
// package-level-sink caveat this option shares.
func WithTraceLevel(level TraceLevel) Option {
	return func(opts *EngineOptions) {
		opts.TraceLevel = level
		opts.traceLevelSet = true
	}
}

// applyLogging pushes opts' trace/debug-logging settings onto the
// process-global github.com/fivetwenty-io/graft/log sink that DEBUG/TRACE
// calls read. It is a no-op for any field the caller never set (TraceOutput
// nil, traceLevelSet/debugLoggingSet false), so an engine constructed
// without WithTraceOutput/WithTraceLevel/WithDebugLogging leaves the
// process's logging configuration exactly as it was - in particular, it
// does not clobber a CLI -d/-t flag or another engine's prior
// configuration. When both WithTraceLevel and WithDebugLogging are
// supplied on the same engine, WithTraceLevel wins (it is the more
// expressive of the two mechanisms).
func applyLogging(opts *EngineOptions) {
	if opts.TraceOutput != nil {
		log.Writer = opts.TraceOutput
	}
	switch {
	case opts.traceLevelSet:
		switch opts.TraceLevel {
		case TraceLevelTrace:
			log.DebugOn = true
			log.TraceOn = true
		case TraceLevelDebug:
			log.DebugOn = true
			log.TraceOn = false
		default: // TraceLevelNone, TraceLevelError, TraceLevelWarn, TraceLevelInfo
			log.DebugOn = false
			log.TraceOn = false
		}
	case opts.debugLoggingSet:
		log.DebugOn = opts.DebugLogging
	}
}
