package natsbackend

import (
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/fivetwenty-io/graft/internal/backends/reqdedup"
)

// kvFetchGroup and objFetchGroup coalesce concurrent cache-miss fetches for
// the same target-namespaced key into a single underlying call: N
// references to the same NATS KV/Object path within one merge produce one
// backend request.
var (
	kvFetchGroup  reqdedup.Group[interface{}]
	objFetchGroup reqdedup.Group[interface{}]
)

// FetchFromKVCachedWith checks the shared TTL cache for (target,
// storePath), and on a miss calls fetch (coalescing concurrent identical
// requests via kvFetchGroup) and caches the result. The cache key includes
// target so the same storePath on two different NATS clusters/targets
// never collides - see FetchFromKVCached for the production entry point
// that wires fetch to a real JetStream call, and
// TestFetchFromKVCached_TargetNamespaced for the collision this replaces
// (FetchFromKV's own internal cache, since removed, keyed only by
// storePath with no target component).
//
// auditLogging, when true, logs an "AUDIT: Accessing" line before the
// cache check - a hit is still an access, so it is audited exactly like a
// miss. FetchFromKV no longer logs its own "Accessing" line (only
// "Successfully retrieved"/"Failed to retrieve", still emitted there on
// the miss path only), so this is the single place that line is emitted
// from, covering both outcomes without doubling the miss case.
// skipCache (the ":nocache" expression modifier) bypasses the cache read,
// the request coalescing, AND the cache write: a nocache fetch must
// neither be served from nor poison/refresh the shared entry. The audit
// line is still emitted - a nocache access is still an access.
func FetchFromKVCachedWith(target, storePath string, ttl time.Duration, auditLogging, skipCache bool, fetch func() (interface{}, error)) (interface{}, error) {
	if auditLogging {
		debugLogf("AUDIT: Accessing KV store: %s", storePath)
	}

	if skipCache {
		return fetch()
	}

	startTime := time.Now()
	key := "kv:" + target + ":" + storePath
	if val, ok := Cache.Get(key); ok {
		GlobalMetrics.RecordOperation("kv", time.Since(startTime), false, true)
		return val, nil
	}

	val, err := kvFetchGroup.Do(key, fetch)
	if err != nil {
		return nil, err
	}

	Cache.Set(key, val, ttl)
	return val, nil
}

// FetchFromObjectCachedWith mirrors FetchFromKVCachedWith for the
// object-store cache namespace, skipCache semantics included.
func FetchFromObjectCachedWith(target, storePath string, ttl time.Duration, auditLogging, skipCache bool, fetch func() (interface{}, error)) (interface{}, error) {
	if auditLogging {
		debugLogf("AUDIT: Accessing Object store: %s", storePath)
	}

	if skipCache {
		return fetch()
	}

	startTime := time.Now()
	key := StoreObj + ":" + target + ":" + storePath
	if val, ok := Cache.Get(key); ok {
		GlobalMetrics.RecordOperation(StoreObj, time.Since(startTime), false, true)
		return val, nil
	}

	val, err := objFetchGroup.Do(key, fetch)
	if err != nil {
		return nil, err
	}

	Cache.Set(key, val, ttl)
	return val, nil
}

// FetchFromKVCached is the production entry point: target-namespaced,
// deduped caching in front of FetchFromKV's real JetStream KV read.
// skipCache bypasses the cache entirely (see FetchFromKVCachedWith).
func FetchFromKVCached(target string, js jetstream.JetStream, storePath string, config *Config, skipCache bool) (interface{}, error) {
	return FetchFromKVCachedWith(target, storePath, config.CacheTTL, config.AuditLogging, skipCache, func() (interface{}, error) {
		return FetchFromKV(js, storePath, config)
	})
}

// FetchFromObjectCached is the production entry point: target-namespaced,
// deduped caching in front of FetchFromObject's real JetStream Object read.
// skipCache bypasses the cache entirely (see FetchFromKVCachedWith).
func FetchFromObjectCached(target string, js jetstream.JetStream, storePath string, config *Config, skipCache bool) (interface{}, error) {
	return FetchFromObjectCachedWith(target, storePath, config.CacheTTL, config.AuditLogging, skipCache, func() (interface{}, error) {
		return FetchFromObject(js, storePath, config)
	})
}
