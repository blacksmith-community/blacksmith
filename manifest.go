package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"blacksmith/bosh"
	"blacksmith/pkg/logger"
	"github.com/geofffranks/spruce"
	"github.com/smallfish/simpleyaml"
	"gopkg.in/yaml.v2"
)

const (
	// If BLACKSMITH_INSTANCE_DATA_DIR environment variable is not set, use this as a default.
	DefaultBlacksmithWorkDir = "/var/vcap/data/blacksmith/"

	// File permissions for executable scripts.
	ExecutableScriptPermissions = 0700

	// Timeout for init script execution.
	InitScriptTimeout = 5 * time.Minute
)

func GetWorkDir() string {
	var blacksmithWorkDir = os.Getenv("BLACKSMITH_INSTANCE_DATA_DIR")
	if blacksmithWorkDir == "" {
		blacksmithWorkDir = DefaultBlacksmithWorkDir
	}

	return blacksmithWorkDir
}

func InitManifest(ctx context.Context, p Plan, instanceID string) error {
	logger.Get().Info("Initializing manifest for plan %s, instance %s", p.ID, instanceID)

	/* skip running the plan initialization script if it doesn't exist */
	if _, err := os.Stat(p.InitScriptPath); err != nil && os.IsNotExist(err) {
		logger.Get().Debug("No init script found at %s, skipping initialization", p.InitScriptPath)

		return nil
	}

	/* otherwise, execute it (chmodding to cut Forge authors some slack...) */
	logger.Get().Info("Running init script: %s", p.InitScriptPath)

	// Validate the executable path for security
	if err := validateExecutablePath(p.InitScriptPath); err != nil {
		logger.Get().Error("init script path validation failed: %s", err)

		return err
	}

	if err := os.Chmod(p.InitScriptPath, ExecutableScriptPermissions /* #nosec G302 - Scripts need execute permission, path validated above */); err != nil {
		logger.Get().Error("failed to make init script executable: %s", err)

		return fmt.Errorf("failed to chmod init script: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, InitScriptTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "/bin/bash", p.InitScriptPath) // #nosec G204 - Path has been validated by validateExecutablePath

	cmd.Env = os.Environ()
	/* TODO: put more environment variables here, as needed */

	// Add safe binary to PATH for init scripts
	for i, env := range cmd.Env {
		if strings.HasPrefix(env, "PATH=") {
			cmd.Env[i] = env + ":/var/vcap/packages/safe/bin"

			break
		}
	}

	cmd.Env = append(cmd.Env, "CREDENTIALS=secret/"+instanceID)
	cmd.Env = append(cmd.Env, fmt.Sprintf("RAWJSONFILE=%s%s.json", GetWorkDir(), instanceID))
	cmd.Env = append(cmd.Env, fmt.Sprintf("YAMLFILE=%s%s.yml", GetWorkDir(), instanceID))
	cmd.Env = append(cmd.Env, "BLACKSMITH_INSTANCE_DATA_DIR="+GetWorkDir())
	cmd.Env = append(cmd.Env, "INSTANCE_ID="+instanceID)
	cmd.Env = append(cmd.Env, "BLACKSMITH_PLAN="+p.ID)
	// NOTE: BOSH_NETWORK is set in the env in config.go

	logger.Get().Debug("Executing init script command: bash %s", p.InitScriptPath)
	logger.Get().Debug("Init script environment variables:")

	for _, env := range cmd.Env {
		logger.Get().Debug("  %s", env)
	}

	out, err := cmd.CombinedOutput()
	if err != nil {
		logger.Get().Error("Init script failed: %s", err)
		logger.Get().Debug("Init script output:\n%s", string(out))

		return fmt.Errorf("init script execution failed: %w", err)
	}

	logger.Get().Info("Init script completed successfully")
	logger.Get().Debug("Init script `%s' output:\n%s", p.InitScriptPath, string(out))

	return nil
}

func GenManifest(p Plan, manifests ...map[interface{}]interface{}) (string, error) {
	logger.Get().Info("Generating manifest for plan %s", p.ID)
	logger.Get().Debug("Starting spruce merge with %d additional manifests", len(manifests))

	merged, err := spruce.Merge(p.Manifest)
	if err != nil {
		logger.Get().Error("Failed to merge base manifest: %s", err)

		return "", fmt.Errorf("failed to merge base manifest with spruce: %w", err)
	}

	for manifestIndex, next := range manifests {
		logger.Get().Debug("Merging manifest %d of %d", manifestIndex+1, len(manifests))

		merged, err = spruce.Merge(merged, next)
		if err != nil {
			logger.Get().Error("Failed to merge manifest %d: %s", manifestIndex+1, err)

			return "", fmt.Errorf("failed to merge manifest %d with spruce: %w", manifestIndex+1, err)
		}
	}

	logger.Get().Debug("Running spruce evaluator on merged manifest")

	eval := &spruce.Evaluator{Tree: merged}

	err = eval.Run(nil, nil)
	if err != nil {
		logger.Get().Error("Failed to evaluate spruce expressions: %s", err)

		return "", fmt.Errorf("failed to evaluate spruce expressions: %w", err)
	}

	final := eval.Tree

	manifestYAML, err := yaml.Marshal(final)
	if err != nil {
		logger.Get().Error("Failed to marshal final manifest to YAML: %s", err)

		return "", fmt.Errorf("failed to marshal final manifest to YAML: %w", err)
	}

	manifestStr := string(manifestYAML)
	logger.Get().Info("Successfully generated manifest (size: %d bytes)", len(manifestStr))

	return manifestStr, nil
}

func UploadReleasesFromManifest(raw string, director bosh.Director, l logger.Logger) error {
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
		return fmt.Errorf("failed to unmarshal manifest for release extraction: %w", err)
	}

	l.Debug("enumerating uploaded BOSH releases")

	boshReleases, err := director.GetReleases()
	if err != nil {
		return fmt.Errorf("failed to get BOSH releases: %w", err)
	}

	have := make(map[string]bool)
	releases := []string{}

	for _, rl := range boshReleases {
		for _, v := range rl.ReleaseVersions {
			releaseID := rl.Name + "/" + v.Version
			releases = append(releases, releaseID)
			have[releaseID] = true
		}
	}

	// Log all releases as a single JSON object
	if len(releases) > 0 {
		releasesJSON, err := json.Marshal(map[string]interface{}{
			"count":    len(releases),
			"releases": releases,
		})
		if err != nil {
			l.Error("failed to marshal releases for debugging: %s", err)
		} else {
			l.Debug("found BOSH releases: %s", string(releasesJSON))
		}
	}

	l.Debug("determining which BOSH releases need uploaded")

	for _, release := range manifest.Releases {
		switch {
		case have[release.Name+"/"+release.Version]:
			l.Debug("already have %s/%s; skipping upload", release.Name, release.Version)
		case release.URL == "":
			l.Debug("%s/%s is missing either its URL; skipping upload", release.Name, release.Version)
		case release.SHA1 == "":
			l.Debug("%s/%s is missing either its SHA1 checksum; skipping upload", release.Name, release.Version)
		default:
			l.Debug("uploading BOSH release %s/%s from %s (sha1 %s)...", release.Name, release.Version, release.URL, release.SHA1)

			_, err := director.UploadRelease(release.URL, release.SHA1)
			if err != nil {
				return fmt.Errorf("failed to upload BOSH release %s/%s: %w", release.Name, release.Version, err)
			}
		}
	}

	return nil
}

func GetCreds(id string, plan Plan, director bosh.Director, l logger.Logger) (interface{}, error) {
	jobsYAML := make(map[string][]*Job)

	var dnsname string

	deployment := plan.ID + "-" + id

	l.Debug("looking up BOSH VM information for %s", deployment)

	vms, err := director.GetDeploymentVMs(deployment)
	if err != nil {
		l.Error("failed to retrieve BOSH VM information for %s: %s", deployment, err)

		return nil, fmt.Errorf("failed to get deployment VMs: %w", err)
	}

	jobs := make([]*Job, 0, len(vms))

	if err := os.Setenv("CREDENTIALS", "secret/"+id); err != nil {
		l.Error("failed to set CREDENTIALS environment variable: %s", err)

		return nil, fmt.Errorf("failed to set CREDENTIALS env var: %w", err)
	}

	byType := make(map[string]*Job)

	network := os.Getenv("BOSH_NETWORK")
	// Remove dots from network component for valid DNS names (matching the pattern in env.rb)
	networkDNS := strings.ReplaceAll(network, ".", "")

	for _, virtualMachine := range vms {
		l.Debug("virtualMachine.id: %s, virtualMachine.CID: %s", virtualMachine.ID, virtualMachine.CID)
		job := Job{
			virtualMachine.Job + "/" + strconv.Itoa(virtualMachine.Index),
			deployment,
			virtualMachine.ID,
			plan.ID,
			plan.Name,
			virtualMachine.ID + "." + plan.Type + "." + networkDNS + "." + deployment + ".bosh",
			virtualMachine.IPs,
			virtualMachine.DNS,
		}
		l.Debug("found job {name: %s, deployment: %s, id: %s, plan_id: %s, plan_name: %s, fqdn: %s, ips: [%s], dns: [%s]", job.Name, job.Deployment, job.ID, job.PlanID, job.PlanName, job.FQDN, strings.Join(virtualMachine.IPs, ", "), strings.Join(virtualMachine.DNS, ", "))
		dnsname = job.FQDN

		jobs = append(jobs, &job)

		if typ, ok := byType[virtualMachine.Job]; ok {
			typ.IPs = append(typ.IPs, virtualMachine.IPs...)
			typ.DNS = append(typ.DNS, virtualMachine.DNS...)
		} else {
			byType[virtualMachine.Job] = &Job{
				virtualMachine.Job,
				deployment,
				virtualMachine.ID,
				plan.ID,
				plan.Name,
				virtualMachine.ID + ".standalone.blacksmith." + deployment + ".bosh",
				virtualMachine.IPs,
				virtualMachine.DNS,
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

		return nil, fmt.Errorf("failed to marshal jobs YAML: %w", err)
	}

	l.Debug("converting BOSH VM information to YAML")

	yamlJobs, err := simpleyaml.NewYaml(jobsMarshal)
	if err != nil {
		l.Error("failed to convert BOSH VM information to YAML (for credentials.yml merge): %s", err)

		return nil, fmt.Errorf("failed to create simpleyaml from jobs: %w", err)
	}

	jobsIfc, err := yamlJobs.Map()

	l.Debug("parsing BOSH VM information from YAML (don't ask)")

	if err != nil {
		l.Error("failed to parse BOSH VM information from YAML (for credentials.yml merge): %s", err)

		return nil, fmt.Errorf("failed to get jobs map: %w", err)
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

		return nil, fmt.Errorf("failed to parse manifest YAML: %w", err)
	}

	l.Debug("retrieving `credentials' top-level key, to return to the caller")

	yamlCreds := yamlManifest.Get("credentials")

	yamlMap, err := yamlCreds.Map()
	if err != nil {
		l.Error("failed to retrieve `credentials' top-level key: %s", err)

		return nil, fmt.Errorf("failed to get credentials map: %w", err)
	}

	return deinterfaceMap(yamlMap), nil
}
