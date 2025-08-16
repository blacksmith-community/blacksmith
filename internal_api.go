package main

import (
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
	"gopkg.in/yaml.v2"
)

type InternalApi struct {
	Env    string
	Vault  *Vault
	Broker *Broker
	Config Config
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

func (api *InternalApi) ServeHTTP(w http.ResponseWriter, req *http.Request) {
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
			instanceDetails["id"] = ""
		}

		// Read Instance Name
		if data, err := os.ReadFile("/var/vcap/instance/name"); err == nil {
			instanceDetails["name"] = strings.TrimSpace(string(data))
		} else {
			l.Debug("unable to read instance name file: %s", err)
			instanceDetails["name"] = ""
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
			Instances: idx.Data,
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

		// Get the logs from the Logger
		logs := Logger.String()

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

	pattern := regexp.MustCompile(`^/b/([^/]+)/manifest\.yml$`)
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
