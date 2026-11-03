package graft

// GetEngine returns the engine from an evaluator.
// If no engine is set, a default engine is created for backward compatibility.
func GetEngine(ev *Evaluator) Engine {
	if ev.engine != nil {
		return ev.engine
	}
	// Return a default engine for backward compatibility
	engine, _ := CreateDefaultEngine()
	return engine
}

// EngineOf returns the engine bound to ev, or nil if none is set. Unlike
// GetEngine, it never constructs a fallback default engine.
//
// Use this instead of GetEngine on any path that may run many times over
// the course of a single evaluation (per-nested-operator-call resolution,
// per-dependency-scan lookups): a discarded default engine's cache carries
// an unstoppable background cleanup goroutine (internal/cache's
// cleanupLoop, started by NewShardedCache and never stopped once the
// engine holding it is dropped), so materializing one per lookup leaks a
// goroutine per lookup. OperatorForEngine and ParseOpcallForEngine already
// treat a nil engine as "resolve against DefaultRegistry only" — the exact
// behavior a freshly constructed, unconfigured default engine would have
// produced anyway — so a caller that only needs operator resolution loses
// nothing by using EngineOf instead of GetEngine.
func EngineOf(ev *Evaluator) Engine {
	return ev.engine
}
