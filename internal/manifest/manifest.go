package manifest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/services"
	"blacksmith/pkg/logger"
	"blacksmith/pkg/utils"

	"github.com/geofffranks/spruce"
	"github.com/smallfish/simpleyaml"
	"gopkg.in/yaml.v3"
)

// spruceMu serializes calls to the spruce library which uses global state
// that is not thread-safe. This is necessary because spruce.SetupOperators()
// and spruce.Evaluator.Run() modify global variables without synchronization.
var spruceMu sync.Mutex

const (
	// If BLACKSMITH_INSTANCE_DATA_DIR environment variable is not set, use this as a default.
	DefaultBlacksmithWorkDir = "/var/vcap/data/blacksmith/"

	// File permissions for executable scripts.
	ExecutableScriptPermissions = 0700

	// Timeout for init script execution.
	InitScriptTimeout = 5 * time.Minute

	// releaseUploadDone is the state of an upload task that succeeded.
	releaseUploadDone = "done"
)

// How a provision looks for a plan release whose upload answer was lost.
// They are variables so a test can shorten them.
//
//nolint:gochecknoglobals // a test seam
var (
	// releasePollInterval is how long the provision waits between
	// listings of the director's releases.
	releasePollInterval = 30 * time.Second
	// releasePollDeadline is how long after the lost answer the provision
	// keeps listing, which matches the deadline of a boot upload.
	releasePollDeadline = 30 * time.Minute
)

// Error variables for err113 compliance.
var (
	ErrCredentialsFieldNotFound = errors.New("credentials field not found in manifest")
	ErrCredentialsNotMap        = errors.New("failed to get credentials map: credentials is a string, not a map")
	// ErrReleaseUploadFailed marks a plan release whose upload task did not
	// succeed and which the director still does not have.
	ErrReleaseUploadFailed = errors.New("BOSH release upload failed")
	// ErrNoReleaseUploadTask marks a release upload the director accepted
	// without naming a task to wait for.
	ErrNoReleaseUploadTask = errors.New("the director named no task for the BOSH release upload")
	// ErrReleaseUploadUnanswered marks a wait on a release upload task that
	// returned neither the task nor an error.
	ErrReleaseUploadUnanswered = errors.New("the wait on the BOSH release upload task returned no task")
)

func GetWorkDir() string {
	var blacksmithWorkDir = os.Getenv("BLACKSMITH_INSTANCE_DATA_DIR")
	if blacksmithWorkDir == "" {
		blacksmithWorkDir = DefaultBlacksmithWorkDir
	}

	return blacksmithWorkDir
}

func InitManifest(ctx context.Context, plan services.Plan, instanceID string) error {
	log := logger.Get().Named("manifest")
	log.Info("Initializing manifest for plan %s, instance %s", plan.ID, instanceID)

	err := validateInitScript(plan.InitScriptPath)
	if err != nil {
		return err
	}

	err = prepareInitScript(plan.InitScriptPath)
	if err != nil {
		return err
	}

	cmd := buildInitCommand(ctx, plan, instanceID)

	return executeInitScript(cmd, plan.InitScriptPath)
}

func validateInitScript(initScriptPath string) error {
	_, err := os.Stat(initScriptPath)
	if err != nil && os.IsNotExist(err) {
		logger.Get().Named("manifest").Debug("No init script found at %s, skipping initialization", initScriptPath)

		return nil
	}

	err = utils.ValidateExecutablePath(initScriptPath)
	if err != nil {
		logger.Get().Named("manifest").Error("init script path validation failed: %s", err)

		return fmt.Errorf("init script path validation failed: %w", err)
	}

	return nil
}

func prepareInitScript(initScriptPath string) error {
	log := logger.Get().Named("manifest")
	log.Info("Running init script: %s", initScriptPath)

	err := os.Chmod(initScriptPath, ExecutableScriptPermissions /* #nosec G302 - Scripts need execute permission, path validated above */)
	if err != nil {
		log.Error("failed to make init script executable: %s", err)

		return fmt.Errorf("failed to chmod init script: %w", err)
	}

	return nil
}

