package upgrade

import (
	"slices"
	"strconv"
	"time"
)

// The bounds of a command's timeout, in seconds.
const (
	// CommandTimeoutDefault applies when a request sets no timeout.
	CommandTimeoutDefault = 600
	// CommandTimeoutMin is the shortest timeout a request may set.
	CommandTimeoutMin = 10
	// CommandTimeoutMax is the longest timeout a request may set.
	CommandTimeoutMax = 3600
)

// CommandPhase says how far one node's command has got.
type CommandPhase string

// The phases of a node's command.
const (
	// CommandPhasePending means nothing has been done on the node yet.
	CommandPhasePending CommandPhase = "pending"
	// CommandPhaseStaged means the command is staged on the node.
	CommandPhaseStaged CommandPhase = "staged"
	// CommandPhaseSent means the command was sent to the node.
	CommandPhaseSent CommandPhase = "sent"
	// CommandPhaseSettled means the node's outcome is known.
	CommandPhaseSettled CommandPhase = "settled"
	// CommandPhaseSkipped means the command was never sent to the node.
	CommandPhaseSkipped CommandPhase = "skipped"
)

// CommandTaskSpec is what a command task was asked to do, and is set on a
// command task only.
type CommandTaskSpec struct {
	Command             string `json:"command"`
	CommandDigest       string `json:"command_digest"`
	CommandLength       int    `json:"command_length"`
	RunAs               string `json:"run_as"`
	Shell               string `json:"shell,omitempty"`
	PreviewID           string `json:"preview_id,omitempty"`
	TimeoutSeconds      int    `json:"timeout_seconds"`
	Parallel            bool   `json:"parallel"`
	MaxInFlight         int    `json:"max_in_flight,omitempty"`
	Reason              string `json:"reason,omitempty"`
	CredentialPresented bool   `json:"credential_presented,omitempty"`
}

// InstanceCommand is the command record of one service instance, and holds
// a record for each of the instance's nodes.
type InstanceCommand struct {
	Deployment string      `json:"deployment"`
	VMs        []CommandVM `json:"vms"`
}

// CommandVMIdentity names one VM as the director reported it.
type CommandVMIdentity struct {
	Group    string   `json:"group"`
	Index    int      `json:"index"`
	ID       string   `json:"id"`
	CID      string   `json:"cid"`
	AZ       string   `json:"az,omitempty"`
	IPs      []string `json:"ips,omitempty"`
	State    string   `json:"state,omitempty"`
	JobState string   `json:"job_state,omitempty"`
}

// CommandVM is the record of a command on one node. Reviewed is the VM
// the operator saw in the preview, and Matched is the VM the command was
// bound to when it ran.
type CommandVM struct {
	Reviewed        CommandVMIdentity `json:"reviewed"`
	Matched         CommandVMIdentity `json:"matched"`
	AZ              string            `json:"az,omitempty"`
	JobState        string            `json:"job_state,omitempty"`
	Phase           CommandPhase      `json:"phase"`
	Status          string            `json:"status,omitempty"`
	Reason          string            `json:"reason,omitempty"`
	ExitCode        *int              `json:"exit_code,omitempty"`
	StagedAt        *time.Time        `json:"staged_at,omitempty"`
	StartedAt       *time.Time        `json:"started_at,omitempty"`
	EndedAt         *time.Time        `json:"ended_at,omitempty"`
	StdoutKept      int               `json:"stdout_kept,omitempty"`
	StdoutReceived  int               `json:"stdout_received,omitempty"`
	StderrKept      int               `json:"stderr_kept,omitempty"`
	StderrReceived  int               `json:"stderr_received,omitempty"`
	StdoutTruncated bool              `json:"stdout_truncated,omitempty"`
	StderrTruncated bool              `json:"stderr_truncated,omitempty"`
	Merged          bool              `json:"merged,omitempty"`
	Killed          bool              `json:"killed,omitempty"`
	Survived        bool              `json:"survived,omitempty"`
	OutputPath      string            `json:"output_path,omitempty"`
	Error           string            `json:"error,omitempty"`
	// Unproven says why the last cleanup of a sent node that is still held
	// showed nothing about whether the command ended: unprovenUnknown when
	// cleanup could not check, and unprovenGone when the task directory was
	// already gone. It is empty once the node settles, and a later strand
	// for any other reason clears it.
	Unproven string `json:"unproven,omitempty"`
	// InFlight says a worker of this process holds the node, which it may
	// still read pending while the worker lists the VMs, connects, and
	// checks sudo. It is set on the copies a reader gets and never saved.
	InFlight bool `json:"in_flight,omitempty"`

	// claimed says a worker of this process is driving the node. It is
	// never saved, so a node left pending by an earlier run is free to be
	// claimed again.
	claimed bool
}

