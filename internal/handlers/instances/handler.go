package instances

import (
	"net/http"
	"os"
	"strings"

	"blacksmith/internal/interfaces"
	"blacksmith/internal/ownname"
	"blacksmith/pkg/http/response"
)

// Constants for instance handler.
const (
	// Minimum parts required for processing.
	minRequiredParts = 2

	// Service names.
	serviceNameBlacksmith = "blacksmith"
)

// Handler handles instance-related HTTP requests.
type Handler struct {
	logger  interfaces.Logger
	env     Environment
	ownFile string
}

// Environment names the broker's environment.
type Environment interface {
	GetEnvironment() string
}

// NewHandler creates a new instance handler.
func NewHandler(logger interfaces.Logger, env Environment) *Handler {
	return &Handler{
		logger:  logger,
		env:     env,
		ownFile: ownname.InstanceFile,
	}
}

// GetInstanceDetails handles the /b/instance endpoint.
func (h *Handler) GetInstanceDetails(writer http.ResponseWriter, req *http.Request) {
	logger := h.logger.Named("instance-details")
	logger.Debug("fetching blacksmith instance details")

	// Read instance details from VCAP files
	instanceDetails := make(map[string]string)

	// Read AZ
	data, err := os.ReadFile("/var/vcap/instance/az")
	if err == nil {
		instanceDetails["az"] = strings.TrimSpace(string(data))
	} else {
		logger.Debug("unable to read AZ file: %s", err)

		instanceDetails["az"] = ""
	}

	// The deployment is the broker's own name, which the ownership gate
	// resolves the same way. With no name it stays empty.
	deploymentName, _ := ownname.Resolve(h.ownFile, h.environment())
	instanceDetails["deployment"] = deploymentName

	// Read Instance ID
	data, err = os.ReadFile("/var/vcap/instance/id")
	if err == nil {
		instanceDetails["id"] = strings.TrimSpace(string(data))
	} else {
		logger.Debug("unable to read instance ID file: %s", err)
		// Use a default ID for blacksmith
		instanceDetails["id"] = "0"
	}

	// Read Instance Name
	data, err = os.ReadFile("/var/vcap/instance/name")
	if err == nil {
		instanceDetails["name"] = strings.TrimSpace(string(data))
	} else {
		logger.Debug("unable to read instance name file: %s", err)
		// Use "blacksmith" as default instance group name
		instanceDetails["name"] = serviceNameBlacksmith
	}

	// Get BOSH DNS from /etc/hosts if we have an instance ID
	if instanceDetails["id"] != "" {
		boshDNS := h.getBoshDNSFromHosts(instanceDetails["id"])
		if boshDNS != "" {
			instanceDetails["bosh_dns"] = boshDNS
			logger.Debug("using BOSH DNS from /etc/hosts: %s", boshDNS)
		} else {
			logger.Debug("no BOSH DNS found in /etc/hosts for instance %s", instanceDetails["id"])
		}
	}

	response.WriteSuccess(writer, instanceDetails)
}

// GetSSHUITerminalStatus handles the /b/config/ssh/ui-terminal-status endpoint.
func (h *Handler) GetSSHUITerminalStatus(writer http.ResponseWriter, req *http.Request, uiTerminalEnabled bool) {
	statusMessage := "SSH Terminal UI is enabled"
	if !uiTerminalEnabled {
		statusMessage = "SSH Terminal UI is disabled in configuration"
	}

	result := map[string]interface{}{
		"enabled": uiTerminalEnabled,
		"message": statusMessage,
	}

	response.WriteSuccess(writer, result)
}

// getBoshDNSFromHosts reads /etc/hosts and finds BOSH DNS entries for the given instance ID.
func (h *Handler) getBoshDNSFromHosts(instanceID string) string {
	logger := h.logger.Named("bosh-dns")

	// Read /etc/hosts
	data, err := os.ReadFile("/etc/hosts")
	if err != nil {
		logger.Debug("unable to read /etc/hosts: %s", err)

		return ""
	}

	// Parse each line looking for the instance ID
	lines := strings.Split(string(data), "\\n")
	for _, line := range lines {
		// Skip comments and empty lines
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Split by whitespace
		parts := strings.Fields(line)
		if len(parts) < minRequiredParts {
			continue
		}

		// Check each hostname for our instance ID and .bosh suffix
		for i := 1; i < len(parts); i++ {
			hostname := parts[i]
			if strings.Contains(hostname, instanceID) && strings.HasSuffix(hostname, ".bosh") {
				logger.Debug("found BOSH DNS entry for instance %s: %s", instanceID, hostname)

				return hostname
			}
		}
	}

	logger.Debug("no BOSH DNS entry found in /etc/hosts for instance %s", instanceID)

	return ""
}

func (h *Handler) environment() string {
	if h.env == nil {
		return ""
	}

	return h.env.GetEnvironment()
}
