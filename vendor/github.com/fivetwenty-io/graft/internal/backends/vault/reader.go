package vault

import (
	"context"
	"fmt"

	api "github.com/hashicorp/vault/api"

	"github.com/fivetwenty-io/graft/internal/vaultkv"
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

// vaultAPIReader implements Reader using hashicorp/vault/api, through
// internal/vaultkv, which resolves each logical path's mount KV version
// once (cached per mount) so KV v2 mounts are read through their data/
// API path, the same transparent v1/v2 handling spruce gets from
// vaultkv. pkg/graft's WithVault backend reads through the same package.
type vaultAPIReader struct {
	kv *vaultkv.Reader
}

// NewReader creates a Reader wrapping the given api.Client.
func NewReader(client *api.Client) Reader {
	return &vaultAPIReader{kv: vaultkv.New(client, graft.DEBUG)}
}

// ReadSecret reads a secret from the given logical path, handling both KV
// v1 and v2 mounts transparently. A KV v2 mount's read path is rewritten
// from <mount>/<rest> to <mount>/data/<rest>; a leading slash on the
// logical path is tolerated (spruce accepts "/secret/foo"). Any returned
// error carries the caller's original path, never the rewritten one. A
// not-found error from a mount whose KV version could not be detected is
// wrapped in a *vaultkv.UndetectedError that says so; errors.As still
// finds the *ErrNotFound inside it.
func (r *vaultAPIReader) ReadSecret(ctx context.Context, path string) (map[string]interface{}, error) {
	graft.DEBUG("vault/api: reading secret at `%s'", path)

	res, err := r.kv.Read(ctx, path)
	if err != nil {
		graft.DEBUG("  failure: %s", err)
		return nil, err
	}

	if res.Data == nil {
		graft.DEBUG("  not found (nil response)")
		return nil, res.NotFound(&ErrNotFound{Path: path})
	}

	if res.KV2 {
		graft.DEBUG("  success (KV v2)")
	} else {
		graft.DEBUG("  success (KV v1)")
	}
	return res.Data, nil
}
