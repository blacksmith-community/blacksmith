package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"

	"gopkg.in/yaml.v2"
)

type InternalApi struct {
	Env    string
	Vault  *Vault
	Broker *Broker
}

func (api *InternalApi) ServeHTTP(w http.ResponseWriter, req *http.Request) {
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
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		fmt.Fprintf(w, "%s\n", string(js))
		return
	}

	pattern := regexp.MustCompile("^/b/([^/]+)/manifest\\.yml$")
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
		w.Write([]byte(manifest.Manifest + "\n"))
		return
	}

	pattern = regexp.MustCompile("^/b/([^/]+)/redeploy$")
	if m := pattern.FindStringSubmatch(req.URL.Path); m != nil {
		l := Logger.Wrap("update-service")
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
		task, err := api.Broker.BOSH.CreateDeployment(manifest.Manifest)
		if err != nil {
			w.WriteHeader(500)
			fmt.Fprintf(w, "error: %s\n", err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, "{\"message\": \"Redeploy of %s requested in task %d\"}", m[1], task.ID)
		return
	}

	pattern = regexp.MustCompile("^/b/([^/]+)/creds\\.yml$")
	if m := pattern.FindStringSubmatch(req.URL.Path); m != nil {
		l := Logger.Wrap("creds.yml")
		l.Debug("looking up credentials for %s", m[1])
		inst, exists, err := api.Vault.FindInstance(m[1])
		if err != nil || !exists {
			l.Error("unable to find service instance %s in vault index", m[1])
		} else {
			w.Header().Set("Content-type", "text/plain")

			l.Debug("looking up service '%s' / plan '%s' in catalog", inst.ServiceID, inst.PlanID)
			plan, err := api.Broker.FindPlan(inst.ServiceID, inst.PlanID)
			if err != nil {
				w.WriteHeader(500)
				fmt.Fprintf(w, "---\nerror: unable to find plan '%s'\n\n", inst.PlanID)
				return
			}

			creds, err := GetCreds(m[1], plan, api.Broker.BOSH, l)
			if err != nil {
				w.WriteHeader(500)
				fmt.Fprintf(w, "---\nerror: unable to retrieve credentials\n\n")
				return
			}

			b, err := yaml.Marshal(creds)
			if err != nil {
				w.WriteHeader(500)
				fmt.Fprintf(w, "---\nerror: unable to yamlify credentials\n\n")
				return
			}

			fmt.Fprintf(w, "%s\n", string(b))
			return
		}

		w.WriteHeader(404)
		return
	}

	pattern = regexp.MustCompile("^/b/([^/]+)/task\\.log$")
	if m := pattern.FindStringSubmatch(req.URL.Path); m != nil {
		l := Logger.Wrap("task.log")
		instanceID := m[1]
		l.Debug("looking up task log for %s", instanceID)
		
		// Get deployment name from vault
		var deploymentData struct {
			DeploymentName string `json:"deployment_name"`
		}
		exists, err := api.Vault.Get(instanceID, &deploymentData)
		if err != nil || !exists || deploymentData.DeploymentName == "" {
			l.Error("unable to find deployment name for service instance %s in vault", instanceID)
			w.WriteHeader(404)
			return
		}
		
		deploymentName := deploymentData.DeploymentName
		l.Debug("fetching task log for deployment %s", deploymentName)
		
		// Get deployment events from BOSH
		events, err := api.Broker.BOSH.GetEvents(deploymentName)
		if err != nil {
			l.Error("unable to get events for deployment %s: %s", deploymentName, err)
			w.WriteHeader(500)
			fmt.Fprintf(w, "error: %s\n", err)
			return
		}
		
		// Find the most recent task ID from events
		var taskID int
		for i := len(events) - 1; i >= 0; i-- {
			if events[i].TaskID != "" && events[i].TaskID != "0" {
				if id, err := strconv.Atoi(events[i].TaskID); err == nil && id > 0 {
					taskID = id
					break
				}
			}
		}
		
		if taskID == 0 {
			l.Error("no valid task ID found in deployment events for %s", deploymentName)
			w.WriteHeader(404)
			return
		}
		
		l.Debug("found task ID %d for deployment %s", taskID, deploymentName)
		
		// Get the actual task output/logs
		// Try "event" type first (what bosh CLI uses for --debug)
		output, err := api.Broker.BOSH.GetTaskOutput(taskID, "event")
		if err != nil || output == "" {
			// If event output fails, try debug output
			output, err = api.Broker.BOSH.GetTaskOutput(taskID, "debug")
			if err != nil || output == "" {
				// Finally try result output
				output, err = api.Broker.BOSH.GetTaskOutput(taskID, "result")
			}
		}
		if err != nil || output == "" {
			// Fall back to events if output is not available
			events, err := api.Broker.BOSH.GetTaskEvents(taskID)
			if err != nil {
				w.WriteHeader(500)
				fmt.Fprintf(w, "error: %s\n", err)
				return
			}
			w.Header().Set("Content-type", "text/plain")
			for _, event := range events {
				ts := event.Time
				if event.Task != "" {
					fmt.Fprintf(w, "Task %d | %s | %s: %s %s\n", taskID, ts.Format("15:04:05"), event.Stage, event.Task, event.State)
				} else if event.Error != nil && event.Error.Code != 0 {
					fmt.Fprintf(w, "Task %d | %s | ERROR: [%d] %s\n", taskID, ts.Format("15:04:05"), event.Error.Code, event.Error.Message)
				}
			}
			return
		}
		
		w.Header().Set("Content-type", "text/plain")
		fmt.Fprintf(w, "%s\n", output)
		return
	}

	pattern = regexp.MustCompile("^/b/([^/]+)/events$")
	if m := pattern.FindStringSubmatch(req.URL.Path); m != nil {
		l := Logger.Wrap("events")
		instanceID := m[1]
		l.Debug("looking up events for %s", instanceID)
		
		// Get deployment name from vault
		var deploymentData struct {
			DeploymentName string `json:"deployment_name"`
		}
		exists, err := api.Vault.Get(instanceID, &deploymentData)
		if err != nil || !exists || deploymentData.DeploymentName == "" {
			l.Error("unable to find deployment name for service instance %s in vault", instanceID)
			w.WriteHeader(404)
			return
		}
		
		deploymentName := deploymentData.DeploymentName
		l.Debug("fetching events for deployment %s", deploymentName)
		
		// Get deployment events from BOSH
		events, err := api.Broker.BOSH.GetEvents(deploymentName)
		if err != nil {
			l.Error("unable to get events for deployment %s: %s", deploymentName, err)
			w.WriteHeader(500)
			fmt.Fprintf(w, "error: %s\n", err)
			return
		}
		
		// Convert events to JSON
		b, err := json.MarshalIndent(events, "", "  ")
		if err != nil {
			w.WriteHeader(500)
			fmt.Fprintf(w, "error marshaling events: %s\n", err)
			return
		}
		
		w.Header().Set("Content-type", "application/json")
		fmt.Fprintf(w, "%s\n", string(b))
		return
	}

	pattern = regexp.MustCompile("^/b/([^/]+)/update-manifest$")
	if m := pattern.FindStringSubmatch(req.URL.Path); m != nil {
		if req.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			fmt.Fprint(w, "Method Not Allowed")
			return
		}

		l := Logger.Wrap("update-manifest")
		l.Debug("Updating BOSH manifest for %s", m[1])

		newManifest, err := io.ReadAll(req.Body)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprintf(w, "error reading request body: %s\n", err)
			return
		}

		vaultPath := fmt.Sprintf("%s/manifest", m[1])

		manifestData := struct {
			Manifest string `json:"manifest"`
		}{
			Manifest: string(newManifest),
		}

		err = api.Vault.Put(vaultPath, &manifestData)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprintf(w, "error updating manifest in vault: %s\n", err)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, "{\"message\": \"Manifest for %s updated successfully\"}", m[1])
		return
	}

	w.WriteHeader(404)
	fmt.Fprintf(w, "endpoint not found...\n")
}
