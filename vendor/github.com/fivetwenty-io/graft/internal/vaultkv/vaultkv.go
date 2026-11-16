// Package vaultkv reads secrets from Vault KV mounts of either version.
// It finds the mount that serves a logical path and the KV version that
// mount speaks, caches that per mount, reads a KV v2 secret at its
// <mount>/data/<rest> API path, and unwraps the KV v2 response so the
// caller gets the secret's own map either way.
//
// Both vault read paths use it: pkg/graft's WithVault backend and
// internal/backends/vault's environment-configured reader. It imports
// neither, so neither import direction creates a cycle.
package vaultkv

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	api "github.com/hashicorp/vault/api"

	"github.com/fivetwenty-io/graft/internal/errtext"
)

// mountLookupPrefix is the per-path mount lookup. Any token with a
// capability on a path may read it, unlike sys/mounts, which a token
// scoped to its secret paths usually cannot, so it is the only mount
// endpoint this package asks.
const mountLookupPrefix = "sys/internal/ui/mounts/"

// Reader reads KV secrets through one Vault client. Its mount cache
// belongs to that client alone, so one Vault's mount table never answers
// for another's. A Reader is safe for concurrent use.
type Reader struct {
	client *api.Client
	debugf func(format string, args ...interface{})

	// mu guards mounts, which maps a mount path with its trailing slash
	// to what is known about it. Entries are added lazily and never
	// removed.
	mu     sync.RWMutex
	mounts map[string]mount
}

// mount is one cached mount table entry.
type mount struct {
	// version is the KV version, 1 or 2.
	version int
	// detected is false when the lookup failed and version is the KV v1
	// fallback.
	detected bool
}

// New returns a Reader over client. debugf receives trace messages and
// may be nil.
func New(client *api.Client, debugf func(format string, args ...interface{})) *Reader {
	if debugf == nil {
		debugf = func(string, ...interface{}) {}
	}
	return &Reader{client: client, debugf: debugf, mounts: make(map[string]mount)}
}

// Client returns the Vault client the Reader reads through.
func (r *Reader) Client() *api.Client { return r.client }

// Result describes one read.
type Result struct {
	// Data is the secret's map, unwrapped from a KV v2 response, or nil
	// when the secret does not exist.
	Data map[string]interface{}
	// Mount is the mount path, with its trailing slash, that the read
	// was resolved against.
	Mount string
	// Detected is false when the mount's KV version could not be looked
	// up and the read assumed KV v1.
	Detected bool
	// KV2 reports that the response had the KV v2 shape.
	KV2 bool
}

// NotFound returns err, the caller's own not-found error for this read,
// wrapped in an *UndetectedError when the mount's KV version was never
// detected, so the error says the secret was read as KV v1.
func (res Result) NotFound(err error) error {
	if res.Detected || err == nil {
		return err
	}
	return &UndetectedError{Mount: res.Mount, Err: err}
}

// Read reads the secret at the logical path, such as "secret/app/db".
// A leading slash is tolerated. On a KV v2 mount the read goes to
// <mount>data/<rest>, unless the path already spells out data/ after
// the mount. A secret that does not exist gives a Result with nil Data
// and a nil error; the error returned is the Vault client's own.
func (r *Reader) Read(ctx context.Context, path string) (Result, error) {
	logical := strings.TrimLeft(path, "/")
	mountPath, m := r.mountFor(ctx, logical)
	res := Result{Mount: mountPath, Detected: m.detected}

	secret, err := r.client.Logical().ReadWithContext(ctx, apiPath(logical, mountPath, m.version))
	if err != nil {
		return res, err
	}
	if secret == nil || secret.Data == nil {
		return res, nil
	}

	res.Data, res.KV2 = Unwrap(secret.Data)
	return res, nil
}

// Unwrap returns the secret map inside a read response's data, and
// whether the response had the KV v2 shape: a "data" map alongside a
// "metadata" key. Any other data is a KV v1 secret and is returned as
// it is.
func Unwrap(data map[string]interface{}) (map[string]interface{}, bool) {
	if inner, ok := data["data"].(map[string]interface{}); ok {
		if _, hasMetadata := data["metadata"]; hasMetadata {
			return inner, true
		}
	}
	return data, false
}