func buildInitCommand(ctx context.Context, plan services.Plan, instanceID string) *exec.Cmd {
	// Create command without timeout - timeout will be handled at a higher level if needed
	cmd := exec.CommandContext(ctx, "/bin/bash", plan.InitScriptPath) // #nosec G204 - Path has been validated by validateExecutablePath

	cmd.Env = buildInitEnvironment(instanceID, plan.ID)

	log := logger.Get().Named("manifest")
	log.Debug("Executing init script command: bash %s", plan.InitScriptPath)

	return cmd
}

func buildInitEnvironment(instanceID, planID string) []string {
	env := appendSafeBinaryToPath(os.Environ())
	env = appendVaultToken(env)
	env = appendBlacksmithVariables(env, instanceID, planID)

	log := logger.Get().Named("manifest")
	logEnvironmentVariables(env, log)
	validateCriticalEnvironment(env, log)

	return env
}

func appendSafeBinaryToPath(env []string) []string {
	for i, envVar := range env {
		if strings.HasPrefix(envVar, "PATH=") {
			env[i] = envVar + ":/var/vcap/packages/safe/bin"

			break
		}
	}

	return env
}

func appendVaultToken(env []string) []string {
	if vaultToken := os.Getenv("VAULT_TOKEN"); vaultToken != "" {
		env = append(env, "VAULT_TOKEN="+vaultToken)
	}

	return env
}

func appendBlacksmithVariables(env []string, instanceID, planID string) []string {
	return append(env,
		"CREDENTIALS=secret/"+instanceID,
		fmt.Sprintf("RAWJSONFILE=%s%s.json", GetWorkDir(), instanceID),
		fmt.Sprintf("YAMLFILE=%s%s.yml", GetWorkDir(), instanceID),
		"BLACKSMITH_INSTANCE_DATA_DIR="+GetWorkDir(),
		"INSTANCE_ID="+instanceID,
		"BLACKSMITH_PLAN="+planID,
	)
}

func logEnvironmentVariables(env []string, log logger.Logger) {
	for _, envVar := range env {
		switch {
		case strings.HasPrefix(envVar, "VAULT_ADDR="):
			log.Debug("Environment: %s", envVar)
		case strings.HasPrefix(envVar, "VAULT_TOKEN="):
			log.Debug("Environment: VAULT_TOKEN=<redacted>")
		case strings.HasPrefix(envVar, "BOSH_CLIENT_SECRET="):
			log.Debug("Environment: BOSH_CLIENT_SECRET=<redacted>")
		case strings.HasPrefix(envVar, "BOSH_ENVIRONMENT="), strings.HasPrefix(envVar, "BOSH_CLIENT="):
			log.Debug("Environment: %s", envVar)
		}
	}
}

func validateCriticalEnvironment(env []string, log logger.Logger) {
	hasVaultAddr, hasVaultToken := checkVaultEnvironment(env)

	if !hasVaultAddr {
		log.Error("WARNING: VAULT_ADDR environment variable is not set")
	}

	if !hasVaultToken {
		log.Error("WARNING: VAULT_TOKEN environment variable is not set - init scripts may fail to authenticate with Vault")
	}
}

func checkVaultEnvironment(env []string) (bool, bool) {
	var hasAddr, hasToken bool

	for _, envVar := range env {
		if strings.HasPrefix(envVar, "VAULT_ADDR=") && len(envVar) > len("VAULT_ADDR=") {
			hasAddr = true
		}

		if strings.HasPrefix(envVar, "VAULT_TOKEN=") && len(envVar) > len("VAULT_TOKEN=") {
			hasToken = true
		}
	}

	return hasAddr, hasToken
}

func executeInitScript(cmd *exec.Cmd, initScriptPath string) error {
	out, err := cmd.CombinedOutput()
	if err != nil {
		logger.Get().Named("manifest").Error("Init script failed: %s", err)
		logger.Get().Named("manifest").Debug("Init script output:\n%s", redactEnvValues(string(out), cmd.Env, "VAULT_TOKEN", "BOSH_CLIENT_SECRET"))

		return fmt.Errorf("init script execution failed: %w", err)
	}

	logger.Get().Named("manifest").Info("Init script completed successfully")
	logger.Get().Named("manifest").Debug("Init script `%s' produced %d bytes of output (not logged because it can echo credentials)", initScriptPath, len(out))

	return nil
}

