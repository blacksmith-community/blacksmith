// Package reqdedup coalesces concurrent, identical backend requests into a
// single in-flight call: N references to the same secret within one merge
// produce one backend request. It is a thin, typed
// wrapper around golang.org/x/sync/singleflight so backend packages
// (internal/backends/vault, aws, nats) share one well-tested coalescing
// primitive instead of each hand-rolling its own.
//
// Group only coalesces callers that are genuinely concurrent for the same
// key; it is not a cache. Callers combine it with their own TTL cache (or
// nothing) to decide when a key is worth fetching at all - Group answers
// "how many in-flight fetches for this key right now", the cache answers
// "do we already know the answer".
package reqdedup

import "golang.org/x/sync/singleflight"

// Group coalesces concurrent Do calls for the same key so only one fetch
// runs; every caller waiting on that key receives its result (or error).
// The zero value is ready to use.
type Group[T any] struct {
	sf singleflight.Group
}

// Do calls fn for key, unless a call for key is already in flight, in which
// case it waits for the existing call to complete and returns its result.
// Multiple callers sharing the in-flight call all receive the same value
// and error.
func (g *Group[T]) Do(key string, fn func() (T, error)) (T, error) {
	v, err, _ := g.sf.Do(key, func() (interface{}, error) {
		return fn()
	})
	if err != nil {
		var zero T
		return zero, err
	}
	// fn returns T, so the singleflight value is always a T when err is nil.
	res, _ := v.(T)
	return res, nil
}
