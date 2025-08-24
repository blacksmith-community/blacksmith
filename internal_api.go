package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"blacksmith/bosh"
	"blacksmith/bosh/ssh"
	"blacksmith/pkg/services"
	"blacksmith/pkg/services/cf"
	"blacksmith/pkg/services/common"
	"blacksmith/pkg/services/rabbitmq"
	"blacksmith/pkg/services/redis"
	rabbitmqssh "blacksmith/services/rabbitmq"
	"blacksmith/websocket"
	"github.com/fivetwenty-io/capi/v3/pkg/capi"
	gorillawebsocket "github.com/gorilla/websocket"
	"gopkg.in/yaml.v2"
)

type InternalApi struct {
	Env                            string
	Vault                          *Vault
	Broker                         *Broker
	Config                         Config
	VMMonitor                      *VMMonitor
	Services                       *services.Manager
	CFManager                      *CFConnectionManager
	SSHService                     ssh.SSHService
	RabbitMQSSHService             *rabbitmqssh.SSHService
	RabbitMQMetadataService        *rabbitmqssh.MetadataService
	RabbitMQExecutorService        *rabbitmqssh.ExecutorService
	RabbitMQAuditService           *rabbitmqssh.AuditService
	RabbitMQPluginsMetadataService *rabbitmqssh.PluginsMetadataService
	RabbitMQPluginsExecutorService *rabbitmqssh.PluginsExecutorService
	RabbitMQPluginsAuditService    *rabbitmqssh.PluginsAuditService
	WebSocketHandler               *websocket.SSHHandler
	SecurityMiddleware             *services.SecurityMiddleware
}

// parseResultOutputToEvents converts BOSH task result output (JSON lines) into TaskEvent array for the frontend
func parseResultOutputToEvents(resultOutput string) []bosh.TaskEvent {
	events := []bosh.TaskEvent{}

	if resultOutput == "" {
		return events
	}

	lines := strings.Split(resultOutput, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Parse BOSH event JSON
		var boshEvent struct {
			Time     int64    `json:"time"`
			Stage    string   `json:"stage"`
			Tags     []string `json:"tags"`
			Total    int      `json:"total"`
			Task     string   `json:"task"`
			Index    int      `json:"index"`
			State    string   `json:"state"`
			Progress int      `json:"progress"`
			Data     struct {
				Status string `json:"status"`
			} `json:"data,omitempty"`
			Error struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error,omitempty"`
		}

		if err := json.Unmarshal([]byte(line), &boshEvent); err != nil {
			// Skip lines that aren't valid JSON events
			continue
		}

		// Convert to TaskEvent structure
		event := bosh.TaskEvent{
			Time:     time.Unix(boshEvent.Time, 0),
			Stage:    boshEvent.Stage,
			Tags:     boshEvent.Tags,
			Total:    boshEvent.Total,
			Task:     boshEvent.Task,
			Index:    boshEvent.Index,
			State:    boshEvent.State,
			Progress: boshEvent.Progress,
		}

		// Add data field if present
		if boshEvent.Data.Status != "" {
			event.Data = map[string]interface{}{
				"status": boshEvent.Data.Status,
			}
		}

		// Add error if present
		if boshEvent.Error.Message != "" {
			event.Error = &bosh.TaskEventError{
				Code:    boshEvent.Error.Code,
				Message: boshEvent.Error.Message,
			}
		}

		events = append(events, event)
	}

	return events
}

// parseDebugLogToEvents converts BOSH task debug output into TaskEvent array for the frontend
func parseDebugLogToEvents(debugOutput string) []bosh.TaskEvent {
	events := []bosh.TaskEvent{}

	if debugOutput == "" {
		return events
	}

	lines := strings.Split(debugOutput, "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}

		// Parse BOSH debug log lines
		// Example format: "I, [2024-01-15T10:30:45.123456 #123]  INFO -- DirectorTaskRunner: Updating deployment"
		// Or task events like: "Task 123 | 10:30:45 | Preparing deployment: Preparing deployment (00:00:01)"

		event := bosh.TaskEvent{
			Time:  time.Now(), // Default to now, will try to parse from line
			Index: i + 1,
		}

		// Try to parse timestamp from various log formats
		if parsedTime := parseTimestampFromLogLine(line); !parsedTime.IsZero() {
			event.Time = parsedTime
		}

		// Try to parse task format: "Task 123 | HH:MM:SS | Stage: Description (duration)"
		if strings.HasPrefix(line, "Task ") {
			parts := strings.Split(line, " | ")
			if len(parts) >= 3 {
				// Extract time if present
				if len(parts) > 1 {
					event.Task = strings.TrimSpace(parts[1])
				}

				// Extract stage and task info
				if len(parts) > 2 {
					stageParts := strings.SplitN(parts[2], ":", 2)
					if len(stageParts) == 2 {
						event.Stage = strings.TrimSpace(stageParts[0])

						// Remove duration info if present (e.g., "(00:00:01)")
						taskDesc := strings.TrimSpace(stageParts[1])
						if idx := strings.LastIndex(taskDesc, "("); idx > 0 {
							taskDesc = strings.TrimSpace(taskDesc[:idx])
						}
						event.Task = taskDesc
					} else {
						event.Task = strings.TrimSpace(parts[2])
					}
				}

				// Determine state based on content
				lowerLine := strings.ToLower(line)
				if strings.Contains(lowerLine, "done") || strings.Contains(lowerLine, "finished") {
					event.State = "finished"
					event.Progress = 100
				} else if strings.Contains(lowerLine, "error") || strings.Contains(lowerLine, "failed") {
					event.State = "failed"
				} else if strings.Contains(lowerLine, "started") || strings.Contains(lowerLine, "starting") {
					event.State = "started"
				} else {
					event.State = "in_progress"
					event.Progress = 50 // Default progress for in-progress tasks
				}
			}
		} else {
			// For non-task format lines, just add them as raw events
			event.Task = strings.TrimSpace(line)
			event.State = "in_progress"

			// Check for completion/error indicators
			lowerLine := strings.ToLower(line)
			if strings.Contains(lowerLine, "done") || strings.Contains(lowerLine, "finished") || strings.Contains(lowerLine, "completed") {
				event.State = "finished"
				event.Progress = 100
			} else if strings.Contains(lowerLine, "error") || strings.Contains(lowerLine, "failed") {
				event.State = "failed"
			}
		}

		// Only add non-empty events
		if event.Task != "" || event.Stage != "" {
			events = append(events, event)
		}
	}

	return events
}

// parseTimestampFromLogLine attempts to parse timestamps from various log line formats
func parseTimestampFromLogLine(line string) time.Time {
	// Common timestamp formats found in BOSH logs
	timestampFormats := []string{
		// BOSH director log format: "I, [2024-01-15T10:30:45.123456 #123]"
		"2006-01-02T15:04:05.000000",
		"2006-01-02T15:04:05",
		// ISO 8601 formats
		"2006-01-02T15:04:05Z",
		"2006-01-02T15:04:05.000Z",
		"2006-01-02T15:04:05-07:00",
		"2006-01-02T15:04:05.000-07:00",
		// Standard formats
		"2006-01-02 15:04:05",
		"2006/01/02 15:04:05",
		// Time-only format for task logs: "10:30:45"
		"15:04:05",
	}

	// Extract timestamp from bracketed format: [YYYY-MM-DDTHH:MM:SS.mmmmmm #pid]
	if strings.Contains(line, "[") && strings.Contains(line, "]") {
		start := strings.Index(line, "[") + 1
		end := strings.Index(line, "]")
		if start < end {
			bracket := line[start:end]
			// Remove PID part if present: "2024-01-15T10:30:45.123456 #123" -> "2024-01-15T10:30:45.123456"
			if spaceIdx := strings.Index(bracket, " "); spaceIdx > 0 {
				bracket = bracket[:spaceIdx]
			}

			for _, format := range timestampFormats {
				if t, err := time.Parse(format, bracket); err == nil {
					return t
				}
			}
		}
	}

	// Try to find timestamp at the beginning of the line
	words := strings.Fields(line)
	if len(words) > 0 {
		// Try first word
		for _, format := range timestampFormats {
			if t, err := time.Parse(format, words[0]); err == nil {
				return t
			}
		}

		// Try first two words combined (date + time)
		if len(words) > 1 {
			combined := words[0] + " " + words[1]
			for _, format := range timestampFormats {
				if t, err := time.Parse(format, combined); err == nil {
					return t
				}
			}
		}
	}

	// For time-only formats in task logs (e.g., "Task 123 | 10:30:45 |"),
	// use today's date with the parsed time
	if strings.Contains(line, "|") {
		parts := strings.Split(line, "|")
		if len(parts) >= 2 {
			timeStr := strings.TrimSpace(parts[1])
			if t, err := time.Parse("15:04:05", timeStr); err == nil {
				// Use today's date with the parsed time
				now := time.Now()
				return time.Date(now.Year(), now.Month(), now.Day(),
					t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), now.Location())
			}
		}
	}

	// Return zero time if no timestamp could be parsed
	return time.Time{}
}

// convertToJSONCompatible converts map[interface{}]interface{} to map[string]interface{}
// This is necessary because YAML unmarshaling creates map[interface{}]interface{} which
// cannot be directly marshaled to JSON
func convertToJSONCompatible(v interface{}) interface{} {
	switch x := v.(type) {
	case map[interface{}]interface{}:
		m := make(map[string]interface{})
		for k, v := range x {
			m[fmt.Sprint(k)] = convertToJSONCompatible(v)
		}
		return m
	case []interface{}:
		for i, v := range x {
			x[i] = convertToJSONCompatible(v)
		}
		return x
	default:
		return v
	}
}

// getBlacksmithDeploymentInfoFromManifest extracts deployment name and instance group name from the blacksmith manifest
func (api *InternalApi) getBlacksmithDeploymentInfoFromManifest() (deploymentName, instanceGroupName string, err error) {
	l := Logger.Wrap("blacksmith-manifest")

	// First try to get manifest from BOSH director
	if api.Broker != nil && api.Broker.BOSH != nil {
		// Try to read deployment name from instance file first
		var actualDeploymentName string
		if data, readErr := os.ReadFile("/var/vcap/instance/deployment"); readErr == nil {
			actualDeploymentName = strings.TrimSpace(string(data))
			l.Debug("Read deployment name from instance file: %s", actualDeploymentName)
		} else {
			l.Debug("Unable to read deployment name from instance file: %s", readErr)
			// Fallback to environment-based name
			actualDeploymentName = fmt.Sprintf("%s-blacksmith", api.Config.Env)
			l.Debug("Using fallback deployment name: %s", actualDeploymentName)
		}

		// Get deployment manifest from BOSH director
		deployment, dirErr := api.Broker.BOSH.GetDeployment(actualDeploymentName)
		if dirErr != nil {
			l.Error("Failed to get deployment manifest from BOSH director for %s: %s", actualDeploymentName, dirErr)
			return actualDeploymentName, "blacksmith", fmt.Errorf("failed to get deployment manifest: %w", dirErr)
		}

		// Parse the manifest to extract deployment name and instance group name
		var manifest map[interface{}]interface{}
		if yamlErr := yaml.Unmarshal([]byte(deployment.Manifest), &manifest); yamlErr != nil {
			l.Error("Failed to parse deployment manifest YAML: %s", yamlErr)
			return actualDeploymentName, "blacksmith", fmt.Errorf("failed to parse manifest YAML: %w", yamlErr)
		}

		// Extract deployment name from manifest
		if manifestName, ok := manifest["name"].(string); ok {
			deploymentName = manifestName
			l.Debug("Extracted deployment name from manifest: %s", deploymentName)
		} else {
			l.Debug("Could not extract deployment name from manifest, using fallback: %s", actualDeploymentName)
			deploymentName = actualDeploymentName
		}

		// Extract instance group name from manifest
		if instanceGroups, ok := manifest["instance_groups"].([]interface{}); !ok {
			l.Error("Manifest missing instance_groups")
			return deploymentName, "blacksmith", fmt.Errorf("manifest missing instance_groups")
		} else if len(instanceGroups) == 0 {
			l.Error("Manifest has empty instance_groups")
			return deploymentName, "blacksmith", fmt.Errorf("manifest has empty instance_groups")
		} else if firstGroup, ok := instanceGroups[0].(map[interface{}]interface{}); !ok {
			l.Error("First instance group has invalid format")
			return deploymentName, "blacksmith", fmt.Errorf("first instance group has invalid format")
		} else if name, ok := firstGroup["name"].(string); !ok {
			l.Error("First instance group missing name field")
			return deploymentName, "blacksmith", fmt.Errorf("first instance group missing name field")
		} else {
			instanceGroupName = name
			l.Info("Successfully extracted deployment info from manifest: %s/%s", deploymentName, instanceGroupName)
		}
	} else {
		l.Error("BOSH director not available")
		return fmt.Sprintf("%s-blacksmith", api.Config.Env), "blacksmith", fmt.Errorf("BOSH director not available")
	}

	return deploymentName, instanceGroupName, nil
}

// getBoshDNSFromHosts reads the BOSH DNS entry from /etc/hosts for a given instance ID
func (api *InternalApi) getBoshDNSFromHosts(instanceID string) string {
	l := Logger.Wrap("bosh-dns")

	// Read /etc/hosts
	data, err := os.ReadFile("/etc/hosts")
	if err != nil {
		l.Debug("unable to read /etc/hosts: %s", err)
		return ""
	}

	// Parse each line looking for the instance ID
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		// Skip comments and empty lines
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Split by whitespace
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}

		// Check each hostname for our instance ID and .bosh suffix
		for i := 1; i < len(parts); i++ {
			hostname := parts[i]
			if strings.Contains(hostname, instanceID) && strings.HasSuffix(hostname, ".bosh") {
				l.Debug("found BOSH DNS entry for instance %s: %s", instanceID, hostname)
				return hostname
			}
		}
	}

	l.Debug("no BOSH DNS entry found in /etc/hosts for instance %s", instanceID)
	return ""
}