// redactEnvValues replaces every occurrence of the named environment
// variables' values in text, so a failed init script's output can be logged
// without echoing the broker's own credentials.
func redactEnvValues(text string, env []string, names ...string) string {
	for _, name := range names {
		prefix := name + "="

		for _, entry := range env {
			value, found := strings.CutPrefix(entry, prefix)
			if found && value != "" {
				text = strings.ReplaceAll(text, value, "<redacted>")
			}
		}
	}

	return text
}

func GenManifest(p services.Plan, manifests ...map[interface{}]interface{}) (string, error) {
	log := logger.Get().Named("manifest")
	log.Info("Generating manifest for plan %s", p.ID)
	log.Debug("Starting spruce merge with %d additional manifests", len(manifests))

	// Serialize spruce operations because the library uses global state
	// that is not thread-safe (StaticIPOperator.Setup, Evaluator globals).
	spruceMu.Lock()
	defer spruceMu.Unlock()

	merged, err := spruce.Merge(p.Manifest)
	if err != nil {
		log.Error("Failed to merge base manifest: %s", err)

		return "", fmt.Errorf("failed to merge base manifest with spruce: %w", err)
	}

	for manifestIndex, next := range manifests {
		log.Debug("Merging manifest %d of %d", manifestIndex+1, len(manifests))

		merged, err = spruce.Merge(merged, next)
		if err != nil {
			log.Error("Failed to merge manifest %d: %s", manifestIndex+1, err)

			return "", fmt.Errorf("failed to merge manifest %d with spruce: %w", manifestIndex+1, err)
		}
	}

	log.Debug("Running spruce evaluator on merged manifest")

	eval := &spruce.Evaluator{Tree: merged}

	err = eval.Run(nil, nil)
	if err != nil {
		log.Error("Failed to evaluate spruce expressions: %s", err)

		return "", fmt.Errorf("failed to evaluate spruce expressions: %w", err)
	}

	final := eval.Tree

	manifestYAML, err := yaml.Marshal(final)
	if err != nil {
		log.Error("Failed to marshal final manifest to YAML: %s", err)

		return "", fmt.Errorf("failed to marshal final manifest to YAML: %w", err)
	}

	manifestStr := string(manifestYAML)
	log.Info("Successfully generated manifest (size: %d bytes)", len(manifestStr))

	return manifestStr, nil
}

