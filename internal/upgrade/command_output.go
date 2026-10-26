package upgrade

import (
	"context"
	"fmt"
	"strconv"

	"blacksmith/internal/bosh/ssh"
	"blacksmith/internal/ops/scripts"
)

// commandOutputVaultPath is where the output of command tasks is kept,
// one entry for each node.
const commandOutputVaultPath = "command-output"

// commandOutputSegmentMax is the longest instance ID or group a path takes.
const commandOutputSegmentMax = 128

// errCommandOutputSegment wraps scripts.ErrRefused, so a caller can match
// either one.
var errCommandOutputSegment = fmt.Errorf("%w: a command output path segment is not valid", scripts.ErrRefused)

// CommandOutput is what one node printed, as it is stored in Vault. The
// streams are base64 in JSON, so any bytes survive. Kept is how much of a
// stream was stored and Received is how much the node sent.
type CommandOutput struct {
	Stdout          []byte `json:"stdout"`
	Stderr          []byte `json:"stderr"`
	StdoutKept      int64  `json:"stdout_kept"`
	StdoutReceived  int64  `json:"stdout_received"`
	StderrKept      int64  `json:"stderr_kept"`
	StderrReceived  int64  `json:"stderr_received"`
	StdoutTruncated bool   `json:"stdout_truncated"`
	StderrTruncated bool   `json:"stderr_truncated"`
}

// validCommandOutputSegment accepts ASCII letters, digits, dot, underscore,
// and hyphen, up to 128 bytes, and refuses "." and "..". Nothing a Vault URL
// path treats specially gets through.
func validCommandOutputSegment(segment string) bool {
	if segment == "" || segment == "." || segment == ".." || len(segment) > commandOutputSegmentMax {
		return false
	}

	for i := range len(segment) {
		c := segment[i]

		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '.', c == '_', c == '-':
		default:
			return false
		}
	}

	return true
}

// commandOutputPath checks every segment and returns the entry's path. An
// error names no segment value, since a segment may come from a request.
func commandOutputPath(taskID, instanceID string, vm CommandVMIdentity) (string, error) {
	err := scripts.ValidTaskID(taskID)
	if err != nil {
		return "", fmt.Errorf("%w: the task ID is not a lowercase UUID", errCommandOutputSegment)
	}

	if !validCommandOutputSegment(instanceID) {
		return "", fmt.Errorf("%w: the instance ID", errCommandOutputSegment)
	}

	if !validCommandOutputSegment(vm.Group) {
		return "", fmt.Errorf("%w: the instance group", errCommandOutputSegment)
	}

	if vm.Index < 0 {
		return "", fmt.Errorf("%w: the instance index", errCommandOutputSegment)
	}

	return commandOutputVaultPath + "/" + taskID + "/" + instanceID + "/" + vm.Group + "-" + strconv.Itoa(vm.Index), nil
}

// storeCommandOutput saves what one node printed in a single Vault entry
// and returns its path. The entry exists even when both streams are empty,
// so a node that ran and printed nothing differs from one that never ran.
// Nothing here logs the output, and an error names the path at most.
func (m *Manager) storeCommandOutput(ctx context.Context, taskID, instanceID string, vm CommandVMIdentity, result ssh.Result) (string, error) {
	path, err := commandOutputPath(taskID, instanceID, vm)
	if err != nil {
		return "", err
	}

	entry := CommandOutput{
		Stdout:          nonNilBytes(result.Stdout),
		Stderr:          nonNilBytes(result.Stderr),
		StdoutKept:      int64(len(result.Stdout)),
		StdoutReceived:  result.StdoutReceived,
		StderrKept:      int64(len(result.Stderr)),
		StderrReceived:  result.StderrReceived,
		StdoutTruncated: result.StdoutTruncated,
		StderrTruncated: result.StderrTruncated,
	}

	err = m.vault.Put(ctx, path, entry)
	if err != nil {
		return "", fmt.Errorf("saving the command output at %s: %w", path, err)
	}

	return path, nil
}

// loadCommandOutput reads an entry back. The bool is false when the node
// has no entry.
func (m *Manager) loadCommandOutput(ctx context.Context, path string) (CommandOutput, bool, error) {
	var entry CommandOutput

	found, err := m.vault.Get(ctx, path, &entry)
	if err != nil {
		return CommandOutput{}, false, fmt.Errorf("reading the command output at %s: %w", path, err)
	}

	if !found {
		return CommandOutput{}, false, nil
	}

	return entry, true, nil
}

// nonNilBytes keeps an empty stream as "" in JSON, never null.
func nonNilBytes(b []byte) []byte {
	if b == nil {
		return []byte{}
	}

	return b
}
