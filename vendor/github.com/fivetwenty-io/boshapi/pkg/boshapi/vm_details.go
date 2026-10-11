package boshapi

import (
	"encoding/json"
	"fmt"
	"strings"
)

// maxOutputExcerpt is the most of an unreadable line or value that the
// parsers in this package quote in an error.
const maxOutputExcerpt = 200

// VMDetails is one VM or instance as the director writes it in the task
// output of GET /deployments/{name}/vms?format=full and
// GET /deployments/{name}/instances?format=full. The director writes one JSON
// object per line.
//
// Index, Active, and Vitals are pointers because the director writes null for
// them: Index for an instance with no index, Active for a VM-less instance,
// and Vitals when the agent did not answer. DeploymentName is not part of
// every line, so callers that need it set it from the request they made.
type VMDetails struct {
	AgentID         string     `json:"agent_id"`
	JobName         string     `json:"job_name"`
	Index           *int       `json:"index"`
	ID              string     `json:"id"`
	DeploymentName  string     `json:"deployment_name"`
	ResourcePool    string     `json:"resource_pool"`
	JobState        string     `json:"job_state"`
	ProcessState    string     `json:"process_state"`
	State           string     `json:"state"`
	Bootstrap       bool       `json:"bootstrap"`
	IPs             []string   `json:"ips"`
	DNS             []string   `json:"dns"`
	AZ              string     `json:"az"`
	VMType          string     `json:"vm_type"`
	VMCID           string     `json:"vm_cid"`
	DiskCID         string     `json:"disk_cid"`
	DiskCIDs        []string   `json:"disk_cids"`
	Ignore          bool       `json:"ignore"`
	VMCreatedAt     string     `json:"vm_created_at"`
	Active          *bool      `json:"active"`
	Stemcell        VMStemcell `json:"stemcell"`
	Processes       []Process  `json:"processes"`
	Vitals          *VMVitals  `json:"vitals"`
	CloudProperties any        `json:"cloud_properties"`
}

// VMStemcell identifies the stemcell a VM was created from.
type VMStemcell struct {
	Name       string `json:"name"`
	Version    string `json:"version"`
	APIVersion int    `json:"api_version"`
}

// VMVitals is the vitals block of a VM as the agent reports it. Disk is keyed
// by disk name, usually system, ephemeral, and persistent, and holds only the
// disks the VM has. Unlike Vitals, it carries the VM's uptime and does not
// fix the set of disks.
type VMVitals struct {
	CPU    VitalsCPU                  `json:"cpu"`
	Disk   map[string]VitalsDiskUsage `json:"disk"`
	Mem    VitalsMem                  `json:"mem"`
	Swap   VitalsMem                  `json:"swap"`
	Load   []string                   `json:"load"`
	Uptime *UptimeInfo                `json:"uptime"`
}

// ParseVMDetails reads the task output of a format=full vms or instances
// listing. Each non-blank line is one JSON object. A decode error names the
// line, counting from 1 and counting blank lines. A VM whose disk_cids is
// empty but whose disk_cid is set gets disk_cids of that one disk, as the
// bosh CLI does.
func ParseVMDetails(output string) ([]VMDetails, error) {
	vms := make([]VMDetails, 0)

	for number, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		var vm VMDetails

		if err := decodeObject(trimmed, &vm); err != nil {
			return nil, fmt.Errorf("decoding VM details at line %d of the task output (line starts %q): %w; "+
				"check that the output is from a format=full request to the director's vms or instances "+
				"endpoint, that the task finished, and that the line is one complete JSON object",
				number+1, excerpt(trimmed), err)
		}

		if len(vm.DiskCIDs) == 0 && vm.DiskCID != "" {
			vm.DiskCIDs = []string{vm.DiskCID}
		}

		vms = append(vms, vm)
	}

	return vms, nil
}

// decodeObject unmarshals text, which must hold exactly one JSON object, into
// target. A JSON null is an error, because it would leave target silently
// empty.
func decodeObject(text string, target any) error {
	if text == "null" {
		return fmt.Errorf("the value is null, not an object")
	}

	return json.Unmarshal([]byte(text), target)
}

// excerpt shortens text to maxOutputExcerpt bytes for an error message.
func excerpt(text string) string {
	text = strings.ToValidUTF8(text, "")
	if len(text) <= maxOutputExcerpt {
		return text
	}

	return strings.ToValidUTF8(text[:maxOutputExcerpt], "") + "..."
}
