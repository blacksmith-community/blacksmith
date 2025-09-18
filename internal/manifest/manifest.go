package manifest

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/services"
	"blacksmith/pkg/logger"
	"blacksmith/pkg/utils"
	"github.com/geofffranks/spruce"
	"github.com/smallfish/simpleyaml"
	"gopkg.in/yaml.v3"
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

func InitManifest(ctx context.Context, plan services.Plan, instanceID string) error {
	logger.Get().Info("Initializing manifest for plan %s, instance %s", plan.ID, instanceID)

	err := validateInitScript(plan.InitScriptPath)
	if err != nil {
		return err
	}

	err = prepareInitScript(plan.InitScriptPath)
	if err != nil {
		return err
	}

	cmd, err := buildInitCommand(ctx, plan, instanceID)
	if err != nil {
		return err
	}

	return executeInitScript(cmd, plan.InitScriptPath)
}

func validateInitScript(initScriptPath string) error {
	_, err := os.Stat(initScriptPath)
	if err != nil && os.IsNotExist(err) {
		logger.Get().Debug("No init script found at %s, skipping initialization", initScriptPath)

		return nil
	}

	err = utils.ValidateExecutablePath(initScriptPath)
	if err != nil {
		logger.Get().Error("init script path validation failed: %s", err)

		return fmt.Errorf("init script path validation failed: %w", err)
	}

	return nil
}

func prepareInitScript(initScriptPath string) error {
	logger.Get().Info("Running init script: %s", initScriptPath)

	err := os.Chmod(initScriptPath, ExecutableScriptPermissions /* #nosec G302 - Scripts need execute permission, path validated above */)
	if err != nil {
		logger.Get().Error("failed to make init script executable: %s", err)

		return fmt.Errorf("failed to chmod init script: %w", err)
	}

	return nil
}

func buildInitCommand(ctx context.Context, plan services.Plan, instanceID string) (*exec.Cmd, error) {
	ctx, cancel := context.WithTimeout(ctx, InitScriptTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "/bin/bash", plan.InitScriptPath) // #nosec G204 - Path has been validated by validateExecutablePath

	cmd.Env = buildInitEnvironment(instanceID, plan.ID)

	logger.Get().Debug("Executing init script command: bash %s", plan.InitScriptPath)
	logger.Get().Debug("Init script environment variables:")

	for _, env := range cmd.Env {
		logger.Get().Debug("  %s", env)
	}

	return cmd, nil
}

func buildInitEnvironment(instanceID, planID string) []string {
	env := os.Environ()

	// Add safe binary to PATH for init scripts
	for i, envVar := range env {
		if strings.HasPrefix(envVar, "PATH=") {
			env[i] = envVar + ":/var/vcap/packages/safe/bin"

			break
		}
	}

	env = append(env, "CREDENTIALS=secret/"+instanceID)
	env = append(env, fmt.Sprintf("RAWJSONFILE=%s%s.json", GetWorkDir(), instanceID))
	env = append(env, fmt.Sprintf("YAMLFILE=%s%s.yml", GetWorkDir(), instanceID))
	env = append(env, "BLACKSMITH_INSTANCE_DATA_DIR="+GetWorkDir())
	env = append(env, "INSTANCE_ID="+instanceID)
	env = append(env, "BLACKSMITH_PLAN="+planID)

	return env
}

func executeInitScript(cmd *exec.Cmd, initScriptPath string) error {
	out, err := cmd.CombinedOutput()
	if err != nil {
		logger.Get().Error("Init script failed: %s", err)
		logger.Get().Debug("Init script output:\n%s", string(out))

		return fmt.Errorf("init script execution failed: %w", err)
	}

	logger.Get().Info("Init script completed successfully")
	logger.Get().Debug("Init script `%s' output:\n%s", initScriptPath, string(out))

	return nil
}

func GenManifest(p services.Plan, manifests ...map[interface{}]interface{}) (string, error) {
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

func UploadReleasesFromManifest(raw string, director bosh.Director, loggerInstance logger.Logger) error {
	manifest, err := parseManifestReleases(raw)
	if err != nil {
		return err
	}

	existingReleases, err := getExistingReleases(director, loggerInstance)
	if err != nil {
		return err
	}

	return uploadMissingReleases(manifest.Releases, existingReleases, director, loggerInstance)
}

type manifestReleases struct {
	Releases []struct {
		Name    string `yaml:"name"`
		Version string `yaml:"version"`
		URL     string `yaml:"url"`
		SHA1    string `yaml:"sha1"`
	} `yaml:"releases"`
}

func parseManifestReleases(raw string) (*manifestReleases, error) {
	var manifest manifestReleases

	err := yaml.Unmarshal([]byte(raw), &manifest)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal manifest for release extraction: %w", err)
	}

	return &manifest, nil
}

