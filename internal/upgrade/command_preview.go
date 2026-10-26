package upgrade

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/google/uuid"

	"blacksmith/internal/apierr"
	"blacksmith/internal/bosh"
	"blacksmith/internal/certinventory"
	"blacksmith/internal/config"
	"blacksmith/internal/mutation"
)

const (
	// MaxCommandBytes is the longest command a preview accepts.
	MaxCommandBytes = 64 * 1024
	// commandShell is the shell a command runs in.
	commandShell = "/bin/bash"
	// minParallelWorkers is the fewest director workers that leave room for
	// a parallel run, which keeps one worker free.
	minParallelWorkers = 2
	// noVMReason is why a node without a VM is not targeted.
	noVMReason = "no VM"
	// warningMixedServices is set when the instances come from more than one
	// service.
	warningMixedServices = "mixed_services"
)

// PreviewKind says what a stored preview approves.
type PreviewKind string

// The kinds of preview a PreviewStore holds.
const (
	PreviewKindCertificate PreviewKind = "certificate"
	PreviewKindCommand     PreviewKind = "command"
)

// CommandPreviewRequest is the body of a command preview.
type CommandPreviewRequest struct {
	InstanceIDs    []string `json:"instance_ids"`
	Command        string   `json:"command"`
	RunAs          string   `json:"run_as"`
	TimeoutSeconds int      `json:"timeout_seconds"`
	Parallel       bool     `json:"parallel"`
	MaxInFlight    int      `json:"max_in_flight"`
}

// CommandPreviewVM is one VM row of a command preview.
type CommandPreviewVM struct {
	CommandVMIdentity
	Targeted bool   `json:"targeted"`
	Reason   string `json:"reason,omitempty"`
}

// CommandPreviewInstance is one instance of a command preview.
type CommandPreviewInstance struct {
	InstanceID   string             `json:"instance_id"`
	InstanceName string             `json:"instance_name,omitempty"`
	Deployment   string             `json:"deployment"`
	Service      string             `json:"service"`
	Plan         string             `json:"plan,omitempty"`
	Blocked      *Blocked           `json:"blocked"`
	VMs          []CommandPreviewVM `json:"vms"`
}

// CommandPreview is the answer to a command preview.
type CommandPreview struct {
	ID                string                   `json:"preview_id"`
	ExpiresAt         time.Time                `json:"expires_at"`
	CommandDigest     string                   `json:"command_digest"`
	CommandLength     int                      `json:"command_length"`
	RunAs             string                   `json:"run_as"`
	Shell             string                   `json:"shell"`
	TimeoutSeconds    int                      `json:"timeout_seconds"`
	Parallel          bool                     `json:"parallel"`
	MaxInFlight       int                      `json:"max_in_flight"`
	ParallelAvailable bool                     `json:"parallel_available"`
	WorkerCount       int                      `json:"worker_count"`
	MaxInFlightLimit  int                      `json:"max_in_flight_limit"`
	Services          []string                 `json:"services"`
	Warnings          []string                 `json:"warnings"`
	Instances         []CommandPreviewInstance `json:"instances"`
}

// MatchesCommand says whether the settings are the ones the preview
// approved. The command is compared by its digest and length, since the
// preview never holds the text. A serial preview stores a max_in_flight of 0
// and ignores the one a create sends.
func (p *CommandPreview) MatchesCommand(digest string, length int, runAs string, timeout int, parallel bool, maxInFlight int) bool {
	if !parallel {
		maxInFlight = 0
	}

	return p.CommandDigest == digest &&
		p.CommandLength == length &&
		p.RunAs == runAs &&
		p.TimeoutSeconds == timeout &&
		p.Parallel == parallel &&
		p.MaxInFlight == maxInFlight
}

// commandDigest returns the hex SHA-256 of the command.
func commandDigest(command string) string {
	sum := sha256.Sum256([]byte(command))

	return hex.EncodeToString(sum[:])
}

