package operators

// Helpers connecting the parsed ":nocache" modifier to backend cache
// layers. Opcall.Run publishes the running call's flag as ev.NoCache
// (ambient per-call context, set and restored exactly like ev.Target), so
// a caching operator needs no Operator interface change to honor it.

// ShouldSkipCache reports whether the operator call currently running was
// written with the ":nocache" modifier (e.g. "(( vault:nocache ... ))").
// Caching operators call this before both the cache read AND the cache
// write: a nocache fetch must neither be served from nor refresh the
// shared cache entry.
func ShouldSkipCache(ev *Evaluator) bool {
	return ev != nil && ev.NoCache
}
