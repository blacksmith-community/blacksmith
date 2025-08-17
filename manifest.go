package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"blacksmith/bosh"
	"github.com/geofffranks/spruce"
	"github.com/smallfish/simpleyaml"
	"gopkg.in/yaml.v2"
)

const (
	// If BLACKSMITH_INSTANCE_DATA_DIR environment variable is not set, use this as a default
	DefaultBlacksmithWorkDir = "/var/vcap/data/blacksmith/"
)

func GetWorkDir() string {
	var blacksmithWorkDir = os.Getenv("BLACKSMITH_INSTANCE_DATA_DIR")
	if blacksmithWorkDir == "" {
		blacksmithWorkDir = DefaultBlacksmithWorkDir
	}
	return blacksmithWorkDir
}

func InitManifest(p Plan, instanceID string) error {
	Info("Initializing manifest for plan %s, instance %s", p.ID, instanceID)

	/* skip running the plan initialization script if it doesn't exist */
	if _, err := os.Stat(p.InitScriptPath); err != nil && os.IsNotExist(err) {
		Debug("No init script found at %s, skipping initialization", p.InitScriptPath)
		return nil
	}

	/* otherwise, execute it (chmodding to cut Forge authors some slack...) */
	Info("Running init script: %s", p.InitScriptPath)

	// Validate the executable path for security
	if err := validateExecutablePath(p.InitScriptPath); err != nil {
		Error("init script path validation failed: %s", err)
		return err
	}

	if err := os.Chmod(p.InitScriptPath, 0700 /* #nosec G302 - Scripts need execute permission, path validated above */); err != nil {
		Error("failed to make init script executable: %s", err)
		return err
	}

	cmd := exec.Command(p.InitScriptPath) // #nosec G204 - Path has been validated by validateExecutablePath

	cmd.Env = os.Environ()
	cmd.Env = append(cmd.Env, fmt.Sprintf("CREDENTIALS=secret/%s", instanceID))
	cmd.Env = append(cmd.Env, fmt.Sprintf("RAWJSONFILE=%s%s.json", GetWorkDir(), instanceID))
	cmd.Env = append(cmd.Env, fmt.Sprintf("YAMLFILE=%s%s.yml", GetWorkDir(), instanceID))
	cmd.Env = append(cmd.Env, fmt.Sprintf("BLACKSMITH_INSTANCE_DATA_DIR=%s", GetWorkDir()))
	cmd.Env = append(cmd.Env, fmt.Sprintf("INSTANCE_ID=%s", instanceID))
	cmd.Env = append(cmd.Env, fmt.Sprintf("BLACKSMITH_PLAN=%s", p.ID))
	// NOTE: BOSH_NETWORK is set in the env in config.go

	/* TODO: put more environment variables here, as needed */

	out, err := cmd.CombinedOutput()
	if err != nil {
		Error("Init script failed: %s", err)
		Debug("Init script output:\n%s", string(out))
		return err
	}
	Info("Init script completed successfully")
	Debug("Init script `%s' output:\n%s", p.InitScriptPath, string(out))
	return nil
}

func GenManifest(p Plan, manifests ...map[interface{}]interface{}) (string, error) {
	Info("Generating manifest for plan %s", p.ID)
	Debug("Starting spruce merge with %d additional manifests", len(manifests))

	merged, err := spruce.Merge(p.Manifest)
	if err != nil {
		Error("Failed to merge base manifest: %s", err)
		return "", err
	}
	for i, next := range manifests {
		Debug("Merging manifest %d of %d", i+1, len(manifests))
		merged, err = spruce.Merge(merged, next)
		if err != nil {
			Error("Failed to merge manifest %d: %s", i+1, err)
			return "", err
		}
	}
	Debug("Running spruce evaluator on merged manifest")
	eval := &spruce.Evaluator{Tree: merged}
	err = eval.Run(nil, nil)
	if err != nil {
		Error("Failed to evaluate spruce expressions: %s", err)
		return "", err
	}
	final := eval.Tree

	b, err := yaml.Marshal(final)
	if err != nil {
		Error("Failed to marshal final manifest to YAML: %s", err)
		return "", err
	}

	manifestStr := string(b)
	Info("Successfully generated manifest (size: %d bytes)", len(manifestStr))
	return manifestStr, nil
}

