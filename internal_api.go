package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"time"

	"github.com/geofffranks/simpleyaml"
	"github.com/geofffranks/spruce"
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
		l := Logger.Wrap("redeploy-service")
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

	pattern = regexp.MustCompile("^/b/([^/]+)/upgrade")
	if m := pattern.FindStringSubmatch(req.URL.Path); m != nil {
		instanceID := m[1]
		l := Logger.Wrap("update-service")

		l.Debug("looking up credentials for %s", m[1])
		inst, exists, err := api.Vault.FindInstance(m[1])
		if err != nil || !exists {
			l.Error("unable to find service instance %s in vault index", m[1])
		}
		l.Debug("looking up service '%s' / plan '%s' in catalog", inst.ServiceID, inst.PlanID)
		plan, err := api.Broker.FindPlan(inst.ServiceID, inst.PlanID)
		if err != nil {
			w.WriteHeader(500)
			fmt.Fprintf(w, "---\nerror: unable to find plan '%s'\n\n", inst.PlanID)
			return
		}

		// Default options for upgrades
		upgradeStemcells := true
		upgradeReleases := false

		l.Debug("looking up BOSH manifest for %s in Vault", instanceID)
		vault_manifest := struct {
			Manifest string `json:"manifest"`
		}{}

		exists, err = api.Vault.Get(fmt.Sprintf("%s/manifest", instanceID), &vault_manifest)
		if err != nil || !exists {
			l.Error("unable to find service instance %s in vault index", instanceID)
			w.WriteHeader(404)
			return
		}
		if vault_manifest.Manifest == "" {
			w.WriteHeader(500)
			fmt.Fprintf(w, "Deployment '%s' empty\n", instanceID)
			return
		}

		y, err := simpleyaml.NewYaml([]byte(vault_manifest.Manifest))
		if err != nil {
			w.WriteHeader(500)
			fmt.Fprintf(w, "YAML '%s' invalid\n", vault_manifest.Manifest)
			return
		}

		deploymentName, err := y.Get("name").String()
		if err != nil {
			w.WriteHeader(500)
			fmt.Fprintf(w, "Deployment name missing\n")
			return
		}

		// Get current Manifest from BOSH
		l.Debug("Determined BOSH deployment name to be %s", deploymentName)
		l.Debug("Looking up live manifest for %s from BOSH", instanceID)

		boshManifest := struct {
			Manifest string `json:"manifest"`
		}{}

		boshManifest, err = api.Broker.BOSH.GetDeployment(deploymentName)
		if err != nil || boshManifest.Manifest == "" {
			w.WriteHeader(404)
			fmt.Fprintf(w, "Deployment '%s' not found.  Err: %s\n", deploymentName, err)
			return
		}

		canUpgradeRelease, err := UpgradePrep(plan, instanceID, boshManifest.Manifest)
		if err != nil {
			w.WriteHeader(500)
			fmt.Fprintf(w, "Err getting Deployments: %s\n", err)
			return
		}
		l.Info("Can Upgrade Debug check returned %s", canUpgradeRelease)

		stemcellVersion, err := GetStemcellBlock(api.Broker.BOSH, deploymentName, upgradeStemcells)
		if err != nil {
			w.WriteHeader(500)
			fmt.Fprintf(w, "Error getting stemcell versions: %s\n", err)
			return
		}

		allReleaseVersions, err := GetReleasesBlock(api.Broker.BOSH, deploymentName, upgradeReleases)
		if err != nil {
			w.WriteHeader(500)
			fmt.Fprintf(w, "Error getting previous releases: %s\n", err)
			return
		}

		mappedManifest := make(map[interface{}]interface{})
		err = yaml.Unmarshal([]byte(boshManifest.Manifest), &mappedManifest)
		if err != nil {
			w.WriteHeader(500)
			fmt.Fprintf(w, "Error parsing bosh manifest: %s\n", err)
			return
		}

		spruceManifest, err := spruce.Merge(mappedManifest, allReleaseVersions, stemcellVersion)
		if err != nil {
			w.WriteHeader(500)
			fmt.Fprintf(w, "Error locking release versions: %s\n", err)
			return
		}

		spruceYamlManifest, _ := yaml.Marshal(spruceManifest)
		targetManifest := string(spruceYamlManifest)

		if canUpgradeRelease == true {
			l.Debug("Generating manifest for updating service deployment")
			params := make(map[interface{}]interface{})

			// TODO: Load params from Vault
			defaults := make(map[interface{}]interface{})
			defaults["name"] = deploymentName

			// Release versions and Stemcell versions will be set by GenManifest
			new_manifest, err := GenManifest(plan, defaults, wrap("meta.params", params), stemcellVersion)
			if err != nil {
				w.WriteHeader(500)
				fmt.Fprintf(w, "failed to generate service deployment manifest: %s", err)
				return
			}
			targetManifest = new_manifest
		} else {
			l.Debug("Cannot upgrade Release, using original manifest")
		}

		///===============================================
		err = api.Vault.Put(fmt.Sprintf("%s/manifest", instanceID), map[string]interface{}{
			"manifest": targetManifest,
		})
		if err != nil {
			w.WriteHeader(500)
			fmt.Fprintf(w, "failed to store generated manifest in the vault (aborting): %s", err)
			return
		}

		l.Debug("uploading releases (if necessary) to BOSH director")
		err = UploadReleasesFromManifest(targetManifest, api.Broker.BOSH, l)
		if err != nil {
			l.Error("failed to upload service deployment releases: %s", err)

		}

		task, err := api.Broker.BOSH.CreateDeployment(targetManifest)
		if err != nil {
			w.WriteHeader(500)
			fmt.Fprintf(w, "error trying to deploy upgrade to %s: %s\n", instanceID, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, "{\"message\": \"Upgrade of %s requested in task %d\"}", instanceID, task.ID)
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
		l.Debug("looking up task log for %s", m[1])
		_, id, _, err := api.Vault.State(m[1])
		if err != nil {
			l.Error("unable to find service instance %s in vault index", m[1])
			w.WriteHeader(404)
			return
		}
		if id == 0 {
			l.Error("'task' key not found in vault index for service instance %s; perhaps vault is corrupted?", m[1])
			w.WriteHeader(404)
			return
		}

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
	}

	w.WriteHeader(404)
	fmt.Fprintf(w, "endpoint not found...\n")
}