// CommandSummary is what a task list shows of a command task. It never
// holds the command text.
type CommandSummary struct {
	CommandDigest  string `json:"command_digest"`
	CommandLength  int    `json:"command_length"`
	RunAs          string `json:"run_as"`
	TimeoutSeconds int    `json:"timeout_seconds"`
	Parallel       bool   `json:"parallel"`
	MaxInFlight    int    `json:"max_in_flight,omitempty"`
}

// commandSummary returns the summary of a command task's settings.
func commandSummary(spec *CommandTaskSpec) *CommandSummary {
	if spec == nil {
		return nil
	}

	return &CommandSummary{
		CommandDigest:  spec.CommandDigest,
		CommandLength:  spec.CommandLength,
		RunAs:          spec.RunAs,
		TimeoutSeconds: spec.TimeoutSeconds,
		Parallel:       spec.Parallel,
		MaxInFlight:    spec.MaxInFlight,
	}
}

// commandTarget describes a command task for the task list, such as "Run
// as vcap on 3 nodes".
func commandTarget(task *UpgradeTask) string {
	user := "vcap"
	if task.Command != nil && task.Command.RunAs != "" {
		user = task.Command.RunAs
	}

	nodes := 0

	for i := range task.Instances {
		if record := task.Instances[i].Command; record != nil {
			nodes += len(record.VMs)
		}
	}

	noun := "nodes"
	if nodes == 1 {
		noun = "node"
	}

	return "Run as " + user + " on " + strconv.Itoa(nodes) + " " + noun
}

// clone returns a copy of the spec that shares nothing with it.
func (s *CommandTaskSpec) clone() *CommandTaskSpec {
	if s == nil {
		return nil
	}

	copied := *s

	return &copied
}

// clone returns a copy of the identity that shares nothing with it.
func (i CommandVMIdentity) clone() CommandVMIdentity {
	i.IPs = slices.Clone(i.IPs)

	return i
}

// clone returns a copy of the record that shares nothing with it.
func (c *InstanceCommand) clone() *InstanceCommand {
	if c == nil {
		return nil
	}

	copied := *c
	copied.VMs = slices.Clone(c.VMs)

	for i := range copied.VMs {
		vm := &copied.VMs[i]
		vm.Reviewed = vm.Reviewed.clone()
		vm.Matched = vm.Matched.clone()
		vm.StagedAt = copyTime(vm.StagedAt)
		vm.StartedAt = copyTime(vm.StartedAt)
		vm.EndedAt = copyTime(vm.EndedAt)

		if vm.ExitCode != nil {
			code := *vm.ExitCode
			vm.ExitCode = &code
		}
	}

	return &copied
}

// commandSurvivorOn reports whether a node of the instance has processes
// that outlived KILL.
func commandSurvivorOn(inst *InstanceUpgrade) bool {
	return inst.Command != nil && slices.ContainsFunc(inst.Command.VMs, func(vm CommandVM) bool { return vm.Survived })
}

// heldCommandInstanceIDs returns the IDs of the instances a command task
// holds, as shouldHoldLocked holds them. An unfinished task holds each one
// it has not settled, and a task in any status holds each one with an
// unresolved node, such as a node still to clean or one whose processes
// outlived KILL. A task that holds nothing gets an empty list, never nil,
// so its summary says it holds nothing.
func heldCommandInstanceIDs(task *UpgradeTask) []string {
	held := []string{}
	unfinished := task.Status == TaskStatusPending || task.Status == TaskStatusRunning || task.Status == TaskStatusPaused

	for i := range task.Instances {
		inst := &task.Instances[i]
		if (unfinished && !inst.Status.settled()) || commandInstanceHeldLocked(inst) {
			held = append(held, inst.InstanceID)
		}
	}

	return held
}

// markInFlight marks each node of a reader's copy of a task that a worker
// holds. Only a reader's copy carries the mark, so a saved record never
// holds it.
func markInFlight(task *UpgradeTask) {
	for i := range task.Instances {
		if task.Instances[i].Command == nil {
			continue
		}

		for vm := range task.Instances[i].Command.VMs {
			node := &task.Instances[i].Command.VMs[vm]
			node.InFlight = node.claimed
		}
	}
}