func getExistingReleases(director bosh.Director, loggerInstance logger.Logger) (map[string]bool, error) {
	loggerInstance.Debug("enumerating uploaded BOSH releases")

	boshReleases, err := director.GetReleases()
	if err != nil {
		return nil, fmt.Errorf("failed to get BOSH releases: %w", err)
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

	logExistingReleases(releases, loggerInstance)

	return have, nil
}

func logExistingReleases(releases []string, loggerInstance logger.Logger) {
	if len(releases) > 0 {
		releasesJSON, err := json.Marshal(map[string]interface{}{
			"count":    len(releases),
			"releases": releases,
		})
		if err != nil {
			loggerInstance.Error("failed to marshal releases for debugging: %s", err)
		} else {
			loggerInstance.Debug("found BOSH releases: %s", string(releasesJSON))
		}
	}
}

func uploadMissingReleases(releases []struct {
	Name    string `yaml:"name"`
	Version string `yaml:"version"`
	URL     string `yaml:"url"`
	SHA1    string `yaml:"sha1"`
}, existingReleases map[string]bool, director bosh.Director, loggerInstance logger.Logger) error {
	loggerInstance.Debug("determining which BOSH releases need uploaded")

	for _, release := range releases {
		err := processRelease(release, existingReleases, director, loggerInstance)
		if err != nil {
			return err
		}
	}

	return nil
}

func processRelease(release struct {
	Name    string `yaml:"name"`
	Version string `yaml:"version"`
	URL     string `yaml:"url"`
	SHA1    string `yaml:"sha1"`
}, existingReleases map[string]bool, director bosh.Director, loggerInstance logger.Logger) error {
	releaseKey := release.Name + "/" + release.Version

	switch {
	case existingReleases[releaseKey]:
		loggerInstance.Debug("already have %s/%s; skipping upload", release.Name, release.Version)
	case release.URL == "":
		loggerInstance.Debug("%s/%s is missing either its URL; skipping upload", release.Name, release.Version)
	case release.SHA1 == "":
		loggerInstance.Debug("%s/%s is missing either its SHA1 checksum; skipping upload", release.Name, release.Version)
	default:
		loggerInstance.Debug("uploading BOSH release %s/%s from %s (sha1 %s)...", release.Name, release.Version, release.URL, release.SHA1)

		_, err := director.UploadRelease(release.URL, release.SHA1)
		if err != nil {
			return fmt.Errorf("failed to upload BOSH release %s/%s: %w", release.Name, release.Version, err)
		}
	}

	return nil
}

func GetCreds(instanceID string, plan services.Plan, director bosh.Director, loggerInstance logger.Logger) (interface{}, error) {
	deployment := plan.ID + "-" + instanceID

	vms, err := getDeploymentVMs(director, deployment, loggerInstance)
	if err != nil {
		return nil, err
	}

	err = setCredentialsEnv(instanceID, loggerInstance)
	if err != nil {
		return nil, err
	}

	jobs, dnsname := buildJobsFromVMs(vms, plan, deployment, loggerInstance)

	jobsIfc, err := marshalJobsToInterface(jobs, loggerInstance)
	if err != nil {
		return nil, err
	}

	manifest, err := generateCredentialsManifest(plan, jobsIfc, deployment, instanceID, dnsname, loggerInstance)
	if err != nil {
		return nil, err
	}

	return extractCredentialsFromManifest(manifest, loggerInstance)
}

func getDeploymentVMs(director bosh.Director, deployment string, loggerInstance logger.Logger) ([]bosh.VM, error) {
	loggerInstance.Debug("looking up BOSH VM information for %s", deployment)

	vms, err := director.GetDeploymentVMs(deployment)
	if err != nil {
		loggerInstance.Error("failed to retrieve BOSH VM information for %s: %s", deployment, err)

		return nil, fmt.Errorf("failed to get deployment VMs: %w", err)
	}

	return vms, nil
}

func setCredentialsEnv(instanceID string, loggerInstance logger.Logger) error {
	err := os.Setenv("CREDENTIALS", "secret/"+instanceID)
	if err != nil {
		loggerInstance.Error("failed to set CREDENTIALS environment variable: %s", err)

		return fmt.Errorf("failed to set CREDENTIALS env var: %w", err)
	}

	return nil
}

func buildJobsFromVMs(vms []bosh.VM, plan services.Plan, deployment string, loggerInstance logger.Logger) ([]*services.Job, string) {
	jobs := make([]*services.Job, 0, len(vms))
	byType := make(map[string]*services.Job)

	var dnsname string

	network := os.Getenv("BOSH_NETWORK")
	networkDNS := strings.ReplaceAll(network, ".", "")

	for _, instance := range vms {
		job := createJobFromVM(instance, plan, deployment, networkDNS)
		loggerInstance.Debug("found job {name: %s, deployment: %s, id: %s, plan_id: %s, plan_name: %s, fqdn: %s, ips: [%s], dns: [%s]", job.Name, job.Deployment, job.ID, job.PlanID, job.PlanName, job.FQDN, strings.Join(instance.IPs, ", "), strings.Join(instance.DNS, ", "))
		dnsname = job.FQDN

		jobs = append(jobs, &job)

		updateJobsByType(byType, instance, plan, deployment)
	}

	for _, job := range byType {
		jobs = append(jobs, job)
	}

	return jobs, dnsname
}

func createJobFromVM(instance bosh.VM, plan services.Plan, deployment, networkDNS string) services.Job {
	return services.Job{
		Name:       instance.Job + "/" + strconv.Itoa(instance.Index),
		Deployment: deployment,
		ID:         instance.ID,
		PlanID:     plan.ID,
		PlanName:   plan.Name,
		FQDN:       instance.ID + "." + plan.Type + "." + networkDNS + "." + deployment + ".bosh",
		IPs:        instance.IPs,
		DNS:        instance.DNS,
	}
}

func updateJobsByType(byType map[string]*services.Job, instance bosh.VM, plan services.Plan, deployment string) {
	if typ, ok := byType[instance.Job]; ok {
		typ.IPs = append(typ.IPs, instance.IPs...)
		typ.DNS = append(typ.DNS, instance.DNS...)
	} else {
		byType[instance.Job] = &services.Job{
			Name:       instance.Job,
			Deployment: deployment,
			ID:         instance.ID,
			PlanID:     plan.ID,
			PlanName:   plan.Name,
			FQDN:       instance.ID + ".standalone.blacksmith." + deployment + ".bosh",
			IPs:        instance.IPs,
			DNS:        instance.DNS,
		}
	}
}

func marshalJobsToInterface(jobs []*services.Job, loggerInstance logger.Logger) (map[interface{}]interface{}, error) {
	jobsYAML := map[string][]*services.Job{"jobs": jobs}

	loggerInstance.Debug("marshaling BOSH VM information")

	jobsMarshal, err := yaml.Marshal(jobsYAML)
	if err != nil {
		loggerInstance.Error("failed to marshal BOSH VM information (for credentials.yml merge): %s", err)

		return nil, fmt.Errorf("failed to marshal jobs YAML: %w", err)
	}

	loggerInstance.Debug("converting BOSH VM information to YAML")

	yamlJobs, err := simpleyaml.NewYaml(jobsMarshal)
	if err != nil {
		loggerInstance.Error("failed to convert BOSH VM information to YAML (for credentials.yml merge): %s", err)

		return nil, fmt.Errorf("failed to create simpleyaml from jobs: %w", err)
	}

	loggerInstance.Debug("parsing BOSH VM information from YAML (don't ask)")

	jobsIfc, err := yamlJobs.Map()
	if err != nil {
		loggerInstance.Error("failed to parse BOSH VM information from YAML (for credentials.yml merge): %s", err)

		return nil, fmt.Errorf("failed to get jobs map: %w", err)
	}

	return jobsIfc, nil
}

func generateCredentialsManifest(plan services.Plan, jobsIfc map[interface{}]interface{}, deployment, instanceID, dnsname string, loggerInstance logger.Logger) (string, error) {
	loggerInstance.Debug("merging service deployment manifest with credentials.yml (for retrieve/bind)")

	defaults := map[interface{}]interface{}{
		"name": deployment,
	}

	params := map[interface{}]interface{}{
		"instance_id": instanceID,
		"hostname":    dnsname,
	}

	manifest, err := GenManifest(plan, jobsIfc, plan.Credentials, defaults, utils.Wrap("params", params))
	if err != nil {
		loggerInstance.Error("failed to merge service deployment manifest with credentials.yml: %s", err)

		return "", err
	}

	return manifest, nil
}

func extractCredentialsFromManifest(manifest string, loggerInstance logger.Logger) (interface{}, error) {
	loggerInstance.Debug("parsing merged YAML super-structure, to retrieve `credentials' top-level key")

	yamlManifest, err := simpleyaml.NewYaml([]byte(manifest))
	if err != nil {
		loggerInstance.Error("failed to parse merged YAML; unable to retrieve credentials for bind: %s", err)

		return nil, fmt.Errorf("failed to parse manifest YAML: %w", err)
	}

	loggerInstance.Debug("retrieving `credentials' top-level key, to return to the caller")

	yamlCreds := yamlManifest.Get("credentials")

	yamlMap, err := yamlCreds.Map()
	if err != nil {
		loggerInstance.Error("failed to retrieve `credentials' top-level key: %s", err)

		return nil, fmt.Errorf("failed to get credentials map: %w", err)
	}

	return utils.DeinterfaceMap(yamlMap), nil
}