// DirectorWorkerCount is the default WorkerCount seam. The director's /info
// exposes no worker count (boshapi@v0.283.9/pkg/boshapi/info.go:4-14), so it
// always answers false and the BOSH configuration decides.
func DirectorWorkerCount(context.Context) (int, bool) { return 0, false }

// commandWorkerCount returns how many workers the director has, from the
// WorkerCount seam first and from the BOSH configuration when the seam does
// not know.
func (m *Manager) commandWorkerCount(ctx context.Context) int {
	d := m.certDeps()

	if d.WorkerCount != nil {
		count, known := d.WorkerCount(ctx)
		if known {
			return count
		}
	}

	var cfg config.BOSHConfig
	if d.BOSH != nil {
		cfg = d.BOSH()
	}

	return cfg.DirectorWorkerCount()
}

// validateCommandSettings checks the request and returns the timeout to
// use. It never puts the command text in an error.
func validateCommandSettings(req CommandPreviewRequest) (int, error) {
	if len(req.Command) > MaxCommandBytes {
		return 0, apierr.New(http.StatusBadRequest, apierr.CodeCommandTooLong,
			fmt.Sprintf("the command is %d bytes, and the limit is %d", len(req.Command), MaxCommandBytes))
	}

	if req.RunAs != runAsRoot && req.RunAs != runAsVcap {
		return 0, apierr.New(http.StatusBadRequest, apierr.CodeInvalidRunUser, "commands run as root or vcap")
	}

	timeout := req.TimeoutSeconds
	if timeout == 0 {
		timeout = CommandTimeoutDefault
	}

	if timeout < CommandTimeoutMin || timeout > CommandTimeoutMax {
		return 0, apierr.New(http.StatusBadRequest, apierr.CodeTimeoutOutOfRange,
			fmt.Sprintf("the timeout is between %d and %d seconds", CommandTimeoutMin, CommandTimeoutMax))
	}

	if len(req.InstanceIDs) == 0 {
		return 0, apierr.New(http.StatusBadRequest, apierr.CodeNoSelections, "Select at least one instance to preview.")
	}

	return timeout, nil
}

// PreviewCommand reads each selected instance and its VMs and returns a
// stored preview that binds the nodes and settings the operator reviews. The
// preview holds the digest and length of the command and never its text. An
// instance that cannot take part is listed with Blocked set, and a read that
// fails for any instance fails the whole preview.
func (m *Manager) PreviewCommand(ctx context.Context, req CommandPreviewRequest) (*CommandPreview, error) {
	timeout, err := validateCommandSettings(req)
	if err != nil {
		return nil, err
	}

	d := m.certDeps()
	if d.Previews == nil || m.director == nil {
		return nil, apierr.New(http.StatusInternalServerError, apierr.CodeInternal, "Blacksmith is not set up to preview commands.")
	}

	workers := m.commandWorkerCount(ctx)
	available := workers >= minParallelWorkers

	limit := 0
	if available {
		limit = workers - 1
	}

	maxInFlight := 0

	if req.Parallel {
		if !available || req.MaxInFlight < 1 || req.MaxInFlight > limit {
			return nil, apierr.New(http.StatusBadRequest, apierr.CodeMaxInFlightTooHigh,
				fmt.Sprintf("max_in_flight must be between 1 and %d with %d director workers", limit, workers))
		}

		maxInFlight = req.MaxInFlight
	}

	preview := &CommandPreview{
		ID:                previewIDPrefix + uuid.NewString(),
		CommandDigest:     commandDigest(req.Command),
		CommandLength:     len(req.Command),
		RunAs:             req.RunAs,
		Shell:             commandShell,
		TimeoutSeconds:    timeout,
		Parallel:          req.Parallel,
		MaxInFlight:       maxInFlight,
		ParallelAvailable: available,
		WorkerCount:       workers,
		MaxInFlightLimit:  limit,
		Services:          []string{},
		Warnings:          []string{},
		Instances:         make([]CommandPreviewInstance, 0, len(req.InstanceIDs)),
	}

	seen := make(map[string]bool, len(req.InstanceIDs))
	services := make(map[string]bool)

	for _, id := range req.InstanceIDs {
		if id == "" || seen[id] {
			continue
		}

		seen[id] = true

		inst, err := m.previewCommandInstance(ctx, id)
		if err != nil {
			return nil, err
		}

		if inst.Service != "" {
			services[inst.Service] = true
		}

		preview.Instances = append(preview.Instances, inst)
	}

	for service := range services {
		preview.Services = append(preview.Services, service)
	}

	sort.Strings(preview.Services)

	if len(preview.Services) > 1 {
		preview.Warnings = append(preview.Warnings, warningMixedServices)
	}

	d.Previews.PutCommand(preview, m.previewTTL())

	return preview, nil
}