func (api *InternalApi) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	// Handle certificate API endpoints using consistent /b/ prefix
	if strings.HasPrefix(req.URL.Path, "/b/certificates/") {
		certAPI := NewCertificateAPI(api.Config, Logger, api.Broker)
		// Transform the path to match certificate API handler expectations
		req.URL.Path = strings.Replace(req.URL.Path, "/b/certificates", "/b/internal/certificates", 1)
		certAPI.HandleCertificatesRequest(w, req)
		return
	}

	if req.URL.Path == "/b/instance" {
		l := Logger.Wrap("instance-details")
		l.Debug("fetching blacksmith instance details")

		// Read instance details from VCAP files
		instanceDetails := make(map[string]string)

		// Read AZ
		if data, err := os.ReadFile("/var/vcap/instance/az"); err == nil {
			instanceDetails["az"] = strings.TrimSpace(string(data))
		} else {
			l.Debug("unable to read AZ file: %s", err)
			instanceDetails["az"] = ""
		}

		// Read Deployment
		if data, err := os.ReadFile("/var/vcap/instance/deployment"); err == nil {
			instanceDetails["deployment"] = strings.TrimSpace(string(data))
		} else {
			l.Debug("unable to read deployment file: %s", err)
			instanceDetails["deployment"] = ""
		}

		// Read Instance ID
		if data, err := os.ReadFile("/var/vcap/instance/id"); err == nil {
			instanceDetails["id"] = strings.TrimSpace(string(data))
		} else {
			l.Debug("unable to read instance ID file: %s", err)
			// Use a default ID for blacksmith
			instanceDetails["id"] = "0"
		}

		// Read Instance Name
		if data, err := os.ReadFile("/var/vcap/instance/name"); err == nil {
			instanceDetails["name"] = strings.TrimSpace(string(data))
		} else {
			l.Debug("unable to read instance name file: %s", err)
			// Use "blacksmith" as default instance group name
			instanceDetails["name"] = "blacksmith"
		}

		// Get BOSH DNS from /etc/hosts if we have an instance ID
		if instanceDetails["id"] != "" {
			boshDNS := api.getBoshDNSFromHosts(instanceDetails["id"])
			if boshDNS != "" {
				instanceDetails["bosh_dns"] = boshDNS
				l.Debug("using BOSH DNS from /etc/hosts: %s", boshDNS)
			} else {
				l.Debug("no BOSH DNS found in /etc/hosts for instance %s", instanceDetails["id"])
			}
		}

		// Convert to JSON
		b, err := json.Marshal(instanceDetails)
		if err != nil {
			w.WriteHeader(500)
			fmt.Fprintf(w, "error marshaling instance details: %s", err)
			return
		}

		w.Header().Set("Content-type", "application/json")
		fmt.Fprintf(w, "%s ", string(b))
		return
	}
	if req.URL.Path == "/b/status" {
		idx, err := api.Vault.GetIndex("db")
		if err != nil {
			w.WriteHeader(500)
			fmt.Fprintf(w, "error: %s\n", err)
			return
		}

		// Enrich instance data with instance names from vault
		enrichedInstances := make(map[string]interface{})
		for instanceID, instanceData := range idx.Data {
			enrichedInstance := instanceData

			// Create a copy of the instance data to avoid modifying the original
			if instanceMap, ok := instanceData.(map[string]interface{}); ok {
				enrichedInstanceMap := make(map[string]interface{})
				for k, v := range instanceMap {
					enrichedInstanceMap[k] = v
				}

				// Try to get instance_name from vault data
				var vaultData map[string]interface{}
				exists, err := api.Vault.Get(instanceID, &vaultData)
				if err == nil && exists {
					if instanceName, ok := vaultData["instance_name"].(string); ok && instanceName != "" {
						enrichedInstanceMap["instance_name"] = instanceName
					}
				}

				// Check for deletion request metadata
				var metadata map[string]interface{}
				metadataExists, metadataErr := api.Vault.Get(fmt.Sprintf("%s/metadata", instanceID), &metadata)
				if metadataErr == nil && metadataExists {
					if deleteRequestedAt, ok := metadata["delete_requested_at"].(string); ok && deleteRequestedAt != "" {
						enrichedInstanceMap["delete_requested_at"] = deleteRequestedAt
						// Mark the instance as being deleted
						enrichedInstanceMap["deletion_in_progress"] = true
						Logger.Wrap("internal-api").Debug("Instance %s marked for deletion at %s", instanceID, deleteRequestedAt)
					}
				}

				// Add VM status if VMMonitor is available
				if api.VMMonitor != nil {
					if vmStatus, err := api.VMMonitor.GetServiceVMStatus(instanceID); err == nil && vmStatus != nil {
						Logger.Wrap("internal-api").Debug("VM status for %s: status=%s, healthy=%d, total=%d",
							instanceID, vmStatus.Status, vmStatus.HealthyVMs, vmStatus.VMCount)
						enrichedInstanceMap["vm_status"] = vmStatus.Status
						enrichedInstanceMap["vm_count"] = vmStatus.VMCount
						enrichedInstanceMap["vm_healthy"] = vmStatus.HealthyVMs
						enrichedInstanceMap["vm_last_updated"] = vmStatus.LastUpdated.Unix()
					} else if err != nil {
						Logger.Wrap("internal-api").Debug("No VM status for %s: %v", instanceID, err)
					}
				}

				enrichedInstance = enrichedInstanceMap
			}

			enrichedInstances[instanceID] = enrichedInstance
		}

		out := struct {
			Env       string      `json:"env"`
			Version   string      `json:"version"`
			BuildTime string      `json:"build_time"`
			GitCommit string      `json:"git_commit"`
			Instances interface{} `json:"instances"`
			Plans     interface{} `json:"plans"`
			Log       string      `json:"log"`
		}{
			Env:       api.Env,
			Version:   Version,
			BuildTime: BuildTime,
			GitCommit: GitCommit,
			Instances: enrichedInstances,
			Plans:     deinterface(api.Broker.Plans),
			Log:       Logger.String(),
		}
		b, err := json.Marshal(out)
		if err != nil {
			w.WriteHeader(500)
			fmt.Fprintf(w, "error: %s\n", err)
			return
		}

		w.Header().Set("Content-type", "application/json")
		fmt.Fprintf(w, "%s\n", string(b))
		return
	}
	if req.URL.Path == "/b/cleanup" {
		task, err := api.Broker.BOSH.Cleanup(false)
		if err != nil {
			w.WriteHeader(500)
			fmt.Fprintf(w, "error: %s\n", err)
			return
		}
		//return a 200 and a task id for the cleanup task
		cleanups, err := api.Broker.serviceWithNoDeploymentCheck()
		if err != nil {
			w.WriteHeader(500)
			fmt.Fprintf(w, "error: %s\n", err)
			return
		}
		out := struct {
			TaskID   int      `json:"task_id"`
			Cleanups []string `json:"cleanups"`
		}{
			TaskID:   task.ID,
			Cleanups: cleanups,
		}
		js, err := json.Marshal(out)
		if err != nil {
			w.WriteHeader(500)
			fmt.Fprintf(w, "error: %s\n", err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		fmt.Fprintf(w, "%s\n", string(js))
		return
	}
	if req.URL.Path == "/b/blacksmith/logs" {
		l := Logger.Wrap("blacksmith-logs")
		l.Debug("fetching blacksmith logs")

		// Check if a specific log file is requested
		logFile := req.URL.Query().Get("file")
		var logs string

		if logFile != "" {
			// Validate the requested log file path for security
			allowedLogFiles := []string{
				"/var/vcap/sys/log/blacksmith/blacksmith.stdout.log",
				"/var/vcap/sys/log/blacksmith/blacksmith.stderr.log",
				"/var/vcap/sys/log/blacksmith/vault.stdout.log",
				"/var/vcap/sys/log/blacksmith/vault.stderr.log",
				"/var/vcap/sys/log/blacksmith.vault/bpm.log",
				"/var/vcap/sys/log/blacksmith/bpm.log",
				"/var/vcap/sys/log/blacksmith/pre-start.stdout.log",
				"/var/vcap/sys/log/blacksmith/pre-start.stderr.log",
			}

			// Check if the requested file is in the allowed list
			isAllowed := false
			for _, allowedFile := range allowedLogFiles {
				if logFile == allowedFile {
					isAllowed = true
					break
				}
			}

			if !isAllowed {
				l.Error("unauthorized log file access attempt: %s", logFile)
				w.WriteHeader(403)
				fmt.Fprintf(w, "error: unauthorized log file access\n")
				return
			}

			l.Debug("fetching logs from file: %s", logFile)

			// Read the log file
			// #nosec G304 - logFile is validated against whitelist above
			content, err := os.ReadFile(logFile)
			if err != nil {
				if os.IsNotExist(err) {
					l.Debug("log file does not exist: %s", logFile)
					logs = "" // Empty logs if file doesn't exist
				} else {
					l.Error("failed to read log file %s: %s", logFile, err)
					w.WriteHeader(500)
					fmt.Fprintf(w, "error: failed to read log file: %s\n", err)
					return
				}
			} else {
				logs = string(content)
			}
		} else {
			// Default behavior: get logs from the Logger
			logs = Logger.String()
		}

		// Return as JSON with the logs
		response := struct {
			Logs string `json:"logs"`
		}{
			Logs: logs,
		}

		b, err := json.Marshal(response)
		if err != nil {
			l.Error("failed to marshal blacksmith logs: %s", err)
			w.WriteHeader(500)
			fmt.Fprintf(w, "error: %s\n", err)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		fmt.Fprintf(w, "%s\n", string(b))
		return
	}
	if req.URL.Path == "/b/blacksmith/credentials" {
		l := Logger.Wrap("blacksmith-credentials")
		l.Debug("fetching blacksmith credentials")

		// Build credentials response from config
		creds := map[string]interface{}{
			"BOSH": map[string]interface{}{
				"address":  api.Config.BOSH.Address,
				"username": api.Config.BOSH.Username,
				"network":  api.Config.BOSH.Network,
			},
			"Vault": map[string]interface{}{
				"address": api.Config.Vault.Address,
			},
			"Broker": map[string]interface{}{
				"username": api.Config.Broker.Username,
				"port":     api.Config.Broker.Port,
				"bind_ip":  api.Config.Broker.BindIP,
			},
		}

		b, err := json.Marshal(creds)
		if err != nil {
			l.Error("failed to marshal blacksmith credentials: %s", err)
			w.WriteHeader(500)
			fmt.Fprintf(w, "error: %s\n", err)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		fmt.Fprintf(w, "%s\n", string(b))
		return
	}

	// Check for blacksmith config endpoint first (before generic pattern)
	if req.URL.Path == "/b/blacksmith/config" {
		l := Logger.Wrap("blacksmith-config")
		l.Debug("fetching blacksmith configuration")

		// Convert the entire config struct to JSON
		// The config is already loaded in api.Config
		b, err := json.Marshal(api.Config)
		if err != nil {
			l.Error("failed to marshal blacksmith config: %s", err)
			w.WriteHeader(500)
			fmt.Fprintf(w, "error: %s\n", err)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		fmt.Fprintf(w, "%s\n", string(b))
		return
	}

	// Instance config endpoint
	pattern := regexp.MustCompile(`^/b/([^/]+)/config$`)
	if m := pattern.FindStringSubmatch(req.URL.Path); m != nil {
		instanceID := m[1]

		l := Logger.Wrap("instance-config")
		l.Debug("fetching config for instance %s", instanceID)

		// Get instance data
		inst, exists, err := api.Vault.FindInstance(instanceID)
		if err != nil || !exists {
			l.Error("unable to find service instance %s in vault index", instanceID)
			w.WriteHeader(404)
			fmt.Fprintf(w, `{"error": "service instance not found"}`)
			return
		}

		// Get the manifest for this instance
		var manifestData map[string]interface{}
		exists, err = api.Vault.Get(fmt.Sprintf("%s/manifest", instanceID), &manifestData)
		if err != nil || !exists {
			l.Debug("manifest not found for instance %s, returning minimal config", instanceID)
			// Return minimal config if manifest not available
			config := map[string]interface{}{
				"instance_id": instanceID,
				"service_id":  inst.ServiceID,
				"plan_id":     inst.PlanID,
				"available":   false,
			}
			api.handleJSONResponse(w, config, nil)
			return
		}

		// Return the manifest data as config
		config := map[string]interface{}{
			"instance_id": instanceID,
			"service_id":  inst.ServiceID,
			"plan_id":     inst.PlanID,
			"manifest":    manifestData,
			"available":   true,
		}
		api.handleJSONResponse(w, config, nil)
		return
	}

	// CF Registration Management Endpoints
	if strings.HasPrefix(req.URL.Path, "/b/cf/") {
		api.handleCFRegistrationEndpoints(w, req)
		return
	}

	// Redis testing endpoints
	pattern = regexp.MustCompile(`^/b/([^/]+)/redis/(.+)$`)
	if m := pattern.FindStringSubmatch(req.URL.Path); m != nil {
		instanceID := m[1]
		operation := m[2]

		l := Logger.Wrap("redis-testing")
		l.Debug("Redis operation %s for instance %s", operation, instanceID)

		// Get credentials from vault
		var creds map[string]interface{}
		exists, err := api.Vault.Get(fmt.Sprintf("%s/creds", instanceID), &creds)
		if err != nil || !exists {
			l.Debug("Credentials not found at %s/creds, trying alternate path", instanceID)
			exists, err = api.Vault.Get(fmt.Sprintf("%s/credentials", instanceID), &creds)
			if err != nil || !exists {
				l.Error("Unable to find credentials for instance %s", instanceID)
				w.WriteHeader(404)
				fmt.Fprintf(w, `{"error": "credentials not found"}`)
				return
			}
		}

		// Check if this is a Redis instance
		if !services.IsRedisInstance(common.Credentials(creds)) {
			l.Debug("Instance %s is not identified as Redis", instanceID)
			w.WriteHeader(400)
			fmt.Fprintf(w, `{"error": "not a Redis instance"}`)
			return
		}

		// Security validation
		params := map[string]interface{}{
			"operation":   operation,
			"instance_id": instanceID,
		}
		if err := api.Services.Security.ValidateRequest(instanceID, operation, params); err != nil {
			if api.Services.Security.HandleSecurityError(w, err) {
				return
			}
		}

		// Add rate limit headers
		if headers := api.Services.Security.GetRateLimitHeaders(instanceID, operation); headers != nil {
			for key, value := range headers {
				w.Header().Set(key, value)
			}
		}

		// Handle Redis operations
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		switch operation {
		case "test":
			useTLS := req.URL.Query().Get("use_tls") == "true"
			connectionType := req.URL.Query().Get("connection_type")

			// Handle connection type parameter from frontend
			if connectionType == "tls" {
				useTLS = true
			}

			opts := common.ConnectionOptions{
				UseTLS:  useTLS,
				Timeout: 30 * time.Second,
			}
			result, err := api.Services.Redis.TestConnection(ctx, common.Credentials(creds), opts)
			api.handleJSONResponse(w, result, err)

		case "info":
			useTLS := req.URL.Query().Get("use_tls") == "true"
			result, err := api.Services.Redis.HandleInfo(ctx, instanceID, common.Credentials(creds), useTLS)
			api.handleJSONResponse(w, result, err)

		case "set":
			var setReq redis.SetRequest
			if err := json.NewDecoder(req.Body).Decode(&setReq); err != nil {
				w.WriteHeader(400)
				fmt.Fprintf(w, `{"error": "invalid request body: %s"}`, err.Error())
				return
			}
			setReq.InstanceID = instanceID
			result, err := api.Services.Redis.HandleSet(ctx, instanceID, common.Credentials(creds), &setReq)
			api.handleJSONResponse(w, result, err)

		case "get":
			var getReq redis.GetRequest
			if err := json.NewDecoder(req.Body).Decode(&getReq); err != nil {
				w.WriteHeader(400)
				fmt.Fprintf(w, `{"error": "invalid request body: %s"}`, err.Error())
				return
			}
			getReq.InstanceID = instanceID
			result, err := api.Services.Redis.HandleGet(ctx, instanceID, common.Credentials(creds), &getReq)
			api.handleJSONResponse(w, result, err)

		case "delete":
			var delReq redis.DeleteRequest
			if err := json.NewDecoder(req.Body).Decode(&delReq); err != nil {
				w.WriteHeader(400)
				fmt.Fprintf(w, `{"error": "invalid request body: %s"}`, err.Error())
				return
			}
			delReq.InstanceID = instanceID
			result, err := api.Services.Redis.HandleDelete(ctx, instanceID, common.Credentials(creds), &delReq)
			api.handleJSONResponse(w, result, err)

		case "command":
			var cmdReq redis.CommandRequest
			if err := json.NewDecoder(req.Body).Decode(&cmdReq); err != nil {
				w.WriteHeader(400)
				fmt.Fprintf(w, `{"error": "invalid request body: %s"}`, err.Error())
				return
			}
			cmdReq.InstanceID = instanceID
			result, err := api.Services.Redis.HandleCommand(ctx, instanceID, common.Credentials(creds), &cmdReq)
			api.handleJSONResponse(w, result, err)

		case "keys":
			var keysReq redis.KeysRequest
			if err := json.NewDecoder(req.Body).Decode(&keysReq); err != nil {
				w.WriteHeader(400)
				fmt.Fprintf(w, `{"error": "invalid request body: %s"}`, err.Error())
				return
			}
			keysReq.InstanceID = instanceID
			result, err := api.Services.Redis.HandleKeys(ctx, instanceID, common.Credentials(creds), &keysReq)
			api.handleJSONResponse(w, result, err)

		case "flush":
			var flushReq redis.FlushRequest
			if err := json.NewDecoder(req.Body).Decode(&flushReq); err != nil {
				w.WriteHeader(400)
				fmt.Fprintf(w, `{"error": "invalid request body: %s"}`, err.Error())
				return
			}
			flushReq.InstanceID = instanceID
			result, err := api.Services.Redis.HandleFlush(ctx, instanceID, common.Credentials(creds), &flushReq)
			api.handleJSONResponse(w, result, err)

		default:
			w.WriteHeader(404)
			fmt.Fprintf(w, `{"error": "unknown Redis operation: %s"}`, operation)
		}

		return
	}

	// RabbitMQ rabbitmqctl management endpoints
	pattern = regexp.MustCompile(`^/b/([^/]+)/rabbitmq/rabbitmqctl/(.+)$`)
	if m := pattern.FindStringSubmatch(req.URL.Path); m != nil {
		instanceID := m[1]
		operation := m[2]

		l := Logger.Wrap("rabbitmqctl")
		l.Debug("RabbitMQctl operation %s for instance %s", operation, instanceID)

		// Get instance data to construct deployment name
		inst, exists, err := api.Vault.FindInstance(instanceID)
		if err != nil || !exists {
			l.Error("unable to find service instance %s in vault index", instanceID)
			w.WriteHeader(404)
			fmt.Fprintf(w, `{"error": "service instance not found"}`)
			return
		}

		// Get the plan which contains service information
		plan, err := api.Broker.FindPlan(inst.ServiceID, inst.PlanID)
		_ = plan // Silence unused variable warning
		if err != nil {
			l.Error("unable to find plan %s/%s: %s", inst.ServiceID, inst.PlanID, err)
			w.WriteHeader(500)
			fmt.Fprintf(w, `{"error": "plan not found: %s"}`, err.Error())
			return
		}

		// Check if this is a RabbitMQ instance
		var creds map[string]interface{}
		exists, err = api.Vault.Get(fmt.Sprintf("%s/creds", instanceID), &creds)
		if err != nil || !exists {
			exists, err = api.Vault.Get(fmt.Sprintf("%s/credentials", instanceID), &creds)
			if err != nil || !exists {
				l.Error("Unable to find credentials for instance %s", instanceID)
				w.WriteHeader(404)
				fmt.Fprintf(w, `{"error": "credentials not found"}`)
				return
			}
		}

		if !services.IsRabbitMQInstance(common.Credentials(creds)) {
			l.Debug("Instance %s is not identified as RabbitMQ", instanceID)
			w.WriteHeader(400)
			fmt.Fprintf(w, `{"error": "not a RabbitMQ instance"}`)
			return
		}

		// Construct deployment name from plan-id and instance-id
		deploymentName := fmt.Sprintf("%s-%s", inst.PlanID, instanceID)

		// Get the correct instance group name from the manifest stored in Vault
		// The instance group name is critical for SSH connections to work properly
		// BOSH expects the actual instance group name from the manifest, not the plan name
		instanceName := "" // We'll determine this from the manifest
		var manifestData struct {
			Manifest string `json:"manifest"`
		}

		// First, try to get the manifest from Vault
		exists, err = api.Vault.Get(fmt.Sprintf("%s/manifest", instanceID), &manifestData)
		if err != nil {
			l.Error("Failed to retrieve manifest from vault for instance %s: %s", instanceID, err)
		} else if !exists {
			l.Error("Manifest does not exist in vault for instance %s", instanceID)
		} else if manifestData.Manifest == "" {
			l.Error("Manifest is empty for instance %s", instanceID)
		} else {
			// Parse the manifest to extract the first instance group name
			// Use map[interface{}]interface{} for compatibility with YAML unmarshaling
			var manifest map[interface{}]interface{}
			if err := yaml.Unmarshal([]byte(manifestData.Manifest), &manifest); err != nil {
				l.Error("Failed to parse manifest YAML for instance %s: %s", instanceID, err)
			} else {
				// Navigate to instance_groups[0].name
				if instanceGroups, ok := manifest["instance_groups"].([]interface{}); !ok {
					l.Error("Manifest missing instance_groups for instance %s", instanceID)
				} else if len(instanceGroups) == 0 {
					l.Error("Manifest has empty instance_groups for instance %s", instanceID)
				} else if firstGroup, ok := instanceGroups[0].(map[interface{}]interface{}); !ok {
					l.Error("First instance group has invalid format for instance %s", instanceID)
				} else if name, ok := firstGroup["name"].(string); !ok {
					l.Error("First instance group missing name field for instance %s", instanceID)
				} else {
					instanceName = name
					l.Info("Successfully extracted instance group name from manifest: %s (was using plan name: %s)", instanceName, plan.Name)
				}
			}
		}

		// If we couldn't get the instance name from the manifest, use a service-specific default
		// This is based on the common patterns seen in the test manifests
		if instanceName == "" {
			// RabbitMQ service always uses "rabbitmq" as the instance group name
			instanceName = "rabbitmq"
			l.Info("Using default instance group name 'rabbitmq' for rabbitmq service (plan was: %s)", plan.Name)
		}

		instanceIndex := 0

		// Handle different rabbitmqctl operations
		switch operation {
		case "categories":
			// GET /b/{instance_id}/rabbitmq/rabbitmqctl/categories - Returns all command categories
			if req.Method != "GET" {
				w.WriteHeader(405)
				fmt.Fprintf(w, `{"error": "Method not allowed. Use GET."}`)
				return
			}

			if api.RabbitMQMetadataService != nil {
				categories := api.RabbitMQMetadataService.GetCategories()
				api.handleJSONResponse(w, categories, nil)
			} else {
				api.handleJSONResponse(w, nil, fmt.Errorf("RabbitMQ metadata service not available"))
			}

		case "history":
			// GET /b/{instance_id}/rabbitmq/rabbitmqctl/history - Returns command execution history
			// DELETE /b/{instance_id}/rabbitmq/rabbitmqctl/history - Clears command execution history
			if req.Method == "GET" {
				if api.RabbitMQAuditService != nil {
					history, err := api.RabbitMQAuditService.GetAuditHistory(req.Context(), instanceID, 100)
					api.handleJSONResponse(w, history, err)
				} else {
					api.handleJSONResponse(w, nil, fmt.Errorf("RabbitMQ audit service not available"))
				}
			} else if req.Method == "DELETE" {
				if api.RabbitMQAuditService != nil {
					err := api.RabbitMQAuditService.ClearAuditHistory(req.Context(), instanceID)
					if err != nil {
						api.handleJSONResponse(w, nil, err)
					} else {
						api.handleJSONResponse(w, map[string]interface{}{"status": "history cleared"}, nil)
					}
				} else {
					api.handleJSONResponse(w, nil, fmt.Errorf("RabbitMQ audit service not available"))
				}
			} else {
				w.WriteHeader(405)
				fmt.Fprintf(w, `{"error": "Method not allowed. Use GET or DELETE."}`)
			}

		case "execute":
			// POST /b/{instance_id}/rabbitmq/rabbitmqctl/execute - Executes command synchronously
			if req.Method != "POST" {
				w.WriteHeader(405)
				fmt.Fprintf(w, `{"error": "Method not allowed. Use POST."}`)
				return
			}

			var executeReq struct {
				Category  string   `json:"category"`
				Command   string   `json:"command"`
				Arguments []string `json:"arguments"`
			}
			if err := json.NewDecoder(req.Body).Decode(&executeReq); err != nil {
				w.WriteHeader(400)
				fmt.Fprintf(w, `{"error": "invalid request body: %s"}`, err.Error())
				return
			}

			if executeReq.Category == "" || executeReq.Command == "" {
				w.WriteHeader(400)
				fmt.Fprintf(w, `{"error": "category and command are required"}`)
				return
			}

			if api.RabbitMQExecutorService != nil {
				// Create execution context
				ctx := rabbitmqssh.ExecutionContext{
					Context:    req.Context(),
					InstanceID: instanceID,
					User:       "api-user", // TODO: get from auth
					ClientIP:   req.RemoteAddr,
				}

				// Execute command synchronously
				execution, err := api.RabbitMQExecutorService.ExecuteCommandSync(ctx, deploymentName, instanceName, instanceIndex, executeReq.Category, executeReq.Command, executeReq.Arguments)
				if err != nil {
					api.handleJSONResponse(w, nil, err)
				} else {
					// Log to audit
					if api.RabbitMQAuditService != nil {
						if err := api.RabbitMQAuditService.LogExecution(req.Context(), execution, ctx.User, ctx.ClientIP, fmt.Sprintf("sync-%d", time.Now().Unix()), execution.Duration); err != nil {
							l.Error("Failed to log audit entry: %v", err)
						}
					}
					api.handleJSONResponse(w, execution, nil)
				}
			} else {
				api.handleJSONResponse(w, nil, fmt.Errorf("RabbitMQ executor service not available"))
			}

		case "stream":
			// WebSocket streaming endpoint for rabbitmqctl commands
			if req.Method != "GET" {
				w.WriteHeader(405)
				fmt.Fprintf(w, `{"error": "Method not allowed. Use GET for WebSocket upgrade."}`)
				return
			}

			// Check for WebSocket upgrade headers
			if req.Header.Get("Upgrade") != "websocket" {
				w.WriteHeader(400)
				fmt.Fprintf(w, `{"error": "WebSocket upgrade required"}`)
				return
			}

			// Handle WebSocket upgrade and streaming
			api.handleRabbitMQStreamingWebSocket(w, req, instanceID, deploymentName, instanceName, instanceIndex)

		default:
			// Handle category/{category} and command/{category}/{command} endpoints
			parts := strings.Split(operation, "/")
			if len(parts) >= 2 && parts[0] == "category" {
				categoryName := parts[1]

				if len(parts) == 2 {
					// GET /b/{instance_id}/rabbitmq/rabbitmqctl/category/{category} - Returns commands for category
					if req.Method != "GET" {
						w.WriteHeader(405)
						fmt.Fprintf(w, `{"error": "Method not allowed. Use GET."}`)
						return
					}

					if api.RabbitMQMetadataService != nil {
						category, err := api.RabbitMQMetadataService.GetCategory(categoryName)
						api.handleJSONResponse(w, category, err)
					} else {
						api.handleJSONResponse(w, nil, fmt.Errorf("RabbitMQ metadata service not available"))
					}
				} else if len(parts) >= 4 && parts[2] == "command" {
					commandName := parts[3]
					// GET /b/{instance_id}/rabbitmq/rabbitmqctl/category/{category}/command/{command} - Returns command help
					if req.Method != "GET" {
						w.WriteHeader(405)
						fmt.Fprintf(w, `{"error": "Method not allowed. Use GET."}`)
						return
					}

					if api.RabbitMQMetadataService != nil {
						command, err := api.RabbitMQMetadataService.GetCommand(categoryName, commandName)
						api.handleJSONResponse(w, command, err)
					} else {
						api.handleJSONResponse(w, nil, fmt.Errorf("RabbitMQ metadata service not available"))
					}
				} else {
					w.WriteHeader(404)
					fmt.Fprintf(w, `{"error": "unknown rabbitmqctl operation: %s"}`, operation)
				}
			} else {
				w.WriteHeader(404)
				fmt.Fprintf(w, `{"error": "unknown rabbitmqctl operation: %s"}`, operation)
			}
		}

		return
	}

	// RabbitMQ rabbitmq-plugins management endpoints
	pattern = regexp.MustCompile(`^/b/([^/]+)/rabbitmq/plugins/(.+)$`)
	if m := pattern.FindStringSubmatch(req.URL.Path); m != nil {
		instanceID := m[1]
		operation := m[2]

		l := Logger.Wrap("rabbitmq-plugins")
		l.Debug("RabbitMQ-plugins operation %s for instance %s", operation, instanceID)

		// Get instance data to construct deployment name
		inst, exists, err := api.Vault.FindInstance(instanceID)
		if err != nil || !exists {
			l.Error("unable to find service instance %s in vault index", instanceID)
			w.WriteHeader(404)
			fmt.Fprintf(w, `{"error": "service instance not found"}`)
			return
		}

		// Get the plan which contains service information
		plan, err := api.Broker.FindPlan(inst.ServiceID, inst.PlanID)
		_ = plan // Silence unused variable warning
		if err != nil {
			l.Error("unable to find plan %s/%s: %s", inst.ServiceID, inst.PlanID, err)
			w.WriteHeader(500)
			fmt.Fprintf(w, `{"error": "plan not found: %s"}`, err.Error())
			return
		}

		// Check if this is a RabbitMQ instance
		var creds map[string]interface{}
		exists, err = api.Vault.Get(fmt.Sprintf("%s/creds", instanceID), &creds)
		if err != nil || !exists {
			exists, err = api.Vault.Get(fmt.Sprintf("%s/credentials", instanceID), &creds)
			if err != nil || !exists {
				l.Error("Unable to find credentials for instance %s", instanceID)
				w.WriteHeader(404)
				fmt.Fprintf(w, `{"error": "credentials not found"}`)
				return
			}
		}

		if !services.IsRabbitMQInstance(common.Credentials(creds)) {
			l.Debug("Instance %s is not identified as RabbitMQ", instanceID)
			w.WriteHeader(400)
			fmt.Fprintf(w, `{"error": "not a RabbitMQ instance"}`)
			return
		}

		// Construct deployment name from plan-id and instance-id
		deploymentName := fmt.Sprintf("%s-%s", inst.PlanID, instanceID)

		// Get the correct instance group name from the manifest stored in Vault
		// The instance group name is critical for SSH connections to work properly
		// BOSH expects the actual instance group name from the manifest, not the plan name
		instanceName := "" // We'll determine this from the manifest
		var manifestData struct {
			Manifest string `json:"manifest"`
		}

		// First, try to get the manifest from Vault
		exists, err = api.Vault.Get(fmt.Sprintf("%s/manifest", instanceID), &manifestData)
		if err != nil {
			l.Error("Failed to retrieve manifest from vault for instance %s: %s", instanceID, err)
		} else if !exists {
			l.Error("Manifest does not exist in vault for instance %s", instanceID)
		} else if manifestData.Manifest == "" {
			l.Error("Manifest is empty for instance %s", instanceID)
		} else {
			// Parse the manifest to extract the first instance group name
			// Use map[interface{}]interface{} for compatibility with YAML unmarshaling
			var manifest map[interface{}]interface{}
			if err := yaml.Unmarshal([]byte(manifestData.Manifest), &manifest); err != nil {
				l.Error("Failed to parse manifest YAML for instance %s: %s", instanceID, err)
			} else {
				// Navigate to instance_groups[0].name
				if instanceGroups, ok := manifest["instance_groups"].([]interface{}); !ok {
					l.Error("Manifest missing instance_groups for instance %s", instanceID)
				} else if len(instanceGroups) == 0 {
					l.Error("Manifest has empty instance_groups for instance %s", instanceID)
				} else if firstGroup, ok := instanceGroups[0].(map[interface{}]interface{}); !ok {
					l.Error("First instance group has invalid format for instance %s", instanceID)
				} else if name, ok := firstGroup["name"].(string); !ok {
					l.Error("First instance group missing name field for instance %s", instanceID)
				} else {
					instanceName = name
					l.Info("Successfully extracted instance group name from manifest: %s (was using plan name: %s)", instanceName, plan.Name)
				}
			}
		}

		// If we couldn't get the instance name from the manifest, use a service-specific default
		// This is based on the common patterns seen in the test manifests
		if instanceName == "" {
			// RabbitMQ service always uses "rabbitmq" as the instance group name
			instanceName = "rabbitmq"
			l.Info("Using default instance group name 'rabbitmq' for rabbitmq service (plan was: %s)", plan.Name)
		}

		instanceIndex := 0

		// Handle different rabbitmq-plugins operations
		switch operation {
		case "categories":
			// GET /b/{instance_id}/rabbitmq/plugins/categories - Returns all command categories
			if req.Method != "GET" {
				w.WriteHeader(405)
				fmt.Fprintf(w, `{"error": "Method not allowed. Use GET."}`)
				return
			}

			if api.RabbitMQPluginsMetadataService != nil {
				categories := api.RabbitMQPluginsMetadataService.GetCategories()
				api.handleJSONResponse(w, categories, nil)
			} else {
				api.handleJSONResponse(w, nil, fmt.Errorf("RabbitMQ plugins metadata service not available"))
			}

		case "history":
			// GET /b/{instance_id}/rabbitmq/plugins/history - Returns command execution history
			// DELETE /b/{instance_id}/rabbitmq/plugins/history - Clears command execution history
			if req.Method == "GET" {
				if api.RabbitMQPluginsAuditService != nil {
					history, err := api.RabbitMQPluginsAuditService.GetHistory(req.Context(), instanceID, 100)
					api.handleJSONResponse(w, history, err)
				} else {
					api.handleJSONResponse(w, nil, fmt.Errorf("RabbitMQ plugins audit service not available"))
				}
			} else if req.Method == "DELETE" {
				if api.RabbitMQPluginsAuditService != nil {
					err := api.RabbitMQPluginsAuditService.ClearHistory(req.Context(), instanceID)
					if err != nil {
						api.handleJSONResponse(w, nil, err)
					} else {
						api.handleJSONResponse(w, map[string]string{"status": "success", "message": "History cleared"}, nil)
					}
				} else {
					api.handleJSONResponse(w, nil, fmt.Errorf("RabbitMQ plugins audit service not available"))
				}
			} else {
				w.WriteHeader(405)
				fmt.Fprintf(w, `{"error": "Method not allowed. Use GET or DELETE."}`)
			}

		case "execute":
			// POST /b/{instance_id}/rabbitmq/plugins/execute - Executes command synchronously
			if req.Method != "POST" {
				w.WriteHeader(405)
				fmt.Fprintf(w, `{"error": "Method not allowed. Use POST."}`)
				return
			}

			var reqData struct {
				Category  string   `json:"category"`
				Command   string   `json:"command"`
				Arguments []string `json:"arguments"`
			}

			decoder := json.NewDecoder(req.Body)
			if err := decoder.Decode(&reqData); err != nil {
				w.WriteHeader(400)
				fmt.Fprintf(w, `{"error": "Invalid JSON: %s"}`, err.Error())
				return
			}

			if reqData.Category == "" || reqData.Command == "" {
				w.WriteHeader(400)
				fmt.Fprintf(w, `{"error": "category and command are required"}`)
				return
			}

			if api.RabbitMQPluginsExecutorService != nil {
				ctx := rabbitmqssh.PluginsExecutionContext{
					Context:    req.Context(),
					InstanceID: instanceID,
					User:       "web-user",
					ClientIP:   req.RemoteAddr,
				}

				output, exitCode, err := api.RabbitMQPluginsExecutorService.ExecuteCommandSync(ctx, deploymentName, instanceName, instanceIndex, reqData.Category, reqData.Command, reqData.Arguments)

				response := map[string]interface{}{
					"output":    output,
					"exit_code": exitCode,
					"success":   err == nil && exitCode == 0,
				}

				if err != nil {
					response["error"] = err.Error()
				}

				// Log to audit
				if api.RabbitMQPluginsAuditService != nil {
					execution := &rabbitmqssh.RabbitMQPluginsExecution{
						InstanceID: instanceID,
						Category:   reqData.Category,
						Command:    reqData.Command,
						Arguments:  reqData.Arguments,
						Timestamp:  time.Now().UnixNano() / int64(time.Millisecond),
						Output:     output,
						ExitCode:   exitCode,
						Success:    err == nil && exitCode == 0,
					}

					_ = api.RabbitMQPluginsAuditService.LogExecution(req.Context(), execution, "web-user", req.RemoteAddr, fmt.Sprintf("sync-%d", time.Now().UnixNano()), 0)
				}

				api.handleJSONResponse(w, response, nil)
			} else {
				api.handleJSONResponse(w, nil, fmt.Errorf("RabbitMQ plugins executor service not available"))
			}

		case "stream":
			// WebSocket streaming endpoint for rabbitmq-plugins commands
			if req.Method != "GET" {
				w.WriteHeader(405)
				fmt.Fprintf(w, `{"error": "Method not allowed. Use GET for WebSocket upgrade."}`)
				return
			}

			api.handleRabbitMQPluginsStreamingWebSocket(w, req, instanceID, deploymentName, instanceName, instanceIndex)

		default:
			// Handle category-specific operations
			parts := strings.Split(operation, "/")
			if len(parts) >= 2 && parts[0] == "category" {
				categoryName := parts[1]

				if len(parts) == 2 {
					// GET /b/{instance_id}/rabbitmq/plugins/category/{category} - Returns commands for category
					if req.Method != "GET" {
						w.WriteHeader(405)
						fmt.Fprintf(w, `{"error": "Method not allowed. Use GET."}`)
						return
					}

					if api.RabbitMQPluginsMetadataService != nil {
						category, err := api.RabbitMQPluginsMetadataService.GetCategory(categoryName)
						api.handleJSONResponse(w, category, err)
					} else {
						api.handleJSONResponse(w, nil, fmt.Errorf("RabbitMQ plugins metadata service not available"))
					}
				} else if len(parts) >= 4 && parts[2] == "command" {
					commandName := parts[3]
					// GET /b/{instance_id}/rabbitmq/plugins/category/{category}/command/{command} - Returns command help
					if req.Method != "GET" {
						w.WriteHeader(405)
						fmt.Fprintf(w, `{"error": "Method not allowed. Use GET."}`)
						return
					}

					if api.RabbitMQPluginsMetadataService != nil {
						command, err := api.RabbitMQPluginsMetadataService.GetCommand(commandName)
						api.handleJSONResponse(w, command, err)
					} else {
						api.handleJSONResponse(w, nil, fmt.Errorf("RabbitMQ plugins metadata service not available"))
					}
				} else {
					w.WriteHeader(404)
					fmt.Fprintf(w, `{"error": "unknown rabbitmq-plugins operation: %s"}`, operation)
				}
			} else {
				w.WriteHeader(404)
				fmt.Fprintf(w, `{"error": "unknown rabbitmq-plugins operation: %s"}`, operation)
			}
		}

		return
	}

	// RabbitMQ testing endpoints
	pattern = regexp.MustCompile(`^/b/([^/]+)/rabbitmq/(.+)$`)
	if m := pattern.FindStringSubmatch(req.URL.Path); m != nil {
		instanceID := m[1]
		operation := m[2]

		l := Logger.Wrap("rabbitmq-testing")
		l.Debug("RabbitMQ operation %s for instance %s", operation, instanceID)

		// Get credentials from vault
		var creds map[string]interface{}
		exists, err := api.Vault.Get(fmt.Sprintf("%s/creds", instanceID), &creds)
		if err != nil || !exists {
			l.Debug("Credentials not found at %s/creds, trying alternate path", instanceID)
			exists, err = api.Vault.Get(fmt.Sprintf("%s/credentials", instanceID), &creds)
			if err != nil || !exists {
				l.Error("Unable to find credentials for instance %s", instanceID)
				w.WriteHeader(404)
				fmt.Fprintf(w, `{"error": "credentials not found"}`)
				return
			}
		}

		// Check if this is a RabbitMQ instance
		if !services.IsRabbitMQInstance(common.Credentials(creds)) {
			l.Debug("Instance %s is not identified as RabbitMQ", instanceID)
			w.WriteHeader(400)
			fmt.Fprintf(w, `{"error": "not a RabbitMQ instance"}`)
			return
		}

		// Security validation
		params := map[string]interface{}{
			"operation":   operation,
			"instance_id": instanceID,
		}
		if err := api.Services.Security.ValidateRequest(instanceID, operation, params); err != nil {
			if api.Services.Security.HandleSecurityError(w, err) {
				return
			}
		}

		// Add rate limit headers
		if headers := api.Services.Security.GetRateLimitHeaders(instanceID, operation); headers != nil {
			for key, value := range headers {
				w.Header().Set(key, value)
			}
		}

		// Handle RabbitMQ operations
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		switch operation {
		case "test":
			useAMQPS := req.URL.Query().Get("use_amqps") == "true"
			connectionType := req.URL.Query().Get("connection_type")
			connectionUser := req.URL.Query().Get("connection_user")
			connectionVHost := req.URL.Query().Get("connection_vhost")
			connectionPassword := req.URL.Query().Get("connection_password")

			// Handle connection type parameter from frontend
			if connectionType == "amqps" {
				useAMQPS = true
			}

			opts := common.ConnectionOptions{
				UseAMQPS:         useAMQPS,
				Timeout:          30 * time.Second,
				OverrideUser:     connectionUser,
				OverridePassword: connectionPassword,
				OverrideVHost:    connectionVHost,
			}
			result, err := api.Services.RabbitMQ.TestConnection(ctx, common.Credentials(creds), opts)
			api.handleJSONResponse(w, result, err)

		case "publish":
			var pubReq rabbitmq.PublishRequest
			if err := json.NewDecoder(req.Body).Decode(&pubReq); err != nil {
				w.WriteHeader(400)
				fmt.Fprintf(w, `{"error": "invalid request body: %s"}`, err.Error())
				return
			}
			pubReq.InstanceID = instanceID

			// Create connection options with overrides if provided
			opts := common.ConnectionOptions{
				UseAMQPS:         pubReq.UseAMQPS,
				Timeout:          30 * time.Second,
				OverrideUser:     pubReq.ConnectionUser,
				OverridePassword: pubReq.ConnectionPassword,
				OverrideVHost:    pubReq.ConnectionVHost,
			}

			result, err := api.Services.RabbitMQ.HandlePublishWithOptions(ctx, instanceID, common.Credentials(creds), &pubReq, opts)
			api.handleJSONResponse(w, result, err)

		case "consume":
			var consReq rabbitmq.ConsumeRequest
			if err := json.NewDecoder(req.Body).Decode(&consReq); err != nil {
				w.WriteHeader(400)
				fmt.Fprintf(w, `{"error": "invalid request body: %s"}`, err.Error())
				return
			}
			consReq.InstanceID = instanceID

			// Create connection options with overrides if provided
			opts := common.ConnectionOptions{
				UseAMQPS:         consReq.UseAMQPS,
				Timeout:          30 * time.Second,
				OverrideUser:     consReq.ConnectionUser,
				OverridePassword: consReq.ConnectionPassword,
				OverrideVHost:    consReq.ConnectionVHost,
			}

			result, err := api.Services.RabbitMQ.HandleConsumeWithOptions(ctx, instanceID, common.Credentials(creds), &consReq, opts)
			api.handleJSONResponse(w, result, err)

		case "queues":
			var queueReq rabbitmq.QueueInfoRequest
			if err := json.NewDecoder(req.Body).Decode(&queueReq); err != nil {
				// For GET requests, use query parameters
				queueReq.UseAMQPS = req.URL.Query().Get("use_amqps") == "true"
			}
			queueReq.InstanceID = instanceID

			// Create connection options with overrides if provided
			opts := common.ConnectionOptions{
				UseAMQPS:         queueReq.UseAMQPS,
				Timeout:          30 * time.Second,
				OverrideUser:     queueReq.ConnectionUser,
				OverridePassword: queueReq.ConnectionPassword,
				OverrideVHost:    queueReq.ConnectionVHost,
			}

			result, err := api.Services.RabbitMQ.HandleQueueInfoWithOptions(ctx, instanceID, common.Credentials(creds), &queueReq, opts)
			api.handleJSONResponse(w, result, err)

		case "queue-ops":
			var queueOpsReq rabbitmq.QueueOpsRequest
			if err := json.NewDecoder(req.Body).Decode(&queueOpsReq); err != nil {
				w.WriteHeader(400)
				fmt.Fprintf(w, `{"error": "invalid request body: %s"}`, err.Error())
				return
			}
			queueOpsReq.InstanceID = instanceID

			// Create connection options with overrides if provided
			opts := common.ConnectionOptions{
				UseAMQPS:         queueOpsReq.UseAMQPS,
				Timeout:          30 * time.Second,
				OverrideUser:     queueOpsReq.ConnectionUser,
				OverridePassword: queueOpsReq.ConnectionPassword,
				OverrideVHost:    queueOpsReq.ConnectionVHost,
			}

			result, err := api.Services.RabbitMQ.HandleQueueOpsWithOptions(ctx, instanceID, common.Credentials(creds), &queueOpsReq, opts)
			api.handleJSONResponse(w, result, err)

		case "management":
			var mgmtReq rabbitmq.ManagementRequest
			if err := json.NewDecoder(req.Body).Decode(&mgmtReq); err != nil {
				w.WriteHeader(400)
				fmt.Fprintf(w, `{"error": "invalid request body: %s"}`, err.Error())
				return
			}
			mgmtReq.InstanceID = instanceID

			// Create connection options with overrides if provided
			opts := common.ConnectionOptions{
				UseAMQPS:         !mgmtReq.UseSSL, // Management API uses HTTP/HTTPS, not AMQP/AMQPS
				Timeout:          30 * time.Second,
				OverrideUser:     mgmtReq.ConnectionUser,
				OverridePassword: mgmtReq.ConnectionPassword,
				OverrideVHost:    mgmtReq.ConnectionVHost,
			}

			result, err := api.Services.RabbitMQ.HandleManagementWithOptions(ctx, instanceID, common.Credentials(creds), &mgmtReq, opts)
			api.handleJSONResponse(w, result, err)

		default:
			w.WriteHeader(404)
			fmt.Fprintf(w, `{"error": "unknown RabbitMQ operation: %s"}`, operation)
		}

		return
	}

	// SSH command execution endpoints
	pattern = regexp.MustCompile(`^/b/([^/]+)/ssh/execute$`)
	if m := pattern.FindStringSubmatch(req.URL.Path); m != nil {
		instanceID := m[1]

		l := Logger.Wrap("ssh-execute")
		l.Debug("SSH execute command for instance %s", instanceID)

		// Only allow POST method
		if req.Method != "POST" {
			w.WriteHeader(405)
			fmt.Fprintf(w, `{"error": "Method not allowed. Use POST."}`)
			return
		}

		// Parse request body
		var sshReq ssh.SSHRequest
		if err := json.NewDecoder(req.Body).Decode(&sshReq); err != nil {
			w.WriteHeader(400)
			fmt.Fprintf(w, `{"error": "invalid request body: %s"}`, err.Error())
			return
		}

		// Get instance data to construct deployment name
		inst, exists, err := api.Vault.FindInstance(instanceID)
		if err != nil || !exists {
			l.Error("unable to find service instance %s in vault index", instanceID)
			w.WriteHeader(404)
			fmt.Fprintf(w, `{"error": "service instance not found"}`)
			return
		}

		// Get the plan which contains service information
		plan, err := api.Broker.FindPlan(inst.ServiceID, inst.PlanID)
		_ = plan // Silence unused variable warning
		if err != nil {
			l.Error("unable to find plan %s/%s: %s", inst.ServiceID, inst.PlanID, err)
			w.WriteHeader(500)
			fmt.Fprintf(w, `{"error": "plan not found: %s"}`, err.Error())
			return
		}

		// Construct deployment name from plan-id and instance-id
		deploymentName := fmt.Sprintf("%s-%s", inst.PlanID, instanceID)

		// Set deployment information from instance data
		sshReq.Deployment = deploymentName
		if sshReq.Instance == "" {
			// Default to the plan name if instance name not specified
			sshReq.Instance = plan.Name
		}
		if sshReq.Index == 0 && sshReq.Index != 0 {
			// Default to index 0 if not specified
			sshReq.Index = 0
		}

		// Validate required fields
		if sshReq.Command == "" {
			w.WriteHeader(400)
			fmt.Fprintf(w, `{"error": "command is required"}`)
			return
		}

		// Set default timeout if not specified (30 seconds)
		if sshReq.Timeout == 0 {
			sshReq.Timeout = 30
		}

		l.Debug("Executing SSH command '%s' on %s/%s/%d", sshReq.Command, sshReq.Deployment, sshReq.Instance, sshReq.Index)

		// Execute the SSH command
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(sshReq.Timeout+5)*time.Second)
		defer cancel()

		// Create SSH response channel
		responseChan := make(chan *ssh.SSHResponse, 1)
		errorChan := make(chan error, 1)

		// Execute SSH command in goroutine to handle timeout
		go func() {
			if api.SSHService != nil {
				response, err := api.SSHService.ExecuteCommand(&sshReq)
				if err != nil {
					errorChan <- err
					return
				}
				responseChan <- response
			} else {
				errorChan <- fmt.Errorf("SSH service not available")
			}
		}()

		// Wait for result or timeout
		select {
		case response := <-responseChan:
			l.Debug("SSH command completed successfully")
			api.handleJSONResponse(w, response, nil)

		case err := <-errorChan:
			l.Error("SSH command failed: %v", err)
			api.handleJSONResponse(w, nil, err)

		case <-ctx.Done():
			l.Error("SSH command timed out")
			timeoutResponse := &ssh.SSHResponse{
				Success:   false,
				ExitCode:  124, // Standard timeout exit code
				Error:     "Command execution timed out",
				Timestamp: time.Now(),
			}
			api.handleJSONResponse(w, timeoutResponse, nil)
		}

		return
	}

	// RabbitMQ SSH endpoints
	pattern = regexp.MustCompile(`^/b/([^/]+)/rabbitmq/ssh/(.+)$`)
	if m := pattern.FindStringSubmatch(req.URL.Path); m != nil {
		instanceID := m[1]
		operation := m[2]

		l := Logger.Wrap("rabbitmq-ssh")
		l.Debug("RabbitMQ SSH operation %s for instance %s", operation, instanceID)

		// Only allow POST method for SSH operations
		if req.Method != "POST" {
			w.WriteHeader(405)
			fmt.Fprintf(w, `{"error": "Method not allowed. Use POST."}`)
			return
		}

		// Get instance data to construct deployment name
		inst, exists, err := api.Vault.FindInstance(instanceID)
		if err != nil || !exists {
			l.Error("unable to find service instance %s in vault index", instanceID)
			w.WriteHeader(404)
			fmt.Fprintf(w, `{"error": "service instance not found"}`)
			return
		}

		// Get the plan which contains service information
		plan, err := api.Broker.FindPlan(inst.ServiceID, inst.PlanID)
		_ = plan // Silence unused variable warning
		if err != nil {
			l.Error("unable to find plan %s/%s: %s", inst.ServiceID, inst.PlanID, err)
			w.WriteHeader(500)
			fmt.Fprintf(w, `{"error": "plan not found: %s"}`, err.Error())
			return
		}

		// Check if this is a RabbitMQ instance
		var creds map[string]interface{}
		exists, err = api.Vault.Get(fmt.Sprintf("%s/creds", instanceID), &creds)
		if err != nil || !exists {
			exists, err = api.Vault.Get(fmt.Sprintf("%s/credentials", instanceID), &creds)
			if err != nil || !exists {
				l.Error("Unable to find credentials for instance %s", instanceID)
				w.WriteHeader(404)
				fmt.Fprintf(w, `{"error": "credentials not found"}`)
				return
			}
		}

		if !services.IsRabbitMQInstance(common.Credentials(creds)) {
			l.Debug("Instance %s is not identified as RabbitMQ", instanceID)
			w.WriteHeader(400)
			fmt.Fprintf(w, `{"error": "not a RabbitMQ instance"}`)
			return
		}

		// Construct deployment name from plan-id and instance-id
		deploymentName := fmt.Sprintf("%s-%s", inst.PlanID, instanceID)

		// Get the correct instance group name from the manifest stored in Vault
		// The instance group name is critical for SSH connections to work properly
		// BOSH expects the actual instance group name from the manifest, not the plan name
		instanceName := "" // We'll determine this from the manifest
		var manifestData struct {
			Manifest string `json:"manifest"`
		}

		// First, try to get the manifest from Vault
		exists, err = api.Vault.Get(fmt.Sprintf("%s/manifest", instanceID), &manifestData)
		if err != nil {
			l.Error("Failed to retrieve manifest from vault for instance %s: %s", instanceID, err)
		} else if !exists {
			l.Error("Manifest does not exist in vault for instance %s", instanceID)
		} else if manifestData.Manifest == "" {
			l.Error("Manifest is empty for instance %s", instanceID)
		} else {
			// Parse the manifest to extract the first instance group name
			// Use map[interface{}]interface{} for compatibility with YAML unmarshaling
			var manifest map[interface{}]interface{}
			if err := yaml.Unmarshal([]byte(manifestData.Manifest), &manifest); err != nil {
				l.Error("Failed to parse manifest YAML for instance %s: %s", instanceID, err)
			} else {
				// Navigate to instance_groups[0].name
				if instanceGroups, ok := manifest["instance_groups"].([]interface{}); !ok {
					l.Error("Manifest missing instance_groups for instance %s", instanceID)
				} else if len(instanceGroups) == 0 {
					l.Error("Manifest has empty instance_groups for instance %s", instanceID)
				} else if firstGroup, ok := instanceGroups[0].(map[interface{}]interface{}); !ok {
					l.Error("First instance group has invalid format for instance %s", instanceID)
				} else if name, ok := firstGroup["name"].(string); !ok {
					l.Error("First instance group missing name field for instance %s", instanceID)
				} else {
					instanceName = name
					l.Info("Successfully extracted instance group name from manifest: %s (was using plan name: %s)", instanceName, plan.Name)
				}
			}
		}

		// If we couldn't get the instance name from the manifest, use a service-specific default
		// This is based on the common patterns seen in the test manifests
		if instanceName == "" {
			// RabbitMQ service always uses "rabbitmq" as the instance group name
			instanceName = "rabbitmq"
			l.Info("Using default instance group name 'rabbitmq' for rabbitmq service (plan was: %s)", plan.Name)
		}

		instanceIndex := 0

		// Handle different RabbitMQ SSH operations

		switch operation {
		case "list_queues":
			if api.RabbitMQSSHService != nil {
				result, err := api.RabbitMQSSHService.ListQueues(deploymentName, instanceName, instanceIndex)
				api.handleJSONResponse(w, result, err)
			} else {
				api.handleJSONResponse(w, nil, fmt.Errorf("RabbitMQ SSH service not available"))
			}

		case "list_connections":
			if api.RabbitMQSSHService != nil {
				result, err := api.RabbitMQSSHService.ListConnections(deploymentName, instanceName, instanceIndex)
				api.handleJSONResponse(w, result, err)
			} else {
				api.handleJSONResponse(w, nil, fmt.Errorf("RabbitMQ SSH service not available"))
			}

		case "list_channels":
			if api.RabbitMQSSHService != nil {
				result, err := api.RabbitMQSSHService.ListChannels(deploymentName, instanceName, instanceIndex)
				api.handleJSONResponse(w, result, err)
			} else {
				api.handleJSONResponse(w, nil, fmt.Errorf("RabbitMQ SSH service not available"))
			}

		case "list_users":
			if api.RabbitMQSSHService != nil {
				result, err := api.RabbitMQSSHService.ListUsers(deploymentName, instanceName, instanceIndex)
				api.handleJSONResponse(w, result, err)
			} else {
				api.handleJSONResponse(w, nil, fmt.Errorf("RabbitMQ SSH service not available"))
			}

		case "cluster_status":
			if api.RabbitMQSSHService != nil {
				result, err := api.RabbitMQSSHService.ClusterStatus(deploymentName, instanceName, instanceIndex)
				api.handleJSONResponse(w, result, err)
			} else {
				api.handleJSONResponse(w, nil, fmt.Errorf("RabbitMQ SSH service not available"))
			}

		case "node_health":
			if api.RabbitMQSSHService != nil {
				result, err := api.RabbitMQSSHService.NodeHealth(deploymentName, instanceName, instanceIndex)
				api.handleJSONResponse(w, result, err)
			} else {
				api.handleJSONResponse(w, nil, fmt.Errorf("RabbitMQ SSH service not available"))
			}

		case "status":
			if api.RabbitMQSSHService != nil {
				result, err := api.RabbitMQSSHService.Status(deploymentName, instanceName, instanceIndex)
				api.handleJSONResponse(w, result, err)
			} else {
				api.handleJSONResponse(w, nil, fmt.Errorf("RabbitMQ SSH service not available"))
			}

		case "environment":
			if api.RabbitMQSSHService != nil {
				result, err := api.RabbitMQSSHService.Environment(deploymentName, instanceName, instanceIndex)
				api.handleJSONResponse(w, result, err)
			} else {
				api.handleJSONResponse(w, nil, fmt.Errorf("RabbitMQ SSH service not available"))
			}

		case "custom":
			// Handle custom commands
			var customReq struct {
				Command string   `json:"command"`
				Args    []string `json:"args"`
				Timeout int      `json:"timeout"`
			}
			if err := json.NewDecoder(req.Body).Decode(&customReq); err != nil {
				w.WriteHeader(400)
				fmt.Fprintf(w, `{"error": "invalid request body: %s"}`, err.Error())
				return
			}

			if customReq.Command == "" {
				w.WriteHeader(400)
				fmt.Fprintf(w, `{"error": "command is required"}`)
				return
			}

			if api.RabbitMQSSHService != nil {
				cmd := rabbitmqssh.RabbitMQCommand{
					Name:        customReq.Command,
					Args:        customReq.Args,
					Description: "Custom RabbitMQ command",
					Timeout:     customReq.Timeout,
				}
				result, err := api.RabbitMQSSHService.ExecuteCommand(deploymentName, instanceName, instanceIndex, cmd)
				api.handleJSONResponse(w, result, err)
			} else {
				api.handleJSONResponse(w, nil, fmt.Errorf("RabbitMQ SSH service not available"))
			}

		default:
			w.WriteHeader(404)
			fmt.Fprintf(w, `{"error": "unknown RabbitMQ SSH operation: %s"}`, operation)
		}

		return
	}

	// WebSocket SSH endpoints
	// Special case for blacksmith deployment SSH
	if req.URL.Path == "/b/blacksmith/ssh/stream" {
		l := Logger.Wrap("websocket-ssh")
		l.Debug("WebSocket SSH connection request for blacksmith deployment")

		// Only allow GET method for WebSocket upgrade
		if req.Method != "GET" {
			w.WriteHeader(405)
			fmt.Fprintf(w, `{"error": "Method not allowed. Use GET for WebSocket upgrade."}`)
			return
		}

		// Check for WebSocket upgrade headers
		if req.Header.Get("Upgrade") != "websocket" {
			w.WriteHeader(400)
			fmt.Fprintf(w, `{"error": "WebSocket upgrade required"}`)
			return
		}

		// Get deployment name from blacksmith manifest
		deploymentName, manifestInstanceName, err := api.getBlacksmithDeploymentInfoFromManifest()
		if err != nil {
			l.Error("Failed to get blacksmith deployment info from manifest: %s", err)
			w.WriteHeader(500)
			fmt.Fprintf(w, `{"error": "Failed to get deployment information: %s"}`, err.Error())
			return
		}

		// Parse instance name and index from query parameters
		instanceName := req.URL.Query().Get("instance")
		instanceIndexStr := req.URL.Query().Get("index")

		// Use manifest instance name as fallback if not provided in query
		if instanceName == "" {
			instanceName = manifestInstanceName
		}

		// Parse instance index, default to 0 if not provided or invalid
		instanceIndex := 0
		if instanceIndexStr != "" {
			if parsedIndex, parseErr := strconv.Atoi(instanceIndexStr); parseErr == nil {
				instanceIndex = parsedIndex
			} else {
				l.Error("Invalid instance index '%s', using default 0", instanceIndexStr)
			}
		}

		l.Info("Establishing WebSocket SSH connection to blacksmith deployment %s/%s/%d", deploymentName, instanceName, instanceIndex)

		// Handle WebSocket SSH connection
		if api.WebSocketHandler != nil {
			api.WebSocketHandler.HandleWebSocket(w, req, deploymentName, instanceName, instanceIndex)
		} else {
			l.Error("WebSocket handler not available")
			w.WriteHeader(500)
			fmt.Fprintf(w, `{"error": "WebSocket SSH service not available"}`)
		}

		return
	}

	// Service instance SSH endpoints
	pattern = regexp.MustCompile(`^/b/([^/]+)/ssh/stream$`)
	if m := pattern.FindStringSubmatch(req.URL.Path); m != nil {
		instanceID := m[1]

		l := Logger.Wrap("websocket-ssh")
		l.Debug("WebSocket SSH connection request for instance %s", instanceID)

		// Only allow GET method for WebSocket upgrade
		if req.Method != "GET" {
			w.WriteHeader(405)
			fmt.Fprintf(w, `{"error": "Method not allowed. Use GET for WebSocket upgrade."}`)
			return
		}

		// Check for WebSocket upgrade headers
		if req.Header.Get("Upgrade") != "websocket" {
			w.WriteHeader(400)
			fmt.Fprintf(w, `{"error": "WebSocket upgrade required"}`)
			return
		}

		// Get instance data to construct deployment name
		inst, exists, err := api.Vault.FindInstance(instanceID)
		if err != nil || !exists {
			l.Error("unable to find service instance %s in vault index", instanceID)
			w.WriteHeader(404)
			fmt.Fprintf(w, `{"error": "service instance not found"}`)
			return
		}

		// Get the plan which contains service information
		plan, err := api.Broker.FindPlan(inst.ServiceID, inst.PlanID)
		_ = plan // Silence unused variable warning
		if err != nil {
			l.Error("unable to find plan %s/%s: %s", inst.ServiceID, inst.PlanID, err)
			w.WriteHeader(500)
			fmt.Fprintf(w, `{"error": "plan not found: %s"}`, err.Error())
			return
		}

		// Construct deployment name from plan-id and instance-id
		deploymentName := fmt.Sprintf("%s-%s", inst.PlanID, instanceID)

		// Get the correct instance group name from the manifest stored in Vault
		// The instance group name is critical for SSH connections to work properly
		// BOSH expects the actual instance group name from the manifest, not the plan name
		instanceName := "" // We'll determine this from the manifest
		manifestData := struct {
			Manifest string `json:"manifest"`
		}{}

		// First, try to get the manifest from Vault
		exists, err = api.Vault.Get(fmt.Sprintf("%s/manifest", instanceID), &manifestData)
		if err != nil {
			l.Error("Failed to retrieve manifest from vault for instance %s: %s", instanceID, err)
		} else if !exists {
			l.Error("Manifest does not exist in vault for instance %s", instanceID)
		} else if manifestData.Manifest == "" {
			l.Error("Manifest is empty for instance %s", instanceID)
		} else {
			// Parse the YAML manifest to extract the instance group name
			// Use map[interface{}]interface{} for compatibility with YAML unmarshaling
			var manifest map[interface{}]interface{}
			if err := yaml.Unmarshal([]byte(manifestData.Manifest), &manifest); err != nil {
				l.Error("Failed to parse manifest YAML for instance %s: %s", instanceID, err)
			} else {
				// Navigate to instance_groups[0].name
				if instanceGroups, ok := manifest["instance_groups"].([]interface{}); !ok {
					l.Error("Manifest missing instance_groups for instance %s", instanceID)
				} else if len(instanceGroups) == 0 {
					l.Error("Manifest has empty instance_groups for instance %s", instanceID)
				} else if firstGroup, ok := instanceGroups[0].(map[interface{}]interface{}); !ok {
					l.Error("First instance group has invalid format for instance %s", instanceID)
				} else if name, ok := firstGroup["name"].(string); !ok {
					l.Error("First instance group missing name field for instance %s", instanceID)
				} else {
					instanceName = name
					l.Info("Successfully extracted instance group name from manifest: %s (was using plan name: %s)", instanceName, plan.Name)
				}
			}
		}

		// If we couldn't get the instance name from the manifest, use a service-specific default
		// This is based on the common patterns seen in the test manifests
		if instanceName == "" {
			switch plan.Service.ID {
			case "redis", "redis-cache":
				// Check if this is a standalone Redis or clustered
				if strings.Contains(plan.Name, "standalone") {
					instanceName = "standalone"
				} else {
					instanceName = "redis"
				}
				l.Info("Using instance group name '%s' for redis service (plan was: %s)", instanceName, plan.Name)
			case "rabbitmq":
				instanceName = "rabbitmq"
				l.Info("Using default instance group name 'rabbitmq' for rabbitmq service (plan was: %s)", plan.Name)
			case "postgresql":
				instanceName = "postgres"
				l.Info("Using default instance group name 'postgres' for postgresql service (plan was: %s)", plan.Name)
			default:
				// Last resort: use plan name
				instanceName = plan.Name
				l.Info("Could not determine instance group name, falling back to plan name: %s", plan.Name)
			}
		}
		instanceIndex := 0

		l.Info("Establishing WebSocket SSH connection to %s/%s/%d", deploymentName, instanceName, instanceIndex)

		// Handle WebSocket SSH connection
		if api.WebSocketHandler != nil {
			api.WebSocketHandler.HandleWebSocket(w, req, deploymentName, instanceName, instanceIndex)
		} else {
			l.Error("WebSocket handler not available")
			w.WriteHeader(500)
			fmt.Fprintf(w, `{"error": "WebSocket SSH service not available"}`)
		}

		return
	}

	// WebSocket SSH status endpoint
	pattern = regexp.MustCompile(`^/b/ssh/status$`)
	if m := pattern.FindStringSubmatch(req.URL.Path); m != nil {
		l := Logger.Wrap("websocket-ssh-status")
		l.Debug("WebSocket SSH status request")

		if req.Method != "GET" {
			w.WriteHeader(405)
			fmt.Fprintf(w, `{"error": "Method not allowed. Use GET."}`)
			return
		}

		status := map[string]interface{}{
			"websocket_enabled": api.WebSocketHandler != nil,
			"active_sessions":   0,
			"timestamp":         time.Now().Format(time.RFC3339),
		}

		if api.WebSocketHandler != nil {
			status["active_sessions"] = api.WebSocketHandler.GetActiveSessions()
		}

		api.handleJSONResponse(w, status, nil)
		return
	}

	// Service instance certificate SSH endpoints
	pattern = regexp.MustCompile(`^/b/([^/]+)/certificates/trusted$`)
	if m := pattern.FindStringSubmatch(req.URL.Path); m != nil {
		instanceID := m[1]

		l := Logger.Wrap("ssh-certificates-trusted")
		l.Debug("SSH certificate listing for instance %s", instanceID)

		// Only allow GET method
		if req.Method != "GET" {
			w.WriteHeader(405)
			fmt.Fprintf(w, `{"error": "Method not allowed. Use GET."}`)
			return
		}

		// Get instance data to construct deployment name
		inst, exists, err := api.Vault.FindInstance(instanceID)
		if err != nil || !exists {
			l.Error("unable to find service instance %s in vault index", instanceID)
			w.WriteHeader(404)
			fmt.Fprintf(w, `{"error": "service instance not found"}`)
			return
		}

		// Get the plan which contains service information
		plan, err := api.Broker.FindPlan(inst.ServiceID, inst.PlanID)
		_ = plan // Silence unused variable warning
		if err != nil {
			l.Error("unable to find plan %s/%s: %s", inst.ServiceID, inst.PlanID, err)
			w.WriteHeader(500)
			fmt.Fprintf(w, `{"error": "plan not found: %s"}`, err.Error())
			return
		}

		// Construct deployment name from plan-id and instance-id
		deploymentName := fmt.Sprintf("%s-%s", inst.PlanID, instanceID)

		// Get the correct instance group name from the manifest stored in Vault
		// The instance group name is critical for SSH connections to work properly
		// BOSH expects the actual instance group name from the manifest, not the plan name
		instanceName := "" // We'll determine this from the manifest
		var manifestData struct {
			Manifest string `json:"manifest"`
		}

		// First, try to get the manifest from Vault
		exists, err = api.Vault.Get(fmt.Sprintf("%s/manifest", instanceID), &manifestData)
		if err != nil {
			l.Error("Failed to retrieve manifest from vault for instance %s: %s", instanceID, err)
		} else if !exists {
			l.Error("Manifest does not exist in vault for instance %s", instanceID)
		} else if manifestData.Manifest == "" {
			l.Error("Manifest is empty for instance %s", instanceID)
		} else {
			// Parse the manifest to extract the first instance group name
			// Use map[interface{}]interface{} for compatibility with YAML unmarshaling
			var manifest map[interface{}]interface{}
			if err := yaml.Unmarshal([]byte(manifestData.Manifest), &manifest); err != nil {
				l.Error("Failed to parse manifest YAML for instance %s: %s", instanceID, err)
			} else {
				// Navigate to instance_groups[0].name
				if instanceGroups, ok := manifest["instance_groups"].([]interface{}); !ok {
					l.Error("Manifest missing instance_groups for instance %s", instanceID)
				} else if len(instanceGroups) == 0 {
					l.Error("Manifest has empty instance_groups for instance %s", instanceID)
				} else if firstGroup, ok := instanceGroups[0].(map[interface{}]interface{}); !ok {
					l.Error("First instance group has invalid format for instance %s", instanceID)
				} else if name, ok := firstGroup["name"].(string); !ok {
					l.Error("First instance group missing name field for instance %s", instanceID)
				} else {
					instanceName = name
					l.Info("Successfully extracted instance group name from manifest: %s (was using plan name: %s)", instanceName, plan.Name)
				}
			}
		}

		// If we couldn't get the instance name from the manifest, use a service-specific default
		// This is based on the common patterns seen in the test manifests
		if instanceName == "" {
			// RabbitMQ service always uses "rabbitmq" as the instance group name
			instanceName = "rabbitmq"
			l.Info("Using default instance group name 'rabbitmq' for rabbitmq service (plan was: %s)", plan.Name)
		}

		instanceIndex := 0

		// Create SSH request to list certificate files
		sshReq := ssh.SSHRequest{
			Deployment: deploymentName,
			Instance:   instanceName,
			Index:      instanceIndex,
			Command:    "/bin/bash",
			Args:       []string{"-c", "/bin/ls /etc/ssl/certs/bosh-trusted-cert-*.pem"},
			Timeout:    30,
		}

		l.Debug("Listing certificates via SSH command '%s' with args %v on %s/%s/%d", sshReq.Command, sshReq.Args, sshReq.Deployment, sshReq.Instance, sshReq.Index)

		// Execute the SSH command
		ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
		defer cancel()

		// Create SSH response channel
		responseChan := make(chan *ssh.SSHResponse, 1)
		errorChan := make(chan error, 1)

		// Execute SSH command in goroutine
		go func() {
			if api.SSHService != nil {
				response, err := api.SSHService.ExecuteCommand(&sshReq)
				if err != nil {
					errorChan <- err
					return
				}
				responseChan <- response
			} else {
				errorChan <- fmt.Errorf("SSH service not available")
			}
		}()

		// Wait for result or timeout
		select {
		case response := <-responseChan:
			l.Debug("SSH certificate listing completed successfully")

			// Parse the certificate file paths from SSH output
			var files []CertificateFileItem
			if response.Success && response.Stdout != "" {
				lines := strings.Split(strings.TrimSpace(response.Stdout), "\n")
				for _, line := range lines {
					line = strings.TrimSpace(line)
					if line != "" && !strings.Contains(line, "No certificates found") && strings.HasSuffix(line, ".pem") {
						fileName := filepath.Base(line)
						files = append(files, CertificateFileItem{
							Name: fileName,
							Path: line,
						})
					}
				}
			}

			certResponse := CertificateFileResponse{
				Success: true,
				Data: CertificateFileData{
					Files: files,
					Metadata: CertificateMetadata{
						Source:    "service-trusted",
						Timestamp: time.Now(),
						Count:     len(files),
					},
				},
			}

			api.handleJSONResponse(w, certResponse, nil)

		case err := <-errorChan:
			l.Error("SSH certificate listing failed: %v", err)
			api.handleJSONResponse(w, CertificateFileResponse{
				Success: false,
				Data: CertificateFileData{
					Files: []CertificateFileItem{},
					Metadata: CertificateMetadata{
						Source:    "service-trusted",
						Timestamp: time.Now(),
						Count:     0,
					},
				},
			}, err)

		case <-ctx.Done():
			l.Error("SSH certificate listing timed out")
			timeoutResponse := CertificateFileResponse{
				Success: false,
				Data: CertificateFileData{
					Files: []CertificateFileItem{},
					Metadata: CertificateMetadata{
						Source:    "service-trusted",
						Timestamp: time.Now(),
						Count:     0,
					},
				},
			}
			api.handleJSONResponse(w, timeoutResponse, nil)
		}

		return
	}

	// Service instance certificate file SSH endpoint
	pattern = regexp.MustCompile(`^/b/([^/]+)/certificates/trusted/file$`)
	if m := pattern.FindStringSubmatch(req.URL.Path); m != nil {
		instanceID := m[1]

		l := Logger.Wrap("ssh-certificates-trusted-file")
		l.Debug("SSH certificate file fetch for instance %s", instanceID)

		// Only allow POST method
		if req.Method != "POST" {
			w.WriteHeader(405)
			fmt.Fprintf(w, `{"error": "Method not allowed. Use POST."}`)
			return
		}

		// Parse request body
		var requestData struct {
			FilePath string `json:"filePath"`
		}

		if err := json.NewDecoder(req.Body).Decode(&requestData); err != nil {
			w.WriteHeader(400)
			fmt.Fprintf(w, `{"error": "invalid request body: %s"}`, err.Error())
			return
		}

		if requestData.FilePath == "" {
			w.WriteHeader(400)
			fmt.Fprintf(w, `{"error": "filePath is required"}`)
			return
		}

		// Security validation: ensure the file path is in /etc/ssl/certs and matches the expected pattern
		if !strings.HasPrefix(requestData.FilePath, "/etc/ssl/certs/bosh-trusted-cert-") ||
			!strings.HasSuffix(requestData.FilePath, ".pem") {
			w.WriteHeader(400)
			fmt.Fprintf(w, `{"error": "invalid file path: must be a BOSH trusted certificate"}`)
			return
		}

		// Get instance data to construct deployment name
		inst, exists, err := api.Vault.FindInstance(instanceID)
		if err != nil || !exists {
			l.Error("unable to find service instance %s in vault index", instanceID)
			w.WriteHeader(404)
			fmt.Fprintf(w, `{"error": "service instance not found"}`)
			return
		}

		// Get the plan which contains service information
		plan, err := api.Broker.FindPlan(inst.ServiceID, inst.PlanID)
		_ = plan // Silence unused variable warning
		if err != nil {
			l.Error("unable to find plan %s/%s: %s", inst.ServiceID, inst.PlanID, err)
			w.WriteHeader(500)
			fmt.Fprintf(w, `{"error": "plan not found: %s"}`, err.Error())
			return
		}

		// Construct deployment name from plan-id and instance-id
		deploymentName := fmt.Sprintf("%s-%s", inst.PlanID, instanceID)

		// Get the correct instance group name from the manifest stored in Vault
		// The instance group name is critical for SSH connections to work properly
		// BOSH expects the actual instance group name from the manifest, not the plan name
		instanceName := "" // We'll determine this from the manifest
		var manifestData struct {
			Manifest string `json:"manifest"`
		}

		// First, try to get the manifest from Vault
		exists, err = api.Vault.Get(fmt.Sprintf("%s/manifest", instanceID), &manifestData)
		if err != nil {
			l.Error("Failed to retrieve manifest from vault for instance %s: %s", instanceID, err)
		} else if !exists {
			l.Error("Manifest does not exist in vault for instance %s", instanceID)
		} else if manifestData.Manifest == "" {
			l.Error("Manifest is empty for instance %s", instanceID)
		} else {
			// Parse the manifest to extract the first instance group name
			// Use map[interface{}]interface{} for compatibility with YAML unmarshaling
			var manifest map[interface{}]interface{}
			if err := yaml.Unmarshal([]byte(manifestData.Manifest), &manifest); err != nil {
				l.Error("Failed to parse manifest YAML for instance %s: %s", instanceID, err)
			} else {
				// Navigate to instance_groups[0].name
				if instanceGroups, ok := manifest["instance_groups"].([]interface{}); !ok {
					l.Error("Manifest missing instance_groups for instance %s", instanceID)
				} else if len(instanceGroups) == 0 {
					l.Error("Manifest has empty instance_groups for instance %s", instanceID)
				} else if firstGroup, ok := instanceGroups[0].(map[interface{}]interface{}); !ok {
					l.Error("First instance group has invalid format for instance %s", instanceID)
				} else if name, ok := firstGroup["name"].(string); !ok {
					l.Error("First instance group missing name field for instance %s", instanceID)
				} else {
					instanceName = name
					l.Info("Successfully extracted instance group name from manifest: %s (was using plan name: %s)", instanceName, plan.Name)
				}
			}
		}

		// If we couldn't get the instance name from the manifest, use a service-specific default
		// This is based on the common patterns seen in the test manifests
		if instanceName == "" {
			// RabbitMQ service always uses "rabbitmq" as the instance group name
			instanceName = "rabbitmq"
			l.Info("Using default instance group name 'rabbitmq' for rabbitmq service (plan was: %s)", plan.Name)
		}

		instanceIndex := 0

		// Create SSH request to read certificate file
		sshReq := ssh.SSHRequest{
			Deployment: deploymentName,
			Instance:   instanceName,
			Index:      instanceIndex,
			Command:    "/bin/bash",
			Args:       []string{"-c", fmt.Sprintf("cat %s", requestData.FilePath)},
			Timeout:    30,
		}

		l.Debug("Reading certificate file via SSH command '%s' with args %v on %s/%s/%d", sshReq.Command, sshReq.Args, sshReq.Deployment, sshReq.Instance, sshReq.Index)

		// Execute the SSH command
		ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
		defer cancel()

		// Create SSH response channel
		responseChan := make(chan *ssh.SSHResponse, 1)
		errorChan := make(chan error, 1)

		// Execute SSH command in goroutine
		go func() {
			if api.SSHService != nil {
				response, err := api.SSHService.ExecuteCommand(&sshReq)
				if err != nil {
					errorChan <- err
					return
				}
				responseChan <- response
			} else {
				errorChan <- fmt.Errorf("SSH service not available")
			}
		}()

		// Wait for result or timeout
		select {
		case response := <-responseChan:
			l.Debug("SSH certificate file read completed successfully")

			if response.Success && response.Stdout != "" {
				// Parse the certificate content
				certInfo, err := ParseCertificateFromPEM(response.Stdout)
				if err != nil {
					l.Error("failed to parse certificate: %v", err)
					api.handleJSONResponse(w, nil, fmt.Errorf("failed to parse certificate: %v", err))
					return
				}

				certificates := []CertificateListItem{
					{
						Name:    filepath.Base(requestData.FilePath),
						Path:    requestData.FilePath,
						Details: *certInfo,
					},
				}

				certResponse := CertificateResponse{
					Success: true,
					Data: CertificateResponseData{
						Certificates: certificates,
						Metadata: CertificateMetadata{
							Source:    "service-trusted-file",
							Timestamp: time.Now(),
							Count:     len(certificates),
						},
					},
				}

				api.handleJSONResponse(w, certResponse, nil)
			} else {
				l.Error("SSH command failed or returned no output: success=%v, stdout='%s', stderr='%s'", response.Success, response.Stdout, response.Stderr)
				errorMsg := response.Error
				if errorMsg == "" && response.Stderr != "" {
					errorMsg = response.Stderr
				}
				if errorMsg == "" {
					errorMsg = "No output returned from certificate file"
				}
				api.handleJSONResponse(w, nil, fmt.Errorf("failed to read certificate file: %s", errorMsg))
			}

		case err := <-errorChan:
			l.Error("SSH certificate file read failed: %v", err)
			api.handleJSONResponse(w, nil, err)

		case <-ctx.Done():
			l.Error("SSH certificate file read timed out")
			timeoutErr := fmt.Errorf("certificate file read operation timed out")
			api.handleJSONResponse(w, nil, timeoutErr)
		}

		return
	}

	pattern = regexp.MustCompile(`^/b/([^/]+)/manifest\.yml$`)
	if m := pattern.FindStringSubmatch(req.URL.Path); m != nil {
		l := Logger.Wrap("manifest.yml")
		l.Debug("looking up BOSH manifest for %s", m[1])
		manifest := struct {
			Manifest string `json:"manifest"`
		}{}
		exists, err := api.Vault.Get(fmt.Sprintf("%s/manifest", m[1]), &manifest)
		if err != nil || !exists {
			l.Error("unable to find service instance %s in vault index", m[1])
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		if _, err := w.Write([]byte(manifest.Manifest + "\n")); err != nil {
			l.Error("failed to write manifest response: %s", err)
		}
		return
	}

	// New endpoint for manifest details (returns both YAML text and parsed JSON)
	pattern = regexp.MustCompile(`^/b/([^/]+)/manifest-details$`)
	if m := pattern.FindStringSubmatch(req.URL.Path); m != nil {
		l := Logger.Wrap("manifest-details")
		l.Debug("looking up BOSH manifest details for %s", m[1])
		manifestData := struct {
			Manifest string `json:"manifest"`
		}{}
		exists, err := api.Vault.Get(fmt.Sprintf("%s/manifest", m[1]), &manifestData)
		if err != nil || !exists {
			l.Error("unable to find service instance %s in vault index", m[1])
			w.WriteHeader(404)
			return
		}

		// Parse the YAML manifest into a generic structure
		var manifestParsed interface{}
		if err := yaml.Unmarshal([]byte(manifestData.Manifest), &manifestParsed); err != nil {
			l.Error("unable to parse manifest YAML: %s", err)
			w.WriteHeader(500)
			fmt.Fprintf(w, `{"error": "unable to parse manifest"}`)
			return
		}

		// Convert map[interface{}]interface{} to map[string]interface{} for JSON compatibility
		manifestParsed = convertToJSONCompatible(manifestParsed)

		// Create response with both text and parsed JSON
		response := map[string]interface{}{
			"text":   manifestData.Manifest,
			"parsed": manifestParsed,
		}

		w.Header().Set("Content-Type", "application/json")
		b, err := json.Marshal(response)
		if err != nil {
			l.Error("unable to marshal manifest details: %s", err)
			w.WriteHeader(500)
			fmt.Fprintf(w, `{"error": "unable to marshal response"}`)
			return
		}
		if _, err := w.Write(b); err != nil {
			l.Error("failed to write manifest details response: %s", err)
		}
		return
	}

	pattern = regexp.MustCompile("^/b/([^/]+)/vms$")
	if m := pattern.FindStringSubmatch(req.URL.Path); m != nil {
		l := Logger.Wrap("vms")
		param := m[1]
		l.Debug("looking up VMs for %s", param)

		var deploymentName string

		// Check if this is the blacksmith deployment itself
		if param == "blacksmith" {
			// For the blacksmith deployment, get the deployment name from the instance files
			if data, err := os.ReadFile("/var/vcap/instance/deployment"); err == nil {
				deploymentName = strings.TrimSpace(string(data))
				l.Debug("using blacksmith deployment name from instance file: %s", deploymentName)
			} else {
				// Fallback to "blacksmith" if we can't read the file
				deploymentName = "blacksmith"
				l.Debug("using default blacksmith deployment name: %s", deploymentName)
			}
		} else {
			// For service instances, construct deployment name from vault data
			inst, exists, err := api.Vault.FindInstance(param)
			if err != nil || !exists {
				l.Error("unable to find service instance %s in vault index", param)
				w.WriteHeader(404)
				return
			}

			// Construct deployment name from service-id, plan-id, and instance-id
			deploymentName = fmt.Sprintf("%s-%s", inst.PlanID, param)
			l.Debug("fetching VMs for service deployment %s", deploymentName)
		}

		// Get VMs from BOSH
		vms, err := api.Broker.BOSH.GetDeploymentVMs(deploymentName)
		if err != nil {
			l.Error("unable to get VMs for deployment %s: %s", deploymentName, err)
			w.WriteHeader(500)
			fmt.Fprintf(w, "error: %s", err)
			return
		}

		// Check if resurrection config exists for this deployment and determine status
		// Use 'blacksmith.{deployment}' format as type already indicates 'resurrection'
		configName := fmt.Sprintf("blacksmith.%s", deploymentName)
		l.Debug("Checking for resurrection config: %s (deployment: %s)", configName, deploymentName)

		resurrectionPaused := false // Default: resurrection active (BOSH default)

		resurrectionConfig, err := api.Broker.BOSH.GetConfig("resurrection", configName)
		if err != nil {
			l.Debug("Error getting resurrection config: %v", err)
		}

		if err == nil && resurrectionConfig != nil {
			l.Debug("Resurrection config found for %s, type: %T", configName, resurrectionConfig)
			l.Debug("Resurrection config content: %+v", resurrectionConfig)

			// Parse the config to determine if resurrection is enabled or disabled
			// The config should contain YAML with rules that specify enabled: true/false
			if configMap, ok := resurrectionConfig.(map[string]interface{}); ok {
				l.Debug("Config parsed as map with %d keys", len(configMap))
				for k, v := range configMap {
					l.Debug("Config key '%s': %T = %+v", k, v, v)
				}

				if rules, ok := configMap["rules"].([]interface{}); ok && len(rules) > 0 {
					l.Debug("Found %d rules in config", len(rules))
					for i, r := range rules {
						l.Debug("Rule %d: %T = %+v", i, r, r)
					}

					// Handle both map[string]interface{} and map[interface{}]interface{} from YAML parsing
					var ruleMap map[string]interface{}

					switch r := rules[0].(type) {
					case map[string]interface{}:
						ruleMap = r
					case map[interface{}]interface{}:
						// Convert map[interface{}]interface{} to map[string]interface{}
						ruleMap = make(map[string]interface{})
						for k, v := range r {
							if ks, ok := k.(string); ok {
								ruleMap[ks] = v
							}
						}
					default:
						l.Error("First rule is not a map: %T", rules[0])
					}

					if ruleMap != nil {
						l.Debug("First rule is a map with %d keys", len(ruleMap))
						for k, v := range ruleMap {
							l.Debug("Rule key '%s': %T = %+v", k, v, v)
						}

						if enabled, ok := ruleMap["enabled"].(bool); ok {
							// If enabled=false in config, then resurrection is paused
							resurrectionPaused = !enabled
							l.Info("Resurrection config for %s: enabled=%v, setting paused=%v", deploymentName, enabled, resurrectionPaused)
						} else {
							l.Error("'enabled' field in rule is not a bool or missing")
						}
					}
				} else {
					l.Debug("No rules found in config or rules is not an array")
				}
			} else {
				l.Error("Resurrection config is not a map: %T", resurrectionConfig)
			}
		} else {
			l.Debug("No resurrection config found (config: %v, err: %v), using BOSH default (resurrection active)", resurrectionConfig, err)
		}

		// Override resurrection status for all VMs based on deployment-level config
		resurrectionConfigExists := (err == nil && resurrectionConfig != nil)
		for i := range vms {
			vms[i].ResurrectionPaused = resurrectionPaused
			vms[i].ResurrectionConfigExists = resurrectionConfigExists
			l.Debug("VM %d (%s): resurrection_paused set to %v, config_exists=%v", i, vms[i].ID, vms[i].ResurrectionPaused, vms[i].ResurrectionConfigExists)
		}

		// Cache VM data in vault at secret/<instance-id>/vms
		if param != "blacksmith" {
			// Only cache for service instances, not for blacksmith deployment itself
			l.Debug("caching VM data in vault for instance %s", param)
			err = api.Vault.Put(fmt.Sprintf("%s/vms", param), map[string]interface{}{
				"vms":        vms,
				"cached_at":  time.Now().Format(time.RFC3339),
				"deployment": deploymentName,
			})
			if err != nil {
				l.Error("failed to cache VM data in vault for instance %s: %s", param, err)
				// Continue anyway - caching failure shouldn't break the API
			} else {
				l.Debug("successfully cached VM data for instance %s", param)
			}
		}

		// Note: We don't compute BOSH DNS for VMs here since it's already displayed in the VMs tab
		// The DNS field remains as provided by BOSH (typically empty)

		// Convert VMs to JSON
		b, err := json.MarshalIndent(vms, "", "  ")
		if err != nil {
			w.WriteHeader(500)
			fmt.Fprintf(w, "error marshaling VMs: %s", err)
			return
		}

		w.Header().Set("Content-type", "application/json")
		fmt.Fprintf(w, "%s ", string(b))
		return
	}

	// Resurrection toggle endpoint - PUT /b/{instance_id}/resurrection
	pattern = regexp.MustCompile("^/b/([^/]+)/resurrection$")
	if m := pattern.FindStringSubmatch(req.URL.Path); m != nil && req.Method == "PUT" {
		l := Logger.Wrap("resurrection-toggle")
		param := m[1]
		l.Debug("toggling resurrection for %s", param)

		var requestBody map[string]interface{}
		if err := json.NewDecoder(req.Body).Decode(&requestBody); err != nil {
			l.Error("failed to decode request body: %s", err)
			w.WriteHeader(400)
			fmt.Fprintf(w, `{"error": "invalid JSON in request body"}`)
			return
		}

		enabled, ok := requestBody["enabled"].(bool)
		if !ok {
			l.Error("missing or invalid 'enabled' field in request body")
			w.WriteHeader(400)
			fmt.Fprintf(w, `{"error": "missing or invalid 'enabled' field"}`)
			return
		}

		var deploymentName string

		// Check if this is the blacksmith deployment itself
		if param == "blacksmith" {
			// For the blacksmith deployment, get the deployment name from the instance files
			if data, err := os.ReadFile("/var/vcap/instance/deployment"); err == nil {
				deploymentName = strings.TrimSpace(string(data))
				l.Debug("using blacksmith deployment name from instance file: %s", deploymentName)
			} else {
				// Fallback to "blacksmith" if we can't read the file
				deploymentName = "blacksmith"
				l.Debug("using default blacksmith deployment name: %s", deploymentName)
			}
		} else {
			// For service instances, construct deployment name from vault data
			inst, exists, err := api.Vault.FindInstance(param)
			if err != nil || !exists {
				l.Error("unable to find service instance %s in vault index", param)
				w.WriteHeader(404)
				fmt.Fprintf(w, `{"error": "service instance not found"}`)
				return
			}

			// Construct deployment name from service-id, plan-id, and instance-id
			deploymentName = fmt.Sprintf("%s-%s", inst.PlanID, param)
			l.Debug("toggling resurrection for service deployment %s", deploymentName)
		}

		// Toggle resurrection via BOSH
		err := api.Broker.BOSH.EnableResurrection(deploymentName, enabled)
		if err != nil {
			l.Error("unable to toggle resurrection for deployment %s: %s", deploymentName, err)
			w.WriteHeader(500)
			fmt.Fprintf(w, `{"error": "failed to toggle resurrection: %s"}`, err)
			return
		}

		// Return success response
		action := "enabled"
		if !enabled {
			action = "disabled"
		}
		response := map[string]interface{}{
			"success": true,
			"message": fmt.Sprintf("Resurrection %s for deployment %s", action, deploymentName),
			"enabled": enabled,
		}

		w.Header().Set("Content-type", "application/json")
		if err := json.NewEncoder(w).Encode(response); err != nil {
			l.Error("failed to encode response: %s", err)
		}
		return
	}

	// Delete resurrection config endpoint - DELETE /b/{instance_id}/resurrection
	pattern = regexp.MustCompile("^/b/([^/]+)/resurrection$")
	if m := pattern.FindStringSubmatch(req.URL.Path); m != nil && req.Method == "DELETE" {
		l := Logger.Wrap("resurrection-delete")
		param := m[1]
		l.Debug("deleting resurrection config for %s", param)

		// Determine deployment name based on param (same logic as toggle)
		var deploymentName string
		if param == "blacksmith" {
			deploymentName = "blacksmith"
			l.Debug("deleting resurrection config for blacksmith deployment")
		} else {
			l.Info("fetching service instance %s from vault", param)
			var instance map[string]interface{}
			exists, err := api.Vault.Get(fmt.Sprintf("secret/service/%s", param), &instance)
			if err != nil || !exists {
				l.Error("unable to fetch service instance %s: %s", param, err)
				w.WriteHeader(404)
				fmt.Fprintf(w, `{"error": "service instance not found"}`)
				return
			}

			if serviceName, ok := instance["service_name"].(string); !ok {
				l.Error("service_name not found or not a string in instance %s", param)
				w.WriteHeader(500)
				fmt.Fprintf(w, `{"error": "invalid service instance data"}`)
				return
			} else {
				deploymentName = fmt.Sprintf("%s-%s", serviceName, param)
			}
			l.Debug("deleting resurrection config for service deployment %s", deploymentName)
		}

		// Delete resurrection config via BOSH
		err := api.Broker.BOSH.DeleteResurrectionConfig(deploymentName)
		if err != nil {
			l.Error("unable to delete resurrection config for deployment %s: %s", deploymentName, err)
			w.WriteHeader(500)
			fmt.Fprintf(w, `{"error": "failed to delete resurrection config: %s"}`, err)
			return
		}

		// Return success response
		response := map[string]interface{}{
			"success": true,
			"message": fmt.Sprintf("Resurrection config deleted for deployment %s", deploymentName),
		}

		w.Header().Set("Content-type", "application/json")
		if err := json.NewEncoder(w).Encode(response); err != nil {
			l.Error("failed to encode response: %s", err)
		}
		return
	}

	// Service instance detail endpoint - returns vault data for the instance
	pattern = regexp.MustCompile("^/b/([^/]+)/details$")
	if m := pattern.FindStringSubmatch(req.URL.Path); m != nil {
		l := Logger.Wrap("instance-details")
		instanceID := m[1]
		l.Debug("fetching details for instance %s", instanceID)

		// Get vault data for the instance
		var instanceData map[string]interface{}
		exists, err := api.Vault.Get(instanceID, &instanceData)
		if err != nil || !exists {
			l.Error("unable to find service instance %s in vault", instanceID)
			w.WriteHeader(404)
			return
		}

		// Get metadata from secret/{instance-id}/metadata
		var metadata map[string]interface{}
		metadataExists, err := api.Vault.Get(fmt.Sprintf("%s/metadata", instanceID), &metadata)
		if err != nil {
			l.Debug("failed to get metadata for instance %s: %s", instanceID, err)
			metadataExists = false
		}

		// Enrich older instances with missing service metadata on-the-fly
		if instanceData["service_name"] == nil || instanceData["service_type"] == nil {
			if serviceID, ok := instanceData["service_id"].(string); ok {
				if planID, ok := instanceData["plan_id"].(string); ok {
					// Try to get service and plan info from broker
					if api.Broker != nil {
						if plan, err := api.Broker.FindPlan(serviceID, planID); err == nil {
							// Add missing service_name
							if instanceData["service_name"] == nil && plan.Service != nil {
								instanceData["service_name"] = plan.Service.Name
								l.Debug("Enriched instance %s with service_name: %s", instanceID, plan.Service.Name)
							}
							// Add missing service_type (infer from service name for common services)
							if instanceData["service_type"] == nil && plan.Service != nil {
								var serviceType string
								switch plan.Service.Name {
								case "redis":
									serviceType = "redis"
								case "rabbitmq":
									serviceType = "rabbitmq"
								default:
									serviceType = plan.Service.Name // Use service name as fallback
								}
								instanceData["service_type"] = serviceType
								l.Debug("Enriched instance %s with service_type: %s", instanceID, serviceType)
							}
						}
					}
				}
			}
		}

		// Remove context field as requested
		delete(instanceData, "context")

		// Merge metadata into instance data if available
		if metadataExists && metadata != nil {
			for key, value := range metadata {
				// Only add metadata fields that don't conflict with existing instance data
				if _, exists := instanceData[key]; !exists {
					instanceData[key] = value
				}
			}
		}

		// Convert to JSON
		b, err := json.MarshalIndent(instanceData, "", "  ")
		if err != nil {
			w.WriteHeader(500)
			fmt.Fprintf(w, "error marshaling instance details: %s", err)
			return
		}

		w.Header().Set("Content-type", "application/json")
		fmt.Fprintf(w, "%s ", string(b))
		return
	}

	pattern = regexp.MustCompile("^/b/([^/]+)/update-manifest$")
	if m := pattern.FindStringSubmatch(req.URL.Path); m != nil {
		l := Logger.Wrap("update-manifest")
		l.Debug("updating BOSH manifest for %s", m[1])

		l.Debug("looking up BOSH manifest for %s", m[1])

		inst, exists, err := api.Vault.FindInstance(m[1])
		if err != nil || !exists {
			l.Error("unable to find service instance %s in vault index", m[1])
			w.WriteHeader(404)
			return
		}

		l.Debug("looking up service '%s' / plan '%s' in catalog", inst.ServiceID, inst.PlanID)
		_, err = api.Broker.FindPlan(inst.ServiceID, inst.PlanID)
		if err != nil {
			w.WriteHeader(500)
			fmt.Fprintf(w, "error: %s\n", err)
			return
		}

		// Read the request body (expected to be the new manifest)
		body, err := io.ReadAll(req.Body)
		if err != nil {
			w.WriteHeader(400)
			fmt.Fprintf(w, "error reading request body: %s\n", err)
			return
		}

		// Validate the manifest is valid YAML
		var testManifest interface{}
		if err := yaml.Unmarshal(body, &testManifest); err != nil {
			w.WriteHeader(400)
			fmt.Fprintf(w, "invalid YAML: %s\n", err)
			return
		}

		// Store the manifest in vault
		data := map[string]interface{}{
			"manifest": string(body),
		}
		if err := api.Vault.Put(fmt.Sprintf("%s/manifest", m[1]), data); err != nil {
			w.WriteHeader(500)
			fmt.Fprintf(w, "error storing manifest: %s\n", err)
			return
		}

		l.Debug("manifest updated for instance %s", m[1])
		w.WriteHeader(200)
		fmt.Fprintf(w, "manifest updated successfully\n")
		return
	}

	// Events endpoint
	pattern = regexp.MustCompile("^/b/([^/]+)/events$")
	if m := pattern.FindStringSubmatch(req.URL.Path); m != nil {
		l := Logger.Wrap("events")
		instanceID := m[1]
		l.Debug("fetching events for instance %s", instanceID)

		// Get instance data to construct deployment name
		inst, exists, err := api.Vault.FindInstance(instanceID)
		if err != nil || !exists {
			l.Error("unable to find service instance %s in vault index", instanceID)
			w.WriteHeader(404)
			return
		}

		// Get the plan which contains service information
		plan, err := api.Broker.FindPlan(inst.ServiceID, inst.PlanID)
		_ = plan // Silence unused variable warning
		if err != nil {
			l.Error("unable to find plan %s/%s: %s", inst.ServiceID, inst.PlanID, err)
			w.WriteHeader(500)
			fmt.Fprintf(w, "error: %s", err)
			return
		}

		deploymentName := fmt.Sprintf("%s-%s", inst.PlanID, instanceID)
		l.Debug("fetching events for deployment %s", deploymentName)

		// Get events from BOSH
		events, err := api.Broker.BOSH.GetEvents(deploymentName)
		if err != nil {
			l.Error("unable to get events for deployment %s: %s", deploymentName, err)
			// Return empty array instead of error to gracefully handle missing deployments
			events = []bosh.Event{}
		}

		// Convert events to JSON
		b, err := json.MarshalIndent(events, "", "  ")
		if err != nil {
			w.WriteHeader(500)
			fmt.Fprintf(w, "error marshaling events: %s", err)
			return
		}

		w.Header().Set("Content-type", "application/json")
		fmt.Fprintf(w, "%s", string(b))
		return
	}

	// Task log endpoint - returns deployment log (result output)
	pattern = regexp.MustCompile("^/b/([^/]+)/task/log$")
	if m := pattern.FindStringSubmatch(req.URL.Path); m != nil {
		l := Logger.Wrap("task-log")
		instanceID := m[1]
		l.Debug("fetching task log for instance %s", instanceID)

		// Get instance data to construct deployment name
		inst, exists, err := api.Vault.FindInstance(instanceID)
		if err != nil || !exists {
			l.Error("unable to find service instance %s in vault index", instanceID)
			w.WriteHeader(404)
			return
		}

		// Get the plan which contains service information
		plan, err := api.Broker.FindPlan(inst.ServiceID, inst.PlanID)
		_ = plan // Silence unused variable warning
		if err != nil {
			l.Error("unable to find plan %s/%s: %s", inst.ServiceID, inst.PlanID, err)
			w.WriteHeader(500)
			fmt.Fprintf(w, "error: %s", err)
			return
		}

		deploymentName := fmt.Sprintf("%s-%s", inst.PlanID, instanceID)
		l.Debug("fetching task log for deployment %s", deploymentName)

		// Get events from BOSH to find the task ID
		events, err := api.Broker.BOSH.GetEvents(deploymentName)
		if err != nil {
			l.Error("unable to get events for deployment %s: %s", deploymentName, err)
			// Return empty array
			w.Header().Set("Content-type", "application/json")
			fmt.Fprintf(w, "[]")
			return
		}

		// Find the most recent deployment task ID from events
		var taskID int
		for i := len(events) - 1; i >= 0; i-- {
			event := events[i]
			// Look for deployment-related events with task IDs
			// Skip lock acquisition events as they don't contain deployment logs
			if event.TaskID != "" && event.Action != "acquire" {
				// Prioritize deployment creation/update events
				if event.ObjectType == "deployment" || event.Action == "create" ||
					event.Action == "update" || event.Action == "deploy" {
					// Convert task ID string to int
					if id, err := strconv.Atoi(event.TaskID); err == nil {
						taskID = id
						l.Debug("found deployment task ID %d from event (action: %s, object: %s)",
							taskID, event.Action, event.ObjectType)
						break
					}
				}
			}
		}

		// If no deployment task found, try any non-lock task as fallback
		if taskID == 0 {
			for i := len(events) - 1; i >= 0; i-- {
				event := events[i]
				if event.TaskID != "" && event.Action != "acquire" && event.Action != "release" {
					if id, err := strconv.Atoi(event.TaskID); err == nil {
						taskID = id
						l.Debug("found fallback task ID %d from event (action: %s, object: %s)",
							taskID, event.Action, event.ObjectType)
						break
					}
				}
			}
		}

		if taskID == 0 {
			l.Debug("no task ID found in events for deployment %s", deploymentName)
			// Return empty array
			w.Header().Set("Content-type", "application/json")
			fmt.Fprintf(w, "[]")
			return
		}

		// Get task output from BOSH (deployment log)
		// Try "event" output first, then fall back to "result" if empty
		eventOutput, err := api.Broker.BOSH.GetTaskOutput(taskID, "event")
		if err != nil {
			l.Debug("unable to get task event output for task %d: %s", taskID, err)
			eventOutput = ""
		}

		var taskOutput string
		if eventOutput != "" {
			l.Debug("using event output for task %d (size: %d bytes)", taskID, len(eventOutput))
			taskOutput = eventOutput
		} else {
			// Fall back to result output
			resultOutput, err := api.Broker.BOSH.GetTaskOutput(taskID, "result")
			if err != nil {
				l.Error("unable to get task result output for task %d: %s", taskID, err)
				// Return empty array on error
				w.Header().Set("Content-type", "application/json")
				fmt.Fprintf(w, "[]")
				return
			}
			l.Debug("using result output for task %d (size: %d bytes)", taskID, len(resultOutput))
			taskOutput = resultOutput
		}

		// Parse the output into task events for the frontend
		taskEvents := parseResultOutputToEvents(taskOutput)

		// Convert to JSON
		b, err := json.MarshalIndent(taskEvents, "", "  ")
		if err != nil {
			w.WriteHeader(500)
			fmt.Fprintf(w, "error marshaling task events: %s", err)
			return
		}

		w.Header().Set("Content-type", "application/json")
		fmt.Fprintf(w, "%s", string(b))
		return
	}

	// Debug log endpoint - returns debug output
	pattern = regexp.MustCompile("^/b/([^/]+)/task/debug$")
	if m := pattern.FindStringSubmatch(req.URL.Path); m != nil {
		l := Logger.Wrap("debug-log")
		instanceID := m[1]
		l.Debug("fetching debug log for instance %s", instanceID)

		// Get instance data to construct deployment name
		inst, exists, err := api.Vault.FindInstance(instanceID)
		if err != nil || !exists {
			l.Error("unable to find service instance %s in vault index", instanceID)
			w.WriteHeader(404)
			return
		}

		// Get the plan which contains service information
		plan, err := api.Broker.FindPlan(inst.ServiceID, inst.PlanID)
		_ = plan // Silence unused variable warning
		if err != nil {
			l.Error("unable to find plan %s/%s: %s", inst.ServiceID, inst.PlanID, err)
			w.WriteHeader(500)
			fmt.Fprintf(w, "error: %s", err)
			return
		}

		deploymentName := fmt.Sprintf("%s-%s", inst.PlanID, instanceID)
		l.Debug("fetching debug log for deployment %s", deploymentName)

		// Get events from BOSH to find the task ID
		events, err := api.Broker.BOSH.GetEvents(deploymentName)
		if err != nil {
			l.Error("unable to get events for deployment %s: %s", deploymentName, err)
			// Return empty array
			w.Header().Set("Content-type", "application/json")
			fmt.Fprintf(w, "[]")
			return
		}

		// Find the most recent deployment task ID from events
		var taskID int
		for i := len(events) - 1; i >= 0; i-- {
			event := events[i]
			// Look for deployment-related events with task IDs
			// Skip lock acquisition events as they don't contain deployment logs
			if event.TaskID != "" && event.Action != "acquire" {
				// Prioritize deployment creation/update events
				if event.ObjectType == "deployment" || event.Action == "create" ||
					event.Action == "update" || event.Action == "deploy" {
					// Convert task ID string to int
					if id, err := strconv.Atoi(event.TaskID); err == nil {
						taskID = id
						l.Debug("found deployment task ID %d from event (action: %s, object: %s)",
							taskID, event.Action, event.ObjectType)
						break
					}
				}
			}
		}

		// If no deployment task found, try any non-lock task as fallback
		if taskID == 0 {
			for i := len(events) - 1; i >= 0; i-- {
				event := events[i]
				if event.TaskID != "" && event.Action != "acquire" && event.Action != "release" {
					if id, err := strconv.Atoi(event.TaskID); err == nil {
						taskID = id
						l.Debug("found fallback task ID %d from event (action: %s, object: %s)",
							taskID, event.Action, event.ObjectType)
						break
					}
				}
			}
		}

		if taskID == 0 {
			l.Debug("no task ID found in events for deployment %s", deploymentName)
			// Return empty array
			w.Header().Set("Content-type", "application/json")
			fmt.Fprintf(w, "[]")
			return
		}

		// Get task debug output from BOSH
		debugOutput, err := api.Broker.BOSH.GetTaskOutput(taskID, "debug")
		if err != nil {
			l.Error("unable to get task debug output for task %d: %s", taskID, err)
			// Return empty array on error
			w.Header().Set("Content-type", "application/json")
			fmt.Fprintf(w, "[]")
			return
		}

		// Parse the debug output into task events for the frontend
		taskEvents := parseDebugLogToEvents(debugOutput)

		// Convert to JSON
		b, err := json.MarshalIndent(taskEvents, "", "  ")
		if err != nil {
			w.WriteHeader(500)
			fmt.Fprintf(w, "error marshaling task events: %s", err)
			return
		}

		w.Header().Set("Content-type", "application/json")
		fmt.Fprintf(w, "%s", string(b))
		return
	}

	// Tasks list endpoint - GET /b/tasks
	if req.URL.Path == "/b/tasks" && req.Method == "GET" {
		l := Logger.Wrap("tasks-list")
		l.Debug("fetching tasks list")

		// Parse query parameters
		query := req.URL.Query()
		taskType := query.Get("type")
		if taskType == "" {
			taskType = "recent"
		}

		limitStr := query.Get("limit")
		limit := 50 // default
		if limitStr != "" {
			if parsedLimit, err := strconv.Atoi(limitStr); err == nil && parsedLimit > 0 && parsedLimit <= 200 {
				limit = parsedLimit
			}
		}

		var states []string
		statesParam := query.Get("states")
		if statesParam != "" {
			states = strings.Split(statesParam, ",")
		}

		l.Debug("getting tasks: type=%s, limit=%d, states=%v", taskType, limit, states)

		// Get tasks from BOSH
		tasks, err := api.Broker.BOSH.GetTasks(taskType, limit, states)
		if err != nil {
			l.Error("unable to get tasks: %s", err)
			w.WriteHeader(500)
			fmt.Fprintf(w, "error: %s", err)
			return
		}

		// Convert to JSON
		b, err := json.MarshalIndent(tasks, "", "  ")
		if err != nil {
			l.Error("error marshaling tasks: %s", err)
			w.WriteHeader(500)
			fmt.Fprintf(w, "error marshaling tasks: %s", err)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, "%s", string(b))
		return
	}

	// Task details endpoint - GET /b/tasks/{id}
	pattern = regexp.MustCompile("^/b/tasks/([0-9]+)$")
	if m := pattern.FindStringSubmatch(req.URL.Path); m != nil && req.Method == "GET" {
		l := Logger.Wrap("task-details")
		taskIDStr := m[1]
		taskID, err := strconv.Atoi(taskIDStr)
		if err != nil {
			l.Error("invalid task ID: %s", taskIDStr)
			w.WriteHeader(400)
			fmt.Fprintf(w, "invalid task ID")
			return
		}

		l.Debug("fetching task details for task %d", taskID)

		// Get task from BOSH
		task, err := api.Broker.BOSH.GetTask(taskID)
		if err != nil {
			l.Error("unable to get task %d: %s", taskID, err)
			w.WriteHeader(404)
			fmt.Fprintf(w, "task not found: %s", err)
			return
		}

		// Get task events
		events, err := api.Broker.BOSH.GetTaskEvents(taskID)
		if err != nil {
			l.Error("unable to get task events for task %d: %s", taskID, err)
			// Continue without events
			events = []bosh.TaskEvent{}
		}

		// Create response with task details and events
		response := struct {
			*bosh.Task
			Events []bosh.TaskEvent `json:"events"`
		}{
			Task:   task,
			Events: events,
		}

		// Convert to JSON
		b, err := json.MarshalIndent(response, "", "  ")
		if err != nil {
			l.Error("error marshaling task details: %s", err)
			w.WriteHeader(500)
			fmt.Fprintf(w, "error marshaling task details: %s", err)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, "%s", string(b))
		return
	}

	// Task output endpoint - GET /b/tasks/{id}/output
	pattern = regexp.MustCompile("^/b/tasks/([0-9]+)/output$")
	if m := pattern.FindStringSubmatch(req.URL.Path); m != nil && req.Method == "GET" {
		l := Logger.Wrap("task-output")
		taskIDStr := m[1]
		taskID, err := strconv.Atoi(taskIDStr)
		if err != nil {
			l.Error("invalid task ID: %s", taskIDStr)
			w.WriteHeader(400)
			fmt.Fprintf(w, "invalid task ID")
			return
		}

		// Parse query parameters
		query := req.URL.Query()
		outputType := query.Get("type")
		if outputType == "" {
			outputType = "result"
		}

		l.Debug("fetching task output for task %d (type: %s)", taskID, outputType)

		// Get task output from BOSH
		output, err := api.Broker.BOSH.GetTaskOutput(taskID, outputType)
		if err != nil {
			l.Error("unable to get task output for task %d: %s", taskID, err)
			w.WriteHeader(404)
			fmt.Fprintf(w, "task output not found: %s", err)
			return
		}

		// Parse output based on type
		var response interface{}
		if outputType == "result" || outputType == "event" {
			// Parse as events for structured output
			events := parseResultOutputToEvents(output)
			response = events
		} else {
			// Return raw output for debug logs
			response = map[string]interface{}{
				"output": output,
				"type":   outputType,
			}
		}

		// Convert to JSON
		b, err := json.MarshalIndent(response, "", "  ")
		if err != nil {
			l.Error("error marshaling task output: %s", err)
			w.WriteHeader(500)
			fmt.Fprintf(w, "error marshaling task output: %s", err)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, "%s", string(b))
		return
	}

	// Task cancel endpoint - POST /b/tasks/{id}/cancel
	pattern = regexp.MustCompile("^/b/tasks/([0-9]+)/cancel$")
	if m := pattern.FindStringSubmatch(req.URL.Path); m != nil && req.Method == "POST" {
		l := Logger.Wrap("task-cancel")
		taskIDStr := m[1]
		taskID, err := strconv.Atoi(taskIDStr)
		if err != nil {
			l.Error("invalid task ID: %s", taskIDStr)
			w.WriteHeader(400)
			fmt.Fprintf(w, "invalid task ID")
			return
		}

		l.Debug("cancelling task %d", taskID)

		// Cancel task via BOSH
		err = api.Broker.BOSH.CancelTask(taskID)
		if err != nil {
			l.Error("unable to cancel task %d: %s", taskID, err)
			w.WriteHeader(400)
			fmt.Fprintf(w, "failed to cancel task: %s", err)
			return
		}

		l.Info("successfully cancelled task %d", taskID)

		response := map[string]interface{}{
			"success": true,
			"message": fmt.Sprintf("Task %d cancelled successfully", taskID),
		}

		b, err := json.MarshalIndent(response, "", "  ")
		if err != nil {
			l.Error("error marshaling cancel response: %s", err)
			w.WriteHeader(500)
			fmt.Fprintf(w, "error marshaling response: %s", err)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, "%s", string(b))
		return
	}

	// Configs list endpoint - GET /b/configs
	if req.URL.Path == "/b/configs" && req.Method == "GET" {
		l := Logger.Wrap("configs-list")
		l.Debug("fetching configs list")

		// Parse query parameters
		query := req.URL.Query()

		limitStr := query.Get("limit")
		limit := 50 // default
		if limitStr != "" {
			if parsedLimit, err := strconv.Atoi(limitStr); err == nil && parsedLimit > 0 && parsedLimit <= 200 {
				limit = parsedLimit
			}
		}

		var configTypes []string
		typesParam := query.Get("types")
		if typesParam != "" {
			configTypes = strings.Split(typesParam, ",")
			// Trim whitespace from each type
			for i, t := range configTypes {
				configTypes[i] = strings.TrimSpace(t)
			}
		}

		l.Debug("fetching configs with limit: %d, types: %v", limit, configTypes)

		// Fetch configs from BOSH
		configs, err := api.Broker.BOSH.GetConfigs(limit, configTypes)
		if err != nil {
			l.Error("unable to fetch configs: %s", err)
			w.WriteHeader(500)
			fmt.Fprintf(w, "failed to fetch configs: %s", err)
			return
		}

		l.Info("successfully fetched %d configs", len(configs))

		response := map[string]interface{}{
			"configs": configs,
			"count":   len(configs),
		}

		b, err := json.MarshalIndent(response, "", "  ")
		if err != nil {
			l.Error("error marshaling configs response: %s", err)
			w.WriteHeader(500)
			fmt.Fprintf(w, "error marshaling response: %s", err)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, "%s", string(b))
		return
	}

	// Config details endpoint - GET /b/configs/{id}
	pattern = regexp.MustCompile("^/b/configs/([^/]+)$")
	if m := pattern.FindStringSubmatch(req.URL.Path); m != nil && req.Method == "GET" {
		l := Logger.Wrap("config-details")
		configID := m[1]
		l.Debug("fetching config details for ID: %s", configID)

		// Fetch config details from BOSH
		config, err := api.Broker.BOSH.GetConfigByID(configID)
		if err != nil {
			l.Error("unable to fetch config %s: %s", configID, err)
			w.WriteHeader(404)
			fmt.Fprintf(w, "config not found: %s", err)
			return
		}

		l.Info("successfully fetched config details for ID: %s", configID)

		b, err := json.MarshalIndent(config, "", "  ")
		if err != nil {
			l.Error("error marshaling config details response: %s", err)
			w.WriteHeader(500)
			fmt.Fprintf(w, "error marshaling response: %s", err)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, "%s", string(b))
		return
	}
	//
	// Service Instance Logs endpoint
	pattern = regexp.MustCompile("^/b/([^/]+)/instance-logs$")
	if m := pattern.FindStringSubmatch(req.URL.Path); m != nil {
		l := Logger.Wrap("instance-logs")
		instanceID := m[1]
		l.Debug("fetching instance logs for instance %s", instanceID)

		// Get instance data to construct deployment name
		inst, exists, err := api.Vault.FindInstance(instanceID)
		if err != nil || !exists {
			l.Error("unable to find service instance %s in vault index", instanceID)
			w.WriteHeader(404)
			return
		}

		// Get the plan which contains service information
		plan, err := api.Broker.FindPlan(inst.ServiceID, inst.PlanID)
		_ = plan // Silence unused variable warning
		if err != nil {
			l.Error("unable to find plan %s/%s: %s", inst.ServiceID, inst.PlanID, err)
			w.WriteHeader(500)
			fmt.Fprintf(w, "error: %s", err)
			return
		}

		deploymentName := fmt.Sprintf("%s-%s", inst.PlanID, instanceID)
		l.Debug("fetching logs for deployment %s", deploymentName)

		// Get the manifest to determine the job name
		var manifestData struct {
			Manifest string `json:"manifest"`
		}
		exists, err = api.Vault.Get(fmt.Sprintf("%s/manifest", instanceID), &manifestData)
		if err != nil || !exists {
			l.Error("unable to find manifest for instance %s", instanceID)
			w.WriteHeader(500)
			fmt.Fprintf(w, "error: unable to find manifest")
			return
		}

		// Parse the manifest to find job names
		var manifest map[string]interface{}
		if err := yaml.Unmarshal([]byte(manifestData.Manifest), &manifest); err != nil {
			l.Error("unable to parse manifest: %s", err)
			w.WriteHeader(500)
			fmt.Fprintf(w, "error: unable to parse manifest")
			return
		}

		// Extract instance groups/jobs from manifest
		instanceGroups, ok := manifest["instance_groups"].([]interface{})
		if !ok {
			l.Error("unable to find instance_groups in manifest")
			w.WriteHeader(500)
			fmt.Fprintf(w, "error: unable to find instance groups")
			return
		}

		// Collect all logs from all instance groups
		allLogs := make(map[string]interface{})

		for _, ig := range instanceGroups {
			group, ok := ig.(map[interface{}]interface{})
			if !ok {
				continue
			}

			jobName, ok := group["name"].(string)
			if !ok {
				continue
			}

			instances := 1
			if instCount, ok := group["instances"].(int); ok {
				instances = instCount
			}

			// Fetch logs for each instance in the group
			for i := 0; i < instances; i++ {
				jobIndex := strconv.Itoa(i)
				logKey := fmt.Sprintf("%s/%s", jobName, jobIndex)

				l.Debug("fetching logs for job %s/%s", jobName, jobIndex)

				// Call FetchLogs on the BOSH director
				logs, err := api.Broker.BOSH.FetchLogs(deploymentName, jobName, jobIndex)
				if err != nil {
					l.Error("failed to fetch logs for %s/%s: %s", jobName, jobIndex, err)
					allLogs[logKey] = map[string]interface{}{
						"error": fmt.Sprintf("Failed to fetch logs: %s", err),
					}
				} else {
					// Parse the logs to extract individual files if structured
					// The logs string contains content like "=== filename ===\ncontent\n"
					logFiles := make(map[string]string)
					if strings.Contains(logs, "===") {
						// Parse structured logs using a more robust approach
						// Split by the delimiter pattern to handle files with special characters
						lines := strings.Split(logs, "\n")
						var currentFile string
						var currentContent strings.Builder

						for _, line := range lines {
							if strings.HasPrefix(line, "===") && strings.HasSuffix(line, "===") {
								// Save previous file if exists
								if currentFile != "" {
									logFiles[currentFile] = strings.TrimSpace(currentContent.String())
								}
								// Extract new filename
								currentFile = strings.TrimSpace(strings.Trim(line, "="))
								currentContent.Reset()
							} else if currentFile != "" {
								// Append to current file content
								if currentContent.Len() > 0 {
									currentContent.WriteString("\n")
								}
								currentContent.WriteString(line)
							}
						}
						// Save last file if exists
						if currentFile != "" {
							logFiles[currentFile] = strings.TrimSpace(currentContent.String())
						}
					} else {
						// Single log content
						logFiles["combined.log"] = logs
					}

					allLogs[logKey] = map[string]interface{}{
						"logs":  logs,     // Keep full logs for compatibility
						"files": logFiles, // Structured log files
					}
				}
			}
		}

		// Convert to JSON
		b, err := json.MarshalIndent(allLogs, "", "  ")
		if err != nil {
			w.WriteHeader(500)
			fmt.Fprintf(w, "error marshaling logs: %s", err)
			return
		}

		w.Header().Set("Content-type", "application/json")
		fmt.Fprintf(w, "%s", string(b))
		return
	}

	// Credentials endpoint
	pattern = regexp.MustCompile(`^/b/([^/]+)/creds\.json$`)
	if m := pattern.FindStringSubmatch(req.URL.Path); m != nil {
		l := Logger.Wrap("credentials")
		instanceID := m[1]
		l.Debug("fetching credentials for instance %s", instanceID)

		// Get credentials from vault
		var creds map[string]interface{}
		exists, err := api.Vault.Get(fmt.Sprintf("%s/creds", instanceID), &creds)
		if err != nil || !exists {
			// Try alternate path
			exists, err = api.Vault.Get(fmt.Sprintf("%s/credentials", instanceID), &creds)
			if err != nil || !exists {
				// Return empty object if no credentials found
				creds = make(map[string]interface{})
			}
		}

		// Convert to JSON
		b, err := json.MarshalIndent(creds, "", "  ")
		if err != nil {
			w.WriteHeader(500)
			fmt.Fprintf(w, "error marshaling credentials: %s", err)
			return
		}

		w.Header().Set("Content-type", "application/json")
		fmt.Fprintf(w, "%s", string(b))
		return
	}

	// Handle deployment-specific endpoints for the Blacksmith deployment itself
	if strings.HasPrefix(req.URL.Path, "/b/deployments/") {
		parts := strings.Split(strings.TrimPrefix(req.URL.Path, "/b/deployments/"), "/")
		if len(parts) >= 2 {
			deploymentName := parts[0]

			// For events endpoint
			if len(parts) == 2 && parts[1] == "events" {
				l := Logger.Wrap("deployment-events")
				l.Debug("fetching events for deployment %s", deploymentName)

				// Use the Broker's BOSH director to get events
				events := []interface{}{}

				if api.Broker != nil && api.Broker.BOSH != nil {
					// Get events from BOSH director
					boshEvents, err := api.Broker.BOSH.GetEvents(deploymentName)
					if err != nil {
						l.Error("failed to get events for deployment %s: %s", deploymentName, err)
						// Return empty array on error rather than failing
						events = []interface{}{}
					} else {
						// Convert BOSH events to the format expected by the frontend
						for _, event := range boshEvents {
							events = append(events, map[string]interface{}{
								"id":          event.ID,
								"time":        event.Time,
								"user":        event.User,
								"action":      event.Action,
								"object_type": event.ObjectType,
								"object_name": event.ObjectName,
								"task":        event.Task,
								"task_id":     event.TaskID,
								"deployment":  event.Deployment,
								"instance":    event.Instance,
								"context":     event.Context,
								"error":       event.Error,
							})
						}
					}
				}

				b, err := json.Marshal(events)
				if err != nil {
					w.WriteHeader(500)
					fmt.Fprintf(w, "error marshaling events: %s", err)
					return
				}

				w.Header().Set("Content-type", "application/json")
				fmt.Fprintf(w, "%s", string(b))
				return
			}

			// For manifest endpoint
			if len(parts) == 2 && parts[1] == "manifest" {
				l := Logger.Wrap("deployment-manifest")
				l.Debug("fetching manifest for deployment %s", deploymentName)

				if api.Broker != nil && api.Broker.BOSH != nil {
					// Get deployment manifest from BOSH director
					deployment, err := api.Broker.BOSH.GetDeployment(deploymentName)
					if err != nil {
						l.Error("failed to get deployment %s: %s", deploymentName, err)
						w.WriteHeader(404)
						fmt.Fprintf(w, "deployment not found: %s", err)
						return
					}

					// Return the manifest as plain text
					w.Header().Set("Content-Type", "text/plain")
					if _, err := w.Write([]byte(deployment.Manifest)); err != nil {
						l.Error("failed to write deployment manifest response: %s", err)
					}
					return
				}

				w.WriteHeader(500)
				fmt.Fprintf(w, "BOSH director not available")
				return
			}

			// For manifest-details endpoint (returns both YAML text and parsed JSON)
			if len(parts) == 2 && parts[1] == "manifest-details" {
				l := Logger.Wrap("deployment-manifest-details")
				l.Debug("fetching manifest details for deployment %s", deploymentName)

				if api.Broker != nil && api.Broker.BOSH != nil {
					// Get deployment manifest from BOSH director
					deployment, err := api.Broker.BOSH.GetDeployment(deploymentName)
					if err != nil {
						l.Error("failed to get deployment %s: %s", deploymentName, err)
						w.WriteHeader(404)
						fmt.Fprintf(w, `{"error": "deployment not found"}`)
						return
					}

					// Parse the YAML manifest into a generic structure
					var manifestParsed interface{}
					if err := yaml.Unmarshal([]byte(deployment.Manifest), &manifestParsed); err != nil {
						l.Error("unable to parse deployment manifest YAML: %s", err)
						w.WriteHeader(500)
						fmt.Fprintf(w, `{"error": "unable to parse manifest"}`)
						return
					}

					// Convert map[interface{}]interface{} to map[string]interface{} for JSON compatibility
					manifestParsed = convertToJSONCompatible(manifestParsed)

					// Create response with both text and parsed JSON
					response := map[string]interface{}{
						"text":   deployment.Manifest,
						"parsed": manifestParsed,
					}

					w.Header().Set("Content-Type", "application/json")
					b, err := json.Marshal(response)
					if err != nil {
						l.Error("unable to marshal manifest details: %s", err)
						w.WriteHeader(500)
						fmt.Fprintf(w, `{"error": "unable to marshal response"}`)
						return
					}
					if _, err := w.Write(b); err != nil {
						l.Error("failed to write manifest details response: %s", err)
					}
					return
				}

				w.WriteHeader(500)
				fmt.Fprintf(w, `{"error": "BOSH director not available"}`)
				return
			}

			// For task log endpoints
			if len(parts) == 4 && parts[1] == "tasks" && (parts[3] == "log" || parts[3] == "debug") {
				taskID := parts[2]
				logType := parts[3]

				l := Logger.Wrap("deployment-task-log")
				l.Debug("fetching %s log for deployment %s, task %s", logType, deploymentName, taskID)

				// Fetch task logs from BOSH
				logs := []interface{}{}

				if api.Broker != nil && api.Broker.BOSH != nil {
					// Convert taskID to int
					tid, err := strconv.Atoi(taskID)
					if err != nil {
						l.Error("invalid task ID %s: %s", taskID, err)
						w.WriteHeader(400)
						fmt.Fprintf(w, "invalid task ID: %s", taskID)
						return
					}

					// Handle output based on logType
					var output string
					if logType == "debug" {
						// For debug, get debug output directly
						debugOutput, err := api.Broker.BOSH.GetTaskOutput(tid, "debug")
						if err != nil {
							l.Error("failed to get task debug output for task %d: %s", tid, err)
							// Return empty logs array rather than error
							logs = []interface{}{}
						} else {
							output = debugOutput
						}
					} else {
						// For deployment log, try "event" output first, then fall back to "result"
						eventOutput, err := api.Broker.BOSH.GetTaskOutput(tid, "event")
						if err != nil {
							l.Debug("unable to get task event output for task %d: %s", tid, err)
							eventOutput = ""
						}

						if eventOutput != "" {
							l.Debug("using event output for task %d (size: %d bytes)", tid, len(eventOutput))
							output = eventOutput
						} else {
							// Fall back to result output
							resultOutput, err := api.Broker.BOSH.GetTaskOutput(tid, "result")
							if err != nil {
								l.Error("unable to get task result output for task %d: %s", tid, err)
								// Return empty logs array rather than error
								logs = []interface{}{}
							} else {
								l.Debug("using result output for task %d (size: %d bytes)", tid, len(resultOutput))
								output = resultOutput
							}
						}
					}

					if output != "" {
						var taskEvents []bosh.TaskEvent

						// Parse output based on type
						if logType == "debug" {
							// Use debug parser for debug output
							taskEvents = parseDebugLogToEvents(output)
						} else {
							// Use result parser for deployment log
							taskEvents = parseResultOutputToEvents(output)
						}

						// Convert task events to log format expected by frontend
						for _, event := range taskEvents {
							// Log timestamp for debugging
							timestamp := event.Time.Unix()
							if timestamp == 0 {
								l.Debug("Warning: Task event has zero timestamp - stage: %s, task: %s", event.Stage, event.Task)
							}

							logEntry := map[string]interface{}{
								"time":     timestamp,
								"stage":    event.Stage,
								"task":     event.Task,
								"index":    event.Index,
								"total":    event.Total,
								"state":    event.State,
								"progress": event.Progress,
								"tags":     event.Tags,
								"data":     event.Data,
							}
							logs = append(logs, logEntry)
						}

						// If no events were parsed, treat as plain text
						if len(logs) == 0 {
							lines := strings.Split(output, "\n")
							for i, line := range lines {
								if line != "" {
									logEntry := map[string]interface{}{
										"time":     time.Now().Unix() - int64(len(lines)-i),
										"stage":    "Task " + taskID,
										"task":     line,
										"index":    i + 1,
										"total":    len(lines),
										"state":    "finished",
										"progress": 100,
										"tags":     []string{deploymentName},
										"data": map[string]interface{}{
											"status": "done",
										},
									}
									logs = append(logs, logEntry)
								}
							}
						}
					}
				}

				b, err := json.Marshal(logs)
				if err != nil {
					w.WriteHeader(500)
					fmt.Fprintf(w, "error marshaling logs: %s", err)
					return
				}

				w.Header().Set("Content-type", "application/json")
				fmt.Fprintf(w, "%s", string(b))
				return
			}
		}
	}

	// Default 404 handler for unmatched routes
	Logger.Debug("No handler found for path: %s", req.URL.Path)
	w.WriteHeader(404)
}

// handleCFRegistrationEndpoints handles all CF registration related endpoints
func (api *InternalApi) handleCFRegistrationEndpoints(w http.ResponseWriter, req *http.Request) {
	l := Logger.Wrap("cf-registration-api")

	// Remove the /b/cf prefix to get the actual path
	path := strings.TrimPrefix(req.URL.Path, "/b/cf")
	l.Debug("handling CF registration endpoint: %s %s", req.Method, path)

	switch {
	// GET /b/cf/registrations - List all registrations
	case path == "/registrations" && req.Method == "GET":
		api.listCFRegistrations(w, req)

	// POST /b/cf/registrations - Create new registration
	case path == "/registrations" && req.Method == "POST":
		api.createCFRegistration(w, req)

	// GET /b/cf/registrations/{id} - Get specific registration
	case strings.HasPrefix(path, "/registrations/") && req.Method == "GET":
		parts := strings.Split(strings.TrimPrefix(path, "/registrations/"), "/")
		if len(parts) == 1 && parts[0] != "" {
			api.getCFRegistration(w, req, parts[0])
		} else {
			w.WriteHeader(404)
			fmt.Fprintf(w, `{"error": "registration not found"}`)
		}

	// PUT /b/cf/registrations/{id} - Update registration
	case strings.HasPrefix(path, "/registrations/") && req.Method == "PUT":
		parts := strings.Split(strings.TrimPrefix(path, "/registrations/"), "/")
		if len(parts) == 1 && parts[0] != "" {
			api.updateCFRegistration(w, req, parts[0])
		} else {
			w.WriteHeader(404)
			fmt.Fprintf(w, `{"error": "registration not found"}`)
		}

	// DELETE /b/cf/registrations/{id} - Delete registration
	case strings.HasPrefix(path, "/registrations/") && req.Method == "DELETE":
		parts := strings.Split(strings.TrimPrefix(path, "/registrations/"), "/")
		if len(parts) == 1 && parts[0] != "" {
			api.deleteCFRegistration(w, req, parts[0])
		} else {
			w.WriteHeader(404)
			fmt.Fprintf(w, `{"error": "registration not found"}`)
		}

	// POST /b/cf/test-connection - Test CF connection
	case path == "/test-connection" && req.Method == "POST":
		api.testCFConnection(w, req)

	// GET /b/cf/registrations/{id}/stream - Progress streaming endpoint
	case regexp.MustCompile(`^/registrations/([^/]+)/stream$`).MatchString(path) && req.Method == "GET":
		parts := regexp.MustCompile(`^/registrations/([^/]+)/stream$`).FindStringSubmatch(path)
		if len(parts) == 2 {
			api.streamCFRegistrationProgress(w, req, parts[1])
		} else {
			w.WriteHeader(404)
			fmt.Fprintf(w, `{"error": "invalid stream endpoint"}`)
		}

	// POST /b/cf/registrations/{id}/sync - Sync registration
	case regexp.MustCompile(`^/registrations/([^/]+)/sync$`).MatchString(path) && req.Method == "POST":
		parts := regexp.MustCompile(`^/registrations/([^/]+)/sync$`).FindStringSubmatch(path)
		if len(parts) == 2 {
			api.syncCFRegistration(w, req, parts[1])
		} else {
			w.WriteHeader(404)
			fmt.Fprintf(w, `{"error": "invalid sync endpoint"}`)
		}

	// POST /b/cf/registrations/{id}/register - Start registration process
	case regexp.MustCompile(`^/registrations/([^/]+)/register$`).MatchString(path) && req.Method == "POST":
		parts := regexp.MustCompile(`^/registrations/([^/]+)/register$`).FindStringSubmatch(path)
		if len(parts) == 2 {
			api.startCFRegistrationProcess(w, req, parts[1])
		} else {
			w.WriteHeader(404)
			fmt.Fprintf(w, `{"error": "invalid registration endpoint"}`)
		}

	// --- CF API Endpoints Management ---

	// GET /b/cf/endpoints - List configured CF API endpoints
	case path == "/endpoints" && req.Method == "GET":
		api.listCFEndpoints(w, req)

	// POST /b/cf/endpoints/{name}/connect - Connect to CF endpoint
	case regexp.MustCompile(`^/endpoints/([^/]+)/connect$`).MatchString(path) && req.Method == "POST":
		parts := regexp.MustCompile(`^/endpoints/([^/]+)/connect$`).FindStringSubmatch(path)
		if len(parts) == 2 {
			api.connectCFEndpoint(w, req, parts[1])
		} else {
			w.WriteHeader(404)
			fmt.Fprintf(w, `{"error": "invalid endpoint"}`)
		}

	// GET /b/cf/endpoints/{name}/marketplace - Get marketplace services for a CF endpoint
	case regexp.MustCompile(`^/endpoints/([^/]+)/marketplace$`).MatchString(path) && req.Method == "GET":
		parts := regexp.MustCompile(`^/endpoints/([^/]+)/marketplace$`).FindStringSubmatch(path)
		if len(parts) == 2 {
			api.getCFMarketplace(w, req, parts[1])
		} else {
			w.WriteHeader(404)
			fmt.Fprintf(w, `{"error": "invalid endpoint"}`)
		}

	// GET /b/cf/endpoints/{name}/orgs - Get organizations for a CF endpoint
	case regexp.MustCompile(`^/endpoints/([^/]+)/orgs$`).MatchString(path) && req.Method == "GET":
		parts := regexp.MustCompile(`^/endpoints/([^/]+)/orgs$`).FindStringSubmatch(path)
		if len(parts) == 2 {
			api.getCFOrganizations(w, req, parts[1])
		} else {
			w.WriteHeader(404)
			fmt.Fprintf(w, `{"error": "invalid endpoint"}`)
		}

	// GET /b/cf/endpoints/{name}/orgs/{org_guid}/spaces - Get spaces for a CF org
	case regexp.MustCompile(`^/endpoints/([^/]+)/orgs/([^/]+)/spaces$`).MatchString(path) && req.Method == "GET":
		parts := regexp.MustCompile(`^/endpoints/([^/]+)/orgs/([^/]+)/spaces$`).FindStringSubmatch(path)
		if len(parts) == 3 {
			api.getCFSpaces(w, req, parts[1], parts[2])
		} else {
			w.WriteHeader(404)
			fmt.Fprintf(w, `{"error": "invalid endpoint"}`)
		}

	// GET /b/cf/endpoints/{name}/orgs/{org_guid}/spaces/{space_guid}/services - Get services for a CF space
	case regexp.MustCompile(`^/endpoints/([^/]+)/orgs/([^/]+)/spaces/([^/]+)/services$`).MatchString(path) && req.Method == "GET":
		parts := regexp.MustCompile(`^/endpoints/([^/]+)/orgs/([^/]+)/spaces/([^/]+)/services$`).FindStringSubmatch(path)
		if len(parts) == 4 {
			api.getCFServices(w, req, parts[1], parts[2], parts[3])
		} else {
			w.WriteHeader(404)
			fmt.Fprintf(w, `{"error": "invalid endpoint"}`)
		}

	// GET /b/cf/endpoints/{name}/orgs/{org_guid}/spaces/{space_guid}/service_instances/{service_guid}/bindings - Get bindings for a service instance
	case regexp.MustCompile(`^/endpoints/([^/]+)/orgs/([^/]+)/spaces/([^/]+)/service_instances/([^/]+)/bindings$`).MatchString(path) && req.Method == "GET":
		parts := regexp.MustCompile(`^/endpoints/([^/]+)/orgs/([^/]+)/spaces/([^/]+)/service_instances/([^/]+)/bindings$`).FindStringSubmatch(path)
		if len(parts) == 5 {
			api.getCFServiceBindings(w, req, parts[1], parts[2], parts[3], parts[4])
		} else {
			w.WriteHeader(404)
			fmt.Fprintf(w, `{"error": "invalid endpoint"}`)
		}

	default:
		l.Debug("unknown CF registration endpoint: %s %s", req.Method, path)
		w.WriteHeader(404)
		fmt.Fprintf(w, `{"error": "endpoint not found"}`)
	}
}

// listCFRegistrations handles GET /b/cf/registrations
func (api *InternalApi) listCFRegistrations(w http.ResponseWriter, req *http.Request) {
	l := Logger.Wrap("cf-list-registrations")
	l.Debug("listing CF registrations")

	registrations, err := api.Vault.ListCFRegistrations()
	if err != nil {
		l.Error("failed to list CF registrations: %s", err)
		api.handleJSONResponse(w, nil, fmt.Errorf("failed to list registrations: %w", err))
		return
	}

	l.Debug("found %d CF registrations", len(registrations))
	api.handleJSONResponse(w, map[string]interface{}{
		"registrations": registrations,
		"count":         len(registrations),
	}, nil)
}

// createCFRegistration handles POST /b/cf/registrations
func (api *InternalApi) createCFRegistration(w http.ResponseWriter, req *http.Request) {
	l := Logger.Wrap("cf-create-registration")
	l.Debug("creating CF registration")

	// Apply rate limiting and security validation
	if err := api.SecurityMiddleware.ValidateRequest("cf-registrations", "create", nil); err != nil {
		l.Error("rate limit exceeded for CF registration creation: %s", err)
		if api.SecurityMiddleware.HandleSecurityError(w, err) {
			return // Error was handled by middleware
		}
		api.handleJSONResponse(w, nil, fmt.Errorf("security validation failed: %w", err))
		return
	}

	var regReq cf.RegistrationRequest
	if err := json.NewDecoder(req.Body).Decode(&regReq); err != nil {
		l.Error("invalid registration request: %s", err)
		api.handleJSONResponse(w, nil, fmt.Errorf("invalid request body: %w", err))
		return
	}

	// Sanitize input data
	cf.SanitizeRegistrationRequest(&regReq)

	// Validate input data
	if err := cf.ValidateRegistrationRequest(&regReq); err != nil {
		l.Error("registration request validation failed: %s", err)
		api.handleJSONResponse(w, nil, fmt.Errorf("validation failed: %w", err))
		return
	}

	// Generate registration ID
	registrationID := generateID()

	// Create CF registration object
	registration := map[string]interface{}{
		"id":          registrationID,
		"name":        regReq.Name,
		"api_url":     regReq.APIURL,
		"username":    regReq.Username,
		"broker_name": regReq.BrokerName,
		"status":      "created",
		"created_at":  time.Now().Format(time.RFC3339),
		"updated_at":  time.Now().Format(time.RFC3339),
	}

	// Save to Vault
	if err := api.Vault.SaveCFRegistration(registration); err != nil {
		l.Error("failed to save CF registration: %s", err)
		api.handleJSONResponse(w, nil, fmt.Errorf("failed to save registration: %w", err))
		return
	}

	l.Info("created CF registration %s (%s)", registrationID, regReq.Name)

	// Audit log the registration creation
	api.SecurityMiddleware.LogOperation("system", registrationID, "cf", "create_registration",
		map[string]interface{}{
			"name":          regReq.Name,
			"api_url":       regReq.APIURL,
			"username":      regReq.Username,
			"auto_register": regReq.AutoRegister,
		}, registration, nil)

	// Start async registration process if requested
	if regReq.AutoRegister {
		go api.performAsyncCFRegistration(registrationID, &regReq)
	}

	api.handleJSONResponse(w, registration, nil)
}

// getCFRegistration handles GET /b/cf/registrations/{id}
func (api *InternalApi) getCFRegistration(w http.ResponseWriter, req *http.Request, registrationID string) {
	l := Logger.Wrap("cf-get-registration")
	l.Debug("getting CF registration %s", registrationID)

	var registration map[string]interface{}
	exists, err := api.Vault.GetCFRegistration(registrationID, &registration)
	if err != nil {
		l.Error("failed to get CF registration %s: %s", registrationID, err)
		api.handleJSONResponse(w, nil, fmt.Errorf("failed to get registration: %w", err))
		return
	}

	if !exists {
		l.Debug("CF registration %s not found", registrationID)
		w.WriteHeader(404)
		fmt.Fprintf(w, `{"error": "registration not found"}`)
		return
	}

	l.Debug("found CF registration %s", registrationID)
	api.handleJSONResponse(w, registration, nil)
}

// updateCFRegistration handles PUT /b/cf/registrations/{id}
func (api *InternalApi) updateCFRegistration(w http.ResponseWriter, req *http.Request, registrationID string) {
	l := Logger.Wrap("cf-update-registration")
	l.Debug("updating CF registration %s", registrationID)

	// Get existing registration
	var existingReg map[string]interface{}
	exists, err := api.Vault.GetCFRegistration(registrationID, &existingReg)
	if err != nil {
		l.Error("failed to get existing registration %s: %s", registrationID, err)
		api.handleJSONResponse(w, nil, fmt.Errorf("failed to get existing registration: %w", err))
		return
	}

	if !exists {
		l.Debug("CF registration %s not found for update", registrationID)
		w.WriteHeader(404)
		fmt.Fprintf(w, `{"error": "registration not found"}`)
		return
	}

	// Parse update request
	var updateReq map[string]interface{}
	if err := json.NewDecoder(req.Body).Decode(&updateReq); err != nil {
		l.Error("invalid update request: %s", err)
		api.handleJSONResponse(w, nil, fmt.Errorf("invalid request body: %w", err))
		return
	}

	// Update allowed fields with validation
	allowedFields := []string{"name", "api_url", "username", "broker_name", "metadata"}
	for _, field := range allowedFields {
		if value, exists := updateReq[field]; exists {
			// Validate each field being updated
			if err := api.validateRegistrationField(field, value); err != nil {
				l.Error("validation failed for field %s: %s", field, err)
				api.handleJSONResponse(w, nil, fmt.Errorf("validation failed for %s: %w", field, err))
				return
			}
			existingReg[field] = value
		}
	}

	// Update timestamp
	existingReg["updated_at"] = time.Now().Format(time.RFC3339)

	// Save updated registration
	if err := api.Vault.SaveCFRegistration(existingReg); err != nil {
		l.Error("failed to update CF registration %s: %s", registrationID, err)
		api.handleJSONResponse(w, nil, fmt.Errorf("failed to update registration: %w", err))
		return
	}

	l.Info("updated CF registration %s", registrationID)

	// Audit log the registration update
	api.SecurityMiddleware.LogOperation("system", registrationID, "cf", "update_registration",
		updateReq, existingReg, nil)

	api.handleJSONResponse(w, existingReg, nil)
}

// deleteCFRegistration handles DELETE /b/cf/registrations/{id}
func (api *InternalApi) deleteCFRegistration(w http.ResponseWriter, req *http.Request, registrationID string) {
	l := Logger.Wrap("cf-delete-registration")
	l.Debug("deleting CF registration %s", registrationID)

	// Check if registration exists
	var registration map[string]interface{}
	exists, err := api.Vault.GetCFRegistration(registrationID, &registration)
	if err != nil {
		l.Error("failed to check CF registration %s: %s", registrationID, err)
		api.handleJSONResponse(w, nil, fmt.Errorf("failed to check registration: %w", err))
		return
	}

	if !exists {
		l.Debug("CF registration %s not found for deletion", registrationID)
		w.WriteHeader(404)
		fmt.Fprintf(w, `{"error": "registration not found"}`)
		return
	}

	// Delete from Vault
	if err := api.Vault.DeleteCFRegistration(registrationID); err != nil {
		l.Error("failed to delete CF registration %s: %s", registrationID, err)
		api.handleJSONResponse(w, nil, fmt.Errorf("failed to delete registration: %w", err))
		return
	}

	l.Info("deleted CF registration %s", registrationID)

	// Audit log the registration deletion
	api.SecurityMiddleware.LogOperation("system", registrationID, "cf", "delete_registration",
		nil, map[string]interface{}{"id": registrationID}, nil)

	api.handleJSONResponse(w, map[string]interface{}{
		"message": "registration deleted successfully",
		"id":      registrationID,
	}, nil)
}

// validateRegistrationField validates individual registration fields during updates
func (api *InternalApi) validateRegistrationField(fieldName string, value interface{}) error {
	strValue, ok := value.(string)
	if !ok {
		return fmt.Errorf("field value must be a string")
	}

	switch fieldName {
	case "name":
		return cf.ValidateName(strValue)
	case "api_url":
		return cf.ValidateURL(strValue)
	case "username":
		return cf.ValidateUsername(strValue)
	case "broker_name":
		if strValue != "" {
			return cf.ValidateBrokerName(strValue)
		}
		return nil
	case "metadata":
		// Metadata validation handled separately
		return nil
	default:
		return fmt.Errorf("field %s is not allowed for updates", fieldName)
	}
}

// validateTestConnectionRequest validates CF connection test requests
func (api *InternalApi) validateTestConnectionRequest(testReq *cf.RegistrationTest) error {
	if testReq == nil {
		return fmt.Errorf("test request cannot be nil")
	}

	// Validate required fields
	if strings.TrimSpace(testReq.APIURL) == "" {
		return fmt.Errorf("API URL is required")
	}

	if strings.TrimSpace(testReq.Username) == "" {
		return fmt.Errorf("username is required")
	}

	if strings.TrimSpace(testReq.Password) == "" {
		return fmt.Errorf("password is required")
	}

	// Validate URL format
	if err := cf.ValidateURL(testReq.APIURL); err != nil {
		return fmt.Errorf("invalid API URL: %w", err)
	}

	// Validate username format
	if err := cf.ValidateUsername(testReq.Username); err != nil {
		return fmt.Errorf("invalid username: %w", err)
	}

	// Validate password
	if err := cf.ValidatePassword(testReq.Password); err != nil {
		return fmt.Errorf("invalid password: %w", err)
	}

	return nil
}

// testCFConnection handles POST /b/cf/test-connection
func (api *InternalApi) testCFConnection(w http.ResponseWriter, req *http.Request) {
	l := Logger.Wrap("cf-test-connection")
	l.Debug("testing CF connection")

	// Apply rate limiting for connection tests (more restrictive)
	if err := api.SecurityMiddleware.ValidateRequest("cf-connections", "test", nil); err != nil {
		l.Error("rate limit exceeded for CF connection test: %s", err)
		if api.SecurityMiddleware.HandleSecurityError(w, err) {
			return // Error was handled by middleware
		}
		api.handleJSONResponse(w, nil, fmt.Errorf("security validation failed: %w", err))
		return
	}

	var testReq cf.RegistrationTest
	if err := json.NewDecoder(req.Body).Decode(&testReq); err != nil {
		l.Error("invalid test request: %s", err)
		api.handleJSONResponse(w, nil, fmt.Errorf("invalid request body: %w", err))
		return
	}

	// Validate test connection request
	if err := api.validateTestConnectionRequest(&testReq); err != nil {
		l.Error("test connection request validation failed: %s", err)
		api.handleJSONResponse(w, nil, fmt.Errorf("validation failed: %w", err))
		return
	}

	// Use CF handler to test connection
	result, err := api.Services.CF.TestConnection(&testReq)
	if err != nil {
		l.Error("CF connection test failed: %s", err)
		api.handleJSONResponse(w, nil, fmt.Errorf("connection test failed: %w", err))
		return
	}

	l.Debug("CF connection test completed: success=%t", result.Success)
	api.handleJSONResponse(w, result, nil)
}

// streamCFRegistrationProgress handles GET /b/cf/registrations/{id}/stream
func (api *InternalApi) streamCFRegistrationProgress(w http.ResponseWriter, req *http.Request, registrationID string) {
	l := Logger.Wrap("cf-stream-progress")
	l.Debug("streaming progress for CF registration %s", registrationID)

	// Set headers for Server-Sent Events
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	// Get flusher for streaming
	flusher, ok := w.(http.Flusher)
	if !ok {
		l.Error("streaming not supported")
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	// Check if registration exists
	var registration map[string]interface{}
	exists, err := api.Vault.GetCFRegistration(registrationID, &registration)
	if err != nil || !exists {
		l.Debug("CF registration %s not found", registrationID)
		http.Error(w, "registration not found", http.StatusNotFound)
		return
	}

	// Get current registration status
	status := getStringFromMap(registration, "status")
	lastError := getStringFromMap(registration, "last_error")

	// Send initial status
	initialEvent := map[string]interface{}{
		"step":      "status",
		"status":    status,
		"message":   fmt.Sprintf("Current status: %s", status),
		"timestamp": time.Now().Format(time.RFC3339),
	}
	if lastError != "" {
		initialEvent["error"] = lastError
	}

	eventData, _ := json.Marshal(initialEvent)
	fmt.Fprintf(w, "data: %s\n\n", string(eventData))
	flusher.Flush()

	// If registration is completed or failed, send final event and close
	if status == "active" || status == "failed" {
		finalEvent := map[string]interface{}{
			"step":      "completed",
			"status":    status,
			"message":   fmt.Sprintf("Registration %s", status),
			"timestamp": time.Now().Format(time.RFC3339),
		}
		if status == "failed" && lastError != "" {
			finalEvent["error"] = lastError
		}

		eventData, _ := json.Marshal(finalEvent)
		fmt.Fprintf(w, "data: %s\n\n", string(eventData))
		flusher.Flush()

		l.Debug("completed streaming for CF registration %s (final status: %s)", registrationID, status)
		return
	}

	// For in-progress registrations, stream updates by polling
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	timeout := time.After(30 * time.Second) // 30 second timeout

	for {
		select {
		case <-ticker.C:
			// Check for updated status
			exists, err := api.Vault.GetCFRegistration(registrationID, &registration)
			if err != nil || !exists {
				l.Debug("registration %s no longer exists, ending stream", registrationID)
				return
			}

			currentStatus := getStringFromMap(registration, "status")
			if currentStatus != status {
				// Status changed, send update
				status = currentStatus
				statusEvent := map[string]interface{}{
					"step":      "status_update",
					"status":    status,
					"message":   fmt.Sprintf("Status changed to: %s", status),
					"timestamp": time.Now().Format(time.RFC3339),
				}

				eventData, _ := json.Marshal(statusEvent)
				fmt.Fprintf(w, "data: %s\n\n", string(eventData))
				flusher.Flush()

				// If completed, send final event and close
				if status == "active" || status == "failed" {
					finalEvent := map[string]interface{}{
						"step":      "completed",
						"status":    status,
						"message":   fmt.Sprintf("Registration %s", status),
						"timestamp": time.Now().Format(time.RFC3339),
					}
					if status == "failed" {
						finalEvent["error"] = getStringFromMap(registration, "last_error")
					}

					eventData, _ := json.Marshal(finalEvent)
					fmt.Fprintf(w, "data: %s\n\n", string(eventData))
					flusher.Flush()

					l.Debug("completed streaming for CF registration %s (final status: %s)", registrationID, status)
					return
				}
			}

		case <-timeout:
			// Timeout reached
			timeoutEvent := map[string]interface{}{
				"step":      "timeout",
				"status":    "timeout",
				"message":   "Stream timeout reached",
				"timestamp": time.Now().Format(time.RFC3339),
			}

			eventData, _ := json.Marshal(timeoutEvent)
			fmt.Fprintf(w, "data: %s\n\n", string(eventData))
			flusher.Flush()

			l.Debug("stream timeout for CF registration %s", registrationID)
			return

		case <-req.Context().Done():
			// Client disconnected
			l.Debug("client disconnected from CF registration %s stream", registrationID)
			return
		}
	}
}

// syncCFRegistration handles POST /b/cf/registrations/{id}/sync
func (api *InternalApi) syncCFRegistration(w http.ResponseWriter, req *http.Request, registrationID string) {
	l := Logger.Wrap("cf-sync-registration")
	l.Debug("syncing CF registration %s", registrationID)

	// Get existing registration
	var registration map[string]interface{}
	exists, err := api.Vault.GetCFRegistration(registrationID, &registration)
	if err != nil {
		l.Error("failed to get CF registration %s: %s", registrationID, err)
		api.handleJSONResponse(w, nil, fmt.Errorf("failed to get registration: %w", err))
		return
	}

	if !exists {
		l.Debug("CF registration %s not found for sync", registrationID)
		w.WriteHeader(404)
		fmt.Fprintf(w, `{"error": "registration not found"}`)
		return
	}

	// Convert to CF registration object
	cfReg := &cf.CFRegistration{
		ID:         registrationID,
		APIURL:     getStringFromMap(registration, "api_url"),
		Username:   getStringFromMap(registration, "username"),
		BrokerName: getStringFromMap(registration, "broker_name"),
	}

	// Create sync request
	syncReq := &cf.SyncRequest{
		RegistrationID: registrationID,
	}

	// Perform sync using CF handler
	result, err := api.Services.CF.SyncRegistration(syncReq, cfReg)
	if err != nil {
		l.Error("CF registration sync failed: %s", err)
		api.handleJSONResponse(w, nil, fmt.Errorf("sync failed: %w", err))
		return
	}

	// Update registration status based on sync result
	if result.Success {
		err = api.Vault.UpdateCFRegistrationStatus(registrationID, "active", "")
	} else {
		err = api.Vault.UpdateCFRegistrationStatus(registrationID, "error", result.Error)
	}

	if err != nil {
		l.Error("failed to update registration status: %s", err)
		// Don't fail the request, just log the error
	}

	l.Debug("CF registration sync completed: success=%t", result.Success)
	api.handleJSONResponse(w, result, nil)
}

// startCFRegistrationProcess handles POST /b/cf/registrations/{id}/register
func (api *InternalApi) startCFRegistrationProcess(w http.ResponseWriter, req *http.Request, registrationID string) {
	l := Logger.Wrap("cf-start-registration")
	l.Debug("starting CF registration process for %s", registrationID)

	// Get existing registration
	var registration map[string]interface{}
	exists, err := api.Vault.GetCFRegistration(registrationID, &registration)
	if err != nil {
		l.Error("failed to get CF registration %s: %s", registrationID, err)
		api.handleJSONResponse(w, nil, fmt.Errorf("failed to get registration: %w", err))
		return
	}

	if !exists {
		l.Debug("CF registration %s not found", registrationID)
		w.WriteHeader(404)
		fmt.Fprintf(w, `{"error": "registration not found"}`)
		return
	}

	// Check current status
	status := getStringFromMap(registration, "status")
	if status == "registering" || status == "active" {
		l.Debug("CF registration %s already in progress or completed (status: %s)", registrationID, status)
		api.handleJSONResponse(w, map[string]interface{}{
			"message": fmt.Sprintf("Registration already %s", status),
			"status":  status,
		}, nil)
		return
	}

	// Convert to CF registration request for processing
	regReq := &cf.RegistrationRequest{
		ID:         registrationID,
		Name:       getStringFromMap(registration, "name"),
		APIURL:     getStringFromMap(registration, "api_url"),
		Username:   getStringFromMap(registration, "username"),
		BrokerName: getStringFromMap(registration, "broker_name"),
	}

	// Parse optional request body for password
	var requestData map[string]interface{}
	if req.Body != nil {
		if err := json.NewDecoder(req.Body).Decode(&requestData); err == nil {
			if password, ok := requestData["password"].(string); ok {
				regReq.Password = password
			}
		}
	}

	// Start async registration process
	go api.performAsyncCFRegistration(registrationID, regReq)

	// Update status to registering
	err = api.Vault.UpdateCFRegistrationStatus(registrationID, "registering", "")
	if err != nil {
		l.Error("failed to update registration status: %s", err)
	}

	l.Info("started CF registration process for %s", registrationID)
	api.handleJSONResponse(w, map[string]interface{}{
		"message": "Registration process started",
		"status":  "registering",
		"id":      registrationID,
	}, nil)
}

// performAsyncCFRegistration performs the actual CF registration in the background
func (api *InternalApi) performAsyncCFRegistration(registrationID string, regReq *cf.RegistrationRequest) {
	l := Logger.Wrap("cf-async-registration")
	l.Info("starting async CF registration for %s", registrationID)

	// Update status to registering
	err := api.Vault.UpdateCFRegistrationStatus(registrationID, "registering", "")
	if err != nil {
		l.Error("failed to update status to registering: %s", err)
	}

	// Create progress channel for tracking
	progressChan := make(chan cf.RegistrationProgress, 100)

	// Start progress tracking in a separate goroutine
	go api.trackRegistrationProgress(registrationID, progressChan)

	// Perform the registration
	err = api.Services.CF.PerformRegistration(regReq, progressChan)

	// Update final status based on result
	if err != nil {
		l.Error("CF registration failed for %s: %s", registrationID, err)
		statusErr := api.Vault.UpdateCFRegistrationStatus(registrationID, "failed", err.Error())
		if statusErr != nil {
			l.Error("failed to update registration status to failed: %s", statusErr)
		}
	} else {
		l.Info("CF registration completed successfully for %s", registrationID)
		statusErr := api.Vault.UpdateCFRegistrationStatus(registrationID, "active", "")
		if statusErr != nil {
			l.Error("failed to update registration status to active: %s", statusErr)
		}
	}
}

// trackRegistrationProgress tracks the registration progress and stores it
func (api *InternalApi) trackRegistrationProgress(registrationID string, progressChan <-chan cf.RegistrationProgress) {
	l := Logger.Wrap("cf-progress-tracker")
	l.Debug("starting progress tracking for registration %s", registrationID)

	// Store progress updates in Vault for streaming
	progressPath := fmt.Sprintf("secret/blacksmith/registrations/%s/progress", registrationID)

	for progress := range progressChan {
		l.Debug("registration %s progress: %s - %s", registrationID, progress.Step, progress.Message)

		// Store progress in Vault
		progressData := map[string]interface{}{
			"step":      progress.Step,
			"status":    progress.Status,
			"message":   progress.Message,
			"timestamp": progress.Timestamp.Format(time.RFC3339),
		}

		err := api.Vault.Put(progressPath, progressData)
		if err != nil {
			l.Error("failed to store progress for registration %s: %s", registrationID, err)
		}
	}

	l.Debug("completed progress tracking for registration %s", registrationID)
}

// --- CF API Endpoints Management Methods ---

// listCFEndpoints handles GET /b/cf/endpoints
func (api *InternalApi) listCFEndpoints(w http.ResponseWriter, req *http.Request) {
	l := Logger.Wrap("cf-list-endpoints")
	l.Debug("listing CF API endpoints")

	// Check if CF manager is initialized
	if api.CFManager == nil {
		api.handleJSONResponse(w, map[string]interface{}{
			"endpoints": map[string]interface{}{},
			"message":   "CF functionality disabled - no CF endpoints configured",
		}, nil)
		return
	}

	// Get CF API endpoints from configuration
	endpoints := make(map[string]interface{})
	for name, config := range api.Config.Broker.CF.APIs {
		endpoints[name] = map[string]interface{}{
			"name":     config.Name,
			"endpoint": config.Endpoint,
			"username": config.Username,
			// Don't expose password in the response
		}
	}

	// Get health status from CF manager
	status := api.CFManager.GetStatus()

	api.handleJSONResponse(w, map[string]interface{}{
		"endpoints": endpoints,
		"status":    status,
	}, nil)
}

// getCFMarketplace handles GET /b/cf/endpoints/{name}/marketplace
func (api *InternalApi) getCFMarketplace(w http.ResponseWriter, req *http.Request, endpointName string) {
	l := Logger.Wrap("cf-get-marketplace")
	l.Debug("getting marketplace services for CF endpoint %s", endpointName)

	// Check if CF manager is initialized
	if api.CFManager == nil {
		api.handleJSONResponse(w, nil, fmt.Errorf("CF functionality disabled - no CF endpoints configured"))
		return
	}

	// Get CF client through CF manager (handles health checks and retries)
	client, err := api.CFManager.GetClient(endpointName)
	if err != nil {
		l.Error("failed to get CF client for endpoint %s: %s", endpointName, err)
		api.handleJSONResponse(w, nil, fmt.Errorf("failed to connect to CF endpoint '%s': %w", endpointName, err))
		return
	}

	// Get service offerings (marketplace services)
	ctx := context.Background()
	params := capi.NewQueryParams().WithPerPage(100)
	response, err := client.ServiceOfferings().List(ctx, params)
	if err != nil {
		l.Error("failed to get service offerings from CF endpoint %s: %s", endpointName, err)
		api.handleJSONResponse(w, nil, fmt.Errorf("failed to get marketplace services from CF: %w", err))
		return
	}

	// Convert to interface{} for JSON response
	services := make([]interface{}, len(response.Resources))
	for i, service := range response.Resources {
		// Get service plans for this service
		planParams := capi.NewQueryParams().WithFilter("service_offering_guids", service.GUID)
		plansResponse, err := client.ServicePlans().List(ctx, planParams)
		if err != nil {
			l.Error("failed to get plans for service %s: %s", service.Name, err)
			plansResponse = &capi.ListResponse[capi.ServicePlan]{Resources: make([]capi.ServicePlan, 0)} // Continue with empty plans
		}

		plansList := make([]interface{}, len(plansResponse.Resources))
		for j, plan := range plansResponse.Resources {
			plansList[j] = map[string]interface{}{
				"name":        plan.Name,
				"description": plan.Description,
				"free":        plan.Free,
				"guid":        plan.GUID,
			}
		}

		services[i] = map[string]interface{}{
			"name":        service.Name,
			"description": service.Description,
			"guid":        service.GUID,
			"plans":       plansList,
			"available":   service.Available,
			"shareable":   service.Shareable,
			"tags":        service.Tags,
		}
	}

	api.handleJSONResponse(w, map[string]interface{}{
		"endpoint": endpointName,
		"services": services,
	}, nil)
}

// getCFOrganizations handles GET /b/cf/endpoints/{name}/orgs
func (api *InternalApi) getCFOrganizations(w http.ResponseWriter, req *http.Request, endpointName string) {
	l := Logger.Wrap("cf-get-organizations")
	l.Debug("getting organizations for CF endpoint %s", endpointName)

	// Check if CF manager is initialized
	if api.CFManager == nil {
		api.handleJSONResponse(w, nil, fmt.Errorf("CF functionality disabled - no CF endpoints configured"))
		return
	}

	// Get CF client through CF manager (handles health checks and retries)
	client, err := api.CFManager.GetClient(endpointName)
	if err != nil {
		l.Error("failed to get CF client for endpoint %s: %s", endpointName, err)
		api.handleJSONResponse(w, nil, fmt.Errorf("failed to connect to CF endpoint '%s': %w", endpointName, err))
		return
	}

	// Get organizations
	ctx := context.Background()
	params := capi.NewQueryParams().WithPerPage(100)
	response, err := client.Organizations().List(ctx, params)
	if err != nil {
		l.Error("failed to get organizations from CF endpoint %s: %s", endpointName, err)
		api.handleJSONResponse(w, nil, fmt.Errorf("failed to get organizations from CF: %w", err))
		return
	}

	// Convert to interface{} for JSON response
	orgs := make([]interface{}, len(response.Resources))
	for i, org := range response.Resources {
		orgs[i] = map[string]interface{}{
			"guid": org.GUID,
			"name": org.Name,
		}
	}

	api.handleJSONResponse(w, map[string]interface{}{
		"endpoint":      endpointName,
		"organizations": orgs,
	}, nil)
}

// getCFSpaces handles GET /b/cf/endpoints/{name}/orgs/{org_guid}/spaces
func (api *InternalApi) getCFSpaces(w http.ResponseWriter, req *http.Request, endpointName string, orgGuid string) {
	l := Logger.Wrap("cf-get-spaces")
	l.Debug("getting spaces for CF endpoint %s, org %s", endpointName, orgGuid)

	// Check if CF manager is initialized
	if api.CFManager == nil {
		api.handleJSONResponse(w, nil, fmt.Errorf("CF functionality disabled - no CF endpoints configured"))
		return
	}

	// Get CF client through CF manager (handles health checks and retries)
	client, err := api.CFManager.GetClient(endpointName)
	if err != nil {
		l.Error("failed to get CF client for endpoint %s: %s", endpointName, err)
		api.handleJSONResponse(w, nil, fmt.Errorf("failed to connect to CF endpoint '%s': %w", endpointName, err))
		return
	}

	// Get spaces for the organization
	ctx := context.Background()
	params := capi.NewQueryParams().WithFilter("organization_guids", orgGuid).WithPerPage(100)
	response, err := client.Spaces().List(ctx, params)
	if err != nil {
		l.Error("failed to get spaces from CF endpoint %s for org %s: %s", endpointName, orgGuid, err)
		api.handleJSONResponse(w, nil, fmt.Errorf("failed to get spaces from CF: %w", err))
		return
	}

	// Convert to interface{} for JSON response
	spaces := make([]interface{}, len(response.Resources))
	for i, space := range response.Resources {
		spaces[i] = map[string]interface{}{
			"guid": space.GUID,
			"name": space.Name,
		}
	}

	api.handleJSONResponse(w, map[string]interface{}{
		"endpoint": endpointName,
		"org_guid": orgGuid,
		"spaces":   spaces,
	}, nil)
}

// getCFServices handles GET /b/cf/endpoints/{name}/orgs/{org_guid}/spaces/{space_guid}/services
func (api *InternalApi) getCFServices(w http.ResponseWriter, req *http.Request, endpointName string, orgGuid string, spaceGuid string) {
	l := Logger.Wrap("cf-get-services")
	l.Debug("getting services for CF endpoint %s, org %s, space %s", endpointName, orgGuid, spaceGuid)

	// Check if CF manager is initialized
	if api.CFManager == nil {
		api.handleJSONResponse(w, nil, fmt.Errorf("CF functionality disabled - no CF endpoints configured"))
		return
	}

	// Get CF client through CF manager (handles health checks and retries)
	client, err := api.CFManager.GetClient(endpointName)
	if err != nil {
		l.Error("failed to get CF client for endpoint %s: %s", endpointName, err)
		api.handleJSONResponse(w, nil, fmt.Errorf("failed to connect to CF endpoint '%s': %w", endpointName, err))
		return
	}

	// Get service instances for the space
	ctx := context.Background()
	params := capi.NewQueryParams().WithFilter("space_guids", spaceGuid).WithPerPage(100)
	response, err := client.ServiceInstances().List(ctx, params)
	if err != nil {
		l.Error("failed to get service instances from CF endpoint %s for space %s: %s", endpointName, spaceGuid, err)
		api.handleJSONResponse(w, nil, fmt.Errorf("failed to get service instances from CF: %w", err))
		return
	}

	// Convert to interface{} for JSON response
	services := make([]interface{}, len(response.Resources))
	for i, si := range response.Resources {
		services[i] = map[string]interface{}{
			"guid":       si.GUID,
			"name":       si.Name,
			"space_guid": si.Relationships.Space.Data.GUID,
			"type":       si.Type,
			"last_operation": map[string]interface{}{
				"type":        si.LastOperation.Type,
				"state":       si.LastOperation.State,
				"description": si.LastOperation.Description,
			},
		}
	}

	api.handleJSONResponse(w, map[string]interface{}{
		"endpoint":   endpointName,
		"org_guid":   orgGuid,
		"space_guid": spaceGuid,
		"services":   services,
	}, nil)
}

// getCFServiceBindings handles GET /b/cf/endpoints/{name}/orgs/{org_guid}/spaces/{space_guid}/service_instances/{service_guid}/bindings
func (api *InternalApi) getCFServiceBindings(w http.ResponseWriter, req *http.Request, endpointName string, orgGuid string, spaceGuid string, serviceGuid string) {
	l := Logger.Wrap("cf-get-service-bindings")
	l.Debug("getting service bindings for CF endpoint %s, org %s, space %s, service %s", endpointName, orgGuid, spaceGuid, serviceGuid)

	// Check if CF manager is initialized
	if api.CFManager == nil {
		api.handleJSONResponse(w, nil, fmt.Errorf("CF functionality disabled - no CF endpoints configured"))
		return
	}

	// Get CF client through CF manager (handles health checks and retries)
	client, err := api.CFManager.GetClient(endpointName)
	if err != nil {
		l.Error("failed to get CF client for endpoint %s: %s", endpointName, err)
		api.handleJSONResponse(w, nil, fmt.Errorf("failed to connect to CF endpoint '%s': %w", endpointName, err))
		return
	}

	// Get service credential bindings for the service instance
	ctx := context.Background()
	params := capi.NewQueryParams().WithFilter("service_instance_guids", serviceGuid).WithPerPage(100)
	response, err := client.ServiceCredentialBindings().List(ctx, params)
	if err != nil {
		l.Error("failed to get service bindings from CF endpoint %s for service %s: %s", endpointName, serviceGuid, err)
		api.handleJSONResponse(w, nil, fmt.Errorf("failed to get service bindings from CF: %w", err))
		return
	}

	// Collect unique app GUIDs to fetch app names
	appGUIDs := make(map[string]bool)
	for _, binding := range response.Resources {
		if binding.Relationships.App != nil {
			appGUIDs[binding.Relationships.App.Data.GUID] = true
		}
	}

	// Fetch app names if there are any apps
	appNames := make(map[string]string)
	if len(appGUIDs) > 0 {
		appGUIDList := make([]string, 0, len(appGUIDs))
		for guid := range appGUIDs {
			appGUIDList = append(appGUIDList, guid)
		}

		appParams := capi.NewQueryParams().WithFilter("guids", appGUIDList...).WithPerPage(100)
		appResponse, err := client.Apps().List(ctx, appParams)
		if err != nil {
			l.Error("failed to fetch app names for bindings: %s", err)
		} else {
			for _, app := range appResponse.Resources {
				appNames[app.GUID] = app.Name
			}
		}
	}

	// Convert to interface{} for JSON response
	bindings := make([]interface{}, len(response.Resources))
	for i, binding := range response.Resources {
		bindingData := map[string]interface{}{
			"guid":                  binding.GUID,
			"name":                  binding.Name,
			"service_instance_guid": binding.Relationships.ServiceInstance.Data.GUID,
			"type":                  binding.Type,
			"last_operation": map[string]interface{}{
				"type":        binding.LastOperation.Type,
				"state":       binding.LastOperation.State,
				"description": binding.LastOperation.Description,
			},
		}

		// Add app information if available
		if binding.Relationships.App != nil {
			appGUID := binding.Relationships.App.Data.GUID
			bindingData["app_guid"] = appGUID
			if appName, exists := appNames[appGUID]; exists {
				bindingData["app_name"] = appName
			}
		}

		bindings[i] = bindingData
	}

	api.handleJSONResponse(w, map[string]interface{}{
		"endpoint":     endpointName,
		"org_guid":     orgGuid,
		"space_guid":   spaceGuid,
		"service_guid": serviceGuid,
		"bindings":     bindings,
	}, nil)
}

// --- CF API Client Helper Methods ---

// connectCFEndpoint handles POST /b/cf/endpoints/{name}/connect
func (api *InternalApi) connectCFEndpoint(w http.ResponseWriter, req *http.Request, endpointName string) {
	l := Logger.Wrap("cf-connect-endpoint")
	l.Debug("connecting to CF endpoint %s", endpointName)

	// Check if CF manager is initialized
	if api.CFManager == nil {
		api.handleJSONResponse(w, map[string]interface{}{
			"success": false,
			"error":   "CF functionality disabled - no CF endpoints configured",
		}, nil)
		return
	}

	// Apply rate limiting for connection tests
	if err := api.SecurityMiddleware.ValidateRequest("cf-endpoint-test", "test", nil); err != nil {
		l.Error("rate limit exceeded for CF endpoint test: %s", err)
		api.handleJSONResponse(w, map[string]interface{}{
			"success": false,
			"error":   "rate limit exceeded for connection tests",
		}, nil)
		return
	}

	// Test the connection
	start := time.Now()
	client, err := api.CFManager.GetClient(endpointName)
	if err != nil {
		l.Error("CF endpoint %s connection test failed: %s", endpointName, err)
		api.handleJSONResponse(w, map[string]interface{}{
			"success":     false,
			"error":       err.Error(),
			"endpoint":    endpointName,
			"duration_ms": time.Since(start).Milliseconds(),
		}, nil)
		return
	}

	// Test API call to verify connection
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	info, err := client.GetInfo(ctx)
	if err != nil {
		l.Error("CF endpoint %s API test failed: %s", endpointName, err)
		api.handleJSONResponse(w, map[string]interface{}{
			"success":     false,
			"error":       fmt.Sprintf("API test failed: %v", err),
			"endpoint":    endpointName,
			"duration_ms": time.Since(start).Milliseconds(),
		}, nil)
		return
	}

	l.Info("CF endpoint %s connection test successful", endpointName)
	api.handleJSONResponse(w, map[string]interface{}{
		"success":     true,
		"endpoint":    endpointName,
		"duration_ms": time.Since(start).Milliseconds(),
		"cf_info": map[string]interface{}{
			"name":        info.Name,
			"build":       info.Build,
			"version":     info.Version,
			"description": info.Description,
			"cf_on_k8s":   info.CFOnK8s,
		},
	}, nil)
}

// Helper function to safely get string values from map
func getStringFromMap(m map[string]interface{}, key string) string {
	if value, exists := m[key]; exists {
		if str, ok := value.(string); ok {
			return str
		}
	}
	return ""
}

// generateID generates a unique ID for registrations
func generateID() string {
	return fmt.Sprintf("cf-reg-%d", time.Now().UnixNano())
}

// handleJSONResponse is a helper method to handle JSON responses consistently
func (api *InternalApi) handleJSONResponse(w http.ResponseWriter, result interface{}, err error) {
	w.Header().Set("Content-Type", "application/json")

	if err != nil {
		w.WriteHeader(500)
		errorResponse := map[string]interface{}{
			"success": false,
			"error":   err.Error(),
		}

		if jsonData, jsonErr := json.Marshal(errorResponse); jsonErr == nil {
			if _, writeErr := w.Write(jsonData); writeErr != nil {
				// Log write error but continue processing
			}
		} else {
			fmt.Fprintf(w, `{"success": false, "error": "internal error"}`)
		}
		return
	}

	if jsonData, jsonErr := json.Marshal(result); jsonErr == nil {
		w.WriteHeader(200)
		if _, writeErr := w.Write(jsonData); writeErr != nil {
			// Log write error but response already started
		}
	} else {
		w.WriteHeader(500)
		fmt.Fprintf(w, `{"success": false, "error": "failed to marshal response"}`)
	}
}

// handleRabbitMQStreamingWebSocket handles WebSocket connections for rabbitmqctl command streaming
func (api *InternalApi) handleRabbitMQStreamingWebSocket(w http.ResponseWriter, r *http.Request, instanceID, deploymentName, instanceName string, instanceIndex int) {
	l := Logger.Wrap("rabbitmqctl-websocket")
	l.Info("WebSocket connection request for rabbitmqctl streaming on instance %s", instanceID)

	// Upgrade to WebSocket
	upgrader := gorillawebsocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			return true // TODO: implement proper origin checking
		},
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		l.Error("WebSocket upgrade failed: %v", err)
		return
	}
	defer conn.Close()

	l.Info("WebSocket connection established for instance %s", instanceID)

	// Set up message handling
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// Handle incoming messages
	for {
		select {
		case <-ctx.Done():
			return
		default:
			var message struct {
				Type      string   `json:"type"`
				Category  string   `json:"category"`
				Command   string   `json:"command"`
				Arguments []string `json:"arguments"`
			}

			// Read message from WebSocket
			err := conn.ReadJSON(&message)
			if err != nil {
				if gorillawebsocket.IsUnexpectedCloseError(err, gorillawebsocket.CloseGoingAway, gorillawebsocket.CloseAbnormalClosure) {
					l.Error("WebSocket read error: %v", err)
				}
				return
			}

			// Handle different message types
			switch message.Type {
			case "execute":
				api.handleStreamingExecution(ctx, conn, instanceID, deploymentName, instanceName, instanceIndex, message.Category, message.Command, message.Arguments, l)
			case "ping":
				// Respond to ping
				response := map[string]interface{}{
					"type": "pong",
				}
				if err := conn.WriteJSON(response); err != nil {
					l.Error("Failed to send pong: %v", err)
					return
				}
			default:
				// Send error for unknown message type
				response := map[string]interface{}{
					"type":  "error",
					"error": fmt.Sprintf("unknown message type: %s", message.Type),
				}
				if err := conn.WriteJSON(response); err != nil {
					l.Error("Failed to send error response: %v", err)
					return
				}
			}
		}
	}
}

// handleStreamingExecution handles the execution of a rabbitmqctl command with streaming output
func (api *InternalApi) handleStreamingExecution(ctx context.Context, conn *gorillawebsocket.Conn, instanceID, deploymentName, instanceName string, instanceIndex int, category, command string, arguments []string, l *Log) {
	if api.RabbitMQExecutorService == nil {
		response := map[string]interface{}{
			"type":  "error",
			"error": "RabbitMQ executor service not available",
		}
		if err := conn.WriteJSON(response); err != nil {
			l.Error("Failed to send WebSocket error response: %v", err)
		}
		return
	}

	// Create execution context
	execCtx := rabbitmqssh.ExecutionContext{
		Context:    ctx,
		InstanceID: instanceID,
		User:       "websocket-user", // TODO: get from auth
		ClientIP:   "websocket",      // TODO: get actual client IP
	}

	// Execute command with streaming
	result, err := api.RabbitMQExecutorService.ExecuteCommand(execCtx, deploymentName, instanceName, instanceIndex, category, command, arguments)
	if err != nil {
		response := map[string]interface{}{
			"type":  "error",
			"error": err.Error(),
		}
		if err := conn.WriteJSON(response); err != nil {
			l.Error("Failed to send WebSocket error response: %v", err)
		}
		return
	}

	// Send initial response
	response := map[string]interface{}{
		"type":         "execution_started",
		"execution_id": result.ExecutionID,
		"category":     result.Category,
		"command":      result.Command,
		"arguments":    result.Arguments,
		"status":       result.Status,
	}
	if err := conn.WriteJSON(response); err != nil {
		l.Error("Failed to send execution started response: %v", err)
		return
	}

	// Stream output from the execution
	go func() {
		defer func() {
			// Send final status
			finalResponse := map[string]interface{}{
				"type":         "execution_completed",
				"execution_id": result.ExecutionID,
				"status":       result.Status,
				"success":      result.Success,
				"exit_code":    result.ExitCode,
			}
			if result.Error != "" {
				finalResponse["error"] = result.Error
			}
			if err := conn.WriteJSON(finalResponse); err != nil {
				l.Error("Failed to send final WebSocket response: %v", err)
			}

			// Log to audit if available
			if api.RabbitMQAuditService != nil {
				if err := api.RabbitMQAuditService.LogStreamingExecution(ctx, result, execCtx.User, execCtx.ClientIP); err != nil {
					l.Error("Failed to log streaming execution audit: %v", err)
				}
			}
		}()

		for {
			select {
			case <-ctx.Done():
				return
			case outputLine, ok := <-result.Output:
				if !ok {
					// Output channel closed, execution finished
					return
				}

				// Send output line
				outputResponse := map[string]interface{}{
					"type":         "output",
					"execution_id": result.ExecutionID,
					"data":         outputLine,
				}
				if err := conn.WriteJSON(outputResponse); err != nil {
					l.Error("Failed to send output: %v", err)
					return
				}
			}
		}
	}()
}

// handleRabbitMQPluginsStreamingWebSocket handles WebSocket connections for rabbitmq-plugins command streaming
func (api *InternalApi) handleRabbitMQPluginsStreamingWebSocket(w http.ResponseWriter, r *http.Request, instanceID, deploymentName, instanceName string, instanceIndex int) {
	l := Logger.Wrap("rabbitmq-plugins-websocket")
	l.Info("WebSocket connection request for rabbitmq-plugins streaming on instance %s", instanceID)

	// Upgrade to WebSocket
	upgrader := gorillawebsocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			return true // TODO: implement proper origin checking
		},
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		l.Error("WebSocket upgrade failed: %v", err)
		return
	}
	defer conn.Close()

	l.Info("WebSocket connection established for instance %s", instanceID)

	// Set up message handling
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// Handle incoming messages
	for {
		select {
		case <-ctx.Done():
			return
		default:
			var message struct {
				Type      string   `json:"type"`
				Category  string   `json:"category"`
				Command   string   `json:"command"`
				Arguments []string `json:"arguments"`
			}

			// Read message from WebSocket
			err := conn.ReadJSON(&message)
			if err != nil {
				if gorillawebsocket.IsUnexpectedCloseError(err, gorillawebsocket.CloseGoingAway, gorillawebsocket.CloseAbnormalClosure) {
					l.Error("WebSocket read error: %v", err)
				}
				return
			}

			// Handle different message types
			switch message.Type {
			case "execute":
				l.Info("Executing rabbitmq-plugins command: %s.%s with args %v", message.Category, message.Command, message.Arguments)
				api.handlePluginsStreamingExecution(ctx, conn, instanceID, deploymentName, instanceName, instanceIndex, message.Category, message.Command, message.Arguments, l)

			case "ping":
				response := map[string]interface{}{
					"type": "pong",
				}
				if err := conn.WriteJSON(response); err != nil {
					l.Error("Failed to send pong response: %v", err)
					return
				}

			default:
				response := map[string]interface{}{
					"type":  "error",
					"error": fmt.Sprintf("unknown message type: %s", message.Type),
				}
				if err := conn.WriteJSON(response); err != nil {
					l.Error("Failed to send error response: %v", err)
					return
				}
			}
		}
	}
}

// handlePluginsStreamingExecution handles the execution of a rabbitmq-plugins command with streaming output
func (api *InternalApi) handlePluginsStreamingExecution(ctx context.Context, conn *gorillawebsocket.Conn, instanceID, deploymentName, instanceName string, instanceIndex int, category, command string, arguments []string, l *Log) {
	if api.RabbitMQPluginsExecutorService == nil {
		response := map[string]interface{}{
			"type":  "error",
			"error": "RabbitMQ plugins executor service not available",
		}
		if err := conn.WriteJSON(response); err != nil {
			l.Error("Failed to send WebSocket error response: %v", err)
		}
		return
	}

	// Create execution context
	execCtx := rabbitmqssh.PluginsExecutionContext{
		Context:    ctx,
		InstanceID: instanceID,
		User:       "websocket-user", // TODO: get from auth
		ClientIP:   "websocket",      // TODO: get actual client IP
	}

	// Execute command with streaming
	result, err := api.RabbitMQPluginsExecutorService.ExecuteCommand(execCtx, deploymentName, instanceName, instanceIndex, category, command, arguments)
	if err != nil {
		response := map[string]interface{}{
			"type":  "error",
			"error": err.Error(),
		}
		if err := conn.WriteJSON(response); err != nil {
			l.Error("Failed to send WebSocket error response: %v", err)
		}
		return
	}

	// Send initial response
	response := map[string]interface{}{
		"type":         "execution_started",
		"execution_id": result.ExecutionID,
		"instance_id":  instanceID,
		"category":     category,
		"command":      command,
		"arguments":    arguments,
		"start_time":   result.StartTime,
	}
	if err := conn.WriteJSON(response); err != nil {
		l.Error("Failed to send initial WebSocket response: %v", err)
		return
	}

	// Start streaming output in a goroutine
	go func() {
		defer func() {
			// Send final status when execution completes
			finalResponse := map[string]interface{}{
				"type":         "execution_completed",
				"execution_id": result.ExecutionID,
				"status":       result.Status,
				"success":      result.Success,
				"exit_code":    result.ExitCode,
			}
			if result.EndTime != nil {
				finalResponse["end_time"] = *result.EndTime
				finalResponse["duration"] = result.EndTime.Sub(result.StartTime).Milliseconds()
			}
			if result.Error != "" {
				finalResponse["error"] = result.Error
			}
			if err := conn.WriteJSON(finalResponse); err != nil {
				l.Error("Failed to send final WebSocket response: %v", err)
			}

			// Log to audit if available
			if api.RabbitMQPluginsAuditService != nil {
				execution := &rabbitmqssh.RabbitMQPluginsExecution{
					InstanceID: instanceID,
					Category:   category,
					Command:    command,
					Arguments:  arguments,
					Timestamp:  result.StartTime.UnixNano() / int64(time.Millisecond),
					Output:     "", // Will be populated during streaming
					ExitCode:   result.ExitCode,
					Success:    result.Success,
				}

				duration := int64(0)
				if result.EndTime != nil {
					duration = result.EndTime.Sub(result.StartTime).Milliseconds()
				}

				if err := api.RabbitMQPluginsAuditService.LogExecution(ctx, execution, execCtx.User, execCtx.ClientIP, result.ExecutionID, duration); err != nil {
					l.Error("Failed to log streaming execution audit: %v", err)
				}
			}
		}()

		for {
			select {
			case <-ctx.Done():
				return
			case outputLine, ok := <-result.Output:
				if !ok {
					// Output channel closed, execution finished
					return
				}

				// Send output line
				outputResponse := map[string]interface{}{
					"type":         "output",
					"execution_id": result.ExecutionID,
					"data":         outputLine,
				}
				if err := conn.WriteJSON(outputResponse); err != nil {
					l.Error("Failed to send output: %v", err)
					return
				}
			}
		}
	}()
}
