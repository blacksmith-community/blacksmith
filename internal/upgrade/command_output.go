package upgrade

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"blacksmith/internal/apierr"
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
// stream was stored and Received is how much the node sent. When Merged is
// true, the command's stderr joined its stdout on the node, so Stdout holds
// both in the order the command wrote them, its counts describe that one
// stream, and Stderr is empty. An entry saved before output was merged has
// no merged field, so it reads back with Merged false and both streams as
// they were saved.
type CommandOutput struct {
	Stdout          []byte `json:"stdout"`
	Stderr          []byte `json:"stderr"`
	StdoutKept      int64  `json:"stdout_kept"`
	StdoutReceived  int64  `json:"stdout_received"`
	StderrKept      int64  `json:"stderr_kept"`
	StderrReceived  int64  `json:"stderr_received"`
	StdoutTruncated bool   `json:"stdout_truncated"`
	StderrTruncated bool   `json:"stderr_truncated"`
	Merged          bool   `json:"merged,omitempty"`
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
// and returns its path. The entry exists even when the stream is empty, so
// a node that ran and printed nothing differs from one that never ran. The
// result's stdout is the merged stream, and its stderr holds only the
// wrapper's own lines, such as the one about a command it could not start,
// which the node's status already explains, so the entry leaves them out.
// Nothing here logs the output, and an error names the path at most.
func (m *Manager) storeCommandOutput(ctx context.Context, taskID, instanceID string, vm CommandVMIdentity, result ssh.Result) (string, error) {
	path, err := commandOutputPath(taskID, instanceID, vm)
	if err != nil {
		return "", err
	}

	entry := CommandOutput{
		Stdout:          nonNilBytes(result.Stdout),
		Stderr:          []byte{},
		StdoutKept:      int64(len(result.Stdout)),
		StdoutReceived:  result.StdoutReceived,
		StdoutTruncated: result.StdoutTruncated,
		Merged:          true,
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

// ReadCommandOutput returns what one node of a command task printed. The vm
// is the node's "<group>-<index>", split on its last hyphen, since a group
// name may hold hyphens. The instance may be empty when only one instance of
// the task has such a node. The path comes from the task's own records and
// never from the request, so a request can't reach another entry.
//
// An unknown task, instance, or node, and a node with no entry, answer 404
// not_found, and a vm that is not a group and an index, or that fits more
// than one instance, answers 400 invalid_request. The caller decides whether
// the read is allowed before it calls.
func (m *Manager) ReadCommandOutput(ctx context.Context, taskID, instanceID, vm string) (CommandOutput, error) {
	group, index, ok := splitCommandVM(vm)
	if !ok {
		return CommandOutput{}, apierr.New(http.StatusBadRequest, apierr.CodeInvalidRequest,
			"vm must be an instance group and an index, such as redis-0")
	}

	path, err := m.commandOutputPathFor(taskID, instanceID, group, index)
	if err != nil {
		return CommandOutput{}, err
	}

	entry, found, err := m.loadCommandOutput(ctx, path)
	if err != nil {
		return CommandOutput{}, apierr.Wrap(http.StatusInternalServerError, apierr.CodeInternal, err)
	}

	if !found {
		return CommandOutput{}, errNoCommandOutput()
	}

	return entry, nil
}

func errNoCommandOutput() error {
	return apierr.New(http.StatusNotFound, apierr.CodeNotFound, "the task has no output for that node")
}

// splitCommandVM splits "<group>-<index>" on the last hyphen.
func splitCommandVM(vm string) (string, int, bool) {
	cut := strings.LastIndex(vm, "-")
	if cut < 1 {
		return "", 0, false
	}

	digits := vm[cut+1:]
	if digits == "" || strings.Trim(digits, "0123456789") != "" {
		return "", 0, false
	}

	index, err := strconv.Atoi(digits)
	if err != nil {
		return "", 0, false
	}

	return vm[:cut], index, true
}

// commandOutputPathFor finds the node in the task's records and returns the
// path of its output entry.
func (m *Manager) commandOutputPathFor(taskID, instanceID, group string, index int) (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	task, exists := m.tasks[taskID]
	if !exists || task.KindOrDefault() != TaskKindCommand {
		return "", errNoCommandOutput()
	}

	type match struct {
		instance string
		vm       CommandVMIdentity
	}

	var matches []match

	for i := range task.Instances {
		inst := &task.Instances[i]
		if inst.Command == nil || (instanceID != "" && inst.InstanceID != instanceID) {
			continue
		}

		for j := range inst.Command.VMs {
			identity := inst.Command.VMs[j].identity()

			if identity.Group == group && identity.Index == index {
				matches = append(matches, match{instance: inst.InstanceID, vm: identity})
			}
		}
	}

	switch len(matches) {
	case 0:
		return "", errNoCommandOutput()
	case 1:
		return commandOutputPath(taskID, matches[0].instance, matches[0].vm)
	default:
		return "", apierr.New(http.StatusBadRequest, apierr.CodeInvalidRequest,
			"more than one instance has that node, so name the instance")
	}
}

// nonNilBytes keeps an empty stream as "" in JSON, never null.
func nonNilBytes(b []byte) []byte {
	if b == nil {
		return []byte{}
	}

	return b
}