// previewCommandInstance reads one instance record and its VMs.
func (m *Manager) previewCommandInstance(ctx context.Context, instanceID string) (CommandPreviewInstance, error) {
	inst := CommandPreviewInstance{InstanceID: instanceID, VMs: []CommandPreviewVM{}}

	var record map[string]interface{}

	exists, err := m.vault.Get(ctx, instanceID, &record)
	if err != nil {
		return inst, apierr.New(http.StatusInternalServerError, apierr.CodeInternal,
			fmt.Sprintf("The instance %s could not be read: %v", instanceID, err))
	}

	if !exists {
		inst.Blocked = &Blocked{Code: apierr.CodeInstanceNotFound, Message: "Blacksmith does not know this instance, so it may have been deleted."}

		return inst, nil
	}

	inst.Deployment, _ = record["deployment_name"].(string)
	inst.InstanceName, _ = record["instance_name"].(string)
	inst.Service, _ = record["service_id"].(string)
	inst.Plan, _ = record["plan_id"].(string)

	if inst.Deployment == "" && inst.Plan != "" {
		inst.Deployment = inst.Plan + "-" + instanceID
	}

	if inst.Deployment == "" {
		inst.Blocked = &Blocked{Code: apierr.CodeInstanceNotFound, Message: "The instance record names no deployment."}

		return inst, nil
	}

	busy := m.busyBlock(certinventory.InstanceRef{ID: instanceID, Deployment: inst.Deployment}, "")
	if busy != nil {
		inst.Blocked = &Blocked{Code: busy.Code, Message: mutation.ErrInstanceBusy.Error() + ": " + busy.Message}

		return inst, nil
	}

	vms, err := directorCall(ctx, func(c context.Context) ([]bosh.VM, error) {
		return m.director.GetDeploymentVMs(c, inst.Deployment)
	})

	switch {
	case errors.Is(err, bosh.ErrDeploymentNotFound):
		inst.Blocked = &Blocked{Code: apierr.CodeInstanceNotFound, Message: fmt.Sprintf("The director has no deployment %s, so the instance may have been deleted.", inst.Deployment)}

		return inst, nil
	case err != nil:
		return inst, apierr.New(http.StatusInternalServerError, apierr.CodeInternal,
			fmt.Sprintf("The director did not list the VMs of deployment %s: %v", inst.Deployment, err))
	}

	sort.SliceStable(vms, func(i, j int) bool {
		if vms[i].Job != vms[j].Job {
			return vms[i].Job < vms[j].Job
		}

		return vms[i].Index < vms[j].Index
	})

	for _, vm := range vms {
		row := CommandPreviewVM{
			CommandVMIdentity: CommandVMIdentity{
				Group: vm.Job, Index: vm.Index, ID: vm.ID, CID: vm.CID,
				AZ: vm.AZ, IPs: vm.IPs, State: vm.State, JobState: vm.JobState,
			},
			Targeted: vm.CID != "",
		}

		if !row.Targeted {
			row.Reason = noVMReason
		}

		inst.VMs = append(inst.VMs, row)
	}

	return inst, nil
}
