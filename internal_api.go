package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"time"
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
			Instances interface{} `json:"instances"`
			Plans     interface{} `json:"plans"`
			Log       string      `json:"log"`
		}{
			Env:       api.Env,
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
		taskID, err := api.Broker.BOSH.Cleanup(false)
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
			TaskID:   taskID,
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
		d, exists, err := api.Vault.Get(fmt.Sprintf("%s/manifest", m[1]))
		if err == nil && exists {
			if s, ok := d["manifest"]; ok {
				w.Header().Set("Content-type", "text/plain")
				fmt.Fprintf(w, "%v\n", s)
				return
			}
		}
		w.WriteHeader(404)
		return
	}

	pattern = regexp.MustCompile("^/b/([^/]+)/task\\.log$")
	if m := pattern.FindStringSubmatch(req.URL.Path); m != nil {
		l := Logger.Wrap("task.log")
		l.Debug("looking up task log for %s", m[1])
		d, exists, err := api.Vault.Get(fmt.Sprintf("%s/task", m[1]))
		if err != nil || !exists {
			l.Error("unable to find service instance %s in vault index", m[1])
		} else if idf, ok := d["task"]; ok {
			id := int(idf.(float64))
			events, err := api.Broker.BOSH.GetTaskEvents(id)
			if err != nil {
				w.WriteHeader(500)
				fmt.Fprintf(w, "error: %s\n", err)
				return
			}
			w.Header().Set("Content-type", "text/plain")
			for _, event := range events {
				ts := time.Unix(int64(event.Time), 0)
				if event.Task != "" {
					fmt.Fprintf(w, "Task %d | %s | %s: %s %s\n", id, ts.Format("15:04:05"), event.Stage, event.Task, event.State)
				} else if event.Error.Code != 0 {
					fmt.Fprintf(w, "Task %d | %s | ERROR: [%d] %s\n", id, ts.Format("15:04:05"), event.Error.Code, event.Error.Message)
				}
			}
			return
		} else {
			l.Error("'task' key not found in vault index for service instance %s; perhaps vault is corrupted?", m[1])
		}
		w.WriteHeader(404)
		return
	}

	w.WriteHeader(404)
	fmt.Fprintf(w, "endpoint not found...\n")
}
