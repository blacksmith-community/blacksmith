package aws

import (
	"github.com/fivetwenty-io/graft/internal/backends/reqdedup"
)

// secretFetchGroup and paramFetchGroup coalesce concurrent GetOrFetch*
// calls for the same (target, key) into one underlying fetch. Package-
// level: the cache maps they front are themselves package-level
// (DefaultPool), and coalescing only needs to be scoped to the key string,
// which already carries the target.
var (
	secretFetchGroup reqdedup.Group[string]
	paramFetchGroup  reqdedup.Group[string]
)

func cacheKey(target, name string) string {
	return target + "\x00" + name
}

// GetOrFetchSecret returns the cached secret value for (target, secret) if
// present, otherwise calls fetch to obtain and cache it. Concurrent callers
// for the same (target, secret) are coalesced onto a single fetch via
// secretFetchGroup, so N references to the same secret within one merge
// produce one backend request. A failed fetch is never cached, so a later
// call retries rather than replaying the error. The cache map is never
// exposed to callers - all access goes through acp.mu - unlike the
// previous GetSecretCache/SetSecretCache pair, which returned the raw
// per-target map and let callers read it outside any lock.
func (acp *ClientPool) GetOrFetchSecret(target, secret string, fetch func() (string, error)) (string, error) {
	if val, ok := acp.lookupSecret(target, secret); ok {
		return val, nil
	}

	val, err := secretFetchGroup.Do(cacheKey(target, secret), fetch)
	if err != nil {
		return "", err
	}

	acp.storeSecret(target, secret, val)
	return val, nil
}

// GetOrFetchParam mirrors GetOrFetchSecret for the parameter-store cache
// namespace.
func (acp *ClientPool) GetOrFetchParam(target, param string, fetch func() (string, error)) (string, error) {
	if val, ok := acp.lookupParam(target, param); ok {
		return val, nil
	}

	val, err := paramFetchGroup.Do(cacheKey(target, param), fetch)
	if err != nil {
		return "", err
	}

	acp.storeParam(target, param, val)
	return val, nil
}

func (acp *ClientPool) lookupSecret(target, secret string) (string, bool) {
	acp.mu.RLock()
	defer acp.mu.RUnlock()
	val, ok := acp.secretsCache[target][secret]
	return val, ok
}

func (acp *ClientPool) storeSecret(target, secret, value string) {
	acp.mu.Lock()
	defer acp.mu.Unlock()
	if acp.secretsCache[target] == nil {
		acp.secretsCache[target] = make(map[string]string)
	}
	acp.secretsCache[target][secret] = value
}

func (acp *ClientPool) lookupParam(target, param string) (string, bool) {
	acp.mu.RLock()
	defer acp.mu.RUnlock()
	val, ok := acp.paramsCache[target][param]
	return val, ok
}

func (acp *ClientPool) storeParam(target, param, value string) {
	acp.mu.Lock()
	defer acp.mu.Unlock()
	if acp.paramsCache[target] == nil {
		acp.paramsCache[target] = make(map[string]string)
	}
	acp.paramsCache[target][param] = value
}