func UploadReleasesFromManifest(ctx context.Context, raw string, director bosh.Director, loggerInstance logger.Logger, progress func(message string)) error {
	manifest, err := parseManifestReleases(raw)
	if err != nil {
		return err
	}

	existingReleases, err := getExistingReleases(ctx, director, loggerInstance)
	if err != nil {
		return err
	}

	return uploadMissingReleases(ctx, manifest.Releases, existingReleases, director, loggerInstance, progress)
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

func getExistingReleases(ctx context.Context, director bosh.Director, loggerInstance logger.Logger) (map[string]bool, error) {
	loggerInstance.Debug("enumerating uploaded BOSH releases")

	boshReleases, err := director.GetReleases(ctx)
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

func uploadMissingReleases(ctx context.Context, releases []struct {
	Name    string `yaml:"name"`
	Version string `yaml:"version"`
	URL     string `yaml:"url"`
	SHA1    string `yaml:"sha1"`
}, existingReleases map[string]bool, director bosh.Director, loggerInstance logger.Logger, progress func(message string)) error {
	loggerInstance.Debug("determining which BOSH releases need uploaded")

	for _, release := range releases {
		err := processRelease(ctx, release, existingReleases, director, loggerInstance, progress)
		if err != nil {
			return err
		}
	}

	return nil
}

func processRelease(ctx context.Context, release struct {
	Name    string `yaml:"name"`
	Version string `yaml:"version"`
	URL     string `yaml:"url"`
	SHA1    string `yaml:"sha1"`
}, existingReleases map[string]bool, director bosh.Director, loggerInstance logger.Logger, progress func(message string)) error {
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

		task, err := director.UploadRelease(ctx, release.URL, release.SHA1)

		var unknown *bosh.OutcomeUnknownError
		if errors.As(err, &unknown) {
			return awaitLostReleaseUpload(ctx, release.Name, release.Version, release.URL, unknown, director, loggerInstance, progress)
		}

		if err != nil {
			return fmt.Errorf("failed to upload BOSH release %s/%s: %w", release.Name, release.Version, err)
		}

		return awaitReleaseUpload(ctx, release.Name, release.Version, release.URL, task, director, loggerInstance, progress)
	}

	return nil
}

// awaitReleaseUpload waits for the director task that uploads a plan's
// release, so the deploy that follows finds the release on the director.
// A failed upload task is forgiven when the release is on the director
// anyway, because a concurrent provision uploaded the same one. A wait that
// stops first leaves the upload task running and fails the provision, and
// a retried provision finds the release once the upload lands.
func awaitReleaseUpload(ctx context.Context, name, version, url string, task *bosh.Task, director bosh.Director, loggerInstance logger.Logger, progress func(message string)) error {
	if task == nil || task.ID <= 0 {
		return fmt.Errorf("%w: BOSH release %s/%s from %s", ErrNoReleaseUploadTask, name, version, url)
	}

	loggerInstance.Info("BOSH task %d is uploading release %s/%s from %s; waiting for it to finish", task.ID, name, version, url)

	if progress != nil {
		progress(fmt.Sprintf("Uploading BOSH release %s/%s (task %d)", name, version, task.ID))
	}

	finished, err := director.WaitForTask(ctx, task.ID)
	if err != nil {
		loggerInstance.Error("stopped waiting for BOSH task %d, which uploads release %s/%s, and left it running: %s", task.ID, name, version, err)

		return fmt.Errorf("stopped waiting for BOSH task %d, which uploads release %s/%s from %s, so the upload task was left running, "+
			"and a retried provision finds the release once the upload lands: %w", task.ID, name, version, url, err)
	}

	if finished == nil {
		return fmt.Errorf("%w: BOSH task %d, which uploads release %s/%s from %s", ErrReleaseUploadUnanswered, task.ID, name, version, url)
	}

	if finished.State == releaseUploadDone {
		loggerInstance.Info("BOSH task %d uploaded release %s/%s", task.ID, name, version)

		return nil
	}

	present, listErr := releasePresent(ctx, director, name, version)
	if listErr != nil {
		loggerInstance.Error("BOSH task %d, which uploads release %s/%s, ended in state %s, and the releases could not be listed again: %s", task.ID, name, version, finished.State, listErr)

		return fmt.Errorf("%w: BOSH release %s/%s from %s, BOSH task %d ended in state %s: %s; the releases could not be listed again to check for it: %w",
			ErrReleaseUploadFailed, name, version, url, task.ID, finished.State, finished.Result, listErr)
	}

	if present {
		loggerInstance.Warn("BOSH task %d, which uploads release %s/%s, ended in state %s (%s), but the director now has the release, "+
			"most likely from a concurrent upload of the same release, so the provision goes on", task.ID, name, version, finished.State, finished.Result)

		return nil
	}

	loggerInstance.Error("BOSH task %d, which uploads release %s/%s from %s, ended in state %s: %s", task.ID, name, version, url, finished.State, finished.Result)

	return fmt.Errorf("%w: BOSH release %s/%s from %s, BOSH task %d ended in state %s: %s",
		ErrReleaseUploadFailed, name, version, url, task.ID, finished.State, finished.Result)
}

// awaitLostReleaseUpload looks for a plan's release after the answer to its
// upload was lost, because the director may have accepted the upload. It
// lists the director's releases at once and then every
// releasePollInterval, and the deploy goes ahead as soon as the release is
// listed. A failed listing is logged, and the poll goes on. When
// releasePollDeadline passes first, the provision fails, and the upload may
// still be running. When ctx ends first, the provision stops, and a retried
// provision finds the release once the upload lands.
func awaitLostReleaseUpload(ctx context.Context, name, version, url string, unknown *bosh.OutcomeUnknownError,
	director bosh.Director, loggerInstance logger.Logger, progress func(message string),
) error {
	deadline := time.Now().Add(releasePollDeadline)

	loggerInstance.Info("the answer to the upload of BOSH release %s/%s from %s was lost, so the director may have accepted it; "+
		"listing the director's releases every %s until %s for it: %s", name, version, url, releasePollInterval, deadline.UTC().Format(time.RFC3339), unknown)

	if progress != nil {
		progress(fmt.Sprintf("Waiting for BOSH release %s/%s, whose upload answer was lost", name, version))
	}

	var lastListErr error

	for {
		present, err := releasePresent(ctx, director, name, version)

		switch {
		case err == nil && present:
			loggerInstance.Warn("the answer to the upload of BOSH release %s/%s from %s was lost, and the director now lists the release, "+
				"so the provision goes on", name, version, url)

			return nil
		case err != nil && ctx.Err() == nil:
			lastListErr = err

			loggerInstance.Error("listing the director's releases to look for BOSH release %s/%s, whose upload answer was lost, failed: %s", name, version, err)
		}

		if ctx.Err() != nil {
			return lostUploadStopped(ctx, name, version, url, unknown, loggerInstance)
		}

		wait := time.Until(deadline)
		if wait <= 0 {
			return lostUploadDeadlinePassed(name, version, url, deadline, unknown, lastListErr, loggerInstance)
		}

		timer := time.NewTimer(min(releasePollInterval, wait))

		select {
		case <-ctx.Done():
			timer.Stop()

			return lostUploadStopped(ctx, name, version, url, unknown, loggerInstance)
		case <-timer.C:
		}
	}
}

// lostUploadStopped is the error of a provision whose context ended while it
// looked for a release whose upload answer was lost.
func lostUploadStopped(ctx context.Context, name, version, url string, unknown *bosh.OutcomeUnknownError, loggerInstance logger.Logger) error {
	ctxDeadline, _ := ctx.Deadline()

	loggerInstance.Error("stopped looking for BOSH release %s/%s, whose upload answer was lost, because the provision ended: %s", name, version, ctx.Err())

	return fmt.Errorf("stopped looking for BOSH release %s/%s from %s, whose upload answer was lost, so the upload may still be running, "+
		"and a retried provision finds the release once the upload lands: %w", name, version, url, &bosh.DirectorCallError{
		Operation: unknown.Operation,
		Director:  unknown.Director,
		Deadline:  ctxDeadline,
		Cause:     ctx.Err(),
		Err:       unknown,
	})
}

// lostUploadDeadlinePassed is the error of a provision that looked for a
// release whose upload answer was lost until releasePollDeadline passed.
// lastListErr is the last listing that failed, or nil.
func lostUploadDeadlinePassed(name, version, url string, deadline time.Time, unknown *bosh.OutcomeUnknownError, lastListErr error, loggerInstance logger.Logger) error {
	listed := ""
	if lastListErr != nil {
		listed = fmt.Sprintf("; the last listing of the director's releases that failed said: %s", lastListErr)
	}

	loggerInstance.Error("BOSH release %s/%s, whose upload answer was lost, was not on the director by %s, so the provision fails, "+
		"and the upload may still be running%s", name, version, deadline.UTC().Format(time.RFC3339), listed)

	return fmt.Errorf("the answer to the upload of BOSH release %s/%s from %s was lost, and the director did not list the release within %s, "+
		"so the upload may still be running; check /b/tasks for it, and retry the provision once it lands%s: %w",
		name, version, url, releasePollDeadline, listed, &bosh.DirectorCallError{
			Operation: unknown.Operation,
			Director:  unknown.Director,
			Deadline:  deadline,
			Cause:     context.DeadlineExceeded,
			Err:       unknown,
		})
}

// releasePresent reports whether the director has release name at version.
func releasePresent(ctx context.Context, director bosh.Director, name, version string) (bool, error) {
	releases, err := director.GetReleases(ctx)
	if err != nil {
		return false, fmt.Errorf("failed to get BOSH releases: %w", err)
	}

	for _, release := range releases {
		if release.Name != name {
			continue
		}

		for _, releaseVersion := range release.ReleaseVersions {
			if releaseVersion.Version == version {
				return true, nil
			}
		}
	}

	return false, nil
}

func GetCreds(ctx context.Context, instanceID string, plan services.Plan, director bosh.Director, deploymentManifest string, loggerInstance logger.Logger) (interface{}, error) {
	deployment := plan.ID + "-" + instanceID

	vms, err := getDeploymentVMs(ctx, director, deployment, loggerInstance)
	if err != nil {
		return nil, err
	}

	err = setCredentialsEnv(instanceID, loggerInstance)
	if err != nil {
		return nil, err
	}

	networkByJob := extractNetworksByJob(deploymentManifest)

	jobs, dnsname := buildJobsFromVMs(vms, plan, deployment, networkByJob, loggerInstance)

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

func getDeploymentVMs(ctx context.Context, director bosh.Director, deployment string, loggerInstance logger.Logger) ([]bosh.VM, error) {
	loggerInstance.Debug("looking up BOSH VM information for %s", deployment)

	vms, err := director.GetDeploymentVMs(ctx, deployment)
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

// extractNetworksByJob parses a BOSH deployment manifest and returns a map of instance group name -> network name.
func extractNetworksByJob(manifestYAML string) map[string]string {
	result := make(map[string]string)

	if manifestYAML == "" {
		return result
	}

	var manifest struct {
		InstanceGroups []struct {
			Name     string `yaml:"name"`
			Networks []struct {
				Name string `yaml:"name"`
			} `yaml:"networks"`
		} `yaml:"instance_groups"`
	}

	if err := yaml.Unmarshal([]byte(manifestYAML), &manifest); err != nil {
		return result
	}

	for _, ig := range manifest.InstanceGroups {
		if len(ig.Networks) > 0 {
			// Use the first network for each instance group
			result[ig.Name] = ig.Networks[0].Name
		}
	}

	return result
}

func buildJobsFromVMs(vms []bosh.VM, plan services.Plan, deployment string, networkByJob map[string]string, loggerInstance logger.Logger) ([]*services.Job, string) {
	jobs := make([]*services.Job, 0, len(vms))
	byType := make(map[string]*services.Job)

	var dnsname string

	// Fallback to BOSH_NETWORK if the manifest doesn't have network info for this job
	defaultNetwork := os.Getenv("BOSH_NETWORK")

	for _, instance := range vms {
		// Use network from manifest (via networkByJob map) or fallback to default
		network := networkByJob[instance.Job]
		if network == "" {
			network = defaultNetwork
			loggerInstance.Debug("VM %s has no network info in manifest, using default BOSH_NETWORK: %s", instance.ID, network)
		} else {
			loggerInstance.Debug("VM %s using network from manifest: %s", instance.ID, network)
		}

		// Remove dots from network component for valid DNS names (matching the pattern in env.rb)
		networkDNS := strings.ReplaceAll(network, ".", "")

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

	// Check if credentials exists and is not nil
	if yamlCreds == nil {
		loggerInstance.Error("credentials field is missing from manifest")

		return nil, ErrCredentialsFieldNotFound
	}

	// Try to get as map, but handle gracefully if it's not a map
	yamlMap, err := yamlCreds.Map()
	if err != nil {
		// If it's not a map, check if it's a different type we can handle
		str, strErr := yamlCreds.String()
		if strErr == nil {
			loggerInstance.Error("failed to retrieve `credentials' top-level key: got string instead of map (string of %d bytes, value not logged)", len(str))

			return nil, ErrCredentialsNotMap
		}

		loggerInstance.Error("failed to retrieve `credentials' top-level key: %s", err)

		return nil, fmt.Errorf("failed to get credentials map: %w", err)
	}

	return utils.DeinterfaceMap(yamlMap), nil
}