// apiPath maps a logical path to the API path to read: unchanged on a
// KV v1 mount, and <mount>data/<rest> on a KV v2 mount unless rest
// already starts with data/.
func apiPath(logical, mountPath string, version int) string {
	if version != 2 {
		return logical
	}
	rest := strings.TrimPrefix(logical, mountPath)
	if strings.HasPrefix(rest, "data/") {
		return logical
	}
	return mountPath + "data/" + rest
}

// mountFor returns the mount serving logical and what is known about
// it, from the cache when a cached mount covers the path and from a
// lookup otherwise. When cached mounts nest, the longest one wins.
// Concurrent first reads under one mount may each look it up; they
// store the same answer.
func (r *Reader) mountFor(ctx context.Context, logical string) (string, mount) {
	r.mu.RLock()
	best, found := "", mount{}
	for mountPath, m := range r.mounts {
		if strings.HasPrefix(logical, mountPath) && len(mountPath) > len(best) {
			best, found = mountPath, m
		}
	}
	r.mu.RUnlock()
	if best != "" {
		return best, found
	}

	mountPath, m, keep := r.lookup(ctx, logical)
	if !keep {
		return mountPath, m
	}

	r.mu.Lock()
	r.mounts[mountPath] = m
	r.mu.Unlock()

	return mountPath, m
}

// lookup asks Vault which mount serves logical and which KV version it
// speaks. Any failure, such as a missing endpoint, a denied request, or
// an unexpected response, falls back to the path's first segment as an
// undetected KV v1 mount, which is how the path was read before
// detection existed. keep reports whether the answer may be cached: a
// lookup Vault answered, or refused with a 4xx, is kept, so a denied
// lookup costs one request per mount. A transport error, a 5xx, or a
// canceled context is not kept, so a long-lived reader looks the mount
// up again instead of reading a KV v2 mount as KV v1 for good.
func (r *Reader) lookup(ctx context.Context, logical string) (string, mount, bool) {
	fallback := logical + "/"
	if idx := strings.Index(logical, "/"); idx >= 0 {
		fallback = logical[:idx+1]
	}

	info, err := r.client.Logical().ReadWithContext(ctx, mountLookupPrefix+logical)
	if err != nil || info == nil || info.Data == nil {
		r.debugf("  mount detection unavailable for `%s' (assuming KV v1): %v", logical, err)
		return fallback, mount{version: 1}, err == nil || isDefinite(err)
	}

	mountPath := fallback
	if p, ok := info.Data["path"].(string); ok && p != "" {
		if !strings.HasSuffix(p, "/") {
			p += "/"
		}
		mountPath = p
	}

	version := 1
	if options, ok := info.Data["options"].(map[string]interface{}); ok {
		if v, ok := options["version"].(string); ok && v == "2" {
			version = 2
		}
	}

	r.debugf("  mount `%s' speaks KV v%d", mountPath, version)
	return mountPath, mount{version: version, detected: true}, true
}

// isDefinite reports whether err is Vault refusing the lookup with a 4xx,
// an answer that will not change on retry, rather than a failure to get
// an answer at all.
func isDefinite(err error) bool {
	var respErr *api.ResponseError
	if !errors.As(err, &respErr) {
		return false
	}
	return respErr.StatusCode >= 400 && respErr.StatusCode < 500
}

// UndetectedClause is the text an *UndetectedError puts ahead of the
// error it wraps.
const UndetectedClause = "KV version undetected, read as KV v1"

// UndetectedError wraps a not-found error from a mount whose KV version
// could not be looked up, so the secret was read as KV v1. Error() is
// "KV version undetected, read as KV v1: <err>". The detailed text puts
// the quoted mount first, as `mount "secret/": ...`, since the mount
// comes from the document. Neither text holds a secret value.
type UndetectedError struct {
	// Mount is the assumed mount path, with its trailing slash.
	Mount string
	// Err is the caller's not-found error.
	Err error
}

func (e *UndetectedError) Error() string {
	return UndetectedClause + ": " + e.Err.Error()
}

// DetailedError implements errtext.Detailer.
func (e *UndetectedError) DetailedError() string {
	return fmt.Sprintf("mount %q: %s: %s", e.Mount, UndetectedClause, errtext.Detail(e.Err))
}

// Unwrap returns Err, so errors.Is and errors.As see the caller's own
// not-found error.
func (e *UndetectedError) Unwrap() error { return e.Err }
