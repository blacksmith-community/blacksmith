package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
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
	"gopkg.in/yaml.v2"
)

type InternalApi struct {
	Env                string
	Vault              *Vault
	Broker             *Broker
	Config             Config
	VMMonitor          *VMMonitor
	Services           *services.Manager
	SSHService         ssh.SSHService
	RabbitMQSSHService *rabbitmqssh.SSHService
	WebSocketHandler   *websocket.SSHHandler
	SecurityMiddleware *services.SecurityMiddleware
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

	// CF Registration Management Endpoints
	if strings.HasPrefix(req.URL.Path, "/b/cf/") {
		api.handleCFRegistrationEndpoints(w, req)
		return
	}

	// Redis testing endpoints
	pattern := regexp.MustCompile(`^/b/([^/]+)/redis/(.+)$`)
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
		if err != nil {
			l.Error("unable to find plan %s/%s: %s", inst.ServiceID, inst.PlanID, err)
			w.WriteHeader(500)
			fmt.Fprintf(w, `{"error": "plan not found: %s"}`, err.Error())
			return
		}

		// Construct deployment name from plan and instance
		deploymentName := fmt.Sprintf("%s-%s-%s", plan.Service.ID, plan.Name, instanceID)

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

		// Construct deployment name from plan and instance
		deploymentName := fmt.Sprintf("%s-%s-%s", plan.Service.ID, plan.Name, instanceID)
		instanceName := plan.Name
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
		if err != nil {
			l.Error("unable to find plan %s/%s: %s", inst.ServiceID, inst.PlanID, err)
			w.WriteHeader(500)
			fmt.Fprintf(w, `{"error": "plan not found: %s"}`, err.Error())
			return
		}

		// Construct deployment name from plan and instance
		deploymentName := fmt.Sprintf("%s-%s-%s", plan.Service.ID, plan.Name, instanceID)
		instanceName := plan.Name
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

			// Construct deployment name from plan_id and instance_id
			deploymentName = inst.PlanID + "-" + param
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

		// Remove context field as requested
		delete(instanceData, "context")

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
		if err != nil {
			l.Error("unable to find plan %s/%s: %s", inst.ServiceID, inst.PlanID, err)
			w.WriteHeader(500)
			fmt.Fprintf(w, "error: %s", err)
			return
		}

		deploymentName := fmt.Sprintf("%s-%s-%s", plan.Service.ID, plan.Name, instanceID)
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
		if err != nil {
			l.Error("unable to find plan %s/%s: %s", inst.ServiceID, inst.PlanID, err)
			w.WriteHeader(500)
			fmt.Fprintf(w, "error: %s", err)
			return
		}

		deploymentName := fmt.Sprintf("%s-%s-%s", plan.Service.ID, plan.Name, instanceID)
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
		if err != nil {
			l.Error("unable to find plan %s/%s: %s", inst.ServiceID, inst.PlanID, err)
			w.WriteHeader(500)
			fmt.Fprintf(w, "error: %s", err)
			return
		}

		deploymentName := fmt.Sprintf("%s-%s-%s", plan.Service.ID, plan.Name, instanceID)
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
		if err != nil {
			l.Error("unable to find plan %s/%s: %s", inst.ServiceID, inst.PlanID, err)
			w.WriteHeader(500)
			fmt.Fprintf(w, "error: %s", err)
			return
		}

		deploymentName := fmt.Sprintf("%s-%s-%s", plan.Service.ID, plan.Name, instanceID)
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
							logEntry := map[string]interface{}{
								"time":     event.Time.Unix(),
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
