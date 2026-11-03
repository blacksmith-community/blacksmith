package vault

import (
	"context"
	"fmt"
	"strings"
	"sync"

	api "github.com/hashicorp/vault/api"

	"github.com/fivetwenty-io/graft/pkg/graft"
)

// ErrNotFound is returned when a secret path does not exist in Vault.
type ErrNotFound struct {
	Path string
}

func (e *ErrNotFound) Error() string {
	return fmt.Sprintf("secret %s not found", e.Path)
}

// Reader abstracts Vault secret reading for both KV v1 and v2 mounts.
type Reader interface {
	ReadSecret(ctx context.Context, path string) (map[string]interface{}, error)
}

// vaultAPIReader implements Reader using hashicorp/vault/api. It
// resolves each logical path's mount KV version once (cached per mount)
// so KV v2 mounts are read through their data/ API path — the same
// transparent v1/v2 handling spruce gets from vaultkv.
type vaultAPIReader struct {
	client *api.Client

	// mountMu guards mountVersions, which caches "<mount path with
	// trailing slash>" -> KV version (1 or 2). Populated lazily from
	// sys/internal/ui/mounts lookups; concurrent operator evaluation
	// reads secrets in parallel.
	mountMu       sync.RWMutex
	mountVersions map[string]int
}

// NewReader creates a Reader wrapping the given api.Client.
func NewReader(client *api.Client) Reader {
	return &vaultAPIReader{
		client:        client,
		mountVersions: make(map[string]int),
	}
}

// ReadSecret reads a secret from the given logical path, handling both KV
// v1 and v2 mounts transparently. A KV v2 mount's read path is rewritten
// from <mount>/<rest> to <mount>/data/<rest>; a leading slash on the
// logical path is tolerated (spruce accepts "/secret/foo"). Any returned
// error carries the caller's original path, never the rewritten one.
func (r *vaultAPIReader) ReadSecret(ctx context.Context, path string) (map[string]interface{}, error) {
	graft.DEBUG("vault/api: reading secret at `%s'", path)

	readPath := r.apiPathFor(ctx, strings.TrimLeft(path, "/"))

	secret, err := r.client.Logical().ReadWithContext(ctx, readPath)
	if err != nil {
		graft.DEBUG("  failure: %s", err)
		return nil, err
	}

	if secret == nil || secret.Data == nil {
		graft.DEBUG("  not found (nil response)")
		return nil, &ErrNotFound{Path: path}
	}

	// Detect KV v2 response structure: has "data" and "metadata" keys
	if innerData, ok := secret.Data["data"].(map[string]interface{}); ok {
		if _, hasMetadata := secret.Data["metadata"]; hasMetadata {
			graft.DEBUG("  success (KV v2)")
			return innerData, nil
		}
	}

	// KV v1 response: data is the secret map directly
	graft.DEBUG("  success (KV v1)")
	return secret.Data, nil
}

// apiPathFor maps a normalized logical secret path to the API path to
// read: unchanged for KV v1 mounts (and for vaults where the mount cannot
// be determined), rewritten to <mount>/data/<rest> for KV v2 mounts. A
// path that already spells out the data/ prefix is left alone so callers
// who wrote explicit v2 API paths keep working.
func (r *vaultAPIReader) apiPathFor(ctx context.Context, path string) string {
	mount, version := r.mountVersionFor(ctx, path)
	if version != 2 {
		return path
	}

	rest := strings.TrimPrefix(path, mount)
	if strings.HasPrefix(rest, "data/") {
		return path
	}
	return mount + "data/" + rest
}

// mountVersionFor returns the mount path (with trailing slash) and KV
// version for the given logical path, caching results per mount. When the
// vault does not expose sys/internal/ui/mounts (pre-0.10, or a policy
// denies it), the path's first segment is cached as KV v1 so the fallback
// probe happens once per mount rather than once per secret.
func (r *vaultAPIReader) mountVersionFor(ctx context.Context, path string) (string, int) {
	r.mountMu.RLock()
	for mount, version := range r.mountVersions {
		if strings.HasPrefix(path, mount) {
			r.mountMu.RUnlock()
			return mount, version
		}
	}
	r.mountMu.RUnlock()

	mount, version := r.lookupMount(ctx, path)

	r.mountMu.Lock()
	r.mountVersions[mount] = version
	r.mountMu.Unlock()

	return mount, version
}

// lookupMount asks the vault which mount serves path and which KV version
// it speaks, via sys/internal/ui/mounts/<path>. Any failure — endpoint
// missing, permission denied, unexpected response shape — degrades to
// treating the path's first segment as a KV v1 mount, which preserves the
// pre-detection read behavior.
func (r *vaultAPIReader) lookupMount(ctx context.Context, path string) (string, int) {
	fallbackMount := path + "/"
	if idx := strings.Index(path, "/"); idx >= 0 {
		fallbackMount = path[:idx+1]
	}

	info, err := r.client.Logical().ReadWithContext(ctx, "sys/internal/ui/mounts/"+path)
	if err != nil || info == nil || info.Data == nil {
		graft.DEBUG("  mount detection unavailable for `%s' (assuming KV v1): %v", path, err)
		return fallbackMount, 1
	}

	mount := fallbackMount
	if mountPath, ok := info.Data["path"].(string); ok && mountPath != "" {
		if !strings.HasSuffix(mountPath, "/") {
			mountPath += "/"
		}
		mount = mountPath
	}

	version := 1
	if options, ok := info.Data["options"].(map[string]interface{}); ok {
		if v, ok := options["version"].(string); ok && v == "2" {
			version = 2
		}
	}

	graft.DEBUG("  mount `%s' speaks KV v%d", mount, version)
	return mount, version
}
