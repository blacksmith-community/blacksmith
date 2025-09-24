package instances

import (
	"net/http"
	"os"
	"regexp"
	"strings"

	"blacksmith/internal/interfaces"
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
	logger interfaces.Logger
}

// NewHandler creates a new instance handler.
func NewHandler(logger interfaces.Logger) *Handler {
	return &Handler{
		logger: logger,
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

	// Read Deployment
	data, err = os.ReadFile("/var/vcap/instance/deployment")
	if err == nil {
		deploymentName := strings.TrimSpace(string(data))
		// If deployment name is just a GUID, provide a more user-friendly name
		if isGUID(deploymentName) {
			instanceDetails["deployment"] = serviceNameBlacksmith
		} else {
			instanceDetails["deployment"] = deploymentName
		}
	} else {
		logger.Debug("unable to read deployment file: %s", err)

		instanceDetails["deployment"] = serviceNameBlacksmith
	}

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

// isGUID checks if a string matches the GUID/UUID pattern.
func isGUID(s string) bool {
	guidRegex := regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

	return guidRegex.MatchString(s)
}
