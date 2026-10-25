package upgrade

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
)

// certificateTasksVaultPath is the index of the certificate tasks, and
// each task's record is saved under it by the task's ID.
const certificateTasksVaultPath = "certificate-tasks"

// commandTasksVaultPath is the index of the command tasks, and each
// task's record is saved under it by the task's ID.
const commandTasksVaultPath = "command-tasks"

// tasksIndexPath returns the index a task of the kind is saved under.
// Upgrades keep the index they have always had.
func tasksIndexPath(kind TaskKind) string {
	switch kind {
	case TaskKindCertificates:
		return certificateTasksVaultPath
	case TaskKindCommand:
		return commandTasksVaultPath
	case TaskKindUpgrade:
		return upgradeTasksVaultPath
	}

	return upgradeTasksVaultPath
}

// taskRecordPath returns the path of the task's own record.
func taskRecordPath(task *UpgradeTask) string {
	return tasksIndexPath(task.KindOrDefault()) + "/" + task.ID
}

// requestEntry is what the request index holds for one client request ID.
type requestEntry struct {
	taskID string
	digest string
}

// requestIndex maps the client request ID of each certificate task's
// create request to the task and the digest of the request's body. It
// lives in memory and the loader rebuilds it from the saved tasks.
type requestIndex struct {
	mu      sync.Mutex
	entries map[string]requestEntry
}

// newRequestIndex returns an empty request index.
func newRequestIndex() *requestIndex {
	return &requestIndex{entries: make(map[string]requestEntry)}
}

// Lookup returns the task and the digest saved for the request ID.
func (r *requestIndex) Lookup(requestID string) (string, string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	entry, ok := r.entries[requestID]

	return entry.taskID, entry.digest, ok
}

// Put records the task and the digest for the request ID. An empty request
// ID is never recorded, so a request sent without one matches nothing.
func (r *requestIndex) Put(requestID, taskID, digest string) {
	if requestID == "" {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	r.entries[requestID] = requestEntry{taskID: taskID, digest: digest}
}

// Delete removes the request ID's entry.
func (r *requestIndex) Delete(requestID string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.entries, requestID)
}

// requestDigest returns the lowercase hex SHA-256 of the request body's
// canonical JSON without its client_request_id field. The body is encoded,
// decoded into a map, and encoded again, so its keys are sorted and the
// order of its fields never changes the digest.
func requestDigest(body any) (string, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("encoding the request: %w", err)
	}

	var fields map[string]any

	// Numbers stay as written, so a large one never turns into a float.
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()

	err = decoder.Decode(&fields)
	if err != nil {
		return "", fmt.Errorf("decoding the request: %w", err)
	}

	delete(fields, "client_request_id")

	canonical, err := json.Marshal(fields)
	if err != nil {
		return "", fmt.Errorf("encoding the canonical request: %w", err)
	}

	sum := sha256.Sum256(canonical)

	return hex.EncodeToString(sum[:]), nil
}