func UploadReleasesFromManifest(raw string, director bosh.Director, l *Log) error {
	var manifest struct {
		Releases []struct {
			Name    string `yaml:"name"`
			Version string `yaml:"version"`
			URL     string `yaml:"url"`
			SHA1    string `yaml:"sha1"`
		} `yaml:"releases"`
	}

	err := yaml.Unmarshal([]byte(raw), &manifest)
	if err != nil {
		return err
	}

	l.Debug("enumerating uploaded BOSH releases")
	rr, err := director.GetReleases()
	if err != nil {
		return err
	}

	have := make(map[string]bool)
	releases := []string{}
	for _, rl := range rr {
		for _, v := range rl.ReleaseVersions {
			releaseID := rl.Name + "/" + v.Version
			releases = append(releases, releaseID)
			have[releaseID] = true
		}
	}

	// Log all releases as a single JSON object
	if len(releases) > 0 {
		releasesJSON, _ := json.Marshal(map[string]interface{}{
			"count":    len(releases),
			"releases": releases,
		})
		l.Debug("found BOSH releases: %s", string(releasesJSON))
	}

	l.Debug("determining which BOSH releases need uploaded")
	for _, rl := range manifest.Releases {
		if have[rl.Name+"/"+rl.Version] {
			l.Debug("already have %s/%s; skipping upload", rl.Name, rl.Version)
		} else if rl.URL == "" {
			l.Debug("%s/%s is missing either its URL; skipping upload", rl.Name, rl.Version)
		} else if rl.SHA1 == "" {
			l.Debug("%s/%s is missing either its SHA1 checksum; skipping upload", rl.Name, rl.Version)
		} else {
			l.Debug("uploading BOSH release %s/%s from %s (sha1 %s)...", rl.Name, rl.Version, rl.URL, rl.SHA1)
			_, err := director.UploadRelease(rl.URL, rl.SHA1)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

func GetCreds(id string, plan Plan, director bosh.Director, l *Log) (interface{}, error) {
	var jobs []*Job
	jobsYAML := make(map[string][]*Job)
	var dnsname string

	deployment := plan.ID + "-" + id

	l.Debug("looking up BOSH VM information for %s", deployment)
	vms, err := director.GetDeploymentVMs(deployment)
	if err != nil {
		l.Error("failed to retrieve BOSH VM information for %s: %s", deployment, err)
		return nil, err
	}

	if err := os.Setenv("CREDENTIALS", fmt.Sprintf("secret/%s", id)); err != nil {
		l.Error("failed to set CREDENTIALS environment variable: %s", err)
		return nil, err
	}

	byType := make(map[string]*Job)

	network := os.Getenv("BOSH_NETWORK")
	// Remove dots from network component for valid DNS names (matching the pattern in env.rb)
	networkDNS := strings.ReplaceAll(network, ".", "")

	for _, vm := range vms {
		l.Debug("vm.id: %s, vm.CID: %s", vm.ID, vm.CID)
		job := Job{
			vm.Job + "/" + strconv.Itoa(vm.Index),
			deployment,
			vm.ID,
			plan.ID,
			plan.Name,
			vm.ID + "." + plan.Type + "." + networkDNS + "." + deployment + ".bosh",
			vm.IPs,
			vm.DNS,
		}
		l.Debug("found job {name: %s, deployment: %s, id: %s, plan_id: %s, plan_name: %s, fqdn: %s, ips: [%s], dns: [%s]", job.Name, job.Deployment, job.ID, job.PlanID, job.PlanName, job.FQDN, strings.Join(vm.IPs, ", "), strings.Join(vm.DNS, ", "))
		dnsname = job.FQDN

		jobs = append(jobs, &job)

		if typ, ok := byType[vm.Job]; ok {
			typ.IPs = append(typ.IPs, vm.IPs...)
			typ.DNS = append(typ.DNS, vm.DNS...)
		} else {
			byType[vm.Job] = &Job{
				vm.Job,
				deployment,
				vm.ID,
				plan.ID,
				plan.Name,
				vm.ID + ".standalone.blacksmith." + deployment + ".bosh",
				vm.IPs,
				vm.DNS,
			}
		}
	}
	for _, job := range byType {
		jobs = append(jobs, job)
	}
	jobsYAML["jobs"] = jobs

	l.Debug("marshaling BOSH VM information")
	jobsMarshal, err := yaml.Marshal(jobsYAML)
	if err != nil {
		l.Error("failed to marshal BOSH VM information (for credentials.yml merge): %s", err)
		return nil, err
	}
	l.Debug("converting BOSH VM information to YAML")
	yamlJobs, err := simpleyaml.NewYaml(jobsMarshal)
	if err != nil {
		l.Error("failed to convert BOSH VM information to YAML (for credentials.yml merge): %s", err)
		return nil, err
	}
	jobsIfc, err := yamlJobs.Map()
	l.Debug("parsing BOSH VM information from YAML (don't ask)")
	if err != nil {
		l.Error("failed to parse BOSH VM information from YAML (for credentials.yml merge): %s", err)
		return nil, err
	}

	l.Debug("merging service deployment manifest with credentials.yml (for retrieve/bind)")

	defaults := make(map[interface{}]interface{})
	defaults["name"] = deployment

	params := make(map[interface{}]interface{})
	params["instance_id"] = id
	params["hostname"] = dnsname

	manifest, err := GenManifest(plan, jobsIfc, plan.Credentials, defaults, wrap("params", params))
	if err != nil {
		l.Error("failed to merge service deployment manifest with credentials.yml: %s", err)
		return nil, err
	}

	l.Debug("parsing merged YAML super-structure, to retrieve `credentials' top-level key")
	yamlManifest, err := simpleyaml.NewYaml([]byte(manifest))
	if err != nil {
		l.Error("failed to parse merged YAML; unable to retrieve credentials for bind: %s", err)
		return nil, err
	}

	l.Debug("retrieving `credentials' top-level key, to return to the caller")
	yamlCreds := yamlManifest.Get("credentials")
	yamlMap, err := yamlCreds.Map()
	if err != nil {
		l.Error("failed to retrieve `credentials' top-level key: %s", err)
		return nil, err
	}

	return deinterfaceMap(yamlMap), nil
}
