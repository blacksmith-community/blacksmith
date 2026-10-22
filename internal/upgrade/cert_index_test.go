//nolint:testpackage // the tests use the unexported request index
package upgrade

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

// The request index finds a task by its client request ID until the entry
// is deleted.
func TestRequestIndexPutLookupDelete(t *testing.T) {
	t.Parallel()

	index := newRequestIndex()

	if _, _, ok := index.Lookup(certRequestID); ok {
		t.Fatalf("expected an empty index to find nothing")
	}

	index.Put(certRequestID, certTaskID, certRequestDigest)

	taskID, digest, ok := index.Lookup(certRequestID)
	if !ok || taskID != certTaskID || digest != certRequestDigest {
		t.Fatalf("expected the entry back, got %q, %q, %v", taskID, digest, ok)
	}

	index.Delete(certRequestID)

	if _, _, ok := index.Lookup(certRequestID); ok {
		t.Fatalf("expected the deleted entry to be gone")
	}
}

// An empty request ID is never indexed, so a request without one never
// matches another.
func TestRequestIndexIgnoresEmptyID(t *testing.T) {
	t.Parallel()

	index := newRequestIndex()
	index.Put("", certTaskID, certRequestDigest)

	if _, _, ok := index.Lookup(""); ok {
		t.Fatalf("expected an empty request ID to stay unindexed")
	}
}

// The digest is the lowercase hex SHA-256 of the request's canonical JSON
// without client_request_id, so the key order and the request ID do not
// change it.
func TestRequestDigestIgnoresRequestIDAndKeyOrder(t *testing.T) {
	t.Parallel()

	first, err := requestDigest(map[string]any{
		"client_request_id": certRequestID,
		"mode":              "redeploy",
		"preview_id":        "p1",
	})
	if err != nil {
		t.Fatalf("digesting the first request: %v", err)
	}

	second, err := requestDigest(struct {
		PreviewID       string `json:"preview_id"`
		Mode            string `json:"mode"`
		ClientRequestID string `json:"client_request_id"`
	}{PreviewID: "p1", Mode: "redeploy", ClientRequestID: "another-id"})
	if err != nil {
		t.Fatalf("digesting the second request: %v", err)
	}

	sum := sha256.Sum256([]byte(`{"mode":"redeploy","preview_id":"p1"}`))
	want := hex.EncodeToString(sum[:])

	if first != want || second != want {
		t.Fatalf("expected both digests to be %s, got %s and %s", want, first, second)
	}

	changed, err := requestDigest(map[string]any{"mode": "patch", "preview_id": "p1"})
	if err != nil {
		t.Fatalf("digesting the changed request: %v", err)
	}

	if changed == want {
		t.Fatalf("expected a different body to give a different digest")
	}
}
